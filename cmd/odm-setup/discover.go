package main

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
)

// chromiumID maps a 32-byte digest into Chrome's a..p extension id alphabet.
func chromiumID(sum [32]byte) string {
	out := make([]byte, 32)
	for i := 0; i < 16; i++ {
		out[i*2] = 'a' + (sum[i] >> 4)
		out[i*2+1] = 'a' + (sum[i] & 0xf)
	}
	return string(out)
}

// idFromKey is the id Chrome computes for an extension whose manifest carries
// a "key": the SHA-256 of the DER public key.
func idFromKey(der []byte) string {
	return chromiumID(sha256.Sum256(der))
}

// idsFromPath are the ids Chrome computes for an unpacked extension with no
// "key" in its manifest, where the absolute directory path is hashed instead.
//
// Windows hashes the UTF-16 bytes of the path; other platforms hash the raw
// bytes. Both are returned because an extension loaded before the key was
// added keeps its path-derived id until it is removed and re-added, and
// allowing an id we do not use costs nothing.
func idsFromPath(dir string) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	var wide []byte
	for _, r := range utf16.Encode([]rune(abs)) {
		wide = append(wide, byte(r), byte(r>>8))
	}
	return []string{
		chromiumID(sha256.Sum256(wide)),
		chromiumID(sha256.Sum256([]byte(abs))),
	}
}

// browserProfileRoots lists the "User Data" directories of the Chromium-family
// browsers we know about.
func browserProfileRoots() map[string]string {
	local := os.Getenv("LOCALAPPDATA")
	roaming := os.Getenv("APPDATA")
	roots := map[string]string{}
	add := func(name, path string) {
		if path == "" {
			return
		}
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			roots[name] = path
		}
	}
	if local != "" {
		add("Chrome", filepath.Join(local, "Google", "Chrome", "User Data"))
		add("Chrome Beta", filepath.Join(local, "Google", "Chrome Beta", "User Data"))
		add("Chrome Canary", filepath.Join(local, "Google", "Chrome SxS", "User Data"))
		add("Edge", filepath.Join(local, "Microsoft", "Edge", "User Data"))
		add("Brave", filepath.Join(local, "BraveSoftware", "Brave-Browser", "User Data"))
		add("Chromium", filepath.Join(local, "Chromium", "User Data"))
		add("Vivaldi", filepath.Join(local, "Vivaldi", "User Data"))
	}
	if roaming != "" {
		add("Opera", filepath.Join(roaming, "Opera Software", "Opera Stable"))
		add("Opera GX", filepath.Join(roaming, "Opera Software", "Opera GX Stable"))
	}
	return roots
}

// LoadedExtension is an unpacked extension a browser has on record.
type LoadedExtension struct {
	Browser string
	Profile string
	ID      string
}

// discoverLoadedIDs finds the ids browsers actually assigned to the extension
// living at extDir.
//
// This is the difference between an install that works and one that fails
// with "Access to the specified native messaging host is forbidden": Chrome
// decides the id, and if our allowed_origins names a different one, every
// connection is refused. Rather than assume, ask the browser.
func discoverLoadedIDs(extDir string) []LoadedExtension {
	want, err := filepath.Abs(extDir)
	if err != nil {
		want = extDir
	}
	want = strings.ToLower(filepath.Clean(want))

	var found []LoadedExtension
	seen := map[string]bool{}

	for browser, root := range browserProfileRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Profile directories are "Default", "Profile 1", and so on.
			name := e.Name()
			if name != "Default" && !strings.HasPrefix(name, "Profile ") {
				continue
			}
			for _, file := range []string{"Secure Preferences", "Preferences"} {
				ids := idsInPrefs(filepath.Join(root, name, file), want)
				for _, id := range ids {
					k := browser + "/" + name + "/" + id
					if seen[k] {
						continue
					}
					seen[k] = true
					found = append(found, LoadedExtension{
						Browser: browser, Profile: name, ID: id,
					})
				}
			}
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Browser != found[j].Browser {
			return found[i].Browser < found[j].Browser
		}
		return found[i].Profile < found[j].Profile
	})
	return found
}

// idsInPrefs reads one preferences file and returns the ids of extensions
// installed from wantPath.
func idsInPrefs(path, wantPath string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// These files are large; decode only what we need.
	var doc struct {
		Extensions struct {
			Settings map[string]struct {
				Path string `json:"path"`
			} `json:"settings"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil
	}

	var out []string
	for id, cfg := range doc.Extensions.Settings {
		if cfg.Path == "" || !validExtensionID(id) {
			continue
		}
		got := strings.ToLower(filepath.Clean(cfg.Path))
		if got == wantPath {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// validExtensionID guards against treating some other settings key as an id.
func validExtensionID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'p' {
			return false
		}
	}
	return true
}
