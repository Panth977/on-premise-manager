package apitest

import (
	"regexp"
	"testing"

	"opm/agent/internal/validate"
)

// wcDPreSite is preDSite: a round-C (pre-D) app site: no `volumes` list; hello-api mounts its image volume through the old
// `data` source with readOnly true; implicit = hello-api has NO row for its image volume.
func wcDPreSite(s J, implicit bool) J {
	cfg := wcOmit(s.At("config"), "volumes")
	cs := L{}
	for _, c := range s.At("config.containers").Arr() {
		if c.At("containerSlug").Str() != "hello-api" {
			cs = append(cs, c.V)
			continue
		}
		vols := L{}
		if !implicit {
			vols = L{A{"imageVolume": "data", "mountPath": nil, "source": A{"kind": "data", "name": "hello-api-data"}, "readOnly": true}}
		}
		cs = append(cs, wcSpread(c, A{"volumes": vols}).V)
	}
	cfg.Map()["containers"] = cs
	return wcSpread(s, A{"config": cfg.V})
}

// wcDPreConfig is preDConfig.
func wcDPreConfig(cfg J, implicit bool) J {
	return wcSpread(cfg, A{"sites": wcWithApp(cfg.At("sites"), wcDPreSite(wcApp(cfg.At("sites")), implicit))})
}

// wcDAssert is assertD.
func wcDAssert(t *testing.T, sc J) {
	t.Helper()
	Eq(t, sc.At("volumes"), L{A{"volumeSlug": "hello-api-data", "label": "hello-api-data", "description": "", "backup": true, "location": A{"kind": "auto"}, "kind": "dir", "seed": nil}})
	api := sc.At("containers").Find("containerSlug", "hello-api")
	Eq(t, api.At("volumes"), L{A{"imageVolume": "data", "mountPath": nil, "source": A{"kind": "volume", "volumeSlug": "hello-api-data"}, "readOnly": false}})
	web := sc.At("containers").Find("containerSlug", "hello-web")
	Eq(t, web.At("volumes"), L{A{"imageVolume": nil, "mountPath": "/etc/hello/banner.txt", "source": A{"kind": "file", "fileSlug": "banner"}, "readOnly": true}})
}

// wcAllVolumesAuto is list.every(s => s.config.volumes.every(v => v.location.kind === 'auto')).
func wcAllVolumesAuto(sites J) bool {
	for _, s := range sites.Arr() {
		for _, v := range s.At("config.volumes").Arr() {
			if v.At("location.kind").Str() != "auto" {
				return false
			}
		}
	}
	return true
}

// Port of backend/test/12-round-d.test.ts.
func TestRoundD(t *testing.T) {
	t.Run("normalize on write: pre-D site (data source, read-only, implicit row) → declared volume, volume source, readOnly false (draft PUT + template PATCH)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		dep := HelloDeployment(t, admin)
		id := dep.ID()
		// draft PUT site: explicit data row with readOnly true
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": wcDPreSite(wcAppSite(Hello(t).At("config")), false)}})
		Eq(t, w.At("validation.ok"), true)
		False(t, wcSome(w.At("validation.issues"), func(i J) bool { return i.At("severity").Str() == "error" }))
		raw0 := wcDocJSON(t, "deployments/"+id, "_draft")
		wcDAssert(t, wcApp(raw0.At("sites")).At("config"))
		// draft PUT sites section: implicit image volume (no row) → explicit row + declared
		sites := admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.sites")
		all := wcWithApp(sites, wcDPreSite(wcApp(sites), true))
		w2 := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "sites", "body": A{"ifRevision": 1, "items": all}})
		Eq(t, w2.At("validation.ok"), true)
		wcDAssert(t, wcApp(admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.sites")).At("config"))
		// template PATCH
		tpl := admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID})
		tw := admin.API.Call(t, "patchTemplate", A{"templateId": dep.TemplateID, "body": A{"ifVersion": tpl.At("version"), "sites": wcWithApp(tpl.At("sites"), wcDPreSite(wcApp(tpl.At("sites")), true))}})
		wcDAssert(t, wcApp(tw.At("template.sites")).At("config"))
		// the db site was already D (declared redis-data "Redis data") → unchanged, nothing auto-declared
		Eq(t, tw.At("template.sites").Find("siteSlug", "db").At("config.volumes"), L{A{"volumeSlug": "redis-data", "label": "Redis data", "description": "", "backup": true, "location": A{"kind": "auto"}, "kind": "dir", "seed": nil}})
	})

	t.Run("normalize on read: a pre-D document written directly to Firestore is served declared + explicit (image volumes known)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		dep := HelloDeployment(t, admin)
		tPath := "templates/" + dep.TemplateID
		UpdateDoc(t, tPath, A{"_body": wcStringify(t, wcDPreConfig(wcDocJSON(t, tPath, "_body"), true))})
		tpl := admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID})
		wcDAssert(t, wcApp(tpl.At("sites")).At("config"))
		// a PATCH that does not touch sites writes the normalized (explicit + declared) sites back — no silent unmount
		admin.API.Call(t, "patchTemplate", A{"templateId": dep.TemplateID, "body": A{"ifVersion": tpl.At("version"), "description": "touched"}})
		wcDAssert(t, wcApp(wcDocJSON(t, tPath, "_body").At("sites")).At("config"))
		// deployment draft
		dPath := "deployments/" + dep.ID()
		UpdateDoc(t, dPath, A{"_draft": wcStringify(t, wcDPreConfig(wcDocJSON(t, dPath, "_draft"), false))})
		wcDAssert(t, wcApp(admin.API.Call(t, "getDeployment", A{"deploymentId": dep.ID()}).At("draft.sites")).At("config"))
	})

	t.Run("validation: undeclared volume → 422 V_VOLUME_UNDECLARED; unused → V_VOLUME_UNUSED warning; not mounted → warning; file readOnly forced; diff section volumes", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		dep := HelloDeployment(t, admin)
		id := dep.ID()
		// a current (D) config is never auto-declared
		undeclared := wcAppSite(Hello(t).At("config"))
		undeclared.At("config").Map()["volumes"] = L{}
		e := admin.API.Rejects(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": undeclared}}, "invalid", 422)
		apiData := regexp.MustCompile(`hello-api-data`)
		True(t, wcSome(e.Details.At("errors"), func(x J) bool {
			return x.At("code").Str() == "V_VOLUME_UNDECLARED" && apiData.MatchString(x.At("message").Str())
		}), e.Details)
		tpl := admin.API.Call(t, "getTemplate", A{"templateId": dep.TemplateID})
		e2 := admin.API.Rejects(t, "patchTemplate", A{"templateId": dep.TemplateID, "body": A{"ifVersion": tpl.At("version"), "sites": wcWithApp(tpl.At("sites"), undeclared)}}, "invalid", 422)
		True(t, wcCodeIs(e2.Details.At("errors"), "V_VOLUME_UNDECLARED"))
		// bad volume slug → V_FORMAT
		badSlug := wcAppSite(Hello(t).At("config"))
		wcPush(badSlug.At("config"), "volumes", wcVolume("Bad Name"))
		e3 := admin.API.Rejects(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": badSlug}}, "invalid", 422)
		True(t, wcCodeIs(e3.Details.At("errors"), "V_FORMAT"))
		// publish the fixture, then: a declared but unused volume (warning), an image volume without row (warning), a file mount sent read-write (forced read-only)
		Publish(t, admin.API, id, 0, "r1")
		site := wcAppSite(Hello(t).At("config"))
		wcPush(site.At("config"), "volumes", validate.NewSiteVolume("spare", "Spare", "not used yet", false))
		cs := site.At("config.containers")
		cs.Find("containerSlug", "hello-api").Map()["volumes"] = L{}
		cs.Find("containerSlug", "hello-web").At("volumes.0").Map()["readOnly"] = false
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": 0, "site": site}})
		Eq(t, w.At("validation.ok"), true)
		codes := L{}
		for _, i := range w.At("validation.issues").Arr() {
			codes = append(codes, i.At("severity").Str()+":"+i.At("code").Str())
		}
		has := func(c string) bool { return wcStrIn(codes, c) }
		True(t, has("warning:V_VOLUME_UNUSED"), JOf(codes))
		True(t, has("warning:V_VOLUME_NOT_MOUNTED"), JOf(codes))
		saved := wcApp(admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.sites")).At("config")
		Eq(t, saved.At("containers").Find("containerSlug", "hello-web").At("volumes.0.readOnly"), true, "file mounts are always read-only")
		diff := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		vol := JOf(diff.At("changes").Filter(func(c J) bool { return c.At("section").Str() == "volumes" }))
		True(t, wcSome(vol, func(c J) bool {
			return c.At("siteSlug").Str() == "app" && regexp.MustCompile(`spare`).MatchString(wcStringify(t, c)) && c.At("kind").Str() == "added"
		}), vol)
	})

	// ───────────────────────────────────────── part 2: storage locations (one copy per site)
	t.Run(`locations: missing → auto on write + read; custom "./" + absolute kept; malformed / reserved / nested → 422 V_LOCATION`, func(t *testing.T) {
		admin := NewPerson(t, "admin")
		templateID := HelloDeployment(t, admin).TemplateID
		tpl := admin.API.Call(t, "getTemplate", A{"templateId": templateID})
		strip := func(x J) J { return wcOmit(x, "location") }
		noVolLocation := func(sites J) L {
			out := L{}
			for _, s := range sites.Arr() {
				vols := L{}
				for _, v := range s.At("config.volumes").Arr() {
					vols = append(vols, strip(v).V)
				}
				out = append(out, wcSpread(s, A{"config": wcSpread(s.At("config"), A{"volumes": vols})}).V)
			}
			return out
		}
		noFileLocation := func(files J) L {
			out := L{}
			for _, f := range files.Arr() {
				out = append(out, strip(f).V)
			}
			return out
		}
		// missing locations on write → auto
		w := admin.API.Call(t, "patchTemplate", A{"templateId": templateID, "body": A{"ifVersion": tpl.At("version"), "files": noFileLocation(tpl.At("files")), "sites": noVolLocation(tpl.At("sites"))}})
		Eq(t, w.At("template.files").Pluck("location"), L{A{"kind": "auto"}})
		True(t, wcAllVolumesAuto(w.At("template.sites")))
		stored := wcDocJSON(t, "templates/"+templateID, "_body")
		Eq(t, stored.At("files.0.location"), A{"kind": "auto"}, "stored with location")
		// custom locations are kept
		files1 := L{}
		for _, f := range w.At("template.files").Arr() {
			files1 = append(files1, wcSpread(f, A{"location": A{"kind": "path", "path": "./config/banner.txt"}}).V)
		}
		appVolumes := func(loc A) L {
			vols := L{}
			for _, v := range wcApp(w.At("template.sites")).At("config.volumes").Arr() {
				vols = append(vols, wcSpread(v, A{"location": loc}).V)
			}
			app := wcApp(w.At("template.sites"))
			return wcWithApp(w.At("template.sites"), wcSpread(app, A{"config": wcSpread(app.At("config"), A{"volumes": vols})}))
		}
		sites1 := appVolumes(A{"kind": "path", "path": "/srv/hello/api-data"})
		w1 := admin.API.Call(t, "patchTemplate", A{"templateId": templateID, "body": A{"ifVersion": w.At("template.version"), "files": files1, "sites": sites1}})
		Eq(t, w1.At("template.files.0.location"), A{"kind": "path", "path": "./config/banner.txt"})
		Eq(t, wcApp(w1.At("template.sites")).At("config.volumes.0.location"), A{"kind": "path", "path": "/srv/hello/api-data"})
		v := w1.At("template.version")
		code := func(files, sites any, expect string) {
			t.Helper()
			body := A{"ifVersion": v}
			if files != nil {
				body["files"] = files
			}
			if sites != nil {
				body["sites"] = sites
			}
			e := admin.API.Rejects(t, "patchTemplate", A{"templateId": templateID, "body": body}, "invalid", 422)
			re := regexp.MustCompile(expect)
			True(t, wcSome(e.Details.At("errors"), func(x J) bool {
				return x.At("code").Str() == "V_LOCATION" && re.MatchString(x.At("path").Str()+" "+x.At("message").Str())
			}), e.Details)
		}
		file1 := func(loc A) L { return L{wcSpread(J{files1[0]}, A{"location": loc}).V} }
		code(file1(A{"kind": "bogus"}), nil, `files\[banner\]\.location`)
		code(nil, appVolumes(A{"kind": "path"}), `volumes\[hello-api-data\]\.location`)
		code(file1(A{"kind": "path", "path": "./blobs/banner"}), nil, `belongs to the agent`)
		code(file1(A{"kind": "path", "path": "./a/../b"}), nil, `\.\.`)
		code(file1(A{"kind": "path", "path": "/srv/hello/api-data/banner.txt"}), nil, `same place or nested`)
		// read: a stored document without locations is served with auto
		body := wcDocJSON(t, "templates/"+templateID, "_body")
		body.Map()["files"] = noFileLocation(body.At("files"))
		body.Map()["sites"] = noVolLocation(body.At("sites"))
		UpdateDoc(t, "templates/"+templateID, A{"_body": wcStringify(t, body)})
		r := admin.API.Call(t, "getTemplate", A{"templateId": templateID})
		Eq(t, r.At("files.0.location"), A{"kind": "auto"})
		True(t, wcAllVolumesAuto(r.At("sites")))
	})
}

// wcStrIn is list.includes(s).
func wcStrIn(list L, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
