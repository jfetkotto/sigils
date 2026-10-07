// Package workspace finds a sigils workspace and the source files in it:
// FindRoot locates the root by its .sigils.json, LoadConfig reads that
// file, and a Discoverer expands its filelists into the set of files to
// index.
package workspace

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrRootNotFound is FindRoot's error when no directory at or above the
// starting point holds ConfigFileName.
var ErrRootNotFound = errors.New("workspace: no " + ConfigFileName + " found")

// FindRoot locates the workspace root by walking upward from start looking
// for ConfigFileName (.sigils.json), the way git walks up looking for .git
// -- ConfigFileName's mere presence marks the root, the same file whose
// contents LoadConfig later reads, so there's no separate marker file to
// keep in sync with it.
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, ConfigFileName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrRootNotFound
		}
		dir = parent
	}
}
