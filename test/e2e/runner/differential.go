// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"
)

// Topology T5 is the compatibility oracle (SPEC §5.6, §19.4). Cases tagged
// `wm: verified` are replayed against a pinned WireMock and the two servers'
// answers are diffed, so recorded expectations cannot drift away from the thing
// they claim to mirror.
//
// Only single-pod shapes take part: a distributed behavior has no single-node
// WireMock to diff against, which is why those cases are `wm: n/a` and carry
// expectations recorded from the spec instead.

// TopologyT5 identifies the differential topology.
const TopologyT5 = "T5"

// WireMock is a running pinned WireMock container.
type WireMock struct {
	Addr      string
	Version   string
	container string
	client    *http.Client
}

// StartWireMock launches the pinned image and waits for it to answer.
//
// The version comes from test/e2e/WIREMOCK_VERSION rather than a constant here,
// so bumping the oracle is one file and a reviewed expectation diff (SPEC §5.6).
func StartWireMock(ctx context.Context, versionFile string) (*WireMock, error) {
	raw, err := os.ReadFile(versionFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", versionFile, err)
	}
	image := strings.TrimSpace(string(raw))
	if image == "" {
		return nil, fmt.Errorf("%s is empty", versionFile)
	}

	name := fmt.Sprintf("mockulus-e2e-wm-%d", os.Getpid())
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()

	// Port 0 lets Docker pick, and `docker port` reports what it picked, so
	// concurrent runs on one machine do not collide.
	run := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name,
		"-p", "0:8080", image)
	out, err := run.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("start %s: %w: %s", image, err, out)
	}

	portOut, err := exec.CommandContext(ctx, "docker", "port", name, "8080/tcp").Output()
	if err != nil {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		return nil, fmt.Errorf("resolve the published port: %w", err)
	}
	hostPort := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		hostPort = "127.0.0.1" + hostPort[i:]
	}

	wm := &WireMock{
		Addr:      "http://" + hostPort,
		Version:   image,
		container: name,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	if err := wm.waitReady(ctx); err != nil {
		_ = wm.Stop()
		return nil, err
	}
	return wm, nil
}

func (w *WireMock) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.Addr+"/__admin/mappings", nil)
		if err != nil {
			return err
		}
		if resp, err := w.client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("WireMock at %s never became ready", w.Addr)
}

// Reset clears every stub between cases. Unlike the mockulus instances, which
// cases share, the oracle is reset per case: WireMock has no namespacing of its
// own, and a leftover stub from one case silently changes another's answer.
func (w *WireMock) Reset(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.Addr+"/__admin/reset", nil)
	if err != nil {
		return err
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// Stop removes the container.
func (w *WireMock) Stop() error {
	if w.container == "" {
		return nil
	}
	return exec.Command("docker", "rm", "-f", w.container).Run()
}

// Client exposes the HTTP client used against the oracle.
func (w *WireMock) Client() *http.Client { return w.client }

// Exchange is one request and the two answers it produced.
type Exchange struct {
	Method string
	Path   string
	// Ours and Theirs are the two responses, normalized.
	Ours, Theirs *NormalizedResponse
}

// NormalizedResponse is a response reduced to what the compatibility contract
// actually covers.
type NormalizedResponse struct {
	Status int
	Header map[string]string
	Body   []byte
}

// volatileHeaders are excluded from the diff: they differ between any two
// servers for reasons that have nothing to do with compatibility (SPEC §5.6).
var volatileHeaders = map[string]bool{
	"date":              true,
	"server":            true,
	"connection":        true,
	"keep-alive":        true,
	"transfer-encoding": true,
	"content-length":    true,
	"vary":              true,
	// WireMock stamps the id of the stub it matched; mockulus does not, and the
	// value would be unstable anyway.
	"matched-stub-id":   true,
	"matched-stub-name": true,
}

// Normalize reduces a response to its comparable form.
func Normalize(resp *http.Response, body []byte) *NormalizedResponse {
	out := &NormalizedResponse{
		Status: resp.StatusCode,
		Header: map[string]string{},
		Body:   body,
	}
	for name, values := range resp.Header {
		if volatileHeaders[strings.ToLower(name)] {
			continue
		}
		out.Header[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	return out
}

// DiffResponses compares two normalized responses under the rules of SPEC §5.6.
//
// JSON bodies compare with **subset semantics**: every field WireMock returned
// must be present and equal in ours, and additive fields on either side are
// ignored. That is what lets mockulus carry catalogued extras on /__admin/health
// and /__admin/version without those counting as compatibility diffs.
func DiffResponses(theirs, ours *NormalizedResponse, ignore []string) []string {
	var diffs []string

	if theirs.Status != ours.Status {
		diffs = append(diffs, fmt.Sprintf("status: WireMock %d, mockulus %d", theirs.Status, ours.Status))
	}

	for name, want := range theirs.Header {
		got, present := ours.Header[name]
		if !present {
			diffs = append(diffs, fmt.Sprintf("header %s: WireMock sent %q, mockulus sent none", name, want))
			continue
		}
		if got != want {
			diffs = append(diffs, fmt.Sprintf("header %s: WireMock %q, mockulus %q", name, want, got))
		}
	}

	diffs = append(diffs, diffBodies(theirs.Body, ours.Body, ignore)...)
	return diffs
}

func diffBodies(theirs, ours []byte, ignore []string) []string {
	for _, entry := range ignore {
		if entry == IgnoreWholeBody {
			return nil
		}
	}
	var theirDoc, ourDoc any
	theirJSON := json.Unmarshal(theirs, &theirDoc) == nil
	ourJSON := json.Unmarshal(ours, &ourDoc) == nil

	if theirJSON && ourJSON {
		theirDoc = stripIgnored(theirDoc, ignore)
		if err := jsonSubset(theirDoc, ourDoc, "$"); err != nil {
			return []string{"body: " + err.Error()}
		}
		return nil
	}
	if theirJSON != ourJSON {
		return []string{fmt.Sprintf("body: WireMock returned %s, mockulus returned %s",
			jsonness(theirJSON), jsonness(ourJSON))}
	}
	if !reflect.DeepEqual(theirs, ours) {
		return []string{fmt.Sprintf("body: WireMock %q, mockulus %q",
			truncate(string(theirs), 400), truncate(string(ours), 400))}
	}
	return nil
}

// IgnoreWholeBody is the wm_ignore entry that skips body comparison entirely,
// for a case whose body difference is itself a documented deviation — the
// unmatched-request diagnostics, where WireMock renders a near-miss table and
// mockulus deliberately does not (deviation #2). Status and headers are still
// compared, so the case still proves the parts that are compatible.
const IgnoreWholeBody = "$body"

// stripIgnored removes the declared identity fields from WireMock's document
// before the subset check, so they are neither required nor compared.
func stripIgnored(doc any, ignore []string) any {
	obj, ok := doc.(map[string]any)
	if !ok || len(ignore) == 0 {
		return doc
	}
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	for _, path := range ignore {
		delete(out, strings.TrimPrefix(path, "$."))
	}
	return out
}

func jsonness(isJSON bool) string {
	if isJSON {
		return "JSON"
	}
	return "non-JSON"
}
