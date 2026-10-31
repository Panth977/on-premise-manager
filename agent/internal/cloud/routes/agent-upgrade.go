package routes

import (
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

// Human endpoints of routes/agent-upgrade.ts + expireAgentUpgrades; the run helpers live in core/route_agent_upgrade.go.

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"requestAgentUpgrade":   requestAgentUpgrade,
		"cancelAgentUpgrade":    cancelAgentUpgrade,
		"upgradeAllAgents":      upgradeAllAgents,
		"cancelAgentUpgradeRun": cancelAgentUpgradeRun,
	})
}

var upgradeTerminal = map[string]bool{"done": true, "failed": true, "rolled-back": true}

func expiredReq(a *doc.Map, now int64) *doc.Map {
	r := a.Map("upgradeRequest")
	if !r.Truthy("requestId") {
		return nil
	}
	e, ok := doc.ParseISO(r.Str("expiresAt"))
	if ok && e <= now && !core.Started(a, r) {
		return r
	}
	return nil
}

// agentsOf is the agents of a deployment read in a transaction: the ids in document order + the map.
func agentsOf(tx *driver.Tx, deploymentID string) ([]string, map[string]*doc.Map, error) {
	q, err := tx.Query(cloud.Col.Agents(deploymentID).Query)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, len(q.Docs))
	m := map[string]*doc.Map{}
	for i, d := range q.Docs {
		ids[i] = d.ID()
		m[d.ID()] = d.Data()
	}
	return ids, m, nil
}

func cancelledData(r *doc.Map, siteSlug string) *doc.Map {
	d := doc.Obj("requestId", fld(r, "requestId"), "siteSlug", siteSlug)
	if r.Truthy("runId") {
		d.Set("runId", r.Val("runId"))
	}
	return d
}

// ExpireAgentUpgrades is expireAgentUpgrades (daily): clear expired unstarted requests (activity agent.upgrade-cancelled
// reason expired) and fail their runs.
func ExpireAgentUpgrades(deploymentID string, now int64) (int, error) {
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (int, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return 0, err
		}
		ids, agents, err := agentsOf(tx, deploymentID)
		if err != nil {
			return 0, err
		}
		if !ds.Exists() {
			return 0, nil
		}
		dep := cloud.DecodeDeployment(ds.Data())
		run, changed := core.ApplyRunExpiry(dep.Map("agentUpgradeRun"), agents, now)
		log := core.NewCloudLog(deploymentID, dep)
		n := 0
		for _, slug := range ids {
			r := expiredReq(agents[slug], now)
			if r == nil {
				continue
			}
			tx.Update(cloud.Col.Agents(deploymentID).Doc(slug), doc.Obj("upgradeRequest", nil))
			log.Add(tx, core.CloudEntry{Type: "agent.upgrade-cancelled", Summary: "Agent upgrade request for site " + slug + " expired (the agent did not pick it up within 24 h)", Data: cancelledData(r, slug).Set("reason", "expired"), Origin: "system"})
			n++
		}
		if n > 0 || changed {
			upd := log.DepUpdate().Set("agentEpoch", dep.Num("agentEpoch")+1)
			if changed {
				upd.Set("agentUpgradeRun", run)
			}
			tx.Update(depRef, upd)
		}
		return n, nil
	})
}

func byOf(c *cloud.Ctx) *doc.Map {
	email := c.Account.UID
	if c.Account.Email != nil && *c.Account.Email != "" {
		email = *c.Account.Email
	}
	return doc.Obj("email", email, "uid", c.Account.UID)
}

func pinFor(version any) (*doc.Map, error) {
	v, ok := version.(string)
	if !ok || v == "" {
		return nil, cloud.E.Bad("version", "V_REQUIRED", "version (the pinned agent version you saw) is required")
	}
	rel := core.AgentReleaseInfo(doc.Undefined)
	if rel == nil {
		return nil, cloud.E.NotFound("no agent release is pinned (upload the agent + deps manifest first)")
	}
	if rel.Val("version") != v {
		return nil, cloud.E.Stale("the pinned agent release moved to "+rel.Str("version")+"; reload and request again", doc.Obj("pinned", rel.Val("version"), "requested", v))
	}
	return rel, nil
}

func invalidHint(message, hint string) *cloud.ApiError {
	d := doc.Obj("errors", []cloud.Issue{{Path: "", Code: "V_STATE", Message: message, Severity: "error"}})
	if hint != "" {
		d.Set("hint", hint)
	}
	return cloud.E.New("invalid", message, d, nil)
}

func strOrNullAudit(b *doc.Map, k string) any {
	if s, ok := b.StrOK(k); ok {
		return s
	}
	return nil
}

func requestAgentUpgrade(c *cloud.Ctx) (any, error) {
	dep0, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep0, "deployment.setup", "write"); err != nil {
		return nil, err
	}
	slug := c.Params["siteSlug"]
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"version", "confirm"}); err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("siteSlug", slug, "version", strOrNullAudit(b, "version"))}
	if b.Val("confirm") != slug {
		return nil, cloud.E.Bad("confirm", "V_FORMAT", "confirm must equal the site slug")
	}
	rel, err := pinFor(fld(b, "version"))
	if err != nil {
		return nil, err
	}
	relV := rel.Str("version")
	depID := dep0.Str("deploymentId")
	depRef := cloud.Col.Deployments().Doc(depID)
	aRef := cloud.Col.Agents(depID).Doc(slug)
	req, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		_, agents, err := agentsOf(tx, depID)
		if err != nil {
			return nil, err
		}
		dep := cloud.DecodeDeployment(ds.Data())
		now := time.Now().UnixMilli()
		a := agents[slug]
		if a == nil {
			return nil, invalidHint("site "+slug+" is not enrolled", "")
		}
		from := a.Str("agentVersion")
		if !shared.SupportsGuiUpgrade(from) {
			return nil, invalidHint("agent "+from+" of site "+slug+" cannot upgrade itself (needs >= "+shared.GUI_UPGRADE_MIN_VERSION+")", core.UpgradeHint(from))
		}
		if shared.CompareAgentVersions(from, relV) >= 0 {
			return nil, invalidHint("site "+slug+" already runs agent "+from+" (pinned "+relV+")", "")
		}
		if live := core.LiveRequest(a, now); live != nil {
			return nil, cloud.E.Conflict("an upgrade of site "+slug+" to "+live.Str("version")+" is already queued (requested by "+live.Str("requestedBy")+")", doc.Obj("request", live))
		}
		run, changed := core.ApplyRunExpiry(dep.Map("agentUpgradeRun"), agents, now)
		if run != nil && run.Val("status") == "running" {
			return nil, cloud.E.Conflict(`an "Upgrade all" run is going; cancel it first`, doc.Obj("runId", fld(run, "runId")))
		}
		if p := a.Map("upgrade"); p != nil && !upgradeTerminal[p.Str("phase")] && p.Val("phase") != "queued" {
			if u, ok := doc.ParseISO(p.Str("updatedAt")); ok && now-u < 15*60_000 {
				return nil, cloud.E.Conflict("an upgrade is running on site "+slug+" ("+core.JSString(fld(p, "phase"))+")", doc.Obj("progress", p))
			}
		}
		r := core.NewUpgradeRequest(slug, relV, byOf(c), nil, now)
		log := core.NewCloudLog(depID, dep)
		log.Add(tx, core.CloudEntry{Type: "agent.upgrade-requested", Summary: "Agent upgrade of site " + slug + " to " + relV + " requested (from " + from + ")", Data: doc.Obj("requestId", r.Val("requestId"), "siteSlug", slug, "version", relV, "from", from), Actor: c.Account.UID})
		tx.Update(aRef, doc.Obj("upgradeRequest", r))
		upd := log.DepUpdate().Set("agentEpoch", dep.Num("agentEpoch")+1)
		if changed {
			upd.Set("agentUpgradeRun", run)
		}
		tx.Update(depRef, upd)
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("siteSlug", slug, "version", req.Val("version"), "requestId", req.Val("requestId"))}
	return doc.Obj("request", req), nil
}

func cancelAgentUpgrade(c *cloud.Ctx) (any, error) {
	dep0, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep0, "deployment.setup", "write"); err != nil {
		return nil, err
	}
	slug := c.Params["siteSlug"]
	c.Audit = &cloud.AuditHint{Data: doc.Obj("siteSlug", slug)}
	depID := dep0.Str("deploymentId")
	depRef := cloud.Col.Deployments().Doc(depID)
	aRef := cloud.Col.Agents(depID).Doc(slug)
	req, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		as, err := tx.Get(aRef)
		if err != nil {
			return nil, err
		}
		if !as.Exists() {
			return nil, cloud.E.NotFound("site " + slug + " is not enrolled")
		}
		a := as.Data()
		r := core.LiveRequest(a, time.Now().UnixMilli())
		if r == nil {
			return nil, cloud.E.NotFound("no queued agent upgrade request for site " + slug)
		}
		if core.Started(a, r) {
			u := a.Map("upgrade")
			return nil, cloud.E.Conflict("the agent already started this upgrade ("+core.JSString(fld(u, "phase"))+"); it finishes or rolls back by itself", doc.Obj("progress", u))
		}
		dep := cloud.DecodeDeployment(ds.Data())
		at := doc.NowISO()
		// the current request of a running "Upgrade all" run: cancelling it cancels the run
		var run *doc.Map
		if cur := dep.Map("agentUpgradeRun"); r.Truthy("runId") && cur != nil && cur.Val("runId") == r.Val("runId") && cur.Val("status") == "running" {
			run = core.CopyRun(cur)
			if s := findBy(run.Arr("sites"), "requestId", r.Str("requestId")); s != nil {
				s.Set("status", "cancelled").Set("at", at)
			}
			core.FinishRun(run, "cancelled", at)
		}
		log := core.NewCloudLog(depID, dep)
		summary := "Agent upgrade request for site " + slug + " cancelled"
		if run != nil {
			summary += " (the upgrade-all run stops)"
		}
		log.Add(tx, core.CloudEntry{Type: "agent.upgrade-cancelled", Summary: summary, Data: cancelledData(r, slug), Actor: c.Account.UID})
		tx.Update(aRef, doc.Obj("upgradeRequest", nil))
		upd := log.DepUpdate().Set("agentEpoch", dep.Num("agentEpoch")+1)
		if run != nil {
			upd.Set("agentUpgradeRun", run)
		}
		tx.Update(depRef, upd)
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	data := doc.Obj("siteSlug", slug, "requestId", req.Val("requestId"))
	if req.Truthy("runId") {
		data.Set("runId", req.Val("runId"))
	}
	c.Audit = &cloud.AuditHint{Data: data}
	return doc.Obj("cancelled", true), nil
}

func upgradeAllAgents(c *cloud.Ctx) (any, error) {
	dep0, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep0, "deployment.setup", "write"); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"version", "confirm"}); err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("version", strOrNullAudit(b, "version"))}
	if b.Val("confirm") != "UPGRADE" {
		return nil, cloud.E.Bad("confirm", "V_FORMAT", "confirm must be UPGRADE")
	}
	rel, err := pinFor(fld(b, "version"))
	if err != nil {
		return nil, err
	}
	relV := rel.Str("version")
	depID := dep0.Str("deploymentId")
	depRef := cloud.Col.Deployments().Doc(depID)
	run, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		ids, agents, err := agentsOf(tx, depID)
		if err != nil {
			return nil, err
		}
		dep := cloud.DecodeDeployment(ds.Data())
		nowT := time.Now()
		now := nowT.UnixMilli()
		at := doc.ISO(nowT)
		cur, _ := core.ApplyRunExpiry(dep.Map("agentUpgradeRun"), agents, now)
		if cur != nil && cur.Val("status") == "running" {
			return nil, cloud.E.Conflict(`an "Upgrade all" run is already going`, doc.Obj("runId", fld(cur, "runId")))
		}
		for _, id := range ids {
			a := agents[id]
			if live := core.LiveRequest(a, now); live != nil {
				return nil, cloud.E.Conflict("site "+a.Str("siteSlug")+" has a queued upgrade request (to "+live.Str("version")+"); wait for it or cancel it first", doc.Obj("request", live))
			}
		}
		// enrolled sites below the pin + not-enrolled sites of the draft (skipped), siteSlug order
		all := append([]string{}, ids...)
		for _, s := range mapsIn(dep.Map("draft").Arr("sites")) {
			all = append(all, s.Str("siteSlug"))
		}
		var sites, pending []*doc.Map
		for _, slug := range sortedSet(all) {
			a := agents[slug]
			if a != nil && !shared.AgentUpgradeAvailable(a.Str("agentVersion"), relV) {
				continue // up to date
			}
			why := core.SkipReason(a, relV)
			s := doc.Obj("siteSlug", slug, "status", "pending", "from", fldOr(a, "agentVersion", nil), "requestId", nil, "error", nil, "at", nil)
			if why != "" {
				s.Set("status", "skipped").Set("error", why).Set("at", at)
			} else {
				pending = append(pending, s)
			}
			sites = append(sites, s)
		}
		if len(pending) == 0 {
			var old []string
			for _, s := range sites {
				if s.Val("status") == "skipped" && agents[s.Str("siteSlug")] != nil {
					old = append(old, s.Str("siteSlug")+" ("+core.JSString(s.Val("from"))+")")
				}
			}
			if len(old) > 0 {
				var firstFrom string
				for _, s := range sites {
					if s.Val("status") == "skipped" && agents[s.Str("siteSlug")] != nil {
						firstFrom = s.Str("from")
						break
					}
				}
				return nil, invalidHint("no site can upgrade from the cloud: "+strings.Join(old, ", ")+" run agents below "+shared.GUI_UPGRADE_MIN_VERSION, core.UpgradeHint(firstFrom))
			}
			return nil, invalidHint("every enrolled site already runs agent "+relV, "")
		}
		siteList := make([]any, len(sites))
		for i, s := range sites {
			siteList[i] = s
		}
		by := byOf(c)
		r := doc.Obj("runId", ulid.At(nowT), "origin", "cloud", "coordinator", nil, "version", relV, "requestedBy", by.Val("email"), "requestedAt", at, "status", "running", "sites", siteList, "finishedAt", nil)
		first := core.QueueNext(r, agents, by, now)
		log := core.NewCloudLog(depID, dep)
		var pendingSlugs, skipped []any
		pendingSlugs, skipped = []any{}, []any{}
		for _, s := range pending {
			pendingSlugs = append(pendingSlugs, s.Val("siteSlug"))
		}
		for _, s := range sites {
			if s.Val("status") == "skipped" {
				skipped = append(skipped, s.Val("siteSlug"))
			}
		}
		log.Add(tx, core.CloudEntry{Type: "agent.upgrade-requested", Summary: "Upgrade all agents to " + relV + ": " + core.JSString(float64(len(pending))) + " site(s), one at a time (first " + first.Str("siteSlug") + ")", Data: doc.Obj("runId", r.Val("runId"), "version", relV, "sites", pendingSlugs, "skipped", skipped, "requestId", first.Val("requestId"), "siteSlug", first.Val("siteSlug")), Actor: c.Account.UID})
		tx.Update(cloud.Col.Agents(depID).Doc(first.Str("siteSlug")), doc.Obj("upgradeRequest", first))
		tx.Update(depRef, log.DepUpdate().Set("agentEpoch", dep.Num("agentEpoch")+1).Set("agentUpgradeRun", r))
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	notSkipped := []any{}
	for _, s := range mapsIn(run.Arr("sites")) {
		if s.Val("status") != "skipped" {
			notSkipped = append(notSkipped, s.Val("siteSlug"))
		}
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("runId", run.Val("runId"), "version", run.Val("version"), "sites", notSkipped)}
	return doc.Obj("run", run), nil
}

func cancelAgentUpgradeRun(c *cloud.Ctx) (any, error) {
	dep0, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep0, "deployment.setup", "write"); err != nil {
		return nil, err
	}
	depID := dep0.Str("deploymentId")
	depRef := cloud.Col.Deployments().Doc(depID)
	run, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		_, agents, err := agentsOf(tx, depID)
		if err != nil {
			return nil, err
		}
		dep := cloud.DecodeDeployment(ds.Data())
		if dep.Map("agentUpgradeRun") == nil {
			return nil, cloud.E.NotFound(`no "Upgrade all" run`)
		}
		nowT := time.Now()
		cur, _ := core.ApplyRunExpiry(dep.Map("agentUpgradeRun"), agents, nowT.UnixMilli())
		if cur.Val("status") != "running" {
			return nil, cloud.E.Conflict(`the "Upgrade all" run is `+core.JSString(fld(cur, "status")), doc.Obj("run", cur))
		}
		r := core.CopyRun(cur)
		at := doc.ISO(nowT)
		var removed *doc.Map
		for _, s := range mapsIn(r.Arr("sites")) {
			if s.Val("status") != "requested" || !s.Truthy("requestId") {
				continue
			}
			a := agents[s.Str("siteSlug")]
			q := a.Map("upgradeRequest")
			if a == nil || q == nil {
				continue
			}
			if q.Val("requestId") == s.Val("requestId") && !core.Started(a, q) {
				tx.Update(cloud.Col.Agents(depID).Doc(s.Str("siteSlug")), doc.Obj("upgradeRequest", nil))
				s.Set("status", "cancelled").Set("at", at)
				removed = q
			} else if core.Started(a, q) {
				s.Set("status", "running").Set("at", at) // finishes by itself
			}
		}
		core.FinishRun(r, "cancelled", at)
		log := core.NewCloudLog(depID, dep)
		summary := "Upgrade all agents (" + r.Str("version") + ") cancelled"
		data := doc.Obj("runId", fld(r, "runId"))
		if removed != nil {
			summary += " (queued request of site " + removed.Str("siteSlug") + " removed)"
			data.Set("requestId", removed.Val("requestId")).Set("siteSlug", removed.Val("siteSlug"))
		}
		log.Add(tx, core.CloudEntry{Type: "agent.upgrade-cancelled", Summary: summary, Data: data, Actor: c.Account.UID})
		tx.Update(depRef, log.DepUpdate().Set("agentEpoch", dep.Num("agentEpoch")+1).Set("agentUpgradeRun", r))
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("runId", run.Val("runId"))}
	return doc.Obj("run", run), nil
}
