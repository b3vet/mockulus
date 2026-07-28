// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"errors"
	"fmt"
	"strings"
)

// PathTemplate is a compiled WireMock 3 URL path template such as
// "/orders/{id}/items/{itemId}".
//
// Templates are segment-structured rather than regex-based, which is what lets
// a match bind named variables the pathParameters criteria then apply matchers
// to. Compiling to a segment list also makes matching allocation-free: it walks
// the path in place instead of splitting it.
type PathTemplate struct {
	// Source is the template as written.
	Source string
	// segments are the template's parts in order.
	segments []templateSegment
	// vars lists the variable names in order of appearance.
	vars []string
	// literalPrefix is the leading literal text every matching path starts
	// with, used to prefilter candidates cheaply.
	literalPrefix string
}

type templateSegment struct {
	// literal is the segment text when this is not a variable.
	literal string
	// name is the variable name when this is a variable segment.
	name string
	// isVar distinguishes the two.
	isVar bool
}

// ParsePathTemplate compiles a template, rejecting anything malformed at
// registration rather than letting it become a stub that never matches.
func ParsePathTemplate(source string) (*PathTemplate, error) {
	if source == "" {
		return nil, errors.New("path template is empty")
	}
	if !strings.HasPrefix(source, "/") {
		return nil, fmt.Errorf("path template %q must start with /", source)
	}

	t := &PathTemplate{Source: source}
	seen := map[string]bool{}

	for _, part := range strings.Split(strings.TrimPrefix(source, "/"), "/") {
		switch {
		case strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}"):
			name := part[1 : len(part)-1]
			if name == "" {
				return nil, fmt.Errorf("path template %q has an unnamed variable", source)
			}
			if strings.ContainsAny(name, "{}/") {
				return nil, fmt.Errorf("path template %q has a malformed variable %q", source, part)
			}
			if seen[name] {
				return nil, fmt.Errorf("path template %q binds %q more than once", source, name)
			}
			seen[name] = true
			t.segments = append(t.segments, templateSegment{name: name, isVar: true})
			t.vars = append(t.vars, name)

		case strings.ContainsAny(part, "{}"):
			// A brace outside a whole-segment variable is a typo, not a
			// template WireMock would accept; say so rather than matching
			// it literally and leaving the author puzzled.
			return nil, fmt.Errorf(
				"path template %q: a variable must be a whole segment, as in /orders/{id}", source)

		default:
			t.segments = append(t.segments, templateSegment{literal: part})
		}
	}

	t.literalPrefix = computeTemplatePrefix(t.segments)
	return t, nil
}

func computeTemplatePrefix(segments []templateSegment) string {
	var sb strings.Builder
	for _, seg := range segments {
		if seg.isVar {
			break
		}
		sb.WriteByte('/')
		sb.WriteString(seg.literal)
	}
	if sb.Len() == 0 {
		return "/"
	}
	return sb.String()
}

// Vars returns the variable names the template binds, in order.
func (t *PathTemplate) Vars() []string { return t.vars }

// LiteralPrefix returns the leading literal text of the template.
func (t *PathTemplate) LiteralPrefix() string { return t.literalPrefix }

// Match reports whether the path fits the template, calling bind for each
// variable it binds. Binding happens as segments are consumed, so a failed
// match may have bound some variables already — the caller clears them between
// candidate stubs, which is what keeps one stub's bindings out of another's
// criteria.
//
// The path is walked in place: no splitting, no allocation.
func (t *PathTemplate) Match(path string, bind func(name, value string)) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	rest := path[1:]

	for i, seg := range t.segments {
		var part string
		last := i == len(t.segments)-1

		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			part, rest = rest[:slash], rest[slash+1:]
			if last {
				// The path has more segments than the template.
				return false
			}
		} else {
			part, rest = rest, ""
			if !last {
				// The path ran out before the template did.
				return false
			}
		}

		if seg.isVar {
			if part == "" {
				// A variable must bind something; an empty segment would make
				// /orders//items match /orders/{id}/items.
				return false
			}
			if bind != nil {
				bind(seg.name, part)
			}
			continue
		}
		if part != seg.literal {
			return false
		}
	}
	return rest == ""
}
