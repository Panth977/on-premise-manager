package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/store"
	"opm/agent/internal/volcontent"
)

// §21 #101 persist-to-cloud volumes (agent side): a cheap scanner (fingerprint every 60 s, auto snapshot after 120 s
// quiet, ≤ 1 auto snapshot per 900 s), snapshots (walk, skips, excludes, caps, hash, copy changed files into the blob
// store, SQLite queue volume_snapshots), the store-and-forward uploader (agentCheckBlobs / agentPutBlob /
// agentCreateVolumeSnapshot; backoff 1 → 10 min), cloud snapshot requests (once per requestId) and heartbeat reports.
// The apply of a cloud push (plan, intent, converge) is vcapply.go.

// vsState is the volume-sync state of one agent.
type vsState struct {
	mu        sync.Mutex
	vols      map[string]*vsVol
	busy      map[string]string // volumeSlug → the running content action (apply / keep-live)
	inflight  map[string]bool   // snapshotIds being uploaded
	kick      chan struct{}
	scanEvery time.Duration
	backoff   time.Duration // first upload retry delay (×2 per attempt)
	backoffMx time.Duration
}

type vsVol struct {
	snapMu      sync.Mutex // one snapshot / apply walk at a time
	cache       map[string]hcEnt
	fp          string
	fpChangedAt time.Time
	snapFp      string // the fingerprint at the last snapshot (auto: changed since)
	lastAutoAt  time.Time
	live        *vsLive
	lastErr     string
}

type hcEnt struct {
	size  int64
	mtime int64
	sha   string
}

type vsLive struct {
	tree     model.VolumeTree
	hash     string
	files    int
	bytes    int64
	excluded int
	at       time.Time
	fp       string
}

var vsInit sync.Mutex

// vsync returns the volume-sync state (created on first use).
func (a *Agent) vsync() *vsState {
	vsInit.Lock()
	defer vsInit.Unlock()
	if a.vs == nil {
		a.vs = &vsState{vols: map[string]*vsVol{}, busy: map[string]string{}, inflight: map[string]bool{}, kick: make(chan struct{}, 1),
			scanEvery: model.VolumeSyncScanSec * time.Second, backoff: time.Minute, backoffMx: 10 * time.Minute}
	}
	return a.vs
}

func (s *vsState) vol(slug string) *vsVol {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.vols[slug]
	if v == nil {
		v = &vsVol{cache: map[string]hcEnt{}}
		s.vols[slug] = v
	}
	return v
}

func (s *vsState) kickUpload() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// contentBusy: the running content action of a volume ("" = free).
func (a *Agent) contentBusy(slug string) string {
	s := a.vsync()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy[slug]
}

func (a *Agent) anyContentBusy() string {
	s := a.vsync()
	s.mu.Lock()
	defer s.mu.Unlock()
	for slug, what := range s.busy {
		return what + " of volume " + slug + " is running"
	}
	return ""
}

// startVolumeSync starts the scanner + uploader loops (Run).
func (a *Agent) startVolumeSync() {
	s := a.vsync()
	a.goLoop("volsync-scan", func(c context.Context) {
		t := time.NewTimer(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-c.Done():
				return
			case <-t.C:
			}
			a.volSyncScanOnce(c, a.clock())
			t.Reset(s.scanEvery)
		}
	})
	a.goLoop("volsync-upload", func(c context.Context) {
		t := time.NewTimer(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-c.Done():
				return
			case <-t.C:
			case <-s.kick:
			}
			next := 30 * time.Second
			if at := a.volSyncUploadOnce(c); !at.IsZero() {
				if d := time.Until(at); d < next {
					next = max(d, 50*time.Millisecond)
				}
			}
			t.Reset(next)
		}
	})
}

// ───────────────────────── persist volumes of the head

type persistVol struct {
	decl    model.SiteVolume
	persist model.VolumePersist
	root    string
	users   []string
}

func (a *Agent) persistVolumes() []persistVol {
	cfg, _ := a.headConfig()
	site := siteOf(cfg, a.site)
	if site == nil {
		return nil
	}
	refs := map[string]volRef{}
	for _, r := range siteVolumes(a.dataDir, &site.Config) {
		refs[r.Name] = r
	}
	var out []persistVol
	for _, v := range site.Config.VolumeList() {
		if v.Persist == nil || v.VolumeKind() != "dir" {
			continue
		}
		users := append([]string{}, refs[v.VolumeSlug].Containers...)
		out = append(out, persistVol{decl: v, persist: *v.Persist, root: driver.VolumePath(a.dataDir, &site.Config, v.VolumeSlug), users: users})
	}
	return out
}

func (a *Agent) persistVolume(slug string) (*persistVol, *httpError) {
	for _, p := range a.persistVolumes() {
		if p.decl.VolumeSlug == slug {
			p := p
			return &p, nil
		}
	}
	if _, e := a.volumeTarget(slug); e != nil {
		return nil, e
	}
	return nil, herr(model.ErrNotFound, "volume "+slug+" is not persisted to the cloud on site "+a.site, nil)
}

// ───────────────────────── walk + hash

type walkFile struct {
	rel   string
	abs   string
	size  int64
	mtime int64
	mode  string
}

type walkOut struct {
	files        []walkFile
	dirs         []string
	excluded     int
	skipped      []model.VolumeSkipped
	skippedCount int
	total        int64
	fp           string
}

func fileModeString(m fs.FileMode) string { return fmt.Sprintf("0%03o", m.Perm()) }

// walkVolume walks a volume folder (never following symlinks): regular files + folders that are not excluded; symlinks,
// special files, bad names and files > 24 MiB are skipped (with a reason). A missing folder = an empty tree.
func walkVolume(root string, ex volcontent.Excludes) (walkOut, error) {
	var w walkOut
	h := sha256.New()
	skip := func(rel, reason string) {
		w.skippedCount++
		if len(w.skipped) < 100 {
			w.skipped = append(w.skipped, model.VolumeSkipped{Path: rel, Reason: reason})
		}
	}
	st, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			w.fp = hex.EncodeToString(h.Sum(nil))
			return w, nil
		}
		return w, err
	}
	if !st.IsDir() {
		return w, fmt.Errorf("%s is not a folder", root)
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // unreadable entry: left out
		}
		if p == root {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		t := d.Type()
		switch {
		case d.IsDir():
			if ex.Excluded(rel, true) {
				w.excluded++
				return filepath.SkipDir
			}
			if volcontent.PathError(rel) != "" {
				skip(rel, "bad-name")
				return filepath.SkipDir
			}
			w.dirs = append(w.dirs, rel)
			fmt.Fprintf(h, "d\x00%s\n", rel)
		case t&fs.ModeSymlink != 0:
			if ex.Excluded(rel, false) {
				w.excluded++
			} else {
				skip(rel, "symlink")
			}
		case t.IsRegular():
			if ex.Excluded(rel, false) {
				w.excluded++
				return nil
			}
			if volcontent.PathError(rel) != "" {
				skip(rel, "bad-name")
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if info.Size() > model.VolumeContentSnapshotFileMaxBytes {
				skip(rel, "too-large")
				return nil
			}
			f := walkFile{rel: rel, abs: p, size: info.Size(), mtime: info.ModTime().UnixNano(), mode: fileModeString(info.Mode())}
			w.files = append(w.files, f)
			w.total += f.size
			fmt.Fprintf(h, "f\x00%s\x00%d\x00%d\x00%s\n", rel, f.size, f.mtime, f.mode)
		default:
			if ex.Excluded(rel, false) {
				w.excluded++
			} else {
				skip(rel, "special")
			}
		}
		return nil
	})
	w.fp = hex.EncodeToString(h.Sum(nil))
	return w, err
}

// hashFile reads a file once: sha256 and (when tmp != "") a copy.
func hashFile(p, tmp string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	var dst io.Writer = h
	var out *os.File
	if tmp != "" {
		if out, err = os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600); err != nil {
			return "", 0, err
		}
		dst = io.MultiWriter(h, out)
	}
	n, err := io.Copy(dst, f)
	if out != nil {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// treeOf hashes the walked files (cache by size + mtime) into a tree; store = copy every file whose blob is missing
// into the agent blob store (snapshots).
func (a *Agent) treeOf(slug string, vol *vsVol, w walkOut, store bool) (model.VolumeTree, error) {
	t := model.VolumeTree{Entries: []model.VolumeTreeEntry{}, Dirs: append([]string{}, w.dirs...)}
	seen := map[string]bool{}
	for _, f := range w.files {
		seen[f.rel] = true
		c, ok := vol.cache[f.rel]
		sha, size := c.sha, f.size
		if !ok || c.size != f.size || c.mtime != f.mtime || (store && !a.haveBlob(hexOf(c.sha))) {
			tmp := ""
			if store {
				tmp = filepath.Join(a.dataDir, "tmp", "vc-"+newID())
			}
			s, n, err := hashFile(f.abs, tmp)
			if err != nil {
				if os.IsNotExist(err) {
					continue // removed while walking
				}
				return t, fmt.Errorf("read %s: %w", f.rel, err)
			}
			sha, size = s, n
			if store {
				if a.haveBlob(hexOf(sha)) {
					_ = os.Remove(tmp)
				} else if err := a.storeBlobFile(tmp, hexOf(sha), "file", "volume:"+slug); err != nil {
					_ = os.Remove(tmp)
					return t, fmt.Errorf("store %s: %w", f.rel, err)
				}
			}
			vol.cache[f.rel] = hcEnt{size: size, mtime: f.mtime, sha: sha}
		}
		t.Entries = append(t.Entries, model.VolumeTreeEntry{Path: f.rel, Sha256: sha, Size: size, Mode: f.mode, Render: "none"})
	}
	for p := range vol.cache {
		if !seen[p] {
			delete(vol.cache, p)
		}
	}
	return volcontent.Normalize(t), nil
}

// scanLive walks + hashes the live tree of a persist volume (excludes applied) and remembers it.
func (a *Agent) scanLive(p *persistVol, now time.Time) (*vsLive, walkOut, error) {
	vol := a.vsync().vol(p.decl.VolumeSlug)
	ex := volcontent.Compile(p.persist.Exclude)
	w, err := walkVolume(p.root, ex)
	if err != nil {
		return nil, w, err
	}
	t, err := a.treeOf(p.decl.VolumeSlug, vol, w, false)
	if err != nil {
		return nil, w, err
	}
	l := &vsLive{tree: t, hash: volcontent.Hash(t), files: len(t.Entries), bytes: w.total, excluded: w.excluded, at: now, fp: w.fp}
	vol.live = l
	return l, w, nil
}

// ───────────────────────── local snapshot records (SQLite volume_snapshots)

type vsRecord struct {
	Local    model.VolumeLocalSnapshot        `json:"local"`
	Req      model.AgentVolumeSnapshotRequest `json:"req"`
	Volume   string                           `json:"volumeSlug"`
	Status   string                           `json:"status"` // queued | uploaded
	Attempts int                              `json:"attempts"`
	NextAt   int64                            `json:"nextAt"` // unix ms
}

func (a *Agent) putRecord(r *vsRecord) {
	_, err := a.db.Exec(`INSERT INTO volume_snapshots(snapshotId, volumeSlug, takenAt, status, data) VALUES(?,?,?,?,?)
		ON CONFLICT(snapshotId) DO UPDATE SET status = excluded.status, data = excluded.data`, r.Local.SnapshotID, r.Volume, r.Local.TakenAt, r.Status, store.JSON(r))
	if err != nil {
		a.log.Error("volsync", "store snapshot record failed", "error", err.Error())
	}
}

func (a *Agent) records(q string, args ...any) []*vsRecord {
	rows, err := a.db.Query(`SELECT data FROM volume_snapshots `+q, args...)
	if err != nil {
		return nil
	}
	var raws []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			raws = append(raws, s)
		}
	}
	rows.Close()
	var out []*vsRecord
	for _, s := range raws {
		var r vsRecord
		if json.Unmarshal([]byte(s), &r) == nil {
			out = append(out, &r)
		}
	}
	return out
}

func (a *Agent) lastRecord(slug string) *vsRecord {
	l := a.records(`WHERE volumeSlug = ? ORDER BY takenAt DESC, snapshotId DESC LIMIT 1`, slug)
	if len(l) == 0 {
		return nil
	}
	return l[0]
}

func (a *Agent) queuedCount(slug string) int {
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM volume_snapshots WHERE volumeSlug = ? AND status = 'queued'`, slug).Scan(&n)
	return n
}

// volSyncReferencedBlobs: blobs of queued snapshots + the last snapshot of every volume (never pruned).
func (a *Agent) volSyncReferencedBlobs(ref map[string]bool) {
	add := func(r *vsRecord) {
		for _, e := range r.Req.Tree.Entries {
			ref[hexOf(e.Sha256)] = true
		}
	}
	for _, r := range a.records(`WHERE status = 'queued'`) {
		add(r)
	}
	for _, p := range a.persistVolumes() {
		if r := a.lastRecord(p.decl.VolumeSlug); r != nil {
			add(r)
		}
	}
}

// ───────────────────────── snapshot

type snapOpts struct {
	trigger   string
	actor     *string
	note      string
	requestID *string
	pushID    *string
	force     bool // store a new snapshot even when the tree equals the last one (cloud requests, after-apply)
}

// errTooLarge carries the details of a refused snapshot.
type errTooLarge struct {
	msg     string
	details map[string]any
}

func (e *errTooLarge) Error() string { return e.msg }

// snapshotVolume takes a snapshot of a persist volume (see the file comment). created=false: the tree equals the last
// snapshot (nothing new stored; the last record is returned).
func (a *Agent) snapshotVolume(p *persistVol, o snapOpts) (*vsRecord, bool, error) {
	slug := p.decl.VolumeSlug
	vol := a.vsync().vol(slug)
	vol.snapMu.Lock()
	defer vol.snapMu.Unlock()
	rec, created, err := a.snapshotLocked(p, vol, o)
	if err != nil {
		vol.lastErr = err.Error()
		a.activity(o.actor, "agent", "volume-content.snapshot-failed", trim200(fmt.Sprintf("snapshot of volume %s failed: %s", slug, err.Error())),
			map[string]any{"siteSlug": a.site, "volumeSlug": slug, "trigger": o.trigger, "error": err.Error()})
		return nil, false, err
	}
	vol.lastErr = ""
	return rec, created, nil
}

func (a *Agent) snapshotLocked(p *persistVol, vol *vsVol, o snapOpts) (*vsRecord, bool, error) {
	slug := p.decl.VolumeSlug
	now := a.clock()
	ex := volcontent.Compile(p.persist.Exclude)
	w, err := walkVolume(p.root, ex)
	if err != nil {
		return nil, false, err
	}
	if len(w.files) > model.VolumeContentVersionMaxEntries || w.total > p.persist.MaxBytes {
		files := append([]walkFile{}, w.files...)
		sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })
		var big []map[string]any
		for i := 0; i < len(files) && i < 10; i++ {
			big = append(big, map[string]any{"path": files[i].rel, "size": files[i].size})
		}
		msg := fmt.Sprintf("the volume holds %s in %d file(s): more than the snapshot cap %s / %d files (exclude big folders or raise the cap)", humanBytes(w.total), len(w.files), humanBytes(p.persist.MaxBytes), model.VolumeContentVersionMaxEntries)
		return nil, false, &errTooLarge{msg: msg, details: map[string]any{"totalBytes": w.total, "maxBytes": p.persist.MaxBytes, "fileCount": len(w.files), "biggest": big}}
	}
	t, err := a.treeOf(slug, vol, w, true)
	if err != nil {
		return nil, false, err
	}
	hash := volcontent.Hash(t)
	vol.live = &vsLive{tree: t, hash: hash, files: len(t.Entries), bytes: w.total, excluded: w.excluded, at: now, fp: w.fp}
	if last := a.lastRecord(slug); last != nil && last.Local.TreeHash == hash && !o.force {
		vol.snapFp = w.fp
		return last, false, nil
	}
	id := newID()
	_, _, total := volcontent.Stats(t)
	rec := &vsRecord{Volume: slug, Status: "queued",
		Local: model.VolumeLocalSnapshot{SnapshotID: id, TreeHash: hash, Trigger: o.trigger, TakenAt: tsOf(now), FileCount: len(t.Entries), TotalBytes: total, Actor: o.actor},
		Req: model.AgentVolumeSnapshotRequest{SnapshotID: id, Trigger: o.trigger, TakenAt: tsOf(now), Actor: o.actor, Tree: t, TreeHash: hash,
			ExcludedCount: w.excluded, SkippedCount: w.skippedCount, Skipped: w.skipped, RequestID: o.requestID, PushID: o.pushID, Note: o.note}}
	if rec.Req.Skipped == nil {
		rec.Req.Skipped = []model.VolumeSkipped{}
	}
	a.putRecord(rec)
	vol.snapFp = w.fp
	data := map[string]any{"siteSlug": a.site, "volumeSlug": slug, "snapshotId": id, "trigger": o.trigger, "treeHash": hash, "fileCount": len(t.Entries),
		"totalBytes": total, "excludedCount": w.excluded, "skippedCount": w.skippedCount}
	if o.requestID != nil {
		data["requestId"] = *o.requestID
	}
	if o.pushID != nil {
		data["pushId"] = *o.pushID
	}
	a.activity(o.actor, "agent", "volume-content.snapshot", trim200(fmt.Sprintf("snapshot of volume %s (%s, %d file(s), %s)", slug, o.trigger, len(t.Entries), humanBytes(total))), data)
	a.vsync().kickUpload()
	return rec, true, nil
}

// ───────────────────────── scanner (auto snapshots)

// volSyncScanOnce: fingerprint every persist volume; auto snapshot when changed since the last snapshot, quiet for
// VOLUME_SYNC_QUIET_SEC and the last auto snapshot is older than VOLUME_SYNC_MIN_INTERVAL_SEC.
func (a *Agent) volSyncScanOnce(ctx context.Context, now time.Time) {
	for _, p := range a.persistVolumes() {
		if ctx.Err() != nil {
			return
		}
		p := p
		slug := p.decl.VolumeSlug
		vol := a.vsync().vol(slug)
		if a.contentBusy(slug) != "" || a.volumeBusy(slug) != "" {
			continue
		}
		vol.snapMu.Lock()
		l, _, err := a.scanLive(&p, now)
		vol.snapMu.Unlock()
		if err != nil {
			vol.lastErr = err.Error()
			continue
		}
		if vol.fp != l.fp {
			vol.fp, vol.fpChangedAt = l.fp, now
		}
		last := a.lastRecord(slug)
		if vol.snapFp == "" && last != nil && last.Local.TreeHash == l.hash {
			vol.snapFp = l.fp // after a restart: unchanged since the last snapshot
		}
		changed := last == nil || last.Local.TreeHash != l.hash
		if !changed {
			vol.snapFp = l.fp
			continue
		}
		if !p.persist.Auto || a.lock() != nil {
			continue
		}
		if now.Sub(vol.fpChangedAt) < model.VolumeSyncQuietSec*time.Second {
			continue
		}
		if !vol.lastAutoAt.IsZero() && now.Sub(vol.lastAutoAt) < model.VolumeSyncMinIntervalSec*time.Second {
			continue
		}
		vol.lastAutoAt = now
		if _, _, err := a.snapshotVolume(&p, snapOpts{trigger: "auto"}); err != nil {
			a.log.Warn("volsync", "auto snapshot failed", "volumeSlug", slug, "error", err.Error())
		}
	}
}

// ───────────────────────── uploader (store-and-forward)

// volSyncUploadOnce uploads the due queued snapshots; returns the earliest next retry (zero = none queued).
func (a *Agent) volSyncUploadOnce(ctx context.Context) (next time.Time) {
	if !a.enrolled || a.isDeleted() {
		return
	}
	nowMs := time.Now().UnixMilli()
	note := func(ms int64) {
		if t := time.UnixMilli(ms); next.IsZero() || t.Before(next) {
			next = t
		}
	}
	for _, r := range a.records(`WHERE status = 'queued' ORDER BY takenAt, snapshotId`) {
		if ctx.Err() != nil {
			return
		}
		if r.NextAt > nowMs {
			note(r.NextAt)
			continue
		}
		if err := a.uploadRecord(ctx, r); err != nil {
			if errors.Is(err, errUploadBusy) {
				continue
			}
			s := a.vsync()
			r.Attempts++
			d := s.backoff << uint(min(r.Attempts-1, 10))
			if d > s.backoffMx || d <= 0 {
				d = s.backoffMx
			}
			// 403 right after an apply: the cloud does not know the new head (persist on) until the next heartbeat →
			// report it now and retry soon (a few times)
			if ae := asAPIError(err); ae != nil && ae.Status == 403 && r.Attempts <= 6 {
				a.kickLoop("heartbeat")
				d = min(d, 5*time.Second)
			}
			r.NextAt = time.Now().Add(d).UnixMilli()
			note(r.NextAt)
			msg := err.Error()
			r.Local.Error = &msg
			a.putRecord(r)
			a.log.Info("volsync", "snapshot upload failed (will retry)", "volumeSlug", r.Volume, "snapshotId", r.Local.SnapshotID, "attempt", r.Attempts, "error", msg)
		}
	}
	return next
}

var errUploadBusy = errors.New("the snapshot is being uploaded")

// uploadRecord: missing blobs (check + PUT), then the version (idempotent by snapshotId). One upload per record at a
// time; a record uploaded meanwhile is not uploaded again.
func (a *Agent) uploadRecord(ctx context.Context, r *vsRecord) error {
	s := a.vsync()
	s.mu.Lock()
	if s.inflight[r.Local.SnapshotID] {
		s.mu.Unlock()
		return errUploadBusy
	}
	s.inflight[r.Local.SnapshotID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, r.Local.SnapshotID)
		s.mu.Unlock()
	}()
	if fresh := a.records(`WHERE snapshotId = ?`, r.Local.SnapshotID); len(fresh) == 1 {
		*r = *fresh[0]
		if r.Status == "uploaded" {
			return nil
		}
	}
	var shas []string
	seen := map[string]bool{}
	for _, e := range r.Req.Tree.Entries {
		if !seen[e.Sha256] {
			seen[e.Sha256] = true
			shas = append(shas, e.Sha256)
		}
	}
	for i := 0; i < len(shas); i += 1000 {
		j := min(i+1000, len(shas))
		var res model.AgentBlobsCheckResponse
		if err := a.cloudJSON(ctx, "POST", "/v1/agent/volume-content/blobs/check", model.AgentBlobsCheckRequest{Sha256s: shas[i:j]}, &res, callOpts{timeout: 60 * time.Second}); err != nil {
			return fmt.Errorf("check blobs: %w", err)
		}
		for _, m := range res.Missing {
			if err := a.putContentBlob(ctx, m); err != nil {
				return err
			}
		}
	}
	var res model.AgentVolumeSnapshotResponse
	err := a.cloudJSON(ctx, "POST", "/v1/agent/volume-content/"+r.Volume+"/versions", r.Req, &res, callOpts{timeout: 120 * time.Second})
	if err != nil {
		return fmt.Errorf("upload snapshot: %w", err)
	}
	now := nowTs()
	vid, seq := res.Version.VersionID, res.Version.Seq
	r.Status, r.Local.VersionID, r.Local.Seq, r.Local.UploadedAt, r.Local.Error = "uploaded", &vid, &seq, &now, nil
	a.putRecord(r)
	a.activity(r.Req.Actor, "agent", "volume-content.snapshot-uploaded", trim200(fmt.Sprintf("snapshot of volume %s uploaded as version %d", r.Volume, seq)),
		map[string]any{"siteSlug": a.site, "volumeSlug": r.Volume, "snapshotId": r.Local.SnapshotID, "versionId": vid, "seq": seq})
	return nil
}

func (a *Agent) putContentBlob(ctx context.Context, sha string) error {
	p, ok := a.blobPath(hexOf(sha))
	if !ok {
		return fmt.Errorf("blob %s is not in the local store any more", short(sha))
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	resp, err := a.cloudCall(ctx, "PUT", "/v1/agent/volume-content/blobs/"+sha, b, callOpts{timeout: 300 * time.Second, noTunnel: len(b) > 3<<20, extraHdr: map[string]string{"Content-Type": "application/octet-stream"}})
	if err != nil {
		return fmt.Errorf("upload blob %s: %w", short(sha), err)
	}
	if resp.Status >= 300 {
		ae := &apiError{Status: resp.Status}
		_ = json.Unmarshal(resp.Body, &ae.Body)
		return fmt.Errorf("upload blob %s: %w", short(sha), ae)
	}
	return nil
}

// uploadNow tries to upload one record at once (apply steps); the queue keeps it on failure.
func (a *Agent) uploadNow(ctx context.Context, r *vsRecord) *string {
	if r == nil || r.Status == "uploaded" {
		if r != nil {
			return r.Local.VersionID
		}
		return nil
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := a.uploadRecord(c, r); err != nil {
		a.log.Info("volsync", "upload deferred (store-and-forward)", "snapshotId", r.Local.SnapshotID, "error", err.Error())
		a.vsync().kickUpload()
		return nil
	}
	return r.Local.VersionID
}

// ───────────────────────── cloud view: snapshot requests + pending pushes

// pendingPush: the pending push of this site's volume from the cloud view (nil = none, or resolved here already).
func (a *Agent) pendingPush(slug string) *model.VolumeContentPush {
	v := a.cloudView()
	if v == nil {
		return nil
	}
	for _, it := range v.VolumeContent {
		if it.SiteSlug == a.site && it.VolumeSlug == slug && it.Push != nil && it.Push.Status == "pending" {
			var done map[string]any
			if ok, _ := a.db.GetKV("vc-push:"+it.Push.PushID, &done); ok {
				return nil
			}
			p := *it.Push
			return &p
		}
	}
	return nil
}

// volSyncOnView answers new cloud snapshot requests of this site (once per requestId).
func (a *Agent) volSyncOnView(v *model.CloudView) {
	for _, it := range v.VolumeContent {
		if it.SiteSlug != a.site || it.SnapshotRequest == nil {
			continue
		}
		key := "vc-request:" + it.SnapshotRequest.RequestID
		var seen string
		if ok, _ := a.db.GetKV(key, &seen); ok {
			continue
		}
		p, e := a.persistVolume(it.VolumeSlug)
		if e != nil {
			continue
		}
		_ = a.db.SetKV(key, nowTs())
		req := it.SnapshotRequest
		pv := *p
		a.goLoop("volsync-request", func(c context.Context) {
			rid := req.RequestID
			actor := req.CreatedBy
			if _, _, err := a.snapshotVolume(&pv, snapOpts{trigger: "manual-cloud", actor: &actor, requestID: &rid, force: true, note: "requested in the cloud"}); err != nil {
				a.log.Warn("volsync", "cloud snapshot request failed", "volumeSlug", pv.decl.VolumeSlug, "error", err.Error())
			}
		})
	}
}

// ───────────────────────── status + heartbeat

func (a *Agent) syncStatus(p *persistVol, rescanOlderThan time.Duration) model.VolumeSyncStatus {
	slug := p.decl.VolumeSlug
	vol := a.vsync().vol(slug)
	now := a.clock()
	if vol.live == nil || now.Sub(vol.live.at) > rescanOlderThan {
		if a.contentBusy(slug) == "" && vol.snapMu.TryLock() {
			if _, _, err := a.scanLive(p, now); err != nil {
				vol.lastErr = err.Error()
			}
			vol.snapMu.Unlock()
		}
	}
	st := model.VolumeSyncStatus{SiteSlug: a.site, VolumeSlug: slug, Label: p.decl.Label, Persist: p.persist, History: []model.VolumeLocalSnapshot{}, Users: p.users}
	if st.Label == "" {
		st.Label = slug
	}
	if st.Users == nil {
		st.Users = []string{}
	}
	for _, r := range a.records(`WHERE volumeSlug = ? ORDER BY takenAt DESC, snapshotId DESC LIMIT 20`, slug) {
		st.History = append(st.History, r.Local)
	}
	if len(st.History) > 0 {
		h := st.History[0]
		st.LastSnapshot = &h
	}
	st.Queued = a.queuedCount(slug)
	if vol.lastErr != "" {
		e := vol.lastErr
		st.LastError = &e
	}
	var liveHash *string
	if l := vol.live; l != nil {
		h, at := l.hash, tsOf(l.at)
		liveHash = &h
		st.Live = &model.VolumeSyncLive{TreeHash: &h, FileCount: l.files, TotalBytes: l.bytes, ExcludedCount: l.excluded, ScannedAt: &at, Changed: st.LastSnapshot == nil || st.LastSnapshot.TreeHash != l.hash}
	}
	st.Push = a.pendingPush(slug)
	st.State = syncState(st.Push, st.LastSnapshot, liveHash, st.LastError, st.Queued)
	if st.State == "conflict" && vol.live != nil && a.cleanMerge(p, *st.Push, vol.live.tree) {
		// live and base differ, but no file changed on both sides (e.g. the app wrote its own state file): the push
		// applies without a choice, like a fast-forward
		st.State = "cloud-ahead"
	}
	return st
}

// cleanMerge: the push and the live tree changed different files since the push's base (a per-file plan without a
// conflict). Only trees already on this agent are read (local snapshots, the blob store): no download from a status
// read; an unknown tree or an unknown base answers false (the plan decides then).
func (a *Agent) cleanMerge(p *persistVol, push model.VolumeContentPush, live model.VolumeTree) bool {
	if push.BaseTreeHash == nil {
		return false
	}
	base, ok := a.localTree(*push.BaseTreeHash)
	if !ok {
		return false
	}
	cloud, ok := a.localTree(push.TreeHash)
	if !ok {
		return false
	}
	base = volcontent.FilterExcluded(base, p.persist.Exclude)
	cloud = volcontent.FilterExcluded(cloud, p.persist.Exclude)
	return volcontent.PlanCounts(volcontent.Plan(&base, live, cloud))["conflict"] == 0
}

// localTree is fetchTree without the network: a local snapshot with that hash, else the tree blob when it is here.
func (a *Agent) localTree(treeHash string) (model.VolumeTree, bool) {
	for _, r := range a.records(`WHERE data LIKE ? LIMIT 5`, "%"+treeHash+"%") {
		if r.Local.TreeHash == treeHash {
			return r.Req.Tree, true
		}
	}
	if !a.haveBlob(hexOf(treeHash)) {
		return model.VolumeTree{}, false
	}
	b, err := a.contentBlobBytes(treeHash)
	if err != nil {
		return model.VolumeTree{}, false
	}
	var t model.VolumeTree
	if json.Unmarshal(b, &t) != nil || volcontent.Hash(t) != treeHash {
		return model.VolumeTree{}, false
	}
	return volcontent.Normalize(t), true
}

// syncState mirrors cloudSyncState with the agent's own knowledge (live tree, local snapshots).
func syncState(push *model.VolumeContentPush, last *model.VolumeLocalSnapshot, liveHash, lastErr *string, queued int) string {
	lh := ""
	if liveHash != nil {
		lh = *liveHash
	} else if last != nil {
		lh = last.TreeHash
	}
	if push != nil {
		switch {
		case push.BaseTreeHash != nil && lh == *push.BaseTreeHash:
			return "cloud-ahead"
		case lh == push.TreeHash:
			return "in-sync"
		}
		return "conflict"
	}
	if last == nil {
		return "never-snapshotted"
	}
	if lastErr != nil {
		return "error"
	}
	if queued > 0 {
		return "pending-upload"
	}
	if lh != last.TreeHash {
		return "live-changed"
	}
	return "in-sync"
}

// volSyncReports is HeartbeatRequest.volumeSync (nil when no volume is persisted).
func (a *Agent) volSyncReports() []model.VolumeSyncReport {
	var out []model.VolumeSyncReport
	for _, p := range a.persistVolumes() {
		p := p
		st := a.syncStatus(&p, 5*time.Minute)
		r := model.VolumeSyncReport{VolumeSlug: st.VolumeSlug, State: st.State, Queued: st.Queued, LastError: st.LastError}
		if st.Live != nil {
			r.LiveTreeHash, r.ScannedAt, r.LiveChanged = st.Live.TreeHash, st.Live.ScannedAt, st.Live.Changed
		}
		if st.LastSnapshot != nil {
			h := st.LastSnapshot.TreeHash
			r.SnapshotTreeHash = &h
		}
		out = append(out, r)
	}
	return out
}

// ───────────────────────── handlers: list / get / snapshot

func (a *Agent) handleListVolumeSync(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	out := []model.VolumeSyncStatus{}
	for _, p := range a.persistVolumes() {
		p := p
		out = append(out, a.syncStatus(&p, 60*time.Second))
	}
	return model.ListResponse[model.VolumeSyncStatus]{Items: out}, 200, nil
}

func (a *Agent) handleGetVolumeSync(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	p, e := a.persistVolume(r.PathValue("volumeSlug"))
	if e != nil {
		return nil, 0, e
	}
	return a.syncStatus(p, 10*time.Second), 200, nil
}

func (a *Agent) handleSnapshotVolumeSync(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.VolumeSyncSnapshotRequest
	if e := readJSONOptional(r, &req); e != nil {
		return nil, 0, e
	}
	slug := r.PathValue("volumeSlug")
	p, e := a.persistVolume(slug)
	if e != nil {
		return nil, 0, e
	}
	if why := a.contentBusy(slug); why != "" {
		return nil, 0, herr(model.ErrConflict, "volume "+slug+" is busy: "+why, nil)
	}
	if why := a.volumeBusy(slug); why != "" {
		return nil, 0, herr(model.ErrConflict, "volume "+slug+" is busy: "+why, nil)
	}
	note := ""
	if req.Note != nil {
		note = *req.Note
		if len([]rune(note)) > model.VolumeContentNoteMax {
			return nil, 0, invalid(fmt.Sprintf("the note is at most %d characters", model.VolumeContentNoteMax), nil)
		}
	}
	actor := rc.actor
	rec, created, err := a.snapshotVolume(p, snapOpts{trigger: "manual-agent", actor: &actor, note: note})
	if err != nil {
		var tl *errTooLarge
		if errors.As(err, &tl) {
			return nil, 0, herr(model.ErrTooLarge, tl.msg, tl.details)
		}
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	if created {
		a.goLoop("volsync-upload-now", func(c context.Context) { a.uploadNow(c, rec) })
	}
	return model.VolumeSyncSnapshotResponse{Snapshot: rec.Local, Created: created}, 201, nil
}

// readJSONOptional: an empty body is no error (the request type has only optional fields).
func readJSONOptional(r *http.Request, v any) *httpError {
	if e := readJSON(r, v); e != nil {
		if strings.Contains(e.msg, "EOF") {
			return nil
		}
		return e
	}
	return nil
}
