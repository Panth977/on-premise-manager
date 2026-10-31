package apitest

import (
	"bytes"
	"crypto/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/model"
)

// waRand is randomBytes(n).
func waRand(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// waNum is Number(x) of a cursor.
func waNum(t testing.TB, j J) float64 {
	t.Helper()
	if s, ok := j.V.(string); ok {
		n, err := strconv.ParseFloat(s, 64)
		NoErr(t, err)
		return n
	}
	return j.Num()
}

// Port of backend/test/06-transfers.test.ts.
func TestTransfers(t *testing.T) {
	t.Run("blobs: small upload, sha header check, dedupe, download + Range, permissions", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		data := waRand(1000)
		hex := Sha256Hex(data)
		admin.API.Rejects(t, "uploadBlob", A{"body": data, "headers": map[string]string{"X-OPM-Sha256": "sha256:" + strings.Repeat("0", 64)}}, "invalid")
		b := admin.API.Call(t, "uploadBlob", A{"body": data, "headers": map[string]string{"X-OPM-Sha256": "sha256:" + hex}})
		Eq(t, b, A{"sha256": "sha256:" + hex, "sizeBytes": 1000})
		Eq(t, admin.API.Call(t, "uploadBlob", A{"body": data}), b, "idempotent")
		g := admin.API.Fetch(t, "getBlob", A{"sha256": b.At("sha256")})
		True(t, bytes.Equal(g.Body, data))
		r := Raw(t, "GET", "/v1/blobs/"+hex, Opt{Headers: map[string]string{"Authorization": admin.Bearer, "Range": "bytes=-10"}})
		Eq(t, r.Status, 206)
		True(t, bytes.Equal(r.Body, data[990:]))
		bad := Raw(t, "GET", "/v1/blobs/"+hex, Opt{Headers: map[string]string{"Authorization": admin.Bearer, "Range": "bytes=5000-"}})
		Eq(t, bad.Status, 416)
		admin.API.Rejects(t, "getBlob", A{"sha256": "sha256:" + strings.Repeat("b", 64)}, "not-found")
		nobody := NewPerson(t, L{})
		nobody.API.Rejects(t, "uploadBlob", A{"body": data}, "forbidden")
		nobody.API.Rejects(t, "getBlob", A{"sha256": b.At("sha256")}, "forbidden")
		cfgWriter := NewPerson(t, L{A{"resource": "deployment.config", "actions": L{"write"}, "scope": A{"kind": "tags", "tagSlugs": L{"x"}}}})
		Eq(t, cfgWriter.API.Call(t, "uploadBlob", A{"body": data}).At("sha256"), b.At("sha256"), "config.write on any deployment may upload")
	})

	t.Run("uploads: multi-part ≥ 24 MiB (8 MiB parts), resume state, exact part sizes, missing parts, sha mismatch", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		const partSize = model.UploadPartSize
		size := 3*partSize + 12345 // 24 MiB + 12345 B → 4 parts
		data := waRand(size)
		sha := "sha256:" + Sha256Hex(data)
		admin.API.Rejects(t, "createUpload", A{"body": A{"purpose": "blob", "sizeBytes": 51 * 1024 * 1024, "sha256": sha}}, "too-large", 413)
		admin.API.Rejects(t, "createUpload", A{"body": A{"purpose": "transfer", "sizeBytes": 1, "sha256": sha}}, "invalid")
		u := admin.API.Call(t, "createUpload", A{"body": A{"purpose": "blob", "sizeBytes": size, "sha256": sha}})
		uid := u.At("uploadId").Str()
		Eq(t, u.At("partCount"), 4)
		Eq(t, u.At("partSize"), partSize)
		Eq(t, u.At("status"), "open")
		part := func(i int) []byte { return data[i*partSize : min(size, (i+1)*partSize)] }
		admin.API.Rejects(t, "putUploadPart", A{"uploadId": uid, "index": "0", "body": part(0)[1:]}, "invalid")
		admin.API.Rejects(t, "putUploadPart", A{"uploadId": uid, "index": "9", "body": part(0)}, "invalid")
		Eq(t, admin.API.Call(t, "putUploadPart", A{"uploadId": uid, "index": "0", "body": part(0)}).At("received"), L{0})
		Eq(t, admin.API.Call(t, "putUploadPart", A{"uploadId": uid, "index": "0", "body": part(0)}).At("received"), L{0}, "idempotent")
		admin.API.Call(t, "putUploadPart", A{"uploadId": uid, "index": "3", "body": part(3)})
		e := admin.API.Rejects(t, "completeUpload", A{"uploadId": uid}, "invalid")
		Eq(t, e.Details.At("missing"), L{1, 2})
		Eq(t, admin.API.Call(t, "getUpload", A{"uploadId": uid}).At("partsReceived"), L{0, 3})
		admin.API.Call(t, "putUploadPart", A{"uploadId": uid, "index": "1", "body": part(1)})
		admin.API.Call(t, "putUploadPart", A{"uploadId": uid, "index": "2", "body": part(2)})
		done := admin.API.Call(t, "completeUpload", A{"uploadId": uid})
		Eq(t, done.At("upload.status"), "complete")
		Eq(t, done.At("blob"), A{"sha256": sha, "sizeBytes": size})
		got := admin.API.Fetch(t, "getBlob", A{"sha256": sha})
		Eq(t, Sha256Hex(got.Body), sha[7:])
		parts, err := Bucket.B.List("uploads/" + uid + "/")
		NoErr(t, err)
		Eq(t, len(parts), 0, "parts deleted")
		// other people cannot touch the upload
		other := NewPerson(t, "admin")
		other.API.Rejects(t, "getUpload", A{"uploadId": uid}, "not-found")
		// sha mismatch → failed
		small := waRand(100)
		u2 := admin.API.Call(t, "createUpload", A{"body": A{"purpose": "blob", "sizeBytes": 100, "sha256": "sha256:" + strings.Repeat("c", 64)}})
		admin.API.Call(t, "putUploadPart", A{"uploadId": u2.At("uploadId"), "index": "0", "body": small})
		admin.API.Rejects(t, "completeUpload", A{"uploadId": u2.At("uploadId")}, "invalid")
		admin.API.Rejects(t, "getUpload", A{"uploadId": u2.At("uploadId")}, "gone", 410)
		// zero-byte upload = one empty part
		z := admin.API.Call(t, "createUpload", A{"body": A{"purpose": "blob", "sizeBytes": 0, "sha256": "sha256:" + Sha256Hex("")}})
		Eq(t, z.At("partCount"), 1)
		admin.API.Call(t, "putUploadPart", A{"uploadId": z.At("uploadId"), "index": "0", "body": []byte{}})
		Eq(t, admin.API.Call(t, "completeUpload", A{"uploadId": z.At("uploadId")}).At("upload.status"), "complete")
	})

	t.Run("transfers: cloud → site (ready, cloud view, agent download, saved) and site → cloud (download → downloaded); delete; daily expiry", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		app := EnrollSite(t, admin, id, "app")
		data := waRand(200_000)
		sha := "sha256:" + Sha256Hex(data)
		admin.API.Rejects(t, "createTransfer", A{"deploymentId": id, "siteSlug": "app", "body": A{"fileName": "a/b", "sizeBytes": 1, "sha256": sha}}, "invalid")
		admin.API.Rejects(t, "createTransfer", A{"deploymentId": id, "siteSlug": "app", "body": A{"fileName": "x", "sizeBytes": 3 * 1024 * 1024 * 1024, "sha256": sha}}, "too-large")
		admin.API.Rejects(t, "createTransfer", A{"deploymentId": id, "siteSlug": "zzz", "body": A{"fileName": "x", "sizeBytes": 1, "sha256": sha}}, "not-found")
		c := admin.API.Call(t, "createTransfer", A{"deploymentId": id, "siteSlug": "app", "body": A{"fileName": "manual.pdf", "sizeBytes": len(data), "sha256": sha, "note": "read me"}})
		tid := c.At("transfer.transferId").Str()
		Eq(t, c.At("transfer.status"), "uploading")
		Eq(t, c.At("transfer.storagePath"), "transfers/"+id+"/"+tid)
		Eq(t, c.At("upload.purpose"), "transfer")
		v0 := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
		Eq(t, v0.At("transfersToSite").Len(), 0)
		admin.API.Call(t, "putUploadPart", A{"uploadId": c.At("upload.uploadId"), "index": "0", "body": data})
		done := admin.API.Call(t, "completeUpload", A{"uploadId": c.At("upload.uploadId")})
		Eq(t, done.At("transfer.status"), "ready")
		v1 := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
		Eq(t, waNum(t, v1.At("cursor")), waNum(t, v0.At("cursor"))+1)
		Eq(t, v1.At("transfersToSite.0.transferId"), tid)
		lt := app.API.Call(t, "listAgentTransfers", A{"query": A{"direction": "to-site"}})
		Eq(t, lt.At("items").Len(), 1)
		dl := app.API.Fetch(t, "downloadAgentTransfer", A{"transferId": tid})
		Eq(t, Sha256Hex(dl.Body), sha[7:])
		app.API.Rejects(t, "reportAgentTransfer", A{"transferId": tid, "body": A{"status": "saved-on-server", "actor": "ops@example.com"}}, "invalid")
		app.API.Call(t, "reportAgentTransfer", A{"transferId": tid, "body": A{"status": "saved-on-server", "savedPath": "/srv/manual.pdf", "actor": "ops@example.com"}})
		lst := admin.API.Call(t, "listTransfers", A{"deploymentId": id, "siteSlug": "app", "query": A{"direction": "to-site"}})
		Eq(t, lst.At("items.0.status"), "saved-on-server")
		Eq(t, lst.At("items.0.savedPath"), "/srv/manual.pdf")
		acts := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "transfer."}})
		// transfer.sent twice: stage "started" (createTransfer) and stage "uploaded" (complete → ready)
		Eq(t, waSorted(acts.At("items").Arr(), "type"), L{"transfer.saved", "transfer.sent", "transfer.sent"})
		Eq(t, waSorted(acts.At("items").Filter(func(a J) bool { return a.At("type").Str() == "transfer.sent" }), "data.stage"), L{"started", "uploaded"})
		// site → cloud
		up := waRand(5000)
		usha := "sha256:" + Sha256Hex(up)
		app.API.Rejects(t, "createAgentTransfer", A{"body": A{"fileName": "dump.sql", "sizeBytes": len(up), "sha256": usha, "actor": "nope"}}, "invalid")
		ac := app.API.Call(t, "createAgentTransfer", A{"body": A{"fileName": "dump.sql", "sizeBytes": len(up), "sha256": usha, "actor": "ops@example.com", "note": "db dump"}})
		acID, acUp := ac.At("transfer.transferId").Str(), ac.At("upload.uploadId").Str()
		Eq(t, ac.At("transfer.direction"), "to-cloud")
		Eq(t, ac.At("transfer.createdBy"), "ops@example.com")
		admin.API.Rejects(t, "putUploadPart", A{"uploadId": acUp, "index": "0", "body": up}, "not-found", 404)
		app.API.Call(t, "putAgentUploadPart", A{"uploadId": acUp, "index": "0", "body": up})
		Eq(t, app.API.Call(t, "getAgentUpload", A{"uploadId": acUp}).At("partsReceived"), L{0})
		acd := app.API.Call(t, "completeAgentUpload", A{"uploadId": acUp})
		Eq(t, acd.At("transfer.status"), "ready")
		app.API.Call(t, "reportAgentTransfer", A{"transferId": acID, "body": A{"status": "ready", "actor": "ops@example.com"}})
		hd := admin.API.Fetch(t, "downloadTransfer", A{"deploymentId": id, "transferId": acID})
		True(t, bytes.Equal(hd.Body, up))
		Match(t, hd.Header("content-disposition"), `dump\.sql`)
		tc := admin.API.Call(t, "listTransfers", A{"deploymentId": id, "siteSlug": "app", "query": A{"direction": "to-cloud"}})
		Eq(t, tc.At("items.0.status"), "downloaded")
		recv := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "transfer.received"}})
		Eq(t, recv.At("items").Len(), 1)
		// delete
		c2 := admin.API.Call(t, "createTransfer", A{"deploymentId": id, "siteSlug": "app", "body": A{"fileName": "b.bin", "sizeBytes": 3, "sha256": "sha256:" + Sha256Hex("abc")}})
		c2ID := c2.At("transfer.transferId").Str()
		admin.API.Call(t, "putUploadPart", A{"uploadId": c2.At("upload.uploadId"), "index": "0", "body": []byte("abc")})
		admin.API.Call(t, "completeUpload", A{"uploadId": c2.At("upload.uploadId")})
		e0 := app.API.Call(t, "poll", A{"query": A{"wait": 0}}).At("cursor")
		admin.API.Call(t, "deleteTransfer", A{"deploymentId": id, "transferId": c2ID})
		vd := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
		Eq(t, waNum(t, vd.At("cursor")), waNum(t, e0)+1)
		// the agents learn of the deletion, so they drop their copy too
		Eq(t, vd.At("deletedTransfersToSite"), L{A{"transferId": c2ID, "siteSlug": "app"}})
		False(t, vd.At("transfersToSite").Find("transferId", c2ID).Exists())
		admin.API.Rejects(t, "downloadTransfer", A{"deploymentId": id, "transferId": c2ID}, "gone", 410)
		objs, err := Bucket.B.List("transfers/" + id + "/" + c2ID)
		NoErr(t, err)
		Eq(t, len(objs), 0)
		// daily job: transfers never expire — even a row with a past expiresAt (written before the expiry was removed)
		// survives and still downloads; an old upload + old uptime are cleaned
		past := func() string { return ISO(time.Now().Add(-time.Second)) }
		UpdateDoc(t, "deployments/"+id+"/transfers/"+acID, A{"expiresAt": past()})
		stale := admin.API.Call(t, "createUpload", A{"body": A{"purpose": "blob", "sizeBytes": 10, "sha256": "sha256:" + strings.Repeat("d", 64)}})
		UpdateDoc(t, "uploads/"+stale.At("uploadId").Str(), A{"expiresAt": past()})
		zeros := L{}
		for i := 0; i < 24; i++ {
			zeros = append(zeros, 0)
		}
		SetDoc(t, "deployments/"+id+"/uptime/app_20000101", A{"siteSlug": "app", "date": "20000101", "beats": zeros, "hours": zeros, "updatedAt": ""})
		r := RunDaily(t)
		True(t, r.At("uploadsExpired").Num() >= 1)
		True(t, r.At("uptimeDeleted").Num() >= 1)
		admin.API.Rejects(t, "getUpload", A{"uploadId": stale.At("uploadId")}, "gone")
		Eq(t, admin.API.Fetch(t, "downloadTransfer", A{"deploymentId": id, "transferId": acID}).Status, 200)
		NotEq(t, Doc(t, "deployments/"+id+"/transfers/"+acID).At("status"), "expired")
		Eq(t, ac.At("transfer.expiresAt"), nil)
		False(t, Doc(t, "deployments/"+id+"/uptime/app_20000101").Exists())
		// permission: transfers.read cannot create
		reader := NewPerson(t, L{A{"resource": "deployment.transfers", "actions": L{"read"}, "scope": A{"kind": "all"}}})
		reader.API.Rejects(t, "createTransfer", A{"deploymentId": id, "siteSlug": "app", "body": A{"fileName": "x", "sizeBytes": 1, "sha256": sha}}, "forbidden")
		True(t, reader.API.Call(t, "listTransfers", A{"deploymentId": id, "siteSlug": "app", "query": A{"direction": "to-site"}}).At("items").Len() >= 1)
	})

	// §21 #100 store-and-forward: nobody needs to be on the receiving end, in either direction.
	t.Run("transfers §21 #100: fetched → fetchedAt (once, idempotent); to-cloud completes with nobody viewing; to-site waits in the cloud view", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		app := EnrollSite(t, admin, id, "app")
		// cloud → site: the operator uploads and leaves; the site has not polled yet
		data := waRand(3000)
		c := admin.API.Call(t, "createTransfer", A{"deploymentId": id, "siteSlug": "app", "body": A{"fileName": "later.bin", "sizeBytes": len(data), "sha256": "sha256:" + Sha256Hex(data)}})
		tid := c.At("transfer.transferId").Str()
		Eq(t, c.At("transfer.fetchedAt"), nil)
		fetched := A{"status": "fetched", "actor": "ops@example.com"}
		app.API.Rejects(t, "reportAgentTransfer", A{"transferId": tid, "body": fetched}, "invalid") // still uploading
		admin.API.Call(t, "putUploadPart", A{"uploadId": c.At("upload.uploadId"), "index": "0", "body": data})
		admin.API.Call(t, "completeUpload", A{"uploadId": c.At("upload.uploadId")})
		// the agent comes online later: the file is still in its cloud view and downloadable
		for i := 0; i < 2; i++ {
			v := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
			Eq(t, v.At("transfersToSite").Pluck("transferId"), L{tid})
		}
		dl := app.API.Fetch(t, "downloadAgentTransfer", A{"transferId": tid})
		True(t, bytes.Equal(dl.Body, data))
		app.API.Call(t, "reportAgentTransfer", A{"transferId": tid, "body": fetched})
		toSite := A{"deploymentId": id, "siteSlug": "app", "query": A{"direction": "to-site"}}
		l1 := admin.API.Call(t, "listTransfers", toSite).At("items.0")
		Eq(t, l1.At("status"), "ready", "fetched does not change the status")
		True(t, l1.At("fetchedAt").Truthy())
		Sleep(20)
		app.API.Call(t, "reportAgentTransfer", A{"transferId": tid, "body": fetched})
		l2 := admin.API.Call(t, "listTransfers", toSite).At("items.0")
		Eq(t, l2.At("fetchedAt"), l1.At("fetchedAt"), "set once")
		acts := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "transfer."}})
		Eq(t, len(acts.At("items").Filter(func(a J) bool {
			return a.At("type").Str() == "transfer.received" || a.At("type").Str() == "transfer.saved"
		})), 0, "fetched writes no activity")
		// saved-on-server sets fetchedAt when an older report never came
		c2 := admin.API.Call(t, "createTransfer", A{"deploymentId": id, "siteSlug": "app", "body": A{"fileName": "b.txt", "sizeBytes": 3, "sha256": "sha256:" + Sha256Hex("abc")}})
		c2ID := c2.At("transfer.transferId").Str()
		admin.API.Call(t, "putUploadPart", A{"uploadId": c2.At("upload.uploadId"), "index": "0", "body": []byte("abc")})
		admin.API.Call(t, "completeUpload", A{"uploadId": c2.At("upload.uploadId")})
		app.API.Call(t, "reportAgentTransfer", A{"transferId": c2ID, "body": A{"status": "saved-on-server", "savedPath": "/srv/b.txt", "actor": "ops@example.com"}})
		s2 := admin.API.Call(t, "listTransfers", toSite).At("items").Find("transferId", c2ID)
		Eq(t, s2.At("status"), "saved-on-server")
		True(t, s2.At("fetchedAt").Truthy())
		// site → cloud: create + part + complete with no human call in between; then a human list shows it ready
		up := waRand(4000)
		ac := app.API.Call(t, "createAgentTransfer", A{"body": A{"fileName": "nobody.log", "sizeBytes": len(up), "sha256": "sha256:" + Sha256Hex(up), "actor": "ops@example.com"}})
		acID := ac.At("transfer.transferId").Str()
		app.API.Call(t, "putAgentUploadPart", A{"uploadId": ac.At("upload.uploadId"), "index": "0", "body": up})
		Eq(t, app.API.Call(t, "completeAgentUpload", A{"uploadId": ac.At("upload.uploadId")}).At("transfer.status"), "ready")
		app.API.Rejects(t, "reportAgentTransfer", A{"transferId": acID, "body": fetched}, "invalid") // to-cloud
		tc := admin.API.Call(t, "listTransfers", A{"deploymentId": id, "siteSlug": "app", "query": A{"direction": "to-cloud"}}).At("items")
		Eq(t, tc.Find("transferId", acID).At("status"), "ready")
		hd := admin.API.Fetch(t, "downloadTransfer", A{"deploymentId": id, "transferId": acID})
		True(t, bytes.Equal(hd.Body, up))
	})
}
