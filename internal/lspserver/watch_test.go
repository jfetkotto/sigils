package lspserver

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/document"
	"github.com/jfetkotto/sigils/internal/workspace"
)

// threadSafeNotifications collects notify calls from any goroutine (unlike
// diagnostics_test.go's capturingNotify, which assumes single-threaded,
// synchronous use) -- needed here since watchFiles calls notify from its
// own background goroutine while the test polls from the main one.
type threadSafeNotifications struct {
	mu    sync.Mutex
	calls []struct {
		method string
		params any
	}
}

func (n *threadSafeNotifications) notify(method string, params any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, struct {
		method string
		params any
	}{method, params})
}

// diagnosticsFor returns the most recent publishDiagnostics params for
// uri, if any have arrived yet.
func (n *threadSafeNotifications) diagnosticsFor(uri string) (protocol.PublishDiagnosticsParams, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := len(n.calls) - 1; i >= 0; i-- {
		if n.calls[i].method != "textDocument/publishDiagnostics" {
			continue
		}
		if params, ok := n.calls[i].params.(protocol.PublishDiagnosticsParams); ok && string(params.URI) == uri {
			return params, true
		}
	}
	return protocol.PublishDiagnosticsParams{}, false
}

// runWatchFiles starts watchFiles in the background and returns a channel
// that receives its result once it stops.
func runWatchFiles(s *Server, ctx context.Context, sourceFiles []workspace.SourceFile, filelistPaths []string) <-chan bool {
	done := make(chan bool, 1)
	go func() { done <- s.watchFiles(ctx, sourceFiles, filelistPaths) }()
	// Give the watcher a moment to call fsnotify.Add and start selecting
	// on its Events channel before the test writes to disk.
	time.Sleep(150 * time.Millisecond)
	return done
}

func TestWatchFilesReindexesChangedSourceFile(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "leaf.sv")
	writeFileT(t, srcPath, "module old_name;\nendmodule\n")
	resolved := evalSymT(t, srcPath)

	s := newTestServer()
	s.index.SetFile(pathToURI(srcPath), "module old_name;\nendmodule\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runWatchFiles(s, ctx, []workspace.SourceFile{{LogicalPath: srcPath, ResolvedPath: resolved}}, nil)

	writeFileT(t, srcPath, "module new_name;\nendmodule\n")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := s.Index().Lookup("new_name"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("file watcher did not pick up the on-disk change in time")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	waitForWatch(t, done)
}

func TestWatchFilesPublishesDiagnosticsForReindexedFile(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "leaf.sv")
	writeFileT(t, srcPath, "module leaf; endmodule\n")
	resolved := evalSymT(t, srcPath)

	s := newTestServer()
	var notifications threadSafeNotifications
	s.setNotify(notifications.notify)
	s.index.SetFile(pathToURI(srcPath), "module leaf; endmodule\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runWatchFiles(s, ctx, []workspace.SourceFile{{LogicalPath: srcPath, ResolvedPath: resolved}}, nil)

	writeFileT(t, srcPath, "42;\nmodule leaf; endmodule\n")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if params, ok := notifications.diagnosticsFor(pathToURI(srcPath)); ok && len(params.Diagnostics) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected a publishDiagnostics notification for the malformed on-disk change")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	waitForWatch(t, done)
}

func TestWatchFilesSkipsReindexingOpenDocuments(t *testing.T) {
	// Shorten the debounce so the 300ms settle below reaches past it --
	// otherwise the assertion would pass trivially without ever exercising
	// the open-document skip in reindexFromDisk.
	old := watchDebounce
	watchDebounce = 50 * time.Millisecond
	t.Cleanup(func() { watchDebounce = old })

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "leaf.sv")
	writeFileT(t, srcPath, "module old_name;\nendmodule\n")
	resolved := evalSymT(t, srcPath)

	s := newTestServer()
	uri := pathToURI(srcPath)
	s.docs.Open(document.URI(uri), "systemverilog", 1, "module old_name;\nendmodule\n")
	s.index.SetFile(uri, "module old_name;\nendmodule\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runWatchFiles(s, ctx, []workspace.SourceFile{{LogicalPath: srcPath, ResolvedPath: resolved}}, nil)

	// An external tool writes to the file on disk while it's still open
	// (and, implicitly, unsaved/ahead) in the editor.
	writeFileT(t, srcPath, "module new_name;\nendmodule\n")
	time.Sleep(300 * time.Millisecond)

	if _, ok := s.Index().Lookup("new_name"); ok {
		t.Fatalf("expected the on-disk change to be ignored while the document is open")
	}
	if _, ok := s.Index().Lookup("old_name"); !ok {
		t.Fatalf("expected old_name to remain indexed (editor buffer stays authoritative)")
	}

	cancel()
	waitForWatch(t, done)
}

func TestWatchFilesCoalescesRapidWritesToFinalContent(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "leaf.sv")
	writeFileT(t, srcPath, "module name_v1;\nendmodule\n")
	resolved := evalSymT(t, srcPath)

	s := newTestServer()
	s.index.SetFile(pathToURI(srcPath), "module name_v1;\nendmodule\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runWatchFiles(s, ctx, []workspace.SourceFile{{LogicalPath: srcPath, ResolvedPath: resolved}}, nil)

	// Two writes in quick succession -- the debounce should coalesce them
	// into one reindex of the final content.
	writeFileT(t, srcPath, "module name_v2;\nendmodule\n")
	writeFileT(t, srcPath, "module name_v3;\nendmodule\n")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := s.Index().Lookup("name_v3"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("debounced watcher did not index the final content in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := s.Index().Lookup("name_v1"); ok {
		t.Fatalf("expected the original content to have been replaced")
	}

	cancel()
	waitForWatch(t, done)
}

func TestWatchFilesReturnsTrueWhenFilelistChanges(t *testing.T) {
	dir := t.TempDir()
	filelistPath := filepath.Join(dir, "top.f")
	writeFileT(t, filelistPath, "leaf.sv\n")
	resolved := evalSymT(t, filelistPath)

	s := newTestServer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runWatchFiles(s, ctx, nil, []string{resolved})

	writeFileT(t, filelistPath, "leaf.sv\nextra.sv\n")

	select {
	case result := <-done:
		if !result {
			t.Fatalf("expected watchFiles to return true after the watched filelist changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("watchFiles did not return after the filelist change")
	}
}

func TestWatchFilesCascadesToDependentsOfAChangedInclude(t *testing.T) {
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	defsPath := filepath.Join(dir, "defs.svh")
	writeFileT(t, topPath, "`include \"defs.svh\"\n`ifdef ENABLE_FOO\nmodule foo_only; endmodule\n`endif\n")
	writeFileT(t, defsPath, "`define ENABLE_FOO\n")

	s := newTestServer()
	s.index.SetIncludeResolverFactory(newIncludeResolverFactory(nil))
	s.index.SetFile(pathToURI(defsPath), "`define ENABLE_FOO\n")
	s.index.SetFile(pathToURI(topPath), "`include \"defs.svh\"\n`ifdef ENABLE_FOO\nmodule foo_only; endmodule\n`endif\n")

	if _, ok := s.Index().Lookup("foo_only"); !ok {
		t.Fatalf("expected foo_only to be declared with ENABLE_FOO defined")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sourceFiles := []workspace.SourceFile{
		{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)},
		{LogicalPath: defsPath, ResolvedPath: evalSymT(t, defsPath)},
	}
	done := runWatchFiles(s, ctx, sourceFiles, nil)

	// Removing the `define undefines ENABLE_FOO -- top.sv itself is
	// untouched on disk, so only the cascade (not the direct reindex of
	// the changed file) can make foo_only disappear from its
	// declarations.
	writeFileT(t, defsPath, "")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := s.Index().Lookup("foo_only"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected the cascade to reindex top.sv after defs.svh changed, removing foo_only")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	waitForWatch(t, done)
}

func TestWatchFilesCascadePublishesDiagnosticsForDependent(t *testing.T) {
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	defsPath := filepath.Join(dir, "defs.svh")
	// While VALID_MODE is defined, top.sv's `ifdef branch is well-formed;
	// the `else branch (taken once defs.svh stops defining it) is not.
	topText := "`include \"defs.svh\"\n`ifdef VALID_MODE\nmodule top; endmodule\n`else\nthis is not valid syntax at all;\n`endif\n"
	writeFileT(t, topPath, topText)
	writeFileT(t, defsPath, "`define VALID_MODE\n")

	s := newTestServer()
	var notifications threadSafeNotifications
	s.setNotify(notifications.notify)
	s.index.SetIncludeResolverFactory(newIncludeResolverFactory(nil))
	s.index.SetFile(pathToURI(defsPath), "`define VALID_MODE\n")
	s.index.SetFile(pathToURI(topPath), topText)

	if diags := s.Index().Diagnostics(pathToURI(topPath)); len(diags) != 0 {
		t.Fatalf("expected top.sv to start with no diagnostics, got %+v", diags)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sourceFiles := []workspace.SourceFile{
		{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)},
		{LogicalPath: defsPath, ResolvedPath: evalSymT(t, defsPath)},
	}
	done := runWatchFiles(s, ctx, sourceFiles, nil)

	// top.sv itself is untouched on disk -- only the cascade reindexes it.
	writeFileT(t, defsPath, "")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if params, ok := notifications.diagnosticsFor(pathToURI(topPath)); ok && len(params.Diagnostics) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected the cascade to publish a new diagnostic for top.sv after defs.svh changed")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	waitForWatch(t, done)
}

func TestWatchFilesCascadeReindexesOpenDependent(t *testing.T) {
	// Same scenario as TestWatchFilesCascadesToDependentsOfAChangedInclude,
	// but top.sv is open in the editor. reindexURI must still re-scan it
	// (from its buffer text, never disk) rather than skip it outright, or
	// its declarations stay resolved against defs.svh's OLD content until
	// top.sv's own next keystroke.
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	defsPath := filepath.Join(dir, "defs.svh")
	topText := "`include \"defs.svh\"\n`ifdef ENABLE_FOO\nmodule foo_only; endmodule\n`endif\n"
	writeFileT(t, topPath, topText)
	writeFileT(t, defsPath, "`define ENABLE_FOO\n")

	s := newTestServer()
	s.index.SetIncludeResolverFactory(newIncludeResolverFactory(nil))
	s.index.SetFile(pathToURI(defsPath), "`define ENABLE_FOO\n")
	s.index.SetFile(pathToURI(topPath), topText)
	s.docs.Open(document.URI(pathToURI(topPath)), "systemverilog", 1, topText)

	if _, ok := s.Index().Lookup("foo_only"); !ok {
		t.Fatalf("expected foo_only to be declared with ENABLE_FOO defined")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sourceFiles := []workspace.SourceFile{
		{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)},
		{LogicalPath: defsPath, ResolvedPath: evalSymT(t, defsPath)},
	}
	done := runWatchFiles(s, ctx, sourceFiles, nil)

	// defs.svh changes on disk; top.sv (open, untouched on disk) must be
	// rescanned from its buffer as part of the cascade for foo_only to
	// disappear.
	writeFileT(t, defsPath, "")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := s.Index().Lookup("foo_only"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected the cascade to reindex the OPEN top.sv from its buffer after defs.svh changed, removing foo_only")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	waitForWatch(t, done)
}

func TestWatchFilesReturnsFalseOnContextCancel(t *testing.T) {
	s := newTestServer()
	ctx, cancel := context.WithCancel(context.Background())
	done := runWatchFiles(s, ctx, nil, nil)

	cancel()
	waitForWatch(t, done)
}

func writeFileT(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func evalSymT(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func waitForWatch(t *testing.T, done <-chan bool) {
	t.Helper()
	select {
	case result := <-done:
		if result {
			t.Fatalf("expected watchFiles to return false after context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("watchFiles did not return promptly after context cancellation")
	}
}
