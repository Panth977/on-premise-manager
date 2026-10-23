package fakecloud

// §21 #125 port tunnels: the cloud view lists the tunnels (EnableTunnels); a test opens a client connection with
// OpenTunnel and drives it like a TCP client while the agent carries it over next / recv / send.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/model"
)

// TunnelReport is what an agent's last poll told.
type TunnelReport struct {
	Enabled   []string
	RemoteGui string
	Reach     []string
	At        time.Time
}

// TunnelConn is one client connection of a test.
type TunnelConn struct {
	ID       string
	in       chan []byte
	inClosed bool
	mu       sync.Mutex
	out      []byte
	closed   chan struct{}
	once     sync.Once
	err      string
}

type tunState struct {
	mu      sync.Mutex
	on      bool
	items   []model.PortTunnelCloudItem
	reports map[string]TunnelReport
	opens   map[string]chan model.PortTunnelOpen
	conns   map[string]*TunnelConn
}

var (
	reTunRecv = regexp.MustCompile(`^/v1/agent/tunnels/conn/([^/]+)/recv$`)
	reTunSend = regexp.MustCompile(`^/v1/agent/tunnels/conn/([^/]+)/send$`)
)

func (c *Cloud) ts() *tunState {
	c.tunOnce.Do(func() {
		c.tunSt = &tunState{reports: map[string]TunnelReport{}, opens: map[string]chan model.PortTunnelOpen{}, conns: map[string]*TunnelConn{}}
	})
	return c.tunSt
}

func (s *tunState) openCh(site string) chan model.PortTunnelOpen {
	ch := s.opens[site]
	if ch == nil {
		ch = make(chan model.PortTunnelOpen, 16)
		s.opens[site] = ch
	}
	return ch
}

// EnableTunnels makes the cloud view carry these tunnels (a VM cloud).
func (c *Cloud) EnableTunnels(items ...model.PortTunnelCloudItem) {
	st := c.ts()
	st.mu.Lock()
	st.on = true
	st.items = append([]model.PortTunnelCloudItem{}, items...)
	st.mu.Unlock()
	c.mu.Lock()
	c.epoch++
	c.mu.Unlock()
}

// TunnelReportOf returns the last poll of a site.
func (c *Cloud) TunnelReportOf(site string) (TunnelReport, bool) {
	st := c.ts()
	st.mu.Lock()
	defer st.mu.Unlock()
	r, ok := st.reports[site]
	return r, ok
}

// OpenTunnel hands a new client connection to the agent of agentSite (for the tunnel of siteSlug).
func (c *Cloud) OpenTunnel(agentSite, tunnelID, siteSlug string) *TunnelConn {
	st := c.ts()
	st.mu.Lock()
	tc := &TunnelConn{ID: randID(), in: make(chan []byte, 64), closed: make(chan struct{})}
	st.conns[tc.ID] = tc
	var target model.PortTunnelTarget
	for _, it := range st.items {
		if it.TunnelID == tunnelID {
			target = it.Target
		}
	}
	ch := st.openCh(agentSite)
	st.mu.Unlock()
	ch <- model.PortTunnelOpen{ConnID: tc.ID, TunnelID: tunnelID, SiteSlug: siteSlug, Target: target}
	return tc
}

// Write sends client bytes to the agent.
func (t *TunnelConn) Write(b []byte) { t.in <- append([]byte(nil), b...) }

// CloseClient ends the client side.
func (t *TunnelConn) CloseClient() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.inClosed {
		t.inClosed = true
		close(t.in)
	}
}

// Received is everything the agent sent to the client so far.
func (t *TunnelConn) Received() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.out)
}

// Closed is closed once the agent ended the connection.
func (t *TunnelConn) Closed() <-chan struct{} { return t.closed }

// Err is the error the agent reported.
func (t *TunnelConn) Err() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func waitOf(r *http.Request) time.Duration {
	w, err := strconv.Atoi(r.URL.Query().Get("wait"))
	if err != nil || w < 0 {
		w = 0
	}
	return time.Duration(min(w, 30)) * time.Second
}

func csvOf(s string) []string {
	out := []string{}
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// routeTunnels serves the agent endpoints (no c.mu: they long-poll).
func (c *Cloud) routeTunnels(w http.ResponseWriter, r *http.Request, body []byte, rc reqCtx) bool {
	p, m := r.URL.Path, r.Method
	var g []string
	st := c.ts()
	switch {
	case m == "GET" && p == "/v1/agent/tunnels/next":
		q := r.URL.Query()
		st.mu.Lock()
		st.reports[rc.signer] = TunnelReport{Enabled: csvOf(q.Get("enabled")), RemoteGui: q.Get("remoteGui"), Reach: csvOf(q.Get("reach")), At: time.Now()}
		ch := st.openCh(rc.signer)
		st.mu.Unlock()
		out := model.PortTunnelNextResponse{Opens: []model.PortTunnelOpen{}}
		select {
		case o := <-ch:
			out.Opens = append(out.Opens, o)
		case <-time.After(waitOf(r)):
		case <-r.Context().Done():
		}
		writeJSON(w, 200, out)
	case m == "GET" && match(reTunRecv, p, &g):
		st.mu.Lock()
		tc := st.conns[g[1]]
		st.mu.Unlock()
		if tc == nil {
			writeJSON(w, 200, model.PortTunnelRecvResponse{Closed: true})
			return true
		}
		select {
		case b, ok := <-tc.in:
			if !ok {
				writeJSON(w, 200, model.PortTunnelRecvResponse{Closed: true})
			} else {
				writeJSON(w, 200, model.PortTunnelRecvResponse{DataB64: base64.StdEncoding.EncodeToString(b)})
			}
		case <-tc.closed:
			writeJSON(w, 200, model.PortTunnelRecvResponse{Closed: true})
		case <-time.After(waitOf(r)):
			writeJSON(w, 200, model.PortTunnelRecvResponse{})
		case <-r.Context().Done():
		}
	case m == "POST" && match(reTunSend, p, &g):
		st.mu.Lock()
		tc := st.conns[g[1]]
		st.mu.Unlock()
		var req model.PortTunnelSendRequest
		if tc == nil || json.Unmarshal(body, &req) != nil {
			writeJSON(w, 200, map[string]bool{"accepted": false})
			return true
		}
		b, _ := base64.StdEncoding.DecodeString(req.DataB64)
		tc.mu.Lock()
		tc.out = append(tc.out, b...)
		if req.Error != nil {
			tc.err = *req.Error
		}
		tc.mu.Unlock()
		if req.Close {
			tc.once.Do(func() { close(tc.closed) })
		}
		writeJSON(w, 200, map[string]bool{"accepted": true})
	default:
		return false
	}
	return true
}

func (c *Cloud) viewTunnels(v *model.CloudView) {
	if c.tunSt == nil {
		return
	}
	st := c.tunSt
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.on {
		return
	}
	items := append([]model.PortTunnelCloudItem{}, st.items...)
	v.Tunnels = &items
}
