package crx

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
)

// ParseKey reads a PEM-encoded RSA private key in either PKCS#1 or PKCS#8
// form, since dm-setup writes PKCS#1 but other tools produce PKCS#8.
func ParseKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := any.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the key is not RSA")
	}
	return key, nil
}
