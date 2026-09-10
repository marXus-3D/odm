package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var winReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeFilename strips path separators and characters NTFS rejects, and
// dodges the reserved DOS device names that still bite on Windows.
func SanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	// Never let a server-supplied name escape the target directory.
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20, r == 0x7f:
			// drop control characters
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name = b.String()

	// Windows silently drops trailing dots and spaces; do it explicitly so the
	// name we report matches the name on disk.
	name = strings.TrimRight(name, ". ")

	if name == "" || name == "." || name == ".." {
		return "download"
	}
	stem := name
	if i := strings.Index(name, "."); i > 0 {
		stem = name[:i]
	}
	if winReserved[strings.ToUpper(stem)] {
		name = "_" + name
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:200-len(ext)] + ext
	}
	return name
}

// UniquePath returns a path that does not exist yet, appending " (n)" before
// the extension the way browsers do. It also treats an in-progress download
// (path + ".dm" sidecar) as taken.
func UniquePath(dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	full := filepath.Join(dir, name)
	if !taken(full) {
		return full, nil
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if !taken(cand) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no free filename for %q in %s", name, dir)
}

func taken(p string) bool {
	if _, err := os.Stat(p); err == nil {
		return true
	}
	if _, err := os.Stat(p + metaSuffix); err == nil {
		return true
	}
	return false
}

// prealloc sizes the output file up front so the filesystem can pick
// contiguous extents instead of growing under N concurrent WriteAt calls.
func prealloc(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() == size {
		return nil
	}
	return f.Truncate(size)
}
