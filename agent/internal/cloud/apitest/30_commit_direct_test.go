package apitest

import (
	"testing"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

func wgTheme(c J, value string) {
	c.At("variables").Find("variableSlug", "theme").Map()["value"] = value
}

// wgPush is array.push on a key of an editable object.
func wgPush(c J, key string, item any) {
	m := c.Map()
	m[key] = append(m[key].([]any), Plain(item))
}

// wgDocMap turns a J object into a doc.Map (what the Go ports of the shared functions take).
func wgDocMap(t testing.TB, j J) *doc.Map {
	t.Helper()
	m, err := doc.ParseMap([]byte(j.String()))
	NoErr(t, err)
	return m
}

func wgAsCfg(c J) J {
	o := c.Clone()
	secrets := L{}
	for _, s := range o.At("secrets").Arr() {
		d := JOf(dropKeys(s, "plainValue", "generateNow"))
		d.Map()["value"], d.Map()["rotatedAt"] = nil, nil
		secrets = append(secrets, d.V)
	}
	o.Map()["secrets"] = secrets
	return o
}

// wgRebase is rebaseChanges(asCfg(base), asCfg(mine), asCfg(head)).
func wgRebase(t testing.TB, base, mine, head J) J {
	t.Helper()
	return JOf(shared.RebaseChanges(wgDocMap(t, wgAsCfg(base)), wgDocMap(t, wgAsCfg(mine)), wgDocMap(t, wgAsCfg(head)), nil, doc.New()))
}

// Port of backend/test/30-commit-direct.test.ts.
func TestCommitDirect(t *testing.T) {
	t.Run("deployment commit: { message, baseCommitId, config }; secrets set / clear / generate recorded by slug only; V_NO_CHANGE; permissions", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		api := admin.Real
		d0 := api.Call(t, "getDeployment", A{"deploymentId": id})
		base := d0.At("branch.head.commitId").Str()
		before := api.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "api-token"})
		// required body fields
		api.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "x", "config": DepInput(t, api, id)}}, "invalid", 422)
		api.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "x", "baseCommitId": base}}, "invalid", 422)
		api.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "", "baseCommitId": base, "config": DepInput(t, api, id)}}, "invalid", 422)
		// nothing changed → V_NO_CHANGE
		nc := api.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "same", "baseCommitId": base, "config": DepInput(t, api, id)}}, "invalid", 422)
		True(t, nc.Details.At("errors").Find("code", "V_NO_CHANGE").Exists())
		// a variable + a typed secret value + a new secret with a generate rule → one commit
		c1in := DepInput(t, api, id)
		wgTheme(c1in, "yellow")
		c1in.At("secrets.0").Map()["plainValue"] = "typed-value-123"
		wgPush(c1in, "secrets", A{"secretSlug": "gen-later", "label": "Gen", "description": "", "required": false, "generate": A{"charset": "alnum", "length": 16}})
		wgPush(c1in, "secrets", A{"secretSlug": "manual", "label": "Manual", "description": "", "required": false, "generate": nil, "plainValue": "m-1"})
		c1 := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "yellow + token", "baseCommitId": base, "config": c1in}})
		Eq(t, c1.At("kind"), "change")
		Eq(t, c1.At("parentId"), base)
		Eq(t, c1.At("data.secretValues"), A{"set": L{"api-token", "manual"}, "generated": L{"gen-later"}, "cleared": L{}})
		NotContains(t, c1.At("data"), "typed-value-123", "no value in commit data")
		Eq(t, api.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "api-token"}).At("value"), "typed-value-123")
		Match(t, api.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "gen-later"}).At("value"), `^[A-Za-z0-9]{16}$`)
		// the copy (draft) = the head; no uncommitted count
		d1 := api.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, d1.At("branch.head.commitId"), c1.At("commitId"))
		Eq(t, d1.At("branch").Has("uncommitted"), false)
		Eq(t, d1.At("draft.variables").Find("variableSlug", "theme").At("value"), "yellow")
		// plainValue absent keeps the value; generateNow regenerates; null clears
		c2in := DepInput(t, api, id)
		c2in.At("secrets").Find("secretSlug", "gen-later").Map()["generateNow"] = true
		wgTheme(c2in, "blue")
		c2 := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "regenerate", "baseCommitId": c1.At("commitId"), "config": c2in}})
		Eq(t, c2.At("data.secretValues"), A{"set": L{}, "generated": L{"gen-later"}, "cleared": L{}})
		Eq(t, api.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "api-token"}).At("value"), "typed-value-123", "absent plainValue = keep")
		c3in := DepInput(t, api, id)
		c3in.At("secrets").Find("secretSlug", "manual").Map()["plainValue"] = nil
		c3 := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "clear manual", "baseCommitId": c2.At("commitId"), "config": c3in}})
		Eq(t, c3.At("data.secretValues"), A{"set": L{}, "generated": L{}, "cleared": L{"manual"}})
		api.Rejects(t, "revealSecret", A{"deploymentId": id, "secretSlug": "manual"}, "not-found", 404)
		// activity + audit carry the slugs, never a value
		acts := api.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "branch.committed"}})
		a1 := acts.At("items").Find("data.commitId", c1.At("commitId"))
		Eq(t, a1.At("data.secretValues"), A{"set": L{"api-token", "manual"}, "generated": L{"gen-later"}, "cleared": L{}})
		NotContains(t, acts.At("items"), "typed-value-123")
		au := api.Call(t, "listAuditLog", A{"query": A{"target": "deployments/" + id, "action": "deployment.committed"}})
		e1 := au.At("items").Find("data.commitId", c1.At("commitId"))
		Eq(t, e1.At("data.secretValues"), A{"set": L{"api-token", "manual"}, "generated": L{"gen-later"}, "cleared": L{}})
		NotContains(t, au.At("items"), "typed-value-123")
		NotContains(t, au.At("items"), before.At("value").Str())
		// permissions: publish needed; secret changes need deployment.secrets.write
		writer := NewPerson(t, L{A{"resource": "deployments", "actions": L{"read"}, "scope": A{"kind": "all"}}, A{"resource": "deployment.config", "actions": L{"read", "write"}, "scope": A{"kind": "all"}}})
		w4 := DepInput(t, api, id)
		wgTheme(w4, "red")
		writer.Real.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "red", "baseCommitId": c3.At("commitId"), "config": w4}}, "forbidden", 403)
		publisher := NewPerson(t, L{A{"resource": "deployments", "actions": L{"read"}, "scope": A{"kind": "all"}}, A{"resource": "deployment.config", "actions": L{"read", "write", "publish"}, "scope": A{"kind": "all"}}})
		s4 := w4.Clone()
		s4.At("secrets.0").Map()["plainValue"] = "nope"
		publisher.Real.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "secret", "baseCommitId": c3.At("commitId"), "config": s4}}, "forbidden", 403)
		c4 := publisher.Real.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "red", "baseCommitId": c3.At("commitId"), "config": w4}})
		Eq(t, c4.At("data.secretValues"), Undefined, "no secret change → no secretValues")
	})

	t.Run("409 stale { reason: head-moved, head } → rebaseChanges (clean, then a conflict) → commit; templates: no values", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, templateID := h.ID(), h.TemplateID
		api := admin.Real
		base := api.Call(t, "getDeployment", A{"deploymentId": id}).At("branch.head.commitId").Str()
		baseCfg := DepInput(t, api, id)
		// browser A edits theme; browser B commits greeting meanwhile
		mine := baseCfg.Clone()
		wgTheme(mine, "yellow")
		other := baseCfg.Clone()
		other.At("variables").Find("variableSlug", "greeting").Map()["value"] = "Hi from B"
		cb := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "B: greeting", "baseCommitId": base, "config": other}})
		st := api.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "A: theme", "baseCommitId": base, "config": mine}}, "stale", 409)
		Eq(t, st.Details.At("reason"), "head-moved")
		Eq(t, st.Details.At("head.commitId"), cb.At("commitId"))
		// rebase in the "browser" with the shared engine: clean → commit
		head := DepInput(t, api, id)
		rb := wgRebase(t, baseCfg, mine, head)
		Eq(t, rb.At("clean"), true)
		rebased := rb.At("config").Clone()
		rebased.Map()["secrets"] = head.At("secrets").V
		ca := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "A: theme", "baseCommitId": cb.At("commitId"), "config": rebased}})
		d := api.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, d.At("draft.variables").Find("variableSlug", "theme").At("value"), "yellow")
		Eq(t, d.At("draft.variables").Find("variableSlug", "greeting").At("value"), "Hi from B")
		// a conflict: both change the theme
		mine2 := DepInput(t, api, id)
		wgTheme(mine2, "green")
		other2 := DepInput(t, api, id)
		wgTheme(other2, "red")
		api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "B: red", "baseCommitId": ca.At("commitId"), "config": other2}})
		api.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "A", "baseCommitId": ca.At("commitId"), "config": mine2}}, "stale", 409)
		head2 := DepInput(t, api, id)
		baseCfg2 := rebased.Clone()
		rc := wgRebase(t, baseCfg2, mine2, head2)
		Eq(t, rc.At("clean"), false)
		Eq(t, rc.At("conflicts").Pluck("key"), L{"variables/theme/value"})
		// templates: a secret value is refused; a stale base too
		tp := api.Call(t, "getTemplate", A{"templateId": templateID})
		ti := TplInput(t, api, templateID)
		ti.At("secrets.0").Map()["plainValue"] = "x"
		tv := api.Rejects(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "v", "baseCommitId": tp.At("branch.head.commitId"), "config": ti}}, "invalid", 422)
		Match(t, tv.Details, `V_TEMPLATE_SECRET_VALUE`)
		api.Rejects(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "v", "baseCommitId": ulid.New(), "config": TplInput(t, api, templateID)}}, "stale", 409)
	})

	t.Run("removed working-copy routes answer 410; at:\"working\" → 422; merges have no dirty check; patchTemplate keeps details", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, templateID := h.ID(), h.TemplateID
		api := admin.Real
		d := api.Call(t, "getDeployment", A{"deploymentId": id})
		H := map[string]string{"Authorization": admin.Bearer, "Content-Type": "application/json"}
		for _, x := range []struct {
			m, p string
			b    A
		}{
			{"PUT", "/v1/deployments/" + id + "/draft/variables", A{"ifRevision": d.At("draftVersion"), "items": d.At("draft.variables")}},
			{"PUT", "/v1/deployments/" + id + "/draft/sites/app", A{"ifRevision": d.At("draftVersion"), "site": d.At("draft.sites.0")}},
			{"POST", "/v1/deployments/" + id + "/secrets/api-token/regenerate", A{"ifRevision": d.At("draftVersion")}},
			{"POST", "/v1/deployments/" + id + "/working/discard", A{}},
			{"POST", "/v1/templates/" + templateID + "/working/discard", A{}},
		} {
			r := Raw(t, x.m, x.p, Opt{Headers: H, Body: []byte(JOf(x.b).String())})
			Eq(t, r.Status, 410, "%s %s → %d %s", x.m, x.p, r.Status, r.J)
			// assert.ok(a ?? b ?? true): only a present falsy hint fails
			hint := r.J.At("error.details.hint")
			if hint.IsNil() {
				hint = r.J.At("details.hint")
			}
			True(t, hint.IsNil() || hint.Truthy())
		}
		tp := api.Call(t, "getTemplate", A{"templateId": templateID})
		api.Rejects(t, "patchTemplate", A{"templateId": templateID, "body": A{"ifVersion": tp.At("version"), "variables": tp.At("variables")}}, "gone", 410)
		t2 := api.Call(t, "patchTemplate", A{"templateId": templateID, "body": A{"ifVersion": tp.At("version"), "label": "renamed " + Uniq("")}})
		Eq(t, t2.At("template.version"), tp.At("version").Num()+1)
		Eq(t, t2.At("template.branch.head.commitId"), tp.At("branch.head.commitId"), "details are not a commit")
		api.Rejects(t, "compareBranches", A{"body": A{"source": A{"branch": A{"kind": "deployment", "deploymentId": id}, "at": "working"}, "target": A{"kind": "deployment", "deploymentId": id}}}, "invalid", 422)
		// a merge is never refused for "uncommitted changes": template change → merge into the deployment
		ti := TplInput(t, api, templateID)
		wgTheme(ti, "red")
		api.Call(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "red", "baseCommitId": tp.At("branch.head.commitId"), "config": ti}})
		plan := api.Call(t, "compareBranches", A{"body": A{"source": A{"branch": A{"kind": "template", "templateId": templateID}, "at": "head"}, "target": A{"kind": "deployment", "deploymentId": id}}})
		Eq(t, plan.At("target.workingClean"), true)
		it := plan.At("items").Find("key", "variables/theme/value")
		m := api.Call(t, "merge", A{"body": A{"source": A{"branch": A{"kind": "template", "templateId": templateID}, "at": "head"}, "target": A{"kind": "deployment", "deploymentId": id}, "targetHead": plan.At("target.head.commitId"), "sourceCommit": plan.At("source.commit.commitId"), "choices": L{A{"key": it.At("key"), "take": "source"}}, "message": "take red"}})
		True(t, m.At("kind").Str() == "merge" || m.At("kind").Str() == "cherry-pick")
		// the deployment doc never carries branch.uncommitted
		Eq(t, Doc(t, "deployments/"+id).At("branch").Has("uncommitted"), false)
	})
}
