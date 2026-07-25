// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Executor runs one case against one instance.
type Executor struct {
	corpusDir string
	// oracle, when set, turns the run differential: every step of a
	// `wm: verified` case is replayed against pinned WireMock and the two
	// answers diffed (SPEC §5.6).
	oracle *WireMock
}

// Result is the outcome of a case.
type Result struct {
	Case     *Case
	Topology string
	Variant  string
	Passed   bool
	Skipped  bool
	Duration time.Duration
	Failure  string
	// Transcript is the full request/response record, attached to artifacts on
	// failure so a red gate is debuggable without a rerun (SPEC §19.3).
	Transcript []string

	// mockulusAddr is the instance the case ran against, so a differential step
	// can re-send the same request there.
	mockulusAddr string
}

// Run executes every step of a case in order.
func (e *Executor) Run(ctx context.Context, c *Case, inst *Instance) *Result {
	start := time.Now()
	res := &Result{Case: c, Topology: inst.Topology, Variant: inst.Variant,
		mockulusAddr: inst.MockAddr}

	if e.differential(c) {
		res.Topology = TopologyT5
		// The oracle has no namespacing of its own, so a leftover stub from an
		// earlier case would silently change this one's answer.
		if err := e.oracle.Reset(ctx); err != nil {
			res.Failure = "could not reset the WireMock oracle: " + err.Error()
			res.Duration = time.Since(start)
			return res
		}
	}

	for i, step := range c.Steps {
		if err := e.runStep(ctx, c, inst, step, res); err != nil {
			res.Failure = fmt.Sprintf("step %d: %v", i+1, err)
			res.Duration = time.Since(start)
			return res
		}
	}
	res.Passed = true
	res.Duration = time.Since(start)
	return res
}

// differential reports whether this case should also run against the oracle.
func (e *Executor) differential(c *Case) bool {
	return e.oracle != nil && c.WM == WMVerified
}

func (e *Executor) runStep(ctx context.Context, c *Case, inst *Instance, step Step, res *Result) error {
	switch {
	case step.Pause != "":
		d, err := time.ParseDuration(step.Pause)
		if err != nil {
			return err
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil

	case step.MetricsProbe != nil:
		return e.metricsProbe(ctx, inst, step)

	case step.LogProbe != nil:
		return e.logProbe(inst, step)

	case step.Request != nil:
		return e.httpStep(ctx, c, inst, step, step.Request, inst.MockAddr, res)

	case step.Admin != nil:
		return e.httpStep(ctx, c, inst, step, step.Admin, inst.AdminAddr, res)
	}
	return fmt.Errorf("step carries no action")
}

func (e *Executor) httpStep(ctx context.Context, c *Case, inst *Instance,
	step Step, hs *HTTPStep, defaultBase string, res *Result) error {

	base := defaultBase
	switch hs.Port {
	case "mock":
		base = inst.MockAddr
	case "admin":
		base = inst.AdminAddr
	case "":
	default:
		return fmt.Errorf("unknown port %q, want mock or admin", hs.Port)
	}

	body, err := e.body(hs)
	if err != nil {
		return err
	}

	expect := step.Expect
	eventually := false
	if step.ExpectEventually != nil {
		expect, eventually = step.ExpectEventually, true
	}
	if expect == nil {
		return fmt.Errorf("an http step must carry expect or expect_eventually")
	}

	var ourResponse *NormalizedResponse

	do := func() (string, error) {
		req, err := http.NewRequestWithContext(ctx, strings.ToUpper(hs.Method), base+hs.Path,
			strings.NewReader(body))
		if err != nil {
			return "", err
		}
		for k, v := range hs.Headers {
			if v == "" {
				// An explicitly empty value means "send no such header",
				// which is how a case asserts the unauthenticated path.
				continue
			}
			req.Header.Set(k, v)
		}
		if body != "" && req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
		// The authed variant needs a token on every call except the ones
		// deliberately exercising rejection, which declare the header themselves.
		if _, declared := hs.Headers["Authorization"]; inst.Variant == VariantAuthed && !declared {
			req.Header.Set("Authorization", "Token "+AdminToken)
		}

		resp, err := inst.Client().Do(req)
		if err != nil {
			return "", err
		}
		defer func() { _ = resp.Body.Close() }()
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		res.Transcript = append(res.Transcript, fmt.Sprintf("%s %s -> %d\n%s",
			strings.ToUpper(hs.Method), hs.Path, resp.StatusCode, truncate(string(respBody), 2048)))

		// Kept for the differential diff: replaying a mutating request to get a
		// second copy of the answer would change the server's state.
		ourResponse = Normalize(resp, respBody)

		return "", assertResponse(expect, resp, respBody)
	}

	if !eventually {
		if _, err := do(); err != nil {
			return err
		}
		return e.diffAgainstOracle(ctx, c, hs, body, ourResponse, res)
	}

	window, err := expect.WithinDuration()
	if err != nil {
		return fmt.Errorf("within: %w", err)
	}
	// Bounded polling, never a bare sleep (SPEC §19.1 zero-flake policy).
	deadline := time.Now().Add(window)
	var last error
	for {
		if _, err := do(); err == nil {
			return nil
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not satisfied within %s: %w", window, last)
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// diffAgainstOracle replays one step against pinned WireMock and reports any
// difference in the answers. It is a no-op unless the run is differential.
//
// The oracle sees exactly the same request; only the base URL differs. A
// difference here means mockulus and WireMock disagree about a behavior the
// corpus claims is compatible, which is the one thing topology T5 exists to
// catch.
func (e *Executor) diffAgainstOracle(ctx context.Context, c *Case, hs *HTTPStep,
	body string, ours *NormalizedResponse, res *Result) error {

	if !e.differential(c) || ours == nil {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(hs.Method),
		e.oracle.Addr+hs.Path, strings.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range hs.Headers {
		if v == "" {
			continue
		}
		req.Header.Set(k, v)
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := e.oracle.Client().Do(req)
	if err != nil {
		return fmt.Errorf("replaying against the WireMock oracle: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	theirs := Normalize(resp, raw)

	if diffs := DiffResponses(theirs, ours, c.WMIgnore); len(diffs) > 0 {
		res.Transcript = append(res.Transcript, fmt.Sprintf(
			"DIFFERENTIAL %s %s\n  WireMock: %d %s\n  mockulus: %d %s",
			strings.ToUpper(hs.Method), hs.Path,
			theirs.Status, truncate(string(theirs.Body), 400),
			ours.Status, truncate(string(ours.Body), 400)))
		return fmt.Errorf("differs from pinned WireMock: %s", strings.Join(diffs, "; "))
	}
	return nil
}

func (e *Executor) body(hs *HTTPStep) (string, error) {
	if hs.BodyFile == "" {
		return hs.Body, nil
	}
	if hs.Body != "" {
		return "", fmt.Errorf("a step may set body or body_file, not both")
	}
	data, err := os.ReadFile(filepath.Join(e.corpusDir, hs.BodyFile))
	if err != nil {
		return "", fmt.Errorf("body_file: %w", err)
	}
	return string(data), nil
}

// assertResponse applies every assertion in the expectation set, collecting all
// mismatches so one run reports every problem with the step.
func assertResponse(e *Expect, resp *http.Response, body []byte) error {
	var problems []string

	if e.Status != 0 && resp.StatusCode != e.Status {
		problems = append(problems, fmt.Sprintf("status: got %d, want %d", resp.StatusCode, e.Status))
	}
	for name, want := range e.Headers {
		if got := resp.Header.Get(name); got != want {
			problems = append(problems, fmt.Sprintf("header %s: got %q, want %q", name, got, want))
		}
	}
	for name, want := range e.HeadersContain {
		if got := resp.Header.Get(name); !strings.Contains(got, want) {
			problems = append(problems, fmt.Sprintf("header %s: got %q, want it to contain %q", name, got, want))
		}
	}
	for _, name := range e.HeaderAbsent {
		if got := resp.Header.Get(name); got != "" {
			problems = append(problems, fmt.Sprintf("header %s: want absent, got %q", name, got))
		}
	}

	if e.Body != nil && string(body) != *e.Body {
		problems = append(problems, fmt.Sprintf("body: got %q, want %q",
			truncate(string(body), 512), truncate(*e.Body, 512)))
	}
	if e.BodyRegex != "" {
		re, err := regexp.Compile(e.BodyRegex)
		if err != nil {
			problems = append(problems, fmt.Sprintf("body_regex: %v", err))
		} else if !re.Match(body) {
			problems = append(problems, fmt.Sprintf("body_regex %q did not match %q",
				e.BodyRegex, truncate(string(body), 512)))
		}
	}
	if e.BodyJSON != nil {
		if err := assertJSONEqual(e.BodyJSON, body); err != nil {
			problems = append(problems, "body_json: "+err.Error())
		}
	}
	if e.BodyJSONSubset != nil {
		if err := assertJSONSubset(e.BodyJSONSubset, body); err != nil {
			problems = append(problems, "body_json_subset: "+err.Error())
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "; "))
}

func assertJSONEqual(want any, body []byte) error {
	var got any
	if err := json.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("response is not JSON: %v", err)
	}
	wantNorm := normalizeYAMLValue(want)
	if !reflect.DeepEqual(got, wantNorm) {
		return fmt.Errorf("got %s, want %s", compactJSON(got), compactJSON(wantNorm))
	}
	return nil
}

// assertJSONSubset applies subset semantics: every field given must be present
// and equal, and anything else in the response is ignored. This is the same
// rule the differential diff uses, so mockulus' catalogued extra fields are not
// diffs (SPEC §5.6).
func assertJSONSubset(want any, body []byte) error {
	var got any
	if err := json.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("response is not JSON: %v", err)
	}
	return jsonSubset(normalizeYAMLValue(want), got, "$")
}

func jsonSubset(want, got any, path string) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: got %s, want an object", path, compactJSON(got))
		}
		for k, wv := range w {
			gv, present := g[k]
			if !present {
				return fmt.Errorf("%s.%s: missing", path, k)
			}
			if err := jsonSubset(wv, gv, path+"."+k); err != nil {
				return err
			}
		}
		return nil
	case []any:
		g, ok := got.([]any)
		if !ok {
			return fmt.Errorf("%s: got %s, want an array", path, compactJSON(got))
		}
		if len(g) < len(w) {
			return fmt.Errorf("%s: got %d elements, want at least %d", path, len(g), len(w))
		}
		for i, wv := range w {
			if err := jsonSubset(wv, g[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	default:
		if !reflect.DeepEqual(want, got) {
			return fmt.Errorf("%s: got %s, want %s", path, compactJSON(got), compactJSON(want))
		}
		return nil
	}
}

// normalizeYAMLValue converts a value decoded from YAML into the shapes
// encoding/json produces, so the two can be compared directly.
func normalizeYAMLValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeYAMLValue(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalizeYAMLValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeYAMLValue(val)
		}
		return out
	case int:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return v
	}
}

func compactJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return truncate(string(data), 512)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// metricsProbe asserts on a series exposed by /metrics.
func (e *Executor) metricsProbe(ctx context.Context, inst *Instance, step Step) error {
	probe := step.MetricsProbe

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, inst.AdminAddr+"/metrics", nil)
	if err != nil {
		return err
	}
	resp, err := inst.Client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	value, found := findSeries(string(body), probe.Series)
	if !found {
		if probe.Present != nil && !*probe.Present {
			return nil
		}
		return fmt.Errorf("metrics: series %q not exposed", probe.Series)
	}
	if probe.Present != nil && !*probe.Present {
		return fmt.Errorf("metrics: series %q should not be exposed, got %v", probe.Series, value)
	}
	if probe.AtLeast != nil && value < *probe.AtLeast {
		return fmt.Errorf("metrics: series %q is %v, want at least %v", probe.Series, value, *probe.AtLeast)
	}
	return nil
}

// findSeries locates a series in a Prometheus exposition body. A selector with
// labels must match exactly; a bare metric name matches any labelled child, and
// for a counter family the values are summed.
func findSeries(exposition, selector string) (float64, bool) {
	name, labels, hasLabels := strings.Cut(selector, "{")
	total, found := 0.0, false

	for _, line := range strings.Split(exposition, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		lineName, rest, ok := cutSeriesName(line)
		if !ok || lineName != name {
			continue
		}
		if hasLabels {
			want := strings.TrimSuffix(labels, "}")
			if !labelsMatch(rest, want) {
				continue
			}
		}
		value, ok := seriesValue(line)
		if !ok {
			continue
		}
		total += value
		found = true
	}
	return total, found
}

func cutSeriesName(line string) (name, rest string, ok bool) {
	i := strings.IndexAny(line, "{ ")
	if i < 0 {
		return "", "", false
	}
	if line[i] == '{' {
		end := strings.IndexByte(line, '}')
		if end < 0 {
			return "", "", false
		}
		return line[:i], line[i+1 : end], true
	}
	return line[:i], "", true
}

// labelsMatch requires every wanted label pair to be present on the series.
func labelsMatch(got, want string) bool {
	for _, pair := range splitLabels(want) {
		if !strings.Contains(got, pair) {
			return false
		}
	}
	return true
}

func splitLabels(s string) []string {
	var out []string
	depth := 0
	cur := strings.Builder{}
	for _, r := range s {
		switch r {
		case '"':
			depth ^= 1
			cur.WriteRune(r)
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(cur.String()))
				cur.Reset()
				continue
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func seriesValue(line string) (float64, bool) {
	i := strings.LastIndexByte(line, ' ')
	if i < 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(line[i+1:]), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// logProbe asserts on the instance's captured stdout.
func (e *Executor) logProbe(inst *Instance, step Step) error {
	probe := step.LogProbe
	logs := inst.Logs()

	if probe.Contains != "" {
		for _, line := range logs {
			if strings.Contains(line, probe.Contains) {
				return nil
			}
		}
		return fmt.Errorf("logs: no line contains %q", probe.Contains)
	}

	if len(probe.JSONFields) > 0 {
		for _, line := range logs {
			var entry map[string]any
			if json.Unmarshal([]byte(line), &entry) != nil {
				continue
			}
			if matchesFields(entry, probe.JSONFields) {
				return nil
			}
		}
		return fmt.Errorf("logs: no JSON line carries %v", probe.JSONFields)
	}
	return fmt.Errorf("logprobe carries no assertion")
}

func matchesFields(entry map[string]any, want map[string]string) bool {
	for k, v := range want {
		got, ok := entry[k]
		if !ok {
			return false
		}
		if fmt.Sprint(got) != v {
			return false
		}
	}
	return true
}
