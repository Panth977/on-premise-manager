package edge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"opm/agent/internal/model"
)

func acmeHost(name, ch string, provider *string) model.EdgeHost {
	slug := "dns-token"
	t := model.EdgeTls{Mode: "acme", Email: "it@acme.example", Challenge: ch}
	if provider != nil {
		t.DNSProvider, t.DNSTokenSecretSlug = provider, &slug
	}
	return model.EdgeHost{Hostname: name, TLS: t, RedirectHTTPToHTTPS: true}
}

func validateJSON(t *testing.T, in *Input) map[string]any {
	t.Helper()
	cfg := Assemble(t.TempDir(), []*Partial{in.Build("")})
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := caddy.Validate(&caddy.Config{}); err != nil {
		t.Fatal(err)
	}
	var c caddy.Config
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if err := caddy.Validate(&c); err != nil {
		t.Fatalf("caddy.Validate: %v\n%s", err, raw)
	}
	return cfg
}

func TestACMEConfigsValidate(t *testing.T) {
	cf, r53 := "cloudflare", "route53"
	https := 443
	cfToken, _ := DNSProviderConfig("cloudflare", "Ab3dEf6hIj9lMn2pQr5tUv8xYz1AbCdEfGhIjKlM")
	r53Cfg, err := DNSProviderConfig("route53", `{"access_key_id":"AKIA1","secret_access_key":"s","region":"eu-west-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	in := &Input{Instance: "acme", SiteSlug: "s", HTTPPort: 80, HTTPSPort: &https,
		Hosts: []model.EdgeHost{
			acmeHost("plant.acme.example", "http-01", nil),
			acmeHost("alpn.acme.example", "tls-alpn-01", nil),
			acmeHost("*.acme.example", "dns-01", &cf),
			acmeHost("*.aws.example", "dns-01", &r53),
			{Hostname: "*", TLS: model.EdgeTls{Mode: "internal"}},
		},
		DNSConfigs: map[string]map[string]any{"*.acme.example": cfToken, "*.aws.example": r53Cfg}}
	cfg := validateJSON(t, in)
	raw, _ := json.Marshal(cfg)
	s := string(raw)
	for _, want := range []string{`"module":"acme"`, `"http":{"disabled":false}`, `"tls-alpn":{"disabled":false}`, `"name":"cloudflare"`, `"api_token":"Ab3dEf6hIj9lMn2pQr5tUv8xYz1AbCdEfGhIjKlM"`, `"name":"route53"`, `"on_demand":true`, `"module":"opm_any"`} {
		if !strings.Contains(s, want) {
			t.Errorf("config misses %s", want)
		}
	}
	red, _ := json.Marshal(Redact(cfg))
	if strings.Contains(string(red), "Ab3dEf6hIj9lMn2pQr5tUv8xYz1AbCdEfGhIjKlM") || strings.Contains(string(red), `"secret_access_key":"s"`) {
		t.Fatalf("secrets not redacted: %s", red)
	}
}

func TestDNSProviderConfig(t *testing.T) {
	if _, err := DNSProviderConfig("bind9", "x"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unknown provider: %v", err)
	}
	if c, err := DNSProviderConfig("digitalocean", "tok"); err != nil || c["auth_token"] != "tok" {
		t.Fatalf("digitalocean %v %v", c, err)
	}
	if _, err := DNSProviderConfig("route53", "plain"); err == nil {
		t.Fatal("route53 needs a JSON secret")
	}
	if _, err := DNSProviderConfig("namecheap", `{"api_key":"k"}`); err == nil || !strings.Contains(err.Error(), "user") {
		t.Fatalf("namecheap missing user: %v", err)
	}
	if _, err := DNSProviderConfig("cloudflare", ""); err == nil {
		t.Fatal("empty token")
	}
	for _, n := range DNSProviderNames() {
		if _, err := caddy.GetModule("dns.providers." + n); err != nil {
			t.Fatalf("module %s not compiled in: %v", n, err)
		}
	}
}

func TestValidateInputRejectsBadToken(t *testing.T) {
	cf := "cloudflare"
	https := 443
	pc, _ := DNSProviderConfig("cloudflare", "\"quoted-token\"")
	in := &Input{Instance: "v", SiteSlug: "s", HTTPPort: 80, HTTPSPort: &https, Hosts: []model.EdgeHost{acmeHost("*.x.example", "dns-01", &cf)},
		DNSConfigs: map[string]map[string]any{"*.x.example": pc}}
	if err := ValidateInput(in, t.TempDir()); err == nil {
		t.Fatal("bad cloudflare token must fail validation")
	}
}
