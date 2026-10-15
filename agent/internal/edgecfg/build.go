package edgecfg

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/model"
)

// PEMPair is a files-mode certificate.
type PEMPair struct {
	Cert string
	Key  string
}

// Input is the head SiteConfig edge of one site after expressions resolved.
type Input struct {
	Instance  string
	SiteSlug  string
	HTTPPort  int
	HTTPSPort *int
	Hosts     []model.EdgeHost
	Routes    []model.EdgeRoute
	// MaxBodyBytes is edge.maxBodyBytes (nil = unlimited; routes may override).
	MaxBodyBytes *int64
	NotFound404  string
	NotFound503  string
	// Upstreams: containerSlug → portName → loopback host port (0 = not running)
	Upstreams  map[string]map[string]int
	FilesPEM   map[string]PEMPair        // hostname → PEMs (files mode)
	DNSConfigs map[string]map[string]any // hostname → Caddy DNS provider config (acme dns-01; see DNSProviderConfig)
	SkipHTTP   bool                      // bind failure fallback
	SkipHTTPS  bool
	// RootRedirect: "/" → 302 to this path (the built-in default edge, §21 #86: "/_opm/app")
	RootRedirect string
	// HTTPListen / HTTPSListen override the listen address (the agent passes "fd/<n>": a socket it owns, so a config
	// swap never closes the listening socket; §21 #86). "" = ":<port>".
	HTTPListen  string
	HTTPSListen string
	// std: BuildStandard is building (standard Caddy modules only, §21 #106)
	std *stdState
}

// AnyTLS: some host serves TLS (internal / acme / files). The HTTPS listener is bound only then (§21 #86).
func (in *Input) AnyTLS() bool {
	for _, h := range in.Hosts {
		if h.TLS.Mode != "" && h.TLS.Mode != "none" {
			return true
		}
	}
	return false
}

// Built-in not-found pages (§12.3).
const (
	BuiltIn404 = `<!doctype html><html><head><meta charset="utf-8"><title>Not found</title></head><body style="font-family:sans-serif;text-align:center;padding:10vh">
<h1>404</h1><p>This page does not exist.</p></body></html>`
	BuiltIn503 = `<!doctype html><html><head><meta charset="utf-8"><title>Not available</title></head><body style="font-family:sans-serif;text-align:center;padding:10vh">
<h1>503</h1><p>This service is starting. Try again in a moment.</p></body></html>`
)

// Partial is one site's part of the Caddy config.
type Partial struct {
	Servers      map[string]any
	Automate     []string
	LoadPEM      []map[string]any
	Policies     []map[string]any
	OnDemand     bool
	OnDemandDeny []string
	HTTPPort     int
	HTTPSPort    int
	Hosts        int
	Routes       int
}

func hostClass(h string) int {
	switch {
	case h == "*":
		return 2
	case strings.HasPrefix(h, "*."):
		return 1
	}
	return 0
}

// SortedHosts returns the hosts in priority order: exact, wildcard, "*".
func SortedHosts(hosts []model.EdgeHost) []model.EdgeHost {
	out := append([]model.EdgeHost(nil), hosts...)
	sort.SliceStable(out, func(i, j int) bool { return hostClass(out[i].Hostname) < hostClass(out[j].Hostname) })
	return out
}

// Build makes this site's partial Caddy config.
func (in *Input) Build(serverPrefix string) *Partial {
	if in.NotFound404 == "" {
		in.NotFound404 = BuiltIn404
	}
	if in.NotFound503 == "" {
		if in.NotFound404 != BuiltIn404 {
			in.NotFound503 = in.NotFound404 // a custom page serves both statuses (§12.3)
		} else {
			in.NotFound503 = BuiltIn503
		}
	}
	p := &Partial{Servers: map[string]any{}, HTTPPort: in.HTTPPort, Hosts: len(in.Hosts), Routes: len(in.Routes)}
	hosts := SortedHosts(in.Hosts)
	errRoutes := in.errorRoutes(hosts)
	if !in.SkipHTTP {
		var routes []any
		for _, h := range hosts {
			routes = append(routes, in.hostRoutes(h, false))
		}
		routes = append(routes, map[string]any{"handle": []any{staticPage(404, in.NotFound404)}})
		listen := ":" + strconv.Itoa(in.HTTPPort)
		if in.HTTPListen != "" {
			listen = in.HTTPListen
		}
		p.Servers[serverPrefix+"edge_http"] = map[string]any{"listen": []string{listen},
			"automatic_https": map[string]any{"disable": true}, "protocols": []string{"h1", "h2c"},
			"routes": routes, "errors": errRoutes}
	}
	if in.HTTPSPort != nil && !in.SkipHTTPS && in.AnyTLS() {
		p.HTTPSPort = *in.HTTPSPort
		var routes []any
		var pols []any
		for _, h := range hosts {
			routes = append(routes, in.hostRoutes(h, true))
			if h.TLS.Mode == "none" || h.Hostname == "*" {
				continue
			}
			pol := map[string]any{"match": map[string]any{"sni": []string{h.Hostname}}}
			if h.TLS.Mode == "files" {
				pol["certificate_selection"] = map[string]any{"any_tag": []string{h.Hostname}}
			}
			pols = append(pols, pol)
		}
		pols = append(pols, map[string]any{})
		listenS := ":" + strconv.Itoa(*in.HTTPSPort)
		if in.HTTPSListen != "" {
			listenS = in.HTTPSListen
		}
		p.Servers[serverPrefix+"edge_https"] = map[string]any{"listen": []string{listenS},
			"automatic_https": map[string]any{"disable": true}, "protocols": []string{"h1", "h2"},
			"tls_connection_policies": pols, "routes": routes, "errors": errRoutes}
		// TLS app pieces
		var internal []string
		acme := map[string][]model.EdgeHost{}
		for _, h := range hosts {
			switch h.TLS.Mode {
			case "internal":
				if h.Hostname == "*" {
					p.OnDemand = true
					continue
				}
				internal = append(internal, h.Hostname)
				p.Automate = append(p.Automate, h.Hostname)
			case "acme":
				acme[h.TLS.Email] = append(acme[h.TLS.Email], h)
				p.Automate = append(p.Automate, h.Hostname)
			case "files":
				if pem, ok := in.FilesPEM[h.Hostname]; ok {
					p.LoadPEM = append(p.LoadPEM, map[string]any{"certificate": pem.Cert, "key": pem.Key, "tags": []string{h.Hostname}})
				}
			case "none":
				p.OnDemandDeny = append(p.OnDemandDeny, h.Hostname)
			}
		}
		if len(internal) > 0 {
			p.Policies = append(p.Policies, map[string]any{"subjects": internal, "issuers": []any{map[string]any{"module": "internal", "lifetime": "12h"}}})
		}
		emails := make([]string, 0, len(acme))
		for e := range acme {
			emails = append(emails, e)
		}
		sort.Strings(emails)
		for _, e := range emails {
			// one policy per (email, challenge, dns provider) group
			groups := map[string][]model.EdgeHost{}
			for _, h := range acme[e] {
				k := h.TLS.Challenge
				if h.TLS.Challenge == "dns-01" {
					b, _ := json.Marshal(in.DNSConfigs[h.Hostname])
					k += "|" + string(b)
				}
				groups[k] = append(groups[k], h)
			}
			keys := make([]string, 0, len(groups))
			for k := range groups {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				hs := groups[k]
				ch := hs[0].TLS.Challenge
				challenges := map[string]any{"http": map[string]any{"disabled": ch != "http-01"}, "tls-alpn": map[string]any{"disabled": ch != "tls-alpn-01"}}
				if ch == "dns-01" {
					if pc := in.DNSConfigs[hs[0].Hostname]; pc != nil {
						challenges["dns"] = map[string]any{"provider": pc}
					}
				}
				var subj []string
				for _, h := range hs {
					subj = append(subj, h.Hostname)
				}
				p.Policies = append(p.Policies, map[string]any{"subjects": subj, "issuers": []any{map[string]any{"module": "acme", "email": e, "challenges": challenges}}})
			}
		}
	}
	return p
}

// Assemble merges partials into one Caddy JSON document.
func Assemble(storageRoot string, parts []*Partial) map[string]any {
	servers := map[string]any{}
	var automate []string
	var loadPEM []any
	var policies []any
	onDemand := false
	var deny []string
	httpPort, httpsPort := 0, 0
	for _, p := range parts {
		for k, v := range p.Servers {
			servers[k] = v
		}
		automate = append(automate, p.Automate...)
		for _, x := range p.LoadPEM {
			loadPEM = append(loadPEM, x)
		}
		for _, x := range p.Policies {
			policies = append(policies, x)
		}
		if p.OnDemand {
			onDemand = true
		}
		deny = append(deny, p.OnDemandDeny...)
		if httpPort == 0 {
			httpPort = p.HTTPPort
		}
		if httpsPort == 0 && p.HTTPSPort != 0 {
			httpsPort = p.HTTPSPort
		}
	}
	if httpsPort == 0 {
		httpsPort = 443
	}
	if httpPort == 0 {
		httpPort = 80
	}
	cfg := map[string]any{
		"admin":   map[string]any{"disabled": true},
		"storage": map[string]any{"module": "file_system", "root": storageRoot},
		"logging": map[string]any{"logs": map[string]any{"default": map[string]any{"level": "WARN"}}},
	}
	apps := map[string]any{}
	if len(servers) > 0 {
		apps["http"] = map[string]any{"http_port": httpPort, "https_port": httpsPort, "servers": servers, "grace_period": int64(30 * 1e9)} // in-flight requests finish after a config swap
	}
	tlsApp := map[string]any{}
	certs := map[string]any{}
	if len(automate) > 0 {
		certs["automate"] = automate
	}
	if len(loadPEM) > 0 {
		certs["load_pem"] = loadPEM
	}
	if len(certs) > 0 {
		tlsApp["certificates"] = certs
	}
	if onDemand {
		policies = append(policies, map[string]any{"on_demand": true, "issuers": []any{map[string]any{"module": "internal", "lifetime": "12h"}}})
	}
	if len(policies) > 0 || onDemand {
		auto := map[string]any{"policies": policies}
		if onDemand {
			auto["on_demand"] = map[string]any{"permission": map[string]any{"module": "opm_any", "deny": deny}}
		}
		tlsApp["automation"] = auto
	}
	if len(tlsApp) > 0 {
		apps["tls"] = tlsApp
	}
	// never touch the host trust store
	apps["pki"] = map[string]any{"certificate_authorities": map[string]any{"local": map[string]any{"install_trust": false}}}
	if len(apps) > 0 {
		cfg["apps"] = apps
	}
	return cfg
}

// Redact returns a copy of the config JSON with secrets removed (key PEMs, DNS tokens).
func Redact(cfg map[string]any) any {
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, vv := range x {
				if k == "key" || k == "api_token" || k == "auth_token" || k == "secret_access_key" || k == "api_key" || k == "gcp_application_default" || k == "session_token" {
					out[k] = "[redacted]"
					continue
				}
				out[k] = walk(vv)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, vv := range x {
				out[i] = walk(vv)
			}
			return out
		case []map[string]any:
			out := make([]any, len(x))
			for i, vv := range x {
				out[i] = walk(vv)
			}
			return out
		}
		return v
	}
	return walk(cfg)
}

var _ = fmt.Sprintf
