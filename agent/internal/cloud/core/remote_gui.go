package core

import (
	"context"
	"crypto/rand"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"opm/agent/internal/cloud/doc"
)

// §21 #118 remote agent GUI: the in-memory hub of ONE cloud process: remote sessions (cookie → person + site, sliding
// expiry), the queue of browser requests per site, the waiting long-polls of the agents and the pending browser requests
// waiting for their result. Nothing is stored: a restart drops the sessions and fails the requests in flight. The cloud
// never calls a site: agents long-poll.

// the remote part of shared/src/remote-gui.ts
const (
	REMOTE_GUI_SESSION_SEC         = 8 * 3600
	REMOTE_GUI_REQUEST_TIMEOUT_SEC = 60
	REMOTE_GUI_CONNECTED_SEC       = 40
	REMOTE_GUI_REQUEST_BODY_MAX    = 16 * 1024 * 1024
	REMOTE_GUI_RESPONSE_BODY_MAX   = 64 * 1024 * 1024
	REMOTE_GUI_BATCH_MAX           = 8
	REMOTE_GUI_STATUS_HEADER       = "x-opm-remote-status"
	REMOTE_GUI_STREAM_HEADER       = "x-opm-remote-stream"
	REMOTE_GUI_HEADERS_HEADER      = "x-opm-remote-headers"
)

// RemoteGuiPrefix is remoteGuiPrefix.
func RemoteGuiPrefix(deploymentID, siteSlug string) string {
	return "/app/deployments/" + deploymentID + "/remote/" + siteSlug
}

var remotePrefixRE = regexp.MustCompile(`^/app/deployments/([A-Za-z0-9_-]{1,64})/remote/([a-z][a-z0-9-]{0,39})(/[^\n\r\x{2028}\x{2029}]*)?$`)

// RemoteTarget is the result of ParseRemoteGuiPath.
type RemoteTarget struct{ DeploymentID, SiteSlug, AgentPath string }

// ParseRemoteGuiPath is parseRemoteGuiPath: nil = not a remote GUI path.
func ParseRemoteGuiPath(path string) *RemoteTarget {
	m := remotePrefixRE.FindStringSubmatch(path)
	if m == nil {
		return nil
	}
	rest := "/_opm/"
	if m[3] != "" && m[3] != "/" {
		rest = m[3]
	}
	return &RemoteTarget{m[1], m[2], rest}
}

var remoteTextType = regexp.MustCompile(`(?i)^(text/html|text/css|text/javascript|application/javascript|application/x-javascript|application/manifest\+json)\b`)

// IsRewritableType is isRewritableType.
func IsRewritableType(ct string) bool { return ct != "" && remoteTextType.MatchString(ct) }

// RewriteRemoteText is rewriteRemoteText: a `/_opm` right after a quote, backtick, `(`, `=` or whitespace and followed by
// `/`, a quote, backtick, `?`, `#` or `)` becomes `<prefix>/_opm`.
func RewriteRemoteText(text, prefix string) string {
	const lit = "/_opm"
	var b strings.Builder
	copied := 0
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		lead := strings.ContainsRune("\"'`(=", r) || doc.IsJSSpace(r)
		if lead && strings.HasPrefix(text[i+n:], lit) {
			after := i + n + len(lit)
			if after < len(text) && strings.IndexByte("/\"'`?#)", text[after]) >= 0 {
				b.WriteString(text[copied : i+n])
				b.WriteString(prefix)
				b.WriteString(lit)
				copied = after
				i = after
				continue
			}
		}
		i += n
	}
	if copied == 0 {
		return text
	}
	b.WriteString(text[copied:])
	return b.String()
}

// RewriteRemoteLocation is rewriteRemoteLocation.
func RewriteRemoteLocation(loc, prefix string) string {
	if strings.HasPrefix(loc, "/_opm") {
		return prefix + loc
	}
	return loc
}

// DecodeUTF8 is Buffer#toString('utf8'): every maximal invalid subpart becomes one U+FFFD.
func DecodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var o strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		need, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case c < 0x80:
			o.WriteByte(c)
			i++
			continue
		case c >= 0xC2 && c <= 0xDF:
			need = 1
		case c >= 0xE0 && c <= 0xEF:
			need = 2
			if c == 0xE0 {
				lo = 0xA0
			} else if c == 0xED {
				hi = 0x9F
			}
		case c >= 0xF0 && c <= 0xF4:
			need = 3
			if c == 0xF0 {
				lo = 0x90
			} else if c == 0xF4 {
				hi = 0x8F
			}
		default:
			o.WriteRune(utf8.RuneError)
			i++
			continue
		}
		j := i + 1
		ok := true
		for k := 0; k < need; k++ {
			if j >= len(b) || b[j] < lo || b[j] > hi {
				ok = false
				break
			}
			lo, hi = 0x80, 0xBF
			j++
		}
		if ok {
			o.Write(b[i:j])
		} else {
			o.WriteRune(utf8.RuneError)
		}
		i = j
	}
	return o.String()
}

// RemoteSession is RemoteSession.
type RemoteSession struct {
	SessionID, DeploymentID, SiteSlug string
	Actor                             *doc.Map
	CreatedAt, ExpiresAt              int64
}

// RemoteResult is RemoteResult. Headers: name → string.
type RemoteResult struct {
	Status  int
	Headers *doc.Map
	Body    []byte
}

type remotePending struct {
	req                    *doc.Map
	deploymentID, siteSlug string
	handedOut              bool
	done                   chan RemoteResult
	timer                  *time.Timer
}

type remoteWaiter struct{ ch chan []any }

var rg = struct {
	sync.Mutex
	sessions map[string]*RemoteSession
	pending  map[string]*remotePending
	queues   map[string][]string
	waiters  map[string][]*remoteWaiter
	lastPoll map[string]int64
	streams  map[string]*WsStream
}{sessions: map[string]*RemoteSession{}, pending: map[string]*remotePending{}, queues: map[string][]string{}, waiters: map[string][]*remoteWaiter{}, lastPoll: map[string]int64{}, streams: map[string]*WsStream{}}

func hubNow() int64 { return time.Now().UnixMilli() }

// hubID is the id of a session, request, stream or connection: 18 random bytes.
func hubID() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return doc.B64URL(b)
}

func rgPrune() {
	now := hubNow()
	for k, s := range rg.sessions {
		if s.ExpiresAt <= now {
			delete(rg.sessions, k)
		}
	}
}

// OpenRemoteSession is openSession: a new remote session for a person (the cookie value is the session id).
func OpenRemoteSession(deploymentID, siteSlug string, actor *doc.Map) *RemoteSession {
	rg.Lock()
	defer rg.Unlock()
	rgPrune()
	now := hubNow()
	s := &RemoteSession{SessionID: hubID(), DeploymentID: deploymentID, SiteSlug: siteSlug, Actor: actor, CreatedAt: now, ExpiresAt: now + REMOTE_GUI_SESSION_SEC*1000}
	rg.sessions[s.SessionID] = s
	return s
}

// TouchRemoteSession is touchSession: the session of a cookie for this site (sliding expiry), else nil.
func TouchRemoteSession(sessionID, deploymentID, siteSlug string) *RemoteSession {
	if sessionID == "" {
		return nil
	}
	rg.Lock()
	defer rg.Unlock()
	s := rg.sessions[sessionID]
	if s == nil || s.DeploymentID != deploymentID || s.SiteSlug != siteSlug {
		return nil
	}
	if s.ExpiresAt <= hubNow() {
		delete(rg.sessions, sessionID)
		return nil
	}
	s.ExpiresAt = hubNow() + REMOTE_GUI_SESSION_SEC*1000
	c := *s
	return &c
}

// RemoteActiveSites is activeSites: CloudView.remoteGui of a deployment: one item per site with an open session.
func RemoteActiveSites(deploymentID string) []any {
	rg.Lock()
	defer rg.Unlock()
	rgPrune()
	by := map[string]int64{}
	var names []string
	for _, s := range rg.sessions {
		if s.DeploymentID != deploymentID {
			continue
		}
		if _, ok := by[s.SiteSlug]; !ok {
			names = append(names, s.SiteSlug)
		}
		by[s.SiteSlug] = max(by[s.SiteSlug], s.ExpiresAt)
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = doc.Obj("siteSlug", n, "until", isoMillis(float64(by[n])))
	}
	return out
}

func rgKey(d, s string) string { return d + "/" + s }

func rgConnected(d, s string) bool {
	t := rg.lastPoll[rgKey(d, s)]
	return t != 0 && hubNow()-t < REMOTE_GUI_CONNECTED_SEC*1000
}

// RemoteConnected is connected: the site long-polled within REMOTE_GUI_CONNECTED_SEC.
func RemoteConnected(deploymentID, siteSlug string) bool {
	rg.Lock()
	defer rg.Unlock()
	return rgConnected(deploymentID, siteSlug)
}

// RemoteForward is forward: queue a browser request for the site; returns its result (504 on timeout). req without reqId.
func RemoteForward(deploymentID, siteSlug string, req *doc.Map) RemoteResult {
	reqID := hubID()
	full := doc.Obj("reqId", reqID).Spread(req)
	p := &remotePending{req: full, deploymentID: deploymentID, siteSlug: siteSlug, done: make(chan RemoteResult, 1)}
	k := rgKey(deploymentID, siteSlug)
	rg.Lock()
	p.timer = time.AfterFunc(REMOTE_GUI_REQUEST_TIMEOUT_SEC*time.Second, func() {
		rg.Lock()
		defer rg.Unlock()
		if rg.pending[reqID] != p {
			return
		}
		delete(rg.pending, reqID)
		q := rg.queues[k]
		kept := q[:0:0]
		for _, x := range q {
			if x != reqID {
				kept = append(kept, x)
			}
		}
		rg.queues[k] = kept
		p.done <- remoteTimeoutPage(siteSlug, p.handedOut, rgConnected(deploymentID, siteSlug))
	})
	rg.pending[reqID] = p
	rg.queues[k] = append(rg.queues[k], reqID)
	rgWake(k)
	rg.Unlock()
	return <-p.done
}

func rgTake(k string) []any {
	q := rg.queues[k]
	out := []any{}
	for len(q) > 0 && len(out) < REMOTE_GUI_BATCH_MAX {
		p := rg.pending[q[0]]
		q = q[1:]
		if p == nil {
			continue
		}
		p.handedOut = true
		out = append(out, p.req)
	}
	rg.queues[k] = q
	return out
}

func rgWake(k string) {
	ws := rg.waiters[k]
	if len(ws) == 0 {
		return
	}
	rg.waiters[k] = ws[1:]
	ws[0].ch <- rgTake(k)
}

func rgRemoveWaiter(k string, w *remoteWaiter) bool {
	ws := rg.waiters[k]
	for i, x := range ws {
		if x == w {
			rg.waiters[k] = append(ws[:i:i], ws[i+1:]...)
			return true
		}
	}
	return false
}

// RemoteNext is next: an agent long-poll: the queued requests at once, else wait ≤ waitMs for the next one.
func RemoteNext(cx context.Context, deploymentID, siteSlug string, waitMs int64) []any {
	k := rgKey(deploymentID, siteSlug)
	rg.Lock()
	rg.lastPoll[k] = hubNow()
	now := rgTake(k)
	if len(now) > 0 || waitMs <= 0 {
		rg.Unlock()
		return now
	}
	w := &remoteWaiter{ch: make(chan []any, 1)}
	rg.waiters[k] = append(rg.waiters[k], w)
	rg.Unlock()
	t := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
	defer t.Stop()
	stamp := func() {
		rg.Lock()
		rg.lastPoll[k] = hubNow()
		rg.Unlock()
	}
	select {
	case items := <-w.ch:
		stamp()
		return items
	case <-t.C:
		rg.Lock()
		gone := rgRemoveWaiter(k, w)
		rg.Unlock()
		stamp()
		if !gone {
			return <-w.ch
		}
		return []any{}
	case <-cx.Done():
		rg.Lock()
		gone := rgRemoveWaiter(k, w)
		rg.Unlock()
		if !gone {
			return <-w.ch
		}
		return []any{}
	}
}

// RemoteDeliver is deliver: the site's answer to a request it was handed (false: unknown / timed out / another site).
func RemoteDeliver(deploymentID, siteSlug, reqID string, r RemoteResult) bool {
	rg.Lock()
	defer rg.Unlock()
	p := rg.pending[reqID]
	if p == nil || p.deploymentID != deploymentID || p.siteSlug != siteSlug {
		return false
	}
	delete(rg.pending, reqID)
	p.timer.Stop()
	p.done <- r
	return true
}

var htmlEsc = strings.NewReplacer("&", "&#38;", "<", "&#60;", ">", "&#62;", `"`, "&#34;", "'", "&#39;")

// RemotePage is page.
func RemotePage(status int, title, text string) RemoteResult {
	html := `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + htmlEsc.Replace(title) + `</title><style>body{font:15px/1.5 system-ui,sans-serif;max-width:640px;margin:12vh auto;padding:0 16px;color:#222}@media(prefers-color-scheme:dark){body{background:#111;color:#ddd}}h1{font-size:20px}</style><h1>` + htmlEsc.Replace(title) + `</h1><p>` + htmlEsc.Replace(text) + `</p>`
	return RemoteResult{status, doc.Obj("Content-Type", "text/html; charset=utf-8", "Cache-Control", "no-store"), []byte(html)}
}

func remoteTimeoutPage(siteSlug string, handedOut, isConnected bool) RemoteResult {
	if handedOut {
		return RemotePage(504, "Site "+siteSlug+" did not answer in time", "The site took the request but sent no answer within 60 s. Reload to try again.")
	}
	if isConnected {
		return RemotePage(504, "Site "+siteSlug+" is busy", "The site is connected but did not pick up the request within 60 s. Reload to try again.")
	}
	return RemotePage(503, "Site "+siteSlug+" is not connected", "The site did not ask the cloud for requests. It needs agent 0.19.0 or newer, the remote GUI switched on (agent GUI › Agent), and a connection to the cloud. It starts within a few seconds of opening this page — reload in a moment.")
}

// ───────────────────────── §21 #118 g WebSocket streams (browser ⇄ cloud ⇄ site, the site long-polls)

// RecvResult is what the site's recv long-poll answers.
type RecvResult struct {
	Frames []any
	Closed bool
}

// Map is the JSON answer.
func (r RecvResult) Map() *doc.Map { return doc.Obj("frames", r.Frames, "closed", r.Closed) }

type recvWaiter struct{ ch chan RecvResult }

// WsStream is WsStream. All fields are guarded by the hub lock; ToBrowser runs under it and must not block.
type WsStream struct {
	StreamID, DeploymentID, SiteSlug string
	toSite                           []any
	recvWaiter                       *recvWaiter
	toBrowser                        func(f *doc.Map)
	early                            []any
	closed                           bool
	lastAt                           int64
}

// NewRemoteStream is newStream.
func NewRemoteStream(deploymentID, siteSlug string) *WsStream {
	s := &WsStream{StreamID: hubID(), DeploymentID: deploymentID, SiteSlug: siteSlug, lastAt: hubNow()}
	rg.Lock()
	rg.streams[s.StreamID] = s
	rg.Unlock()
	return s
}

// RemoteStreamOf is streamOf.
func RemoteStreamOf(streamID, deploymentID, siteSlug string) *WsStream {
	rg.Lock()
	defer rg.Unlock()
	s := rg.streams[streamID]
	if s != nil && s.DeploymentID == deploymentID && s.SiteSlug == siteSlug {
		return s
	}
	return nil
}

// Closed is s.closed.
func (s *WsStream) Closed() bool {
	rg.Lock()
	defer rg.Unlock()
	return s.closed
}

func spliceFrames(s *WsStream) []any {
	out := s.toSite
	s.toSite = nil
	if out == nil {
		out = []any{}
	}
	return out
}

func toSiteLocked(s *WsStream, f *doc.Map) {
	s.lastAt = hubNow()
	if f != nil {
		s.toSite = append(s.toSite, f)
	} else {
		s.closed = true
	}
	if s.recvWaiter != nil {
		w := s.recvWaiter
		s.recvWaiter = nil
		w.ch <- RecvResult{spliceFrames(s), s.closed}
	}
	if s.closed && len(s.toSite) == 0 {
		time.AfterFunc(60*time.Second, func() {
			rg.Lock()
			delete(rg.streams, s.StreamID)
			rg.Unlock()
		})
	}
}

// RemoteToSite is toSite: a message from the browser (nil: the browser's close).
func RemoteToSite(s *WsStream, f *doc.Map) {
	rg.Lock()
	defer rg.Unlock()
	toSiteLocked(s, f)
}

// RemoteRecv is recv: the site's long-poll for browser messages.
func RemoteRecv(cx context.Context, s *WsStream, waitMs int64) RecvResult {
	rg.Lock()
	s.lastAt = hubNow()
	if len(s.toSite) > 0 || s.closed || waitMs <= 0 {
		defer rg.Unlock()
		return RecvResult{spliceFrames(s), s.closed}
	}
	w := &recvWaiter{ch: make(chan RecvResult, 1)}
	s.recvWaiter = w
	rg.Unlock()
	t := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
	defer t.Stop()
	select {
	case r := <-w.ch:
		return r
	case <-t.C:
		rg.Lock()
		defer rg.Unlock()
		if s.recvWaiter == w {
			s.recvWaiter = nil
		}
		select {
		case r := <-w.ch:
			return r
		default:
		}
		return RecvResult{spliceFrames(s), s.closed}
	case <-cx.Done():
		rg.Lock()
		defer rg.Unlock()
		if s.recvWaiter == w {
			s.recvWaiter = nil
		}
		return RecvResult{[]any{}, s.closed}
	}
}

func anyClose(frames []any) bool {
	for _, f := range frames {
		if m, ok := f.(*doc.Map); ok && m.Val("op") == "close" {
			return true
		}
	}
	return false
}

// RemoteToBrowser is toBrowser: messages from the site to the browser; a close frame ends the stream.
func RemoteToBrowser(s *WsStream, frames []any) bool {
	rg.Lock()
	defer rg.Unlock()
	s.lastAt = hubNow()
	if s.toBrowser == nil {
		// an event stream: the browser response starts right after the result; keep what comes before (bounded)
		if s.closed || len(s.early) > 256 {
			return false
		}
		s.early = append(s.early, frames...)
		return true
	}
	for _, f := range frames {
		s.toBrowser(f.(*doc.Map))
	}
	if anyClose(frames) {
		s.closed = true
		toSiteLocked(s, nil)
	}
	return true
}

// RemoteSetBrowser sets toBrowser of a WebSocket stream (nothing can have arrived: the agent relays after the 101).
func RemoteSetBrowser(s *WsStream, fn func(f *doc.Map)) { RemoteAttachBrowser(s, fn) }

// RemoteAttachBrowser is attachBrowser: the browser side of an event stream is ready: hand it what already arrived.
func RemoteAttachBrowser(s *WsStream, fn func(f *doc.Map)) {
	rg.Lock()
	defer rg.Unlock()
	s.toBrowser = fn
	early := s.early
	s.early = nil
	for _, f := range early {
		fn(f.(*doc.Map))
	}
	if anyClose(early) {
		s.closed = true
		toSiteLocked(s, nil)
	}
}
