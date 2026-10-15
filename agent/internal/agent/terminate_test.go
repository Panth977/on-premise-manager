package agent

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

// §21 #110 an update never freezes the agent: stop from any site, supersede, follow, repair.

func actorCall(t *testing.T, a *Agent, method, path string, body any) (int, []byte) {
	t.Helper()
	sid := "S-term"
	exp := time.Now().Add(time.Hour).Unix()
	_, _ = a.db.Exec(`INSERT OR IGNORE INTO sessions(sessionId, createdAt, expiresAt, keyVersion, ip, userAgent) VALUES(?,?,?,?,?,?)`, sid, time.Now().Unix(), exp, 1, "", "")
	tok := sign.MakeActorToken(a.key, model.ActorTokenClaims{Email: "ops@example.com", Sid: sid, Dep: a.dep, Site: a.site, Exp: exp})
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Cookie", "opm_session_"+a.site+"="+sid)
	r.Header.Set("Authorization", "Actor "+tok)
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.handlerFor("gui-alias").ServeHTTP(rec, r)
	return rec.Code, rec.Body.Bytes()
}

func TestTerminateParticipant(t *testing.T) {
	a := newTestAgent(t)
	plan := lockedParticipant(t, a, time.Minute) // coordinator "db" (not reachable in a unit test)
	st := &model.UpdateSiteState{UpdateID: plan.UpdateID, SiteSlug: a.site, State: "running", Containers: []model.UpdateContainerState{}}
	s := model.StepApply
	st.Step = &s
	a.setSiteState(st)
	code, body := actorCall(t, a, "POST", "/_opm/api/updates/current/terminate", model.TerminateUpdateRequest{Reason: "stuck"})
	if code != 200 {
		t.Fatalf("terminate → %d %s", code, body)
	}
	var res model.TerminateUpdateResponse
	_ = json.Unmarshal(body, &res)
	if res.UpdateID != plan.UpdateID || len(res.Sites) != 2 {
		t.Fatalf("result %+v", res)
	}
	for _, r := range res.Sites {
		if r.SiteSlug == "db" && r.Ok {
			t.Fatalf("db is not reachable: %+v", r)
		}
		if r.SiteSlug == a.site && (!r.Ok || r.Message != "stopped") {
			t.Fatalf("this site: %+v", r)
		}
	}
	if a.lock() != nil {
		t.Fatal("lock kept")
	}
	if got := a.siteStateOf(plan.UpdateID); got == nil || got.State != "failed" || got.Error == nil {
		t.Fatalf("site state %+v", got)
	}
	if !a.wasTerminated(plan.UpdateID) || !contains(a.selfPing().Terminated, plan.UpdateID) {
		t.Fatal("not remembered / not in the ping")
	}
	if items, _ := a.activitiesPage(0, 10, "update.terminated", ""); len(items) != 1 || items[0].Actor == nil {
		t.Fatalf("activity %+v", items)
	}
	// a late coordinator never starts a step of a terminated update here
	if err := a.startSiteStep("db", plan.UpdateID, model.UpdateStepRequest{Step: model.StepApply}); err == nil {
		t.Fatal("a step of a terminated update started")
	}
	// nothing running any more
	if code, _ := actorCall(t, a, "POST", "/_opm/api/updates/current/terminate", model.TerminateUpdateRequest{}); code != 404 {
		t.Fatalf("second terminate of current → %d", code)
	}
}

func TestTerminateCoordinatorRun(t *testing.T) {
	a := newTestAgent(t)
	rev := mkRevision(t, 1, nil, a.site)
	plan := &model.UpdatePlan{UpdateID: newID(), Kind: "update", Revision: *rev, PreSnapshotID: newID(), Coordinator: a.site, Sites: []string{a.site}, Order: []string{a.site}}
	a.savePlan(plan)
	run := &model.UpdateRun{UpdateID: plan.UpdateID, Kind: "update", RevID: rev.RevID, Coordinator: a.site, Phase: model.StepApply, Sites: map[string]model.UpdateSiteState{}, StartedAt: nowTs()}
	a.saveRun(run)
	_ = a.db.SetKV("lock", model.UpdateLock{UpdateID: plan.UpdateID, Kind: "update", RevID: rev.RevID, Coordinator: a.site, StartedAt: nowTs(), LastContactAt: nowTs()})
	// a loop of this update, cancelled by terminate
	c, done := a.updateCtx(plan.UpdateID, t.Context())
	defer done()
	if code, body := actorCall(t, a, "POST", "/_opm/api/updates/"+plan.UpdateID+"/terminate", model.TerminateUpdateRequest{}); code != 200 {
		t.Fatalf("terminate → %d %s", code, body)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the update's loop was not cancelled")
	}
	r, ok := a.loadRun(plan.UpdateID)
	if !ok || r.FinishedAt == nil || r.Result == nil || r.Result.Ok {
		t.Fatalf("run %+v", r)
	}
	if len(a.activeUpdates()) != 0 || a.lock() != nil {
		t.Fatal("still active / locked")
	}
	// the cancelled coordinator's late writes never resurrect the run
	run.Phase = model.StepVerify
	a.saveRun(run)
	a.finishRun(run, true, "applied", nil)
	if r, _ := a.loadRun(plan.UpdateID); r.Result == nil || r.Result.Ok || r.FinishedAt == nil {
		t.Fatalf("resurrected %+v", r)
	}
}

func TestFollowTerminated(t *testing.T) {
	a := newTestAgent(t)
	plan := lockedParticipant(t, a, time.Second) // even a fresh lock
	a.followCoordinator("db", &pingWire{PeerPing: model.PeerPing{SiteSlug: "db"}, ActiveUpdates: []string{plan.UpdateID}, Terminated: []string{plan.UpdateID}})
	if a.lock() != nil {
		t.Fatal("a site that saw the update terminated keeps its lock")
	}
	if items, _ := a.activitiesPage(0, 10, "update.abandoned", ""); len(items) != 0 {
		t.Fatal("terminate must not roll back")
	}
	// any site may announce it (not only the coordinator)
	b := newTestAgent(t)
	plan = lockedParticipant(t, b, time.Minute)
	b.followCoordinator("other", &pingWire{PeerPing: model.PeerPing{SiteSlug: "other"}, Terminated: []string{plan.UpdateID}})
	if b.lock() != nil {
		t.Fatal("terminated by a non-coordinator: lock kept")
	}
}

func TestLockStepSupersedes(t *testing.T) {
	a := newTestAgent(t)
	old := lockedParticipant(t, a, time.Minute)
	rev := mkRevision(t, 1, nil, "app")
	plan := &model.UpdatePlan{UpdateID: newID(), Kind: "update", Revision: *rev, PreSnapshotID: newID(), Coordinator: "db", Sites: []string{"app", "db"}, Order: []string{"app", "db"}}
	if err := a.startSiteStep("db", plan.UpdateID, model.UpdateStepRequest{Step: model.StepLock, Plan: plan}); err != nil {
		t.Fatalf("lock step refused: %v", err)
	}
	waitFor(t, "new lock", 5*time.Second, func() bool { l := a.lock(); return l != nil && l.UpdateID == plan.UpdateID })
	if !a.wasTerminated(old.UpdateID) {
		t.Fatal("the older update was not stopped")
	}
}

func TestIdleGateWarns(t *testing.T) {
	if g := idleWarn(nil); !g.Ok || g.Warn {
		t.Fatalf("%+v", g)
	}
	if g := idleWarn([]string{"app"}); !g.Ok || !g.Warn {
		t.Fatalf("a running update closes the gate: %+v", g)
	}
}

func TestRepairStuck(t *testing.T) {
	a := newTestAgent(t)
	plan := lockedParticipant(t, a, time.Hour)
	rep, err := a.Repair(RepairOptions{CheckOnly: true})
	if err != nil || !rep.DatabaseOk || len(rep.Stuck) != 1 || rep.Stuck[0] != plan.UpdateID || len(rep.Terminated) != 0 {
		t.Fatalf("check %+v %v", rep, err)
	}
	if a.lock() == nil {
		t.Fatal("--check changed something")
	}
	rep, err = a.Repair(RepairOptions{})
	if err != nil || len(rep.Terminated) != 1 || rep.RolledBack {
		t.Fatalf("repair %+v %v", rep, err)
	}
	if a.lock() != nil || !a.wasTerminated(plan.UpdateID) {
		t.Fatal("still stuck")
	}
	if rep, _ := a.Repair(RepairOptions{CheckOnly: true}); len(rep.Stuck) != 0 {
		t.Fatalf("stuck after repair %+v", rep)
	}
}

func TestWatchdog(t *testing.T) {
	ctx := t.Context()
	// a plan's lock within its deadline is kept, past it the watchdog stops it (no roll back)
	a := newTestAgent(t)
	plan := lockedParticipant(t, a, 2*time.Hour)
	if d := updateDeadline(plan); d < 2*time.Hour || d > 4*time.Hour {
		t.Fatalf("2-site deadline %s", d)
	}
	a.watchdogOnce(ctx)
	if a.lock() == nil {
		t.Fatal("stopped within its deadline")
	}
	a.clock = func() time.Time { return time.Now().Add(updateDeadline(plan)) }
	a.watchdogOnce(ctx)
	if a.lock() != nil || !a.wasTerminated(plan.UpdateID) {
		t.Fatal("past its deadline: not stopped")
	}
	if items, _ := a.activitiesPage(0, 10, "update.terminated", ""); len(items) != 1 {
		t.Fatalf("activity %+v", items)
	}
	// a lock without a plan: 15 min
	b := newTestAgent(t)
	_ = b.db.SetKV("lock", model.UpdateLock{UpdateID: "ORPHAN", Kind: "update", Coordinator: "db", StartedAt: tsOf(time.Now().Add(-5 * time.Minute))})
	b.watchdogOnce(ctx)
	if b.lock() == nil {
		t.Fatal("orphan lock stopped too early")
	}
	b.clock = func() time.Time { return time.Now().Add(11 * time.Minute) }
	b.watchdogOnce(ctx)
	if b.lock() != nil {
		t.Fatal("orphan lock kept")
	}
	// an unreadable lock goes at once; a roll back lock after its own deadline
	_ = b.db.SetKV("lock", model.UpdateLock{UpdateID: "BAD", Coordinator: "app"})
	b.watchdogOnce(ctx)
	if b.lock() != nil {
		t.Fatal("unreadable lock kept")
	}
	_ = b.db.SetKV("lock", model.UpdateLock{UpdateID: "recover-X", Kind: "rollback", Coordinator: b.site, StartedAt: tsOf(time.Now())})
	b.clock = func() time.Time { return time.Now().Add(recoveryDeadline() + time.Minute) }
	b.watchdogOnce(ctx)
	if b.lock() != nil {
		t.Fatal("roll back lock past its deadline kept")
	}
	// an unfinished coordinated run past its deadline
	c := newTestAgent(t)
	rev := mkRevision(t, 1, nil, c.site)
	p := &model.UpdatePlan{UpdateID: newID(), Kind: "update", Revision: *rev, PreSnapshotID: newID(), Coordinator: c.site, Sites: []string{c.site}, Order: []string{c.site}}
	c.savePlan(p)
	c.saveRun(&model.UpdateRun{UpdateID: p.UpdateID, Kind: "update", RevID: rev.RevID, Coordinator: c.site, Phase: model.StepApply, Sites: map[string]model.UpdateSiteState{}, StartedAt: nowTs()})
	c.clock = func() time.Time { return time.Now().Add(updateDeadline(p) + time.Minute) }
	c.watchdogOnce(ctx)
	if r, _ := c.loadRun(p.UpdateID); r == nil || r.FinishedAt == nil || len(c.activeUpdates()) != 0 {
		t.Fatalf("run past its deadline still active %+v", r)
	}
}

func TestInterruptedRecoveryAtStart(t *testing.T) {
	a := newTestAgent(t)
	_ = a.db.SetKV("lock", model.UpdateLock{UpdateID: "recover-Y", Kind: "rollback", Coordinator: a.site, StartedAt: nowTs()})
	a.resumeRecovery()
	if a.lock() != nil {
		t.Fatal("the lock of a roll back interrupted by a restart is kept")
	}
}
