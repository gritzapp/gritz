package configfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Dir returns the gritz config directory.
// It checks GRITZ_CONFIG_DIR first, then falls back to
// os.UserConfigDir()/gritz (e.g. ~/.config/gritz on Linux).
func Dir() (string, error) {
	if dir := os.Getenv("GRITZ_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config directory: %w", err)
	}
	return filepath.Join(dir, "gritz"), nil
}
