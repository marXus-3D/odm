package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeckoIDFromManifest covers reading the add-on id Firefox will use.
// Firefox ignores the "key" that fixes the Chromium id, so this is the only
// place its id can come from.
func TestGeckoIDFromManifest(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "manifest.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"present",
			`{"browser_specific_settings":{"gecko":{"id":"odm@example.org"}}}`,
			"odm@example.org",
		},
		{"no settings", `{"name":"ODM"}`, ""},
		{"no gecko", `{"browser_specific_settings":{}}`, ""},
		{"empty id", `{"browser_specific_settings":{"gecko":{"id":""}}}`, ""},
		{"not json", `not json at all`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := geckoIDFromManifest(write(c.body)); got != c.want {
				t.Errorf("geckoIDFromManifest = %q, want %q", got, c.want)
			}
		})
	}

	if got := geckoIDFromManifest(filepath.Join(dir, "absent.json")); got != "" {
		t.Errorf("a missing manifest gave %q, want \"\"", got)
	}
}

// TestGeckoManifestShape pins down the one thing that makes Firefox's
// manifest different from the Chromium one. Getting this wrong produces a
// file that looks right and that Firefox refuses every connection against.
func TestGeckoManifestShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, hostName+".firefox.json")
	exe := filepath.Join(dir, "odm-nmh.exe")

	if err := writeGeckoManifest(path, exe, []string{"odm@example.org"}); err != nil {
		t.Fatalf("writeGeckoManifest: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Decode loosely: the point is which keys are present, not just that the
	// struct round-trips through itself.
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
	if _, ok := raw["allowed_extensions"]; !ok {
		t.Error("no allowed_extensions: Firefox identifies callers by add-on id")
	}
	if _, ok := raw["allowed_origins"]; ok {
		t.Error("allowed_origins is the Chromium spelling and means nothing to Firefox")
	}
	if raw["name"] != hostName {
		t.Errorf("name = %v, want %s", raw["name"], hostName)
	}
	if raw["type"] != "stdio" {
		t.Errorf("type = %v, want stdio", raw["type"])
	}
	if raw["path"] != exe {
		t.Errorf("path = %v, want %s", raw["path"], exe)
	}

	var m geckoManifestFile
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.AllowedExtensions) != 1 || m.AllowedExtensions[0] != "odm@example.org" {
		t.Errorf("allowed_extensions = %v, want [odm@example.org]", m.AllowedExtensions)
	}
}

// TestChromiumManifestShape is the same guard from the other side: the two
// files are written by neighbouring functions and swapping a field between
// them would be easy and silent.
func TestChromiumManifestShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, hostName+".json")
	exe := filepath.Join(dir, "odm-nmh.exe")

	if err := writeHostManifest(path, exe, []string{"abcdefghijklmnopabcdefghijklmnop"}); err != nil {
		t.Fatalf("writeHostManifest: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
	if _, ok := raw["allowed_extensions"]; ok {
		t.Error("allowed_extensions is the Firefox spelling and means nothing to Chromium")
	}
	origins, ok := raw["allowed_origins"].([]any)
	if !ok || len(origins) != 1 {
		t.Fatalf("allowed_origins = %v, want one entry", raw["allowed_origins"])
	}
	// Chromium matches on the origin, not the bare id.
	if s, _ := origins[0].(string); !strings.HasPrefix(s, "chrome-extension://") ||
		!strings.HasSuffix(s, "/") {
		t.Errorf("origin = %q, want chrome-extension://<id>/", s)
	}
}

// TestRegistryRootsAreDistinct guards the pairing of manifest to registry
// root. Pointing Firefox's key at the Chromium manifest registers a host
// Firefox will never be allowed to start.
func TestRegistryRootsAreDistinct(t *testing.T) {
	for name, key := range geckoBrowsers {
		if !strings.Contains(key, `\Mozilla\NativeMessagingHosts\`) {
			t.Errorf("%s key = %q, want it under Mozilla\\NativeMessagingHosts", name, key)
		}
		if !strings.HasSuffix(key, hostName) {
			t.Errorf("%s key = %q, want it to end in the host name", name, key)
		}
	}
	for name, key := range browsers {
		if strings.Contains(key, `\Mozilla\`) {
			t.Errorf("%s is a Chromium browser but its key %q is under Mozilla", name, key)
		}
	}
	for name := range geckoBrowsers {
		if _, clash := browsers[name]; clash {
			t.Errorf("%s appears in both browser maps, so one registration overwrites the other", name)
		}
	}
}
