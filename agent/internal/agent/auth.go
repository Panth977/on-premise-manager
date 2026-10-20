package agent

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

const sessionTTL = 6 * time.Hour

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type authKind int

const (
	authPublic authKind = iota
	authSession
	authActor
)

// withAuth wraps an API handler with the session / actor checks of §4.6.
func (a *Agent) withAuth(kind authKind, h func(w http.ResponseWriter, r *http.Request) (any, int, *httpError)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc := getRC(r)
		if rc.peer == nil && kind != authPublic {
			if e := a.checkSession(r, rc); e != nil {
				writeErr(w, e)
				return
			}
			if kind == authActor {
				if e := a.checkActor(r, rc); e != nil {
					writeErr(w, e)
					return
				}
			}
		}
		if rc.peer != nil && kind == authActor && rc.actor == "" {
			writeErr(w, herr(model.ErrActorRequired, "forwarded request without X-OPM-Actor", nil))
			return
		}
		v, st, e := h(w, r)
		if e != nil {
			writeErr(w, e)
			return
		}
		if v == nil {
			return // handler wrote the response
		}
		writeJSON(w, st, v)
	}
}

// cookieName is unique per agent so two agents on one host (cookies ignore ports) do not overwrite each other.
func (a *Agent) cookieName() string { return "opm_session_" + a.site }

// sessionCookie: opm_session_<siteSlug> first, then the legacy opm_session (transition; see SPEC §4.6).
func (a *Agent) sessionCookie(r *http.Request) string {
	if c, err := r.Cookie(a.cookieName()); err == nil && c.Value != "" {
		return c.Value
	}
	if c, err := r.Cookie("opm_session"); err == nil {
		return c.Value
	}
	return ""
}

func (a *Agent) setSessionCookies(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	for _, n := range []string{a.cookieName(), "opm_session"} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: value, Path: "/_opm", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Secure: secure})
	}
}

func (a *Agent) checkSession(r *http.Request, rc *reqCtx) *httpError {
	val := a.sessionCookie(r)
	if val == "" {
		return herr(model.ErrUnauthenticated, "log in first", nil)
	}
	c := &http.Cookie{Value: val}
	var exp int64
	var out sql.NullInt64
	if err := a.db.QueryRow(`SELECT expiresAt, loggedOutAt FROM sessions WHERE sessionId = ?`, c.Value).Scan(&exp, &out); err != nil || out.Valid {
		return herr(model.ErrUnauthenticated, "no such session", nil)
	}
	if exp <= time.Now().Unix() {
		return herr(model.ErrSessionExpired, "the session has expired; log in again", nil)
	}
	rc.sessionID, rc.expiresAt = c.Value, exp
	return nil
}

func (a *Agent) checkActor(r *http.Request, rc *reqCtx) *httpError {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Actor ") {
		return herr(model.ErrActorRequired, "declare who is working (Authorization: Actor <token>)", nil)
	}
	claims, err := sign.ParseActorToken(a.pub, strings.TrimPrefix(h, "Actor "))
	if err != nil {
		return herr(model.ErrActorRequired, "actor token invalid: "+err.Error(), nil)
	}
	if claims.Sid != rc.sessionID || claims.Exp <= time.Now().Unix() || claims.Dep != a.dep || claims.Site != a.site {
		return herr(model.ErrActorRequired, "actor token does not match this session / site", nil)
	}
	rc.actor = claims.Email
	return nil
}

// actorData adds sessionId (and the forwarding site) to activity data.
func actorData(rc *reqCtx, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	if rc != nil && rc.sessionID != "" {
		data["sessionId"] = rc.sessionID
	}
	return data
}

func (a *Agent) handleLogin(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.LoginRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	hash, ver := a.keyHash()
	if hash == "" {
		return nil, 0, herr(model.ErrUnavailable, "this site has no access key yet; it gets it from the cloud or a peer", nil)
	}
	ip := rc.ip
	now := time.Now().Unix()
	var fails int
	var win int64
	if err := a.db.QueryRow(`SELECT fails, windowStart FROM login_limits WHERE ip = ?`, ip).Scan(&fails, &win); err == nil {
		if now-win >= 300 {
			fails, win = 0, now
		}
		if fails >= 5 {
			retry := 300 - (now - win)
			if retry < 1 {
				retry = 1
			}
			e := herr(model.ErrRateLimited, fmt.Sprintf("too many failed logins; try again in %d s", retry), map[string]any{"retryAfterSec": retry})
			e.header = map[string]string{"Retry-After": strconv.FormatInt(retry, 10)}
			return nil, 0, e
		}
	} else {
		fails, win = 0, now
	}
	key := sign.NormalizeAccessKey(req.AccessKey)
	ok := false
	if len(key) == 24 {
		ok, _ = sign.VerifyPHC(hash, key)
	}
	if !ok {
		fails++
		_, _ = a.db.Exec(`INSERT INTO login_limits(ip, fails, windowStart) VALUES(?,?,?) ON CONFLICT(ip) DO UPDATE SET fails = excluded.fails, windowStart = excluded.windowStart`, ip, fails, win)
		a.activity(nil, "agent", "login.failed", "login failed", map[string]any{"siteSlug": a.site, "ip": ip, "userAgent": rc.ua})
		return nil, 0, herr(model.ErrInvalidKey, fmt.Sprintf("wrong access key (%d of 5)", fails), map[string]any{"failures": fails, "max": 5})
	}
	_, _ = a.db.Exec(`DELETE FROM login_limits WHERE ip = ?`, ip)
	sid := newID()
	exp := time.Now().Add(sessionTTL)
	_, err := a.db.Exec(`INSERT INTO sessions(sessionId, createdAt, expiresAt, keyVersion, ip, userAgent) VALUES(?,?,?,?,?,?)`, sid, now, exp.Unix(), ver, ip, rc.ua)
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	a.setSessionCookies(w, r, sid, int(sessionTTL.Seconds()))
	a.activity(nil, "agent", "login.succeeded", "login", map[string]any{"sessionId": sid, "siteSlug": a.site, "keyVersion": ver, "ip": ip, "userAgent": rc.ua})
	return model.LoginResponse{SessionID: sid, ExpiresAt: tsOf(exp), KeyVersion: ver}, 200, nil
}

func (a *Agent) handleLogout(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	_, _ = a.db.Exec(`UPDATE sessions SET loggedOutAt = ? WHERE sessionId = ?`, time.Now().Unix(), rc.sessionID)
	a.setSessionCookies(w, r, "", -1)
	data := map[string]any{"sessionId": rc.sessionID, "siteSlug": a.site}
	var actor *string
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Actor ") {
		if c, err := sign.ParseActorToken(a.pub, strings.TrimPrefix(h, "Actor ")); err == nil && c.Sid == rc.sessionID {
			data["actor"] = c.Email
			actor = &c.Email
		}
	}
	a.activity(actor, "agent", "logout", "logout", data)
	return model.LoggedOutResponse{LoggedOut: true}, 200, nil
}

func (a *Agent) handleSession(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var kv int
	_ = a.db.QueryRow(`SELECT keyVersion FROM sessions WHERE sessionId = ?`, rc.sessionID).Scan(&kv)
	out := model.SessionView{LoginResponse: model.LoginResponse{SessionID: rc.sessionID, ExpiresAt: tsOf(time.Unix(rc.expiresAt, 0)), KeyVersion: kv}}
	if ri := remoteOf(r); ri != nil { // §21 #118 through the cloud: the GUI adopts the person (no email prompt)
		out.Remote = &model.SessionRemote{Email: ri.email, Name: ri.name, ActorToken: ri.token, ExpiresAt: tsOf(time.Unix(ri.exp, 0))}
	}
	return out, 200, nil
}

func (a *Agent) handleActor(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.ActorRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if len(email) > 254 || !emailRE.MatchString(email) {
		return nil, 0, invalid("email is not valid", []model.ValidationIssue{{Path: "email", Code: "V_FORMAT", Message: "enter an email address", Severity: "error"}})
	}
	a.mu.Lock()
	var recent []time.Time
	for _, t := range a.actorRate[rc.sessionID] {
		if time.Since(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 30 {
		a.mu.Unlock()
		e := herr(model.ErrRateLimited, "too many actor declarations", nil)
		e.header = map[string]string{"Retry-After": "60"}
		return nil, 0, e
	}
	a.actorRate[rc.sessionID] = append(recent, time.Now())
	a.mu.Unlock()
	tok := sign.MakeActorToken(a.key, model.ActorTokenClaims{Email: email, Sid: rc.sessionID, Dep: a.dep, Site: a.site, Iat: time.Now().Unix(), Exp: rc.expiresAt})
	_, _ = a.db.Exec(`INSERT INTO actors(email, lastUsedAt, count) VALUES(?,?,1) ON CONFLICT(email) DO UPDATE SET lastUsedAt = excluded.lastUsedAt, count = actors.count + 1`, email, nowTs())
	a.activity(&email, "agent", "actor.declared", email+" is working", map[string]any{"email": email, "sessionId": rc.sessionID, "siteSlug": a.site, "userAgent": rc.ua, "ip": rc.ip})
	return model.ActorResponse{ActorToken: tok, ExpiresAt: tsOf(time.Unix(rc.expiresAt, 0)), Email: email}, 200, nil
}

func (a *Agent) cleanupSessions() {
	cut := time.Now().Add(-7 * 24 * time.Hour).Unix()
	_, _ = a.db.Exec(`DELETE FROM sessions WHERE expiresAt < ?`, cut)
	_, _ = a.db.Exec(`DELETE FROM nonces WHERE expiresAt < ?`, time.Now().Unix())
	_, _ = a.db.Exec(`DELETE FROM login_limits WHERE windowStart < ?`, time.Now().Add(-10*time.Minute).Unix())
	_, _ = a.db.Exec(`DELETE FROM intents WHERE expiresAt < ?`, time.Now().Add(-24*time.Hour).Unix())
	_, _ = a.db.Exec(`DELETE FROM intent_waits WHERE intentId NOT IN (SELECT intentId FROM intents WHERE usedAt IS NULL)`)
}
