package apitest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wcVKSeedLegacyImage is seedLegacyImage: a READY node-red-like image written straight to the store, volumes WITHOUT kind
// (as stored before the fix).
func wcVKSeedLegacyImage(t testing.TB, slug string) {
	t.Helper()
	now := "2026-10-01T00:00:00.000Z"
	audit := func(m A) A {
		m["createdAt"], m["createdBy"], m["updatedAt"], m["updatedBy"] = now, "test", now, "test"
		return m
	}
	SetDoc(t, "services/"+slug, audit(A{"serviceSlug": slug, "label": slug, "icon": nil, "description": ""}))
	hx := func(k string) string { return Sha256Hex(slug + ":" + k) }
	SetDoc(t, "services/"+slug+"/images/v1", audit(A{
		"version":     "v1",
		"serviceSlug": slug,
		"reference":   "docker.io/nodered/node-red:4.0",
		"source":      A{"host": "docker.io", "repository": "nodered/node-red", "tag": "4.0", "digest": nil},
		"registryId":  nil,
		"digest":      "sha256:" + hx("m"),
		"platform":    "linux/amd64",
		"import":      A{"mode": "copy", "status": "ready", "error": nil, "sizeBytes": 100, "manifestMediaType": "application/vnd.oci.image.manifest.v1+json", "configDigest": "sha256:" + hx("c"), "layers": L{A{"digest": "sha256:" + hx("l"), "sizeBytes": 100, "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip"}}, "startedAt": now, "finishedAt": now},
		"kind":        "service",
		"entrypoint":  nil,
		"command":     nil,
		"workdir":     nil,
		"user":        nil,
		"ports":       L{A{"name": "http", "port": 1880, "protocol": "tcp", "description": ""}},
		"env":         L{},
		"volumes": L{
			A{"name": "flow", "mountPath": "/data/flows.json", "description": ""},
			A{"name": "settings", "mountPath": "/data/settings.js", "description": ""},
			A{"name": "lib", "mountPath": "/data/lib", "description": ""},
		},
		"healthcheck":    nil,
		"stopTimeoutSec": 30,
		"description":    "",
	}))
}

// wcVKNodeRed is nodeRed.
func wcVKNodeRed(slug, flowVolume string) A {
	return A{
		"containerSlug": slug,
		"serviceSlug":   slug,
		"version":       "v1",
		"enabled":       true,
		"env":           L{},
		"ports":         L{A{"portName": "http", "hostPort": nil, "hostIp": "0.0.0.0", "edge": true}},
		"volumes": L{
			A{"imageVolume": "flow", "mountPath": nil, "source": A{"kind": "volume", "volumeSlug": flowVolume}, "readOnly": false},
			A{"imageVolume": "settings", "mountPath": nil, "source": A{"kind": "none"}, "readOnly": false},
			A{"imageVolume": "lib", "mountPath": nil, "source": A{"kind": "none"}, "readOnly": false},
		},
		"command":     nil,
		"entrypoint":  nil,
		"restart":     "always",
		"resources":   A{"cpus": nil, "memoryMb": nil},
		"healthcheck": nil,
		"depends":     L{},
	}
}

// wcVKFakeRegistry serves one image with the given OCI config on a local plain-HTTP registry and stores a registries
// document for it (written directly: the API accepts https URLs only). It returns the registry id and host.
func wcVKFakeRegistry(t *testing.T, repo, tag string, config A) (string, string) {
	t.Helper()
	cfg := []byte(show(config))
	digest := "sha256:" + Sha256Hex(cfg)
	const mt = "application/vnd.oci.image.manifest.v1+json"
	manifest := []byte(show(A{"schemaVersion": 2, "mediaType": mt, "config": A{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digest, "size": len(cfg)}, "layers": L{}}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(200)
		case "/v2/" + repo + "/manifests/" + tag:
			w.Header().Set("Content-Type", mt)
			w.Header().Set("Docker-Content-Digest", "sha256:"+Sha256Hex(manifest))
			_, _ = w.Write(manifest)
		case "/v2/" + repo + "/blobs/" + digest:
			_, _ = w.Write(cfg)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	id := Uniq("RegVolumeKind")
	now := Now()
	SetDoc(t, "registries/"+id, A{"registryId": id, "label": "fake", "url": srv.URL, "username": "", "password": "", "lastCheck": nil, "createdAt": now, "createdBy": "test", "updatedAt": now, "updatedBy": "test"})
	t.Cleanup(func() { NoErr(t, DB.Doc("registries/"+id).Delete()) })
	return id, strings.TrimPrefix(srv.URL, "http://")
}

// Port of backend/test/15-volume-kind.test.ts.
func TestVolumeKind(t *testing.T) {
	t.Run("register / patch / read: kind explicit (absent → inferred from the path), invalid kind → V_ENUM, inspect volumes = dir", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		slug := Uniq("vk")
		admin.API.Call(t, "createService", A{"body": A{"label": slug, "serviceSlug": slug}})
		reg := admin.API.Call(t, "createRegistry", A{"body": A{"label": "dead", "url": "https://127.0.0.1:1", "username": "u", "password": "p"}})
		body := A{
			"version":    "v1",
			"reference":  "127.0.0.1:1/acme/node-red:4",
			"registryId": reg.At("registryId"),
			"mode":       "copy",
			"kind":       "service",
			"volumes": L{
				A{"name": "flow", "mountPath": "/data/flows.json", "description": ""},             // auto → file
				A{"name": "data", "mountPath": "/data", "description": ""},                        // auto → dir
				A{"name": "conf", "mountPath": "/etc/app.conf", "kind": "dir", "description": ""}, // explicit wins
			},
		}
		with := func(version string, vols L) A {
			o := A{}
			for k, v := range body {
				o[k] = v
			}
			o["version"], o["volumes"] = version, vols
			return o
		}
		img := admin.API.Call(t, "registerImage", A{"serviceSlug": slug, "body": body})
		Eq(t, wcRows(img.At("volumes"), "name", "kind"), L{L{"flow", "file"}, L{"data", "dir"}, L{"conf", "dir"}})
		imgPath := "services/" + slug + "/images/v1"
		Eq(t, Doc(t, imgPath).At("volumes").Pluck("kind"), L{"file", "dir", "dir"}, "stored explicitly")
		bad := admin.API.Rejects(t, "registerImage", A{"serviceSlug": slug, "body": with("v2", L{A{"name": "x", "mountPath": "/x", "kind": "folder", "description": ""}})}, "invalid", 422)
		True(t, strings.Contains(bad.Details.String(), "volumes[x].kind") && strings.Contains(bad.Details.String(), "V_ENUM"), bad.Details)
		slash := admin.API.Rejects(t, "registerImage", A{"serviceSlug": slug, "body": with("v3", L{A{"name": "x", "mountPath": "/x/", "kind": "file", "description": ""}})}, "invalid", 422)
		Match(t, slash.Details, `V_VOLUME_KIND`)
		// patch: change kind (Folder → File) + a new row without kind (inferred)
		vols := L{}
		for _, v := range img.At("volumes").Arr() {
			if v.At("name").Str() == "conf" {
				v = wcSpread(v, A{"kind": "file"})
			}
			vols = append(vols, v.V)
		}
		p := admin.API.Call(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"volumes": append(vols, A{"name": "settings", "mountPath": "/data/settings.js", "description": ""})}})
		Eq(t, wcRows(p.At("volumes"), "name", "kind"), L{L{"flow", "file"}, L{"data", "dir"}, L{"conf", "file"}, L{"settings", "file"}})
		Eq(t, Doc(t, imgPath).At("volumes").Pluck("kind"), L{"file", "dir", "file", "file"})
		pb := admin.API.Rejects(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"volumes": L{A{"name": "flow", "mountPath": "/data/flows.json", "kind": "blob", "description": ""}}}}, "invalid", 422)
		Match(t, pb.Details, `V_ENUM`)
		// registry VOLUME entries are folders, even with an extension in the path
		// (prefillFromConfig is private in Go: it is reached through inspectImage, on a fake registry)
		regID, host := wcVKFakeRegistry(t, "acme/app", "1", A{"architecture": "amd64", "os": "linux", "config": A{"Volumes": A{"/data": A{}, "/data/flows.json": A{}}}})
		pre := admin.API.Call(t, "inspectImage", A{"serviceSlug": slug, "body": A{"reference": host + "/acme/app:1", "registryId": regID, "platform": "linux/amd64"}})
		Eq(t, wcRows(pre.At("volumes"), "mountPath", "kind"), L{L{"/data", "dir"}, L{"/data/flows.json", "dir"}})
	})

	t.Run("container kind matrix through the draft API + migrate-f (legacy image: dry run → apply → rerun 0, verify-schema, audit)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		slug := Uniq("nr")
		wcVKSeedLegacyImage(t, slug)
		// legacy doc read through the API: kinds inferred
		got := admin.API.Call(t, "getImage", A{"serviceSlug": slug, "version": "v1"})
		Eq(t, wcRows(got.At("volumes"), "name", "kind"), L{L{"flow", "file"}, L{"settings", "file"}, L{"lib", "dir"}})
		id := HelloDeployment(t, admin).ID()
		draft := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		app := wcApp(draft.At("draft.sites")).Clone()
		wcPush(app.At("config"), "volumes", wcSpread(wcVolume("nodered-flow"), A{"kind": "file"}), wcVolume("nodered-data"))
		// folder volume on the File image volume flow → error (save level)
		wcPush(app.At("config"), "containers", wcVKNodeRed(slug, "nodered-data"))
		e := admin.API.Rejects(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": app}}, "invalid", 422)
		issue := e.Details.At("errors").Find("code", "V_VOLUME_KIND_MISMATCH")
		True(t, issue.Truthy(), e.Details)
		Eq(t, issue.At("path"), "sites[app].config.containers["+slug+"].volumes[flow].source.volumeSlug")
		Match(t, issue.At("message"), `folder volume nodered-data cannot be mounted on file path /data/flows\.json .* use a single-file volume, or change the image volume to Folder`)
		// the user's case: single-file volume on the File image volume → no V_VOLUME_KIND_IMAGE, only the rename info
		cs := app.At("config.containers").V.(L)
		cs[len(cs)-1] = Plain(wcVKNodeRed(slug, "nodered-flow"))
		app.At("config").Map()["volumes"] = Plain(app.At("config.volumes").Filter(func(v J) bool { return v.At("volumeSlug").Str() != "nodered-data" }))
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": app}})
		mine := JOf(w.At("validation.issues").Filter(func(x J) bool { return strings.Contains(x.At("path").Str(), "containers["+slug+"]") }))
		rows := JOf(mine.Filter(func(x J) bool {
			return strings.HasPrefix(x.At("code").Str(), "V_VOLUME_KIND") || x.At("code").Str() == "V_FILE_VOLUME_RENAME"
		}))
		Eq(t, wcRows(rows, "code", "severity"), L{L{"V_FILE_VOLUME_RENAME", "info"}}, mine)
		// the rest of the TS test ran scripts/migrate-f.ts + verify-schema: TS-only, gone with them
	})
}
