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
		// A background context, because one reason the port lookup failed is
		// that the run was cancelled, and the container still has to go.
		_ = exec.CommandContext(context.Background(), "docker", "rm", "-f", name).Run()
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
		client: &http.Client{
			Timeout: 30 * time.Second,
			// Never reuse a connection to the oracle. Jetty memoizes the parsed
			// cookies of a connection's previous request and treats a new Cookie
			// header that differs only by case as the same header, so a pooled
			// connection answers one step with the cookies of the step before
			// it. Verified against 3.13.2 with a cookies.session equalTo
			// "abc123" stub: on one reused connection `session=abc123` then
			// `SESSION=ABC123` both match, and those same two requests in the
			// opposite order both miss — the answer depends on what the
			// connection saw earlier, which no matching rule can produce.
			//
			// That is oracle-side connection state, not WireMock's cookie
			// semantics, and diffing mockulus against it manufactures failures
			// (and could just as easily manufacture agreement). A fresh
			// connection per request is free at corpus scale.
			Transport: &http.Transport{DisableKeepAlives: true},
		},
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
// Reset returns the oracle to an empty deployment between cases.
//
// It takes two calls, because WireMock's own reset does not mean what the name
// suggests: `POST /__admin/reset` restores the *baseline*, and a stub
// registered with `"persistent": true` is part of that baseline — it is written
// to the mappings directory and reloaded by the very call meant to clear it.
// One case exercising the persistent flag would therefore leave its stub in the
// oracle for the rest of the run, and every later case comparing a listing
// would be diffed against a WireMock carrying a stub mockulus has no reason to
// have. That failure names the innocent case rather than the one that caused
// it, which is the expensive kind to debug. `DELETE /__admin/mappings` is the
// call that removes persistent stubs, so the reset is both: the POST clears the
// journal and scenario state, the DELETE clears the mappings for real.
func (w *WireMock) Reset(ctx context.Context) error {
	if err := w.do(ctx, http.MethodPost, "/__admin/reset"); err != nil {
		return err
	}
	return w.do(ctx, http.MethodDelete, "/__admin/mappings")
}

func (w *WireMock) do(ctx context.Context, method, path string) error {
	req, err := http.NewRequestWithContext(ctx, method, w.Addr+path, nil)
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
	// Teardown runs on the way out of a cancelled run, so the removal is bound
	// to a background context: the run's would already be done and would kill
	// the removal, leaving the container behind.
	return exec.CommandContext(context.Background(), "docker", "rm", "-f", w.container).Run()
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
// mockPort tells the body rules which listener answered, because the one
// documented whole-body difference — the unmatched-request 404 — exists only on
// the mock port.
func DiffResponses(theirs, ours *NormalizedResponse, ignore []string, mockPort bool) []string {
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

	diffs = append(diffs, diffBodies(theirs, ours, ignore, mockPort)...)
	return diffs
}

func diffBodies(theirs, ours *NormalizedResponse, ignore []string, mockPort bool) []string {
	for _, entry := range ignore {
		if entry == IgnoreWholeBody {
			return nil
		}
		if entry == IgnoreUnmatchedBody && mockPort &&
			theirs.Status == http.StatusNotFound && ours.Status == http.StatusNotFound {
			return nil
		}
		if entry == CompareListingByIdentity {
			if diffs, isListing := diffListings(theirs.Body, ours.Body); isListing {
				return diffs
			}
		}
	}
	return diffBodyBytes(theirs.Body, ours.Body, ignore)
}

func diffBodyBytes(theirs, ours []byte, ignore []string) []string {
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

// IgnoreUnmatchedBody skips body comparison on the mock port's unmatched 404,
// and only there.
//
// That 404 is the one body mockulus deliberately does not reproduce: WireMock
// renders a near-miss table into it and mockulus does not, because scoring near
// misses on the request path would put diagnostic work in front of every
// unmatched request (deviation #2).
//
// It exists because the blunt instrument was being used for this: almost every
// matcher case contains a deliberately non-matching step, so declaring $body
// turned off body comparison for the case's *matching* steps too — and the
// response body of a matching stub is exactly what compatibility means. This
// entry gives up only the 404 body, on the one listener where the deviation
// applies, and leaves every 200 body being compared.
const IgnoreUnmatchedBody = "$unmatched-body"

// IgnoreWholeBody skips body comparison for every step of a case.
//
// Reach for it only when the difference is not confined to the unmatched 404 —
// otherwise use IgnoreUnmatchedBody, which keeps the matched bodies under
// comparison. Status and headers are still compared either way.
const IgnoreWholeBody = "$body"

// CompareListingByIdentity compares a deployment-global listing entry by entry
// instead of position by position.
//
// The admin listings are collections of the whole deployment. The oracle is
// reset before each case and so holds only that case's stubs, while corpus cases
// share one mockulus instance and so see every case's — ours is a superset of
// theirs, and neither the ordering nor `meta.total` can agree. That is a
// property of the harness, not a compatibility difference: pointed at a fresh
// instance the two servers return the same listing byte for byte.
//
// So this does not give the body up. Every entry WireMock listed must still be
// in our listing, matched by id and compared in full, and the rest of the
// envelope is compared as usual — only the collection's order and size stop
// being claims. It applies solely to a response actually shaped like a listing
// envelope, so declaring it cannot quietly weaken a case's other steps.
const CompareListingByIdentity = "$global-listing"

// diffListings compares two collection envelopes by identity. isListing is false
// when WireMock's body is not one, which leaves the ordinary body rules to run.
func diffListings(theirs, ours []byte) (diffs []string, isListing bool) {
	var theirDoc, ourDoc map[string]any
	if json.Unmarshal(theirs, &theirDoc) != nil || json.Unmarshal(ours, &ourDoc) != nil {
		return nil, false
	}
	collection, wanted, ok := collectionOf(theirDoc)
	if !ok {
		return nil, false
	}
	got, ok := ourDoc[collection].([]any)
	if !ok {
		return []string{fmt.Sprintf("body: WireMock returned a %s listing, mockulus did not",
			collection)}, true
	}

	for _, want := range wanted {
		if err := findListed(want, got); err != nil {
			diffs = append(diffs, "body: "+err.Error())
		}
	}
	if err := jsonSubset(withoutCollection(theirDoc, collection),
		withoutCollection(ourDoc, collection), "$"); err != nil {
		diffs = append(diffs, "body: "+err.Error())
	}
	return diffs, true
}

// collectionOf recognises WireMock's listing envelope — one array-valued field
// alongside {"meta":{"total":n}} — and returns the collection.
//
// Requiring the counter keeps the recognition tight. An admin error body carries
// an `errors` array and no meta, so it is not a listing and stays under the
// ordinary comparison.
func collectionOf(doc map[string]any) (name string, items []any, ok bool) {
	meta, isEnvelope := doc["meta"].(map[string]any)
	if !isEnvelope {
		return "", nil, false
	}
	if _, counted := meta["total"]; !counted {
		return "", nil, false
	}
	for field, value := range doc {
		arr, isArray := value.([]any)
		if !isArray {
			continue
		}
		if name != "" {
			// Two collections in one envelope is a shape nothing serves today,
			// and guessing which one is "the" listing would be the wrong call.
			return "", nil, false
		}
		name, items = field, arr
	}
	if name == "" {
		return "", nil, false
	}
	return name, items, true
}

// withoutCollection drops the two things a shared instance cannot match — the
// collection and its size — and leaves the rest of the envelope to be compared.
// `meta` itself survives, so a listing that stopped carrying one is still a diff.
func withoutCollection(doc map[string]any, collection string) map[string]any {
	out := make(map[string]any, len(doc))
	for field, value := range doc {
		if field == collection {
			continue
		}
		out[field] = value
	}
	if meta, ok := out["meta"].(map[string]any); ok {
		counted := make(map[string]any, len(meta))
		for field, value := range meta {
			if field == "total" {
				continue
			}
			counted[field] = value
		}
		out["meta"] = counted
	}
	return out
}

// findListed locates our copy of one of WireMock's entries.
//
// Entries are matched on their id so that a disagreement about a stub both
// servers listed is reported as a difference in that stub, rather than as its
// absence — which is the diff a reader can act on.
func findListed(want any, got []any) error {
	entry, _ := want.(map[string]any)
	id, identified := entry["id"].(string)
	if !identified {
		for _, candidate := range got {
			if jsonSubset(want, candidate, "$") == nil {
				return nil
			}
		}
		return fmt.Errorf("WireMock listed %s, mockulus's listing has no matching entry",
			compactJSON(want))
	}

	for _, candidate := range got {
		c, isObject := candidate.(map[string]any)
		if !isObject || c["id"] != id {
			continue
		}
		if err := jsonSubset(want, c, "$"); err != nil {
			return fmt.Errorf("listed entry %s: %w", id, err)
		}
		return nil
	}
	return fmt.Errorf("WireMock listed entry %s, mockulus's listing does not contain it", id)
}

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
