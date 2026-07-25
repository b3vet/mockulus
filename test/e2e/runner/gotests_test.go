// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Go-native case counts for coverage, so the join between the manifest and
// the code has to be exact. The failure this guards against is silent: a
// renamed test function leaving a manifest entry that claims a behavior nothing
// proves any more.

func writeGoTestFixture(t *testing.T, manifest, source string) string {
	t.Helper()
	dir := t.TempDir()
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, "gotests.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if source != "" {
		if err := os.WriteFile(filepath.Join(dir, "sample_test.go"), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const sampleSource = `package gotests

import "testing"

func TestFaultEmptyResponse(t *testing.T) {
	// asserts a zero-byte close, which is the fault contract
	_ = t
}

func TestSomethingElse(t *testing.T) {
	_ = t
}
`

func TestLoadGoTestsJoinsManifestToSource(t *testing.T) {
	dir := writeGoTestFixture(t, `
tests:
  - name: TestFaultEmptyResponse
    behaviors: [B-RESP-FAULT]
    why: net/http hides a zero-byte close
`, sampleSource)

	tests, err := LoadGoTests(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 1 {
		t.Fatalf("expected one test, got %d", len(tests))
	}
	if !strings.Contains(tests[0].Source, "zero-byte close") {
		t.Error("the evidence text must be the function's own source")
	}
	// Only the named function's text, not the whole file: a token appearing in a
	// neighbouring test must not satisfy this one's contract.
	if strings.Contains(tests[0].Source, "TestSomethingElse") {
		t.Error("the source must stop at the next function")
	}
}

func TestLoadGoTestsRejectsAManifestThatDoesNotMatchTheCode(t *testing.T) {
	dir := writeGoTestFixture(t, `
tests:
  - name: TestRenamedAway
    behaviors: [B-RESP-FAULT]
    why: something
`, sampleSource)

	if _, err := LoadGoTests(dir); err == nil {
		t.Fatal("a manifest naming a function that does not exist must be an error")
	}
}

func TestLoadGoTestsRejectsAnEntryClaimingNoBehaviors(t *testing.T) {
	dir := writeGoTestFixture(t, `
tests:
  - name: TestFaultEmptyResponse
    why: something
`, sampleSource)

	if _, err := LoadGoTests(dir); err == nil {
		t.Fatal("an entry claiming no behaviors cannot count for coverage and must be rejected")
	}
}

func TestLoadGoTestsIsAbsentWithoutAManifest(t *testing.T) {
	tests, err := LoadGoTests(writeGoTestFixture(t, "", sampleSource))
	if err != nil {
		t.Fatalf("a directory with no manifest is not an error: %v", err)
	}
	if len(tests) != 0 {
		t.Errorf("expected no tests, got %d", len(tests))
	}
}

// The `why` gate is what keeps the escape hatch deliberate: a Go-native case is
// harder to read than a corpus case, so one that could have been YAML should be.
func TestGoTestGatesRequireAReason(t *testing.T) {
	catalog := &Catalog{
		Behaviors: []Behavior{{ID: "B-RESP-FAULT"}},
		byID:      map[string]int{"B-RESP-FAULT": 0},
	}

	withReason := []*GoTest{{Entry: GoTestEntry{
		Name: "TestX", Behaviors: []string{"B-RESP-FAULT"}, Why: "raw socket"}}}
	if problems := goTestGates(catalog, withReason); len(problems) > 0 {
		t.Errorf("a complete entry should pass, got %v", problems)
	}

	noReason := []*GoTest{{Entry: GoTestEntry{
		Name: "TestX", Behaviors: []string{"B-RESP-FAULT"}}}}
	if problems := goTestGates(catalog, noReason); len(problems) == 0 {
		t.Error("an entry with no why must be reported")
	}

	unknown := []*GoTest{{Entry: GoTestEntry{
		Name: "TestX", Behaviors: []string{"B-NOPE"}, Why: "raw socket"}}}
	if problems := goTestGates(catalog, unknown); len(problems) == 0 {
		t.Error("an entry claiming an unknown behavior must be reported")
	}
}

// A test that produced no event at all must read as failed, never as passed:
// the package failing to build is the case where the events go missing, and
// that is exactly when a false green would be most damaging.
func TestParseGoTestJSONTreatsSilenceAsFailure(t *testing.T) {
	stream := `{"Action":"run","Test":"TestA"}
{"Action":"output","Test":"TestA","Output":"=== RUN   TestA\n"}
{"Action":"pass","Test":"TestA"}
{"Action":"run","Test":"TestB"}
{"Action":"output","Test":"TestB","Output":"    b_test.go:9: boom\n"}
{"Action":"fail","Test":"TestB"}
`
	passed, output := parseGoTestJSON([]byte(stream))

	if !passed["TestA"] {
		t.Error("TestA passed")
	}
	if passed["TestB"] {
		t.Error("TestB failed")
	}
	if passed["TestNeverRan"] {
		t.Error("a test with no events must not read as passed")
	}
	if !strings.Contains(output["TestB"], "boom") {
		t.Errorf("the failure output should be captured, got %q", output["TestB"])
	}
}

// A failing subtest fails the test the manifest names, and its output belongs
// to that test.
func TestParseGoTestJSONAttributesSubtests(t *testing.T) {
	stream := `{"Action":"run","Test":"TestA"}
{"Action":"run","Test":"TestA/case_one"}
{"Action":"output","Test":"TestA/case_one","Output":"    a_test.go:4: nope\n"}
{"Action":"fail","Test":"TestA/case_one"}
{"Action":"fail","Test":"TestA"}
`
	passed, output := parseGoTestJSON([]byte(stream))
	if passed["TestA"] {
		t.Error("a test with a failing subtest has failed")
	}
	if !strings.Contains(output["TestA"], "nope") {
		t.Errorf("subtest output belongs to the named test, got %q", output["TestA"])
	}
}

// The evidence contract is checked against the Go source rather than against
// rendered steps, which is what lets one gate serve both kinds of case.
func TestEvidenceComesFromTheGoSource(t *testing.T) {
	gt := &GoTest{
		Entry:  GoTestEntry{Name: "TestFault", Behaviors: []string{"B-RESP-FAULT"}},
		Source: "func TestFault(t *testing.T) { registerStub(t, `\"fault\": \"EMPTY_RESPONSE\"`) }",
	}
	c := syntheticCase(gt)

	if !satisfiesEvidence(c, []string{"fault"}) {
		t.Error("a token present in the test's source satisfies its contract")
	}
	if satisfiesEvidence(c, []string{"h2c"}) {
		t.Error("a token absent from the source must not satisfy the contract")
	}
}
