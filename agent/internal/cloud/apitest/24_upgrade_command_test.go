package apitest

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/shared"
)

// wfMs is Date.parse.
func wfMs(t testing.TB, s string) int64 {
	t.Helper()
	x, err := time.Parse(time.RFC3339Nano, s)
	NoErr(t, err)
	return x.UnixMilli()
}

func wfNowMs() int64 { return time.Now().UnixMilli() }

func wfWithData(t testing.TB, d J) J {
	t.Helper()
	c := d.Clone()
	c.Map()["data"] = ParseJ([]byte(d.At("_data").Str())).V
	return c
}

// wfCloudActivities is cloudActivities(): the deployment's activities of the cloud, by seq, data parsed.
func wfCloudActivities(t testing.TB, deploymentID string) J {
	t.Helper()
	q, err := DB.Collection("deployments/"+deploymentID+"/activities").Where("siteSlug", "==", "_cloud").Get()
	NoErr(t, err)
	out := []J{}
	for _, d := range q.Docs {
		out = append(out, wfWithData(t, JOf(d.Data())))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At("seq").Num() < out[j].At("seq").Num() })
	return JOf(out)
}

// wfAuditOf is auditOf(): the audit entries of a target, data parsed.
func wfAuditOf(t testing.TB, target string) J {
	t.Helper()
	q, err := DB.Collection("auditLog").Where("target", "==", target).Get()
	NoErr(t, err)
	out := []J{}
	for _, d := range q.Docs {
		out = append(out, wfWithData(t, JOf(d.Data())))
	}
	return JOf(out)
}

func wfEnrollBody() A {
	return A{"publicKey": NewSiteKey().PublicKey, "agentVersion": "0.1.0-test", "hostname": "h", "ip": "127.0.0.1", "advertiseHost": "127.0.0.1", "agentPort": 7080, "os": "darwin", "arch": "arm64"}
}

func wfSite(t testing.TB, admin *Person, id, slug string) J {
	t.Helper()
	return admin.API.Call(t, "getSetup", A{"deploymentId": id}).At("sites").Find("siteSlug", slug)
}

func wfAuthHeader(token string) Opt {
	return Opt{Headers: map[string]string{"Authorization": "Bearer " + token}}
}

// Port of backend/test/24-upgrade-command.test.ts.
func TestUpgradeCommand(t *testing.T) {
	t.Run("§21 #92 upgrade / reinstall command: install token kept, reveal logged, regenerate = maintenance token, rules", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		toks := "deployments/" + id + "/installTokens"
		app := EnrollSite(t, admin, id, "app")
		q, err := DB.Collection(toks).Where("siteSlug", "==", "app").Get()
		NoErr(t, err)
		firstHash := q.Docs[0].ID()
		m1 := J{A{"token": "", "tokenHash": "", "kind": "", "value": "", "command": "", "enrolledAt": ""}}

		t.Run("enrolled site: the install token stays stored and is the site's upgrade command (view logged + audited)", func(t *testing.T) {
			d := Doc(t, toks+"/"+firstHash)
			Eq(t, d.At("token"), app.Token, "enroll keeps the plaintext")
			True(t, d.At("usedAt").Truthy())
			setup := admin.API.Call(t, "getSetup", A{"deploymentId": id})
			True(t, math.Abs(float64(wfMs(t, setup.At("serverTime").Str())-wfNowMs())) < 10_000, "serverTime")
			site := setup.At("sites").Find("siteSlug", "app")
			uc := site.At("upgradeCommand").Clone()
			uc.Map()["createdAt"], uc.Map()["createdBy"] = nil, nil
			Eq(t, uc, A{"tokenHash": firstHash, "kind": "install", "stored": true, "createdAt": nil, "createdBy": nil})
			tv := site.At("tokens").Find("tokenHash", firstHash)
			Eq(t, tv.At("state"), "used")
			Eq(t, tv.At("kind"), "install")
			Eq(t, tv.At("upgradeCommand"), true)
			Eq(t, tv.At("stored"), false, "the fresh-site View stays off for a used token")
			NotContains(t, setup.String(), app.Token, "setup never carries the token")
			// not enrolled → 404 (fresh sites use the install command)
			Eq(t, setup.At("sites").Find("siteSlug", "db").At("upgradeCommand"), nil)
			admin.API.Rejects(t, "revealUpgradeCommand", A{"deploymentId": id, "siteSlug": "db"}, "not-found", 404)
			r := Raw(t, "POST", "/v1/deployments/"+id+"/agents/app/upgrade-command/reveal", Opt{Headers: map[string]string{"Authorization": admin.Bearer}})
			Eq(t, r.Status, 200)
			Eq(t, r.Header("cache-control"), "no-store")
			j := r.J
			Eq(t, j.At("token"), app.Token)
			Eq(t, j.At("kind"), "install")
			Eq(t, j.At("hideAfterSec"), 60)
			Eq(t, j.At("value"), j.At("command"))
			Match(t, j.At("command").Str(), `^curl -fsSL -H "Authorization: Bearer `+regexp.QuoteMeta(app.Token)+`" .+/v1/install/script \| sudo sh$`)
			act := wfCloudActivities(t, id).Find("type", "install-command.revealed")
			Eq(t, act.At("data"), A{"siteSlug": "app", "tokenHash": firstHash, "kind": "install"})
			Eq(t, act.At("actor"), admin.UID)
			au := JOf(wfAuditOf(t, "deployments/"+id).Filter(func(e J) bool {
				return e.At("action").Str() == "deployment.upgrade-command-revealed" && e.At("outcome").Str() == "ok"
			}))
			Eq(t, au.Len(), 1)
			Eq(t, au.At("0.data"), A{"siteSlug": "app", "tokenHash": firstHash, "kind": "install"})
			// the fresh-site reveal still says used
			e := admin.API.Rejects(t, "revealInstallCommand", A{"deploymentId": id, "tokenHash": firstHash}, "gone", 410)
			Eq(t, e.Details, A{"reason": "used"})
			// a new install command for an enrolled site stays a conflict
			admin.API.Rejects(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}}, "conflict", 409)
		})

		t.Run("older token (plaintext not stored) → 410 not-stored; Regenerate mints a maintenance token and revokes the old command", func(t *testing.T) {
			UpdateDoc(t, toks+"/"+firstHash, A{"token": nil}) // like the user's Dev Server (migrate-g nulled it)
			site := wfSite(t, admin, id, "app")
			Eq(t, site.At("upgradeCommand.stored"), false)
			Eq(t, site.At("upgradeCommand.tokenHash"), firstHash)
			e := admin.API.Rejects(t, "revealUpgradeCommand", A{"deploymentId": id, "siteSlug": "app"}, "gone", 410)
			Eq(t, e.Details, A{"reason": "not-stored"})
			// the old command still works on the server until Regenerate (upgrade rule of §21 #86)
			Contains(t, Raw(t, "GET", "/v1/install/script", wfAuthHeader(app.Token)).Text(), `UPGRADE_ONLY="1"`)
			r := Raw(t, "POST", "/v1/deployments/"+id+"/agents/app/upgrade-command", Opt{Headers: map[string]string{"Authorization": admin.Bearer}})
			Eq(t, r.Status, 201)
			Eq(t, r.Header("cache-control"), "no-store")
			m1 = r.J
			Eq(t, m1.At("kind"), "maintenance")
			Eq(t, m1.At("value"), m1.At("command"))
			enrolledAt := Doc(t, "deployments/"+id+"/agents/app").At("enrolledAt")
			Eq(t, m1.At("enrolledAt"), enrolledAt)
			md := Doc(t, toks+"/"+m1.At("tokenHash").Str())
			Eq(t, A{"kind": md.At("kind"), "token": md.At("token"), "expiresAt": md.At("expiresAt"), "boundEnrolledAt": md.At("boundEnrolledAt"), "usedAt": md.At("usedAt"), "revokedAt": md.At("revokedAt")},
				A{"kind": "maintenance", "token": m1.At("token"), "expiresAt": nil, "boundEnrolledAt": enrolledAt, "usedAt": nil, "revokedAt": nil})
			old := Doc(t, toks+"/"+firstHash)
			True(t, old.At("revokedAt").Truthy(), "the enrollment's install token is revoked by Regenerate")
			Eq(t, old.At("token"), nil)
			TinyScript(t, Raw(t, "GET", "/v1/install/script", wfAuthHeader(app.Token)), `token revoked \(site app\)`)
			act := JOf(wfCloudActivities(t, id).Filter(func(a J) bool { return a.At("type").Str() == "install-command.regenerated" }))
			Eq(t, act.Len(), 1)
			Eq(t, act.At("0.data"), A{"siteSlug": "app", "tokenHash": m1.At("tokenHash"), "revoked": L{firstHash}})
			au := JOf(wfAuditOf(t, "deployments/"+id).Filter(func(e J) bool {
				return e.At("action").Str() == "deployment.upgrade-command-regenerated" && e.At("outcome").Str() == "ok"
			}))
			Eq(t, au.Len(), 1)
			Eq(t, au.At("0.data"), A{"siteSlug": "app", "tokenHash": m1.At("tokenHash"), "kind": "maintenance", "revoked": L{firstHash}})
			site = wfSite(t, admin, id, "app")
			Eq(t, site.At("upgradeCommand.kind"), "maintenance")
			Eq(t, site.At("upgradeCommand.stored"), true)
			Eq(t, site.At("upgradeCommand.tokenHash"), m1.At("tokenHash"))
			mv := site.At("tokens").Find("tokenHash", m1.At("tokenHash"))
			Eq(t, A{"kind": mv.At("kind"), "state": mv.At("state"), "expiresAt": mv.At("expiresAt"), "stored": mv.At("stored"), "upgradeCommand": mv.At("upgradeCommand")},
				A{"kind": "maintenance", "state": "active", "expiresAt": nil, "stored": false, "upgradeCommand": true})
			rv := admin.API.Call(t, "revealUpgradeCommand", A{"deploymentId": id, "siteSlug": "app"})
			Eq(t, rv.At("token"), m1.At("token"))
			Eq(t, rv.At("kind"), "maintenance")
			// the maintenance token is not a fresh-site install command
			e = admin.API.Rejects(t, "revealInstallCommand", A{"deploymentId": id, "tokenHash": m1.At("tokenHash")}, "gone", 410)
			Eq(t, e.Details, A{"reason": "maintenance"})
		})

		t.Run("maintenance token: upgrade-only script + binary/deps/release accepted, never enrolls, never expires", func(t *testing.T) {
			H := wfAuthHeader(m1.At("token").Str())
			s := Raw(t, "GET", "/v1/install/script", H)
			Eq(t, s.Status, 200)
			sh := s.Text()
			Contains(t, sh, `UPGRADE_ONLY="1"`, "upgrade-only flow")
			Contains(t, sh, "This upgrade command belongs to the enrolled site $SITE; it can only upgrade that server")
			NotContains(t, sh, "This install command already enrolled")
			Contains(t, sh, `TOKEN="`+m1.At("token").Str()+`"`)
			// binary / release: the token is accepted (404 / 422 = past the auth check; no agent uploaded in this test)
			for _, p := range []string{
				"/v1/install/binary?os=linux&arch=amd64&version=0.0.0-none",
				"/v1/install/release?os=linux&arch=amd64&version=0.0.0-none",
			} {
				b := Raw(t, "GET", p, H)
				True(t, b.Status != 401 && b.Status != 403 && b.Status != 410, "%s → %d", p, b.Status)
			}
			anon := Bearer(m1.At("token").Str())
			e := anon.Rejects(t, "enroll", A{"body": wfEnrollBody()}, "forbidden", 403)
			Eq(t, e.Details.At("reason"), "maintenance")
			// no 24 h expiry: the daily job keeps it, an old createdAt changes nothing
			UpdateDoc(t, toks+"/"+m1.At("tokenHash").Str(), A{"createdAt": ISO(time.Now().Add(-30 * 24 * time.Hour))})
			RunDaily(t)
			after := Doc(t, toks+"/"+m1.At("tokenHash").Str())
			Eq(t, after.At("token"), m1.At("token"))
			Eq(t, after.At("revokedAt"), nil)
			Eq(t, Raw(t, "GET", "/v1/install/script", H).Status, 200)
			Contains(t, Raw(t, "GET", "/v1/install/script", H).Text(), `UPGRADE_ONLY="1"`)
		})

		t.Run("Regenerate again revokes the previous maintenance token", func(t *testing.T) {
			m2 := admin.API.Call(t, "regenerateUpgradeCommand", A{"deploymentId": id, "siteSlug": "app"})
			NotEq(t, m2.At("token"), m1.At("token"))
			prev := Doc(t, toks+"/"+m1.At("tokenHash").Str())
			True(t, prev.At("revokedAt").Truthy())
			Eq(t, prev.At("token"), nil)
			TinyScript(t, Raw(t, "GET", "/v1/install/script", wfAuthHeader(m1.At("token").Str())), `token revoked \(site app\)`)
			Eq(t, Raw(t, "GET", "/v1/install/binary?os=linux&arch=amd64", wfAuthHeader(m1.At("token").Str())).Status, 410)
			Contains(t, Raw(t, "GET", "/v1/install/script", wfAuthHeader(m2.At("token").Str())).Text(), `UPGRADE_ONLY="1"`)
			site := wfSite(t, admin, id, "app")
			Eq(t, site.At("upgradeCommand.tokenHash"), m2.At("tokenHash"))
			Eq(t, site.At("tokens").Find("tokenHash", m1.At("tokenHash")).At("state"), "revoked")

			// a maintenance token bound to another (ended) enrollment never works
			foreign := "opi_" + strings.Repeat("x", 43)
			fh := Sha256Hex(foreign)
			SetDoc(t, toks+"/"+fh, A{"tokenHash": fh, "siteSlug": "app", "kind": "maintenance", "token": nil, "expiresAt": nil, "boundEnrolledAt": "2020-01-01T00:00:00.000Z", "usedAt": nil, "revokedAt": nil, "createdAt": Now(), "createdBy": admin.UID})
			TinyScript(t, Raw(t, "GET", "/v1/install/script", wfAuthHeader(foreign)), `belongs to an enrollment \(site app\) that was revoked`)
			NoErr(t, DB.Doc(toks+"/"+fh).Delete())

			// revokeAgent ends the enrollment's upgrade command
			admin.API.Call(t, "revokeAgent", A{"deploymentId": id, "siteSlug": "app", "body": A{"confirm": "app"}})
			m2d := Doc(t, toks+"/"+m2.At("tokenHash").Str())
			True(t, m2d.At("revokedAt").Truthy())
			Eq(t, m2d.At("token"), nil)
			TinyScript(t, Raw(t, "GET", "/v1/install/script", wfAuthHeader(m2.At("token").Str())), `token revoked \(site app\)`)
			admin.API.Rejects(t, "revealUpgradeCommand", A{"deploymentId": id, "siteSlug": "app"}, "not-found", 404)
			admin.API.Rejects(t, "regenerateUpgradeCommand", A{"deploymentId": id, "siteSlug": "app"}, "not-found", 404)
			// the site enrolls again with a fresh 24 h install token (single enroll; the new token becomes the upgrade command)
			fresh := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
			left := wfMs(t, fresh.At("expiresAt").Str()) - wfNowMs()
			True(t, left > 23*3600_000 && left <= 24*3600_000+5000)
			anon := Bearer(fresh.At("token").Str())
			anon.Call(t, "enroll", A{"body": wfEnrollBody()})
			anon.Rejects(t, "enroll", A{"body": wfEnrollBody()}, "gone", 410) // never a second enroll
			s2 := wfSite(t, admin, id, "app")
			Eq(t, L{s2.At("upgradeCommand.tokenHash"), s2.At("upgradeCommand.kind"), s2.At("upgradeCommand.stored")}, L{fresh.At("tokenHash"), "install", true})
			Eq(t, admin.API.Call(t, "revealUpgradeCommand", A{"deploymentId": id, "siteSlug": "app"}).At("token"), fresh.At("token"))
		})

		t.Run("permissions: setup.read cannot view or regenerate; tokens never leak into logs; schema clean", func(t *testing.T) {
			all := A{"kind": "all"}
			viewer := NewPerson(t, L{
				A{"resource": "deployments", "actions": L{"read"}, "scope": all},
				A{"resource": "deployment.setup", "actions": L{"read"}, "scope": all},
			})
			viewer.API.Rejects(t, "revealUpgradeCommand", A{"deploymentId": id, "siteSlug": "app"}, "forbidden", 403)
			viewer.API.Rejects(t, "regenerateUpgradeCommand", A{"deploymentId": id, "siteSlug": "app"}, "forbidden", 403)
			plain := []string{}
			for _, d := range ListDocs(t, toks) {
				if s, ok := d.Data.At("token").V.(string); ok {
					plain = append(plain, s)
				}
			}
			var logs strings.Builder
			for _, d := range ListDocs(t, "auditLog") {
				logs.WriteString(d.Data.String() + "\n")
			}
			for _, d := range ListDocs(t, "deployments/"+id+"/activities") {
				logs.WriteString(d.Data.String() + "\n")
			}
			for _, p := range append(plain, app.Token) {
				NotContains(t, logs.String(), p, "a token leaked into a log")
			}
			lines, err := core.VerifySchema(DB, core.VerifyOptions{})
			NoErr(t, err)
			mine := []string{}
			for _, l := range lines {
				if strings.Contains(l, id) {
					mine = append(mine, l)
				}
			}
			Eq(t, mine, L{})
		})
	})

	t.Run("§21 #92 heartbeat: lastSeenAt = server receive time; agentTime → clockSkew (agent − server); 422 on a bad agentTime", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		app := EnrollSite(t, admin, id, "app")
		hb := func(extra ...any) A {
			m := A{"siteSlug": "app", "agentVersion": "0.11.0-test", "headRevId": nil, "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0, "memPct": 0, "diskPct": 0}, "edge": nil, "keyVersion": 1, "advertiseHost": "127.0.0.1", "agentPort": 7080, "ip": "127.0.0.1", "hostname": "h"}
			if len(extra) > 0 {
				m["agentTime"] = extra[0]
			}
			return m
		}
		warning := func(skew J) any {
			if w := shared.ClockSkewWarning(skew.V); w != nil {
				return *w
			}
			return nil
		}
		// an old agent (no agentTime): no skew
		app.API.Call(t, "heartbeat", A{"body": hb()})
		site := wfSite(t, admin, id, "app")
		Eq(t, site.At("agent.clockSkew"), nil)
		// the agent clock 5 h 30 min ahead (the signature still verifies: the agent signs with the server offset)
		ahead := ISO(time.Now().Add(19800 * time.Second))
		t0 := wfNowMs()
		app.API.Call(t, "heartbeat", A{"body": hb(ahead)})
		setup := admin.API.Call(t, "getSetup", A{"deploymentId": id})
		site = setup.At("sites").Find("siteSlug", "app")
		seen := wfMs(t, site.At("agent.lastSeenAt").Str())
		True(t, seen >= t0-1000 && seen <= wfNowMs()+1000, "lastSeenAt is the server receive time (%s), never the agent clock (%s)", site.At("agent.lastSeenAt").Str(), ahead)
		True(t, math.Abs(site.At("agent.clockSkew.skewSec").Num()-19800) <= 5, "%s", site.At("agent.clockSkew").String())
		True(t, math.Abs(float64(wfMs(t, site.At("agent.clockSkew.measuredAt").Str())-seen)) < 2000)
		Eq(t, site.At("agent.online"), true)
		Match(t, warning(site.At("agent.clockSkew.skewSec")), `^agent clock is 5 h 30 min ahead — fix NTP on the server`)
		st := admin.API.Call(t, "getStatus", A{"deploymentId": id})
		True(t, math.Abs(st.At("sites").Find("siteSlug", "app").At("clockSkew.skewSec").Num()-19800) <= 5)
		// the stored doc: lastSeenAt server time, clockSkew separate
		Eq(t, Doc(t, "deployments/"+id+"/agents/app").At("lastSeenAt"), site.At("agent.lastSeenAt"))
		// behind by 3 min → negative skew; an in-sync clock → |skew| small, no warning
		app.API.Call(t, "heartbeat", A{"body": hb(ISO(time.Now().Add(-180 * time.Second)))})
		site = wfSite(t, admin, id, "app")
		True(t, math.Abs(site.At("agent.clockSkew.skewSec").Num()+180) <= 5)
		Match(t, warning(site.At("agent.clockSkew.skewSec")), `behind`)
		app.API.Call(t, "heartbeat", A{"body": hb(Now())})
		site = wfSite(t, admin, id, "app")
		True(t, math.Abs(site.At("agent.clockSkew.skewSec").Num()) <= 5)
		Eq(t, warning(site.At("agent.clockSkew.skewSec")), nil)
		// without agentTime the last measurement is kept
		app.API.Call(t, "heartbeat", A{"body": hb()})
		True(t, wfSite(t, admin, id, "app").At("agent.clockSkew").V != nil)
		// bad shapes → 422
		for _, bad := range []any{"yesterday", "2026-10-08 05:00:00", 12345, "2026-13-40T99:99:99Z"} {
			e := app.API.Rejects(t, "heartbeat", A{"body": hb(bad)}, "invalid", 422)
			Match(t, e.Details.String(), `agentTime`)
		}
	})
}
