package apitest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

// wbWith is {...j, ...over}.
func wbWith(j J, over A) J {
	c := j.Clone()
	for k, v := range over {
		c.Map()[k] = Plain(v)
	}
	return c
}

// wbHeaders is {...a, ...b, ...}.
func wbHeaders(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// wbSetVar is vars.map((v) => (v.variableSlug === slug ? { ...v, value } : v)).
func wbSetVar(vars J, slug, value string) L {
	out := L{}
	for _, v := range vars.Arr() {
		if v.At("variableSlug").Str() == slug {
			v = wbWith(v, A{"value": value})
		}
		out = append(out, v.V)
	}
	return out
}

// wbCanon is canonicalize(j).
func wbCanon(t testing.TB, j J) string {
	t.Helper()
	s, err := canonical.CanonicalizeJSON([]byte(j.String()))
	NoErr(t, err)
	return s
}

// wbCursor is Number(cursor).
func wbCursor(t testing.TB, j J) int {
	t.Helper()
	n, err := strconv.Atoi(textOf(j))
	NoErr(t, err)
	return n
}

// wbMakeActivity is makeActivity() of 05-agent.test.ts. prevHash: nil or a string.
func wbMakeActivity(t testing.TB, deploymentID, siteSlug string, seq int, prevHash any) J {
	t.Helper()
	base := JOf(A{
		"activityId": fmt.Sprintf("%s:%d", siteSlug, seq), "deploymentId": deploymentID, "siteSlug": siteSlug, "seq": seq,
		"ts": ISO(time.Now().Add(time.Duration(seq) * time.Millisecond)), "actor": "ops@example.com", "origin": "agent",
		"type": "actor.declared", "summary": fmt.Sprintf("entry %d", seq), "data": A{"n": seq, "nested": L{L{1, 2}, A{"a": nil}}}, "prevHash": prevHash,
	})
	in, err := shared.ActivityHashInput(toMap(t, base))
	NoErr(t, err)
	return wbWith(base, A{"hash": "sha256:" + Sha256Hex(in), "uploadedVia": nil, "uploadedAt": nil})
}

// wbChain is chain().
func wbChain(t testing.TB, deploymentID, siteSlug string, from, to int, prev any) []J {
	t.Helper()
	out := []J{}
	p := prev
	for s := from; s <= to; s++ {
		a := wbMakeActivity(t, deploymentID, siteSlug, s, p)
		out = append(out, a)
		p = a.At("hash").V
	}
	return out
}

// wbOpmlog is opmlog().
func wbOpmlog(t testing.TB, deploymentID, signerSlug string, key SiteKey, entries []J, tamperSig ...bool) []byte {
	t.Helper()
	var nd bytes.Buffer
	for _, e := range entries {
		nd.WriteString(wbCanon(t, e) + "\n")
	}
	sites := L{}
	seen := map[string]bool{}
	for _, e := range entries {
		s := e.At("siteSlug").Str()
		if seen[s] {
			continue
		}
		seen[s] = true
		var l []J
		for _, x := range entries {
			if x.At("siteSlug").Str() == s {
				l = append(l, x)
			}
		}
		sites = append(sites, A{"siteSlug": s, "fromSeq": l[0].At("seq").V, "toSeq": l[len(l)-1].At("seq").V, "count": len(l)})
	}
	manifest := JOf(A{
		"format": "opmlog/1", "deploymentId": deploymentID, "createdAt": Now(), "createdBy": "ops@example.com",
		"signedBy": A{"siteSlug": signerSlug, "publicKey": key.PublicKey}, "sites": sites,
		"activities": A{"path": "activities.ndjson", "sizeBytes": nd.Len(), "sha256": "sha256:" + Sha256Hex(nd.Bytes())}, "debug": L{},
	})
	mb := []byte(wbCanon(t, manifest))
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(key.Priv, mb))
	if len(tamperSig) > 0 && tamperSig[0] {
		sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key.Priv, []byte("x")))
	}
	out, err := core.WriteTar([]core.TarEntry{
		{Name: "manifest.json", Data: mb},
		{Name: "activities.ndjson", Data: nd.Bytes()},
		{Name: "signature.sig", Data: []byte(sig)},
	})
	NoErr(t, err)
	return out
}

// wbStored is the "stored" list of the opmlog tests: the site's activities by seq, upload fields cleared.
func wbStored(t testing.TB, admin *Person, id string) []J {
	t.Helper()
	items := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"site": "app"}}).At("items").Arr()
	sort.SliceStable(items, func(i, j int) bool { return items[i].At("seq").Num() < items[j].At("seq").Num() })
	out := []J{}
	for _, a := range items {
		out = append(out, wbWith(a, A{"uploadedVia": nil, "uploadedAt": nil}))
	}
	return out
}

// Port of backend/test/05-agent.test.ts.
func TestAgent(t *testing.T) {
	t.Run("agent lifecycle: install → enroll → signed calls → poll → staging → head → activities → relay → revoke", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		var app, db *Enrolled
		enrolled := func(t *testing.T) {
			t.Helper()
			if app == nil || db == nil {
				t.Fatal("the enroll subtest did not complete")
			}
		}

		t.Run("install token + script + binary", func(t *testing.T) {
			admin.API.Rejects(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "nope"}}, "not-found")
			tok := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
			token := tok.At("token").Str()
			auth := map[string]string{"Authorization": "Bearer " + token}
			Match(t, token, `^opi_[A-Za-z0-9_-]{43}$`)
			Eq(t, tok.At("tokenHash"), Sha256Hex(token))
			Eq(t, tok.At("command"), `curl -fsSL -H "Authorization: Bearer `+token+`" `+Base+`/v1/install/script | sudo sh`)
			s := Raw(t, "GET", "/v1/install/script", Opt{Headers: auth})
			Eq(t, s.Status, 200)
			sh := s.Text()
			True(t, strings.HasPrefix(sh, "#!/bin/sh\n# On-Prem Manager agent installer · site app"))
			Contains(t, sh, `API="`+Base+`"; DEP="`+id+`"; SITE="app"; TOKEN="`+token+`"`)
			Contains(t, sh, `RELEASE_PUBKEY="`+ReleaseKey.PublicKeyB64+`"`)
			Contains(t, sh, "ExecStart=/usr/local/bin/opm-agent run --config /etc/opm/agent.json")
			TinyScript(t, Raw(t, "GET", "/v1/install/script", Opt{Headers: map[string]string{"Authorization": "Bearer opi_unknown"}}), `unknown install token`)
			Eq(t, Raw(t, "GET", "/v1/install/script").Status, 401)
			Eq(t, Raw(t, "GET", "/v1/install/binary?os=linux&arch=arm64&version=9.9.9", Opt{Headers: auth}).Status, 404)
			Eq(t, Raw(t, "GET", "/v1/install/binary?os=windows&arch=arm64", Opt{Headers: auth}).Status, 422)
			bin := []byte("#!/bin/sh\necho fake opm-agent\n")
			hex := Sha256Hex(bin)
			base := "agent/0.0.1-test/linux-arm64/opm-agent"
			Bucket.Save(t, base, bin)
			Bucket.Save(t, base+".sha256", hex+"\n")
			Bucket.Save(t, base+".sig", cloud.SignHex(hex, ReleaseKey.PrivateKeySeedB64)+"\n")
			Bucket.Save(t, "agent/latest", "0.0.1-test\n")
			b := Raw(t, "GET", "/v1/install/binary?os=linux&arch=arm64", Opt{Headers: auth})
			Eq(t, b.Status, 200)
			Eq(t, b.Body, bin)
			Eq(t, b.Header("x-opm-sha256"), hex)
			pk, err := base64.StdEncoding.DecodeString(ReleaseKey.PublicKeyB64)
			NoErr(t, err)
			rsig, err := base64.RawURLEncoding.DecodeString(b.Header("x-opm-signature"))
			NoErr(t, err)
			True(t, ed25519.Verify(pk, []byte(hex), rsig), "release signature verifies")
			rg := Raw(t, "GET", "/v1/install/binary?os=linux&arch=arm64", Opt{Headers: wbHeaders(auth, map[string]string{"Range": "bytes=0-8"})})
			Eq(t, rg.Status, 206)
			Eq(t, rg.Text(), "#!/bin/sh")
			Eq(t, rg.Header("content-range"), fmt.Sprintf("bytes 0-8/%d", len(bin)))
			setup := admin.API.Call(t, "getSetup", A{"deploymentId": id})
			appRow := setup.At("sites").Find("siteSlug", "app")
			Eq(t, appRow.At("tokens.0.state"), "active")
			Eq(t, setup.At("apiBase"), Base)
			// revoke an unused token → the script says so (§21 #91: 200 + tiny script, exit 1)
			admin.API.Call(t, "revokeInstallToken", A{"deploymentId": id, "tokenHash": tok.At("tokenHash")})
			TinyScript(t, Raw(t, "GET", "/v1/install/script", Opt{Headers: auth}), `token revoked`)
			// expired token → gone
			t2 := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
			UpdateDoc(t, "deployments/"+id+"/installTokens/"+t2.At("tokenHash").Str(), A{"expiresAt": ISO(time.Now().Add(-time.Second))})
			TinyScript(t, Raw(t, "GET", "/v1/install/script", Opt{Headers: map[string]string{"Authorization": "Bearer " + t2.At("token").Str()}}), `token expired`)
		})

		t.Run("enroll: consumes the token; reuse → gone; enrolled site → conflict", func(t *testing.T) {
			app = EnrollSite(t, admin, id, "app", EnrollOpt{AgentPort: 7080})
			Eq(t, app.Enroll.At("siteSlug"), "app")
			Eq(t, app.Enroll.At("deploymentId"), id)
			Eq(t, app.Enroll.At("accessKey.version"), 1)
			Match(t, app.Enroll.At("accessKey.hash"), `^\$argon2id\$`)
			Eq(t, app.Enroll.At("peers").Pluck("siteSlug"), L{"app"})
			again := Bearer(app.Token)
			again.Rejects(t, "enroll", A{"body": A{"publicKey": NewSiteKey().PublicKey, "agentVersion": "1", "hostname": "h", "ip": "1.2.3.4", "advertiseHost": "1.2.3.4", "agentPort": 7080, "os": "linux", "arch": "arm64"}}, "gone", 410)
			admin.API.Rejects(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}}, "conflict")
			bad := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "db"}})
			Bearer(bad.At("token").Str()).Rejects(t, "enroll", A{"body": A{"publicKey": "short", "agentVersion": "1", "hostname": "h", "ip": "", "advertiseHost": "x", "agentPort": 7081, "os": "linux", "arch": "arm64"}}, "invalid")
			admin.API.Call(t, "revokeInstallToken", A{"deploymentId": id, "tokenHash": bad.At("tokenHash")})
			db = EnrollSite(t, admin, id, "db", EnrollOpt{AgentPort: 7081})
			Eq(t, db.Enroll.At("peers").Pluck("siteSlug"), L{"app", "db"})
			acts := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "install.enrolled"}})
			Eq(t, acts.At("items").Len(), 2)
			setup := admin.API.Call(t, "getSetup", A{"deploymentId": id})
			row := setup.At("sites").Find("siteSlug", "app")
			Eq(t, row.At("agent.hostname"), "host-app")
			Eq(t, row.At("tokens").Find("state", "used").At("siteSlug"), "app")
		})

		t.Run("signed requests: clock, nonce replay, bad signature, unknown site, tampered body", func(t *testing.T) {
			enrolled(t)
			path := "/v1/agent/poll?wait=0"
			h1 := SignHeaders(app.Key, id, "app", "GET", path, nil)
			ok := Raw(t, "GET", path, Opt{Headers: h1})
			Eq(t, ok.Status, 200, ok.Text())
			// §21 #86 add-on: the agent GUI links to the cloud console and shows the deployment name
			_, isStr := ok.J.At("deploymentLabel").V.(string)
			Eq(t, isStr, true)
			True(t, ok.J.At("deploymentLabel").Len() > 0)
			if cu := ok.J.At("consoleUrl"); !cu.IsNull() {
				Match(t, cu.Str(), `^https?://`)
			}
			True(t, app.Enroll.Has("consoleUrl") && app.Enroll.Has("deploymentLabel"))
			replay := Raw(t, "GET", path, Opt{Headers: h1})
			Eq(t, replay.Status, 401)
			Eq(t, replay.J.At("error.details.reason"), "nonce")
			clock := Raw(t, "GET", path, Opt{Headers: SignHeaders(app.Key, id, "app", "GET", path, nil, SignOpt{Ts: time.Now().Unix() - 120})})
			Eq(t, clock.J.At("error.details.reason"), "clock")
			True(t, clock.Header("x-opm-server-time") != "")
			other := NewSiteKey()
			sig := Raw(t, "GET", path, Opt{Headers: SignHeaders(other, id, "app", "GET", path, nil)})
			Eq(t, sig.J.At("error.details.reason"), "signature")
			unk := Raw(t, "GET", path, Opt{Headers: SignHeaders(app.Key, id, "zzz", "GET", path, nil)})
			Eq(t, unk.J.At("error.details.reason"), "unknown-site")
			unkDep := Raw(t, "GET", path, Opt{Headers: SignHeaders(app.Key, "NoSuchDeployment0001", "app", "GET", path, nil)})
			Eq(t, unkDep.J.At("error.details.reason"), "unknown-site")
			body := []byte(`{"siteSlug":"app"}`)
			hb := SignHeaders(app.Key, id, "app", "POST", "/v1/agent/heartbeat", body)
			tampered := Raw(t, "POST", "/v1/agent/heartbeat", Opt{Headers: wbHeaders(hb, map[string]string{"Content-Type": "application/json"}), Body: []byte(`{"siteSlug":"db"}`)})
			Eq(t, tampered.J.At("error.details.reason"), "signature")
			// path with a different query is a different string
			hq := SignHeaders(app.Key, id, "app", "GET", "/v1/agent/poll?wait=0", nil)
			q2 := Raw(t, "GET", "/v1/agent/poll?wait=1", Opt{Headers: hq})
			Eq(t, q2.J.At("error.details.reason"), "signature")
		})

		t.Run("poll: immediate answer, long-poll wakes on publish, holds when nothing changes", func(t *testing.T) {
			enrolled(t)
			v0 := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
			Eq(t, v0.At("deploymentId"), id)
			Eq(t, v0.At("source"), "cloud")
			Eq(t, v0.At("deleted"), false)
			Eq(t, v0.At("draftHead"), nil)
			Eq(t, v0.At("accessKey.version"), 1)
			Eq(t, v0.At("peers").Pluck("siteSlug"), L{"app", "db"})
			t0 := time.Now()
			held := app.API.Call(t, "poll", A{"query": A{"cursor": v0.At("cursor"), "wait": 2}})
			True(t, time.Since(t0) >= 1800*time.Millisecond, "held about 2 s")
			Eq(t, held.At("cursor"), v0.At("cursor"))
			type polled struct {
				j J
				e *APIError
			}
			waiting := make(chan polled, 1)
			go func() {
				j, e := app.API.Try("poll", A{"query": A{"cursor": v0.At("cursor"), "wait": 20}})
				waiting <- polled{j, e}
			}()
			Sleep(1500)
			// §21 #104: a commit of the working copy is the release (compat proposal for these 0.1.0-test agents)
			d0 := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
			v1 := wbSetVar(d0.At("draft.variables"), "greeting", "Hello release 1")
			w1 := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": d0.At("draftVersion"), "items": v1}})
			pub := Publish(t, admin.API, id, w1.At("draftVersion"), "release 1")
			t1 := time.Now()
			r := <-waiting
			if r.e != nil {
				t.Fatalf("poll: %v", r.e)
			}
			woke := r.j
			True(t, time.Since(t1) < 5*time.Second, "answered soon after the change")
			NotEq(t, woke.At("cursor"), v0.At("cursor"))
			Eq(t, woke.At("proposals.0.revId"), pub.Proposal.At("revId"))
			Eq(t, woke.At("proposals.0.status"), "published")
			Eq(t, woke.At("branch.head.commitId"), pub.Commit.At("commitId"), "§21 #104 the cloud view carries the deployment head")
			Eq(t, woke.At("branch.recent").Len(), 2)
		})

		var p1, r1 J
		t.Run("proposal fetch (plain secrets + secret.fetched), staging reports → derived status", func(t *testing.T) {
			enrolled(t)
			view := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
			got := app.API.Call(t, "getAgentProposal", A{"revId": view.At("proposals.0.revId")})
			p1 = got.At("proposal")
			r1 = got.At("revision")
			Match(t, r1.At("config.secrets.0.value"), `^[A-Za-z0-9]{24}$`, "plain secret for agents")
			acts := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "secret.fetched"}})
			Eq(t, acts.At("items.0.data.siteSlug"), "app")
			Eq(t, acts.At("items.0.data.secretSlugs"), L{"api-token"})
			rev := p1.At("revId")
			report := func(c *Client, body A) J {
				t.Helper()
				return c.Call(t, "reportProposalStatus", A{"revId": rev, "body": body})
			}
			app.API.Rejects(t, "reportProposalStatus", A{"revId": rev, "body": A{"siteSlug": "db", "staging": "ready"}}, "invalid")
			s1 := report(app.API, A{"siteSlug": "app", "staging": "downloading", "progress": 0.5})
			Eq(t, s1.At("proposal.status"), "staging")
			Eq(t, s1.At("proposal.sites.app.progress"), 0.5)
			s2 := report(app.API, A{"siteSlug": "app", "staging": "ready"})
			Eq(t, s2.At("proposal.status"), "staging")
			s3 := report(db.API, A{"siteSlug": "db", "staging": "ready"})
			Eq(t, s3.At("proposal.status"), "ready")
			s4 := report(db.API, A{"siteSlug": "db", "staging": "failed", "error": "disk full"})
			Eq(t, s4.At("proposal.status"), "staging", "ready → staging when a site falls back")
			report(db.API, A{"siteSlug": "db", "staging": "ready"})
			a := report(app.API, A{"siteSlug": "app", "status": "approved", "approvedBy": "ops@example.com", "approvedAt": Now(), "updateId": ulid.New()})
			Eq(t, a.At("proposal.status"), "approved")
			// LOCK failed → back to ready with result
			lf := report(app.API, A{"siteSlug": "app", "result": A{"ok": false, "message": "lock refused by db", "snapshotId": nil}})
			Eq(t, lf.At("proposal.status"), "ready")
			report(app.API, A{"siteSlug": "app", "status": "approved", "approvedBy": "ops@example.com"})
			ap := report(app.API, A{"siteSlug": "app", "status": "applying"})
			Eq(t, ap.At("proposal.status"), "applying")
			// §21 #104: nothing to commit → invalid V_NO_CHANGE (a running update never blocks a commit; it only skips the compat proposal)
			admin.Real.Rejects(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "x", "baseCommitId": admin.Real.Call(t, "getDeployment", A{"deploymentId": id}).At("branch.head.commitId"), "config": DepInput(t, admin.Real, id)}}, "invalid")
			app.API.Rejects(t, "reportProposalStatus", A{"revId": "01ZZZZZZZZZZZZZZZZZZZZZZZZ", "body": A{"siteSlug": "app", "staging": "ready"}}, "not-found")
		})

		t.Run("reportHead: verify hashes, store, head moves, proposal applied, others stale, fork conflict", func(t *testing.T) {
			enrolled(t)
			app.API.Rejects(t, "reportHead", A{"body": A{"revision": wbWith(r1, A{"hash": "sha256:" + strings.Repeat("0", 64)})}}, "invalid")
			app.API.Rejects(t, "reportHead", A{"body": A{"revision": wbWith(r1, A{"parentRevId": ulid.New(), "seq": 2})}}, "invalid")
			r := app.API.Call(t, "reportHead", A{"body": A{"revision": r1}})
			Eq(t, r, A{"stored": false, "headChanged": true})
			again := db.API.Call(t, "reportHead", A{"body": A{"revision": r1}})
			Eq(t, again, A{"stored": false, "headChanged": false})
			d := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
			Eq(t, d.At("head.revId"), r1.At("revId"))
			Eq(t, d.At("head.seq"), 1)
			props := admin.API.Call(t, "listProposals", A{"deploymentId": id})
			Eq(t, props.At("items").Find("revId", r1.At("revId")).At("status"), "applied")
			// a stale proposal: publish P2 (parent = head), and a local fork at seq 2 wins → P2 stale
			vars := wbSetVar(admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.variables"), "theme", "yellow")
			w := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": d.At("draftVersion"), "items": vars}})
			p2 := Publish(t, admin.API, id, w.At("draftVersion"), "release 2")
			localCfg := wbWith(r1.At("config"), A{"variables": wbSetVar(r1.At("config.variables"), "greeting", "Hi from db")})
			local := wbWith(r1, A{"revId": ulid.New(), "seq": 2, "parentRevId": r1.At("revId"), "origin": "agent", "actor": "ops@example.com", "createdAt": Now(), "config": localCfg, "hash": "sha256:" + Sha256Hex(wbCanon(t, localCfg)), "summary": "local greeting", "kind": "change"})
			pending := A{"staging": "pending", "progress": 0, "error": nil, "updatedAt": nil}
			lp := wbWith(p1, A{"revId": local.At("revId"), "parentRevId": r1.At("revId"), "seq": 2, "summary": local.At("summary"), "origin": "agent", "status": "published", "createdBy": "ops@example.com", "sites": A{"app": pending, "db": pending}, "approvedBy": nil, "approvedAt": nil, "updateId": nil, "result": nil, "supersededBy": nil})
			Eq(t, db.API.Call(t, "registerLocalProposal", A{"body": A{"proposal": lp, "revision": local}}), A{"stored": true})
			Eq(t, db.API.Call(t, "registerLocalProposal", A{"body": A{"proposal": lp, "revision": local}}), A{"stored": false})
			list := admin.API.Call(t, "listProposals", A{"deploymentId": id})
			Eq(t, list.At("items").Find("revId", p2.Proposal.At("revId")).At("status"), "superseded", "the newer local proposal supersedes the cloud one")
			Eq(t, list.At("items").Find("revId", local.At("revId")).At("origin"), "agent")
			h2 := db.API.Call(t, "reportHead", A{"body": A{"revision": local}})
			Eq(t, h2, A{"stored": false, "headChanged": true})
			// fork: another seq-2 revision
			fork := wbWith(local, A{"revId": ulid.New(), "summary": "fork"})
			app.API.Rejects(t, "reportHead", A{"body": A{"revision": fork}}, "conflict")
			// stale: a published proposal whose parent is no longer the head
			d3 := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
			vars2 := wbSetVar(d3.At("draft.variables"), "theme", "blue")
			w3 := admin.API.Call(t, "putDraftSection", A{"deploymentId": id, "section": "variables", "body": A{"ifRevision": d3.At("draftVersion"), "items": vars2}})
			p3 := Publish(t, admin.API, id, w3.At("draftVersion"), "release 3")
			Eq(t, p3.Revision.At("seq"), 3)
			app.API.Call(t, "getAgentRevision", A{"revId": p3.Proposal.At("revId")})
			cfg4 := wbWith(local.At("config"), A{"variables": wbSetVar(local.At("config.variables"), "greeting", "rollback")})
			r4 := wbWith(local, A{"revId": ulid.New(), "seq": 3, "parentRevId": local.At("revId"), "kind": "rollback", "rollbackOf": A{"snapshotId": ulid.New(), "seq": 1}, "config": cfg4, "hash": "sha256:" + Sha256Hex(wbCanon(t, cfg4)), "summary": "rollback to X (seq 1)"})
			Eq(t, app.API.Call(t, "reportHead", A{"body": A{"revision": r4}}).At("headChanged"), true)
			list2 := admin.API.Call(t, "listProposals", A{"deploymentId": id})
			Eq(t, list2.At("items").Find("revId", p3.Proposal.At("revId")).At("status"), "stale")
			stale := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"type": "proposal.stale"}})
			Eq(t, stale.At("items.0.data.revId"), p3.Proposal.At("revId"))
			Eq(t, admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("head.seq"), 3)
			// §21 #104: the pre-0.14 sites' agent-branch mirrors follow their reported heads; the local proposal is a legacy commit
			ab := admin.API.Call(t, "listAgentBranches", A{"deploymentId": id})
			Eq(t, ab.At("items").Find("siteSlug", "app").At("head.commitId"), r4.At("revId"))
			for _, x := range ab.At("items").Arr() {
				True(t, x.At("needsAgent014").Truthy() && x.At("legacy").Truthy())
			}
			lc := admin.API.Call(t, "listAgentBranchCommits", A{"deploymentId": id, "siteSlug": "db"})
			Eq(t, lc.At("items").Find("commitId", local.At("revId")).At("kind"), "legacy")
		})

		t.Run("image manifest + blobs: only head / open proposal content; Range; file blobs", func(t *testing.T) {
			enrolled(t)
			m := app.API.Call(t, "getImageManifest", A{"serviceSlug": "redis", "version": "v1"})
			Eq(t, m.At("config.sizeBytes"), 1234)
			Eq(t, m.At("layers").Len(), 1)
			app.API.Rejects(t, "getImageManifest", A{"serviceSlug": "redis", "version": "other"}, "forbidden")
			banner := admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("draft.files.0.content.sha256")
			fb := app.API.Fetch(t, "getAgentBlob", A{"sha256": banner})
			Eq(t, fb.Text(), "Welcome to the hello-world deployment\n")
			digest := m.At("layers.0.digest").Str()
			layerHex := digest[7:]
			Bucket.Delete(t, "blobs/sha256/"+layerHex)
			app.API.Rejects(t, "getAgentBlob", A{"sha256": digest}, "not-found")
			layer := bytes.Repeat([]byte{7}, 5000)
			Bucket.Save(t, "blobs/sha256/"+layerHex, layer)
			lb := app.API.Fetch(t, "getAgentBlob", A{"sha256": layerHex})
			Eq(t, len(lb.Body), 5000)
			p := "/v1/agent/blobs/sha256:" + layerHex
			rg := Raw(t, "GET", p, Opt{Headers: wbHeaders(SignHeaders(app.Key, id, "app", "GET", p, nil), map[string]string{"Range": "bytes=4990-"})})
			Eq(t, rg.Status, 206)
			Eq(t, len(rg.Body), 10)
			Eq(t, rg.Header("accept-ranges"), "bytes")
			app.API.Rejects(t, "getAgentBlob", A{"sha256": "sha256:" + strings.Repeat("a", 64)}, "forbidden")
			app.API.Rejects(t, "getAgentBlob", A{"sha256": "zz"}, "invalid")
		})

		t.Run("heartbeat: liveness, uptime beats, status online; advertise change bumps agentEpoch", func(t *testing.T) {
			enrolled(t)
			hb := JOf(A{"siteSlug": "app", "agentVersion": "0.1.0-test", "headRevId": nil, "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0.1, "memPct": 0.2, "diskPct": 0.3}, "edge": nil, "keyVersion": 1, "advertiseHost": "127.0.0.1", "agentPort": 7080, "ip": "127.0.0.1", "hostname": "host-app"})
			app.API.Rejects(t, "heartbeat", A{"body": wbWith(hb, A{"siteSlug": "db"})}, "invalid")
			before := app.API.Call(t, "poll", A{"query": A{"wait": 0}}).At("cursor")
			r := app.API.Call(t, "heartbeat", A{"body": hb})
			Eq(t, r.At("ok"), true)
			Eq(t, app.API.Call(t, "poll", A{"query": A{"wait": 0}}).At("cursor"), before, "no epoch change")
			app.API.Call(t, "heartbeat", A{"body": wbWith(hb, A{"advertiseHost": "10.0.0.5"})})
			Eq(t, wbCursor(t, app.API.Call(t, "poll", A{"query": A{"wait": 0}}).At("cursor")), wbCursor(t, before)+1)
			st := admin.API.Call(t, "getStatus", A{"deploymentId": id})
			s := st.At("sites").Find("siteSlug", "app")
			Eq(t, s.At("online"), true)
			Eq(t, s.At("enrolled"), true)
			Eq(t, s.At("metrics"), A{"cpu": 0.1, "memPct": 0.2, "diskPct": 0.3})
			up := st.At("uptime").Find("siteSlug", "app")
			Eq(t, up.At("days").Len(), 30)
			today := up.At("days.-1")
			h := time.Now().UTC().Hour()
			True(t, math.Abs(today.At("hours."+Itoa(h)).Num()-2.0/120) < 1e-9)
			True(t, today.At("onlineFraction").Num() > 0)
			Eq(t, up.At("days.0.onlineFraction"), 0)
		})

		t.Run("activities pipe: contiguous, gap → invalid, skip existing, broken chain → 422", func(t *testing.T) {
			enrolled(t)
			ch := wbChain(t, id, "app", 1, 3, nil)
			r := app.API.Call(t, "uploadAgentActivities", A{"body": A{"siteSlug": "app", "entries": ch}})
			Eq(t, L{r.At("ackSeq"), r.At("inserted"), r.At("skipped"), r.At("rejected")}, L{3, 3, 0, 0})
			again := app.API.Call(t, "uploadAgentActivities", A{"body": A{"siteSlug": "app", "entries": ch}})
			Eq(t, L{again.At("ackSeq"), again.At("inserted"), again.At("skipped")}, L{3, 0, 3})
			gap := wbChain(t, id, "app", 5, 6, "sha256:"+strings.Repeat("1", 64))
			g := app.API.Rejects(t, "uploadAgentActivities", A{"body": A{"siteSlug": "app", "entries": gap}}, "invalid")
			Match(t, g.Message, `missing seq 4`)
			app.API.Rejects(t, "uploadAgentActivities", A{"body": A{"siteSlug": "db", "entries": L{}}}, "invalid")
			next := wbChain(t, id, "app", 4, 6, ch[2].At("hash").V)
			next[1] = wbWith(next[1], A{"summary": "tampered"})
			br := app.API.Rejects(t, "uploadAgentActivities", A{"body": A{"siteSlug": "app", "entries": next}}, "invalid")
			Eq(t, br.Details.At("inserted"), 1)
			Eq(t, br.Details.At("rejected"), 2)
			Eq(t, br.Details.At("ackSeq"), 4)
			list := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"site": "app", "via": "pipe"}})
			Eq(t, list.At("items").Len(), 4)
			Eq(t, list.At("items").Pluck("seq"), L{4, 3, 2, 1})
			Eq(t, list.At("items.3.data"), A{"n": 1, "nested": L{L{1, 2}, A{"a": nil}}}, "nested arrays round-trip")
			Eq(t, list.At("items.0.uploadedVia"), "pipe")
			byActor := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"actor": "ops@example.com", "site": "app", "type": "actor."}})
			Eq(t, byActor.At("items").Len(), 4)
			pg := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"limit": 2}})
			Eq(t, pg.At("items").Len(), 2)
			pg2 := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"limit": 2, "cursor": pg.At("cursor")}})
			for _, a := range pg2.At("items").Arr() {
				False(t, pg.At("items").Find("activityId", a.At("activityId")).Exists())
			}
			admin.API.Rejects(t, "listActivities", A{"deploymentId": id, "query": A{"origin": "martian"}}, "invalid")
		})

		t.Run(".opmlog manual upload: signature, chain, skip / insert / reject", func(t *testing.T) {
			enrolled(t)
			// seq 1..3 equal the piped ones? they differ (new ts) → use the stored ones for 1..4 and continue
			stored := wbStored(t, admin, id)
			more := wbChain(t, id, "app", 5, 6, stored[len(stored)-1].At("hash").V)
			bundle := wbOpmlog(t, id, "app", app.Key, append(append([]J{}, stored...), more...))
			res := admin.API.Call(t, "uploadActivities", A{"deploymentId": id, "body": bundle})
			Eq(t, L{res.At("inserted"), res.At("skipped"), res.At("rejected"), res.At("signatureOk")}, L{2, 4, 0, true})
			Eq(t, res.At("sites"), L{A{"siteSlug": "app", "fromSeq": 1, "toSeq": 6}})
			tamper := []J{more[0], wbWith(more[1], A{"summary": "changed"})}
			res2 := admin.API.Call(t, "uploadActivities", A{"deploymentId": id, "body": wbOpmlog(t, id, "app", app.Key, append(append([]J{}, stored...), tamper...))})
			True(t, res2.At("rejected").Num() >= 1)
			admin.API.Rejects(t, "uploadActivities", A{"deploymentId": id, "body": wbOpmlog(t, id, "app", app.Key, stored, true)}, "invalid")
			admin.API.Rejects(t, "uploadActivities", A{"deploymentId": id, "body": wbOpmlog(t, id, "app", NewSiteKey(), stored)}, "invalid")
			admin.API.Rejects(t, "uploadActivities", A{"deploymentId": id, "body": []byte("not a tar file at all")}, "invalid")
			manual := admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"via": "manual"}})
			Eq(t, manual.At("items").Len(), 2)
			files, err := Bucket.B.List("activity-bundles/" + id + "/")
			NoErr(t, err)
			True(t, len(files) >= 2)
		})

		t.Run("relay: db relays app heartbeats; inner signature checked; relayVia set", func(t *testing.T) {
			enrolled(t)
			hb := A{"siteSlug": "app", "agentVersion": "0.1.0-test", "headRevId": nil, "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0, "memPct": 0, "diskPct": 0}, "edge": nil, "keyVersion": 1, "advertiseHost": "10.0.0.5", "agentPort": 7080, "ip": "127.0.0.1", "hostname": "host-app"}
			body := []byte(show(hb))
			ctype := map[string]string{"Content-Type": "application/json"}
			inner := JOf(A{"method": "POST", "path": "/v1/agent/heartbeat", "headers": wbHeaders(SignHeaders(app.Key, id, "app", "POST", "/v1/agent/heartbeat", body), ctype), "bodyB64": base64.StdEncoding.EncodeToString(body)})
			b64 := func(j J) []byte {
				t.Helper()
				b, err := base64.StdEncoding.DecodeString(j.Str())
				NoErr(t, err)
				return b
			}
			rr := db.API.Call(t, "relay", A{"body": A{"forSite": "app", "request": inner}})
			Eq(t, rr.At("status"), 200, string(b64(rr.At("bodyB64"))))
			Eq(t, ParseJ(b64(rr.At("bodyB64"))).At("ok"), true)
			st := admin.API.Call(t, "getStatus", A{"deploymentId": id})
			Eq(t, st.At("sites").Find("siteSlug", "app").At("relayVia"), "db")
			replay := db.API.Call(t, "relay", A{"body": A{"forSite": "app", "request": inner}})
			Eq(t, replay.At("status"), 401, "inner nonce replay")
			forged := wbWith(inner, A{"headers": wbHeaders(SignHeaders(db.Key, id, "app", "POST", "/v1/agent/heartbeat", body), ctype)})
			Eq(t, db.API.Call(t, "relay", A{"body": A{"forSite": "app", "request": forged}}).At("status"), 401)
			db.API.Rejects(t, "relay", A{"body": A{"forSite": "db", "request": inner}}, "invalid")
			db.API.Rejects(t, "relay", A{"body": A{"forSite": "app", "request": wbWith(inner, A{"path": "/v1/me"})}}, "invalid")
			// a relayed poll
			pp := "/v1/agent/poll?wait=0"
			pr := db.API.Call(t, "relay", A{"body": A{"forSite": "app", "request": A{"method": "GET", "path": pp, "headers": SignHeaders(app.Key, id, "app", "GET", pp, nil), "bodyB64": nil}}})
			Eq(t, pr.At("status"), 200)
			Eq(t, ParseJ(b64(pr.At("bodyB64"))).At("deploymentId"), id)
			// direct call clears relayVia
			app.API.Call(t, "poll", A{"query": A{"wait": 0}})
			st2 := admin.API.Call(t, "getStatus", A{"deploymentId": id})
			Eq(t, st2.At("sites").Find("siteSlug", "app").At("relayVia"), nil)
		})

		t.Run("access key rotate → cloud view version 2; revoke → key stops working; deleted → poll says deleted", func(t *testing.T) {
			enrolled(t)
			before := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
			rot := admin.API.Call(t, "rotateAccessKey", A{"deploymentId": id})
			Eq(t, rot.At("version"), 2)
			Match(t, rot.At("accessKey"), `^[A-Z2-7]{4}(-[A-Z2-7]{4}){5}$`)
			after := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
			Eq(t, after.At("accessKey.version"), 2)
			NotEq(t, after.At("accessKey.hash"), before.At("accessKey.hash"))
			Eq(t, wbCursor(t, after.At("cursor")), wbCursor(t, before.At("cursor"))+1)
			admin.API.Rejects(t, "revokeAgent", A{"deploymentId": id, "siteSlug": "app", "body": A{"confirm": "wrong"}}, "invalid")
			admin.API.Call(t, "revokeAgent", A{"deploymentId": id, "siteSlug": "app", "body": A{"confirm": "app"}})
			app.API.Rejects(t, "poll", A{"query": A{"wait": 0}}, "unauthenticated", 401)
			admin.API.Rejects(t, "revokeAgent", A{"deploymentId": id, "siteSlug": "app", "body": A{"confirm": "app"}}, "not-found")
			// a bundle signed with the revoked key still verifies
			stored := wbStored(t, admin, id)
			res := admin.API.Call(t, "uploadActivities", A{"deploymentId": id, "body": wbOpmlog(t, id, "app", app.Key, stored)})
			Eq(t, res.At("signatureOk"), true)
			Eq(t, res.At("inserted"), 0)
			// re-install with a new key
			app2 := EnrollSite(t, admin, id, "app")
			Eq(t, app2.API.Call(t, "poll", A{"query": A{"wait": 0}}).At("peers").Len(), 2)
			d := admin.API.Call(t, "getDeployment", A{"deploymentId": id})
			admin.API.Call(t, "deleteDeployment", A{"deploymentId": id, "body": A{"confirm": d.At("label")}})
			v := db.API.Call(t, "poll", A{"query": A{"wait": 0}})
			Eq(t, v.At("deleted"), true)
			db.API.Rejects(t, "heartbeat", A{"body": A{"siteSlug": "db", "agentVersion": "1", "headRevId": nil, "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0, "memPct": 0, "diskPct": 0}, "edge": nil, "keyVersion": 2, "advertiseHost": "127.0.0.1", "agentPort": 7081, "ip": "", "hostname": ""}}, "unauthenticated")
		})
	})
}
