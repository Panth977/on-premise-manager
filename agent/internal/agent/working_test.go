package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/model"
)

// a unit agent in branch mode with the hello config as its head (no Docker: a.dc = nil, drift = the saved scan)
func branchUnitAgent(t *testing.T) (*Agent, *model.Revision) {
	t.Helper()
	a := newTestAgent(t)
	a.dc = nil
	cfg, cat := helloFixture(t)
	if cat.NotFoundPages == nil {
		cat.NotFoundPages = map[string]model.CatalogNotFoundPage{}
	}
	for _, s := range cfg.Sites {
		if s.Config.NotFoundPageID != nil {
			cat.NotFoundPages[*s.Config.NotFoundPageID] = model.CatalogNotFoundPage{NotFoundPageID: *s.Config.NotFoundPageID, Label: "404", HTMLCode: "<h1>404</h1>"}
		}
	}
	for i := range cfg.Secrets {
		v := "plain-" + cfg.Secrets[i].SecretSlug
		cfg.Secrets[i].Value = &v
	}
	r1 := mkRev(t, "01JB0000000000000000000001", 1, nil, cfg, cat, "release 1", "cloud")
	if err := a.putRevision(r1); err != nil {
		t.Fatal(err)
	}
	_ = a.db.SetKV("head", model.HeadHash{RevID: r1.RevID, Seq: 1, Hash: r1.Hash})
	_ = a.db.SetKV("branchMode", true)
	a.migrateCommits()
	return a, r1
}

func callCommit(t *testing.T, a *Agent, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/_opm/api/branch/commit", bytes.NewReader(b))
	r = withRC(r, &reqCtx{actor: "ops@example.com"})
	out, code, he := a.handleCommitLocal(httptest.NewRecorder(), r)
	if he != nil {
		det := map[string]any{"code": he.code, "message": he.msg}
		for k, v := range he.details {
			det[k] = v
		}
		return model.ErrorHTTPStatus[he.code], det
	}
	m := map[string]any{}
	jb, _ := json.Marshal(out)
	_ = json.Unmarshal(jb, &m)
	return code, m
}

func inputOf(c model.Config) model.ConfigInput {
	in := configToInput(c)
	for i := range in.Secrets {
		in.Secrets[i].PlainValue = model.Optional[string]{} // absent = keep
	}
	return in
}

// §21 #105 J11 / J12: the GUI-edits commit refusals (before anything is applied), putWorking 410, secretValues.
func TestCommitEditsRefusals(t *testing.T) {
	a, r1 := branchUnitAgent(t)
	// putWorking is gone
	if _, _, he := a.handlePutWorking(httptest.NewRecorder(), httptest.NewRequest("PUT", "/", nil)); he == nil || he.code != model.ErrGone {
		t.Fatal("putWorking: expected 410")
	}
	edited := clone(t, r1.Config)
	edited.Variables[0].Value = model.VariableValue{V: "yellow"}
	in := inputOf(edited)
	// config + keys → 422
	if c, b := callCommit(t, a, map[string]any{"message": "x", "baseCommitId": r1.RevID, "config": in, "keys": []string{"a"}}); c != 422 {
		t.Fatalf("mixed: %d %v", c, b)
	}
	// baseCommitId missing → 422
	if c, b := callCommit(t, a, map[string]any{"message": "x", "config": in}); c != 422 {
		t.Fatalf("no base: %d %v", c, b)
	}
	// another base → 409 stale head-moved with the head
	c, b := callCommit(t, a, map[string]any{"message": "x", "baseCommitId": "01JB0000000000000000000099", "config": in})
	if c != 409 || b["code"] != model.ErrStale || b["reason"] != model.ReasonHeadMoved || b["head"] == nil {
		t.Fatalf("stale: %d %v", c, b)
	}
	// uncommitted drift → 409 conflict uncommitted-drift
	id := r1.RevID
	scan := &driftScan{DriftReport: model.DriftReport{ScannedAt: nowTs(), HeadCommitID: &id, Notes: []model.DriftNote{}, LocalImages: []string{}},
		LiveConfig: r1.Config}
	scan.Items = []model.DriftItem{{MergeItem: model.MergeItem{Key: "sites/app/containers/hello-api/restart", Section: "containers", Kind: "change", Effect: "change"}, Origin: "docker", Detail: "changed by hand"}}
	scan.LiveConfig = clone(t, r1.Config)
	for i := range scan.LiveConfig.Sites {
		for j := range scan.LiveConfig.Sites[i].Config.Containers {
			if scan.LiveConfig.Sites[i].Config.Containers[j].ContainerSlug == "hello-api" {
				scan.LiveConfig.Sites[i].Config.Containers[j].Restart = "never"
			}
		}
	}
	a.saveDrift(scan)
	if u := a.uncommittedNow(); u.blocking() != 1 {
		ds := a.lastDrift()
		t.Fatalf("drift fixture: blocking %d items %+v scan %v", u.blocking(), u.items, ds != nil)
	}
	c, b = callCommit(t, a, map[string]any{"message": "x", "baseCommitId": r1.RevID, "config": in})
	if c != 409 || b["code"] != model.ErrConflict || b["reason"] != model.ReasonUncommittedDrift {
		t.Fatalf("drift: %d %v", c, b)
	}
	a.saveDrift(&driftScan{DriftReport: model.DriftReport{ScannedAt: nowTs(), HeadCommitID: &id, Items: []model.DriftItem{}, Notes: []model.DriftNote{}, LocalImages: []string{}}, LiveConfig: r1.Config})
	// nothing changed → V_NO_CHANGE
	c, b = callCommit(t, a, map[string]any{"message": "x", "baseCommitId": r1.RevID, "config": inputOf(r1.Config)})
	if c != 422 || !strings.Contains(jsonText(b), "V_NO_CHANGE") {
		t.Fatalf("no change: %d %v", c, b)
	}
	// an invalid edit → 422 (§21 #113: adding a container is allowed; this copy publishes the same host ports → conflict)
	bad := clone(t, r1.Config)
	ct := bad.Sites[0].Config.Containers[0]
	ct.ContainerSlug = "extra"
	bad.Sites[0].Config.Containers = append(bad.Sites[0].Config.Containers, ct)
	if c, b := callCommit(t, a, map[string]any{"message": "x", "baseCommitId": r1.RevID, "config": inputOf(bad)}); c != 422 {
		t.Fatalf("local edit: %d %v", c, b)
	}
	// secretValues: slugs only
	if len(r1.Config.Secrets) == 0 {
		t.Skip("fixture without secrets")
	}
	sin := inputOf(edited)
	sin.Secrets[0].PlainValue = model.Some("new-secret-value")
	rev, data, he := a.editRevision(a.headRevision(), sin, "ops@example.com", "m")
	if he != nil {
		t.Fatal(he.msg)
	}
	if data.SecretValues == nil || len(data.SecretValues.Set) != 1 || data.SecretValues.Set[0] != sin.Secrets[0].SecretSlug || strings.Contains(jsonText(data), "new-secret-value") {
		t.Fatalf("secretValues %+v", data.SecretValues)
	}
	if rev.Config.Secrets[0].Value == nil || *rev.Config.Secrets[0].Value != "new-secret-value" {
		t.Fatal("the value is in the commit config")
	}
	kin := inputOf(edited)
	krev, kdata, he := a.editRevision(a.headRevision(), kin, "ops@example.com", "m")
	if he != nil {
		t.Fatal(he.msg)
	}
	if v := krev.Config.Secrets[0].Value; v == nil || *v != *r1.Config.Secrets[0].Value || kdata.SecretValues != nil {
		t.Fatal("plainValue absent keeps the head value")
	}
}

// §21 #105 J13: saved 0.14.0 edits that cannot be auto-committed are dropped to the backup file (0600) once;
// edits equal to the head are just cleared.
func TestLegacyWorkingMigrationDrop(t *testing.T) {
	oldWait, oldPoll := legacyWorkingWait, legacyWorkingPoll
	legacyWorkingWait, legacyWorkingPoll = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { legacyWorkingWait, legacyWorkingPoll = oldWait, oldPoll })
	a, r1 := branchUnitAgent(t)
	edited := clone(t, r1.Config)
	edited.Variables[0].Value = model.VariableValue{V: "yellow"}
	other := "01JB0000000000000000000077"
	_ = a.db.SetKV("workingCopy", workingCopy{BaseCommitID: &other, Config: edited, Keys: []string{"variables/theme/value"}, UpdatedAt: nowTs(), UpdatedBy: "ops@example.com"})
	_, _ = a.db.Exec(`INSERT OR REPLACE INTO working_items(key, has, value, updatedAt, updatedBy) VALUES('variables/theme/value',1,NULL,?,'x')`, nowTs())
	id := r1.RevID
	a.saveDrift(&driftScan{DriftReport: model.DriftReport{ScannedAt: nowTs(), HeadCommitID: &id, Items: []model.DriftItem{}, Notes: []model.DriftNote{}, LocalImages: []string{}}, LiveConfig: r1.Config})
	a.migrateLegacyWorking(context.Background())
	m, ok := a.legacyMigrationState()
	if !ok || m.State != "done" || m.Result != "dropped" || !strings.Contains(m.Reason, "head moved") {
		t.Fatalf("migration %+v", m)
	}
	if a.legacyWorking() != nil {
		t.Fatal("kv workingCopy not cleared")
	}
	var n int
	_ = a.db.QueryRow(`SELECT count(*) FROM working_items`).Scan(&n)
	if n != 0 {
		t.Fatal("working_items not cleared")
	}
	p := filepath.Join(a.dataDir, "backup", legacyWorkingBackup)
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup %v %v", st, err)
	}
	_ = a.db.QueryRow(`SELECT count(*) FROM activities WHERE type = 'working.discarded' AND data LIKE '%head moved%'`).Scan(&n)
	if n != 1 {
		t.Fatal("working.discarded activity")
	}
	// done → a restart does not start the loop again
	a.startLegacyWorkingMigration()
	if a.legacyMigrating.Load() {
		t.Fatal("migration restarted")
	}
	// edits equal to the head: cleared, nothing committed or dropped
	_ = a.db.DelKV("workingMigration")
	_ = a.db.SetKV("workingCopy", workingCopy{BaseCommitID: &id, Config: r1.Config, Keys: []string{"variables/theme/value"}, UpdatedAt: nowTs(), UpdatedBy: "x"})
	a.migrateLegacyWorking(context.Background())
	if m, _ := a.legacyMigrationState(); m == nil || m.Result != "empty" || a.legacyWorking() != nil {
		t.Fatalf("empty %+v", m)
	}
	// no head scan within the wait → dropped with the reason
	_ = a.db.DelKV("workingMigration")
	_, _ = a.db.Exec(`DELETE FROM drift`)
	_ = a.db.SetKV("workingCopy", workingCopy{BaseCommitID: &id, Config: edited, Keys: []string{"variables/theme/value"}, UpdatedAt: nowTs(), UpdatedBy: "x"})
	a.migrateLegacyWorking(context.Background())
	if m, _ := a.legacyMigrationState(); m == nil || m.Result != "dropped" || !strings.Contains(m.Reason, "drift scan") {
		t.Fatalf("timeout %+v", m)
	}
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
