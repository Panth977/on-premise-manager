package edgecfg

import (
	"net/http"
	"strconv"
	"strings"

	"opm/agent/internal/edgeres"
	"opm/agent/internal/model"
)

// Round E host routes (SPEC §12.3). Per host subroute:
//
//	(http + redirectHttpToHttps) static 308 → https
//	/_opm, /_opm/*          → opm_agent
//	(no match)              → opm_route   {routes}: edgeres.Walk (nginx selection + internal rewrites); sets the vars
//	                                        opm_route, opm_auto, opm_location, opm_uri, opm_args, opm_is_args,
//	                                        opm_query, opm_caps; 404 / 500 → error routes
//	vars opm_route=<id> (auto-redirect candidates first, then every proxy / redirect route in effective order)
//	                        → response header handlers, then opm_redirect | opm_ws_guard, opm_body_limit, opm_ready,
//	                          opm_upstream, reverse_proxy (transport opm_http)

// Built-in pages of the edge's own 413 / 500 answers.
const (
	BuiltIn413 = `<!doctype html><html><head><meta charset="utf-8"><title>Too large</title></head><body style="font-family:sans-serif;text-align:center;padding:10vh">
<h1>413</h1><p>The request body is too large.</p></body></html>`
	BuiltIn500 = `<!doctype html><html><head><meta charset="utf-8"><title>Error</title></head><body style="font-family:sans-serif;text-align:center;padding:10vh">
<h1>500</h1><p>The request could not be routed (rewrite loop).</p></body></html>`
)

func staticPage(status int, body string) map[string]any {
	return map[string]any{"handler": "static_response", "status_code": status,
		"headers": map[string]any{"Content-Type": []string{"text/html; charset=utf-8"}}, "body": body}
}

// escapeCaddy escapes literal braces so the Caddy replacer keeps them.
func escapeCaddy(s string) string {
	s = strings.ReplaceAll(s, "{", `\{`)
	return strings.ReplaceAll(s, "}", `\}`)
}

// ToPlaceholders maps an edge header value (nginx variables + {path} {query} {host}) to Caddy placeholders (SPEC §12.6).
// Literal "{" / "}" are escaped. $1…$9 are empty in header values (TS: captures []). Unknown "$name" stays literal.
func ToPlaceholders(v string) string {
	var b strings.Builder
	for _, tk := range edgeres.Tokenize(v) {
		if tk.Var == "" {
			b.WriteString(escapeCaddy(tk.Literal))
			continue
		}
		if tk.Placeholder {
			switch tk.Var {
			case "path":
				b.WriteString("{http.vars.opm_uri}")
			case "query":
				b.WriteString("{http.vars.opm_query}")
			case "host":
				b.WriteString("{http.request.host}")
			}
			continue
		}
		if len(tk.Var) == 1 && tk.Var[0] >= '1' && tk.Var[0] <= '9' {
			continue
		}
		switch tk.Var {
		case "uri":
			b.WriteString("{http.vars.opm_uri}")
		case "args", "query_string":
			b.WriteString("{http.vars.opm_args}")
		case "is_args":
			b.WriteString("{http.vars.opm_is_args}")
		case "request_uri":
			b.WriteString("{http.request.orig_uri}")
		case "host":
			b.WriteString("{http.request.host}")
		case "scheme":
			b.WriteString("{http.request.scheme}")
		case "remote_addr":
			b.WriteString("{http.request.remote.host}")
		case "server_port":
			b.WriteString("{http.request.local.port}")
		default:
			if strings.HasPrefix(tk.Var, "http_") && len(tk.Var) > 5 {
				b.WriteString("{http.request.header." + http.CanonicalHeaderKey(strings.ReplaceAll(tk.Var[5:], "_", "-")) + "}")
			} else {
				b.WriteString(escapeCaddy(tk.Raw))
			}
		}
	}
	return b.String()
}

func inFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func headerOps(m model.HeaderMap, keep func(name string) bool) map[string]any {
	out := map[string]any{}
	for _, kv := range m {
		if keep(kv.Name) {
			out[kv.Name] = []string{ToPlaceholders(kv.Value)}
		}
	}
	return out
}

// responseHeaderHandlers: set / add for the add_header statuses (require), set / add of "always" names (deferred), then
// remove (proxied responses only, deferred). Listed outermost first: Caddy applies the innermost wrapper first, so the
// order applied to a response is remove → set → add (SPEC §12.1 #7).
func responseHeaderHandlers(rh model.EdgeResponseHeaders, proxied bool) []any {
	var out []any
	notAlways := func(n string) bool { return !inFold(rh.Always, n) }
	always := func(n string) bool { return inFold(rh.Always, n) }
	cond := map[string]any{}
	if s := headerOps(rh.Set, notAlways); len(s) > 0 {
		cond["set"] = s
	}
	if a := headerOps(rh.Add, notAlways); len(a) > 0 {
		cond["add"] = a
	}
	if len(cond) > 0 {
		cond["require"] = map[string]any{"status_code": edgeres.AddHeaderStatuses}
		out = append(out, map[string]any{"handler": "headers", "response": cond})
	}
	alw := map[string]any{}
	if s := headerOps(rh.Set, always); len(s) > 0 {
		alw["set"] = s
	}
	if a := headerOps(rh.Add, always); len(a) > 0 {
		alw["add"] = a
	}
	if len(alw) > 0 {
		alw["deferred"] = true
		out = append(out, map[string]any{"handler": "headers", "response": alw})
	}
	if proxied && len(rh.Remove) > 0 {
		out = append(out, map[string]any{"handler": "headers", "response": map[string]any{"delete": rh.Remove, "deferred": true}})
	}
	return out
}

// alwaysHandler: the "always" set / add applied directly (error pages of the edge).
func alwaysHandler(rh model.EdgeResponseHeaders) map[string]any {
	always := func(n string) bool { return inFold(rh.Always, n) }
	resp := map[string]any{}
	if s := headerOps(rh.Set, always); len(s) > 0 {
		resp["set"] = s
	}
	if a := headerOps(rh.Add, always); len(a) > 0 {
		resp["add"] = a
	}
	if len(resp) == 0 {
		return nil
	}
	return map[string]any{"handler": "headers", "response": resp}
}

func secs(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) + "s" }

func varMatch(key, val string) []any {
	return []any{map[string]any{"vars": map[string]any{key: []string{val}}}}
}

// RequestHeaderSet is the reverse_proxy headers.request.set of a proxy action: the defaults (X-Real-IP,
// X-Forwarded-For / -Proto / -Host; Host is kept by Caddy), then setHeaders (override case-insensitively; hop-by-hop
// ignored), minus removeHeaders.
func RequestHeaderSet(a *model.EdgeRouteAction) map[string][]string {
	type kv struct{ k, v string }
	list := []kv{{"X-Real-IP", "{http.request.remote.host}"}, {"X-Forwarded-For", "{http.request.remote.host}"},
		{"X-Forwarded-Proto", "{http.request.scheme}"}, {"X-Forwarded-Host", "{http.request.hostport}"}}
	for _, h := range a.SetHeaders {
		if edgeres.IsHopByHop(h.Name) {
			continue
		}
		nl := list[:0:0]
		for _, x := range list {
			if !strings.EqualFold(x.k, h.Name) {
				nl = append(nl, x)
			}
		}
		list = append(nl, kv{h.Name, ToPlaceholders(h.Value)})
	}
	out := map[string][]string{}
	for _, x := range list {
		if !inFold(a.RemoveHeaders, x.k) {
			out[x.k] = []string{x.v}
		}
	}
	return out
}

func (in *Input) proxyHandlers(r *model.EdgeRoute, rh model.EdgeResponseHeaders) []any {
	a := &r.Action
	handle := responseHeaderHandlers(rh, true)
	handle = append(handle, map[string]any{"handler": "opm_ws_guard", "allow": a.Websocket})
	if mb, _ := edgeres.EffectiveMaxBody(in.MaxBodyBytes, a); mb != nil {
		handle = append(handle, map[string]any{"handler": "opm_body_limit", "max": *mb})
	}
	handle = append(handle, map[string]any{"handler": "opm_ready", "containerSlug": a.ContainerSlug, "instance": in.Instance})
	up := map[string]any{"handler": "opm_upstream", "matched": r.Match.Path, "stripPrefix": a.StripPrefix}
	if a.Rewrite != nil {
		up["rewrite"] = a.Rewrite
	}
	if a.AddPrefix != nil && *a.AddPrefix != "" {
		up["addPrefix"] = *a.AddPrefix
	}
	handle = append(handle, up)
	port := in.Upstreams[a.ContainerSlug][a.PortName]
	if port == 0 {
		port = 1 // not running: opm_ready answers 503 first; a dial would fail → 503 page
	}
	def := func(v, d float64) float64 {
		if v <= 0 {
			return d
		}
		return v
	}
	read := def(a.ReadTimeoutSec, model.DefaultReadTimeoutSec)
	rp := map[string]any{"handler": "reverse_proxy",
		"upstreams": []any{map[string]any{"dial": "127.0.0.1:" + strconv.Itoa(port)}},
		"transport": map[string]any{"protocol": "opm_http", "versions": []string{"1.1"},
			"dial_timeout":            secs(def(a.ConnectTimeoutSec, model.DefaultConnectTimeoutSec)),
			"response_header_timeout": secs(read),
			"read_idle_timeout":       secs(read),
			"write_idle_timeout":      secs(def(a.SendTimeoutSec, model.DefaultSendTimeoutSec))},
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
	return append(handle, rp)
}

func (in *Input) edge() *model.Edge {
	return &model.Edge{HTTPPort: in.HTTPPort, HTTPSPort: in.HTTPSPort, MaxBodyBytes: in.MaxBodyBytes, Hosts: in.Hosts, Routes: in.Routes}
}

func (in *Input) hostRoutes(h model.EdgeHost, https bool) map[string]any {
	var sub []any
	if !https && h.RedirectHTTPToHTTPS && h.TLS.Mode != "none" && in.HTTPSPort != nil {
		port := ""
		if *in.HTTPSPort != 443 {
			port = ":" + strconv.Itoa(*in.HTTPSPort)
		}
		sub = append(sub, map[string]any{"handle": []any{map[string]any{"handler": "static_response", "status_code": 308,
			"headers": map[string]any{"Location": []string{"https://{http.request.host}" + port + "{http.request.uri}"}}}}})
	} else if in.std != nil {
		if in.RootRedirect != "" {
			sub = append(sub, map[string]any{"match": []any{map[string]any{"path": []string{"/"}}}, "terminal": true,
				"handle": []any{map[string]any{"handler": "static_response", "status_code": 302, "headers": map[string]any{"Location": []string{in.RootRedirect}}}}})
		}
		sub = append(sub, in.stdHostRoutes(h)...)
	} else {
		sub = append(sub, map[string]any{"match": []any{map[string]any{"path": []string{"/_opm", "/_opm/*"}}},
			"handle": []any{map[string]any{"handler": "opm_agent", "surface": "edge", "instance": in.Instance}}})
		if in.RootRedirect != "" {
			sub = append(sub, map[string]any{"match": []any{map[string]any{"path": []string{"/"}}}, "terminal": true,
				"handle": []any{map[string]any{"handler": "static_response", "status_code": 302, "headers": map[string]any{"Location": []string{in.RootRedirect}}}}})
		}
		e := in.edge()
		sub = append(sub, map[string]any{"handle": []any{map[string]any{"handler": "opm_route", "routes": edgeres.HostRoutes(e, &h)}}})
		ordered := edgeres.RoutesForHost(e, &h)
		for i := range ordered {
			r := &ordered[i]
			rh, _ := edgeres.EffectiveResponseHeaders(r, &h)
			a := &r.Action
			switch a.Type {
			case "proxy":
				if r.Match.Kind == "prefix" && len(r.Match.Path) > 1 && strings.HasSuffix(r.Match.Path, "/") {
					handle := append(responseHeaderHandlers(rh, false), map[string]any{"handler": "opm_redirect", "auto": true, "status": 301})
					sub = append(sub, map[string]any{"match": varMatch("opm_auto", r.RouteID), "handle": handle, "terminal": true})
				}
				sub = append(sub, map[string]any{"match": varMatch("opm_route", r.RouteID), "handle": in.proxyHandlers(r, rh), "terminal": true})
			case "redirect":
				prefix := ""
				if r.Match.Kind == "prefix" {
					prefix = r.Match.Path
				}
				handle := append(responseHeaderHandlers(rh, false), map[string]any{"handler": "opm_redirect", "to": a.To, "status": a.Status, "keepPath": a.KeepPath, "prefix": prefix})
				sub = append(sub, map[string]any{"match": varMatch("opm_route", r.RouteID), "handle": handle, "terminal": true})
			}
		}
		sub = append(sub, map[string]any{"handle": []any{map[string]any{"handler": "error", "status_code": 404, "error": "no route"}}})
	}
	entry := map[string]any{"terminal": true, "handle": []any{map[string]any{"handler": "subroute", "routes": sub}}}
	if h.Hostname != "*" {
		entry["match"] = []any{map[string]any{"host": []string{h.Hostname}}}
	}
	return entry
}

func (in *Input) statusPages() []any {
	st := func(code string) []any {
		return []any{map[string]any{"vars": map[string]any{"{http.error.status_code}": []string{code}}}}
	}
	p413 := staticPage(413, BuiltIn413)
	if in.std != nil {
		// standard caddy: the unread rest of a chunked body must not be parsed as the next request (the agent's
		// opm_body_limit sets Connection: close itself)
		p413["headers"].(map[string]any)["Connection"] = []string{"close"}
	}
	return []any{
		map[string]any{"match": st("404"), "handle": []any{staticPage(404, in.NotFound404)}, "terminal": true},
		map[string]any{"match": st("413"), "handle": []any{p413}, "terminal": true},
		map[string]any{"match": st("500"), "handle": []any{staticPage(500, BuiltIn500)}, "terminal": true},
		map[string]any{"handle": []any{staticPage(503, in.NotFound503)}, "terminal": true},
	}
}

// errorRoutes: per host, the "always" response headers of the route that answered (route's own list, else the host's),
// then the page for the status (404 not-found page, 413, 500, else the 503 page).
func (in *Input) errorRoutes(hosts []model.EdgeHost) map[string]any {
	var routes []any
	e := in.edge()
	for _, h := range hosts {
		var sub []any
		own := []string{}
		for _, r := range edgeres.RoutesForHost(e, &h) {
			if r.ResponseHeaders == nil {
				continue
			}
			own = append(own, r.RouteID)
			if ah := alwaysHandler(*r.ResponseHeaders); ah != nil {
				sub = append(sub, map[string]any{"match": varMatch("opm_route", r.RouteID), "handle": []any{ah}})
			}
		}
		if ah := alwaysHandler(h.ResponseHeaders); ah != nil {
			x := map[string]any{"handle": []any{ah}}
			if len(own) > 0 {
				x["match"] = []any{map[string]any{"not": []any{map[string]any{"vars": map[string]any{"opm_route": own}}}}}
			}
			sub = append(sub, x)
		}
		if len(sub) == 0 {
			continue
		}
		sub = append(sub, in.statusPages()...)
		entry := map[string]any{"terminal": true, "handle": []any{map[string]any{"handler": "subroute", "routes": sub}}}
		if h.Hostname != "*" {
			entry["match"] = []any{map[string]any{"host": []string{h.Hostname}}}
		}
		routes = append(routes, entry)
	}
	routes = append(routes, in.statusPages()...)
	return map[string]any{"routes": routes}
}
