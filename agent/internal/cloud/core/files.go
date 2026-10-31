package core

import (
	"regexp"
	"sort"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// RE_FILE_ID is RE_FILE_ID.
var RE_FILE_ID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// FileView is fileView.
func FileView(f *doc.Map) *doc.Map { return cloud.Pub(f) }

// FilesCloudItems is filesCloudItems: {files, fileFetches}.
func FilesCloudItems(deploymentID string) (*doc.Map, error) {
	cutoff := doc.ISO(time.Now().Add(-shared.FILE_TOMBSTONE_DAYS * 24 * time.Hour))
	live, err := cloud.Col.Files(deploymentID).Where("state", "in", []string{"uploading", "ready"}).Get()
	if err != nil {
		return nil, err
	}
	dead, err := cloud.Col.Files(deploymentID).Where("unpublishedAt", ">=", cutoff).Get()
	if err != nil {
		return nil, err
	}
	var all []*doc.Map
	for _, d := range append(append([]*driver.DocSnap{}, live.Docs...), dead.Docs...) {
		all = append(all, d.Data())
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if x, y := a.Str("publishedAt"), b.Str("publishedAt"); x != y {
			return x > y
		}
		return a.Str("fileId") > b.Str("fileId")
	})
	files, fetches := []any{}, []any{}
	for _, f := range all {
		files = append(files, FileView(f))
		cl := f.Map("cloud")
		if from := cl.Map("fetch").Val("from"); f.Val("state") == "ready" && !cl.Truthy("stored") && doc.Truthy(from) {
			fetches = append(fetches, doc.Obj("fileId", present(f, "fileId"), "from", from))
		}
	}
	return doc.Obj("files", files, "fileFetches", fetches), nil
}

// PickHolder is pickHolder: the holding site whose agent contacted the cloud most recently; "" = none. exclude "" = none.
func PickHolder(tx *driver.Tx, deploymentID string, sites []string, exclude string) (string, error) {
	var pool []string
	for _, s := range sites {
		if s != exclude {
			pool = append(pool, s)
		}
	}
	if len(pool) == 0 {
		pool = sites
	}
	if len(pool) == 0 {
		return "", nil
	}
	best, bestSeen := 0, int64(0)
	for i, s := range pool {
		snap, err := tx.Get(cloud.Col.Agents(deploymentID).Doc(s))
		if err != nil {
			return "", err
		}
		seen, _ := doc.ParseISO(snap.Data().Str("lastSeenAt"))
		if i == 0 || seen > bestSeen {
			best, bestSeen = i, seen
		}
	}
	return pool[best], nil
}

// NewFetch is newFetch; from: string or nil.
func NewFetch(requestedBy string, from any) *doc.Map {
	return doc.Obj("requestedAt", doc.NowISO(), "requestedBy", requestedBy, "from", from, "startedAt", nil, "bytesDone", 0.0, "error", nil)
}

// Actor is the { id, siteSlug } of uploads: string or nil.
type Actor struct{ ID, SiteSlug any }

// MarkFileStored is markFileStored: a 'file' upload completed; false when the file was unpublished meanwhile (the caller
// deletes the object it just wrote).
func MarkFileStored(tx *driver.Tx, deploymentID, fileID, uploadID string, actor Actor) (bool, error) {
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	fRef := cloud.Col.Files(deploymentID).Doc(fileID)
	ds, err := tx.Get(depRef)
	if err != nil {
		return false, err
	}
	fs, err := tx.Get(fRef)
	if err != nil {
		return false, err
	}
	if !ds.Exists() || !fs.Exists() {
		return false, cloud.E.NotFound("file not found")
	}
	f := fs.Data()
	if f.Val("state") == "unpublished" {
		return false, nil
	}
	if f.Map("cloud").Truthy("stored") && f.Val("state") == "ready" {
		return true, nil
	}
	dep := cloud.DecodeDeployment(ds.Data())
	log := NewCloudLog(deploymentID, dep)
	upd := doc.Obj("state", "ready", "cloud", doc.Obj("stored", true, "fetch", nil))
	if f.Val("_uploadId") == uploadID {
		upd.Set("_uploadId", nil)
	}
	if f.Val("state") == "ready" {
		from := or(f.Map("cloud").Map("fetch"), "from", actor.SiteSlug)
		where := ""
		if doc.Truthy(from) {
			where = " from site " + JSString(from)
		}
		origin := "cloud"
		if doc.Truthy(actor.SiteSlug) {
			origin = "agent"
		}
		log.Add(tx, CloudEntry{Type: "file.fetched", Summary: "The cloud got a copy of " + JSString(present(f, "fileName")) + where, Data: doc.Obj("fileId", fileID, "where", "cloud", "from", from), Actor: actor.ID, Origin: origin})
	}
	tx.Update(fRef, upd)
	d := doc.New()
	if log.Changed() {
		d = log.DepUpdate()
	}
	tx.Update(depRef, d.Set("agentEpoch", dep.Num("agentEpoch")+1))
	return true, nil
}
