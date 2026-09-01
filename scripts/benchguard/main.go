// SPDX-License-Identifier: Apache-2.0

// Command benchguard fails a build whose microbenchmarks regressed further than
// SPEC §16.2 allows.
//
//	benchstat -format csv base=base.txt head=head.txt | go run ./scripts/benchguard
//
// §16.2 has asked for benchmarks "tracked with `benchstat` in CI (fail > 15%
// regression)" since v1.0 and nothing did it, which is how v1.3.0 shipped a
// change that made acquiring a request — before any matching happens at all —
// measurably slower for every request, in service of three criteria almost no
// stub carries. The threshold here is that sentence.
//
// Only the `sec/op` tables are judged. Allocation counts have a gate of their
// own in `make test-alloc`, which asserts fixed ceilings rather than a delta,
// and two gates over one number disagree eventually.
//
// A shared runner cannot produce an absolute number, which is why this reads a
// comparison rather than a measurement: benchstat reports a change only when it
// clears its own significance test, and a blank means the runs did not separate.
// A blank is not a regression.
package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Threshold is the regression SPEC §16.2 permits, in percent.
const Threshold = 15.0

// Regression is one benchmark that moved further than the threshold.
type Regression struct {
	Name    string
	Percent float64
}

func main() {
	regressions, err := Scan(os.Stdin, Threshold)
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchguard:", err)
		os.Exit(2)
	}
	if len(regressions) == 0 {
		fmt.Printf("no benchmark regressed more than %.0f%% (SPEC §16.2)\n", Threshold)
		return
	}
	fmt.Fprintf(os.Stderr, "%d benchmark(s) regressed more than %.0f%% (SPEC §16.2):\n",
		len(regressions), Threshold)
	for _, r := range regressions {
		fmt.Fprintf(os.Stderr, "  %-40s %+.2f%%\n", r.Name, r.Percent)
	}
	os.Exit(1)
}

// Scan reads benchstat's CSV and returns the sec/op rows that regressed past
// the threshold.
//
// The format interleaves header rows with data rows and repeats the whole shape
// once per metric, so the column holding the comparison has to be read from the
// header rather than assumed: the first percentage on a data row is the *base*
// confidence interval, not the change, and a reader that takes it finds a
// regression in every row and a real one in none.
func Scan(r io.Reader, threshold float64) ([]Regression, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1

	var (
		out       []Regression
		column    = -1
		measuring bool
		compared  int
	)
	for {
		row, err := reader.Read()
		if err == io.EOF {
			// A comparison of nothing is not a pass. An empty report means the
			// two runs did not produce comparable benchmarks — a base branch
			// whose benchmarks are all newly added, a filter that matched
			// nothing — and a gate that stayed quiet through that would be
			// green for every release after the one that broke it.
			if compared == 0 {
				return nil, errors.New("benchstat reported no sec/op comparisons; " +
					"the two runs produced nothing to compare")
			}
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if len(row) == 0 {
			continue
		}

		// A header row names the metric and the columns; it has an empty first
		// cell where a data row carries the benchmark's name.
		if strings.TrimSpace(row[0]) == "" {
			metric, at := readHeader(row)
			if metric != "" {
				measuring, column = metric == "sec/op", at
			}
			continue
		}
		if !measuring || column < 0 || column >= len(row) {
			continue
		}
		// benchstat closes each table with a geomean summary. It is a roll-up of
		// the rows above rather than a benchmark, so failing on it reports the
		// same regression twice and names nothing a reader can go and look at.
		if strings.TrimSpace(row[0]) == "geomean" {
			continue
		}
		compared++
		percent, ok := parsePercent(row[column])
		if ok && percent > threshold {
			out = append(out, Regression{Name: row[0], Percent: percent})
		}
	}
}

// readHeader returns the metric a header row describes and the index of its
// "vs base" column, or "" when the row names no metric.
func readHeader(row []string) (metric string, column int) {
	column = -1
	for i, cell := range row {
		switch strings.TrimSpace(cell) {
		case "sec/op", "B/op", "allocs/op":
			if metric == "" {
				metric = strings.TrimSpace(cell)
			}
		case "vs base":
			column = i
		}
	}
	return metric, column
}

// parsePercent reads a cell like "+16.00%". An empty cell means benchstat found
// no significant difference, and "~" means the same thing in its text output.
func parsePercent(cell string) (float64, bool) {
	cell = strings.TrimSpace(cell)
	if cell == "" || cell == "~" || !strings.HasSuffix(cell, "%") {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSuffix(cell, "%"), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
