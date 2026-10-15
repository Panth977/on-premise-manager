package validate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"opm/agent/internal/edgeres"
	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// Round E edge rules (mirror of validate.ts validateEdge after the host checks).

var responseOnlyCacheControl = regexp.MustCompile(`(?i)\b(public|private|max-age|s-maxage|no-store|must-revalidate|proxy-revalidate|immutable)\b`)

// IsResponseOnlyHeader is TS isResponseOnlyHeader.
func IsResponseOnlyHeader(name, value string) bool {
	n := strings.ToLower(name)
	if n == "cache-control" {
		return responseOnlyCacheControl.MatchString(value)
	}
	switch n {
	case "expires", "pragma", "set-cookie", "content-security-policy", "content-security-policy-report-only", "strict-transport-security", "x-frame-options":
		return true
	}
	return strings.HasPrefix(n, "access-control-allow-") || n == "access-control-expose-headers" || n == "access-control-max-age"
}

// MoveToResponseHeaders is TS moveToResponseHeaders (pure; returns a new route).
func MoveToResponseHeaders(route model.EdgeRoute, name string, host *model.EdgeHost) model.EdgeRoute {
	if route.Action.Type != "proxy" {
		return route
	}
	key, value, found := "", "", false
	for _, kv := range route.Action.SetHeaders {
		if strings.EqualFold(kv.Name, name) {
			key, value, found = kv.Name, kv.Value, true
			break
		}
	}
	if !found {
		return route
	}
	rest := model.HeaderMap{}
	for _, kv := range route.Action.SetHeaders {
		if kv.Name != key {
			rest = append(rest, kv)
		}
	}
	var base model.EdgeResponseHeaders
	switch {
	case route.ResponseHeaders != nil:
		base = route.ResponseHeaders.Clone()
	case host != nil:
		base = host.ResponseHeaders.Clone()
	default:
		base = model.EmptyResponseHeaders()
	}
	strip := func(m model.HeaderMap) model.HeaderMap {
		out := model.HeaderMap{}
		for _, kv := range m {
			if !strings.EqualFold(kv.Name, key) {
				out = append(out, kv)
			}
		}
		return out
	}
	base.Add = strip(base.Add)
	base.Set = strip(base.Set)
	base.Set.Set(key, value)
	rm := []string{}
	for _, k := range base.Remove {
		if !strings.EqualFold(k, key) {
			rm = append(rm, k)
		}
	}
	base.Remove = rm
	route.Action.SetHeaders = rest
	route.ResponseHeaders = &base
	return route
}

// NewEdgeHost is TS newEdgeHost.
func NewEdgeHost(hostname string) model.EdgeHost {
	return model.EdgeHost{Hostname: hostname, TLS: model.EdgeTls{Mode: "none"}, ResponseHeaders: model.EmptyResponseHeaders()}
}

// NewEdgeRoute is TS newEdgeRoute (a prefix route).
func NewEdgeRoute(routeID, path string, action model.EdgeRouteAction, order int) model.EdgeRoute {
	return model.EdgeRoute{RouteID: routeID, Hosts: []string{}, Match: model.EdgeMatch{Kind: "prefix", Path: path}, Action: action, Order: order}
}

func checkRewrite(regex, replacement, at string, is *Issues) {
	if e := edgeres.CheckEdgeRegex(regex); e != "" {
		is.err(at+".regex", "V_EDGE_REGEX", e)
	}
	if replacement == "" {
		is.err(at+".replacement", "V_REQUIRED", "the rewrite needs a replacement (the new path, e.g. $1 or /x$request_uri)")
	} else if expr.IsExpr(replacement) {
		checkExprField(replacement, expr.FieldEdge, at+".replacement", is)
	}
}

func checkResponseHeaders(h *model.EdgeResponseHeaders, at string, is *Issues) {
	names := []string{}
	for _, part := range []struct {
		name string
		m    model.HeaderMap
	}{{"set", h.Set}, {"add", h.Add}} {
		for _, kv := range part.m {
			k := kv.Name
			if !RE.HeaderName.MatchString(k) {
				is.err(at+"."+part.name, "V_FORMAT", fmt.Sprintf(`header name "%s" is not valid`, k))
			}
			for _, x := range names {
				if strings.EqualFold(x, k) {
					is.err(at+"."+part.name+"."+k, "V_EDGE_HEADER", k+" is in both set and add: use one")
					break
				}
			}
			names = append(names, k)
			if expr.IsExpr(kv.Value) {
				checkExprField(kv.Value, expr.FieldEdge, at+"."+part.name+"."+k, is)
			}
		}
	}
	for _, k := range h.Remove {
		if !RE.HeaderName.MatchString(k) {
			is.err(at+".remove", "V_FORMAT", fmt.Sprintf(`header name "%s" is not valid`, k))
		}
	}
	for _, k := range h.Always {
		ok := false
		for _, x := range names {
			if strings.EqualFold(x, k) {
				ok = true
			}
		}
		if !ok {
			is.err(at+".always", "V_EDGE_HEADER", fmt.Sprintf(`"always" lists %s, which is not in set / add`, k))
		}
	}
}

func matchKey(r *model.EdgeRoute) string {
	k := r.Match.Kind
	if k == "" {
		k = "prefix"
	}
	hosts := []string{}
	for _, h := range r.Hosts {
		hosts = append(hosts, strings.ToLower(h))
	}
	sort.Strings(hosts)
	ms := append([]string{}, r.Match.Methods...)
	sort.Strings(ms)
	return k + "\x00" + r.Match.Path + "\x00" + strings.Join(hosts, "\x01") + "\x00" + strings.Join(ms, "\x01")
}

func validateEdgeRoundE(edge *model.Edge, o EdgeOptions, b string, is *Issues) {
	if edge.MaxBodyBytes != nil && (*edge.MaxBodyBytes < 0 || *edge.MaxBodyBytes > edgeres.MaxBodyLimit) {
		is.err(b+".maxBodyBytes", "V_EDGE_BODY", "maxBodyBytes must be null (unlimited) or 0–1 TiB in bytes")
	}
	for i := range edge.Hosts {
		h := &edge.Hosts[i]
		label := h.Hostname
		if label == "" {
			label = fmt.Sprint(i)
		}
		checkResponseHeaders(&h.ResponseHeaders, fmt.Sprintf("%s.hosts[%s].responseHeaders", b, label), is)
	}
	ids := []string{}
	keys := []string{}
	for i := range edge.Routes {
		ids = append(ids, edge.Routes[i].RouteID)
		keys = append(keys, matchKey(&edge.Routes[i]))
	}
	for i := range edge.Routes {
		r := &edge.Routes[i]
		at := fmt.Sprintf("%s.routes[%s]", b, r.RouteID)
		if !RE.RouteID.MatchString(r.RouteID) {
			is.err(at+".routeId", "V_FORMAT", fmt.Sprintf(`route id "%s" is not a slug (^[a-z0-9][a-z0-9-]{0,62}$)`, r.RouteID))
		}
		cnt := 0
		for _, x := range ids {
			if x == r.RouteID {
				cnt++
			}
		}
		if cnt > 1 {
			is.err(at+".routeId", "V_DUPLICATE", fmt.Sprintf("route id %s is used two times", r.RouteID))
		}
		if r.Order < 1 {
			is.err(at+".order", "V_RANGE", "order must be an integer ≥ 1")
		}
		kind, p := r.Match.Kind, r.Match.Path
		switch {
		case kind != "prefix" && kind != "exact" && kind != "regex":
			is.err(at+".match.kind", "V_ENUM", "match.kind must be prefix | exact | regex")
		case kind == "regex":
			if e := edgeres.CheckEdgeRegex(p); e != "" {
				is.err(at+".match.path", "V_EDGE_REGEX", e)
			}
		case !strings.HasPrefix(p, "/") || pathBad.MatchString(p):
			is.err(at+".match.path", "V_EDGE_ROUTE", `the path must start with "/" and hold no spaces, "?" or "#"`)
		case p == "/_opm" || strings.HasPrefix(p, "/_opm/"):
			is.err(at+".match.path", "V_RESERVED", "/_opm/ is reserved for the agent")
		}
		if r.Match.StopRegex && kind != "prefix" {
			is.err(at+".match.stopRegex", "V_EDGE_ROUTE", `"stop regex" (nginx ^~) is only for prefix routes`)
		}
		for j := 0; j < i; j++ {
			if keys[j] == keys[i] {
				is.err(at+".match", "V_DUPLICATE", fmt.Sprintf("route %s has the same match as route %s (it would never be used)", r.RouteID, edge.Routes[j].RouteID))
				break
			}
		}
		for _, x := range r.Hosts {
			found := false
			for _, h := range edge.Hosts {
				if strings.EqualFold(h.Hostname, x) {
					found = true
				}
			}
			if !found {
				is.err(at+".hosts", "V_REF", fmt.Sprintf("host %s is not in edge.hosts", x))
			}
		}
		for _, m := range r.Match.Methods {
			if !contains(EdgeMethods, m) {
				is.err(at+".match.methods", "V_ENUM", fmt.Sprintf("method %s is not one of %s", m, strings.Join(EdgeMethods, ", ")))
			}
		}
		a := &r.Action
		if a.Type != "proxy" && a.Type != "redirect" && a.Type != "rewrite" {
			is.err(at+".action.type", "V_ENUM", "action.type must be proxy | redirect | rewrite")
			continue
		}
		if r.ResponseHeaders != nil {
			if a.Type == "rewrite" {
				is.err(at+".responseHeaders", "V_EDGE_HEADER", "a rewrite route has no response of its own: set the headers on the route it rewrites to")
			} else {
				checkResponseHeaders(r.ResponseHeaders, at+".responseHeaders", is)
			}
		}
		switch a.Type {
		case "proxy":
			c, ok := o.Containers[a.ContainerSlug]
			switch {
			case a.ContainerSlug == "":
				is.err(at+".action.containerSlug", "V_REQUIRED", "pick the container this route proxies to")
			case o.Containers != nil && !ok:
				is.err(at+".action.containerSlug", "V_REF", fmt.Sprintf("container %s does not exist on this site", a.ContainerSlug))
			case ok && !contains(c.PortNames, a.PortName):
				if a.PortName != "" {
					is.err(at+".action.portName", "V_REF", fmt.Sprintf("port %s does not exist on the image of %s", a.PortName, c.Slug))
				} else {
					is.err(at+".action.portName", "V_REQUIRED", fmt.Sprintf("pick the port of %s this route proxies to", c.Slug))
				}
			case ok && c.EdgePorts != nil && !contains(c.EdgePorts, a.PortName):
				is.err(at+".action.portName", "V_EDGE_ROUTE", fmt.Sprintf("port %s of container %s is closed to the edge (ports[].edge = false)", a.PortName, c.Slug))
			case ok && !c.Enabled:
				is.Warn(at+".action.containerSlug", "V_EDGE_TARGET_DISABLED", fmt.Sprintf("container %s is disabled: this route answers 503", c.Slug))
			}
			rng := func(k string, v float64, max float64) {
				if !isIntF(v) || v < 1 || v > max {
					is.err(at+".action."+k, "V_RANGE", fmt.Sprintf("%s must be 1–%d seconds", k, int(max)))
				}
			}
			rng("connectTimeoutSec", a.ConnectTimeoutSec, edgeres.MaxConnectTimeout)
			rng("sendTimeoutSec", a.SendTimeoutSec, edgeres.MaxTimeoutSec)
			rng("readTimeoutSec", a.ReadTimeoutSec, edgeres.MaxTimeoutSec)
			if a.MaxBodyBytes != nil && (*a.MaxBodyBytes < 0 || *a.MaxBodyBytes > edgeres.MaxBodyLimit) {
				is.err(at+".action.maxBodyBytes", "V_EDGE_BODY", "maxBodyBytes must be null (inherit the edge), 0 (unlimited) or up to 1 TiB in bytes")
			}
			if a.AddPrefix != nil && !strings.HasPrefix(*a.AddPrefix, "/") {
				is.err(at+".action.addPrefix", "V_EDGE_ROUTE", `addPrefix must start with "/"`)
			}
			if a.StripPrefix && kind == "regex" {
				is.err(at+".action.stripPrefix", "V_EDGE_ROUTE", "strip prefix needs a prefix or exact route; use rewrite for a regex route")
			}
			if a.Rewrite != nil {
				if a.StripPrefix {
					is.err(at+".action.rewrite", "V_EDGE_ROUTE", "use either strip prefix or rewrite, not both")
				}
				checkRewrite(a.Rewrite.Regex, a.Rewrite.Replacement, at+".action.rewrite", is)
			}
			for _, kv := range a.SetHeaders {
				k, v := kv.Name, kv.Value
				if !RE.HeaderName.MatchString(k) {
					is.err(at+".action.setHeaders", "V_FORMAT", fmt.Sprintf(`header name "%s" is not valid`, k))
				} else if edgeres.IsHopByHop(k) {
					is.Warn(at+".action.setHeaders."+k, "V_EDGE_HOP", k+" is managed by the edge (WebSocket upgrades and keep-alive work without it); the value is ignored")
				}
				if expr.IsExpr(v) {
					checkExprField(v, expr.FieldEdge, at+".action.setHeaders."+k, is)
				}
				if IsResponseOnlyHeader(k, v) {
					is.Warn(at+".action.setHeaders."+k, "W_RESPONSE_HEADER_AS_REQUEST",
						k+` is set as a REQUEST header (sent to the container), but it only means something on responses. For nginx add_header / proxy_hide_header use "Response headers → browser" (fix: moveToResponseHeaders).`)
				}
			}
			for _, k := range a.RemoveHeaders {
				if !RE.HeaderName.MatchString(k) {
					is.err(at+".action.removeHeaders", "V_FORMAT", fmt.Sprintf(`header name "%s" is not valid`, k))
				}
			}
		case "redirect":
			if a.To == "" {
				is.err(at+".action.to", "V_REQUIRED", `redirect needs "to"`)
			} else if expr.IsExpr(a.To) {
				checkExprField(a.To, expr.FieldEdge, at+".action.to", is)
			}
			switch a.Status {
			case 301, 302, 303, 307, 308:
			default:
				is.err(at+".action.status", "V_ENUM", "status must be 301, 302, 303, 307 or 308")
			}
			if a.KeepPath && kind != "prefix" {
				is.err(at+".action.keepPath", "V_EDGE_ROUTE", `keep path needs a prefix route; use $uri / $1 in "to" for exact and regex routes`)
			}
		default:
			checkRewrite(a.Regex, a.Replacement, at+".action", is)
		}
	}
}
