package shared

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/expr"
)

// ─────────────────────────────────────────── validateConfig: doc-native port of validate.ts, so a malformed document
// gives the issues (or the TypeError safeValidate reports) of TS. A typed decode into model.Config cannot do that: the
// model refuses unknown enums and wrong JSON types the TS rules report as ordinary issues.

// jVCtx is ValidationContext.
type jVCtx struct {
	mode, deploymentID         string
	services, images           *doc.Map
	notFound, secretsSet       []any
	hasNotFound, hasSecretsSet bool
	detected, files, blobs     *doc.Map
	deferred                   bool
}

func jNewVCtx(ctx *doc.Map) *jVCtx {
	c := &jVCtx{mode: ctx.Str("mode"), deploymentID: ctx.Str("deploymentId"), services: ctx.Map("services"), images: ctx.Map("images"), detected: ctx.Map("detectedHosts"), files: ctx.Map("fileContents"), blobs: ctx.Map("blobContents")}
	c.notFound, c.hasNotFound = ctx.Val("notFoundPageIds").([]any)
	c.secretsSet, c.hasSecretsSet = ctx.Val("secretsSet").([]any)
	c.deferred = c.mode != "agent"
	return c
}

func (c *jVCtx) hasService(slug any) bool {
	k := jStr(slug)
	return jProtoKey(k) || doc.Truthy(c.images0(c.services, k))
}

func (c *jVCtx) images0(m *doc.Map, k string) any {
	v, ok := m.Get(k)
	if !ok {
		return doc.Undefined
	}
	return v
}

func (c *jVCtx) image(ct any) any {
	return c.images0(c.images, ImageKey(jStr(jGet(ct, "serviceSlug")), jStr(jGet(ct, "version"))))
}

func jOrderedDupes(items []any, key func(any) any) []any {
	seen, done := map[string]bool{}, map[string]bool{}
	var out []any
	for _, it := range items {
		v := key(it)
		k := jKey(v)
		if seen[k] && !done[k] && k != "e" {
			done[k] = true
			out = append(out, v)
		}
		seen[k] = true
	}
	return out
}

func jPlain(v any) any {
	switch x := v.(type) {
	case *doc.Map:
		if x == nil {
			return nil
		}
		m := make(map[string]any, x.Len())
		x.Each(func(k string, y any) { m[k] = jPlain(y) })
		return m
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = jPlain(y)
		}
		return out
	}
	if v == doc.Undefined {
		return nil
	}
	return v
}

func jEmptyVal(v any) bool { return jNull(v) || jIs(v, "") }

func jVarValues(config *doc.Map, deferred bool) map[string]any {
	out := map[string]any{}
	for _, v := range config.Arr("variables") {
		val := jGet(v, "value")
		k := jStr(jGet(v, "variableSlug"))
		if deferred && doc.Truthy(jGet(v, "required")) && jEmptyVal(val) {
			out[k] = expr.DeployPlaceholder
		} else {
			out[k] = jPlain(jCoal(val, nil))
		}
	}
	return out
}

func jSecretValues(config *doc.Map, c *jVCtx) map[string]*string {
	out := map[string]*string{}
	for _, s := range config.Arr("secrets") {
		slug := jGet(s, "secretSlug")
		var v any
		if c.hasSecretsSet {
			if jIncludes(c.secretsSet, slug) {
				v = MASK
			}
		} else {
			v = jCoal(jGet(s, "value"), nil)
		}
		if c.deferred && doc.Truthy(jGet(s, "required")) && (v == nil || jIs(v, "")) {
			v = expr.DeployPlaceholder
		}
		if v == nil {
			out[jStr(slug)] = nil
		} else {
			t := jStr(v)
			out[jStr(slug)] = &t
		}
	}
	return out
}

type jHost struct {
	host     *string
	err      *expr.Error
	auto     bool
	fallback *string
}

func jBaseCtx(deploymentID, slug string, host *string, vars map[string]any) *expr.Context {
	x := &expr.Context{Var: vars}
	x.Deployment.ID = deploymentID
	x.Self.Slug = slug
	x.Self.Host = host
	return x
}

func jExprErr(err error) *expr.Error {
	if e, ok := err.(*expr.Error); ok {
		return e
	}
	return &expr.Error{Code: expr.ESyntax, Message: err.Error()}
}

func jHostFallback(src string, vars map[string]any) *string {
	var names []string
	if refs, err := expr.ExprRefs(src); err == nil {
		for _, r := range refs {
			if n, ok := strings.CutPrefix(r, "var."); ok && jEmptyVal(vars[n]) {
				names = append(names, n)
			}
		}
	}
	what := "the host expression has no value"
	if len(names) > 0 {
		what = "var " + strings.Join(names, ", ") + " has no value"
	}
	s := what + " — leave host on auto or set the variable (the site uses its detected address)"
	return &s
}

func jResolveSiteHosts(config *doc.Map, c *jVCtx) map[string]jHost {
	vars := jVarValues(config, c.deferred)
	out := map[string]jHost{}
	for _, s := range config.Arr("sites") {
		slug := jStr(jGet(s, "siteSlug"))
		var auto *string
		if d, ok := c.detected.Val(slug).(string); ok {
			auto = &d
		} else if c.mode != "agent" {
			auto = expr.Str(expr.AutoHostPlaceholder(slug))
		}
		h := jOpt(jGet(s, "config"), "host")
		text, isStr := h.(string)
		if jNull(h) || (isStr && doc.JSTrim(text) == "") {
			out[slug] = jHost{host: auto, auto: true}
			continue
		}
		if a, ok := h.([]any); ok { // resolve() reads a non-string through Array.from
			text = jJoin(a, "")
		}
		r, err := expr.Resolve(text, jBaseCtx(c.deploymentID, slug, nil, vars), expr.FieldHost)
		switch {
		case err == nil && doc.JSTrim(r.Value) != "":
			out[slug] = jHost{host: expr.Str(r.Value)}
		case err == nil || jExprErr(err).Code == expr.ENull:
			out[slug] = jHost{host: auto, auto: true, fallback: jHostFallback(text, vars)}
		default:
			out[slug] = jHost{err: jExprErr(err)}
		}
	}
	return out
}

// jContainerPorts is containerPorts.
func jContainerPorts(c, img any) []*doc.Map {
	rows := jArrOf(jGet(c, "ports"))
	var out []*doc.Map
	for _, ip := range jArr(jGet(img, "ports"), "map", "img.ports") {
		r := jFind(rows, func(x any) bool { return doc.Truthy(x) && jEq(jGet(x, "portName"), jGet(ip, "name")) })
		out = append(out, doc.Obj("portName", jGet(ip, "name"), "hostPort", jCoal(jOpt(r, "hostPort"), nil), "hostIp", jCoal(jOpt(r, "hostIp"), "0.0.0.0"), "edge", jCoal(jOpt(r, "edge"), true), "protocol", jGet(ip, "protocol")))
	}
	return out
}

func jNumField(v any, x *expr.Context, f expr.Field) (*float64, error) {
	if v == doc.Undefined {
		v = true // not null, not a number, not an expression
	}
	n, err := expr.ResolveNumberField(v, x, f)
	if err != nil || n == nil {
		return nil, err
	}
	out := float64(*n)
	return &out, nil
}

func jSiteExprMap(config *doc.Map, c *jVCtx, hosts map[string]jHost) map[string]expr.SiteRef {
	vars := jVarValues(config, c.deferred)
	out := map[string]expr.SiteRef{}
	for _, s := range config.Arr("sites") {
		slug := jStr(jGet(s, "siteSlug"))
		port := map[string]map[string]*float64{}
		for _, ct := range jList(jGet(jGet(s, "config"), "containers"), "(s.config.containers ?? [])") {
			cs, isStr := jGet(ct, "serviceSlug"), false
			var cslug string
			if c.hasService(cs) {
				cslug, isStr = jGet(ct, "containerSlug").(string)
			}
			if !isStr {
				continue
			}
			var rows []any
			if img := c.image(ct); doc.Truthy(img) {
				for _, r := range jContainerPorts(ct, img) {
					rows = append(rows, r)
				}
			} else {
				rows = jArrOf(jGet(ct, "ports"))
			}
			m := map[string]*float64{}
			for _, p := range rows {
				var hp *float64
				if !jNull(p) {
					hp, _ = jNumField(jGet(p, "hostPort"), jBaseCtx(c.deploymentID, slug, hosts[slug].host, vars), expr.FieldHostPort)
				}
				m[jStr(jGet(p, "portName"))] = hp
			}
			port[cslug] = m
		}
		out[slug] = expr.SiteRef{Host: hosts[slug].host, Port: port}
	}
	return out
}

func jFindSite(config *doc.Map, slug string) any {
	return jFind(config.Arr("sites"), func(s any) bool { return jIs(jGet(s, "siteSlug"), slug) })
}

func jSiteContainersMap(config *doc.Map, c *jVCtx, siteSlug string) map[string]expr.ContainerPeer {
	out := map[string]expr.ContainerPeer{}
	site := jFindSite(config, siteSlug)
	if !doc.Truthy(site) {
		return out
	}
	vars := jVarValues(config, c.deferred)
	for _, ct := range jList(jGet(jGet(site, "config"), "containers"), "(site.config.containers ?? [])") {
		if !doc.Truthy(ct) {
			continue
		}
		slug, ok := jGet(ct, "containerSlug").(string)
		if !ok {
			continue
		}
		img := c.image(ct)
		port := map[string]*float64{}
		for _, p := range jList(jOpt(img, "ports"), "(img?.ports ?? [])") {
			var n *float64
			if f, ok := jGet(p, "port").(float64); ok {
				n = &f
			} else {
				n, _ = jNumField(jGet(p, "port"), jBaseCtx(c.deploymentID, "", nil, vars), expr.FieldImagePort)
			}
			port[jStr(jGet(p, "name"))] = n
		}
		out[slug] = expr.ContainerPeer{Host: slug, Enabled: !jEq(jGet(ct, "enabled"), false), Port: port}
	}
	return out
}

// jMount is EffectiveMount.
type jMount struct {
	key                    string
	imageVolume, mountPath any
	source                 any
	readOnly               bool
}

func jMountRO(row, src any) bool {
	return jIsOneOf(jGet(src, "kind"), "file", "secret") || doc.Truthy(jGet(row, "readOnly"))
}

func jContainerMounts(c, imgVolumes any) []jMount {
	var out []jMount
	vols := jArrOf(jGet(c, "volumes"))
	for _, iv := range jIter(imgVolumes, "img.volumes") {
		row := jFind(vols, func(v any) bool { return doc.Truthy(v) && jEq(jGet(v, "imageVolume"), jGet(iv, "name")) })
		if !doc.Truthy(row) || !doc.Truthy(jGet(row, "source")) || jIs(jGet(jGet(row, "source"), "kind"), "none") {
			continue
		}
		src := jGet(row, "source")
		out = append(out, jMount{"image:" + jStr(jGet(iv, "name")), jGet(iv, "name"), jCoal(jGet(row, "mountPath"), jGet(iv, "mountPath")), src, jMountRO(row, src)})
	}
	for i, v := range vols {
		if !doc.Truthy(v) || jGet(v, "imageVolume") != nil || !doc.Truthy(jGet(v, "source")) || jIs(jGet(jGet(v, "source"), "kind"), "none") {
			continue
		}
		src := jGet(v, "source")
		out = append(out, jMount{"volume[" + strconv.Itoa(i) + "]", nil, jCoal(jGet(v, "mountPath"), ""), src, jMountRO(v, src)})
	}
	return out
}

func jImgVolumes(img any) any {
	if doc.Truthy(img) {
		return jGet(img, "volumes")
	}
	return []any{}
}

func jContainerExprContext(config *doc.Map, c *jVCtx, siteSlug string, ct any, sm map[string]expr.SiteRef) *expr.Context {
	secret := jSecretValues(config, c)
	mounts := jContainerMounts(ct, jImgVolumes(c.image(ct)))
	file := map[string]expr.FileRef{}
	for _, f := range config.Arr("files") {
		var p *string
		for _, m := range mounts {
			if jIs(jGet(m.source, "kind"), "file") && jEq(jGet(m.source, "fileSlug"), jGet(f, "fileSlug")) {
				if s, ok := m.mountPath.(string); ok {
					p = &s
				}
				break
			}
		}
		file[jStr(jGet(f, "fileSlug"))] = expr.FileRef{Path: p}
	}
	x := jBaseCtx(c.deploymentID, siteSlug, sm[siteSlug].Host, jVarValues(config, c.deferred))
	x.Container = &expr.ContainerRef{Slug: jStr(jGet(ct, "containerSlug"))}
	x.Secret, x.File, x.Site = secret, file, sm
	x.Containers = jSiteContainersMap(config, c, siteSlug)
	return x
}

func jSiteFileContext(config *doc.Map, c *jVCtx, siteSlug string, sm map[string]expr.SiteRef) *expr.Context {
	x := jBaseCtx(c.deploymentID, siteSlug, sm[siteSlug].Host, jVarValues(config, c.deferred))
	x.Secret, x.Site = jSecretValues(config, c), sm
	x.Containers = jSiteContainersMap(config, c, siteSlug)
	return x
}

func jStrSrc(v any) *string {
	if jNull(v) {
		return nil
	}
	s, _ := v.(string) // Array.from(non-string): empty template
	return &s
}

func jSources(ct any, imgEnv, imgVolumes any, secrets map[string]*string) ([]expr.EnvSource, []expr.MountSource) {
	var env []expr.EnvSource
	given := map[string]bool{}
	rows := jArrOf(jGet(ct, "env"))
	for _, e := range rows {
		given[jKey(jGet(e, "name"))] = true
	}
	for _, e := range rows {
		name := jStr(jGet(e, "name"))
		if ref := jGet(e, "secretRef"); !jNull(ref) {
			env = append(env, expr.EnvSource{Name: name, Field: expr.FieldEnv, SecretRef: &expr.SecretRefVal{Value: secrets[jStr(ref)]}})
		} else {
			env = append(env, expr.EnvSource{Name: name, Src: jStrSrc(jGet(e, "value")), Field: expr.FieldEnv})
		}
	}
	for _, ie := range jIter(imgEnv, "img.env") {
		if !given[jKey(jGet(ie, "name"))] {
			env = append(env, expr.EnvSource{Name: jStr(jGet(ie, "name")), Src: jStrSrc(jGet(ie, "value")), Field: expr.FieldImageEnv})
		}
	}
	var mounts []expr.MountSource
	for _, m := range jContainerMounts(ct, imgVolumes) {
		ms := expr.MountSource{Key: m.key}
		ms.Src, _ = m.mountPath.(string)
		if jIs(jGet(m.source, "kind"), "file") {
			s, _ := jGet(m.source, "fileSlug").(string)
			ms.FileSlug = &s
		}
		mounts = append(mounts, ms)
	}
	return env, mounts
}

func jExprAt(e *expr.Error) string { return e.Message + " (at " + strconv.Itoa(e.Pos) + ")" }

// jTopo is topoOrder: the nodes in a cycle (nil = none).
func jTopo(nodes []any, dependsOn func(any) []any) []any {
	indeg := map[string]int{}
	users := map[string][]any{}
	for _, n := range nodes {
		indeg[jKey(n)] = 0
		users[jKey(n)] = []any{}
	}
	for _, n := range nodes {
		seen := map[string]bool{}
		for _, d := range dependsOn(n) {
			k := jKey(d)
			if seen[k] {
				continue
			}
			seen[k] = true
			if _, ok := indeg[k]; !ok || k == jKey(n) {
				continue
			}
			indeg[jKey(n)]++
			users[k] = append(users[k], n)
		}
	}
	less := func(a, b any) int {
		if jCmp16(jStr(a), jStr(b)) < 0 {
			return -1
		}
		return 1
	}
	var ready []any
	for _, n := range nodes {
		if indeg[jKey(n)] == 0 {
			ready = append(ready, n)
		}
	}
	slices.SortStableFunc(ready, less)
	done := map[string]bool{}
	count := 0
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		done[jKey(n)] = true
		count++
		for _, u := range users[jKey(n)] {
			indeg[jKey(u)]--
			if indeg[jKey(u)] == 0 {
				ready = append(ready, u)
				slices.SortStableFunc(ready, less)
			}
		}
	}
	if count == len(nodes) {
		return nil
	}
	cycle := []any{}
	for _, n := range nodes {
		if !done[jKey(n)] {
			cycle = append(cycle, n)
		}
	}
	return cycle
}

func jSiteCycle(config *doc.Map) []any {
	by := map[string]any{}
	var nodes []any
	for _, s := range config.Arr("sites") {
		by[jKey(jGet(s, "siteSlug"))] = s
	}
	for _, s := range config.Arr("sites") {
		nodes = append(nodes, jGet(s, "siteSlug"))
	}
	return jTopo(nodes, func(n any) []any {
		var out []any
		for _, d := range jArr(jGet(jGet(by[jKey(n)], "config"), "depends"), "map", "by.get(...).config.depends") {
			out = append(out, jGet(d, "siteSlug"))
		}
		return out
	})
}

func jContainerCycle(sc any) []any {
	by := map[string]any{}
	var nodes []any
	list := jArrOf(jGet(sc, "containers"))
	for _, c := range list {
		if doc.Truthy(jGet(c, "enabled")) {
			by[jKey(jGet(c, "containerSlug"))] = c
			nodes = append(nodes, jGet(c, "containerSlug"))
		}
	}
	return jTopo(nodes, func(n any) []any {
		var out []any
		for _, d := range jArr(jCoal(jGet(by[jKey(n)], "depends"), []any{}), "map", "(by.get(...).depends ?? [])") {
			out = append(out, jGet(d, "containerSlug"))
		}
		return out
	})
}

// ValidateConfig is validateConfig; ctx is the ValidationContext ({ mode, deploymentId, services, images, notFoundPageIds?,
// secretsSet?, detectedHosts?, fileContents?, blobContents? }). The error is the TS throw safeValidate reports as
// `malformed config (<msg>)`.
func ValidateConfig(config *doc.Map, ctx *doc.Map) (out *Issues, err error) {
	defer jCatch(&err)
	c := jNewVCtx(ctx)
	is := NewIssues(c.mode)
	template := c.mode == "template"
	if config == nil {
		is.Err("", "V_TYPE", "config must be an object")
		return is, nil
	}
	for _, k := range []string{"variables", "secrets", "files", "sites"} {
		if _, ok := config.Val(k).([]any); !ok {
			is.Err(k, "V_TYPE", k+" must be a list")
		}
	}
	if !is.OK() {
		return is, nil
	}
	vars, secrets, files, sites := config.Arr("variables"), config.Arr("secrets"), config.Arr("files"), config.Arr("sites")
	if len(vars) > LIMITS.Variables {
		is.Err("variables", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.Variables))
	}
	if len(secrets) > LIMITS.Secrets {
		is.Err("secrets", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.Secrets))
	}
	if len(files) > LIMITS.Files {
		is.Err("files", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.Files))
	}
	if len(sites) > LIMITS.Sites {
		is.Err("sites", "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.Sites))
	}
	field := func(k string) func(any) any { return func(v any) any { return jGet(v, k) } }
	for _, d := range jOrderedDupes(vars, field("variableSlug")) {
		is.Err("variables["+jStr(d)+"]", "V_DUPLICATE", "variable "+jStr(d)+" is defined twice")
	}
	for _, d := range jOrderedDupes(secrets, field("secretSlug")) {
		is.Err("secrets["+jStr(d)+"]", "V_DUPLICATE", "secret "+jStr(d)+" is defined twice")
	}
	for _, d := range jOrderedDupes(files, field("fileSlug")) {
		is.Err("files["+jStr(d)+"]", "V_DUPLICATE", "file "+jStr(d)+" is defined twice")
	}
	for _, d := range jOrderedDupes(sites, field("siteSlug")) {
		is.Err("sites["+jStr(d)+"]", "V_DUPLICATE", "site "+jStr(d)+" is defined twice")
	}
	for _, v := range vars {
		jValidateVariable(v, "variables["+jStr(jGet(v, "variableSlug"))+"]", is, c.mode)
	}
	for _, s := range secrets {
		base := "secrets[" + jStr(jGet(s, "secretSlug")) + "]"
		if c.hasSecretsSet && !template {
			var val any
			if jIncludes(c.secretsSet, jGet(s, "secretSlug")) {
				val = "x"
			}
			jValidateSecretDef(jSpread(doc.New(), s).Set("value", val), base, is, false)
		} else {
			jValidateSecretDef(s, base, is, template)
		}
	}
	for _, f := range files {
		jValidateFileDef(f, "files["+jStr(jGet(f, "fileSlug"))+"]", is)
	}
	if len(sites) == 0 {
		is.Err("sites", "V_NO_SITES", "a deployment needs at least one site", "publish")
	}
	g := &jGlobals{config: config, ctx: c, is: is}
	for _, s := range sites {
		g.siteSlugs = append(g.siteSlugs, jGet(s, "siteSlug"))
	}
	for _, f := range files {
		g.fileSlugs = append(g.fileSlugs, jGet(f, "fileSlug"))
	}
	for _, s := range secrets {
		g.secretSlugs = append(g.secretSlugs, jGet(s, "secretSlug"))
	}
	g.hosts = jResolveSiteHosts(config, c)
	g.siteMap = jSiteExprMap(config, c, g.hosts)
	if cycle := jSiteCycle(config); cycle != nil {
		is.Err("sites", "V_CYCLE", "site depends form a cycle: "+jJoin(cycle, " → "))
	}
	for _, s := range sites {
		g.validateSite(s)
		jValidateSiteDevTools(s, config, c, is)
	}
	return is, nil
}

// ─────────────────────────────────────────── variables, secrets, files

func jToNum(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case nil:
		return 0
	case []any:
		return jToNum(jJoin(x, ","))
	case string:
		s := doc.JSTrim(x)
		if s == "" {
			return 0
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return math.NaN()
}

func jValueMatches(v any, value any) bool {
	if value == nil {
		return true
	}
	switch t := jGet(v, "type"); {
	case jIs(t, "string"):
		_, ok := value.(string)
		return ok
	case jIs(t, "number"):
		f, ok := value.(float64)
		return ok && f-f == 0
	case jIs(t, "boolean"):
		_, ok := value.(bool)
		return ok
	case jIs(t, "enum"):
		s, ok := value.(string)
		if !ok {
			return false
		}
		switch o := jGet(v, "options").(type) {
		case []any:
			return jIncludes(o, s)
		case string:
			return o != "" && strings.Contains(o, s)
		default:
			if !doc.Truthy(o) {
				return false
			}
			jThrow("v.options.includes is not a function")
		}
	case jIs(t, "list"):
		a, ok := value.([]any)
		return ok && !slices.ContainsFunc(a, func(x any) bool { _, s := x.(string); return !s })
	case jIs(t, "json"):
		return jIsObj(value)
	}
	return false
}

func jVariablePatternMatch(pattern, value string) (bool, bool) {
	re, err := regexp.Compile(`^(?:` + pattern + `)$`)
	if err != nil {
		return false, false
	}
	return re.MatchString(value), true
}

func jValidateVariable(v any, base string, is *Issues, mode string) {
	slug := jGet(v, "variableSlug")
	CheckSlug(slug, base+".variableSlug", is)
	CheckLabel(jGet(v, "label"), base+".label", is, 0)
	CheckDescription(jGet(v, "description"), base+".description", is)
	typ := jGet(v, "type")
	if !jIsOneOf(typ, "string", "number", "boolean", "enum", "list", "json") {
		is.Err(base+".type", "V_ENUM", "type must be string | number | boolean | enum | list | json")
		return
	}
	required := jGet(v, "required")
	if _, ok := required.(bool); !ok {
		is.Err(base+".required", "V_TYPE", "required must be true / false")
	}
	opts := jGet(v, "options")
	if jIs(typ, "enum") {
		a, ok := opts.([]any)
		if !ok || len(a) < 1 || len(a) > LIMITS.EnumOptions || slices.ContainsFunc(a, func(x any) bool { _, s := x.(string); return !s }) {
			is.Err(base+".options", "V_LENGTH", "enum needs 1–"+strconv.Itoa(LIMITS.EnumOptions)+" string options")
		} else if len(jOrderedDupes(a, func(x any) any { return x })) > 0 {
			is.Err(base+".options", "V_DUPLICATE", "options must be unique")
		}
	} else if !jNull(opts) {
		is.Err(base+".options", "V_TYPE", "options are only for type enum (use null)")
	}
	val := jGet(v, "validation")
	if !jNull(val) {
		min, max, pat := jGet(val, "min"), jGet(val, "max"), jGet(val, "pattern")
		if (min != nil || max != nil) && !jIs(typ, "number") {
			is.Err(base+".validation", "V_TYPE", "min / max are only for type number")
		}
		if pat != nil && !jIs(typ, "string") {
			is.Err(base+".validation", "V_TYPE", "pattern is only for type string")
		}
		fmin, minNum := min.(float64)
		fmax, maxNum := max.(float64)
		if min != nil && !minNum {
			is.Err(base+".validation.min", "V_TYPE", "min must be a number or null")
		}
		if max != nil && !maxNum {
			is.Err(base+".validation.max", "V_TYPE", "max must be a number or null")
		}
		if minNum && maxNum && fmin > fmax {
			is.Err(base+".validation", "V_RANGE", "min > max")
		}
		if pat != nil {
			CheckRe2(pat, base+".validation.pattern", is)
		}
	}
	value := jGet(v, "value")
	if !jValueMatches(v, value) {
		suffix := ""
		if jIs(typ, "enum") {
			suffix = " (one of " + jJoin(jArr(jCoal(opts, []any{}), "join", "(v.options ?? [])"), ", ") + ")"
		}
		is.Err(base+".value", "V_VALUE_TYPE", "value does not match type "+jStr(typ)+suffix)
		return
	}
	if value != nil && doc.Truthy(val) {
		if jIs(typ, "number") {
			n := jToNum(value)
			if min := jGet(val, "min"); min != nil && n < jToNum(min) {
				is.Err(base+".value", "V_VALUE_RULE", "value must be ≥ "+jStr(min))
			}
			if max := jGet(val, "max"); max != nil && n > jToNum(max) {
				is.Err(base+".value", "V_VALUE_RULE", "value must be ≤ "+jStr(max))
			}
		}
		if p, ok := jGet(val, "pattern").(string); jIs(typ, "string") && ok && p != "" {
			if s, isStr := value.(string); isStr && jsRegexError(`^(?:`+p+`)$`) == "" {
				if m, known := jVariablePatternMatch(p, s); known && !m {
					is.Err(base+".value", "V_VALUE_RULE", "value does not match "+p)
				}
			}
		}
	}
	if s, ok := value.(string); ok && jIs(typ, "string") && doc.JSLen(s) > LIMITS.Value {
		is.Err(base+".value", "V_LENGTH", "at most "+strconv.Itoa(LIMITS.Value)+" characters")
	}
	empty := value == nil || jIs(value, "")
	if doc.Truthy(required) && empty && mode != "template" {
		is.Err(base+".value", "V_VAR_REQUIRED", jStr(slug)+" is required — set it before publishing", "publish")
	}
	if !doc.Truthy(required) && empty && mode == "template" {
		is.Info(base+".value", "V_VAR_OPTIONAL_NULL", "optional var "+jStr(slug)+" has no value; add | default(…) where it is used, or a template default")
	}
}

func jValidateSecretDef(s any, base string, is *Issues, template bool) {
	slug := jGet(s, "secretSlug")
	CheckSlug(slug, base+".secretSlug", is)
	CheckLabel(jGet(s, "label"), base+".label", is, 0)
	CheckDescription(jGet(s, "description"), base+".description", is)
	required := jGet(s, "required")
	if _, ok := required.(bool); !ok {
		is.Err(base+".required", "V_TYPE", "required must be true / false")
	}
	if gen := jGet(s, "generate"); !jNull(gen) {
		if !jIsOneOf(jGet(gen, "charset"), "alnum", "hex", "base64url") {
			is.Err(base+".generate.charset", "V_ENUM", "charset must be alnum | hex | base64url")
		}
		l := jGet(gen, "length")
		f, _ := l.(float64)
		if !jIsInt(l) || f < 8 || f > 128 {
			is.Err(base+".generate.length", "V_RANGE", "length must be 8–128")
		}
	}
	value := jGet(s, "value")
	if template && !jNull(value) {
		is.Err(base+".value", "V_TEMPLATE_SECRET_VALUE", "templates never hold secret values")
	}
	if t, ok := value.(string); ok && len(t) > LIMITS.SecretBytes {
		is.Err(base+".value", "V_LENGTH", "at most "+strconv.Itoa(LIMITS.SecretBytes)+" bytes")
	}
	if !template && doc.Truthy(required) && jEmptyVal(value) {
		is.Err(base+".value", "V_SECRET_REQUIRED", "secret "+jStr(slug)+" is required — set it before publishing", "publish")
	}
}

func jValidateFileDef(f any, base string, is *Issues) {
	CheckSlug(jGet(f, "fileSlug"), base+".fileSlug", is)
	CheckLabel(jGet(f, "label"), base+".label", is, 0)
	CheckDescription(jGet(f, "description"), base+".description", is)
	if m, ok := jGet(f, "mime").(string); !ok || !RE.Mime.MatchString(m) {
		is.Err(base+".mime", "V_FORMAT", "mime must be type/subtype")
	}
	editor, render := jGet(f, "editor"), jGet(f, "render")
	if !jIsOneOf(editor, "text", "binary") {
		is.Err(base+".editor", "V_ENUM", "editor must be text | binary")
	}
	if render != doc.Undefined && !jIsOneOf(render, "expressions", "none") {
		is.Err(base+".render", "V_ENUM", "render must be expressions | none")
	} else if jIs(editor, "binary") && jIs(render, "expressions") {
		is.Err(base+".render", "V_ENUM", "binary files are mounted as they are (render none)")
	}
	mb := jGet(f, "maxBytes")
	fmb, _ := mb.(float64)
	if !jIsInt(mb) || fmb < 1 || fmb > float64(LIMITS.FileMaxBytes) {
		is.Err(base+".maxBytes", "V_RANGE", "maxBytes must be 1–"+strconv.Itoa(LIMITS.FileMaxBytes))
	}
	jCheckLocation(jGet(f, "location"), "files", base+".location", is)
	if c := jGet(f, "content"); !jNull(c) {
		if s, ok := jGet(c, "sha256").(string); !ok || !RE.Sha256.MatchString(s) {
			is.Err(base+".content.sha256", "V_FORMAT", `sha256 must be "sha256:" + 64 hex`)
		}
		sz := jGet(c, "sizeBytes")
		fsz, _ := sz.(float64)
		if !jIsInt(sz) || fsz < 0 {
			is.Err(base+".content.sizeBytes", "V_RANGE", "sizeBytes must be ≥ 0")
		} else if jIsInt(mb) && fsz > fmb {
			is.Err(base+".content", "V_FILE_CONTENT", "content is "+jNum(fsz)+" bytes; maxBytes is "+jNum(fmb))
		}
	}
}

var jAgentHomeReserved = []string{"site.key", "opm.db", "opm.db-wal", "opm.db-shm", "blobs", "snapshots", "certs", "edge", "transfers", "tunnel", "logs", "notfound", "rendered", "secrets", "vol", "files", "volumes", "shared-files"}

// CheckLocation is checkLocation.
func CheckLocation(l any, kind, path string, is *Issues) { jCheckLocation(l, kind, path, is) }

func jCheckLocation(l any, kind, path string, is *Issues) {
	if jNull(l) {
		return
	}
	k := jGet(l, "kind")
	if !jIsObj(l) || !jIsOneOf(k, "auto", "path") {
		is.Err(path, "V_LOCATION", `location must be { kind: "auto" } or { kind: "path", path }`)
		return
	}
	if jIs(k, "auto") {
		return
	}
	p, ok := jGet(l, "path").(string)
	if !ok || p == "" || expr.IsExpr(p) {
		is.Err(path+".path", "V_LOCATION", `a custom location needs a path ("./…" = relative to the agent home, or an absolute path); no {{ }}`)
		return
	}
	if rest, rel := strings.CutPrefix(p, "./"); rel {
		segs := strings.Split(rest, "/")
		if slices.ContainsFunc(segs, func(g string) bool { return g == "" || g == "." || g == ".." || jHasSpace(g) }) {
			is.Err(path+".path", "V_LOCATION", `"`+p+`": a "./" path is relative to the agent home: no empty, ".", ".." segments or spaces`)
			return
		}
		if slices.Contains(jAgentHomeReserved, segs[0]) && segs[0] != kind {
			is.Err(path+".path", "V_LOCATION", `"`+p+`": ./`+segs[0]+` belongs to the agent`)
			return
		}
		if segs[0] == kind && len(segs) < 2 {
			is.Err(path+".path", "V_LOCATION", `"`+p+`": pick a name inside ./`+kind+`/`)
		}
	} else if !IsSafeMountPath(p) {
		is.Err(path+".path", "V_LOCATION", `"`+p+`" must start with "./" (agent home) or be an absolute path (not "/", no spaces, no "..")`)
	}
}

func jLocationPath(l any, kind, slug string) any {
	if !doc.Truthy(l) || !jIs(jGet(l, "kind"), "path") {
		return "./" + kind + "/" + slug
	}
	return jGet(l, "path")
}

func jTrimSlash(s string) string { return strings.TrimRight(s, "/") }

func jNestedOrSame(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// ─────────────────────────────────────────── volume seeds

// SeedPathError is seedPathError (nil = valid).
func SeedPathError(p any) *string {
	e := func(s string) *string { return &s }
	s, ok := p.(string)
	if !ok || s == "" {
		return e("enter a path inside the volume, e.g. flows.json or lib/settings.js")
	}
	if doc.JSLen(s) > 255 {
		return e("at most 255 characters")
	}
	if strings.HasPrefix(s, "/") {
		return e(`the path is relative to the volume: no leading "/"`)
	}
	if !jReSeedPath.MatchString(s) {
		return e(`letters, digits, . _ - and "/" between folders only`)
	}
	if slices.ContainsFunc(strings.Split(s, "/"), func(x string) bool { return x == "." || x == ".." }) {
		return e(`"." and ".." are not allowed`)
	}
	return nil
}

var (
	jReSeedPath = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+/)*[A-Za-z0-9._-]+$`)
	jReTarName  = regexp.MustCompile(`(?i)\.(tar\.gz|tgz)$`)
)

// jContentRenders is ContentRenders: sha256 → where, in first-seen order.
type jContentRenders struct {
	order []string
	where map[string][]string
}

func (r *jContentRenders) add(sha, at string) {
	if _, ok := r.where[sha]; !ok {
		r.order = append(r.order, sha)
	}
	r.where[sha] = append(r.where[sha], at)
}

func jCheckBackupPolicy(v any, vp string, is *Issues) {
	p := jGet(v, "backupPolicy")
	if p == doc.Undefined {
		return
	}
	at := vp + ".backupPolicy"
	if !jPlainObj(p) {
		is.Err(at, "V_TYPE", "backupPolicy must be absent (the daily site snapshot) or { schedule, keep }")
		return
	}
	sched, keep := jGet(p, "schedule"), jGet(p, "keep")
	if !jIsOneOf(sched, VOLUME_BACKUP_SCHEDULES...) {
		is.Err(at+".schedule", "V_ENUM", "schedule must be off, hourly, 6h, daily or weekly")
	}
	f, _ := keep.(float64)
	if !jIs(sched, "off") && (!jIsInt(keep) || f < 1 || f > 100) {
		is.Err(at+".keep", "V_RANGE", "keep must be 1 … 100 (got "+jStr(keep)+")")
	}
	if !jIs(sched, "off") && !doc.Truthy(jGet(v, "backup")) {
		is.Warn(at, "V_VOLUME_BACKUP_OFF", "volume "+jStr(jGet(v, "volumeSlug"))+" has backup off: its automatic backups do not run")
	}
}

func jCheckPersist(v any, vp string, is *Issues) {
	p := jGet(v, "persist")
	if p == doc.Undefined {
		return
	}
	at := vp + ".persist"
	if !jPlainObj(p) {
		is.Err(at, "V_TYPE", "persist must be absent (off) or { exclude, maxBytes, auto }")
		return
	}
	L := VOLUME_CONTENT_LIMITS
	if !jIs(jCoal(jGet(v, "kind"), "dir"), "dir") {
		is.Err(at, "V_VOLUME_PERSIST", "volume "+jStr(jGet(v, "volumeSlug"))+" is a single file: persist to cloud needs a folder volume")
	}
	if ex, ok := jGet(p, "exclude").([]any); !ok {
		is.Err(at+".exclude", "V_TYPE", "exclude must be a list of patterns")
	} else {
		if len(ex) > L.ExcludeMax {
			is.Err(at+".exclude", "V_TOO_MANY", "at most "+strconv.Itoa(L.ExcludeMax)+" exclude patterns")
		}
		for i, pat := range ex {
			if e := ExcludePatternError(pat); e != nil {
				is.Err(at+".exclude["+strconv.Itoa(i)+"]", "V_VOLUME_PERSIST", *e)
			}
		}
		for _, d := range jOrderedDupes(ex, func(q any) any { return q }) {
			is.Err(at+".exclude", "V_DUPLICATE", "exclude pattern "+jStr(d)+" is listed twice")
		}
	}
	mb := jGet(p, "maxBytes")
	f, _ := mb.(float64)
	if !jIsInt(mb) || f < float64(L.PersistMinBytes) || f > float64(L.PersistMaxBytes) {
		is.Err(at+".maxBytes", "V_RANGE", "the snapshot size cap is "+strconv.Itoa(L.PersistMinBytes/1048576)+" – "+strconv.Itoa(L.PersistMaxBytes/1048576)+" MiB")
	}
	if _, ok := jGet(p, "auto").(bool); !ok {
		is.Err(at+".auto", "V_TYPE", "auto must be true / false")
	}
	for _, k := range p.(*doc.Map).Keys() {
		if k != "exclude" && k != "maxBytes" && k != "auto" {
			is.Err(at+"."+k, "V_FORMAT", "persist has no field "+k)
		}
	}
}

func jCheckPersistContent(v, sd, persist any, vp string, is *Issues) {
	total := 0.0
	vs := jStr(jGet(v, "volumeSlug"))
	entries := jList(jGet(sd, "entries"), "sd.entries ?? []")
	for _, e := range entries {
		if s := jOpt(e, "size"); jIsInt(s) {
			total += s.(float64)
		}
	}
	mb := jGet(persist, "maxBytes")
	if f, _ := mb.(float64); jIsInt(mb) && total > f {
		is.Err(vp+".seed", "V_TOO_LARGE", "volume "+vs+": the content is "+FormatContentBytes(total)+" — at most "+FormatContentBytes(f)+" (Persist to cloud › size cap)")
	}
	ex, ok := jGet(persist, "exclude").([]any)
	if !ok || slices.ContainsFunc(ex, func(p any) bool { return ExcludePatternError(p) != nil }) {
		return
	}
	compiled := CompileExcludes(strsAny(ex))
	for _, e := range entries {
		if p, ok := jOpt(e, "path").(string); ok && IsExcluded(p, false, compiled) {
			is.Err(vp+".seed.entries["+p+"]", "V_VOLUME_CONTENT_EXCLUDED", "volume "+vs+": "+p+" is excluded from the persisted content (it could never come back from the site)")
		}
	}
	for _, d := range jList(jGet(sd, "dirs"), "sd.dirs ?? []") {
		if p, ok := d.(string); ok && IsExcluded(p, true, compiled) {
			is.Err(vp+".seed.entries["+p+"]", "V_VOLUME_CONTENT_EXCLUDED", "volume "+vs+": folder "+p+" is excluded from the persisted content")
		}
	}
}

func strsAny(a []any) []string {
	out := make([]string, 0, len(a))
	for _, x := range a {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (g *jGlobals) checkSeed(v any, vp string, renders *[]string, content *jContentRenders) {
	is := g.is
	jCheckPersist(v, vp, is)
	jCheckBackupPolicy(v, vp, is)
	sd := jGet(v, "seed")
	if jNull(sd) {
		return
	}
	kd := jOpt(sd, "kind")
	if !jIsObj(sd) || !jIsOneOf(jCoal(kd, ""), "tree", "single", "files", "file", "archive") {
		is.Err(vp+".seed", "V_VOLUME_SEED", "seed must be null, tree, single or archive")
		return
	}
	k := kd.(string)
	kind := jCoal(jGet(v, "kind"), "dir")
	vs := jStr(jGet(v, "volumeSlug"))
	if (k == "files" || k == "file") && is.Mode != "agent" {
		var from string
		if k == "file" {
			from = jStr(jGet(sd, "fileSlug"))
		} else {
			var names []any
			for _, e := range jArr(jGet(sd, "entries"), "map", "sd.entries") {
				names = append(names, jGet(e, "fileSlug"))
			}
			from = jJoin(names, ", ")
		}
		is.Err(vp+".seed", "V_VOLUME_SEED_FILE_REF", "volume "+vs+" takes its starter content from Files ("+from+") — Files are static and read-only and are never volume content: give the volume its own content (Volume → Content; backend/scripts/migrate-h.ts converts it)")
		return
	}
	var persist any
	if p := jGet(v, "persist"); doc.Truthy(p) && jIsObj(p) {
		persist = p
	}
	if persist != nil && k != "tree" {
		is.Err(vp+".seed", "V_VOLUME_SEED", "volume "+vs+" persists to the cloud: its content is a file tree (kind tree)")
		return
	}
	if k == "tree" {
		if !jIs(kind, "dir") {
			is.Err(vp+".seed", "V_VOLUME_SEED", "a single-file volume has one file of content (kind single), not a tree")
			return
		}
		list, err := VolumeTreeIssues(sd, persist == nil)
		if err != nil {
			jThrow("%s", err.Error())
		}
		for _, x := range list {
			m := x.(*doc.Map)
			at, code := vp+".seed", m.Str("code")
			if m.Str("path") != "" {
				at = vp + ".seed.entries[" + m.Str("path") + "]"
			}
			if code == "V_VOLUME_CONTENT" {
				code = "V_VOLUME_SEED"
			}
			is.Err(at, code, "volume "+vs+": "+m.Str("message"))
		}
		if persist != nil {
			jCheckPersistContent(v, sd, persist, vp, is)
		} else {
			for _, e := range jList(jGet(sd, "entries"), "sd.entries ?? []") {
				if s, ok := jOpt(e, "sha256").(string); ok && jIs(jOpt(e, "render"), "expressions") {
					content.add(s, vp+".seed.entries["+jStr(jGet(e, "path"))+"]")
				}
			}
		}
		return
	}
	if k == "single" {
		if !jIs(kind, "file") {
			is.Err(vp+".seed", "V_VOLUME_SEED", "a folder volume has a tree of content (kind tree), not a single file")
			return
		}
		at := vp + ".seed"
		sha, shaOK := jGet(sd, "sha256").(string)
		if !shaOK || !RE.Sha256.MatchString(sha) {
			is.Err(at+".sha256", "V_FORMAT", "upload the content")
		}
		sz := jGet(sd, "size")
		f, _ := sz.(float64)
		if !jIsInt(sz) || f < 0 || f > float64(VOLUME_CONTENT_LIMITS.FileMaxBytes) {
			is.Err(at+".size", "V_TOO_LARGE", "the file is 0 B – "+strconv.Itoa(VOLUME_CONTENT_LIMITS.FileMaxBytes/1048576)+" MiB")
		}
		if m, ok := jGet(sd, "mode").(string); !ok || !RE_FILE_MODE.MatchString(m) {
			is.Err(at+".mode", "V_FORMAT", "mode must look like 0644")
		}
		if r := jGet(sd, "render"); !jIsOneOf(r, "none", "expressions") {
			is.Err(at+".render", "V_ENUM", "render must be none | expressions")
		} else if jIs(r, "expressions") && shaOK {
			content.add(sha, at)
		}
		return
	}
	if k == "file" && !jIs(kind, "file") {
		is.Err(vp+".seed", "V_VOLUME_SEED", "a folder volume starts from files or an archive (a single file seed is for single-file volumes)")
		return
	}
	if k != "file" && jIs(kind, "file") {
		is.Err(vp+".seed", "V_VOLUME_SEED", "a single-file volume starts from one file")
		return
	}
	fileRef := func(slug any, at string) {
		f := jFind(g.config.Arr("files"), func(x any) bool { return jEq(jGet(x, "fileSlug"), slug) })
		if !doc.Truthy(f) {
			if doc.Truthy(slug) {
				is.Err(at, "V_REF", "file "+jStr(slug)+" does not exist")
			} else {
				is.Err(at, "V_REQUIRED", "pick the file with the starter content")
			}
			return
		}
		if !doc.Truthy(jGet(f, "content")) {
			is.Err(at, "V_FILE_CONTENT", "file "+jStr(jGet(f, "fileSlug"))+" has no content", "publish")
		}
		if jIs(jFileRenderMode(f), "expressions") {
			jAddStr(renders, jStr(jGet(f, "fileSlug")))
		}
	}
	if k == "file" {
		fileRef(jGet(sd, "fileSlug"), vp+".seed.fileSlug")
		return
	}
	if k == "files" {
		entries, ok := jGet(sd, "entries").([]any)
		if !ok || len(entries) < 1 || len(entries) > 100 {
			is.Err(vp+".seed.entries", "V_VOLUME_SEED", "starter files need 1–100 entries")
			return
		}
		paths := make([]any, len(entries))
		for i, e := range entries {
			paths[i] = jOpt(e, "path")
		}
		for i, e := range entries {
			at := vp + ".seed.entries[" + strconv.Itoa(i) + "]"
			p := jOpt(e, "path")
			if pe := SeedPathError(p); pe != nil {
				is.Err(at+".path", "V_VOLUME_SEED", *pe)
			} else if slices.IndexFunc(paths, func(q any) bool { return jEq(q, p) }) != i {
				is.Err(at+".path", "V_DUPLICATE", jStr(p)+" is listed twice")
			} else {
				for n, q := range paths {
					if s, ok := q.(string); ok && n != i && strings.HasPrefix(s, jStr(p)+"/") {
						is.Err(at+".path", "V_VOLUME_SEED", jStr(p)+" is a file, but another entry puts files inside it")
						break
					}
				}
			}
			fileRef(jOpt(e, "fileSlug"), at+".fileSlug")
		}
		return
	}
	if s, ok := jGet(sd, "blobSha256").(string); !ok || !RE.Sha256.MatchString(s) {
		is.Err(vp+".seed.blobSha256", "V_FORMAT", "upload the archive (.tar.gz)")
	}
	if n, ok := jGet(sd, "name").(string); !ok || !jReTarName.MatchString(n) || doc.JSLen(n) > 200 {
		is.Err(vp+".seed.name", "V_VOLUME_SEED", "the archive must be a .tar.gz / .tgz")
	}
	sz := jGet(sd, "size")
	f, _ := sz.(float64)
	if !jIsInt(sz) || f < 1 || f > SEED_ARCHIVE_MAX {
		is.Err(vp+".seed.size", "V_VOLUME_SEED", "the archive must be 1 B – "+strconv.Itoa(SEED_ARCHIVE_MAX/1048576)+" MiB")
	}
}

func jAddStr(l *[]string, s string) {
	if !slices.Contains(*l, s) {
		*l = append(*l, s)
	}
}
