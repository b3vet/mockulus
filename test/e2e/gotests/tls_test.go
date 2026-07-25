// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TLS on the mock port is a property of the connection, not of an exchange: by
// the time a corpus case sees a response, its client has already negotiated,
// verified and hidden the whole handshake. What has to be asserted here is that
// the listener speaks TLS at all, that it is the certificate mockulus was
// configured with, and that cleartext is refused — none of which survives the
// trip through a YAML expectation.

// tlsFixture is a generated certificate and its key, on disk.
//
// It is generated per test rather than committed. A checked-in private key is a
// private key in a public repository: a fixture in intent, and a finding in
// every secret scanner in practice. Generating one costs a few milliseconds,
// and t.TempDir removes it afterwards, so the repository never holds a key at
// all. This mirrors the runner's own fixture (test/e2e/runner/tlsfixture.go).
type tlsFixture struct {
	certFile string
	keyFile  string
	certPEM  []byte
}

// generateTLSFixture writes a short-lived self-signed certificate for localhost
// into the test's temporary directory. Short-lived on purpose: a leaked copy is
// worthless within a day, and nothing but this test trusts it.
func generateTLSFixture(t *testing.T) tlsFixture {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}

	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "mockulus-gotests", Organization: []string{"mockulus e2e"}},
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
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	f := tlsFixture{
		certFile: filepath.Join(dir, "cert.pem"),
		keyFile:  filepath.Join(dir, "key.pem"),
		certPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(f.certFile, f.certPEM, 0o644); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	// Owner-only, even for a throwaway: a fixture that models bad key handling
	// is a fixture people copy.
	if err := os.WriteFile(f.keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return f
}

// env is the configuration that turns the mock port into a TLS listener.
func (f tlsFixture) env() map[string]string {
	return map[string]string{
		"MOCKULUS_TLS_CERT_FILE": f.certFile,
		"MOCKULUS_TLS_KEY_FILE":  f.keyFile,
		// Nothing here asserts the drain window, and the default 5s would be
		// paid at every teardown.
		"MOCKULUS_SHUTDOWN_DRAIN": "0s",
	}
}

// client trusts exactly this certificate and nothing else.
//
// The pool is the assertion. InsecureSkipVerify would make the test pass
// against a listener presenting any certificate at all — including one from a
// wholly different process that happened to grab the port — so the one thing a
// TLS case must never do is skip verification.
func (f tlsFixture) client(t *testing.T) *http.Client {
	t.Helper()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(f.certPEM) {
		t.Fatal("the generated certificate is not usable as a root")
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
}

// TestTLSMockPortServesTLSAndAdminStaysCleartext pins SPEC §12.1: setting
// tls_cert_file and tls_key_file wraps the mock listener, and only the mock
// listener.
func TestTLSMockPortServesTLSAndAdminStaysCleartext(t *testing.T) {
	fixture := generateTLSFixture(t)
	m := start(t, fixture.env())

	// The stub is registered over the admin port, in the ordinary way. That it
	// works over plain http is itself half the assertion: the ops listener is
	// never TLS-wrapped, so probes and scrapes are unaffected by turning tls on
	// for mock traffic.
	m.registerStub(t, `{
	  "request": {"method": "GET", "urlPath": "/e2e/gotests-tls/hello"},
	  "response": {"status": 200, "body": "world"}
	}`)

	resp, err := fixture.client(t).Get("https://" + m.mockAddr + "/e2e/gotests-tls/hello")
	if err != nil {
		t.Fatalf("https request to the mock port: %v\n%s", err, strings.Join(m.logs(), "\n"))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("stub over tls: status %d, want 200", resp.StatusCode)
	}
	if resp.TLS == nil {
		t.Fatal("the mock port answered in cleartext; tls_cert_file/tls_key_file did not wrap the listener")
	}
	if !resp.TLS.HandshakeComplete {
		t.Error("the tls handshake did not complete")
	}
	if resp.TLS.Version < tls.VersionTLS12 {
		t.Errorf("negotiated tls version %#04x, want at least TLS 1.2", resp.TLS.Version)
	}
	// A verified chain is what distinguishes this from an InsecureSkipVerify
	// pass: the listener presented the configured certificate, not merely some
	// certificate.
	if len(resp.TLS.VerifiedChains) == 0 {
		t.Error("no verified certificate chain: the presented certificate is not the configured one")
	}

	// The admin listener stays cleartext (SPEC §12.1): only the mock port is
	// TLS-wrapped.
	status, _, err := plainGet(m.adminURL("/readyz"))
	if err != nil {
		t.Fatalf("cleartext request to the admin port: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("admin /readyz over cleartext: status %d, want 200", status)
	}
	if status, _, err := plainGet(m.adminURL("/__admin/mappings")); err != nil || status != http.StatusOK {
		t.Errorf("admin API over cleartext: status %d, err %v; want 200", status, err)
	}
}

// TestTLSMockPortRefusesCleartext pins the other half: with tls on, a plaintext
// request must not reach a stub.
func TestTLSMockPortRefusesCleartext(t *testing.T) {
	fixture := generateTLSFixture(t)
	m := start(t, fixture.env())

	m.registerStub(t, `{
	  "request": {"method": "GET", "urlPath": "/e2e/gotests-tls-cleartext/hello"},
	  "response": {"status": 200, "body": "world"}
	}`)

	// Go answers a plaintext request on a TLS listener with a 400 rather than
	// dropping it, because it can read the verb out of the failed handshake.
	// Either shape is fail-closed; what must never happen is the stub being
	// served, which would mean the listener was not wrapped at all.
	status, body, err := plainGet("http://" + m.mockAddr + "/e2e/gotests-tls-cleartext/hello")
	switch {
	case err != nil:
		// The handshake failed outright and nothing was served.
	case status == http.StatusBadRequest && strings.Contains(body, "HTTPS server"):
		// The stdlib's explicit refusal.
	default:
		t.Fatalf("a cleartext request to the tls mock port was answered %d %q; it must not be served",
			status, body)
	}
	if strings.Contains(body, "world") {
		t.Fatal("the stub was served over cleartext on a tls listener")
	}
}

// TestTLSMisconfigurationRefusesToStart pins the fail-loud contract of
// SPEC §4.4 step 1 for the tls keys.
//
// This is the case that cannot be written as a corpus case at all: what is
// asserted is that no serving process exists. A misconfigured certificate used
// to bind the port, fail the handshake setup on its own goroutine, log an error
// and leave the pod live, ready and serving nothing — the worst possible
// outcome, since Kubernetes would have routed traffic straight at it.
func TestTLSMisconfigurationRefusesToStart(t *testing.T) {
	fixture := generateTLSFixture(t)
	missing := filepath.Join(t.TempDir(), "absent.pem")

	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "cert file does not exist",
			env: map[string]string{
				"MOCKULUS_TLS_CERT_FILE": missing,
				"MOCKULUS_TLS_KEY_FILE":  fixture.keyFile,
			},
			want: "tls_cert_file",
		},
		{
			name: "key file does not exist",
			env: map[string]string{
				"MOCKULUS_TLS_CERT_FILE": fixture.certFile,
				"MOCKULUS_TLS_KEY_FILE":  missing,
			},
			want: "tls_cert_file",
		},
		{
			name: "cert without its key",
			env: map[string]string{
				"MOCKULUS_TLS_CERT_FILE": fixture.certFile,
			},
			want: "tls_key_file",
		},
		{
			name: "the key does not belong to the certificate",
			env: map[string]string{
				"MOCKULUS_TLS_CERT_FILE": fixture.certFile,
				"MOCKULUS_TLS_KEY_FILE":  generateTLSFixture(t).keyFile,
			},
			want: "tls_cert_file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := launch(t, tc.env)

			if m.awaitStartup(10 * time.Second) {
				t.Fatalf("mockulus started and reported %s as its mock listener; "+
					"an unusable certificate must exit 1 rather than leave a ready pod serving nothing",
					m.mockAddr)
			}
			code, exited := m.awaitExit(10 * time.Second)
			if !exited {
				t.Fatal("mockulus neither started nor exited")
			}
			if code == 0 {
				t.Errorf("exit code %d, want non-zero: a configuration error must fail loudly", code)
			}

			out := strings.Join(m.logs(), "\n")
			if !strings.Contains(out, tc.want) {
				t.Errorf("the failure message does not name %q, so an operator cannot tell what is wrong:\n%s",
					tc.want, out)
			}
		})
	}
}

// plainGet issues a request with no TLS configuration of any kind, which is how
// the cleartext assertions stay honest.
func plainGet(url string) (int, string, error) {
	resp, err := harnessClient.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}
