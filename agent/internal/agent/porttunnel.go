package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

// §21 #125 port tunnels: a port of a site reachable at <cloud host>:<public port>. While the cloud view lists tunnels
// (a VM cloud), the agent long-polls GET /v1/agent/tunnels/next (direct link only) and reports which of its site's
// tunnels are switched on (kv port-tunnels, default off), its remote GUI switch and the peers it reaches. For each new
// connection it dials the target — a container port, the edge or a port on the server — or, for a peer's tunnel, that
// peer's agent over the mesh (GET /_opm/peer/tunnel/{tunnelId}, an HTTP upgrade; the peer checks its own switch) — and
// moves the bytes: client → target with .../recv long-polls, target → client with .../send.

const (
	portTunnelPollWait = 25 // s
	portTunnelReadBuf  = 256 << 10
)

type ptStat struct {
	open    int
	last    *string
	lastErr *string
}

type ptState struct {
	mu    sync.Mutex
	stats map[string]*ptStat
	kick  context.CancelFunc
}

var (
	ptMu sync.Mutex
	ptOf = map[*Agent]*ptState{}
)

func (a *Agent) pts() *ptState {
	ptMu.Lock()
	defer ptMu.Unlock()
	s := ptOf[a]
	if s == nil {
		s = &ptState{stats: map[string]*ptStat{}}
		ptOf[a] = s
	}
	return s
}

func (s *ptState) stat(id string) *ptStat {
	st := s.stats[id]
	if st == nil {
		st = &ptStat{}
		s.stats[id] = st
	}
	return st
}

func (a *Agent) ptNote(id string, delta int, errMsg string) {
	s := a.pts()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stat(id)
	st.open += delta
	if delta > 0 {
		t := nowTs()
		st.last = &t
	}
	if errMsg != "" {
		e := trim200(errMsg)
		st.lastErr = &e
	}
}

// portTunnelSwitches: kv port-tunnels (tunnelId → on); absent = off.
func (a *Agent) portTunnelSwitches() map[string]bool {
	m := map[string]bool{}
	_, _ = a.db.GetKV("port-tunnels", &m)
	return m
}

// portTunnelsOfView: the tunnels the cloud lists (nil, false: the cloud carries none).
func (a *Agent) portTunnelsOfView() ([]model.PortTunnelCloudItem, bool) {
	v := a.cloudView()
	if v == nil || v.Tunnels == nil {
		return nil, false
	}
	return *v.Tunnels, true
}

// ownPortTunnel: a tunnel of this site from the cloud view.
func (a *Agent) ownPortTunnel(id string) (model.PortTunnelCloudItem, bool) {
	ts, _ := a.portTunnelsOfView()
	for _, t := range ts {
		if t.TunnelID == id && t.SiteSlug == a.site {
			return t, true
		}
	}
	return model.PortTunnelCloudItem{}, false
}

// ───────────────────────── the cloud long-poll

func (a *Agent) portTunnelLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		ts, ok := a.portTunnelsOfView()
		if !ok {
			sleepCtx(ctx, 30*time.Second)
			continue
		}
		sw := a.portTunnelSwitches()
		on := []string{}
		for _, t := range ts {
			if t.SiteSlug == a.site && sw[t.TunnelID] {
				on = append(on, t.TunnelID)
			}
		}
		reach := []string{}
		for _, s := range a.enrolledSites() {
			if s != a.site && a.reachable(s) {
				reach = append(reach, s)
			}
		}
		rg := "0"
		if a.remoteGuiEnabled() {
			rg = "1"
		}
		q := url.Values{"wait": {strconv.Itoa(portTunnelPollWait)}, "enabled": {strings.Join(on, ",")}, "remoteGui": {rg}, "reach": {strings.Join(reach, ",")}}
		// a switch changed on this site: the running poll is cut short so the cloud learns it at once
		pctx, cancel := context.WithCancel(ctx)
		st := a.pts()
		st.mu.Lock()
		st.kick = cancel
		st.mu.Unlock()
		var resp model.PortTunnelNextResponse
		err := a.cloudJSON(pctx, "GET", "/v1/agent/tunnels/next?"+q.Encode(), nil, &resp, callOpts{direct: true, timeout: (portTunnelPollWait + 15) * time.Second})
		kicked := pctx.Err() != nil && ctx.Err() == nil
		cancel()
		if kicked {
			continue
		}
		if err != nil {
			a.log.Debug("tunnel", "poll failed", "error", err.Error())
			sleepCtx(ctx, backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		for _, o := range resp.Opens {
			o := o
			a.goLoop("port-tunnel-conn", func(c context.Context) { a.servePortTunnel(c, o) })
		}
	}
}

func (a *Agent) kickPortTunnelPoll() {
	s := a.pts()
	s.mu.Lock()
	k := s.kick
	s.mu.Unlock()
	if k != nil {
		k()
	}
}

// servePortTunnel carries one client connection the cloud handed out.
func (a *Agent) servePortTunnel(ctx context.Context, o model.PortTunnelOpen) {
	var conn io.ReadWriteCloser
	var err error
	if o.SiteSlug == a.site {
		conn, err = a.openOwnPortTunnel(ctx, o.TunnelID)
	} else {
		conn, err = a.peerTunnelDial(ctx, o.SiteSlug, o.TunnelID)
	}
	if err != nil {
		msg := err.Error()
		a.ptNote(o.TunnelID, 0, msg)
		a.log.Info("tunnel", "connection refused", "tunnelId", o.TunnelID, "site", o.SiteSlug, "error", msg)
		_ = a.cloudJSON(ctx, "POST", "/v1/agent/tunnels/conn/"+o.ConnID+"/send", model.PortTunnelSendRequest{Close: true, Error: &msg}, nil, callOpts{direct: true, timeout: 15 * time.Second})
		return
	}
	if o.SiteSlug == a.site {
		a.ptNote(o.TunnelID, 1, "")
		defer a.ptNote(o.TunnelID, -1, "")
	}
	a.pumpPortTunnel(ctx, o.ConnID, conn)
}

// openOwnPortTunnel: a tunnel of this site — switched on, then its target dialled.
func (a *Agent) openOwnPortTunnel(ctx context.Context, id string) (net.Conn, error) {
	t, ok := a.ownPortTunnel(id)
	if !ok {
		return nil, fmt.Errorf("site %s does not know tunnel %s (its cloud view is older)", a.site, id)
	}
	if !a.portTunnelSwitches()[id] {
		return nil, fmt.Errorf("the tunnel is switched off on site %s", a.site)
	}
	return a.dialPortTunnelTarget(ctx, t.Target)
}

func (a *Agent) dialPortTunnelTarget(ctx context.Context, t model.PortTunnelTarget) (net.Conn, error) {
	addr := ""
	switch t.Kind {
	case "host":
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(t.Port))
	case "edge":
		a.mu.Lock()
		running, h, hs := a.edgeRunning, a.edgeHTTP, a.edgeHTTPS
		a.mu.Unlock()
		p := h
		if t.Scheme == "https" {
			p = hs
		}
		if !running || p == nil {
			return nil, fmt.Errorf("the edge of site %s does not serve %s", a.site, t.Scheme)
		}
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(*p))
	case "container":
		var err error
		if addr, err = a.DevTools().ContainerPortAddr(ctx, t.ContainerSlug, t.PortName); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown tunnel target %q", t.Kind)
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("site %s: %s", a.site, err.Error())
	}
	return c, nil
}

// pumpPortTunnel moves the bytes between the cloud (the client) and the target until either side ends.
func (a *Agent) pumpPortTunnel(ctx context.Context, connID string, c io.ReadWriteCloser) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer c.Close()
	base := "/v1/agent/tunnels/conn/" + connID
	opts := callOpts{direct: true, timeout: (portTunnelPollWait + 15) * time.Second}
	// client → target
	go func() {
		fails := 0
		for ctx.Err() == nil {
			var r model.PortTunnelRecvResponse
			if err := a.cloudJSON(ctx, "GET", base+"/recv?wait="+strconv.Itoa(portTunnelPollWait), nil, &r, opts); err != nil {
				if fails++; fails > 3 {
					cancel()
					return
				}
				sleepCtx(ctx, time.Second)
				continue
			}
			fails = 0
			if r.DataB64 != "" {
				b, err := base64.StdEncoding.DecodeString(r.DataB64)
				if err != nil {
					cancel()
					return
				}
				if _, err := c.Write(b); err != nil {
					cancel()
					return
				}
			}
			if r.Closed {
				if hc, ok := c.(interface{ CloseWrite() error }); ok {
					_ = hc.CloseWrite()
				}
				return
			}
		}
	}()
	// target → client
	buf := make([]byte, portTunnelReadBuf)
	for ctx.Err() == nil {
		n, rerr := c.Read(buf)
		if n > 0 {
			var r struct {
				Accepted bool `json:"accepted"`
			}
			if err := a.cloudJSON(ctx, "POST", base+"/send", model.PortTunnelSendRequest{DataB64: base64.StdEncoding.EncodeToString(buf[:n])}, &r, opts); err != nil || !r.Accepted {
				return
			}
		}
		if rerr != nil {
			_ = a.cloudJSON(context.WithoutCancel(ctx), "POST", base+"/send", model.PortTunnelSendRequest{Close: true}, nil, callOpts{direct: true, timeout: 15 * time.Second})
			return
		}
	}
}

// ───────────────────────── a peer's tunnel over the mesh

// peerTunnelDial asks the peer that owns the tunnel to dial its target; the upgraded connection carries the bytes.
// A plain TCP connection (not the HTTP client) so a half-close of the client reaches the peer, and through it the target.
func (a *Agent) peerTunnelDial(ctx context.Context, slug, tunnelID string) (io.ReadWriteCloser, error) {
	if !a.reachable(slug) {
		return nil, fmt.Errorf("site %s is not reachable from site %s", slug, a.site)
	}
	base, _, err := a.peerAddr(slug)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("site %s: %s", slug, err.Error())
	}
	path := "/_opm/peer/tunnel/" + url.PathEscape(tunnelID)
	req, _ := http.NewRequest("GET", base+path, nil)
	for k, v := range sign.SignRequest(a.key, a.dep+"/"+a.site, "GET", path, nil, a.signTs()) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "opm-tunnel")
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	br := bufio.NewReader(conn)
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("site %s: %s", slug, err.Error())
	}
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("site %s: %s", slug, err.Error())
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer conn.Close()
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = json.Unmarshal(b, &e)
		if e.Error.Message == "" {
			e.Error.Message = fmt.Sprintf("site %s: status %d", slug, resp.StatusCode)
		}
		return nil, errors.New(e.Error.Message)
	}
	_ = conn.SetDeadline(time.Time{})
	return &upgraded{Conn: conn, r: br}, nil
}

// upgraded reads what the response reader already buffered first; CloseWrite half-closes the TCP connection.
type upgraded struct {
	net.Conn
	r *bufio.Reader
}

func (u *upgraded) Read(b []byte) (int, error) { return u.r.Read(b) }
func (u *upgraded) CloseWrite() error {
	if tc, ok := u.Conn.(interface{ CloseWrite() error }); ok {
		return tc.CloseWrite()
	}
	return nil
}

// servePeerTunnel: GET /_opm/peer/tunnel/{tunnelId} (signed): this site's tunnel, switched on → 101 + raw bytes.
func (a *Agent) servePeerTunnel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("tunnelId")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "opm-tunnel") {
		writeErr(w, herr(model.ErrInvalid, "Upgrade: opm-tunnel is required", nil))
		return
	}
	target, err := a.openOwnPortTunnel(r.Context(), id)
	if err != nil {
		a.ptNote(id, 0, err.Error())
		writeErr(w, herr(model.ErrUnavailable, err.Error(), nil))
		return
	}
	conn, brw, herr2 := http.NewResponseController(w).Hijack()
	if herr2 != nil {
		target.Close()
		writeErr(w, herr(model.ErrInternal, "cannot take over the connection: "+herr2.Error(), nil))
		return
	}
	a.ptNote(id, 1, "")
	defer a.ptNote(id, -1, "")
	defer conn.Close()
	defer target.Close()
	_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: opm-tunnel\r\n\r\n")
	if brw.Flush() != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(target, readerOf(brw.Reader, conn))
		if tc, ok := target.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, target)
		done <- struct{}{}
	}()
	<-done
}

// readerOf: what the server already buffered, then the connection.
func readerOf(br *bufio.Reader, c net.Conn) io.Reader {
	if br.Buffered() > 0 {
		b, _ := br.Peek(br.Buffered())
		return io.MultiReader(strings.NewReader(string(b)), c)
	}
	return c
}

// ───────────────────────── agent GUI: the switches

func (a *Agent) sitePortTunnels() model.SitePortTunnelsResponse {
	ts, cloud := a.portTunnelsOfView()
	sw := a.portTunnelSwitches()
	out := model.SitePortTunnelsResponse{SiteSlug: a.site, CloudTunnels: cloud, RemoteGui: a.remoteGuiEnabled(), Items: []model.SitePortTunnel{}}
	s := a.pts()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range ts {
		if t.SiteSlug != a.site {
			continue
		}
		st := s.stat(t.TunnelID)
		out.Items = append(out.Items, model.SitePortTunnel{PortTunnelCloudItem: t, Enabled: sw[t.TunnelID], OpenConnections: st.open, LastConnectionAt: st.last, LastError: st.lastErr})
	}
	return out
}

func (a *Agent) handleSitePortTunnels(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return a.sitePortTunnels(), 200, nil
}

func (a *Agent) handleSetPortTunnel(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req model.SetPortTunnelRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Enabled == nil {
		return nil, 0, herr(model.ErrInvalid, "body {enabled: boolean} is required", nil)
	}
	id := r.PathValue("tunnelId")
	t, ok := a.ownPortTunnel(id)
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "site "+a.site+" has no tunnel "+id, nil)
	}
	rc := getRC(r)
	sw := a.portTunnelSwitches()
	if sw[id] != *req.Enabled {
		if *req.Enabled {
			sw[id] = true
		} else {
			delete(sw, id)
		}
		if err := a.db.SetKV("port-tunnels", sw); err != nil {
			return nil, 0, herr(model.ErrInternal, err.Error(), nil)
		}
		typ, word := "tunnel.disabled", "off"
		if *req.Enabled {
			typ, word = "tunnel.enabled", "on"
		}
		a.activity(rc.actorPtr(), "agent", typ, trim200(fmt.Sprintf("Tunnel %s on port %d switched %s", portTunnelTargetText(t.Target), t.PublicPort, word)),
			actorData(rc, map[string]any{"tunnelId": id, "target": t.Target, "publicPort": t.PublicPort}))
		a.kickPortTunnelPoll()
	}
	for _, it := range a.sitePortTunnels().Items {
		if it.TunnelID == id {
			return it, 200, nil
		}
	}
	return nil, 0, herr(model.ErrNotFound, "tunnel "+id+" not found", nil)
}

func portTunnelTargetText(t model.PortTunnelTarget) string {
	switch t.Kind {
	case "container":
		return t.ContainerSlug + " · " + t.PortName
	case "edge":
		return "edge " + t.Scheme
	}
	return "server port " + strconv.Itoa(t.Port)
}
