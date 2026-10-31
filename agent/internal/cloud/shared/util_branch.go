package shared

import (
	"errors"
	"math"
	"sort"
	"strings"
	"unicode/utf16"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud/doc"
)

// Helpers shared by branch.go, diff.go and template_sync.go: the JS semantics the TS code relies on, on doc values.

// Slot is one side of an item (TS Slot / SyncSlot); Has=false: the item does not exist there.
type Slot struct {
	Has bool
	V   any
}

// brNone is TS NONE.
var brNone = Slot{}

func brIsUndef(v any) bool { return v == any(doc.Undefined) }

// brSlot is TS slot(v): undefined = absent.
func brSlot(v any) Slot {
	if brIsUndef(v) {
		return brNone
	}
	return Slot{Has: true, V: v}
}

// brUnd is TS m[k]: doc.Undefined when the key is missing (m nil = undefined too).
func brUnd(m *doc.Map, k string) any {
	if v, ok := m.Get(k); ok {
		return v
	}
	return doc.Undefined
}

// brNullish is TS v ?? null.
func brNullish(v any) any {
	if brIsUndef(v) {
		return nil
	}
	return v
}

// brNul is TS m[k] ?? null.
func brNul(m *doc.Map, k string) any { return brNullish(m.Val(k)) }

func brIsNullish(v any) bool { return v == nil || brIsUndef(v) }

// brEmptyVal is v === null || v === undefined || v === ”.
func brEmptyVal(v any) bool { return brIsNullish(v) || v == any("") }

// brIsObject is v && typeof v === 'object'.
func brIsObject(v any) bool {
	switch x := v.(type) {
	case *doc.Map:
		return x != nil
	case []any:
		return true
	}
	return false
}

// brEq is ===.
func brEq(a, b any) bool {
	switch x := a.(type) {
	case *doc.Map:
		y, ok := b.(*doc.Map)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		return ok && len(x) > 0 && len(x) == len(y) && &x[0] == &y[0]
	}
	if fa, ok := doc.AsNumber(a); ok {
		fb, ok := doc.AsNumber(b)
		return ok && fa == fb
	}
	switch b.(type) {
	case *doc.Map, []any:
		return false
	}
	return a == b
}

// brJSString is String(v) (also `${v}`).
func brJSString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case *doc.Map:
		if x == nil {
			return "null"
		}
		return "[object Object]"
	case []any:
		parts := make([]string, len(x))
		for i, y := range x {
			if !brIsNullish(y) {
				parts[i] = brJSString(y)
			}
		}
		return strings.Join(parts, ",")
	}
	if brIsUndef(v) {
		return "undefined"
	}
	if f, ok := doc.AsNumber(v); ok {
		return brNumString(f)
	}
	return doc.MarshalString(v)
}

func brNumString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return canonical.MustFormatNumber(f)
}

func brLessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

func brSortStrings(l []string) {
	sort.SliceStable(l, func(i, j int) bool { return brLessUTF16(l[i], l[j]) })
}

// brClone is TS clone: JSON.parse(JSON.stringify(v)), undefined stays undefined.
func brClone(v any) any {
	switch x := v.(type) {
	case nil, bool, string:
		return v
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		if x == 0 {
			return 0.0
		}
		return x
	case *doc.Map:
		if x == nil {
			return nil
		}
		o := doc.New()
		x.Each(func(k string, y any) {
			if !brIsUndef(y) {
				o.Set(k, brClone(y))
			}
		})
		return o
	case []any:
		o := make([]any, len(x))
		for i, y := range x {
			if !brIsUndef(y) {
				o[i] = brClone(y)
			}
		}
		return o
	}
	if brIsUndef(v) {
		return v
	}
	b, err := doc.Marshal(v)
	if err != nil {
		return nil
	}
	y, err := doc.Parse(b)
	if err != nil {
		return nil
	}
	return y
}

// brCloneMap is brClone of an object.
func brCloneMap(m *doc.Map) *doc.Map {
	c, _ := brClone(m).(*doc.Map)
	return c
}

// brCanonicalize is TS canonicalize on doc values, the error texts are those of CanonicalError.
func brCanonicalize(v any) (string, error) {
	var b strings.Builder
	if err := brSer(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func brSer(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(brJSString(x))
	case string:
		b.WriteString(doc.Quote(x))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return errors.New("not a finite number: " + brNumString(x))
		}
		b.WriteString(canonical.MustFormatNumber(x))
	case []any:
		b.WriteByte('[')
		for i, y := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if brIsUndef(y) {
				y = nil
			}
			if err := brSer(b, y); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case *doc.Map:
		if x == nil {
			b.WriteString("null")
			return nil
		}
		keys := make([]string, 0, x.Len())
		for _, k := range x.Keys() {
			if !brIsUndef(x.Val(k)) {
				keys = append(keys, k)
			}
		}
		brSortStrings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(doc.Quote(k))
			b.WriteByte(':')
			if err := brSer(b, x.Val(k)); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		if brIsUndef(v) {
			return errors.New("cannot canonicalize undefined")
		}
		if f, ok := doc.AsNumber(v); ok {
			return brSer(b, f)
		}
		y := brClone(v)
		switch y.(type) {
		case nil, bool, string, float64, []any, *doc.Map:
			return brSer(b, y)
		}
		return errors.New("cannot canonicalize object")
	}
	return nil
}

// brCanon is TS canon: canonicalize, JSON.stringify when that throws.
func brCanon(v any) string {
	s, err := brCanonicalize(v)
	if err != nil {
		if brIsUndef(v) {
			return "undefined"
		}
		return doc.MarshalString(v)
	}
	return s
}

func brSame(a, b Slot) bool { return a.Has == b.Has && (!a.Has || brCanon(a.V) == brCanon(b.V)) }

func brIsScalar(v any) bool {
	switch v.(type) {
	case nil, string, bool:
		return true
	}
	_, ok := doc.AsNumber(v)
	return ok
}

// brCyrb53 is the hash of TS itemHash / templateSyncHash.
func brCyrb53(str string) string {
	h1, h2 := uint32(0xdeadbeef), uint32(0x41c6ce57)
	for _, ch := range utf16.Encode([]rune(str)) {
		h1 = (h1 ^ uint32(ch)) * 2654435761
		h2 = (h2 ^ uint32(ch)) * 1597334677
	}
	h1 = ((h1 ^ (h1 >> 16)) * 2246822507) ^ ((h2 ^ (h2 >> 13)) * 3266489909)
	h2 = ((h2 ^ (h2 >> 16)) * 2246822507) ^ ((h1 ^ (h1 >> 13)) * 3266489909)
	const hexd = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 0; i < 8; i++ {
		out[i] = hexd[(h2>>(28-4*uint(i)))&15]
		out[8+i] = hexd[(h1>>(28-4*uint(i)))&15]
	}
	return string(out)
}

// brSlotHash is itemHash of a Slot | unknown argument.
func brSlotHash(s any) string {
	var x Slot
	switch y := s.(type) {
	case Slot:
		x = y
	case *doc.Map:
		if y != nil && y.Has("has") && y.Has("v") {
			x = Slot{Has: doc.Truthy(y.Val("has")), V: brUnd(y, "v")}
		} else {
			x = brSlot(s)
		}
	default:
		x = brSlot(s)
	}
	if !x.Has {
		return "-"
	}
	return brCyrb53(brCanon(x.V))
}

// ── keyed lists

type brKeyOf func(any) string

// brField is (x) => x.<field> of a keyed list.
func brField(f string) brKeyOf {
	return func(x any) string {
		m, _ := x.(*doc.Map)
		return brJSString(brUnd(m, f))
	}
}

func brSelf(x any) string { return brJSString(x) }

type brKeyed map[string]any

// get is Map.get (doc.Undefined when missing).
func (m brKeyed) get(k string) any {
	if v, ok := m[k]; ok {
		return v
	}
	return doc.Undefined
}

func brByKey(list []any, keyOf brKeyOf) brKeyed {
	m := brKeyed{}
	for _, x := range list {
		m[keyOf(x)] = x
	}
	return m
}

func brKeysOf(keyOf brKeyOf, lists ...[]any) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, x := range l {
			if k := keyOf(x); !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

func brOrder(list []any, keyOf brKeyOf) []string {
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = keyOf(x)
	}
	return out
}

func brIndex(list []any, keyOf brKeyOf, key string) int {
	for i, x := range list {
		if keyOf(x) == key {
			return i
		}
	}
	return -1
}

// brUpsert inserts / replaces / removes (value doc.Undefined) one keyed item; new items go after their predecessor in
// the source order. It returns the list, the caller stores it back.
func brUpsert(list []any, keyOf brKeyOf, key string, value any, order []string) []any {
	i := brIndex(list, keyOf, key)
	if brIsUndef(value) {
		if i >= 0 {
			list = append(list[:i:i], list[i+1:]...)
		}
		return list
	}
	if i >= 0 {
		list[i] = value
		return list
	}
	insert := func(pos int) []any {
		out := make([]any, 0, len(list)+1)
		out = append(out, list[:pos]...)
		out = append(out, value)
		return append(out, list[pos:]...)
	}
	at := -1
	for j, k := range order {
		if k == key {
			at = j
			break
		}
	}
	if at >= 0 {
		for k := at - 1; k >= 0; k-- {
			if j := brIndex(list, keyOf, order[k]); j >= 0 {
				return insert(j + 1)
			}
		}
		for k := at + 1; k < len(order); k++ {
			if j := brIndex(list, keyOf, order[k]); j >= 0 {
				return insert(j)
			}
		}
	}
	return append(list[:len(list):len(list)], value)
}

// brFind is list.find((x) => x.<f> === key).
func brFind(list []any, f, key string) *doc.Map {
	if i := brIndex(list, brField(f), key); i >= 0 {
		m, _ := list[i].(*doc.Map)
		return m
	}
	return nil
}

func brSiteOf(cfg *doc.Map, slug string) *doc.Map { return brFind(cfg.Arr("sites"), "siteSlug", slug) }

func brContainerOf(cfg *doc.Map, site, c string) *doc.Map {
	return brFind(brSiteOf(cfg, site).Map("config").Arr("containers"), "containerSlug", c)
}

func brVolumeOf(cfg *doc.Map, site, v string) *doc.Map {
	return brFind(brSiteOf(cfg, site).Map("config").Arr("volumes"), "volumeSlug", v)
}

// brListInit is (m[k] ??= []).
func brListInit(m *doc.Map, k string) []any {
	if brIsNullish(m.Val(k)) {
		l := []any{}
		m.Set(k, l)
		return l
	}
	return m.Arr(k)
}

func brEmptyConfig() *doc.Map {
	return doc.Obj("variables", []any{}, "secrets", []any{}, "files", []any{}, "sites", []any{})
}

func brStrs(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

func brContains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// brPick is an object of the listed fields in that order (a missing field stays undefined).
func brPick(m *doc.Map, keys ...string) *doc.Map {
	o := doc.New()
	for _, k := range keys {
		o.Set(k, brUnd(m, k))
	}
	return o
}

// brSecretMissing / brVariableMissing are the filters + maps of mergeMissing and templateSyncMissing.
func brMissing(cfg *doc.Map) *doc.Map {
	vars := []any{}
	for _, x := range cfg.Arr("variables") {
		v, _ := x.(*doc.Map)
		if doc.Truthy(v.Val("required")) && brEmptyVal(v.Val("value")) {
			vars = append(vars, doc.Obj("variableSlug", brUnd(v, "variableSlug"), "label", brUnd(v, "label"), "type", brUnd(v, "type"), "options", brNul(v, "options"), "required", brUnd(v, "required")))
		}
	}
	secrets := []any{}
	for _, x := range cfg.Arr("secrets") {
		s, _ := x.(*doc.Map)
		if !doc.Truthy(s.Val("required")) || doc.Truthy(s.Val("generate")) {
			continue
		}
		var missing bool
		if s.Has("isSet") {
			missing = !doc.Truthy(s.Val("isSet"))
		} else {
			missing = brEmptyVal(s.Val("value"))
		}
		if missing {
			secrets = append(secrets, doc.Obj("secretSlug", brUnd(s, "secretSlug"), "label", brUnd(s, "label")))
		}
	}
	return doc.Obj("variables", vars, "secrets", secrets)
}
