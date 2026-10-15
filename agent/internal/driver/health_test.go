package driver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/model"
)

// fakeContainer is a scripted docker daemon for one container "c1": state(n) gives the n-th inspect answer.
type fakeContainer struct {
	inspects atomic.Int64
	state    func(n int64) docker.ContainerJSON
}

func newFakeDocker(t *testing.T, fc *fakeContainer) *docker.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"Version": "28.5.2", "ApiVersion": "1.51", "MinAPIVersion": "1.24"})
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/containers/c1/json"):
			n := fc.inspects.Add(1)
			cj := fc.state(n)
			cj.ID = "c1"
			_ = json.NewEncoder(w).Encode(cj)
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return docker.New("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
}

func cjRunning(ports map[string][]docker.PortBinding, restarts int) docker.ContainerJSON {
	var cj docker.ContainerJSON
	cj.State = docker.ContainerState{Status: "running", Running: true}
	cj.RestartCount = restarts
	cj.NetworkSettings.Ports = ports
	return cj
}

func loopBinding(port int) map[string][]docker.PortBinding {
	return map[string][]docker.PortBinding{"8080/tcp": {{HostIP: "0.0.0.0", HostPort: "6007"}, {HostIP: "127.0.0.1", HostPort: strconv.Itoa(port)}}}
}

// healthServer answers /health with the status code of code().
func healthServer(t *testing.T, code func() int) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code()) })}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func testBuilt(h *model.HealthCheck, restart string) *Built {
	b := &Built{ContainerSlug: "admin", Slug: "admin", Name: "opm_d_s_admin", Kind: "service", Enabled: true, Health: h,
		Ports: []PortRef{{Name: "server", ContainerPort: 8080, Protocol: "tcp", HostIP: "0.0.0.0"}}}
	b.Spec.Restart = restart
	return b
}

func httpCheck(interval, start float64, retries float64) *model.HealthCheck {
	return &model.HealthCheck{Type: "http", PortName: "server", Path: "/health", ExpectStatus: 200, IntervalSec: interval, TimeoutSec: 1, Retries: retries, StartPeriodSec: start}
}

func newTestDriver(t *testing.T, dc *docker.Client) *Driver {
	d := New(dc, "d", "s", t.TempDir(), dlog.New(t.TempDir(), "error", nil), 20*time.Second)
	d.NoneReadyAfter = 400 * time.Millisecond
	return d
}

// The user's case (§21 #87): the port map is empty at first (Docker restarting the container) → not ready yet inside the
// start period, then the loopback publication appears and the check passes. Never "not published on loopback".
func TestWaitCompletedLoopMapEmptyAtFirst(t *testing.T) {
	hp := healthServer(t, func() int { return 200 })
	fc := &fakeContainer{state: func(n int64) docker.ContainerJSON {
		if n <= 3 {
			cj := cjRunning(map[string][]docker.PortBinding{}, 0)
			cj.State.Restarting = n%2 == 1
			return cj
		}
		return cjRunning(loopBinding(hp), 1)
	}}
	d := newTestDriver(t, newFakeDocker(t, fc))
	if err := d.WaitCompleted(context.Background(), testBuilt(httpCheck(0.1, 2, 3), "always"), "c1"); err != nil {
		t.Fatalf("must become ready: %v", err)
	}
	hist := d.History("admin")
	if len(hist) < 4 || !hist[len(hist)-1].OK {
		t.Fatalf("history %+v", hist)
	}
	for _, r := range hist[:len(hist)-1] {
		if r.Counted {
			t.Fatalf("checks inside the start period must not count: %+v", r)
		}
		if strings.Contains(r.Message, "not published on loopback") {
			t.Fatalf("old misleading message: %s", r.Message)
		}
	}
	if !strings.Contains(hist[0].Message, "restarting") {
		t.Fatalf("first record should say restarting: %s", hist[0].Message)
	}
}

// A crash loop after the start period fails with the restart state, not with a port message.
func TestWaitCompletedCrashLoop(t *testing.T) {
	fc := &fakeContainer{state: func(n int64) docker.ContainerJSON {
		cj := cjRunning(map[string][]docker.PortBinding{}, int(n))
		cj.State.Restarting, cj.State.ExitCode = true, 3
		return cj
	}}
	d := newTestDriver(t, newFakeDocker(t, fc))
	err := d.WaitCompleted(context.Background(), testBuilt(httpCheck(0.1, 0.3, 3), "always"), "c1")
	if err == nil || !strings.Contains(err.Error(), "container is restarting: last exit code 3") || !strings.Contains(err.Error(), "3 checks failed in a row") {
		t.Fatalf("crash loop: %v", err)
	}
}

// Failures inside startPeriod do not count; retries = consecutive failures after it; the interval is respected.
func TestWaitCompletedStartPeriodRetriesInterval(t *testing.T) {
	var okAfter atomic.Int64
	okAfter.Store(time.Now().Add(600 * time.Millisecond).UnixNano())
	hp := healthServer(t, func() int {
		if time.Now().UnixNano() > okAfter.Load() {
			return 200
		}
		return 500
	})
	fc := &fakeContainer{state: func(int64) docker.ContainerJSON { return cjRunning(loopBinding(hp), 0) }}
	d := newTestDriver(t, newFakeDocker(t, fc))
	// retries 1, but the 500s happen inside the 1 s start period → ready
	if err := d.WaitCompleted(context.Background(), testBuilt(httpCheck(0.1, 1, 1), "always"), "c1"); err != nil {
		t.Fatalf("failures inside startPeriod must not count: %v", err)
	}

	// always 500: fails after startPeriod + retries × interval
	hp2 := healthServer(t, func() int { return 500 })
	fc2 := &fakeContainer{state: func(int64) docker.ContainerJSON { return cjRunning(loopBinding(hp2), 0) }}
	d2 := newTestDriver(t, newFakeDocker(t, fc2))
	t0 := time.Now()
	err := d2.WaitCompleted(context.Background(), testBuilt(httpCheck(0.2, 0.5, 3), "always"), "c1")
	el := time.Since(t0)
	if err == nil || !strings.Contains(err.Error(), "GET /health → 500") || !strings.Contains(err.Error(), "3 checks failed in a row") {
		t.Fatalf("always 500: %v", err)
	}
	counted := 0
	for _, r := range d2.History("admin") {
		if r.Counted {
			counted++
		}
	}
	if counted != 3 {
		t.Fatalf("counted %d checks, want 3", counted)
	}
	// ≥ startPeriod (0.5) + 2 intervals (0.4) between the 3 counted checks
	if el < 900*time.Millisecond {
		t.Fatalf("interval not respected: failed after %v", el)
	}
	// interval: ~ el / 0.2 s inspects, not a busy loop
	if n := fc2.inspects.Load(); n > int64(el/(150*time.Millisecond))+2 {
		t.Fatalf("%d inspects in %v: interval 0.2 s not respected", n, el)
	}
}

// type none (no health check): ready after running NoneReadyAfter without a restart; a restart resets the wait.
func TestWaitCompletedNone(t *testing.T) {
	var restarts atomic.Int64
	fc := &fakeContainer{state: func(n int64) docker.ContainerJSON {
		if n == 2 {
			restarts.Store(1)
		}
		return cjRunning(nil, int(restarts.Load()))
	}}
	d := newTestDriver(t, newFakeDocker(t, fc))
	t0 := time.Now()
	if err := d.WaitCompleted(context.Background(), testBuilt(nil, "always"), "c1"); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(t0); el < 1900*time.Millisecond {
		// interval 1 s for type none: inspect 1 (t=0), the restart seen at inspect 2 (t=1 s) resets the wait, ready at t=2 s
		t.Fatalf("ready too early: %v", el)
	}
	// explicit type "none" behaves the same
	fc2 := &fakeContainer{state: func(int64) docker.ContainerJSON { return cjRunning(nil, 0) }}
	d2 := newTestDriver(t, newFakeDocker(t, fc2))
	if err := d2.WaitCompleted(context.Background(), testBuilt(&model.HealthCheck{Type: "none"}, "always"), "c1"); err != nil {
		t.Fatal(err)
	}
}

// LoopPortsOf: 127.0.0.1 wins; else a publication on every address is reached via 127.0.0.1; else nothing.
func TestLoopPortsFallback(t *testing.T) {
	b := testBuilt(nil, "always")
	cases := []struct {
		ports map[string][]docker.PortBinding
		want  int
	}{
		{loopBinding(32768), 32768},
		{map[string][]docker.PortBinding{"8080/tcp": {{HostIP: "0.0.0.0", HostPort: "6007"}}}, 6007},
		{map[string][]docker.PortBinding{"8080/tcp": {{HostIP: "", HostPort: "6008"}}}, 6008},
		{map[string][]docker.PortBinding{"8080/tcp": {{HostIP: "::", HostPort: "6009"}}}, 6009},
		{map[string][]docker.PortBinding{"8080/tcp": {{HostIP: "10.0.0.5", HostPort: "6010"}}}, 0},
		{map[string][]docker.PortBinding{}, 0},
	}
	for i, c := range cases {
		cj := cjRunning(c.ports, 0)
		if got := LoopPortsOf(&cj, b)["server"]; got != c.want {
			t.Errorf("case %d: %d want %d", i, got, c.want)
		}
	}
	cj := cjRunning(map[string][]docker.PortBinding{}, 0)
	if PortMapText(&cj) != "none" {
		t.Fatal(PortMapText(&cj))
	}
	cj = cjRunning(loopBinding(1), 0)
	if PortMapText(&cj) != "8080/tcp=0.0.0.0:6007,127.0.0.1:1" {
		t.Fatal(PortMapText(&cj))
	}
}
