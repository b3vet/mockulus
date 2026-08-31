// SPDX-License-Identifier: Apache-2.0

// Command deviationdocs checks that docs/deviations.md accounts for every
// deviation numbered in SPEC §5.5, and that the count in its opening sentence
// is the real one.
//
//	go run ./scripts/deviationdocs
//
// The maintenance note at the foot of that page promises a new deviation
// "arrives as a numbered entry in SPEC §5.5, a corpus case that pins it, and a
// section here — or it does not arrive". The first two clauses are enforced: the
// E2E gate fails when a §5.5 row has no catalog entry, and again when a catalog
// behavior has no passing case. The third was enforced by nobody, and the page
// drifted exactly the way an unenforced promise does — its opening sentence said
// 57 while the list behind it had grown to 61, because the number counted
// headings and several headings had begun covering two deviations at once.
//
// The gap matters more than the arithmetic. A reader planning a migration counts
// the deviations to size the work, and a page that undercounts them by four is
// worse than a page with no number at all, because the number reads as measured.
//
// The check is deliberately shallow: presence of each number and agreement of
// the total. It does not judge whether a section describes its deviation well —
// that is review's job, and a gate that pretended to do it would be the same
// unearned assurance in a new place.
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	specPath = "SPEC.md"
	docPath  = "docs/deviations.md"
)

var (
	specItem    = regexp.MustCompile(`(?m)^(\d+)\.\s`)
	docHeading  = regexp.MustCompile(`(?m)^### (.+)$`)
	headingNums = regexp.MustCompile(`#(\d+)`)
	countPhrase = regexp.MustCompile(`differently from WireMock in (\d+) places`)
)

func main() {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		fail("read %s: %v", specPath, err)
	}
	doc, err := os.ReadFile(docPath)
	if err != nil {
		fail("read %s: %v", docPath, err)
	}

	declared := specDeviations(string(spec))
	if len(declared) == 0 {
		fail("found no deviations in %s §5.5; the section's shape must have changed", specPath)
	}
	documented := docDeviations(string(doc))

	var problems []string
	for _, n := range declared {
		if !documented[n] {
			problems = append(problems, fmt.Sprintf(
				"deviation #%d is numbered in SPEC §5.5 but no section of %s mentions it; "+
					"add one, or fold it into an existing heading as `### #%d, #n — …`", n, docPath, n))
		}
	}
	for n := range documented {
		if !contains(declared, n) {
			problems = append(problems, fmt.Sprintf(
				"%s documents deviation #%d, which SPEC §5.5 does not number", docPath, n))
		}
	}

	if m := countPhrase.FindSubmatch(doc); m == nil {
		problems = append(problems, fmt.Sprintf(
			"%s no longer opens with a %q sentence; this gate reads that number, so "+
				"either restore it or retire the gate deliberately", docPath, "differently from WireMock in N places"))
	} else if stated, _ := strconv.Atoi(string(m[1])); stated != len(declared) {
		problems = append(problems, fmt.Sprintf(
			"%s says mockulus differs in %d places; SPEC §5.5 numbers %d. "+
				"The number counts deviations, not headings — several headings cover two",
			docPath, stated, len(declared)))
	}

	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "deviation docs are out of sync (%d):\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		os.Exit(1)
	}
	fmt.Printf("deviation docs: %d deviations in SPEC §5.5, all documented in %s\n", len(declared), docPath)
}

// specDeviations reads the numbered list of §5.5, bounded by the next section
// heading so that a numbered list elsewhere in the spec cannot leak in.
func specDeviations(spec string) []int {
	start := strings.Index(spec, "### 5.5")
	if start < 0 {
		return nil
	}
	end := strings.Index(spec[start:], "### 5.6")
	if end < 0 {
		end = len(spec) - start
	}
	var out []int
	for _, m := range specItem.FindAllStringSubmatch(spec[start:start+end], -1) {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// docDeviations collects the numbers named in section headings. Headings are the
// unit rather than the whole body because a number mentioned in passing inside
// some other entry's prose is a cross-reference, not coverage.
func docDeviations(doc string) map[int]bool {
	out := map[int]bool{}
	for _, h := range docHeading.FindAllStringSubmatch(doc, -1) {
		for _, num := range headingNums.FindAllStringSubmatch(h[1], -1) {
			if n, err := strconv.Atoi(num[1]); err == nil {
				out[n] = true
			}
		}
	}
	return out
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "deviationdocs: "+format+"\n", args...)
	os.Exit(1)
}
