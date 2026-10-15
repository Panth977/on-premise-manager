package validate

import (
	"reflect"
	"testing"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// TestContainersRefs (§21 #88; port of the TS test): containers.<slug>.host / .port.<portName> of the site,
// V_CONTAINER_REF_DISABLED for a disabled target, unknown container / port → E_UNKNOWN_PATH.
func TestContainersRefs(t *testing.T) {
	cfg, ctx := load(t, ModePublish)
	web := cont(cfg, "app", "hello-web")
	web.Env = append(web.Env, model.ContainerEnv{Name: "API_URL", Value: sp("http://{{ containers.hello-api.host }}:{{ containers.hello-api.port.http }}")})
	if e := errCodes(ValidateConfig(clone(t, cfg), ctx)); len(e) != 0 {
		t.Fatalf("errors %v", e)
	}
	ec := ContainerExprContext(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, nil, "app", web, nil)
	r, err := expr.Resolve(*web.Env[len(web.Env)-1].Value, ec, expr.FieldEnv)
	if err != nil || r.Value != "http://hello-api:8080" {
		t.Fatalf("resolve %+v %v", r, err)
	}
	cont(cfg, "app", "hello-api").Enabled = false
	var w []model.ValidationIssue
	for _, x := range ValidateConfig(clone(t, cfg), ctx).List {
		if x.Code == "V_CONTAINER_REF_DISABLED" {
			w = append(w, x)
		}
	}
	if len(w) != 1 || w[0].Severity != "warning" || w[0].Path != "sites[app].config.containers[hello-web].env[API_URL].value" {
		t.Fatalf("warning %+v", w)
	}
	u, _ := load(t, ModePublish)
	web2 := cont(u, "app", "hello-web")
	web2.Env = append(web2.Env, model.ContainerEnv{Name: "X", Value: sp("{{ containers.redis.host }}")}, model.ContainerEnv{Name: "Y", Value: sp("{{ containers.hello-api.port.nope }}")})
	if e := errCodes(ValidateConfig(clone(t, u), ctx)); !reflect.DeepEqual(e, []string{"E_UNKNOWN_PATH", "E_UNKNOWN_PATH"}) {
		t.Fatalf("unknown: %v", e)
	}
}
