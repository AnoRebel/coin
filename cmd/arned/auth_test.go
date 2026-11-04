package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOIDCFlagsAreAllOrNothing(t *testing.T) {
	for name, options := range map[string]authOptions{
		"issuer only":   {oidcIssuer: "https://id.example.test"},
		"audience only": {oidcAudience: "arne-node"},
		"jwks only":     {oidcJWKS: "/tmp/jwks.json"},
	} {
		if _, err := options.oidc(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if options, err := (&authOptions{}).oidc(); err != nil || options != nil {
		t.Fatalf("empty auth options = %v, %v", options, err)
	}
}

func TestTLSConfig(t *testing.T) {
	if config, err := (&authOptions{}).tlsConfig(); err != nil || config != nil {
		t.Fatalf("plain HTTP = %v, %v", config, err)
	}
	if _, err := (&authOptions{tlsCert: "cert.pem"}).tlsConfig(); err == nil {
		t.Fatal("certificate without key accepted")
	}

	ca := writeTestCA(t)
	// The certificate files are not read by tlsConfig; only the client CA is.
	config, err := (&authOptions{tlsCert: "cert.pem", tlsKey: "key.pem"}).tlsConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.ClientAuth != tls.NoClientCert || config.MinVersion != tls.VersionTLS12 {
		t.Fatalf("server-only TLS = %+v", config)
	}

	config, err = (&authOptions{
		tlsCert: "cert.pem", tlsKey: "key.pem", tlsClientCA: ca,
	}).tlsConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.ClientAuth != tls.RequireAndVerifyClientCert || config.ClientCAs == nil {
		t.Fatalf("mutual TLS = %+v", config)
	}

	if _, err := (&authOptions{
		tlsCert: "cert.pem", tlsKey: "key.pem", tlsClientCA: filepath.Join(t.TempDir(), "missing.pem"),
	}).tlsConfig(); err == nil {
		t.Fatal("missing client CA accepted")
	}
	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&authOptions{
		tlsCert: "cert.pem", tlsKey: "key.pem", tlsClientCA: empty,
	}).tlsConfig(); err == nil {
		t.Fatal("CA bundle without certificates accepted")
	}
}
