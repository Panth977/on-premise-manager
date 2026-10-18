package agent

import (
	"testing"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

// §21 #116 an update loads its images before any container uses them, and in branch mode no proposal holds its
// revision: the hourly image prune must still count them as referenced, else a container created late in the update
// fails with "image opm/cfg:…: not found".
func TestReferencedBlobsKeepsInFlightUpdate(t *testing.T) {
	a := newTestAgent(t)
	const cfg = "sha256:" + "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	const layer = "sha256:" + "1a7e1a7e1a7e1a7e1a7e1a7e1a7e1a7e1a7e1a7e1a7e1a7e1a7e1a7e1a7e1a7e"
	rev := model.Revision{RevID: "RevInFlight", Seq: 9, Catalog: model.RevisionCatalog{Images: map[string]model.ImageSpec{"code-shell:v3": {
		ServiceSlug: "code-shell", Version: "v3", ConfigDigest: cfg, Layers: []model.ImageLayer{{Digest: layer}},
	}}}}
	if a.referencedBlobs()[cfg[7:]] {
		t.Fatal("referenced before any update")
	}
	a.savePlan(&model.UpdatePlan{UpdateID: "UpdInFlight", Kind: "merge", Revision: rev})
	ref := a.referencedBlobs()
	if !ref[cfg[7:]] || !ref[layer[7:]] {
		t.Fatalf("an unfinished update's image is not referenced: %v", ref)
	}
	fin := nowTs()
	a.saveRun(&model.UpdateRun{UpdateID: "UpdInFlight", Kind: "merge", RevID: rev.RevID, FinishedAt: &fin})
	if a.referencedBlobs()[cfg[7:]] {
		t.Fatal("a finished update still holds its images")
	}
	// a cloud commit staged for a merge
	normalizeConfig(&rev.Config)
	normalizeCatalog(&rev.Catalog)
	rev.Hash, _ = canonical.HashCanonical(rev.Config)
	rev.CatalogHash, _ = canonical.HashCanonical(rev.Catalog)
	if err := a.putRevision(&rev); err != nil {
		t.Fatal(err)
	}
	a.markBranchRev(rev.RevID)
	if !a.referencedBlobs()[cfg[7:]] {
		t.Fatal("a staged cloud commit's image is not referenced")
	}
}
