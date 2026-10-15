package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/docker"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/validate"
)

// §21 #96 volume manager.

// volHead: site app declares data (dir), other (dir), cfg (file); containers (optional) mount data.
func volHead(t *testing.T, a *Agent, containers []model.Container) *model.Revision {
	t.Helper()
	cfg := validate.EmptyConfig()
	sc := validate.DefaultSiteConfig(ptr("127.0.0.1"))
	file := "file"
	sc.Volumes = &[]model.SiteVolume{{VolumeSlug: "data", Label: "Data", Backup: true}, {VolumeSlug: "other", Label: "Other", Backup: true}, {VolumeSlug: "cfg", Label: "Config file", Backup: true, Kind: &file}}
	sc.Containers = containers
	cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: a.site, Label: a.site, Config: sc})
	cat := *emptyCatalog()
	h, _ := canonical.HashCanonical(cfg)
	ch, _ := canonical.HashCanonical(cat)
	rev := &model.Revision{RevID: newID(), Seq: 1, Origin: "cloud", Actor: "t", CreatedAt: nowTs(), Config: cfg, Hash: h, Catalog: cat, CatalogHash: ch, Summary: "s", Kind: "change"}
	setHead(t, a, rev)
	return rev
}

type volAPI struct {
	t    *testing.T
	a    *Agent
	ck   string
	auth string
	mu   sync.Mutex
	off  time.Duration
}

func newVolAPI(t *testing.T, a *Agent) *volAPI {
	v := &volAPI{t: t, a: a}
	a.clock = func() time.Time {
		v.mu.Lock()
		defer v.mu.Unlock()
		return time.Now().Add(v.off)
	}
	v.ck, v.auth = agentSession(t, a, a.handlerFor("gui-alias"), "ops@acme.example")
	return v
}

func (v *volAPI) skip(d time.Duration) {
	v.mu.Lock()
	v.off += d
	v.mu.Unlock()
}

func (v *volAPI) do(method, path string, body any, out any) (int, string) {
	v.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = "10.0.0.9:1234"
	r.Header.Set("Cookie", "opm_session_"+v.a.site+"="+v.ck)
	r.Header.Set("Authorization", v.auth)
	rec := httptest.NewRecorder()
	v.a.handlerFor("gui-alias").ServeHTTP(rec, r)
	if out != nil {
		_ = json.Unmarshal(rec.Body.Bytes(), out)
	}
	return rec.Code, rec.Body.String()
}

func (v *volAPI) wait(op model.VolumeOp) model.VolumeOp {
	v.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var cur model.VolumeOp
		if c, b := v.do("GET", "/_opm/api/sites/"+v.a.site+"/volume-ops/"+op.OpID, nil, &cur); c != 200 {
			v.t.Fatalf("get op %d %s", c, b)
		}
		if cur.FinishedAt != nil {
			return cur
		}
		time.Sleep(50 * time.Millisecond)
	}
	v.t.Fatalf("op %s did not finish", op.OpID)
	return op
}

// intent → wait (fake clock) → typed slug
func (v *volAPI) intent(kind, slug string) string {
	v.t.Helper()
	var st model.ApproveStartResponse
	if c, b := v.do("POST", "/_opm/api/sites/"+v.a.site+"/volumes/"+slug+"/"+kind+"/start", nil, &st); c != 201 {
		v.t.Fatalf("%s/start %d %s", kind, c, b)
	}
	return st.Intent.IntentID
}

func listDir(t *testing.T, p string) []string {
	es, err := os.ReadDir(p)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestVolumeManagerUnit(t *testing.T) {
	a := newTestAgent(t)
	volHead(t, a, nil)
	v := newVolAPI(t, a)
	data := filepath.Join(a.dataDir, "volumes", "data")
	other := filepath.Join(a.dataDir, "volumes", "other")
	cfgFile := filepath.Join(a.dataDir, "volumes", "cfg")
	_ = os.MkdirAll(filepath.Join(data, "sub"), 0o750)
	_ = os.WriteFile(filepath.Join(data, "a.txt"), []byte("hello"), 0o640)
	_ = os.WriteFile(filepath.Join(data, "sub", "b.bin"), bytes.Repeat([]byte{1}, 4096), 0o600)
	_ = os.Symlink("a.txt", filepath.Join(data, "link"))
	_ = os.MkdirAll(other, 0o755)
	_ = os.WriteFile(cfgFile, []byte("key=1\n"), 0o644)
	base := "/_opm/api/sites/app"

	// ── copy to the default path
	var op model.VolumeOp
	if c, b := v.do("POST", base+"/volumes/data/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "path"}, StopContainers: true, Preserve: true}, &op); c != 202 {
		t.Fatalf("copy %d %s", c, b)
	}
	op = v.wait(op)
	if op.Ok == nil || !*op.Ok || op.Target == nil || !strings.HasPrefix(op.Target.Path, filepath.Join(a.dataDir, "volume-copies", "data-")) || *op.ResultBytes != 4101 || op.Progress.Files != 2 {
		t.Fatalf("copy op %+v err %v", op, op.Error)
	}
	cp := op.Target.Path
	if st, err := os.Stat(filepath.Join(cp, "a.txt")); err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("copied a.txt %v %v", st, err)
	}
	if st, _ := os.Stat(filepath.Join(cp, "sub")); st == nil || st.Mode().Perm() != 0o750 {
		t.Fatal("folder mode not kept")
	}
	if l, err := os.Readlink(filepath.Join(cp, "link")); err != nil || l != "a.txt" {
		t.Fatalf("symlink %q %v", l, err)
	}
	var copies model.ListResponse[model.VolumeCopy]
	v.do("GET", base+"/volume-copies", nil, &copies)
	if len(copies.Items) != 1 || !copies.Items[0].Exists || *copies.Items[0].SizeBytes != 4101 || !copies.Items[0].InCopiesDir || copies.Items[0].VolumeSlug != "data" {
		t.Fatalf("copies %+v", copies)
	}
	copyID := copies.Items[0].CopyID

	// ── path safety
	evil := filepath.Join(t.TempDir(), "evil")
	_ = os.Symlink("/etc", evil)
	nonEmpty := t.TempDir()
	_ = os.WriteFile(filepath.Join(nonEmpty, "x"), nil, 0o644)
	for _, p := range []string{"/", "/etc/opm-copy", "/usr/local/x", "relative/x", a.dataDir, filepath.Join(a.dataDir, "logs", "x"), "./volumes/x", filepath.Join(data, "inside"), filepath.Join(a.dataDir, "volume-copies"), filepath.Join(evil, "x"), nonEmpty} {
		p := p
		if c, b := v.do("POST", base+"/volumes/data/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "path", Path: &p}}, nil); c != 422 {
			t.Fatalf("copy to %q → %d %s", p, c, b)
		}
	}
	custom := filepath.Join(t.TempDir(), "pg-aside")
	a.volRate = map[string][]time.Time{}
	if c, b := v.do("POST", base+"/volumes/data/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "path", Path: &custom}, Preserve: false}, &op); c != 202 {
		t.Fatalf("copy to a custom path %d %s", c, b)
	}
	if op = v.wait(op); !*op.Ok || listDir(t, custom)[0] != "a.txt" {
		t.Fatalf("%+v", op)
	}

	// ── copy into another volume: not empty → conflict unless its name is typed
	_ = os.WriteFile(filepath.Join(other, "old"), []byte("old"), 0o644)
	if c, _ := v.do("POST", base+"/volumes/data/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "volume", VolumeSlug: "other"}}, nil); c != 409 {
		t.Fatalf("copy into a non-empty volume → %d", c)
	}
	if c, _ := v.do("POST", base+"/volumes/data/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "volume", VolumeSlug: "cfg"}}, nil); c != 422 {
		t.Fatalf("copy a folder into a file volume → %d", c)
	}
	ow := "other"
	if c, b := v.do("POST", base+"/volumes/data/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "volume", VolumeSlug: "other", Overwrite: &ow}, Preserve: true}, &op); c != 202 {
		t.Fatalf("copy into other %d %s", c, b)
	}
	if op = v.wait(op); !*op.Ok || strings.Join(listDir(t, other), ",") != "a.txt,link,sub" {
		t.Fatalf("other after copy %v %+v", listDir(t, other), op)
	}

	// ── empty: wait + typed slug, snapshot first
	a.volRate = map[string][]time.Time{}
	id := v.intent("empty", "data")
	if c, b := v.do("POST", base+"/volumes/data/empty", model.VolumeEmptyRequest{IntentID: id, Typed: "data", Snapshot: true}, nil); c != 422 || !strings.Contains(b, "wait") {
		t.Fatalf("empty before the wait %d %s", c, b)
	}
	v.skip(61 * time.Second)
	if c, _ := v.do("POST", base+"/volumes/data/empty", model.VolumeEmptyRequest{IntentID: id, Typed: "ACCEPT", Snapshot: true}, nil); c != 422 {
		t.Fatalf("empty with ACCEPT instead of the slug → %d", c)
	}
	if c, b := v.do("POST", base+"/volumes/data/empty", model.VolumeEmptyRequest{IntentID: id, Typed: "data", Snapshot: true}, &op); c != 202 {
		t.Fatalf("empty %d %s", c, b)
	}
	op = v.wait(op)
	if !*op.Ok || op.SnapshotID == nil || len(listDir(t, data)) != 0 || *op.ResultBytes != 4101 {
		t.Fatalf("empty %+v %v", op, listDir(t, data))
	}
	if st, _ := os.Stat(data); st == nil || !st.IsDir() {
		t.Fatal("the volume folder itself must stay")
	}
	if rows, _ := a.activitiesPage(0, 50, "volume.emptied", ""); len(rows) != 1 || rows[0].Data["snapshotId"] == nil {
		t.Fatalf("volume.emptied %+v", rows)
	}

	// ── restore from the copy
	id = v.intent("restore", "data")
	v.skip(61 * time.Second)
	if c, b := v.do("POST", base+"/volumes/data/restore", model.VolumeRestoreRequest{IntentID: id, Typed: "data", Source: model.VolumeRestoreSourceInput{Kind: "copy", CopyID: copyID}}, &op); c != 202 {
		t.Fatalf("restore %d %s", c, b)
	}
	if op = v.wait(op); !*op.Ok || op.SnapshotID == nil {
		t.Fatalf("restore %+v", op)
	}
	if b, _ := os.ReadFile(filepath.Join(data, "a.txt")); string(b) != "hello" {
		t.Fatalf("restored a.txt %q", b)
	}

	// ── restore from an uploaded archive: unsafe archives refused
	a.volRate = map[string][]time.Time{}
	if c, _ := v.do("POST", base+"/volumes/data/restore/upload", mkTarGz(t, map[string]string{"../evil": "x"}), nil); c != 422 {
		t.Fatalf("archive with .. → %d", c)
	}
	if c, _ := v.do("POST", base+"/volumes/data/restore/upload", []byte("not gzip"), nil); c != 422 {
		t.Fatalf("not an archive → %d", c)
	}
	var up model.VolumeRestoreUpload
	if c, b := v.do("POST", base+"/volumes/data/restore/upload", mkTarGz(t, map[string]string{"x/y.txt": "from archive"}), &up); c != 201 || up.Entries != 1 {
		t.Fatalf("upload %d %s", c, b)
	}
	id = v.intent("restore", "data")
	v.skip(61 * time.Second)
	if c, b := v.do("POST", base+"/volumes/data/restore", model.VolumeRestoreRequest{IntentID: id, Typed: "data", Source: model.VolumeRestoreSourceInput{Kind: "upload", UploadID: up.UploadID}}, &op); c != 202 {
		t.Fatalf("restore upload %d %s", c, b)
	}
	if op = v.wait(op); !*op.Ok {
		t.Fatalf("%+v", op)
	}
	if b, _ := os.ReadFile(filepath.Join(data, "x", "y.txt")); string(b) != "from archive" || len(listDir(t, data)) != 1 {
		t.Fatalf("after archive restore %v", listDir(t, data))
	}

	// ── file-kind volume: empty = truncate to 0 bytes
	a.volRate = map[string][]time.Time{}
	id = v.intent("empty", "cfg")
	v.skip(61 * time.Second)
	if c, b := v.do("POST", base+"/volumes/cfg/empty", model.VolumeEmptyRequest{IntentID: id, Typed: "cfg"}, &op); c != 202 {
		t.Fatalf("empty file %d %s", c, b)
	}
	op = v.wait(op)
	if st, err := os.Stat(cfgFile); err != nil || st.Size() != 0 || !*op.Ok {
		t.Fatalf("file volume after empty %v %v", st, err)
	}
	// file volume copy = a file copy
	if c, _ := v.do("POST", base+"/volumes/cfg/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "path"}}, &op); c != 202 {
		t.Fatal("copy file volume")
	}
	if op = v.wait(op); !*op.Ok {
		t.Fatalf("%+v", op)
	}
	if st, err := os.Stat(op.Target.Path); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("file copy %v %v", st, err)
	}

	// ── snapshot of one volume
	a.volRate = map[string][]time.Time{}
	var sr model.VolumeSnapshotResponse
	if c, b := v.do("POST", base+"/volumes/data/snapshot", model.VolumeSnapshotRequest{Note: "before PG17"}, &sr); c != 201 || sr.SnapshotID == "" {
		t.Fatalf("snapshot %d %s", c, b)
	}

	// ── locks: a busy volume, an update running, an update while an action runs
	a.volMu.Lock()
	a.volOps["X"] = &model.VolumeOp{OpID: "X", Kind: "copy", VolumeSlug: "data"}
	a.volBusy["data"] = "X"
	a.volMu.Unlock()
	if c, b := v.do("POST", base+"/volumes/data/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "path"}}, nil); c != 409 {
		t.Fatalf("busy volume → %d %s", c, b)
	}
	if a.volumeBusy("data") == "" || a.anyVolumeOp() == "" {
		t.Fatal("busy")
	}
	if _, he := a.startUpdate("update", a.headRevision(), nil, nil, &reqCtx{actor: "x"}); he == nil || he.code != model.ErrConflict {
		t.Fatalf("an update while a volume action runs: %+v", he)
	}
	a.volMu.Lock()
	delete(a.volBusy, "data")
	a.volMu.Unlock()
	_ = a.db.SetKV("lock", model.UpdateLock{UpdateID: "U1", Coordinator: "app", StartedAt: nowTs()})
	// §21 #110 a running update never blocks a volume action
	if c, b := v.do("POST", base+"/volumes/data/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "path"}}, nil); c != 202 {
		t.Fatalf("update running → %d %s (want 202)", c, b)
	}
	for end := time.Now().Add(20 * time.Second); a.volumeBusy("data") != "" && time.Now().Before(end); {
		time.Sleep(50 * time.Millisecond)
	}
	_ = a.db.DelKV("lock")

	// ── rate limit
	a.volRate = map[string][]time.Time{}
	for i := 0; i < model.VolumeOpRatePerMinute; i++ {
		if c, b := v.do("POST", base+"/volumes/other/snapshot", model.VolumeSnapshotRequest{}, nil); c != 201 {
			t.Fatalf("snapshot %d → %d %s", i, c, b)
		}
	}
	if c, _ := v.do("POST", base+"/volumes/other/snapshot", model.VolumeSnapshotRequest{}, nil); c != 429 {
		t.Fatalf("rate limit → %d", c)
	}

	// ── delete a copy (typed id)
	if c, _ := v.do("POST", base+"/volume-copies/"+copyID+"/delete", model.DeleteVolumeCopyRequest{Confirm: "x"}, nil); c != 422 {
		t.Fatal("delete without the typed id")
	}
	if c, b := v.do("POST", base+"/volume-copies/"+copyID+"/delete", model.DeleteVolumeCopyRequest{Confirm: copyID}, nil); c != 200 {
		t.Fatalf("delete copy %d %s", c, b)
	}
	if _, err := os.Stat(cp); !os.IsNotExist(err) {
		t.Fatal("the copy data must be gone")
	}
	for _, typ := range []string{"volume.copied", "volume.restored", "volume.copy-deleted", "snapshot.created"} {
		if rows, _ := a.activitiesPage(0, 50, typ, ""); len(rows) == 0 {
			t.Fatalf("no %s activity", typ)
		}
	}
	// an op interrupted by an agent stop is failed on load
	a.volMu.Lock()
	a.volOps["Y"] = &model.VolumeOp{OpID: "Y", Kind: "empty", VolumeSlug: "data", StartedAt: nowTs(), Steps: []model.VolumeOpStep{}}
	a.volMu.Unlock()
	a.saveVolumeOps()
	a.volOps = map[string]*model.VolumeOp{}
	a.loadVolumeOps()
	if y := a.volOps["Y"]; y == nil || y.Phase != "failed" || y.FinishedAt == nil {
		t.Fatalf("interrupted op %+v", y)
	}
}

func mkTarGz(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for n, c := range files {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(c)), Typeflag: tar.TypeReg, ModTime: time.Now()})
		_, _ = tw.Write([]byte(c))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// Real Docker: the containers that mount a volume stop in REVERSE start order (web, then db — web depends on db) and
// start again in start order; the data is emptied in between; both run afterwards.
func TestDockerVolumeEmptyOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	needDocker(t)
	if out, err := exec.Command("docker", "image", "inspect", "busybox:1.36").CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("docker", "pull", "busybox:1.36").CombinedOutput(); err2 != nil {
			t.Skipf("no busybox: %s %s", out, out2)
		}
	}
	home, _ := os.UserHomeDir() // Colima bind mounts need $HOME
	dir, err := os.MkdirTemp(home, ".opm-voltest-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dep := fmt.Sprintf("DepVol%x", time.Now().UnixNano()%0xffffff)
	a, err := New(Options{Config: model.AgentConfigFile{APIBase: "http://127.0.0.1:1", DeploymentID: dep, SiteSlug: "app", DataDir: dir, AdvertiseHost: "127.0.0.1",
		AgentPort: freePort(t), GuiAliasPort: model.Some(freePort(t)), EdgeHTTPPort: model.Null[int]()}, Timings: fastTimings()})
	if err != nil {
		t.Fatal(err)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	defer func() { a.cancel(); a.db.Close() }()
	vol := "data"
	mount := func() []model.ContainerMount {
		return []model.ContainerMount{{MountPath: ptr("/data"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: vol}}}
	}
	volHead(t, a, []model.Container{
		{ContainerSlug: "db", ServiceSlug: "bb", Version: "1.36", Enabled: true, Volumes: mount(), Ports: []model.ContainerPort{}},
		{ContainerSlug: "web", ServiceSlug: "bb", Version: "1.36", Enabled: true, Volumes: mount(), Ports: []model.ContainerPort{}, Depends: []model.ContainerRef{{ContainerSlug: "db"}}},
	})
	data := filepath.Join(dir, "volumes", "data")
	_ = os.MkdirAll(data, 0o755)
	_ = os.WriteFile(filepath.Join(data, "PG_VERSION"), []byte("16\n"), 0o600)
	names := map[string]string{}
	for _, c := range []string{"db", "web"} {
		names[c] = strings.ToLower(dep) + "-" + c
		out, err := exec.Command("docker", "run", "-d", "--name", names[c], "--label", "opm.deployment="+dep, "--label", "opm.site=app", "--label", "opm.container="+c,
			"-v", data+":/data", "busybox:1.36", "sleep", "3600").CombinedOutput()
		if err != nil {
			t.Fatalf("docker run %s: %v %s", c, err, out)
		}
		defer exec.Command("docker", "rm", "-f", names[c]).Run()
	}
	v := newVolAPI(t, a)
	id := v.intent("empty", "data")
	v.skip(61 * time.Second)
	var op model.VolumeOp
	if c, b := v.do("POST", "/_opm/api/sites/app/volumes/data/empty", model.VolumeEmptyRequest{IntentID: id, Typed: "data", Snapshot: true}, &op); c != 202 {
		t.Fatalf("empty %d %s", c, b)
	}
	op = v.wait(op)
	if !*op.Ok || strings.Join(op.Stopped, ",") != "db,web" {
		t.Fatalf("op %+v %v", op, op.Error)
	}
	if len(listDir(t, data)) != 0 {
		t.Fatal("not emptied")
	}
	dc := docker.New(docker.DefaultHost(""))
	state := func(n string) (bool, time.Time, time.Time) {
		var st struct {
			State struct {
				Running    bool
				StartedAt  time.Time
				FinishedAt time.Time
			}
		}
		out, err := exec.Command("docker", "inspect", "--format", "{{json .}}", n).Output()
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(out, &st)
		return st.State.Running, st.State.StartedAt, st.State.FinishedAt
	}
	_ = dc
	dbRun, dbStart, dbStop := state(names["db"])
	webRun, webStart, webStop := state(names["web"])
	if !dbRun || !webRun {
		t.Fatal("both containers must run again")
	}
	if !webStop.Before(dbStop) {
		t.Fatalf("stop order: web %v must stop before db %v", webStop, dbStop)
	}
	if !dbStart.Before(webStart) {
		t.Fatalf("start order: db %v must start before web %v", dbStart, webStart)
	}
	// the user's case: copy aside with the containers stopped, then the copy holds PG_VERSION
	_ = os.WriteFile(filepath.Join(data, "PG_VERSION"), []byte("16\n"), 0o600)
	if c, b := v.do("POST", "/_opm/api/sites/app/volumes/data/copy", model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "path"}, StopContainers: true, Preserve: true}, &op); c != 202 {
		t.Fatalf("copy %d %s", c, b)
	}
	op = v.wait(op)
	if !*op.Ok || len(op.Stopped) != 2 {
		t.Fatalf("copy %+v", op)
	}
	if b, _ := os.ReadFile(filepath.Join(op.Target.Path, "PG_VERSION")); string(b) != "16\n" {
		t.Fatal("copy content")
	}
	if r1, _, _ := state(names["db"]); !r1 {
		t.Fatal("db must run after the copy")
	}
}

// Over the mesh: app's GUI drives db's volume manager (copy, op polling, file upload + mkdir through tools); db logs the
// action, app logs volume.action-forwarded.
func TestVolumeManagerMeshForward(t *testing.T) {
	app := newTestAgent(t)
	db := newTestAgentSite(t, "db")
	volHead(t, db, nil)
	appSrv := httptest.NewServer(app.handlerFor("agent"))
	dbSrv := httptest.NewServer(db.handlerFor("agent"))
	defer appSrv.Close()
	defer dbSrv.Close()
	port := func(s *httptest.Server) int {
		var p int
		_, ps, _ := strings.Cut(strings.TrimPrefix(s.URL, "http://"), ":")
		for _, c := range ps {
			p = p*10 + int(c-'0')
		}
		return p
	}
	now := nowTs()
	peers := []model.PeerInfo{{SiteSlug: "db", PublicKey: sign.PublicKeyB64(db.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(dbSrv), EnrolledAt: now},
		{SiteSlug: "app", PublicKey: sign.PublicKeyB64(app.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(appSrv), EnrolledAt: now}}
	app.storePeers(peers)
	db.storePeers(peers)
	app.mu.Lock()
	app.peerState["db"] = &peerRuntime{lastAnswer: time.Now().Add(time.Hour)}
	app.mu.Unlock()
	data := filepath.Join(db.dataDir, "volumes", "data")
	_ = os.MkdirAll(data, 0o755)
	_ = os.WriteFile(filepath.Join(data, "f.txt"), []byte("db data"), 0o644)

	ck, auth := agentSession(t, app, app.handlerFor("agent"), "ops@acme.example")
	call := func(method, path string, body []byte, out any) (int, string) {
		t.Helper()
		r, _ := http.NewRequest(method, appSrv.URL+path, bytes.NewReader(body))
		r.Header.Set("Cookie", "opm_session_app="+ck)
		r.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(r)
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
	j := func(v any) []byte { b, _ := json.Marshal(v); return b }
	var op model.VolumeOp
	if c, b := call("POST", "/_opm/api/sites/db/volumes/data/copy", j(model.VolumeCopyRequest{Target: model.VolumeCopyTargetInput{Kind: "path"}, Preserve: true}), &op); c != 202 || op.SiteSlug != "db" {
		t.Fatalf("forwarded copy %d %s", c, b)
	}
	deadline := time.Now().Add(20 * time.Second)
	for op.FinishedAt == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		call("GET", "/_opm/api/sites/db/volume-ops/"+op.OpID, nil, &op)
	}
	if op.Ok == nil || !*op.Ok || !strings.HasPrefix(op.Target.Path, filepath.Join(db.dataDir, "volume-copies")) {
		t.Fatalf("op %+v", op)
	}
	var copies model.ListResponse[model.VolumeCopy]
	if c, _ := call("GET", "/_opm/api/sites/db/volume-copies", nil, &copies); c != 200 || len(copies.Items) != 1 {
		t.Fatalf("copies %d %+v", c, copies)
	}
	// tools file ops forwarded (raw upload body + JSON)
	if c, b := call("POST", "/_opm/api/sites/db/tools/volumes/data/upload?path=/up/x.bin", []byte("UPLOADED-OVER-MESH"), nil); c != 200 {
		t.Fatalf("forwarded upload %d %s", c, b)
	}
	if b, _ := os.ReadFile(filepath.Join(data, "up", "x.bin")); string(b) != "UPLOADED-OVER-MESH" {
		t.Fatalf("uploaded %q", b)
	}
	if c, b := call("POST", "/_opm/api/sites/db/tools/volumes/data/mkdir", j(map[string]string{"path": "/made"}), nil); c != 200 {
		t.Fatalf("forwarded mkdir %d %s", c, b)
	}
	// both sites logged it
	if rows, _ := db.activitiesPage(0, 50, "volume.copied", ""); len(rows) != 1 {
		t.Fatal("db: volume.copied")
	}
	if rows, _ := db.activitiesPage(0, 50, "volume.file-uploaded", ""); len(rows) != 1 || rows[0].Data["via"] != "app" {
		t.Fatalf("db: volume.file-uploaded %+v", rows)
	}
	if rows, _ := app.activitiesPage(0, 50, "volume.action-forwarded", ""); len(rows) != 3 {
		t.Fatalf("app: volume.action-forwarded %d", len(rows))
	}
}
