package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

// §21 #118 remote agent GUI: the site's GUI opened from the cloud console. The cloud never calls the site: while the
// cloud view lists this site (`remoteGui`, a person has a remote session open) and the setting is on (kv remote-gui,
// default on), the agent long-polls GET /v1/agent/remote-gui/next, runs every browser request against its own GUI
// handler as that cloud person — one agent session + actor token per cloud session (no access key, no email prompt;
// activity remote-gui.session) — and posts the response to /v1/agent/remote-gui/{reqId}/result.

const (
	remoteGuiPollWait    = 25 // s
	remoteGuiResponseMax = 64 << 20
)

type remoteKey struct{}

// remoteInfo marks a request that came through the cloud (handleSession hands the actor token to the GUI).
type remoteInfo struct {
	email, token string
	name         *string
	exp          int64
}

func remoteOf(r *http.Request) *remoteInfo {
	v, _ := r.Context().Value(remoteKey{}).(*remoteInfo)
	return v
}

type remoteSess struct {
	sid, token, email string
	name              *string
	exp               int64
	since             time.Time
}

type remoteState struct {
	mu       sync.Mutex
	until    time.Time
	running  bool
	sessions map[string]*remoteSess // cloud session id → agent session
}

var remoteInit sync.Mutex

func (a *Agent) rgui() *remoteState {
	remoteInit.Lock()
	defer remoteInit.Unlock()
	if a.remote == nil {
		a.remote = &remoteState{sessions: map[string]*remoteSess{}}
	}
	return a.remote
}

// remoteGuiEnabled: kv remote-gui (absent = on).
func (a *Agent) remoteGuiEnabled() bool {
	var s model.RemoteGuiSetting
	if ok, _ := a.db.GetKV("remote-gui", &s); ok {
		return s.Enabled
	}
	return true
}

// remoteGuiOnView starts / extends the long-poll loop while the cloud lists this site.
func (a *Agent) remoteGuiOnView(v *model.CloudView) {
	if v == nil || !a.remoteGuiEnabled() {
		return
	}
	var until time.Time
	for _, it := range v.RemoteGui {
		if it.SiteSlug == a.site {
			if t := parseTs(it.Until); t.After(until) {
				until = t
			}
		}
	}
	if until.IsZero() || !until.After(time.Now()) {
		return
	}
	st := a.rgui()
	st.mu.Lock()
	if until.After(st.until) {
		st.until = until
	}
	start := !st.running
	st.running = true
	st.mu.Unlock()
	if start {
		a.goLoop("remote-gui", a.remoteGuiLoop)
	}
}

func (a *Agent) remoteGuiLoop(ctx context.Context) {
	st := a.rgui()
	defer func() {
		st.mu.Lock()
		st.running = false
		st.mu.Unlock()
	}()
	backoff := time.Second
	for ctx.Err() == nil {
		st.mu.Lock()
		until := st.until
		st.mu.Unlock()
		if !time.Now().Before(until) || !a.remoteGuiEnabled() {
			return
		}
		var resp model.RemoteGuiNextResponse
		err := a.cloudJSON(ctx, "GET", fmt.Sprintf("/v1/agent/remote-gui/next?wait=%d", remoteGuiPollWait), nil, &resp, callOpts{timeout: (remoteGuiPollWait + 15) * time.Second})
		if err != nil {
			a.log.Debug("remote-gui", "poll failed", "error", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, it := range resp.Items {
			it := it
			a.goLoop("remote-gui-req", func(c context.Context) { a.serveRemoteGui(c, it) })
		}
	}
}

// remoteSession: the agent session of a cloud session (made on first use; renewed before it expires).
func (a *Agent) remoteSession(cloudSID string, actor model.RemoteGuiActor) (*remoteSess, error) {
	st := a.rgui()
	st.mu.Lock()
	defer st.mu.Unlock()
	if s := st.sessions[cloudSID]; s != nil && s.exp > time.Now().Add(5*time.Minute).Unix() && s.email == strings.ToLower(actor.Email) {
		return s, nil
	}
	email := strings.ToLower(strings.TrimSpace(actor.Email))
	if !emailRE.MatchString(email) {
		return nil, fmt.Errorf("the cloud person has no valid email")
	}
	_, ver := a.keyHash()
	sid := newID()
	now := time.Now()
	exp := now.Add(sessionTTL).Unix()
	if _, err := a.db.Exec(`INSERT INTO sessions(sessionId, createdAt, expiresAt, keyVersion, ip, userAgent) VALUES(?,?,?,?,?,?)`, sid, now.Unix(), exp, ver, "cloud", "OPM cloud · "+email); err != nil {
		return nil, err
	}
	tok := sign.MakeActorToken(a.key, model.ActorTokenClaims{Email: email, Sid: sid, Dep: a.dep, Site: a.site, Iat: now.Unix(), Exp: exp})
	_, _ = a.db.Exec(`INSERT INTO actors(email, lastUsedAt, count) VALUES(?,?,1) ON CONFLICT(email) DO UPDATE SET lastUsedAt = excluded.lastUsedAt, count = actors.count + 1`, email, nowTs())
	since := now
	if old := st.sessions[cloudSID]; old != nil {
		since = old.since
	}
	s := &remoteSess{sid: sid, token: tok, email: email, name: actor.Name, exp: exp, since: since}
	st.sessions[cloudSID] = s
	a.activity(&email, "agent", "remote-gui.session", trim200(email+" works on this site through the cloud (remote GUI)"),
		map[string]any{"siteSlug": a.site, "sessionId": sid, "cloudSessionId": cloudSID[:min(8, len(cloudSID))], "email": email})
	return s, nil
}

// remoteWriter buffers one response (capped).
type remoteWriter struct {
	h      http.Header
	status int
	buf    bytes.Buffer
	over   bool
}

func (w *remoteWriter) Header() http.Header { return w.h }
func (w *remoteWriter) WriteHeader(s int) {
	if w.status == 0 {
		w.status = s
	}
}
func (w *remoteWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.buf.Len()+len(b) > remoteGuiResponseMax {
		w.over = true
		return 0, fmt.Errorf("response over %d bytes", remoteGuiResponseMax)
	}
	return w.buf.Write(b)
}
func (w *remoteWriter) Flush() {}

// remoteGuiForbidden: what a remote session may not do (switching the remote GUI off is done on the site).
func remoteGuiForbidden(method, path string) bool {
	return method != "GET" && strings.HasPrefix(path, "/_opm/api/agent/remote-gui")
}

func (a *Agent) serveRemoteGui(ctx context.Context, it model.RemoteGuiRequest) {
	w := &remoteWriter{h: http.Header{}}
	var ws *remoteSess
	func() {
		u, err := url.Parse(it.Path)
		if err != nil || !(u.Path == "/_opm" || strings.HasPrefix(u.Path, "/_opm/")) || strings.HasPrefix(u.Path, "/_opm/peer") {
			w.WriteHeader(404)
			_, _ = w.Write([]byte("not found"))
			return
		}
		if remoteGuiForbidden(it.Method, u.Path) {
			writeErr(w, herr(model.ErrForbidden, "switch the remote GUI on / off on the site itself, not through the cloud", nil))
			return
		}
		sess, err := a.remoteSession(it.SessionID, it.Actor)
		if err != nil {
			writeErr(w, herr(model.ErrForbidden, err.Error(), nil))
			return
		}
		if it.WS != nil && it.WS.StreamID != "" { // §21 #118 g a WebSocket: relayed (remotegui_ws.go)
			ws = sess
			return
		}
		var body []byte
		if it.BodyB64 != nil {
			if body, err = base64.StdEncoding.DecodeString(*it.BodyB64); err != nil {
				writeErr(w, invalid("bad request body", nil))
				return
			}
		}
		rctx, cancel := context.WithTimeout(ctx, 55*time.Second)
		defer cancel()
		rctx = context.WithValue(rctx, remoteKey{}, &remoteInfo{email: sess.email, name: sess.name, token: sess.token, exp: sess.exp})
		r, err := http.NewRequestWithContext(rctx, it.Method, it.Path, bytes.NewReader(body))
		if err != nil {
			writeErr(w, invalid(err.Error(), nil))
			return
		}
		for k, v := range it.Headers {
			if strings.EqualFold(k, "cookie") || strings.EqualFold(k, "authorization") {
				continue
			}
			r.Header.Set(k, v)
		}
		// the person of the cloud session: the agent session cookie + its actor token
		r.AddCookie(&http.Cookie{Name: a.cookieName(), Value: sess.sid})
		r.Header.Set("Authorization", "Actor "+sess.token)
		r.Header.Del("Accept-Encoding") // bodies travel whole; the cloud rewrites text
		r.RemoteAddr = "cloud:0"
		r.Host = "opm-remote"
		a.handlerFor("agent").ServeHTTP(w, r)
	}()
	if ws != nil {
		a.serveRemoteWS(ctx, it, ws)
		return
	}
	if w.status == 0 {
		w.status = 200
	}
	if w.over {
		w.status = 413
		w.buf.Reset()
		w.buf.WriteString("the response is too large to show through the cloud")
		w.h = http.Header{"Content-Type": {"text/plain; charset=utf-8"}}
	}
	hdr := map[string]string{}
	keys := make([]string, 0, len(w.h))
	for k := range w.h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		hdr[k] = strings.Join(w.h[k], ", ")
	}
	hb, _ := json.Marshal(hdr)
	c, cancel := context.WithTimeout(a.ctxOr(ctx), 2*time.Minute)
	defer cancel()
	resp, err := a.cloudCall(c, "POST", "/v1/agent/remote-gui/"+url.PathEscape(it.ReqID)+"/result", w.buf.Bytes(), callOpts{timeout: 2 * time.Minute, extraHdr: map[string]string{
		"Content-Type": "application/octet-stream", "X-OPM-Remote-Status": fmt.Sprint(w.status), "X-OPM-Remote-Headers": base64.StdEncoding.EncodeToString(hb)}})
	if err != nil {
		a.log.Warn("remote-gui", "result not delivered", "path", pathOnly(it.Path), "error", err.Error())
		return
	}
	if resp.Status >= 300 {
		a.log.Warn("remote-gui", "result refused", "path", pathOnly(it.Path), "status", resp.Status, "body", string(resp.Body))
	}
}

func (a *Agent) ctxOr(c context.Context) context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return c
}

// ───────────────────────── GET / PUT /_opm/api/agent/remote-gui

func (a *Agent) remoteGuiSetting() model.RemoteGuiSetting {
	out := model.RemoteGuiSetting{Enabled: a.remoteGuiEnabled(), Sessions: []model.RemoteGuiSession{}}
	st := a.rgui()
	st.mu.Lock()
	defer st.mu.Unlock()
	seen := map[string]bool{}
	for _, s := range st.sessions {
		if s.exp > time.Now().Unix() && !seen[s.email] {
			seen[s.email] = true
			out.Sessions = append(out.Sessions, model.RemoteGuiSession{Email: s.email, Since: tsOf(s.since)})
		}
	}
	sort.Slice(out.Sessions, func(i, j int) bool { return out.Sessions[i].Email < out.Sessions[j].Email })
	return out
}

func (a *Agent) handleGetRemoteGui(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return a.remoteGuiSetting(), 200, nil
}

func (a *Agent) handlePutRemoteGui(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	if remoteOf(r) != nil {
		return nil, 0, herr(model.ErrForbidden, "switch the remote GUI on / off on the site itself, not through the cloud", nil)
	}
	var req model.RemoteGuiSetting
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if err := a.db.SetKV("remote-gui", model.RemoteGuiSetting{Enabled: req.Enabled}); err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	if !req.Enabled {
		// end the remote sessions at once
		st := a.rgui()
		st.mu.Lock()
		for k, s := range st.sessions {
			_, _ = a.db.Exec(`UPDATE sessions SET loggedOutAt = ? WHERE sessionId = ?`, time.Now().Unix(), s.sid)
			delete(st.sessions, k)
		}
		st.until = time.Time{}
		st.mu.Unlock()
	}
	state := "on"
	if !req.Enabled {
		state = "off"
	}
	a.activity(rc.actorPtr(), "agent", "agent.remote-gui-changed", "remote GUI through the cloud switched "+state, actorData(rc, map[string]any{"siteSlug": a.site, "enabled": req.Enabled}))
	return a.remoteGuiSetting(), 200, nil
}
