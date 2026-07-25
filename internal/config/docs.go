// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// tableHeader is the first line of the generated SPEC §13 table; it doubles as
// the anchor used to locate the table when rewriting the document.
const tableHeader = "| Key (yaml) | Default | Description |"

// specSection is the heading the generated table lives under.
const specSection = "## 13. Configuration reference"

// Table renders the SPEC §13 configuration reference table from the struct
// tags in this package. Keys sharing a `docrow` tag collapse into one row, in
// declaration order.
func Table() string {
	var sb strings.Builder
	sb.WriteString(tableHeader + "\n|---|---|---|\n")

	type row struct {
		keys []string
		defs []string
		doc  string
	}
	var rows []row
	index := map[string]int{}

	for _, f := range fields() {
		group := f.DocRow
		if group == "" {
			group = f.Path
		}
		def := f.Def
		if def != "" {
			def = "`" + def + "`"
		}
		if i, ok := index[group]; ok {
			rows[i].defs = append(rows[i].defs, def)
			if rows[i].doc == "" {
				rows[i].doc = f.Doc
			}
			continue
		}
		index[group] = len(rows)
		keys := strings.Split(group, "|")
		for i := range keys {
			keys[i] = "`" + keys[i] + "`"
		}
		rows = append(rows, row{keys: keys, defs: []string{def}, doc: f.Doc})
	}

	for _, r := range rows {
		defs := strings.Join(r.defs, " / ")
		if strings.Trim(defs, " /") == "" {
			defs = "—"
		}
		line := "| " + strings.Join(r.keys, " / ") + " | " + defs + " |"
		if doc := renderDoc(r.doc); doc != "" {
			line += " " + doc + " |"
		} else {
			line += " |"
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// docReplacer expands the stand-in characters a struct tag has to use for
// Markdown it cannot contain literally (see the package comment).
var docReplacer = strings.NewReplacer("~", "`", "¦", `\|`)

// renderDoc turns a `doc` struct tag into the Markdown of a table cell.
func renderDoc(s string) string { return docReplacer.Replace(s) }

// errTableNotFound reports that the generated block is missing from the spec.
var errTableNotFound = errors.New("configuration table not found")

// UpdateSpec rewrites the §13 table in the given SPEC.md so the document can
// never drift from the code. It reports whether the file changed.
func UpdateSpec(path string) (changed bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	updated, err := replaceTable(string(data), Table())
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if updated == string(data) {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(updated), 0o644)
}

// CheckSpec reports whether the §13 table in the given SPEC.md matches what the
// code would generate. Used by the CI drift gate.
func CheckSpec(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, err := replaceTable(string(data), Table())
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if updated != string(data) {
		return fmt.Errorf("%s: §13 configuration table is out of date; run `make config-docs`", path)
	}
	return nil
}

func replaceTable(doc, table string) (string, error) {
	sectionAt := strings.Index(doc, specSection)
	if sectionAt < 0 {
		return "", fmt.Errorf("%w: section %q missing", errTableNotFound, specSection)
	}
	headerAt := strings.Index(doc[sectionAt:], tableHeader)
	if headerAt < 0 {
		return "", fmt.Errorf("%w: header row missing under %q", errTableNotFound, specSection)
	}
	start := sectionAt + headerAt

	// The table ends at the first line that is not part of it.
	end := start
	for end < len(doc) {
		lineEnd := strings.IndexByte(doc[end:], '\n')
		if lineEnd < 0 {
			end = len(doc)
			break
		}
		line := doc[end : end+lineEnd]
		if !strings.HasPrefix(line, "|") {
			break
		}
		end += lineEnd + 1
	}
	return doc[:start] + table + doc[end:], nil
}
