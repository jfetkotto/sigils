package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRootWalksUpward(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "ip", "Spu", "rtl")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindRoot(nested)
	if err != nil {
		t.Fatalf("FindRoot: %v", err)
	}
	want, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Fatalf("FindRoot = %q, want %q", got, root)
	}
}

func TestFindRootWalksUpwardThroughSymlinkedRepo(t *testing.T) {
	// Mirrors the real layout: a workspace root containing an ip/ repo
	// folder that's actually a symlink into a separate (read-only cache)
	// directory tree.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cache := t.TempDir()
	cachedRepo := filepath.Join(cache, "Spu-readonly")
	if err := os.MkdirAll(filepath.Join(cachedRepo, "rtl"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "ip"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "ip", "Spu")
	if err := os.Symlink(cachedRepo, symlink); err != nil {
		t.Fatal(err)
	}

	got, err := FindRoot(filepath.Join(symlink, "rtl"))
	if err != nil {
		t.Fatalf("FindRoot: %v", err)
	}
	want, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Fatalf("FindRoot = %q, want %q", got, root)
	}
}

func TestFindRootNotFound(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindRoot(dir); err != ErrRootNotFound {
		t.Fatalf("expected ErrRootNotFound, got %v", err)
	}
}
