// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The behavior catalog is what makes "100% of externally observable behavior"
// falsifiable (SPEC §19.2). Every row the spec parsers find must have an entry
// here, every entry must be covered by a case once its milestone lands, and
// every case must name at least one entry.

// Behavior kinds recognised by the catalog.
var validKinds = map[string]bool{
	"functional": true, "matcher": true, "template": true, "lifecycle": true,
	"distributed": true, "degraded": true, "security": true, "observability": true,
}

// Entry statuses.
const (
	// StatusOK means the behavior is settled and a case can pin it.
	StatusOK = "ok"
	// StatusPendingDH means the behavior awaits differential verification
	// against pinned WireMock; it must reach zero by its owning milestone.
	StatusPendingDH = "pending-dh"
)

// Behavior is one catalog entry.
type Behavior struct {
	ID string `yaml:"id"`
	// SpecRow ties the entry to the row the spec parsers extract.
	SpecRow string `yaml:"spec_row"`
	// SpecHash pins the extracted behavior tuple, so a content edit forces a
	// deliberate catalog re-sync.
	SpecHash string `yaml:"spec_hash"`
	// Anchor is the SPEC.md heading this behavior is defined under.
	Anchor string `yaml:"spec"`
	Kind   string `yaml:"kind,omitempty"`
	// Milestone is the milestone that implements the behavior; entries beyond
	// the current cursor are catalogued but not yet required to be covered.
	Milestone string `yaml:"impl_milestone,omitempty"`
	Summary   string `yaml:"summary,omitempty"`
	// Evidence is the minimal behavior-specific assertion a binding case must
	// make. It is what stops a vacuous `status: 200` from claiming coverage.
	Evidence string `yaml:"evidence,omitempty"`
	// EvidenceTokens is the machine-checkable form of that contract: strings
	// that must literally appear in a binding case's assertions — the 422 code
	// for a fail-loud row, the metric name for an observability row.
	EvidenceTokens []string `yaml:"evidence_tokens,omitempty"`
	Status         string   `yaml:"status,omitempty"`
	// Exempt records why a spec row has no distinct observable behavior — a
	// pure tuning knob, say. Reviewed, not automatic.
	Exempt string `yaml:"exempt,omitempty"`
}

// ProseContract is a hand-maintained entry for a behavior stated in prose
// rather than a table. These are explicitly marked manually synced: the
// "single source of the universe" claim covers the structured blocks only
// (SPEC §19.2).
type ProseContract struct {
	ID             string   `yaml:"id"`
	Section        string   `yaml:"section"`
	SectionHash    string   `yaml:"section_hash"`
	Anchor         string   `yaml:"spec"`
	Kind           string   `yaml:"kind,omitempty"`
	Milestone      string   `yaml:"impl_milestone,omitempty"`
	Summary        string   `yaml:"summary,omitempty"`
	Evidence       string   `yaml:"evidence,omitempty"`
	EvidenceTokens []string `yaml:"evidence_tokens,omitempty"`
	Status         string   `yaml:"status,omitempty"`
}

// Catalog is the loaded catalog directory.
type Catalog struct {
	dir        string
	Behaviors  []Behavior
	Prose      []ProseContract
	byID       map[string]int
	bySpecRow  map[string]int
	proseByID  map[string]int
	Milestone  string // the CURRENT_MILESTONE cursor
	milestoneN int
}

type behaviorFile struct {
	Behaviors []Behavior `yaml:"behaviors"`
}

type proseFile struct {
	Contracts []ProseContract `yaml:"contracts"`
}

// LoadCatalog reads every catalog file plus the milestone cursor.
func LoadCatalog(dir string) (*Catalog, error) {
	c := &Catalog{
		dir:       dir,
		byID:      map[string]int{},
		bySpecRow: map[string]int{},
		proseByID: map[string]int{},
	}

	entries, globErr := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if globErr != nil {
		return nil, globErr
	}
	sort.Strings(entries)

	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if filepath.Base(path) == "prose.yaml" {
			var pf proseFile
			if err := yaml.Unmarshal(data, &pf); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			c.Prose = append(c.Prose, pf.Contracts...)
			continue
		}
		var bf behaviorFile
		if err := yaml.Unmarshal(data, &bf); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		c.Behaviors = append(c.Behaviors, bf.Behaviors...)
	}

	for i, b := range c.Behaviors {
		if _, dup := c.byID[b.ID]; dup {
			return nil, fmt.Errorf("catalog: duplicate behavior id %q", b.ID)
		}
		c.byID[b.ID] = i
		c.bySpecRow[b.SpecRow] = i
	}
	for i, p := range c.Prose {
		if _, dup := c.proseByID[p.ID]; dup {
			return nil, fmt.Errorf("catalog: duplicate prose contract id %q", p.ID)
		}
		if _, dup := c.byID[p.ID]; dup {
			return nil, fmt.Errorf("catalog: prose contract %q collides with a behavior id", p.ID)
		}
		c.proseByID[p.ID] = i
	}

	cursor, err := os.ReadFile(filepath.Join(dir, "..", "CURRENT_MILESTONE"))
	if err != nil {
		return nil, fmt.Errorf("read CURRENT_MILESTONE: %w", err)
	}
	c.Milestone = strings.TrimSpace(string(cursor))
	c.milestoneN, err = milestoneNumber(c.Milestone)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Known reports whether an id names a catalogued behavior or prose contract.
func (c *Catalog) Known(id string) bool {
	if _, ok := c.byID[id]; ok {
		return true
	}
	_, ok := c.proseByID[id]
	return ok
}

// InScope reports whether a behavior's milestone is at or below the cursor, and
// so must already be covered by a passing case.
func (c *Catalog) InScope(milestone string) bool {
	n, err := milestoneNumber(milestone)
	if err != nil {
		return false
	}
	return n <= c.milestoneN
}

var milestonePattern = regexp.MustCompile(`^M(\d+)$`)

func milestoneNumber(m string) (int, error) {
	match := milestonePattern.FindStringSubmatch(strings.TrimSpace(m))
	if match == nil {
		return 0, fmt.Errorf("milestone %q is not of the form M<number>", m)
	}
	return strconv.Atoi(match[1])
}

// Lint checks the catalog against the spec: every extracted row has an entry,
// every entry still matches its row's hash, and every anchor resolves
// (SPEC §19.2 completeness gate (d) and the spec-source lint).
func (c *Catalog) Lint(spec *specDoc) []string {
	var problems []string

	rows, err := spec.Rows()
	if err != nil {
		return []string{err.Error()}
	}

	seen := map[string]bool{}
	for _, row := range rows {
		id := row.ID()
		seen[id] = true

		idx, ok := c.bySpecRow[id]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"spec row has no catalog entry: %s\n      add an entry with spec_row: %q, spec_hash: %q (or a reviewed `exempt:`)",
				row.Key, id, row.Hash()))
			continue
		}
		b := c.Behaviors[idx]
		if b.SpecHash != row.Hash() {
			problems = append(problems, fmt.Sprintf(
				"behavior %s is out of sync with its spec row %q\n      catalog spec_hash: %s\n      spec  spec_hash: %s\n      re-read the row, update the entry, then update the hash",
				b.ID, row.Key, b.SpecHash, row.Hash()))
		}
	}

	for _, b := range c.Behaviors {
		if !seen[b.SpecRow] {
			problems = append(problems, fmt.Sprintf(
				"behavior %s references spec row %q, which no longer exists in the spec",
				b.ID, b.SpecRow))
		}
		problems = append(problems, c.lintEntry(spec, b.ID, b.Anchor, b.Kind, b.Milestone, b.Status, b.Evidence, b.EvidenceTokens, b.Exempt)...)
	}

	for _, p := range c.Prose {
		problems = append(problems, c.lintEntry(spec, p.ID, p.Anchor, p.Kind, p.Milestone, p.Status, p.Evidence, p.EvidenceTokens, "")...)
		if got := spec.SectionHash(p.Section); got == "" {
			problems = append(problems, fmt.Sprintf(
				"prose contract %s names section %q, which was not found in the spec", p.ID, p.Section))
		} else if got != p.SectionHash {
			problems = append(problems, fmt.Sprintf(
				"prose contract %s is out of sync with section %q\n      catalog section_hash: %s\n      spec  section_hash: %s\n      re-read the section and re-sync by hand — prose contracts are manually synced",
				p.ID, p.Section, p.SectionHash, got))
		}
	}

	return problems
}

func (c *Catalog) lintEntry(spec *specDoc, id, anchor, kind, milestone, status, evidence string, tokens []string, exempt string) []string {
	var problems []string
	if exempt != "" {
		return problems
	}
	if anchor == "" || !spec.HasAnchor(anchor) {
		problems = append(problems, fmt.Sprintf(
			"behavior %s has a dangling spec anchor %q", id, anchor))
	}
	if !validKinds[kind] {
		problems = append(problems, fmt.Sprintf(
			"behavior %s has kind %q, which is not one of the eight catalog kinds", id, kind))
	}
	if _, err := milestoneNumber(milestone); err != nil {
		problems = append(problems, fmt.Sprintf("behavior %s: %v", id, err))
	}
	if status != "" && status != StatusOK && status != StatusPendingDH {
		problems = append(problems, fmt.Sprintf(
			"behavior %s has status %q, want %q or %q", id, status, StatusOK, StatusPendingDH))
	}
	if evidence == "" {
		problems = append(problems, fmt.Sprintf(
			"behavior %s has no evidence contract; without one a vacuous case could claim to cover it", id))
	}
	if len(tokens) == 0 {
		problems = append(problems, fmt.Sprintf(
			"behavior %s has no evidence_tokens; the evidence contract must be machine-checkable", id))
	}
	return problems
}

// SectionHash digests the body text of a numbered spec section, which is how
// prose contracts detect that the prose they encode has changed.
func (d *specDoc) SectionHash(section string) string {
	start := -1
	for i, line := range d.lines {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		// Match the section number exactly: "## 8. Cluster synchronization"
		// must not be found by looking for "8." inside "## 18. …".
		if strings.HasPrefix(strings.TrimLeft(line, "# "), section+". ") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	depth := len(d.lines[start]) - len(strings.TrimLeft(d.lines[start], "#"))

	h := sha256.New()
	for i := start + 1; i < len(d.lines); i++ {
		line := d.lines[i]
		if strings.HasPrefix(line, "#") {
			if d := len(line) - len(strings.TrimLeft(line, "#")); d <= depth {
				break
			}
		}
		if t := normalizeCell(line); t != "" {
			_, _ = fmt.Fprintln(h, t)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Generate emits skeleton entries for every spec row that has none, so adding a
// row to the spec produces catalog work rather than silent coverage loss.
func (c *Catalog) Generate(spec *specDoc) ([]Behavior, error) {
	rows, err := spec.Rows()
	if err != nil {
		return nil, err
	}
	var out []Behavior
	for _, row := range rows {
		if _, ok := c.bySpecRow[row.ID()]; ok {
			continue
		}
		out = append(out, Behavior{
			ID:        behaviorID(row),
			SpecRow:   row.ID(),
			SpecHash:  row.Hash(),
			Anchor:    row.Anchor,
			Kind:      "", // filled in by a human
			Milestone: "",
			Summary:   normalizeCell(row.Key),
			Evidence:  "",
			Status:    StatusOK,
		})
	}
	return out, nil
}

// sourcePrefix maps a spec block to the prefix of the behavior ids it produces.
var sourcePrefix = map[string]string{
	SourceAdminEndpoints:  "B-ADMIN",
	SourceStubTopLevel:    "B-STUB",
	SourceStubRequest:     "B-REQ",
	SourceStubMatchers:    "B-MATCH",
	SourceStubResponse:    "B-RESP",
	SourceDeviations:      "B-DEV",
	SourceDegradedModes:   "B-DEGRADED",
	SourceTemplateHelpers: "B-TPL",
	SourceConfigKeys:      "B-CFG",
	SourceMetrics:         "B-METRIC",
	SourceErrorCatalog:    "B-ERR",
}

var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true, "HEAD": true,
}

var slugDrop = regexp.MustCompile(`[^A-Z0-9]+`)

// behaviorID derives a stable, readable identifier from a spec row.
func behaviorID(row SpecRow) string {
	key := normalizeCell(row.Key)
	prefix := sourcePrefix[row.Source]

	// Admin rows read better with the method last: B-ADMIN-MAPPINGS-POST.
	if row.Source == SourceAdminEndpoints {
		if head, rest, ok := strings.Cut(key, " "); ok && httpMethods[head] {
			key = rest + " " + head
		}
	}
	key = strings.ReplaceAll(key, "/__admin", "")
	key = strings.ReplaceAll(key, "mockulus_", "")

	slug := slugDrop.ReplaceAllString(strings.ToUpper(key), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "UNNAMED"
	}
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	return prefix + "-" + slug
}

// WriteGenerated appends skeleton entries to a file for a human to complete.
func WriteGenerated(path string, behaviors []Behavior) error {
	data, err := yaml.Marshal(behaviorFile{Behaviors: behaviors})
	if err != nil {
		return err
	}
	header := "# SPDX-License-Identifier: Apache-2.0\n" +
		"#\n# Generated skeleton entries. Fill in kind, impl_milestone and evidence,\n" +
		"# then move each entry into the catalog file for its area.\n\n"
	return os.WriteFile(path, append([]byte(header), data...), 0o644)
}
