package core

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
)

// §21 #118 remote agent GUI: the browser side on the VM cloud: <cloud>/app/deployments/{id}/remote/{site}/…. The remote
// session cookie (set by openRemoteGui) names the person; the request is queued for the site and answered once the
// site's agent long-polled it and posted the result. The agent GUI build is unchanged: every /_opm path of its HTML / JS /
// CSS and of redirects is re-prefixed here. The agent's own cookies never reach the browser.

// RemoteCookieName is remoteCookieName: the cookie of a remote session (one per site; Path = the remote prefix).
func RemoteCookieName(siteSlug string) string {
	return "opm_remote_" + strings.ReplaceAll(siteSlug, "-", "_")
}

// request headers forwarded to the agent (no cookies / auth of the cloud, no hop-by-hop, no encodings)
var remoteForward = []string{"accept", "accept-language", "authorization", "content-type", "if-modified-since", "if-none-match", "range", "x-requested-with"}

// response headers never passed back
var remoteDrop = map[string]bool{"set-cookie": true, "content-length": true, "content-encoding": true, "transfer-encoding": true, "connection": true, "keep-alive": true, "strict-transport-security": true}

func cookieOf(header, name string) string {
	for _, part := range strings.Split(header, ";") {
		i := strings.IndexByte(part, '=')
		if i > 0 && doc.JSTrim(part[:i]) == name {
			return doc.JSTrim(part[i+1:])
		}
	}
	return ""
}

func remoteOut(r RemoteResult, prefix string) (int, map[string]string, []byte) {
	keys := []string{}
	h := doc.New()
	r.Headers.Each(func(k string, v any) {
		if !remoteDrop[strings.ToLower(k)] {
			h.Set(k, v)
			keys = append(keys, k)
		}
	})
	body := r.Body
	if prefix != "" {
		find := func(n string) string {
			for _, k := range keys {
				if strings.ToLower(k) == n {
					return k
				}
			}
			return ""
		}
		if loc := find("location"); loc != "" {
			h.Set(loc, RewriteRemoteLocation(h.Str(loc), prefix))
		}
		if ct := find("content-type"); ct != "" && IsRewritableType(h.Str(ct)) {
			body = []byte(RewriteRemoteText(DecodeUTF8(body), prefix))
			// the rewritten text differs per prefix: never reuse it from a shared cache
			h.Delete("etag", "ETag")
			h.Set("Cache-Control", "private, no-cache")
		}
	}
	return r.Status, stringHeaders(h), body
}

func stringHeaders(h *doc.Map) map[string]string {
	out := make(map[string]string, h.Len())
	h.Each(func(k string, v any) { out[k], _ = v.(string) })
	return out
}

func tooLargePage() RemoteResult {
	return RemotePage(413, "Too large", "A request through the cloud is at most 16 MiB.")
}

// serveRemoteGui is serveRemoteGui + pipeRemoteStream. False = not a remote GUI path.
func serveRemoteGui(w http.ResponseWriter, r *http.Request, path string, headers map[string]string) bool {
	t := ParseRemoteGuiPath(path)
	if t == nil {
		return false
	}
	send := func(st int, h map[string]string, b []byte) bool {
		cloud.WriteReply(w, st, h, b, path)
		return true
	}
	page := func(p RemoteResult) bool {
		st, h, b := remoteOut(p, "")
		return send(st, h, b)
	}
	prefix := RemoteGuiPrefix(t.DeploymentID, t.SiteSlug)
	s := TouchRemoteSession(cookieOf(headers["cookie"], RemoteCookieName(t.SiteSlug)), t.DeploymentID, t.SiteSlug)
	if s == nil {
		return page(RemotePage(401, "Open the agent GUI from the cloud console", `This link needs a remote session: open the deployment in the cloud console, go to Status and choose "Open agent GUI" next to site `+t.SiteSlug+`.`))
	}
	// the bare prefix → the site's status page
	if path == prefix || path == prefix+"/" {
		return send(302, map[string]string{"Location": prefix + "/_opm/app/" + t.SiteSlug + "/status", "Cache-Control": "no-store"}, []byte{})
	}
	if !strings.HasPrefix(t.AgentPath, "/_opm") {
		return page(RemotePage(404, "Not found", "Only the agent GUI (/_opm) is served through the cloud."))
	}
	// §21 #118 h an event stream (log follow, MQTT live messages): the agent sends the body chunk by chunk
	sse := strings.Contains(headers["accept"], "text/event-stream")
	var body []byte
	if r.Method != "GET" && r.Method != "HEAD" {
		b, err := io.ReadAll(io.LimitReader(r.Body, REMOTE_GUI_REQUEST_BODY_MAX+1))
		if len(b) > REMOTE_GUI_REQUEST_BODY_MAX {
			panic(http.ErrAbortHandler) // readRemoteBody destroys the connection at the limit
		}
		if err != nil {
			return page(tooLargePage())
		}
		body = b
	}
	fwd := doc.New()
	for _, k := range remoteForward {
		if v, ok := headers[k]; ok {
			fwd.Set(k, v)
		}
	}
	var bodyB64 any
	if len(body) > 0 {
		bodyB64 = base64.StdEncoding.EncodeToString(body)
	}
	_, query, hasQ := strings.Cut(r.RequestURI, "?")
	agentPath := t.AgentPath
	if hasQ {
		agentPath += "?" + query
	}
	var stream *WsStream
	req := doc.Obj("sessionId", s.SessionID, "method", r.Method, "path", agentPath, "headers", fwd, "bodyB64", bodyB64, "actor", s.Actor)
	if sse {
		stream = NewRemoteStream(t.DeploymentID, t.SiteSlug)
		req.Set("stream", doc.Obj("streamId", stream.StreamID))
	}
	res := RemoteForward(t.DeploymentID, t.SiteSlug, req)
	streamed := false
	res.Headers.Each(func(k string, _ any) { streamed = streamed || strings.ToLower(k) == REMOTE_GUI_STREAM_HEADER })
	if stream != nil && streamed {
		h := map[string]string{}
		res.Headers.Each(func(k string, v any) {
			if l := strings.ToLower(k); !remoteDrop[l] && l != REMOTE_GUI_STREAM_HEADER {
				h[k], _ = v.(string)
			}
		})
		pipeRemoteStream(w, r, stream, res.Status, h)
		return true
	}
	if stream != nil {
		RemoteToSite(stream, nil) // an older agent answered the whole body at once
	}
	st, h, b := remoteOut(res, prefix)
	return send(st, h, b)
}

// frameQueue moves frames from the hub lock (never blocks) to the goroutine that owns the connection.
type frameQueue struct {
	mu     sync.Mutex
	items  []*doc.Map
	notify chan struct{}
}

func newFrameQueue() *frameQueue { return &frameQueue{notify: make(chan struct{}, 1)} }

func (q *frameQueue) push(f *doc.Map) {
	q.mu.Lock()
	q.items = append(q.items, f)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *frameQueue) take() []*doc.Map {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = nil
	return out
}

// pipeRemoteStream: headers now, then every frame the agent sends; the browser leaving closes the stream (the agent's recv
// long-poll sees it and ends the handler).
func pipeRemoteStream(w http.ResponseWriter, r *http.Request, s *WsStream, status int, headers map[string]string) {
	defer RemoteToSite(s, nil)
	rc := http.NewResponseController(w)
	// the server's ReadTimeout must not end a long stream: the background read would cancel the request
	_ = rc.SetReadDeadline(time.Time{})
	h := w.Header()
	h["Content-Type"] = nil
	for k, v := range headers {
		h[k] = []string{v}
	}
	h["Cache-Control"] = []string{"no-store"}
	h["X-Accel-Buffering"] = []string{"no"}
	w.WriteHeader(status)
	if rc.Flush() != nil {
		return
	}
	q := newFrameQueue()
	RemoteAttachBrowser(s, q.push)
	for {
		for _, f := range q.take() {
			if f.Val("op") == "close" {
				return
			}
			if _, err := w.Write(doc.B64Decode(f.Str("dataB64"))); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		}
		select {
		case <-q.notify:
		case <-r.Context().Done():
			return
		}
	}
}

// ───────────────────────── §21 #118 g WebSocket upgrades (terminal, MQTT client): relayed frame by frame

const (
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsMax  = 8 * 1024 * 1024
)

func wsFrame(op byte, payload []byte) []byte {
	n := len(payload)
	var head []byte
	switch {
	case n < 126:
		head = []byte{0x80 | op, byte(n)}
	case n < 65536:
		head = []byte{0x80 | op, 126, 0, 0}
		binary.BigEndian.PutUint16(head[2:], uint16(n))
	default:
		head = make([]byte, 10)
		head[0], head[1] = 0x80|op, 127
		binary.BigEndian.PutUint64(head[2:], uint64(n))
	}
	return append(head, payload...)
}

// wsConn is the socket of one relay: one goroutine writes, in order; End flushes then half-closes, as socket.end().
type wsConn struct {
	c         net.Conn
	mu        sync.Mutex
	out       [][]byte
	ended     bool
	destroyed bool
	notify    chan struct{}
}

func (s *wsConn) write(b []byte, end bool) {
	s.mu.Lock()
	if !s.destroyed && !s.ended {
		s.out = append(s.out, b)
		s.ended = end
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *wsConn) destroy() {
	s.mu.Lock()
	first := !s.destroyed
	s.destroyed = true
	s.mu.Unlock()
	if first {
		s.c.Close()
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
}

func (s *wsConn) isDestroyed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.destroyed
}

func (s *wsConn) writer() {
	for {
		s.mu.Lock()
		out, ended, dead := s.out, s.ended, s.destroyed
		s.out = nil
		s.mu.Unlock()
		if dead {
			return
		}
		for _, b := range out {
			if _, err := s.c.Write(b); err != nil {
				s.destroy()
				return
			}
		}
		if ended {
			if cw, ok := s.c.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			} else {
				s.destroy()
			}
			return
		}
		<-s.notify
	}
}

// wsParser reads the frames from the browser (masked; fragmentation joined; ping answered here).
type wsParser struct {
	buf    []byte
	parts  [][]byte
	partOp byte
	sock   *wsConn
	stream *WsStream
}

func (p *wsParser) parse() {
	for {
		buf := p.buf
		if len(buf) < 2 {
			return
		}
		fin := buf[0]&0x80 != 0
		op := buf[0] & 0x0f
		masked := buf[1]&0x80 != 0
		n := uint64(buf[1] & 0x7f)
		off := uint64(2)
		switch n {
		case 126:
			if len(buf) < 4 {
				return
			}
			n = uint64(binary.BigEndian.Uint16(buf[2:]))
			off = 4
		case 127:
			if len(buf) < 10 {
				return
			}
			n = binary.BigEndian.Uint64(buf[2:])
			if n > wsMax {
				p.sock.destroy()
				return
			}
			off = 10
		}
		var mask []byte
		if masked {
			if uint64(len(buf)) < off+4 {
				return
			}
			mask = buf[off : off+4]
			off += 4
		}
		if uint64(len(buf)) < off+n {
			return
		}
		payload := append([]byte(nil), buf[off:off+n]...)
		p.buf = buf[off+n:]
		if mask != nil {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch {
		case op == 9:
			p.sock.write(wsFrame(10, payload), false)
			continue
		case op == 10:
			continue
		case op == 8:
			code := 1000
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload))
			}
			reason, head := []byte{}, payload
			if len(payload) > 2 {
				reason = payload[2:]
			}
			if len(head) > 2 {
				head = head[:2]
			}
			RemoteToSite(p.stream, doc.Obj("op", "close", "dataB64", base64.StdEncoding.EncodeToString(reason), "code", code))
			RemoteToSite(p.stream, nil)
			p.sock.write(wsFrame(8, head), true)
			return
		case op == 1 || op == 2:
			p.partOp = op
			p.parts = [][]byte{payload}
		case op == 0:
			p.parts = append(p.parts, payload)
		}
		if fin && op <= 2 {
			var msg []byte
			for _, b := range p.parts {
				msg = append(msg, b...)
			}
			p.parts = nil
			if len(msg) > wsMax {
				p.sock.destroy()
				return
			}
			kind := "binary"
			if p.partOp == 1 {
				kind = "text"
			}
			RemoteToSite(p.stream, doc.Obj("op", kind, "dataB64", base64.StdEncoding.EncodeToString(msg)))
		}
	}
}

// handleRemoteUpgrade: the upgrade of the HTTP server: true = it was a remote GUI path (handled).
func handleRemoteUpgrade(w http.ResponseWriter, r *http.Request) bool {
	rawURL := r.RequestURI
	if rawURL == "" {
		rawURL = "/"
	}
	path, query, hasQ := strings.Cut(rawURL, "?")
	t := ParseRemoteGuiPath(path)
	if t == nil {
		return false
	}
	headers := cloud.RequestHeaders(r)
	hj, ok := w.(http.Hijacker)
	if !ok {
		panic(errors.New("remote gui: the connection cannot be hijacked"))
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return true
	}
	_ = conn.SetDeadline(time.Time{})
	head, _ := rw.Reader.Peek(rw.Reader.Buffered())
	head = append([]byte(nil), head...)
	sock := &wsConn{c: conn, notify: make(chan struct{}, 1)}
	go sock.writer()
	deny := func(status int, msg string) {
		sock.write([]byte("HTTP/1.1 "+strconv.Itoa(status)+" "+msg+"\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"), true)
		// socket.end() of a socket nobody reads: nothing more comes from this connection
		time.AfterFunc(time.Second, sock.destroy)
	}
	s := TouchRemoteSession(cookieOf(headers["cookie"], RemoteCookieName(t.SiteSlug)), t.DeploymentID, t.SiteSlug)
	key := headers["sec-websocket-key"]
	if s == nil {
		deny(401, "Unauthorized")
		return true
	}
	if key == "" || !strings.HasPrefix(t.AgentPath, "/_opm") {
		deny(400, "Bad Request")
		return true
	}
	stream := NewRemoteStream(t.DeploymentID, t.SiteSlug)
	fwd := doc.New()
	keys := []string{"sec-websocket-protocol", "user-agent"}
	sort.Strings(keys)
	for _, k := range keys {
		if headers[k] != "" {
			fwd.Set(k, headers[k])
		}
	}
	// bytes of the browser: buffered until the 101 is out, then parsed; EOF or an error is socket 'close'
	parser := &wsParser{buf: head, sock: sock, stream: stream}
	var pmu sync.Mutex
	upgraded := false
	go func() {
		b := make([]byte, 64*1024)
		for {
			n, err := conn.Read(b)
			if n > 0 {
				pmu.Lock()
				parser.buf = append(parser.buf, b[:n]...)
				if upgraded {
					parser.parse()
				}
				over := !upgraded && len(parser.buf) > wsMax
				pmu.Unlock()
				if over {
					sock.destroy()
				}
			}
			if err != nil {
				sock.destroy()
				RemoteToSite(stream, nil)
				return
			}
		}
	}()
	agentPath := t.AgentPath
	if hasQ {
		agentPath += "?" + query
	}
	go func() {
		res := RemoteForward(t.DeploymentID, t.SiteSlug, doc.Obj("sessionId", s.SessionID, "method", "GET", "path", agentPath, "headers", fwd, "bodyB64", nil, "actor", s.Actor, "ws", doc.Obj("streamId", stream.StreamID)))
		if sock.isDestroyed() {
			RemoteToSite(stream, nil)
			return
		}
		if res.Status != 101 {
			RemoteToSite(stream, nil)
			st := 502
			if res.Status == 401 || res.Status == 403 {
				st = res.Status
			}
			deny(st, "Not upgraded")
			return
		}
		sum := sha1.Sum([]byte(key + wsGUID))
		accept := base64.StdEncoding.EncodeToString(sum[:])
		proto := ""
		res.Headers.Each(func(k string, v any) {
			if p, ok := v.(string); ok && proto == "" && strings.ToLower(k) == "sec-websocket-protocol" {
				proto = p
			}
		})
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n"
		if proto != "" {
			resp += "Sec-WebSocket-Protocol: " + proto + "\r\n"
		}
		sock.write([]byte(resp+"\r\n"), false)
		RemoteSetBrowser(stream, func(f *doc.Map) {
			if sock.isDestroyed() {
				return
			}
			data := doc.B64Decode(f.Str("dataB64"))
			switch f.Val("op") {
			case "close":
				code := 1000
				if c, ok := f.Val("code").(float64); ok {
					code = int(c)
				}
				p := make([]byte, 2+len(data))
				binary.BigEndian.PutUint16(p, uint16(code))
				copy(p[2:], data)
				sock.write(wsFrame(8, p), true)
			case "text":
				sock.write(wsFrame(1, data), false)
			default:
				sock.write(wsFrame(2, data), false)
			}
		})
		pmu.Lock()
		upgraded = true
		parser.parse()
		pmu.Unlock()
	}()
	return true
}

func init() {
	cloud.RemoteGUI = serveRemoteGui
	cloud.RemoteUpgrade = handleRemoteUpgrade
}
