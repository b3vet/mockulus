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
	"strconv"
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
	// TopologyT2 is one mockulus over the run's shared Couchbase. It provides
	// the `couchbase` capability: persistence, TTL, counters, storeprobes.
	TopologyT2 = "T2"
	// TopologyT3 is three mockulus replicas over the same Couchbase keyspace
	// behind a round-robin proxy. It provides `couchbase` and `multi-pod`.
	TopologyT3 = "T3"
)

// t3Replicas is how many pods stand behind T3's load balancer. Three is the
// smallest number that can tell "the writer serves it immediately and one other
// pod caught up" apart from "both pods happen to be the writer".
const t3Replicas = 3

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
	// VariantTinyTemplate shrinks the render output cap for the same reason:
	// the default is 10 MB, and a case reaching it honestly — by letting the
	// request drive the expansion — would have to send megabytes to prove that
	// a stub cannot be made to allocate without bound.
	VariantTinyTemplate = "tiny-template"
	// VariantBoundedJournal shrinks both bounds of deviation #16 until a case
	// can reach them. The defaults are a 10k-entry scan window and a 64 KiB body
	// cap, so honouring them would mean serving ten thousand requests and
	// committing a fixture bigger than the rest of the corpus to observe two
	// bounds that under-report in silence.
	VariantBoundedJournal = "bounded-journal"
	// VariantAccessLog turns on per-request logging with no sampling, so a
	// logprobe can assert one line per request.
	VariantAccessLog = "access-log"
	// VariantSampledLog logs every second request, which is what makes the
	// sampling observable at all.
	VariantSampledLog = "sampled-log"
	// VariantCBVerbose is the store lane at debug level, where the resolved
	// configuration is echoed against the key that set it (SPEC §14.2). Keyspace
	// settings are read once at boot and never appear in a response, so that dump
	// is the only surface on which a case can see the value one of them took.
	VariantCBVerbose = "cb-verbose"
	// VariantCBMajority asks for majority durability, which is the mode teams
	// treating mocks as long-lived environment config run in (SPEC §7.2).
	VariantCBMajority = "cb-majority"
	// VariantScenarioBudget tightens the request-path scenario budget well below
	// the general KV timeout, which is what makes the budget observable: with
	// the store away the two differ by seconds, and only the tight one leaves a
	// failing request inside a window a test would wait through.
	VariantScenarioBudget = "scenario-budget"
	// VariantStartWithoutStore points an instance at a store that does not
	// resolve and takes the escape hatch of SPEC §4.4. The host is under
	// `.invalid`, which RFC 2606 reserves as never resolvable: a port nobody
	// listens on would still be reached if a stray Couchbase were running here,
	// and the case would then prove the opposite of what it claims.
	VariantStartWithoutStore = "start-without-store"
)

// tlsFixtureDir is where the generated certificate lands. It sits under the
// artifacts directory so a run leaves nothing in the source tree.
const tlsFixtureDir = "test/e2e/.artifacts/tls"

// AdminToken is the token the `authed` variant requires. It is a fixture, not a
// secret: the harness needs no secrets by design (SPEC §22.4).
const AdminToken = "e2e-admin-token"

// fileStoreFixture is the WireMock project the `file-store` variant is pointed
// at. It is committed rather than generated because what it has to be is the
// layout a real project already has — `mappings/` beside `__files/`, one stub
// per file and several in one, and a malformed document among them.
const fileStoreFixture = "test/e2e/corpus/file-store"

// variantEnv maps each variant to the configuration that defines it. Adding a
// variant is a reviewed harness change, which is what keeps the matrix bounded.
var variantEnv = map[string]map[string]string{
	VariantDefault: {},
	VariantJournal: {"MOCKULUS_JOURNAL_ENABLED": "true"},
	VariantTinyJournal: {
		"MOCKULUS_JOURNAL_ENABLED":      "true",
		"MOCKULUS_JOURNAL_BUFFER":       "1",
		"MOCKULUS_JOURNAL_BUFFER_BYTES": "1KiB",
		// Debug because both caps are start-time-only and a drop leaves nothing
		// in a response: without the startup dump a case can see the counter
		// rise but not that a one-entry queue is what made it rise, which is the
		// difference between proving the drop path and proving that something
		// somewhere failed to record (SPEC §14.2).
		"MOCKULUS_LOG_LEVEL": "debug",
	},
	VariantBoundedJournal: {
		"MOCKULUS_JOURNAL_ENABLED": "true",
		// Bare byte counts rather than IEC spellings: the case has to post a
		// body past the cap, and 64 KiB of YAML in the corpus buys nothing that
		// a few hundred bytes does not.
		"MOCKULUS_JOURNAL_MAX_BODY":         "256",
		"MOCKULUS_JOURNAL_QUERY_SCAN_LIMIT": "3",
		// Debug for the same reason as the queue caps above, and more sharply:
		// both of these bounds answer with a number that is simply wrong past
		// them, so a case that could not see which bounds it ran under would be
		// asserting an under-report it cannot attribute.
		"MOCKULUS_LOG_LEVEL": "debug",
	},
	VariantAuthed:        {"MOCKULUS_ADMIN_AUTH_TOKEN": AdminToken},
	VariantTemplatingOn:  {"MOCKULUS_TEMPLATING_ENABLED": "on"},
	VariantTemplatingOff: {"MOCKULUS_TEMPLATING_ENABLED": "off"},
	VariantDiagnostics:   {"MOCKULUS_DIAGNOSTICS_ON_UNMATCHED": "true"},
	VariantH2C:           {"MOCKULUS_H2C_ENABLED": "true"},
	// The TLS variant's cert and key are filled in per run by TLSFixture, since
	// they do not exist until the run generates them.
	VariantTLS:          {},
	VariantTinyBody:     {"MOCKULUS_MAX_BODY_BYTES": "1KiB"},
	VariantTinyTemplate: {"MOCKULUS_TEMPLATE_MAX_OUTPUT_BYTES": "1KiB"},
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
	// The file driver is the WireMock drop-in shape: a directory of mappings
	// read at boot, with no store to write to (SPEC §7.1, §19.4).
	VariantFileStore: {
		"MOCKULUS_STORE":     "file",
		"MOCKULUS_FILE_ROOT": fileStoreFixture,
	},
	VariantFastClock: {
		"MOCKULUS_EPHEMERAL_STUB_TTL": "3s",
		"MOCKULUS_RESYNC_INTERVAL":    "2s",
		// The journal is off by default (deviation #1), so the entry TTL beside
		// it steers nothing unless the switch is flipped here too: there would
		// be no entry for it to expire.
		"MOCKULUS_JOURNAL_ENABLED": "true",
		// Thirty seconds where the stub TTL above is three, because the two are
		// observed through different machinery. An expired stub disappears from
		// a KV range scan the moment it expires; a journal entry becomes visible
		// only once the index behind the query endpoints has caught up, and that
		// lag is part of the contract rather than a defect (§11.4, deviation
		// #10). A TTL inside the lag would let an entry expire before it was
		// ever visible, and the case watching for it would then be measuring the
		// index rather than the TTL — an unreproducible failure whose message
		// says the entry was never recorded.
		"MOCKULUS_JOURNAL_TTL":   "30s",
		"MOCKULUS_SYNC_INTERVAL": "100ms",
	},
	VariantCBVerbose:  {"MOCKULUS_LOG_LEVEL": "debug"},
	VariantCBMajority: {"MOCKULUS_COUCHBASE_DURABILITY": "majority", "MOCKULUS_LOG_LEVEL": "debug"},
	VariantScenarioBudget: {
		"MOCKULUS_SCENARIO_KV_TIMEOUT": "150ms",
		// Debug for the same reason the durability lane runs there: the startup
		// dump is the only surface on which the resolved value of a key that
		// never appears in a response can be seen at all (SPEC §14.2).
		"MOCKULUS_LOG_LEVEL": "debug",
	},
	VariantStartWithoutStore: {
		"MOCKULUS_START_WITHOUT_STORE": "true",
		"MOCKULUS_COUCHBASE_CONNSTR":   "couchbase://store.invalid",
	},
}

// t1OnlyVariants are the variants a store topology would silently defeat, with
// the reason a case cannot ask for both. The topology's store configuration is
// applied after the variant's and wins, so the case would run against the run's
// Couchbase while believing it proved something about a directory, or about a
// store that is not there (SPEC §19.4).
var t1OnlyVariants = map[string]string{
	VariantFileStore: "it points the instance at a directory, so a case cannot ask for it and for a store topology at once",
	VariantStartWithoutStore: "its whole subject is a store that is absent at boot, " +
		"and a store topology would hand the instance a working one",
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
// ports and waits until it reports itself started and ready. store carries the
// topology's store configuration and is empty for T1, whose instances get the
// memory driver by simply not being told about anything else.
func StartInstance(ctx context.Context, binary, topology, variant string,
	store map[string]string) (*Instance, error) {

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
	for k, v := range store {
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

// PodAny is the `pod:` selector that lets the load balancer choose. It is the
// default, so a case says nothing unless it means to pin.
const PodAny = "any"

// Deployment is what a case runs against: the addresses it talks to, plus the
// replicas behind them.
//
// In T1 and T2 the deployment is one process and the addresses are its own. In
// T3 they belong to the round-robin proxy, and the replicas are reachable
// individually so a case can watch a write on one pod arrive at another.
type Deployment struct {
	Topology  string
	Variant   string
	MockAddr  string
	AdminAddr string

	pods  []*Instance
	proxy []*LBProxy
}

// Pod resolves a step's `pod:` selector to one replica's addresses.
//
// An unpinned step goes to the deployment address, which in T3 is the load
// balancer: the default spreads a case across replicas rather than quietly
// pinning it to the first one.
func (d *Deployment) Pod(spec string) (mock, admin string, err error) {
	if spec == "" || spec == PodAny {
		return d.MockAddr, d.AdminAddr, nil
	}
	index, err := strconv.Atoi(spec)
	if err != nil || index < 0 {
		return "", "", fmt.Errorf("pod %q: want %q or a replica index", spec, PodAny)
	}
	if index >= len(d.pods) {
		return "", "", fmt.Errorf(
			"pod %d: topology %s runs %d replica(s); pinning past the first needs requires: [multi-pod]",
			index, d.Topology, len(d.pods))
	}
	return d.pods[index].MockAddr, d.pods[index].AdminAddr, nil
}

// Client is the HTTP client cases issue requests with.
func (d *Deployment) Client() *http.Client { return d.pods[0].Client() }

// Logs returns every replica's captured output.
//
// The lines are returned as written, unprefixed, because a log probe matches
// JSON fields and a harness annotation would stop the line being JSON. So a
// probe against a multi-pod deployment asserts that *some* replica logged the
// line, which is the right claim for the startup and configuration lines cases
// probe for.
func (d *Deployment) Logs() []string {
	var out []string
	for _, pod := range d.pods {
		out = append(out, pod.Logs()...)
	}
	return out
}

// Stop tears the deployment down, load balancer first so no request is in
// flight to a replica that is going away.
func (d *Deployment) Stop() {
	for _, proxy := range d.proxy {
		_ = proxy.Stop()
	}
	for _, pod := range d.pods {
		_ = pod.Stop()
	}
}

// Pool lazily boots one deployment per (topology, variant) pair and shares it
// across the cases that need it. mockulus starts in well under a second, so
// variants multiply cheap processes rather than containers: T2 and T3 share the
// run's single Couchbase and are separated by scope (SPEC §19.4, §7.2).
type Pool struct {
	binary string
	// couchbaseFile pins the image the T2/T3 lane's container comes from.
	couchbaseFile string

	mu      sync.Mutex
	entries map[string]*poolEntry

	// The container is started at most once per run, on the first case that
	// needs one. A run with no T2/T3 case in it never touches Docker.
	couchbaseOnce sync.Once
	couchbase     *Couchbase
	couchbaseErr  error
}

// poolEntry is one deployment's boot, done once and shared — including its
// failure, so a topology that cannot start reports the same reason to every
// case waiting on it instead of trying again per case.
type poolEntry struct {
	once sync.Once
	dep  *Deployment
	err  error
}

// NewPool creates an empty pool around a built mockulus binary.
func NewPool(binary, couchbaseFile string) *Pool {
	return &Pool{
		binary:        binary,
		couchbaseFile: couchbaseFile,
		entries:       map[string]*poolEntry{},
	}
}

// Get returns the shared deployment for a topology and variant, booting it on
// first use.
func (p *Pool) Get(ctx context.Context, topology, variant string) (*Deployment, error) {
	key := topology + "/" + variant

	p.mu.Lock()
	entry, known := p.entries[key]
	if !known {
		entry = &poolEntry{}
		p.entries[key] = entry
	}
	p.mu.Unlock()

	// The boot runs outside the pool lock. Holding it across one would put every
	// T1 case behind the minute the T2/T3 lane spends waiting for a container
	// none of them need.
	entry.once.Do(func() { entry.dep, entry.err = p.start(ctx, topology, variant) })
	return entry.dep, entry.err
}

func (p *Pool) start(ctx context.Context, topology, variant string) (*Deployment, error) {
	replicas := 1
	if topology == TopologyT3 {
		replicas = t3Replicas
	}

	var storeEnv map[string]string
	if topology == TopologyT2 || topology == TopologyT3 {
		// A variant that names its own store and a topology that provides one are
		// two answers to the same question, and the topology wins. Say so here
		// rather than let the combination quietly mean the weaker thing.
		if reason, t1Only := t1OnlyVariants[variant]; t1Only {
			return nil, fmt.Errorf("the %s variant is T1 only: %s", variant, reason)
		}
		cb, err := p.couchbaseLane(ctx)
		if err != nil {
			return nil, err
		}
		storeEnv = cb.StoreEnv(ScopeFor(topology, variant))
	}

	dep := &Deployment{Topology: topology, Variant: variant}
	for range replicas {
		// Replicas boot one after another. The first one through creates the
		// scope, its collections and the journal index — manage_bucket is on, so
		// the harness never applies DDL itself (SPEC §7.2) — and three pods
		// racing on that has nothing to win and a bring-up flake to lose.
		inst, err := StartInstance(ctx, p.binary, topology, variant, storeEnv)
		if err != nil {
			dep.Stop()
			return nil, err
		}
		dep.pods = append(dep.pods, inst)
	}

	if replicas == 1 {
		dep.MockAddr, dep.AdminAddr = dep.pods[0].MockAddr, dep.pods[0].AdminAddr
		return dep, nil
	}

	transport := dep.pods[0].Client().Transport
	for _, listener := range []struct {
		addr func(*Instance) string
		into *string
	}{
		{func(i *Instance) string { return i.MockAddr }, &dep.MockAddr},
		// The admin port gets a load balancer of its own: an admin call landing
		// on an arbitrary replica is exactly the claim T3 exists to keep honest.
		{func(i *Instance) string { return i.AdminAddr }, &dep.AdminAddr},
	} {
		backends := make([]string, 0, len(dep.pods))
		for _, pod := range dep.pods {
			backends = append(backends, listener.addr(pod))
		}
		proxy, err := StartLBProxy(backends, transport)
		if err != nil {
			dep.Stop()
			return nil, fmt.Errorf("start the %s load balancer: %w", topology, err)
		}
		dep.proxy = append(dep.proxy, proxy)
		*listener.into = proxy.Addr
	}
	return dep, nil
}

// couchbaseLane starts the run's container the first time a topology needs it.
func (p *Pool) couchbaseLane(ctx context.Context) (*Couchbase, error) {
	p.couchbaseOnce.Do(func() {
		p.couchbase, p.couchbaseErr = StartCouchbase(ctx, p.couchbaseFile)
		if p.couchbaseErr == nil {
			log("couchbase lane: " + p.couchbase.Image + " on " + p.couchbase.ConnStr)
		}
	})
	return p.couchbase, p.couchbaseErr
}

// ScopeFor is the Couchbase scope a (topology, variant) deployment owns.
//
// Sharing one container is only safe because the keyspaces do not overlap: the
// `fast-clock` variant sweeping its expired stubs must not be able to touch
// what the `default` variant registered, and T2's deployment-wide resets must
// not reach T3's replicas (SPEC §7.2).
func ScopeFor(topology, variant string) string {
	return strings.ToLower(topology) + "-" + variant
}

// Close stops every deployment in the pool, and the container behind them.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entry := range p.entries {
		if entry.dep != nil {
			entry.dep.Stop()
		}
	}
	clear(p.entries)
	if p.couchbase != nil {
		_ = p.couchbase.Stop()
		p.couchbase = nil
	}
}

// Deployments returns everything running, for artifact collection.
func (p *Pool) Deployments() map[string]*Deployment {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]*Deployment, len(p.entries))
	for key, entry := range p.entries {
		if entry.dep != nil {
			out[key] = entry.dep
		}
	}
	return out
}
