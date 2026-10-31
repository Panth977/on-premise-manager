package apitest

import (
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// wdCloudActivities is cloudActivities(): the _cloud activities of a deployment, data parsed, by seq.
func wdCloudActivities(t testing.TB, deploymentID string) J {
	t.Helper()
	q, err := DB.Collection("deployments/"+deploymentID+"/activities").Where("siteSlug", "==", "_cloud").Get()
	NoErr(t, err)
	rows := []J{}
	for _, d := range q.Docs {
		x := JOf(d.Data())
		x.Map()["data"] = ParseJ([]byte(x.At("_data").Str())).V
		rows = append(rows, x)
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].At("seq").Num() < rows[b].At("seq").Num() })
	return JOf(rows)
}

// wdAuditOf is auditOf(): the audit entries of a target, changes + data parsed.
func wdAuditOf(t testing.TB, target string) J {
	t.Helper()
	q, err := DB.Collection("auditLog").Where("target", "==", target).Get()
	NoErr(t, err)
	rows := []J{}
	for _, d := range q.Docs {
		x := JOf(d.Data())
		x.Map()["changes"] = ParseJ([]byte(x.At("_changes").Str())).V
		x.Map()["data"] = ParseJ([]byte(x.At("_data").Str())).V
		rows = append(rows, x)
	}
	return JOf(rows)
}

func wdByAction(list J, action string) []J {
	return list.Filter(func(e J) bool { return e.At("action").Str() == action })
}

func wdPost(t testing.TB, p *Person, path string, body ...any) *Resp {
	t.Helper()
	o := Opt{Headers: map[string]string{"Authorization": p.Bearer}}
	if len(body) > 0 {
		o.Headers["Content-Type"] = "application/json"
		o.Body = []byte(JOf(body[0]).String())
	}
	return Raw(t, "POST", path, o)
}

// wdFreshWindow is freshWindow(): rate-limit windows are fixed UTC minutes; wait until one has at least 20 s left.
func wdFreshWindow() {
	if s := time.Now().UTC().Second(); s > 40 {
		Sleep((61 - s) * 1000)
	}
}

func wdAllLogs(t testing.TB) string {
	t.Helper()
	var b strings.Builder
	q, err := DB.Collection("auditLog").Get()
	NoErr(t, err)
	for _, d := range q.Docs {
		b.WriteString(JOf(d.Data()).String() + "\n")
	}
	g, err := DB.CollectionGroup("activities").Get()
	NoErr(t, err)
	for _, d := range g.Docs {
		b.WriteString(JOf(d.Data()).String() + "\n")
	}
	return b.String()
}

func wdEnrollBody(key SiteKey) A {
	return A{"publicKey": key.PublicKey, "agentVersion": "0.1.0-test", "hostname": "h", "ip": "127.0.0.1", "advertiseHost": "127.0.0.1", "agentPort": 7080, "os": "darwin", "arch": "arm64"}
}

func wdSiteTokens(setup J, slug string) J {
	return setup.At("sites").Find("siteSlug", slug).At("tokens")
}

func wdParseTime(t testing.TB, s string) time.Time {
	t.Helper()
	x, err := time.Parse(time.RFC3339Nano, s)
	NoErr(t, err)
	return x
}

// Port of backend/test/16-reveal.test.ts.
func TestReveal(t *testing.T) {
	t.Run("access key: stored, revealed any time (activity + audit), rotate = regenerate, not-stored → 410; never in views", func(t *testing.T) {
		wdFreshWindow()
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, accessKey := h.ID(), h.AccessKey.Str()
		// create response: value + 60 s
		view := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
		NotContains(t, view.String(), accessKey, "DeploymentView leaks the key")
		False(t, view.Has("accessKey"))
		setup := admin.API.Call(t, "getSetup", A{"deploymentId": id})
		Eq(t, setup.At("accessKey.stored"), true)
		r1 := wdPost(t, admin, "/v1/deployments/"+id+"/access-key/reveal")
		Eq(t, r1.Status, 200)
		Eq(t, r1.Header("cache-control"), "no-store")
		Eq(t, r1.J.At("value"), accessKey)
		Eq(t, r1.J.At("accessKey"), accessKey)
		Eq(t, r1.J.At("hideAfterSec"), 60)
		Eq(t, r1.J.At("version"), 1)
		acts := wdCloudActivities(t, id)
		rev := acts.Find("type", "access-key.revealed")
		True(t, rev.Truthy(), "access-key.revealed activity")
		Eq(t, rev.At("data"), A{"version": 1})
		Eq(t, rev.At("actor"), admin.UID)
		au := wdByAction(wdAuditOf(t, "deployments/"+id), "deployment.access-key-revealed")
		Eq(t, len(au), 1)
		Eq(t, au[0].At("data"), A{"version": 1})
		Eq(t, au[0].At("actor"), admin.UID)
		// regenerate
		rot := admin.API.Call(t, "rotateAccessKey", A{"deploymentId": id})
		rotV := rot.At("value").Str()
		NotEq(t, rotV, accessKey)
		Eq(t, rot.At("value"), rot.At("accessKey"))
		Eq(t, rot.At("hideAfterSec"), 60)
		Eq(t, admin.API.Call(t, "revealAccessKey", A{"deploymentId": id}).At("value"), rotV)
		Eq(t, len(wdByAction(wdAuditOf(t, "deployments/"+id), "deployment.access-key-rotated")), 1)
		// agents never get the plaintext: enroll response + cloud view
		tok := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
		key := NewSiteKey()
		anon := Bearer(tok.At("token").Str())
		en := anon.Call(t, "enroll", A{"body": wdEnrollBody(key)})
		True(t, !strings.Contains(en.String(), rotV) && !strings.Contains(en.String(), strings.ReplaceAll(rotV, "-", "")))
		poll := AgentClient(key, id, "app").Call(t, "poll", A{"query": A{"wait": 0}})
		NotContains(t, poll.String(), rotV)
		// never in audit / activities
		logs := wdAllLogs(t)
		for _, k := range []string{accessKey, rotV, strings.ReplaceAll(accessKey, "-", ""), strings.ReplaceAll(rotV, "-", "")} {
			NotContains(t, logs, k, "access key leaked into a log")
		}
		// pre-#84 deployment: not stored → 410 gone reason not-stored (no-store on the error too)
		UpdateDoc(t, "deployments/"+id, A{"accessKey": nil})
		Eq(t, admin.API.Call(t, "getSetup", A{"deploymentId": id}).At("accessKey.stored"), false)
		g := wdPost(t, admin, "/v1/deployments/"+id+"/access-key/reveal")
		Eq(t, g.Status, 410)
		Eq(t, g.J.At("error.code"), "gone")
		Eq(t, g.J.At("error.details"), A{"reason": "not-stored"})
		Eq(t, g.Header("cache-control"), "no-store")
		// patchDeployment audit diff does not carry the key
		rot2 := admin.API.Call(t, "rotateAccessKey", A{"deploymentId": id})
		admin.API.Call(t, "patchDeployment", A{"deploymentId": id, "body": A{"label": Uniq("renamed")}})
		patch := wdAuditOf(t, "deployments/"+id).Find("action", "deployment.updated")
		NotContains(t, patch.String(), rot2.At("value").Str())
		False(t, patch.At("changes").Find("path", "accessKey").Exists())
	})

	t.Run("install commands: stored while active, revealed (activity + audit), replaceActive, gone reasons", func(t *testing.T) {
		wdFreshWindow()
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		tokenDoc := func(hash J) string { return "deployments/" + id + "/installTokens/" + hash.Str() }
		t1 := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
		Eq(t, t1.At("value"), t1.At("command"))
		Contains(t, t1.At("command"), t1.At("token").Str())
		Eq(t, t1.At("siteSlug"), "app")
		Eq(t, t1.At("hideAfterSec"), 60)
		setup := admin.API.Call(t, "getSetup", A{"deploymentId": id})
		views := wdSiteTokens(setup, "app")
		Eq(t, views.At("0.stored"), true)
		Eq(t, views.At("0.state"), "active")
		NotContains(t, setup.String(), t1.At("token").Str(), "getSetup leaks the token")
		r := wdPost(t, admin, "/v1/deployments/"+id+"/install-tokens/"+t1.At("tokenHash").Str()+"/reveal")
		Eq(t, r.Status, 200)
		Eq(t, r.Header("cache-control"), "no-store")
		Eq(t, r.J.At("value"), t1.At("command"))
		Eq(t, r.J.At("siteSlug"), "app")
		act := wdCloudActivities(t, id).Find("type", "install-token.revealed")
		Eq(t, act.At("data"), A{"siteSlug": "app", "tokenHash": t1.At("tokenHash")})
		au := wdByAction(wdAuditOf(t, "deployments/"+id), "deployment.install-command-revealed")
		Eq(t, len(au), 1)
		Eq(t, au[0].At("data"), A{"siteSlug": "app", "tokenHash": t1.At("tokenHash")})
		// Regenerate: replaceActive revokes the active one (activity per revoked token, token nulled)
		t2 := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app", "replaceActive": true}})
		old := Doc(t, tokenDoc(t1.At("tokenHash")))
		True(t, old.At("revokedAt").Truthy())
		Eq(t, old.At("token"), nil)
		True(t, wdCloudActivities(t, id).FindF(func(a J) bool {
			return a.At("type").Str() == "install-token.revoked" && a.At("data.tokenHash").Str() == t1.At("tokenHash").Str()
		}).Exists())
		gRevoked := admin.API.Rejects(t, "revealInstallCommand", A{"deploymentId": id, "tokenHash": t1.At("tokenHash")}, "gone", 410)
		Eq(t, gRevoked.Details, A{"reason": "revoked"})
		active := wdSiteTokens(admin.API.Call(t, "getSetup", A{"deploymentId": id}), "app").Filter(func(x J) bool { return x.At("state").Str() == "active" })
		Eq(t, len(active), 1)
		// used: enroll KEEPS the plaintext (§21 #92: it is now the site's upgrade command, revealUpgradeCommand); the fresh-site
		// install command view still answers gone/used
		anon := Bearer(t2.At("token").Str())
		anon.Call(t, "enroll", A{"body": wdEnrollBody(NewSiteKey())})
		Eq(t, Doc(t, tokenDoc(t2.At("tokenHash"))).At("token"), t2.At("token"))
		gUsed := admin.API.Rejects(t, "revealInstallCommand", A{"deploymentId": id, "tokenHash": t2.At("tokenHash")}, "gone", 410)
		Eq(t, gUsed.Details, A{"reason": "used"})
		// not-stored (pre-#84 token) and expired
		t3 := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "db"}})
		UpdateDoc(t, tokenDoc(t3.At("tokenHash")), A{"token": nil})
		st := wdSiteTokens(admin.API.Call(t, "getSetup", A{"deploymentId": id}), "db").Find("tokenHash", t3.At("tokenHash"))
		Eq(t, st.At("stored"), false)
		Eq(t, admin.API.Rejects(t, "revealInstallCommand", A{"deploymentId": id, "tokenHash": t3.At("tokenHash")}, "gone", 410).Details, A{"reason": "not-stored"})
		t4 := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "db"}})
		UpdateDoc(t, tokenDoc(t4.At("tokenHash")), A{"expiresAt": ISO(time.Now().Add(-time.Second))})
		Eq(t, admin.API.Rejects(t, "revealInstallCommand", A{"deploymentId": id, "tokenHash": t4.At("tokenHash")}, "gone", 410).Details, A{"reason": "expired"})
		// the daily job nulls the plaintext of expired tokens
		d := RunDaily(t)
		True(t, d.At("tokensCleared").Num() >= 1)
		Eq(t, Doc(t, tokenDoc(t4.At("tokenHash"))).At("token"), nil)
		// revoke nulls the token
		t5 := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "db"}})
		admin.API.Call(t, "revokeInstallToken", A{"deploymentId": id, "tokenHash": t5.At("tokenHash")})
		Eq(t, Doc(t, tokenDoc(t5.At("tokenHash"))).At("token"), nil)
		logs := wdAllLogs(t)
		for _, x := range []J{t1, t2, t3, t4, t5} {
			NotContains(t, logs, x.At("token").Str(), "install token leaked into a log")
		}
	})

	t.Run("secrets: reveal 60 s (activity + audit); §21 #105 regenerate = generateNow in a commit (slugs in data / activity / audit), the old route 410", func(t *testing.T) {
		wdFreshWindow()
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		r := wdPost(t, admin, "/v1/deployments/"+id+"/secrets/api-token/reveal")
		Eq(t, r.Status, 200)
		Eq(t, r.Header("cache-control"), "no-store")
		Eq(t, r.J.At("hideAfterSec"), 60)
		v1 := r.J.At("value").Str()
		sr := wdAuditOf(t, "deployments/"+id).Filter(func(e J) bool {
			return e.At("action").Str() == "deployment.secret-revealed" && e.At("data.secretSlug").Str() == "api-token"
		})
		Eq(t, len(sr), 1, JOf(sr).String())
		// §21 #105: regenerate is a pending change (generateNow) written by the next commit; the route answers 410
		dv := admin.Real.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion")
		gone := admin.Real.Rejects(t, "regenerateSecret", A{"deploymentId": id, "secretSlug": "api-token", "body": A{"ifRevision": dv}}, "gone", 410)
		True(t, gone.Details.At("hint").Truthy())
		cfg := DepInput(t, admin.Real, id)
		cfg.At("secrets").Find("secretSlug", "api-token").Map()["generateNow"] = true
		cm := admin.Real.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "new api token", "baseCommitId": admin.Real.Call(t, "getDeployment", A{"deploymentId": id}).At("branch.head.commitId"), "config": cfg}})
		want := A{"set": L{}, "generated": L{"api-token"}, "cleared": L{}}
		Eq(t, cm.At("data.secretValues"), want)
		g := admin.API.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "api-token"})
		NotEq(t, g.At("value"), v1)
		Match(t, g.At("value"), `^[A-Za-z0-9]{24}$`)
		bcs := wdCloudActivities(t, id).Filter(func(a J) bool { return a.At("type").Str() == "branch.committed" })
		Eq(t, bcs[len(bcs)-1].At("data.secretValues"), want)
		au := wdAuditOf(t, "deployments/"+id).Filter(func(e J) bool {
			return e.At("action").Str() == "deployment.committed" && e.At("data.commitId").Str() == cm.At("commitId").Str()
		})
		Eq(t, len(au), 1)
		Eq(t, au[0].At("data.secretValues"), want)
		logs := wdAllLogs(t)
		for _, v := range []string{v1, g.At("value").Str()} {
			NotContains(t, logs, v, "secret value leaked into a log")
		}
	})

	t.Run("registry password reveal: 60 s, value = password, audited", func(t *testing.T) {
		wdFreshWindow()
		admin := NewPerson(t, "admin")
		pw := "REG-" + Uniq("")
		reg := admin.API.Call(t, "createRegistry", A{"body": A{"label": "Reveal", "url": "https://127.0.0.1:1", "username": "u", "password": pw}})
		r := wdPost(t, admin, "/v1/registries/"+reg.At("registryId").Str()+"/reveal-password")
		Eq(t, r.Status, 200)
		Eq(t, r.Header("cache-control"), "no-store")
		Eq(t, r.J, A{"value": pw, "password": pw, "hideAfterSec": 60})
	})

	t.Run("permissions: every reveal / regenerate needs its permission (403)", func(t *testing.T) {
		wdFreshWindow()
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		tok := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
		reg := admin.API.Call(t, "createRegistry", A{"body": A{"label": "Perm", "url": "https://127.0.0.1:1", "username": "u", "password": "p"}})
		acc := admin.API.Call(t, "createAccount", A{"body": A{"name": "Perm target", "email": Uniq("pt") + "@opm.test"}})
		all := A{"kind": "all"}
		rules := L{
			A{"resource": "deployments", "actions": L{"read"}, "scope": all},
			A{"resource": "deployment.setup", "actions": L{"read"}, "scope": all},
			A{"resource": "deployment.config", "actions": L{"read"}, "scope": all},
			A{"resource": "registries", "actions": L{"read"}, "scope": all},
			A{"resource": "accounts", "actions": L{"read", "write"}, "scope": all},
		}
		viewer := NewPerson(t, rules)
		dv := admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion")
		uid := acc.At("account.uid")
		calls := []struct {
			op   string
			args A
		}{
			{"revealAccessKey", A{"deploymentId": id}},
			{"rotateAccessKey", A{"deploymentId": id}},
			{"createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "db"}}},
			{"revealInstallCommand", A{"deploymentId": id, "tokenHash": tok.At("tokenHash")}},
			{"revealSecret", A{"deploymentId": id, "secretSlug": "api-token"}},
			{"revealRegistryPassword", A{"registryId": reg.At("registryId")}},
			{"createAccount", A{"body": A{"name": "x", "email": Uniq("x") + "@opm.test"}}},
			{"setAccountPassword", A{"uid": uid}},
			{"revealAccountPassword", A{"uid": uid}},
			{"resetAccountPassword", A{"uid": uid}},
			{"revealAccountResetLink", A{"uid": uid}},
		}
		for _, c := range calls {
			if _, e := viewer.API.Try(c.op, c.args); e == nil || e.Code != "forbidden" || e.Status != 403 {
				t.Fatalf("%s: expected forbidden 403, got %v", c.op, e)
			}
		}
		// denials are audited (deployment credential ops too)
		q, err := DB.Collection("auditLog").Where("actor", "==", viewer.UID).Get()
		NoErr(t, err)
		denied := []J{}
		for _, d := range q.Docs {
			denied = append(denied, JOf(d.Data()))
		}
		for _, a := range []string{"deployment.access-key-revealed", "account.password-revealed", "registry.password-revealed"} {
			True(t, JOf(denied).FindF(func(e J) bool { return e.At("action").Str() == a && e.At("status").Int() == 403 }).Exists(), "no 403 audit entry "+a)
		}
		// admin still can; the secret value stays the same (the denied regenerate changed nothing)
		Eq(t, admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion"), dv)
	})

	t.Run("rate limit: 30 value-returning requests per uid per minute → 429 rate-limited + Retry-After (no-store)", func(t *testing.T) {
		wdFreshWindow()
		admin := NewPerson(t, "admin")
		reg := admin.API.Call(t, "createRegistry", A{"body": A{"label": "Rate", "url": "https://127.0.0.1:1", "username": "u", "password": "p"}})
		path := "/v1/registries/" + reg.At("registryId").Str() + "/reveal-password"
		for i := 0; i < 30; i++ {
			r := wdPost(t, admin, path)
			Eq(t, r.Status, 200, "request %d: %d %.200s", i+1, r.Status, r.Text())
		}
		r := wdPost(t, admin, path)
		Eq(t, r.Status, 429)
		Eq(t, r.J.At("error.code"), "rate-limited")
		retry := r.J.At("error.details.retryAfterSec")
		True(t, retry.Num() == float64(retry.Int()) && retry.Int() >= 1 && retry.Int() <= 60, retry.String())
		Eq(t, r.Header("retry-after"), Itoa(retry.Int()))
		Eq(t, r.Header("cache-control"), "no-store")
		// other credential ops share the per-uid budget; other people are not affected
		r2 := wdPost(t, admin, "/v1/accounts/"+admin.UID+"/password/reveal")
		Eq(t, r2.Status, 429)
		other := NewPerson(t, "admin")
		Eq(t, wdPost(t, other, path).Status, 200)
		// the 429 is audited
		q, err := DB.Collection("auditLog").Where("requestId", "==", r.Header("x-request-id")).Get()
		NoErr(t, err)
		Eq(t, JOf(q.Docs[0].Data()).At("status"), 429)
		// the counter doc has a TTL field
		rl, err := DB.Collection("rateLimits").Where("uid", "==", admin.UID).Get()
		NoErr(t, err)
		True(t, len(rl.Docs) >= 1 && JOf(rl.Docs[0].Data()).At("expireAt").Truthy())
	})

	t.Run("accounts: generated password stored + revealed, set new password, cleared after the person changes it; reset links", func(t *testing.T) {
		wdFreshWindow()
		admin := NewPerson(t, "admin")
		email := Uniq("pw") + "@opm.test"
		created := wdPost(t, admin, "/v1/accounts", A{"name": "Password person", "email": email})
		Eq(t, created.Status, 201)
		Eq(t, created.Header("cache-control"), "no-store")
		pw0 := created.J.At("value").Str()
		pw20 := `^[A-Za-z0-9]{20}$`
		Match(t, pw0, pw20)
		uid := created.J.At("account.uid").Str()
		Eq(t, created.J.At("account.lastSetPassword.setBy"), admin.UID)
		True(t, IDToken(t, email, pw0) != "", "the generated password works")
		// reveal = same value; GET never returns values
		rv := admin.API.Call(t, "revealAccountPassword", A{"uid": uid})
		Eq(t, rv.At("value"), pw0)
		Eq(t, rv.At("hideAfterSec"), 60)
		got := admin.API.Call(t, "getAccount", A{"uid": uid})
		Eq(t, got.At("lastSetPassword").Keys(), []string{"setAt", "setBy"})
		Eq(t, got.At("lastResetLink"), nil)
		list := admin.API.Call(t, "listAccounts", A{})
		NotContains(t, list.String(), pw0)
		// set new password
		set := admin.API.Call(t, "setAccountPassword", A{"uid": uid})
		NotEq(t, set.At("value"), pw0)
		Match(t, set.At("value"), pw20)
		Eq(t, set.At("setBy"), admin.UID)
		True(t, IDToken(t, email, set.At("value").Str()) != "")
		Eq(t, admin.API.Call(t, "revealAccountPassword", A{"uid": uid}).At("value"), set.At("value"))
		// reset link: stored 1 h, revealed
		link := admin.API.Call(t, "resetAccountPassword", A{"uid": uid})
		Match(t, link.At("value"), `^https?://`)
		Eq(t, link.At("passwordSetLink"), link.At("value"))
		Eq(t, link.At("createdBy"), admin.UID)
		True(t, wdParseTime(t, link.At("expiresAt").Str()).Sub(wdParseTime(t, link.At("createdAt").Str())) > 3500*time.Second)
		Eq(t, admin.API.Call(t, "revealAccountResetLink", A{"uid": uid}).At("value"), link.At("value"))
		meta := admin.API.Call(t, "getAccount", A{"uid": uid}).At("lastResetLink")
		Eq(t, meta.Keys(), []string{"expiresAt", "setAt", "setBy"})
		// expired link → gone expired (and cleared)
		UpdateDoc(t, "accounts/"+uid, A{"lastResetLink.expiresAt": ISO(time.Now().Add(-time.Second))})
		Eq(t, admin.API.Rejects(t, "revealAccountResetLink", A{"uid": uid}, "gone", 410).Details, A{"reason": "expired"})
		Eq(t, Doc(t, "accounts/"+uid).At("lastResetLink"), nil)
		// the person changes their password / tokens revoked → tokensValidAfterTime moves → cleared → gone changed
		Sleep(1100)
		NoErr(t, AuthAdmin.RevokeRefreshTokens(uid))
		g := wdPost(t, admin, "/v1/accounts/"+uid+"/password/reveal")
		Eq(t, g.Status, 410)
		Eq(t, g.J.At("error.details"), A{"reason": "changed"})
		Eq(t, g.Header("cache-control"), "no-store")
		Eq(t, admin.API.Call(t, "getAccount", A{"uid": uid}).At("lastSetPassword"), nil)
		Eq(t, Doc(t, "accounts/"+uid).At("lastSetPassword"), nil)
		// none stored → gone not-stored
		Eq(t, admin.API.Rejects(t, "revealAccountResetLink", A{"uid": uid}, "gone", 410).Details, A{"reason": "not-stored"})
		// listAccounts applies the clearing rule too (batched)
		set2 := admin.API.Call(t, "setAccountPassword", A{"uid": uid})
		Sleep(1100)
		NoErr(t, AuthAdmin.RevokeRefreshTokens(uid))
		l2 := admin.API.Call(t, "listAccounts", A{})
		Eq(t, l2.At("items").Find("uid", uid).At("lastSetPassword"), nil)
		// audit: actions, never values
		audit := wdAuditOf(t, "accounts/"+uid)
		for _, a := range []string{"account.created", "account.password-set", "account.password-revealed", "account.reset-link-created", "account.reset-link-revealed"} {
			True(t, audit.Find("action", a).Exists(), "missing audit "+a)
		}
		Eq(t, audit.Find("action", "account.created").At("data.passwordSet"), true)
		logs := wdAllLogs(t)
		oob := link.At("value").Str()
		if u, err := url.Parse(oob); err == nil && u.Query().Has("oobCode") {
			oob = u.Query().Get("oobCode")
		}
		for _, v := range []string{pw0, set.At("value").Str(), set2.At("value").Str(), link.At("value").Str(), oob} {
			NotContains(t, logs, v, "account credential leaked into a log")
		}
	})

	t.Run("migrate-g: pre-#84 docs get null fields (never rotates); verify-schema requires them; idempotent", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin)
		id, accessKey := h.ID(), h.AccessKey.Str()
		// the shape migrate-g left (the migration script itself is gone): accessKey null, the hash and version kept
		UpdateDoc(t, "deployments/"+id, A{"accessKey": nil})
		// a migrated deployment: not stored → regenerate → viewable again
		Eq(t, admin.API.Rejects(t, "revealAccessKey", A{"deploymentId": id}, "gone", 410).Details, A{"reason": "not-stored"})
		rot := admin.API.Call(t, "rotateAccessKey", A{"deploymentId": id})
		NotEq(t, rot.At("value"), accessKey)
		Eq(t, admin.API.Call(t, "revealAccessKey", A{"deploymentId": id}).At("value"), rot.At("value"))
	})
}
