// SPDX-License-Identifier: Apache-2.0

package template

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The templated serve path of SPEC §16.1 S3. Rendering is a walk of an
// already-parsed tree, so what these measure is the request model built for it:
// every template pays for the whole model, whatever it reads from it.

var benchTemplateBody = []byte(`{"amount":1299,"currency":"EUR","card":{"brand":"visa","last4":"4242"}}`)

// BenchmarkBuildContext is the per-request cost of assembling the model of
// SPEC §10.2, which a templated stub pays before a single node is rendered.
func BenchmarkBuildContext(b *testing.B) {
	req := httptest.NewRequestWithContext(b.Context(), "POST", "/api/v2/payments/000509/authorize?trace=abc123",
		strings.NewReader(string(benchTemplateBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Request-Id", "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
	req.Header.Set("Cookie", "session=abc; tenant=acme")

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if ctx := BuildContext(req, benchTemplateBody, nil, nil); ctx == nil {
			b.Fatal("nil context")
		}
	}
}

// BenchmarkBuildContextLargeBody is the same model over a body of the size a
// mock server standing in for a real API is handed. It is here because the
// model's cost used to scale with the body twice over — once to copy it into
// `request.body` and again to base64 it — and only the second of those was
// avoidable.
func BenchmarkBuildContextLargeBody(b *testing.B) {
	body := make([]byte, 256<<10)
	for i := range body {
		body[i] = byte('a' + i%26)
	}
	req := httptest.NewRequestWithContext(b.Context(), "POST", "/api/v2/payments/000509/authorize", nil)
	req.Header.Set("Content-Type", "application/json")

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if ctx := BuildContext(req, body, nil, nil); ctx == nil {
			b.Fatal("nil context")
		}
	}
}

// BenchmarkRenderTemplated is the whole templated response: build the model,
// then render the body §16.1 S3 describes — a jsonPath lookup and a now helper.
func BenchmarkRenderTemplated(b *testing.B) {
	engine := NewEngine(1<<20, nil)
	tpl, err := engine.Compile(`{"id":"{{request.path.[3]}}","at":"{{now}}","method":"{{request.method}}"}`)
	if err != nil {
		b.Fatalf("compile: %v", err)
	}

	req := httptest.NewRequestWithContext(b.Context(), "POST", "/api/v2/payments/000509/authorize",
		strings.NewReader(string(benchTemplateBody)))
	req.Header.Set("Content-Type", "application/json")

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ctx := BuildContext(req, benchTemplateBody, nil, nil)
		if _, err := engine.Render(tpl, ctx); err != nil {
			b.Fatalf("render: %v", err)
		}
	}
}
