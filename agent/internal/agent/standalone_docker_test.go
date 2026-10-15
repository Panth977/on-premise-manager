package agent

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
)

// §21 #113 with real Docker: a container ADDED in the agent GUI (a local commit) is applied through the same engine as a
// cloud merge — a pre-update snapshot is taken first — and History → revert removes it again (containers + config).
func TestStandaloneAddContainerRevertDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dc := docker.New(docker.DefaultHost(""))
	if err := dc.Ping(context.Background()); err != nil {
		t.Skip("no docker: ", err)
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".opm-agent-test", "sa-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	dep := "DepSA" + strconv.FormatInt(time.Now().Unix()%1000000000, 10)
	fc := fakecloud.Start(dep)
	defer fc.Close()
	fc.SetAccessKey(testKey)
	bb := loadBusybox(t, fc)
	network := driver.NetworkName(dep, "edge")
	defer func() {
		cs, _ := dc.ContainerList(context.Background(), true)
		for _, c := range cs {
			if c.Labels["opm.deployment"] == dep || strings.HasPrefix(strings.TrimPrefix(firstName(c.Names), "/"), "opm_"+dep+"_") {
				_ = dc.ContainerRemove(context.Background(), c.ID, true)
			}
		}
		_ = dc.NetworkRemove(context.Background(), network)
	}()
	s := &site{slug: "edge", agentPort: freePort(t), alias: freePort(t), httpP: freePort(t)}
	var total int64
	for _, l := range bb.layers {
		total += l.SizeBytes
	}
	spec := model.ImageSpec{ServiceSlug: "bb", Version: "1", Digest: "sha256:" + bb.manifestHex, ConfigDigest: "sha256:" + bb.configHex,
		Platform: "linux/" + runtime.GOARCH, ManifestMediaType: bb.mediaType, Layers: bb.layers, SizeBytes: total, Kind: "service",
		Ports: []model.ImagePort{}, Env: []model.ImageEnv{}, Volumes: []model.ImageVolume{}, StopTimeoutSec: 1, Command: []string{"sh"}}
	cat := model.RevisionCatalog{Services: map[string]model.CatalogService{"bb": {ServiceSlug: "bb", Label: "busybox"}}, Images: map[string]model.ImageSpec{"bb:1": spec}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	sleeper := func(slug string) model.Container {
		return model.Container{ContainerSlug: slug, ServiceSlug: "bb", Version: "1", Enabled: true, Env: []model.ContainerEnv{},
			Ports: []model.ContainerPort{}, Volumes: []model.ContainerMount{}, Command: []string{"sh", "-c", "sleep 3600"}, Restart: "always", Depends: []model.ContainerRef{}}
	}
	cfg := model.Config{Variables: []model.Variable{}, Secrets: []model.SecretDef{}, Files: []model.FileDef{},
		Sites: []model.Site{{SiteSlug: "edge", Label: "Edge", Config: model.SiteConfig{Host: ptr("127.0.0.1"), AgentPort: s.agentPort, AgentGuiAliasPort: &s.alias,
			Volumes: &[]model.SiteVolume{}, Containers: []model.Container{sleeper("app")}, Depends: []model.SiteRef{},
			Edge: model.Edge{HTTPPort: s.httpP, Hosts: []model.EdgeHost{}, Routes: []model.EdgeRoute{}}}}}}

	dirA := filepath.Join(root, "edge")
	tok := filepath.Join(dirA, "install-token")
	_ = os.MkdirAll(dirA, 0o700)
	_ = os.WriteFile(tok, []byte(fc.CreateInstallToken("edge")), 0o600)
	acfg := model.AgentConfigFile{APIBase: fc.URL, DeploymentID: dep, SiteSlug: "edge", DataDir: filepath.Join(dirA, "data"), AgentPort: s.agentPort,
		GuiAliasPort: model.Some(s.alias), AdvertiseHost: "127.0.0.1", InstallTokenFile: tok, EdgeHTTPPort: model.Null[int](), ContainerNames: "prefixed"}
	a, err := New(Options{Config: acfg, Timings: fastTimings(), ApproveWait: time.Second, CompleteTimeout: 60 * time.Second, LogLevel: "debug", DisableSchedule: true})
	if err != nil {
		t.Fatal(err)
	}
	a.drv.NoneReadyAfter = time.Second
	s.a = a
	jar, _ := cookiejar.New(nil)
	s.cl = &http.Client{Jar: jar, Timeout: 20 * time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "enroll", 30*time.Second, func() bool { _, ok := fc.Agent("edge"); return ok })
	waitFor(t, "key hash", 20*time.Second, func() bool { h, _ := a.keyHash(); return h != "" })
	if c, _ := s.api(t, "POST", "/_opm/api/login", model.LoginRequest{AccessKey: testKey}, nil); c != 200 {
		t.Fatalf("login %d", c)
	}
	var ar model.ActorResponse
	if c, b := s.api(t, "POST", "/_opm/api/actor", model.ActorRequest{Email: "ops@example.com"}, &ar); c != 200 {
		t.Fatalf("actor %d %s", c, b)
	}
	s.actor = ar.ActorToken
	waitRun := func(id string) {
		t.Helper()
		var run model.UpdateRun
		waitFor(t, "run "+id, 5*time.Minute, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/updates/"+id, nil, &run)
			return c == 200 && run.FinishedAt != nil
		})
		if run.Result == nil || !run.Result.Ok {
			t.Fatalf("run failed: %+v", run.Result)
		}
	}
	state := func() model.AgentBranchState {
		var st model.AgentBranchState
		if c, b := s.api(t, "GET", "/_opm/api/branch", nil, &st); c != 200 {
			t.Fatalf("branch %d %s", c, b)
		}
		return st
	}
	preSnapshots := func() int {
		var l model.ListResponse[model.SnapshotSummary]
		s.api(t, "GET", "/_opm/api/snapshots", nil, &l)
		n := 0
		for _, x := range l.Items {
			if x.Reason == "pre-update" {
				n++
			}
		}
		return n
	}

	// the first cloud commit, merged on the site (fast-forward)
	cm1 := fc.Commit(cfg, cat, "initial release")
	waitFor(t, "cloud head on the site", 30*time.Second, func() bool { st := state(); return st.Cloud.Head != nil && st.Cloud.Head.CommitID == cm1.CommitID })
	var plan model.MergePlan
	s.api(t, "POST", "/_opm/api/branch/compare", model.LocalCompareRequest{Source: map[string]any{"cloudCommitId": cm1.CommitID}}, &plan)
	var ch []model.MergeChoice
	for _, it := range plan.Items {
		if it.Preselect {
			ch = append(ch, model.MergeChoice{Key: it.Key, Take: "source"})
		}
	}
	var run model.UpdateRun
	if c, b := s.api(t, "POST", "/_opm/api/branch/merge", model.LocalMergeRequest{SourceCommitID: cm1.CommitID, Choices: ch, Message: "take the release"}, &run); c != 202 {
		t.Fatalf("merge %d %s", c, b)
	}
	waitRun(run.UpdateID)
	before := preSnapshots()

	// a container ADDED on the site (GUI edits → one local commit)
	head := state().Local.Head
	ed := clone(t, cfg)
	ed.Sites[0].Config.Containers = append(ed.Sites[0].Config.Containers, sleeper("extra"))
	var cr model.LocalCommitResponse
	if c, b := s.api(t, "POST", "/_opm/api/branch/commit", map[string]any{"message": "add extra on the site", "baseCommitId": head.CommitID, "config": configToInput(ed)}, &cr); c != 202 || cr.Run == nil {
		t.Fatalf("commit %d %s", c, b)
	}
	waitRun(cr.Run.UpdateID)
	ex, _ := a.drv.Existing(context.Background())
	if _, ok := ex["extra"]; !ok {
		t.Fatal("extra not running after the local commit")
	}
	if n := preSnapshots(); n <= before {
		t.Fatalf("no pre-update snapshot before the local commit (%d → %d)", before, n)
	}
	added := state().Local.Head

	// History → revert the commit: the engine removes the container again (another pre-update snapshot first)
	before = preSnapshots()
	if c, b := s.api(t, "POST", "/_opm/api/branch/revert", model.LocalRevertRequest{CommitID: added.CommitID, Message: "undo the extra container", TargetHead: &added.CommitID}, &run); c != 202 {
		t.Fatalf("revert %d %s", c, b)
	}
	waitRun(run.UpdateID)
	ex, _ = a.drv.Existing(context.Background())
	if _, ok := ex["extra"]; ok {
		t.Fatal("extra still there after the revert")
	}
	if _, ok := ex["app"]; !ok {
		t.Fatal("app gone after the revert")
	}
	if n := preSnapshots(); n <= before {
		t.Fatalf("no pre-update snapshot before the revert (%d → %d)", before, n)
	}
}
