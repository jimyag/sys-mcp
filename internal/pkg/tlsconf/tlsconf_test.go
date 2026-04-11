package tlsconf_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jimyag/sys-mcp/internal/pkg/tlsconf"
)

// generateTestCert writes a self-signed cert+key pair and CA cert to dir.
func generateTestCert(t *testing.T, dir string) (certFile, keyFile, caFile string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	caFile = filepath.Join(dir, "ca.pem")

	writePEM(t, certFile, "CERTIFICATE", der)
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, keyFile, "EC PRIVATE KEY", keyBytes)
	writePEM(t, caFile, "CERTIFICATE", der)

	return certFile, keyFile, caFile
}

func writePEM(t *testing.T, path, typ string, data []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: data}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadServerTLS_ValidNoCa(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _ := generateTestCert(t, dir)

	cfg, err := tlsconf.LoadServerTLS(certFile, keyFile, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
}

func TestLoadServerTLS_WithCA(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile := generateTestCert(t, dir)

	cfg, err := tlsconf.LoadServerTLS(certFile, keyFile, caFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientCAs == nil {
		t.Fatal("expected ClientCAs to be set")
	}
}

func TestLoadServerTLS_MissingFile(t *testing.T) {
	_, err := tlsconf.LoadServerTLS("/nonexistent/cert.pem", "/nonexistent/key.pem", "")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadClientTLS_NoClientCert(t *testing.T) {
	cfg, err := tlsconf.LoadClientTLS("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 0 {
		t.Fatal("expected no certificates")
	}
	if cfg.RootCAs != nil {
		t.Fatal("expected nil RootCAs (system pool)")
	}
}

func TestLoadClientTLS_WithClientCertAndCA(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile := generateTestCert(t, dir)

	cfg, err := tlsconf.LoadClientTLS(certFile, keyFile, caFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatal("expected 1 client certificate")
	}
	if cfg.RootCAs == nil {
		t.Fatal("expected custom RootCAs")
	}
}

func TestLoadClientTLS_MissingCa(t *testing.T) {
	_, err := tlsconf.LoadClientTLS("", "", "/nonexistent/ca.pem")
	if err == nil {
		t.Fatal("expected error for missing CA file")
	}
}
