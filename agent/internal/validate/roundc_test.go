package validate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// Round C: ports of the shared/src/validate.test.ts "round C" block (auto host, container instances, rendered files).

func sp(v string) *string { return &v }

func cont(c *model.Config, siteSlug, containerSlug string) *model.Container {
	s := site(c, siteSlug)
	for i := range s.Config.Containers {
		if s.Config.Containers[i].ContainerSlug == containerSlug {
			return &s.Config.Containers[i]
		}
	}
	return nil
}

func errCodes(is *Issues) []string {
	out := []string{}
	for _, x := range is.Errors() {
		out = append(out, x.Code)
	}
	return out
}

func TestRoundCAutoHost(t *testing.T) {
	cfg, _ := load(t, ModePublish)
	site(cfg, "app").Config.Host = nil
	for _, m := range []Mode{ModeTemplate, ModeDraft, ModePublish} {
		_, ctx := load(t, m)
		for _, x := range ValidateConfig(clone(t, cfg), ctx).List {
			if strings.HasSuffix(x.Path, ".host") {
				t.Errorf("%s: %+v", m, x)
			}
		}
	}
	if h := ResolveSiteHosts(cfg, "d", HostOptions{Placeholder: true})["app"].Host; h == nil || *h != expr.AutoHostPlaceholder("app") {
		t.Errorf("placeholder %v", h)
	}
	if h := ResolveSiteHosts(cfg, "d", HostOptions{Detected: map[string]string{"app": "10.0.0.7"}})["app"].Host; h == nil || *h != "10.0.0.7" {
		t.Errorf("detected %v", h)
	}
	if h := ResolveSiteHosts(cfg, "d", HostOptions{})["app"].Host; h != nil {
		t.Errorf("agent: %v", *h)
	}
	db := &site(cfg, "db").Config.Containers[0]
	db.Env = append(db.Env, model.ContainerEnv{Name: "PEER", Value: sp("{{ site.app.host }}")})
	_, pctx := load(t, ModePublish)
	if e := errCodes(ValidateConfig(clone(t, cfg), pctx)); len(e) != 0 {
		t.Errorf("publish (placeholder): %v", e)
	}
	_, actx := load(t, ModeAgent)
	if e := errCodes(ValidateConfig(clone(t, cfg), actx)); !contains(e, "E_NULL") {
		t.Errorf("agent without address: want E_NULL, got %v", e)
	}
	actx.DetectedHosts = map[string]string{"app": "10.0.0.7"}
	if e := errCodes(ValidateConfig(clone(t, cfg), actx)); len(e) != 0 {
		t.Errorf("agent with address: %v", e)
	}
}

func TestRoundCHostFallback(t *testing.T) {
	cfg, _ := load(t, ModePublish)
	cfg.Variables = append(cfg.Variables, model.Variable{VariableSlug: "server-ip", Label: "Server IP", Type: "string"})
	site(cfg, "app").Config.Host = sp("{{ var.server-ip }}")
	for _, m := range []Mode{ModeDraft, ModePublish} {
		_, ctx := load(t, m)
		var l []model.ValidationIssue
		for _, x := range ValidateConfig(clone(t, cfg), ctx).List {
			if x.Path == "sites[app].config.host" {
				l = append(l, x)
			}
		}
		if len(l) != 1 || l[0].Code != "V_HOST_AUTO" || l[0].Severity != "warning" || !strings.Contains(l[0].Message, "var server-ip has no value — leave host on auto or set the variable") {
			t.Errorf("%s: %+v", m, l)
		}
	}
	if h := ResolveSiteHosts(cfg, "d", HostOptions{Detected: map[string]string{"app": "192.168.1.5"}})["app"].Host; h == nil || *h != "192.168.1.5" {
		t.Error("fallback to detected")
	}
	cfg.Variables[len(cfg.Variables)-1].Value = model.VariableValue{V: "10.1.1.1"}
	if h := ResolveSiteHosts(cfg, "d", HostOptions{})["app"].Host; h == nil || *h != "10.1.1.1" {
		t.Error("var value")
	}
}

func TestRoundCSameImageTwice(t *testing.T) {
	const A, W = "hello-api", "hello-web"
	cfg, ctx := load(t, ModePublish)
	app := site(cfg, "app")
	taken := []string{}
	for _, c := range app.Config.Containers {
		taken = append(taken, c.ContainerSlug)
	}
	two := DuplicateContainer(*cont(cfg, "app", A), SuggestContainerSlug(A, taken))
	if two.ContainerSlug != "hello-api-2" {
		t.Fatal(two.ContainerSlug)
	}
	for _, v := range two.Volumes {
		if v.ImageVolume != nil && *v.ImageVolume == "data" && (v.Source.Kind != "volume" || v.Source.VolumeSlug != "hello-api-2-data") {
			t.Fatal(v.Source)
		}
	}
	tmp := app.Config
	tmp.Containers = append(append([]model.Container{}, app.Config.Containers...), two)
	if u := UndeclaredVolumes(&tmp); strings.Join(u, ",") != "hello-api-2-data" {
		t.Fatalf("undeclared %v", u)
	}
	*app.Config.Volumes = append(*app.Config.Volumes, NewSiteVolume("hello-api-2-data", "", "", true))
	for i := range two.Env {
		if two.Env[i].Name == "GREETING" {
			two.Env[i].Value = sp("second")
		}
	}
	f := 18080.0
	two.Ports[0].HostPort = model.NumOrExpr{Num: &f}
	app.Config.Containers = append(app.Config.Containers, two)
	if e := errCodes(ValidateConfig(clone(t, cfg), ctx)); len(e) != 0 {
		t.Fatalf("two instances: %v", e)
	}
	ec := ContainerExprContext(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, nil, "app", &two, nil)
	keys := []string{}
	for k := range ec.Site["app"].Port {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "hello-api,hello-api-2,hello-web" || *ec.Site["app"].Port["hello-api-2"]["http"] != 18080 || ec.Container.Slug != "hello-api-2" {
		t.Fatalf("site map %v", keys)
	}
	_ = W
	dup := clone(t, cfg)
	site(dup, "app").Config.Containers = append(site(dup, "app").Config.Containers, two)
	found := false
	for _, x := range ValidateConfig(dup, ctx).List {
		if x.Code == "V_DUPLICATE" && strings.HasSuffix(x.Path, ".containerSlug") {
			found = true
		}
	}
	if !found {
		t.Error("duplicate containerSlug")
	}
	// host port twice
	d, _ := load(t, ModePublish)
	t2 := DuplicateContainer(site(d, "app").Config.Containers[0], "api-b")
	site(d, "app").Config.Containers[0].Ports[0].HostPort = model.NumOrExpr{Num: &f}
	t2.Ports[0].HostPort = model.NumOrExpr{Num: &f}
	site(d, "app").Config.Containers = append(site(d, "app").Config.Containers, t2)
	if e := errCodes(ValidateConfig(clone(t, d), ctx)); !contains(e, "V_PORT_CONFLICT") {
		t.Errorf("port conflict: %v", e)
	}
	last := &site(d, "app").Config.Containers[len(site(d, "app").Config.Containers)-1]
	last.Ports[0].HostPort = model.NumOrExpr{}
	last.Depends = []model.ContainerRef{{ContainerSlug: "nope"}}
	if e := errCodes(ValidateConfig(clone(t, d), ctx)); !contains(e, "V_REF") {
		t.Errorf("depends: %v", e)
	}
	// reserved name
	r, _ := load(t, ModePublish)
	site(r, "db").Config.Containers[0].ContainerSlug = "new"
	if e := errCodes(ValidateConfig(r, ctx)); !contains(e, "V_RESERVED") {
		t.Errorf("reserved: %v", e)
	}
}

func TestRoundCEdgeTargets(t *testing.T) {
	cfg, ctx := load(t, ModePublish)
	cont(cfg, "app", "hello-web").Ports[0].Edge = false
	ok := false
	for _, x := range ValidateConfig(cfg, ctx).List {
		if x.Code == "V_EDGE_ROUTE" && strings.Contains(x.Path, "routes[web]") {
			ok = true
		}
	}
	if !ok {
		t.Error("closed port")
	}
	d, _ := load(t, ModePublish)
	site(d, "app").Config.Edge.Routes[0].Action.ContainerSlug = "ghost"
	ok = false
	for _, x := range ValidateConfig(d, ctx).List {
		if x.Code == "V_REF" && strings.Contains(x.Path, "action.containerSlug") {
			ok = true
		}
	}
	if !ok {
		t.Error("ghost target")
	}
}

func TestRoundCVolumeRows(t *testing.T) {
	const A = "hello-api"
	cfg, ctx := load(t, ModePublish)
	api := cont(cfg, "app", A)
	api.Volumes = append(api.Volumes,
		model.ContainerMount{ImageVolume: sp("nope"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "hello-api-data"}},
		model.ContainerMount{MountPath: sp("/host"), Source: model.VolumeSource{Kind: "host", Path: "relative"}},
		model.ContainerMount{Source: model.VolumeSource{Kind: "none"}},
		model.ContainerMount{MountPath: sp("/s"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "Bad Name"}},
		model.ContainerMount{MountPath: sp("/u"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "undeclared"}},
		model.ContainerMount{MountPath: sp("/f"), Source: model.VolumeSource{Kind: "file", FileSlug: "nope"}},
		model.ContainerMount{MountPath: sp("/old"), Source: model.VolumeSource{Kind: "data", Name: "x"}},
	)
	cs := codes(ValidateConfig(cfg, ctx))
	for _, k := range []string{"V_REF", "V_VOLUME", "V_REQUIRED", "V_FORMAT", "V_VOLUME_UNDECLARED", "V_READONLY", "V_ENUM"} {
		if !contains(cs, k) {
			t.Errorf("%s in %v", k, cs)
		}
	}
	d, _ := load(t, ModePublish)
	a := cont(d, "app", A)
	img := ctx.Images[A+":v1"]
	a.Volumes[0].Source = model.VolumeSource{Kind: "none"}
	if n := len(ContainerMounts(a, img.Volumes)); n != 0 {
		t.Errorf("none: %d mounts", n)
	}
	a.Volumes[0].Source = model.VolumeSource{Kind: "host", Path: "/srv/hello"}
	b := DuplicateContainer(*a, "api-b")
	b.Volumes[0].Source = model.VolumeSource{Kind: "volume", VolumeSlug: "shared"}
	a.Volumes[0].Source = model.VolumeSource{Kind: "volume", VolumeSlug: "shared"}
	app := site(d, "app")
	app.Config.Containers = append(app.Config.Containers, b)
	all := ValidateConfig(clone(t, d), ctx).List
	unused := false
	for _, x := range all {
		if x.Severity == "warning" && x.Code == "V_VOLUME_UNUSED" && strings.HasSuffix(x.Path, "volumes[hello-api-data]") {
			unused = true
		}
	}
	if !unused {
		t.Errorf("V_VOLUME_UNUSED missing: %+v", all)
	}
	if e := errCodes(ValidateConfig(clone(t, d), ctx)); !contains(e, "V_VOLUME_UNDECLARED") {
		t.Errorf("shared is not declared yet: %v", e)
	}
	*app.Config.Volumes = append(*app.Config.Volumes, NewSiteVolume("shared", "Shared data", "", true))
	if e := errCodes(ValidateConfig(clone(t, d), ctx)); len(e) != 0 {
		t.Errorf("shared volume: %v", e)
	}
	us := []string{}
	for _, u := range VolumeUsers(&app.Config)["shared"] {
		us = append(us, u.ContainerSlug)
	}
	if strings.Join(us, ",") != A+",api-b" {
		t.Errorf("users %v", us)
	}
	noDecl := app.Config
	noDecl.Volumes = &[]model.SiteVolume{}
	if u := UndeclaredVolumes(&noDecl); strings.Join(u, ",") != "shared" {
		t.Errorf("undeclared %v", u)
	}
	// missing rows = not mounted (warning)
	a = cont(d, "app", A)
	a.Volumes = []model.ContainerMount{}
	if m := ContainerMounts(a, img.Volumes); len(m) != 0 {
		t.Errorf("missing row mounted: %+v", m)
	}
	nm := false
	for _, x := range ValidateConfig(clone(t, d), ctx).List {
		if x.Severity == "warning" && x.Code == "V_VOLUME_NOT_MOUNTED" && strings.HasSuffix(x.Path, "containers["+A+"].volumes[data]") {
			nm = true
		}
	}
	if !nm {
		t.Error("V_VOLUME_NOT_MOUNTED missing")
	}
	// site volume fields
	*app.Config.Volumes = append(*app.Config.Volumes, model.SiteVolume{VolumeSlug: "shared", Label: strings.Repeat("x", 81)})
	vc := codes(ValidateConfig(clone(t, d), ctx))
	for _, k := range []string{"V_DUPLICATE", "V_LENGTH"} {
		if !contains(vc, k) {
			t.Errorf("%s in %v", k, vc)
		}
	}
	if s := SuggestVolumeSlug("hello-api-data", []string{"hello-api-data"}); s != "hello-api-data-2" {
		t.Error(s)
	}
	// volumes missing → V_REQUIRED; too many → V_TOO_MANY
	app.Config.Volumes = nil
	if e := codes(ValidateConfig(clone(t, d), ctx)); !contains(e, "V_REQUIRED") {
		t.Errorf("missing volumes: %v", e)
	}
	many := []model.SiteVolume{}
	for i := 0; i < 101; i++ {
		many = append(many, NewSiteVolume(fmt.Sprintf("v%d", i), "", "", true))
	}
	app.Config.Volumes = &many
	if e := codes(ValidateConfig(clone(t, d), ctx)); !contains(e, "V_TOO_MANY") {
		t.Errorf("too many: %v", e)
	}
}

func TestRoundCFiles(t *testing.T) {
	cfg, ctx := load(t, ModeAgent)
	banner := "Hi from {{ self.slug }} theme={{ var.theme }}\npw={{ secret.api-token }} lit={{ \"{{\" }}x}}\ndb={{ site.db.host }}"
	ctx.DetectedHosts = map[string]string{"app": "10.0.0.1", "db": "10.0.0.2"}
	ctx.FileContents = map[string]string{"banner": banner}
	if e := ValidateConfig(clone(t, cfg), ctx).Errors(); len(e) != 0 {
		t.Fatalf("base: %+v", e)
	}
	hosts := ResolveSiteHosts(cfg, ctx.DeploymentID, HostOptions{Detected: ctx.DetectedHosts})
	sm := SiteExprMap(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, hosts)
	ectx := SiteFileExprContext(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, nil, "app", sm)
	r := RenderSiteFile(banner, ectx)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if r.Value != "Hi from app theme=green\npw="+*cfg.Secrets[0].Value+" lit={{x}}\ndb="+*sm["db"].Host || !r.Secret || !strings.Contains(r.Masked, "pw=••••") {
		t.Errorf("value %q masked %q", r.Value, r.Masked)
	}
	// container.* / file.* are not allowed: one copy is shared by every container of the site
	for _, src := range []string{"x={{ container.env.THEME }}", "p={{ file.banner.path }}", "{{ container.slug }}"} {
		ctx.FileContents = map[string]string{"banner": src}
		var v *model.ValidationIssue
		for _, x := range ValidateConfig(clone(t, cfg), ctx).Errors() {
			if x.Code == "V_FILE_RENDER" {
				x := x
				v = &x
			}
		}
		if v == nil || !regexp.MustCompile(`E_NOT_ALLOWED .*files are shared by all containers on the site — use a variable instead`).MatchString(v.Message) {
			t.Errorf("%s: %+v", src, v)
		}
	}
	if ci := CheckFileContent("a {{ container.env.X }}"); len(ci) == 0 || ci[0].Code != "E_NOT_ALLOWED" {
		t.Errorf("check container: %+v", ci)
	}
	ctx.FileContents = map[string]string{"banner": "line one\n  x={{ var.nope }}"}
	n := 0
	var v *model.ValidationIssue
	for _, x := range ValidateConfig(clone(t, cfg), ctx).Errors() {
		if x.Code == "V_FILE_RENDER" {
			x := x
			v = &x
			n++
		}
	}
	if n != 1 || !regexp.MustCompile(`^banner:2:8: E_UNKNOWN_PATH .*\(site app\)`).MatchString(v.Message) {
		t.Errorf("V_FILE_RENDER (once per site): %d %+v", n, v)
	}
	ci := CheckFileContent("a\nb {{ nope.x }} {{ var.ok }}")
	if len(ci) != 1 || ci[0].Code != "E_UNKNOWN_PATH" || ci[0].Pos != 7 || ci[0].Line != 2 || ci[0].Col != 6 {
		t.Errorf("check: %+v", ci)
	}
	if ci := CheckFileContent("{{ var.a | upper }}\n{{ \"{{\" }}"); len(ci) != 0 {
		t.Errorf("check ok: %+v", ci)
	}
	if ci := CheckFileContent("{{ var.a\n}}"); len(ci) == 0 || ci[0].Code != "E_SYNTAX" {
		t.Errorf("check syntax: %+v", ci)
	}
	// JSON stays JSON
	j := clone(t, cfg)
	j.Files[0].Mime = "application/json"
	ctx.FileContents = map[string]string{"banner": `{"theme": "{{ var.theme }}"}`}
	if e := ValidateConfig(clone(t, j), ctx).Errors(); len(e) != 0 {
		t.Errorf("json ok: %+v", e)
	}
	ctx.FileContents = map[string]string{"banner": `{"theme": {{ var.theme }}}`}
	if e := errCodes(ValidateConfig(clone(t, j), ctx)); !contains(e, "V_FILE_JSON") {
		t.Errorf("json bad: %v", e)
	}
	// render none: not checked; binary + expressions refused
	j.Files[0].Render = model.Some("none")
	ctx.FileContents = map[string]string{"banner": "{{ nope"}
	if e := ValidateConfig(clone(t, j), ctx).Errors(); len(e) != 0 {
		t.Errorf("none: %+v", e)
	}
	j.Files[0].Editor = "binary"
	j.Files[0].Render = model.Some("expressions")
	if e := errCodes(ValidateConfig(clone(t, j), ctx)); !contains(e, "V_ENUM") {
		t.Errorf("binary: %v", e)
	}
}

func TestRoundCLocalEdit(t *testing.T) {
	cfg, _ := load(t, ModeAgent)
	a := clone(t, cfg)
	site(a, "app").Config.Containers[0].ContainerSlug = "renamed"
	// §21 #113: renames and image changes are local edits
	if l := CheckLocalEdit(cfg, a); len(l) != 0 {
		t.Errorf("rename: %+v", l)
	}
	b := clone(t, cfg)
	site(b, "app").Config.Containers[0].ServiceSlug = "redis"
	if l := CheckLocalEdit(cfg, b); len(l) != 0 {
		t.Errorf("service change: %+v", l)
	}
	// everything else of an existing container may change
	c := clone(t, cfg)
	x := &site(c, "app").Config.Containers[0]
	x.Entrypoint = []string{"/bin/sh"}
	x.Ports[0].HostIP = "127.0.0.1"
	x.Volumes[0].Source = model.VolumeSource{Kind: "volume", VolumeSlug: "other"}
	if l := CheckLocalEdit(cfg, c); len(l) != 0 {
		t.Errorf("editable: %+v", l)
	}
}
