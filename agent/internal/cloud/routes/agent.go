package routes

// Port of backend/src/routes/agent.ts: the signed agent endpoints (poll, heartbeat, heads, proposals, images, blobs,
// activities, transfers, uploads, relay). Helpers of this file are prefixed ag: all route files share the package.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// RemoteGuiCloudItems is remoteGuiCloudItems of routes/remote-gui.ts (set by its port): the sites with an open remote GUI
// session. The result must not be nil.
var RemoteGuiCloudItems = func(deploymentID string) []any { return []any{} }

// PortTunnelCloudItems is portTunnelCloudItems of routes/port-tunnels.ts (set by its port). nil = undefined: the cloud
// carries no tunnels and CloudView has no tunnels key; an empty non-nil list is sent as [].
var PortTunnelCloudItems = func(deploymentID string) ([]any, error) { return nil, nil }

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"poll":                  poll,
		"heartbeat":             heartbeat,
		"reportHead":            reportHead,
		"getAgentProposal":      getAgentProposal,
		"registerLocalProposal": registerLocalProposal,
		"reportProposalStatus":  reportProposalStatus,
		"getAgentRevision":      getAgentRevision,
		"getImageManifest":      getImageManifest,
		"getAgentBlob":          getAgentBlob,
		"uploadAgentActivities": uploadAgentActivities,
		"listAgentTransfers":    listAgentTransfers,
		"createAgentTransfer":   createAgentTransfer,
		"reportAgentTransfer":   reportAgentTransfer,
		"downloadAgentTransfer": downloadAgentTransfer,
		"getAgentUpload":        getAgentUpload,
		"putAgentUploadPart":    putAgentUploadPart,
		"completeAgentUpload":   completeAgentUpload,
		"relay":                 relay,
	})
}

// ─────────────────────────────────────────── JS semantics shared by the four route files of this port

// agPresent is m[k] with undefined for a missing key.
func agPresent(m *doc.Map, k string) any {
	if v, ok := m.Get(k); ok {
		return v
	}
	return doc.Undefined
}

// agNullish is `v === null || v === undefined`.
func agNullish(v any) bool { return v == nil || v == any(doc.Undefined) }

// agOr is m[k] ?? def.
func agOr(m *doc.Map, k string, def any) any {
	if v, ok := m.Get(k); ok && !agNullish(v) {
		return v
	}
	return def
}

// agIsInt is Number.isInteger.
func agIsInt(v any) bool {
	f, ok := v.(float64)
	return ok && !math.IsInf(f, 0) && f == math.Trunc(f)
}

// agObjectLike is `!!v && typeof v === 'object'` (an array passes, as in JS).
func agObjectLike(v any) bool {
	switch x := v.(type) {
	case *doc.Map:
		return x != nil
	case []any:
		return true
	}
	return false
}

// agIsObj is isObj of volume-content.ts: an object that is not an array.
func agIsObj(v any) bool {
	m, ok := v.(*doc.Map)
	return ok && m != nil
}

func agIn(l []string, v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// agMapOrNil: a nil *doc.Map as the JSON null.
func agMapOrNil(m *doc.Map) any {
	if m == nil {
		return nil
	}
	return m
}

// agList is `for (const x of m[k])`: a missing or non-list value is a TypeError (500), never an empty loop.
func agList(m *doc.Map, k string) []any {
	v := m.Val(k)
	l, ok := v.([]any)
	if !ok {
		panic(fmt.Sprintf("%s is not iterable", k))
	}
	return l
}

// agInvalid is E.invalid(list, message?) for issue maps: the key order of each issue reaches the client as built.
func agInvalid(list []any, msg ...string) *cloud.ApiError {
	m := "invalid request"
	if len(list) > 0 {
		m = list[0].(*doc.Map).Str("message")
	}
	if len(msg) > 0 {
		m = msg[0]
	}
	return &cloud.ApiError{Code: "invalid", Message: m, Details: doc.Obj("errors", list)}
}

func agIssue(path, code, msg string) *doc.Map {
	return doc.Obj("path", path, "code", code, "message", msg, "severity", "error")
}

func agJSONReply(status int, body any) *cloud.Reply {
	return &cloud.Reply{Status: status, Headers: map[string]string{"Content-Type": "application/json; charset=utf-8"}, Body: doc.MarshalString(body)}
}

// ─────────────────────────────────────────── heartbeat checks (§21 #86, #92)

var agGuiKinds = []string{"edge", "gui-alias", "agent-port"}

func agIsPort(v any) bool {
	return agIsInt(v) && v.(float64) >= 1 && v.(float64) <= 65535
}

// EdgeExtrasError is edgeExtrasError: "" = ok.
func EdgeExtrasError(v any) string {
	if agNullish(v) {
		return ""
	}
	e, ok := v.(*doc.Map)
	if !ok || e == nil {
		return "edge must be an object or null"
	}
	if d := agPresent(e, "defaultEdge"); d != any(doc.Undefined) {
		if _, ok := d.(bool); !ok {
			return "edge.defaultEdge must be a boolean"
		}
	}
	if be := agPresent(e, "bindErrors"); be != any(doc.Undefined) {
		l, ok := be.([]any)
		if !ok {
			return "edge.bindErrors must be a list"
		}
		for _, b := range l {
			o, _ := b.(*doc.Map)
			if owner := agPresent(o, "owner"); !agNullish(owner) {
				if s, ok := owner.(string); !ok || doc.JSLen(s) > 255 {
					return "edge.bindErrors[].owner must be null or a string ≤ 255"
				}
			}
		}
	}
	return ""
}

// GuiPortsError is guiPortsError: "" = ok.
func GuiPortsError(v any) string {
	if agNullish(v) {
		return ""
	}
	l, ok := v.([]any)
	if !ok {
		return "guiPorts must be a list"
	}
	if len(l) > 16 {
		return "guiPorts has at most 16 entries"
	}
	for _, g := range l {
		if !agObjectLike(g) {
			return "guiPorts entries must be objects"
		}
		o, _ := g.(*doc.Map)
		if !agIsPort(o.Val("port")) {
			return "guiPorts[].port must be 1–65535"
		}
		if s := o.Val("scheme"); s != any("http") && s != any("https") {
			return "guiPorts[].scheme must be http | https"
		}
		if !agIn(agGuiKinds, o.Val("kind")) {
			return "guiPorts[].kind must be " + strings.Join(agGuiKinds, " | ")
		}
	}
	return ""
}

var agTimeRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$`)

// agParseAgentTime is Date.parse of a string of the HeartbeatRequest.agentTime form, as V8 does it: any day 1–31 of any
// month (a day past the month's end rolls over), 24:00:00 is the next midnight, fractions cut to milliseconds.
func agParseAgentTime(s string) (int64, bool) {
	m := agTimeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n := func(i int) int {
		x, _ := strconv.Atoi(m[i])
		return x
	}
	year, month, day, hour, minute, sec := n(1), n(2), n(3), n(4), n(5), n(6)
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 24 || minute > 59 || sec > 59 {
		return 0, false
	}
	frac := 0
	if m[7] != "" {
		f := (m[7] + "00")[:3]
		frac, _ = strconv.Atoi(f)
	}
	if hour == 24 && (minute != 0 || sec != 0 || frac != 0) {
		return 0, false
	}
	return time.Date(year, time.Month(month), day, hour, minute, sec, frac*1_000_000, time.UTC).UnixMilli(), true
}

// AgentTimeError is agentTimeError: "" = ok.
func AgentTimeError(v any) string {
	if agNullish(v) {
		return ""
	}
	const msg = "agentTime must be an ISO 8601 UTC timestamp (…Z)"
	s, ok := v.(string)
	if !ok || doc.JSLen(s) > 40 {
		return msg
	}
	if _, ok := agParseAgentTime(s); !ok {
		return msg
	}
	return ""
}

// ClockSkewOf is clockSkewOf: agent clock − server receive time, whole seconds (positive = agent ahead).
func ClockSkewOf(agentTime string, receivedAt time.Time) *doc.Map {
	ms, _ := agParseAgentTime(agentTime)
	return doc.Obj("skewSec", math.Floor(float64(ms-receivedAt.UnixMilli())/1000+0.5), "measuredAt", doc.ISO(receivedAt))
}

const agRelayMax = 4 * 1024 * 1024

// ─────────────────────────────────────────── cloud view

// BuildCloudView is buildCloudView.
func BuildCloudView(deploymentID string, dep *doc.Map) (*doc.Map, error) {
	open, err := cloud.Col.Proposals(deploymentID).Where("status", "in", core.OPEN_STATUSES).Get()
	if err != nil {
		return nil, err
	}
	recent, err := cloud.Col.Proposals(deploymentID).OrderBy("revId", "desc").Limit(25).Get()
	if err != nil {
		return nil, err
	}
	transfers, err := cloud.Col.Transfers(deploymentID).Where("direction", "==", "to-site").Where("status", "==", "ready").Get()
	if err != nil {
		return nil, err
	}
	deletedTransfers, err := cloud.Col.Transfers(deploymentID).Where("direction", "==", "to-site").Where("status", "==", "expired").Get()
	if err != nil {
		return nil, err
	}
	agents, err := cloud.Col.Agents(deploymentID).Get()
	if err != nil {
		return nil, err
	}
	release := core.AgentReleaseInfo(doc.Undefined)
	volumeContent, err := core.VolumeContentCloudItems(deploymentID)
	if err != nil {
		return nil, err
	}
	branch, err := core.CloudBranchView(deploymentID, dep)
	if err != nil {
		return nil, err
	}
	files, err := core.FilesCloudItems(deploymentID)
	if err != nil {
		return nil, err
	}
	tunnels, err := PortTunnelCloudItems(deploymentID)
	if err != nil {
		return nil, err
	}
	var props []*doc.Map
	for _, d := range open.Docs {
		props = append(props, d.Data())
	}
	terminal := 0
	for _, d := range recent.Docs {
		p := d.Data()
		if core.IsTerminal(p.Str("status")) && terminal < 5 {
			terminal++
			props = append(props, p)
		}
	}
	sort.SliceStable(props, func(i, j int) bool { return props[i].Str("revId") > props[j].Str("revId") })
	proposals := make([]any, len(props))
	for i, p := range props {
		proposals[i] = core.ProposalMeta(p)
	}
	now := time.Now().UnixMilli()
	// transfers never expire: expiresAt is always sent as null, so agents that still honour it never expire one
	transfersToSite := make([]any, len(transfers.Docs))
	for i, d := range transfers.Docs {
		t := d.Data()
		transfersToSite[i] = doc.Obj("transferId", agPresent(t, "transferId"), "siteSlug", agPresent(t, "siteSlug"), "direction", agPresent(t, "direction"), "fileName", agPresent(t, "fileName"), "sizeBytes", agPresent(t, "sizeBytes"), "sha256", agPresent(t, "sha256"), "status", agPresent(t, "status"), "createdAt", agPresent(t, "createdAt"), "createdBy", agPresent(t, "createdBy"), "note", agPresent(t, "note"), "expiresAt", nil)
	}
	deleted := make([]any, len(deletedTransfers.Docs))
	for i, d := range deletedTransfers.Docs {
		t := d.Data()
		deleted[i] = doc.Obj("transferId", agPresent(t, "transferId"), "siteSlug", agPresent(t, "siteSlug"))
	}
	agentDocs := make([]*doc.Map, len(agents.Docs))
	for i, d := range agents.Docs {
		agentDocs[i] = d.Data()
	}
	peerDocs := append([]*doc.Map{}, agentDocs...)
	sort.SliceStable(peerDocs, func(i, j int) bool { return peerDocs[i].Str("siteSlug") < peerDocs[j].Str("siteSlug") })
	peers := make([]any, len(peerDocs))
	for i, a := range peerDocs {
		peers[i] = core.PeerInfo(a)
	}
	var draftHead any
	if h := dep.Val("head"); doc.Truthy(h) {
		hm := dep.Map("head")
		draftHead = doc.Obj("revId", agPresent(hm, "revId"), "seq", agPresent(hm, "seq"))
	}
	var consoleURL any
	if u := cloud.Env.ConsoleURL(); u != "" {
		consoleURL = u
	}
	view := doc.Obj(
		"fetchedAt", doc.NowISO(),
		"source", "cloud",
		"cursor", core.JSString(agPresent(dep, "agentEpoch")),
		"deploymentId", deploymentID,
		"deleted", dep.Truthy("deleted"),
		"proposals", proposals,
		"transfersToSite", transfersToSite,
		"deletedTransfersToSite", deleted,
		"draftHead", draftHead,
		"accessKey", doc.Obj("hash", agPresent(dep, "accessKeyHash"), "version", agPresent(dep, "accessKeyVersion")),
		"peers", peers,
		"consoleUrl", consoleURL,
		"deploymentLabel", agOr(dep, "label", nil),
		// §21 #90
		"agentRelease", agMapOrNil(release),
		"agentUpgrades", core.LiveRequests(agentDocs, now),
		// §21 #101 pending pushes + open snapshot requests (all sites; each agent filters its own)
		"volumeContent", volumeContent,
		// §21 #104 the deployment branch head = what the sites should run (agents ≥ 0.14 merge it)
		"branch", branch,
		// §21 #118 sites with an open remote GUI session
		"remoteGui", RemoteGuiCloudItems(deploymentID),
		// §21 #123 shared files: the whole listing (+ tombstones) and the copies the cloud wants from a site
		"files", files.Val("files"),
		"fileFetches", files.Val("fileFetches"),
	)
	// §21 #125 port tunnels (absent: this cloud carries none; the agent does not poll)
	if tunnels != nil {
		view.Set("tunnels", tunnels)
	}
	return view, nil
}

// ─────────────────────────────────────────── what a site may download (head + open proposals)

type agImageRef struct{ serviceSlug, version string }

type agBlobRef struct{ kind, serviceSlug, version string }

type agAllowed struct {
	images map[string]agImageRef
	blobs  map[string]agBlobRef
}

type agAllowedHit struct {
	at time.Time
	v  *agAllowed
}

var (
	agAllowedMu    sync.Mutex
	agAllowedCache = map[string]*agAllowedHit{}
)

func agAllowedFor(deploymentID string, dep *doc.Map) (*agAllowed, error) {
	key := deploymentID + ":" + core.JSString(agPresent(dep, "agentEpoch"))
	agAllowedMu.Lock()
	hit := agAllowedCache[key]
	agAllowedMu.Unlock()
	if hit != nil && time.Since(hit.at) < time.Minute {
		return hit.v, nil
	}
	open, err := cloud.Col.Proposals(deploymentID).Where("status", "in", core.OPEN_STATUSES).Select().Get()
	if err != nil {
		return nil, err
	}
	var revIDs []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			revIDs = append(revIDs, id)
		}
	}
	for _, d := range open.Docs {
		add(d.ID())
	}
	if dep.Truthy("head") {
		add(dep.Map("head").Str("revId"))
	}
	// every site's own head too (sites may lag behind deployment.head)
	agents, err := cloud.Col.Agents(deploymentID).Select("headRevId").Get()
	if err != nil {
		return nil, err
	}
	for _, a := range agents.Docs {
		if a.Data().Truthy("headRevId") {
			add(a.Data().Str("headRevId"))
		}
	}
	var sources []*doc.Map
	if len(revIDs) > 0 {
		refs := make([]*driver.DocRef, len(revIDs))
		for i, r := range revIDs {
			refs[i] = cloud.Col.Revisions(deploymentID).Doc(r)
		}
		revs, err := cloud.DB().GetAll(refs...)
		if err != nil {
			return nil, err
		}
		for _, s := range revs {
			if s.Exists() {
				sources = append(sources, cloud.DecodeRevision(s.Data()))
			}
		}
	}
	// §21 #104: the deployment head commit + its newest commits + every site's agent-branch head
	fromCommits, err := core.AllowedCommits(deploymentID)
	if err != nil {
		return nil, err
	}
	sources = append(sources, fromCommits...)
	v := &agAllowed{images: map[string]agImageRef{}, blobs: map[string]agBlobRef{}}
	for _, r := range sources {
		config := r.Map("config")
		catImages := r.Map("catalog").Map("images")
		for _, x := range agList(config, "sites") {
			site := x.(*doc.Map)
			for _, cx := range agList(site.Map("config"), "containers") {
				ct := cx.(*doc.Map)
				slug, version := ct.Str("serviceSlug"), ct.Str("version")
				k := shared.ImageKey(slug, version)
				v.images[k] = agImageRef{slug, version}
				if spec := catImages.Map(k); spec != nil {
					v.blobs[cloud.HexOf(spec.Str("digest"))] = agBlobRef{"manifest", slug, version}
					v.blobs[cloud.HexOf(spec.Str("configDigest"))] = agBlobRef{"config", slug, version}
					for _, l := range agList(spec, "layers") {
						v.blobs[cloud.HexOf(l.(*doc.Map).Str("digest"))] = agBlobRef{"layer", slug, version}
					}
				}
			}
		}
		for _, x := range agList(config, "files") {
			if f := x.(*doc.Map); f.Truthy("content") {
				v.blobs[cloud.HexOf(f.Map("content").Str("sha256"))] = agBlobRef{kind: "file"}
			}
		}
		// round E: volume seed archives (stored like file contents, files/<hex>); seed 'files' / 'file' name config files
		for _, x := range agList(config, "sites") {
			site := x.(*doc.Map)
			for _, vx := range site.Map("config").Arr("volumes") {
				seed := vx.(*doc.Map).Map("seed")
				kind := seed.Val("kind")
				if s, ok := seed.StrOK("blobSha256"); ok && kind == "archive" {
					v.blobs[cloud.HexOf(s)] = agBlobRef{kind: "file"}
				}
				// §21 #101 the volume's own content (tree / single seeds; blobs files/<hex>)
				if kind == "tree" {
					for _, e := range seed.Arr("entries") {
						em, _ := e.(*doc.Map)
						if s, ok := em.StrOK("sha256"); ok {
							v.blobs[cloud.HexOf(s)] = agBlobRef{kind: "file"}
						}
					}
				}
				if s, ok := seed.StrOK("sha256"); ok && kind == "single" {
					v.blobs[cloud.HexOf(s)] = agBlobRef{kind: "file"}
				}
			}
		}
	}
	agAllowedMu.Lock()
	defer agAllowedMu.Unlock()
	agAllowedCache[key] = &agAllowedHit{time.Now(), v}
	if len(agAllowedCache) > 500 {
		for k, x := range agAllowedCache {
			if time.Since(x.at) > time.Minute {
				delete(agAllowedCache, k)
			}
		}
	}
	return v, nil
}

func agLoadRev(deploymentID, revID string) (*doc.Map, error) {
	s, err := cloud.Col.Revisions(deploymentID).Doc(revID).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("revision not found")
	}
	return cloud.DecodeRevision(s.Data()), nil
}

func agSecretFetched(c *cloud.Ctx, r *doc.Map) error {
	secrets := agList(r.Map("config"), "secrets")
	slugs := []any{}
	for _, x := range secrets {
		if s := x.(*doc.Map); core.IsSecretSet(s) {
			slugs = append(slugs, agPresent(s, "secretSlug"))
		}
	}
	if len(secrets) == 0 {
		return nil
	}
	a := c.Agent
	return core.AppendCloud(a.DeploymentID, core.CloudEntry{Type: "secret.fetched", Summary: "Site " + a.SiteSlug + " fetched revision seq " + core.JSString(agPresent(r, "seq")) + " with " + strconv.Itoa(len(slugs)) + " secret value(s)", Data: doc.Obj("siteSlug", a.SiteSlug, "secretSlugs", slugs, "revId", agPresent(r, "revId"))})
}

// agCheckRevisionShape is checkRevisionShape (the shape, then both hashes).
func agCheckRevisionShape(v any) error {
	const path = "revision"
	bad := func(m string) error { return cloud.E.Bad(path, "V_TYPE", m) }
	if !agObjectLike(v) {
		return bad("revision is required")
	}
	r, _ := v.(*doc.Map)
	if s, ok := r.StrOK("revId"); !ok || !shared.RE.ULID.MatchString(s) {
		return bad("revId must be a ULID")
	}
	if seq := r.Val("seq"); !agIsInt(seq) || seq.(float64) < 1 {
		return bad("seq must be an integer ≥ 1")
	}
	if p := agPresent(r, "parentRevId"); p != nil {
		if s, ok := p.(string); !ok || !shared.RE.ULID.MatchString(s) {
			return bad("parentRevId must be a ULID or null")
		}
	}
	if !agObjectLike(r.Val("config")) || !agObjectLike(r.Val("catalog")) {
		return bad("config and catalog are required")
	}
	if o := r.Val("origin"); o != any("cloud") && o != any("agent") {
		return bad("origin must be cloud | agent")
	}
	if k := r.Val("kind"); k != any("change") && k != any("rollback") {
		return bad("kind must be change | rollback")
	}
	_, ok1 := r.Val("summary").(string)
	_, ok2 := r.Val("actor").(string)
	_, ok3 := r.Val("createdAt").(string)
	if !ok1 || !ok2 || !ok3 {
		return bad("summary, actor and createdAt are required")
	}
	h, err := cloud.JcsHash(r.Val("config"))
	if err != nil {
		return err
	}
	if any(h) != agPresent(r, "hash") {
		return cloud.E.Bad(path+".hash", "V_FORMAT", "hash does not match sha256(JCS(config))")
	}
	if h, err = cloud.JcsHash(r.Val("catalog")); err != nil {
		return err
	}
	if any(h) != agPresent(r, "catalogHash") {
		return cloud.E.Bad(path+".catalogHash", "V_FORMAT", "catalogHash does not match sha256(JCS(catalog))")
	}
	return nil
}

func agCleanRevision(r *doc.Map) *doc.Map {
	return doc.Obj("revId", agPresent(r, "revId"), "seq", agPresent(r, "seq"), "parentRevId", agPresent(r, "parentRevId"), "origin", agPresent(r, "origin"), "actor", agPresent(r, "actor"), "createdAt", agPresent(r, "createdAt"), "config", agPresent(r, "config"), "hash", agPresent(r, "hash"), "catalog", agPresent(r, "catalog"), "catalogHash", agPresent(r, "catalogHash"), "summary", agPresent(r, "summary"), "kind", agPresent(r, "kind"), "rollbackOf", agOr(r, "rollbackOf", nil))
}

var agLiveStatuses = []string{"published", "staging", "ready"}

// DeriveStatus is deriveStatus: the derived proposal status (§6.1 reportProposalStatus, §14.2).
func DeriveStatus(p *doc.Map) any {
	status := agPresent(p, "status")
	if !agIn(agLiveStatuses, status) {
		return status
	}
	sites := p.Map("sites")
	every, some := sites.Len() > 0, false
	sites.Each(func(_ string, v any) {
		st := agPresent(v.(*doc.Map), "staging")
		every = every && st == any("ready")
		some = some || st != any("pending")
	})
	if every {
		return "ready"
	}
	if some {
		return "staging"
	}
	if status == any("published") {
		return "published"
	}
	return "staging"
}

// agReplyBytes is replyBytes: only a stream is capped at RELAY_MAX.
func agReplyBytes(r *cloud.Reply) ([]byte, error) {
	switch b := r.Body.(type) {
	case nil:
		return []byte{}, nil
	case []byte:
		return b, nil
	case string:
		return []byte(b), nil
	case io.Reader:
		if cl, ok := b.(io.Closer); ok {
			defer cl.Close()
		}
		out, err := io.ReadAll(io.LimitReader(b, agRelayMax+1))
		if err != nil {
			return nil, err
		}
		if len(out) > agRelayMax {
			return nil, cloud.E.New("too-large", fmt.Sprintf("relayed responses are at most %d bytes (use smart distribution)", agRelayMax), nil, nil)
		}
		return out, nil
	}
	return nil, fmt.Errorf("relay: reply body %T", r.Body)
}

// agHeaderOrder is the order the TS reply objects build their headers in (serveObject, then the error envelope, then
// dispatch). A Go Reply holds a map, the relay answers with an ordered object.
var agHeaderOrder = []string{"X-OPM-Sha256", "Accept-Ranges", "Content-Disposition", "Content-Type", "Retry-After", "Content-Range", "Content-Length", "Cache-Control", "Pragma", "X-Request-Id", "X-OPM-Server-Time"}

func agOrderedHeaders(h map[string]string) *doc.Map {
	out := doc.New()
	known := map[string]bool{}
	for _, k := range agHeaderOrder {
		known[k] = true
	}
	var other []string
	for k := range h {
		if !known[k] {
			other = append(other, k)
		}
	}
	sort.Strings(other)
	for _, k := range agHeaderOrder[:7] {
		if v, ok := h[k]; ok {
			out.Set(k, v)
		}
	}
	for _, k := range other {
		out.Set(k, h[k])
	}
	for _, k := range agHeaderOrder[7:] {
		if v, ok := h[k]; ok {
			out.Set(k, v)
		}
	}
	return out
}

func agRelayed(a *cloud.AgentAuth) string {
	if a.RelayedBy != nil {
		return *a.RelayedBy
	}
	return ""
}

// ─────────────────────────────────────────── handlers

func poll(c *cloud.Ctx) (any, error) {
	a := c.Agent
	deploymentID := a.DeploymentID
	wait := 25.0
	if ws := c.Query.Get("wait"); ws != "" {
		n, ok := cloud.JSNumber(ws)
		if !ok || math.IsInf(n, 0) || n < 0 {
			return nil, cloud.E.Bad("wait", "V_RANGE", "wait must be 0–50")
		}
		wait = math.Min(50, math.Floor(n))
	}
	cursor, hasCursor := c.Query.Lookup("cursor")
	_ = cloud.Col.Agents(deploymentID).Doc(a.SiteSlug).Update(doc.Obj("lastSeenAt", doc.NowISO()))
	dep := a.Dep
	if hasCursor && cursor == core.JSString(agPresent(dep, "agentEpoch")) && wait > 0 && !dep.Truthy("deleted") {
		deadline := time.Now().Add(time.Duration(wait) * time.Second)
		for time.Now().Before(deadline) && c.Context.Err() == nil {
			time.Sleep(min(time.Second, max(0, time.Until(deadline))))
			s, err := cloud.Col.Deployments().Doc(deploymentID).Get()
			if err != nil {
				return nil, err
			}
			if !s.Exists() {
				return nil, errors.New("deployment " + deploymentID + " is gone")
			}
			d := s.Data()
			if d.Val("agentEpoch") != dep.Val("agentEpoch") || d.Truthy("deleted") {
				dep = cloud.DecodeDeployment(d)
				break
			}
		}
		if !time.Now().Before(deadline) {
			s, err := cloud.Col.Deployments().Doc(deploymentID).Get()
			if err != nil {
				return nil, err
			}
			if !s.Exists() {
				return nil, errors.New("deployment " + deploymentID + " is gone")
			}
			dep = cloud.DecodeDeployment(s.Data())
		}
	}
	return BuildCloudView(deploymentID, dep)
}

func heartbeat(c *cloud.Ctx) (any, error) {
	a := c.Agent
	deploymentID, siteSlug, agent := a.DeploymentID, a.SiteSlug, a.Agent
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if b.Val("siteSlug") != any(siteSlug) {
		return nil, cloud.E.Bad("siteSlug", "V_REF", "siteSlug must equal the signing site")
	}
	advertiseHost, ok := b.StrOK("advertiseHost")
	if !ok || advertiseHost == "" {
		return nil, cloud.E.Bad("advertiseHost", "V_REQUIRED", "advertiseHost is required")
	}
	if !agIsPort(b.Val("agentPort")) {
		return nil, cloud.E.Bad("agentPort", "V_RANGE", "agentPort must be 1–65535")
	}
	if e := core.DetectedAddressesError(agPresent(b, "detectedAddresses")); e != "" {
		return nil, cloud.E.Bad("detectedAddresses", "V_TYPE", e)
	}
	if e := EdgeExtrasError(agPresent(b, "edge")); e != "" {
		return nil, cloud.E.Bad("edge", "V_TYPE", e)
	}
	if e := GuiPortsError(agPresent(b, "guiPorts")); e != "" {
		return nil, cloud.E.Bad("guiPorts", "V_TYPE", e)
	}
	if e := core.UpgradeProgressError(agPresent(b, "upgrade")); e != "" {
		return nil, cloud.E.Bad("upgrade", "V_TYPE", e)
	}
	if e := AgentTimeError(agPresent(b, "agentTime")); e != "" {
		return nil, cloud.E.Bad("agentTime", "V_FORMAT", e)
	}
	if e := core.VolumeSyncError(agPresent(b, "volumeSync")); e != "" {
		return nil, cloud.E.Bad("volumeSync", "V_TYPE", e)
	}
	// §21 #92: lastSeenAt is ALWAYS this server's receive time; the agent's own clock only feeds clockSkew
	now := time.Now()
	nowISO := doc.ISO(now)
	agentVersion := core.JSString(agOr(b, "agentVersion", agPresent(agent, "agentVersion")))
	peersSeen, ok := b.Val("peersSeen").([]any)
	if !ok {
		peersSeen = []any{}
	}
	keyVersion := agPresent(b, "keyVersion")
	if !agIsInt(keyVersion) {
		keyVersion = agPresent(agent, "keyVersion")
	}
	var relayVia any
	if a.RelayedBy != nil {
		relayVia = *a.RelayedBy
	}
	// round C: old agents omit the field → keep what is stored (or [])
	var detected []any
	if da := agPresent(b, "detectedAddresses"); agNullish(da) {
		if detected, ok = agent.Val("detectedAddresses").([]any); !ok {
			detected = []any{}
		}
	} else {
		detected = core.DetectedAddressesOf(da)
	}
	// §21 #86: old agents omit it → [] (the Setup page then falls back to :8080 / :7080)
	guiPorts := []any{}
	for _, g := range b.Arr("guiPorts") {
		m := g.(*doc.Map)
		guiPorts = append(guiPorts, doc.Obj("port", agPresent(m, "port"), "scheme", agPresent(m, "scheme"), "kind", agPresent(m, "kind")))
	}
	upd := doc.Obj(
		"lastSeenAt", nowISO,
		"agentVersion", agentVersion,
		"headRevId", agOr(b, "headRevId", nil),
		"staging", agOr(b, "staging", nil),
		"metrics", agOr(b, "metrics", nil),
		"edge", agOr(b, "edge", nil),
		"peersSeen", peersSeen,
		"keyVersion", keyVersion,
		"ip", core.JSString(agOr(b, "ip", agPresent(agent, "ip"))),
		"hostname", core.JSString(agOr(b, "hostname", agPresent(agent, "hostname"))),
		"advertiseHost", advertiseHost,
		"agentPort", agPresent(b, "agentPort"),
		"relayVia", relayVia,
		"detectedAddresses", detected,
		"guiPorts", guiPorts,
	)
	// §21 #90: the last upgrade progress (omitted / null → the stored one is kept)
	if u := b.Map("upgrade"); u != nil {
		upd.Set("upgrade", core.CleanProgress(u, siteSlug))
	}
	// §21 #92: agent clock − receive time (old agents omit agentTime → the stored value is kept)
	if s, ok := b.StrOK("agentTime"); ok {
		upd.Set("clockSkew", ClockSkewOf(s, now))
	}
	date := cloud.YMD(now)
	hour := now.UTC().Hour()
	upRef := cloud.Col.Uptime(deploymentID).Doc(siteSlug + "_" + date)
	zeros := func() []any {
		l := make([]any, 24)
		for i := range l {
			l[i] = 0.0
		}
		return l
	}
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		s, err := tx.Get(upRef)
		if err != nil {
			return err
		}
		u := s.Data()
		beats, hours := u.Arr("beats"), u.Arr("hours")
		if len(beats) == 24 {
			beats = append([]any{}, beats...)
		} else {
			beats = zeros()
		}
		if len(hours) == 24 {
			hours = append([]any{}, hours...)
		} else {
			hours = zeros()
		}
		n, _ := beats[hour].(float64)
		n++
		beats[hour] = n
		hours[hour] = math.Min(1, n/120)
		tx.Set(upRef, doc.Obj("siteSlug", siteSlug, "date", date, "beats", beats, "hours", hours, "updatedAt", nowISO))
		tx.Update(cloud.Col.Agents(deploymentID).Doc(siteSlug), upd)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if any(advertiseHost) != agPresent(agent, "advertiseHost") || agPresent(b, "agentPort") != agPresent(agent, "agentPort") {
		if err := cloud.Col.Deployments().Doc(deploymentID).Update(doc.Obj("agentEpoch", doc.Increment(1))); err != nil {
			return nil, err
		}
	}
	// §21 #90: a queued cloud request → clear it when finished / reached / expired; advance the upgrade-all run
	if agent.Truthy("upgradeRequest") {
		if err := core.SettleUpgrade(deploymentID, siteSlug); err != nil {
			return nil, err
		}
	}
	// §21 #101 sync state of the persist-to-cloud volumes → volumeContent state.live
	if err := core.StoreVolumeSync(deploymentID, siteSlug, b.Val("volumeSync"), nowISO); err != nil {
		return nil, err
	}
	// §21 #104 the agent branch of this site (uncommitted drift / edits, head)
	if err := core.StoreHeartbeatBranch(deploymentID, siteSlug, agPresent(b, "branch"), agentVersion); err != nil {
		return nil, err
	}
	return doc.Obj("ok", true, "serverTime", nowISO), nil
}

func reportHead(c *cloud.Ctx) (any, error) {
	a := c.Agent
	deploymentID, siteSlug := a.DeploymentID, a.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	rev := agPresent(b, "revision")
	if err := agCheckRevisionShape(rev); err != nil {
		return nil, err
	}
	revision := agCleanRevision(rev.(*doc.Map))
	revID := revision.Str("revId")
	seq := revision.Num("seq")
	parentRevID := revision.Str("parentRevId")
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		rs, err := tx.Get(cloud.Col.Revisions(deploymentID).Doc(revID))
		if err != nil {
			return nil, err
		}
		var ps *driver.DocSnap
		if parentRevID != "" {
			if ps, err = tx.Get(cloud.Col.Revisions(deploymentID).Doc(parentRevID)); err != nil {
				return nil, err
			}
		}
		dep := cloud.DecodeDeployment(ds.Data())
		openQ, err := tx.Query(cloud.Col.Proposals(deploymentID).Where("status", "in", core.OPEN_STATUSES))
		if err != nil {
			return nil, err
		}
		ownRef := cloud.Col.Proposals(deploymentID).Doc(revID)
		own, err := tx.Get(ownRef)
		if err != nil {
			return nil, err
		}
		// §21 #104: a pre-0.14 site's agent-branch mirror follows its reported head
		mirror, err := core.LegacyHeadMirror(tx, deploymentID, siteSlug, revision, agOr(a.Agent, "agentVersion", nil))
		if err != nil {
			return nil, err
		}
		if rs.Exists() && agPresent(rs.Data(), "hash") != agPresent(revision, "hash") {
			return nil, cloud.E.Conflict("a different revision with this revId is stored")
		}
		if agPresent(revision, "parentRevId") == nil {
			if seq != 1 {
				return nil, cloud.E.Bad("revision.seq", "V_RANGE", "a revision without parent must have seq 1")
			}
		} else {
			if ps == nil || !ps.Exists() {
				return nil, cloud.E.Bad("revision.parentRevId", "V_REF", "unknown parent revision "+parentRevID+" (report the parent first)")
			}
			want := ps.Data().Num("seq") + 1
			if seq != want {
				return nil, cloud.E.Bad("revision.seq", "V_RANGE", "seq must be parent.seq + 1 = "+core.JSString(want))
			}
		}
		head := dep.Map("head")
		curSeq := head.Num("seq")
		if dep.Truthy("head") && seq == curSeq && agPresent(head, "revId") != any(revID) {
			return nil, cloud.E.Conflict("fork: head seq "+core.JSString(curSeq)+" is "+core.JSString(agPresent(head, "revId")), doc.Obj("current", head))
		}
		stored := !rs.Exists()
		now := doc.NowISO()
		log := core.NewCloudLog(deploymentID, dep)
		depUpd := doc.New()
		headChanged, epoch := false, false
		if seq > curSeq {
			headChanged, epoch = true, true
			depUpd.Set("head", doc.Obj("revId", revID, "seq", seq, "hash", agPresent(revision, "hash"), "reportedAt", now))
			for _, d := range openQ.Docs {
				p := d.Data()
				if p.Str("revId") == revID {
					continue
				}
				if agIn(agLiveStatuses, p.Val("status")) && agPresent(p, "parentRevId") != any(revID) {
					tx.Update(cloud.Col.Proposals(deploymentID).Doc(p.Str("revId")), doc.Obj("status", "stale", "updatedAt", now))
					log.Add(tx, core.CloudEntry{Type: "proposal.stale", Summary: "Proposal seq " + core.JSString(agPresent(p, "seq")) + " is stale (head is now seq " + core.JSString(seq) + ")", Data: doc.Obj("revId", agPresent(p, "revId"), "parentRevId", agPresent(p, "parentRevId"), "headRevId", revID)})
				}
			}
		}
		if seq >= curSeq && own.Exists() {
			p := own.Data()
			if p.Val("status") != any("applied") {
				tx.Update(ownRef, doc.Obj("status", "applied", "updatedAt", now, "result", agOr(p, "result", doc.Obj("ok", true, "message", "applied", "snapshotId", nil))))
				epoch = true
			}
		}
		if stored {
			tx.Create(cloud.Col.Revisions(deploymentID).Doc(revID), cloud.EncodeRevision(revision))
		}
		if epoch {
			depUpd.Set("agentEpoch", dep.Num("agentEpoch")+1)
		}
		if log.Changed() {
			depUpd.Spread(log.DepUpdate())
		}
		if depUpd.Len() > 0 {
			tx.Update(depRef, depUpd)
		}
		tx.Update(cloud.Col.Agents(deploymentID).Doc(siteSlug), doc.Obj("headRevId", revID, "lastSeenAt", now))
		if mirror != nil {
			mirror()
		}
		return doc.Obj("stored", stored, "headChanged", headChanged), nil
	})
}

func getAgentProposal(c *cloud.Ctx) (any, error) {
	deploymentID, revID := c.Agent.DeploymentID, c.Params["revId"]
	ps, err := cloud.Col.Proposals(deploymentID).Doc(revID).Get()
	if err != nil {
		return nil, err
	}
	if !ps.Exists() {
		return nil, cloud.E.NotFound("proposal not found")
	}
	revision, err := agLoadRev(deploymentID, revID)
	if err != nil {
		return nil, err
	}
	if err := agSecretFetched(c, revision); err != nil {
		return nil, err
	}
	return doc.Obj("proposal", ps.Data(), "revision", revision), nil
}

func registerLocalProposal(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	rev := agPresent(b, "revision")
	propV := agPresent(b, "proposal")
	if err := agCheckRevisionShape(rev); err != nil {
		return nil, err
	}
	revM := rev.(*doc.Map)
	prop, _ := propV.(*doc.Map)
	if !agObjectLike(propV) || agPresent(prop, "revId") != agPresent(revM, "revId") {
		return nil, cloud.E.Bad("proposal.revId", "V_REF", "proposal.revId must equal revision.revId")
	}
	if revM.Val("origin") != any("agent") || prop.Val("origin") != any("agent") {
		return nil, cloud.E.Bad("proposal.origin", "V_ENUM", "local proposals have origin agent")
	}
	revision := agCleanRevision(revM)
	revID := revision.Str("revId")
	seq := revision.Num("seq")
	config := revision.Map("config")
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		revRef := cloud.Col.Revisions(deploymentID).Doc(revID)
		propRef := cloud.Col.Proposals(deploymentID).Doc(revID)
		mirrorRef := core.Bcol.AgentCommits(deploymentID, siteSlug).Doc(revID)
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		rs, err := tx.Get(revRef)
		if err != nil {
			return nil, err
		}
		ps, err := tx.Get(propRef)
		if err != nil {
			return nil, err
		}
		ms, err := tx.Get(mirrorRef)
		if err != nil {
			return nil, err
		}
		dep := cloud.DecodeDeployment(ds.Data())
		if rs.Exists() && agPresent(rs.Data(), "hash") != agPresent(revision, "hash") {
			return nil, cloud.E.Conflict("a different revision with this revId is stored")
		}
		if ps.Exists() {
			return doc.Obj("stored", false), nil
		}
		// §21 #104: a pre-0.14 local proposal is mirrored as a "legacy" commit of the site's agent branch (same id = revId,
		// so the 0.14 migration push of the same revision is recognised as already stored)
		var legacy *doc.Map
		if !ms.Exists() {
			var parentCfg *doc.Map
			if parentID := revision.Str("parentRevId"); parentID != "" {
				parent, err := tx.Get(cloud.Col.Revisions(deploymentID).Doc(parentID))
				if err != nil {
					return nil, err
				}
				if parent.Exists() {
					parentCfg = cloud.DecodeRevision(parent.Data()).Map("config")
				}
			}
			message := agPresent(revision, "summary")
			if !doc.Truthy(message) {
				message = "local proposal seq " + core.JSString(seq)
			}
			legacy = doc.Obj("commitId", revID, "branch", doc.Obj("kind", "agent", "deploymentId", deploymentID, "siteSlug", siteSlug), "seq", seq, "parentId", agPresent(revision, "parentRevId"), "kind", "legacy", "message", message, "author", doc.Obj("kind", "person", "email", agPresent(revision, "actor"), "uid", nil, "name", nil, "siteSlug", siteSlug), "createdAt", agPresent(revision, "createdAt"), "source", nil, "revertOf", nil, "resetTo", nil, "rollbackOf", nil, "config", config, "hash", agPresent(revision, "hash"), "catalog", agPresent(revision, "catalog"), "catalogHash", agPresent(revision, "catalogHash"), "stats", shared.CommitStats(parentCfg, config, doc.Obj("secretValues", true, "scope", siteSlug)), "data", doc.Obj("migratedFrom", doc.Obj("revId", revID, "seq", seq, "localProposal", true)))
		}
		openQ, err := tx.Query(cloud.Col.Proposals(deploymentID).Where("status", "in", agLiveStatuses))
		if err != nil {
			return nil, err
		}
		open := make([]*doc.Map, len(openQ.Docs))
		for i, d := range openQ.Docs {
			open[i] = d.Data()
		}
		now := doc.NowISO()
		sites := doc.New()
		switch ps := prop.Val("sites").(type) {
		case *doc.Map:
			ps.Each(func(k string, x any) {
				v, _ := x.(*doc.Map)
				var progress any = 0.0
				if p, ok := v.Val("progress").(float64); ok {
					progress = p
				}
				sites.Set(k, doc.Obj("staging", agOr(v, "staging", "pending"), "progress", progress, "error", agOr(v, "error", nil), "updatedAt", agOr(v, "updatedAt", nil)))
			})
		case []any:
			for i, x := range ps {
				v, _ := x.(*doc.Map)
				var progress any = 0.0
				if p, ok := v.Val("progress").(float64); ok {
					progress = p
				}
				sites.Set(strconv.Itoa(i), doc.Obj("staging", agOr(v, "staging", "pending"), "progress", progress, "error", agOr(v, "error", nil), "updatedAt", agOr(v, "updatedAt", nil)))
			}
		}
		if sites.Len() == 0 {
			for _, s := range agList(config, "sites") {
				sites.Set(core.JSString(agPresent(s.(*doc.Map), "siteSlug")), doc.Obj("staging", "pending", "progress", 0.0, "error", nil, "updatedAt", nil))
			}
		}
		var newest *doc.Map
		for _, p := range open {
			if p.Str("revId") > revID && (newest == nil || p.Str("revId") > newest.Str("revId")) {
				newest = p
			}
		}
		newer := newest != nil
		var status, supersededBy any
		switch {
		case newer:
			status = "superseded"
			supersededBy = agPresent(newest, "revId")
		case core.IsTerminal(prop.Str("status")) || agIn([]string{"approved", "applying"}, prop.Val("status")):
			status = agPresent(prop, "status")
		default:
			status = "published"
		}
		proposal := doc.Obj(
			"revId", revID,
			"parentRevId", agPresent(revision, "parentRevId"),
			"seq", seq,
			"summary", agPresent(revision, "summary"),
			"origin", "agent",
			"kind", agPresent(revision, "kind"),
			"createdAt", agOr(prop, "createdAt", now),
			"createdBy", agOr(prop, "createdBy", agPresent(revision, "actor")),
			"status", status,
			"sites", sites,
			"approvedBy", agOr(prop, "approvedBy", nil),
			"approvedAt", agOr(prop, "approvedAt", nil),
			"updateId", agOr(prop, "updateId", nil),
			"result", agOr(prop, "result", nil),
			"supersededBy", supersededBy,
			"updatedAt", now,
		)
		proposal.Set("status", DeriveStatus(proposal))
		log := core.NewCloudLog(deploymentID, dep)
		if !newer {
			for _, p := range open {
				if p.Str("revId") < revID {
					tx.Update(cloud.Col.Proposals(deploymentID).Doc(p.Str("revId")), doc.Obj("status", "superseded", "supersededBy", revID, "updatedAt", now))
					log.Add(tx, core.CloudEntry{Type: "proposal.superseded", Summary: "Proposal seq " + core.JSString(agPresent(p, "seq")) + " superseded by the local proposal seq " + core.JSString(seq) + " (site " + siteSlug + ")", Data: doc.Obj("revId", agPresent(p, "revId"), "byRevId", revID)})
				}
			}
		}
		if !rs.Exists() {
			tx.Create(revRef, cloud.EncodeRevision(revision))
		}
		tx.Create(propRef, proposal)
		if legacy != nil {
			tx.Create(mirrorRef, core.EncodeCommit(legacy, false))
		}
		upd := doc.Obj("agentEpoch", dep.Num("agentEpoch")+1)
		if log.Changed() {
			upd.Spread(log.DepUpdate())
		}
		tx.Update(depRef, upd)
		return doc.Obj("stored", true), nil
	})
}

func reportProposalStatus(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if b.Val("siteSlug") != any(siteSlug) {
		return nil, cloud.E.Bad("siteSlug", "V_REF", "siteSlug must equal the signing site")
	}
	staging, progress, status, errV := agPresent(b, "staging"), agPresent(b, "progress"), agPresent(b, "status"), agPresent(b, "error")
	undef := any(doc.Undefined)
	if staging != undef && !agIn([]string{"pending", "downloading", "ready", "failed"}, staging) {
		return nil, cloud.E.Bad("staging", "V_ENUM", "staging must be pending | downloading | ready | failed")
	}
	if progress != undef {
		if f, ok := progress.(float64); !ok || f < 0 || f > 1 {
			return nil, cloud.E.Bad("progress", "V_RANGE", "progress must be 0..1")
		}
	}
	if status != undef && !agIn([]string{"approved", "applying", "applied", "failed-rolled-back", "stale", "ready"}, status) {
		return nil, cloud.E.Bad("status", "V_ENUM", "status must be approved | applying | applied | failed-rolled-back | stale")
	}
	pRef := cloud.Col.Proposals(deploymentID).Doc(c.Params["revId"])
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ps, err := tx.Get(pRef)
		if err != nil {
			return nil, err
		}
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		if !ps.Exists() {
			return nil, cloud.E.NotFound("proposal not found")
		}
		cur := ps.Data()
		next := cloud.Clone(cur).(*doc.Map)
		now := doc.NowISO()
		curStatus := cur.Str("status")
		terminal := core.IsTerminal(curStatus)
		if !terminal && (staging != undef || progress != undef || errV != undef) {
			s := next.Map("sites").Map(siteSlug)
			if s == nil {
				s = doc.Obj("staging", "pending", "progress", 0.0, "error", nil, "updatedAt", nil)
			}
			prog := agOr(b, "progress", nil)
			if prog == nil {
				if staging == any("ready") {
					prog = 1.0
				} else {
					prog = agPresent(s, "progress")
				}
			}
			var e any
			switch {
			case errV != undef:
				e = errV
			case doc.Truthy(staging) && staging != any("failed"):
				e = nil
			default:
				e = agPresent(s, "error")
			}
			next.Map("sites").Set(siteSlug, doc.Obj("staging", agOr(b, "staging", agPresent(s, "staging")), "progress", prog, "error", e, "updatedAt", now))
		}
		if doc.Truthy(status) {
			var allowed bool
			switch status {
			case "applied", "failed-rolled-back":
				allowed = true
			case "stale":
				allowed = !terminal
			case "ready":
				allowed = curStatus == "approved"
			default:
				allowed = agIn([]string{"published", "staging", "ready", "approved", "applying"}, curStatus)
			}
			if allowed {
				next.Set("status", status)
			}
		}
		for _, k := range []string{"approvedBy", "approvedAt", "updateId", "result"} {
			if v := agPresent(b, k); v != undef {
				next.Set(k, v)
			}
		}
		// LOCK failed: the coordinator reports the result without a status → approved goes back to ready
		if !doc.Truthy(status) && b.Truthy("result") && b.Map("result").Val("ok") == any(false) && curStatus == "approved" {
			next.Set("status", "ready")
		}
		next.Set("status", DeriveStatus(next))
		if cloud.SameJSON(next.Shallow().Set("updatedAt", nil), cur.Shallow().Set("updatedAt", nil)) {
			return doc.Obj("ok", true, "proposal", cur), nil
		}
		next.Set("updatedAt", now)
		tx.Set(pRef, next)
		dep := cloud.DecodeDeployment(ds.Data())
		tx.Update(depRef, doc.Obj("agentEpoch", dep.Num("agentEpoch")+1))
		return doc.Obj("ok", true, "proposal", next), nil
	})
}

func getAgentRevision(c *cloud.Ctx) (any, error) {
	r, err := agLoadRev(c.Agent.DeploymentID, c.Params["revId"])
	if err != nil {
		return nil, err
	}
	if err := agSecretFetched(c, r); err != nil {
		return nil, err
	}
	return doc.Obj("revision", r), nil
}

func getImageManifest(c *cloud.Ctx) (any, error) {
	serviceSlug, version := c.Params["serviceSlug"], c.Params["version"]
	allowed, err := agAllowedFor(c.Agent.DeploymentID, c.Agent.Dep)
	if err != nil {
		return nil, err
	}
	if _, ok := allowed.images[shared.ImageKey(serviceSlug, version)]; !ok {
		return nil, cloud.E.Forbidden("the image is not in the head or an open proposal")
	}
	s, err := cloud.Col.Images(serviceSlug).Doc(version).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("image not found")
	}
	img := s.Data()
	imp := img.Map("import")
	if imp.Val("status") != any("ready") || !img.Truthy("digest") || !imp.Truthy("configDigest") {
		return nil, cloud.E.Bad("import", "V_IMAGE_NOT_READY", "import status is "+core.JSString(agPresent(imp, "status")))
	}
	return doc.Obj("serviceSlug", serviceSlug, "version", version, "digest", agPresent(img, "digest"), "platform", agPresent(img, "platform"), "mediaType", agOr(imp, "manifestMediaType", "application/vnd.oci.image.manifest.v1+json"), "config", doc.Obj("digest", agPresent(imp, "configDigest"), "sizeBytes", agOr(img, "_configSizeBytes", 0.0)), "layers", agPresent(imp, "layers")), nil
}

func getAgentBlob(c *cloud.Ctx) (any, error) {
	a := c.Agent
	hex := cloud.HexOf(c.Params["sha256"])
	if !cloud.IsHex64(hex) {
		return nil, cloud.E.Bad("sha256", "V_FORMAT", `sha256 must be "sha256:" + 64 hex`)
	}
	allowed, err := agAllowedFor(a.DeploymentID, a.Dep)
	if err != nil {
		return nil, err
	}
	// §21 #101 also: the trees + files of a pending volume-content push to this site (and its base)
	what, ok := allowed.blobs[hex]
	if !ok {
		push, err := core.PushBlobAllowed(a.DeploymentID, a.SiteSlug, a.Dep.Num("agentEpoch"), hex)
		if err != nil {
			return nil, err
		}
		if !push {
			return nil, cloud.E.Forbidden("the blob does not belong to the head or an open proposal")
		}
		what = agBlobRef{kind: "file"}
	}
	path := core.Paths.Blob(hex)
	if what.kind == "file" {
		path = core.Paths.File(hex)
	}
	here, err := core.Exists(path)
	if err != nil {
		return nil, err
	}
	if what.kind == "layer" && !here && what.serviceSlug != "" && what.version != "" {
		s, err := cloud.Col.Images(what.serviceSlug).Doc(what.version).Get()
		if err != nil {
			return nil, err
		}
		if img := s.Data(); img.Map("import").Val("mode") == any("gateway") {
			if err := core.FetchLayer(img, "sha256:"+hex); err != nil {
				return nil, err
			}
		}
	}
	if what.kind == "file" && !here {
		inBlobs, err := core.Exists(core.Paths.Blob(hex))
		if err != nil {
			return nil, err
		}
		if inBlobs {
			path = core.Paths.Blob(hex)
		}
	}
	return core.ServeObject(path, core.ServeOpts{Range: c.Headers["range"], Headers: map[string]string{"X-OPM-Sha256": hex}, NoRedirect: core.NoRedirectFor(c.Headers, agRelayed(a))})
}

func uploadAgentActivities(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if b.Val("siteSlug") != any(siteSlug) {
		return nil, cloud.E.Bad("siteSlug", "V_REF", "siteSlug must equal the signing site")
	}
	entries, ok := b.Val("entries").([]any)
	if !ok {
		return nil, cloud.E.Bad("entries", "V_TYPE", "entries must be a list")
	}
	if len(entries) > 500 {
		return nil, cloud.E.Bad("entries", "V_TOO_MANY", "at most 500 entries per call")
	}
	ack := c.Agent.Agent.Num("ackSeq")
	seqOf := func(i int) float64 {
		m, _ := entries[i].(*doc.Map)
		return m.Num("seq")
	}
	if len(entries) > 0 {
		for i, x := range entries {
			e, _ := x.(*doc.Map)
			if !agIsInt(e.Val("seq")) {
				return nil, cloud.E.Bad(fmt.Sprintf("entries[%d].seq", i), "V_TYPE", "seq must be an integer")
			}
			if i > 0 && seqOf(i) != seqOf(i-1)+1 {
				return nil, cloud.E.Bad(fmt.Sprintf("entries[%d].seq", i), "V_RANGE", "entries must be contiguous ascending (missing seq "+core.JSString(seqOf(i-1)+1)+")")
			}
			if e.Val("siteSlug") != any(siteSlug) {
				return nil, cloud.E.Bad(fmt.Sprintf("entries[%d].siteSlug", i), "V_REF", "entries must be of the signing site")
			}
		}
		if seqOf(0) > ack+1 {
			return nil, cloud.E.Bad("entries[0].seq", "V_RANGE", "missing seq "+core.JSString(ack+1))
		}
	}
	var r *doc.Map
	if len(entries) > 0 {
		if r, err = core.IngestSiteLog(deploymentID, siteSlug, entries, "pipe"); err != nil {
			return nil, err
		}
	} else {
		r = doc.Obj("inserted", 0.0, "skipped", 0.0, "rejected", 0.0, "errors", []any{}, "ackSeq", ack)
	}
	res := doc.Obj("ackSeq", agPresent(r, "ackSeq"), "inserted", agPresent(r, "inserted"), "skipped", agPresent(r, "skipped"), "rejected", agPresent(r, "rejected"), "errors", agPresent(r, "errors"))
	if r.Num("rejected") > 0 {
		list := r.Arr("errors")
		first, _ := list[0].(*doc.Map)
		mapped := make([]any, len(list))
		for i, x := range list {
			e := x.(*doc.Map)
			mapped[i] = doc.Obj("path", agPresent(e, "activityId"), "code", "V_CHAIN", "message", agPresent(e, "reason"), "severity", "error")
		}
		return nil, cloud.E.New("invalid", "activity chain broken at "+core.JSString(agPresent(first, "activityId"))+": "+core.JSString(agPresent(first, "reason")), res.Shallow().Set("errors", mapped).Set("rejectedEntries", list), nil)
	}
	return res, nil
}

func listAgentTransfers(c *cloud.Ctx) (any, error) {
	dir, ok := c.Query.Lookup("direction")
	if !ok {
		dir = "to-site"
	}
	if dir != "to-site" {
		return nil, cloud.E.Bad("direction", "V_ENUM", "direction must be to-site")
	}
	q, err := cloud.Col.Transfers(c.Agent.DeploymentID).Where("siteSlug", "==", c.Agent.SiteSlug).Where("direction", "==", "to-site").OrderBy("createdAt", "desc").Limit(500).Get()
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, d := range q.Docs {
		t := d.Data()
		if agIn([]string{"ready", "saved-on-server", "downloaded"}, t.Val("status")) {
			items = append(items, t.Shallow().Set("expiresAt", nil))
		}
	}
	return doc.Obj("items", items), nil
}

func createAgentTransfer(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	actor, ok := b.StrOK("actor")
	if !ok || !shared.RE.Email.MatchString(actor) {
		return nil, cloud.E.Bad("actor", "V_FORMAT", "actor (email) is required")
	}
	body := doc.Obj("fileName", agPresent(b, "fileName"), "sizeBytes", agPresent(b, "sizeBytes"), "sha256", agPresent(b, "sha256"))
	if n := agPresent(b, "note"); n != any(doc.Undefined) {
		body.Set("note", n)
	}
	if err := core.ValidateTransferBody(body); err != nil {
		return nil, err
	}
	tRef := cloud.Col.Transfers(deploymentID).Doc()
	target := core.Paths.Transfer(deploymentID, tRef.ID())
	upload := core.NewUpload(core.NewUploadOpts{Purpose: "transfer", DeploymentID: deploymentID, TransferID: tRef.ID(), CreatorSite: siteSlug, SizeBytes: body.Num("sizeBytes"), Sha256: body.Str("sha256"), CreatedBy: actor, Target: target})
	t := core.NewTransfer(core.NewTransferOpts{DeploymentID: deploymentID, SiteSlug: siteSlug, Direction: "to-cloud", Body: body, CreatedBy: actor, UploadID: upload.Str("uploadId")}).Set("transferId", tRef.ID()).Set("storagePath", target)
	batch := cloud.DB().Batch()
	batch.Create(tRef, t)
	batch.Create(cloud.Col.Uploads().Doc(upload.Str("uploadId")), upload)
	if err := batch.Commit(); err != nil {
		return nil, err
	}
	return doc.Obj("transfer", t, "upload", core.UploadView(upload)), nil
}

func reportAgentTransfer(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	status, _ := b.StrOK("status")
	if !agIn([]string{"ready", "saved-on-server", "downloaded", "fetched"}, b.Val("status")) {
		return nil, cloud.E.Bad("status", "V_ENUM", "status must be ready | saved-on-server | downloaded | fetched")
	}
	savedPath, isPath := b.StrOK("savedPath")
	if status == "saved-on-server" && (!isPath || !strings.HasPrefix(savedPath, "/")) {
		return nil, cloud.E.Bad("savedPath", "V_FORMAT", "savedPath (absolute) is required")
	}
	tRef := cloud.Col.Transfers(deploymentID).Doc(c.Params["transferId"])
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ts, err := tx.Get(tRef)
		if err != nil {
			return err
		}
		ds, err := tx.Get(depRef)
		if err != nil {
			return err
		}
		if !ts.Exists() {
			return cloud.E.NotFound("transfer not found")
		}
		t := ts.Data()
		if t.Val("siteSlug") != any(siteSlug) {
			return cloud.E.NotFound("transfer not found")
		}
		tStatus := agPresent(t, "status")
		if tStatus == any("expired") {
			return cloud.E.Bad("status", "V_RANGE", "the transfer expired")
		}
		direction := t.Val("direction")
		if status == "ready" {
			if direction != any("to-cloud") {
				return cloud.E.Bad("status", "V_ENUM", "ready is reported only for to-cloud transfers")
			}
			if tStatus == any("uploading") {
				return cloud.E.Bad("status", "V_RANGE", "complete the upload first")
			}
			return nil
		}
		if direction != any("to-site") {
			return cloud.E.Bad("status", "V_ENUM", status+" is reported only for to-site transfers")
		}
		if tStatus == any("uploading") {
			return cloud.E.Bad("status", "V_RANGE", "the transfer is still uploading")
		}
		// §21 #100: the site has the file (store-and-forward); saved / downloaded imply it
		fetched := doc.New()
		if !t.Truthy("fetchedAt") {
			fetched.Set("fetchedAt", doc.NowISO())
		}
		if status == "fetched" {
			if t.Truthy("fetchedAt") {
				return nil // idempotent
			}
			tx.Update(tRef, fetched)
			return nil
		}
		saved := status == "saved-on-server"
		if tStatus == any(status) && (!saved || agPresent(t, "savedPath") == any(savedPath)) {
			return nil
		}
		dep := cloud.DecodeDeployment(ds.Data())
		log := core.NewCloudLog(deploymentID, dep)
		upd := doc.Obj("status", status).Spread(fetched)
		if saved {
			upd.Set("savedPath", savedPath)
		}
		tx.Update(tRef, upd)
		fileName := core.JSString(agPresent(t, "fileName"))
		data := doc.Obj("transferId", agPresent(t, "transferId"), "direction", agPresent(t, "direction"), "fileName", agPresent(t, "fileName"), "sizeBytes", agPresent(t, "sizeBytes"), "siteSlug", siteSlug)
		e := core.CloudEntry{Type: "transfer.received", Summary: "File " + fileName + " downloaded on site " + siteSlug, Origin: "agent"}
		if saved {
			data.Set("savedPath", savedPath)
			e.Type = "transfer.saved"
			e.Summary = "File " + fileName + " saved on site " + siteSlug + " at " + savedPath
		}
		e.Data = data
		if s, ok := b.StrOK("actor"); ok {
			e.Actor = s
		}
		log.Add(tx, e)
		depUpd := log.DepUpdate()
		if tStatus == any("ready") {
			depUpd.Set("agentEpoch", dep.Num("agentEpoch")+1)
		}
		tx.Update(depRef, depUpd)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("ok", true), nil
}

func downloadAgentTransfer(c *cloud.Ctx) (any, error) {
	a := c.Agent
	s, err := cloud.Col.Transfers(a.DeploymentID).Doc(c.Params["transferId"]).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("transfer not found")
	}
	t := s.Data()
	if t.Val("siteSlug") != any(a.SiteSlug) || t.Val("direction") != any("to-site") {
		return nil, cloud.E.NotFound("transfer not found")
	}
	if t.Val("status") == any("expired") {
		return nil, cloud.E.Gone("the transfer was deleted")
	}
	if t.Val("status") == any("uploading") {
		return nil, cloud.E.NotFound("the transfer is still uploading")
	}
	return core.ServeObject(t.Str("storagePath"), core.ServeOpts{Range: c.Headers["range"], FileName: t.Str("fileName"), Headers: map[string]string{"X-OPM-Sha256": cloud.HexOf(t.Str("sha256"))}, NoRedirect: core.NoRedirectFor(c.Headers, agRelayed(a))})
}

func agUploadCreator(c *cloud.Ctx) core.Creator {
	return core.Creator{Agent: true, DeploymentID: c.Agent.DeploymentID, SiteSlug: c.Agent.SiteSlug}
}

func getAgentUpload(c *cloud.Ctx) (any, error) {
	u, err := core.LoadUpload(c.Params["uploadId"], agUploadCreator(c), true)
	if err != nil {
		return nil, err
	}
	if u.Val("status") == any("failed") {
		msg := "the upload failed"
		if u.Truthy("error") {
			msg += ": " + core.JSString(u.Val("error"))
		}
		return nil, cloud.E.Gone(msg)
	}
	if exp, ok := doc.ParseISO(u.Str("expiresAt")); u.Val("status") == any("open") && ok && exp <= time.Now().UnixMilli() {
		return nil, cloud.E.Gone("the upload expired")
	}
	return core.UploadView(u), nil
}

func putAgentUploadPart(c *cloud.Ctx) (any, error) {
	u, err := core.LoadUpload(c.Params["uploadId"], agUploadCreator(c), false)
	if err != nil {
		return nil, err
	}
	r, err := core.PutPart(u, c.Params["index"], c.Body)
	if err != nil {
		return nil, err
	}
	// §21 #123 a holder sending its copy: the console follows cloud.fetch.bytesDone
	if u.Val("purpose") == any("file") && u.Truthy("deploymentId") && u.Truthy("transferId") {
		fRef := cloud.Col.Files(u.Str("deploymentId")).Doc(u.Str("transferId"))
		bytesDone := 0.0
		for _, i := range r.Arr("received") {
			bytesDone += core.ExpectedPartSize(u, i.(float64))
		}
		_ = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
			s, err := tx.Get(fRef)
			if err != nil {
				return err
			}
			f := s.Data()
			if f.Map("cloud").Truthy("fetch") && f.Val("_uploadId") == agPresent(u, "uploadId") {
				tx.Update(fRef, doc.Obj("cloud.fetch.bytesDone", bytesDone))
			}
			return nil
		})
	}
	return r, nil
}

func completeAgentUpload(c *cloud.Ctx) (any, error) {
	u, err := core.LoadUpload(c.Params["uploadId"], agUploadCreator(c), false)
	if err != nil {
		return nil, err
	}
	return core.CompleteUpload(u, core.Actor{ID: agPresent(u, "createdBy"), SiteSlug: c.Agent.SiteSlug})
}

func agB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func agErrorBody(e *cloud.ApiError) string {
	return agB64([]byte(doc.MarshalString(doc.Obj("error", doc.Obj("code", e.Code, "message", e.Message, "details", e.Details)))))
}

func relay(c *cloud.Ctx) (any, error) {
	outer := c.Agent
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	rV := b.Val("request")
	forSite, ok := b.StrOK("forSite")
	if !ok || !shared.RE.Slug.MatchString(forSite) {
		return nil, cloud.E.Bad("forSite", "V_FORMAT", "forSite must be a site slug")
	}
	if forSite == outer.SiteSlug {
		return nil, cloud.E.Bad("forSite", "V_REF", "a site cannot relay for itself")
	}
	r, _ := rV.(*doc.Map)
	method, isM := r.StrOK("method")
	path, isP := r.StrOK("path")
	if !agObjectLike(rV) || !isM || !isP || !agObjectLike(r.Val("headers")) {
		return nil, cloud.E.Bad("request", "V_TYPE", "request { method, path, headers, bodyB64 } is required")
	}
	if !strings.HasPrefix(path, "/v1/agent/") || strings.HasPrefix(path, "/v1/agent/relay") || strings.HasPrefix(path, "/v1/agent/enroll") {
		return nil, cloud.E.Bad("request.path", "V_FORMAT", "only /v1/agent/* requests (not relay / enroll) can be relayed")
	}
	headers := map[string]string{}
	r.Map("headers").Each(func(k string, v any) {
		if s, ok := v.(string); ok {
			headers[strings.ToLower(k)] = s
		}
	})
	if headers["x-opm-site"] != outer.DeploymentID+"/"+forSite {
		return nil, cloud.E.Bad("request.headers.X-OPM-Site", "V_REF", "the inner request must be signed by "+outer.DeploymentID+"/"+forSite)
	}
	body := []byte{}
	if bv := r.Val("bodyB64"); doc.Truthy(bv) {
		s, ok := bv.(string)
		if !ok {
			return nil, errors.New("relay: bodyB64 is not a string")
		}
		body = doc.B64Decode(s)
	}
	inner, err := cloud.VerifyAgent(cloud.SignedParts{Method: strings.ToUpper(method), Path: path, Headers: headers, Body: body}, strings.HasPrefix(path, "/v1/agent/poll"))
	if err != nil {
		var e *cloud.ApiError
		if errors.As(err, &e) {
			return doc.Obj("status", float64(e.Status()), "headers", doc.Obj("Content-Type", "application/json; charset=utf-8", "X-OPM-Server-Time", strconv.FormatInt(time.Now().Unix(), 10)), "bodyB64", agErrorBody(e)), nil
		}
		return nil, err
	}
	relayedBy := outer.SiteSlug
	inner.RelayedBy = &relayedBy
	rep := cloud.Dispatch(cloud.ReqLike{Method: method, URL: path, Headers: headers, Body: body, IP: c.IP, Context: c.Context}, cloud.DispatchOpts{PreAgent: inner})
	data, err := agReplyBytes(rep)
	if err != nil {
		var e *cloud.ApiError
		if errors.As(err, &e) {
			return doc.Obj("status", float64(e.Status()), "headers", doc.Obj("Content-Type", "application/json; charset=utf-8"), "bodyB64", agErrorBody(e)), nil
		}
		return nil, err
	}
	if inner.Agent.Val("relayVia") != any(outer.SiteSlug) {
		_ = cloud.Col.Agents(inner.DeploymentID).Doc(inner.SiteSlug).Update(doc.Obj("relayVia", outer.SiteSlug))
	}
	var bodyB64 any
	if len(data) > 0 {
		bodyB64 = agB64(data)
	}
	return doc.Obj("status", float64(rep.Status), "headers", agOrderedHeaders(rep.Headers), "bodyB64", bodyB64), nil
}
