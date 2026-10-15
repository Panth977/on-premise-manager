package agent

// The operator toolbox (internal/tools, internal/metrics, internal/tools/accesslog) mounted on the agent. SPEC §7.4,
// §21 #89. Hooks in shared files: apiHandler (handleToolsRoute), buildSiteMux (toolsSiteHandler), Run (startTools),
// reloadEdgeQ (withAccessLog).

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/edge"
	"opm/agent/internal/model"
	"opm/agent/internal/tools"
	"opm/agent/internal/tools/accesslog"
)

var (
	toolsMu   sync.Mutex
	toolsOf   = map[*Agent]*tools.Service{}
	toolsMuxs = map[*Agent]http.Handler{}
	builtMemo = map[*Agent]*builtCache{}
)

type builtCache struct {
	revID  string
	at     time.Time
	builts []*driver.Built
}

// Tools returns the toolbox of the agent (created on first use).
func (a *Agent) Tools() *tools.Service {
	toolsMu.Lock()
	defer toolsMu.Unlock()
	if s := toolsOf[a]; s != nil {
		return s
	}
	s := tools.New(&toolsHost{a: a})
	toolsOf[a] = s
	return s
}

// startTools runs the sampler and the transcript cleanup (hook in Run).
func (a *Agent) startTools() {
	a.Tools().Start(a.ctx)
	go func() {
		<-a.ctx.Done()
		toolsMu.Lock()
		delete(toolsOf, a)
		delete(toolsMuxs, a)
		delete(builtMemo, a)
		toolsMu.Unlock()
	}()
}

// withAccessLog adds the edge access log middleware to every edge server (hook in reloadEdgeQ).
func (a *Agent) withAccessLog(p *edge.Partial) *edge.Partial {
	if p != nil {
		accesslog.AddTo(p.Servers, a.dataDir+"/logs")
	}
	return p
}

// toolsSiteHandler serves /tools/* of THIS site (local requests and /_opm/peer/site/tools/*).
func (a *Agent) toolsSiteHandler() http.Handler {
	toolsMu.Lock()
	if h := toolsMuxs[a]; h != nil {
		toolsMu.Unlock()
		return h
	}
	toolsMu.Unlock()
	svc := a.Tools()
	mux := http.NewServeMux()
	for _, rt := range svc.Routes() {
		fn := rt.Fn
		kind := authSession
		if rt.Auth == tools.AuthActor {
			kind = authActor
		}
		mux.HandleFunc(rt.Method+" "+rt.Pattern, a.withAuth(kind, func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
			// a forwarded body (signed peer request) was spooled by the peer API: hand it to the tool (§21 #96 uploads)
			if rc := getRC(r); rc.peer != nil && (rc.body != nil || rc.bodyFile != "") {
				if rd, err := rc.reader(); err == nil {
					defer rd.Close()
					r.Body = rd
				}
			}
			v, st, err := fn(w, r, a.toolsCaller(r))
			if err != nil {
				return nil, 0, toolsErr(err)
			}
			return v, st, nil
		}))
	}
	mux.HandleFunc("/tools/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, herr(model.ErrNotFound, "no such tools endpoint: "+r.Method+" "+r.URL.Path, nil))
	})
	toolsMu.Lock()
	toolsMuxs[a] = mux
	toolsMu.Unlock()
	return mux
}

func toolsErr(err error) *httpError {
	te, ok := err.(*tools.Error)
	if !ok {
		return toHTTPErr(err)
	}
	e := herr(te.Code, te.Msg, te.Details)
	if te.Code == model.ErrRateLimited {
		if s, ok := te.Details["retryAfterSec"]; ok {
			e.header = map[string]string{"Retry-After": fmt.Sprint(s)}
		}
	}
	return e
}

func (a *Agent) toolsCaller(r *http.Request) tools.Caller {
	rc := getRC(r)
	c := tools.Caller{Actor: rc.actor, SessionID: rc.sessionID, IP: rc.ip}
	if rc.peer != nil {
		c.Via = rc.peer.Site
	}
	return c
}

// handleToolsRoute dispatches /_opm/api/sites/{siteSlug}/tools/{rest...}: self → local; "*" → merged GET; another site
// → forwarded over the mesh (JSON as the other per-site routes; SSE copied with flushes; the terminal WebSocket bridged).
func (a *Agent) handleToolsRoute(w http.ResponseWriter, r *http.Request) {
	slug, rest := r.PathValue("siteSlug"), r.PathValue("rest")
	rc := getRC(r)
	if slug == a.site {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/tools/" + rest
		r2.URL.RawPath = ""
		a.siteMux().ServeHTTP(w, r2)
		return
	}
	if e := a.checkSession(r, rc); e != nil {
		writeErr(w, e)
		return
	}
	if slug == "*" {
		if r.Method != http.MethodGet || !tools.Merged(rest) {
			writeErr(w, herr(model.ErrInvalid, `"*" is supported for GET tools/info, containers, metrics/summary, docker/images, volumes, terminal/sessions and terminal/transcripts`, nil))
			return
		}
		a.mergeTools(w, r, rest)
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
	switch {
	case rest == "terminal/ws":
		a.bridgeTerminal(w, r, slug)
	case r.Method == http.MethodGet && tools.Streaming(rest):
		a.forwardStream(w, r, slug, "tools/"+rest)
	default:
		if r.Method != http.MethodGet {
			if e := a.checkActor(r, rc); e != nil {
				writeErr(w, e)
				return
			}
			if strings.HasPrefix(rest, "volumes/") && !strings.HasSuffix(rest, "/download") { // §21 #96 file writes: logged here too
				a.forwardVolumeAction(w, r, slug, "tools/"+rest)
				return
			}
		}
		a.forwardSite(w, r, slug, "tools/"+rest)
	}
}

// mergeTools answers "*" with one list over the reachable sites.
func (a *Agent) mergeTools(w http.ResponseWriter, r *http.Request, rest string) {
	path := "tools/" + rest
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	sites := a.allSites()
	type res struct {
		items []json.RawMessage
		one   json.RawMessage
	}
	out := make([]res, len(sites))
	var wg sync.WaitGroup
	for i, s := range sites {
		if s != a.site && !a.reachable(s) {
			continue
		}
		wg.Add(1)
		go func(i int, s string) {
			defer wg.Done()
			var raw json.RawMessage
			if err := a.fetchSiteJSON(r.Context(), r, s, path, &raw); err != nil {
				return
			}
			var lr struct {
				Items []json.RawMessage `json:"items"`
			}
			if json.Unmarshal(raw, &lr) == nil && lr.Items != nil {
				out[i].items = lr.Items
			} else {
				out[i].one = raw
			}
		}(i, s)
	}
	wg.Wait()
	items := []json.RawMessage{}
	for _, o := range out {
		switch rest {
		case "info", "metrics/summary", "docker/images": // one object per site
			if o.one != nil {
				items = append(items, o.one)
			}
		default:
			items = append(items, o.items...)
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// forwardStream proxies an SSE GET from a peer, flushing as data arrives.
func (a *Agent) forwardStream(w http.ResponseWriter, r *http.Request, slug, rest string) {
	path := "/_opm/peer/site/" + rest
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	resp, done, err := a.peerStream(r.Context(), slug, "GET", path, nil, nil, 6*time.Hour+time.Minute)
	if err != nil {
		writeErr(w, herr(model.ErrUnavailable, "site "+slug+": "+err.Error(), nil))
		return
	}
	defer done()
	for _, k := range []string{"Content-Type", "Cache-Control", "X-Accel-Buffering"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	rcw := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rcw.Flush()
		}
		if err != nil {
			return
		}
	}
}

// bridgeTerminal connects the browser's WebSocket to the terminal on another site: a signed upgrade request to the
// peer (the single-use ticket was issued there by the forwarded createTerminal), then raw frames both ways. Both sites
// write terminal.opened / terminal.closed (this one with forwardedTo).
func (a *Agent) bridgeTerminal(w http.ResponseWriter, r *http.Request, slug string) {
	rc := getRC(r)
	if !tools.SameOrigin(r) {
		writeErr(w, herr(model.ErrForbidden, "cross-origin WebSocket refused", nil))
		return
	}
	if !tools.IsWebSocket(r) || r.Header.Get("Sec-WebSocket-Key") == "" {
		writeErr(w, herr(model.ErrInvalid, "this endpoint needs a WebSocket upgrade", nil))
		return
	}
	kb := make([]byte, 16)
	_, _ = rand.Read(kb)
	hdr := map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Key": base64.StdEncoding.EncodeToString(kb), "X-OPM-Session": rc.sessionID + "@" + a.site}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, done, err := a.peerStream(ctx, slug, "GET", "/_opm/peer/site/tools/terminal/ws?"+r.URL.RawQuery, nil, hdr, 24*time.Hour)
	if err != nil {
		writeErr(w, herr(model.ErrUnavailable, "site "+slug+": "+err.Error(), nil))
		return
	}
	defer done()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		for _, k := range []string{"Content-Type"} {
			w.Header().Set(k, resp.Header.Get(k))
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 64*1024))
		return
	}
	peer, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		writeErr(w, herr(model.ErrUnavailable, "site "+slug+": the WebSocket cannot be bridged", nil))
		return
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		peer.Close()
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Time{})
	_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + tools.WSAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n"))
	actor := resp.Header.Get("X-OPM-Terminal-Actor")
	var ap *string
	if actor != "" {
		ap = &actor
	}
	target, tid, kind := resp.Header.Get("X-OPM-Terminal-Target"), resp.Header.Get("X-OPM-Terminal-Id"), resp.Header.Get("X-OPM-Terminal-Kind")
	start := time.Now()
	a.activity(ap, "agent", "terminal.opened", fmt.Sprintf("terminal opened on site %s: %s (forwarded from %s)", slug, target, a.site),
		actorData(rc, map[string]any{"terminalId": tid, "kind": kind, "target": target, "forwardedTo": slug, "siteSlug": slug}))
	var br io.Reader = conn
	if brw != nil && brw.Reader.Buffered() > 0 {
		br = io.MultiReader(io.LimitReader(brw.Reader, int64(brw.Reader.Buffered())), conn)
	}
	var once sync.Once
	stop := func() { once.Do(func() { peer.Close(); conn.Close() }) }
	go func() { _, _ = io.Copy(peer, br); stop() }()
	_, _ = io.Copy(conn, bufio.NewReader(peer))
	stop()
	dur := int64(time.Since(start).Seconds())
	a.activity(ap, "agent", "terminal.closed", fmt.Sprintf("terminal closed on site %s: %s (%ds, forwarded from %s)", slug, target, dur, a.site),
		actorData(rc, map[string]any{"terminalId": tid, "kind": kind, "target": target, "forwardedTo": slug, "siteSlug": slug, "durationSec": dur}))
}

// ───────────────────────── tools.Host

type toolsHost struct{ a *Agent }

func (h *toolsHost) Site() string                  { return h.a.site }
func (h *toolsHost) DataDir() string               { return h.a.dataDir }
func (h *toolsHost) ConfigPath() string            { return h.a.opt.ConfigPath }
func (h *toolsHost) Docker() *docker.Client        { return h.a.dc }
func (h *toolsHost) Log() *dlog.Logger             { return h.a.log }
func (h *toolsHost) UpdateRunning() bool           { return h.a.lock() != nil }
func (h *toolsHost) VolumeBusy(slug string) string { return h.a.volumeBusy(slug) }
func (h *toolsHost) CallerOf(r *http.Request) tools.Caller {
	return h.a.toolsCaller(r)
}

func (h *toolsHost) HeadSeq() int {
	if hr := h.a.headRevision(); hr != nil {
		return hr.Seq
	}
	return 0
}

func (h *toolsHost) Activity(actor *string, typ, summary string, data map[string]any) {
	h.a.activity(actor, "agent", typ, summary, data)
}

// builts of the head revision on this site (cached per revision for 30 s).
func (h *toolsHost) builts() (*model.Revision, []*driver.Built) {
	hr := h.a.headRevision()
	if hr == nil {
		return nil, nil
	}
	site := siteOf(&hr.Config, h.a.site)
	if site == nil {
		return hr, nil
	}
	toolsMu.Lock()
	bc := builtMemo[h.a]
	toolsMu.Unlock()
	if bc != nil && bc.revID == hr.RevID && time.Since(bc.at) < 30*time.Second {
		return hr, bc.builts
	}
	bs, err := h.a.buildSite(hr, site)
	if err != nil {
		return hr, nil
	}
	toolsMu.Lock()
	builtMemo[h.a] = &builtCache{revID: hr.RevID, at: time.Now(), builts: bs}
	toolsMu.Unlock()
	return hr, bs
}

func (h *toolsHost) Targets(ctx context.Context) ([]tools.Target, error) {
	existing, err := h.a.drv.Existing(ctx)
	if err != nil {
		return nil, err
	}
	hr, bs := h.builts()
	bmap := map[string]*driver.Built{}
	for _, b := range bs {
		bmap[b.ContainerSlug] = b
	}
	out := []tools.Target{}
	seen := map[string]bool{}
	if hr != nil {
		if site := siteOf(&hr.Config, h.a.site); site != nil {
			for _, c := range site.Config.Containers {
				seen[c.ContainerSlug] = true
				t := tools.Target{Ref: c.ContainerSlug, ContainerSlug: c.ContainerSlug, ServiceSlug: c.ServiceSlug, Image: c.ServiceSlug + ":" + c.Version,
					Enabled: c.Enabled, Kind: "service", Health: h.a.drv.HealthOf(c.ContainerSlug).Health}
				if b := bmap[c.ContainerSlug]; b != nil {
					t.Kind = b.Kind
					t.Image = b.Readable
					t.SecretValues = b.Secrets
					sec := map[string]bool{}
					for _, v := range b.Secrets {
						sec[v] = true
					}
					for _, kv := range b.Spec.Env {
						k, v, _ := strings.Cut(kv, "=")
						if v != "" && sec[v] {
							t.SecretEnv = append(t.SecretEnv, k)
						}
					}
					for _, p := range b.Ports {
						t.Ports = append(t.Ports, tools.Port{ContainerPort: p.ContainerPort, HostPort: p.HostPort, Protocol: p.Protocol})
					}
				}
				if ex, ok := existing[c.ContainerSlug]; ok {
					t.ID, t.Name = ex.ID, ex.Name
				}
				out = append(out, t)
			}
		}
	}
	for slug, ex := range existing {
		if seen[slug] {
			continue
		}
		out = append(out, tools.Target{Ref: slug, ContainerSlug: slug, ServiceSlug: ex.Service, Name: ex.Name, ID: ex.ID, Kind: "service", Health: "none"})
	}
	if failed, err := h.a.drv.FailedContainers(ctx); err == nil {
		for id, name := range failed {
			slug := name
			if i := strings.LastIndex(name, "-failed-"); i > 0 {
				slug = name[:i]
			}
			out = append(out, tools.Target{Ref: name, ContainerSlug: slug, Name: name, ID: id, FailedCopy: true, Kind: "service", Health: "none"})
		}
	}
	return out, nil
}

func (h *toolsHost) SecretValues() []string {
	var out []string
	hr, bs := h.builts()
	if hr != nil {
		for _, s := range hr.Config.Secrets {
			if s.Value != nil && *s.Value != "" {
				out = append(out, *s.Value)
			}
		}
	}
	for _, b := range bs {
		out = append(out, b.Secrets...)
	}
	return out
}

func (h *toolsHost) Volumes() []tools.Volume {
	vr := h.a.siteVolumesResponse()
	out := []tools.Volume{}
	for _, v := range vr.Volumes {
		var used []string
		for _, u := range v.UsedBy {
			used = append(used, u.ContainerSlug)
		}
		out = append(out, tools.Volume{Slug: v.VolumeSlug, Label: v.Label, Kind: v.Kind, Path: v.Path, Backup: v.Backup, UsedBy: used})
	}
	return out
}

func (h *toolsHost) HeadImages() map[string]bool {
	out := map[string]bool{}
	_, bs := h.builts()
	for _, b := range bs {
		if b.Readable != "" {
			out[b.Readable] = true
		}
		if b.Spec.Image != "" {
			out[driver.ImageRef(b.Spec.Image)] = true
			out[b.Spec.Image] = true
		}
	}
	return out
}

func (h *toolsHost) Checks(containerSlug string) []tools.Check {
	out := []tools.Check{}
	for _, c := range h.a.drv.History(containerSlug) {
		out = append(out, tools.Check{At: c.At, OK: c.OK, Message: c.Message})
	}
	return out
}

var _ = url.Values{}
