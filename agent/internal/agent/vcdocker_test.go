package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
	"opm/agent/internal/volcontent"
)

// activityData returns the data of every activity of a type (oldest first).
func activityData(t *testing.T, a *Agent, typ string) []map[string]any {
	t.Helper()
	rows, err := a.db.Query(`SELECT data FROM activities WHERE type = ? ORDER BY seq`, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		m := map[string]any{}
		_ = json.Unmarshal([]byte(s), &m)
		out = append(out, m)
	}
	return out
}

func readHost(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// TestVolumeContentDocker (§21 #101 + #117, real Docker + the fake cloud, branch mode): a cloud commit with a persist
// volume's committed content (nested folder, modes, an empty folder) and a non-persist starter tree (rendered once per
// site) is merged and written; the container changes files (incl. excluded ones) → uncommitted content items that never
// block a merge → committed on the site (drift commit) → the bytes reach the cloud with the push; a cloud commit that
// changes other files merges per file (server change kept, cloud change written, users recreated, excludes untouched);
// a file both sides changed is a conflict (unresolved → 422, Keep this site's); discard writes the committed content
// back; a revert over uncommitted server changes is refused (409); the File placeholder rule still fills a 0-byte
// former mountpoint.
func TestVolumeContentDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dc := docker.New(docker.DefaultHost(""))
	if err := dc.Ping(context.Background()); err != nil {
		t.Skip("no docker: ", err)
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".opm-agent-test", "vc-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	dep := "DepVC" + strconv.FormatInt(time.Now().Unix()%1000000000, 10)
	fc := fakecloud.Start(dep)
	defer fc.Close()
	fc.SetAccessKey(testKey)
	bb := loadBusybox(t, fc)
	defer func() {
		cs, _ := dc.ContainerList(context.Background(), true, "opm.deployment="+dep)
		for _, c := range cs {
			_ = dc.ContainerRemove(context.Background(), c.ID, true)
		}
		_ = dc.NetworkRemove(context.Background(), "opm_"+dep+"_edge")
	}()
	s := &site{slug: "edge", agentPort: freePort(t), alias: freePort(t), httpP: freePort(t)}
	var total int64
	for _, l := range bb.layers {
		total += l.SizeBytes
	}
	spec := model.ImageSpec{ServiceSlug: "bb", Version: "1", Digest: "sha256:" + bb.manifestHex, ConfigDigest: "sha256:" + bb.configHex,
		Platform: "linux/" + runtime.GOARCH, ManifestMediaType: bb.mediaType, Layers: bb.layers, SizeBytes: total, Kind: "service",
		Ports: []model.ImagePort{{Name: "http", Port: num(8080), Protocol: "tcp"}}, Env: []model.ImageEnv{}, Volumes: []model.ImageVolume{},
		Healthcheck: &model.HealthCheck{Type: "http", PortName: "http", Path: "/ok", ExpectStatus: 200, IntervalSec: 1, TimeoutSec: 2, Retries: 5, StartPeriodSec: 1}, StopTimeoutSec: 2,
		Command: []string{"sh"}}
	cat := model.RevisionCatalog{Services: map[string]model.CatalogService{"bb": {ServiceSlug: "bb", Label: "busybox"}}, Images: map[string]model.ImageSpec{"bb:1": spec}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}

	ent := func(path, body, mode, render string) model.VolumeTreeEntry {
		h := fc.PutBlob([]byte(body))
		return model.VolumeTreeEntry{Path: path, Sha256: model.Sha256("sha256:" + h), Size: int64(len(body)), Mode: mode, Render: render}
	}
	flows := map[string]model.VolumeTreeEntry{
		"flows.json":         ent("flows.json", `[{"id":"a"}]`, "0644", "none"),
		"lib/nested/util.js": ent("lib/nested/util.js", "#!/bin/sh\necho util\n", "0755", "none"),
	}
	flowDirs := []string{"empty/inner"}
	treeOfMap := func(m map[string]model.VolumeTreeEntry, dirs []string) model.VolumeSeed {
		var es []model.VolumeTreeEntry
		for _, e := range m {
			es = append(es, e)
		}
		t := volcontent.Normalize(model.VolumeTree{Entries: es, Dirs: append([]string{}, dirs...)})
		return model.VolumeSeed{Kind: "tree", Tree: t.Entries, Dirs: t.Dirs}
	}
	starter := model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{ent("settings.js", "module.exports = { site: '{{ self.slug }}' }\n", "0640", "expressions")}, Dirs: []string{}}
	settingsFile := []byte("module.exports = { uiPort: 1880 }\n")
	settingsHex := fc.PutBlob(settingsFile)
	placeholderSeed := model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{ent("settings.js", string(settingsFile), "0644", "none"), ent("flows.json", "[]", "0644", "none")}, Dirs: []string{}}
	dir := "dir"
	mkConfig := func(gen string, withFile bool) model.Config {
		mounts := []model.ContainerMount{
			{MountPath: ptr("/flows"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "flows"}},
			{MountPath: ptr("/data"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "nr"}},
			{MountPath: ptr("/starter"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "starter"}},
		}
		nr := model.SiteVolume{VolumeSlug: "nr", Label: "Node-RED", Backup: true, Kind: &dir, Seed: model.Null[model.VolumeSeed]()}
		if withFile {
			mounts = append(mounts, model.ContainerMount{MountPath: ptr("/data/settings.js"), ReadOnly: true, Source: model.VolumeSource{Kind: "file", FileSlug: "settings"}})
		} else {
			nr.Seed = model.Some(placeholderSeed)
		}
		return model.Config{Variables: []model.Variable{}, Secrets: []model.SecretDef{},
			Files: []model.FileDef{{FileSlug: "settings", Label: "Settings", Mime: "text/plain", Editor: "text", MaxBytes: 65536, Render: model.Some("none"), Content: &model.BlobRef{Sha256: "sha256:" + settingsHex, SizeBytes: int64(len(settingsFile))}}},
			Sites: []model.Site{{SiteSlug: "edge", Label: "Edge", Config: model.SiteConfig{Host: ptr("127.0.0.1"), AgentPort: s.agentPort, AgentGuiAliasPort: &s.alias,
				Volumes: &[]model.SiteVolume{
					{VolumeSlug: "flows", Label: "Flows", Backup: true, Kind: &dir, Seed: model.Some(treeOfMap(flows, flowDirs)), Persist: &model.VolumePersist{Exclude: []string{"node_modules/", "*.log"}, MaxBytes: 50 << 20, Auto: true}},
					nr,
					{VolumeSlug: "starter", Label: "Starter", Backup: true, Kind: &dir, Seed: model.Some(starter)},
				},
				Containers: []model.Container{{ContainerSlug: "app", ServiceSlug: "bb", Version: "1", Enabled: true, Env: []model.ContainerEnv{{Name: "GEN", Value: ptr(gen)}},
					Ports: []model.ContainerPort{{PortName: "http", HostIP: "0.0.0.0", Edge: false}}, Volumes: mounts,
					Command: []string{"sh", "-c", `mkdir -p /www && echo ok > /www/ok && httpd -f -p 8080 -h /www`}, Restart: "always", Depends: []model.ContainerRef{}}},
				Depends: []model.SiteRef{}, Edge: model.Edge{HTTPPort: s.httpP, Hosts: []model.EdgeHost{}, Routes: []model.EdgeRoute{}}}}},
		}
	}

	// agent
	dirA := filepath.Join(root, "edge")
	tok := filepath.Join(dirA, "install-token")
	_ = os.MkdirAll(dirA, 0o700)
	_ = os.WriteFile(tok, []byte(fc.CreateInstallToken("edge")), 0o600)
	acfg := model.AgentConfigFile{APIBase: fc.URL, DeploymentID: dep, SiteSlug: "edge", DataDir: filepath.Join(dirA, "data"), AgentPort: s.agentPort,
		GuiAliasPort: model.Some(s.alias), AdvertiseHost: "127.0.0.1", InstallTokenFile: tok, EdgeHTTPPort: model.Null[int](), ContainerNames: "prefixed"}
	a, err := New(Options{Config: acfg, Timings: fastTimings(), ApproveWait: time.Second, CompleteTimeout: 90 * time.Second, LogLevel: "debug", DisableSchedule: true})
	if err != nil {
		t.Fatal(err)
	}
	s.a = a
	jar, _ := cookiejar.New(nil)
	s.cl = &http.Client{Jar: jar, Timeout: 5 * time.Minute}
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
		return run
	}
	state := func() model.AgentBranchState {
		var st model.AgentBranchState
		if c, b := s.api(t, "GET", "/_opm/api/branch", nil, &st); c != 200 {
			t.Fatalf("branch %d %s", c, b)
		}
		return st
	}
	compare := func(srcID string) model.MergePlan {
		t.Helper()
		var plan model.MergePlan
		if c, b := s.api(t, "POST", "/_opm/api/branch/compare", model.LocalCompareRequest{Source: map[string]any{"cloudCommitId": srcID}}, &plan); c != 200 {
			t.Fatalf("compare %d %s", c, b)
		}
		return plan
	}
	merge := func(srcID, msg string, choose func(model.MergeItem) string) (int, []byte, model.UpdateRun) {
		t.Helper()
		plan := compare(srcID)
		var ch []model.MergeChoice
		for _, it := range plan.Items {
			if take := choose(it); take != "" {
				ch = append(ch, model.MergeChoice{Key: it.Key, Take: take})
			}
		}
		var head *string
		if plan.Target.Head != nil {
			head = &plan.Target.Head.CommitID
		}
		var run model.UpdateRun
		c, b := s.api(t, "POST", "/_opm/api/branch/merge", model.LocalMergeRequest{SourceCommitID: srcID, TargetHead: head, Choices: ch, Message: msg}, &run)
		if c == 202 {
			run = waitRun(run.UpdateID)
		}
		return c, b, run
	}
	preselected := func(it model.MergeItem) string {
		if it.Preselect {
			return "source"
		}
		return ""
	}
	theirs := func(it model.MergeItem) string { // what a person ticks: every change only the cloud made
		if it.Kind == "theirs" {
			return "source"
		}
		return ""
	}
	uncommitted := func() model.UncommittedResponse {
		t.Helper()
		if c, b := s.api(t, "POST", "/_opm/api/branch/drift/rescan", nil, nil); c != 200 {
			t.Fatalf("rescan %d %s", c, b)
		}
		var u model.UncommittedResponse
		if c, b := s.api(t, "GET", "/_opm/api/branch/uncommitted", nil, &u); c != 200 {
			t.Fatalf("uncommitted %d %s", c, b)
		}
		return u
	}
	contentKeys := func(u model.UncommittedResponse) []string {
		var ks []string
		for _, it := range u.Items {
			if strings.Contains(it.Key, "/volumes/flows/content") {
				if it.Origin != "volume-content" {
					t.Fatalf("content item %s origin %s", it.Key, it.Origin)
				}
				ks = append(ks, it.Key)
			}
		}
		sort.Strings(ks)
		return ks
	}
	const ck = "sites/edge/volumes/flows/content/"

	// 1. the first cloud commit: committed content + starter content are written at the merge
	cm1 := fc.Commit(mkConfig("1", true), cat, "initial release")
	waitFor(t, "cloud head on the site", 30*time.Second, func() bool { st := state(); return st.Cloud.Head != nil && st.Cloud.Head.CommitID == cm1.CommitID })
	if c, b, run := merge(cm1.CommitID, "take the first release", preselected); c != 202 || run.Result == nil || !run.Result.Ok {
		t.Fatalf("merge 1: %d %s %+v", c, b, run.Result)
	}
	flowsDir := filepath.Join(a.dataDir, "volumes", "flows")
	nrDir := filepath.Join(a.dataDir, "volumes", "nr")
	starterDir := filepath.Join(a.dataDir, "volumes", "starter")
	if got := readHost(t, filepath.Join(starterDir, "settings.js")); got != "module.exports = { site: 'edge' }\n" {
		t.Fatalf("rendered starter settings.js: %q", got)
	}
	if st, _ := os.Stat(filepath.Join(starterDir, "settings.js")); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
	if got := readHost(t, filepath.Join(flowsDir, "lib/nested/util.js")); got != "#!/bin/sh\necho util\n" {
		t.Fatalf("nested: %q", got)
	}
	if st, _ := os.Stat(filepath.Join(flowsDir, "lib/nested/util.js")); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", st.Mode())
	}
	if st, err := os.Stat(filepath.Join(flowsDir, "empty/inner")); err != nil || !st.IsDir() {
		t.Fatal("empty folder missing")
	}
	if ap := activityData(t, a, "volume-content.applied"); len(ap) != 1 || ap[0]["volumeSlug"] != "flows" {
		t.Fatalf("applied activity %+v", ap)
	}
	if u := uncommitted(); len(contentKeys(u)) != 0 {
		t.Fatalf("freshly merged content must be in sync: %+v", u.Items)
	}
	if st, err := os.Stat(filepath.Join(nrDir, "settings.js")); err != nil || st.Size() != 0 {
		t.Fatalf("expected the 0-byte mountpoint: %v %v", st, err)
	}

	cname := func() string {
		cs, _ := dc.ContainerList(context.Background(), true, "opm.deployment="+dep)
		for _, c := range cs {
			if c.Labels["opm.container"] == "app" {
				return strings.TrimPrefix(c.Names[0], "/")
			}
		}
		t.Fatal("app container not found")
		return ""
	}
	dexec := func(script string) {
		t.Helper()
		if out, err := exec.Command("docker", "exec", cname(), "sh", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("docker exec: %v %s", err, out)
		}
	}
	startedAt := func() string {
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.StartedAt}}", cname()).Output()
		return strings.TrimSpace(string(out))
	}

	// 2. the container changes files (incl. excluded ones) → uncommitted content items that never block a merge
	dexec(`echo '[{"id":"live1"}]' > /flows/flows.json && mkdir -p /flows/node_modules/pkg && echo dep > /flows/node_modules/pkg/i.js && echo log > /flows/app.log && rm -r /flows/empty`)
	u := uncommitted()
	if got := strings.Join(contentKeys(u), ","); got != ck+"empty/inner/,"+ck+"flows.json" {
		t.Fatalf("content items %s", got)
	}
	var vs model.VolumeSyncStatus
	if c, b := s.api(t, "GET", "/_opm/api/sites/edge/volume-sync/flows", nil, &vs); c != 200 || vs.State != "live-changed" || vs.Committed == nil {
		t.Fatalf("volume sync %d %s", c, b)
	}
	if st := state(); st.Uncommitted.Drift != 2 {
		t.Fatalf("uncommitted count %+v", st.Uncommitted)
	}
	if c, _ := s.api(t, "POST", "/_opm/api/sites/edge/volume-sync/flows/apply", map[string]any{}, nil); c != 410 {
		t.Fatalf("the old apply must be gone: %d", c)
	}

	// 3. a person commits them (drift commit, nothing applied) → the bytes reach the cloud with the push
	var cr model.LocalCommitResponse
	if c, b := s.api(t, "POST", "/_opm/api/branch/commit", map[string]any{"message": "flows edited in Node-RED", "keys": contentKeys(u)}, &cr); c != 201 || cr.Commit == nil || cr.Commit.Kind != "drift" {
		t.Fatalf("commit drift %d %s", c, b)
	}
	live1 := ""
	for _, v := range *siteOf(&cr.Commit.Config, "edge").Config.Volumes {
		if v.VolumeSlug == "flows" {
			for _, e := range v.SeedOf().Tree {
				if e.Path == "flows.json" {
					live1 = string(e.Sha256)
				}
			}
			if len(v.SeedOf().Dirs) != 0 {
				t.Fatalf("the removed empty folder is still committed: %v", v.SeedOf().Dirs)
			}
		}
	}
	if live1 == "" || live1 == string(flows["flows.json"].Sha256) {
		t.Fatalf("committed flows.json %s", live1)
	}
	waitFor(t, "agent branch + bytes in the cloud", 30*time.Second, func() bool {
		br := fc.AgentBranch("edge")
		_, ok := fc.Blob(live1)
		return len(br) > 0 && br[len(br)-1].CommitID == cr.Commit.CommitID && ok
	})
	if u := uncommitted(); len(contentKeys(u)) != 0 {
		t.Fatalf("committed content must be in sync: %+v", u.Items)
	}

	// 4. a cloud commit changing other files: the server change is kept, the cloud change written, the user recreated
	flows["lib/nested/util.js"] = ent("lib/nested/util.js", "#!/bin/sh\necho util v2\n", "0755", "none")
	flows["new/added.txt"] = ent("new/added.txt", "added in the cloud\n", "0644", "none")
	cm2 := fc.Commit(mkConfig("1", true), cat, "cloud edits util.js")
	waitFor(t, "cm2 on the site", 30*time.Second, func() bool { st := state(); return st.Cloud.Head != nil && st.Cloud.Head.CommitID == cm2.CommitID })
	plan := compare(cm2.CommitID)
	kinds := map[string]string{}
	for _, it := range plan.Items {
		if strings.HasPrefix(it.Key, ck) {
			kinds[strings.TrimPrefix(it.Key, ck)] = it.Kind
		}
	}
	if kinds["lib/nested/util.js"] != "theirs" || kinds["new/added.txt"] != "theirs" || kinds["flows.json"] == "theirs" || kinds["flows.json"] == "conflict" {
		t.Fatalf("plan kinds %v", kinds)
	}
	before := startedAt()
	if c, b, run := merge(cm2.CommitID, "take util v2", theirs); c != 202 || run.Result == nil || !run.Result.Ok {
		t.Fatalf("merge 2: %d %s %+v", c, b, run.Result)
	}
	if got := readHost(t, filepath.Join(flowsDir, "lib/nested/util.js")); got != "#!/bin/sh\necho util v2\n" {
		t.Fatalf("util after merge %q", got)
	}
	if got := readHost(t, filepath.Join(flowsDir, "new/added.txt")); got != "added in the cloud\n" {
		t.Fatalf("added %q", got)
	}
	if got := readHost(t, filepath.Join(flowsDir, "flows.json")); got != "[{\"id\":\"live1\"}]\n" {
		t.Fatalf("the server's flows.json must be kept: %q", got)
	}
	if readHost(t, filepath.Join(flowsDir, "node_modules/pkg/i.js")) != "dep\n" || readHost(t, filepath.Join(flowsDir, "app.log")) != "log\n" {
		t.Fatal("excluded paths must stay untouched")
	}
	if after := startedAt(); after == before {
		t.Fatal("the container mounting the volume must have been recreated")
	}

	// 5. both sides change flows.json → a conflict; unresolved → 422; Keep this site's
	dexec(`echo '[{"id":"live2"}]' > /flows/flows.json && echo mine > /flows/mine.txt`)
	flows["flows.json"] = ent("flows.json", `[{"id":"cloud3"}]`, "0644", "none")
	flows["b.txt"] = ent("b.txt", "bee\n", "0644", "none")
	cm3 := fc.Commit(mkConfig("1", true), cat, "cloud edits flows")
	waitFor(t, "cm3 on the site", 30*time.Second, func() bool { st := state(); return st.Cloud.Head != nil && st.Cloud.Head.CommitID == cm3.CommitID })
	plan = compare(cm3.CommitID)
	var conflict string
	for _, it := range plan.Items {
		if it.Kind == "conflict" {
			conflict = it.Key
		}
	}
	if conflict != ck+"flows.json" || !plan.Target.WorkingClean {
		t.Fatalf("conflict %q clean %v items %+v", conflict, plan.Target.WorkingClean, plan.Items)
	}
	if c, _, _ := merge(cm3.CommitID, "unresolved", theirs); c != 422 {
		t.Fatalf("an unresolved conflict must be refused: %d", c)
	}
	if c, b, run := merge(cm3.CommitID, "keep the site's flows", func(it model.MergeItem) string {
		if it.Key == conflict {
			return "target"
		}
		return theirs(it)
	}); c != 202 || run.Result == nil || !run.Result.Ok {
		t.Fatalf("merge 3: %d %s %+v", c, b, run.Result)
	}
	if readHost(t, filepath.Join(flowsDir, "flows.json")) != "[{\"id\":\"live2\"}]\n" || readHost(t, filepath.Join(flowsDir, "b.txt")) != "bee\n" || readHost(t, filepath.Join(flowsDir, "mine.txt")) != "mine\n" {
		t.Fatal("merge 3 tree on disk")
	}
	if u := uncommitted(); len(contentKeys(u)) != 0 {
		t.Fatalf("the merge commit records the kept server files: %+v", u.Items)
	}

	// 6. a revert over uncommitted server changes is refused; discard writes the committed content back
	dexec(`echo junk > /flows/b.txt`)
	if got := strings.Join(contentKeys(uncommitted()), ","); got != ck+"b.txt" {
		t.Fatalf("items %s", got)
	}
	st := state()
	if c, b := s.api(t, "POST", "/_opm/api/branch/revert", model.LocalRevertRequest{CommitID: st.Local.Head.CommitID, Message: "undo", TargetHead: &st.Local.Head.CommitID}, nil); c != 409 {
		t.Fatalf("revert over server changes: %d %s", c, b)
	}
	var rr model.RunResponse
	if c, b := s.api(t, "POST", "/_opm/api/branch/discard", model.LocalDiscardRequest{Keys: []string{ck + "b.txt"}}, &rr); c != 202 || rr.Run == nil {
		t.Fatalf("discard %d %s", c, b)
	}
	if run := waitRun(rr.Run.UpdateID); run.Result == nil || !run.Result.Ok {
		t.Fatalf("discard run %+v", run.Result)
	}
	if got := readHost(t, filepath.Join(flowsDir, "b.txt")); got != "bee\n" {
		t.Fatalf("discarded b.txt %q", got)
	}

	// 7. the placeholder rule: the File mount is dropped and nr gets its own content
	cm4 := fc.Commit(mkConfig("2", false), cat, "settings.js moved into the volume")
	waitFor(t, "cm4 on the site", 30*time.Second, func() bool { st := state(); return st.Cloud.Head != nil && st.Cloud.Head.CommitID == cm4.CommitID })
	if c, b, run := merge(cm4.CommitID, "settings into the volume", theirs); c != 202 || run.Result == nil || !run.Result.Ok {
		t.Fatalf("merge 4: %d %s %+v", c, b, run.Result)
	}
	if got := readHost(t, filepath.Join(nrDir, "settings.js")); got != string(settingsFile) {
		t.Fatalf("placeholder not filled: %q", got)
	}
	if _, err := os.Stat(filepath.Join(nrDir, "flows.json")); !os.IsNotExist(err) {
		t.Fatal("a non-empty volume is never seeded (only the placeholder is filled)")
	}
	if out, err := exec.Command("docker", "exec", cname(), "cat", "/data/settings.js").CombinedOutput(); err != nil || !bytes.Equal(out, settingsFile) {
		t.Fatalf("container view of settings.js: %v %q", err, out)
	}
}
