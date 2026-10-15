package agent

// §21 #105 J13 start-up migration (0.14.0 → 0.14.1): the 0.14.0 agent kept saved GUI edits on the agent (kv
// workingCopy + working_items). 0.14.1 keeps no working copy, so — once — those edits are either AUTO-COMMITTED
// through the apply engine (author system, message legacyWorkingMessage, data.autoCommitted) when the site is idle
// with a head, no blocking drift and the edits are valid; or DROPPED (any condition fails, or the apply fails and is
// rolled back): written to <dataDir>/backup/working-copy-pre-0.14.1.json (0600; it may hold plain secret values, like
// the agent database) + activity working.discarded { reason }. kv workingCopy / working_items are cleared exactly once
// (before the apply starts, or at the drop); kv workingMigration records the progress so a restart mid-way resumes.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"opm/agent/internal/model"
)

const legacyWorkingMessage = "Uncommitted changes from before 0.14.1 (auto-committed)"
const legacyWorkingBackup = "working-copy-pre-0.14.1.json"

// tests shorten these
var (
	legacyWorkingWait = 10 * time.Minute
	legacyWorkingPoll = 5 * time.Second
)

type legacyMigration struct {
	State    string       `json:"state"` // applying | done
	CommitID string       `json:"commitId,omitempty"`
	UpdateID string       `json:"updateId,omitempty"`
	Copy     *workingCopy `json:"copy,omitempty"`
	Result   string       `json:"result,omitempty"` // committed | dropped | empty
	Reason   string       `json:"reason,omitempty"`
	At       string       `json:"at"`
}

func (a *Agent) legacyMigrationState() (*legacyMigration, bool) {
	var m legacyMigration
	ok, _ := a.db.GetKV("workingMigration", &m)
	if !ok {
		return nil, false
	}
	return &m, true
}

// startLegacyWorkingMigration is called at start (Run); a no-op without saved edits.
func (a *Agent) startLegacyWorkingMigration() {
	m, ok := a.legacyMigrationState()
	if ok && m.State == "done" {
		if a.legacyWorking() != nil { // edits saved again later (pre-0.14 local proposals entering branch mode)
			_ = a.db.DelKV("workingMigration")
		} else {
			return
		}
	}
	if !ok && a.legacyWorking() == nil {
		return
	}
	if !a.legacyMigrating.CompareAndSwap(false, true) {
		return
	}
	a.goLoop("working-migrate", func(c context.Context) {
		defer a.legacyMigrating.Store(false)
		a.migrateLegacyWorking(c)
	})
}

func (a *Agent) migrateLegacyWorking(ctx context.Context) {
	sleep := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(legacyWorkingPoll):
			return true
		}
	}
	deadline := time.Now().Add(legacyWorkingWait)
	if m, ok := a.legacyMigrationState(); ok && m.State == "applying" {
		// restarted while the auto-commit ran: committed → done; the run ended without a commit → drop
		for {
			if _, ok := a.commitByID(m.CommitID); ok {
				a.legacyWorkingDone(m.CommitID)
				return
			}
			if r, ok := a.loadRun(m.UpdateID); !ok || r.FinishedAt != nil {
				a.legacyWorkingFailed("the auto-commit did not finish (agent restarted)")
				return
			}
			if time.Now().After(deadline) || !sleep() {
				return
			}
		}
	}
	w := a.legacyWorking()
	if w == nil {
		return
	}
	reason := ""
	for {
		h := a.head()
		ds := a.lastDrift()
		switch {
		case !a.branchMode():
			reason = "the cloud does not serve branches"
		case h == nil:
			reason = "this site has no head"
		case a.lock() != nil:
			reason = "an update is running"
		case ds == nil || ds.HeadCommitID == nil || *ds.HeadCommitID != h.RevID:
			reason = "waiting for the first drift scan"
			a.kickLoop("drift")
		default:
			reason = ""
		}
		if reason == "" {
			break
		}
		if time.Now().After(deadline) {
			a.dropLegacyWorking(w, "not auto-committed: "+reason)
			return
		}
		if !sleep() {
			return
		}
	}
	hr := a.headRevision()
	if w.BaseCommitID == nil || *w.BaseCommitID != hr.RevID {
		a.dropLegacyWorking(w, "not auto-committed: the head moved since the edits were saved")
		return
	}
	if u := a.uncommittedNow(); u.blocking() > 0 {
		a.dropLegacyWorking(w, "not auto-committed: changes made outside the agent are uncommitted")
		return
	}
	rev, data, he := a.editRevision(hr, configToInput(w.Config), "", legacyWorkingMessage)
	if he != nil {
		if he.code == model.ErrInvalid && len(w.Keys) > 0 && isNoChange(he) {
			a.clearLegacyWorking()
			a.setLegacyMigration(&legacyMigration{State: "done", Result: "empty", Reason: "the edits equal the head"})
			return
		}
		a.dropLegacyWorking(w, "not auto-committed: "+he.msg)
		return
	}
	data.AutoCommitted = &model.AutoCommitted{From: "agent-working-copy", Items: len(w.Keys)}
	if e := a.stageRevisionNow(ctx, rev); e != nil {
		a.dropLegacyWorking(w, "not auto-committed: "+e.msg)
		return
	}
	meta := a.commitMetaFor(rev, "change", legacyWorkingMessage, "", nil, data)
	// clear once BEFORE the apply; the copy rides in kv workingMigration until the outcome is known
	a.clearLegacyWorking()
	run, he := a.startBranchApply(map[string]model.UpdatePlanTarget{a.site: {Revision: *rev, FromRevID: rev.ParentRevID}}, map[string]model.CommitMeta{a.site: meta}, &mergeRun{LegacyWorking: true}, "")
	if he != nil {
		a.dropLegacyWorking(w, "not auto-committed: "+he.msg)
		return
	}
	a.setLegacyMigration(&legacyMigration{State: "applying", CommitID: rev.RevID, UpdateID: run.UpdateID, Copy: w})
	a.log.Info("branch", "0.14.0 saved edits are being auto-committed", "updateId", run.UpdateID, "items", len(w.Keys))
}

func isNoChange(he *httpError) bool {
	if he == nil || he.details == nil {
		return false
	}
	if l, ok := he.details["errors"].([]model.ValidationIssue); ok {
		for _, i := range l {
			if i.Code == "V_NO_CHANGE" {
				return true
			}
		}
	}
	return false
}

func (a *Agent) setLegacyMigration(m *legacyMigration) {
	m.At = nowTs()
	_ = a.db.SetKV("workingMigration", m)
}

// legacyWorkingDone: the auto-commit landed (COMMIT step).
func (a *Agent) legacyWorkingDone(commitID string) {
	a.setLegacyMigration(&legacyMigration{State: "done", Result: "committed", CommitID: commitID})
	a.log.Info("branch", "0.14.0 saved edits auto-committed", "commitId", commitID)
}

// legacyWorkingFailed: the auto-commit apply failed (rolled back) → drop the copy to the backup file.
func (a *Agent) legacyWorkingFailed(reason string) {
	m, ok := a.legacyMigrationState()
	if !ok || m.State != "applying" || m.Copy == nil {
		return
	}
	a.dropLegacyWorking(m.Copy, "auto-commit failed: "+reason)
}

// dropLegacyWorking writes the edits to the backup file, logs working.discarded and clears the working copy.
func (a *Agent) dropLegacyWorking(w *workingCopy, reason string) {
	path := filepath.Join(a.dataDir, "backup", legacyWorkingBackup)
	file := ""
	if b, err := json.MarshalIndent(w, "", " "); err == nil {
		if os.MkdirAll(filepath.Dir(path), 0o700) == nil && os.WriteFile(path, b, 0o600) == nil {
			_ = os.Chmod(path, 0o600)
			file = path
		}
	}
	a.clearLegacyWorking()
	a.setLegacyMigration(&legacyMigration{State: "done", Result: "dropped", Reason: reason})
	a.activity(nil, "system", "working.discarded", trim200("saved edits from agent 0.14.0 dropped — "+reason), map[string]any{"reason": reason, "keys": firstN(w.Keys, 50), "backupFile": file, "migration": "0.14.1"})
	a.log.Warn("branch", "0.14.0 saved edits dropped", "reason", reason, "backup", file)
}

// configToInput: a plain Config as ConfigInput (every secret value set explicitly: the saved copy held plain values).
func configToInput(c model.Config) model.ConfigInput {
	in := model.ConfigInput{Variables: c.Variables, Files: c.Files, Sites: c.Sites, Secrets: []model.SecretDefInput{}}
	for _, s := range c.Secrets {
		si := model.SecretDefInput{SecretSlug: s.SecretSlug, Label: s.Label, Description: s.Description, Required: s.Required, Generate: s.Generate}
		if s.Value == nil {
			si.PlainValue = model.Null[string]()
		} else {
			si.PlainValue = model.Some(*s.Value)
		}
		in.Secrets = append(in.Secrets, si)
	}
	return in
}
