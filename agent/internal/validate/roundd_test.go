package validate

import (
	"encoding/json"
	"strings"
	"testing"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

// Round D: ports of the shared/src/validate.test.ts "round D" block (legacy data sources → declared site volumes).

const roundCSite = `{"host":null,"agentPort":7080,"agentGuiAliasPort":8080,"depends":[],"notFoundPageId":null,
 "edge":{"httpPort":80,"httpsPort":443,"hosts":[{"hostname":"*","tls":{"mode":"none"},"redirectHttpToHttps":false}],"routes":[]},
 "containers":[
  {"containerSlug":"cassandra","serviceSlug":"redis","version":"v1","enabled":true,"env":[],"ports":[],"volumes":[{"imageVolume":"data","mountPath":null,"source":{"kind":"data","name":"cassandra-data"},"readOnly":true}],"command":null,"entrypoint":null,"restart":"always","resources":{"cpus":null,"memoryMb":null},"healthcheck":null,"depends":[]},
  {"containerSlug":"cache","serviceSlug":"redis","version":"v1","enabled":true,"env":[],"ports":[],"volumes":[],"command":null,"entrypoint":null,"restart":"always","resources":{"cpus":null,"memoryMb":null},"healthcheck":null,"depends":[]}]}`

func redisImages() map[string]model.ImageSpec {
	return map[string]model.ImageSpec{"redis:v1": {ServiceSlug: "redis", Version: "v1", Volumes: []model.ImageVolume{{Name: "data", MountPath: "/data"}}}}
}

func TestRoundDLegacyNormalize(t *testing.T) {
	var sc model.SiteConfig
	if err := json.Unmarshal([]byte(roundCSite), &sc); err != nil {
		t.Fatal(err)
	}
	if !IsLegacySiteConfig(&sc) {
		t.Fatal("round-C config must be legacy")
	}
	n := NormalizeSiteConfigVolumes(sc, redisImages())
	if IsLegacySiteConfig(&n) {
		t.Fatal("normalized config is not legacy")
	}
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	if got := j(n.Containers[0].Volumes); got != `[{"imageVolume":"data","mountPath":null,"source":{"kind":"volume","volumeSlug":"cassandra-data"},"readOnly":false}]` {
		t.Errorf("data → volume, readOnly reset: %s", got)
	}
	if got := j(n.Containers[1].Volumes); got != `[{"imageVolume":"data","mountPath":null,"source":{"kind":"volume","volumeSlug":"cache-data"},"readOnly":false}]` {
		t.Errorf("legacy implicit row made explicit: %s", got)
	}
	if got := j(n.Volumes); got != j([]model.SiteVolume{NewSiteVolume("cassandra-data", "", "", true), NewSiteVolume("cache-data", "", "", true)}) {
		t.Errorf("declared: %s", got)
	}
	// idempotent; the input is not changed
	if j(NormalizeSiteConfigVolumes(n, redisImages())) != j(n) {
		t.Error("not idempotent")
	}
	if sc.Volumes != nil || sc.Containers[0].Volumes[0].Source.Kind != "data" || !sc.Containers[0].Volumes[0].ReadOnly {
		t.Error("input changed")
	}
	// a current config is not auto-declared and keeps missing rows
	cur := n
	cur.Volumes = &[]model.SiteVolume{}
	cur.Containers = []model.Container{n.Containers[1]}
	cur.Containers[0].Volumes = []model.ContainerMount{}
	c2 := NormalizeSiteConfigVolumes(cur, redisImages())
	if len(c2.VolumeList()) != 0 || len(c2.Containers[0].Volumes) != 0 || c2.Volumes == nil {
		t.Errorf("current config changed: %s", j(c2))
	}
	// a current config with a stray data source: converted + declared (TS normalizeSiteConfig)
	stray := cur
	stray.Containers = []model.Container{n.Containers[0]}
	iv := "data"
	stray.Containers[0].Volumes = []model.ContainerMount{{ImageVolume: &iv, Source: model.VolumeSource{Kind: "data", Name: "x1"}, ReadOnly: true}}
	s2 := NormalizeSiteConfigVolumes(stray, redisImages())
	if s2.FindVolume("x1") == nil || s2.Containers[0].Volumes[0].Source.Kind != "volume" || s2.Containers[0].Volumes[0].ReadOnly {
		t.Errorf("stray data: %s", j(s2))
	}
}

// A revision written before round D keeps its hash: the absent `volumes` key and the `data` sources round-trip
// unchanged through the Go model, and NormalizeConfigVolumes returns a copy (the original stays as stored).
func TestRoundDLegacyHashRoundTrip(t *testing.T) {
	raw := `{"variables":[],"secrets":[],"files":[],"sites":[{"siteSlug":"s1","label":"S1","config":` + roundCSite + `}]}`
	var generic any
	_ = json.Unmarshal([]byte(raw), &generic)
	want, err := canonical.HashCanonical(generic)
	if err != nil {
		t.Fatal(err)
	}
	var cfg model.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	got, _ := canonical.HashCanonical(cfg)
	if got != want {
		t.Fatalf("hash %s ≠ %s", got, want)
	}
	b, _ := json.Marshal(cfg.Sites[0].Config)
	if strings.Contains(string(b), `"volumes":[{"volumeSlug"`) || !strings.Contains(string(b), `"kind":"data","name":"cassandra-data"`) {
		t.Fatalf("re-encoded %s", b)
	}
	n := NormalizeConfigVolumes(&cfg, redisImages())
	if n == &cfg || n.Sites[0].Config.Volumes == nil {
		t.Fatal("legacy config must be normalized into a copy")
	}
	if h2, _ := canonical.HashCanonical(cfg); h2 != want {
		t.Fatal("NormalizeConfigVolumes changed the stored config")
	}
	// a current config: same pointer
	if NormalizeConfigVolumes(n, redisImages()) != n {
		t.Fatal("a current config must not be copied")
	}
	// the normalized config validates (agent mode) — no V_REQUIRED / V_ENUM / V_VOLUME_UNDECLARED
	ctx := Context{Mode: ModeAgent, DeploymentID: "d", Services: map[string]string{"redis": "redis"},
		Images: map[string]CatalogImage{"redis:v1": {Kind: "service", Volumes: []model.ImageVolume{{Name: "data", MountPath: "/data"}}}}}
	if e := errCodes(ValidateConfig(n, ctx)); len(e) != 0 {
		t.Errorf("normalized: %v", e)
	}
	if e := errCodes(ValidateConfig(&cfg, ctx)); !contains(e, "V_REQUIRED") || !contains(e, "V_ENUM") {
		t.Errorf("raw legacy config: %v", e)
	}
}

func TestRoundDCheckLocalEditVolumes(t *testing.T) {
	before, _ := load(t, ModeAgent)
	after := clone(t, before)
	app := site(after, "app")
	(*app.Config.Volumes)[0].Backup = false
	*app.Config.Volumes = append(*app.Config.Volumes, NewSiteVolume("extra", "", "", true))
	if l := CheckLocalEdit(before, after); len(l) != 0 {
		t.Errorf("local edits of site volumes are allowed: %+v", l)
	}
	// removing a mounted volume fails validation (V_VOLUME_UNDECLARED)
	_, ctx := load(t, ModeAgent)
	*app.Config.Volumes = []model.SiteVolume{}
	if e := errCodes(ValidateConfig(after, ctx)); !contains(e, "V_VOLUME_UNDECLARED") {
		t.Errorf("remove a used volume: %v", e)
	}
	if DefaultVolumeSlug("cassandra", "data") != "cassandra-data" || len(DefaultVolumeSlug(strings.Repeat("a", 60), strings.Repeat("b", 60))) != 100 {
		t.Error("DefaultVolumeSlug")
	}
	if SuggestVolumeSlug("Cassandra Data!", nil) != "cassandra-data-" || SuggestVolumeSlug("", nil) != "data" {
		t.Errorf("SuggestVolumeSlug %q %q", SuggestVolumeSlug("Cassandra Data!", nil), SuggestVolumeSlug("", nil))
	}
	if d := DefaultSiteConfig(nil); d.Volumes == nil || len(*d.Volumes) != 0 {
		t.Error("DefaultSiteConfig volumes = []")
	}
}

// Round D part 2: storage locations (TS "round D: storage locations of files and site volumes").
func TestRoundDLocations(t *testing.T) {
	auto := &model.StorageLocation{Kind: "auto"}
	pth := func(p string) *model.StorageLocation { return &model.StorageLocation{Kind: "path", Path: p} }
	for _, c := range [][2]string{
		{LocationPath(auto, "files", "banner", ""), "./files/banner"},
		{LocationPath(nil, "volumes", "v", "/var/lib/opm"), "/var/lib/opm/volumes/v"},
		{LocationPath(pth("./cfg/app.json"), "files", "x", "/var/lib/opm/"), "/var/lib/opm/cfg/app.json"},
		{LocationPath(pth("/srv/x"), "files", "x", "/var/lib/opm"), "/srv/x"},
		{LocationLabel(auto, "files", "banner", "/var/lib/opm"), "auto · /var/lib/opm/files/banner"},
	} {
		if c[0] != c[1] {
			t.Errorf("%q ≠ %q", c[0], c[1])
		}
	}
	cfg, ctx := load(t, ModePublish)
	cfg.Files[0].Location = pth("./cfg/banner.txt")
	(*site(cfg, "app").Config.Volumes)[0].Location = pth("/srv/hello-api")
	if e := errCodes(ValidateConfig(clone(t, cfg), ctx)); len(e) != 0 {
		t.Fatalf("custom locations: %v", e)
	}
	bad := func(f, v string) []string {
		d, _ := load(t, ModePublish)
		d.Files[0].Location = pth(f)
		if v != "" {
			(*site(d, "app").Config.Volumes)[0].Location = pth(v)
		}
		out := []string{}
		for _, x := range ValidateConfig(d, ctx).List {
			if x.Code == "V_LOCATION" {
				out = append(out, x.Path)
			}
		}
		return out
	}
	for _, p := range []string{"./../etc/x", "./a/../b", "relative", "/", "./blobs/x", "./volumes/x", "{{ var.x }}", "./a b"} {
		if len(bad(p, "")) == 0 {
			t.Errorf("%s must be V_LOCATION", p)
		}
	}
	if l := bad("./files/custom-name", ""); len(l) != 0 {
		t.Errorf("./files/custom-name: %v", l)
	}
	if len(bad("./data/f", "./data")) == 0 || len(bad("/srv/a", "/srv/a")) == 0 || len(bad("./volumes/hello-api-data", "")) == 0 {
		t.Error("same / nested locations")
	}
	// the agent compares "./" against absolute paths after resolving them (staging)
	r, _ := load(t, ModeAgent)
	r.Files[0].Location = pth("/home/opm/x/banner")
	(*site(r, "app").Config.Volumes)[0].Location = pth("./x")
	if l := CheckResolvedLocations(r, "app", "/home/opm"); len(l) != 1 || l[0].Code != "V_LOCATION" {
		t.Errorf("resolved nested: %+v", l)
	}
	if l := CheckResolvedLocations(r, "db", "/home/opm"); len(l) != 0 {
		t.Errorf("db: %+v", l)
	}
	(*site(r, "app").Config.Volumes)[0].Location = pth("/home")
	if l := CheckResolvedLocations(r, "app", "/home/opm"); len(l) == 0 {
		t.Error("a location that contains the agent home")
	}
	(*site(r, "app").Config.Volumes)[0].Location = pth("/home/opm/blobs/v")
	if l := CheckResolvedLocations(r, "app", "/home/opm"); len(l) == 0 {
		t.Error("an absolute location inside an agent directory")
	}
	if f := NormalizeFileDef(model.FileDef{FileSlug: "f", Editor: "text"}); f.Location == nil || f.Location.Kind != "auto" {
		t.Error("NormalizeFileDef location auto")
	}
}
