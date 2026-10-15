package tools

// A minimal RFC 6455 WebSocket (server side + a test/bridge client), enough for the terminal: binary + text
// messages, fragmentation, ping/pong, close. No extensions (permessage-deflate is never negotiated).

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WS opcodes.
const (
	OpText   = 1
	OpBinary = 2
	OpClose  = 8
	OpPing   = 9
	OpPong   = 10
)

const wsMaxMessage = 4 << 20

// WSConn is one WebSocket connection.
type WSConn struct {
	c      net.Conn
	br     *bufio.Reader
	client bool // client side: frames are masked
	wmu    sync.Mutex
	closed bool
}

// WSAccept computes Sec-WebSocket-Accept.
func WSAccept(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// IsWebSocket reports an upgrade request.
func IsWebSocket(r *http.Request) bool {
	return headerHas(r.Header, "Connection", "upgrade") && strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func headerHas(h http.Header, k, token string) bool {
	for _, v := range h.Values(k) {
		for _, p := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), token) {
				return true
			}
		}
	}
	return false
}

// SameOrigin: browsers send Origin on WebSocket requests; it must be this agent's own origin (no cross-site
// WebSocket hijacking with the session cookie). Requests without Origin (agents over the mesh) pass here; the agent
// only lets them through when they are signed peers.
func SameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// Upgrade answers 101 and returns the connection. The caller has checked auth.
func Upgrade(w http.ResponseWriter, r *http.Request) (*WSConn, error) { return UpgradeWith(w, r, nil) }

// UpgradeWith is Upgrade with extra response headers on the 101.
func UpgradeWith(w http.ResponseWriter, r *http.Request, extra http.Header) (*WSConn, error) {
	if !IsWebSocket(r) {
		return nil, errors.New("not a WebSocket upgrade request")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("unsupported WebSocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	var sb strings.Builder
	sb.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + WSAccept(key) + "\r\n")
	for k, vs := range extra {
		for _, v := range vs {
			sb.WriteString(k + ": " + strings.NewReplacer("\r", "", "\n", "").Replace(v) + "\r\n")
		}
	}
	sb.WriteString("\r\n")
	resp := sb.String()
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	br := brw.Reader
	if br == nil {
		br = bufio.NewReader(conn)
	}
	return &WSConn{c: conn, br: br}, nil
}

// NewWSClientConn wraps a connection on which a client handshake already completed (bridge, tests).
func NewWSClientConn(c net.Conn, br *bufio.Reader) *WSConn {
	if br == nil {
		br = bufio.NewReader(c)
	}
	return &WSConn{c: c, br: br, client: true}
}

// DialWS performs a client handshake on rawURL (ws://host/path) with extra headers (tests).
func DialWS(rawURL string, hdr http.Header) (*WSConn, *http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	c, err := net.DialTimeout("tcp", u.Host, 10*time.Second)
	if err != nil {
		return nil, nil, err
	}
	kb := make([]byte, 16)
	_, _ = rand.Read(kb)
	key := base64.StdEncoding.EncodeToString(kb)
	var sb strings.Builder
	fmt.Fprintf(&sb, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", u.RequestURI(), u.Host, key)
	for k, vs := range hdr {
		for _, v := range vs {
			fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
		}
	}
	sb.WriteString("\r\n")
	if _, err := c.Write([]byte(sb.String())); err != nil {
		c.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	if resp.StatusCode != 101 {
		c.Close()
		return nil, resp, fmt.Errorf("websocket handshake: status %d", resp.StatusCode)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != WSAccept(key) {
		c.Close()
		return nil, resp, errors.New("websocket handshake: bad accept")
	}
	return NewWSClientConn(c, br), resp, nil
}

// ReadMessage returns the next data message (text or binary); ping is answered, pong ignored, close → io.EOF.
func (w *WSConn) ReadMessage() (int, []byte, error) {
	var msg []byte
	op := 0
	for {
		fin, fop, payload, err := w.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch fop {
		case OpPing:
			_ = w.writeFrame(OpPong, payload)
			continue
		case OpPong:
			continue
		case OpClose:
			code := []byte{0x03, 0xe8}
			if len(payload) >= 2 {
				code = payload[:2]
			}
			_ = w.writeFrame(OpClose, code)
			w.Close()
			return 0, nil, io.EOF
		case 0: // continuation
			if op == 0 {
				return 0, nil, errors.New("websocket: unexpected continuation frame")
			}
		case OpText, OpBinary:
			if op != 0 {
				return 0, nil, errors.New("websocket: interleaved data frames")
			}
			op = fop
		default:
			return 0, nil, fmt.Errorf("websocket: unknown opcode %d", fop)
		}
		if len(msg)+len(payload) > wsMaxMessage {
			return 0, nil, errors.New("websocket: message too large")
		}
		msg = append(msg, payload...)
		if fin {
			return op, msg, nil
		}
	}
}

func (w *WSConn) readFrame() (bool, int, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(w.br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin := h[0]&0x80 != 0
	op := int(h[0] & 0x0f)
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(w.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(w.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > wsMaxMessage {
		return false, 0, nil, errors.New("websocket: frame too large")
	}
	if !w.client && !masked {
		return false, 0, nil, errors.New("websocket: client frames must be masked")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(w.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(w.br, p); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range p {
			p[i] ^= mask[i%4]
		}
	}
	return fin, op, p, nil
}

// WriteMessage sends one unfragmented message.
func (w *WSConn) WriteMessage(op int, p []byte) error { return w.writeFrame(op, p) }

func (w *WSConn) writeFrame(op int, p []byte) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	if w.closed {
		return net.ErrClosed
	}
	hdr := make([]byte, 0, 14)
	hdr = append(hdr, 0x80|byte(op))
	mbit := byte(0)
	if w.client {
		mbit = 0x80
	}
	switch n := len(p); {
	case n < 126:
		hdr = append(hdr, mbit|byte(n))
	case n <= 0xffff:
		hdr = append(hdr, mbit|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, mbit|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	if w.client {
		var m [4]byte
		_, _ = rand.Read(m[:])
		hdr = append(hdr, m[:]...)
		q := make([]byte, len(p))
		for i := range p {
			q[i] = p[i] ^ m[i%4]
		}
		p = q
	}
	_ = w.c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := w.c.Write(append(hdr, p...)); err != nil {
		return err
	}
	return nil
}

// CloseWith sends a close frame with a code + reason and closes.
func (w *WSConn) CloseWith(code int, reason string) {
	b := []byte{byte(code >> 8), byte(code)}
	if len(reason) > 120 {
		reason = reason[:120]
	}
	_ = w.writeFrame(OpClose, append(b, reason...))
	w.Close()
}

// Close closes the connection.
func (w *WSConn) Close() error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.c.Close()
}
