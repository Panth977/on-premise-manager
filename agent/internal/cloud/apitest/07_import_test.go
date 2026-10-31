package apitest

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud/core"
)

// Port of backend/test/07-import.test.ts: image import against the real Artifact Registry test repo (skipped when offline
// or without the reader key).
func TestImport(t *testing.T) {
	const (
		ref = "registry.example.com/opm-test/busybox:1.36"
		ar  = "https://asia-south1-docker.pkg.dev"
	)
	online := func() bool {
		c := http.Client{Timeout: 5 * time.Second}
		r, err := c.Get(ar + "/v2/")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 401 || (r.StatusCode >= 200 && r.StatusCode < 300)
	}

	t.Run("registry test + inspect + copy import + gateway import against Artifact Registry", func(t *testing.T) {
		key, err := os.ReadFile(filepath.Join(RepoRoot, ".secrets/registry-reader-key.json"))
		if err != nil {
			t.Skip("no registry reader key")
		}
		if !online() {
			t.Skip("offline")
		}
		settled := func(j J) bool {
			s := j.At("import.status").Str()
			return s != "pending" && s != "copying"
		}
		blobExists := func(hex string) bool { return Bucket.Exists(t, "blobs/sha256/"+hex) }
		admin := NewPerson(t, "admin")
		reg := admin.API.Call(t, "createRegistry", A{"body": A{"label": "AR test", "url": ar, "username": "_json_key", "password": string(key)}})
		regID := reg.At("registryId").Str()
		svc := admin.API.Call(t, "createService", A{"body": A{"label": "Busybox", "serviceSlug": Uniq("busybox"), "icon": "lucide:box"}})
		slug := svc.At("serviceSlug").Str()
		tst := admin.API.Call(t, "testRegistry", A{"registryId": regID})
		Eq(t, tst.At("ok"), true, tst.At("message").Str())
		Eq(t, tst.At("message"), "login ok")
		bad := admin.API.Call(t, "createRegistry", A{"body": A{"label": "AR bad", "url": ar, "username": "_json_key", "password": `{"bad":true}`}})
		Eq(t, admin.API.Call(t, "testRegistry", A{"registryId": bad.At("registryId")}).At("ok"), false)
		ins := admin.API.Call(t, "inspectImage", A{"serviceSlug": slug, "body": A{"reference": ref, "registryId": regID, "platform": "linux/arm64"}})
		Match(t, ins.At("digest"), `^sha256:[0-9a-f]{64}$`)
		Eq(t, ins.At("layers"), 1)
		True(t, ins.At("sizeBytes").Num() > 100_000)
		Eq(t, ins.At("command"), L{"sh"})
		admin.API.Rejects(t, "inspectImage", A{"serviceSlug": slug, "body": A{"reference": ref, "registryId": regID, "platform": "linux/amd64"}}, "invalid")
		admin.API.Rejects(t, "inspectImage", A{"serviceSlug": slug, "body": A{"reference": strings.Replace(ref, ":1.36", ":no-such-tag", 1), "registryId": regID, "platform": "linux/arm64"}}, "not-found")
		// gateway mode: manifest + config only
		gsvc := admin.API.Call(t, "createService", A{"body": A{"label": "Busybox gw", "serviceSlug": Uniq("bbgw"), "icon": "lucide:box"}})
		gslug := gsvc.At("serviceSlug").Str()
		const layerHex = "821d9dafb26dc8e4afce2273366520f49e38f8f679a72fc512f2a83e62f442b7"
		Bucket.Delete(t, "blobs/sha256/"+layerHex)
		admin.API.Call(t, "registerImage", A{"serviceSlug": gslug, "body": A{"version": "v1", "reference": ref, "registryId": regID, "platform": "linux/arm64", "mode": "gateway", "kind": "service", "command": L{"sh"}}})
		g := admin.API.Call(t, "getImage", A{"serviceSlug": gslug, "version": "v1"})
		for i := 0; i < 240 && !settled(g); i++ {
			Sleep(500)
			g = admin.API.Call(t, "getImage", A{"serviceSlug": gslug, "version": "v1"})
		}
		Eq(t, g.At("import.status"), "ready", g.At("import.error").Str())
		Eq(t, g.At("digest"), ins.At("digest"))
		Eq(t, blobExists(g.At("digest").Str()[7:]), true, "manifest stored")
		Eq(t, blobExists(g.At("import.configDigest").Str()[7:]), true, "config stored")
		Eq(t, blobExists(layerHex), false, "gateway does not copy layers")
		gi := g.Clone()
		gi.Map()["registryId"] = regID
		NoErr(t, core.FetchLayer(toMap(t, gi), "sha256:"+layerHex))
		Eq(t, blobExists(layerHex), true, "first fetch streams the layer into Storage")
		Bucket.Delete(t, "blobs/sha256/"+layerHex)
		// copy mode
		img := admin.API.Call(t, "registerImage", A{"serviceSlug": slug, "body": A{"version": "v1", "reference": ref, "registryId": regID, "platform": "linux/arm64", "mode": "copy", "kind": "service", "command": L{"sh"}}})
		Eq(t, img.At("import.status"), "pending")
		cur := img
		for i := 0; i < 600 && !settled(cur); i++ {
			Sleep(500)
			cur = admin.API.Call(t, "getImage", A{"serviceSlug": slug, "version": "v1"})
		}
		Eq(t, cur.At("import.status"), "ready", cur.At("import.error").Str())
		Eq(t, cur.At("digest"), ins.At("digest"))
		Eq(t, cur.At("import.layers").Len(), 1)
		Eq(t, cur.At("import.layers.0.digest"), "sha256:"+layerHex)
		Eq(t, cur.At("import.sizeBytes"), cur.At("import.layers.0.sizeBytes"))
		True(t, cur.At("import.startedAt").Truthy() && cur.At("import.finishedAt").Truthy())
		meta, err := Bucket.B.Head("blobs/sha256/" + layerHex)
		NoErr(t, err)
		True(t, meta != nil, "No such object")
		Eq(t, meta.Size, cur.At("import.layers.0.sizeBytes"), "layer copied, size matches")
		// wrong platform → failed with the message
		asvc := admin.API.Call(t, "createService", A{"body": A{"label": "Busybox amd", "serviceSlug": Uniq("bbamd"), "icon": "lucide:box"}})
		aslug := asvc.At("serviceSlug").Str()
		admin.API.Call(t, "registerImage", A{"serviceSlug": aslug, "body": A{"version": "v1", "reference": ref, "registryId": regID, "platform": "linux/amd64", "mode": "copy", "kind": "service"}})
		a := admin.API.Call(t, "getImage", A{"serviceSlug": aslug, "version": "v1"})
		for i := 0; i < 240 && !settled(a); i++ {
			Sleep(500)
			a = admin.API.Call(t, "getImage", A{"serviceSlug": aslug, "version": "v1"})
		}
		Eq(t, a.At("import.status"), "failed")
		Match(t, a.At("import.error").Str(), `platform linux/amd64 not in the image`)
	})
}
