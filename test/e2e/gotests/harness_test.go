// SPDX-License-Identifier: Apache-2.0

// Package gotests holds the E2E cases the YAML corpus cannot express: the ones
// whose observable is a socket, a connection or a process rather than an
// exchange an HTTP client would hand back (test/e2e/README.md).
//
// They are black-box like every other case. The process under test is the
// binary the runner built and passed in MOCKULUS_E2E_BINARY; without it the
// suite builds its own, so `go test ./test/e2e/gotests/` works on its own too.
package gotests

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain exists for one reason: the fallback build has to be cleaned up, and
// a package-level temporary directory outlives every individual test.
func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

var (
	buildOnce sync.Once
	buildPath string
	buildDir  string
	buildErr  error
)

// mockulusBinary returns the binary under test, building one only when the
// runner did not hand one over. Building is done once for the whole package:
// these tests start several processes, and a per-test build would dominate.
func mockulusBinary(t *testing.T) string {
	t.Helper()

	if p := os.Getenv("MOCKULUS_E2E_BINARY"); p != "" {
		return p
	}

	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "mockulus-gotests")
		if buildErr != nil {
			return
		}
		buildPath = filepath.Join(buildDir, "mockulus")
		// `go test` runs with the package directory as its working directory.
		// The binary is built once and used by every case, so the build is not
		// bound to the context of whichever case happened to ask for it first:
		// that case being cancelled would be recorded here as a build failure,
		// and every case after it would fail for a reason belonging to a test it
		// never ran in.
		cmd := exec.CommandContext(context.Background(), "go", "build", "-o", buildPath, "./cmd/mockulus")
		cmd.Dir = filepath.Join("..", "..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build mockulus: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return buildPath
}

// mockulus is one running process together with everything it has written.
type mockulus struct {
	// mockAddr and adminAddr are dialable host:port pairs.
	mockAddr  string
	adminAddr string

	cmd *exec.Cmd

	// started carries the startup summary once the process writes it, and
	// exited closes once it has been reaped. A lifecycle case asserts on both
	// edges, not only on the healthy middle.
	started chan startupLine
	exited  chan struct{}

	mu     sync.Mutex
	output []string
}

// startupLine is the JSON summary the process writes once both listeners are
// bound. Reading it is how the harness learns which ephemeral ports it got;
// captured stdout is a sanctioned observation surface (SPEC §19.1).
type startupLine struct {
	Msg       string `json:"msg"`
	MockAddr  string `json:"mock_addr"`
	AdminAddr string `json:"admin_addr"`
}

// start boots one mockulus on ephemeral ports with the given configuration
// overlaid, and stops it when the test ends.
func start(t *testing.T, env map[string]string) *mockulus {
	t.Helper()

	m := launch(t, env)
	if !m.awaitStartup(30 * time.Second) {
		t.Fatalf("mockulus never reported a startup line:\n%s", strings.Join(m.logs(), "\n"))
	}
	m.waitReady(t)
	return m
}

// launch starts a process and returns without waiting for it to serve — or even
// to survive. A case about a fail-loud configuration needs the process before it
// is healthy, and a case about shutdown needs it after it stops being.
func launch(t *testing.T, env map[string]string) *mockulus {
	t.Helper()

	// The child is deliberately not tied to the test's context. That context is
	// cancelled before the cleanups run, so binding the process to it would kill
	// it outright before stop() ever got to signal: every instance in the package
	// would go by SIGKILL, taking with it the lines it writes on the way out —
	// which are where a failure to shut down explains itself.
	cmd := exec.CommandContext(context.Background(), mockulusBinary(t))
	cmd.Env = append(os.Environ(),
		"MOCKULUS_PORT=0",
		"MOCKULUS_ADMIN_PORT=0",
		"MOCKULUS_LOG_FORMAT=json",
		"MOCKULUS_LOG_LEVEL=info",
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe mockulus stdout: %v", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		t.Fatalf("start mockulus: %v", err)
	}

	m := &mockulus{
		cmd:     cmd,
		started: make(chan startupLine, 1),
		exited:  make(chan struct{}),
	}
	t.Cleanup(m.stop)

	// The reader owns Wait: the pipe has to be drained to EOF before the child
	// is reaped, or the last lines written — the ones that explain a failure to
	// start — are lost with it.
	go func() {
		m.capture(stdout)
		_ = cmd.Wait()
		close(m.exited)
	}()
	return m
}

// awaitStartup waits for the startup summary and records the ephemeral ports it
// reports, returning false if the process stayed silent or exited instead.
func (m *mockulus) awaitStartup(within time.Duration) bool {
	select {
	case line := <-m.started:
		m.address(line)
		return true
	case <-m.exited:
	case <-time.After(within):
	}
	// A process can write the line and then exit; the buffered channel is still
	// holding it, and a case about shutdown wants the addresses either way.
	select {
	case line := <-m.started:
		m.address(line)
		return true
	default:
		return false
	}
}

func (m *mockulus) address(line startupLine) {
	m.mockAddr = dialable(line.MockAddr)
	m.adminAddr = dialable(line.AdminAddr)
}

// signal delivers a signal to the process under test. Signalling is one of the
// reasons these cases start their own: the corpus shares one instance per
// variant, and a signal there would end the run for every case using it.
func (m *mockulus) signal(t *testing.T, sig os.Signal) {
	t.Helper()

	if err := m.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %v: %v", sig, err)
	}
}

// awaitExit reports the exit code once the process has been reaped, or false if
// it outlived the window — which is how a case pins a shutdown that is required
// to be bounded.
func (m *mockulus) awaitExit(within time.Duration) (int, bool) {
	select {
	case <-m.exited:
		return m.cmd.ProcessState.ExitCode(), true
	case <-time.After(within):
		return 0, false
	}
}

// capture records every line the process writes and signals the startup line.
func (m *mockulus) capture(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	signalled := false
	for scanner.Scan() {
		line := scanner.Text()

		m.mu.Lock()
		m.output = append(m.output, line)
		m.mu.Unlock()

		if signalled {
			continue
		}
		var s startupLine
		if json.Unmarshal([]byte(line), &s) == nil && s.Msg == "mockulus started" {
			signalled = true
			m.started <- s
		}
	}
}

// dialable turns a reported listener address into one a client can connect to:
// a wildcard bind reports itself as [::]:PORT.
func dialable(addr string) string {
	if rest, ok := strings.CutPrefix(addr, "[::]"); ok {
		return "127.0.0.1" + rest
	}
	if rest, ok := strings.CutPrefix(addr, "0.0.0.0"); ok {
		return "127.0.0.1" + rest
	}
	return addr
}

// waitReady blocks until the instance reports it can serve.
func (m *mockulus) waitReady(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		// A process that has already exited will never answer, and polling it
		// for the rest of the window turns "it died" into "it never became
		// ready" — the same message a genuinely slow start produces, thirty
		// seconds later. The distinction is the whole diagnostic.
		select {
		case <-m.exited:
			t.Fatalf("mockulus exited with code %d before becoming ready:\n%s",
				m.cmd.ProcessState.ExitCode(), strings.Join(m.logs(), "\n"))
		default:
		}

		resp, err := httpGet(t.Context(), harnessClient, m.adminURL("/readyz"))
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("mockulus was still running but never became ready:\n%s", strings.Join(m.logs(), "\n"))
}

// harnessClient issues the harness' own administrative traffic. Tests that are
// about the connection build their own client, which is the whole reason they
// live here.
var harnessClient = &http.Client{Timeout: 30 * time.Second}

// httpGet and httpPost do what (*http.Client).Get and .Post do, with the
// caller's context carried on the request. These cases poll processes that are
// on their way out, and a request left in flight by a case that has already
// finished would go on holding a connection open against an instance nobody is
// watching any more; the context is what ends it alongside its test.
func httpGet(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func httpPost(ctx context.Context, client *http.Client, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return client.Do(req)
}

// mockURL and adminURL address the two listeners.
func (m *mockulus) mockURL(path string) string  { return "http://" + m.mockAddr + path }
func (m *mockulus) adminURL(path string) string { return "http://" + m.adminAddr + path }

// registerStub installs a stub through the ordinary admin API. Nothing here
// reaches past the public surface, so a test proves the served behavior rather
// than the harness' own shortcut.
func (m *mockulus) registerStub(t *testing.T, mapping string) {
	t.Helper()

	resp, err := httpPost(t.Context(), harnessClient, m.adminURL("/__admin/mappings"),
		"application/json", strings.NewReader(mapping))
	if err != nil {
		t.Fatalf("register stub: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register stub: status %d, body %s", resp.StatusCode, body)
	}
}

// logs returns a copy of everything the process has written, for failure
// messages.
func (m *mockulus) logs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.output...)
}

// stop terminates the process, escalating if it does not go quietly. A case
// that already waited for the exit it was asserting on lands in the first
// branch and pays nothing.
func (m *mockulus) stop() {
	if m.cmd == nil || m.cmd.Process == nil {
		return
	}
	select {
	case <-m.exited:
		return
	default:
	}

	_ = m.cmd.Process.Signal(os.Interrupt)
	select {
	case <-m.exited:
	case <-time.After(10 * time.Second):
		_ = m.cmd.Process.Kill()
		<-m.exited
	}
}
