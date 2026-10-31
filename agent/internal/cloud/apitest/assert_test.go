package apitest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A is an argument / JSON object, L a JSON array.
type (
	A = map[string]any
	L = []any
)

type undefinedT struct{}

// Undefined is the value of a missing key: Eq(t, j.At("x"), Undefined) is assert.equal(j.x, undefined) for a key that must
// be absent, Eq(t, j.At("x"), nil) for one that must be null.
var Undefined = undefinedT{}

// J is a decoded JSON value with lookups that never panic: a missing path gives Undefined.
type J struct{ V any }

// Plain turns anything JSON-shaped (J, *doc.Map, structs, map[string]string, ints …) into plain decoded JSON.
func Plain(v any) any {
	switch x := v.(type) {
	case J:
		return x.V
	case undefinedT:
		return x
	case []J:
		out := make([]any, len(x))
		for i := range x {
			out[i] = Plain(x[i])
		}
		return out
	case nil, string, bool, float64:
		return x
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("Plain %T: %v", v, err))
	}
	var o any
	if err := json.Unmarshal(b, &o); err != nil {
		panic(err)
	}
	return o
}

// JOf wraps a JSON-shaped value.
func JOf(v any) J { return J{Plain(v)} }

// ParseJ decodes JSON text.
func ParseJ(b []byte) J {
	var o any
	if err := json.Unmarshal(b, &o); err != nil {
		return J{Undefined}
	}
	return J{o}
}

// At walks "a.b.0.c" (numbers index arrays; "-1" is the last element). Keys with a dot: Key.
func (j J) At(path string) J {
	cur := j
	for _, p := range strings.Split(path, ".") {
		cur = cur.step(p)
	}
	return cur
}

// Key is one object key, taken literally.
func (j J) Key(k string) J {
	if m, ok := j.V.(map[string]any); ok {
		if v, ok := m[k]; ok {
			return J{v}
		}
	}
	return J{Undefined}
}

func (j J) step(p string) J {
	if l, ok := j.V.([]any); ok {
		if n, err := strconv.Atoi(p); err == nil {
			if n < 0 {
				n += len(l)
			}
			if n >= 0 && n < len(l) {
				return J{l[n]}
			}
		}
		return J{Undefined}
	}
	return j.Key(p)
}

// Str is the string (or "" when it is none); Num the number (0); Bool the bool (false); Int the number as int.
func (j J) Str() string       { s, _ := j.V.(string); return s }
func (j J) Num() float64      { n, _ := j.V.(float64); return n }
func (j J) Int() int          { return int(j.Num()) }
func (j J) Bool() bool        { b, _ := j.V.(bool); return b }
func (j J) IsNull() bool      { return j.V == nil }
func (j J) Exists() bool      { _, u := j.V.(undefinedT); return !u }
func (j J) IsNil() bool       { return j.V == nil || !j.Exists() } // null or undefined
func (j J) Truthy() bool      { return truthy(j.V) }
func (j J) Has(k string) bool { return j.Key(k).Exists() }

// Len is the length of an array, object or string (0 otherwise).
func (j J) Len() int {
	switch x := j.V.(type) {
	case []any:
		return len(x)
	case map[string]any:
		return len(x)
	case string:
		return len(x)
	}
	return 0
}

// Arr lists the elements of an array.
func (j J) Arr() []J {
	l, _ := j.V.([]any)
	out := make([]J, len(l))
	for i := range l {
		out[i] = J{l[i]}
	}
	return out
}

// Keys lists an object's keys, sorted.
func (j J) Keys() []string {
	m, _ := j.V.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Find is array.find(x => x[path] === val); Undefined when nothing matches.
func (j J) Find(path string, val any) J {
	want := Plain(val)
	for _, e := range j.Arr() {
		if reflect.DeepEqual(e.At(path).V, want) {
			return e
		}
	}
	return J{Undefined}
}

// FindF is array.find(fn).
func (j J) FindF(fn func(J) bool) J {
	for _, e := range j.Arr() {
		if fn(e) {
			return e
		}
	}
	return J{Undefined}
}

// Filter is array.filter(fn).
func (j J) Filter(fn func(J) bool) []J {
	out := []J{}
	for _, e := range j.Arr() {
		if fn(e) {
			out = append(out, e)
		}
	}
	return out
}

// Pluck is array.map(x => x[path]).
func (j J) Pluck(path string) []any {
	out := []any{}
	for _, e := range j.Arr() {
		out = append(out, e.At(path).V)
	}
	return out
}

// Clone deep-copies the value (a mutable map / slice for building request bodies).
func (j J) Clone() J { return JOf(j.V) }

// Map is the object for editing (nil when it is none); changes show through j.
func (j J) Map() map[string]any { m, _ := j.V.(map[string]any); return m }

// MarshalJSON lets a J sit anywhere in a request body or document (Undefined = null).
func (j J) MarshalJSON() ([]byte, error) {
	if !j.Exists() {
		return []byte("null"), nil
	}
	return json.Marshal(j.V)
}

// String is the JSON text.
func (j J) String() string {
	if !j.Exists() {
		return "undefined"
	}
	b, _ := json.Marshal(j.V)
	return string(b)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil, undefinedT:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	}
	return true
}

func msgOf(msg []any) string {
	if len(msg) == 0 {
		return ""
	}
	if f, ok := msg[0].(string); ok && strings.Contains(f, "%") {
		return fmt.Sprintf(f, msg[1:]...) + ": "
	}
	return fmt.Sprintln(msg...) + ": "
}

func show(v any) string {
	if _, u := v.(undefinedT); u {
		return "undefined"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// Eq is assert.deepEqual / strictEqual: both sides as plain JSON, Undefined apart from null.
func Eq(t testing.TB, got, want any, msg ...any) {
	t.Helper()
	g, w := Plain(got), Plain(want)
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%snot equal\n got %s\nwant %s", msgOf(msg), show(g), show(w))
	}
}

// NotEq is assert.notEqual / notDeepEqual.
func NotEq(t testing.TB, got, other any, msg ...any) {
	t.Helper()
	g, w := Plain(got), Plain(other)
	if reflect.DeepEqual(g, w) {
		t.Fatalf("%sexpected a different value than %s", msgOf(msg), show(w))
	}
}

// True is assert.ok.
func True(t testing.TB, cond bool, msg ...any) {
	t.Helper()
	if !cond {
		t.Fatalf("%sexpected true", msgOf(msg))
	}
}

// False is assert.ok(!x).
func False(t testing.TB, cond bool, msg ...any) {
	t.Helper()
	if cond {
		t.Fatalf("%sexpected false", msgOf(msg))
	}
}

// Match is assert.match; JS regexes port as Go regexes (use (?m) / (?s) where the JS had m / s).
func Match(t testing.TB, s any, re string, msg ...any) {
	t.Helper()
	str := textOf(s)
	if !regexp.MustCompile(re).MatchString(str) {
		t.Fatalf("%sno match for /%s/ in:\n%s", msgOf(msg), re, str)
	}
}

// NoMatch is assert.doesNotMatch.
func NoMatch(t testing.TB, s any, re string, msg ...any) {
	t.Helper()
	str := textOf(s)
	if m := regexp.MustCompile(re).FindString(str); m != "" {
		t.Fatalf("%sunexpected match %q for /%s/ in:\n%s", msgOf(msg), m, re, str)
	}
}

// Contains is assert.ok(s.includes(sub)).
func Contains(t testing.TB, s any, sub string, msg ...any) {
	t.Helper()
	str := textOf(s)
	if !strings.Contains(str, sub) {
		t.Fatalf("%s%q is not in:\n%s", msgOf(msg), sub, str)
	}
}

// NotContains is assert.ok(!s.includes(sub)).
func NotContains(t testing.TB, s any, sub string, msg ...any) {
	t.Helper()
	str := textOf(s)
	if strings.Contains(str, sub) {
		t.Fatalf("%sunexpected %q in:\n%s", msgOf(msg), sub, str)
	}
}

func textOf(s any) string {
	switch x := s.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case J:
		if str, ok := x.V.(string); ok {
			return str
		}
		return x.String()
	}
	return fmt.Sprint(s)
}

// NoErr fails on an error.
func NoErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
