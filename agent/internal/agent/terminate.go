package agent

// §21 #110 an update never freezes the agent.
//
// The update lock is ADVISORY: it tells the GUI (and the peers) that an update runs, nothing more. No action waits for
// it: container actions, volume files, snapshots, local commits, merges, restarts and upgrades all go ahead, and a gate
// only warns about it. Starting a new update while one runs stops the running one first (no roll back).
//
// A running update can be stopped by a person at any time, from any site of its plan (POST
// /_opm/api/updates/{updateId}/terminate):
//   - every site cancels the loops of that update (coordinator + its own step), drops its lock and marks it failed
//     ("terminated"); nothing is rolled back, the containers stay as they are;
//   - rollback: each site then converges back to its head config with the volumes of the update's pre-update
//     snapshot (the last backup before the update) — a recovery, itself shown as a running "rollback" and stoppable;
//   - restartAgent: each agent restarts (with rollback: the recovery runs right after the restart).
// A site that is not reachable learns it from the next ping of any site that terminated it (pingWire.Terminated).

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/model"
)

const terminatedKeep = 50

type terminatedRec struct {
	UpdateID string `json:"updateId"`
	At       string `json:"at"`
}

// pendingRecovery: a recovery to run right after the agent restarted (terminate with rollback + restartAgent).
type pendingRecovery struct {
	UpdateID string  `json:"updateId"`
	Actor    *string `json:"actor"`
	Reason   string  `json:"reason"`
}

// updateCtx derives a context that terminate cancels; call done when the loop ends.
func (a *Agent) updateCtx(id string, parent context.Context) (context.Context, func()) {
	c, cancel := context.WithCancel(parent)
	a.mu.Lock()
	a.updCtlN++
	n := a.updCtlN
	if a.updCtl[id] == nil {
		a.updCtl[id] = map[int]context.CancelFunc{}
	}
	a.updCtl[id][n] = cancel
	a.mu.Unlock()
	return c, func() {
		cancel()
		a.mu.Lock()
		delete(a.updCtl[id], n)
		if len(a.updCtl[id]) == 0 {
			delete(a.updCtl, id)
		}
		a.mu.Unlock()
	}
}

func (a *Agent) cancelUpdate(id string) {
	a.mu.Lock()
	var fns []context.CancelFunc
	for _, f := range a.updCtl[id] {
		fns = append(fns, f)
	}
	a.mu.Unlock()
	for _, f := range fns {
		f()
	}
}

func (a *Agent) terminatedIDs() []string {
	var recs []terminatedRec
	_, _ = a.db.GetKV("terminated", &recs)
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.UpdateID)
	}
	return out
}

func (a *Agent) wasTerminated(id string) bool { return contains(a.terminatedIDs(), id) }

func (a *Agent) rememberTerminated(id string) {
	var recs []terminatedRec
	_, _ = a.db.GetKV("terminated", &recs)
	for _, r := range recs {
		if r.UpdateID == id {
			return
		}
	}
	recs = append(recs, terminatedRec{UpdateID: id, At: nowTs()})
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].At < recs[j].At })
	if len(recs) > terminatedKeep {
		recs = recs[len(recs)-terminatedKeep:]
	}
	_ = a.db.SetKV("terminated", recs)
}

// terminateLocal stops update id on THIS site: its loops, its lock, its run (coordinator) or step (participant).
// Nothing is rolled back. Returns whether anything was running here.
func (a *Agent) terminateLocal(id, reason string, actor *string) bool {
	a.rememberTerminated(id) // first: the loops that end now see it and stay quiet
	a.cancelUpdate(id)
	msg := "terminated: " + reason
	touched := false
	if l := a.lock(); l != nil && l.UpdateID == id {
		// atomic: only this update's lock
		_, _ = a.db.Exec(`DELETE FROM kv WHERE key = 'lock' AND json_extract(value, '$.updateId') = ?`, id)
		touched = true
	}
	if st := a.siteStateOf(id); st != nil && st.State == "running" {
		st.State = "failed"
		st.Error = &msg
		fin := nowTs()
		st.FinishedAt = &fin
		a.setSiteState(st)
		touched = true
	}
	a.finishSiteUpdate(id)
	plan, hasPlan := a.loadPlan(id)
	if run, ok := a.loadRun(id); ok && run.FinishedAt == nil && run.Coordinator == a.site {
		var snap *string
		if hasPlan {
			s := plan.PreSnapshotID
			snap = &s
		}
		a.finishRun(run, false, msg, snap)
		touched = true
	}
	if hasPlan {
		if _, ok := plan.Commits[a.site]; ok {
			_, _ = a.db.Exec(`UPDATE merge_runs SET state = 'failed', finishedAt = ? WHERE runId = ? AND finishedAt IS NULL`, nowTs(), id)
			if mr, ok := a.loadMergeRun(id); ok && mr != nil && mr.LegacyWorking {
				a.legacyWorkingFailed("the update was terminated: " + reason)
			}
		}
		if rev := plan.RevisionFor(a.site); rev != nil {
			if p, ok := a.proposal(rev.RevID); ok && !model.IsTerminal(p.Status) && p.Status != model.ProposalReady {
				p.Status = model.ProposalReady
				p.ApprovedBy, p.ApprovedAt, p.UpdateID = nil, nil, nil
				p.Result = &model.ProposalResult{Ok: false, Message: msg}
				p.UpdatedAt = nowTs()
				a.putProposal(p)
				if plan.Coordinator == a.site {
					a.queueStatus(p.RevID, model.ProposalStatusReport{SiteSlug: a.site, Status: model.ProposalReady, Result: p.Result, UpdateID: id})
				}
			}
		}
	}
	if touched {
		a.activity(actor, "agent", "update.terminated", trim200(fmt.Sprintf("update terminated on %s: %s", a.site, reason)), map[string]any{"updateId": id, "siteSlug": a.site, "reason": reason})
		a.log.Warn("coordinator", "update terminated", "updateId", id, "reason", reason)
	}
	a.kickLoop("ping")
	return touched
}

func (a *Agent) lockOf(id string) *model.UpdateLock {
	if l := a.lock(); l != nil && l.UpdateID == id {
		return l
	}
	return nil
}

// terminateOnSite: terminateLocal + the optional recovery / restart of this site.
func (a *Agent) terminateOnSite(id string, req model.TerminateUpdateRequest, actor *string) string {
	reason := req.Reason
	if reason == "" {
		reason = "stopped by a person"
	}
	ran := a.terminateLocal(id, reason, actor)
	msg := "nothing was running here"
	if ran {
		msg = "stopped"
	}
	switch {
	case req.Rollback && req.RestartAgent:
		_ = a.db.SetKV("recover", pendingRecovery{UpdateID: id, Actor: actor, Reason: reason})
		a.restartAgent(actor, map[string]any{"updateId": id}, "terminate + roll back: "+reason)
		msg += "; the agent restarts, then rolls back to the pre-update backup"
	case req.Rollback:
		a.startRecovery(id, actor, reason)
		msg += "; rolling back to the pre-update backup"
	case req.RestartAgent:
		if n := a.restartStopped(id); n != "" {
			msg += "; " + n
		}
		a.restartAgent(actor, map[string]any{"updateId": id}, "terminate: "+reason)
		msg += "; the agent restarts"
	default:
		if n := a.restartStopped(id); n != "" {
			msg += "; " + n
		}
	}
	return msg
}

// restartStopped starts again the service containers of this site's HEAD config that THIS update touched (stopped for
// the pre-update snapshot, or stopped / recreated by the apply) and that are not running now — a Stop must not leave a
// service down. Start only: nothing is recreated or removed; containers a person stopped are never touched. Returns a
// note for the answer ("" = nothing to start).
func (a *Agent) restartStopped(id string) string {
	if a.dc == nil || a.drv == nil {
		return ""
	}
	touched := map[string]bool{}
	if st := a.siteStateOf(id); st != nil {
		for _, c := range st.Containers {
			if c.Action != "keep" && c.Action != "remove" {
				touched[c.ContainerSlug] = true
			}
		}
	}
	if len(touched) == 0 {
		return ""
	}
	rev := a.headRevision()
	if rev == nil {
		return ""
	}
	site := siteOf(revConfig(rev), a.site)
	if site == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()
	builts, err := a.buildSite(rev, site)
	if err != nil {
		return ""
	}
	ex, err := a.drv.Existing(ctx)
	if err != nil {
		return ""
	}
	var started, failed []string
	for _, b := range builts {
		if !b.Enabled || b.Kind == "job" || !touched[b.ContainerSlug] {
			continue
		}
		e, ok := ex[b.ContainerSlug]
		if !ok {
			continue // missing (recreated half-way): Stop & roll back brings it back
		}
		cj, err := a.dc.ContainerInspect(ctx, e.ID)
		if err != nil || cj.State.Running || cj.State.Restarting {
			continue
		}
		if err := a.dc.ContainerStart(ctx, e.ID); err != nil {
			failed = append(failed, b.ContainerSlug+" ("+err.Error()+")")
			continue
		}
		started = append(started, b.ContainerSlug)
	}
	if len(started)+len(failed) == 0 {
		return ""
	}
	note := ""
	if len(started) > 0 {
		note = "started again: " + strings.Join(started, ", ")
	}
	if len(failed) > 0 {
		if note != "" {
			note += "; "
		}
		note += "could not start: " + strings.Join(failed, ", ")
	}
	a.activity(nil, "system", "container.started", trim200("after the update was stopped, "+note), map[string]any{"updateId": id, "started": started, "failed": failed})
	return note
}

// startRecovery converges this site back to its head config with the volumes of update id's pre-update snapshot
// (when this site has its part; else the head config only). It holds a "rollback" lock of its own (advisory,
// terminable like any update).
func (a *Agent) startRecovery(id string, actor *string, reason string) {
	plan, _ := a.loadPlan(id)
	recID := "recover-" + id
	now := nowTs()
	_ = a.db.SetKV("lock", model.UpdateLock{UpdateID: recID, Kind: "rollback", RevID: "", Coordinator: a.site, StartedAt: now, LastContactAt: now})
	a.kickLoop("ping")
	a.goLoop("recover", func(parent context.Context) {
		c, done := a.updateCtx(recID, parent)
		defer done()
		c, cancel := context.WithTimeout(c, stepTimeout[model.StepRollback]+siteStepGrace) // bounded like a rollback step
		defer cancel()
		start := time.Now()
		st := &model.UpdateSiteState{UpdateID: recID, SiteSlug: a.site, State: "running", Containers: []model.UpdateContainerState{}}
		s := model.StepRollback
		st.Step = &s
		prev := a.headRevision()
		o := applyOpts{rev: prev, state: st, rollback: true}
		snap := ""
		if plan != nil && a.hasSnapshotPart(plan.PreSnapshotID) {
			o.restoreFrom, snap = plan.PreSnapshotID, plan.PreSnapshotID
		}
		var err error
		if prev == nil {
			err = fmt.Errorf("this site has no head config to roll back to")
		} else {
			err = a.applySite(c, o)
		}
		_, _ = a.db.Exec(`DELETE FROM kv WHERE key = 'lock' AND json_extract(value, '$.updateId') = ?`, recID)
		a.kickLoop("ping")
		if c.Err() != nil && a.wasTerminated(recID) {
			return // stopped by a person: terminateLocal logged it
		}
		summary := fmt.Sprintf("rolled back on %s to the pre-update backup %s", a.site, snap)
		if snap == "" {
			summary = fmt.Sprintf("rolled back on %s to its head config (no pre-update backup on this site)", a.site)
		}
		data := map[string]any{"updateId": id, "siteSlug": a.site, "snapshotId": snap, "reason": reason, "durationMs": time.Since(start).Milliseconds()}
		if err != nil {
			summary += " — with errors: " + err.Error()
			data["error"] = err.Error()
		}
		a.activity(actor, "agent", "update.recovered", trim200(summary), data)
	})
}

// resumeRecovery: a recovery requested before a restart (terminate with rollback + restartAgent).
func (a *Agent) resumeRecovery() {
	var p pendingRecovery
	if ok, _ := a.db.GetKV("recover", &p); !ok || p.UpdateID == "" {
		// a recovery that was running when the agent stopped: its loop is gone, so is its lock (nothing waits on it)
		if l := a.lock(); l != nil && strings.HasPrefix(l.UpdateID, "recover-") {
			a.terminateLocal(l.UpdateID, "the agent restarted during the roll back (run it again with Stop & roll back)", nil)
		}
		return
	}
	_ = a.db.DelKV("recover")
	a.startRecovery(p.UpdateID, p.Actor, p.Reason)
}

// supersedeRunning stops every update this site knows to be running (its own lock, its coordinated runs) before a new
// one starts. Returns the stopped ids (for the activity of the new update).
func (a *Agent) supersedeRunning(ctx context.Context, by string, actor *string) []string {
	ids := map[string]bool{}
	if l := a.lock(); l != nil {
		ids[l.UpdateID] = true
	}
	for _, id := range a.activeUpdates() {
		if r, ok := a.loadRun(id); ok && r.FinishedAt == nil {
			ids[id] = true
		}
	}
	var out []string
	for id := range ids {
		a.terminateEverywhere(ctx, id, model.TerminateUpdateRequest{Reason: "superseded by " + by}, actor)
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// terminateEverywhere: every other site of the plan first (peer call), this site last.
func (a *Agent) terminateEverywhere(ctx context.Context, id string, req model.TerminateUpdateRequest, actor *string) []model.TerminateSiteResult {
	sites := []string{a.site}
	if plan, ok := a.loadPlan(id); ok {
		sites = union(union(plan.Sites, []string{plan.Coordinator}), sites)
	} else if l := a.lock(); l != nil && l.UpdateID == id && l.Coordinator != "" {
		sites = union([]string{l.Coordinator}, sites)
	}
	sort.Strings(sites)
	var res []model.TerminateSiteResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	body := peerTerminate{TerminateUpdateRequest: req, Actor: actor}
	for _, s := range sites {
		if s == a.site {
			continue
		}
		wg.Add(1)
		go func(s string) { // parallel: one slow site never delays the others
			defer wg.Done()
			defer a.log.Recover("update", "terminate fan-out "+s)
			r := model.TerminateSiteResult{SiteSlug: s}
			var out model.TerminateSiteResult
			switch {
			case !a.reachable(s):
				r.Message = "not reachable: it stops the update when it next sees a site that stopped it"
			default:
				if err := a.peerJSON(ctx, s, "POST", "/_opm/peer/update/"+id+"/terminate", body, &out, 20*time.Second); err != nil {
					r.Message = err.Error() + " (it stops the update when it next sees a site that stopped it)"
				} else {
					r.Ok, r.Message = true, out.Message
				}
			}
			mu.Lock()
			res = append(res, r)
			mu.Unlock()
		}(s)
	}
	wg.Wait()
	res = append(res, model.TerminateSiteResult{SiteSlug: a.site, Ok: true, Message: a.terminateOnSite(id, req, actor)})
	sort.Slice(res, func(i, j int) bool { return res[i].SiteSlug < res[j].SiteSlug })
	return res
}

type peerTerminate struct {
	model.TerminateUpdateRequest
	Actor *string `json:"actor"`
}

// POST /_opm/api/updates/{updateId}/terminate ("current" = the update this site is locked for, or coordinates)
func (a *Agent) handleTerminate(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.TerminateUpdateRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if len(req.Reason) > 200 {
		req.Reason = req.Reason[:200]
	}
	id := r.PathValue("updateId")
	if id == "current" {
		id = ""
		if l := a.lock(); l != nil {
			id = l.UpdateID
		}
		for _, x := range a.activeUpdates() {
			if run, ok := a.loadRun(x); ok && run.FinishedAt == nil && id == "" {
				id = x
			}
		}
		if id == "" {
			return nil, 0, herr(model.ErrNotFound, "no update is running on this site", nil)
		}
	}
	if req.Reason == "" {
		req.Reason = "stopped by " + rc.actor
	}
	// a recovery is local to its site: stopping it touches only this site
	if len(id) > len("recover-") && id[:len("recover-")] == "recover-" {
		return model.TerminateUpdateResponse{UpdateID: id, Sites: []model.TerminateSiteResult{{SiteSlug: a.site, Ok: true, Message: a.terminateOnSite(id, model.TerminateUpdateRequest{Reason: req.Reason, RestartAgent: req.RestartAgent}, rc.actorPtr())}}}, 200, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res := a.terminateEverywhere(ctx, id, req, rc.actorPtr())
	return model.TerminateUpdateResponse{UpdateID: id, Sites: res}, 200, nil
}

// POST /_opm/peer/update/{updateId}/terminate (any site of the deployment)
func (a *Agent) handlePeerTerminate(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	var req peerTerminate
	if e := readJSON(r, &req); e != nil {
		return nil, e
	}
	caller := getRC(r).peer
	if caller == nil {
		return nil, herr(model.ErrUnauthenticated, "peer only", nil)
	}
	id := r.PathValue("updateId")
	reason := req.Reason
	if reason == "" {
		reason = "stopped on " + caller.Site
	}
	req.Reason = reason + " (from " + caller.Site + ")"
	return model.TerminateSiteResult{SiteSlug: a.site, Ok: true, Message: a.terminateOnSite(id, req.TerminateUpdateRequest, req.Actor)}, nil
}

// ───────────────────────── watchdog (§21 #110): no update, roll back or lock outlives its deadline

// orphanLockGrace: a lock whose update this site has no plan for (a crash between the lock and the plan, a damaged row).
const orphanLockGrace = 15 * time.Minute

// updateDeadline: the longest an update of this plan may take end to end — every step at its timeout, the apply and
// the roll back of every site one after the other — plus margin. Past it, the watchdog stops the update.
func updateDeadline(plan *model.UpdatePlan) time.Duration {
	n := len(plan.Sites)
	if len(plan.Order) > n {
		n = len(plan.Order)
	}
	if n < 1 {
		n = 1
	}
	per := stepTimeout[model.StepApply] + stepTimeout[model.StepRollback] + 2*siteStepGrace
	return stepTimeout[model.StepLock] + stepTimeout[model.StepSnapshot] + stepTimeout[model.StepVerify] + stepTimeout[model.StepCommit] +
		time.Duration(n)*per + 10*time.Minute
}

func recoveryDeadline() time.Duration {
	return stepTimeout[model.StepRollback] + siteStepGrace + 5*time.Minute
}

// lockDeadline: how long the lock l may be held on this site.
func (a *Agent) lockDeadline(l *model.UpdateLock) time.Duration {
	if strings.HasPrefix(l.UpdateID, "recover-") {
		return recoveryDeadline()
	}
	if plan, ok := a.loadPlan(l.UpdateID); ok {
		return updateDeadline(plan)
	}
	return orphanLockGrace
}

// watchdogOnce stops (no roll back) the lock and the coordinated runs of this site that passed their deadline. The
// other sites stop them too when they see this site's ping (pingWire.Terminated). Every minute.
func (a *Agent) watchdogOnce(_ context.Context) {
	now := a.clock()
	if l := a.lock(); l != nil {
		limit := a.lockDeadline(l)
		if age := now.Sub(parseTs(l.StartedAt)); !parseTs(l.StartedAt).IsZero() && age > limit {
			a.watchdogStop(l.UpdateID, age, limit)
		} else if parseTs(l.StartedAt).IsZero() {
			a.watchdogStop(l.UpdateID, 0, 0) // an unreadable lock
		}
	}
	for _, id := range a.activeUpdates() {
		run, ok := a.loadRun(id)
		plan, ok2 := a.loadPlan(id)
		if !ok || !ok2 || run.FinishedAt != nil {
			continue
		}
		limit := updateDeadline(plan)
		if age := now.Sub(parseTs(run.StartedAt)); age > limit {
			a.watchdogStop(id, age, limit)
		}
	}
}

func (a *Agent) watchdogStop(id string, age, limit time.Duration) {
	reason := "watchdog: the lock could not be read"
	if limit > 0 {
		reason = fmt.Sprintf("watchdog: running for %s, longer than its deadline of %s", age.Round(time.Minute), limit.Round(time.Minute))
	}
	a.terminateLocal(id, reason, nil)
}
