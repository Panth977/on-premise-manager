package apitest

import (
	"strings"
	"testing"

	"opm/agent/internal/cloud/shared"
)

// wcIESeedImage is seedImage: a READY image written straight to the store (no import), unique service.
func wcIESeedImage(t testing.TB, slug, version string) {
	t.Helper()
	now := Now()
	audit := func(m A) A {
		m["createdAt"], m["createdBy"], m["updatedAt"], m["updatedBy"] = now, "test", now, "test"
		return m
	}
	SetDoc(t, "services/"+slug, audit(A{"serviceSlug": slug, "label": slug, "icon": nil, "description": ""}))
	hx := func(k string) string { return Sha256Hex(slug + ":" + version + ":" + k) }
	layers := L{A{"digest": "sha256:" + hx("l1"), "sizeBytes": 100, "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip"}}
	SetDoc(t, "services/"+slug+"/images/"+version, audit(A{
		"version":     version,
		"serviceSlug": slug,
		"reference":   "docker.io/library/busybox:" + version,
		"source":      A{"host": "docker.io", "repository": "library/busybox", "tag": version, "digest": nil},
		"registryId":  nil,
		"digest":      "sha256:" + hx("m"),
		"platform":    "linux/amd64",
		"import":      A{"mode": "copy", "status": "ready", "error": nil, "sizeBytes": 100, "manifestMediaType": "application/vnd.oci.image.manifest.v1+json", "configDigest": "sha256:" + hx("c"), "layers": layers, "startedAt": now, "finishedAt": now},
		"kind":        "service",
		"entrypoint":  nil,
		"command":     nil,
		"workdir":     nil,
		"user":        nil,
		"ports": L{
			A{"name": "http", "port": 8080, "protocol": "tcp", "description": "HTTP"},
			A{"name": "metrics", "port": 9100, "protocol": "tcp", "description": ""},
		},
		"env": L{
			A{"name": "NODE_ENV", "value": "prod", "options": L{"prod", "dev"}, "required": false, "secret": false, "description": ""},
			A{"name": "LOG", "value": "info", "options": nil, "required": false, "secret": false, "description": ""},
		},
		"volumes": L{
			A{"name": "data", "mountPath": "/data", "kind": "dir", "description": ""},
			A{"name": "cache", "mountPath": "/cache", "kind": "dir", "description": ""},
		},
		"healthcheck":    A{"type": "http", "portName": "http", "path": "/healthz", "expectStatus": 200, "intervalSec": 10, "timeoutSec": 5, "retries": 3, "startPeriodSec": 5},
		"stopTimeoutSec": 30,
		"description":    "",
	}))
}

// wcIEConfig is config(slug): one site "app": container <slug> (ports http published, env NODE_ENV + an expression, volume
// data), a second container of another version reading site.app.port.<slug>.http, an edge route to <slug>:http.
func wcIEConfig(t testing.TB, slug string) A {
	t.Helper()
	sc := JOf(shared.DefaultSiteConfig(nil))
	sc.Map()["volumes"] = L{wcVolume("app-data").V}
	c := func(cs, version string, hostPort any) A {
		return A{
			"containerSlug": cs,
			"serviceSlug":   slug,
			"version":       version,
			"enabled":       true,
			"env": L{
				A{"name": "NODE_ENV", "value": "dev", "secretRef": nil},
				A{"name": "CFG", "value": "/cfg/{{ container.env.NODE_ENV }}.json", "secretRef": nil},
			},
			"ports": L{
				A{"portName": "http", "hostPort": hostPort, "hostIp": "0.0.0.0", "edge": true},
				A{"portName": "metrics", "hostPort": nil, "hostIp": "0.0.0.0", "edge": true},
			},
			"volumes": L{
				A{"imageVolume": "data", "mountPath": nil, "source": A{"kind": "volume", "volumeSlug": "app-data"}, "readOnly": false},
				A{"imageVolume": "cache", "mountPath": nil, "source": A{"kind": "none"}, "readOnly": false},
			},
			"command":     nil,
			"entrypoint":  nil,
			"restart":     "always",
			"resources":   A{"cpus": nil, "memoryMb": nil},
			"healthcheck": nil,
			"depends":     L{},
		}
	}
	main := c(slug, "v1", 18080)
	other := JOf(c(slug+"-b", "v2", nil))
	wcPush(other, "env", A{"name": "UP", "value": "http://{{ site.app.host }}:{{ site.app.port." + slug + ".http }}", "secretRef": nil})
	sc.Map()["containers"] = Plain(L{main, other})
	route := wcApp(Hello(t).At("config.sites")).At("config.edge.routes.0").Clone()
	route.Map()["routeId"] = "main"
	route.At("action").Map()["containerSlug"] = slug
	route.At("action").Map()["portName"] = "http"
	sc.At("edge").Map()["routes"] = L{route.V}
	return A{"variables": L{}, "secrets": L{}, "files": L{}, "sites": L{A{"siteSlug": "app", "label": "App", "config": sc}}}
}

type wcIESetup struct {
	admin                *Person
	slug, templateID, id string
	templateVersion      J
	revID                J
}

func wcIENew(t testing.TB) wcIESetup {
	t.Helper()
	admin := NewPerson(t, "admin")
	slug := Uniq("ie")
	wcIESeedImage(t, slug, "v1")
	wcIESeedImage(t, slug, "v2")
	body := wcIEConfig(t, slug)
	tpl := admin.API.Call(t, "createTemplate", A{"body": A{"label": Uniq("tpl")}})
	body["ifVersion"] = 1
	tw := admin.API.Call(t, "patchTemplate", A{"templateId": tpl.At("templateId").Str(), "body": body})
	Eq(t, tw.At("validation.ok"), true, tw.At("validation.issues"))
	d := admin.API.Call(t, "createDeployment", A{"body": A{"label": Uniq("dep"), "templateId": tpl.At("templateId").Str(), "tagSlugs": L{}, "managerAccountUids": L{}}})
	id := d.At("deployment.deploymentId").Str()
	p := Publish(t, admin.API, id, 0, "r1")
	return wcIESetup{admin: admin, slug: slug, templateID: tpl.At("templateId").Str(), templateVersion: tw.At("template.version"), id: id, revID: p.Revision.At("revId")}
}

// wcIEContainer is container(cfg, cs).
func wcIEContainer(cfg J, cs string) J {
	return cfg.At("sites.0.config.containers").Find("containerSlug", cs)
}

// wcIEItems is det.items.map(x => `${x.kind}:${x.name}>${x.to}:${x.change}`).
func wcIEItems(items J, withTo bool) L {
	out := L{}
	for _, x := range items.Arr() {
		s := x.At("kind").Str() + ":" + x.At("name").Str()
		if withTo {
			s += ">" + x.At("to").Str()
		}
		out = append(out, s+":"+x.At("change").Str())
	}
	return out
}

// Port of backend/test/14-image-edit.test.ts.
func TestImageEdit(t *testing.T) {
	t.Run("edit every editable field of an unused image (same validation as registration)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		slug := Uniq("ie")
		wcIESeedImage(t, slug, "v1")
		patch := func(body A) A { return A{"serviceSlug": slug, "version": "v1", "body": body} }
		next := admin.API.Call(t, "patchImage", patch(A{
			"kind":       "job",
			"entrypoint": L{"/bin/sh", "-c"},
			"command":    L{"run.sh"},
			"workdir":    "/srv",
			"user":       "1000:1000",
			"ports": L{
				A{"name": "web", "port": 8081, "protocol": "tcp", "description": "Web"},
				A{"name": "dns", "port": 53, "protocol": "udp", "description": ""},
			},
			"env": L{
				A{"name": "APP_ENV", "value": "dev", "options": L{"dev", "prod"}, "required": true, "secret": false, "description": "mode"},
				A{"name": "TOKEN", "required": true, "secret": true, "description": "", "options": nil},
			},
			"volumes":        L{A{"name": "store", "mountPath": "/var/{{ container.env.APP_ENV }}", "description": "data"}},
			"healthcheck":    A{"type": "tcp", "portName": "web", "intervalSec": 5, "timeoutSec": 2, "retries": 2, "startPeriodSec": 0},
			"stopTimeoutSec": 45,
			"description":    "edited",
			"renames":        L{A{"kind": "port", "from": "http", "to": "web"}},
		}))
		Eq(t, next.At("kind"), "job")
		Eq(t, next.At("entrypoint"), L{"/bin/sh", "-c"})
		Eq(t, next.At("command"), L{"run.sh"})
		Eq(t, next.At("workdir"), "/srv")
		Eq(t, next.At("user"), "1000:1000")
		ports := L{}
		for _, p := range next.At("ports").Arr() {
			ports = append(ports, p.At("name").Str()+":"+Itoa(p.At("port").Int())+"/"+p.At("protocol").Str())
		}
		Eq(t, ports, L{"web:8081/tcp", "dns:53/udp"})
		// [['APP_ENV', 'dev', ['dev', 'prod'], true, false], ['TOKEN', undefined, null, true, true]]
		Eq(t, next.At("env").Len(), 2)
		Eq(t, wcRows(next.At("env"), "name", "options", "required", "secret"), L{L{"APP_ENV", L{"dev", "prod"}, true, false}, L{"TOKEN", nil, true, true}})
		Eq(t, next.At("env.0.value"), "dev")
		Eq(t, next.At("env.1.value"), Undefined)
		Eq(t, next.At("volumes"), L{A{"name": "store", "mountPath": "/var/{{ container.env.APP_ENV }}", "kind": "dir", "description": "data"}}, "kind inferred + stored (§21 #83)")
		Eq(t, next.At("healthcheck.type"), "tcp")
		Eq(t, next.At("stopTimeoutSec"), 45)
		Eq(t, next.At("description"), "edited")
		Eq(t, next.At("digest"), Doc(t, "services/"+slug+"/images/v1").At("digest"), "identity kept")
		Eq(t, next.At("import.status"), "ready")
		// workdir / user cleared by null or ""
		cleared := admin.API.Call(t, "patchImage", patch(A{"workdir": nil, "user": ""}))
		Eq(t, cleared.At("workdir"), nil)
		Eq(t, cleared.At("user"), nil)
		// validation = registration rules
		bad := admin.API.Rejects(t, "patchImage", patch(A{"kind": "daemon", "workdir": "rel", "ports": L{A{"name": "a", "port": 70000, "protocol": "tcp", "description": ""}}, "env": L{A{"name": "bad name", "required": false, "secret": false, "description": ""}}, "volumes": L{A{"name": "v", "mountPath": "rel", "description": ""}}, "healthcheck": A{"type": "tcp", "portName": "nope", "intervalSec": 5, "timeoutSec": 2, "retries": 2, "startPeriodSec": 0}}), "invalid")
		paths := bad.Details.At("errors").Pluck("path")
		for _, p := range []string{"kind", "workdir", "ports[a].port", "env[bad name].name", "volumes[v].mountPath", "healthcheck.portName"} {
			True(t, wcStrIn(paths, p), "%s in %v", p, paths)
		}
		// bad renames
		br := admin.API.Rejects(t, "patchImage", patch(A{"renames": L{A{"kind": "port", "from": "nope", "to": "web"}}}), "invalid")
		Match(t, br.Details, `V_RENAME`)
		// legacy partial form still works; mixing → V_TYPE
		leg := admin.API.Call(t, "patchImage", patch(A{"ports": L{A{"name": "web", "description": "legacy"}}, "env": L{A{"name": "APP_ENV", "value": "prod"}}}))
		Eq(t, leg.At("ports.0.description"), "legacy")
		Eq(t, leg.At("ports").Len(), 2)
		Eq(t, leg.At("env.0.value"), "prod")
		admin.API.Rejects(t, "patchImage", patch(A{"ports": L{A{"name": "web", "description": "x"}, A{"name": "dns", "port": 53, "protocol": "udp", "description": ""}}}), "invalid")
		// audit entry carries the change summary
		a := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "services/" + slug + "/images/v1", "action": "image.updated"}})
		first := a.At("items.-1")
		Eq(t, first.At("data.renamed"), L{A{"kind": "port", "from": "http", "to": "web"}})
		True(t, wcSome(first.At("changes"), func(ch J) bool { return strings.HasPrefix(ch.At("path").Str(), "kind") }), first.At("changes"))
	})

	t.Run("identity fields are rejected; reference only while the import failed", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		slug := Uniq("ie")
		wcIESeedImage(t, slug, "v1")
		for _, f := range []struct {
			k string
			v any
		}{{"version", "v9"}, {"serviceSlug", "x"}, {"source", A{}}, {"digest", "sha256:00"}, {"platform", "linux/arm64"}, {"import", A{}}, {"mode", "gateway"}, {"layers", L{}}, {"createdAt", "x"}, {"createdBy", "x"}} {
			e := admin.API.Rejects(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{f.k: f.v}}, "invalid")
			Match(t, e.Details, `V_IMMUTABLE`, f.k)
		}
		admin.API.Rejects(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": A{"reference": "docker.io/library/busybox:1.37"}}, "conflict", 409)
	})

	t.Run("in-use rename → 409 in-use with the list; rename everywhere updates TEMPLATES only (§21 #94: drafts detached), never the head; Template sync takes the rename", func(t *testing.T) {
		s := wcIENew(t)
		admin, slug, templateID, id := s.admin, s.slug, s.templateID, s.id
		dep0 := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		body := A{
			"ports": L{
				A{"name": "web", "port": 8080, "protocol": "tcp", "description": "HTTP"},
				A{"name": "metrics", "port": 9100, "protocol": "tcp", "description": ""},
			},
			"env": L{
				A{"name": "APP_ENV", "value": "prod", "options": L{"prod", "dev"}, "required": false, "secret": false, "description": ""},
				A{"name": "LOG", "value": "info", "options": nil, "required": false, "secret": false, "description": ""},
			},
			"volumes": L{
				A{"name": "store", "mountPath": "/data", "description": ""},
				A{"name": "cache", "mountPath": "/cache", "kind": "dir", "description": ""},
			},
			"healthcheck": A{"type": "http", "portName": "web", "path": "/healthz", "expectStatus": 200, "intervalSec": 10, "timeoutSec": 5, "retries": 3, "startPeriodSec": 5},
			"renames": L{
				A{"kind": "port", "from": "http", "to": "web"},
				A{"kind": "env", "from": "NODE_ENV", "to": "APP_ENV"},
				A{"kind": "volume", "from": "data", "to": "store"},
			},
		}
		e := admin.API.Rejects(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": body}, "conflict", 409)
		det := e.Details
		Eq(t, det.At("reason"), "in-use")
		Eq(t, wcIEItems(det.At("items"), true), L{"port:http>web:renamed", "env:NODE_ENV>APP_ENV:renamed", "volume:data>store:renamed"})
		port := det.At("items.0.usedBy")
		True(t, wcSome(port, func(u J) bool {
			return u.At("kind").Str() == "template" && u.At("templateId").Str() == templateID && u.At("containerSlug").Str() == slug && u.At("field").Str() == "ports[http]"
		}))
		True(t, wcSome(port, func(u J) bool {
			return u.At("kind").Str() == "deployment" && u.At("deploymentId").Str() == id && u.At("source").Str() == "draft" && u.At("field").Str() == "edge.routes[main].portName"
		}))
		True(t, wcSome(port, func(u J) bool {
			return u.At("containerSlug").Str() == slug+"-b" && strings.HasPrefix(u.At("field").Str(), "env[UP].value")
		}), "expression in another container")
		True(t, wcSome(det.At("items.1.usedBy"), func(u J) bool { return u.At("field").Str() == "env[CFG].value" }))
		False(t, wcSome(det.At("items"), func(x J) bool {
			return wcSome(x.At("usedBy"), func(u J) bool {
				return u.At("containerSlug").Str() == slug+"-b" && !strings.HasPrefix(u.At("field").Str(), "env[UP]")
			})
		}), "the v2 container is not a use of v1")
		Eq(t, admin.API.Call(t, "getImage", A{"serviceSlug": slug, "version": "v1"}).At("ports.0.name"), "http", "nothing saved")

		// rename everywhere
		everywhere := A{"renameEverywhere": true}
		for k, v := range body {
			everywhere[k] = v
		}
		img := admin.API.Call(t, "patchImage", A{"serviceSlug": slug, "version": "v1", "body": everywhere})
		Eq(t, img.At("ports").Pluck("name"), L{"web", "metrics"})
		tp := admin.API.Call(t, "getTemplate", A{"templateId": templateID})
		Eq(t, tp.At("version"), s.templateVersion.Num()+1)
		tc := wcIEContainer(tp, slug)
		Eq(t, tc.At("ports.0.portName"), "web")
		Eq(t, tc.At("env.0.name"), "APP_ENV")
		Eq(t, tc.At("env.1.value"), "/cfg/{{ container.env.APP_ENV }}.json")
		Eq(t, tc.At("volumes.0.imageVolume"), "store")
		Eq(t, tp.At("sites.0.config.edge.routes.0.action.portName"), "web")
		Eq(t, wcIEContainer(tp, slug+"-b").At("env").Find("name", "UP").At("value"), "http://{{ site.app.host }}:{{ site.app.port."+slug+".web }}")
		Eq(t, wcIEContainer(tp, slug+"-b").At("ports.0.portName"), "http", "other version untouched")
		// §21 #94: the deployment draft is detached — NOT rewritten (old names kept, no draft.updated activity)
		dep := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, dep.At("draftVersion"), dep0.At("draftVersion"), "draft untouched")
		Eq(t, wcIEContainer(dep.At("draft"), slug).At("ports.0.portName"), "http")
		Eq(t, dep.At("draft.sites.0.config.edge.routes.0.action.portName"), "http")
		rev := admin.API.Call(t, "getRevision", A{"deploymentId": id, "revId": s.revID})
		Eq(t, wcIEContainer(rev.At("config"), slug).At("ports.0.portName"), "http", "head unchanged")
		True(t, wcSome(rev.At("catalog.images").Key(slug+":v1").At("ports"), func(p J) bool { return p.At("name").Str() == "http" }), "head keeps its ImageSpec copy")
		acts := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "draft.updated"}})
		False(t, wcSome(acts.At("items"), func(a J) bool { return a.At("data.image").Str() == slug+":v1" }), "no draft.updated by the rename")
		// audit: templates rewritten + the deployments that were NOT rewritten (need a Template sync or a manual edit)
		au := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "services/" + slug + "/images/v1", "action": "image.updated"}})
		re := au.At("items.0.data.renamedEverywhere")
		True(t, wcSome(re.At("templates"), func(x J) bool { return x.At("templateId").Str() == templateID }))
		True(t, wcSome(re.At("deploymentsNotRewritten"), func(x J) bool { return x.At("deploymentId").Str() == id && x.At("references").Num() > 0 }), re)
		ta := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "templates/" + templateID, "action": "template.committed"}})
		True(t, wcSome(ta.At("items"), func(x J) bool {
			return x.At("data.reason").Str() == "image-rename-everywhere" && x.At("requestId").V == au.At("items.0.requestId").V
		}))
		// the stale draft is not publishable until it is fixed …
		diff0 := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		Eq(t, diff0.At("publishable"), false, "old names on the renamed image")
		// §21 #105: the rewrite is ONE COMMIT on the template (no working copy); then compare template → deployment and
		// cherry-pick the rename (replaces Template sync)
		th := admin.API.Call(t, "listTemplateCommits", A{"templateId": templateID, "query": A{"limit": 1}})
		Match(t, th.At("items.0.message"), `^Rename everywhere: `)
		Eq(t, th.At("items.0.author.email"), admin.Email)
		Eq(t, th.At("items.0.commitId"), tp.At("branch.head.commitId"))
		source := A{"branch": A{"kind": "template", "templateId": templateID}, "at": "head"}
		target := A{"kind": "deployment", "deploymentId": id}
		plan := admin.API.Call(t, "compareBranches", A{"body": A{"source": source, "target": target}})
		Eq(t, plan.At("base.kind"), "record")
		True(t, wcSome(plan.At("items"), func(x J) bool {
			return x.At("key").Str() == "sites/app/containers/"+slug+"/ports" && x.At("kind").Str() == "theirs"
		}), plan.At("items"))
		True(t, wcSome(plan.At("items"), func(x J) bool {
			return x.At("key").Str() == "sites/app/edge/routes/main" && x.At("kind").Str() == "theirs"
		}))
		choices := L{}
		for _, x := range plan.At("items").Filter(func(x J) bool { return x.At("kind").Str() == "theirs" }) {
			choices = append(choices, A{"key": x.At("key").V, "take": "source"})
		}
		head := admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("branch.head.commitId")
		m := admin.API.Call(t, "merge", A{"body": A{"source": source, "target": target, "targetHead": head, "sourceCommit": plan.At("source.commit.commitId"), "choices": choices, "message": "take the port rename"}})
		Eq(t, m.At("kind"), "merge")
		dep2 := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, wcIEContainer(dep2.At("draft"), slug).At("ports.0.portName"), "web")
	})

	t.Run("removal of an in-use name is blocked (also with renameEverywhere); unused removal, additions and port retargets are ok", func(t *testing.T) {
		s := wcIENew(t)
		admin, slug := s.admin, s.slug
		cur := admin.API.Call(t, "getImage", A{"serviceSlug": slug, "version": "v1"})
		patch := func(body A) A { return A{"serviceSlug": slug, "version": "v1", "body": body} }
		e := admin.API.Rejects(t, "patchImage", patch(A{"volumes": cur.At("volumes").Filter(func(v J) bool { return v.At("name").Str() != "data" }), "renameEverywhere": true}), "conflict", 409)
		Eq(t, wcIEItems(e.Details.At("items"), false), L{"volume:data:removed"})
		True(t, wcSome(e.Details.At("items.0.usedBy"), func(u J) bool { return u.At("field").Str() == "volumes[data]" }))
		// a removal disguised as a different name (no rename entry) is a removal too
		e2 := admin.API.Rejects(t, "patchImage", patch(A{"ports": L{wcSpread(cur.At("ports.0"), A{"name": "web"}), cur.At("ports.1")}, "healthcheck": nil}), "conflict", 409)
		Eq(t, e2.Details.At("items.0.change"), "removed")
		// cache is mounted as "none" on the container: a row = still a use; LOG is not used → removable
		envs := JOf(cur.At("env").Filter(func(x J) bool { return x.At("name").Str() != "LOG" })).Clone()
		withNew := append(envs.V.(L), A{"name": "NEW_VAR", "value": "x", "options": nil, "required": false, "secret": false, "description": "added"})
		env := admin.API.Call(t, "patchImage", patch(A{"env": withNew}))
		Eq(t, env.At("env").Pluck("name"), L{"NODE_ENV", "NEW_VAR"})
		// port number / protocol change of an in-use port: allowed (mappings reference the name)
		pt := admin.API.Call(t, "patchImage", patch(A{"ports": L{wcSpread(cur.At("ports.0"), A{"port": 8081}), cur.At("ports.1")}}))
		Eq(t, pt.At("ports.0.port"), 8081)
		au := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "services/" + slug + "/images/v1", "action": "image.updated"}})
		True(t, wcSome(au.At("items"), func(x J) bool { _, ok := x.At("data.portRetargeted").V.(L); return ok }))
	})
}
