package cloud

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type shot struct {
	status  int
	headers []string
	body    string
}

func fire(t *testing.T, base, method, path, body string, hdr []string) shot {
	t.Helper()
	r, err := http.NewRequest(method, base+path, strings.NewReader(body))
	must(t, err)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rs, err := c.Do(r)
	must(t, err)
	defer rs.Body.Close()
	b, _ := io.ReadAll(rs.Body)
	var h []string
	for k, v := range rs.Header {
		switch k {
		case "Date", "Connection", "Keep-Alive", "Transfer-Encoding", "Content-Length", "X-Request-Id", "X-Opm-Server-Time":
			continue
		}
		h = append(h, k+": "+strings.Join(v, ","))
	}
	sort.Strings(h)
	return shot{rs.StatusCode, h, string(b)}
}

// The same requests against the real TS server and the Go handler: same status, headers (spelling apart) and body bytes.
func TestHTTPParityWithTS(t *testing.T) {
	be := backend(t)
	port := freePort(t)
	tsURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	dir := t.TempDir()
	cmd := exec.Command("node", "--import", "tsx", "--no-warnings=ExperimentalWarning", "src/server.ts", "serve")
	cmd.Dir = be
	cmd.Env = append(os.Environ(), fmt.Sprintf("OPM_PORT=%d", port), "OPM_HOST=127.0.0.1", "OPM_DATA_DIR="+dir, "OPM_PUBLIC_URL=http://opm.test", "OPM_FRONTEND_ORIGINS=http://localhost:5173", "OPM_RUN_JOBS=0", "OPM_ADAPTER=firebase")
	must(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; ; i++ {
		if r, err := http.Get(tsURL + "/healthz"); err == nil {
			r.Body.Close()
			break
		}
		if i > 200 {
			t.Fatal("TS server did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Setenv("OPM_PUBLIC_URL", "http://opm.test")
	t.Setenv("OPM_CONSOLE_URL", "")
	t.Setenv("OPM_FRONTEND_ORIGINS", "http://localhost:5173")
	t.Setenv("OPM_DATA_DIR", t.TempDir())
	t.Setenv("OPM_SMTP_URL", "")
	t.Setenv("OPM_RELEASE_PUBLIC_KEY", "x")
	cfg, err := LoadConfig([]string{"opm-cloud"})
	must(t, err)
	must(t, ApplyEnv(cfg))
	p, err := InstallPorts(cfg)
	must(t, err)
	defer p.Close()
	srv := httptest.NewServer(NewHandler(cfg, p))
	defer srv.Close()

	type rq struct {
		m, p, b string
		h       []string
	}
	json := []string{"Content-Type", "application/json"}
	cases := []rq{
		{"GET", "/healthz", "", nil},
		{"HEAD", "/healthz", "", nil},
		{"GET", "/opm-config.json", "", nil},
		{"GET", "/nope", "", nil},
		{"POST", "/nope", "x", nil},
		{"GET", "/v1/nope", "", nil},
		{"GET", "/mount/v1/nope?x=1", "", nil},
		{"PATCH", "/v1/not-found-pages/abc", "", nil},
		{"PUT", "/v1/not-found-pages/abc", "", nil},
		{"HEAD", "/v1/me", "", nil},
		{"GET", "/v1/me", "", nil},
		{"GET", "/v1/me", "", []string{"Authorization", "Bearer nope"}},
		{"GET", "/v1/me", "", []string{"Authorization", "Bearer opi_x"}},
		{"GET", "/v1/me", "", []string{"Origin", "http://localhost:5173"}},
		{"GET", "/v1/me", "", []string{"Origin", "https://evil.example"}},
		{"OPTIONS", "/v1/agent/poll", "", []string{"Origin", "https://evil.example"}},
		{"OPTIONS", "/v1/me", "", []string{"Origin", "http://localhost:5173"}},
		{"OPTIONS", "/v1/me", "", []string{"Origin", "https://evil.example"}},
		{"OPTIONS", "/v1/icons/a/content", "", nil},
		{"GET", "/v1/agent/poll", "", nil},
		{"GET", "/v1/agent/poll", "", []string{"X-OPM-Site", "bad", "X-OPM-Ts", "1", "X-OPM-Nonce", "n", "X-OPM-Sig", "s"}},
		{"GET", "/v1/agent/poll", "", []string{"X-OPM-Site", "D1/site-a", "X-OPM-Ts", "1", "X-OPM-Nonce", "n", "X-OPM-Sig", "s"}},
		{"POST", "/v1/registries", `{"a":1}`, nil},
		{"GET", "/auth/config", "", nil},
		{"POST", "/auth/login", `{"email":"a@b.c","password":"x"}`, json},
		{"POST", "/auth/login", `{}`, json},
		{"POST", "/auth/login", `not json`, json},
		{"POST", "/auth/refresh", `{}`, json},
		{"POST", "/auth/refresh", `{"refreshToken":"x"}`, json},
		{"POST", "/auth/logout", `{}`, json},
		{"GET", "/auth/nope", "", nil},
		{"OPTIONS", "/auth/login", "", nil},
		{"GET", "/auth/reset/check?token=x", "", nil},
		{"GET", "/auth/reset/check", "", nil},
		{"POST", "/auth/reset/request", `{"email":"x@y.z"}`, json},
		{"POST", "/auth/reset/confirm", `{"token":"x","password":"short"}`, json},
		{"POST", "/auth/reset/confirm", `{"token":"x","password":"long-enough"}`, json},
		{"GET", "/auth/oidc/start", "", nil},
		{"GET", "/auth/oidc/callback", "", nil},
		{"POST", "/auth/oidc/exchange", `{"code":"x"}`, json},
		{"GET", "/_blob/x?exp=1&sig=a", "", nil},
		{"GET", "/_blob/..%2Fsecrets?exp=1&sig=a", "", nil},
		{"OPTIONS", "/_blob/x", "", []string{"Origin", "http://opm.test", "Access-Control-Request-Headers", "Range, X"}},
		{"OPTIONS", "/_blob/x", "", []string{"Origin", "https://evil.example"}},
		{"POST", "/_blob/x", "", nil},
		{"HEAD", "/_blob/x?exp=1&sig=a", "", nil},
	}
	for _, c := range cases {
		a := fire(t, tsURL, c.m, c.p, c.b, c.h)
		b := fire(t, srv.URL, c.m, c.p, c.b, c.h)
		if a.status != b.status || strings.Join(a.headers, "\n") != strings.Join(b.headers, "\n") || a.body != b.body {
			t.Errorf("%s %s %v\nTS: %d %v\n    %s\nGo: %d %v\n    %s", c.m, c.p, c.h, a.status, a.headers, a.body, b.status, b.headers, b.body)
		}
	}
}
