package apitest

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"opm/agent/internal/cloud/core"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// ── helpers shared by the rounds C, D, E + image edit + volume kind ports

// wcAppSite is appSite(cfg): a copy of the site "app".
func wcAppSite(cfg J) J { return wcApp(cfg.At("sites")).Clone() }

// wcApp is sites.find(s => s.siteSlug === 'app').
func wcApp(sites J) J { return sites.Find("siteSlug", "app") }

// wcWithApp is sites.map(s => s.siteSlug === 'app' ? site : s).
func wcWithApp(sites J, site J) L {
	out := L{}
	for _, s := range sites.Arr() {
		if s.At("siteSlug").Str() == "app" {
			out = append(out, site.V)
		} else {
			out = append(out, s.V)
		}
	}
	return out
}

// wcSpread is {...j, ...kv}.
func wcSpread(j J, kv A) J {
	c := j.Clone()
	for k, v := range kv {
		c.Map()[k] = Plain(v)
	}
	return c
}

// wcOmit is ({a, b, ...rest}) => rest.
func wcOmit(j J, keys ...string) J { return J{dropKeys(j, keys...)} }

// wcPush is o[key].push(...items).
func wcPush(o J, key string, items ...any) {
	l, _ := o.Map()[key].(L)
	for _, x := range items {
		l = append(l, Plain(x))
	}
	o.Map()[key] = l
}

// wcSome is list.some(fn).
func wcSome(list J, fn func(J) bool) bool { return len(list.Filter(fn)) > 0 }

// wcCodeIs is some(x => x.code === code).
func wcCodeIs(list J, code string) bool {
	return wcSome(list, func(x J) bool { return x.At("code").Str() == code })
}

// wcRows is list.map(x => [x[a], x[b], …]).
func wcRows(list J, paths ...string) L {
	out := L{}
	for _, e := range list.Arr() {
		r := L{}
		for _, p := range paths {
			r = append(r, e.At(p).V)
		}
		out = append(out, r)
	}
	return out
}

// wcStringify is JSON.stringify: unlike J.String it leaves < > & as they are.
func wcStringify(t testing.TB, j J) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	NoErr(t, enc.Encode(j.V))
	return strings.TrimSuffix(b.String(), "\n")
}

// wcDocJSON is JSON.parse(doc[field]).
func wcDocJSON(t testing.TB, path, field string) J {
	t.Helper()
	j := ParseJ([]byte(Doc(t, path).At(field).Str()))
	True(t, j.Exists(), "%s.%s is no JSON", path, field)
	return j
}

// wcVolume is newSiteVolume(slug).
func wcVolume(slug string) J { return JOf(validate.NewSiteVolume(slug, "", "", true)) }

// wcUndeclared is undeclaredVolumes(sc).
func wcUndeclared(t testing.TB, sc J) []string {
	t.Helper()
	var m model.SiteConfig
	NoErr(t, json.Unmarshal([]byte(sc.String()), &m))
	return validate.UndeclaredVolumes(&m)
}

// wcDuplicateContainer is duplicateContainer(c, slug).
func wcDuplicateContainer(t testing.TB, c J, slug string) J {
	t.Helper()
	var m model.Container
	NoErr(t, json.Unmarshal([]byte(c.String()), &m))
	return JOf(validate.DuplicateContainer(m, slug))
}

// ── round C

// wcCLegacyContainer is legacyContainer.
func wcCLegacyContainer(c J) A {
	ports, vols, deps := L{}, L{}, L{}
	for _, p := range c.At("ports").Arr() {
		ports = append(ports, A{"portName": p.At("portName").V, "hostPort": p.At("hostPort").V, "protocol": "tcp"})
	}
	for _, v := range c.At("volumes").Arr() {
		if v.At("imageVolume").Truthy() {
			vols = append(vols, A{"kind": "data", "name": v.At("imageVolume").V, "mountPath": "/data"})
		} else if v.At("source.kind").Str() == "file" {
			vols = append(vols, A{"kind": "file", "fileSlug": v.At("source.fileSlug").V, "mountPath": v.At("mountPath").V})
		}
	}
	for _, d := range c.At("depends").Arr() {
		deps = append(deps, A{"serviceSlug": d.At("containerSlug").V})
	}
	return A{"serviceSlug": c.At("serviceSlug").V, "version": c.At("version").V, "enabled": c.At("enabled").V, "env": c.At("env").V, "port": ports, "volume": vols, "command": c.At("command").V, "restart": c.At("restart").V, "resources": c.At("resources").V, "depends": deps}
}

// wcCLegacySite is legacySite.
func wcCLegacySite(s J) J {
	cfg := wcOmit(s.At("config"), "volumes")
	cs, routes := L{}, L{}
	for _, c := range s.At("config.containers").Arr() {
		cs = append(cs, wcCLegacyContainer(c))
	}
	for _, r := range s.At("config.edge.routes").Arr() {
		if r.At("action.type").Str() != "proxy" {
			routes = append(routes, r.V)
			continue
		}
		a := wcSpread(wcOmit(r.At("action"), "containerSlug"), A{"serviceSlug": r.At("action.containerSlug").V})
		routes = append(routes, wcSpread(r, A{"action": a.V}).V)
	}
	cfg.Map()["host"], cfg.Map()["containers"] = "", cs
	cfg.Map()["edge"] = wcSpread(s.At("config.edge"), A{"routes": routes}).V
	return wcSpread(s, A{"config": cfg.V})
}

// wcCAssertMigratedApp is assertMigratedApp.
func wcCAssertMigratedApp(t *testing.T, sc J) {
	t.Helper()
	Eq(t, sc.At("host"), nil)
	Eq(t, sc.At("containers").Pluck("containerSlug"), L{"hello-api", "hello-web"})
	a := sc.At("containers.0")
	True(t, !a.Has("port") && !a.Has("volume"))
	Eq(t, a.At("ports"), L{A{"portName": "http", "hostPort": nil, "hostIp": "0.0.0.0", "edge": true}})
	Eq(t, a.At("volumes"), L{A{"imageVolume": "data", "mountPath": nil, "source": A{"kind": "volume", "volumeSlug": "hello-api-data"}, "readOnly": false}})
	Eq(t, sc.At("volumes"), L{A{"volumeSlug": "hello-api-data", "label": "hello-api-data", "description": "", "backup": true, "location": A{"kind": "auto"}, "kind": "dir", "seed": nil}}, "round D: legacy volumes declared")
	Eq(t, sc.At("containers.1.volumes"), L{A{"imageVolume": nil, "mountPath": "/etc/hello/banner.txt", "source": A{"kind": "file", "fileSlug": "banner"}, "readOnly": true}})
	Eq(t, sc.At("containers.1.depends"), L{A{"containerSlug": "hello-api"}})
	Eq(t, sc.At("edge.routes.0.action.containerSlug"), "hello-api")
	False(t, sc.At("edge.routes.0.action").Has("serviceSlug"))
}

// Port of backend/test/11-round-c.test.ts.
func TestRoundC(t *testing.T) {
	t.Run("instances: two containers of the same image on one site → save, publish, catalog, usage, delete conflict", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		site := wcAppSite(Hello(t).At("config"))
		api := site.At("config.containers").Find("containerSlug", "hello-api")
		api2 := wcDuplicateContainer(t, api, "hello-api-2")
		env := L{}
		for _, e := range api2.At("env").Arr() {
			if e.At("name").Str() != "GREETING" {
				env = append(env, e.V)
			}
		}
		api2.Map()["env"] = append(env, A{"name": "GREETING", "value": "second instance", "secretRef": nil})
		wcPush(site.At("config"), "containers", api2)
		// round D: the duplicate's `<new>-data` volume must be declared on the site
		for _, v := range wcUndeclared(t, site.At("config")) {
			wcPush(site.At("config"), "volumes", wcVolume(v))
		}
		// legacy (pre-E) route shape on purpose: { pathPrefix } without responseHeaders is normalized on write
		wcPush(site.At("config.edge"), "routes", A{"routeId": "api2", "hosts": L{}, "match": A{"pathPrefix": "/api2", "methods": nil}, "action": wcSpread(site.At("config.edge.routes.0.action"), A{"containerSlug": "hello-api-2"}), "order": site.At("config.edge.routes").Len() + 1})
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": site}})
		Eq(t, w.At("validation.ok"), true)
		d := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		saved := wcApp(d.At("draft.sites")).At("config")
		Eq(t, wcRows(saved.At("containers"), "containerSlug", "serviceSlug", "version"), L{L{"hello-api", "hello-api", "v1"}, L{"hello-web", "hello-web", "v1"}, L{"hello-api-2", "hello-api", "v1"}})
		Eq(t, saved.At("containers.2.volumes.0.source"), A{"kind": "volume", "volumeSlug": "hello-api-2-data"})
		True(t, wcSome(saved.At("volumes"), func(v J) bool { return v.At("volumeSlug").Str() == "hello-api-2-data" }))
		// a duplicate containerSlug is refused
		dup := site.Clone()
		wcPush(dup.At("config"), "containers", api2.Clone())
		admin.API.Rejects(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 1, "site": dup}}, "invalid")
		p := Publish(t, admin.API, id, 1, "two api instances")
		rev := admin.API.Call(t, "getRevision", A{"deploymentId": id, "revId": p.Revision.At("revId")})
		Eq(t, rev.At("catalog.images").Keys(), L{"hello-api:v1", "hello-web:v1", "redis:v1"}, "one catalog entry per image")
		Eq(t, wcApp(rev.At("config.sites")).At("config.containers").Len(), 3)
		// usage: one row per container instance
		su := admin.API.Call(t, "serviceUsage", A{"serviceSlug": "hello-api"})
		slugs := map[string]bool{}
		for _, u := range su.At("items").Filter(func(u J) bool { return u.At("id").Str() == id }) {
			slugs[u.At("containerSlug").Str()] = true
		}
		Eq(t, JOf(slugs).Keys(), L{"hello-api", "hello-api-2"})
		iu := admin.API.Call(t, "imageUsage", A{"serviceSlug": "hello-api", "version": "v1"})
		True(t, wcSome(iu.At("items"), func(u J) bool { return u.At("id").Str() == id && u.At("containerSlug").Str() == "hello-api-2" }))
		e := admin.API.Rejects(t, "deleteImage", A{"serviceSlug": "hello-api", "version": "v1"}, "conflict")
		True(t, wcSome(e.Details.At("usedBy"), func(u J) bool { return u.At("containerSlug").Str() == "hello-api-2" }))
	})

	t.Run("auto host: host null in a template + deployment publishes; {{ var.x }} with no value → V_HOST_AUTO warning, never an error", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		SeedHelloCatalog(t)
		h := Hello(t)
		banner := admin.API.Call(t, "uploadBlob", A{"body": []byte(h.At("banner").Str())})
		Eq(t, banner.At("sha256"), h.At("config.files.0.content.sha256"))
		sites := L{}
		for _, s := range h.At("config.sites").Arr() {
			sites = append(sites, wcSpread(s, A{"config": wcSpread(s.At("config"), A{"host": nil})}).V)
		}
		tpl := admin.API.Call(t, "createTemplate", A{"body": A{"label": Uniq("auto")}})
		secrets := L{}
		for _, s := range h.At("config.secrets").Arr() {
			secrets = append(secrets, wcSpread(s, A{"value": nil, "rotatedAt": nil}).V)
		}
		tw := admin.API.Call(t, "patchTemplate", A{"templateId": tpl.At("templateId").Str(), "body": A{"ifVersion": 1, "variables": h.At("config.variables"), "secrets": secrets, "files": h.At("config.files"), "sites": sites}})
		Eq(t, tw.At("validation.ok"), true)
		Eq(t, wcRows(tw.At("template.sites"), "config.host"), L{L{nil}, L{nil}})
		d := admin.API.Call(t, "createDeployment", A{"body": A{"label": Uniq("auto"), "templateId": tpl.At("templateId").Str(), "tagSlugs": L{}, "managerAccountUids": L{}}})
		id := d.At("deployment.deploymentId").Str()
		diff := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		// §21 #105: the diff is what the head (create) commit changed; it is publishable
		Eq(t, diff.At("blockers").Pluck("code"), L{}, diff.At("blockers"))
		Publish(t, admin.API, id, 0, "auto hosts")
		// host "" is stored as null
		site := wcAppSite(h.At("config"))
		site.At("config").Map()["host"] = ""
		admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": site}})
		Eq(t, wcApp(admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.sites")).At("config.host"), nil)
		// the user's case: host {{ var.server-ip }} with no value
		vars := h.At("config.variables").Clone()
		list, _ := vars.V.(L)
		list = append(list, A{"variableSlug": "server-ip", "label": "Server IP", "description": "", "type": "string", "value": nil, "required": false, "options": nil, "validation": nil})
		admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": 1, "items": list}})
		site.At("config").Map()["host"] = "{{ var.server-ip }}"
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 2, "site": site}})
		True(t, wcSome(w.At("validation.issues"), func(i J) bool { return i.At("code").Str() == "V_HOST_AUTO" && i.At("severity").Str() == "warning" }), w.At("validation.issues"))
		diff2 := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		True(t, wcCodeIs(diff2.At("warnings"), "V_HOST_AUTO"))
		hostRe := regexp.MustCompile(`(?i)host`)
		False(t, wcSome(diff2.At("blockers"), func(i J) bool { return hostRe.MatchString(i.At("path").Str()) }), diff2.At("blockers"))
		Eq(t, diff2.At("publishable"), true, diff2.At("blockers"))
		Publish(t, admin.API, id, 3, "host expression without value")
	})

	t.Run("legacy shapes on write are normalized (template PATCH + draft PUT): containerSlug, ports, explicit image volumes, depends, proxy, host", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		dep := HelloDeployment(t, admin)
		legacy := wcCLegacySite(wcAppSite(Hello(t).At("config")))
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": dep.ID(), "siteSlug": "app", "body": A{"ifRevision": 0, "site": legacy}})
		Eq(t, w.At("validation.ok"), true)
		wcCAssertMigratedApp(t, wcApp(admin.API.Call(t, "getDeployment", A{"deploymentId": dep.ID()}).At("draft.sites")).At("config"))
		raw0 := wcDocJSON(t, "deployments/"+dep.ID(), "_draft")
		NotContains(t, wcStringify(t, raw0), `"port":`, "stored in the new shape")
		// template PATCH
		tpl := admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID})
		tw := admin.API.Call(t, "patchTemplate", A{"templateId": dep.TemplateID, "body": A{"ifVersion": tpl.At("version"), "sites": wcWithApp(tpl.At("sites"), legacy)}})
		wcCAssertMigratedApp(t, wcApp(tw.At("template.sites")).At("config"))
		// legacy documents written directly to Firestore are normalized on read
		body := wcDocJSON(t, "templates/"+dep.TemplateID, "_body")
		body.Map()["sites"] = wcWithApp(body.At("sites"), legacy)
		UpdateDoc(t, "templates/"+dep.TemplateID, A{"_body": wcStringify(t, body)})
		read := admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID})
		rc := wcApp(read.At("sites")).At("config")
		Eq(t, rc.At("containers").Pluck("containerSlug"), L{"hello-api", "hello-web"})
		Eq(t, rc.At("host"), nil)
		Eq(t, rc.At("edge.routes.0.action.containerSlug"), "hello-api")
	})

	t.Run("files: render defaults (text → expressions, binary → none); binary + expressions → 422 invalid", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		noRender := wcOmit(Hello(t).At("config.files.0"), "render")
		bin := JOf(A{"fileSlug": "logo", "label": "Logo", "description": "", "mime": "image/png", "editor": "binary", "maxBytes": 4096, "content": nil})
		put := func(rev int, items ...J) A {
			return A{"deploymentId": id, "section": "files", "body": A{"ifRevision": rev, "items": items}}
		}
		w := admin.API.Call(t, "putDraftSection", put(0, noRender, bin))
		Eq(t, w.At("validation.ok"), true)
		files := admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.files")
		Eq(t, wcRows(files, "fileSlug", "render"), L{L{"banner", "expressions"}, L{"logo", "none"}})
		e := admin.API.Rejects(t, "putDraftSection", put(1, noRender, wcSpread(bin, A{"render": "expressions"})), "invalid", 422)
		Match(t, e.Details, `V_ENUM`)
		admin.API.Rejects(t, "putDraftSection", put(1, wcSpread(noRender, A{"render": "jinja"})), "invalid", 422)
		off := admin.API.Call(t, "putDraftSection", put(1, wcSpread(noRender, A{"render": "none"})))
		Eq(t, off.At("validation.ok"), true)
		Eq(t, admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.files.0.render"), "none")
	})

	t.Run("blobs: human GET ≤ BLOB_STREAM_MAX → 200 body + ETag + private cache (304 on If-None-Match); larger → 302 (serveMode)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		text := "round-c " + Uniq("x") + "\n"
		b := admin.API.Call(t, "uploadBlob", A{"body": []byte(text)})
		sha := b.At("sha256").Str()
		hex := sha[7:]
		r := Raw(t, "GET", "/v1/blobs/"+sha, Opt{Headers: map[string]string{"Authorization": admin.Bearer, "Origin": "http://localhost:5173"}})
		Eq(t, r.Status, 200)
		Eq(t, r.Text(), text)
		Eq(t, r.Header("content-type"), "application/octet-stream")
		Eq(t, r.Header("etag"), `"`+hex+`"`)
		Eq(t, r.Header("cache-control"), "private, max-age=300")
		Eq(t, r.Header("access-control-allow-origin"), "http://localhost:5173")
		Match(t, r.Header("access-control-expose-headers"), `ETag`)
		nm := Raw(t, "GET", "/v1/blobs/"+sha, Opt{Headers: map[string]string{"Authorization": admin.Bearer, "If-None-Match": `"` + hex + `"`}})
		Eq(t, nm.Status, 304)
		Eq(t, len(nm.Body), 0)
		// decision used by serveObject (prod: OPM_BLOB_REDIRECT=1; the emulator never redirects)
		max := int64(8 * 1024 * 1024)
		Eq(t, core.ServeMode(max, true, false, &max), "stream")
		Eq(t, core.ServeMode(max+1, true, false, &max), "redirect")
		Eq(t, core.ServeMode(max+1, false, false, &max), "stream")
		Eq(t, core.ServeMode(max+1, true, true, &max), "stream")
		Eq(t, core.ServeMode(10, true, false, nil), "redirect", "agent blobs (no streamMax) keep redirecting")
		// §21 #114: a tunnelled agent download (sent by the GUI tab: Origin header) or a relayed one streams; the agent itself redirects
		Eq(t, core.NoRedirectFor(map[string]string{"origin": "http://10.0.0.5:8080"}, ""), true)
		Eq(t, core.NoRedirectFor(map[string]string{}, "app2"), true)
		Eq(t, core.NoRedirectFor(map[string]string{"range": "bytes=0-9"}, ""), false)
	})

	t.Run("enroll + heartbeat store detectedAddresses (≤ 32 strings, each ≤ 255); status + setup expose them", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		key := NewSiteKey()
		enrollBody := func(detected any) A {
			return A{"publicKey": key.PublicKey, "agentVersion": "0.3.0-test", "hostname": "host-app", "ip": "10.0.0.7", "advertiseHost": "10.0.0.7", "agentPort": 7080, "os": "linux", "arch": "amd64", "detectedAddresses": detected}
		}
		tok := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
		anon := Bearer(tok.At("token").Str())
		many := L{}
		for i := 0; i < 33; i++ {
			many = append(many, "10.0.0."+Itoa(i))
		}
		anon.Rejects(t, "enroll", A{"body": enrollBody(many)}, "invalid")
		anon.Rejects(t, "enroll", A{"body": enrollBody(L{strings.Repeat("x", 256)})}, "invalid")
		anon.Call(t, "enroll", A{"body": enrollBody(L{"10.0.0.7", "192.168.1.4", "host-app"})})
		agentDoc := "deployments/" + id + "/agents/app"
		Eq(t, Doc(t, agentDoc).At("detectedAddresses"), L{"10.0.0.7", "192.168.1.4", "host-app"})
		api := AgentClient(key, id, "app")
		hb := JOf(A{"siteSlug": "app", "agentVersion": "0.3.0-test", "headRevId": nil, "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0.1, "memPct": 0.2, "diskPct": 0.3}, "edge": nil, "keyVersion": 1, "advertiseHost": "10.0.0.7", "agentPort": 7080, "ip": "10.0.0.7", "hostname": "host-app"})
		api.Call(t, "heartbeat", A{"body": wcSpread(hb, A{"detectedAddresses": L{"10.0.0.8", "host-app"}})})
		Eq(t, Doc(t, agentDoc).At("detectedAddresses"), L{"10.0.0.8", "host-app"})
		api.Call(t, "heartbeat", A{"body": hb})
		Eq(t, Doc(t, agentDoc).At("detectedAddresses"), L{"10.0.0.8", "host-app"}, "old agents (no field) keep the stored list")
		api.Rejects(t, "heartbeat", A{"body": wcSpread(hb, A{"detectedAddresses": "nope"})}, "invalid")
		st := admin.API.Call(t, "getStatus", A{"deploymentId": id})
		s := wcApp(st.At("sites"))
		Eq(t, s.At("detectedAddresses"), L{"10.0.0.8", "host-app"})
		Eq(t, s.At("advertiseHost"), "10.0.0.7")
		setup := admin.API.Call(t, "getSetup", A{"deploymentId": id})
		Eq(t, wcApp(setup.At("sites")).At("agent.detectedAddresses"), L{"10.0.0.8", "host-app"})
		// the auto host of an enrolled site resolves to its advertiseHost in validation (no placeholder for app)
		site := wcAppSite(Hello(t).At("config"))
		site.At("config").Map()["host"] = nil
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": site}})
		NotContains(t, wcStringify(t, w.At("validation.issues")), "<auto:app>")
	})
}
