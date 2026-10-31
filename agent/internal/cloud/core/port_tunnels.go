package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// §21 #125 port tunnels: the in-memory hub of ONE cloud process: a TCP listener per tunnel on its public port, the agents'
// long-polls for new connections, and the byte queues of the open connections. The tunnels themselves are stored; the
// live state is not: a restart drops the connections and the agents' reports come back within a poll.

// the tunnel part of shared/src/tunnels.ts
const (
	PORT_TUNNEL_CHUNK_MAX    = 1024 * 1024
	portTunnelBufferMax      = 4 * 1024 * 1024
	portTunnelOpenTimeoutSec = 20
	portTunnelIdleAgentSec   = 75
	portTunnelLabelMax       = 100
	portTunnelConnectedSec   = 45
)

var tunnelPortEntry = regexp.MustCompile(`^(\d{1,5})(?:[` + jsSpace + `]*-[` + jsSpace + `]*(\d{1,5}))?$`)

// ParseTunnelPorts is parseTunnelPorts.
func ParseTunnelPorts(spec string) ([]int, error) {
	seen := map[int]bool{}
	for _, raw := range strings.Split(spec, ",") {
		e := doc.JSTrim(raw)
		if e == "" {
			continue
		}
		m := tunnelPortEntry.FindStringSubmatch(e)
		if m == nil {
			return nil, fmt.Errorf(`TUNNEL_FREE_PORTS: "%s" is not a port or a range a-b`, e)
		}
		a, _ := strconv.Atoi(m[1])
		b := a
		if m[2] != "" {
			b, _ = strconv.Atoi(m[2])
		}
		if a < 1024 || b > 65535 || a > b {
			return nil, fmt.Errorf(`TUNNEL_FREE_PORTS: "%s" must be within 1024-65535 (low-high)`, e)
		}
		if b-a > 20000 {
			return nil, fmt.Errorf(`TUNNEL_FREE_PORTS: "%s" is too large a range`, e)
		}
		for p := a; p <= b; p++ {
			seen[p] = true
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out, nil
}

// PortTunnelTargetText is portTunnelTargetText.
func PortTunnelTargetText(t *doc.Map) string {
	switch t.Val("kind") {
	case "container":
		return JSString(t.Val("containerSlug")) + " · " + JSString(t.Val("portName"))
	case "edge":
		return "edge " + JSString(t.Val("scheme"))
	}
	return "server port " + JSString(t.Val("port"))
}

var tunnelSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func isPort(v any, lo float64) bool {
	f, ok := v.(float64)
	return ok && isInt(v) && f >= lo && f <= 65535
}

// ValidatePortTunnel is validatePortTunnel: the problem with a tunnel request, or "".
func ValidatePortTunnel(v any) string {
	var r *doc.Map
	switch x := v.(type) {
	case *doc.Map:
		r = x
	case []any:
		r = doc.New()
	default:
		return "a body is required"
	}
	if s, ok := r.StrOK("siteSlug"); !ok || !tunnelSlugRE.MatchString(s) {
		return "siteSlug is not a site slug"
	}
	if !isPort(r.Val("publicPort"), 1024) {
		return "publicPort must be a port 1024–65535"
	}
	if l := r.Val("label"); !nullish(l) {
		if s, ok := l.(string); !ok || doc.JSLen(s) > portTunnelLabelMax {
			return "label is at most 100 characters"
		}
	}
	var t *doc.Map
	switch x := r.Val("target").(type) {
	case *doc.Map:
		t = x
	case []any:
		t = doc.New()
	default:
		return "target is required"
	}
	switch t.Val("kind") {
	case "container":
		if s, ok := t.StrOK("containerSlug"); !ok || !tunnelSlugRE.MatchString(s) {
			return "target.containerSlug is not a container slug"
		}
		if s, ok := t.StrOK("portName"); !ok || s == "" || doc.JSLen(s) > 64 {
			return "target.portName is required"
		}
		return ""
	case "edge":
		if s := t.Val("scheme"); s == "http" || s == "https" {
			return ""
		}
		return "target.scheme must be http or https"
	case "host":
		if isPort(t.Val("port"), 1) {
			return ""
		}
		return "target.port must be a port 1–65535"
	}
	return "target.kind must be container, edge or host"
}

type tunnelSite struct {
	lastPoll  int64
	enabled   map[string]bool
	remoteGui any // bool or nil
	reach     []string
	queue     []any
	waiters   []*tunnelWaiter
}

type tunnelWaiter struct{ ch chan []any }

type tunnelConn struct {
	connID, deploymentID, tunnelID, agentSite string

	sock net.Conn
	// write + half-close of the socket; a pending send finishes before the client's EOF closes it
	wmu sync.Mutex
	// guarded by pt
	inbuf        []byte
	clientClosed bool
	picked       bool
	destroyed    bool
	paused       bool
	recvWaiter   *connWaiter
	lastAgentAt  int64
	openTimer    *time.Timer
	resume       chan struct{}
	onClose      sync.Once
	listener     *tunnelListener
}

type connWaiter struct{ ch chan struct{} }

type tunnelListener struct {
	tunnel           *doc.Map
	server           net.Listener
	listening        bool
	err              any // string or nil
	open             map[string]bool
	lastConnectionAt any
	lastError        any
}

var pt = struct {
	sync.Mutex
	sites     map[string]*tunnelSite
	siteOrder []string
	conns     map[string]*tunnelConn
	listeners map[int]*tunnelListener
	stop      chan struct{}
	enabled   bool
}{sites: map[string]*tunnelSite{}, conns: map[string]*tunnelConn{}, listeners: map[int]*tunnelListener{}}

// ConfiguredPorts is configuredPorts: TUNNEL_FREE_PORTS (or OPM_TUNNEL_FREE_PORTS); empty when unset or malformed (logged).
func ConfiguredPorts() []int {
	spec, ok := os.LookupEnv("TUNNEL_FREE_PORTS")
	if !ok {
		spec = os.Getenv("OPM_TUNNEL_FREE_PORTS")
	}
	l, err := ParseTunnelPorts(spec)
	if err != nil {
		driver.Log.Error("TUNNEL_FREE_PORTS is not valid", "error", err.Error())
		return nil
	}
	return l
}

func portConfigured(p int) bool {
	for _, x := range ConfiguredPorts() {
		if x == p {
			return true
		}
	}
	return false
}

// HubRunning is hubRunning: the hub runs (the VM cloud started it).
func HubRunning() bool {
	pt.Lock()
	defer pt.Unlock()
	return pt.enabled
}

// FreePorts is freePorts: configured and not taken by any tunnel.
func FreePorts() []any {
	out := []any{}
	cfg := ConfiguredPorts()
	pt.Lock()
	defer pt.Unlock()
	for _, p := range cfg {
		if pt.listeners[p] == nil {
			out = append(out, float64(p))
		}
	}
	return out
}

// PortTaken is portTaken.
func PortTaken(p int) bool {
	pt.Lock()
	defer pt.Unlock()
	return pt.listeners[p] != nil
}

func ptState(d, s string) *tunnelSite {
	k := rgKey(d, s)
	st := pt.sites[k]
	if st == nil {
		st = &tunnelSite{enabled: map[string]bool{}}
		pt.sites[k] = st
		pt.siteOrder = append(pt.siteOrder, k)
	}
	return st
}

func ptConnected(st *tunnelSite) bool {
	return st != nil && hubNow()-st.lastPoll < portTunnelConnectedSec*1000
}

// ptRoute is routeOf: via "" + ok = direct; a site = through that connected agent; ok false = no route.
func ptRoute(d, s string) (via string, ok bool) {
	if ptConnected(pt.sites[rgKey(d, s)]) {
		return "", true
	}
	for _, k := range pt.siteOrder {
		st := pt.sites[k]
		if !strings.HasPrefix(k, d+"/") || !ptConnected(st) {
			continue
		}
		for _, x := range st.reach {
			if x == s {
				return k[len(d)+1:], true
			}
		}
	}
	return "", false
}

func routeJSON(via string, ok bool) any {
	if !ok {
		return nil
	}
	if via == "" {
		return doc.Obj("kind", "direct")
	}
	return doc.Obj("kind", "via", "siteSlug", via)
}

// ───────────────────────── listeners

// StartPortTunnels is startPortTunnels: start the hub with the stored tunnels (server start, VM cloud only).
func StartPortTunnels(tunnels []*doc.Map) {
	pt.Lock()
	pt.enabled = true
	start := pt.stop == nil
	if start {
		pt.stop = make(chan struct{})
	}
	stop := pt.stop
	pt.Unlock()
	for _, t := range tunnels {
		Listen(t)
	}
	if start {
		go func() {
			tick := time.NewTicker(15 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					ptSweep()
				}
			}
		}()
	}
}

// StopPortTunnels is stopPortTunnels.
func StopPortTunnels() {
	pt.Lock()
	ports := make([]int, 0, len(pt.listeners))
	for p := range pt.listeners {
		ports = append(ports, p)
	}
	stop := pt.stop
	pt.stop = nil
	pt.enabled = false
	pt.Unlock()
	for _, p := range ports {
		Unlisten(p)
	}
	if stop != nil {
		close(stop)
	}
}

// node's message for a failed listen (it is shown in the console)
func listenMessage(err error, host string, port int) string {
	at := host + ":" + strconv.Itoa(port)
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return "listen EADDRINUSE: address already in use " + at
	case errors.Is(err, syscall.EACCES):
		return "listen EACCES: permission denied " + at
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "listen EADDRNOTAVAIL: address not available " + at
	}
	return err.Error()
}

// Listen is listen.
func Listen(t *doc.Map) {
	port := int(t.Num("publicPort"))
	Unlisten(port)
	l := &tunnelListener{tunnel: t, open: map[string]bool{}}
	pt.Lock()
	pt.listeners[port] = l
	if !portConfigured(port) {
		l.err = "port " + strconv.Itoa(port) + " is not in TUNNEL_FREE_PORTS any more"
		pt.Unlock()
		return
	}
	pt.Unlock()
	host := os.Getenv("OPM_TUNNEL_HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	srv, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if strings.Contains(host, ":") && err != nil {
		srv, err = net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	pt.Lock()
	if err != nil {
		l.listening = false
		l.err = listenMessage(err, host, port)
		pt.Unlock()
		driver.Log.Error("port tunnel listen failed", "port", port, "tunnelId", t.Val("tunnelId"), "error", l.err)
		return
	}
	if pt.listeners[port] != l { // removed meanwhile
		pt.Unlock()
		srv.Close()
		return
	}
	l.server = srv
	l.listening = true
	l.err = nil
	pt.Unlock()
	go func() {
		for {
			c, err := srv.Accept()
			if err != nil {
				return
			}
			ptOnClient(l, c)
		}
	}()
}

// Unlisten is unlisten.
func Unlisten(port int) {
	pt.Lock()
	l := pt.listeners[port]
	if l == nil {
		pt.Unlock()
		return
	}
	delete(pt.listeners, port)
	srv := l.server
	var open []*tunnelConn
	for id := range l.open {
		if c := pt.conns[id]; c != nil {
			open = append(open, c)
		}
	}
	pt.Unlock()
	if srv != nil {
		srv.Close()
	}
	for _, c := range open {
		c.destroy()
	}
}

// destroy is socket.destroy(): the 'close' event follows.
func (c *tunnelConn) destroy() {
	pt.Lock()
	c.destroyed = true
	pt.Unlock()
	c.sock.Close()
	c.closed()
}

// closed is the socket's 'close' event.
func (c *tunnelConn) closed() {
	c.onClose.Do(func() {
		pt.Lock()
		c.destroyed = true
		c.clientClosed = true
		c.openTimer.Stop()
		delete(c.listener.open, c.connID)
		ptWakeRecv(c)
		close(c.resume)
		pt.Unlock()
		time.AfterFunc(60*time.Second, func() {
			pt.Lock()
			delete(pt.conns, c.connID)
			pt.Unlock()
		})
	})
}

func ptOnClient(l *tunnelListener, sock net.Conn) {
	pt.Lock()
	defer pt.Unlock()
	t := l.tunnel
	dep, site, tunnelID := t.Str("deploymentId"), t.Str("siteSlug"), t.Str("tunnelId")
	l.lastConnectionAt = doc.NowISO()
	via, ok := ptRoute(dep, site)
	refuse := func(why string) {
		l.lastError = why
		sock.Close()
	}
	if !ok {
		refuse("site " + site + " is not connected to the cloud (nor through a site that reaches it)")
		return
	}
	if via == "" && !ptState(dep, site).enabled[tunnelID] {
		refuse("the tunnel is switched off on site " + site + " (switch it on in that site's agent GUI)")
		return
	}
	agentSite := site
	if via != "" {
		agentSite = via
	}
	c := &tunnelConn{connID: hubID(), deploymentID: dep, tunnelID: tunnelID, agentSite: agentSite, sock: sock, lastAgentAt: hubNow(), paused: true, resume: make(chan struct{}, 1), listener: l}
	c.openTimer = time.AfterFunc(portTunnelOpenTimeoutSec*time.Second, func() {
		pt.Lock()
		picked := c.picked
		if !picked {
			l.lastError = "site " + agentSite + " did not take the connection within 20 s"
		}
		pt.Unlock()
		if !picked {
			c.destroy()
		}
	})
	pt.conns[c.connID] = c
	l.open[c.connID] = true
	go c.reader()
	st := ptState(dep, agentSite)
	st.queue = append(st.queue, doc.Obj("connId", c.connID, "tunnelId", tunnelID, "siteSlug", site, "target", t.Val("target")))
	if len(st.waiters) > 0 {
		w := st.waiters[0]
		st.waiters = st.waiters[1:]
		w.ch <- spliceOpens(st)
	}
}

func spliceOpens(st *tunnelSite) []any {
	out := st.queue
	st.queue = nil
	if out == nil {
		out = []any{}
	}
	return out
}

// reader is the socket's data flow. The socket starts paused (pauseOnConnect): nothing is read, and a client leaving is
// not seen, before the agent's first recv; it pauses again while bufferMax bytes wait.
func (c *tunnelConn) reader() {
	b := make([]byte, 64*1024)
	for {
		pt.Lock()
		paused := c.paused
		pt.Unlock()
		if paused {
			if _, ok := <-c.resume; !ok {
				return
			}
			continue
		}
		n, err := c.sock.Read(b)
		pt.Lock()
		if n > 0 {
			c.inbuf = append(c.inbuf, b[:n]...)
			if len(c.inbuf) >= portTunnelBufferMax {
				c.paused = true
			}
			ptWakeRecv(c)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.clientClosed = true
				ptWakeRecv(c)
			}
			pt.Unlock()
			// allowHalfOpen false: the end of the client ends the connection once pending writes are out
			c.wmu.Lock()
			c.sock.Close()
			c.wmu.Unlock()
			c.closed()
			return
		}
		pt.Unlock()
	}
}

func ptWakeRecv(c *tunnelConn) {
	if w := c.recvWaiter; w != nil {
		c.recvWaiter = nil
		close(w.ch)
	}
}

func ptSweep() {
	now := hubNow()
	var idle []*tunnelConn
	pt.Lock()
	for _, c := range pt.conns {
		if c.picked && !c.destroyed && now-c.lastAgentAt > portTunnelIdleAgentSec*1000 {
			idle = append(idle, c)
		}
	}
	pt.Unlock()
	for _, c := range idle {
		c.destroy()
	}
}

// ───────────────────────── agent side

// TunnelReport is what an agent reports with each long-poll. RemoteGui: bool or nil.
type TunnelReport struct {
	Enabled   []string
	RemoteGui any
	Reach     []string
}

// TunnelNext is next: an agent long-poll: report its switches + the peers it reaches; answer the queued connections or wait.
func TunnelNext(cx context.Context, d, s string, report TunnelReport, waitMs int64) []any {
	pt.Lock()
	st := ptState(d, s)
	st.lastPoll = hubNow()
	st.enabled = map[string]bool{}
	for _, e := range report.Enabled {
		st.enabled[e] = true
	}
	st.remoteGui = report.RemoteGui
	st.reach = st.reach[:0:0]
	for _, x := range report.Reach {
		if x != s {
			st.reach = append(st.reach, x)
		}
	}
	if len(st.queue) > 0 || waitMs <= 0 {
		defer pt.Unlock()
		return spliceOpens(st)
	}
	w := &tunnelWaiter{ch: make(chan []any, 1)}
	st.waiters = append(st.waiters, w)
	pt.Unlock()
	remove := func() bool {
		for i, x := range st.waiters {
			if x == w {
				st.waiters = append(st.waiters[:i:i], st.waiters[i+1:]...)
				return true
			}
		}
		return false
	}
	t := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
	defer t.Stop()
	select {
	case opens := <-w.ch:
		pt.Lock()
		st.lastPoll = hubNow()
		pt.Unlock()
		return opens
	case <-t.C:
		pt.Lock()
		defer pt.Unlock()
		gone := remove()
		st.lastPoll = hubNow()
		if !gone {
			return <-w.ch
		}
		return []any{}
	case <-cx.Done():
		pt.Lock()
		defer pt.Unlock()
		if !remove() {
			return <-w.ch
		}
		return []any{}
	}
}

func ptConnOf(connID, d, s string) *tunnelConn {
	c := pt.conns[connID]
	if c != nil && c.deploymentID == d && c.agentSite == s {
		return c
	}
	return nil
}

func (c *tunnelConn) resumeIfPaused() {
	if c.paused && len(c.inbuf) < portTunnelBufferMax && !c.destroyed {
		c.paused = false
		select {
		case c.resume <- struct{}{}:
		default:
		}
	}
}

// TunnelRecv is recv: bytes from the client (≤ chunkMax); closed once the client left and everything was handed over.
func TunnelRecv(cx context.Context, connID, d, s string, waitMs int64) *doc.Map {
	pt.Lock()
	defer pt.Unlock()
	c := ptConnOf(connID, d, s)
	if c == nil {
		return doc.Obj("dataB64", "", "closed", true)
	}
	c.picked = true
	c.lastAgentAt = hubNow()
	c.openTimer.Stop()
	c.resumeIfPaused()
	if len(c.inbuf) == 0 && !c.clientClosed && waitMs > 0 {
		w := &connWaiter{ch: make(chan struct{})}
		c.recvWaiter = w
		pt.Unlock()
		t := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
		select {
		case <-w.ch:
		case <-t.C:
		case <-cx.Done():
		}
		t.Stop()
		pt.Lock()
		if c.recvWaiter == w {
			c.recvWaiter = nil
		}
	}
	c.lastAgentAt = hubNow()
	n := min(len(c.inbuf), PORT_TUNNEL_CHUNK_MAX)
	data := base64.StdEncoding.EncodeToString(c.inbuf[:n])
	c.inbuf = append([]byte(nil), c.inbuf[n:]...)
	c.resumeIfPaused()
	return doc.Obj("dataB64", data, "closed", c.clientClosed && len(c.inbuf) == 0)
}

// TunnelSend is send: bytes for the client (returns once written); close ends the client connection.
func TunnelSend(connID, d, s string, data []byte, closeIt bool, errText any) bool {
	pt.Lock()
	c := ptConnOf(connID, d, s)
	if c == nil {
		pt.Unlock()
		return false
	}
	c.picked = true
	c.lastAgentAt = hubNow()
	c.openTimer.Stop()
	if errText != nil {
		for _, l := range pt.listeners {
			if l.tunnel.Val("tunnelId") == c.tunnelID {
				l.lastError = errText
				break
			}
		}
	}
	dead := c.destroyed
	pt.Unlock()
	if dead {
		return false
	}
	c.wmu.Lock()
	if len(data) > 0 {
		if _, err := c.sock.Write(data); err != nil {
			c.wmu.Unlock()
			c.destroy()
			return false
		}
	}
	if closeIt {
		if cw, ok := c.sock.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	c.wmu.Unlock()
	pt.Lock()
	defer pt.Unlock()
	return !c.destroyed
}

// ───────────────────────── console view

// TunnelStatus is tunnelStatus.
func TunnelStatus(t *doc.Map, host string) *doc.Map {
	pt.Lock()
	defer pt.Unlock()
	port := int(t.Num("publicPort"))
	l := pt.listeners[port]
	if l != nil && l.tunnel.Val("tunnelId") != t.Val("tunnelId") {
		l = nil
	}
	st := pt.sites[rgKey(t.Str("deploymentId"), t.Str("siteSlug"))]
	var enabled any
	if ptConnected(st) {
		enabled = st.enabled[t.Str("tunnelId")]
	}
	via, ok := ptRoute(t.Str("deploymentId"), t.Str("siteSlug"))
	out := t.Without("deploymentId").Set("address", host+":"+strconv.Itoa(port))
	if l == nil {
		return out.Set("listening", false).Set("listenError", "not started").Set("enabledOnSite", enabled).Set("route", routeJSON(via, ok)).Set("openConnections", 0).Set("lastConnectionAt", nil).Set("lastError", nil)
	}
	return out.Set("listening", l.listening).Set("listenError", l.err).Set("enabledOnSite", enabled).Set("route", routeJSON(via, ok)).Set("openConnections", len(l.open)).Set("lastConnectionAt", l.lastConnectionAt).Set("lastError", l.lastError)
}

// AgentTunnels is agentTunnels.
func AgentTunnels(d string, siteSlugs []string) []any {
	pt.Lock()
	defer pt.Unlock()
	out := make([]any, len(siteSlugs))
	for i, s := range siteSlugs {
		st := pt.sites[rgKey(d, s)]
		var en any
		if ptConnected(st) {
			en = st.remoteGui
		}
		out[i] = doc.Obj("siteSlug", s, "connected", ptConnected(st), "enabledOnSite", en)
	}
	return out
}
