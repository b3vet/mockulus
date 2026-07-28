// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Topologies T2 and T3 run mockulus against a real Couchbase (SPEC §19.4).
//
// One container serves the whole run. T2, T3 and every config variant share it
// and are held apart by scope (§7.2), because a container per variant would put
// a minute and a gigabyte between a developer and each `make e2e`. The
// separation is real: a scope is a distinct keyspace, so the `fast-clock`
// variant's TTL sweeps cannot reach the `default` variant's stubs.
//
// The container is driven with plain `docker`, the same way the WireMock oracle
// is. testcontainers-go is on the §18 test-only allowlist and would do the same
// three things — run, wait, remove — behind a dependency the runner otherwise
// has no use for.

// Fixture credentials for the run's Couchbase. They are not secrets: the
// harness needs none by design (SPEC §22.4), and the container is published on
// the loopback interface only. Couchbase rejects passwords under six
// characters, which is the only constraint on the value.
const (
	couchbaseUser     = "Administrator"
	couchbasePassword = "e2e-couchbase-pw"
	couchbaseBucket   = "mockulus"
)

// Ports the SDK reaches the node on: cluster management, views, query, and the
// KV service. They are published one-to-one rather than ephemerally, because a
// Couchbase client is told where the services live *by the cluster* — the node
// advertises 11210 for KV, and a client that was handed a remapped host port
// would still dial 11210. Ephemeral publishing needs the cluster's alternate-
// address machinery to agree; the price of not having it is that one machine
// runs one gate at a time, which claimLane makes a wait rather than a failure.
var couchbasePorts = []string{"8091", "8092", "8093", "11210"}

// couchbaseLabel marks a container as this harness's, so a later run can find
// one left behind and tell it apart from a live run's.
const couchbaseLabel = "mockulus-e2e=couchbase"

// errNoDocker marks a container lane that cannot run here at all, which the
// runner turns into skipped cases rather than failures. Skipping still cannot
// satisfy a coverage gate — an uncovered behavior is reported either way — so
// the absence of Docker costs visibility, never correctness.
var errNoDocker = errors.New("docker is unavailable")

// Couchbase is the run's shared Couchbase container.
type Couchbase struct {
	// ConnStr is what mockulus is pointed at.
	ConnStr string
	// Image is the pinned tag, for the run log.
	Image string

	container string
	client    *http.Client

	// paused tracks the frozen state so that asking for it twice is a no-op.
	// Docker refuses to unpause a running container, and a case's own
	// `start_store` and the runner's unconditional restore after it are the
	// same call — the second one must not turn a passing case red.
	mu     sync.Mutex
	paused bool
}

// StartCouchbase boots the pinned image, initializes the cluster and creates
// the bucket, returning only once the bucket is queryable.
//
// The version comes from a file rather than a constant for the same reason the
// WireMock pin does: bumping the server under the suite is one reviewed line.
func StartCouchbase(ctx context.Context, versionFile string) (*Couchbase, error) {
	if err := requireDocker(ctx); err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(versionFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", versionFile, err)
	}
	image := strings.TrimSpace(string(raw))
	if image == "" {
		return nil, fmt.Errorf("%s is empty", versionFile)
	}

	name := containerName(os.Getpid())
	if err := claimLane(ctx, name, image); err != nil {
		return nil, err
	}

	cb := &Couchbase{
		ConnStr:   "couchbase://127.0.0.1",
		Image:     image,
		container: name,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	if err := cb.provision(ctx); err != nil {
		logs := cb.containerLogs()
		_ = cb.Stop()
		if logs != "" {
			return nil, fmt.Errorf("%w\ncontainer log tail:\n%s", err, logs)
		}
		return nil, err
	}
	return cb, nil
}

// StoreEnv is the configuration that points one mockulus instance at this
// container, in the given scope.
//
// `store` is left at its default: a connection string alone selects the
// couchbase driver (SPEC §13), and the harness asserting that is worth more
// than the harness pinning it.
func (c *Couchbase) StoreEnv(scope string) map[string]string {
	return map[string]string{
		"MOCKULUS_COUCHBASE_CONNSTR":  c.ConnStr,
		"MOCKULUS_COUCHBASE_USERNAME": couchbaseUser,
		"MOCKULUS_COUCHBASE_PASSWORD": couchbasePassword,
		"MOCKULUS_COUCHBASE_BUCKET":   couchbaseBucket,
		"MOCKULUS_COUCHBASE_SCOPE":    scope,
	}
}

// Pause takes the store away, and Resume gives it back. Together they are the
// `stop_store` / `start_store` steps a degraded-mode case is written in.
//
// Freezing the container is SIGSTOP over the processes inside it: the TCP
// connections the driver already holds stay open and every operation on them
// runs out its own timeout, which is what an unreachable cluster looks like from
// gocb's side. Stopping the container would close them instead — a different
// failure, and one that costs a minute of bucket warm-up to undo, so a case
// could not assert the recovery it is half about. Freezing is also the only
// version of this that leaves the run's *other* deployments intact: they find
// the store back where they left it.
func (c *Couchbase) Pause(ctx context.Context) error { return c.freeze(ctx, true) }

// Resume thaws a paused container.
func (c *Couchbase) Resume(ctx context.Context) error { return c.freeze(ctx, false) }

func (c *Couchbase) freeze(ctx context.Context, want bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused == want {
		return nil
	}

	verb := "unpause"
	if want {
		verb = "pause"
	}
	if out, err := exec.CommandContext(ctx, "docker", verb, c.container).CombinedOutput(); err != nil {
		return fmt.Errorf("docker %s %s: %w: %s",
			verb, c.container, err, truncate(collapse(string(out)), 200))
	}
	c.paused = want
	return nil
}

// Stop removes the container.
func (c *Couchbase) Stop() error {
	if c.container == "" {
		return nil
	}
	// Deliberately a background context rather than the run's: teardown happens
	// on the way out of a cancelled run, and binding the removal to a context
	// that is already done would kill it and leave the container behind.
	return exec.CommandContext(context.Background(), "docker", "rm", "-f", c.container).Run()
}

// provision walks the container from "process started" to "usable keyspace".
//
// Every wait here is on an observable state, never on a duration. Couchbase
// takes tens of seconds to become usable and the exact number moves with the
// machine, so a sleep long enough to be safe on a loaded CI runner would be
// most of a minute of dead time locally — and still be the suite's most likely
// flake (SPEC §19.1).
func (c *Couchbase) provision(ctx context.Context) error {
	if err := c.waitManagement(ctx); err != nil {
		return err
	}
	if err := c.clusterInit(ctx); err != nil {
		return err
	}
	if err := c.createBucket(ctx); err != nil {
		return err
	}
	return c.waitBucketQueryable(ctx)
}

// waitManagement waits for the management service to answer, which is the
// earliest point cluster-init can succeed.
func (c *Couchbase) waitManagement(ctx context.Context) error {
	return poll(ctx, 90*time.Second, "the couchbase management service never answered", func() error {
		resp, err := c.get(ctx, "http://127.0.0.1:8091/pools")
		if err != nil {
			return err
		}
		if resp.status != http.StatusOK {
			return fmt.Errorf("/pools answered %d", resp.status)
		}
		return nil
	})
}

// clusterInit initializes the single node with the services the driver needs:
// data for KV, and index plus query for the journal's GSI and the N1QL
// fallback path (SPEC §7.2).
func (c *Couchbase) clusterInit(ctx context.Context) error {
	return poll(ctx, 90*time.Second, "couchbase never accepted cluster-init", func() error {
		return c.cli(ctx, "cluster-init",
			"--cluster-username", couchbaseUser,
			"--cluster-password", couchbasePassword,
			"--services", "data,index,query",
			"--cluster-ramsize", "512",
			"--cluster-index-ramsize", "512")
	})
}

// createBucket creates the bucket mockulus is pointed at.
//
// The bucket is the one thing the harness must create itself: `manage_bucket`
// (default true) has mockulus create the scope, the collections and the
// journal index at boot, which is the zero-config promise of SPEC §7.2 — but
// creating a *bucket* needs cluster-manager rights that the product
// deliberately does not ask for.
func (c *Couchbase) createBucket(ctx context.Context) error {
	return poll(ctx, 60*time.Second, "couchbase never accepted the bucket creation", func() error {
		return c.cli(ctx, "bucket-create",
			"-u", couchbaseUser, "-p", couchbasePassword,
			"--bucket", couchbaseBucket,
			"--bucket-type", "couchbase",
			"--bucket-ramsize", "256",
			"--wait")
	})
}

// waitBucketQueryable is the readiness contract: the bucket exists, its nodes
// are healthy, and the query service can see it.
//
// "The container started" and "a client can use the bucket" are a long way
// apart, and the gap is where a suite that fails one run in twenty comes from.
// The query check earns its place separately from the KV one: mockulus runs
// DDL against the query service at boot when it manages the keyspace, so a
// bucket that is healthy for KV but not yet visible to N1QL still fails the
// first instance to start.
func (c *Couchbase) waitBucketQueryable(ctx context.Context) error {
	if err := poll(ctx, 120*time.Second, "the bucket's nodes never became healthy", func() error {
		resp, err := c.get(ctx, "http://127.0.0.1:8091/pools/default/buckets/"+couchbaseBucket)
		if err != nil {
			return err
		}
		if resp.status != http.StatusOK {
			return fmt.Errorf("the bucket endpoint answered %d", resp.status)
		}
		var doc struct {
			Nodes []struct {
				Status string `json:"status"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal(resp.body, &doc); err != nil {
			return fmt.Errorf("decode the bucket document: %w", err)
		}
		if len(doc.Nodes) == 0 {
			return errors.New("the bucket reports no nodes yet")
		}
		for _, n := range doc.Nodes {
			if n.Status != "healthy" {
				return fmt.Errorf("a node is %q", n.Status)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	statement := fmt.Sprintf(`SELECT RAW COUNT(*) FROM system:keyspaces WHERE name = %q`, couchbaseBucket)
	return poll(ctx, 120*time.Second, "the query service never saw the bucket", func() error {
		resp, err := c.query(ctx, statement)
		if err != nil {
			return err
		}
		if resp.status != http.StatusOK {
			return fmt.Errorf("the query service answered %d: %s",
				resp.status, truncate(string(resp.body), 200))
		}
		// system:keyspaces needs no index, so a count of one means the bucket is
		// in the query engine's catalog rather than merely in the cluster's.
		var doc struct {
			Results []int `json:"results"`
		}
		if err := json.Unmarshal(resp.body, &doc); err != nil {
			return fmt.Errorf("decode the query answer: %w", err)
		}
		if len(doc.Results) == 0 || doc.Results[0] == 0 {
			return errors.New("the query service does not list the bucket yet")
		}
		return nil
	})
}

// cli runs a couchbase-cli subcommand inside the container. Talking to the
// server's own tooling keeps the harness out of the business of knowing which
// REST endpoints initialize a cluster in which server version.
func (c *Couchbase) cli(ctx context.Context, subcommand string, args ...string) error {
	full := append([]string{"exec", c.container, "couchbase-cli", subcommand, "-c", "127.0.0.1"}, args...)
	out, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("couchbase-cli %s: %w: %s", subcommand, err, lastLine(string(out)))
	}
	return nil
}

// httpAnswer is one management or query response, read whole.
type httpAnswer struct {
	status int
	body   []byte
}

func (c *Couchbase) get(ctx context.Context, url string) (httpAnswer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return httpAnswer{}, err
	}
	req.SetBasicAuth(couchbaseUser, couchbasePassword)
	return c.do(req)
}

func (c *Couchbase) query(ctx context.Context, statement string) (httpAnswer, error) {
	form := strings.NewReader("statement=" + url.QueryEscape(statement))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://127.0.0.1:8093/query/service", form)
	if err != nil {
		return httpAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(couchbaseUser, couchbasePassword)
	return c.do(req)
}

func (c *Couchbase) do(req *http.Request) (httpAnswer, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return httpAnswer{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return httpAnswer{}, err
	}
	return httpAnswer{status: resp.StatusCode, body: body}, nil
}

// containerLogs is the tail of the container's own output, attached to a
// bring-up failure so the reason is in the failure rather than in a container
// that the same failure is about to remove.
//
// Like the removal itself this runs on a background context: one reason
// bring-up fails is that the run was cancelled, and that is precisely when the
// log tail still has to be collected.
func (c *Couchbase) containerLogs() string {
	out, err := exec.CommandContext(context.Background(),
		"docker", "logs", "--tail", "40", c.container).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// laneWait is how long a run waits for the T2/T3 lane before giving up. Two
// runs on one machine take turns rather than failing: a full corpus run is a
// couple of minutes, so waiting one out costs less than a red gate that has
// nothing to do with the change under test.
const laneWait = 5 * time.Minute

// containerName ties a container to the run that owns it, which is what lets a
// later run tell an abandoned container from a live one.
// containerPrefix names the run's Couchbase container. The owning process id is
// the suffix, which is how a later run tells an abandoned container from one
// still in use.
const containerPrefix = "mockulus-e2e-cb-"

func containerName(pid int) string { return containerPrefix + strconv.Itoa(pid) }

// claimLane takes sole ownership of the Couchbase lane and starts the run's
// container in it.
//
// Only one Couchbase can exist per machine, because the ports it is reached on
// cannot be remapped (see couchbasePorts). So a second run has to wait rather
// than help itself: removing a container another run is using would turn one
// person's `make e2e` into somebody else's mystery failure. A container whose
// run is gone, though, is nobody's — a run killed hard enough to skip its own
// teardown leaves one holding the ports forever, so those are cleared here and
// the next run self-heals.
//
// Claiming and starting are one retried step because they cannot be made
// atomic: two runs can both find the lane free and race for the ports, and the
// loser would rather wait the winner out than fail on a collision it only had
// to outlast.
func claimLane(ctx context.Context, name, image string) error {
	if holder := laneHolder(ctx); holder != "" {
		log("waiting for the T2/T3 lane, held by " + holder)
	}

	args := make([]string, 0, 7+2*len(couchbasePorts))
	args = append(args, "run", "-d", "--name", name, "--label", couchbaseLabel)
	for _, port := range couchbasePorts {
		args = append(args, "-p", "127.0.0.1:"+port+":"+port)
	}
	args = append(args, image)

	return poll(ctx, laneWait, "the T2/T3 lane never came free", func() error {
		if holder := laneHolder(ctx); holder != "" {
			return fmt.Errorf("%s holds it; if no run owns that container, `docker rm -f %s`",
				holder, holder)
		}
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			// A run that fails to publish leaves the container behind in the
			// created state, where the next sweep collects it — so the message
			// is about the ports, which is what a reader has to act on.
			return fmt.Errorf("starting %s failed (T2/T3 publish %s on 127.0.0.1 and cannot remap them): %s",
				image, strings.Join(couchbasePorts, ", "), truncate(collapse(string(out)), 300))
		}
		return nil
	})
}

// laneHolder names a live container holding the lane, clearing any that no
// longer has a run behind it. It returns "" when the lane is free.
func laneHolder(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label="+couchbaseLabel, "--format", "{{.Names}}\t{{.State}}").Output()
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, state, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || name == "" {
			continue
		}
		if state == "running" && runnerAlive(name) {
			return name
		}
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	}
	return ""
}

// runnerAlive reports whether the run that started a container still exists.
//
// Erring towards "yes" is the safe direction — it costs a wait and a message
// saying how to clear the lane, rather than destroying another run's work. But
// it is only safe while "yes" is *rare* when wrong, and one case makes it
// permanent: a container named for a process id that will never exit holds the
// lane for every future run on the machine. Process 1 is the standard way to
// get there, since it is init, always exists, and always answers signal 0 — a
// container someone names mockulus-e2e-cb-1 by hand, as a probe, blocks the
// suite until a human finds it.
//
// So the id has to plausibly belong to a runner, not merely exist. Process 1
// never is one; nor is anything whose command has nothing to do with Go.
func runnerAlive(container string) bool {
	pid, err := strconv.Atoi(strings.TrimPrefix(container, containerPrefix))
	if err != nil || pid <= 0 {
		return true
	}
	if pid == os.Getpid() {
		return true
	}
	// init adopts orphans and outlives everything; it is never the runner.
	if pid == 1 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks for existence without delivering anything: nil means the
	// process is there, EPERM means it is there and owned by somebody else.
	err = process.Signal(syscall.Signal(0))
	if err != nil && !errors.Is(err, os.ErrPermission) {
		return false
	}
	return looksLikeRunner(pid)
}

// looksLikeRunner reports whether a live process id plausibly belongs to an
// E2E run. A recycled id usually lands on something unrelated, and that is the
// case worth catching: the lane is held by a container whose owner is long gone
// and whose number now belongs to a shell.
//
// Unrecognisable answers count as a runner, keeping the conservative direction
// wherever this cannot tell.
func looksLikeRunner(pid int) bool {
	out, err := exec.CommandContext(context.Background(),
		"ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return true
	}
	comm := strings.TrimSpace(string(out))
	if comm == "" {
		return true
	}
	base := filepath.Base(comm)
	return strings.Contains(base, "runner") || strings.HasPrefix(base, "go") ||
		strings.Contains(base, "e2e") || strings.Contains(base, "exe")
}

// requireDocker reports whether a container lane can run at all.
func requireDocker(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("%w: no docker on PATH", errNoDocker)
	}
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: the daemon is not usable: %s", errNoDocker, lastLine(string(out)))
	}
	return nil
}

// poll retries an observation until it holds or the window runs out, reporting
// the last reason it did not.
func poll(ctx context.Context, window time.Duration, what string, observe func() error) error {
	deadline := time.Now().Add(window)
	var last error
	for {
		if last = observe(); last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s within %s: %w", what, window, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// collapse folds command output onto one line, so a multi-line daemon error
// survives being embedded in a failure message.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
