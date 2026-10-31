package shared

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/expr"
)

var (
	jDevToolSlugRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	jRedisDbRe      = regexp.MustCompile(`^(?:[0-9]|1[0-5])$`)
	jReHTTPURL      = regexp.MustCompile(`^[hH][tT][tT][pP][sS]?://[^` + jWS + `]`)
	jHTTPSchemes    = []string{"none", "basic", "bearer", "api-key"}
	jPgSSLModes     = []string{"disable", "prefer", "require", "verify-full"}
	jCqlConsistency = []string{"ANY", "ONE", "TWO", "THREE", "QUORUM", "ALL", "LOCAL_QUORUM", "EACH_QUORUM", "LOCAL_ONE"}
	jDevToolTargets = map[string][]string{"http": {"container", "host", "url"}, "postgres": {"container", "host"}, "cassandra": {"container", "host"}, "redis": {"container", "host"}, "mqtt": {"container", "host"}, "sqlite": {"volume-file", "container-file"}}
)

func jNonEmpty(v any) bool {
	s, ok := v.(string)
	return ok && doc.JSTrim(s) != ""
}

func jSafeRelative(p any) bool {
	s, ok := p.(string)
	if !ok || s == "" || doc.JSLen(s) > 1024 {
		return false
	}
	if strings.HasPrefix(s, "/") || strings.Contains(s, "\\") || strings.Contains(s, "\x00") || strings.Contains(s, "{{") {
		return false
	}
	return !slices.Contains(strings.Split(s, "/"), "..")
}

func jEnvNames(c, img any) ([]string, bool) {
	var names []string
	for _, e := range jArrOf(jGet(c, "env")) {
		if doc.Truthy(e) {
			if s, ok := jGet(e, "name").(string); ok {
				jAddStr(&names, s)
			}
		}
	}
	for _, e := range jList(jOpt(img, "env"), "(img?.env ?? [])") {
		if doc.Truthy(e) {
			if s, ok := jGet(e, "name").(string); ok {
				jAddStr(&names, s)
			}
		}
	}
	_, isArr := jOpt(img, "env").([]any)
	return names, doc.Truthy(img) && isArr
}

func jEnvIsSecret(c any, name string, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	e := jFind(jArrOf(jGet(c, "env")), func(x any) bool { return doc.Truthy(x) && jIs(jGet(x, "name"), name) })
	if !doc.Truthy(e) {
		return false
	}
	if !jNull(jGet(e, "secretRef")) {
		return true
	}
	v, ok := jGet(e, "value").(string)
	if !ok || !expr.IsExpr(v) {
		return false
	}
	refs, err := expr.ExprRefs(v)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(refs, func(r string) bool {
		if strings.HasPrefix(r, "secret.") {
			return true
		}
		n, ok := strings.CutPrefix(r, "container.env.")
		return ok && jEnvIsSecret(c, n, seen)
	})
}

func jSplit4(ref string) (string, string, string, string) {
	p := append(strings.Split(ref, "."), "", "", "")
	return p[0], p[1], p[2], p[3]
}

func jValidateSiteDevTools(site any, config *doc.Map, ctx *jVCtx, is *Issues) {
	sc := jOpt(site, "config")
	if !doc.Truthy(sc) || !jIsObj(sc) {
		return
	}
	ss := jStr(jGet(site, "siteSlug"))
	lp := "sites[" + ss + "].config.devTools"
	raw := jGet(sc, "devTools")
	if jNull(raw) {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		is.Err(lp, "V_TYPE", "devTools must be a list of presets")
		return
	}
	if len(list) > 50 {
		is.Err(lp, "V_DEVTOOL_MAX", "at most 50 dev-tool presets per site ("+strconv.Itoa(len(list))+")")
	}
	var objs []any
	for _, p := range list {
		if jPlainObj(p) {
			objs = append(objs, p)
		}
	}
	for _, d := range jOrderedDupes(objs, func(p any) any { return jStr(jGet(p, "presetSlug")) }) {
		is.Err(lp+"["+jStr(d)+"]", "V_DEVTOOL_DUP", "preset "+jStr(d)+" is defined twice on site "+ss)
	}
	containers, volumes := jArrOf(jGet(sc, "containers")), jArrOf(jGet(sc, "volumes"))
	secretSlugs, varSlugs := map[string]bool{}, map[string]bool{}
	for _, s := range config.Arr("secrets") {
		secretSlugs[jStr(jGet(s, "secretSlug"))] = true
	}
	for _, v := range config.Arr("variables") {
		varSlugs[jStr(jGet(v, "variableSlug"))] = true
	}
	for _, p := range list {
		if !jPlainObj(p) {
			is.Err(lp, "V_TYPE", "a preset must be an object")
			continue
		}
		jValidatePreset(p, lp, ss, containers, volumes, secretSlugs, varSlugs, ctx, is)
	}
}

func jValidatePreset(raw any, lp, ss string, containers, volumes []any, secretSlugs, varSlugs map[string]bool, ctx *jVCtx, is *Issues) {
	slug := jGet(raw, "presetSlug")
	pp := lp + "[" + jStr(slug) + "]"
	if s, ok := slug.(string); !ok || !jDevToolSlugRe.MatchString(s) {
		is.Err(pp+".presetSlug", "V_DEVTOOL_SLUG", `preset slug "`+jStr(slug)+`" must match ^[a-z0-9][a-z0-9-]{0,62}$`)
	}
	CheckLabel(jGet(raw, "name"), pp+".name", is, 0)
	CheckDescription(jGet(raw, "description"), pp+".description", is)
	if ro := jGet(raw, "readOnly"); ro != doc.Undefined {
		if _, ok := ro.(bool); !ok {
			is.Err(pp+".readOnly", "V_TYPE", "readOnly must be true / false")
		}
	}
	if !jIsOneOf(jGet(raw, "kind"), jDevToolKinds...) {
		is.Err(pp+".kind", "V_DEVTOOL_KIND", `kind "`+jStr(jGet(raw, "kind"))+`" must be one of `+strings.Join(jDevToolKinds, ", "))
		return
	}
	kind := jGet(raw, "kind").(string)
	findC := func(s any) any {
		return jFind(containers, func(x any) bool { return doc.Truthy(x) && jEq(jGet(x, "containerSlug"), s) })
	}
	exprField := func(v any, path string) bool {
		s, ok := v.(string)
		if !ok || !expr.IsExpr(s) {
			return true
		}
		errs := expr.CheckExpr(s, expr.FieldDevtool)
		for _, e := range errs {
			is.Err(path, e.Code, jExprAt(e))
		}
		if len(errs) > 0 {
			return false
		}
		refs, err := expr.ExprRefs(s)
		if err != nil {
			jThrow("%s", err.Error())
		}
		for _, ref := range refs {
			root, sl, sub, name := jSplit4(ref)
			if root == "secret" && sl != "" && !secretSlugs[sl] {
				is.Err(path, "V_DEVTOOL_SECRET", "secret "+sl+" does not exist")
			}
			if root == "var" && sl != "" && !varSlugs[sl] {
				is.Err(path, "V_DEVTOOL_VAR", "variable "+sl+" does not exist")
			}
			if root == "containers" && sl != "" {
				c := findC(sl)
				if !doc.Truthy(c) {
					is.Err(path, "V_DEVTOOL_CONTAINER", "container "+sl+" (in {{ "+ref+" }}) does not exist on site "+ss)
				} else if sub == "env" && name != "" {
					names, known := jEnvNames(c, ctx.image(c))
					if !slices.Contains(names, name) && known {
						list := strings.Join(names, ", ")
						if list == "" {
							list = "none"
						}
						is.Err(path, "V_DEVTOOL_ENV", "container "+sl+" has no env var "+name+" (its env: "+list+")")
					}
				}
			}
		}
		return true
	}
	plainEnv := func(src, path, f string) {
		refs, err := expr.ExprRefs(src)
		if err != nil {
			return
		}
		for _, ref := range refs {
			root, sl, sub, name := jSplit4(ref)
			if root != "containers" || sub != "env" || sl == "" || name == "" {
				continue
			}
			c := findC(sl)
			if !doc.Truthy(c) {
				continue
			}
			if names, _ := jEnvNames(c, ctx.image(c)); !slices.Contains(names, name) {
				continue
			}
			if !jEnvIsSecret(c, name, map[string]bool{}) {
				is.Warn(path, "V_DEVTOOL_PLAIN_ENV", "env "+name+" of container "+sl+" holds a plain value, not a secret: the "+f+" is readable in the config — set that env from a secret (secretRef or {{ secret.x }})")
			}
		}
	}
	t := jGet(raw, "target")
	tp := pp + ".target"
	allowed := jDevToolTargets[kind]
	containerOf := func(s any, at string) any {
		if !jNonEmpty(s) {
			is.Err(at+".containerSlug", "V_DEVTOOL_TARGET", "pick the container of this site")
			return nil
		}
		c := findC(s)
		if !doc.Truthy(c) {
			is.Err(at+".containerSlug", "V_DEVTOOL_CONTAINER", "container "+jStr(s)+" does not exist on site "+ss)
			return nil
		}
		if jEq(jGet(c, "enabled"), false) {
			is.Warn(at+".containerSlug", "V_DEVTOOL_CONTAINER", "container "+jStr(s)+" is disabled: the preset cannot connect until it runs")
		}
		return c
	}
	tk := jOpt(t, "kind")
	if !jPlainObj(t) || !jIsOneOf(tk, allowed...) {
		is.Err(tp, "V_DEVTOOL_TARGET", "a "+kind+" preset target must be one of "+strings.Join(allowed, ", "))
	} else {
		switch tk {
		case "container":
			c := containerOf(jGet(t, "containerSlug"), tp)
			pn := jGet(t, "portName")
			if !jNonEmpty(pn) {
				is.Err(tp+".portName", "V_DEVTOOL_TARGET", "pick the port of the container")
			} else if c != nil {
				if img := ctx.image(c); doc.Truthy(img) && !slices.ContainsFunc(jContainerPorts(c, img), func(p *doc.Map) bool { return jEq(p.Val("portName"), pn) }) {
					var names []any
					for _, p := range jArrOf(jGet(img, "ports")) {
						names = append(names, jGet(p, "name"))
					}
					list := jJoin(names, ", ")
					if list == "" {
						list = "no ports"
					}
					is.Err(tp+".portName", "V_DEVTOOL_PORT", "port "+jStr(pn)+" is not a port of the image of "+jStr(jGet(c, "containerSlug"))+" ("+list+")")
				}
			}
		case "host":
			h := jGet(t, "host")
			hs, _ := h.(string)
			if !jNonEmpty(h) || strings.Contains(hs, "{{") || doc.JSLen(hs) > 253 || !(jIsIPLiteral(hs) || (RE.DNSName.MatchString(hs) && !strings.HasPrefix(hs, "*"))) {
				is.Err(tp+".host", "V_DEVTOOL_TARGET", `host "`+jStr(h)+`" must be an IP or a DNS name (no {{ }})`)
			}
			port := jGet(t, "port")
			if f, _ := port.(float64); !jIsInt(port) || f < 1 || f > 65535 {
				is.Err(tp+".port", "V_DEVTOOL_TARGET", "port "+jStr(port)+" must be an integer 1–65535")
			}
		case "url":
			if u, ok := jGet(t, "url").(string); !ok || !jReHTTPURL.MatchString(u) || doc.JSLen(u) > LIMITS.Value {
				is.Err(tp+".url", "V_DEVTOOL_TARGET", "url must be an absolute http:// or https:// URL (≤ 4096 characters; may hold {{ var.x }})")
			} else {
				exprField(u, tp+".url")
			}
		case "volume-file":
			vs := jGet(t, "volumeSlug")
			if !jNonEmpty(vs) {
				is.Err(tp+".volumeSlug", "V_DEVTOOL_TARGET", "pick a declared volume of the site")
			} else {
				v := jFind(volumes, func(x any) bool { return doc.Truthy(x) && jEq(jGet(x, "volumeSlug"), vs) })
				if !doc.Truthy(v) {
					is.Err(tp+".volumeSlug", "V_DEVTOOL_VOLUME", "volume "+jStr(vs)+" is not declared on site "+ss)
				} else if jIs(jGet(v, "kind"), "file") {
					is.Err(tp+".volumeSlug", "V_DEVTOOL_VOLUME", "volume "+jStr(vs)+" is a single-file volume: SQLite needs a folder volume (journal / WAL files live next to the database)")
				}
			}
			if p := jGet(t, "path"); !jSafeRelative(p) {
				is.Err(tp+".path", "V_DEVTOOL_VOLUME", `path "`+jStr(p)+`" must be a relative path inside the volume (no leading "/", no "..")`)
			}
		case "container-file":
			containerOf(jGet(t, "containerSlug"), tp)
			p := jGet(t, "path")
			if s, ok := p.(string); !ok || !IsSafeMountPath(s) || strings.Contains(s, "{{") {
				is.Err(tp+".path", "V_DEVTOOL_VOLUME", `path "`+jStr(p)+`" must be an absolute path in the container (not "/", no spaces, no "..")`)
			}
		}
	}
	a := jGet(raw, "auth")
	ap := pp + ".auth"
	if !jPlainObj(a) {
		is.Err(ap, "V_DEVTOOL_AUTH", "auth must be an object")
	} else {
		for _, f := range []string{"username", "database", "clientId", "apiKeyHeader"} {
			v := jGet(a, f)
			if _, isStr := v.(string); !jNull(v) && !isStr {
				is.Err(ap+"."+f, "V_TYPE", f+" must be a string or null")
			} else {
				exprField(v, ap+"."+f)
			}
		}
		cred := func(c any, f string) string { // "" = null
			at := ap + "." + f
			if jNull(c) {
				return "none"
			}
			if !jPlainObj(c) {
				is.Err(at, "V_DEVTOOL_AUTH", f+" must be { kind: none } or { kind: expr, expr }")
				return ""
			}
			k := jGet(c, "kind")
			if jIs(k, "none") {
				return "none"
			}
			plain := "the " + f + " is stored in plain text in the config — anyone who can read the config sees it; use a secret for production"
			if jIs(k, "literal") {
				v, ok := jGet(c, "value").(string)
				switch {
				case !ok || v == "":
					is.Err(at, "V_DEVTOOL_AUTH", f+" value must be a non-empty string (pick None for no "+f+")")
				case doc.JSLen(v) > LIMITS.Value:
					is.Err(at, "V_DEVTOOL_AUTH", f+" value must be at most "+strconv.Itoa(LIMITS.Value)+" characters")
				default:
					is.Warn(at, "V_DEVTOOL_LITERAL", plain)
				}
				return "literal"
			}
			if !jIs(k, "expr") {
				is.Err(at, "V_DEVTOOL_AUTH", f+".kind must be none, literal or expr")
				return ""
			}
			x, ok := jGet(c, "expr").(string)
			if !ok || doc.JSTrim(x) == "" {
				is.Err(at, "V_DEVTOOL_AUTH", f+".expr must be a non-empty expression")
				return "expr"
			}
			if !expr.IsExpr(x) {
				is.Warn(at, "V_DEVTOOL_LITERAL", plain+" (an expression without {{ }} is a static value)")
				return "expr"
			}
			if exprField(x, at) {
				refs, _ := expr.ExprRefs(x)
				if len(refs) == 0 {
					is.Warn(at, "V_DEVTOOL_LITERAL", plain+" (the expression references no secret or variable)")
				} else {
					plainEnv(x, at, f)
				}
			}
			return "expr"
		}
		pw := cred(jGet(a, "password"), "password")
		tk := cred(jGet(a, "token"), "token")
		scheme := jCoal(jGet(a, "httpScheme"), "none")
		if !jIsOneOf(scheme, jHTTPSchemes...) {
			is.Err(ap+".httpScheme", "V_DEVTOOL_AUTH", "httpScheme must be one of "+strings.Join(jHTTPSchemes, ", "))
		}
		user := jGet(a, "username")
		db, clientID, apiKey := jGet(a, "database"), jGet(a, "clientId"), jGet(a, "apiKeyHeader")
		switch kind {
		case "postgres":
			if !jNonEmpty(user) {
				is.Err(ap+".username", "V_DEVTOOL_AUTH", "PostgreSQL needs a username")
			}
		case "cassandra":
			if jNonEmpty(user) != (pw != "none" && pw != "") {
				is.Err(ap, "V_DEVTOOL_AUTH", "Cassandra needs both username and password, or neither")
			}
		case "redis":
			if s, ok := db.(string); !jNull(db) && !(ok && jRedisDbRe.MatchString(s)) {
				is.Err(ap+".database", "V_DEVTOOL_AUTH", `Redis database must be "0"–"15" or null (got "`+jStr(db)+`")`)
			}
		case "http":
			if jIs(scheme, "basic") && !jNonEmpty(user) {
				is.Err(ap+".username", "V_DEVTOOL_AUTH", "basic auth needs a username")
			}
			if jIsOneOf(scheme, "bearer", "api-key") && tk == "none" {
				is.Err(ap+".token", "V_DEVTOOL_AUTH", jStr(scheme)+" auth needs a token (an expression such as {{ secret.api-token }})")
			}
			if s, ok := apiKey.(string); jIs(scheme, "api-key") && !jNull(apiKey) && !(ok && (RE.HeaderName.MatchString(s) || expr.IsExpr(s))) {
				is.Err(ap+".apiKeyHeader", "V_DEVTOOL_AUTH", `"`+jStr(apiKey)+`" is not a valid header name`)
			}
		case "mqtt":
			if s, ok := clientID.(string); ok && len(s) > 65535 {
				is.Err(ap+".clientId", "V_DEVTOOL_AUTH", "clientId must be at most 65535 bytes")
			}
		}
		if kind != "http" && (!jIs(scheme, "none") || tk != "none" || !jNull(apiKey)) {
			is.Warn(ap, "V_DEVTOOL_AUTH", "httpScheme / token / apiKeyHeader are only used by HTTP presets (ignored for "+kind+")")
		}
		if kind != "mqtt" && !jNull(clientID) {
			is.Warn(ap+".clientId", "V_DEVTOOL_AUTH", "clientId is only used by MQTT presets (ignored for "+kind+")")
		}
		if kind == "sqlite" && (!jNull(user) || pw != "none" || !jNull(db)) {
			is.Warn(ap, "V_DEVTOOL_AUTH", "SQLite has no username / password / database (ignored)")
		}
	}
	o := jGet(raw, "options")
	op := pp + ".options"
	if !jPlainObj(o) {
		is.Err(op, "V_DEVTOOL_OPTION", "options must be an object")
		return
	}
	bad := func(f, msg string) { is.Err(op+"."+f, "V_DEVTOOL_OPTION", msg) }
	intIn := func(f string, min, max int) {
		v := jGet(o, f)
		n, _ := v.(float64)
		if !jNull(v) && (!jIsInt(v) || n < float64(min) || n > float64(max)) {
			bad(f, f+" must be an integer "+strconv.Itoa(min)+"–"+strconv.Itoa(max)+" or null (got "+jStr(v)+")")
		}
	}
	for _, f := range []string{"tls", "tlsInsecure"} {
		if v := jGet(o, f); v != doc.Undefined {
			if _, ok := v.(bool); !ok {
				bad(f, f+" must be true / false")
			}
		}
	}
	for _, f := range []string{"followRedirects", "cleanSession"} {
		if v := jGet(o, f); !jNull(v) {
			if _, ok := v.(bool); !ok {
				bad(f, f+" must be true / false or null")
			}
		}
	}
	if v := jGet(o, "tlsServerName"); !jNull(v) {
		if _, ok := v.(string); !ok {
			bad("tlsServerName", "tlsServerName must be a string or null")
		}
	}
	if v := jGet(o, "sslMode"); !jNull(v) && !jIsOneOf(v, jPgSSLModes...) {
		bad("sslMode", "sslMode must be one of "+strings.Join(jPgSSLModes, ", ")+" or null")
	}
	if v := jGet(o, "consistency"); !jNull(v) && !jIsOneOf(v, jCqlConsistency...) {
		bad("consistency", "consistency must be one of "+strings.Join(jCqlConsistency, ", ")+" or null")
	}
	if v := jGet(o, "scheme"); !jNull(v) && !jIsOneOf(v, "http", "https") {
		bad("scheme", "scheme must be http, https or null")
	}
	intIn("timeoutSec", 1, 3600)
	intIn("maxRows", 1, 10000)
	intIn("keepAliveSec", 0, 65535)
	if v := jGet(o, "basePath"); !jNull(v) {
		if s, ok := v.(string); !ok || !strings.HasPrefix(s, "/") {
			bad("basePath", `basePath must start with "/" (got "`+jStr(v)+`")`)
		}
	}
	hs := jGet(o, "defaultHeaders")
	if jNull(hs) {
		return
	}
	list, ok := hs.([]any)
	if !ok {
		bad("defaultHeaders", "defaultHeaders must be a list of { name, value }")
		return
	}
	if len(list) > 50 {
		bad("defaultHeaders", "at most 50 default headers")
	}
	for i, h := range list {
		hp := "defaultHeaders[" + strconv.Itoa(i) + "]"
		if !jPlainObj(h) {
			bad(hp, "a header must be { name, value }")
			continue
		}
		if s, ok := jGet(h, "name").(string); !ok || !RE.HeaderName.MatchString(s) {
			bad(hp+".name", `"`+jStr(jGet(h, "name"))+`" is not a valid header name`)
		}
		if s, ok := jGet(h, "value").(string); !ok || doc.JSLen(s) > LIMITS.Value {
			bad(hp+".value", "header value must be a string of at most "+strconv.Itoa(LIMITS.Value)+" characters")
		} else {
			exprField(s, op+"."+hp+".value")
		}
	}
}
