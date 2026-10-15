package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	"opm/agent/internal/docker"
	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
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

// TestVolumeContentDocker (§21 #101, real Docker + the fake cloud): a tree seed (nested folder, a render-expressions
// file, modes, an empty folder) is written at APPLY; a container modifies files (incl. excluded ones); Snapshot now →
// the fake cloud has the version (excludes left out); a cloud edit + Send → plan = fast-forward → apply (⏩ speed 4)
// with restart → files on disk, excludes untouched, the user container restarted, push applied + after-apply version;
// live changes again + a conflicting cloud edit → plan shows the conflict → per-file resolution; a cloud snapshot
// request is answered; heartbeat reports; then the placeholder rule with a real nested File bind mount.
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

	// content blobs
	put := func(s string) model.VolumeTreeEntry {
		h := fc.PutBlob([]byte(s))
		return model.VolumeTreeEntry{Sha256: "sha256:" + h, Size: int64(len(s)), Mode: "0644", Render: "none"}
	}
	ent := func(path, body, mode, render string) model.VolumeTreeEntry {
		e := put(body)
		e.Path, e.Mode, e.Render = path, mode, render
		return e
	}
	treeSeed := model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{
		ent("flows.json", `[{"id":"a"}]`, "0644", "none"),
		ent("settings.js", "module.exports = { site: '{{ self.slug }}' }\n", "0640", "expressions"),
		ent("lib/nested/util.js", "#!/bin/sh\necho util\n", "0755", "none"),
	}, Dirs: []string{"empty/inner"}}
	settingsFile := []byte("module.exports = { uiPort: 1880 }\n")
	settingsHex := fc.PutBlob(settingsFile)
	placeholderSeed := model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{ent("settings.js", string(settingsFile), "0644", "none"), ent("flows.json", "[]", "0644", "none")}, Dirs: []string{}}
	dir := "dir"
	mkConfig := func(gen string, withFile bool) model.Config {
		mounts := []model.ContainerMount{
			{MountPath: ptr("/flows"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "flows"}},
			{MountPath: ptr("/data"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "nr"}},
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
					{VolumeSlug: "flows", Label: "Flows", Backup: true, Kind: &dir, Seed: model.Some(treeSeed), Persist: &model.VolumePersist{Exclude: []string{"node_modules/", "*.log"}, MaxBytes: 50 << 20, Auto: true}},
					nr,
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
	a.vsync().scanEvery = time.Hour // the scanner is covered by TestVolumeSyncScanner; snapshots here are explicit
	s.a = a
	jar, _ := cookiejar.New(nil)
	s.cl = &http.Client{Jar: jar, Timeout: 120 * time.Second}
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
	approve := func(revID string) {
		t.Helper()
		var det model.ProposalDetailResponse
		waitFor(t, "gate open", 240*time.Second, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/proposals/"+revID, nil, &det)
			if c == 200 && det.Proposal.Sites["edge"].Staging == model.StagingFailed {
				t.Fatalf("staging failed: %v", *det.Proposal.Sites["edge"].Error)
			}
			return c == 200 && det.Gate.Open
		})
		var st model.ApproveStartResponse
		if c, b := s.api(t, "POST", "/_opm/api/proposals/"+revID+"/approve/start", nil, &st); c != 201 {
			t.Fatalf("approve/start %d %s", c, b)
		}
		time.Sleep(1100 * time.Millisecond)
		var res model.ApproveResponse
		if c, b := s.api(t, "POST", "/_opm/api/proposals/"+revID+"/approve", model.ApproveRequest{IntentID: st.Intent.IntentID, Typed: "ACCEPT"}, &res); c != 201 {
			t.Fatalf("approve %d %s", c, b)
		}
		var run model.UpdateRun
		waitFor(t, "update finished", 300*time.Second, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/updates/"+res.UpdateID, nil, &run)
			return c == 200 && run.FinishedAt != nil
		})
		if run.Result == nil || !run.Result.Ok {
			t.Fatalf("update failed: %+v", run.Result)
		}
	}
	rev1, _ := fc.Publish(mkConfig("1", true), cat, "volume content")
	approve(rev1.RevID)

	flowsDir := filepath.Join(a.dataDir, "volumes", "flows")
	nrDir := filepath.Join(a.dataDir, "volumes", "nr")
	// 1. the tree seed: nested folder, rendered once per site, modes, empty folder, marker, activity
	if got := readHost(t, filepath.Join(flowsDir, "settings.js")); got != "module.exports = { site: 'edge' }\n" {
		t.Fatalf("rendered settings.js: %q", got)
	}
	if got := readHost(t, filepath.Join(flowsDir, "lib/nested/util.js")); got != "#!/bin/sh\necho util\n" {
		t.Fatalf("nested: %q", got)
	}
	if st, _ := os.Stat(filepath.Join(flowsDir, "lib/nested/util.js")); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", st.Mode())
	}
	if st, _ := os.Stat(filepath.Join(flowsDir, "settings.js")); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
	if st, err := os.Stat(filepath.Join(flowsDir, "empty/inner")); err != nil || !st.IsDir() {
		t.Fatal("empty folder missing")
	}
	if _, err := os.Stat(filepath.Join(flowsDir, ".opm-seeded")); err != nil {
		t.Fatal("marker missing")
	}
	seeded := activityData(t, a, "volume.seeded")
	if len(seeded) != 1 || seeded[0]["source"] != "tree" || seeded[0]["volumeSlug"] != "flows" {
		t.Fatalf("volume.seeded %+v", seeded)
	}
	// the File bind-mounted inside the writable nr volume left Docker's 0-byte mountpoint on the host
	if st, err := os.Stat(filepath.Join(nrDir, "settings.js")); err != nil || st.Size() != 0 {
		t.Fatalf("expected the 0-byte mountpoint: %v %v", st, err)
	}

	// container name + docker helpers
	cs, _ := dc.ContainerList(context.Background(), true, "opm.deployment="+dep)
	var cname string
	for _, c := range cs {
		if c.Labels["opm.container"] == "app" {
			cname = strings.TrimPrefix(c.Names[0], "/")
		}
	}
	if cname == "" {
		t.Fatal("app container not found")
	}
	dexec := func(script string) {
		t.Helper()
		if out, err := exec.Command("docker", "exec", cname, "sh", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("docker exec: %v %s", err, out)
		}
	}
	startedAt := func() string {
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.StartedAt}}", cname).Output()
		return strings.TrimSpace(string(out))
	}
	vs := "/_opm/api/sites/edge/volume-sync/flows"

	waitFor(t, "the cloud knows the head", 20*time.Second, func() bool {
		ad, _ := fc.Agent("edge")
		return ad.HeadRevID != nil && *ad.HeadRevID == rev1.RevID
	})
	// 2. the container modifies files (incl. excluded ones) → Snapshot now → uploaded to the cloud
	dexec(`echo '[{"id":"live1"}]' > /flows/flows.json && mkdir -p /flows/node_modules/pkg && echo dep > /flows/node_modules/pkg/i.js && echo log > /flows/app.log && rm -r /flows/empty`)
	var snap model.VolumeSyncSnapshotResponse
	if c, b := s.api(t, "POST", vs+"/snapshot", map[string]any{"note": "first"}, &snap); c != 201 || !snap.Created {
		t.Fatalf("snapshot %d %s", c, b)
	}
	if c, _ := s.api(t, "POST", vs+"/snapshot", nil, &snap); c != 201 || snap.Created {
		t.Fatalf("an unchanged snapshot must not be created (%d %+v)", c, snap)
	}
	waitFor(t, "snapshot uploaded", 30*time.Second, func() bool {
		if len(fc.VolumeVersions("edge", "flows")) == 1 {
			return true
		}
		for _, r := range a.records(`WHERE status = 'queued'`) {
			if r.Local.Error != nil {
				t.Logf("queued %s: %s", r.Local.SnapshotID, *r.Local.Error)
			}
		}
		return false
	})
	v1 := fc.VolumeVersions("edge", "flows")[0]
	tree1, _ := fc.VolumeTree(v1.TreeHash)
	paths := func(tr model.VolumeTree) string {
		var ps []string
		for _, e := range tr.Entries {
			ps = append(ps, e.Path)
		}
		return strings.Join(ps, ",") + "|" + strings.Join(tr.Dirs, ",")
	}
	if got := paths(tree1); got != "flows.json,lib/nested/util.js,settings.js|" {
		t.Fatalf("snapshot tree %s", got)
	}
	if v1.ExcludedCount < 2 || *v1.Trigger != "manual-agent" || v1.CreatedBy != "ops@example.com" || v1.Note != "first" {
		t.Fatalf("version %+v", v1)
	}
	waitFor(t, "snapshot-uploaded activity", 10*time.Second, func() bool { return len(activityData(t, a, "volume-content.snapshot-uploaded")) == 1 })

	// 3. a cloud edit + Send (restart) → fast-forward
	e1, err := fc.CreateVolumeEdit("edge", "flows", map[string][]byte{"lib/nested/util.js": []byte("#!/bin/sh\necho util v2\n"), "new/added.txt": []byte("added in the cloud\n")}, []string{"settings.js"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fc.SendVolumeContent("edge", "flows", e1.VersionID, true, "please apply"); err != nil {
		t.Fatal(err)
	}
	var st model.VolumeSyncStatus
	waitFor(t, "push in the view", 30*time.Second, func() bool {
		c, _ := s.api(t, "GET", vs, nil, &st)
		return c == 200 && st.Push != nil
	})
	if st.State != "cloud-ahead" {
		t.Fatalf("state %s", st.State)
	}
	var plan model.VolumePushPlan
	if c, b := s.api(t, "GET", vs+"/plan", nil, &plan); c != 200 || !plan.FastForward || plan.Counts["conflict"] != 0 || len(plan.Items) != 3 {
		t.Fatalf("plan %d %s", c, b)
	}
	// per-file bytes for the diff viewer
	if c, b := s.api(t, "GET", vs+"/file?side=cloud&path=lib/nested/util.js", nil, nil); c != 200 || string(b) != "#!/bin/sh\necho util v2\n" {
		t.Fatalf("file cloud %d %q", c, b)
	}
	if c, b := s.api(t, "GET", vs+"/file?side=live&path=lib/nested/util.js", nil, nil); c != 200 || string(b) != "#!/bin/sh\necho util\n" {
		t.Fatalf("file live %d %q", c, b)
	}
	if c, _ := s.api(t, "GET", vs+"/file?side=live&path=app.log", nil, nil); c != 422 {
		t.Fatalf("excluded file must be refused: %d", c)
	}
	before := startedAt()
	var ist model.ApproveStartResponse
	if c, b := s.api(t, "POST", vs+"/apply/start", nil, &ist); c != 201 || ist.Intent.Kind != "volume-content-apply" {
		t.Fatalf("apply/start %d %s", c, b)
	}
	if c, b := s.api(t, "POST", "/_opm/api/sites/edge/intents/"+ist.Intent.IntentID+"/speed", model.IntentSpeedRequest{Speed: 4}, nil); c != 200 {
		t.Fatalf("speed %d %s", c, b)
	}
	time.Sleep(1100 * time.Millisecond)
	var ap model.VolumeSyncApplyResponse
	if c, b := s.api(t, "POST", vs+"/apply", model.VolumeSyncApplyRequest{IntentID: ist.Intent.IntentID, PushID: plan.PushID, Resolution: "overwrite", Restart: true, LiveTreeHash: plan.LiveTreeHash}, &ap); c != 200 {
		t.Fatalf("apply %d %s", c, b)
	}
	if ap.Result.Resolution != "fast-forward" || ap.Result.Written != 2 || ap.Result.Deleted != 1 || len(ap.Result.Restarted) != 1 || ap.Result.SnapshotID == nil || ap.Result.AfterVersionID == nil {
		t.Fatalf("result %+v", ap.Result)
	}
	if got := readHost(t, filepath.Join(flowsDir, "lib/nested/util.js")); got != "#!/bin/sh\necho util v2\n" {
		t.Fatalf("util after apply %q", got)
	}
	if st, _ := os.Stat(filepath.Join(flowsDir, "lib/nested/util.js")); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode kept %v", st.Mode())
	}
	if got := readHost(t, filepath.Join(flowsDir, "new/added.txt")); got != "added in the cloud\n" {
		t.Fatalf("added %q", got)
	}
	if _, err := os.Stat(filepath.Join(flowsDir, "settings.js")); !os.IsNotExist(err) {
		t.Fatal("settings.js must be deleted")
	}
	if readHost(t, filepath.Join(flowsDir, "node_modules/pkg/i.js")) != "dep\n" || readHost(t, filepath.Join(flowsDir, "app.log")) != "log\n" {
		t.Fatal("excluded paths must stay untouched")
	}
	if _, err := os.Stat(filepath.Join(flowsDir, ".opm-seeded")); err != nil {
		t.Fatal("marker must stay")
	}
	if after := startedAt(); after == before {
		t.Fatal("the user container must have been restarted")
	}
	waitFor(t, "push applied in the cloud", 20*time.Second, func() bool {
		ps := fc.VolumePushes("edge", "flows")
		return len(ps) == 1 && ps[0].Status == "applied" && ps[0].Result != nil && ps[0].Result.AfterVersionID != nil
	})
	if ad := activityData(t, a, "volume-content.applied"); len(ad) != 1 || ad[0]["resolution"] != "fast-forward" || ad[0]["fastForward"] == nil {
		t.Fatalf("applied activity %+v", ad)
	}
	var bkNote string
	_ = a.db.QueryRow(`SELECT summary FROM activities WHERE type = 'snapshot.created' ORDER BY seq DESC LIMIT 1`).Scan(&bkNote)
	if want := fmt.Sprintf("before applying cloud version seq %d", e1.Seq); !strings.Contains(bkNote, want) {
		t.Fatalf("Backups snapshot note %q, want %q", bkNote, want)
	}

	// 4. live changes again + a conflicting cloud edit → conflict → per file
	dexec(`echo '[{"id":"live2"}]' > /flows/flows.json && echo mine > /flows/mine.txt`)
	e2, _ := fc.CreateVolumeEdit("edge", "flows", map[string][]byte{"flows.json": []byte(`[{"id":"cloud2"}]`), "b.txt": []byte("bee\n")}, nil, "")
	if _, err := fc.SendVolumeContent("edge", "flows", e2.VersionID, false, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second push", 30*time.Second, func() bool {
		c, _ := s.api(t, "GET", vs, nil, &st)
		return c == 200 && st.Push != nil && st.Push.VersionID == e2.VersionID
	})
	if c, b := s.api(t, "GET", vs+"/plan", nil, &plan); c != 200 || plan.FastForward || plan.Counts["conflict"] != 1 || plan.Counts["cloud"] != 1 || plan.Counts["live"] != 1 {
		t.Fatalf("conflict plan %d %s", c, b)
	}
	if s.api(t, "GET", vs, nil, &st); st.State != "conflict" { // the plan rescanned the live tree
		t.Fatalf("state %s", st.State)
	}
	if ca := activityData(t, a, "volume-content.conflict"); len(ca) != 1 {
		t.Fatalf("conflict activity %+v", ca)
	}
	if c, b := s.api(t, "POST", vs+"/apply/start", nil, &ist); c != 201 {
		t.Fatalf("apply/start %d %s", c, b)
	}
	time.Sleep(1100 * time.Millisecond)
	// a choice is required for the conflict
	if c, _ := s.api(t, "POST", vs+"/apply", model.VolumeSyncApplyRequest{IntentID: ist.Intent.IntentID, PushID: plan.PushID, Resolution: "per-file", LiveTreeHash: plan.LiveTreeHash}, nil); c != 422 {
		t.Fatalf("per-file without a choice: %d", c)
	}
	if c, b := s.api(t, "POST", vs+"/apply/start", nil, &ist); c != 201 {
		t.Fatalf("apply/start %d %s", c, b)
	}
	time.Sleep(1100 * time.Millisecond)
	if c, b := s.api(t, "POST", vs+"/apply", model.VolumeSyncApplyRequest{IntentID: ist.Intent.IntentID, PushID: plan.PushID, Resolution: "per-file", Choices: map[string]string{"flows.json": "live"}, LiveTreeHash: plan.LiveTreeHash}, &ap); c != 200 {
		t.Fatalf("apply per-file %d %s", c, b)
	}
	if ap.Result.Resolution != "per-file" || ap.Result.KeptLive != 2 || ap.Result.Written != 1 || len(ap.Result.Restarted) != 0 {
		t.Fatalf("per-file result %+v", ap.Result)
	}
	if readHost(t, filepath.Join(flowsDir, "flows.json")) != "[{\"id\":\"live2\"}]\n" || readHost(t, filepath.Join(flowsDir, "b.txt")) != "bee\n" || readHost(t, filepath.Join(flowsDir, "mine.txt")) != "mine\n" {
		t.Fatal("per-file tree on disk")
	}

	// 5. a cloud snapshot request is answered once
	rid := fc.RequestVolumeSnapshot("edge", "flows")
	waitFor(t, "request answered", 30*time.Second, func() bool { return fc.VolumeSnapshotRequest("edge", "flows") == nil })
	vv := fc.VolumeVersions("edge", "flows")
	if last := vv[len(vv)-1]; last.RequestID == nil || *last.RequestID != rid || *last.Trigger != "manual-cloud" {
		t.Fatalf("answer %+v", last)
	}
	waitFor(t, "heartbeat volumeSync", 20*time.Second, func() bool {
		r := fc.VolumeSyncLive("edge", "flows")
		return r != nil && r.LiveTreeHash != nil && r.State == "in-sync"
	})

	// 6. the placeholder rule: rev2 drops the File mount and gives nr its own content
	rev2, _ := fc.Publish(mkConfig("2", false), cat, "settings.js moved into the volume")
	approve(rev2.RevID)
	if got := readHost(t, filepath.Join(nrDir, "settings.js")); got != string(settingsFile) {
		t.Fatalf("placeholder not filled: %q", got)
	}
	if _, err := os.Stat(filepath.Join(nrDir, "flows.json")); !os.IsNotExist(err) {
		t.Fatal("a non-empty volume is never seeded (only the placeholder is filled)")
	}
	if pf := activityData(t, a, "volume.placeholder-filled"); len(pf) != 1 || pf[0]["path"] != "settings.js" || pf[0]["formerFileSlug"] != "settings" {
		t.Fatalf("placeholder activity %+v", pf)
	}
	cs, _ = dc.ContainerList(context.Background(), true, "opm.deployment="+dep) // recreated (mounts changed)
	for _, c := range cs {
		if c.Labels["opm.container"] == "app" {
			cname = strings.TrimPrefix(c.Names[0], "/")
		}
	}
	if out, err := exec.Command("docker", "exec", cname, "cat", "/data/settings.js").CombinedOutput(); err != nil || !bytes.Equal(out, settingsFile) {
		t.Fatalf("container view of settings.js: %v %q", err, out)
	}
}
