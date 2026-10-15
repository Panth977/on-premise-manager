package edgeres

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"opm/agent/internal/model"
)

type vectors struct {
	Edges map[string]model.Edge                `json:"edges"`
	Ports map[string]map[string]map[string]int `json:"ports"`
	Cases []struct {
		Edge      string          `json:"edge"`
		URL       string          `json:"url"`
		Method    string          `json:"method"`
		BodyBytes *int64          `json:"bodyBytes"`
		Upgrade   bool            `json:"upgrade"`
		Expect    json.RawMessage `json:"expect"`
	} `json:"cases"`
}

func loadVectors(t *testing.T) vectors {
	b, err := os.ReadFile("../../../shared/testdata/edge-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestVectors: every case of shared/testdata/edge-vectors.json gives the TS edgeVectorView exactly.
func TestVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.Cases) == 0 {
		t.Fatal("no cases")
	}
	pass := 0
	for _, c := range v.Cases {
		e := v.Edges[c.Edge]
		r := Resolve(&e, c.URL, Options{Method: c.Method, Ports: v.Ports[c.Edge], BodyBytes: c.BodyBytes, Upgrade: c.Upgrade})
		got, _ := json.Marshal(VectorView(r))
		var a, b any
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal(c.Expect, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s %s %s\n got: %s\nwant: %s", c.Edge, c.Method, c.URL, got, c.Expect)
			continue
		}
		pass++
	}
	t.Logf("edge vectors: %d/%d", pass, len(v.Cases))
}

// The vector edges are in the round E shape: decoding + marshalling gives the same JSON.
func TestVectorEdgesRoundTrip(t *testing.T) {
	b, _ := os.ReadFile("../../../shared/testdata/edge-vectors.json")
	var raw struct {
		Edges map[string]json.RawMessage `json:"edges"`
	}
	_ = json.Unmarshal(b, &raw)
	for name, x := range raw.Edges {
		var e model.Edge
		if err := json.Unmarshal(x, &e); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(e.Normalized())
		var a, c any
		_ = json.Unmarshal(x, &a)
		_ = json.Unmarshal(out, &c)
		if !reflect.DeepEqual(a, c) {
			t.Errorf("%s: normalized differs:\n%s", name, out)
		}
	}
}

func TestCheckEdgeRegex(t *testing.T) {
	ok := []string{`^/apis/(docs|_docs_/[^/]+)$`, `(?i)\.PNG$`, `^/u/([0-9]+)/(\w+)$`, `(?<name>x)`, `(?:a|b)`, `[]a]`, `^`}
	for _, s := range ok {
		if e := CheckEdgeRegex(s); e != "" {
			t.Errorf("%s: %s", s, e)
		}
	}
	bad := []string{"", `(?=x)`, `(?<=x)`, `(?>x)`, `(?P<n>x)`, `a(?i)b`, `\1`, `\A`, `\pL`, `\x{41}`, "\\u0041", `\cA`, `\0`, `[[:alpha:]]`, `a++`, `(`}
	for _, s := range bad {
		if e := CheckEdgeRegex(s); e == "" {
			t.Errorf("%s should fail", s)
		}
	}
}

func TestExpandEdgeVars(t *testing.T) {
	ra := "1.2.3.4"
	v := Vars{URI: "/a", Args: "x=1", RequestURI: "/o?x=1", Host: "h", Scheme: "https", Captures: []string{"/a", "A"}, RemoteAddr: &ra, Headers: map[string]string{"x-y": "z"}}
	got := ExpandEdgeVars("$uri|$args|$is_args|$request_uri|$host|$scheme|$1|$2|${uri}|$http_x_y|$nope|{path}{query}{host}|$remote_addr", v)
	want := "/a|x=1|?|/o?x=1|h|https|A||/a|z|$nope|/a?x=1h|1.2.3.4"
	if got != want {
		t.Fatalf("got %s", got)
	}
}
