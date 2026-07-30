// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// noRedirectClient answers with the redirect itself rather than following it.
// Where the admin root points is the behavior under test, and a client that
// followed would report whatever the target said — which for the disabled case
// is the same 404 the redirect's absence produces, making the two
// indistinguishable.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func getNoRedirect(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// TestUIDisabledRemovesTheSurface pins `ui_enabled: false` (SPEC §5.7).
//
// This is a Go-native case rather than a corpus one because the knob is a
// startup flag: the corpus runs its cases against shared instances started from
// a fixed set of variants (§19.4), so asserting on a differently-configured
// process would mean adding a variant for one boolean. The Go lane starts a
// process per test, which is exactly what this needs.
//
// What it must establish is that the surface is *gone* rather than merely
// inert. An empty page or a 200 saying the UI is off would both leave a route
// that exists, and §5.7 says the routes stop existing — so the assertion is the
// ordinary unsupported-endpoint 404 with code 1001, the same answer any path
// nothing claims gets.
func TestUIDisabledRemovesTheSurface(t *testing.T) {
	m := start(t, map[string]string{
		"MOCKULUS_SHUTDOWN_DRAIN": "0",
		"MOCKULUS_UI_ENABLED":     "false",
	})

	for _, path := range []string{
		"/__admin/mockulus/ui/",
		"/__admin/mockulus/ui",
		"/__admin/mockulus/ui/stubs/7f3a",
	} {
		resp := getNoRedirect(t, m.adminURL(path))
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 with the UI disabled", path, resp.StatusCode)
			continue
		}
		if code := firstErrorCode(t, body); code != 1001 {
			t.Errorf("GET %s answered error code %d, want the unsupported-endpoint 1001", path, code)
		}
		// A disabled feature must not leave its headers behind either — a CSP on
		// a 404 would say the route is still being served by something.
		if csp := resp.Header.Get("Content-Security-Policy"); csp != "" {
			t.Errorf("GET %s carried a UI Content-Security-Policy while disabled: %q", path, csp)
		}
	}

	// The redirect goes with it. This is the half a following client cannot see:
	// with the UI enabled the root is a 302, and with it disabled the route was
	// never registered, so the answer is the same 404 it was before the UI
	// existed at all.
	resp := getNoRedirect(t, m.adminURL("/"))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET / on the admin port = %d, want 404 with the UI disabled", resp.StatusCode)
	}

	// And nothing else moved: the admin API is untouched by the switch.
	api := getNoRedirect(t, m.adminURL("/__admin/mappings"))
	_ = api.Body.Close()
	if api.StatusCode != http.StatusOK {
		t.Errorf("GET /__admin/mappings = %d with the UI disabled, want 200", api.StatusCode)
	}
}

// TestUIEnabledByDefault is the other side of the switch, and the reason it is
// here rather than only in the corpus is that "on by default" is a claim about
// a process started with no configuration at all. A corpus variant sets
// something; this sets nothing but the drain window.
func TestUIEnabledByDefault(t *testing.T) {
	m := start(t, map[string]string{"MOCKULUS_SHUTDOWN_DRAIN": "0"})

	resp := getNoRedirect(t, m.adminURL("/__admin/mockulus/ui/"))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET the UI prefix = %d on a default instance, want 200", resp.StatusCode)
	}

	root := getNoRedirect(t, m.adminURL("/"))
	_ = root.Body.Close()
	if root.StatusCode != http.StatusFound {
		t.Fatalf("GET / on the admin port = %d, want a 302 on a default instance", root.StatusCode)
	}
	if loc := root.Header.Get("Location"); loc != "/__admin/mockulus/ui/" {
		t.Errorf("admin root redirects to %q, want the UI prefix", loc)
	}
}

// firstErrorCode reads the code out of a WireMock-shaped error envelope.
func firstErrorCode(t *testing.T, body []byte) int {
	t.Helper()
	var envelope struct {
		Errors []struct {
			Code int `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("response is not an error envelope: %v (body %q)", err, strings.TrimSpace(string(body)))
	}
	if len(envelope.Errors) == 0 {
		t.Fatalf("error envelope carried no errors: %q", strings.TrimSpace(string(body)))
	}
	return envelope.Errors[0].Code
}
