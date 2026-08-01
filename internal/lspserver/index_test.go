package lspserver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jfetkotto/sigils/internal/document"
	"github.com/jfetkotto/sigils/internal/workspace"
)

// fakeDiscoverer serves a fixed file set the test can mutate between
// rebuildIndex passes, simulating a filelist edit changing the workspace
// view. It also satisfies workspace.FilelistProvider (empty/nil by
// default) so tests can configure includeDirs/defines to exercise
// buildIndex's resolver-wiring without needing a real FilelistDiscoverer --
// IncludeDirs/Defines deliberately only reflect what's configured *before*
// Files() is called, populated inside Files() itself rather than exposed
// directly, mirroring workspace.FilelistDiscoverer's own timing (they only
// ever reflect its last Files() call). Getting this wrong once already cost
// a real bug: buildIndex briefly wired the resolver from these before
// calling Files() at all, which this fake's earlier, simpler shape (direct
// fields, no timing dependency) couldn't have caught -- only the slower
// end-to-end test against a real FilelistDiscoverer did.
type fakeDiscoverer struct {
	files              []workspace.SourceFile
	visitedFilelists   []string
	pendingIncludeDirs []string
	pendingDefines     map[string]string

	includeDirs []string
	defines     map[string]string
}

func (d *fakeDiscoverer) Roots(ctx context.Context) ([]workspace.Root, error) {
	return []workspace.Root{{Path: "/"}}, nil
}

func (d *fakeDiscoverer) Files(ctx context.Context, root workspace.Root) ([]workspace.SourceFile, error) {
	d.includeDirs = d.pendingIncludeDirs
	d.defines = d.pendingDefines
	return d.files, nil
}

func (d *fakeDiscoverer) VisitedFilelists() []string { return d.visitedFilelists }
func (d *fakeDiscoverer) IncludeDirs() []string      { return d.includeDirs }
func (d *fakeDiscoverer) Defines() map[string]string { return d.defines }

func TestRebuildIndexDropsFilesNoLongerDiscovered(t *testing.T) {
	dir := t.TempDir()
	keepPath := filepath.Join(dir, "keep.sv")
	dropPath := filepath.Join(dir, "drop.sv")
	writeFileT(t, keepPath, "module keep_mod;\nendmodule\n")
	writeFileT(t, dropPath, "module drop_mod;\nendmodule\n")

	src := func(path string) workspace.SourceFile {
		return workspace.SourceFile{LogicalPath: path, ResolvedPath: evalSymT(t, path)}
	}

	s := newTestServer()
	d := &fakeDiscoverer{files: []workspace.SourceFile{src(keepPath), src(dropPath)}}

	_, prev := s.rebuildIndex(context.Background(), d, nil)
	if _, ok := s.Index().Lookup("drop_mod"); !ok {
		t.Fatalf("expected drop_mod to be indexed after the first pass")
	}

	d.files = []workspace.SourceFile{src(keepPath)}
	s.rebuildIndex(context.Background(), d, prev)

	if _, ok := s.Index().Lookup("drop_mod"); ok {
		t.Fatalf("expected drop_mod to be dropped once its file left the discovered set")
	}
	if _, ok := s.Index().Lookup("keep_mod"); !ok {
		t.Fatalf("expected keep_mod to remain indexed")
	}
}

func TestRemoveStaleFilesClearsDiagnosticsForDroppedFile(t *testing.T) {
	dir := t.TempDir()
	keepPath := filepath.Join(dir, "keep.sv")
	dropPath := filepath.Join(dir, "drop.sv")
	writeFileT(t, keepPath, "module keep_mod; endmodule\n")
	writeFileT(t, dropPath, "42;\nmodule drop_mod; endmodule\n") // malformed on purpose

	src := func(path string) workspace.SourceFile {
		return workspace.SourceFile{LogicalPath: path, ResolvedPath: evalSymT(t, path)}
	}

	s := newTestServer()
	// buildIndex's worker pool calls publishDiagnostics concurrently
	// (rebuildIndex -> buildIndex fans out across GOMAXPROCS goroutines),
	// so this needs the thread-safe notifier, not diagnostics_test.go's
	// single-goroutine capturingNotify.
	var notifications threadSafeNotifications
	s.setNotify(notifications.notify)
	d := &fakeDiscoverer{files: []workspace.SourceFile{src(keepPath), src(dropPath)}}

	_, prev := s.rebuildIndex(context.Background(), d, nil)
	if diags := s.Index().Diagnostics(pathToURI(dropPath)); len(diags) == 0 {
		t.Fatalf("expected drop.sv's malformed content to produce a diagnostic after the first pass")
	}

	d.files = []workspace.SourceFile{src(keepPath)}
	s.rebuildIndex(context.Background(), d, prev)

	params, ok := notifications.diagnosticsFor(pathToURI(dropPath))
	if !ok {
		t.Fatalf("expected a publishDiagnostics notification for the dropped file")
	}
	if len(params.Diagnostics) != 0 {
		t.Fatalf("expected an empty diagnostics list clearing drop.sv, got %+v", params.Diagnostics)
	}
}

func TestBuildIndexIndexesNothingAfterContextCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaf.sv")
	writeFileT(t, path, "module leaf;\nendmodule\n")

	s := newTestServer()
	d := &fakeDiscoverer{files: []workspace.SourceFile{
		{LogicalPath: path, ResolvedPath: evalSymT(t, path)},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.buildIndex(ctx, d)

	if got := s.Index().FileCount(); got != 0 {
		t.Fatalf("expected nothing indexed under a cancelled context, got %d file(s)", got)
	}
}

func TestRebuildIndexKeepsDroppedFileWhileOpenInEditor(t *testing.T) {
	dir := t.TempDir()
	dropPath := filepath.Join(dir, "drop.sv")
	writeFileT(t, dropPath, "module drop_mod;\nendmodule\n")

	s := newTestServer()
	d := &fakeDiscoverer{files: []workspace.SourceFile{
		{LogicalPath: dropPath, ResolvedPath: evalSymT(t, dropPath)},
	}}

	_, prev := s.rebuildIndex(context.Background(), d, nil)
	s.docs.Open(document.URI(pathToURI(dropPath)), "systemverilog", 1, "module drop_mod;\nendmodule\n")

	d.files = nil
	s.rebuildIndex(context.Background(), d, prev)

	if _, ok := s.Index().Lookup("drop_mod"); !ok {
		t.Fatalf("expected drop_mod to survive the rebuild while its document is open (editor buffer stays authoritative)")
	}
}

func TestBuildIndexDoesNotOverwriteOpenBufferWithDiskContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "top.sv")
	writeFileT(t, path, "module m;\n  logic disk_only_ref;\nendmodule\n")

	s := newTestServer()
	uri := pathToURI(path)
	bufferText := "module m;\n  logic buffer_only_ref;\nendmodule\n"
	// Simulate TextDocumentDidOpen: register the open document and scan its
	// buffer content directly, exactly as the real handler does -- the
	// buffer text deliberately differs from what's on disk (an unsaved
	// edit).
	s.docs.Open(document.URI(uri), "systemverilog", 1, bufferText)
	s.Index().SetFile(uri, bufferText)

	d := &fakeDiscoverer{files: []workspace.SourceFile{
		{LogicalPath: path, ResolvedPath: evalSymT(t, path)},
	}}

	s.buildIndex(context.Background(), d)

	if _, ok := s.Index().Lookup("buffer_only_ref"); !ok {
		t.Fatalf("expected the open buffer's declaration to survive buildIndex")
	}
	if _, ok := s.Index().Lookup("disk_only_ref"); ok {
		t.Fatalf("expected buildIndex to NOT overwrite the open buffer's index entry with on-disk content")
	}
}

func TestScanIncludeDiscoveredFilesDoesNotOverwriteOpenBufferOccurrences(t *testing.T) {
	// Unlike the plain top-level case above, an `include d file's own
	// DECLARATION bucket is still replaced by whatever the includer's scan
	// resolved the `include to (via IncludeResolver, which always reads
	// from disk -- a separate, documented, out-of-scope design point, see
	// sv.Index.SetFile's own doc comment). What must survive is the file's
	// own OCCURRENCE data and dependency-graph entry, which only ever come
	// from a direct SetFile call on that file's own URI -- exactly what
	// scanIncludeDiscoveredFiles issues, and exactly what must be skipped
	// while the file is open in the editor.
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	defsPath := filepath.Join(dir, "defs.svh")
	writeFileT(t, topPath, "`include \"defs.svh\"\nmodule top;\nendmodule\n")
	writeFileT(t, defsPath, "typedef logic [7:0] bus_t;\ntypedef logic [7:0] disk_only_ref;\n")

	s := newTestServer()
	defsURI := pathToURI(defsPath)
	bufferText := "typedef logic [7:0] bus_t;\ntypedef logic [7:0] buffer_only_ref;\n"
	s.docs.Open(document.URI(defsURI), "systemverilog", 1, bufferText)
	s.Index().SetFile(defsURI, bufferText)

	d := &fakeDiscoverer{files: []workspace.SourceFile{
		{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)},
	}}

	s.rebuildIndex(context.Background(), d, nil)

	if locs := s.Index().Occurrences("buffer_only_ref"); len(locs) == 0 {
		t.Fatalf("expected the open buffer's own occurrence data for defs.svh to survive the rebuild")
	}
	if locs := s.Index().Occurrences("disk_only_ref"); len(locs) != 0 {
		t.Fatalf("expected the on-disk content to NOT overwrite the open buffer's occurrence data, got %+v", locs)
	}
}

func TestBuildIndexRescansOpenBufferWhenConfigChanges(t *testing.T) {
	// Unlike TestBuildIndexDoesNotOverwriteOpenBufferWithDiskContent (which
	// confirms the buffer's TEXT is never replaced by disk content), this
	// confirms the buffer is still re-SCANNED through SetFile on every
	// rebuild -- otherwise an open file's index entry keeps reflecting
	// whatever `+define+`/`+incdir+` config was active at its last
	// keystroke, even after a filelist edit changes it.
	dir := t.TempDir()
	path := filepath.Join(dir, "top.sv")
	bufferText := "`ifdef SYNTHESIS\nmodule synth_only; endmodule\n`endif\n"
	writeFileT(t, path, bufferText)

	s := newTestServer()
	uri := pathToURI(path)
	s.docs.Open(document.URI(uri), "systemverilog", 1, bufferText)
	s.Index().SetFile(uri, bufferText) // no SYNTHESIS defined yet, same as a real didOpen

	if _, ok := s.Index().Lookup("synth_only"); ok {
		t.Fatalf("expected synth_only to be undeclared before SYNTHESIS is defined")
	}

	d := &fakeDiscoverer{
		files:          []workspace.SourceFile{{LogicalPath: path, ResolvedPath: evalSymT(t, path)}},
		pendingDefines: map[string]string{"SYNTHESIS": ""},
	}
	s.buildIndex(context.Background(), d)

	if _, ok := s.Index().Lookup("synth_only"); !ok {
		t.Fatalf("expected the open buffer to be rescanned with the new +define+SYNTHESIS, declaring synth_only")
	}
}

func TestBuildIndexWiresIncludeResolverFromFilelistProvider(t *testing.T) {
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	writeFileT(t, topPath, "`include \"defs.svh\"\nmodule top;\n  bus_t data;\nendmodule\n")
	writeFileT(t, filepath.Join(dir, "defs.svh"), "typedef logic [7:0] bus_t;\n")

	s := newTestServer()
	d := &fakeDiscoverer{files: []workspace.SourceFile{
		{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)},
	}}

	s.buildIndex(context.Background(), d)

	if _, ok := s.Index().Lookup("bus_t"); !ok {
		t.Fatalf("expected bus_t (from the `include) to be indexed once buildIndex wires a resolver")
	}
}

func TestBuildIndexWiresIncludeDirsFromFilelistProvider(t *testing.T) {
	// Unlike TestBuildIndexWiresIncludeResolverFromFilelistProvider (where
	// defs.svh sits next to top.sv and resolves via the includer-relative
	// path alone), defs.svh here lives under a separate incdir -- the only
	// way this resolves is if buildIndex wires the resolver from
	// IncludeDirs() *after* Files() has populated it, not before. This is
	// the regression guard for that exact ordering bug (caught originally
	// by the slower end-to-end test against a real FilelistDiscoverer).
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	incDir := filepath.Join(dir, "common")
	writeFileT(t, topPath, "`include \"defs.svh\"\nmodule top;\n  bus_t data;\nendmodule\n")
	mkdirT(t, incDir)
	writeFileT(t, filepath.Join(incDir, "defs.svh"), "typedef logic [7:0] bus_t;\n")

	s := newTestServer()
	d := &fakeDiscoverer{
		files:              []workspace.SourceFile{{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)}},
		pendingIncludeDirs: []string{incDir},
	}

	s.buildIndex(context.Background(), d)

	if _, ok := s.Index().Lookup("bus_t"); !ok {
		t.Fatalf("expected bus_t (resolved via +incdir+) to be indexed once buildIndex wires IncludeDirs")
	}
}

func TestBuildIndexWiresInitialMacrosFromFilelistProvider(t *testing.T) {
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	writeFileT(t, topPath, "`ifdef SYNTHESIS\nmodule synth_only; endmodule\n`endif\n")

	s := newTestServer()
	d := &fakeDiscoverer{
		files:          []workspace.SourceFile{{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)}},
		pendingDefines: map[string]string{"SYNTHESIS": ""},
	}

	s.buildIndex(context.Background(), d)

	if _, ok := s.Index().Lookup("synth_only"); !ok {
		t.Fatalf("expected synth_only to be declared with the workspace's +define+SYNTHESIS wired in")
	}
}

func TestRebuildIndexScansFileDiscoveredOnlyViaInclude(t *testing.T) {
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	defsPath := filepath.Join(dir, "defs.svh")
	writeFileT(t, topPath, "`include \"defs.svh\"\nmodule top;\n  bus_t data;\nendmodule\n")
	writeFileT(t, defsPath, "typedef logic [7:0] bus_t;\n")

	s := newTestServer()
	// defs.svh is deliberately NOT in the discoverer's own file list --
	// the only way it's known is via top.sv's `include.
	d := &fakeDiscoverer{files: []workspace.SourceFile{
		{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)},
	}}

	files, _ := s.rebuildIndex(context.Background(), d, nil)

	// The resolver reports the as-written/as-joined path, not its
	// symlink-resolved form -- see resolver.go's doc comment on why (it
	// must land in the same URI "namespace" pathToURI(LogicalPath) uses
	// everywhere else in the index).
	defsURI := pathToURI(defsPath)
	found := false
	for _, f := range files {
		if pathToURI(f.LogicalPath) == defsURI {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected defs.svh to appear in rebuildIndex's returned file set (for watching), got %+v", files)
	}

	// A direct scan of defs.svh (not just declarations attributed to it
	// as a side effect of scanning top.sv) is what populates its own
	// occurrences -- confirm goto-def-style lookup finds it with a real
	// declaration site.
	locs, ok := s.Index().Lookup("bus_t")
	if !ok || len(locs) != 1 || locs[0].URI != defsURI {
		t.Fatalf("Lookup(bus_t) = %+v, %v, want one location in %s", locs, ok, defsURI)
	}
}

func TestBuildIndexPublishesDiagnosticsForMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.sv")
	writeFileT(t, path, "42;\nmodule top; endmodule\n")

	s := newTestServer()
	var notifications threadSafeNotifications
	s.setNotify(notifications.notify)
	d := &fakeDiscoverer{files: []workspace.SourceFile{
		{LogicalPath: path, ResolvedPath: evalSymT(t, path)},
	}}

	s.buildIndex(context.Background(), d)

	params, ok := notifications.diagnosticsFor(pathToURI(path))
	if !ok || len(params.Diagnostics) == 0 {
		t.Fatalf("expected buildIndex to publish a diagnostic for the malformed file, got %+v (found=%v)", params, ok)
	}
}

func TestScanIncludeDiscoveredFilesPublishesDiagnostics(t *testing.T) {
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	defsPath := filepath.Join(dir, "defs.svh")
	writeFileT(t, topPath, "`include \"defs.svh\"\nmodule top; endmodule\n")
	writeFileT(t, defsPath, "42;\n") // malformed on purpose, discovered only via `include

	s := newTestServer()
	var notifications threadSafeNotifications
	s.setNotify(notifications.notify)
	d := &fakeDiscoverer{files: []workspace.SourceFile{
		{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)},
	}}

	s.rebuildIndex(context.Background(), d, nil)

	params, ok := notifications.diagnosticsFor(pathToURI(defsPath))
	if !ok || len(params.Diagnostics) == 0 {
		t.Fatalf("expected scanIncludeDiscoveredFiles to publish a diagnostic for defs.svh, got %+v (found=%v)", params, ok)
	}
}

func TestRebuildIndexDropsIncludeDiscoveredFileOnceIncludeIsRemoved(t *testing.T) {
	dir := t.TempDir()
	topPath := filepath.Join(dir, "top.sv")
	defsPath := filepath.Join(dir, "defs.svh")
	writeFileT(t, topPath, "`include \"defs.svh\"\nmodule top; endmodule\n")
	writeFileT(t, defsPath, "typedef logic [7:0] bus_t;\n")

	s := newTestServer()
	d := &fakeDiscoverer{files: []workspace.SourceFile{
		{LogicalPath: topPath, ResolvedPath: evalSymT(t, topPath)},
	}}

	_, prev := s.rebuildIndex(context.Background(), d, nil)
	if _, ok := s.Index().Lookup("bus_t"); !ok {
		t.Fatalf("expected bus_t to be indexed after the first pass")
	}

	writeFileT(t, topPath, "module top; endmodule\n")
	s.rebuildIndex(context.Background(), d, prev)

	if _, ok := s.Index().Lookup("bus_t"); ok {
		t.Fatalf("expected bus_t to be dropped once top.sv no longer includes defs.svh")
	}
}
