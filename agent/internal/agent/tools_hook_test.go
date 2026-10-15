package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/edge"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/tools"
	"opm/agent/internal/validate"
)

// Operator toolbox through the real agent handlers (SPEC §21 #89): session / actor auth, cross-origin refusal, the
// host shell on another site through the mesh (createTerminal forwarded with the actor, WebSocket bridged, activities
// on BOTH sites), SSE forwarded with flushes, "*" merge.
func TestToolsThroughAgentAndMesh(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no pty")
	}
	app := newTestAgent(t)
	db := newTestAgentSite(t, "db")
	appSrv := httptest.NewServer(app.handlerFor("agent"))
	dbSrv := httptest.NewServer(db.handlerFor("agent"))
	defer appSrv.Close()
	defer dbSrv.Close()
	port := func(s *httptest.Server) int {
		var p int
		_, ps, _ := strings.Cut(strings.TrimPrefix(s.URL, "http://"), ":")
		for _, c := range ps {
			p = p*10 + int(c-'0')
		}
		return p
	}
	now := nowTs()
	app.storePeers([]model.PeerInfo{{SiteSlug: "db", PublicKey: sign.PublicKeyB64(db.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(dbSrv), EnrolledAt: now},
		{SiteSlug: "app", PublicKey: sign.PublicKeyB64(app.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(appSrv), EnrolledAt: now}})
	db.storePeers([]model.PeerInfo{{SiteSlug: "app", PublicKey: sign.PublicKeyB64(app.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(appSrv), EnrolledAt: now},
		{SiteSlug: "db", PublicKey: sign.PublicKeyB64(db.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(dbSrv), EnrolledAt: now}})
	app.mu.Lock()
	app.peerState["db"] = &peerRuntime{lastAnswer: time.Now().Add(time.Hour)}
	app.mu.Unlock()

	key := "ABCDEFGHIJKLMNOPQRSTUVWX"
	_ = app.db.SetKV("keyHash", model.AccessKeyRef{Hash: sign.HashAccessKey(key), Version: 1})
	req := func(method, path, body, cookie, auth string, hdr map[string]string) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, appSrv.URL+path, strings.NewReader(body))
		if cookie != "" {
			r.Header.Set("Cookie", "opm_session_app="+cookie)
		}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	jsonOf := func(resp *http.Response, out any) int {
		defer resp.Body.Close()
		_ = json.NewDecoder(resp.Body).Decode(out)
		return resp.StatusCode
	}
	var lr model.LoginResponse
	resp := req("POST", "/_opm/api/login", `{"accessKey":"`+key+`"}`, "", "", nil)
	ck := resp.Cookies()[0].Value
	jsonOf(resp, &lr)
	var ar model.ActorResponse
	jsonOf(req("POST", "/_opm/api/actor", `{"email":"ops@acme.example"}`, ck, "", nil), &ar)
	auth := "Actor " + ar.ActorToken

	// auth rules
	var e map[string]any
	if st := jsonOf(req("GET", "/_opm/api/sites/app/tools/info", "", "", "", nil), &e); st != 401 {
		t.Fatalf("no session → %d", st)
	}
	var info struct {
		SiteSlug  string `json:"siteSlug"`
		HostShell struct {
			Enabled bool `json:"enabled"`
		} `json:"hostShell"`
	}
	if st := jsonOf(req("GET", "/_opm/api/sites/app/tools/info", "", ck, "", nil), &info); st != 200 || info.SiteSlug != "app" || !info.HostShell.Enabled {
		t.Fatalf("info %d %+v", st, info)
	}
	if st := jsonOf(req("POST", "/_opm/api/sites/app/tools/terminal/sessions", `{"kind":"host","confirm":"app"}`, ck, "", nil), &e); st != 401 {
		t.Fatalf("write without actor → %d", st)
	}
	var merged struct {
		Items []map[string]any `json:"items"`
	}
	if st := jsonOf(req("GET", "/_opm/api/sites/*/tools/info", "", ck, "", nil), &merged); st != 200 || len(merged.Items) != 2 {
		t.Fatalf("merged info %d %+v", st, merged)
	}

	// host shell on db through app (forwarded create, bridged WebSocket)
	var cr map[string]any
	if st := jsonOf(req("POST", "/_opm/api/sites/db/tools/terminal/sessions", `{"kind":"host","confirm":"db"}`, ck, auth, nil), &cr); st != 201 {
		t.Fatalf("forwarded create %d %+v", st, cr)
	}
	wsURL := "ws" + strings.TrimPrefix(appSrv.URL, "http") + cr["wsPath"].(string)
	if !strings.Contains(wsURL, "/sites/db/tools/terminal/ws?ticket=") {
		t.Fatal(wsURL)
	}
	if _, resp, err := tools.DialWS(wsURL, http.Header{"Cookie": {"opm_session_app=" + ck}, "Origin": {"https://evil.example"}}); err == nil || resp.StatusCode != 403 {
		t.Fatal("cross-origin WebSocket must be refused")
	}
	ws, _, err := tools.DialWS(wsURL, http.Header{"Cookie": {"opm_session_app=" + ck}, "Origin": {appSrv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.WriteMessage(tools.OpBinary, []byte("echo bridged-$((6*7)) $OPM_SITE; exit 0\n"))
	var out strings.Builder
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		op, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if op == tools.OpBinary {
			out.Write(msg)
		} else if strings.Contains(string(msg), `"exit"`) {
			break
		}
	}
	ws.Close()
	if !strings.Contains(out.String(), "bridged-42 db") {
		t.Fatalf("bridged output %q", out.String())
	}
	types := func(a *Agent) []string {
		var out []string
		waitFor(t, "activities", 5*time.Second, func() bool {
			out = nil
			rows, _ := a.db.Query(`SELECT type, actor, data FROM activities WHERE type LIKE 'terminal.%' ORDER BY seq`)
			defer rows.Close()
			for rows.Next() {
				var typ, data string
				var actor *string
				_ = rows.Scan(&typ, &actor, &data)
				who := ""
				if actor != nil {
					who = *actor
				}
				via := ""
				if strings.Contains(data, `"via":"app"`) {
					via = "<via app>"
				}
				if strings.Contains(data, `"forwardedTo":"db"`) {
					via = "<to db>"
				}
				out = append(out, typ+" "+who+via)
			}
			return len(out) == 2
		})
		return out
	}
	if got := strings.Join(types(db), ","); got != "terminal.opened ops@acme.example<via app>,terminal.closed ops@acme.example<via app>" {
		t.Fatalf("db activities %s", got)
	}
	if got := strings.Join(types(app), ","); got != "terminal.opened ops@acme.example<to db>,terminal.closed ops@acme.example<to db>" {
		t.Fatalf("app activities %s", got)
	}

	// SSE forwarded with flushes: db's agent log, follow
	db.log.Info("tools", "marker-before")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", appSrv.URL+"/_opm/api/sites/db/tools/logs/agent?follow=1&tail=5", nil)
	r.Header.Set("Cookie", "opm_session_app="+ck)
	sresp, err := http.DefaultClient.Do(r)
	if err != nil || sresp.StatusCode != 200 || !strings.HasPrefix(sresp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(err, sresp.StatusCode)
	}
	go func() { time.Sleep(1500 * time.Millisecond); db.log.Info("tools", "marker-after") }()
	sc := bufio.NewScanner(sresp.Body)
	seen := ""
	for sc.Scan() {
		if strings.Contains(sc.Text(), "marker-after") {
			seen = sc.Text()
			break
		}
	}
	sresp.Body.Close()
	if seen == "" {
		t.Fatal("followed line not received through the mesh")
	}
}

func newTestAgentSite(t *testing.T, site string) *Agent {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Options{Config: model.AgentConfigFile{APIBase: "http://127.0.0.1:1", DeploymentID: "DepUnit", SiteSlug: site, DataDir: dir, AdvertiseHost: "127.0.0.1",
		AgentPort: freePort(t), GuiAliasPort: model.Some(freePort(t)), EdgeHTTPPort: model.Null[int]()}, Timings: fastTimings()})
	if err != nil {
		t.Fatal(err)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() { a.cancel(); a.db.Close() })
	return a
}

// The edge access log (§21 #89 a): every edge request becomes a JSON line in <agentHome>/logs/edge-access.log, and the
// access-log SSE stream serves it.
func TestEdgeAccessLog(t *testing.T) {
	dir := t.TempDir()
	edgePort := freePort(t)
	a, err := New(Options{Config: model.AgentConfigFile{APIBase: "http://127.0.0.1:1", DeploymentID: "DepAccessLog", SiteSlug: "app", DataDir: dir, AdvertiseHost: "127.0.0.1",
		AgentPort: freePort(t), GuiAliasPort: model.Some(freePort(t)), EdgeHTTPPort: model.Some(edgePort)}, Timings: fastTimings()})
	if err != nil {
		t.Fatal(err)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	edge.Register(a.instance, a)
	t.Cleanup(func() {
		a.cancel()
		a.mu.Lock()
		a.cfg.EdgeHTTPPort = model.Null[int]()
		a.mu.Unlock()
		c := validate.EmptyConfig()
		_ = a.reloadEdge(&c, emptyCatalog(), 0, false)
		edge.Unregister(a.instance)
		a.db.Close()
	})
	if err := a.reloadCurrentEdge(false); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello?x=1", edgePort))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var line string
	waitFor(t, "access log line", 5*time.Second, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "logs", "edge-access.log"))
		line = string(b)
		return strings.Contains(line, `"/hello?x=1"`)
	})
	var ln struct {
		Method string `json:"method"`
		URI    string `json:"uri"`
		Status int    `json:"status"`
	}
	_ = json.Unmarshal([]byte(strings.SplitN(line, "\n", 2)[0]), &ln)
	if ln.Method != "GET" || ln.URI != "/hello?x=1" || ln.Status != resp.StatusCode {
		t.Fatalf("line %s (status %d)", line, resp.StatusCode)
	}
}
