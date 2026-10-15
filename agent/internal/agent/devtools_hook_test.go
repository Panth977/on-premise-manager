package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/devtools"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/validate"
)

func devPresetHTTP(url string) model.DevToolPreset {
	p := model.NewDevToolPreset("http", "api")
	p.Name = "Hello API"
	p.Target = model.DevToolTarget{Kind: "url", URL: &url}
	p.Auth.HTTPScheme = "bearer"
	ex := "{{ secret.api-token }}"
	p.Auth.Token = model.DevToolCredential{Kind: "expr", Expr: &ex}
	return p
}

// revisionWithPresets: sites app + db, secret api-token, presets on the given site.
func revisionWithPresets(t *testing.T, seq int, site string, presets []model.DevToolPreset) *model.Revision {
	rev := mkRevision(t, seq, nil, "app", "db")
	v := "tok-SECRET-4242"
	rev.Config.Secrets = []model.SecretDef{{SecretSlug: "api-token", Label: "API token", Value: &v}}
	for i := range rev.Config.Sites {
		if rev.Config.Sites[i].SiteSlug == site {
			l := presets
			rev.Config.Sites[i].Config.DevTools = &l
		}
	}
	h, _ := canonical.HashCanonical(rev.Config)
	rev.Hash = model.Sha256(h)
	return rev
}

// devMesh: two test agents (app, db) on a signed mesh, logged in on app with an actor.
type devMeshT struct {
	app, db *Agent
	appURL  string
	ck      string
	auth    string
}

func newDevMesh(t *testing.T, rev *model.Revision) *devMeshT {
	t.Helper()
	app := newTestAgent(t)
	db := newTestAgentSite(t, "db")
	appSrv := httptest.NewServer(app.handlerFor("agent"))
	dbSrv := httptest.NewServer(db.handlerFor("agent"))
	t.Cleanup(appSrv.Close)
	t.Cleanup(dbSrv.Close)
	port := func(s *httptest.Server) int {
		var p int
		_, ps, _ := strings.Cut(strings.TrimPrefix(s.URL, "http://"), ":")
		for _, c := range ps {
			p = p*10 + int(c-'0')
		}
		return p
	}
	now := nowTs()
	peers := []model.PeerInfo{{SiteSlug: "db", PublicKey: sign.PublicKeyB64(db.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(dbSrv), EnrolledAt: now},
		{SiteSlug: "app", PublicKey: sign.PublicKeyB64(app.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(appSrv), EnrolledAt: now}}
	app.storePeers(peers)
	db.storePeers(peers)
	app.mu.Lock()
	app.peerState["db"] = &peerRuntime{lastAnswer: time.Now().Add(time.Hour)}
	app.mu.Unlock()
	setHead(t, app, rev)
	setHead(t, db, rev)
	key := "ABCDEFGHIJKLMNOPQRSTUVWX"
	_ = app.db.SetKV("keyHash", model.AccessKeyRef{Hash: sign.HashAccessKey(key), Version: 1})
	m := &devMeshT{app: app, db: db, appURL: appSrv.URL}
	resp := m.req(t, "POST", "/_opm/api/login", `{"accessKey":"`+key+`"}`, false)
	m.ck = resp.Cookies()[0].Value
	resp.Body.Close()
	var ar model.ActorResponse
	jsonBody(m.req(t, "POST", "/_opm/api/actor", `{"email":"ops@acme.example"}`, false), &ar)
	m.auth = "Actor " + ar.ActorToken
	return m
}

func (m *devMeshT) req(t *testing.T, method, path, body string, actor bool) *http.Response {
	t.Helper()
	r, _ := http.NewRequest(method, m.appURL+path, strings.NewReader(body))
	if m.ck != "" {
		r.Header.Set("Cookie", "opm_session_app="+m.ck)
	}
	if actor {
		r.Header.Set("Authorization", m.auth)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func jsonBody(resp *http.Response, out any) int {
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(out)
	return resp.StatusCode
}

// Dev tools through the real agent handlers + a signed mesh (SPEC §7.6): auth (401 without a session, actor
// required for executions), "*" refused, a preset of the head revision whose token is {{ secret.api-token }} executed
// on db through app, activities on both sites (devtools.query on db with via, devtools.action-forwarded on app),
// no secret in any activity, results bound to the forwarding session, PUT / reveal forwarded.
func TestDevToolsThroughAgentAndMesh(t *testing.T) {
	var mu sync.Mutex
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"hello":"world"}`)
	}))
	defer up.Close()
	rev := revisionWithPresets(t, 1, "db", []model.DevToolPreset{devPresetHTTP(up.URL)})
	m := newDevMesh(t, rev)
	app, db := m.app, m.db
	ck, auth := m.ck, m.auth
	req := func(method, path, body, cookie, authz string) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, m.appURL+path, strings.NewReader(body))
		if cookie != "" {
			r.Header.Set("Cookie", "opm_session_app="+cookie)
		}
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	jsonOf := jsonBody
	var e map[string]any
	if st := jsonOf(req("GET", "/_opm/api/sites/app/devtools/info", "", "", ""), &e); st != 401 {
		t.Fatalf("no session → %d", st)
	}
	var info struct {
		SiteSlug string `json:"siteSlug"`
		Enabled  bool   `json:"enabled"`
	}
	if st := jsonOf(req("GET", "/_opm/api/sites/app/devtools/info", "", ck, ""), &info); st != 200 || info.SiteSlug != "app" || !info.Enabled {
		t.Fatalf("info %d %+v", st, info)
	}
	if st := jsonOf(req("POST", "/_opm/api/sites/app/devtools/connections", `{}`, ck, ""), &e); st != 401 || !strings.Contains(asJSON(e), "actor-required") {
		t.Fatalf("write without actor → %d %v", st, e)
	}
	if st := jsonOf(req("GET", "/_opm/api/sites/*/devtools/connections", "", ck, ""), &e); st != 422 {
		t.Fatalf(`"*" → %d`, st)
	}
	// db's presets through app
	var list model.ListResponse[map[string]any]
	if st := jsonOf(req("GET", "/_opm/api/sites/db/devtools/connections", "", ck, ""), &list); st != 200 || len(list.Items) != 1 || list.Items[0]["id"] != "p:api" || list.Items[0]["siteSlug"] != "db" {
		t.Fatalf("forwarded list %d %+v", st, list)
	}
	if st := jsonOf(req("GET", "/_opm/api/sites/app/devtools/connections", "", ck, ""), &list); st != 200 || len(list.Items) != 0 {
		t.Fatalf("app has no presets %d %+v", st, list)
	}
	// execute the preset on db (forwarded with the actor)
	var hr map[string]any
	body := `{"queryId":"q1","request":{"method":"GET","url":"/hello?token={{ secret.api-token }}","query":[],"headers":[],"body":{"kind":"none"},"auth":null,"followRedirects":null,"tlsInsecure":null,"timeoutSec":null}}`
	if st := jsonOf(req("POST", "/_opm/api/sites/db/devtools/connections/p:api/http", body, ck, ""), &e); st != 401 {
		t.Fatalf("forwarded execute without actor %d", st)
	}
	if st := jsonOf(req("POST", "/_opm/api/sites/db/devtools/connections/p:api/http", body, ck, auth), &hr); st != 200 || hr["ok"] != true || hr["status"].(float64) != 200 {
		t.Fatalf("forwarded http %d %+v", st, hr)
	}
	mu.Lock()
	if gotAuth != "Bearer tok-SECRET-4242" {
		t.Fatalf("token not resolved on the site: %q", gotAuth)
	}
	mu.Unlock()
	if strings.Contains(asJSON(hr["sent"]), "tok-SECRET-4242") {
		t.Fatalf("sent leaks the secret: %v", hr["sent"])
	}
	// the result body is bound to the forwarding session
	rid, _ := hr["resultId"].(string)
	r2 := req("GET", "/_opm/api/sites/db/devtools/results/"+rid+"/body", "", ck, "")
	b, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != 200 || string(b) != `{"hello":"world"}` {
		t.Fatalf("forwarded body %d %s", r2.StatusCode, b)
	}
	// activities on both sites, no secret anywhere
	acts := func(a *Agent, typ string) []string {
		rows, _ := a.db.Query(`SELECT actor, data FROM activities WHERE type = ? ORDER BY seq`, typ)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var actor *string
			var d string
			_ = rows.Scan(&actor, &d)
			who := ""
			if actor != nil {
				who = *actor
			}
			out = append(out, who+" "+d)
		}
		return out
	}
	q := acts(db, "devtools.query")
	if len(q) != 1 || !strings.HasPrefix(q[0], "ops@acme.example ") || !strings.Contains(q[0], `"via":"app"`) || !strings.Contains(q[0], `"source":"preset"`) || !strings.Contains(q[0], `"status":200`) {
		t.Fatalf("db devtools.query %v", q)
	}
	f := acts(app, "devtools.action-forwarded")
	if len(f) != 1 || !strings.Contains(f[0], `"forwardedTo":"db"`) || !strings.Contains(f[0], `"connectionId":"p:api"`) || !strings.Contains(f[0], `"status":200`) {
		t.Fatalf("app devtools.action-forwarded %v", f)
	}
	for _, a := range []*Agent{app, db} {
		rows, _ := a.db.Query(`SELECT data FROM activities`)
		for rows.Next() {
			var d string
			_ = rows.Scan(&d)
			if strings.Contains(d, "tok-SECRET-4242") {
				t.Fatalf("activity leaks the secret: %s", d)
			}
		}
		rows.Close()
	}
	// manual connection on db: create, PUT, reveal (forwarded; no-store)
	in := `{"name":"Local API","description":"","kind":"http","target":{"kind":"url","url":"` + up.URL + `"},"auth":{"username":null,"password":{"kind":"none"},"database":null,"clientId":null,"httpScheme":"bearer","token":{"kind":"literal","value":"lit-token-777"},"apiKeyHeader":null},"options":{"tls":false,"tlsInsecure":false,"tlsServerName":null,"sslMode":null,"timeoutSec":null,"maxRows":null,"consistency":null,"scheme":null,"basePath":null,"followRedirects":null,"defaultHeaders":[],"cleanSession":null,"keepAliveSec":null},"readOnly":false}`
	var conn map[string]any
	if st := jsonOf(req("POST", "/_opm/api/sites/db/devtools/connections", in, ck, auth), &conn); st != 201 || strings.Contains(asJSON(conn), "lit-token-777") {
		t.Fatalf("forwarded create %d %v", st, conn)
	}
	id := conn["id"].(string)
	upd := strings.Replace(strings.Replace(in, "Local API", "Local API 2", 1), "lit-token-777", model.Mask, 1)
	if st := jsonOf(req("PUT", "/_opm/api/sites/db/devtools/connections/"+id, upd, ck, auth), &conn); st != 200 || conn["name"] != "Local API 2" {
		t.Fatalf("forwarded PUT %d %v", st, conn)
	}
	rr := req("POST", "/_opm/api/sites/db/devtools/connections/"+id+"/reveal?field=token", "", ck, auth)
	var rv devtools.RevealResponse
	if st := jsonOf(rr, &rv); st != 200 || rv.Value != "lit-token-777" || rr.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("forwarded reveal %d %+v %v", st, rv, rr.Header)
	}
	if r := acts(db, "devtools.credential-revealed"); len(r) != 1 || !strings.Contains(r[0], `"via":"app"`) {
		t.Fatalf("reveal activity %v", r)
	}
	// history on db (forwarded GET)
	var hist model.PageResponse[map[string]any]
	if st := jsonOf(req("GET", "/_opm/api/sites/db/devtools/history?connectionId=p:api", "", ck, ""), &hist); st != 200 || len(hist.Items) != 1 || hist.Items[0]["via"] != "app" {
		t.Fatalf("history %d %+v", st, hist)
	}
	// switch off on db: agent.json tools.devTools false → 403 with the reason (through the mesh too)
	cfg := db.DevTools().Config()
	cfg.Enabled = false
	db.DevTools().SetConfig(cfg)
	if st := jsonOf(req("GET", "/_opm/api/sites/db/devtools/connections", "", ck, ""), &e); st != 403 || !strings.Contains(asJSON(e), "tools.devTools") {
		t.Fatalf("switch off %d %v", st, e)
	}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// §21 #97 c: a local edit keeps the head presets when the input carries none; §21 #113: changed / removed presets
// are local edits (no V_LOCAL_* issue).
func TestLocalEditDevTools(t *testing.T) {
	rev := revisionWithPresets(t, 1, "db", []model.DevToolPreset{devPresetHTTP("http://127.0.0.1:9")})
	head := &rev.Config
	in := model.ConfigInput{Variables: head.Variables, Files: head.Files, Sites: append([]model.Site(nil), head.Sites...)}
	for _, s := range head.Secrets {
		in.Secrets = append(in.Secrets, model.SecretDefInput{SecretSlug: s.SecretSlug, Label: s.Label})
	}
	for i := range in.Sites {
		in.Sites[i].Config.DevTools = nil // the GUI's ConfigInput does not carry presets
	}
	cfg := buildLocalConfig(head, in)
	if l := siteOf(&cfg, "db").Config.DevToolList(); len(l) != 1 || l[0].PresetSlug != "api" {
		t.Fatalf("presets not copied: %+v", l)
	}
	if siteOf(&cfg, "app").Config.DevTools != nil {
		t.Fatal("an empty devTools must stay absent")
	}
	for _, is := range validate.CheckLocalEdit(head, &cfg) {
		if is.Code == "V_LOCAL_DEVTOOLS" {
			t.Fatal("unchanged presets refused")
		}
	}
	changed := devPresetHTTP("http://127.0.0.1:10").Detached()
	l := []model.DevToolPreset{changed}
	for i := range in.Sites {
		if in.Sites[i].SiteSlug == "db" {
			in.Sites[i].Config.DevTools = &l
		}
	}
	cfg = buildLocalConfig(head, in)
	// §21 #113: a changed preset is a local edit
	if got := siteOf(&cfg, "db").Config.DevToolList(); len(got) != 1 {
		t.Fatalf("changed preset not taken: %+v", got)
	}
	if l := validate.CheckLocalEdit(head, &cfg); len(l) != 0 {
		t.Fatal(l)
	}
	// removing all presets is a change too
	empty := []model.DevToolPreset{}
	for i := range in.Sites {
		if in.Sites[i].SiteSlug == "db" {
			in.Sites[i].Config.DevTools = &empty
		}
	}
	cfg = buildLocalConfig(head, in)
	if siteOf(&cfg, "db").Config.DevTools != nil {
		t.Fatal("empty list must be normalized to absent")
	}
	if l := validate.CheckLocalEdit(head, &cfg); len(l) != 0 {
		t.Fatal(l)
	}
}

// The MQTT live stream of a connection on db, opened and read through app (§7.6: the SSE stream is copied with
// flushes; the single-use ticket is bound to the forwarding site), with a real mosquitto on Docker.
func TestDevToolsMQTTStreamThroughMesh(t *testing.T) {
	if testing.Short() {
		t.Skip("docker (-short)")
	}
	needDocker(t)
	name := fmt.Sprintf("opm-dt-mqtt-%d", time.Now().UnixNano()%1e8)
	out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", "127.0.0.1::1883", "eclipse-mosquitto:2.0.14", "mosquitto", "-c", "/mosquitto-no-auth.conf").CombinedOutput()
	if err != nil {
		t.Skip("mosquitto: ", string(out))
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	pout, _ := exec.Command("docker", "port", name, "1883/tcp").Output()
	_, portS, _ := strings.Cut(strings.TrimSpace(strings.Split(string(pout), "\n")[0]), ":")
	m := newDevMesh(t, revisionWithPresets(t, 1, "db", nil))
	in := `{"name":"Broker","description":"","kind":"mqtt","target":{"kind":"host","host":"127.0.0.1","port":` + portS + `},"auth":{"username":null,"password":{"kind":"none"},"database":null,"clientId":null,"httpScheme":"none","token":{"kind":"none"},"apiKeyHeader":null},"options":{"tls":false,"tlsInsecure":false,"tlsServerName":null,"sslMode":null,"timeoutSec":null,"maxRows":null,"consistency":null,"scheme":null,"basePath":null,"followRedirects":null,"defaultHeaders":[],"cleanSession":null,"keepAliveSec":null},"readOnly":false}`
	var conn map[string]any
	if st := jsonBody(m.req(t, "POST", "/_opm/api/sites/db/devtools/connections", in, true), &conn); st != 201 {
		t.Fatalf("create %d %v", st, conn)
	}
	id := conn["id"].(string)
	var sess map[string]any
	for i := 0; i < 30; i++ { // mosquitto needs a moment to listen
		if st := jsonBody(m.req(t, "POST", "/_opm/api/sites/db/devtools/connections/"+id+"/mqtt/sessions", `{"subscriptions":[{"filter":"plant/+/temp","qos":0}]}`, true), &sess); st == 201 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	path, _ := sess["streamPath"].(string)
	if !strings.HasPrefix(path, "/_opm/api/sites/db/devtools/connections/"+id+"/mqtt/sessions/") {
		t.Fatalf("session %v", sess)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", m.appURL+path, nil)
	r.Header.Set("Cookie", "opm_session_app="+m.ck)
	resp, err := http.DefaultClient.Do(r)
	if err != nil || resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("forwarded stream %v %v", err, resp)
	}
	go func() {
		time.Sleep(700 * time.Millisecond)
		jsonBody(m.req(t, "POST", "/_opm/api/sites/db/devtools/connections/"+id+"/mqtt/publish", `{"topic":"plant/l1/temp","payload":"21.5","encoding":"utf8","qos":0,"retain":false}`, true), &map[string]any{})
	}()
	sc := bufio.NewScanner(resp.Body)
	got := ""
	for sc.Scan() {
		if l := sc.Text(); strings.HasPrefix(l, "data: ") && strings.Contains(l, `"topic":"plant/l1/temp"`) {
			got = l
			break
		}
	}
	resp.Body.Close()
	if !strings.Contains(got, `"text":"21.5"`) {
		t.Fatalf("message through the mesh: %q", got)
	}
	waitFor(t, "mqtt-unsubscribed on db", 5*time.Second, func() bool {
		var n int
		_ = m.db.db.QueryRow(`SELECT count(*) FROM activities WHERE type = 'devtools.mqtt-unsubscribed' AND data LIKE '%"via":"app"%'`).Scan(&n)
		return n == 1
	})
	var n int
	_ = m.app.db.QueryRow(`SELECT count(*) FROM activities WHERE type = 'devtools.action-forwarded'`).Scan(&n)
	if n != 3 { // create, session, publish
		t.Fatalf("forwarded actions on app: %d", n)
	}
}
