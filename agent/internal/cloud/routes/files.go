package routes

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

func init() {
	cloud.Handlers(map[string]cloud.Handler{"listFiles": listFiles, "publishFile": publishFile, "fetchFileToCloud": fetchFileToCloud, "downloadFile": downloadFile, "unpublishFile": unpublishFile, "registerAgentFile": registerAgentFile, "reportAgentFileHeld": reportAgentFileHeld, "startAgentFileUpload": startAgentFileUpload, "requestAgentFileFetch": requestAgentFileFetch, "downloadAgentFile": downloadAgentFile, "unpublishAgentFile": unpublishAgentFile})
}

// fileValidateBody is validateFileBody: fileName 1–255 without / \ NUL; sizeBytes 1..FILE_MAX; sha256; note ≤ FILE_NOTE_MAX
// (null / absent = none). The result has fileName, sizeBytes, sha256, note.
func fileValidateBody(b *doc.Map) (*doc.Map, error) {
	v := &cloud.V{}
	fn, ok := b.StrOK("fileName")
	if n := doc.JSLen(fn); !ok || n < 1 || n > 255 || strings.ContainsAny(fn, "/\\\x00") || fn == "." || fn == ".." {
		v.Err("fileName", "V_FORMAT", `fileName must be 1–255 characters without / \ or NUL`)
	}
	if !aIsInt(aFld(b, "sizeBytes")) || b.Num("sizeBytes") < 1 {
		v.Err("sizeBytes", "V_RANGE", "sizeBytes must be an integer ≥ 1")
	}
	if s, ok := b.StrOK("sha256"); !ok || !shared.RE.Sha256.MatchString(s) {
		v.Err("sha256", "V_FORMAT", `sha256 must be "sha256:" + 64 hex`)
	}
	if n := aFld(b, "note"); !aNullish(n) {
		if s, ok := n.(string); !ok || doc.JSLen(s) > shared.FILE_NOTE_MAX {
			v.Err("note", "V_LENGTH", "note must be ≤ "+strconv.Itoa(shared.FILE_NOTE_MAX)+" characters")
		}
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	if b.Num("sizeBytes") > shared.FILE_MAX {
		return nil, cloud.E.TooLarge("files are at most " + strconv.Itoa(shared.FILE_MAX) + " bytes")
	}
	var note any
	if doc.Truthy(aFld(b, "note")) {
		note = b.Val("note")
	}
	return doc.Obj("fileName", fn, "sizeBytes", b.Val("sizeBytes"), "sha256", b.Val("sha256"), "note", note), nil
}

func fileIDOf(c *cloud.Ctx) (string, error) {
	id := c.Params["fileId"]
	if !core.RE_FILE_ID.MatchString(id) {
		return "", cloud.E.NotFound("file not found")
	}
	return id, nil
}

func fileActorEmail(b *doc.Map) (string, error) {
	s, ok := b.StrOK("actor")
	if !ok || !shared.RE.Email.MatchString(s) {
		return "", cloud.E.Bad("actor", "V_FORMAT", "actor (email) is required")
	}
	return s, nil
}

// fileRequestFetch is requestFetch: the cloud wants a copy: cloud.fetch from a holding site (listed in that site's
// fileFetches). Idempotent. requester "" = null.
func fileRequestFetch(deploymentID, fileID, requestedBy, requester string) (*doc.Map, error) {
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	fRef := cloud.Col.Files(deploymentID).Doc(fileID)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		fs, err := tx.Get(fRef)
		if err != nil {
			return nil, err
		}
		if !fs.Exists() {
			return nil, cloud.E.NotFound("file not found")
		}
		f := fs.Data()
		if f.Val("state") == "unpublished" {
			return nil, cloud.E.Gone("the file was unpublished")
		}
		if f.Val("state") == "uploading" {
			return nil, cloud.E.Conflict("the file is still uploading to the cloud")
		}
		cl := f.Map("cloud")
		if cl.Truthy("stored") {
			return core.FileView(f), nil
		}
		if open := cl.Map("fetch"); open != nil && !open.Truthy("error") && open.Truthy("from") {
			return core.FileView(f), nil // already asked
		}
		from, err := core.PickHolder(tx, deploymentID, f.Strs("sites"), requester)
		if err != nil {
			return nil, err
		}
		if from == "" {
			return nil, cloud.E.Conflict("no site holds a copy of the file")
		}
		fetch := core.NewFetch(requestedBy, from)
		dep := cloud.DecodeDeployment(ds.Data())
		if f.Truthy("_uploadId") {
			tx.Update(cloud.Col.Uploads().Doc(f.Str("_uploadId")), doc.Obj("status", "failed", "error", "the fetch was requested again"))
		}
		tx.Update(fRef, doc.Obj("cloud.fetch", fetch, "_uploadId", nil))
		tx.Update(depRef, doc.Obj("agentEpoch", dep.Num("agentEpoch")+1))
		return core.FileView(f.Shallow().Set("cloud", doc.Obj("stored", false, "fetch", fetch)).Set("_uploadId", nil)), nil
	})
}

// fileUnpublish is unpublish: tombstone, the open upload failed, the cloud copy deleted (a migrated file also expires its
// transfer). Idempotent.
func fileUnpublish(deploymentID, fileID string, actor any, email, origin string) (any, error) {
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	fRef := cloud.Col.Files(deploymentID).Doc(fileID)
	gone, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		fs, err := tx.Get(fRef)
		if err != nil {
			return nil, err
		}
		if !fs.Exists() {
			return nil, cloud.E.NotFound("file not found")
		}
		f := fs.Data()
		if f.Val("state") == "unpublished" {
			return nil, nil
		}
		var legacy *driver.DocSnap
		if f.Truthy("_legacyTransferId") {
			if legacy, err = tx.Get(cloud.Col.Transfers(deploymentID).Doc(f.Str("_legacyTransferId"))); err != nil {
				return nil, err
			}
		}
		dep := cloud.DecodeDeployment(ds.Data())
		log := core.NewCloudLog(deploymentID, dep)
		tx.Update(fRef, doc.Obj("state", "unpublished", "unpublishedAt", doc.NowISO(), "unpublishedBy", email, "cloud", doc.Obj("stored", false, "fetch", nil), "sites", []any{}, "_uploadId", nil))
		if f.Truthy("_uploadId") {
			tx.Update(cloud.Col.Uploads().Doc(f.Str("_uploadId")), doc.Obj("status", "failed", "error", "the file was unpublished"))
		}
		// §21 #100 agents that still use the transfer drop their copy too (deletedTransfersToSite)
		if legacy != nil && legacy.Exists() && legacy.Data().Val("status") != "expired" {
			tx.Update(legacy.Ref(), doc.Obj("status", "expired", "error", "unpublished"))
		}
		log.Add(tx, core.CloudEntry{Type: "file.unpublished", Summary: "File " + f.Str("fileName") + " unpublished", Data: doc.Obj("fileId", fileID, "fileName", f.Val("fileName")), Actor: actor, Origin: origin})
		tx.Update(depRef, doc.New().Spread(log.DepUpdate()).Set("agentEpoch", dep.Num("agentEpoch")+1))
		return f, nil
	})
	if err != nil {
		return nil, err
	}
	if gone != nil {
		_ = core.Remove(gone.Str("_storagePath"))
	}
	return doc.Obj("deleted", true), nil
}

func fileLoad(deploymentID, fileID string) (*doc.Map, error) {
	s, err := cloud.Col.Files(deploymentID).Doc(fileID).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("file not found")
	}
	return s.Data(), nil
}

// fileCloudCopy is cloudCopy: the cloud copy, or why there is none (410 unpublished, 409 not in the cloud yet).
func fileCloudCopy(f *doc.Map) error {
	if f.Val("state") == "unpublished" {
		return cloud.E.Gone("the file was unpublished")
	}
	if f.Val("state") == "uploading" || !f.Map("cloud").Truthy("stored") {
		return cloud.E.Conflict("the cloud has no copy of the file yet")
	}
	return nil
}

func listFiles(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.transfers", "read"); err != nil {
		return nil, err
	}
	u := c.Query.Get("unpublished")
	var q *driver.QuerySnap
	if u == "1" || u == "true" {
		q, err = cloud.Col.Files(dep.Str("deploymentId")).Get()
	} else {
		q, err = cloud.Col.Files(dep.Str("deploymentId")).Where("state", "in", []any{"uploading", "ready"}).Get()
	}
	if err != nil {
		return nil, err
	}
	items := make([]*doc.Map, 0, len(q.Docs))
	for _, d := range q.Docs {
		items = append(items, core.FileView(d.Data()))
	}
	// sortNewest (publishedAt and fileId are ASCII: byte order is the JS string order)
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if x, y := a.Str("publishedAt"), b.Str("publishedAt"); x != y {
			return x > y
		}
		return a.Str("fileId") > b.Str("fileId")
	})
	out := make([]any, len(items))
	for i, x := range items {
		out[i] = x
	}
	return doc.Obj("items", out), nil
}

func publishFile(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.transfers", "write"); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"fileName", "sizeBytes", "sha256", "note"}); err != nil {
		return nil, err
	}
	body, err := fileValidateBody(b)
	if err != nil {
		return nil, err
	}
	uid := c.Account.UID
	depID := dep.Str("deploymentId")
	fileID := ulid.New()
	target := core.Paths.SharedFile(depID, fileID)
	upload := core.NewUpload(core.NewUploadOpts{Purpose: "file", DeploymentID: depID, TransferID: fileID, SizeBytes: body.Num("sizeBytes"), Sha256: body.Str("sha256"), CreatedBy: uid, Target: target})
	f := doc.Obj("fileId", fileID, "fileName", body.Val("fileName"), "sizeBytes", body.Val("sizeBytes"), "sha256", body.Val("sha256"), "note", body.Val("note"), "publishedAt", doc.NowISO(), "publishedBy", aEmail(c), "origin", doc.Obj("kind", "cloud"), "state", "uploading", "unpublishedAt", nil, "unpublishedBy", nil, "cloud", doc.Obj("stored", false, "fetch", nil), "sites", []any{}, "_storagePath", target, "_uploadId", upload.Val("uploadId"))
	depRef := cloud.Col.Deployments().Doc(depID)
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ds, err := tx.Get(depRef)
		if err != nil {
			return err
		}
		d := cloud.DecodeDeployment(ds.Data())
		tx.Create(cloud.Col.Files(d.Str("deploymentId")).Doc(fileID), f)
		tx.Create(cloud.Col.Uploads().Doc(upload.Str("uploadId")), upload)
		log := core.NewCloudLog(d.Str("deploymentId"), d)
		log.Add(tx, core.CloudEntry{Type: "file.published", Summary: "File " + f.Str("fileName") + " published from the cloud (" + core.JSString(f.Val("sizeBytes")) + " bytes)", Data: doc.Obj("fileId", fileID, "fileName", f.Val("fileName"), "sizeBytes", f.Val("sizeBytes"), "origin", f.Val("origin")), Actor: uid})
		tx.Update(depRef, doc.New().Spread(log.DepUpdate()).Set("agentEpoch", d.Num("agentEpoch")+1))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("file", core.FileView(f), "upload", core.UploadView(upload)), nil
}

func fetchFileToCloud(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.transfers", "read"); err != nil {
		return nil, err
	}
	id, err := fileIDOf(c)
	if err != nil {
		return nil, err
	}
	return fileRequestFetch(dep.Str("deploymentId"), id, aEmail(c), "")
}

func downloadFile(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.transfers", "read"); err != nil {
		return nil, err
	}
	depID := dep.Str("deploymentId")
	id, err := fileIDOf(c)
	if err != nil {
		return nil, err
	}
	f, err := fileLoad(depID, id)
	if err != nil {
		return nil, err
	}
	if err := fileCloudCopy(f); err != nil {
		return nil, err
	}
	// one activity per download (a resumed Range request that does not start at 0 is the same download)
	rng := c.Headers["range"]
	if rng == "" || strings.HasPrefix(doc.JSTrim(rng), "bytes=0-") {
		depRef := cloud.Col.Deployments().Doc(depID)
		err := cloud.DB().RunTransaction(func(tx *driver.Tx) error {
			ds, err := tx.Get(depRef)
			if err != nil {
				return err
			}
			d := cloud.DecodeDeployment(ds.Data())
			log := core.NewCloudLog(d.Str("deploymentId"), d)
			log.Add(tx, core.CloudEntry{Type: "file.downloaded", Summary: "File " + f.Str("fileName") + " downloaded from the cloud", Data: doc.Obj("fileId", f.Val("fileId"), "where", "cloud"), Actor: c.Account.UID})
			tx.Update(depRef, log.DepUpdate())
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return core.ServeObject(f.Str("_storagePath"), core.ServeOpts{Range: rng, FileName: f.Str("fileName"), Headers: map[string]string{"X-OPM-Sha256": cloud.HexOf(f.Str("sha256"))}})
}

func unpublishFile(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.transfers", "write"); err != nil {
		return nil, err
	}
	id, err := fileIDOf(c)
	if err != nil {
		return nil, err
	}
	return fileUnpublish(dep.Str("deploymentId"), id, c.Account.UID, aEmail(c), "cloud")
}

// ── agents (site key)

func registerAgentFile(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	body, err := fileValidateBody(doc.Obj("fileName", aFld(b, "fileName"), "sizeBytes", aFld(b, "sizeBytes"), "sha256", aFld(b, "sha256"), "note", aFld(b, "note")))
	if err != nil {
		return nil, err
	}
	v := &cloud.V{}
	fileID, ok := b.StrOK("fileId")
	if !ok || !core.RE_FILE_ID.MatchString(fileID) {
		v.Err("fileId", "V_FORMAT", "fileId must be 1–64 characters A–Z a–z 0–9 -")
	}
	var published int64
	if s, ok := b.StrOK("publishedAt"); ok {
		published, ok = doc.ParseISO(s)
		if !ok {
			v.Err("publishedAt", "V_FORMAT", "publishedAt must be an ISO time")
		}
	} else {
		v.Err("publishedAt", "V_FORMAT", "publishedAt must be an ISO time")
	}
	if s, ok := b.StrOK("publishedBy"); !ok || !shared.RE.Email.MatchString(s) {
		v.Err("publishedBy", "V_FORMAT", "publishedBy (email) is required")
	}
	via := b.Val("via")
	if via != "browser" && via != "path" {
		v.Err("via", "V_ENUM", "via must be browser | path")
	}
	if p := aFld(b, "sourcePath"); !aNullish(p) {
		if s, ok := p.(string); !ok || !strings.HasPrefix(s, "/") || doc.JSLen(s) > 4096 {
			v.Err("sourcePath", "V_FORMAT", "sourcePath must be null or an absolute path ≤ 4096")
		}
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	fRef := cloud.Col.Files(deploymentID).Doc(fileID)
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		fs, err := tx.Get(fRef)
		if err != nil {
			return nil, err
		}
		if fs.Exists() {
			cur := fs.Data()
			o := cur.Map("origin")
			mine := o.Val("kind") == "site" && o.Val("siteSlug") == siteSlug && cur.Val("sha256") == body.Val("sha256") && cur.Val("sizeBytes") == body.Val("sizeBytes")
			if !mine {
				return nil, cloud.E.Conflict("file " + fileID + " is already published with other content or from another place")
			}
			return core.FileView(cur), nil // idempotent (an unpublished one comes back as its tombstone)
		}
		var sourcePath any
		if via == "path" {
			sourcePath = aOr(b, "sourcePath", nil)
		}
		f := doc.Obj("fileId", fileID, "fileName", body.Val("fileName"), "sizeBytes", body.Val("sizeBytes"), "sha256", body.Val("sha256"), "note", body.Val("note"), "publishedAt", doc.ISO(time.UnixMilli(published)), "publishedBy", b.Val("publishedBy"), "origin", doc.Obj("kind", "site", "siteSlug", siteSlug, "via", via, "sourcePath", sourcePath), "state", "ready", "unpublishedAt", nil, "unpublishedBy", nil, "cloud", doc.Obj("stored", false, "fetch", nil), "sites", []any{siteSlug}, "_storagePath", core.Paths.SharedFile(deploymentID, fileID), "_uploadId", nil)
		dep := cloud.DecodeDeployment(ds.Data())
		log := core.NewCloudLog(deploymentID, dep)
		tx.Create(fRef, f)
		log.Add(tx, core.CloudEntry{Type: "file.published", Summary: "File " + f.Str("fileName") + " published from site " + siteSlug + " (" + core.JSString(f.Val("sizeBytes")) + " bytes)", Data: doc.Obj("fileId", f.Val("fileId"), "fileName", f.Val("fileName"), "sizeBytes", f.Val("sizeBytes"), "origin", f.Val("origin")), Actor: b.Val("publishedBy"), Origin: "agent"})
		tx.Update(depRef, doc.New().Spread(log.DepUpdate()).Set("agentEpoch", dep.Num("agentEpoch")+1))
		return core.FileView(f), nil
	})
}

func reportAgentFileHeld(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	held, ok := b.Val("held").(bool)
	if !ok {
		return nil, cloud.E.Bad("held", "V_TYPE", "held must be a boolean")
	}
	fileID, err := fileIDOf(c)
	if err != nil {
		return nil, err
	}
	fRef := cloud.Col.Files(deploymentID).Doc(fileID)
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ds, err := tx.Get(depRef)
		if err != nil {
			return err
		}
		fs, err := tx.Get(fRef)
		if err != nil {
			return err
		}
		if !fs.Exists() {
			return cloud.E.NotFound("file not found")
		}
		f := fs.Data()
		if f.Val("state") == "unpublished" {
			return nil // the site drops its copy on the tombstone
		}
		cur := f.Strs("sites")
		has := false
		for _, s := range cur {
			has = has || s == siteSlug
		}
		if has == held {
			return nil
		}
		sites := []string{}
		for _, s := range cur {
			if held || s != siteSlug {
				sites = append(sites, s)
			}
		}
		if held {
			sites = append(sites, siteSlug)
			sort.Strings(sites)
		}
		list := make([]any, len(sites))
		for i, s := range sites {
			list[i] = s
		}
		upd := doc.Obj("sites", list)
		fetch := f.Map("cloud").Map("fetch")
		// an open fetch follows the holders: a first holder is asked; the asked site dropped its copy → another one
		asked := fetch != nil && !f.Map("cloud").Truthy("stored")
		if asked {
			if held {
				asked = !fetch.Truthy("from")
			} else {
				asked = fetch.Val("from") == siteSlug
			}
		}
		if asked {
			var from any = siteSlug
			if !held {
				s, err := core.PickHolder(tx, deploymentID, sites, "")
				if err != nil {
					return err
				}
				from = nil
				if s != "" {
					from = s
				}
			}
			upd.Set("cloud.fetch", fetch.Shallow().Set("from", from).Set("startedAt", nil).Set("bytesDone", 0.0).Set("error", nil))
			if f.Truthy("_uploadId") && !held {
				tx.Update(cloud.Col.Uploads().Doc(f.Str("_uploadId")), doc.Obj("status", "failed", "error", "site "+siteSlug+" dropped its copy"))
				upd.Set("_uploadId", nil)
			}
		}
		dep := cloud.DecodeDeployment(ds.Data())
		tx.Update(fRef, upd)
		tx.Update(depRef, doc.Obj("agentEpoch", dep.Num("agentEpoch")+1))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("ok", true), nil
}

func startAgentFileUpload(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	fileID, err := fileIDOf(c)
	if err != nil {
		return nil, err
	}
	fRef := cloud.Col.Files(deploymentID).Doc(fileID)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		fs, err := tx.Get(fRef)
		if err != nil {
			return nil, err
		}
		if !fs.Exists() {
			return nil, cloud.E.NotFound("file not found")
		}
		f := fs.Data()
		if f.Val("state") == "unpublished" {
			return nil, cloud.E.Gone("the file was unpublished")
		}
		if f.Val("state") == "uploading" {
			return nil, cloud.E.Conflict("the file is still uploading to the cloud")
		}
		if f.Map("cloud").Truthy("stored") {
			return nil, cloud.E.Conflict("the cloud already has a copy")
		}
		fetch := f.Map("cloud").Map("fetch")
		if fetch == nil {
			return nil, cloud.E.Conflict("the cloud did not ask for a copy")
		}
		holds := false
		for _, s := range f.Strs("sites") {
			holds = holds || s == siteSlug
		}
		if fetch.Val("from") != siteSlug && !holds {
			return nil, cloud.E.Conflict("site " + siteSlug + " is not asked for a copy")
		}
		// resume the open upload of this site
		if f.Truthy("_uploadId") {
			us, err := tx.Get(cloud.Col.Uploads().Doc(f.Str("_uploadId")))
			if err != nil {
				return nil, err
			}
			if u := us.Data(); u != nil {
				exp, _ := doc.ParseISO(u.Str("expiresAt"))
				if u.Val("status") == "open" && u.Val("_creatorSite") == siteSlug && exp > time.Now().UnixMilli() {
					if !fetch.Truthy("startedAt") || fetch.Val("from") != siteSlug {
						tx.Update(fRef, doc.Obj("cloud.fetch", fetch.Shallow().Set("from", siteSlug).Set("startedAt", aOr(fetch, "startedAt", doc.NowISO())).Set("error", nil)))
					}
					return doc.Obj("upload", core.UploadView(u)), nil
				}
				if u.Val("status") == "open" {
					tx.Update(us.Ref(), doc.Obj("status", "failed", "error", "replaced by a new upload"))
				}
			}
		}
		upload := core.NewUpload(core.NewUploadOpts{Purpose: "file", DeploymentID: deploymentID, TransferID: fileID, SizeBytes: f.Num("sizeBytes"), Sha256: f.Str("sha256"), CreatedBy: fetch.Str("requestedBy"), CreatorSite: siteSlug, Target: f.Str("_storagePath")})
		tx.Create(cloud.Col.Uploads().Doc(upload.Str("uploadId")), upload)
		tx.Update(fRef, doc.Obj("cloud.fetch", fetch.Shallow().Set("from", siteSlug).Set("startedAt", doc.NowISO()).Set("bytesDone", 0.0).Set("error", nil), "_uploadId", upload.Val("uploadId")))
		return doc.Obj("upload", core.UploadView(upload)), nil
	})
}

func requestAgentFileFetch(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if _, err := fileActorEmail(b); err != nil {
		return nil, err
	}
	id, err := fileIDOf(c)
	if err != nil {
		return nil, err
	}
	return fileRequestFetch(deploymentID, id, "site:"+siteSlug, siteSlug)
}

func downloadAgentFile(c *cloud.Ctx) (any, error) {
	id, err := fileIDOf(c)
	if err != nil {
		return nil, err
	}
	f, err := fileLoad(c.Agent.DeploymentID, id)
	if err != nil {
		return nil, err
	}
	if err := fileCloudCopy(f); err != nil {
		return nil, err
	}
	relayed := ""
	if c.Agent.RelayedBy != nil {
		relayed = *c.Agent.RelayedBy
	}
	return core.ServeObject(f.Str("_storagePath"), core.ServeOpts{Range: c.Headers["range"], FileName: f.Str("fileName"), Headers: map[string]string{"X-OPM-Sha256": cloud.HexOf(f.Str("sha256"))}, NoRedirect: core.NoRedirectFor(c.Headers, relayed)})
}

func unpublishAgentFile(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	actor, err := fileActorEmail(b)
	if err != nil {
		return nil, err
	}
	id, err := fileIDOf(c)
	if err != nil {
		return nil, err
	}
	return fileUnpublish(c.Agent.DeploymentID, id, actor, actor, "agent")
}
