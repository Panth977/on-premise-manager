package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/docker"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestDevmeshDevToolsPresetsEndToEnd: `opm-devmesh -devtools` over the REAL agent HTTP API, the way the GUI uses it
// (§21 #97): the preset revision is visible as a proposal → approve (start + ACCEPT) → the head carries devTools →
// db's dev tools list the 5 presets (source preset) → SQL on the PostgreSQL preset (password = {{ secret.pg-pass }})
// succeeds → devtools.query on db, without the password. Also: a data dir from a previous run is reset (the root cause
// of "the preset revision never reached the agents": agents enrolled with an older in-memory fake cloud).
func TestDevmeshDevToolsPresetsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("docker (-short)")
	}
	if err := docker.New(docker.DefaultHost("")).Ping(context.Background()); err != nil {
		t.Skip("no docker")
	}
	o := defaultOptions()
	o.Dir = t.TempDir()
	o.ApproveWait = time.Second
	o.DevTools = true
	o.AppPort, o.AppAlias, o.DBPort, o.DBAlias, o.EdgePort = freePort(t), freePort(t), freePort(t), freePort(t), 0
	o.EdgePort = freePort(t)
	o.Out = io.Discard
	// a previous run's data must not survive (it would not be enrolled with the new cloud)
	run1, cancel1 := context.WithCancel(context.Background())
	o.DevTools = false
	m1, err := startMesh(run1, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitEnrolled(m1.fc, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	cancel1()
	m1.stop()
	o.DevTools = true
	ctx, cancel := context.WithCancel(context.Background())
	m, err := startMesh(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); m.stop() }()
	if m.revID == "" {
		t.Fatal("no preset revision published")
	}
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", o.AppAlias)
	actor := ""
	call := func(method, path, body string, out any) int {
		t.Helper()
		r, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if actor != "" && method != "GET" {
			r.Header.Set("Authorization", "Actor "+actor)
		}
		resp, err := cl.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if out != nil {
			_ = json.Unmarshal(b, out)
		}
		return resp.StatusCode
	}
	wait := func(what string, max time.Duration, fn func() bool) {
		t.Helper()
		deadline := time.Now().Add(max)
		for time.Now().Before(deadline) {
			if fn() {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("timeout: %s", what)
	}
	wait("GUI up", 30*time.Second, func() bool { return call("POST", "/_opm/api/login", `{"accessKey":"`+o.Key+`"}`, nil) == 200 })
	var ar struct {
		ActorToken string `json:"actorToken"`
	}
	call("POST", "/_opm/api/actor", `{"email":"gui-smoke@acme.example"}`, &ar)
	actor = ar.ActorToken
	// 1. the proposal is visible and stages ready on both sites
	wait("proposal ready", 90*time.Second, func() bool {
		var pl struct {
			Items []struct {
				Proposal struct {
					RevID  string `json:"revId"`
					Status string `json:"status"`
				} `json:"proposal"`
			} `json:"items"`
		}
		call("GET", "/_opm/api/proposals", "", &pl)
		return len(pl.Items) == 1 && pl.Items[0].Proposal.RevID == m.revID && pl.Items[0].Proposal.Status == "ready"
	})
	// 2. approve: start + ACCEPT after the (1 s) wait
	var st struct {
		Intent struct {
			IntentID string `json:"intentId"`
		} `json:"intent"`
	}
	if c := call("POST", "/_opm/api/proposals/"+m.revID+"/approve/start", "", &st); c != 200 && c != 201 {
		t.Fatalf("approve/start %d", c)
	}
	time.Sleep(1500 * time.Millisecond)
	if c := call("POST", "/_opm/api/proposals/"+m.revID+"/approve", `{"intentId":"`+st.Intent.IntentID+`","typed":"ACCEPT"}`, nil); c != 200 && c != 201 && c != 202 {
		t.Fatalf("approve %d", c)
	}
	// 3. the head carries devTools; db lists the 5 presets
	type conn struct {
		ID     string `json:"id"`
		Source string `json:"source"`
		Kind   string `json:"kind"`
	}
	var conns struct {
		Items []conn `json:"items"`
	}
	wait("presets on db", 90*time.Second, func() bool {
		call("GET", "/_opm/api/sites/db/devtools/connections", "", &conns)
		return len(conns.Items) == 5
	})
	for _, c := range conns.Items {
		if c.Source != "preset" || !strings.HasPrefix(c.ID, "p:") {
			t.Fatalf("not a preset: %+v", c)
		}
	}
	var cfg map[string]any
	call("GET", "/_opm/api/config", "", &cfg)
	if b, _ := json.Marshal(cfg); !strings.Contains(string(b), `"devTools"`) {
		t.Fatal("head config has no devTools")
	}
	// 4. SQL on the PostgreSQL preset (password from {{ secret.pg-pass }}, resolved on db)
	var res struct {
		OK      bool `json:"ok"`
		Results []struct {
			Rows [][]any `json:"rows"`
		} `json:"results"`
		ActivityID *string `json:"activityId"`
	}
	wait("pg answers", 60*time.Second, func() bool {
		call("POST", "/_opm/api/sites/db/devtools/connections/p:main-db/sql", `{"queryId":"`+fmt.Sprint(time.Now().UnixNano())+`","statement":"SELECT current_user, 40 + 2 AS answer"}`, &res)
		return res.OK
	})
	if len(res.Results) != 1 || res.Results[0].Rows[0][0] != "postgres" || res.Results[0].Rows[0][1].(float64) != 42 || res.ActivityID == nil {
		t.Fatalf("sql %+v", res)
	}
	// 5. devtools.query on db (read through app), never the password
	var acts struct {
		Items []map[string]any `json:"items"`
	}
	call("GET", "/_opm/api/sites/db/activities?type=devtools.query&limit=50", "", &acts)
	found := false
	for _, a := range acts.Items {
		b, _ := json.Marshal(a)
		if strings.Contains(string(b), "devmesh-pg-pass") {
			t.Fatalf("activity leaks the password: %s", b)
		}
		if a["type"] == "devtools.query" && strings.Contains(string(b), `"connectionId":"p:main-db"`) && strings.Contains(string(b), `"ok":true`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no devtools.query on db: %+v", acts.Items)
	}
}
