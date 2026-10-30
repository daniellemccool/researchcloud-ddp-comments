package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// ensureCA loads the CA from certPath/keyPath, or generates a fresh 2048-bit
// RSA self-signed CA and writes it there on first run. If either file is
// missing both are regenerated together, so cert and key always match.
func ensureCA(certPath, keyPath string) (*tls.Certificate, error) {
	if fileExists(certPath) && fileExists(keyPath) {
		if err := fixModes(certPath, keyPath); err != nil {
			return nil, err
		}
		return loadCA(certPath, keyPath)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("CA serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "deskproxy-ca", Organization: []string{"deskproxy"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return nil, err
	}
	// Write through a fresh 0600 temp file and rename, so the key never
	// lands in an old, more permissive inode.
	if err := writeAtomic(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", keyPath, err)
	}
	if err := writeAtomic(certPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", certPath, err)
	}
	return loadCA(certPath, keyPath)
}

func loadCA(certPath, keyPath string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load CA: %w", err)
	}
	return &cert, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// fixModes makes an existing CA readable as intended: key 0600, cert 0644.
func fixModes(certPath, keyPath string) error {
	if err := os.Chmod(keyPath, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", keyPath, err)
	}
	if err := os.Chmod(certPath, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", certPath, err)
	}
	return nil
}

// writeAtomic writes data to a 0600 temp file in path's directory, sets mode
// and renames it over path.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after a successful rename
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
