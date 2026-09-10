package hls

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sanitize strips path separators and characters Windows rejects.
func sanitize(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20, r == 0x7f:
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.TrimRight(b.String(), ". ")
	if name == "" || name == "." || name == ".." {
		return "video"
	}
	if len(name) > 180 {
		ext := filepath.Ext(name)
		name = name[:180-len(ext)] + ext
	}
	return name
}

// uniquePath returns a path that is not already taken, counting an
// in-progress download (its .dmh sidecar) as taken.
func uniquePath(dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	full := filepath.Join(dir, name)
	if !exists(full) {
		return full, nil
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if !exists(cand) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no free filename for %q in %s", name, dir)
}

func exists(p string) bool {
	if _, err := os.Stat(p); err == nil {
		return true
	}
	if _, err := os.Stat(p + sidecarSuffix); err == nil {
		return true
	}
	return false
}
