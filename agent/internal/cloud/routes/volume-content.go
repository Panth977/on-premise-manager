package routes

// Port of backend/src/routes/volume-content.ts (§21 #101 volume content + persist-to-cloud volumes). What other files need
// lives in core/route_volume_content.go (vcCol, storeTree, readTree, the cloud items, volumeSync, seedBlobContents).
// createVersionBody, snapshotAncestor and excludedIssues have no caller in the TS and are not ported. Helpers of this
// file are prefixed vc.

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"listVolumeContent":           listVolumeContent,
		"getVolumeContent":            getVolumeContent,
		"listVolumeContentVersions":   listVolumeContentVersions,
		"getVolumeContentVersion":     getVolumeContentVersion,
		"createVolumeContentVersion":  vcGone,
		"restoreVolumeContentVersion": vcGone,
		"requestVolumeSnapshot":       vcGone,
		"sendVolumeContent":           vcGone,
		"cancelVolumeContentPush":     vcGone,
		"agentCheckBlobs":             agentCheckBlobs,
		"agentPutBlob":                agentPutBlob,
		"agentCreateVolumeSnapshot":   agentCreateVolumeSnapshot,
		"agentReportVolumePush":       agentReportVolumePush,
	})
}

func vcInt(v any) bool {
	return agIsInt(v) && v.(float64) >= 0
}

// vcIso is isIso: `typeof v === 'string' && v.length <= 40 && Number.isFinite(Date.parse(v))` for the ISO forms OPM writes.
func vcIso(v any) bool {
	s, ok := v.(string)
	if !ok || doc.JSLen(s) > 40 {
		return false
	}
	_, ok = doc.ParseISO(s)
	return ok
}

func vcVersions(deploymentID, key string) *driver.CollectionRef {
	return core.VcCol(deploymentID).Doc(key).Collection("versions")
}

func vcPushes(deploymentID, key string) *driver.CollectionRef {
	return core.VcCol(deploymentID).Doc(key).Collection("pushes")
}

// vcDecodeState is decodeState: the stored state document (fields may be missing on a doc created by a heartbeat).
func vcDecodeState(deploymentID, siteSlug, volumeSlug string, d *doc.Map) *doc.Map {
	return doc.Obj("deploymentId", deploymentID, "siteSlug", siteSlug, "volumeSlug", volumeSlug, "latest", agOr(d, "latest", nil), "lastSnapshot", agOr(d, "lastSnapshot", nil), "versionCount", agOr(d, "versionCount", 0.0), "lastSeq", agOr(d, "lastSeq", 0.0), "push", agOr(d, "push", nil), "snapshotRequest", agOr(d, "snapshotRequest", nil), "live", agOr(d, "live", nil), "updatedAt", agOr(d, "updatedAt", nil), "_open", d.Truthy("_open"))
}

func vcOpenOf(s *doc.Map) bool {
	return s.Map("push").Val("status") == any("pending") || s.Truthy("snapshotRequest")
}

func vcSummaryOf(v *doc.Map) *doc.Map {
	return doc.Obj("versionId", agPresent(v, "versionId"), "seq", agPresent(v, "seq"), "origin", agPresent(v, "origin"), "treeHash", agPresent(v, "treeHash"), "createdAt", agPresent(v, "createdAt"), "createdBy", agPresent(v, "createdBy"))
}

func vcStateView(s *doc.Map, persist any, persistInHead bool) *doc.Map {
	return doc.Obj("deploymentId", agPresent(s, "deploymentId"), "siteSlug", agPresent(s, "siteSlug"), "volumeSlug", agPresent(s, "volumeSlug"), "persist", persist, "persistInHead", persistInHead, "latest", agPresent(s, "latest"), "lastSnapshot", agPresent(s, "lastSnapshot"), "versionCount", agPresent(s, "versionCount"), "push", agPresent(s, "push"), "snapshotRequest", agPresent(s, "snapshotRequest"), "live", agPresent(s, "live"), "sync", shared.CloudSyncState(s), "updatedAt", agPresent(s, "updatedAt"))
}

// ─────────────────────────────────────────── configs: draft / head persist settings

// vcPersistOf is persistOf: the persist setting of a volume, nil when off. sc may be nil.
func vcPersistOf(sc *doc.Map, volumeSlug string) any {
	for _, x := range sc.Arr("volumes") {
		v, _ := x.(*doc.Map)
		if v.Val("volumeSlug") == any(volumeSlug) && v.Val("kind") != any("file") {
			return agOr(v, "persist", nil)
		}
	}
	return nil
}

func vcSiteConfig(cfg *doc.Map, siteSlug string) *doc.Map {
	for _, s := range agList(cfg, "sites") {
		if m := s.(*doc.Map); m.Val("siteSlug") == any(siteSlug) {
			return m.Map("config")
		}
	}
	return nil
}

func vcDraftSite(dep *doc.Map, siteSlug string) *doc.Map {
	return vcSiteConfig(dep.Map("draft"), siteSlug)
}

func vcLegacyHeadConfig(dep *doc.Map) (*doc.Map, error) {
	if !dep.Truthy("head") {
		return nil, nil
	}
	r, err := cloud.Col.Revisions(dep.Str("deploymentId")).Doc(dep.Map("head").Str("revId")).Get()
	if err != nil || !r.Exists() {
		return nil, err
	}
	return cloud.DecodeRevision(r.Data()).Map("config"), nil
}

// HeadSiteConfigs is headSiteConfigs: siteSlug → the site config that site runs: its own reported head (a legacy revision
// or, O3, an agent-branch commit), else the legacy deployment head revision.
func HeadSiteConfigs(dep *doc.Map) (map[string]*doc.Map, error) {
	deploymentID := dep.Str("deploymentId")
	agents, err := cloud.Col.Agents(deploymentID).Select("headRevId").Get()
	if err != nil {
		return nil, err
	}
	bySite := map[string]string{}
	for _, a := range agents.Docs {
		if s, ok := a.Data().StrOK("headRevId"); ok {
			bySite[a.ID()] = s
		}
	}
	depHead, err := vcLegacyHeadConfig(dep)
	if err != nil {
		return nil, err
	}
	sites := map[string]bool{}
	for s := range bySite {
		sites[s] = true
	}
	if depHead != nil {
		for _, s := range agList(depHead, "sites") {
			sites[s.(*doc.Map).Str("siteSlug")] = true
		}
	}
	out := map[string]*doc.Map{}
	for site := range sites {
		run, err := core.SiteRunningCommit(deploymentID, site, bySite[site])
		if err != nil {
			return nil, err
		}
		cfg := depHead
		if rc := run.Map("config"); rc != nil {
			cfg = rc
		}
		if cfg != nil {
			if sc := vcSiteConfig(cfg, site); sc != nil {
				out[site] = sc
			}
		}
	}
	return out, nil
}

func vcHeadSiteConfig(dep *doc.Map, siteSlug string) (*doc.Map, error) {
	deploymentID := dep.Str("deploymentId")
	a, err := cloud.Col.Agents(deploymentID).Doc(siteSlug).Get()
	if err != nil {
		return nil, err
	}
	run, err := core.SiteRunningCommit(deploymentID, siteSlug, a.Data().Str("headRevId"))
	if err != nil {
		return nil, err
	}
	cfg := run.Map("config")
	if cfg == nil {
		if cfg, err = vcLegacyHeadConfig(dep); err != nil {
			return nil, err
		}
	}
	if cfg == nil {
		return nil, nil
	}
	return vcSiteConfig(cfg, siteSlug), nil
}

// vcSignerHeadConfig: the persist setting of the signing site's head (agents): a legacy revision, else (O3) the agent-branch
// commit with that id.
func vcSignerHeadConfig(deploymentID, siteSlug, headRevID string) (*doc.Map, error) {
	run, err := core.SiteRunningCommit(deploymentID, siteSlug, headRevID)
	if err != nil {
		return nil, err
	}
	if cfg := run.Map("config"); cfg != nil {
		return vcSiteConfig(cfg, siteSlug), nil
	}
	return nil, nil
}

type vcTarget struct {
	dep                       *doc.Map
	siteSlug, volumeSlug      string
	key                       string
	state                     *doc.Map
	draftPersist, headPersist any
}

// vcLoadTarget: a persist-to-cloud volume of a deployment: persist on in the draft or in the head, or stored versions exist
// → else 404.
func vcLoadTarget(c *cloud.Ctx, action string) (*vcTarget, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", action); err != nil {
		return nil, err
	}
	siteSlug, volumeSlug := c.Params["siteSlug"], c.Params["volumeSlug"]
	if !shared.RE.Slug.MatchString(siteSlug) || !shared.RE.VolumeSlug.MatchString(volumeSlug) {
		return nil, cloud.E.NotFound("volume not found")
	}
	deploymentID := dep.Str("deploymentId")
	key := core.VcKey(siteSlug, volumeSlug)
	s, err := core.VcCol(deploymentID).Doc(key).Get()
	if err != nil {
		return nil, err
	}
	head, err := vcHeadSiteConfig(dep, siteSlug)
	if err != nil {
		return nil, err
	}
	draftPersist := vcPersistOf(vcDraftSite(dep, siteSlug), volumeSlug)
	headPersist := vcPersistOf(head, volumeSlug)
	if !doc.Truthy(draftPersist) && !doc.Truthy(headPersist) && !(s.Exists() && s.Data().Num("versionCount") > 0) {
		return nil, cloud.E.NotFound("volume " + volumeSlug + " of site " + siteSlug + " is not persisted to the cloud")
	}
	return &vcTarget{dep, siteSlug, volumeSlug, key, vcDecodeState(deploymentID, siteSlug, volumeSlug, s.Data()), draftPersist, headPersist}, nil
}

// ─────────────────────────────────────────── trees + blobs

// MissingBlobs is missingBlobs: sha256s (sha256:<hex>) not stored as files/<hex>, sorted.
func MissingBlobs(shas []string) ([]string, error) {
	seen := map[string]bool{}
	uniq := []string{}
	for _, s := range shas {
		if !seen[s] {
			seen[s] = true
			uniq = append(uniq, s)
		}
	}
	ok, err := core.Pool(uniq, 25, func(s string) (bool, error) { return core.Exists(core.Paths.File(cloud.HexOf(s))) })
	if err != nil {
		return nil, err
	}
	out := []string{}
	for i, s := range uniq {
		if !ok[i] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

func vcTreeShas(t *doc.Map) []string {
	l := agList(t, "entries")
	out := make([]string, len(l))
	for i, e := range l {
		out[i] = e.(*doc.Map).Str("sha256")
	}
	return out
}

func vcTreeBodyIssues(tree any) ([]any, error) {
	const at = "tree"
	list, err := shared.VolumeTreeIssues(tree, false)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(list))
	for i, x := range list {
		m := x.(*doc.Map)
		p := at
		if m.Truthy("path") {
			p = at + ".entries[" + core.JSString(m.Val("path")) + "]"
		}
		out[i] = doc.Obj("path", p, "code", agPresent(m, "code"), "message", agPresent(m, "message"), "severity", "error")
	}
	return out, nil
}

func vcTotalTooLarge(t *doc.Map) *doc.Map {
	if shared.TreeStats(t).Num("totalBytes") > float64(shared.VOLUME_CONTENT_LIMITS.PersistMaxBytes) {
		return agIssue("tree", "V_TOO_LARGE", "a persisted version is at most "+core.JSString(float64(shared.VOLUME_CONTENT_LIMITS.PersistMaxBytes)/1048576)+" MiB in total")
	}
	return nil
}

func vcBlobMissing(missing []string) error {
	shown := missing
	if len(shown) > 3 {
		shown = shown[:3]
	}
	more := ""
	if len(missing) > 3 {
		more = ", …"
	}
	e := agInvalid([]any{agIssue("tree", "V_BLOB_MISSING", fmt.Sprintf("%d file(s) of the tree are not uploaded (%s%s)", len(missing), strings.Join(shown, ", "), more))})
	l := make([]any, len(missing))
	for i, s := range missing {
		l[i] = s
	}
	e.Details.Set("missing", l)
	return e
}

// vcDecodeVersion: stored version → API (docs written before restoredFromVersionId existed default it to null).
func vcDecodeVersion(d *doc.Map) *doc.Map {
	return d.Shallow().Set("restoredFromVersionId", agOr(d, "restoredFromVersionId", nil))
}

func vcLoadVersion(deploymentID, key, versionID string) (*doc.Map, error) {
	s, err := vcVersions(deploymentID, key).Doc(versionID).Get()
	if err != nil || !s.Exists() {
		return nil, err
	}
	return vcDecodeVersion(s.Data()), nil
}

// vcBuildVersion: a version doc (seq = state.lastSeq + 1); changes against the previous latest tree. n: the fields of
// NewVersion; prevTree may be nil.
func vcBuildVersion(deploymentID, siteSlug, volumeSlug string, state, n, tree, prevTree *doc.Map) *doc.Map {
	st := shared.TreeStats(tree)
	changes := doc.Obj("added", 0.0, "removed", 0.0, "modified", 0.0)
	for _, x := range shared.DiffVolumeTrees(prevTree, tree) {
		k := x.(*doc.Map).Str("kind")
		if k == "changed" {
			k = "modified"
		}
		changes.Set(k, changes.Num(k)+1)
	}
	return doc.Obj(
		"versionId", ulid.New(),
		"seq", state.Num("lastSeq")+1,
		"origin", agPresent(n, "origin"),
		"treeHash", agPresent(n, "treeHash"),
		"createdAt", doc.NowISO(),
		"createdBy", agPresent(n, "createdBy"),
		"deploymentId", deploymentID,
		"siteSlug", siteSlug,
		"volumeSlug", volumeSlug,
		"trigger", agOr(n, "trigger", nil),
		"fileCount", agPresent(st, "fileCount"),
		"dirCount", agPresent(st, "dirCount"),
		"totalBytes", agPresent(st, "totalBytes"),
		"baseVersionId", agPresent(n, "baseVersionId"),
		"restoredFromVersionId", agOr(n, "restoredFromVersionId", nil),
		"note", agPresent(n, "note"),
		"excludedCount", agOr(n, "excludedCount", 0.0),
		"skippedCount", agOr(n, "skippedCount", 0.0),
		"skipped", agOr(n, "skipped", []any{}),
		"agentSnapshotId", agOr(n, "agentSnapshotId", nil),
		"requestId", agOr(n, "requestId", nil),
		"pushId", agOr(n, "pushId", nil),
		"changes", changes,
	)
}

func vcNote(v any) (string, error) {
	const at = "note"
	if agNullish(v) {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", cloud.E.Bad(at, "V_TYPE", at+" must be a string")
	}
	if max := shared.VOLUME_CONTENT_LIMITS.NoteMax; doc.JSLen(s) > max {
		return "", cloud.E.Bad(at, "V_LENGTH", at+": at most "+strconv.Itoa(max)+" characters")
	}
	return s, nil
}

func vcUlidOrNull(v any, at string) (any, error) {
	if agNullish(v) {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok || !shared.RE.ULID.MatchString(s) {
		return nil, cloud.E.Bad(at, "V_FORMAT", at+" must be a ULID or null")
	}
	return s, nil
}

// vcCommitOpts: the fields of commitVersion that its one caller uses.
type vcCommitOpts struct {
	deploymentID, siteSlug, volumeSlug, key string
	new                                     *doc.Map
	tree, prevTree                          *doc.Map
	prevLatestID                            any
	check                                   func(tx *driver.Tx) (*doc.Map, error)
	extraState                              func(state *doc.Map) *doc.Map
}

// vcCommitVersion is commitVersion: write a new version in one transaction: state re-read, base check, seq, latest
// (+ lastSnapshot). Returns the version and whether it is new.
func vcCommitVersion(o *vcCommitOpts) (*doc.Map, bool, error) {
	depRef := cloud.Col.Deployments().Doc(o.deploymentID)
	stRef := core.VcCol(o.deploymentID).Doc(o.key)
	var version *doc.Map
	created := false
	err := cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ds, err := tx.Get(depRef)
		if err != nil {
			return err
		}
		ss, err := tx.Get(stRef)
		if err != nil {
			return err
		}
		if !ds.Exists() {
			return cloud.E.NotFound("deployment not found")
		}
		state := vcDecodeState(o.deploymentID, o.siteSlug, o.volumeSlug, ss.Data())
		existing, err := o.check(tx)
		if err != nil {
			return err
		}
		if existing != nil {
			version, created = existing, false
			return nil
		}
		// the previous tree was read outside the tx: a concurrent new latest → retry the whole call
		if agOr(state.Map("latest"), "versionId", nil) != o.prevLatestID {
			return cloud.E.Conflict("a newer version arrived; try again", doc.Obj("latest", agPresent(state, "latest")))
		}
		v := vcBuildVersion(o.deploymentID, o.siteSlug, o.volumeSlug, state, o.new, o.tree, o.prevTree)
		tx.Create(vcVersions(o.deploymentID, o.key).Doc(v.Str("versionId")), v)
		next := doc.Obj("deploymentId", o.deploymentID, "siteSlug", o.siteSlug, "volumeSlug", o.volumeSlug, "latest", vcSummaryOf(v), "versionCount", state.Num("versionCount")+1, "lastSeq", agPresent(v, "seq"), "updatedAt", agPresent(v, "createdAt"))
		if v.Val("origin") == any("snapshot") {
			next.Set("lastSnapshot", vcSummaryOf(v))
		}
		next.Spread(o.extraState(state))
		next.Set("_open", vcOpenOf(state.Shallow().Spread(next)))
		tx.Set(stRef, next, driver.Merge)
		version, created = v, true
		return nil
	})
	return version, created, err
}

// ─────────────────────────────────────────── daily: prune AUTO snapshots beyond the newest versionsKept per volume

// PruneVolumeContent is pruneVolumeContent (jobs/daily.ts).
func PruneVolumeContent() (int, error) {
	keep := shared.VOLUME_CONTENT_LIMITS.VersionsKept
	states, err := cloud.DB().CollectionGroup("volumeContent").Where("versionCount", ">", keep).Get()
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, st := range states.Docs {
		s := st.Data()
		ref := st.Ref()
		vs, err := ref.Collection("versions").OrderBy("seq", "desc").Get()
		if err != nil {
			return pruned, err
		}
		pushes, err := ref.Collection("pushes").Get()
		if err != nil {
			return pruned, err
		}
		pinned := map[string]bool{}
		pin := func(v any) {
			if id, ok := v.(string); ok && id != "" {
				pinned[id] = true
			}
		}
		for _, p := range pushes.Docs {
			x := p.Data()
			pin(x.Val("versionId"))
			pin(x.Val("baseVersionId"))
			pin(x.Map("result").Val("afterVersionId"))
		}
		for _, v := range vs.Docs {
			pin(v.Data().Val("baseVersionId")) // a base of another version (the push base chain)
		}
		pin(s.Map("latest").Val("versionId"))
		pin(s.Map("lastSnapshot").Val("versionId"))
		var doomed []*driver.DocSnap
		if len(vs.Docs) > keep {
			for _, v := range vs.Docs[keep:] {
				x := v.Data()
				if x.Val("origin") == any("snapshot") && x.Val("trigger") == any("auto") && !pinned[x.Str("versionId")] {
					doomed = append(doomed, v)
				}
			}
		}
		if len(doomed) == 0 {
			continue
		}
		for i := 0; i < len(doomed); i += 400 {
			b := cloud.DB().Batch()
			for _, d := range doomed[i:min(i+400, len(doomed))] {
				b.Delete(d.Ref())
			}
			if err := b.Commit(); err != nil {
				return pruned, err
			}
		}
		err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
			cur, err := tx.Get(ref)
			if err != nil {
				return err
			}
			n := cur.Data().Num("versionCount") - float64(len(doomed))
			if n < 0 {
				n = 0
			}
			tx.Update(ref, doc.Obj("versionCount", n))
			return nil
		})
		if err != nil {
			return pruned, err
		}
		pruned += len(doomed)
	}
	return pruned, nil
}

// ─────────────────────────────────────────── handlers

func listVolumeContent(c *cloud.Ctx) (any, error) {
	dep, err := brReadDep(c)
	if err != nil {
		return nil, err
	}
	deploymentID := dep.Str("deploymentId")
	states, err := core.VcCol(deploymentID).Get()
	if err != nil {
		return nil, err
	}
	heads, err := HeadSiteConfigs(dep)
	if err != nil {
		return nil, err
	}
	stored := map[string]*doc.Map{}
	for _, d := range states.Docs {
		stored[d.ID()] = d.Data()
	}
	keys := map[string]bool{}
	addVolumes := func(site string, sc *doc.Map) {
		for _, x := range sc.Arr("volumes") {
			slug := x.(*doc.Map).Str("volumeSlug")
			if doc.Truthy(vcPersistOf(sc, slug)) {
				keys[core.VcKey(site, slug)] = true
			}
		}
	}
	for _, x := range agList(dep.Map("draft"), "sites") {
		s := x.(*doc.Map)
		addVolumes(s.Str("siteSlug"), s.Map("config"))
	}
	for site, sc := range heads {
		addVolumes(site, sc)
	}
	for k, d := range stored {
		if d.Num("versionCount") > 0 {
			keys[k] = true
		}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	items := make([]any, len(sorted))
	for i, k := range sorted {
		site, vol, _ := strings.Cut(k, ":")
		items[i] = vcStateView(vcDecodeState(deploymentID, site, vol, stored[k]), vcPersistOf(vcDraftSite(dep, site), vol), doc.Truthy(vcPersistOf(heads[site], vol)))
	}
	return doc.Obj("items", items), nil
}

func getVolumeContent(c *cloud.Ctx) (any, error) {
	t, err := vcLoadTarget(c, "read")
	if err != nil {
		return nil, err
	}
	return vcStateView(t.state, t.draftPersist, doc.Truthy(t.headPersist)), nil
}

func listVolumeContentVersions(c *cloud.Ctx) (any, error) {
	t, err := vcLoadTarget(c, "read")
	if err != nil {
		return nil, err
	}
	limit, err := core.ClampLimit(c, 50, 200)
	if err != nil {
		return nil, err
	}
	q := vcVersions(t.dep.Str("deploymentId"), t.key).OrderBy("seq", "desc")
	if cur := c.Query.Get("cursor"); cur != "" {
		n, ok := cloud.JSNumber(cur)
		if !ok || !agIsInt(n) || n < 1 {
			return nil, cloud.E.Bad("cursor", "V_FORMAT", "bad cursor")
		}
		q = q.StartAfter(n)
	}
	s, err := q.Limit(limit + 1).Get()
	if err != nil {
		return nil, err
	}
	docs := s.Docs
	if len(docs) > limit {
		docs = docs[:limit]
	}
	items := make([]any, len(docs))
	var cursor any
	for i, d := range docs {
		v := vcDecodeVersion(d.Data())
		items[i] = v
		if len(s.Docs) > limit && i == len(docs)-1 {
			cursor = core.JSString(agPresent(v, "seq"))
		}
	}
	return doc.Obj("items", items, "cursor", cursor), nil
}

func getVolumeContentVersion(c *cloud.Ctx) (any, error) {
	t, err := vcLoadTarget(c, "read")
	if err != nil {
		return nil, err
	}
	v, err := vcLoadVersion(t.dep.Str("deploymentId"), t.key, c.Params["versionId"])
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, cloud.E.NotFound("version not found")
	}
	tree, err := core.ReadTree(v.Str("treeHash"))
	if err != nil {
		return nil, err
	}
	if tree == nil {
		return nil, cloud.E.NotFound("the tree of this version is not stored")
	}
	return doc.Obj("version", v, "tree", tree), nil
}

func vcGone(c *cloud.Ctx) (any, error) {
	return nil, cloud.E.Gone("persist-to-cloud content is committed now (§21 #117): edit it under Volume › Content and Commit; sites write it when they merge", doc.Obj("hint", "POST /v1/deployments/{id}/commits"))
}

func agentCheckBlobs(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	list, ok := b.Val("sha256s").([]any)
	if !ok {
		return nil, cloud.E.Bad("sha256s", "V_TYPE", "sha256s must be a list")
	}
	if len(list) > 1000 {
		return nil, cloud.E.Bad("sha256s", "V_TOO_MANY", "at most 1000 per call")
	}
	shas := make([]string, len(list))
	for i, x := range list {
		s, ok := x.(string)
		if !ok || !shared.RE.Sha256.MatchString(s) {
			return nil, cloud.E.Bad("sha256s["+strconv.Itoa(i)+"]", "V_FORMAT", `must be "sha256:" + 64 hex`)
		}
		shas[i] = s
	}
	missing, err := MissingBlobs(shas)
	if err != nil {
		return nil, err
	}
	return doc.Obj("missing", missing), nil
}

func agentPutBlob(c *cloud.Ctx) (any, error) {
	// §21 #117 any enrolled site uploads the bytes of its commits (content-addressed, size-capped; c.agent = signed)
	hex := cloud.HexOf(c.Params["sha256"])
	if !cloud.IsHex64(hex) {
		return nil, cloud.E.Bad("sha256", "V_FORMAT", `sha256 must be "sha256:" + 64 hex`)
	}
	if max := shared.VOLUME_CONTENT_LIMITS.SnapshotFileMaxBytes; len(c.Body) > max {
		return nil, cloud.E.TooLarge("a snapshot file is at most " + strconv.Itoa(max) + " bytes")
	}
	got := cloud.Sha256Hex(c.Body)
	if got != hex {
		return nil, cloud.E.Bad("sha256", "V_FORMAT", "sha256 mismatch: the body is sha256:"+got)
	}
	p := core.Paths.File(hex)
	here, err := core.Exists(p)
	if err != nil {
		return nil, err
	}
	if !here {
		if err := core.WriteBytes(p, c.Body, ""); err != nil {
			return nil, err
		}
	}
	return doc.Obj("sha256", "sha256:"+hex, "sizeBytes", float64(len(c.Body))), nil
}

var (
	vcSnapshotKeys = []string{"snapshotId", "trigger", "takenAt", "actor", "tree", "treeHash", "excludedCount", "skippedCount", "skipped", "requestId", "pushId", "note"}
	vcTriggers     = []string{"manual-cloud", "manual-agent", "auto", "before-apply", "after-apply"}
	vcSkipReasons  = []string{"symlink", "special", "bad-name", "too-large"}
	vcPushKeys     = []string{"status", "resolvedBy", "resolvedAt", "result"}
	vcResolutions  = []string{"overwrite", "keep-live", "per-file", "fast-forward", "up-to-date"}
)

func agentCreateVolumeSnapshot(c *cloud.Ctx) (any, error) {
	a := c.Agent
	deploymentID, siteSlug := a.DeploymentID, a.SiteSlug
	volumeSlug := c.Params["volumeSlug"]
	if !shared.RE.VolumeSlug.MatchString(volumeSlug) {
		return nil, cloud.E.Bad("volumeSlug", "V_FORMAT", "bad volume slug")
	}
	sc, err := vcSignerHeadConfig(deploymentID, siteSlug, a.Agent.Str("headRevId"))
	if err != nil {
		return nil, err
	}
	if !doc.Truthy(vcPersistOf(sc, volumeSlug)) {
		return nil, cloud.E.Forbidden("volume " + volumeSlug + " is not persisted to the cloud in the head of site " + siteSlug)
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, vcSnapshotKeys); err != nil {
		return nil, err
	}
	snapshotID, ok := b.StrOK("snapshotId")
	if !ok || !shared.RE.ULID.MatchString(snapshotID) {
		return nil, cloud.E.Bad("snapshotId", "V_FORMAT", "snapshotId must be a ULID")
	}
	if !agIn(vcTriggers, b.Val("trigger")) {
		return nil, cloud.E.Bad("trigger", "V_ENUM", "trigger must be "+strings.Join(vcTriggers, " | "))
	}
	if !vcIso(b.Val("takenAt")) {
		return nil, cloud.E.Bad("takenAt", "V_FORMAT", "takenAt must be an ISO time")
	}
	if actor := agPresent(b, "actor"); !agNullish(actor) {
		if s, ok := actor.(string); !ok || doc.JSLen(s) > 320 {
			return nil, cloud.E.Bad("actor", "V_TYPE", "actor must be a string or null")
		}
	}
	treeHash, ok := b.StrOK("treeHash")
	if !ok || !shared.RE.Sha256.MatchString(treeHash) {
		return nil, cloud.E.Bad("treeHash", "V_FORMAT", `treeHash must be "sha256:" + 64 hex`)
	}
	if !vcInt(agOr(b, "excludedCount", 0.0)) || !vcInt(agOr(b, "skippedCount", 0.0)) {
		return nil, cloud.E.Bad("excludedCount", "V_TYPE", "excludedCount / skippedCount must be integers ≥ 0")
	}
	skipped, isList := agOr(b, "skipped", []any{}).([]any)
	good := isList && len(skipped) <= 100
	if good {
		for _, s := range skipped {
			m, _ := s.(*doc.Map)
			p, isStr := m.StrOK("path")
			if !agIsObj(s) || !isStr || doc.JSLen(p) > 4096 || !agIn(vcSkipReasons, m.Val("reason")) {
				good = false
				break
			}
		}
	}
	if !good {
		return nil, cloud.E.Bad("skipped", "V_TYPE", "skipped must be ≤ 100 of { path, reason "+strings.Join(vcSkipReasons, " | ")+" }")
	}
	requestID, err := vcUlidOrNull(agPresent(b, "requestId"), "requestId")
	if err != nil {
		return nil, err
	}
	pushID, err := vcUlidOrNull(agPresent(b, "pushId"), "pushId")
	if err != nil {
		return nil, err
	}
	note, err := vcNote(agPresent(b, "note"))
	if err != nil {
		return nil, err
	}
	ti, err := vcTreeBodyIssues(agPresent(b, "tree"))
	if err != nil {
		return nil, err
	}
	if len(ti) > 0 {
		return nil, agInvalid(ti)
	}
	tree, err := shared.NormalizeVolumeTree(agPresent(b, "tree"))
	if err != nil {
		return nil, err
	}
	if big := vcTotalTooLarge(tree); big != nil {
		return nil, agInvalid([]any{big})
	}
	input, err := shared.VolumeTreeHashInput(tree)
	if err != nil {
		return nil, err
	}
	if "sha256:"+cloud.Sha256Hex([]byte(input)) != treeHash {
		return nil, cloud.E.Bad("treeHash", "V_FORMAT", "treeHash does not match sha256(JCS(canonical tree))")
	}
	key := core.VcKey(siteSlug, volumeSlug)
	versions := vcVersions(deploymentID, key)
	// idempotent: this snapshotId stored before → the stored version (a store-and-forward retry)
	prior, err := versions.Where("agentSnapshotId", "==", snapshotID).Limit(1).Get()
	if err != nil {
		return nil, err
	}
	if !prior.Empty() {
		return agJSONReply(200, doc.Obj("version", vcDecodeVersion(prior.Docs[0].Data()), "existing", true)), nil
	}
	missing, err := MissingBlobs(vcTreeShas(tree))
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, vcBlobMissing(missing)
	}
	if _, err := core.StoreTree(tree); err != nil {
		return nil, err
	}
	createdBy := "agent:" + siteSlug
	if s, ok := b.StrOK("actor"); ok && s != "" {
		createdBy = s
	}
	cleanSkipped := make([]any, len(skipped))
	for i, s := range skipped {
		m := s.(*doc.Map)
		cleanSkipped[i] = doc.Obj("path", agPresent(m, "path"), "reason", agPresent(m, "reason"))
	}
	store := func() (any, error) {
		st, err := core.VcCol(deploymentID).Doc(key).Get()
		if err != nil {
			return nil, err
		}
		st0 := vcDecodeState(deploymentID, siteSlug, volumeSlug, st.Data())
		var prevTree *doc.Map
		if l := st0.Map("latest"); l != nil {
			if prevTree, err = core.ReadTree(l.Str("treeHash")); err != nil {
				return nil, err
			}
		}
		v, created, err := vcCommitVersion(&vcCommitOpts{
			deploymentID: deploymentID, siteSlug: siteSlug, volumeSlug: volumeSlug, key: key,
			tree: tree, prevTree: prevTree,
			prevLatestID: agOr(st0.Map("latest"), "versionId", nil),
			check: func(tx *driver.Tx) (*doc.Map, error) {
				again, err := tx.Query(versions.Where("agentSnapshotId", "==", snapshotID).Limit(1))
				if err != nil || again.Empty() {
					return nil, err
				}
				return vcDecodeVersion(again.Docs[0].Data()), nil
			},
			new: doc.Obj("origin", "snapshot", "treeHash", treeHash, "baseVersionId", nil, "note", note, "createdBy", createdBy, "trigger", b.Val("trigger"), "excludedCount", agOr(b, "excludedCount", 0.0), "skippedCount", agOr(b, "skippedCount", 0.0), "skipped", cleanSkipped, "agentSnapshotId", snapshotID, "requestId", requestID, "pushId", pushID),
			// the answered cloud request is cleared (an older / other request stays open)
			extraState: func(s *doc.Map) *doc.Map {
				if requestID != nil && s.Map("snapshotRequest").Val("requestId") == requestID {
					return doc.Obj("snapshotRequest", nil)
				}
				return doc.New()
			},
			// no _cloud entry: the agent logs volume-content.snapshot-uploaded in its own chain
		})
		if err != nil {
			return nil, err
		}
		if !created {
			return agJSONReply(200, doc.Obj("version", v, "existing", true)), nil
		}
		return doc.Obj("version", v, "existing", false), nil
	}
	// a concurrent new latest (another upload / an edit) → re-read the previous tree and retry
	for attempt := 1; ; attempt++ {
		out, err := store()
		if err == nil {
			return out, nil
		}
		var e *cloud.ApiError
		if attempt >= 3 || !errors.As(err, &e) || e.Code != "conflict" {
			return nil, err
		}
	}
}

func agentReportVolumePush(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	volumeSlug, pushID := c.Params["volumeSlug"], c.Params["pushId"]
	if !shared.RE.VolumeSlug.MatchString(volumeSlug) || !shared.RE.ULID.MatchString(pushID) {
		return nil, cloud.E.NotFound("push not found")
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, vcPushKeys); err != nil {
		return nil, err
	}
	if !agIn([]string{"applied", "kept-live", "failed"}, b.Val("status")) {
		return nil, cloud.E.Bad("status", "V_ENUM", "status must be applied | kept-live | failed")
	}
	resolvedBy, ok := b.StrOK("resolvedBy")
	if !ok || resolvedBy == "" || doc.JSLen(resolvedBy) > 320 {
		return nil, cloud.E.Bad("resolvedBy", "V_REQUIRED", "resolvedBy is required")
	}
	if !vcIso(b.Val("resolvedAt")) {
		return nil, cloud.E.Bad("resolvedAt", "V_FORMAT", "resolvedAt must be an ISO time")
	}
	r := b.Map("result")
	if r == nil {
		return nil, cloud.E.Bad("result", "V_TYPE", "result is required")
	}
	if !agIn(vcResolutions, r.Val("resolution")) {
		return nil, cloud.E.Bad("result.resolution", "V_ENUM", "resolution must be "+strings.Join(vcResolutions, " | "))
	}
	for _, k := range []string{"written", "deleted", "keptLive"} {
		if !vcInt(r.Val(k)) {
			return nil, cloud.E.Bad("result."+k, "V_TYPE", k+" must be an integer ≥ 0")
		}
	}
	if rs := agPresent(r, "restarted"); rs != any(doc.Undefined) {
		l, ok := rs.([]any)
		good := ok && len(l) <= 200
		for _, x := range l {
			if _, isStr := x.(string); !isStr {
				good = false
			}
		}
		if !good {
			return nil, cloud.E.Bad("result.restarted", "V_TYPE", "restarted must be a list of container slugs")
		}
	}
	if e := agPresent(r, "error"); !agNullish(e) {
		if _, ok := e.(string); !ok {
			return nil, cloud.E.Bad("result.error", "V_TYPE", "error must be a string or null")
		}
	}
	snapshotID, err := vcUlidOrNull(agPresent(r, "snapshotId"), "result.snapshotId")
	if err != nil {
		return nil, err
	}
	var errText any
	if s, ok := r.StrOK("error"); ok {
		errText = doc.JSSlice(s, 2000)
	}
	afterVersionID, err := vcUlidOrNull(agPresent(r, "afterVersionId"), "result.afterVersionId")
	if err != nil {
		return nil, err
	}
	result := doc.Obj("resolution", r.Val("resolution"), "written", r.Val("written"), "deleted", r.Val("deleted"), "keptLive", r.Val("keptLive"), "snapshotId", snapshotID, "restarted", agOr(r, "restarted", []any{}), "error", errText, "afterVersionId", afterVersionID)
	key := core.VcKey(siteSlug, volumeSlug)
	pRef := vcPushes(deploymentID, key).Doc(pushID)
	stRef := core.VcCol(deploymentID).Doc(key)
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ps, err := tx.Get(pRef)
		if err != nil {
			return nil, err
		}
		ss, err := tx.Get(stRef)
		if err != nil {
			return nil, err
		}
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		if !ps.Exists() {
			return nil, cloud.E.NotFound("push not found")
		}
		cur := ps.Data()
		if cur.Val("siteSlug") != any(siteSlug) || cur.Val("volumeSlug") != any(volumeSlug) {
			return nil, cloud.E.NotFound("push not found")
		}
		// idempotent; a superseded / cancelled (or already resolved) push keeps its status
		if cur.Val("status") != any("pending") {
			return cur, nil
		}
		push := cur.Shallow().Set("status", b.Val("status")).Set("resolvedAt", b.Val("resolvedAt")).Set("resolvedBy", resolvedBy).Set("result", result)
		tx.Set(pRef, push)
		st := vcDecodeState(deploymentID, siteSlug, volumeSlug, ss.Data())
		if st.Map("push").Val("pushId") == any(pushID) {
			next := st.Shallow().Set("push", push)
			tx.Set(stRef, doc.Obj("push", push, "updatedAt", doc.NowISO(), "_open", vcOpenOf(next)), driver.Merge)
		}
		dep := cloud.DecodeDeployment(ds.Data())
		tx.Update(depRef, doc.Obj("agentEpoch", dep.Num("agentEpoch")+1))
		return push, nil
	})
}
