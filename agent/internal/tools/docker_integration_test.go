package tools

// Docker integration (real daemon, e.g. Colima): exec terminal in busybox, logs follow (SSE), stats sampling,
// netcheck from inside a container, stop/start/restart + manuallyStopped, inspect masking, top, events, images.
// Skipped with -short or when no Docker daemon answers.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/metrics"
)

func dockerOrSkip(t *testing.T) *docker.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("docker integration (-short)")
	}
	dc := docker.New(docker.DefaultHost(""))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dc.Ping(ctx); err != nil {
		t.Skip("no docker daemon: " + err.Error())
	}
	return dc
}

func startBusybox(t *testing.T, dc *docker.Client, name string, env []string, script string) string {
	t.Helper()
	ctx := context.Background()
	_ = dc.ContainerRemove(ctx, name, true)
	img := "busybox:1.36"
	if _, err := dc.ImageInspect(ctx, img); err != nil {
		img = "busybox:latest"
	}
	id, err := dc.ContainerCreate(ctx, name, &docker.ContainerConfig{Image: img, Cmd: []string{"sh", "-c", script}, Env: env,
		Labels: map[string]string{"opm.test": "tools"}, HostConfig: docker.HostConfig{RestartPolicy: docker.RestartPolicy{Name: "no"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dc.ContainerRemove(context.Background(), id, true) })
	if err := dc.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDockerTools(t *testing.T) {
	dc := dockerOrSkip(t)
	h := newFakeHost(t)
	h.dc = dc
	h.secrets = []string{"supersecret99"}
	name := fmt.Sprintf("opm-tools-it-%d", time.Now().UnixNano()%1e6)
	id := startBusybox(t, dc, name, []string{"API_TOKEN=abc123456", "PLAIN=hello", "DB_URL=postgres://u:supersecret99@db/x"},
		`mkdir -p /www && echo ok > /www/index.html && httpd -p 8080 -h /www; i=0; while true; do echo "line $i"; echo "err $i" >&2; i=$((i+1)); sleep 0.2; done`)
	h.targets = []Target{{Ref: "web", ContainerSlug: "web", ServiceSlug: "web", Name: name, ID: id, Image: "busybox", Enabled: true, Kind: "service",
		SecretValues: []string{"supersecret99"}}}
	s := New(h)
	srv := serve(t, s)
	c := call{actor: "ops@example.com", session: "S1"}
	time.Sleep(700 * time.Millisecond)

	t.Run("exec terminal", func(t *testing.T) {
		var cr map[string]any
		if st := doJSON(t, srv, c, "POST", "/terminal/sessions", map[string]any{"kind": "container", "containerSlug": "web", "shell": "sh"}, &cr); st != 201 {
			t.Fatalf("create %d %v", st, cr)
		}
		ws, _, err := DialWS("ws"+strings.TrimPrefix(srv.URL, "http")+cr["wsPath"].(string), http.Header{"X-Test-Session": {"S1"}})
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		_ = ws.WriteMessage(OpText, []byte(`{"type":"resize","cols":100,"rows":30}`))
		_ = ws.WriteMessage(OpBinary, []byte("echo hi-$((40+2)); stty size; exit 7\n"))
		var out strings.Builder
		exit := ""
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) && exit == "" {
			op, msg, err := ws.ReadMessage()
			if err != nil {
				break
			}
			if op == OpBinary {
				out.Write(msg)
			} else if strings.Contains(string(msg), `"exit"`) {
				exit = string(msg)
			}
		}
		if !strings.Contains(out.String(), "hi-42") || !strings.Contains(out.String(), "30 100") {
			t.Fatalf("exec output %q", out.String())
		}
		if !strings.Contains(exit, `"code":7`) {
			t.Fatalf("exit %q", exit)
		}
		time.Sleep(200 * time.Millisecond)
		if len(h.activities("terminal.opened")) != 1 || len(h.activities("terminal.closed")) != 1 {
			t.Fatal("terminal activities")
		}
	})

	t.Run("logs", func(t *testing.T) {
		lines := readSSE(t, srv.URL+"/_opm/api/sites/app/tools/logs/containers?containers=web&tail=6", 10*time.Second, 0)
		if len(lines) < 6 || !strings.Contains(strings.Join(lines, "\n"), `"s":"stderr"`) {
			t.Fatalf("tail lines %v", lines)
		}
		follow := readSSE(t, srv.URL+"/_opm/api/sites/app/tools/logs/containers?containers=web&tail=1&follow=1&stderr=0", 2500*time.Millisecond, 0)
		if len(follow) < 5 || strings.Contains(strings.Join(follow, "\n"), `"s":"stderr"`) {
			t.Fatalf("follow got %d lines: %v", len(follow), follow)
		}
		req, _ := http.NewRequest("GET", srv.URL+"/_opm/api/sites/app/tools/logs/containers/download?containers=web&tail=4", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), ".log") {
			t.Fatal(err)
		}
		resp.Body.Close()
	})

	t.Run("stats sampling", func(t *testing.T) {
		m := s.Metrics
		m.Interval = time.Second
		m.SampleOnce(context.Background())
		time.Sleep(1100 * time.Millisecond)
		m.SampleOnce(context.Background())
		l := m.Latest("web")
		if l == nil || l.CPUPct == nil || l.MemBytes == nil || *l.MemBytes <= 0 || l.NetRxBps == nil {
			t.Fatalf("latest %+v", l)
		}
		var sum metrics.Summary
		doJSON(t, srv, c, "GET", "/metrics/summary", nil, &sum)
		if len(sum.Containers) != 1 || sum.Containers[0].Ref != "web" || len(sum.Containers[0].Spark.CPU) != 60 {
			t.Fatalf("summary %+v", sum)
		}
		var ser metrics.Series
		doJSON(t, srv, c, "GET", "/metrics/series?subject=container:web&range=1h", nil, &ser)
		if len(ser.T) != 2 || len(ser.Series["cpuPct"]) != 2 {
			t.Fatalf("series %+v", ser)
		}
	})

	t.Run("netcheck from container", func(t *testing.T) {
		var res NetResult
		doJSON(t, srv, c, "POST", "/netcheck", map[string]any{"from": map[string]string{"kind": "container", "containerSlug": "web"}, "check": "http", "target": "http://127.0.0.1:8080/index.html", "timeoutSec": 5}, &res)
		if !res.OK || res.Status == nil || *res.Status != 200 || res.Tool != "wget" {
			t.Fatalf("http %+v", res)
		}
		doJSON(t, srv, c, "POST", "/netcheck", map[string]any{"from": map[string]string{"kind": "container", "containerSlug": "web"}, "check": "tcp", "target": "127.0.0.1:8080", "timeoutSec": 3}, &res)
		if res.Tool != "nc" || !res.OK {
			t.Fatalf("tcp %+v", res)
		}
		doJSON(t, srv, c, "POST", "/netcheck", map[string]any{"from": map[string]string{"kind": "container", "containerSlug": "web"}, "check": "ping", "target": "127.0.0.1"}, &res)
		if res.Tool != "ping" {
			t.Fatalf("ping %+v", res)
		}
	})

	t.Run("inspect top events", func(t *testing.T) {
		var ins map[string]any
		doJSON(t, srv, c, "GET", "/containers/web/inspect", nil, &ins)
		b, _ := json.Marshal(ins)
		if strings.Contains(string(b), "abc123456") || strings.Contains(string(b), "supersecret99") || !strings.Contains(string(b), "PLAIN=hello") {
			t.Fatalf("inspect masking: %s", b)
		}
		masked := fmt.Sprint(ins["maskedEnv"])
		if masked != "[API_TOKEN DB_URL]" {
			t.Fatalf("maskedEnv %s", masked)
		}
		var top ContainerTopResponseT
		doJSON(t, srv, c, "GET", "/containers/web/top", nil, &top)
		if len(top.Titles) == 0 || len(top.Processes) == 0 {
			t.Fatalf("top %+v", top)
		}
		var ev struct {
			Items []map[string]any `json:"items"`
		}
		doJSON(t, srv, c, "GET", "/containers/web/events", nil, &ev)
		if !strings.Contains(fmt.Sprint(ev.Items), "start") {
			t.Fatalf("events %+v", ev.Items)
		}
	})

	t.Run("actions + manuallyStopped", func(t *testing.T) {
		var r map[string]any
		if st := doJSON(t, srv, c, "POST", "/containers/web/stop", map[string]any{"reason": "maintenance"}, &r); st != 200 {
			t.Fatalf("stop %d %v", st, r)
		}
		cont := r["container"].(map[string]any)
		if cont["state"] != "exited" || cont["manuallyStopped"] == nil || cont["expectedRunning"] != true {
			t.Fatalf("after stop %+v", cont)
		}
		if !s.ManuallyStopped("web") {
			t.Fatal("ManuallyStopped")
		}
		h.seq++ // a new head (next update) ends the mark
		var list struct {
			Items []map[string]any `json:"items"`
		}
		doJSON(t, srv, c, "GET", "/containers", nil, &list)
		if list.Items[0]["manuallyStopped"] != nil {
			t.Fatal("a new head must clear manuallyStopped")
		}
		doJSON(t, srv, c, "POST", "/containers/web/stop", nil, &r)
		doJSON(t, srv, c, "POST", "/containers/web/start", nil, &r)
		if cont := r["container"].(map[string]any); cont["state"] != "running" || cont["manuallyStopped"] != nil {
			t.Fatalf("after start %+v", cont)
		}
		// §21 #110: a running update is a warning, never a lock — container actions are not refused
		h.locked = true
		if st := doJSON(t, srv, c, "POST", "/containers/web/restart", nil, &r); st != 200 {
			t.Fatal("restart during an update must not be refused", st)
		}
		h.locked = false
		if st := doJSON(t, srv, c, "POST", "/containers/web/restart", nil, &r); st != 200 {
			t.Fatal("restart", st)
		}
		for _, typ := range []string{"container.stopped", "container.started", "container.restarted"} {
			if len(h.activities(typ)) == 0 {
				t.Fatal("missing activity", typ)
			}
		}
		if a := h.activities("container.stopped"); a[0].Data["reason"] != "maintenance" || a[0].Data["expectedRunning"] != true {
			t.Fatalf("stopped data %+v", a[0].Data)
		}
	})

	t.Run("images", func(t *testing.T) {
		var im map[string]any
		doJSON(t, srv, c, "GET", "/docker/images", nil, &im)
		items, _ := im["items"].([]any)
		found := false
		for _, x := range items {
			row := x.(map[string]any)
			if strings.Contains(fmt.Sprint(row["tags"]), "busybox") && row["inUse"] == true {
				found = true
			}
		}
		if !found || im["df"] == nil {
			t.Fatalf("images %v", im)
		}
		var e map[string]any
		if st := doJSON(t, srv, c, "POST", "/docker/prune-images", map[string]any{"typed": "prune"}, &e); st != 422 {
			t.Fatal("typed PRUNE required", st)
		}
	})
}

// ContainerTopResponseT decodes the top answer in tests.
type ContainerTopResponseT struct {
	Titles    []string   `json:"titles"`
	Processes [][]string `json:"processes"`
}

// readSSE collects `line` events until `end`, the timeout, or max lines (0 = no max).
func readSSE(t *testing.T, url string, timeout time.Duration, max int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type %s", resp.Header.Get("Content-Type"))
	}
	var out []string
	sc := bufio.NewScanner(resp.Body)
	ev := ""
	for sc.Scan() {
		ln := sc.Text()
		switch {
		case strings.HasPrefix(ln, "event: "):
			ev = strings.TrimPrefix(ln, "event: ")
		case strings.HasPrefix(ln, "data: "):
			if ev == "line" {
				out = append(out, strings.TrimPrefix(ln, "data: "))
				if max > 0 && len(out) >= max {
					return out
				}
			}
			if ev == "end" {
				return out
			}
		}
	}
	return out
}
