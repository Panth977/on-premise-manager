package branch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"opm/agent/internal/model"
)

type vecItem struct {
	Key        string   `json:"key"`
	Kind       string   `json:"kind"`
	Effect     string   `json:"effect"`
	Preselect  bool     `json:"preselect"`
	Requires   []string `json:"requires"`
	SourceHash string   `json:"sourceHash"`
	Scalar     bool     `json:"scalar"`
}

type vecCase struct {
	Name   string `json:"name"`
	Base   any    `json:"base"`
	Source any    `json:"source"`
	Target any    `json:"target"`
	Opts   struct {
		SecretValues bool              `json:"secretValues"`
		Scope        string            `json:"scope"`
		Skipped      []model.MergeSkip `json:"skipped"`
	} `json:"opts"`
	Items       []vecItem           `json:"items"`
	FastForward bool                `json:"fastForward"`
	Choices     []model.MergeChoice `json:"choices"`
	Values      map[string]any      `json:"values"`
	Result      *struct {
		Hash       string   `json:"hash"`
		Taken      []string `json:"taken"`
		NotTaken   []string `json:"notTaken"`
		Unresolved []string `json:"unresolved"`
		Kind       string   `json:"kind"`
	} `json:"result"`
	Revert *struct {
		Commit, Parent, Head any
	} `json:"revert"`
}

func vectors(t *testing.T) map[string]json.RawMessage {
	_, f, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(f), "../../../shared/testdata/branch-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func compact(items []model.MergeItem) []vecItem {
	out := []vecItem{}
	for _, i := range items {
		req := i.Requires
		if req == nil {
			req = []string{}
		}
		out = append(out, vecItem{i.Key, i.Kind, i.Effect, i.Preselect, req, i.SourceHash, i.Scalar})
	}
	return out
}

func TestVectors(t *testing.T) {
	v := vectors(t)
	var cases []vecCase
	if err := json.Unmarshal(v["cases"], &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Revert != nil {
			items := RevertPlan(c.Revert.Commit, c.Revert.Parent, c.Revert.Head, Options{SecretValues: true})
			if got := compact(items); !reflect.DeepEqual(got, c.Items) {
				t.Errorf("%s: revert plan\n got %+v\nwant %+v", c.Name, got, c.Items)
			}
			r := ApplyRevert(c.Revert.Commit, c.Revert.Parent, c.Revert.Head, nil, Options{SecretValues: true})
			if got := ConfigHash(r.Config); got != c.Result.Hash {
				t.Errorf("%s: revert hash %s want %s", c.Name, got, c.Result.Hash)
			}
			continue
		}
		var base any
		if c.Base != nil {
			base = c.Base
		}
		opt := Options{SecretValues: c.Opts.SecretValues, Scope: c.Opts.Scope, Skipped: c.Opts.Skipped}
		items := Plan(base, c.Source, c.Target, opt)
		if got := compact(items); !reflect.DeepEqual(got, c.Items) {
			t.Errorf("%s: plan\n got %+v\nwant %+v", c.Name, got, c.Items)
		}
		if IsFastForward(items) != c.FastForward {
			t.Errorf("%s: fastForward", c.Name)
		}
		if c.Choices != nil && c.Result != nil {
			opt.Values = c.Values
			r := Apply(base, c.Source, c.Target, c.Choices, opt)
			if got := ConfigHash(r.Config); got != c.Result.Hash {
				t.Errorf("%s: result hash %s want %s", c.Name, got, c.Result.Hash)
			}
			nt := []string{}
			for _, x := range r.NotTaken {
				nt = append(nt, x.Key)
			}
			if !reflect.DeepEqual(r.Taken, c.Result.Taken) || !reflect.DeepEqual(nt, c.Result.NotTaken) || !reflect.DeepEqual(r.Unresolved, c.Result.Unresolved) || r.Kind != c.Result.Kind {
				t.Errorf("%s: taken %v/%v notTaken %v/%v unresolved %v/%v kind %s/%s", c.Name, r.Taken, c.Result.Taken, nt, c.Result.NotTaken, r.Unresolved, c.Result.Unresolved, r.Kind, c.Result.Kind)
			}
		}
	}
	var hashes []struct {
		Config any    `json:"config"`
		Hash   string `json:"hash"`
	}
	_ = json.Unmarshal(v["hashes"], &hashes)
	for _, h := range hashes {
		if got := ConfigHash(h.Config); got != h.Hash {
			t.Errorf("hash %s want %s", got, h.Hash)
		}
	}
	var counts []struct {
		Head, Working any
		Expect        int `json:"expect"`
	}
	_ = json.Unmarshal(v["counts"], &counts)
	for _, c := range counts {
		if n := len(Uncommitted(c.Head, c.Working, Options{SecretValues: true})); n != c.Expect {
			t.Errorf("uncommitted %d want %d", n, c.Expect)
		}
	}
	var stats struct {
		Parent, Config any
		Expect         model.CommitStats `json:"expect"`
	}
	_ = json.Unmarshal(v["stats"], &stats)
	if got := Stats(stats.Parent, stats.Config, Options{SecretValues: true}); !reflect.DeepEqual(got, stats.Expect) {
		t.Errorf("stats %+v want %+v", got, stats.Expect)
	}
	var ih []struct {
		Value  any    `json:"value"`
		Absent bool   `json:"absent"`
		Hash   string `json:"hash"`
	}
	_ = json.Unmarshal(v["itemHashes"], &ih)
	for _, h := range ih {
		s := Slot{Has: !h.Absent, V: h.Value}
		if got := ItemHash(s); got != h.Hash {
			t.Errorf("itemHash(%v) %s want %s", h.Value, got, h.Hash)
		}
	}
}

func TestTypedConfigRoundTrip(t *testing.T) {
	v := vectors(t)
	var cases []vecCase
	_ = json.Unmarshal(v["cases"], &cases)
	// a typed model.Config gives the same hash as its tree (the agent passes typed configs)
	for _, c := range cases {
		if c.Source == nil {
			continue
		}
		var cfg model.Config
		if err := FromTree(c.Source, &cfg); err != nil {
			t.Fatal(err)
		}
		if ConfigHash(cfg) != ConfigHash(c.Source) {
			t.Errorf("%s: typed hash differs", c.Name)
		}
	}
}

func TestMessages(t *testing.T) {
	if CheckMessage("") == "" || CheckMessage("  ") == "" || CheckMessage("ok") != "" {
		t.Fatal("message rules")
	}
	if Summary("  a \nb") != "a" {
		t.Fatal("summary")
	}
}
