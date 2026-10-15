package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/volcontent"
)

// persistRev: site <site> declares the folder volume "flows" persisted to the cloud (auto as given).
func persistRev(t *testing.T, site string, auto bool, maxBytes int64) *model.Revision {
	t.Helper()
	rev := mkRevision(t, 1, nil, site)
	dir := "dir"
	rev.Config.Sites[0].Config.Volumes = &[]model.SiteVolume{{VolumeSlug: "flows", Label: "Flows", Backup: true, Kind: &dir, Seed: model.Null[model.VolumeSeed](),
		Persist: &model.VolumePersist{Exclude: []string{"node_modules/", "*.log"}, MaxBytes: maxBytes, Auto: auto}}}
	rev.Hash, _ = canonical.HashCanonical(rev.Config)
	return rev
}

func newVSAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Options{Config: model.AgentConfigFile{APIBase: "http://127.0.0.1:1", DeploymentID: "DepVS", SiteSlug: "app", DataDir: dir, AdvertiseHost: "127.0.0.1",
		AgentPort: freePort(t), GuiAliasPort: model.Null[int](), EdgeHTTPPort: model.Null[int]()}, Timings: fastTimings()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

// The scanner (injected clock): auto snapshot only after VOLUME_SYNC_QUIET_SEC without changes, at most one per
// VOLUME_SYNC_MIN_INTERVAL_SEC, never with auto off; unchanged trees store nothing; too-large is refused.
func TestVolumeSyncScanner(t *testing.T) {
	a := newVSAgent(t)
	rev := persistRev(t, "app", true, 1<<20)
	setHead(t, a, rev)
	root := filepath.Join(a.dataDir, "volumes", "flows")
	_ = os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "flows.json"), []byte("[]"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "node_modules", "x", "i.js"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "a.log"), []byte("x"), 0o644)
	_ = os.Symlink("/etc/passwd", filepath.Join(root, "link"))
	t0 := time.Now()
	ctx := context.Background()
	count := func() int { return len(a.records(`WHERE volumeSlug = 'flows'`)) }
	a.volSyncScanOnce(ctx, t0)
	if count() != 0 {
		t.Fatal("no snapshot before the quiet time")
	}
	a.volSyncScanOnce(ctx, t0.Add(60*time.Second))
	if count() != 0 {
		t.Fatal("no snapshot after 60 s")
	}
	a.volSyncScanOnce(ctx, t0.Add(121*time.Second))
	if count() != 1 {
		t.Fatalf("auto snapshot after the quiet time: %d", count())
	}
	r := a.lastRecord("flows")
	if r.Local.Trigger != "auto" || len(r.Req.Tree.Entries) != 1 || r.Req.ExcludedCount != 2 || r.Req.SkippedCount != 1 || r.Req.Skipped[0].Reason != "symlink" {
		t.Fatalf("record %+v", r.Req)
	}
	if !a.haveBlob(hexOf(r.Req.Tree.Entries[0].Sha256)) {
		t.Fatal("the file must be in the blob store")
	}
	// unchanged → nothing
	a.volSyncScanOnce(ctx, t0.Add(400*time.Second))
	if count() != 1 {
		t.Fatal("unchanged: no new snapshot")
	}
	// changed at 400 s, quiet by 530 s, but the last auto snapshot was at 121 s (< 900 s)
	_ = os.WriteFile(filepath.Join(root, "flows.json"), []byte(`[{"id":1}]`), 0o644)
	a.volSyncScanOnce(ctx, t0.Add(401*time.Second))
	a.volSyncScanOnce(ctx, t0.Add(530*time.Second))
	if count() != 1 {
		t.Fatal("min interval")
	}
	st := a.syncStatus(&a.persistVolumes()[0], time.Hour)
	if st.State != "pending-upload" && st.State != "live-changed" {
		t.Fatalf("state %s", st.State)
	}
	a.volSyncScanOnce(ctx, t0.Add(1030*time.Second))
	if count() != 2 {
		t.Fatalf("second auto snapshot: %d", count())
	}
	// manual: unchanged → created false
	p := a.persistVolumes()[0]
	if _, created, err := a.snapshotVolume(&p, snapOpts{trigger: "manual-agent"}); err != nil || created {
		t.Fatalf("unchanged manual snapshot: %v %v", created, err)
	}
	// too large (cap 1 MiB)
	_ = os.WriteFile(filepath.Join(root, "big.bin"), make([]byte, 2<<20), 0o644)
	if _, _, err := a.snapshotVolume(&p, snapOpts{trigger: "manual-agent"}); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("too large: %v", err)
	}
	if len(activityData(t, a, "volume-content.snapshot-failed")) != 1 {
		t.Fatal("snapshot-failed activity")
	}
	_ = os.Remove(filepath.Join(root, "big.bin"))
	// auto off
	setHead(t, a, persistRev(t, "app", false, 1<<20))
	_ = os.WriteFile(filepath.Join(root, "flows.json"), []byte(`[{"id":2}]`), 0o644)
	a.volSyncScanOnce(ctx, t0.Add(3000*time.Second))
	a.volSyncScanOnce(ctx, t0.Add(5000*time.Second))
	if count() != 2 {
		t.Fatal("auto off: no snapshot")
	}
}

// converge: deletes / writes / modes / folders; excludes, .opm-seeded and symlinked folders are never touched.
func TestVolumeConverge(t *testing.T) {
	a := newVSAgent(t)
	root := filepath.Join(t.TempDir(), "v")
	w := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	w("keep.txt", "keep")
	w("gone.txt", "gone")
	w("old/x.txt", "x")
	w("logs/a.log", "log")
	w(".opm-seeded", "{}")
	w("node_modules/m.js", "m")
	outside := t.TempDir()
	_ = os.Symlink(outside, filepath.Join(root, "sym"))
	ex := volcontent.Compile([]string{"node_modules/", "*.log"})
	walk, err := walkVolume(root, ex)
	if err != nil {
		t.Fatal(err)
	}
	vol := &vsVol{cache: map[string]hcEnt{}}
	live, _ := a.treeOf("v", vol, walk, false)
	newHex, _ := a.storeBlobBytes([]byte("new"), "file", "test")
	var keep model.VolumeTreeEntry
	for _, e := range live.Entries {
		if e.Path == "keep.txt" {
			keep = e
		}
	}
	target := volcontent.Normalize(model.VolumeTree{Entries: []model.VolumeTreeEntry{
		{Path: "keep.txt", Sha256: keep.Sha256, Size: keep.Size, Mode: "0600", Render: "none"},
		{Path: "a/b/new.txt", Sha256: "sha256:" + newHex, Size: 3, Mode: "0755", Render: "none"},
		{Path: "a.log", Sha256: "sha256:" + newHex, Size: 3, Mode: "0644", Render: "none"}, // excluded → never written
	}, Dirs: []string{"empty"}})
	written, deleted, err := a.converge(root, ex, walk, live, target)
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 || deleted != 2 {
		t.Fatalf("written %d deleted %d", written, deleted)
	}
	if st, _ := os.Stat(filepath.Join(root, "keep.txt")); st.Mode().Perm() != 0o600 {
		t.Fatal("mode change")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a/b/new.txt")); string(b) != "new" {
		t.Fatal("new file")
	}
	for _, p := range []string{"gone.txt", "old", "a.log"} {
		if _, err := os.Lstat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Fatalf("%s must not exist", p)
		}
	}
	for _, p := range []string{"logs/a.log", ".opm-seeded", "node_modules/m.js", "sym", "empty"} {
		if _, err := os.Lstat(filepath.Join(root, p)); err != nil {
			t.Fatalf("%s must stay: %v", p, err)
		}
	}
	// never through a symlinked folder
	bad := model.VolumeTree{Entries: []model.VolumeTreeEntry{{Path: "sym/evil.txt", Sha256: "sha256:" + newHex, Size: 3, Mode: "0644", Render: "none"}}, Dirs: []string{}}
	walk, _ = walkVolume(root, ex)
	live, _ = a.treeOf("v", vol, walk, false)
	if _, _, err := a.converge(root, ex, walk, live, volcontent.Normalize(bad)); err == nil {
		t.Fatal("writing through a symlink must fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); err == nil {
		t.Fatal("wrote through the symlink")
	}
}

// Store-and-forward: a snapshot taken while the cloud is unreachable waits in the queue (attempts + error) and is
// uploaded once the cloud is back; a cloud snapshot request is answered once.
func TestVolumeSyncStoreAndForward(t *testing.T) {
	fc := fakecloud.Start("DepVSAF")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	r := e.startSAF()
	a := r.a
	a.vsync().backoff, a.vsync().backoffMx = 200*time.Millisecond, time.Second
	waitUntil(t, 20*time.Second, "enrolled", func() bool { _, ok := fc.Agent("app"); return ok })
	rev := persistRev(t, "app", false, 50<<20)
	cr, _ := fc.Publish(rev.Config, rev.Catalog, "persist")
	setHead(t, a, &cr)
	waitUntil(t, 20*time.Second, "head reported", func() bool { ad, _ := fc.Agent("app"); return ad.HeadRevID != nil && *ad.HeadRevID == cr.RevID })
	root := filepath.Join(a.dataDir, "volumes", "flows")
	_ = os.MkdirAll(root, 0o755)
	_ = os.WriteFile(filepath.Join(root, "flows.json"), []byte(`[1]`), 0o644)
	fc.BlockSite("app", true)
	p := a.persistVolumes()[0]
	rec, created, err := a.snapshotVolume(&p, snapOpts{trigger: "manual-agent"})
	if err != nil || !created {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "failed attempts", func() bool {
		l := a.records(`WHERE snapshotId = ?`, rec.Local.SnapshotID)
		return len(l) == 1 && l[0].Attempts >= 2 && l[0].Local.Error != nil
	})
	if len(fc.VolumeVersions("app", "flows")) != 0 {
		t.Fatal("nothing reached the cloud")
	}
	fc.BlockSite("app", false)
	waitUntil(t, 30*time.Second, "uploaded", func() bool { return len(fc.VolumeVersions("app", "flows")) == 1 })
	waitUntil(t, 10*time.Second, "record uploaded", func() bool {
		l := a.records(`WHERE snapshotId = ?`, rec.Local.SnapshotID)
		return len(l) == 1 && l[0].Status == "uploaded" && l[0].Local.Seq != nil && *l[0].Local.Seq == 1
	})
	if b, ok := fc.Blob(fc.VolumeVersions("app", "flows")[0].TreeHash); !ok || !strings.Contains(string(b), "flows.json") {
		t.Fatal("tree blob")
	}
	rid := fc.RequestVolumeSnapshot("app", "flows")
	waitUntil(t, 30*time.Second, "request answered", func() bool { return fc.VolumeSnapshotRequest("app", "flows") == nil })
	vv := fc.VolumeVersions("app", "flows")
	if len(vv) != 2 || *vv[1].RequestID != rid {
		t.Fatalf("versions %+v", vv)
	}
	time.Sleep(2 * time.Second)
	if len(fc.VolumeVersions("app", "flows")) != 2 {
		t.Fatal("a request is answered once")
	}
}

// Over the mesh: app's GUI asks db for its volume sync state and takes a snapshot there; db logs the snapshot, app logs
// volume.action-forwarded.
func TestVolumeSyncMeshForward(t *testing.T) {
	app := newTestAgent(t)
	db := newTestAgentSite(t, "db")
	setHead(t, db, persistRev(t, "db", false, 50<<20))
	appSrv := httptest.NewServer(app.handlerFor("agent"))
	dbSrv := httptest.NewServer(db.handlerFor("agent"))
	defer appSrv.Close()
	defer dbSrv.Close()
	port := func(s *httptest.Server) int {
		_, ps, _ := strings.Cut(strings.TrimPrefix(s.URL, "http://"), ":")
		p, _ := strconv.Atoi(ps)
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
	root := filepath.Join(db.dataDir, "volumes", "flows")
	_ = os.MkdirAll(root, 0o755)
	_ = os.WriteFile(filepath.Join(root, "flows.json"), []byte(`[]`), 0o644)
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
	var list model.ListResponse[model.VolumeSyncStatus]
	if c, b := call("GET", "/_opm/api/sites/db/volume-sync", nil, &list); c != 200 || len(list.Items) != 1 || list.Items[0].State != "never-snapshotted" || list.Items[0].SiteSlug != "db" {
		t.Fatalf("forwarded list %d %s", c, b)
	}
	var snap model.VolumeSyncSnapshotResponse
	if c, b := call("POST", "/_opm/api/sites/db/volume-sync/flows/snapshot", []byte(`{}`), &snap); c != 201 || !snap.Created || *snap.Snapshot.Actor != "ops@acme.example" {
		t.Fatalf("forwarded snapshot %d %s", c, b)
	}
	if rows, _ := db.activitiesPage(0, 50, "volume-content.snapshot", ""); len(rows) != 1 {
		t.Fatal("db: volume-content.snapshot")
	}
	if rows, _ := app.activitiesPage(0, 50, "volume.action-forwarded", ""); len(rows) != 1 {
		t.Fatalf("app: volume.action-forwarded %d", len(rows))
	}
	if c, _ := call("GET", "/_opm/api/sites/db/volume-sync/flows/plan", nil, nil); c != 422 {
		t.Fatalf("plan without a push: %d", c)
	}
}

// §21 #101 tree / single starter content: written once into a new volume (rendered once per site, modes, empty
// folders, marker, kv, activity source tree|single), never into a volume with data; Reset re-applies it (writeSeed).
func TestContentSeedTreeSingle(t *testing.T) {
	a := newVSAgent(t)
	put := func(path, body, mode, render string) model.VolumeTreeEntry {
		h, _ := a.storeBlobBytes([]byte(body), "file", "test")
		return model.VolumeTreeEntry{Path: path, Sha256: "sha256:" + h, Size: int64(len(body)), Mode: mode, Render: render}
	}
	dir, file := "dir", "file"
	one := put("", "single {{ self.slug }}", "0600", "expressions")
	cfg := &model.Config{Variables: []model.Variable{}, Secrets: []model.SecretDef{}, Files: []model.FileDef{},
		Sites: []model.Site{{SiteSlug: a.site, Config: model.SiteConfig{Volumes: &[]model.SiteVolume{
			{VolumeSlug: "t", Backup: true, Kind: &dir, Seed: model.Some(model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{
				put("a/b/c.sh", "#!/bin/sh\n", "0755", "none"), put("s.js", "site={{ self.slug }}", "0644", "expressions")}, Dirs: []string{"e/f"}})},
			{VolumeSlug: "one", Backup: true, Kind: &file, Seed: model.Some(model.VolumeSeed{Kind: "single", Sha256: one.Sha256, Size: one.Size, Mode: "0600", Render: "expressions"})},
		}}}}}
	cat := emptyCatalog()
	site := &cfg.Sites[0]
	if err := a.seedVolumes(cfg, cat, site, nil); err != nil {
		t.Fatal(err)
	}
	tdir := filepath.Join(a.dataDir, "volumes", "t")
	if b, _ := os.ReadFile(filepath.Join(tdir, "s.js")); string(b) != "site="+a.site {
		t.Fatalf("rendered %q", b)
	}
	if st, _ := os.Stat(filepath.Join(tdir, "a/b/c.sh")); st == nil || st.Mode().Perm() != 0o755 {
		t.Fatal("mode 0755")
	}
	if st, _ := os.Stat(filepath.Join(tdir, "e/f")); st == nil || !st.IsDir() {
		t.Fatal("empty folder")
	}
	oneP := filepath.Join(a.dataDir, "volumes", "one")
	if b, _ := os.ReadFile(oneP); string(b) != "single "+a.site {
		t.Fatalf("single %q", b)
	}
	if st, _ := os.Stat(oneP); st.Mode().Perm() != 0o600 {
		t.Fatal("single mode")
	}
	if s := a.seededInfo("t"); s == nil || s.Source != "tree" || s.Entries != 2 {
		t.Fatalf("seeded %+v", s)
	}
	if s := a.seededInfo("one"); s == nil || s.Source != "single" {
		t.Fatalf("seeded %+v", s)
	}
	srcs := map[string]bool{}
	for _, d := range activityData(t, a, "volume.seeded") {
		srcs[d["source"].(string)] = true
	}
	if !srcs["tree"] || !srcs["single"] {
		t.Fatalf("activities %v", srcs)
	}
	// reset: wipe + forced seed
	_ = os.WriteFile(filepath.Join(tdir, "s.js"), []byte("edited"), 0o644)
	_ = os.RemoveAll(filepath.Join(tdir, "a"))
	_ = os.Remove(filepath.Join(tdir, SeedMarker))
	v := &(*site.Config.Volumes)[0]
	if _, _, err := a.seedVolume(cfg, cat, &site.Config, v, true, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(tdir, "s.js")); string(b) != "site="+a.site {
		t.Fatal("reset content")
	}
	if _, err := os.Stat(filepath.Join(tdir, "a/b/c.sh")); err != nil {
		t.Fatal("reset nested")
	}
}
