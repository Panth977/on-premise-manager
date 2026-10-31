package apitest

import (
	"testing"

	"opm/agent/internal/cloud/shared"
)

func wgAssertMasked(t testing.TB, label string, res J, plain string) {
	t.Helper()
	NotContains(t, res.String(), plain, "%s: the response carries a plain secret value", label)
	True(t, res.At("config.secrets").Len() > 0, "%s: secrets listed", label)
	for _, s := range res.At("config.secrets").Arr() {
		True(t, s.At("value").V == shared.MASK || s.At("value").IsNull(), "%s: %s is not masked", label, s.At("value"))
		_, isBool := s.At("isSet").V.(bool)
		True(t, isBool, "%s: isSet", label)
	}
}

// Port of backend/test/35-commit-mask.test.ts.
func TestCommitMask(t *testing.T) {
	t.Run("O1: commit / merge / revert / reset / template commit responses mask secret values", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, templateID := h.ID(), h.TemplateID
		plain := admin.API.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "api-token"}).At("value").Str()
		True(t, len(plain) >= 8)
		// a publisher WITHOUT deployment.secrets.read
		pub := NewPerson(t, L{
			A{"resource": "deployments", "actions": L{"read"}, "scope": A{"kind": "all"}},
			A{"resource": "deployment.config", "actions": L{"read", "write", "publish"}, "scope": A{"kind": "all"}},
			A{"resource": "templates", "actions": L{"read", "write"}, "scope": A{"kind": "all"}},
		})
		api := pub.Real
		d0 := api.Call(t, "getDeployment", A{"deploymentId": id})
		head0 := d0.At("branch.head.commitId")
		// commit
		ci := DepInput(t, api, id)
		wgTheme(ci, "yellow")
		c1 := api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "green", "baseCommitId": head0, "config": ci}})
		wgAssertMasked(t, "commitDeployment", c1, plain)
		// revert + reset
		dep := A{"kind": "deployment", "deploymentId": id}
		rv := api.Call(t, "revertCommit", A{"body": A{"branch": dep, "commitId": c1.At("commitId"), "message": "Revert green", "ifHead": c1.At("commitId")}})
		wgAssertMasked(t, "revertCommit", rv, plain)
		rs := api.Call(t, "resetToCommit", A{"body": A{"branch": dep, "commitId": c1.At("commitId"), "message": "Reset to green", "ifHead": rv.At("commitId")}})
		wgAssertMasked(t, "resetToCommit", rs, plain)
		// template commit → merge into the deployment
		tp := api.Call(t, "getTemplate", A{"templateId": templateID})
		ti := TplInput(t, api, templateID)
		wgTheme(ti, "red")
		tc := api.Call(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "purple", "baseCommitId": tp.At("branch.head.commitId"), "config": ti}})
		for _, s := range tc.At("config.secrets").Arr() {
			True(t, s.At("value").IsNull() || s.At("value").V == shared.MASK, "template commit masked")
		}
		source := A{"branch": A{"kind": "template", "templateId": templateID}, "at": "head"}
		plan := api.Call(t, "compareBranches", A{"body": A{"source": source, "target": dep}})
		it := plan.At("items").Find("key", "variables/theme/value")
		req := func(extra A) A {
			m := A{"source": source, "target": dep, "targetHead": plan.At("target.head.commitId"), "sourceCommit": plan.At("source.commit.commitId"), "choices": L{A{"key": it.At("key"), "take": "source"}}}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		pv := api.Call(t, "previewMerge", A{"body": req(nil)})
		NotContains(t, pv.String(), plain, "previewMerge masked")
		NotContains(t, plan.String(), plain, "compare masked")
		// UX: a preview with nothing picked says so
		none := api.Call(t, "previewMerge", A{"body": req(A{"choices": L{}})})
		Eq(t, none.At("validation.errors").Len(), 1)
		Match(t, none.At("validation.errors.0.message"), `nothing picked`)
		m := api.Call(t, "merge", A{"body": req(A{"message": "take purple"})})
		wgAssertMasked(t, "merge", m, plain)
		// the reads stay masked too
		det := api.Call(t, "getDeploymentCommit", A{"deploymentId": id, "commitId": m.At("commitId")})
		wgAssertMasked(t, "getDeploymentCommit", det.At("commit"), plain)
		// the value itself is untouched
		Eq(t, admin.API.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "api-token"}).At("value"), plain)
	})
}
