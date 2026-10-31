package apitest

import (
	"testing"

	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/shared"
)

func toMap(t testing.TB, j J) *doc.Map {
	t.Helper()
	m, err := doc.ParseMap([]byte(j.String()))
	NoErr(t, err)
	return m
}

func commitMessageOK(m J) bool { return len(shared.CheckCommitMessage(m.V, "message")) == 0 }

// validateLive runs the config validator against the live catalog, like saveLevelIssues of the TS helpers.
func validateLive(t testing.TB, config J, mode, deploymentID string) []J {
	t.Helper()
	svc, err := DB.Collection("services").Get()
	NoErr(t, err)
	imgs, err := DB.CollectionGroup("images").Get()
	NoErr(t, err)
	pages, err := DB.Collection("notFoundPages").Select().Get()
	NoErr(t, err)
	services, images := doc.New(), doc.New()
	for _, d := range svc.Docs {
		services.Set(d.ID(), doc.New().Set("label", core.ServiceView(d.Data(), d.ID()).Str("label")))
	}
	for _, d := range imgs.Docs {
		parent := d.Ref().Parent().Parent()
		if parent == nil || parent.Parent().ID() != "services" {
			continue
		}
		i, err := core.ImageView(d.Data(), d.ID(), parent.ID())
		NoErr(t, err)
		images.Set(i.Str("serviceSlug")+":"+i.Str("version"), core.CatalogImage(i))
	}
	detected := doc.New()
	if mode == "draft" {
		ag, err := DB.Collection("deployments/" + deploymentID + "/agents").Get()
		NoErr(t, err)
		for _, a := range ag.Docs {
			if h := a.Data().Str("advertiseHost"); h != "" {
				detected.Set(a.ID(), h)
			}
		}
	}
	ids := []any{}
	for _, d := range pages.Docs {
		ids = append(ids, d.ID())
	}
	id := deploymentID
	if mode == "template" {
		id = "template"
	}
	ctx := doc.New().Set("mode", mode).Set("deploymentId", id).Set("services", services).Set("images", images).Set("notFoundPageIds", ids).Set("detectedHosts", detected)
	is, err := shared.ValidateConfig(toMap(t, config), ctx)
	if err != nil {
		return nil // the TS helper's try/catch: an invalid shape is not a save-level result
	}
	return JOf(is.List()).Arr()
}
