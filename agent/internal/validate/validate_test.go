package validate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"opm/agent/internal/model"
)

type fixture struct {
	DeploymentID string       `json:"deploymentId"`
	Config       model.Config `json:"config"`
}
type catFixture struct {
	Services map[string]struct {
		ServiceSlug string `json:"serviceSlug"`
	} `json:"services"`
	Images map[string]struct {
		Kind        string              `json:"kind"`
		Ports       []model.ImagePort   `json:"ports"`
		Env         []model.ImageEnv    `json:"env"`
		Volumes     []model.ImageVolume `json:"volumes"`
		Healthcheck *model.HealthCheck  `json:"healthcheck"`
	} `json:"images"`
	NotFoundPageIDs []string `json:"notFoundPageIds"`
}

func load(t *testing.T, mode Mode) (*model.Config, Context) {
	t.Helper()
	var f fixture
	var c catFixture
	b, err := os.ReadFile("../../../shared/testdata/hello-config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile("../../../shared/testdata/hello-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	ctx := Context{Mode: mode, DeploymentID: f.DeploymentID, Services: map[string]string{}, Images: map[string]CatalogImage{}, NotFoundPageIDs: c.NotFoundPageIDs}
	for slug := range c.Services {
		ctx.Services[slug] = slug
	}
	for k, im := range c.Images {
		ctx.Images[k] = CatalogImage{Kind: im.Kind, Ports: im.Ports, Env: im.Env, Volumes: im.Volumes, Healthcheck: im.Healthcheck}
	}
	return &f.Config, ctx
}

func clone(t *testing.T, c *model.Config) *model.Config {
	b, _ := json.Marshal(c)
	var out model.Config
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func hasIssue(is *Issues, code, sev string) bool {
	for _, x := range is.List {
		if x.Code == code && (sev == "" || x.Severity == sev) {
			return true
		}
	}
	return false
}

func site(c *model.Config, slug string) *model.Site {
	for i := range c.Sites {
		if c.Sites[i].SiteSlug == slug {
			return &c.Sites[i]
		}
	}
	return nil
}

func TestHelloValid(t *testing.T) {
	for _, m := range []Mode{ModeAgent, ModePublish, ModeDraft} {
		cfg, ctx := load(t, m)
		is := ValidateConfig(cfg, ctx)
		if !is.Ok() {
			t.Fatalf("mode %s: %+v", m, is.Errors())
		}
		if !is.Report().Ok {
			t.Fatal("report")
		}
	}
}

func TestOrders(t *testing.T) {
	cfg, ctx := load(t, ModeAgent)
	o, cyc := SiteOrder(cfg)
	if cyc != nil || strings.Join(o, ",") != "db,app" {
		t.Fatalf("site order %v %v", o, cyc)
	}
	o, cyc = ContainerOrder(&site(cfg, "app").Config, ctx.Services)
	if cyc != nil || strings.Join(o, ",") != "hello-api,hello-web" {
		t.Fatalf("container order %v %v", o, cyc)
	}
	_, cyc = TopoOrder([]string{"a", "b"}, func(n string) []string {
		if n == "a" {
			return []string{"b"}
		}
		return []string{"a"}
	}, nil)
	if len(cyc) != 2 {
		t.Fatal("cycle")
	}
}

func TestContexts(t *testing.T) {
	cfg, ctx := load(t, ModeAgent)
	sm := SiteExprMap(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, nil)
	if sm["db"].Host == nil || *sm["db"].Host != "host.docker.internal" || *sm["db"].Port["redis"]["tcp"] != 16379 {
		t.Fatalf("%+v", sm["db"])
	}
	app := site(cfg, "app")
	ec := ContainerExprContext(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, nil, "app", &app.Config.Containers[1], nil)
	if ec.File["banner"].Path == nil || *ec.File["banner"].Path != "/etc/hello/banner.txt" || ec.Container.Slug != "hello-web" {
		t.Fatal("container ctx")
	}
	if h := EdgeExprContext(cfg, ctx.DeploymentID, "app", nil).Self.Host; h == nil || *h != "127.0.0.1" {
		t.Fatal("edge ctx")
	}
	cc := ContextFromCatalog(ModeAgent, "d", &model.RevisionCatalog{NotFoundPages: map[string]model.CatalogNotFoundPage{"b": {}, "a": {}}})
	if strings.Join(cc.NotFoundPageIDs, ",") != "a,b" {
		t.Fatal(cc.NotFoundPageIDs)
	}
}

func TestRules(t *testing.T) {
	type tc struct {
		name, code string
		mut        func(c *model.Config)
	}
	s := func(v string) *string { return &v }
	f := func(v float64) *float64 { return &v }
	cases := []tc{
		{"dup var", "V_DUPLICATE", func(c *model.Config) { c.Variables = append(c.Variables, c.Variables[0]) }},
		{"unknown service", "V_REF", func(c *model.Config) { site(c, "db").Config.Containers[0].ServiceSlug = "SvcNope0000000000001" }},
		{"site cycle", "V_CYCLE", func(c *model.Config) { site(c, "db").Config.Depends = []model.SiteRef{{SiteSlug: "app"}} }},
		{"port conflict", "V_PORT_CONFLICT", func(c *model.Config) {
			site(c, "db").Config.Containers[0].Ports[0].HostPort = model.NumOrExpr{Num: f(7081)}
		}},
		{"port edge", "V_PORT_EDGE", func(c *model.Config) {
			site(c, "db").Config.Containers[0].Ports[0].HostPort = model.NumOrExpr{Num: f(8082)}
		}},
		{"env one of", "V_ENV_ONE_OF", func(c *model.Config) { site(c, "app").Config.Containers[0].Env[1].Value = s("x") }},
		{"mount", "V_MOUNT", func(c *model.Config) { site(c, "db").Config.Containers[0].Volumes[0].MountPath = s("/") }},
		{"reserved route", "V_RESERVED", func(c *model.Config) { site(c, "app").Config.Edge.Routes[1].Match.Path = "/_opm/x" }},
		{"bad route prefix", "V_EDGE_ROUTE", func(c *model.Config) { site(c, "app").Config.Edge.Routes[1].Match.Path = "old" }},
		{"reserved slug", "V_RESERVED", func(c *model.Config) { c.Sites[0].SiteSlug = "backups" }},
		{"job restart", "V_ENUM", func(c *model.Config) { site(c, "db").Config.Containers[0].Restart = "sometimes" }},
	}
	for _, x := range cases {
		cfg, ctx := load(t, ModeAgent)
		x.mut(cfg)
		is := ValidateConfig(cfg, ctx)
		if !hasIssue(is, x.code, "error") {
			t.Errorf("%s: want %s, got %+v", x.name, x.code, is.List)
		}
	}
	// expression unknown path: warning in draft, error in agent
	for _, m := range []Mode{ModeDraft, ModeAgent} {
		cfg, ctx := load(t, m)
		site(cfg, "app").Config.Containers[0].Env[2].Value = s("{{ var.nope }}")
		is := ValidateConfig(cfg, ctx)
		want := "warning"
		if m == ModeAgent {
			want = "error"
		}
		if !hasIssue(is, "E_UNKNOWN_PATH", want) {
			t.Errorf("mode %s: %+v", m, is.List)
		}
	}
	// required variable
	cfg, ctx := load(t, ModeAgent)
	cfg.Variables[0].Value = model.VariableValue{}
	if is := ValidateConfig(cfg, ctx); !hasIssue(is, "V_VAR_REQUIRED", "error") {
		t.Errorf("var required: %+v", is.List)
	}
	// enum value type
	cfg, ctx = load(t, ModeAgent)
	cfg.Variables[0].Value = model.VariableValue{V: "pink"}
	if is := ValidateConfig(cfg, ctx); !hasIssue(is, "V_VALUE_TYPE", "error") {
		t.Errorf("value type: %+v", is.List)
	}
	// number rule
	cfg, ctx = load(t, ModeAgent)
	cfg.Variables[2].Value = model.VariableValue{V: 80.0}
	if is := ValidateConfig(cfg, ctx); !hasIssue(is, "V_VALUE_RULE", "error") {
		t.Errorf("value rule: %+v", is.List)
	}
	// secret required + secretsSet
	cfg, ctx = load(t, ModeAgent)
	ctx.SecretsSet = []string{}
	if is := ValidateConfig(cfg, ctx); !hasIssue(is, "V_SECRET_REQUIRED", "error") {
		t.Errorf("secret required: %+v", is.List)
	}
	// template secret value
	cfg, ctx = load(t, ModeTemplate)
	if is := ValidateConfig(cfg, ctx); !hasIssue(is, "V_TEMPLATE_SECRET_VALUE", "error") {
		t.Errorf("template secret: %+v", is.List)
	}
	// no sites
	cfg, ctx = load(t, ModeDraft)
	cfg.Sites = nil
	if is := ValidateConfig(cfg, ctx); !hasIssue(is, "V_NO_SITES", "warning") || !is.Ok() {
		t.Errorf("no sites: %+v", is.List)
	}
}

func TestEdgeRules(t *testing.T) {
	is := NewIssues(ModeAgent)
	e := DefaultEdge()
	e.Hosts = append(e.Hosts, model.EdgeHost{Hostname: "*.x.com", TLS: model.EdgeTls{Mode: "acme", Email: "a@b.co", Challenge: "http-01"}})
	e.Routes = []model.EdgeRoute{
		{RouteID: "all", Order: 1, Hosts: []string{}, Match: model.EdgeMatch{Kind: "prefix", Path: "/"}, Action: model.EdgeRouteAction{Type: "redirect", To: "/x", Status: 308}},
		{RouteID: "api", Order: 2, Hosts: []string{}, Match: model.EdgeMatch{Kind: "prefix", Path: "/"}, Action: model.EdgeRouteAction{Type: "redirect", To: "/y", Status: 303}},
	}
	ValidateEdge(&e, EdgeOptions{AgentPort: 80}, is)
	for _, c := range []string{"V_PORT_CONFLICT", "V_EDGE_TLS", "V_DUPLICATE", "V_EDGE_ACME_INTERNET"} {
		if !hasIssue(is, c, "") {
			t.Errorf("want %s: %+v", c, is.List)
		}
	}
	is = NewIssues(ModeAgent)
	CheckRe2(`(?=x)`, "p", is)
	CheckRe2(`(`, "p", is)
	if len(is.List) != 2 {
		t.Fatal(is.List)
	}
	if !IsIPLiteral("10.0.0.1") || !IsIPLiteral("::1") || IsIPLiteral("a.com") {
		t.Fatal("ip")
	}
}

func TestLocalEdit(t *testing.T) {
	cfg, _ := load(t, ModeAgent)
	n := clone(t, cfg)
	n.Variables[1].Value = model.VariableValue{V: "Hi"}
	v := "new"
	n.Secrets[0].Value = &v
	if l := CheckLocalEdit(cfg, n); len(l) != 0 {
		t.Fatal(l)
	}
	n = clone(t, cfg)
	n.Variables[1].Label = "x"
	n.Sites[0].Label = "y"
	site(n, "app").Config.Containers[0].Version = "v2"
	n.Variables = append(n.Variables, model.Variable{VariableSlug: "added"})
	n.Secrets = n.Secrets[:0]
	n.Files = n.Files[:0]
	// §21 #113: definitions, labels, the image, adds and removes are all local edits
	if l := CheckLocalEdit(cfg, n); len(l) != 0 {
		t.Fatal(l)
	}
	n = clone(t, cfg)
	site(n, "app").Config.Containers = site(n, "app").Config.Containers[:1]
	if l := CheckLocalEdit(cfg, n); len(l) != 0 {
		t.Fatal(l)
	}
	// the list of sites stays the deployment's
	n.Sites = append(n.Sites, model.Site{SiteSlug: "new"})
	if l := CheckLocalEdit(cfg, n); len(l) != 1 || l[0].Code != "V_LOCAL_SITES" {
		t.Fatal(l)
	}
}

// ───────────────────────── phase U (ports of the shared/src/validate.test.ts phase-U tests)

func codes(is *Issues) []string {
	out := []string{}
	for _, x := range is.List {
		out = append(out, x.Code)
	}
	return out
}

func TestImageEnvOptions(t *testing.T) {
	s := func(v string) *model.ImageEnvValue { return &model.ImageEnvValue{Str: &v} }
	env := func(e model.ImageEnv) string {
		e.Name = "NODE_ENV"
		is := NewIssues(ModePublish)
		ValidateImageEnv([]model.ImageEnv{e}, is, "env")
		return strings.Join(codes(is), ",")
	}
	for _, c := range []struct {
		name string
		e    model.ImageEnv
		want string
	}{
		{"default in options", model.ImageEnv{Value: s("worker"), Options: model.Some([]string{"integration", "worker"})}, ""},
		{"default not in options", model.ImageEnv{Value: s("prod"), Options: model.Some([]string{"integration", "worker"})}, "V_OPTION"},
		{"expression default with options", model.ImageEnv{Value: s("{{ var.x }}"), Options: model.Some([]string{"a"})}, "V_OPTION"},
		{"duplicate options", model.ImageEnv{Options: model.Some([]string{"a", "a"})}, "V_DUPLICATE"},
		{"empty options", model.ImageEnv{Options: model.Some([]string{})}, "V_LENGTH"},
		{"legacy list", model.ImageEnv{Value: &model.ImageEnvValue{List: []string{"dc", "worker"}}}, ""},
		{"secret with options", model.ImageEnv{Secret: true, Options: model.Some([]string{"a"})}, "V_IMAGE_ENV"},
	} {
		if got := env(c.e); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	n := NormalizeImageEnv(model.ImageEnv{Name: "X", Value: &model.ImageEnvValue{List: []string{"dc", "worker"}}})
	b, _ := json.Marshal(n)
	if string(b) != `{"description":"","name":"X","options":["dc","worker"],"required":false,"secret":false,"value":"dc"}` {
		t.Errorf("normalize legacy: %s", b)
	}
	b, _ = json.Marshal(NormalizeImageEnv(model.ImageEnv{Name: "Y"}))
	if !strings.Contains(string(b), `"options":null`) || strings.Contains(string(b), `"value"`) {
		t.Errorf("normalize plain: %s", b)
	}
}

func TestImageVolumeMountPathExpressions(t *testing.T) {
	vol := func(mp string) string {
		is := NewIssues(ModePublish)
		ValidateImageVolumes([]model.ImageVolume{{Name: "config", MountPath: mp}}, is, "volumes")
		return strings.Join(codes(is), ",")
	}
	for mp, want := range map[string]string{
		"/app/helper/config/env/{{ container.env.NODE_ENV }}.ts": "",
		"helper/config/env/{{ container.env.NODE_ENV }}.ts":      "V_PATH",
		"/x/{{ secret.a }}": "E_NOT_ALLOWED",
		"helper/config":     "V_MOUNT",
	} {
		if got := vol(mp); got != want {
			t.Errorf("%s: got %q want %q", mp, got, want)
		}
	}
	if !RE.Icon.MatchString("lucide:globe") || !RE.Icon.MatchString("upload:AbCdEfGhIjKlMnOpQrSt") || !RE.Icon.MatchString("server") || RE.Icon.MatchString("https://x/y.png") {
		t.Error("RE.Icon")
	}
	if !RE.Picture.MatchString("https://x/y.png") || RE.Picture.MatchString("server") {
		t.Error("RE.Picture")
	}
	is := NewIssues(ModePublish)
	CheckIcon(nil, "icon", is)
	if len(is.List) != 0 {
		t.Error("null icon must be valid")
	}
}

func TestContainerChoiceEnvMountExprCycle(t *testing.T) {
	const A = "hello-api"
	key := A + ":v1"
	setup := func(mode Mode) (*model.Config, Context) {
		cfg, ctx := load(t, mode)
		im := ctx.Images[key]
		w := "worker"
		im.Env = append(append([]model.ImageEnv{}, im.Env...), model.ImageEnv{Name: "NODE_ENV", Value: &model.ImageEnvValue{Str: &w}, Options: model.Some([]string{"integration", "dc", "worker"})})
		im.Volumes = append(append([]model.ImageVolume{}, im.Volumes...), model.ImageVolume{Name: "config", MountPath: "/app/helper/config/env/{{ container.env.NODE_ENV }}.ts"})
		ctx.Images[key] = im
		return cfg, ctx
	}
	api := func(c *model.Config) *model.Container {
		for i, x := range site(c, "app").Config.Containers {
			if x.ServiceSlug == A {
				return &site(c, "app").Config.Containers[i]
			}
		}
		t.Fatal("no api container")
		return nil
	}
	errs := func(c *model.Config, ctx Context) []string {
		out := []string{}
		for _, x := range ValidateConfig(c, ctx).Errors() {
			out = append(out, x.Code+"@"+x.Path)
		}
		return out
	}
	some := func(l []string, pred func(string) bool) bool {
		for _, x := range l {
			if pred(x) {
				return true
			}
		}
		return false
	}
	s := func(v string) *string { return &v }

	cfg, ctx := setup(ModePublish)
	if e := errs(cfg, ctx); len(e) != 0 {
		t.Fatalf("base: %v", e)
	}
	c1 := clone(t, cfg)
	a := api(c1)
	a.Env = append(a.Env, model.ContainerEnv{Name: "NODE_ENV", Value: s("prod")})
	if e := errs(c1, ctx); !some(e, func(x string) bool { return strings.HasPrefix(x, "V_OPTION@") && strings.Contains(x, "env[NODE_ENV]") }) {
		t.Errorf("V_OPTION: %v", e)
	}
	a.Env[len(a.Env)-1].Value = s(`{{ var.nope | default("dc") }}`)
	if e := errs(c1, ctx); !some(e, func(x string) bool { return strings.HasPrefix(x, "E_UNKNOWN_PATH@") }) {
		t.Errorf("E_UNKNOWN_PATH: %v", e)
	}
	a.Env[len(a.Env)-1].Value = s("dc")
	if e := errs(c1, ctx); len(e) != 0 {
		t.Errorf("dc: %v", e)
	}
	// resolved mount target of the image volume (an explicit row: round D has no implicit data volume)
	a.Volumes = append(a.Volumes, model.ContainerMount{ImageVolume: s("config"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "cfg"}})
	*site(c1, "app").Config.Volumes = append(*site(c1, "app").Config.Volumes, NewSiteVolume("cfg", "", "", true))
	r := ResolveContainerConfig(c1, a, ctx.Images[key].Env, ctx.Images[key].Volumes, ContainerExprContext(c1, ctx.Services, ctx.Images, ctx.DeploymentID, nil, "app", a, nil))
	if m := r.Resolution.Mounts["image:config"]; m.Err != nil || m.Result.Value != "/app/helper/config/env/dc.ts" {
		t.Errorf("mount: %+v %+v", m.Result, m.Err)
	}
	// cycle: NODE_ENV → CYC → NODE_ENV
	a.Env[len(a.Env)-1].Value = s("{{ container.env.CYC }}")
	a.Env = append(a.Env, model.ContainerEnv{Name: "CYC", Value: s("x{{ container.env.NODE_ENV }}")})
	n := 0
	for _, x := range errs(c1, ctx) {
		if strings.HasPrefix(x, "E_CYCLE@") {
			n++
		}
	}
	if n < 2 {
		t.Errorf("E_CYCLE count %d: %v", n, errs(c1, ctx))
	}
	// a mount path resolving to "/"
	c2 := clone(t, cfg)
	a2 := api(c2)
	a2.Env = append(a2.Env, model.ContainerEnv{Name: "EMPTY_DIR", Value: s("")})
	*site(c2, "app").Config.Volumes = append(*site(c2, "app").Config.Volumes, NewSiteVolume("cfg", "", "", true))
	a2.Volumes = append(a2.Volumes, model.ContainerMount{ImageVolume: s("config"), MountPath: s("/{{ container.env.EMPTY_DIR }}"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "cfg"}})
	if e := errs(c2, ctx); !some(e, func(x string) bool { return strings.HasPrefix(x, "V_PATH@") }) {
		t.Errorf("V_PATH: %v", e)
	}
	_, dctx := setup(ModeDraft)
	if !hasIssue(ValidateConfig(c2, dctx), "V_PATH", "warning") {
		t.Error("draft: V_PATH must be a warning")
	}
}
