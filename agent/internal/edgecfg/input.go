package edgecfg

import (
	"fmt"
	"strings"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// Note is a fact about a built edge that the caller should surface (e.g. "not equivalent on the official caddy image").
type Note struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResolveEdge resolves the edge expressions of a site (hostnames, response / request header values, redirect.to,
// rewrite replacements). hosts = the site addresses (validate.ResolveSiteHosts); nil = without detected addresses.
func ResolveEdge(cfg *model.Config, deploymentID string, site *model.Site, hosts map[string]validate.SiteHost) (*model.Edge, error) {
	ctx := validate.EdgeExprContext(cfg, deploymentID, site.SiteSlug, hosts)
	e := site.Config.Edge
	out := model.Edge{HTTPPort: e.HTTPPort, HTTPSPort: e.HTTPSPort, MaxBodyBytes: e.MaxBodyBytes, Hosts: []model.EdgeHost{}, Routes: []model.EdgeRoute{}}
	res := func(v string) (string, error) {
		if !expr.IsExpr(v) {
			return v, nil
		}
		r, err := expr.Resolve(v, ctx, expr.FieldEdge)
		if err != nil {
			return "", err
		}
		return r.Value, nil
	}
	resMap := func(m model.HeaderMap, what string) (model.HeaderMap, error) {
		o := model.HeaderMap{}
		for _, kv := range m {
			rv, err := res(kv.Value)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", what, kv.Name, err)
			}
			o = append(o, model.HeaderKV{Name: kv.Name, Value: rv})
		}
		return o, nil
	}
	resRH := func(h model.EdgeResponseHeaders) (model.EdgeResponseHeaders, error) {
		c := h.Clone()
		var err error
		if c.Set, err = resMap(h.Set, "response header"); err != nil {
			return c, err
		}
		c.Add, err = resMap(h.Add, "response header")
		return c, err
	}
	for _, h := range e.Hosts {
		n, err := res(h.Hostname)
		if err != nil {
			return nil, fmt.Errorf("host %s: %w", h.Hostname, err)
		}
		h.Hostname = n
		if h.ResponseHeaders, err = resRH(h.ResponseHeaders); err != nil {
			return nil, fmt.Errorf("host %s: %w", h.Hostname, err)
		}
		out.Hosts = append(out.Hosts, h)
	}
	for _, r := range e.Routes {
		var err error
		switch r.Action.Type {
		case "redirect":
			if r.Action.To, err = res(r.Action.To); err != nil {
				return nil, fmt.Errorf("route %s: %w", r.RouteID, err)
			}
		case "rewrite":
			if r.Action.Replacement, err = res(r.Action.Replacement); err != nil {
				return nil, fmt.Errorf("route %s: %w", r.RouteID, err)
			}
		case "proxy":
			if r.Action.SetHeaders, err = resMap(r.Action.SetHeaders, "header"); err != nil {
				return nil, fmt.Errorf("route %s: %w", r.RouteID, err)
			}
			if r.Action.Rewrite != nil {
				rw := *r.Action.Rewrite
				if rw.Replacement, err = res(rw.Replacement); err != nil {
					return nil, fmt.Errorf("route %s: %w", r.RouteID, err)
				}
				r.Action.Rewrite = &rw
			}
		}
		if r.ResponseHeaders != nil {
			rh, err := resRH(*r.ResponseHeaders)
			if err != nil {
				return nil, fmt.Errorf("route %s: %w", r.RouteID, err)
			}
			r.ResponseHeaders = &rh
		}
		if r.Hosts == nil {
			r.Hosts = []string{}
		}
		out.Routes = append(out.Routes, r)
	}
	return &out, nil
}

// InputParams are the inputs of InputFromConfig.
type InputParams struct {
	Instance     string
	DeploymentID string
	SiteSlug     string
	Config       *model.Config
	Catalog      *model.RevisionCatalog
	// Hosts: site addresses (validate.ResolveSiteHosts); nil = without detected addresses.
	Hosts map[string]validate.SiteHost
	// Upstreams: port map of an ENABLED container (agent: portName → loopback host port; exporter: portName → container
	// port). nil = every container gets an empty map.
	Upstreams func(containerSlug string) map[string]int
	// Secret: the value of a secret ("" / false = none). nil = the plain values in Config.secrets.
	Secret func(secretSlug string) (string, bool)
	// FileContent: the bytes of a File content blob (hex sha256) — TLS files mode certificates. nil / false = not available
	// (the host gets no PEM pair; Caddy then has no certificate for it).
	FileContent func(hex string) ([]byte, bool)
	// NotFoundPage: fallback for a notFoundPageId that is not in the catalog (agent: <dataDir>/notfound/<id>.html).
	NotFoundPage func(id string) (string, bool)
	// DNSConfig: overrides DNSProviderConfig (exporter: placeholders instead of the secret). nil = DNSProviderConfig.
	DNSConfig func(provider, secret string) (map[string]any, error)
}

// SecretFromConfig returns the plain value of a secret of cfg.
func SecretFromConfig(cfg *model.Config, slug string) (string, bool) {
	for _, s := range cfg.Secrets {
		if s.SecretSlug == slug {
			if s.Value != nil {
				return *s.Value, true
			}
			return "", false
		}
	}
	return "", false
}

// DNSConfigFor maps EdgeTls.dnsProvider + dnsTokenSecretSlug to the Caddy DNS provider config (dns-01).
func DNSConfigFor(h model.EdgeHost, secret func(string) (string, bool), build func(provider, secret string) (map[string]any, error)) (map[string]any, error) {
	if h.TLS.DNSProvider == nil || *h.TLS.DNSProvider == "" {
		return nil, fmt.Errorf("host %s: dns-01 needs dnsProvider", h.Hostname)
	}
	if h.TLS.DNSTokenSecretSlug == nil {
		return nil, fmt.Errorf("host %s: dns-01 needs dnsTokenSecretSlug", h.Hostname)
	}
	val, ok := secret(*h.TLS.DNSTokenSecretSlug)
	if !ok || val == "" {
		return nil, fmt.Errorf("host %s: secret %s has no value", h.Hostname, *h.TLS.DNSTokenSecretSlug)
	}
	if build == nil {
		build = DNSProviderConfig
	}
	pc, err := build(*h.TLS.DNSProvider, val)
	if err != nil {
		return nil, fmt.Errorf("host %s: %w", h.Hostname, err)
	}
	return pc, nil
}

func siteOf(cfg *model.Config, slug string) *model.Site {
	for i := range cfg.Sites {
		if cfg.Sites[i].SiteSlug == slug {
			return &cfg.Sites[i]
		}
	}
	return nil
}

// InputFromConfig builds the edge Input of one site of a config (the agent's head → Caddy path, shared with the
// exporter). nil Input + nil error = the site is not in the config (the agent then runs its default edge).
func InputFromConfig(p InputParams) (*Input, []Note, error) {
	site := siteOf(p.Config, p.SiteSlug)
	if site == nil {
		return nil, nil, nil
	}
	re, err := ResolveEdge(p.Config, p.DeploymentID, site, p.Hosts)
	if err != nil {
		return nil, nil, err
	}
	secret := p.Secret
	if secret == nil {
		secret = func(s string) (string, bool) { return SecretFromConfig(p.Config, s) }
	}
	var notes []Note
	in := &Input{Instance: p.Instance, SiteSlug: p.SiteSlug, HTTPPort: re.HTTPPort, HTTPSPort: re.HTTPSPort, MaxBodyBytes: re.MaxBodyBytes, Hosts: re.Hosts, Routes: re.Routes,
		Upstreams: map[string]map[string]int{}, FilesPEM: map[string]PEMPair{}, DNSConfigs: map[string]map[string]any{}}
	if id := site.Config.NotFoundPageID; id != nil {
		if pg, ok := p.Catalog.NotFoundPages[*id]; ok {
			in.NotFound404 = pg.HTMLCode
		} else if p.NotFoundPage != nil {
			if html, ok := p.NotFoundPage(*id); ok {
				in.NotFound404 = html
			}
		}
		if in.NotFound404 == "" {
			notes = append(notes, Note{Code: "NOT_FOUND_PAGE_MISSING", Message: fmt.Sprintf("not-found page %s is not in the catalog: the built-in page is used", *id)})
		}
	}
	for _, c := range site.Config.Containers {
		if !c.Enabled {
			continue
		}
		var m map[string]int
		if p.Upstreams != nil {
			m = p.Upstreams(c.ContainerSlug)
		}
		in.Upstreams[c.ContainerSlug] = m
	}
	for _, h := range re.Hosts {
		switch h.TLS.Mode {
		case "files":
			for _, f := range p.Config.Files {
				if f.FileSlug == h.TLS.CertFileSlug && f.Content != nil && p.FileContent != nil {
					if cert, ok := p.FileContent(strings.TrimPrefix(f.Content.Sha256, "sha256:")); ok {
						k, _ := secret(h.TLS.KeySecretSlug)
						in.FilesPEM[h.Hostname] = PEMPair{Cert: RepairPEM(string(cert)), Key: RepairPEM(k)}
					}
				}
			}
		case "acme":
			if h.TLS.Challenge == "dns-01" {
				pc, err := DNSConfigFor(h, secret, p.DNSConfig)
				if err != nil {
					return nil, nil, err
				}
				in.DNSConfigs[h.Hostname] = pc
			}
		}
	}
	return in, notes, nil
}
