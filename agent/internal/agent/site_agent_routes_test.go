package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

// §21 #121 every site is managed from any site: the agent page's storage, health and remote-GUI cards of another
// site come over the mesh (/_opm/api/sites/{site}/agent/…); the remote-GUI switch needs an actor.
func TestSiteAgentRoutesThroughMesh(t *testing.T) {
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
	peers := []model.PeerInfo{{SiteSlug: "db", PublicKey: sign.PublicKeyB64(db.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(dbSrv), EnrolledAt: now},
		{SiteSlug: "app", PublicKey: sign.PublicKeyB64(app.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(appSrv), EnrolledAt: now}}
	app.storePeers(peers)
	db.storePeers(peers)
	app.mu.Lock()
	app.peerState["db"] = &peerRuntime{lastAnswer: time.Now().Add(time.Hour)}
	app.mu.Unlock()

	key := "ABCDEFGHIJKLMNOPQRSTUVWX"
	_ = app.db.SetKV("keyHash", model.AccessKeyRef{Hash: sign.HashAccessKey(key), Version: 1})
	req := func(method, path, body, cookie, auth string) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, appSrv.URL+path, strings.NewReader(body))
		if cookie != "" {
			r.Header.Set("Cookie", "opm_session_app="+cookie)
		}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		r.Header.Set("Content-Type", "application/json")
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
	resp := req("POST", "/_opm/api/login", `{"accessKey":"`+key+`"}`, "", "")
	ck := resp.Cookies()[0].Value
	resp.Body.Close()
	var ar model.ActorResponse
	jsonOf(req("POST", "/_opm/api/actor", `{"email":"ops@acme.example"}`, ck, ""), &ar)
	auth := "Actor " + ar.ActorToken

	for _, site := range []string{"app", "db"} {
		var st struct {
			Home        string `json:"home"`
			Filesystems []any  `json:"filesystems"`
		}
		if code := jsonOf(req("GET", "/_opm/api/sites/"+site+"/agent/storage", "", ck, ""), &st); code != 200 || st.Home == "" {
			t.Fatalf("%s storage: %d %+v", site, code, st)
		}
		var h struct {
			Version string `json:"version"`
		}
		if code := jsonOf(req("GET", "/_opm/api/sites/"+site+"/agent/health", "", ck, ""), &h); code != 200 || h.Version == "" {
			t.Fatalf("%s health: %d %+v", site, code, h)
		}
	}
	var g model.RemoteGuiSetting
	if code := jsonOf(req("GET", "/_opm/api/sites/db/agent/remote-gui", "", ck, ""), &g); code != 200 || !g.Enabled {
		t.Fatalf("db remote-gui: %d %+v", code, g)
	}
	var e map[string]any
	if code := jsonOf(req("PUT", "/_opm/api/sites/db/agent/remote-gui", `{"enabled":false}`, ck, ""), &e); code != 401 {
		t.Fatalf("switch without actor: %d", code)
	}
	if code := jsonOf(req("PUT", "/_opm/api/sites/db/agent/remote-gui", `{"enabled":false}`, ck, auth), &g); code != 200 || g.Enabled || db.remoteGuiEnabled() {
		t.Fatalf("switch db off from app: %d %+v (db: %v)", code, g, db.remoteGuiEnabled())
	}
	if !app.remoteGuiEnabled() {
		t.Fatal("app's own setting must not change")
	}

	// §21 #126 db's branch is edited from app's GUI: its config, the stage of the commit and the commit itself go to db
	var cfg model.AgentConfigResponse
	if code := jsonOf(req("GET", "/_opm/api/sites/db/config", "", ck, ""), &cfg); code != 200 || cfg.Head != nil {
		t.Fatalf("db config: %d %+v", code, cfg.Head)
	}
	content := "hello db"
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	var st model.Stage
	if code := jsonOf(req("POST", "/_opm/api/sites/db/stages", `{"blobs":[{"sha256":"sha256:`+sha+`","sizeBytes":8}]}`, ck, auth), &st); code != 201 || st.StageID == "" {
		t.Fatalf("db stage: %d %+v", code, st)
	}
	var put map[string]any
	if code := jsonOf(req("PUT", "/_opm/api/sites/db/stages/"+st.StageID+"/blobs/sha256:"+sha, content, ck, auth), &put); code != 200 {
		t.Fatalf("db stage blob: %d %+v", code, put)
	}
	if _, err := os.Stat(db.stageDir(st.StageID)); err != nil {
		t.Fatalf("the stage must be on db: %v", err)
	}
	if _, err := os.Stat(app.stageDir(st.StageID)); err == nil {
		t.Fatal("the stage must not be on app")
	}
	// db has no head yet: db's own answer comes back (stale, "this site has no head yet")
	var ce struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if code := jsonOf(req("POST", "/_opm/api/sites/db/branch/commit", `{"message":"x","baseCommitId":null,"config":{"variables":[],"secrets":[],"files":[],"sites":[]},"stageId":"`+st.StageID+`"}`, ck, auth), &ce); code != 409 || ce.Error.Code != "stale" || !strings.Contains(ce.Error.Message, "no head") {
		t.Fatalf("db commit: %d %+v", code, ce)
	}
}
