// Package core holds the shared modules of the cloud plus the helpers route files import from each other, one Go
// file per TS file with the same base name (route helpers: route_<ts file>.go). Function names are the TS names with a
// capital first letter. Documents are *doc.Map, lists []any, numbers float64; `T | null` results are nil. Importing
// this package installs cloud.NormalizeStored, cloud.PrimeImageVolumes and the importImage trigger.
package core

import (
	"regexp"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
)

// IssuesOf converts the issue maps of package shared to cloud.Issue (for E.Invalid and V).
func IssuesOf(l []any) []cloud.Issue {
	out := make([]cloud.Issue, 0, len(l))
	for _, x := range l {
		m, _ := x.(*doc.Map)
		out = append(out, cloud.Issue{Path: m.Str("path"), Code: m.Str("code"), Message: m.Str("message"), Severity: m.Str("severity")})
	}
	return out
}

// present is a field value with undefined for a missing key.
func present(m *doc.Map, k string) any {
	if v, ok := m.Get(k); ok {
		return v
	}
	return doc.Undefined
}

// nullish is `v === null || v === undefined`.
func nullish(v any) bool { return v == nil || v == doc.Undefined }

// or is m[k] ?? def.
func or(m *doc.Map, k string, def any) any {
	if v, ok := m.Get(k); ok && !nullish(v) {
		return v
	}
	return def
}

// JSString is String(v) of a document value.
func JSString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case *doc.Map:
		return "[object Object]"
	case []any:
		p := make([]string, len(x))
		for i, e := range x {
			if !nullish(e) {
				p[i] = JSString(e)
			}
		}
		return strings.Join(p, ",")
	}
	if v == doc.Undefined {
		return "undefined"
	}
	return doc.MarshalString(v)
}

// strs is the []any of a string list.
func strs(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// mapsOf keeps the objects of a list.
func mapsOf(l []any) []*doc.Map {
	out := make([]*doc.Map, 0, len(l))
	for _, x := range l {
		if m, ok := x.(*doc.Map); ok && m != nil {
			out = append(out, m)
		}
	}
	return out
}

// isInt is Number.isInteger.
func isInt(v any) bool {
	f, ok := v.(float64)
	return ok && f == float64(int64(f)) && f-f == 0
}

// jsSpace is the body of a \s class of a JS regex, for use inside [...] of a Go regex.
const jsSpace = `\t\n\x0b\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var reSpaces = regexp.MustCompile(`[` + jsSpace + `]+`)

// ciRe makes a literal case-insensitive the way a JS /i regex does (a Go (?i) also folds U+017F and U+212A).
func ciRe(lit string) string {
	var b strings.Builder
	for _, r := range lit {
		if r >= 'a' && r <= 'z' {
			b.WriteString("[" + string(r) + string(r-32) + "]")
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

// Utf8 is buffer.toString('utf8').
func Utf8(b []byte) string { return strings.ToValidUTF8(string(b), "�") }

// ConfigOf is a stored config-like value as a map (nil when it is not an object).
func ConfigOf(v any) *doc.Map {
	m, _ := v.(*doc.Map)
	return m
}
