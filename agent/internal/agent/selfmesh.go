package agent

// Mesh "Upgrade all" (SPEC-final §21 #90 e): the coordinating site upgrades every reachable site below the pin, ONE at
// a time, in siteSlug order with itself LAST (it restarts), by a forwarded POST sites/{s}/agent/upgrade + polling GET
// sites/{s}/agent/upgrade until done / failed / rolled-back (10 min); the first failure stops the run; agents below
// 0.10.0 / unreachable sites are skipped. State: <agentHome>/upgrade/run.json, finished by the coordinator after its own
// restart (finishMeshRunOnStart).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/selfupgrade"
)

// runFile is upgrade/run.json.
type upgradeRunFile struct {
	Run     model.AgentUpgradeRun `json:"run"`
	Cancel  bool                  `json:"cancel"`
	Actor   string                `json:"actor"`
	Session string                `json:"session"`
}

// meshPollEvery / meshSiteTimeout are the run's polling period and per-site limit (tests shorten them).
var (
	meshPollEvery   = 2 * time.Second
	meshSiteTimeout = 10 * time.Minute
)

func (a *Agent) loadUpgradeRun() *upgradeRunFile {
	var rf upgradeRunFile
	if ok, _ := selfupgrade.ReadJSON(a.self.paths.Run(), &rf); !ok || rf.Run.RunID == "" {
		return nil
	}
	return &rf
}

func (a *Agent) saveUpgradeRun(rf *upgradeRunFile) {
	if err := selfupgrade.WriteJSON(a.self.paths.Run(), rf); err != nil {
		a.log.Error("gui", "run.json write failed", "error", err.Error())
	}
}

func (a *Agent) handleUpgradeAllState(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	a.self.runMu.Lock()
	defer a.self.runMu.Unlock()
	if rf := a.loadUpgradeRun(); rf != nil {
		return model.AgentUpgradeRunState{Run: &rf.Run}, 200, nil
	}
	return model.AgentUpgradeRunState{}, 200, nil
}

func (a *Agent) handleStartUpgradeAll(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.StartUpgradeAllRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if req.Confirm != "UPGRADE" {
		return nil, 0, invalid("type UPGRADE to confirm", []model.ValidationIssue{{Path: "confirm", Code: "V_CONFIRM", Message: "type UPGRADE", Severity: "error"}})
	}
	if !a.enrolled {
		return nil, 0, herr(model.ErrInvalid, "this site is not enrolled", nil)
	}
	// §21 #110 never blocked by a running update (the person decides)
	if p := a.upgradeRunning(); p != nil {
		return nil, 0, herr(model.ErrConflict, "an agent upgrade is running on this site", nil)
	}
	pin := a.pinnedRelease()
	if pin == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		rel, e := a.fetchRelease(ctx)
		cancel()
		if e != nil {
			return nil, 0, e
		}
		pin = &model.AgentReleaseInfo{Version: rel.Version}
	}
	if req.Version != pin.Version {
		return nil, 0, herr(model.ErrStale, fmt.Sprintf("the pinned release is %s now (you asked for %s): reload and try again", pin.Version, req.Version), map[string]any{"pinned": pin.Version})
	}
	a.self.runMu.Lock()
	if rf := a.loadUpgradeRun(); rf != nil && rf.Run.Status == "running" {
		a.self.runMu.Unlock()
		return nil, 0, herr(model.ErrConflict, "an upgrade-all run is running ("+rf.Run.RunID+")", nil)
	}
	now := nowTs()
	self := a.site
	run := model.AgentUpgradeRun{RunID: newID(), Origin: "agent", Coordinator: &self, Version: req.Version, RequestedBy: rc.actor, RequestedAt: now, Status: "running"}
	others := []string{}
	for _, s := range a.allSites() {
		if s != a.site {
			others = append(others, s)
		}
	}
	sort.Strings(others)
	for _, s := range append(others, a.site) {
		row := model.AgentUpgradeRunSite{SiteSlug: s, Status: "pending"}
		from := ""
		if s == a.site {
			from = a.version
		} else if pw := a.lastPing(s); pw != nil {
			from = pw.AgentVersion
		}
		if from != "" {
			f := from
			row.From = &f
		}
		skip := ""
		switch {
		case s != a.site && !a.reachable(s):
			skip = "site " + s + " is not reachable"
		case from == "":
			skip = "the agent version of " + s + " is unknown"
		case !model.SupportsGuiUpgrade(from):
			skip = fmt.Sprintf("agent %s is older than %s: rerun the install command of %s once", from, model.GuiUpgradeMinVersion, s)
		case model.CompareAgentVersions(from, req.Version) >= 0:
			skip = "already on " + from
		}
		if skip != "" {
			row.Status, row.Error, row.At = "skipped", &skip, &now
		}
		run.Sites = append(run.Sites, row)
	}
	rf := &upgradeRunFile{Run: run, Actor: rc.actor, Session: rc.sessionID}
	a.finishRunIfDone(rf)
	a.saveUpgradeRun(rf)
	a.self.runMu.Unlock()
	a.log.Info("gui", "upgrade-all started", "runId", run.RunID, "version", run.Version)
	if rf.Run.Status == "running" {
		a.goLoop("upgrade-all", func(c context.Context) { a.runUpgradeAll(c, run.RunID) })
	}
	return model.AgentUpgradeRunState{Run: &rf.Run}, 202, nil
}

func (a *Agent) handleCancelUpgradeAll(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	a.self.runMu.Lock()
	defer a.self.runMu.Unlock()
	rf := a.loadUpgradeRun()
	if rf == nil || rf.Run.Status != "running" {
		return nil, 0, herr(model.ErrConflict, "no upgrade-all run is running", nil)
	}
	rf.Cancel = true
	now := nowTs()
	for i := range rf.Run.Sites {
		if rf.Run.Sites[i].Status == "pending" {
			rf.Run.Sites[i].Status, rf.Run.Sites[i].At = "cancelled", &now
		}
	}
	a.finishRunIfDone(rf)
	a.saveUpgradeRun(rf)
	return model.AgentUpgradeRunState{Run: &rf.Run}, 200, nil
}

// finishRunIfDone sets the final status when no site is pending / running.
func (a *Agent) finishRunIfDone(rf *upgradeRunFile) {
	if rf.Run.Status != "running" {
		return
	}
	failed, cancelled := false, false
	for _, s := range rf.Run.Sites {
		switch s.Status {
		case "pending", "requested", "running":
			return
		case "failed":
			failed = true
		case "cancelled":
			cancelled = true
		}
	}
	now := nowTs()
	rf.Run.FinishedAt = &now
	switch {
	case failed:
		rf.Run.Status = "failed"
	case cancelled:
		rf.Run.Status = "cancelled"
	default:
		rf.Run.Status = "done"
	}
	a.log.Info("gui", "upgrade-all finished", "runId", rf.Run.RunID, "status", rf.Run.Status)
}

// updateRunSite changes one row under the lock and returns the new run (nil when the run is gone / another run).
func (a *Agent) updateRunSite(runID, site string, fn func(row *model.AgentUpgradeRunSite, rf *upgradeRunFile)) *upgradeRunFile {
	a.self.runMu.Lock()
	defer a.self.runMu.Unlock()
	rf := a.loadUpgradeRun()
	if rf == nil || rf.Run.RunID != runID {
		return nil
	}
	for i := range rf.Run.Sites {
		if rf.Run.Sites[i].SiteSlug == site {
			fn(&rf.Run.Sites[i], rf)
			now := nowTs()
			rf.Run.Sites[i].At = &now
		}
	}
	if fn != nil && site == "" {
		fn(nil, rf)
	}
	a.finishRunIfDone(rf)
	a.saveUpgradeRun(rf)
	return rf
}

// failRun marks a site failed, every pending site cancelled, the run failed.
func (a *Agent) failRunAt(runID, site, msg string) {
	a.updateRunSite(runID, site, func(row *model.AgentUpgradeRunSite, rf *upgradeRunFile) {
		row.Status, row.Error = "failed", &msg
		now := nowTs()
		for i := range rf.Run.Sites {
			if rf.Run.Sites[i].Status == "pending" {
				stop := "stopped: " + site + " failed"
				rf.Run.Sites[i].Status, rf.Run.Sites[i].Error, rf.Run.Sites[i].At = "cancelled", &stop, &now
			}
		}
	})
}

// runUpgradeAll drives the run (one site at a time; this site last).
func (a *Agent) runUpgradeAll(ctx context.Context, runID string) {
	for ctx.Err() == nil {
		a.self.runMu.Lock()
		rf := a.loadUpgradeRun()
		a.self.runMu.Unlock()
		if rf == nil || rf.Run.RunID != runID || rf.Run.Status != "running" {
			return
		}
		var next *model.AgentUpgradeRunSite
		for i := range rf.Run.Sites {
			if st := rf.Run.Sites[i].Status; st == "pending" || st == "running" || st == "requested" {
				next = &rf.Run.Sites[i]
				break
			}
		}
		if next == nil {
			a.updateRunSite(runID, "", nil)
			return
		}
		site := next.SiteSlug
		if rf.Cancel && next.Status == "pending" {
			a.updateRunSite(runID, site, func(row *model.AgentUpgradeRunSite, _ *upgradeRunFile) { row.Status = "cancelled" })
			continue
		}
		actor := rf.Actor
		if site == a.site {
			a.updateRunSite(runID, site, func(row *model.AgentUpgradeRunSite, _ *upgradeRunFile) { row.Status = "running" })
			p, e := a.startUpgrade(ctx, upgradeStart{Version: rf.Run.Version, Via: "mesh", Actor: &actor, RunID: &runID, SessionID: rf.Session})
			if e != nil {
				a.failRunAt(runID, site, e.msg)
				return
			}
			// this process normally restarts; finishMeshRunOnStart completes the row. A failure before the swap does not
			// restart: report it here.
			for ctx.Err() == nil {
				cur := a.upgradeCurrent()
				if cur != nil && cur.UpgradeID == p.UpgradeID && model.UpgradeTerminal(cur.Phase) {
					a.finishSelfRow(runID, cur)
					return
				}
				sleepCtx(ctx, meshPollEvery)
			}
			return
		}
		if !a.reachable(site) {
			a.updateRunSite(runID, site, func(row *model.AgentUpgradeRunSite, _ *upgradeRunFile) {
				m := "site " + site + " is not reachable"
				row.Status, row.Error = "skipped", &m
			})
			continue
		}
		a.updateRunSite(runID, site, func(row *model.AgentUpgradeRunSite, _ *upgradeRunFile) { row.Status = "running" })
		var started model.AgentUpgradeProgress
		st, err := a.peerSiteJSON(ctx, site, "POST", "agent/upgrade", model.StartAgentUpgradeRequest{Version: rf.Run.Version, Confirm: site}, actor, rf.Session, &started)
		if err != nil || st != 202 {
			msg := fmt.Sprintf("start on %s: ", site)
			if err != nil {
				msg += err.Error()
			} else {
				msg += fmt.Sprintf("HTTP %d", st)
			}
			a.failRunAt(runID, site, msg)
			return
		}
		deadline := time.Now().Add(meshSiteTimeout)
		outcome, errMsg := "", ""
		for ctx.Err() == nil && time.Now().Before(deadline) {
			sleepCtx(ctx, meshPollEvery)
			var us model.AgentUpgradeStateResponse
			if st, err := a.peerSiteJSON(ctx, site, "GET", "agent/upgrade", nil, "", "", &us); err != nil || st != 200 || us.Current == nil {
				continue // restarting
			}
			if us.Current.UpgradeID == started.UpgradeID && model.UpgradeTerminal(us.Current.Phase) {
				outcome, errMsg = us.Current.Phase, deref(us.Current.Error)
				break
			}
		}
		if ctx.Err() != nil {
			return
		}
		switch outcome {
		case model.UpgradeDone:
			a.updateRunSite(runID, site, func(row *model.AgentUpgradeRunSite, _ *upgradeRunFile) { row.Status, row.Error = "done", nil })
		case "":
			a.failRunAt(runID, site, fmt.Sprintf("no result from %s within %s", site, meshSiteTimeout))
			return
		default:
			a.failRunAt(runID, site, fmt.Sprintf("%s: %s", outcome, errMsg))
			return
		}
	}
}

func (a *Agent) finishSelfRow(runID string, cur *model.AgentUpgradeProgress) {
	if cur.Phase == model.UpgradeDone {
		a.updateRunSite(runID, a.site, func(row *model.AgentUpgradeRunSite, _ *upgradeRunFile) { row.Status, row.Error = "done", nil })
		return
	}
	a.failRunAt(runID, a.site, fmt.Sprintf("%s: %s", cur.Phase, deref(cur.Error)))
}

// finishMeshRunOnStart: the coordinator came back (after its own upgrade, or a restart mid-run).
func (a *Agent) finishMeshRunOnStart(ctx context.Context) {
	a.self.runMu.Lock()
	rf := a.loadUpgradeRun()
	a.self.runMu.Unlock()
	if rf == nil || rf.Run.Status != "running" {
		return
	}
	for _, s := range rf.Run.Sites {
		if s.SiteSlug == a.site && s.Status == "running" {
			if cur := a.upgradeCurrent(); cur != nil && cur.RunID != nil && *cur.RunID == rf.Run.RunID && model.UpgradeTerminal(cur.Phase) {
				a.finishSelfRow(rf.Run.RunID, cur)
				return
			}
		}
	}
	// a restart in the middle of the run: continue it
	a.goLoop("upgrade-all", func(c context.Context) { a.runUpgradeAll(c, rf.Run.RunID) })
}

// peerSiteJSON calls /_opm/peer/site/<rest> on a peer with the actor (unsigned raw answer, like forwarded GUI calls).
func (a *Agent) peerSiteJSON(ctx context.Context, site, method, rest string, in any, actor, session string, out any) (int, error) {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = b
	}
	hdr := map[string]string{}
	if actor != "" {
		hdr["X-OPM-Actor"] = actor
		hdr["X-OPM-Session"] = session + "@" + a.site
	}
	resp, done, err := a.peerStream(ctx, site, method, "/_opm/peer/site/"+rest, body, hdr, 30*time.Second)
	if err != nil {
		return 0, err
	}
	defer done()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		var eb model.APIErrorBody
		if json.Unmarshal(rb, &eb) == nil && eb.Error.Message != "" {
			return resp.StatusCode, fmt.Errorf("%d %s: %s", resp.StatusCode, eb.Error.Code, eb.Error.Message)
		}
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(rb))
	}
	if out != nil && len(rb) > 0 {
		return resp.StatusCode, json.Unmarshal(rb, out)
	}
	return resp.StatusCode, nil
}
