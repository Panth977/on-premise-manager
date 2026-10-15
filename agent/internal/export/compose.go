package export

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// Service names of the helpers (a container may not take them: then they get a suffix).
const (
	InitService = "opm-init"
	EdgeService = "opm-edge"
	ToolsVolume = "opm-tools"
	EdgeVolume  = "opm-edge-data"
)

func helperName(base string, taken map[string]bool) string {
	n := base
	for i := 2; taken[n]; i++ {
		n = base + "-" + strconv.Itoa(i)
	}
	return n
}

// healthTest maps an OPM health check to a compose healthcheck (nil = none: the image HEALTHCHECK applies, like on the
// agent) and whether the probe tool is needed.
func healthTest(b *driver.Built) (*ymap, bool, string) {
	h := b.Health
	if h == nil || h.Type == "" || h.Type == "none" {
		return nil, false, ""
	}
	port := func(name string) int {
		for _, p := range b.Ports {
			if p.Name == name {
				return p.ContainerPort
			}
		}
		return 0
	}
	timeout := h.TimeoutSec
	if timeout <= 0 {
		timeout = 5
	}
	t := strconv.FormatFloat(timeout, 'f', -1, 64)
	var test yflow
	tool := false
	switch h.Type {
	case "http":
		want := int(h.ExpectStatus)
		if want == 0 {
			want = 200
		}
		test = yflow{"CMD", "/.opm/opm-render", "probe", "http", "--port", strconv.Itoa(port(h.PortName)), "--path", h.Path, "--expect", strconv.Itoa(want), "--timeout", t}
		tool = true
	case "tcp":
		test = yflow{"CMD", "/.opm/opm-render", "probe", "tcp", "--port", strconv.Itoa(port(h.PortName)), "--timeout", t}
		tool = true
	case "exec":
		if int(h.ExpectExitCode) == 0 {
			test = append(yflow{"CMD"}, h.Command...)
		} else {
			test = append(yflow{"CMD", "/.opm/opm-render", "probe", "exec", "--expect", strconv.Itoa(int(h.ExpectExitCode)), "--"}, h.Command...)
			tool = true
		}
	case "log":
		return nil, false, fmt.Sprintf("container %s: the health check waits for the log pattern %q — compose has no log health check: dependents only wait for the start", b.ContainerSlug, h.Pattern)
	default:
		return nil, false, ""
	}
	interval := h.IntervalSec
	if interval <= 0 {
		interval = 1
	}
	retries := int(h.Retries)
	if retries <= 0 {
		retries = 3
	}
	for i := range test {
		test[i] = strings.ReplaceAll(test[i], "$", "$$")
	}
	m := newMap().set("test", test).set("interval", fmtSecs(interval)).set("timeout", fmtSecs(timeout)).set("retries", yraw(strconv.Itoa(retries)))
	m.set("start_period", fmtSecs(h.StartPeriodSec))
	return m, tool, ""
}

// waitCondition: what a dependent waits for (the agent's "completed": healthy / job exited 0 / running).
func waitCondition(b *driver.Built) string {
	if b.Kind == "job" {
		return "service_completed_successfully"
	}
	if h := b.Health; h != nil && (h.Type == "http" || h.Type == "tcp" || h.Type == "exec") {
		return "service_healthy"
	}
	return "service_started"
}

func bindMount(src, target string, ro bool) *ymap {
	m := newMap().set("type", "bind").set("source", src).set("target", target)
	if ro {
		m.set("read_only", yraw("true"))
	}
	return m.set("bind", newMap().set("create_host_path", yraw("true")))
}

func volMount(src, target string, ro bool) *ymap {
	m := newMap().set("type", "volume").set("source", src).set("target", target)
	if ro {
		m.set("read_only", yraw("true"))
	}
	return m.set("volume", newMap().set("nocopy", yraw("true")))
}

// ProjectName of a site's compose project.
func (x *exporter) projectName(site string) string {
	return sanitizeProject("opm-" + x.req.Branch.EntitySlug + "-" + site)
}

func sanitizeProject(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// compose renders sites/<site>/docker-compose.yml.
func (x *exporter) compose(st *siteState, sc *model.Site) string {
	site := st.site
	taken := map[string]bool{}
	for _, b := range st.a.builts {
		if b != nil {
			taken[b.ContainerSlug] = true
		}
	}
	initName := helperName(InitService, taken)
	taken[initName] = true
	edgeName := helperName(EdgeService, taken)
	taken[edgeName] = true
	toolsVol, edgeVol := ToolsVolume, EdgeVolume
	volTaken := map[string]bool{}
	for _, v := range sc.Config.VolumeList() {
		volTaken[v.VolumeSlug] = true
	}
	toolsVol = helperName(toolsVol, volTaken)
	volTaken[toolsVol] = true
	edgeVol = helperName(edgeVol, volTaken)

	byslug := map[string]*driver.Built{}
	for _, b := range st.a.builts {
		if b != nil {
			byslug[b.ContainerSlug] = b
		}
	}
	bOf := func(slug string) *driver.Built {
		for _, b := range st.b.builts {
			if b != nil && b.ContainerSlug == slug {
				return b
			}
		}
		return nil
	}
	// waits (explicit + implicit depends, like the agent's startPlan)
	contents := map[string]string{}
	for _, f := range st.a.cfg.Files {
		if f.Content != nil && f.RenderMode() == "expressions" {
			if t, ok := x.req.Texts[hexOf(f.Content.Sha256)]; ok {
				contents[f.FileSlug] = t
			}
		}
	}
	inf := validate.InferDepends(validate.DependsInput{Site: &sc.Config, Images: driver.CatalogImages(&x.req.Catalog), Files: st.a.cfg.Files, FileContents: contents})
	order, _ := validate.StartOrder(&sc.Config, inf)
	var enabled []string
	for _, c := range sc.Config.Containers {
		if c.Enabled {
			enabled = append(enabled, c.ContainerSlug)
		}
	}
	waits := map[string][]string{}
	for _, d := range inf {
		waits[d.ContainerSlug] = validate.WaitDepends(d, enabled)
	}

	services := newMap()
	namedVols := newMap()
	usedTools := false
	// containers in the agent's start order (disabled ones after it)
	seen := map[string]bool{}
	var slugs []string
	for _, s := range order {
		if byslug[s] != nil && !seen[s] {
			seen[s] = true
			slugs = append(slugs, s)
		}
	}
	for _, b := range st.a.builts {
		if b != nil && !seen[b.ContainerSlug] {
			seen[b.ContainerSlug] = true
			slugs = append(slugs, b.ContainerSlug)
		}
	}
	declared := map[string]*model.SiteVolume{}
	for i := range sc.Config.VolumeList() {
		v := (*sc.Config.Volumes)[i]
		declared[v.VolumeSlug] = &v
	}
	for _, slug := range slugs {
		a := byslug[slug]
		b := bOf(slug)
		if b == nil {
			continue
		}
		svc := newMap()
		key := a.Image.ServiceSlug + ":" + a.Image.Version
		img, ok := x.req.Images[key]
		pull := img.Pull
		if !ok || pull == "" {
			pull, _, _ = driver.ReadableRef(a.Image)
			x.note(site, "IMAGE_UNKNOWN", fmt.Sprintf("image %s has no registry reference in OPM: compose uses %s (it must exist on the machine)", key, pull))
		} else if img.Private && img.RegistryHost != nil {
			// README lists the docker login
		}
		svc.set("image", pull)
		if p := img.Platform; p != "" {
			svc.set("platform", p)
		} else if a.Image.Platform != "" {
			svc.set("platform", a.Image.Platform)
		}
		svc.set("container_name", a.Name)
		if !a.Enabled {
			svc.set("profiles", yflow{"disabled"})
		}
		// command / entrypoint (+ the derived-env wrapper)
		derived := st.derived[slug]
		flow := func(xs, ys []string, what string) yflow {
			out := yflow{}
			for i := range xs {
				y := ""
				if i < len(ys) {
					y = ys[i]
				}
				v, ok := x.sub(st, xs[i], y)
				if !ok {
					x.note(site, "VALUE_DERIVED", fmt.Sprintf("container %s: %s argument %d depends on an .env value through a filter: the first sentinel form is used", slug, what, i+1))
				}
				out = append(out, v)
			}
			return out
		}
		entry := flow(a.Spec.Entrypoint, b.Spec.Entrypoint, "entrypoint")
		if len(derived) > 0 {
			wrap := yflow{"/.opm/opm-render", "exec", "--env", "/opm-env/" + slug + ".env.json", "--"}
			entry = append(wrap, entry...)
			usedTools = true
			x.note(site, "ENV_DERIVED", fmt.Sprintf("container %s: env %s derive from .env values through filters: opm-init renders them and the entrypoint wrapper /.opm/opm-render exec sets them (docker inspect does not show them)", slug, strings.Join(derived, ", ")))
		}
		if len(entry) > 0 {
			svc.set("entrypoint", entry)
		}
		if len(a.Spec.Cmd) > 0 || (len(derived) > 0 && a.Spec.Cmd != nil) {
			svc.set("command", flow(a.Spec.Cmd, b.Spec.Cmd, "command"))
		}
		if a.Spec.Workdir != nil {
			v, _ := x.sub(st, *a.Spec.Workdir, *b.Spec.Workdir)
			svc.set("working_dir", v)
		}
		if a.Spec.User != nil {
			v, _ := x.sub(st, *a.Spec.User, *b.Spec.User)
			svc.set("user", v)
		}
		// environment
		ea, eb := envMap(a.Spec.Env), envMap(b.Spec.Env)
		env := newMap()
		dset := map[string]bool{}
		for _, k := range derived {
			dset[k] = true
		}
		for _, k := range sortedKeys(ea) {
			if dset[k] {
				continue
			}
			v, _ := x.sub(st, ea[k], eb[k])
			env.set(k, v)
		}
		if !env.empty() {
			svc.set("environment", env)
		}
		// ports: the published rows (the agent's loopback ephemeral publications are an agent detail)
		var ports yseq
		for _, p := range a.Spec.Ports {
			if p.HostPort == 0 {
				continue
			}
			ip := p.HostIP
			ports = append(ports, fmt.Sprintf("%s:%d:%d/%s", ip, p.HostPort, p.ContainerPort, p.Protocol))
		}
		if len(ports) > 0 {
			svc.set("ports", ports)
		}
		// mounts
		var vols yseq
		for i, m := range a.Spec.Mounts {
			tb := m.Target
			if i < len(b.Spec.Mounts) {
				tb = b.Spec.Mounts[i].Target
			}
			target, ok := x.sub(st, m.Target, tb)
			if !ok {
				x.note(site, "VALUE_DERIVED", fmt.Sprintf("container %s: mount path %s depends on an .env value through a filter", slug, x.clean(m.Target)))
			}
			o := m.Origin
			switch o.Kind {
			case "volume":
				v := declared[o.Slug]
				if v == nil {
					nv := model.SiteVolume{VolumeSlug: o.Slug}
					v = &nv
				}
				vb := volumeBindOf(v)
				if vb.named {
					vols = append(vols, volMount(vb.source, target, m.ReadOnly))
					namedVols.set(vb.source, newMap())
				} else {
					vols = append(vols, bindMount(vb.source, target, m.ReadOnly))
				}
			case "host":
				vols = append(vols, bindMount(o.Slug, target, m.ReadOnly))
			case "file":
				// opm-init places every File there with the agent's mode (git keeps files/<slug> or *.opm-template)
				vols = append(vols, bindMount("./.opm-runtime/files/"+o.Slug, target, true))
			case "secret":
				vols = append(vols, bindMount("./.opm-runtime/secrets/"+o.Slug, target, true))
			}
		}
		hc, tool, msg := healthTest(a)
		if msg != "" {
			x.note(site, "HEALTH_LOG", msg)
		}
		if tool || len(derived) > 0 {
			usedTools = true
			vols = append(vols, volMount(toolsVol, "/.opm", true))
		}
		if len(derived) > 0 {
			vols = append(vols, bindMount("./.opm-runtime/env/"+slug+".env.json", "/opm-env/"+slug+".env.json", true))
		}
		if len(vols) > 0 {
			svc.set("volumes", vols)
		}
		if hc != nil {
			svc.set("healthcheck", hc)
		}
		svc.set("restart", a.Spec.Restart)
		if n := a.Spec.Resources["nanoCpus"]; n > 0 {
			svc.set("cpus", yraw(strconv.FormatFloat(float64(n)/1e9, 'f', -1, 64)))
		}
		if m := a.Spec.Resources["memoryBytes"]; m > 0 {
			svc.set("mem_limit", yraw(strconv.FormatInt(m, 10)))
		}
		svc.set("stop_grace_period", fmt.Sprintf("%ds", a.Spec.StopTimeoutSec))
		if len(a.Spec.ExtraHosts) > 0 {
			svc.set("extra_hosts", yflow(a.Spec.ExtraHosts))
		}
		labels := newMap()
		for _, k := range sortedKeys(a.Spec.Labels) {
			labels.set(k, a.Spec.Labels[k])
		}
		labels.set("opm.export.commit", x.req.Commit.ID)
		svc.set("labels", labels)
		svc.set("networks", newMap().set("site", newMap().set("aliases", yflow(a.Aliases()))))
		deps := newMap().set(initName, newMap().set("condition", "service_completed_successfully"))
		for _, w := range waits[slug] {
			if wb := byslug[w]; wb != nil && wb.Enabled {
				deps.set(w, newMap().set("condition", waitCondition(wb)))
			}
		}
		svc.set("depends_on", deps)
		services.set(slug, svc)
	}

	// opm-init
	initSvc := newMap().set("image", InitImage)
	initSvc.set("entrypoint", yflow{"sh", "/opm/bin/opm-render", "init", "--site", site})
	initSvc.set("env_file", yseq{newMap().set("path", ".env").set("required", yraw("false"))})
	initSvc.set("network_mode", "none")
	iv := yseq{bindMount("../../opm", "/opm", true), bindMount(".", "/site", true), bindMount("./.opm-runtime", "/opm-runtime", false), bindMount("./.opm-data", "/opm-data", false)}
	for _, v := range sc.Config.VolumeList() {
		v := v
		vb := volumeBindOf(&v)
		dirT, fileT := InitVolumeTarget(&v)
		switch {
		case vb.named:
			iv = append(iv, volMount(vb.source, dirT, false))
			namedVols.set(vb.source, newMap())
		case !vb.fileKind:
			iv = append(iv, bindMount(vb.source, dirT, false))
		case strings.HasPrefix(vb.source, "/"):
			iv = append(iv, bindMount(parentOf(vb.source), parentOf(fileT), false))
		}
	}
	iv = append(iv, volMount(toolsVol, "/opm-tools", false))
	initSvc.set("volumes", iv)
	initSvc.set("restart", "no")
	initSvc.set("labels", newMap().set("opm.export.helper", "init").set("opm.export.commit", x.req.Commit.ID))
	root := newMap().set("name", x.projectName(site))
	all := newMap().set(initName, initSvc)
	for _, k := range services.keys {
		all.set(k, services.vals[k])
	}
	// edge
	if st.a.edge != nil {
		e := newMap()
		if dnsDockerfile(sc) != "" {
			e.set("build", newMap().set("context", "./edge"))
			e.set("image", "opm-edge-"+sanitizeProject(x.req.Branch.EntitySlug)+"-"+site+":dns")
			e.set("env_file", yseq{newMap().set("path", ".env").set("required", yraw("false"))})
		} else {
			e.set("image", EdgeImage)
		}
		e.set("command", yflow{"caddy", "run", "--config", "/etc/caddy/caddy.json"})
		hp := portOr(st.a.httpPort, 80)
		ports := yseq{fmt.Sprintf("${OPM_EDGE_HTTP_PORT:-%d}:%d", hp, hp)}
		if st.a.httpsPort != 0 {
			ports = append(ports, fmt.Sprintf("${OPM_EDGE_HTTPS_PORT:-%d}:%d", st.a.httpsPort, st.a.httpsPort))
		}
		e.set("ports", ports)
		src := "./edge/caddy.json"
		if st.edgeDef {
			src = "./.opm-runtime/edge/caddy.json"
		}
		ev := yseq{bindMount(src, "/etc/caddy/caddy.json", true), volMount(edgeVol, "/data", false)}
		if len(st.a.tls) > 0 || hasFilesTLS(sc) {
			ev = append(ev, bindMount("./.opm-runtime/tls", "/opm-runtime/tls", true))
		}
		e.set("volumes", ev)
		e.set("restart", "always")
		e.set("labels", newMap().set("opm.export.helper", "edge").set("opm.export.commit", x.req.Commit.ID))
		e.set("networks", newMap().set("site", newMap()))
		e.set("depends_on", newMap().set(initName, newMap().set("condition", "service_completed_successfully")))
		all.set(edgeName, e)
		namedVols.set(edgeVol, newMap())
	}
	root.set("services", all)
	root.set("networks", newMap().set("site", newMap().set("name", driver.NetworkName(x.req.DeploymentID, site))))
	namedVols.set(toolsVol, newMap())
	_ = usedTools
	// named volumes sorted
	vm := newMap()
	ks := append([]string(nil), namedVols.keys...)
	sort.Strings(ks)
	for _, k := range ks {
		vm.set(k, newMap())
	}
	root.set("volumes", vm)
	header := []string{
		"OPM compose export (SPEC §21 #106) — generated from " + x.req.Branch.GitBranch + " @ " + x.req.Commit.ID + "; do not edit by hand.",
		"Site " + site + ": cp .env.example .env, fill it, then `docker compose up -d` (Docker Compose v2).",
		"Services = the OPM containers (service name = container_name = network alias = containerSlug), opm-init (renders",
		".env-derived files, seeds empty volumes once, installs the probe tool) and opm-edge (the agent's edge on the official caddy image).",
	}
	return yamlDoc(header, root)
}

func hasFilesTLS(sc *model.Site) bool {
	for _, h := range sc.Config.Edge.Hosts {
		if h.TLS.Mode == "files" {
			return true
		}
	}
	return false
}
