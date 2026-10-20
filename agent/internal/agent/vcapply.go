package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/volcontent"
)

// §21 #101 apply of a cloud push (VOLUME_SYNC_API): plan (3-way against the live tree), per-file bytes, apply (no
// intent wait: an apply is a merge, §21 #104 (e); apply/start + intentId stay for older GUIs: before-apply upload,
// Backups snapshot, [stop users], converge live → target, [start users], after-apply snapshot, report) and keep-live. Another site = forwarded over
// the mesh like the volume manager (api.go handleSiteRoute → forwardVolumeAction; both sites log).

// registerVolumeSyncRoutes adds the per-site routes (buildSiteMux).
func (a *Agent) registerVolumeSyncRoutes(h func(pattern string, kind authKind, fn apiFn)) {
	h("GET /volume-sync", authSession, a.handleListVolumeSync)
	h("GET /volume-sync/{volumeSlug}", authSession, a.handleGetVolumeSync)
	// §21 #117 content is committed and merged (Branch page): snapshots / plans / applies of cloud versions are gone
	h("POST /volume-sync/{volumeSlug}/snapshot", authActor, a.goneVolumeApply)
	h("GET /volume-sync/{volumeSlug}/plan", authSession, a.goneVolumeApply)
	h("GET /volume-sync/{volumeSlug}/file", authSession, a.goneVolumeApply)
	h("POST /volume-sync/{volumeSlug}/apply/start", authActor, a.goneVolumeApply)
	h("POST /volume-sync/{volumeSlug}/apply", authActor, a.goneVolumeApply)
	h("POST /volume-sync/{volumeSlug}/keep-live", authActor, a.goneVolumeApply)
}

// ───────────────────────── cloud trees

// fetchTree returns the tree whose hash is treeHash: a local snapshot with that hash, else the blob files/<hex>
// (getAgentBlob, peers) — the canonical tree JSON (its sha256 IS the tree hash).
func (a *Agent) fetchTree(ctx context.Context, treeHash string) (model.VolumeTree, error) {
	for _, r := range a.records(`WHERE data LIKE ? LIMIT 5`, "%"+treeHash+"%") {
		if r.Local.TreeHash == treeHash {
			return r.Req.Tree, nil
		}
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := a.ensureBlob(c, blobNeed{Hex: hexOf(treeHash), Kind: "file", Ref: model.FetchRef{FileSlug: "volume-tree"}}, nil); err != nil {
		return model.VolumeTree{}, fmt.Errorf("cloud tree %s: %w", short(treeHash), err)
	}
	b, err := a.contentBlobBytes(treeHash)
	if err != nil {
		return model.VolumeTree{}, err
	}
	var t model.VolumeTree
	if err := json.Unmarshal(b, &t); err != nil {
		return model.VolumeTree{}, fmt.Errorf("cloud tree %s: %w", short(treeHash), err)
	}
	if volcontent.Hash(t) != treeHash {
		return model.VolumeTree{}, fmt.Errorf("cloud tree %s: hash mismatch", short(treeHash))
	}
	return volcontent.Normalize(t), nil
}

// ensureContentBlob makes a content blob present locally (peers, then the cloud).
func (a *Agent) ensureContentBlob(ctx context.Context, slug string, e model.VolumeTreeEntry) error {
	c, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return a.ensureBlob(c, blobNeed{Hex: hexOf(e.Sha256), Size: e.Size, Kind: "file", Ref: model.FetchRef{FileSlug: "volume-" + slug}}, nil)
}

// ───────────────────────── plan

type vsPlan struct {
	p     *persistVol
	push  model.VolumeContentPush
	base  *model.VolumeTree
	cloud model.VolumeTree
	live  *vsLive
	walk  walkOut
	plan  model.VolumePushPlan
}

func (a *Agent) buildPlan(ctx context.Context, slug string) (*vsPlan, *httpError) {
	p, e := a.persistVolume(slug)
	if e != nil {
		return nil, e
	}
	push := a.pendingPush(slug)
	if push == nil {
		return nil, invalid("no cloud version is waiting for volume "+slug, nil)
	}
	cloud, err := a.fetchTree(ctx, push.TreeHash)
	if err != nil {
		return nil, herr(model.ErrUnavailable, err.Error(), nil)
	}
	cloud = volcontent.FilterExcluded(cloud, p.persist.Exclude)
	var base *model.VolumeTree
	if push.BaseTreeHash != nil {
		b, err := a.fetchTree(ctx, *push.BaseTreeHash)
		if err != nil {
			return nil, herr(model.ErrUnavailable, err.Error(), nil)
		}
		b = volcontent.FilterExcluded(b, p.persist.Exclude)
		base = &b
	}
	vol := a.vsync().vol(slug)
	vol.snapMu.Lock()
	live, w, err := a.scanLive(p, a.clock())
	vol.snapMu.Unlock()
	if err != nil {
		return nil, herr(model.ErrInternal, "scan the volume: "+err.Error(), nil)
	}
	items := volcontent.Plan(base, live.tree, cloud)
	pl := model.VolumePushPlan{PushID: push.PushID, VersionID: push.VersionID, BaseKnown: base != nil, LiveTreeHash: live.hash, CloudTreeHash: volcontent.Hash(cloud),
		Items: items, Counts: volcontent.PlanCounts(items), Exclude: append([]string{}, p.persist.Exclude...), Users: p.users, Restart: push.Restart}
	if pl.Users == nil {
		pl.Users = []string{}
	}
	if base != nil {
		h := volcontent.Hash(*base)
		pl.BaseTreeHash = &h
		pl.FastForward = h == live.hash
	}
	pl.UpToDate = pl.CloudTreeHash == live.hash
	if pl.Counts["conflict"] > 0 && !pl.FastForward && !pl.UpToDate {
		key := "vc-conflict:" + push.PushID
		var seen string
		if ok, _ := a.db.GetKV(key, &seen); !ok {
			_ = a.db.SetKV(key, nowTs())
			a.activity(nil, "agent", "volume-content.conflict", trim200(fmt.Sprintf("volume %s: the cloud version and the live volume both changed %d file(s)", slug, pl.Counts["conflict"])),
				map[string]any{"siteSlug": a.site, "volumeSlug": slug, "pushId": push.PushID, "versionId": push.VersionID, "conflicts": pl.Counts["conflict"], "liveTreeHash": live.hash, "baseTreeHash": pl.BaseTreeHash})
		}
	}
	return &vsPlan{p: p, push: *push, base: base, cloud: cloud, live: live, walk: w, plan: pl}, nil
}

func (a *Agent) handlePlanVolumeSync(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	pl, e := a.buildPlan(r.Context(), r.PathValue("volumeSlug"))
	if e != nil {
		return nil, 0, e
	}
	return pl.plan, 200, nil
}

// handleVolumeSyncFile: bytes of one file on one side of the plan (per-file diff).
func (a *Agent) handleVolumeSyncFile(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	slug := r.PathValue("volumeSlug")
	side, rel := r.URL.Query().Get("side"), r.URL.Query().Get("path")
	if pe := volcontent.PathError(rel); pe != "" {
		return nil, 0, invalid("path: "+pe, nil)
	}
	p, e := a.persistVolume(slug)
	if e != nil {
		return nil, 0, e
	}
	if volcontent.Compile(p.persist.Exclude).Excluded(rel, false) {
		return nil, 0, invalid(rel+" is excluded from the volume's content", nil)
	}
	var data []byte
	switch side {
	case "live":
		abs := filepath.Join(p.root, filepath.FromSlash(rel))
		if err := noLinkOnPath(p.root, abs); err != nil {
			return nil, 0, invalid(err.Error(), nil)
		}
		st, err := os.Lstat(abs)
		if err != nil || !st.Mode().IsRegular() {
			return nil, 0, herr(model.ErrNotFound, rel+" is not a file in the live volume", nil)
		}
		if st.Size() > model.VolumeContentSnapshotFileMaxBytes {
			return nil, 0, herr(model.ErrTooLarge, rel+" is too large to show", nil)
		}
		if data, err = os.ReadFile(abs); err != nil {
			return nil, 0, herr(model.ErrNotFound, err.Error(), nil)
		}
	case "cloud", "base":
		push := a.pendingPush(slug)
		if push == nil {
			return nil, 0, invalid("no cloud version is waiting for volume "+slug, nil)
		}
		th := push.TreeHash
		if side == "base" {
			if push.BaseTreeHash == nil {
				return nil, 0, herr(model.ErrNotFound, "the base is unknown", nil)
			}
			th = *push.BaseTreeHash
		}
		t, err := a.fetchTree(r.Context(), th)
		if err != nil {
			return nil, 0, herr(model.ErrUnavailable, err.Error(), nil)
		}
		var ent *model.VolumeTreeEntry
		for i := range t.Entries {
			if t.Entries[i].Path == rel {
				ent = &t.Entries[i]
			}
		}
		if ent == nil {
			return nil, 0, herr(model.ErrNotFound, rel+" is not in the "+side+" version", nil)
		}
		if err := a.ensureContentBlob(r.Context(), slug, *ent); err != nil {
			return nil, 0, herr(model.ErrUnavailable, err.Error(), nil)
		}
		if data, err = a.contentBlobBytes(ent.Sha256); err != nil {
			return nil, 0, herr(model.ErrNotFound, err.Error(), nil)
		}
	default:
		return nil, 0, invalid("side must be live | cloud | base", nil)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", volcontent.BaseName(rel)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(data)
	return nil, 0, nil
}

// ───────────────────────── intent + apply

func (a *Agent) contentGuard(slug string) *httpError {
	if why := a.volumeBusy(slug); why != "" {
		return herr(model.ErrConflict, "volume "+slug+" is busy: "+why, nil)
	}
	if why := a.contentBusy(slug); why != "" {
		return herr(model.ErrConflict, "volume "+slug+" is busy: "+why, nil)
	}
	return nil
}

func (a *Agent) claimContent(slug, what string) bool {
	s := a.vsync()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[slug] != "" {
		return false
	}
	s.busy[slug] = what
	return true
}

func (a *Agent) releaseContent(slug string) {
	s := a.vsync()
	s.mu.Lock()
	delete(s.busy, slug)
	s.mu.Unlock()
}

func (a *Agent) handleApplyVolumeSyncStart(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	slug := r.PathValue("volumeSlug")
	if _, e := a.persistVolume(slug); e != nil {
		return nil, 0, e
	}
	if a.pendingPush(slug) == nil {
		return nil, 0, invalid("no cloud version is waiting for volume "+slug, nil)
	}
	if e := a.contentGuard(slug); e != nil {
		return nil, 0, e
	}
	in := a.createIntent("volume-content-apply", &slug, nil, getRC(r))
	in.RevID = nil
	in.Volume = &model.IntentVolume{SiteSlug: a.site, VolumeSlug: slug}
	g := model.GateCheck{Open: true, Conditions: []model.GateCondition{{Key: "idle", Ok: true, Detail: "no update and no other action on this volume is running"}}, Sites: []string{a.site}}
	return model.ApproveStartResponse{Intent: in, Gate: g}, 201, nil
}

type pushDone struct {
	Push   model.VolumeContentPush       `json:"push"`
	Result model.VolumeContentPushResult `json:"result"`
}

// resolvePush records the outcome locally (the GUI stops offering the push) and reports it (outbox, store-and-forward).
func (a *Agent) resolvePush(slug string, push model.VolumeContentPush, status, actor string, res model.VolumeContentPushResult) model.VolumeContentPush {
	now := nowTs()
	push.Status, push.ResolvedAt, push.ResolvedBy, push.Result = status, &now, &actor, &res
	_ = a.db.SetKV("vc-push:"+push.PushID, pushDone{Push: push, Result: res})
	a.enqueue("volume-push", "volume-push:"+push.PushID, "POST", "/v1/agent/volume-content/"+slug+"/pushes/"+push.PushID,
		model.AgentVolumePushReport{Status: status, ResolvedBy: actor, ResolvedAt: now, Result: res})
	return push
}

func (a *Agent) handleApplyVolumeSync(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.VolumeSyncApplyRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	slug := r.PathValue("volumeSlug")
	if _, e := a.persistVolume(slug); e != nil {
		return nil, 0, e
	}
	if req.Resolution != "overwrite" && req.Resolution != "keep-live" && req.Resolution != "per-file" {
		return nil, 0, invalid("resolution must be overwrite | keep-live | per-file", nil)
	}
	push := a.pendingPush(slug)
	if push == nil || push.PushID != req.PushID {
		return nil, 0, herr(model.ErrConflict, "this cloud version is no longer waiting (superseded, cancelled or resolved): reload", nil)
	}
	// an apply is a merge (§21 #104 (e)): no 60 s intent wait; an intent of an older GUI is still checked
	if req.IntentID != "" {
		if e := a.checkIntentWord(req.IntentID, "volume-content-apply", slug, rc, "ACCEPT", "ACCEPT"); e != nil {
			return nil, 0, e
		}
	}
	if e := a.contentGuard(slug); e != nil {
		return nil, 0, e
	}
	if !a.claimContent(slug, "apply of a cloud version") {
		return nil, 0, herr(model.ErrConflict, "volume "+slug+" is busy", nil)
	}
	defer a.releaseContent(slug)
	ctx := a.ctx
	if ctx == nil {
		ctx = r.Context()
	}
	pl, e := a.buildPlan(ctx, slug)
	if e != nil {
		return nil, 0, e
	}
	if pl.plan.LiveTreeHash != req.LiveTreeHash {
		return nil, 0, herr(model.ErrConflict, "the live volume changed since the plan: review the plan again", map[string]any{"liveTreeHash": pl.plan.LiveTreeHash})
	}
	var ff *model.FastForwardUse
	if req.IntentID != "" {
		ff = a.markIntentUsed(req.IntentID, rc.actor)
	}
	if req.Resolution == "keep-live" {
		p, res := a.keepLive(ctx, pl, rc.actor, ff)
		return model.VolumeSyncApplyResponse{Push: p, Result: res}, 200, nil
	}
	target, err := volcontent.Merge(pl.base, pl.live.tree, pl.cloud, req.Resolution, req.Choices)
	if err != nil {
		return nil, 0, invalid(err.Error(), []model.ValidationIssue{{Path: "choices", Code: "V_REQUIRED", Message: err.Error(), Severity: "error"}})
	}
	p, res, ferr := a.applyPush(ctx, pl, req, target, rc.actor, ff)
	if ferr != nil {
		return nil, 0, herr(model.ErrInternal, ferr.Error(), map[string]any{"push": p, "result": res})
	}
	return model.VolumeSyncApplyResponse{Push: p, Result: res}, 200, nil
}

// applyPush runs the apply steps (brief §4); the users are always started again.
func (a *Agent) applyPush(ctx context.Context, pl *vsPlan, req model.VolumeSyncApplyRequest, target model.VolumeTree, actor string, ff *model.FastForwardUse) (model.VolumeContentPush, model.VolumeContentPushResult, error) {
	slug := pl.p.decl.VolumeSlug
	who := &actor
	pushID := pl.push.PushID
	res := model.VolumeContentPushResult{Resolution: req.Resolution, Restarted: []string{}}
	switch {
	case pl.plan.UpToDate:
		res.Resolution = "up-to-date"
	case pl.plan.FastForward:
		res.Resolution = "fast-forward"
	}
	for _, it := range pl.plan.Items {
		if it.Kind == "live" || (it.Kind == "conflict" && req.Resolution == "per-file" && req.Choices[it.Path] == "live") {
			res.KeptLive++
		}
	}
	fail := func(step string, err error) (model.VolumeContentPush, model.VolumeContentPushResult, error) {
		msg := err.Error()
		res.Error = &msg
		p := a.resolvePush(slug, pl.push, "failed", actor, res)
		a.activity(who, "agent", "volume-content.apply-failed", trim200(fmt.Sprintf("applying the cloud version to volume %s failed at %s: %s", slug, step, msg)),
			map[string]any{"siteSlug": a.site, "volumeSlug": slug, "pushId": pushID, "versionId": pl.push.VersionID, "step": step, "error": msg})
		return p, res, fmt.Errorf("%s: %w", step, err)
	}
	// 1. every needed blob present before anything stops
	liveMap := map[string]model.VolumeTreeEntry{}
	for _, e := range pl.live.tree.Entries {
		liveMap[e.Path] = e
	}
	for _, e := range target.Entries {
		if l, ok := liveMap[e.Path]; ok && l.Sha256 == e.Sha256 {
			continue
		}
		if err := a.ensureContentBlob(ctx, slug, e); err != nil {
			return fail("fetch", err)
		}
	}
	// 2. before-apply snapshot (live changed since the last snapshot) → uploaded when the cloud is reachable
	if last := a.lastRecord(slug); last == nil || last.Local.TreeHash != pl.live.hash {
		if rec, created, err := a.snapshotVolume(pl.p, snapOpts{trigger: "before-apply", actor: who, pushID: &pushID, note: "before applying cloud version " + pl.push.VersionID}); err != nil {
			return fail("before-apply snapshot", err)
		} else if created {
			a.uploadNow(ctx, rec)
		}
	}
	// 3. a Backups snapshot of the volume
	t, he := a.volumeTarget(slug)
	if he != nil {
		return fail("snapshot", fmt.Errorf("%s", he.msg))
	}
	if volExists(t.ref.Dir) {
		id, _, err := a.snapshotOneVolume(ctx, t, actor, "before applying cloud version "+versionLabel(pl.push))
		if err != nil {
			return fail("snapshot", err)
		}
		res.SnapshotID = &id
	}
	// 4. stop the users (reverse start order), always started again
	var stopped []string
	if req.Restart {
		s, err := a.stopUsers(ctx, t, t.ref.Containers)
		stopped = s
		if err != nil {
			if serr := a.startUsers(stopped); serr != nil {
				a.log.Warn("volsync", "start again failed", "error", serr.Error())
			}
			return fail("stop", err)
		}
	}
	// 5. converge live → target
	vol := a.vsync().vol(slug)
	vol.snapMu.Lock()
	written, deleted, cerr := a.converge(pl.p.root, volcontent.Compile(pl.p.persist.Exclude), pl.walk, pl.live.tree, target)
	vol.snapMu.Unlock()
	a.forgetVolumeSizes()
	res.Written, res.Deleted = written, deleted
	// 6. start the users (also on failure)
	if len(stopped) > 0 {
		if err := a.startUsers(stopped); err != nil {
			if cerr == nil {
				cerr = fmt.Errorf("start %s: %w", strings.Join(stopped, ", "), err)
			}
		}
		res.Restarted = append([]string{}, stopped...)
	}
	if cerr != nil {
		return fail("write", cerr)
	}
	// 7. after-apply snapshot + upload
	if rec, _, err := a.snapshotVolume(pl.p, snapOpts{trigger: "after-apply", actor: who, pushID: &pushID, force: true, note: "after applying cloud version " + pl.push.VersionID}); err == nil {
		res.AfterVersionID = a.uploadNow(ctx, rec)
	}
	// 8. report + activity
	p := a.resolvePush(slug, pl.push, "applied", actor, res)
	sum := fmt.Sprintf("cloud version applied to volume %s (%s: %d written, %d deleted, %d kept live)", slug, res.Resolution, written, deleted, res.KeptLive)
	data := map[string]any{"siteSlug": a.site, "volumeSlug": slug, "pushId": pushID, "versionId": pl.push.VersionID, "resolution": res.Resolution, "written": written,
		"deleted": deleted, "keptLive": res.KeptLive, "snapshotId": res.SnapshotID, "restarted": res.Restarted}
	if ff != nil {
		sum += " · " + fastForwardLabel(ff)
		data["fastForward"] = ff
	}
	a.activity(who, "agent", "volume-content.applied", trim200(sum), data)
	return p, res, nil
}

// keepLive declines a push: nothing written, a snapshot of live is uploaded so the cloud sees it.
func (a *Agent) keepLive(ctx context.Context, pl *vsPlan, actor string, ff *model.FastForwardUse) (model.VolumeContentPush, model.VolumeContentPushResult) {
	slug := pl.p.decl.VolumeSlug
	pushID := pl.push.PushID
	res := model.VolumeContentPushResult{Resolution: "keep-live", Restarted: []string{}, KeptLive: len(pl.plan.Items)}
	if rec, _, err := a.snapshotVolume(pl.p, snapOpts{trigger: "manual-agent", actor: &actor, pushID: &pushID, force: true, note: "kept live instead of cloud version " + pl.push.VersionID}); err == nil {
		res.AfterVersionID = a.uploadNow(ctx, rec)
	}
	p := a.resolvePush(slug, pl.push, "kept-live", actor, res)
	sum := fmt.Sprintf("volume %s: kept the live content instead of the cloud version", slug)
	data := map[string]any{"siteSlug": a.site, "volumeSlug": slug, "pushId": pushID, "versionId": pl.push.VersionID}
	if ff != nil {
		sum += " · " + fastForwardLabel(ff)
		data["fastForward"] = ff
	}
	a.activity(&actor, "agent", "volume-content.kept-live", trim200(sum), data)
	return p, res
}

func (a *Agent) handleKeepLiveVolumeSync(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.VolumeSyncKeepLiveRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	slug := r.PathValue("volumeSlug")
	if _, e := a.persistVolume(slug); e != nil {
		return nil, 0, e
	}
	push := a.pendingPush(slug)
	if push == nil || push.PushID != req.PushID {
		return nil, 0, invalid("this cloud version is no longer waiting (superseded, cancelled or resolved)", nil)
	}
	if e := a.contentGuard(slug); e != nil {
		return nil, 0, e
	}
	if !a.claimContent(slug, "keep live") {
		return nil, 0, herr(model.ErrConflict, "volume "+slug+" is busy", nil)
	}
	defer a.releaseContent(slug)
	ctx := a.ctx
	if ctx == nil {
		ctx = r.Context()
	}
	pl, e := a.buildPlan(ctx, slug)
	if e != nil {
		// offline: decline anyway (nothing is written); the snapshot of live waits in the queue
		p, _ := a.persistVolume(slug)
		pl = &vsPlan{p: p, push: *push}
	}
	p, _ := a.keepLive(ctx, pl, rc.actor, nil)
	return p, 200, nil
}

// ───────────────────────── converge

// converge brings the live folder to the target tree: non-excluded live files not in the target are deleted, empty
// non-target folders that hold nothing excluded are removed, changed files are written atomically (temp + rename in the
// same folder, mode, owner = the parent folder's), target folders are created. Excluded paths and .opm-seeded are never
// touched; nothing is written through a symlink.
func (a *Agent) converge(root string, ex volcontent.Excludes, w walkOut, live, target model.VolumeTree) (written, deleted int, err error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return 0, 0, err
	}
	ruid, rgid, rok := vcOwnerOf(root)
	tgt := map[string]model.VolumeTreeEntry{}
	keepDirs := map[string]bool{}
	for _, e := range target.Entries {
		if volcontent.PathError(e.Path) != "" || ex.Excluded(e.Path, false) {
			continue
		}
		tgt[e.Path] = e
		for _, d := range volcontent.AncestorDirs(e.Path) {
			keepDirs[d] = true
		}
	}
	for _, d := range target.Dirs {
		if volcontent.PathError(d) != "" || ex.Excluded(d, true) {
			continue
		}
		keepDirs[d] = true
		for _, x := range volcontent.AncestorDirs(d) {
			keepDirs[x] = true
		}
	}
	liveMap := map[string]model.VolumeTreeEntry{}
	for _, e := range live.Entries {
		liveMap[e.Path] = e
	}
	// 1. delete live files that are not in the target
	for _, f := range w.files {
		if _, ok := tgt[f.rel]; ok {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(f.rel))
		if err := noLinkOnPath(root, abs); err != nil {
			return written, deleted, err
		}
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return written, deleted, fmt.Errorf("delete %s: %w", f.rel, err)
		}
		deleted++
	}
	// 2. remove empty non-target folders (deepest first; a folder that still holds something stays)
	dirs := append([]string{}, w.dirs...)
	sort.Slice(dirs, func(i, j int) bool { return strings.Count(dirs[i], "/") > strings.Count(dirs[j], "/") })
	for _, d := range dirs {
		if keepDirs[d] {
			continue
		}
		_ = os.Remove(filepath.Join(root, filepath.FromSlash(d)))
	}
	// 3. write changed files
	paths := make([]string, 0, len(tgt))
	for p := range tgt {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		e := tgt[p]
		l, had := liveMap[p]
		abs := filepath.Join(root, filepath.FromSlash(p))
		if had && l.Sha256 == e.Sha256 {
			if l.Mode != e.Mode {
				if err := noLinkOnPath(root, abs); err != nil {
					return written, deleted, err
				}
				if err := os.Chmod(abs, parseMode(e.Mode)); err != nil {
					return written, deleted, err
				}
				written++
			}
			continue
		}
		if err := vcMkdirAll(root, filepath.Dir(abs), ruid, rgid, rok); err != nil {
			return written, deleted, err
		}
		b, err := a.contentBlobBytes(e.Sha256)
		if err != nil {
			return written, deleted, fmt.Errorf("%s: content not on this site: %w", p, err)
		}
		uid, gid, ok := vcOwnerOf(filepath.Dir(abs))
		if st, err := os.Lstat(abs); err == nil && st.IsDir() {
			return written, deleted, fmt.Errorf("%s is a folder with other content", p)
		}
		if err := vcWriteFile(root, abs, b, parseMode(e.Mode), uid, gid, ok); err != nil {
			return written, deleted, fmt.Errorf("write %s: %w", p, err)
		}
		written++
	}
	// 4. target folders
	for _, d := range target.Dirs {
		if !keepDirs[d] {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(d))
		if err := noLinkOnPath(root, filepath.Join(abs, "x")); err != nil {
			return written, deleted, err
		}
		if err := vcMkdirAll(root, abs, ruid, rgid, rok); err != nil {
			return written, deleted, err
		}
	}
	return written, deleted, nil
}

// versionLabel: "seq N" (VolumeContentPush.versionSeq), else the versionId (older clouds).
func versionLabel(p model.VolumeContentPush) string {
	if p.VersionSeq != nil {
		return fmt.Sprintf("seq %d", *p.VersionSeq)
	}
	return p.VersionID
}
