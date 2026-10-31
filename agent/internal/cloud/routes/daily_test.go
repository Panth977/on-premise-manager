package routes

import (
	"testing"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// TestRunDaily: the TS suite runs runDaily of the TS tree against the data dir, so the Go job is exercised here, on a strict
// store (every query needs its index).
func TestRunDaily(t *testing.T) {
	strict := true
	p, err := driver.Open(driver.Config{DataDir: t.TempDir(), PublicURL: "http://x", Strict: &strict})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cloud.SetPorts(p)
	now := time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC)
	iso := func(d time.Duration) string { return doc.ISO(now.Add(d)) }
	past := doc.TimestampFromMillis(now.Add(-time.Hour).UnixMilli())
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	draft := doc.MarshalString(doc.Obj("variables", []any{}, "secrets", []any{}, "files", []any{}, "sites", []any{}))
	run := doc.Obj("runId", "r1", "status", "running", "version", "1.0.0", "sites", []any{doc.Obj("siteSlug", "aa", "status", "requested", "requestId", "q1", "at", nil)}, "finishedAt", nil)
	must(cloud.Col.Deployments().Doc("d1").Set(doc.Obj("deploymentId", "d1", "label", "L", "tagSlugs", []any{}, "managerAccountUids", []any{}, "cloudActivitySeq", 0.0, "agentEpoch", 1.0, "deleted", false, "_draft", draft, "agentUpgradeRun", run)))
	must(cloud.Col.Uptime("d1").Doc("aa_20260101").Set(doc.Obj("date", "20260101", "hours", []any{})))
	must(cloud.Col.Uptime("d1").Doc("aa_20261030").Set(doc.Obj("date", "20261030", "hours", []any{})))
	must(cloud.Col.Uploads().Doc("u1").Set(doc.Obj("uploadId", "u1", "status", "open", "expiresAt", iso(-time.Hour))))
	must(cloud.Col.AgentNonces().Doc("n1").Set(doc.Obj("expiresAt", past)))
	must(cloud.DB().Collection("rateLimits").Doc("r1").Set(doc.Obj("expireAt", past)))
	must(cloud.Col.InstallTokens("d1").Doc("recent").Set(doc.Obj("tokenHash", "recent", "token", "opi_a", "expiresAt", iso(-time.Hour), "usedAt", nil)))
	must(cloud.Col.InstallTokens("d1").Doc("old").Set(doc.Obj("tokenHash", "old", "token", "opi_b", "expiresAt", iso(-10*24*time.Hour), "usedAt", nil)))
	must(cloud.Col.InstallTokens("d1").Doc("used").Set(doc.Obj("tokenHash", "used", "token", "opi_c", "expiresAt", iso(-10*24*time.Hour), "usedAt", iso(-11*24*time.Hour))))
	must(cloud.Col.Agents("d1").Doc("aa").Set(doc.Obj("siteSlug", "aa", "agentVersion", "0.9.0", "upgradeRequest", doc.Obj("requestId", "q1", "siteSlug", "aa", "runId", "r1", "expiresAt", iso(-time.Hour)))))
	r, err := RunDaily(now)
	must(err)
	want := doc.Obj("uptimeDeleted", 1.0, "uploadsExpired", 1.0, "noncesDeleted", 1.0, "tokensDeleted", 1.0, "bundlesDeleted", 0.0, "tokensCleared", 2.0, "rateLimitsDeleted", 1.0, "agentUpgradesExpired", 1.0, "volumeVersionsPruned", 0.0, "stagesExpired", 0.0)
	if a, b := doc.MarshalString(r), doc.MarshalString(want); a != b {
		t.Fatalf("result\n got %s\nwant %s", a, b)
	}
	s, err := cloud.Col.Deployments().Doc("d1").Get()
	must(err)
	if got := s.Data().Map("agentUpgradeRun").Val("status"); got != "failed" {
		t.Fatalf("run status %v", got)
	}
	a, err := cloud.Col.Agents("d1").Doc("aa").Get()
	must(err)
	if a.Data().Val("upgradeRequest") != nil {
		t.Fatal("upgrade request kept")
	}
	if s, err := cloud.Col.InstallTokens("d1").Doc("recent").Get(); err != nil || s.Data().Val("token") != nil {
		t.Fatalf("recent token %v %v", s.Data(), err)
	}
	if s, err := cloud.Col.InstallTokens("d1").Doc("used").Get(); err != nil || s.Data().Val("token") != "opi_c" {
		t.Fatalf("used token %v %v", s.Data(), err)
	}
	q, err := cloud.Col.AuditLog().Where("action", "==", "system.daily-cleanup").Get()
	must(err)
	if q.Size() != 1 {
		t.Fatalf("audit entries %d", q.Size())
	}
}
