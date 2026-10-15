// Package tools is the agent's operator toolbox (SPEC-final §7.4, §21 #89): container logs (SSE), the agent + edge
// access logs, web terminals (container exec and host shell over a WebSocket, with local transcripts), monitoring
// (internal/metrics), live container actions + inspect, a read-only volumes browser, network checks and Docker
// housekeeping. It is mounted by the agent under /_opm/api/sites/{site}/tools/* (and /_opm/peer/site/tools/* for
// requests forwarded over the mesh); auth (session cookie / actor token / signed peer) is checked by the agent before
// a handler runs, the handlers here add the tool switches, tickets, rate limits and activities.
package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/metrics"
)

// Caller is who sends a request (filled by the agent from its request context).
type Caller struct {
	Actor     string // declared actor (empty for session-only reads)
	SessionID string // GUI session id, or "<sid>@<site>" when forwarded by a peer
	Via       string // the forwarding site when the request came over the mesh ("" = local)
	IP        string
}

// Target is an OPM container of this site as the toolbox sees it.
type Target struct {
	Ref           string // containerSlug, or the docker name of a kept failed copy
	ContainerSlug string
	ServiceSlug   string
	Name          string
	ID            string // "" = missing
	Image         string
	FailedCopy    bool
	Enabled       bool // enabled in the head config (expected running)
	Kind          string
	Health        string
	Ports         []Port
	SecretValues  []string // secret-tainted values of this container (masking)
	SecretEnv     []string // env names whose value comes from a secret
}

// Port is a published port.
type Port struct {
	ContainerPort int
	HostPort      *int
	Protocol      string
}

// Volume is a declared site volume.
type Volume struct {
	Slug   string
	Label  string
	Kind   string // dir | file
	Path   string
	Backup bool
	UsedBy []string
}

// Check is one OPM health check result.
type Check struct {
	At      time.Time
	OK      bool
	Message string
}

// Host is what the toolbox needs from the agent.
type Host interface {
	Site() string
	DataDir() string
	ConfigPath() string
	Docker() *docker.Client
	Log() *dlog.Logger
	// Targets lists the OPM containers of this site (head config + kept failed copies).
	Targets(ctx context.Context) ([]Target, error)
	Activity(actor *string, typ, summary string, data map[string]any)
	SecretValues() []string
	Volumes() []Volume
	// HeadImages: image references (tags) the head revision of this site runs (never pruned).
	HeadImages() map[string]bool
	HeadSeq() int
	UpdateRunning() bool
	// VolumeBusy: why a volume cannot change now (a volume action holds it), "" = free (§21 #96)
	VolumeBusy(volumeSlug string) string
	Checks(containerSlug string) []Check
	CallerOf(r *http.Request) Caller
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
func notFound(format string, a ...any) *Error { return errf(404, "not-found", format, a...) }
func invalid(format string, a ...any) *Error  { return errf(422, "invalid", format, a...) }
func unavailable(err error) *Error            { return errf(503, "unavailable", "docker: %s", err.Error()) }

// AuthKind of a route (checked by the agent wrapper).
type AuthKind int

const (
	AuthSession AuthKind = iota
	AuthActor
)

// Handler returns a JSON value (nil = the handler wrote the response itself), the status, or an error.
type Handler func(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error)

// Route is one endpoint under /tools/ (pattern without the method).
type Route struct {
	Method  string
	Pattern string // e.g. /tools/containers/{ref}/restart
	Auth    AuthKind
	Fn      Handler
}

// Service is the toolbox of one site.
type Service struct {
	h       Host
	cfg     Config
	term    *termManager
	tickets *ticketStore
	rl      *rateLimiter
	stops   *manualStops
	Metrics *metrics.Monitor
	clock   func() time.Time
	mu      sync.Mutex
	dfCache struct {
		at  time.Time
		val *docker.DiskUsage
	}
}

// New creates the toolbox (Start runs its loops).
func New(h Host) *Service {
	s := &Service{h: h, cfg: LoadConfig(h.ConfigPath()), tickets: newTicketStore(), rl: newRateLimiter(), clock: time.Now}
	s.stops = loadManualStops(filepath.Join(h.DataDir(), "tools", "manual-stops.json"))
	s.term = newTermManager(s)
	s.Metrics = metrics.New(filepath.Join(h.DataDir(), "metrics.db"), h.DataDir(), h.Docker(), s.metricTargets, h.Log())
	return s
}

// Config returns the effective tools config.
func (s *Service) Config() Config { return s.cfg }

// Start runs the sampler (15 s) and the transcript cleanup (hourly) until ctx ends.
func (s *Service) Start(ctx context.Context) {
	_ = os.MkdirAll(filepath.Join(s.h.DataDir(), "tools"), 0o700)
	go s.Metrics.Run(ctx)
	go func() {
		defer s.h.Log().Recover("tools", "cleanup")
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		s.term.cleanupTranscripts()
		for {
			select {
			case <-ctx.Done():
				s.term.closeAll("agent stopping")
				return
			case <-t.C:
				s.term.cleanupTranscripts()
				s.tickets.sweep()
			}
		}
	}()
}

func (s *Service) metricTargets(ctx context.Context) []metrics.Target {
	ts, err := s.h.Targets(ctx)
	if err != nil {
		return nil
	}
	var out []metrics.Target
	for _, t := range ts {
		if t.ID == "" || t.FailedCopy {
			continue
		}
		out = append(out, metrics.Target{Ref: t.Ref, ID: t.ID})
	}
	return out
}

// Routes lists every endpoint (paths relative to /_opm/api/sites/{site}).
func (s *Service) Routes() []Route {
	r := func(m, p string, a AuthKind, fn Handler) Route {
		return Route{Method: m, Pattern: "/tools" + p, Auth: a, Fn: fn}
	}
	return []Route{
		r("GET", "/info", AuthSession, s.handleInfo),
		// containers
		r("GET", "/containers", AuthSession, s.handleContainers),
		r("POST", "/containers/{ref}/restart", AuthActor, s.handleAction("restart")),
		r("POST", "/containers/{ref}/stop", AuthActor, s.handleAction("stop")),
		r("POST", "/containers/{ref}/start", AuthActor, s.handleAction("start")),
		r("GET", "/containers/{ref}/inspect", AuthSession, s.handleInspect),
		r("GET", "/containers/{ref}/top", AuthSession, s.handleTop),
		r("GET", "/containers/{ref}/events", AuthSession, s.handleEvents),
		// logs
		r("GET", "/logs/containers", AuthSession, s.handleLogStream),
		r("GET", "/logs/containers/download", AuthSession, s.handleLogDownload),
		r("GET", "/logs/agent", AuthSession, s.handleAgentLog),
		r("GET", "/logs/access", AuthSession, s.handleAccessLog),
		// terminal
		r("POST", "/terminal/sessions", AuthActor, s.term.handleCreate),
		r("GET", "/terminal/sessions", AuthSession, s.term.handleList),
		r("POST", "/terminal/sessions/{sessionId}/close", AuthActor, s.term.handleClose),
		r("GET", "/terminal/ws", AuthSession, s.term.handleWS),
		r("GET", "/terminal/transcripts", AuthSession, s.term.handleTranscripts),
		r("POST", "/terminal/transcripts/{id}/ticket", AuthActor, s.term.handleTranscriptTicket),
		r("GET", "/terminal/transcripts/{id}/download", AuthSession, s.term.handleTranscriptDownload),
		// monitoring
		r("GET", "/metrics/summary", AuthSession, s.handleMetricsSummary),
		r("GET", "/metrics/series", AuthSession, s.handleMetricsSeries),
		// volumes
		r("GET", "/volumes", AuthSession, s.handleVolumes),
		r("GET", "/volumes/{volumeSlug}/tree", AuthSession, s.handleVolumeTree),
		r("GET", "/volumes/{volumeSlug}/file", AuthSession, s.handleVolumeFile),
		r("POST", "/volumes/{volumeSlug}/download", AuthActor, s.handleVolumeTicket),
		r("GET", "/volumes/{volumeSlug}/download", AuthSession, s.handleVolumeDownload),
		// §21 #96 volume manager: file operations (volumes_write.go)
		r("POST", "/volumes/{volumeSlug}/file", AuthActor, s.handleVolumeWrite),
		r("POST", "/volumes/{volumeSlug}/upload", AuthActor, s.handleVolumeUpload),
		r("POST", "/volumes/{volumeSlug}/mkdir", AuthActor, s.handleVolumeMkdir),
		r("POST", "/volumes/{volumeSlug}/move", AuthActor, s.handleVolumeMove),
		r("POST", "/volumes/{volumeSlug}/delete", AuthActor, s.handleVolumeDelete),
		// network
		r("POST", "/netcheck", AuthActor, s.handleNetcheck),
		// docker
		r("GET", "/docker/images", AuthSession, s.handleImages),
		r("POST", "/docker/prune-images", AuthActor, s.handlePrune),
	}
}

// Merged reports whether "*" merges a GET endpoint (rest = path after /tools/).
func Merged(rest string) bool {
	switch rest {
	case "info", "containers", "metrics/summary", "docker/images", "volumes", "terminal/sessions", "terminal/transcripts":
		return true
	}
	return false
}

// Streaming reports whether a GET endpoint streams (SSE): the agent copies it with flushes when forwarding.
func Streaming(rest string) bool {
	switch rest {
	case "logs/containers", "logs/agent", "logs/access":
		return true
	}
	return false
}

func actorPtr(c Caller) *string {
	if c.Actor == "" {
		return nil
	}
	a := c.Actor
	return &a
}

func (s *Service) activity(c Caller, typ, summary string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if c.SessionID != "" {
		data["sessionId"] = c.SessionID
	}
	if c.Via != "" {
		data["via"] = c.Via
	}
	s.h.Activity(actorPtr(c), typ, summary, data)
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func tsPtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := ts(t)
	return &v
}

func readBody(r *http.Request, v any) *Error {
	if r.Body == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return invalid("read body: %s", err.Error())
	}
	if len(bytes.TrimSpace(b)) == 0 {
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

// ───────────────────────── single-use tickets (WebSocket, downloads)

type ticket struct {
	kind      string // terminal | transcript | volume
	ref       string // sessionId | transcript id | volumeSlug
	extra     map[string]string
	sessionID string // GUI session (local)
	via       string // forwarding site (mesh)
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

// take consumes a ticket: it must exist, be unexpired, of the kind and belong to the caller (same GUI session, or the
// same forwarding site when it came over the mesh).
func (t *ticketStore) take(id, kind string, c Caller) (*ticket, *Error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tk := t.m[id]
	if tk == nil || tk.kind != kind {
		return nil, errf(401, "unauthenticated", "the link is not valid (tickets are single-use); start again")
	}
	delete(t.m, id)
	if time.Now().After(tk.expires) {
		return nil, errf(401, "unauthenticated", "the link expired; start again")
	}
	if c.Via != "" {
		if tk.via != c.Via {
			return nil, errf(401, "unauthenticated", "the link belongs to another session")
		}
	} else if tk.sessionID == "" || tk.sessionID != c.SessionID {
		return nil, errf(401, "unauthenticated", "the link belongs to another session")
	}
	return tk, nil
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

var rateLimits = map[string]int{"terminal": 10, "action": 20, "netcheck": 30, "download": 20, "prune": 2, "bundle": 5}

type rateLimiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{m: map[string][]time.Time{}} }

func (rl *rateLimiter) allow(cat string, c Caller) *Error {
	key := cat + "|" + c.SessionID + "|" + c.Via
	max := rateLimits[cat]
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
		return &Error{Status: 429, Code: "rate-limited", Msg: fmt.Sprintf("too many %s requests (%d per minute); try again in %d s", cat, max, retry), Details: map[string]any{"retryAfterSec": retry}}
	}
	rl.m[key] = append(keep, now)
	return nil
}

// IssueTicket issues a single-use ticket bound to the caller (the agent's support bundle reuses the tools tickets,
// SPEC §21 #90).
func (s *Service) IssueTicket(kind, ref string, c Caller, ttl time.Duration) (string, time.Time) {
	return s.tickets.issue(&ticket{kind: kind, ref: ref, sessionID: c.SessionID, via: c.Via, actor: c.Actor}, ttl)
}

// TakeTicket consumes a ticket of the kind; it returns the ref and the actor it was issued to.
func (s *Service) TakeTicket(id, kind string, c Caller) (ref, actor string, err error) {
	tk, e := s.tickets.take(id, kind, c)
	if e != nil {
		return "", "", e
	}
	return tk.ref, tk.actor, nil
}

// Allow applies a rate-limit category (per session / forwarding site, sliding minute).
func (s *Service) Allow(cat string, c Caller) error {
	if e := s.rl.allow(cat, c); e != nil {
		return e
	}
	return nil
}
