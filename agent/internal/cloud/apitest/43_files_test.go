package apitest

import (
	"bytes"
	"crypto/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/ulid"
)

func wfShaOf(b any) string { return "sha256:" + Sha256Hex(b) }

// wfCursor is Number(view.cursor).
func wfCursor(t testing.TB, v J) float64 {
	t.Helper()
	if s, ok := v.At("cursor").V.(string); ok {
		n, err := strconv.ParseFloat(s, 64)
		NoErr(t, err)
		return n
	}
	return v.At("cursor").Num()
}

func wfRandom(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	NoErr(t, err)
	return b
}

func wfSameBytes(t testing.TB, got, want []byte, msg ...any) {
	t.Helper()
	True(t, bytes.Equal(got, want), msg...)
}

// wfReg is reg(): the body of registerAgentFile; o overrides the defaults.
func wfReg(data []byte, o A) A {
	b := A{"fileId": ulid.New(), "fileName": "site.bin", "sizeBytes": len(data), "sha256": wfShaOf(data), "note": nil, "publishedAt": Now(), "publishedBy": "ops@example.com", "via": "browser", "sourcePath": nil}
	for k, v := range o {
		b[k] = v
	}
	return b
}

func wfPollNow(t testing.TB, c *Client) J {
	t.Helper()
	return c.Call(t, "poll", A{"query": A{"wait": 0}})
}

func wfBlobCount(t testing.TB, prefix string) int {
	t.Helper()
	l, err := Bucket.B.List(prefix)
	NoErr(t, err)
	return len(l)
}

func wfSorted(l []any) L {
	out := L{}
	for _, s := range wfSortedStrs(l) {
		out = append(out, s)
	}
	return out
}

// wfWith is { ...base, ...extra }.
func wfWith(base, extra A) A {
	out := A{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// Port of backend/test/43-files.test.ts. "files: migrate-m ..." is in testdata/ts-dropped-wf.txt.
func TestFiles(t *testing.T) {
	t.Run("files: console publish → upload → ready; cloud view; download (Range); unpublish → tombstone + 410", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		app := EnrollSite(t, admin, id, "app")
		data := wfRandom(t, 300_000)
		v0 := wfPollNow(t, app.API)
		Eq(t, v0.At("files"), L{})
		Eq(t, v0.At("fileFetches"), L{})
		p := admin.Real.Call(t, "publishFile", A{"deploymentId": id, "body": A{"fileName": "manual.pdf", "sizeBytes": len(data), "sha256": wfShaOf(data), "note": "read me"}})
		fileID := p.At("file.fileId").Str()
		Eq(t, p.At("file.state"), "uploading")
		Eq(t, p.At("file.origin"), A{"kind": "cloud"})
		Eq(t, p.At("file.publishedBy"), admin.Email)
		Eq(t, p.At("upload.purpose"), "file")
		Eq(t, p.At("upload.transferId"), fileID)
		False(t, p.At("file").Has("_storagePath"))
		v1 := wfPollNow(t, app.API)
		Eq(t, wfCursor(t, v1), wfCursor(t, v0)+1)
		Eq(t, v1.At("files.0.state"), "uploading")
		admin.Real.Rejects(t, "downloadFile", A{"deploymentId": id, "fileId": fileID}, "conflict", 409)
		admin.Real.Rejects(t, "fetchFileToCloud", A{"deploymentId": id, "fileId": fileID}, "conflict", 409)
		admin.Real.Call(t, "putUploadPart", A{"uploadId": p.At("upload.uploadId"), "index": "0", "body": data})
		done := admin.Real.Call(t, "completeUpload", A{"uploadId": p.At("upload.uploadId")})
		Eq(t, done.At("upload.status"), "complete")
		v2 := wfPollNow(t, app.API)
		Eq(t, wfCursor(t, v2), wfCursor(t, v1)+1)
		f := v2.At("files").Find("fileId", fileID)
		Eq(t, f.At("state"), "ready")
		Eq(t, f.At("cloud"), A{"stored": true, "fetch": nil})
		True(t, !f.Has("_storagePath") && !f.Has("_uploadId"))
		Eq(t, wfBlobCount(t, "shared-files/"+id+"/"+fileID), 1)
		// downloads: console (activity) + Range; agent
		dl := admin.Real.Fetch(t, "downloadFile", A{"deploymentId": id, "fileId": fileID})
		wfSameBytes(t, dl.Body, data)
		Match(t, dl.Header("content-disposition"), `manual\.pdf`)
		r := Raw(t, "GET", "/v1/deployments/"+id+"/files/"+fileID+"/download", Opt{Headers: map[string]string{"Authorization": admin.Bearer, "Range": "bytes=100-199"}})
		Eq(t, r.Status, 206)
		wfSameBytes(t, r.Body, data[100:200])
		adl := app.API.Fetch(t, "downloadAgentFile", A{"fileId": fileID, "headers": map[string]string{"Range": "bytes=-10"}})
		wfSameBytes(t, adl.Body, data[len(data)-10:])
		acts := admin.Real.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "file."}})
		Eq(t, wfSorted(acts.At("items").Pluck("type")), L{"file.downloaded", "file.published"})
		Eq(t, acts.At("items").Find("type", "file.downloaded").At("data"), A{"fileId": fileID, "where": "cloud"})
		// fetch on a stored file: unchanged
		Eq(t, admin.Real.Call(t, "fetchFileToCloud", A{"deploymentId": id, "fileId": fileID}).At("cloud"), A{"stored": true, "fetch": nil})
		// list
		l := admin.Real.Call(t, "listFiles", A{"deploymentId": id})
		Eq(t, l.At("items").Pluck("fileId"), L{fileID})
		// unpublish (idempotent) → tombstone in the cloud view, object gone, 410
		admin.Real.Call(t, "unpublishFile", A{"deploymentId": id, "fileId": fileID})
		Eq(t, admin.Real.Call(t, "unpublishFile", A{"deploymentId": id, "fileId": fileID}), A{"deleted": true})
		v3 := wfPollNow(t, app.API)
		Eq(t, wfCursor(t, v3), wfCursor(t, v2)+1, "the second unpublish changes nothing")
		tomb := v3.At("files").Find("fileId", fileID)
		Eq(t, tomb.At("state"), "unpublished")
		Eq(t, tomb.At("unpublishedBy"), admin.Email)
		True(t, tomb.At("unpublishedAt").Truthy())
		Eq(t, tomb.At("cloud"), A{"stored": false, "fetch": nil})
		Eq(t, wfBlobCount(t, "shared-files/"+id+"/"+fileID), 0)
		admin.Real.Rejects(t, "downloadFile", A{"deploymentId": id, "fileId": fileID}, "gone", 410)
		app.API.Rejects(t, "downloadAgentFile", A{"fileId": fileID}, "gone", 410)
		admin.Real.Rejects(t, "fetchFileToCloud", A{"deploymentId": id, "fileId": fileID}, "gone", 410)
		Eq(t, admin.Real.Call(t, "listFiles", A{"deploymentId": id}).At("items").Len(), 0)
		Eq(t, admin.Real.Call(t, "listFiles", A{"deploymentId": id, "query": A{"unpublished": true}}).At("items").Len(), 1, "the client sends ?unpublished=1")
		bearer := Opt{Headers: map[string]string{"Authorization": admin.Bearer}}
		Eq(t, Raw(t, "GET", "/v1/deployments/"+id+"/files?unpublished=true", bearer).J.At("items").Len(), 1)
		Eq(t, Raw(t, "GET", "/v1/deployments/"+id+"/files?unpublished=0", bearer).J.At("items").Len(), 0)
		Eq(t, admin.Real.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "file.unpublished"}}).At("items").Len(), 1)
		// tombstones older than FILE_TOMBSTONE_DAYS leave the cloud view
		UpdateDoc(t, "deployments/"+id+"/files/"+fileID, A{"unpublishedAt": ISO(time.Now().Add(-31 * 24 * time.Hour))})
		Eq(t, wfPollNow(t, app.API).At("files").Len(), 0)
		// unpublished while uploading: the open upload fails
		p2 := admin.Real.Call(t, "publishFile", A{"deploymentId": id, "body": A{"fileName": "b.bin", "sizeBytes": 3, "sha256": wfShaOf("abc")}})
		admin.Real.Call(t, "unpublishFile", A{"deploymentId": id, "fileId": p2.At("file.fileId")})
		admin.Real.Rejects(t, "getUpload", A{"uploadId": p2.At("upload.uploadId")}, "gone", 410)
		admin.Real.Rejects(t, "unpublishFile", A{"deploymentId": id, "fileId": "nope"}, "not-found")
		// the generic upload route does not start file uploads
		admin.Real.Rejects(t, "createUpload", A{"body": A{"purpose": "file", "sizeBytes": 1, "sha256": wfShaOf("a")}}, "invalid")
	})

	t.Run("files: a site registers (idempotent) + held; console fetch → fileFetches → the holder uploads → stored; site fetch request; agent unpublish", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		app := EnrollSite(t, admin, id, "app")
		db := EnrollSite(t, admin, id, "db")
		data := wfRandom(t, 9*1024*1024+77) // two parts
		body := wfReg(data, A{"fileName": "dump.sql", "note": "nightly", "via": "path", "sourcePath": "/srv/dump.sql"})
		sha := body["sha256"].(string)
		e0 := wfCursor(t, wfPollNow(t, app.API))
		f := app.API.Call(t, "registerAgentFile", A{"body": body})
		fid := f.At("fileId")
		Eq(t, f.At("state"), "ready")
		Eq(t, f.At("origin"), A{"kind": "site", "siteSlug": "app", "via": "path", "sourcePath": "/srv/dump.sql"})
		Eq(t, f.At("sites"), L{"app"})
		Eq(t, f.At("publishedBy"), "ops@example.com")
		Eq(t, app.API.Call(t, "registerAgentFile", A{"body": body}), f, "idempotent")
		db.API.Rejects(t, "registerAgentFile", A{"body": body}, "conflict", 409)
		app.API.Rejects(t, "registerAgentFile", A{"body": wfWith(body, A{"sha256": wfShaOf("other")})}, "conflict", 409)
		v1 := wfPollNow(t, db.API)
		Eq(t, wfCursor(t, v1), e0+1)
		Eq(t, v1.At("files.0.fileId"), fid)
		Eq(t, v1.At("fileFetches"), L{})
		pub := admin.Real.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "file.published"}})
		Eq(t, pub.At("items").Len(), 1)
		Eq(t, pub.At("items.0.origin"), "agent")
		// no cloud copy yet
		admin.Real.Rejects(t, "downloadFile", A{"deploymentId": id, "fileId": fid}, "conflict", 409)
		app.API.Rejects(t, "downloadAgentFile", A{"fileId": fid}, "conflict", 409)
		// db gets a copy over the mesh and reports it; the most recently seen holder is the one asked
		db.API.Call(t, "reportAgentFileHeld", A{"fileId": fid, "body": A{"held": true}})
		Eq(t, admin.Real.Call(t, "listFiles", A{"deploymentId": id}).At("items.0.sites"), L{"app", "db"})
		UpdateDoc(t, "deployments/"+id+"/agents/db", A{"lastSeenAt": ISO(time.Now().Add(-time.Hour))})
		UpdateDoc(t, "deployments/"+id+"/agents/app", A{"lastSeenAt": Now()})
		// the asked site cannot start before being asked
		app.API.Rejects(t, "startAgentFileUpload", A{"fileId": fid}, "conflict", 409)
		fx := admin.Real.Call(t, "fetchFileToCloud", A{"deploymentId": id, "fileId": fid})
		Eq(t, fx.At("cloud.fetch.from"), "app")
		Eq(t, fx.At("cloud.fetch.requestedBy"), admin.Email)
		Eq(t, fx.At("cloud.fetch.startedAt"), nil)
		again := admin.Real.Call(t, "fetchFileToCloud", A{"deploymentId": id, "fileId": fid})
		Eq(t, again.At("cloud.fetch"), fx.At("cloud.fetch"), "idempotent while open")
		v2 := wfPollNow(t, app.API)
		Eq(t, v2.At("fileFetches"), L{A{"fileId": fid, "from": "app"}})
		// the holder sends its copy (agent upload endpoints)
		st := app.API.Call(t, "startAgentFileUpload", A{"fileId": fid})
		Eq(t, st.At("upload.purpose"), "file")
		Eq(t, st.At("upload.partCount"), 2)
		resumed := app.API.Call(t, "startAgentFileUpload", A{"fileId": fid})
		Eq(t, resumed.At("upload.uploadId"), st.At("upload.uploadId"), "resumes the open upload")
		partSize := st.At("upload.partSize").Int()
		part := func(i int) []byte { return data[i*partSize : min(len(data), (i+1)*partSize)] }
		app.API.Call(t, "putAgentUploadPart", A{"uploadId": st.At("upload.uploadId"), "index": "0", "body": part(0)})
		mid := admin.Real.Call(t, "listFiles", A{"deploymentId": id}).At("items.0")
		Eq(t, mid.At("cloud.fetch.bytesDone"), st.At("upload.partSize"))
		True(t, mid.At("cloud.fetch.startedAt").Truthy())
		app.API.Call(t, "putAgentUploadPart", A{"uploadId": st.At("upload.uploadId"), "index": "1", "body": part(1)})
		app.API.Call(t, "completeAgentUpload", A{"uploadId": st.At("upload.uploadId")})
		v3 := wfPollNow(t, db.API)
		Eq(t, v3.At("fileFetches"), L{})
		Eq(t, v3.At("files.0.cloud"), A{"stored": true, "fetch": nil})
		fetched := admin.Real.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "file.fetched"}})
		Eq(t, fetched.At("items").Len(), 1)
		Eq(t, fetched.At("items.0.data"), A{"fileId": fid, "where": "cloud", "from": "app"})
		dl := admin.Real.Fetch(t, "downloadFile", A{"deploymentId": id, "fileId": fid})
		Eq(t, Sha256Hex(dl.Body), strings.TrimPrefix(sha, "sha256:"))
		rr := Raw(t, "GET", "/v1/deployments/"+id+"/files/"+fid.Str()+"/download", Opt{Headers: map[string]string{"Authorization": admin.Bearer, "Range": "bytes=0-9"}})
		Eq(t, rr.Status, 206)
		wfSameBytes(t, rr.Body, data[:10])
		adl := db.API.Fetch(t, "downloadAgentFile", A{"fileId": fid})
		Eq(t, len(adl.Body), len(data))
		app.API.Rejects(t, "startAgentFileUpload", A{"fileId": fid}, "conflict", 409)

		// a site asks the cloud for a copy it cannot get over the mesh
		small := wfRandom(t, 1000)
		g := db.API.Call(t, "registerAgentFile", A{"body": wfReg(small, A{"fileName": "g.txt"})})
		gid := g.At("fileId")
		app.API.Rejects(t, "requestAgentFileFetch", A{"fileId": gid, "body": A{"actor": "nope"}}, "invalid")
		rq := app.API.Call(t, "requestAgentFileFetch", A{"fileId": gid, "body": A{"actor": "ops@example.com"}})
		Eq(t, rq.At("cloud.fetch.from"), "db")
		Eq(t, rq.At("cloud.fetch.requestedBy"), "site:app")
		Eq(t, wfPollNow(t, db.API).At("fileFetches"), L{A{"fileId": gid, "from": "db"}})
		// the asked site dropped its copy (it was the only one): the fetch waits for a holder
		db.API.Call(t, "reportAgentFileHeld", A{"fileId": gid, "body": A{"held": false}})
		w := admin.Real.Call(t, "listFiles", A{"deploymentId": id}).At("items").Find("fileId", gid)
		Eq(t, w.At("sites"), L{})
		Eq(t, w.At("cloud.fetch.from"), nil)
		Eq(t, wfPollNow(t, db.API).At("fileFetches"), L{})
		db.API.Call(t, "reportAgentFileHeld", A{"fileId": gid, "body": A{"held": true}})
		Eq(t, wfPollNow(t, db.API).At("fileFetches"), L{A{"fileId": gid, "from": "db"}})
		su := db.API.Call(t, "startAgentFileUpload", A{"fileId": gid})
		db.API.Call(t, "putAgentUploadPart", A{"uploadId": su.At("upload.uploadId"), "index": "0", "body": small})
		db.API.Call(t, "completeAgentUpload", A{"uploadId": su.At("upload.uploadId")})
		wfSameBytes(t, app.API.Fetch(t, "downloadAgentFile", A{"fileId": gid}).Body, small)
		// no holder, no cloud copy → conflict
		h := app.API.Call(t, "registerAgentFile", A{"body": wfReg(small, A{"fileName": "h.txt"})})
		app.API.Call(t, "reportAgentFileHeld", A{"fileId": h.At("fileId"), "body": A{"held": false}})
		admin.Real.Rejects(t, "fetchFileToCloud", A{"deploymentId": id, "fileId": h.At("fileId")}, "conflict", 409)
		app.API.Rejects(t, "reportAgentFileHeld", A{"fileId": "missing", "body": A{"held": true}}, "not-found")

		// agent unpublish → tombstone everywhere, the cloud copy deleted
		app.API.Rejects(t, "unpublishAgentFile", A{"fileId": fid, "body": A{"actor": ""}}, "invalid")
		e1 := wfCursor(t, wfPollNow(t, db.API))
		Eq(t, app.API.Call(t, "unpublishAgentFile", A{"fileId": fid, "body": A{"actor": "ops@example.com"}}), A{"deleted": true})
		v4 := wfPollNow(t, db.API)
		Eq(t, wfCursor(t, v4), e1+1)
		tomb := v4.At("files").Find("fileId", fid)
		Eq(t, tomb.At("state"), "unpublished")
		Eq(t, tomb.At("unpublishedBy"), "ops@example.com")
		db.API.Rejects(t, "downloadAgentFile", A{"fileId": fid}, "gone", 410)
		app.API.Rejects(t, "requestAgentFileFetch", A{"fileId": fid, "body": A{"actor": "ops@example.com"}}, "gone", 410)
		un := admin.Real.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "file.unpublished"}})
		Eq(t, un.At("items.0.origin"), "agent")
		// re-register of the unpublished file returns its tombstone
		Eq(t, app.API.Call(t, "registerAgentFile", A{"body": body}).At("state"), "unpublished")
	})

	t.Run("files: permissions + validation", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		app := EnrollSite(t, admin, id, "app")
		ok := A{"fileName": "a.bin", "sizeBytes": 3, "sha256": wfShaOf("abc")}
		for _, c := range []struct {
			b    A
			code string
		}{
			{wfWith(ok, A{"fileName": "a/b"}), "invalid"},
			{wfWith(ok, A{"fileName": `a\b`}), "invalid"},
			{wfWith(ok, A{"fileName": "a\x00b"}), "invalid"},
			{wfWith(ok, A{"fileName": ""}), "invalid"},
			{wfWith(ok, A{"fileName": strings.Repeat("x", 256)}), "invalid"},
			{wfWith(ok, A{"sizeBytes": 0}), "invalid"},
			{wfWith(ok, A{"sha256": "sha256:abc"}), "invalid"},
			{wfWith(ok, A{"note": strings.Repeat("n", 501)}), "invalid"},
			{wfWith(ok, A{"extra": 1}), "invalid"},
			{wfWith(ok, A{"sizeBytes": 3 * 1024 * 1024 * 1024}), "too-large"},
		} {
			admin.Real.Rejects(t, "publishFile", A{"deploymentId": id, "body": c.b}, c.code)
		}
		data := []byte("abc")
		for _, b := range []A{
			wfReg(data, A{"fileId": "bad/id"}),
			wfReg(data, A{"fileId": ""}),
			wfReg(data, A{"publishedBy": "x"}),
			wfReg(data, A{"via": "mail"}),
			wfReg(data, A{"publishedAt": "never"}),
			wfReg(data, A{"via": "path", "sourcePath": "rel/path"}),
			wfReg(data, A{"fileName": ".."}),
		} {
			app.API.Rejects(t, "registerAgentFile", A{"body": b}, "invalid")
		}
		// a legacy transfer id is a valid fileId too
		Eq(t, app.API.Call(t, "registerAgentFile", A{"body": wfReg(data, A{"fileId": "AbC123xyz-legacy"})}).At("fileId"), "AbC123xyz-legacy")

		p := admin.Real.Call(t, "publishFile", A{"deploymentId": id, "body": ok})
		pid := p.At("file.fileId")
		reader := NewPerson(t, L{A{"resource": "deployment.transfers", "actions": L{"read"}, "scope": A{"kind": "all"}}})
		reader.Real.Rejects(t, "publishFile", A{"deploymentId": id, "body": ok}, "forbidden")
		reader.Real.Rejects(t, "unpublishFile", A{"deploymentId": id, "fileId": pid}, "forbidden")
		Eq(t, reader.Real.Call(t, "listFiles", A{"deploymentId": id}).At("items").Len(), 2)
		reader.Real.Rejects(t, "downloadFile", A{"deploymentId": id, "fileId": pid}, "conflict")
		nobody := NewPerson(t, L{A{"resource": "deployment.config", "actions": L{"read"}, "scope": A{"kind": "all"}}})
		nobody.Real.Rejects(t, "listFiles", A{"deploymentId": id}, "forbidden")
		nobody.Real.Rejects(t, "fetchFileToCloud", A{"deploymentId": id, "fileId": pid}, "forbidden")
		// another person cannot send the parts of the publisher's upload
		reader.Real.Rejects(t, "putUploadPart", A{"uploadId": p.At("upload.uploadId"), "index": "0", "body": data}, "not-found")
		// sha mismatch on complete: the upload fails, the file stays uploading
		bad := admin.Real.Call(t, "publishFile", A{"deploymentId": id, "body": wfWith(ok, A{"sha256": wfShaOf("xyz")})})
		admin.Real.Call(t, "putUploadPart", A{"uploadId": bad.At("upload.uploadId"), "index": "0", "body": data})
		admin.Real.Rejects(t, "completeUpload", A{"uploadId": bad.At("upload.uploadId")}, "invalid")
		Eq(t, admin.Real.Call(t, "listFiles", A{"deploymentId": id}).At("items").Find("fileId", bad.At("file.fileId")).At("state"), "uploading")
	})
}
