// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// This file derives the universe of externally observable behaviors from
// SPEC.md itself (SPEC §19.2). Each structured block in the spec has a parser
// here; the catalog lint then requires that every row those parsers find has a
// catalog entry, and that every catalog entry still corresponds to a row.
//
// Rows are hashed over the *extracted behavior tuple* — trimmed cell text —
// rather than raw Markdown, so reflowing a table or rewording a heading does
// not trip the gate, while editing what a row actually says does.

// Source names identify which structured block a row came from. They are part
// of a behavior's identity, so they are stable API for the catalog files.
const (
	SourceAdminEndpoints  = "spec:5.1:admin-endpoints"
	SourceStubTopLevel    = "spec:5.2:stub-top-level"
	SourceStubRequest     = "spec:5.2:stub-request"
	SourceStubMatchers    = "spec:5.2:content-matchers"
	SourceStubResponse    = "spec:5.2:stub-response"
	SourceDeviations      = "spec:5.5:deviations"
	SourceDegradedModes   = "spec:4.6:degraded-modes"
	SourceTemplateHelpers = "spec:10.3:template-helpers"
	SourceConfigKeys      = "spec:13:config"
	SourceMetrics         = "spec:14.1:metrics"
	SourceErrorCatalog    = "spec:B:error-catalog"
)

// SpecRow is one behavior-bearing row extracted from the spec.
type SpecRow struct {
	// Source names the structured block this row came from.
	Source string
	// Key is the row's identity within its block — an endpoint, a field name,
	// a config key, an error code.
	Key string
	// Cells is the rest of the row's meaning, used for the hash.
	Cells []string
	// Anchor is the SPEC.md heading anchor the row lives under.
	Anchor string
}

// Hash is the row's spec_hash: a digest of the extracted tuple, insensitive to
// Markdown formatting but sensitive to content.
func (r SpecRow) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", r.Source, normalizeCell(r.Key))
	for _, c := range r.Cells {
		fmt.Fprintf(h, "%s\n", normalizeCell(c))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ID is the deterministic identifier the catalog keys on.
func (r SpecRow) ID() string { return r.Source + "|" + normalizeCell(r.Key) }

var whitespaceRun = regexp.MustCompile(`\s+`)

// normalizeCell reduces a table cell to comparable text: Markdown emphasis and
// code fences dropped, whitespace collapsed.
func normalizeCell(s string) string {
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, `\|`, "|")
	s = whitespaceRun.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// specDoc is a parsed SPEC.md.
type specDoc struct {
	path    string
	lines   []string
	tables  []specTable
	anchors map[string]bool
}

// specTable is one Markdown table together with the context that identifies it.
type specTable struct {
	heading  string
	anchor   string
	preamble string
	header   []string
	rows     [][]string
}

func loadSpec(path string) (*specDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	doc := &specDoc{
		path:    path,
		lines:   strings.Split(string(data), "\n"),
		anchors: map[string]bool{},
	}
	doc.parse()
	return doc, nil
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
			d.anchors[anchor] = true
			preamble = ""
			continue
		}

		if isTableRow(line) && i+1 < len(d.lines) && isTableDivider(d.lines[i+1]) {
			table := specTable{
				heading:  heading,
				anchor:   anchor,
				preamble: preamble,
				header:   splitRow(line),
			}
			i += 2
			for i < len(d.lines) && isTableRow(d.lines[i]) {
				table.rows = append(table.rows, splitRow(d.lines[i]))
				i++
			}
			i--
			d.tables = append(d.tables, table)
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

// splitRow splits a Markdown table row into cells, honouring the `\|` escape.
func splitRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")

	var cells []string
	var cur strings.Builder
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

// headingAnchor reproduces GitHub's heading-to-anchor slug, which is what the
// catalog's `spec` field points at.
var anchorDrop = regexp.MustCompile(`[^a-z0-9\- ]`)

func headingAnchor(heading string) string {
	s := strings.ToLower(heading)
	s = strings.ReplaceAll(s, "`", "")
	s = anchorDrop.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, " ", "-")
	return "#" + s
}

// findTable returns the first table whose heading starts with headingPrefix and
// whose preamble contains preambleContains (empty matches any).
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

// Rows extracts every behavior-bearing row from the spec. A missing block is an
// error, not an empty result: the gate must fail loudly if the spec is
// restructured out from under it.
func (d *specDoc) Rows() ([]SpecRow, error) {
	var out []SpecRow
	var missing []string

	table := func(source, headingPrefix, preamble string, keyCol int) {
		t := d.findTable(headingPrefix, preamble)
		if t == nil {
			missing = append(missing, fmt.Sprintf("%s (heading %q, preamble %q)",
				source, headingPrefix, preamble))
			return
		}
		for _, row := range t.rows {
			if len(row) <= keyCol {
				continue
			}
			out = append(out, SpecRow{
				Source: source,
				Key:    row[keyCol],
				Cells:  append([]string{}, row[keyCol+1:]...),
				Anchor: t.anchor,
			})
		}
	}

	table(SourceAdminEndpoints, "5.1", "", 0)
	table(SourceStubTopLevel, "5.2", "Top level:", 0)
	table(SourceStubRequest, "5.2", "`request` object:", 0)
	table(SourceStubMatchers, "5.2", "Content matchers", 0)
	table(SourceStubResponse, "5.2", "`response` object:", 0)
	table(SourceDegradedModes, "4.6", "", 0)
	table(SourceTemplateHelpers, "10.3", "", 0)
	table(SourceConfigKeys, "13", "", 0)
	table(SourceErrorCatalog, "Appendix B", "", 0)

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

// deviations parses the numbered list of SPEC §5.5. Each deviation is a
// behavior: it is what mockulus does *instead of* what WireMock does, so it
// needs a case proving the deviation is real and bounded.
func (d *specDoc) deviations() ([]SpecRow, error) {
	start, anchor := -1, ""
	for i, line := range d.lines {
		if strings.HasPrefix(line, "#") && strings.Contains(line, "5.5") {
			start = i
			anchor = headingAnchor(strings.TrimSpace(strings.TrimLeft(line, "# ")))
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("%s: section 5.5 not found", SourceDeviations)
	}

	var out []SpecRow
	for i := start + 1; i < len(d.lines); i++ {
		line := d.lines[i]
		if strings.HasPrefix(line, "#") {
			break
		}
		m := deviationItem.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		out = append(out, SpecRow{
			Source: SourceDeviations,
			Key:    "deviation-" + m[1],
			Cells:  []string{m[2]},
			Anchor: anchor,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no numbered deviations found under section 5.5", SourceDeviations)
	}
	return out, nil
}

// metricLine matches a collector declaration in the SPEC §14.1 block. The type
// column is optional: the block lists several collectors per line where they
// share a type, and those must not be silently skipped.
var metricLine = regexp.MustCompile(`^(mockulus_[a-z0-9_]+)(\{[^}]*\})?\s*(\S*)`)

// metrics parses the collector block of SPEC §14.1. Every metric named there is
// a behavior: something must be observable on /metrics.
func (d *specDoc) metrics() ([]SpecRow, error) {
	start, anchor := -1, ""
	for i, line := range d.lines {
		if strings.HasPrefix(line, "#") && strings.Contains(line, "14.1") {
			start = i
			anchor = headingAnchor(strings.TrimSpace(strings.TrimLeft(line, "# ")))
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("%s: section 14.1 not found", SourceMetrics)
	}

	var out []SpecRow
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
		// A single spec line may list several collectors separated by " / ".
		for _, part := range strings.Split(line, " / ") {
			m := metricLine.FindStringSubmatch(strings.TrimSpace(part))
			if m == nil {
				continue
			}
			if seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			out = append(out, SpecRow{
				Source: SourceMetrics,
				Key:    m[1],
				Cells:  []string{strings.TrimSpace(m[2]), m[3]},
				Anchor: anchor,
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no collectors found under section 14.1", SourceMetrics)
	}
	return out, nil
}

// HasAnchor reports whether a heading anchor exists in the spec, backing
// completeness gate (d): every catalog anchor must resolve.
func (d *specDoc) HasAnchor(anchor string) bool { return d.anchors[anchor] }
