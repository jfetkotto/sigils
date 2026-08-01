package lspserver

import (
	"os"
	"path/filepath"
	"testing"
)

// mkdirT creates dir (and any missing parents), since writeFileT --
// unlike workspace's own test helper of the same name -- doesn't.
func mkdirT(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestIncludeResolverIncluderRelative(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "top.sv"), "// root\n")
	writeFileT(t, filepath.Join(dir, "defs.svh"), "`define WIDTH 8\n")

	r := newIncludeResolverFactory(nil)()
	text, resolvedURI, err := r.Resolve("defs.svh", pathToURI(filepath.Join(dir, "top.sv")))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if text != "`define WIDTH 8\n" {
		t.Fatalf("unexpected text: %q", text)
	}
	if resolvedURI != pathToURI(filepath.Join(dir, "defs.svh")) {
		t.Fatalf("resolvedURI = %q, want defs.svh's own URI", resolvedURI)
	}
}

func TestIncludeResolverFallsBackToIncdir(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	incDir := filepath.Join(root, "inc")
	mkdirT(t, srcDir)
	mkdirT(t, incDir)
	writeFileT(t, filepath.Join(srcDir, "top.sv"), "// root\n")
	writeFileT(t, filepath.Join(incDir, "defs.svh"), "`define WIDTH 8\n")

	r := newIncludeResolverFactory([]string{incDir})()
	_, resolvedURI, err := r.Resolve("defs.svh", pathToURI(filepath.Join(srcDir, "top.sv")))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolvedURI != pathToURI(filepath.Join(incDir, "defs.svh")) {
		t.Fatalf("resolvedURI = %q, want the incdir-resolved defs.svh", resolvedURI)
	}
}

func TestIncludeResolverIncluderRelativeWinsOverIncdir(t *testing.T) {
	// Both a local and an incdir copy of the same filename exist -- the
	// includer-relative one must win, matching real `include semantics.
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	incDir := filepath.Join(root, "inc")
	mkdirT(t, srcDir)
	mkdirT(t, incDir)
	writeFileT(t, filepath.Join(srcDir, "top.sv"), "// root\n")
	writeFileT(t, filepath.Join(srcDir, "defs.svh"), "`define WIDTH 8\n")
	writeFileT(t, filepath.Join(incDir, "defs.svh"), "`define WIDTH 16\n")

	r := newIncludeResolverFactory([]string{incDir})()
	text, _, err := r.Resolve("defs.svh", pathToURI(filepath.Join(srcDir, "top.sv")))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if text != "`define WIDTH 8\n" {
		t.Fatalf("expected the includer-relative copy to win, got %q", text)
	}
}

func TestIncludeResolverNotFoundReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "top.sv"), "// root\n")

	r := newIncludeResolverFactory(nil)()
	if _, _, err := r.Resolve("missing.svh", pathToURI(filepath.Join(dir, "top.sv"))); err == nil {
		t.Fatalf("expected an error for an unresolvable include")
	}
}

func TestIncludeResolverTracksResolvedPaths(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "top.sv"), "// root\n")
	writeFileT(t, filepath.Join(dir, "a.svh"), "// a\n")
	writeFileT(t, filepath.Join(dir, "b.svh"), "// b\n")

	r := newIncludeResolverFactory(nil)()
	topURI := pathToURI(filepath.Join(dir, "top.sv"))
	if _, _, err := r.Resolve("a.svh", topURI); err != nil {
		t.Fatalf("Resolve(a.svh): %v", err)
	}
	if _, _, err := r.Resolve("b.svh", topURI); err != nil {
		t.Fatalf("Resolve(b.svh): %v", err)
	}

	resolved := r.Resolved()
	want := map[string]bool{
		pathToURI(filepath.Join(dir, "a.svh")): true,
		pathToURI(filepath.Join(dir, "b.svh")): true,
	}
	if len(resolved) != len(want) {
		t.Fatalf("Resolved() = %v, want %v", resolved, want)
	}
	for _, uri := range resolved {
		if !want[uri] {
			t.Fatalf("unexpected resolved URI %s", uri)
		}
	}
}

func TestIncludeResolverDeduplicatesResolvedPaths(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "top.sv"), "// root\n")
	writeFileT(t, filepath.Join(dir, "a.svh"), "// a\n")

	r := newIncludeResolverFactory(nil)()
	topURI := pathToURI(filepath.Join(dir, "top.sv"))
	r.Resolve("a.svh", topURI)
	r.Resolve("a.svh", topURI)

	if resolved := r.Resolved(); len(resolved) != 1 {
		t.Fatalf("Resolved() = %v, want exactly 1 entry", resolved)
	}
}

func TestIncludeResolverFreshInstancePerCall(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "top.sv"), "// root\n")
	writeFileT(t, filepath.Join(dir, "a.svh"), "// a\n")

	factory := newIncludeResolverFactory(nil)
	r1 := factory()
	r1.Resolve("a.svh", pathToURI(filepath.Join(dir, "top.sv")))
	r2 := factory()

	if resolved := r2.Resolved(); len(resolved) != 0 {
		t.Fatalf("expected a fresh resolver from the factory to start with no resolved paths, got %v", resolved)
	}
}
