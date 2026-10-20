package agent

// §21 #105 (agent 0.14.1, docs-v2/branches-commits.md §15 J11 / J12): ONE action — Commit. GUI edits live in the
// browser; POST /_opm/api/branch/commit { message, baseCommitId, config } validates them, stages the result and runs
// the existing apply engine on this site (LOCK → SNAPSHOT → APPLY → VERIFY → COMMIT, rollback on failure). The local
// commit (kind change) is written only at COMMIT (commitBranchAt); a failed apply writes no commit and the browser keeps
// the changes. Refused while changes made outside the agent (drift) are uncommitted.

import (
	"net/http"
	"sort"

	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// commitEdits is the GUI-edits mode of handleCommitLocal (message already checked, no lock held).
func (a *Agent) commitEdits(r *http.Request, rc *reqCtx, req *model.LocalCommitRequest) (any, int, *httpError) {
	if !req.BaseCommitID.Set {
		return nil, 0, invalid("baseCommitId is required with config (the head the edits were made on)", []model.ValidationIssue{{Path: "baseCommitId", Code: "V_REQUIRED", Message: "baseCommitId is required", Severity: "error"}})
	}
	hr := a.headRevision()
	var base *string
	if req.BaseCommitID.Value != nil {
		base = req.BaseCommitID.Value
	}
	if hr == nil || base == nil || *base != hr.RevID {
		if hr == nil {
			return nil, 0, herr(model.ErrStale, "this site has no head yet; merge the cloud commit first", map[string]any{"reason": model.ReasonHeadMoved, "head": nil})
		}
		return nil, 0, a.staleHead("this site's head moved since the edits were made — rebase them and commit again")
	}
	a.rescanDriftNow(r.Context())
	if u := a.uncommittedNow(); u.blocking() > 0 {
		return nil, 0, driftErr(u)
	}
	rev, data, he := a.editRevision(hr, *req.Config, rc.actor, req.Message)
	if he != nil {
		return nil, 0, he
	}
	// §21 #107: every new blob must be stored or staged (size + sha256); staged blobs are promoted into the blob store
	// BEFORE the engine stages anything (a failed apply leaves them as unreferenced content-addressed data)
	if e := a.promoteStage(req.StageID, revConfig(hr), &rev.Config); e != nil {
		return nil, 0, e
	}
	if e := a.stageRevisionNow(r.Context(), rev); e != nil {
		return nil, 0, e
	}
	meta := a.commitMetaFor(rev, "change", req.Message, rc.actor, nil, data)
	run, he := a.startBranchApply(map[string]model.UpdatePlanTarget{a.site: {Revision: *rev, FromRevID: rev.ParentRevID}}, map[string]model.CommitMeta{a.site: meta}, &mergeRun{Actor: rc.actor}, rc.actor)
	if he != nil {
		return nil, 0, he
	}
	return model.LocalCommitResponse{Run: run}, 202, nil
}

// editRevision builds + checks the revision of GUI edits on the head: local-edit rules, agent validation,
// V_NO_CHANGE; data.secretValues = the secret value changes (slugs only).
func (a *Agent) editRevision(hr *model.Revision, in model.ConfigInput, actor, message string) (*model.Revision, model.CommitData, *httpError) {
	head := revConfig(hr)
	cfg := buildLocalConfig(head, in)
	issues := validate.CheckLocalEdit(head, &cfg)
	for _, x := range issues {
		if x.Severity == "error" {
			return nil, model.CommitData{}, invalid("this edit is not allowed on the site", issues)
		}
	}
	// §21 #113: an edit may use any image this site knows (head, recent cloud commits, drift-adopted, site images)
	full := a.knownCatalog(hr)
	is := validate.ValidateConfig(&cfg, a.validationContext(&full))
	cat := pruneCatalog(&cfg, full)
	var errs []model.ValidationIssue
	for _, x := range is.List {
		if x.Severity == "error" {
			errs = append(errs, x)
		}
	}
	if len(errs) > 0 {
		return nil, model.CommitData{}, invalid("the edited configuration is not valid: "+errs[0].Message, errs)
	}
	if e := a.contentDriftConflict(&cfg); e != nil { // §21 #117 d
		return nil, model.CommitData{}, e
	}
	rev := a.newBranchRevision(cfg, cat, actor, message, "change")
	if rev.Hash == hr.Hash {
		return nil, model.CommitData{}, invalid("nothing to commit: the edits equal the head", []model.ValidationIssue{{Path: "config", Code: "V_NO_CHANGE", Message: "the edits equal the head", Severity: "error"}})
	}
	var generated []string
	for _, s := range in.Secrets {
		if s.GenerateNow != nil && *s.GenerateNow && s.Generate != nil {
			generated = append(generated, s.SecretSlug)
		}
	}
	data := model.CommitData{}
	if sv := secretValueChanges(head.Secrets, rev.Config.Secrets, generated); sv.Any() {
		data.SecretValues = sv
	}
	return rev, data, nil
}

// secretValueChanges mirrors TS secretValueChanges (branch.ts): slugs whose plain value was set / generated / cleared.
func secretValueChanges(before, after []model.SecretDef, generated []string) *model.SecretValueChanges {
	empty := func(v *string) bool { return v == nil || *v == "" }
	gen := map[string]bool{}
	for _, g := range generated {
		gen[g] = true
	}
	prev := map[string]*string{}
	for i := range before {
		prev[before[i].SecretSlug] = before[i].Value
	}
	out := &model.SecretValueChanges{Set: []string{}, Generated: []string{}, Cleared: []string{}}
	for _, s := range after {
		b := prev[s.SecretSlug]
		switch {
		case empty(s.Value):
			if !empty(b) {
				out.Cleared = append(out.Cleared, s.SecretSlug)
			}
		case gen[s.SecretSlug]:
			out.Generated = append(out.Generated, s.SecretSlug)
		case b == nil || *b != *s.Value:
			out.Set = append(out.Set, s.SecretSlug)
		}
	}
	sort.Strings(out.Set)
	sort.Strings(out.Generated)
	sort.Strings(out.Cleared)
	return out
}
