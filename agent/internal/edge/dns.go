package edge

import (
	"opm/agent/internal/edgecfg"

	// ACME dns-01 providers compiled into the agent.
	_ "github.com/caddy-dns/cloudflare"
	_ "github.com/caddy-dns/digitalocean"
	_ "github.com/caddy-dns/duckdns"
	_ "github.com/caddy-dns/godaddy"
	_ "github.com/caddy-dns/googleclouddns"
	_ "github.com/caddy-dns/namecheap"
	_ "github.com/caddy-dns/route53"
)

// DNSProviders are the supported EdgeTls.dnsProvider names (table in edgecfg, caddy-free).
var DNSProviders = edgecfg.DNSProviders

// DNSProviderNames returns the supported names (sorted).
func DNSProviderNames() []string { return edgecfg.DNSProviderNames() }

// DNSProviderConfig builds the Caddy `challenges.dns.provider` object (edgecfg.DNSProviderConfig).
func DNSProviderConfig(name, secret string) (map[string]any, error) {
	return edgecfg.DNSProviderConfig(name, secret)
}
