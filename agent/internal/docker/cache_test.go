package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countDaemon: n containers, a streaming /events, counts every request by kind.
type countDaemon struct {
	n      int
	mu     sync.Mutex
	counts map[string]int
	events chan Event
	state  map[string]string // id → State
}

func newCountDaemon(n int) (*countDaemon, *httptest.Server) {
	f := &countDaemon{n: n, counts: map[string]int{}, events: make(chan Event, 16), state: map[string]string{}}
	for i := 0; i < n; i++ {
		f.state[fmt.Sprintf("c%02d", i)] = "running"
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	return f, srv
}

func (f *countDaemon) count(k string) {
	f.mu.Lock()
	f.counts[k]++
	f.mu.Unlock()
}

func (f *countDaemon) total(skip ...string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := 0
	for k, v := range f.counts {
		if k == "version" || k == "events" {
			continue
		}
		t += v
	}
	return t
}

func (f *countDaemon) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if i := strings.Index(p[1:], "/"); strings.HasPrefix(p, "/v1.") && i > 0 {
		p = p[1+i:]
	}
	switch {
	case p == "/version":
		f.count("version")
		_, _ = w.Write([]byte(`{"Version":"27.3.1","ApiVersion":"1.47","MinAPIVersion":"1.24"}`))
	case p == "/events":
		f.count("events")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		fl.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-f.events:
				b, _ := json.Marshal(e)
				_, _ = w.Write(append(b, '\n'))
				fl.Flush()
			}
		}
	case p == "/containers/json":
		f.count("list")
		f.mu.Lock()
		var out []ContainerSummary
		for i := 0; i < f.n; i++ {
			id := fmt.Sprintf("c%02d", i)
			out = append(out, ContainerSummary{ID: id, Names: []string{"/" + id}, State: f.state[id], Labels: map[string]string{"opm.site": "app1"}})
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasSuffix(p, "/stats"):
		f.count("stats")
		_, _ = w.Write([]byte(`{"read":"2026-10-14T00:00:00Z","cpu_stats":{"cpu_usage":{"total_usage":1},"online_cpus":2},"memory_stats":{"usage":1}}`))
	case strings.HasPrefix(p, "/containers/") && strings.HasSuffix(p, "/json"):
		f.count("inspect")
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/containers/"), "/json")
		f.mu.Lock()
		st := f.state[id]
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Name": "/" + id, "State": map[string]any{"Status": st, "Running": st == "running"}})
	case strings.HasSuffix(p, "/restart"):
		f.count("write")
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

// oneMinute replays the per-minute access pattern of the agent loops before §21 #112 (14 managed containers):
// monitor every 5 s (list + inspect each), metrics every 15 s (2 lists + inspect + stats each), drift every 60 s
// (list all + inspect each). The clock advances 5 s per step; one container restarts at 32 s (an event).
func oneMinute(t *testing.T, c *Client, f *countDaemon, clock *atomic.Int64, withEvent bool) {
	ctx := context.Background()
	ids := []string{}
	for i := 0; i < f.n; i++ {
		ids = append(ids, fmt.Sprintf("c%02d", i))
	}
	inspectAll := func() {
		for _, id := range ids {
			if _, err := c.ContainerInspect(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	for s := 0; s < 60; s += 5 {
		clock.Store(int64(s))
		if withEvent && s == 35 { // the restart happened at 32 s
			f.mu.Lock()
			f.state["c03"] = "restarting"
			f.mu.Unlock()
			f.events <- Event{Type: "container", Action: "restart", Actor: struct {
				ID         string            `json:"ID"`
				Attributes map[string]string `json:"Attributes"`
			}{ID: "c03", Attributes: map[string]string{"name": "c03"}}}
			time.Sleep(50 * time.Millisecond) // the stream delivers it
		}
		// monitor
		if _, err := c.ContainerList(ctx, true, "opm.site=app1"); err != nil {
			t.Fatal(err)
		}
		inspectAll()
		if s%15 == 0 { // metrics
			_, _ = c.ContainerList(ctx, true, "opm.site=app1")
			_, _ = c.ContainerList(ctx, true, "opm.site=app1", "opm.deployment=d")
			inspectAll()
			for _, id := range ids {
				if _, err := c.StatsOneShot(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
		}
		if s == 0 { // drift
			_, _ = c.ContainerList(ctx, true)
			inspectAll()
		}
	}
}

func TestCacheCallsPerMinute(t *testing.T) {
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	nowFn = func() time.Time { return base.Add(time.Duration(clock.Load()) * time.Second) }
	defer func() { nowFn = time.Now }()

	// before: no cache
	f1, s1 := newCountDaemon(14)
	defer s1.Close()
	c1 := New("tcp://" + strings.TrimPrefix(s1.URL, "http://"))
	oneMinute(t, c1, f1, &clock, false)
	before := f1.total()

	// after: the event-driven cache
	f2, s2 := newCountDaemon(14)
	defer s2.Close()
	c2 := New("tcp://" + strings.TrimPrefix(s2.URL, "http://"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c2.StartCache(ctx, nil)
	select {
	case <-c2.stateCache().connected:
	case <-time.After(5 * time.Second):
		t.Fatal("events stream did not connect")
	}
	oneMinute(t, c2, f2, &clock, true)
	after := f2.total()
	t.Logf("Docker API calls per minute, 14 containers: before %d (lists %d, inspects %d, stats %d) → after %d (lists %d, inspects %d, stats %d) + 1 long-lived events stream",
		before, f1.counts["list"], f1.counts["inspect"], f1.counts["stats"], after, f2.counts["list"], f2.counts["inspect"], f2.counts["stats"])
	if after >= before/2 {
		t.Fatalf("cache should at least halve the calls: %d → %d", before, after)
	}
	// the restart event dropped c03: the next inspect saw the new state (Docker stays the source of truth)
	cj, err := c2.ContainerInspect(context.Background(), "c03")
	if err != nil || cj.State.Status != "restarting" {
		t.Fatalf("stale inspect after an event: %+v %v", cj, err)
	}
	// a write through the client drops the entry at once
	f2.mu.Lock()
	f2.state["c05"] = "exited"
	f2.mu.Unlock()
	if err := c2.ContainerRestart(context.Background(), "c05", 1); err != nil {
		t.Fatal(err)
	}
	if cj, _ := c2.ContainerInspect(context.Background(), "c05"); cj.State.Status != "exited" {
		t.Fatalf("stale inspect after a write: %s", cj.State.Status)
	}
	// stream down → no caching
	c2.stateCache().reset(false)
	n := f2.counts["inspect"]
	_, _ = c2.ContainerInspect(context.Background(), "c01")
	_, _ = c2.ContainerInspect(context.Background(), "c01")
	if f2.counts["inspect"] != n+2 {
		t.Fatalf("cache served while the stream is down")
	}
}

func TestDefaultTimeout(t *testing.T) {
	cases := []struct {
		m, p, q string
		want    time.Duration
	}{
		{"GET", "/containers/json", "", 15 * time.Second},
		{"GET", "/events", "", 0},
		{"GET", "/events", "until=5", 2 * time.Minute},
		{"GET", "/containers/x/stats", "stream=0", 10 * time.Second},
		{"GET", "/containers/x/logs", "follow=1", 0},
		{"POST", "/containers/x/wait", "", 0},
		{"POST", "/images/load", "", 0},
		{"POST", "/containers/x/stop", "t=10", 70 * time.Second},
		{"POST", "/containers/create", "", 2 * time.Minute},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.q)
		if got := DefaultTimeout(c.m, c.p, q); got != c.want {
			t.Errorf("%s %s?%s = %s, want %s", c.m, c.p, c.q, got, c.want)
		}
	}
}
