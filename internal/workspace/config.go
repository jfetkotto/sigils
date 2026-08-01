package workspace

import (
	"encoding/json"
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
}

// LoadConfig reads .sigils.json from root. A missing config file is not an
// error -- it yields a zero-value Config (no filelists configured), since
// the server should still run without one.
func LoadConfig(root string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		if os.IsNotExist(err) {
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
