package edge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"opm/agent/internal/model"
)

const clientHost = "env.client.example.gov.in"

// testUpstream is the upstream of every client route: echo JSON, SSE, websocket echo, a Range file, an upload sink.
type testUpstream struct {
	srv       *httptest.Server
	port      int
	uploads   atomic.Int64 // requests that reached the sink
	firstByte atomic.Int64 // unix nanos of the first upload byte
	lastBytes atomic.Int64
}

func newTestUpstream(t *testing.T) *testUpstream {
	u := &testUpstream{}
	file := bytes.Repeat([]byte("0123456789"), 100)
	ws := websocket.Handler(func(c *websocket.Conn) { _, _ = io.Copy(c, c) })
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/ws"):
			ws.ServeHTTP(w, r)
		case strings.Contains(p, "/real-time/"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fl := w.(http.Flusher)
			n, gap := 10, 500*time.Millisecond
			if s := r.URL.Query().Get("gap"); s != "" {
				gap, _ = time.ParseDuration(s)
				n = 3
			}
			for i := 0; i < n; i++ {
				fmt.Fprintf(w, "data: %d\n\n", i)
				fl.Flush()
				if i < n-1 {
					time.Sleep(gap)
				}
			}
		case strings.Contains(p, "/upload"):
			u.uploads.Add(1)
			buf := make([]byte, 64*1024)
			var total int64
			for {
				n, err := r.Body.Read(buf)
				if n > 0 && total == 0 {
					u.firstByte.Store(time.Now().UnixNano())
				}
				total += int64(n)
				if err != nil {
					break
				}
			}
			u.lastBytes.Store(total)
			_, _ = fmt.Fprintf(w, `{"bytes":%d}`, total)
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
			_ = json.NewEncoder(w).Encode(map[string]any{"method": r.Method, "uri": r.RequestURI, "host": r.Host, "headers": hdr})
		}
	}))
	u.port, _ = strconv.Atoi(u.srv.URL[strings.LastIndex(u.srv.URL, ":")+1:])
	t.Cleanup(u.srv.Close)
	return u
}

func selfSigned(t *testing.T, host string) (string, string, *x509.CertPool) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})), pool
}

// clientEdge is edges.client of shared/testdata/edge-vectors.json (the anonymized client nginx config).
func clientEdge(t *testing.T) model.Edge {
	b, err := os.ReadFile("../../../shared/testdata/edge-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Edges map[string]model.Edge `json:"edges"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v.Edges["client"]
}

// TestClientEdgeParity runs the client edge on real Caddy against real upstream test servers.
func TestClientEdgeParity(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	up := newTestUpstream(t)
	e := clientEdge(t)
	for i := range e.Routes {
		r := &e.Routes[i]
		if r.RouteID == "x-ez-terminal-real-time" {
			r.Action.ReadTimeoutSec = 2 // SSE: data every 500 ms must pass, an idle gap > 2 s is cut
		}
		if r.RouteID == "x-ez-admin" { // an upload sink route with a small body limit for the chunked 413
			lim := int64(1 << 20)
			r.Action.MaxBodyBytes = &lim
		}
	}
	e.Hosts[0].ResponseHeaders.Always = []string{"Cache-Control"}
	ups := map[string]map[string]int{}
	for _, r := range e.Routes {
		if r.Action.Type == "proxy" {
			ups[r.Action.ContainerSlug] = map[string]int{r.Action.PortName: up.port}
		}
	}
	ready := map[string]bool{}
	for k := range ups {
		ready[k] = k != "ez-heatmap"
	}
	Register("parity", &fakeHost{ready: ready})
	defer Unregister("parity")
	certPEM, keyPEM, pool := selfSigned(t, clientHost)
	hp, sp := freePort(t), freePort(t)
	in := &Input{Instance: "parity", SiteSlug: "app", HTTPPort: hp, HTTPSPort: &sp, Hosts: e.Hosts, Routes: e.Routes, MaxBodyBytes: e.MaxBodyBytes,
		Upstreams: ups, FilesPEM: map[string]PEMPair{clientHost: {Cert: certPEM, Key: keyPEM}}}
	if err := ValidateInput(in, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("parity", t.TempDir(), in.Build(""), false); err != nil {
		t.Fatal(err)
	}
	defer Stop("parity")
	base := "http://127.0.0.1:" + strconv.Itoa(hp)
	tr := &http.Transport{DisableCompression: true}
	cl := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 60 * time.Second}
	do := func(method, path string, body io.Reader, hdr map[string]string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, body)
		req.Host = clientHost
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	echo := func(body string) (uri string, h map[string]string) {
		var x struct {
			URI     string            `json:"uri"`
			Headers map[string]string `json:"headers"`
			Host    string            `json:"host"`
		}
		_ = json.Unmarshal([]byte(body), &x)
		x.Headers["Host"] = x.Host
		return x.URI, x.Headers
	}

	t.Run("longest prefix", func(t *testing.T) {
		for path, want := range map[string]string{
			"/x/ez-terminal/y": "/y", "/": "/", "/dashboard/42": "/dashboard/42", "/x/ez-apis/v1/devices?limit=5": "/v1/devices?limit=5",
			"/swagger/ui": "/swagger/ui", "/apis/docs/": "/docs/", "/x/pg-admin/browser/": "/x/pg-admin/browser/", "/x/admin/": "/",
		} {
			r, b := do("GET", path, nil, nil)
			if uri, _ := echo(b); r.StatusCode != 200 || uri != want {
				t.Errorf("%s: %d %s (want %s)", path, r.StatusCode, b, want)
			}
		}
		r, _ := do("GET", "/x/ez-terminal", nil, nil)
		if r.StatusCode != 301 || r.Header.Get("Location") != "/x/ez-terminal/" {
			t.Errorf("auto-redirect: %d %s", r.StatusCode, r.Header.Get("Location"))
		}
		r, _ = do("GET", "/x/nodered?a=b", nil, nil)
		if r.StatusCode != 301 || r.Header.Get("Location") != "/x/nodered/?a=b" {
			t.Errorf("auto-redirect query: %d %s", r.StatusCode, r.Header.Get("Location"))
		}
	})
	t.Run("regex 301", func(t *testing.T) {
		for path, want := range map[string]string{"/apis/docs": "/apis/docs/", "/apis/docs?a=1": "/apis/docs/?a=1", "/apis/_docs_/v2": "/apis/_docs_/v2/"} {
			r, _ := do("GET", path, nil, nil)
			if r.StatusCode != 301 || r.Header.Get("Location") != want {
				t.Errorf("%s: %d %q", path, r.StatusCode, r.Header.Get("Location"))
			}
		}
	})
	t.Run("internal rewrite", func(t *testing.T) {
		r, b := do("GET", "/api/v0002/device/types?x=1", nil, nil)
		if uri, _ := echo(b); r.StatusCode != 200 || uri != "/api/v0002/device/types?x=1" {
			t.Errorf("%d %s", r.StatusCode, b)
		}
	})
	t.Run("request headers", func(t *testing.T) {
		r, b := do("GET", "/x/pg-admin/browser/", nil, map[string]string{"X-Forwarded-For": "6.6.6.6"})
		_, h := echo(b)
		if r.StatusCode != 200 || h["X-Script-Name"] != "/x/pg-admin" || h["Host"] != clientHost || h["X-Real-Ip"] != "127.0.0.1" ||
			h["X-Forwarded-For"] != "127.0.0.1" || h["X-Forwarded-Proto"] != "http" || h["X-Forwarded-Host"] != clientHost {
			t.Errorf("%d %v", r.StatusCode, h)
		}
	})
	t.Run("response headers", func(t *testing.T) {
		r, _ := do("GET", "/x/ez-terminal/s3/file.bin", nil, nil)
		if r.StatusCode != 200 || r.Header.Values("Cache-Control")[0] != "public, max-age=3600, must-revalidate" || len(r.Header.Values("Cache-Control")) != 1 ||
			r.Header.Get("Pragma") != "" || r.Header.Get("Expires") != "" {
			t.Errorf("s3: %d %v", r.StatusCode, r.Header)
		}
		r, _ = do("GET", "/x/ez-admin/z", nil, nil)
		if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0" || r.Header.Get("Pragma") != "no-cache" || r.Header.Get("Expires") != "0" {
			t.Errorf("server-level: %d %v", r.StatusCode, r.Header)
		}
		r, _ = do("GET", "/x/ez-admin/404", nil, nil)
		if r.StatusCode != 404 || r.Header.Get("Cache-Control") == "" || r.Header.Get("Pragma") != "" {
			t.Errorf("upstream 404: always only: %d %v", r.StatusCode, r.Header)
		}
		r, b := do("GET", "/x/ez-heatmap/t", nil, nil)
		if r.StatusCode != 503 || r.Header.Get("Cache-Control") == "" || r.Header.Get("Pragma") != "" || !strings.Contains(b, "503") {
			t.Errorf("edge 503: always on the edge page: %d %v", r.StatusCode, r.Header)
		}
	})
	t.Run("range", func(t *testing.T) {
		r, b := do("GET", "/x/ez-terminal/s3/file.bin", nil, map[string]string{"Range": "bytes=10-19"})
		if r.StatusCode != 206 || r.Header.Get("Content-Range") != "bytes 10-19/1000" || b != "0123456789" {
			t.Errorf("%d %s %q", r.StatusCode, r.Header.Get("Content-Range"), b)
		}
	})
	t.Run("sse", func(t *testing.T) {
		start := time.Now()
		req, _ := http.NewRequest("GET", base+"/x/ez-terminal/real-time/stream", nil)
		req.Host = clientHost
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(resp.Body)
		line, _ := br.ReadString('\n')
		if first := time.Since(start); line != "data: 0\n" || first > time.Second {
			t.Fatalf("first event %q after %v", line, first)
		}
		rest, err := io.ReadAll(br)
		resp.Body.Close()
		if err != nil || !strings.Contains(string(rest), "data: 9") {
			t.Fatalf("stream with data every 500 ms (read timeout 2 s) was cut: %v %q", err, rest)
		}
		if d := time.Since(start); d < 4*time.Second {
			t.Fatalf("stream too short: %v", d)
		}
		// an upstream idle for 3 s (> readTimeoutSec 2) is cut
		req, _ = http.NewRequest("GET", base+"/x/ez-terminal/real-time/stream?gap=3s", nil)
		req.Host = clientHost
		resp, err = cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		rest, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(rest), "data: 0") || strings.Contains(string(rest), "data: 1") {
			t.Fatalf("idle stream not cut: %q", rest)
		}
	})
	t.Run("websocket", func(t *testing.T) {
		cfg, _ := websocket.NewConfig("ws://"+clientHost+"/x/ez-terminal/real-time/ws", "http://"+clientHost)
		conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(hp))
		if err != nil {
			t.Fatal(err)
		}
		ws, err := websocket.NewClient(cfg, conn)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			msg := fmt.Sprintf("hello %d", i)
			if err := websocket.Message.Send(ws, msg); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := websocket.Message.Receive(ws, &got); err != nil || got != msg {
				t.Fatalf("echo %q %v", got, err)
			}
		}
		ws.Close()
		r, _ := do("GET", "/x/pg-admin/ws", nil, map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"})
		if r.StatusCode != 400 {
			t.Errorf("websocket off: %d", r.StatusCode)
		}
	})
	t.Run("upload", func(t *testing.T) {
		const size = 100 * 1024 * 1024
		var peak atomic.Uint64
		var ms runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&ms)
		base0 := ms.HeapInuse
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				case <-time.After(20 * time.Millisecond):
				}
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
			}
		}()
		var sentDone atomic.Int64
		body := &countingReader{left: size, done: &sentDone}
		req, _ := http.NewRequest("PUT", base+"/x/ez-terminal/s3/upload", body)
		req.ContentLength = size
		req.Host = clientHost
		resp, err := cl.Do(req)
		close(stop)
		wg.Wait()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || up.lastBytes.Load() != size {
			t.Fatalf("100 MB upload: %d %s", resp.StatusCode, b)
		}
		growth := int64(peak.Load()) - int64(base0)
		t.Logf("100 MB upload: heap growth %d MiB, first upstream byte %v before the client finished", growth>>20, time.Duration(sentDone.Load()-up.firstByte.Load()))
		if growth > 48<<20 {
			t.Errorf("upload not streamed: heap grew %d MiB", growth>>20)
		}
		if up.firstByte.Load() >= sentDone.Load() {
			t.Errorf("upstream got the first byte only after the client finished")
		}
		// Content-Length over the edge limit (100 MiB) → 413 before the upstream
		n0 := up.uploads.Load()
		req, _ = http.NewRequest("PUT", base+"/x/ez-terminal/s3/upload", io.LimitReader(zeroReader{}, size+1))
		req.ContentLength = size + 1
		req.Host = clientHost
		resp, err = cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 413 || up.uploads.Load() != n0 || resp.Header.Get("Cache-Control") != "" {
			t.Errorf("413 by Content-Length: %d (upstream calls %d) %v", resp.StatusCode, up.uploads.Load()-n0, resp.Header)
		}
		// chunked over the route limit (1 MiB on x-ez-admin) → 413
		req, _ = http.NewRequest("POST", base+"/x/ez-admin/upload", io.NopCloser(io.LimitReader(zeroReader{}, 2<<20)))
		req.ContentLength = -1
		req.Host = clientHost
		resp, err = cl.Do(req)
		switch {
		case err != nil && (strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "connection reset")):
			// the edge answered 413 and closed while the client was still sending: the Go client may report the write
			// error instead of the answer (a race of the test client, round I fix of a flaky run)
			t.Logf("413 chunked: the client saw %v (edge closed the request early)", err)
		case err != nil:
			t.Fatal(err)
		default:
			b, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 413 || !strings.Contains(string(b), "413") {
				t.Errorf("413 chunked: %d %s", resp.StatusCode, b)
			}
		}
		// chunked under the limit passes
		req, _ = http.NewRequest("POST", base+"/x/ez-admin/upload", io.NopCloser(io.LimitReader(zeroReader{}, 512<<10)))
		req.ContentLength = -1
		req.Host = clientHost
		resp, err = cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), `"bytes":524288`) {
			t.Errorf("chunked under the limit: %d %s", resp.StatusCode, b)
		}
	})
	t.Run("tls files + http on one host", func(t *testing.T) {
		hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: clientHost},
			DialContext: func(ctx2 context.Context, n, a string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx2, "tcp", "127.0.0.1:"+strconv.Itoa(sp))
			}}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := hc.Get("https://" + clientHost + "/x/admin/login")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		uri, h := echo(string(b))
		if resp.StatusCode != 200 || uri != "/login" || h["X-Forwarded-Proto"] != "https" {
			t.Errorf("https: %d %s", resp.StatusCode, b)
		}
		r, b2 := do("GET", "/x/admin/login", nil, nil)
		if uri, _ := echo(b2); r.StatusCode != 200 || uri != "/login" {
			t.Errorf("http (no redirect): %d %s", r.StatusCode, b2)
		}
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

type countingReader struct {
	left int64
	done *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		c.done.Store(time.Now().UnixNano())
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	for i := range p {
		p[i] = 'x'
	}
	c.left -= int64(len(p))
	return len(p), nil
}
