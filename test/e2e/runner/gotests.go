// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Some behavior cannot be asserted from a YAML case, because the assertion is
// not about a response at all.
//
// A fault that closes the socket mid-body has no status to check; an h2c
// upgrade is a property of the connection rather than the exchange; a drain
// window only exists between SIGTERM and exit. A corpus case reaches the server
// through an HTTP client that has already hidden every one of those.
//
// So those cases are written in Go, against the raw socket and the process —
// and then joined back to the same gate. That last part is what matters: a
// behavior proved by a Go test must count for coverage exactly as a corpus case
// does, or the catalog would report a hole where there is a test, and the
// pressure would be to write a weaker YAML case instead of the real one.
//
// Joining them costs one accommodation. A corpus case's evidence contract is
// checked against its rendered steps; a Go test has no steps, so its evidence
// is its own source text. That is a stronger check rather than a weaker one:
// the tokens must appear in the code that does the asserting.

// GoTestManifest is test/e2e/gotests/gotests.yaml.
type GoTestManifest struct {
	Tests []GoTestEntry `yaml:"tests"`
}

// GoTestEntry binds one Go test function to the behaviors it proves.
type GoTestEntry struct {
	// Name is the Go test function name, matched exactly.
	Name string `yaml:"name"`
	// Behaviors lists the catalog ids this test provides evidence for.
	Behaviors []string `yaml:"behaviors"`
	// Why records what a YAML case could not express, so the escape hatch stays
	// deliberate rather than becoming the easy path.
	Why string `yaml:"why"`
	// Requires lists topology capabilities, as for a corpus case.
	Requires []string `yaml:"requires,omitempty"`
}

// GoTest is a manifest entry joined to the source that implements it.
type GoTest struct {
	Entry GoTestEntry
	// Source is the test function's body, which the evidence-token check
	// searches in place of a corpus case's rendered steps.
	Source string
	// Path is the file the function was found in.
	Path string
}

// LoadGoTests reads the manifest and locates each named function.
//
// A manifest entry with no function, or a test function claiming behaviors
// without a manifest entry, is an error: the two must agree, or coverage could
// be claimed by a test that does not exist.
func LoadGoTests(dir string) ([]*GoTest, error) {
	manifestPath := filepath.Join(dir, "gotests.yaml")
	data, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var m GoTestManifest
	if err = yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestPath, err)
	}

	sources, err := goTestSources(dir)
	if err != nil {
		return nil, err
	}

	out := make([]*GoTest, 0, len(m.Tests))
	for _, e := range m.Tests {
		if e.Name == "" {
			return nil, fmt.Errorf("%s: a test entry has no name", manifestPath)
		}
		src, ok := sources[e.Name]
		if !ok {
			return nil, fmt.Errorf(
				"%s: manifest names %s but no such test function exists under %s",
				manifestPath, e.Name, dir)
		}
		if len(e.Behaviors) == 0 {
			return nil, fmt.Errorf("%s: %s claims no behaviors, so it cannot count for coverage",
				manifestPath, e.Name)
		}
		out = append(out, &GoTest{Entry: e, Source: src.body, Path: src.path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Entry.Name < out[j].Entry.Name })
	return out, nil
}

type goTestSource struct {
	body string
	path string
}

// testFuncRE finds a top-level test function and is deliberately anchored to
// column zero, so a nested closure named like a test cannot be mistaken for one.
var testFuncRE = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`)

// goTestSources maps each test function to its body text.
func goTestSources(dir string) (map[string]goTestSource, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	out := map[string]goTestSource{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		text := string(data)

		matches := testFuncRE.FindAllStringSubmatchIndex(text, -1)
		for i, m := range matches {
			name := text[m[2]:m[3]]
			end := len(text)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			out[name] = goTestSource{body: text[m[0]:end], path: path}
		}
	}
	return out, nil
}

// goTestTimeout bounds the whole Go-native suite. These tests hold sockets open
// and wait on process exits, so they are slower than corpus cases by nature —
// but a hung one must fail the gate rather than stall it.
const goTestTimeout = 5 * time.Minute

// RunGoTests executes the Go-native suite and returns one Result per manifest
// entry.
//
// The package is compiled and run once rather than per test: these tests start
// real processes, and paying the build cost per entry would dominate.
//
// A suite that fails to build, or to run at all, comes back through the results
// rather than as an error: every entry is then a failure carrying the reason,
// which is what the coverage gates and the artifact matrix have to see. An
// unproven behavior is a failed case, not a silent one.
func RunGoTests(dir, binary string, tests []*GoTest) []*Result {
	if len(tests) == 0 {
		return nil
	}

	names := make([]string, 0, len(tests))
	for _, t := range tests {
		names = append(names, regexp.QuoteMeta(t.Entry.Name))
	}

	// The suite bounds itself with -timeout, which is what turns a hung test
	// into a failed one carrying its stack: killing the process from out here
	// instead would leave the gate with no reason to report.
	cmd := exec.CommandContext(context.Background(), "go", "test", "-json",
		"-timeout", goTestTimeout.String(),
		"-run", "^("+strings.Join(names, "|")+")$",
		"./"+filepath.ToSlash(dir))
	// The suite starts the binary under test the same way the corpus harness
	// does, so it is handed the same one rather than building its own.
	cmd.Env = append(os.Environ(), "MOCKULUS_E2E_BINARY="+binary)

	out, runErr := cmd.Output()
	passed, output := parseGoTestJSON(out)

	results := make([]*Result, 0, len(tests))
	for _, t := range tests {
		res := &Result{
			Case:     syntheticCase(t),
			Topology: "T1",
			Variant:  "gotest",
			Passed:   passed[t.Entry.Name],
		}
		if !res.Passed {
			res.Failure = strings.TrimSpace(output[t.Entry.Name])
			if res.Failure == "" {
				// A test that produced no record at all did not run — a build
				// failure, or a name that compiles to nothing. Either way the
				// behavior is unproven, and reporting it as such is the point.
				res.Failure = "test did not run"
				if runErr != nil {
					res.Failure += ": " + runErr.Error()
				}
				if stderr := goTestStderr(runErr); stderr != "" {
					res.Failure += "\n" + stderr
				}
			}
			res.Transcript = strings.Split(output[t.Entry.Name], "\n")
		}
		results = append(results, res)
	}
	return results
}

func goTestStderr(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return ""
}

// syntheticCase presents a Go test to the gates as a case, so coverage,
// artifacts and failure reporting have one shape rather than two.
func syntheticCase(t *GoTest) *Case {
	return &Case{
		ID:        t.Entry.Name,
		Behaviors: t.Entry.Behaviors,
		Requires:  t.Entry.Requires,
		WM:        WMNotApplicable,
		Path:      t.Path,
		evidence:  t.Source,
	}
}

// goTestEvent is the subset of `go test -json` this needs.
type goTestEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
	Output string `json:"Output"`
}

// parseGoTestJSON reduces the event stream to a pass flag and the captured
// output per top-level test.
func parseGoTestJSON(stream []byte) (map[string]bool, map[string]string) {
	passed := map[string]bool{}
	output := map[string]string{}

	for _, line := range strings.Split(string(stream), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev goTestEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Test == "" {
			continue
		}
		// Subtest output belongs to the test that owns it, which is what the
		// manifest names.
		top := ev.Test
		if i := strings.Index(top, "/"); i >= 0 {
			top = top[:i]
		}
		switch ev.Action {
		case "pass":
			if ev.Test == top {
				passed[top] = true
			}
		case "fail":
			passed[top] = false
		case "output":
			output[top] += ev.Output
		}
	}
	return passed, output
}

// goTestGates checks the manifest itself: every claimed behavior must exist,
// and every entry must say what a corpus case could not express.
func goTestGates(catalog *Catalog, tests []*GoTest) []string {
	var problems []string
	for _, t := range tests {
		for _, id := range t.Entry.Behaviors {
			if !catalog.Known(id) {
				problems = append(problems, fmt.Sprintf(
					"%s: go test %s references unknown behavior %q",
					t.Path, t.Entry.Name, id))
			}
		}
		if strings.TrimSpace(t.Entry.Why) == "" {
			problems = append(problems, fmt.Sprintf(
				"%s: go test %s has no `why`; a Go-native case must record what a corpus case could not express, or the escape hatch becomes the easy path",
				t.Path, t.Entry.Name))
		}
	}
	return problems
}
