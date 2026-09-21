package endpoint

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/x509roots/fallback/bundle"
)

const isrgRootX1Fingerprint = "96bcec06264976f37460779acf28c5a7cfe8a3c0aae11a8ffcee05c0bddf08c6"

func TestAddMozillaRootsIncludesISRGRootX1(t *testing.T) {
	t.Parallel()

	pool := x509.NewCertPool()
	if err := addMozillaRoots(pool); err != nil {
		t.Fatalf("addMozillaRoots() error = %v", err)
	}

	root := findBundleRoot(t, isrgRootX1Fingerprint)
	if _, err := root.Verify(x509.VerifyOptions{
		Roots:       pool,
		CurrentTime: time.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Fatalf("verifying ISRG Root X1 with Mozilla roots: %v", err)
	}
}

func TestRootCertPoolAppendsCAFile(t *testing.T) {
	customRoot := newTestRoot(t)
	path := filepath.Join(t.TempDir(), "custom-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: customRoot.Raw,
	}), 0o600); err != nil {
		t.Fatalf("writing custom CA: %v", err)
	}

	pool, err := rootCertPool(path)
	if err != nil {
		t.Fatalf("rootCertPool() error = %v", err)
	}
	if _, err := customRoot.Verify(x509.VerifyOptions{
		Roots:       pool,
		CurrentTime: time.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Fatalf("verifying custom root: %v", err)
	}
}

func TestRootCertPoolRejectsInvalidCAFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("writing invalid CA: %v", err)
	}

	_, err := rootCertPool(path)
	if err == nil || !strings.Contains(err.Error(), "contains no certificates") {
		t.Fatalf("rootCertPool() error = %v, want invalid certificate error", err)
	}
}

func TestRootCertPoolReportsMissingCAFile(t *testing.T) {
	t.Parallel()

	_, err := rootCertPool(filepath.Join(t.TempDir(), "missing.pem"))
	if err == nil || !strings.Contains(err.Error(), "reading CA file") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rootCertPool() error = %v, want wrapped not-exist error", err)
	}
}

func findBundleRoot(t *testing.T, fingerprint string) *x509.Certificate {
	t.Helper()

	for root := range bundle.Roots() {
		sum := sha256.Sum256(root.Certificate)
		if hex.EncodeToString(sum[:]) != fingerprint {
			continue
		}
		cert, err := x509.ParseCertificate(root.Certificate)
		if err != nil {
			t.Fatalf("parsing bundle root: %v", err)
		}
		return cert
	}
	t.Fatalf("Mozilla bundle does not contain root %s", fingerprint)
	return nil
}

func newTestRoot(t *testing.T) *x509.Certificate {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Relaycat test root"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatalf("creating test certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing test certificate: %v", err)
	}
	return cert
}
