package driver

import (
	"os"
	"path/filepath"

	"opm/agent/internal/canonical"
	"opm/agent/internal/expr"
	"opm/agent/internal/validate"
	"strings"
	"testing"

	"opm/agent/internal/model"
)

func fixture(theme string) (*model.Config, *model.RevisionCatalog) {
	p := 8080.0
	hp := "{{ var.port }}"
	v := func(s string) *string { return &s }
	cfg := &model.Config{
		Variables: []model.Variable{{VariableSlug: "theme", Type: "string", Value: model.VariableValue{V: theme}}, {VariableSlug: "port", Type: "number", Value: model.VariableValue{V: 9000.0}}},
		Secrets:   []model.SecretDef{{SecretSlug: "tok", Value: v("secret-xyz")}},
		Files:     []model.FileDef{{FileSlug: "cfg", Editor: "binary", Mime: "application/octet-stream", Content: &model.BlobRef{Sha256: "sha256:" + strings.Repeat("a", 64), SizeBytes: 3}}},
		Sites: []model.Site{{SiteSlug: "app", Config: model.SiteConfig{Host: v("10.0.0.1"), Containers: []model.Container{{ContainerSlug: "web", ServiceSlug: "web", Version: "v1", Enabled: true,
			Env:     []model.ContainerEnv{{Name: "THEME", Value: v("{{ var.theme }}")}, {Name: "TOK", SecretRef: v("tok")}, {Name: "F", Value: v("{{ file.cfg.path }}")}},
			Volumes: []model.ContainerMount{{MountPath: v("/etc/x"), Source: model.VolumeSource{Kind: "file", FileSlug: "cfg"}, ReadOnly: true}, {MountPath: v("/run/tok"), Source: model.VolumeSource{Kind: "secret", SecretSlug: "tok"}, ReadOnly: true}},
			Ports:   []model.ContainerPort{{PortName: "http", HostPort: model.NumOrExpr{Expr: &hp}, HostIP: "0.0.0.0", Edge: true}}, Restart: "never"}}}}},
	}
	cat := &model.RevisionCatalog{Services: map[string]model.CatalogService{"web": {ServiceSlug: "web", Label: "Web"}},
		Images: map[string]model.ImageSpec{"web:v1": {ServiceSlug: "web", Version: "v1", ConfigDigest: "sha256:" + strings.Repeat("b", 64), Kind: "service",
			Ports:   []model.ImagePort{{Name: "http", Port: model.NumOrExpr{Num: &p}, Protocol: "tcp"}},
			Env:     []model.ImageEnv{{Name: "MODE", Value: &model.ImageEnvValue{Str: v("prod-{{ container.slug }}")}}, {Name: "LVL", Value: &model.ImageEnvValue{List: []string{"info", "debug"}}}},
			Volumes: []model.ImageVolume{{Name: "data", MountPath: "/data"}}, StopTimeoutSec: 5}}}
	return cfg, cat
}

func build(t *testing.T, theme string) *Built {
	cfg, cat := fixture(theme)
	b, err := Build(BuildParams{DataDir: "/d", DeploymentID: "Dep", SiteSlug: "app", RevID: "R", Config: cfg, Catalog: cat, Container: &cfg.Sites[0].Config.Containers[0]})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildSpec(t *testing.T) {
	b := build(t, "green")
	env := strings.Join(b.Spec.Env, " ")
	for _, want := range []string{"THEME=green", "TOK=secret-xyz", "F=/etc/x", "MODE=prod-web", "LVL=info"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env %v missing %s", b.Spec.Env, want)
		}
	}
	if b.Name != "web" || b.Spec.Network != "opm_Dep_app" || b.Spec.Restart != "no" {
		t.Fatalf("names %+v", b.Spec)
	}
	if len(b.Spec.Mounts) != 3 || b.Spec.Mounts[0].Target != "/data" || !b.Spec.Mounts[1].ReadOnly {
		t.Fatalf("mounts %+v", b.Spec.Mounts)
	}
	if b.Spec.Labels[LContainer] != "web" || b.Spec.Labels[LService] != "web" || b.Spec.Mounts[1].Sha256 == nil {
		t.Fatalf("labels / file sha %+v", b.Spec)
	}
	if len(b.Spec.Ports) != 2 || b.Spec.Ports[0].HostIP != "0.0.0.0" || b.Spec.Ports[0].HostPort != 9000 || b.Spec.Ports[1].HostIP != "127.0.0.1" {
		t.Fatalf("ports %+v", b.Spec.Ports)
	}
	cc := b.CreateConfig("R", 3)
	if cc.Image != "web:v1" || cc.Labels[LSpecHash] != b.Hash || len(cc.HostConfig.PortBindings["8080/tcp"]) != 2 {
		t.Fatalf("create config %+v", cc)
	}
	if len(b.Secrets) == 0 {
		t.Fatal("secret values must be registered for redaction")
	}
	if b.Hash != build(t, "green").Hash {
		t.Fatal("hash not deterministic")
	}
	if b.Hash == build(t, "yellow").Hash {
		t.Fatal("hash must change with env")
	}
}

func TestPlan(t *testing.T) {
	b := build(t, "green")
	ex := map[string]Existing{"web": {ID: "c1", ContainerSlug: "web", Hash: b.Hash, Running: true}, "old": {ID: "c9", ContainerSlug: "old", Running: true}}
	acts := Plan([]*Built{b}, ex)
	kinds := map[string]string{}
	for _, a := range acts {
		kinds[a.ContainerSlug] = a.Kind
	}
	if kinds["web"] != ActKeep || kinds["old"] != ActRemove {
		t.Fatalf("%v", kinds)
	}
	ex["web"] = Existing{ID: "c1", ContainerSlug: "web", Hash: "old", Running: true}
	if Plan([]*Built{b}, ex)[0].Kind != ActRecreate {
		t.Fatal("hash change → recreate")
	}
	b.Enabled = false
	for _, a := range Plan([]*Built{b}, ex) {
		if a.ContainerSlug == "web" && a.Kind != ActRemove {
			t.Fatal("disabled → remove")
		}
	}
	if Plan([]*Built{build(t, "x")}, map[string]Existing{})[0].Kind != ActCreate {
		t.Fatal("missing → create")
	}
}

// A job that has run (exited) with an unchanged spec is kept - not re-run on every merge; a changed spec re-runs it.
// Keeps is the one rule for Plan and the merge preview's runtime actions.
func TestPlanJobKeptAfterExit(t *testing.T) {
	b := build(t, "green")
	b.Kind = "job"
	for _, c := range []struct {
		ex   Existing
		want string
	}{
		{Existing{ID: "c1", ContainerSlug: b.ContainerSlug, Hash: b.Hash, State: "exited"}, ActKeep},
		{Existing{ID: "c1", ContainerSlug: b.ContainerSlug, Hash: b.Hash, State: "running", Running: true}, ActKeep},
		{Existing{ID: "c1", ContainerSlug: b.ContainerSlug, Hash: "old", State: "exited"}, ActRecreate},
		{Existing{ID: "c1", ContainerSlug: b.ContainerSlug, Hash: b.Hash, State: "created"}, ActRecreate},
	} {
		got := Plan([]*Built{b}, map[string]Existing{b.ContainerSlug: c.ex})[0].Kind
		if got != c.want || Keeps(b, c.ex) != (c.want == ActKeep) {
			t.Fatalf("job %+v: plan %s, keeps %v, want %s", c.ex, got, Keeps(b, c.ex), c.want)
		}
	}
	b.Kind = "service"
	if Keeps(b, Existing{Hash: b.Hash, State: "exited"}) {
		t.Fatal("an exited service is recreated")
	}
}

// Phase U: mount paths with {{ container.env.<NAME> }} resolve to the final env value; a choice env without a container
// value uses its default; file.<slug>.path answers the resolved mount path.
func TestBuildResolvedMountPaths(t *testing.T) {
	cfg, cat := fixture("green")
	v := func(s string) *string { return &s }
	img := cat.Images["web:v1"]
	img.Env = append(img.Env,
		model.ImageEnv{Name: "NODE_ENV", Value: &model.ImageEnvValue{Str: v("integration")}, Options: model.Some([]string{"integration", "dc", "worker"})},
		model.ImageEnv{Name: "REGION", Value: &model.ImageEnvValue{Str: v("eu")}, Options: model.Some([]string{"eu", "us"})})
	img.Volumes = append(img.Volumes, model.ImageVolume{Name: "config", MountPath: "/app/helper/config/env/{{ container.env.NODE_ENV }}.ts"})
	cat.Images["web:v1"] = img
	c := &cfg.Sites[0].Config.Containers[0]
	c.Env = append(c.Env, model.ContainerEnv{Name: "NODE_ENV", Value: v("worker")}, model.ContainerEnv{Name: "CFGDIR", Value: v("conf-{{ container.env.REGION }}")})
	c.Volumes[0].MountPath = v("/etc/{{ container.env.CFGDIR }}/x.json") // file cfg → F={{ file.cfg.path }}
	b, err := Build(BuildParams{DataDir: "/d", DeploymentID: "Dep", SiteSlug: "app", RevID: "R", Config: cfg, Catalog: cat, Container: c})
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]bool{}
	for _, m := range b.Spec.Mounts {
		targets[m.Target] = true
	}
	for _, want := range []string{"/app/helper/config/env/worker.ts", "/etc/conf-eu/x.json", "/data", "/run/tok"} {
		if !targets[want] {
			t.Errorf("mount target %s missing: %+v", want, b.Spec.Mounts)
		}
	}
	env := strings.Join(b.Spec.Env, " ")
	for _, want := range []string{"NODE_ENV=worker", "REGION=eu", "CFGDIR=conf-eu", "F=/etc/conf-eu/x.json", "LVL=info"} {
		if !strings.Contains(env, want) {
			t.Errorf("env %v missing %s", b.Spec.Env, want)
		}
	}
	// a cycle is a build error
	c.Env = append(c.Env, model.ContainerEnv{Name: "A", Value: v("{{ container.env.B }}")}, model.ContainerEnv{Name: "B", Value: v("{{ container.env.A }}")})
	if _, err := Build(BuildParams{DataDir: "/d", DeploymentID: "Dep", SiteSlug: "app", RevID: "R", Config: cfg, Catalog: cat, Container: c}); err == nil || !strings.Contains(err.Error(), "E_CYCLE") {
		t.Fatalf("want E_CYCLE, got %v", err)
	}
}

// The readable docker name <serviceSlug>:<version> is not part of the spec hash: the same image (config digest)
// registered as another version keeps the hash (no recreate), while the container is created from the new name.
func TestReadableNameNotHashed(t *testing.T) {
	b1 := build(t, "green")
	cfg, cat := fixture("green")
	img := cat.Images["web:v1"]
	img.Version = "v7"
	cat.Images = map[string]model.ImageSpec{"web:v7": img}
	cfg.Sites[0].Config.Containers[0].Version = "v7"
	b2, err := Build(BuildParams{DataDir: "/d", DeploymentID: "Dep", SiteSlug: "app", RevID: "R", Config: cfg, Catalog: cat, Container: &cfg.Sites[0].Config.Containers[0]})
	if err != nil {
		t.Fatal(err)
	}
	if b1.Hash != b2.Hash {
		t.Fatal("spec hash must not depend on the readable image name")
	}
	if b1.Readable != "web:v1" || b2.Readable != "web:v7" || b2.CreateConfig("R", 1).Image != "web:v7" {
		t.Fatalf("readable %s %s", b1.Readable, b2.Readable)
	}
	for in, want := range map[string]string{"northwind-backend": "northwind-backend", "Northwind Backend!": "northwind-backend", "": "opm-image", "-x-": "x"} {
		if got := SanitizeRepo(in); got != want {
			t.Errorf("SanitizeRepo(%q) = %q want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"v1": "v1", "3.1.0": "3.1.0", ".hidden": "_hidden", "a b/c": "a-b-c", "": "latest", strings.Repeat("x", 200): strings.Repeat("x", 128)} {
		if got := SanitizeTag(in); got != want {
			t.Errorf("SanitizeTag(%q) = %q want %q", in, got, want)
		}
	}
}

// ───────────────────────── round C: container instances, mounts, rendered files, hash rules

func TestRoundCInstancesMountsAndHash(t *testing.T) {
	dir := t.TempDir()
	v := func(s string) *string { return &s }
	cfg, cat := fixture("green")
	// a text file with expressions (secret inside), mounted by two instances of the same image: rendered ONCE per site
	src := "site={{ self.slug }} theme={{ var.theme }} tok={{ secret.tok }} peer={{ site.db.host }}\n"
	sum := canonical.Sha256Hex([]byte(src))
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FileBlobPath(dir, sum), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Files = append(cfg.Files, model.FileDef{FileSlug: "tpl", Editor: "text", Mime: "text/plain", Render: model.Some("expressions"), Content: &model.BlobRef{Sha256: "sha256:" + sum, SizeBytes: int64(len(src))}})
	cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: "db", Config: model.SiteConfig{Host: nil}})
	web := &cfg.Sites[0].Config.Containers[0]
	web.Volumes = append(web.Volumes, model.ContainerMount{MountPath: v("/etc/tpl.txt"), Source: model.VolumeSource{Kind: "file", FileSlug: "tpl"}, ReadOnly: true})
	two := validate.DuplicateContainer(*web, "web-2")
	two.Env[0].Value = v("yellow")
	f := 9100.0
	two.Ports[0].HostPort = model.NumOrExpr{Num: &f}
	two.Ports[0].HostIP = "127.0.0.1"
	cfg.Sites[0].Config.Containers = append(cfg.Sites[0].Config.Containers, two)
	hosts := func(db string) map[string]validate.SiteHost {
		d := map[string]string{}
		if db != "" {
			d["db"] = db
		}
		return validate.ResolveSiteHosts(cfg, "Dep", validate.HostOptions{Detected: d})
	}
	bp := func(i int, h map[string]validate.SiteHost) BuildParams {
		return BuildParams{DataDir: dir, DeploymentID: "Dep", SiteSlug: "app", RevID: "R", Config: cfg, Catalog: cat, Container: &cfg.Sites[0].Config.Containers[i], Hosts: h}
	}
	// db host auto and unknown → the rendered file fails with E_NULL (staging error with file:line:col)
	if _, err := Build(bp(0, hosts(""))); err == nil || !strings.Contains(err.Error(), "V_FILE_RENDER tpl:1:") || !strings.Contains(err.Error(), "E_NULL") {
		t.Fatalf("want V_FILE_RENDER E_NULL, got %v", err)
	}
	h := hosts("10.9.9.9")
	b1, err := Build(bp(0, h))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Build(bp(1, h))
	if err != nil {
		t.Fatal(err)
	}
	if b1.Name != "web" || b2.Name != "web-2" || b2.Spec.Alias != "web-2" || b2.Spec.Labels[LContainer] != "web-2" || b2.Spec.Labels[LService] != "web" {
		t.Fatalf("names %s %s %+v", b1.Name, b2.Name, b2.Spec.Labels)
	}
	if b1.Readable != b2.Readable || b1.Spec.Image != b2.Spec.Image {
		t.Fatal("instances share the image")
	}
	tplPath := filepath.Join(dir, "files", "tpl")
	r1, r2 := b1.SiteFiles[tplPath], b2.SiteFiles[tplPath]
	if string(r1.Bytes) != "site=app theme=green tok=secret-xyz peer=10.9.9.9\n" || string(r2.Bytes) != string(r1.Bytes) || r1.Mode != 0o400 || r1.Sha != r2.Sha {
		t.Fatalf("rendered once per site %q %q %v", r1.Bytes, r2.Bytes, r1.Mode)
	}
	src1, src2 := "", ""
	for _, m := range b1.Spec.Mounts {
		if m.Target == "/etc/tpl.txt" {
			src1 = m.Source
		}
	}
	for _, m := range b2.Spec.Mounts {
		if m.Target == "/etc/tpl.txt" {
			src2 = m.Source
		}
	}
	if src1 != tplPath || src2 != tplPath {
		t.Fatalf("one host file for both containers: %s %s", src1, src2)
	}
	// data volumes per instance, secret files per instance
	var d1, d2 string
	for _, m := range b1.Spec.Mounts {
		if m.Target == "/data" {
			d1 = m.Source
		}
	}
	for _, m := range b2.Spec.Mounts {
		if m.Target == "/data" {
			d2 = m.Source
		}
	}
	if d1 != VolumeDir(dir, "web-data") || d2 != VolumeDir(dir, "web-2-data") {
		t.Fatalf("data dirs %s %s", d1, d2)
	}
	if _, ok := b2.SecretFiles[filepath.Join(dir, "secrets", "tok")]; !ok {
		t.Fatalf("secret path %v", b2.SecretFiles)
	}
	// published port with hostIp
	found := false
	for _, p := range b2.Spec.Ports {
		found = found || (p.HostIP == "127.0.0.1" && p.HostPort == 9100)
	}
	if !found {
		t.Fatalf("ports %+v", b2.Spec.Ports)
	}
	// PrepareHost: one copy 0400 (secret content), placed atomically; a staged copy is renamed in place; an old
	// per-container secrets/<name>/ directory with the same name is replaced
	if err := os.MkdirAll(filepath.Join(dir, "secrets", "tok", "old"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(FileBlobPath(dir, strings.Repeat("a", 64)), []byte("abc"), 0o644) // the fixture's binary file "cfg"
	if err := StageSiteFile(tplPath, r1, "R"); err != nil {
		t.Fatal(err)
	}
	if err := b1.PrepareHost(dir, "R"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StagedPath(tplPath, "R")); !os.IsNotExist(err) {
		t.Fatal("staged copy must be renamed in place")
	}
	st, err := os.Stat(tplPath)
	if err != nil || st.Mode().Perm() != 0o400 {
		t.Fatalf("site file mode %v %v", st, err)
	}
	ino := st.Sys()
	if err := b2.PrepareHost(dir, "R"); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(tplPath); !os.SameFile(st, st2) {
		t.Fatalf("an unchanged copy keeps its inode %v", ino)
	}
	if st, err := os.Stat(filepath.Join(dir, "secrets", "tok")); err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o400 {
		t.Fatalf("secret copy %v %v", st, err)
	}
	if b, _ := os.ReadFile(tplPath); string(b) != string(r1.Bytes) {
		t.Fatalf("content %q", b)
	}
	// the secret is registered for redaction even if it only appears in the rendered file
	has := false
	for _, s := range b1.Secrets {
		has = has || s == "secret-xyz"
	}
	if !has {
		t.Fatal("rendered secret must be redacted")
	}
	// hash rules: changes with the rendered file (peer address used), hostIp, entrypoint, healthcheck
	base := b2.Hash
	hb, _ := Build(bp(1, hosts("10.9.9.8")))
	if hb.Hash == base {
		t.Error("hash must change when a USED auto address changes the rendered file")
	}
	c := &cfg.Sites[0].Config.Containers[1]
	c.Ports[0].HostIP = "0.0.0.0"
	if x, _ := Build(bp(1, h)); x.Hash == base {
		t.Error("hostIp")
	}
	c.Ports[0].HostIP = "127.0.0.1"
	c.Entrypoint = []string{"/bin/sh", "-c"}
	if x, _ := Build(bp(1, h)); x.Hash == base || x.Spec.Entrypoint[0] != "/bin/sh" {
		t.Error("entrypoint")
	}
	c.Entrypoint = nil
	c.Healthcheck = &model.HealthCheck{Type: "tcp", PortName: "http", IntervalSec: 1, TimeoutSec: 1, Retries: 1}
	if x, _ := Build(bp(1, h)); x.Hash == base || x.Health.Type != "tcp" {
		t.Error("healthcheck")
	}
	c.Healthcheck = nil
	if x, _ := Build(bp(1, h)); x.Hash != base {
		t.Error("hash must be back to base")
	}
	// an auto address that nothing uses does not change the hash
	cfg.Files = cfg.Files[:1]
	cfg.Sites[0].Config.Containers[1].Volumes = cfg.Sites[0].Config.Containers[1].Volumes[:2]
	x1, _ := Build(bp(1, hosts("10.0.0.1")))
	x2, _ := Build(bp(1, hosts("10.0.0.2")))
	if x1.Hash != x2.Hash {
		t.Error("an unused auto address must not change the hash")
	}
	// volume sources: none = no mount; host bind (must exist); shared data volume
	c = &cfg.Sites[0].Config.Containers[1]
	iv := "data"
	c.Volumes = append(c.Volumes, model.ContainerMount{ImageVolume: &iv, Source: model.VolumeSource{Kind: "none"}})
	if x, _ := Build(bp(1, h)); len(x.DataDirs) != 0 {
		t.Errorf("none: %v", x.DataDirs)
	}
	c.Volumes[len(c.Volumes)-1].Source = model.VolumeSource{Kind: "host", Path: filepath.Join(dir, "nope")}
	if _, err := Build(bp(1, h)); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("host path: %v", err)
	}
	c.Volumes[len(c.Volumes)-1].Source = model.VolumeSource{Kind: "host", Path: dir}
	c.Volumes[len(c.Volumes)-1].ReadOnly = true
	x, err := Build(bp(1, h))
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, m := range x.Spec.Mounts {
		ok = ok || (m.Source == dir && m.Target == "/data" && m.ReadOnly)
	}
	if !ok {
		t.Errorf("host bind %+v", x.Spec.Mounts)
	}
}

func TestRenderFileJSON(t *testing.T) {
	ectx := &expr.Context{Var: map[string]any{"theme": "green"}}
	f := &model.FileDef{FileSlug: "j", Mime: "application/json", Editor: "text"}
	if _, err := RenderSiteFileFor(f, `{"t": "{{ var.theme }}"}`, ectx, "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderSiteFileFor(f, `{"t": {{ var.theme }}}`, ectx, "app"); err == nil || !strings.HasPrefix(err.Error(), "V_FILE_JSON j:") {
		t.Fatalf("json: %v", err)
	}
	if _, err := RenderSiteFileFor(f, `{"t": "{{ container.slug }}"}`, ectx, "app"); err == nil || !strings.Contains(err.Error(), "E_NOT_ALLOWED") || !strings.Contains(err.Error(), "(site app)") {
		t.Fatalf("container.*: %v", err)
	}
}

// Round D: declared site volumes. A missing image-volume row is not mounted; a volume source binds
// <dataDir>/vol/<volumeSlug>; two containers share one volume; an undeclared volume fails Build; a legacy config (no
// `volumes`) keeps its implicit data volume and its data source is mounted read-write.
func TestBuildSiteVolumes(t *testing.T) {
	cfg, cat := fixture("green")
	sc := &cfg.Sites[0].Config
	sc.Volumes = &[]model.SiteVolume{{VolumeSlug: "shared", Label: "Shared", Backup: true}}
	web2 := sc.Containers[0]
	web2.ContainerSlug = "web-2"
	web2.Ports = []model.ContainerPort{{PortName: "http", HostIP: "0.0.0.0", Edge: true}}
	sc.Containers = append(sc.Containers, web2)
	bp := func(i int) BuildParams {
		return BuildParams{DataDir: "/d", DeploymentID: "Dep", SiteSlug: "app", RevID: "R", Config: cfg, Catalog: cat, Container: &cfg.Sites[0].Config.Containers[i]}
	}
	b, err := Build(bp(0))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range b.Spec.Mounts {
		if m.Target == "/data" {
			t.Fatalf("an image volume without a row must not be mounted: %+v", b.Spec.Mounts)
		}
	}
	iv := "data"
	for i := range sc.Containers {
		sc.Containers[i].Volumes = append(sc.Containers[i].Volumes, model.ContainerMount{ImageVolume: &iv, Source: model.VolumeSource{Kind: "volume", VolumeSlug: "shared"}})
	}
	srcs := []string{}
	for i := range sc.Containers {
		x, err := Build(bp(i))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range x.Spec.Mounts {
			if m.Target == "/data" {
				if m.ReadOnly {
					t.Error("a volume mount defaults to read-write")
				}
				srcs = append(srcs, m.Source)
			}
		}
		if len(x.DataDirs) != 1 || x.DataDirs[0] != VolumeDir("/d", "shared") {
			t.Errorf("data dirs %v", x.DataDirs)
		}
	}
	if len(srcs) != 2 || srcs[0] != VolumeDir("/d", "shared") || srcs[1] != srcs[0] {
		t.Fatalf("shared volume binds %v", srcs)
	}
	if VolumeDir("/d", "shared") != filepath.Join("/d", "volumes", "shared") {
		t.Fatal(VolumeDir("/d", "shared"))
	}
	*sc.Volumes = []model.SiteVolume{}
	if _, err := Build(bp(0)); err == nil || !strings.Contains(err.Error(), "V_VOLUME_UNDECLARED") {
		t.Fatalf("undeclared: %v", err)
	}
	// legacy (round C): no volumes list, a data source stored read-only, the second container without a row
	sc.Volumes = nil
	sc.Containers[0].Volumes[len(sc.Containers[0].Volumes)-1] = model.ContainerMount{ImageVolume: &iv, Source: model.VolumeSource{Kind: "data", Name: "old-data"}, ReadOnly: true}
	sc.Containers[1].Volumes = sc.Containers[1].Volumes[:len(sc.Containers[1].Volumes)-1]
	want := map[int]string{0: VolumeDir("/d", "old-data"), 1: VolumeDir("/d", "web-2-data")}
	for i, w := range want {
		x, err := Build(bp(i))
		if err != nil {
			t.Fatal(err)
		}
		ok := false
		for _, m := range x.Spec.Mounts {
			ok = ok || (m.Target == "/data" && m.Source == w && !m.ReadOnly)
		}
		if !ok {
			t.Errorf("legacy %d: %+v", i, x.Spec.Mounts)
		}
	}
	if sc.Volumes != nil || sc.Containers[0].Volumes[len(sc.Containers[0].Volumes)-1].Source.Kind != "data" {
		t.Fatal("Build must not change the stored config")
	}
}

// A mounted file on the host with another mode than it should have (only the mode changed: same bytes, same spec
// hash) recreates the container — a file is written only at create, so keeping it would keep the old 0400 copy.
func TestPlanRecreatesOnFileModeChange(t *testing.T) {
	b := build(t, "green")
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte("{}"), 0o400); err != nil {
		t.Fatal(err)
	}
	b.SiteFiles = map[string]SiteFile{p: {FileSlug: "cfg", Bytes: []byte("{}"), Mode: 0o444}}
	ex := Existing{ID: "c1", ContainerSlug: b.ContainerSlug, Hash: b.Hash, State: "running", Running: true}
	if got := Plan([]*Built{b}, map[string]Existing{b.ContainerSlug: ex})[0].Kind; got != ActRecreate {
		t.Fatalf("0400 on disk, 0444 wanted: plan %s, want recreate", got)
	}
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	if !Keeps(b, ex) {
		t.Fatal("the right mode on disk: kept")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if !Keeps(b, ex) {
		t.Fatal("a missing file is written at create, not a reason to recreate")
	}
}
