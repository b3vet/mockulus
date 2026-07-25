// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that renders and parses in Go duration syntax
// ("1s", "200ms"), as SPEC §13 specifies for every duration-valued key.
type Duration time.Duration

// D returns the wrapped time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String renders the duration in the same syntax it is parsed from.
func (d Duration) String() string { return time.Duration(d).String() }

func (d *Duration) parse(s string) error {
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q (want Go syntax, e.g. 1s, 200ms)", s)
	}
	*d = Duration(v)
	return nil
}

// Bytes is a byte-count quantity written either bare ("8192") or with an
// IEC suffix ("64KiB", "10MiB"), as SPEC §13 specifies for every size key.
type Bytes int64

// B returns the size in bytes.
func (b Bytes) B() int64 { return int64(b) }

// String renders the size using the largest IEC suffix that divides it evenly,
// so a parsed value round-trips to the spelling it came from.
func (b Bytes) String() string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	n := int64(b)
	switch {
	case n == 0:
		return "0"
	case n%gib == 0:
		return strconv.FormatInt(n/gib, 10) + "GiB"
	case n%mib == 0:
		return strconv.FormatInt(n/mib, 10) + "MiB"
	case n%kib == 0:
		return strconv.FormatInt(n/kib, 10) + "KiB"
	default:
		return strconv.FormatInt(n, 10)
	}
}

var byteSuffixes = []struct {
	suffix string
	mult   int64
}{
	{"GiB", 1 << 30},
	{"MiB", 1 << 20},
	{"KiB", 1 << 10},
	{"B", 1},
}

func (b *Bytes) parse(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("empty size")
	}
	for _, sfx := range byteSuffixes {
		if len(s) > len(sfx.suffix) && strings.EqualFold(s[len(s)-len(sfx.suffix):], sfx.suffix) {
			n, err := strconv.ParseInt(strings.TrimSpace(s[:len(s)-len(sfx.suffix)]), 10, 64)
			if err != nil {
				return fmt.Errorf("invalid size %q", s)
			}
			if n < 0 {
				return fmt.Errorf("invalid size %q: must not be negative", s)
			}
			if n > (1<<62)/sfx.mult {
				return fmt.Errorf("invalid size %q: out of range", s)
			}
			*b = Bytes(n * sfx.mult)
			return nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid size %q (want bytes, optionally suffixed KiB/MiB/GiB)", s)
	}
	if n < 0 {
		return fmt.Errorf("invalid size %q: must not be negative", s)
	}
	*b = Bytes(n)
	return nil
}
