package validate

import (
	"fmt"
	"regexp"
	"strings"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// EdgeContainerInfo describes one container of the site for edge checks.
type EdgeContainerInfo struct {
	Enabled   bool
	HostPorts []int
	PortNames []string
	EdgePorts []string // ports with ContainerPort.edge true (nil = do not check)
	Slug      string
}

// EdgeOptions is TS EdgeValidateOptions.
type EdgeOptions struct {
	Base              string
	AgentPort         int
	AgentGuiAliasPort *int
	Containers        map[string]EdgeContainerInfo // nil = do not check
	FileSlugs         []string                     // nil = do not check
	SecretSlugs       []string                     // nil = do not check
}

var ipv6ish = regexp.MustCompile(`(?i)^[0-9a-f:.]+$`)

// IsIPLiteral reports an IPv4 or IPv6 literal.
func IsIPLiteral(s string) bool {
	return RE.IPv4.MatchString(s) || (strings.Contains(s, ":") && ipv6ish.MatchString(s))
}

var pathBad = regexp.MustCompile(`[\s?#]`)

// ValidateEdge checks the edge of one site (§12.4).
func ValidateEdge(edge *model.Edge, o EdgeOptions, is *Issues) {
	b := o.Base
	if b == "" {
		b = "edge"
	}
	checkPort(edge.HTTPPort, b+".httpPort", is, 1)
	if edge.HTTPSPort != nil {
		checkPort(*edge.HTTPSPort, b+".httpsPort", is, 1)
		if edge.HTTPPort == *edge.HTTPSPort {
			is.err(b+".httpsPort", "V_PORT_CONFLICT", "httpPort and httpsPort must differ")
		}
	}
	type kp struct {
		k string
		p *int
	}
	hp := edge.HTTPPort
	for _, x := range []kp{{"httpPort", &hp}, {"httpsPort", edge.HTTPSPort}} {
		if x.p == nil {
			continue
		}
		p := *x.p
		if p == o.AgentPort {
			is.err(b+"."+x.k, "V_PORT_CONFLICT", fmt.Sprintf("%s %d is the agentPort", x.k, p))
		}
		if o.AgentGuiAliasPort != nil && p == *o.AgentGuiAliasPort {
			is.err(b+"."+x.k, "V_PORT_CONFLICT", fmt.Sprintf("%s %d is the agentGuiAliasPort", x.k, p))
		}
	}
	if len(edge.Hosts) == 0 {
		is.Warn(b+".hosts", "V_EDGE_NO_HOSTS", "no hosts: every request gets 404 (HTTP) or a TLS failure")
	}
	if len(edge.Hosts) > Limits.Hosts {
		is.err(b+".hosts", "V_TOO_MANY", fmt.Sprintf("at most %d hosts", Limits.Hosts))
	}
	if len(edge.Routes) > Limits.Routes {
		is.err(b+".routes", "V_TOO_MANY", fmt.Sprintf("at most %d routes", Limits.Routes))
	}
	names := []string{}
	for _, h := range edge.Hosts {
		names = append(names, strings.ToLower(h.Hostname))
	}
	for i, h := range edge.Hosts {
		n := h.Hostname
		label := n
		if label == "" {
			label = fmt.Sprint(i)
		}
		at := fmt.Sprintf("%s.hosts[%s]", b, label)
		if n == "" {
			is.err(at+".hostname", "V_REQUIRED", "hostname is empty")
			continue
		}
		for j, x := range names {
			if x == strings.ToLower(n) {
				if j != i {
					is.err(at+".hostname", "V_DUPLICATE", fmt.Sprintf("host %s is listed two times", n))
				}
				break
			}
		}
		if expr.IsExpr(n) {
			checkExprField(n, expr.FieldEdge, at+".hostname", is)
		} else if n != "*" && !IsIPLiteral(n) && !RE.DNSName.MatchString(n) {
			is.err(at+".hostname", "V_EDGE_HOST", fmt.Sprintf(`"%s" is not a DNS name, a wildcard, an IP or "*"`, n))
		}
		t := h.TLS
		if !contains([]string{"none", "internal", "acme", "files"}, t.Mode) {
			is.err(at+".tls.mode", "V_ENUM", "tls.mode must be none | internal | acme | files")
			continue
		}
		if t.Mode == "acme" {
			if n == "*" || IsIPLiteral(n) {
				is.err(at+".tls", "V_EDGE_TLS", `ACME cannot issue a certificate for an IP or "*"; use internal`)
			}
			if !RE.Email.MatchString(t.Email) {
				is.err(at+".tls.email", "V_FORMAT", "ACME needs an email")
			}
			if !contains([]string{"http-01", "tls-alpn-01", "dns-01"}, t.Challenge) {
				is.err(at+".tls.challenge", "V_ENUM", "challenge must be http-01 | tls-alpn-01 | dns-01")
			}
			if strings.HasPrefix(n, "*.") && t.Challenge != "dns-01" {
				is.err(at+".tls.challenge", "V_EDGE_TLS", "a wildcard certificate needs the dns-01 challenge")
			}
			if t.Challenge == "dns-01" {
				if t.DNSProvider == nil || *t.DNSProvider == "" || t.DNSTokenSecretSlug == nil || *t.DNSTokenSecretSlug == "" {
					is.err(at+".tls", "V_EDGE_TLS", "dns-01 needs dnsProvider and dnsTokenSecretSlug")
				} else if o.SecretSlugs != nil && !contains(o.SecretSlugs, *t.DNSTokenSecretSlug) {
					is.err(at+".tls.dnsTokenSecretSlug", "V_REF", fmt.Sprintf("secret %s does not exist", *t.DNSTokenSecretSlug))
				}
			} else {
				need := 443
				if t.Challenge == "http-01" {
					need = 80
				}
				is.Warn(at+".tls", "V_EDGE_ACME_INTERNET", fmt.Sprintf("%s needs internet on this site AND %s reachable from the internet on port %d", t.Challenge, n, need))
				var have *int
				if need == 80 {
					have = &hp
				} else {
					have = edge.HTTPSPort
				}
				if have == nil || *have != need {
					is.err(at+".tls.challenge", "V_EDGE_TLS", fmt.Sprintf("%s needs the edge on port %d", t.Challenge, need))
				}
			}
		}
		if t.Mode == "files" {
			if t.CertFileSlug == "" || t.KeySecretSlug == "" {
				is.err(at+".tls", "V_EDGE_TLS", "files mode needs certFileSlug and keySecretSlug")
			}
			if t.CertFileSlug != "" && o.FileSlugs != nil && !contains(o.FileSlugs, t.CertFileSlug) {
				is.err(at+".tls.certFileSlug", "V_REF", fmt.Sprintf("file %s does not exist", t.CertFileSlug))
			}
			if t.KeySecretSlug != "" && o.SecretSlugs != nil && !contains(o.SecretSlugs, t.KeySecretSlug) {
				is.err(at+".tls.keySecretSlug", "V_REF", fmt.Sprintf("secret %s does not exist", t.KeySecretSlug))
			}
		}
		if t.Mode != "none" && edge.HTTPSPort == nil {
			is.Warn(at+".tls", "V_EDGE_TLS_NO_HTTPS", "TLS is set but httpsPort is null (no HTTPS listener)")
		}
		if h.RedirectHTTPToHTTPS && (t.Mode == "none" || edge.HTTPSPort == nil) {
			is.err(at+".redirectHttpToHttps", "V_EDGE_TLS", "redirectHttpToHttps needs TLS and an httpsPort")
		}
	}
	validateEdgeRoundE(edge, o, b, is)
}

func intp(i int) *int { return &i }

// DefaultEdge is the edge of a new site.
func DefaultEdge() model.Edge {
	return model.Edge{HTTPPort: 80, HTTPSPort: intp(443), Hosts: []model.EdgeHost{NewEdgeHost("*")}, Routes: []model.EdgeRoute{}}
}

// DefaultSiteConfig is the config of a new site (host nil = auto-detect, the default).
func DefaultSiteConfig(host *string) model.SiteConfig {
	return model.SiteConfig{Host: host, AgentPort: 7080, AgentGuiAliasPort: intp(8080), Volumes: &[]model.SiteVolume{}, Containers: []model.Container{}, Depends: []model.SiteRef{}, Edge: DefaultEdge()}
}

// EmptyConfig is a config with empty sections.
func EmptyConfig() model.Config {
	return model.Config{Variables: []model.Variable{}, Secrets: []model.SecretDef{}, Files: []model.FileDef{}, Sites: []model.Site{}}
}
