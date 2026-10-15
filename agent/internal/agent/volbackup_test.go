package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
	"opm/agent/internal/store"
	"opm/agent/internal/validate"
)

// backupHead: site app declares data (its own policy) and other (no policy = the daily site snapshot).
func backupHead(t *testing.T, a *Agent, p *model.VolumeBackupPolicy) {
	t.Helper()
	cfg := validate.EmptyConfig()
	sc := validate.DefaultSiteConfig(ptr("127.0.0.1"))
	sc.Volumes = &[]model.SiteVolume{{VolumeSlug: "data", Label: "Data", Backup: true, BackupPolicy: p}, {VolumeSlug: "other", Label: "Other", Backup: true}}
	cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: a.site, Label: a.site, Config: sc})
	cat := *emptyCatalog()
	h, _ := canonical.HashCanonical(cfg)
	ch, _ := canonical.HashCanonical(cat)
	setHead(t, a, &model.Revision{RevID: newID(), Seq: 1, Origin: "cloud", Actor: "t", CreatedAt: nowTs(), Config: cfg, Hash: h, Catalog: cat, CatalogHash: ch, Summary: "s", Kind: "change"})
}

func TestVolumeBackupPolicy(t *testing.T) {
	a := newTestAgent(t)
	backupHead(t, a, &model.VolumeBackupPolicy{Schedule: "hourly", Keep: 2})
	v := newVolAPI(t, a)
	data := filepath.Join(a.dataDir, "volumes", "data")
	other := filepath.Join(a.dataDir, "volumes", "other")
	_ = os.MkdirAll(data, 0o755)
	_ = os.MkdirAll(other, 0o755)
	_ = os.WriteFile(filepath.Join(data, "a.txt"), []byte("v1"), 0o644)
	_ = os.WriteFile(filepath.Join(other, "o.txt"), []byte("o"), 0o644)
	ctx := context.Background()
	base := "/_opm/api/sites/app"

	// the policy is in the revision hash only when set (absent keeps older hashes)
	if b, _ := canonical.Canonicalize(toAny(model.SiteVolume{VolumeSlug: "x", Backup: true})); strings.Contains(b, "backupPolicy") {
		t.Fatal("absent policy serialized")
	}

	// first round: due (never taken); second round right after: not due
	a.volumeBackupLoop(ctx)
	a.volumeBackupLoop(ctx)
	if n := countAuto(a, "data"); n != 1 {
		t.Fatalf("after two rounds: %d automatic backups", n)
	}
	if n := countAuto(a, "other"); n != 0 {
		t.Fatal("a volume without a policy got its own backup")
	}
	// one hour later: due again; three hours of backups, keep 2
	for i := 0; i < 3; i++ {
		v.skip(61 * time.Minute)
		_ = os.WriteFile(filepath.Join(data, "a.txt"), []byte("v"+string(rune('2'+i))), 0o644)
		a.volumeBackupLoop(ctx)
	}
	if n := countAuto(a, "data"); n != 2 {
		t.Fatalf("keep 2: %d automatic backups", n)
	}

	// the list per volume: newest first, automatic ones flagged, the policy and the next backup
	var lb model.VolumeBackupsResponse
	if c, b := v.do("GET", base+"/volumes/data/backups", nil, &lb); c != 200 {
		t.Fatalf("backups %d %s", c, b)
	}
	if !lb.Explicit || lb.Policy.Schedule != "hourly" || lb.NextAt == nil || len(lb.Items) != 2 || !lb.Items[0].Auto || lb.Items[0].Reason != volumeAutoReason {
		t.Fatalf("%+v", lb)
	}
	newest, oldest := lb.Items[0].SnapshotID, lb.Items[1].SnapshotID

	// the scheduled site snapshot leaves out the volume with its own policy
	sid := newID()
	a.takeSitePart(ctx, model.PeerSnapshotStepRequest{SnapshotID: sid, Reason: "scheduled", CreatedBy: "system"})
	pi, err := a.loadPart(sid)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range pi.site.Volumes {
		names = append(names, x.VolumeSlug)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "other" {
		t.Fatalf("scheduled snapshot volumes %v", names)
	}
	var lo model.VolumeBackupsResponse
	v.do("GET", base+"/volumes/other/backups", nil, &lo)
	if lo.Explicit || len(lo.Items) != 1 || lo.Items[0].Reason != "scheduled" || lo.Items[0].Auto {
		t.Fatalf("other %+v", lo)
	}

	// restore the volume from the older automatic backup: snapshot of the current data first, then the old content
	_ = os.WriteFile(filepath.Join(data, "a.txt"), []byte("broken"), 0o644)
	_ = os.WriteFile(filepath.Join(data, "junk"), []byte("x"), 0o644)
	id := v.intent("restore", "data")
	v.skip(61 * time.Second)
	var op model.VolumeOp
	if c, b := v.do("POST", base+"/volumes/data/restore", model.VolumeRestoreRequest{IntentID: id, Typed: "data", Source: model.VolumeRestoreSourceInput{Kind: "snapshot", SnapshotID: oldest}}, &op); c != 202 {
		t.Fatalf("restore %d %s", c, b)
	}
	if op = v.wait(op); op.Ok == nil || !*op.Ok || op.SnapshotID == nil {
		t.Fatalf("restore %+v err %v", op, op.Error)
	}
	if b, _ := os.ReadFile(filepath.Join(data, "a.txt")); string(b) != "v3" {
		t.Fatalf("restored a.txt = %q", b)
	}
	if _, err := os.Stat(filepath.Join(data, "junk")); err == nil {
		t.Fatal("restore kept a file the backup does not have")
	}
	// the pre-restore snapshot holds the replaced data (every change can be rolled back)
	pre, err := a.loadPart(*op.SnapshotID)
	if err != nil || len(pre.site.Volumes) != 1 || pre.site.Volumes[0].VolumeSlug != "data" {
		t.Fatalf("pre-restore snapshot %+v %v", pre, err)
	}
	// a snapshot without that volume, or an unknown snapshot: refused
	id = v.intent("restore", "data")
	v.skip(61 * time.Second)
	if c, _ := v.do("POST", base+"/volumes/data/restore", model.VolumeRestoreRequest{IntentID: id, Typed: "data", Source: model.VolumeRestoreSourceInput{Kind: "snapshot", SnapshotID: sid}}, nil); c != 404 {
		t.Fatalf("restore data from a snapshot without it → %d", c)
	}
	_ = newest

	// schedule off: no automatic backup
	backupHead(t, a, &model.VolumeBackupPolicy{Schedule: "off", Keep: 0})
	v.skip(10 * time.Hour)
	before := countAuto(a, "data")
	a.volumeBackupLoop(ctx)
	if countAuto(a, "data") != before {
		t.Fatal("schedule off took a backup")
	}
}

func countAuto(a *Agent, slug string) int {
	n := 0
	for _, b := range a.volumeBackups(slug) {
		if b.Auto {
			n++
		}
	}
	return n
}

// The size budget never removes the newest snapshot of a reason, a volume's kept automatic backups, manual or
// imported snapshots; when those alone exceed it, status warns.
func TestSnapshotSizeBudgetProtects(t *testing.T) {
	a := newTestAgent(t)
	backupHead(t, a, &model.VolumeBackupPolicy{Schedule: "hourly", Keep: 1})
	insert := func(reason, note string, size int64) string {
		id := newID()
		snap := model.Snapshot{SnapshotID: id, CreatedAt: nowTs(), Reason: reason, Sites: map[string]model.SnapshotSite{a.site: {Volumes: []model.SnapshotVolume{{VolumeSlug: "data", SizeBytes: size}}}}, Note: note}
		if _, err := a.db.Exec(`INSERT INTO snapshots(snapshotId, createdAt, reason, revId, seq, manifest, sizeBytes, createdBy, note, complete) VALUES(?,?,?,?,?,?,?,?,?,1)`,
			id, snap.CreatedAt, reason, nil, 0, store.JSON(snap), size, "t", note); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // ids are time ordered
		return id
	}
	pct := 1
	a.cfg.SnapshotBudgetPct = &pct
	budget := a.snapshotBudget()
	big := budget // each one alone fills the budget
	s1 := insert("scheduled", "daily", big)
	s2 := insert("scheduled", "daily", big)
	p1 := insert("pre-update", "", big)
	p2 := insert("pre-update", "", big)
	m1 := insert("manual", "before restore of data", big)
	v1 := insert(volumeAutoReason, volumeAutoNote("data"), big)
	a.retentionBySize("")
	have := map[string]bool{}
	for _, s := range a.snapRows() {
		have[s.SnapshotID] = true
	}
	if have[s1] || have[p1] {
		t.Fatalf("the older scheduled / pre-update snapshots should go first: %v", have)
	}
	for _, id := range []string{s2, p2, m1, v1} {
		if !have[id] {
			t.Fatalf("protected snapshot %s was deleted", id)
		}
	}
	if w := a.diskWarningTexts(); len(w) == 0 || !strings.Contains(w[0], "over the budget") {
		t.Fatalf("no budget warning: %v", w)
	}
}

// The free-space floor and the daily opm.db backup + recovery.
func TestDiskFloorAndDBRecovery(t *testing.T) {
	if f := DiskFloorFor(10 << 30); f != 512<<20 {
		t.Fatal(f)
	}
	if f := DiskFloorFor(1 << 40); f != 2<<30 {
		t.Fatal(f)
	}
	a := newTestAgent(t)
	_, free := a.diskTotals()
	if err := a.checkDiskFor(free, "a test"); err == nil || !strings.Contains(err.Error(), "always leaves") {
		t.Fatalf("filling the disk must be refused: %v", err)
	}
	if err := a.checkDiskFor(1, "a test"); err != nil {
		t.Fatal(err)
	}
	// daily backup (once a day, 3 kept)
	_ = a.db.SetKV("probe", "before-backup")
	day := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := a.db.WriteDailyBackup(day.AddDate(0, 0, i), dbBackupsKept); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := a.db.WriteDailyBackup(day.AddDate(0, 0, 4), dbBackupsKept); p != "" {
		t.Fatal("a second backup on the same day")
	}
	bs := store.DailyBackups(a.db.Path)
	if len(bs) != 3 || !strings.HasSuffix(bs[0], "opm-20261005.db") {
		t.Fatalf("backups %v", bs)
	}
	path := a.db.Path
	a.db.Close()
	// a corrupt file: replaced by the newest backup, the broken one kept aside
	if err := os.WriteFile(path, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	db, restored, err := store.OpenOrRecover(path)
	if err != nil || !strings.HasSuffix(restored, "opm-20261005.db") {
		t.Fatalf("recover: %v %q", err, restored)
	}
	var probe string
	if ok, _ := db.GetKV("probe", &probe); !ok || probe != "before-backup" {
		t.Fatalf("restored data %q", probe)
	}
	db.Close()
	if m, _ := filepath.Glob(path + ".broken-*"); len(m) == 0 {
		t.Fatal("the broken file was not kept")
	}
	a.db, _ = store.Open(path) // for the cleanup
}

// A cloud report that keeps failing with 5xx is parked instead of blocking every later report.
func TestOutboxDeadLetter(t *testing.T) {
	a := newTestAgent(t)
	got := map[string]int{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/v1/a" {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	a.cfg.APIBase = srv.URL
	a.enrolled = true
	// the head item has failed outboxDeadAfter-1 times already; the next 5xx parks it and the later item goes out
	_, _ = a.db.Exec(`INSERT INTO outbox(kind, method, path, body, createdAt, nextAt, attempts) VALUES('x','POST','/v1/a',NULL,?,0,?)`, time.Now().Unix(), outboxDeadAfter-1)
	_, _ = a.db.Exec(`INSERT INTO outbox(kind, method, path, body, createdAt, nextAt, attempts) VALUES('y','POST','/v1/b',NULL,?,0,0)`, time.Now().Unix())
	a.outboxOnce(context.Background())
	mu.Lock()
	ga, gb := got["/v1/a"], got["/v1/b"]
	mu.Unlock()
	if ga != 1 || gb != 1 {
		t.Fatalf("cloud saw a=%d b=%d (the parked head must not block b)", ga, gb)
	}
	if n := a.deadLetters(); n != 1 {
		t.Fatal(n)
	}
	var due int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE nextAt <= ?`, time.Now().Unix()).Scan(&due)
	if due != 0 {
		t.Fatal("a dead letter is still due")
	}
	if w := a.selfWarnings(); len(w) == 0 || !strings.Contains(strings.Join(w, " "), "parked") {
		t.Fatalf("%v", w)
	}
	// pruned after 30 days
	_, _ = a.db.Exec(`UPDATE outbox SET createdAt = ?`, time.Now().Add(-31*24*time.Hour).Unix())
	a.pruneStore(time.Now())
	if a.deadLetters() != 0 {
		t.Fatal("old dead letter not pruned")
	}
	// self-metrics answer
	ps := a.processStats()
	if ps["goroutines"].(int) <= 0 || ps["disk"].(map[string]int64)["totalBytes"] <= 0 {
		t.Fatalf("%v", ps)
	}
}
