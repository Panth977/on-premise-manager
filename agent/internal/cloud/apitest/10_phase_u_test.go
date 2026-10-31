package apitest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
)

var wbPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

// wbAuditByRequest is auditByRequest().
func wbAuditByRequest(t testing.TB, requestID string) []J {
	t.Helper()
	q, err := DB.Collection("auditLog").Where("requestId", "==", requestID).Get()
	NoErr(t, err)
	out := []J{}
	for _, d := range q.Docs {
		j := JOf(d.Data())
		out = append(out, wbWith(j, A{"data": ParseJ([]byte(j.At("_data").Str()))}))
	}
	return out
}

// wbUpload is upload(); name is optional.
func wbUpload(t testing.TB, bearer string, body []byte, ctype string, name ...string) *Resp {
	t.Helper()
	p := "/v1/icons"
	if len(name) > 0 {
		p += "?name=" + doc.EncodeURIComponent(name[0])
	}
	return Raw(t, "POST", p, Opt{Headers: map[string]string{"Authorization": bearer, "Content-Type": ctype}, Body: body})
}

// wbJwt is jwt().
func wbJwt(payload any) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(show(payload))) + ".sig"
}

// wbServe is serve(): closed at the end of the test.
func wbServe(t testing.TB, h http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

// wbJSON is json().
func wbJSON(w http.ResponseWriter, status int, body any, headers ...map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	for _, h := range headers {
		for k, v := range h {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(show(body)))
}

// wbFindIndex is list.findIndex((x) => x[key] === val).
func wbFindIndex(list J, key string, val any) int {
	want := Plain(val)
	for i, e := range list.Arr() {
		if Plain(e.At(key)) == want {
			return i
		}
	}
	return -1
}

func wbAccessErr(t testing.TB, err error) *core.RegistryError {
	t.Helper()
	a := core.IsRegistryAccessError(err)
	if a == nil {
		t.Fatalf("expected a registry access error, got %v", err)
	}
	return a
}

// Port of backend/test/10-phase-u.test.ts.
func TestPhaseU(t *testing.T) {
	t.Run("icons: sniff + svg sanitizer (unit)", func(t *testing.T) {
		Eq(t, core.SniffIcon(wbPNG), "image/png")
		Eq(t, core.SniffIcon(append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 8)...)), "image/webp")
		Eq(t, core.SniffIcon([]byte(`<?xml version="1.0"?>`+"\n"+`<svg xmlns="http://www.w3.org/2000/svg"/>`)), "image/svg+xml")
		Eq(t, core.SniffIcon([]byte("GIF89a....")), "") // "" = null
		dirty := `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" onload="alert(1)" viewBox="0 0 10 10">` +
			`<script>alert(2)</script><foreignObject><div onclick="x()">hi</div></foreignObject>` +
			`<a href="javascript:alert(3)"><circle r="4" fill="url(#g)" style="fill:url(https://evil.example/x)"/></a>` +
			`<use xlink:href="#c"/><use xlink:href="https://evil.example/s.svg#x"/><image href="data:image/png;base64,iVBORw0KGgo="/>` +
			`<style>@import url(https://evil.example/a.css);</style><style>.a{fill:red}</style><rect width="1" height="1" class="a"/></svg>`
		clean, err := core.SanitizeSvg(dirty)
		NoErr(t, err)
		for _, bad := range []string{"<script", "alert", "foreignObject", "onload", "onclick", "javascript:", "evil.example", "@import"} {
			NotContains(t, clean, bad, bad+" removed: "+clean)
		}
		for _, good := range []string{`fill="url(#g)"`, `xlink:href="#c"`, "data:image/png;base64", ".a{fill:red}", `viewBox="0 0 10 10"`} {
			Contains(t, clean, good, good+" kept: "+clean)
		}
		for _, s := range []string{`<html><body/></html>`, `<svg><g></svg>`, `<!DOCTYPE svg [<!ENTITY x "y">]><svg/>`} {
			_, err := core.SanitizeSvg(s)
			True(t, err != nil, s)
		}
	})

	t.Run("icons: upload, dedupe, svg sanitizing, limits, list usedBy, delete 409, public content, audit", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		nobody := NewPerson(t, L{})
		name := Uniq("png")
		up := wbUpload(t, admin.Bearer, wbPNG, "image/png", name)
		Eq(t, up.Status, 201, up.Text())
		icon := up.J.At("icon")
		iconID := icon.At("iconId").Str()
		Eq(t, up.J.At("existing"), false)
		Match(t, iconID, `^[A-Za-z0-9]{20}$`)
		Eq(t, icon.At("name"), name)
		Eq(t, icon.At("contentType"), "image/png")
		Eq(t, icon.At("sizeBytes"), len(wbPNG))
		Match(t, icon.At("sha256"), `^sha256:[0-9a-f]{64}$`)
		a := wbAuditByRequest(t, up.Header("x-request-id"))
		Eq(t, len(a), 1)
		Eq(t, a[0].At("action"), "icon.uploaded")
		Eq(t, a[0].At("target"), "icons/"+iconID)
		Eq(t, a[0].At("data.existing"), false)
		// same bytes → existing
		again := wbUpload(t, admin.Bearer, wbPNG, "image/png", "other")
		Eq(t, again.Status, 201)
		Eq(t, again.J.At("existing"), true)
		Eq(t, again.J.At("icon.iconId"), iconID)
		a = wbAuditByRequest(t, again.Header("x-request-id"))
		Eq(t, a[0].At("data.existing"), true)
		Eq(t, a[0].At("target"), "icons/"+iconID)
		// svg sanitized
		svg := wbUpload(t, admin.Bearer, []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)" data-u="`+Uniq("x")+`"><script>alert(1)</script><circle r="1"/></svg>`), "image/svg+xml")
		Eq(t, svg.Status, 201, svg.Text())
		Eq(t, svg.J.At("icon.name"), "icon", "default name")
		// errors
		Eq(t, wbUpload(t, admin.Bearer, bytes.Repeat([]byte{0x89}, 65537), "image/png").Status, 413)
		wrong := wbUpload(t, admin.Bearer, []byte("GIF89a"), "image/gif")
		Eq(t, wrong.Status, 422)
		Eq(t, wbUpload(t, admin.Bearer, []byte("GIF89a......"), "image/png").Status, 422, "magic bytes must match")
		Eq(t, wbUpload(t, admin.Bearer, wbPNG, "image/svg+xml").Status, 422, "content-type mismatch")
		Eq(t, wbUpload(t, admin.Bearer, []byte("<svg><g></svg>"), "image/svg+xml").Status, 422, "not parseable")
		Eq(t, wbUpload(t, admin.Bearer, wbPNG, "image/png", strings.Repeat("x", 81)).Status, 422)
		Eq(t, wbUpload(t, nobody.Bearer, wbPNG, "image/png").Status, 403)
		// public content + headers
		c := Raw(t, "GET", "/v1/icons/"+iconID+"/content", Opt{Headers: map[string]string{"Origin": "https://elsewhere.example"}})
		Eq(t, c.Status, 200)
		Eq(t, c.Body, wbPNG)
		Eq(t, c.Header("content-type"), "image/png")
		Eq(t, c.Header("cache-control"), "public, max-age=31536000, immutable")
		Eq(t, c.Header("x-content-type-options"), "nosniff")
		Eq(t, c.Header("access-control-allow-origin"), "*")
		sc := Raw(t, "GET", "/v1/icons/"+svg.J.At("icon.iconId").Str()+"/content")
		Eq(t, sc.Header("content-type"), "image/svg+xml")
		Eq(t, sc.Header("content-security-policy"), "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		True(t, !strings.Contains(sc.Text(), "script") && !strings.Contains(sc.Text(), "onload"), sc.Text())
		Eq(t, Raw(t, "GET", "/v1/icons/AAAAAAAAAAAAAAAAAAAA/content").Status, 404)
		// users: service, tag, deployment, account
		ref := "upload:" + iconID
		svc := admin.API.Call(t, "createService", A{"body": A{"label": "Iconic", "serviceSlug": Uniq("ico"), "icon": ref}})
		tagSlug := Uniq("tag")
		admin.API.Call(t, "createTag", A{"body": A{"tagSlug": tagSlug, "label": "T", "icon": ref, "color": "#112233"}})
		dep := admin.API.Call(t, "createDeployment", A{"body": A{"label": "iconic", "templateId": nil, "tagSlugs": L{}, "managerAccountUids": L{}, "image": ref}})
		admin.API.Call(t, "patchAccount", A{"uid": admin.UID, "body": A{"img": ref}})
		admin.API.Rejects(t, "patchAccount", A{"uid": admin.UID, "body": A{"img": "upload:BBBBBBBBBBBBBBBBBBBB"}}, "invalid")
		admin.API.Rejects(t, "patchAccount", A{"uid": admin.UID, "body": A{"img": "javascript:x"}}, "invalid")
		list := admin.API.Call(t, "listIcons", nil)
		Eq(t, list.At("items").Find("iconId", iconID).At("usedBy"), 4)
		True(t, wbFindIndex(list.At("items"), "iconId", svg.J.At("icon.iconId")) < wbFindIndex(list.At("items"), "iconId", iconID), "newest first")
		Eq(t, list.At("items.0._bytes"), Undefined)
		del := Raw(t, "DELETE", "/v1/icons/"+iconID, Opt{Headers: map[string]string{"Authorization": admin.Bearer}})
		Eq(t, del.Status, 409)
		types := []string{}
		for _, u := range del.J.At("error.details.users").Arr() {
			types = append(types, u.At("type").Str())
		}
		sort.Strings(types)
		Eq(t, types, []string{"account", "deployment", "service", "tag"})
		Eq(t, wbAuditByRequest(t, del.Header("x-request-id"))[0].At("action"), "icon.deleted")
		admin.API.Call(t, "patchService", A{"serviceSlug": svc.At("serviceSlug"), "body": A{"icon": nil}})
		admin.API.Call(t, "patchTag", A{"tagSlug": tagSlug, "body": A{"icon": "lucide:tag"}})
		admin.API.Call(t, "patchDeployment", A{"deploymentId": dep.At("deployment.deploymentId"), "body": A{"image": "https://example.com/logo.png"}})
		admin.API.Call(t, "patchAccount", A{"uid": admin.UID, "body": A{"img": nil}})
		nobody.API.Rejects(t, "deleteIcon", A{"iconId": iconID}, "forbidden")
		ok := Raw(t, "DELETE", "/v1/icons/"+iconID, Opt{Headers: map[string]string{"Authorization": admin.Bearer}})
		Eq(t, ok.Status, 200)
		da := wbAuditByRequest(t, ok.Header("x-request-id"))[0]
		Eq(t, da.At("target"), "icons/"+iconID)
		Eq(t, da.At("outcome"), "ok")
		Eq(t, Raw(t, "GET", "/v1/icons/"+iconID+"/content").Status, 404)
		admin.API.Rejects(t, "deleteIcon", A{"iconId": iconID}, "not-found")
	})

	t.Run("revision catalogs embed uploaded service icons as data: URLs", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		SeedHelloCatalog(t)
		up := wbUpload(t, admin.Bearer, []byte(`<svg xmlns="http://www.w3.org/2000/svg" id="`+Uniq("x")+`"/>`), "image/svg+xml")
		iconID := up.J.At("icon.iconId").Str()
		UpdateDoc(t, "services/redis", A{"icon": "upload:" + iconID})
		UpdateDoc(t, "services/hello-api", A{"icon": nil})
		defer SeedHelloCatalog(t)
		loaded, err := core.LoadCatalog()
		NoErr(t, err)
		built, err := core.BuildRevisionCatalog(toMap(t, Hello(t).At("config")), loaded)
		NoErr(t, err)
		cat := JOf(built)
		redis := cat.At("services.redis.icon").Str()
		Match(t, redis, `^data:image/svg\+xml;base64,`)
		dec, err := base64.StdEncoding.DecodeString(strings.Split(redis, ",")[1])
		NoErr(t, err)
		Eq(t, string(dec), Raw(t, "GET", "/v1/icons/"+iconID+"/content").Text())
		Eq(t, cat.At("services").Key("hello-web").At("icon"), "lucide:globe")
		Eq(t, cat.At("services").Key("hello-api").At("icon"), nil)
	})

	t.Run("registries: anonymous (both empty) allowed, exactly one empty → invalid", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		r := admin.API.Call(t, "createRegistry", A{"body": A{"label": "Hub public", "username": "", "password": ""}})
		Eq(t, r.At("isSet"), false)
		admin.API.Rejects(t, "createRegistry", A{"body": A{"label": "x", "username": "u", "password": ""}}, "invalid")
		admin.API.Rejects(t, "createRegistry", A{"body": A{"label": "x", "username": "", "password": "p"}}, "invalid")
		admin.API.Rejects(t, "patchRegistry", A{"registryId": r.At("registryId"), "body": A{"username": "only-user"}}, "invalid")
		admin.API.Call(t, "deleteRegistry", A{"registryId": r.At("registryId")})
	})

	t.Run("registry JWT access claim: token without pull access → clear error + accessible repositories hint", func(t *testing.T) {
		Eq(t, core.TokenGrantsPull(wbJwt(A{"access": L{}}), "northwind/backend"), false)
		Eq(t, core.TokenGrantsPull(wbJwt(A{"access": L{A{"type": "repository", "name": "northwind/northwind", "actions": L{"pull"}}}}), "northwind/northwind"), true)
		Eq(t, core.TokenGrantsPull("opaque-token", "x/y"), nil)
		reScope := regexp.MustCompile(`^repository:(.+):pull$`)
		stub := wbServe(t, func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			p := r.URL.Path
			if p == "/token" {
				access := L{}
				if m := reScope.FindStringSubmatch(r.URL.Query().Get("scope")); m != nil && m[1] != "acme/secret" {
					access = L{A{"type": "repository", "name": m[1], "actions": L{"pull"}}}
				}
				wbJSON(w, 200, A{"token": wbJwt(A{"access": access})})
				return
			}
			switch {
			case !strings.HasPrefix(auth, "Bearer "):
				wbJSON(w, 401, A{"errors": L{A{"code": "UNAUTHORIZED"}}}, map[string]string{"WWW-Authenticate": `Bearer realm="http://` + r.Host + `/token",service="stub"`})
			case p == "/v2/":
				wbJSON(w, 200, A{})
			case p == "/v2/_catalog":
				wbJSON(w, 200, A{"repositories": L{"acme/api", "acme/web", "other/x"}})
			case p == "/v2/acme/api/tags/list":
				wbJSON(w, 200, A{"name": "acme/api", "tags": L{"2.0", "1.0"}})
			case strings.HasPrefix(p, "/v2/acme/secret/"):
				wbJSON(w, 401, A{"errors": L{A{"code": "UNAUTHORIZED", "message": "authentication required"}}})
			default:
				wbJSON(w, 404, A{"errors": L{A{"code": "NOT_FOUND"}}})
			}
		})
		creds := core.RegistryCreds{URL: stub, Username: "bob", Password: "pw"}
		cl := core.NewRegistryClient(creds, 0)
		_, err := cl.Manifest("acme/secret", "1.0")
		Eq(t, wbAccessErr(t, err).Message, "repository acme/secret not found, or account bob has no pull access to it (the registry answers 401 for both)")
		_, err = core.WithAccessHint(creds, func() (*core.ManifestResult, error) {
			return core.NewRegistryClient(creds, 0).Manifest("acme/secret", "1.0")
		}, core.BrowseOpts{})
		Match(t, wbAccessErr(t, err).Message, `repositories account bob can access: acme/api, acme/web, other/x$`)
		// login test with a named repository
		t1 := JOf(core.NewRegistryClient(creds, 0).Test("repository:acme/secret:pull", true))
		Eq(t, t1.At("ok"), false)
		Eq(t, t1.At("accessDenied"), "acme/secret")
		Eq(t, JOf(core.NewRegistryClient(creds, 0).Test("repository:acme/api:pull", true)).At("ok"), true)
		// generic browsing
		repos := JOf(core.ListRepositories(creds, "acme", core.BrowseOpts{}))
		Eq(t, wbWith(repos, A{"items": repos.At("items").Pluck("name")}), A{"items": L{"acme/api", "acme/web"}, "source": "catalog", "supported": true, "message": nil})
		tags := JOf(core.ListTags(creds, "acme/api", "", core.BrowseOpts{}))
		Eq(t, tags.At("source"), "registry-v2")
		Eq(t, tags.At("items").Pluck("tag"), L{"1.0", "2.0"})
		none := JOf(core.ListRepositories(core.RegistryCreds{URL: "http://127.0.0.1:1"}, "", core.BrowseOpts{}))
		Eq(t, none.At("supported"), false)
		Eq(t, none.At("source"), "none")
		True(t, none.At("message").Truthy())
	})

	t.Run("Docker Hub browsing via the hub API (stub): account + org repos, search, tags, hint", func(t *testing.T) {
		hub := wbServe(t, func(w http.ResponseWriter, r *http.Request) {
			authed := r.Header.Get("Authorization") == "Bearer hub-jwt"
			p := r.URL.Path
			if p == "/v2/users/login" && r.Method == "POST" {
				var b struct {
					Password string `json:"password"`
				}
				_ = json.NewDecoder(r.Body).Decode(&b)
				if b.Password == "pat" {
					wbJSON(w, 200, A{"token": "hub-jwt"})
				} else {
					wbJSON(w, 401, A{"detail": "Incorrect authentication credentials"})
				}
				return
			}
			if p == "/v2/search/repositories/" {
				wbJSON(w, 200, A{"results": L{A{"repo_name": "redis", "short_description": "Redis", "star_count": 9, "pull_count": 99, "is_official": true}, A{"repo_name": "bitnami/redis", "star_count": 1}}})
				return
			}
			switch {
			case !authed:
				wbJSON(w, 401, A{})
			case p == "/v2/namespaces/northwindbot/repositories":
				wbJSON(w, 200, A{"results": L{A{"name": "tools", "namespace": "northwindbot", "description": "x", "star_count": 0, "pull_count": 3, "last_updated": "2026-01-01T00:00:00Z"}}})
			case p == "/v2/user/orgs/":
				wbJSON(w, 200, A{"results": L{A{"orgname": "northwind"}}})
			case p == "/v2/namespaces/northwind/repositories":
				wbJSON(w, 200, A{"results": L{A{"name": "northwind", "namespace": "northwind"}, A{"name": "redis-fork", "namespace": "northwind"}}})
			case p == "/v2/namespaces/northwind/repositories/northwind/tags":
				wbJSON(w, 200, A{"results": L{A{"name": "old", "last_updated": "2025-01-01T00:00:00Z", "digest": nil, "full_size": 1}, A{"name": "backend-3.0.9", "last_updated": "2026-02-01T00:00:00Z", "digest": "sha256:" + strings.Repeat("a", 64), "full_size": 123}}})
			default:
				wbJSON(w, 404, A{})
			}
		})
		o := core.BrowseOpts{HubAPI: hub}
		creds := core.RegistryCreds{URL: "https://registry-1.docker.io", Username: "northwindbot", Password: "pat"}
		all := JOf(core.ListRepositories(creds, "", o))
		Eq(t, all.At("source"), "docker-hub")
		Eq(t, all.At("items").Pluck("name"), L{"northwindbot/tools", "northwind/northwind", "northwind/redis-fork"})
		q := JOf(core.ListRepositories(creds, "redis", o))
		Eq(t, q.At("items").Pluck("name"), L{"northwind/redis-fork", "library/redis", "bitnami/redis"})
		Eq(t, q.At("items").Find("name", "library/redis").At("official"), true)
		anon := JOf(core.ListRepositories(core.RegistryCreds{URL: "https://registry-1.docker.io"}, "", o))
		True(t, anon.At("items").Find("name", "library/redis").Exists(), "popular official images without q")
		tags := JOf(core.ListTags(creds, "northwind/northwind", "", o))
		Eq(t, tags.At("items").Pluck("tag"), L{"backend-3.0.9", "old"}, "newest first")
		Eq(t, tags.At("items.0.sizeBytes"), 123)
		Eq(t, tags.At("items.0.pushedAt"), "2026-02-01T00:00:00.000Z")
		wrongPw := creds
		wrongPw.Password = "wrong"
		bad := JOf(core.ListRepositories(wrongPw, "", o))
		Eq(t, bad.At("supported"), false)
		Match(t, bad.At("message"), `Docker Hub login failed`)
		hint := core.AccessHint(creds, "repository northwind/backend not found", o)
		Match(t, hint, `account northwindbot can access: northwindbot/tools, northwind/northwind, northwind/redis-fork$`)
	})

	t.Run("registry browse endpoints: perms, validation, supported:false fallback; test login with a repository body", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		nobody := NewPerson(t, L{})
		reg := admin.API.Call(t, "createRegistry", A{"body": A{"label": "dead", "url": "https://127.0.0.1:1", "username": "u", "password": "p"}})
		id := reg.At("registryId").Str()
		r := admin.API.Call(t, "listRegistryRepositories", A{"registryId": id, "query": A{"q": "x"}})
		Eq(t, r.At("supported"), false)
		True(t, r.At("message").Truthy())
		tg := admin.API.Call(t, "listRegistryTags", A{"registryId": id, "query": A{"repository": "acme/api"}})
		Eq(t, tg.At("supported"), false)
		admin.API.Rejects(t, "listRegistryTags", A{"registryId": id, "query": A{"repository": "Bad Repo"}}, "invalid")
		admin.API.Rejects(t, "listRegistryRepositories", A{"registryId": "nope", "query": A{}}, "not-found")
		nobody.API.Rejects(t, "listRegistryRepositories", A{"registryId": id, "query": A{}}, "forbidden")
		audited := func() int {
			t.Helper()
			q, err := DB.Collection("auditLog").Where("target", "==", "registries/"+id).Get()
			NoErr(t, err)
			return q.Size()
		}
		before := audited()
		admin.API.Call(t, "listRegistryRepositories", A{"registryId": id, "query": A{}})
		Eq(t, audited(), before, "GET not audited")
		hdr := map[string]string{"Authorization": admin.Bearer, "Content-Type": "application/json"}
		tr := Raw(t, "POST", "/v1/registries/"+id+"/test", Opt{Headers: hdr, Body: []byte(`{"repository":"acme/api"}`)})
		Eq(t, tr.Status, 200)
		Eq(t, tr.J.At("ok"), false)
		tb := Raw(t, "POST", "/v1/registries/"+id+"/test", Opt{Headers: hdr, Body: []byte(`{"repository":"NOT OK"}`)})
		Eq(t, tb.Status, 422)
	})
}
