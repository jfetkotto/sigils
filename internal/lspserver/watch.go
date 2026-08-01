package lspserver

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/jfetkotto/sigils/internal/document"
	"github.com/jfetkotto/sigils/internal/workspace"
)

// watchDebounce is how long watchFiles waits after the last file event
// before acting on the accumulated batch. Many tools save via a temp file
// plus rename (several events per save), and a filelist change triggers a
// full workspace rebuild -- reacting per event would re-read files and
// rebuild the workspace several times for one logical change. A var, not
// a const, so tests can shorten it.
var watchDebounce = 500 * time.Millisecond

// watchFiles watches the parent directories of every discovered source
// file and filelist file for on-disk changes, keeping the index fresh for
// edits made outside the editor (a teammate's commit landing via a
// workspace refresh, a codegen tool regenerating a file, etc). It watches
// directories rather than the files themselves and filters by name,
// per fsnotify's own guidance: many tools save by writing a temp file and
// renaming it over the original, which replaces the inode a per-file
// watch would be attached to.
//
// It returns true if a watched filelist changed on disk -- the caller
// should rebuild the index and call watchFiles again, since the file set
// a changed filelist names may be different now. It returns false if ctx
// was cancelled or the watcher couldn't be started at all. Events are
// debounced (see watchDebounce): a burst of writes coalesces into one
// re-read per touched source file, or one rebuild if any filelist was
// touched.
//
// Known limitations: a brand-new file that isn't yet referenced by any
// watched filelist won't be picked up until either the filelist is edited
// to reference it (which does trigger a full rescan) or the server
// restarts. A very large workspace's file set can also exceed the host
// OS's watch-handle limit (e.g. Linux's inotify instance limit); when that
// happens, watching degrades to "some files aren't live-reindexed until
// restart" rather than failing outright.
func (s *Server) watchFiles(ctx context.Context, sourceFiles []workspace.SourceFile, filelistPaths []string) bool {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		s.Log.Warningf("file watching: could not start: %s", err)
		return false
	}
	defer watcher.Close()

	sourceByPath := make(map[string]string, len(sourceFiles)) // resolved path -> logical path
	filelistSet := make(map[string]bool, len(filelistPaths))
	dirs := make(map[string]bool)

	for _, f := range sourceFiles {
		sourceByPath[f.ResolvedPath] = f.LogicalPath
		dirs[filepath.Dir(f.ResolvedPath)] = true
	}
	for _, p := range filelistPaths {
		filelistSet[p] = true
		dirs[filepath.Dir(p)] = true
	}
	for dir := range dirs {
		if err := watcher.Add(dir); err != nil {
			s.Log.Warningf("file watching: could not watch %s: %s", dir, err)
		}
	}

	// Debounce state: nothing is acted on until the timer has been quiet
	// for watchDebounce after the last relevant event. timerC stays nil
	// (never selectable) until the first event arms the timer.
	var timer *time.Timer
	var timerC <-chan time.Time
	armTimer := func() {
		if timer == nil {
			timer = time.NewTimer(watchDebounce)
			timerC = timer.C
		} else {
			timer.Reset(watchDebounce)
		}
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	pending := make(map[string]string) // resolved event path -> logical path
	rebuildPending := false

	for {
		select {
		case <-ctx.Done():
			return false

		case event, ok := <-watcher.Events:
			if !ok {
				return false
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			if filelistSet[event.Name] {
				if !rebuildPending {
					s.Log.Infof("filelist %s changed on disk; rescanning the workspace shortly", event.Name)
				}
				rebuildPending = true
				armTimer()
				continue
			}

			logicalPath, isSource := sourceByPath[event.Name]
			if !isSource {
				continue
			}
			pending[event.Name] = logicalPath
			armTimer()

		case <-timerC:
			if rebuildPending {
				return true
			}
			changed := make([]string, 0, len(pending))
			for eventPath, logicalPath := range pending {
				s.reindexFromDisk(eventPath, logicalPath)
				changed = append(changed, pathToURI(logicalPath))
			}
			clear(pending)
			s.cascadeReindexDependents(changed)

		case err, ok := <-watcher.Errors:
			if !ok {
				return false
			}
			s.Log.Warningf("file watching error: %s", err)
		}
	}
}

// reindexFromDisk re-reads one watched source file and replaces its index
// entries, unless the document is open in the editor -- the live buffer
// is authoritative while open.
func (s *Server) reindexFromDisk(eventPath, logicalPath string) {
	uri := pathToURI(logicalPath)
	if _, open := s.docs.Get(document.URI(uri)); open {
		return
	}
	data, err := os.ReadFile(eventPath)
	if err != nil {
		s.Log.Warningf("file watching: could not reread %s: %s", eventPath, err)
		return
	}
	s.publishDiagnostics(s.index.SetFile(uri, string(data)))
	s.Log.Infof("reindexed %s after an on-disk change", logicalPath)
}

// cascadeReindexDependents re-reads and re-scans every file that
// `include s one of changed (each already reindexed by the caller), so a
// saved header's effect on the files that include it -- resolved macros,
// conditional-compilation state, declarations attributed through it --
// catches up in the same debounced batch rather than waiting for those
// files' own next edit. Scoped to the on-disk/save path only (this is
// watchFiles' own debounced tick), not live didChange keystrokes -- an
// unsaved edit to a widely-included header doesn't synchronously re-scan
// every dependent on every keystroke, a deliberate, documented tradeoff
// (see the Phase 4 plan).
//
// One pass over each changed URI's own Dependents is enough, with no
// further transitive cascade: sv.Index.Dependents(uri) already returns
// every file whose *own* last scan resolved an `include to uri, directly
// or transitively (svparse's preprocessor flattens an included file's own
// includes into the same resolver instance) -- so if Z includes X which
// includes Y, Z's own last scan already recorded Y as a dependency too,
// and Dependents(Y) already lists Z directly, not just X.
func (s *Server) cascadeReindexDependents(changed []string) {
	seen := make(map[string]bool, len(changed))
	for _, uri := range changed {
		seen[uri] = true
	}
	for _, uri := range changed {
		for _, dep := range s.index.Dependents(uri) {
			if seen[dep] {
				continue
			}
			seen[dep] = true
			s.reindexURI(dep)
		}
	}
}

// reindexURI re-scans uri -- from its open editor buffer if it has one,
// otherwise re-read from disk -- and replaces its index entries. Unlike
// reindexFromDisk, an open buffer is NOT skipped here: this is called for
// a *dependent* of some other file whose `include d content just changed
// (see cascadeReindexDependents), so even though uri's own text hasn't
// changed, what it resolves to (macros, conditional-compilation state,
// declarations reached through the include) has -- an open dependent
// needs its diagnostics/declarations refreshed from the buffer just as
// much as an on-disk one needs them refreshed from disk, or its
// declarations/diagnostics stay stale until its own next keystroke.
// A no-op if uri isn't a resolvable file:// URI.
func (s *Server) reindexURI(uri string) {
	if doc, open := s.docs.Get(document.URI(uri)); open {
		s.publishDiagnostics(s.index.SetFile(uri, doc.Text))
		s.Log.Infof("reindexed open buffer %s after a dependency's on-disk change", uri)
		return
	}
	path, err := uriToPath(uri)
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		s.Log.Warningf("file watching: could not reread %s: %s", path, err)
		return
	}
	s.publishDiagnostics(s.index.SetFile(uri, string(data)))
	s.Log.Infof("reindexed %s after a dependency's on-disk change", path)
}
