package agent

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/model"
)

// §21 #107 commit staging on the agent (unit: no Docker).
func stageCall(t *testing.T, a *Agent, sid string, blobs []model.BlobNeed) model.Stage {
	t.Helper()
	b, _ := json.Marshal(model.CreateStageRequest{Blobs: blobs})
	r := withRC(httptest.NewRequest("POST", "/_opm/api/stages", bytes.NewReader(b)), &reqCtx{actor: "ops@example.com", sessionID: sid})
	out, code, he := a.handleCreateStage(httptest.NewRecorder(), r)
	if he != nil || code != 201 {
		t.Fatalf("create stage: %v", he)
	}
	return out.(model.Stage)
}
func putStage(a *Agent, sid, stageID, sha string, body []byte) (*model.StageBlobResponse, *httpError) {
	r := httptest.NewRequest("PUT", "/", bytes.NewReader(body))
	r.SetPathValue("stageId", stageID)
	r.SetPathValue("sha256", sha)
	r = withRC(r, &reqCtx{actor: "ops@example.com", sessionID: sid})
	out, _, he := a.handlePutStageBlob(httptest.NewRecorder(), r)
	if he != nil {
		return nil, he
	}
	x := out.(model.StageBlobResponse)
	return &x, nil
}

func TestStagingCommit(t *testing.T) {
	a, r1 := branchUnitAgent(t)
	body := []byte("new banner from the GUI\n")
	h := bytesSha256Hex(body)
	edited := clone(t, r1.Config)
	edited.Files[0].Content = &model.BlobRef{Sha256: "sha256:" + h, SizeBytes: int64(len(body))}
	needs := model.NewConfigBlobs(&r1.Config, &edited)
	if len(needs) != 1 || needs[0].Sha256 != "sha256:"+h {
		t.Fatalf("needs %+v", needs)
	}
	// no stage → 422 V_BLOB_MISSING with details.missing
	c, b := callCommit(t, a, map[string]any{"message": "x", "baseCommitId": r1.RevID, "config": inputOf(edited)})
	if c != 422 || !strings.Contains(jsonText(b), "V_BLOB_MISSING") || b["missing"] == nil {
		t.Fatalf("missing: %d %v", c, b)
	}
	st := stageCall(t, a, "s1", needs)
	if len(st.Missing) != 1 || st.Present != 0 {
		t.Fatalf("stage %+v", st)
	}
	// wrong bytes → 422; other session → 404
	if _, he := putStage(a, "s1", st.StageID, "sha256:"+h, []byte("other")); he == nil || he.code != model.ErrInvalid {
		t.Fatal("sha mismatch accepted")
	}
	if _, he := putStage(a, "s2", st.StageID, "sha256:"+h, body); he == nil || he.code != model.ErrNotFound {
		t.Fatal("another session wrote into the stage")
	}
	if x, he := putStage(a, "s1", st.StageID, "sha256:"+h, body); he != nil || !x.Staged {
		t.Fatalf("put: %v", he)
	}
	if a.haveBlob(h) {
		t.Fatal("staged blob in the store before Commit")
	}
	// a failing commit (stale base) removes the stage and stores nothing
	c, _ = callCommit(t, a, map[string]any{"message": "x", "baseCommitId": "01JB0000000000000000000099", "config": inputOf(edited), "stageId": st.StageID})
	if c != 409 {
		t.Fatalf("stale %d", c)
	}
	if _, err := os.Stat(a.stageDir(st.StageID)); !os.IsNotExist(err) || a.haveBlob(h) {
		t.Fatal("failed commit: stage kept or blob promoted")
	}
	// corrupted staged bytes (size ok, sha wrong) → 422 V_BLOB_MISSING
	st2 := stageCall(t, a, "s1", needs)
	if _, he := putStage(a, "s1", st2.StageID, "sha256:"+h, body); he != nil {
		t.Fatal(he.msg)
	}
	_ = os.WriteFile(a.stageFile(st2.StageID, h), bytes.Repeat([]byte("x"), len(body)), 0o600)
	if he := a.promoteStage(st2.StageID, &r1.Config, &edited); he == nil || !strings.Contains(jsonText(he.details), "V_BLOB_MISSING") {
		t.Fatal("corrupted staged blob accepted")
	}
	// a good stage promotes into the blob store (what commitEdits does before the engine stages)
	_ = os.WriteFile(a.stageFile(st2.StageID, h), body, 0o600)
	if he := a.promoteStage(st2.StageID, &r1.Config, &edited); he != nil {
		t.Fatal(he.msg)
	}
	if !a.haveBlob(h) {
		t.Fatal("not promoted")
	}
	// already stored → createStage answers present, put answers staged=false
	st3 := stageCall(t, a, "s1", needs)
	if len(st3.Missing) != 0 || st3.Present != 1 {
		t.Fatalf("present %+v", st3)
	}
	if x, he := putStage(a, "s1", st3.StageID, "sha256:"+h, body); he != nil || x.Staged {
		t.Fatal("re-staged a stored blob")
	}
	// bad requests
	b2, _ := json.Marshal(model.CreateStageRequest{Blobs: []model.BlobNeed{{Sha256: "nope", SizeBytes: 1}}})
	if _, _, he := a.handleCreateStage(httptest.NewRecorder(), withRC(httptest.NewRequest("POST", "/", bytes.NewReader(b2)), &reqCtx{})); he == nil || he.code != model.ErrInvalid {
		t.Fatal("bad sha accepted")
	}
}

func TestStagingTTL(t *testing.T) {
	a, _ := branchUnitAgent(t)
	st := stageCall(t, a, "s1", nil)
	fresh := stageCall(t, a, "s1", nil)
	p := filepath.Join(a.stageDir(st.StageID), "stage.json")
	var m stageMeta
	b, _ := os.ReadFile(p)
	_ = json.Unmarshal(b, &m)
	m.CreatedAt = tsOf(time.Now().Add(-25 * time.Hour))
	m.ExpiresAt = tsOf(time.Now().Add(-time.Hour))
	b, _ = json.Marshal(m)
	_ = os.WriteFile(p, b, 0o600)
	if _, he := putStage(a, "s1", st.StageID, "sha256:"+strings.Repeat("a", 64), []byte("x")); he == nil || he.code != model.ErrGone {
		t.Fatal("expired stage accepted a blob")
	}
	_ = os.MkdirAll(filepath.Join(a.stagingDir(), "junk"), 0o700)
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(a.stagingDir(), "junk"), old, old)
	a.cleanupStages()
	if _, err := os.Stat(a.stageDir(st.StageID)); !os.IsNotExist(err) {
		t.Fatal("expired stage kept")
	}
	if _, err := os.Stat(filepath.Join(a.stagingDir(), "junk")); !os.IsNotExist(err) {
		t.Fatal("old junk kept")
	}
	if _, err := os.Stat(a.stageDir(fresh.StageID)); err != nil {
		t.Fatal("fresh stage removed")
	}
	// delete
	r := httptest.NewRequest("DELETE", "/", nil)
	r.SetPathValue("stageId", fresh.StageID)
	if _, _, he := a.handleDeleteStage(httptest.NewRecorder(), r); he != nil {
		t.Fatal(he.msg)
	}
	if _, err := os.Stat(a.stageDir(fresh.StageID)); !os.IsNotExist(err) {
		t.Fatal("deleted stage kept")
	}
}
