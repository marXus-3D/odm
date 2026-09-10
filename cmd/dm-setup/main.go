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
	"sort"
	"strings"

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
		check     = flag.Bool("check", false, "report on the current installation without changing it")
		extraIDs  = flag.String("extension-id", "", "comma separated extension ids to allow in addition to the ones detected")
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
	manifestPath := filepath.Join(ext, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		fatal("no manifest.json in %s -- pass -extension with the right path", ext)
	}

	nmh := filepath.Join(binDir, "dm-nmh.exe")
	hostManifest := filepath.Join(*stateDir, hostName+".json")

	if *check {
		runCheck(ext, manifestPath, nmh, hostManifest)
		return
	}

	if _, err := os.Stat(nmh); err != nil {
		fatal("dm-nmh.exe is not next to dm-setup (looked in %s)", binDir)
	}
	must(os.MkdirAll(*stateDir, 0o700), "create the state directory")

	pub, err := loadOrCreateKey(filepath.Join(*stateDir, "extension_key.pem"))
	must(err, "prepare the extension signing key")
	der, err := x509.MarshalPKIXPublicKey(pub)
	must(err, "encode the public key")
	must(writeManifestKey(manifestPath, base64.StdEncoding.EncodeToString(der)),
		"write the extension key")

	// Derive the id from the key that is actually in manifest.json now, not
	// from the key we meant to write. Chrome reads the manifest, so if the
	// two ever disagree the registration names an id that does not exist and
	// every connection fails with "forbidden".
	primary, err := idFromManifest(manifestPath)
	must(err, "read back the extension id")

	ids := []string{primary}
	ids = append(ids, idsFromPath(ext)...)
	for _, id := range strings.Split(*extraIDs, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	loaded := discoverLoadedIDs(ext)
	for _, l := range loaded {
		ids = append(ids, l.ID)
	}
	ids = dedupe(ids)

	must(writeHostManifest(hostManifest, nmh, ids), "write the native host manifest")
	registered := registerAll(hostManifest)

	fmt.Println("DM browser integration installed.")
	fmt.Println()
	fmt.Printf("  extension id     %s\n", primary)
	fmt.Printf("  extension folder %s\n", ext)
	fmt.Printf("  native host      %s\n", nmh)
	fmt.Printf("  host manifest    %s\n", hostManifest)
	if len(registered) > 0 {
		fmt.Printf("  registered for   %s\n", strings.Join(registered, ", "))
	} else {
		fmt.Println("  registered for   (none -- no browser registry keys could be written)")
	}
	if len(loaded) > 0 {
		fmt.Println()
		fmt.Println("  Already loaded in:")
		for _, l := range loaded {
			marker := ""
			if l.ID != primary {
				marker = "  <- different id, allowed as well"
			}
			fmt.Printf("    %-8s %-10s %s%s\n", l.Browser, l.Profile, l.ID, marker)
		}
	}
	if len(ids) > 1 {
		fmt.Printf("\n  allowing %d ids in total\n", len(ids))
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Open chrome://extensions (or edge://extensions)")
	fmt.Println("  2. Turn on Developer mode")
	fmt.Printf("  3. Load unpacked -> %s\n", ext)
	fmt.Println("  4. Press Reload on the DM card if it was already loaded")
	fmt.Println()
	fmt.Println("If you get \"Access to the specified native messaging host is")
	fmt.Println("forbidden\", the id changed: rerun dm-setup, then reload the")
	fmt.Println("extension. Run 'dm-setup -check' to see what is registered.")
}

// runCheck reports the state of an existing installation.
func runCheck(extDir, manifestPath, nmh, hostManifest string) {
	fmt.Println("DM browser integration check")
	fmt.Println()

	primary, err := idFromManifest(manifestPath)
	if err != nil {
		fmt.Printf("  manifest.json      ERROR: %v\n", err)
	} else {
		fmt.Printf("  manifest.json      key -> %s\n", primary)
	}

	if _, err := os.Stat(nmh); err != nil {
		fmt.Printf("  dm-nmh.exe         MISSING at %s\n", nmh)
	} else {
		fmt.Printf("  dm-nmh.exe         %s\n", nmh)
	}

	allowed := map[string]bool{}
	b, err := os.ReadFile(hostManifest)
	if err != nil {
		fmt.Printf("  host manifest      MISSING at %s\n", hostManifest)
	} else {
		var hm hostManifestFile
		if err := json.Unmarshal(b, &hm); err != nil {
			fmt.Printf("  host manifest      UNREADABLE: %v\n", err)
		} else {
			fmt.Printf("  host manifest      %s\n", hostManifest)
			for _, o := range hm.AllowedOrigins {
				id := strings.TrimSuffix(strings.TrimPrefix(o, "chrome-extension://"), "/")
				allowed[id] = true
				fmt.Printf("     allows          %s\n", id)
			}
			if hm.Path != nmh {
				fmt.Printf("     WARNING         points at %s\n", hm.Path)
			}
		}
	}

	fmt.Println()
	loaded := discoverLoadedIDs(extDir)
	if len(loaded) == 0 {
		fmt.Println("  Not loaded in any browser yet (or the browser has not")
		fmt.Println("  written its preferences to disk since you loaded it).")
	}
	problems := 0
	for _, l := range loaded {
		status := "ok"
		if !allowed[l.ID] {
			status = "NOT ALLOWED -- rerun dm-setup"
			problems++
		}
		fmt.Printf("  %-8s %-10s %s  %s\n", l.Browser, l.Profile, l.ID, status)
	}

	fmt.Println()
	for _, name := range sortedBrowserNames() {
		out, err := exec.Command("reg", "query", browsers[name], "/ve").CombinedOutput()
		if err != nil {
			fmt.Printf("  %-9s registry   not registered\n", name)
			continue
		}
		line := ""
		for _, l := range strings.Split(string(out), "\n") {
			if strings.Contains(l, "REG_SZ") {
				parts := strings.SplitN(strings.TrimSpace(l), "REG_SZ", 2)
				line = strings.TrimSpace(parts[len(parts)-1])
			}
		}
		fmt.Printf("  %-9s registry   %s\n", name, line)
	}

	if problems > 0 {
		fmt.Printf("\n%d loaded extension(s) are not in allowed_origins. Run dm-setup again.\n", problems)
		os.Exit(1)
	}
}

func sortedBrowserNames() []string {
	names := make([]string, 0, len(browsers))
	for n := range browsers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// loadOrCreateKey keeps one RSA key for the life of the install. The public
// half goes in the extension manifest, which is what fixes the extension id:
// without it Chrome derives the id from the folder path and the native host
// registration breaks the moment the folder moves.
func loadOrCreateKey(path string) (*rsa.PublicKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		if block, _ := pem.Decode(b); block != nil {
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

// idFromManifest derives the extension id from the key stored in the
// manifest, which is the only key Chrome ever sees.
func idFromManifest(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var m struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", fmt.Errorf("%s is not valid json: %w", path, err)
	}
	if m.Key == "" {
		return "", fmt.Errorf("%s has no \"key\"", path)
	}
	der, err := base64.StdEncoding.DecodeString(m.Key)
	if err != nil {
		return "", fmt.Errorf("the \"key\" in %s is not valid base64: %w", path, err)
	}
	return idFromKey(der), nil
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

type hostManifestFile struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

func writeHostManifest(path, exe string, ids []string) error {
	origins := make([]string, 0, len(ids))
	for _, id := range ids {
		origins = append(origins, "chrome-extension://"+id+"/")
	}
	m := hostManifestFile{
		Name:        hostName,
		Description: "DM download manager native host",
		Path:        exe,
		Type:        "stdio",
		// Only these extensions may launch the host; Chrome refuses anything
		// else, which is the point of the list.
		AllowedOrigins: origins,
	}
	b, err := marshalNoEscape(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// registerAll points every installed Chromium-family browser at the manifest.
// Browsers that are not installed simply get a key they will read if they
// ever are, so a failure here is not fatal.
func registerAll(manifestPath string) []string {
	var ok []string
	for _, name := range sortedBrowserNames() {
		cmd := exec.Command("reg", "add", browsers[name], "/ve", "/t", "REG_SZ",
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
	for _, name := range sortedBrowserNames() {
		if out, err := exec.Command("reg", "delete", browsers[name], "/f").CombinedOutput(); err != nil {
			_ = out
			fmt.Printf("%-9s not registered\n", name)
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
