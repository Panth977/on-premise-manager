// Command testsrv is the upstream of the edge-parity Docker test: one container per upstream of the client nginx
// config. It listens on $PORT (default 8080) and answers:
//
//	/healthz                     200 ok
//	…/ws                         websocket echo (minimal RFC 6455, text + binary, no extensions)
//	…/real-time/…                text/event-stream: 10 events, one every 500 ms (?gap=3s: 3 events with that gap)
//	…/upload…                    reads the body, answers {"bytes":N}
//	…/file.bin                   a 1000-byte file with Range support + Cache-Control / Pragma / Expires (s3-like)
//	…/404                        404 + the echo JSON
//	anything else                the echo JSON {name, method, uri, host, headers}
package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	name := os.Getenv("NAME")
	file := bytes.Repeat([]byte("0123456789"), 100)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/healthz":
			_, _ = io.WriteString(w, "ok")
		case strings.HasSuffix(p, "/ws"):
			wsEcho(w, r)
		case strings.Contains(p, "/real-time/"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fl, _ := w.(http.Flusher)
			n, gap := 10, 500*time.Millisecond
			if s := r.URL.Query().Get("gap"); s != "" {
				gap, _ = time.ParseDuration(s)
				n = 3
			}
			for i := 0; i < n; i++ {
				fmt.Fprintf(w, "data: %d\n\n", i)
				if fl != nil {
					fl.Flush()
				}
				if i < n-1 {
					time.Sleep(gap)
				}
			}
		case strings.Contains(p, "/upload"):
			n, _ := io.Copy(io.Discard, r.Body)
			fmt.Fprintf(w, `{"bytes":%d}`, n)
		case strings.HasSuffix(p, "/file.bin"):
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			http.ServeContent(w, r, "file.bin", time.Unix(0, 0), bytes.NewReader(file))
		default:
			if strings.HasSuffix(p, "/404") {
				w.WriteHeader(404)
			}
			hdr := map[string]string{}
			for k := range r.Header {
				hdr[k] = r.Header.Get(k)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "method": r.Method, "uri": r.RequestURI, "host": r.Host, "headers": hdr})
		}
	})
	if err := http.ListenAndServe(":"+port, h); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// wsEcho is a minimal websocket echo server (enough for the test client).
func wsEcho(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket only", 400)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", 500)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
	_ = rw.Flush()
	br := bufio.NewReader(rw)
	for {
		op, payload, err := readFrame(br)
		if err != nil || op == 8 {
			return
		}
		if op == 9 { // ping → pong
			op = 10
		}
		if err := writeFrame(rw, op, payload); err != nil {
			return
		}
		_ = rw.Flush()
	}
}

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	op := h[0] & 0x0f
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range p {
			p[i] ^= mask[i%4]
		}
	}
	return op, p, nil
}

func writeFrame(w io.Writer, op byte, p []byte) error {
	h := []byte{0x80 | op}
	switch {
	case len(p) < 126:
		h = append(h, byte(len(p)))
	case len(p) < 65536:
		h = append(h, 126, byte(len(p)>>8), byte(len(p)))
	default:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(len(p)))
		h = append(append(h, 127), b[:]...)
	}
	if _, err := w.Write(h); err != nil {
		return err
	}
	_, err := w.Write(p)
	return err
}
