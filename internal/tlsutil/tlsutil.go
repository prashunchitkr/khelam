// Package tlsutil manages the server's TLS certificate and lets agents
// pin it by fingerprint.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const selfSignedValidity = 10 * 365 * 24 * time.Hour

// Fingerprint returns the SHA-256 fingerprint of a DER certificate in
// the form "sha256:<hex>".
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// LoadOrCreateSelfSigned loads the certificate at certFile and keyFile,
// or generates and saves a new self-signed one if neither exists, so the
// fingerprint stays stable across restarts.
func LoadOrCreateSelfSigned(certFile, keyFile string) (tls.Certificate, error) {
	_, certErr := os.Stat(certFile)
	_, keyErr := os.Stat(keyFile)
	if certErr == nil && keyErr == nil {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}
	if !errors.Is(certErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return tls.Certificate{}, fmt.Errorf("incomplete certificate pair at %s, %s", certFile, keyFile)
	}

	certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}

	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}

// NewSelfSigned generates a self-signed certificate held only in memory.
func NewSelfSigned() (tls.Certificate, error) {
	certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

func generateSelfSigned() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "khelam server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(selfSignedValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// FingerprintMismatchError is returned when the server presents a
// certificate other than the pinned one.
type FingerprintMismatchError struct {
	Want, Got string
}

func (e *FingerprintMismatchError) Error() string {
	return fmt.Sprintf("server certificate fingerprint %s does not match pinned %s", e.Got, e.Want)
}

// PinnedConfig returns a client config that trusts exactly the server
// certificate with the given fingerprint, instead of a CA chain.
func PinnedConfig(fingerprint string) *tls.Config {
	return &tls.Config{
		// Chain verification is replaced by the fingerprint check below.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server presented no certificate")
			}
			got := Fingerprint(cs.PeerCertificates[0].Raw)
			if got != fingerprint {
				return &FingerprintMismatchError{Want: fingerprint, Got: got}
			}
			return nil
		},
	}
}
