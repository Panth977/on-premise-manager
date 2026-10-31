package core

import (
	"sort"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

// Constants of routes/agent-upgrade.ts.
const (
	RELEASE_NOTES_MAX = 8000
	EXPIRED_ERROR     = "expired: the agent did not pick it up within 24 h"
)

var (
	upgradeTerminal = []string{"done", "failed", "rolled-back"}
	upgradePhases   = []string{"queued", "download", "verify", "swap", "restart", "health", "done", "failed", "rolled-back"}
)

func hasStr(l []string, s any) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// UpgradeHint is upgradeHint: installed "" = unknown.
func UpgradeHint(installed string) string {
	if installed == "" {
		installed = "(unknown)"
	}
	return "Agent " + installed + " cannot upgrade itself from the GUI: rerun the install command of this site once (it stays valid for upgrades) to reach agent >= " + shared.GUI_UPGRADE_MIN_VERSION + "; later upgrades are one click here."
}

// AgentReleaseInfo is agentReleaseInfo: pass doc.Undefined to load the manifest, else the loaded manifest (*doc.Map or nil).
// nil = nothing pinned or uploaded.
func AgentReleaseInfo(m any) *doc.Map {
	var man *doc.Map
	if m == doc.Undefined {
		man = LoadDepsManifest()
	} else {
		man, _ = m.(*doc.Map)
	}
	if ag := man.Map("agent"); ag.Truthy("version") {
		var notes any
		if s, ok := ag.StrOK("notes"); ok && doc.JSTrim(s) != "" {
			notes = doc.JSSlice(s, RELEASE_NOTES_MAX)
		}
		return doc.Obj("version", ag.Val("version"), "notes", notes)
	}
	v, err := ResolveAgentVersion("")
	if err != nil {
		return nil
	}
	return doc.Obj("version", v, "notes", nil)
}

// msOf is ms: false for NaN.
func msOf(ts string) (int64, bool) {
	if ts == "" {
		return 0, false
	}
	return doc.ParseISO(ts)
}

func expiredAt(r *doc.Map, now int64) bool {
	e, ok := msOf(r.Str("expiresAt"))
	return ok && e <= now
}

// Started is started: the agent reported a phase for this request.
func Started(a, req *doc.Map) bool {
	u := a.Map("upgrade")
	return u != nil && u.Val("requestId") == req.Val("requestId")
}

// LiveRequest is liveRequest: the queued request of a site, nil when absent or expired (and not started by the agent).
func LiveRequest(a *doc.Map, now int64) *doc.Map {
	r := a.Map("upgradeRequest")
	if !r.Truthy("requestId") {
		return nil
	}
	if expiredAt(r, now) && !Started(a, r) {
		return nil
	}
	return r
}

func expiredRequest(a *doc.Map, now int64) *doc.Map {
	r := a.Map("upgradeRequest")
	if r.Truthy("requestId") && expiredAt(r, now) && !Started(a, r) {
		return r
	}
	return nil
}

func mapOrNil(m *doc.Map) any {
	if m == nil {
		return nil
	}
	return m
}

// UpgradeViewOf is upgradeViewOf: nil when not enrolled (a nil). release may be nil.
func UpgradeViewOf(a, release *doc.Map, now int64) *doc.Map {
	if a == nil {
		return nil
	}
	installed := JSString(or(a, "agentVersion", ""))
	var latest any
	lv := ""
	if release != nil {
		latest = or(release, "version", nil)
		lv = release.Str("version")
	}
	supported := shared.SupportsGuiUpgrade(installed)
	var hint any
	if !supported {
		hint = UpgradeHint(installed)
	}
	return doc.Obj("installed", installed, "latest", latest, "available", shared.AgentUpgradeAvailable(installed, lv), "supported", supported, "hint", hint, "request", mapOrNil(LiveRequest(a, now)), "progress", or(a, "upgrade", nil))
}

// LiveRequests is liveRequests: unexpired requests of every site, by siteSlug.
func LiveRequests(agents []*doc.Map, now int64) []any {
	var l []*doc.Map
	for _, a := range agents {
		if r := LiveRequest(a, now); r != nil {
			l = append(l, r)
		}
	}
	sort.SliceStable(l, func(i, j int) bool { return l[i].Str("siteSlug") < l[j].Str("siteSlug") })
	out := make([]any, len(l))
	for i, r := range l {
		out[i] = r
	}
	return out
}

func optShortStr(o *doc.Map, k string) bool {
	v := present(o, k)
	if nullish(v) {
		return true
	}
	s, ok := v.(string)
	return ok && doc.JSLen(s) <= 64
}

// UpgradeProgressError is upgradeProgressError: "" = ok.
func UpgradeProgressError(v any) string {
	if nullish(v) {
		return ""
	}
	o, ok := v.(*doc.Map)
	if !ok || o == nil {
		return "upgrade must be an object or null"
	}
	if s, ok := o.StrOK("upgradeId"); !ok || s == "" || doc.JSLen(s) > 64 {
		return "upgrade.upgradeId must be a string (1–64)"
	}
	if !hasStr(upgradePhases, o.Val("phase")) {
		return "upgrade.phase must be " + strings.Join(upgradePhases, " | ")
	}
	if !optShortStr(o, "requestId") {
		return "upgrade.requestId must be null or a string ≤ 64"
	}
	if !optShortStr(o, "runId") {
		return "upgrade.runId must be null or a string ≤ 64"
	}
	if doc.JSLen(doc.MarshalString(o)) > 16*1024 {
		return "upgrade is larger than 16 KiB"
	}
	return ""
}

func strMax(m *doc.Map, k string, max int) any {
	if s, ok := m.StrOK(k); ok {
		return doc.JSSlice(s, max)
	}
	return nil
}

func strMaxOr(m *doc.Map, k string, max int, def string) any {
	if v := strMax(m, k, max); v != nil {
		return v
	}
	return def
}

func finiteNum(m *doc.Map, k string) any {
	if f, ok := m.Val(k).(float64); ok && f-f == 0 {
		return f
	}
	return nil
}

// CleanProgress is cleanProgress: the stored copy of a heartbeat `upgrade` (known fields only).
func CleanProgress(v *doc.Map, siteSlug string) *doc.Map {
	via := v.Val("via")
	if !hasStr([]string{"gui", "cloud", "cli", "mesh"}, via) {
		via = "cli"
	}
	var ver any
	if x := v.Map("verification"); x != nil {
		src := x.Val("publicKeySource")
		if !hasStr([]string{"agent.json", "cloud", "flag"}, src) {
			src = "cloud"
		}
		tri := func(k string) any {
			if b, ok := x.Val(k).(bool); ok {
				return b
			}
			return nil
		}
		ver = doc.Obj("version", strMaxOr(x, "version", 64, ""), "sha256", strMaxOr(x, "sha256", 128, ""), "signatureOk", x.Val("signatureOk") == true, "publicKeySource", src, "publicKeyFingerprint", strMaxOr(x, "publicKeyFingerprint", 128, ""), "sha256Ok", tri("sha256Ok"), "selfTestOk", tri("selfTestOk"), "error", strMax(x, "error", 2000), "checkedAt", strMaxOr(x, "checkedAt", 64, ""))
	}
	done := finiteNum(v, "bytesDone")
	if done == nil {
		done = 0.0
	}
	return doc.Obj("upgradeId", present(v, "upgradeId"), "requestId", strMax(v, "requestId", 64), "runId", strMax(v, "runId", 64), "via", via, "siteSlug", siteSlug, "from", strMaxOr(v, "from", 64, ""), "to", strMaxOr(v, "to", 64, ""), "phase", present(v, "phase"), "progress", finiteNum(v, "progress"), "bytesDone", done, "bytesTotal", finiteNum(v, "bytesTotal"), "verification", ver, "error", strMax(v, "error", 2000), "actor", strMax(v, "actor", 320), "startedAt", strMaxOr(v, "startedAt", 64, ""), "updatedAt", strMaxOr(v, "updatedAt", 64, doc.NowISO()), "finishedAt", strMax(v, "finishedAt", 64))
}

// CopyRun is copyRun.
func CopyRun(r *doc.Map) *doc.Map {
	sites := []any{}
	for _, s := range r.Arr("sites") {
		sites = append(sites, s.(*doc.Map).Shallow())
	}
	return r.Shallow().Set("sites", sites)
}

// NewUpgradeRequest is newRequest; by: {email, uid}; runID: string or nil; now in ms.
func NewUpgradeRequest(siteSlug, version string, by *doc.Map, runID any, now int64) *doc.Map {
	t := time.UnixMilli(now)
	return doc.Obj("requestId", ulid.At(t), "siteSlug", siteSlug, "version", version, "requestedBy", present(by, "email"), "requestedByUid", present(by, "uid"), "requestedAt", doc.ISO(t), "expiresAt", doc.ISO(t.Add(shared.AGENT_UPGRADE_REQUEST_TTL_SEC*time.Second)), "runId", runID)
}

// FinishRun is finish.
func FinishRun(run *doc.Map, status, at string) {
	run.Set("status", status).Set("finishedAt", at)
	for _, x := range run.Arr("sites") {
		if s := x.(*doc.Map); s.Val("status") == "pending" {
			s.Set("status", "cancelled").Set("at", at)
		}
	}
}

// SkipReason is skipReason: why a site cannot be upgraded by the run now; "" = it can.
func SkipReason(a *doc.Map, version string) string {
	if a == nil {
		return "not enrolled"
	}
	installed := a.Str("agentVersion")
	if !shared.SupportsGuiUpgrade(installed) {
		return UpgradeHint(installed)
	}
	if shared.CompareAgentVersions(installed, version) >= 0 {
		return "already on " + installed
	}
	return ""
}

// QueueNext is queueNext: queue the next pending site; nil when none is left (then the run is done).
func QueueNext(run *doc.Map, agents map[string]*doc.Map, by *doc.Map, now int64) *doc.Map {
	at := doc.ISO(time.UnixMilli(now))
	version := run.Str("version")
	for _, x := range run.Arr("sites") {
		s := x.(*doc.Map)
		if s.Val("status") != "pending" {
			continue
		}
		a := agents[s.Str("siteSlug")]
		if why := SkipReason(a, version); why != "" {
			s.Set("status", "skipped").Set("error", why).Set("at", at)
			continue
		}
		req := NewUpgradeRequest(s.Str("siteSlug"), version, by, present(run, "runId"), now)
		s.Set("status", "requested").Set("requestId", req.Val("requestId")).Set("from", present(a, "agentVersion")).Set("at", at).Set("error", nil)
		return req
	}
	FinishRun(run, "done", at)
	return nil
}

// ApplyRunExpiry is applyRunExpiry: expired, unstarted requests of the run's current site → that site failed, the run
// failed. run may be nil.
func ApplyRunExpiry(run *doc.Map, agents map[string]*doc.Map, now int64) (*doc.Map, bool) {
	if run == nil || run.Val("status") != "running" {
		return run, false
	}
	out := CopyRun(run)
	changed := false
	for _, x := range out.Arr("sites") {
		s := x.(*doc.Map)
		if s.Val("status") != "requested" || !s.Truthy("requestId") {
			continue
		}
		a := agents[s.Str("siteSlug")]
		r := a.Map("upgradeRequest")
		if a == nil || r == nil || r.Val("requestId") != s.Val("requestId") || expiredRequest(a, now) == nil {
			continue
		}
		at := doc.ISO(time.UnixMilli(now))
		s.Set("status", "failed").Set("error", EXPIRED_ERROR).Set("at", at)
		FinishRun(out, "failed", at)
		changed = true
	}
	return out, changed
}

// SettleUpgrade is settleUpgrade: after a heartbeat of a site with an upgradeRequest, clear it when done, failed or
// expired, and advance or stop the run. One transaction.
func SettleUpgrade(deploymentID, siteSlug string) error {
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	aRef := cloud.Col.Agents(deploymentID).Doc(siteSlug)
	return cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		as, err := tx.Get(aRef)
		if err != nil {
			return err
		}
		ds, err := tx.Get(depRef)
		if err != nil {
			return err
		}
		if !as.Exists() || !ds.Exists() {
			return nil
		}
		a := as.Data()
		req := a.Map("upgradeRequest")
		if !req.Truthy("requestId") {
			return nil
		}
		nowT := time.Now()
		now := nowT.UnixMilli()
		at := doc.ISO(nowT)
		var p *doc.Map
		if u := a.Map("upgrade"); u != nil && u.Val("requestId") == req.Val("requestId") {
			p = u
		}
		outcome := ""
		var errText any
		if p != nil && hasStr(upgradeTerminal, p.Val("phase")) {
			if p.Val("phase") == "done" {
				outcome = "done"
			} else {
				outcome = "failed"
				t := JSString(p.Val("phase"))
				if p.Truthy("error") {
					t += ": " + JSString(p.Val("error"))
				}
				errText = doc.JSSlice(t, 2000)
			}
		} else if shared.CompareAgentVersions(a.Str("agentVersion"), req.Str("version")) >= 0 {
			outcome = "done"
		} else if p == nil && expiredAt(req, now) {
			outcome, errText = "expired", EXPIRED_ERROR
		}
		dep := cloud.DecodeDeployment(ds.Data())
		var run, site *doc.Map
		if r := dep.Map("agentUpgradeRun"); r != nil && req.Truthy("runId") && r.Val("runId") == req.Val("runId") {
			run = CopyRun(r)
			for _, x := range run.Arr("sites") {
				if s := x.(*doc.Map); s.Val("requestId") == req.Val("requestId") {
					site = s
					break
				}
			}
		}
		if outcome == "" {
			if p != nil && run != nil && site != nil && site.Val("status") == "requested" {
				site.Set("status", "running").Set("at", at)
				tx.Update(depRef, doc.Obj("agentUpgradeRun", run))
			}
			return nil
		}
		agents := map[string]*doc.Map{}
		if run != nil && run.Val("status") == "running" && outcome == "done" {
			q, err := tx.Query(cloud.Col.Agents(deploymentID).Query)
			if err != nil {
				return err
			}
			for _, d := range q.Docs {
				agents[d.ID()] = d.Data()
			}
		}
		agents[siteSlug] = a.Shallow().Set("upgradeRequest", nil)
		log := NewCloudLog(deploymentID, dep)
		type write struct {
			ref  *driver.DocRef
			data *doc.Map
		}
		writes := []write{{aRef, doc.Obj("upgradeRequest", nil)}}
		if outcome == "expired" {
			data := doc.Obj("requestId", req.Val("requestId"), "siteSlug", siteSlug)
			if req.Truthy("runId") {
				data.Set("runId", req.Val("runId"))
			}
			log.Add(tx, CloudEntry{Type: "agent.upgrade-cancelled", Summary: "Agent upgrade request for site " + siteSlug + " expired (the agent did not pick it up within 24 h)", Data: data.Set("reason", "expired"), Origin: "system"})
		}
		if run != nil && site != nil {
			status := "failed"
			if outcome == "done" {
				status = "done"
			}
			site.Set("status", status).Set("error", errText).Set("at", at)
			if run.Val("status") == "running" {
				if outcome != "done" {
					FinishRun(run, "failed", at)
				} else if next := QueueNext(run, agents, doc.Obj("email", present(req, "requestedBy"), "uid", present(req, "requestedByUid")), now); next != nil {
					ns := next.Str("siteSlug")
					writes = append(writes, write{cloud.Col.Agents(deploymentID).Doc(ns), doc.Obj("upgradeRequest", next)})
					log.Add(tx, CloudEntry{Type: "agent.upgrade-requested", Summary: "Agent upgrade of site " + ns + " to " + next.Str("version") + " queued (upgrade all, after " + siteSlug + ")", Data: doc.Obj("requestId", next.Val("requestId"), "siteSlug", ns, "version", next.Val("version"), "runId", present(run, "runId")), Actor: present(req, "requestedByUid")})
				}
			}
		}
		for _, w := range writes {
			tx.Update(w.ref, w.data)
		}
		upd := log.DepUpdate().Set("agentEpoch", dep.Num("agentEpoch")+1)
		if run != nil {
			upd.Set("agentUpgradeRun", run)
		}
		tx.Update(depRef, upd)
		return nil
	})
}
