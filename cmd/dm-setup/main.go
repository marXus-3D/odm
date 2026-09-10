// Command dm-setup installs the browser integration.
//
// It pins the extension to a stable id by giving it an RSA public key, writes
// the native messaging host manifest that points Chrome at dm-nmh, and
// registers that manifest with every Chromium-family browser it finds.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/marcus/dm/internal/store"
)

const hostName = "com.dm.host"

// browsers maps a display name to the registry key where a Chromium-family
// browser looks for native messaging host manifests.
var browsers = map[string]string{
	"Chrome":   `HKCU\Software\Google\Chrome\NativeMessagingHosts\` + hostName,
	"Edge":     `HKCU\Software\Microsoft\Edge\NativeMessagingHosts\` + hostName,
	"Brave":    `HKCU\Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\` + hostName,
	"Chromium": `HKCU\Software\Chromium\NativeMessagingHosts\` + hostName,
	"Vivaldi":  `HKCU\Software\Vivaldi\NativeMessagingHosts\` + hostName,
	"Opera":    `HKCU\Software\Opera Software\NativeMessagingHosts\` + hostName,
}

func main() {
	var (
		extDir    = flag.String("extension", "", "path to the extension directory (default: ../extension next to this binary)")
		stateDir  = flag.String("state", store.StateDir(), "DM state directory")
		uninstall = flag.Bool("uninstall", false, "remove the native host registration")
	)
	flag.Parse()

	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "dm-setup currently registers native hosts on Windows only.")
		os.Exit(1)
	}

	if *uninstall {
		removeRegistrations()
		return
	}

	self, err := os.Executable()
	must(err, "locate this executable")
	binDir := filepath.Dir(self)

	if *extDir == "" {
		*extDir = filepath.Join(filepath.Dir(binDir), "extension")
	}
	ext, err := filepath.Abs(*extDir)
	must(err, "resolve the extension path")
	if _, err := os.Stat(filepath.Join(ext, "manifest.json")); err != nil {
		fatal("no manifest.json in %s -- pass -extension with the right path", ext)
	}

	nmh := filepath.Join(binDir, "dm-nmh.exe")
	if _, err := os.Stat(nmh); err != nil {
		fatal("dm-nmh.exe is not next to dm-setup (looked in %s)", binDir)
	}

	must(os.MkdirAll(*stateDir, 0o700), "create the state directory")

	pub, err := loadOrCreateKey(filepath.Join(*stateDir, "extension_key.pem"))
	must(err, "prepare the extension signing key")

	der, err := x509.MarshalPKIXPublicKey(pub)
	must(err, "encode the public key")
	keyB64 := base64.StdEncoding.EncodeToString(der)
	extID := extensionID(der)

	must(writeManifestKey(filepath.Join(ext, "manifest.json"), keyB64), "write the extension key")

	hostManifest := filepath.Join(*stateDir, hostName+".json")
	must(writeHostManifest(hostManifest, nmh, extID), "write the native host manifest")

	registered := registerAll(hostManifest)

	fmt.Println("DM browser integration installed.")
	fmt.Println()
	fmt.Printf("  extension id     %s\n", extID)
	fmt.Printf("  extension folder %s\n", ext)
	fmt.Printf("  native host      %s\n", nmh)
	fmt.Printf("  host manifest    %s\n", hostManifest)
	if len(registered) > 0 {
		fmt.Printf("  registered for   %v\n", registered)
	} else {
		fmt.Println("  registered for   (none -- no supported browser registry keys could be written)")
	}
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Open chrome://extensions (or edge://extensions)")
	fmt.Println("  2. Turn on Developer mode")
	fmt.Printf("  3. Load unpacked -> %s\n", ext)
	fmt.Printf("  4. Confirm the id shown matches %s\n", extID)
	fmt.Println()
	fmt.Println("The daemon starts on demand; nothing else needs to be running.")
}

// loadOrCreateKey keeps one RSA key for the life of the install. The public
// half goes in the extension manifest, which is what fixes the extension id:
// without it Chrome derives the id from the folder path and the native host
// registration breaks the moment the folder moves.
func loadOrCreateKey(path string) (*rsa.PublicKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(b)
		if block != nil {
			if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
				return &key.PublicKey, nil
			}
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return &key.PublicKey, nil
}

// extensionID reproduces Chrome's id derivation: the first 16 bytes of the
// SHA-256 of the DER public key, with each nibble mapped into a..p.
func extensionID(der []byte) string {
	sum := sha256.Sum256(der)
	out := make([]byte, 32)
	for i := 0; i < 16; i++ {
		out[i*2] = 'a' + (sum[i] >> 4)
		out[i*2+1] = 'a' + (sum[i] & 0xf)
	}
	return string(out)
}

// writeManifestKey adds or replaces the "key" field in the extension
// manifest, leaving every other field untouched.
func writeManifestKey(path, key string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("%s is not valid json: %w", path, err)
	}
	if existing, ok := m["key"].(string); ok && existing == key {
		return nil // already correct; do not churn the file
	}
	m["key"] = key
	out, err := marshalNoEscape(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// marshalNoEscape keeps values like "<all_urls>" readable. encoding/json
// escapes < and > for HTML safety by default, which is valid JSON but turns
// the manifest into line noise.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type hostManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

func writeHostManifest(path, exe, extID string) error {
	m := hostManifest{
		Name:        hostName,
		Description: "DM download manager native host",
		Path:        exe,
		Type:        "stdio",
		// Only our own extension may launch the host. Anything else Chrome
		// refuses to connect, which is the whole point of this list.
		AllowedOrigins: []string{"chrome-extension://" + extID + "/"},
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// registerAll points every installed Chromium-family browser at the manifest.
// Browsers that are not installed simply get a key they will read if they
// ever are, so a failure here is not fatal.
func registerAll(manifestPath string) []string {
	var ok []string
	for name, key := range browsers {
		cmd := exec.Command("reg", "add", key, "/ve", "/t", "REG_SZ",
			"/d", manifestPath, "/f")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not register for %s: %v: %s\n",
				name, err, out)
			continue
		}
		ok = append(ok, name)
	}
	return ok
}

func removeRegistrations() {
	for name, key := range browsers {
		cmd := exec.Command("reg", "delete", key, "/f")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("%-9s not registered\n", name)
			_ = out
			continue
		}
		fmt.Printf("%-9s removed\n", name)
	}
	fmt.Println("\nRemove the extension from chrome://extensions to finish uninstalling.")
}

func must(err error, what string) {
	if err != nil {
		fatal("could not %s: %v", what, err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dm-setup: "+format+"\n", args...)
	os.Exit(1)
}
