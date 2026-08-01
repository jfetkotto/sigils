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
		s.Log.Warningf("indexing: could not list workspace roots: %s", err)
		return nil
	}

	var all []workspace.SourceFile
	for _, root := range roots {
		files, err := discoverer.Files(ctx, root)
		if err != nil {
			s.Log.Warningf("indexing: could not list files under %s: %s", root.Path, err)
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
	// noticeably slow. Index.SetFile is thread-safe, and its tokenize pass
	// (the expensive part) runs before it takes the index lock. ctx is
	// checked per file so Shutdown doesn't have to wait out a full scan;
	// on cancellation the returned file list can exceed what was actually
	// indexed, which is fine because cancellation only happens at
	// shutdown, when the caller is about to exit anyway.
	work := make(chan workspace.SourceFile)
	var indexed atomic.Int64
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range work {
				if ctx.Err() != nil {
					continue // keep draining so the feed loop can't block
				}
				uri := pathToURI(file.LogicalPath)
				if doc, open := s.docs.Get(document.URI(uri)); open {
					// The live editor buffer is authoritative while open
					// (same reasoning as reindexFromDisk/removeStaleFiles) --
					// but this rebuild pass can be running because a
					// filelist edit just changed the workspace's
					// `+incdir+`/`+define+` (the resolver factory/initial
					// macros wired in above), which an open file's index
					// entry needs to reflect too. So its text still comes
					// from the buffer, never disk, but it's re-scanned
					// through SetFile like everything else rather than
					// left untouched with whatever config was active at
					// its last keystroke.
					s.publishDiagnostics(s.index.SetFile(uri, doc.Text))
					indexed.Add(1)
					continue
				}
				data, err := os.ReadFile(file.ResolvedPath)
				if err != nil {
					s.Log.Warningf("indexing: could not read %s: %s", file.ResolvedPath, err)
					continue
				}
				s.publishDiagnostics(s.index.SetFile(uri, string(data)))
				indexed.Add(1)
			}
		}()
	}

feed:
	for _, file := range all {
		select {
		case work <- file:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()

	s.Log.Infof("indexed %d source file(s)", indexed.Load())
	return all
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
// Deliberately consults IncludesOf (this pass's fresh data), not
// AllKnownURIs (which can still hold a stale entry from a file's
// *previous* scan, before this pass's staleness reconciliation runs) --
// using the latter would make an include-discovered file "sticky" forever
// once found once, even after nothing includes it anymore.
func (s *Server) scanIncludeDiscoveredFiles(ctx context.Context, discovered []workspace.SourceFile) []workspace.SourceFile {
	known := make(map[string]bool, len(discovered))
	for _, f := range discovered {
		known[pathToURI(f.LogicalPath)] = true
	}

	var reachable []string
	for uri := range known {
		reachable = append(reachable, s.index.IncludesOf(uri)...)
	}

	var extra []workspace.SourceFile
	for _, uri := range reachable {
		if known[uri] {
			continue // e.g. two top-level files `include the same header
		}
		known[uri] = true
		if ctx.Err() != nil {
			break
		}
		path, err := uriToPath(uri)
		if err != nil {
			continue
		}
		if doc, open := s.docs.Get(document.URI(uri)); open {
			// Same "buffer text, but still re-scanned through SetFile"
			// treatment as the worker pool above -- an `include d file can
			// also be directly open in the editor, and this pass's
			// resolver/macro config may have just changed.
			s.publishDiagnostics(s.index.SetFile(uri, doc.Text))
			extra = append(extra, workspace.SourceFile{LogicalPath: path, ResolvedPath: path})
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			s.Log.Warningf("indexing: could not read %s (discovered via `include): %s", path, err)
			continue
		}
		s.publishDiagnostics(s.index.SetFile(uri, string(data)))
		extra = append(extra, workspace.SourceFile{LogicalPath: path, ResolvedPath: path})
	}
	return extra
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
		s.index.RemoveFile(uri)
		s.publishDiagnostics([]string{uri})
		s.Log.Infof("dropped %s from the index (no longer referenced by the workspace)", uri)
	}
}
