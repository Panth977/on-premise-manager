// Package devtools runs the agent's dev tools (SPEC-final §7.6, §21 #97): an HTTP client (Postman-like),
// PostgreSQL, SQLite, Cassandra (CQL), Redis and MQTT — the queries run ON THE SITE SERVER, the agent GUI is only the
// workbench. It is mounted by the agent under /_opm/api/sites/{site}/devtools/* (and /_opm/peer/site/devtools/* for
// requests forwarded over the mesh); the agent checks the session / actor / signed peer before a handler runs, the
// handlers here add the switch, rate limits, tickets, pooled clients and activities.
package devtools

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// Caller is who sends a request (filled by the agent from its request context).
type Caller struct {
	Actor     string // declared actor (empty for session-only reads)
	SessionID string // GUI session id, or "<sid>@<site>" when forwarded by a peer
	Via       string // the forwarding site when the request came over the mesh ("" = local)
	IP        string
}

func (c Caller) key() string { return c.SessionID + "|" + c.Via }

// Head is what the dev tools need from the head revision of this site.
type Head struct {
	Seq        int
	RevID      string
	Presets    []model.DevToolPreset
	Containers []HeadContainer
	Volumes    []HeadVolume
	Secrets    []string // slugs
	Variables  []string // slugs
}

// HeadContainer is one container of the head on this site.
type HeadContainer struct {
	ContainerSlug string
	ServiceSlug   string
	Image         string
	Enabled       bool
	DockerID      string // "" = not created
	DockerName    string
	Running       bool
	Network       string
	Ports         []HeadPort
	Mounts        []HeadMount
	// Env (§21 #99): the env names of the container (image env + container env) and whether each holds a secret —
	// names only (containers.<c>.env.<NAME> completion in the agent GUI); values stay on the agent.
	Env []HeadEnv
}

// HeadEnv is one env name of a HeadContainer.
type HeadEnv struct {
	Name   string
	Secret bool
}

// HeadPort is an image port of a container.
type HeadPort struct {
	Name          string
	ContainerPort int
	Protocol      string
	HostPort      *int
	HostIP        string
}

// HeadMount maps a container path to a host path.
type HeadMount struct {
	Target     string // path inside the container
	Source     string // host path
	Kind       string // volume | host | file | secret
	VolumeSlug *string
	ReadOnly   bool
}

// HeadVolume is a declared site volume.
type HeadVolume struct {
	Slug  string
	Label string
	Kind  string // dir | file
	Path  string
}

// Host is what the dev tools need from the agent.
type Host interface {
	Site() string
	DataDir() string
	ConfigPath() string
	// DB is the agent SQLite (dev-tool tables + the activities read by history).
	DB() *sql.DB
	Docker() *docker.Client
	Log() *dlog.Logger
	// Head returns the head of this site (nil = no head revision yet).
	Head() *Head
	// ResolveExpr resolves a value with {{ }} against the head config of this site (secrets resolved; field devtool:
	// roots var, secret, site, self, deployment, containers incl. containers.<c>.env.<NAME>, §21 #99).
	ResolveExpr(src string) (*expr.Result, error)
	// SecretValues are every known secret value of the site (masking).
	SecretValues() []string
	// Activity writes an activity and returns its id ("" = not written).
	Activity(actor *string, typ, summary string, data map[string]any) string
	// SessionActor is the actor declared in a GUI session ("" = unknown).
	SessionActor(sessionID string) string
}

// Error is an API error (code from the shared ErrorCode list).
type Error struct {
	Status  int
	Code    string
	Msg     string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func errf(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Msg: fmt.Sprintf(format, a...)}
}
func notFound(format string, a ...any) *Error  { return errf(404, model.ErrNotFound, format, a...) }
func invalid(format string, a ...any) *Error   { return errf(422, model.ErrInvalid, format, a...) }
func forbidden(format string, a ...any) *Error { return errf(403, model.ErrForbidden, format, a...) }
func conflict(format string, a ...any) *Error  { return errf(409, model.ErrConflict, format, a...) }
func unavailable(format string, a ...any) *Error {
	return errf(503, model.ErrUnavailable, format, a...)
}

// confirmRequired is the 422 of a destructive statement without the typed connection name.
func confirmRequired(name string, reasons []string) *Error {
	return &Error{Status: 422, Code: model.ErrInvalid, Msg: fmt.Sprintf("destructive (%s): type the connection name %q to confirm", strings.Join(reasons, ", "), name),
		Details: map[string]any{"confirmRequired": true, "reasons": reasons}}
}

// AuthKind of a route (checked by the agent wrapper).
type AuthKind int

const (
	AuthSession AuthKind = iota
	AuthActor
)

// Handler returns a JSON value (nil = the handler wrote the response itself), the status, or an error.
type Handler func(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error)

// Route is one endpoint under /devtools/ (pattern without the method).
type Route struct {
	Method  string
	Pattern string
	Auth    AuthKind
	Fn      Handler
}

// Service is the dev tools of one site.
type Service struct {
	h       Host
	cfg     *configWatcher
	pool    *pool
	rl      *rateLimiter
	tickets *ticketStore
	results *resultCache
	running *runningSet
	mqtt    *mqttManager
	schema  *schemaCache
	clock   func() time.Time
	initErr error
	once    sync.Once

	lastTargetMu sync.Mutex
	lastTarget   map[string]string // connection id → the address resolved last
}

// New creates the dev tools of a site (Start runs its loops).
func New(h Host) *Service {
	s := &Service{h: h, cfg: newConfigWatcher(h.ConfigPath()), rl: newRateLimiter(), tickets: newTicketStore(), results: newResultCache(),
		running: newRunningSet(), schema: newSchemaCache(), clock: time.Now, lastTarget: map[string]string{}}
	s.pool = newPool(s)
	s.mqtt = newMQTTManager(s)
	return s
}

// SetConfig overrides agent.json (tests).
func (s *Service) SetConfig(c Config) { s.cfg.set(c) }

// Config is the effective config.
func (s *Service) Config() Config { return s.cfg.get() }

func (s *Service) limits() Limits { return s.cfg.get().Limits }

// Start creates the tables and runs the idle sweeper until ctx ends (then every pooled client is closed).
func (s *Service) Start(ctx context.Context) {
	s.ensureTables()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				s.pool.closeAll()
				s.mqtt.closeAll("agent stopping")
				return
			case <-t.C:
				s.pool.sweep()
				s.results.sweep()
				s.tickets.sweep()
				s.mqtt.sweep()
			}
		}
	}()
}

func (s *Service) ensureTables() error {
	s.once.Do(func() { s.initErr = createTables(s.h.DB()) })
	return s.initErr
}

func (s *Service) logWarn(msg string, kv ...any) {
	if l := s.h.Log(); l != nil {
		l.Warn("devtools", msg, kv...)
	}
}

// Routes lists every endpoint (paths relative to /_opm/api/sites/{site}).
func (s *Service) Routes() []Route {
	r := func(m, p string, a AuthKind, fn Handler) Route {
		return Route{Method: m, Pattern: "/devtools" + p, Auth: a, Fn: s.guard(p, fn)}
	}
	const C = "/connections/{connectionId}"
	return []Route{
		r("GET", "/info", AuthSession, s.handleInfo),
		// connections
		r("GET", "/connections", AuthSession, s.handleListConnections),
		r("POST", "/connections", AuthActor, s.handleCreateConnection),
		r("POST", "/connections/test", AuthActor, s.handleTestDraft),
		r("PUT", C, AuthActor, s.handleUpdateConnection),
		r("DELETE", C, AuthActor, s.handleDeleteConnection),
		r("POST", C+"/duplicate", AuthActor, s.handleDuplicate),
		r("POST", C+"/test", AuthActor, s.handleTestConnection),
		r("POST", C+"/disconnect", AuthActor, s.handleDisconnect),
		r("POST", C+"/reveal", AuthActor, s.handleReveal),
		// schema
		r("GET", C+"/schema", AuthSession, s.handleSchema),
		r("GET", C+"/schema/table", AuthSession, s.handleTableDetail),
		// execute
		r("POST", C+"/sql", AuthActor, s.handleExecuteSQL),
		r("POST", C+"/redis", AuthActor, s.handleExecuteRedis),
		r("POST", C+"/http", AuthActor, s.handleExecuteHTTP),
		r("POST", C+"/cancel", AuthActor, s.handleCancel),
		r("GET", "/results/{resultId}", AuthSession, s.handleResultPage),
		r("GET", "/results/{resultId}/value", AuthSession, s.handleResultValue),
		r("GET", "/results/{resultId}/export", AuthSession, s.handleResultExport),
		r("GET", "/results/{resultId}/body", AuthSession, s.handleResultBody),
		// redis browser
		r("GET", C+"/redis/keys", AuthSession, s.handleRedisScan),
		r("GET", C+"/redis/key", AuthSession, s.handleRedisKey),
		r("GET", C+"/redis/info", AuthSession, s.handleRedisInfo),
		// mqtt
		r("POST", C+"/mqtt/publish", AuthActor, s.handleMQTTPublish),
		r("POST", C+"/mqtt/sessions", AuthActor, s.handleMQTTSession),
		r("GET", C+"/mqtt/sessions/{sessionId}/stream", AuthSession, s.handleMQTTStream),
		r("POST", C+"/mqtt/sessions/{sessionId}/subscribe", AuthActor, s.handleMQTTSubscribe),
		r("POST", C+"/mqtt/sessions/{sessionId}/unsubscribe", AuthActor, s.handleMQTTUnsubscribe),
		r("POST", C+"/mqtt/sessions/{sessionId}/close", AuthActor, s.handleMQTTClose),
		r("GET", C+"/mqtt/topics", AuthSession, s.handleMQTTTopics),
		// saved, environments, history
		r("GET", "/saved", AuthSession, s.handleListSaved),
		r("POST", "/saved", AuthActor, s.handleCreateSaved),
		r("PUT", "/saved/{savedId}", AuthActor, s.handleUpdateSaved),
		r("DELETE", "/saved/{savedId}", AuthActor, s.handleDeleteSaved),
		r("GET", "/environments", AuthSession, s.handleListEnvironments),
		r("POST", "/environments", AuthActor, s.handleCreateEnvironment),
		r("PUT", "/environments/{environmentId}", AuthActor, s.handleUpdateEnvironment),
		r("DELETE", "/environments/{environmentId}", AuthActor, s.handleDeleteEnvironment),
		r("POST", "/environments/{environmentId}/reveal", AuthActor, s.handleRevealEnvironment),
		r("GET", "/history", AuthSession, s.handleHistory),
	}
}

// Streaming reports whether a GET endpoint (rest = path after /devtools/) streams (SSE): copied with flushes when
// forwarded over the mesh.
func Streaming(rest string) bool {
	p := rest
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return strings.HasPrefix(p, "connections/") && strings.Contains(p, "/mqtt/sessions/") && strings.HasSuffix(p, "/stream")
}

// guard applies the switch (agent.json tools.devTools) and the tables to every route but info.
func (s *Service) guard(p string, fn Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
		if p != "/info" {
			if reason := s.cfg.get().Reason(); reason != "" {
				return nil, 0, &Error{Status: 403, Code: model.ErrForbidden, Msg: reason, Details: map[string]any{"reason": reason, "setting": "tools.devTools"}}
			}
			if err := s.ensureTables(); err != nil {
				return nil, 0, unavailable("dev tools storage: %s", err.Error())
			}
		}
		return fn(w, r, c)
	}
}

func actorPtr(c Caller) *string {
	if c.Actor == "" {
		return nil
	}
	a := c.Actor
	return &a
}

// activity writes an activity with sessionId / via (returns the activity id or nil).
func (s *Service) activity(c Caller, typ, summary string, data map[string]any) *string {
	if data == nil {
		data = map[string]any{}
	}
	if c.SessionID != "" {
		data["sessionId"] = c.SessionID
	}
	if c.Via != "" {
		data["via"] = c.Via
	}
	if len(summary) > 300 {
		summary = TruncateUTF8(summary, 300).Text
	}
	id := s.h.Activity(actorPtr(c), typ, summary, data)
	if id == "" {
		return nil
	}
	return &id
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func tsPtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := ts(t)
	return &v
}

func strPtr(s string) *string { return &s }

func exprRefs(src string) ([]string, error) { return expr.ExprRefs(src) }

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func readBody(r *http.Request, v any, max int64) *Error {
	if r.Body == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return invalid("read body: %s", err.Error())
	}
	if int64(len(b)) > max {
		return &Error{Status: 413, Code: model.ErrTooLarge, Msg: fmt.Sprintf("the request body is larger than %d bytes", max)}
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return invalid("invalid JSON body: %s", err.Error())
	}
	return nil
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ───────────────────────── single-use tickets (MQTT streams)

type ticket struct {
	kind      string
	ref       string
	sessionID string
	via       string
	actor     string
	expires   time.Time
}

type ticketStore struct {
	mu sync.Mutex
	m  map[string]*ticket
}

func newTicketStore() *ticketStore { return &ticketStore{m: map[string]*ticket{}} }

func (t *ticketStore) issue(tk *ticket, ttl time.Duration) (string, time.Time) {
	id := randToken(24)
	tk.expires = time.Now().Add(ttl)
	t.mu.Lock()
	t.m[id] = tk
	t.mu.Unlock()
	return id, tk.expires
}

// take consumes a ticket: it must exist, be unexpired, of the kind + ref and belong to the caller (same GUI session,
// or the same forwarding site over the mesh).
func (t *ticketStore) take(id, kind, ref string, c Caller) *Error {
	t.mu.Lock()
	defer t.mu.Unlock()
	tk := t.m[id]
	if tk == nil || tk.kind != kind || tk.ref != ref {
		return errf(401, model.ErrUnauthenticated, "the stream link is not valid (tickets are single-use); open the session again")
	}
	delete(t.m, id)
	if time.Now().After(tk.expires) {
		return errf(401, model.ErrUnauthenticated, "the stream link expired; open the session again")
	}
	if c.Via != "" {
		if tk.via != c.Via {
			return errf(401, model.ErrUnauthenticated, "the stream link belongs to another session")
		}
	} else if tk.sessionID == "" || tk.sessionID != c.SessionID {
		return errf(401, model.ErrUnauthenticated, "the stream link belongs to another session")
	}
	return nil
}

func (t *ticketStore) sweep() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if time.Now().After(v.expires) {
			delete(t.m, k)
		}
	}
}

// ───────────────────────── rate limits (per session or forwarding site, sliding minute)

var rateLimits = map[string]int{"execute": 120, "reveal": 30, "test": 30, "write": 60, "mqtt-session": 20}

type rateLimiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{m: map[string][]time.Time{}} }

func (rl *rateLimiter) allow(cat string, c Caller, max int) *Error {
	key := cat + "|" + c.key()
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	var keep []time.Time
	for _, t := range rl.m[key] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= max {
		retry := 60 - int(now.Sub(keep[0]).Seconds())
		if retry < 1 {
			retry = 1
		}
		rl.m[key] = keep
		return &Error{Status: 429, Code: model.ErrRateLimited, Msg: fmt.Sprintf("too many %s requests (%d per minute); try again in %d s", cat, max, retry), Details: map[string]any{"retryAfterSec": retry}}
	}
	rl.m[key] = append(keep, now)
	return nil
}

func (s *Service) allow(cat string, c Caller) *Error {
	max := rateLimits[cat]
	switch cat {
	case "execute":
		max = s.limits().ExecutePerMinute
	case "reveal":
		max = s.limits().RevealPerMinute
	}
	return s.rl.allow(cat, c, max)
}

// ───────────────────────── running executions (cancel by queryId, maxConcurrent per site)

type runningExec struct {
	queryID string
	connID  string
	caller  Caller
	cancel  context.CancelFunc
	started time.Time
}

type runningSet struct {
	mu sync.Mutex
	m  map[string]*runningExec // queryId → exec
}

func newRunningSet() *runningSet { return &runningSet{m: map[string]*runningExec{}} }

func (rs *runningSet) count(connID string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n := 0
	for _, e := range rs.m {
		if connID == "" || e.connID == connID {
			n++
		}
	}
	return n
}

// begin registers an execution: 409 when maxConcurrent run on the site or the queryId is already running.
func (s *Service) begin(ctx context.Context, c Caller, connID, queryID string, timeout time.Duration) (context.Context, func(), *Error) {
	if strings.TrimSpace(queryID) == "" {
		queryID = randToken(12)
	}
	if len(queryID) > 64 {
		return nil, nil, invalid("queryId is at most 64 characters")
	}
	rs := s.running
	rs.mu.Lock()
	if _, dup := rs.m[queryID]; dup {
		rs.mu.Unlock()
		return nil, nil, conflict("query %s is already running", queryID)
	}
	if max := s.limits().MaxConcurrent; len(rs.m) >= max {
		rs.mu.Unlock()
		return nil, nil, &Error{Status: 409, Code: model.ErrConflict, Msg: fmt.Sprintf("%d dev-tool executions are already running on site %s (agent.json devTools.maxConcurrent); wait or cancel one", max, s.h.Site()),
			Details: map[string]any{"maxConcurrent": max}}
	}
	// the execution outlives the HTTP request only until the timeout (a closed browser tab cancels it)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	rs.m[queryID] = &runningExec{queryID: queryID, connID: connID, caller: c, cancel: cancel, started: time.Now()}
	rs.mu.Unlock()
	return cctx, func() {
		cancel()
		rs.mu.Lock()
		delete(rs.m, queryID)
		rs.mu.Unlock()
	}, nil
}

func (s *Service) timeoutFor(req *int, conn *resolvedConn) time.Duration {
	sec := s.limits().StatementTimeoutSec
	if conn != nil && conn.def.Options.TimeoutSec != nil {
		sec = int(*conn.def.Options.TimeoutSec)
	}
	if req != nil && *req > 0 {
		sec = *req
	}
	if sec < 1 {
		sec = 1
	}
	if sec > 3600 {
		sec = 3600
	}
	return time.Duration(sec) * time.Second
}
