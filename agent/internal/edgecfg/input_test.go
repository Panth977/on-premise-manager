package edgecfg

import (
	"strings"
	"testing"

	"opm/agent/internal/model"
)

func TestInputFromConfig(t *testing.T) {
	https := 443
	prov, tok := "cloudflare", "dns-token"
	srv := "10.1.2.3"
	cfg := &model.Config{
		Variables: []model.Variable{{VariableSlug: "domain", Type: "string", Value: model.VariableValue{V: "plant.example"}}},
		Secrets:   []model.SecretDef{{SecretSlug: tok, Value: &srv}},
		Sites: []model.Site{{SiteSlug: "app", Config: model.SiteConfig{Containers: []model.Container{{ContainerSlug: "web", Enabled: true}, {ContainerSlug: "off"}},
			Edge: model.Edge{HTTPPort: 80, HTTPSPort: &https,
				Hosts:  []model.EdgeHost{{Hostname: "{{ var.domain }}", TLS: model.EdgeTls{Mode: "acme", Email: "a@b.c", Challenge: "dns-01", DNSProvider: &prov, DNSTokenSecretSlug: &tok}}},
				Routes: []model.EdgeRoute{{RouteID: "r", Match: model.EdgeMatch{Kind: "prefix", Path: "/"}, Action: model.EdgeRouteAction{Type: "redirect", To: "https://{{ var.domain }}/x"}, Order: 1}}}}}},
	}
	in, notes, err := InputFromConfig(InputParams{DeploymentID: "dep", SiteSlug: "app", Config: cfg, Catalog: &model.RevisionCatalog{},
		Upstreams: func(s string) map[string]int { return map[string]int{"http": 8080} },
		Secret:    func(s string) (string, bool) { return "{env.OPM_SECRET_" + strings.ToUpper(s) + "}", true },
		DNSConfig: func(p, v string) (map[string]any, error) { return map[string]any{"name": p, "api_token": v}, nil }})
	if err != nil || len(notes) != 0 {
		t.Fatal(err, notes)
	}
	if in.Hosts[0].Hostname != "plant.example" || in.Routes[0].Action.To != "https://plant.example/x" {
		t.Fatalf("expressions: %+v", in)
	}
	if in.Upstreams["web"]["http"] != 8080 || in.Upstreams["off"] != nil {
		t.Fatalf("upstreams %v", in.Upstreams)
	}
	if in.DNSConfigs["plant.example"]["api_token"] != "{env.OPM_SECRET_DNS-TOKEN}" {
		t.Fatalf("dns %v", in.DNSConfigs)
	}
	if x, _, _ := InputFromConfig(InputParams{SiteSlug: "nope", Config: cfg, Catalog: &model.RevisionCatalog{}}); x != nil {
		t.Fatal("unknown site → nil")
	}
}
