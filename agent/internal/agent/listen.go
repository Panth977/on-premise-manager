package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/edge"
	"opm/agent/internal/edgecfg"
	"opm/agent/internal/model"
	"opm/agent/internal/sdsock"
	"opm/agent/internal/validate"
)

type listener struct {
	purpose string // agent | gui-alias | agent-old
	port    int
	srv     *http.Server
}

func (a *Agent) currentAgentPort() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if l := a.listeners["agent"]; l != nil {
		return l.port
	}
	cfg, _ := a.headConfigUnlocked()
	if s := siteOf(cfg, a.site); s != nil {
		return s.Config.AgentPort
	}
	return a.cfg.AgentPort
}

func (a *Agent) currentAliasPort() *int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if l := a.listeners["gui-alias"]; l != nil {
		p := l.port
		return &p
	}
	return nil
}

// headConfigUnlocked is headConfig without taking a.mu (it only uses the db).
func (a *Agent) headConfigUnlocked() (*model.Config, *model.RevisionCatalog) { return a.headConfig() }

// wantedPorts returns (agentPort, aliasPort) of the head SiteConfig of this site, else agent.json.
func (a *Agent) wantedPorts() (int, *int) {
	cfg, _ := a.headConfig()
	if s := siteOf(cfg, a.site); s != nil {
		return s.Config.AgentPort, s.Config.AgentGuiAliasPort
	}
	return a.cfg.AgentPort, a.cfg.GuiAliasPort.Value
}

func (a *Agent) bindInitial() {
	ap, alias := a.wantedPorts()
	_ = a.applyListeners(ap, alias, false)
	// the edge ALWAYS runs (§21 #86): the head config, else the built-in default edge on agent.json edgeHttpPort (80)
	if err := a.reloadCurrentEdge(false); err != nil {
		a.log.Error("edge", "edge start failed", "error", err.Error())
	}
}

// reloadCurrentEdge reloads the edge from the head config (or the default edge when there is no head).
func (a *Agent) reloadCurrentEdge(quiet bool) error {
	if h := a.headRevision(); h != nil {
		return a.reloadEdgeQ(&h.Config, &h.Catalog, h.Seq, false, quiet)
	}
	c := validate.EmptyConfig()
	return a.reloadEdgeQ(&c, emptyCatalog(), 0, false, quiet)
}

// edgeRetryOnce: while an edge port could not be bound (e.g. :80 held by nginx), retry every 30 s (§21 #86).
func (a *Agent) edgeRetryOnce(context.Context) {
	a.mu.Lock()
	_, e1 := a.bindErrs["edge-http"]
	_, e2 := a.bindErrs["edge-https"]
	a.mu.Unlock()
	if !e1 && !e2 {
		return
	}
	if err := a.reloadCurrentEdge(true); err != nil {
		a.log.Debug("edge", "edge port retry failed", "error", err.Error())
	}
}

func (a *Agent) handlerFor(purpose string) http.Handler {
	if purpose == "agent" || purpose == "agent-old" {
		pm := a.peerMux()
		return reqID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/_opm/peer" || strings.HasPrefix(r.URL.Path, "/_opm/peer/") {
				pm.ServeHTTP(w, r)
				return
			}
			a.serveSurface(w, r, "agent-port")
		}))
	}
	return reqID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_opm/peer" || strings.HasPrefix(r.URL.Path, "/_opm/peer/") {
			a.agent404(w, r)
			return
		}
		a.serveSurface(w, r, "gui-alias")
	}))
}

func (a *Agent) serveSurface(w http.ResponseWriter, r *http.Request, surface string) {
	if r.URL.Path == "/" {
		http.Redirect(w, r, "/_opm/app", http.StatusFound)
		return
	}
	if r.URL.Path == "/_opm" || strings.HasPrefix(r.URL.Path, "/_opm/") {
		a.ServeAgent(w, r, surface)
		return
	}
	a.agent404(w, r)
}

func (a *Agent) startListener(purpose string, port int) error {
	ln, inherited, err := sdsock.Listen(port) // §21 #91: a systemd-held socket when socket-activated
	if err != nil {
		return err
	}
	if inherited {
		a.log.Info("gui", "using the systemd socket", "purpose", purpose, "port", port)
	}
	srv := &http.Server{Handler: a.handlerFor(purpose), ReadHeaderTimeout: 30 * time.Second}
	a.mu.Lock()
	a.listeners[purpose] = &listener{purpose: purpose, port: port, srv: srv}
	delete(a.bindErrs, purpose)
	a.mu.Unlock()
	go func() {
		defer a.log.Recover("gui", "listener "+purpose)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Error("gui", "listener stopped", "purpose", purpose, "port", port, "error", err.Error())
		}
	}()
	a.log.Info("gui", "listening", "purpose", purpose, "port", port)
	return nil
}

func (a *Agent) stopListener(purpose string) {
	a.mu.Lock()
	l := a.listeners[purpose]
	delete(a.listeners, purpose)
	a.mu.Unlock()
	if l != nil {
		_ = l.srv.Close()
	}
}

// bindFailed records a bind error (+ the owning process when /proc tells). The activity + error log are written only
// when the error is new for that purpose / port (the 30 s edge retry does not repeat them).
func (a *Agent) bindFailed(purpose string, port int, err error) {
	owner := portOwner(port)
	a.mu.Lock()
	prev, had := a.bindErrs[purpose]
	a.bindErrs[purpose] = model.BindError{Port: port, Purpose: purpose, Error: err.Error(), Owner: owner}
	a.mu.Unlock()
	if had && prev.Port == port {
		return
	}
	data := map[string]any{"port": port, "purpose": purpose, "error": err.Error()}
	sum := fmt.Sprintf("cannot bind %s port %d", purpose, port)
	if owner != nil {
		data["owner"] = *owner
		sum += " (used by " + *owner + ")"
	}
	a.activity(nil, "system", "agent.port-bind-failed", sum, data)
	a.log.Error("gui", "bind failed", "purpose", purpose, "port", port, "error", err.Error(), "owner", owner)
}

// applyListeners binds agentPort + alias port. The previous agentPort stays open for OldPortGrace (peers learn the
// new port from pings / the cloud view). strict = return the first bind error (APPLY step).
func (a *Agent) applyListeners(agentPort int, alias *int, strict bool) error {
	a.mu.Lock()
	cur := a.listeners["agent"]
	curAlias := a.listeners["gui-alias"]
	a.mu.Unlock()
	if cur == nil || cur.port != agentPort {
		if err := a.startListenerAs("agent", agentPort, cur); err != nil {
			a.bindFailed("agent", agentPort, err)
			if strict {
				return err
			}
		}
	}
	if alias == nil {
		if curAlias != nil {
			a.stopListener("gui-alias")
		}
	} else if curAlias == nil || curAlias.port != *alias {
		if curAlias != nil {
			a.stopListener("gui-alias")
		}
		if err := a.startListener("gui-alias", *alias); err != nil {
			a.bindFailed("gui-alias", *alias, err)
			if strict {
				return err
			}
		}
	}
	return nil
}

func (a *Agent) startListenerAs(purpose string, port int, prev *listener) error {
	if prev != nil {
		// keep the old agent port for a while under another purpose key
		a.mu.Lock()
		delete(a.listeners, purpose)
		if old := a.listeners["agent-old"]; old != nil {
			_ = old.srv.Close()
		}
		prev.purpose = "agent-old"
		a.listeners["agent-old"] = prev
		a.mu.Unlock()
		go func(l *listener) {
			defer a.log.Recover("gui", "old port grace")
			time.Sleep(a.tm.OldPortGrace)
			a.mu.Lock()
			if a.listeners["agent-old"] == l {
				delete(a.listeners, "agent-old")
			}
			a.mu.Unlock()
			_ = l.srv.Close()
		}(prev)
	}
	err := a.startListener(purpose, port)
	if err != nil && prev != nil && prev.port == port {
		return nil
	}
	return err
}

// ───────────────────────── edge

// DefaultEdge404 is the not-found page of the built-in default edge (§21 #86).
const defaultEdgePage = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>OPM · %s</title></head>
<body style="font-family:system-ui,sans-serif;max-width:40rem;margin:10vh auto;padding:0 16px;line-height:1.5"><h1 style="font-size:1.4rem">%s</h1>
<p>%s</p><p><a href="/_opm/">Open /_opm/</a></p><p style="opacity:.6;font-size:.85rem">OPM agent · site %s</p></body></html>`

// defaultEdgeInput is the built-in default edge (§21 #86): no head yet, or this site is not in the head config. Host "*",
// no TLS (no 443), /_opm/* → GUI + API, / → 302 /_opm/app, everything else → the "no configuration" page. nil when
// agent.json edgeHttpPort is null.
func (a *Agent) defaultEdgeInput(cfg *model.Config) *edge.Input {
	port := 80
	if a.cfg.EdgeHTTPPort.Set {
		if a.cfg.EdgeHTTPPort.Value == nil || *a.cfg.EdgeHTTPPort.Value <= 0 {
			return nil
		}
		port = *a.cfg.EdgeHTTPPort.Value
	}
	title, msg := "No configuration applied yet", "This site has no applied configuration yet — open /_opm/ to approve the first update."
	if cfg != nil && len(cfg.Sites) > 0 {
		title, msg = "Site not configured", "This site is not part of the applied configuration — open /_opm/ to see the deployment."
	}
	page := fmt.Sprintf(defaultEdgePage, title, title, msg, htmlEscape(a.site))
	return &edge.Input{Instance: a.instance, SiteSlug: a.site, HTTPPort: port, Hosts: []model.EdgeHost{{Hostname: "*", TLS: model.EdgeTls{Mode: "none"}}},
		Routes: []model.EdgeRoute{}, NotFound404: page, NotFound503: page, RootRedirect: "/_opm/app",
		Upstreams: map[string]map[string]int{}, FilesPEM: map[string]edge.PEMPair{}, DNSConfigs: map[string]map[string]any{}}
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(s)
}

// edgeInput builds the Caddy input of this site from a config (the default edge when the site is not in it).
// The mapping itself is edgecfg.InputFromConfig (shared with the compose exporter, §21 #106).
func (a *Agent) edgeInput(cfg *model.Config, cat *model.RevisionCatalog) (*edge.Input, error) {
	in, _, err := edgecfg.InputFromConfig(edgecfg.InputParams{Instance: a.instance, DeploymentID: a.dep, SiteSlug: a.site, Config: cfg, Catalog: cat,
		Hosts:     a.siteHosts(cfg),
		Upstreams: func(slug string) map[string]int { return a.drv.HealthOf(slug).LoopPorts },
		FileContent: func(hex string) ([]byte, bool) {
			p, ok := a.blobPath(hex)
			if !ok {
				return nil, false
			}
			cert, _ := os.ReadFile(p)
			return cert, true
		},
		NotFoundPage: func(id string) (string, bool) {
			b, err := os.ReadFile(filepath.Join(a.dataDir, "notfound", id+".html"))
			return string(b), err == nil
		},
		DNSConfig: edge.DNSProviderConfig})
	if err != nil {
		return nil, err
	}
	if in == nil {
		return a.defaultEdgeInput(cfg), nil
	}
	return in, nil
}

// dnsConfigFor maps EdgeTls.dnsProvider + dnsTokenSecretSlug to the Caddy DNS provider config (dns-01).
func dnsConfigFor(cfg *model.Config, h model.EdgeHost) (map[string]any, error) {
	return edgecfg.DNSConfigFor(h, func(s string) (string, bool) { return edgecfg.SecretFromConfig(cfg, s) }, edge.DNSProviderConfig)
}

// reloadEdge loads the Caddy config for cfg (strict = APPLY: any failure is returned and the old config keeps running).
// caddy.Load swaps the config gracefully: a port that stays in the config keeps its listening socket (no unbind / rebind,
// no refused connections during an update; §21 #86).
func (a *Agent) reloadEdge(cfg *model.Config, cat *model.RevisionCatalog, revSeq int, strict bool) error {
	return a.reloadEdgeQ(cfg, cat, revSeq, strict, false)
}

// reloadEdgeQ: quiet = a periodic bind retry (no edge.reloaded activity while the port stays busy).
func (a *Agent) reloadEdgeQ(cfg *model.Config, cat *model.RevisionCatalog, revSeq int, strict, quiet bool) error {
	a.edgeMu.Lock()
	defer a.edgeMu.Unlock()
	start := time.Now()
	in, err := a.edgeInput(cfg, cat)
	if err != nil {
		a.edgeFailed(revSeq, err)
		return err
	}
	if in == nil {
		_ = edge.Stop(a.instance)
		a.mu.Lock()
		a.edgeRunning, a.edgeHTTP, a.edgeHTTPS, a.edgeDefault = false, nil, nil, false
		delete(a.bindErrs, "edge-http")
		delete(a.bindErrs, "edge-https")
		a.mu.Unlock()
		a.releaseEdgeSockets()
		return nil
	}
	isDefault := siteOf(cfg, a.site) == nil
	// the agent owns the edge sockets (Caddy gets "fd/<n>"): a config swap never closes a listening socket
	skipHTTP, skipHTTPS := false, false
	wantHTTPS := in.HTTPSPort != nil && in.AnyTLS()
	if l, err := a.edgeSocket(in.HTTPPort); err != nil {
		if strict {
			a.bindFailed("edge-http", in.HTTPPort, err)
			a.edgeFailed(revSeq, err)
			a.releaseEdgeSockets()
			return err
		}
		a.bindFailed("edge-http", in.HTTPPort, err)
		in.SkipHTTP, skipHTTP = true, true
	} else {
		in.HTTPListen = l
	}
	if wantHTTPS {
		if l, err := a.edgeSocket(*in.HTTPSPort); err != nil {
			if strict {
				a.bindFailed("edge-https", *in.HTTPSPort, err)
				a.edgeFailed(revSeq, err)
				a.releaseEdgeSockets()
				return err
			}
			a.bindFailed("edge-https", *in.HTTPSPort, err)
			in.SkipHTTPS, skipHTTPS = true, true
		} else {
			in.HTTPSListen = l
		}
	}
	raw, err := edge.Apply(a.instance, filepath.Join(a.dataDir, "certs"), a.withAccessLog(in.Build("")), false) // §21 #89 access log
	if err != nil {
		a.edgeFailed(revSeq, err)
		a.releaseEdgeSockets() // keeps the sockets of the config that still runs
		return err
	}
	_ = os.WriteFile(filepath.Join(a.dataDir, "edge", "caddy.json"), raw, 0o600)
	t := nowTs()
	seq := revSeq
	a.mu.Lock()
	a.edgeRunning = true
	hp := in.HTTPPort
	a.edgeHTTP, a.edgeHTTPS = &hp, in.HTTPSPort
	if skipHTTP { // the HTTP port could not be bound (retried every 30 s by edgeRetryOnce)
		a.edgeHTTP = nil
	} else {
		delete(a.bindErrs, "edge-http")
	}
	if skipHTTPS || !wantHTTPS { // 443 is bound only when a host has TLS
		a.edgeHTTPS = nil
	}
	if !skipHTTPS {
		delete(a.bindErrs, "edge-https")
	}
	a.edgeDefault = isDefault
	a.edgeErr, a.edgeRevSeq, a.edgeReloaded = nil, &seq, &t
	a.mu.Unlock()
	a.releaseEdgeSockets()
	if quiet && (skipHTTP || skipHTTPS) {
		return nil
	}
	a.activity(nil, "system", "edge.reloaded", fmt.Sprintf("edge reloaded (seq %d)", revSeq), map[string]any{"revSeq": revSeq, "hosts": len(in.Hosts), "routes": len(in.Routes), "durationMs": time.Since(start).Milliseconds(), "defaultEdge": isDefault})
	a.log.Info("edge", "reloaded", "revSeq", revSeq, "hosts", len(in.Hosts), "routes", len(in.Routes), "defaultEdge", isDefault)
	return nil
}

// edgeSock is a listening socket the agent owns for the edge; Caddy listens on "fd/<fd>" (a dup of it).
type edgeSock struct {
	ln *net.TCPListener
	f  *os.File
}

// edgeSocket returns the Caddy listen address of the agent-owned socket on port (bound on first use).
func (a *Agent) edgeSocket(port int) (string, error) {
	a.mu.Lock()
	s := a.edgeSocks[port]
	a.mu.Unlock()
	if s == nil {
		ln, _, err := sdsock.Listen(port) // §21 #91: the systemd-held edge socket when socket-activated
		if err != nil {
			return "", err
		}
		tl, ok := ln.(*net.TCPListener)
		if !ok {
			ln.Close()
			return "", fmt.Errorf("edge port %d: not a TCP socket", port)
		}
		f, err := tl.File()
		if err != nil {
			tl.Close()
			return "", err
		}
		s = &edgeSock{ln: tl, f: f}
		a.mu.Lock()
		a.edgeSocks[port] = s
		a.mu.Unlock()
	}
	return "fd/" + strconv.Itoa(int(s.f.Fd())), nil
}

// releaseEdgeSockets closes the agent-owned sockets the running edge config does not use.
func (a *Agent) releaseEdgeSockets() {
	a.mu.Lock()
	keep := map[int]bool{}
	if a.edgeRunning {
		if a.edgeHTTP != nil {
			keep[*a.edgeHTTP] = true
		}
		if a.edgeHTTPS != nil {
			keep[*a.edgeHTTPS] = true
		}
	}
	var drop []*edgeSock
	for p, s := range a.edgeSocks {
		if !keep[p] {
			drop = append(drop, s)
			delete(a.edgeSocks, p)
		}
	}
	a.mu.Unlock()
	for _, s := range drop {
		_ = s.ln.Close()
		_ = s.f.Close()
	}
}

func (a *Agent) edgeFailed(revSeq int, err error) {
	msg := err.Error()
	a.mu.Lock()
	a.edgeErr = &msg
	a.mu.Unlock()
	a.activity(nil, "system", "edge.reload-failed", "edge reload failed", map[string]any{"revSeq": revSeq, "error": msg})
	a.log.Error("edge", "reload failed", "revSeq", revSeq, "error", msg)
}

// refreshEdgeUpstreams reloads the edge when loopback ports changed (container restarts).
func (a *Agent) refreshEdgeUpstreams() {
	h := a.headRevision()
	if h == nil {
		return
	}
	_ = a.reloadEdgeQ(&h.Config, &h.Catalog, h.Seq, false, false)
}

// guiPorts lists every host port on which /_opm/ answers right now (heartbeat → cloud Setup URLs, §21 #86).
func (a *Agent) guiPorts() []model.GuiPort {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []model.GuiPort{}
	if a.edgeRunning {
		if a.edgeHTTP != nil {
			out = append(out, model.GuiPort{Port: *a.edgeHTTP, Scheme: "http", Kind: "edge"})
		}
		if a.edgeHTTPS != nil {
			out = append(out, model.GuiPort{Port: *a.edgeHTTPS, Scheme: "https", Kind: "edge"})
		}
	}
	if l := a.listeners["gui-alias"]; l != nil {
		out = append(out, model.GuiPort{Port: l.port, Scheme: "http", Kind: "gui-alias"})
	}
	if l := a.listeners["agent"]; l != nil {
		out = append(out, model.GuiPort{Port: l.port, Scheme: "http", Kind: "agent-port"})
	}
	return out
}

// edgeReport for heartbeat / status.
func (a *Agent) edgeReport() model.EdgeReport {
	a.mu.Lock()
	r := model.EdgeReport{DefaultEdge: a.edgeRunning && a.edgeDefault, RevSeq: a.edgeRevSeq, ReloadedAt: a.edgeReloaded, LastReloadError: a.edgeErr, BoundPorts: []int{}, BindErrors: []model.BindError{}, Certs: []model.CertStatus{}}
	for _, l := range a.listeners {
		r.BoundPorts = append(r.BoundPorts, l.port)
	}
	if a.edgeRunning {
		if a.edgeHTTP != nil {
			r.BoundPorts = append(r.BoundPorts, *a.edgeHTTP)
		}
		if a.edgeHTTPS != nil {
			r.BoundPorts = append(r.BoundPorts, *a.edgeHTTPS)
		}
	}
	for _, b := range a.bindErrs {
		r.BindErrors = append(r.BindErrors, b)
	}
	a.mu.Unlock()
	r.Certs = a.certStatuses()
	return r
}

func (a *Agent) certStatuses() []model.CertStatus {
	out := []model.CertStatus{}
	h := a.headRevision()
	if h == nil {
		return out
	}
	site := siteOf(&h.Config, a.site)
	if site == nil {
		return out
	}
	re, err := a.resolveEdge(&h.Config, site)
	if err != nil {
		return out
	}
	for _, host := range re.Hosts {
		if host.TLS.Mode == "none" {
			continue
		}
		a.mu.Lock()
		var le *string
		if e, ok := a.certErrs[host.Hostname]; ok {
			le = &e
		}
		a.mu.Unlock()
		out = append(out, edge.CertStatusFor(filepath.Join(a.dataDir, "certs"), a.site, host, le))
	}
	return out
}
