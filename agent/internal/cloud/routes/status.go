package routes

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// Status + uptime, activities list, manual .opmlog upload (routes/status.ts).

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"getStatus":        getStatus,
		"listActivities":   listActivities,
		"uploadActivities": uploadActivities,
	})
}

// BUNDLE_MAX is BUNDLE_MAX.
const BUNDLE_MAX = 24 * 1024 * 1024

func last30Days(now time.Time) []string {
	out := make([]string, 0, 30)
	n := now.UTC()
	for i := 29; i >= 0; i-- {
		out = append(out, cloud.YMD(time.Date(n.Year(), n.Month(), n.Day()-i, 0, 0, 0, 0, time.UTC)))
	}
	return out
}

func getStatus(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	id := dep.Str("deploymentId")
	// O2: the deployment BRANCH head (§21 #104; the legacy dep.head only before migration), and each agent's reported
	// head resolved through its agent branch (branch-mode agents report an agent-branch commit id, not a revision)
	agents, err := cloud.Col.Agents(id).Get()
	if err != nil {
		return nil, err
	}
	headConfig, err := core.DeploymentHeadConfig(dep)
	if err != nil {
		return nil, err
	}
	release := core.AgentReleaseInfo(core.LoadDepsManifest())
	now := time.Now().UnixMilli()
	headSites := headConfig.Arr("sites")
	draftSites := dep.Map("draft").Arr("sites")
	labels := map[string]any{}
	slugList := []string{}
	for _, s := range mapsIn(headSites) {
		labels[s.Str("siteSlug")] = fld(s, "label")
		slugList = append(slugList, s.Str("siteSlug"))
	}
	for _, s := range mapsIn(draftSites) {
		if _, ok := labels[s.Str("siteSlug")]; !ok {
			labels[s.Str("siteSlug")] = fld(s, "label")
		}
		slugList = append(slugList, s.Str("siteSlug"))
	}
	agentBy := map[string]*doc.Map{}
	// head seq of every site: the deployment seq it runs
	seqOf := map[string]any{}
	for _, d := range agents.Docs {
		a := core.AgentView(d.Data())
		agentBy[d.ID()] = a
		slugList = append(slugList, d.ID())
		if h := a.Str("headRevId"); h != "" {
			r, err := core.SiteRunningCommit(id, d.ID(), h)
			if err != nil {
				return nil, err
			}
			seqOf[d.ID()] = fldOr(r, "deploymentSeq", nil)
		}
	}
	slugs := sortedSet(slugList)
	sites := make([]any, len(slugs))
	for i, slug := range slugs {
		a := agentBy[slug]
		var label any = slug
		if l, ok := labels[slug]; ok && l != nil && l != doc.Undefined {
			label = l
		}
		var headSeq any
		if a.Truthy("headRevId") {
			headSeq = seqOf[slug]
		}
		sites[i] = doc.Obj(
			"siteSlug", slug, "label", label, "inHead", headConfig != nil && siteIn(headSites, slug), "enrolled", a != nil, "online", fldOr(a, "online", false),
			"lastSeenAt", fldOr(a, "lastSeenAt", nil), "relayVia", fldOr(a, "relayVia", nil), "agentVersion", fldOr(a, "agentVersion", nil), "headRevId", fldOr(a, "headRevId", nil), "headSeq", headSeq,
			"staging", fldOr(a, "staging", nil), "keyVersion", fldOr(a, "keyVersion", nil), "metrics", fldOr(a, "metrics", nil), "edge", fldOr(a, "edge", nil),
			// round C extension (not yet in shared SiteCloudStatus): the site's mesh / auto host + detected addresses
			"advertiseHost", fldOr(a, "advertiseHost", nil), "detectedAddresses", fldOr(a, "detectedAddresses", []any{}),
			"upgrade", mapOrNull(core.UpgradeViewOf(a, release, now)), "clockSkew", fldOr(a, "clockSkew", nil),
		)
	}
	days := last30Days(time.Now())
	today := days[len(days)-1]
	curHour := time.Now().UTC().Hour()
	refs := make([]*driver.DocRef, 0, len(slugs)*len(days))
	for _, s := range slugs {
		for _, d := range days {
			refs = append(refs, cloud.Col.Uptime(id).Doc(s+"_"+d))
		}
	}
	docs := map[string]*doc.Map{}
	for i := 0; i < len(refs); i += 300 {
		snaps, err := cloud.DB().GetAll(refs[i:min(i+300, len(refs))]...)
		if err != nil {
			return nil, err
		}
		for _, s := range snaps {
			if s.Exists() {
				docs[s.ID()] = s.Data()
			}
		}
	}
	uptime := make([]any, len(slugs))
	for i, slug := range slugs {
		list := make([]any, len(days))
		for j, date := range days {
			hours := docs[slug+"_"+date].Arr("hours")
			if len(hours) != 24 {
				hours = make([]any, 24)
				for h := range hours {
					hours[h] = 0.0
				}
			}
			n := 24
			if date == today {
				n = curHour + 1
			}
			sum := 0.0
			for _, h := range hours[:n] {
				f, _ := doc.AsNumber(h)
				sum += f
			}
			list[j] = doc.Obj("date", date, "onlineFraction", math.Floor(sum/float64(n)*10000+0.5)/10000, "hours", hours)
		}
		uptime[i] = doc.Obj("siteSlug", slug, "days", list)
	}
	return doc.Obj("serverTime", doc.NowISO(), "sites", sites, "uptime", uptime), nil
}

func listActivities(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.activities", "read"); err != nil {
		return nil, err
	}
	q0 := c.Query
	limit, err := core.ClampLimit(c, 100, 500)
	if err != nil {
		return nil, err
	}
	site, origin, typ, actor, via, from, to := q0.Get("site"), q0.Get("origin"), q0.Get("type"), q0.Get("actor"), q0.Get("via"), q0.Get("from"), q0.Get("to")
	var errs []string
	if origin != "" && !slices.Contains([]string{"agent", "cloud", "system"}, origin) {
		errs = append(errs, "origin must be agent | cloud | system")
	}
	if via != "" && via != "pipe" && via != "manual" {
		errs = append(errs, "via must be pipe | manual")
	}
	if from != "" && !validISO(from) {
		errs = append(errs, "from must be an ISO time")
	}
	if to != "" && !validISO(to) {
		errs = append(errs, "to must be an ISO time")
	}
	if typ != "" && !reActivityFilter.MatchString(typ) {
		errs = append(errs, `type must be an activity type or a prefix ending in "."`)
	}
	if len(errs) > 0 {
		return nil, cloud.E.Invalid(queryIssues(errs))
	}
	acts := cloud.Col.Activities(dep.Str("deploymentId"))
	// one equality filter runs in the store; the others are applied here
	f := &filterRun{q: acts.Query}
	eq := func(k, v string) func(*doc.Map) bool { return func(a *doc.Map) bool { return a.Val(k) == v } }
	if site != "" {
		f.use("siteSlug", site, "==", eq("siteSlug", site))
	}
	if typ != "" {
		f.use("_typeKeys", typ, "array-contains", func(a *doc.Map) bool {
			if strings.HasSuffix(typ, ".") {
				return strings.HasPrefix(a.Str("type"), typ)
			}
			return a.Val("type") == typ
		})
	}
	if actor != "" {
		f.use("actor", actor, "==", eq("actor", actor))
	}
	if origin != "" {
		f.use("origin", origin, "==", eq("origin", origin))
	}
	if via != "" {
		f.use("uploadedVia", via, "==", eq("uploadedVia", via))
	}
	if from != "" {
		ms, _ := doc.ParseISO(from)
		f.q = f.q.Where("_tsMs", ">=", ms)
	}
	if to != "" {
		ms, _ := doc.ParseISO(to)
		f.q = f.q.Where("_tsMs", "<=", ms)
	}
	f.q = f.q.OrderBy("_tsMs", driver.Desc).OrderBy("seq", driver.Desc)
	var after *driver.DocSnap
	if cursor := q0.Get("cursor"); cursor != "" {
		if after, err = acts.Doc(cursor).Get(); err != nil {
			return nil, err
		}
		if !after.Exists() {
			return nil, cloud.E.Bad("cursor", "V_FORMAT", "bad cursor")
		}
	}
	items, err := f.page(after, limit, cloud.DecodeActivity)
	if err != nil {
		return nil, err
	}
	return pageOf(items, limit, "activityId"), nil
}

// asciiText is buffer.toString('ascii').
func asciiText(b []byte) string {
	o := make([]byte, len(b))
	for i, x := range b {
		o[i] = x & 0x7f
	}
	return string(o)
}

func uploadActivities(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.activities", "write"); err != nil {
		return nil, err
	}
	if len(c.Body) > BUNDLE_MAX {
		return nil, cloud.E.TooLarge("the bundle is larger than " + strconv.Itoa(BUNDLE_MAX) + " bytes")
	}
	entries, err := core.ReadTar(c.Body)
	if err != nil {
		return nil, cloud.E.Bad("body", "V_FORMAT", "not a .opmlog (ustar) file: "+err.Error())
	}
	get := func(n string) ([]byte, bool) {
		for _, e := range entries {
			if e.Name == n {
				return e.Data, true
			}
		}
		return nil, false
	}
	manBytes, ok1 := get("manifest.json")
	sig, ok2 := get("signature.sig")
	nd, ok3 := get("activities.ndjson")
	if !ok1 || !ok2 || !ok3 {
		return nil, cloud.E.Bad("body", "V_REQUIRED", ".opmlog needs manifest.json, activities.ndjson and signature.sig")
	}
	parsed, err := doc.ParseString(core.Utf8(manBytes))
	if err != nil {
		return nil, cloud.E.Bad("manifest.json", "V_FORMAT", "manifest.json is not JSON")
	}
	if parsed == nil {
		return nil, errors.New("manifest.json is null") // man.format throws: 500
	}
	man, _ := parsed.(*doc.Map)
	depID := dep.Str("deploymentId")
	if man.Val("format") != "opmlog/1" {
		return nil, cloud.E.Bad("manifest.format", "V_ENUM", "format must be opmlog/1")
	}
	if man.Val("deploymentId") != depID {
		return nil, cloud.E.Bad("manifest.deploymentId", "V_REF", "the bundle belongs to another deployment")
	}
	signer := man.Map("signedBy")
	if !signer.Truthy("siteSlug") || !signer.Truthy("publicKey") {
		return nil, cloud.E.Bad("manifest.signedBy", "V_REQUIRED", "signedBy is required")
	}
	signerSite, ok1 := signer.StrOK("siteSlug")
	publicKey, ok2 := signer.StrOK("publicKey")
	if !ok1 || !ok2 {
		return nil, errors.New("manifest.signedBy holds a non-string") // the TS driver throws on such a document id / key: 500
	}
	agentSnap, err := cloud.Col.Agents(depID).Doc(signerSite).Get()
	if err != nil {
		return nil, err
	}
	revokedSnap, err := cloud.Col.RevokedKeys(depID).Doc(cloud.KeyFingerprint(publicKey)).Get()
	if err != nil {
		return nil, err
	}
	known := (agentSnap.Exists() && agentSnap.Data().Val("publicKey") == publicKey) ||
		(revokedSnap.Exists() && revokedSnap.Data().Val("siteSlug") == signerSite && revokedSnap.Data().Val("publicKey") == publicKey)
	if !known {
		return nil, cloud.E.Bad("manifest.signedBy", "V_REF", "the signer key is not a key of site "+signerSite+" (enrolled or revoked)")
	}
	if !cloud.VerifyEd25519(publicKey, manBytes, doc.JSTrim(asciiText(sig))) {
		return nil, cloud.E.Bad("signature.sig", "V_FORMAT", "bad signature: the manifest is not signed by the site key")
	}
	act := man.Map("activities")
	if n, ok := act.NumOK("sizeBytes"); !ok || n != float64(len(nd)) || act.Val("sha256") != "sha256:"+cloud.Sha256Hex(nd) {
		return nil, cloud.E.Bad("activities.ndjson", "V_FORMAT", "activities.ndjson does not match the manifest (size / sha256)")
	}
	bySite := map[string][]any{}
	errorsOut := []any{}
	rejected, inserted, skipped := 0.0, 0.0, 0.0
	i := 0
	for _, line := range strings.Split(core.Utf8(nd), "\n") {
		if doc.JSTrim(line) == "" {
			continue
		}
		i++
		v, err := doc.ParseString(line)
		if err != nil {
			rejected++
			errorsOut = append(errorsOut, doc.Obj("activityId", "line:"+strconv.Itoa(i), "reason", "not JSON"))
			continue
		}
		m, _ := v.(*doc.Map)
		slug, ok := m.StrOK("siteSlug")
		if !ok {
			rejected++
			errorsOut = append(errorsOut, doc.Obj("activityId", "line:"+strconv.Itoa(i), "reason", "no siteSlug"))
			continue
		}
		bySite[slug] = append(bySite[slug], m)
	}
	slugs := make([]string, 0, len(bySite))
	for s := range bySite {
		slugs = append(slugs, s)
	}
	slices.Sort(slugs)
	sites := []any{}
	for _, slug := range slugs {
		r, err := core.IngestSiteLog(depID, slug, bySite[slug], "manual")
		if err != nil {
			return nil, err
		}
		inserted += r.Num("inserted")
		skipped += r.Num("skipped")
		rejected += r.Num("rejected")
		errorsOut = append(errorsOut, r.Arr("errors")...)
		sites = append(sites, doc.Obj("siteSlug", slug, "fromSeq", fld(r, "fromSeq"), "toSeq", fld(r, "toSeq")))
	}
	rnd := make([]byte, 4)
	if _, err := rand.Read(rnd); err != nil {
		return nil, err
	}
	name := strings.NewReplacer(":", "-", ".", "-").Replace(doc.NowISO()) + "-" + hex.EncodeToString(rnd) + ".opmlog"
	if err := core.WriteBytes(core.Paths.Bundle(depID, name), c.Body, ""); err != nil {
		return nil, err
	}
	num := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	err = core.AppendCloud(depID, core.CloudEntry{
		Type:    "activities.uploaded",
		Summary: ".opmlog signed by site " + signerSite + " uploaded: " + num(inserted) + " inserted, " + num(skipped) + " skipped, " + num(rejected) + " rejected",
		Data:    doc.Obj("signedBy", signerSite, "inserted", inserted, "skipped", skipped, "rejected", rejected, "sites", sites, "bundle", name, "sizeBytes", float64(len(c.Body))),
		Actor:   c.Account.UID,
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("inserted", inserted, "skipped", skipped, "rejected", rejected, "errors", errorsOut[:min(1000, len(errorsOut))], "sites", sites, "signatureOk", true), nil
}
