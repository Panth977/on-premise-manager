package apitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// instUpProgress is progress(): an AgentUpgradeProgress of a request. to is req.version.
func instUpProgress(req J, from, phase string, extra ...A) A {
	now := Now()
	var finished, prog any
	if phase == "done" || phase == "failed" || phase == "rolled-back" {
		finished = now
	}
	if phase == "download" {
		prog = 0.5
	}
	p := A{"upgradeId": "U" + req.At("requestId").Str()[1:], "requestId": req.At("requestId").V, "runId": req.At("runId").V, "via": "cloud", "siteSlug": req.At("siteSlug").V, "from": from, "to": req.At("version").V, "phase": phase, "progress": prog, "bytesDone": 10, "bytesTotal": 20, "verification": nil, "error": nil, "actor": req.At("requestedBy").V, "startedAt": now, "updatedAt": now, "finishedAt": finished}
	for _, e := range extra {
		p = instWith(p, e)
	}
	return p
}

// instUpHb is hb(): upgrade nil = omitted.
func instUpHb(slug, version string, upgrade A) A {
	h := A{"siteSlug": slug, "agentVersion": version, "headRevId": nil, "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0, "memPct": 0, "diskPct": 0}, "edge": nil, "keyVersion": 1, "advertiseHost": "127.0.0.1", "agentPort": 7080, "ip": "127.0.0.1", "hostname": "host-" + slug, "detectedAddresses": L{}}
	if upgrade != nil {
		h["upgrade"] = upgrade
	}
	return h
}

// instFakeSite is fakeSite(): an enrolled site written directly (more sites than the hello config).
func instFakeSite(t testing.TB, depID, slug, agentVersion string) *Client {
	t.Helper()
	key := NewSiteKey()
	now := Now()
	SetDoc(t, "deployments/"+depID+"/agents/"+slug, A{"siteSlug": slug, "publicKey": key.PublicKey, "enrolledAt": now, "agentVersion": agentVersion, "lastSeenAt": now, "ip": "127.0.0.1", "hostname": "host-" + slug, "advertiseHost": "127.0.0.1", "agentPort": 7080, "os": "linux", "arch": "amd64", "relayVia": nil, "headRevId": nil, "staging": nil, "keyVersion": 1, "ackSeq": 0, "metrics": nil, "edge": nil, "peersSeen": L{}, "detectedAddresses": L{}})
	return AgentClient(key, depID, slug)
}

func instParseMs(t testing.TB, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.RFC3339Nano, s)
	NoErr(t, err)
	return d
}

// instOpsFails runs an opm-cloud subcommand that must fail; stdout + stderr.
func instOpsFails(t testing.TB, args ...string) string {
	t.Helper()
	c := exec.Command(CloudBin, args...)
	c.Env = ServerEnv
	b, err := c.CombinedOutput()
	if err == nil {
		t.Fatalf("opm-cloud %s: expected a failure\n%s", strings.Join(args, " "), b)
	}
	return string(b)
}

// Port of backend/test/20-agent-upgrade.test.ts.
func TestAgentUpgrade(t *testing.T) {
	const PIN = "0.10.3-20"
	const NOTES = "Round I release\n- upgrade from the GUI\n- boot guard"
	BIN := []byte("fake opm-agent binary " + PIN + strings.Repeat(" ", 100))

	t.Run("round I: agent upgrades from the cloud console", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		instUploadAgent(t, PIN, BIN)
		instManifest(t, "0.0.1-i", "i", A{"version": PIN, "notes": NOTES})
		app := EnrollSite(t, admin, id, "app")
		db := EnrollSite(t, admin, id, "db")
		agentDoc := func(t testing.TB, slug string) J { return Doc(t, "deployments/"+id+"/agents/"+slug) }
		depDoc := func(t testing.TB) J { return Doc(t, "deployments/"+id) }
		epoch := func(t testing.TB) float64 { return depDoc(t).At("agentEpoch").Num() }
		beat := func(t testing.TB, c *Client, slug, v string, up A) {
			t.Helper()
			c.Call(t, "heartbeat", A{"body": instUpHb(slug, v, up)})
		}
		cloudTypes := func(t testing.TB) []J {
			return admin.API.Call(t, "listActivities", A{"deploymentId": id, "query": A{"site": "_cloud", "type": "agent.", "limit": 500}}).At("items").Arr()
		}
		hasType := func(items []J, fn func(J) bool) bool {
			for _, a := range items {
				if fn(a) {
					return true
				}
			}
			return false
		}
		auditItems := func(t testing.TB) []J {
			return admin.API.Call(t, "listAuditLog", A{"query": A{"target": "deployments/" + id}}).At("items").Arr()
		}
		getSetup := func(t testing.TB) J { return admin.API.Call(t, "getSetup", A{"deploymentId": id}) }
		siteOf := func(s J, slug string) J { return s.At("sites").Find("siteSlug", slug) }
		poll := func(t testing.TB, c *Client) J { return c.Call(t, "poll", A{"query": A{"wait": 0}}) }
		reqBody := func(site, version, confirm string) A {
			return A{"deploymentId": id, "siteSlug": site, "body": A{"version": version, "confirm": confirm}}
		}
		request := func(t testing.TB, site string) J {
			t.Helper()
			return admin.API.Call(t, "requestAgentUpgrade", reqBody(site, PIN, site)).At("request")
		}
		allBody := func(version, confirm string) A {
			return A{"deploymentId": id, "body": A{"version": version, "confirm": confirm}}
		}
		beat(t, app.API, "app", "0.10.0-1", nil)
		beat(t, db.API, "db", "0.7.2-10080232", nil)

		t.Run("release notes + sizeBytes: agentRelease, poll agentRelease, setup agentRelease", func(t *testing.T) {
			r := app.API.Call(t, "agentRelease", A{"query": A{"os": "linux", "arch": "amd64"}})
			Eq(t, r.At("version"), PIN)
			Eq(t, r.At("notes"), NOTES)
			Eq(t, r.At("sizeBytes"), len(BIN))
			view := poll(t, app.API)
			Eq(t, view.At("agentRelease"), A{"version": PIN, "notes": NOTES})
			Eq(t, view.At("agentUpgrades"), L{})
			setup := getSetup(t)
			Eq(t, setup.At("agentRelease"), A{"version": PIN, "notes": NOTES})
			Eq(t, setup.At("agentUpgradeRun"), nil)
			sa := siteOf(setup, "app").At("upgrade")
			Eq(t, sa, A{"installed": "0.10.0-1", "latest": PIN, "available": true, "supported": true, "hint": nil, "request": nil, "progress": nil})
			sd := siteOf(setup, "db").At("upgrade")
			Eq(t, sd.At("supported"), false)
			Eq(t, sd.At("available"), true)
			Match(t, sd.At("hint"), `rerun the install command of this site once`)
		})

		t.Run("stale / invalid (+ hint) / not enrolled / bad confirm", func(t *testing.T) {
			admin.API.Rejects(t, "requestAgentUpgrade", reqBody("app", "0.10.2", "app"), "stale", 409)
			admin.API.Rejects(t, "requestAgentUpgrade", reqBody("app", PIN, "db"), "invalid", 422)
			e := admin.API.Rejects(t, "requestAgentUpgrade", reqBody("db", PIN, "db"), "invalid", 422)
			Match(t, e.Details.At("hint"), `rerun the install command of this site once`)
			admin.API.Rejects(t, "requestAgentUpgrade", reqBody("nope", PIN, "nope"), "invalid", 422)
			ro := NewPerson(t, L{A{"resource": "deployment.setup", "actions": L{"read"}, "scope": A{"kind": "all"}}})
			ro.API.Rejects(t, "requestAgentUpgrade", reqBody("app", PIN, "app"), "forbidden", 403)
		})

		t.Run("queued request → poll agentUpgrades → progress download … done clears it (+ activities, audit)", func(t *testing.T) {
			e0 := epoch(t)
			req := request(t, "app")
			Eq(t, req.At("siteSlug"), "app")
			Eq(t, req.At("version"), PIN)
			Eq(t, req.At("requestedBy"), admin.Email)
			Eq(t, req.At("requestedByUid"), admin.UID)
			Eq(t, req.At("runId"), nil)
			Eq(t, instParseMs(t, req.At("expiresAt").Str()).Sub(instParseMs(t, req.At("requestedAt").Str())), 24*time.Hour)
			True(t, epoch(t) > e0, "agentEpoch +1 so long polls wake up")
			view := poll(t, app.API)
			Eq(t, view.At("agentUpgrades"), L{req})
			// the other site sees it too (each agent filters its own)
			Eq(t, poll(t, db.API).At("agentUpgrades"), L{req})
			admin.API.Rejects(t, "requestAgentUpgrade", reqBody("app", PIN, "app"), "conflict", 409)
			admin.API.Rejects(t, "upgradeAllAgents", allBody(PIN, "UPGRADE"), "conflict", 409)
			setup := getSetup(t)
			Eq(t, siteOf(setup, "app").At("upgrade.request"), req)
			for _, phase := range []string{"download", "verify", "swap", "restart", "health"} {
				beat(t, app.API, "app", "0.10.0-1", instUpProgress(req, "0.10.0-1", phase))
				Eq(t, agentDoc(t, "app").At("upgradeRequest.requestId"), req.At("requestId"), "kept during "+phase)
				Eq(t, agentDoc(t, "app").At("upgrade.phase"), phase)
			}
			st := admin.API.Call(t, "getStatus", A{"deploymentId": id})
			Eq(t, siteOf(st, "app").At("upgrade.progress.phase"), "health")
			e1 := epoch(t)
			// the NEW agent reports done
			beat(t, app.API, "app", PIN, instUpProgress(req, "0.10.0-1", "done"))
			Eq(t, agentDoc(t, "app").At("upgradeRequest"), nil)
			True(t, epoch(t) > e1)
			view = poll(t, app.API)
			Eq(t, view.At("agentUpgrades"), L{})
			setup = getSetup(t)
			u := siteOf(setup, "app").At("upgrade")
			Eq(t, u.At("installed"), PIN)
			Eq(t, u.At("available"), false)
			Eq(t, u.At("request"), nil)
			Eq(t, u.At("progress.phase"), "done")
			Eq(t, u.At("progress.siteSlug"), "app")
			True(t, hasType(cloudTypes(t), func(a J) bool {
				return a.At("type").Str() == "agent.upgrade-requested" && a.At("data.requestId").V == req.At("requestId").V && a.At("data.siteSlug").Str() == "app" && a.At("data.version").Str() == PIN
			}))
			items := auditItems(t)
			var ent J
			found := false
			for _, x := range items {
				if x.At("action").Str() == "deployment.agent-upgrade-requested" && x.At("outcome").Str() == "ok" {
					ent, found = x, true
					break
				}
			}
			True(t, found, JOf(items).String())
			Eq(t, ent.At("op"), "requestAgentUpgrade")
			Eq(t, ent.At("data.requestId"), req.At("requestId"))
			// already on it
			admin.API.Rejects(t, "requestAgentUpgrade", reqBody("app", PIN, "app"), "invalid", 422)
		})

		t.Run("cancel before start; conflict after start; failed clears it; agentVersion reaching the pin clears it", func(t *testing.T) {
			beat(t, app.API, "app", "0.10.1", nil)
			req := request(t, "app")
			cancelArgs := A{"deploymentId": id, "siteSlug": "app"}
			Eq(t, admin.API.Call(t, "cancelAgentUpgrade", cancelArgs), A{"cancelled": true})
			Eq(t, agentDoc(t, "app").At("upgradeRequest"), nil)
			Eq(t, poll(t, app.API).At("agentUpgrades"), L{})
			admin.API.Rejects(t, "cancelAgentUpgrade", cancelArgs, "not-found", 404)
			True(t, hasType(cloudTypes(t), func(a J) bool {
				return a.At("type").Str() == "agent.upgrade-cancelled" && a.At("data.requestId").V == req.At("requestId").V
			}))
			True(t, hasType(auditItems(t), func(x J) bool {
				return x.At("action").Str() == "deployment.agent-upgrade-cancelled" && x.At("op").Str() == "cancelAgentUpgrade" && x.At("data.requestId").V == req.At("requestId").V
			}))
			// started → conflict; failed → cleared
			r2 := request(t, "app")
			beat(t, app.API, "app", "0.10.1", instUpProgress(r2, "0.10.1", "download"))
			admin.API.Rejects(t, "cancelAgentUpgrade", cancelArgs, "conflict", 409)
			admin.API.Rejects(t, "requestAgentUpgrade", reqBody("app", PIN, "app"), "conflict", 409)
			beat(t, app.API, "app", "0.10.1", instUpProgress(r2, "0.10.1", "failed", A{"error": "the pinned release moved to 9.9.9; request again"}))
			Eq(t, agentDoc(t, "app").At("upgradeRequest"), nil)
			Eq(t, agentDoc(t, "app").At("upgrade.error"), "the pinned release moved to 9.9.9; request again")
			// upgraded by other means (CLI / rerun install): the reported version reaching the request clears it
			request(t, "app")
			beat(t, app.API, "app", PIN, nil)
			Eq(t, agentDoc(t, "app").At("upgradeRequest"), nil)
			// omitted upgrade keeps the stored progress
			Eq(t, agentDoc(t, "app").At("upgrade.phase"), "failed")
			// heartbeat validation
			app.API.Rejects(t, "heartbeat", A{"body": instUpHb("app", PIN, instUpProgress(r2, "0.10.1", "done", A{"phase": "exploded"}))}, "invalid", 422)
			app.API.Rejects(t, "heartbeat", A{"body": instUpHb("app", PIN, instUpProgress(r2, "0.10.1", "done", A{"upgradeId": 7}))}, "invalid", 422)
		})

		t.Run("upgrade all: sequential in siteSlug order; A done → B queued; B rolled-back → run failed, C cancelled; old agent skipped", func(t *testing.T) {
			beat(t, app.API, "app", "0.10.0", nil)
			cache := instFakeSite(t, id, "cache", "0.10.1")
			edge := instFakeSite(t, id, "edge", "0.10.0")
			instFakeSite(t, id, "fresh", PIN) // up to date → not in the run
			admin.API.Rejects(t, "upgradeAllAgents", allBody(PIN, "upgrade"), "invalid", 422)
			admin.API.Rejects(t, "upgradeAllAgents", allBody("0.1.0", "UPGRADE"), "stale", 409)
			admin.API.Rejects(t, "cancelAgentUpgradeRun", A{"deploymentId": id}, "not-found", 404)
			run := admin.API.Call(t, "upgradeAllAgents", allBody(PIN, "UPGRADE")).At("run")
			Eq(t, run.At("status"), "running")
			Eq(t, run.At("origin"), "cloud")
			Eq(t, run.At("requestedBy"), admin.Email)
			pairs := func(r J) L {
				o := L{}
				for _, s := range r.At("sites").Arr() {
					o = append(o, L{s.At("siteSlug").V, s.At("status").V})
				}
				return o
			}
			Eq(t, pairs(run), L{L{"app", "requested"}, L{"cache", "pending"}, L{"db", "skipped"}, L{"edge", "pending"}})
			Match(t, run.At("sites.2.error"), `rerun the install command of this site once`)
			Eq(t, run.At("sites.0.from"), "0.10.0")
			admin.API.Rejects(t, "upgradeAllAgents", allBody(PIN, "UPGRADE"), "conflict", 409)
			admin.API.Rejects(t, "requestAgentUpgrade", reqBody("cache", PIN, "cache"), "conflict", 409)
			view := poll(t, app.API)
			ups := view.At("agentUpgrades")
			Eq(t, ups.Len(), 1)
			Eq(t, L{L{ups.At("0.siteSlug"), ups.At("0.runId")}}, L{L{"app", run.At("runId")}})
			reqA := ups.At("0")
			Eq(t, reqA.At("requestId"), run.At("sites.0.requestId"))
			// A starts → running; A done → B queued (only once even when the done heartbeat repeats)
			beat(t, app.API, "app", "0.10.0", instUpProgress(reqA, "0.10.0", "download"))
			setup := getSetup(t)
			Eq(t, setup.At("agentUpgradeRun.sites.0.status"), "running")
			beat(t, app.API, "app", PIN, instUpProgress(reqA, "0.10.0", "done"))
			beat(t, app.API, "app", PIN, instUpProgress(reqA, "0.10.0", "done"))
			view = poll(t, app.API)
			Eq(t, view.At("agentUpgrades").Pluck("siteSlug"), L{"cache"})
			reqB := view.At("agentUpgrades.0")
			Eq(t, reqB.At("runId"), run.At("runId"))
			Eq(t, reqB.At("requestedBy"), admin.Email)
			setup = getSetup(t)
			Eq(t, pairs(setup.At("agentUpgradeRun")), L{L{"app", "done"}, L{"cache", "requested"}, L{"db", "skipped"}, L{"edge", "pending"}})
			Eq(t, setup.At("agentUpgradeRun.sites.1.requestId"), reqB.At("requestId"))
			// B rolled back → run failed, C cancelled, no request left
			beat(t, cache, "cache", "0.10.1", instUpProgress(reqB, "0.10.1", "rolled-back", A{"error": "not healthy within 60 s"}))
			setup = getSetup(t)
			r := setup.At("agentUpgradeRun")
			Eq(t, r.At("status"), "failed")
			True(t, r.At("finishedAt").Truthy())
			Eq(t, pairs(r), L{L{"app", "done"}, L{"cache", "failed"}, L{"db", "skipped"}, L{"edge", "cancelled"}})
			Match(t, r.At("sites.1.error"), `rolled-back: not healthy within 60 s`)
			Eq(t, poll(t, edge).At("agentUpgrades"), L{})
			admin.API.Rejects(t, "cancelAgentUpgradeRun", A{"deploymentId": id}, "conflict", 409)
			True(t, hasType(cloudTypes(t), func(a J) bool {
				return a.At("type").Str() == "agent.upgrade-requested" && a.At("data.runId").V == run.At("runId").V && a.At("data.siteSlug").Str() == "cache"
			}))
			// the deployment view carries the run too
			Eq(t, admin.API.Call(t, "getDeployment", A{"deploymentId": id}).At("agentUpgradeRun.runId"), run.At("runId"))
		})

		t.Run("cancel a run: the queued, unstarted request is removed, remaining sites cancelled; audit", func(t *testing.T) {
			beat(t, app.API, "app", "0.10.0", nil)
			run := admin.API.Call(t, "upgradeAllAgents", allBody(PIN, "UPGRADE")).At("run")
			notSkipped := L{}
			for _, s := range run.At("sites").Arr() {
				if s.At("status").Str() != "skipped" {
					notSkipped = append(notSkipped, s.At("siteSlug").V)
				}
			}
			Eq(t, notSkipped, L{"app", "cache", "edge"})
			c := admin.API.Call(t, "cancelAgentUpgradeRun", A{"deploymentId": id}).At("run")
			Eq(t, c.At("status"), "cancelled")
			Eq(t, c.At("sites").Pluck("status"), L{"cancelled", "cancelled", "skipped", "cancelled"})
			Eq(t, agentDoc(t, "app").At("upgradeRequest"), nil)
			items := auditItems(t)
			True(t, hasType(items, func(x J) bool {
				return x.At("op").Str() == "cancelAgentUpgradeRun" && x.At("action").Str() == "deployment.agent-upgrade-cancelled" && x.At("data.runId").V == run.At("runId").V
			}))
			True(t, hasType(items, func(x J) bool {
				return x.At("op").Str() == "upgradeAllAgents" && x.At("action").Str() == "deployment.agent-upgrade-requested" && x.At("data.runId").V == run.At("runId").V
			}))
			// cancelling the run's queued request itself also cancels the run
			r2 := admin.API.Call(t, "upgradeAllAgents", allBody(PIN, "UPGRADE")).At("run")
			admin.API.Call(t, "cancelAgentUpgrade", A{"deploymentId": id, "siteSlug": "app"})
			s := getSetup(t)
			Eq(t, s.At("agentUpgradeRun.runId"), r2.At("runId"))
			Eq(t, s.At("agentUpgradeRun.status"), "cancelled")
		})

		t.Run("expiry: an expired request is absent on read; heartbeat / daily job fail the run with the expired error", func(t *testing.T) {
			past := instPast()
			appDoc := "deployments/" + id + "/agents/app"
			// single request: expired → not in the poll / setup, a new request is allowed
			request(t, "app")
			UpdateDoc(t, appDoc, A{"upgradeRequest.expiresAt": past})
			Eq(t, poll(t, app.API).At("agentUpgrades"), L{})
			Eq(t, siteOf(getSetup(t), "app").At("upgrade.request"), nil)
			again := request(t, "app")
			// a heartbeat of an agent that never picked it up, after expiry → cleared + agent.upgrade-cancelled reason expired
			UpdateDoc(t, appDoc, A{"upgradeRequest.expiresAt": past})
			beat(t, app.API, "app", "0.10.0", nil)
			Eq(t, agentDoc(t, "app").At("upgradeRequest"), nil)
			True(t, hasType(cloudTypes(t), func(a J) bool {
				return a.At("type").Str() == "agent.upgrade-cancelled" && a.At("data.requestId").V == again.At("requestId").V && a.At("data.reason").Str() == "expired"
			}))
			// run: the current request expires → setup shows the run failed at once; the daily job persists it
			run := admin.API.Call(t, "upgradeAllAgents", allBody(PIN, "UPGRADE")).At("run")
			UpdateDoc(t, appDoc, A{"upgradeRequest.expiresAt": past})
			shown := getSetup(t).At("agentUpgradeRun")
			Eq(t, shown.At("status"), "failed")
			Eq(t, shown.At("sites.0.error"), "expired: the agent did not pick it up within 24 h")
			Eq(t, depDoc(t).At("agentUpgradeRun.status"), "running", "not persisted by a read")
			d := RunDaily(t)
			True(t, d.At("agentUpgradesExpired").Num() >= 1)
			stored := depDoc(t).At("agentUpgradeRun")
			Eq(t, stored.At("runId"), run.At("runId"))
			Eq(t, stored.At("status"), "failed")
			Eq(t, stored.At("sites").Pluck("status"), L{"failed", "cancelled", "skipped", "cancelled"})
			Eq(t, agentDoc(t, "app").At("upgradeRequest"), nil)
		})

		t.Run("status: SiteCloudStatus.upgrade (null when not enrolled)", func(t *testing.T) {
			NoErr(t, DB.Doc("deployments/"+id+"/agents/fresh").Delete())
			st := admin.API.Call(t, "getStatus", A{"deploymentId": id})
			a := siteOf(st, "app").At("upgrade")
			Eq(t, a.At("latest"), PIN)
			Eq(t, a.At("supported"), true)
			Eq(t, siteOf(st, "db").At("upgrade.supported"), false)
			admin.API.Call(t, "revokeAgent", A{"deploymentId": id, "siteSlug": "db", "body": A{"confirm": "db"}})
			st2 := admin.API.Call(t, "getStatus", A{"deploymentId": id})
			Eq(t, siteOf(st2, "db").At("upgrade"), nil)
			Eq(t, siteOf(getSetup(t), "db").At("upgrade"), nil)
		})

		t.Run("used install token of the current enrollment → upgrade-only script with the boot guard + GUI upgrade line", func(t *testing.T) {
			s := Raw(t, "GET", "/v1/install/script", Opt{Headers: map[string]string{"Authorization": "Bearer " + app.Token}})
			Eq(t, s.Status, 200)
			sh := s.Text()
			Contains(t, sh, `UPGRADE_ONLY="1"`)
			Contains(t, sh, `AGENT_VERSION="`+PIN+`"`)
			Contains(t, sh, `GUI_UPGRADE="1"`)
			Contains(t, sh, `upgrade-guard --install --data-dir`)
			Contains(t, sh, `Upgrades from now on: cloud console Setup → Upgrade agent, or the agent GUI Agent page`)
		})

		Bucket.Delete(t, "deps/manifest.json")
	})

	t.Run("upload-deps --agent-notes / --agent-notes-file: written, kept for the same version, cleared for a new one, > 8000 refused", func(t *testing.T) {
		dir := t.TempDir()
		for _, a := range []string{"amd64", "arm64"} {
			instWrite(t, filepath.Join(dir, "docker-0.0.7-"+a+".tgz"), "fake docker "+a, 0o644)
		}
		notesFile := filepath.Join(dir, "notes.md")
		instWrite(t, notesFile, "# 0.10.0\n- from a file\n", 0o644)
		args := func(extra ...string) []string {
			return append([]string{"upload-deps", "--from-dir", dir, "--docker-version", "0.0.7"}, extra...)
		}
		run := func(extra ...string) string { return OpmCloud(t, args(extra...)...) }
		manifest := func() J { return ParseJ(Bucket.Download(t, "deps/manifest.json")) }
		run("--agent-version", "0.10.0-n", "--agent-notes", "inline notes")
		Eq(t, manifest().At("agent"), A{"version": "0.10.0-n", "notes": "inline notes", "schema": nil, "minSchema": nil})
		out := run("--agent-version", "0.10.0-n")
		Match(t, out, `notes 12 chars \(kept\)`)
		Eq(t, manifest().At("agent"), A{"version": "0.10.0-n", "notes": "inline notes", "schema": nil, "minSchema": nil})
		run("--agent-version", "0.10.0-n", "--agent-notes-file", notesFile)
		Eq(t, manifest().At("agent.notes"), "# 0.10.0\n- from a file")
		run("--agent-version", "0.10.1-n")
		Eq(t, manifest().At("agent"), A{"version": "0.10.1-n", "notes": nil, "schema": nil, "minSchema": nil})
		NoErr(t, os.WriteFile(notesFile, []byte(strings.Repeat("x", 8001)), 0o644))
		Match(t, instOpsFails(t, args("--agent-version", "0.10.1-n", "--agent-notes-file", notesFile)...), `at most 8000`)
		Match(t, instOpsFails(t, args("--agent-version", "0.10.1-n", "--agent-notes", "a", "--agent-notes-file", notesFile)...), `either --agent-notes or --agent-notes-file`)
		Eq(t, manifest().At("agent.version"), "0.10.1-n", "a refused run writes nothing")
		Bucket.Delete(t, "deps/manifest.json")
	})
}
