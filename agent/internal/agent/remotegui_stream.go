package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/model"
)

// §21 #118 h: an event stream of the agent GUI (log follow, MQTT live messages) opened through the cloud. As soon as
// the handler writes, the agent posts the result (status + headers, X-OPM-Remote-Stream: 1, no body); every write is
// then sent as a binary frame (agentRemoteGuiWsSend, what is written within 100 ms goes together) and a close frame
// ends it. The browser leaving shows as closed on the agentRemoteGuiWsRecv long-poll: the handler's context is
// cancelled, so the handler ends as if the browser had left the site's own GUI.

const remoteStreamChunkMax = 512 << 10

type streamWriter struct {
	h       http.Header
	mu      sync.Mutex
	status  int
	started bool
	buf     bytes.Buffer
	kick    chan struct{}
	start   func(status int, h http.Header)
}

func (w *streamWriter) Header() http.Header { return w.h }
func (w *streamWriter) WriteHeader(s int) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started, w.status = true, s
	h := w.h.Clone()
	w.mu.Unlock()
	w.start(s, h)
}
func (w *streamWriter) Write(b []byte) (int, error) {
	w.WriteHeader(200)
	w.mu.Lock()
	w.buf.Write(b)
	w.mu.Unlock()
	w.Flush()
	return len(b), nil
}
func (w *streamWriter) Flush() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}
func (w *streamWriter) take() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() == 0 {
		return nil
	}
	n := min(w.buf.Len(), remoteStreamChunkMax)
	out := append([]byte(nil), w.buf.Next(n)...)
	return out
}

func (a *Agent) serveRemoteStream(ctx context.Context, cancel context.CancelFunc, it model.RemoteGuiRequest, r *http.Request) {
	base := "/v1/agent/remote-gui/ws/" + url.PathEscape(it.Stream.StreamID)
	sent := make(chan struct{}) // closed once the result is posted (frames only after it)
	var once sync.Once
	w := &streamWriter{h: http.Header{}, kick: make(chan struct{}, 1)}
	w.start = func(status int, h http.Header) {
		once.Do(func() {
			hdr := map[string]string{"X-OPM-Remote-Stream": "1"}
			keys := make([]string, 0, len(h))
			for k := range h {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !strings.EqualFold(k, "Set-Cookie") && !strings.EqualFold(k, "Content-Length") {
					hdr[k] = strings.Join(h[k], ", ")
				}
			}
			a.postRemoteResult(ctx, it, status, hdr, nil)
			close(sent)
		})
	}
	send := func(frames []model.RemoteGuiWsFrame) bool {
		var out struct {
			Accepted bool `json:"accepted"`
		}
		err := a.cloudJSON(a.ctxOr(ctx), "POST", base+"/send", model.RemoteGuiWsSendRequest{Frames: frames}, &out, callOpts{timeout: 30 * time.Second})
		return err == nil && out.Accepted
	}
	// the browser left (or the cloud forgot the stream) → end the handler
	go func() {
		for ctx.Err() == nil {
			var rr model.RemoteGuiWsRecvResponse
			if err := a.cloudJSON(ctx, "GET", base+"/recv?wait=25", nil, &rr, callOpts{timeout: 40 * time.Second}); err != nil {
				if ctx.Err() == nil {
					time.Sleep(time.Second)
				}
				continue
			}
			if rr.Closed {
				cancel()
				return
			}
		}
	}()
	// writes → frames
	done := make(chan struct{})
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		flush := func() bool {
			for {
				b := w.take()
				if b == nil {
					return true
				}
				if !send([]model.RemoteGuiWsFrame{{Op: "binary", DataB64: base64.StdEncoding.EncodeToString(b)}}) {
					return false
				}
			}
		}
		for {
			select {
			case <-done:
				<-sent
				if flush() {
					send([]model.RemoteGuiWsFrame{{Op: "close", Code: 1000}})
				}
				return
			case <-w.kick:
			case <-tick.C:
			}
			select {
			case <-sent:
			default:
				continue // nothing written yet
			}
			// let more writes of the same moment join this frame
			time.Sleep(30 * time.Millisecond)
			if !flush() {
				cancel()
			}
		}
	}()
	a.handlerFor("agent").ServeHTTP(w, r)
	w.WriteHeader(200) // a handler that wrote nothing still answers
	close(done)
	<-pumped
	cancel()
}
