package shared

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"opm/agent/internal/cloud/doc"
)

func issueLines(l []any) []string {
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = doc.MarshalString(x)
	}
	return out
}

// The twin against TS validateConfig on the recorded suites, the vectors and seeded mutations.
func TestValidateConfigDifferential(t *testing.T) {
	file := filepath.Join(t.TempDir(), "corpus.json")
	tsxRun(t, "testdata/validate_diff.mts", file)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	v, err := doc.ParseMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	cases := v.Arr("cases")
	type stat struct{ total, bad, hang int }
	stats := map[string]*stat{}
	buckets := map[string]int{}
	samples := map[string]string{}
	for _, c := range cases {
		m := c.(*doc.Map)
		kind := m.Str("kind")
		cfg, _ := m.Val("config").(*doc.Map)
		ctx := m.Map("ctx")
		st := stats[kind]
		if st == nil {
			st = &stat{}
			stats[kind] = st
		}
		st.total++
		if m.Bool("hang") { // TS never returns on this config: Go must, there is nothing to compare
			st.hang++
		}
		is, gerr := func() (i *Issues, e error) {
			defer func() {
				if r := recover(); r != nil {
					e = fmt.Errorf("PANIC %v", r)
				}
			}()
			return ValidateConfig(cfg, ctx)
		}()
		var got, want []string
		if gerr != nil {
			got = []string{"throw: " + gerr.Error()}
		} else {
			got = issueLines(is.List())
		}
		if m.Has("throw") {
			want = []string{"throw: " + m.Str("throw")}
		} else {
			want = issueLines(m.Arr("list"))
		}
		if m.Bool("hang") || strings.Join(got, "\n") == strings.Join(want, "\n") {
			continue
		}
		st.bad++
		if d := os.Getenv("DIFF_REPORT"); d != "" {
			os.WriteFile(d+".case."+kind, []byte(doc.MarshalString(m)), 0o644)
		}
		// the first differing line names the bucket
		g, w := "", ""
		gs := map[string]bool{}
		for _, x := range got {
			gs[x] = true
		}
		ws := map[string]bool{}
		for _, x := range want {
			ws[x] = true
		}
		for _, x := range got {
			if !ws[x] {
				g = x
				break
			}
		}
		for _, x := range want {
			if !gs[x] {
				w = x
				break
			}
		}
		key := "order"
		if g != "" || w != "" {
			gm, _ := doc.ParseMap([]byte(g))
			wm, _ := doc.ParseMap([]byte(w))
			key = kind + " got=" + bucket(gm, g) + " want=" + bucket(wm, w)
		}
		buckets[key]++
		if samples[key] == "" {
			samples[key] = "got  " + short(g) + "\nwant " + short(w)
		}
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return buckets[keys[i]] > buckets[keys[j]] })
	report := []string{}
	for _, k := range keys {
		report = append(report, fmt.Sprintf("%5d %s\n%s", buckets[k], k, samples[k]))
	}
	if d := os.Getenv("DIFF_REPORT"); d != "" {
		os.WriteFile(d, []byte(strings.Join(report, "\n")), 0o644)
	}
	for k, s := range stats {
		t.Logf("%s: %d cases, %d mismatches, %d TS hangs", k, s.total, s.bad, s.hang)
	}
	t.Logf("suite calls: %v", v.Val("suite"))
	for _, k := range []string{"suite", "hello", "vector", "typed"} {
		if s := stats[k]; s != nil && s.bad > 0 {
			t.Errorf("%s: %d of %d cases differ from TS", k, s.bad, s.total)
		}
	}
}

func bucket(m *doc.Map, raw string) string {
	if strings.HasPrefix(raw, "throw:") {
		s := raw
		if len(s) > 60 {
			s = s[:60]
		}
		return s
	}
	if m == nil {
		return "-"
	}
	return m.Str("code")
}
