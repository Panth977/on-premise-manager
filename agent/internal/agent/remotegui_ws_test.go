package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/model"
)

// §21 #118 g: a terminal through the cloud. The cloud hands out the create (actor-only POST) and then the WebSocket
// request with a stream; the agent dials its own GUI port as the cloud person, answers 101 and relays the frames over
// plain HTTP — keystrokes from the recv long-poll, output posted to send — and nothing but HTTP goes to the cloud.
func TestRemoteGuiWebSocketRelay(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no pty")
	}
	a := newTestAgent(t)
	gui := httptest.NewServer(a.handlerFor("agent"))
	defer gui.Close()
	var port int
	_, ps, _ := strings.Cut(strings.TrimPrefix(gui.URL, "http://"), ":")
	for _, c := range ps {
		port = port*10 + int(c-'0')
	}
	a.mu.Lock()
	a.listeners["agent"] = &listener{purpose: "agent", port: port}
	a.mu.Unlock()

	type result struct {
		status string
		body   string
	}
	var mu sync.Mutex
	queue := []model.RemoteGuiRequest{}
	results := make(chan result, 10)
	keys := make(chan model.RemoteGuiWsFrame, 10)
	var out strings.Builder
	closedByAgent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/agent/remote-gui/next":
			mu.Lock()
			items := queue
			queue = nil
			mu.Unlock()
			if len(items) == 0 {
				time.Sleep(100 * time.Millisecond)
			}
			_ = json.NewEncoder(w).Encode(model.RemoteGuiNextResponse{Items: append([]model.RemoteGuiRequest{}, items...)})
		case r.URL.Path == "/v1/agent/remote-gui/ws/st1/recv":
			var fr []model.RemoteGuiWsFrame
			select {
			case f := <-keys:
				fr = append(fr, f)
			case <-time.After(300 * time.Millisecond):
			}
			_ = json.NewEncoder(w).Encode(model.RemoteGuiWsRecvResponse{Frames: append([]model.RemoteGuiWsFrame{}, fr...)})
		case r.URL.Path == "/v1/agent/remote-gui/ws/st1/send":
			var b model.RemoteGuiWsSendRequest
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			for _, f := range b.Frames {
				d, _ := base64.StdEncoding.DecodeString(f.DataB64)
				if f.Op == "close" {
					closedByAgent = true
				} else if f.Op == "binary" {
					out.Write(d)
				}
			}
			mu.Unlock()
			_, _ = w.Write([]byte(`{"accepted":true}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/result"):
			b, _ := io.ReadAll(r.Body)
			results <- result{status: r.Header.Get("X-OPM-Remote-Status"), body: string(b)}
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
	push := func(r model.RemoteGuiRequest) {
		mu.Lock()
		queue = append(queue, r)
		mu.Unlock()
	}
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
	actor := model.RemoteGuiActor{UID: "u1", Email: "ops@example.com"}
	a.remoteGuiOnView(&model.CloudView{RemoteGui: []model.RemoteGuiCloudItem{{SiteSlug: "app", Until: tsOf(time.Now().Add(time.Minute))}}})
	push(model.RemoteGuiRequest{ReqID: "c1", SessionID: "cloud-s1", Method: "POST", Path: "/_opm/api/sites/app/tools/terminal/sessions", Headers: map[string]string{"content-type": "application/json"},
		BodyB64: ptr(base64.StdEncoding.EncodeToString([]byte(`{"kind":"host","confirm":"app"}`))), Actor: actor})
	cr := get()
	var created struct {
		WsPath string `json:"wsPath"`
	}
	if err := json.Unmarshal([]byte(cr.body), &created); err != nil || cr.status != "201" || created.WsPath == "" {
		t.Fatalf("create through the cloud: %s %s", cr.status, cr.body)
	}
	ws := model.RemoteGuiRequest{ReqID: "w1", SessionID: "cloud-s1", Method: "GET", Path: created.WsPath, Headers: map[string]string{}, Actor: actor}
	ws.WS = &struct {
		StreamID string `json:"streamId"`
	}{StreamID: "st1"}
	push(ws)
	if r := get(); r.status != "101" {
		t.Fatalf("upgrade through the cloud: %s %s", r.status, r.body)
	}
	keys <- model.RemoteGuiWsFrame{Op: "binary", DataB64: base64.StdEncoding.EncodeToString([]byte("echo relayed-$((6*7)); exit 0\n"))}
	waitFor(t, "output over send", 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(out.String(), "relayed-42")
	})
	waitFor(t, "the shell's end closes the stream", 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return closedByAgent
	})
}
