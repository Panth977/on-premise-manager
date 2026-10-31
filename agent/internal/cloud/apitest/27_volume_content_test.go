package apitest

import (
	"sort"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

func wdShaOf(s string) string { return "sha256:" + Sha256Hex(s) }

// wdEntry is entry(path, text, render = none, mode = 0644).
func wdEntry(path, text, render string) A {
	if render == "" {
		render = "none"
	}
	return A{"path": path, "sha256": wdShaOf(text), "size": len(text), "mode": "0644", "render": render}
}

func wdTree(dirs L, entries ...A) A {
	l := L{}
	for _, e := range entries {
		l = append(l, e)
	}
	return A{"entries": l, "dirs": dirs}
}

func wdSeed(tree A) A {
	m := A{"kind": "tree"}
	for k, v := range tree {
		m[k] = v
	}
	return m
}

func wdWith(base A, extra A) A {
	m := A{}
	for k, v := range base {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func wdAppSite(t testing.TB, admin *Person, id string) (J, J) {
	t.Helper()
	d := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
	return wdSiteOf(d.At("draft"), "app").Clone(), d.At("draftVersion")
}

func wdVol0(site J) map[string]any { return site.At("config.volumes.0").Map() }

// Port of backend/test/27-volume-content.test.ts.
func TestVolumeContent(t *testing.T) {
	const miB = 1024 * 1024
	persist := A{"exclude": L{"node_modules/", "*.log"}, "maxBytes": 50 * miB, "auto": true}

	t.Run("§21 #117 persist content is committed: removed Send / edit endpoints 410, commit rules, agent blob upload for any site, old-agent snapshots readable", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		one := A{"deploymentId": id, "siteSlug": "app", "volumeSlug": "hello-api-data"}

		// not persisted yet → 404; persist on in the draft → listed
		admin.API.Rejects(t, "getVolumeContent", one, "not-found", 404)
		site, dv := wdAppSite(t, admin, id)
		wdVol0(site)["persist"] = persist
		admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": dv, "site": site}})
		list := admin.API.Call(t, "listVolumeContent", A{"deploymentId": id})
		Eq(t, list.At("items").Len(), 1)
		Eq(t, list.At("items.0.versionCount"), 0)

		textA := `[{"id":"flow-` + Uniq("") + `"}]`
		textB := "module.exports = " + Itoa(int(time.Now().UnixMilli())) + ";"
		for _, x := range []string{textA, textB} {
			admin.API.Call(t, "uploadBlob", A{"body": []byte(x)})
		}
		t1 := wdTree(L{"lib"}, wdEntry("flows.json", textA, ""))

		// the cloud copy is no editing surface any more: every write answers 410 gone
		admin.API.Rejects(t, "createVolumeContentVersion", wdWith(one, A{"body": A{"baseVersionId": nil, "tree": t1, "note": ""}}), "gone", 410)
		admin.API.Rejects(t, "restoreVolumeContentVersion", wdWith(one, A{"versionId": ulid.New(), "body": A{"note": ""}}), "gone", 410)
		admin.API.Rejects(t, "requestVolumeSnapshot", one, "gone", 410)
		admin.API.Rejects(t, "sendVolumeContent", wdWith(one, A{"body": A{"versionId": ulid.New(), "restart": false, "note": ""}}), "gone", 410)
		admin.API.Rejects(t, "cancelVolumeContentPush", one, "gone", 410)

		// the content is the persist volume's seed tree, committed with the deployment: literal, nothing excluded
		withSeed := func(tree A) (J, *APIError) {
			s2, dv2 := wdAppSite(t, admin, id)
			wdVol0(s2)["seed"] = wdSeed(tree)
			if _, e := admin.API.Try("putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": dv2, "site": s2}}); e != nil {
				return J{Undefined}, e
			}
			return admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion"), nil
		}
		// (the test client commits a draft save: the commit refuses it)
		_, e := withSeed(wdTree(L{}, wdEntry("a.txt", textA, "expressions")))
		rend := Rejected(t, e, "invalid", 422)
		wdHasCode(t, rend, "V_VOLUME_SEED")
		_, e = withSeed(wdTree(L{}, wdEntry("debug.log", textA, ""), wdEntry("flows.json", textB, "")))
		exc := Rejected(t, e, "invalid", 422)
		Eq(t, wdCodes(exc), []string{"V_VOLUME_CONTENT_EXCLUDED"})
		dv1, e := withSeed(t1)
		if e != nil {
			t.Fatal(e)
		}
		ok := Publish(t, admin.API, id, dv1, "persist content")
		True(t, ok.Revision.At("revId").Truthy())
		headSeed := wdSiteOf(admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft"), "app").At("config.volumes.0.seed")
		Eq(t, headSeed, wdSeed(t1))

		// agents: any enrolled site may upload the bytes of its commits (content-addressed, checked)
		app := EnrollSite(t, admin, id, "app")
		db := EnrollSite(t, admin, id, "db")
		textC := "binary-ish " + Uniq("")
		Eq(t, db.API.Call(t, "agentCheckBlobs", A{"body": A{"sha256s": L{wdShaOf(textA), wdShaOf(textC)}}}).At("missing"), L{wdShaOf(textC)})
		db.API.Rejects(t, "agentCheckBlobs", A{"body": A{"sha256s": L{"nope"}}}, "invalid", 422)
		db.API.Rejects(t, "agentPutBlob", A{"sha256": wdShaOf(textC), "body": []byte(textC + "x")}, "invalid", 422)
		Eq(t, db.API.Call(t, "agentPutBlob", A{"sha256": wdShaOf(textC), "body": []byte(textC)}), A{"sha256": wdShaOf(textC), "sizeBytes": len(textC)})
		Eq(t, app.API.Call(t, "agentCheckBlobs", A{"body": A{"sha256s": L{wdShaOf(textA), wdShaOf(textC)}}}).At("missing"), L{})

		// an older agent still uploads snapshots: the history stays readable; the cloud view carries no pushes
		head := app.API.Call(t, "getAgentRevision", A{"revId": ok.Revision.At("revId")}).At("revision")
		app.API.Call(t, "reportHead", A{"body": A{"revision": head}})
		snapTree := wdTree(L{}, wdEntry("data/c.bin", textC, ""), wdEntry("flows.json", textA, ""))
		treeHash, err := shared.VolumeTreeHash(toMap(t, JOf(snapTree)))
		NoErr(t, err)
		snap := A{"snapshotId": ulid.New(), "trigger": "manual-agent", "takenAt": Now(), "actor": "ops@example.com", "tree": snapTree, "treeHash": treeHash, "excludedCount": 0, "skippedCount": 0, "skipped": L{}, "requestId": nil, "pushId": nil, "note": ""}
		v1 := app.API.Call(t, "agentCreateVolumeSnapshot", A{"volumeSlug": "hello-api-data", "body": snap}).At("version")
		Eq(t, admin.API.Call(t, "listVolumeContentVersions", one).At("items").Pluck("versionId"), L{v1.At("versionId")})
		Eq(t, admin.API.Call(t, "getVolumeContentVersion", wdWith(one, A{"versionId": v1.At("versionId")})).At("tree"), snapTree)
		Eq(t, app.API.Call(t, "poll", A{"query": A{"wait": 0}}).At("volumeContent"), L{})

		// permissions: reading needs deployment.config.read
		nobody := NewPerson(t, L{})
		nobody.API.Rejects(t, "listVolumeContent", A{"deploymentId": id}, "not-found", 404)
	})

	t.Run("starter content: tree seeds publish with blobContents render errors, diff per file, agent blob access, legacy File seeds refused", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		badText := "listen {{ nope.thing }}\n"
		goodText := "deployment {{ deployment.id }} " + Uniq("") + "\n"
		plain := "static " + Uniq("")
		for _, x := range []string{badText, goodText, plain} {
			admin.API.Call(t, "uploadBlob", A{"body": []byte(x)})
		}
		site, dv := wdAppSite(t, admin, id)
		wdVol0(site)["seed"] = wdSeed(wdTree(L{"empty"}, wdEntry("cfg/app.conf", badText, "expressions"), wdEntry("static.txt", plain, "")))
		w := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": dv, "site": site}})
		False(t, w.At("validation.issues").Find("severity", "error").Exists(), w.At("validation.issues").String())
		// §21 #105: the render error blocks the commit (the change stays in the browser change set)
		at := "sites[app].config.volumes[hello-api-data].seed.entries[cfg/app.conf]"
		_, e := admin.API.Try("commitDeployment", A{"deploymentId": id, "body": A{"message": "x", "ifRevision": w.At("draftVersion")}})
		pub := Rejected(t, e, "invalid", 422)
		wdHasCode(t, pub, "V_FILE_RENDER")
		True(t, pub.Details.At("errors").FindF(func(b J) bool { return b.At("code").Str() == "V_FILE_RENDER" && b.At("path").Str() == at }).Exists(), pub.Details.String())
		// fix the entry → publishable; diff lists one change per file (blob before / after) + the empty folder
		wdVol0(site)["seed"] = wdSeed(wdTree(L{"empty"}, wdEntry("cfg/app.conf", goodText, "expressions"), wdEntry("static.txt", plain, "")))
		w2 := admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": w.At("draftVersion"), "site": site}})
		d2 := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		Eq(t, d2.At("publishable"), true, d2.At("blockers").String())
		p := Publish(t, admin.API, id, w2.At("draftVersion"), "starter content")
		Eq(t, p.Revision.At("seq"), 1)
		// the site may fetch the content blobs of an open proposal / its head
		app := EnrollSite(t, admin, id, "app")
		Eq(t, app.API.Fetch(t, "getAgentBlob", A{"sha256": wdShaOf(plain)}).Text(), plain)
		Eq(t, app.API.Fetch(t, "getAgentBlob", A{"sha256": wdShaOf(goodText)}).Text(), goodText)
		_, e = app.API.Send("getAgentBlob", A{"sha256": wdShaOf(badText)})
		Rejected(t, e, "forbidden", 403)
		// a second file change → the diff against the head shows exactly that file as changed
		plain2 := "static v2 " + Uniq("")
		admin.API.Call(t, "uploadBlob", A{"body": []byte(plain2)})
		r1 := app.API.Call(t, "getAgentRevision", A{"revId": p.Revision.At("revId")}).At("revision")
		app.API.Call(t, "reportHead", A{"body": A{"revision": r1}})
		wdVol0(site)["seed"] = wdSeed(wdTree(L{}, wdEntry("cfg/app.conf", goodText, "expressions"), wdEntry("new.txt", plain, ""), wdEntry("static.txt", plain2, "")))
		dv3 := admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion")
		admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": dv3, "site": site}})
		d3 := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		c3 := d3.At("changes").Filter(func(c J) bool {
			return strings.HasPrefix(c.At("path").Str(), "app/volumes/")
		})
		nz := func(v J) any {
			if v.IsNil() {
				return nil
			}
			return v.V
		}
		rows := []L{}
		for _, c := range c3 {
			rows = append(rows, L{c.At("path").V, c.At("kind").V, nz(c.At("blob.before")), nz(c.At("blob.after"))})
		}
		sort.Slice(rows, func(a, b int) bool { return rows[a][0].(string) < rows[b][0].(string) })
		Eq(t, rows, []L{
			{"app/volumes/hello-api-data/content/empty/", "removed", nil, nil},
			{"app/volumes/hello-api-data/content/new.txt", "added", nil, wdShaOf(plain)},
			{"app/volumes/hello-api-data/content/static.txt", "changed", wdShaOf(plain), wdShaOf(plain2)},
		})
		for _, c := range c3 {
			True(t, c.At("note").Truthy() && c.At("section").Str() == "volumes")
		}
		// legacy File seeds are refused in the draft (Files are never volume content)
		legacy := site.Clone()
		wdVol0(legacy)["seed"] = A{"kind": "files", "entries": L{A{"path": "banner.txt", "fileSlug": "banner"}}}
		le := admin.API.Rejects(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": dv3.Num() + 1, "site": legacy}}, "invalid", 422)
		wdHasCode(t, le, "V_VOLUME_SEED_FILE_REF")
	})

	t.Run("daily job prunes AUTO snapshots beyond the newest 200 versions (pinned / referenced / manual kept)", func(t *testing.T) {
		id := "prune" + Uniq("")
		st := DB.Collection("deployments").Doc(id).Collection("volumeContent").Doc("app:data")
		now := Now()
		ids := []string{}
		b := DB.NewBulkWriter()
		start := time.Now()
		for seq := 1; seq <= 205; seq++ {
			versionID := ulid.At(start.Add(time.Duration(seq) * time.Millisecond))
			ids = append(ids, versionID)
			origin, trigger, base := "snapshot", any("auto"), any(nil)
			if seq == 3 {
				trigger = "manual-agent"
			}
			if seq == 150 {
				origin, trigger, base = "edit", nil, ids[1]
			}
			b.Set(st.Collection("versions").Doc(versionID), A{"versionId": versionID, "seq": seq, "origin": origin, "trigger": trigger, "treeHash": "sha256:" + strings.Repeat("c", 64), "createdAt": now, "createdBy": "agent:app", "deploymentId": id, "siteSlug": "app", "volumeSlug": "data", "baseVersionId": base})
		}
		b.Close()
		NoErr(t, st.Set(A{"deploymentId": id, "siteSlug": "app", "volumeSlug": "data", "versionCount": 205, "lastSeq": 205, "latest": nil, "lastSnapshot": nil, "push": nil, "_open": false}))
		NoErr(t, st.Collection("pushes").Doc(ulid.New()).Set(A{"pushId": "p", "versionId": ids[0], "baseVersionId": nil, "status": "applied"}))
		r := RunDaily(t)
		True(t, r.At("volumeVersionsPruned").Num() >= 2)
		q, err := st.Collection("versions").OrderBy("seq").Get()
		NoErr(t, err)
		left := []int{}
		for _, d := range q.Docs {
			left = append(left, JOf(d.Data()).At("seq").Int())
		}
		Eq(t, len(left), 203)
		Eq(t, left[:4], []int{1, 2, 3, 6}) // 1 = a push's version, 2 = base of an edit, 3 = manual
		s, err := st.Get()
		NoErr(t, err)
		Eq(t, JOf(s.Data()).At("versionCount"), 203)
		RunDaily(t)
		q2, err := st.Collection("versions").Get()
		NoErr(t, err)
		Eq(t, len(q2.Docs), 203)
	})
}
