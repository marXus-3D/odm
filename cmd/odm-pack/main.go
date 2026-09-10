// Command odm-pack signs the extension into a .crx for the installer to
// ship. It is a build-time tool, not something the user ever runs.
//
// The private key stays on the build machine. What ships is the public key
// inside manifest.json plus a signature over the zip, which together fix
// the extension id: the packaged extension, the .crx and the native
// messaging registration all agree on one id.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/marXus-3D/odm/internal/crx"
)

func main() {
	extDir := flag.String("extension", "extension", "the extension folder to pack")
	out := flag.String("out", "dist/odm.crx", "where to write the signed .crx")
	keyPath := flag.String("key", "", "signing key (default: the key odm-setup uses)")
	flag.Parse()

	if *keyPath == "" {
		*keyPath = defaultKeyPath()
	}

	key, err := loadOrCreateKey(*keyPath)
	check(err, "prepare the signing key")

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	check(err, "encode the public key")
	encoded := base64.StdEncoding.EncodeToString(der)

	manifest := filepath.Join(*extDir, "manifest.json")
	current, _ := manifestKey(manifest)
	if current != encoded {
		// The manifest must carry the key we are about to sign with, or
		// Chrome will compute a different id than the .crx installs under.
		check(setManifestKey(manifest, encoded), "write the key into the manifest")
		fmt.Printf("updated %s with the signing key\n", manifest)
	}

	check(os.MkdirAll(filepath.Dir(*out), 0o755), "create the output folder")
	id, err := crx.Pack(*extDir, *out, key)
	check(err, "pack the extension")

	info, err := os.Stat(*out)
	check(err, "stat the packed extension")
	fmt.Printf("packed %s (%.1f KB)\n", *out, float64(info.Size())/1024)
	fmt.Printf("extension id %s\n", id)
}

// defaultKeyPath is the key odm-setup generates, so a machine that has been
// developing against a loaded unpacked extension keeps the same id.
func defaultKeyPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "extension_key.pem"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "odm", "extension_key.pem")
}

func loadOrCreateKey(path string) (*rsa.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("%s is not a PEM file", path)
		}
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		return key, nil
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	encoded := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, err
	}
	fmt.Printf("generated a new signing key at %s -- keep it, it fixes the extension id\n", path)
	return key, nil
}

func manifestKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var m struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", err
	}
	return m.Key, nil
}

// setManifestKey rewrites just the "key" member, leaving the rest of the
// file — comments in the surrounding tooling aside, its formatting and
// member order — exactly as the author wrote it.
func setManifestKey(path, key string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(b)
	line := `"key": "` + key + `"`

	re := regexp.MustCompile(`"key"\s*:\s*"[^"]*"`)
	if re.MatchString(text) {
		text = re.ReplaceAllString(text, line)
	} else {
		i := strings.Index(text, "{")
		if i < 0 {
			return fmt.Errorf("%s does not look like json", path)
		}
		text = text[:i+1] + "\n  " + line + "," + text[i+1:]
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return fmt.Errorf("the edit broke %s: %w", path, err)
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func check(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "odm-pack: could not %s: %v\n", what, err)
		os.Exit(1)
	}
}
