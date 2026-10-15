package devtools

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestVectors: the Go ports pass shared/testdata/devtools-vectors.json exactly (same file as the TS tests).
func TestVectors(t *testing.T) {
	b, err := os.ReadFile("../../../shared/testdata/devtools-vectors.json")
	if err != nil {
		t.Skip("vectors not found: ", err)
	}
	var v struct {
		SplitSQL []struct {
			In  string   `json:"in"`
			Out []string `json:"out"`
		} `json:"splitSql"`
		Classify []struct {
			Kind string         `json:"kind"`
			In   string         `json:"in"`
			Out  StatementClass `json:"out"`
		} `json:"classify"`
		Redis []struct {
			In  []string          `json:"in"`
			Out RedisCommandClass `json:"out"`
		} `json:"redis"`
		ParseRedis []struct {
			In  string    `json:"in"`
			Out *[]string `json:"out"`
		} `json:"parseRedis"`
		Redact []struct {
			Kind string    `json:"kind"`
			In   string    `json:"in"`
			Out  Truncated `json:"out"`
		} `json:"redact"`
		RedactHTTP []struct {
			In  []json.RawMessage `json:"in"`
			Out Truncated         `json:"out"`
		} `json:"redactHttp"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.SplitSQL {
		if got := SplitSQLStatements(c.In); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("split %q: got %q want %q", c.In, got, c.Out)
		}
	}
	for _, c := range v.Classify {
		if got := ClassifyStatement(c.Kind, c.In); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("classify %s %q: got %+v want %+v", c.Kind, c.In, got, c.Out)
		}
	}
	for _, c := range v.Redis {
		if got := ClassifyRedis(c.In); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("redis %q: got %+v want %+v", c.In, got, c.Out)
		}
	}
	for _, c := range v.ParseRedis {
		got, ok := ParseRedisCommandLine(c.In)
		if c.Out == nil {
			if ok {
				t.Errorf("parse %q: want null, got %q", c.In, got)
			}
			continue
		}
		if !ok || !reflect.DeepEqual(got, *c.Out) {
			t.Errorf("parse %q: got %q want %q", c.In, got, *c.Out)
		}
	}
	for _, c := range v.Redact {
		if got := RedactStatement(c.Kind, c.In); got != c.Out {
			t.Errorf("redact %s %q: got %+v want %+v", c.Kind, c.In, got, c.Out)
		}
	}
	for _, c := range v.RedactHTTP {
		var method, u string
		var hdr [][2]string
		var n int64
		_ = json.Unmarshal(c.In[0], &method)
		_ = json.Unmarshal(c.In[1], &u)
		_ = json.Unmarshal(c.In[2], &hdr)
		_ = json.Unmarshal(c.In[3], &n)
		if got := RedactHTTPRequest(method, u, hdr, n); got != c.Out {
			t.Errorf("redactHttp: got %q want %q", got.Text, c.Out.Text)
		}
	}
}

func TestHelpers(t *testing.T) {
	if !ConfirmOk("Main DB", " Main DB ") || ConfirmOk("Main DB", "main db") || ConfirmOk("", "") {
		t.Fatal("confirm")
	}
	long := strings.Repeat("é", 3000) // 6000 bytes
	tr := TruncateUTF8(long, 4096)
	if !tr.Truncated || len(tr.Text) > 4096 || !strings.HasSuffix(tr.Text, "…") || strings.ContainsRune(tr.Text, '�') {
		t.Fatalf("truncate: %d %v", len(tr.Text), tr.Truncated)
	}
	if got := ApplyEnvironment("{{host}}/x?t={{ secret.tok }}&u={{unknown}}&v={{ v | upper }}", map[string]string{"host": "http://h"}); got != "http://h/x?t={{ secret.tok }}&u={{unknown}}&v={{ v | upper }}" {
		t.Fatalf("applyEnvironment: %s", got)
	}
	m := NewMasker([]string{"abc", "hunter22", "hunter22-long"})
	if got := m.Mask("pw hunter22-long and hunter22 abc"); got != "pw "+Redacted+" and "+Redacted+" abc" {
		t.Fatalf("mask: %s", got)
	}
	got := LogStatement("postgres", "select 'sup3rs3cret' -- CREATE ROLE x PASSWORD 'p'", NewMasker([]string{"sup3rs3cret"}))
	if strings.Contains(got.Text, "sup3rs3cret") || strings.Contains(got.Text, "'p'") {
		t.Fatalf("log statement: %s", got.Text)
	}
	if r := RedactStatement("redis", `SET "a b" "x\ny"`); r.Text != `SET "a b" "x\ny"` {
		t.Fatalf("redis quote: %s", r.Text)
	}
	if r := RedactStatement("redis", `HELLO 3 AUTH u pw`); r.Text != "HELLO 3 AUTH u "+Redacted {
		t.Fatalf("hello: %s", r.Text)
	}
}

// StatementResult.error.position is relative to the statement as written (the EXPLAIN prefix subtracted; nil inside it).
func TestShiftPosition(t *testing.T) {
	p := func(v int) *int { return &v }
	prefix := len("EXPLAIN (FORMAT JSON) ")
	if got := shiftPosition(p(prefix+8), prefix); got == nil || *got != 8 {
		t.Fatalf("shift %v", got)
	}
	if got := shiftPosition(p(3), prefix); got != nil {
		t.Fatalf("inside the prefix %v", *got)
	}
	if got := shiftPosition(p(5), 0); *got != 5 || shiftPosition(nil, prefix) != nil {
		t.Fatal("no prefix")
	}
}

// Redis truncations say fetchable:false; result-cell truncations omit the key.
func TestCellTruncatedFetchable(t *testing.T) {
	rp := redisReply(strings.Repeat("x", 100), 10)
	b, _ := json.Marshal(rp)
	if !strings.Contains(string(b), `"fetchable":false`) || !strings.Contains(string(b), `"$truncated":true`) {
		t.Fatalf("redis %s", b)
	}
	cells := cellsOf([][]any{{strings.Repeat("y", 100)}}, 0, 10)
	b, _ = json.Marshal(cells)
	if strings.Contains(string(b), "fetchable") || !strings.Contains(string(b), `"$truncated":true`) {
		t.Fatalf("result cell %s", b)
	}
}
