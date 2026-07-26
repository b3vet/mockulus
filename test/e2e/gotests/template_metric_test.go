// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// A counter's contract has two halves — it moves when the thing happens, and it
// does not move when the thing does not — and a corpus case can only assert the
// first. Cases share one instance per variant, so the absolute value of
// mockulus_template_render_errors_total belongs to the whole run and `at_least`
// is the strongest claim any of them can make; an upper bound would be a claim
// about what every other case did. This test owns its process, so it can read
// the counter before and after and hold it to both halves (SPEC §14.1).

// renderErrorStub renders whatever arithmetic the request asks for, so the same
// stub can be made to succeed or to fail by the query alone. Anything else — two
// stubs, or a template that always fails — would leave "the counter only counts
// failures" resting on the two stubs being otherwise identical.
const renderErrorStub = `{
  "id": "10030015-0000-4000-8000-000000000001",
  "request": {"method": "GET", "urlPath": "/e2e/template-render-metric/total"},
  "response": {"status": 200,
               "headers": {"Content-Type": "text/plain"},
               "body": "total={{math request.query.a '+' request.query.b}}",
               "transformers": ["response-template"]}}`

func TestTemplateRenderErrorsCountFailuresOnly(t *testing.T) {
	m := start(t, nil)
	m.registerStub(t, renderErrorStub)

	// A process that has served nothing has not failed to render anything, and
	// the series is exported from the start rather than springing into
	// existence on the first failure — a dashboard cannot alert on a series
	// that is absent until the incident it is meant to catch.
	if got := m.renderErrors(t); got != 0 {
		t.Fatalf("mockulus_template_render_errors_total = %v on a fresh instance, want 0", got)
	}

	if body := m.get(t, "/e2e/template-render-metric/total?a=2&b=3", http.StatusOK); body != "total=5" {
		t.Fatalf("rendered body = %q, want %q", body, "total=5")
	}
	if got := m.renderErrors(t); got != 0 {
		t.Fatalf("mockulus_template_render_errors_total = %v after a successful render, want 0", got)
	}

	body := m.get(t, "/e2e/template-render-metric/total?a=2&b=twelve", http.StatusInternalServerError)
	if !strings.Contains(body, `"twelve" is not a number`) {
		t.Fatalf("render-error body = %q, want it to carry the error text", body)
	}
	if got := m.renderErrors(t); got != 1 {
		t.Fatalf("mockulus_template_render_errors_total = %v after one failed render, want 1", got)
	}

	// And it stays put once the requests go back to being renderable, which is
	// what makes a rate over this series mean "failing now" rather than "failed
	// once, ever".
	if body := m.get(t, "/e2e/template-render-metric/total?a=10&b=32", http.StatusOK); body != "total=42" {
		t.Fatalf("rendered body = %q, want %q", body, "total=42")
	}
	if got := m.renderErrors(t); got != 1 {
		t.Fatalf("mockulus_template_render_errors_total = %v after recovery, want it to stay at 1", got)
	}
}

// get issues a mock-port request and returns the body, failing the test if the
// status is not the expected one.
func (m *mockulus) get(t *testing.T, path string, want int) string {
	t.Helper()

	resp, err := harnessClient.Get(m.mockURL(path))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("GET %s: status %d, want %d, body %s", path, resp.StatusCode, want, body)
	}
	return string(body)
}

// renderErrors reads mockulus_template_render_errors_total off /metrics. The
// series is unlabelled, so the sample line is the metric name and a value.
func (m *mockulus) renderErrors(t *testing.T) float64 {
	t.Helper()

	resp, err := harnessClient.Get(m.adminURL("/metrics"))
	if err != nil {
		t.Fatalf("scrape /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	const series = "mockulus_template_render_errors_total"
	for _, line := range strings.Split(string(body), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || name != series {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			t.Fatalf("%s: value %q is not a number", series, value)
		}
		return v
	}
	t.Fatalf("%s is not exposed on /metrics:\n%s", series, body)
	return 0
}
