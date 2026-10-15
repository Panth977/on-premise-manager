package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

// §21 #95 hold-to-fast-forward — the same cases as shared/src/intent-wait.test.ts.

var iwT0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestIntentWaitOnlyX1(t *testing.T) {
	s := newIntentWait(60*time.Second, iwT0)
	s.advance(iwT0.Add(ms(59_999)))
	if s.ready() {
		t.Fatal("ready before 60 s at ×1")
	}
	s.advance(iwT0.Add(60 * time.Second))
	if !s.ready() || s.fastForward("a@x") != nil {
		t.Fatalf("ready %v ff %+v", s.ready(), s.fastForward("a@x"))
	}
}

func TestIntentWaitMinimumIsQuarter(t *testing.T) {
	s := newIntentWait(60*time.Second, iwT0)
	now := iwT0
	_ = s.setSpeed(4, now)
	for !s.ready() {
		now = now.Add(ms(model.IntentSpeedHeartbeatMs))
		s.advance(now)
		if !s.ready() {
			_ = s.setSpeed(4, now)
		}
	}
	if d := now.Sub(iwT0); d != 15*time.Second {
		t.Fatalf("min real wait %v, want 15 s (60 / 4)", d)
	}
	ff := s.fastForward("a@x")
	if ff == nil || ff.MaxSpeed != 4 || ff.RealSeconds != 15 || ff.EffectiveSeconds != 60 || ff.UsedBy != "a@x" {
		t.Fatalf("ff %+v", ff)
	}
	if fastForwardLabel(ff) != "fast-forwarded (×4, 15 s real)" {
		t.Fatal(fastForwardLabel(ff))
	}
}

func TestIntentWaitHeartbeatDecay(t *testing.T) {
	s := newIntentWait(60*time.Second, iwT0)
	_ = s.setSpeed(4, iwT0)
	s.advance(iwT0.Add(10 * time.Second))
	if s.Effective != 4*1.5+8.5 || s.Speed != 1 {
		t.Fatalf("effective %v speed %d, want 14.5 / ×1 (a lost release decays after 1.5 s)", s.Effective, s.Speed)
	}
}

func TestIntentWaitPiecewiseAndCap(t *testing.T) {
	s := newIntentWait(60*time.Second, iwT0)
	_ = s.setSpeed(2, iwT0.Add(ms(1000)))
	_ = s.setSpeed(4, iwT0.Add(ms(2000)))
	_ = s.setSpeed(1, iwT0.Add(ms(3000)))
	s.advance(iwT0.Add(ms(4000)))
	if s.Effective != 1+2+4+1 || s.MaxSpeed != 4 {
		t.Fatalf("effective %v max %d", s.Effective, s.MaxSpeed)
	}
	for _, bad := range []float64{0, 3, 8, -1, 4.5, 100} {
		if err := s.setSpeed(bad, iwT0.Add(ms(4000))); err == nil {
			t.Fatalf("speed %v accepted (only 1 | 2 | 4, at most ×4)", bad)
		}
	}
	// clock backwards credits nothing
	s.advance(iwT0.Add(ms(1000)))
	if s.Effective != 8 {
		t.Fatal(s.Effective)
	}
}

func TestIntentWaitReadyInterpolated(t *testing.T) {
	s := newIntentWait(10*time.Second, iwT0)
	_ = s.setSpeed(2, iwT0.Add(ms(4000)))
	s.advance(iwT0.Add(ms(5000)))
	_ = s.setSpeed(2, iwT0.Add(ms(5000)))
	s.advance(iwT0.Add(ms(6500)))
	_ = s.setSpeed(2, iwT0.Add(ms(6500)))
	s.advance(iwT0.Add(ms(8000)))
	if ff := s.fastForward("b@x"); ff == nil || ff.RealSeconds != 7 {
		t.Fatalf("%+v", ff)
	}
}

// agentSession logs in + declares an actor on a test agent handler; returns cookie value + Authorization.
func agentSession(t *testing.T, a *Agent, h http.Handler, email string) (string, string) {
	t.Helper()
	key := "ABCDEFGHIJKLMNOPQRSTUVWX"
	_ = a.db.SetKV("keyHash", model.AccessKeyRef{Hash: sign.HashAccessKey(key), Version: 1})
	do := func(path, body, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.RemoteAddr = "10.0.0.9:1234"
		if cookie != "" {
			r.Header.Set("Cookie", "opm_session_"+a.site+"="+cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	rec := do("/_opm/api/login", `{"accessKey":"`+key+`"}`, "")
	if rec.Code != 200 {
		t.Fatalf("login %d %s", rec.Code, rec.Body.String())
	}
	ck := rec.Result().Cookies()[0].Value
	rec = do("/_opm/api/actor", `{"email":"`+email+`"}`, ck)
	var ar model.ActorResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &ar) != nil {
		t.Fatalf("actor %d %s", rec.Code, rec.Body.String())
	}
	return ck, "Actor " + ar.ActorToken
}

// The agent endpoints with a fake clock: speed cap, heartbeat decay, ACCEPT refused until effective ≥ wait, minimum
// real time = wait / 4, the fast-forward record, other sessions, cancel.
func TestIntentSpeedEndpoints(t *testing.T) {
	a := newTestAgent(t)
	now := iwT0
	a.clock = func() time.Time { return now }
	h := a.handlerFor("gui-alias")
	ck, auth := agentSession(t, a, h, "ops@acme.example")
	ck2, auth2 := agentSession(t, a, h, "other@acme.example")
	call := func(method, path, body, ck, auth string, out any) int {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "10.0.0.9:1234"
		r.Header.Set("Cookie", "opm_session_app="+ck)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if out != nil {
			_ = json.Unmarshal(rec.Body.Bytes(), out)
		}
		return rec.Code
	}
	rc := &reqCtx{sessionID: ck, actor: "ops@acme.example"}
	rev := "rev-under-test"
	in := a.createIntent("update", &rev, nil, rc)
	if in.WaitSec != 60 {
		t.Fatalf("waitSec %v", in.WaitSec)
	}
	base := "/_opm/api/intents/" + in.IntentID
	var p model.IntentProgress
	if c := call("GET", base, "", ck, "", &p); c != 200 || p.WaitSec != 60 || p.RemainingSec != 60 || p.Speed != 1 || p.Ready {
		t.Fatalf("get %d %+v", c, p)
	}
	// another session cannot see / drive it; the speed needs the actor
	if c := call("GET", base, "", ck2, "", nil); c != 404 {
		t.Fatalf("other session get → %d", c)
	}
	if c := call("POST", base+"/speed", `{"speed":4}`, ck2, auth2, nil); c != 404 {
		t.Fatalf("other session speed → %d", c)
	}
	if c := call("POST", base+"/speed", `{"speed":4}`, ck, "", nil); c != 401 {
		t.Fatalf("speed without actor → %d", c)
	}
	// at most ×4
	for _, bad := range []string{`{"speed":8}`, `{"speed":3}`, `{"speed":0}`} {
		if c := call("POST", base+"/speed", bad, ck, auth, nil); c != 422 {
			t.Fatalf("%s → %d", bad, c)
		}
	}
	// hold: ×2 at once, ×4 after 1.5 s, heartbeats every 500 ms
	if c := call("POST", base+"/speed", `{"speed":2}`, ck, auth, &p); c != 200 || p.Speed != 2 || p.SpeedUntil == nil {
		t.Fatalf("speed 2 %d %+v", c, p)
	}
	for i := 0; i < 3; i++ {
		now = now.Add(ms(500))
		call("POST", base+"/speed", `{"speed":2}`, ck, auth, &p)
	}
	if p.EffectiveSec != 3 {
		t.Fatalf("1.5 s at ×2 → %v", p.EffectiveSec)
	}
	call("POST", base+"/speed", `{"speed":4}`, ck, auth, &p)
	if e := a.checkIntent(in.IntentID, "update", rev, rc, "ACCEPT"); e == nil || !strings.Contains(e.msg, "wait") {
		t.Fatalf("ACCEPT before effective ≥ wait: %+v", e)
	}
	// keep holding at ×4 until ready: 57 effective s left = 14.25 s
	for !p.Ready {
		now = now.Add(ms(250))
		call("POST", base+"/speed", `{"speed":4}`, ck, auth, &p)
		if !p.Ready {
			if e := a.checkIntent(in.IntentID, "update", rev, rc, "ACCEPT"); e == nil {
				t.Fatalf("ACCEPT allowed at effective %v", p.EffectiveSec)
			}
		}
	}
	if real := now.Sub(iwT0); real < 15*time.Second || real > 16*time.Second {
		t.Fatalf("real wait %v (min = 60/4 = 15 s; 1.5 s ran at ×2)", real)
	}
	if e := a.checkIntent(in.IntentID, "update", rev, rc, "ACCEPT"); e != nil {
		t.Fatalf("ACCEPT after the wait: %+v", e)
	}
	ff := a.markIntentUsed(in.IntentID, rc.actor)
	if ff == nil || ff.MaxSpeed != 4 || ff.UsedBy != "ops@acme.example" || ff.EffectiveSeconds != 60 || ff.RealSeconds < 15 || ff.RealSeconds > 16 {
		t.Fatalf("ff %+v", ff)
	}
	if c := call("POST", base+"/speed", `{"speed":4}`, ck, auth, nil); c != 404 {
		t.Fatalf("speed on a used intent → %d", c)
	}

	// a lost release: one ×4 then silence → decays after 1.5 s; ACCEPT needs the rest at ×1
	now = iwT0.Add(time.Hour)
	in2 := a.createIntent("update", &rev, nil, rc)
	base2 := "/_opm/api/intents/" + in2.IntentID
	call("POST", base2+"/speed", `{"speed":4}`, ck, auth, &p)
	now = now.Add(10 * time.Second)
	if c := call("GET", base2, "", ck, "", &p); c != 200 || p.EffectiveSec != 14.5 || p.Speed != 1 || p.MaxSpeed != 4 {
		t.Fatalf("decay %d %+v", c, p)
	}
	now = now.Add(ms(45_400))
	if e := a.checkIntent(in2.IntentID, "update", rev, rc, "ACCEPT"); e == nil {
		t.Fatal("ACCEPT at effective 59.9")
	}
	now = now.Add(ms(100))
	if e := a.checkIntent(in2.IntentID, "update", rev, rc, "ACCEPT"); e != nil {
		t.Fatalf("ACCEPT at effective 60: %+v", e)
	}
	// the state survives a restart (memory dropped → intent_waits)
	in3 := a.createIntent("update", &rev, nil, rc)
	call("POST", "/_opm/api/intents/"+in3.IntentID+"/speed", `{"speed":4}`, ck, auth, &p)
	now = now.Add(ms(1000))
	a.iwMu.Lock()
	a.iw = map[string]*intentWait{}
	a.iwMu.Unlock()
	if c := call("GET", "/_opm/api/intents/"+in3.IntentID, "", ck, "", &p); c != 200 || p.EffectiveSec != 4 || p.MaxSpeed != 4 {
		t.Fatalf("after restart %d %+v", c, p)
	}
	// cancel removes it
	if c := call("DELETE", "/_opm/api/intents/"+in3.IntentID, "", ck, auth, nil); c != 200 {
		t.Fatalf("cancel %d", c)
	}
	if c := call("GET", "/_opm/api/intents/"+in3.IntentID, "", ck, "", nil); c != 404 {
		t.Fatalf("get after cancel %d", c)
	}
}

// Over the mesh: an intent held by another site (a forwarded volume reset) is driven through
// /_opm/api/sites/{site}/intents/{id}[/speed] like every other per-site call.
func TestIntentSpeedMeshForward(t *testing.T) {
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

	ck, auth := agentSession(t, app, app.handlerFor("agent"), "ops@acme.example")
	// what a forwarded resetVolumeStart creates on db: session "<sid>@app"
	vol := "data"
	in := db.createIntent("volume-reset", &vol, nil, &reqCtx{sessionID: ck + "@app", actor: "ops@acme.example"})
	call := func(method, path, body, auth string, out any) int {
		t.Helper()
		r, _ := http.NewRequest(method, appSrv.URL+path, strings.NewReader(body))
		r.Header.Set("Cookie", "opm_session_app="+ck)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
	base := "/_opm/api/sites/db/intents/" + in.IntentID
	var p model.IntentProgress
	if c := call("POST", base+"/speed", `{"speed":4}`, "", nil); c != 401 {
		t.Fatalf("forwarded speed without actor → %d", c)
	}
	if c := call("POST", base+"/speed", `{"speed":4}`, auth, &p); c != 200 || p.Speed != 4 || p.Kind != "volume-reset" || p.IntentID != in.IntentID {
		t.Fatalf("forwarded speed %d %+v", c, p)
	}
	if c := call("POST", base+"/speed", `{"speed":16}`, auth, nil); c != 422 {
		t.Fatalf("forwarded speed 16 → %d", c)
	}
	if c := call("GET", base, "", "", &p); c != 200 || p.MaxSpeed != 4 {
		t.Fatalf("forwarded get %d %+v", c, p)
	}
	// the intent is not on app
	if c := call("GET", "/_opm/api/intents/"+in.IntentID, "", "", nil); c != 404 {
		t.Fatalf("local get of a db intent → %d", c)
	}
	if c := call("DELETE", base, "", auth, nil); c != 200 {
		t.Fatalf("forwarded cancel → %d", c)
	}
	if c := call("GET", base, "", "", nil); c != 404 {
		t.Fatalf("after cancel → %d", c)
	}
}
