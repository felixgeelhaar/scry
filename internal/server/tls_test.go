package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBuildTLSConfigLoadsCertAndKey asserts the happy path: a valid
// PEM pair on disk produces a usable tls.Config with our cert in it.
func TestBuildTLSConfigLoadsCertAndKey(t *testing.T) {
	certPath, keyPath, _ := writeSelfSignedCert(t)
	cfg := Config{TLSCertFile: certPath, TLSKeyFile: keyPath}
	tcfg, err := buildTLSConfig(cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if tcfg == nil || len(tcfg.Certificates) != 1 {
		t.Fatalf("expected 1 cert, got %+v", tcfg)
	}
	if tcfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("min TLS version should be 1.2, got %d", tcfg.MinVersion)
	}
	if tcfg.ClientAuth != tls.NoClientCert {
		t.Errorf("ClientAuth should be NoClientCert without mTLS, got %d", tcfg.ClientAuth)
	}
}

// TestBuildTLSConfigEnablesMTLS asserts that --mtls-ca produces a
// tls.Config requiring client certs from the given CA pool.
func TestBuildTLSConfigEnablesMTLS(t *testing.T) {
	certPath, keyPath, certPEM := writeSelfSignedCert(t)
	// Reuse the same self-signed cert as both the server cert AND
	// the CA bundle — the test only cares that ClientAuth flips on
	// + ClientCAs gets populated.
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatalf("write CA: %v", err)
	}
	cfg := Config{TLSCertFile: certPath, TLSKeyFile: keyPath, MTLSCAFile: caPath}
	tcfg, err := buildTLSConfig(cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if tcfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth should be RequireAndVerifyClientCert with mTLS, got %d", tcfg.ClientAuth)
	}
	if tcfg.ClientCAs == nil {
		t.Errorf("ClientCAs should be populated for mTLS")
	}
}

func TestBuildTLSConfigRejectsHalfConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"cert without key", Config{TLSCertFile: "cert.pem"}},
		{"mtls without cert", Config{MTLSCAFile: "ca.pem"}},
		{"key without cert", Config{TLSKeyFile: "key.pem"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := buildTLSConfig(c.cfg); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

// TestBuildTLSConfigDisabledWhenNoCert returns (nil, nil) — caller
// uses the result as a signal to skip TLS wiring.
func TestBuildTLSConfigDisabledWhenNoCert(t *testing.T) {
	tcfg, err := buildTLSConfig(Config{})
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	if tcfg != nil {
		t.Errorf("expected nil tls.Config when disabled, got %+v", tcfg)
	}
}

// TestServeHTTPWithTLSBinds confirms the end-to-end wiring: boot the
// HTTP transport with TLS enabled, dial it with a TLS client that
// trusts the self-signed cert, observe a successful handshake.
func TestServeHTTPWithTLSBinds(t *testing.T) {
	certPath, keyPath, certPEM := writeSelfSignedCert(t)
	addr := freeAddr(t)
	cfg := newSmokeCfg(t, "http", addr, "")
	cfg.TLSCertFile = certPath
	cfg.TLSKeyFile = keyPath

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg) }()

	// Build a TLS-aware client that trusts our self-signed cert.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := tls.Dial("tcp", addr, &tls.Config{
			RootCAs:    pool,
			ServerName: "scry.test",
		})
		if err == nil {
			_ = conn.Close()
			return
		}
		select {
		case e := <-errCh:
			t.Fatalf("server exited before TLS handshake succeeded: %v", e)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("TLS handshake at %s never succeeded", addr)
}

// writeSelfSignedCert produces a fresh ECDSA cert + key in t.TempDir
// and returns paths plus the cert PEM bytes (for callers that want
// to use it as a CA bundle in the same test).
func writeSelfSignedCert(t *testing.T) (certPath, keyPath string, certPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "scry.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"scry.test", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return
}
