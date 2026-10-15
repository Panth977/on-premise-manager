package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/agent"
	"opm/agent/internal/docker"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// TestStuckUpdateE2E (§21 #110) — two REAL agents (app = coordinator, db) + real Docker, the failure of the HA-4VM
// incident: an update whose container never gets healthy. Checks, over the agent HTTP API the GUI uses:
//
//	S1 while it runs nothing is refused (container restart on the other site, snapshot, a second Stop call);
//	   Stop & roll back → both sites unlocked, rolled back to the head config (web GEN=1 runs again)
//	S2 plain Stop → unlocked at once, no roll back, the run finished
//	S3 a new update while one runs → the running one is stopped (superseded), the new one is applied
//	S4 agents killed in the middle of an update → offline `Repair` (= opm-agent repair --rollback) clears it and rolls
//	   the site back to its last backup; a check afterwards finds nothing stuck
//
// Long (minutes) — runs only with OPM_STUCK_E2E=1 (Linux + Docker; in CI or inside the Colima VM).
func TestStuckUpdateE2E(t *testing.T) {
	if os.Getenv("OPM_STUCK_E2E") != "1" {
		t.Skip("set OPM_STUCK_E2E=1")
	}
	if err := docker.New(docker.DefaultHost("")).Ping(context.Background()); err != nil {
		t.Skip("no docker")
	}
	o := defaultOptions()
	o.Dir = t.TempDir()
	o.Deployment = "DepStuck" + fmt.Sprint(time.Now().Unix()%100000)
	o.AppPort, o.AppAlias, o.DBPort, o.DBAlias, o.EdgePort = freePort(t), freePort(t), freePort(t), freePort(t), freePort(t)
	o.Out = io.Discard
	ctx, cancel := context.WithCancel(context.Background())
	m, err := startMesh(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			cancel()
			m.stop()
		}
		removeDeploymentContainers(o.Deployment)
	}()
	if err := waitEnrolled(m.fc, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	spec, err := loadImageIntoCloud(m.fc, BranchImage)
	if err != nil {
		t.Fatal(err)
	}
	spec.ServiceSlug, spec.Version = "bb", "1"
	cat := model.RevisionCatalog{Services: map[string]model.CatalogService{"bb": {ServiceSlug: "bb", Label: "busybox"}}, Images: map[string]model.ImageSpec{"bb:1": spec}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	// webGen / healthy: web on app; cache on db changes with webGen too, so every update touches both sites
	mk := func(webGen string, healthy bool) model.Config {
		ctr := func(slug, gen string, hc *model.HealthCheck) model.Container {
			return model.Container{ContainerSlug: slug, ServiceSlug: "bb", Version: "1", Enabled: true, Env: []model.ContainerEnv{{Name: "GEN", Value: &gen}},
				Ports: []model.ContainerPort{}, Volumes: []model.ContainerMount{}, Command: []string{"sh", "-c", "sleep 2147483647"}, Restart: "always", Depends: []model.ContainerRef{}, Healthcheck: hc}
		}
		var hc *model.HealthCheck
		if !healthy { // the incident: never healthy (here a 40 s start period instead of 900 s)
			hc = &model.HealthCheck{Type: "exec", Command: []string{"false"}, IntervalSec: 2, TimeoutSec: 2, Retries: 2, StartPeriodSec: 40}
		}
		cfg := validate.EmptyConfig()
		host := "127.0.0.1"
		for i, s := range []struct {
			slug        string
			port, alias int
		}{{"app", o.AppPort, o.AppAlias}, {"db", o.DBPort, o.DBAlias}} {
			sc := validate.DefaultSiteConfig(&host)
			alias := s.alias
			sc.AgentPort, sc.AgentGuiAliasPort = s.port, &alias
			sc.Edge.HTTPPort, sc.Edge.HTTPSPort = o.EdgePort+i, nil
			dir := "dir"
			vols := []model.SiteVolume{{VolumeSlug: "data", Label: "Data", Backup: true, Location: validate.AutoLocation(), Kind: &dir, Seed: model.Null[model.VolumeSeed]()}}
			sc.Volumes = &vols
			if s.slug == "app" {
				sc.Containers = []model.Container{ctr("web", webGen, hc)}
			} else {
				sc.Containers = []model.Container{ctr("cache", webGen, nil)}
			}
			cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: s.slug, Label: s.slug, Config: sc})
		}
		return cfg
	}
	c1 := m.fc.Commit(mk("1", true), cat, "gen 1 (healthy)")
	for _, alias := range []int{o.AppAlias, o.DBAlias} {
		var err error
		for try := 0; try < 30; try++ {
			if err = mergeOverAPI(o, alias, c1.CommitID); err == nil || !strings.Contains(err.Error(), "provisioning CA") {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if err != nil {
			t.Fatalf("merge c1: %v", err)
		}
	}
	app, db := newGUI(t, o, o.AppAlias, "app"), newGUI(t, o, o.DBAlias, "db")
	gen := func(slug string) string { // GEN of the running container (docker)
		out, _ := exec.Command("docker", "ps", "--filter", "label=opm.deployment="+o.Deployment, "--filter", "label=opm.container="+slug, "--format", "{{.ID}}").Output()
		id := strings.TrimSpace(strings.Split(strings.TrimSpace(string(out)), "\n")[0])
		if id == "" {
			return ""
		}
		env, _ := exec.Command("docker", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", id).Output()
		for _, l := range strings.Split(string(env), "\n") {
			if strings.HasPrefix(l, "GEN=") {
				return strings.TrimPrefix(l, "GEN=")
			}
		}
		return "?"
	}
	lockOf := func(g *gui) *model.UpdateLock {
		var s map[string]json.RawMessage
		g.call("GET", "/_opm/api/overview", nil, &s)
		var l *model.UpdateLock
		_ = json.Unmarshal(s["lock"], &l)
		return l
	}
	activities := func(g *gui, typ string) int {
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		g.call("GET", "/_opm/api/sites/"+g.site+"/activities?type="+typ+"&limit=100", nil, &p)
		return len(p.Items)
	}
	ffAll := func(commitID, msg string) model.UpdateRun {
		t.Helper()
		for _, g := range []*gui{app, db} { // the agents see a new cloud commit at their next view exchange
			waitT(t, "cloud commit "+commitID+" seen by "+g.site, 2*time.Minute, func() bool {
				var st model.AgentBranchState
				g.call("GET", "/_opm/api/branch", nil, &st)
				return st.Cloud.Head != nil && st.Cloud.Head.CommitID == commitID
			})
		}
		var run model.UpdateRun
		if c, b := app.call("POST", "/_opm/api/branch/fast-forward", model.FastForwardAllRequest{SourceCommitID: commitID, Sites: []string{"app", "db"}, Message: msg}, &run); c != 202 && c != 200 {
			t.Fatalf("fast-forward %d %s", c, b)
		}
		return run
	}
	waitApplying := func(what string) *model.UpdateLock {
		t.Helper()
		var l *model.UpdateLock
		waitT(t, what, 3*time.Minute, func() bool { l = lockOf(app); return l != nil && lockOf(db) != nil })
		return l
	}
	if g := gen("web"); g != "1" {
		t.Fatalf("web GEN after c1: %q", g)
	}

	dump := func(when string) { // container states for a failure report
		out, _ := exec.Command("docker", "ps", "-a", "--filter", "label=opm.deployment="+o.Deployment, "--format", "{{.Names}} {{.Status}} {{.Label \"opm.container\"}} {{.Label \"opm.rev\"}}").CombinedOutput()
		t.Logf("%s — containers:\n%s", when, out)
	}
	headGen := "1"
	only4 := os.Getenv("OPM_STUCK_ONLY") == "S4"
	if !only4 {
		// ── S1 a stuck update: nothing is refused while it runs; Stop & roll back
		c2 := m.fc.Commit(mk("2", false), cat, "gen 2 (web never healthy)")
		run := ffAll(c2.CommitID, "take gen 2")
		l := waitApplying("S1 update running on both sites")
		t.Logf("S1 update %s running (lock on both sites)", l.UpdateID)
		if c, b := db.call("POST", "/_opm/api/sites/db/tools/containers/cache/restart", map[string]string{"reason": "while an update runs"}, nil); c != 200 {
			t.Fatalf("S1 container restart during an update refused: %d %s", c, b)
		}
		if c, b := app.call("POST", "/_opm/api/snapshots", map[string]any{"note": "during an update"}, nil); c == 409 && strings.Contains(b, "locked") {
			t.Fatalf("S1 snapshot refused: %s", b)
		}
		var tr model.TerminateUpdateResponse
		if c, b := db.call("POST", "/_opm/api/updates/current/terminate", model.TerminateUpdateRequest{Reason: "S1 test", Rollback: true}, &tr); c != 200 {
			t.Fatalf("S1 terminate from db (not the coordinator) %d %s", c, b)
		}
		t.Logf("S1 terminate: %+v", tr.Sites)
		for _, s := range tr.Sites {
			if !s.Ok {
				t.Fatalf("S1 site %s: %s", s.SiteSlug, s.Message)
			}
		}
		waitT(t, "S1 both sites rolled back + unlocked", 5*time.Minute, func() bool {
			return lockOf(app) == nil && lockOf(db) == nil && activities(app, "update.recovered") >= 1 && activities(db, "update.recovered") >= 1
		})
		waitT(t, "S1 web GEN=1 runs again", time.Minute, func() bool { return gen("web") == "1" && gen("cache") == "1" })
		var r model.UpdateRun
		app.call("GET", "/_opm/api/updates/"+run.UpdateID, nil, &r)
		if r.FinishedAt == nil || r.Result == nil || r.Result.Ok || !strings.Contains(r.Result.Message, "terminated") {
			t.Fatalf("S1 run %+v", r.Result)
		}
		t.Log("S1 ok: nothing refused while running; Stop & roll back from db → both sites back on gen 1")

		// ── S2 plain Stop: unlocked at once, nothing rolled back
		recBefore := activities(app, "update.recovered")
		run = ffAll(c2.CommitID, "take gen 2 again")
		waitApplying("S2 update running")
		start := time.Now()
		if c, b := app.call("POST", "/_opm/api/updates/"+run.UpdateID+"/terminate", model.TerminateUpdateRequest{Reason: "S2 test"}, &tr); c != 200 {
			t.Fatalf("S2 terminate %d %s", c, b)
		}
		waitT(t, "S2 unlocked", 30*time.Second, func() bool { return lockOf(app) == nil && lockOf(db) == nil })
		t.Logf("S2 unlocked in %s: %+v", time.Since(start).Round(time.Second), tr.Sites)
		time.Sleep(5 * time.Second)
		if activities(app, "update.recovered") != recBefore {
			t.Fatal("S2 a plain Stop rolled back")
		}
		app.call("GET", "/_opm/api/updates/"+run.UpdateID, nil, &r)
		if r.FinishedAt == nil {
			t.Fatal("S2 run not finished")
		}
		t.Log("S2 ok: plain Stop, no roll back")

		// ── S3 a new update while one runs: the running one is stopped, the new one applied
		old := ffAll(c2.CommitID, "gen 2 a third time")
		waitApplying("S3 first update running")
		c3 := m.fc.Commit(mk("3", true), cat, "gen 3 (healthy)")
		nw := ffAll(c3.CommitID, "gen 3 while gen 2 runs")
		if nw.UpdateID == old.UpdateID {
			t.Fatal("S3 same update")
		}
		waitT(t, "S3 gen 3 applied", 5*time.Minute, func() bool {
			app.call("GET", "/_opm/api/updates/"+nw.UpdateID, nil, &r)
			if r.FinishedAt != nil && (r.Result == nil || !r.Result.Ok) {
				t.Fatalf("S3 new update failed: %+v", r.Result)
			}
			return r.FinishedAt != nil
		})
		app.call("GET", "/_opm/api/updates/"+old.UpdateID, nil, &r)
		if r.FinishedAt == nil || r.Result == nil || !strings.Contains(r.Result.Message, "superseded") {
			t.Fatalf("S3 old update %+v", r.Result)
		}
		if gen("web") != "3" || gen("cache") != "3" || lockOf(app) != nil || lockOf(db) != nil {
			t.Fatalf("S3 web %s cache %s", gen("web"), gen("cache"))
		}
		t.Log("S3 ok: a new update stopped the running one and was applied (gen 3)")
		headGen = "3"
	}

	// ── S5 the incident itself, nobody presses Stop: the update fails on health, rolls back by itself, unlocks
	c5 := m.fc.Commit(mk("5", false), cat, "gen 5 (never healthy, left alone)")
	run5 := ffAll(c5.CommitID, "gen 5, left alone")
	var r model.UpdateRun
	waitApplying("S5 update running")
	t0 := time.Now()
	waitT(t, "S5 failed + rolled back by itself", 8*time.Minute, func() bool {
		app.call("GET", "/_opm/api/updates/"+run5.UpdateID, nil, &r)
		return r.FinishedAt != nil && lockOf(app) == nil && lockOf(db) == nil
	})
	if r.Result == nil || r.Result.Ok {
		t.Fatalf("S5 result %+v", r.Result)
	}
	waitT(t, "S5 the head runs again on both sites", 2*time.Minute, func() bool { return gen("web") == headGen && gen("cache") == headGen })
	t.Logf("S5 ok: failed (%s) and rolled back by itself in %s, both sites unlocked, gen %s runs", r.Result.Message, time.Since(t0).Round(time.Second), headGen)

	// ── S4 the agents die mid-update → offline repair (opm-agent repair --rollback)
	c4 := m.fc.Commit(mk("4", false), cat, "gen 4 (never healthy)")
	ffAll(c4.CommitID, "gen 4")
	waitApplying("S4 update running")
	time.Sleep(3 * time.Second)
	dump("S4 before the kill")
	cancel()
	m.stop() // both agents stop in the middle of the apply (as if killed)
	stopped = true
	for _, s := range []string{"app", "db"} {
		cfg := model.AgentConfigFile{APIBase: m.fc.URL, DeploymentID: o.Deployment, SiteSlug: s, DataDir: filepath.Join(o.Dir, s, "data"), ContainerNames: "prefixed"}
		a, err := agent.New(agent.Options{Config: cfg, LogLevel: "warn", LogEcho: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		chk, err := a.Repair(agent.RepairOptions{CheckOnly: true})
		if err != nil || len(chk.Stuck) == 0 {
			t.Fatalf("S4 %s check: %+v %v", s, chk, err)
		}
		rep, err := a.Repair(agent.RepairOptions{Rollback: true})
		if err != nil || len(rep.Terminated) == 0 || !rep.RolledBack {
			dump("S4 repair failed")
			t.Fatalf("S4 %s repair: %+v %v", s, rep, err)
		}
		again, _ := a.Repair(agent.RepairOptions{CheckOnly: true})
		a.Close()
		if len(again.Stuck) != 0 {
			t.Fatalf("S4 %s still stuck: %+v", s, again)
		}
		t.Logf("S4 %s: stopped %v, rolled back to %q (%s), notes %v", s, rep.Terminated, rep.Backup, rep.BackupAt, rep.Notes)
	}
	if gen("web") != headGen || gen("cache") != headGen {
		dump("S4 after repair")
		t.Fatalf("S4 after repair: web %s cache %s (want the head, gen %s)", gen("web"), gen("cache"), headGen)
	}
	t.Log("S4 ok: offline repair cleared the stuck update and rolled both sites back to gen 3")
}

type gui struct {
	t     *testing.T
	cl    *http.Client
	base  string
	site  string
	actor string
}

func newGUI(t *testing.T, o meshOptions, alias int, site string) *gui {
	jar, _ := cookiejar.New(nil)
	g := &gui{t: t, cl: &http.Client{Jar: jar, Timeout: 5 * time.Minute}, base: fmt.Sprintf("http://127.0.0.1:%d", alias), site: site}
	waitT(t, "GUI login", 60*time.Second, func() bool {
		c, _ := g.call("POST", "/_opm/api/login", map[string]string{"accessKey": o.Key}, nil)
		return c == 200
	})
	var ar model.ActorResponse
	g.call("POST", "/_opm/api/actor", map[string]string{"email": "stuck-e2e@opm.test"}, &ar)
	g.actor = ar.ActorToken
	return g
}

func (g *gui) call(method, path string, body, out any) (int, string) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r, _ := http.NewRequest(method, g.base+path, rd)
	if g.actor != "" && method != "GET" {
		r.Header.Set("Authorization", "Actor "+g.actor)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.cl.Do(r)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(b, out)
	}
	return resp.StatusCode, string(b)
}

func waitT(t *testing.T, what string, max time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timeout: %s", what)
}
