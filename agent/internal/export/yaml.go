package export

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// A tiny deterministic YAML emitter (block style; strings always double-quoted JSON-style, which YAML 1.2 reads as the
// same string; keys plain when unambiguous). Only what docker-compose.yml needs.

type ymap struct {
	keys []string
	vals map[string]any
}

func newMap() *ymap { return &ymap{vals: map[string]any{}} }

// set appends (or replaces) a key; insertion order is the output order.
func (m *ymap) set(k string, v any) *ymap {
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
	return m
}

func (m *ymap) empty() bool { return len(m.keys) == 0 }

type yseq []any

// yflow is a flow sequence of strings: ["a", "b"].
type yflow []string

// yraw is a plain scalar (numbers, booleans).
type yraw string

var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9._/-]*$`)
var ambiguous = map[string]bool{"y": true, "n": true, "yes": true, "no": true, "on": true, "off": true, "true": true, "false": true, "null": true, "~": true}

func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func key(k string) string {
	if plainKey.MatchString(k) && !ambiguous[strings.ToLower(k)] {
		return k
	}
	return quote(k)
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return quote(x), true
	case yraw:
		return string(x), true
	case yflow:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = quote(s)
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	case *ymap:
		if x.empty() {
			return "{}", true
		}
	case yseq:
		if len(x) == 0 {
			return "[]", true
		}
	}
	return "", false
}

func emitMap(b *strings.Builder, m *ymap, ind int) {
	pad := strings.Repeat(" ", ind)
	for _, k := range m.keys {
		v := m.vals[k]
		if s, ok := scalar(v); ok {
			b.WriteString(pad + key(k) + ": " + s + "\n")
			continue
		}
		b.WriteString(pad + key(k) + ":\n")
		emit(b, v, ind+2)
	}
}

func emitSeq(b *strings.Builder, s yseq, ind int) {
	pad := strings.Repeat(" ", ind)
	for _, it := range s {
		if sc, ok := scalar(it); ok {
			b.WriteString(pad + "- " + sc + "\n")
			continue
		}
		switch x := it.(type) {
		case *ymap:
			var sub strings.Builder
			emitMap(&sub, x, ind+2)
			lines := sub.String()
			b.WriteString(pad + "- " + strings.TrimPrefix(lines, pad+"  "))
		case yseq:
			b.WriteString(pad + "-\n")
			emitSeq(b, x, ind+2)
		}
	}
}

func emit(b *strings.Builder, v any, ind int) {
	switch x := v.(type) {
	case *ymap:
		emitMap(b, x, ind)
	case yseq:
		emitSeq(b, x, ind)
	}
}

// yamlDoc renders a document (header comment lines + the root map).
func yamlDoc(header []string, root *ymap) string {
	var b strings.Builder
	for _, h := range header {
		if h == "" {
			b.WriteString("#\n")
		} else {
			b.WriteString("# " + h + "\n")
		}
	}
	emitMap(&b, root, 0)
	return b.String()
}
