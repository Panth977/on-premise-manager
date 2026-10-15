package edgecfg

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// dnsProvider describes how EdgeHost.tls.dnsTokenSecretSlug maps to a Caddy DNS provider config.
type dnsProvider struct {
	tokenField string   // provider field that takes a plain secret value ("" = JSON object secret required)
	required   []string // fields that must be present
}

// DNSProviders are the supported EdgeTls.dnsProvider names.
var DNSProviders = map[string]dnsProvider{
	"cloudflare":     {tokenField: "api_token", required: []string{"api_token"}},
	"digitalocean":   {tokenField: "auth_token", required: []string{"auth_token"}},
	"duckdns":        {tokenField: "api_token", required: []string{"api_token"}},
	"godaddy":        {tokenField: "api_token", required: []string{"api_token"}}, // "<key>:<secret>"
	"route53":        {required: []string{"access_key_id", "secret_access_key"}},
	"googleclouddns": {required: []string{"gcp_project"}},
	"namecheap":      {required: []string{"api_key", "user"}},
}

// DNSProviderNames returns the supported names (sorted).
func DNSProviderNames() []string {
	out := make([]string, 0, len(DNSProviders))
	for k := range DNSProviders {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DNSProviderConfig builds the Caddy `challenges.dns.provider` object from the provider name and the secret value.
// The secret is either the plain token (single-token providers) or a JSON object with the provider's fields
// (e.g. route53 {"access_key_id","secret_access_key","region"}). Unknown fields are refused.
func DNSProviderConfig(name, secret string) (map[string]any, error) {
	p, ok := DNSProviders[name]
	if !ok {
		return nil, fmt.Errorf("dns provider %q is not supported (supported: %s)", name, strings.Join(DNSProviderNames(), ", "))
	}
	cfg := map[string]any{"name": name}
	s := strings.TrimSpace(secret)
	if strings.HasPrefix(s, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(s), &obj); err != nil {
			return nil, fmt.Errorf("dns provider %s: the secret is not a valid JSON object: %v", name, err)
		}
		for k, v := range obj {
			if k == "name" {
				continue
			}
			cfg[k] = v
		}
	} else if p.tokenField != "" && s != "" {
		cfg[p.tokenField] = s
	} else if p.tokenField == "" {
		return nil, fmt.Errorf("dns provider %s needs a JSON object secret with %s", name, strings.Join(p.required, ", "))
	}
	for _, f := range p.required {
		if v, ok := cfg[f].(string); !ok || v == "" {
			return nil, fmt.Errorf("dns provider %s: the secret has no %s", name, f)
		}
	}
	return cfg, nil
}
