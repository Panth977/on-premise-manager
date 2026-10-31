package routes

import (
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

func init() {
	cloud.Handlers(map[string]cloud.Handler{"listTransfers": listTransfers, "createTransfer": createTransfer, "downloadTransfer": downloadTransfer, "deleteTransfer": deleteTransfer})
}

func listTransfers(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.transfers", "read"); err != nil {
		return nil, err
	}
	direction := c.Query.Get("direction")
	if direction != "to-site" && direction != "to-cloud" {
		return nil, cloud.E.Bad("direction", "V_ENUM", "direction must be to-site | to-cloud")
	}
	q, err := cloud.Col.Transfers(dep.Str("deploymentId")).Where("siteSlug", "==", c.Params["siteSlug"]).Where("direction", "==", direction).OrderBy("createdAt", "desc").Limit(500).Get()
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(q.Docs))
	for _, d := range q.Docs {
		items = append(items, d.Data())
	}
	return doc.Obj("items", items), nil
}

func createTransfer(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.transfers", "write"); err != nil {
		return nil, err
	}
	slug := c.Params["siteSlug"]
	depID := dep.Str("deploymentId")
	known := false
	for _, s := range dep.Map("draft").Arr("sites") {
		known = known || s.(*doc.Map).Val("siteSlug") == slug
	}
	if !known {
		a, err := cloud.Col.Agents(depID).Doc(slug).Get()
		if err != nil {
			return nil, err
		}
		known = a.Exists()
	}
	if !known {
		return nil, cloud.E.NotFound("site " + slug + " is not in the deployment")
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"fileName", "sizeBytes", "sha256", "note"}); err != nil {
		return nil, err
	}
	if err := core.ValidateTransferBody(b); err != nil {
		return nil, err
	}
	tRef := cloud.Col.Transfers(depID).Doc()
	uid := c.Account.UID
	upload := core.NewUpload(core.NewUploadOpts{Purpose: "transfer", DeploymentID: depID, TransferID: tRef.ID(), SizeBytes: b.Num("sizeBytes"), Sha256: b.Str("sha256"), CreatedBy: uid, Target: core.Paths.Transfer(depID, tRef.ID())})
	t := core.NewTransfer(core.NewTransferOpts{DeploymentID: depID, SiteSlug: slug, Direction: "to-site", Body: b, CreatedBy: uid, UploadID: upload.Str("uploadId")}).
		Set("transferId", tRef.ID()).Set("storagePath", core.Paths.Transfer(depID, tRef.ID()))
	depRef := cloud.Col.Deployments().Doc(depID)
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ds, err := tx.Get(depRef)
		if err != nil {
			return err
		}
		d := cloud.DecodeDeployment(ds.Data())
		tx.Create(tRef, t)
		tx.Create(cloud.Col.Uploads().Doc(upload.Str("uploadId")), upload)
		log := core.NewCloudLog(d.Str("deploymentId"), d)
		log.Add(tx, core.CloudEntry{
			Type:    "transfer.sent",
			Summary: "Sending " + t.Str("fileName") + " to site " + slug + " started (" + core.JSString(t.Val("sizeBytes")) + " bytes)",
			Data:    doc.Obj("stage", "started", "transferId", t.Val("transferId"), "direction", t.Val("direction"), "fileName", t.Val("fileName"), "sizeBytes", t.Val("sizeBytes"), "siteSlug", slug, "uploadId", upload.Val("uploadId")),
			Actor:   uid,
		})
		tx.Update(depRef, log.DepUpdate())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("transfer", t, "upload", core.UploadView(upload)), nil
}

func downloadTransfer(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.transfers", "read"); err != nil {
		return nil, err
	}
	depID := dep.Str("deploymentId")
	tRef := cloud.Col.Transfers(depID).Doc(c.Params["transferId"])
	s, err := tRef.Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("transfer not found")
	}
	t := s.Data()
	if t.Val("status") == "expired" {
		return nil, cloud.E.Gone("the transfer was deleted")
	}
	if t.Val("status") == "uploading" {
		return nil, cloud.E.NotFound("the transfer is still uploading")
	}
	if t.Val("direction") == "to-cloud" && t.Val("status") == "ready" {
		depRef := cloud.Col.Deployments().Doc(depID)
		err := cloud.DB().RunTransaction(func(tx *driver.Tx) error {
			ds, err := tx.Get(depRef)
			if err != nil {
				return err
			}
			ts, err := tx.Get(tRef)
			if err != nil {
				return err
			}
			cur := ts.Data()
			if cur.Val("status") != "ready" {
				return nil
			}
			d := cloud.DecodeDeployment(ds.Data())
			log := core.NewCloudLog(d.Str("deploymentId"), d)
			tx.Update(tRef, doc.Obj("status", "downloaded"))
			log.Add(tx, core.CloudEntry{
				Type:    "transfer.received",
				Summary: "File " + cur.Str("fileName") + " from site " + cur.Str("siteSlug") + " downloaded",
				Data:    doc.Obj("transferId", cur.Val("transferId"), "direction", cur.Val("direction"), "fileName", cur.Val("fileName"), "sizeBytes", cur.Val("sizeBytes"), "siteSlug", cur.Val("siteSlug")),
				Actor:   c.Account.UID,
			})
			tx.Update(depRef, log.DepUpdate())
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return core.ServeObject(t.Str("storagePath"), core.ServeOpts{Range: c.Headers["range"], FileName: t.Str("fileName"), Headers: map[string]string{"X-OPM-Sha256": cloud.HexOf(t.Str("sha256"))}})
}

func deleteTransfer(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.transfers", "write"); err != nil {
		return nil, err
	}
	depID := dep.Str("deploymentId")
	tRef := cloud.Col.Transfers(depID).Doc(c.Params["transferId"])
	depRef := cloud.Col.Deployments().Doc(depID)
	t, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		ts, err := tx.Get(tRef)
		if err != nil {
			return nil, err
		}
		if !ts.Exists() {
			return nil, cloud.E.NotFound("transfer not found")
		}
		cur := ts.Data()
		d := cloud.DecodeDeployment(ds.Data())
		errText := any("deleted")
		if cur.Val("status") == "expired" {
			errText = aFld(cur, "error")
		}
		tx.Update(tRef, doc.Obj("status", "expired", "error", errText))
		log := core.NewCloudLog(d.Str("deploymentId"), d)
		where := "from site " + cur.Str("siteSlug")
		if cur.Val("direction") == "to-site" {
			where = "to site " + cur.Str("siteSlug")
		}
		log.Add(tx, core.CloudEntry{
			Type:    "transfer.deleted",
			Summary: "Transfer " + cur.Str("fileName") + " (" + where + ") deleted",
			Data:    doc.Obj("transferId", cur.Val("transferId"), "direction", cur.Val("direction"), "fileName", cur.Val("fileName"), "sizeBytes", cur.Val("sizeBytes"), "siteSlug", cur.Val("siteSlug"), "previousStatus", cur.Val("status")),
			Actor:   c.Account.UID,
		})
		upd := doc.New().Spread(log.DepUpdate())
		if cur.Val("direction") == "to-site" && cur.Val("status") != "expired" {
			upd.Set("agentEpoch", d.Num("agentEpoch")+1)
		}
		tx.Update(depRef, upd)
		if cur.Truthy("uploadId") && cur.Val("status") == "uploading" {
			tx.Update(cloud.Col.Uploads().Doc(cur.Str("uploadId")), doc.Obj("status", "failed", "error", "the transfer was deleted"))
		}
		return cur, nil
	})
	if err != nil {
		return nil, err
	}
	_ = core.Remove(t.Str("storagePath"))
	return doc.Obj("deleted", true), nil
}
