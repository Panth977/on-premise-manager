package driver

import (
	"fmt"
	"regexp"
	"strings"

	"opm/agent/internal/cloud/doc"
)

// decode is `dec` of codec.ts over a parsed tree: {"$timestamp":…} and {"$bytes":…} single-key objects become values.
func decode(v any) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = decode(x[i])
		}
		return x
	case *doc.Map:
		if x.Len() == 1 {
			if t := x.Map("$timestamp"); t != nil {
				s, okS := t.NumOK("seconds")
				n, okN := t.NumOK("nanos")
				if okS && okN {
					return doc.Timestamp{Seconds: int64(s), Nanos: int64(n)}
				}
			}
			if b, ok := x.StrOK("$bytes"); ok {
				return doc.Bytes(doc.B64Decode(b))
			}
		}
		for _, k := range x.Keys() {
			y, _ := x.Get(k)
			x.Set(k, decode(y))
		}
		return x
	}
	return v
}

// DecodeDoc is decodeDoc: stored JSON → document.
func DecodeDoc(raw string) (*doc.Map, error) {
	v, err := doc.ParseString(raw)
	if err != nil {
		return nil, err
	}
	m, ok := decode(v).(*doc.Map)
	if !ok {
		return nil, fmt.Errorf("stored document is not an object")
	}
	return m, nil
}

func mustDecode(raw string) *doc.Map {
	m, err := DecodeDoc(raw)
	if err != nil {
		panic(err)
	}
	return m
}

// EncodeDoc is encodeDoc: document → stored JSON.
func EncodeDoc(d *doc.Map) (string, error) {
	s, err := doc.EncodeStored(d)
	if err != nil {
		return "", bad("%s", err.Error())
	}
	return s, nil
}

// canon is valueKey: canonical form for equality (arrayUnion / arrayRemove), sorted map keys over the stored form.
func valueKey(v any) (string, error) {
	s, err := doc.EncodeStored(v)
	if err != nil {
		return "", bad("%s", err.Error())
	}
	x, err := doc.ParseString(s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	var w func(any)
	w = func(v any) {
		switch y := v.(type) {
		case []any:
			b.WriteByte('[')
			for i, z := range y {
				if i > 0 {
					b.WriteByte(',')
				}
				w(z)
			}
			b.WriteByte(']')
		case *doc.Map:
			b.WriteByte('{')
			for i, k := range y.SortedKeys() {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(doc.Quote(k))
				b.WriteByte(':')
				w(y.Val(k))
			}
			b.WriteByte('}')
		default:
			b.WriteString(doc.MarshalString(y))
		}
	}
	w(x)
	return b.String(), nil
}

// ixOf: value of an index column (string, number as is; boolean 0|1; Timestamp epoch ms; anything else NULL).
func ixOf(v any) any {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return float64(1)
		}
		return float64(0)
	case doc.Timestamp:
		return float64(x.Millis())
	}
	if f, ok := doc.AsNumber(v); ok {
		if f != f || f > 1.7976931348623157e308 || f < -1.7976931348623157e308 {
			return nil
		}
		return f
	}
	return nil
}

type valueKind int

const (
	kString valueKind = iota
	kNumber
	kBoolean
	kTimestamp
	kNull
)

func kindOf(v any) (valueKind, error) {
	switch v.(type) {
	case nil:
		return kNull, nil
	case string:
		return kString, nil
	case bool:
		return kBoolean, nil
	case doc.Timestamp:
		return kTimestamp, nil
	}
	if _, ok := doc.AsNumber(v); ok {
		return kNumber, nil
	}
	return 0, bad("unsupported filter / cursor value %T (maps, arrays and bytes cannot be compared)", v)
}

var fieldSeg = regexp.MustCompile(`^[A-Za-z0-9_$-]+$`)

func checkField(field string) ([]string, error) {
	segs := strings.Split(field, ".")
	for _, s := range segs {
		if !fieldSeg.MatchString(s) {
			return nil, bad(`unsupported field path "%s"`, field)
		}
	}
	return segs, nil
}

func getField(d *doc.Map, field string) (any, bool) { return d.Path(field) }

func setPath(d *doc.Map, segs []string, v any) {
	cur := d
	for _, s := range segs[:len(segs)-1] {
		next := cur.Map(s)
		if next == nil {
			next = doc.New()
			cur.Set(s, next)
		}
		cur = next
	}
	cur.Set(segs[len(segs)-1], v)
}

func deletePath(d *doc.Map, segs []string) {
	cur := d
	for _, s := range segs[:len(segs)-1] {
		if cur = cur.Map(s); cur == nil {
			return
		}
	}
	cur.Delete(segs[len(segs)-1])
}

type deleteMark struct{}

// transform: the result of a FieldTransform; `has` tells a missing field from a null one.
func transform(t *doc.Transform, current any, now doc.Timestamp) (any, error) {
	switch t.Op {
	case "increment":
		c, ok := doc.AsNumber(current)
		if !ok {
			c = 0
		}
		o, _ := doc.AsNumber(t.Operand)
		return c + o, nil
	case "arrayUnion":
		cur, _ := current.([]any)
		arr := append([]any{}, cur...)
		have := map[string]bool{}
		for _, x := range arr {
			k, err := valueKey(x)
			if err != nil {
				return nil, err
			}
			have[k] = true
		}
		for _, x := range t.Operand.([]any) {
			k, err := valueKey(x)
			if err != nil {
				return nil, err
			}
			if !have[k] {
				have[k] = true
				arr = append(arr, x)
			}
		}
		return arr, nil
	case "arrayRemove":
		rm := map[string]bool{}
		for _, x := range t.Operand.([]any) {
			k, err := valueKey(x)
			if err != nil {
				return nil, err
			}
			rm[k] = true
		}
		cur, _ := current.([]any)
		out := []any{}
		for _, x := range cur {
			k, err := valueKey(x)
			if err != nil {
				return nil, err
			}
			if !rm[k] {
				out = append(out, x)
			}
		}
		return out, nil
	case "delete":
		return deleteMark{}, nil
	case "serverTimestamp":
		return now, nil
	}
	return nil, bad("unknown transform %s", t.Op)
}

// resolve: a value written at a position whose previous content is `current` (maps: transforms relative to their fields).
func resolve(v, current any, now doc.Timestamp) (any, error) {
	switch x := v.(type) {
	case *doc.Transform:
		return transform(x, current, now)
	case *doc.Map:
		o := doc.New()
		for _, k := range x.Keys() {
			y, _ := x.Get(k)
			r, err := resolve(y, nil, now)
			if err != nil {
				return nil, err
			}
			if _, del := r.(deleteMark); !del {
				o.Set(k, r)
			}
		}
		return o, nil
	}
	return v, nil
}

// applySet: set(data) replaces the document.
func applySet(data *doc.Map, now doc.Timestamp) (*doc.Map, error) {
	r, err := resolve(data, nil, now)
	if err != nil {
		return nil, err
	}
	return r.(*doc.Map), nil
}

// applyMerge: set(data, merge) deep-merges maps (an empty map is a leaf), arrays replaced.
func applyMerge(current, data *doc.Map, now doc.Timestamp) (*doc.Map, error) {
	out := doc.New()
	if current != nil {
		out = current.Clone()
	}
	var walk func(target, src *doc.Map) error
	walk = func(target, src *doc.Map) error {
		for _, k := range src.Keys() {
			v, _ := src.Get(k)
			switch x := v.(type) {
			case *doc.Transform:
				r, err := transform(x, target.Val(k), now)
				if err != nil {
					return err
				}
				if _, del := r.(deleteMark); del {
					target.Delete(k)
				} else {
					target.Set(k, r)
				}
				continue
			case *doc.Map:
				if x.Len() > 0 {
					t := target.Map(k)
					if t == nil {
						t = doc.New()
						target.Set(k, t)
					}
					if err := walk(t, x); err != nil {
						return err
					}
					continue
				}
			}
			r, err := resolve(v, nil, now)
			if err != nil {
				return err
			}
			target.Set(k, r)
		}
		return nil
	}
	if err := walk(out, data); err != nil {
		return nil, err
	}
	return out, nil
}

// applyUpdate: update({'a.b': v}) with dotted field paths; a map value replaces the field.
func applyUpdate(current, data *doc.Map, now doc.Timestamp) (*doc.Map, error) {
	out := current.Clone()
	for _, path := range data.Keys() {
		v, _ := data.Get(path)
		segs, err := checkField(path)
		if err != nil {
			return nil, err
		}
		cur, _ := getField(out, path)
		r, err := resolve(v, cur, now)
		if err != nil {
			return nil, err
		}
		if _, del := r.(deleteMark); del {
			deletePath(out, segs)
		} else {
			setPath(out, segs, r)
		}
	}
	return out, nil
}

// project: select(fields…).
func project(d *doc.Map, fields []string) *doc.Map {
	out := doc.New()
	for _, f := range fields {
		if v, ok := getField(d, f); ok {
			setPath(out, strings.Split(f, "."), v)
		}
	}
	return out
}
