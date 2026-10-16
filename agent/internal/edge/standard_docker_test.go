package edge

// §21 #106 D7 parity: the edge exported for the OFFICIAL caddy image (edgecfg.BuildStandard) answers like the agent edge.
// Every case of shared/testdata/edge-vectors.json (the agent's decisions, edgeres) plus the client nginx parity behaviours
// of TestClientEdgeParity run against caddy:2.11.7 in Docker, with e2e/edge-parity/testsrv upstreams named like the
// containers on one docker network. Skipped with -short or without docker.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"opm/agent/internal/edgecfg"
	"opm/agent/internal/model"
)

// StandardCaddyImage is the official image the compose export pins (edgecfg / internal/export use the same).
const standardCaddyImage = "caddy:2.11.7"

func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func dockerOK(t *testing.T) {
	if testing.Short() {
		t.Skip("docker integration (-short)")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no docker: ", err)
	}
}

// testsrvImage builds e2e/edge-parity/testsrv into opm-edge-testsrv:1 (same image as the agent's edge parity test).
func testsrvImage(t *testing.T) string {
	t.Helper()
	root, _ := filepath.Abs("../../..")
	src := filepath.Join(root, "e2e", "edge-parity", "testsrv")
	dir := t.TempDir()
	b := exec.Command("go", "build", "-o", filepath.Join(dir, "testsrv"), ".")
	b.Dir = src
	b.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off", "GOFLAGS=")
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("build testsrv: %v %s", err, out)
	}
	df, _ := os.ReadFile(filepath.Join(src, "Dockerfile"))
	_ = os.WriteFile(filepath.Join(dir, "Dockerfile"), df, 0o644)
	dockerRun(t, "build", "-q", "-t", "opm-edge-testsrv:1", dir)
	return "opm-edge-testsrv:1"
}

type stdEdge struct {
	http, https int
	name        string
}

// startStandard starts the upstreams (except skip) + one official caddy container with the BuildStandard output of in.
func startStandard(t *testing.T, label string, in *edgecfg.Input, ports map[string]map[string]int, files map[string]string, skip map[string]bool) stdEdge {
	t.Helper()
	img := testsrvImage(t)
	sfx := strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
	netName := "opmstd-" + label + "-" + sfx
	dockerRun(t, "network", "create", netName)
	var cleanup []string
	t.Cleanup(func() {
		if len(cleanup) > 0 {
			_ = exec.Command("docker", append([]string{"rm", "-f"}, cleanup...)...).Run()
		}
		_ = exec.Command("docker", "network", "rm", netName).Run()
	})
	for slug, pm := range ports {
		if skip[slug] {
			continue
		}
		for _, p := range pm {
			c := "opmstd-" + slug + "-" + sfx
			dockerRun(t, "run", "-d", "--name", c, "--network", netName, "--network-alias", slug, "-e", "NAME="+slug, "-e", "PORT="+strconv.Itoa(p), img)
			cleanup = append(cleanup, c)
		}
	}
	cfg, notes := edgecfg.BuildStandard(in, edgecfg.StandardOptions{})
	t.Logf("%s: notes %v", label, notes)
	raw, _ := json.Marshal(cfg)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "caddy.json"), raw, 0o644)
	name := "opmstd-caddy-" + label + "-" + sfx
	args := []string{"create", "--name", name, "--network", netName, "-p", "127.0.0.1::" + strconv.Itoa(in.HTTPPort)}
	if in.HTTPSPort != nil {
		args = append(args, "-p", "127.0.0.1::"+strconv.Itoa(*in.HTTPSPort))
	}
	args = append(args, standardCaddyImage, "caddy", "run", "--config", "/etc/caddy/caddy.json")
	dockerRun(t, args...)
	cleanup = append(cleanup, name)
	dockerRun(t, "cp", filepath.Join(dir, "caddy.json"), name+":/etc/caddy/caddy.json")
	if len(files) > 0 {
		fd := t.TempDir()
		_ = os.MkdirAll(filepath.Join(fd, "tls"), 0o755)
		for n, c := range files {
			_ = os.WriteFile(filepath.Join(fd, "tls", n), []byte(c), 0o644)
		}
		dockerRun(t, "cp", fd+"/.", name+":/etc/caddy") // → /etc/caddy/tls/<host>.crt|.key (the default TLSDir)
	}
	dockerRun(t, "start", name)
	hp := func(p int) int {
		out := dockerRun(t, "port", name, strconv.Itoa(p)+"/tcp")
		_, port, _ := net.SplitHostPort(strings.Split(out, "\n")[0])
		n, _ := strconv.Atoi(port)
		return n
	}
	e := stdEdge{http: hp(in.HTTPPort), name: name}
	if in.HTTPSPort != nil {
		e.https = hp(*in.HTTPSPort)
	}
	// wait until caddy answers
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(e.http), time.Second)
		if err == nil {
			c.Close()
			resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(e.http) + "/")
			if err == nil {
				resp.Body.Close()
				break
			}
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatalf("caddy did not start: %s", logs)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return e
}

type echoBody struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	URI     string            `json:"uri"`
	Host    string            `json:"host"`
	Headers map[string]string `json:"headers"`
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func TestStandardEdgeVectorsDocker(t *testing.T) {
	dockerOK(t)
	edges, ports := vectorEdges(t)
	b, _ := os.ReadFile(filepath.Join("..", "..", "..", "shared", "testdata", "edge-vectors.json"))
	var v struct {
		Cases []struct {
			Edge      string `json:"edge"`
			URL       string `json:"url"`
			Method    string `json:"method"`
			BodyBytes *int64 `json:"bodyBytes"`
			Upgrade   bool   `json:"upgrade"`
			Expect    struct {
				Status      int     `json:"status"`
				Kind        string  `json:"kind"`
				Location    *string `json:"location"`
				UpstreamURL *string `json:"upstreamUrl"`
				Trace       *struct {
					RequestHeaders []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"requestHeaders"`
					RemovedRequestHeaders []string                  `json:"removedRequestHeaders"`
					ResponseHeaders       model.EdgeResponseHeaders `json:"responseHeaders"`
				} `json:"trace"`
			} `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, _ := selfSigned(t, clientHost)
	running := map[string]stdEdge{}
	for name, e := range edges {
		files := map[string]string{}
		for _, h := range e.Hosts {
			if h.TLS.Mode == "files" {
				files[h.Hostname+".crt"], files[h.Hostname+".key"] = certPEM, keyPEM
			}
		}
		running[name] = startStandard(t, name, standardInput(e, ports[name]), ports[name], files, nil)
	}
	pass, na := 0, 0
	for i, c := range v.Cases {
		c := c
		t.Run(fmt.Sprintf("%02d %s %s", i, c.Edge, c.URL), func(t *testing.T) {
			if c.Expect.Kind == "agent" {
				na++
				t.Skip("agent GUI / API path (/_opm): not part of the export (no /_opm route)")
			}
			u, _ := url.Parse(c.URL)
			se := running[c.Edge]
			port := se.http
			if u.Scheme == "https" {
				port = se.https
			}
			method := c.Method
			if method == "" {
				method = "GET"
			}
			tr := &http.Transport{DisableCompression: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: u.Hostname()},
				DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
				}}
			cl := &http.Client{Transport: tr, CheckRedirect: noRedirect, Timeout: 60 * time.Second}
			var body io.Reader
			if c.BodyBytes != nil {
				body = io.LimitReader(zeroReader{}, *c.BodyBytes)
			}
			req, _ := http.NewRequest(method, u.Scheme+"://"+u.Host+u.EscapedPath()+func() string {
				if u.RawQuery != "" {
					return "?" + u.RawQuery
				}
				return ""
			}(), body)
			if c.BodyBytes != nil {
				req.ContentLength = *c.BodyBytes
			}
			if c.Upgrade {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
				req.Header.Set("Sec-WebSocket-Version", "13")
				req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			}
			resp, err := cl.Do(req)
			if c.Expect.Kind == "tls-fail" {
				if err == nil {
					resp.Body.Close()
					t.Fatalf("want a TLS failure, got %d", resp.StatusCode)
				}
				pass++
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			rb, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			want := c.Expect.Status
			if c.Upgrade && c.Expect.Kind == "proxy" {
				want = http.StatusSwitchingProtocols
			}
			if resp.StatusCode != want {
				t.Fatalf("status %d, want %d (%s): %s", resp.StatusCode, want, c.Expect.Kind, rb)
			}
			if c.Expect.Location != nil && resp.Header.Get("Location") != *c.Expect.Location {
				t.Fatalf("Location %q, want %q", resp.Header.Get("Location"), *c.Expect.Location)
			}
			if c.Expect.Kind == "proxy" && !c.Upgrade && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				pass++ // the SSE upstream (testsrv) answers events, not the echo; status checked above
				return
			}
			if c.Expect.Kind == "proxy" && !c.Upgrade {
				var eb echoBody
				if err := json.Unmarshal(rb, &eb); err != nil {
					t.Fatalf("not the echo: %s", rb)
				}
				up, _ := url.Parse(*c.Expect.UpstreamURL)
				if eb.Name != up.Hostname() {
					t.Fatalf("upstream %s, want %s", eb.Name, up.Hostname())
				}
				gotURI, _ := url.PathUnescape(eb.URI)
				wantURI := strings.TrimPrefix(*c.Expect.UpstreamURL, up.Scheme+"://"+up.Host)
				if gotURI != wantURI {
					t.Fatalf("upstream uri %q, want %q", gotURI, wantURI)
				}
				eb.Headers["Host"] = eb.Host
				for _, h := range c.Expect.Trace.RequestHeaders {
					got := eb.Headers[http.CanonicalHeaderKey(h.Name)]
					if h.Value == "{client-ip}" {
						if got == "" {
							t.Fatalf("request header %s missing", h.Name)
						}
						continue
					}
					if h.Name == "X-Forwarded-Host" && u.Port() != "" {
						// $host:$port when the request names a port (Caddy {http.request.hostport}); same on the agent
						if got != u.Host && got != h.Value {
							t.Fatalf("request header %s = %q, want %q", h.Name, got, h.Value)
						}
						continue
					}
					if got != h.Value {
						t.Fatalf("request header %s = %q, want %q", h.Name, got, h.Value)
					}
				}
				for _, n := range c.Expect.Trace.RemovedRequestHeaders {
					if _, ok := eb.Headers[http.CanonicalHeaderKey(n)]; ok {
						t.Fatalf("request header %s must be removed", n)
					}
				}
				rh := c.Expect.Trace.ResponseHeaders
				for _, kv := range rh.Set {
					if resp.Header.Get(kv.Name) != kv.Value {
						t.Fatalf("response header %s = %q, want %q", kv.Name, resp.Header.Get(kv.Name), kv.Value)
					}
				}
				for _, kv := range rh.Add {
					found := false
					for _, x := range resp.Header.Values(kv.Name) {
						found = found || x == kv.Value
					}
					if !found {
						t.Fatalf("response header %s lacks %q (%v)", kv.Name, kv.Value, resp.Header.Values(kv.Name))
					}
				}
			}
			pass++
		})
	}
	t.Logf("edge vectors on %s: %d passed, %d agent-only (not exported), %d total", standardCaddyImage, pass, na, len(v.Cases))
}

// TestStandardClientParityDocker replays TestClientEdgeParity (the agent edge with the client nginx config) on the
// official caddy image: longest prefix, regex 301, internal rewrite, request + response headers, range, SSE flush + read
// timeout, websocket, 100 MB streamed upload, 413 by Content-Length + chunked, TLS files + http on one host, the 503 page
// of a container that is not running.
func TestStandardClientParityDocker(t *testing.T) {
	dockerOK(t)
	e := clientEdge(t)
	for i := range e.Routes {
		r := &e.Routes[i]
		if r.RouteID == "x-ez-terminal-real-time" {
			r.Action.ReadTimeoutSec = 2
		}
		if r.RouteID == "x-ez-admin" {
			lim := int64(1 << 20)
			r.Action.MaxBodyBytes = &lim
		}
	}
	e.Hosts[0].ResponseHeaders.Always = []string{"Cache-Control"}
	_, vports := vectorEdges(t)
	ports := vports["client"]
	certPEM, keyPEM, pool := selfSigned(t, clientHost)
	in := standardInput(e, ports)
	se := startStandard(t, "client", in, ports, map[string]string{clientHost + ".crt": certPEM, clientHost + ".key": keyPEM}, map[string]bool{"ez-heatmap": true})
	base := "http://127.0.0.1:" + strconv.Itoa(se.http)
	cl := &http.Client{Transport: &http.Transport{DisableCompression: true}, CheckRedirect: noRedirect, Timeout: 120 * time.Second}
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
	echo := func(body string) (string, map[string]string) {
		var x echoBody
		_ = json.Unmarshal([]byte(body), &x)
		if x.Headers == nil {
			x.Headers = map[string]string{}
		}
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
		if r, _ := do("GET", "/x/ez-terminal", nil, nil); r.StatusCode != 301 || r.Header.Get("Location") != "/x/ez-terminal/" {
			t.Errorf("auto-redirect: %d %s", r.StatusCode, r.Header.Get("Location"))
		}
		if r, _ := do("GET", "/x/nodered?a=b", nil, nil); r.StatusCode != 301 || r.Header.Get("Location") != "/x/nodered/?a=b" {
			t.Errorf("auto-redirect query: %d %s", r.StatusCode, r.Header.Get("Location"))
		}
	})
	t.Run("regex 301", func(t *testing.T) {
		for path, want := range map[string]string{"/apis/docs": "/apis/docs/", "/apis/docs?a=1": "/apis/docs/?a=1", "/apis/_docs_/v2": "/apis/_docs_/v2/"} {
			if r, _ := do("GET", path, nil, nil); r.StatusCode != 301 || r.Header.Get("Location") != want {
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
		// the client address is the docker gateway here (127.0.0.1 on the agent): only equality of the two is checked
		if r.StatusCode != 200 || h["X-Script-Name"] != "/x/pg-admin" || h["Host"] != clientHost || h["X-Real-Ip"] == "" || h["X-Real-Ip"] == "6.6.6.6" ||
			h["X-Forwarded-For"] != h["X-Real-Ip"] || h["X-Forwarded-Proto"] != "http" || h["X-Forwarded-Host"] != clientHost {
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
			t.Errorf("edge 503 (container not running): %d %v %s", r.StatusCode, r.Header, b)
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
			t.Fatalf("stream cut: %v %q", err, rest)
		}
		req, _ = http.NewRequest("GET", base+"/x/ez-terminal/real-time/stream?gap=3s", nil)
		req.Host = clientHost
		resp, err = cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		rest, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(rest), "data: 0") || strings.Contains(string(rest), "data: 1") {
			t.Fatalf("idle stream (3 s > read timeout 2 s) not cut: %q", rest)
		}
	})
	t.Run("websocket", func(t *testing.T) {
		cfg, _ := websocket.NewConfig("ws://"+clientHost+"/x/ez-terminal/real-time/ws", "http://"+clientHost)
		conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(se.http))
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
		if r, _ := do("GET", "/x/pg-admin/ws", nil, map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}); r.StatusCode != 400 {
			t.Errorf("websocket off: %d", r.StatusCode)
		}
	})
	t.Run("upload", func(t *testing.T) {
		const size = 100 * 1024 * 1024
		req, _ := http.NewRequest("PUT", base+"/x/ez-terminal/s3/upload", io.LimitReader(zeroReader{}, size))
		req.ContentLength = size
		req.Host = clientHost
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !bytes.Contains(b, []byte(fmt.Sprintf(`"bytes":%d`, size))) {
			t.Fatalf("100 MB upload: %d %s", resp.StatusCode, b)
		}
		req, _ = http.NewRequest("PUT", base+"/x/ez-terminal/s3/upload", io.LimitReader(zeroReader{}, size+1))
		req.ContentLength = size + 1
		req.Host = clientHost
		resp, err = cl.Do(req)
		if err == nil {
			b, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 413 || resp.Header.Get("Cache-Control") != "" || !strings.Contains(string(b), "413") {
				t.Errorf("413 by Content-Length: %d %v", resp.StatusCode, resp.Header)
			}
		} else if !strings.Contains(err.Error(), "broken pipe") && !strings.Contains(err.Error(), "reset") {
			t.Fatal(err)
		}
		req, _ = http.NewRequest("POST", base+"/x/ez-admin/upload", io.NopCloser(io.LimitReader(zeroReader{}, 2<<20)))
		req.ContentLength = -1
		req.Host = clientHost
		resp, err = cl.Do(req)
		switch {
		case err != nil && (strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "connection reset")):
			t.Logf("413 chunked: the client saw %v (edge closed early)", err)
		case err != nil:
			t.Fatal(err)
		default:
			b, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 413 || !strings.Contains(string(b), "413") {
				t.Errorf("413 chunked: %d %s", resp.StatusCode, b)
			}
		}
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
			DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(se.https))
			}}, Timeout: 10 * time.Second, CheckRedirect: noRedirect}
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
		if r, b2 := do("GET", "/x/admin/login", nil, nil); r.StatusCode != 200 {
			t.Errorf("http (no redirect): %d %s", r.StatusCode, b2)
		} else if uri, _ := echo(b2); uri != "/login" {
			t.Errorf("http uri %s", uri)
		}
	})
}
