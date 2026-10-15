package edge

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/model"
)

type fakeHost struct{ ready map[string]bool }

func (f *fakeHost) ServeAgent(w http.ResponseWriter, r *http.Request, surface string) {
	if strings.HasPrefix(r.URL.Path, "/_opm/__peer") {
		http.Error(w, "agent 404", 404)
		return
	}
	w.Header().Set("X-Surface", surface)
	_, _ = io.WriteString(w, "agent:"+r.URL.Path)
}
func (f *fakeHost) Ready(id string) bool { return f.ready[id] }

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func strp(s string) *string { return &s }

func TestEdgeRouting(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.RequestURI(), "site": r.Header.Get("X-Opm-Site"), "xfh": r.Header.Get("X-Forwarded-Host"), "drop": r.Header.Get("X-Drop")})
	}))
	defer up.Close()
	upPort, _ := strconv.Atoi(strings.TrimPrefix(up.URL[strings.LastIndex(up.URL, ":"):], ":"))
	host := &fakeHost{ready: map[string]bool{"api": true, "web": true}}
	Register("t1", host)
	defer Unregister("t1")
	port := freePort(t)
	in := &Input{Instance: "t1", SiteSlug: "app", HTTPPort: port,
		Hosts: []model.EdgeHost{{Hostname: "*", TLS: model.EdgeTls{Mode: "none"}}, {Hostname: "special.test", TLS: model.EdgeTls{Mode: "none"}}},
		Routes: []model.EdgeRoute{
			{RouteID: "api", Order: 1, Match: model.EdgeMatch{Kind: "prefix", Path: "/api"}, Action: model.EdgeRouteAction{Type: "proxy", ContainerSlug: "api", PortName: "http", StripPrefix: true, SetHeaders: model.MapOf("X-Opm-Site", "app"), RemoveHeaders: []string{"X-Drop"}, ConnectTimeoutSec: 5, SendTimeoutSec: 5, ReadTimeoutSec: 5}},
			{RouteID: "old", Order: 2, Match: model.EdgeMatch{Kind: "prefix", Path: "/old"}, Action: model.EdgeRouteAction{Type: "redirect", To: "/", Status: 308}},
			{RouteID: "keep", Order: 2, Match: model.EdgeMatch{Kind: "prefix", Path: "/k"}, Action: model.EdgeRouteAction{Type: "redirect", To: "https://x.test/new/", Status: 302, KeepPath: true}},
			{RouteID: "pre", Order: 3, Hosts: []string{"special.test"}, Match: model.EdgeMatch{Kind: "prefix", Path: "/p", Methods: []string{"POST"}}, Action: model.EdgeRouteAction{Type: "proxy", ContainerSlug: "web", PortName: "http", AddPrefix: strp("/base/"), ConnectTimeoutSec: 5, SendTimeoutSec: 5, ReadTimeoutSec: 5}},
			{RouteID: "web", Order: 4, Match: model.EdgeMatch{Kind: "prefix", Path: "/w"}, Action: model.EdgeRouteAction{Type: "proxy", ContainerSlug: "down", PortName: "http", ConnectTimeoutSec: 5, SendTimeoutSec: 5, ReadTimeoutSec: 5}},
		},
		NotFound404: "<h1>Hello 404</h1>",
		Upstreams:   map[string]map[string]int{"api": {"http": upPort}, "web": {"http": upPort}},
	}
	raw, err := Apply("t1", t.TempDir(), in.Build(""), false)
	if err != nil {
		t.Fatal(err)
	}
	defer Stop("t1")
	if !strings.Contains(string(raw), "edge_http") {
		t.Fatal("json missing server")
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}
	get := func(method, path, host string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, base+path, nil)
		if host != "" {
			req.Host = host
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	r, b := get("GET", "/api/count?x=1", "", map[string]string{"X-Drop": "1"})
	if r.StatusCode != 200 || !strings.Contains(b, `"path":"/count?x=1"`) || !strings.Contains(b, `"site":"app"`) || !strings.Contains(b, `"drop":""`) {
		t.Fatalf("api: %d %s", r.StatusCode, b)
	}
	r, b = get("GET", "/nope-at-all", "special.test", nil)
	if r.StatusCode != 404 || !strings.Contains(b, "Hello 404") {
		t.Fatalf("not found: %d %s", r.StatusCode, b)
	}
	r, b = get("GET", "/api", "", nil)
	if r.StatusCode != 200 || !strings.Contains(b, `"path":"/"`) {
		t.Fatalf("api root: %d %s", r.StatusCode, b)
	}
	r, b = get("GET", "/apix", "", nil) // nginx plain prefix: /api matches /apix
	if r.StatusCode != 200 || !strings.Contains(b, `"path":"/x"`) {
		t.Fatalf("apix: %d %s", r.StatusCode, b)
	}
	r, _ = get("GET", "/old/x", "", nil)
	if r.StatusCode != 308 || r.Header.Get("Location") != "/" {
		t.Fatalf("old: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	r, _ = get("GET", "/k/a/b?q=1", "", nil)
	if r.StatusCode != 302 || r.Header.Get("Location") != "https://x.test/new/a/b?q=1" {
		t.Fatalf("keep: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	r, b = get("POST", "/p/z", "special.test", nil)
	if r.StatusCode != 200 || !strings.Contains(b, `"path":"/base/p/z"`) {
		t.Fatalf("pre: %d %s", r.StatusCode, b)
	}
	r, _ = get("GET", "/p/z", "special.test", nil)
	if r.StatusCode != 404 {
		t.Fatalf("pre GET should 404: %d", r.StatusCode)
	}
	r, b = get("GET", "/w", "", nil)
	if r.StatusCode != 503 || !strings.Contains(b, "Hello 404") {
		t.Fatalf("not ready: %d %s", r.StatusCode, b)
	}
	host.ready["down"] = true // ready but nothing listens → dial error → 503 page
	r, b = get("GET", "/w", "", nil)
	if r.StatusCode != 503 {
		t.Fatalf("dial error: %d %s", r.StatusCode, b)
	}
	r, b = get("GET", "/api/ws", "", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"})
	if r.StatusCode != 400 {
		t.Fatalf("ws: %d %s", r.StatusCode, b)
	}
	r, b = get("GET", "/_opm/app", "", nil)
	if r.StatusCode != 200 || b != "agent:/_opm/app" || r.Header.Get("X-Surface") != "edge" {
		t.Fatalf("agent: %d %s", r.StatusCode, b)
	}
	r, _ = get("GET", "/_opm/peer/ping", "", nil)
	if r.StatusCode != 404 {
		t.Fatalf("peer on edge: %d", r.StatusCode)
	}
	// reload with a broken listener keeps the old config
	in2 := *in
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	in2.HTTPPort = busy.Addr().(*net.TCPAddr).Port // already bound
	if _, err := Apply("t1", t.TempDir(), in2.Build(""), false); err == nil {
		t.Fatal("expected bind failure")
	} else if BindPort(err) != in2.HTTPPort {
		t.Logf("bind port not parsed from %v", err)
	}
	r, _ = get("GET", "/api/x", "", nil)
	if r.StatusCode != 200 {
		t.Fatalf("old config not kept: %d", r.StatusCode)
	}
}

func TestEdgeTLSInternal(t *testing.T) {
	host := &fakeHost{ready: map[string]bool{}}
	Register("t2", host)
	defer Unregister("t2")
	hp, sp := freePort(t), freePort(t)
	root := t.TempDir()
	in := &Input{Instance: "t2", SiteSlug: "s", HTTPPort: hp, HTTPSPort: &sp,
		Hosts: []model.EdgeHost{{Hostname: "localhost", TLS: model.EdgeTls{Mode: "internal"}, RedirectHTTPToHTTPS: true}}}
	if _, err := Apply("t2", root, in.Build(""), false); err != nil {
		t.Fatal(err)
	}
	defer Stop("t2")
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(hp)+"/a?b=1", nil)
	req.Host = "localhost"
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 308 || resp.Header.Get("Location") != "https://localhost:"+strconv.Itoa(sp)+"/a?b=1" {
		t.Fatalf("redirect %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// wait for the internal certificate
	var cs model.CertStatus
	for i := 0; i < 50; i++ {
		cs = CertStatusFor(root, "s", in.Hosts[0], nil)
		if cs.NotAfter != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if cs.NotAfter == nil {
		t.Fatal("no internal certificate issued")
	}
}
