package validate

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// Round D part 2: storage locations (TS StorageLocation helpers) and files rendered ONCE PER SITE.

// AgentHomeReserved are the top-level names under agentHome that belong to the agent (TS AGENT_HOME_RESERVED).
var AgentHomeReserved = []string{"site.key", "opm.db", "opm.db-wal", "opm.db-shm", "blobs", "snapshots", "certs", "edge", "transfers", "tunnel", "logs", "notfound", "rendered", "secrets", "vol", "files", "volumes"}

// AutoLocation is {kind:"auto"}.
func AutoLocation() *model.StorageLocation { return &model.StorageLocation{Kind: "auto"} }

// LocationPath is TS locationPath: "./files/<slug>" / "./volumes/<slug>" for auto, the "./…" or absolute path as
// written; with agentHome != "" the "./" paths become absolute (agentHome + rest).
func LocationPath(l *model.StorageLocation, kind, slug, agentHome string) string {
	n := l.Normalized()
	rel := "./" + kind + "/" + slug
	if n.Kind == "path" {
		rel = n.Path
	}
	if agentHome == "" || !strings.HasPrefix(rel, "./") {
		return rel
	}
	return strings.TrimRight(agentHome, "/") + "/" + rel[2:]
}

// LocationLabel is TS locationLabel: "auto · <agentHome>/files/<slug>" or the custom path.
func LocationLabel(l *model.StorageLocation, kind, slug, agentHome string) string {
	if agentHome == "" {
		agentHome = "<agentHome>"
	}
	if n := l.Normalized(); n.Kind == "path" {
		return n.Path
	}
	return "auto · " + agentHome + "/" + kind + "/" + slug
}

var wsAny = regexp.MustCompile(`\s`)

// CheckLocation is TS checkLocation (V_LOCATION). nil = auto (no issue).
func CheckLocation(l *model.StorageLocation, kind, path string, is *Issues) {
	if l == nil {
		return
	}
	if l.Kind != "auto" && l.Kind != "path" {
		is.err(path, "V_LOCATION", `location must be { kind: "auto" } or { kind: "path", path }`)
		return
	}
	if l.Kind == "auto" {
		return
	}
	p := l.Path
	if p == "" || expr.IsExpr(p) {
		is.err(path+".path", "V_LOCATION", `a custom location needs a path ("./…" = relative to the agent home, or an absolute path); no {{ }}`)
		return
	}
	if strings.HasPrefix(p, "./") {
		segs := strings.Split(p[2:], "/")
		for _, g := range segs {
			if g == "" || g == "." || g == ".." || wsAny.MatchString(g) {
				is.err(path+".path", "V_LOCATION", fmt.Sprintf(`"%s": a "./" path is relative to the agent home: no empty, ".", ".." segments or spaces`, p))
				return
			}
		}
		if contains(AgentHomeReserved, segs[0]) && segs[0] != kind {
			is.err(path+".path", "V_LOCATION", fmt.Sprintf(`"%s": ./%s belongs to the agent`, p, segs[0]))
			return
		}
		if segs[0] == kind && len(segs) < 2 {
			is.err(path+".path", "V_LOCATION", fmt.Sprintf(`"%s": pick a name inside ./%s/`, p, kind))
		}
	} else if !strings.HasPrefix(p, "/") || !IsSafeMountPath(p) {
		is.err(path+".path", "V_LOCATION", fmt.Sprintf(`"%s" must start with "./" (agent home) or be an absolute path (not "/", no spaces, no "..")`, p))
	}
}

func nestedOrSame(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

type siteLoc struct{ what, path, at string }

func siteLocations(cfg *model.Config, sc *model.SiteConfig, cb, agentHome string) []siteLoc {
	var locs []siteLoc
	for _, f := range cfg.Files {
		locs = append(locs, siteLoc{"file " + f.FileSlug, LocationPath(f.Location, "files", f.FileSlug, agentHome), "files[" + f.FileSlug + "].location"})
	}
	for _, v := range sc.VolumeList() {
		locs = append(locs, siteLoc{"volume " + v.VolumeSlug, LocationPath(v.Location, "volumes", v.VolumeSlug, agentHome), cb + ".volumes[" + v.VolumeSlug + "].location"})
	}
	return locs
}

// checkSiteLocations: the files + this site's volumes are never the same place or nested (TS validateSite; relative vs
// absolute are not compared here: the agent compares them at staging, CheckResolvedLocations).
func checkSiteLocations(cfg *model.Config, site *model.Site, cb string, is *Issues) {
	locs := siteLocations(cfg, &site.Config, cb, "")
	for i := range locs {
		for k := 0; k < i; k++ {
			a, b := locs[i], locs[k]
			if strings.HasPrefix(a.path, "./") != strings.HasPrefix(b.path, "./") {
				continue
			}
			if nestedOrSame(strings.TrimRight(a.path, "/"), strings.TrimRight(b.path, "/")) {
				at := a.at // TS: both files → a; a file → the other (b); else a
				if !strings.HasPrefix(b.at, "files[") || !strings.HasPrefix(a.at, "files[") {
					if strings.HasPrefix(a.at, "files[") {
						at = b.at
					}
				}
				is.err(at, "V_LOCATION", fmt.Sprintf("%s (%s) and %s (%s) are the same place or nested on site %s", a.what, a.path, b.what, b.path, site.SiteSlug))
			}
		}
	}
}

// CheckResolvedLocations is the agent's staging check of one site's locations resolved against agentHome: never the
// same place or nested (also "./" vs absolute), never agentHome itself or one of its parents, and never inside an agent
// directory of agentHome other than the kind's own (absolute paths too). Messages carry V_LOCATION.
func CheckResolvedLocations(cfg *model.Config, siteSlug, agentHome string) []model.ValidationIssue {
	is := NewIssues(ModeAgent)
	var site *model.Site
	for i := range cfg.Sites {
		if cfg.Sites[i].SiteSlug == siteSlug {
			site = &cfg.Sites[i]
		}
	}
	if site == nil {
		return is.List
	}
	home := filepath.Clean(agentHome)
	cb := "sites[" + siteSlug + "].config"
	locs := siteLocations(cfg, &site.Config, cb, home)
	for i := range locs {
		p := filepath.Clean(locs[i].path)
		kind := "files"
		if strings.HasPrefix(locs[i].what, "volume ") {
			kind = "volumes"
		}
		if p == home || strings.HasPrefix(home, p+"/") {
			is.err(locs[i].at, "V_LOCATION", fmt.Sprintf("%s (%s) contains the agent home %s", locs[i].what, p, home))
			continue
		}
		if strings.HasPrefix(p, home+"/") {
			first := strings.SplitN(strings.TrimPrefix(p, home+"/"), "/", 2)[0]
			if contains(AgentHomeReserved, first) && first != kind {
				is.err(locs[i].at, "V_LOCATION", fmt.Sprintf("%s (%s) is inside the agent directory %s/%s", locs[i].what, p, home, first))
				continue
			}
		}
		for k := 0; k < i; k++ {
			if nestedOrSame(p, filepath.Clean(locs[k].path)) {
				is.err(locs[i].at, "V_LOCATION", fmt.Sprintf("%s (%s) and %s (%s) are the same place or nested on site %s", locs[i].what, p, locs[k].what, filepath.Clean(locs[k].path), siteSlug))
			}
		}
	}
	return is.List
}

// RenderedFile is TS RenderedFile / RenderFileError: Value (ok) or Err with line:col.
type RenderedFile struct {
	Value  string
	Masked string
	Secret bool
	// SecretHashed (§21 #103): every secret part is a mosquitto_passwd hash → the file is written 0444, not 0400.
	SecretHashed bool
	Err          *FileExprIssue
}

// RenderSiteFile is TS renderSiteFile: renders a file ONCE for a site (field file: var, secret, site, self, deployment).
func RenderSiteFile(src string, ectx *expr.Context) RenderedFile {
	r, err := expr.Resolve(src, ectx, expr.FieldFile)
	if err != nil {
		fi := fileIssue(src, asExprErr(err))
		return RenderedFile{Err: &fi}
	}
	return RenderedFile{Value: r.Value, Masked: r.Masked, Secret: r.Secret, SecretHashed: r.SecretHashed}
}

// SiteFileExprContext is TS siteFileExprContext: var, secret (secretsSet != nil → "•••" / nil), site, self,
// deployment. siteMap nil = SiteExprMap without detected addresses.
func SiteFileExprContext(cfg *model.Config, services map[string]string, images map[string]CatalogImage, deploymentID string, secretsSet []string, siteSlug string, siteMap map[string]expr.SiteRef) *expr.Context {
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
	ctx := baseCtx(deploymentID, siteSlug, sm[siteSlug].Host, VarValues(cfg))
	ctx.Secret = secret
	ctx.Site = sm
	ctx.Containers = SiteContainersMap(cfg, images, deploymentID, siteSlug)
	return ctx
}

// SiteContainersEnv is TS siteContainersEnv (§21 #99): containers.<slug>.env.<NAME> of ONE site — the FINAL env value
// of every container (image default overridden by the container env, expressions resolved with ResolveContainerConfig,
// exactly what the driver applies). Secrets: secretsSet != nil → "•••" (cloud-like), else the plain values.
func SiteContainersEnv(cfg *model.Config, services map[string]string, images map[string]CatalogImage, deploymentID string, secretsSet []string, siteSlug string, siteMap map[string]expr.SiteRef) map[string]map[string]expr.PeerEnv {
	out := map[string]map[string]expr.PeerEnv{}
	var site *model.Site
	for i := range cfg.Sites {
		if cfg.Sites[i].SiteSlug == siteSlug {
			site = &cfg.Sites[i]
		}
	}
	if site == nil {
		return out
	}
	sm := siteMap
	if sm == nil {
		sm = SiteExprMap(cfg, services, images, deploymentID, nil)
	}
	for i := range site.Config.Containers {
		c := &site.Config.Containers[i]
		if c.ContainerSlug == "" {
			continue
		}
		img := images[ImageKey(c.ServiceSlug, c.Version)]
		ectx := ContainerExprContext(cfg, services, images, deploymentID, secretsSet, siteSlug, c, sm)
		cr := ResolveContainerConfig(cfg, c, NormalizeImageEnvs(img.Env), img.Volumes, ectx)
		env := map[string]expr.PeerEnv{}
		for _, src := range cr.Env {
			r, ok := cr.Resolution.Env[src.Name]
			if !ok {
				continue
			}
			if r.Err != nil {
				env[src.Name] = expr.PeerEnv{Error: &expr.Error{Code: r.Err.Code, Message: r.Err.Message}}
				continue
			}
			var v *string
			switch {
			case src.SecretRef != nil:
				v = src.SecretRef.Value
			case src.Src == nil:
				v = nil
			default:
				x := r.Result.Value
				v = &x
			}
			env[src.Name] = expr.PeerEnv{Value: v, Secret: r.Result.Secret}
		}
		out[c.ContainerSlug] = env
	}
	return out
}

// SiteDevtoolExprContext is TS siteDevtoolExprContext (§21 #99): the site file context + containers[<slug>].env
// (SiteContainersEnv). Dev-tool preset values resolve with it and field expr.FieldDevtool.
func SiteDevtoolExprContext(cfg *model.Config, services map[string]string, images map[string]CatalogImage, deploymentID string, secretsSet []string, siteSlug string, siteMap map[string]expr.SiteRef) *expr.Context {
	sm := siteMap
	if sm == nil {
		sm = SiteExprMap(cfg, services, images, deploymentID, nil)
	}
	ctx := SiteFileExprContext(cfg, services, images, deploymentID, secretsSet, siteSlug, sm)
	env := SiteContainersEnv(cfg, services, images, deploymentID, secretsSet, siteSlug, sm)
	for slug, peer := range ctx.Containers {
		peer.Env = env[slug]
		if peer.Env == nil {
			peer.Env = map[string]expr.PeerEnv{}
		}
		ctx.Containers[slug] = peer
	}
	return ctx
}
