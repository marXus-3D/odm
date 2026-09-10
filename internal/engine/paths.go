package engine

import (
	"os"
	"path/filepath"
)

// DefaultDownloadDir picks the user Downloads folder, falling back to the
// working directory when the home directory is unavailable.
func DefaultDownloadDir() string {
	if v := os.Getenv("DM_DOWNLOAD_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Join(home, "Downloads")
}
