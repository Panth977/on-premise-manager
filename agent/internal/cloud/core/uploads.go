package core

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// UPLOAD_TTL_MS is UPLOAD_TTL_MS.
const UPLOAD_TTL_MS = 24 * 3600_000

// NewUploadOpts of NewUpload. DeploymentID, TransferID, CreatorSite: string or nil; StageID "" = none.
type NewUploadOpts struct {
	Purpose                               string
	SeedArchive                           bool
	DeploymentID, TransferID, CreatorSite any
	SizeBytes                             float64
	Sha256                                string
	CreatedBy                             string
	Target                                string
	StageID                               string
}

// NewUpload is newUpload: a StoredUpload (not written yet).
func NewUpload(o NewUploadOpts) *doc.Map {
	u := doc.Obj("uploadId", cloud.Col.Uploads().Doc().ID(), "purpose", o.Purpose, "deploymentId", o.DeploymentID, "transferId", o.TransferID, "sizeBytes", o.SizeBytes, "sha256", o.Sha256, "partSize", float64(shared.UPLOAD_PART_SIZE), "partCount", math.Max(1, math.Ceil(o.SizeBytes/shared.UPLOAD_PART_SIZE)), "partsReceived", []any{}, "status", "open", "error", nil, "createdAt", doc.NowISO(), "createdBy", o.CreatedBy, "expiresAt", doc.IsoPlus(UPLOAD_TTL_MS), "_creatorSite", o.CreatorSite, "_target", o.Target)
	if o.SeedArchive {
		u.Set("_seedArchive", true)
	}
	if o.StageID != "" {
		u.Set("_stageId", o.StageID)
	}
	return u
}

// UploadView is uploadView.
func UploadView(u *doc.Map) *doc.Map {
	return cloud.Pub(u).Set("partsReceived", sortedNums(u.Arr("partsReceived")))
}

// Creator is Creator: a person (UID) or an agent (DeploymentID + SiteSlug).
type Creator struct {
	Agent                       bool
	UID, DeploymentID, SiteSlug string
}

// LoadUpload is loadUpload.
func LoadUpload(id string, who Creator, allowClosed bool) (*doc.Map, error) {
	s, err := cloud.Col.Uploads().Doc(id).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("upload not found")
	}
	u := s.Data()
	var mine bool
	if who.Agent {
		mine = u.Val("_creatorSite") == who.SiteSlug && u.Val("deploymentId") == who.DeploymentID
	} else {
		mine = !u.Truthy("_creatorSite") && u.Val("createdBy") == who.UID
	}
	if !mine {
		return nil, cloud.E.NotFound("upload not found")
	}
	if !allowClosed {
		if u.Val("status") == "failed" {
			msg := "the upload failed"
			if u.Truthy("error") {
				msg += ": " + JSString(u.Val("error"))
			}
			return nil, cloud.E.Gone(msg)
		}
		if exp, ok := doc.ParseISO(u.Str("expiresAt")); ok && exp <= time.Now().UnixMilli() && u.Val("status") == "open" {
			return nil, cloud.E.Gone("the upload expired")
		}
	}
	return u, nil
}

// ExpectedPartSize is expectedPartSize.
func ExpectedPartSize(u *doc.Map, index float64) float64 {
	if index < u.Num("partCount")-1 {
		return u.Num("partSize")
	}
	return u.Num("sizeBytes") - u.Num("partSize")*(u.Num("partCount")-1)
}

var reIndex = regexp.MustCompile(`^[0-9]+$`)

// PutPart is putPart: {received}, the sorted list of received indexes.
func PutPart(u *doc.Map, indexStr string, body []byte) (*doc.Map, error) {
	if !reIndex.MatchString(indexStr) {
		return nil, cloud.E.Bad("index", "V_FORMAT", "index must be an integer")
	}
	index, _ := strconv.ParseFloat(indexStr, 64)
	if index >= u.Num("partCount") {
		return nil, cloud.E.Bad("index", "V_RANGE", "index must be 0–"+JSString(u.Num("partCount")-1))
	}
	if u.Val("status") == "complete" {
		return nil, cloud.E.Gone("the upload is complete")
	}
	want := ExpectedPartSize(u, index)
	if float64(len(body)) != want {
		return nil, cloud.E.Bad("body", "V_LENGTH", fmt.Sprintf("part %s must be exactly %s bytes (got %d)", JSString(index), JSString(want), len(body)))
	}
	if u.Truthy("_seedArchive") && index == 0 && !(len(body) > 1 && body[0] == 0x1f && body[1] == 0x8b) {
		return nil, cloud.E.Bad("body", "V_FORMAT", "a seed archive must be a .tar.gz (gzip) file")
	}
	id := u.Str("uploadId")
	if err := cloud.Blobs().Put(Paths.UploadPart(id, int(index)), body, driver.PutOptions{ContentType: "application/octet-stream"}); err != nil {
		return nil, err
	}
	if err := cloud.Col.Uploads().Doc(id).Update(doc.Obj("partsReceived", doc.ArrayUnion(index))); err != nil {
		return nil, err
	}
	seen := map[float64]bool{}
	out := []any{}
	for _, x := range append(append([]any{}, u.Arr("partsReceived")...), index) {
		if f, ok := doc.AsNumber(x); ok && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return doc.Obj("received", sortedNums(out)), nil
}

// partsReader reads the parts of an upload one after the other.
type partsReader struct {
	id    string
	count int
	i     int
	cur   io.ReadCloser
}

func (r *partsReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			if r.i >= r.count {
				return 0, io.EOF
			}
			s, err := cloud.Blobs().Stream(Paths.UploadPart(r.id, r.i), nil)
			if err != nil {
				return 0, err
			}
			r.cur = s
			r.i++
		}
		n, err := r.cur.Read(p)
		if err == io.EOF {
			r.cur.Close()
			r.cur = nil
			if n == 0 {
				continue
			}
			return n, nil
		}
		return n, err
	}
}

func (r *partsReader) close() {
	if r.cur != nil {
		r.cur.Close()
	}
}

// joinParts is the concat branch of completeUpload (the blob store cannot compose).
func joinParts(u *doc.Map, target, hex string) error {
	r := &partsReader{id: u.Str("uploadId"), count: int(u.Num("partCount"))}
	defer r.close()
	_, size, err := StreamToStorage(target, r, hex, "")
	if err != nil {
		return err
	}
	if float64(size) != u.Num("sizeBytes") {
		if err := Remove(target); err != nil {
			return err
		}
		return fmt.Errorf("size mismatch (got %d bytes, expected %s)", size, JSString(u.Val("sizeBytes")))
	}
	return nil
}

func dropParts(id string) { _ = RemovePrefix("uploads/" + id + "/") }

// CompleteUpload is completeUpload: join + verify, marks the upload complete (or failed) and finishes the transfer.
// Idempotent once complete. The result is a CompleteUploadResponse.
func CompleteUpload(u *doc.Map, actor Actor) (*doc.Map, error) {
	id := u.Str("uploadId")
	ref := cloud.Col.Uploads().Doc(id)
	if u.Val("status") == "complete" {
		return finishedResponse(u)
	}
	dep, tid := u.Str("deploymentId"), u.Str("transferId")
	purpose := u.Val("purpose")
	target := u.Str("_target")
	sizeIs := func() (bool, error) {
		s, err := SizeOf(target)
		return float64(s) == u.Num("sizeBytes"), err
	}
	finish := func() (*doc.Map, error) {
		dropParts(id)
		if err := ref.Update(doc.Obj("status", "complete", "error", nil)); err != nil {
			return nil, err
		}
		return finishedResponse(u.Shallow().Set("status", "complete").Set("error", nil))
	}
	if purpose == "transfer" && dep != "" && tid != "" {
		t, err := cloud.Col.Transfers(dep).Doc(tid).Get()
		if err != nil {
			return nil, err
		}
		if t.Exists() && t.Data().Val("status") != "uploading" {
			ok, err := sizeIs()
			if err != nil {
				return nil, err
			}
			if ok {
				return finish()
			}
		}
	}
	if purpose == "file" && dep != "" && tid != "" {
		f, err := cloud.Col.Files(dep).Doc(tid).Get()
		if err != nil {
			return nil, err
		}
		if fd := f.Data(); f.Exists() && fd.Val("state") != "unpublished" && fd.Map("cloud").Truthy("stored") && fd.Val("_uploadId") != id {
			ok, err := sizeIs()
			if err != nil {
				return nil, err
			}
			if ok {
				return finish()
			}
		}
	}
	count := int(u.Num("partCount"))
	got := map[float64]bool{}
	for _, x := range u.Arr("partsReceived") {
		if f, ok := doc.AsNumber(x); ok {
			got[f] = true
		}
	}
	missing, parts := []any{}, []any{}
	var names []string
	for i := range count {
		parts = append(parts, float64(i))
		if !got[float64(i)] {
			missing = append(missing, float64(i))
			if len(names) < 20 {
				names = append(names, strconv.Itoa(i))
			}
		}
	}
	if len(missing) > 0 {
		return nil, cloud.E.New("invalid", "parts missing: "+strings.Join(names, ", "), doc.Obj("missing", missing, "errors", []any{doc.Obj("path", "parts", "code", "V_REQUIRED", "message", "parts missing", "severity", "error")}), nil)
	}
	hex := cloud.HexOf(u.Str("sha256"))
	already := false
	if purpose == "blob" {
		var err error
		if already, err = sizeIs(); err != nil {
			return nil, err
		}
	}
	if !already {
		if err := joinParts(u, target, hex); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "sha256 mismatch") {
				msg = "sha256 mismatch"
			}
			if err := ref.Update(doc.Obj("status", "failed", "error", msg)); err != nil {
				return nil, err
			}
			if purpose == "transfer" && tid != "" && dep != "" {
				_ = cloud.Col.Transfers(dep).Doc(tid).Update(doc.Obj("error", msg))
			}
			if purpose == "file" && tid != "" && dep != "" {
				_ = failFileFetch(dep, tid, id, msg)
			}
			dropParts(id)
			return nil, cloud.E.Bad("sha256", "V_FORMAT", "upload verification failed: "+msg)
		}
	}
	dropParts(id)
	if u.Truthy("_stageId") {
		if err := cloud.Blobs().SetMeta(target, map[string]*string{"opmSha256": &hex}); err != nil {
			return nil, err
		}
	}
	var transfer any
	fileGone := false
	err := cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		if purpose == "transfer" && dep != "" && tid != "" {
			t, err := markTransferReady(tx, dep, tid, actor)
			if err != nil {
				return err
			}
			transfer = t
		}
		if purpose == "file" && dep != "" && tid != "" {
			ok, err := MarkFileStored(tx, dep, tid, id, actor)
			if err != nil {
				return err
			}
			fileGone = !ok
		}
		tx.Update(ref, doc.Obj("status", "complete", "error", nil))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if fileGone {
		_ = Remove(target)
	}
	done := u.Shallow().Set("status", "complete").Set("error", nil).Set("partsReceived", parts)
	return doc.Obj("upload", UploadView(done), "blob", blobOf(u), "transfer", transfer), nil
}

func blobOf(u *doc.Map) any {
	if u.Val("purpose") == "blob" {
		return doc.Obj("sha256", present(u, "sha256"), "sizeBytes", present(u, "sizeBytes"))
	}
	return nil
}

func finishedResponse(u *doc.Map) (*doc.Map, error) {
	var transfer any
	if dep, tid := u.Str("deploymentId"), u.Str("transferId"); u.Val("purpose") == "transfer" && dep != "" && tid != "" {
		t, err := cloud.Col.Transfers(dep).Doc(tid).Get()
		if err != nil {
			return nil, err
		}
		if t.Exists() {
			transfer = t.Data()
		}
	}
	return doc.Obj("upload", UploadView(u), "blob", blobOf(u), "transfer", transfer), nil
}

func markTransferReady(tx *driver.Tx, deploymentID, transferID string, actor Actor) (*doc.Map, error) {
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	tRef := cloud.Col.Transfers(deploymentID).Doc(transferID)
	ds, err := tx.Get(depRef)
	if err != nil {
		return nil, err
	}
	ts, err := tx.Get(tRef)
	if err != nil {
		return nil, err
	}
	if !ds.Exists() || !ts.Exists() {
		return nil, cloud.E.NotFound("transfer not found")
	}
	t := ts.Data()
	if t.Val("status") != "uploading" {
		return t, nil
	}
	dep := cloud.DecodeDeployment(ds.Data())
	log := NewCloudLog(deploymentID, dep)
	next := t.Shallow().Set("status", "ready").Set("error", nil)
	tx.Set(tRef, next)
	name, site := JSString(present(t, "fileName")), JSString(present(t, "siteSlug"))
	summary := "Site " + site + " sent " + name + " to the cloud"
	if t.Val("direction") == "to-site" {
		summary = "File " + name + " sent to site " + site
	}
	log.Add(tx, CloudEntry{Type: "transfer.sent", Summary: summary, Data: doc.Obj("stage", "uploaded", "transferId", transferID, "direction", present(t, "direction"), "fileName", present(t, "fileName"), "sizeBytes", present(t, "sizeBytes"), "siteSlug", present(t, "siteSlug")), Actor: actor.ID})
	upd := log.DepUpdate()
	if t.Val("direction") == "to-site" {
		upd.Set("agentEpoch", dep.Num("agentEpoch")+1)
	}
	tx.Update(depRef, upd)
	return next, nil
}

func failFileFetch(deploymentID, fileID, uploadID, msg string) error {
	fRef := cloud.Col.Files(deploymentID).Doc(fileID)
	return cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		s, err := tx.Get(fRef)
		if err != nil {
			return err
		}
		f := s.Data()
		if !s.Exists() || f.Val("_uploadId") != uploadID {
			return nil
		}
		upd := doc.Obj("_uploadId", nil)
		if f.Map("cloud").Truthy("fetch") {
			upd.Set("cloud.fetch.error", msg)
		}
		tx.Update(fRef, upd)
		return nil
	})
}
