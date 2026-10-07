package lspserver

import (
	"context"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/jfetkotto/sigils/internal/document"
	"github.com/jfetkotto/sigils/internal/workspace"
)

// buildIndex enumerates every source file the given discoverer resolves
// and scans each for declarations, returning what it found so the caller
// can arm file watches against the same set. It's kicked off in the
// background from Initialize -- a real company workspace's file set can be
// large enough that blocking the LSP handshake on it would hang the
// editor.
//
// If discoverer is a workspace.FilelistProvider, its captured `+incdir+`/
// `+define+` are (re)wired into the index before scanning, so this pass's
// own scans resolve `include and `ifdef against the current configuration
// -- done on every call, not just once, since a filelist edit can change
// either between rebuilds. Wired in *after* the discovery loop below, not
// before: a FilelistProvider's IncludeDirs/Defines only reflect its own
// last Files() call, which hasn't happened yet on entry to this function.
func (s *Server) buildIndex(ctx context.Context, discoverer workspace.Discoverer) []workspace.SourceFile {
	roots, err := discoverer.Roots(ctx)
	if err != nil {
		s.log.Warningf("indexing: could not list workspace roots: %s", err)
		return nil
	}

	var all []workspace.SourceFile
	for _, root := range roots {
		files, err := discoverer.Files(ctx, root)
		if err != nil {
			s.log.Warningf("indexing: could not list files under %s: %s", root.Path, err)
			continue
		}
		all = append(all, files...)
	}

	if fp, ok := discoverer.(workspace.FilelistProvider); ok {
		s.index.SetIncludeResolverFactory(newIncludeResolverFactory(fp.IncludeDirs()))
		s.index.SetInitialMacros(fp.Defines())
	} else {
		s.index.SetIncludeResolverFactory(nil)
		s.index.SetInitialMacros(nil)
	}

	// Reading and scanning are per-file independent, so fan out across
	// the CPUs -- on a company-sized workspace a serial scan makes startup
	// noticeably slow. On cancellation the returned file list can exceed
	// what was actually indexed, which is fine because cancellation only
	// happens at shutdown, when the caller is about to exit anyway.
	var indexed atomic.Int64
	forEachParallel(ctx, len(all), func(i int) {
		file := all[i]
		uri := pathToURI(file.LogicalPath)
		// The live editor buffer is authoritative while open (same
		// reasoning as syncFromDisk/removeStaleFiles) -- but this rebuild
		// pass can be running because a filelist edit just changed the
		// workspace's `+incdir+`/`+define+` (the resolver factory/initial
		// macros wired in above), which an open file's index entry needs
		// to reflect too. So its text still comes from the buffer, never
		// disk, but it's re-scanned through SetFile like everything else
		// rather than left untouched with whatever config was active at
		// its last keystroke.
		if s.scanOpenBuffer(uri) {
			indexed.Add(1)
			return
		}
		data, err := os.ReadFile(file.ResolvedPath)
		if err != nil {
			s.log.Warningf("indexing: could not read %s: %s", file.ResolvedPath, err)
			return
		}
		s.publishDiagnostics(s.index.SetFile(uri, string(data)))
		indexed.Add(1)
	})

	s.log.Infof("indexed %d source file(s)", indexed.Load())
	return all
}

// forEachParallel calls fn(i) for every i in [0, n), spread across
// GOMAXPROCS goroutines, and returns once every call has finished. Every
// bulk rescan (startup indexing, include discovery, the watcher's
// debounced batch and the cascade after a header save) goes through it:
// each file's read and scan is independent of the others, Index.SetFile is
// thread-safe, and its tokenize pass (the expensive part) runs before it
// takes the index lock.
//
// Cancelling ctx stops new calls from starting, so Shutdown never has to
// wait out a full pass; calls already running finish.
func forEachParallel(ctx context.Context, n int, fn func(i int)) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), n) {
		wg.Go(func() {
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				fn(i)
			}
		})
	}
	wg.Wait()
}

// indexAndWatch builds the index and then watches the discovered files
// (and, for a FilelistProvider, the filelist files themselves) for on-disk
// changes, rebuilding whenever a filelist changes (the file set it names
// may have changed) and looping to re-arm watches against the fresh file
// set. Files a previous pass discovered but the latest one didn't (e.g. a
// filelist edit removed them from the workspace view) are dropped from the
// index, so their symbols stop resolving and rename can't emit edits for
// files no longer in the project. It returns once ctx is cancelled (see
// Server.Shutdown) or file watching can't start at all.
func (s *Server) indexAndWatch(ctx context.Context, discoverer workspace.Discoverer) {
	var prev map[string]bool
	for {
		var files []workspace.SourceFile
		files, prev = s.rebuildIndex(ctx, discoverer, prev)

		var filelists []string
		if fp, ok := discoverer.(workspace.FilelistProvider); ok {
			filelists = fp.VisitedFilelists()
		}

		if !s.watchFiles(ctx, files, filelists) {
			return
		}
	}
}

// rebuildIndex runs one full discovery+scan pass and reconciles the index
// with its result: discovered files are (re)scanned in via buildIndex, any
// file reached only via another file's `include gets its own direct scan
// too (see scanIncludeDiscoveredFiles), and anything prev contains that
// this pass didn't rediscover either way is dropped. It returns the full
// file set (for arming watches) and the URI set to pass back as prev on
// the next pass.
func (s *Server) rebuildIndex(ctx context.Context, discoverer workspace.Discoverer, prev map[string]bool) ([]workspace.SourceFile, map[string]bool) {
	files := s.buildIndex(ctx, discoverer)
	files = append(files, s.scanIncludeDiscoveredFiles(ctx, files)...)

	current := make(map[string]bool, len(files))
	for _, f := range files {
		current[pathToURI(f.LogicalPath)] = true
	}
	s.removeStaleFiles(prev, current)
	return files, current
}

// scanIncludeDiscoveredFiles finds every URI discovered's own freshly-
// recorded dependency data (sv.Index.IncludesOf) says is `include d --
// already the full transitive set per file, since svparse's preprocessor
// flattens an included file's own includes into the same resolver
// instance -- and gives each its own direct SetFile call: a file
// populated only as a side effect of scanning its includer has
// declarations but no occurrences of its own (see sv.Index.SetFile's doc
// comment) and no dependency-graph entry for its own includes, if it has
// any -- both only ever come from a direct scan. Returns SourceFile-shaped
// entries for them so the caller can fold them into the watched file set,
// and the returned+discovered union into the staleness check, too.
//
// Deliberately consults IncludesOf (this pass's fresh data), not every URI
// the index holds declarations for (which can still include a stale entry
// from a file's *previous* scan, before this pass's staleness
// reconciliation runs) -- using the latter would make an include-discovered
// file "sticky" forever once found once, even after nothing includes it
// anymore.
func (s *Server) scanIncludeDiscoveredFiles(ctx context.Context, discovered []workspace.SourceFile) []workspace.SourceFile {
	known := make(map[string]bool, len(discovered))
	for _, f := range discovered {
		known[pathToURI(f.LogicalPath)] = true
	}

	var reachable []string
	for uri := range known {
		reachable = append(reachable, s.index.IncludesOf(uri)...)
	}

	var candidates []string
	for _, uri := range reachable {
		if known[uri] {
			continue // e.g. two top-level files `include the same header
		}
		known[uri] = true
		candidates = append(candidates, uri)
	}

	// scanned[i] is candidates[i]'s path once it has been indexed, and ""
	// if it wasn't, so the result keeps candidates' order however the
	// workers interleave.
	scanned := make([]string, len(candidates))
	forEachParallel(ctx, len(candidates), func(i int) {
		uri := candidates[i]
		path, err := uriToPath(uri)
		if err != nil {
			return
		}
		// Same "buffer text, but still re-scanned through SetFile"
		// treatment as buildIndex -- an `include d file can also be
		// directly open in the editor, and this pass's resolver/macro
		// config may have just changed.
		if s.scanOpenBuffer(uri) {
			scanned[i] = path
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			s.log.Warningf("indexing: could not read %s (discovered via `include): %s", path, err)
			return
		}
		s.publishDiagnostics(s.index.SetFile(uri, string(data)))
		scanned[i] = path
	})

	var extra []workspace.SourceFile
	for _, path := range scanned {
		if path != "" {
			extra = append(extra, workspace.SourceFile{LogicalPath: path, ResolvedPath: path})
		}
	}
	return extra
}

// scanOpenBuffer indexes an open document's live text, retrying if an edit
// landed while the scan was in flight.
//
// docs.Get and index.SetFile are each safe on their own but not atomic
// together, so a didChange arriving between them let a background pass
// write version N over the version N+1 the handler had already indexed.
// That showed up as a diagnostic or a goto-definition result reverting to
// the pre-keystroke state and staying there until the next keystroke.
// Re-reading after the write and repeating while the version has moved
// converges on the newest text without a lock, and so without serializing
// the worker pool that made background indexing parallel in the first
// place. Reports whether the document was open at all.
func (s *Server) scanOpenBuffer(uri string) bool {
	doc, open := s.docs.Get(document.URI(uri))
	if !open {
		return false
	}
	for {
		s.publishDiagnostics(s.index.SetFile(uri, doc.Text))
		latest, stillOpen := s.docs.Get(document.URI(uri))
		if !stillOpen || latest.Version == doc.Version {
			return true
		}
		doc = latest
	}
}

// removeStaleFiles drops index entries for every URI in prev that current
// no longer contains. An open editor buffer is authoritative regardless of
// what discovery finds (the same reasoning TextDocumentDidClose applies),
// so open documents are spared. Republishes an empty diagnostics list for
// each dropped URI, clearing any squiggles the client was still showing
// for a file no longer in the workspace.
func (s *Server) removeStaleFiles(prev, current map[string]bool) {
	for uri := range prev {
		if current[uri] {
			continue
		}
		if _, open := s.docs.Get(document.URI(uri)); open {
			continue
		}
		s.publishDiagnostics(s.index.RemoveFile(uri))
		s.log.Infof("dropped %s from the index (no longer referenced by the workspace)", uri)
	}
}
