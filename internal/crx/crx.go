// Package crx packs a directory into a signed CRX3 extension.
//
// CRX3 is a small header in front of an ordinary zip: a magic number, a
// protobuf describing the signatures, then the zip itself. The protobuf is
// hand-encoded here rather than pulling in a protobuf runtime, because the
// three messages involved are a handful of length-delimited fields.
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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// magic is "Cr24" followed by format version 3.
var magic = []byte{'C', 'r', '2', '4', 3, 0, 0, 0}

// signatureContext is prepended to the bytes that get signed, so a
// signature cannot be replayed in another context.
var signatureContext = []byte("CRX3 SignedData\x00")

// ID returns the extension id Chrome derives from a public key: the first
// 16 bytes of its SHA-256, with each nibble mapped into a..p.
func ID(der []byte) string {
	sum := sha256.Sum256(der)
	out := make([]byte, 32)
	for i := 0; i < 16; i++ {
		out[i*2] = 'a' + (sum[i] >> 4)
		out[i*2+1] = 'a' + (sum[i] & 0xf)
	}
	return string(out)
}

// crxID is the raw 16-byte form of the same value, which is what goes in
// the signed header.
func crxID(der []byte) []byte {
	sum := sha256.Sum256(der)
	return sum[:16]
}

// Pack zips dir and writes a signed CRX3 to out. It returns the extension
// id the result will install under.
func Pack(dir, out string, key *rsa.PrivateKey) (string, error) {
	zipped, err := zipDir(dir)
	if err != nil {
		return "", fmt.Errorf("zip %s: %w", dir, err)
	}

	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}

	// SignedData { bytes crx_id = 1 }
	signedHeader := field(1, crxID(pub))

	// The signature covers the context, the length of the signed header,
	// the signed header itself, and then the zip.
	h := sha256.New()
	h.Write(signatureContext)
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(signedHeader)))
	h.Write(n[:])
	h.Write(signedHeader)
	h.Write(zipped)

	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}

	// AsymmetricKeyProof { bytes public_key = 1; bytes signature = 2 }
	proof := append(field(1, pub), field(2, sig)...)

	// CrxFileHeader {
	//   repeated AsymmetricKeyProof sha256_with_rsa = 2;
	//   bytes signed_header_data = 10000;
	// }
	header := append(field(2, proof), field(10000, signedHeader)...)

	var buf bytes.Buffer
	buf.Write(magic)
	binary.Write(&buf, binary.LittleEndian, uint32(len(header)))
	buf.Write(header)
	buf.Write(zipped)

	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return ID(pub), nil
}

// field encodes one length-delimited protobuf field.
func field(number int, data []byte) []byte {
	var b []byte
	b = appendVarint(b, uint64(number)<<3|2) // wire type 2
	b = appendVarint(b, uint64(len(data)))
	return append(b, data...)
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// zipDir packs a directory with forward-slash paths relative to its root,
// which is what Chrome expects inside a CRX.
func zipDir(dir string) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// A stray key file would hand out the signing key with the
		// extension, so never let one in.
		if strings.HasSuffix(strings.ToLower(rel), ".pem") {
			return nil
		}

		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Method = zip.Deflate

		out, err := w.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// LoadKey reads a PKCS#1 private key from a PEM file.
func LoadKey(path string) (*rsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseKey(b)
}
