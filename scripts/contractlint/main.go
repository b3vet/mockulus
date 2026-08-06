// SPDX-License-Identifier: Apache-2.0

// Command contractlint cross-checks the authored OpenAPI contract against the
// behavior catalog, in both directions (SOW_TS_ADMIN_SDK decisions K1 and K8).
//
//	go run ./scripts/contractlint
//
// `api/openapi.yaml` is what the TypeScript SDK's types are generated from, so
// an operation missing there is a call the SDK cannot make, and an operation
// present there that the server does not implement is a call that compiles and
// then 404s. Neither is visible by reading either file alone, which is the
// whole reason this exists: the coupling rule in AGENTS.md says a change to the
// admin surface updates the contract in the same PR, and a rule enforced by
// review vigilance is a rule that erodes.
//
// The comparison is against the catalog rather than against SPEC §5.1 directly,
// and that is deliberate (K8). The catalog is already pinned row-by-row to §5.1
// by the E2E gate's own lint — every entry carries a `spec_hash` that fails the
// build when its row is edited — so checking the contract against the catalog
// closes the triangle transitively. A third parser reading §5.1's markdown
// would duplicate a job the runner already does and double the markdown-parsing
// surface, which the risk register already flags as the fragile part.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// httpMethods are the methods an operation key may name. OpenAPI puts other
// keys beside them under a path item — `parameters`, `summary`, `$ref` — so the
// set has to be explicit rather than "every key that is not one I know about".
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// operation is one (method, path) pair, which is the unit both sides are
// reduced to before they are compared.
type operation struct {
	Method string
	Path   string
}

func (o operation) String() string { return o.Method + " " + o.Path }

func main() {
	root, err := repoRoot()
	if err != nil {
		fail(err)
	}

	contract, err := contractOperations(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		fail(err)
	}
	catalog, err := catalogOperations(filepath.Join(root, "test", "e2e", "catalog", "admin-api.yaml"))
	if err != nil {
		fail(err)
	}

	var problems []string
	for _, op := range sortedOps(catalog) {
		if !contract[op] {
			problems = append(problems, fmt.Sprintf(
				"%s is catalogued as an admin behavior but is missing from api/openapi.yaml — "+
					"the SDK cannot express a call the contract does not describe", op))
		}
	}
	for _, op := range sortedOps(contract) {
		if !catalog[op] {
			problems = append(problems, fmt.Sprintf(
				"%s is in api/openapi.yaml but has no B-ADMIN-* catalog entry — "+
					"either the server does not implement it or the behavior is uncatalogued, "+
					"and the contract promises it either way", op))
		}
	}

	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "contract lint failed (%d):\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		os.Exit(1)
	}

	fmt.Printf("contract lint: %d operations, matching the catalog in both directions.\n", len(contract))
}

// contractOperations reads the paths block of the OpenAPI document.
func contractOperations(path string) (map[operation]bool, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed repo-relative path
	if err != nil {
		return nil, fmt.Errorf("read the contract: %w", err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Paths) == 0 {
		return nil, fmt.Errorf("%s declares no paths", path)
	}

	out := map[operation]bool{}
	for p, item := range doc.Paths {
		for key := range item {
			if httpMethods[strings.ToLower(key)] {
				out[operation{Method: strings.ToUpper(key), Path: p}] = true
			}
		}
	}
	return out, nil
}

// endpointRow strips the `spec:5.1:admin-endpoints|` prefix off a catalog
// entry's spec_row, leaving the endpoint text §5.1 states.
var endpointRow = regexp.MustCompile(`^spec:5\.1:admin-endpoints\|(.+)$`)

// paramSuffix drops the parenthetical a row may carry to name its query
// parameters — "GET /__admin/requests (+limit,since)". Which parameters an
// endpoint takes is the contract's business to describe; what this lint
// compares is which endpoints exist.
var paramSuffix = regexp.MustCompile(`\s*\(.*\)\s*$`)

// catalogOperations reduces the admin catalog to the same (method, path) set.
//
// A §5.1 row is prose, and a few rows name more than one endpoint because the
// spec groups them: `GET /__admin/settings, POST /__admin/settings` and
// `GET/PUT/DELETE /__admin/files/{name}, GET /__admin/files`. Both spellings
// are expanded here rather than being special-cased by name, so a new grouped
// row needs no change to this program.
func catalogOperations(path string) (map[operation]bool, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed repo-relative path
	if err != nil {
		return nil, fmt.Errorf("read the catalog: %w", err)
	}
	var doc struct {
		Behaviors []struct {
			ID      string `yaml:"id"`
			SpecRow string `yaml:"spec_row"`
		} `yaml:"behaviors"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	out := map[operation]bool{}
	for _, b := range doc.Behaviors {
		m := endpointRow.FindStringSubmatch(b.SpecRow)
		if m == nil {
			continue
		}
		for _, op := range parseEndpoints(m[1]) {
			out[op] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s yielded no admin endpoints, which cannot be right", path)
	}
	return out, nil
}

// parseEndpoints turns one §5.1 endpoint cell into the operations it names.
//
// A segment with no HTTP method is skipped, which is how the unsupported-
// endpoints row (`/__admin/recordings/, /__admin/proxy/, …`) stays out of the
// comparison. Those paths answer 404 by design, so a contract that described
// them would be describing something that does not exist — the one thing this
// lint is for.
func parseEndpoints(cell string) []operation {
	// The parenthetical goes first, before the cell is split. It can contain a
	// comma of its own — "GET /__admin/requests (+limit,since)" — so splitting
	// first tears it in half and leaves neither piece parseable, which is a
	// missing operation rather than an error and so would have gone unnoticed.
	cell = paramSuffix.ReplaceAllString(strings.TrimSpace(cell), "")

	var out []operation
	for _, segment := range strings.Split(cell, ",") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		fields := strings.Fields(segment)
		if len(fields) != 2 {
			// A bare path, or prose. Either way it names no operation.
			continue
		}
		methods, p := fields[0], fields[1]
		if !strings.HasPrefix(p, "/") {
			continue
		}
		// `GET/PUT/DELETE /__admin/files/{name}` is three operations.
		for _, method := range strings.Split(methods, "/") {
			method = strings.ToUpper(strings.TrimSpace(method))
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			out = append(out, operation{Method: method, Path: p})
		}
	}
	return out
}

func sortedOps(set map[operation]bool) []operation {
	out := make([]operation, 0, len(set))
	for op := range set {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// repoRoot walks up from the working directory until it finds the module file,
// so the lint runs the same from the repo root and from an editor's cwd.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "contract lint:", err)
	os.Exit(1)
}
