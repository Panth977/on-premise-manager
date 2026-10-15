package agent

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// Round D: site volumes. A declared site volume is the directory <dataDir>/vol/<volumeSlug>/. Snapshots archive exactly
// the declared volumes with backup = true (manual / scheduled: all of them; pre-update: see preUpdateVolumes); a
// restore is keyed by volumeSlug, whatever container mounts the volume now. Host paths, file / secret mounts, rendered
// files and backup = false volumes are never archived. Undeclaring a volume never deletes its directory (orphan).

// volRef is one declared site volume and the containers that mount it.
type volRef struct {
	Name       string   // volumeSlug
	Dir        string   // <dataDir>/vol/<volumeSlug>
	Containers []string // containerSlugs that mount it (sorted; enabled or not)
	Backup     bool
	Kind       string // dir | file (round E)
}

// siteVolumes returns the declared volumes of a site config (declaration order) with the containers that mount them.
// sc must be normalized (revConfig / headConfig); a legacy config is normalized here as well (images unknown).
func siteVolumes(dataDir string, sc *model.SiteConfig) []volRef {
	if validate.IsLegacySiteConfig(sc) {
		n := validate.NormalizeSiteConfigVolumes(*sc, nil)
		sc = &n
	}
	users := validate.VolumeUsers(sc)
	out := []volRef{}
	for _, v := range sc.VolumeList() {
		if !validate.RE.VolumeSlug.MatchString(v.VolumeSlug) {
			continue
		}
		cs := []string{}
		for _, u := range users[v.VolumeSlug] {
			if !contains(cs, u.ContainerSlug) {
				cs = append(cs, u.ContainerSlug)
			}
		}
		sort.Strings(cs)
		out = append(out, volRef{Name: v.VolumeSlug, Dir: driver.VolumePath(dataDir, sc, v.VolumeSlug), Containers: cs, Backup: v.Backup, Kind: v.VolumeKind()})
	}
	return out
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// volExists: the location of a volume exists (a directory, or the file of a single-file volume).
func volExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}

// backupVolumes = the declared volumes with backup = true whose directory exists (manual + scheduled snapshots).
func backupVolumes(dataDir string, sc *model.SiteConfig) []volRef {
	out := []volRef{}
	for _, v := range siteVolumes(dataDir, sc) {
		if v.Backup && volExists(v.Dir) {
			out = append(out, v)
		}
	}
	return out
}

// preUpdateVolumes selects the volumes of a pre-update snapshot (§13.2, round D): the backup = true volumes of the head
// config mounted by a container that the update recreates or removes (changed), plus the backup = true volumes of the
// head that the new config mounts in a container that is created or recreated (newDirs = their directories), plus (for
// a rollback) the backup = true head volumes the target snapshot restores. Directories that do not exist are skipped.
func preUpdateVolumes(head []volRef, changed map[string]bool, newDirs map[string]bool, restoring map[string]bool) []volRef {
	out := []volRef{}
	for _, v := range head {
		if !v.Backup || !volExists(v.Dir) {
			continue
		}
		use := restoring[v.Name] || newDirs[v.Dir]
		for _, c := range v.Containers {
			use = use || changed[c]
		}
		if use {
			out = append(out, v)
		}
	}
	return out
}

// ───────────────────────── GET /_opm/api/sites/{siteSlug}/volumes

type volSizes struct {
	at    time.Time
	sizes map[string]int64 // absolute dir → bytes (missing = no directory)
}

var volSizeCache sync.Map // instance → volSizes (sizes are measured at most every 60 s)

// volumeSizes returns the apparent size of the given directories plus every directory under <agentHome>/volumes/ and
// the part-1 <agentHome>/vol/ (measured at most every 60 s; a new directory list forces a measurement).
func (a *Agent) volumeSizes(dirs []string) (map[string]int64, time.Time) {
	if v, ok := volSizeCache.Load(a.instance); ok {
		c := v.(volSizes)
		fresh := time.Since(c.at) < 60*time.Second
		for _, d := range dirs {
			if _, ok := c.sizes[d]; !ok && volExists(d) {
				fresh = false
			}
		}
		if fresh {
			return c.sizes, c.at
		}
	}
	sizes := map[string]int64{}
	for _, root := range []string{filepath.Join(a.dataDir, "volumes"), filepath.Join(a.dataDir, "vol")} {
		if ents, err := os.ReadDir(root); err == nil {
			for _, e := range ents {
				if e.IsDir() {
					p := filepath.Join(root, e.Name())
					sizes[p] = dirSize(p)
				}
			}
		}
	}
	for _, d := range dirs {
		if _, ok := sizes[d]; !ok && volExists(d) {
			sizes[d] = dirSize(d)
		}
	}
	now := time.Now()
	volSizeCache.Store(a.instance, volSizes{at: now, sizes: sizes})
	return sizes, now
}

// forgetVolumeSizes drops the size cache (after a restore / snapshot the numbers changed).
func (a *Agent) forgetVolumeSizes() { volSizeCache.Delete(a.instance) }

// lastVolumeSnapshots: volumeSlug → the newest complete snapshot part of this site that contains it.
func (a *Agent) lastVolumeSnapshots() map[string]*model.VolumeSnapshotRef {
	out := map[string]*model.VolumeSnapshotRef{}
	for _, s := range a.snapRows() { // newest first
		if !s.Complete {
			continue
		}
		part, ok := s.Sites[a.site]
		if !ok {
			continue
		}
		for _, v := range part.Volumes {
			if _, seen := out[v.VolumeSlug]; !seen {
				out[v.VolumeSlug] = &model.VolumeSnapshotRef{SnapshotID: s.SnapshotID, CreatedAt: s.CreatedAt, SizeBytes: v.SizeBytes}
			}
		}
	}
	return out
}

// siteVolumesResponse builds SiteVolumesResponse for this site from the head config.
func (a *Agent) siteVolumesResponse() model.SiteVolumesResponse {
	cfg, cat := a.headConfig()
	var sc *model.SiteConfig
	if s := siteOf(cfg, a.site); s != nil {
		sc = &s.Config
	}
	var dirs []string
	for _, v := range sc.VolumeList() {
		dirs = append(dirs, driver.VolumePath(a.dataDir, sc, v.VolumeSlug))
	}
	sizes, at := a.volumeSizes(dirs)
	out := model.SiteVolumesResponse{SiteSlug: a.site, AgentHome: a.dataDir, MeasuredAt: tsOf(at), Volumes: []model.SiteVolumeStatus{}, HostPaths: []model.HostPathMount{}}
	last := a.lastVolumeSnapshots()
	size := func(dir string) *int64 {
		if n, ok := sizes[dir]; ok {
			return &n
		}
		return nil
	}
	declaredDirs := map[string]bool{}
	if sc != nil {
		vctx := a.validationContext(cat)
		hosts := a.siteHosts(cfg)
		sm := driver.SiteMapFor(cfg, cat, a.dep, hosts)
		uses := map[string][]model.SiteVolumeUse{}
		for i := range sc.Containers {
			c := &sc.Containers[i]
			img, ok := vctx.Images[validate.ImageKey(c.ServiceSlug, c.Version)]
			if !ok {
				continue
			}
			ectx := validate.ContainerExprContext(cfg, vctx.Services, vctx.Images, a.dep, nil, a.site, c, sm)
			cr := validate.ResolveContainerConfig(cfg, c, img.Env, img.Volumes, ectx)
			for _, m := range validate.ContainerMounts(c, img.Volumes) {
				path := m.MountPath
				if r, ok := cr.Resolution.Mounts[m.Key]; ok && r.Err == nil {
					path = r.Result.Value
				}
				switch m.Source.Kind {
				case "volume":
					uses[m.Source.VolumeSlug] = append(uses[m.Source.VolumeSlug], model.SiteVolumeUse{ContainerSlug: c.ContainerSlug, ImageVolume: m.ImageVolume, MountPath: path, ReadOnly: m.ReadOnly})
				case "host":
					out.HostPaths = append(out.HostPaths, model.HostPathMount{ContainerSlug: c.ContainerSlug, MountPath: path, Path: m.Source.Path, ReadOnly: m.ReadOnly})
				}
			}
		}
		for _, v := range sc.VolumeList() {
			dir := driver.VolumePath(a.dataDir, sc, v.VolumeSlug)
			declaredDirs[dir] = true
			u := uses[v.VolumeSlug]
			if u == nil {
				u = []model.SiteVolumeUse{}
			}
			out.Volumes = append(out.Volumes, model.SiteVolumeStatus{VolumeSlug: v.VolumeSlug, Label: v.Label, Description: v.Description, Backup: v.Backup,
				Declared: true, Path: dir, Location: v.VolumeLocation(), UsedBy: u, SizeBytes: size(dir), LastSnapshot: last[v.VolumeSlug],
				Kind: v.VolumeKind(), Seed: v.SeedOf(), Seeded: a.seededInfo(v.VolumeSlug)})
		}
	}
	// orphans: directories under volumes/ (and the part-1 vol/) that the head config does not use (kept on disk, never
	// backed up)
	var orphans []string
	for dir := range sizes {
		if !declaredDirs[dir] && (filepath.Dir(dir) == filepath.Join(a.dataDir, "volumes") || filepath.Dir(dir) == filepath.Join(a.dataDir, "vol")) {
			orphans = append(orphans, dir)
		}
	}
	sort.Strings(orphans)
	for _, dir := range orphans {
		name := filepath.Base(dir)
		loc := model.StorageLocation{Kind: "auto"} // contract: orphans carry auto; path tells where the directory is
		out.Volumes = append(out.Volumes, model.SiteVolumeStatus{VolumeSlug: name, Label: name, Description: "", Backup: false, Declared: false,
			Path: dir, Location: loc, UsedBy: []model.SiteVolumeUse{}, SizeBytes: size(dir), LastSnapshot: last[name], Kind: "dir"})
	}
	return out
}

func (a *Agent) handleSiteVolumes(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return a.siteVolumesResponse(), 200, nil
}

// ───────────────────────── round D part 2: locations on disk

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// moveFileBlobs moves the content-addressed file blobs out of files/ (before part 2) into blobs/files/, so files/
// only holds the per-site copies (fileSlugs never look like 64 hex characters).
func (a *Agent) moveFileBlobs() {
	old := filepath.Join(a.dataDir, "files")
	ents, err := os.ReadDir(old)
	if err != nil {
		return
	}
	n := 0
	for _, e := range ents {
		if !e.Type().IsRegular() || !hex64.MatchString(e.Name()) {
			continue
		}
		from, to := filepath.Join(old, e.Name()), driver.FileBlobPath(a.dataDir, e.Name())
		if _, err := os.Stat(to); err == nil {
			_ = os.Remove(from)
		} else if err := os.Rename(from, to); err != nil {
			a.log.Warn("files", "file blob move failed", "blob", e.Name(), "error", err.Error())
			continue
		}
		_, _ = a.db.Exec(`UPDATE blobs SET path = ? WHERE sha256 = ?`, to, e.Name())
		n++
	}
	if n > 0 {
		a.log.Info("files", "file blobs moved to blobs/files", "count", n)
	}
}

// moveLegacyVolumes moves volume directories into the locations of a target config (nil = the head): when the target
// directory does not exist, the data comes from the location the head declares for the same volumeSlug (a changed
// location) or from the part-1 directory <agentHome>/vol/<volumeSlug>. Returns an error for a move that failed (e.g.
// another file system) so the apply stops before a container starts on an empty directory.
func (a *Agent) moveLegacyVolumes(target *model.SiteConfig) error {
	hc, _ := a.headConfig()
	var head *model.SiteConfig
	if hs := siteOf(hc, a.site); hs != nil {
		head = &hs.Config
	}
	if target == nil {
		target = head
	}
	if target == nil {
		return nil
	}
	var errs []string
	for _, v := range target.VolumeList() {
		dst := driver.VolumePath(a.dataDir, target, v.VolumeSlug)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		var cands []string
		if head != nil && head.FindVolume(v.VolumeSlug) != nil {
			if p := driver.VolumePath(a.dataDir, head, v.VolumeSlug); p != dst {
				cands = append(cands, p)
			}
		}
		cands = append(cands, driver.LegacyVolumeDir(a.dataDir, v.VolumeSlug))
		for _, from := range cands {
			if st, err := os.Stat(from); err != nil || !st.IsDir() {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				errs = append(errs, err.Error())
				break
			}
			if err := os.Rename(from, dst); err != nil {
				errs = append(errs, fmt.Sprintf("move volume %s from %s to %s: %v (move it by hand)", v.VolumeSlug, from, dst, err))
				break
			}
			a.log.Info("volumes", "volume directory moved", "volumeSlug", v.VolumeSlug, "from", from, "to", dst)
			a.forgetVolumeSizes()
			break
		}
	}
	if len(errs) > 0 {
		a.log.Error("volumes", "volume move failed", "error", strings.Join(errs, "; "))
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ResolveDataDir is agentHome: the --data-dir flag > OPM_DATA_DIR > agent.json dataDir > /var/lib/opm.
func ResolveDataDir(flag, fromConfig string) string {
	for _, v := range []string{flag, os.Getenv("OPM_DATA_DIR"), fromConfig} {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return "/var/lib/opm"
}
