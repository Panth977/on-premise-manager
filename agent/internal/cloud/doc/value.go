// Package doc holds the schemaless document values of the cloud (the Go side of what JSON.parse gives the TS code):
//
//	null → nil · boolean → bool · number → float64 · string → string · array → []any · object → *Map
//	Timestamp → Timestamp · Buffer → Bytes
//
// *Map keeps key order with the semantics of a JS object (integer-like keys first, ascending; an overwritten key keeps
// its position; delete + set moves it to the end), so a document read, changed and written or sent as JSON gets the same
// bytes as in TS. Marshal is JSON.stringify, Parse is JSON.parse.
package doc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Map is an ordered JSON object. A nil *Map is an empty read-only object (reads are nil-safe).
type Map struct {
	keys []string
	vals map[string]any
}

// New returns an empty map.
func New() *Map { return &Map{vals: map[string]any{}} }

// Obj builds a map from key, value pairs in order: doc.Obj("a", 1, "b", "x").
func Obj(kv ...any) *Map {
	if len(kv)%2 != 0 {
		panic("doc.Obj: odd number of arguments")
	}
	m := New()
	for i := 0; i < len(kv); i += 2 {
		m.Set(kv[i].(string), kv[i+1])
	}
	return m
}

// indexKey reports a canonical array index ("0", "17"; JS orders these first, ascending).
func indexKey(k string) (uint64, bool) {
	if k == "" || len(k) > 10 || (k[0] == '0' && len(k) > 1) {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, n < 4294967295
}

// Set sets a key (an existing key keeps its position) and returns the map.
func (m *Map) Set(k string, v any) *Map {
	if m.vals == nil {
		m.vals = map[string]any{}
	}
	if _, ok := m.vals[k]; ok {
		m.vals[k] = v
		return m
	}
	m.vals[k] = v
	if n, ok := indexKey(k); ok {
		i := sort.Search(len(m.keys), func(i int) bool {
			x, isIdx := indexKey(m.keys[i])
			return !isIdx || x > n
		})
		m.keys = append(m.keys, "")
		copy(m.keys[i+1:], m.keys[i:])
		m.keys[i] = k
		return m
	}
	m.keys = append(m.keys, k)
	return m
}

// Get returns a value and whether the key exists.
func (m *Map) Get(k string) (any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m.vals[k]
	return v, ok
}

// Val returns a value, nil when missing.
func (m *Map) Val(k string) any {
	v, _ := m.Get(k)
	return v
}

// Has reports whether a key exists.
func (m *Map) Has(k string) bool {
	_, ok := m.Get(k)
	return ok
}

// Delete removes a key and returns the map.
func (m *Map) Delete(ks ...string) *Map {
	if m == nil {
		return m
	}
	for _, k := range ks {
		if _, ok := m.vals[k]; !ok {
			continue
		}
		delete(m.vals, k)
		for i, x := range m.keys {
			if x == k {
				m.keys = append(m.keys[:i], m.keys[i+1:]...)
				break
			}
		}
	}
	return m
}

// Keys returns the keys in order (a copy).
func (m *Map) Keys() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.keys...)
}

// Len is the number of keys.
func (m *Map) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// Each calls fn for every key in order (fn may not change the map).
func (m *Map) Each(fn func(k string, v any)) {
	if m == nil {
		return
	}
	for _, k := range m.keys {
		fn(k, m.vals[k])
	}
}

// Str returns a string value, "" when missing or not a string.
func (m *Map) Str(k string) string {
	s, _ := m.Val(k).(string)
	return s
}

// StrOK returns a string value and whether the value is a string.
func (m *Map) StrOK(k string) (string, bool) {
	s, ok := m.Val(k).(string)
	return s, ok
}

// StrPtr returns a string value, nil when missing or not a string (string | null fields).
func (m *Map) StrPtr(k string) *string {
	if s, ok := m.Val(k).(string); ok {
		return &s
	}
	return nil
}

// Bool returns a bool value, false when missing or not a bool.
func (m *Map) Bool(k string) bool {
	b, _ := m.Val(k).(bool)
	return b
}

// Truthy is JS !!value of a field.
func (m *Map) Truthy(k string) bool { return Truthy(m.Val(k)) }

// Num returns a number value, 0 when missing or not a number.
func (m *Map) Num(k string) float64 {
	f, _ := AsNumber(m.Val(k))
	return f
}

// NumOK returns a number value and whether the value is a number.
func (m *Map) NumOK(k string) (float64, bool) { return AsNumber(m.Val(k)) }

// Int returns a number value as int64.
func (m *Map) Int(k string) int64 { return int64(m.Num(k)) }

// Map returns a nested map, nil when missing or not an object.
func (m *Map) Map(k string) *Map {
	x, _ := m.Val(k).(*Map)
	return x
}

// Arr returns an array value, nil when missing or not an array.
func (m *Map) Arr(k string) []any {
	x, _ := m.Val(k).([]any)
	return x
}

// Strs returns the string items of an array field.
func (m *Map) Strs(k string) []string {
	var out []string
	for _, v := range m.Arr(k) {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Path returns the value at a dotted field path (a.b.c), like getField of the driver.
func (m *Map) Path(field string) (any, bool) {
	var cur any = m
	for _, s := range strings.Split(field, ".") {
		x, ok := cur.(*Map)
		if !ok || x == nil {
			return nil, false
		}
		if cur, ok = x.Get(s); !ok {
			return nil, false
		}
	}
	return cur, true
}

// Clone is a deep copy (Timestamp and Bytes kept).
func (m *Map) Clone() *Map {
	if m == nil {
		return nil
	}
	return CloneValue(m).(*Map)
}

// CloneValue deep-copies a document value.
func CloneValue(v any) any {
	switch x := v.(type) {
	case *Map:
		if x == nil {
			return (*Map)(nil)
		}
		o := &Map{keys: append(make([]string, 0, len(x.keys)), x.keys...), vals: make(map[string]any, len(x.vals))}
		for k, y := range x.vals {
			o.vals[k] = CloneValue(y)
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, y := range x {
			o[i] = CloneValue(y)
		}
		return o
	case Bytes:
		return append(Bytes(nil), x...)
	}
	return v
}

// Spread copies the top-level entries of o into m ({ ...m, ...o }) and returns m.
func (m *Map) Spread(o *Map) *Map {
	o.Each(func(k string, v any) { m.Set(k, v) })
	return m
}

// Shallow is { ...m } (a new map sharing the values).
func (m *Map) Shallow() *Map { return New().Spread(m) }

// Without is { ...m } without the given keys.
func (m *Map) Without(ks ...string) *Map { return m.Shallow().Delete(ks...) }

// JSValue makes a type marshal as a document value (Marshal, From).
type JSValue interface{ JSValue() any }

// Undefined in a map or passed to From is a missing value: dropped from objects, null in arrays (JSON.stringify).
var Undefined = undefinedT{}

type undefinedT struct{}

// Timestamp is the driver's point in time ({"$timestamp":{seconds,nanos}} when stored).
type Timestamp struct {
	Seconds int64
	Nanos   int64
}

// TimestampFromMillis is Timestamp.fromMillis.
func TimestampFromMillis(ms int64) Timestamp {
	s := ms / 1000
	if ms%1000 < 0 {
		s--
	}
	return Timestamp{Seconds: s, Nanos: (ms - s*1000) * 1_000_000}
}

// Now is Timestamp.now (millisecond precision).
func Now() Timestamp { return TimestampFromMillis(time.Now().UnixMilli()) }

// Millis is toMillis.
func (t Timestamp) Millis() int64 { return t.Seconds*1000 + t.Nanos/1_000_000 }

// Time is toDate.
func (t Timestamp) Time() time.Time { return time.UnixMilli(t.Millis()) }

// MarshalJSON is Timestamp.toJSON (what JSON.stringify gives an API client).
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`{"_seconds":%d,"_nanoseconds":%d}`, t.Seconds, t.Nanos)), nil
}

// Bytes is a Buffer ({"$bytes":"<b64>"} when stored).
type Bytes []byte

// Transform is a FieldValue write transform.
type Transform struct {
	Op      string // increment | arrayUnion | arrayRemove | delete | serverTimestamp
	Operand any
}

// Increment is FieldValue.increment.
func Increment(n float64) *Transform { return &Transform{Op: "increment", Operand: n} }

// ArrayUnion is FieldValue.arrayUnion.
func ArrayUnion(v ...any) *Transform { return &Transform{Op: "arrayUnion", Operand: v} }

// ArrayRemove is FieldValue.arrayRemove.
func ArrayRemove(v ...any) *Transform { return &Transform{Op: "arrayRemove", Operand: v} }

// DeleteField is FieldValue.delete.
func DeleteField() *Transform { return &Transform{Op: "delete"} }

// ServerTimestamp is FieldValue.serverTimestamp.
func ServerTimestamp() *Transform { return &Transform{Op: "serverTimestamp"} }

// AsNumber returns the float64 of any Go number.
func AsNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return f, err == nil
	}
	return 0, false
}

// Truthy is JS Boolean(v) of a document value.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil, undefinedT:
		return false
	case bool:
		return x
	case string:
		return x != ""
	}
	if f, ok := AsNumber(v); ok {
		return f != 0 && f == f
	}
	return true
}

// From normalizes a Go value into document values: numbers become float64, map[string]any a *Map with sorted keys, any
// other type its JSON (a Timestamp field of a struct stays a Timestamp, a []byte field does not: use a Map with Bytes).
// Transforms stay in place, Undefined map values are dropped.
func From(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case bool, string, Timestamp, *Transform, undefinedT:
		return v, nil
	case float64:
		return x, nil
	case Bytes:
		return x, nil
	case []byte:
		return Bytes(x), nil
	case time.Time:
		return TimestampFromMillis(x.UnixMilli()), nil
	case *Map:
		if x == nil {
			return nil, nil
		}
		o := New()
		for _, k := range x.keys {
			y := x.vals[k]
			if _, un := y.(undefinedT); un {
				continue
			}
			z, err := From(y)
			if err != nil {
				return nil, err
			}
			o.Set(k, z)
		}
		return o, nil
	case []any:
		o := make([]any, len(x))
		for i, y := range x {
			z, err := From(y)
			if err != nil {
				return nil, err
			}
			o[i] = z
		}
		return o, nil
	case []string:
		o := make([]any, len(x))
		for i, y := range x {
			o[i] = y
		}
		return o, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := New()
		for _, k := range keys {
			if _, un := x[k].(undefinedT); un {
				continue
			}
			z, err := From(x[k])
			if err != nil {
				return nil, err
			}
			o.Set(k, z)
		}
		return o, nil
	case JSValue:
		return From(x.JSValue())
	case json.RawMessage:
		return Parse(x)
	}
	if f, ok := AsNumber(v); ok {
		return f, nil
	}
	rv := reflect.ValueOf(v)
	if (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map) && rv.IsNil() {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	parsed, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	return restoreTimestamps(parsed), nil
}

// restoreTimestamps undoes Timestamp.MarshalJSON after a struct went through encoding/json.
func restoreTimestamps(v any) any {
	switch x := v.(type) {
	case *Map:
		if x.Len() == 2 && x.Has("_seconds") && x.Has("_nanoseconds") {
			return Timestamp{Seconds: x.Int("_seconds"), Nanos: x.Int("_nanoseconds")}
		}
		for _, k := range x.keys {
			x.vals[k] = restoreTimestamps(x.vals[k])
		}
	case []any:
		for i := range x {
			x[i] = restoreTimestamps(x[i])
		}
	}
	return v
}

// ToMap normalizes an object-like Go value into a *Map (see From).
func ToMap(v any) (*Map, error) {
	x, err := From(v)
	if err != nil {
		return nil, err
	}
	if x == nil {
		return New(), nil
	}
	m, ok := x.(*Map)
	if !ok {
		return nil, fmt.Errorf("doc: %T is not an object", v)
	}
	return m, nil
}
