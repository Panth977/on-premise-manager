package apitest

import (
	"strings"
	"testing"

	"opm/agent/internal/cloud/core"
	"opm/agent/internal/model"
)

// waHelloVars is HELLO().config.variables with the theme variable's value replaced.
func waHelloVars(t testing.TB, theme any) L {
	t.Helper()
	out := L{}
	for _, v := range Hello(t).At("config.variables").Clone().Arr() {
		if v.At("variableSlug").Str() == "theme" {
			v.Map()["value"] = theme
		}
		out = append(out, v.V)
	}
	return out
}

// Port of backend/test/04-deployments.test.ts.
func TestDeployments(t *testing.T) {
	t.Run("templates (§21 #105): create v1, details PATCH (ifVersion), config only by a commit; save-level errors → 422 nothing saved", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		api := admin.Real
		SeedHelloCatalog(t)
		tpl := api.Call(t, "createTemplate", A{"body": A{"label": "Hello", "icon": "box", "description": "d"}})
		id := tpl.At("templateId").Str()
		Eq(t, tpl.At("version"), 1)
		internal := false
		for _, k := range api.Call(t, "getTemplate", A{"templateId": id}).Keys() {
			internal = internal || strings.HasPrefix(k, "_")
		}
		Eq(t, internal, false, "no internal fields")
		Eq(t, L{tpl.At("variables"), tpl.At("secrets"), tpl.At("files"), tpl.At("sites")}, L{L{}, L{}, L{}, L{}})
		h := Hello(t)
		api.Rejects(t, "patchTemplate", A{"templateId": id, "body": A{"ifVersion": 7, "label": "x"}}, "conflict")
		api.Rejects(t, "patchTemplate", A{"templateId": id, "body": A{"ifVersion": 1, "variables": h.At("config.variables")}}, "gone", 410)
		base := tpl.At("branch.head.commitId").Str()
		withPlain := h.At("config").Clone()
		plain := L{}
		for _, s := range h.At("config.secrets").Arr() {
			m := JOf(dropKeys(s, "value", "rotatedAt"))
			m.Map()["plainValue"] = s.At("value").V
			plain = append(plain, m.V)
		}
		withPlain.Map()["secrets"] = plain
		withValue := api.Rejects(t, "commitTemplate", A{"templateId": id, "body": A{"message": "v", "baseCommitId": base, "config": withPlain}}, "invalid", 422)
		Match(t, withValue.Details, `V_TEMPLATE_SECRET_VALUE`)
		Eq(t, api.Call(t, "getTemplate", A{"templateId": id}).At("version"), 1, "nothing saved")
		// §21 #107: a commit's new blobs must be stored (or staged) — the banner File content
		api.Call(t, "uploadBlob", A{"body": []byte(h.At("banner").Str())})
		secrets := secretsInput(h.At("config.secrets"), "value", "rotatedAt")
		c := api.Call(t, "commitTemplate", A{"templateId": id, "body": A{"message": "hello config", "baseCommitId": base, "config": A{"variables": h.At("config.variables"), "secrets": secrets, "files": h.At("config.files"), "sites": h.At("config.sites")}}})
		Eq(t, c.At("seq"), 2)
		t2 := api.Call(t, "getTemplate", A{"templateId": id})
		Eq(t, t2.At("version"), 2, "a commit that changes the config moves the version")
		Eq(t, t2.At("branch.head.commitId"), c.At("commitId"))
		s := api.Call(t, "listTemplates", A{})
		item := s.At("items").Find("templateId", id)
		Eq(t, item.At("counts"), A{"variables": 5, "secrets": 1, "files": 1, "sites": 2})
		Eq(t, item.At("siteSlugs"), L{"db", "app"})
		// a save-level error: unknown service
		bad := h.At("config.sites").Clone()
		bad.At("0.config.containers.0").Map()["serviceSlug"] = "no-such-service"
		api.Rejects(t, "commitTemplate", A{"templateId": id, "body": A{"message": "bad", "baseCommitId": c.At("commitId"), "config": A{"variables": h.At("config.variables"), "secrets": secrets, "files": h.At("config.files"), "sites": bad}}}, "invalid")
		api.Call(t, "deleteTemplate", A{"templateId": id})
		api.Rejects(t, "getTemplate", A{"templateId": id}, "not-found")
	})

	t.Run("deployments: create from template generates secrets + access key; view masks secrets; delete is soft", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		hd := HelloDeployment(t, admin)
		dep, accessKey, id := hd.Dep, hd.AccessKey.Str(), hd.ID()
		Match(t, accessKey, `^[A-Z2-7]{4}(-[A-Z2-7]{4}){5}$`)
		stored := Doc(t, "deployments/"+id)
		ok, err := core.VerifyAccessKey(accessKey, stored.At("accessKeyHash").Str())
		NoErr(t, err)
		True(t, ok, "PHC argon2id hash verifies")
		Match(t, stored.At("accessKeyHash"), `^\$argon2id\$v=19\$m=65536,t=3,p=1\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$`)
		Eq(t, dep.At("draftVersion"), 0)
		Eq(t, dep.At("agentEpoch"), 2, "the create commit wrote a compat proposal (agentEpoch +1)")
		Eq(t, dep.At("head"), nil)
		// §21 #104 the create commit + origin link (srcTemplate is frozen legacy data)
		Eq(t, dep.At("branch.head.seq"), 1)
		Eq(t, dep.At("branch.head.kind"), "create")
		Eq(t, dep.At("branch.origin.branch.kind"), "template")
		Eq(t, dep.At("draft.secrets.0.value"), model.Mask)
		Eq(t, dep.At("draft.secrets.0.isSet"), true)
		Eq(t, dep.At("accessKeyHash"), Undefined)
		True(t, dep.At("srcTemplate.label").Len() > 0)
		rev := admin.API.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "api-token"})
		Match(t, rev.At("value"), `^[A-Za-z0-9]{24}$`)
		acts := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"site": "_cloud"}})
		True(t, waSome(acts.At("items"), func(a J) bool {
			return a.At("type").Str() == "secret.revealed" && a.At("data.where").Str() == "cloud"
		}))
		True(t, waSome(acts.At("items"), func(a J) bool { return a.At("type").Str() == "deployment.created" }))
		// patch
		p := admin.API.Call(t, "patchDeployment", A{"deploymentId": id, "body": A{"label": "Renamed", "image": "https://x.example/logo.png"}})
		Eq(t, p.At("label"), "Renamed")
		admin.API.Rejects(t, "patchDeployment", A{"deploymentId": id, "body": A{"tagSlugs": L{"no-such-tag"}}}, "invalid")
		admin.API.Rejects(t, "patchDeployment", A{"deploymentId": id, "body": A{"managerAccountUids": L{"nobody"}}}, "invalid")
		list := admin.API.Call(t, "listDeployments", A{"query": A{"limit": 500}})
		sum := list.At("items").Find("deploymentId", id)
		Eq(t, sum.At("siteSlugs"), L{"db", "app"})
		Eq(t, sum.At("sitesTotal"), 2)
		Eq(t, sum.At("sitesOnline"), 0)
		admin.API.Rejects(t, "deleteDeployment", A{"deploymentId": id, "body": A{"confirm": "wrong"}}, "invalid")
		Eq(t, admin.API.Call(t, "deleteDeployment", A{"deploymentId": id, "body": A{"confirm": "Renamed"}}), A{"deleted": true})
		admin.API.Rejects(t, "getDeployment", A{"deploymentId": id}, "not-found", 404)
		after := Doc(t, "deployments/"+id)
		Eq(t, after.At("deleted"), true)
		Eq(t, after.At("agentEpoch"), 3)
		False(t, admin.API.Call(t, "listDeployments", A{"query": A{"limit": 500}}).At("items").Find("deploymentId", id).Exists())
	})

	t.Run("deployment access: create needs deployments.write covering tags; list filtered; managers see their deployment", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		tag := Uniq("tg")
		admin.API.Call(t, "createTag", A{"body": A{"tagSlug": tag, "label": "T", "icon": "tag", "color": "#123456"}})
		tagWriter := NewPerson(t, L{A{"resource": "deployments", "actions": L{"read", "write"}, "scope": A{"kind": "tags", "tagSlugs": L{tag}}}})
		tagWriter.API.Rejects(t, "createDeployment", A{"body": A{"label": "no tag", "templateId": nil, "tagSlugs": L{}, "managerAccountUids": L{}}}, "forbidden")
		d := tagWriter.API.Call(t, "createDeployment", A{"body": A{"label": "tagged", "templateId": nil, "tagSlugs": L{tag}, "managerAccountUids": L{}}})
		other := admin.API.Call(t, "createDeployment", A{"body": A{"label": "other", "templateId": nil, "tagSlugs": L{}, "managerAccountUids": L{}}})
		dID, otherID := d.At("deployment.deploymentId").Str(), other.At("deployment.deploymentId").Str()
		mine := tagWriter.API.Call(t, "listDeployments", A{"query": A{"limit": 500}})
		True(t, mine.At("items").Find("deploymentId", dID).Exists())
		False(t, mine.At("items").Find("deploymentId", otherID).Exists())
		tagWriter.API.Rejects(t, "getDeployment", A{"deploymentId": otherID}, "not-found", 404)
		filtered := admin.API.Call(t, "listDeployments", A{"query": A{"tag": tag}})
		Eq(t, filtered.At("items").Pluck("deploymentId"), L{dID})
		// manager
		mgr := NewPerson(t, L{})
		admin.API.Call(t, "patchDeployment", A{"deploymentId": otherID, "body": A{"managerAccountUids": L{mgr.UID}}})
		Eq(t, mgr.API.Call(t, "getDeployment", A{"deploymentId": otherID}).At("label"), "other")
		True(t, mgr.API.Call(t, "getDiff", A{"deploymentId": otherID}).At("blockers").Truthy())
		mgr.API.Rejects(t, "patchDeployment", A{"deploymentId": otherID, "body": A{"label": "x"}}, "forbidden")
		// deployment-scoped reader sees it but cannot write config
		cfgReader := NewPerson(t, L{A{"resource": "deployment.config", "actions": L{"read"}, "scope": A{"kind": "deployments", "deploymentIds": L{otherID}}}})
		Eq(t, cfgReader.API.Call(t, "getDeployment", A{"deploymentId": otherID}).At("deploymentId"), otherID)
		cfgReader.API.Rejects(t, "putDraftSection", A{"deploymentId": otherID, "section": "variables", "body": A{"ifRevision": 0, "items": L{}}}, "forbidden")
		cfgReader.API.Rejects(t, "revealSecret", A{"deploymentId": otherID, "secretSlug": "x"}, "forbidden")
		cfgReader.API.Rejects(t, "getSetup", A{"deploymentId": otherID}, "forbidden")
		// paging
		p1 := admin.API.Call(t, "listDeployments", A{"query": A{"limit": 1}})
		Eq(t, p1.At("items").Len(), 1)
		True(t, p1.At("cursor").Truthy())
		p2 := admin.API.Call(t, "listDeployments", A{"query": A{"limit": 1, "cursor": p1.At("cursor")}})
		NotEq(t, p2.At("items.0.deploymentId"), p1.At("items.0.deploymentId"))
	})

	t.Run("drafts are gone (§21 #105): section / site writes 410; a commit validates like the old save + publish", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		api := admin.Real
		id := HelloDeployment(t, admin).ID()
		h := Hello(t)
		d := api.Call(t, "getDeployment", A{"deploymentId": id})
		head := d.At("branch.head.commitId")
		api.Rejects(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": d.At("draftVersion"), "items": h.At("config.variables")}}, "gone", 410)
		api.Rejects(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": d.At("draftVersion"), "site": h.At("config.sites.1")}}, "gone", 410)
		// a save-level error (enum) → 422, nothing saved; a site whose slug mismatches is just another config (validated)
		cfg := DepInput(t, api, id)
		cfg.At("variables.0").Map()["value"] = "purple"
		api.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "bad", "baseCommitId": head, "config": cfg}}, "invalid", 422)
		Eq(t, api.Call(t, "getDeployment", A{"deploymentId": id}).At("branch.head.commitId"), head, "nothing saved on 422")
		// a site label edit commits
		ok := DepInput(t, api, id)
		ok.At("sites").Find("siteSlug", "app").Map()["label"] = "App site!"
		api.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "site label", "baseCommitId": head, "config": ok}})
		Eq(t, api.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.sites").Find("siteSlug", "app").At("label"), "App site!")
	})

	t.Run("§21 #104 diff (working copy vs head commit) + commit + compat proposal + supersede + discard + revisions", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		// the create commit (seq 1) is the head; §21 #105 getDiff = what the head changed (the create commit: everything)
		d0 := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		Eq(t, d0.At("head.seq"), 1)
		Eq(t, d0.At("publishable"), true, d0.At("blockers").String())
		False(t, waSome(d0.At("blockers"), func(b J) bool { return b.At("code").Str() == "V_NO_CHANGE" }))
		True(t, d0.At("changes").Len() > 0)
		Eq(t, d0.At("pending"), L{})
		// the create commit wrote the compat proposal seq 1 (no site enrolled yet → old agents possible)
		props0 := admin.API.Call(t, "listProposals", A{"deploymentId": id})
		Eq(t, props0.At("items").Len(), 1)
		Eq(t, props0.At("items.0.status"), "published")
		Eq(t, props0.At("items.0.sites").Keys(), L{"app", "db"})
		admin.API.Rejects(t, "publish", A{"deploymentId": id, "body": A{"ifRevision": 0, "summary": "x"}}, "gone", 410)
		admin.API.Rejects(t, "resetDraftToHead", A{"deploymentId": id, "body": A{"confirm": true}}, "gone", 410)
		// edit → uncommitted → commit
		cy := DepInput(t, admin.Real, id)
		cy.Map()["variables"] = waHelloVars(t, "yellow")
		h1 := admin.Real.Call(t, "getDeployment", A{"deploymentId": id}).At("branch.head.commitId")
		False(t, admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("branch").Has("uncommitted"), "§21 #105 no server-side count")
		admin.Real.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "  ", "baseCommitId": h1, "config": cy}}, "invalid")
		c2 := admin.Real.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "theme yellow", "baseCommitId": h1, "config": cy}})
		p2 := Publish(t, admin.API, id, 0, "theme yellow")
		Eq(t, p2.Commit.At("commitId"), c2.At("commitId"))
		Eq(t, p2.Commit.At("seq"), 2)
		Eq(t, p2.Commit.At("kind"), "change")
		Eq(t, p2.Commit.At("message"), "theme yellow")
		Eq(t, p2.Proposal.At("revId"), p2.Commit.At("commitId"))
		Eq(t, p2.Revision.At("seq"), 1, "compat revisions count the legacy chain (no head reported yet → seq 1)")
		p2ID := p2.Commit.At("commitId").Str()
		props := admin.API.Call(t, "listProposals", A{"deploymentId": id})
		old := props.At("items").Find("revId", props0.At("items.0.revId"))
		Eq(t, old.At("status"), "superseded")
		Eq(t, old.At("supersededBy"), p2ID)
		rv := admin.API.Call(t, "getRevision", A{"deploymentId": id, "revId": p2ID})
		Eq(t, rv.At("config.secrets.0.value"), model.Mask)
		True(t, rv.At("catalog.images").Key("redis:v1").Truthy())
		Eq(t, rv.At("hash"), p2.Commit.At("hash"))
		// running update: the commit is still written; no compat proposal while an old site applies
		propPath := "deployments/" + id + "/proposals/" + p2ID
		UpdateDoc(t, propPath, A{"status": "applying"})
		w3 := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion"), "items": waHelloVars(t, "blue")}})
		c3 := admin.API.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "blue", "ifRevision": w3.At("draftVersion")}})
		True(t, c3.At("data.compat").IsNil())
		UpdateDoc(t, propPath, A{"status": "ready"})
		// §21 #105 the discard endpoint is gone (pending changes are discarded in the browser)
		admin.Real.Rejects(t, "discardDeploymentWorking", A{"deploymentId": id, "body": A{}}, "gone", 410)
		after := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, after.At("draft.variables").Find("variableSlug", "theme").At("value"), "blue")
		// blockers: a required variable without a value cannot be committed
		w5 := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": after.At("draftVersion"), "items": waHelloVars(t, nil)}})
		e := admin.API.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "bad", "ifRevision": w5.At("draftVersion")}}, "invalid")
		True(t, waSome(e.Details.At("errors"), func(x J) bool { return x.At("code").Str() == "V_VAR_REQUIRED" }))
		// image not ready blocks the commit
		UpdateDoc(t, "services/redis/images/v1", A{"import.status": "copying"})
		func() {
			defer UpdateDoc(t, "services/redis/images/v1", A{"import.status": "ready"})
			d6 := admin.API.Call(t, "getDiff", A{"deploymentId": id})
			True(t, waSome(d6.At("blockers"), func(b J) bool { return b.At("code").Str() == "V_IMAGE_NOT_READY" }))
		}()
		// commits paging
		cs := admin.API.Call(t, "listDeploymentCommits", A{"deploymentId": id, "query": A{"limit": 1}})
		Eq(t, cs.At("items.0.seq"), 3)
		True(t, cs.At("cursor").Truthy())
		r1 := admin.API.Call(t, "listRevisions", A{"deploymentId": id, "query": A{"limit": 1}})
		True(t, r1.At("items").Len() == 1)
	})
}
