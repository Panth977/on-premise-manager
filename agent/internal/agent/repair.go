package agent

// §21 #110 offline repair (`opm-agent repair`, also run by the install script on every reinstall / upgrade while the
// agent service is stopped): whatever state the agent was left in, it starts clean.
//
//  1. database: PRAGMA integrity_check; a corrupt database is reported (the install script restores a backup with
//     OPM_RESTORE_DB_BACKUP=1);
//  2. every update this site still holds (its lock, its unfinished coordinated runs, a step marked running, a pending
//     recovery) is TERMINATED here — nothing is rolled back; the other sites stop it too when this agent pings them
//     (pingWire.Terminated);
//  3. with Rollback: this site converges back to its head config with the volumes of its last backup — the pre-update
//     snapshot of the stuck update, else the newest snapshot of this site taken at the head (the same revision, so
//     config and volumes match). An older snapshot is a real roll back (new commit): Backups → Roll back in the GUI.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"opm/agent/internal/model"
)

// RepairOptions: Rollback = restore the last backup; SnapshotID = that snapshot instead (must be at the head).
type RepairOptions struct {
	Rollback   bool
	SnapshotID string
	CheckOnly  bool
	Reason     string
}

// RepairReport is what `opm-agent repair` prints (--json) and the install script reads.
type RepairReport struct {
	DatabaseOk      bool     `json:"databaseOk"`
	DatabaseProblem string   `json:"databaseProblem,omitempty"`
	Stuck           []string `json:"stuck"`      // updates this site still held
	Terminated      []string `json:"terminated"` // stopped by this repair
	Head            *int     `json:"headSeq"`
	Backup          string   `json:"backup,omitempty"` // the snapshot restored (or that would be: CheckOnly)
	BackupAt        string   `json:"backupAt,omitempty"`
	RolledBack      bool     `json:"rolledBack"`
	Notes           []string `json:"notes"`
}

// Repair runs the offline repair. The agent service must not run at the same time (the install script stops it;
// `opm-agent repair` stops and starts it).
func (a *Agent) Repair(opt RepairOptions) (*RepairReport, error) {
	// always a fresh context: the agent is not running (its own context may be missing or already cancelled)
	prevCtx, prevCancel := a.ctx, a.cancel
	a.ctx, a.cancel = context.WithCancel(context.Background())
	defer func() { a.cancel(); a.ctx, a.cancel = prevCtx, prevCancel }()
	rep := &RepairReport{Stuck: []string{}, Terminated: []string{}, Notes: []string{}}
	if h := a.head(); h != nil {
		s := h.Seq
		rep.Head = &s
	}
	// 1. database
	var res string
	if err := a.db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil {
		rep.DatabaseProblem = err.Error()
	} else if res != "ok" {
		rep.DatabaseProblem = res
	}
	rep.DatabaseOk = rep.DatabaseProblem == ""
	if !rep.DatabaseOk {
		rep.Notes = append(rep.Notes, "the agent database is damaged: rerun the install command with OPM_RESTORE_DB_BACKUP=1 to restore its newest backup")
		return rep, nil
	}
	// 2. stuck updates
	ids := map[string]bool{}
	if l := a.lock(); l != nil {
		ids[l.UpdateID] = true
	}
	type row struct {
		id string
		st *string
	}
	var open []row
	rows, err := a.db.Query(`SELECT updateId, json_extract(site, '$.state') FROM updates WHERE finishedAt IS NULL`)
	if err == nil {
		for rows.Next() {
			var x row
			if rows.Scan(&x.id, &x.st) == nil {
				open = append(open, x)
			}
		}
		rows.Close() // one connection: no other query while the rows are open
	}
	for _, x := range open {
		if run, ok := a.loadRun(x.id); (ok && run.FinishedAt == nil && run.Coordinator == a.site) || (x.st != nil && *x.st == "running") {
			ids[x.id] = true
		}
	}
	var pr pendingRecovery
	if ok, _ := a.db.GetKV("recover", &pr); ok && pr.UpdateID != "" {
		rep.Notes = append(rep.Notes, "a roll back requested before the last restart had not run yet")
		if !opt.CheckOnly {
			_ = a.db.DelKV("recover")
			opt.Rollback = true
			if pr.UpdateID != "" {
				ids[pr.UpdateID] = true
			}
		}
	}
	for id := range ids {
		rep.Stuck = append(rep.Stuck, id)
	}
	sort.Strings(rep.Stuck)
	// the backup to restore
	backup, at, note := a.lastBackup(rep.Stuck, opt.SnapshotID)
	rep.Backup, rep.BackupAt = backup, at
	if note != "" {
		rep.Notes = append(rep.Notes, note)
	}
	if opt.CheckOnly {
		return rep, nil
	}
	reason := opt.Reason
	if reason == "" {
		reason = "repaired from the terminal (opm-agent repair)"
	}
	for _, id := range rep.Stuck {
		a.terminateLocal(id, reason, nil)
		rep.Terminated = append(rep.Terminated, id)
	}
	if l := a.lock(); l != nil { // a lock without its update row
		_ = a.db.DelKV("lock")
		rep.Notes = append(rep.Notes, "removed the lock of update "+l.UpdateID)
	}
	// 3. roll back to the last backup
	if !opt.Rollback {
		return rep, nil
	}
	prev := a.headRevision()
	if prev == nil {
		rep.Notes = append(rep.Notes, "this site has no head config: nothing to roll back to")
		return rep, nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Hour)
	defer cancel()
	if _, err := a.dc.Negotiate(ctx); err != nil {
		return rep, fmt.Errorf("docker: %w", err)
	}
	st := &model.UpdateSiteState{UpdateID: "repair", SiteSlug: a.site, State: "running", Containers: []model.UpdateContainerState{}}
	o := applyOpts{rev: prev, state: st, rollback: true, restoreFrom: backup}
	start := time.Now()
	err = a.applySite(ctx, o)
	rep.RolledBack = true
	summary := fmt.Sprintf("rolled back on %s to its head config (seq %d)", a.site, prev.Seq)
	if backup != "" {
		summary += " with the volumes of the backup " + backup
	}
	data := map[string]any{"siteSlug": a.site, "snapshotId": backup, "reason": reason, "durationMs": time.Since(start).Milliseconds(), "via": "cli"}
	if err != nil {
		summary += " — with errors: " + err.Error()
		data["error"] = err.Error()
		rep.Notes = append(rep.Notes, "the roll back had errors: "+err.Error())
	}
	a.activity(nil, "agent", "update.recovered", trim200(summary), data)
	return rep, nil
}

// lastBackup: the snapshot whose volumes match this site's head: the pre-update snapshot of a stuck update, else the
// newest snapshot of this site taken at the head revision. want = an explicit id (must be such a snapshot).
func (a *Agent) lastBackup(stuck []string, want string) (id, at, note string) {
	h := a.head()
	atHead := func(s model.SnapshotSummary) bool {
		return h != nil && s.RevID != nil && *s.RevID == h.RevID
	}
	snaps := a.ownSnapshotSummaries()
	byID := map[string]model.SnapshotSummary{}
	for _, s := range snaps {
		byID[s.SnapshotID] = s
	}
	if want != "" {
		s, ok := byID[want]
		switch {
		case !ok || !a.hasSnapshotPart(want):
			return "", "", "snapshot " + want + " has no part on this site"
		case !atHead(s):
			return "", "", "snapshot " + want + " was taken at another config (seq " + fmt.Sprint(s.Seq) + "): roll back to it from the GUI (Backups → Roll back)"
		}
		return want, s.CreatedAt, ""
	}
	for i := len(stuck) - 1; i >= 0; i-- {
		if p, ok := a.loadPlan(stuck[i]); ok && a.hasSnapshotPart(p.PreSnapshotID) {
			return p.PreSnapshotID, byID[p.PreSnapshotID].CreatedAt, ""
		}
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].CreatedAt > snaps[j].CreatedAt })
	for _, s := range snaps {
		if atHead(s) && a.hasSnapshotPart(s.SnapshotID) {
			return s.SnapshotID, s.CreatedAt, ""
		}
	}
	return "", "", "no backup of this site matches its head config: a roll back re-applies the head config only (volumes kept)"
}

// Close releases the database (the CLI commands that open the agent without running it).
func (a *Agent) Close() {
	if a.db != nil {
		_ = a.db.Close()
	}
}
