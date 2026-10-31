package apitest

import (
	"sort"
	"strings"
	"testing"

	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

func wdTplRef(templateID string) A { return A{"kind": "template", "templateId": templateID} }
func wdDepRef(deploymentID string) A {
	return A{"kind": "deployment", "deploymentId": deploymentID}
}

func wdVar(c J, slug string) J { return c.At("variables").Find("variableSlug", slug) }

// wdEditTemplate is editTemplate(): the commit, or Undefined without a message.
func wdEditTemplate(t testing.TB, admin *Person, templateID string, edit func(c J), message string) J {
	t.Helper()
	tp := admin.API.Call(t, "getTemplate", A{"templateId": templateID})
	c := JOf(A{"variables": tp.At("variables"), "secrets": tp.At("secrets"), "files": tp.At("files"), "sites": tp.At("sites")})
	edit(c)
	body := c.Clone().Map()
	body["ifVersion"] = tp.At("version").V
	w := admin.API.Call(t, "patchTemplate", A{"templateId": templateID, "body": body})
	if message != "" {
		return admin.API.Call(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": message, "ifVersion": w.At("template.version")}})
	}
	return J{Undefined}
}

// wdEditDeployment is editDeployment(): the commit, or Undefined without a message.
func wdEditDeployment(t testing.TB, admin *Person, id string, edit func(c J), message string) J {
	t.Helper()
	d := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
	draft := d.At("draft").Clone()
	edit(draft)
	w1 := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": d.At("draftVersion"), "items": draft.At("variables")}})
	w2 := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "sites", "body": A{"ifRevision": w1.At("draftVersion"), "items": draft.At("sites")}})
	if message != "" {
		return admin.API.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": message, "ifRevision": w2.At("draftVersion")}})
	}
	return J{Undefined}
}

func wdKeys(items J) string { return JOf(items.Pluck("key")).String() }

func wdCommit(t testing.TB, in A) J {
	t.Helper()
	c, err := shared.MakeCommit(toMap(t, JOf(in)))
	NoErr(t, err)
	return JOf(c)
}

// Port of backend/test/28-branches.test.ts.
func TestBranches(t *testing.T) {
	t.Run("create from: template ← template / deployment, deployment ← deployment (secret rules); origin + merge base; first compare is 3-way", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, templateID := h.ID(), h.TemplateID
		// template ← template (head)
		t2 := admin.API.Call(t, "createTemplate", A{"body": A{"label": Uniq("copy"), "from": A{"branch": wdTplRef(templateID), "at": "head"}}})
		Eq(t, t2.At("branch.head.seq"), 1)
		Eq(t, t2.At("branch.head.kind"), "create")
		Match(t, t2.At("branch.head.message"), `^created from .+@\d+$`)
		Eq(t, t2.At("branch.origin.branch.kind"), "template")
		Eq(t, t2.At("sites").Len(), Hello(t).At("config.sites").Len())
		mb := Doc(t, "templates/"+t2.At("templateId").Str()+"/mergeBases/template:"+templateID)
		Eq(t, mb.At("sourceKey"), "template:"+templateID)
		// compare with the origin right away: nothing differs, base = record
		p0 := admin.API.Call(t, "compareBranches", A{"body": A{"source": A{"branch": wdTplRef(templateID), "at": "head"}, "target": wdTplRef(t2.At("templateId").Str())}})
		Eq(t, p0.At("base.kind"), "record")
		Eq(t, p0.At("items").Len(), 0)
		// template ← deployment: secret values never reach a template; generate rules stay
		t3 := admin.API.Call(t, "createTemplate", A{"body": A{"label": Uniq("fromdep"), "from": A{"branch": wdDepRef(id), "at": "head"}, "message": "snapshot of the deployment"}})
		Eq(t, t3.At("branch.head.message"), "snapshot of the deployment")
		Eq(t, t3.At("secrets.0.value"), nil)
		True(t, t3.At("secrets.0.generate").Truthy())
		tc := admin.API.Call(t, "getTemplateCommit", A{"templateId": t3.At("templateId"), "commitId": t3.At("branch.head.commitId")})
		Eq(t, tc.At("commit.source.branch.kind"), "deployment")
		// deployment ← deployment: generated secrets get NEW values; copySecretValues needs secrets.read and copies only non-generate ones
		wdEditDeployment(t, admin, id, func(c J) { wdVar(c, "theme").Map()["value"] = "blue" }, "blue")
		src := admin.API.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "api-token"})
		d2 := admin.API.Call(t, "createDeployment", A{"body": A{"label": Uniq("clone"), "templateId": nil, "tagSlugs": L{}, "managerAccountUids": L{}, "from": A{"branch": wdDepRef(id), "at": "head"}}})
		d2ID := d2.At("deployment.deploymentId").Str()
		Eq(t, wdVar(d2.At("deployment.draft"), "theme").At("value"), "blue")
		v2 := admin.API.Call(t, "revealSecret", A{"deploymentId": d2ID, "secretSlug": "api-token"})
		NotEq(t, v2.At("value"), src.At("value"), "generate → a new value")
		Eq(t, d2.At("deployment.branch.origin.branch.kind"), "deployment")
		Eq(t, d2.At("deployment.srcTemplate"), nil)
		// a user without deployment.secrets.read on the source cannot copy values
		ops := NewPerson(t, L{
			A{"resource": "deployments", "actions": L{"read", "write"}, "scope": A{"kind": "all"}},
			A{"resource": "deployment.config", "actions": L{"read", "write", "publish"}, "scope": A{"kind": "all"}},
		})
		ops.API.Rejects(t, "createDeployment", A{"body": A{"label": Uniq("x"), "templateId": nil, "tagSlugs": L{}, "managerAccountUids": L{}, "from": A{"branch": wdDepRef(id), "at": "head"}, "copySecretValues": true}}, "forbidden", 403)
		// deployment ← deployment compare: secret VALUE items exist, masked
		wdEditDeployment(t, admin, d2ID, func(c J) { wdVar(c, "greeting").Map()["value"] = "other" }, "greeting")
		cmp := A{"body": A{"source": A{"branch": wdDepRef(id), "at": "head"}, "target": wdDepRef(d2ID)}}
		pc := admin.API.Call(t, "compareBranches", cmp)
		sv := pc.At("items").Find("key", "secrets/api-token/value")
		True(t, sv.Truthy(), wdKeys(pc.At("items")))
		Eq(t, sv.At("source.value"), "set")
		Eq(t, pc.At("items").Find("key", "variables/greeting/value").At("kind"), "ours")
		// the ops person sees the secret value item locked (no deployment.secrets.*)
		pl := ops.API.Call(t, "compareBranches", cmp)
		Match(t, pl.At("items").Find("key", "secrets/api-token/value").At("locked").Str(), `secrets`)
		ops.API.Rejects(t, "merge", A{"body": A{"source": A{"branch": wdDepRef(id), "at": "head"}, "target": wdDepRef(d2ID), "targetHead": pl.At("target.head.commitId"), "sourceCommit": pl.At("source.commit.commitId"), "choices": L{A{"key": "secrets/api-token/value", "take": "source"}}, "message": "copy the token"}}, "forbidden", 403)
		// admin takes the value: the target now holds the source plain value
		m := admin.API.Call(t, "merge", A{"body": A{"source": A{"branch": wdDepRef(id), "at": "head"}, "target": wdDepRef(d2ID), "targetHead": pc.At("target.head.commitId"), "sourceCommit": pc.At("source.commit.commitId"), "choices": L{A{"key": "secrets/api-token/value", "take": "source"}}, "message": "copy the token"}})
		Eq(t, m.At("kind"), "merge", "the source has no changes since the clone: taking the item is a (trivial) merge")
		Eq(t, admin.API.Call(t, "revealSecret", A{"deploymentId": d2ID, "secretSlug": "api-token"}).At("value"), src.At("value"))
		mbs := Doc(t, "deployments/"+d2ID+"/mergeBases/deployment:"+id)
		NotContains(t, textOf(mbs.At("_base")), src.At("value").Str(), "merge bases never store a plain secret value")
	})

	t.Run("template commits (§21 #105 direct): message required, V_NO_CHANGE, stale base, history, commit detail; detachment proof", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, templateID := h.ID(), h.TemplateID
		api := admin.Real
		depBefore := Doc(t, "deployments/"+id).At("_draft").String()
		tp := api.Call(t, "getTemplate", A{"templateId": templateID})
		base := tp.At("branch.head.commitId")
		n0 := api.Call(t, "listTemplateCommits", A{"templateId": templateID}).At("items").Len()
		api.Rejects(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "x", "baseCommitId": base, "config": TplInput(t, api, templateID)}}, "invalid", 422) // nothing to commit
		red := TplInput(t, api, templateID)
		wdVar(red, "theme").Map()["value"] = "red"
		Eq(t, tp.At("branch").Has("uncommitted"), false, "§21 #105: no server-side uncommitted count")
		api.Rejects(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "", "baseCommitId": base, "config": red}}, "invalid", 422)
		api.Rejects(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": strings.Repeat("x", 201), "baseCommitId": base, "config": red}}, "invalid", 422)
		api.Rejects(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "red", "baseCommitId": ulid.New(), "config": red}}, "stale", 409)
		c := api.Call(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "theme red\n\nbody text", "baseCommitId": base, "config": red}})
		Eq(t, c.At("kind"), "change")
		Eq(t, c.At("stats.changed"), 1)
		Eq(t, c.At("seq"), n0+1)
		hist := api.Call(t, "listTemplateCommits", A{"templateId": templateID})
		Eq(t, hist.At("items.0.commitId"), c.At("commitId"))
		det := api.Call(t, "getTemplateCommit", A{"templateId": templateID, "commitId": c.At("commitId")})
		Eq(t, det.At("changes").Pluck("key"), L{"variables/theme/value"})
		Eq(t, wdVar(api.Call(t, "getTemplate", A{"templateId": templateID}), "theme").At("value"), "red")
		// discard is gone (pending changes live in the browser)
		api.Rejects(t, "discardTemplateWorking", A{"templateId": templateID, "body": A{}}, "gone", 410)
		// detachment: the deployment draft is untouched by template commits
		Eq(t, Doc(t, "deployments/"+id).At("_draft").String(), depBefore)
		// template audit entries
		au := api.Call(t, "listAuditLog", A{"query": A{"target": "templates/" + templateID, "action": "template.committed"}})
		True(t, au.At("items").Find("outcome", "ok").Exists())
	})

	t.Run("compare + merge: conflict / theirs removal / target-only removal / skipped; 409 stale / source-changed; revert + reset", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, templateID := h.ID(), h.TemplateID
		// template: theme yellow (conflict with the deployment's blue), greeting changed (clean), removes the first app env
		wdEditTemplate(t, admin, templateID, func(c J) {
			wdVar(c, "theme").Map()["value"] = "yellow"
			wdVar(c, "greeting").Map()["value"] = "Hello from the template"
			c0 := wdSiteOf(c, "app").At("config.containers.0")
			if env := c0.At("env").V.([]any); len(env) > 0 {
				c0.Map()["env"] = env[1:]
			}
		}, "template changes")
		wdEditDeployment(t, admin, id, func(c J) {
			wdVar(c, "theme").Map()["value"] = "blue"
			c0 := wdSiteOf(c, "db").At("config.containers.0")
			c0.Map()["env"] = append(c0.At("env").V.([]any), A{"name": "ONLY_HERE", "value": "1", "secretRef": nil})
		}, "deployment changes")
		src := A{"branch": wdTplRef(templateID), "at": "head"}
		plan := admin.API.Call(t, "compareBranches", A{"body": A{"source": src, "target": wdDepRef(id)}})
		items := plan.At("items")
		Eq(t, items.Find("key", "variables/theme/value").At("kind"), "conflict")
		Eq(t, items.Find("key", "variables/greeting/value").At("kind"), "theirs")
		only := "sites/db/containers/redis/env/ONLY_HERE"
		Eq(t, items.Find("key", only).At("kind"), "ours")
		Eq(t, items.Find("key", only).At("effect"), "remove")
		appEnv0 := wdSiteOf(Hello(t).At("config"), "app").At("config.containers.0.env.0")
		if appEnv0.Truthy() {
			Eq(t, items.FindF(func(i J) bool { return strings.HasSuffix(i.At("key").Str(), "/env/"+appEnv0.At("name").Str()) }).At("effect"), "remove")
		}
		req := func(extra A) A {
			return wdWith(A{"source": src, "target": wdDepRef(id), "targetHead": plan.At("target.head.commitId"), "sourceCommit": plan.At("source.commit.commitId"), "message": "take greeting"}, extra)
		}
		// unresolved conflict → 422
		un := admin.API.Rejects(t, "merge", A{"body": req(A{"choices": L{A{"key": "variables/greeting/value", "take": "source"}}})}, "invalid", 422)
		Match(t, un.Details.String(), `V_MERGE_UNRESOLVED`)
		// stale target head
		admin.API.Rejects(t, "merge", A{"body": req(A{"targetHead": ulid.New(), "choices": L{}})}, "stale", 409)
		// §21 #105: no server working copy → no dirty-working-copy refusal (30-commit-direct covers merges next to browser changes)
		// cherry-pick: greeting taken, theme kept (target), ONLY_HERE kept → kind cherry-pick? every source change = greeting + theme + app env removal
		choices := L{A{"key": "variables/greeting/value", "take": "source"}, A{"key": "variables/theme/value", "take": "target"}, A{"key": only, "take": "source"}}
		pv := admin.API.Call(t, "previewMerge", A{"body": req(A{"choices": choices})})
		Eq(t, pv.At("validation.errors").Len(), 0, pv.At("validation.errors").String())
		m := admin.API.Call(t, "merge", A{"body": req(A{"choices": choices})})
		Eq(t, m.At("kind"), "cherry-pick")
		Eq(t, m.At("source.branch.kind"), "template")
		after := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, wdVar(after.At("draft"), "greeting").At("value"), "Hello from the template")
		Eq(t, wdVar(after.At("draft"), "theme").At("value"), "blue")
		False(t, wdSiteOf(after.At("draft"), "db").At("config.containers.0.env").Find("name", "ONLY_HERE").Exists(), "removed from target")
		// the not-taken template changes are "skipped" next time; a source change of a moved head → source-changed
		plan2 := admin.API.Call(t, "compareBranches", A{"body": A{"source": src, "target": wdDepRef(id)}})
		// the not-taken template change is no longer offered as a source change (base moved to the source); shared/branch.ts
		// classifies it 'ours' today — CONTRACT REQUEST (report): mark 'ours' items with a matching skip record as 'skipped' (#94 rule)
		th := plan2.At("items").Find("key", "variables/theme/value")
		True(t, th.At("kind").Str() == "skipped" || th.At("kind").Str() == "ours", th.At("kind").String())
		rec := Doc(t, "deployments/"+id+"/mergeBases/template:"+templateID)
		True(t, ParseJ([]byte(rec.At("_skipped").Str())).Find("key", "variables/theme/value").Exists(), "skip recorded")
		wdEditTemplate(t, admin, templateID, func(c J) { wdVar(c, "app-host").Map()["value"] = "10.1.1.1" }, "app host")
		admin.API.Rejects(t, "merge", A{"body": req(A{"targetHead": m.At("commitId"), "sourceCommit": plan2.At("source.commit.commitId"), "choices": L{}})}, "conflict", 409)
		// revert the merge, then reset to the create commit
		rv := admin.API.Call(t, "revertCommit", A{"body": A{"branch": wdDepRef(id), "commitId": m.At("commitId"), "message": `Revert "take greeting"`, "ifHead": m.At("commitId")}})
		Eq(t, rv.At("kind"), "revert")
		Eq(t, rv.At("revertOf.commitId"), m.At("commitId"))
		ar := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, wdVar(ar.At("draft"), "greeting").At("value"), wdVar(Hello(t).At("config"), "greeting").At("value"))
		all := admin.API.Call(t, "listDeploymentCommits", A{"deploymentId": id})
		first := all.At("items.-1")
		admin.API.Rejects(t, "revertCommit", A{"body": A{"branch": wdDepRef(id), "commitId": first.At("commitId"), "message": "x", "ifHead": rv.At("commitId")}}, "invalid", 422)
		rs := admin.API.Call(t, "resetToCommit", A{"body": A{"branch": wdDepRef(id), "commitId": first.At("commitId"), "message": "back to the start", "ifHead": rv.At("commitId")}})
		Eq(t, rs.At("kind"), "reset")
		Eq(t, rs.At("hash"), first.At("hash"))
		acts := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "branch."}})
		for _, tp := range []string{"branch.committed", "branch.cherry-picked", "branch.reverted", "branch.reset"} {
			True(t, acts.At("items").Find("type", tp).Exists(), tp)
		}
		au := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "deployments/" + id, "action": "deployment.merged"}})
		True(t, au.At("items").FindF(func(x J) bool { return x.At("outcome").Str() == "ok" && x.At("data.kind").Str() == "cherry-pick" }).Exists())
		// read-only person cannot merge
		reader := NewPerson(t, L{A{"resource": "deployments", "actions": L{"read"}, "scope": A{"kind": "all"}}, A{"resource": "deployment.config", "actions": L{"read"}, "scope": A{"kind": "all"}}, A{"resource": "templates", "actions": L{"read"}, "scope": A{"kind": "all"}}})
		reader.API.Call(t, "compareBranches", A{"body": A{"source": src, "target": wdDepRef(id)}})
		reader.API.Rejects(t, "merge", A{"body": req(A{"targetHead": rs.At("commitId"), "sourceCommit": nil, "choices": L{}})}, "forbidden", 403)
		// diffOnly compare of two commits
		dc := admin.API.Call(t, "compareBranches", A{"body": A{"source": A{"branch": wdDepRef(id), "at": A{"commitId": m.At("commitId")}}, "target": A{"branch": wdDepRef(id), "at": A{"commitId": first.At("commitId")}}, "diffOnly": true}})
		Eq(t, dc.At("diffOnly"), true)
		True(t, dc.At("items").Len() > 0)
	})

	t.Run("agent branches: compat only for old sites; push verify + fork; views; cloud picks from the site; 0.14 cloud view; removed routes 410", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id := h.ID()
		app := EnrollSite(t, admin, id, "app")
		EnrollSite(t, admin, id, "db")
		UpdateDoc(t, "deployments/"+id+"/agents/app", A{"agentVersion": "0.14.0-test"})
		// db is still old → commits keep writing compat proposals
		c1 := wdEditDeployment(t, admin, id, func(c J) { wdVar(c, "theme").Map()["value"] = "red" }, "red")
		True(t, c1.At("data.compat").Truthy(), "db < 0.14 → compat proposal")
		UpdateDoc(t, "deployments/"+id+"/agents/db", A{"agentVersion": "0.14.1"})
		c2 := wdEditDeployment(t, admin, id, func(c J) { wdVar(c, "theme").Map()["value"] = "blue" }, "blue")
		True(t, c2.At("data.compat").IsNil(), "every site ≥ 0.14 → no compat proposal")
		// the cloud view carries the head; agents fetch it with plain secrets
		view := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
		Eq(t, view.At("branch.head.commitId"), c2.At("commitId"))
		True(t, view.At("branch.head.catalogHash").Truthy())
		got := app.API.Call(t, "getAgentCommit", A{"commitId": c2.At("commitId")})
		Match(t, got.At("commit.config.secrets.0.value"), `^[A-Za-z0-9]{24}$`)
		metas := app.API.Call(t, "listAgentCloudCommits", A{"query": A{"limit": 2}})
		Eq(t, metas.At("items").Pluck("commitId"), L{c2.At("commitId"), c1.At("commitId")})
		// the site merges the cloud head (seq 1 on its branch), then commits a hotfix (seq 2)
		own := A{"kind": "agent", "deploymentId": id, "siteSlug": "app"}
		author := A{"kind": "person", "email": "ops@site.test", "uid": nil, "name": nil, "siteSlug": "app"}
		a1 := wdCommit(t, A{"commitId": ulid.New(), "branch": own, "parent": nil, "kind": "merge", "message": "merge cloud seq " + Itoa(c2.At("seq").Int()), "author": author, "createdAt": Now(), "config": got.At("commit.config"), "catalog": got.At("commit.catalog"), "source": A{"branch": wdDepRef(id), "commitId": c2.At("commitId"), "seq": c2.At("seq"), "label": h.Dep.At("label"), "picks": "all", "skipped": 0}, "scope": "app"})
		hot := got.At("commit.config").Clone()
		hc0 := wdSiteOf(hot, "app").At("config.containers.0")
		hc0.Map()["env"] = append(hc0.At("env").V.([]any), A{"name": "HOTFIX", "value": "on", "secretRef": nil})
		a2 := wdCommit(t, A{"commitId": ulid.New(), "branch": own, "parent": a1, "kind": "drift", "message": "hotfix made on the server", "author": author, "createdAt": Now(), "config": hot, "catalog": got.At("commit.catalog"), "scope": "app"})
		over := func(c J, m A) J {
			o := c.Clone()
			for k, v := range m {
				o.Map()[k] = Plain(v)
			}
			return o
		}
		app.API.Rejects(t, "pushAgentBranch", A{"body": A{"commits": L{over(a1, A{"hash": "sha256:" + strings.Repeat("0", 64)})}}}, "invalid", 422)
		app.API.Rejects(t, "pushAgentBranch", A{"body": A{"commits": L{over(a1, A{"branch": A{"kind": "agent", "deploymentId": id, "siteSlug": "db"}})}}}, "invalid", 422)
		app.API.Rejects(t, "pushAgentBranch", A{"body": A{"commits": L{a2}}}, "invalid", 422) // parent unknown
		pushed := app.API.Call(t, "pushAgentBranch", A{"body": A{"commits": L{a2, a1}}})
		sorted := func(l []any) []string {
			out := []string{}
			for _, x := range l {
				out = append(out, x.(string))
			}
			sort.Strings(out)
			return out
		}
		Eq(t, sorted(pushed.At("stored").V.([]any)), sorted(L{a1.At("commitId").V, a2.At("commitId").V}))
		Eq(t, pushed.At("head.commitId"), a2.At("commitId"))
		Eq(t, app.API.Call(t, "pushAgentBranch", A{"body": A{"commits": L{a1, a2}}}).At("stored"), L{}, "idempotent")
		fork := over(a2, A{"commitId": ulid.New(), "message": "fork"})
		app.API.Rejects(t, "pushAgentBranch", A{"body": A{"commits": L{fork}}}, "conflict", 409)
		// heartbeat.branch → uncommitted drift
		app.API.Call(t, "heartbeat", A{"body": A{"siteSlug": "app", "agentVersion": "0.14.0-test", "headRevId": nil, "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0, "memPct": 0, "diskPct": 0}, "edge": nil, "keyVersion": 1, "advertiseHost": "127.0.0.1", "agentPort": 7080, "ip": "127.0.0.1", "hostname": "h", "branch": A{"head": nil, "mergedFrom": A{"commitId": c2.At("commitId"), "seq": c2.At("seq")}, "uncommitted": A{"working": 1, "drift": 2, "notes": 0, "scannedAt": Now()}}}})
		views := admin.API.Call(t, "listAgentBranches", A{"deploymentId": id})
		av := views.At("items").Find("siteSlug", "app")
		Eq(t, av.At("head.commitId"), a2.At("commitId"))
		Eq(t, av.At("behind"), 0)
		Eq(t, av.At("differs"), 1)
		Eq(t, av.At("ahead"), 1)
		Eq(t, av.At("uncommitted.drift"), 2)
		Eq(t, av.At("needsAgent014"), false)
		hist := admin.API.Call(t, "listAgentBranchCommits", A{"deploymentId": id, "siteSlug": "app"})
		Eq(t, hist.At("items").Pluck("seq"), L{2, 1})
		det := admin.API.Call(t, "getAgentBranchCommit", A{"deploymentId": id, "siteSlug": "app", "commitId": a2.At("commitId")})
		Eq(t, det.At("commit.config.secrets.0.value"), shared.MASK)
		Eq(t, det.At("changes").Pluck("key"), L{"sites/app/containers/" + hc0.At("containerSlug").Str() + "/env/HOTFIX"})
		// the cloud cherry-picks the hotfix into the deployment (base = the merged deployment commit)
		plan := admin.API.Call(t, "compareBranches", A{"body": A{"source": A{"branch": own, "at": "head"}, "target": wdDepRef(id)}})
		Eq(t, plan.At("base.kind"), "origin")
		Eq(t, plan.At("items").Len(), 1)
		Eq(t, plan.At("items.0.kind"), "theirs")
		m := admin.API.Call(t, "merge", A{"body": A{"source": A{"branch": own, "at": "head"}, "target": wdDepRef(id), "targetHead": c2.At("commitId"), "sourceCommit": a2.At("commitId"), "choices": L{A{"key": plan.At("items.0.key"), "take": "source"}}, "message": "take the hotfix from app"}})
		Eq(t, m.At("kind"), "merge")
		Eq(t, m.At("source.branch.kind"), "agent")
		// agent branches are never written by the cloud
		admin.API.Rejects(t, "merge", A{"body": A{"source": A{"branch": wdDepRef(id), "at": "head"}, "target": own, "targetHead": a2.At("commitId"), "sourceCommit": nil, "choices": L{}, "message": "x"}}, "forbidden", 403)
		// a template made from the agent branch
		tp := admin.API.Call(t, "createTemplate", A{"body": A{"label": Uniq("from-site"), "from": A{"branch": own, "at": "head"}}})
		Eq(t, tp.At("branch.origin.branch.kind"), "agent")
		// removed routes
		g := admin.API.Rejects(t, "publish", A{"deploymentId": id, "body": A{"ifRevision": 0, "summary": "x"}}, "gone", 410)
		True(t, g.Details.At("hint").Truthy())
		admin.API.Rejects(t, "getTemplateSyncStatus", A{"deploymentId": id}, "gone", 410)
		admin.API.Rejects(t, "getTemplateSyncPlan", A{"deploymentId": id}, "gone", 410)
		admin.API.Rejects(t, "unlinkTemplate", A{"deploymentId": id, "body": A{"confirm": true}}, "gone", 410)
		r := Raw(t, "POST", "/v1/deployments/"+id+"/template-sync/apply", Opt{Headers: map[string]string{"Authorization": admin.Bearer, "Content-Type": "application/json"}, Body: []byte("{}")})
		Eq(t, r.Status, 410)
	})

	t.Run("merge into a deployment: a new required secret gets its value in the merge (secretValues); never on a template", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, templateID := h.ID(), h.TemplateID
		wdEditTemplate(t, admin, templateID, func(c J) {
			c.Map()["secrets"] = append(c.At("secrets").V.([]any), A{"secretSlug": "maps-key", "label": "Maps key", "description": "", "required": true, "generate": nil, "value": nil, "rotatedAt": nil})
		}, "add maps-key")
		src := A{"branch": wdTplRef(templateID), "at": "head"}
		plan := admin.API.Call(t, "compareBranches", A{"body": A{"source": src, "target": wdDepRef(id)}})
		choices := L{}
		for _, i := range plan.At("items").Filter(func(i J) bool { return strings.HasPrefix(i.At("key").Str(), "secrets/maps-key") }) {
			choices = append(choices, A{"key": i.At("key"), "take": "source"})
		}
		True(t, len(choices) > 0, wdKeys(plan.At("items")))
		req := A{"source": src, "target": wdDepRef(id), "targetHead": plan.At("target.head.commitId"), "sourceCommit": plan.At("source.commit.commitId"), "choices": choices, "message": "add maps-key"}
		pv0 := admin.API.Call(t, "previewMerge", A{"body": req})
		Eq(t, pv0.At("missing.secrets").Pluck("secretSlug"), L{"maps-key"})
		True(t, pv0.At("validation.errors").Find("code", "V_SECRET_REQUIRED").Exists())
		admin.API.Rejects(t, "merge", A{"body": req}, "invalid", 422)
		withValue := wdWith(req, A{"secretValues": A{"maps-key": "abc123"}})
		pv1 := admin.API.Call(t, "previewMerge", A{"body": withValue})
		Eq(t, pv1.At("validation.errors").Len(), 0, pv1.At("validation.errors").String())
		Eq(t, pv1.At("missing.secrets").Len(), 0)
		NotContains(t, pv1.String(), "abc123", "the preview never echoes the value")
		m := admin.API.Call(t, "merge", A{"body": withValue})
		NotContains(t, m.String(), "abc123", "the merge response masks the value")
		Eq(t, m.At("data.secretValues.set"), L{"maps-key"})
		d := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, d.At("draft.secrets").Find("secretSlug", "maps-key").At("isSet"), true)
		// a template target holds no values
		admin.API.Rejects(t, "previewMerge", A{"body": A{"source": A{"branch": wdDepRef(id), "at": "head"}, "target": wdTplRef(templateID), "targetHead": nil, "sourceCommit": nil, "choices": L{}, "secretValues": A{"maps-key": "x"}}}, "invalid", 422)
	})
}
