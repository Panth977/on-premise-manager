// Package canonical implements RFC 8785 JSON canonicalization (JCS) exactly like shared/src/canonical.ts,
// plus the activity hash and signed request / response strings.
package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// FormatNumber formats a float64 like ECMAScript Number.prototype.toString(10).
func FormatNumber(v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", fmt.Errorf("canonical: not a finite number: %v", v)
	}
	if v == 0 {
		return "0", nil
	}
	neg := v < 0
	if neg {
		v = -v
	}
	// shortest round-trip digits
	e := strconv.FormatFloat(v, 'e', -1, 64) // d.ddddde±XX
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	k := len(digits)
	n := exp + 1
	var s string
	switch {
	case k <= n && n <= 21:
		s = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		s = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		s = "0." + strings.Repeat("0", -n) + digits
	default:
		ex := n - 1
		sign := "+"
		if ex < 0 {
			sign = "-"
			ex = -ex
		}
		if k == 1 {
			s = digits + "e" + sign + strconv.Itoa(ex)
		} else {
			s = digits[:1] + "." + digits[1:] + "e" + sign + strconv.Itoa(ex)
		}
	}
	if neg {
		s = "-" + s
	}
	return s, nil
}

// MustFormatNumber is FormatNumber for values known to be finite.
func MustFormatNumber(v float64) string {
	s, err := FormatNumber(v)
	if err != nil {
		panic(err)
	}
	return s
}

// QuoteString quotes like JSON.stringify(string).
func QuoteString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// Canonicalize returns the JCS form of v. Values that are not plain JSON values (nil, bool, numbers,
// string, []any, map[string]any, json.Number, json.RawMessage) are first marshalled with encoding/json.
func Canonicalize(v any) (string, error) {
	var b strings.Builder
	if err := ser(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

// MustCanonicalize panics on error.
func MustCanonicalize(v any) string {
	s, err := Canonicalize(v)
	if err != nil {
		panic(err)
	}
	return s
}

// CanonicalizeJSON canonicalizes raw JSON bytes.
func CanonicalizeJSON(data []byte) (string, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return "", err
	}
	return Canonicalize(v)
}

func ser(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		b.WriteString(QuoteString(x))
	case float64:
		s, err := FormatNumber(x)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case float32:
		return ser(b, float64(x))
	case int:
		return ser(b, float64(x))
	case int64:
		return ser(b, float64(x))
	case int32:
		return ser(b, float64(x))
	case uint64:
		return ser(b, float64(x))
	case uint32:
		return ser(b, float64(x))
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return err
		}
		return ser(b, f)
	case json.RawMessage:
		var y any
		if err := json.Unmarshal(x, &y); err != nil {
			return err
		}
		return ser(b, y)
	case []any:
		b.WriteByte('[')
		for i, it := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := ser(b, it); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(QuoteString(k))
			b.WriteByte(':')
			if err := ser(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Pointer && rv.IsNil() {
			b.WriteString("null")
			return nil
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var y any
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(&y); err != nil {
			return err
		}
		return ser(b, y)
	}
	return nil
}

// Sha256Hex returns lowercase hex sha256 of data.
func Sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Sha256Tagged returns "sha256:" + hex.
func Sha256Tagged(data []byte) string { return "sha256:" + Sha256Hex(data) }

// HashCanonical returns "sha256:"+hex(sha256(JCS(v))) (Revision.hash, catalogHash, spec_hash).
func HashCanonical(v any) (string, error) {
	s, err := Canonicalize(v)
	if err != nil {
		return "", err
	}
	return Sha256Tagged([]byte(s)), nil
}

// ActivityFields are the hashed fields of an Activity (see canonical.ts activityHashInput).
type ActivityFields struct {
	ActivityID   string
	DeploymentID string
	SiteSlug     string
	Seq          float64
	Ts           string
	Actor        *string
	Origin       string
	Type         string
	Summary      string
	Data         any // map[string]any or any JSON-marshalable value
	PrevHash     *string
}

// ActivityHashInput = (prevHash ?? "") + JCS({activityId, deploymentId, siteSlug, seq, ts, actor, origin, type, summary, data}).
func ActivityHashInput(a ActivityFields) (string, error) {
	var actor any
	if a.Actor != nil {
		actor = *a.Actor
	}
	data := a.Data
	if data == nil {
		data = map[string]any{}
	}
	body := map[string]any{
		"activityId": a.ActivityID, "deploymentId": a.DeploymentID, "siteSlug": a.SiteSlug, "seq": a.Seq,
		"ts": a.Ts, "actor": actor, "origin": a.Origin, "type": a.Type, "summary": a.Summary, "data": data,
	}
	s, err := Canonicalize(body)
	if err != nil {
		return "", err
	}
	prev := ""
	if a.PrevHash != nil {
		prev = *a.PrevHash
	}
	return prev + s, nil
}

// ActivityHash returns "sha256:"+hex(sha256(ActivityHashInput(a))).
func ActivityHash(a ActivityFields) (string, error) {
	s, err := ActivityHashInput(a)
	if err != nil {
		return "", err
	}
	return Sha256Tagged([]byte(s)), nil
}

// SignedRequestString = METHOD\nPATH\nTS\nNONCE\nBODYHASH (METHOD upper-cased).
func SignedRequestString(method, path string, ts int64, nonce, bodySha256Hex string) string {
	return strings.ToUpper(method) + "\n" + path + "\n" + strconv.FormatInt(ts, 10) + "\n" + nonce + "\n" + bodySha256Hex
}

// SignedResponseString = RESPONSE\nSTATUS\nREQUEST_NONCE\nTS\nBODYHASH.
func SignedResponseString(status int, requestNonce string, ts int64, bodySha256Hex string) string {
	return "RESPONSE\n" + strconv.Itoa(status) + "\n" + requestNonce + "\n" + strconv.FormatInt(ts, 10) + "\n" + bodySha256Hex
}

// Sha256EmptyHex is the hash of the empty body.
const Sha256EmptyHex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
