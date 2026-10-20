package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/model"
)

// §21 #118: while the cloud view lists this site the agent long-polls for remote GUI requests, runs each one against
// its own GUI as the cloud person (one agent session + actor token per cloud session: GET /_opm/api/session answers
// remote, an actor-only POST is allowed), posts the result with status + headers, never passes a Set-Cookie on, refuses
// switching the remote GUI off through the cloud, and stops polling when it is off.
func TestRemoteGuiServes(t *testing.T) {
	a := newVSAgent(t)
	type result struct {
		status  string
		headers map[string]string
		body    string
	}
	var mu sync.Mutex
	queue := []model.RemoteGuiRequest{}
	polls := 0
	results := make(chan result, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/agent/remote-gui/next":
			mu.Lock()
			polls++
			items := queue
			queue = nil
			mu.Unlock()
			if len(items) == 0 {
				time.Sleep(100 * time.Millisecond)
			}
			_ = json.NewEncoder(w).Encode(model.RemoteGuiNextResponse{Items: append([]model.RemoteGuiRequest{}, items...)})
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/agent/remote-gui/"):
			b, _ := io.ReadAll(r.Body)
			var h map[string]string
			hb, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-OPM-Remote-Headers"))
			_ = json.Unmarshal(hb, &h)
			results <- result{status: r.Header.Get("X-OPM-Remote-Status"), headers: h, body: string(b)}
			_, _ = w.Write([]byte(`{"accepted":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	a.cfg.APIBase = srv.URL
	a.enrolled = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.ctx = ctx
	push := func(reqs ...model.RemoteGuiRequest) {
		mu.Lock()
		queue = append(queue, reqs...)
		mu.Unlock()
	}
	actor := model.RemoteGuiActor{UID: "u1", Email: "Ops@Example.com"}
	push(model.RemoteGuiRequest{ReqID: "r1", SessionID: "cloud-s1", Method: "GET", Path: "/_opm/api/session", Headers: map[string]string{"accept": "application/json"}, Actor: actor})
	a.remoteGuiOnView(&model.CloudView{RemoteGui: []model.RemoteGuiCloudItem{{SiteSlug: "other", Until: tsOf(time.Now().Add(time.Hour))}}})
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	if polls != 0 {
		t.Fatal("another site's session must not start polling")
	}
	mu.Unlock()
	a.remoteGuiOnView(&model.CloudView{RemoteGui: []model.RemoteGuiCloudItem{{SiteSlug: "app", Until: tsOf(time.Now().Add(time.Minute))}}})
	get := func() result {
		t.Helper()
		select {
		case r := <-results:
			return r
		case <-time.After(10 * time.Second):
			t.Fatal("no result")
		}
		return result{}
	}
	r1 := get()
	var sv model.SessionView
	if err := json.Unmarshal([]byte(r1.body), &sv); err != nil || r1.status != "200" || sv.Remote == nil || sv.Remote.Email != "ops@example.com" || sv.Remote.ActorToken == "" {
		t.Fatalf("session through the cloud: %s %s", r1.status, r1.body)
	}
	if _, ok := r1.headers["Set-Cookie"]; ok {
		t.Fatal("Set-Cookie must never be passed on")
	}
	// the same cloud session reuses the agent session; an actor-only POST works; switching off through the cloud does not
	push(model.RemoteGuiRequest{ReqID: "r2", SessionID: "cloud-s1", Method: "GET", Path: "/_opm/api/session", Headers: map[string]string{}, Actor: actor},
		model.RemoteGuiRequest{ReqID: "r3", SessionID: "cloud-s1", Method: "PUT", Path: "/_opm/api/agent/remote-gui", Headers: map[string]string{"content-type": "application/json"}, BodyB64: ptr(base64.StdEncoding.EncodeToString([]byte(`{"enabled":false}`))), Actor: actor},
		model.RemoteGuiRequest{ReqID: "r4", SessionID: "cloud-s1", Method: "GET", Path: "/elsewhere", Headers: map[string]string{}, Actor: actor})
	got := map[string]result{}
	for i := 0; i < 3; i++ {
		r := get()
		switch {
		case strings.Contains(r.body, "sessionId"):
			got["session"] = r
		case strings.Contains(r.body, "forbidden"):
			got["put"] = r
		default:
			got["other"] = r
		}
	}
	var sv2 model.SessionView
	_ = json.Unmarshal([]byte(got["session"].body), &sv2)
	if sv2.SessionID != sv.SessionID {
		t.Fatalf("a new agent session per request: %s vs %s", sv2.SessionID, sv.SessionID)
	}
	if got["put"].status != "403" || !a.remoteGuiEnabled() {
		t.Fatalf("switching off through the cloud: %+v", got["put"])
	}
	if got["other"].status != "404" {
		t.Fatalf("a path outside /_opm: %+v", got["other"])
	}
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE ip = 'cloud'`).Scan(&n)
	if n != 1 {
		t.Fatalf("agent sessions for the cloud person: %d", n)
	}
	if len(activityData(t, a, "remote-gui.session")) != 1 {
		t.Fatal("remote-gui.session activity")
	}
	// off on the site → no more polling
	_ = a.db.SetKV("remote-gui", model.RemoteGuiSetting{Enabled: false})
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	before := polls
	mu.Unlock()
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	after := polls
	mu.Unlock()
	if after > before+1 {
		t.Fatalf("still polling while off: %d → %d", before, after)
	}
}
