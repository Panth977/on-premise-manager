package cloud

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// Sha256Hex of bytes.
func Sha256Hex(b []byte) string {
	x := sha256.Sum256(b)
	return hex.EncodeToString(x[:])
}

// Sha256Of is "sha256:" + Sha256Hex.
func Sha256Of(b []byte) string { return "sha256:" + Sha256Hex(b) }

// JcsHash is the sha256 of the RFC 8785 form of a value.
func JcsHash(v any) (string, error) {
	s, err := canonical.Canonicalize(v)
	if err != nil {
		return "", err
	}
	return Sha256Of([]byte(s)), nil
}

// HexOf strips "sha256:".
func HexOf(sha string) string { return strings.TrimPrefix(sha, "sha256:") }

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// IsHex64 is isHex64.
func IsHex64(v any) bool {
	s, ok := v.(string)
	return ok && hex64.MatchString(s)
}

var charsets = map[string]string{
	"alnum":     "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
	"hex":       "0123456789abcdef",
	"base64url": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-",
}

// GenerateSecret is a crypto-random secret of an exact length from a charset: alnum | hex | base64url.
func GenerateSecret(charset string, length int) string {
	return driver.RandomFrom(charsets[charset], length)
}

// RandomToken is n random bytes, base64url (default 32).
func RandomToken(n ...int) string {
	l := 32
	if len(n) > 0 {
		l = n[0]
	}
	b := make([]byte, l)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return doc.B64URL(b)
}

// Clone is JSON.parse(JSON.stringify(v)) over document values.
func Clone(v any) any {
	x, err := doc.Parse(mustJSON(v))
	if err != nil {
		panic(err)
	}
	return x
}

func mustJSON(v any) []byte {
	b, err := doc.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// SameJSON compares two values canonically.
func SameJSON(a, b any) bool {
	x, err1 := canonical.Canonicalize(a)
	y, err2 := canonical.Canonicalize(b)
	return err1 == nil && err2 == nil && x == y
}

// V collects validation issues.
type V struct{ List []Issue }

// Err adds an issue.
func (v *V) Err(path, code, msg string) {
	v.List = append(v.List, Issue{Path: path, Code: code, Message: msg, Severity: "error"})
}

// ThrowIf returns E.invalid(list) when there are issues.
func (v *V) ThrowIf() error {
	if len(v.List) > 0 {
		return E.Invalid(v.List)
	}
	return nil
}

// OK reports no issues.
func (v *V) OK() bool { return len(v.List) == 0 }

// AsObject requires a JSON object.
func AsObject(b any) (*doc.Map, error) {
	m, ok := b.(*doc.Map)
	if !ok || m == nil {
		return nil, E.Bad("", "V_TYPE", "body must be a JSON object")
	}
	return m, nil
}

func has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// OnlyKeys: unknown body fields → invalid (V_TYPE "unknown field x"); immutable ones V_IMMUTABLE.
func OnlyKeys(b *doc.Map, allowed []string, immutable ...string) error {
	v := &V{}
	for _, k := range b.Keys() {
		if has(allowed, k) {
			continue
		}
		if has(immutable, k) {
			v.Err(k, "V_IMMUTABLE", k+" cannot be changed")
		} else {
			v.Err(k, "V_TYPE", "unknown field "+k)
		}
	}
	return v.ThrowIf()
}

// VLabel checks a label; pass doc.Undefined for a missing field.
func VLabel(v *V, val any, path string, max int, required bool) {
	if val == doc.Undefined && !required {
		return
	}
	s, ok := val.(string)
	if !ok || doc.JSTrim(s) == "" {
		v.Err(path, "V_REQUIRED", path+" is required")
		return
	}
	if doc.JSLen(s) > max {
		v.Err(path, "V_LENGTH", path+": at most "+itoa(max)+" characters")
	}
}

// VDescription checks a description; pass doc.Undefined for a missing field.
func VDescription(v *V, val any, path string) {
	if val == doc.Undefined {
		return
	}
	s, ok := val.(string)
	if !ok {
		v.Err(path, "V_TYPE", path+" must be a string")
		return
	}
	if doc.JSLen(s) > Limits["description"] {
		v.Err(path, "V_LENGTH", path+": at most "+itoa(Limits["description"])+" characters")
	}
}

// ReSlug is RE.slug.
var ReSlug = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

var reHTTPSAny = regexp.MustCompile(`^https://[^\s\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+$`)

// VSlug checks a slug.
func VSlug(v *V, val any, path string) {
	if s, ok := val.(string); !ok || !ReSlug.MatchString(s) {
		v.Err(path, "V_FORMAT", path+" must match ^[a-z][a-z0-9-]{1,39}$")
	}
}

// VHTTPSURLOrNull checks an https URL or null / missing.
func VHTTPSURLOrNull(v *V, val any, path string) {
	if val == nil || val == doc.Undefined {
		return
	}
	if s, ok := val.(string); !ok || !reHTTPSAny.MatchString(s) || doc.JSLen(s) > 2048 {
		v.Err(path, "V_FORMAT", path+" must be an https URL or null")
	}
}

// VString checks a string length.
func VString(v *V, val any, path string, min, max int) {
	s, ok := val.(string)
	if !ok {
		if min > 0 {
			v.Err(path, "V_REQUIRED", path+" must be a string")
		} else {
			v.Err(path, "V_TYPE", path+" must be a string")
		}
		return
	}
	if n := doc.JSLen(s); n < min || n > max {
		c := "V_LENGTH"
		if min > 0 && n == 0 {
			c = "V_REQUIRED"
		}
		v.Err(path, c, path+" must be "+itoa(min)+"–"+itoa(max)+" characters")
	}
}

// VInt checks an integer range.
func VInt(v *V, val any, path string, min, max int) {
	f, ok := doc.AsNumber(val)
	if !ok || f != float64(int64(f)) {
		v.Err(path, "V_TYPE", path+" must be an integer")
		return
	}
	if f < float64(min) || f > float64(max) {
		v.Err(path, "V_RANGE", path+" must be "+itoa(min)+"–"+itoa(max))
	}
}

// GroupKey groups a 24-char access key as XXXX-XXXX-….
func GroupKey(k string) string {
	var p []string
	for len(k) > 4 {
		p = append(p, k[:4])
		k = k[4:]
	}
	return strings.Join(append(p, k), "-")
}

// YMD is yyyymmdd (UTC).
func YMD(t time.Time) string { return t.UTC().Format("20060102") }

// KeyFingerprint: Ed25519 public key (raw 32 bytes, base64 std) → first 16 hex of sha256(raw).
func KeyFingerprint(publicKeyB64 string) string { return Sha256Hex(doc.B64Decode(publicKeyB64))[:16] }
