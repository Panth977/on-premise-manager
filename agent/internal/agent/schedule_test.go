package agent

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/docker"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/validate"
)

// mkRevision builds a valid revision whose config holds the given (empty) sites.
func mkRevision(t *testing.T, seq int, parent *string, sites ...string) *model.Revision {
	cfg := validate.EmptyConfig()
	for _, s := range sites {
		cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: s, Label: s, Config: validate.DefaultSiteConfig(ptr("127.0.0.1"))})
	}
	cat := *emptyCatalog()
	h, _ := canonical.HashCanonical(cfg)
	ch, _ := canonical.HashCanonical(cat)
	return &model.Revision{RevID: newID(), Seq: seq, ParentRevID: parent, Origin: "cloud", Actor: "t", CreatedAt: nowTs(), Config: cfg, Hash: h, Catalog: cat, CatalogHash: ch, Summary: "s", Kind: "change"}
}

func setHead(t *testing.T, a *Agent, rev *model.Revision) {
	if err := a.putRevision(rev); err != nil {
		t.Fatal(err)
	}
	_ = a.db.SetKV("head", model.HeadHash{RevID: rev.RevID, Seq: rev.Seq, Hash: rev.Hash})
}

func needDocker(t *testing.T) {
	if err := docker.New(docker.DefaultHost("")).Ping(context.Background()); err != nil {
		t.Skip("no docker")
	}
}

func TestNextScheduledAt(t *testing.T) {
	loc := time.Local
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 1, 5, 1, 59, 59, 0, loc), time.Date(2026, 1, 5, 2, 0, 0, 0, loc)},
		{time.Date(2026, 1, 5, 2, 0, 0, 0, loc), time.Date(2026, 1, 6, 2, 0, 0, 0, loc)},
		{time.Date(2026, 1, 31, 23, 0, 0, 0, loc), time.Date(2026, 2, 1, 2, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		if got := nextScheduledAt(c.now, 2); !got.Equal(c.want) {
			t.Errorf("%v → %v want %v", c.now, got, c.want)
		}
	}
}

func TestScheduledSnapshotLoop(t *testing.T) {
	needDocker(t)
	a := newTestAgent(t)
	a.enrolled = true
	setHead(t, a, mkRevision(t, 1, nil, "app"))
	// injectable clock: 01:59:59.5 → the loop sleeps 500 ms (simulated) and fires the 02:00 snapshot
	fake := time.Date(2026, 3, 1, 1, 59, 59, 500e6, time.Local)
	a.clock = func() time.Time { return fake }
	var slept []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	a.sleepFn = func(c context.Context, d time.Duration) bool {
		slept = append(slept, d)
		if len(slept) > 1 {
			cancel()
			return false
		}
		return true
	}
	a.scheduleLoop(ctx)
	if len(slept) != 2 || slept[0] != 500*time.Millisecond {
		t.Fatalf("slept %v", slept)
	}
	waitFor(t, "scheduled snapshot part", 20*time.Second, func() bool {
		for _, s := range a.ownSnapshotSummaries() {
			if s.Reason == "scheduled" && s.CreatedBy == "system" && len(s.Sites) == 1 {
				return true
			}
		}
		return false
	})
	// not the first reachable site in slug order → does not start
	b := newTestAgent(t)
	b.enrolled = true
	setHead(t, b, mkRevision(t, 1, nil, "abc", "app"))
	b.peerState["abc"] = &peerRuntime{lastAnswer: time.Now()}
	if _, started := b.scheduledSnapshotOnce(context.Background()); started {
		t.Fatal("app is not first (abc is reachable)")
	}
	delete(b.peerState, "abc") // abc unreachable → app is the first reachable site, but abc must be reachable for a snapshot
	if _, started := b.scheduledSnapshotOnce(context.Background()); started {
		t.Fatal("snapshot needs every head-config site reachable")
	}
}

func TestSnapshotRetention(t *testing.T) {
	a := newTestAgent(t)
	ins := func(id, reason string) {
		_, _ = a.db.Exec(`INSERT INTO snapshots(snapshotId, createdAt, reason, seq, manifest, sizeBytes, createdBy, note, complete) VALUES(?,?,?,0,'{}',0,'system','',1)`, id, nowTs(), reason)
	}
	var sched, pre []string
	for i := 0; i < 9; i++ {
		id := newID()
		sched = append(sched, id)
		ins(id, "scheduled")
	}
	for i := 0; i < 12; i++ {
		id := newID()
		pre = append(pre, id)
		ins(id, "pre-update")
	}
	ins(newID(), "manual")
	ins(newID(), "imported")
	// the oldest pre-update belongs to a running update → protected
	plan := &model.UpdatePlan{UpdateID: newID(), Kind: "update", Revision: *mkRevision(t, 1, nil, "app"), PreSnapshotID: pre[0], Coordinator: "db", Sites: []string{"app"}, Order: []string{"app"}}
	a.savePlan(plan)
	_ = a.db.SetKV("lock", model.UpdateLock{UpdateID: plan.UpdateID, Coordinator: "db", StartedAt: nowTs(), LastContactAt: nowTs()})
	a.retention(sched[8])
	count := map[string]int{}
	ids := map[string]bool{}
	for _, s := range a.snapRows() {
		count[s.Reason]++
		ids[s.SnapshotID] = true
	}
	if count["scheduled"] != 7 || count["manual"] != 1 || count["imported"] != 1 || count["pre-update"] != 11 {
		t.Fatalf("retention %v", count)
	}
	if ids[sched[0]] || ids[sched[1]] || !ids[sched[2]] || !ids[pre[0]] || ids[pre[1]] {
		t.Fatal("wrong snapshots removed")
	}
	if e := a.deleteOwnSnapshot(pre[0], nil); e == nil || e.code != model.ErrConflict {
		t.Fatal("the pre-update snapshot of a running update cannot be deleted")
	}
}

func lockedParticipant(t *testing.T, a *Agent, contactAgo time.Duration) *model.UpdatePlan {
	rev := mkRevision(t, 1, nil, "app")
	plan := &model.UpdatePlan{UpdateID: newID(), Kind: "update", Revision: *rev, PreSnapshotID: newID(), Coordinator: "db", Sites: []string{"app", "db"}, Order: []string{"app", "db"}}
	a.savePlan(plan)
	old := tsOf(time.Now().Add(-contactAgo))
	_ = a.db.SetKV("lock", model.UpdateLock{UpdateID: plan.UpdateID, Kind: "update", RevID: rev.RevID, Coordinator: "db", StartedAt: old, LastContactAt: old})
	return plan
}

func TestAbandonUpdate(t *testing.T) {
	needDocker(t)
	a := newTestAgent(t)
	plan := lockedParticipant(t, a, time.Minute)
	sid := "S1"
	exp := time.Now().Add(time.Hour).Unix()
	_, _ = a.db.Exec(`INSERT INTO sessions(sessionId, createdAt, expiresAt, keyVersion, ip, userAgent) VALUES(?,?,?,?,?,?)`, sid, time.Now().Unix(), exp, 1, "", "")
	tok := sign.MakeActorToken(a.key, model.ActorTokenClaims{Email: "ops@example.com", Sid: sid, Dep: a.dep, Site: a.site, Exp: exp})
	abandon := func() int {
		r := httptest.NewRequest("POST", "/_opm/api/updates/"+plan.UpdateID+"/abandon", bytes.NewReader(nil))
		r.Header.Set("Cookie", "opm_session_app="+sid)
		r.Header.Set("Authorization", "Actor "+tok)
		rec := httptest.NewRecorder()
		a.handlerFor("gui-alias").ServeHTTP(rec, r)
		return rec.Code
	}
	if c := abandon(); c != 409 {
		t.Fatalf("coordinator in contact 1 min ago → %d", c)
	}
	// injectable clock: 11 minutes later the coordinator has been silent for > 10 min
	a.clock = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if c := abandon(); c != 200 {
		t.Fatalf("abandon → %d", c)
	}
	if a.lock() != nil {
		t.Fatal("lock not released")
	}
	if items, _ := a.activitiesPage(0, 10, "update.abandoned", ""); len(items) != 1 || items[0].Actor == nil || *items[0].Actor != "ops@example.com" {
		t.Fatalf("abandon activity %+v", items)
	}
	if items, _ := a.activitiesPage(0, 10, "update.rolled-back", ""); len(items) != 1 {
		t.Fatal("rolled-back activity missing")
	}
}

func TestFollowCoordinator(t *testing.T) {
	needDocker(t)
	// coordinator dropped the update and its head is not the revision → this site rolls back by itself
	a := newTestAgent(t)
	plan := lockedParticipant(t, a, 5*time.Minute)
	a.followCoordinator("db", &pingWire{PeerPing: model.PeerPing{SiteSlug: "db"}, ActiveUpdates: []string{plan.UpdateID}})
	if a.lock() == nil {
		t.Fatal("coordinator still runs the update: keep the lock")
	}
	a.followCoordinator("db", &pingWire{PeerPing: model.PeerPing{SiteSlug: "db"}})
	waitFor(t, "automatic rollback", 30*time.Second, func() bool { return a.lock() == nil })
	if items, _ := a.activitiesPage(0, 10, "update.abandoned", ""); len(items) != 1 {
		t.Fatal("update.abandoned missing")
	}
	// coordinator committed (its head = the revision) → this site commits
	b := newTestAgent(t)
	plan = lockedParticipant(t, b, 5*time.Minute)
	b.followCoordinator("db", &pingWire{PeerPing: model.PeerPing{SiteSlug: "db", Head: &model.HeadHash{RevID: plan.Revision.RevID, Seq: 1}}})
	if h := b.head(); h == nil || h.RevID != plan.Revision.RevID || b.lock() != nil {
		t.Fatalf("commit by following: head %+v lock %+v", h, b.lock())
	}
	// a fresh lock (< 30 s) is never resolved by pings (start of an update)
	c := newTestAgent(t)
	lockedParticipant(t, c, time.Second)
	c.followCoordinator("db", &pingWire{PeerPing: model.PeerPing{SiteSlug: "db"}})
	if c.lock() == nil {
		t.Fatal("fresh lock resolved")
	}
}
