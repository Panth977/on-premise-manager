package doc

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// JSLen is string.length (UTF-16 code units).
func JSLen(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// JSSlice is s.slice(0, n) in UTF-16 code units (a cut surrogate pair becomes U+FFFD, as UTF-8 output of JS does).
func JSSlice(s string, n int) string {
	if len(s) <= n {
		return s
	}
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}

// IsJSSpace is the \s of a JS regex (also String.prototype.trim).
func IsJSSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// JSTrim is String.prototype.trim.
func JSTrim(s string) string { return strings.TrimFunc(s, IsJSSpace) }

// ISO is Date.prototype.toISOString.
func ISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// NowISO is new Date().toISOString().
func NowISO() string { return ISO(time.Now()) }

// IsoPlus is new Date(Date.now() + ms).toISOString().
func IsoPlus(ms int64) string { return ISO(time.Now().Add(time.Duration(ms) * time.Millisecond)) }

// ParseISO is Date.parse of the ISO forms OPM stores; false = NaN.
func ParseISO(s string) (int64, bool) {
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04Z07:00", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

// B64Decode is Buffer.from(s, 'base64' | 'base64url'): both alphabets, characters outside them skipped, padding optional.
func B64Decode(s string) []byte {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
			b = append(b, c)
		case c == '-':
			b = append(b, '+')
		case c == '_':
			b = append(b, '/')
		}
	}
	if len(b)%4 == 1 {
		b = b[:len(b)-1]
	}
	out, _ := base64.RawStdEncoding.DecodeString(string(b))
	return out
}

// B64URL is toString('base64url').
func B64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func encodeSet(s string, keep func(byte) bool, space string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case keep(c):
			b.WriteByte(c)
		case c == ' ' && space != "":
			b.WriteString(space)
		default:
			b.WriteByte('%')
			b.WriteByte("0123456789ABCDEF"[c>>4])
			b.WriteByte("0123456789ABCDEF"[c&15])
		}
	}
	return b.String()
}
func alnum(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

// EncodeURIComponent is the JS function.
func EncodeURIComponent(s string) string {
	return encodeSet(s, func(c byte) bool { return alnum(c) || strings.IndexByte("-_.!~*'()", c) >= 0 }, "")
}

// DecodeURIComponent is the JS function (an error where it throws URIError).
func DecodeURIComponent(s string) (string, error) {
	out, err := url.PathUnescape(s)
	if err != nil {
		return "", err
	}
	if strings.Contains(s, "%") && !utf8.ValidString(out) {
		return "", errors.New("URI malformed")
	}
	return out, nil
}

// FormEncode is one key or value of URLSearchParams.toString.
func FormEncode(s string) string {
	return encodeSet(s, func(c byte) bool { return alnum(c) || strings.IndexByte("*-._", c) >= 0 }, "+")
}

// Search is URLSearchParams: ordered pairs, lenient decoding.
type Search struct{ Pairs [][2]string }

func searchDecode(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	if !strings.Contains(s, "%") {
		return s
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b = append(b, byte(v))
				i += 2
				continue
			}
		}
		b = append(b, s[i])
	}
	return strings.ToValidUTF8(string(b), "�")
}

// ParseSearch parses a query string (without "?").
func ParseSearch(q string) *Search {
	out := &Search{}
	for _, p := range strings.Split(q, "&") {
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		out.Pairs = append(out.Pairs, [2]string{searchDecode(k), searchDecode(v)})
	}
	return out
}

// Get is get(k) ("" when missing; Lookup tells the two apart).
func (s *Search) Get(k string) string {
	v, _ := s.Lookup(k)
	return v
}

// Lookup is get(k) with null = false.
func (s *Search) Lookup(k string) (string, bool) {
	for _, p := range s.Pairs {
		if p[0] == k {
			return p[1], true
		}
	}
	return "", false
}

// Has is has(k).
func (s *Search) Has(k string) bool {
	_, ok := s.Lookup(k)
	return ok
}

// All is getAll(k).
func (s *Search) All(k string) []string {
	var out []string
	for _, p := range s.Pairs {
		if p[0] == k {
			out = append(out, p[1])
		}
	}
	return out
}

// Set is set(k, v).
func (s *Search) Set(k, v string) {
	for i, p := range s.Pairs {
		if p[0] == k {
			s.Pairs[i][1] = v
			return
		}
	}
	s.Pairs = append(s.Pairs, [2]string{k, v})
}

// String is toString.
func (s *Search) String() string {
	var b strings.Builder
	for i, p := range s.Pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(FormEncode(p[0]))
		b.WriteByte('=')
		b.WriteString(FormEncode(p[1]))
	}
	return b.String()
}
