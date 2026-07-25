// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The TLS variant needs a certificate, and the certificate is generated rather
// than committed.
//
// A checked-in private key is a private key in a public repository. It would be
// a test fixture in intent and a finding in every secret scanner in practice,
// and the first person to copy it into something real would be doing exactly
// what the file looks like it invites. Generating one per run costs a few
// milliseconds and leaves nothing behind.
//
// It is self-signed and short-lived on purpose: the harness trusts this one
// certificate explicitly, so nothing else can be reached with it, and an expiry
// measured in hours means a leaked copy is worthless by the time anyone finds
// it.

// tlsFixture is the generated certificate and its key, on disk.
type tlsFixture struct {
	CertFile string
	KeyFile  string
	// CertPEM is the same certificate in memory, for the client's trust pool.
	CertPEM []byte
}

var (
	tlsOnce sync.Once
	tlsFix  *tlsFixture
	tlsErr  error
)

// TLSFixture generates the certificate once per run and returns it.
func TLSFixture(dir string) (*tlsFixture, error) {
	tlsOnce.Do(func() { tlsFix, tlsErr = generateTLSFixture(dir) })
	return tlsFix, tlsErr
}

func generateTLSFixture(dir string) (*tlsFixture, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "mockulus-e2e", Organization: []string{"mockulus e2e"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	certFile := filepath.Join(dir, "e2e-tls-cert.pem")
	keyFile := filepath.Join(dir, "e2e-tls-key.pem")
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		return nil, err
	}
	// The key is written owner-only. It is throwaway, but a fixture that models
	// bad key handling is a fixture people copy.
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return nil, err
	}

	return &tlsFixture{CertFile: certFile, KeyFile: keyFile, CertPEM: certPEM}, nil
}
