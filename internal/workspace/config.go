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

	// SemanticNavigation makes go-to-definition and find-references answer
	// from svparse's semantic model (scopes, imports, instance ports and
	// parameters) instead of by lexical lookup, falling back to the lexical
	// answer where the model has none. Off unless true: the lexical lookup
	// works on any text, including a file that is mid-edit, while the model
	// only resolves what parses.
	SemanticNavigation *bool `json:"semanticNavigation,omitempty"`

	// SemanticTokens turns semantic highlighting on or off. It is on unless
	// this is false.
	SemanticTokens *bool `json:"semanticTokens,omitempty"`

	// Lint tunes the lint rules, which run with the semantic diagnostics
	// (so they stop with them). Absent, every rule runs at its default
	// severity.
	Lint *LintSettings `json:"lint,omitempty"`
}

// LintSettings is the "lint" section of .sigils.json. Rules maps a rule name
// to "error", "warning", "info", "hint" or "off"; Ignore lists globs of files
// whose findings are dropped. The names are svparse's (`svparse lint
// --list-rules`).
type LintSettings struct {
	Enabled *bool             `json:"enabled,omitempty"`
	Rules   map[string]string `json:"rules,omitempty"`
	Ignore  []string          `json:"ignore,omitempty"`
}

// SemanticNavigationEnabled reports whether definition and references use the
// semantic model first.
func (c Config) SemanticNavigationEnabled() bool {
	return c.SemanticNavigation != nil && *c.SemanticNavigation
}

// SemanticTokensEnabled reports whether semantic highlighting is on.
func (c Config) SemanticTokensEnabled() bool {
	return c.SemanticTokens == nil || *c.SemanticTokens
}

// LintEnabled reports whether lint findings are published.
func (c Config) LintEnabled() bool {
	return c.Lint == nil || c.Lint.Enabled == nil || *c.Lint.Enabled
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
