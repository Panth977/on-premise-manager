package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"opm/agent/internal/branch"
	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

func helloFixture(t *testing.T) (model.Config, model.RevisionCatalog) {
	t.Helper()
	var f struct {
		Config model.Config `json:"config"`
	}
	b, err := os.ReadFile("../../../shared/testdata/hello-config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	var cat model.RevisionCatalog
	cb, err := os.ReadFile("../../../shared/testdata/hello-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var wrap map[string]json.RawMessage
	_ = json.Unmarshal(cb, &wrap)
	if raw, ok := wrap["catalog"]; ok {
		_ = json.Unmarshal(raw, &cat)
	} else {
		_ = json.Unmarshal(cb, &cat)
	}
	for k, im := range cat.Images {
		svc, ver, _ := strings.Cut(k, ":")
		im.ServiceSlug, im.Version = svc, ver
		cat.Images[k] = im
	}
	for k, sv := range cat.Services {
		sv.ServiceSlug = k
		cat.Services[k] = sv
	}
	return f.Config, cat
}

func mkRev(t *testing.T, id string, seq int, parent *string, cfg model.Config, cat model.RevisionCatalog, summary, origin string) *model.Revision {
	t.Helper()
	normalizeConfig(&cfg)
	normalizeCatalog(&cat)
	h, _ := canonical.HashCanonical(cfg)
	ch, _ := canonical.HashCanonical(cat)
	return &model.Revision{RevID: id, Seq: seq, ParentRevID: parent, Origin: origin, Actor: "ops@example.com", CreatedAt: "2026-10-09T00:00:00.000Z", Config: cfg, Hash: h, Catalog: cat, CatalogHash: ch, Summary: summary, Kind: "change"}
}

func clone[T any](t *testing.T, v T) T {
	var out T
	b, _ := json.Marshal(v)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// §21 #104 §5.1: the first 0.14 start turns the revision chain into commits (same ids), the open local proposal into
// uncommitted edits, and the merge base into the head; old proposal endpoints answer 410 in branch mode.
func TestBranchMigrationAndMerge(t *testing.T) {
	a := newTestAgent(t)
	cfg, cat := helloFixture(t)
	r1 := mkRev(t, "01JA0000000000000000000001", 1, nil, cfg, cat, "release 1", "cloud")
	c2 := clone(t, cfg)
	c2.Variables[0].Value = model.VariableValue{V: "yellow"}
	p1 := r1.RevID
	r2 := mkRev(t, "01JA0000000000000000000002", 2, &p1, c2, cat, "", "cloud")
	for _, r := range []*model.Revision{r1, r2} {
		if err := a.putRevision(r); err != nil {
			t.Fatal(err)
		}
	}
	_ = a.db.SetKV("head", model.HeadHash{RevID: r2.RevID, Seq: 2, Hash: r2.Hash})
	// an open local proposal on the head
	c3 := clone(t, c2)
	c3.Variables[1].Value = model.VariableValue{V: "Hi from the site"}
	p2 := r2.RevID
	r3 := mkRev(t, "01JA0000000000000000000003", 3, &p2, c3, cat, "local greeting", "agent")
	_ = a.putRevision(r3)
	a.putProposal(&model.Proposal{RevID: r3.RevID, ParentRevID: &p2, Seq: 3, Summary: "local greeting", Origin: "agent", Kind: "change", CreatedAt: r3.CreatedAt, CreatedBy: "ops@example.com",
		Status: model.ProposalPublished, Sites: map[string]model.SiteStaging{}, UpdatedAt: r3.CreatedAt})

	a.migrateCommits()
	a.enterBranchMode()

	c1, ok := a.commitByID(r1.RevID)
	if !ok || c1.Kind != "migrated" || c1.Message != "release 1" || c1.Hash != r1.Hash {
		t.Fatalf("commit 1: %+v", c1)
	}
	cc2, ok := a.commitByID(r2.RevID)
	if !ok || cc2.Message != "migrated revision 2" || *cc2.ParentID != r1.RevID || cc2.Stats.Changed != 1 {
		t.Fatalf("commit 2: %+v", cc2)
	}
	if hc := a.headCommit(); hc == nil || hc.CommitID != r2.RevID {
		t.Fatal("head commit")
	}
	mf := a.mergedFrom()
	if mf == nil || mf.CommitID != r2.RevID || mf.Seq != 2 {
		t.Fatalf("merged from %+v", mf)
	}
	// §21 #105: no working copy — the local proposal is kept as 0.14.0-style saved edits for the J13 migration
	if u := a.uncommittedNow(); len(u.items) != 0 {
		t.Fatalf("uncommitted %+v", u.items)
	}
	if w := a.legacyWorking(); w == nil || len(w.Keys) != 1 || w.Keys[0] != "variables/greeting/value" {
		t.Fatalf("legacy working %+v", w)
	}
	if p, _ := a.proposal(r3.RevID); p.Status != model.ProposalSuperseded {
		t.Fatalf("local proposal %s", p.Status)
	}
	if rep := a.branchReport(); rep == nil || rep.Uncommitted.Working != 0 || rep.Head.CommitID != r2.RevID {
		t.Fatalf("report %+v", rep)
	}
	// the legacy endpoints are gone in branch mode
	_ = a.db.SetKV("branchMode", true)
	rec := httptest.NewRecorder()
	_, _, he := a.legacyGone(func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) { return nil, 200, nil })(rec, httptest.NewRequest("POST", "/", nil))
	if he == nil || he.code != model.ErrGone {
		t.Fatal("expected 410 gone")
	}

	// a cloud commit (release 3: theme blue + a new variable) merged with a cherry-pick of one change
	c4 := clone(t, c2)
	c4.Variables[0].Value = model.VariableValue{V: "blue"}
	c4.Variables = append(c4.Variables, model.Variable{VariableSlug: "motd", Label: "MOTD", Type: "string", Value: model.VariableValue{V: "hello"}, Options: nil})
	cat4 := cat
	ch, _ := canonical.HashCanonical(cat4)
	p := r2.RevID
	cloud := &model.Commit{CommitID: "01JA0000000000000000000010", Branch: model.BranchRef{Kind: "deployment", DeploymentID: a.dep}, Seq: 3, ParentID: &p, Kind: "change", Message: "release 3",
		Author: model.CommitAuthor{Kind: "person", Email: "dev@opm.test"}, CreatedAt: "2026-10-09T01:00:00.000Z", Config: c4, Hash: branch.ConfigHash(c4), Catalog: &cat4, CatalogHash: &ch, Data: model.CommitData{}}
	a.putCloudCommit(cloud, "cloud")
	mc, e := a.computeMerge(context.Background(), cloud.CommitID, []model.MergeChoice{{Key: "variables/motd", Take: "source"}}, nil)
	if e != nil {
		t.Fatal(e.msg)
	}
	kinds := map[string]string{}
	for _, it := range mc.plan {
		kinds[it.Key] = it.Kind
	}
	if kinds["variables/theme/value"] != "theirs" || kinds["variables/motd"] != "theirs" || len(kinds) != 2 {
		t.Fatalf("plan %v", kinds)
	}
	if mc.apply.Kind != "cherry-pick" || len(mc.apply.Taken) != 1 || len(mc.apply.NotTaken) != 1 {
		t.Fatalf("apply %+v", mc.apply)
	}
	if mc.result.Variables[0].Value.V != "yellow" || mc.result.Variables[len(mc.result.Variables)-1].VariableSlug != "motd" {
		t.Fatalf("result %+v", mc.result.Variables)
	}
	// UX: a preview with nothing picked says so (not a validation error of the unchanged result)
	if mc0, e := a.computeMerge(context.Background(), cloud.CommitID, nil, nil); e != nil {
		t.Fatal(e.msg)
	} else if pv := a.previewOf(context.Background(), mc0, false); len(pv.Validation.Errors) != 1 || pv.Validation.Errors[0].Code != "V_NO_CHANGE" || !strings.Contains(pv.Validation.Errors[0].Message, "nothing picked") {
		t.Fatalf("no-picks preview errors %+v", pv.Validation.Errors)
	}
	// §21 #105: saved edits are not uncommitted items (only drift blocks a merge)
	if u := a.uncommittedNow(); u.blocking() != 0 {
		t.Fatalf("blocking %d", u.blocking())
	}
}
