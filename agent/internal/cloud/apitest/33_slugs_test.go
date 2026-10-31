package apitest

import (
	"testing"

	"opm/agent/internal/cloud/shared"
)

// Port of backend/test/33-slugs.test.ts.
func TestSlugs(t *testing.T) {
	t.Run("template + deployment slugs: derived, unique, requested, immutable, listed", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		api := admin.Real
		label := "Slug Plant " + Uniq("")
		t1 := api.Call(t, "createTemplate", A{"body": A{"label": label}})
		Eq(t, t1.At("slug"), shared.SlugifyLabel(label, "x-"))
		t2 := api.Call(t, "createTemplate", A{"body": A{"label": label}})
		Eq(t, t2.At("slug"), t1.At("slug").Str()+"-2", "same label → -2")
		want := Uniq("custom-")
		t3 := api.Call(t, "createTemplate", A{"body": A{"label": "Anything", "slug": want}})
		Eq(t, t3.At("slug"), want)
		e := api.Rejects(t, "createTemplate", A{"body": A{"label": "Other", "slug": want}}, "conflict", 409)
		Eq(t, e.Details.At("reason"), "slug-taken")
		api.Rejects(t, "createTemplate", A{"body": A{"label": "Bad", "slug": "Bad Slug"}}, "invalid", 422)
		p := api.Rejects(t, "patchTemplate", A{"templateId": t1.At("templateId"), "body": A{"ifVersion": 1, "slug": "x-new"}}, "invalid", 422)
		Eq(t, p.Details.At("errors.0.code"), "V_IMMUTABLE")
		list := api.Call(t, "listTemplates", nil)
		Eq(t, list.At("items").Find("templateId", t1.At("templateId")).At("slug"), t1.At("slug"))
		// a label starting with a digit
		t4 := api.Call(t, "createTemplate", A{"body": A{"label": "2026 " + Uniq("")}})
		Match(t, t4.At("slug"), `^t-2026-`)
		// deployments
		dl := "Dep " + Uniq("")
		d1 := api.Call(t, "createDeployment", A{"body": A{"label": dl, "templateId": nil, "tagSlugs": L{}, "managerAccountUids": L{}}})
		Eq(t, d1.At("deployment.slug"), shared.SlugifyLabel(dl, "x-"))
		d2 := api.Call(t, "createDeployment", A{"body": A{"label": dl, "templateId": nil, "tagSlugs": L{}, "managerAccountUids": L{}}})
		Eq(t, d2.At("deployment.slug"), d1.At("deployment.slug").Str()+"-2")
		api.Rejects(t, "createDeployment", A{"body": A{"label": "x", "slug": d1.At("deployment.slug"), "templateId": nil, "tagSlugs": L{}, "managerAccountUids": L{}}}, "conflict", 409)
		api.Rejects(t, "patchDeployment", A{"deploymentId": d1.At("deployment.deploymentId"), "body": A{"slug": "zz"}}, "invalid", 422)
		dlist := api.Call(t, "listDeployments", nil)
		Eq(t, dlist.At("items").Find("deploymentId", d1.At("deployment.deploymentId")).At("slug"), d1.At("deployment.slug"))
		// a deleted deployment keeps its slug reserved
		_, _ = api.Try("deleteDeployment", A{"deploymentId": d2.At("deployment.deploymentId"), "body": A{"confirm": dl}})
		d3 := api.Call(t, "createDeployment", A{"body": A{"label": dl, "templateId": nil, "tagSlugs": L{}, "managerAccountUids": L{}}})
		Eq(t, d3.At("deployment.slug"), d1.At("deployment.slug").Str()+"-3")
	})
}
