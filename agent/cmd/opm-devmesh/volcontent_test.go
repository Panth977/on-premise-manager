package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/docker"
)

// TestDevmeshVolumeContent: `opm-devmesh -volume-content` over the real agent API: approve the revision → app/flows
// holds its starter content and shows "No snapshot yet" → Snapshot now → the demo cloud edit arrives as a pending push
// (Cloud ahead) with a fast-forward plan.
func TestDevmeshVolumeContent(t *testing.T) {
	if testing.Short() {
		t.Skip("docker (-short)")
	}
	if err := docker.New(docker.DefaultHost("")).Ping(context.Background()); err != nil {
		t.Skip("no docker")
	}
	o := defaultOptions()
	o.Dir = t.TempDir()
	o.ApproveWait = time.Second
	o.VolumeContent = true
	o.AppPort, o.AppAlias, o.DBPort, o.DBAlias, o.EdgePort = freePort(t), freePort(t), freePort(t), freePort(t), freePort(t)
	o.Out = io.Discard
	ctx, cancel := context.WithCancel(context.Background())
	m, err := startMesh(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); m.stop() }()
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
		return len(pl.Items) == 1 && pl.Items[0].Proposal.Status == "ready"
	})
	var st struct {
		Intent struct {
			IntentID string `json:"intentId"`
		} `json:"intent"`
	}
	call("POST", "/_opm/api/proposals/"+m.revID+"/approve/start", "", &st)
	time.Sleep(1500 * time.Millisecond)
	if c := call("POST", "/_opm/api/proposals/"+m.revID+"/approve", `{"intentId":"`+st.Intent.IntentID+`","typed":"ACCEPT"}`, nil); c >= 300 {
		t.Fatalf("approve %d", c)
	}
	type status struct {
		State string `json:"state"`
		Push  *struct {
			PushID string `json:"pushId"`
		} `json:"push"`
	}
	var vs status
	wait("app/flows persisted", 90*time.Second, func() bool {
		return call("GET", "/_opm/api/sites/app/volume-sync/flows", "", &vs) == 200 && vs.State == "never-snapshotted"
	})
	if c := call("POST", "/_opm/api/sites/app/volume-sync/flows/snapshot", `{}`, nil); c != 201 {
		t.Fatalf("snapshot %d", c)
	}
	wait("demo push", 60*time.Second, func() bool {
		return call("GET", "/_opm/api/sites/app/volume-sync/flows", "", &vs) == 200 && vs.Push != nil
	})
	if vs.State != "cloud-ahead" {
		t.Fatalf("state %s", vs.State)
	}
	var plan struct {
		FastForward bool `json:"fastForward"`
		Items       []any
	}
	if c := call("GET", "/_opm/api/sites/app/volume-sync/flows/plan", "", &plan); c != 200 || !plan.FastForward || len(plan.Items) != 2 {
		t.Fatalf("plan %d %+v", c, plan)
	}
}
