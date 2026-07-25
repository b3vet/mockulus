// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// A topology is a running shape of mockulus the corpus executes against
// (SPEC §19.4). Topology alone cannot express start-time configuration
// differences, so each topology hosts a fixed set of named instance variants;
// a case declares which one it needs.

// Topology identifiers.
const (
	// TopologyT1 is one mockulus with the memory store: the fastest lane, and
	// the only one that needs no containers.
	TopologyT1 = "T1"
)

// Instance variant names (SPEC §19.4).
const (
	VariantDefault       = "default"
	VariantJournal       = "journal"
	VariantTinyJournal   = "tiny-journal"
	VariantAuthed        = "authed"
	VariantTemplatingOn  = "templating-on"
	VariantTemplatingOff = "templating-off"
	VariantDiagnostics   = "diagnostics"
	VariantH2C           = "h2c"
	VariantTLS           = "tls"
	VariantFastClock     = "fast-clock"
	VariantFileStore     = "file-store"
	// VariantTinyBody shrinks the request-body cap so the 413 of deviation #6 is
	// reachable from a corpus case. The default cap is 10 MiB, which no case can
	// exercise without committing a 10 MiB fixture.
	VariantTinyBody = "tiny-body"
	// VariantAccessLog turns on per-request logging with no sampling, so a
	// logprobe can assert one line per request.
	VariantAccessLog = "access-log"
	// VariantSampledLog logs every second request, which is what makes the
	// sampling observable at all.
	VariantSampledLog = "sampled-log"
)

// tlsFixtureDir is where the generated certificate lands. It sits under the
// artifacts directory so a run leaves nothing in the source tree.
const tlsFixtureDir = "test/e2e/.artifacts/tls"

// AdminToken is the token the `authed` variant requires. It is a fixture, not a
// secret: the harness needs no secrets by design (SPEC §22.4).
const AdminToken = "e2e-admin-token"

// variantEnv maps each variant to the configuration that defines it. Adding a
// variant is a reviewed harness change, which is what keeps the matrix bounded.
var variantEnv = map[string]map[string]string{
	VariantDefault:       {},
	VariantJournal:       {"MOCKULUS_JOURNAL_ENABLED": "true"},
	VariantTinyJournal:   {"MOCKULUS_JOURNAL_ENABLED": "true", "MOCKULUS_JOURNAL_BUFFER": "1", "MOCKULUS_JOURNAL_BUFFER_BYTES": "1KiB"},
	VariantAuthed:        {"MOCKULUS_ADMIN_AUTH_TOKEN": AdminToken},
	VariantTemplatingOn:  {"MOCKULUS_TEMPLATING_ENABLED": "on"},
	VariantTemplatingOff: {"MOCKULUS_TEMPLATING_ENABLED": "off"},
	VariantDiagnostics:   {"MOCKULUS_DIAGNOSTICS_ON_UNMATCHED": "true"},
	VariantH2C:           {"MOCKULUS_H2C_ENABLED": "true"},
	// The TLS variant's cert and key are filled in per run by TLSFixture, since
	// they do not exist until the run generates them.
	VariantTLS:      {},
	VariantTinyBody: {"MOCKULUS_MAX_BODY_BYTES": "1KiB"},
	VariantAccessLog: {
		"MOCKULUS_LOG_REQUESTS":         "true",
		"MOCKULUS_LOG_REQUEST_SAMPLE_N": "1",
		// Debug for the same reason as the sampled variant below: it is where
		// the resolved configuration is echoed against the key that set it.
		"MOCKULUS_LOG_LEVEL": "debug",
	},
	VariantSampledLog: {
		"MOCKULUS_LOG_REQUESTS":         "true",
		"MOCKULUS_LOG_REQUEST_SAMPLE_N": "2",
		// Debug because the startup config dump — the one place the resolved
		// value is echoed next to the key that carries it — is logged at debug
		// (SPEC §14.2). Without it a case can see the sampling happen but not
		// that `log.request_sample_n` is what steers it.
		"MOCKULUS_LOG_LEVEL": "debug",
	},
	VariantFastClock: {
		"MOCKULUS_EPHEMERAL_STUB_TTL": "3s",
		"MOCKULUS_RESYNC_INTERVAL":    "2s",
		"MOCKULUS_JOURNAL_TTL":        "5s",
		"MOCKULUS_SYNC_INTERVAL":      "100ms",
	},
}

// Instance is one running mockulus process together with its captured output.
type Instance struct {
	Variant   string
	Topology  string
	MockAddr  string
	AdminAddr string

	cmd    *exec.Cmd
	client *http.Client

	mu   sync.Mutex
	logs []string
}

// startupLine is the JSON startup summary the runner waits for. Parsing it is
// black-box: captured stdout is a sanctioned observation surface (SPEC §19.1),
// and it is how the harness learns which ephemeral ports were bound.
type startupLine struct {
	Msg       string `json:"msg"`
	MockAddr  string `json:"mock_addr"`
	AdminAddr string `json:"admin_addr"`
	Store     string `json:"store"`
}

// StartInstance boots one mockulus process in the given variant on ephemeral
// ports and waits until it reports itself started and ready.
func StartInstance(ctx context.Context, binary, topology, variant string) (*Instance, error) {
	env, ok := variantEnv[variant]
	if !ok {
		return nil, fmt.Errorf("unknown config variant %q", variant)
	}

	var fixture *tlsFixture
	if variant == VariantTLS {
		var err error
		fixture, err = TLSFixture(tlsFixtureDir)
		if err != nil {
			return nil, fmt.Errorf("generate the TLS fixture: %w", err)
		}
		env = map[string]string{
			"MOCKULUS_TLS_CERT_FILE": fixture.CertFile,
			"MOCKULUS_TLS_KEY_FILE":  fixture.KeyFile,
		}
	}

	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = append(os.Environ(),
		"MOCKULUS_PORT=0",
		"MOCKULUS_ADMIN_PORT=0",
		"MOCKULUS_LOG_FORMAT=json",
		"MOCKULUS_LOG_LEVEL=info",
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// The runner's coverage directory, when set, is inherited so the
	// instrumented build writes counters out (SPEC §19.2 coverage floor).
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start mockulus: %w", err)
	}

	transport := &http.Transport{
		MaxIdleConnsPerHost: 32,
		DisableCompression:  true,
	}
	if fixture != nil {
		// The fixture certificate is trusted explicitly rather than by skipping
		// verification: a harness that accepts any certificate could not tell a
		// working TLS listener from a broken one.
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(fixture.CertPEM) {
			return nil, fmt.Errorf("the generated TLS fixture is not a usable certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

	inst := &Instance{
		Variant:  variant,
		Topology: topology,
		cmd:      cmd,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}

	started := make(chan startupLine, 1)
	go inst.captureLogs(stdout, started)

	select {
	case line := <-started:
		scheme := "http://"
		if fixture != nil {
			scheme = "https://"
		}
		inst.MockAddr = scheme + normalizeAddr(line.MockAddr)
		// The admin listener is never TLS-wrapped, only the mock port is.
		inst.AdminAddr = "http://" + normalizeAddr(line.AdminAddr)
	case <-time.After(30 * time.Second):
		_ = inst.Stop()
		return nil, fmt.Errorf("mockulus (%s/%s) did not report a startup line within 30s:\n%s",
			topology, variant, strings.Join(inst.Logs(), "\n"))
	case <-ctx.Done():
		_ = inst.Stop()
		return nil, ctx.Err()
	}

	if err := inst.waitReady(ctx); err != nil {
		_ = inst.Stop()
		return nil, err
	}
	return inst, nil
}

// captureLogs records every log line and signals the startup summary.
func (i *Instance) captureLogs(r io.Reader, started chan<- startupLine) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	signalled := false
	for scanner.Scan() {
		line := scanner.Text()
		i.mu.Lock()
		i.logs = append(i.logs, line)
		i.mu.Unlock()

		if signalled {
			continue
		}
		var s startupLine
		if json.Unmarshal([]byte(line), &s) == nil && s.Msg == "mockulus started" {
			signalled = true
			started <- s
		}
	}
}

// normalizeAddr turns the listener's reported address into something dialable:
// a wildcard bind reports as [::]:PORT.
func normalizeAddr(addr string) string {
	if strings.HasPrefix(addr, "[::]") {
		return "127.0.0.1" + strings.TrimPrefix(addr, "[::]")
	}
	if strings.HasPrefix(addr, "0.0.0.0") {
		return "127.0.0.1" + strings.TrimPrefix(addr, "0.0.0.0")
	}
	return addr
}

// waitReady polls /readyz until the instance reports it can serve.
func (i *Instance) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.AdminAddr+"/readyz", nil)
		if err != nil {
			return err
		}
		resp, err := i.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return fmt.Errorf("mockulus (%s/%s) never became ready", i.Topology, i.Variant)
}

// Logs returns a copy of everything the instance has written.
func (i *Instance) Logs() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.logs...)
}

// Client is the HTTP client cases issue requests with.
func (i *Instance) Client() *http.Client { return i.client }

// Stop terminates the instance.
func (i *Instance) Stop() error {
	if i.cmd == nil || i.cmd.Process == nil {
		return nil
	}
	_ = i.cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- i.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = i.cmd.Process.Kill()
		<-done
	}
	return nil
}

// Pool lazily boots one instance per (topology, variant) pair and shares it
// across the cases that need it. mockulus starts in well under a second, so
// variants multiply cheap processes rather than containers (SPEC §19.4).
type Pool struct {
	binary string

	mu        sync.Mutex
	instances map[string]*Instance
	errs      map[string]error
}

// NewPool creates an empty instance pool around a built mockulus binary.
func NewPool(binary string) *Pool {
	return &Pool{
		binary:    binary,
		instances: map[string]*Instance{},
		errs:      map[string]error{},
	}
}

// Get returns the shared instance for a topology and variant, booting it on
// first use.
func (p *Pool) Get(ctx context.Context, topology, variant string) (*Instance, error) {
	key := topology + "/" + variant

	p.mu.Lock()
	defer p.mu.Unlock()

	if inst, ok := p.instances[key]; ok {
		return inst, nil
	}
	if err, ok := p.errs[key]; ok {
		return nil, err
	}

	inst, err := StartInstance(ctx, p.binary, topology, variant)
	if err != nil {
		p.errs[key] = err
		return nil, err
	}
	p.instances[key] = inst
	return inst, nil
}

// Close stops every instance in the pool.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, inst := range p.instances {
		_ = inst.Stop()
	}
	clear(p.instances)
}

// Instances returns every running instance, for artifact collection.
func (p *Pool) Instances() map[string]*Instance {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]*Instance, len(p.instances))
	for k, v := range p.instances {
		out[k] = v
	}
	return out
}
