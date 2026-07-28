// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// A corpus case is the unit of the E2E gate: a sequence of steps against a real
// mockulus instance, with explicit expected outcomes (SPEC §19.3). Cases are
// data, not code, so the same file can be replayed deterministically on every
// PR and re-derived against pinned WireMock in topology T5.

// WireMock verification tags.
const (
	// WMVerified marks a case whose expectations are re-derived from pinned
	// WireMock; it participates in differential runs.
	WMVerified = "verified"
	// WMNotApplicable marks mockulus-specific behavior — deviations,
	// distributed behavior — that has no single-node WireMock oracle.
	WMNotApplicable = "n/a"
)

// Topology capabilities a case can declare. The runner schedules each case onto
// the cheapest topology that provides everything it asks for (SPEC §19.4).
const (
	// CapabilityCouchbase demands a real store: persistence, TTL, counters.
	CapabilityCouchbase = "couchbase"
	// CapabilityMultiPod demands replicas that can be addressed individually.
	CapabilityMultiPod = "multi-pod"
	// CapabilityExclusive is not a topology at all but a scheduling claim: the
	// case owns its deployment and runs when nothing else does.
	CapabilityExclusive = "exclusive"
)

var knownCapabilities = []string{CapabilityCouchbase, CapabilityMultiPod, CapabilityExclusive}

// Case is one corpus case.
type Case struct {
	ID string `yaml:"id"`
	// Behaviors lists the catalog ids this case provides evidence for. A case
	// referencing none is an orphan and fails completeness gate (b).
	Behaviors []string `yaml:"behaviors"`
	// Requires lists topology capabilities: couchbase, multi-pod, exclusive.
	Requires []string `yaml:"requires,omitempty"`
	// Config names the instance variant to run against (SPEC §19.4).
	Config string `yaml:"config,omitempty"`
	WM     string `yaml:"wm"`
	// WMIgnore lists JSON paths excluded from the differential diff, for fields
	// that identify the server rather than describe its behavior — a version
	// string, a product name. Each entry needs a reason in the case comment;
	// the compatibility contract is the response *shape*, not our identity.
	WMIgnore []string `yaml:"wm_ignore,omitempty"`
	// Skip, when set, must carry a linked issue and an expiry the runner
	// enforces — an expired skip fails the gate (SPEC §19.1).
	Skip  *Skip  `yaml:"skip,omitempty"`
	Steps []Step `yaml:"steps"`

	// Path is where the case was loaded from, for error messages.
	Path string `yaml:"-"`

	// evidence overrides the text the evidence-token check searches. A corpus
	// case leaves it empty and is checked against its rendered steps; a
	// Go-native case has no steps and supplies its own source instead.
	evidence string `yaml:"-"`
}

// Skip records a temporary exclusion with its justification and expiry.
type Skip struct {
	Reason  string `yaml:"reason"`
	Issue   string `yaml:"issue"`
	Expires string `yaml:"expires"`
}

// Step is one action plus its expectations. Exactly one action field is set.
type Step struct {
	Request      *HTTPStep     `yaml:"request,omitempty"`
	Admin        *HTTPStep     `yaml:"admin,omitempty"`
	Pause        string        `yaml:"pause,omitempty"`
	MetricsProbe *MetricsProbe `yaml:"metricsprobe,omitempty"`
	LogProbe     *LogProbe     `yaml:"logprobe,omitempty"`

	// StopStore and StartStore take the run's store away and give it back,
	// which is the choreography the degraded modes of SPEC §4.6 are stated in.
	// The interesting half of that contract is what keeps working, so a case
	// asserts on the serving path between the two.
	StopStore  bool `yaml:"stop_store,omitempty"`
	StartStore bool `yaml:"start_store,omitempty"`

	Expect           *Expect `yaml:"expect,omitempty"`
	ExpectEventually *Expect `yaml:"expect_eventually,omitempty"`
}

// HTTPStep is a request to the mock port or the admin port.
type HTTPStep struct {
	// Pod selects a replica in multi-pod topologies; "any" round-robins.
	Pod     string            `yaml:"pod,omitempty"`
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Body    string            `yaml:"body,omitempty"`
	// BodyFile reads the body from test/e2e/corpus/<file>.
	BodyFile string `yaml:"body_file,omitempty"`
	// Port selects which listener to use for a `request` step; admin steps
	// default to the admin port.
	Port string `yaml:"port,omitempty"`
}

// MetricsProbe asserts on a series exposed by /metrics.
type MetricsProbe struct {
	// Pod selects a replica in multi-pod topologies, as on an HTTP step. It
	// exists because metrics are per-process: an unpinned probe would read a
	// replica the round-robin chose, and a case that drove one pod and read
	// another's counters is a coin flip, not an assertion.
	Pod string `yaml:"pod,omitempty"`
	// Series is the metric name, optionally with a label selector:
	// mockulus_http_requests_total{matched="true",code="200"}
	Series string `yaml:"series"`
	// Present asserts the series exists (default true when nothing else is set).
	Present *bool `yaml:"present,omitempty"`
	// AtLeast asserts a minimum value, which is what makes a counter assertion
	// stable under concurrent cases sharing an instance.
	AtLeast *float64 `yaml:"at_least,omitempty"`
	// Within turns the probe into bounded polling, for counters a background
	// loop moves rather than the step before them: a reload failure is recorded
	// by the next poller tick, so reading once asserts the tick's timing instead
	// of the product. Never a bare sleep, for the same reason `expect_eventually`
	// is not one (SPEC §19.1).
	Within string `yaml:"within,omitempty"`
}

// LogProbe asserts on the instance's captured stdout.
type LogProbe struct {
	// Contains requires a substring in some captured log line.
	Contains string `yaml:"contains,omitempty"`
	// JSONFields requires a single JSON log line carrying all these fields.
	JSONFields map[string]string `yaml:"json_fields,omitempty"`
}

// Expect is the assertion set applied to a step's response.
type Expect struct {
	Status int `yaml:"status,omitempty"`
	// StatusMessage asserts the HTTP/1.1 reason phrase. Go's client keeps the
	// status line's text verbatim in Response.Status, which makes this the one
	// way a black-box case can observe `statusMessage` at all (SPEC §5.2).
	StatusMessage string            `yaml:"status_message,omitempty"`
	Headers       map[string]string `yaml:"headers,omitempty"`
	// HeadersContain matches a header value by substring.
	HeadersContain map[string]string `yaml:"headers_contain,omitempty"`
	HeaderAbsent   []string          `yaml:"header_absent,omitempty"`

	Body      *string `yaml:"body,omitempty"`
	BodyRegex string  `yaml:"body_regex,omitempty"`
	// BodyJSON compares the whole document structurally.
	BodyJSON any `yaml:"body_json,omitempty"`
	// BodyJSONSubset requires every field given to be present and equal,
	// ignoring anything else in the response.
	BodyJSONSubset any `yaml:"body_json_subset,omitempty"`
	// BodyJSONContains searches an array in the response for an element, which
	// is the only way to assert on a deployment-global listing: cases share one
	// instance, so a case's own entry sits at an index nothing can predict.
	BodyJSONContains []JSONContains `yaml:"body_json_contains,omitempty"`

	// Within turns the assertion into bounded polling, for behaviors whose
	// contract is eventual (SPEC §8, §11.4). Never a bare sleep.
	Within string `yaml:"within,omitempty"`
}

// JSONContains names an array in the response document and the entry that must
// be somewhere in it.
type JSONContains struct {
	// Path is a dotted path to the array — "mappings", "meta.items". A leading
	// "$." is accepted, so a path can be spelled the way a diff reports one.
	Path string `yaml:"path"`
	// Match is what one element has to satisfy, under the subset semantics of
	// body_json_subset: fields the case does not name are not compared.
	Match any `yaml:"match"`
}

// WithinDuration returns the polling window, defaulting to the generous
// propagation-class window of SPEC §19.3.
func (e *Expect) WithinDuration() (time.Duration, error) {
	if e.Within == "" {
		return defaultEventuallyWindow, nil
	}
	return time.ParseDuration(e.Within)
}

// defaultEventuallyWindow is deliberately generous: the E2E gate asserts
// eventual correctness only, never latency (SPEC §19.3).
const defaultEventuallyWindow = 15 * time.Second

// LoadCorpus reads every case file under dir.
func LoadCorpus(dir string) ([]*Case, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext == ".yaml" || ext == ".yml" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	seen := map[string]string{}
	cases := make([]*Case, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var c Case
		if err := yaml.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		c.Path = path
		if err := c.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if prev, dup := seen[c.ID]; dup {
			return nil, fmt.Errorf("%s: case id %q is already used by %s", path, c.ID, prev)
		}
		seen[c.ID] = path
		cases = append(cases, &c)
	}
	return cases, nil
}

func (c *Case) validate() error {
	if c.ID == "" {
		return errors.New("case has no id")
	}
	if len(c.Behaviors) == 0 {
		return fmt.Errorf("case %s references no catalog behavior (completeness gate b)", c.ID)
	}
	if c.WM != WMVerified && c.WM != WMNotApplicable {
		return fmt.Errorf("case %s: wm must be %q or %q, got %q",
			c.ID, WMVerified, WMNotApplicable, c.WM)
	}
	// A typo in a capability would schedule the case onto a topology that cannot
	// serve it and let it pass there — the accept-and-behave-differently the
	// gate exists to prevent. Reject it at load instead.
	for _, capability := range c.Requires {
		if !slices.Contains(knownCapabilities, capability) {
			return fmt.Errorf("case %s: unknown capability %q in requires, want one of %v",
				c.ID, capability, knownCapabilities)
		}
	}
	if len(c.Steps) == 0 {
		return fmt.Errorf("case %s has no steps", c.ID)
	}
	multiPod := c.RequiresCapability(CapabilityMultiPod)
	for i, s := range c.Steps {
		if err := s.validate(); err != nil {
			return fmt.Errorf("case %s step %d: %w", c.ID, i+1, err)
		}
		if err := s.validatePods(multiPod); err != nil {
			return fmt.Errorf("case %s step %d: %w", c.ID, i+1, err)
		}
	}
	if err := c.validateStoreChoreography(); err != nil {
		return err
	}
	if c.Skip != nil {
		if c.Skip.Issue == "" {
			return fmt.Errorf("case %s: a skip must link the issue tracking it", c.ID)
		}
		if _, err := time.Parse("2006-01-02", c.Skip.Expires); err != nil {
			return fmt.Errorf("case %s: skip expires must be YYYY-MM-DD, got %q", c.ID, c.Skip.Expires)
		}
	}
	return nil
}

// validateStoreChoreography checks the declarations a store outage needs.
//
// The run has one store and every T2/T3 deployment shares it, so taking it away
// takes it away from whatever else is running. A case that did that alongside
// others would fail them for a reason that has nothing to do with what they
// assert — and the failure would land on them, not on it. `exclusive` is the
// declaration that stops it; `couchbase` is the one that means there is a
// container to pause at all.
func (c *Case) validateStoreChoreography() error {
	if !c.TouchesStore() {
		return nil
	}
	for _, needed := range []string{CapabilityCouchbase, CapabilityExclusive} {
		if !c.RequiresCapability(needed) {
			return fmt.Errorf(
				"case %s choreographs a store outage, which needs requires: [%s]",
				c.ID, needed)
		}
	}
	return nil
}

func (s Step) validate() error {
	actions := 0
	for _, set := range []bool{s.Request != nil, s.Admin != nil, s.Pause != "",
		s.MetricsProbe != nil, s.LogProbe != nil, s.StopStore, s.StartStore} {
		if set {
			actions++
		}
	}
	if actions != 1 {
		return fmt.Errorf("a step must carry exactly one action, found %d", actions)
	}
	if s.MetricsProbe != nil && s.MetricsProbe.Within != "" {
		if _, err := time.ParseDuration(s.MetricsProbe.Within); err != nil {
			return fmt.Errorf("metricsprobe within: %w", err)
		}
	}
	if s.Expect != nil && s.ExpectEventually != nil {
		return errors.New("a step may carry expect or expect_eventually, not both")
	}
	if s.Pause != "" {
		if _, err := time.ParseDuration(s.Pause); err != nil {
			return fmt.Errorf("pause: %w", err)
		}
	}
	for _, e := range []*Expect{s.Expect, s.ExpectEventually} {
		if e == nil {
			continue
		}
		if err := e.validate(); err != nil {
			return err
		}
	}
	return nil
}

// validatePods checks every replica selector in a step. A selector past the
// first replica only resolves in a multi-pod topology, and a case that pins one
// without asking for the topology would fail mid-run on its third step rather
// than at load — with a message about a missing replica instead of about the
// declaration that is actually wrong.
func (s Step) validatePods(multiPod bool) error {
	selectors := map[string]string{}
	if s.Request != nil {
		selectors["request"] = s.Request.Pod
	}
	if s.Admin != nil {
		selectors["admin"] = s.Admin.Pod
	}
	if s.MetricsProbe != nil {
		selectors["metricsprobe"] = s.MetricsProbe.Pod
	}

	for kind, spec := range selectors {
		if spec == "" || spec == PodAny {
			continue
		}
		index, err := strconv.Atoi(spec)
		if err != nil || index < 0 {
			return fmt.Errorf("%s pod %q: want %q or a replica index", kind, spec, PodAny)
		}
		if index > 0 && !multiPod {
			return fmt.Errorf("%s pins pod %d, which needs requires: [%s]",
				kind, index, CapabilityMultiPod)
		}
	}
	return nil
}

func (e *Expect) validate() error {
	// A half-written contains clause would search the whole document, or match
	// every element, and report a pass — the vacuous assertion the gate exists
	// to prevent. Reject it at load instead.
	for i, c := range e.BodyJSONContains {
		if c.Path == "" {
			return fmt.Errorf("body_json_contains[%d]: path names the array to search and is required", i)
		}
		if c.Match == nil {
			return fmt.Errorf("body_json_contains[%d]: match is what an element must satisfy and is required", i)
		}
	}
	return nil
}

// TouchesStore reports whether the case choreographs a store outage. The runner
// puts the store back afterwards whatever the case did, so one that failed
// halfway through cannot leave the run's store paused behind it.
func (c *Case) TouchesStore() bool {
	for _, s := range c.Steps {
		if s.StopStore || s.StartStore {
			return true
		}
	}
	return false
}

// RequiresCapability reports whether the case needs a topology capability.
func (c *Case) RequiresCapability(capability string) bool {
	for _, r := range c.Requires {
		if r == capability {
			return true
		}
	}
	return false
}

// Variant returns the instance variant the case runs against.
func (c *Case) Variant() string {
	if c.Config == "" {
		return VariantDefault
	}
	return c.Config
}

// SkipExpired reports whether a skip annotation has passed its expiry, which
// the runner treats as a gate failure rather than a silent pass (SPEC §19.1).
func (c *Case) SkipExpired(now time.Time) bool {
	if c.Skip == nil {
		return false
	}
	expiry, err := time.Parse("2006-01-02", c.Skip.Expires)
	if err != nil {
		return true
	}
	return now.After(expiry.Add(24 * time.Hour))
}

// Namespace is the URL prefix a case's mock traffic must stay inside, so cases
// can run concurrently against a shared instance (SPEC §19.3).
func (c *Case) Namespace() string { return "/e2e/" + c.ID + "/" }

// UsesNamespace reports whether a mock-port path respects the case namespace.
// Admin paths and deliberately global probes are exempt.
func (c *Case) UsesNamespace(path string) bool {
	return strings.HasPrefix(path, c.Namespace())
}
