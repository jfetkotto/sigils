package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	root := t.TempDir()
	contents := `{"filelists": ["products/Spu/SpuTO1/abc/top.f"]}`
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Filelists) != 1 || cfg.Filelists[0] != "products/Spu/SpuTO1/abc/top.f" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConfigMissingFileIsNotAnError(t *testing.T) {
	root := t.TempDir()

	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Filelists) != 0 {
		t.Fatalf("expected zero-value config, got %+v", cfg)
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig(root); err == nil {
		t.Fatalf("expected an error for invalid JSON")
	}
}
