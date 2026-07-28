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
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	differential  bool
	wiremockFile  string
	couchbaseFile string
	// gotests is the directory of Go-native cases, which cover behavior that is
	// not observable through an HTTP client.
	gotests string
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
	flag.StringVar(&opt.couchbaseFile, "couchbase-version", "test/e2e/topologies/COUCHBASE_VERSION",
		"file naming the pinned Couchbase image topologies T2 and T3 run against")
	flag.StringVar(&opt.gotests, "gotests", "test/e2e/gotests",
		"directory of Go-native cases for behavior a corpus case cannot express")
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
		return generateSkeletons(catalog, spec, opt.generate)
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

	gotests, err := LoadGoTests(opt.gotests)
	if err != nil {
		return err
	}

	if problems := staticGates(catalog, cases); len(problems) > 0 {
		failures = append(failures, section("static completeness gates failed", problems))
	}
	if problems := goTestGates(catalog, gotests); len(problems) > 0 {
		failures = append(failures, section("go-native case gates failed", problems))
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
		// The Go-native results join the corpus results before any gate runs, so
		// coverage, failure reporting and the artifact matrix see one suite.
		if opt.filter == "" {
			results = append(results, RunGoTests(opt.gotests, binary, gotests)...)
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

	summarize(catalog, cases, gotests, results, opt.checkOnly)

	if len(failures) > 0 {
		return report(failures)
	}
	return nil
}

func report(failures []string) error {
	return fmt.Errorf("gate failed\n\n%s", strings.Join(failures, "\n"))
}

// generateSkeletons writes a catalog entry for every spec row that has none.
//
// It is the whole of the --generate mode: the run stops here rather than going
// on to execute, because the entries it just wrote are unfilled and a gate run
// against them would report coverage nobody has written yet.
func generateSkeletons(catalog *Catalog, spec *specDoc, path string) error {
	skeletons, err := catalog.Generate(spec)
	if err != nil {
		return err
	}
	if len(skeletons) == 0 {
		fmt.Println("catalog is complete: every spec row has an entry")
		return nil
	}
	if err := WriteGenerated(path, skeletons); err != nil {
		return err
	}
	fmt.Printf("wrote %d skeleton entries to %s\n", len(skeletons), path)
	return nil
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
//
// Only a passing case counts. A skipped one — an annotated skip, or a lane that
// could not start because there is no Docker here — leaves the behavior
// uncovered and the gate red, which is the point: the run that cannot exercise
// a behavior must say so, or "green" would mean "green on whatever happened to
// be runnable". The skip is quoted in the failure so the reason is one line
// away from the hole it made.
func coverageGates(catalog *Catalog, results []*Result) []string {
	covered := map[string][]*Result{}
	skipped := map[string][]*Result{}
	for _, r := range results {
		bucket := covered
		switch {
		case r.Skipped:
			bucket = skipped
		case !r.Passed:
			continue
		}
		for _, id := range r.Case.Behaviors {
			bucket[id] = append(bucket[id], r)
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
				"behavior %s (milestone %s) has no passing case; its milestone has landed, so a case is required%s",
				id, milestone, skipNote(skipped[id])))
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

// skipNote names the cases that were skipped for a behavior, so an uncovered
// behavior reads as "nothing ran it, and here is why" rather than as an
// unexplained hole.
func skipNote(skipped []*Result) string {
	if len(skipped) == 0 {
		return ""
	}
	notes := make([]string, 0, len(skipped))
	for _, r := range skipped {
		note := r.Case.ID
		if r.SkipReason != "" {
			note += " (" + r.SkipReason + ")"
		}
		notes = append(notes, note)
	}
	sort.Strings(notes)
	return " — skipped: " + strings.Join(notes, ", ")
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
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", out, "./cmd/mockulus")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build mockulus for the gate: %w", err)
	}
	return out, nil
}

// log prints a progress line during a run.
func log(msg string) { fmt.Println(msg) }

// topologyFor schedules a case onto the cheapest topology that provides
// everything it declared (SPEC §19.4). `exclusive` is not part of the choice:
// it is a claim on the deployment, not a shape.
func topologyFor(c *Case) string {
	switch {
	case c.RequiresCapability(CapabilityMultiPod):
		return TopologyT3
	case c.RequiresCapability(CapabilityCouchbase):
		return TopologyT2
	default:
		return TopologyT1
	}
}

// execute runs the corpus. Cases share deployments per (topology, variant) and
// stay isolated through their URL namespace; cases needing pristine global
// state declare `requires: [exclusive]` and run serially (SPEC §19.3).
func execute(opt options, cases []*Case, binary string) ([]*Result, error) {
	// A cancelled run has containers to remove, so the signal is turned into a
	// cancellation the run unwinds through rather than being left to the default
	// disposition, which would take the process down with the Couchbase and
	// WireMock containers still up.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool := NewPool(binary, opt.couchbaseFile)
	defer pool.Close()

	exec := &Executor{corpusDir: opt.corpus, pool: pool}

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
		topology, variant := topologyFor(c), c.Variant()

		// A panicking case must not take the run down with it: the process would
		// die without running a single deferred teardown, leaving the run's
		// containers behind for a human to notice. Recovering turns it into the
		// failure it is, and lets the teardown happen.
		defer func() {
			if p := recover(); p != nil {
				record(&Result{Case: c, Topology: topology, Variant: variant,
					Failure: fmt.Sprintf("the runner panicked: %v\n%s", p, debug.Stack())})
			}
		}()

		if c.Skip != nil {
			record(&Result{Case: c, Skipped: true, SkipReason: c.Skip.Reason,
				Topology: topology, Variant: variant})
			return
		}
		dep, err := pool.Get(ctx, topology, variant)
		if err != nil {
			if errors.Is(err, errNoDocker) {
				// No Docker here, so the container lane cannot run at all.
				// Skipping keeps the T1 lane usable on a machine without it; the
				// coverage gate still reports every behavior these cases would
				// have covered as uncovered, so the run cannot come out green.
				record(&Result{Case: c, Skipped: true, Topology: topology, Variant: variant,
					SkipReason: fmt.Sprintf("%s needs a container: %v", topology, err)})
				return
			}
			record(&Result{Case: c, Topology: topology, Variant: variant,
				Failure: "could not start the deployment: " + err.Error()})
			return
		}
		record(exec.Run(ctx, c, dep))
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

func summarize(catalog *Catalog, cases []*Case, gotests []*GoTest, results []*Result, checkOnly bool) {
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
	if len(gotests) > 0 {
		fmt.Printf("go-native:        %d cases\n", len(gotests))
	}

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
	for _, reason := range skipReasons(results) {
		fmt.Printf("skipped:          %s\n", reason)
	}
}

// skipReasons summarizes why cases did not run, one line per distinct reason.
// A lane that quietly skipped is the one thing a reader of a green-looking
// summary most needs told.
func skipReasons(results []*Result) []string {
	counts := map[string]int{}
	for _, r := range results {
		if r.Skipped && r.SkipReason != "" {
			counts[r.SkipReason]++
		}
	}
	out := make([]string, 0, len(counts))
	for reason, n := range counts {
		out = append(out, fmt.Sprintf("%d case(s): %s", n, reason))
	}
	sort.Strings(out)
	return out
}
