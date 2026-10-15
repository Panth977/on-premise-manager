package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/docker"
)

// Terminal limits (§21 #89 b).
const (
	maxSessions   = 4
	defaultIdle   = 30 * time.Minute
	ticketTTL     = 60 * time.Second
	idleWarnAhead = 60 * time.Second
)

type termProc interface {
	io.ReadWriter
	Resize(cols, rows int)
	Kill()
	Wait() int
}

type termSession struct {
	id        string
	kind      string // container | host
	target    string
	ref       string
	command   []string
	actor     string
	via       string
	sessionID string
	cols      int
	rows      int
	created   time.Time
	expires   time.Time // reservation (ticket) expiry
	open      bool
	started   time.Time
	lastIn    time.Time
	lastAct   time.Time
	bytesOut  int64
	closer    func(reason, by string)
	mu        sync.Mutex
}

type termManager struct {
	s        *Service
	mu       sync.Mutex
	sessions map[string]*termSession
	idle     time.Duration
}

func newTermManager(s *Service) *termManager {
	return &termManager{s: s, sessions: map[string]*termSession{}, idle: defaultIdle}
}

// SetIdleTimeout changes the idle timeout (tests).
func (s *Service) SetIdleTimeout(d time.Duration) { s.term.idle = d }

func (m *termManager) dir() string { return filepath.Join(m.s.h.DataDir(), "transcripts") }

// active counts open sessions + unexpired reservations.
func (m *termManager) activeLocked() int {
	n := 0
	now := time.Now()
	for id, ss := range m.sessions {
		if ss.open || now.Before(ss.expires) {
			n++
		} else {
			delete(m.sessions, id)
		}
	}
	return n
}

// ActiveSessions is the number of open + reserved terminals.
func (s *Service) ActiveSessions() int {
	s.term.mu.Lock()
	defer s.term.mu.Unlock()
	return s.term.activeLocked()
}

type createReq struct {
	Kind          string   `json:"kind"`
	ContainerSlug string   `json:"containerSlug"`
	Shell         string   `json:"shell"`
	Command       []string `json:"command"`
	Confirm       string   `json:"confirm"`
	Cols          int      `json:"cols"`
	Rows          int      `json:"rows"`
}

func (m *termManager) handleCreate(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	if e := m.s.rl.allow("terminal", c); e != nil {
		return nil, 0, e
	}
	var req createReq
	if e := readBody(r, &req); e != nil {
		return nil, 0, e
	}
	ss := &termSession{id: randToken(12), actor: c.Actor, via: c.Via, sessionID: c.SessionID, cols: clamp(req.Cols, 20, 500, 120), rows: clamp(req.Rows, 5, 200, 32), created: time.Now()}
	site := m.s.h.Site()
	switch req.Kind {
	case "container":
		if reason := m.s.cfg.containerExecReason(); reason != "" {
			return nil, 0, &Error{Status: 403, Code: "forbidden", Msg: "container terminals are " + reason, Details: map[string]any{"tool": "containerExec"}}
		}
		t, e := m.s.target(r.Context(), req.ContainerSlug)
		if e != nil {
			return nil, 0, e
		}
		if t.FailedCopy {
			return nil, 0, invalid("%s is a kept copy of a failed update: logs and inspect only", t.Ref)
		}
		if t.ID == "" {
			return nil, 0, &Error{Status: 409, Code: "conflict", Msg: t.Ref + " has no container (missing)"}
		}
		cj, err := m.s.h.Docker().ContainerInspect(r.Context(), t.ID)
		if err != nil {
			return nil, 0, unavailable(err)
		}
		if !cj.State.Running {
			return nil, 0, &Error{Status: 409, Code: "conflict", Msg: t.Ref + " is not running (" + cj.State.Status + "); start it first"}
		}
		switch req.Shell {
		case "", "sh":
			ss.command = []string{"/bin/sh"}
		case "bash":
			ss.command = []string{"/bin/bash"}
		case "custom":
			if len(req.Command) == 0 || len(req.Command) > 32 || strings.TrimSpace(req.Command[0]) == "" {
				return nil, 0, invalid("custom command: 1–32 arguments, the first one not empty")
			}
			for _, a := range req.Command {
				if len(a) > 1024 {
					return nil, 0, invalid("custom command: an argument is longer than 1024 characters")
				}
			}
			ss.command = req.Command
		default:
			return nil, 0, invalid("shell must be sh, bash or custom")
		}
		ss.kind, ss.target, ss.ref = "container", t.Ref, t.ID
	case "host":
		if reason := m.s.cfg.hostShellReason(); reason != "" {
			return nil, 0, &Error{Status: 403, Code: "forbidden", Msg: "the host shell is " + reason, Details: map[string]any{"tool": "hostShell"}}
		}
		if strings.TrimSpace(req.Confirm) != site {
			return nil, 0, &Error{Status: 422, Code: "invalid", Msg: fmt.Sprintf("type the site name %q to open a shell on the server", site),
				Details: map[string]any{"errors": []map[string]string{{"path": "confirm", "code": "V_CONFIRM", "message": "type " + site, "severity": "error"}}}}
		}
		user, shell := m.s.cfg.shellFor()
		ss.kind, ss.target, ss.command = "host", "host ("+user+")", []string{shell, "-l"}
	default:
		return nil, 0, invalid("kind must be container or host")
	}
	m.mu.Lock()
	if n := m.activeLocked(); n >= maxSessions {
		m.mu.Unlock()
		return nil, 0, &Error{Status: 409, Code: "conflict", Msg: fmt.Sprintf("%d terminals are open on site %s (the maximum); close one first", n, site), Details: map[string]any{"max": maxSessions, "open": n}}
	}
	tk, exp := m.s.tickets.issue(&ticket{kind: "terminal", ref: ss.id, sessionID: c.SessionID, via: c.Via, actor: c.Actor}, ticketTTL)
	ss.expires = exp
	m.sessions[ss.id] = ss
	m.mu.Unlock()
	return map[string]any{"sessionId": ss.id, "ticket": tk, "expiresAt": ts(exp),
		"wsPath": "/_opm/api/sites/" + site + "/tools/terminal/ws?ticket=" + tk}, 201, nil
}

func clamp(v, lo, hi, def int) int {
	if v == 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (m *termManager) view(ss *termSession) map[string]any {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	var actor, via *string
	if ss.actor != "" {
		a := ss.actor
		actor = &a
	}
	if ss.via != "" {
		v := ss.via
		via = &v
	}
	started := ss.started
	if started.IsZero() {
		started = ss.created
	}
	last := ss.lastAct
	if last.IsZero() {
		last = started
	}
	return map[string]any{"sessionId": ss.id, "siteSlug": m.s.h.Site(), "kind": ss.kind, "target": ss.target, "command": ss.command,
		"actor": actor, "startedAt": ts(started), "lastActivityAt": ts(last), "via": via, "bytesOut": ss.bytesOut, "open": ss.open}
}

func (m *termManager) handleList(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	m.mu.Lock()
	m.activeLocked()
	list := make([]*termSession, 0, len(m.sessions))
	for _, ss := range m.sessions {
		list = append(list, ss)
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].created.Before(list[j].created) })
	items := []map[string]any{}
	for _, ss := range list {
		items = append(items, m.view(ss))
	}
	return map[string]any{"items": items}, 200, nil
}

func (m *termManager) handleClose(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	id := r.PathValue("sessionId")
	m.mu.Lock()
	ss := m.sessions[id]
	m.mu.Unlock()
	if ss == nil {
		return nil, 0, notFound("no open terminal %s on site %s", id, m.s.h.Site())
	}
	ss.mu.Lock()
	closer := ss.closer
	ss.mu.Unlock()
	by := c.Actor
	if closer != nil {
		closer("ended by "+by, by)
	} else {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}
	return map[string]any{"closed": true}, 200, nil
}

func (m *termManager) closeAll(reason string) {
	m.mu.Lock()
	var cl []func(string, string)
	for _, ss := range m.sessions {
		ss.mu.Lock()
		if ss.closer != nil {
			cl = append(cl, ss.closer)
		}
		ss.mu.Unlock()
	}
	m.mu.Unlock()
	for _, f := range cl {
		f(reason, "")
	}
}

func (m *termManager) cleanupTranscripts() {
	m.mu.Lock()
	open := map[string]bool{}
	for id, ss := range m.sessions {
		if ss.open {
			open[id] = true
		}
	}
	m.mu.Unlock()
	if n := cleanupTranscriptDir(m.dir(), open); n > 0 {
		m.s.h.Log().Info("tools", "old terminal transcripts removed", "count", n)
	}
}

func control(ws *WSConn, v map[string]any) {
	b, _ := json.Marshal(v)
	_ = ws.WriteMessage(OpText, b)
}

// handleWS attaches a WebSocket to a reserved terminal (single-use ticket) and runs it until either side ends.
func (m *termManager) handleWS(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	if c.Via == "" && !SameOrigin(r) {
		return nil, 0, errf(403, "forbidden", "cross-origin WebSocket refused")
	}
	if !IsWebSocket(r) {
		return nil, 0, invalid("this endpoint needs a WebSocket upgrade")
	}
	tk, e := m.s.tickets.take(r.URL.Query().Get("ticket"), "terminal", c)
	if e != nil {
		return nil, 0, e
	}
	m.mu.Lock()
	ss := m.sessions[tk.ref]
	m.mu.Unlock()
	if ss == nil || ss.open {
		return nil, 0, errf(401, "unauthenticated", "the terminal reservation expired; start again")
	}
	extra := http.Header{}
	if c.Via != "" { // the forwarding site logs the session too (bridgeTerminal)
		extra.Set("X-OPM-Terminal-Id", ss.id)
		extra.Set("X-OPM-Terminal-Kind", ss.kind)
		extra.Set("X-OPM-Terminal-Target", ss.target)
		extra.Set("X-OPM-Terminal-Actor", ss.actor)
	}
	ws, err := UpgradeWith(w, r, extra)
	if err != nil {
		return nil, 0, invalid("%s", err.Error())
	}
	m.run(ss, ws)
	return nil, 0, nil
}

func (m *termManager) start(ctx context.Context, ss *termSession) (termProc, func() *int, error) {
	if ss.kind == "host" {
		user, shell := m.s.cfg.shellFor()
		p, err := startHostShell(user, shell, ss.cols, ss.rows, []string{"OPM_SITE=" + m.s.h.Site()})
		if err != nil {
			return nil, nil, err
		}
		return p, func() *int { code := p.Wait(); return &code }, nil
	}
	dc := m.s.h.Docker()
	execID, err := dc.ExecCreate(ctx, ss.ref, docker.ExecConfig{Cmd: ss.command, Tty: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
		Env: []string{"TERM=xterm-256color", "COLORTERM=truecolor"}})
	if err != nil {
		return nil, nil, err
	}
	conn, err := dc.ExecAttach(ctx, execID, true)
	if err != nil {
		return nil, nil, err
	}
	p := &execProc{dc: dc, id: execID, conn: conn}
	p.Resize(ss.cols, ss.rows)
	return p, func() *int {
		for i := 0; i < 20; i++ {
			st, err := dc.ExecInspect(context.Background(), execID)
			if err != nil {
				return nil
			}
			if !st.Running {
				return st.ExitCode
			}
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	}, nil
}

// execProc is a docker exec with a TTY.
type execProc struct {
	dc   *docker.Client
	id   string
	conn io.ReadWriteCloser
}

func (p *execProc) Read(b []byte) (int, error)  { return p.conn.Read(b) }
func (p *execProc) Write(b []byte) (int, error) { return p.conn.Write(b) }
func (p *execProc) Resize(cols, rows int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.dc.ExecResize(ctx, p.id, rows, cols)
}

// Kill: closing the hijacked connection hangs up the exec's TTY (the shell gets SIGHUP / EOF).
func (p *execProc) Kill()     { _ = p.conn.Close() }
func (p *execProc) Wait() int { return 0 }

func (m *termManager) run(ss *termSession, ws *WSConn) {
	site := m.s.h.Site()
	caller := Caller{Actor: ss.actor, SessionID: ss.sessionID, Via: ss.via}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc, wait, err := m.start(ctx, ss)
	if err != nil {
		control(ws, map[string]any{"type": "error", "message": err.Error()})
		ws.CloseWith(1011, "start failed")
		m.mu.Lock()
		delete(m.sessions, ss.id)
		m.mu.Unlock()
		m.s.h.Log().Warn("tools", "terminal start failed", "kind", ss.kind, "target", ss.target, "error", err.Error())
		return
	}
	now := time.Now()
	ss.mu.Lock()
	ss.open, ss.started, ss.lastIn, ss.lastAct = true, now, now, now
	ss.mu.Unlock()
	var actor, via *string
	if ss.actor != "" {
		actor = &ss.actor
	}
	if ss.via != "" {
		via = &ss.via
	}
	tr, terr := openTranscript(m.dir(), TranscriptMeta{ID: ss.id, SiteSlug: site, Kind: ss.kind, Target: ss.target, Command: ss.command, Actor: actor, Via: via,
		StartedAt: ts(now), ExpiresAt: ts(now.Add(transcriptDays * 24 * time.Hour))}, ss.cols, ss.rows, m.s.h.SecretValues())
	if terr != nil {
		m.s.h.Log().Warn("tools", "transcript not recorded", "error", terr.Error())
	}
	m.s.activity(caller, "terminal.opened", fmt.Sprintf("terminal opened: %s on %s", ss.target, site),
		map[string]any{"terminalId": ss.id, "kind": ss.kind, "target": ss.target, "command": ss.command, "siteSlug": site})
	m.s.h.Log().Info("tools", "terminal opened", "terminalId", ss.id, "kind", ss.kind, "target", ss.target, "actor", ss.actor, "via", ss.via)
	control(ws, map[string]any{"type": "ready", "sessionId": ss.id, "target": ss.target})

	var once sync.Once
	reasonCh := make(chan [2]string, 1)
	finish := func(reason, by string) {
		once.Do(func() {
			reasonCh <- [2]string{reason, by}
			proc.Kill()
			cancel()
		})
	}
	ss.mu.Lock()
	ss.closer = finish
	ss.mu.Unlock()

	// process → browser
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := proc.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				if tr != nil {
					tr.Output(chunk)
				}
				ss.mu.Lock()
				ss.bytesOut += int64(n)
				ss.lastAct = time.Now()
				ss.mu.Unlock()
				if werr := ws.WriteMessage(OpBinary, chunk); werr != nil {
					finish("browser disconnected", "")
					return
				}
			}
			if err != nil {
				finish("exited", "")
				return
			}
		}
	}()
	// browser → process
	go func() {
		for {
			op, msg, err := ws.ReadMessage()
			if err != nil {
				finish("browser disconnected", "")
				return
			}
			ss.mu.Lock()
			ss.lastIn, ss.lastAct = time.Now(), time.Now()
			ss.mu.Unlock()
			if op == OpBinary {
				if _, err := proc.Write(msg); err != nil {
					finish("exited", "")
					return
				}
				continue
			}
			var ctl struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
				Data string `json:"data"`
			}
			if json.Unmarshal(msg, &ctl) != nil {
				continue
			}
			switch ctl.Type {
			case "resize":
				cols, rows := clamp(ctl.Cols, 20, 500, 120), clamp(ctl.Rows, 5, 200, 32)
				proc.Resize(cols, rows)
				if tr != nil {
					tr.Resize(cols, rows)
				}
			case "ping":
				control(ws, map[string]any{"type": "pong"})
			case "input": // text input as JSON (fallback for clients that cannot send binary)
				if _, err := proc.Write([]byte(ctl.Data)); err != nil {
					finish("exited", "")
					return
				}
			}
		}
	}()
	// idle watchdog + transcript flush
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		warned := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if tr != nil {
					tr.flush()
				}
				ss.mu.Lock()
				idleFor := time.Since(ss.lastIn)
				ss.mu.Unlock()
				left := m.idle - idleFor
				switch {
				case left <= 0:
					control(ws, map[string]any{"type": "error", "message": fmt.Sprintf("closed after %s without input", m.idle)})
					finish("idle", "")
					return
				case left <= idleWarnAhead && !warned:
					warned = true
					control(ws, map[string]any{"type": "idle-warning", "secondsLeft": int(left.Seconds())})
				case left > idleWarnAhead:
					warned = false
				}
			}
		}
	}()
	<-ctx.Done()
	rb := <-reasonCh
	<-outDone
	code := wait()
	if tr != nil {
		tr.Close(code)
	}
	ex := map[string]any{"type": "exit", "code": nil, "reason": rb[0]}
	if code != nil {
		ex["code"] = *code
	}
	control(ws, ex)
	ws.CloseWith(1000, rb[0])
	ss.mu.Lock()
	dur := int64(time.Since(ss.started).Seconds())
	bytesOut := ss.bytesOut
	ss.open = false
	ss.mu.Unlock()
	m.mu.Lock()
	delete(m.sessions, ss.id)
	m.mu.Unlock()
	data := map[string]any{"terminalId": ss.id, "kind": ss.kind, "target": ss.target, "durationSec": dur, "reason": rb[0], "bytesOut": bytesOut, "siteSlug": site}
	if code != nil {
		data["exitCode"] = *code
	}
	if rb[1] != "" {
		data["closedBy"] = rb[1]
	}
	m.s.activity(caller, "terminal.closed", fmt.Sprintf("terminal closed: %s on %s (%s, %ds)", ss.target, site, rb[0], dur), data)
	m.s.h.Log().Info("tools", "terminal closed", "terminalId", ss.id, "reason", rb[0], "durationSec", dur)
}

// ───────────────────────── transcripts

func (m *termManager) handleTranscripts(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	return map[string]any{"items": listTranscripts(m.dir())}, 200, nil
}

func (m *termManager) transcriptMeta(id string) (*TranscriptMeta, *Error) {
	if !idRe.MatchString(id) {
		return nil, notFound("no transcript %s", id)
	}
	b, err := os.ReadFile(filepath.Join(m.dir(), id+".json"))
	if err != nil {
		return nil, notFound("no transcript %s on site %s (kept 7 days)", id, m.s.h.Site())
	}
	var meta TranscriptMeta
	if json.Unmarshal(b, &meta) != nil {
		return nil, notFound("no transcript %s", id)
	}
	return &meta, nil
}

func (m *termManager) handleTranscriptTicket(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	id := r.PathValue("id")
	if _, e := m.transcriptMeta(id); e != nil {
		return nil, 0, e
	}
	if e := m.s.rl.allow("download", c); e != nil {
		return nil, 0, e
	}
	format := r.URL.Query().Get("format")
	if format != "text" {
		format = "cast"
	}
	tk, exp := m.s.tickets.issue(&ticket{kind: "transcript", ref: id, sessionID: c.SessionID, via: c.Via, actor: c.Actor}, 5*time.Minute)
	return map[string]any{"url": "/_opm/api/sites/" + m.s.h.Site() + "/tools/terminal/transcripts/" + id + "/download?format=" + format + "&ticket=" + tk, "expiresAt": ts(exp)}, 200, nil
}

func (m *termManager) handleTranscriptDownload(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	id := r.PathValue("id")
	meta, e := m.transcriptMeta(id)
	if e != nil {
		return nil, 0, e
	}
	tk, e := m.s.tickets.take(r.URL.Query().Get("ticket"), "transcript", c)
	if e != nil {
		return nil, 0, e
	}
	if tk.ref != id {
		return nil, 0, errf(401, "unauthenticated", "the link is for another transcript")
	}
	path := filepath.Join(m.dir(), id+".cast")
	base := fmt.Sprintf("opm-%s-%s-%s", m.s.h.Site(), meta.Kind, strings.ReplaceAll(strings.NewReplacer(" ", "", "(", "-", ")", "").Replace(meta.Target), "/", "-"))
	if t, err := time.Parse(time.RFC3339Nano, meta.StartedAt); err == nil {
		base += "-" + t.UTC().Format("20060102-1504")
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("format") == "text" {
		b, err := castToText(path)
		if err != nil {
			return nil, 0, notFound("transcript file missing")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+base+`.txt"`)
		_, _ = w.Write(b)
		return nil, 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, notFound("transcript file missing")
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/x-asciicast")
	w.Header().Set("Content-Disposition", `attachment; filename="`+base+`.cast"`)
	_, _ = io.Copy(w, f)
	return nil, 0, nil
}
