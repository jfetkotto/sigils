package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func evalSym(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return resolved
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFilelistDiscovererExpandsNestedFilelist(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "products", "Spu", "SpuTO1", "abc")

	writeFile(t, filepath.Join(abc, "top.f"), "# top-level filelist\n\nrtl.f\nextra.sv\n")
	writeFile(t, filepath.Join(abc, "rtl.f"), "core.sv\nsub/tb.sv\n")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")
	writeFile(t, filepath.Join(abc, "sub", "tb.sv"), "module tb; endmodule")
	writeFile(t, filepath.Join(abc, "extra.sv"), "module extra; endmodule")

	cfg := Config{Filelists: []string{"products/Spu/SpuTO1/abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, func(msg string) { t.Logf("warn: %s", msg) })

	files, err := d.Files(context.Background(), Root{Path: root})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	want := map[string]bool{
		evalSym(t, filepath.Join(abc, "core.sv")):      true,
		evalSym(t, filepath.Join(abc, "sub", "tb.sv")): true,
		evalSym(t, filepath.Join(abc, "extra.sv")):     true,
	}
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d: %+v", len(files), len(want), files)
	}
	for _, f := range files {
		if !want[f.ResolvedPath] {
			t.Fatalf("unexpected resolved path %s", f.ResolvedPath)
		}
	}
}

func TestFilelistDiscovererStripsTrailingComment(t *testing.T) {
	// A per-entry trailing "// comment" (a common VCS-style filelist
	// convention) must not glue onto the path -- previously it did,
	// EvalSymlinks failed, and the file was silently dropped from the
	// workspace view (a log warning only).
	root := t.TempDir()
	abc := filepath.Join(root, "abc")

	writeFile(t, filepath.Join(abc, "top.f"), "core.sv  // main product top\n")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")

	cfg := Config{Filelists: []string{"abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, func(msg string) { t.Fatalf("unexpected warning: %s", msg) })

	files, err := d.Files(context.Background(), Root{Path: root})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].ResolvedPath != evalSym(t, filepath.Join(abc, "core.sv")) {
		t.Fatalf("expected core.sv to resolve despite its trailing comment, got %+v", files)
	}
}

func TestFilelistDiscovererStripsTrailingCommentFromIncdir(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "abc")

	writeFile(t, filepath.Join(abc, "top.f"), "+incdir+"+filepath.Join(abc, "inc")+"  // shared headers\ncore.sv\n")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")

	cfg := Config{Filelists: []string{"abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, func(msg string) { t.Fatalf("unexpected warning: %s", msg) })

	if _, err := d.Files(context.Background(), Root{Path: root}); err != nil {
		t.Fatalf("Files: %v", err)
	}
	dirs := d.IncludeDirs()
	if len(dirs) != 1 || dirs[0] != filepath.Join(abc, "inc") {
		t.Fatalf("IncludeDirs() = %v, want [%s]", dirs, filepath.Join(abc, "inc"))
	}
}

func TestFilelistDiscovererFullLineCommentStillSkipped(t *testing.T) {
	// Regression guard: stripTrailingComment must not interfere with the
	// existing full-line "#"/"//" comment handling, which runs first.
	root := t.TempDir()
	abc := filepath.Join(root, "abc")

	writeFile(t, filepath.Join(abc, "top.f"), "// a full-line comment\n# another one\ncore.sv\n")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")

	cfg := Config{Filelists: []string{"abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, func(msg string) { t.Fatalf("unexpected warning: %s", msg) })

	files, err := d.Files(context.Background(), Root{Path: root})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].ResolvedPath != evalSym(t, filepath.Join(abc, "core.sv")) {
		t.Fatalf("expected only core.sv, got %+v", files)
	}
}

func TestFilelistDiscovererExpandsFlExtensionAsNestedFilelist(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "products", "Spu", "SpuTO1", "abc")

	writeFile(t, filepath.Join(abc, "top.fl"), "rtl.fl\n")
	writeFile(t, filepath.Join(abc, "rtl.fl"), "core.sv\n")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")

	cfg := Config{Filelists: []string{"products/Spu/SpuTO1/abc/top.fl"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, func(msg string) { t.Logf("warn: %s", msg) })

	files, err := d.Files(context.Background(), Root{Path: root})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].ResolvedPath != evalSym(t, filepath.Join(abc, "core.sv")) {
		t.Fatalf("expected core.sv resolved via the .fl chain, got %+v", files)
	}
}

func TestFilelistDiscovererDeduplicatesRepeatedEntries(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "abc")

	writeFile(t, filepath.Join(abc, "top.f"), "a.f\nb.f\n")
	writeFile(t, filepath.Join(abc, "a.f"), "shared.sv\n")
	writeFile(t, filepath.Join(abc, "b.f"), "shared.sv\n")
	writeFile(t, filepath.Join(abc, "shared.sv"), "module shared; endmodule")

	cfg := Config{Filelists: []string{"abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, nil)

	files, err := d.Files(context.Background(), Root{Path: root})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected shared.sv to be deduplicated, got %+v", files)
	}
}

func TestFilelistDiscovererDetectsCycles(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "abc")

	writeFile(t, filepath.Join(abc, "a.f"), "b.f\n")
	writeFile(t, filepath.Join(abc, "b.f"), "a.f\nreal.sv\n")
	writeFile(t, filepath.Join(abc, "real.sv"), "module real; endmodule")

	cfg := Config{Filelists: []string{"abc/a.f"}}
	var warnings []string
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, func(msg string) { warnings = append(warnings, msg) })

	done := make(chan struct{})
	var files []SourceFile
	var err error
	go func() {
		files, err = d.Files(context.Background(), Root{Path: root})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Files did not terminate -- likely an infinite loop on the a.f <-> b.f cycle")
	}

	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0].LogicalPath) != "real.sv" {
		t.Fatalf("expected exactly real.sv despite the cycle, got %+v", files)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected a warning about the cyclic filelist reference")
	}
}

func TestFilelistDiscovererResolvesSymlinkedRepo(t *testing.T) {
	// Mirrors the real layout: a workspace root whose ip/ repo folder is
	// actually a symlink into a separate (read-only cache) directory tree.
	root := t.TempDir()
	cache := t.TempDir()

	writeFile(t, filepath.Join(cache, "Spu-readonly", "rtl", "dut.sv"), "module dut; endmodule")
	if err := os.MkdirAll(filepath.Join(root, "ip"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkedRepo := filepath.Join(root, "ip", "Spu")
	if err := os.Symlink(filepath.Join(cache, "Spu-readonly"), symlinkedRepo); err != nil {
		t.Fatal(err)
	}

	abc := filepath.Join(root, "products", "Spu", "SpuTO1", "abc")
	writeFile(t, filepath.Join(abc, "top.f"), filepath.Join(symlinkedRepo, "rtl", "dut.sv")+"\n")

	cfg := Config{Filelists: []string{"products/Spu/SpuTO1/abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, nil)

	files, err := d.Files(context.Background(), Root{Path: root})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly one file, got %+v", files)
	}

	got := files[0]
	if got.LogicalPath != filepath.Join(symlinkedRepo, "rtl", "dut.sv") {
		t.Fatalf("LogicalPath = %q, want the symlinked path", got.LogicalPath)
	}
	wantResolved := evalSym(t, filepath.Join(cache, "Spu-readonly", "rtl", "dut.sv"))
	if got.ResolvedPath != wantResolved {
		t.Fatalf("ResolvedPath = %q, want %q (the real underlying file)", got.ResolvedPath, wantResolved)
	}
}

func TestFilelistDiscovererVisitedFilelists(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "abc")

	writeFile(t, filepath.Join(abc, "top.f"), "rtl.f\n")
	writeFile(t, filepath.Join(abc, "rtl.f"), "core.sv\n")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")

	cfg := Config{Filelists: []string{"abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, nil)

	if visited := d.VisitedFilelists(); len(visited) != 0 {
		t.Fatalf("expected no visited filelists before Files() runs, got %v", visited)
	}

	if _, err := d.Files(context.Background(), Root{Path: root}); err != nil {
		t.Fatalf("Files: %v", err)
	}

	visited := d.VisitedFilelists()
	want := map[string]bool{
		evalSym(t, filepath.Join(abc, "top.f")): true,
		evalSym(t, filepath.Join(abc, "rtl.f")): true,
	}
	if len(visited) != len(want) {
		t.Fatalf("VisitedFilelists() = %v, want %v", visited, want)
	}
	for _, p := range visited {
		if !want[p] {
			t.Fatalf("unexpected visited filelist %s", p)
		}
	}
}

func TestFilelistDiscovererToleratesEDAStyleSyntax(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "abc")

	// A realistic VCS-style filelist: comments, tool options to skip,
	// -f-referenced nested filelists (note the non-.f extension -- the
	// flag alone must force filelist treatment), and a -v library source.
	writeFile(t, filepath.Join(abc, "top.f"),
		"// tool options\n"+
			"+incdir+"+filepath.Join(abc, "inc")+"\n"+
			"+define+SYNTHESIS\n"+
			"-y "+filepath.Join(abc, "libdir")+"\n"+
			"-sverilog\n"+
			"-f nested.fl\n"+
			"-v lib_cell.v\n"+
			"core.sv\n")
	writeFile(t, filepath.Join(abc, "nested.fl"), "nested.sv\n")
	writeFile(t, filepath.Join(abc, "nested.sv"), "module nested; endmodule")
	writeFile(t, filepath.Join(abc, "lib_cell.v"), "module lib_cell; endmodule")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")

	cfg := Config{Filelists: []string{"abc/top.f"}}
	var warnings []string
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, func(msg string) { warnings = append(warnings, msg) })

	files, err := d.Files(context.Background(), Root{Path: root})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	want := map[string]bool{
		evalSym(t, filepath.Join(abc, "nested.sv")):  true,
		evalSym(t, filepath.Join(abc, "lib_cell.v")): true,
		evalSym(t, filepath.Join(abc, "core.sv")):    true,
	}
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d: %+v", len(files), len(want), files)
	}
	for _, f := range files {
		if !want[f.ResolvedPath] {
			t.Fatalf("unexpected resolved path %s", f.ResolvedPath)
		}
	}
	if len(warnings) != 0 {
		t.Fatalf("expected tool-option lines to be skipped without warnings, got %v", warnings)
	}

	wantIncdir := []string{filepath.Join(abc, "inc")}
	if got := d.IncludeDirs(); len(got) != 1 || got[0] != wantIncdir[0] {
		t.Fatalf("IncludeDirs() = %v, want %v", got, wantIncdir)
	}
	if len(d.Defines()) != 1 || d.Defines()["SYNTHESIS"] != "" {
		t.Fatalf("Defines() = %+v, want {SYNTHESIS: \"\"}", d.Defines())
	}
}

func TestFilelistDiscovererCapturesIncdirAndDefine(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "abc")

	writeFile(t, filepath.Join(abc, "top.f"),
		"+incdir+"+filepath.Join(abc, "inc1")+"\n"+
			"+incdir+"+filepath.Join(abc, "inc2")+"+"+filepath.Join(abc, "inc3")+"\n"+
			"+define+SYNTHESIS\n"+
			"+define+WIDTH=8+DEPTH=16\n"+
			"core.sv\n")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")

	cfg := Config{Filelists: []string{"abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, func(msg string) { t.Fatalf("unexpected warning: %s", msg) })

	if _, err := d.Files(context.Background(), Root{Path: root}); err != nil {
		t.Fatalf("Files: %v", err)
	}

	wantDirs := []string{
		filepath.Join(abc, "inc1"),
		filepath.Join(abc, "inc2"),
		filepath.Join(abc, "inc3"),
	}
	gotDirs := d.IncludeDirs()
	if len(gotDirs) != len(wantDirs) {
		t.Fatalf("IncludeDirs() = %v, want %v", gotDirs, wantDirs)
	}
	for i := range wantDirs {
		if gotDirs[i] != wantDirs[i] {
			t.Fatalf("IncludeDirs() = %v, want %v", gotDirs, wantDirs)
		}
	}

	wantDefines := map[string]string{"SYNTHESIS": "", "WIDTH": "8", "DEPTH": "16"}
	if gotDefines := d.Defines(); len(gotDefines) != len(wantDefines) {
		t.Fatalf("Defines() = %+v, want %+v", gotDefines, wantDefines)
	} else {
		for k, v := range wantDefines {
			if gotDefines[k] != v {
				t.Fatalf("Defines() = %+v, want %+v", gotDefines, wantDefines)
			}
		}
	}
}

func TestFilelistDiscovererDeduplicatesIncludeDirs(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "abc")

	writeFile(t, filepath.Join(abc, "top.f"),
		"+incdir+"+filepath.Join(abc, "inc")+"\n"+
			"+incdir+"+filepath.Join(abc, "inc")+"\n"+
			"core.sv\n")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")

	cfg := Config{Filelists: []string{"abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, nil)

	if _, err := d.Files(context.Background(), Root{Path: root}); err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got := d.IncludeDirs(); len(got) != 1 {
		t.Fatalf("expected the repeated +incdir+ to be deduplicated, got %v", got)
	}
}

func TestFilelistDiscovererExpandsEnvVars(t *testing.T) {
	root := t.TempDir()
	abc := filepath.Join(root, "abc")
	writeFile(t, filepath.Join(abc, "core.sv"), "module core; endmodule")
	t.Setenv("SIGILS_TEST_ABC", abc)
	writeFile(t, filepath.Join(abc, "top.f"), "${SIGILS_TEST_ABC}/core.sv\n")

	cfg := Config{Filelists: []string{"abc/top.f"}}
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, nil)

	files, err := d.Files(context.Background(), Root{Path: root})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].ResolvedPath != evalSym(t, filepath.Join(abc, "core.sv")) {
		t.Fatalf("expected ${SIGILS_TEST_ABC}/core.sv to resolve to core.sv, got %+v", files)
	}
}

func TestFilelistDiscovererWarnsOnMissingFilelist(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Filelists: []string{"does/not/exist.f"}}

	var warnings []string
	d := NewFilelistDiscoverer(Root{Path: root}, cfg, func(msg string) { warnings = append(warnings, msg) })

	files, err := d.Files(context.Background(), Root{Path: root})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no files, got %+v", files)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected a warning about the missing filelist")
	}
}
