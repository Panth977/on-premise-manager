package fakecloud

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"

	"opm/agent/internal/model"
	"opm/agent/internal/volcontent"
)

// §21 #101 volume content (agent endpoints of VOLUME_CONTENT_CLOUD_API) + test helpers that play the cloud console:
// edits, restores, sends, snapshot requests. State per "<site>:<volume>"; trees are stored as the content-addressed
// blob files/<treeHash hex> (canonical JSON), exactly like the real cloud.

type vcVol struct {
	versions []model.VolumeContentVersion // seq order
	pushes   []*model.VolumeContentPush
	request  *model.VolumeSnapshotRequestRef
	live     *model.VolumeSyncReport
}

var (
	reVCVersions = regexp.MustCompile(`^/v1/agent/volume-content/([^/]+)/versions$`)
	reVCPush     = regexp.MustCompile(`^/v1/agent/volume-content/([^/]+)/pushes/([^/]+)$`)
	reVCBlob     = regexp.MustCompile(`^/v1/agent/volume-content/blobs/([^/]+)$`)
)

// vcol returns the state of a volume (c.mu held).
func (c *Cloud) vcol(site, vol string) *vcVol {
	if c.vc == nil {
		c.vc = map[string]*vcVol{}
	}
	k := site + ":" + vol
	if c.vc[k] == nil {
		c.vc[k] = &vcVol{}
	}
	return c.vc[k]
}

// routeVolumeContent handles the agent volume-content endpoints (false = not one of them).
func (c *Cloud) routeVolumeContent(w http.ResponseWriter, r *http.Request, body []byte, rc reqCtx) bool {
	p, m := r.URL.Path, r.Method
	var g []string
	switch {
	case m == "POST" && p == "/v1/agent/volume-content/blobs/check":
		var req model.AgentBlobsCheckRequest
		if !decode(w, body, &req) {
			return true
		}
		if len(req.Sha256s) > 1000 {
			writeErr(w, model.ErrInvalid, "at most 1000 per call", nil)
			return true
		}
		c.mu.Lock()
		miss := []string{}
		for _, s := range req.Sha256s {
			if _, ok := c.blobs[hexOf(s)]; !ok {
				miss = append(miss, s)
			}
		}
		c.mu.Unlock()
		writeJSON(w, 200, model.AgentBlobsCheckResponse{Missing: miss})
	case m == "PUT" && match(reVCBlob, p, &g):
		h := hexOf(g[1])
		if len(body) > model.VolumeContentSnapshotFileMaxBytes {
			writeErr(w, model.ErrTooLarge, "a snapshot file is at most 24 MiB", nil)
			return true
		}
		if sha256Hex(body) != h {
			writeErr(w, model.ErrInvalid, "sha256 mismatch", nil)
			return true
		}
		if !c.persistInHead(rc.signer, "") {
			writeErr(w, model.ErrForbidden, "the site runs no persist-to-cloud volume", nil)
			return true
		}
		c.PutBlob(body)
		writeJSON(w, 200, model.AgentPutBlobResponse{Sha256: "sha256:" + h, SizeBytes: int64(len(body))})
	case m == "POST" && match(reVCVersions, p, &g):
		c.agentSnapshot(w, body, rc.signer, g[1])
	case m == "POST" && match(reVCPush, p, &g):
		c.agentPushReport(w, body, rc.signer, g[1], g[2])
	default:
		return false
	}
	return true
}

func hexOf(s string) string {
	if len(s) > 7 && s[:7] == "sha256:" {
		return s[7:]
	}
	return s
}

// persistInHead: the volume (any when vol == "") is persisted in the head config the site reported.
func (c *Cloud) persistInHead(site, vol string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.agents[site]
	if a == nil || a.HeadRevID == nil {
		return false
	}
	rev, ok := c.revisions[*a.HeadRevID]
	if !ok {
		return false
	}
	for _, s := range rev.Config.Sites {
		if s.SiteSlug != site {
			continue
		}
		for _, v := range s.Config.VolumeList() {
			if v.Persist != nil && (vol == "" || v.VolumeSlug == vol) {
				return true
			}
		}
	}
	return false
}

// storeVersion appends a version (c.mu held); the tree is stored as its blob.
func (c *Cloud) storeVersion(site, vol string, v model.VolumeContentVersion, tree model.VolumeTree) model.VolumeContentVersion {
	st := c.vcol(site, vol)
	t := volcontent.Normalize(tree)
	in := volcontent.HashInput(t)
	c.blobs[sha256Hex([]byte(in))] = []byte(in)
	v.VersionID = c.newULID()
	v.Seq = len(st.versions) + 1
	v.DeploymentID, v.SiteSlug, v.VolumeSlug = c.DeploymentID, site, vol
	v.TreeHash = volcontent.Hash(t)
	v.CreatedAt = nowTs()
	files, dirs, total := volcontent.Stats(t)
	v.FileCount, v.DirCount, v.TotalBytes = files, dirs, total
	if v.Skipped == nil {
		v.Skipped = []model.VolumeSkipped{}
	}
	var prev *model.VolumeTree
	if n := len(st.versions); n > 0 {
		if pt, ok := c.treeLocked(st.versions[n-1].TreeHash); ok {
			prev = &pt
		}
	}
	for _, d := range volcontent.DiffTrees(prev, &t) {
		switch d.Kind {
		case "added":
			v.Changes.Added++
		case "removed":
			v.Changes.Removed++
		default:
			v.Changes.Modified++
		}
	}
	st.versions = append(st.versions, v)
	return v
}

func (c *Cloud) treeLocked(treeHash string) (model.VolumeTree, bool) {
	b, ok := c.blobs[hexOf(treeHash)]
	if !ok {
		return model.VolumeTree{}, false
	}
	var t model.VolumeTree
	if json.Unmarshal(b, &t) != nil {
		return model.VolumeTree{}, false
	}
	return t, true
}

func (c *Cloud) agentSnapshot(w http.ResponseWriter, body []byte, site, vol string) {
	if !c.persistInHead(site, vol) {
		writeErr(w, model.ErrForbidden, fmt.Sprintf("volume %s is not persisted to the cloud in the head of site %s", vol, site), nil)
		return
	}
	var req model.AgentVolumeSnapshotRequest
	if !decode(w, body, &req) {
		return
	}
	if issues := volcontent.TreeIssues(req.Tree, false); len(issues) > 0 {
		writeErr(w, model.ErrInvalid, issues[0].Message, nil)
		return
	}
	if volcontent.Hash(req.Tree) != req.TreeHash {
		writeErr(w, model.ErrInvalid, "treeHash does not match sha256(JCS(canonical tree))", nil)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.vcol(site, vol)
	for _, v := range st.versions {
		if v.AgentSnapshotID != nil && *v.AgentSnapshotID == req.SnapshotID {
			writeJSON(w, 200, model.AgentVolumeSnapshotResponse{Version: v, Existing: true})
			return
		}
	}
	var missing []string
	for _, e := range req.Tree.Entries {
		if _, ok := c.blobs[hexOf(e.Sha256)]; !ok {
			missing = append(missing, e.Sha256)
		}
	}
	if len(missing) > 0 {
		writeErr(w, model.ErrInvalid, fmt.Sprintf("%d file(s) of the tree are not uploaded", len(missing)), map[string]any{"missing": missing,
			"errors": []model.ValidationIssue{{Path: "tree", Code: "V_BLOB_MISSING", Message: "blobs missing", Severity: "error"}}})
		return
	}
	trig := req.Trigger
	by := "agent:" + site
	if req.Actor != nil && *req.Actor != "" {
		by = *req.Actor
	}
	sid := req.SnapshotID
	v := c.storeVersion(site, vol, model.VolumeContentVersion{VolumeContentVersionSummary: model.VolumeContentVersionSummary{Origin: "snapshot", CreatedBy: by},
		Trigger: &trig, Note: req.Note, ExcludedCount: req.ExcludedCount, SkippedCount: req.SkippedCount, Skipped: req.Skipped,
		AgentSnapshotID: &sid, RequestID: req.RequestID, PushID: req.PushID}, req.Tree)
	if req.RequestID != nil && st.request != nil && st.request.RequestID == *req.RequestID {
		st.request = nil
		c.epoch++
	}
	// (the agent logs volume-content.snapshot-uploaded in its own chain; the cloud writes no activity)
	writeJSON(w, 201, model.AgentVolumeSnapshotResponse{Version: v})
}

func (c *Cloud) agentPushReport(w http.ResponseWriter, body []byte, site, vol, pushID string) {
	var req model.AgentVolumePushReport
	if !decode(w, body, &req) {
		return
	}
	if req.Status != "applied" && req.Status != "kept-live" && req.Status != "failed" {
		writeErr(w, model.ErrInvalid, "status must be applied | kept-live | failed", nil)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.vcol(site, vol)
	for _, p := range st.pushes {
		if p.PushID != pushID {
			continue
		}
		if p.Status == "pending" {
			at, by, res := req.ResolvedAt, req.ResolvedBy, req.Result
			p.Status, p.ResolvedAt, p.ResolvedBy, p.Result = req.Status, &at, &by, &res
			c.epoch++
		}
		writeJSON(w, 200, p)
		return
	}
	writeErr(w, model.ErrNotFound, "push not found", nil)
}

// viewVolumeContent: CloudView.volumeContent (c.mu held).
func (c *Cloud) viewVolumeContent() []model.VolumeContentCloudItem {
	var out []model.VolumeContentCloudItem
	keys := make([]string, 0, len(c.vc))
	for k := range c.vc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		st := c.vc[k]
		var pending *model.VolumeContentPush
		if n := len(st.pushes); n > 0 && st.pushes[n-1].Status == "pending" {
			p := *st.pushes[n-1]
			pending = &p
		}
		if pending == nil && st.request == nil {
			continue
		}
		site, vol := splitKey(k)
		var req *model.VolumeSnapshotRequestRef
		if st.request != nil {
			r := *st.request
			req = &r
		}
		out = append(out, model.VolumeContentCloudItem{SiteSlug: site, VolumeSlug: vol, Push: pending, SnapshotRequest: req})
	}
	return out
}

func splitKey(k string) (string, string) {
	for i := 0; i < len(k); i++ {
		if k[i] == ':' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

// storeVolumeSync keeps the heartbeat reports (c.mu held).
func (c *Cloud) storeVolumeSync(site string, list []model.VolumeSyncReport) {
	for _, r := range list {
		r := r
		c.vcol(site, r.VolumeSlug).live = &r
	}
}

// ───────────────────────── test helpers (the cloud console)

// VolumeVersions returns the versions of a volume (seq order).
func (c *Cloud) VolumeVersions(site, vol string) []model.VolumeContentVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.VolumeContentVersion{}, c.vcol(site, vol).versions...)
}

// VolumeTree returns a stored tree by its hash.
func (c *Cloud) VolumeTree(treeHash string) (model.VolumeTree, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.treeLocked(treeHash)
}

// Blob returns stored bytes by sha256 ("sha256:" prefix optional).
func (c *Cloud) Blob(sha string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.blobs[hexOf(sha)]
	return b, ok
}

// CreateVolumeEdit saves an edit as a new version (origin edit, base = the latest); files = path → bytes are uploaded
// (content-addressed) and override / add entries of the latest tree; remove = paths deleted; dirs = empty folders added.
func (c *Cloud) CreateVolumeEdit(site, vol string, files map[string][]byte, remove []string, by string) (model.VolumeContentVersion, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.vcol(site, vol)
	var base model.VolumeTree
	var baseID *string
	if n := len(st.versions); n > 0 {
		latest := st.versions[n-1]
		t, ok := c.treeLocked(latest.TreeHash)
		if !ok {
			return model.VolumeContentVersion{}, fmt.Errorf("latest tree missing")
		}
		base = t
		id := latest.VersionID
		baseID = &id
	}
	m := map[string]model.VolumeTreeEntry{}
	for _, e := range base.Entries {
		m[e.Path] = e
	}
	for _, p := range remove {
		delete(m, p)
	}
	for p, b := range files {
		h := sha256Hex(b)
		c.blobs[h] = append([]byte(nil), b...)
		mode := "0644"
		if old, ok := m[p]; ok {
			mode = old.Mode
		}
		m[p] = model.VolumeTreeEntry{Path: p, Sha256: "sha256:" + h, Size: int64(len(b)), Mode: mode, Render: "none"}
	}
	t := model.VolumeTree{Entries: []model.VolumeTreeEntry{}, Dirs: base.Dirs}
	for _, e := range m {
		t.Entries = append(t.Entries, e)
	}
	t = volcontent.Normalize(t)
	if by == "" {
		by = "editor@example.com"
	}
	v := c.storeVersion(site, vol, model.VolumeContentVersion{VolumeContentVersionSummary: model.VolumeContentVersionSummary{Origin: "edit", CreatedBy: by}, BaseVersionID: baseID, Skipped: []model.VolumeSkipped{}}, t)
	c.cloudActivity("volume-content.version-created", fmt.Sprintf("Volume %s: version %d (edit)", vol, v.Seq), map[string]any{"siteSlug": site, "volumeSlug": vol, "versionId": v.VersionID, "seq": v.Seq, "origin": "edit"})
	return v, nil
}

// SendVolumeContent sends a version to the site (supersedes a pending push). base = the newest SNAPSHOT ancestor via
// the baseVersionId chain (nil when none).
func (c *Cloud) SendVolumeContent(site, vol, versionID string, restart bool, note string) (model.VolumeContentPush, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.vcol(site, vol)
	find := func(id string) *model.VolumeContentVersion {
		for i := range st.versions {
			if st.versions[i].VersionID == id {
				return &st.versions[i]
			}
		}
		return nil
	}
	v := find(versionID)
	if v == nil {
		return model.VolumeContentPush{}, fmt.Errorf("no version %s", versionID)
	}
	var base *model.VolumeContentVersion
	for cur := v; cur != nil; {
		if cur.Origin == "snapshot" {
			base = cur
			break
		}
		if cur.BaseVersionID == nil {
			break
		}
		cur = find(*cur.BaseVersionID)
	}
	now := nowTs()
	seq := v.Seq
	p := &model.VolumeContentPush{PushID: c.newULID(), SiteSlug: site, VolumeSlug: vol, VersionID: v.VersionID, VersionSeq: &seq, TreeHash: v.TreeHash, Restart: restart, Note: note,
		Status: "pending", CreatedAt: now, CreatedBy: "editor@example.com"}
	if base != nil {
		id, h := base.VersionID, base.TreeHash
		p.BaseVersionID, p.BaseTreeHash = &id, &h
	}
	if n := len(st.pushes); n > 0 && st.pushes[n-1].Status == "pending" {
		st.pushes[n-1].Status = "superseded"
		st.pushes[n-1].ResolvedAt = &now
	}
	st.pushes = append(st.pushes, p)
	c.epoch++
	c.cloudActivity("volume-content.sent", fmt.Sprintf("Version %d of volume %s sent to site %s", v.Seq, vol, site), map[string]any{"siteSlug": site, "volumeSlug": vol, "pushId": p.PushID, "versionId": v.VersionID})
	return *p, nil
}

// CancelVolumePush cancels the pending push.
func (c *Cloud) CancelVolumePush(site, vol string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.vcol(site, vol)
	if n := len(st.pushes); n > 0 && st.pushes[n-1].Status == "pending" {
		now := nowTs()
		st.pushes[n-1].Status, st.pushes[n-1].ResolvedAt = "cancelled", &now
		c.epoch++
		return true
	}
	return false
}

// VolumePushes returns the pushes of a volume (oldest first).
func (c *Cloud) VolumePushes(site, vol string) []model.VolumeContentPush {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []model.VolumeContentPush
	for _, p := range c.vcol(site, vol).pushes {
		out = append(out, *p)
	}
	return out
}

// RequestVolumeSnapshot opens a snapshot request (answered by the site's next snapshot with this requestId).
func (c *Cloud) RequestVolumeSnapshot(site, vol string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := &model.VolumeSnapshotRequestRef{RequestID: c.newULID(), CreatedAt: nowTs(), CreatedBy: "editor@example.com"}
	c.vcol(site, vol).request = r
	c.epoch++
	return r.RequestID
}

// VolumeSnapshotRequest returns the open request (nil = none).
func (c *Cloud) VolumeSnapshotRequest(site, vol string) *model.VolumeSnapshotRequestRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vcol(site, vol).request
}

// VolumeSyncLive returns the last heartbeat report of a volume.
func (c *Cloud) VolumeSyncLive(site, vol string) *model.VolumeSyncReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vcol(site, vol).live
}
