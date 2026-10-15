package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// Round E: site volume kind (dir | file) and starter content (seed).
//
// A seed is applied at APPLY, before the containers start, only when the volume is new or empty (a directory without
// entries / a file of size 0) and was never seeded on this site; then it is marked seeded (marker .opm-seeded in a dir
// volume + kv "volume-seeded:<volumeSlug>") so it never seeds again. A later revision with another seed never touches
// existing data. "Reset to starter content" (resetVolumeStart / resetVolume) re-applies it on purpose after a manual
// snapshot of the volume.

// SeedMarker is the file written into a seeded dir volume.
const SeedMarker = ".opm-seeded"

// SeedArchiveUncompressedMax is the cap of the extracted size of a seed archive.
const SeedArchiveUncompressedMax = int64(2) << 30

func seededKey(slug string) string { return "volume-seeded:" + slug }

// seededInfo is SiteVolumeStatus.seeded (nil = never seeded on this site).
func (a *Agent) seededInfo(slug string) *model.VolumeSeeded {
	var s model.VolumeSeeded
	if ok, _ := a.db.GetKV(seededKey(slug), &s); ok && s.At != "" {
		return &s
	}
	return nil
}

// volumeEmpty: the location does not exist, is a directory without entries, or a file of size 0.
func volumeEmpty(p string) bool {
	st, err := os.Stat(p)
	if err != nil {
		return true
	}
	if st.IsDir() {
		es, _ := os.ReadDir(p)
		return len(es) == 0
	}
	return st.Size() == 0
}

// seedFileContent is the content of a seed file: the blob bytes, rendered once per site when render is expressions.
func (a *Agent) seedFileContent(cfg *model.Config, cat *model.RevisionCatalog, fileSlug string) ([]byte, error) {
	var f *model.FileDef
	for i := range cfg.Files {
		if cfg.Files[i].FileSlug == fileSlug {
			f = &cfg.Files[i]
		}
	}
	if f == nil || f.Content == nil {
		return nil, fmt.Errorf("seed file %s has no content", fileSlug)
	}
	p, ok := a.blobPath(hexOf(f.Content.Sha256))
	if !ok {
		p = driver.FileBlobPath(a.dataDir, hexOf(f.Content.Sha256))
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("seed file %s: %w", fileSlug, err)
	}
	if f.RenderMode() != "expressions" {
		return raw, nil
	}
	vctx := a.validationContext(cat)
	sm := driver.SiteMapFor(cfg, cat, a.dep, a.siteHosts(cfg))
	fctx := validate.SiteFileExprContext(cfg, vctx.Services, vctx.Images, a.dep, nil, a.site, sm)
	out, err := driver.RenderSiteFileFor(f, string(raw), fctx, a.site)
	if err != nil {
		return nil, err
	}
	return []byte(out.Value), nil
}

// seedBlobs lists the blobs the seeds of a site need (files of files / file seeds, archive blobs).
func seedBlobs(cfg *model.Config, site *model.Site, revID string) []blobNeed {
	var out []blobNeed
	fileBlob := func(slug string) {
		for _, f := range cfg.Files {
			if f.FileSlug == slug && f.Content != nil {
				out = append(out, blobNeed{Hex: hexOf(f.Content.Sha256), Size: f.Content.SizeBytes, Kind: "file", Ref: model.FetchRef{FileSlug: f.FileSlug, RevID: revID}})
			}
		}
	}
	for _, v := range site.Config.VolumeList() {
		sd := v.SeedOf()
		if sd == nil {
			continue
		}
		switch sd.Kind {
		case "files":
			for _, e := range sd.Entries {
				fileBlob(e.FileSlug)
			}
		case "file":
			fileBlob(sd.FileSlug)
		case "archive":
			out = append(out, blobNeed{Hex: hexOf(sd.BlobSha256), Size: sd.Size, Kind: "file", Ref: model.FetchRef{FileSlug: "seed-" + v.VolumeSlug, RevID: revID}})
		case "tree", "single": // §21 #101 the volume's own content (vcseed.go)
			out = append(out, contentSeedBlobs(v, revID)...)
		}
	}
	return out
}

// seedFileSlugs are the files the seeds of a site use with render expressions (rendered at staging for validation).
func seedFileSlugs(site *model.Site) []string {
	var out []string
	for _, v := range site.Config.VolumeList() {
		sd := v.SeedOf()
		if sd == nil {
			continue
		}
		if sd.Kind == "file" {
			out = append(out, sd.FileSlug)
		}
		for _, e := range sd.Entries {
			out = append(out, e.FileSlug)
		}
	}
	return out
}

// writeSeed writes the starter content of v into its (empty) location. Returns the entries written.
func (a *Agent) writeSeed(cfg *model.Config, cat *model.RevisionCatalog, sc *model.SiteConfig, v *model.SiteVolume) (int, error) {
	sd := v.SeedOf()
	p := driver.VolumePath(a.dataDir, sc, v.VolumeSlug)
	switch sd.Kind {
	case "tree", "single": // §21 #101 (vcseed.go)
		return a.writeContentSeed(cfg, cat, sc, v)
	case "file":
		b, err := a.seedFileContent(cfg, cat, sd.FileSlug)
		if err != nil {
			return 0, err
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return 0, fmt.Errorf("volume %s is a single file but %s is a directory", v.VolumeSlug, p)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return 0, err
		}
		// in place (O_TRUNC): a bind-mounted file keeps its inode
		return 1, os.WriteFile(p, b, 0o644)
	case "files":
		if err := os.MkdirAll(p, 0o755); err != nil {
			return 0, err
		}
		n := 0
		for _, e := range sd.Entries {
			if pe := validate.SeedPathError(e.Path); pe != "" {
				return n, fmt.Errorf("seed path %s: %s", e.Path, pe)
			}
			b, err := a.seedFileContent(cfg, cat, e.FileSlug)
			if err != nil {
				return n, err
			}
			dst := filepath.Join(p, filepath.FromSlash(e.Path))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return n, err
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return n, err
			}
			n++
		}
		return n, nil
	case "archive":
		src, ok := a.blobPath(hexOf(sd.BlobSha256))
		if !ok {
			src = driver.FileBlobPath(a.dataDir, hexOf(sd.BlobSha256))
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return 0, err
		}
		return extractSeedArchive(src, p, SeedArchiveUncompressedMax)
	}
	return 0, fmt.Errorf("volume %s: unknown seed kind %q", v.VolumeSlug, sd.Kind)
}

// ErrUnsafeArchive is returned for archive entries the seed extraction refuses.
var ErrUnsafeArchive = errors.New("unsafe seed archive")

// extractSeedArchive extracts a .tar.gz into dir: regular files and directories only, relative paths without "..",
// no absolute paths, no symlinks / hard links / devices; the uncompressed total is capped. Returns the regular files.
func extractSeedArchive(src, dir string, max int64) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("seed archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	root, _ := filepath.Abs(dir)
	var total int64
	n := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, fmt.Errorf("seed archive: %w", err)
		}
		name := hdr.Name
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
			return n, fmt.Errorf("%w: absolute path %s", ErrUnsafeArchive, name)
		}
		for _, seg := range strings.Split(name, "/") {
			if seg == ".." {
				return n, fmt.Errorf("%w: %s contains ..", ErrUnsafeArchive, name)
			}
		}
		clean := path.Clean(name)
		if clean == "." {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(clean))
		if !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return n, fmt.Errorf("%w: %s escapes the volume", ErrUnsafeArchive, name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return n, err
			}
		case tar.TypeReg, tar.TypeRegA:
			total += hdr.Size
			if total > max {
				return n, fmt.Errorf("%w: more than %d bytes uncompressed", ErrUnsafeArchive, max)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return n, err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return n, err
			}
			_, err = io.Copy(out, io.LimitReader(tr, hdr.Size))
			out.Close()
			if err != nil {
				return n, err
			}
			n++
		case tar.TypeXGlobalHeader:
			continue
		default:
			return n, fmt.Errorf("%w: %s is not a regular file or directory (links and devices are refused)", ErrUnsafeArchive, name)
		}
	}
}

// seedVolume seeds one volume when it needs it (or always with force) and records it.
func (a *Agent) seedVolume(cfg *model.Config, cat *model.RevisionCatalog, sc *model.SiteConfig, v *model.SiteVolume, force bool, actor *string) (bool, int, error) {
	sd := v.SeedOf()
	if sd == nil {
		return false, 0, nil
	}
	p := driver.VolumePath(a.dataDir, sc, v.VolumeSlug)
	if !force && (a.seededInfo(v.VolumeSlug) != nil || !volumeEmpty(p)) {
		return false, 0, nil
	}
	n, err := a.writeSeed(cfg, cat, sc, v)
	if err != nil {
		return false, n, fmt.Errorf("seed volume %s: %w", v.VolumeSlug, err)
	}
	info := model.VolumeSeeded{At: nowTs(), Source: sd.Kind, Entries: n}
	if v.VolumeKind() == "dir" {
		_ = os.WriteFile(filepath.Join(p, SeedMarker), []byte(fmt.Sprintf(`{"at":%q,"source":%q,"entries":%d}`+"\n", info.At, info.Source, n)), 0o644)
	}
	_ = a.db.SetKV(seededKey(v.VolumeSlug), info)
	a.forgetVolumeSizes()
	a.activity(actor, "agent", "volume.seeded", fmt.Sprintf("volume %s seeded from %s (%d entries)", v.VolumeSlug, sd.Kind, n),
		map[string]any{"siteSlug": a.site, "volumeSlug": v.VolumeSlug, "source": sd.Kind, "entries": n})
	a.log.Info("apply", "volume seeded", "volumeSlug", v.VolumeSlug, "source", sd.Kind, "entries", n)
	return true, n, nil
}

// seedVolumes seeds the new / empty volumes of this site at APPLY (before containers start). skip = volumes restored
// from a snapshot in this apply.
func (a *Agent) seedVolumes(cfg *model.Config, cat *model.RevisionCatalog, site *model.Site, skip map[string]bool) error {
	for _, v := range site.Config.VolumeList() {
		if skip[v.VolumeSlug] {
			continue
		}
		v := v
		if _, _, err := a.seedVolume(cfg, cat, &site.Config, &v, false, nil); err != nil {
			return err
		}
	}
	return nil
}

// checkVolumeKinds: a volume whose kind changes while its location holds data fails STAGING; so does a location whose
// type (directory / file) does not match the kind.
func (a *Agent) checkVolumeKinds(head, next *model.Config) error {
	ns := siteOf(next, a.site)
	if ns == nil {
		return nil
	}
	hs := siteOf(head, a.site)
	for _, v := range ns.Config.VolumeList() {
		p := driver.VolumePath(a.dataDir, &ns.Config, v.VolumeSlug)
		if hs != nil {
			if hv := hs.Config.FindVolume(v.VolumeSlug); hv != nil && hv.VolumeKind() != v.VolumeKind() && !volumeEmpty(driver.VolumePath(a.dataDir, &hs.Config, v.VolumeSlug)) {
				return fmt.Errorf("volume %s changes kind (%s → %s) but already holds data: use a new volume name", v.VolumeSlug, hv.VolumeKind(), v.VolumeKind())
			}
		}
		if st, err := os.Stat(p); err == nil && !volumeEmpty(p) {
			if v.VolumeKind() == "file" && st.IsDir() {
				return fmt.Errorf("volume %s is a single file but %s is a directory with data: use a new volume name", v.VolumeSlug, p)
			}
			if v.VolumeKind() == "dir" && !st.IsDir() {
				return fmt.Errorf("volume %s is a folder but %s is a file with data: use a new volume name", v.VolumeSlug, p)
			}
		}
	}
	return nil
}

// ───────────────────────── reset to starter content

func (a *Agent) resetTarget(r *http.Request) (*model.Config, *model.RevisionCatalog, *model.Site, *model.SiteVolume, *httpError) {
	cfg, cat := a.headConfig()
	site := siteOf(cfg, a.site)
	if site == nil {
		return nil, nil, nil, nil, herr(model.ErrNotFound, "no head config", nil)
	}
	v := site.Config.FindVolume(r.PathValue("volumeSlug"))
	if v == nil {
		return nil, nil, nil, nil, herr(model.ErrNotFound, "no such volume on this site", nil)
	}
	if v.SeedOf() == nil {
		return nil, nil, nil, nil, invalid("volume "+v.VolumeSlug+" has no starter content", []model.ValidationIssue{{Path: "seed", Code: "V_VOLUME_SEED", Message: "no seed", Severity: "error"}})
	}
	return cfg, cat, site, v, nil
}

func (a *Agent) handleResetVolumeStart(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	if _, _, _, _, e := a.resetTarget(r); e != nil {
		return nil, 0, e
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	slug := r.PathValue("volumeSlug")
	in := a.createIntent("volume-reset", &slug, nil, getRC(r))
	in.RevID = nil
	in.Volume = &model.IntentVolume{SiteSlug: a.site, VolumeSlug: slug}
	g := model.GateCheck{Open: true, Conditions: []model.GateCondition{{Key: "idle", Ok: true, Detail: "no update is running"}}, Sites: []string{a.site}}
	return model.ApproveStartResponse{Intent: in, Gate: g}, 201, nil
}

func (a *Agent) handleResetVolume(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.ApproveRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	cfg, cat, site, v, e := a.resetTarget(r)
	if e != nil {
		return nil, 0, e
	}
	if e := a.checkIntent(req.IntentID, "volume-reset", v.VolumeSlug, rc, req.Typed); e != nil {
		return nil, 0, e
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	ff := a.markIntentUsed(req.IntentID, rc.actor)
	res, err := a.resetVolume(r.Context(), cfg, cat, site, v, rc.actor, ff)
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	return res, 201, nil
}

// resetVolume: manual snapshot of the volume (note "before reset of <slug>"), stop its containers, wipe, seed, start.
func (a *Agent) resetVolume(ctx context.Context, cfg *model.Config, cat *model.RevisionCatalog, site *model.Site, v *model.SiteVolume, actor string, ff *model.FastForwardUse) (*model.ResetVolumeResponse, error) {
	var vr *volRef
	for _, x := range siteVolumes(a.dataDir, &site.Config) {
		if x.Name == v.VolumeSlug {
			x := x
			vr = &x
		}
	}
	if vr == nil {
		return nil, fmt.Errorf("volume %s not found", v.VolumeSlug)
	}
	ex, _ := a.drv.Existing(ctx)
	var running []string
	for _, cs := range vr.Containers {
		if e, ok := ex[cs]; ok && e.Running {
			running = append(running, e.ID)
		}
	}
	for _, id := range running {
		_ = a.drv.Stop(ctx, id, -1)
	}
	restart := func() {
		for _, id := range running {
			if err := a.dc.ContainerStart(context.Background(), id); err != nil {
				a.log.Error("volume", "restart after reset failed", "error", err.Error())
			}
		}
	}
	defer restart()
	id := newID()
	note := "before reset of " + v.VolumeSlug
	var who *string
	if actor != "" {
		who = &actor
	}
	if volExists(vr.Dir) {
		part, err := a.createPart(ctx, id, "manual", actor, note, a.headRevision(), cfg, []volRef{*vr}, nil)
		if err != nil {
			return nil, fmt.Errorf("snapshot before reset: %w", err)
		}
		a.activity(who, "agent", "snapshot.created", "snapshot "+id+" (manual, "+note+")", map[string]any{"snapshotId": id, "reason": "manual", "sizeBytes": part.SizeBytes})
	} else {
		id = ""
	}
	// wipe
	if st, err := os.Stat(vr.Dir); err == nil {
		if st.IsDir() {
			es, _ := os.ReadDir(vr.Dir)
			for _, e := range es {
				if err := os.RemoveAll(filepath.Join(vr.Dir, e.Name())); err != nil {
					return nil, err
				}
			}
		} else if err := os.Truncate(vr.Dir, 0); err != nil {
			return nil, err
		}
	}
	_ = a.db.DelKV(seededKey(v.VolumeSlug))
	_, n, err := a.seedVolume(cfg, cat, &site.Config, v, true, who)
	if err != nil {
		return nil, err
	}
	sum, data := fmt.Sprintf("volume %s reset to its starter content", v.VolumeSlug), map[string]any{"siteSlug": a.site, "volumeSlug": v.VolumeSlug, "snapshotId": id, "entries": n}
	if ff != nil { // §21 #95
		sum += " · " + fastForwardLabel(ff)
		data["fastForward"] = ff
	}
	a.activity(who, "agent", "volume.reset-to-seed", sum, data)
	_ = time.Now
	return &model.ResetVolumeResponse{SiteSlug: a.site, VolumeSlug: v.VolumeSlug, SnapshotID: id, Entries: n}, nil
}
