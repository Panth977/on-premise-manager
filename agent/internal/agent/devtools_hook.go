package agent

// The dev tools (internal/devtools) mounted on the agent. SPEC §7.6, §21 #97. Hooks in shared files (marked §21 #97):
// apiHandler (handleDevtoolsRoute), buildSiteMux (devtoolsSiteHandler), Run (startDevTools), peer API (PUT on
// /_opm/peer/site/*), local proposals (buildLocalConfig copies the head presets; validate.CheckLocalEdit).

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/devtools"
	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/expr"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

var (
	devtoolsMu   sync.Mutex
	devtoolsOf   = map[*Agent]*devtools.Service{}
	devtoolsMuxs = map[*Agent]http.Handler{}
	devHeadMemo  = map[*Agent]*devHeadCache{}
)

type devHeadCache struct {
	revID string
	at    time.Time
	head  *devtools.Head
	ectx  *expr.Context
}

// DevTools returns the dev tools of the agent (created on first use).
func (a *Agent) DevTools() *devtools.Service {
	devtoolsMu.Lock()
	defer devtoolsMu.Unlock()
	if s := devtoolsOf[a]; s != nil {
		return s
	}
	s := devtools.New(&devtoolsHost{a: a})
	devtoolsOf[a] = s
	return s
}

// startDevTools creates the tables and runs the idle sweeper (hook in Run).
func (a *Agent) startDevTools() {
	a.DevTools().Start(a.ctx)
	go func() {
		<-a.ctx.Done()
		devtoolsMu.Lock()
		delete(devtoolsOf, a)
		delete(devtoolsMuxs, a)
		delete(devHeadMemo, a)
		devtoolsMu.Unlock()
	}()
}

// devtoolsSiteHandler serves /devtools/* of THIS site (local requests and /_opm/peer/site/devtools/*).
func (a *Agent) devtoolsSiteHandler() http.Handler {
	devtoolsMu.Lock()
	if h := devtoolsMuxs[a]; h != nil {
		devtoolsMu.Unlock()
		return h
	}
	devtoolsMu.Unlock()
	svc := a.DevTools()
	mux := http.NewServeMux()
	for _, rt := range svc.Routes() {
		fn := rt.Fn
		kind := authSession
		if rt.Auth == devtools.AuthActor {
			kind = authActor
		}
		mux.HandleFunc(rt.Method+" "+rt.Pattern, a.withAuth(kind, func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
			if rc := getRC(r); rc.peer != nil && (rc.body != nil || rc.bodyFile != "") {
				if rd, err := rc.reader(); err == nil {
					defer rd.Close()
					r.Body = rd
				}
			}
			v, st, err := fn(w, r, a.devtoolsCaller(r))
			if err != nil {
				return nil, 0, devtoolsErr(err)
			}
			return v, st, nil
		}))
	}
	mux.HandleFunc("/devtools/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, herr(model.ErrNotFound, "no such dev tools endpoint: "+r.Method+" "+r.URL.Path, nil))
	})
	devtoolsMu.Lock()
	devtoolsMuxs[a] = mux
	devtoolsMu.Unlock()
	return mux
}

func devtoolsErr(err error) *httpError {
	de, ok := err.(*devtools.Error)
	if !ok {
		return toHTTPErr(err)
	}
	e := herr(de.Code, de.Msg, de.Details)
	if de.Code == model.ErrRateLimited {
		if s, ok := de.Details["retryAfterSec"]; ok {
			e.header = map[string]string{"Retry-After": fmt.Sprint(s)}
		}
	}
	return e
}

func (a *Agent) devtoolsCaller(r *http.Request) devtools.Caller {
	rc := getRC(r)
	c := devtools.Caller{Actor: rc.actor, SessionID: rc.sessionID, IP: rc.ip}
	if rc.peer != nil {
		c.Via = rc.peer.Site
	}
	return c
}

// handleDevtoolsRoute dispatches /_opm/api/sites/{siteSlug}/devtools/{rest...}: self → local; "*" → 422 (a connection
// lives on one site); another site → forwarded over the mesh (writes need the actor and are logged here too as
// devtools.action-forwarded; the MQTT SSE stream is copied with flushes).
func (a *Agent) handleDevtoolsRoute(w http.ResponseWriter, r *http.Request) {
	slug, rest := r.PathValue("siteSlug"), r.PathValue("rest")
	rc := getRC(r)
	if slug == a.site {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/devtools/" + rest
		r2.URL.RawPath = ""
		a.siteMux().ServeHTTP(w, r2)
		return
	}
	if e := a.checkSession(r, rc); e != nil {
		writeErr(w, e)
		return
	}
	if slug == "*" {
		writeErr(w, herr(model.ErrInvalid, `"*" is not supported for dev tools: a connection lives on one site — pick the site`, nil))
		return
	}
	if !contains(a.enrolledSites(), slug) {
		cfg, _ := a.headConfig()
		if siteOf(cfg, slug) == nil {
			writeErr(w, herr(model.ErrNotFound, "site "+slug+" does not exist", nil))
			return
		}
	}
	if !a.reachable(slug) {
		writeErr(w, herr(model.ErrUnavailable, "site "+slug+" is not reachable", nil))
		return
	}
	if strings.HasSuffix(rest, "/reveal") {
		w.Header().Set("Cache-Control", "no-store")
	}
	switch {
	case r.Method == http.MethodGet && devtools.Streaming(rest):
		a.forwardStream(w, r, slug, "devtools/"+rest)
	case r.Method == http.MethodGet:
		a.forwardSite(w, r, slug, "devtools/"+rest)
	default:
		if e := a.checkActor(r, rc); e != nil {
			writeErr(w, e)
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		a.forwardSite(sw, r, slug, "devtools/"+rest)
		action := rest
		if i := strings.Index(action, "?"); i >= 0 {
			action = action[:i]
		}
		var connID any
		if p := strings.Split(action, "/"); len(p) >= 2 && p[0] == "connections" && p[1] != "test" {
			connID = p[1]
		}
		a.activity(rc.actorPtr(), "agent", "devtools.action-forwarded", trim200(fmt.Sprintf("dev tools: %s %s forwarded to %s → %d", r.Method, action, slug, sw.status)),
			actorData(rc, map[string]any{"siteSlug": slug, "forwardedTo": slug, "method": r.Method, "action": action, "connectionId": connID, "status": sw.status}))
	}
}

// ───────────────────────── devtools.Host

type devtoolsHost struct{ a *Agent }

func (h *devtoolsHost) Site() string           { return h.a.site }
func (h *devtoolsHost) DataDir() string        { return h.a.dataDir }
func (h *devtoolsHost) ConfigPath() string     { return h.a.opt.ConfigPath }
func (h *devtoolsHost) DB() *sql.DB            { return h.a.db.DB }
func (h *devtoolsHost) Docker() *docker.Client { return h.a.dc }
func (h *devtoolsHost) Log() *dlog.Logger      { return h.a.log }
func (h *devtoolsHost) SecretValues() []string { return (&toolsHost{a: h.a}).SecretValues() }

func (h *devtoolsHost) Activity(actor *string, typ, summary string, data map[string]any) string {
	if act := h.a.activity(actor, "agent", typ, summary, data); act != nil {
		return act.ActivityID
	}
	return ""
}

func (h *devtoolsHost) SessionActor(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	var actor sql.NullString
	_ = h.a.db.QueryRow(`SELECT actor FROM activities WHERE type = 'actor.declared' AND json_extract(data, '$.sessionId') = ? ORDER BY seq DESC LIMIT 1`, sessionID).Scan(&actor)
	return actor.String
}

// cached returns the head view + the expression context of the head revision (rebuilt per revision, every 10 s for
// the container states).
func (h *devtoolsHost) cached() *devHeadCache {
	hr := h.a.headRevision()
	if hr == nil {
		return nil
	}
	devtoolsMu.Lock()
	c := devHeadMemo[h.a]
	devtoolsMu.Unlock()
	if c != nil && c.revID == hr.RevID && time.Since(c.at) < 10*time.Second {
		return c
	}
	cfg := revConfig(hr)
	site := siteOf(cfg, h.a.site)
	head := &devtools.Head{Seq: hr.Seq, RevID: hr.RevID, Presets: []model.DevToolPreset{}, Secrets: []string{}, Variables: []string{}}
	for _, s := range cfg.Secrets {
		head.Secrets = append(head.Secrets, s.SecretSlug)
	}
	for _, v := range cfg.Variables {
		head.Variables = append(head.Variables, v.VariableSlug)
	}
	if rs := siteOf(&hr.Config, h.a.site); rs != nil {
		head.Presets = rs.Config.DevToolList()
	}
	th := &toolsHost{a: h.a}
	vols := th.Volumes()
	for _, v := range vols {
		head.Volumes = append(head.Volumes, devtools.HeadVolume{Slug: v.Slug, Label: v.Label, Kind: v.Kind, Path: v.Path})
	}
	_, builts := th.builts()
	bmap := map[string]*driver.Built{}
	for _, b := range builts {
		bmap[b.ContainerSlug] = b
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	existing, _ := h.a.drv.Existing(ctx)
	cancel()
	if site != nil {
		for _, ct := range site.Config.Containers {
			hc := devtools.HeadContainer{ContainerSlug: ct.ContainerSlug, ServiceSlug: ct.ServiceSlug, Image: ct.ServiceSlug + ":" + ct.Version, Enabled: ct.Enabled}
			if ex, ok := existing[ct.ContainerSlug]; ok {
				hc.DockerID, hc.DockerName, hc.Running = ex.ID, ex.Name, ex.Running
			}
			if b := bmap[ct.ContainerSlug]; b != nil {
				hc.Image, hc.Network = b.Readable, b.Spec.Network
				for _, p := range b.Ports {
					hc.Ports = append(hc.Ports, devtools.HeadPort{Name: p.Name, ContainerPort: p.ContainerPort, Protocol: p.Protocol, HostPort: p.HostPort, HostIP: p.HostIP})
				}
				for _, m := range b.Spec.Mounts {
					hm := devtools.HeadMount{Target: m.Target, Source: m.Source, ReadOnly: m.ReadOnly, Kind: "host"}
					switch {
					case m.Sha256 != nil:
						hm.Kind = "file"
					case strings.HasPrefix(m.Source, filepath.Join(h.a.dataDir, "secrets")+"/"):
						hm.Kind = "secret"
					}
					for _, v := range vols {
						if v.Path != "" && (m.Source == v.Path || strings.HasPrefix(m.Source, strings.TrimRight(v.Path, "/")+"/")) {
							slug := v.Slug
							hm.Kind, hm.VolumeSlug = "volume", &slug
						}
					}
					hc.Mounts = append(hc.Mounts, hm)
				}
			}
			head.Containers = append(head.Containers, hc)
		}
	}
	vctx := h.a.validationContext(&hr.Catalog)
	sm := driver.SiteMapFor(cfg, &hr.Catalog, h.a.dep, h.a.siteHosts(cfg))
	ectx := validate.SiteDevtoolExprContext(cfg, vctx.Services, vctx.Images, h.a.dep, nil, h.a.site, sm) // §21 #99: + containers.<c>.env
	for i := range head.Containers {
		hc := &head.Containers[i]
		peer := ectx.Containers[hc.ContainerSlug]
		names := make([]string, 0, len(peer.Env))
		for n := range peer.Env {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			hc.Env = append(hc.Env, devtools.HeadEnv{Name: n, Secret: peer.Env[n].Secret})
		}
	}
	c = &devHeadCache{revID: hr.RevID, at: time.Now(), head: head, ectx: ectx}
	devtoolsMu.Lock()
	devHeadMemo[h.a] = c
	devtoolsMu.Unlock()
	return c
}

func (h *devtoolsHost) Head() *devtools.Head {
	if c := h.cached(); c != nil {
		return c.head
	}
	return nil
}

func (h *devtoolsHost) ResolveExpr(src string) (*expr.Result, error) {
	c := h.cached()
	if c == nil {
		return nil, fmt.Errorf("no head revision")
	}
	return expr.Resolve(src, c.ectx, validate.DevToolExprField)
}
