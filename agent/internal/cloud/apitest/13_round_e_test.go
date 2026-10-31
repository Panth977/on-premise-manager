package apitest

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud/shared"
)

const wcECC = "public, max-age=3600, must-revalidate"

// wcELegacyEdge is legacyEdge: a pre-E edge (as stored before round E): {pathPrefix} matches, proxy timeoutSec, no
// responseHeaders / maxBodyBytes.
func wcELegacyEdge(edge J) J {
	e := wcOmit(edge, "maxBodyBytes")
	hosts, routes := L{}, L{}
	for _, h := range edge.At("hosts").Arr() {
		hosts = append(hosts, wcOmit(h, "responseHeaders").V)
	}
	for _, r := range edge.At("routes").Arr() {
		action := r.At("action")
		a := action.V
		if action.At("type").Str() == "proxy" {
			pa := wcOmit(action, "connectTimeoutSec", "sendTimeoutSec", "readTimeoutSec", "rewrite", "requestBuffering", "responseBuffering", "maxBodyBytes")
			pa.Map()["timeoutSec"] = action.At("readTimeoutSec").V
			a = pa.V
		}
		routes = append(routes, wcSpread(wcOmit(r, "responseHeaders"), A{"match": A{"pathPrefix": r.At("match.path").V, "methods": r.At("match.methods").V}, "action": a}).V)
	}
	e.Map()["hosts"], e.Map()["routes"] = hosts, routes
	return e
}

// wcEAppWithCacheHeader is appWithCacheHeader: the hello app site with a response-only header on the api route.
func wcEAppWithCacheHeader(t testing.TB) J {
	t.Helper()
	s := wcAppSite(Hello(t).At("config"))
	api := s.At("config.edge.routes").Find("routeId", "api")
	if api.At("action.type").Str() == "proxy" {
		api.At("action.setHeaders").Map()["Cache-Control"] = wcECC
	}
	return s
}

// wcENoKindSeed is volumes.map(({kind, seed, ...v}) => v).
func wcENoKindSeed(vols J) L {
	out := L{}
	for _, v := range vols.Arr() {
		out = append(out, wcOmit(v, "kind", "seed").V)
	}
	return out
}

// wcELegacySite is legacySite: pre-E site: legacy edge + site volumes without kind / seed.
func wcELegacySite(s J) J {
	return wcSpread(s, A{"config": wcSpread(s.At("config"), A{"volumes": wcENoKindSeed(s.At("config.volumes")), "edge": wcELegacyEdge(s.At("config.edge"))})})
}

// wcEAssertVolumes is assertVolumesE.
func wcEAssertVolumes(t *testing.T, sc J) {
	t.Helper()
	True(t, sc.At("volumes").Len() >= 1)
	for _, v := range sc.At("volumes").Arr() {
		True(t, v.At("kind").Str() == "dir" && v.At("seed").V == nil, v)
	}
}

// wcELegacyConfig is legacyConfig.
func wcELegacyConfig(cfg J) J {
	return wcSpread(cfg, A{"sites": wcWithApp(cfg.At("sites"), wcELegacySite(wcApp(cfg.At("sites"))))})
}

// wcEAssert is assertE: the hello app edge is in the round-E shape with the fixture's values (websocket kept false,
// readTimeoutSec 30).
func wcEAssert(t *testing.T, edge J, cache bool) {
	t.Helper()
	Eq(t, edge.At("maxBodyBytes"), nil)
	Eq(t, edge.At("hosts.0.responseHeaders"), A{"set": A{}, "add": A{}, "remove": L{}, "always": L{}})
	Eq(t, wcRows(edge.At("routes"), "routeId", "order", "match"), L{
		L{"api", 1, A{"kind": "prefix", "path": "/api", "stopRegex": false, "methods": nil}},
		L{"old", 2, A{"kind": "prefix", "path": "/old", "stopRegex": false, "methods": nil}},
		L{"web", 3, A{"kind": "prefix", "path": "/", "stopRegex": false, "methods": nil}},
	})
	api := edge.At("routes.0")
	Eq(t, api.At("responseHeaders"), nil)
	headers := A{"X-Opm-Site": "{{ self.slug }}"}
	if cache {
		headers["Cache-Control"] = wcECC
	}
	Eq(t, api.At("action"), A{
		"type":              "proxy",
		"containerSlug":     "hello-api",
		"portName":          "http",
		"stripPrefix":       true,
		"rewrite":           nil,
		"addPrefix":         nil,
		"setHeaders":        headers,
		"removeHeaders":     L{},
		"websocket":         false,
		"connectTimeoutSec": 60,
		"sendTimeoutSec":    60,
		"readTimeoutSec":    30,
		"requestBuffering":  false,
		"responseBuffering": false,
		"maxBodyBytes":      nil,
	})
	for _, r := range edge.At("routes").Arr() {
		True(t, !r.At("match").Has("pathPrefix") && !r.At("action").Has("timeoutSec"), r)
	}
}

// wcEProxy is {...newProxyAction('hello-api', 'http'), ...kv}.
func wcEProxy(kv A) J { return wcSpread(JOf(shared.NewProxyAction("hello-api", "http")), kv) }

// wcERoute is {...newEdgeRoute(id, path, action, order), ...kv}.
func wcERoute(id, path string, action J, order float64, kv A) J {
	r := wcSpread(JOf(shared.NewEdgeRoute(id, path, nil, order)), kv)
	r.Map()["action"] = action.V
	return r
}

// wcEPick is pick(issues): the vector's codes only, as {path, code, severity}, sorted.
func wcEPick(vec J, issues any) L {
	codes := map[string]bool{}
	for _, c := range vec.At("codes").Arr() {
		codes[c.Str()] = true
	}
	out := []J{}
	for _, i := range JOf(issues).Arr() {
		if codes[i.At("code").Str()] {
			out = append(out, JOf(A{"path": i.At("path").V, "code": i.At("code").V, "severity": i.At("severity").V}))
		}
	}
	return wcESorted(out)
}

// wcESorted is sorted(x).
func wcESorted(x []J) L {
	c := append([]J{}, x...)
	sort.SliceStable(c, func(a, b int) bool {
		return c[a].At("path").Str()+c[a].At("code").Str() < c[b].At("path").Str()+c[b].At("code").Str()
	})
	out := L{}
	for _, j := range c {
		out = append(out, j.V)
	}
	return out
}

// wcEVec is vec(name).
func wcEVec(vectors J, name string) J {
	return vectors.At("cases").FindF(func(c J) bool { return strings.HasPrefix(c.At("name").Str(), name) })
}

// wcEReqConfig is reqConfig: the vector base config: hello + variable server-ip (host + env SERVER_URL) + required secret
// jwt-key (env JWT).
func wcEReqConfig(t testing.TB, required bool, value any) J {
	t.Helper()
	c := Hello(t).At("config").Clone()
	wcPush(c, "variables", A{"variableSlug": "server-ip", "label": "Server IP", "description": "", "type": "string", "value": value, "required": required, "options": nil, "validation": nil})
	wcPush(c, "secrets", A{"secretSlug": "jwt-key", "label": "JWT key", "description": "", "required": true, "generate": nil, "value": nil, "rotatedAt": nil})
	app := wcApp(c.At("sites")).At("config")
	app.Map()["host"] = "{{ var.server-ip }}"
	for _, k := range app.At("containers").Arr() {
		wcPush(k, "env", A{"name": "SERVER_URL", "value": "http://{{ var.server-ip | upper }}:8080", "secretRef": nil})
	}
	wcPush(app.At("containers.0"), "env", A{"name": "JWT", "value": "Bearer {{ secret.jwt-key }}", "secretRef": nil})
	return c
}

// wcEReqTemplate is reqTemplate.
func wcEReqTemplate(t testing.TB, admin *Person, required bool) (string, J) {
	t.Helper()
	templateID := HelloDeployment(t, admin).TemplateID
	c := wcEReqConfig(t, required, nil)
	tpl := admin.API.Call(t, "getTemplate", A{"templateId": templateID})
	secrets := L{}
	for _, s := range c.At("secrets").Arr() {
		secrets = append(secrets, wcSpread(s, A{"value": nil, "rotatedAt": nil}).V)
	}
	w := admin.API.Call(t, "patchTemplate", A{"templateId": templateID, "body": A{"ifVersion": tpl.At("version"), "variables": c.At("variables"), "secrets": secrets, "sites": c.At("sites")}})
	return templateID, w
}

// wcEUploadSeed is uploadSeed.
func wcEUploadSeed(t testing.TB, admin *Person, data []byte) (string, J) {
	t.Helper()
	sha := "sha256:" + Sha256Hex(data)
	u := admin.API.Call(t, "createUpload", A{"body": A{"purpose": "seed-archive", "sizeBytes": len(data), "sha256": sha}})
	admin.API.Call(t, "putUploadPart", A{"uploadId": u.At("uploadId"), "index": "0", "body": data})
	return sha, admin.API.Call(t, "completeUpload", A{"uploadId": u.At("uploadId")})
}

// wcEGzip is gzipSync.
func wcEGzip(t testing.TB, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	_, err := z.Write([]byte(s))
	NoErr(t, err)
	NoErr(t, z.Close())
	return b.Bytes()
}

// Port of backend/test/13-round-e.test.ts.
func TestRoundE(t *testing.T) {
	t.Run("new edge shape: regex / exact / rewrite / stop-regex routes, response headers, timeouts, body limits are saved as sent (draft PUT + template PATCH)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		dep := HelloDeployment(t, admin)
		site := wcAppSite(Hello(t).At("config"))
		e := site.At("config.edge")
		e.Map()["maxBodyBytes"] = 10.0 * 1024 * 1024
		e.At("hosts.0").Map()["responseHeaders"] = A{"set": A{"X-Frame-Options": "DENY"}, "add": A{"X-Served-By": "$host"}, "remove": L{"Server"}, "always": L{"X-Frame-Options"}}
		sse := wcERoute("events", `^/events/(\d+)$`, wcEProxy(A{"rewrite": A{"regex": `^/events/(\d+)$`, "replacement": "/stream?id=$1"}, "readTimeoutSec": 86400, "responseBuffering": false, "maxBodyBytes": 0}), 4, A{
			"match":           A{"kind": "regex", "path": `(?i)^/events/(\d+)$`, "stopRegex": false, "methods": L{"GET"}},
			"responseHeaders": A{"set": A{"Cache-Control": "no-cache"}, "add": A{}, "remove": L{}, "always": L{}},
		})
		s3 := wcERoute("s3", "/s3/", wcEProxy(A{"stripPrefix": true, "addPrefix": "/s3", "requestBuffering": true, "connectTimeoutSec": 5, "sendTimeoutSec": 600}), 5, A{"match": A{"kind": "prefix", "path": "/s3/", "stopRegex": true, "methods": nil}})
		rw := wcERoute("legacy-app", "/app", JOf(A{"type": "rewrite", "regex": `^/app(/.*)?$`, "replacement": "/$1"}), 6, A{"match": A{"kind": "exact", "path": "/app", "stopRegex": false, "methods": nil}})
		see := wcERoute("see", "/see", JOf(A{"type": "redirect", "to": "/other$is_args$args", "status": 303, "keepPath": false}), 7, A{"match": A{"kind": "exact", "path": "/see", "stopRegex": false, "methods": nil}})
		wcPush(e, "routes", sse, s3, rw, see)
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": dep.ID(), "siteSlug": "app", "body": A{"ifRevision": 0, "site": site}})
		False(t, wcSome(w.At("validation.issues"), func(i J) bool { return i.At("severity").Str() == "error" }), w.At("validation.issues"))
		saved := wcApp(admin.API.Call(t, "getDeployment", A{"deploymentId": dep.ID()}).At("draft.sites")).At("config.edge")
		Eq(t, saved, e)
		raw := wcDocJSON(t, "deployments/"+dep.ID(), "_draft")
		Eq(t, wcApp(raw.At("sites")).At("config.edge"), e, "stored as sent")
		// template PATCH
		tpl := admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID})
		tw := admin.API.Call(t, "patchTemplate", A{"templateId": dep.TemplateID, "body": A{"ifVersion": tpl.At("version"), "sites": wcWithApp(tpl.At("sites"), site)}})
		Eq(t, wcApp(tw.At("template.sites")).At("config.edge"), e)
		// publishable (edge rules hold at publish level too)
		p := Publish(t, admin.API, dep.ID(), 1, "round E edge")
		rev := admin.API.Call(t, "getRevision", A{"deploymentId": dep.ID(), "revId": p.Revision.At("revId")})
		Eq(t, wcApp(rev.At("config.sites")).At("config.edge"), e)
	})

	t.Run("legacy edge shape from a client (pathPrefix / timeoutSec, no responseHeaders / maxBodyBytes) is normalized on write, not rejected; legacy stored data normalized on read", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		dep := HelloDeployment(t, admin)
		id := dep.ID()
		// draft PUT site
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": wcELegacySite(wcEAppWithCacheHeader(t))}})
		Eq(t, w.At("validation.ok"), true, w.At("validation.issues"))
		raw0 := wcDocJSON(t, "deployments/"+id, "_draft")
		wcEAssert(t, wcApp(raw0.At("sites")).At("config.edge"), true)
		wcEAssertVolumes(t, wcApp(raw0.At("sites")).At("config"))
		// draft PUT sites section
		all := wcWithApp(admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.sites"), wcELegacySite(wcAppSite(Hello(t).At("config"))))
		admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "sites", "body": A{"ifRevision": 1, "items": all}})
		wcEAssert(t, wcApp(admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.sites")).At("config.edge"), false)
		// template PATCH
		tpl := admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID})
		tw := admin.API.Call(t, "patchTemplate", A{"templateId": dep.TemplateID, "body": A{"ifVersion": tpl.At("version"), "sites": wcWithApp(tpl.At("sites"), wcELegacySite(wcApp(tpl.At("sites"))))}})
		wcEAssert(t, wcApp(tw.At("template.sites")).At("config.edge"), false)
		tPath := "templates/" + dep.TemplateID
		wcEAssert(t, wcApp(wcDocJSON(t, tPath, "_body").At("sites")).At("config.edge"), false)
		// normalize on read: a legacy edge written directly to Firestore is served in the round-E shape
		UpdateDoc(t, tPath, A{"_body": wcStringify(t, wcELegacyConfig(wcDocJSON(t, tPath, "_body")))})
		got := admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID})
		wcEAssert(t, wcApp(got.At("sites")).At("config.edge"), false)
		wcEAssertVolumes(t, wcApp(got.At("sites")).At("config"))
		// a PATCH that does not touch sites writes the normalized edge back
		admin.API.Call(t, "patchTemplate", A{"templateId": dep.TemplateID, "body": A{"ifVersion": got.At("version"), "description": "touched"}})
		wcEAssert(t, wcApp(wcDocJSON(t, tPath, "_body").At("sites")).At("config.edge"), false)
	})

	t.Run("validation: V_EDGE_REGEX → 422 invalid on draft + template save; W_RESPONSE_HEADER_AS_REQUEST is a warning (saved, publishable)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		dep := HelloDeployment(t, admin)
		id := dep.ID()
		// lookahead: not in RE2 ∩ JS
		bad := wcAppSite(Hello(t).At("config"))
		wcPush(bad.At("config.edge"), "routes", wcERoute("look", "/x", JOf(shared.NewProxyAction("hello-api", "http")), 4, A{"match": A{"kind": "regex", "path": "^/(?=admin)", "stopRegex": false, "methods": nil}}))
		e := admin.API.Rejects(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": bad}}, "invalid", 422)
		True(t, wcSome(e.Details.At("errors"), func(x J) bool {
			return x.At("code").Str() == "V_EDGE_REGEX" && strings.Contains(x.At("path").Str(), "look")
		}), e.Details)
		// bad rewrite regex (backreference) in a proxy rewrite, on a template
		bad2 := wcAppSite(Hello(t).At("config"))
		api := bad2.At("config.edge.routes.0")
		if api.At("action.type").Str() == "proxy" {
			api.At("action").Map()["stripPrefix"] = false
			api.At("action").Map()["rewrite"] = A{"regex": `^/api/(a)\1`, "replacement": "/$1"}
		}
		tpl := admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID})
		e2 := admin.API.Rejects(t, "patchTemplate", A{"templateId": dep.TemplateID, "body": A{"ifVersion": tpl.At("version"), "sites": wcWithApp(tpl.At("sites"), bad2)}}, "invalid", 422)
		True(t, wcCodeIs(e2.Details.At("errors"), "V_EDGE_REGEX"), e2.Details)
		// response-only header as a request header → warning only
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": wcEAppWithCacheHeader(t)}})
		Eq(t, w.At("validation.ok"), true)
		warn := w.At("validation.issues").Filter(func(i J) bool { return i.At("code").Str() == "W_RESPONSE_HEADER_AS_REQUEST" })
		Eq(t, len(warn), 1, w.At("validation.issues"))
		Eq(t, warn[0].At("severity"), "warning")
		Match(t, warn[0].At("path"), `routes\[api\]\.action\.setHeaders\.Cache-Control`)
		tw := admin.API.Call(t, "patchTemplate", A{"templateId": dep.TemplateID, "body": A{"ifVersion": tpl.At("version"), "sites": wcWithApp(tpl.At("sites"), wcEAppWithCacheHeader(t))}})
		True(t, wcSome(tw.At("validation.issues"), func(i J) bool {
			return i.At("code").Str() == "W_RESPONSE_HEADER_AS_REQUEST" && i.At("severity").Str() == "warning"
		}))
		Publish(t, admin.API, id, 1, "warning only")
	})

	// ───────────────────────────────────────── round E additions: required variables (shared/testdata/required-vars-vectors.json)
	t.Run("required variables: template save (required null → nothing; optional null → INFO), draft V_VAR_REQUIRED warning, publish blocked, publish ok when set", func(t *testing.T) {
		vectors := Load(t, "required-vars-vectors.json")
		pick := func(issues any) L { return wcEPick(vectors, issues) }
		vec := func(name string) J { return wcEVec(vectors, name) }
		sorted := func(name string) L { return wcESorted(vec(name).At("expect").Arr()) }
		admin := NewPerson(t, "admin")
		// template + required null → no issue
		r1ID, r1 := wcEReqTemplate(t, admin, true)
		Eq(t, r1.At("validation.ok"), true)
		Eq(t, pick(r1.At("validation.issues")), sorted("template + required null"))
		// template + optional null → one INFO on the variable (never counted as warning / error)
		_, r2 := wcEReqTemplate(t, admin, false)
		Eq(t, r2.At("validation.ok"), true)
		Eq(t, pick(r2.At("validation.issues")), sorted("template + optional null"))
		True(t, wcSome(r2.At("validation.issues"), func(i J) bool { return i.At("code").Str() == "V_VAR_OPTIONAL_NULL" && i.At("severity").Str() == "info" }))
		// draft from the required template: one V_VAR_REQUIRED + V_SECRET_REQUIRED (warnings in the draft: saves work)
		d := admin.API.Call(t, "createDeployment", A{"body": A{"label": fmt.Sprintf("req-%d", time.Now().UnixMilli()), "templateId": r1ID, "tagSlugs": L{}, "managerAccountUids": L{}}})
		id := d.At("deployment.deploymentId").Str()
		dep := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		wv := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": 0, "items": dep.At("draft.variables")}})
		Eq(t, wv.At("validation.ok"), true)
		Eq(t, pick(wv.At("validation.issues")), sorted("draft + required null"))
		// publish: blocked by the errors on the variable / secret only
		diff := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		Eq(t, pick(diff.At("blockers")), sorted("publish + required null"))
		False(t, wcSome(diff.At("warnings"), func(i J) bool { return i.At("severity").Str() != "warning" }), "diff warnings hold warnings only")
		_, pe := admin.API.Try("commitDeployment", A{"deploymentId": id, "body": A{"message": "x", "ifRevision": wv.At("draftVersion")}}) // rejects(publish(…)): Publish fails the test on an error
		e := Rejected(t, pe, "invalid", 422)
		Eq(t, pick(e.Details.At("errors")), sorted("publish + required null"))
		// set the variable + the secret → clean, publish works
		set := vec("draft + required set")
		items := L{}
		for _, v := range dep.At("draft.variables").Arr() {
			if v.At("variableSlug").Str() == "server-ip" {
				v = wcSpread(v, A{"value": set.At("variable.value")})
			}
			items = append(items, v.V)
		}
		w2 := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": wv.At("draftVersion"), "items": items}})
		cur := admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.secrets")
		secrets := L{}
		for _, x := range cur.Arr() {
			if x.At("secretSlug").Str() == "jwt-key" {
				x = wcSpread(x, A{"plainValue": set.At("secretValue")})
			}
			secrets = append(secrets, x.V)
		}
		w3 := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "secrets", "body": A{"ifRevision": w2.At("draftVersion"), "items": secrets}})
		Eq(t, pick(w3.At("validation.issues")), wcESorted(set.At("expect").Arr()))
		p := Publish(t, admin.API, id, w3.At("draftVersion"), "vars set")
		Eq(t, p.Revision.At("seq"), 1)
	})

	// ───────────────────────────────────────── round E additions: volume kind + starter content (seed)
	t.Run("seed archives: createUpload purpose seed-archive ≤ 200 MiB (gzip only), seed on a volume saved + published, agent may fetch it, daily keeps it", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		sha0 := "sha256:" + strings.Repeat("e", 64)
		mib60 := 60 * 1024 * 1024
		// limits: blob 60 MiB → 413; seed-archive 60 MiB ok; > SEED_ARCHIVE_MAX → 413; unknown purpose → invalid
		admin.API.Rejects(t, "createUpload", A{"body": A{"purpose": "blob", "sizeBytes": mib60, "sha256": sha0}}, "too-large", 413)
		big := admin.API.Call(t, "createUpload", A{"body": A{"purpose": "seed-archive", "sizeBytes": mib60, "sha256": sha0}})
		Eq(t, big.At("partCount"), math.Ceil(float64(mib60)/big.At("partSize").Num()))
		admin.API.Rejects(t, "createUpload", A{"body": A{"purpose": "seed-archive", "sizeBytes": shared.SEED_ARCHIVE_MAX + 1, "sha256": sha0}}, "too-large", 413)
		admin.API.Rejects(t, "createUpload", A{"body": A{"purpose": "nope", "sizeBytes": 1, "sha256": sha0}}, "invalid")
		// not gzip → 422 V_FORMAT on the first part
		notGz := []byte("plain text, not an archive")
		nu := admin.API.Call(t, "createUpload", A{"body": A{"purpose": "seed-archive", "sizeBytes": len(notGz), "sha256": "sha256:" + Sha256Hex(notGz)}})
		ne := admin.API.Rejects(t, "putUploadPart", A{"uploadId": nu.At("uploadId"), "index": "0", "body": notGz}, "invalid", 422)
		True(t, wcCodeIs(ne.Details.At("errors"), "V_FORMAT"), ne.Details)
		// a real .tar.gz-like (gzip) archive → content-addressed blob
		data := wcEGzip(t, fmt.Sprintf("starter %d", time.Now().UnixMilli()))
		sha, done := wcEUploadSeed(t, admin, data)
		Eq(t, done.At("blob"), A{"sha256": sha, "sizeBytes": len(data)})
		Eq(t, done.At("upload.purpose"), "blob")
		False(t, done.At("upload").Has("_seedArchive"), "internal flag not exposed")
		Eq(t, hex.EncodeToString(admin.API.Fetch(t, "getBlob", A{"sha256": sha}).Body), hex.EncodeToString(data))
		// seed on a declared volume: saved, read back, publishable
		id := HelloDeployment(t, admin).ID()
		site := wcAppSite(Hello(t).At("config"))
		seed := A{"kind": "archive", "blobSha256": sha, "name": "starter.tar.gz", "size": len(data)}
		site.At("config.volumes.0").Map()["seed"] = Plain(seed)
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": site}})
		False(t, wcSome(w.At("validation.issues"), func(i J) bool { return i.At("severity").Str() == "error" }), w.At("validation.issues"))
		saved := wcApp(admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.sites")).At("config.volumes.0")
		Eq(t, saved.At("seed"), seed)
		Eq(t, saved.At("kind"), "dir")
		// a wrong seed (single-file seed on a folder volume) → 422 V_VOLUME_SEED; bad kind → 422 V_VOLUME_KIND
		bad := site.Clone()
		// §21 #101: legacy File seeds are V_VOLUME_SEED_FILE_REF now; a single-file seed on a folder volume stays V_VOLUME_SEED
		bad.At("config.volumes.0").Map()["seed"] = Plain(A{"kind": "single", "sha256": sha, "size": len(data), "mode": "0644", "render": "none"})
		be := admin.API.Rejects(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 1, "site": bad}}, "invalid", 422)
		True(t, wcCodeIs(be.Details.At("errors"), "V_VOLUME_SEED"), be.Details)
		bad2 := site.Clone()
		bad2.At("config.volumes.0").Map()["kind"] = "folder"
		be2 := admin.API.Rejects(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 1, "site": bad2}}, "invalid", 422)
		True(t, wcCodeIs(be2.Details.At("errors"), "V_VOLUME_KIND"), be2.Details)
		Publish(t, admin.API, id, 1, "seeded volume")
		// the agent may fetch the archive (open proposal), not an unrelated blob
		app := EnrollSite(t, admin, id, "app")
		ab := app.API.Fetch(t, "getAgentBlob", A{"sha256": sha})
		Eq(t, hex.EncodeToString(ab.Body), hex.EncodeToString(data))
		otherSha, _ := wcEUploadSeed(t, admin, wcEGzip(t, fmt.Sprintf("other %d", time.Now().UnixMilli())))
		app.API.Rejects(t, "getAgentBlob", A{"sha256": otherSha}, "forbidden")
		// the daily job never deletes content blobs referenced by volume seeds
		RunDaily(t)
		Eq(t, Bucket.Exists(t, "files/"+sha[7:]), true)
	})

	t.Run("volumes kind + seed: missing → dir / null on write and read", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		dep := HelloDeployment(t, admin)
		site := wcAppSite(Hello(t).At("config"))
		site.At("config").Map()["volumes"] = wcENoKindSeed(site.At("config.volumes"))
		admin.API.Call(t, "putDraftSite", A{"deploymentId": dep.ID(), "siteSlug": "app", "body": A{"ifRevision": 0, "site": site}})
		wcEAssertVolumes(t, wcApp(wcDocJSON(t, "deployments/"+dep.ID(), "_draft").At("sites")).At("config"))
		tPath := "templates/" + dep.TemplateID
		body := wcDocJSON(t, tPath, "_body")
		body.Map()["sites"] = wcWithApp(body.At("sites"), site)
		UpdateDoc(t, tPath, A{"_body": wcStringify(t, body)})
		wcEAssertVolumes(t, wcApp(admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID}).At("sites")).At("config"))
	})
}
