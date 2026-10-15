package diff

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"opm/agent/internal/model"
)

func load(t *testing.T) *model.Config {
	var f struct {
		Config model.Config `json:"config"`
	}
	b, err := os.ReadFile("../../../shared/testdata/hello-config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return &f.Config
}

func clone(t *testing.T, c *model.Config) *model.Config {
	b, _ := json.Marshal(c)
	var o model.Config
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatal(err)
	}
	return &o
}

func slugs(id string) string {
	return id // containers are keyed by serviceSlug (identity)
}

func TestNoChange(t *testing.T) {
	c := load(t)
	if d := Configs(c, clone(t, c), slugs); d == nil || len(d) != 0 {
		t.Fatal(d)
	}
}

func TestTheme(t *testing.T) {
	c := load(t)
	n := clone(t, c)
	n.Variables[0].Value = model.VariableValue{V: "yellow"}
	d := Configs(c, n, slugs)
	if len(d) != 1 || d[0].Section != "variables" || d[0].Path != "theme" || d[0].Kind != "changed" || d[0].Before != "green" || d[0].After != "yellow" || d[0].SiteSlug != nil {
		t.Fatalf("%+v", d)
	}
}

func TestSecretAndEnv(t *testing.T) {
	c := load(t)
	n := clone(t, c)
	v := "other"
	n.Secrets[0].Value = &v
	n.Sites[1].Config.Containers[0].Env[2].Value = &v
	d := Configs(c, n, slugs)
	if len(d) != 2 {
		t.Fatalf("%+v", d)
	}
	if d[0].Section != "secrets" || d[0].Before != "set" || d[0].After != "changed" {
		t.Fatalf("%+v", d[0])
	}
	if d[1].Path != "app/hello-api/env/GREETING" || d[1].Before != "{{ var.greeting }}" || d[1].After != "other" {
		t.Fatalf("%+v", d[1])
	}
}

func TestAddedSite(t *testing.T) {
	c := load(t)
	d := Configs(nil, c, slugs)
	sites, conts, vols := 0, 0, 0
	for _, x := range d {
		if x.Kind != "added" {
			t.Fatalf("%+v", x)
		}
		if x.Section == "sites" {
			sites++
		}
		if x.Section == "containers" {
			conts++
		}
		if x.Section == "volumes" {
			vols++
		}
	}
	if sites != 2 || conts != 3 || vols != 2 || len(d) != 5+1+1+2+2+3 {
		t.Fatalf("%d %d %d %d", sites, conts, vols, len(d))
	}
	b, _ := json.Marshal(d)
	if len(b) == 0 {
		t.Fatal("marshal")
	}
}

func TestViews(t *testing.T) {
	m := "•••"
	a := &model.ConfigView{Secrets: []model.SecretDefView{{SecretSlug: "k", Label: "K", Value: &m, IsSet: true}}}
	b := &model.ConfigView{Secrets: []model.SecretDefView{{SecretSlug: "k", Label: "K", Value: nil, IsSet: false}}}
	d := ConfigViews(a, b, nil, nil)
	if len(d) != 1 || d[0].Before != "set" || d[0].After != "changed" {
		t.Fatalf("%+v", d)
	}
	if d := ConfigViews(a, a, nil, func(string) bool { return true }); len(d) != 1 {
		t.Fatal(d)
	}
}

func TestVolumesDiff(t *testing.T) {
	before := load(t)
	after := clone(t, before)
	app := &after.Sites[1].Config
	if after.Sites[1].SiteSlug != "app" {
		t.Fatal(after.Sites[1].SiteSlug)
	}
	(*app.Volumes)[0].Backup = false
	*app.Volumes = append(*app.Volumes, model.SiteVolume{VolumeSlug: "extra", Label: "extra", Backup: true})
	got := []string{}
	for _, x := range Configs(before, after, slugs) {
		if x.Section == "volumes" {
			got = append(got, x.Path+":"+x.Kind)
		}
	}
	if j(got) != j([]string{"app/volumes/hello-api-data:changed", "app/volumes/extra:added"}) {
		t.Fatal(got)
	}
	removed := clone(t, before)
	*removed.Sites[1].Config.Volumes = []model.SiteVolume{}
	d := Configs(before, removed, slugs)
	if len(d) != 1 || d[0].Kind != "removed" || d[0].Path != "app/volumes/hello-api-data" || d[0].Section != "volumes" {
		t.Fatalf("%+v", d)
	}
}

// Round E: a seed change of a volume carries the review note.
func TestVolumeSeedNote(t *testing.T) {
	d := "dir"
	mk := func(seed *model.VolumeSeed) *model.Config {
		v := model.SiteVolume{VolumeSlug: "nr", Label: "nr", Backup: true, Kind: &d, Seed: model.Null[model.VolumeSeed]()}
		if seed != nil {
			v.Seed = model.Some(*seed)
		}
		return &model.Config{Sites: []model.Site{{SiteSlug: "app", Config: model.SiteConfig{Volumes: &[]model.SiteVolume{v}}}}}
	}
	ch := Configs(mk(nil), mk(&model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "flows.json", FileSlug: "f"}}}), nil)
	n := 0
	for _, c := range ch {
		if c.Section == "volumes" {
			n++
			if c.Note != VolumeSeedChangedNote {
				t.Errorf("note %q", c.Note)
			}
		}
	}
	if n != 1 {
		t.Fatalf("changes %+v", ch)
	}
}

// §21 #101: tree content is listed per file (with blob refs); the volume row only when other fields change.
func TestVolumeContentPerFile(t *testing.T) {
	d := "dir"
	e := func(p, sha string) model.VolumeTreeEntry {
		return model.VolumeTreeEntry{Path: p, Sha256: sha, Size: 1, Mode: "0644", Render: "none"}
	}
	mk := func(entries []model.VolumeTreeEntry, dirs []string, label string) *model.Config {
		v := model.SiteVolume{VolumeSlug: "nr", Label: label, Backup: true, Kind: &d, Seed: model.Some(model.VolumeSeed{Kind: "tree", Tree: entries, Dirs: dirs})}
		return &model.Config{Sites: []model.Site{{SiteSlug: "app", Config: model.SiteConfig{Volumes: &[]model.SiteVolume{v}}}}}
	}
	a := mk([]model.VolumeTreeEntry{e("a.json", "sha256:a"), e("b.json", "sha256:b")}, []string{"old"}, "nr")
	b := mk([]model.VolumeTreeEntry{e("a.json", "sha256:a2"), e("c.json", "sha256:c")}, []string{"new"}, "nr")
	ch := Configs(a, b, nil)
	got := map[string]string{}
	for _, c := range ch {
		got[c.Path] = c.Kind
		if c.Path == "app/volumes/nr/content/a.json" && (c.Blob == nil || *c.Blob.Before != "sha256:a" || *c.Blob.After != "sha256:a2" || c.Note != VolumeContentChangedNote) {
			t.Errorf("blob %+v", c)
		}
	}
	want := map[string]string{"app/volumes/nr/content/a.json": "changed", "app/volumes/nr/content/b.json": "removed", "app/volumes/nr/content/c.json": "added",
		"app/volumes/nr/content/new/": "added", "app/volumes/nr/content/old/": "removed"}
	if len(got) != len(want) {
		t.Fatalf("changes %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s want %s", k, got[k], v)
		}
	}
	ch = Configs(a, mk([]model.VolumeTreeEntry{e("a.json", "sha256:a"), e("b.json", "sha256:b")}, []string{"old"}, "Node-RED"), nil)
	if len(ch) != 1 || ch[0].Path != "app/volumes/nr" {
		t.Fatalf("label only: %+v", ch)
	}
}

// §21 #103: a static dev-tool password is compared raw but never shown in clear.
func TestDevToolsStaticValueMasked(t *testing.T) {
	a := load(t)
	pw := func(v string) model.DevToolCredential { return model.DevToolCredential{Kind: "literal", Value: &v} }
	p := model.NewDevToolPreset("mqtt", "broker")
	p.Auth.Password = pw("hunter2-secret")
	list := []model.DevToolPreset{p}
	a.Sites[0].Config.DevTools = &list
	b := clone(t, a)
	q := (*b.Sites[0].Config.DevTools)[0].Detached()
	q.Auth.Password = pw("other-value-99")
	(*b.Sites[0].Config.DevTools)[0] = q
	d := Configs(a, b, slugs)
	if len(d) != 1 || d[0].Section != "devTools" || d[0].Kind != "changed" || d[0].Note != DevToolValueChangedNote {
		t.Fatalf("diff %+v", d)
	}
	js, _ := json.Marshal(d)
	if s := string(js); strings.Contains(s, "hunter2-secret") || strings.Contains(s, "other-value-99") || !strings.Contains(s, "••••") {
		t.Fatalf("static value in clear: %s", s)
	}
	// added with a site: masked too
	empty := clone(t, a)
	empty.Sites[0].Config.DevTools = nil
	d = Configs(empty, a, slugs)
	js, _ = json.Marshal(d)
	if len(d) != 1 || d[0].Kind != "added" || strings.Contains(string(js), "hunter2-secret") {
		t.Fatalf("added %s", js)
	}
}
