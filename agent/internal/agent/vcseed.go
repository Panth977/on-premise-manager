package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"opm/agent/internal/canonical"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
	"opm/agent/internal/volcontent"
)

// §21 #101 starter content = the volume's OWN content (seed kind tree / single): blobs fetched at staging like file
// blobs (cloud getAgentBlob, peers by sha256), written at APPLY under the round-E rules (seed.go seedVolume): only into a
// new / empty volume never seeded here, render 'expressions' entries ONCE per site, modes from the manifest, owner =
// the volume folder's owner, marker .opm-seeded + kv. Plus the placeholder transition (migration of Files that were
// mounted inside a writable volume, e.g. Node-RED settings.js).

// contentSeedBlobs lists the blobs of tree / single seeds (called from seedBlobs).
func contentSeedBlobs(v model.SiteVolume, revID string) []blobNeed {
	sd := v.SeedOf()
	if sd == nil {
		return nil
	}
	ref := model.FetchRef{FileSlug: "seed-" + v.VolumeSlug, RevID: revID}
	var out []blobNeed
	switch sd.Kind {
	case "tree":
		for _, e := range sd.Tree {
			out = append(out, blobNeed{Hex: hexOf(e.Sha256), Size: e.Size, Kind: "file", Ref: ref})
		}
	case "single":
		out = append(out, blobNeed{Hex: hexOf(sd.Sha256), Size: sd.Size, Kind: "file", Ref: ref})
	}
	return out
}

// contentBlobBytes reads a staged content blob.
func (a *Agent) contentBlobBytes(sha string) ([]byte, error) {
	p, ok := a.blobPath(hexOf(sha))
	if !ok {
		p = driver.FileBlobPath(a.dataDir, hexOf(sha))
	}
	return os.ReadFile(p)
}

// seedBlobContents: sha256 → text of every render-'expressions' tree / single entry of this site (staging validation:
// V_FILE_RENDER at …seed.entries[path]).
func (a *Agent) seedBlobContents(cfg *model.Config) map[string]string {
	s := siteOf(cfg, a.site)
	if s == nil {
		return nil
	}
	out := map[string]string{}
	add := func(sha, render string) {
		if render != "expressions" {
			return
		}
		if b, err := a.contentBlobBytes(sha); err == nil {
			out[sha] = string(b)
		}
	}
	for _, v := range s.Config.VolumeList() {
		sd := v.SeedOf()
		if sd == nil {
			continue
		}
		if sd.Kind == "single" {
			add(sd.Sha256, sd.Render)
		}
		if sd.Kind == "tree" {
			for _, e := range sd.Tree {
				add(e.Sha256, e.Render)
			}
		}
	}
	return out
}

// contentBytes is the content of one entry: the blob bytes, rendered once per site when render is expressions.
func (a *Agent) contentBytes(cfg *model.Config, cat *model.RevisionCatalog, sha, render, where string) ([]byte, error) {
	raw, err := a.contentBlobBytes(sha)
	if err != nil {
		return nil, fmt.Errorf("%s: content %s not staged: %w", where, short(sha), err)
	}
	if got := canonical.Sha256Tagged(raw); got != sha {
		return nil, fmt.Errorf("%s: content blob %s is corrupt", where, short(sha))
	}
	if render != "expressions" {
		return raw, nil
	}
	vctx := a.validationContext(cat)
	sm := driver.SiteMapFor(cfg, cat, a.dep, a.siteHosts(cfg))
	fctx := validate.SiteFileExprContext(cfg, vctx.Services, vctx.Images, a.dep, nil, a.site, sm)
	out := validate.RenderSiteFile(string(raw), fctx)
	if out.Err != nil {
		return nil, fmt.Errorf("V_FILE_RENDER %s:%d:%d: %s %s (site %s)", where, out.Err.Line, out.Err.Col, out.Err.Code, out.Err.Message, a.site)
	}
	return []byte(out.Value), nil
}

func parseMode(m string) fs.FileMode {
	if v, err := strconv.ParseUint(m, 8, 32); err == nil && volcontent.ReFileMode.MatchString(m) {
		return fs.FileMode(v)
	}
	return 0o644
}

// vcOwnerOf: uid / gid of p.
func vcOwnerOf(p string) (int, int, bool) {
	st, err := os.Lstat(p)
	if err != nil {
		return 0, 0, false
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(s.Uid), int(s.Gid), true
	}
	return 0, 0, false
}

// vcChown: best effort (an agent that is not root keeps its own uid).
func vcChown(p string, uid, gid int, ok bool) {
	if ok {
		_ = os.Lchown(p, uid, gid)
	}
}

// vcMkdirAll creates dir (and missing parents below root) 0755, owned like root.
func vcMkdirAll(root, dir string, uid, gid int, ok bool) error {
	if dir == root {
		return nil
	}
	if st, err := os.Lstat(dir); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s is not a folder", strings.TrimPrefix(dir, root+"/"))
		}
		return nil
	}
	if err := vcMkdirAll(root, filepath.Dir(dir), uid, gid, ok); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	_ = os.Chmod(dir, 0o755)
	vcChown(dir, uid, gid, ok)
	return nil
}

// vcWriteFile writes b to dst atomically (temp + rename in the same folder), mode, owner; never through a symlink.
func vcWriteFile(root, dst string, b []byte, mode fs.FileMode, uid, gid int, ok bool) error {
	if err := noLinkOnPath(root, dst); err != nil {
		return err
	}
	if st, err := os.Lstat(dst); err == nil && st.IsDir() {
		return fmt.Errorf("%s is a folder", strings.TrimPrefix(dst, root+"/"))
	}
	tmp := filepath.Join(filepath.Dir(dst), ".opm-tmp-"+newID())
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	_ = os.Chmod(tmp, mode)
	vcChown(tmp, uid, gid, ok)
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// writeContentSeed writes a tree / single seed into the (empty) location of v. Returns the files written.
func (a *Agent) writeContentSeed(cfg *model.Config, cat *model.RevisionCatalog, sc *model.SiteConfig, v *model.SiteVolume) (int, error) {
	sd := v.SeedOf()
	p := driver.VolumePath(a.dataDir, sc, v.VolumeSlug)
	if sd.Kind == "single" {
		b, err := a.contentBytes(cfg, cat, sd.Sha256, sd.Render, v.VolumeSlug)
		if err != nil {
			return 0, err
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return 0, fmt.Errorf("volume %s is a single file but %s is a directory", v.VolumeSlug, p)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return 0, err
		}
		uid, gid, ok := vcOwnerOf(p)
		if !ok {
			uid, gid, ok = vcOwnerOf(filepath.Dir(p))
		}
		// in place (O_TRUNC): a bind-mounted file keeps its inode
		if err := os.WriteFile(p, b, parseMode(sd.Mode)); err != nil {
			return 0, err
		}
		_ = os.Chmod(p, parseMode(sd.Mode))
		vcChown(p, uid, gid, ok)
		return 1, nil
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return 0, err
	}
	uid, gid, ok := vcOwnerOf(p)
	t := volcontent.Normalize(sd.TreeOf())
	n := 0
	for _, e := range t.Entries {
		if pe := volcontent.PathError(e.Path); pe != "" {
			return n, fmt.Errorf("content path %s: %s", e.Path, pe)
		}
		b, err := a.contentBytes(cfg, cat, e.Sha256, e.Render, v.VolumeSlug+"/"+e.Path)
		if err != nil {
			return n, err
		}
		dst := filepath.Join(p, filepath.FromSlash(e.Path))
		if err := vcMkdirAll(p, filepath.Dir(dst), uid, gid, ok); err != nil {
			return n, err
		}
		if err := vcWriteFile(p, dst, b, parseMode(e.Mode), uid, gid, ok); err != nil {
			return n, err
		}
		n++
	}
	for _, d := range t.Dirs {
		if pe := volcontent.PathError(d); pe != "" {
			return n, fmt.Errorf("content folder %s: %s", d, pe)
		}
		if err := noLinkOnPath(p, filepath.Join(p, filepath.FromSlash(d), "x")); err != nil {
			return n, err
		}
		if err := vcMkdirAll(p, filepath.Join(p, filepath.FromSlash(d)), uid, gid, ok); err != nil {
			return n, err
		}
	}
	return n, nil
}

// ───────────────────────── placeholder transition (§21 #101 migration safety)

type literalMount struct {
	container string
	path      string // cleaned, no trailing "/"
	source    model.VolumeSource
	readOnly  bool
}

// literalMounts: the effective mounts of an ENABLED container with literal mount paths (expressions skipped).
func literalMounts(c *model.Container, cat *model.RevisionCatalog) []literalMount {
	var ivs []model.ImageVolume
	if cat != nil {
		if im, ok := cat.Images[validate.ImageKey(c.ServiceSlug, c.Version)]; ok {
			ivs = im.Volumes
		}
	}
	var out []literalMount
	for _, m := range validate.ContainerMounts(c, ivs) {
		if strings.Contains(m.MountPath, "{{") || !strings.HasPrefix(m.MountPath, "/") {
			continue
		}
		out = append(out, literalMount{container: c.ContainerSlug, path: strings.TrimRight(filepath.Clean(m.MountPath), "/"), source: m.Source, readOnly: m.ReadOnly})
	}
	return out
}

// fillPlaceholders: at APPLY, for a volume V mounted read-write at Q by container C in the new config, when the
// PREVIOUS head mounted a File in C at P inside Q that the new config no longer mounts there, V's starter content has an
// entry at rel(P, Q), and the live file at <location>/rel is a regular file of size 0 (Docker created it as the bind
// mountpoint) → write that entry (rendered when render expressions) once; activity volume.placeholder-filled.
func (a *Agent) fillPlaceholders(prev *model.Config, prevCat *model.RevisionCatalog, cfg *model.Config, cat *model.RevisionCatalog, site *model.Site) {
	ps := siteOf(prev, a.site)
	if ps == nil || site == nil {
		return
	}
	done := map[string]bool{}
	for ci := range site.Config.Containers {
		c := &site.Config.Containers[ci]
		var pc *model.Container
		for k := range ps.Config.Containers {
			if ps.Config.Containers[k].ContainerSlug == c.ContainerSlug {
				pc = &ps.Config.Containers[k]
			}
		}
		if pc == nil {
			continue
		}
		now := literalMounts(c, cat)
		before := literalMounts(pc, prevCat)
		for _, w := range now {
			if w.readOnly || w.source.Kind != "volume" {
				continue
			}
			v := site.Config.FindVolume(w.source.VolumeSlug)
			if v == nil || v.VolumeKind() != "dir" || v.SeedOf() == nil || v.SeedOf().Kind != "tree" {
				continue
			}
			for _, f := range before {
				if f.source.Kind != "file" || !strings.HasPrefix(f.path, w.path+"/") {
					continue
				}
				still := false
				for _, m := range now {
					if m.path == f.path && m.source.Kind == "file" {
						still = true
					}
				}
				if still {
					continue
				}
				rel := strings.TrimPrefix(f.path, w.path+"/")
				var entry *model.VolumeTreeEntry
				for i := range v.SeedOf().Tree {
					if v.SeedOf().Tree[i].Path == rel {
						entry = &v.SeedOf().Tree[i]
					}
				}
				key := v.VolumeSlug + "/" + rel
				if entry == nil || done[key] || volcontent.PathError(rel) != "" {
					continue
				}
				root := driver.VolumePath(a.dataDir, &site.Config, v.VolumeSlug)
				dst := filepath.Join(root, filepath.FromSlash(rel))
				st, err := os.Lstat(dst)
				if err != nil || !st.Mode().IsRegular() || st.Size() != 0 {
					continue
				}
				done[key] = true
				b, err := a.contentBytes(cfg, cat, entry.Sha256, entry.Render, key)
				if err != nil {
					a.log.Warn("apply", "placeholder not filled", "volumeSlug", v.VolumeSlug, "path", rel, "error", err.Error())
					continue
				}
				uid, gid, ok := vcOwnerOf(dst)
				if err := vcWriteFile(root, dst, b, parseMode(entry.Mode), uid, gid, ok); err != nil {
					a.log.Warn("apply", "placeholder not filled", "volumeSlug", v.VolumeSlug, "path", rel, "error", err.Error())
					continue
				}
				a.forgetVolumeSizes()
				a.activity(nil, "agent", "volume.placeholder-filled", trim200(fmt.Sprintf("volume %s: %s (the former mountpoint of file %s) got its content", v.VolumeSlug, rel, f.source.FileSlug)),
					map[string]any{"siteSlug": a.site, "volumeSlug": v.VolumeSlug, "path": rel, "sha256": entry.Sha256, "size": len(b), "formerFileSlug": f.source.FileSlug})
				a.log.Info("apply", "placeholder filled", "volumeSlug", v.VolumeSlug, "path", rel, "formerFileSlug", f.source.FileSlug)
			}
		}
	}
}
