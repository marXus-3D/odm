package crx

import (
	"archive/zip"
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// buildExtension writes a minimal extension directory.
func buildExtension(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"manifest.json":   `{"manifest_version":3,"name":"T","version":"1.0"}`,
		"background.js":   "console.log(1)",
		"icons/i16.png":   "notreallyapng",
		"private_key.pem": "-----BEGIN RSA PRIVATE KEY-----",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPackProducesVerifiableCRX(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := buildExtension(t)
	out := filepath.Join(t.TempDir(), "test.crx")

	id, err := Pack(dir, out, key)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if len(id) != 32 {
		t.Fatalf("id %q is not 32 characters", id)
	}
	for _, r := range id {
		if r < 'a' || r > 'p' {
			t.Fatalf("id %q has a character outside a..p", id)
		}
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, magic) {
		t.Fatalf("missing Cr24 magic, got % x", data[:8])
	}

	headerLen := binary.LittleEndian.Uint32(data[8:12])
	header := data[12 : 12+headerLen]
	zipped := data[12+headerLen:]

	// The zip must be readable, and must not carry the key file.
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		t.Fatalf("payload is not a zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if filepath.Ext(f.Name) == ".pem" {
			t.Errorf("the signing key was packed into the crx: %s", f.Name)
		}
	}
	if len(names) != 3 {
		t.Errorf("packed %v, want the three non-key files", names)
	}

	// Re-derive the signature the way Chrome does and check it verifies.
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	signedHeader := field(1, crxID(pub))
	if !bytes.Contains(header, signedHeader) {
		t.Fatal("the signed header data is not present in the crx header")
	}
	sig := extractSignature(t, header, pub)

	h := sha256.New()
	h.Write(signatureContext)
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(signedHeader)))
	h.Write(n[:])
	h.Write(signedHeader)
	h.Write(zipped)

	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h.Sum(nil), sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
}

// extractSignature pulls the signature bytes back out of the header, by
// finding the proof that carries our public key.
func extractSignature(t *testing.T, header, pub []byte) []byte {
	t.Helper()
	keyField := field(1, pub)
	i := bytes.Index(header, keyField)
	if i < 0 {
		t.Fatal("public key not found in the crx header")
	}
	rest := header[i+len(keyField):]
	if len(rest) < 2 || rest[0] != 0x12 {
		t.Fatalf("expected a signature field after the key, got % x", rest[:min(4, len(rest))])
	}
	length, n := binary.Uvarint(rest[1:])
	return rest[1+n : 1+n+int(length)]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestIDMatchesChromeDerivation(t *testing.T) {
	// A fixed key gives a fixed id; this pins the a..p mapping so a change
	// in it cannot silently break every native messaging registration.
	der := []byte{0x01, 0x02, 0x03}
	sum := sha256.Sum256(der)
	want := make([]byte, 32)
	for i := 0; i < 16; i++ {
		want[i*2] = 'a' + (sum[i] >> 4)
		want[i*2+1] = 'a' + (sum[i] & 0xf)
	}
	if got := ID(der); got != string(want) {
		t.Errorf("ID = %q, want %q", got, want)
	}
}

func TestParseKeyRoundTrip(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := []byte("-----BEGIN RSA PRIVATE KEY-----\n")
	_ = pemBytes
	der := x509.MarshalPKCS1PrivateKey(key)
	block := encodePEM("RSA PRIVATE KEY", der)
	got, err := ParseKey(block)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.N.Cmp(key.N) != 0 {
		t.Error("round-tripped key differs")
	}
}

func encodePEM(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
