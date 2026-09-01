// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// Real benchstat output, from the comparison that found the v1.3.0 request-bind
// regression. It is kept verbatim rather than hand-written because the shape is
// the whole difficulty: the first percentage on a data row is the base
// confidence interval, and a reader that takes it reports every row as a
// regression while missing what actually moved.
const sample = `goos: darwin
goarch: arm64
pkg: github.com/b3vet/mockulus/internal/match
cpu: Apple M4
,base,,head,,,
,sec/op,CI,sec/op,CI,vs base,P
Match/exact/1-10,5.5389999999999995e-08,4%,6.425500000000002e-08,1%,+16.00%,p=0.000 n=10
Match/exact/1000-10,6.1595e-08,9%,7.027500000000001e-08,5%,+14.09%,p=0.003 n=10
AcquireRelease-10,1.818e-08,2%,2.677e-08,16%,+47.32%,p=0.000 n=10
HeaderLookup-10,2.82e-08,1%,2.804e-08,1%,,p=0.900 n=10
,base,,head,,,
,B/op,CI,B/op,CI,vs base,P
SomethingAllocating-10,9,11%,10,10%,+11.11%,p=0.015 n=10
`

func TestScanFindsOnlyRealTimeRegressions(t *testing.T) {
	got, err := Scan(strings.NewReader(sample), Threshold)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// +16.00% and +47.32% clear 15%; +14.09% does not; the blank row is not a
	// regression however much slower it looks.
	want := map[string]float64{"Match/exact/1-10": 16.00, "AcquireRelease-10": 47.32}
	if len(got) != len(want) {
		t.Fatalf("found %d regressions %v, want %d", len(got), got, len(want))
	}
	for _, r := range got {
		if w, ok := want[r.Name]; !ok {
			t.Errorf("reported %s, which is not over the threshold", r.Name)
		} else if r.Percent != w {
			t.Errorf("%s: read %.2f%%, want %.2f%%", r.Name, r.Percent, w)
		}
	}
}

// The B/op table in the sample carries a +11.11% row. It must be ignored even
// though it is a real number, because allocation ceilings are asserted by
// `make test-alloc` and two gates over one number disagree eventually.
func TestScanIgnoresNonTimeMetrics(t *testing.T) {
	for _, r := range mustScan(t, sample) {
		if r.Name == "SomethingAllocating-10" {
			t.Error("judged a B/op row; only sec/op is in scope")
		}
	}
}

// The reader must not mistake the base confidence interval for the comparison.
// Every row here has a large CI and no significant change, so nothing is a
// regression; a reader taking the first percentage would report all of them.
func TestScanDoesNotReadTheConfidenceInterval(t *testing.T) {
	noisy := `,base,,head,,,
,sec/op,CI,sec/op,CI,vs base,P
A-10,1e-08,53%,1e-08,56%,,p=0.900 n=8
B-10,2e-08,44%,2e-08,41%,,p=0.700 n=8
`
	if got := mustScan(t, noisy); len(got) != 0 {
		t.Errorf("reported %v from rows with no significant change", got)
	}
}

// An improvement is not a regression, however large.
func TestScanIgnoresImprovements(t *testing.T) {
	better := `,base,,head,,,
,sec/op,CI,sec/op,CI,vs base,P
A-10,1e-07,2%,5e-08,1%,-50.00%,p=0.000 n=8
`
	if got := mustScan(t, better); len(got) != 0 {
		t.Errorf("reported %v for a 50%% improvement", got)
	}
}

// The geomean row rolls up the table above it. Failing on it would report one
// regression twice and name a row nobody can open.
func TestScanIgnoresGeomean(t *testing.T) {
	// A real row sits beside it, because a report of nothing but a geomean is an
	// empty comparison and is refused separately.
	rolled := `,base,,head,,,
,sec/op,CI,sec/op,CI,vs base,P
A-10,1e-08,1%,1e-08,1%,,p=0.900 n=8
geomean,2.9e-08,,3.5e-08,,+18.73%,
`
	if got := mustScan(t, rolled); len(got) != 0 {
		t.Errorf("reported %v for a geomean summary row", got)
	}
}

// A report with no sec/op rows is an error, not a pass. This is the shape a
// gate takes when it silently stops testing anything.
func TestScanRefusesAnEmptyComparison(t *testing.T) {
	for _, name := range []string{"nothing at all", "headers only", "wrong metric only"} {
		var in string
		switch name {
		case "nothing at all":
			in = ""
		case "headers only":
			in = ",base,,head,,,\n,sec/op,CI,sec/op,CI,vs base,P\n"
		case "wrong metric only":
			in = ",base,,head,,,\n,B/op,CI,B/op,CI,vs base,P\nA-10,9,1%,10,1%,+11.11%,p=0.01 n=8\n"
		}
		if _, err := Scan(strings.NewReader(in), Threshold); err == nil {
			t.Errorf("%s: expected an error, got a pass", name)
		}
	}
}

func mustScan(t *testing.T, in string) []Regression {
	t.Helper()
	got, err := Scan(strings.NewReader(in), Threshold)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return got
}
