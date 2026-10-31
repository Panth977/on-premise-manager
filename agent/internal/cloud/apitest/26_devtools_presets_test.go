package apitest

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/shared"
)

func wdSiteOf(c J, s string) J { return c.At("sites").Find("siteSlug", s) }

// wdNewPreset is newDevToolPreset(kind) of @opm/shared (model.NewDevToolPreset is the agent's, with another name and shape).
func wdNewPreset(kind string) A {
	label := map[string]string{"http": "HTTP client", "postgres": "PostgreSQL", "redis": "Redis", "mqtt": "MQTT"}[kind]
	var username, database any
	if kind == "postgres" {
		username = "postgres"
	}
	if kind == "redis" {
		database = "0"
	}
	return A{"presetSlug": kind, "name": label, "description": "", "kind": kind, "readOnly": false,
		"target": A{"kind": "container", "containerSlug": "", "portName": ""},
		"auth":   A{"username": username, "password": A{"kind": "none"}, "database": database, "clientId": nil, "httpScheme": "none", "token": A{"kind": "none"}, "apiKeyHeader": nil},
		"options": A{"tls": false, "tlsInsecure": false, "tlsServerName": nil, "sslMode": nil, "timeoutSec": nil, "maxRows": nil, "consistency": nil, "scheme": nil, "basePath": nil,
			"followRedirects": nil, "defaultHeaders": L{}, "cleanSession": nil, "keepAliveSec": nil}}
}

func wdRedis(slug, name string) A {
	p := wdNewPreset("redis")
	p["presetSlug"], p["name"] = slug, name
	p["target"] = A{"kind": "container", "containerSlug": "redis", "portName": "tcp"}
	p["auth"].(A)["password"] = A{"kind": "expr", "expr": "{{ secret.api-token }}"}
	return p
}

func wdMqtt(slug string) A {
	p := wdNewPreset("mqtt")
	p["presetSlug"] = slug
	p["target"] = A{"kind": "host", "host": "broker.local", "port": 1883}
	return p
}

func wdPutSite(t testing.TB, admin *Person, id, slug string, edit func(s J)) (J, *APIError) {
	t.Helper()
	d := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
	s := wdSiteOf(d.At("draft"), slug).Clone()
	edit(s)
	return admin.API.Try("putDraftSite", A{"deploymentId": id, "siteSlug": slug, "body": A{"ifRevision": d.At("draftVersion"), "site": s}})
}

func wdMustPutSite(t testing.TB, admin *Person, id, slug string, edit func(s J)) {
	t.Helper()
	if _, e := wdPutSite(t, admin, id, slug, edit); e != nil {
		t.Fatalf("putDraftSite: %v", e)
	}
}

func wdPatchTpl(t testing.TB, admin *Person, templateID string, edit func(c J)) J {
	t.Helper()
	tp := admin.API.Call(t, "getTemplate", A{"templateId": templateID})
	c := JOf(A{"variables": tp.At("variables"), "secrets": tp.At("secrets"), "files": tp.At("files"), "sites": tp.At("sites")})
	edit(c)
	body := c.Clone().Map()
	body["ifVersion"] = tp.At("version").V
	return admin.API.Call(t, "patchTemplate", A{"templateId": templateID, "body": body}).At("template")
}

func wdRawDraft(t testing.TB, id string) J {
	t.Helper()
	return ParseJ([]byte(Doc(t, "deployments/"+id).At("_draft").Str()))
}

func wdCodes(e *APIError) []string {
	out := []string{}
	for _, x := range e.Details.At("errors").Arr() {
		out = append(out, x.At("code").Str())
	}
	return out
}

func wdHasCode(t testing.TB, e *APIError, code string) {
	t.Helper()
	for _, c := range wdCodes(e) {
		if c == code {
			return
		}
	}
	t.Fatalf("%s: %s", code, e.Details)
}

func wdJcs(t testing.TB, v J) string {
	t.Helper()
	h, err := cloud.JcsHash(v.V)
	NoErr(t, err)
	return h
}

func wdCanon(t testing.TB, v J) string {
	t.Helper()
	s, err := canonical.Canonicalize(v.V)
	NoErr(t, err)
	return s
}

func wdKindPath(items []J) []string {
	out := []string{}
	for _, c := range items {
		out = append(out, c.At("kind").Str()+":"+c.At("path").Str())
	}
	return out
}

func wdDevToolChanges(list J) []J {
	return list.Filter(func(c J) bool { return c.At("section").Str() == "devTools" })
}

// wdVerify is (await verifySchema(adb)).filter(keep).
func wdVerify(t testing.TB, keep func(string) bool) []string {
	t.Helper()
	lines, err := core.VerifySchema(DB, core.VerifyOptions{})
	NoErr(t, err)
	out := []string{}
	for _, l := range lines {
		if keep(l) {
			out = append(out, l)
		}
	}
	return out
}

// wdMakeActivity is makeActivity().
func wdMakeActivity(t testing.TB, deploymentID, siteSlug string, seq int, prevHash any, typ string, data any) J {
	t.Helper()
	base := JOf(A{"activityId": fmt.Sprintf("%s:%d", siteSlug, seq), "deploymentId": deploymentID, "siteSlug": siteSlug, "seq": seq, "ts": ISO(time.Now().Add(time.Duration(seq) * time.Millisecond)), "actor": "ops@example.com", "origin": "agent", "type": typ, "summary": fmt.Sprintf("%s %d", typ, seq), "data": data, "prevHash": prevHash})
	in, err := shared.ActivityHashInput(toMap(t, base))
	NoErr(t, err)
	base.Map()["hash"] = "sha256:" + Sha256Hex(in)
	base.Map()["uploadedVia"], base.Map()["uploadedAt"] = nil, nil
	return base
}

// Port of backend/test/26-devtools-presets.test.ts.
func TestDevtoolsPresets(t *testing.T) {
	t.Run("draft PUT: presets validated (422 V_DEVTOOL_*), normalized, [] omitted; diff section; publish carries them + hash", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		// the hello draft has no presets: no devTools key anywhere
		for _, s := range wdRawDraft(t, id).At("sites").Arr() {
			False(t, s.At("config").Has("devTools"), s.At("siteSlug").Str())
		}

		// invalid presets → 422 with the codes, nothing stored
		v0 := admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion")
		_, e := wdPutSite(t, admin, id, "db", func(s J) {
			lit := wdRedis("lit", "Cache")
			lit["auth"].(A)["password"] = A{"kind": "literal", "value": ""} // §21 #103: a static value is allowed; an EMPTY one is not
			port := wdRedis("port", "Cache")
			port["target"] = A{"kind": "container", "containerSlug": "redis", "portName": "http"}
			gone := wdRedis("gone", "Cache")
			gone["target"] = A{"kind": "container", "containerSlug": "nope", "portName": "tcp"}
			sec := wdRedis("sec", "Cache")
			sec["auth"].(A)["password"] = A{"kind": "expr", "expr": "{{ secret.missing }}"}
			opt := wdRedis("Bad_Slug", "Cache")
			opt["options"].(A)["timeoutSec"] = 0
			s.At("config").Map()["devTools"] = L{lit, port, gone, sec, opt, wdRedis("lit", "Cache")}
		})
		bad := Rejected(t, e, "invalid", 422)
		for _, code := range []string{"V_DEVTOOL_AUTH", "V_DEVTOOL_PORT", "V_DEVTOOL_CONTAINER", "V_DEVTOOL_SECRET", "V_DEVTOOL_SLUG", "V_DEVTOOL_OPTION", "V_DEVTOOL_DUP"} {
			wdHasCode(t, bad, code)
		}
		Eq(t, admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion"), v0, "nothing stored")
		// the sites section PUT validates too
		d0 := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		sitesBad := d0.At("draft.sites").Clone()
		pg := wdNewPreset("postgres")
		pg["target"] = A{"kind": "host", "host": "pg", "port": 5432}
		pg["auth"].(A)["username"] = nil
		sitesBad.Find("siteSlug", "app").At("config").Map()["devTools"] = L{pg}
		sb := admin.API.Rejects(t, "putDraftSection", A{"deploymentId": id, "section": "sites", "body": A{"ifRevision": d0.At("draftVersion"), "items": sitesBad}}, "invalid", 422)
		wdHasCode(t, sb, "V_DEVTOOL_AUTH")

		// valid preset (missing optional keys + an unknown key) → stored in the normalized shape
		wdMustPutSite(t, admin, id, "db", func(s J) {
			p := wdRedis("cache", "Cache")
			delete(p, "options")
			p["junk"] = 1
			s.At("config").Map()["devTools"] = L{p}
		})
		stored := wdSiteOf(wdRawDraft(t, id), "db").At("config.devTools")
		Eq(t, stored.Len(), 1)
		False(t, stored.At("0").Has("junk"))
		Eq(t, stored.At("0.options"), wdNewPreset("redis")["options"])
		Eq(t, stored.At("0.auth.password"), A{"kind": "expr", "expr": "{{ secret.api-token }}"})
		view := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		Eq(t, wdSiteOf(view.At("draft"), "db").At("config.devTools"), stored)
		// an empty list on another site is never written
		wdMustPutSite(t, admin, id, "app", func(s J) { s.At("config").Map()["devTools"] = L{} })
		False(t, wdSiteOf(wdRawDraft(t, id), "app").At("config").Has("devTools"))

		// diff: section devTools
		diff := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		Eq(t, wdKindPath(wdDevToolChanges(diff.At("changes"))), []string{"added:db/devtools/cache"})
		Eq(t, diff.At("publishable"), true, diff.At("blockers").String())

		// publish → the revision carries the presets; hash = sha256(JCS(config)); app has no devTools key
		p := Publish(t, admin.API, id, admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion"), "presets")
		revID := p.Revision.At("revId")
		rv := admin.API.Call(t, "getRevision", A{"deploymentId": id, "revId": revID})
		Eq(t, wdSiteOf(rv.At("config"), "db").At("config.devTools"), stored)
		rawRev := Doc(t, "deployments/"+id+"/revisions/"+revID.Str())
		revCfg := ParseJ([]byte(rawRev.At("_config").Str()))
		Eq(t, rawRev.At("hash"), wdJcs(t, revCfg))
		Eq(t, p.Revision.At("hash"), wdJcs(t, revCfg))
		False(t, wdSiteOf(revCfg, "app").At("config").Has("devTools"))
		// without the db presets the config hashes exactly like a config that never had the key (agents < 0.11)
		noPresets := revCfg.Clone()
		delete(wdSiteOf(noPresets, "db").At("config").Map(), "devTools")
		NotEq(t, wdJcs(t, noPresets), rawRev.At("hash"))
		NotContains(t, wdCanon(t, noPresets), "devTools")

		// removing the last preset → key absent again; diff shows removed relative to head
		UpdateDoc(t, "deployments/"+id, A{"head": A{"revId": revID, "seq": p.Revision.At("seq"), "hash": p.Revision.At("hash"), "reportedAt": Now(), "reportedBy": "db"}})
		wdMustPutSite(t, admin, id, "db", func(s J) { delete(s.At("config").Map(), "devTools") })
		False(t, wdSiteOf(wdRawDraft(t, id), "db").At("config").Has("devTools"))
		diff2 := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		Eq(t, wdKindPath(wdDevToolChanges(diff2.At("changes"))), []string{"removed:db/devtools/cache"})
		// a deployment without presets publishes a revision without any devTools key
		p2 := Publish(t, admin.API, id, admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion"), "no presets")
		NotContains(t, Doc(t, "deployments/"+id+"/revisions/"+p2.Revision.At("revId").Str()).At("_config").Str(), "devTools")

		// verify-schema: these documents are clean
		mine := func(l string) bool { return strings.HasPrefix(l, "deployments/"+id) }
		Eq(t, wdVerify(t, mine), []string{})
	})

	t.Run("templates: presets saved + validated in template mode; [] omitted; Template sync plan / apply incl. a conflict", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, templateID := h.ID(), h.TemplateID
		// template save validates (template mode: still an error — presets are save level)
		t0 := admin.API.Call(t, "getTemplate", A{"templateId": templateID})
		tc := JOf(A{"variables": t0.At("variables"), "secrets": t0.At("secrets"), "files": t0.At("files"), "sites": t0.At("sites")})
		lit := wdRedis("cache", "Cache")
		lit["auth"].(A)["password"] = A{"kind": "expr", "expr": "{{ secret.api-token | mosquitto_passwd(9) }}"} // bad filter argument
		wdSiteOf(tc, "db").At("config").Map()["devTools"] = L{lit}
		body := tc.Clone().Map()
		body["ifVersion"] = t0.At("version").V
		e := admin.API.Rejects(t, "patchTemplate", A{"templateId": templateID, "body": body}, "invalid", 422)
		wdHasCode(t, e, "E_SYNTAX")
		// [] → omitted in the template body
		wdPatchTpl(t, admin, templateID, func(c J) { wdSiteOf(c, "app").At("config").Map()["devTools"] = L{} })
		body0 := ParseJ([]byte(Doc(t, "templates/"+templateID).At("_body").Str()))
		False(t, wdSiteOf(body0, "app").At("config").Has("devTools"))

		// the template adds "cache" (Template cache) + "broker"; the draft adds its own "cache" → conflict, broker → theirs
		tpl := wdPatchTpl(t, admin, templateID, func(c J) {
			wdSiteOf(c, "db").At("config").Map()["devTools"] = L{wdRedis("cache", "Template cache"), wdMqtt("broker")}
		})
		Eq(t, wdSiteOf(tpl, "db").At("config.devTools").Len(), 2)
		draftBefore := wdRawDraft(t, id)
		False(t, wdSiteOf(draftBefore, "db").At("config").Has("devTools"), "detached: the template edit does not reach the draft")
		wdMustPutSite(t, admin, id, "db", func(s J) { s.At("config").Map()["devTools"] = L{wdRedis("cache", "My cache")} })
		// §21 #104: Template sync → commit both sides, compare template → deployment, cherry-pick
		tplRef := A{"kind": "template", "templateId": templateID}
		depRef := A{"kind": "deployment", "deploymentId": id}
		admin.API.Call(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "presets", "ifVersion": admin.API.Call(t, "getTemplate", A{"templateId": templateID}).At("version")}})
		admin.API.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "my cache", "ifRevision": admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion")}})
		source := A{"branch": tplRef, "at": "head"}
		plan := admin.API.Call(t, "compareBranches", A{"body": A{"source": source, "target": depRef}})
		k := A{}
		for _, c := range wdDevToolChanges(plan.At("items")) {
			k[c.At("key").Str()] = c.At("kind").V
		}
		Eq(t, k, A{"sites/db/devtools/cache": "conflict", "sites/db/devtools/broker": "theirs"})
		base := func(extra A) A {
			m := A{"source": source, "target": depRef, "targetHead": plan.At("target.head.commitId"), "sourceCommit": plan.At("source.commit.commitId"), "message": "presets from the template"}
			for x, v := range extra {
				m[x] = v
			}
			return m
		}
		un := admin.API.Rejects(t, "merge", A{"body": base(A{"choices": L{A{"key": "sites/db/devtools/broker", "take": "source"}}})}, "invalid", 422)
		Match(t, un.Details.String(), `V_MERGE_UNRESOLVED`)
		// a preset cannot take a custom value (not a scalar)
		cu := admin.API.Rejects(t, "merge", A{"body": base(A{"choices": L{A{"key": "sites/db/devtools/broker", "take": "source"}, A{"key": "sites/db/devtools/cache", "take": "custom", "value": "x"}}})}, "invalid", 422)
		Match(t, cu.Details.String(), `V_MERGE_CHOICE`)
		choices := L{A{"key": "sites/db/devtools/broker", "take": "source"}, A{"key": "sites/db/devtools/cache", "take": "source"}}
		pv := admin.API.Call(t, "previewMerge", A{"body": base(A{"choices": choices})})
		got := wdKindPath(wdDevToolChanges(pv.At("changes")))
		sort.Strings(got)
		Eq(t, got, []string{"added:db/devtools/broker", "changed:db/devtools/cache"})
		admin.API.Call(t, "merge", A{"body": base(A{"choices": choices})})
		after := wdSiteOf(wdRawDraft(t, id), "db").At("config.devTools")
		names := []string{}
		for _, p := range after.Arr() {
			names = append(names, p.At("presetSlug").Str()+":"+p.At("name").Str())
		}
		Eq(t, names, []string{"cache:Template cache", "broker:MQTT"})
		// the template removes every preset; merging it leaves the key ABSENT (never devTools: [])
		wdPatchTpl(t, admin, templateID, func(c J) { delete(wdSiteOf(c, "db").At("config").Map(), "devTools") })
		admin.API.Call(t, "commitTemplate", A{"templateId": templateID, "body": A{"message": "no presets", "ifVersion": admin.API.Call(t, "getTemplate", A{"templateId": templateID}).At("version")}})
		plan2 := admin.API.Call(t, "compareBranches", A{"body": A{"source": source, "target": depRef}})
		ch2 := wdDevToolChanges(plan2.At("items"))
		rows := []string{}
		ch2Choices := L{}
		for _, c := range ch2 {
			rows = append(rows, c.At("key").Str()+":"+c.At("kind").Str()+":"+c.At("effect").Str())
			ch2Choices = append(ch2Choices, A{"key": c.At("key"), "take": "source"})
		}
		sort.Strings(rows)
		Eq(t, rows, []string{"sites/db/devtools/broker:theirs:remove", "sites/db/devtools/cache:theirs:remove"})
		admin.API.Call(t, "merge", A{"body": A{"source": source, "target": depRef, "targetHead": plan2.At("target.head.commitId"), "sourceCommit": plan2.At("source.commit.commitId"), "message": "drop presets", "choices": ch2Choices}})
		False(t, wdSiteOf(wdRawDraft(t, id), "db").At("config").Has("devTools"))
		mine := func(l string) bool {
			return strings.HasPrefix(l, "deployments/"+id) || strings.HasPrefix(l, "templates/"+templateID)
		}
		Eq(t, wdVerify(t, mine), []string{})
		// verify-schema reports a stored devTools: [] and a preset that is not in the stored shape
		b := ParseJ([]byte(Doc(t, "templates/"+templateID).At("_body").Str()))
		wdSiteOf(b, "app").At("config").Map()["devTools"] = L{}
		junk := wdRedis("cache", "Cache")
		junk["junk"] = 1
		wdSiteOf(b, "db").At("config").Map()["devTools"] = JOf(L{junk}).V
		UpdateDoc(t, "templates/"+templateID, A{"_body": b.String()})
		v := strings.Join(wdVerify(t, func(l string) bool { return strings.HasPrefix(l, "templates/"+templateID) }), "\n")
		Match(t, v, `site app: devTools is \[\]`)
		Match(t, v, `site db dev tool cache: not in the stored shape`)
	})

	t.Run("agent activities: every devtools.* type is accepted by POST /v1/agent/activities (the pipe never breaks)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		app := EnrollSite(t, admin, id, "app")
		types := []string{"devtools.query", "devtools.query-cancelled", "devtools.connection-created", "devtools.connection-updated", "devtools.connection-deleted", "devtools.connection-tested", "devtools.credential-revealed", "devtools.mqtt-published", "devtools.mqtt-subscribed", "devtools.mqtt-unsubscribed", "devtools.action-forwarded"}
		query := JOf(A{"connectionId": "p:cache", "connectionName": "Cache", "kind": "redis", "source": "preset", "target": "redis:tcp", "op": "redis", "statement": "SET k ••••••", "statementTruncated": false, "durationMs": 3, "rows": nil, "rowsAffected": nil, "status": nil, "ok": true, "error": nil, "readOnly": false, "destructive": false, "confirmed": false, "queryId": "q1"})
		entries := L{}
		var prev any
		for i, ty := range types {
			var data any = A{"connectionId": "c_01J", "n": i, "nested": L{L{1}, A{"a": nil}}}
			if ty == "devtools.query" {
				data = query
			}
			a := wdMakeActivity(t, id, "app", i+1, prev, ty, data)
			entries = append(entries, a)
			prev = a.At("hash").V
		}
		r := app.API.Call(t, "uploadAgentActivities", A{"body": A{"siteSlug": "app", "entries": entries}})
		Eq(t, L{r.At("ackSeq"), r.At("inserted"), r.At("rejected")}, L{len(types), len(types), 0})
		list := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"site": "app", "type": "devtools."}})
		Eq(t, list.At("items").Len(), len(types))
		q := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "devtools.query"}}).At("items")
		True(t, q.FindF(func(a J) bool {
			return a.At("type").Str() == "devtools.query" && wdCanon(t, a.At("data")) == wdCanon(t, query)
		}).Exists(), q.String())
	})

	t.Run("§21 #99: presets may read another container env ({{ containers.<c>.env.<NAME> }}); unknown name → 422 V_DEVTOOL_ENV; plain env password → warning only", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		// app: hello-api env API_TOKEN (secretRef api-token), GREETING (plain)
		http := func(slug, user, pw string) A {
			p := wdNewPreset("http")
			p["presetSlug"] = slug
			p["target"] = A{"kind": "container", "containerSlug": "hello-api", "portName": "http"}
			a := p["auth"].(A)
			a["httpScheme"], a["username"], a["password"] = "basic", user, A{"kind": "expr", "expr": pw}
			return p
		}
		_, e := wdPutSite(t, admin, id, "app", func(s J) {
			s.At("config").Map()["devTools"] = L{http("api", "{{ containers.hello-api.env.GREETING }}", "{{ containers.hello-api.env.NOPE }}")}
		})
		bad := Rejected(t, e, "invalid", 422)
		wdHasCode(t, bad, "V_DEVTOOL_ENV")
		// a container env reference in the env of a CONTAINER is not allowed (field env) → E_NOT_ALLOWED
		_, e = wdPutSite(t, admin, id, "app", func(s J) {
			wdEnvPush(s, "hello-web", A{"name": "X", "value": "{{ containers.hello-api.env.GREETING }}", "secretRef": nil})
		})
		bad2 := Rejected(t, e, "invalid", 422)
		wdHasCode(t, bad2, "E_NOT_ALLOWED")
		// valid: secret env as password; plain env as password is only a warning (stored)
		wdMustPutSite(t, admin, id, "app", func(s J) {
			s.At("config").Map()["devTools"] = L{http("api", "{{ containers.hello-api.env.GREETING }}", "{{ containers.hello-api.env.API_TOKEN }}"), http("plain", "u", "{{ containers.hello-api.env.GREETING }}")}
		})
		stored := wdSiteOf(wdRawDraft(t, id), "app").At("config.devTools")
		Eq(t, stored.Pluck("auth.password"), L{
			A{"kind": "expr", "expr": "{{ containers.hello-api.env.API_TOKEN }}"},
			A{"kind": "expr", "expr": "{{ containers.hello-api.env.GREETING }}"},
		})
	})

	t.Run("§21 #103: a preset password may be a static Value (stored as a literal, warning only); the Diff never shows it; hash filters in files + presets", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		b := wdMqtt("broker")
		b["auth"].(A)["username"] = "northwind"
		b["auth"].(A)["password"] = A{"kind": "literal", "value": "Static-Value-103"}
		h := wdMqtt("hashed")
		h["auth"].(A)["password"] = A{"kind": "expr", "expr": "{{ secret.api-token | sha256 }}"}
		wdMustPutSite(t, admin, id, "db", func(s J) { s.At("config").Map()["devTools"] = L{b, h} })
		stored := wdSiteOf(wdRawDraft(t, id), "db").At("config.devTools")
		Eq(t, stored.At("0.auth.password"), A{"kind": "literal", "value": "Static-Value-103"}, "stored in the config as a literal")
		Eq(t, stored.At("1.auth.password"), A{"kind": "expr", "expr": "{{ secret.api-token | sha256 }}"})
		diff := admin.API.Call(t, "getDiff", A{"deploymentId": id})
		NotContains(t, diff.String(), "Static-Value-103", "Diff never shows the static value")
		True(t, diff.At("changes").Find("path", "db/devtools/broker").Exists())
		Eq(t, diff.At("publishable"), true, diff.At("blockers").String())
		// change only the value → one changed row with the hidden-value note
		wdMustPutSite(t, admin, id, "db", func(s J) {
			s.At("config.devTools.0.auth").Map()["password"] = A{"kind": "literal", "value": "Other-Value-103"}
		})
		// the user's Mosquitto password file shape is accepted in a container env too (field env allows secrets)
		wdMustPutSite(t, admin, id, "app", func(s J) {
			wdEnvPush(s, "hello-api", A{"name": "MQTT_LINE", "value": "northwind:{{ secret.api-token | mosquitto_passwd }}", "secretRef": nil})
		})
		// …but not in an edge field (password-hash filters only where secrets are allowed)
		_, e := wdPutSite(t, admin, id, "app", func(s J) {
			wdEnvPush(s, "hello-web", A{"name": "BAD", "value": "{{ var.greeting | mosquitto_passwd(5) }}", "secretRef": nil})
		})
		bad := Rejected(t, e, "invalid", 422)
		wdHasCode(t, bad, "E_SYNTAX")
		var audit strings.Builder
		for _, d := range ListDocs(t, "auditLog") {
			audit.WriteString(d.Data.String() + "\n")
		}
		True(t, !strings.Contains(audit.String(), "Static-Value-103") && !strings.Contains(audit.String(), "Other-Value-103"), "audit log redacts the static value")
		p := Publish(t, admin.API, id, admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion"), "static value")
		rv := admin.API.Call(t, "getRevision", A{"deploymentId": id, "revId": p.Revision.At("revId")})
		Eq(t, wdSiteOf(rv.At("config"), "db").At("config.devTools.0.auth.password"), A{"kind": "literal", "value": "Other-Value-103"}, "the revision carries it to the site")
		// verify-schema accepts a static value
		Eq(t, wdVerify(t, func(l string) bool { return strings.Contains(l, id) }), []string{})
	})
}

// wdEnvPush is containers.find(c).env.push(row) on a site.
func wdEnvPush(s J, containerSlug string, row A) {
	c := s.At("config.containers").Find("containerSlug", containerSlug)
	c.Map()["env"] = append(c.At("env").V.([]any), row)
}
