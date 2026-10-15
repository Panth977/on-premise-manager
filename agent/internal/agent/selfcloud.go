package agent

// Cloud "Upgrade agent" (SPEC-final §21 #90 f): CloudView.agentUpgrades lists the unexpired requests of all sites; this
// agent takes only its own siteSlug's, once per requestId (persisted in upgrade/handled-requests.json so a restart does
// not run it again), with via "cloud" and actor = requestedBy. A request for a version other than the current pin fails
// at once without a download. HeartbeatRequest.upgrade carries the current / last attempt (immediately on every phase
// change, then with every heartbeat until 24 h after it finished).

import (
	"context"
	"fmt"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/selfupgrade"
)

// heartbeatUpgrade is HeartbeatRequest.upgrade.
func (a *Agent) heartbeatUpgrade() *model.AgentUpgradeProgress {
	p := a.upgradeCurrent()
	if p == nil {
		return nil
	}
	if model.UpgradeTerminal(p.Phase) && p.FinishedAt != nil && time.Since(parseTs(*p.FinishedAt)) > 24*time.Hour {
		return nil
	}
	return p
}

type handledRequests struct {
	IDs map[string]string `json:"ids"` // requestId → handled at
}

func (a *Agent) handledRequest(id string) bool {
	var h handledRequests
	_, _ = selfupgrade.ReadJSON(a.self.paths.Handled(), &h)
	_, ok := h.IDs[id]
	return ok
}

func (a *Agent) markRequestHandled(id string) {
	var h handledRequests
	_, _ = selfupgrade.ReadJSON(a.self.paths.Handled(), &h)
	if h.IDs == nil {
		h.IDs = map[string]string{}
	}
	h.IDs[id] = nowTs()
	cut := time.Now().Add(-7 * 24 * time.Hour) // requests expire after 24 h; keep a week
	for k, v := range h.IDs {
		if parseTs(v).Before(cut) {
			delete(h.IDs, k)
		}
	}
	if err := selfupgrade.WriteJSON(a.self.paths.Handled(), h); err != nil {
		a.log.Error("gui", "handled-requests.json write failed", "error", err.Error())
	}
}

// reportOnly records an attempt that ends without running (pinned moved / already on the version).
func (a *Agent) reportUpgradeOutcome(r model.AgentUpgradeRequest, phase, errMsg string) {
	now := nowTs()
	actor := r.RequestedBy
	p := &model.AgentUpgradeProgress{UpgradeID: newID(), RequestID: &r.RequestID, RunID: r.RunID, Via: "cloud", SiteSlug: a.site, From: a.version,
		To: r.Version, Phase: phase, Actor: &actor, StartedAt: now, UpdatedAt: now}
	if phase == model.UpgradeFailed {
		a.failUpgrade(p, model.UpgradeDownload, fmt.Errorf("%s", errMsg))
		return
	}
	a.saveUpgrade(p, true)
}

// handleCloudUpgrades runs at every adopted cloud view (reconcile).
func (a *Agent) handleCloudUpgrades(ctx context.Context, v *model.CloudView) {
	for _, r := range v.AgentUpgrades {
		if r.SiteSlug != a.site || r.RequestID == "" {
			continue
		}
		if t := parseTs(r.ExpiresAt); !t.IsZero() && time.Now().After(t) {
			continue
		}
		if a.handledRequest(r.RequestID) {
			continue
		}
		if a.lockedErr() != nil {
			continue // an update / snapshot runs: take it at a later view (the request stays queued until it expires)
		}
		if p := a.upgradeRunning(); p != nil {
			if p.RequestID != nil && *p.RequestID == r.RequestID {
				a.markRequestHandled(r.RequestID)
			}
			continue // the running attempt finishes first; the request is taken afterwards
		}
		a.markRequestHandled(r.RequestID) // before starting: a restart never runs it twice
		pin := ""
		if v.AgentRelease != nil {
			pin = v.AgentRelease.Version
		}
		switch {
		case pin == "" || pin != r.Version:
			msg := "the pinned release moved to " + pin + "; request again"
			if pin == "" {
				msg = "no agent release is pinned anymore; request again"
			}
			a.reportUpgradeOutcome(r, model.UpgradeFailed, msg)
		case r.Version == a.version:
			a.reportUpgradeOutcome(r, model.UpgradeDone, "")
		default:
			actor := r.RequestedBy
			reqID := r.RequestID
			if _, e := a.startUpgrade(ctx, upgradeStart{Version: r.Version, Via: "cloud", Actor: &actor, RequestID: &reqID, RunID: r.RunID}); e != nil {
				a.reportUpgradeOutcome(r, model.UpgradeFailed, e.msg)
			}
		}
	}
}
