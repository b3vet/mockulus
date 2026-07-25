// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
)

// parseYAML reads the deliberately small YAML subset mockulus configuration
// files are written in: nested mappings of scalar values, with comments and
// quoted strings. Every key of SPEC §13 is a scalar, so the subset is complete
// for the configuration surface — and anything outside it (lists, anchors,
// block scalars, multiple documents) is rejected with a line number rather than
// silently reinterpreted. Keeping the parser in-tree keeps the shipped binary's
// module graph to the SPEC §18 allowlist.
//
// The result maps dotted paths ("couchbase.connstr") to raw scalar text; typing
// happens in setField against the destination struct field.
func parseYAML(src string) (map[string]string, error) {
	out := make(map[string]string)

	type frame struct {
		indent int
		prefix string
	}
	// The root frame has indent -1 so any top-level key (indent 0) nests under it.
	stack := []frame{{indent: -1, prefix: ""}}

	for lineNo, raw := range strings.Split(src, "\n") {
		line := strings.TrimRight(raw, "\r")
		at := func(format string, args ...any) error {
			return fmt.Errorf("line %d: %s", lineNo+1, fmt.Sprintf(format, args...))
		}

		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.ContainsRune(line[:len(line)-len(strings.TrimLeft(line, " \t"))], '\t') {
			return nil, at("tab indentation is not supported; use spaces")
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))
		body := strings.TrimLeft(line, " ")

		switch {
		case strings.HasPrefix(body, "- "), body == "-":
			return nil, at("list values are not supported in mockulus configuration")
		case strings.HasPrefix(body, "---"), strings.HasPrefix(body, "..."):
			return nil, at("multi-document YAML is not supported")
		}

		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		if indent <= stack[len(stack)-1].indent {
			return nil, at("inconsistent indentation")
		}

		key, value, hasValue := strings.Cut(body, ":")
		if !hasValue {
			return nil, at("expected `key: value`, got %q", body)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, at("empty key")
		}
		if strings.ContainsAny(key, "\"'{}[]&*") {
			return nil, at("unsupported key syntax %q", key)
		}

		path := key
		if p := stack[len(stack)-1].prefix; p != "" {
			path = p + "." + key
		}

		value = strings.TrimSpace(value)
		if value == "" || strings.HasPrefix(value, "#") {
			// A key with no scalar opens a nested section.
			stack = append(stack, frame{indent: indent, prefix: path})
			continue
		}

		scalar, err := parseScalar(value)
		if err != nil {
			return nil, at("%s: %v", path, err)
		}
		if _, dup := out[path]; dup {
			return nil, at("duplicate key %q", path)
		}
		out[path] = scalar
	}
	return out, nil
}

func parseScalar(v string) (string, error) {
	switch v[0] {
	case '"':
		return parseQuoted(v)
	case '\'':
		return parseSingleQuoted(v)
	case '|', '>', '&', '*', '{', '[':
		return "", fmt.Errorf("unsupported value syntax %q", v)
	}
	// Strip a trailing comment; YAML requires whitespace before the `#`.
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v), nil
}

func parseQuoted(v string) (string, error) {
	var sb strings.Builder
	for i := 1; i < len(v); i++ {
		switch v[i] {
		case '\\':
			if i+1 >= len(v) {
				return "", fmt.Errorf("unterminated escape in %q", v)
			}
			i++
			switch v[i] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case '"', '\\', '/':
				sb.WriteByte(v[i])
			default:
				return "", fmt.Errorf("unsupported escape \\%c", v[i])
			}
		case '"':
			if rest := strings.TrimSpace(v[i+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
				return "", fmt.Errorf("trailing content after quoted value: %q", rest)
			}
			return sb.String(), nil
		default:
			sb.WriteByte(v[i])
		}
	}
	return "", fmt.Errorf("unterminated quoted value %q", v)
}

func parseSingleQuoted(v string) (string, error) {
	var sb strings.Builder
	for i := 1; i < len(v); i++ {
		if v[i] != '\'' {
			sb.WriteByte(v[i])
			continue
		}
		if i+1 < len(v) && v[i+1] == '\'' { // '' is an escaped quote
			sb.WriteByte('\'')
			i++
			continue
		}
		if rest := strings.TrimSpace(v[i+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
			return "", fmt.Errorf("trailing content after quoted value: %q", rest)
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("unterminated quoted value %q", v)
}
