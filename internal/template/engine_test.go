// SPDX-License-Identifier: Apache-2.0

package template

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/b3vet/mockulus/internal/handlebars"
)

// The engine itself rather than the helpers it registers: the allowlist it
// exposes, the output cap of §10.4, and the lifecycle a compiled template
// actually has — compiled once at registration, cached on the stub, and
// rendered concurrently by every request that matches it thereafter.

// The allowlist of SPEC §10.3, pinned as a set. This is the sandbox boundary of
// §17 expressed as a list of names: `file`, `systemValue`, `secret` and
// `hostname` are absent because nothing in a mock server should read the
// filesystem, the environment or the host it runs on, and "absent" is a property
// of this slice and nothing else. A helper added to the registry by accident —
// or a debugging one left registered — is invisible to every other test in the
// package and to the corpus, which only ever calls helpers it knows about.
func TestTheRegistryExposesExactlyTheAllowlistOfTheSpec(t *testing.T) {
	engine := NewEngine(1<<20, func(_ []any, _ map[string]any) (any, error) { return nil, nil })

	want := []string{
		"base64", "concat", "default", "join", "jsonPath", "lookup", "lower",
		"lowercase", "math", "now", "number", "pickRandom", "randomDecimal",
		"randomInt", "randomValue", "range", "replace", "size", "split",
		"substring", "trim", "upper", "uppercase", "urlEncode",
	}

	got := engine.HelperNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the allowlist is\n  %v\nwant\n  %v", got, want)
	}

	// Sorted order is not cosmetic: the unknown-helper refusal quotes this list
	// to the stub author, and a list that reordered itself between registrations
	// would make two identical 422s look like different answers.
	if !isSorted(got) {
		t.Errorf("the helper names are not sorted: %v", got)
	}

	// The exclusions, asserted by name rather than left to the set comparison
	// above, because these are the ones whose reappearance would be a security
	// regression rather than a compatibility one.
	for _, forbidden := range []string{"file", "systemValue", "secret", "hostname", "env", "exec", "readFile"} {
		for _, name := range got {
			if name == forbidden {
				t.Errorf("%q is registered; §17 excludes it deliberately", forbidden)
			}
		}
	}

	// And the block constructs of §10.3 are the parser's, not the registry's —
	// they must not be registered as helpers, or `{{#if}}` would resolve
	// through a lookup that has nothing to do with branching.
	for _, block := range []string{"if", "unless", "each", "with"} {
		for _, name := range got {
			if name == block {
				t.Errorf("the block helper %q is registered as an ordinary helper", block)
			}
		}
	}
}

func isSorted(names []string) bool {
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			return false
		}
	}
	return true
}

// `HasTemplate` is the cheap skip of §10.1: a body or header value without it
// never reaches the engine, which is what makes templating free for the stubs
// that do not use it. A false negative here is the expensive one — a stub whose
// body really is a template would be served with its mustaches intact, which
// looks to a client like the mock is broken rather than like the gate is.
func TestHasTemplateRecognisesTheOpeningMustacheAndNothingElse(t *testing.T) {
	cases := map[string]bool{
		`{{now}}`:                         true,
		`{"at":"{{now}}"}`:                true,
		`{{{request.body}}}`:              true,
		`{{#if request.query.x}}y{{/if}}`: true,
		`{{`:                              true,
		`prefix {{`:                       true,
		``:                                false,
		`plain text`:                      false,
		`{"a":1}`:                         false,
		// A single brace is not an opening mustache, and neither is a closing
		// pair on its own: a JSON body full of braces must take the cheap path.
		`{ "nested": { "a": 1 } }`: false,
		`}}`:                       false,
		`{ {`:                      false,
		`${notHandlebars}`:         false,
	}

	for value, want := range cases {
		if got := HasTemplate(value); got != want {
			t.Errorf("HasTemplate(%q) = %v, want %v", value, got, want)
		}
	}
}

// The output cap of §10.4, at the byte either side of it. The cap exists because
// a template's expansion can be driven by the request, so the boundary is where
// a stub that has always fitted starts being refused — a check written `>=`
// would refuse a response of exactly the configured size, which is a response
// the operator configured for.
func TestTheOutputCapRefusesTheByteAfterItsLimitAndServesTheOneBefore(t *testing.T) {
	// "AAAA" plus the method is seven bytes, whatever else changes.
	const source = "AAAA{{request.method}}"

	for _, c := range []struct {
		max     int
		refused bool
		why     string
	}{
		{7, false, "an output of exactly the cap is what the cap allows"},
		{8, false, "and one byte of room to spare, plainly"},
		{6, true, "one byte over is refused"},
		{1, true, "and far over, likewise"},
	} {
		engine := NewEngine(c.max, nil)
		tpl, err := engine.Compile(source)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/cap/limit", nil)

		out, err := engine.Render(tpl, BuildContext(r, nil, nil, nil))
		switch {
		case c.refused && err == nil:
			t.Errorf("a cap of %d rendered %q, want a refusal (%s)", c.max, out, c.why)
		case !c.refused && err != nil:
			t.Errorf("a cap of %d refused a 7-byte render: %v (%s)", c.max, err, c.why)
		case !c.refused && out != "AAAAGET":
			t.Errorf("a cap of %d rendered %q, want AAAAGET", c.max, out)
		}
	}
}

// The refusal names the knob, because it reaches an operator as a response body
// rather than as a log line: a stub whose expansion depends on the request can
// be driven over the cap by a caller, and "which setting refused this" is the
// first thing whoever is looking at that 500 needs. It also has to be the
// sentinel, so a caller can tell a cap from a helper failure without reading
// the text.
func TestTheOutputCapRefusalIsTheSentinelAndNamesTheSetting(t *testing.T) {
	engine := NewEngine(16, nil)
	tpl, err := engine.Compile(`{{request.body}}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "POST", "/e2e/cap/named", nil)

	_, err = engine.Render(tpl, BuildContext(r, []byte(strings.Repeat("x", 64)), nil, nil))
	if err == nil {
		t.Fatal("a 64-byte body rendered under a 16-byte cap")
	}
	if !errors.Is(err, handlebars.ErrOutputTooLarge) {
		t.Errorf("the refusal is %v, want it to wrap ErrOutputTooLarge", err)
	}
	if !strings.Contains(err.Error(), "template_max_output_bytes") {
		t.Errorf("the refusal says %q, want it to name the setting an operator would change", err)
	}
	if !strings.Contains(err.Error(), "16") {
		t.Errorf("the refusal says %q, want the configured size in it", err)
	}
}

// The cap is the one the engine was built with, and it is spent per render
// rather than accumulated across them. A cap held on a shared builder would let
// the first few requests through and refuse the rest, which is a stub that works
// until it has been used enough — the hardest kind of failure to attribute.
func TestTheCapIsPerRenderAndIsTheOneTheEngineWasBuiltWith(t *testing.T) {
	engine := NewEngine(8, nil)
	tpl, err := engine.Compile(`{{request.method}}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/cap/repeat", nil)
	ctx := BuildContext(r, nil, nil, nil)

	for i := range 50 {
		out, err := engine.Render(tpl, ctx)
		if err != nil {
			t.Fatalf("render %d of the same template failed: %v", i, err)
		}
		if out != "GET" {
			t.Fatalf("render %d gave %q", i, out)
		}
	}

	// Two engines, one template source, different caps: the cap travels with
	// the engine and is not a package-level constant.
	roomy, tight := NewEngine(1<<20, nil), NewEngine(2, nil)
	for _, e := range []*Engine{roomy, tight} {
		tpl, err := e.Compile(`{{request.method}}`)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		_, err = e.Render(tpl, ctx)
		if e == roomy && err != nil {
			t.Errorf("the roomy engine refused a 3-byte render: %v", err)
		}
		if e == tight && err == nil {
			t.Error("the 2-byte engine served a 3-byte render")
		}
	}
}

// The cap counts bytes rather than characters, which is the only reading that
// bounds memory. A cap enforced on runes would let a body of multibyte text
// through at up to four times the configured size, and the configured size is
// there to keep a pod inside its memory limit.
func TestTheOutputCapCountsBytesRatherThanCharacters(t *testing.T) {
	// Five runes, ten bytes.
	const body = "日本語です"

	if len([]rune(body)) != 5 || len(body) != 15 {
		t.Fatalf("the fixture is %d runes and %d bytes; the assertion below assumes 5 and 15",
			len([]rune(body)), len(body))
	}

	r := httptest.NewRequestWithContext(context.Background(), "POST", "/e2e/cap/bytes", nil)
	for _, c := range []struct {
		max     int
		refused bool
	}{
		{15, false},
		{14, true},
		{5, true},
	} {
		engine := NewEngine(c.max, nil)
		tpl, err := engine.Compile(`{{request.body}}`)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		_, err = engine.Render(tpl, BuildContext(r, []byte(body), nil, nil))
		if c.refused != (err != nil) {
			t.Errorf("a cap of %d over a 5-rune, 15-byte body gave %v, refused=%v", c.max, err, c.refused)
		}
	}
}

// A caller can drive a template over its cap, which is the threat the cap exists
// for and the reason it is enforced during the render rather than checked after
// it. The template below is a fixed, innocuous stub body; the size of what it
// produces is entirely the caller's choice.
func TestACallerCanDriveATemplateOverItsCapAndIsRefusedRatherThanServed(t *testing.T) {
	engine := NewEngine(1024, nil)
	tpl, err := engine.Compile(`{{#each (range 1 request.query.n)}}{{request.body}}{{/each}}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	body := []byte(strings.Repeat("x", 64))
	for _, c := range []struct {
		n       int
		refused bool
	}{
		{4, false},
		{16, false},
		{17, true},
		{1000, true},
	} {
		r := httptest.NewRequestWithContext(context.Background(), "POST",
			"/e2e/cap/driven?n="+strconv.Itoa(c.n), nil)
		out, err := engine.Render(tpl, BuildContext(r, body, nil, nil))
		if c.refused != (err != nil) {
			t.Errorf("n=%d produced %d bytes and err=%v, refused=%v", c.n, len(out), err, c.refused)
		}
		if err != nil && len(out) != 0 {
			t.Errorf("n=%d was refused but still returned %d bytes", c.n, len(out))
		}
	}
}

// A compiled template is cached on the stub and rendered by every request that
// matches it, so one *Template is walked by many goroutines at once. Rendering
// therefore has to keep all of its state in the call: a buffer or a scope stack
// hung off the template would corrupt one response with another's data under
// exactly the load a mock server is deployed for, and would do it without an
// error anywhere.
//
// The contexts differ per goroutine so the check is not merely that nothing
// raced but that each render produced its own answer — a shared buffer that
// happened not to trip the detector would still hand back somebody else's
// method and path.
func TestOneCompiledTemplateRendersConcurrentlyWithoutCrossingRequests(t *testing.T) {
	engine := NewEngine(1<<20, nil)
	tpl, err := engine.Compile(
		`{"method":"{{request.method}}","n":"{{request.query.n}}","path":"{{request.path.[2]}}",` +
			`"upper":"{{upper request.query.n}}","sum":{{math request.query.n '+' 1}}}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	const workers = 32
	const rounds = 50

	var wg sync.WaitGroup
	errs := make(chan string, workers*rounds)

	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := strconv.Itoa(w)
			r := httptest.NewRequestWithContext(context.Background(), "POST",
				"/e2e/concurrent/w"+n+"?n="+n, nil)
			want := `{"method":"POST","n":"` + n + `","path":"w` + n + `",` +
				`"upper":"` + n + `","sum":` + strconv.Itoa(w+1) + `}`

			for range rounds {
				out, err := engine.Render(tpl, BuildContext(r, nil, nil, nil))
				if err != nil {
					errs <- "worker " + n + ": " + err.Error()
					return
				}
				if out != want {
					errs <- "worker " + n + " rendered " + out + ", want " + want
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

// Compilation happens at registration, which on a live server means while other
// requests are being served off the same engine. The registry is written once
// when the engine is built and only read afterwards, and this is the test that
// says so: a helper registered lazily, or a name cache filled on first use,
// would be a write racing every render in flight.
func TestCompilingOnALiveEngineDoesNotRaceTheRendersInFlight(t *testing.T) {
	engine := NewEngine(1<<20, nil)

	serving, err := engine.Compile(`{{upper request.method}} {{request.path}}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/concurrent/registering", nil)
	ctx := BuildContext(r, nil, nil, nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				out, err := engine.Render(serving, ctx)
				if err != nil || out != "GET /e2e/concurrent/registering" {
					t.Errorf("render during registration gave %q (%v)", out, err)
					return
				}
			}
		}()
	}

	for i := range 200 {
		source := `{{now}} {{randomValue type='UUID'}} {{substring request.body 0 ` + strconv.Itoa(i) + `}}`
		if _, err := engine.Compile(source); err != nil {
			t.Errorf("compile %d during serving: %v", i, err)
			break
		}
		// The refusal path reads the same registry and builds the same name
		// list, so it belongs in the race window too.
		if _, err := engine.Compile(`{{hostname 'h'}} {{secret 'k'}}`); err == nil {
			t.Error("an unknown helper compiled")
			break
		}
		_ = engine.HelperNames()
	}

	close(stop)
	wg.Wait()
}
