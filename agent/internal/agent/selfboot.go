package agent

// The upgrade's last phase, run by the agent that just started (SPEC-final §21 #90 b, c): the NEW agent finds
// upgrade/pending.json for its version, finishes its startup (store, listeners, a local GET /_opm/healthz) and writes
// upgrade/healthy + state done + agent.upgraded; the RESTORED old agent finds upgrade/rolled-back.json (watcher / boot
// guard) and writes agent.upgrade-failed + agent.rolled-back with the original actor.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/selfupgrade"
)

func (a *Agent) upgradeOnStart(ctx context.Context) {
	paths := a.self.paths
	var rb selfupgrade.RolledBack
	if ok, _ := selfupgrade.ReadJSON(paths.RolledBack(), &rb); ok && rb.UpgradeID != "" && rb.HandledAt == nil {
		a.recordRolledBack(&rb)
	}
	pd := paths.ReadPending()
	cur := a.upgradeCurrent()
	switch {
	case pd != nil && pd.To == a.version && pd.From != a.version:
		a.finishHealthy(ctx, pd)
	case pd != nil && pd.From == a.version && pd.To != a.version:
		if pd.Deadline == 0 { // CLI --no-restart: the old agent restarted before the binary changed? keep waiting
			break
		}
		// the old binary runs again but nothing wrote rolled-back.json (restored by hand / an interrupted rollback)
		rb := selfupgrade.RolledBack{UpgradeID: pd.UpgradeID, From: pd.From, To: pd.To, Restored: pd.From, By: "guard",
			Reason: "the previous agent " + pd.From + " runs again (the new agent " + pd.To + " never reported healthy)", At: nowTs()}
		_ = os.Remove(paths.Pending())
		a.recordRolledBack(&rb)
	case pd != nil:
		a.log.Warn("gui", "stale upgrade/pending.json removed", "upgradeId", pd.UpgradeID, "from", pd.From, "to", pd.To, "running", a.version)
		_ = os.Remove(paths.Pending())
		if cur != nil && cur.UpgradeID == pd.UpgradeID && !model.UpgradeTerminal(cur.Phase) {
			a.failUpgrade(cur, cur.Phase, fmt.Errorf("the agent now runs %s (neither %s nor %s)", a.version, pd.From, pd.To))
		}
	case cur != nil && !model.UpgradeTerminal(cur.Phase):
		a.self.mu.Lock()
		running := a.self.running
		a.self.mu.Unlock()
		if !running {
			if cur.To == a.version && (cur.Phase == model.UpgradeRestart || cur.Phase == model.UpgradeHealth) {
				cur.Phase, cur.Error = model.UpgradeDone, nil
				a.saveUpgrade(cur, true)
			} else {
				a.failUpgrade(cur, cur.Phase, fmt.Errorf("interrupted by an agent restart during %s", cur.Phase))
			}
		}
	}
	a.finishMeshRunOnStart(ctx)
}

// finishHealthy: this is the new agent of a pending upgrade.
func (a *Agent) finishHealthy(ctx context.Context, pd *selfupgrade.Pending) {
	paths := a.self.paths
	p := a.upgradeCurrent()
	if p == nil || p.UpgradeID != pd.UpgradeID {
		p = &model.AgentUpgradeProgress{UpgradeID: pd.UpgradeID, RequestID: pd.RequestID, RunID: pd.RunID, Via: pd.Via, SiteSlug: a.site,
			From: pd.From, To: pd.To, Actor: pd.Actor, StartedAt: pd.RestartedAt}
	}
	p.Phase, p.Progress = model.UpgradeHealth, nil
	a.saveUpgrade(p, true)
	// the local self-check: the agent port answers /_opm/healthz
	url := fmt.Sprintf("http://127.0.0.1:%d/_opm/api/health", a.currentAgentPort()) // §21 #91
	until := time.Now().Add(30 * time.Second)
	if pd.Deadline > 0 {
		if d := time.Unix(pd.Deadline, 0).Add(-2 * time.Second); d.Before(until) {
			until = d
		}
	}
	ok := false
	for {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, _ := http.NewRequestWithContext(cctx, "GET", url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			ok = resp.StatusCode == 200
			resp.Body.Close()
		}
		cancel()
		if ok || time.Now().After(until) || ctx.Err() != nil {
			break
		}
		sleepCtx(ctx, 500*time.Millisecond)
	}
	if !ok {
		a.log.Error("gui", "upgrade health self-check failed: not marking healthy (the watcher / boot guard roll back)", "url", url)
		return
	}
	if err := paths.MarkHealthy(pd.UpgradeID); err != nil {
		a.log.Error("gui", "write upgrade/healthy failed", "error", err.Error())
		return
	}
	_ = os.Remove(paths.Pending())
	p.Phase, p.Error = model.UpgradeDone, nil
	a.saveUpgrade(p, true)
	dur := int64(time.Since(parseTs(p.StartedAt)).Seconds())
	a.activity(p.Actor, "agent", "agent.upgraded", fmt.Sprintf("agent upgraded %s → %s", p.From, p.To),
		upgradeActivityData(p, map[string]any{"durationSec": dur}))
}

// recordRolledBack: the restored agent reports the failed attempt (once: handledAt is written back).
func (a *Agent) recordRolledBack(rb *selfupgrade.RolledBack) {
	p := a.upgradeCurrent()
	if p == nil || p.UpgradeID != rb.UpgradeID {
		p = &model.AgentUpgradeProgress{UpgradeID: rb.UpgradeID, Via: "gui", SiteSlug: a.site, From: rb.From, To: rb.To, StartedAt: rb.At}
	}
	reason := rb.Reason
	if reason == "" {
		reason = "the new agent was not healthy in time"
	}
	failed := model.UpgradeHealth
	if p.Phase == model.UpgradeRestart { // the new process never reached its health phase
		failed = model.UpgradeRestart
	}
	p.Phase, p.Error, p.Progress, p.FailedPhase = model.UpgradeRolledBack, &reason, nil, &failed
	a.saveUpgrade(p, true)
	a.activity(p.Actor, "agent", "agent.upgrade-failed", fmt.Sprintf("agent upgrade to %s failed (%s): %s", rb.To, failed, reason),
		upgradeActivityData(p, map[string]any{"phase": failed, "error": reason}))
	a.activity(p.Actor, "agent", "agent.rolled-back", fmt.Sprintf("agent rolled back to %s (%s was not healthy; %s)", rb.Restored, rb.To, rb.By),
		map[string]any{"upgradeId": rb.UpgradeID, "from": rb.To, "to": rb.Restored, "reason": reason, "by": rb.By})
	h := nowTs()
	rb.HandledAt = &h
	_ = selfupgrade.WriteJSON(a.self.paths.RolledBack(), rb)
}
