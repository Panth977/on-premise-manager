package edgecfg

// BuildStandard — the agent edge for the OFFICIAL caddy image (§21 #106 D7, the docker compose export).
//
// API (used by internal/export):
//
//	in, notes, err := edgecfg.InputFromConfig(edgecfg.InputParams{ … Upstreams: portName → CONTAINER port … })
//	cfg, more := edgecfg.BuildStandard(in, edgecfg.StandardOptions{})
//	b, _ := json.Marshal(cfg) // caddy run --config caddy.json (image caddy:2.11.x)
//
// Same hosts, TLS, response headers, error / not-found pages and nginx semantics as Input.Build, with the agent's
// opm_* Caddy modules compiled to standard modules:
//
//   - opm_route (edgeres.Walk) → per host: preamble (vars opm_uri / opm_args / opm_is_args / opm_query), then exact routes,
//     nginx auto-redirects, prefix routes longest first (a prefix without "^~" is a subroute: the regex routes in order,
//     then itself), the regex routes, then error 404. Path tests are case-sensitive vars_regexp matchers on the raw
//     {http.request.uri.path} (as edgeres; Caddy's path matchers fold case and clean the path). Captures:
//     {http.regexp.<name>.<n>}. Rewrite routes (nginx `rewrite … last`) are unrolled: level k+1 = the whole selection
//     again; a rewrite at level 10 answers 500.
//   - opm_upstream → rewrite handlers (proxy rewrite regex / strip prefix / addPrefix); opm_redirect → static_response
//     with a Location template; opm_body_limit → an `expression` matcher on Content-Length (413 before the upstream is
//     contacted) + request_body max_size for chunked bodies; opm_ws_guard → 400 static response; opm_ready → nothing
//     (a stopped container = dial error 502 → the 503 page); transport opm_http → http (read_timeout / write_timeout =
//     per read / write deadlines ≈ the idle timeouts).
//   - upstream dial "<containerSlug>:<containerPort>" (in.Upstreams = portName → CONTAINER port; 0 / missing → 503).
//   - no /_opm route (the agent GUI is not part of the export); admin off; storage file_system /data.
//   - TLS files mode → load_files <TLSDir>/<host>.crt|.key (StandardOptions.TLSDir; the compose export renders them with setup.sh);
//     on-demand internal "*" → no permission module (internal issuer only); hosts with TLS none get a connection policy
//     that selects no certificate (handshake fails, like the agent's opm_any deny).
//   - dns-01: the provider config as given in in.DNSConfigs (the exporter passes {env.OPM_SECRET_…} placeholders) — needs a
//     caddy build with that caddy-dns module (note EDGE_DNS01).

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/edgeres"
	"opm/agent/internal/model"
)

// StandardCaddyImage is the official image the compose export runs the edge with (Caddy v2.11.7 = the agent's Caddy;
// multi-arch index digest).
const StandardCaddyImage = "caddy:2.11.7@sha256:f2a1290d0463aad60660d4ec134943f183ee2a5f6c3eb7bf32dd984f2f020772"

// StandardOptions tune BuildStandard.
type StandardOptions struct {
	// StorageRoot of Caddy (default /data — the edge-data volume).
	StorageRoot string
	// TLSDir: where the files-mode certificate + key of each host are (default /etc/caddy/tls): <dir>/<host>.crt, .key.
	TLSDir string
	// UpstreamHost: dial host of a container (default the containerSlug = its service name / network alias).
	UpstreamHost func(containerSlug string) string
}

// BuildStandard makes the complete Caddy JSON of one site for the official caddy image (see the file comment).
func BuildStandard(src *Input, o StandardOptions) (map[string]any, []Note) {
	if o.StorageRoot == "" {
		o.StorageRoot = "/data"
	}
	if o.TLSDir == "" {
		o.TLSDir = "/etc/caddy/tls"
	}
	if o.UpstreamHost == nil {
		o.UpstreamHost = func(s string) string { return s }
	}
	in := *src
	in.HTTPListen, in.HTTPSListen = "", ""
	in.FilesPEM = map[string]PEMPair{}
	st := &stdState{o: o}
	in.std = st
	p := in.Build("")
	cfg := Assemble(o.StorageRoot, []*Partial{p})
	apps, _ := cfg["apps"].(map[string]any)
	var notes []Note
	// TLS: files → load_files; on-demand without the opm_any permission; TLS-none hosts select no certificate
	if apps == nil {
		apps = map[string]any{}
		cfg["apps"] = apps
	}
	if _, ok := apps["tls"]; !ok && in.HTTPSPort != nil && in.AnyTLS() {
		apps["tls"] = map[string]any{}
	}
	if tlsApp, ok := apps["tls"].(map[string]any); ok {
		var files []any
		for _, h := range SortedHosts(in.Hosts) {
			if h.TLS.Mode == "files" && h.Hostname != "*" {
				files = append(files, map[string]any{"certificate": o.TLSDir + "/" + h.Hostname + ".crt", "key": o.TLSDir + "/" + h.Hostname + ".key", "tags": []string{h.Hostname}})
			}
		}
		certs, _ := tlsApp["certificates"].(map[string]any)
		if certs == nil {
			certs = map[string]any{}
		}
		delete(certs, "load_pem")
		if len(files) > 0 {
			certs["load_files"] = files
		}
		if len(certs) > 0 {
			tlsApp["certificates"] = certs
		} else {
			delete(tlsApp, "certificates")
		}
		if len(tlsApp) == 0 {
			delete(apps, "tls")
		}
		if auto, ok := tlsApp["automation"].(map[string]any); ok {
			delete(auto, "on_demand")
		}
	}
	var deny []string
	for _, d := range p.OnDemandDeny {
		if d != "*" {
			deny = append(deny, d)
		}
	}
	if httpApp, ok := apps["http"].(map[string]any); ok && p.OnDemand && len(deny) > 0 {
		if srv, ok := httpApp["servers"].(map[string]any)["edge_https"].(map[string]any); ok {
			pols, _ := srv["tls_connection_policies"].([]any)
			deny := map[string]any{"match": map[string]any{"sni": deny}, "certificate_selection": map[string]any{"any_tag": []string{"opm-tls-none"}}}
			srv["tls_connection_policies"] = append([]any{deny}, pols...)
		}
	}
	for _, h := range in.Hosts {
		if h.TLS.Mode == "acme" && h.TLS.Challenge == "dns-01" {
			prov := ""
			if h.TLS.DNSProvider != nil {
				prov = *h.TLS.DNSProvider
			}
			notes = append(notes, Note{Code: "EDGE_DNS01", Message: fmt.Sprintf("host %s uses ACME dns-01 (%s): the official caddy image has no DNS provider modules — build it with xcaddy --with github.com/caddy-dns/%s", h.Hostname, prov, prov)})
		}
	}
	if httpApp, ok := apps["http"].(map[string]any); ok && len(st.named) > 0 {
		for _, srv := range httpApp["servers"].(map[string]any) {
			srv.(map[string]any)["named_routes"] = st.named
		}
	}
	notes = append(notes, st.notes...)
	return cfg, notes
}

type stdState struct {
	o     StandardOptions
	notes []Note
	seq   int // regexp name counter (unique per config)
	// named routes of the rewrite levels ≥ 1 (one per host + level; invoked by the rewrite routes — linear size)
	named map[string]any
	hostN map[string]int
}

// selName is the named route holding the selection of a host (hosts with rewrite routes: re-entered after each internal
// rewrite through `invoke`; the rewrite count lives in the var opm_rw).
func (st *stdState) selName(host string) string {
	if st.hostN == nil {
		st.hostN = map[string]int{}
	}
	n, ok := st.hostN[host]
	if !ok {
		n = len(st.hostN) + 1
		st.hostN[host] = n
	}
	return "opm_select_h" + strconv.Itoa(n)
}

func (st *stdState) name(prefix string) string {
	st.seq++
	return prefix + strconv.Itoa(st.seq)
}

func pathRE(name, pattern string) map[string]any {
	return map[string]any{"vars_regexp": map[string]any{"{http.request.uri.path}": map[string]any{"name": name, "pattern": pattern}}}
}

func methodsOf(r *model.EdgeRoute) []string {
	if len(r.Match.Methods) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.Match.Methods))
	for _, m := range r.Match.Methods {
		out = append(out, strings.ToUpper(m))
	}
	sort.Strings(out)
	return out
}

func kindOf(r *model.EdgeRoute) string {
	if r.Match.Kind == "" {
		return "prefix"
	}
	return r.Match.Kind
}

func byOrder(a, b *model.EdgeRoute) bool {
	if a.Order != b.Order {
		return a.Order < b.Order
	}
	return a.RouteID < b.RouteID
}

func u16(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// matcher of a route's path (+ methods); capName gets the regex captures (regex routes).
func (st *stdState) routeMatch(r *model.EdgeRoute, capName string) map[string]any {
	var m map[string]any
	switch kindOf(r) {
	case "exact":
		m = pathRE(capName, "^"+regexp.QuoteMeta(r.Match.Path)+"$")
	case "regex":
		m = pathRE(capName, r.Match.Path)
	default:
		m = pathRE(capName, "^"+regexp.QuoteMeta(r.Match.Path))
	}
	if ms := methodsOf(r); ms != nil {
		m["method"] = ms
	}
	return m
}

// numCaps is the number of groups of a route's match regex (0 for exact / prefix).
func numCaps(src string) int {
	s := src
	if strings.HasPrefix(s, "(?i)") {
		s = "(?i)" + s[4:]
	}
	re, err := regexp.Compile(s)
	if err != nil {
		return 0
	}
	return re.NumSubexp()
}

// expandStd is edgeres.ExpandEdgeVars as a Caddy placeholder template (vars of the current selection level; captures
// {http.regexp.<capName>.<n>}; groups that do not exist expand to "").
func expandStd(t, capName string, caps int) string {
	var b strings.Builder
	for _, tk := range edgeres.Tokenize(t) {
		if tk.Var == "" {
			b.WriteString(escapeCaddy(tk.Literal))
			continue
		}
		if len(tk.Var) == 1 && tk.Var[0] >= '1' && tk.Var[0] <= '9' {
			n := int(tk.Var[0] - '0')
			if capName != "" && n <= caps {
				b.WriteString("{http.regexp." + capName + "." + tk.Var + "}")
			}
			continue
		}
		b.WriteString(ToPlaceholders(tk.Raw))
	}
	return b.String()
}

// preamble sets the nginx variables of the current path / query (agent: set by opm_route at the end of the walk; here
// at every selection level, so the last level holds the final values).
func preamble() []any {
	return []any{
		map[string]any{"handle": []any{map[string]any{"handler": "vars", "opm_uri": "{http.request.uri.path}", "opm_args": "{http.request.uri.query}", "opm_is_args": "?", "opm_query": "?{http.request.uri.query}"}}},
		map[string]any{"match": []any{map[string]any{"vars": map[string]any{"{http.request.uri.query}": []string{""}}}},
			"handle": []any{map[string]any{"handler": "vars", "opm_is_args": "", "opm_query": ""}}},
	}
}

func setRoute(id string) map[string]any {
	return map[string]any{"handler": "vars", "opm_route": id}
}

func errorH(code int, msg string) map[string]any {
	return map[string]any{"handler": "error", "status_code": code, "error": msg}
}

func subroute(routes []any) map[string]any {
	return map[string]any{"handler": "subroute", "routes": routes}
}

// stdHostRoutes is the standard-module equivalent of the opm_route handler + the per-route handlers of hostRoutes.
func (in *Input) stdHostRoutes(h model.EdgeHost) []any {
	st := in.std
	routes := edgeres.HostRoutes(in.edge(), &h)
	hasRewrite := false
	for _, r := range routes {
		if r.Action.Type == "rewrite" {
			hasRewrite = true
		}
	}
	if !hasRewrite {
		return in.stdSelect(h, routes, 0, false)
	}
	name := st.selName(h.Hostname)
	if st.named == nil {
		st.named = map[string]any{}
	}
	if _, ok := st.named[name]; !ok {
		st.named[name] = map[string]any{"handle": []any{subroute(in.stdSelect(h, routes, 0, true))}}
	}
	return []any{map[string]any{"handle": []any{map[string]any{"handler": "vars", "opm_rw": "0"}, map[string]any{"handler": "invoke", "name": name}}, "terminal": true}}
}

// stdSelect is one selection level (k = internal rewrites done so far).
func (in *Input) stdSelect(h model.EdgeHost, routes []model.EdgeRoute, k int, hasRewrite bool) []any {
	st := in.std
	ptr := make([]*model.EdgeRoute, len(routes))
	for i := range routes {
		ptr[i] = &routes[i]
	}
	var exact, prefixes, rx []*model.EdgeRoute
	for _, r := range ptr {
		switch kindOf(r) {
		case "exact":
			exact = append(exact, r)
		case "regex":
			rx = append(rx, r)
		default:
			prefixes = append(prefixes, r)
		}
	}
	sort.SliceStable(exact, func(i, j int) bool { return byOrder(exact[i], exact[j]) })
	sort.SliceStable(rx, func(i, j int) bool { return byOrder(rx[i], rx[j]) })
	sort.SliceStable(prefixes, func(i, j int) bool {
		a, b := prefixes[i], prefixes[j]
		if la, lb := u16(a.Match.Path), u16(b.Match.Path); la != lb {
			return la > lb
		}
		return byOrder(a, b)
	})
	out := preamble()
	// 1. exact
	for _, r := range exact {
		n := st.name("e")
		out = append(out, map[string]any{"match": []any{st.routeMatch(r, n)}, "handle": in.stdHandle(h, r, n, 0, routes, k, hasRewrite), "terminal": true})
	}
	// 2. nginx auto-redirect: proxy prefix "/x/" requested as "/x" when no prefix route "/x" (same method) exists
	var autos []*model.EdgeRoute
	for _, r := range prefixes {
		if r.Action.Type == "proxy" && len(r.Match.Path) > 1 && strings.HasSuffix(r.Match.Path, "/") {
			autos = append(autos, r)
		}
	}
	sort.SliceStable(autos, func(i, j int) bool { return byOrder(autos[i], autos[j]) })
	for _, a := range autos {
		base := strings.TrimSuffix(a.Match.Path, "/")
		all := false
		var block []string
		for _, r := range prefixes {
			if r.Match.Path == base {
				ms := methodsOf(r)
				if ms == nil {
					all = true
				}
				block = append(block, ms...)
			}
		}
		if all {
			continue
		}
		m := pathRE(st.name("a"), "^"+regexp.QuoteMeta(base)+"$")
		if ms := methodsOf(a); ms != nil {
			m["method"] = ms
		}
		match := []any{m}
		if len(block) > 0 {
			m["not"] = []any{map[string]any{"method": uniq(block)}}
		}
		rh, _ := edgeres.EffectiveResponseHeaders(a, &h)
		handle := []any{setRoute(a.RouteID)}
		handle = append(handle, responseHeaderHandlers(rh, false)...)
		handle = append(handle, map[string]any{"handler": "static_response", "status_code": 301,
			"headers": map[string]any{"Location": []string{"{http.request.uri.path}/{http.vars.opm_query}"}}})
		out = append(out, map[string]any{"match": match, "handle": handle, "terminal": true})
	}
	regexRoutes := func() []any {
		var o []any
		for _, r := range rx {
			n := st.name("r")
			o = append(o, map[string]any{"match": []any{st.routeMatch(r, n)}, "handle": in.stdHandle(h, r, n, numCaps(r.Match.Path), routes, k, hasRewrite), "terminal": true})
		}
		return o
	}
	// 3. prefixes, longest first
	for _, r := range prefixes {
		n := st.name("p")
		m := st.routeMatch(r, n)
		own := in.stdHandle(h, r, n, 0, routes, k, hasRewrite)
		if r.Match.StopRegex || len(rx) == 0 {
			out = append(out, map[string]any{"match": []any{m}, "handle": own, "terminal": true})
			continue
		}
		sub := regexRoutes()
		sub = append(sub, map[string]any{"handle": own, "terminal": true})
		out = append(out, map[string]any{"match": []any{m}, "handle": []any{subroute(sub)}, "terminal": true})
	}
	// 4. regex routes when no prefix matched, 5. 404
	out = append(out, regexRoutes()...)
	out = append(out, map[string]any{"handle": []any{errorH(404, "no route")}, "terminal": true})
	return out
}

func uniq(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// restRoutes: the path after the matched prefix (edgeres.RestAfterPrefix) into {http.regexp.<name>.1}.
// keepSlash: "" for an exact "/" remainder (redirect keepPath) when false.
func restMatch(name, prefix string) map[string]any {
	p := strings.TrimSuffix(prefix, "/")
	if prefix == "/" {
		p = ""
	}
	return pathRE(name, "^"+regexp.QuoteMeta(p)+"(.*)$")
}

// stdHandle: the handlers of the selected route r (capName / caps: its match captures).
func (in *Input) stdHandle(h model.EdgeHost, r *model.EdgeRoute, capName string, caps int, routes []model.EdgeRoute, k int, hasRewrite bool) []any {
	st := in.std
	a := &r.Action
	rh, _ := edgeres.EffectiveResponseHeaders(r, &h)
	switch a.Type {
	case "rewrite":
		n := st.name("w")
		rw := map[string]any{"handler": "rewrite", "uri": expandStd(a.Replacement, n, numCaps(a.Regex))}
		// opm_rw counts the internal rewrites ("0" … "10"): the 11th answers 500 (edgeres.Walk)
		// non-terminal routes in DESCENDING order: a terminal route would end the whole chain (subroutes never call the
		// outer handler after a terminal route), and ascending order would cascade 0 → 1 → 2 …
		var bump []any
		for d := edgeres.MaxRewrites - 1; d >= 0; d-- {
			bump = append(bump, map[string]any{"match": []any{map[string]any{"vars": map[string]any{"opm_rw": []string{strconv.Itoa(d)}}}},
				"handle": []any{map[string]any{"handler": "vars", "opm_rw": strconv.Itoa(d + 1)}}})
		}
		return []any{setRoute(r.RouteID), subroute([]any{
			map[string]any{"match": []any{map[string]any{"vars": map[string]any{"opm_rw": []string{strconv.Itoa(edgeres.MaxRewrites)}}}}, "handle": []any{errorH(500, "rewrite loop")}, "terminal": true},
			map[string]any{"match": []any{pathRE(n, a.Regex)}, "handle": []any{rw, subroute(bump), map[string]any{"handler": "invoke", "name": st.selName(h.Hostname)}}, "terminal": true},
			map[string]any{"handle": []any{errorH(404, "rewrite regex did not match")}, "terminal": true},
		})}
	case "redirect":
		handle := []any{setRoute(r.RouteID)}
		handle = append(handle, responseHeaderHandlers(rh, false)...)
		status := a.Status
		if status == 0 {
			status = 302
		}
		loc := expandStd(a.To, capName, caps)
		if !a.KeepPath {
			return append(handle, map[string]any{"handler": "static_response", "status_code": status, "headers": map[string]any{"Location": []string{loc}}})
		}
		if strings.HasSuffix(a.To, "/") {
			loc = strings.TrimSuffix(loc, "/")
		} else if strings.HasSuffix(loc, "}") {
			st.notes = append(st.notes, Note{Code: "EDGE_REDIRECT_TRIM", Message: fmt.Sprintf("route %s: a trailing \"/\" produced by a variable at the end of the redirect target is not removed", r.RouteID)})
		}
		prefix := ""
		if kindOf(r) == "prefix" {
			prefix = r.Match.Path
		}
		n := st.name("k")
		p := strings.TrimSuffix(prefix, "/")
		if prefix == "/" {
			p = ""
		}
		resp := func(rest string) map[string]any {
			return map[string]any{"handler": "static_response", "status_code": status, "headers": map[string]any{"Location": []string{loc + rest + "{http.vars.opm_query}"}}}
		}
		var sub []any
		if kindOf(r) == "prefix" {
			sub = []any{
				map[string]any{"match": []any{pathRE(st.name("k"), "^"+regexp.QuoteMeta(p)+"/?$")}, "handle": []any{resp("")}, "terminal": true},
				map[string]any{"match": []any{restMatch(n, prefix)}, "handle": []any{resp("{http.regexp." + n + ".1}")}, "terminal": true},
			}
		} else {
			// exact / regex: RestAfterPrefix("", path) = the whole path ("/" → "")
			sub = []any{
				map[string]any{"match": []any{pathRE(st.name("k"), "^/$")}, "handle": []any{resp("")}, "terminal": true},
				map[string]any{"handle": []any{resp("{http.request.uri.path}")}, "terminal": true},
			}
		}
		return append(handle, subroute(sub))
	}
	// proxy
	handle := []any{setRoute(r.RouteID)}
	handle = append(handle, responseHeaderHandlers(rh, true)...)
	var sub []any
	if !a.Websocket {
		sub = append(sub, map[string]any{"match": []any{map[string]any{"header_regexp": map[string]any{"Upgrade": map[string]any{"pattern": "(?i)^websocket$"}}}},
			"handle": []any{map[string]any{"handler": "static_response", "status_code": 400, "body": "WebSocket is not enabled for this route\n",
				"headers": map[string]any{"Content-Type": []string{"text/plain; charset=utf-8"}, "X-Content-Type-Options": []string{"nosniff"}}}}, "terminal": true})
	}
	var final []any
	if mb, _ := edgeres.EffectiveMaxBody(in.MaxBodyBytes, a); mb != nil {
		cl := "{http.request.header.Content-Length}"
		sub = append(sub, map[string]any{"match": []any{map[string]any{"expression": fmt.Sprintf(`%s.matches('^[0-9]+$') && int(%s) > %d`, quoteCEL(cl), quoteCEL(cl), *mb)}},
			"handle": []any{errorH(413, "Content-Length is above the limit")}, "terminal": true})
		final = append(final, map[string]any{"handler": "request_body", "max_size": *mb})
	}
	// upstream path: proxy rewrite (nginx rewrite … break) or strip prefix, then addPrefix
	var up []any
	if a.Rewrite != nil {
		n := st.name("u")
		up = append(up, map[string]any{"match": []any{pathRE(n, a.Rewrite.Regex)}, "handle": []any{map[string]any{"handler": "rewrite", "uri": expandStd(a.Rewrite.Replacement, n, numCaps(a.Rewrite.Regex))}}})
	} else if a.StripPrefix {
		// edgeres.UpstreamPathQuery: rest = path after TrimSuffix(prefix, "/"); "/" + rest unless it starts with "/"
		// = "/" + (rest without one leading "/"). One NON-terminal route (a terminal route would end the chain here).
		matched := r.Match.Path
		p := strings.TrimSuffix(matched, "/")
		if matched != "/" && p != "" {
			n1 := st.name("s")
			up = append(up, map[string]any{"match": []any{pathRE(n1, "^"+regexp.QuoteMeta(p)+"/?(.*)$")}, "handle": []any{map[string]any{"handler": "rewrite", "uri": "/{http.regexp." + n1 + ".1}"}}})
		}
	}
	if a.AddPrefix != nil && *a.AddPrefix != "" {
		up = append(up, map[string]any{"handle": []any{map[string]any{"handler": "rewrite", "uri": escapeCaddy(strings.TrimSuffix(*a.AddPrefix, "/")) + "{http.request.uri.path}"}}})
	}
	if len(up) > 0 {
		final = append(final, subroute(up))
	}
	port := in.Upstreams[a.ContainerSlug][a.PortName]
	if port == 0 {
		final = append(final, errorH(503, "container "+a.ContainerSlug+" has no port "+a.PortName))
	} else {
		final = append(final, in.stdProxy(a, st.o.UpstreamHost(a.ContainerSlug)+":"+strconv.Itoa(port)))
	}
	sub = append(sub, map[string]any{"handle": final, "terminal": true})
	return append(handle, subroute(sub))
}

func quoteCEL(placeholder string) string { return placeholder }

func (in *Input) stdProxy(a *model.EdgeRouteAction, dial string) map[string]any {
	def := func(v, d float64) float64 {
		if v <= 0 {
			return d
		}
		return v
	}
	read := def(a.ReadTimeoutSec, model.DefaultReadTimeoutSec)
	rp := map[string]any{"handler": "reverse_proxy",
		"upstreams": []any{map[string]any{"dial": dial}},
		"transport": map[string]any{"protocol": "http", "versions": []string{"1.1"},
			"dial_timeout":            secs(def(a.ConnectTimeoutSec, model.DefaultConnectTimeoutSec)),
			"response_header_timeout": secs(read),
			"read_timeout":            secs(read),
			"write_timeout":           secs(def(a.SendTimeoutSec, model.DefaultSendTimeoutSec))},
		"headers": map[string]any{"request": map[string]any{"set": RequestHeaderSet(a)}}}
	if len(a.RemoveHeaders) > 0 {
		rp["headers"].(map[string]any)["request"].(map[string]any)["delete"] = a.RemoveHeaders
	}
	if !a.ResponseBuffering {
		rp["flush_interval"] = -1
	}
	if a.RequestBuffering {
		rp["request_buffers"] = edgeres.RequestBufferBytes
	}
	return rp
}
