package validate

import (
	"fmt"
	"regexp"
	"strings"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

type globals struct {
	siteSlugs, fileSlugs, secretSlugs []string
	hosts                             map[string]SiteHost
	siteMap                           map[string]expr.SiteRef
}

// ValidateConfig validates a whole Config (template, cloud draft, publish, or agent staging).
func ValidateConfig(cfg *model.Config, ctx Context) *Issues {
	is := NewIssues(ctx.Mode)
	template := ctx.Mode == ModeTemplate
	if cfg == nil {
		is.err("", "V_TYPE", "config must be an object")
		return is
	}
	if ctx.Services == nil {
		ctx.Services = map[string]string{}
	}
	if ctx.Images == nil {
		ctx.Images = map[string]CatalogImage{}
	}
	if len(cfg.Variables) > Limits.Variables {
		is.err("variables", "V_TOO_MANY", fmt.Sprintf("at most %d", Limits.Variables))
	}
	if len(cfg.Secrets) > Limits.Secrets {
		is.err("secrets", "V_TOO_MANY", fmt.Sprintf("at most %d", Limits.Secrets))
	}
	if len(cfg.Files) > Limits.Files {
		is.err("files", "V_TOO_MANY", fmt.Sprintf("at most %d", Limits.Files))
	}
	if len(cfg.Sites) > Limits.Sites {
		is.err("sites", "V_TOO_MANY", fmt.Sprintf("at most %d", Limits.Sites))
	}
	var vs, ss, fs, sites []string
	for _, v := range cfg.Variables {
		vs = append(vs, v.VariableSlug)
	}
	for _, v := range cfg.Secrets {
		ss = append(ss, v.SecretSlug)
	}
	for _, v := range cfg.Files {
		fs = append(fs, v.FileSlug)
	}
	for _, v := range cfg.Sites {
		sites = append(sites, v.SiteSlug)
	}
	for _, d := range dupes(vs) {
		is.err("variables["+d+"]", "V_DUPLICATE", fmt.Sprintf("variable %s is defined twice", d))
	}
	for _, d := range dupes(ss) {
		is.err("secrets["+d+"]", "V_DUPLICATE", fmt.Sprintf("secret %s is defined twice", d))
	}
	for _, d := range dupes(fs) {
		is.err("files["+d+"]", "V_DUPLICATE", fmt.Sprintf("file %s is defined twice", d))
	}
	for _, d := range dupes(sites) {
		is.err("sites["+d+"]", "V_DUPLICATE", fmt.Sprintf("site %s is defined twice", d))
	}
	for i := range cfg.Variables {
		validateVariable(&cfg.Variables[i], "variables["+cfg.Variables[i].VariableSlug+"]", is)
	}
	for i := range cfg.Secrets {
		s := cfg.Secrets[i]
		if ctx.SecretsSet != nil && !template {
			if contains(ctx.SecretsSet, s.SecretSlug) {
				x := "x"
				s.Value = &x
			} else {
				s.Value = nil
			}
			validateSecretDef(&s, "secrets["+s.SecretSlug+"]", is, false)
		} else {
			validateSecretDef(&s, "secrets["+s.SecretSlug+"]", is, template)
		}
	}
	for i := range cfg.Files {
		validateFileDef(&cfg.Files[i], "files["+cfg.Files[i].FileSlug+"]", is)
	}
	if len(cfg.Sites) == 0 {
		is.Err("sites", "V_NO_SITES", "a deployment needs at least one site", true)
	}
	if ctx.Mode != ModeAgent {
		cfg = DeferredConfig(cfg) // required variables / secrets without a value are set at deployment (round E)
	}
	hosts := ResolveSiteHosts(cfg, ctx.DeploymentID, HostOptions{Detected: ctx.DetectedHosts, Placeholder: ctx.Mode != ModeAgent})
	g := globals{siteSlugs: sites, fileSlugs: fs, secretSlugs: ss, hosts: hosts, siteMap: SiteExprMap(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, hosts)}
	if g.fileSlugs == nil {
		g.fileSlugs = []string{}
	}
	if g.secretSlugs == nil {
		g.secretSlugs = []string{}
	}
	if _, cyc := SiteOrder(cfg); cyc != nil {
		is.err("sites", "V_CYCLE", "site depends form a cycle: "+strings.Join(cyc, " → "))
	}
	for i := range cfg.Sites {
		validateSite(&cfg.Sites[i], cfg, ctx, is, &g)
	}
	return is
}

var secretExprRe = regexp.MustCompile(`\{\{[^}]*(secret\.|container\.env\.)`)

type listener struct {
	port  int
	proto string
	what  string
	path  string
}

func validateSite(site *model.Site, cfg *model.Config, ctx Context, is *Issues, g *globals) {
	b := "sites[" + site.SiteSlug + "]"
	checkSlug(site.SiteSlug, b+".siteSlug", is)
	if contains(ReservedSiteSlugs, site.SiteSlug) {
		is.err(b+".siteSlug", "V_RESERVED", fmt.Sprintf(`"%s" is reserved (agent GUI routes)`, site.SiteSlug))
	}
	checkLabel(site.Label, b+".label", is)
	sc := &site.Config
	cb := b + ".config"
	// host: nil / "" = auto (no issue); an expression with no value falls back to auto (warning only)
	if sc.Host != nil && strings.TrimSpace(*sc.Host) != "" {
		hv := *sc.Host
		if utf16Len(hv) > Limits.Value {
			is.err(cb+".host", "V_LENGTH", fmt.Sprintf("at most %d characters", Limits.Value))
		}
		if expr.IsExpr(hv) {
			checkExprField(hv, expr.FieldHost, cb+".host", is)
		}
		h := g.hosts[site.SiteSlug]
		syntaxOk := !expr.IsExpr(hv) || len(expr.CheckExpr(hv, expr.FieldHost)) == 0
		if h.Fallback != nil {
			if ctx.Mode != ModeTemplate {
				is.Warn(cb+".host", "V_HOST_AUTO", *h.Fallback)
			}
		} else if h.Err != nil && syntaxOk {
			is.Err(cb+".host", h.Err.Code, h.Err.Message, true)
		} else if !h.Auto && h.Host != nil && !expr.IsDeferredValue(*h.Host) && !IsIPLiteral(*h.Host) && !(RE.DNSName.MatchString(*h.Host) && !strings.HasPrefix(*h.Host, "*")) {
			is.Err(cb+".host", "V_HOST", fmt.Sprintf(`"%s" is not an IP or a DNS name`, *h.Host), true)
		}
	}
	checkPort(sc.AgentPort, cb+".agentPort", is, 1024)
	if sc.AgentGuiAliasPort != nil {
		checkPort(*sc.AgentGuiAliasPort, cb+".agentGuiAliasPort", is, 1024)
		if *sc.AgentGuiAliasPort == sc.AgentPort {
			is.err(cb+".agentGuiAliasPort", "V_PORT_CONFLICT", fmt.Sprintf("%d is already used by agentPort", *sc.AgentGuiAliasPort))
		}
	}
	for _, d := range sc.Depends {
		if d.SiteSlug == site.SiteSlug {
			is.err(cb+".depends", "V_CYCLE", "a site cannot depend on itself")
		} else if !contains(g.siteSlugs, d.SiteSlug) {
			is.err(cb+".depends", "V_REF", fmt.Sprintf("site %s does not exist", d.SiteSlug))
		}
	}
	if sc.NotFoundPageID != nil && ctx.NotFoundPageIDs != nil && !contains(ctx.NotFoundPageIDs, *sc.NotFoundPageID) {
		is.err(cb+".notFoundPageId", "V_REF", fmt.Sprintf("not-found page %s does not exist", *sc.NotFoundPageID))
	}
	// volumes (round D): the declared data folders of the site
	declared := map[string]bool{}
	seedRenders := []string{}           // files used by volume seeds (rendered once per site like mounted files)
	contentRenders := &ContentRenders{} // §21 #101 volume content entries with render expressions (volcontent.go)
	if sc.Volumes == nil {
		is.err(cb+".volumes", "V_REQUIRED", "volumes must be a list (may be empty)")
	} else {
		vols := *sc.Volumes
		if len(vols) > Limits.VolumesPerSite {
			is.err(cb+".volumes", "V_TOO_MANY", fmt.Sprintf("at most %d", Limits.VolumesPerSite))
		}
		var vn []string
		for _, v := range vols {
			vn = append(vn, v.VolumeSlug)
		}
		for _, d := range dupes(vn) {
			is.err(cb+".volumes["+d+"]", "V_DUPLICATE", fmt.Sprintf("volume %s is declared twice", d))
		}
		for _, v := range vols {
			vp := cb + ".volumes[" + v.VolumeSlug + "]"
			if !RE.VolumeSlug.MatchString(v.VolumeSlug) {
				is.err(vp+".volumeSlug", "V_FORMAT", fmt.Sprintf(`volume name "%s" must match ^[a-z0-9][a-z0-9_.-]{0,99}$`, v.VolumeSlug))
			} else {
				declared[v.VolumeSlug] = true
			}
			if utf16Len(v.Label) > Limits.Label {
				is.err(vp+".label", "V_LENGTH", fmt.Sprintf("label must be a string of at most %d characters", Limits.Label))
			}
			if utf16Len(v.Description) > Limits.Description {
				is.err(vp+".description", "V_LENGTH", fmt.Sprintf("description must be a string of at most %d characters", Limits.Description))
			}
			CheckLocation(v.Location, "volumes", vp+".location", is)
			if k := v.VolumeKind(); k != "dir" && k != "file" {
				is.err(vp+".kind", "V_VOLUME_KIND", "kind must be dir | file")
			}
			checkSeed(&v, vp, cfg, is, &seedRenders, contentRenders)
		}
		checkSiteLocations(cfg, site, cb, is)
		used := VolumeUsers(sc)
		for _, v := range vols {
			if _, ok := used[v.VolumeSlug]; !ok {
				extra := ""
				if v.Backup {
					extra = " and is still backed up"
				}
				is.Warn(cb+".volumes["+v.VolumeSlug+"]", "V_VOLUME_UNUSED", fmt.Sprintf("volume %s is declared but no container mounts it (its data stays on disk%s)", v.VolumeSlug, extra))
			}
		}
	}
	if len(sc.Containers) > Limits.ContainersPerSite {
		is.err(cb+".containers", "V_TOO_MANY", fmt.Sprintf("at most %d", Limits.ContainersPerSite))
	}
	services := ctx.Services
	var slugs []string
	for _, c := range sc.Containers {
		slugs = append(slugs, c.ContainerSlug)
	}
	for _, d := range dupes(slugs) {
		is.err(cb+".containers["+d+"].containerSlug", "V_DUPLICATE", fmt.Sprintf("container name %s is used twice on this site", d))
	}
	vars := VarValues(cfg)
	listeners := []listener{{sc.AgentPort, "tcp", "agentPort", cb + ".agentPort"}}
	if sc.AgentGuiAliasPort != nil {
		listeners = append(listeners, listener{*sc.AgentGuiAliasPort, "tcp", "agentGuiAliasPort", cb + ".agentGuiAliasPort"})
	}
	edgePorts := []int{sc.Edge.HTTPPort}
	if sc.Edge.HTTPSPort != nil {
		edgePorts = append(edgePorts, *sc.Edge.HTTPSPort)
	}
	containerInfo := map[string]EdgeContainerInfo{}
	renders := append([]string{}, seedRenders...) // fileSlugs mounted by an enabled container (rendered once per site) + volume seeds
	for ci := range sc.Containers {
		c := &sc.Containers[ci]
		at := cb + ".containers[" + c.ContainerSlug + "]"
		checkSlug(c.ContainerSlug, at+".containerSlug", is)
		if contains(ReservedContainerSlugs, c.ContainerSlug) {
			is.err(at+".containerSlug", "V_RESERVED", fmt.Sprintf(`"%s" is reserved (GUI route …/containers/new)`, c.ContainerSlug))
		}
		slug, ok := services[c.ServiceSlug]
		if !ok || slug == "" {
			is.err(at+".serviceSlug", "V_REF", fmt.Sprintf("service %s does not exist", c.ServiceSlug))
			continue
		}
		img, ok := ctx.Images[ImageKey(c.ServiceSlug, c.Version)]
		if !RE.Version.MatchString(c.Version) {
			is.err(at+".version", "V_FORMAT", fmt.Sprintf(`version "%s" is not an image version (v1, 3.1.0 …)`, c.Version))
		}
		if !ok {
			is.err(at+".version", "V_REF", fmt.Sprintf("image %s:%s does not exist", slug, c.Version))
			continue
		}
		if img.ImportStatus != "" && img.ImportStatus != "ready" {
			is.Err(at+".version", "V_IMAGE_NOT_READY", fmt.Sprintf("image %s:%s import is %s", slug, c.Version, img.ImportStatus), true)
		}
		if !contains([]string{"always", "on-failure", "never"}, c.Restart) {
			is.err(at+".restart", "V_ENUM", "restart must be always | on-failure | never")
		}
		if img.Kind == "job" && c.Restart == "always" {
			is.err(at+".restart", "V_JOB_RESTART", "a job must use restart never or on-failure")
		}
		validateCommand(c.Command, at+".command", is)
		validateCommand(c.Entrypoint, at+".entrypoint", is)
		r := c.Resources
		if r.Cpus != nil && (*r.Cpus < 0.1 || *r.Cpus > 64) {
			is.err(at+".resources.cpus", "V_RANGE", "cpus must be 0.1–64 or null")
		}
		if r.MemoryMb != nil && (!isIntF(*r.MemoryMb) || *r.MemoryMb < 64 || *r.MemoryMb > 1048576) {
			is.err(at+".resources.memoryMb", "V_RANGE", "memoryMb must be an integer 64–1048576 or null")
		}
		imgPortNames := []string{}
		for _, x := range img.Ports {
			imgPortNames = append(imgPortNames, x.Name)
		}
		if c.Healthcheck != nil {
			ValidateHealthCheck(c.Healthcheck, imgPortNames, is, at+".healthcheck")
		}
		// env
		if len(c.Env) > Limits.EnvPerContainer {
			is.err(at+".env", "V_TOO_MANY", fmt.Sprintf("at most %d", Limits.EnvPerContainer))
		}
		var en []string
		for _, e := range c.Env {
			en = append(en, e.Name)
		}
		for _, d := range dupes(en) {
			is.err(at+".env["+d+"]", "V_DUPLICATE", fmt.Sprintf("env %s is set twice", d))
		}
		ectx := ContainerExprContext(cfg, services, ctx.Images, ctx.DeploymentID, ctx.SecretsSet, site.SiteSlug, c, g.siteMap)
		imgEnv := NormalizeImageEnvs(img.Env)
		cr := ResolveContainerConfig(cfg, c, imgEnv, img.Volumes, ectx)
		findImgEnv := func(name string) *model.ImageEnv {
			for i := range imgEnv {
				if imgEnv[i].Name == name {
					return &imgEnv[i]
				}
			}
			return nil
		}
		for _, e := range c.Env {
			ep := at + ".env[" + e.Name + "]"
			if !RE.EnvName.MatchString(e.Name) {
				is.err(ep+".name", "V_FORMAT", fmt.Sprintf(`"%s" does not match ^[A-Z_][A-Z0-9_]*$`, e.Name))
			}
			hv, hs := e.Value != nil, e.SecretRef != nil
			if hv == hs {
				w := "neither"
				if hv {
					w = "both"
				}
				is.err(ep, "V_ENV_ONE_OF", fmt.Sprintf("needs exactly one of value / secretRef (it has %s)", w))
			}
			if hs && !contains(g.secretSlugs, *e.SecretRef) {
				is.err(ep+".secretRef", "V_REF", fmt.Sprintf("secret %s does not exist", *e.SecretRef))
			}
			ie := findImgEnv(e.Name)
			if hv {
				v := *e.Value
				if utf16Len(v) > Limits.Value {
					is.err(ep+".value", "V_LENGTH", fmt.Sprintf("at most %d characters", Limits.Value))
				}
				if se := expr.CheckExpr(v, expr.FieldEnv); len(se) > 0 {
					for _, x := range se {
						is.err(ep+".value", x.Code, fmt.Sprintf("%s (at %d)", x.Message, x.Pos))
					}
				} else if r, ok := cr.Resolution.Env[e.Name]; ok {
					if r.Err != nil {
						is.Err(ep+".value", r.Err.Code, fmt.Sprintf("%s (at %d)", r.Err.Message, r.Err.Pos), true)
					} else if ie != nil && ie.OptionList() != nil && !r.Result.Secret && !r.Result.Deferred && !contains(ie.OptionList(), r.Result.Value) {
						is.Err(ep+".value", "V_OPTION", fmt.Sprintf(`"%s" is not one of the allowed values of %s: %s`, r.Result.Value, e.Name, strings.Join(ie.OptionList(), ", ")), true)
					}
				}
			}
			if ie != nil && ie.Secret && hv && !secretExprRe.MatchString(*e.Value) {
				is.Err(ep, "V_ENV_SECRET", fmt.Sprintf("image env %s is secret: use secretRef or a secret.* expression", e.Name), true)
			}
		}
		for _, ie := range imgEnv {
			given := false
			for _, x := range c.Env {
				if x.Name == ie.Name {
					given = true
				}
			}
			if !given && ie.Required && ie.Value == nil {
				is.Err(at+".env", "V_ENV_REQUIRED", fmt.Sprintf("image env %s is required (no default)", ie.Name), true)
			}
			if !given && ie.Value != nil && ie.Value.Str != nil {
				if r, ok := cr.Resolution.Env[ie.Name]; ok && r.Err != nil {
					is.Err(at+".env["+ie.Name+"]", r.Err.Code, "image default: "+r.Err.Message, true)
				}
			}
		}
		// volumes: one row per image volume (missing = not mounted, warning) + extra mounts; mount paths may hold {{ }}
		if len(c.Volumes) > Limits.VolumesPerContainer {
			is.err(at+".volumes", "V_TOO_MANY", fmt.Sprintf("at most %d", Limits.VolumesPerContainer))
		}
		var ivn []string
		for _, v := range c.Volumes {
			if v.ImageVolume != nil && *v.ImageVolume != "" {
				ivn = append(ivn, *v.ImageVolume)
			}
		}
		for _, d := range dupes(ivn) {
			is.err(at+".volumes["+d+"]", "V_DUPLICATE", fmt.Sprintf("image volume %s is mapped twice", d))
		}
		for _, iv := range img.Volumes {
			if !contains(ivn, iv.Name) {
				is.Warn(at+".volumes["+iv.Name+"]", "V_VOLUME_NOT_MOUNTED", fmt.Sprintf(`image volume %s (%s) is not mounted: its data is lost when the container is recreated. Pick a data volume, or choose "Not mounted"`, iv.Name, iv.MountPath))
			}
		}
		for i, v := range c.Volumes {
			vp := fmt.Sprintf("%s.volumes[%d]", at, i)
			if v.ImageVolume != nil {
				vp = at + ".volumes[" + *v.ImageVolume + "]"
				found := false
				for _, x := range img.Volumes {
					if x.Name == *v.ImageVolume {
						found = true
					}
				}
				if !found {
					is.err(vp+".imageVolume", "V_REF", fmt.Sprintf("image %s:%s has no volume %s", slug, c.Version, *v.ImageVolume))
				}
			} else {
				if v.MountPath == nil || *v.MountPath == "" {
					is.err(vp+".mountPath", "V_REQUIRED", "an extra mount needs a mount path")
				}
				if v.Source.Kind == "none" {
					is.err(vp+".source.kind", "V_VOLUME", "an extra mount needs a source (volume, host, file or secret)")
				}
			}
			if v.MountPath != nil {
				checkMountPath(*v.MountPath, vp+".mountPath", is)
			}
			src := v.Source
			if (src.Kind == "file" || src.Kind == "secret") && !v.ReadOnly {
				is.err(vp+".readOnly", "V_READONLY", fmt.Sprintf("a %s mount is always read-only (readOnly must be true)", src.Kind))
			}
			switch src.Kind {
			case "volume":
				if !RE.VolumeSlug.MatchString(src.VolumeSlug) {
					is.err(vp+".source.volumeSlug", "V_FORMAT", fmt.Sprintf(`volume name "%s" must match ^[a-z0-9][a-z0-9_.-]{0,99}$`, src.VolumeSlug))
				} else if sc.Volumes != nil && !declared[src.VolumeSlug] {
					is.err(vp+".source.volumeSlug", "V_VOLUME_UNDECLARED", fmt.Sprintf("volume %s is not declared on site %s: add it under the site's Volumes", src.VolumeSlug, site.SiteSlug))
				} else {
					// kind matrix (§21 #83): only at the image's own path (an image volume row without its own mount path)
					svKind := "dir"
					if sv := sc.FindVolume(src.VolumeSlug); sv != nil && sv.VolumeKind() == "file" {
						svKind = "file"
					}
					var iv *model.ImageVolume
					if v.ImageVolume != nil {
						for k := range img.Volumes {
							if img.Volumes[k].Name == *v.ImageVolume {
								iv = &img.Volumes[k]
							}
						}
					}
					atImagePath := iv != nil && v.MountPath == nil
					ivKind := ""
					if iv != nil {
						ivKind = ImageVolumeKind(*iv)
					}
					if svKind == "file" {
						mp := ""
						if v.MountPath != nil {
							mp = *v.MountPath
						} else if iv != nil {
							mp = iv.MountPath
						}
						if mp == "" || strings.HasSuffix(mp, "/") {
							is.err(vp+".mountPath", "V_VOLUME_KIND", fmt.Sprintf("volume %s is a single file: mount it at a file path (e.g. /data/flows.json), not a folder", src.VolumeSlug))
						} else if atImagePath && ivKind == "dir" {
							is.Warn(vp+".source.volumeSlug", "V_VOLUME_KIND_IMAGE", fmt.Sprintf("image volume %s (%s) is a Folder in image %s:%s; the single-file volume %s replaces the whole folder with one file — pick a folder volume here, or set image volume %s to File on the image page if %s is a file", iv.Name, iv.MountPath, slug, c.Version, src.VolumeSlug, iv.Name, iv.MountPath))
						}
						if !v.ReadOnly {
							is.Info(vp+".source.volumeSlug", "V_FILE_VOLUME_RENAME", fmt.Sprintf("%s is a single-file volume mounted read-write: %s", src.VolumeSlug, FileVolumeRenameInfo))
						}
					} else if atImagePath && ivKind == "file" {
						is.err(vp+".source.volumeSlug", "V_VOLUME_KIND_MISMATCH", fmt.Sprintf("folder volume %s cannot be mounted on file path %s (image volume %s is a File) — use a single-file volume, or change the image volume to Folder on the image page", src.VolumeSlug, iv.MountPath, iv.Name))
					}
				}
			case "host":
				if expr.IsExpr(src.Path) || !IsSafeMountPath(src.Path) {
					is.err(vp+".source.path", "V_VOLUME", `host path must be an absolute path, not "/", without spaces, ".." or {{ }}`)
				}
			case "file":
				var f *model.FileDef
				for k := range cfg.Files {
					if cfg.Files[k].FileSlug == src.FileSlug {
						f = &cfg.Files[k]
					}
				}
				if f == nil {
					is.err(vp+".source.fileSlug", "V_REF", fmt.Sprintf("file %s does not exist", src.FileSlug))
				} else {
					if f.Content == nil {
						is.Err(vp+".source.fileSlug", "V_FILE_CONTENT", fmt.Sprintf("file %s has no content", src.FileSlug), true)
					}
					if c.Enabled && f.RenderMode() == "expressions" && !contains(renders, f.FileSlug) {
						renders = append(renders, f.FileSlug)
					}
				}
			case "secret":
				if !contains(g.secretSlugs, src.SecretSlug) {
					is.err(vp+".source.secretSlug", "V_REF", fmt.Sprintf("secret %s does not exist", src.SecretSlug))
				}
			case "none":
			default:
				if src.Kind == "data" {
					is.err(vp+".source.kind", "V_ENUM", `source kind "data" was replaced by declared site volumes ({ kind: "volume", volumeSlug })`)
				} else {
					is.err(vp+".source.kind", "V_ENUM", "source.kind must be volume | host | file | secret | none")
				}
			}
		}
		// effective mounts: resolved paths unique
		type mountUse struct {
			what string
			expr bool
		}
		mounts := map[string]mountUse{}
		for _, m := range ContainerMounts(c, img.Volumes) {
			var vp string
			if m.ImageVolume != nil {
				vp = at + ".volumes[" + *m.ImageVolume + "].mountPath"
			} else {
				vp = at + ".volumes[" + m.Key[7:len(m.Key)-1] + "].mountPath"
			}
			rr, has := cr.Resolution.Mounts[m.Key]
			isx := expr.IsExpr(m.MountPath)
			if isx && strings.HasPrefix(m.MountPath, "/") && len(expr.CheckExpr(m.MountPath, expr.FieldMountPath)) == 0 && has {
				if rr.Err != nil {
					is.Err(vp, rr.Err.Code, fmt.Sprintf("%s (at %d)", rr.Err.Message, rr.Err.Pos), true)
				} else if !rr.Result.Deferred && !IsSafeMountPath(rr.Result.Value) {
					is.Err(vp, "V_PATH", fmt.Sprintf(`resolves to "%s": a mount path must be absolute, not "/", without spaces or ".."`, rr.Result.Value), true)
				}
			}
			eff := m.MountPath
			if has && rr.Err == nil {
				eff = rr.Result.Value
			}
			what := m.Key
			if m.ImageVolume != nil {
				what = "image volume " + *m.ImageVolume
			}
			if prev, ok := mounts[eff]; ok {
				is.Err(vp, "V_MOUNT", fmt.Sprintf("mount path %s is also used by %s", eff, prev.what), prev.expr || isx)
			} else {
				mounts[eff] = mountUse{what, isx}
			}
		}
		checkFileInWritableVolume(ContainerMounts(c, img.Volumes), cr.Resolution.Mounts, at, is) // §21 #101 (volcontent.go)
		// ports: one row per image port (missing = not published, edge on)
		hostPorts := []int{}
		var pn []string
		for _, p := range c.Ports {
			pn = append(pn, p.PortName)
		}
		for _, d := range dupes(pn) {
			is.err(at+".ports["+d+"]", "V_DUPLICATE", fmt.Sprintf("port %s is listed twice", d))
		}
		for _, p := range c.Ports {
			pp := at + ".ports[" + p.PortName + "]"
			var ip *model.ImagePort
			for k := range img.Ports {
				if img.Ports[k].Name == p.PortName {
					ip = &img.Ports[k]
				}
			}
			if ip == nil {
				is.err(pp+".portName", "V_REF", fmt.Sprintf("image %s:%s has no port %s", slug, c.Version, p.PortName))
			}
			if p.HostIP != "0.0.0.0" && p.HostIP != "127.0.0.1" {
				is.err(pp+".hostIp", "V_ENUM", "hostIp must be 0.0.0.0 | 127.0.0.1")
			}
			if p.HostPort.IsNull() {
				continue
			}
			var n *int
			if p.HostPort.Num != nil {
				checkPortF(*p.HostPort.Num, pp+".hostPort", is, 1)
				if isIntF(*p.HostPort.Num) {
					x := int(*p.HostPort.Num)
					n = &x
				}
			} else if expr.IsExpr(*p.HostPort.Expr) {
				if se := expr.CheckExpr(*p.HostPort.Expr, expr.FieldHostPort); len(se) > 0 {
					for _, x := range se {
						is.err(pp+".hostPort", x.Code, x.Message)
					}
				} else if r, err := expr.ResolveNumberField(*p.HostPort.Expr, baseCtx(ctx.DeploymentID, site.SiteSlug, nil, vars), expr.FieldHostPort); err != nil {
					x := asExprErr(err)
					is.Err(pp+".hostPort", x.Code, x.Message, true)
				} else {
					n = r
				}
			} else {
				is.err(pp+".hostPort", "V_TYPE", "hostPort must be null, a number, or one whole {{ }} block")
			}
			if n == nil {
				continue
			}
			isEdge := *n == 80 || *n == 443
			for _, e := range edgePorts {
				if e == *n {
					isEdge = true
				}
			}
			if isEdge {
				is.err(pp+".hostPort", "V_PORT_EDGE", fmt.Sprintf("host port %d belongs to the edge; add a route instead", *n))
			}
			hostPorts = append(hostPorts, *n)
			if c.Enabled {
				proto := "tcp"
				if ip != nil {
					proto = ip.Protocol
				}
				listeners = append(listeners, listener{*n, proto, c.ContainerSlug + "." + p.PortName, pp + ".hostPort"})
			}
		}
		// depends (containers of this site)
		for _, d := range c.Depends {
			if d.ContainerSlug == c.ContainerSlug {
				is.err(at+".depends", "V_CYCLE", "a container cannot depend on itself")
			} else if !contains(slugs, d.ContainerSlug) {
				is.err(at+".depends", "V_REF", fmt.Sprintf("container %s does not exist on this site", d.ContainerSlug))
			}
		}
		edgeOpen := []string{}
		for _, p := range ContainerPorts(c, img.Ports) {
			if p.Edge {
				edgeOpen = append(edgeOpen, p.PortName)
			}
		}
		containerInfo[c.ContainerSlug] = EdgeContainerInfo{Enabled: c.Enabled, HostPorts: hostPorts, PortNames: imgPortNames, EdgePorts: edgeOpen, Slug: c.ContainerSlug}
	}
	if _, cyc := ContainerOrder(sc, services); cyc != nil {
		is.err(cb+".containers", "V_CYCLE", "container depends form a cycle: "+strings.Join(cyc, " → "))
	} else {
		// §21 #93: implicit (containers.<slug>.*) + suggested (literal hostname) depends; a reference cycle → declared order
		inf := InferDepends(DependsInput{Site: sc, Images: ctx.Images, Files: cfg.Files, FileContents: ctx.FileContents})
		for _, d := range inf {
			for _, sg := range d.Suggested {
				is.Info(cb+".containers["+d.ContainerSlug+"].depends", "V_DEPENDS_SUGGESTED", d.ContainerSlug+" mentions `"+sg.ContainerSlug+"` ("+strings.Join(sg.From, ", ")+") — add it to Depends on?")
			}
		}
		if _, cyc := StartOrder(sc, inf); len(cyc) > 0 {
			is.Warn(cb+".containers", "V_START_ORDER_CYCLE", "containers "+strings.Join(cyc, ", ")+" reference each other (expressions or hostnames): they start in the declared order")
		}
	}
	// §21 #88: containers.<slug>.* of a DISABLED container still resolves, but nothing answers on that name
	disabledRef := func(src, path string) {
		if !expr.IsExpr(src) {
			return
		}
		refs, err := expr.ExprRefs(src)
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, r := range refs {
			parts := strings.Split(r, ".")
			if len(parts) < 3 || parts[0] != "containers" || seen[parts[1]] {
				continue
			}
			seen[parts[1]] = true
			if ci, ok := containerInfo[parts[1]]; ok && !ci.Enabled {
				is.Warn(path, "V_CONTAINER_REF_DISABLED", fmt.Sprintf("container %s is disabled: %s resolves, but nothing answers on it", parts[1], r))
			}
		}
	}
	for i := range sc.Containers {
		c := &sc.Containers[i]
		if !c.Enabled {
			continue
		}
		at := cb + ".containers[" + c.ContainerSlug + "]"
		given := map[string]bool{}
		for _, e := range c.Env {
			given[e.Name] = true
			if e.Value != nil {
				disabledRef(*e.Value, at+".env["+e.Name+"].value")
			}
		}
		if img, ok := ctx.Images[ImageKey(c.ServiceSlug, c.Version)]; ok {
			for _, raw := range img.Env {
				ie := NormalizeImageEnv(raw)
				if !given[ie.Name] && ie.Value != nil && ie.Value.Str != nil {
					disabledRef(*ie.Value.Str, at+".env["+ie.Name+"]")
				}
			}
		}
	}
	if ctx.FileContents != nil {
		for _, slug := range renders {
			if content, ok := ctx.FileContents[slug]; ok {
				disabledRef(content, "files["+slug+"]")
			}
		}
	}
	// rendered files (only when the contents are known: agent staging, cloud preview)
	if ctx.FileContents != nil && len(renders) > 0 {
		fctx := SiteFileExprContext(cfg, services, ctx.Images, ctx.DeploymentID, ctx.SecretsSet, site.SiteSlug, g.siteMap)
		where := "site " + site.SiteSlug
		for _, slug := range renders {
			content, ok := ctx.FileContents[slug]
			if !ok {
				continue
			}
			var f *model.FileDef
			for k := range cfg.Files {
				if cfg.Files[k].FileSlug == slug {
					f = &cfg.Files[k]
				}
			}
			out := RenderSiteFile(content, fctx)
			if out.Err != nil && out.Err.Code == expr.EDeferred {
				continue
			}
			if out.Err != nil {
				is.Err("files["+slug+"]", "V_FILE_RENDER", fmt.Sprintf("%s:%d:%d: %s %s (%s)", slug, out.Err.Line, out.Err.Col, out.Err.Code, out.Err.Message, where), true)
			} else if f != nil && IsJSONMime(f.Mime) {
				if je := JSONError(out.Value); je != "" && !expr.IsDeferredValue(out.Value) {
					is.Err("files["+slug+"]", "V_FILE_JSON", fmt.Sprintf("%s: the rendered file is not valid JSON for %s: %s", slug, where, je), true)
				}
			}
		}
	}
	checkContentRenders(cfg, ctx, site.SiteSlug, g.siteMap, contentRenders, disabledRef, is) // §21 #101 (volcontent.go)
	// edge
	ValidateEdge(&sc.Edge, EdgeOptions{Base: cb + ".edge", AgentPort: sc.AgentPort, AgentGuiAliasPort: sc.AgentGuiAliasPort, Containers: containerInfo, FileSlugs: g.fileSlugs, SecretSlugs: g.secretSlugs}, is)
	for i, p := range edgePorts {
		w := "edge.httpPort"
		if i == 1 {
			w = "edge.httpsPort"
		}
		listeners = append(listeners, listener{p, "tcp", w, cb + ".edge"})
	}
	ex := EdgeExprContext(cfg, ctx.DeploymentID, site.SiteSlug, g.hosts)
	tryEdge := func(v, path string) {
		if !expr.IsExpr(v) || len(expr.CheckExpr(v, expr.FieldEdge)) > 0 {
			return
		}
		if _, err := expr.Resolve(v, ex, expr.FieldEdge); err != nil {
			x := asExprErr(err)
			is.Err(path, x.Code, x.Message, true)
		}
	}
	for _, h := range sc.Edge.Hosts {
		tryEdge(h.Hostname, cb+".edge.hosts["+h.Hostname+"].hostname")
	}
	for _, r := range sc.Edge.Routes {
		if r.Action.Type == "redirect" {
			tryEdge(r.Action.To, cb+".edge.routes["+r.RouteID+"].action.to")
		}
		if r.Action.Type == "proxy" {
			for _, kv := range r.Action.SetHeaders {
				tryEdge(kv.Value, cb+".edge.routes["+r.RouteID+"].action.setHeaders."+kv.Name)
			}
			if r.Action.Rewrite != nil {
				tryEdge(r.Action.Rewrite.Replacement, cb+".edge.routes["+r.RouteID+"].action.rewrite.replacement")
			}
		}
		if r.Action.Type == "rewrite" {
			tryEdge(r.Action.Replacement, cb+".edge.routes["+r.RouteID+"].action.replacement")
		}
		if r.ResponseHeaders != nil {
			for _, kv := range r.ResponseHeaders.Set {
				tryEdge(kv.Value, cb+".edge.routes["+r.RouteID+"].responseHeaders.set."+kv.Name)
			}
			for _, kv := range r.ResponseHeaders.Add {
				tryEdge(kv.Value, cb+".edge.routes["+r.RouteID+"].responseHeaders.add."+kv.Name)
			}
		}
	}
	for _, h := range sc.Edge.Hosts {
		for _, kv := range h.ResponseHeaders.Set {
			tryEdge(kv.Value, cb+".edge.hosts["+h.Hostname+"].responseHeaders.set."+kv.Name)
		}
		for _, kv := range h.ResponseHeaders.Add {
			tryEdge(kv.Value, cb+".edge.hosts["+h.Hostname+"].responseHeaders.add."+kv.Name)
		}
	}
	seen := map[string]string{}
	for _, l := range listeners {
		k := fmt.Sprintf("%s/%d", l.proto, l.port)
		prev, ok := seen[k]
		if ok && !(strings.HasPrefix(l.what, "edge.") && strings.HasPrefix(prev, "edge.")) {
			is.err(l.path, "V_PORT_CONFLICT", fmt.Sprintf("%d/%s is already used by %s", l.port, l.proto, prev))
		} else {
			seen[k] = l.what
		}
	}
	ValidateSiteDevTools(site, cfg, ctx.Images, is) // §21 #97 b dev-tool presets
}
