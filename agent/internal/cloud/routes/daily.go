package routes

import (
	"context"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// The daily job (jobs/daily.ts, SPEC-final §2.2 UptimeDay, §6.1 uploads, §14.7): uptime docs > 30 days (transfers never
// expire), expired uploads, expired agent nonces, install tokens expired more than 7 days ago, activity bundles older than
// 90 days, plaintext of expired install tokens, reveal rate-limit counters, unpicked agent upgrade requests, AUTO
// volume-content snapshots, expired commit stages.

func init() {
	cloud.RegisterJob(driver.ScheduledJob{Name: "daily", Schedule: "every day 03:00", TimeoutSeconds: 540, Run: func(context.Context) error {
		r, err := RunDaily(time.Now())
		if err != nil {
			msg := err.Error()
			cloud.AuditSystem(cloud.AuditSystemOpts{Job: "daily", Action: "system.daily-cleanup", TargetType: "system", Target: "system/daily", OK: false, ErrorMessage: &msg})
			return err
		}
		args := []any{}
		r.Each(func(k string, v any) { args = append(args, k, v) })
		driver.Log.Info("daily job", args...)
		return nil
	}})
}

func deleteRefs(refs []*driver.DocRef) error {
	for i := 0; i < len(refs); i += 500 {
		b := cloud.DB().Batch()
		for _, r := range refs[i:min(i+500, len(refs))] {
			b.Delete(r)
		}
		if err := b.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func refsOf(docs []*driver.DocSnap) []*driver.DocRef {
	out := make([]*driver.DocRef, len(docs))
	for i, d := range docs {
		out[i] = d.Ref()
	}
	return out
}

// RunDaily is runDaily: the DailyResult.
func RunDaily(now time.Time) (*doc.Map, error) {
	t0 := time.Now()
	db := cloud.DB()
	nowTs := doc.TimestampFromMillis(now.UnixMilli())
	var uptimeDeleted, uploadsExpired, noncesDeleted, tokensDeleted, bundlesDeleted, tokensCleared, rateLimitsDeleted, agentUpgradesExpired, volumeVersionsPruned, stagesExpired int
	// uptime: keep 30 days
	cutoff := cloud.YMD(now.Add(-30 * 24 * time.Hour))
	up, err := db.CollectionGroup("uptime").Where("date", "<", cutoff).Get()
	if err != nil {
		return nil, err
	}
	if err := deleteRefs(refsOf(up.Docs)); err != nil {
		return nil, err
	}
	uptimeDeleted = up.Size()
	// transfers never expire (only Delete removes one): nothing to do here
	iso := doc.ISO(now)
	// uploads
	ups, err := cloud.Col.Uploads().Where("expiresAt", "<=", iso).Get()
	if err != nil {
		return nil, err
	}
	for _, d := range ups.Docs {
		u := d.Data()
		if u.Val("status") == "open" {
			if err := d.Ref().Update(doc.Obj("status", "failed", "error", "expired")); err != nil {
				return nil, err
			}
			uploadsExpired++
		}
		_ = core.RemovePrefix("uploads/" + u.Str("uploadId") + "/")
	}
	// §21 #107 abandoned commit stages (doc + staging/<id>/ objects; the Storage lifecycle rule removes stray objects too)
	if stagesExpired, err = core.CleanupStages(now.UnixMilli()); err != nil {
		stagesExpired = 0
	}
	// nonces (TTL policy also removes them)
	nn, err := cloud.Col.AgentNonces().Where("expiresAt", "<", nowTs).Limit(5000).Get()
	if err != nil {
		return nil, err
	}
	if err := deleteRefs(refsOf(nn.Docs)); err != nil {
		return nil, err
	}
	noncesDeleted = nn.Size()
	// install tokens: expired UNUSED ones keep no plaintext (§21 #84). A used install token is its enrollment's upgrade
	// command (§21 #92, no expiry; nulled by revokeAgent / Regenerate); maintenance tokens have expiresAt null (never matched).
	expired, err := db.CollectionGroup("installTokens").Where("expiresAt", "<=", iso).Get()
	if err != nil {
		return nil, err
	}
	var withToken []*driver.DocSnap
	for _, d := range expired.Docs {
		t := d.Data()
		if _, ok := t.StrOK("token"); ok && !t.Truthy("usedAt") && t.Val("kind") != "maintenance" {
			withToken = append(withToken, d)
		}
	}
	for i := 0; i < len(withToken); i += 500 {
		b := db.Batch()
		for _, d := range withToken[i:min(i+500, len(withToken))] {
			b.Update(d.Ref(), doc.Obj("token", nil))
		}
		if err := b.Commit(); err != nil {
			return nil, err
		}
	}
	tokensCleared = len(withToken)
	// reveal rate-limit counters (the TTL policy on rateLimits.expireAt removes them too)
	rl, err := db.Collection("rateLimits").Where("expireAt", "<", nowTs).Limit(5000).Get()
	if err != nil {
		return nil, err
	}
	if err := deleteRefs(refsOf(rl.Docs)); err != nil {
		return nil, err
	}
	rateLimitsDeleted = rl.Size()
	// install tokens: unused tokens expired > 7 days ago
	tokCut := doc.ISO(now.Add(-7 * 24 * time.Hour))
	toks, err := db.CollectionGroup("installTokens").Where("expiresAt", "<", tokCut).Get()
	if err != nil {
		return nil, err
	}
	var stale []*driver.DocRef
	for _, d := range toks.Docs {
		if !d.Data().Truthy("usedAt") {
			stale = append(stale, d.Ref())
		}
	}
	if err := deleteRefs(stale); err != nil {
		return nil, err
	}
	tokensDeleted = len(stale)
	// §21 #90 agent upgrade requests (unstarted, past expiresAt) + running "Upgrade all" runs waiting on one
	reqs, err := db.CollectionGroup("agents").Where("upgradeRequest.expiresAt", "<=", iso).Get()
	if err != nil {
		return nil, err
	}
	runs, err := cloud.Col.Deployments().Where("agentUpgradeRun.status", "==", "running").Get()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(reqs.Docs)+len(runs.Docs))
	for _, d := range reqs.Docs {
		ids = append(ids, d.Ref().Parent().Parent().ID())
	}
	for _, d := range runs.Docs {
		ids = append(ids, d.ID())
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		n, err := ExpireAgentUpgrades(id, now.UnixMilli())
		if err != nil {
			return nil, err
		}
		agentUpgradesExpired += n
	}
	// §21 #101 volume content: prune old AUTO snapshots (never versions a push / another version references; blobs are never deleted)
	if volumeVersionsPruned, err = PruneVolumeContent(); err != nil {
		return nil, err
	}
	// activity bundles: 90 days
	files, err := cloud.Blobs().List("activity-bundles/")
	if err != nil {
		return nil, err
	}
	bCut := now.UnixMilli() - 90*86400_000
	for _, f := range files {
		if f.CreatedMs > 0 && f.CreatedMs < bCut {
			if err := cloud.Blobs().Delete(f.Path); err != nil {
				return nil, err
			}
			bundlesDeleted++
		}
	}
	n := func(i int) float64 { return float64(i) }
	res := doc.Obj("uptimeDeleted", n(uptimeDeleted), "uploadsExpired", n(uploadsExpired), "noncesDeleted", n(noncesDeleted), "tokensDeleted", n(tokensDeleted), "bundlesDeleted", n(bundlesDeleted), "tokensCleared", n(tokensCleared), "rateLimitsDeleted", n(rateLimitsDeleted), "agentUpgradesExpired", n(agentUpgradesExpired), "volumeVersionsPruned", n(volumeVersionsPruned), "stagesExpired", n(stagesExpired))
	cloud.AuditSystem(cloud.AuditSystemOpts{Job: "daily", Action: "system.daily-cleanup", TargetType: "system", Target: "system/daily", OK: true, Data: res.Shallow().Set("startedAt", doc.ISO(now)).Set("durationMs", float64(time.Since(t0).Milliseconds()))})
	return res, nil
}
