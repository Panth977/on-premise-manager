package validate

import (
	"strings"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// VarValues maps every variable slug to its value.
func VarValues(cfg *model.Config) map[string]any {
	out := map[string]any{}
	for _, v := range cfg.Variables {
		out[v.VariableSlug] = v.Value.V
	}
	return out
}

// SiteHost is TS SiteHost: the resolved address of one site.
type SiteHost struct {
	Host     *string     // the address for site.<slug>.host / self.host; nil = auto and not known (agent: E_NULL at use)
	Err      *expr.Error // an error of the host expression (not E_NULL: a null / empty result falls back to auto)
	Auto     bool        // the address is the auto one (host null, or the expression had no value)
	Fallback *string     // set when the host expression resolved to null / "" (warning V_HOST_AUTO)
}

// HostOptions is TS SiteHostOptions.
type HostOptions struct {
	Detected    map[string]string // siteSlug → advertised / detected address (agent: itself + peers)
	Placeholder bool              // true (cloud): unknown auto hosts become "<auto:slug>"; false (agent): nil
}

func baseCtx(deploymentID, slug string, host *string, vars map[string]any) *expr.Context {
	c := &expr.Context{Var: vars}
	c.Deployment.ID = deploymentID
	c.Self.Slug = slug
	c.Self.Host = host
	return c
}

// ResolveSiteHosts resolves every SiteConfig.host (TS resolveSiteHosts): nil / "" = auto; an expression that resolves
// to null or "" falls back to auto with a warning message; other expression errors are kept.
func ResolveSiteHosts(cfg *model.Config, deploymentID string, o HostOptions) map[string]SiteHost {
	vars := VarValues(cfg)
	out := map[string]SiteHost{}
	for _, s := range cfg.Sites {
		var auto *string
		if d, ok := o.Detected[s.SiteSlug]; ok {
			auto = expr.Str(d)
		} else if o.Placeholder {
			auto = expr.Str(expr.AutoHostPlaceholder(s.SiteSlug))
		}
		h := s.Config.Host
		if h == nil || strings.TrimSpace(*h) == "" {
			out[s.SiteSlug] = SiteHost{Host: auto, Auto: true}
			continue
		}
		r, err := expr.Resolve(*h, baseCtx(deploymentID, s.SiteSlug, nil, vars), expr.FieldHost)
		if err != nil {
			e := asExprErr(err)
			if e.Code == expr.ENull {
				m := hostFallbackMessage(*h, vars)
				out[s.SiteSlug] = SiteHost{Host: auto, Auto: true, Fallback: &m}
			} else {
				out[s.SiteSlug] = SiteHost{Err: e}
			}
			continue
		}
		if strings.TrimSpace(r.Value) == "" {
			m := hostFallbackMessage(*h, vars)
			out[s.SiteSlug] = SiteHost{Host: auto, Auto: true, Fallback: &m}
			continue
		}
		out[s.SiteSlug] = SiteHost{Host: expr.Str(r.Value)}
	}
	return out
}

func hostFallbackMessage(src string, vars map[string]any) string {
	names := []string{}
	if refs, err := expr.ExprRefs(src); err == nil {
		for _, r := range refs {
			if !strings.HasPrefix(r, "var.") {
				continue
			}
			n := r[4:]
			v, ok := vars[n]
			if s, isStr := v.(string); !ok || v == nil || (isStr && s == "") {
				names = append(names, n)
			}
		}
	}
	what := "the host expression has no value"
	if len(names) > 0 {
		what = "var " + strings.Join(names, ", ") + " has no value"
	}
	return what + " — leave host on auto or set the variable (the site uses its detected address)"
}

// ImagePortNumber resolves an image port number (nil when it cannot resolve).
func ImagePortNumber(p model.ImagePort, vars map[string]any, deploymentID string) *int {
	if p.Port.Num != nil {
		n := int(*p.Port.Num)
		return &n
	}
	n, err := expr.ResolveNumberField(p.Port.Any(), baseCtx(deploymentID, "", nil, vars), expr.FieldImagePort)
	if err != nil {
		return nil
	}
	return n
}

// SiteExprMap builds the site.<slug>.host / site.<slug>.port.<containerSlug>.<portName> map (TS siteExprMap).
// images nil → only the explicit port rows are used. hosts nil → ResolveSiteHosts(cfg, deploymentID, {}) (agent-like:
// unknown auto hosts are nil).
func SiteExprMap(cfg *model.Config, services map[string]string, images map[string]CatalogImage, deploymentID string, hosts map[string]SiteHost) map[string]expr.SiteRef {
	vars := VarValues(cfg)
	if hosts == nil {
		hosts = ResolveSiteHosts(cfg, deploymentID, HostOptions{})
	}
	out := map[string]expr.SiteRef{}
	for _, s := range cfg.Sites {
		host := hosts[s.SiteSlug].Host
		port := map[string]map[string]*float64{}
		for i := range s.Config.Containers {
			c := &s.Config.Containers[i]
			if _, ok := services[c.ServiceSlug]; !ok || c.ContainerSlug == "" {
				continue
			}
			var rows []model.ContainerPort
			if img, ok := images[ImageKey(c.ServiceSlug, c.Version)]; ok {
				for _, p := range ContainerPorts(c, img.Ports) {
					rows = append(rows, p.ContainerPort)
				}
			} else {
				rows = c.Ports
			}
			m := map[string]*float64{}
			for _, p := range rows {
				var hp *float64
				if n, err := expr.ResolveNumberField(p.HostPort.Any(), baseCtx(deploymentID, s.SiteSlug, host, vars), expr.FieldHostPort); err == nil && n != nil {
					f := float64(*n)
					hp = &f
				}
				m[p.PortName] = hp
			}
			port[c.ContainerSlug] = m
		}
		out[s.SiteSlug] = expr.SiteRef{Host: host, Port: port}
	}
	return out
}

// SiteContainersMap is TS siteContainersMap (§21 #88): containers.<containerSlug>.host / .port.<portName> of ONE site —
// every container of the site (self and disabled ones included); host = the containerSlug (its network alias), port =
// the INTERNAL port of each image port (nil when it does not resolve or the image is unknown).
func SiteContainersMap(cfg *model.Config, images map[string]CatalogImage, deploymentID, siteSlug string) map[string]expr.ContainerPeer {
	out := map[string]expr.ContainerPeer{}
	var site *model.Site
	for i := range cfg.Sites {
		if cfg.Sites[i].SiteSlug == siteSlug {
			site = &cfg.Sites[i]
		}
	}
	if site == nil {
		return out
	}
	vars := VarValues(cfg)
	for i := range site.Config.Containers {
		c := &site.Config.Containers[i]
		if c.ContainerSlug == "" {
			continue
		}
		port := map[string]*float64{}
		if img, ok := images[ImageKey(c.ServiceSlug, c.Version)]; ok {
			for _, p := range img.Ports {
				var v *float64
				if n := ImagePortNumber(p, vars, deploymentID); n != nil {
					f := float64(*n)
					v = &f
				}
				port[p.Name] = v
			}
		}
		out[c.ContainerSlug] = expr.ContainerPeer{Host: c.ContainerSlug, Enabled: c.Enabled, Port: port}
	}
	return out
}

// ContainerExprContext is the context of one container (env values, image env defaults, mount paths, rendered files).
// file.<slug>.path: the literal mount path of the first mount of that file (resolveContainer answers the resolved one).
func ContainerExprContext(cfg *model.Config, services map[string]string, images map[string]CatalogImage, deploymentID string, secretsSet []string, siteSlug string, c *model.Container, siteMap map[string]expr.SiteRef) *expr.Context {
	sm := siteMap
	if sm == nil {
		sm = SiteExprMap(cfg, services, images, deploymentID, nil)
	}
	secret := map[string]*string{}
	for _, s := range cfg.Secrets {
		if secretsSet != nil {
			if contains(secretsSet, s.SecretSlug) {
				m := "•••"
				secret[s.SecretSlug] = &m
			} else if s.Value != nil && expr.IsDeferredValue(*s.Value) {
				secret[s.SecretSlug] = s.Value // required, set at deployment (DeferredConfig)
			} else {
				secret[s.SecretSlug] = nil
			}
		} else {
			secret[s.SecretSlug] = s.Value
		}
	}
	var imgVols []model.ImageVolume
	if img, ok := images[ImageKey(c.ServiceSlug, c.Version)]; ok {
		imgVols = img.Volumes
	}
	mounts := ContainerMounts(c, imgVols)
	file := map[string]expr.FileRef{}
	for _, f := range cfg.Files {
		var p *string
		for _, m := range mounts {
			if m.Source.Kind == "file" && m.Source.FileSlug == f.FileSlug {
				p = expr.Str(m.MountPath)
				break
			}
		}
		file[f.FileSlug] = expr.FileRef{Path: p}
	}
	ctx := baseCtx(deploymentID, siteSlug, sm[siteSlug].Host, VarValues(cfg))
	ctx.Container = &expr.ContainerRef{Slug: c.ContainerSlug}
	ctx.Secret = secret
	ctx.File = file
	ctx.Site = sm
	ctx.Containers = SiteContainersMap(cfg, images, deploymentID, siteSlug)
	return ctx
}

// EdgeExprContext is the context of the edge fields of one site (hosts nil → agent-like resolution).
func EdgeExprContext(cfg *model.Config, deploymentID, siteSlug string, hosts map[string]SiteHost) *expr.Context {
	if hosts == nil {
		hosts = ResolveSiteHosts(cfg, deploymentID, HostOptions{})
	}
	return baseCtx(deploymentID, siteSlug, hosts[siteSlug].Host, VarValues(cfg))
}

// DeferredConfig is the config used for the expression checks outside agent mode (TS varValues / secretValues with
// defer): a REQUIRED variable / secret without a value gets expr.DeployPlaceholder (set at deployment), so its usages
// are never reported — only the variable / secret itself (V_VAR_REQUIRED / V_SECRET_REQUIRED). Agent mode stays strict.
func DeferredConfig(cfg *model.Config) *model.Config {
	cp := *cfg
	cp.Variables = append([]model.Variable(nil), cfg.Variables...)
	for i := range cp.Variables {
		v := &cp.Variables[i]
		if s, ok := v.Value.V.(string); v.Required && (v.Value.V == nil || (ok && s == "")) {
			v.Value = model.VariableValue{V: expr.DeployPlaceholder}
		}
	}
	cp.Secrets = append([]model.SecretDef(nil), cfg.Secrets...)
	for i := range cp.Secrets {
		s := &cp.Secrets[i]
		if s.Required && (s.Value == nil || *s.Value == "") {
			p := expr.DeployPlaceholder
			s.Value = &p
		}
	}
	return &cp
}
