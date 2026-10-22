package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/tools"
)

// §21 #118 g: a WebSocket of the agent GUI (terminal, MQTT client) opened through the cloud. The agent dials its own
// GUI port as the cloud person (the remote session's cookie + actor token, same-origin), answers the upgrade with the
// result (101 or the refusal), then relays: local → cloud with agentRemoteGuiWsSend (messages that arrive within
// 15 ms are sent together), cloud → local with the agentRemoteGuiWsRecv long-poll, until either side closes.
func (a *Agent) serveRemoteWS(ctx context.Context, it model.RemoteGuiRequest, sess *remoteSess) {
	sid := it.WS.StreamID
	port := a.currentAgentPort()
	hdr := http.Header{}
	hdr.Set("Cookie", a.cookieName()+"="+sess.sid)
	hdr.Set("Authorization", "Actor "+sess.token)
	hdr.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", port))
	for k, v := range it.Headers {
		if strings.EqualFold(k, "sec-websocket-protocol") || strings.EqualFold(k, "user-agent") {
			hdr.Set(k, v)
		}
	}
	conn, resp, err := tools.DialWS(fmt.Sprintf("ws://127.0.0.1:%d%s", port, it.Path), hdr)
	if err != nil {
		st := 502
		if resp != nil {
			st = resp.StatusCode
		}
		a.postRemoteResult(ctx, it, st, map[string]string{"Content-Type": "text/plain; charset=utf-8"}, []byte(err.Error()))
		return
	}
	defer conn.Close()
	rh := map[string]string{}
	if p := resp.Header.Get("Sec-WebSocket-Protocol"); p != "" {
		rh["Sec-WebSocket-Protocol"] = p
	}
	a.postRemoteResult(ctx, it, 101, rh, nil)
	base := "/v1/agent/remote-gui/ws/" + url.PathEscape(sid)
	done := make(chan struct{})
	send := func(frames []model.RemoteGuiWsFrame) bool {
		var out struct {
			Accepted bool `json:"accepted"`
		}
		err := a.cloudJSON(ctx, "POST", base+"/send", model.RemoteGuiWsSendRequest{Frames: frames}, &out, callOpts{timeout: 30 * time.Second})
		return err == nil && out.Accepted
	}
	// local → cloud
	msgs := make(chan model.RemoteGuiWsFrame, 256)
	go func() {
		defer close(msgs)
		for {
			op, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			o := "binary"
			if op == tools.OpText {
				o = "text"
			}
			msgs <- model.RemoteGuiWsFrame{Op: o, DataB64: base64.StdEncoding.EncodeToString(data)}
		}
	}()
	go func() {
		defer close(done)
		for {
			f, ok := <-msgs
			if !ok {
				send([]model.RemoteGuiWsFrame{{Op: "close", Code: 1000}})
				return
			}
			batch := []model.RemoteGuiWsFrame{f}
			timer := time.NewTimer(15 * time.Millisecond)
		collect:
			for len(batch) < 64 {
				select {
				case g, ok := <-msgs:
					if !ok {
						break collect
					}
					batch = append(batch, g)
				case <-timer.C:
					break collect
				}
			}
			timer.Stop()
			if !send(batch) {
				return
			}
		}
	}()
	// cloud → local
	for ctx.Err() == nil {
		select {
		case <-done:
			return
		default:
		}
		var r model.RemoteGuiWsRecvResponse
		if err := a.cloudJSON(ctx, "GET", base+"/recv?wait=25", nil, &r, callOpts{timeout: 40 * time.Second}); err != nil {
			time.Sleep(time.Second)
			continue
		}
		for _, f := range r.Frames {
			data, _ := base64.StdEncoding.DecodeString(f.DataB64)
			switch f.Op {
			case "text":
				_ = conn.WriteMessage(tools.OpText, data)
			case "binary":
				_ = conn.WriteMessage(tools.OpBinary, data)
			case "close":
				conn.CloseWith(1000, "")
				return
			}
		}
		if r.Closed {
			conn.CloseWith(1000, "")
			return
		}
	}
}

// postRemoteResult posts one result (status + headers + body) of a forwarded request.
func (a *Agent) postRemoteResult(ctx context.Context, it model.RemoteGuiRequest, status int, hdr map[string]string, body []byte) {
	hb, _ := json.Marshal(hdr)
	c, cancel := context.WithTimeout(a.ctxOr(ctx), 2*time.Minute)
	defer cancel()
	resp, err := a.cloudCall(c, "POST", "/v1/agent/remote-gui/"+url.PathEscape(it.ReqID)+"/result", body, callOpts{timeout: 2 * time.Minute, extraHdr: map[string]string{
		"Content-Type": "application/octet-stream", "X-OPM-Remote-Status": fmt.Sprint(status), "X-OPM-Remote-Headers": base64.StdEncoding.EncodeToString(hb)}})
	if err != nil {
		a.log.Warn("remote-gui", "result not delivered", "path", pathOnly(it.Path), "error", err.Error())
	} else if resp.Status >= 300 {
		a.log.Warn("remote-gui", "result refused", "path", pathOnly(it.Path), "status", resp.Status)
	}
}
