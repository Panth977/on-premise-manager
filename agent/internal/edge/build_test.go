package edge

import (
	"encoding/json"
	"strings"
	"testing"

	"opm/agent/internal/model"
)

func i64(v int64) *int64 { return &v }

// jsonOf marshals v and decodes it generically.
func jsonOf(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var x any
	_ = json.Unmarshal(b, &x)
	return x
}

func hostSub(t *testing.T, srv map[string]any, host string) []any {
	t.Helper()
	for _, r := range srv["routes"].([]any) {
		m := r.(map[string]any)
		ms, _ := m["match"].([]any)
		h := "*"
		if len(ms) > 0 {
			h = ms[0].(map[string]any)["host"].([]any)[0].(string)
		}
		if h == host {
			return m["handle"].([]any)[0].(map[string]any)["routes"].([]any)
		}
	}
	t.Fatalf("host %s not found", host)
	return nil
}

func routeByVar(t *testing.T, sub []any, key, id string) []any {
	t.Helper()
	for _, r := range sub {
		m := r.(map[string]any)
		ms, _ := m["match"].([]any)
		if len(ms) == 0 {
			continue
		}
		vars, _ := ms[0].(map[string]any)["vars"].(map[string]any)
		if v, ok := vars[key]; ok && v.([]any)[0] == id {
			return m["handle"].([]any)
		}
	}
	t.Fatalf("route %s=%s not found", key, id)
	return nil
}

func handler(t *testing.T, hs []any, name string) map[string]any {
	t.Helper()
	for _, h := range hs {
		if m := h.(map[string]any); m["handler"] == name {
			return m
		}
	}
	return nil
}

func TestBuildCaddyJSON(t *testing.T) {
	p := 8443
	rw := &model.EdgeRewrite{Regex: "^/q/(.*)$", Replacement: "/search?term=$1"}
	px := func(slug string) model.EdgeRouteAction { return model.NewProxyAction(slug, "http") }
	sse := px("sse")
	sse.ReadTimeoutSec, sse.ConnectTimeoutSec, sse.SendTimeoutSec = 2, 5, 7
	buf := px("buf")
	buf.RequestBuffering, buf.ResponseBuffering, buf.Websocket = true, true, false
	buf.MaxBodyBytes = i64(10)
	hdr := px("hdr")
	hdr.SetHeaders = model.MapOf("X-Real-IP", "$remote_addr", "X-Lit", "a{b}c $nope", "Connection", "Upgrade", "X-Uri", "$uri$is_args$args", "X-H", "$http_x_y")
	hdr.RemoveHeaders = []string{"X-Forwarded-Host"}
	hdr.Rewrite = rw
	strip := px("strip")
	strip.StripPrefix, strip.AddPrefix = true, func() *string { s := "/v1/"; return &s }()
	unl := px("unl")
	unl.MaxBodyBytes = i64(0)
	rh := &model.EdgeResponseHeaders{Set: model.MapOf("Cache-Control", "public"), Add: model.MapOf("X-A", "$host", "X-B", "1"), Remove: []string{"Pragma"}, Always: []string{"X-B"}}
	in := &Input{Instance: "b1", HTTPPort: 8080, HTTPSPort: &p, MaxBodyBytes: i64(100),
		Hosts: []model.EdgeHost{{Hostname: "a.example", TLS: model.EdgeTls{Mode: "files", CertFileSlug: "c", KeySecretSlug: "k"},
			ResponseHeaders: model.EdgeResponseHeaders{Set: model.HeaderMap{}, Add: model.MapOf("X-Host", "h"), Remove: []string{}, Always: []string{"X-Host"}}}},
		Routes: []model.EdgeRoute{
			{RouteID: "root", Match: model.EdgeMatch{Kind: "prefix", Path: "/"}, Action: unl, Order: 9},
			{RouteID: "ex", Match: model.EdgeMatch{Kind: "exact", Path: "/login"}, Action: px("ex"), Order: 1},
			{RouteID: "stop", Match: model.EdgeMatch{Kind: "prefix", Path: "/static/", StopRegex: true}, Action: px("st"), Order: 2},
			{RouteID: "rx", Match: model.EdgeMatch{Kind: "regex", Path: `^/u/(\d+)$`}, Action: model.EdgeRouteAction{Type: "redirect", To: "/users/$1", Status: 303}, Order: 3},
			{RouteID: "rw", Match: model.EdgeMatch{Kind: "prefix", Path: "/old/"}, Action: model.EdgeRouteAction{Type: "rewrite", Regex: "^/old/(.*)$", Replacement: "/new/$1"}, Order: 4},
			{RouteID: "sse", Match: model.EdgeMatch{Kind: "prefix", Path: "/sse/"}, Action: sse, Order: 5},
			{RouteID: "buf", Match: model.EdgeMatch{Kind: "prefix", Path: "/buf/"}, Action: buf, Order: 6, ResponseHeaders: rh},
			{RouteID: "hdr", Match: model.EdgeMatch{Kind: "prefix", Path: "/q/", Methods: []string{"GET"}}, Action: hdr, Order: 7},
			{RouteID: "strip", Match: model.EdgeMatch{Kind: "prefix", Path: "/api"}, Action: strip, Order: 8},
		},
		Upstreams: map[string]map[string]int{"sse": {"http": 9001}},
		FilesPEM:  map[string]PEMPair{"a.example": {Cert: "C", Key: "K"}},
	}
	part := in.Build("")
	raw := jsonOf(t, part.Servers).(map[string]any)
	httpSrv := raw["edge_http"].(map[string]any)
	sub := hostSub(t, httpSrv, "a.example")
	// order: agent, opm_route, then the routes in effective order (exact, prefix-stop, regex, prefix longest first)
	if handler(t, sub[0].(map[string]any)["handle"].([]any), "opm_agent") == nil {
		t.Fatal("agent first")
	}
	or := handler(t, sub[1].(map[string]any)["handle"].([]any), "opm_route")
	if or == nil || len(or["routes"].([]any)) != 9 {
		t.Fatalf("opm_route: %v", sub[1])
	}
	var ids []string
	for _, r := range sub[2:] {
		ms, _ := r.(map[string]any)["match"].([]any)
		if len(ms) == 0 {
			continue
		}
		vars, _ := ms[0].(map[string]any)["vars"].(map[string]any)
		for k, v := range vars {
			ids = append(ids, k+"="+v.([]any)[0].(string))
		}
	}
	want := "opm_route=ex opm_auto=stop opm_route=stop opm_route=rx opm_auto=sse opm_route=sse opm_auto=buf opm_route=buf opm_route=strip opm_auto=hdr opm_route=hdr opm_route=root"
	if strings.Join(ids, " ") != want {
		t.Fatalf("order:\n got %s\nwant %s", strings.Join(ids, " "), want)
	}
	// timeouts, flush, buffering, transport
	hs := routeByVar(t, sub, "opm_route", "sse")
	rp := handler(t, hs, "reverse_proxy")
	tr := rp["transport"].(map[string]any)
	if tr["protocol"] != "opm_http" || tr["dial_timeout"] != "5s" || tr["response_header_timeout"] != "2s" || tr["read_idle_timeout"] != "2s" || tr["write_idle_timeout"] != "7s" ||
		rp["flush_interval"] != -1.0 || rp["request_buffers"] != nil || rp["upstreams"].([]any)[0].(map[string]any)["dial"] != "127.0.0.1:9001" {
		t.Fatalf("sse proxy: %v", rp)
	}
	if bl := handler(t, hs, "opm_body_limit"); bl == nil || bl["max"] != 100.0 {
		t.Fatalf("edge body limit: %v", bl)
	}
	hs = routeByVar(t, sub, "opm_route", "buf")
	rp = handler(t, hs, "reverse_proxy")
	if rp["flush_interval"] != nil || rp["request_buffers"] != 8388608.0 || handler(t, hs, "opm_ws_guard")["allow"] != false || handler(t, hs, "opm_body_limit")["max"] != 10.0 {
		t.Fatalf("buf: %v", hs)
	}
	// response headers: cond (require statuses), always (deferred), remove (deferred) — outermost first
	h0, h1, h2 := hs[0].(map[string]any)["response"].(map[string]any), hs[1].(map[string]any)["response"].(map[string]any), hs[2].(map[string]any)["response"].(map[string]any)
	if h0["require"] == nil || h0["set"].(map[string]any)["Cache-Control"].([]any)[0] != "public" || h0["add"].(map[string]any)["X-A"].([]any)[0] != "{http.request.host}" ||
		h1["deferred"] != true || h1["add"].(map[string]any)["X-B"] == nil || h2["delete"].([]any)[0] != "Pragma" {
		t.Fatalf("response headers: %v %v %v", h0, h1, h2)
	}
	if st := h0["require"].(map[string]any)["status_code"].([]any); len(st) != 10 || st[2] != 204.0 {
		t.Fatalf("statuses %v", st)
	}
	if handler(t, routeByVar(t, sub, "opm_route", "root"), "opm_body_limit") != nil {
		t.Fatal("route maxBodyBytes 0 = unlimited")
	}
	// request headers: defaults, override, hop-by-hop ignored, remove, placeholders, escaped literals; rewrite in opm_upstream
	hs = routeByVar(t, sub, "opm_route", "hdr")
	set := handler(t, hs, "reverse_proxy")["headers"].(map[string]any)["request"].(map[string]any)
	s := set["set"].(map[string]any)
	g := func(k string) string {
		if v, ok := s[k]; ok {
			return v.([]any)[0].(string)
		}
		return "<none>"
	}
	if g("X-Real-IP") != "{http.request.remote.host}" || g("X-Forwarded-For") != "{http.request.remote.host}" || g("X-Forwarded-Proto") != "{http.request.scheme}" ||
		g("X-Forwarded-Host") != "<none>" || g("Connection") != "<none>" || g("X-Lit") != `a\{b\}c $nope` || g("X-Uri") != "{http.vars.opm_uri}{http.vars.opm_is_args}{http.vars.opm_args}" ||
		g("X-H") != "{http.request.header.X-Y}" || set["delete"].([]any)[0] != "X-Forwarded-Host" {
		t.Fatalf("request headers: %v", set)
	}
	if up := handler(t, hs, "opm_upstream"); up["rewrite"].(map[string]any)["regex"] != "^/q/(.*)$" || up["matched"] != "/q/" {
		t.Fatalf("upstream rewrite: %v", up)
	}
	if up := handler(t, routeByVar(t, sub, "opm_route", "strip"), "opm_upstream"); up["stripPrefix"] != true || up["addPrefix"] != "/v1/" || up["matched"] != "/api" {
		t.Fatalf("strip: %v", up)
	}
	// redirect 303 with captures (opm_redirect), auto-redirect, no route for the rewrite route itself
	if rd := handler(t, routeByVar(t, sub, "opm_route", "rx"), "opm_redirect"); rd["status"] != 303.0 || rd["to"] != "/users/$1" {
		t.Fatalf("redirect: %v", rd)
	}
	if rd := handler(t, routeByVar(t, sub, "opm_auto", "sse"), "opm_redirect"); rd["auto"] != true || rd["status"] != 301.0 {
		t.Fatalf("auto: %v", rd)
	}
	if last := sub[len(sub)-1].(map[string]any)["handle"].([]any)[0].(map[string]any); last["handler"] != "error" || last["status_code"] != 404.0 {
		t.Fatalf("last: %v", last)
	}
	// error routes: buf's own always headers, else the host's always; pages by status
	er := httpSrv["errors"].(map[string]any)["routes"].([]any)
	esub := er[0].(map[string]any)["handle"].([]any)[0].(map[string]any)["routes"].([]any)
	if a := routeByVar(t, esub, "opm_route", "buf")[0].(map[string]any)["response"].(map[string]any); a["add"].(map[string]any)["X-B"] == nil || a["set"] != nil {
		t.Fatalf("route always on error pages: %v", a)
	}
	hostAlways := esub[1].(map[string]any)
	if !strings.Contains(string(mustJSON(hostAlways)), `"not":[{"vars":{"opm_route":["buf"]}}]`) || !strings.Contains(string(mustJSON(hostAlways)), `"X-Host"`) {
		t.Fatalf("host always: %s", mustJSON(hostAlways))
	}
	if !strings.Contains(string(mustJSON(er)), `"{http.error.status_code}":["413"]`) {
		t.Fatal("413 page")
	}
	// TLS files: https server listens with a policy by any_tag; http server keeps serving the routes (no redirect)
	https := raw["edge_https"].(map[string]any)
	if !strings.Contains(string(mustJSON(https["tls_connection_policies"])), `"any_tag":["a.example"]`) || hostSub(t, https, "a.example") == nil || len(part.LoadPEM) != 1 {
		t.Fatal("tls files")
	}
	c, k, _ := selfSigned(t, "a.example")
	in.FilesPEM = map[string]PEMPair{"a.example": {Cert: c, Key: k}}
	if err := ValidateInput(in, t.TempDir()); err != nil {
		t.Fatalf("caddy.Validate: %v", err)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestToPlaceholders(t *testing.T) {
	for in, want := range map[string]string{
		"$host":                       "{http.request.host}",
		"{host}{path}{query}":         "{http.request.host}{http.vars.opm_uri}{http.vars.opm_query}",
		"$scheme://$host$request_uri": "{http.request.scheme}://{http.request.host}{http.request.orig_uri}",
		"${server_port}|$1|$x":        "{http.request.local.port}||$x",
		"{json}":                      `\{json\}`,
	} {
		if got := ToPlaceholders(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
