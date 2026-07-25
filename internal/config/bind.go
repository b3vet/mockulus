// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// fieldMeta is the static description of one configuration key, derived from
// the struct tags in config.go. It drives binding, redaction and doc generation
// alike, which is what keeps SPEC §13 and the code in sync.
type fieldMeta struct {
	Path   string // dotted YAML path, e.g. "couchbase.connstr"
	Env    string // environment variable name, e.g. "MOCKULUS_COUCHBASE_CONNSTR"
	Doc    string // description, from the `doc` tag
	DocRow string // display key for the generated table; defaults to Path
	Def    string // default value, from the `default` tag
	Secret bool   // redact in dumps; also honours the `_FILE` env variant
	index  []int  // field index path for reflect.Value.FieldByIndex
}

// fields returns every configuration key in declaration order.
func fields() []fieldMeta {
	var out []fieldMeta
	collectFields(reflect.TypeOf(Config{}), nil, "", &out)
	return out
}

func collectFields(t reflect.Type, index []int, prefix string, out *[]fieldMeta) {
	for i := range t.NumField() {
		sf := t.Field(i)
		name := sf.Tag.Get("yaml")
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		idx := append(append([]int{}, index...), i)

		if sf.Type.Kind() == reflect.Struct && !isLeafType(sf.Type) {
			collectFields(sf.Type, idx, path, out)
			continue
		}

		docRow := sf.Tag.Get("docrow")
		if docRow == "" {
			docRow = path
		}
		*out = append(*out, fieldMeta{
			Path:   path,
			Env:    EnvPrefix + strings.ToUpper(strings.ReplaceAll(path, ".", "_")),
			Doc:    sf.Tag.Get("doc"),
			DocRow: docRow,
			Def:    sf.Tag.Get("default"),
			Secret: sf.Tag.Get("secret") == "true",
			index:  idx,
		})
	}
}

// isLeafType reports whether a struct type is a scalar configuration value
// rather than a nested section.
func isLeafType(t reflect.Type) bool {
	switch t {
	case reflect.TypeOf(Duration(0)), reflect.TypeOf(Bytes(0)):
		return true
	default:
		return false
	}
}

func applyDefaults(c *Config) error {
	v := reflect.ValueOf(c).Elem()
	for _, f := range fields() {
		if f.Def == "" {
			continue
		}
		if err := setField(v.FieldByIndex(f.index), f.Def); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
	}
	return nil
}

func applyYAML(c *Config, doc map[string]string) error {
	byPath := make(map[string]fieldMeta, len(doc))
	for _, f := range fields() {
		byPath[f.Path] = f
	}
	v := reflect.ValueOf(c).Elem()

	// Report every unknown or invalid key at once (P3: fail loudly, completely).
	var problems []string
	for _, path := range sortedKeys(doc) {
		f, ok := byPath[path]
		if !ok {
			problems = append(problems, fmt.Sprintf("unknown key %q", path))
			continue
		}
		if err := setField(v.FieldByIndex(f.index), doc[path]); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

func applyEnv(c *Config, lookupEnv func(string) (string, bool)) error {
	v := reflect.ValueOf(c).Elem()
	var problems []string
	for _, f := range fields() {
		raw, ok := lookupEnv(f.Env)
		if !ok && f.Secret {
			// Mounted-secret form: MOCKULUS_..._FILE points at a file whose
			// contents are the value (SPEC §13, §17).
			if path, fileOK := lookupEnv(f.Env + "_FILE"); fileOK {
				data, err := os.ReadFile(path)
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s_FILE: %v", f.Env, err))
					continue
				}
				raw, ok = strings.TrimRight(string(data), "\r\n"), true
			}
		}
		if !ok {
			continue
		}
		if err := setField(v.FieldByIndex(f.index), raw); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", f.Env, err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid environment configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func setField(dst reflect.Value, raw string) error {
	switch dst.Type() {
	case reflect.TypeOf(Duration(0)):
		var d Duration
		if err := d.parse(raw); err != nil {
			return err
		}
		dst.Set(reflect.ValueOf(d))
		return nil
	case reflect.TypeOf(Bytes(0)):
		var b Bytes
		if err := b.parse(raw); err != nil {
			return err
		}
		dst.Set(reflect.ValueOf(b))
		return nil
	}

	switch dst.Kind() {
	case reflect.String:
		dst.SetString(raw)
		return nil
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid boolean %q (want true or false)", raw)
		}
		dst.SetBool(b)
		return nil
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer %q", raw)
		}
		dst.SetInt(n)
		return nil
	default:
		return fmt.Errorf("unsupported configuration field kind %s", dst.Kind())
	}
}

// Dump renders the resolved configuration as `key=value` lines with secrets
// replaced by a fixed marker. Logged at debug level only (SPEC §14.2).
func (c Config) Dump() []string {
	v := reflect.ValueOf(c)
	out := make([]string, 0, len(fields()))
	for _, f := range fields() {
		val := renderValue(v.FieldByIndex(f.index))
		if f.Secret && val != "" {
			val = "[redacted]"
		}
		out = append(out, f.Path+"="+val)
	}
	return out
}

func renderValue(v reflect.Value) string {
	switch tv := v.Interface().(type) {
	case Duration:
		return tv.String()
	case Bytes:
		return tv.String()
	case bool:
		return strconv.FormatBool(tv)
	case int:
		return strconv.Itoa(tv)
	case string:
		return tv
	default:
		return fmt.Sprint(tv)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
