package agent

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
)

// TestDriftDocker (§21 #104, real Docker): a cloud commit is merged on a site (no proposal); then a person changes the
// site by hand — a container recreated with other env (same name), a file copy edited, a container switched to another
// image, an unknown container on the site network — and the agent shows all of it as UNCOMMITTED; a merge is refused
// (409 uncommitted-drift; a GUI-edits commit too, §21 #105); the changes are committed (kind drift); a new cloud commit with 2 changes is merged by
// CHERRY-PICK (one change taken, one skipped) together with a DELETION removing the local-only container; the commit
// chain, the merge base and the cloud mirror are checked. §21 #105: a GUI-edits commit ({ config, baseCommitId }) applies
// and writes a commit kind change; a GUI-edits commit whose apply fails writes NO commit; saved 0.14.0 edits are
// auto-committed by the start-up migration.
func TestDriftDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dc := docker.New(docker.DefaultHost(""))
	if err := dc.Ping(context.Background()); err != nil {
		t.Skip("no docker: ", err)
	}
	start := time.Now()
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".opm-agent-test", "br-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	dep := "DepBR" + strconv.FormatInt(time.Now().Unix()%1000000000, 10)
	fc := fakecloud.Start(dep)
	defer fc.Close()
	fc.SetAccessKey(testKey)
	bb := loadBusybox(t, fc)
	network := driver.NetworkName(dep, "edge")
	defer func() {
		cs, _ := dc.ContainerList(context.Background(), true)
		for _, c := range cs {
			n := strings.TrimPrefix(firstName(c.Names), "/")
			if c.Labels["opm.deployment"] == dep || n == "stray-"+dep || strings.HasPrefix(n, "opm_"+dep+"_") {
				_ = dc.ContainerRemove(context.Background(), c.ID, true)
			}
		}
		_ = dc.NetworkRemove(context.Background(), network)
		_ = exec.Command("docker", "rmi", "-f", "opm-br-test:"+strings.ToLower(dep)).Run()
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
	confBody := []byte("conf v1\n")
	confHex := fc.PutBlob(confBody)
	dir := "dir"
	sleeper := func(slug, gen string, mounts []model.ContainerMount) model.Container {
		return model.Container{ContainerSlug: slug, ServiceSlug: "bb", Version: "1", Enabled: true, Env: []model.ContainerEnv{{Name: "GEN", Value: ptr(gen)}},
			Ports: []model.ContainerPort{}, Volumes: mounts, Command: []string{"sh", "-c", "sleep 3600"}, Restart: "always", Depends: []model.ContainerRef{}}
	}
	mkConfig := func(dataLabel string, extraVar bool) model.Config {
		c := model.Config{Variables: []model.Variable{{VariableSlug: "greeting", Label: "Greeting", Type: "string", Value: model.VariableValue{V: "hi"}}}, Secrets: []model.SecretDef{},
			Files: []model.FileDef{{FileSlug: "conf", Label: "Conf", Mime: "text/plain", Editor: "text", MaxBytes: 65536, Render: model.Some("none"), Content: &model.BlobRef{Sha256: "sha256:" + confHex, SizeBytes: int64(len(confBody))}}},
			Sites: []model.Site{{SiteSlug: "edge", Label: "Edge", Config: model.SiteConfig{Host: ptr("127.0.0.1"), AgentPort: s.agentPort, AgentGuiAliasPort: &s.alias,
				Volumes: &[]model.SiteVolume{{VolumeSlug: "data", Label: dataLabel, Backup: false, Kind: &dir, Seed: model.Null[model.VolumeSeed]()}},
				Containers: []model.Container{
					sleeper("app", "1", []model.ContainerMount{{MountPath: ptr("/data"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "data"}}, {MountPath: ptr("/conf.txt"), ReadOnly: true, Source: model.VolumeSource{Kind: "file", FileSlug: "conf"}}}),
					sleeper("web", "1", []model.ContainerMount{}),
				},
				Depends: []model.SiteRef{}, Edge: model.Edge{HTTPPort: s.httpP, Hosts: []model.EdgeHost{}, Routes: []model.EdgeRoute{}}}}},
		}
		if extraVar {
			c.Variables = append(c.Variables, model.Variable{VariableSlug: "motd", Label: "MOTD", Type: "string", Value: model.VariableValue{V: "hello"}})
		}
		return c
	}

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
	waitRun := func(id string) model.UpdateRun {
		t.Helper()
		var run model.UpdateRun
		waitFor(t, "run "+id, 5*time.Minute, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/updates/"+id, nil, &run)
			return c == 200 && run.FinishedAt != nil
		})
		if run.Result == nil || !run.Result.Ok {
			t.Fatalf("run failed: %+v", run.Result)
		}
		return run
	}
	state := func() model.AgentBranchState {
		var st model.AgentBranchState
		if c, b := s.api(t, "GET", "/_opm/api/branch", nil, &st); c != 200 {
			t.Fatalf("branch %d %s", c, b)
		}
		return st
	}
	mergeAll := func(srcID, msg string, pick func(model.MergeItem) bool) (int, []byte) {
		t.Helper()
		var plan model.MergePlan
		if c, b := s.api(t, "POST", "/_opm/api/branch/compare", model.LocalCompareRequest{Source: map[string]any{"cloudCommitId": srcID}}, &plan); c != 200 {
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
		c, b := s.api(t, "POST", "/_opm/api/branch/merge", model.LocalMergeRequest{SourceCommitID: srcID, TargetHead: head, Choices: ch, Message: msg}, &run)
		if c == 202 {
			waitRun(run.UpdateID)
		}
		return c, b
	}

	// 1. the first cloud commit reaches the site; a person merges it (fast-forward, no proposal, no wait)
	cm1 := fc.Commit(mkConfig("Data", false), cat, "initial release")
	waitFor(t, "cloud head on the site", 30*time.Second, func() bool { st := state(); return st.Cloud.Head != nil && st.Cloud.Head.CommitID == cm1.CommitID })
	if c, b := mergeAll(cm1.CommitID, "take the first release", func(it model.MergeItem) bool { return it.Preselect }); c != 202 {
		t.Fatalf("merge 1: %d %s", c, b)
	}
	st := state()
	if st.Local.Head == nil || st.Local.Head.Kind != "merge" || st.Local.MergedFrom == nil || st.Local.MergedFrom.CommitID != cm1.CommitID {
		t.Fatalf("after merge 1: %+v", st.Local)
	}
	ex, _ := a.drv.Existing(context.Background())
	if _, ok := ex["app"]; !ok {
		t.Fatal("app not running")
	}
	if _, ok := ex["web"]; !ok {
		t.Fatal("web not running")
	}

	// 2. hand edits on the server
	appName, webName := a.drv.NameFor("app"), a.drv.NameFor("web")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
	}
	run("rm", "-f", appName)
	run("run", "-d", "--name", appName, "--network", network, "-e", "GEN=hand", "busybox:1.36", "sh", "-c", "sleep 3600")
	img := "opm-br-test:" + strings.ToLower(dep)
	run("commit", webName, img)
	run("rm", "-f", webName)
	run("run", "-d", "--name", webName, "--network", network, img, "sh", "-c", "sleep 3600")
	run("run", "-d", "--name", "stray-"+dep, "--network", network, "busybox:1.36", "sh", "-c", "sleep 3600")
	confPath := filepath.Join(a.dataDir, "files", "conf")
	_ = os.Chmod(confPath, 0o644)
	if err := os.WriteFile(confPath, []byte("conf EDITED BY HAND\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. everything shows as uncommitted
	if c, b := s.api(t, "POST", "/_opm/api/branch/drift/rescan", nil, nil); c != 200 {
		t.Fatalf("rescan %d %s", c, b)
	}
	var unc model.UncommittedResponse
	s.api(t, "GET", "/_opm/api/branch/uncommitted", nil, &unc)
	keys := map[string]model.DriftItem{}
	for _, it := range unc.Items {
		keys[it.Key] = it
	}
	strayKey := "sites/edge/containers/" + sanitizeSlug("stray-"+dep, map[string]bool{})
	for _, k := range []string{"sites/edge/containers/app/env/GEN", "sites/edge/containers/web/image", "files/conf", strayKey} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("uncommitted lacks %s: %v", k, keysOf(keys))
		}
	}
	if keys["files/conf"].Origin != "file" || keys["sites/edge/containers/app/env/GEN"].Origin != "docker" {
		t.Fatalf("origins %+v", keys["files/conf"])
	}

	// §21 #106: the drift reaches the cloud (POST /v1/agent/branch/drift) with the live config, secrets masked
	var pushed model.AgentDriftPushRequest
	waitFor(t, "drift pushed to the cloud", 30*time.Second, func() bool {
		l := fc.DriftPushes("edge")
		if n := len(l); n > 0 && len(l[n-1].Items) > 0 {
			pushed = l[n-1]
			return true
		}
		return false
	})
	if pushed.LiveConfig == nil || pushed.Fingerprint == "" || pushed.HeadCommitID == nil {
		t.Fatalf("drift push %+v", pushed)
	}
	for _, sd := range pushed.LiveConfig.Secrets {
		if sd.Value != nil && *sd.Value != model.Mask {
			t.Fatalf("secret %s sent in clear", sd.SecretSlug)
		}
	}
	pk := map[string]bool{}
	for _, it := range pushed.Items {
		pk[it.Key] = true
	}
	if !pk["sites/edge/containers/app/env/GEN"] || !pk["files/conf"] {
		t.Fatalf("pushed keys %v", pk)
	}
	nPush := len(fc.DriftPushes("edge"))
	s.api(t, "POST", "/_opm/api/branch/drift/rescan", nil, nil)
	time.Sleep(3 * time.Second)
	if n := len(fc.DriftPushes("edge")); n != nPush {
		t.Fatalf("same drift pushed again (%d → %d pushes)", nPush, n)
	}

	// 4. a merge is refused while server changes are uncommitted
	if c, b := mergeAll(cm1.CommitID, "should fail", func(model.MergeItem) bool { return true }); c != 409 || !strings.Contains(string(b), "uncommitted-drift") {
		t.Fatalf("merge while dirty: %d %s", c, b)
	}
	// … and so is a commit of GUI edits (§21 #105 J12: commit the server changes first)
	{
		in := inputOf(a.headRevision().Config)
		in.Variables[0].Value = model.VariableValue{V: "from the GUI"}
		if c, b := s.api(t, "POST", "/_opm/api/branch/commit", map[string]any{"message": "gui edit while dirty", "baseCommitId": a.head().RevID, "config": in}, nil); c != 409 || !strings.Contains(string(b), "uncommitted-drift") {
			t.Fatalf("edits commit while dirty: %d %s", c, b)
		}
	}

	// 5. commit the server changes (no apply: they are live already)
	var cr model.LocalCommitResponse
	if c, b := s.api(t, "POST", "/_opm/api/branch/commit", model.LocalCommitRequest{Message: "record the hotfix done by hand"}, &cr); c != 201 || cr.Commit == nil || cr.Commit.Kind != "drift" {
		t.Fatalf("commit drift %d %s", c, b)
	}
	s.api(t, "POST", "/_opm/api/branch/drift/rescan", nil, nil)
	s.api(t, "GET", "/_opm/api/branch/uncommitted", nil, &unc)
	if len(unc.Items) != 0 || len(unc.Notes) != 0 {
		t.Fatalf("still uncommitted after the commit: %+v %+v", unc.Items, unc.Notes)
	}
	if ex, _ := dc.ContainerNamed(context.Background(), "stray-"+dep); ex == nil {
		t.Fatal("the committed stray container must still run")
	}
	waitFor(t, "drift cleared in the cloud", 30*time.Second, func() bool {
		l := fc.DriftPushes("edge")
		n := len(l)
		return n > nPush && len(l[n-1].Items) == 0 && len(l[n-1].Notes) == 0 && l[n-1].LiveConfig == nil
	})

	// 6. a new cloud commit with 2 changes: cherry-pick one, skip one, and remove the local-only container
	cm2 := fc.Commit(mkConfig("Data (renamed)", true), cat, "add motd + rename the data volume")
	waitFor(t, "cloud head 2", 30*time.Second, func() bool { st := state(); return st.Cloud.Head != nil && st.Cloud.Head.CommitID == cm2.CommitID })
	var plan model.MergePlan
	s.api(t, "POST", "/_opm/api/branch/compare", model.LocalCompareRequest{Source: "cloud"}, &plan)
	kinds := map[string]string{}
	for _, it := range plan.Items {
		kinds[it.Key] = it.Kind + "/" + it.Effect
	}
	if kinds["variables/motd"] != "theirs/add" || kinds["sites/edge/volumes/data"] != "theirs/change" || kinds[strayKey] != "ours/remove" {
		t.Fatalf("plan kinds %v", kinds)
	}
	if c, b := mergeAll(cm2.CommitID, "take motd, drop the stray container", func(it model.MergeItem) bool {
		return it.Key == "variables/motd" || it.Key == strayKey
	}); c != 202 {
		t.Fatalf("merge 2: %d %s", c, b)
	}
	if ex, _ := dc.ContainerNamed(context.Background(), "stray-"+dep); ex != nil {
		t.Fatal("the stray container must be removed locally")
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "volumes", "data")); err != nil {
		t.Fatal("the data folder must stay: ", err)
	}
	hr := a.headRevision()
	if hr.Config.Variables[len(hr.Config.Variables)-1].VariableSlug != "motd" || (*hr.Config.Sites[0].Config.Volumes)[0].Label != "Data" {
		t.Fatalf("head after cherry-pick: %+v", hr.Config.Variables)
	}
	hc := a.headCommit()
	if hc.Kind != "cherry-pick" || hc.Source == nil || *hc.Source.CommitID != cm2.CommitID || hc.Source.Skipped != 1 {
		t.Fatalf("head commit %+v", hc)
	}
	mb, _ := a.mergeBase()
	if *mb.SourceCommitID != cm2.CommitID || len(mb.Skipped) != 1 || mb.Skipped[0].Key != "sites/edge/volumes/data" {
		t.Fatalf("merge base %+v", mb)
	}
	// the adopted app (recreated by hand) is now a managed container with the hand env
	ex, _ = a.drv.Existing(context.Background())
	if e, ok := ex["app"]; !ok || !e.Running {
		t.Fatal("app must be managed again")
	}
	// 7. the chain reached the cloud mirror: merge, drift, cherry-pick
	waitFor(t, "push to the cloud mirror", 30*time.Second, func() bool { return len(fc.AgentBranch("edge")) >= 3 })
	var got []string
	for _, c := range fc.AgentBranch("edge") {
		got = append(got, c.Kind)
	}
	if strings.Join(got, ",") != "merge,drift,cherry-pick" {
		t.Fatalf("mirror kinds %v", got)
	}
	waitFor(t, "heartbeat branch report", 20*time.Second, func() bool {
		r := fc.AgentBranchReport("edge")
		return r != nil && r.Head != nil && r.Head.CommitID == hc.CommitID
	})
	// 8. §21 #105 GUI edits commit = apply + commit (kind change): app env GEN → gui, greeting changed
	waitRunAny := func(id string) model.UpdateRun {
		t.Helper()
		var run model.UpdateRun
		waitFor(t, "run "+id, 5*time.Minute, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/updates/"+id, nil, &run)
			return c == 200 && run.FinishedAt != nil
		})
		return run
	}
	setEnv := func(c *model.Config, ctr, val string) {
		for i := range c.Sites[0].Config.Containers {
			if c.Sites[0].Config.Containers[i].ContainerSlug == ctr {
				c.Sites[0].Config.Containers[i].Env = []model.ContainerEnv{{Name: "GEN", Value: ptr(val)}}
			}
		}
	}
	s.api(t, "POST", "/_opm/api/branch/drift/rescan", nil, nil)
	before := a.headCommit()
	edited := clone(t, a.headRevision().Config)
	edited.Variables[0].Value = model.VariableValue{V: "hello from the GUI"}
	setEnv(&edited, "app", "gui")
	// §21 #107: new File content travels with the commit through a stage (nothing stored before Commit)
	conf2 := []byte("conf v2 (gui)\n")
	conf2Hex := bytesSha256Hex(conf2)
	edited.Files[0].Content = &model.BlobRef{Sha256: "sha256:" + conf2Hex, SizeBytes: int64(len(conf2))}
	if c, b := s.api(t, "POST", "/_opm/api/branch/commit", map[string]any{"message": "GUI: no stage", "baseCommitId": before.CommitID, "config": inputOf(edited)}, nil); c != 422 || !strings.Contains(string(b), "V_BLOB_MISSING") {
		t.Fatalf("commit without the staged blob: %d %s", c, b)
	}
	var stg model.Stage
	if c, b := s.api(t, "POST", "/_opm/api/stages", model.CreateStageRequest{Blobs: model.NewConfigBlobs(revConfig(a.headRevision()), &edited)}, &stg); c != 201 || len(stg.Missing) != 1 {
		t.Fatalf("create stage %d %s", c, b)
	}
	if c, b := s.api(t, "PUT", "/_opm/api/stages/"+stg.StageID+"/blobs/sha256:"+conf2Hex, conf2, nil); c != 200 || !strings.Contains(string(b), `"staged":true`) {
		t.Fatalf("stage blob %d %s", c, b)
	}
	if a.haveBlob(conf2Hex) {
		t.Fatal("the staged blob reached the blob store before Commit")
	}
	var cr2 model.LocalCommitResponse
	if c, b := s.api(t, "POST", "/_opm/api/branch/commit", map[string]any{"message": "GUI: greeting + app GEN", "baseCommitId": before.CommitID, "config": inputOf(edited), "stageId": stg.StageID}, &cr2); c != 202 || cr2.Run == nil || cr2.Commit != nil {
		t.Fatalf("edits commit %d %s", c, b)
	}
	if _, err := os.Stat(a.stageDir(stg.StageID)); !os.IsNotExist(err) || !a.haveBlob(conf2Hex) {
		t.Fatalf("stage not promoted / removed: %v", err)
	}
	// a second commit on the same base while the first is applying / after it → stale (head-moved) or locked
	if r := waitRunAny(cr2.Run.UpdateID); r.Result == nil || !r.Result.Ok {
		t.Fatalf("edits apply failed: %+v", r.Result)
	}
	gc := a.headCommit()
	if gc.CommitID == before.CommitID || gc.Kind != "change" || gc.Message != "GUI: greeting + app GEN" || gc.Author.Email != "ops@example.com" || gc.Data.UpdateID == nil {
		t.Fatalf("GUI commit %+v", gc)
	}
	if out, _ := exec.Command("docker", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", a.drv.NameFor("app")).CombinedOutput(); !strings.Contains(string(out), "GEN=gui") {
		t.Fatalf("app env after the GUI commit: %s", out)
	}
	// the committed head names the new content and the agent serves it (conf is not mounted at this point of the test)
	if hf := a.headRevision().Config.Files[0].Content; hf == nil || hf.Sha256 != "sha256:"+conf2Hex {
		t.Fatalf("head conf content %+v", hf)
	}
	if c, got := s.api(t, "GET", "/_opm/api/files/conf/content", nil, nil); c != 200 || string(got) != string(conf2) {
		t.Fatalf("conf content after the GUI commit: %d %q", c, got)
	}
	if c, b := s.api(t, "POST", "/_opm/api/branch/commit", map[string]any{"message": "stale", "baseCommitId": before.CommitID, "config": inputOf(edited)}, nil); c != 409 || !strings.Contains(string(b), "head-moved") {
		t.Fatalf("stale base: %d %s", c, b)
	}
	waitFor(t, "GUI commit in the cloud mirror", 30*time.Second, func() bool {
		l := fc.AgentBranch("edge")
		return len(l) >= 4 && l[len(l)-1].CommitID == gc.CommitID && l[len(l)-1].Kind == "change"
	})
	// 9. a GUI edit whose apply fails (web exits at once): rolled back, NO commit, branch.merge-failed
	s.api(t, "POST", "/_opm/api/branch/drift/rescan", nil, nil)
	broken := clone(t, a.headRevision().Config)
	for i := range broken.Sites[0].Config.Containers {
		if broken.Sites[0].Config.Containers[i].ContainerSlug == "web" {
			broken.Sites[0].Config.Containers[i].Command = []string{"sh", "-c", "exit 1"}
		}
	}
	failsBefore := len(activityData(t, a, "branch.merge-failed"))
	var cr3 model.LocalCommitResponse
	if c, b := s.api(t, "POST", "/_opm/api/branch/commit", map[string]any{"message": "GUI: break web", "baseCommitId": gc.CommitID, "config": inputOf(broken)}, &cr3); c != 202 || cr3.Run == nil {
		t.Fatalf("broken edits commit %d %s", c, b)
	}
	if r := waitRunAny(cr3.Run.UpdateID); r.Result == nil || r.Result.Ok {
		t.Fatalf("the broken edit must fail: %+v", r.Result)
	}
	if a.headCommit().CommitID != gc.CommitID {
		t.Fatal("a failed GUI-edits commit must not write a commit")
	}
	if len(activityData(t, a, "branch.merge-failed")) != failsBefore+1 {
		t.Fatal("branch.merge-failed activity missing")
	}
	if ex, _ := a.drv.Existing(context.Background()); !ex["web"].Running {
		t.Fatal("web must run again after the rollback")
	}
	// 10. J13: saved 0.14.0 edits are auto-committed through the apply engine at start-up
	oldWait, oldPoll := legacyWorkingWait, legacyWorkingPoll
	legacyWorkingWait, legacyWorkingPoll = 90*time.Second, 200*time.Millisecond
	defer func() { legacyWorkingWait, legacyWorkingPoll = oldWait, oldPoll }()
	saved := clone(t, a.headRevision().Config)
	setEnv(&saved, "app", "saved-in-0.14.0")
	base := a.head().RevID
	_ = a.db.SetKV("workingCopy", workingCopy{BaseCommitID: &base, Config: saved, Keys: []string{"sites/edge/containers/app/env/GEN"}, UpdatedAt: nowTs(), UpdatedBy: "ops@example.com"})
	s.api(t, "POST", "/_opm/api/branch/drift/rescan", nil, nil)
	a.startLegacyWorkingMigration()
	waitFor(t, "auto-commit of the saved edits", 3*time.Minute, func() bool {
		m, ok := a.legacyMigrationState()
		return ok && m.State == "done"
	})
	m, _ := a.legacyMigrationState()
	ac := a.headCommit()
	if m.Result != "committed" || ac.Message != legacyWorkingMessage || ac.Author.Kind != "system" || ac.Data.AutoCommitted == nil || ac.Data.AutoCommitted.From != "agent-working-copy" || a.legacyWorking() != nil {
		t.Fatalf("auto-commit %+v / %+v", m, ac)
	}
	if out, _ := exec.Command("docker", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", a.drv.NameFor("app")).CombinedOutput(); !strings.Contains(string(out), "GEN=saved-in-0.14.0") {
		t.Fatalf("app env after the auto-commit: %s", out)
	}
	t.Logf("TestDriftDocker ok in %s", time.Since(start).Round(time.Second))
}

func keysOf(m map[string]model.DriftItem) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestTwoSiteBranches (§21 #104, real Docker, two agents): per-site agent branches — db merges the cloud commit on its
// own (app stays behind), "Fast-forward all" moves both sites to the next cloud commit in ONE coordinated update, a
// failing container rolls the merge back and leaves NO commit, and a legacy (proposal) update of an old coordinator is
// answered as a no-op participant.
func TestTwoSiteBranches(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dc := docker.New(docker.DefaultHost(""))
	if err := dc.Ping(context.Background()); err != nil {
		t.Skip("no docker: ", err)
	}
	began := time.Now()
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".opm-agent-test", "br2-"+time.Now().Format("20060102-150405"))
	_ = os.MkdirAll(root, 0o755)
	defer os.RemoveAll(root)
	dep := "DepBR2" + strconv.FormatInt(time.Now().Unix()%1000000000, 10)
	fc := fakecloud.Start(dep)
	defer fc.Close()
	fc.SetAccessKey(testKey)
	bb := loadBusybox(t, fc)
	defer func() {
		cs, _ := dc.ContainerList(context.Background(), true, "opm.deployment="+dep)
		for _, c := range cs {
			_ = dc.ContainerRemove(context.Background(), c.ID, true)
		}
		_ = dc.NetworkRemove(context.Background(), "opm_"+dep+"_app")
		_ = dc.NetworkRemove(context.Background(), "opm_"+dep+"_db")
	}()
	var total int64
	for _, l := range bb.layers {
		total += l.SizeBytes
	}
	spec := model.ImageSpec{ServiceSlug: "bb", Version: "1", Digest: "sha256:" + bb.manifestHex, ConfigDigest: "sha256:" + bb.configHex,
		Platform: "linux/" + runtime.GOARCH, ManifestMediaType: bb.mediaType, Layers: bb.layers, SizeBytes: total, Kind: "service",
		Ports: []model.ImagePort{}, Env: []model.ImageEnv{}, Volumes: []model.ImageVolume{}, StopTimeoutSec: 1, Command: []string{"sh"}}
	cat := model.RevisionCatalog{Services: map[string]model.CatalogService{"bb": {ServiceSlug: "bb", Label: "busybox"}}, Images: map[string]model.ImageSpec{"bb:1": spec}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	app := &site{slug: "app", agentPort: freePort(t), alias: freePort(t), httpP: freePort(t)}
	db := &site{slug: "db", agentPort: freePort(t), alias: freePort(t), httpP: freePort(t)}
	ctr := func(slug, gen, cmd string) model.Container {
		return model.Container{ContainerSlug: slug, ServiceSlug: "bb", Version: "1", Enabled: true, Env: []model.ContainerEnv{{Name: "GEN", Value: ptr(gen)}},
			Ports: []model.ContainerPort{}, Volumes: []model.ContainerMount{}, Command: []string{"sh", "-c", cmd}, Restart: "always", Depends: []model.ContainerRef{}}
	}
	mk := func(gen, appCmd string) model.Config {
		siteCfg := func(s *site, c model.Container) model.SiteConfig {
			return model.SiteConfig{Host: ptr("127.0.0.1"), AgentPort: s.agentPort, AgentGuiAliasPort: &s.alias, Volumes: &[]model.SiteVolume{}, Containers: []model.Container{c},
				Depends: []model.SiteRef{}, Edge: model.Edge{HTTPPort: s.httpP, Hosts: []model.EdgeHost{}, Routes: []model.EdgeRoute{}}}
		}
		return model.Config{Variables: []model.Variable{{VariableSlug: "gen", Label: "Gen", Type: "string", Value: model.VariableValue{V: gen}}}, Secrets: []model.SecretDef{}, Files: []model.FileDef{},
			Sites: []model.Site{{SiteSlug: "db", Label: "DB", Config: siteCfg(db, ctr("cache", gen, "sleep 3600"))}, {SiteSlug: "app", Label: "App", Config: siteCfg(app, ctr("web", gen, appCmd))}}}
	}
	start := func(s *site) {
		dir := filepath.Join(root, s.slug)
		tok := filepath.Join(dir, "install-token")
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(tok, []byte(fc.CreateInstallToken(s.slug)), 0o600)
		cfg := model.AgentConfigFile{APIBase: fc.URL, DeploymentID: dep, SiteSlug: s.slug, DataDir: filepath.Join(dir, "data"), AgentPort: s.agentPort,
			GuiAliasPort: model.Some(s.alias), AdvertiseHost: "127.0.0.1", InstallTokenFile: tok, EdgeHTTPPort: model.Some(s.httpP), ContainerNames: "prefixed"}
		a, err := New(Options{Config: cfg, Timings: fastTimings(), ApproveWait: time.Second, CompleteTimeout: 20 * time.Second, LogLevel: "debug", DisableSchedule: true})
		if err != nil {
			t.Fatal(err)
		}
		a.drv.NoneReadyAfter = 2 * time.Second
		s.a = a
		jar, _ := cookiejar.New(nil)
		s.cl = &http.Client{Jar: jar, Timeout: 10 * time.Minute}
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel, s.done = cancel, make(chan struct{})
		go func() { _ = a.Run(ctx); close(s.done) }()
	}
	start(db)
	start(app)
	defer func() {
		for _, s := range []*site{app, db} {
			s.cancel()
			<-s.done
		}
	}()
	waitFor(t, "enroll", 30*time.Second, func() bool { _, a1 := fc.Agent("app"); _, a2 := fc.Agent("db"); return a1 && a2 })
	waitFor(t, "mesh", 30*time.Second, func() bool { return app.a.reachable("db") && db.a.reachable("app") })
	waitFor(t, "key hash", 20*time.Second, func() bool { h1, _ := app.a.keyHash(); h2, _ := db.a.keyHash(); return h1 != "" && h2 != "" })
	for _, s := range []*site{app, db} {
		if c, _ := s.api(t, "POST", "/_opm/api/login", model.LoginRequest{AccessKey: testKey}, nil); c != 200 {
			t.Fatal("login")
		}
		var ar model.ActorResponse
		s.api(t, "POST", "/_opm/api/actor", model.ActorRequest{Email: "ops@example.com"}, &ar)
		s.actor = ar.ActorToken
	}
	waitRun := func(s *site, id string) model.UpdateRun {
		var run model.UpdateRun
		waitFor(t, "run", 5*time.Minute, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/updates/"+id, nil, &run)
			return c == 200 && run.FinishedAt != nil
		})
		return run
	}
	merge := func(s *site, srcID, msg string) model.UpdateRun {
		t.Helper()
		var plan model.MergePlan
		if c, b := s.api(t, "POST", "/_opm/api/branch/compare", model.LocalCompareRequest{Source: map[string]any{"cloudCommitId": srcID}}, &plan); c != 200 {
			t.Fatalf("compare %d %s", c, b)
		}
		var ch []model.MergeChoice
		for _, it := range plan.Items {
			ch = append(ch, model.MergeChoice{Key: it.Key, Take: "source"})
		}
		var head *string
		if plan.Target.Head != nil {
			head = &plan.Target.Head.CommitID
		}
		var run model.UpdateRun
		if c, b := s.api(t, "POST", "/_opm/api/branch/merge", model.LocalMergeRequest{SourceCommitID: srcID, TargetHead: head, Choices: ch, Message: msg}, &run); c != 202 {
			t.Fatalf("merge %d %s", c, b)
		}
		return waitRun(s, run.UpdateID)
	}
	// 1. cloud commit 1; db merges ONLY its own branch, app stays behind
	cm1 := fc.Commit(mk("1", "sleep 3600"), cat, "release 1")
	waitFor(t, "cloud head on both", 30*time.Second, func() bool {
		return app.a.cloudHeadRef() != nil && db.a.cloudHeadRef() != nil && db.a.cloudHeadRef().CommitID == cm1.CommitID && app.a.cloudHeadRef().CommitID == cm1.CommitID
	})
	if r := merge(db, cm1.CommitID, "db takes release 1"); r.Result == nil || !r.Result.Ok || len(r.Sites) != 1 {
		t.Fatalf("db merge %+v", r.Result)
	}
	if app.a.head() != nil {
		t.Fatal("app must not change when db merges")
	}
	exDB, _ := db.a.drv.Existing(context.Background())
	if _, ok := exDB["cache"]; !ok {
		t.Fatal("cache not running on db")
	}
	// 2. cloud commit 2; app sees db clean but ahead of its own last merge? db merged 1 → strictly behind on 2; app never merged → behind
	cm2 := fc.Commit(mk("2", "sleep 3600"), cat, "release 2")
	waitFor(t, "cloud head 2", 30*time.Second, func() bool {
		return app.a.cloudHeadRef() != nil && app.a.cloudHeadRef().CommitID == cm2.CommitID && db.a.cloudHeadRef() != nil && db.a.cloudHeadRef().CommitID == cm2.CommitID
	})
	var st model.AgentBranchState
	waitFor(t, "both sites can fast-forward", 30*time.Second, func() bool {
		app.api(t, "GET", "/_opm/api/branch", nil, &st)
		ok := 0
		for _, s := range st.Sites {
			if s.CanFastForward {
				ok++
			}
		}
		if ok != 2 {
			for _, x := range st.Sites {
				if x.Reason != nil {
					t.Logf("site %s: %s", x.SiteSlug, *x.Reason)
				}
			}
			var u model.UncommittedResponse
			db.api(t, "GET", "/_opm/api/branch/uncommitted", nil, &u)
			for _, it := range u.Items {
				t.Logf("db uncommitted %s %s %v -> %v", it.Key, it.Detail, it.Target.Value, it.Source.Value)
			}
		}
		return ok == 2
	})
	var run model.UpdateRun
	if c, b := app.api(t, "POST", "/_opm/api/branch/fast-forward", model.FastForwardAllRequest{SourceCommitID: cm2.CommitID, Sites: []string{"app", "db"}, Message: "release 2 everywhere"}, &run); c != 202 {
		t.Fatalf("fast-forward %d %s", c, b)
	}
	if r := waitRun(app, run.UpdateID); r.Result == nil || !r.Result.Ok || len(r.Sites) != 2 {
		t.Fatalf("fast-forward run %+v", r.Result)
	}
	for _, s := range []*site{app, db} {
		hc := s.a.headCommit()
		if hc == nil || hc.Kind != "merge" || hc.Source == nil || *hc.Source.CommitID != cm2.CommitID || hc.Message != "release 2 everywhere" {
			t.Fatalf("%s head after fast-forward: %+v", s.slug, hc)
		}
		if mf := s.a.mergedFrom(); mf == nil || mf.CommitID != cm2.CommitID {
			t.Fatalf("%s merged from %+v", s.slug, mf)
		}
	}
	// 3. a failing container: the merge rolls back and leaves NO commit
	before := app.a.headCommit().CommitID
	cm3 := fc.Commit(mk("3", "exit 1"), cat, "broken release")
	waitFor(t, "cloud head 3", 30*time.Second, func() bool { return app.a.cloudHeadRef() != nil && app.a.cloudHeadRef().CommitID == cm3.CommitID })
	if r := merge(app, cm3.CommitID, "try the broken release"); r.Result == nil || r.Result.Ok {
		t.Fatalf("the broken merge must fail: %+v", r.Result)
	}
	if app.a.headCommit().CommitID != before {
		t.Fatal("a failed merge must not commit")
	}
	if len(activityData(t, app.a, "branch.merge-failed")) == 0 {
		t.Fatal("branch.merge-failed activity missing")
	}
	// 4. legacy participant: an old coordinator (db, simulated) runs a proposal update over app
	exApp, _ := app.a.drv.Existing(context.Background())
	webID := exApp["web"].ID
	lcfg := mk("legacy", "sleep 3600")
	normalizeConfig(&lcfg)
	normalizeCatalog(&cat)
	h, _ := canonical.HashCanonical(lcfg)
	ch, _ := canonical.HashCanonical(cat)
	lrev := model.Revision{RevID: newID(), Seq: 1, Origin: "cloud", Actor: "uid", CreatedAt: nowTs(), Config: lcfg, Hash: h, Catalog: cat, CatalogHash: ch, Summary: "legacy", Kind: "change"}
	plan := &model.UpdatePlan{UpdateID: newID(), Kind: "update", Revision: lrev, PreSnapshotID: newID(), Sites: []string{"app"}, Order: []string{"app"}, Coordinator: "db", ApprovedBy: "old@example.com", ApprovedAt: nowTs()}
	for _, step := range []string{model.StepLock, model.StepSnapshot, model.StepApply, model.StepVerify, model.StepCommit} {
		req := model.UpdateStepRequest{Step: step}
		if step == model.StepLock {
			req.Plan = plan
		}
		if err := db.a.peerJSON(context.Background(), "app", "POST", "/_opm/peer/update/"+plan.UpdateID+"/step", req, nil, 10*time.Second); err != nil {
			t.Fatalf("legacy %s: %v", step, err)
		}
		waitFor(t, "legacy "+step, 20*time.Second, func() bool {
			var x model.UpdateSiteState
			if err := db.a.peerJSON(context.Background(), "app", "GET", "/_opm/peer/update/"+plan.UpdateID, nil, &x, 10*time.Second); err != nil {
				return false
			}
			return x.Step != nil && *x.Step == step && x.State == "ok"
		})
	}
	if app.a.lock() != nil {
		t.Fatal("the legacy participant must release the lock")
	}
	if lh := app.a.legacyHead(); lh == nil || lh.RevID != lrev.RevID {
		t.Fatalf("legacy head %+v", lh)
	}
	exApp, _ = app.a.drv.Existing(context.Background())
	if exApp["web"].ID != webID || app.a.headCommit().CommitID != before {
		t.Fatal("a legacy update must not touch a 0.14 site")
	}
	t.Logf("TestTwoSiteBranches ok in %s", time.Since(began).Round(time.Second))
}
