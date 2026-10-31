package doc

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"opm/agent/internal/canonical"
)

// ─────────────────────────────────────────── parse (JSON.parse)

// Parse is JSON.parse: numbers are float64, objects *Map (a repeated key keeps its first position, last value), lone
// surrogate escapes survive a round trip (kept as WTF-8 inside the Go string, written back as \udxxx by Marshal).
func Parse(data []byte) (any, error) {
	p := parser{b: data}
	p.ws()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.b) {
		return nil, p.bad()
	}
	return v, nil
}

// ParseString is Parse of a string.
func ParseString(s string) (any, error) { return Parse([]byte(s)) }

// ParseMap is Parse for a value that must be an object.
func ParseMap(data []byte) (*Map, error) {
	v, err := Parse(data)
	if err != nil {
		return nil, err
	}
	m, ok := v.(*Map)
	if !ok {
		return nil, errors.New("doc: JSON value is not an object")
	}
	return m, nil
}

type parser struct {
	b []byte
	i int
}

func (p *parser) bad() error {
	if p.i >= len(p.b) {
		return errors.New("Unexpected end of JSON input")
	}
	return fmt.Errorf("Unexpected token %q in JSON at position %d", p.b[p.i], p.i)
}
func (p *parser) ws() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}
func (p *parser) lit(s string, v any) (any, error) {
	if len(p.b)-p.i < len(s) || string(p.b[p.i:p.i+len(s)]) != s {
		return nil, p.bad()
	}
	p.i += len(s)
	return v, nil
}
func (p *parser) value() (any, error) {
	if p.i >= len(p.b) {
		return nil, p.bad()
	}
	switch c := p.b[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return p.str()
	case c == 't':
		return p.lit("true", true)
	case c == 'f':
		return p.lit("false", false)
	case c == 'n':
		return p.lit("null", nil)
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, p.bad()
}
func (p *parser) object() (any, error) {
	p.i++
	m := New()
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == '}' {
		p.i++
		return m, nil
	}
	for {
		p.ws()
		if p.i >= len(p.b) || p.b[p.i] != '"' {
			return nil, p.bad()
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.i >= len(p.b) || p.b[p.i] != ':' {
			return nil, p.bad()
		}
		p.i++
		p.ws()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		m.Set(k, v)
		p.ws()
		if p.i >= len(p.b) {
			return nil, p.bad()
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return m, nil
		default:
			return nil, p.bad()
		}
	}
}
func (p *parser) array() (any, error) {
	p.i++
	out := []any{}
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == ']' {
		p.i++
		return out, nil
	}
	for {
		p.ws()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.ws()
		if p.i >= len(p.b) {
			return nil, p.bad()
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return out, nil
		default:
			return nil, p.bad()
		}
	}
}
func hex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[:4] {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			r |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return r, true
}

// appendWTF8 appends a code point, lone surrogates as their 3-byte form (invalid UTF-8 that Marshal recognizes).
func appendWTF8(b []byte, r rune) []byte {
	if r >= 0xD800 && r <= 0xDFFF {
		return append(b, 0xE0|byte(r>>12), 0x80|byte(r>>6)&0x3F, 0x80|byte(r)&0x3F)
	}
	return utf8.AppendRune(b, r)
}
func (p *parser) str() (string, error) {
	p.i++
	start := p.i
	for p.i < len(p.b) {
		c := p.b[p.i]
		if c == '"' {
			s := string(p.b[start:p.i])
			p.i++
			return s, nil
		}
		if c == '\\' || c < 0x20 {
			break
		}
		p.i++
	}
	out := append([]byte(nil), p.b[start:p.i]...)
	for p.i < len(p.b) {
		c := p.b[p.i]
		switch {
		case c == '"':
			p.i++
			return string(out), nil
		case c < 0x20:
			return "", p.bad()
		case c != '\\':
			out = append(out, c)
			p.i++
		default:
			p.i++
			if p.i >= len(p.b) {
				return "", p.bad()
			}
			e := p.b[p.i]
			p.i++
			switch e {
			case '"', '\\', '/':
				out = append(out, e)
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				r, ok := hex4(p.b[p.i:])
				if !ok {
					return "", p.bad()
				}
				p.i += 4
				if utf16.IsSurrogate(r) && r < 0xDC00 && p.i+6 <= len(p.b) && p.b[p.i] == '\\' && p.b[p.i+1] == 'u' {
					if r2, ok := hex4(p.b[p.i+2:]); ok && r2 >= 0xDC00 && r2 <= 0xDFFF {
						r = utf16.DecodeRune(r, r2)
						p.i += 6
					}
				}
				out = appendWTF8(out, r)
			default:
				p.i--
				return "", p.bad()
			}
		}
	}
	return "", p.bad()
}
func (p *parser) number() (any, error) {
	start := p.i
	if p.b[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.b) {
		return nil, p.bad()
	}
	if p.b[p.i] == '0' {
		p.i++
	} else if p.b[p.i] >= '1' && p.b[p.i] <= '9' {
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
		}
	} else {
		return nil, p.bad()
	}
	digits := func() error {
		n := p.i
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
		}
		if p.i == n {
			return p.bad()
		}
		return nil
	}
	if p.i < len(p.b) && p.b[p.i] == '.' {
		p.i++
		if err := digits(); err != nil {
			return nil, err
		}
	}
	if p.i < len(p.b) && (p.b[p.i] == 'e' || p.b[p.i] == 'E') {
		p.i++
		if p.i < len(p.b) && (p.b[p.i] == '+' || p.b[p.i] == '-') {
			p.i++
		}
		if err := digits(); err != nil {
			return nil, err
		}
	}
	f, err := strconv.ParseFloat(string(p.b[start:p.i]), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, err
	}
	return f, nil
}

// ─────────────────────────────────────────── stringify (JSON.stringify)

type mode int

const (
	modeAPI    mode = iota // JSON.stringify: Timestamp.toJSON, Buffer.toJSON, undefined dropped, NaN → null
	modeStored             // the driver's stored JSON: $timestamp, $bytes, strict
)

// Marshal is JSON.stringify (Timestamp → {_seconds,_nanoseconds}, Bytes → {type:"Buffer",data}, time.Time → ISO).
func Marshal(v any) ([]byte, error) {
	e := encoder{m: modeAPI}
	if err := e.val(v, ""); err != nil {
		return nil, err
	}
	return e.b, nil
}

// MarshalString is Marshal as a string; it panics on a value JSON cannot hold (a programming error).
func MarshalString(v any) string {
	b, err := Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// MarshalIndent is JSON.stringify(v, null, indent).
func MarshalIndent(v any, indent int) ([]byte, error) {
	e := encoder{m: modeAPI, indent: indent}
	if err := e.val(v, ""); err != nil {
		return nil, err
	}
	return e.b, nil
}

// EncodeStored is the stored form of a value (encodeDoc of the driver). Errors name the position in the value.
func EncodeStored(v any) (string, error) {
	e := encoder{m: modeStored}
	if err := e.val(v, "doc"); err != nil {
		return "", err
	}
	return string(e.b), nil
}

// MarshalJSON writes the map like JSON.stringify.
func (m *Map) MarshalJSON() ([]byte, error) { return Marshal(m) }

// UnmarshalJSON reads an object.
func (m *Map) UnmarshalJSON(data []byte) error {
	x, err := ParseMap(data)
	if err != nil {
		return err
	}
	*m = *x
	return nil
}

type encoder struct {
	b      []byte
	m      mode
	indent int
	depth  int
}

func (e *encoder) nl() {
	if e.indent > 0 {
		e.b = append(e.b, '\n')
		for i := 0; i < e.depth*e.indent; i++ {
			e.b = append(e.b, ' ')
		}
	}
}

const hexd = "0123456789abcdef"

// Quote is JSON.stringify of a string.
func Quote(s string) string { return string(appendQuoted(nil, s)) }

func appendQuoted(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"':
				b = append(b, '\\', '"')
			case c == '\\':
				b = append(b, '\\', '\\')
			case c == '\b':
				b = append(b, '\\', 'b')
			case c == '\f':
				b = append(b, '\\', 'f')
			case c == '\n':
				b = append(b, '\\', 'n')
			case c == '\r':
				b = append(b, '\\', 'r')
			case c == '\t':
				b = append(b, '\\', 't')
			case c < 0x20:
				b = append(b, '\\', 'u', '0', '0', hexd[c>>4], hexd[c&15])
			default:
				b = append(b, c)
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			// a lone surrogate kept by Parse (WTF-8): \udxxx, like well-formed JSON.stringify
			if c == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 && s[i+1] <= 0xBF && s[i+2]&0xC0 == 0x80 {
				sr := uint16(c&0x0F)<<12 | uint16(s[i+1]&0x3F)<<6 | uint16(s[i+2]&0x3F)
				b = append(b, '\\', 'u', hexd[sr>>12], hexd[sr>>8&15], hexd[sr>>4&15], hexd[sr&15])
				i += 3
				continue
			}
			b = append(b, "\xef\xbf\xbd"...)
			i++
			continue
		}
		b = append(b, s[i:i+n]...)
		i += n
	}
	return append(b, '"')
}

func (e *encoder) num(f float64, where string) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		if e.m == modeStored {
			return fmt.Errorf("%s: non-finite numbers are not supported", where)
		}
		e.b = append(e.b, "null"...)
		return nil
	}
	s, err := canonical.FormatNumber(f)
	if err != nil {
		return err
	}
	e.b = append(e.b, s...)
	return nil
}

func undef(v any) bool {
	_, ok := v.(undefinedT)
	return ok
}

func (e *encoder) arr(n int, at func(int) any, where string) error {
	if n == 0 {
		e.b = append(e.b, '[', ']')
		return nil
	}
	e.b = append(e.b, '[')
	e.depth++
	for i := 0; i < n; i++ {
		if i > 0 {
			e.b = append(e.b, ',')
		}
		e.nl()
		x := at(i)
		if undef(x) {
			if e.m == modeStored {
				return fmt.Errorf("%s[%d]: undefined inside an array", where, i)
			}
			x = nil
		}
		if err := e.val(x, where); err != nil {
			return err
		}
	}
	e.depth--
	e.nl()
	e.b = append(e.b, ']')
	return nil
}

func (e *encoder) obj(m *Map, where string) error {
	first := true
	e.b = append(e.b, '{')
	e.depth++
	for _, k := range m.keys {
		v := m.vals[k]
		if undef(v) {
			continue
		}
		if !first {
			e.b = append(e.b, ',')
		}
		first = false
		e.nl()
		e.b = appendQuoted(e.b, k)
		e.b = append(e.b, ':')
		if e.indent > 0 {
			e.b = append(e.b, ' ')
		}
		if err := e.val(v, where); err != nil {
			return err
		}
	}
	e.depth--
	if !first {
		e.nl()
	}
	e.b = append(e.b, '}')
	return nil
}

func (e *encoder) val(v any, where string) error {
	switch x := v.(type) {
	case nil:
		e.b = append(e.b, "null"...)
	case undefinedT:
		e.b = append(e.b, "null"...)
	case bool:
		e.b = strconv.AppendBool(e.b, x)
	case string:
		e.b = appendQuoted(e.b, x)
	case float64:
		return e.num(x, where)
	case *Map:
		if x == nil {
			e.b = append(e.b, "null"...)
			return nil
		}
		return e.obj(x, where)
	case []any:
		return e.arr(len(x), func(i int) any { return x[i] }, where)
	case Timestamp:
		if e.m == modeStored {
			e.b = fmt.Appendf(e.b, `{"$timestamp":{"seconds":%d,"nanos":%d}}`, x.Seconds, x.Nanos)
			return nil
		}
		return e.val(Obj("_seconds", x.Seconds, "_nanoseconds", x.Nanos), where)
	case Bytes:
		if e.m == modeStored {
			e.b = append(e.b, `{"$bytes":`...)
			e.b = appendQuoted(e.b, base64.StdEncoding.EncodeToString(x))
			e.b = append(e.b, '}')
			return nil
		}
		data := make([]any, len(x))
		for i, c := range x {
			data[i] = float64(c)
		}
		return e.val(Obj("type", "Buffer", "data", data), where)
	case *Transform:
		return fmt.Errorf("%s: a FieldTransform cannot be stored (only at a top-level / map position of a write)", where)
	default:
		n, err := From(v)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		switch n.(type) {
		case nil, bool, string, float64, *Map, []any, Timestamp, Bytes:
			return e.val(n, where)
		}
		return fmt.Errorf("%s: unsupported value type %T", where, v)
	}
	return nil
}

var _ json.Marshaler = (*Map)(nil)

// SortedKeys returns the keys of m sorted (Object.keys(m).sort() for ASCII keys).
func (m *Map) SortedKeys() []string {
	k := m.Keys()
	sort.Strings(k)
	return k
}
