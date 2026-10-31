package core

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
)

func tunnelFixture(t *testing.T, port int) *doc.Map {
	t.Helper()
	t.Setenv("TUNNEL_FREE_PORTS", fmt.Sprintf("%d-%d", port, port+1))
	t.Setenv("OPM_TUNNEL_HOST", "127.0.0.1")
	StartPortTunnels(nil)
	t.Cleanup(StopPortTunnels)
	tn := doc.Obj("tunnelId", "T1", "deploymentId", "d1", "siteSlug", "app", "target", doc.Obj("kind", "host", "port", 22), "publicPort", port, "label", nil, "createdAt", "t", "createdBy", "e")
	Listen(tn)
	if st := TunnelStatus(tn, "h"); st.Val("listening") != true || st.Str("address") != fmt.Sprintf("h:%d", port) {
		t.Fatalf("status %s", doc.MarshalString(st))
	}
	return tn
}

func dial(t *testing.T, port int) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func report(on ...string) TunnelReport { return TunnelReport{Enabled: on, RemoteGui: true} }

func expectClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadAll(c); err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("not closed: %v", err)
	}
}

func TestTunnelRelay(t *testing.T) {
	const port = 28850
	tn := tunnelFixture(t, port)
	cx := context.Background()
	// not connected, then switched off: refused
	expectClosed(t, dial(t, port))
	TunnelNext(cx, "d1", "app", report(), 0)
	expectClosed(t, dial(t, port))
	if s := TunnelStatus(tn, "h").Str("lastError"); !strings.Contains(s, "switched off") {
		t.Fatalf("lastError %q", s)
	}
	// on: long-poll wakes with the connection, bytes both ways, the first recv starts reading
	got := make(chan []any, 1)
	go func() { got <- TunnelNext(cx, "d1", "app", report("T1"), 5000) }()
	for {
		pt.Lock()
		n := len(pt.sites["d1/app"].waiters)
		pt.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	c := dial(t, port)
	opens := <-got
	if len(opens) != 1 {
		t.Fatalf("opens %v", opens)
	}
	id := opens[0].(*doc.Map).Str("connId")
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	r := TunnelRecv(cx, id, "d1", "app", 2000)
	if b := doc.B64Decode(r.Str("dataB64")); string(b) != "ping" || r.Val("closed") != false {
		t.Fatalf("recv %s", doc.MarshalString(r))
	}
	if !TunnelSend(id, "d1", "app", []byte("pong"), false, nil) {
		t.Fatal("send refused")
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "pong" {
		t.Fatalf("%q %v", b, err)
	}
	if TunnelRecv(cx, id, "d1", "other", 0).Val("closed") != true {
		t.Fatal("another site reads the connection")
	}
	// the client leaves after a last write: bytes first, closed after
	c.Write([]byte("bye"))
	c.(*net.TCPConn).CloseWrite()
	all := ""
	for i := 0; i < 50; i++ {
		r = TunnelRecv(cx, id, "d1", "app", 200)
		all += string(doc.B64Decode(r.Str("dataB64")))
		if r.Val("closed") == true {
			break
		}
	}
	if all != "bye" || r.Val("closed") != true {
		t.Fatalf("%q %v", all, r.Val("closed"))
	}
	if TunnelSend(id, "d1", "app", []byte("x"), false, nil) {
		t.Fatal("send to a closed client accepted")
	}
}

func TestTunnelBackpressureAndClose(t *testing.T) {
	const port = 28852
	tunnelFixture(t, port)
	cx := context.Background()
	TunnelNext(cx, "d2", "app", report(), 0)
	pt.Lock()
	pt.listeners[port].tunnel.Set("deploymentId", "d2")
	pt.Unlock()
	TunnelNext(cx, "d2", "app", report("T1"), 0)
	c := dial(t, port)
	var opens []any
	for len(opens) == 0 {
		opens = TunnelNext(cx, "d2", "app", report("T1"), 200)
	}
	id := opens[0].(*doc.Map).Str("connId")
	const total = 9 * 1024 * 1024
	go func() {
		chunk := make([]byte, 256*1024)
		for i := range chunk {
			chunk[i] = byte(i)
		}
		for n := 0; n < total; n += len(chunk) {
			if _, err := c.Write(chunk); err != nil {
				return
			}
		}
	}()
	TunnelRecv(cx, id, "d2", "app", 0) // first recv: reading starts
	time.Sleep(500 * time.Millisecond)
	pt.Lock()
	buffered, paused := len(pt.conns[id].inbuf), pt.conns[id].paused
	pt.Unlock()
	if buffered < portTunnelBufferMax || buffered > portTunnelBufferMax+64*1024 || !paused {
		t.Fatalf("buffered %d paused %v", buffered, paused)
	}
	n := 0
	for n < total-2*1024*1024 {
		r := TunnelRecv(cx, id, "d2", "app", 500)
		b := doc.B64Decode(r.Str("dataB64"))
		if len(b) > PORT_TUNNEL_CHUNK_MAX {
			t.Fatalf("chunk %d", len(b))
		}
		n += len(b)
	}
	if !TunnelSend(id, "d2", "app", nil, true, "target closed") {
		t.Fatal("close refused")
	}
	Unlisten(port)
	expectClosed(t, c)
}

// remoteServer: the two hooks behind a real HTTP server, with a session for site app.
func remoteServer(t *testing.T, dep string) (*httptest.Server, string) {
	t.Helper()
	s := OpenRemoteSession(dep, "app", doc.Obj("uid", "u", "email", "e", "name", nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			if !handleRemoteUpgrade(w, r) {
				t.Error("not a remote path")
			}
			return
		}
		if !serveRemoteGui(w, r, r.URL.Path, cloud.RequestHeaders(r)) {
			t.Error("not a remote path")
		}
	}))
	t.Cleanup(srv.Close)
	return srv, RemoteCookieName("app") + "=" + s.SessionID
}

func nextReq(t *testing.T, dep string) *doc.Map {
	t.Helper()
	items := RemoteNext(context.Background(), dep, "app", 5000)
	if len(items) != 1 {
		t.Fatalf("items %v", items)
	}
	return items[0].(*doc.Map)
}

func TestRemotePageRewrite(t *testing.T) {
	const dep = "dpage"
	srv, cookie := remoteServer(t, dep)
	done := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest("POST", srv.URL+RemoteGuiPrefix(dep, "app")+"/_opm/x?a=1", strings.NewReader("hello"))
		req.Header.Set("Cookie", cookie)
		req.Header.Set("X-Requested-With", "t")
		req.Header.Set("X-Other", "dropped")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		done <- res
	}()
	q := nextReq(t, dep)
	if q.Str("path") != "/_opm/x?a=1" || q.Str("bodyB64") != base64.StdEncoding.EncodeToString([]byte("hello")) || q.Map("headers").Has("x-other") || !q.Map("headers").Has("x-requested-with") {
		t.Fatalf("%s", doc.MarshalString(q))
	}
	ok := RemoteDeliver(dep, "app", q.Str("reqId"), RemoteResult{200, doc.Obj("Content-Type", "text/html", "ETag", "x", "Set-Cookie", "a=1", "Content-Length", "1"), []byte(`<a href="/_opm/y">`)})
	if !ok || RemoteDeliver(dep, "app", q.Str("reqId"), RemoteResult{Headers: doc.New()}) {
		t.Fatal("deliver")
	}
	res := <-done
	b, _ := io.ReadAll(res.Body)
	if want := `<a href="` + RemoteGuiPrefix(dep, "app") + `/_opm/y">`; string(b) != want {
		t.Fatalf("%s", b)
	}
	if res.Header.Get("Set-Cookie") != "" || res.Header.Get("ETag") != "" || res.Header.Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("%v", res.Header)
	}
}

func TestRemoteEventStream(t *testing.T) {
	const dep = "dsse"
	srv, cookie := remoteServer(t, dep)
	cx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequestWithContext(cx, "GET", srv.URL+RemoteGuiPrefix(dep, "app")+"/_opm/logs", nil)
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Accept", "text/event-stream")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		done <- res
	}()
	q := nextReq(t, dep)
	s := RemoteStreamOf(q.Map("stream").Str("streamId"), dep, "app")
	frame := func(x string) *doc.Map {
		return doc.Obj("op", "binary", "dataB64", base64.StdEncoding.EncodeToString([]byte(x)))
	}
	// a chunk before the browser response started is kept
	if !RemoteToBrowser(s, []any{frame("a\n")}) {
		t.Fatal("early frame refused")
	}
	RemoteDeliver(dep, "app", q.Str("reqId"), RemoteResult{200, doc.Obj("Content-Type", "text/event-stream", REMOTE_GUI_STREAM_HEADER, "1"), nil})
	res := <-done
	if res.Header.Get("X-Accel-Buffering") != "no" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%v", res.Header)
	}
	rd := bufio.NewReader(res.Body)
	if l, err := rd.ReadString('\n'); err != nil || l != "a\n" {
		t.Fatalf("%q %v", l, err)
	}
	RemoteToBrowser(s, []any{frame("b\n")})
	if l, _ := rd.ReadString('\n'); l != "b\n" {
		t.Fatalf("%q", l)
	}
	// the browser leaves: the site's recv sees closed
	cancel()
	r := RemoteRecv(context.Background(), s, 3000)
	if !r.Closed {
		t.Fatal("not closed")
	}
}

func wsClientFrame(op byte, payload []byte) []byte {
	mask := []byte{1, 2, 3, 4}
	var h []byte
	if len(payload) < 126 {
		h = []byte{0x80 | op, 0x80 | byte(len(payload))}
	} else {
		h = []byte{0x80 | op, 0x80 | 126, byte(len(payload) >> 8), byte(len(payload))}
	}
	out := append(append([]byte{}, h...), mask...)
	for i, b := range payload {
		out = append(out, b^mask[i%4])
	}
	return out
}

func TestRemoteWebSocket(t *testing.T) {
	const dep = "dws"
	srv, cookie := remoteServer(t, dep)
	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET %s/_opm/term?x=1 HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Protocol: mqtt\r\nSec-WebSocket-Version: 13\r\nCookie: %s\r\n\r\n", RemoteGuiPrefix(dep, "app"), cookie)
	q := nextReq(t, dep)
	if q.Str("path") != "/_opm/term?x=1" || q.Map("headers").Str("sec-websocket-protocol") != "mqtt" {
		t.Fatalf("%s", doc.MarshalString(q))
	}
	s := RemoteStreamOf(q.Map("ws").Str("streamId"), dep, "app")
	RemoteDeliver(dep, "app", q.Str("reqId"), RemoteResult{101, doc.Obj("Sec-WebSocket-Protocol", "mqtt"), nil})
	rd := bufio.NewReader(c)
	head := ""
	for {
		l, err := rd.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		head += l
		if l == "\r\n" {
			break
		}
	}
	if !strings.Contains(head, "101 Switching") || !strings.Contains(head, "Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=") || !strings.Contains(head, "Sec-WebSocket-Protocol: mqtt") {
		t.Fatal(head)
	}
	// browser → site: fragments joined
	c.Write([]byte{0x01, 0x80 | 2, 0, 0, 0, 0, 'h', 'e'})
	c.Write(wsClientFrame(0, []byte("llo")))
	// ping answered here
	c.Write(wsClientFrame(9, []byte("p")))
	got := ""
	for got == "" {
		r := RemoteRecv(context.Background(), s, 2000)
		for _, f := range r.Frames {
			m := f.(*doc.Map)
			if m.Val("op") != "text" {
				t.Fatalf("%v", m.Val("op"))
			}
			got += string(doc.B64Decode(m.Str("dataB64")))
		}
	}
	if got != "hello" {
		t.Fatal(got)
	}
	pong := make([]byte, 3)
	if _, err := io.ReadFull(rd, pong); err != nil || pong[0] != 0x8a || pong[2] != 'p' {
		t.Fatalf("%v %v", pong, err)
	}
	// site → browser
	RemoteToBrowser(s, []any{doc.Obj("op", "binary", "dataB64", base64.StdEncoding.EncodeToString([]byte("xy")))})
	bin := make([]byte, 4)
	if _, err := io.ReadFull(rd, bin); err != nil || bin[0] != 0x82 || string(bin[2:]) != "xy" {
		t.Fatalf("%v %v", bin, err)
	}
	// the browser closes: frame + closed for the site, close echoed
	c.Write(wsClientFrame(8, []byte{0x03, 0xE8, 'b'}))
	var last *doc.Map
	closed := false
	for !closed {
		r := RemoteRecv(context.Background(), s, 2000)
		closed = r.Closed
		for _, f := range r.Frames {
			last = f.(*doc.Map)
		}
	}
	if last == nil || last.Val("op") != "close" || last.Num("code") != 1000 || string(doc.B64Decode(last.Str("dataB64"))) != "b" {
		t.Fatalf("%v", doc.MarshalString(last))
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(rd, echo); err != nil || echo[0] != 0x88 || echo[1] != 2 {
		t.Fatalf("%v %v", echo, err)
	}
	if !s.Closed() {
		t.Fatal("stream open")
	}
}

func TestRemoteUpgradeRefused(t *testing.T) {
	const dep = "dno"
	srv, _ := remoteServer(t, dep)
	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET %s/_opm/term HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: k\r\n\r\n", RemoteGuiPrefix(dep, "app"))
	l, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.HasPrefix(l, "HTTP/1.1 401 Unauthorized") {
		t.Fatalf("%q %v", l, err)
	}
}

func TestDecodeUTF8(t *testing.T) {
	for in, want := range map[string]string{"\xe2\x82": "�", "a\xffb": "a�b", "\xf0\x9f\x98": "�", "\xc0\x80": "��", "\xed\xa0\x80": "���", "é": "é", "\xe2\x82x": "�x"} {
		if got := DecodeUTF8([]byte(in)); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
