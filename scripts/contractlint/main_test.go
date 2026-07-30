// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"
)

func TestParseEndpointsExpandsTheGroupedRows(t *testing.T) {
	cases := []struct {
		cell string
		want []operation
	}{
		// The ordinary shape: one row, one operation.
		{"POST /__admin/mappings", []operation{{"POST", "/__admin/mappings"}}},
		{"PUT /__admin/scenarios/{name}/state", []operation{{"PUT", "/__admin/scenarios/{name}/state"}}},

		// A row naming two endpoints, which §5.1 does where they are one idea.
		{"GET /__admin/settings, POST /__admin/settings", []operation{
			{"GET", "/__admin/settings"},
			{"POST", "/__admin/settings"},
		}},

		// Methods collapsed onto one path, and a second endpoint beside it.
		{"GET/PUT/DELETE /__admin/files/{name}, GET /__admin/files", []operation{
			{"GET", "/__admin/files/{name}"},
			{"PUT", "/__admin/files/{name}"},
			{"DELETE", "/__admin/files/{name}"},
			{"GET", "/__admin/files"},
		}},

		// The parenthetical names query parameters, which are the contract's
		// business rather than this lint's.
		{"GET /__admin/requests (+limit,since)", []operation{{"GET", "/__admin/requests"}}},

		// The unsupported-endpoints row. No methods, so no operations — this is
		// what keeps paths that answer 404 by design out of the comparison, and
		// getting it wrong would demand the contract describe endpoints that do
		// not exist.
		{"/__admin/recordings/, /__admin/proxy/, /__admin/certificates/, /__admin/mappings/unmatched*", nil},
	}

	for _, tc := range cases {
		got := parseEndpoints(tc.cell)
		if len(got) != len(tc.want) {
			t.Errorf("parseEndpoints(%q) gave %v, want %v", tc.cell, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseEndpoints(%q)[%d] = %v, want %v", tc.cell, i, got[i], tc.want[i])
			}
		}
	}
}

// The lint is only worth having if it reads the real catalog, so this asserts
// against the file rather than a fixture: a change to the catalog's shape that
// this parser cannot follow shows up here rather than as a lint that quietly
// compares an empty set and passes.
func TestCatalogOperationsReadsTheRealCatalog(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate the repo root: %v", err)
	}
	ops, err := catalogOperations(filepath.Join(root, "test", "e2e", "catalog", "admin-api.yaml"))
	if err != nil {
		t.Fatalf("read the catalog: %v", err)
	}

	// A handful of operations that must be there, chosen to cover each row
	// shape the parser handles.
	for _, want := range []operation{
		{"POST", "/__admin/mappings"},
		{"GET", "/__admin/mappings/{id}"},
		{"DELETE", "/__admin/mappings/{id}"},
		{"POST", "/__admin/mappings/find-by-metadata"},
		{"GET", "/__admin/settings"},
		{"POST", "/__admin/settings"},
		{"PUT", "/__admin/files/{name}"},
		{"GET", "/__admin/files"},
		{"GET", "/__admin/requests"},
		{"PUT", "/__admin/scenarios/{name}/state"},
		{"GET", "/__admin/health"},
		{"GET", "/__admin/version"},
	} {
		if !ops[want] {
			t.Errorf("the catalog should yield %v", want)
		}
	}

	// And the unsupported paths must not be, or the contract would be required
	// to describe endpoints the server refuses on purpose.
	for _, unwanted := range []operation{
		{"GET", "/__admin/recordings/"},
		{"GET", "/__admin/proxy/"},
	} {
		if ops[unwanted] {
			t.Errorf("%v is an unsupported endpoint and must not be required of the contract", unwanted)
		}
	}

	// A sanity floor: §5.1 is a large surface, and a parser that silently
	// matched almost nothing would pass every assertion above and still make
	// the lint vacuous.
	if len(ops) < 30 {
		t.Errorf("the catalog yielded only %d operations, which is too few to be a full read of §5.1", len(ops))
	}
}
