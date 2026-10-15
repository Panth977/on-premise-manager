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
	"opm/agent/internal/model"
)

// TestDevmeshBranches: `opm-devmesh -branches` over the real agent API — the cloud head appears on the Branch page,
// commit 1 is merged (fast-forward), commit 2 is cherry-picked (motd only) and the history shows both commits.
func TestDevmeshBranches(t *testing.T) {
	if testing.Short() {
		t.Skip("docker (-short)")
	}
	if err := docker.New(docker.DefaultHost("")).Ping(context.Background()); err != nil {
		t.Skip("no docker")
	}
	o := defaultOptions()
	o.Dir = t.TempDir()
	o.Branches = true
	o.AppPort, o.AppAlias, o.DBPort, o.DBAlias, o.EdgePort = freePort(t), freePort(t), freePort(t), freePort(t), freePort(t)
	o.Out = io.Discard
	ctx, cancel := context.WithCancel(context.Background())
	m, err := startMesh(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); m.stop() }()
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 5 * time.Minute}
	base := fmt.Sprintf("http://127.0.0.1:%d", o.AppAlias)
	actor := ""
	call := func(method, path string, body any, out any) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
		r, _ := http.NewRequest(method, base+path, rd)
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
		return resp.StatusCode, string(b)
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
	wait("GUI up", 30*time.Second, func() bool {
		c, _ := call("POST", "/_opm/api/login", map[string]string{"accessKey": o.Key}, nil)
		return c == 200
	})
	var ar model.ActorResponse
	call("POST", "/_opm/api/actor", map[string]string{"email": "gui-smoke@acme.example"}, &ar)
	actor = ar.ActorToken
	commits := m.fc.CloudCommits()
	if len(commits) != 3 {
		t.Fatalf("commits %d", len(commits))
	}
	var st model.AgentBranchState
	wait("cloud head 2 ahead", 60*time.Second, func() bool {
		call("GET", "/_opm/api/branch", nil, &st)
		return st.Cloud.Head != nil && st.Cloud.Head.CommitID == commits[2].CommitID && st.Local.Head != nil && st.Local.Behind == 2
	})
	mergeIt := func(src string, pick func(model.MergeItem) bool, msg string) {
		t.Helper()
		var plan model.MergePlan
		if c, b := call("POST", "/_opm/api/branch/compare", map[string]any{"source": map[string]string{"cloudCommitId": src}}, &plan); c != 200 {
			t.Fatalf("compare %d %s", c, b)
		}
		var ch []model.MergeChoice
		for _, it := range plan.Items {
			if pick(it) {
				ch = append(ch, model.MergeChoice{Key: it.Key, Take: "source"})
			}
		}
		var head *string
		if plan.Target.Head != nil {
			head = &plan.Target.Head.CommitID
		}
		var run model.UpdateRun
		if c, b := call("POST", "/_opm/api/branch/merge", model.LocalMergeRequest{SourceCommitID: src, TargetHead: head, Choices: ch, Message: msg}, &run); c != 202 {
			t.Fatalf("merge %d %s", c, b)
		}
		wait("merge applied", 3*time.Minute, func() bool {
			var r model.UpdateRun
			call("GET", "/_opm/api/updates/"+run.UpdateID, nil, &r)
			if r.FinishedAt != nil && (r.Result == nil || !r.Result.Ok) {
				t.Fatalf("merge failed %+v", r.Result)
			}
			return r.FinishedAt != nil
		})
	}
	mergeIt(commits[1].CommitID, func(it model.MergeItem) bool { return it.Key == "variables/motd" }, "only the motd")
	var plan model.MergePlan
	call("POST", "/_opm/api/branch/compare", map[string]any{"source": "cloud"}, &plan)
	removal := false
	for _, it := range plan.Items {
		if it.Key == "sites/app/containers/legacy" && it.Effect == "remove" {
			removal = true
		}
	}
	if !removal {
		t.Fatalf("the removal of legacy is not offered: %+v", plan.Items)
	}
	mergeIt(commits[2].CommitID, func(it model.MergeItem) bool { return true }, "take release 3")
	var page model.PageResponse[model.CommitSummary]
	call("GET", "/_opm/api/branch/commits", nil, &page)
	if len(page.Items) != 3 || page.Items[0].Kind != "merge" || page.Items[1].Kind != "cherry-pick" || page.Items[2].Kind != "merge" {
		t.Fatalf("history %+v", page.Items)
	}
}
