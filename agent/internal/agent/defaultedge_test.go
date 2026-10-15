package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"opm/agent/internal/edge"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// newEdgeAgent: a unit agent whose default edge listens on edgePort (production default 80; §21 #86).
func newEdgeAgent(t *testing.T, edgePort int, tm Timings) *Agent {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Options{Config: model.AgentConfigFile{APIBase: "http://127.0.0.1:1", DeploymentID: "DepDefEdge", SiteSlug: "app", DataDir: dir, AdvertiseHost: "127.0.0.1",
		AgentPort: freePort(t), GuiAliasPort: model.Some(freePort(t)), EdgeHTTPPort: model.Some(edgePort)}, Timings: tm})
	if err != nil {
		t.Fatal(err)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	edge.Register(a.instance, a)
	t.Cleanup(func() {
		a.cancel()
		a.edgeStopForTest()
		edge.Unregister(a.instance)
		a.db.Close()
	})
	return a
}

func (a *Agent) edgeStopForTest() {
	a.mu.Lock()
	a.cfg.EdgeHTTPPort = model.Null[int]()
	a.mu.Unlock()
	c := validate.EmptyConfig()
	_ = a.reloadEdge(&c, emptyCatalog(), 0, false) // nil input → this instance leaves the shared Caddy
}

// siteConfigWithEdge: this site with an edge on httpPort, hosts "*" (TLS none) and no routes.
func siteConfigWithEdge(httpPort int, httpsPort *int) *model.Config {
	c := validate.EmptyConfig()
	sc := validate.DefaultSiteConfig(nil)
	sc.Edge.HTTPPort = httpPort
	sc.Edge.HTTPSPort = httpsPort
	sc.Edge.Hosts = []model.EdgeHost{{Hostname: "*", TLS: model.EdgeTls{Mode: "none"}}}
	sc.Edge.Routes = []model.EdgeRoute{}
	c.Sites = append(c.Sites, model.Site{SiteSlug: "app", Label: "App", Config: sc})
	return &c
}

func get(t *testing.T, url string) (int, string, string) {
	t.Helper()
	cl := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := cl.Get(url)
	if err != nil {
		return 0, err.Error(), ""
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header.Get("Location")
}

func TestDefaultEdgeBoundWithoutHead(t *testing.T) {
	p := freePort(t)
	a := newEdgeAgent(t, p, fastTimings())
	a.bindInitial()
	base := fmt.Sprintf("http://127.0.0.1:%d", p)
	if c, _, loc := get(t, base+"/"); c != 302 || loc != "/_opm/app" {
		t.Fatalf("/ → %d %q", c, loc)
	}
	if c, b, _ := get(t, base+"/_opm/api/info"); c != 200 || !strings.Contains(b, `"surface":"edge"`) {
		t.Fatalf("info → %d %s", c, b)
	}
	if c, b, _ := get(t, base+"/_opm/app"); c != 200 || !strings.Contains(b, "<") {
		t.Fatalf("GUI → %d", c)
	}
	if c, b, _ := get(t, base+"/anything/else"); c != 404 || !strings.Contains(b, "no applied configuration yet") || !strings.Contains(b, "/_opm/") {
		t.Fatalf("not-found → %d %s", c, b)
	}
	r := a.edgeReport()
	if !r.DefaultEdge || len(r.BindErrors) != 0 {
		t.Fatalf("report %+v", r)
	}
	gp := a.guiPorts()
	if len(gp) < 3 || gp[0] != (model.GuiPort{Port: p, Scheme: "http", Kind: "edge"}) || gp[1].Kind != "gui-alias" || gp[2].Kind != "agent-port" {
		t.Fatalf("guiPorts %+v", gp)
	}
	a.mu.Lock()
	https := a.edgeHTTPS
	a.mu.Unlock()
	if https != nil {
		t.Fatal("the default edge must not bind 443 (no TLS host)")
	}
}

func TestEdgeWithEmptyConfigKeepsPort(t *testing.T) {
	p := freePort(t)
	a := newEdgeAgent(t, p, fastTimings())
	a.bindInitial()
	https := freePort(t)
	// a head whose edge has a "*" host without TLS and zero routes: same port, GUI still on /_opm/, rest → site 404
	if err := a.reloadEdge(siteConfigWithEdge(p, &https), emptyCatalog(), 1, true); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", p)
	if c, _, _ := get(t, base+"/_opm/healthz"); c != 200 {
		t.Fatalf("healthz %d", c)
	}
	if c, b, _ := get(t, base+"/x"); c != 404 || strings.Contains(b, "no applied configuration yet") {
		t.Fatalf("site not-found → %d %s", c, b)
	}
	if r := a.edgeReport(); r.DefaultEdge {
		t.Fatal("defaultEdge must be false with a head")
	}
	if ln, err := net.Listen("tcp", fmt.Sprintf(":%d", https)); err != nil {
		t.Fatalf("443-equivalent must stay unbound without a TLS host: %v", err)
	} else {
		ln.Close()
	}
	// rollback to "no configuration" → the default edge again, same port
	c := validate.EmptyConfig()
	if err := a.reloadEdge(&c, emptyCatalog(), 0, false); err != nil {
		t.Fatal(err)
	}
	if c, b, _ := get(t, base+"/x"); c != 404 || !strings.Contains(b, "no applied configuration yet") {
		t.Fatalf("after rollback → %d %s", c, b)
	}
}

// TestEdgeSwapUnderLoad: config swaps (default edge ↔ site config, repeatedly) never refuse a connection.
func TestEdgeSwapUnderLoad(t *testing.T) {
	p := freePort(t)
	a := newEdgeAgent(t, p, fastTimings())
	a.bindInitial()
	url := fmt.Sprintf("http://127.0.0.1:%d/_opm/healthz", p)
	var ok, refused, other atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr := &http.Transport{DisableKeepAlives: w%2 == 0}
			cl := &http.Client{Timeout: 5 * time.Second, Transport: tr}
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := cl.Get(url)
				if err != nil {
					if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused") {
						refused.Add(1)
					} else {
						other.Add(1)
						if os.Getenv("OPM_SWAP_DEBUG") != "" {
							t.Logf("keepalive=%v error: %v", w%2 != 0, err)
						}
					}
					continue
				}
				_, _ = io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode == 200 {
					ok.Add(1)
				} else {
					other.Add(1)
				}
			}
		}()
	}
	empty := validate.EmptyConfig()
	for i := 0; i < 20; i++ {
		if err := a.reloadEdge(siteConfigWithEdge(p, nil), emptyCatalog(), i+1, true); err != nil {
			t.Fatalf("swap %d: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
		if err := a.reloadEdge(&empty, emptyCatalog(), 0, false); err != nil {
			t.Fatalf("swap back %d: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	t.Logf("ok=%d refused=%d other=%d", ok.Load(), refused.Load(), other.Load())
	if refused.Load() != 0 {
		t.Fatalf("%d connections refused during config swaps", refused.Load())
	}
	// Caddy swaps the listening socket (SO_REUSEPORT) without a gap: no refused connections. Requests caught exactly
	// in the old server's shutdown may see EOF / reset (keep-alive closed, old accept queue) — bounded here at 2 %.
	if other.Load()*50 > ok.Load() {
		t.Fatalf("too many failed requests during swaps: ok=%d other=%d", ok.Load(), other.Load())
	}
	if ok.Load() < 100 {
		t.Fatalf("too few successful requests: %d", ok.Load())
	}
}

func TestEdgePortBusyRetry(t *testing.T) {
	hold, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	p := hold.Addr().(*net.TCPAddr).Port
	tm := fastTimings()
	tm.EdgeRetry = 200 * time.Millisecond
	a := newEdgeAgent(t, p, tm)
	a.bindInitial()
	r := a.edgeReport()
	if len(r.BindErrors) != 1 || r.BindErrors[0].Port != p || r.BindErrors[0].Purpose != "edge-http" {
		t.Fatalf("bind errors %+v", r.BindErrors)
	}
	for _, g := range a.guiPorts() {
		if g.Kind == "edge" {
			t.Fatal("busy edge port reported as a GUI port")
		}
	}
	// the GUI stays on the alias + agent ports
	if l := a.listeners["gui-alias"]; l == nil {
		t.Fatal("alias listener missing")
	}
	// retries do not repeat the activity
	a.edgeRetryOnce(context.Background())
	a.edgeRetryOnce(context.Background())
	if page, _ := a.activitiesPage(1<<30, 500, "agent.port-bind-failed", ""); len(page) != 1 {
		t.Fatalf("agent.port-bind-failed activities: %d (want 1)", len(page))
	}
	// the loop binds the port once it is free
	hold.Close()
	a.every("edge-retry", a.tm.EdgeRetry, a.edgeRetryOnce)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r := a.edgeReport(); len(r.BindErrors) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if r := a.edgeReport(); len(r.BindErrors) != 0 {
		t.Fatalf("still busy after the port freed: %+v", r.BindErrors)
	}
	if c, _, loc := get(t, fmt.Sprintf("http://127.0.0.1:%d/", p)); c != 302 || loc != "/_opm/app" {
		t.Fatalf("default edge after retry: %d %q", c, loc)
	}
}
