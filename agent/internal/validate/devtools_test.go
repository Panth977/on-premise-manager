package validate

import (
	"encoding/json"
	"os"
	"testing"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// TestDevToolsValidateVectors: ValidateSiteDevTools produces exactly the issues of
// shared/testdata/devtools-validate-vectors.json (path, code, severity, in order). Cases whose config does not decode
// into the Go model (TS V_TYPE cases, e.g. devTools not a list) are checked to fail decoding instead.
func TestDevToolsValidateVectors(t *testing.T) {
	b, err := os.ReadFile("../../../shared/testdata/devtools-validate-vectors.json")
	if err != nil {
		t.Skip("vectors not found: ", err)
	}
	var v struct {
		Cases []struct {
			Name     string          `json:"name"`
			SiteSlug string          `json:"siteSlug"`
			Config   json.RawMessage `json:"config"`
			Catalog  struct {
				Images map[string]struct {
					Ports []model.ImagePort `json:"ports"`
					Env   []model.ImageEnv  `json:"env"`
				} `json:"images"`
			} `json:"catalog"`
			Expected []struct {
				Path     string `json:"path"`
				Code     string `json:"code"`
				Severity string `json:"severity"`
			} `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		var cfg model.Config
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			typeOnly := len(c.Expected) > 0
			for _, e := range c.Expected {
				if e.Code != "V_TYPE" {
					typeOnly = false
				}
			}
			if !typeOnly {
				t.Errorf("%s: config does not decode: %v", c.Name, err)
			}
			continue
		}
		images := map[string]CatalogImage{}
		for k, im := range c.Catalog.Images {
			images[k] = CatalogImage{Ports: im.Ports, Env: im.Env}
		}
		var site *model.Site
		for i := range cfg.Sites {
			if cfg.Sites[i].SiteSlug == c.SiteSlug {
				site = &cfg.Sites[i]
			}
		}
		if site == nil {
			t.Fatalf("%s: no site %s", c.Name, c.SiteSlug)
		}
		is := NewIssues(ModePublish)
		ValidateSiteDevTools(site, &cfg, images, is)
		ok := len(is.List) == len(c.Expected)
		for i := 0; ok && i < len(c.Expected); i++ {
			g, w := is.List[i], c.Expected[i]
			ok = g.Path == w.Path && g.Code == w.Code && g.Severity == w.Severity
		}
		if !ok {
			t.Errorf("%s:\n got  %+v\n want %+v", c.Name, is.List, c.Expected)
		}
	}
}

// TestSiteDevtoolExprContext (§21 #99, same checks as the TS test): containers.<c>.env.<NAME> = the FINAL env value of
// the container (image default ← container env, resolved), secrets marked; only field devtool resolves it.
func TestSiteDevtoolExprContext(t *testing.T) {
	cfg, ctx := load(t, ModeAgent)
	cfg = clone(t, cfg)
	api := &site(cfg, "app").Config.Containers[0]
	if api.ContainerSlug != "hello-api" {
		t.Fatalf("fixture: %s", api.ContainerSlug)
	}
	tc, loop := "Bearer {{ container.env.API_TOKEN }}", "{{ container.env.LOOP }}"
	api.Env = append(api.Env, model.ContainerEnv{Name: "TOKEN_COPY", Value: &tc}, model.ContainerEnv{Name: "LOOP", Value: &loop})
	tok := "tok-123"
	for i := range cfg.Secrets {
		if cfg.Secrets[i].SecretSlug == "api-token" {
			cfg.Secrets[i].Value = &tok
		}
	}
	ectx := SiteDevtoolExprContext(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, nil, "app", nil)
	env := ectx.Containers["hello-api"].Env
	if e := env["API_TOKEN"]; !e.Secret || e.Value == nil || *e.Value != "tok-123" {
		t.Fatalf("API_TOKEN %+v", e)
	}
	if e := env["TOKEN_COPY"]; !e.Secret || e.Value == nil || *e.Value != "Bearer tok-123" {
		t.Fatalf("TOKEN_COPY %+v", e)
	}
	if e := env["LOOP"]; e.Error == nil || e.Error.Code != expr.ECycle {
		t.Fatalf("LOOP %+v", e)
	}
	if _, ok := env["DATA_DIR"]; !ok {
		t.Fatal("image env default missing")
	}
	r, err := expr.Resolve("{{ containers.hello-api.env.API_TOKEN }}", ectx, DevToolExprField)
	if err != nil || r.Value != "tok-123" || r.Masked != "••••" || !r.Secret {
		t.Fatalf("resolve %+v %v", r, err)
	}
	if _, err := expr.Resolve("{{ containers.hello-api.env.API_TOKEN }}", ectx, expr.FieldFile); err == nil {
		t.Fatal("field file must refuse containers.<c>.env")
	}
	if SiteFileExprContext(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, nil, "app", nil).Containers["hello-api"].Env != nil {
		t.Fatal("the file context has no env")
	}
	cloud := SiteDevtoolExprContext(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, []string{"api-token"}, "app", nil)
	if e := cloud.Containers["hello-api"].Env["API_TOKEN"]; e.Value == nil || *e.Value != "•••" || !e.Secret {
		t.Fatalf("masked %+v", e)
	}
	if !ContainerEnvIsSecret(api, "TOKEN_COPY", nil) || ContainerEnvIsSecret(api, "GREETING", nil) || ContainerEnvIsSecret(api, "LOOP", nil) {
		t.Fatal("ContainerEnvIsSecret")
	}
	if len(CheckDevToolExpr("{{ containers.pg.env.POSTGRES_USER }}")) != 0 || CheckDevToolExpr("{{ container.env.X }}")[0].Code != expr.ENotAllowed {
		t.Fatal("CheckDevToolExpr")
	}
}
