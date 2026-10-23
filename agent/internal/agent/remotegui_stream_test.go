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

// §21 #118 h: an event stream through the cloud (the agent log, follow). The agent answers the headers at once
// (X-OPM-Remote-Stream: 1, no body), sends what the handler writes as binary frames — a line logged after the start
// arrives while the stream is open — and ends the handler when the cloud says the browser left.
func TestRemoteGuiEventStream(t *testing.T) {
	a := newTestAgent(t)
	type result struct {
		status, headers, body string
	}
	var mu sync.Mutex
	queue := []model.RemoteGuiRequest{}
	results := make(chan result, 10)
	var out strings.Builder
	gone, closedByAgent := false, false
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
		case r.URL.Path == "/v1/agent/remote-gui/ws/sse1/recv":
			for i := 0; i < 10; i++ {
				mu.Lock()
				g := gone
				mu.Unlock()
				if g {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			mu.Lock()
			_ = json.NewEncoder(w).Encode(model.RemoteGuiWsRecvResponse{Frames: []model.RemoteGuiWsFrame{}, Closed: gone})
			mu.Unlock()
		case r.URL.Path == "/v1/agent/remote-gui/ws/sse1/send":
			var b model.RemoteGuiWsSendRequest
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			for _, f := range b.Frames {
				d, _ := base64.StdEncoding.DecodeString(f.DataB64)
				if f.Op == "close" {
					closedByAgent = true
				} else {
					out.Write(d)
				}
			}
			mu.Unlock()
			_, _ = w.Write([]byte(`{"accepted":true}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/result"):
			b, _ := io.ReadAll(r.Body)
			h, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-OPM-Remote-Headers"))
			results <- result{status: r.Header.Get("X-OPM-Remote-Status"), headers: string(h), body: string(b)}
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
	a.remoteGuiOnView(&model.CloudView{RemoteGui: []model.RemoteGuiCloudItem{{SiteSlug: "app", Until: tsOf(time.Now().Add(time.Minute))}}})
	req := model.RemoteGuiRequest{ReqID: "s1", SessionID: "cloud-s1", Method: "GET", Path: "/_opm/api/sites/app/tools/logs/agent?follow=1&tail=5", Headers: map[string]string{"accept": "text/event-stream"}, Actor: model.RemoteGuiActor{UID: "u1", Email: "ops@example.com"}}
	req.Stream = &struct {
		StreamID string `json:"streamId"`
	}{StreamID: "sse1"}
	mu.Lock()
	queue = append(queue, req)
	mu.Unlock()
	var res result
	select {
	case res = <-results:
	case <-time.After(10 * time.Second):
		t.Fatal("no result")
	}
	if res.status != "200" || res.body != "" || !strings.Contains(res.headers, `"X-OPM-Remote-Stream":"1"`) || !strings.Contains(res.headers, "text/event-stream") {
		t.Fatalf("stream start: %+v", res)
	}
	time.Sleep(300 * time.Millisecond)
	a.log.Info("test", "hello-through-the-cloud")
	waitFor(t, "a line logged after the start arrives as a chunk", 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(out.String(), "hello-through-the-cloud")
	})
	mu.Lock()
	gone = true
	mu.Unlock()
	waitFor(t, "the browser leaving ends the stream", 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return closedByAgent
	})
}
