package apitest

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"opm/agent/internal/cloud/core"
)

// wfRequest is one plain request, safe off the test goroutine (redirects not followed, like raw).
func wfRequest(method, path string, o Opt) (*Resp, error) {
	var rd io.Reader
	if o.Body != nil {
		rd = bytes.NewReader(o.Body)
	}
	req, err := http.NewRequest(method, Base+path, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range o.Headers {
		req.Header.Set(k, v)
	}
	rs, err := noRedirect.Do(req)
	if err != nil {
		return nil, err
	}
	return readResp(rs), nil
}

type wfResult struct {
	r   *Resp
	err error
}

// wfGo starts a request that only ends once the test acted (const p = raw(...)); wfAwait is `await p`.
func wfGo(method, path string, o Opt) <-chan wfResult {
	ch := make(chan wfResult, 1)
	go func() {
		r, err := wfRequest(method, path, o)
		ch <- wfResult{r, err}
	}()
	return ch
}

func wfAwait(t testing.TB, ch <-chan wfResult) *Resp {
	t.Helper()
	select {
	case x := <-ch:
		NoErr(t, x.err)
		return x.r
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for a response")
	}
	return nil
}

// wfSigned is the options of a request signed by a site.
func wfSigned(key SiteKey, dep, site, method, path string, body []byte, headers map[string]string) Opt {
	h := SignHeaders(key, dep, site, method, path, body)
	for k, v := range headers {
		h[k] = v
	}
	return Opt{Headers: h, Body: body}
}

// wfAgentGet is a signed GET that must answer 200.
func wfAgentGet(t testing.TB, key SiteKey, dep, site, path string) J {
	t.Helper()
	r := Raw(t, "GET", path, wfSigned(key, dep, site, "GET", path, nil, nil))
	Eq(t, r.Status, 200, r.Text())
	return r.J
}

// wfAgentPost is a signed JSON POST that must answer 200.
func wfAgentPost(t testing.TB, key SiteKey, dep, site, path string, body any) J {
	t.Helper()
	b := []byte(show(Plain(body)))
	r := Raw(t, "POST", path, wfSigned(key, dep, site, "POST", path, b, map[string]string{"Content-Type": "application/json"}))
	Eq(t, r.Status, 200, r.Text())
	return r.J
}

func wfB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func wfUnB64(t testing.TB, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	NoErr(t, err)
	return string(b)
}

func wfNoHeader(t testing.TB, r *Resp, name string, msg ...any) {
	t.Helper()
	Eq(t, len(r.Headers.Values(name)), 0, msg...)
}

func wfNext(t testing.TB, key SiteKey, dep, site string, wait int) J {
	t.Helper()
	return wfAgentGet(t, key, dep, site, "/v1/agent/remote-gui/next?wait="+Itoa(wait))
}

func wfAnswer(t testing.TB, key SiteKey, dep, site, reqID string, status int, headers map[string]string, body string) J {
	t.Helper()
	path := "/v1/agent/remote-gui/" + reqID + "/result"
	r := Raw(t, "POST", path, wfSigned(key, dep, site, "POST", path, []byte(body), map[string]string{
		"Content-Type":         "application/octet-stream",
		"X-OPM-Remote-Status":  Itoa(status),
		"X-OPM-Remote-Headers": wfB64(show(Plain(headers))),
	}))
	return r.J
}

// wfReadChunk is `await reader.read()` of a streamed body.
func wfReadChunk(t testing.TB, body io.Reader) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		b := make([]byte, 64*1024)
		n, _ := body.Read(b)
		ch <- string(b[:n])
	}()
	select {
	case s := <-ch:
		return s
	case <-time.After(15 * time.Second):
		t.Fatal("timed out reading the stream")
	}
	return ""
}

// Port of backend/test/42-remote-gui.test.ts.
func TestRemoteGui(t *testing.T) {
	t.Run("§21 #118 remote agent GUI: session cookie, long-poll hand-off, prefix rewriting, WebSocket relay, event streams", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		app := EnrollSite(t, admin, id, "app")
		db := EnrollSite(t, admin, id, "db")
		prefix := core.RemoteGuiPrefix(id, "app")
		bearer := Opt{Headers: map[string]string{"Authorization": admin.Bearer}}

		// no remote session → 401 page
		anon := Raw(t, "GET", prefix+"/_opm/app/app/status")
		Eq(t, anon.Status, 401)
		Match(t, anon.Text(), `Open agent GUI`)

		// open: cookie with the remote prefix as Path; the cloud view lists the site
		o := Raw(t, "POST", "/v1/deployments/"+id+"/remote-gui/app/session", bearer)
		Eq(t, o.Status, 200, o.Text())
		Eq(t, o.J.At("url"), prefix+"/_opm/app/app/status")
		setCookie := o.Header("set-cookie")
		Match(t, setCookie, `^opm_remote_app=[^;]+; Path=`+regexp.QuoteMeta(prefix)+`;`)
		Match(t, setCookie, `HttpOnly`)
		cookie := strings.Split(setCookie, ";")[0]
		view := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
		Eq(t, view.At("remoteGui").Pluck("siteSlug"), L{"app"})
		Eq(t, Raw(t, "POST", "/v1/deployments/"+id+"/remote-gui/nope/session", bearer).Status, 404)

		// the bare prefix → the site's status page
		bare := Raw(t, "GET", prefix, Opt{Headers: map[string]string{"Cookie": cookie}})
		Eq(t, bare.Status, 302)
		Eq(t, bare.Header("location"), prefix+"/_opm/app/app/status")

		// a page: waits for the site's long-poll, comes back rewritten
		pageP := wfGo("GET", prefix+"/_opm/app/app/status?x=1", Opt{Headers: map[string]string{"Cookie": cookie, "Accept": "text/html"}})
		Eq(t, wfNext(t, db.Key, id, "db", 0).At("items"), L{}, "another site never sees the request")
		got := wfNext(t, app.Key, id, "app", 10)
		Eq(t, got.At("items").Len(), 1)
		it := got.At("items.0")
		Eq(t, it.At("method"), "GET")
		Eq(t, it.At("path"), "/_opm/app/app/status?x=1")
		Eq(t, it.At("actor.email"), admin.Email)
		Eq(t, it.At("headers.accept"), "text/html")
		Eq(t, it.At("headers.cookie"), Undefined, "the cloud cookie never reaches the site")
		html := `<html><head><link href="/_opm/assets/app.css"><script>const base = "/_opm"; fetch('/_opm/api/session'); const re = /\/_opm\//;</script></head><body><a href="/_opm/app">x</a></body></html>`
		Eq(t, wfAnswer(t, app.Key, id, "app", it.At("reqId").Str(), 200, map[string]string{"Content-Type": "text/html; charset=utf-8", "Set-Cookie": "opm_session_app=s; Path=/_opm", "ETag": `"1"`}, html), A{"accepted": true})
		pg := wfAwait(t, pageP)
		Eq(t, pg.Status, 200)
		text := pg.Text()
		Contains(t, text, `href="`+prefix+`/_opm/assets/app.css"`)
		Contains(t, text, `const base = "`+prefix+`/_opm"`)
		Contains(t, text, `fetch('`+prefix+`/_opm/api/session')`)
		Contains(t, text, `href="`+prefix+`/_opm/app"`)
		Contains(t, text, `/\/_opm\//`, "escaped forms stay")
		wfNoHeader(t, pg, "set-cookie", "the agent's cookies never reach the browser")
		wfNoHeader(t, pg, "etag")
		// a second answer to the same request is not accepted
		Eq(t, wfAnswer(t, app.Key, id, "app", it.At("reqId").Str(), 200, map[string]string{}, "again"), A{"accepted": false})

		// a POST with a body + a redirect answer (Location re-prefixed); JSON is passed through unchanged
		postP := wfGo("POST", prefix+"/_opm/api/actor", Opt{Headers: map[string]string{"Cookie": cookie, "Content-Type": "application/json"}, Body: []byte(`{"email":"x@y.z"}`)})
		p2 := wfNext(t, app.Key, id, "app", 10).At("items.0")
		Eq(t, p2.At("method"), "POST")
		Eq(t, wfUnB64(t, p2.At("bodyB64").Str()), `{"email":"x@y.z"}`)
		wfAnswer(t, app.Key, id, "app", p2.At("reqId").Str(), 302, map[string]string{"Location": "/_opm/app/app/branch", "Content-Type": "application/json"}, `{"href":"/_opm/x"}`)
		post := wfAwait(t, postP)
		Eq(t, post.Status, 302)
		Eq(t, post.Header("location"), prefix+"/_opm/app/app/branch")
		Eq(t, post.Text(), `{"href":"/_opm/x"}`)

		// a WebSocket (§21 #118 g): the browser upgrades at the cloud; the site answers 101 and relays the frames over its
		// HTTP long-polls (recv / send) — the site never opens a socket to the cloud
		type dialed struct {
			c   *websocket.Conn
			err error
		}
		openedC := make(chan dialed, 1)
		go func() {
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(Base, "http")+prefix+"/_opm/api/sites/app/tools/terminal", http.Header{"Cookie": {cookie}})
			openedC <- dialed{c, err}
		}()
		wsItem := wfNext(t, app.Key, id, "app", 10).At("items.0")
		True(t, wsItem.At("ws.streamId").Truthy(), "the request names its stream")
		Eq(t, wsItem.At("path"), "/_opm/api/sites/app/tools/terminal")
		Eq(t, wfAnswer(t, app.Key, id, "app", wsItem.At("reqId").Str(), 101, map[string]string{}, ""), A{"accepted": true})
		var opened dialed
		select {
		case opened = <-openedC:
		case <-time.After(15 * time.Second):
			t.Fatal("the WebSocket did not open")
		}
		NoErr(t, opened.err)
		ws := opened.c
		t.Cleanup(func() { ws.Close() })
		NoErr(t, ws.SetReadDeadline(time.Now().Add(15*time.Second)))
		sp := "/v1/agent/remote-gui/ws/" + wsItem.At("ws.streamId").Str()
		Eq(t, wfAgentPost(t, app.Key, id, "app", sp+"/send", A{"frames": L{A{"op": "text", "dataB64": wfB64("$ hello")}}}), A{"accepted": true})
		_, msg, err := ws.ReadMessage()
		NoErr(t, err)
		Eq(t, string(msg), "$ hello")
		NoErr(t, ws.WriteMessage(websocket.TextMessage, []byte("ls\n")))
		rv := wfAgentGet(t, app.Key, id, "app", sp+"/recv?wait=5")
		frames := L{}
		for _, f := range rv.At("frames").Arr() {
			frames = append(frames, L{f.At("op"), wfUnB64(t, f.At("dataB64").Str())})
		}
		Eq(t, frames, L{L{"text", "ls\n"}})
		// ws.close(): a close frame without a code; closed = the server's answer ended the connection
		NoErr(t, ws.WriteMessage(websocket.CloseMessage, nil))
		_, _, err = ws.ReadMessage()
		True(t, websocket.IsCloseError(err, websocket.CloseNoStatusReceived, websocket.CloseNormalClosure), "the browser's close is answered: %v", err)
		rc := wfAgentGet(t, app.Key, id, "app", sp+"/recv?wait=5")
		Eq(t, rc.At("closed"), true, "the browser closing ends the stream for the site")
		// §21 #118 h an event stream is carried chunk by chunk: headers at once, then the frames the site sends; the browser
		// leaving shows as closed for the site
		ctx, abort := context.WithCancel(context.Background())
		t.Cleanup(abort)
		type streamed struct {
			r   *http.Response
			err error
		}
		liveC := make(chan streamed, 1)
		go func() {
			req, err := http.NewRequestWithContext(ctx, "GET", Base+prefix+"/_opm/api/sites/app/tools/logs/containers?follow=true", nil)
			if err != nil {
				liveC <- streamed{nil, err}
				return
			}
			req.Header.Set("Cookie", cookie)
			req.Header.Set("Accept", "text/event-stream")
			r, err := http.DefaultClient.Do(req)
			liveC <- streamed{r, err}
		}()
		lv := wfNext(t, app.Key, id, "app", 10).At("items.0")
		True(t, lv.At("stream.streamId").Truthy(), "the site is asked to stream")
		wfAnswer(t, app.Key, id, "app", lv.At("reqId").Str(), 200, map[string]string{"Content-Type": "text/event-stream", "X-OPM-Remote-Stream": "1"}, "")
		var liveR streamed
		select {
		case liveR = <-liveC:
		case <-time.After(15 * time.Second):
			t.Fatal("the event stream did not start")
		}
		NoErr(t, liveR.err)
		live := liveR.r
		t.Cleanup(func() { live.Body.Close() })
		Eq(t, live.StatusCode, 200)
		Match(t, live.Header.Get("content-type"), `text/event-stream`)
		Eq(t, len(live.Header.Values("x-opm-remote-stream")), 0, "the flag stays between cloud and site")
		lsp := "/v1/agent/remote-gui/ws/" + lv.At("stream.streamId").Str()
		wfAgentPost(t, app.Key, id, "app", lsp+"/send", A{"frames": L{A{"op": "binary", "dataB64": wfB64("event: line\ndata: {\"t\":\"first\"}\n\n")}}})
		Match(t, wfReadChunk(t, live.Body), `first`)
		wfAgentPost(t, app.Key, id, "app", lsp+"/send", A{"frames": L{A{"op": "binary", "dataB64": wfB64("event: line\ndata: {\"t\":\"second\"}\n\n")}}})
		Match(t, wfReadChunk(t, live.Body), `second`)
		abort()
		live.Body.Close()
		closed := false
		for i := 0; i < 20 && !closed; i++ {
			closed = wfAgentGet(t, app.Key, id, "app", lsp+"/recv?wait=1").At("closed").Bool()
		}
		Eq(t, closed, true, "the browser leaving ends the stream for the site")
		// a site that does not stream answers the whole body at once: still carried
		logsP := wfGo("GET", prefix+"/_opm/api/sites/app/tools/logs/containers?tail=10&follow=false", Opt{Headers: map[string]string{"Cookie": cookie, "Accept": "text/event-stream"}})
		lg := wfNext(t, app.Key, id, "app", 10).At("items.0")
		Eq(t, lg.At("path"), "/_opm/api/sites/app/tools/logs/containers?tail=10&follow=false")
		sseBody := "event: line\ndata: {\"t\":\"x\"}\n\nevent: end\ndata: {\"reason\":\"done\"}\n\n"
		wfAnswer(t, app.Key, id, "app", lg.At("reqId").Str(), 200, map[string]string{"Content-Type": "text/event-stream"}, sseBody)
		logs := wfAwait(t, logsP)
		Eq(t, logs.Status, 200)
		Eq(t, logs.Text(), sseBody)
		// the cookie of site app does not open site db
		other := Raw(t, "GET", core.RemoteGuiPrefix(id, "db")+"/_opm/app/db/status", Opt{Headers: map[string]string{"Cookie": strings.Replace(cookie, "opm_remote_app", "opm_remote_db", 1)}})
		Eq(t, other.Status, 401)
	})
}
