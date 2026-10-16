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

// Service / volume names of the helpers (a container may not take them: then they get a suffix).
const (
	InitService      = "init"
	EdgeService      = "edge"
	PortainerService = "portainer"
	ToolsVolume      = "tools"
	EdgeVolume       = "edge-data"
	PortainerVolume  = "portainer-data"
	// ToolsDir is where the tools volume (the static busybox of init.sh) is mounted in a container with a health check.
	ToolsDir = "/.tools"
)

func helperName(base string, taken map[string]bool) string {
	n := base
	for i := 2; taken[n]; i++ {
		n = base + "-" + strconv.Itoa(i)
	}
	return n
}

// shq quotes s for sh (single quotes).
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// healthTest maps an OPM health check to a compose healthcheck run with the static busybox of the tools volume (nil =
// none: the image HEALTHCHECK applies, like on the agent) and whether the tools volume is needed.
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
	t := strconv.Itoa(int(timeout + 0.999))
	bb := ToolsDir + "/busybox"
	// the container's own address (the agent checks from outside the container's loopback)
	addr := "$(" + bb + " hostname -i | " + bb + " cut -d' ' -f1)"
	var test yflow
	switch h.Type {
	case "http":
		want := int(h.ExpectStatus)
		if want == 0 {
			want = 200
		}
		path := h.Path
		if path == "" {
			path = "/"
		}
		// busybox wget -S prints the status line ("  HTTP/1.1 200 OK") even for 4xx / 5xx
		script := "s=$(" + bb + " wget -S -q -T " + t + " -O /dev/null " + shq("http://") + "\"" + addr + "\"" + shq(":"+strconv.Itoa(port(h.PortName))+path) +
			" 2>&1 | " + bb + " awk '/^ *HTTP\\//{print $2; exit}'); [ \"$s\" = " + strconv.Itoa(want) + " ]"
		test = yflow{"CMD", bb, "sh", "-c", script}
	case "tcp":
		test = yflow{"CMD", bb, "sh", "-c", bb + " nc -z -w " + t + " \"" + addr + "\" " + strconv.Itoa(port(h.PortName))}
	case "exec":
		if int(h.ExpectExitCode) == 0 {
			test = append(yflow{"CMD"}, h.Command...)
		} else {
			test = append(yflow{"CMD", bb, "sh", "-c", "\"$@\"; [ $? -eq " + strconv.Itoa(int(h.ExpectExitCode)) + " ]", "sh"}, h.Command...)
		}
	case "log":
		return nil, false, fmt.Sprintf("container %s: the health check waits for the log pattern %q — compose has no log health check: dependents only wait for the start", b.ContainerSlug, h.Pattern)
	default:
		return nil, false, ""
	}
	tools := test[1] == bb
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
	return m, tools, ""
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
	return sanitizeProject(x.req.Branch.EntitySlug + "-" + site)
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

// helpers are the helper service / volume names of a site (computed once, used by compose and the scripts).
type helpers struct {
	init, edge, portainer   string
	tools, edgeVol, portVol string
}

func (x *exporter) helpersOf(st *siteState, sc *model.Site) helpers {
	taken := map[string]bool{}
	for _, b := range st.a.builts {
		if b != nil {
			taken[b.ContainerSlug] = true
		}
	}
	var h helpers
	h.init = helperName(InitService, taken)
	taken[h.init] = true
	h.edge = helperName(EdgeService, taken)
	taken[h.edge] = true
	h.portainer = helperName(PortainerService, taken)
	volTaken := map[string]bool{}
	for _, v := range sc.Config.VolumeList() {
		volTaken[v.VolumeSlug] = true
	}
	h.tools = helperName(ToolsVolume, volTaken)
	volTaken[h.tools] = true
	h.edgeVol = helperName(EdgeVolume, volTaken)
	volTaken[h.edgeVol] = true
	h.portVol = helperName(PortainerVolume, volTaken)
	return h
}

// compose renders sites/<site>/docker-compose.yml.
func (x *exporter) compose(st *siteState, sc *model.Site) string {
	site := st.site
	h := x.helpersOf(st, sc)

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
			x.note(site, "IMAGE_UNKNOWN", fmt.Sprintf("image %s has no registry reference: compose uses %s (it must exist on the machine)", key, pull))
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
		flow := func(xs, ys []string, what string) yflow {
			out := yflow{}
			for i := range xs {
				y := ""
				if i < len(ys) {
					y = ys[i]
				}
				v, ok := x.sub(st, xs[i], y)
				if !ok {
					x.note(site, "VALUE_DERIVED", fmt.Sprintf("container %s: %s argument %d depends on a machine value in a way the export cannot express", slug, what, i+1))
				}
				out = append(out, v)
			}
			return out
		}
		if entry := flow(a.Spec.Entrypoint, b.Spec.Entrypoint, "entrypoint"); len(entry) > 0 {
			svc.set("entrypoint", entry)
		}
		if len(a.Spec.Cmd) > 0 {
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
		// environment (values derived from secrets through filters come from .env like every other machine value)
		ea, eb := envMap(a.Spec.Env), envMap(b.Spec.Env)
		env := newMap()
		for _, k := range sortedKeys(ea) {
			v, ok := x.sub(st, ea[k], eb[k])
			if !ok {
				x.note(site, "VALUE_DERIVED", fmt.Sprintf("container %s: env %s depends on a machine value in a way the export cannot express", slug, k))
			}
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
			ports = append(ports, fmt.Sprintf("%s:%d:%d/%s", p.HostIP, p.HostPort, p.ContainerPort, p.Protocol))
		}
		if len(ports) > 0 {
			svc.set("ports", ports)
		}
		// mounts
		var vols yseq
		var mounted []string // the files of .runtime/ this service mounts (their hash recreates it on a change)
		for i, m := range a.Spec.Mounts {
			tb := m.Target
			if i < len(b.Spec.Mounts) {
				tb = b.Spec.Mounts[i].Target
			}
			target, ok := x.sub(st, m.Target, tb)
			if !ok {
				x.note(site, "VALUE_DERIVED", fmt.Sprintf("container %s: mount path %s depends on a machine value in a way the export cannot express", slug, x.clean(m.Target)))
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
				vols = append(vols, bindMount("./.runtime/files/"+o.Slug, target, true))
				mounted = append(mounted, ".runtime/files/"+o.Slug)
			case "secret":
				vols = append(vols, bindMount("./.runtime/secrets/"+o.Slug, target, true))
				mounted = append(mounted, ".runtime/secrets/"+o.Slug)
			}
		}
		hc, tools, msg := healthTest(a)
		if msg != "" {
			x.note(site, "HEALTH_LOG", msg)
		}
		if tools {
			vols = append(vols, volMount(h.tools, ToolsDir, true))
			namedVols.set(h.tools, newMap())
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
		// the image's own labels stay; OPM's bookkeeping labels (opm.*) are not set: no agent manages these containers
		labels := newMap()
		for _, k := range sortedKeys(a.Spec.Labels) {
			if !strings.HasPrefix(k, "opm.") {
				labels.set(k, a.Spec.Labels[k])
			}
		}
		if len(mounted) > 0 {
			n := filesShaName(slug)
			st.hashes = append(st.hashes, fileHash{Name: n, Paths: mounted})
			labels.set("files.sha256", "${"+n+":-}")
		}
		if !labels.empty() {
			svc.set("labels", labels)
		}
		svc.set("networks", newMap().set("site", newMap().set("aliases", yflow(a.Aliases()))))
		deps := newMap().set(h.init, newMap().set("condition", "service_completed_successfully"))
		for _, w := range waits[slug] {
			if wb := byslug[w]; wb != nil && wb.Enabled {
				deps.set(w, newMap().set("condition", waitCondition(wb)))
			}
		}
		svc.set("depends_on", deps)
		services.set(slug, svc)
	}

	// init: seeds the volumes once and installs the static busybox for the health checks (init.sh)
	initSvc := newMap().set("image", InitImage)
	initSvc.set("entrypoint", yflow{"sh", "/site/init.sh"})
	initSvc.set("network_mode", "none")
	iv := yseq{bindMount(".", "/site", true), bindMount("./.runtime", "/runtime", true), bindMount("./.data", "/data", false)}
	for _, v := range sc.Config.VolumeList() {
		v := v
		vb := volumeBindOf(&v)
		dirT, fileT := initVolumeTarget(&v)
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
	iv = append(iv, volMount(h.tools, "/tools", false))
	namedVols.set(h.tools, newMap())
	initSvc.set("volumes", iv)
	initSvc.set("restart", "no")
	root := newMap().set("name", x.projectName(site))
	all := newMap().set(h.init, initSvc)
	for _, k := range services.keys {
		all.set(k, services.vals[k])
	}
	// edge: the agent's edge on the official caddy image
	if st.a.edge != nil {
		e := newMap()
		if dnsDockerfile(sc) != "" {
			e.set("build", newMap().set("context", "./edge"))
			e.set("image", sanitizeProject(x.req.Branch.EntitySlug)+"-"+site+"-edge:dns")
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
			src = "./.runtime/edge/caddy.json"
		}
		ev := yseq{bindMount(src, "/etc/caddy/caddy.json", true), volMount(h.edgeVol, "/data", false)}
		paths := []string{strings.TrimPrefix(src, "./")}
		if len(st.a.tls) > 0 {
			ev = append(ev, bindMount("./.runtime/tls", EdgeTLSDir, true))
			for _, host := range sortedKeys(st.a.tls) {
				paths = append(paths, ".runtime/tls/"+host+".crt", ".runtime/tls/"+host+".key")
			}
		}
		e.set("volumes", ev)
		// a new config (git merge) or certificate recreates the edge, like the agent reloads it
		n := filesShaName(h.edge)
		st.hashes = append(st.hashes, fileHash{Name: n, Paths: paths})
		e.set("labels", newMap().set("files.sha256", "${"+n+":-}"))
		if usesEnv(st.a.edgeJSON) {
			e.set("env_file", yseq{newMap().set("path", ".env").set("required", yraw("false"))})
		}
		e.set("restart", "always")
		e.set("networks", newMap().set("site", newMap()))
		e.set("depends_on", newMap().set(h.init, newMap().set("condition", "service_completed_successfully")))
		all.set(h.edge, e)
		namedVols.set(h.edgeVol, newMap())
	}
	// portainer: the Docker GUI of this machine (profile "portainer"; deploy.sh enables it unless PORTAINER=off)
	p := newMap().set("image", PortainerImage)
	p.set("profiles", yflow{"portainer"})
	p.set("command", yflow{"--admin-password-file", "/run/portainer-admin-password"})
	p.set("ports", yseq{"${PORTAINER_HTTPS_PORT:-9443}:9443"})
	p.set("volumes", yseq{bindMount("/var/run/docker.sock", "/var/run/docker.sock", false), volMount(h.portVol, "/data", false),
		bindMount("./.runtime/portainer-admin-password", "/run/portainer-admin-password", true)})
	p.set("restart", "always")
	all.set(h.portainer, p)
	namedVols.set(h.portVol, newMap())

	root.set("services", all)
	root.set("networks", newMap().set("site", newMap().set("name", driver.NetworkName(x.req.DeploymentID, site))))
	// named volumes sorted
	vm := newMap()
	ks := append([]string(nil), namedVols.keys...)
	sort.Strings(ks)
	for _, k := range ks {
		vm.set(k, newMap())
	}
	root.set("volumes", vm)
	header := []string{
		"Site " + site + " of " + x.req.Branch.GitBranch + " @ " + x.req.Commit.ID + " (generated: change the config in the cloud, or edit and commit on this machine's site branch).",
		"Start it with ./deploy.sh (it renders .env and .runtime/ from site.env, then runs docker compose up -d); see README.md.",
		"Services: the site's containers (service = container_name = network alias), init (seeds empty volumes once, installs the",
		"health-check tool), edge (Caddy: the routes and TLS of the site) and portainer (the Docker GUI, https://<machine>:9443).",
	}
	return yamlDoc(header, root)
}

// usesEnv: the edge config reads values from the environment ({env.NAME}: DNS provider tokens).
func usesEnv(edgeJSON string) bool { return strings.Contains(edgeJSON, "{env.") }
