// SPDX-License-Identifier: Apache-2.0

// Package jsonschemax compiles the JSON Schema documents `matchesJsonSchema`
// carries, and is the only place in mockulus that knows a schema library exists.
//
// It is a seam for the reason `internal/regexx` is one: the matcher package
// takes a compiled validator through an injected function and never imports the
// library, so swapping it is a change here rather than a change everywhere. It
// is also where three policies live that are not the library's defaults and are
// not obvious from the call site — the draft a schema is read under, whether
// `format` is asserted, and what happens to a `$ref` that points outside the
// document. All three were established by probing pinned WireMock 3.13.2 rather
// than assumed, and each is explained where it is implemented.
package jsonschemax

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validator is a compiled schema. The matcher package holds one of these and
// nothing else from this package.
type Validator interface {
	// Valid reports whether a parsed JSON document satisfies the schema.
	Valid(doc any) bool
	// Source is the schema as registered, for diagnostics.
	Source() string
}

// DefaultVersion is the draft a schema is read under when it names none.
//
// 2020-12, which is WireMock's default and is also what it injects into the
// mapping it echoes back. It matters beyond keyword availability: `format` is
// annotation-only in this draft, so the common case validates the shape of a
// document and not the contents of its strings.
const DefaultVersion = "V202012"

// versions are the schemaVersion spellings WireMock accepts, and the only ones
// accepted here. The set is exact and case-sensitive there — `v7` and `"V7 "`
// are both refused — and the error it answers names the whole set, which is
// worth copying because it turns a typo into a fix rather than a search.
var versions = map[string]*jsonschema.Draft{
	"V4":      jsonschema.Draft4,
	"V6":      jsonschema.Draft6,
	"V7":      jsonschema.Draft7,
	"V201909": jsonschema.Draft2019,
	"V202012": jsonschema.Draft2020,
}

// schemaURIs maps the `$schema` values a document may declare onto drafts.
//
// A document's own `$schema` overrides the `schemaVersion` parameter outright,
// in both directions — verified against the oracle with probes constructed so
// the two candidates give opposite verdicts. A URI outside this set is refused:
// WireMock accepts it and the stub then matches nothing at all, silently, even
// where the schema would otherwise be always-true.
var schemaURIs = map[string]*jsonschema.Draft{
	"http://json-schema.org/draft-04/schema":       jsonschema.Draft4,
	"http://json-schema.org/draft-06/schema":       jsonschema.Draft6,
	"http://json-schema.org/draft-07/schema":       jsonschema.Draft7,
	"https://json-schema.org/draft/2019-09/schema": jsonschema.Draft2019,
	"https://json-schema.org/draft/2020-12/schema": jsonschema.Draft2020,
}

// assertsFormat reports whether a draft treats `format` as an assertion.
//
// The split is the specification's own vocabulary boundary and WireMock follows
// it exactly: drafts 4, 6 and 7 assert, and 2019-09 and 2020-12 treat `format`
// as an annotation a validator may ignore. Since 2020-12 is the default, a
// stub that writes `{"format": "email"}` and expects validation gets none
// unless it also pins an older draft — surprising, and reproduced deliberately.
func assertsFormat(d *jsonschema.Draft) bool {
	return d == jsonschema.Draft4 || d == jsonschema.Draft6 || d == jsonschema.Draft7
}

// Compile builds a validator, refusing at registration anything that could not
// work at match time.
//
// WireMock validates only that the operand is JSON. Everything else — a `type`
// value that names no type, a `$ref` to a location that does not exist, a schema
// that is a bare number — registers there and then misbehaves: the first two
// match nothing ever, and the last matches *everything*. Compiling here turns
// each of those into a 422 naming the field (SPEC §5.5).
func Compile(source, version string) (Validator, error) {
	if strings.TrimSpace(source) == "" {
		return nil, errors.New("the schema is empty")
	}

	var doc any
	if err := json.Unmarshal([]byte(source), &doc); err != nil {
		return nil, fmt.Errorf("the schema is not valid JSON: %w", err)
	}

	// A schema is an object or a boolean. WireMock accepts a bare number,
	// string, array or null and the stub then matches every request — the
	// fail-open half of accepting an invalid schema, and the worse half.
	switch doc.(type) {
	case map[string]any, bool:
	default:
		return nil, errors.New("a schema must be a JSON object or a boolean; " +
			"a bare value here would accept every request")
	}

	draft, err := effectiveDraft(doc, version)
	if err != nil {
		return nil, err
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(draft)
	if assertsFormat(draft) {
		compiler.AssertFormat()
	}
	// Nothing outside the document is reachable. See refusingLoader.
	compiler.UseLoader(refusingLoader{})

	const rootURL = "mem://schema"
	if addErr := compiler.AddResource(rootURL, doc); addErr != nil {
		return nil, fmt.Errorf("the schema could not be read: %w", addErr)
	}
	compiled, err := compiler.Compile(rootURL)
	if err != nil {
		return nil, fmt.Errorf("the schema is not usable: %w", cleanError(err))
	}
	return &validator{schema: compiled, source: source}, nil
}

// effectiveDraft resolves which draft a schema is read under.
func effectiveDraft(doc any, version string) (*jsonschema.Draft, error) {
	if version == "" {
		version = DefaultVersion
	}
	draft, ok := versions[version]
	if !ok {
		return nil, fmt.Errorf("schemaVersion must be one of %s", versionList())
	}

	obj, isObject := doc.(map[string]any)
	if !isObject {
		return draft, nil
	}
	declared, present := obj["$schema"]
	if !present {
		return draft, nil
	}
	uri, isString := declared.(string)
	if !isString {
		return nil, errors.New("$schema must be a string naming a JSON Schema draft")
	}
	// Trailing "#" is how several of these are conventionally written.
	if fromDoc, known := schemaURIs[strings.TrimSuffix(uri, "#")]; known {
		return fromDoc, nil
	}
	return nil, fmt.Errorf("$schema %q names no JSON Schema draft this understands; "+
		"expected one of %s", uri, schemaURIList())
}

// refusingLoader answers every reference that is not inside the document.
//
// WireMock resolves `$ref` strictly in-document — JSON pointers, `$anchor` and
// `$id` — and **never fetches a remote one**. That was established rather than
// assumed: a listener that accepts and never answers recorded no connection at
// all across the whole probe, and a `$ref` pointing at a reachable schema still
// did not resolve. So this is not closing a network hole; there is no fetch to
// close.
//
// What it does is make the failure sayable. On WireMock an unresolvable
// reference registers cleanly and then aborts the whole schema evaluation the
// first time that subschema is applied — the stub simply never matches, with no
// error text anywhere in the response and nothing in the diagnostics that points
// at the reference. Refusing at registration names the field instead.
//
// The cost is real and is documented with the deviation: WireMock's failure is
// lazy, so a stub whose bad reference sits under a property no request happens to
// carry works there and is refused here.
type refusingLoader struct{}

func (refusingLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("$ref %q points outside the schema; only references "+
		"within the document resolve — $defs, definitions, a JSON pointer, $anchor or $id", url)
}

// validator adapts a compiled schema to the interface the matcher holds.
type validator struct {
	schema *jsonschema.Schema
	source string
}

func (v *validator) Valid(doc any) bool { return v.schema.Validate(doc) == nil }
func (v *validator) Source() string     { return v.source }

// versionList renders the accepted schemaVersion set for an error message.
func versionList() string {
	names := make([]string, 0, len(versions))
	for name := range versions {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func schemaURIList() string {
	uris := make([]string, 0, len(schemaURIs))
	for uri := range schemaURIs {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	return strings.Join(uris, ", ")
}

// cleanError trims the library's multi-line compilation report to its first
// line, which is the part that names the problem. The rest is a jsonschema
// location trace that means nothing to somebody reading a 422 about their stub.
func cleanError(err error) error {
	first, _, _ := strings.Cut(err.Error(), "\n")
	return errors.New(strings.TrimSpace(first))
}
