package workspace

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ConfigFileName is checked in at the workspace root and names the
// product-specific filelist entry points. Its presence there also doubles
// as the workspace-root marker FindRoot looks for -- see root.go.
const ConfigFileName = ".sigils.json"

// Config is the on-disk shape of .sigils.json.
type Config struct {
	// Filelists is one or more top-level filelist paths, relative to the
	// workspace root, each a recursive filelist-of-filelists entry point
	// (e.g. "products/Spu/SpuTO1/abc/top.f"). See Discoverer for how
	// they get resolved.
	Filelists []string `json:"filelists"`

	// SemanticDiagnostics turns the semantic checks (unresolved names,
	// illegal assignments, duplicate definitions and so on) on or off. They
	// are on unless this is false: a workspace whose dependencies are not all
	// in the index may prefer the syntax errors alone.
	SemanticDiagnostics *bool `json:"semanticDiagnostics,omitempty"`
}

// SemanticDiagnosticsEnabled reports whether semantic diagnostics are on.
func (c Config) SemanticDiagnosticsEnabled() bool {
	return c.SemanticDiagnostics == nil || *c.SemanticDiagnostics
}

// LoadConfig reads .sigils.json from root. A missing config file is not an
// error -- it yields a zero-value Config (no filelists configured), since
// the server should still run without one.
func LoadConfig(root string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
