package apitest

import (
	"sort"
	"strings"
	"testing"

	"opm/agent/internal/model"
)

// waSome is array.some(fn).
func waSome(j J, fn func(J) bool) bool { return j.FindF(fn).Exists() }

// waEvery is array.every(fn).
func waEvery(j J, fn func(J) bool) bool {
	return len(j.Filter(fn)) == j.Len()
}

// waSorted is array.map(x => x[path]).sort().
func waSorted(items []J, path string) []string {
	out := []string{}
	for _, e := range items {
		out = append(out, e.At(path).Str())
	}
	sort.Strings(out)
	return out
}

// Port of backend/test/02-catalog.test.ts.
func TestCatalog(t *testing.T) {
	t.Run("registries: create (masked), get, patch resets lastCheck, reveal writes audit, delete conflict while used", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		admin.API.Rejects(t, "createRegistry", A{"body": A{"label": "", "username": "u", "password": "p"}}, "invalid", 422)
		admin.API.Rejects(t, "createRegistry", A{"body": A{"label": "x", "username": "u", "password": "p", "url": "http://insecure.example"}}, "invalid")
		admin.API.Rejects(t, "createRegistry", A{"body": A{"label": "x", "username": "u", "password": "p", "url": "https://r.example/path"}}, "invalid")
		e := admin.API.Rejects(t, "createRegistry", A{"body": A{"label": "x", "username": "u", "password": "p", "bogus": 1}}, "invalid")
		Match(t, e.Details, `unknown field bogus`)
		r := admin.API.Call(t, "createRegistry", A{"body": A{"label": "Hub", "username": "me", "password": "secret-pw"}})
		Eq(t, r.At("url"), "https://registry-1.docker.io")
		Eq(t, r.At("password"), model.Mask)
		Eq(t, r.At("isSet"), true)
		rid := r.At("registryId").Str()
		Eq(t, admin.API.Call(t, "getRegistry", A{"registryId": rid}).At("password"), model.Mask)
		list := admin.API.Call(t, "listRegistries", A{})
		True(t, waEvery(list.At("items"), func(x J) bool { return x.At("password").Str() == model.Mask }))
		UpdateDoc(t, "registries/"+rid, A{"lastCheck": A{"at": Now(), "ok": true, "message": "x"}})
		p1 := admin.API.Call(t, "patchRegistry", A{"registryId": rid, "body": A{"label": "Hub 2"}})
		True(t, p1.At("lastCheck").Truthy(), "label change keeps lastCheck")
		p2 := admin.API.Call(t, "patchRegistry", A{"registryId": rid, "body": A{"password": "new-pw"}})
		Eq(t, p2.At("lastCheck"), nil)
		rev := admin.API.Call(t, "revealRegistryPassword", A{"registryId": rid})
		Eq(t, rev.At("password"), "new-pw")
		Eq(t, rev.At("hideAfterSec"), 60)
		Eq(t, rev.At("value"), rev.At("password"))
		audit := admin.API.Call(t, "listAuditLog", A{"query": A{"limit": 50}})
		True(t, waSome(audit.At("items"), func(a J) bool {
			return a.At("action").Str() == "registry.password-revealed" && a.At("target").Str() == "registries/"+rid && a.At("actor").Str() == admin.UID
		}))
		// in use → conflict
		svc := admin.API.Call(t, "createService", A{"body": A{"label": "S", "serviceSlug": Uniq("svc"), "icon": "lucide:box"}})
		imgPath := "services/" + svc.At("serviceSlug").Str() + "/images/v1"
		SetDoc(t, imgPath, A{"version": "v1", "serviceSlug": svc.At("serviceSlug").V, "registryId": rid, "import": A{"status": "failed"}, "createdAt": Now()})
		c := admin.API.Rejects(t, "deleteRegistry", A{"registryId": rid}, "conflict", 409)
		Eq(t, c.Details.At("usedBy"), L{A{"serviceSlug": svc.At("serviceSlug").V, "version": "v1"}})
		NoErr(t, DB.Doc(imgPath).Delete())
		Eq(t, admin.API.Call(t, "deleteRegistry", A{"registryId": rid}), A{"deleted": true})
		admin.API.Rejects(t, "getRegistry", A{"registryId": rid}, "not-found", 404)
	})

	t.Run("registry test against an unreachable registry stores lastCheck ok=false", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		r := admin.API.Call(t, "createRegistry", A{"body": A{"label": "dead", "url": "https://127.0.0.1:1", "username": "u", "password": "p"}})
		tst := admin.API.Call(t, "testRegistry", A{"registryId": r.At("registryId")})
		Eq(t, tst.At("ok"), false)
		True(t, tst.At("message").Len() > 0)
		g := admin.API.Call(t, "getRegistry", A{"registryId": r.At("registryId")})
		Eq(t, g.At("lastCheck.ok"), false)
	})

	t.Run("permissions: developer resources need scope all; catalog read is implicit", func(t *testing.T) {
		nobody := NewPerson(t, L{})
		nobody.API.Rejects(t, "listRegistries", A{}, "forbidden", 403)
		nobody.API.Rejects(t, "createService", A{"body": A{"label": "S", "serviceSlug": Uniq("svc")}}, "forbidden")
		isArr := func(j J) bool { _, ok := j.V.([]any); return ok }
		True(t, isArr(nobody.API.Call(t, "listServices", A{}).At("items")), "services.read is implicit")
		True(t, isArr(nobody.API.Call(t, "listTags", A{}).At("items")), "tags.read is implicit")
		True(t, isArr(nobody.API.Call(t, "listNotFoundPages", A{}).At("items")))
		nobody.API.Rejects(t, "listTemplates", A{}, "forbidden")
		nobody.API.Rejects(t, "listAccounts", A{}, "forbidden")
		nobody.API.Rejects(t, "listAuditLog", A{}, "forbidden")
		scoped := NewPerson(t, L{A{"resource": "registries", "actions": L{"read"}, "scope": A{"kind": "tags", "tagSlugs": L{"x"}}}})
		scoped.API.Rejects(t, "listRegistries", A{}, "forbidden")
		reader := NewPerson(t, L{A{"resource": "registries", "actions": L{"read"}, "scope": A{"kind": "all"}}})
		True(t, reader.API.Call(t, "listRegistries", A{}).At("items").Truthy())
		r := reader.API.Call(t, "listRegistries", A{}).At("items.0")
		if r.Truthy() {
			reader.API.Rejects(t, "revealRegistryPassword", A{"registryId": r.At("registryId")}, "forbidden")
		}
	})

	t.Run("services: slug = id (unique, immutable), no repository; images: version = id, reference + registry match, source fix only while failed, env options", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		slug := Uniq("svc")
		admin.API.Rejects(t, "createService", A{"body": A{"label": "S", "serviceSlug": "Bad_Slug"}}, "invalid")
		admin.API.Rejects(t, "createService", A{"body": A{"label": "S", "serviceSlug": Uniq("s"), "repository": "a/b"}}, "invalid")
		admin.API.Rejects(t, "createService", A{"body": A{"label": "S", "serviceSlug": Uniq("s"), "icon": "https://x.example/a.png"}}, "invalid")
		bad := admin.API.Rejects(t, "createService", A{"body": A{"label": "S", "serviceSlug": Uniq("s"), "icon": "upload:AAAAAAAAAAAAAAAAAAAA"}}, "invalid")
		Match(t, bad.Details, `V_REF`)
		s := admin.API.Call(t, "createService", A{"body": A{"label": "Svc", "serviceSlug": slug, "description": "d"}})
		Eq(t, s.At("serviceSlug"), slug)
		Eq(t, s.At("icon"), nil, "icon defaults to null")
		Eq(t, s.At("serviceId"), Undefined)
		True(t, Doc(t, "services/"+slug).Exists(), "doc id = slug")
		admin.API.Rejects(t, "createService", A{"body": A{"label": "Svc2", "serviceSlug": slug}}, "conflict", 409)
		imm := admin.API.Rejects(t, "patchService", A{"serviceSlug": slug, "body": A{"serviceSlug": "other"}}, "invalid")
		Match(t, imm.Details, `V_IMMUTABLE`)
		p := admin.API.Call(t, "patchService", A{"serviceSlug": slug, "body": A{"label": "Svc!", "icon": "lucide:box"}})
		Eq(t, p.At("label"), "Svc!")
		Eq(t, p.At("icon"), "lucide:box")
		reg := admin.API.Call(t, "createRegistry", A{"body": A{"label": "dead", "url": "https://127.0.0.1:1", "username": "u", "password": "p"}})
		regID := reg.At("registryId").Str()
		// host mismatch → invalid; bad reference → V_REFERENCE
		hm := admin.API.Rejects(t, "registerImage", A{"serviceSlug": slug, "body": A{"version": "v1", "reference": "docker.io/library/busybox:1.36", "registryId": regID, "mode": "copy", "kind": "service"}}, "invalid")
		Match(t, hm.Details, `registryId`)
		br := admin.API.Rejects(t, "registerImage", A{"serviceSlug": slug, "body": A{"version": "v1", "reference": "not a ref", "mode": "copy", "kind": "service"}}, "invalid")
		Match(t, br.Details, `V_REFERENCE`)
		// registry auto-matched by host (first by label)
		host := Uniq("reg") + ".example"
		r2 := admin.API.Call(t, "createRegistry", A{"body": A{"label": "B match", "url": "https://" + host, "username": "u", "password": "p"}})
		admin.API.Call(t, "createRegistry", A{"body": A{"label": "C match", "url": "https://" + host, "username": "u", "password": "p"}})
		auto := admin.API.Call(t, "registerImage", A{"serviceSlug": slug, "body": A{"version": "auto", "reference": host + "/acme/api:1.0", "mode": "gateway", "kind": "service"}})
		Eq(t, auto.At("registryId"), r2.At("registryId"), "registry matched by host")
		Eq(t, auto.At("source.host"), host)
		NoErr(t, DB.Doc("services/"+slug+"/images/auto").Delete())
		img := admin.API.Call(t, "registerImage", A{
			"serviceSlug": slug,
			"body": A{
				"version":    "v1",
				"reference":  "127.0.0.1:1/acme/api:1.0",
				"registryId": regID,
				"mode":       "copy",
				"kind":       "service",
				"ports":      L{A{"name": "http", "port": 8080, "protocol": "tcp", "description": ""}},
				"env": L{
					A{"name": "A", "value": "x", "required": false, "secret": false, "description": ""},
					A{"name": "MODE", "value": "dc", "options": L{"dc", "worker"}, "required": false, "secret": false, "description": ""},
					A{"name": "LEGACY", "value": L{"b", "a"}, "required": false, "secret": false, "description": ""},
				},
			},
		})
		Eq(t, img.At("import.status"), "pending")
		Eq(t, img.At("version"), "v1")
		Eq(t, img.At("serviceSlug"), slug)
		Eq(t, img.At("registryId"), regID)
		Eq(t, img.At("source"), A{"host": "127.0.0.1:1", "repository": "acme/api", "tag": "1.0", "digest": nil})
		Eq(t, img.At("digest"), nil)
		Eq(t, img.At("platform"), "linux/amd64")
		envRows := L{}
		for _, e := range img.At("env").Arr() {
			envRows = append(envRows, L{e.At("name").V, e.At("value").V, e.At("options").V}) // .V: undefined must not pass for null
		}
		Eq(t, envRows, L{L{"A", "x", nil}, L{"MODE", "dc", L{"dc", "worker"}}, L{"LEGACY", "b", L{"b", "a"}}}, "env normalized, options always stored")
		vx := admin.API.Rejects(t, "registerImage", A{"serviceSlug": slug, "body": A{"version": "v1", "reference": "127.0.0.1:1/acme/api:1.0", "mode": "copy", "kind": "service"}}, "conflict", 409)
		Eq(t, vx.Details.At("reason"), "version-exists")
		admin.API.Rejects(t, "registerImage", A{"serviceSlug": slug, "body": A{"version": "Bad Version", "reference": "redis", "mode": "copy", "kind": "service"}}, "invalid")
		opt := admin.API.Rejects(t, "registerImage", A{"serviceSlug": slug, "body": A{"version": "v9", "reference": "redis:7", "registryId": nil, "mode": "copy", "kind": "service", "env": L{A{"name": "M", "value": "x", "options": L{"a", "b"}, "required": false, "secret": false, "description": ""}}}}, "invalid")
		Match(t, opt.Details, `V_OPTION`)
		admin.API.Rejects(t, "registerImage", A{"serviceSlug": slug, "body": A{"version": "v2", "reference": "127.0.0.1:1/acme/api:1.0", "registryId": "nope", "mode": "copy", "kind": "service"}}, "not-found")
		admin.API.Rejects(t, "deleteService", A{"serviceSlug": slug}, "conflict")
		cur := img
		for i := 0; i < 60 && cur.At("import.status").Str() != "failed"; i++ {
			Sleep(500)
			cur = admin.API.Call(t, "getImage", A{"serviceSlug": slug, "version": "v1"})
		}
		Eq(t, cur.At("import.status"), "failed", "trigger ran and failed")
		True(t, cur.At("import.error").Truthy())
		// source change only while failed
		iref := "services/" + slug + "/images/v1"
		UpdateDoc(t, iref, A{"import.status": "ready"})
		admin.API.Rejects(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"reference": "127.0.0.1:1/acme/other:1.0"}}, "conflict", 409)
		UpdateDoc(t, iref, A{"import.status": "failed"})
		fixed := admin.API.Call(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"reference": "docker.io/library/busybox:1.36", "registryId": nil}})
		Eq(t, fixed.At("registryId"), nil, "explicit null → public")
		Eq(t, fixed.At("source"), A{"host": "docker.io", "repository": "library/busybox", "tag": "1.36", "digest": nil})
		back := admin.API.Call(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"reference": "127.0.0.1:1/acme/api:1.0", "registryId": regID}})
		Eq(t, back.At("registryId"), regID)
		admin.API.Rejects(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"platform": "linux/arm64"}}, "invalid") // identity (kind is editable since §21 #82)
		admin.API.Rejects(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"ports": L{A{"name": "nope", "description": "x"}}}}, "invalid")
		eo := admin.API.Rejects(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"env": L{A{"name": "MODE", "value": "nope"}}}}, "invalid")
		Match(t, eo.Details, `V_OPTION`)
		pi := admin.API.Call(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"description": "new", "ports": L{A{"name": "http", "description": "web"}}, "env": L{A{"name": "A", "value": nil, "description": "no default"}, A{"name": "MODE", "value": "worker", "options": L{"worker", "dc", "pg"}}, A{"name": "LEGACY", "options": nil}}, "stopTimeoutSec": 10}})
		Eq(t, pi.At("ports.0.description"), "web")
		Eq(t, pi.At("env.0.value"), Undefined)
		Eq(t, pi.At("env.1.options"), L{"worker", "dc", "pg"})
		Eq(t, pi.At("env.1.value"), "worker")
		Eq(t, pi.At("env.2.options"), nil)
		Eq(t, pi.At("stopTimeoutSec"), 10)
		rr := admin.API.Call(t, "retryImport", A{"serviceSlug": slug, "version": "v1"})
		Eq(t, rr.At("import.status"), "pending")
		admin.API.Rejects(t, "retryImport", A{"serviceSlug": slug, "version": "v1"}, "conflict")
		Eq(t, admin.API.Call(t, "listImages", A{"serviceSlug": slug}).At("items").Len(), 1)
		Eq(t, admin.API.Call(t, "imageUsage", A{"serviceSlug": slug, "version": "v1"}).At("items"), L{})
		Eq(t, admin.API.Call(t, "serviceUsage", A{"serviceSlug": slug}).At("items"), L{})
		ls := admin.API.Call(t, "listServices", A{})
		Eq(t, ls.At("items").Find("serviceSlug", slug).At("imageCount"), 1)
		for i := 0; i < 60; i++ {
			g := admin.API.Call(t, "getImage", A{"serviceSlug": slug, "version": "v1"})
			if g.At("import.status").Str() == "failed" {
				break
			}
			Sleep(500)
		}
		admin.API.Call(t, "deleteImage", A{"serviceSlug": slug, "version": "v1"})
		Eq(t, admin.API.Call(t, "deleteService", A{"serviceSlug": slug}), A{"deleted": true})
		again := admin.API.Call(t, "createService", A{"body": A{"label": "Svc", "serviceSlug": slug}})
		Eq(t, again.At("serviceSlug"), slug, "slug is free again after delete")
	})

	t.Run("not-found pages: create-only (405), too-large, usage blocks delete", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		big := strings.Repeat("x", 262145)
		admin.API.Rejects(t, "createNotFoundPage", A{"body": A{"label": "big", "htmlCode": big}}, "too-large", 413)
		p := admin.API.Call(t, "createNotFoundPage", A{"body": A{"label": "Oops", "htmlCode": "<h1>Oops</h1>"}})
		id := p.At("notFoundPageId").Str()
		l := admin.API.Call(t, "listNotFoundPages", A{})
		item := l.At("items").Find("notFoundPageId", id)
		Eq(t, item.At("htmlCode"), Undefined)
		Eq(t, item.At("sizeBytes"), 13)
		d := admin.API.Call(t, "getNotFoundPage", A{"notFoundPageId": id})
		Eq(t, d.At("htmlCode"), "<h1>Oops</h1>")
		Eq(t, d.At("usedBy"), L{})
		r := Raw(t, "PATCH", "/v1/not-found-pages/"+id, Opt{Headers: map[string]string{"Authorization": admin.Bearer}})
		Eq(t, r.Status, 405)
		// used by a template
		tpl := admin.API.Call(t, "createTemplate", A{"body": A{"label": "T", "icon": "box"}})
		admin.API.Call(t, "patchTemplate", A{"templateId": tpl.At("templateId").Str(), "body": A{"ifVersion": 1, "sites": L{A{"siteSlug": "app", "label": "App", "config": A{"host": "127.0.0.1", "notFoundPageId": id}}}}})
		c := admin.API.Rejects(t, "deleteNotFoundPage", A{"notFoundPageId": id}, "conflict")
		Eq(t, c.Details.At("usedBy").Len(), 1)
		admin.API.Call(t, "deleteTemplate", A{"templateId": tpl.At("templateId")})
		Eq(t, admin.API.Call(t, "deleteNotFoundPage", A{"notFoundPageId": id}), A{"deleted": true})
	})

	t.Run("tags: create / conflict / patch / delete removes the slug from deployments and access scopes", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		slug := Uniq("tag")
		admin.API.Rejects(t, "createTag", A{"body": A{"tagSlug": slug, "label": "T", "icon": "lucide:tag", "color": "red"}}, "invalid")
		tg := admin.API.Call(t, "createTag", A{"body": A{"tagSlug": slug, "label": "Tag", "icon": "lucide:tag", "color": "#ff0000"}})
		Eq(t, tg.At("tagSlug"), slug)
		admin.API.Rejects(t, "createTag", A{"body": A{"tagSlug": slug, "label": "Tag", "icon": "lucide:tag", "color": "#ff0000"}}, "conflict")
		Eq(t, admin.API.Call(t, "patchTag", A{"tagSlug": slug, "body": A{"color": "#00ff00"}}).At("color"), "#00ff00")
		slug2 := Uniq("tag")
		admin.API.Call(t, "createTag", A{"body": A{"tagSlug": slug2, "label": "Tag2", "icon": "lucide:tag", "color": "#0000ff"}})
		d := admin.API.Call(t, "createDeployment", A{"body": A{"label": "tagged", "templateId": nil, "tagSlugs": L{slug, slug2}, "managerAccountUids": L{}}})
		user := NewPerson(t, L{
			A{"resource": "deployments", "actions": L{"read"}, "scope": A{"kind": "tags", "tagSlugs": L{slug}}},
			A{"resource": "deployment.config", "actions": L{"read"}, "scope": A{"kind": "tags", "tagSlugs": L{slug, slug2}}},
		})
		Eq(t, admin.API.Call(t, "listTags", A{}).At("items").Find("tagSlug", slug).At("deployments"), 1)
		res := admin.API.Call(t, "deleteTag", A{"tagSlug": slug})
		Eq(t, res.At("removedFrom"), A{"deployments": 1, "accessRules": 2})
		dep := admin.API.Call(t, "getDeployment", A{"deploymentId": d.At("deployment.deploymentId")})
		Eq(t, dep.At("tagSlugs"), L{slug2})
		acc := admin.API.Call(t, "getAccount", A{"uid": user.UID})
		Eq(t, acc.At("access").Len(), 1, "rule with only that tag removed")
		Eq(t, acc.At("access.0.scope"), A{"kind": "tags", "tagSlugs": L{slug2}})
		admin.API.Rejects(t, "deleteTag", A{"tagSlug": slug}, "not-found")
	})
}
