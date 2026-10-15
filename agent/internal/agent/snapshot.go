package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/diff"
	"opm/agent/internal/model"
	"opm/agent/internal/store"
)

type partInfo struct {
	snap model.Snapshot
	site model.SnapshotSite
	dir  string
}

func (a *Agent) snapDir(id string) string { return filepath.Join(a.dataDir, "snapshots", id) }

func (a *Agent) volumeArchivePath(id, name string) string {
	return filepath.Join(a.snapDir(id), "sites", a.site, "volumes", name+".tar.zst")
}

func (a *Agent) hasSnapshotPart(id string) bool {
	var c int
	if err := a.db.QueryRow(`SELECT complete FROM snapshots WHERE snapshotId = ?`, id).Scan(&c); err != nil {
		return false
	}
	return c == 1
}

func (a *Agent) loadPart(id string) (*partInfo, error) {
	var raw string
	var complete int
	if err := a.db.QueryRow(`SELECT manifest, complete FROM snapshots WHERE snapshotId = ?`, id).Scan(&raw, &complete); err != nil {
		return nil, fmt.Errorf("no part of snapshot %s on site %s", id, a.site)
	}
	if complete != 1 {
		return nil, fmt.Errorf("the part of snapshot %s on site %s is not complete", id, a.site)
	}
	var s model.Snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	site, ok := s.Sites[a.site]
	if !ok {
		site = model.SnapshotSite{Config: model.Config{}, Images: []model.SnapshotImage{}, Volumes: []model.SnapshotVolume{}}
		normalizeConfig(&site.Config)
	}
	return &partInfo{snap: s, site: site, dir: a.snapDir(id)}, nil
}

func (a *Agent) restoreVolume(id string, v model.SnapshotVolume, dir string) error {
	src := filepath.Join(a.snapDir(id), filepath.FromSlash(v.Path))
	got, err := fileSha256(src)
	if err != nil {
		return err
	}
	if "sha256:"+got != v.Sha256 {
		return fmt.Errorf("archive %s is damaged (sha256 mismatch)", v.Path)
	}
	a.log.Info("snapshot", "restoring volume", "snapshotId", id, "volumeSlug", v.VolumeSlug)
	return extractArchive(src, dir)
}

func snapImages(cfg *model.Config, cat *model.RevisionCatalog, site string) []model.SnapshotImage {
	out := []model.SnapshotImage{}
	if s := siteOf(cfg, site); s != nil && cat != nil {
		for _, c := range s.Config.Containers {
			if img, ok := cat.Images[c.ServiceSlug+":"+c.Version]; ok {
				out = append(out, model.SnapshotImage{ServiceSlug: c.ServiceSlug, Version: c.Version, Digest: img.Digest, ConfigDigest: img.ConfigDigest})
			}
		}
	}
	return out
}

// createPart takes this site's part of a snapshot. around (optional) stops a container around its own archive and
// returns the restart function (manual / scheduled snapshots).
func (a *Agent) createPart(ctx context.Context, id, reason, createdBy, note string, rev *model.Revision, cfg *model.Config, vols []volRef, around func(v volRef) func()) (*model.SnapshotSite, error) {
	a.mu.Lock()
	if a.snapBusy[id] {
		a.mu.Unlock()
		return nil, fmt.Errorf("snapshot %s is already being taken", id)
	}
	a.snapBusy[id] = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.snapBusy, id)
		a.mu.Unlock()
	}()
	var need int64
	for _, v := range vols {
		need += dirSize(v.Dir)
	}
	if free := freeBytes(a.dataDir); float64(free) < 1.2*float64(need) {
		return nil, fmt.Errorf("free space %d < 1.2 × %d", free, need)
	}
	if err := a.checkDiskFor(int64(1.2*float64(need)), "snapshot "+id); err != nil { // §21 #112 the free-space floor
		return nil, err
	}
	var cat *model.RevisionCatalog
	var revID *string
	seq := 0
	if rev != nil {
		cat = &rev.Catalog
		r := rev.RevID
		revID, seq = &r, rev.Seq
	}
	cfgCopy := *cfg
	normalizeConfig(&cfgCopy)
	now := nowTs()
	snap := model.Snapshot{SnapshotID: id, CreatedAt: now, Reason: reason, RevID: revID, Seq: seq, Sites: map[string]model.SnapshotSite{}, CreatedBy: createdBy, Note: note, Revision: rev}
	_, _ = a.db.Exec(`INSERT INTO snapshots(snapshotId, createdAt, reason, revId, seq, manifest, sizeBytes, createdBy, note, complete) VALUES(?,?,?,?,?,?,?,?,?,0)
		ON CONFLICT(snapshotId) DO UPDATE SET complete = 0, error = NULL`, id, now, reason, store.NullStr(revID), seq, store.JSON(snap), 0, createdBy, note)
	part := model.SnapshotSite{Config: cfgCopy, Images: snapImages(cfg, cat, a.site), Volumes: []model.SnapshotVolume{}}
	var total int64
	failPart := func(err error) (*model.SnapshotSite, error) {
		_, _ = a.db.Exec(`UPDATE snapshots SET error = ? WHERE snapshotId = ?`, err.Error(), id)
		return nil, err
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	for _, v := range vols {
		var restart func()
		if around != nil {
			restart = around(v)
		}
		if ctx.Err() != nil {
			if restart != nil {
				restart()
			}
			return failPart(ctx.Err())
		}
		dst := a.volumeArchivePath(id, v.Name)
		size, sum, err := archiveDir(v.Dir, dst)
		if restart != nil {
			restart()
		}
		if err != nil {
			return failPart(fmt.Errorf("archive %s: %w", v.Name, err))
		}
		rel, _ := filepath.Rel(a.snapDir(id), dst)
		cs := append([]string{}, v.Containers...)
		part.Volumes = append(part.Volumes, model.SnapshotVolume{VolumeSlug: v.Name, Containers: cs, SizeBytes: size, Sha256: "sha256:" + sum, Path: filepath.ToSlash(rel)})
		total += size
	}
	part.SizeBytes = total
	snap.Sites[a.site] = part
	if err := a.writePartFiles(id, &snap, &part); err != nil {
		return failPart(err)
	}
	_, err := a.db.Exec(`UPDATE snapshots SET manifest = ?, sizeBytes = ?, complete = 1, error = NULL WHERE snapshotId = ?`, store.JSON(snap), total, id)
	if err != nil {
		return nil, err
	}
	a.log.Info("snapshot", "part complete", "snapshotId", id, "reason", reason, "volumes", len(part.Volumes), "sizeBytes", total)
	return &part, nil
}

func (a *Agent) writePartFiles(id string, snap *model.Snapshot, part *model.SnapshotSite) error {
	dir := filepath.Join(a.snapDir(id), "sites", a.site)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	cj, err := canonical.Canonicalize(toAny(part))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cj), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(a.snapDir(id), "manifest.json"), []byte(store.JSON(snap)), 0o600)
}

func (a *Agent) snapshotIDs() []string {
	rows, err := a.db.Query(`SELECT snapshotId FROM snapshots WHERE complete = 1 ORDER BY snapshotId`)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func (a *Agent) snapshotRevisionIDs() []string {
	rows, err := a.db.Query(`SELECT DISTINCT revId FROM snapshots WHERE revId IS NOT NULL`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

type snapRow struct {
	model.Snapshot
	SizeBytes int64
	Complete  bool
	Error     *string
}

func (a *Agent) snapRows() []snapRow {
	rows, err := a.db.Query(`SELECT snapshotId, createdAt, manifest, sizeBytes, complete, error, note, createdBy, reason FROM snapshots ORDER BY snapshotId DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []snapRow
	for rows.Next() {
		var id, created, raw, note, by, reason string
		var size int64
		var c int
		var e sql.NullString
		if rows.Scan(&id, &created, &raw, &size, &c, &e, &note, &by, &reason) != nil {
			continue
		}
		var s snapRow
		_ = json.Unmarshal([]byte(raw), &s.Snapshot)
		s.SnapshotID, s.CreatedAt = id, created
		s.Note, s.CreatedBy, s.Reason = note, by, reason
		s.SizeBytes, s.Complete = size, c == 1
		if e.Valid {
			x := e.String
			s.Error = &x
		}
		out = append(out, s)
	}
	return out
}

// ownSnapshotSummaries are this site's parts (sites = [self] when complete; missingSites = other expected sites).
func (a *Agent) ownSnapshotSummaries() []model.SnapshotSummary {
	out := []model.SnapshotSummary{}
	for _, s := range a.snapRows() {
		sum := model.SnapshotSummary{SnapshotID: s.SnapshotID, CreatedAt: s.CreatedAt, Reason: s.Reason, RevID: s.RevID, Seq: s.Seq, CreatedBy: s.CreatedBy,
			Note: s.Note, SizeBytes: s.SizeBytes, Sites: []string{}, MissingSites: []string{}}
		expected := map[string]bool{a.site: true}
		if p, ok := s.Sites[a.site]; ok {
			for _, x := range p.Config.Sites {
				expected[x.SiteSlug] = true
			}
			sum.HasImages = true
			for _, im := range p.Images {
				if !a.haveBlob(hexOf(im.ConfigDigest)) {
					sum.HasImages = false
				}
			}
		}
		if s.Complete {
			sum.Sites = append(sum.Sites, a.site)
		}
		for e := range expected {
			if !s.Complete || e != a.site {
				sum.MissingSites = append(sum.MissingSites, e)
			}
		}
		sort.Strings(sum.MissingSites)
		out = append(out, sum)
	}
	return out
}

// mergedSnapshots merges the parts of every reachable site.
func (a *Agent) mergedSnapshots(ctx context.Context) ([]model.SnapshotSummary, map[string]map[string]model.SnapshotSummary) {
	per := map[string]map[string]model.SnapshotSummary{} // id → site → own summary
	add := func(site string, list []model.SnapshotSummary) {
		for _, s := range list {
			if per[s.SnapshotID] == nil {
				per[s.SnapshotID] = map[string]model.SnapshotSummary{}
			}
			per[s.SnapshotID][site] = s
		}
	}
	add(a.site, a.ownSnapshotSummaries())
	for _, p := range a.reachablePeers() {
		var lr model.ListResponse[model.SnapshotSummary]
		if err := a.peerJSON(ctx, p, "GET", "/_opm/peer/snapshots", nil, &lr, 10*time.Second); err == nil {
			add(p, lr.Items)
		}
	}
	var out []model.SnapshotSummary
	for id, sites := range per {
		var m model.SnapshotSummary
		have := map[string]bool{}
		expected := map[string]bool{}
		first := true
		m.HasImages = true
		for site, s := range sites {
			if first || site == a.site {
				m = model.SnapshotSummary{SnapshotID: id, CreatedAt: s.CreatedAt, Reason: s.Reason, RevID: s.RevID, Seq: s.Seq, CreatedBy: s.CreatedBy, Note: s.Note, HasImages: m.HasImages}
				first = false
			}
			for _, x := range s.Sites {
				have[x] = true
			}
			for _, x := range s.MissingSites {
				expected[x] = true
			}
			for _, x := range s.Sites {
				expected[x] = true
			}
			m.SizeBytes += s.SizeBytes
			if !s.HasImages {
				m.HasImages = false
			}
		}
		m.Sites, m.MissingSites = []string{}, []string{}
		for x := range have {
			m.Sites = append(m.Sites, x)
		}
		for x := range expected {
			if !have[x] {
				m.MissingSites = append(m.MissingSites, x)
			}
		}
		sort.Strings(m.Sites)
		sort.Strings(m.MissingSites)
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SnapshotID > out[j].SnapshotID })
	if out == nil {
		out = []model.SnapshotSummary{}
	}
	return out, per
}

func (a *Agent) findSnapshot(ctx context.Context, id string) (*model.SnapshotSummary, map[string]model.SnapshotSummary) {
	list, per := a.mergedSnapshots(ctx)
	for _, s := range list {
		if s.SnapshotID == id {
			ss := s
			return &ss, per[id]
		}
	}
	return nil, nil
}

// ───────────────────────── manual + scheduled snapshots

func (a *Agent) handleListSnapshots(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	list, _ := a.mergedSnapshots(r.Context())
	return model.ListResponse[model.SnapshotSummary]{Items: list}, 200, nil
}

func (a *Agent) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.CreateSnapshotRequest
	if r.ContentLength != 0 {
		if e := readJSON(r, &req); e != nil {
			return nil, 0, e
		}
	}
	if len([]rune(req.Note)) > 500 {
		return nil, 0, invalid("note: at most 500 characters", []model.ValidationIssue{{Path: "note", Code: "V_LENGTH", Message: "at most 500 characters", Severity: "error"}})
	}
	id, sites, e := a.startSnapshot(r.Context(), "manual", rc.actor, req.Note)
	if e != nil {
		return nil, 0, e
	}
	return model.CreateSnapshotResponse{SnapshotID: id, Sites: sites}, 202, nil
}

func (a *Agent) startSnapshot(ctx context.Context, reason, createdBy, note string) (string, []string, *httpError) {
	cfg, _ := a.headConfig()
	sites := siteSlugs(cfg)
	if len(sites) == 0 {
		return "", nil, herr(model.ErrGateClosed, "there is no head config yet", nil)
	}
	if bad := a.unreachable(sites); len(bad) > 0 {
		return "", nil, herr(model.ErrGateClosed, "not reachable: "+strings.Join(bad, ", "), map[string]any{"unreachable": bad})
	}
	// §21 #110 a snapshot is taken even while an update runs (the lock is advisory)
	id := newID()
	req := model.PeerSnapshotStepRequest{SnapshotID: id, Reason: reason, Note: note, CreatedBy: createdBy}
	for _, s := range sites {
		if s == a.site {
			a.goLoop("snapshot", func(c context.Context) { a.takeSitePart(c, req) })
			continue
		}
		if err := a.peerJSON(ctx, s, "POST", "/_opm/peer/snapshots", req, nil, 15*time.Second); err != nil {
			a.log.Warn("snapshot", "peer refused the snapshot step", "peer", s, "error", err.Error())
		}
	}
	return id, sites, nil
}

func (a *Agent) handlePeerSnapshotStep(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	var req model.PeerSnapshotStepRequest
	if e := readJSON(r, &req); e != nil {
		return nil, e
	}
	if req.SnapshotID == "" || (req.Reason != "manual" && req.Reason != "scheduled") {
		return nil, invalid("snapshotId and reason manual|scheduled are required", nil)
	}
	a.goLoop("snapshot", func(c context.Context) { a.takeSitePart(c, req) })
	return model.AcceptedResponse{Accepted: true}, nil
}

// takeSitePart archives the declared backup = true volumes of this site (round D; host paths and backup = false
// volumes never), the containers that mount a volume stopped around its archive.
func (a *Agent) takeSitePart(ctx context.Context, req model.PeerSnapshotStepRequest) {
	rev := a.headRevision()
	cfg, _ := a.headConfig()
	var vols []volRef
	ids := map[string]string{}
	if s := siteOf(cfg, a.site); s != nil {
		ex, _ := a.drv.Existing(ctx)
		vols = backupVolumes(a.dataDir, &s.Config)
		if req.Reason == "scheduled" { // §21 #112 a volume with its own backup policy has its own schedule
			own := explicitBackupVolumes(s)
			kept := vols[:0]
			for _, v := range vols {
				if !own[v.Name] {
					kept = append(kept, v)
				}
			}
			vols = kept
		}
		for i := range s.Config.Containers {
			c := &s.Config.Containers[i]
			if e, ok := ex[c.ContainerSlug]; ok && e.Running {
				ids[c.ContainerSlug] = e.ID
			}
		}
	}
	// every container that mounts a volume is stopped around its archive (a shared volume stops all of them)
	around := func(v volRef) func() {
		var stopped []string
		for _, cs := range v.Containers {
			if id, ok := ids[cs]; ok {
				_ = a.drv.Stop(ctx, id, -1)
				stopped = append(stopped, cs)
			}
		}
		return func() {
			for _, cs := range stopped {
				if err := a.dc.ContainerStart(context.Background(), ids[cs]); err != nil {
					a.log.Error("snapshot", "restart after archive failed", "containerSlug", cs, "error", err.Error())
				}
			}
		}
	}
	var actor *string
	if req.CreatedBy != "" && req.CreatedBy != "system" {
		actor = &req.CreatedBy
	}
	part, err := a.createPart(ctx, req.SnapshotID, req.Reason, req.CreatedBy, req.Note, rev, cfg, vols, around)
	if err != nil {
		a.log.Error("snapshot", "snapshot part failed", "snapshotId", req.SnapshotID, "error", err.Error())
		return
	}
	a.activity(actor, "agent", "snapshot.created", "snapshot "+req.SnapshotID+" ("+req.Reason+")", map[string]any{"snapshotId": req.SnapshotID, "reason": req.Reason, "sizeBytes": part.SizeBytes})
	a.retention(req.SnapshotID)
}

// nextScheduledAt returns the next daily snapshot time (hour:00 local) strictly after now.
func nextScheduledAt(now time.Time, hour int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (a *Agent) scheduleLoop(ctx context.Context) {
	for ctx.Err() == nil {
		now := a.clock()
		if !a.sleepFn(ctx, nextScheduledAt(now, a.tm.ScheduledAtHour).Sub(now)) {
			return
		}
		a.scheduledSnapshotOnce(ctx)
	}
}

// scheduledSnapshotOnce: only the first reachable head-config site (slug order) starts the scheduled snapshot (§9.1).
func (a *Agent) scheduledSnapshotOnce(ctx context.Context) (string, bool) {
	cfg, _ := a.headConfig()
	var cands []string
	for _, s := range siteSlugs(cfg) {
		if a.reachable(s) {
			cands = append(cands, s)
		}
	}
	sort.Strings(cands)
	if len(cands) == 0 || cands[0] != a.site {
		return "", false
	}
	id, _, e := a.startSnapshot(ctx, "scheduled", "system", "daily")
	if e != nil {
		a.log.Warn("snapshot", "scheduled snapshot not started", "error", e.msg)
		return "", false
	}
	a.log.Info("snapshot", "scheduled snapshot started", "snapshotId", id)
	return id, true
}

// retention: keep the 10 newest pre-update, the 7 newest scheduled, all manual + imported; never the pre-update
// snapshot of a running update.
func (a *Agent) retention(justTaken string) {
	protect := map[string]bool{justTaken: true}
	if l := a.lock(); l != nil {
		if p, ok := a.loadPlan(l.UpdateID); ok {
			protect[p.PreSnapshotID] = true
		}
	}
	for _, id := range a.activeUpdates() {
		if p, ok := a.loadPlan(id); ok {
			protect[p.PreSnapshotID] = true
		}
	}
	keep := map[string]int{"pre-update": 10, "scheduled": 7}
	count := map[string]int{}
	for _, s := range a.snapRows() { // newest first
		lim, limited := keep[s.Reason]
		if !limited {
			continue
		}
		count[s.Reason]++
		if count[s.Reason] > lim && !protect[s.SnapshotID] {
			_ = a.deleteOwnSnapshot(s.SnapshotID, nil)
			a.log.Info("snapshot", "retention removed snapshot", "snapshotId", s.SnapshotID, "reason", s.Reason)
		}
	}
	a.retentionBySize(justTaken) // §21 #112 the size budget on top of the counts
}

func (a *Agent) handleGetSnapshot(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("snapshotId")
	sum, _ := a.findSnapshot(r.Context(), id)
	if sum == nil {
		return nil, 0, herr(model.ErrNotFound, "snapshot not found", nil)
	}
	snap := a.mergedSnapshotDoc(r.Context(), id)
	return model.SnapshotDetailResponse{Summary: *sum, Snapshot: maskSnapshot(snap)}, 200, nil
}

// mergedSnapshotDoc builds the Snapshot with every reachable site's part (secrets plain).
func (a *Agent) mergedSnapshotDoc(ctx context.Context, id string) model.Snapshot {
	var out model.Snapshot
	if p, err := a.loadPartAny(id); err == nil {
		out = p.snap
	}
	out.SnapshotID = id
	if out.Sites == nil {
		out.Sites = map[string]model.SnapshotSite{}
	}
	for _, peer := range a.reachablePeers() {
		var sd model.SnapshotDetailResponse
		if err := a.fetchSiteJSON(ctx, nil, peer, "snapshot-doc/"+id, &sd); err == nil {
			for k, v := range sd.Snapshot.Sites {
				out.Sites[k] = v
			}
			if out.CreatedAt == "" {
				out = mergeSnapMeta(out, sd.Snapshot)
			}
		}
	}
	return out
}

func mergeSnapMeta(dst, src model.Snapshot) model.Snapshot {
	sites := dst.Sites
	dst = src
	if sites != nil {
		for k, v := range src.Sites {
			sites[k] = v
		}
		dst.Sites = sites
	}
	return dst
}

// loadPartAny reads the stored manifest even when incomplete.
func (a *Agent) loadPartAny(id string) (*partInfo, error) {
	var raw string
	if err := a.db.QueryRow(`SELECT manifest FROM snapshots WHERE snapshotId = ?`, id).Scan(&raw); err != nil {
		return nil, err
	}
	var s model.Snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	var note, by string
	_ = a.db.QueryRow(`SELECT note, createdBy FROM snapshots WHERE snapshotId = ?`, id).Scan(&note, &by)
	s.Note, s.CreatedBy = note, by
	return &partInfo{snap: s, site: s.Sites[a.site], dir: a.snapDir(id)}, nil
}

func maskSnapshot(s model.Snapshot) model.Snapshot {
	out := s
	out.Sites = map[string]model.SnapshotSite{}
	for k, v := range s.Sites {
		c := v.Config
		c.Secrets = maskSecrets(c.Secrets)
		v.Config = c
		out.Sites[k] = v
	}
	if s.Revision != nil {
		r := *s.Revision
		r.Config.Secrets = maskSecrets(r.Config.Secrets)
		out.Revision = &r
	}
	return out
}

func maskSecrets(in []model.SecretDef) []model.SecretDef {
	out := make([]model.SecretDef, 0, len(in))
	for _, s := range in {
		if s.Value != nil {
			m := model.Mask
			s.Value = &m
		}
		out = append(out, s)
	}
	return out
}

func (a *Agent) handlePatchSnapshot(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("snapshotId")
	var req model.PatchSnapshotRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if len([]rune(req.Note)) > 500 {
		return nil, 0, invalid("note: at most 500 characters", []model.ValidationIssue{{Path: "note", Code: "V_LENGTH", Message: "at most 500 characters", Severity: "error"}})
	}
	sum, per := a.findSnapshot(r.Context(), id)
	if sum == nil {
		return nil, 0, herr(model.ErrNotFound, "snapshot not found", nil)
	}
	for site := range per {
		if site == a.site {
			_ = a.patchOwnSnapshot(id, req.Note)
		} else {
			_ = a.peerJSON(r.Context(), site, "PATCH", "/_opm/peer/snapshots/"+id, req, nil, 10*time.Second)
		}
	}
	sum.Note = req.Note
	return sum, 200, nil
}

func (a *Agent) patchOwnSnapshot(id, note string) *httpError {
	res, err := a.db.Exec(`UPDATE snapshots SET note = ? WHERE snapshotId = ?`, note, id)
	if err != nil {
		return herr(model.ErrInternal, err.Error(), nil)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return herr(model.ErrNotFound, "snapshot not on this site", nil)
	}
	return nil
}

func (a *Agent) protectedSnapshot(id string) bool {
	for _, uid := range append(a.activeUpdates(), func() []string {
		if l := a.lock(); l != nil {
			return []string{l.UpdateID}
		}
		return nil
	}()...) {
		if p, ok := a.loadPlan(uid); ok && p.PreSnapshotID == id {
			return true
		}
	}
	return false
}

func (a *Agent) deleteOwnSnapshot(id string, rc *reqCtx) *httpError {
	if a.protectedSnapshot(id) {
		return herr(model.ErrConflict, "this is the pre-update snapshot of a running update", nil)
	}
	res, err := a.db.Exec(`DELETE FROM snapshots WHERE snapshotId = ?`, id)
	if err != nil {
		return herr(model.ErrUnavailable, "store: "+err.Error(), nil)
	}
	n, _ := res.RowsAffected()
	_ = os.RemoveAll(a.snapDir(id))
	if n == 0 {
		return herr(model.ErrNotFound, "snapshot not on this site", nil)
	}
	return nil
}

func (a *Agent) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	id := r.PathValue("snapshotId")
	sum, per := a.findSnapshot(r.Context(), id)
	if sum == nil {
		return nil, 0, herr(model.ErrNotFound, "snapshot not found", nil)
	}
	if a.protectedSnapshot(id) {
		return nil, 0, herr(model.ErrConflict, "this is the pre-update snapshot of a running update", nil)
	}
	for site := range per {
		if site == a.site {
			if e := a.deleteOwnSnapshot(id, rc); e != nil && e.code != model.ErrNotFound {
				return nil, 0, e
			}
		} else if err := a.peerJSON(r.Context(), site, "DELETE", "/_opm/peer/snapshots/"+id, nil, nil, 30*time.Second); err != nil {
			if ae := asAPIError(err); ae != nil && ae.Status == 409 {
				return nil, 0, herr(model.ErrConflict, "site "+site+": "+ae.Body.Error.Message, nil)
			}
		}
	}
	_ = os.RemoveAll(filepath.Join(a.snapDir(id), "pending"))
	a.activity(rc.actorPtr(), "agent", "snapshot.deleted", "snapshot "+id+" deleted", actorData(rc, map[string]any{"snapshotId": id}))
	return model.Deleted{Deleted: true}, 200, nil
}

// ───────────────────────── rollback to a snapshot (§14.6)

func (a *Agent) rollbackRevision(ctx context.Context, id string, actor string) (*model.Revision, *model.SnapshotSummary, *httpError) {
	sum, per := a.findSnapshot(ctx, id)
	if sum == nil {
		return nil, nil, herr(model.ErrNotFound, "snapshot not found", nil)
	}
	doc := a.mergedSnapshotDoc(ctx, id)
	if len(sum.MissingSites) > 0 {
		return nil, sum, invalid("the snapshot is not complete on: "+strings.Join(sum.MissingSites, ", "), nil)
	}
	_ = per
	var cfg *model.Config
	for _, s := range doc.Sites {
		c := s.Config
		cfg = &c
		break
	}
	if cfg == nil {
		return nil, sum, invalid("the snapshot holds no site part", nil)
	}
	normalizeConfig(cfg)
	cat := emptyCatalog()
	if doc.Revision != nil {
		cat = &doc.Revision.Catalog
	}
	head := a.headRevision()
	seq := 1
	var parent *string
	if head != nil {
		seq = head.Seq + 1
		p := head.RevID
		parent = &p
	}
	hash, _ := canonical.HashCanonical(cfg)
	chash, _ := canonical.HashCanonical(cat)
	rev := &model.Revision{RevID: newID(), Seq: seq, ParentRevID: parent, Origin: "agent", Actor: actor, CreatedAt: nowTs(), Config: *cfg, Hash: hash,
		Catalog: *cat, CatalogHash: chash, Summary: fmt.Sprintf("rollback to %s (seq %d)", id, sum.Seq), Kind: "rollback", RollbackOf: &model.RollbackRef{SnapshotID: id, Seq: sum.Seq}}
	return rev, sum, nil
}

func (a *Agent) rollbackGate(ctx context.Context, rev *model.Revision, sum *model.SnapshotSummary, per map[string]model.SnapshotSummary) model.GateCheck {
	sites := a.gateSites(&rev.Config)
	g := model.GateCheck{Sites: sites, Conditions: []model.GateCondition{{Key: "parent", Ok: true, Detail: "a rollback has no parent condition"}}}
	g.Conditions = append(g.Conditions, cond("reachable", a.unreachable(sites), "every site is enrolled and answered in the last 30 s", "not reachable / not installed: "))
	var bad []string
	for _, s := range siteSlugs(&rev.Config) {
		ps, ok := per[s]
		if !ok || !contains(ps.Sites, s) {
			bad = append(bad, s+" (snapshot part missing)")
		} else if !ps.HasImages {
			bad = append(bad, s+" (images missing)")
		}
	}
	g.Conditions = append(g.Conditions, cond("staged", bad, "the snapshot is complete on every site and its images exist there", "not ready: "))
	g.Conditions = append(g.Conditions, idleWarn(a.lockedSites(sites))) // §21 #110 warns, never closes
	g.Conditions = append(g.Conditions, model.GateCondition{Key: "status", Ok: true, Detail: "snapshot " + sum.SnapshotID})
	g.Open = true
	for _, c := range g.Conditions {
		if !c.Ok {
			g.Open = false
		}
	}
	return g
}

func (a *Agent) handleRollbackPlan(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("snapshotId")
	rev, sum, e := a.rollbackRevision(r.Context(), id, getRC(r).actor)
	if e != nil && sum == nil {
		return nil, 0, e
	}
	_, per := a.findSnapshot(r.Context(), id)
	headCfg, headCat := a.headConfig()
	out := model.RollbackPlanResponse{Snapshot: *sum, Images: []model.RollbackImageChange{}, Changes: []model.DiffChange{}, VolumesRestored: []model.RollbackVolume{}, VolumesKept: []model.KeptVolume{}, DataLossSince: sum.CreatedAt, NewSeq: 1}
	if h := a.head(); h != nil {
		out.NewSeq = h.Seq + 1
	}
	if rev == nil {
		out.Gate = model.GateCheck{Open: false, Conditions: []model.GateCondition{{Key: "staged", Ok: false, Detail: e.msg}}, Sites: []string{}}
		return out, 200, nil
	}
	slugs := svcSlugs(&rev.Catalog)
	for k, v := range svcSlugs(headCat) {
		slugs[k] = v
	}
	out.Changes = diff.Configs(headCfg, revConfig(rev), func(id string) string {
		if s, ok := slugs[id]; ok {
			return s
		}
		return id
	})
	doc := a.mergedSnapshotDoc(r.Context(), id)
	restored := map[string]bool{}
	var siteKeys []string
	for site := range doc.Sites {
		siteKeys = append(siteKeys, site)
	}
	sort.Strings(siteKeys)
	for _, site := range siteKeys {
		for _, v := range doc.Sites[site].Volumes {
			restored[site+"/"+v.VolumeSlug] = true
			out.VolumesRestored = append(out.VolumesRestored, model.RollbackVolume{SiteSlug: site, VolumeSlug: v.VolumeSlug, SizeBytes: v.SizeBytes})
		}
	}
	// declared volumes of the current head that the snapshot does not hold stay untouched
	for _, hs := range headCfg.Sites {
		for _, v := range siteVolumes(a.dataDir, &hs.Config) {
			if !restored[hs.SiteSlug+"/"+v.Name] {
				out.VolumesKept = append(out.VolumesKept, model.KeptVolume{SiteSlug: hs.SiteSlug, VolumeSlug: v.Name, Backup: v.Backup})
			}
		}
	}
	for _, s := range revConfig(rev).Sites {
		for i := range s.Config.Containers {
			c := &s.Config.Containers[i]
			var from, to *string
			if img, ok := rev.Catalog.Images[c.ServiceSlug+":"+c.Version]; ok {
				t := img.ServiceSlug + ":" + img.Version
				to = &t
			}
			if hs := siteOf(headCfg, s.SiteSlug); hs != nil {
				for _, hc := range hs.Config.Containers {
					if hc.ContainerSlug == c.ContainerSlug {
						f := hc.ServiceSlug + ":" + hc.Version
						from = &f
					}
				}
			}
			if deref(from) != deref(to) {
				out.Images = append(out.Images, model.RollbackImageChange{SiteSlug: s.SiteSlug, ContainerSlug: c.ContainerSlug, ServiceSlug: c.ServiceSlug, From: from, To: to})
			}
		}
	}
	out.Gate = a.rollbackGate(r.Context(), rev, sum, per)
	return out, 200, nil
}

func (a *Agent) handleRollbackStart(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("snapshotId")
	rev, sum, e := a.rollbackRevision(r.Context(), id, getRC(r).actor)
	if e != nil {
		return nil, 0, e
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	_, per := a.findSnapshot(r.Context(), id)
	g := a.rollbackGate(r.Context(), rev, sum, per)
	if !g.Open {
		return nil, 0, herr(model.ErrGateClosed, "the gate is closed", map[string]any{"gate": g})
	}
	in := a.createIntent("rollback", nil, &id, getRC(r))
	return model.ApproveStartResponse{Intent: in, Gate: g}, 201, nil
}

func (a *Agent) handleRollback(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	id := r.PathValue("snapshotId")
	var req model.ApproveRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if a.branchMode() { // §21 #104: a rollback is a commit — message required
		if e := checkMsg(req.Message); e != nil {
			return nil, 0, e
		}
	}
	if e := a.checkIntent(req.IntentID, "rollback", id, rc, req.Typed); e != nil {
		return nil, 0, e
	}
	rev, sum, e := a.rollbackRevision(r.Context(), id, rc.actor)
	if e != nil {
		return nil, 0, e
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	_, per := a.findSnapshot(r.Context(), id)
	if g := a.rollbackGate(r.Context(), rev, sum, per); !g.Open {
		return nil, 0, herr(model.ErrGateClosed, "the gate closed again", map[string]any{"gate": g})
	}
	rc.ff = a.markIntentUsed(req.IntentID, rc.actor)
	if a.branchMode() {
		run, he := a.branchRollback(r.Context(), id, rev, sum, req.Message, rc)
		if he != nil {
			return nil, 0, he
		}
		a.activity(rc.actorPtr(), "agent", "update.started", "rollback to snapshot "+id+" started", actorData(rc, map[string]any{"updateId": run.UpdateID, "snapshotId": id, "kind": "rollback"}))
		return model.ApproveResponse{UpdateID: run.UpdateID, Run: *run}, 201, nil
	}
	if err := a.putRevision(rev); err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	run, he := a.startUpdate("rollback", rev, nil, &id, rc)
	if he != nil {
		return nil, 0, he
	}
	return model.ApproveResponse{UpdateID: run.UpdateID, Run: *run}, 201, nil
}
