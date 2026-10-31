package shared

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/expr"
)

// jGlobals is the `g` argument of validateSite + what it reads from the enclosing scope.
type jGlobals struct {
	config                            *doc.Map
	ctx                               *jVCtx
	is                                *Issues
	siteSlugs, fileSlugs, secretSlugs []any
	hosts                             map[string]jHost
	siteMap                           map[string]expr.SiteRef
}

type jListener struct {
	port  any
	proto any
	what  string
	path  string
}

type jContainerInfo struct {
	enabled              any
	hostPorts            []float64
	portNames, edgePorts []any
	slug                 any
}

var jReEnvSecret = regexp.MustCompile(`\{\{[^}]*(secret\.|container\.env\.)`)

func jStartsWith(v any, prefix, src string) bool {
	s, ok := v.(string)
	if !ok {
		jNotFn(v, "startsWith", src)
	}
	return strings.HasPrefix(s, prefix)
}

func (g *jGlobals) validateSite(site any) {
	is, ctx, config := g.is, g.ctx, g.config
	siteSlug := jGet(site, "siteSlug")
	ss := jStr(siteSlug)
	b := "sites[" + ss + "]"
	CheckSlug(siteSlug, b+".siteSlug", is)
	if jIsOneOf(siteSlug, RESERVED_SITE_SLUGS...) {
		is.Err(b+".siteSlug", "V_RESERVED", `"`+ss+`" is reserved (agent GUI routes)`)
	}
	CheckLabel(jGet(site, "label"), b+".label", is, 0)
	sc := jGet(site, "config")
	if !doc.Truthy(sc) || !jIsObj(sc) {
		is.Err(b+".config", "V_TYPE", "config must be an object")
		return
	}
	cb := b + ".config"
	host := jGet(sc, "host")
	hs, hostStr := host.(string)
	if !jNull(host) && !hostStr {
		is.Err(cb+".host", "V_TYPE", "host must be null (auto), an IP, a DNS name or {{ var.… }}")
	} else if hostStr && doc.JSTrim(hs) != "" {
		if doc.JSLen(hs) > LIMITS.Value {
			is.Err(cb+".host", "V_LENGTH", "at most "+strconv.Itoa(LIMITS.Value)+" characters")
		}
		if expr.IsExpr(hs) {
			jCheckExprField(hs, expr.FieldHost, cb+".host", is)
		}
		h, known := g.hosts[ss]
		syntaxOk := !expr.IsExpr(hs) || len(expr.CheckExpr(hs, expr.FieldHost)) == 0
		switch {
		case known && h.fallback != nil:
			if ctx.mode != "template" {
				is.Warn(cb+".host", "V_HOST_AUTO", *h.fallback)
			}
		case known && h.err != nil && syntaxOk:
			is.Err(cb+".host", h.err.Code, h.err.Message, "publish")
		case known && !h.auto && h.host != nil && !expr.IsDeferredValue(*h.host) && !jIsIPLiteral(*h.host) && !(RE.DNSName.MatchString(*h.host) && !strings.HasPrefix(*h.host, "*")):
			is.Err(cb+".host", "V_HOST", `"`+*h.host+`" is not an IP or a DNS name`, "publish")
		}
	}
	agentPort, aliasPort := jGet(sc, "agentPort"), jGet(sc, "agentGuiAliasPort")
	CheckPort(agentPort, cb+".agentPort", is, 1024)
	if aliasPort != nil {
		CheckPort(aliasPort, cb+".agentGuiAliasPort", is, 1024)
		if jEq(aliasPort, agentPort) {
			is.Err(cb+".agentGuiAliasPort", "V_PORT_CONFLICT", jStr(aliasPort)+" is already used by agentPort")
		}
	}
	if deps, ok := jGet(sc, "depends").([]any); !ok {
		is.Err(cb+".depends", "V_TYPE", "depends must be a list")
	} else {
		for _, d := range deps {
			ds := jGet(d, "siteSlug")
			if jEq(ds, siteSlug) {
				is.Err(cb+".depends", "V_CYCLE", "a site cannot depend on itself")
			} else if !jIncludes(g.siteSlugs, ds) {
				is.Err(cb+".depends", "V_REF", "site "+jStr(ds)+" does not exist")
			}
		}
	}
	if nf := jGet(sc, "notFoundPageId"); nf != nil && ctx.hasNotFound && !jIncludes(ctx.notFound, nf) {
		is.Err(cb+".notFoundPageId", "V_REF", "not-found page "+jStr(nf)+" does not exist")
	}
	declared := map[string]bool{}
	var seedRenders []string
	content := &jContentRenders{where: map[string][]string{}}
	volsRaw := jGet(sc, "volumes")
	volList, volsArr := volsRaw.([]any)
	if jNull(volsRaw) {
		is.Err(cb+".volumes", "V_REQUIRED", "volumes must be a list (may be empty)")
	} else if !volsArr {
		is.Err(cb+".volumes", "V_TYPE", "volumes must be a list")
	} else {
		if len(volList) > LIMITS.VolumesPerSite {
			is.Err(cb+".volumes", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.VolumesPerSite))
		}
		for _, d := range jOrderedDupes(volList, func(v any) any { return jOpt(v, "volumeSlug") }) {
			is.Err(cb+".volumes["+jStr(d)+"]", "V_DUPLICATE", "volume "+jStr(d)+" is declared twice")
		}
		for _, v := range volList {
			if !doc.Truthy(v) || !jIsObj(v) {
				is.Err(cb+".volumes", "V_TYPE", "a volume needs volumeSlug, label, description, backup")
				continue
			}
			vs := jGet(v, "volumeSlug")
			vp := cb + ".volumes[" + jStr(vs) + "]"
			if s, ok := vs.(string); !ok || !RE.VolumeSlug.MatchString(s) {
				is.Err(vp+".volumeSlug", "V_FORMAT", `volume name "`+jStr(vs)+`" must match ^[a-z0-9][a-z0-9_.-]{0,99}$`)
			} else {
				declared[s] = true
			}
			if s, ok := jGet(v, "label").(string); !ok || doc.JSLen(s) > LIMITS.Label {
				is.Err(vp+".label", "V_LENGTH", "label must be a string of at most "+strconv.Itoa(LIMITS.Label)+" characters")
			}
			if s, ok := jGet(v, "description").(string); !ok || doc.JSLen(s) > LIMITS.Description {
				is.Err(vp+".description", "V_LENGTH", "description must be a string of at most "+strconv.Itoa(LIMITS.Description)+" characters")
			}
			if _, ok := jGet(v, "backup").(bool); !ok {
				is.Err(vp+".backup", "V_TYPE", "backup must be true / false")
			}
			jCheckLocation(jGet(v, "location"), "volumes", vp+".location", is)
			if !jIsOneOf(jGet(v, "kind"), "dir", "file") {
				is.Err(vp+".kind", "V_VOLUME_KIND", "kind must be dir | file")
			}
			g.checkSeed(v, vp, &seedRenders, content)
		}
		type loc struct {
			what string
			path any
			at   string
		}
		var locs []loc
		for _, f := range config.Arr("files") {
			if s, ok := jOpt(f, "fileSlug").(string); doc.Truthy(f) && ok {
				locs = append(locs, loc{"file " + s, jLocationPath(jGet(f, "location"), "files", s), "files[" + s + "].location"})
			}
		}
		for _, v := range volList {
			if !doc.Truthy(v) {
				continue
			}
			if s, ok := jGet(v, "volumeSlug").(string); ok {
				locs = append(locs, loc{"volume " + s, jLocationPath(jGet(v, "location"), "volumes", s), cb + ".volumes[" + s + "].location"})
			}
		}
		for i := range locs {
			for k := 0; k < i; k++ {
				a, b := locs[i], locs[k]
				if jStartsWith(a.path, "./", "a.path") != jStartsWith(b.path, "./", "b.path") {
					continue
				}
				if jNestedOrSame(jTrimSlash(a.path.(string)), jTrimSlash(b.path.(string))) {
					at := a.at
					if strings.HasPrefix(a.at, "files[") && !strings.HasPrefix(b.at, "files[") {
						at = b.at
					}
					is.Err(at, "V_LOCATION", a.what+" ("+jStr(a.path)+") and "+b.what+" ("+jStr(b.path)+") are the same place or nested on site "+ss)
				}
			}
		}
		if cts, ok := jGet(sc, "containers").([]any); ok {
			used := map[string]bool{}
			for _, c := range cts {
				for _, v := range jArrOf(jGet(c, "volumes")) {
					src := jOpt(v, "source")
					if jIs(jOpt(src, "kind"), "volume") {
						used[jStr(jGet(src, "volumeSlug"))] = true
					}
				}
			}
			for _, v := range volList {
				if !doc.Truthy(v) {
					continue
				}
				if s, ok := jGet(v, "volumeSlug").(string); ok && !used[s] {
					tail := ""
					if doc.Truthy(jGet(v, "backup")) {
						tail = " and is still backed up"
					}
					is.Warn(cb+".volumes["+s+"]", "V_VOLUME_UNUSED", "volume "+s+" is declared but no container mounts it (its data stays on disk"+tail+")")
				}
			}
		}
	}
	cts, ok := jGet(sc, "containers").([]any)
	if !ok {
		is.Err(cb+".containers", "V_TYPE", "containers must be a list")
		return
	}
	if len(cts) > LIMITS.ContainersPerSite {
		is.Err(cb+".containers", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.ContainersPerSite))
	}
	for _, d := range jOrderedDupes(cts, func(c any) any { return jGet(c, "containerSlug") }) {
		is.Err(cb+".containers["+jStr(d)+"].containerSlug", "V_DUPLICATE", "container name "+jStr(d)+" is used twice on this site")
	}
	vars := jVarValues(config, ctx.deferred)
	listeners := []jListener{{agentPort, "tcp", "agentPort", cb + ".agentPort"}}
	if aliasPort != nil {
		listeners = append(listeners, jListener{aliasPort, "tcp", "agentGuiAliasPort", cb + ".agentGuiAliasPort"})
	}
	edgeRaw := jGet(sc, "edge")
	var edgePorts []any
	for _, p := range []any{jOpt(edgeRaw, "httpPort"), jOpt(edgeRaw, "httpsPort")} {
		if jIsInt(p) {
			edgePorts = append(edgePorts, p)
		}
	}
	info := map[string]*jContainerInfo{}
	slugs := make([]any, len(cts))
	for i, c := range cts {
		slugs[i] = jGet(c, "containerSlug")
	}
	renders := slices.Clone(seedRenders)
	for _, c := range cts {
		g.validateContainer(c, sc, cb, siteSlug, declared, volsArr, slugs, vars, edgePorts, &renders, &listeners, info)
	}
	if cycle := jContainerCycle(sc); cycle != nil {
		is.Err(cb+".containers", "V_CYCLE", "container depends form a cycle: "+jJoin(cycle, " → "))
	} else {
		inf := jInferDepends(sc, ctx, config)
		for _, d := range inf {
			for _, sg := range d.suggested {
				is.Info(cb+".containers["+jStr(d.slug)+"].depends", "V_DEPENDS_SUGGESTED", jStr(d.slug)+" mentions `"+sg.slug+"` ("+strings.Join(sg.from, ", ")+") — add it to Depends on?")
			}
		}
		if cyc := jStartCycle(sc, inf); len(cyc) > 0 {
			is.Warn(cb+".containers", "V_START_ORDER_CYCLE", "containers "+jJoin(cyc, ", ")+" reference each other (expressions or hostnames): they start in the declared order")
		}
	}
	disabledRef := func(src any, path string) {
		s, ok := src.(string)
		if !ok || !expr.IsExpr(s) {
			return
		}
		refs, err := expr.ExprRefs(s)
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, r := range refs {
			m := jReContainersRef.FindStringSubmatch(r)
			if m == nil || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			if ci := info[m[1]]; ci != nil && !doc.Truthy(ci.enabled) {
				is.Warn(path, "V_CONTAINER_REF_DISABLED", "container "+m[1]+" is disabled: "+r+" resolves, but nothing answers on it")
			}
		}
	}
	for _, c := range cts {
		env, ok := jGet(c, "env").([]any)
		if !doc.Truthy(c) || jEq(jGet(c, "enabled"), false) || !ok {
			continue
		}
		at := cb + ".containers[" + jStr(jGet(c, "containerSlug")) + "]"
		for _, e := range env {
			if doc.Truthy(e) {
				if s, ok := jGet(e, "value").(string); ok {
					disabledRef(s, at+".env["+jStr(jGet(e, "name"))+"].value")
				}
			}
		}
		img := ctx.image(c)
		for _, raw := range jList(jOpt(img, "env"), "(img?.env ?? [])") {
			ie := jNormalizeImageEnv(raw)
			given := slices.ContainsFunc(env, func(x any) bool { return jEq(jOpt(x, "name"), ie.Val("name")) })
			if s, ok := ie.Val("value").(string); !given && ok {
				disabledRef(s, at+".env["+jStr(ie.Val("name"))+"]")
			}
		}
	}
	if ctx.files != nil {
		for _, slug := range renders {
			disabledRef(ctx.files.Val(slug), "files["+slug+"]")
		}
	}
	if ctx.files != nil && len(renders) > 0 {
		fctx := jSiteFileContext(config, ctx, ss, g.siteMap)
		for _, slug := range renders {
			f := jFind(config.Arr("files"), func(x any) bool { return jIs(jGet(x, "fileSlug"), slug) })
			text, ok := ctx.files.Val(slug).(string)
			if !ok {
				continue
			}
			r, err := expr.Resolve(text, fctx, expr.FieldFile)
			if err != nil {
				e := jExprErr(err)
				if e.Code == "E_DEFERRED" {
					continue
				}
				line, col := expr.LineCol(text, e.Pos)
				is.Err("files["+slug+"]", "V_FILE_RENDER", slug+":"+strconv.Itoa(line)+":"+strconv.Itoa(col)+": "+e.Code+" "+e.Message+" (site "+ss+")", "publish")
			} else if jReJSONMime.MatchString(jStr(jGet(f, "mime"))) && !expr.IsDeferredValue(r.Value) {
				if je := jJSONError(r.Value); je != "" {
					is.Err("files["+slug+"]", "V_FILE_JSON", slug+": the rendered file is not valid JSON for site "+ss+": "+strings.Replace(je, r.Value, "…", 1), "publish")
				}
			}
		}
	}
	if ctx.blobs != nil && len(content.order) > 0 {
		fctx := jSiteFileContext(config, ctx, ss, g.siteMap)
		for _, sha := range content.order {
			text, ok := ctx.blobs.Val(sha).(string)
			if !ok {
				continue
			}
			_, err := expr.Resolve(text, fctx, expr.FieldFile)
			var e *expr.Error
			if err != nil {
				e = jExprErr(err)
				if e.Code == "E_DEFERRED" {
					continue
				}
			}
			for _, w := range content.where[sha] {
				disabledRef(text, w)
				if e != nil {
					line, col := expr.LineCol(text, e.Pos)
					short := jReEntriesWhere.ReplaceAllString(w, "")
					is.Err(w, "V_FILE_RENDER", short+":"+strconv.Itoa(line)+":"+strconv.Itoa(col)+": "+e.Code+" "+e.Message+" (site "+ss+")", "publish")
				}
			}
		}
	}
	if !doc.Truthy(edgeRaw) || !jIsObj(edgeRaw) {
		is.Err(cb+".edge", "V_TYPE", "edge must be an object")
	} else {
		g.validateEdge(edgeRaw, cb+".edge", agentPort, aliasPort, info)
		for i, p := range edgePorts {
			what := "edge.httpPort"
			if i > 0 {
				what = "edge.httpsPort"
			}
			listeners = append(listeners, jListener{p, "tcp", what, cb + ".edge"})
		}
		ex := jBaseCtx(ctx.deploymentID, ss, g.hosts[ss].host, jVarValues(config, ctx.deferred))
		tryEdge := func(v any, path string) {
			s, ok := v.(string)
			if !ok || !expr.IsExpr(s) || len(expr.CheckExpr(s, expr.FieldEdge)) > 0 {
				return
			}
			if _, err := expr.Resolve(s, ex, expr.FieldEdge); err != nil {
				e := jExprErr(err)
				is.Err(path, e.Code, e.Message, "publish")
			}
		}
		parts := func(owner any, prefix string) {
			rh := jGet(owner, "responseHeaders")
			for _, part := range []string{"set", "add"} {
				for _, kv := range jObjEntries(jCoal(jOpt(rh, part), doc.New())) {
					tryEdge(kv.v, prefix+".responseHeaders."+part+"."+kv.k)
				}
			}
		}
		hostsArr := jGet(edgeRaw, "hosts")
		if !jNull(hostsArr) {
			for _, h := range jArr(hostsArr, "forEach", "sc.edge.hosts?") {
				if s, ok := jGet(h, "hostname").(string); ok {
					tryEdge(s, cb+".edge.hosts["+s+"].hostname")
				}
			}
		}
		if rs := jGet(edgeRaw, "routes"); !jNull(rs) {
			for _, r := range jArr(rs, "forEach", "sc.edge.routes?") {
				a := jGet(r, "action")
				rid := jStr(jGet(r, "routeId"))
				t := jOpt(a, "type")
				if jIs(t, "redirect") {
					if s, ok := jGet(a, "to").(string); ok {
						tryEdge(s, cb+".edge.routes["+rid+"].action.to")
					}
				}
				if jIs(t, "proxy") {
					for _, kv := range jObjEntries(jCoal(jGet(a, "setHeaders"), doc.New())) {
						tryEdge(kv.v, cb+".edge.routes["+rid+"].action.setHeaders."+kv.k)
					}
					if rw := jGet(a, "rewrite"); doc.Truthy(rw) {
						if s, ok := jGet(rw, "replacement").(string); ok {
							tryEdge(s, cb+".edge.routes["+rid+"].action.rewrite.replacement")
						}
					}
				}
				if jIs(t, "rewrite") {
					if s, ok := jGet(a, "replacement").(string); ok {
						tryEdge(s, cb+".edge.routes["+rid+"].action.replacement")
					}
				}
				parts(r, cb+".edge.routes["+rid+"]")
			}
		}
		if !jNull(hostsArr) {
			for _, h := range jArr(hostsArr, "forEach", "sc.edge.hosts?") {
				parts(h, cb+".edge.hosts["+jStr(jGet(h, "hostname"))+"]")
			}
		}
	}
	seen := map[string]string{}
	for _, l := range listeners {
		if !jIsInt(l.port) {
			continue
		}
		k := jStr(l.proto) + "/" + jStr(l.port)
		prev, dup := seen[k]
		if dup && prev != "" && !(strings.HasPrefix(l.what, "edge.") && strings.HasPrefix(prev, "edge.")) {
			is.Err(l.path, "V_PORT_CONFLICT", jStr(l.port)+"/"+jStr(l.proto)+" is already used by "+prev)
		} else {
			seen[k] = l.what
		}
	}
}

var (
	jReContainersRef = regexp.MustCompile(`^containers\.([^.]+)\.`)
	jReEntriesWhere  = regexp.MustCompile(`^(?s:.*)\.seed\.entries\[|\]$`)
	jReJSONMime      = regexp.MustCompile(`^[aA][pP][pP][lL][iI][cC][aA][tT][iI][oO][nN]/([a-zA-Z0-9.+-]*\+)?[jJ][sS][oO][nN]$`)
)

type jKV struct {
	k string
	v any
}

// jObjEntries is Object.entries.
func jObjEntries(v any) []jKV {
	var out []jKV
	switch x := v.(type) {
	case *doc.Map:
		x.Each(func(k string, y any) { out = append(out, jKV{k, y}) })
	case []any:
		for i, y := range x {
			out = append(out, jKV{strconv.Itoa(i), y})
		}
	case string:
		for i, r := range []rune(x) {
			out = append(out, jKV{strconv.Itoa(i), string(r)})
		}
	}
	return out
}

func (g *jGlobals) validateContainer(c, sc any, cb string, siteSlug any, declared map[string]bool, volsArr bool, slugs []any, vars map[string]any, edgePorts []any, renders *[]string, listeners *[]jListener, info map[string]*jContainerInfo) {
	is, ctx, config := g.is, g.ctx, g.config
	ss := jStr(siteSlug)
	cslug := jGet(c, "containerSlug")
	at := cb + ".containers[" + jStr(cslug) + "]"
	CheckSlug(cslug, at+".containerSlug", is)
	if jIsOneOf(cslug, RESERVED_CONTAINER_SLUGS...) {
		is.Err(at+".containerSlug", "V_RESERVED", `"`+jStr(cslug)+`" is reserved (GUI route …/containers/new)`)
	}
	service := jGet(c, "serviceSlug")
	if !ctx.hasService(service) {
		is.Err(at+".serviceSlug", "V_REF", "service "+jStr(service)+" does not exist")
		return
	}
	slug := jStr(service)
	version := jGet(c, "version")
	img := ctx.image(c)
	if s, ok := version.(string); !ok || !RE.Version.MatchString(s) {
		is.Err(at+".version", "V_FORMAT", `version "`+jStr(version)+`" is not an image version (v1, 3.1.0 …)`)
	}
	imgName := slug + ":" + jStr(version)
	if !doc.Truthy(img) {
		is.Err(at+".version", "V_REF", "image "+imgName+" does not exist")
		return
	}
	if st := jGet(img, "importStatus"); doc.Truthy(st) && !jIs(st, "ready") {
		is.Err(at+".version", "V_IMAGE_NOT_READY", "image "+imgName+" import is "+jStr(st), "publish")
	}
	enabled := jGet(c, "enabled")
	if _, ok := enabled.(bool); !ok {
		is.Err(at+".enabled", "V_TYPE", "enabled must be true / false")
	}
	restart := jGet(c, "restart")
	if !jIsOneOf(restart, "always", "on-failure", "never") {
		is.Err(at+".restart", "V_ENUM", "restart must be always | on-failure | never")
	}
	if jIs(jGet(img, "kind"), "job") && jIs(restart, "always") {
		is.Err(at+".restart", "V_JOB_RESTART", "a job must use restart never or on-failure")
	}
	ValidateCommand(jGet(c, "command"), at+".command", is)
	ValidateCommand(jGet(c, "entrypoint"), at+".entrypoint", is)
	r := jCoal(jGet(c, "resources"), doc.Obj("cpus", nil, "memoryMb", nil))
	if v := jGet(r, "cpus"); v != nil {
		f, ok := v.(float64)
		if !ok || f < 0.1 || f > 64 {
			is.Err(at+".resources.cpus", "V_RANGE", "cpus must be 0.1–64 or null")
		}
	}
	if v := jGet(r, "memoryMb"); v != nil {
		f, _ := v.(float64)
		if !jIsInt(v) || f < 64 || f > 1048576 {
			is.Err(at+".resources.memoryMb", "V_RANGE", "memoryMb must be an integer 64–1048576 or null")
		}
	}
	imgPorts := jArr(jGet(img, "ports"), "map", "img.ports")
	portNames := make([]any, len(imgPorts))
	for i, p := range imgPorts {
		portNames[i] = jGet(p, "name")
	}
	if hc := jGet(c, "healthcheck"); !jNull(hc) {
		jValidateHealthCheck(hc, portNames, is, at+".healthcheck")
	}
	env, ok := jGet(c, "env").([]any)
	if !ok {
		is.Err(at+".env", "V_TYPE", "env must be a list")
		return
	}
	if len(env) > LIMITS.EnvPerContainer {
		is.Err(at+".env", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.EnvPerContainer))
	}
	for _, d := range jOrderedDupes(env, jName) {
		is.Err(at+".env["+jStr(d)+"]", "V_DUPLICATE", "env "+jStr(d)+" is set twice")
	}
	ectx := jContainerExprContext(config, ctx, ss, c, g.siteMap)
	var imgEnv []any
	for _, e := range jArr(jGet(img, "env"), "map", "img.env") {
		imgEnv = append(imgEnv, jNormalizeImageEnv(e))
	}
	imgVols := jGet(img, "volumes")
	envSrc, mountSrc := jSources(c, jCoal(any(imgEnv), []any{}), imgVols, ectx.Secret)
	for _, e := range env { // resolveContainer: envBy.get(`${name}`) misses a non-string name
		if _, ok := jGet(e, "name").(string); !ok {
			jThrow("Cannot read properties of undefined (reading 'secretRef')")
		}
	}
	res := expr.ResolveContainer(ectx, envSrc, mountSrc)
	imgFind := func(name any) any {
		return jFind(imgEnv, func(x any) bool { return jEq(jGet(x, "name"), name) })
	}
	for _, e := range env {
		name := jGet(e, "name")
		ep := at + ".env[" + jStr(name) + "]"
		if s, ok := name.(string); !ok || !RE.EnvName.MatchString(s) {
			is.Err(ep+".name", "V_FORMAT", `"`+jStr(name)+`" does not match ^[A-Z_][A-Z0-9_]*$`)
		}
		value, ref := jGet(e, "value"), jGet(e, "secretRef")
		hv, hs := !jNull(value), !jNull(ref)
		if hv == hs {
			word := "neither"
			if hv {
				word = "both"
			}
			is.Err(ep, "V_ENV_ONE_OF", "needs exactly one of value / secretRef (it has "+word+")")
		}
		if hs && !jIncludes(g.secretSlugs, ref) {
			is.Err(ep+".secretRef", "V_REF", "secret "+jStr(ref)+" does not exist")
		}
		ie := imgFind(name)
		sv, isStr := value.(string)
		if hv {
			if !isStr {
				is.Err(ep+".value", "V_TYPE", "value must be a string")
			} else {
				if doc.JSLen(sv) > LIMITS.Value {
					is.Err(ep+".value", "V_LENGTH", "at most "+strconv.Itoa(LIMITS.Value)+" characters")
				}
				se := expr.CheckExpr(sv, expr.FieldEnv)
				if len(se) > 0 {
					for _, x := range se {
						is.Err(ep+".value", x.Code, jExprAt(x))
					}
				} else if rr, known := res.Env[jStr(name)]; known {
					if rr.Err != nil {
						is.Err(ep+".value", rr.Err.Code, jExprAt(rr.Err), "publish")
					} else if o, ok := jOpt(ie, "options").([]any); ok && len(o) >= 0 && !rr.Result.Secret && !rr.Result.Deferred && !jIncludes(o, rr.Result.Value) {
						is.Err(ep+".value", "V_OPTION", `"`+rr.Result.Value+`" is not one of the allowed values of `+jStr(name)+": "+jJoin(o, ", "), "publish")
					}
				}
			}
		}
		if doc.Truthy(jOpt(ie, "secret")) && hv && isStr && !jReEnvSecret.MatchString(sv) {
			is.Err(ep, "V_ENV_SECRET", "image env "+jStr(name)+" is secret: use secretRef or a secret.* expression", "publish")
		}
	}
	for _, ie := range imgEnv {
		name, value := jGet(ie, "name"), jGet(ie, "value")
		given := slices.ContainsFunc(env, func(x any) bool { return jEq(jGet(x, "name"), name) })
		if !given && doc.Truthy(jGet(ie, "required")) && value == doc.Undefined {
			is.Err(at+".env", "V_ENV_REQUIRED", "image env "+jStr(name)+" is required (no default)", "publish")
		}
		if _, ok := value.(string); !given && ok {
			if rr, known := res.Env[jStr(name)]; known && rr.Err != nil {
				is.Err(at+".env["+jStr(name)+"]", rr.Err.Code, "image default: "+rr.Err.Message, "publish")
			}
		}
	}
	ivList := jIter(imgVols, "img.volumes")
	if vols, ok := jGet(c, "volumes").([]any); !ok {
		is.Err(at+".volumes", "V_TYPE", "volumes must be a list")
	} else {
		if len(vols) > LIMITS.VolumesPerContainer {
			is.Err(at+".volumes", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.VolumesPerContainer))
		}
		var mapped []any
		for _, v := range vols {
			if doc.Truthy(jOpt(v, "imageVolume")) {
				mapped = append(mapped, v)
			}
		}
		for _, d := range jOrderedDupes(mapped, func(v any) any { return jGet(v, "imageVolume") }) {
			is.Err(at+".volumes["+jStr(d)+"]", "V_DUPLICATE", "image volume "+jStr(d)+" is mapped twice")
		}
		for _, iv := range ivList {
			name := jGet(iv, "name")
			if !slices.ContainsFunc(vols, func(v any) bool { return jEq(jOpt(v, "imageVolume"), name) }) {
				is.Warn(at+".volumes["+jStr(name)+"]", "V_VOLUME_NOT_MOUNTED", "image volume "+jStr(name)+" ("+jStr(jGet(iv, "mountPath"))+`) is not mounted: its data is lost when the container is recreated. Pick a data volume, or choose "Not mounted"`)
			}
		}
		for i, v := range vols {
			g.validateMount(v, i, at, imgName, ivList, sc, declared, volsArr, ss, c, renders)
		}
		g.validateEffectiveMounts(c, at, imgVols, res)
	}
	hostPorts := []float64{}
	if ports, ok := jGet(c, "ports").([]any); !ok {
		is.Err(at+".ports", "V_TYPE", "ports must be a list")
	} else {
		for _, d := range jOrderedDupes(ports, func(p any) any { return jGet(p, "portName") }) {
			is.Err(at+".ports["+jStr(d)+"]", "V_DUPLICATE", "port "+jStr(d)+" is listed twice")
		}
		for _, p := range ports {
			pn := jGet(p, "portName")
			pp := at + ".ports[" + jStr(pn) + "]"
			ip := jFind(imgPorts, func(x any) bool { return jEq(jGet(x, "name"), pn) })
			if !doc.Truthy(ip) {
				is.Err(pp+".portName", "V_REF", "image "+imgName+" has no port "+jStr(pn))
			}
			if !jIsOneOf(jGet(p, "hostIp"), "0.0.0.0", "127.0.0.1") {
				is.Err(pp+".hostIp", "V_ENUM", "hostIp must be 0.0.0.0 | 127.0.0.1")
			}
			if _, ok := jGet(p, "edge").(bool); !ok {
				is.Err(pp+".edge", "V_TYPE", "edge must be true / false")
			}
			hp := jGet(p, "hostPort")
			if hp == nil {
				continue
			}
			var n *float64
			switch x := hp.(type) {
			case float64:
				CheckPort(x, pp+".hostPort", is, 1)
				if jIsInt(x) {
					n = &x
				}
			default:
				s, isStr := hp.(string)
				if !isStr || !expr.IsExpr(s) {
					is.Err(pp+".hostPort", "V_TYPE", "hostPort must be null, a number, or one whole {{ }} block")
					break
				}
				if se := expr.CheckExpr(s, expr.FieldHostPort); len(se) > 0 {
					for _, e := range se {
						is.Err(pp+".hostPort", e.Code, e.Message)
					}
					break
				}
				v, err := jNumField(s, jBaseCtx(ctx.deploymentID, ss, nil, vars), expr.FieldHostPort)
				if err != nil {
					e := jExprErr(err)
					is.Err(pp+".hostPort", e.Code, e.Message, "publish")
				}
				n = v
			}
			if n == nil {
				continue
			}
			if *n == 80 || *n == 443 || jIncludes(edgePorts, *n) {
				is.Err(pp+".hostPort", "V_PORT_EDGE", "host port "+jNum(*n)+" belongs to the edge; add a route instead")
			}
			hostPorts = append(hostPorts, *n)
			if doc.Truthy(enabled) {
				*listeners = append(*listeners, jListener{*n, jCoal(jOpt(ip, "protocol"), "tcp"), jStr(cslug) + "." + jStr(pn), pp + ".hostPort"})
			}
		}
	}
	if deps, ok := jGet(c, "depends").([]any); !ok {
		is.Err(at+".depends", "V_TYPE", "depends must be a list")
	} else {
		for _, d := range deps {
			ds := jGet(d, "containerSlug")
			if jEq(ds, cslug) {
				is.Err(at+".depends", "V_CYCLE", "a container cannot depend on itself")
			} else if !jIncludes(slugs, ds) {
				is.Err(at+".depends", "V_REF", "container "+jStr(ds)+" does not exist on this site")
			}
		}
	}
	var edgeNames []any
	for _, p := range jContainerPorts(c, img) {
		if doc.Truthy(p.Val("edge")) {
			edgeNames = append(edgeNames, p.Val("portName"))
		}
	}
	info[jStr(cslug)] = &jContainerInfo{enabled, hostPorts, portNames, edgeNames, cslug}
}

func (g *jGlobals) validateMount(v any, i int, at, imgName string, ivList []any, sc any, declared map[string]bool, volsArr bool, ss string, c any, renders *[]string) {
	is, config := g.is, g.config
	iv0 := jOpt(v, "imageVolume")
	vp := at + ".volumes[" + jStr(jCoal(iv0, float64(i))) + "]"
	src := jOpt(v, "source")
	if !doc.Truthy(v) || !jIsObj(v) || !doc.Truthy(src) || !jIsObj(src) {
		is.Err(vp, "V_TYPE", "a volume row needs imageVolume, mountPath, source, readOnly")
		return
	}
	imageVolume := jGet(v, "imageVolume")
	byName := func(name any) any {
		return jFind(ivList, func(x any) bool { return jEq(jGet(x, "name"), name) })
	}
	if imageVolume != nil && !doc.Truthy(byName(imageVolume)) {
		is.Err(vp+".imageVolume", "V_REF", "image "+imgName+" has no volume "+jStr(imageVolume))
	}
	mp := jGet(v, "mountPath")
	kind := jGet(src, "kind")
	if imageVolume == nil {
		if jEmptyVal(mp) {
			is.Err(vp+".mountPath", "V_REQUIRED", "an extra mount needs a mount path")
		}
		if jIs(kind, "none") {
			is.Err(vp+".source.kind", "V_VOLUME", "an extra mount needs a source (volume, host, file or secret)")
		}
	}
	if !jNull(mp) {
		jCheckMountPath(mp, vp+".mountPath", is)
	}
	readOnly := jGet(v, "readOnly")
	if _, ok := readOnly.(bool); !ok {
		is.Err(vp+".readOnly", "V_TYPE", "readOnly must be true / false")
	}
	if jIsOneOf(kind, "file", "secret") && !jEq(readOnly, true) {
		is.Err(vp+".readOnly", "V_READONLY", "a "+jStr(kind)+" mount is always read-only (readOnly must be true)")
	}
	switch {
	case jIs(kind, "volume"):
		vs := jGet(src, "volumeSlug")
		s, ok := vs.(string)
		if !ok || !RE.VolumeSlug.MatchString(s) {
			is.Err(vp+".source.volumeSlug", "V_FORMAT", `volume name "`+jStr(vs)+`" must match ^[a-z0-9][a-z0-9_.-]{0,99}$`)
			return
		}
		if volsArr && !declared[s] {
			is.Err(vp+".source.volumeSlug", "V_VOLUME_UNDECLARED", "volume "+s+" is not declared on site "+ss+": add it under the site's Volumes")
			return
		}
		sv := jFind(jArr(jGet(sc, "volumes"), "find", "sc.volumes"), func(x any) bool { return jIs(jOpt(x, "volumeSlug"), s) })
		var iv any = doc.Undefined
		if imageVolume != nil {
			iv = byName(imageVolume)
		}
		hasIV := doc.Truthy(iv)
		atImagePath := hasIV && jNull(mp)
		ivKind := ""
		if hasIV {
			ivKind = jImageVolumeKind(iv)
		}
		if jIs(jOpt(sv, "kind"), "file") {
			m := mp
			if jNull(m) {
				m = jCoal(jOpt(iv, "mountPath"), "")
			}
			if !doc.Truthy(m) || (m != nil && jStrEnds(m, "/", "mp")) {
				is.Err(vp+".mountPath", "V_VOLUME_KIND", "volume "+s+" is a single file: mount it at a file path (e.g. /data/flows.json), not a folder")
			} else if atImagePath && ivKind == "dir" {
				name, path := jStr(jGet(iv, "name")), jStr(jGet(iv, "mountPath"))
				is.Warn(vp+".source.volumeSlug", "V_VOLUME_KIND_IMAGE", "image volume "+name+" ("+path+") is a Folder in image "+imgName+"; the single-file volume "+s+" replaces the whole folder with one file — pick a folder volume here, or set image volume "+name+" to File on the image page if "+path+" is a file")
			}
			if jEq(readOnly, false) {
				is.Info(vp+".source.volumeSlug", "V_FILE_VOLUME_RENAME", s+" is a single-file volume mounted read-write: apps that save by rename (e.g. Node-RED flows.json) fail on single-file mounts (EBUSY) — use a folder volume seeded with the file")
			}
		} else if atImagePath && ivKind == "file" {
			name, path := jStr(jGet(iv, "name")), jStr(jGet(iv, "mountPath"))
			is.Err(vp+".source.volumeSlug", "V_VOLUME_KIND_MISMATCH", "folder volume "+s+" cannot be mounted on file path "+path+" (image volume "+name+" is a File) — use a single-file volume, or change the image volume to Folder on the image page")
		}
	case jIs(kind, "host"):
		if p, ok := jGet(src, "path").(string); !ok || expr.IsExpr(p) || !IsSafeMountPath(p) {
			is.Err(vp+".source.path", "V_VOLUME", `host path must be an absolute path, not "/", without spaces, ".." or {{ }}`)
		}
	case jIs(kind, "file"):
		slug := jGet(src, "fileSlug")
		f := jFind(config.Arr("files"), func(x any) bool { return jEq(jGet(x, "fileSlug"), slug) })
		if !doc.Truthy(f) {
			is.Err(vp+".source.fileSlug", "V_REF", "file "+jStr(slug)+" does not exist")
			return
		}
		if !doc.Truthy(jGet(f, "content")) {
			is.Err(vp+".source.fileSlug", "V_FILE_CONTENT", "file "+jStr(slug)+" has no content", "publish")
		}
		if doc.Truthy(jGet(c, "enabled")) && jIs(jFileRenderMode(f), "expressions") {
			jAddStr(renders, jStr(jGet(f, "fileSlug")))
		}
	case jIs(kind, "secret"):
		if slug := jGet(src, "secretSlug"); !jIncludes(g.secretSlugs, slug) {
			is.Err(vp+".source.secretSlug", "V_REF", "secret "+jStr(slug)+" does not exist")
		}
	case !jIs(kind, "none"):
		if jIs(kind, "data") {
			is.Err(vp+".source.kind", "V_ENUM", `source kind "data" was replaced by declared site volumes ({ kind: "volume", volumeSlug })`)
		} else {
			is.Err(vp+".source.kind", "V_ENUM", "source.kind must be volume | host | file | secret | none")
		}
	}
}

func jStrEnds(v any, suffix, src string) bool {
	s, ok := v.(string)
	if !ok {
		jNotFn(v, "endsWith", src)
	}
	return strings.HasSuffix(s, suffix)
}

func (g *jGlobals) validateEffectiveMounts(c any, at string, imgVols any, res *expr.ContainerResolution) {
	is := g.is
	type used struct {
		what string
		expr bool
	}
	seen := map[string]used{}
	all := jContainerMounts(c, imgVols)
	slotOf := func(m jMount) string {
		if doc.Truthy(m.imageVolume) {
			return jStr(m.imageVolume)
		}
		return m.key[7 : len(m.key)-1]
	}
	for _, m := range all {
		vp := at + ".volumes[" + slotOf(m) + "].mountPath"
		rr, known := res.Mounts[m.key]
		s, isStr := m.mountPath.(string)
		isExpr := isStr && expr.IsExpr(s)
		if isExpr && strings.HasPrefix(s, "/") && len(expr.CheckExpr(s, expr.FieldMountPath)) == 0 && known {
			if rr.Err != nil {
				is.Err(vp, rr.Err.Code, jExprAt(rr.Err), "publish")
			} else if !rr.Result.Deferred && !IsSafeMountPath(rr.Result.Value) {
				is.Err(vp, "V_PATH", `resolves to "`+rr.Result.Value+`": a mount path must be absolute, not "/", without spaces or ".."`, "publish")
			}
		}
		var eff any = m.mountPath
		if known && rr.Err == nil {
			eff = rr.Result.Value
		}
		what := m.key
		if doc.Truthy(m.imageVolume) {
			what = "image volume " + jStr(m.imageVolume)
		}
		if prev, dup := seen[jKey(eff)]; dup {
			level := "save"
			if prev.expr || isExpr {
				level = "publish"
			}
			is.Err(vp, "V_MOUNT", "mount path "+jStr(eff)+" is also used by "+prev.what, level)
		} else {
			seen[jKey(eff)] = used{what, isExpr}
		}
	}
	type effT struct {
		path     string
		expr, ok bool
	}
	effOf := func(m jMount) effT {
		rr, known := res.Mounts[m.key]
		var p any = m.mountPath
		if known && rr.Err == nil && !rr.Result.Deferred {
			p = rr.Result.Value
		}
		ps, ok := p.(string)
		if !ok {
			jThrow("(rr?.ok && !rr.result.deferred ? rr.result.value : m.mountPath).replace is not a function")
		}
		s, _ := m.mountPath.(string)
		isExpr := expr.IsExpr(s)
		return effT{jTrimSlash(ps), isExpr, !isExpr || (known && rr.Err == nil && !rr.Result.Deferred)}
	}
	for _, fm := range all {
		if !jIs(jGet(fm.source, "kind"), "file") {
			continue
		}
		f := effOf(fm)
		if !f.ok || f.path == "" {
			continue
		}
		for _, wm := range all {
			wk := jGet(wm.source, "kind")
			if wm.readOnly || !jIsOneOf(wk, "volume", "host") {
				continue
			}
			w := effOf(wm)
			if !w.ok || w.path == "" || !(f.path == w.path || strings.HasPrefix(f.path, w.path+"/")) {
				continue
			}
			fileSlug := jStr(jGet(fm.source, "fileSlug"))
			var what, fix string
			if jIs(wk, "volume") {
				what = "volume " + jStr(jGet(wm.source, "volumeSlug"))
				rel := f.path[min(len(w.path)+1, len(f.path)):]
				if rel == "" {
					rel = "(the mount itself)"
				}
				fix = "put it into the volume's own content (Volume → Content) as " + rel + ", mount the file outside " + w.path + ", or mount " + what + " read-only"
			} else {
				what = "host folder " + jStr(jGet(wm.source, "path"))
				fix = "mount the file outside " + w.path + ", or mount the host folder read-only"
			}
			fp := at + ".volumes[" + slotOf(fm) + "].source.fileSlug"
			msg := "file " + fileSlug + " is mounted at " + f.path + " inside the writable " + what + " (" + w.path + ") — Files are static and read-only and are never mounted on a writable volume: " + fix
			if is.Mode == "agent" {
				is.Warn(fp, "V_FILE_IN_WRITABLE_VOLUME", msg)
			} else if f.expr || w.expr {
				is.Err(fp, "V_FILE_IN_WRITABLE_VOLUME", msg, "publish")
			} else {
				is.Err(fp, "V_FILE_IN_WRITABLE_VOLUME", msg)
			}
			break
		}
	}
}

// ─────────────────────────────────────────── edge

var (
	jReIPChars      = regexp.MustCompile(`^[0-9a-fA-F:.]+$`)
	jRePathBad      = regexp.MustCompile(`[` + jWS + `?#]`)
	jReCacheControl = regexp.MustCompile(`(?i)\b(public|private|max-age|s-maxage|no-store|must-revalidate|proxy-revalidate|immutable)\b`)
)

var jHopByHop = []string{"Connection", "Upgrade", "Keep-Alive", "Transfer-Encoding", "TE", "Trailer", "Proxy-Connection"}

const (
	jEdgeMaxBody           = 1024 * 1024 * 1024 * 1024
	jEdgeMaxTimeoutSec     = 86400
	jEdgeMaxConnectTimeout = 3600
)

func jIsIPLiteral(s string) bool {
	return RE.IPv4.MatchString(s) || (strings.Contains(s, ":") && jReIPChars.MatchString(s))
}

// IsIpLiteral is isIpLiteral.
func IsIpLiteral(s string) bool { return jIsIPLiteral(s) }

// IsResponseOnlyHeader is isResponseOnlyHeader.
func IsResponseOnlyHeader(name, value string) bool {
	n := jLower(name)
	if n == "cache-control" {
		return jReCacheControl.MatchString(value)
	}
	switch n {
	case "expires", "pragma", "set-cookie", "content-security-policy", "content-security-policy-report-only", "strict-transport-security", "x-frame-options":
		return true
	}
	return strings.HasPrefix(n, "access-control-allow-") || n == "access-control-expose-headers" || n == "access-control-max-age"
}

// CheckEdgeRegex is checkEdgeRegex of edge-resolve.ts (nil = valid).
func CheckEdgeRegex(src string) *string {
	e := func(s string) *string { return &s }
	if src == "" {
		return e("the regex is empty")
	}
	if doc.JSLen(src) > 1024 {
		return e("the regex is longer than 1024 characters")
	}
	body, flags := src, ""
	if strings.HasPrefix(src, "(?i)") {
		body, flags = src[4:], "i"
	}
	inClass := false
	at := func(i int) string {
		if i < len(body) {
			return body[i : i+1]
		}
		return ""
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '\\' {
			n := at(i + 1)
			switch {
			case n >= "1" && n <= "9" && len(n) == 1:
				return e(`backreferences (\` + n + `) are not supported (RE2 has none)`)
			case n == "k":
				return e(`named backreferences (\k<…>) are not supported (RE2 has none)`)
			case n != "" && strings.Contains("AzZQEGhHRXCKNVoe", n):
				return e(`\` + n + ` is not supported by both RE2 and JavaScript: write it another way (e.g. ^ $ instead of \A \z)`)
			case n == "p" || n == "P":
				return e(`\p{…} Unicode classes are not supported here (JavaScript needs Unicode mode)`)
			case n == "x" && at(i+2) == "{":
				return e(`\x{…} is not supported by JavaScript; use \xHH or the character itself`)
			case n == "u":
				return e(`\uHHHH is not supported by RE2; use the character itself`)
			case n == "c":
				return e(`\c control escapes are not supported by RE2`)
			case n == "0":
				return e(`\0 is not supported by RE2; use \x00`)
			}
			i++
			continue
		}
		if inClass {
			if c == ']' {
				inClass = false
			}
			continue
		}
		if c == '[' {
			if strings.HasPrefix(body[i:], "[[:") || strings.HasPrefix(body[i:], "[^[:") || strings.HasPrefix(body[i+1:], "[:") {
				return e(`POSIX classes like [[:alpha:]] are not supported by JavaScript; use [a-zA-Z]`)
			}
			inClass = true
			if at(i+1) == "^" {
				i++
			}
			if at(i+1) == "]" {
				i++
			}
			continue
		}
		if c == '(' && at(i+1) == "?" {
			rest := body[i+2:]
			switch {
			case strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, "!"):
				return e(`lookahead (?= / (?! is not supported (RE2 has no lookaround)`)
			case strings.HasPrefix(rest, "<=") || strings.HasPrefix(rest, "<!"):
				return e(`lookbehind (?<= / (?<! is not supported (RE2 has no lookaround)`)
			case strings.HasPrefix(rest, ">"):
				return e(`atomic groups (?> are not supported (RE2)`)
			case strings.HasPrefix(rest, "P<"):
				return e(`write named groups as (?<name>…) (JavaScript does not know (?P<name>…))`)
			case strings.HasPrefix(rest, "<") || strings.HasPrefix(rest, ":"):
				continue
			}
			return e(`inline flags are not supported except a leading (?i) for the whole pattern`)
		}
		if (c == '+' || c == '*' || c == '?' || c == '}') && at(i+1) == "+" {
			return e(`possessive quantifiers (++ *+ ?+) are not supported (RE2)`)
		}
	}
	if msg := jsRegexScan(body); msg != "" {
		return e("not a valid regex: /" + body + "/" + flags + ": " + msg)
	}
	return nil
}

func jCheckRewrite(regex, replacement any, at string, is *Issues) {
	msg := "the regex is empty"
	if s, ok := regex.(string); ok {
		if e := CheckEdgeRegex(s); e != nil {
			msg = *e
		} else {
			msg = ""
		}
	}
	if msg != "" {
		is.Err(at+".regex", "V_EDGE_REGEX", msg)
	}
	if s, ok := replacement.(string); !ok || s == "" {
		is.Err(at+".replacement", "V_REQUIRED", "the rewrite needs a replacement (the new path, e.g. $1 or /x$request_uri)")
	} else if expr.IsExpr(s) {
		jCheckExprField(s, expr.FieldEdge, at+".replacement", is)
	}
}

func jCheckResponseHeaders(h any, at string, is *Issues) {
	if !doc.Truthy(h) || !jIsObj(h) {
		is.Err(at, "V_TYPE", "responseHeaders must be an object")
		return
	}
	var names []string
	for _, part := range []string{"set", "add"} {
		m := jGet(h, part)
		if !jPlainObj(m) {
			is.Err(at+"."+part, "V_TYPE", part+" must be an object of header → value")
			continue
		}
		for _, kv := range jObjEntries(m) {
			if !RE.HeaderName.MatchString(kv.k) {
				is.Err(at+"."+part, "V_FORMAT", `header name "`+kv.k+`" is not valid`)
			}
			if slices.ContainsFunc(names, func(x string) bool { return jLower(x) == jLower(kv.k) }) {
				is.Err(at+"."+part+"."+kv.k, "V_EDGE_HEADER", kv.k+" is in both set and add: use one")
			}
			names = append(names, kv.k)
			if s, ok := kv.v.(string); !ok {
				is.Err(at+"."+part+"."+kv.k, "V_TYPE", "header value must be a string")
			} else if expr.IsExpr(s) {
				jCheckExprField(s, expr.FieldEdge, at+"."+part+"."+kv.k, is)
			}
		}
	}
	if r, ok := jGet(h, "remove").([]any); !ok {
		is.Err(at+".remove", "V_TYPE", "remove must be a list")
	} else {
		for _, k := range r {
			if s, ok := k.(string); !ok || !RE.HeaderName.MatchString(s) {
				is.Err(at+".remove", "V_FORMAT", `header name "`+jStr(k)+`" is not valid`)
			}
		}
	}
	if r, ok := jGet(h, "always").([]any); !ok {
		is.Err(at+".always", "V_TYPE", "always must be a list")
	} else {
		for _, k := range r {
			if !slices.ContainsFunc(names, func(x string) bool { return jLower(x) == jLower(jStr(k)) }) {
				is.Err(at+".always", "V_EDGE_HEADER", `"always" lists `+jStr(k)+", which is not in set / add")
			}
		}
	}
}

func (g *jGlobals) validateEdge(edge any, b string, agentPort, aliasPort any, containers map[string]*jContainerInfo) {
	is := g.is
	httpPort, httpsPort := jGet(edge, "httpPort"), jGet(edge, "httpsPort")
	CheckPort(httpPort, b+".httpPort", is, 1)
	if httpsPort != nil {
		CheckPort(httpsPort, b+".httpsPort", is, 1)
		if jEq(httpPort, httpsPort) {
			is.Err(b+".httpsPort", "V_PORT_CONFLICT", "httpPort and httpsPort must differ")
		}
	}
	for _, kv := range []jKV{{"httpPort", httpPort}, {"httpsPort", httpsPort}} {
		if kv.v == nil {
			continue
		}
		if jEq(kv.v, agentPort) {
			is.Err(b+"."+kv.k, "V_PORT_CONFLICT", kv.k+" "+jStr(kv.v)+" is the agentPort")
		}
		if jEq(kv.v, aliasPort) {
			is.Err(b+"."+kv.k, "V_PORT_CONFLICT", kv.k+" "+jStr(kv.v)+" is the agentGuiAliasPort")
		}
	}
	hosts, ok := jGet(edge, "hosts").([]any)
	if !ok {
		is.Err(b+".hosts", "V_TYPE", "hosts must be a list")
		return
	}
	routes, ok := jGet(edge, "routes").([]any)
	if !ok {
		is.Err(b+".routes", "V_TYPE", "routes must be a list")
		return
	}
	if len(hosts) == 0 {
		is.Warn(b+".hosts", "V_EDGE_NO_HOSTS", "no hosts: every request gets 404 (HTTP) or a TLS failure")
	}
	if len(hosts) > LIMITS.Hosts {
		is.Err(b+".hosts", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.Hosts)+" hosts")
	}
	if len(routes) > LIMITS.Routes {
		is.Err(b+".routes", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.Routes)+" routes")
	}
	names := make([]string, len(hosts))
	for i, h := range hosts {
		n := jCoal(jGet(h, "hostname"), "")
		s, ok := n.(string)
		if !ok {
			jThrow(`(h.hostname ?? "").toLowerCase is not a function`)
		}
		names[i] = jLower(s)
	}
	hostAt := func(h any, i int) string {
		n := jGet(h, "hostname")
		if doc.Truthy(n) {
			return b + ".hosts[" + jStr(n) + "]"
		}
		return b + ".hosts[" + strconv.Itoa(i) + "]"
	}
	for i, h := range hosts {
		at := hostAt(h, i)
		n, ok := jGet(h, "hostname").(string)
		if !ok || n == "" {
			is.Err(at+".hostname", "V_REQUIRED", "hostname is empty")
			continue
		}
		if slices.Index(names, jLower(n)) != i {
			is.Err(at+".hostname", "V_DUPLICATE", "host "+n+" is listed two times")
		}
		if expr.IsExpr(n) {
			jCheckExprField(n, expr.FieldEdge, at+".hostname", is)
		} else if n != "*" && !jIsIPLiteral(n) && !RE.DNSName.MatchString(n) {
			is.Err(at+".hostname", "V_EDGE_HOST", `"`+n+`" is not a DNS name, a wildcard, an IP or "*"`)
		}
		t := jGet(h, "tls")
		mode := jOpt(t, "mode")
		if !doc.Truthy(t) || !jIsOneOf(mode, "none", "internal", "acme", "files") {
			is.Err(at+".tls.mode", "V_ENUM", "tls.mode must be none | internal | acme | files")
			continue
		}
		if jIs(mode, "acme") {
			if n == "*" || jIsIPLiteral(n) {
				is.Err(at+".tls", "V_EDGE_TLS", `ACME cannot issue a certificate for an IP or "*"; use internal`)
			}
			if s, ok := jGet(t, "email").(string); !ok || !RE.Email.MatchString(s) {
				is.Err(at+".tls.email", "V_FORMAT", "ACME needs an email")
			}
			ch := jGet(t, "challenge")
			if !jIsOneOf(ch, "http-01", "tls-alpn-01", "dns-01") {
				is.Err(at+".tls.challenge", "V_ENUM", "challenge must be http-01 | tls-alpn-01 | dns-01")
			}
			if strings.HasPrefix(n, "*.") && !jIs(ch, "dns-01") {
				is.Err(at+".tls.challenge", "V_EDGE_TLS", "a wildcard certificate needs the dns-01 challenge")
			}
			if jIs(ch, "dns-01") {
				if !doc.Truthy(jGet(t, "dnsProvider")) || !doc.Truthy(jGet(t, "dnsTokenSecretSlug")) {
					is.Err(at+".tls", "V_EDGE_TLS", "dns-01 needs dnsProvider and dnsTokenSecretSlug")
				} else if !jIncludes(g.secretSlugs, jGet(t, "dnsTokenSecretSlug")) {
					is.Err(at+".tls.dnsTokenSecretSlug", "V_REF", "secret "+jStr(jGet(t, "dnsTokenSecretSlug"))+" does not exist")
				}
			} else {
				need, port := 443.0, httpsPort
				if jIs(ch, "http-01") {
					need, port = 80, httpPort
				}
				is.Warn(at+".tls", "V_EDGE_ACME_INTERNET", jStr(ch)+" needs internet on this site AND "+n+" reachable from the internet on port "+jNum(need))
				if !jEq(port, need) {
					is.Err(at+".tls.challenge", "V_EDGE_TLS", jStr(ch)+" needs the edge on port "+jNum(need))
				}
			}
		}
		if jIs(mode, "files") {
			cert, key := jGet(t, "certFileSlug"), jGet(t, "keySecretSlug")
			if !doc.Truthy(cert) || !doc.Truthy(key) {
				is.Err(at+".tls", "V_EDGE_TLS", "files mode needs certFileSlug and keySecretSlug")
			}
			if doc.Truthy(cert) && !jIncludes(g.fileSlugs, cert) {
				is.Err(at+".tls.certFileSlug", "V_REF", "file "+jStr(cert)+" does not exist")
			}
			if doc.Truthy(key) && !jIncludes(g.secretSlugs, key) {
				is.Err(at+".tls.keySecretSlug", "V_REF", "secret "+jStr(key)+" does not exist")
			}
		}
		if !jIs(mode, "none") && httpsPort == nil {
			is.Warn(at+".tls", "V_EDGE_TLS_NO_HTTPS", "TLS is set but httpsPort is null (no HTTPS listener)")
		}
		rd := jGet(h, "redirectHttpToHttps")
		if b, ok := rd.(bool); !ok {
			is.Err(at+".redirectHttpToHttps", "V_TYPE", "must be true / false")
		} else if b && (jIs(mode, "none") || httpsPort == nil) {
			is.Err(at+".redirectHttpToHttps", "V_EDGE_TLS", "redirectHttpToHttps needs TLS and an httpsPort")
		}
	}
	if mb := jGet(edge, "maxBodyBytes"); !jNull(mb) {
		f, _ := mb.(float64)
		if !jIsInt(mb) || f < 0 || f > jEdgeMaxBody {
			is.Err(b+".maxBodyBytes", "V_EDGE_BODY", "maxBodyBytes must be null (unlimited) or 0–1 TiB in bytes")
		}
	}
	for i, h := range hosts {
		if rh := jGet(h, "responseHeaders"); !jNull(rh) {
			jCheckResponseHeaders(rh, hostAt(h, i)+".responseHeaders", is)
		}
	}
	ids := make([]any, len(routes))
	keys := make([]string, len(routes))
	for i, r := range routes {
		ids[i] = jGet(r, "routeId")
	}
	for i, r := range routes {
		m := jGet(r, "match")
		hl := []string{}
		for _, x := range jArr(jCoal(jGet(r, "hosts"), []any{}), "map", "(r.hosts ?? [])") {
			hl = append(hl, jLower(jStr(x)))
		}
		slices.SortFunc(hl, jCmp16)
		ml := jArr(jCoal(jOpt(m, "methods"), []any{}), "slice", "((intermediate value) ?? [])")
		ms := make([]any, len(ml))
		copy(ms, ml)
		slices.SortStableFunc(ms, func(a, b any) int { return jCmp16(jStr(a), jStr(b)) })
		keys[i] = doc.MarshalString([]any{jCoal(jOpt(m, "kind"), "prefix"), jOpt(m, "path"), jStrs(hl), ms})
	}
	for i, r := range routes {
		rid := jGet(r, "routeId")
		at := b + ".routes[" + jStr(rid) + "]"
		if s, ok := rid.(string); !ok || !RE.RouteID.MatchString(s) {
			is.Err(at+".routeId", "V_FORMAT", `route id "`+jStr(rid)+`" is not a slug (^[a-z0-9][a-z0-9-]{0,62}$)`)
		}
		eqID := func(x any) bool { return jEq(x, rid) }
		last := 0
		for k, x := range ids {
			if eqID(x) {
				last = k
			}
		}
		if a, isArr := rid.([]any); !(isArr && len(a) == 0) && slices.IndexFunc(ids, eqID) != last {
			is.Err(at+".routeId", "V_DUPLICATE", "route id "+jStr(rid)+" is used two times")
		}
		order := jGet(r, "order")
		if f, _ := order.(float64); !jIsInt(order) || f < 1 {
			is.Err(at+".order", "V_RANGE", "order must be an integer ≥ 1")
		}
		m := jGet(r, "match")
		kind, p := jOpt(m, "kind"), jOpt(m, "path")
		ps, pStr := p.(string)
		if !jIsOneOf(kind, "prefix", "exact", "regex") {
			is.Err(at+".match.kind", "V_ENUM", "match.kind must be prefix | exact | regex")
		} else if jIs(kind, "regex") {
			msg := "the regex is empty"
			if pStr {
				msg = ""
				if e := CheckEdgeRegex(ps); e != nil {
					msg = *e
				}
			}
			if msg != "" {
				is.Err(at+".match.path", "V_EDGE_REGEX", msg)
			}
		} else if !pStr || !strings.HasPrefix(ps, "/") || jRePathBad.MatchString(ps) {
			is.Err(at+".match.path", "V_EDGE_ROUTE", `the path must start with "/" and hold no spaces, "?" or "#"`)
		} else if ps == "/_opm" || strings.HasPrefix(ps, "/_opm/") {
			is.Err(at+".match.path", "V_RESERVED", "/_opm/ is reserved for the agent")
		}
		if stop, ok := jOpt(m, "stopRegex").(bool); !ok {
			is.Err(at+".match.stopRegex", "V_TYPE", "stopRegex must be true / false")
		} else if stop && !jIs(kind, "prefix") {
			is.Err(at+".match.stopRegex", "V_EDGE_ROUTE", `"stop regex" (nginx ^~) is only for prefix routes`)
		}
		if first := slices.Index(keys, keys[i]); first != i {
			is.Err(at+".match", "V_DUPLICATE", "route "+jStr(rid)+" has the same match as route "+jStr(jGet(routes[first], "routeId"))+" (it would never be used)")
		}
		if hl, ok := jGet(r, "hosts").([]any); !ok {
			is.Err(at+".hosts", "V_TYPE", "hosts must be a list")
		} else {
			for _, x := range hl {
				want := jLower(jStr(x))
				found := false
				for _, h := range hosts {
					hn := jGet(h, "hostname")
					s, ok := hn.(string)
					if !ok {
						jNotFn(hn, "toLowerCase", "h.hostname")
					}
					if jLower(s) == want {
						found = true
						break
					}
				}
				if !found {
					is.Err(at+".hosts", "V_REF", "host "+jStr(x)+" is not in edge.hosts")
				}
			}
		}
		if ms := jOpt(m, "methods"); !jNull(ms) {
			if a, ok := ms.([]any); !ok {
				is.Err(at+".match.methods", "V_TYPE", "methods must be null or a list")
			} else {
				for _, x := range a {
					if !jIsOneOf(x, EDGE_METHODS...) {
						is.Err(at+".match.methods", "V_ENUM", "method "+jStr(x)+" is not one of "+strings.Join(EDGE_METHODS, ", "))
					}
				}
			}
		}
		a := jGet(r, "action")
		atype := jOpt(a, "type")
		if !doc.Truthy(a) || !jIsOneOf(atype, "proxy", "redirect", "rewrite") {
			is.Err(at+".action.type", "V_ENUM", "action.type must be proxy | redirect | rewrite")
			continue
		}
		if rh := jGet(r, "responseHeaders"); !jNull(rh) {
			if jIs(atype, "rewrite") {
				is.Err(at+".responseHeaders", "V_EDGE_HEADER", "a rewrite route has no response of its own: set the headers on the route it rewrites to")
			} else {
				jCheckResponseHeaders(rh, at+".responseHeaders", is)
			}
		}
		switch {
		case jIs(atype, "proxy"):
			g.validateProxy(a, at, kind, containers)
		case jIs(atype, "redirect"):
			if s, ok := jGet(a, "to").(string); !ok || s == "" {
				is.Err(at+".action.to", "V_REQUIRED", `redirect needs "to"`)
			} else if expr.IsExpr(s) {
				jCheckExprField(s, expr.FieldEdge, at+".action.to", is)
			}
			if st, ok := jGet(a, "status").(float64); !ok || !slices.Contains([]float64{301, 302, 303, 307, 308}, st) {
				is.Err(at+".action.status", "V_ENUM", "status must be 301, 302, 303, 307 or 308")
			}
			if kp, ok := jGet(a, "keepPath").(bool); !ok {
				is.Err(at+".action.keepPath", "V_TYPE", "keepPath must be true / false")
			} else if kp && !jIs(kind, "prefix") {
				is.Err(at+".action.keepPath", "V_EDGE_ROUTE", `keep path needs a prefix route; use $uri / $1 in "to" for exact and regex routes`)
			}
		default:
			jCheckRewrite(jGet(a, "regex"), jGet(a, "replacement"), at+".action", is)
		}
	}
}

func (g *jGlobals) validateProxy(a any, at string, kind any, containers map[string]*jContainerInfo) {
	is := g.is
	cs := jGet(a, "containerSlug")
	pn := jGet(a, "portName")
	c := containers[jStr(cs)]
	if s, ok := cs.(string); !ok || s == "" {
		is.Err(at+".action.containerSlug", "V_REQUIRED", "pick the container this route proxies to")
	} else if c == nil {
		is.Err(at+".action.containerSlug", "V_REF", "container "+s+" does not exist on this site")
	} else if !jIncludes(c.portNames, pn) {
		if doc.Truthy(pn) {
			is.Err(at+".action.portName", "V_REF", "port "+jStr(pn)+" does not exist on the image of "+jStr(c.slug))
		} else {
			is.Err(at+".action.portName", "V_REQUIRED", "pick the port of "+jStr(c.slug)+" this route proxies to")
		}
	} else if !jIncludes(c.edgePorts, pn) {
		is.Err(at+".action.portName", "V_EDGE_ROUTE", "port "+jStr(pn)+" of container "+jStr(c.slug)+" is closed to the edge (ports[].edge = false)")
	} else if !doc.Truthy(c.enabled) {
		is.Warn(at+".action.containerSlug", "V_EDGE_TARGET_DISABLED", "container "+jStr(c.slug)+" is disabled: this route answers 503")
	}
	rng := func(k string, max int) {
		v := jGet(a, k)
		f, _ := v.(float64)
		if !jIsInt(v) || f < 1 || f > float64(max) {
			is.Err(at+".action."+k, "V_RANGE", k+" must be 1–"+strconv.Itoa(max)+" seconds")
		}
	}
	rng("connectTimeoutSec", jEdgeMaxConnectTimeout)
	rng("sendTimeoutSec", jEdgeMaxTimeoutSec)
	rng("readTimeoutSec", jEdgeMaxTimeoutSec)
	for _, k := range []string{"stripPrefix", "websocket", "requestBuffering", "responseBuffering"} {
		if _, ok := jGet(a, k).(bool); !ok {
			is.Err(at+".action."+k, "V_TYPE", k+" must be true / false")
		}
	}
	if mb := jGet(a, "maxBodyBytes"); mb != nil {
		f, _ := mb.(float64)
		if !jIsInt(mb) || f < 0 || f > jEdgeMaxBody {
			is.Err(at+".action.maxBodyBytes", "V_EDGE_BODY", "maxBodyBytes must be null (inherit the edge), 0 (unlimited) or up to 1 TiB in bytes")
		}
	}
	if ap := jGet(a, "addPrefix"); ap != nil {
		if s, ok := ap.(string); !ok || !strings.HasPrefix(s, "/") {
			is.Err(at+".action.addPrefix", "V_EDGE_ROUTE", `addPrefix must start with "/"`)
		}
	}
	strip := doc.Truthy(jGet(a, "stripPrefix"))
	if strip && jIs(kind, "regex") {
		is.Err(at+".action.stripPrefix", "V_EDGE_ROUTE", "strip prefix needs a prefix or exact route; use rewrite for a regex route")
	}
	if rw := jGet(a, "rewrite"); !jNull(rw) {
		if strip {
			is.Err(at+".action.rewrite", "V_EDGE_ROUTE", "use either strip prefix or rewrite, not both")
		}
		jCheckRewrite(jGet(rw, "regex"), jGet(rw, "replacement"), at+".action.rewrite", is)
	}
	for _, kv := range jObjEntries(jCoal(jGet(a, "setHeaders"), doc.New())) {
		k := kv.k
		if !RE.HeaderName.MatchString(k) {
			is.Err(at+".action.setHeaders", "V_FORMAT", `header name "`+k+`" is not valid`)
		} else if slices.ContainsFunc(jHopByHop, func(x string) bool { return jLower(x) == jLower(k) }) {
			is.Warn(at+".action.setHeaders."+k, "V_EDGE_HOP", k+" is managed by the edge (WebSocket upgrades and keep-alive work without it); the value is ignored")
		}
		s, ok := kv.v.(string)
		if !ok {
			is.Err(at+".action.setHeaders."+k, "V_TYPE", "header value must be a string")
		} else if expr.IsExpr(s) {
			jCheckExprField(s, expr.FieldEdge, at+".action.setHeaders."+k, is)
		}
		if ok && IsResponseOnlyHeader(k, s) {
			is.Warn(at+".action.setHeaders."+k, "W_RESPONSE_HEADER_AS_REQUEST", k+` is set as a REQUEST header (sent to the container), but it only means something on responses. For nginx add_header / proxy_hide_header use "Response headers → browser" (fix: moveToResponseHeaders).`)
		}
	}
	for _, k := range jIter(jCoal(jGet(a, "removeHeaders"), []any{}), "a.removeHeaders ?? []") {
		if !RE.HeaderName.MatchString(jStr(k)) {
			is.Err(at+".action.removeHeaders", "V_FORMAT", `header name "`+jStr(k)+`" is not valid`)
		}
	}
}

// ─────────────────────────────────────────── depends.ts: what validateSite uses

type jInferred struct {
	slug string
	from []string
}

type jDepends struct {
	slug                any
	explicit            []string
	implicit, suggested []jInferred
}

func jAddInferred(l *[]jInferred, slug, where string) {
	for i := range *l {
		if (*l)[i].slug == slug {
			if !slices.Contains((*l)[i].from, where) {
				(*l)[i].from = append((*l)[i].from, where)
			}
			return
		}
	}
	*l = append(*l, jInferred{slug, []string{where}})
}

var (
	jReURLBefore = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://(?:[^` + jWS + `/@"'` + "`" + `]*@)?$`)
	jReHostPort  = regexp.MustCompile(`^\d{1,5}(?:[^\d]|$)`)
	jReKeyBefore = regexp.MustCompile(`([A-Za-z0-9_]+)["']?[` + jWS + `]*[:=][` + jWS + `]*["']?$`)
	jReHostKey   = regexp.MustCompile(`(?i)(host|server|broker|addr|endpoint|upstream|url|uri|dsn)`)
	jReBlockAny  = regexp.MustCompile(`\{\{(?s:.*?)\}\}`)
)

func jHostChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// jLiteralHostMentions is literalHostMentions.
func jLiteralHostMentions(text string, slugs []string) []string {
	type hit struct {
		slug string
		pos  int
	}
	var found []hit
	if text == "" {
		return nil
	}
	done := map[string]bool{}
	for _, slug := range slugs {
		if slug == "" || done[slug] {
			continue
		}
		done[slug] = true
		for from := 0; ; {
			k := strings.Index(text[from:], slug)
			if k < 0 {
				break
			}
			i := from + k
			from = i + 1
			j := i + len(slug)
			if i > 0 && jHostChar(text[i-1]) {
				continue
			}
			if j < len(text) && text[j] != '.' && jHostChar(text[j]) {
				continue
			}
			if j+1 < len(text) && text[j] == '.' {
				if c := text[j+1]; c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
					continue
				}
			}
			before := text[strings.LastIndexByte(text[:i], '\n')+1 : i]
			url := jReURLBefore.MatchString(before)
			hostPort := j < len(text) && text[j] == ':' && jReHostPort.MatchString(text[j+1:])
			km := jReKeyBefore.FindStringSubmatch(before)
			if url || hostPort || (km != nil && jReHostKey.MatchString(km[1])) {
				found = append(found, hit{slug, i})
				break
			}
		}
	}
	sort.SliceStable(found, func(a, b int) bool {
		if found[a].pos != found[b].pos {
			return found[a].pos < found[b].pos
		}
		return found[a].slug < found[b].slug
	})
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = f.slug
	}
	return out
}

func jContainerRefs(value string) []string {
	if !strings.Contains(value, "{{") {
		return nil
	}
	refs, err := expr.ExprRefs(value)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range refs {
		if m := jReContainersRef.FindStringSubmatch(r); m != nil && !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

func jInferDepends(sc any, ctx *jVCtx, config *doc.Map) []jDepends {
	var cs []any
	var enabled []string // string slugs: a non-string one never matches a reference (and it hangs literalHostMentions in TS)
	for _, c := range jList(jGet(sc, "containers"), "(input.site.containers ?? [])") {
		if doc.Truthy(c) && doc.Truthy(jGet(c, "enabled")) {
			cs = append(cs, c)
			if s, ok := jGet(c, "containerSlug").(string); ok {
				enabled = append(enabled, s)
			}
		}
	}
	var out []jDepends
	for _, c := range cs {
		self := jGet(c, "containerSlug")
		explicit := []string{}
		for _, d := range jList(jGet(c, "depends"), "(c.depends ?? [])") {
			if s, ok := jGet(d, "containerSlug").(string); ok {
				explicit = append(explicit, s)
			}
		}
		var implicit, suggested []jInferred
		var others []string
		for _, s := range enabled {
			if !jIs(self, s) {
				others = append(others, s)
			}
		}
		scan := func(where, value string, exprs bool, literal string) {
			if exprs {
				for _, r := range jContainerRefs(value) {
					if !jIs(self, r) && slices.Contains(others, r) && !slices.Contains(explicit, r) {
						jAddInferred(&implicit, r, where)
					}
				}
			}
			for _, r := range jLiteralHostMentions(literal, others) {
				if !slices.Contains(explicit, r) {
					jAddInferred(&suggested, r, where)
				}
			}
		}
		noBlocks := func(s string) string { return jReBlockAny.ReplaceAllString(s, " ") }
		given := map[string]bool{}
		for _, e := range jList(jGet(c, "env"), "(c.env ?? [])") {
			if !doc.Truthy(e) {
				continue
			}
			n, ok := jGet(e, "name").(string)
			if !ok {
				continue
			}
			given[n] = true
		}
		// container env, in order, then the image defaults
		for _, e := range jList(jGet(c, "env"), "(c.env ?? [])") {
			if !doc.Truthy(e) {
				continue
			}
			n, ok := jGet(e, "name").(string)
			if !ok {
				continue
			}
			if v, ok := jGet(e, "value").(string); ok {
				scan("env "+n, v, true, n+"="+noBlocks(v))
			}
		}
		for _, raw := range jList(jOpt(ctx.image(c), "env"), "(input.images?.[…]?.env ?? [])") {
			if !doc.Truthy(raw) || !jIsObj(raw) {
				continue
			}
			n, ok := jGet(raw, "name").(string)
			if !ok || given[n] {
				continue
			}
			val := jGet(raw, "value")
			if a, isArr := val.([]any); isArr {
				val = jFind(a, func(x any) bool { _, s := x.(string); return s })
			}
			if v, ok := val.(string); ok {
				scan("image env "+n, v, true, n+"="+noBlocks(v))
			}
		}
		seen := map[string]bool{}
		for _, m := range jList(jGet(c, "volumes"), "(c.volumes ?? [])") {
			if !doc.Truthy(m) || !jIs(jOpt(jGet(m, "source"), "kind"), "file") {
				continue
			}
			slug := jStr(jGet(jGet(m, "source"), "fileSlug"))
			if seen[slug] {
				continue
			}
			seen[slug] = true
			f := jFind(config.Arr("files"), func(x any) bool { return jIs(jGet(x, "fileSlug"), slug) })
			text, ok := ctx.files.Val(slug).(string)
			if !doc.Truthy(f) || jIs(jGet(f, "editor"), "binary") || !ok {
				continue
			}
			if jIs(jFileRenderMode(f), "expressions") {
				scan("file "+slug, text, true, noBlocks(text))
			} else {
				scan("file "+slug, text, false, text)
			}
		}
		var kept []jInferred
		for _, s := range suggested {
			if !slices.ContainsFunc(implicit, func(d jInferred) bool { return d.slug == s.slug }) {
				kept = append(kept, s)
			}
		}
		out = append(out, jDepends{self, explicit, implicit, kept})
	}
	return out
}

// jStartCycle is startOrder(...).cycle (an undefined slug: Array.find cannot tell it from "no next container").
func jStartCycle(sc any, deps []jDepends) []any {
	var cs []any
	idx := map[string]bool{}
	for _, c := range jList(jGet(sc, "containers"), "(site.containers ?? [])") {
		if doc.Truthy(c) && doc.Truthy(jGet(c, "enabled")) {
			s := jGet(c, "containerSlug")
			if k := jKey(s); !idx[k] {
				idx[k] = true
				cs = append(cs, s)
			}
		}
	}
	edges, explicitOf := map[string]map[string]bool{}, map[string]map[string]bool{}
	for _, s := range cs {
		edges[jKey(s)], explicitOf[jKey(s)] = map[string]bool{}, map[string]bool{}
	}
	for _, d := range deps {
		me := jKey(d.slug)
		e := edges[me]
		if e == nil {
			continue
		}
		for _, x := range d.explicit {
			if k := jKey(x); idx[k] && k != me {
				e[k] = true
				explicitOf[me][k] = true
			}
		}
		for _, l := range [][]jInferred{d.implicit, d.suggested} {
			for _, x := range l {
				if k := jKey(x.slug); idx[k] && k != me {
					e[k] = true
				}
			}
		}
	}
	placed, cycle := map[string]bool{}, map[string]bool{}
	allPlaced := func(m map[string]bool) bool {
		for x := range m {
			if !placed[x] {
				return false
			}
		}
		return true
	}
	for len(placed) < len(cs) {
		var left []any
		for _, s := range cs {
			if !placed[jKey(s)] {
				left = append(left, s)
			}
		}
		found := func(m map[string]map[string]bool) (any, bool) {
			for _, s := range left {
				if allPlaced(m[jKey(s)]) {
					return s, s != doc.Undefined
				}
			}
			return nil, false
		}
		next, ok := found(edges)
		if !ok {
			for _, s := range left {
				cycle[jKey(s)] = true
			}
			if next, ok = found(explicitOf); !ok {
				next = left[0]
			}
		}
		placed[jKey(next)] = true
	}
	var out []any
	for _, s := range cs {
		if cycle[jKey(s)] {
			out = append(out, s)
		}
	}
	return out
}

// ─────────────────────────────────────────── JSON.parse error text (V_FILE_JSON)

func jJSONAt(msg string, u []rune, pos int) string {
	line, col := 1, 1
	for i := 0; i < pos && i < len(u); i++ {
		if u[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return msg + " in JSON at position " + strconv.Itoa(pos) + " (line " + strconv.Itoa(line) + " column " + strconv.Itoa(col) + ")"
}

// jJSONError is the message of JSON.parse(text), "" when it parses.
func jJSONError(text string) string {
	u := []rune(text)
	n := len(u)
	i := 0
	ws := func() {
		for i < n && (u[i] == ' ' || u[i] == '\t' || u[i] == '\n' || u[i] == '\r') {
			i++
		}
	}
	unexpected := func() string {
		if i >= n {
			return "Unexpected end of JSON input"
		}
		c := string(u[i])
		if n <= 40 {
			return "Unexpected token '" + c + `', "` + text + `" is not valid JSON`
		}
		lo, hi := max(i-10, 0), min(i+10, n)
		pre, post := "...", "..."
		if lo == 0 {
			pre = ""
		}
		if hi == n {
			post = ""
		}
		return "Unexpected token '" + c + "', " + pre + `"` + string(u[lo:hi]) + `"` + post + " is not valid JSON"
	}
	var value func() string
	str := func() string {
		i++
		for {
			if i >= n {
				return jJSONAt("Unterminated string", u, i)
			}
			c := u[i]
			switch {
			case c == '"':
				i++
				return ""
			case c < 0x20:
				return jJSONAt("Bad control character in string literal", u, i)
			case c == '\\':
				i++
				if i >= n {
					return jJSONAt("Unterminated string", u, i)
				}
				if u[i] == 'u' {
					for k := 1; k <= 4; k++ {
						if i+k >= n || !strings.ContainsRune("0123456789abcdefABCDEF", u[i+k]) {
							return jJSONAt("Bad Unicode escape", u, min(i+k, n))
						}
					}
					i += 5
					continue
				}
				if !strings.ContainsRune(`"\/bfnrt`, u[i]) {
					return jJSONAt("Bad escaped character", u, i)
				}
				i++
			default:
				i++
			}
		}
	}
	digit := func() bool { return i < n && u[i] >= '0' && u[i] <= '9' }
	num := func() string {
		if u[i] == '-' {
			i++
			if !digit() {
				return jJSONAt("No number after minus sign", u, i)
			}
		}
		if u[i] == '0' {
			i++
			if digit() {
				return jJSONAt("Unexpected number", u, i)
			}
		} else {
			for digit() {
				i++
			}
		}
		if i < n && u[i] == '.' {
			i++
			if !digit() {
				return jJSONAt("Unterminated fractional number", u, i)
			}
			for digit() {
				i++
			}
		}
		if i < n && (u[i] == 'e' || u[i] == 'E') {
			i++
			if i < n && (u[i] == '+' || u[i] == '-') {
				i++
			}
			if !digit() {
				return jJSONAt("Exponent part is missing a number", u, i)
			}
			for digit() {
				i++
			}
		}
		return ""
	}
	value = func() string {
		ws()
		if i >= n {
			return "Unexpected end of JSON input"
		}
		switch c := u[i]; {
		case c == '{':
			i++
			ws()
			if i < n && u[i] == '}' {
				i++
				return ""
			}
			first := true
			for {
				ws()
				if i >= n || u[i] != '"' {
					if first {
						return jJSONAt("Expected property name or '}'", u, i)
					}
					return jJSONAt("Expected double-quoted property name", u, i)
				}
				first = false
				if e := str(); e != "" {
					return e
				}
				ws()
				if i >= n || u[i] != ':' {
					return jJSONAt("Expected ':' after property name", u, i)
				}
				i++
				if e := value(); e != "" {
					return e
				}
				ws()
				if i < n && u[i] == '}' {
					i++
					return ""
				}
				if i >= n || u[i] != ',' {
					return jJSONAt("Expected ',' or '}' after property value", u, i)
				}
				i++
			}
		case c == '[':
			i++
			ws()
			if i < n && u[i] == ']' {
				i++
				return ""
			}
			for {
				if e := value(); e != "" {
					return e
				}
				ws()
				if i < n && u[i] == ']' {
					i++
					return ""
				}
				if i >= n || u[i] != ',' {
					return jJSONAt("Expected ',' or ']' after array element", u, i)
				}
				i++
			}
		case c == '"':
			return str()
		case c == '-' || c >= '0' && c <= '9':
			return num()
		}
		for _, w := range []string{"true", "false", "null"} {
			r := []rune(w)
			k := 0
			for k < len(r) && i+k < n && u[i+k] == r[k] {
				k++
			}
			if k == len(r) {
				i += k
				return ""
			}
			if k > 0 {
				i += k
				return unexpected()
			}
		}
		return unexpected()
	}
	if e := value(); e != "" {
		return e
	}
	ws()
	if i < n {
		return "Unexpected non-whitespace character after JSON at position " + strconv.Itoa(i) + func() string {
			s := jJSONAt("", u, i)
			return s[strings.LastIndex(s, " (line"):]
		}()
	}
	return ""
}
