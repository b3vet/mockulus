// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Every run emits the artifacts of SPEC §19.3: JUnit XML for CI, the behavior
// coverage matrix that is the audit trail behind the 100% claim, and — on
// failure — the transcript of the failing case.

// renderCaseAssertions flattens a case's steps to text, which is what the
// evidence-token check searches.
func renderCaseAssertions(c *Case) string {
	data, err := yaml.Marshal(c.Steps)
	if err != nil {
		return ""
	}
	return string(data)
}

// CoverageMatrix is the behavior × case × topology/variant × result record.
type CoverageMatrix struct {
	Milestone string          `json:"milestone"`
	Behaviors []MatrixEntry   `json:"behaviors"`
	Cases     []MatrixCaseRow `json:"cases"`
}

// MatrixEntry is one behavior's coverage.
type MatrixEntry struct {
	ID        string   `json:"id"`
	Milestone string   `json:"impl_milestone"`
	Kind      string   `json:"kind,omitempty"`
	InScope   bool     `json:"in_scope"`
	Exempt    string   `json:"exempt,omitempty"`
	Status    string   `json:"status,omitempty"`
	CoveredBy []string `json:"covered_by"`
}

// MatrixCaseRow is one case's outcome.
type MatrixCaseRow struct {
	ID        string   `json:"id"`
	Topology  string   `json:"topology"`
	Variant   string   `json:"variant"`
	WM        string   `json:"wm"`
	Result    string   `json:"result"`
	Behaviors []string `json:"behaviors"`
	DurationS float64  `json:"duration_seconds"`
}

func writeArtifacts(dir string, catalog *Catalog, results []*Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeCoverageMatrix(filepath.Join(dir, "behavior-coverage.json"), catalog, results); err != nil {
		return err
	}
	if err := writeJUnit(filepath.Join(dir, "junit.xml"), results); err != nil {
		return err
	}
	return writeTranscripts(filepath.Join(dir, "transcripts"), results)
}

func writeCoverageMatrix(path string, catalog *Catalog, results []*Result) error {
	passing := map[string][]string{}
	for _, r := range results {
		if !r.Passed {
			continue
		}
		label := fmt.Sprintf("%s[%s/%s]", r.Case.ID, r.Topology, r.Variant)
		for _, id := range r.Case.Behaviors {
			passing[id] = append(passing[id], label)
		}
	}

	m := CoverageMatrix{Milestone: catalog.Milestone}
	add := func(id, milestone, kind, exempt, status string) {
		covered := passing[id]
		sort.Strings(covered)
		m.Behaviors = append(m.Behaviors, MatrixEntry{
			ID:        id,
			Milestone: milestone,
			Kind:      kind,
			InScope:   exempt == "" && catalog.InScope(milestone),
			Exempt:    exempt,
			Status:    status,
			CoveredBy: covered,
		})
	}
	for _, b := range catalog.Behaviors {
		add(b.ID, b.Milestone, b.Kind, b.Exempt, b.Status)
	}
	for _, p := range catalog.Prose {
		add(p.ID, p.Milestone, p.Kind, "", p.Status)
	}
	sort.Slice(m.Behaviors, func(i, j int) bool { return m.Behaviors[i].ID < m.Behaviors[j].ID })

	for _, r := range results {
		m.Cases = append(m.Cases, MatrixCaseRow{
			ID:        r.Case.ID,
			Topology:  r.Topology,
			Variant:   r.Variant,
			WM:        r.Case.WM,
			Result:    resultLabel(r),
			Behaviors: r.Case.Behaviors,
			DurationS: r.Duration.Seconds(),
		})
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func resultLabel(r *Result) string {
	switch {
	case r.Skipped:
		return "skipped"
	case r.Passed:
		return "passed"
	default:
		return "failed"
	}
}

type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      float64       `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	Skipped   *struct{}     `xml:"skipped,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func writeJUnit(path string, results []*Result) error {
	bySuite := map[string][]*Result{}
	for _, r := range results {
		bySuite[r.Topology+"/"+r.Variant] = append(bySuite[r.Topology+"/"+r.Variant], r)
	}

	out := junitSuites{}
	names := make([]string, 0, len(bySuite))
	for name := range bySuite {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		suite := junitSuite{Name: name}
		for _, r := range bySuite[name] {
			jc := junitCase{Name: r.Case.ID, ClassName: name, Time: r.Duration.Seconds()}
			switch {
			case r.Skipped:
				jc.Skipped = &struct{}{}
				out.Skipped++
			case !r.Passed:
				jc.Failure = &junitFailure{
					Message: firstLine(r.Failure),
					Body:    r.Failure + "\n\n" + strings.Join(r.Transcript, "\n\n"),
				}
				suite.Failures++
				out.Failures++
			}
			suite.Cases = append(suite.Cases, jc)
			suite.Tests++
			out.Tests++
		}
		out.Suites = append(out.Suites, suite)
	}

	data, err := xml.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(xml.Header), data...), 0o644)
}

// writeTranscripts records the full exchange of every failing case, so a red
// gate can be diagnosed from the artifact alone.
func writeTranscripts(dir string, results []*Result) error {
	var failing []*Result
	for _, r := range results {
		if !r.Passed && !r.Skipped {
			failing = append(failing, r)
		}
	}
	if len(failing) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, r := range failing {
		body := fmt.Sprintf("case:     %s\ntopology: %s/%s\nfailure:  %s\n\n%s\n",
			r.Case.ID, r.Topology, r.Variant, r.Failure, strings.Join(r.Transcript, "\n\n"))
		if err := os.WriteFile(filepath.Join(dir, r.Case.ID+".txt"), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
