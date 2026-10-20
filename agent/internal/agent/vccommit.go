package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/volcontent"
)

// §21 #117 persist-to-cloud volume content is COMMITTED content: the seed tree of a persist volume. Every apply whose
// target tree differs from the head's converges the live folder (fetch on merge); the live tree differing from the head
// is drift (uncommitted content items, never blocking); a merge takes "ours" = the live tree, so files both sides
// changed are conflicts; the bytes travel with the agent-branch push. There is no Send / Apply of cloud versions.

// persistTreeOf is the committed content of a persist volume (seed null = empty).
func persistTreeOf(v *model.SiteVolume) model.VolumeTree {
	if sd := v.SeedOf(); sd != nil && sd.Kind == "tree" {
		return volcontent.Normalize(sd.TreeOf())
	}
	return volcontent.Normalize(model.VolumeTree{Entries: []model.VolumeTreeEntry{}, Dirs: []string{}})
}

// committedTree: the tree of a persist volume in cfg (this site), the excludes applied; ok=false = no such persist volume.
func (a *Agent) committedTree(cfg *model.Config, slug string) (model.VolumeTree, bool) {
	s := siteOf(cfg, a.site)
	if s == nil {
		return model.VolumeTree{}, false
	}
	v := s.Config.FindVolume(slug)
	if v == nil || v.Persist == nil || v.VolumeKind() != "dir" {
		return model.VolumeTree{}, false
	}
	return volcontent.FilterExcluded(persistTreeOf(v), v.Persist.Exclude), true
}

// isPersistContentKey: a merge / drift key of the content of a persist volume of this site (never blocks, §21 #117 c).
func (a *Agent) isPersistContentKey(key string) bool {
	pre := "sites/" + a.site + "/volumes/"
	if !strings.HasPrefix(key, pre) {
		return false
	}
	rest := strings.TrimPrefix(key, pre)
	i := strings.Index(rest, "/content")
	if i <= 0 {
		return false
	}
	slug := rest[:i]
	for _, p := range a.persistVolumes() {
		if p.decl.VolumeSlug == slug {
			return true
		}
	}
	return false
}

// captureLive walks + hashes the live tree of a persist volume and keeps the bytes of every file in the blob store
// (the drift diffs, the drift commit and its push need them). Over the persisted limits → errTooLarge.
func (a *Agent) captureLive(p *persistVol) (*vsLive, walkOut, error) {
	vol := a.vsync().vol(p.decl.VolumeSlug)
	vol.snapMu.Lock()
	defer vol.snapMu.Unlock()
	ex := volcontent.Compile(p.persist.Exclude)
	w, err := walkVolume(p.root, ex)
	if err != nil {
		return nil, w, err
	}
	if len(w.files) > model.VolumeContentVersionMaxEntries || w.total > p.persist.MaxBytes {
		return nil, w, &errTooLarge{msg: fmt.Sprintf("volume %s holds %s in %d file(s): more than the persisted content allows (%s / %d files) — exclude big folders or raise the size cap",
			p.decl.VolumeSlug, humanBytes(w.total), len(w.files), humanBytes(p.persist.MaxBytes), model.VolumeContentVersionMaxEntries)}
	}
	t, err := a.treeOf(p.decl.VolumeSlug, vol, w, true)
	if err != nil {
		return nil, w, err
	}
	l := &vsLive{tree: t, hash: volcontent.Hash(t), files: len(t.Entries), bytes: w.total, excluded: w.excluded, at: a.clock(), fp: w.fp}
	vol.live, vol.stored = l, l.hash
	return l, w, nil
}

// liveContentConfig returns a copy of cfg whose persist volumes of this site hold the LIVE tree where it differs from
// cfg (the server's side for drift and merges) + the live tree hash of every persist volume (merge-run guard).
func (a *Agent) liveContentConfig(cfg *model.Config) (*model.Config, map[string]string) {
	out := cloneConfig(cfg)
	hashes := map[string]string{}
	s := siteOf(out, a.site)
	if s == nil {
		return out, hashes
	}
	for _, p := range a.persistVolumes() {
		p := p
		slug := p.decl.VolumeSlug
		if a.contentBusy(slug) != "" || a.volumeBusy(slug) != "" {
			continue
		}
		v := s.Config.FindVolume(slug)
		if v == nil || v.Persist == nil {
			continue
		}
		l, _, err := a.captureLive(&p)
		if err != nil {
			// not expressible (over the limits / unreadable): shown on Docker › Volumes, never blocks a merge
			a.vsync().vol(slug).lastErr = err.Error()
			continue
		}
		hashes[slug] = l.hash
		committed := volcontent.FilterExcluded(persistTreeOf(v), v.Persist.Exclude)
		if volcontent.Hash(committed) == l.hash {
			continue
		}
		t := l.tree
		v.Seed = model.Some(model.VolumeSeed{Kind: "tree", Tree: append([]model.VolumeTreeEntry{}, t.Entries...), Dirs: append([]string{}, t.Dirs...)})
	}
	return out, hashes
}

func cloneConfig(cfg *model.Config) *model.Config {
	b, _ := json.Marshal(cfg)
	var out model.Config
	if err := json.Unmarshal(b, &out); err != nil {
		c := *cfg
		return &c
	}
	return &out
}

// ───────────────────────── apply (fetch on merge)

// contentApply is what an apply of a revision may / must do with persist volumes (kv vc-apply:<revId>):
// Live = the live tree hash the person reviewed (merge); Force = converge even when the head already had this tree
// (discard of content changes).
type contentApply struct {
	Live  map[string]string `json:"live,omitempty"`
	Force map[string]bool   `json:"force,omitempty"`
}

func (a *Agent) setContentApply(revID string, c contentApply) {
	_ = a.db.SetKV("vc-apply:"+revID, c)
}

// contentApplyOf reads (and removes: one apply uses it) the instructions of revID.
func (a *Agent) contentApplyOf(revID string) contentApply {
	var c contentApply
	if ok, _ := a.db.GetKV("vc-apply:"+revID, &c); ok {
		_, _ = a.db.Exec(`DELETE FROM kv WHERE key = ?`, "vc-apply:"+revID)
	}
	return c
}

// contentChange is one persist volume an apply writes.
type contentChange struct {
	p      persistVol
	target model.VolumeTree
	live   *vsLive
}

// contentChanges decides (before anything is stopped) which persist volumes this apply converges and checks that no
// server change would be overwritten: live must be what the person reviewed (merge) or the head's tree; rollbacks and
// discards converge unguarded. Restored volumes are skipped (the snapshot brings them back).
func (a *Agent) contentChanges(o applyOpts, cfg *model.Config, site *model.Site, restored map[string]bool) ([]contentChange, error) {
	if site == nil || o.rev == nil {
		return nil, nil
	}
	var prev *model.Config
	if h := a.headRevision(); h != nil {
		prev = revConfig(h)
	}
	ca := a.contentApplyOf(o.rev.RevID)
	refs := map[string]volRef{}
	for _, r := range siteVolumes(a.dataDir, &site.Config) {
		refs[r.Name] = r
	}
	var out []contentChange
	for _, v := range site.Config.VolumeList() {
		v := v
		if v.Persist == nil || v.VolumeKind() != "dir" || restored[v.VolumeSlug] {
			continue
		}
		slug := v.VolumeSlug
		target := volcontent.FilterExcluded(persistTreeOf(&v), v.Persist.Exclude)
		th := volcontent.Hash(target)
		var prevHash string
		hadPrev := false
		if prev != nil {
			if pt, ok := a.committedTree(prev, slug); ok {
				prevHash, hadPrev = volcontent.Hash(pt), true
			}
		}
		force := o.rollback || ca.Force[slug]
		if !force && hadPrev && prevHash == th {
			continue // the content did not change: the live folder is left alone (server changes stay uncommitted)
		}
		p := persistVol{decl: v, persist: *v.Persist, root: driver.VolumePath(a.dataDir, &site.Config, slug), users: append([]string{}, refs[slug].Containers...)}
		vol := a.vsync().vol(slug)
		vol.snapMu.Lock()
		l, _, err := a.scanLive(&p, a.clock())
		vol.snapMu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("volume %s: read the live content: %w", slug, err)
		}
		if l.hash == th {
			continue
		}
		if !force && len(l.tree.Entries) > 0 {
			expect, reviewed := ca.Live[slug]
			if !reviewed {
				expect = prevHash
			}
			if l.hash != expect {
				if reviewed {
					return nil, fmt.Errorf("volume %s changed on the server since the merge was reviewed — review the merge again", slug)
				}
				return nil, fmt.Errorf("volume %s has server changes that are not committed — commit or discard them first", slug)
			}
		}
		out = append(out, contentChange{p: p, target: target, live: l})
	}
	return out, nil
}

// applyContent converges the live folders (apply step 2b, the users are stopped).
func (a *Agent) applyContent(ctx context.Context, changes []contentChange, actor *string) error {
	for _, c := range changes {
		slug := c.p.decl.VolumeSlug
		for _, e := range c.target.Entries {
			if a.haveBlob(hexOf(e.Sha256)) {
				continue
			}
			if err := a.ensureContentBlob(ctx, slug, e); err != nil {
				return fmt.Errorf("volume %s: %s: %w", slug, e.Path, err)
			}
		}
		vol := a.vsync().vol(slug)
		vol.snapMu.Lock()
		ex := volcontent.Compile(c.p.persist.Exclude)
		w, err := walkVolume(c.p.root, ex)
		var live model.VolumeTree
		if err == nil {
			live, err = a.treeOf(slug, vol, w, false)
		}
		if err == nil && volcontent.Hash(live) != c.live.hash {
			err = fmt.Errorf("it changed on the server during the update")
		}
		var written, deleted int
		if err == nil {
			written, deleted, err = a.converge(c.p.root, ex, w, live, c.target)
		}
		vol.live = nil
		vol.snapMu.Unlock()
		a.forgetVolumeSizes()
		if err != nil {
			return fmt.Errorf("volume %s: write the committed content: %w", slug, err)
		}
		a.activity(actor, "agent", "volume-content.applied", trim200(fmt.Sprintf("volume %s: committed content written (%d written, %d deleted)", slug, written, deleted)),
			map[string]any{"siteSlug": a.site, "volumeSlug": slug, "treeHash": volcontent.Hash(c.target), "written": written, "deleted": deleted})
		a.log.Info("apply", "volume content written", "volumeSlug", slug, "written", written, "deleted", deleted)
	}
	return nil
}

// contentDriftConflict: 409 uncommitted-drift when target changes the content of a persist volume that has server
// changes not committed (revert / reset / fast-forward / commit of GUI edits, §21 #117 d).
func (a *Agent) contentDriftConflict(target *model.Config) *httpError {
	head, _ := a.headRaw()
	var bad []string
	for _, p := range a.persistVolumes() {
		p := p
		slug := p.decl.VolumeSlug
		ht, _ := a.committedTree(head, slug)
		tt, ok := a.committedTree(target, slug)
		if !ok || volcontent.Hash(ht) == volcontent.Hash(tt) {
			continue
		}
		vol := a.vsync().vol(slug)
		vol.snapMu.Lock()
		l, _, err := a.scanLive(&p, a.clock())
		vol.snapMu.Unlock()
		if err != nil || (l.hash != volcontent.Hash(ht) && len(l.tree.Entries) > 0) {
			bad = append(bad, slug)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return herr(model.ErrConflict, "volume "+strings.Join(bad, ", ")+" has server changes that are not committed — commit or discard them first",
		map[string]any{"reason": model.ReasonUncommittedDrift, "volumes": bad})
}

// ───────────────────────── site → cloud: the bytes of pushed commits

// commitBlobs: the File and volume-content blobs of this site's part of a commit config.
func (a *Agent) commitBlobs(cfg *model.Config) map[string]int64 {
	out := map[string]int64{}
	for _, f := range cfg.Files {
		if f.Content != nil {
			out[f.Content.Sha256] = f.Content.SizeBytes
		}
	}
	if s := siteOf(cfg, a.site); s != nil {
		for _, v := range s.Config.VolumeList() {
			sd := v.SeedOf()
			if sd == nil {
				continue
			}
			if sd.Kind == "tree" {
				for _, e := range sd.Tree {
					out[e.Sha256] = e.Size
				}
			}
			if sd.Kind == "single" {
				out[sd.Sha256] = sd.Size
			}
		}
	}
	return out
}

// uploadPushBlobs uploads the blobs of an agent-branch push the cloud lacks (outbox, before the push itself).
func (a *Agent) uploadPushBlobs(ctx context.Context, body []byte) error {
	var req model.PushAgentBranchRequest
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	want := map[string]int64{}
	for i := range req.Commits {
		for sha, n := range a.commitBlobs(&req.Commits[i].Config) {
			if a.haveBlob(hexOf(sha)) && n <= model.VolumeContentSnapshotFileMaxBytes {
				want[sha] = n
			}
		}
	}
	if len(want) == 0 {
		return nil
	}
	list := make([]string, 0, len(want))
	for sha := range want {
		list = append(list, sha)
	}
	sort.Strings(list)
	for len(list) > 0 {
		n := min(len(list), 1000)
		var resp model.AgentBlobsCheckResponse
		if err := a.cloudJSON(ctx, "POST", "/v1/agent/volume-content/blobs/check", model.AgentBlobsCheckRequest{Sha256s: list[:n]}, &resp, callOpts{timeout: 60 * time.Second}); err != nil {
			return fmt.Errorf("check blobs: %w", err)
		}
		for _, sha := range resp.Missing {
			if err := a.putContentBlob(ctx, sha); err != nil {
				if ae := asAPIError(err); ae != nil && ae.Status >= 400 && ae.Status < 500 && ae.Status != 401 && ae.Status != 429 {
					a.log.Warn("cloudlink", "blob of a pushed commit refused by the cloud (push goes on)", "sha256", short(sha), "status", ae.Status)
					continue
				}
				return err
			}
		}
		list = list[n:]
	}
	return nil
}

// ───────────────────────── removed (§21 #117 f)

func (a *Agent) goneVolumeApply(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return nil, 0, herr(model.ErrGone, "persist-to-cloud content is committed now: merge the cloud commit on the Branch page (server changes: commit them there)", nil)
}
