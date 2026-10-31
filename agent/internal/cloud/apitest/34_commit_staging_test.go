package apitest

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

func wgShaOf(b []byte) string { return "sha256:" + Sha256Hex(b) }

func wgHex(s string) string { return s[7:] }

func wgBytes(label string) []byte {
	r := make([]byte, 8)
	_, _ = rand.Read(r)
	return []byte(label + " " + Uniq("c") + " " + hex.EncodeToString(r) + "\n")
}

func wgStagedObjects(t testing.TB, stageID string) L {
	t.Helper()
	files, err := Bucket.B.List("staging/" + stageID + "/")
	NoErr(t, err)
	out := L{}
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func wgInStore(t testing.TB, sha string) bool {
	t.Helper()
	return Bucket.Exists(t, "files/"+wgHex(sha))
}

func wgStageDoc(t testing.TB, stageID string) bool {
	t.Helper()
	return Doc(t, "stages/"+stageID).Exists()
}

// wgWithContent: the first File of the hello config gets new content.
func wgWithContent(c J, b []byte) J {
	c.At("files.0").Map()["content"] = A{"sha256": wgShaOf(b), "sizeBytes": len(b)}
	return c
}

func wgCommitRaw(t testing.TB, bearer, path string, body any, fault string) *Resp {
	t.Helper()
	h := map[string]string{"authorization": bearer, "content-type": "application/json"}
	if fault != "" {
		h["x-opm-test-fault"] = fault
	}
	return Raw(t, http.MethodPost, path, Opt{Headers: h, Body: []byte(JOf(body).String())})
}

// Port of backend/test/34-commit-staging.test.ts.
func TestCommitStaging(t *testing.T) {
	t.Run("deployment commit with a new blob: stage reports only missing blobs, commit promotes, stage gone, blob readable; no blob audit noise", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		api := admin.Real
		d0 := api.Call(t, "getDeployment", A{"deploymentId": id})
		base := d0.At("branch.head.commitId")
		b := wgBytes("banner v2")
		cfg := wgWithContent(DepInput(t, api, id), b)
		banner := d0.At("draft.files.0.content")
		t1 := Now()
		// the stage: the head's banner is present, the new one missing
		st := api.Call(t, "createStage", A{"body": A{"blobs": L{A{"sha256": wgShaOf(b), "sizeBytes": len(b)}, banner}}})
		stageID := st.At("stageId").Str()
		Eq(t, st.At("present"), 1)
		Eq(t, st.At("missing"), L{A{"sha256": wgShaOf(b), "sizeBytes": len(b)}})
		True(t, wgStageDoc(t, stageID))
		// sha mismatch → 422; present blob → staged false
		api.Rejects(t, "putStageBlob", A{"stageId": stageID, "sha256": wgShaOf(b), "body": wgBytes("other")}, "invalid", 422)
		r := api.Call(t, "putStageBlob", A{"stageId": stageID, "sha256": wgShaOf(b), "body": b})
		Eq(t, r.At("staged"), true)
		Eq(t, wgStagedObjects(t, stageID), L{"staging/" + stageID + "/" + wgHex(wgShaOf(b))})
		Eq(t, wgInStore(t, wgShaOf(b)), false, "nothing in the store before the commit")
		// the blob is not readable through the store API before the commit
		api.Rejects(t, "getBlob", A{"sha256": wgShaOf(b)}, "not-found", 404)
		c := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "new banner", "baseCommitId": base, "config": cfg, "stageId": stageID}})
		Eq(t, c.At("parentId"), base)
		Eq(t, wgInStore(t, wgShaOf(b)), true)
		Eq(t, wgStagedObjects(t, stageID), L{})
		Eq(t, wgStageDoc(t, stageID), false)
		got := api.Fetch(t, "getBlob", A{"sha256": wgShaOf(b)})
		Eq(t, string(got.Body), string(b))
		// the next stage for the same blob: present, nothing to upload
		st2 := api.Call(t, "createStage", A{"body": A{"blobs": L{A{"sha256": wgShaOf(b), "sizeBytes": len(b)}}}})
		Eq(t, st2.At("missing").Len(), 0)
		api.Call(t, "deleteStage", A{"stageId": st2.At("stageId")})
		Eq(t, wgStageDoc(t, st2.At("stageId").Str()), false)
		// audit: no blob / upload / stage entries for this person; one commit entry
		au := api.Call(t, "listAuditLog", A{"query": A{"actor": admin.UID}})
		after := J{Plain(au.At("items").Filter(func(x J) bool { return x.At("ts").Str() >= t1 }))}
		Eq(t, after.Pluck("op"), L{"commitDeployment"}, "one entry: the commit")
		commits := api.Call(t, "listAuditLog", A{"query": A{"target": "deployments/" + id, "action": "deployment.committed"}}).At("items").Filter(func(x J) bool { return x.At("data.commitId").V == c.At("commitId").V })
		Eq(t, len(commits), 1)
	})

	t.Run("template commit with staged blob; missing staged blob → 422 V_BLOB_MISSING (no commit, stage dropped); no stageId → 422", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		templateID := HelloDeployment(t, admin).TemplateID
		api := admin.Real
		t0 := api.Call(t, "getTemplate", A{"templateId": templateID})
		base := t0.At("branch.head.commitId")
		b := wgBytes("tpl banner")
		cfg := wgWithContent(TplInput(t, api, templateID), b)
		need := A{"sha256": wgShaOf(b), "sizeBytes": len(b)}
		// no stage and not stored → 422
		e0 := api.Rejects(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "x", "baseCommitId": base, "config": cfg}}, "invalid", 422)
		True(t, e0.Details.At("errors").FindF(func(x J) bool {
			return x.At("code").Str() == "V_BLOB_MISSING" && strings.HasPrefix(x.At("path").Str(), "files[")
		}).Exists())
		Eq(t, e0.Details.At("missing"), L{need})
		// a stage without the blob uploaded → 422, the stage is dropped
		st := api.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		api.Rejects(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "x", "baseCommitId": base, "config": cfg, "stageId": st.At("stageId")}}, "invalid", 422)
		Eq(t, wgStageDoc(t, st.At("stageId").Str()), false)
		Eq(t, api.Call(t, "getTemplate", A{"templateId": templateID}).At("branch.head.commitId"), base)
		// a wrong size in the config vs the staged bytes → 422
		st1 := api.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		api.Call(t, "putStageBlob", A{"stageId": st1.At("stageId"), "sha256": wgShaOf(b), "body": b})
		bad := cfg.Clone()
		bad.At("files.0.content").Map()["sizeBytes"] = len(b) + 1
		api.Rejects(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "x", "baseCommitId": base, "config": bad, "stageId": st1.At("stageId")}}, "invalid", 422)
		Eq(t, wgStagedObjects(t, st1.At("stageId").Str()), L{})
		Eq(t, wgInStore(t, wgShaOf(b)), false)
		// the real thing
		st2 := api.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		api.Call(t, "putStageBlob", A{"stageId": st2.At("stageId"), "sha256": wgShaOf(b), "body": b})
		c := api.Call(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "tpl banner", "baseCommitId": base, "config": cfg, "stageId": st2.At("stageId")}})
		Eq(t, c.At("seq"), t0.At("branch.head.seq").Num()+1)
		Eq(t, wgInStore(t, wgShaOf(b)), true)
		Eq(t, wgStageDoc(t, st2.At("stageId").Str()), false)
	})

	t.Run("failure after staging (validation error, fault after-verify) deletes the staged blobs and writes no commit", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		api := admin.Real
		base := api.Call(t, "getDeployment", A{"deploymentId": id}).At("branch.head.commitId")
		b := wgBytes("fail")
		sha := wgShaOf(b)
		need := A{"sha256": sha, "sizeBytes": len(b)}
		// validation error (an unknown variable reference / bad slug) after the stage was filled
		st := api.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		stageID := st.At("stageId").Str()
		api.Call(t, "putStageBlob", A{"stageId": stageID, "sha256": sha, "body": b})
		bad := wgWithContent(DepInput(t, api, id), b)
		wgPush(bad, "variables", A{"variableSlug": "BAD SLUG", "label": "x", "description": "", "value": "", "required": false})
		api.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "bad", "baseCommitId": base, "config": bad, "stageId": stageID}}, "invalid", 422)
		Eq(t, wgStagedObjects(t, stageID), L{})
		Eq(t, wgStageDoc(t, stageID), false)
		Eq(t, wgInStore(t, sha), false)
		// fault after verify (a crash before promotion)
		st2 := api.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		api.Call(t, "putStageBlob", A{"stageId": st2.At("stageId"), "sha256": sha, "body": b})
		res := wgCommitRaw(t, admin.Bearer, "/v1/deployments/"+id+"/commits", A{"message": "crash", "baseCommitId": base, "config": wgWithContent(DepInput(t, api, id), b), "stageId": st2.At("stageId")}, "after-verify")
		Eq(t, res.Status, 500)
		Eq(t, wgStagedObjects(t, st2.At("stageId").Str()), L{})
		Eq(t, wgInStore(t, sha), false)
		Eq(t, api.Call(t, "getDeployment", A{"deploymentId": id}).At("branch.head.commitId"), base)
	})

	t.Run("crash after promotion: no commit visible, head unchanged; retry finds the blob present and commits", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		api := admin.Real
		d0 := api.Call(t, "getDeployment", A{"deploymentId": id})
		base := d0.At("branch.head.commitId")
		count0 := d0.At("branch.commitCount")
		b := wgBytes("crash")
		sha := wgShaOf(b)
		need := A{"sha256": sha, "sizeBytes": len(b)}
		cfg := wgWithContent(DepInput(t, api, id), b)
		st := api.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		api.Call(t, "putStageBlob", A{"stageId": st.At("stageId"), "sha256": sha, "body": b})
		res := wgCommitRaw(t, admin.Bearer, "/v1/deployments/"+id+"/commits", A{"message": "crash", "baseCommitId": base, "config": cfg, "stageId": st.At("stageId")}, "after-promote")
		Eq(t, res.Status, 500)
		d1 := api.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, d1.At("branch.head.commitId"), base, "no commit visible")
		Eq(t, d1.At("branch.commitCount"), count0)
		Eq(t, api.Call(t, "listDeploymentCommits", A{"deploymentId": id}).At("items.0.commitId"), base)
		Eq(t, wgStageDoc(t, st.At("stageId").Str()), false)
		Eq(t, wgInStore(t, sha), true, "promoted (unreferenced) — harmless")
		// retry: present → nothing to upload; the commit works without re-sending bytes
		st2 := api.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		Eq(t, st2.At("missing").Len(), 0)
		c := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "retry", "baseCommitId": base, "config": cfg, "stageId": st2.At("stageId")}})
		Eq(t, c.At("parentId"), base)
		Eq(t, wgStageDoc(t, st2.At("stageId").Str()), false)
	})

	t.Run("409 stale with staged blobs: stage deleted, retry with a new stage on the new head succeeds", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		api := admin.Real
		base := api.Call(t, "getDeployment", A{"deploymentId": id}).At("branch.head.commitId")
		// someone else moves the head
		other := DepInput(t, api, id)
		other.At("variables").Find("variableSlug", "theme").Map()["value"] = "red"
		moved := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "other", "baseCommitId": base, "config": other}})
		b := wgBytes("stale")
		sha := wgShaOf(b)
		need := A{"sha256": sha, "sizeBytes": len(b)}
		st := api.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		stageID := st.At("stageId").Str()
		api.Call(t, "putStageBlob", A{"stageId": stageID, "sha256": sha, "body": b})
		cfg := wgWithContent(DepInput(t, api, id), b)
		e := api.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "mine", "baseCommitId": base, "config": cfg, "stageId": stageID}}, "stale", 409)
		Eq(t, e.Details.At("head.commitId"), moved.At("commitId"))
		Eq(t, wgStagedObjects(t, stageID), L{})
		Eq(t, wgStageDoc(t, stageID), false)
		Eq(t, wgInStore(t, sha), false)
		st2 := api.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		Eq(t, st2.At("missing").Len(), 1)
		api.Call(t, "putStageBlob", A{"stageId": st2.At("stageId"), "sha256": sha, "body": b})
		c := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "mine again", "baseCommitId": moved.At("commitId"), "config": cfg, "stageId": st2.At("stageId")}})
		Eq(t, c.At("parentId"), moved.At("commitId"))
	})

	t.Run("another person's stage → 404; expired stage → 410; multi-part upload into a stage is not audited", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		other := NewPerson(t, "admin")
		b := wgBytes("owner")
		sha := wgShaOf(b)
		need := A{"sha256": sha, "sizeBytes": len(b)}
		st := admin.Real.Call(t, "createStage", A{"body": A{"blobs": L{need}}})
		stageID := st.At("stageId").Str()
		other.Real.Rejects(t, "putStageBlob", A{"stageId": stageID, "sha256": sha, "body": b}, "not-found", 404)
		other.Real.Rejects(t, "deleteStage", A{"stageId": stageID}, "not-found", 404)
		True(t, wgStageDoc(t, stageID))
		// a commit naming someone else's stage must not drop it
		depID := HelloDeployment(t, other).ID()
		base := other.Real.Call(t, "getDeployment", A{"deploymentId": depID}).At("branch.head.commitId")
		other.Real.Rejects(t, "commitDeployment", A{"deploymentId": depID, "body": A{"message": "x", "baseCommitId": base, "config": wgWithContent(DepInput(t, other.Real, depID), b), "stageId": stageID}}, "not-found", 404)
		True(t, wgStageDoc(t, stageID), "the owner's stage survives")
		// multi-part (createUpload with stageId) → into the stage, no audit entry
		t2 := Now()
		up := admin.Real.Call(t, "createUpload", A{"body": A{"purpose": "blob", "sizeBytes": len(b), "sha256": sha, "stageId": stageID}})
		admin.Real.Call(t, "putUploadPart", A{"uploadId": up.At("uploadId"), "index": "0", "body": b})
		done := admin.Real.Call(t, "completeUpload", A{"uploadId": up.At("uploadId")})
		Eq(t, done.At("blob.sha256"), sha)
		staged := "staging/" + stageID + "/" + wgHex(sha)
		Eq(t, wgStagedObjects(t, stageID), L{staged})
		Eq(t, wgInStore(t, sha), false)
		m, err := Bucket.B.Head(staged)
		NoErr(t, err)
		True(t, m != nil)
		Eq(t, m.Meta["opmSha256"], wgHex(sha))
		au := admin.Real.Call(t, "listAuditLog", A{"query": A{"actor": admin.UID}})
		after := J{Plain(au.At("items").Filter(func(x J) bool { return x.At("ts").Str() >= t2 }))}
		Eq(t, after.Pluck("op"), L{}, "staged uploads are not audited")
		// expiry → 410
		UpdateDoc(t, "stages/"+stageID, A{"expiresAt": ISO(time.Now().Add(-time.Second))})
		admin.Real.Rejects(t, "putStageBlob", A{"stageId": stageID, "sha256": sha, "body": b}, "gone", 410)
		admin.Real.Call(t, "deleteStage", A{"stageId": stageID})
		Eq(t, wgStagedObjects(t, stageID), L{})
	})

	t.Run("the daily job removes expired stages (§21 #117: the persist \"Save as new version\" is gone — content is committed)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		api := admin.Real
		// the daily job removes expired stages (doc + objects)
		st3 := api.Call(t, "createStage", A{"body": A{"blobs": L{A{"sha256": wgShaOf(wgBytes("x")), "sizeBytes": 1}}}})
		id := st3.At("stageId").Str()
		Bucket.Save(t, "staging/"+id+"/"+strings.Repeat("0", 64), "x")
		UpdateDoc(t, "stages/"+id, A{"expiresAt": ISO(time.Now().Add(-time.Second))})
		res := RunDaily(t)
		True(t, res.At("stagesExpired").Num() >= 1)
		Eq(t, wgStageDoc(t, id), false)
		Eq(t, wgStagedObjects(t, id), L{})
	})
}
