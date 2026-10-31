package doc

import (
	"encoding/json"
	"testing"
	"time"

	"opm/agent/internal/canonical"
)

func TestJSObjectOrder(t *testing.T) {
	m, err := ParseMap([]byte(`{"b":1,"2":1,"1":1,"a":2,"b":3,"01":0,"4294967295":0,"4294967294":0}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"1":1,"2":1,"4294967294":0,"b":3,"a":2,"01":0,"4294967295":0}`
	if got := MarshalString(m); got != want {
		t.Fatalf("%s\n%s", got, want)
	}
	m.Delete("b").Set("b", 9)
	if m.Keys()[len(m.Keys())-1] != "b" {
		t.Fatal(m.Keys())
	}
}

func TestStringifyLikeJS(t *testing.T) {
	cases := map[string]any{
		`{"a":null,"c":[null,null],"n":100000000000000000000,"e":1e+21,"f":1.5e-7,"z":0}`: Obj("a", nil, "u", Undefined, "c", []any{nil, Undefined}, "n", 1e20, "e", 1e21, "f", 1.5e-7, "z", -0.0),
		`"<>&  é\"\\\n\u0001\u007f"`:       nil,
		`{"_seconds":5,"_nanoseconds":7}`:  Timestamp{5, 7},
		`{"type":"Buffer","data":[0,255]}`: Bytes{0, 255},
		`"2026-01-02T03:04:05.006Z"`:       time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC),
		`{"a":1,"b":2}`:                    map[string]any{"b": 2, "a": 1},
		`{"x":"y"}`: struct {
			X string `json:"x"`
		}{"y"},
		`[1,"a"]`: json.RawMessage(`[1.0, "a"]`),
	}
	for want, v := range cases {
		if v == nil {
			x, _ := ParseString(`"<>&  é\"\\\n\u0001` + "\x7f" + `"`)
			got := MarshalString(x)
			if got != "\"<>&  é\\\"\\\\\\n\\u0001\x7f\"" {
				t.Fatal(got)
			}
			continue
		}
		if got := MarshalString(v); got != want {
			t.Fatalf("%s\n%s", got, want)
		}
	}
	b, _ := MarshalIndent(Obj("a", []any{1, Obj("b", 2)}, "e", New(), "l", []any{}), 2)
	if string(b) != "{\n  \"a\": [\n    1,\n    {\n      \"b\": 2\n    }\n  ],\n  \"e\": {},\n  \"l\": []\n}" {
		t.Fatal(string(b))
	}
}

func TestLoneSurrogateRoundTrip(t *testing.T) {
	v, err := ParseString(`"a\ud83dz\udc00😀😀"`)
	if err != nil {
		t.Fatal(err)
	}
	if got := MarshalString(v); got != `"a\ud83dz\udc00😀😀"` {
		t.Fatal(got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, s := range []string{``, `{`, `[1,]`, `{"a":}`, `01`, `1.`, `"\x"`, "\"a\nb\"", `tru`, `1 2`, `{"a":1,}`} {
		if _, err := ParseString(s); err == nil {
			t.Fatal(s)
		}
	}
}

func TestCanonicalAcceptsDocs(t *testing.T) {
	s, err := canonical.Canonicalize(Obj("b", 1, "a", []any{Obj("z", 1, "y", nil)}))
	if err != nil || s != `{"a":[{"y":null,"z":1}],"b":1}` {
		t.Fatal(s, err)
	}
}

func TestJSHelpers(t *testing.T) {
	if JSLen("a😀") != 3 || JSSlice("a😀b", 2) != "a�" || JSSlice("abc", 2) != "ab" {
		t.Fatal("utf-16")
	}
	if string(B64Decode("AAEC_w")) != string(B64Decode("AAEC/w==")) || len(B64Decode("AAEC/w==")) != 4 {
		t.Fatal("base64")
	}
	if EncodeURIComponent(`a b/ü~'()!*`) != `a%20b%2F%C3%BC~'()!*` {
		t.Fatal(EncodeURIComponent(`a b/ü~'()!*`))
	}
	if FormEncode(`a b~*`) != `a+b%7E*` {
		t.Fatal(FormEncode(`a b~*`))
	}
	if _, err := DecodeURIComponent("%FF"); err == nil {
		t.Fatal("URI malformed")
	}
	q := ParseSearch("a=1&a=2&b=x+y%20z&c&d=%ZZ")
	if q.Get("a") != "1" || len(q.All("a")) != 2 || q.Get("b") != "x y z" || !q.Has("c") || q.Has("e") || q.Get("d") != "%ZZ" {
		t.Fatal(q)
	}
	if TimestampFromMillis(-1).Seconds != -1 || TimestampFromMillis(-1).Nanos != 999_000_000 || TimestampFromMillis(1500).Millis() != 1500 {
		t.Fatal("timestamp")
	}
	if ms, ok := ParseISO("2026-01-01T00:00:00.000Z"); !ok || ms != 1767225600000 {
		t.Fatal(ms)
	}
	if Truthy(0) || Truthy("") || Truthy(nil) || !Truthy([]any{}) || !Truthy(New()) {
		t.Fatal("truthy")
	}
}
