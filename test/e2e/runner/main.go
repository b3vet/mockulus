// SPDX-License-Identifier: Apache-2.0

// Command runner executes the mockulus E2E regression gate (SPEC §19).
//
// It is the authoritative gate: a black-box suite run against a real, started
// mockulus, plus the completeness gates that make the "100% of externally
// observable behavior" claim falsifiable rather than aspirational. No artifact
// ships without it.
//
//	runner --corpus test/e2e/corpus --catalog test/e2e/catalog
//	runner --catalog test/e2e/catalog --check-only     # gates without execution
//	runner --catalog test/e2e/catalog --generate out.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: "+err.Error())
		os.Exit(1)
	}
}

type options struct {
	corpus    string
	catalog   string
	spec      string
	binary    string
	artifacts string
	generate  string
	checkOnly bool
	parallel  int
	filter    string
	// differential turns on topology T5: `wm: verified` cases are replayed
	// against pinned WireMock and the answers diffed (SPEC §5.6).
	differential bool
	wiremockFile string
}

func run() error {
	var opt options
	flag.StringVar(&opt.corpus, "corpus", "test/e2e/corpus", "directory of corpus cases")
	flag.StringVar(&opt.catalog, "catalog", "test/e2e/catalog", "directory of behavior catalog files")
	flag.StringVar(&opt.spec, "spec", "SPEC.md", "path to the specification")
	flag.StringVar(&opt.binary, "binary", "", "path to a built mockulus binary (built on demand when empty)")
	flag.StringVar(&opt.artifacts, "artifacts", "test/e2e/.artifacts", "directory for run artifacts")
	flag.StringVar(&opt.generate, "generate", "", "write skeleton catalog entries for uncatalogued spec rows to this file")
	flag.BoolVar(&opt.checkOnly, "check-only", false, "run the catalog and static gates without executing cases")
	flag.IntVar(&opt.parallel, "parallel", 8, "how many cases to run concurrently")
	flag.StringVar(&opt.filter, "run", "", "only run cases whose id contains this substring")
	flag.BoolVar(&opt.differential, "differential", false,
		"also replay wm:verified cases against pinned WireMock and diff (topology T5)")
	flag.StringVar(&opt.wiremockFile, "wiremock-version", "test/e2e/WIREMOCK_VERSION",
		"file naming the pinned WireMock image")
	flag.Parse()

	spec, err := loadSpec(opt.spec)
	if err != nil {
		return err
	}
	catalog, err := LoadCatalog(opt.catalog)
	if err != nil {
		return err
	}

	if opt.generate != "" {
		skeletons, err := catalog.Generate(spec)
		if err != nil {
			return err
		}
		if len(skeletons) == 0 {
			fmt.Println("catalog is complete: every spec row has an entry")
			return nil
		}
		if err := WriteGenerated(opt.generate, skeletons); err != nil {
			return err
		}
		fmt.Printf("wrote %d skeleton entries to %s\n", len(skeletons), opt.generate)
		return nil
	}

	var failures []string

	if problems := catalog.Lint(spec); len(problems) > 0 {
		failures = append(failures, section("catalog is out of sync with the spec", problems))
	}

	cases, err := LoadCorpus(opt.corpus)
	if err != nil {
		return err
	}
	if opt.filter != "" {
		cases = filterCases(cases, opt.filter)
	}

	if problems := staticGates(catalog, cases); len(problems) > 0 {
		failures = append(failures, section("static completeness gates failed", problems))
	}

	var results []*Result
	if !opt.checkOnly {
		if len(failures) > 0 {
			// Executing against a broken catalog produces misleading coverage.
			return report(failures)
		}
		binary, err := resolveBinary(opt.binary)
		if err != nil {
			return err
		}
		results, err = execute(opt, cases, binary)
		if err != nil {
			return err
		}
		if problems := coverageGates(catalog, results); len(problems) > 0 {
			failures = append(failures, section("behavior coverage gates failed", problems))
		}
		if problems := failedCases(results); len(problems) > 0 {
			failures = append(failures, section("cases failed", problems))
		}
		if err := writeArtifacts(opt.artifacts, catalog, results); err != nil {
			return err
		}
	}

	summarize(catalog, cases, results, opt.checkOnly)

	if len(failures) > 0 {
		return report(failures)
	}
	return nil
}

func report(failures []string) error {
	return fmt.Errorf("gate failed\n\n%s", strings.Join(failures, "\n"))
}

func section(title string, problems []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%d):\n", title, len(problems))
	for _, p := range problems {
		fmt.Fprintf(&sb, "  - %s\n", p)
	}
	return sb.String()
}

func filterCases(cases []*Case, substr string) []*Case {
	out := cases[:0]
	for _, c := range cases {
		if strings.Contains(c.ID, substr) {
			out = append(out, c)
		}
	}
	return out
}

// staticGates are the completeness checks that need no execution: every case
// names known behaviors, no skip has expired, and no behavior at or below the
// milestone cursor is still awaiting differential verification.
func staticGates(catalog *Catalog, cases []*Case) []string {
	var problems []string
	now := time.Now()

	for _, c := range cases {
		// Gate (b): no orphan tests.
		for _, id := range c.Behaviors {
			if !catalog.Known(id) {
				problems = append(problems, fmt.Sprintf(
					"%s: case %s references unknown behavior %q", c.Path, c.ID, id))
			}
		}
		if c.SkipExpired(now) {
			problems = append(problems, fmt.Sprintf(
				"%s: case %s is skipped past its expiry of %s (issue %s) — an expired skip fails the gate",
				c.Path, c.ID, c.Skip.Expires, c.Skip.Issue))
		}
	}

	// Gate (c): pending-dh must reach zero by its owning milestone.
	for _, b := range catalog.Behaviors {
		if b.Status == StatusPendingDH && catalog.InScope(b.Milestone) {
			problems = append(problems, fmt.Sprintf(
				"behavior %s is still pending-dh but its milestone %s has landed; resolve it against pinned WireMock and fold the answer into the spec",
				b.ID, b.Milestone))
		}
	}
	for _, p := range catalog.Prose {
		if p.Status == StatusPendingDH && catalog.InScope(p.Milestone) {
			problems = append(problems, fmt.Sprintf(
				"prose contract %s is still pending-dh but its milestone %s has landed", p.ID, p.Milestone))
		}
	}
	return problems
}

// coverageGates is gate (a): every in-scope behavior must be referenced by at
// least one passing case whose assertions satisfy its evidence contract.
func coverageGates(catalog *Catalog, results []*Result) []string {
	covered := map[string][]*Result{}
	for _, r := range results {
		if !r.Passed {
			continue
		}
		for _, id := range r.Case.Behaviors {
			covered[id] = append(covered[id], r)
		}
	}

	var problems []string
	check := func(id, milestone string, tokens []string, exempt string) {
		if exempt != "" || !catalog.InScope(milestone) {
			return
		}
		binding := covered[id]
		if len(binding) == 0 {
			problems = append(problems, fmt.Sprintf(
				"behavior %s (milestone %s) has no passing case; its milestone has landed, so a case is required",
				id, milestone))
			return
		}
		for _, r := range binding {
			if satisfiesEvidence(r.Case, tokens) {
				return
			}
		}
		problems = append(problems, fmt.Sprintf(
			"behavior %s is referenced by %d passing case(s), none of which assert its evidence contract %v",
			id, len(binding), tokens))
	}

	for _, b := range catalog.Behaviors {
		check(b.ID, b.Milestone, b.EvidenceTokens, b.Exempt)
	}
	for _, p := range catalog.Prose {
		check(p.ID, p.Milestone, p.EvidenceTokens, "")
	}
	return problems
}

// satisfiesEvidence reports whether a case's steps literally contain every
// evidence token. This is the anti-vacuity backstop: a case asserting only
// `status: 200` cannot claim to cover an error code or a metric.
func satisfiesEvidence(c *Case, tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	text := renderCaseAssertions(c)
	for _, token := range tokens {
		if !strings.Contains(text, token) {
			return false
		}
	}
	return true
}

func failedCases(results []*Result) []string {
	var problems []string
	for _, r := range results {
		if r.Passed || r.Skipped {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s [%s/%s]: %s",
			r.Case.ID, r.Topology, r.Variant, r.Failure))
	}
	return problems
}

// resolveBinary builds mockulus unless a path was supplied.
func resolveBinary(path string) (string, error) {
	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("mockulus binary %s: %w", abs, err)
		}
		return abs, nil
	}

	out := filepath.Join(os.TempDir(), fmt.Sprintf("mockulus-e2e-%d", os.Getpid()))
	cmd := exec.Command("go", "build", "-o", out, "./cmd/mockulus")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build mockulus for the gate: %w", err)
	}
	return out, nil
}

// execute runs the corpus. Cases share instances per (topology, variant) and
// stay isolated through their URL namespace; cases needing pristine global
// state declare `requires: [exclusive]` and run serially (SPEC §19.3).
// log prints a progress line during a run.
func log(msg string) { fmt.Println(msg) }

func execute(opt options, cases []*Case, binary string) ([]*Result, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool := NewPool(binary)
	defer pool.Close()

	exec := &Executor{corpusDir: opt.corpus}

	if opt.differential {
		oracle, err := StartWireMock(ctx, opt.wiremockFile)
		if err != nil {
			return nil, fmt.Errorf("start the WireMock oracle: %w", err)
		}
		defer func() { _ = oracle.Stop() }()
		exec.oracle = oracle
		log("differential run against " + oracle.Version)
	}

	var concurrent, exclusive []*Case
	for _, c := range cases {
		// A differential case resets the shared oracle, so it cannot run
		// alongside another differential case.
		if c.RequiresCapability("exclusive") || (exec.oracle != nil && c.WM == WMVerified) {
			exclusive = append(exclusive, c)
			continue
		}
		concurrent = append(concurrent, c)
	}

	results := make([]*Result, 0, len(cases))
	var mu sync.Mutex
	record := func(r *Result) {
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	}

	runOne := func(c *Case) {
		if c.Skip != nil {
			record(&Result{Case: c, Skipped: true, Topology: TopologyT1, Variant: c.Variant()})
			return
		}
		inst, err := pool.Get(ctx, TopologyT1, c.Variant())
		if err != nil {
			record(&Result{Case: c, Topology: TopologyT1, Variant: c.Variant(),
				Failure: "could not start instance: " + err.Error()})
			return
		}
		record(exec.Run(ctx, c, inst))
	}

	sem := make(chan struct{}, max(1, opt.parallel))
	var wg sync.WaitGroup
	for _, c := range concurrent {
		wg.Add(1)
		go func(c *Case) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			runOne(c)
		}(c)
	}
	wg.Wait()

	for _, c := range exclusive {
		runOne(c)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Case.ID < results[j].Case.ID })
	return results, nil
}

func summarize(catalog *Catalog, cases []*Case, results []*Result, checkOnly bool) {
	inScope, exempt := 0, 0
	for _, b := range catalog.Behaviors {
		if b.Exempt != "" {
			exempt++
			continue
		}
		if catalog.InScope(b.Milestone) {
			inScope++
		}
	}

	fmt.Printf("milestone cursor: %s\n", catalog.Milestone)
	fmt.Printf("catalog:          %d behaviors (%d in scope, %d exempt), %d prose contracts\n",
		len(catalog.Behaviors), inScope, exempt, len(catalog.Prose))
	fmt.Printf("corpus:           %d cases\n", len(cases))

	if checkOnly {
		fmt.Println("mode:             check-only (no cases executed)")
		return
	}

	passed, failed, skipped := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Skipped:
			skipped++
		case r.Passed:
			passed++
		default:
			failed++
		}
	}
	fmt.Printf("results:          %d passed, %d failed, %d skipped\n", passed, failed, skipped)
}
