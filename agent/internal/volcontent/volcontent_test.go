package volcontent

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"opm/agent/internal/model"
)

type vectors struct {
	Paths []struct {
		Path string `json:"path"`
		Ok   bool   `json:"ok"`
	} `json:"paths"`
	Trees []struct {
		Name       string           `json:"name"`
		Tree       model.VolumeTree `json:"tree"`
		Normalized model.VolumeTree `json:"normalized"`
		Canonical  string           `json:"canonical"`
		Hash       string           `json:"hash"`
	} `json:"trees"`
	Excludes []struct {
		Patterns []string `json:"patterns"`
		Path     string   `json:"path"`
		IsDir    bool     `json:"isDir"`
		Excluded bool     `json:"excluded"`
	} `json:"excludes"`
	Patterns []struct {
		Pattern string `json:"pattern"`
		Ok      bool   `json:"ok"`
	} `json:"patterns"`
	Plans []struct {
		Name  string            `json:"name"`
		Base  *model.VolumeTree `json:"base"`
		Live  model.VolumeTree  `json:"live"`
		Cloud model.VolumeTree  `json:"cloud"`
		Items []struct {
			Path    string `json:"path"`
			Kind    string `json:"kind"`
			LiveOp  string `json:"liveOp"`
			CloudOp string `json:"cloudOp"`
		} `json:"items"`
	} `json:"plans"`
	Merges []struct {
		Name       string            `json:"name"`
		Plan       string            `json:"plan"`
		Resolution string            `json:"resolution"`
		Choices    map[string]string `json:"choices"`
		Result     model.VolumeTree  `json:"result"`
		Hash       string            `json:"hash"`
	} `json:"merges"`
}

func load(t *testing.T) vectors {
	b, err := os.ReadFile("../../../shared/testdata/volume-content-vectors.json")
	if err != nil {
		t.Skip("vectors not found: ", err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestVectors runs every section of shared/testdata/volume-content-vectors.json.
func TestVectors(t *testing.T) {
	v := load(t)
	n := 0
	for _, p := range v.Paths {
		n++
		if got := PathError(p.Path) == ""; got != p.Ok {
			t.Errorf("path %q: ok=%v want %v (%s)", p.Path, got, p.Ok, PathError(p.Path))
		}
	}
	for _, tr := range v.Trees {
		n++
		norm := Normalize(tr.Tree)
		if !reflect.DeepEqual(norm, Normalize(tr.Normalized)) || len(norm.Entries) != len(tr.Normalized.Entries) || len(norm.Dirs) != len(tr.Normalized.Dirs) {
			t.Errorf("tree %s: normalized %+v want %+v", tr.Name, norm, tr.Normalized)
		}
		if got := HashInput(tr.Tree); got != tr.Canonical {
			t.Errorf("tree %s: canonical\n got %s\nwant %s", tr.Name, got, tr.Canonical)
		}
		if got := Hash(tr.Tree); got != tr.Hash {
			t.Errorf("tree %s: hash %s want %s", tr.Name, got, tr.Hash)
		}
	}
	for _, x := range v.Excludes {
		n++
		if got := Compile(x.Patterns).Excluded(x.Path, x.IsDir); got != x.Excluded {
			t.Errorf("exclude %v %q dir=%v: %v want %v", x.Patterns, x.Path, x.IsDir, got, x.Excluded)
		}
	}
	for _, p := range v.Patterns {
		n++
		if got := ExcludePatternError(p.Pattern) == ""; got != p.Ok {
			t.Errorf("pattern %q: ok=%v want %v", p.Pattern, got, p.Ok)
		}
	}
	plans := map[string]int{}
	for i, p := range v.Plans {
		n++
		plans[p.Name] = i
		items := Plan(p.Base, p.Live, p.Cloud)
		if len(items) != len(p.Items) {
			t.Errorf("plan %s: %d items want %d: %+v", p.Name, len(items), len(p.Items), items)
			continue
		}
		for k, it := range items {
			w := p.Items[k]
			if it.Path != w.Path || it.Kind != w.Kind || it.LiveOp != w.LiveOp || it.CloudOp != w.CloudOp {
				t.Errorf("plan %s item %d: %+v want %+v", p.Name, k, it, w)
			}
		}
	}
	for _, m := range v.Merges {
		n++
		p := v.Plans[plans[m.Plan]]
		got, err := Merge(p.Base, p.Live, p.Cloud, m.Resolution, m.Choices)
		if err != nil {
			t.Errorf("merge %s: %v", m.Name, err)
			continue
		}
		if HashInput(got) != HashInput(m.Result) || Hash(got) != m.Hash {
			t.Errorf("merge %s:\n got %s\nwant %s", m.Name, HashInput(got), HashInput(m.Result))
		}
	}
	// a conflict without a choice → error
	for _, p := range v.Plans {
		if p.Name == "three-way" {
			if _, err := Merge(p.Base, p.Live, p.Cloud, "per-file", nil); err == nil {
				t.Error("per-file without choices must fail")
			}
		}
	}
	t.Logf("%d vectors", n)
}

func TestTreeIssues(t *testing.T) {
	good := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	tr := model.VolumeTree{Entries: []model.VolumeTreeEntry{
		{Path: "a", Sha256: good, Size: 1, Mode: "0644", Render: "none"},
		{Path: "a/b", Sha256: good, Size: 1, Mode: "0644", Render: "none"},
		{Path: "a/b", Sha256: "x", Size: -1, Mode: "644", Render: "x"},
		{Path: "../x", Sha256: good, Size: 1, Mode: "0644", Render: "none"},
		{Path: "r", Sha256: good, Size: 1, Mode: "0644", Render: "expressions"},
	}, Dirs: []string{"a", "d", "d"}}
	codes := map[string]int{}
	for _, x := range TreeIssues(tr, false) {
		codes[x.Code]++
	}
	if codes["V_VOLUME_CONTENT_PATH"] != 1 || codes["V_DUPLICATE"] != 2 || codes["V_FORMAT"] != 3 || codes["V_TOO_LARGE"] != 1 || codes["V_VOLUME_CONTENT"] != 3 {
		t.Fatalf("codes %v", codes)
	}
	if len(TreeIssues(model.VolumeTree{}, true)) != 1 {
		t.Fatal("missing entries")
	}
}
