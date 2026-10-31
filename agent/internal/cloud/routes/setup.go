package routes

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// Setup: access key, install tokens, agent enrollment revoke (routes/setup.ts). apiBaseOf is core.ApiBaseOf.

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"getSetup":                 getSetup,
		"rotateAccessKey":          rotateAccessKey,
		"revealAccessKey":          revealAccessKey,
		"createInstallToken":       createInstallToken,
		"revealInstallCommand":     revealInstallCommand,
		"revealUpgradeCommand":     revealUpgradeCommand,
		"regenerateUpgradeCommand": regenerateUpgradeCommand,
		"revokeInstallToken":       revokeInstallToken,
		"revokeAgent":              revokeAgent,
	})
}

var kindOrder = map[string]float64{"edge": 0, "gui-alias": 1, "agent-port": 2}

// PortAlertsOf is portAlertsOf: §21 #86 edge ports the agent cannot bind (heartbeat edge.bindErrors).
func PortAlertsOf(a *doc.Map) []any {
	out := []any{}
	for _, b := range mapsIn(a.Map("edge").Arr("bindErrors")) {
		p := b.Val("purpose")
		if p != "edge-http" && p != "edge-https" {
			continue
		}
		var owner any
		name := "another program"
		if s := b.Str("owner"); s != "" {
			owner, name = s, s
		}
		port := core.JSString(fld(b, "port"))
		out = append(out, doc.Obj("port", fld(b, "port"), "purpose", p, "owner", owner, "message", "port "+port+" is used by "+name+" — stop it or change edge.httpPort"))
	}
	return out
}

var reIPv4Addr = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){3}$`)

// GuiUrlsOf is guiUrlsOf: §21 #86 agent GUI URLs from the last heartbeat: addresses (IPv4 of detectedAddresses, max 3; else
// the first entry; else agent.ip) × guiPorts, port-major (http 80 first, then https 443, then edge, gui-alias, agent-port).
func GuiUrlsOf(a *doc.Map) []any {
	ports := mapsIn(a.Arr("guiPorts"))
	out := []any{}
	if len(ports) == 0 {
		return out
	}
	da := a.Strs("detectedAddresses")
	var addrs []string
	for _, x := range da {
		if reIPv4Addr.MatchString(x) && len(addrs) < 3 {
			addrs = append(addrs, x)
		}
	}
	if len(addrs) == 0 {
		if len(da) > 0 {
			addrs = da[:1]
		} else if s := a.Str("ip"); s != "" {
			addrs = []string{s}
		}
	}
	rank := func(g *doc.Map) float64 {
		p := g.Num("port")
		switch {
		case g.Val("scheme") == "http" && p == 80:
			return 0
		case g.Val("scheme") == "https" && p == 443:
			return 1
		}
		if o, ok := kindOrder[g.Str("kind")]; ok {
			return 2 + o
		}
		return math.NaN()
	}
	sort.SliceStable(ports, func(i, j int) bool {
		d := rank(ports[i]) - rank(ports[j])
		if d == 0 || d != d {
			d = ports[i].Num("port") - ports[j].Num("port")
		}
		return d < 0
	})
	seen := map[string]bool{}
	for _, g := range ports {
		for _, addr := range addrs {
			host := addr
			if strings.Contains(addr, ":") {
				host = "[" + addr + "]"
			}
			scheme, p := g.Str("scheme"), g.Num("port")
			suffix := ":" + core.JSString(fld(g, "port"))
			if (scheme == "http" && p == 80) || (scheme == "https" && p == 443) {
				suffix = ""
			}
			url := scheme + "://" + host + suffix + "/_opm/"
			if seen[url] {
				continue
			}
			seen[url] = true
			out = append(out, doc.Obj("url", url, "kind", fld(g, "kind")))
		}
	}
	return out
}

// INSTALL_TOKEN_TTL_MS is INSTALL_TOKEN_TTL_MS.
const INSTALL_TOKEN_TTL_MS = 24 * 3600_000

func tokenState(t *doc.Map) string { return shared.InstallTokenState(t, time.Now().UnixMilli()) }

func kindOf(t *doc.Map) string {
	if t.Val("kind") == "maintenance" {
		return "maintenance"
	}
	return "install"
}

func hasPlain(t *doc.Map) bool { return t != nil && t.Str("token") != "" }

// InstallTokenView is installTokenView: stored token → view, never the token (§21 #84). upgradeHash: the site's current
// upgrade command token (§21 #92), "" = null.
func InstallTokenView(t *doc.Map, upgradeHash string) *doc.Map {
	state := tokenState(t)
	kind := kindOf(t)
	return t.Without("token").Set("kind", kind).Set("expiresAt", fldOr(t, "expiresAt", nil)).Set("state", state).Set("stored", kind == "install" && state == "active" && hasPlain(t)).Set("upgradeCommand", upgradeHash != "" && t.Val("tokenHash") == upgradeHash)
}

// sortNewestFirst is toks.sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1)) as V8 runs it. That comparator calls equal
// values "less", which reverses ties, so a stable sort is not the same; this is the run detection + binary insertion of
// V8's TimSort, which covers every list shorter than 64 (the merge phase only changes the order of ties beyond that).
func sortNewestFirst(l []*doc.Map) {
	cmp := func(a, b *doc.Map) int {
		if a.Str("createdAt") < b.Str("createdAt") {
			return 1
		}
		return -1
	}
	n := len(l)
	if n < 2 {
		return
	}
	run := 2
	if cmp(l[1], l[0]) < 0 {
		for run < n && cmp(l[run], l[run-1]) < 0 {
			run++
		}
		for i, j := 0, run-1; i < j; i, j = i+1, j-1 {
			l[i], l[j] = l[j], l[i]
		}
	} else {
		for run < n && cmp(l[run], l[run-1]) >= 0 {
			run++
		}
	}
	for s := run; s < n; s++ {
		p := l[s]
		lo, hi := 0, s
		for lo < hi {
			mid := lo + (hi-lo)/2
			if cmp(p, l[mid]) < 0 {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		copy(l[lo+1:s+1], l[lo:s])
		l[lo] = p
	}
}

// UpgradeTokenOf is upgradeTokenOf: §21 #92 the token of an enrolled site's "Upgrade / reinstall command": the newest
// unrevoked maintenance token bound to the current enrollment, else the enrollment's own install token; nil = none.
func UpgradeTokenOf(toks []*doc.Map, a *doc.Map) *doc.Map {
	if a == nil {
		return nil
	}
	var mine, maint []*doc.Map
	for _, t := range toks {
		if t.Val("siteSlug") == a.Val("siteSlug") && !t.Truthy("revokedAt") {
			mine = append(mine, t)
			if kindOf(t) == "maintenance" && t.Val("boundEnrolledAt") == a.Val("enrolledAt") {
				maint = append(maint, t)
			}
		}
	}
	sortNewestFirst(maint)
	if len(maint) > 0 {
		return maint[0]
	}
	for _, t := range mine {
		if kindOf(t) == "install" && t.Val("usedAt") == a.Val("enrolledAt") {
			return t
		}
	}
	return nil
}

// enrollmentTokens: the tokens the upgrade command of this enrollment consists of (what Regenerate / revokeAgent end).
func enrollmentTokens(toks []*doc.Map, a *doc.Map) []*doc.Map {
	var out []*doc.Map
	for _, t := range toks {
		if t.Truthy("revokedAt") {
			continue
		}
		k := kindOf(t)
		if (k == "maintenance" && t.Val("boundEnrolledAt") == a.Val("enrolledAt")) || (k == "install" && t.Val("usedAt") == a.Val("enrolledAt")) {
			out = append(out, t)
		}
	}
	return out
}

// UpgradeCommandView is upgradeCommandView.
func UpgradeCommandView(t *doc.Map) *doc.Map {
	var kind any
	if t != nil {
		kind = kindOf(t)
	}
	return doc.Obj("tokenHash", fldOr(t, "tokenHash", nil), "kind", kind, "stored", hasPlain(t), "createdAt", fldOr(t, "createdAt", nil), "createdBy", fldOr(t, "createdBy", nil))
}

// InstallCommand is installCommand.
func InstallCommand(c *cloud.Ctx, token string) string {
	return `curl -fsSL -H "Authorization: Bearer ` + token + `" ` + core.ApiBaseOf(c) + "/v1/install/script | sudo sh"
}

func installTokenResponse(c *cloud.Ctx, t *doc.Map) *doc.Map {
	command := InstallCommand(c, t.Str("token"))
	return doc.Obj("value", command, "hideAfterSec", shared.REVEAL_SECONDS, "command", command, "token", fld(t, "token"), "tokenHash", fld(t, "tokenHash"), "siteSlug", fld(t, "siteSlug"), "expiresAt", fldOr(t, "expiresAt", ""))
}

func upgradeCommandResponse(c *cloud.Ctx, t *doc.Map, enrolledAt any) *doc.Map {
	command := InstallCommand(c, t.Str("token"))
	return doc.Obj("value", command, "hideAfterSec", shared.REVEAL_SECONDS, "command", command, "token", fld(t, "token"), "tokenHash", fld(t, "tokenHash"), "siteSlug", fld(t, "siteSlug"), "kind", kindOf(t), "enrolledAt", enrolledAt)
}

// sortedSet is [...new Set(l)].sort().
func sortedSet(l []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range l {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func siteIn(sites []any, slug string) bool { return findBy(sites, "siteSlug", slug) != nil }

func getSetup(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.setup", "read"); err != nil {
		return nil, err
	}
	id := dep.Str("deploymentId")
	agents, err := cloud.Col.Agents(id).Get()
	if err != nil {
		return nil, err
	}
	tokens, err := cloud.Col.InstallTokens(id).Get()
	if err != nil {
		return nil, err
	}
	var head *doc.Map
	if h := dep.Map("head"); h != nil {
		if head, err = core.LoadRevision(id, h.Str("revId"), nil); err != nil {
			return nil, err
		}
	}
	deps := core.LoadDepsManifest()
	agentBy := map[string]*doc.Map{}
	slugList := []string{}
	for _, s := range mapsIn(dep.Map("draft").Arr("sites")) {
		slugList = append(slugList, s.Str("siteSlug"))
	}
	headSites := head.Map("config").Arr("sites")
	for _, s := range mapsIn(headSites) {
		slugList = append(slugList, s.Str("siteSlug"))
	}
	for _, d := range agents.Docs {
		agentBy[d.ID()] = core.AgentView(d.Data())
		slugList = append(slugList, d.ID())
	}
	release := core.AgentReleaseInfo(deps)
	now := time.Now().UnixMilli()
	labels := map[string]any{}
	for _, s := range mapsIn(headSites) {
		labels[s.Str("siteSlug")] = fld(s, "label")
	}
	draftSites := dep.Map("draft").Arr("sites")
	for _, s := range mapsIn(draftSites) {
		labels[s.Str("siteSlug")] = fld(s, "label")
	}
	toks := make([]*doc.Map, len(tokens.Docs))
	for i, d := range tokens.Docs {
		toks[i] = d.Data()
	}
	sortNewestFirst(toks)
	var installDeps any
	if deps != nil {
		installDeps = doc.Obj("dockerVersion", fld(deps.Map("docker"), "version"), "minDockerApi", fld(deps, "minDockerApi"), "agentVersion", fldOr(deps.Map("agent"), "version", nil))
	}
	// an expired, unstarted request of the run's current site shows the run as failed (the daily job persists it)
	run, _ := core.ApplyRunExpiry(dep.Map("agentUpgradeRun"), agentBy, now)
	sites := []any{}
	for _, slug := range sortedSet(slugList) {
		a := agentBy[slug]
		up := UpgradeTokenOf(toks, a)
		var label any = slug
		if l, ok := labels[slug]; ok && l != nil && l != doc.Undefined {
			label = l
		}
		var agent, upgradeCommand any
		guiUrls, portAlerts := []any{}, []any{}
		if a != nil {
			agent = doc.Obj("agentVersion", fld(a, "agentVersion"), "enrolledAt", fld(a, "enrolledAt"), "lastSeenAt", fld(a, "lastSeenAt"), "online", fld(a, "online"), "hostname", fld(a, "hostname"), "ip", fld(a, "ip"), "keyVersion", fld(a, "keyVersion"), "os", fld(a, "os"), "arch", fld(a, "arch"), "advertiseHost", fld(a, "advertiseHost"), "detectedAddresses", fld(a, "detectedAddresses"), "clockSkew", fldOr(a, "clockSkew", nil))
			upgradeCommand = UpgradeCommandView(up)
			guiUrls = GuiUrlsOf(a)
			portAlerts = PortAlertsOf(a)
		}
		var defaultEdge any
		if v, ok := a.Map("edge").Val("defaultEdge").(bool); ok {
			defaultEdge = v
		}
		upHash := ""
		if up != nil {
			upHash = up.Str("tokenHash")
		}
		viewToks := []any{}
		for _, t := range toks {
			if t.Val("siteSlug") == slug && len(viewToks) < 10 {
				viewToks = append(viewToks, InstallTokenView(t, upHash))
			}
		}
		sites = append(sites, doc.Obj("siteSlug", slug, "label", label, "inDraft", siteIn(draftSites, slug), "inHead", head != nil && siteIn(headSites, slug), "agent", agent, "upgradeCommand", upgradeCommand, "guiUrls", guiUrls, "defaultEdge", defaultEdge, "portAlerts", portAlerts, "upgrade", mapOrNull(core.UpgradeViewOf(a, release, now)), "tokens", viewToks))
	}
	return doc.Obj(
		"accessKey", doc.Obj("version", fld(dep, "accessKeyVersion"), "rotatedAt", fld(dep, "accessKeyRotatedAt"), "rotatedBy", fld(dep, "accessKeyRotatedBy"), "stored", dep.Str("accessKey") != ""),
		"apiBase", core.ApiBaseOf(c), "installDeps", installDeps, "agentRelease", mapOrNull(release), "agentUpgradeRun", mapOrNull(run), "serverTime", isoNow(now), "sites", sites,
	), nil
}

func rotateAccessKey(c *cloud.Ctx) (any, error) {
	dep0, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep0, "deployment.setup", "write"); err != nil {
		return nil, err
	}
	key := core.NewAccessKey()
	ref := cloud.Col.Deployments().Doc(dep0.Str("deploymentId"))
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (any, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		dep := cloud.DecodeDeployment(s.Data())
		version := dep.Num("accessKeyVersion") + 1
		rotatedAt := doc.NowISO()
		log := core.NewCloudLog(dep.Str("deploymentId"), dep)
		log.Add(tx, core.CloudEntry{Type: "access-key.rotated", Summary: "Access key rotated (version " + core.JSString(version) + ")", Data: doc.Obj("version", version), Actor: c.Account.UID})
		tx.Update(ref, doc.Obj("accessKey", key.Grouped, "accessKeyHash", key.Hash, "accessKeyVersion", version, "accessKeyRotatedAt", rotatedAt, "accessKeyRotatedBy", c.Account.UID, "agentEpoch", dep.Num("agentEpoch")+1).Spread(log.DepUpdate()))
		c.Audit = &cloud.AuditHint{Data: doc.Obj("version", version)}
		return doc.Obj("value", key.Grouped, "hideAfterSec", shared.REVEAL_SECONDS, "accessKey", key.Grouped, "version", version, "rotatedAt", rotatedAt), nil
	})
}

// revealAccessKey: §21 #84: the stored key again (60 s in RevealValue); logged as access-key.revealed + audit.
func revealAccessKey(c *cloud.Ctx) (any, error) {
	dep0, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep0, "deployment.setup", "write"); err != nil {
		return nil, err
	}
	ref := cloud.Col.Deployments().Doc(dep0.Str("deploymentId"))
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (any, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		dep := cloud.DecodeDeployment(s.Data())
		key := dep.Str("accessKey")
		if key == "" {
			return nil, cloud.E.Gone("the access key of this deployment is not stored (created before keys were kept) — regenerate it to view", doc.Obj("reason", "not-stored"))
		}
		version := fld(dep, "accessKeyVersion")
		log := core.NewCloudLog(dep.Str("deploymentId"), dep)
		log.Add(tx, core.CloudEntry{Type: "access-key.revealed", Summary: "Access key viewed (version " + core.JSString(version) + ")", Data: doc.Obj("version", version), Actor: c.Account.UID})
		tx.Update(ref, log.DepUpdate())
		c.Audit = &cloud.AuditHint{Data: doc.Obj("version", version)}
		return doc.Obj("value", key, "hideAfterSec", shared.REVEAL_SECONDS, "accessKey", key, "version", version, "rotatedAt", fld(dep, "accessKeyRotatedAt")), nil
	})
}

func createInstallToken(c *cloud.Ctx) (any, error) {
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
	if err := cloud.OnlyKeys(b, []string{"siteSlug", "replaceActive"}); err != nil {
		return nil, err
	}
	slug := b.Str("siteSlug")
	if slug == "" {
		return nil, cloud.E.Bad("siteSlug", "V_REQUIRED", "siteSlug is required")
	}
	replace, isBool := b.Val("replaceActive").(bool)
	if b.Has("replaceActive") && !isBool {
		return nil, cloud.E.Bad("replaceActive", "V_TYPE", "replaceActive must be true / false")
	}
	if !siteIn(dep0.Map("draft").Arr("sites"), slug) {
		return nil, cloud.E.NotFound("site " + slug + " is not in the draft")
	}
	token := "opi_" + cloud.RandomToken(32)
	tokenHash := cloud.Sha256Hex([]byte(token))
	depID := dep0.Str("deploymentId")
	ref := cloud.Col.Deployments().Doc(depID)
	out, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		dep := cloud.DecodeDeployment(ds.Data())
		agent, err := tx.Get(cloud.Col.Agents(depID).Doc(slug))
		if err != nil {
			return nil, err
		}
		if agent.Exists() {
			return nil, cloud.E.Conflict("site " + slug + " is enrolled (revoke the enrollment first)")
		}
		// Regenerate (replaceActive): revoke this site's active tokens in the same transaction
		var active []*driver.DocSnap
		if replace {
			q, err := tx.Query(cloud.Col.InstallTokens(depID).Where("siteSlug", "==", slug))
			if err != nil {
				return nil, err
			}
			for _, d := range q.Docs {
				if kindOf(d.Data()) == "install" && tokenState(d.Data()) == "active" {
					active = append(active, d)
				}
			}
		}
		now := doc.NowISO()
		me := c.Account.UID
		t := doc.Obj("tokenHash", tokenHash, "siteSlug", slug, "token", token, "expiresAt", doc.IsoPlus(INSTALL_TOKEN_TTL_MS), "usedAt", nil, "revokedAt", nil, "createdAt", now, "createdBy", me)
		log := core.NewCloudLog(depID, dep)
		replaced := []any{}
		for _, d := range active {
			tx.Update(d.Ref(), doc.Obj("revokedAt", now, "token", nil))
			log.Add(tx, core.CloudEntry{Type: "install-token.revoked", Summary: "Install token for site " + slug + " revoked (replaced by a new install command)", Data: doc.Obj("siteSlug", slug, "tokenHash", d.ID()), Actor: me})
			replaced = append(replaced, d.ID())
		}
		tx.Create(cloud.Col.InstallTokens(depID).Doc(tokenHash), t)
		data := doc.Obj("siteSlug", slug, "tokenHash", tokenHash, "expiresAt", t.Val("expiresAt"))
		if replace {
			data.Set("replaced", float64(len(active)))
		}
		log.Add(tx, core.CloudEntry{Type: "install-token.created", Summary: "Install command created for site " + slug, Data: data, Actor: me})
		tx.Update(ref, log.DepUpdate())
		audit := doc.Obj("siteSlug", slug, "tokenHash", tokenHash)
		if replace {
			audit.Set("replaced", replaced)
		}
		c.Audit = &cloud.AuditHint{Data: audit}
		return t, nil
	})
	if err != nil {
		return nil, err
	}
	return installTokenResponse(c, out), nil
}

// revealInstallCommand: §21 #84: the install command of an ACTIVE token again; gone reason used | revoked | expired | not-stored.
func revealInstallCommand(c *cloud.Ctx) (any, error) {
	dep0, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep0, "deployment.setup", "write"); err != nil {
		return nil, err
	}
	depID := dep0.Str("deploymentId")
	ref := cloud.Col.Deployments().Doc(depID)
	tRef := cloud.Col.InstallTokens(depID).Doc(c.Params["tokenHash"])
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (any, error) {
		ds, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		ts, err := tx.Get(tRef)
		if err != nil {
			return nil, err
		}
		if !ts.Exists() {
			return nil, cloud.E.NotFound("install token not found")
		}
		t := ts.Data()
		c.Audit = &cloud.AuditHint{Data: doc.Obj("siteSlug", fld(t, "siteSlug"), "tokenHash", fld(t, "tokenHash"))}
		state := tokenState(t)
		if kindOf(t) == "maintenance" {
			return nil, cloud.E.Gone(`this is the upgrade command of an enrolled site — view it with the site's "Upgrade / reinstall command"`, doc.Obj("reason", "maintenance"))
		}
		switch state {
		case "used":
			return nil, cloud.E.Gone("the install token was already used (the site enrolled) — create a new install command", doc.Obj("reason", "used"))
		case "revoked":
			return nil, cloud.E.Gone("the install token was revoked — create a new install command", doc.Obj("reason", "revoked"))
		case "expired":
			return nil, cloud.E.Gone("the install token expired — create a new install command", doc.Obj("reason", "expired"))
		}
		if t.Str("token") == "" {
			return nil, cloud.E.Gone("the install command of this token is not stored (created before commands were kept) — regenerate it to view", doc.Obj("reason", "not-stored"))
		}
		dep := cloud.DecodeDeployment(ds.Data())
		log := core.NewCloudLog(depID, dep)
		log.Add(tx, core.CloudEntry{Type: "install-token.revealed", Summary: "Install command for site " + t.Str("siteSlug") + " viewed", Data: doc.Obj("siteSlug", fld(t, "siteSlug"), "tokenHash", fld(t, "tokenHash")), Actor: c.Account.UID})
		tx.Update(ref, log.DepUpdate())
		return installTokenResponse(c, t), nil
	})
}

// enrolledSite reads the deployment, the agent and the site's tokens of the upgrade command endpoints.
func enrolledSite(tx *driver.Tx, ref, aRef *driver.DocRef, depID, slug, notEnrolled string) (ds *driver.DocSnap, a *doc.Map, toks []*doc.Map, err error) {
	if ds, err = tx.Get(ref); err != nil {
		return
	}
	as, err := tx.Get(aRef)
	if err != nil {
		return
	}
	tq, err := tx.Query(cloud.Col.InstallTokens(depID).Where("siteSlug", "==", slug))
	if err != nil {
		return
	}
	if !as.Exists() {
		return nil, nil, nil, cloud.E.NotFound(notEnrolled)
	}
	for _, d := range tq.Docs {
		toks = append(toks, d.Data())
	}
	return ds, as.Data(), toks, nil
}

// revealUpgradeCommand: §21 #92: the enrolled site's upgrade / reinstall command (60 s); install-command.revealed + audit.
func revealUpgradeCommand(c *cloud.Ctx) (any, error) {
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
	ref := cloud.Col.Deployments().Doc(depID)
	aRef := cloud.Col.Agents(depID).Doc(slug)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (any, error) {
		ds, a, toks, err := enrolledSite(tx, ref, aRef, depID, slug, "site "+slug+" is not enrolled (create an install command instead)")
		if err != nil {
			return nil, err
		}
		t := UpgradeTokenOf(toks, a)
		if t != nil {
			c.Audit = &cloud.AuditHint{Data: doc.Obj("siteSlug", slug, "tokenHash", fld(t, "tokenHash"), "kind", kindOf(t))}
		}
		if !hasPlain(t) {
			return nil, cloud.E.Gone("the upgrade command of this site is not stored (its token was created before commands were kept) — Regenerate to view a new one", doc.Obj("reason", "not-stored"))
		}
		dep := cloud.DecodeDeployment(ds.Data())
		log := core.NewCloudLog(depID, dep)
		log.Add(tx, core.CloudEntry{Type: "install-command.revealed", Summary: "Upgrade / reinstall command for site " + slug + " viewed", Data: doc.Obj("siteSlug", slug, "tokenHash", fld(t, "tokenHash"), "kind", kindOf(t)), Actor: c.Account.UID})
		tx.Update(ref, log.DepUpdate())
		return upgradeCommandResponse(c, t, fld(a, "enrolledAt")), nil
	})
}

// regenerateUpgradeCommand: §21 #92 Regenerate: a new site-bound maintenance token; the previous upgrade command (the
// enrollment's install token and older maintenance tokens) is revoked in the same transaction.
func regenerateUpgradeCommand(c *cloud.Ctx) (any, error) {
	dep0, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep0, "deployment.setup", "write"); err != nil {
		return nil, err
	}
	slug := c.Params["siteSlug"]
	c.Audit = &cloud.AuditHint{Data: doc.Obj("siteSlug", slug)}
	token := "opi_" + cloud.RandomToken(32)
	tokenHash := cloud.Sha256Hex([]byte(token))
	depID := dep0.Str("deploymentId")
	ref := cloud.Col.Deployments().Doc(depID)
	aRef := cloud.Col.Agents(depID).Doc(slug)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (any, error) {
		ds, a, toks, err := enrolledSite(tx, ref, aRef, depID, slug, "site "+slug+" is not enrolled (create an install command instead)")
		if err != nil {
			return nil, err
		}
		dep := cloud.DecodeDeployment(ds.Data())
		now := doc.NowISO()
		revoked := []any{}
		for _, o := range enrollmentTokens(toks, a) {
			tx.Update(cloud.Col.InstallTokens(depID).Doc(o.Str("tokenHash")), doc.Obj("revokedAt", now, "token", nil))
			revoked = append(revoked, fld(o, "tokenHash"))
		}
		t := doc.Obj("tokenHash", tokenHash, "siteSlug", slug, "kind", "maintenance", "token", token, "expiresAt", nil, "boundEnrolledAt", fld(a, "enrolledAt"), "usedAt", nil, "revokedAt", nil, "createdAt", now, "createdBy", c.Account.UID)
		tx.Create(cloud.Col.InstallTokens(depID).Doc(tokenHash), t)
		summary := "Upgrade / reinstall command for site " + slug + " regenerated"
		if len(revoked) > 0 {
			summary += " (the previous command no longer works)"
		}
		log := core.NewCloudLog(depID, dep)
		log.Add(tx, core.CloudEntry{Type: "install-command.regenerated", Summary: summary, Data: doc.Obj("siteSlug", slug, "tokenHash", tokenHash, "revoked", revoked), Actor: c.Account.UID})
		tx.Update(ref, log.DepUpdate())
		c.Audit = &cloud.AuditHint{Data: doc.Obj("siteSlug", slug, "tokenHash", tokenHash, "kind", "maintenance", "revoked", revoked)}
		return upgradeCommandResponse(c, t, fld(a, "enrolledAt")), nil
	})
}

func revokeInstallToken(c *cloud.Ctx) (any, error) {
	dep0, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep0, "deployment.setup", "write"); err != nil {
		return nil, err
	}
	depID := dep0.Str("deploymentId")
	ref := cloud.Col.Deployments().Doc(depID)
	tRef := cloud.Col.InstallTokens(depID).Doc(c.Params["tokenHash"])
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ds, err := tx.Get(ref)
		if err != nil {
			return err
		}
		ts, err := tx.Get(tRef)
		if err != nil {
			return err
		}
		if !ts.Exists() {
			return cloud.E.NotFound("install token not found")
		}
		t := ts.Data()
		if t.Truthy("usedAt") {
			return cloud.E.Gone("the install token was already used")
		}
		if t.Truthy("revokedAt") {
			return nil
		}
		dep := cloud.DecodeDeployment(ds.Data())
		tx.Update(tRef, doc.Obj("revokedAt", doc.NowISO(), "token", nil))
		log := core.NewCloudLog(depID, dep)
		log.Add(tx, core.CloudEntry{Type: "install-token.revoked", Summary: "Install token for site " + t.Str("siteSlug") + " revoked", Data: doc.Obj("siteSlug", fld(t, "siteSlug"), "tokenHash", fld(t, "tokenHash")), Actor: c.Account.UID})
		tx.Update(ref, log.DepUpdate())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("revoked", true), nil
}

func revokeAgent(c *cloud.Ctx) (any, error) {
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
	if b.Val("confirm") != slug {
		return nil, cloud.E.Bad("confirm", "V_FORMAT", "confirm must equal the site slug")
	}
	depID := dep0.Str("deploymentId")
	ref := cloud.Col.Deployments().Doc(depID)
	aRef := cloud.Col.Agents(depID).Doc(slug)
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ds, a, toks, err := enrolledSite(tx, ref, aRef, depID, slug, "site "+slug+" is not enrolled")
		if err != nil {
			return err
		}
		dep := cloud.DecodeDeployment(ds.Data())
		pub := a.Str("publicKey")
		fp := cloud.KeyFingerprint(pub)
		now := doc.NowISO()
		tx.Set(cloud.Col.RevokedKeys(depID).Doc(fp), doc.Obj("siteSlug", slug, "publicKey", pub, "revokedAt", now))
		tx.Delete(aRef)
		// §21 #92: the enrollment's upgrade command dies with it (maintenance tokens revoked; the used install token loses its plaintext)
		ended := enrollmentTokens(toks, a)
		for _, t := range ended {
			upd := doc.Obj("token", nil)
			if kindOf(t) == "maintenance" {
				upd = doc.Obj("revokedAt", now, "token", nil)
			}
			tx.Update(cloud.Col.InstallTokens(depID).Doc(t.Str("tokenHash")), upd)
		}
		log := core.NewCloudLog(depID, dep)
		log.Add(tx, core.CloudEntry{Type: "agent.revoked", Summary: "Enrollment of site " + slug + " revoked", Data: doc.Obj("siteSlug", slug, "publicKeyFingerprint", fp, "upgradeCommandsEnded", float64(len(ended))), Actor: c.Account.UID})
		tx.Update(ref, log.DepUpdate().Set("agentEpoch", dep.Num("agentEpoch")+1))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("revoked", true), nil
}
