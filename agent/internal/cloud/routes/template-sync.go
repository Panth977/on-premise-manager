package routes

import (
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
)

func init() {
	cloud.Handlers(map[string]cloud.Handler{"getTemplateSyncStatus": tsGone, "getTemplateSyncPlan": tsGone, "previewTemplateSync": tsGone, "applyTemplateSync": tsGone, "unlinkTemplate": tsGone})
}

// tsGone: §21 #104: Template sync is replaced by Compare + cherry-pick between any two branches.
func tsGone(*cloud.Ctx) (any, error) {
	return nil, cloud.E.Gone("Template sync was replaced by branches (§21 #104): compare the deployment with its template and cherry-pick", doc.Obj("hint", `POST /v1/branches/compare { source: { branch: { kind: "template", templateId }, at: "head" }, target: { kind: "deployment", deploymentId } }`))
}
