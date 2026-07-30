// SPDX-License-Identifier: Apache-2.0

// Package template provides the response-templating engine of SPEC §10: the
// WireMock helper allowlist, the request model a template can see, and the
// compile-and-render path.
//
// Two properties matter more than feature coverage. Templates are compiled at
// stub registration, so an unknown helper or a parse error is a 422 there
// rather than a broken response later (P3). And the helper set is an allowlist,
// not a denylist — `file`, `systemValue`, `secret` and `hostname` are absent
// because nothing in a mock server should read the filesystem, the environment
// or the host it happens to be running on (SPEC §17).
package template

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/b3vet/mockulus/internal/handlebars"
	"github.com/b3vet/mockulus/internal/javatime"
)

// randomValue types, as WireMock spells them.
const (
	randAlphanumeric = "ALPHANUMERIC"
	randAlphabetic   = "ALPHABETIC"
	randNumeric      = "NUMERIC"
	randUUID         = "UUID"
	randHexadecimal  = "HEXADECIMAL"
)

// alphabets backing randomValue.
const (
	alphaLower = "abcdefghijklmnopqrstuvwxyz"
	alphaUpper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digits     = "0123456789"
	hexDigits  = "0123456789abcdef"
)

// NewRegistry builds the helper allowlist of SPEC §10.3.
//
// jsonPath is supplied by the caller rather than built here, because it shares
// the matcher engine's JSONPath implementation — there is one definition of
// what a path expression means across the product.
func NewRegistry(jsonPath handlebars.Helper) *handlebars.Registry {
	r := handlebars.NewRegistry()

	if jsonPath != nil {
		r.Register("jsonPath", jsonPath)
	}

	r.Register("now", nowHelper)
	r.Register("randomValue", randomValueHelper)
	r.Register("pickRandom", pickRandomHelper)
	r.Register("randomInt", randomIntHelper)
	r.Register("randomDecimal", randomDecimalHelper)
	r.Register("math", mathHelper)
	r.Register("number", numberHelper)
	r.Register("base64", base64Helper)
	r.Register("urlEncode", urlEncodeHelper)
	r.Register("range", rangeHelper)
	r.Register("lookup", lookupHelper)

	// String helpers.
	r.Register("trim", unary(strings.TrimSpace))
	r.Register("lower", unary(strings.ToLower))
	r.Register("lowercase", unary(strings.ToLower))
	r.Register("upper", unary(strings.ToUpper))
	r.Register("uppercase", unary(strings.ToUpper))
	r.Register("split", splitHelper)
	r.Register("join", joinHelper)
	r.Register("concat", concatHelper)
	r.Register("substring", substringHelper)
	r.Register("replace", replaceHelper)
	r.Register("size", sizeHelper)
	r.Register("default", defaultHelper)

	return r
}

// unary lifts a string function into a helper.
func unary(fn func(string) string) handlebars.Helper {
	return func(args []any, _ map[string]any) (any, error) {
		if len(args) == 0 {
			return "", nil
		}
		return fn(handlebars.Stringify(args[0])), nil
	}
}

// nowHelper renders the current time, with WireMock's offset, format and
// timezone options.
func nowHelper(args []any, hash map[string]any) (any, error) {
	t := time.Now().UTC()

	if raw, ok := hash["offset"]; ok {
		offset, err := javatime.ParseOffset(handlebars.Stringify(raw))
		if err != nil {
			return nil, err
		}
		t = offset.Shift(t)
	}

	if raw, ok := hash["timezone"]; ok {
		name := handlebars.Stringify(raw)
		loc, err := time.LoadLocation(name)
		if err != nil {
			return nil, fmt.Errorf("unknown timezone %q", name)
		}
		t = t.In(loc)
	}

	// The format a bare `now` falls back to. WireMock's default is not a Java
	// pattern at all — it formats the instant with its ISO-8601 helper, which
	// ends in "Z" at a zero offset and in "+HH:MM" elsewhere. That is exactly
	// what XXX spells, so the default is written as the pattern that produces
	// it rather than as a second code path that has to be kept in step.
	format := "yyyy-MM-dd'T'HH:mm:ssXXX"
	if raw, ok := hash["format"]; ok {
		format = handlebars.Stringify(raw)
	}

	switch format {
	case "epoch":
		return strconv.FormatInt(t.UnixMilli(), 10), nil
	case "unix":
		return strconv.FormatInt(t.Unix(), 10), nil
	default:
		return t.Format(javatime.Layout(format)), nil
	}
}

func randomValueHelper(_ []any, hash map[string]any) (any, error) {
	kind := randAlphanumeric
	if raw, ok := hash["type"]; ok {
		kind = strings.ToUpper(handlebars.Stringify(raw))
	}

	length := 36
	if raw, ok := hash["length"]; ok {
		n, err := toInt(raw)
		if err != nil {
			return nil, fmt.Errorf("randomValue length: %w", err)
		}
		if n < 0 {
			return nil, errors.New("randomValue length must not be negative")
		}
		length = n
	}

	var value string
	switch kind {
	case randUUID:
		value = randomUUID()
	case randAlphabetic:
		value = randomFrom(alphaLower+alphaUpper, length)
	case randNumeric:
		value = randomFrom(digits, length)
	case randHexadecimal:
		value = randomFrom(hexDigits, length)
	case randAlphanumeric:
		value = randomFrom(alphaLower+alphaUpper+digits, length)
	default:
		return nil, fmt.Errorf("unknown randomValue type %q", kind)
	}

	if truthyHash(hash, "uppercase") {
		value = strings.ToUpper(value)
	}
	return value, nil
}

func randomFrom(alphabet string, length int) string {
	if length <= 0 {
		return ""
	}
	out := make([]byte, length)
	for i := range out {
		out[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(out)
}

// randomUUID builds a version 4 UUID without pulling the request path into a
// dependency it does not otherwise need.
func randomUUID() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func pickRandomHelper(args []any, _ map[string]any) (any, error) {
	var options []any
	if len(args) == 1 {
		switch list := args[0].(type) {
		case []any:
			options = list
		case []string:
			for _, s := range list {
				options = append(options, s)
			}
		default:
			options = args
		}
	} else {
		options = args
	}
	if len(options) == 0 {
		return "", nil
	}
	return options[rand.IntN(len(options))], nil
}

func randomIntHelper(_ []any, hash map[string]any) (any, error) {
	lower, upper := 0, math.MaxInt32
	if raw, ok := hash["lower"]; ok {
		n, err := toInt(raw)
		if err != nil {
			return nil, err
		}
		lower = n
	}
	if raw, ok := hash["upper"]; ok {
		n, err := toInt(raw)
		if err != nil {
			return nil, err
		}
		upper = n
	}
	if upper < lower {
		return nil, errors.New("randomInt upper must not be below lower")
	}
	return lower + rand.IntN(upper-lower+1), nil
}

func randomDecimalHelper(_ []any, hash map[string]any) (any, error) {
	lower, upper := 0.0, 1.0
	if raw, ok := hash["lower"]; ok {
		f, err := toFloat(raw)
		if err != nil {
			return nil, err
		}
		lower = f
	}
	if raw, ok := hash["upper"]; ok {
		f, err := toFloat(raw)
		if err != nil {
			return nil, err
		}
		upper = f
	}
	if upper < lower {
		return nil, errors.New("randomDecimal upper must not be below lower")
	}
	return lower + rand.Float64()*(upper-lower), nil
}

func mathHelper(args []any, _ map[string]any) (any, error) {
	if len(args) != 3 {
		return nil, errors.New("math takes a left operand, an operator and a right operand")
	}
	left, err := toFloat(args[0])
	if err != nil {
		return nil, err
	}
	right, err := toFloat(args[2])
	if err != nil {
		return nil, err
	}

	switch op := handlebars.Stringify(args[1]); op {
	case "+":
		return left + right, nil
	case "-":
		return left - right, nil
	case "*", "x":
		return left * right, nil
	case "/":
		if right == 0 {
			return nil, errors.New("math: division by zero")
		}
		return left / right, nil
	case "%":
		if right == 0 {
			return nil, errors.New("math: modulo by zero")
		}
		return math.Mod(left, right), nil
	default:
		return nil, fmt.Errorf("unknown math operator %q", op)
	}
}

func numberHelper(args []any, hash map[string]any) (any, error) {
	if len(args) == 0 {
		return "", nil
	}
	f, err := toFloat(args[0])
	if err != nil {
		return nil, err
	}
	if raw, ok := hash["decimals"]; ok {
		n, err := toInt(raw)
		if err != nil {
			return nil, err
		}
		return strconv.FormatFloat(f, 'f', n, 64), nil
	}
	return handlebars.Stringify(f), nil
}

func base64Helper(args []any, hash map[string]any) (any, error) {
	if len(args) == 0 {
		return "", nil
	}
	s := handlebars.Stringify(args[0])
	if truthyHash(hash, "decode") {
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("base64 decode: %w", err)
		}
		return string(decoded), nil
	}
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}

func urlEncodeHelper(args []any, hash map[string]any) (any, error) {
	if len(args) == 0 {
		return "", nil
	}
	s := handlebars.Stringify(args[0])
	if truthyHash(hash, "decode") {
		decoded, err := url.QueryUnescape(s)
		if err != nil {
			return nil, fmt.Errorf("urlEncode decode: %w", err)
		}
		return decoded, nil
	}
	return url.QueryEscape(s), nil
}

// rangeHelper builds a list, which {{#each}} then iterates.
func rangeHelper(args []any, _ map[string]any) (any, error) {
	if len(args) != 2 {
		return nil, errors.New("range takes a lower and an upper bound")
	}
	lower, err := toInt(args[0])
	if err != nil {
		return nil, err
	}
	upper, err := toInt(args[1])
	if err != nil {
		return nil, err
	}
	if upper < lower {
		return []any{}, nil
	}
	// A template that asks for a huge range would otherwise allocate it before
	// the output cap ever gets a chance to fire.
	const maxRange = 10000
	if upper-lower+1 > maxRange {
		return nil, fmt.Errorf("range of %d exceeds the %d limit", upper-lower+1, maxRange)
	}

	out := make([]any, 0, upper-lower+1)
	for i := lower; i <= upper; i++ {
		out = append(out, i)
	}
	return out, nil
}

// lookupHelper indexes a collection by a dynamic key. A key that names nothing
// — a map without that entry, an index that is not a number, an index past the
// end, or an argument that is not a collection at all — is a miss and renders
// as nothing, the same as Handlebars' own lookup. A miss is deliberately not an
// error, and that is why the failed Atoi below is discarded rather than
// returned: a template walking a list and asking for a key only some entries
// carry would otherwise fail the whole response rather than leave one value
// blank.
func lookupHelper(args []any, _ map[string]any) (any, error) {
	if len(args) != 2 {
		return nil, errors.New("lookup takes a collection and a key")
	}
	key := handlebars.Stringify(args[1])

	switch coll := args[0].(type) {
	case map[string]any:
		return coll[key], nil
	case map[string]string:
		return coll[key], nil
	case []any:
		i, ok := elementIndex(key, len(coll))
		if !ok {
			return nil, nil
		}
		return coll[i], nil
	case []string:
		i, ok := elementIndex(key, len(coll))
		if !ok {
			return nil, nil
		}
		return coll[i], nil
	default:
		return nil, nil
	}
}

func splitHelper(args []any, _ map[string]any) (any, error) {
	if len(args) < 2 {
		return nil, errors.New("split takes a string and a separator")
	}
	parts := strings.Split(handlebars.Stringify(args[0]), handlebars.Stringify(args[1]))
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out, nil
}

// joinHelper puts a separator between a list's elements, and — given a name a
// header or query parameter arrived under more than once — between all of its
// values rather than around the first one. Reading such a node as a scalar
// answers "crimson" for `?tag=crimson&tag=blue&tag=green` where WireMock
// answers "crimson-blue-green", which is a response that has quietly dropped
// what the caller sent. It is the node `size` counts, read the same way here:
// the values, rather than the one of them that prints.
func joinHelper(args []any, _ map[string]any) (any, error) {
	if len(args) < 2 {
		return nil, errors.New("join takes a list and a separator")
	}
	sep := handlebars.Stringify(args[len(args)-1])

	var parts []string
	switch list := args[0].(type) {
	case multiValue:
		parts = list
	case []any:
		for _, item := range list {
			parts = append(parts, handlebars.Stringify(item))
		}
	case []string:
		parts = list
	default:
		for _, a := range args[:len(args)-1] {
			parts = append(parts, handlebars.Stringify(a))
		}
	}
	return strings.Join(parts, sep), nil
}

func concatHelper(args []any, _ map[string]any) (any, error) {
	var sb strings.Builder
	for _, a := range args {
		sb.WriteString(handlebars.Stringify(a))
	}
	return sb.String(), nil
}

// substringHelper slices by rune, so a multibyte body is not cut mid-character.
func substringHelper(args []any, _ map[string]any) (any, error) {
	if len(args) < 2 {
		return nil, errors.New("substring takes a string, a start and an optional end")
	}
	runes := []rune(handlebars.Stringify(args[0]))

	start, err := toInt(args[1])
	if err != nil {
		return nil, err
	}
	end := len(runes)
	if len(args) > 2 {
		end, err = toInt(args[2])
		if err != nil {
			return nil, err
		}
	}

	start = clamp(start, 0, len(runes))
	end = clamp(end, start, len(runes))
	return string(runes[start:end]), nil
}

func replaceHelper(args []any, _ map[string]any) (any, error) {
	if len(args) != 3 {
		return nil, errors.New("replace takes a string, a target and a replacement")
	}
	return strings.ReplaceAll(
		handlebars.Stringify(args[0]),
		handlebars.Stringify(args[1]),
		handlebars.Stringify(args[2])), nil
}

// sizeHelper counts a collection's elements and a scalar's characters, which is
// the split WireMock's helper makes on the Java type it is handed.
//
// A query parameter and a header are collections on that side even when the
// wire carried one value: they print as their first value and carry all of
// them (§10.2). Measuring the text they print answers 5 for `?tier=go-ld`
// where WireMock answers 1, and the length of "crimson" for a parameter sent
// three times where WireMock answers 3 — a number that equals the count only
// by coincidence, and a plausible one every time, so a stub paging or
// branching on it is wrong quietly.
//
// `request.path` is dual-natured in the same way and is deliberately not
// counted: it stays the text it prints, which is the reading
// cov-tmpl-strings-shapes-001 records. WireMock counts its segments instead —
// a real disagreement, wider than the repeated values here, and not one this
// helper settles on its own.
func sizeHelper(args []any, _ map[string]any) (any, error) {
	if len(args) == 0 {
		return 0, nil
	}
	switch v := args[0].(type) {
	case string:
		return len([]rune(v)), nil
	case multiValue:
		return len(v), nil
	case []any:
		return len(v), nil
	case []string:
		return len(v), nil
	case map[string]any:
		return len(v), nil
	case map[string]string:
		return len(v), nil
	default:
		return len([]rune(handlebars.Stringify(v))), nil
	}
}

// defaultHelper substitutes a fallback for an absent or empty value, which is
// what keeps a templated stub serving when an optional field is missing.
func defaultHelper(args []any, _ map[string]any) (any, error) {
	for _, a := range args {
		if handlebars.Truthy(a) {
			return a, nil
		}
	}
	if len(args) > 0 {
		return args[len(args)-1], nil
	}
	return "", nil
}

func truthyHash(hash map[string]any, key string) bool {
	raw, ok := hash[key]
	return ok && handlebars.Truthy(raw)
}

func toInt(v any) (int, error) {
	f, err := toFloat(v)
	if err != nil {
		return 0, err
	}
	return int(f), nil
}

func toFloat(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", t)
		}
		return f, nil
	case nil:
		return 0, errors.New("expected a number, got nothing")
	case fmt.Stringer:
		// Query parameters, header values and path segments arrive as a value
		// that is both a scalar and an indexable list (§10.2). A number helper
		// has to read the same characters the body would have printed, or
		// {{math request.query.a '+' request.query.b}} — which WireMock 3.13.2
		// answers with the sum — fails on every request that supplies one.
		return toFloat(t.String())
	default:
		return 0, fmt.Errorf("expected a number, got %T", v)
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// elementIndex resolves a lookup key against a collection of n elements. A key
// that is not a number, or that falls outside the collection, selects nothing —
// the same answer a lookup of an absent map member gives, and the reason this
// returns a bool rather than the error strconv produced: there is no failure
// here to report, only a key that named no element.
func elementIndex(key string, n int) (int, bool) {
	i, err := strconv.Atoi(key)
	if err != nil || i < 0 || i >= n {
		return 0, false
	}
	return i, true
}
