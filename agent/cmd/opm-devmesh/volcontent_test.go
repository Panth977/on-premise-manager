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
// holds its committed content (in sync) → the demo cloud edit arrives as the next revision → approving it writes the
// edited content into the live volume (§21 #117).
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
	approve := func(revID string) {
		t.Helper()
		wait("proposal "+revID+" ready", 90*time.Second, func() bool {
			var pl struct {
				Items []struct {
					Proposal struct {
						RevID  string `json:"revId"`
						Status string `json:"status"`
					} `json:"proposal"`
				} `json:"items"`
			}
			call("GET", "/_opm/api/proposals", "", &pl)
			for _, it := range pl.Items {
				if (revID == "" || it.Proposal.RevID == revID) && it.Proposal.Status == "ready" {
					revID = it.Proposal.RevID
					return true
				}
			}
			return false
		})
		var st struct {
			Intent struct {
				IntentID string `json:"intentId"`
			} `json:"intent"`
		}
		call("POST", "/_opm/api/proposals/"+revID+"/approve/start", "", &st)
		time.Sleep(1500 * time.Millisecond)
		if c := call("POST", "/_opm/api/proposals/"+revID+"/approve", `{"intentId":"`+st.Intent.IntentID+`","typed":"ACCEPT"}`, nil); c >= 300 {
			t.Fatalf("approve %d", c)
		}
	}
	type status struct {
		State     string `json:"state"`
		Committed *struct {
			FileCount int `json:"fileCount"`
		} `json:"committed"`
	}
	var vs status
	approve(m.revID)
	wait("app/flows in sync with its committed content", 90*time.Second, func() bool {
		return call("GET", "/_opm/api/sites/app/volume-sync/flows", "", &vs) == 200 && vs.State == "in-sync" && vs.Committed != nil && vs.Committed.FileCount == 3
	})
	approve("") // the demo cloud edit
	wait("the edited content written", 120*time.Second, func() bool {
		return call("GET", "/_opm/api/sites/app/volume-sync/flows", "", &vs) == 200 && vs.State == "in-sync" && vs.Committed != nil && vs.Committed.FileCount == 4
	})
}
