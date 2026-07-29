// SPDX-License-Identifier: Apache-2.0

// Command compatmatrix generates the body of docs/compatibility.md from the
// behavior catalog, the E2E corpus and the spec rows the two are keyed on
// (SPEC §20, M6: "compat matrix doc generated from the behavior catalog +
// corpus").
//
//	go run ./scripts/compatmatrix            # rewrite docs/compatibility.md
//	go run ./scripts/compatmatrix -check     # fail if it is out of date
//
// A hand-written compatibility matrix is a document that was true once. This
// one is derived from the same three files the E2E gate is derived from, so the
// only way to change what it claims is to change what the gate enforces.
//
// The spec parsing here deliberately reproduces the row identity the gate
// computes in test/e2e/runner/spec.go: source name plus normalized key. That is
// duplication, and it is load-bearing duplication — every catalog entry must
// find its spec row or this program exits non-zero, so the two parsers
// disagreeing is a build failure rather than a quietly thinner document.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The generated block is delimited so the hand-written preamble — what the
// matrix is, how to regenerate it, what the verification tags mean — survives
// every regeneration.
const (
	beginMarker = "<!-- BEGIN GENERATED MATRIX -->"
	endMarker   = "<!-- END GENERATED MATRIX -->"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "compatmatrix: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		root   string
		docRel string
		check  bool
	)
	flag.StringVar(&root, "root", ".", "repository root")
	flag.StringVar(&docRel, "out", "docs/compatibility.md", "document to rewrite, relative to root")
	flag.BoolVar(&check, "check", false, "exit non-zero if the document is not what would be generated")
	flag.Parse()

	src, err := loadSources(root)
	if err != nil {
		return err
	}

	body, err := render(src)
	if err != nil {
		return err
	}

	path := filepath.Join(root, docRel)
	existing, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w\n"+
			"      the preamble of this document is written by hand and preserved across runs,\n"+
			"      so the generator edits the file rather than creating it: restore it with\n"+
			"      `git checkout %s`", docRel, err, docRel)
	}

	updated, err := splice(string(existing), body)
	if err != nil {
		return fmt.Errorf("%s: %w", docRel, err)
	}

	if check {
		if updated != string(existing) {
			return fmt.Errorf("%s is out of date; run `make compat-docs` and commit the result", docRel)
		}
		fmt.Printf("%s is up to date (%d behaviors, %d cases)\n",
			docRel, len(src.catalog.Behaviors), len(src.cases))
		return nil
	}

	if updated == string(existing) {
		fmt.Printf("%s is already up to date (%d behaviors, %d cases)\n",
			docRel, len(src.catalog.Behaviors), len(src.cases))
		return nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d behaviors, %d corpus cases, %d Go-native cases)\n",
		docRel, len(src.catalog.Behaviors), len(src.cases), len(src.gotests))
	return nil
}

// splice replaces the region between the markers, leaving everything else — the
// hand-written preamble above them — untouched.
func splice(doc, body string) (string, error) {
	begin := strings.Index(doc, beginMarker)
	end := strings.Index(doc, endMarker)
	if begin < 0 || end < 0 || end < begin {
		return "", fmt.Errorf("the %s / %s markers were not found in that order;\n"+
			"      the generator rewrites the region between them and preserves the rest",
			beginMarker, endMarker)
	}
	return doc[:begin+len(beginMarker)] + "\n\n" + body + "\n" + doc[end:], nil
}

// ---------------------------------------------------------------------------
// Sources
// ---------------------------------------------------------------------------

type sources struct {
	rows     map[string]specRow // keyed by "<source>|<normalized key>"
	catalog  *catalog
	cases    []*corpusCase
	gotests  []goTestEntry
	wiremock string
}

func loadSources(root string) (*sources, error) {
	spec, err := loadSpec(filepath.Join(root, "SPEC.md"))
	if err != nil {
		return nil, err
	}
	rows, err := spec.rows()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]specRow, len(rows))
	for _, r := range rows {
		byID[r.id()] = r
	}

	cat, err := loadCatalog(filepath.Join(root, "test", "e2e", "catalog"))
	if err != nil {
		return nil, err
	}
	cat.milestone, err = readTrimmed(filepath.Join(root, "test", "e2e", "CURRENT_MILESTONE"))
	if err != nil {
		return nil, err
	}
	cat.milestoneN = milestoneNumber(cat.milestone)
	if cat.milestoneN < 0 {
		return nil, fmt.Errorf("CURRENT_MILESTONE is %q, want the form M<number>", cat.milestone)
	}
	cases, err := loadCorpus(filepath.Join(root, "test", "e2e", "corpus"))
	if err != nil {
		return nil, err
	}
	gotests, err := loadGoTests(filepath.Join(root, "test", "e2e", "gotests", "gotests.yaml"))
	if err != nil {
		return nil, err
	}
	wiremock, err := readTrimmed(filepath.Join(root, "test", "e2e", "WIREMOCK_VERSION"))
	if err != nil {
		return nil, err
	}

	return &sources{
		rows: byID, catalog: cat,
		cases: cases, gotests: gotests, wiremock: wiremock,
	}, nil
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// ---------------------------------------------------------------------------
// Catalog
// ---------------------------------------------------------------------------

type behavior struct {
	ID      string `yaml:"id"`
	SpecRow string `yaml:"spec_row"`
	Anchor  string `yaml:"spec"`
	// Milestone is the milestone that implements the behavior. A row beyond the
	// cursor is catalogued but not built yet, and the matrix must say so rather
	// than inherit the spec's ✅.
	Milestone string `yaml:"impl_milestone"`
	Evidence  string `yaml:"evidence"`
	// Status is "ok" or "pending-dh" — the latter meaning the behavior is
	// catalogued but its WireMock answer has not been settled differentially.
	Status string `yaml:"status"`
	Exempt string `yaml:"exempt"`
}

type proseContract struct {
	ID      string `yaml:"id"`
	Section string `yaml:"section"`
	Anchor  string `yaml:"spec"`
	Summary string `yaml:"summary"`
}

type catalog struct {
	Behaviors []behavior
	Prose     []proseContract
	milestone string
	// milestoneN is the cursor as a number, for comparing a behavior's own.
	milestoneN int
}

func loadCatalog(dir string) (*catalog, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	c := &catalog{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if filepath.Base(path) == "prose.yaml" {
			var pf struct {
				Contracts []proseContract `yaml:"contracts"`
			}
			if err := yaml.Unmarshal(data, &pf); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			c.Prose = append(c.Prose, pf.Contracts...)
			continue
		}
		var bf struct {
			Behaviors []behavior `yaml:"behaviors"`
		}
		if err := yaml.Unmarshal(data, &bf); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		c.Behaviors = append(c.Behaviors, bf.Behaviors...)
	}
	return c, nil
}

var milestonePattern = regexp.MustCompile(`^M(\d+)$`)

func milestoneNumber(m string) int {
	match := milestonePattern.FindStringSubmatch(strings.TrimSpace(m))
	if match == nil {
		return -1
	}
	n, err := strconv.Atoi(match[1])
	if err != nil {
		return -1
	}
	return n
}

// built reports whether a behavior's milestone has landed. One beyond the
// cursor is in the catalog and in the spec but not in the binary, and a matrix
// that showed the spec's ✅ for it would be claiming support that does not
// exist yet.
func (c *catalog) built(milestone string) bool {
	n := milestoneNumber(milestone)
	return n >= 0 && n <= c.milestoneN
}

// ---------------------------------------------------------------------------
// Corpus and Go-native cases
// ---------------------------------------------------------------------------

type corpusCase struct {
	ID        string   `yaml:"id"`
	Behaviors []string `yaml:"behaviors"`
	WM        string   `yaml:"wm"`
}

func loadCorpus(dir string) ([]*corpusCase, error) {
	var cases []*corpusCase
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var c corpusCase
		if unmarshalErr := yaml.Unmarshal(data, &c); unmarshalErr != nil {
			return fmt.Errorf("%s: %w", path, unmarshalErr)
		}
		cases = append(cases, &c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

// goTestEntry is one row of the Go-native manifest. Only the join matters here;
// the runner is what checks that the named function exists and asserts what the
// catalog demands of it.
type goTestEntry struct {
	Behaviors []string `yaml:"behaviors"`
}

func loadGoTests(path string) ([]goTestEntry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Tests []goTestEntry `yaml:"tests"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return manifest.Tests, nil
}

// ---------------------------------------------------------------------------
// Evidence: how many cases bind a behavior, and whether any is differential
// ---------------------------------------------------------------------------

// evidence is what the matrix's third column reports: the cases that pin a
// behavior, and whether any of them had its expectations re-derived from pinned
// WireMock. The distinction is the whole basis of the compatibility claim, so
// it is carried per behavior rather than summarized once.
type evidence struct {
	corpus   int
	verified int
	gonative int
}

func (e evidence) tag() string {
	if e.verified > 0 {
		return "verified"
	}
	return "n/a"
}

func (e evidence) cell() string {
	total := e.corpus + e.gonative
	if total == 0 {
		return "—"
	}
	s := fmt.Sprintf("%d · %s", total, e.tag())
	if e.gonative > 0 {
		s += fmt.Sprintf(" (%d Go-native)", e.gonative)
	}
	return s
}

func (s *sources) evidenceIndex() map[string]*evidence {
	index := map[string]*evidence{}
	get := func(id string) *evidence {
		if e, ok := index[id]; ok {
			return e
		}
		e := &evidence{}
		index[id] = e
		return e
	}
	for _, c := range s.cases {
		for _, id := range c.Behaviors {
			e := get(id)
			e.corpus++
			if c.WM == "verified" {
				e.verified++
			}
		}
	}
	// A Go-native case asserts against a raw socket or a process, which pinned
	// WireMock has no counterpart for; the runner replays only `wm: verified`
	// corpus cases differentially, so these count as evidence but never as
	// verification.
	for _, t := range s.gotests {
		for _, id := range t.Behaviors {
			get(id).gonative++
		}
	}
	return index
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// supportClass buckets a spec row's v1 marker.
type supportClass int

const (
	supported supportClass = iota
	supportedWithDeviation
	unsupported
	noMarker
	// planned is not a spec marker at all: it is a behavior whose milestone has
	// not landed. It overrides whatever the spec row says, because the spec
	// states the contract and the cursor states what is built.
	planned
)

func classify(marker string) supportClass {
	switch {
	case strings.Contains(marker, "❌"):
		return unsupported
	case strings.Contains(marker, "🔶"):
		return supportedWithDeviation
	case strings.Contains(marker, "✅"):
		return supported
	default:
		return noMarker
	}
}

// group is one section of the generated matrix: a block of spec rows that share
// a shape, rendered as one table.
type group struct {
	source string
	title  string
	specRef,
	anchor string
	// itemHeader names the first column — "Endpoint", "Field", "Key".
	itemHeader string
	// extraHeaders are the columns between the item and the evidence column,
	// taken from the row's own cells.
	extraHeaders []string
	// extraCells picks which of the row's cells fill those columns.
	extraCells []int
	// codeKey wraps the item column in backticks, for blocks whose keys arrive
	// as plain text rather than as spec-formatted code.
	codeKey bool
	// v1Cell is the index of the cell holding the ✅/🔶/❌ marker, or -1.
	v1Cell int
	// notesCell is the index of the cell rendered as the trailing prose column,
	// or -1.
	notesCell int
	intro     string
}

func render(s *sources) (string, error) {
	index := s.evidenceIndex()
	var b strings.Builder

	if err := renderSummary(&b, s, index); err != nil {
		return "", err
	}

	fmt.Fprintf(&b, "## The WireMock surface\n\n")
	if err := renderRefused(&b, s); err != nil {
		return "", err
	}
	for _, g := range wireMockGroups() {
		if err := renderGroup(&b, s, index, g); err != nil {
			return "", err
		}
	}

	if err := renderDeviations(&b, s, index); err != nil {
		return "", err
	}

	fmt.Fprintf(&b, "## Beyond the WireMock surface\n\n")
	fmt.Fprintf(&b, "A single-node oracle has nothing to diff these against: an operational contract\n"+
		"under a store outage, an error code, a configuration key, a Prometheus collector.\n"+
		"Their expectations come from the spec, and the case pins the spec — which is what\n"+
		"`wm: n/a` records.\n\n")
	for _, g := range mockulusGroups() {
		if err := renderGroup(&b, s, index, g); err != nil {
			return "", err
		}
	}
	renderProse(&b, s, index)

	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func wireMockGroups() []group {
	return []group{
		{
			source: sourceAdminEndpoints, title: "Admin API endpoints", specRef: "SPEC §5.1",
			itemHeader: "Method & path", v1Cell: 0, notesCell: 1,
			intro: "Every `/__admin` path mockulus answers. Anything not listed — and every path " +
				"under `/__admin/recordings`, `/__admin/proxy` and `/__admin/certificates` — is 404 " +
				"with an error body carrying code 1001 and a link to ROADMAP.md.",
		},
		{
			source: sourceStubTopLevel, title: "Stub mapping — top-level fields", specRef: "SPEC §5.2",
			itemHeader: "Field", v1Cell: 0, notesCell: 1,
		},
		{
			source: sourceStubRequest, title: "Stub mapping — `request`", specRef: "SPEC §5.2",
			itemHeader: "Field", v1Cell: 0, notesCell: 1,
		},
		{
			source: sourceStubMatchers, title: "Content matchers", specRef: "SPEC §5.2",
			itemHeader: "Matcher", v1Cell: 0, notesCell: 1,
			intro: "Used in `bodyPatterns`, as the values of `headers`, `queryParameters`, `cookies`, " +
				"`pathParameters` and `formParameters`, and by verification criteria and " +
				"`find-by-metadata`.",
		},
		{
			source: sourceStubResponse, title: "Stub mapping — `response`", specRef: "SPEC §5.2",
			itemHeader: "Field", v1Cell: 0, notesCell: 1,
		},
		{
			source: sourceTemplateHelpers, title: "Response-template helpers", specRef: "SPEC §10.3",
			itemHeader: "Helper(s)", v1Cell: -1, notesCell: 0,
			intro: "The allowlist. Any other helper name — `xPath`, `soapXPath`, `formatXml`, `jwt`, " +
				"`secret`, `systemValue`, `hostname`, `file` — is 422 code 1002 at registration, " +
				"naming the helper. Environment, file and system access are excluded deliberately " +
				"(SPEC §17).",
		},
	}
}

func mockulusGroups() []group {
	return []group{
		{
			source: sourceDegradedModes, title: "Degraded modes", specRef: "SPEC §4.6",
			itemHeader: "Condition", v1Cell: -1, notesCell: 0,
			intro: "What the server does when the store is not there. The half worth reading is " +
				"what keeps working.",
		},
		{
			source: sourceErrorCatalog, title: "Error catalog", specRef: "SPEC Appendix B",
			itemHeader: "Code", v1Cell: -1, notesCell: 1,
			extraHeaders: []string{"HTTP"}, extraCells: []int{0},
			intro: "Every rejection carries one of these in a WireMock-shaped error envelope, with a " +
				"JSON pointer at the offending field. A 422 lists **all** problems in one response.",
		},
		{
			source: sourceConfigKeys, title: "Configuration keys", specRef: "SPEC §13",
			itemHeader: "Key", v1Cell: -1, notesCell: 1,
			extraHeaders: []string{"Default"}, extraCells: []int{0},
			intro: "Precedence is env var > YAML file > default; the env spelling is `MOCKULUS_` plus " +
				"the key in upper snake case.",
		},
		{
			source: sourceMetrics, title: "Metrics", specRef: "SPEC §14.1",
			itemHeader: "Collector", codeKey: true, v1Cell: -1, notesCell: -1,
			extraHeaders: []string{"Labels", "Type"}, extraCells: []int{0, 1},
			intro: "Prometheus exposition on the admin port's `/metrics`. Low-cardinality by design: " +
				"no per-stub labels, so a 10k-stub deployment does not mint 10k series. The Type " +
				"column reproduces what the spec's collector block states, and that block names a " +
				"type only where it declares one collector per line — `/metrics` itself carries a " +
				"`# TYPE` line for every series either way.",
		},
	}
}

func renderSummary(b *strings.Builder, s *sources, index map[string]*evidence) error {
	var wmSupported, wmDeviating, wmUnsupported int
	var wmPlanned []string
	for _, g := range wireMockGroups() {
		rows, err := s.groupRows(g)
		if err != nil {
			return err
		}
		for _, r := range rows {
			switch r.class {
			case supported, noMarker:
				wmSupported++
			case supportedWithDeviation:
				wmDeviating++
			case unsupported:
				wmUnsupported++
			case planned:
				wmPlanned = append(wmPlanned, r.behavior.ID+" ("+r.behavior.Milestone+")")
			}
		}
	}

	verifiedCases, naCases := 0, 0
	for _, c := range s.cases {
		if c.WM == "verified" {
			verifiedCases++
		} else {
			naCases++
		}
	}

	exempt := 0
	uncovered := []string{}
	for _, bh := range s.catalog.Behaviors {
		if bh.Exempt != "" {
			exempt++
			continue
		}
		if index[bh.ID] == nil {
			uncovered = append(uncovered, bh.ID)
		}
	}

	fmt.Fprintf(b, "## At a glance\n\n")
	fmt.Fprintf(b, "| | Count |\n|---|---:|\n")
	fmt.Fprintf(b, "| WireMock surface — supported | %d |\n", wmSupported)
	fmt.Fprintf(b, "| WireMock surface — supported with a documented deviation | %d |\n", wmDeviating)
	fmt.Fprintf(b, "| WireMock surface — not supported (422 or 404, with a ROADMAP pointer) | %d |\n", wmUnsupported)
	if len(wmPlanned) > 0 {
		fmt.Fprintf(b, "| WireMock surface — specified but not yet built | %d |\n", len(wmPlanned))
	}
	fmt.Fprintf(b, "| Deliberate deviations from WireMock | %d |\n", s.countRows(sourceDeviations))
	fmt.Fprintf(b, "| Catalogued behaviors in total | %d |\n", len(s.catalog.Behaviors))
	fmt.Fprintf(b, "| … of those, with no distinct observable of their own (reviewed exemptions) | %d |\n", exempt)
	fmt.Fprintf(b, "| Behaviors stated in prose rather than a table | %d |\n", len(s.catalog.Prose))
	fmt.Fprintf(b, "| E2E corpus cases | %d |\n", len(s.cases))
	fmt.Fprintf(b, "| … `wm: verified` — expectations re-derived from `%s` | %d |\n", s.wiremock, verifiedCases)
	fmt.Fprintf(b, "| … `wm: n/a` — expectations from the spec | %d |\n", naCases)
	fmt.Fprintf(b, "| Go-native cases (raw socket, process lifecycle) | %d |\n", len(s.gotests))
	fmt.Fprintf(b, "\n")

	fmt.Fprintf(b, "Milestone cursor `%s`; oracle pinned at `%s`. SPEC §5.6 sets ≥300 differentially\n"+
		"verified cases as a v1.0 release criterion.\n\n", s.catalog.milestone, s.wiremock)

	if len(wmPlanned) > 0 {
		sort.Strings(wmPlanned)
		fmt.Fprintf(b, "**Specified but not yet built** — the spec states the contract and the catalog\n"+
			"holds the behavior, but the milestone that implements it has not landed, so the row\n"+
			"below carries ⏳ rather than the spec's marker: %s.\n\n",
			"`"+strings.Join(wmPlanned, "`, `")+"`")
	}

	var pending []string
	for _, bh := range s.catalog.Behaviors {
		if bh.Status == "pending-dh" {
			pending = append(pending, bh.ID)
		}
	}
	if len(pending) > 0 {
		sort.Strings(pending)
		fmt.Fprintf(b, "**Awaiting differential verification** — catalogued, but the WireMock answer has\n"+
			"not been settled against the pinned image yet: %s. SPEC §20 requires this list to be\n"+
			"empty at v1.0.\n\n", "`"+strings.Join(pending, "`, `")+"`")
	}

	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		fmt.Fprintf(b, "**%d catalogued behaviors have no case binding them yet**: %s.\n\n",
			len(uncovered), "`"+strings.Join(uncovered, "`, `")+"`")
	} else {
		fmt.Fprintf(b, "Every catalogued behavior is bound by at least one case. "+
			"The E2E gate fails when that stops being true (SPEC §19.2).\n\n")
	}
	return nil
}

// renderRefused lists the refused surface before the tables that repeat it. It
// is the first question a migration asks, and the answer a reader should not
// have to assemble from five tables.
func renderRefused(b *strings.Builder, s *sources) error {
	type refusal struct {
		item, answer, note, id string
	}
	var out []refusal
	for _, g := range wireMockGroups() {
		rows, err := s.groupRows(g)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.class != unsupported {
				continue
			}
			out = append(out, refusal{
				item:   tableCell(r.row.Key),
				answer: tableCell(r.marker),
				note:   tableCell(cellAt(r.row, g.notesCell)),
				id:     r.behavior.ID,
			})
		}
	}
	if len(out) == 0 {
		return nil
	}

	fmt.Fprintf(b, "### What is refused\n\n")
	fmt.Fprintf(b, "%d rows of the surface below are not implemented, and every one of them is "+
		"refused rather than ignored. A mapping carrying one of the stub fields never "+
		"registers, so a suite that depends on it fails when it loads its mappings — not "+
		"later, and not quietly; an admin path that is not implemented is 404 with code 1001 "+
		"rather than a plausible-looking empty answer. [ROADMAP.md](../ROADMAP.md) tracks each "+
		"with a design sketch and a size.\n\n", len(out))
	writeTableHeader(b, []string{"Feature", "Answer", "Behavior", "Note"})
	for _, r := range out {
		writeTableRow(b, []string{r.item, r.answer, "`" + r.id + "`", r.note})
	}
	fmt.Fprintf(b, "\n")
	return nil
}

// groupRow is a spec row joined to its catalog entry.
type groupRow struct {
	row      specRow
	behavior *behavior
	marker   string
	class    supportClass
}

func (s *sources) groupRows(g group) ([]groupRow, error) {
	var out []groupRow
	for i := range s.catalog.Behaviors {
		bh := &s.catalog.Behaviors[i]
		source, _, ok := strings.Cut(bh.SpecRow, "|")
		if !ok || source != g.source {
			continue
		}
		row, found := s.rows[bh.SpecRow]
		if !found {
			return nil, fmt.Errorf("catalog entry %s names spec row %q, which this parser did not find in SPEC.md",
				bh.ID, bh.SpecRow)
		}
		marker := ""
		if g.v1Cell >= 0 && g.v1Cell < len(row.Cells) {
			marker = row.Cells[g.v1Cell]
		}
		class := classify(marker)
		if !s.catalog.built(bh.Milestone) {
			class = planned
		}
		out = append(out, groupRow{row: row, behavior: bh, marker: marker, class: class})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].row.Order < out[j].row.Order })
	return out, nil
}

func (s *sources) countRows(source string) int {
	n := 0
	for _, bh := range s.catalog.Behaviors {
		if strings.HasPrefix(bh.SpecRow, source+"|") {
			n++
		}
	}
	return n
}

func renderGroup(b *strings.Builder, s *sources, index map[string]*evidence, g group) error {
	rows, err := s.groupRows(g)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("group %q matched no catalog entries", g.title)
	}

	anchor := rows[0].behavior.Anchor
	fmt.Fprintf(b, "### %s\n\n", g.title)
	fmt.Fprintf(b, "[%s](../SPEC.md%s) · %d behaviors\n\n", g.specRef, anchor, len(rows))
	if g.intro != "" {
		fmt.Fprintf(b, "%s\n\n", g.intro)
	}

	header := []string{g.itemHeader}
	if g.v1Cell >= 0 {
		header = append(header, "v1")
	}
	header = append(header, g.extraHeaders...)
	header = append(header, "Evidence", "Behavior")
	if g.notesCell >= 0 {
		header = append(header, "Notes")
	}
	writeTableHeader(b, header)

	var exemptions []string
	for _, r := range rows {
		key := tableCell(r.row.Key)
		if g.codeKey {
			key = "`" + key + "`"
		}
		cells := []string{key}
		if g.v1Cell >= 0 {
			cells = append(cells, marker(r.class, r.marker))
		}
		for _, i := range g.extraCells {
			cell := tableCell(cellAt(r.row, i))
			if g.codeKey && cell != "" {
				cell = "`" + cell + "`"
			}
			cells = append(cells, cell)
		}
		cells = append(cells, evidenceCell(index, r.behavior), "`"+r.behavior.ID+"`")
		if g.notesCell >= 0 {
			cells = append(cells, tableCell(cellAt(r.row, g.notesCell)))
		}
		writeTableRow(b, cells)

		if r.behavior.Exempt != "" {
			exemptions = append(exemptions, fmt.Sprintf("`%s` — %s",
				normalizeCell(r.row.Key), r.behavior.Exempt))
		}
	}
	fmt.Fprintf(b, "\n")

	if len(exemptions) > 0 {
		fmt.Fprintf(b, "Rows marked ○ have no distinct observable of their own — a tuning knob whose\n"+
			"effect is asserted through the behavior it protects. The exemption is reviewed, not\n"+
			"automatic:\n\n")
		for _, e := range exemptions {
			fmt.Fprintf(b, "- %s\n", e)
		}
		fmt.Fprintf(b, "\n")
	}
	return nil
}

func renderDeviations(b *strings.Builder, s *sources, index map[string]*evidence) error {
	rows, err := s.groupRows(group{source: sourceDeviations, v1Cell: -1, notesCell: 0, title: "deviations"})
	if err != nil {
		return err
	}
	anchor := rows[0].behavior.Anchor
	fmt.Fprintf(b, "## Deliberate deviations\n\n")
	fmt.Fprintf(b, "[SPEC §5.5](../SPEC.md%s) · %d deviations\n\n", anchor, len(rows))
	fmt.Fprintf(b, "The complete list — every place a request that WireMock would accept is answered\n"+
		"differently here, or refused. Each is deliberate and, where it makes sense, has a\n"+
		"configuration knob that restores WireMock's behavior. Read this section before\n"+
		"pointing an existing suite at mockulus: it is where an afternoon goes.\n\n")

	for _, r := range rows {
		fmt.Fprintf(b, "**%d.** %s\n\n", deviationNumber(r.row.Key), prose(cellAt(r.row, 0)))
		fmt.Fprintf(b, "> `%s` · %s\n\n", r.behavior.ID, evidenceSentence(index, r.behavior))
	}
	return nil
}

func renderProse(b *strings.Builder, s *sources, index map[string]*evidence) {
	fmt.Fprintf(b, "## Behaviors stated in prose\n\n")
	fmt.Fprintf(b, "%d contracts are stated as prose rather than as a table, so they cannot be derived\n"+
		"mechanically the way every row above was. They are catalogued by hand against a hash\n"+
		"of the section they encode: editing that prose fails the gate until a person re-reads\n"+
		"it and re-syncs the entry. All three are the distributed form of something a\n"+
		"single-process server gets for free, which is why none has an oracle.\n\n",
		len(s.catalog.Prose))
	writeTableHeader(b, []string{"Contract", "Section", "Evidence", "Behavior"})
	for i := range s.catalog.Prose {
		p := &s.catalog.Prose[i]
		e := index[p.ID]
		cell := "—"
		if e != nil {
			cell = e.cell()
		}
		writeTableRow(b, []string{
			tableCell(p.Summary),
			fmt.Sprintf("[§%s](../SPEC.md%s)", p.Section, p.Anchor),
			cell,
			"`" + p.ID + "`",
		})
	}
	fmt.Fprintf(b, "\n")
}

var deviationKey = regexp.MustCompile(`^deviation-(\d+)$`)

func deviationNumber(key string) int {
	m := deviationKey.FindStringSubmatch(normalizeCell(key))
	if m == nil {
		return 0
	}
	n := 0
	for _, c := range m[1] {
		n = n*10 + int(c-'0')
	}
	return n
}

func cellAt(r specRow, i int) string {
	if i < 0 || i >= len(r.Cells) {
		return ""
	}
	return r.Cells[i]
}

func marker(class supportClass, m string) string {
	switch class {
	case supported:
		return "✅"
	case supportedWithDeviation:
		return "🔶"
	case unsupported:
		// The marker carries the refusal: "❌ 422" or a bare "❌" (404).
		return tableCell(m)
	case planned:
		return "⏳"
	default:
		return ""
	}
}

func evidenceCell(index map[string]*evidence, bh *behavior) string {
	if bh.Exempt != "" {
		return "○"
	}
	e := index[bh.ID]
	if e == nil {
		return "—"
	}
	return e.cell()
}

// evidenceSentence is the deviation list's form of the same fact: how many
// cases pin the deviation, whether any was diffed against pinned WireMock, and
// the assertion the catalog demands of them.
func evidenceSentence(index map[string]*evidence, bh *behavior) string {
	e := index[bh.ID]
	if e == nil {
		return "no case binds this yet"
	}
	total := e.corpus + e.gonative
	noun := "cases"
	if total == 1 {
		noun = "case"
	}
	if e.gonative > 0 {
		noun = fmt.Sprintf("%s (%d Go-native)", noun, e.gonative)
	}
	return fmt.Sprintf("%d %s · wm: %s · %s", total, noun, e.tag(), prose(bh.Evidence))
}

// ---------------------------------------------------------------------------
// Markdown helpers
// ---------------------------------------------------------------------------

func writeTableHeader(b *strings.Builder, header []string) {
	fmt.Fprintf(b, "| %s |\n", strings.Join(header, " | "))
	fmt.Fprintf(b, "|%s\n", strings.Repeat("---|", len(header)))
}

func writeTableRow(b *strings.Builder, cells []string) {
	filled := make([]string, len(cells))
	for i, c := range cells {
		if strings.TrimSpace(c) == "" {
			c = "—"
		}
		filled[i] = c
	}
	fmt.Fprintf(b, "| %s |\n", strings.Join(filled, " | "))
}

// tableCell prepares spec text for a Markdown table cell. Spec text arrives
// from two places — table cells, where a literal pipe is already escaped, and
// code fences and numbered lists, where it is not — so both are normalized to
// an unescaped pipe and then escaped once.
func tableCell(s string) string {
	s = strings.ReplaceAll(s, `\|`, "|")
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.TrimSpace(collapse(s))
}

// prose prepares spec text for a paragraph, where a pipe needs no escaping.
func prose(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, `\|`, "|"))
}

var whitespaceRun = regexp.MustCompile(`\s+`)

func collapse(s string) string { return whitespaceRun.ReplaceAllString(s, " ") }

// normalizeCell reduces a table cell to comparable text, exactly as the E2E
// runner does when it computes a row's identity.
func normalizeCell(s string) string {
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, `\|`, "|")
	return strings.TrimSpace(collapse(s))
}

// ---------------------------------------------------------------------------
// Spec parsing
//
// A deliberate re-derivation of the row identity the E2E gate computes in
// test/e2e/runner/spec.go. Every catalog entry must resolve against it, so the
// two disagreeing fails this program rather than thinning the document.
// ---------------------------------------------------------------------------

const (
	sourceAdminEndpoints  = "spec:5.1:admin-endpoints"
	sourceStubTopLevel    = "spec:5.2:stub-top-level"
	sourceStubRequest     = "spec:5.2:stub-request"
	sourceStubMatchers    = "spec:5.2:content-matchers"
	sourceStubResponse    = "spec:5.2:stub-response"
	sourceDeviations      = "spec:5.5:deviations"
	sourceDegradedModes   = "spec:4.6:degraded-modes"
	sourceTemplateHelpers = "spec:10.3:template-helpers"
	sourceConfigKeys      = "spec:13:config"
	sourceMetrics         = "spec:14.1:metrics"
	sourceErrorCatalog    = "spec:B:error-catalog"
)

type specRow struct {
	Source string
	Key    string
	Cells  []string
	Anchor string
	// Order is the row's position within its block, so the matrix reads in the
	// order the spec states it rather than the order the catalog files happen
	// to list it.
	Order int
}

func (r specRow) id() string { return r.Source + "|" + normalizeCell(r.Key) }

type specTable struct {
	heading  string
	anchor   string
	preamble string
	rows     [][]string
}

type specDoc struct {
	lines  []string
	tables []specTable
}

func loadSpec(path string) (*specDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	d := &specDoc{lines: strings.Split(string(data), "\n")}
	d.parse()
	return d, nil
}

func (d *specDoc) parse() {
	heading, anchor, preamble := "", "", ""
	inFence := false

	for i := 0; i < len(d.lines); i++ {
		line := d.lines[i]

		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heading = strings.TrimSpace(strings.TrimLeft(line, "# "))
			anchor = headingAnchor(heading)
			preamble = ""
			continue
		}
		if isTableRow(line) && i+1 < len(d.lines) && isTableDivider(d.lines[i+1]) {
			t := specTable{heading: heading, anchor: anchor, preamble: preamble}
			i += 2
			for i < len(d.lines) && isTableRow(d.lines[i]) {
				t.rows = append(t.rows, splitRow(d.lines[i]))
				i++
			}
			i--
			d.tables = append(d.tables, t)
			continue
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			preamble = trimmed
		}
	}
}

func isTableRow(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "|") }

func isTableDivider(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "|") {
		return false
	}
	return strings.Trim(t, "|-: ") == ""
}

func splitRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")

	var cells []string
	var cur bytes.Buffer
	for i := 0; i < len(t); i++ {
		switch {
		case t[i] == '\\' && i+1 < len(t) && t[i+1] == '|':
			cur.WriteString(`\|`)
			i++
		case t[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(t[i])
		}
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

var anchorDrop = regexp.MustCompile(`[^a-z0-9\- ]`)

func headingAnchor(heading string) string {
	s := strings.ToLower(heading)
	s = strings.ReplaceAll(s, "`", "")
	s = anchorDrop.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, " ", "-")
	return "#" + s
}

func (d *specDoc) findTable(headingPrefix, preambleContains string) *specTable {
	for i := range d.tables {
		t := &d.tables[i]
		if !strings.HasPrefix(t.heading, headingPrefix) {
			continue
		}
		if preambleContains != "" && !strings.Contains(t.preamble, preambleContains) {
			continue
		}
		return t
	}
	return nil
}

func (d *specDoc) rows() ([]specRow, error) {
	var out []specRow
	var missing []string

	table := func(source, headingPrefix, preamble string) {
		t := d.findTable(headingPrefix, preamble)
		if t == nil {
			missing = append(missing, fmt.Sprintf("%s (heading %q, preamble %q)",
				source, headingPrefix, preamble))
			return
		}
		for i, row := range t.rows {
			if len(row) == 0 {
				continue
			}
			out = append(out, specRow{
				Source: source, Key: row[0], Cells: append([]string{}, row[1:]...),
				Anchor: t.anchor, Order: i,
			})
		}
	}

	table(sourceAdminEndpoints, "5.1", "")
	table(sourceStubTopLevel, "5.2", "Top level:")
	table(sourceStubRequest, "5.2", "`request` object:")
	table(sourceStubMatchers, "5.2", "Content matchers")
	table(sourceStubResponse, "5.2", "`response` object:")
	table(sourceDegradedModes, "4.6", "")
	table(sourceTemplateHelpers, "10.3", "")
	table(sourceConfigKeys, "13", "")
	table(sourceErrorCatalog, "Appendix B", "")

	deviations, err := d.deviations()
	if err != nil {
		missing = append(missing, err.Error())
	}
	out = append(out, deviations...)

	metrics, err := d.metrics()
	if err != nil {
		missing = append(missing, err.Error())
	}
	out = append(out, metrics...)

	if len(missing) > 0 {
		return nil, fmt.Errorf("spec structure changed; these blocks were not found:\n  - %s",
			strings.Join(missing, "\n  - "))
	}
	return out, nil
}

var deviationItem = regexp.MustCompile(`^(\d+)\.\s+(.*)$`)

func (d *specDoc) deviations() ([]specRow, error) {
	start, anchor := -1, ""
	for i, line := range d.lines {
		if strings.HasPrefix(line, "#") && strings.Contains(line, "5.5") {
			start = i
			anchor = headingAnchor(strings.TrimSpace(strings.TrimLeft(line, "# ")))
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("%s: section 5.5 not found", sourceDeviations)
	}
	var out []specRow
	for i := start + 1; i < len(d.lines); i++ {
		if strings.HasPrefix(d.lines[i], "#") {
			break
		}
		m := deviationItem.FindStringSubmatch(strings.TrimSpace(d.lines[i]))
		if m == nil {
			continue
		}
		out = append(out, specRow{
			Source: sourceDeviations, Key: "deviation-" + m[1], Cells: []string{m[2]},
			Anchor: anchor, Order: len(out),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no numbered deviations found under section 5.5", sourceDeviations)
	}
	return out, nil
}

var metricLine = regexp.MustCompile(`^(mockulus_[a-z0-9_]+)(\{[^}]*\})?\s*(\S*)`)

func (d *specDoc) metrics() ([]specRow, error) {
	start, anchor := -1, ""
	for i, line := range d.lines {
		if strings.HasPrefix(line, "#") && strings.Contains(line, "14.1") {
			start = i
			anchor = headingAnchor(strings.TrimSpace(strings.TrimLeft(line, "# ")))
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("%s: section 14.1 not found", sourceMetrics)
	}
	var out []specRow
	seen := map[string]bool{}
	inFence := false
	for i := start + 1; i < len(d.lines); i++ {
		line := d.lines[i]
		if strings.HasPrefix(line, "#") {
			break
		}
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		for _, part := range strings.Split(line, " / ") {
			m := metricLine.FindStringSubmatch(strings.TrimSpace(part))
			if m == nil || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			out = append(out, specRow{
				Source: sourceMetrics, Key: m[1],
				Cells: []string{strings.TrimSpace(m[2]), m[3]}, Anchor: anchor, Order: len(out),
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no collectors found under section 14.1", sourceMetrics)
	}
	return out, nil
}
