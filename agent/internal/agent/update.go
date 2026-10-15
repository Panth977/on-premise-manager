package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/store"
	"opm/agent/internal/validate"
)

// stepTimeout: how long the coordinator waits for a step; a site gives its own step this + siteStepGrace, then cancels
// it (§21 #110: no step runs forever). Apply 30 min: a container with a 900 s start period + retries needs ~17 min.
var stepTimeout = map[string]time.Duration{
	model.StepLock: 10 * time.Second, model.StepSnapshot: 30 * time.Minute, model.StepApply: 30 * time.Minute,
	model.StepVerify: 5 * time.Minute, model.StepCommit: 30 * time.Second, model.StepRollback: 30 * time.Minute, model.StepUnlock: 30 * time.Second,
}

// ───────────────────────── persistence

func (a *Agent) savePlan(plan *model.UpdatePlan) {
	_, _ = a.db.Exec(`INSERT INTO updates(updateId, plan, startedAt) VALUES(?,?,?) ON CONFLICT(updateId) DO NOTHING`, plan.UpdateID, store.JSON(plan), time.Now().Unix())
}

func (a *Agent) loadPlan(id string) (*model.UpdatePlan, bool) {
	var raw string
	if err := a.db.QueryRow(`SELECT plan FROM updates WHERE updateId = ?`, id).Scan(&raw); err != nil {
		return nil, false
	}
	var p model.UpdatePlan
	if json.Unmarshal([]byte(raw), &p) != nil {
		return nil, false
	}
	return &p, true
}

func (a *Agent) saveRun(run *model.UpdateRun) {
	a.mu.Lock()
	if prev := a.runs[run.UpdateID]; prev != nil && prev.FinishedAt != nil && run.FinishedAt == nil {
		a.mu.Unlock()
		return // §21 #110 a late write of a terminated run's loop
	}
	cp := *run
	cp.Sites = map[string]model.UpdateSiteState{}
	for k, v := range run.Sites {
		cp.Sites[k] = v
	}
	a.runs[run.UpdateID] = &cp
	a.mu.Unlock()
	var fin any
	if run.FinishedAt != nil {
		fin = parseTs(*run.FinishedAt).Unix()
	}
	_, _ = a.db.Exec(`UPDATE updates SET run = ?, finishedAt = ? WHERE updateId = ?`, store.JSON(cp), fin, run.UpdateID)
}

func (a *Agent) loadRun(id string) (*model.UpdateRun, bool) {
	a.mu.Lock()
	if r := a.runs[id]; r != nil {
		cp := *r
		a.mu.Unlock()
		return &cp, true
	}
	a.mu.Unlock()
	var raw sql.NullString
	if err := a.db.QueryRow(`SELECT run FROM updates WHERE updateId = ?`, id).Scan(&raw); err != nil || !raw.Valid {
		return nil, false
	}
	var r model.UpdateRun
	if json.Unmarshal([]byte(raw.String), &r) != nil {
		return nil, false
	}
	return &r, true
}

func (a *Agent) siteStateOf(id string) *model.UpdateSiteState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.siteStates[id]; s != nil {
		cp := *s
		cp.Containers = append([]model.UpdateContainerState{}, s.Containers...)
		return &cp
	}
	return nil
}

func (a *Agent) setSiteState(s *model.UpdateSiteState) {
	if s.Containers == nil {
		s.Containers = []model.UpdateContainerState{}
	}
	a.mu.Lock()
	cp := *s
	cp.Containers = append([]model.UpdateContainerState{}, s.Containers...)
	a.siteStates[s.UpdateID] = &cp
	a.mu.Unlock()
	_, _ = a.db.Exec(`UPDATE updates SET site = ? WHERE updateId = ?`, store.JSON(cp), s.UpdateID)
}

func (a *Agent) activeUpdates() []string {
	rows, err := a.db.Query(`SELECT updateId, plan FROM updates WHERE finishedAt IS NULL`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, raw string
		if rows.Scan(&id, &raw) == nil {
			var p model.UpdatePlan
			if json.Unmarshal([]byte(raw), &p) == nil && p.Coordinator == a.site {
				out = append(out, id)
			}
		}
	}
	return out
}

// ───────────────────────── coordinator

// startUpdate builds the plan and runs the update with this agent as coordinator (§14.5).
func (a *Agent) startUpdate(kind string, rev *model.Revision, p *model.Proposal, targetSnapshot *string, rc *reqCtx) (*model.UpdateRun, *httpError) {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	if why := a.anyVolumeOp(); why != "" { // §21 #96: a volume restore / copy in flight would be corrupted
		return nil, herr(model.ErrConflict, why+"; start the update when it has finished (or stop that action)", nil)
	}
	// §21 #110 never refused: a running update is stopped first (no roll back; the new one converges the sites)
	superseded := a.supersedeRunning(a.ctx, "a new "+kind+" by "+rc.actor, rc.actorPtr())
	headCfg, _ := a.headConfig()
	var from *string
	if h := a.head(); h != nil {
		f := h.RevID
		from = &f
	}
	enrolled := a.enrolledSites()
	sites := []string{}
	for _, s := range union(siteSlugs(headCfg), siteSlugs(&rev.Config)) {
		if contains(enrolled, s) {
			sites = append(sites, s)
		}
	}
	var removed []string
	for _, s := range siteSlugs(headCfg) {
		if siteOf(&rev.Config, s) == nil && contains(sites, s) {
			removed = append(removed, s)
		}
	}
	sort.Strings(removed)
	order := append([]string{}, removed...)
	so, cyc := validate.SiteOrder(&rev.Config)
	if cyc != nil {
		return nil, invalid("site depends form a cycle", nil)
	}
	for _, s := range so {
		if contains(sites, s) {
			order = append(order, s)
		}
	}
	now := nowTs()
	plan := &model.UpdatePlan{UpdateID: newID(), Kind: kind, Revision: *rev, FromRevID: from, PreSnapshotID: newID(), TargetSnapshotID: targetSnapshot,
		Sites: sites, Order: order, Coordinator: a.site, ApprovedBy: rc.actor, ApprovedAt: now}
	run := &model.UpdateRun{UpdateID: plan.UpdateID, Kind: kind, RevID: rev.RevID, Coordinator: a.site, ApprovedBy: rc.actor, ApprovedAt: now,
		Phase: model.StepLock, Sites: map[string]model.UpdateSiteState{}, StartedAt: now, FastForward: rc.ff}
	ffSum := "" // §21 #95
	withFF := func(d map[string]any) map[string]any {
		if rc.ff != nil {
			d["fastForward"] = rc.ff
		}
		return d
	}
	if rc.ff != nil {
		ffSum = " · " + fastForwardLabel(rc.ff)
	}
	for _, s := range sites {
		run.Sites[s] = model.UpdateSiteState{UpdateID: plan.UpdateID, SiteSlug: s, State: "idle", Containers: []model.UpdateContainerState{}}
	}
	a.savePlan(plan)
	a.saveRun(run)
	actor := rc.actorPtr()
	if p != nil {
		p.Status = model.ProposalApproved
		p.ApprovedBy, p.ApprovedAt, p.UpdateID = &rc.actor, &now, &plan.UpdateID
		p.ApprovedFastForward = rc.ff
		p.UpdatedAt = now
		a.putProposal(p)
		a.activity(actor, "agent", "proposal.approved", fmt.Sprintf("seq %d approved by %s%s", p.Seq, rc.actor, ffSum), actorData(rc, withFF(map[string]any{"revId": p.RevID, "approver": rc.actor})))
		a.queueStatus(p.RevID, model.ProposalStatusReport{SiteSlug: a.site, Status: model.ProposalApproved, ApprovedBy: rc.actor, ApprovedAt: now, UpdateID: plan.UpdateID})
	}
	startData := withFF(map[string]any{"updateId": plan.UpdateID, "revId": rev.RevID, "kind": kind, "sites": sites})
	if len(superseded) > 0 {
		startData["superseded"] = superseded
	}
	a.activity(actor, "agent", "update.started", fmt.Sprintf("%s to seq %d started%s", kind, rev.Seq, ffSum), actorData(rc, startData))
	a.log.Info("coordinator", "update started", "updateId", plan.UpdateID, "kind", kind, "revId", rev.RevID, "order", order)
	a.goLoop("coordinate", func(c context.Context) {
		c, done := a.updateCtx(plan.UpdateID, c)
		defer done()
		a.coordinate(c, plan, run, p, false)
	})
	cp := *run
	return &cp, nil
}

const siteStepGrace = time.Minute

func (a *Agent) setPhase(run *model.UpdateRun, phase string) {
	run.Phase = phase
	a.saveRun(run)
}

// refreshPings pings every peer synchronously (gate state right after an update).
func (a *Agent) refreshPings(ctx context.Context) {
	var wg sync.WaitGroup
	for _, p := range a.reachablePeers() {
		wg.Add(1)
		go func(s string) { defer wg.Done(); defer a.log.Recover("mesh", "ping "+s); a.pingOne(ctx, s) }(p)
	}
	wg.Wait()
}

func (a *Agent) finishRun(run *model.UpdateRun, ok bool, msg string, snap *string) {
	if cur, has := a.loadRun(run.UpdateID); has && cur.FinishedAt != nil && run.FinishedAt == nil {
		return // §21 #110 already finished (terminated) by someone else
	}
	if a.wasTerminated(run.UpdateID) && !strings.HasPrefix(msg, "terminated: ") {
		return // §21 #110 a stopped update's loop, woken by the cancel: terminateLocal finishes it with the reason
	}
	a.refreshPings(a.ctx)
	t := nowTs()
	run.FinishedAt = &t
	run.Result = &model.ProposalResult{Ok: ok, Message: msg, SnapshotID: snap}
	if ok {
		run.Phase = "done"
	} else {
		run.Phase = "failed"
	}
	a.saveRun(run)
}

// coordinate drives LOCK → SNAPSHOT → APPLY → VERIFY → COMMIT (or ROLLBACK).
func (a *Agent) coordinate(ctx context.Context, plan *model.UpdatePlan, run *model.UpdateRun, p *model.Proposal, resumeCommit bool) {
	start := time.Now()
	report := func(status string, res *model.ProposalResult) {
		if p == nil || (ctx.Err() != nil && a.wasTerminated(plan.UpdateID)) {
			return // §21 #110 terminated: terminateLocal reported it
		}
		if cur, ok := a.proposal(p.RevID); ok {
			cur.Status = status
			cur.Result = res
			cur.UpdatedAt = nowTs()
			if status == model.ProposalReady {
				cur.ApprovedBy, cur.ApprovedAt, cur.UpdateID = nil, nil, nil
			}
			a.putProposal(cur)
		}
		r := model.ProposalStatusReport{SiteSlug: a.site, Status: status, Result: res, UpdateID: plan.UpdateID}
		a.queueStatus(p.RevID, r)
	}
	parallel := func(step string, sites []string) map[string]error {
		var mu sync.Mutex
		errs := map[string]error{}
		var wg sync.WaitGroup
		for _, s := range sites {
			wg.Add(1)
			go func(s string) {
				defer wg.Done()
				var err error
				func() { // §21 #112 a panic in a step fails the step on that site (the update rolls back), never the agent
					defer func() {
						if r := recover(); r != nil {
							err = fmt.Errorf("step %s on %s panicked: %v", step, s, r)
							a.log.Error("update", "step panic", "site", s, "step", string(step), "panic", fmt.Sprint(r))
						}
					}()
					err = a.runStep(ctx, run, plan, s, step)
				}()
				mu.Lock()
				if err != nil {
					errs[s] = err
				}
				mu.Unlock()
			}(s)
		}
		wg.Wait()
		return errs
	}
	if resumeCommit {
		goto commit
	}
	// 1. LOCK
	a.setPhase(run, model.StepLock)
	if errs := parallel(model.StepLock, plan.Sites); len(errs) > 0 {
		var locked []string
		for _, s := range plan.Sites {
			if errs[s] == nil {
				locked = append(locked, s)
			}
		}
		_ = parallel(model.StepUnlock, locked)
		msg := firstErr("lock", errs, plan.Order)
		a.activity(nil, "system", "update.failed", "update failed at lock: "+msg, map[string]any{"revId": plan.Revision.RevID, "step": "lock", "siteSlug": firstSite(errs, plan.Order), "error": msg})
		a.finishRun(run, false, msg, nil)
		report(model.ProposalReady, &model.ProposalResult{Ok: false, Message: msg})
		a.log.Warn("coordinator", "lock failed", "error", msg)
		return
	}
	report(model.ProposalApplying, nil)
	// 2. SNAPSHOT
	a.setPhase(run, model.StepSnapshot)
	if errs := parallel(model.StepSnapshot, plan.Sites); len(errs) > 0 {
		a.rollbackAll(ctx, plan, run, p, "snapshot", errs, start)
		return
	}
	// 3. APPLY (one by one)
	a.setPhase(run, model.StepApply)
	for _, s := range plan.Order {
		if err := a.runStep(ctx, run, plan, s, model.StepApply); err != nil {
			a.rollbackAll(ctx, plan, run, p, "apply", map[string]error{s: err}, start)
			return
		}
	}
	// 4. VERIFY
	a.setPhase(run, model.StepVerify)
	if errs := parallel(model.StepVerify, plan.Sites); len(errs) > 0 {
		a.rollbackAll(ctx, plan, run, p, "verify", errs, start)
		return
	}
commit:
	if ctx.Err() != nil {
		return // §21 #110 terminated (or the agent stops): never commit / report applied
	}
	// 5. COMMIT
	a.setPhase(run, model.StepCommit)
	if errs := parallel(model.StepCommit, plan.Sites); len(errs) > 0 {
		a.log.Warn("coordinator", "commit did not reach every site (they commit when they see the coordinator)", "errors", fmt.Sprint(errs))
	}
	snap := plan.PreSnapshotID
	a.finishRun(run, true, "applied", &snap)
	report(model.ProposalApplied, &model.ProposalResult{Ok: true, Message: "applied", SnapshotID: &snap})
	a.log.Info("coordinator", "update applied", "updateId", plan.UpdateID, "durationMs", time.Since(start).Milliseconds())
}

func firstSite(errs map[string]error, order []string) string {
	for _, s := range order {
		if errs[s] != nil {
			return s
		}
	}
	for s := range errs {
		return s
	}
	return ""
}

func firstErr(step string, errs map[string]error, order []string) string {
	s := firstSite(errs, order)
	return fmt.Sprintf("%s failed on %s: %v", step, s, errs[s])
}

func (a *Agent) rollbackAll(ctx context.Context, plan *model.UpdatePlan, run *model.UpdateRun, p *model.Proposal, step string, errs map[string]error, start time.Time) {
	if ctx.Err() != nil || a.wasTerminated(plan.UpdateID) {
		return // §21 #110 a person stopped it: no roll back (they chose stop, or stop + roll back)
	}
	msg := firstErr(step, errs, plan.Order)
	a.log.Warn("coordinator", "rolling back", "reason", msg)
	a.setPhase(run, model.StepRollback)
	for i := len(plan.Order) - 1; i >= 0; i-- {
		s := plan.Order[i]
		if err := a.runStep(ctx, run, plan, s, model.StepRollback); err != nil {
			a.log.Error("coordinator", "rollback step failed (the site rolls back itself when it sees the coordinator)", "site", s, "error", err.Error())
		}
	}
	for _, s := range plan.Sites { // sites not in order (should not happen)
		if !contains(plan.Order, s) {
			_ = a.runStep(ctx, run, plan, s, model.StepRollback)
		}
	}
	snap := plan.PreSnapshotID
	a.finishRun(run, false, msg, &snap)
	if p != nil {
		if cur, ok := a.proposal(p.RevID); ok {
			cur.Status = model.ProposalFailedRolledBack
			cur.Result = &model.ProposalResult{Ok: false, Message: msg, SnapshotID: &snap}
			cur.UpdatedAt = nowTs()
			a.putProposal(cur)
		}
		a.queueStatus(p.RevID, model.ProposalStatusReport{SiteSlug: a.site, Status: model.ProposalFailedRolledBack, Result: &model.ProposalResult{Ok: false, Message: msg, SnapshotID: &snap}, UpdateID: plan.UpdateID})
	}
}

// runStep starts one step on a site and waits for its end.
func (a *Agent) runStep(ctx context.Context, run *model.UpdateRun, plan *model.UpdatePlan, site, step string) error {
	if err := ctx.Err(); err != nil {
		return err // §21 #110 terminated
	}
	timeout := stepTimeout[step]
	deadline := time.Now().Add(timeout + 15*time.Second)
	req := model.UpdateStepRequest{Step: step}
	if step == model.StepLock {
		req.Plan = plan
	}
	upd := func(st *model.UpdateSiteState) {
		a.mu.Lock()
		if st != nil {
			run.Sites[site] = *st
		}
		a.mu.Unlock()
		a.saveRun(run)
	}
	a.mu.Lock()
	cur := run.Sites[site] // read under the lock: the other sites' steps write the map concurrently
	a.mu.Unlock()
	s := step
	cur.Step, cur.State, cur.Error = &s, "running", nil
	upd(&cur)
	// start
	var startErr error
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		if site == a.site {
			startErr = a.startSiteStep(plan.Coordinator, plan.UpdateID, req)
			if startErr != nil {
				if he, ok := startErr.(*httpError); ok {
					startErr = fmt.Errorf("%s", he.msg)
				}
			}
		} else {
			startErr = a.peerJSON(ctx, site, "POST", "/_opm/peer/update/"+plan.UpdateID+"/step", req, nil, 15*time.Second)
		}
		if startErr == nil || !isNetErr(startErr) || step == model.StepLock {
			break
		}
		if !sleepCtx(ctx, a.tm.StepPoll) {
			return ctx.Err()
		}
	}
	if startErr != nil {
		cur.State = "failed"
		e := startErr.Error()
		cur.Error = &e
		upd(&cur)
		return startErr
	}
	// wait
	for time.Now().Before(deadline) {
		var st *model.UpdateSiteState
		if site == a.site {
			st = a.siteStateOf(plan.UpdateID)
		} else {
			var x model.UpdateSiteState
			if err := a.peerJSON(ctx, site, "GET", "/_opm/peer/update/"+plan.UpdateID, nil, &x, 10*time.Second); err == nil {
				st = &x
			}
		}
		if st != nil {
			upd(st)
			if st.Step != nil && *st.Step == step && (st.State == "ok" || st.State == "failed") {
				if st.State == "failed" {
					msg := "failed"
					if st.Error != nil {
						msg = *st.Error
					}
					return fmt.Errorf("%s", msg)
				}
				return nil
			}
		}
		if !sleepCtx(ctx, a.tm.StepPoll) {
			return ctx.Err()
		}
	}
	a.mu.Lock()
	cur = run.Sites[site]
	a.mu.Unlock()
	cur.State = "failed"
	e := fmt.Sprintf("%s timed out on %s", step, site)
	cur.Error = &e
	upd(&cur)
	return fmt.Errorf("%s", e)
}

// ───────────────────────── site side

func (a *Agent) handleUpdateStep(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	var req model.UpdateStepRequest
	if e := readJSON(r, &req); e != nil {
		return nil, e
	}
	caller := getRC(r).peer
	if caller == nil {
		return nil, herr(model.ErrUnauthenticated, "peer only", nil)
	}
	if err := a.startSiteStep(caller.Site, r.PathValue("updateId"), req); err != nil {
		return nil, err.(*httpError)
	}
	return model.AcceptedResponse{Accepted: true}, nil
}

func (a *Agent) handleUpdateState(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	id := r.PathValue("updateId")
	caller := getRC(r).peer
	if l := a.lock(); l != nil && l.UpdateID == id && caller != nil && caller.Site == l.Coordinator {
		// atomic: never resurrect a lock that a concurrent commit / rollback just deleted
		_, _ = a.db.Exec(`UPDATE kv SET value = json_set(value, '$.lastContactAt', ?) WHERE key = 'lock' AND json_extract(value, '$.updateId') = ?`, nowTs(), id)
		a.mu.Lock()
		a.coordLast[id] = time.Now()
		a.mu.Unlock()
	}
	if st := a.siteStateOf(id); st != nil {
		return st, nil
	}
	if _, ok := a.loadPlan(id); ok {
		return model.UpdateSiteState{UpdateID: id, SiteSlug: a.site, State: "idle", Containers: []model.UpdateContainerState{}}, nil
	}
	return nil, herr(model.ErrNotFound, "unknown update", nil)
}

// startSiteStep validates and launches a step asynchronously.
func (a *Agent) startSiteStep(caller, updateID string, req model.UpdateStepRequest) error {
	var plan *model.UpdatePlan
	if req.Step == model.StepLock {
		if req.Plan == nil || req.Plan.UpdateID != updateID {
			return herr(model.ErrInvalid, "lock needs the plan", nil)
		}
		if req.Plan.Coordinator != caller {
			return herr(model.ErrUnauthenticated, "the signer is not the plan coordinator", nil)
		}
		if err := verifyRevision(req.Plan.RevisionFor(a.site)); err != nil {
			return herr(model.ErrInvalid, err.Error(), nil)
		}
		plan = req.Plan
		// §21 #104 a 0.14 site never applies a legacy (proposal) update: it takes part as a no-op participant (§9.3)
		if a.branchMode() && plan.Kind != "merge" {
			a.markLegacyParticipant(plan.UpdateID)
		}
	} else {
		p, ok := a.loadPlan(updateID)
		if !ok {
			return herr(model.ErrInvalid, "unknown update (lock first)", nil)
		}
		if p.Coordinator != caller {
			return herr(model.ErrUnauthenticated, "the signer is not the plan coordinator", nil)
		}
		plan = p
	}
	if st := a.siteStateOf(updateID); st != nil && st.State == "running" {
		return herr(model.ErrConflict, "step "+deref(st.Step)+" is still running", nil)
	}
	step := req.Step
	now := nowTs()
	st := &model.UpdateSiteState{UpdateID: updateID, SiteSlug: a.site, Step: &step, State: "running", PreSnapshotID: &plan.PreSnapshotID, StartedAt: &now, Containers: []model.UpdateContainerState{}}
	if prev := a.siteStateOf(updateID); prev != nil {
		st.Containers = prev.Containers
	}
	if a.wasTerminated(updateID) {
		return herr(model.ErrConflict, "update "+updateID+" was terminated on this site", nil)
	}
	if step == model.StepLock {
		// synchronous checks so the coordinator gets locked / conflict at once
		if l := a.lock(); l != nil && l.UpdateID != updateID {
			// §21 #110 never refused: the older update stops here (its coordinator stops it everywhere when it starts)
			a.terminateLocal(l.UpdateID, "superseded by update "+updateID+" of "+caller, nil)
		}
		if !a.isLegacyParticipant(updateID) && !headEq(a.head(), plan.FromFor(a.site)) {
			h := ""
			if hh := a.head(); hh != nil {
				h = hh.RevID
			}
			return herr(model.ErrConflict, "head is "+h+", the plan starts from "+deref(plan.FromFor(a.site)), nil)
		}
		a.savePlan(plan)
	}
	a.setSiteState(st)
	a.mu.Lock()
	a.coordLast[updateID] = time.Now()
	a.mu.Unlock()
	a.goLoop("step-"+step, func(c context.Context) {
		c, done := a.updateCtx(updateID, c)
		defer done()
		defer func() { // §21 #110 a panic fails the step at once (goLoop logs it); never "running" until the timeout
			if r := recover(); r != nil {
				e := fmt.Sprintf("internal error in step %s: %v", step, r)
				fin := nowTs()
				st.State, st.Error, st.FinishedAt = "failed", &e, &fin
				a.setSiteState(st)
				panic(r)
			}
		}()
		if d, ok := stepTimeout[step]; ok { // §21 #110 bounded: a hung Docker call never holds the lock forever
			var cancel context.CancelFunc
			c, cancel = context.WithTimeout(c, d+siteStepGrace)
			defer cancel()
		}
		err := a.execStep(c, plan, step, st)
		if c.Err() != nil && a.wasTerminated(updateID) {
			return // §21 #110 terminated: terminateLocal already marked this site
		}
		fin := nowTs()
		st.FinishedAt = &fin
		if err != nil {
			st.State = "failed"
			e := err.Error()
			st.Error = &e
			if step != model.StepRollback && step != model.StepUnlock && step != model.StepLock {
				data := map[string]any{"revId": plan.RevisionFor(a.site).RevID, "step": step, "siteSlug": a.site, "error": e}
				var se *svcErr
				if errors.As(err, &se) && se.sid != "" {
					data["containerSlug"] = se.sid
				}
				if fs := failureActivityData(st); len(fs) > 0 {
					data["failures"] = fs // §21 #87: exit code, restarts, log tail (capped, redacted) — also in the cloud Activities
				}
				a.activity(nil, "system", "update.failed", fmt.Sprintf("%s failed on %s: %v", step, a.site, err), data)
			}
			a.log.Warn("coordinator", "step failed", "step", step, "updateId", updateID, "error", e)
		} else {
			st.State = "ok"
		}
		a.setSiteState(st)
	})
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *Agent) execStep(ctx context.Context, plan *model.UpdatePlan, step string, st *model.UpdateSiteState) error {
	if a.isLegacyParticipant(plan.UpdateID) {
		return a.legacyParticipantStep(plan, step)
	}
	switch step {
	case model.StepLock:
		now := nowTs()
		l := model.UpdateLock{UpdateID: plan.UpdateID, Kind: plan.Kind, RevID: plan.RevisionFor(a.site).RevID, Coordinator: plan.Coordinator, StartedAt: now, LastContactAt: now}
		if err := a.db.SetKV("lock", l); err != nil {
			return err
		}
		if p, ok := a.proposal(plan.RevisionFor(a.site).RevID); ok && !model.IsTerminal(p.Status) {
			p.Status = model.ProposalApplying
			p.UpdatedAt = now
			a.putProposal(p)
		}
		a.kickLoop("ping")
		return nil
	case model.StepUnlock:
		if l := a.lock(); l != nil && l.UpdateID == plan.UpdateID {
			_ = a.db.DelKV("lock")
		}
		a.finishSiteUpdate(plan.UpdateID)
		return nil
	case model.StepSnapshot:
		return a.preUpdateSnapshot(ctx, plan, st)
	case model.StepApply:
		if err := a.requireLock(plan); err != nil {
			return err
		}
		o := applyOpts{rev: plan.RevisionFor(a.site), state: st}
		if plan.Kind == "merge" {
			a.removeDroppedAdopted(ctx, o.rev) // §21 #104 "remove locally" of hand-made (adopted) containers
		}
		if plan.Kind == "rollback" && plan.TargetSnapshotID != nil {
			o.restoreFrom = *plan.TargetSnapshotID
		}
		return a.applySite(ctx, o)
	case model.StepVerify:
		return a.verifySite(ctx, plan.RevisionFor(a.site), st)
	case model.StepCommit:
		return a.commitSite(plan)
	case model.StepRollback:
		return a.rollbackSite(ctx, plan, st)
	}
	return fmt.Errorf("unknown step %s", step)
}

func (a *Agent) requireLock(plan *model.UpdatePlan) error {
	l := a.lock()
	if l == nil || l.UpdateID != plan.UpdateID {
		return fmt.Errorf("this site is not locked for update %s", plan.UpdateID)
	}
	return nil
}

func (a *Agent) finishSiteUpdate(id string) {
	_, _ = a.db.Exec(`UPDATE updates SET finishedAt = COALESCE(finishedAt, ?) WHERE updateId = ? AND json_extract(plan, '$.coordinator') != ?`, time.Now().Unix(), id, a.site)
}

// commitSite: head = revision, store it, unlock (§14.5 step 5).
func (a *Agent) commitSite(plan *model.UpdatePlan) error {
	rev := *plan.RevisionFor(a.site)
	if h := a.head(); h != nil && h.RevID == rev.RevID {
		if l := a.lock(); l != nil && l.UpdateID == plan.UpdateID {
			_ = a.db.DelKV("lock")
		}
		return nil // already committed (idempotent)
	}
	if err := a.requireLock(plan); err != nil {
		return err
	}
	l := a.lock()
	started := time.Now()
	if l != nil {
		started = parseTs(l.StartedAt)
	}
	if err := a.putRevision(&rev); err != nil {
		return err
	}
	if err := a.db.SetKV("head", model.HeadHash{RevID: rev.RevID, Seq: rev.Seq, Hash: rev.Hash}); err != nil {
		return err
	}
	// §21 #104 the branch commit is written only here (COMMIT): a failed merge leaves no commit
	if meta, ok := plan.Commits[a.site]; ok {
		a.commitBranchAt(plan, &rev, meta)
	}
	_ = a.db.DelKV("lock")
	a.finishSiteUpdate(plan.UpdateID)
	a.activity(nil, "system", "update.applied", fmt.Sprintf("seq %d applied on %s", rev.Seq, a.site), map[string]any{"revId": rev.RevID, "seq": rev.Seq, "durationMs": time.Since(started).Milliseconds()})
	if rev.Origin == "agent" && rev.Kind == "change" {
		var sections []string
		if rev.ParentRevID != nil {
			if pr, ok := a.revision(*rev.ParentRevID); ok {
				sections = changedSectionsOf(&pr.Config, &rev.Config)
			}
		}
		a.activity(nil, "system", "config.local-change", "local change applied", map[string]any{"revId": rev.RevID, "sections": sections})
	}
	if plan.Kind == "rollback" && plan.TargetSnapshotID != nil {
		a.activity(nil, "system", "snapshot.rolled-back", "rolled back to snapshot "+*plan.TargetSnapshotID, map[string]any{"snapshotId": *plan.TargetSnapshotID, "newRevId": rev.RevID})
	}
	if p, ok := a.proposal(rev.RevID); ok {
		p.Status = model.ProposalApplied
		p.UpdatedAt = nowTs()
		a.putProposal(p)
	}
	// other open proposals whose parent is not the new head are stale now
	for _, q := range a.openProposals() {
		if q.ParentRevID == nil || *q.ParentRevID != rev.RevID {
			q.Status = model.ProposalStale
			q.UpdatedAt = nowTs()
			a.putProposal(&q)
			a.stopStaging(q.RevID)
			a.activity(nil, "system", "proposal.stale", fmt.Sprintf("proposal seq %d is stale", q.Seq), map[string]any{"revId": q.RevID, "parentRevId": q.ParentRevID, "headRevId": rev.RevID})
		}
	}
	if !a.branchMode() {
		a.enqueue("head", "head", "POST", "/v1/agent/head", model.ReportHeadRequest{Revision: rev})
	}
	a.kickLoop("ping")
	a.log.Info("coordinator", "committed", "revId", rev.RevID, "seq", rev.Seq)
	go func() { // the next successful update removes the containers kept from failed updates (§21 #87)
		defer func() { _ = recover() }() // §21 #110 best effort: never takes the agent down
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		a.cleanupFailedContainers(ctx, true)
	}()
	return nil
}

func changedSectionsOf(before, after *model.Config) []string {
	var out []string
	if canonical.MustCanonicalize(toAny(before.Variables)) != canonical.MustCanonicalize(toAny(after.Variables)) {
		out = append(out, "variables")
	}
	if canonical.MustCanonicalize(toAny(before.Secrets)) != canonical.MustCanonicalize(toAny(after.Secrets)) {
		out = append(out, "secrets")
	}
	if canonical.MustCanonicalize(toAny(before.Files)) != canonical.MustCanonicalize(toAny(after.Files)) {
		out = append(out, "files")
	}
	if canonical.MustCanonicalize(toAny(before.Sites)) != canonical.MustCanonicalize(toAny(after.Sites)) {
		out = append(out, "sites")
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func toAny(v any) any {
	b, _ := json.Marshal(v)
	var x any
	_ = json.Unmarshal(b, &x)
	return x
}

// rollbackSite restores the pre-update snapshot (previous config, images, volumes), reloads the edge, unlocks.
func (a *Agent) rollbackSite(ctx context.Context, plan *model.UpdatePlan, st *model.UpdateSiteState) error {
	l := a.lock()
	if l == nil || l.UpdateID != plan.UpdateID {
		if h := a.head(); h == nil || h.RevID != plan.RevisionFor(a.site).RevID {
			return nil // nothing locked for this update: already rolled back (idempotent)
		}
		return fmt.Errorf("this site already committed seq %d", plan.RevisionFor(a.site).Seq)
	}
	start := time.Now()
	prev := a.headRevision()
	o := applyOpts{rev: prev, state: st, rollback: true}
	if a.hasSnapshotPart(plan.PreSnapshotID) {
		o.restoreFrom = plan.PreSnapshotID
	}
	err := a.applySite(ctx, o)
	_ = a.db.DelKV("lock")
	a.finishSiteUpdate(plan.UpdateID)
	if p, ok := a.proposal(plan.RevisionFor(a.site).RevID); ok && !model.IsTerminal(p.Status) {
		p.Status = model.ProposalFailedRolledBack
		p.UpdatedAt = nowTs()
		a.putProposal(p)
	}
	a.activity(nil, "system", "update.rolled-back", fmt.Sprintf("rolled back on %s", a.site), map[string]any{"revId": plan.RevisionFor(a.site).RevID, "snapshotId": plan.PreSnapshotID, "durationMs": time.Since(start).Milliseconds()})
	if _, ok := plan.Commits[a.site]; ok {
		a.mergeFailedHere(plan)
	}
	a.kickLoop("ping")
	if err != nil {
		a.log.Error("coordinator", "rollback restore had errors", "error", err.Error())
	}
	return err
}

// verifySite: every enabled container still completed (§14.5 step 4).
func (a *Agent) verifySite(ctx context.Context, rev *model.Revision, st *model.UpdateSiteState) error {
	err := a.verifySiteOnce(ctx, rev)
	var se *svcErr
	if err != nil && errors.As(err, &se) && se.sid != "" {
		var b *driver.Built
		id := ""
		if site := siteOf(revConfig(rev), a.site); site != nil {
			if builts, e := a.buildSite(rev, site); e == nil {
				for _, x := range builts {
					if x.ContainerSlug == se.sid {
						b = x
					}
				}
			}
		}
		if ex, e := a.drv.Existing(ctx); e == nil {
			if c, ok := ex[se.sid]; ok {
				id = c.ID
			}
		}
		a.setContainerFailure(st, a.captureFailure(se.sid, b, id, err))
	}
	return err
}

func (a *Agent) verifySiteOnce(ctx context.Context, rev *model.Revision) error {
	site := siteOf(revConfig(rev), a.site)
	if site == nil {
		return nil
	}
	builts, err := a.buildSite(rev, site)
	if err != nil {
		return err
	}
	ex, err := a.drv.Existing(ctx)
	if err != nil {
		return err
	}
	for _, b := range builts {
		if !b.Enabled {
			continue
		}
		e, ok := ex[b.ContainerSlug]
		if !ok {
			return withService(fmt.Errorf("%s: container missing", b.Slug), b.ContainerSlug)
		}
		cj, err := a.dc.ContainerInspect(ctx, e.ID)
		if err != nil {
			return withService(fmt.Errorf("%s: %w", b.Slug, err), b.ContainerSlug)
		}
		if b.Kind == "job" {
			if cj.State.Running {
				continue
			}
			if cj.State.ExitCode != 0 {
				return withService(fmt.Errorf("%s: job exited with code %d", b.Slug, cj.State.ExitCode), b.ContainerSlug)
			}
			continue
		}
		if cj.State.Restarting {
			return withService(fmt.Errorf("%s: restarting (last exit code %d, %d restarts)", b.Slug, cj.State.ExitCode, cj.RestartCount), b.ContainerSlug)
		}
		if !cj.State.Running {
			return withService(fmt.Errorf("%s: not running (exit code %d)", b.Slug, cj.State.ExitCode), b.ContainerSlug)
		}
		if b.Health != nil && b.Health.Type != "none" && b.Health.Type != "" {
			loop, _, _ := a.drv.LoopPorts(ctx, e.ID, b)
			if err := a.drv.CheckOnce(ctx, b, e.ID, loop, time.Now().Add(-time.Hour)); err != nil {
				// one retry after an interval (a single slow answer is not a crash loop)
				time.Sleep(driver.Secs(b.Health.IntervalSec))
				loop, _, _ = a.drv.LoopPorts(ctx, e.ID, b) // fresh: a restart changes the loopback port
				if err2 := a.drv.CheckOnce(ctx, b, e.ID, loop, time.Now().Add(-time.Hour)); err2 != nil {
					return withService(fmt.Errorf("%s: health check failed: %v", b.Slug, err2), b.ContainerSlug)
				}
			}
		}
	}
	return nil
}

// ───────────────────────── recovery + coordinator following

// recoverOnStart: running site steps are failed; unfinished coordinated runs are rolled back (or their commit finished).
func (a *Agent) recoverOnStart() {
	rows, err := a.db.Query(`SELECT updateId, plan, run, site FROM updates WHERE finishedAt IS NULL`)
	if err != nil {
		return
	}
	type rec struct {
		plan model.UpdatePlan
		run  *model.UpdateRun
		site *model.UpdateSiteState
	}
	var recs []rec
	for rows.Next() {
		var id, praw string
		var rraw, sraw sql.NullString
		if rows.Scan(&id, &praw, &rraw, &sraw) != nil {
			continue
		}
		var x rec
		if json.Unmarshal([]byte(praw), &x.plan) != nil {
			continue
		}
		if rraw.Valid {
			var r model.UpdateRun
			if json.Unmarshal([]byte(rraw.String), &r) == nil {
				x.run = &r
			}
		}
		if sraw.Valid {
			var s model.UpdateSiteState
			if json.Unmarshal([]byte(sraw.String), &s) == nil {
				x.site = &s
			}
		}
		recs = append(recs, x)
	}
	rows.Close()
	for _, x := range recs {
		if x.site != nil && x.site.State == "running" {
			x.site.State = "failed"
			e := "agent restarted during " + deref(x.site.Step)
			x.site.Error = &e
			a.setSiteState(x.site)
		} else if x.site != nil {
			a.setSiteState(x.site)
		}
		if x.plan.Coordinator == a.site && x.run != nil && x.run.FinishedAt == nil {
			plan, run := x.plan, x.run
			var p *model.Proposal
			if pp, ok := a.proposal(plan.Revision.RevID); ok {
				p = pp
			}
			a.log.Warn("coordinator", "coordinator restarted mid-update", "updateId", plan.UpdateID, "phase", run.Phase)
			if run.Phase == model.StepCommit {
				a.goLoop("coordinate", func(c context.Context) {
					c, done := a.updateCtx(plan.UpdateID, c)
					defer done()
					a.coordinate(c, &plan, run, p, true)
				})
			} else if run.Phase == model.StepLock {
				a.goLoop("coordinate", func(c context.Context) {
					c, done := a.updateCtx(plan.UpdateID, c)
					defer done()
					for _, s := range plan.Sites {
						_ = a.runStep(c, run, &plan, s, model.StepUnlock)
					}
					a.finishRun(run, false, "coordinator restarted during lock", nil)
					if p != nil {
						a.queueStatus(p.RevID, model.ProposalStatusReport{SiteSlug: a.site, Status: model.ProposalReady, Result: &model.ProposalResult{Ok: false, Message: "coordinator restarted during lock"}})
					}
				})
			} else {
				a.goLoop("coordinate", func(c context.Context) {
					c, done := a.updateCtx(plan.UpdateID, c)
					defer done()
					a.rollbackAll(c, &plan, run, p, run.Phase, map[string]error{a.site: fmt.Errorf("coordinator restarted")}, time.Now())
				})
			}
		}
	}
}

// followCoordinator: a participant whose coordinator no longer runs the update commits (coordinator head = the
// revision) or rolls itself back (§14.5 last paragraph).
func (a *Agent) followCoordinator(slug string, pw *pingWire) {
	l := a.lock()
	if l == nil {
		return
	}
	if contains(pw.Terminated, l.UpdateID) { // §21 #110 a person stopped it on that site: stop it here too (no roll back)
		a.terminateLocal(l.UpdateID, "stopped on "+slug, nil)
		return
	}
	if l.Coordinator != slug || l.Coordinator == a.site {
		return
	}
	if a.clock().Sub(parseTs(l.StartedAt)) < 30*time.Second {
		return
	}
	for _, id := range pw.ActiveUpdates {
		if id == l.UpdateID {
			return
		}
	}
	if st := a.siteStateOf(l.UpdateID); st != nil && st.State == "running" {
		return
	}
	plan, ok := a.loadPlan(l.UpdateID)
	if !ok {
		return
	}
	if pw.Head != nil && pw.Head.RevID == plan.RevisionFor(slug).RevID || (len(plan.Targets) > 0 && pw.Branch != nil && pw.Branch.Head != nil && pw.Branch.Head.CommitID == plan.RevisionFor(slug).RevID) {
		a.log.Warn("coordinator", "coordinator committed without us; committing", "updateId", plan.UpdateID)
		_ = a.commitSite(plan)
		return
	}
	a.log.Warn("coordinator", "coordinator dropped the update; rolling back this site", "updateId", plan.UpdateID)
	a.goLoop("abandon", func(c context.Context) {
		st := &model.UpdateSiteState{UpdateID: plan.UpdateID, SiteSlug: a.site, State: "running", Containers: []model.UpdateContainerState{}}
		s := model.StepRollback
		st.Step = &s
		a.setSiteState(st)
		c, cancel := context.WithTimeout(c, stepTimeout[model.StepRollback]+siteStepGrace) // §21 #110 bounded
		defer cancel()
		err := a.rollbackSite(c, plan, st)
		st.State = "ok"
		if err != nil {
			st.State = "failed"
		}
		a.setSiteState(st)
		a.activity(nil, "system", "update.abandoned", "update abandoned on "+a.site, map[string]any{"updateId": plan.UpdateID, "revId": plan.Revision.RevID})
	})
}

// ───────────────────────── API: updates

func (a *Agent) handleCurrentUpdate(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	// own coordinated runs
	var best *model.UpdateRun
	rows, err := a.db.Query(`SELECT updateId FROM updates ORDER BY updateId DESC LIMIT 20`)
	var ids []string
	if err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	for _, id := range ids {
		run, ok := a.loadRun(id)
		if !ok {
			continue
		}
		if run.FinishedAt == nil {
			return model.CurrentUpdateResponse{Run: run}, 200, nil
		}
		if best == nil && time.Since(parseTs(*run.FinishedAt)) < 10*time.Minute {
			best = run
		}
	}
	// another coordinator's run: build it from the plan + site states
	if l := a.lock(); l != nil && l.Coordinator != a.site {
		if run := a.remoteRun(r.Context(), l.UpdateID); run != nil {
			return model.CurrentUpdateResponse{Run: run}, 200, nil
		}
	}
	for _, s := range a.reachablePeers() {
		if pw := a.lastPing(s); pw != nil && pw.Lock != nil {
			if run := a.remoteRun(r.Context(), pw.Lock.UpdateID); run != nil {
				return model.CurrentUpdateResponse{Run: run}, 200, nil
			}
		}
	}
	return model.CurrentUpdateResponse{Run: best}, 200, nil
}

func (a *Agent) remoteRun(ctx context.Context, id string) *model.UpdateRun {
	plan, ok := a.loadPlan(id)
	if !ok {
		return nil
	}
	run := &model.UpdateRun{UpdateID: id, Kind: plan.Kind, RevID: plan.Revision.RevID, Coordinator: plan.Coordinator, ApprovedBy: plan.ApprovedBy,
		ApprovedAt: plan.ApprovedAt, Phase: "running", Sites: map[string]model.UpdateSiteState{}, StartedAt: plan.ApprovedAt}
	for _, s := range plan.Sites {
		if s == a.site {
			if st := a.siteStateOf(id); st != nil {
				run.Sites[s] = *st
				if st.Step != nil {
					run.Phase = *st.Step
				}
			}
			continue
		}
		var st model.UpdateSiteState
		if a.reachable(s) && a.peerJSON(ctx, s, "GET", "/_opm/peer/update/"+id, nil, &st, 5*time.Second) == nil {
			run.Sites[s] = st
		}
	}
	return run
}

func (a *Agent) handleGetUpdate(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("updateId")
	if run, ok := a.loadRun(id); ok {
		return run, 200, nil
	}
	if run := a.remoteRun(r.Context(), id); run != nil {
		return run, 200, nil
	}
	return nil, 0, herr(model.ErrNotFound, "unknown update", nil)
}

func (a *Agent) handleAbandon(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	id := r.PathValue("updateId")
	l := a.lock()
	if l == nil || l.UpdateID != id {
		return nil, 0, herr(model.ErrNotFound, "this site holds no lock for that update", nil)
	}
	if l.Coordinator == a.site {
		return nil, 0, herr(model.ErrConflict, "this site is the coordinator", nil)
	}
	last := parseTs(l.LastContactAt)
	a.mu.Lock()
	if t, ok := a.coordLast[id]; ok && t.After(last) {
		last = t
	}
	a.mu.Unlock()
	if a.clock().Sub(last) < a.tm.AbandonAfter || a.reachable(l.Coordinator) {
		return nil, 0, herr(model.ErrConflict, "the coordinator was in contact less than 10 min ago", map[string]any{"lastContactAt": tsOf(last)})
	}
	plan, ok := a.loadPlan(id)
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "unknown update", nil)
	}
	st := &model.UpdateSiteState{UpdateID: id, SiteSlug: a.site, State: "running", Containers: []model.UpdateContainerState{}}
	s := model.StepRollback
	st.Step = &s
	a.setSiteState(st)
	err := a.rollbackSite(r.Context(), plan, st)
	st.State = "ok"
	if err != nil {
		st.State = "failed"
	}
	a.setSiteState(st)
	a.activity(rc.actorPtr(), "agent", "update.abandoned", "update abandoned on "+a.site, actorData(rc, map[string]any{"updateId": id, "revId": plan.Revision.RevID}))
	return model.OkResponse{Ok: true}, 200, nil
}
