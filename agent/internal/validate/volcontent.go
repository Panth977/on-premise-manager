package validate

import (
	"fmt"
	"regexp"
	"strings"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
	"opm/agent/internal/volcontent"
)

// §21 #101 volume content (tree / single seeds), persist-to-cloud settings, Files never inside a writable mount, local
// edits never change content / persist. Mirror of shared/src/validate.ts (checkSeed, checkPersist,
// V_FILE_IN_WRITABLE_VOLUME, blobContents renders, checkLocalEdit).

// ContentRenders: sha256 → the paths ("…seed.entries[p]" / "…seed") of entries with render 'expressions' (ordered).
type ContentRenders struct {
	order []string
	where map[string][]string
}

func (c *ContentRenders) add(sha, at string) {
	if c == nil {
		return
	}
	if c.where == nil {
		c.where = map[string][]string{}
	}
	if _, ok := c.where[sha]; !ok {
		c.order = append(c.order, sha)
	}
	c.where[sha] = append(c.where[sha], at)
}

// checkContentSeed handles tree / single seeds and the legacy-seed rule; true = the seed is fully checked here.
func checkContentSeed(v *model.SiteVolume, sd *model.VolumeSeed, vp string, is *Issues, renders *ContentRenders) bool {
	kind := v.VolumeKind()
	if (sd.Kind == "files" || sd.Kind == "file") && is.Mode != ModeAgent {
		var refs []string
		if sd.Kind == "file" {
			refs = []string{sd.FileSlug}
		} else {
			for _, e := range sd.Entries {
				refs = append(refs, e.FileSlug)
			}
		}
		is.err(vp+".seed", "V_VOLUME_SEED_FILE_REF", fmt.Sprintf("volume %s takes its starter content from Files (%s) — Files are static and read-only and are never volume content: give the volume its own content (Volume → Content; backend/scripts/migrate-h.ts converts it)", v.VolumeSlug, strings.Join(refs, ", ")))
		return true
	}
	// §21 #117 a persist-to-cloud volume's seed is its COMMITTED content: a literal tree within the persisted limits
	persist := v.Persist
	if persist != nil && sd.Kind != "tree" {
		is.err(vp+".seed", "V_VOLUME_SEED", fmt.Sprintf("volume %s persists to the cloud: its content is a file tree (kind tree)", v.VolumeSlug))
		return true
	}
	switch sd.Kind {
	case "tree":
		if kind != "dir" {
			is.err(vp+".seed", "V_VOLUME_SEED", "a single-file volume has one file of content (kind single), not a tree")
			return true
		}
		for _, x := range volcontent.TreeIssues(sd.TreeOf(), persist == nil) {
			at := vp + ".seed"
			if x.Path != "" {
				at = vp + ".seed.entries[" + x.Path + "]"
			}
			code := x.Code
			if code == "V_VOLUME_CONTENT" {
				code = "V_VOLUME_SEED"
			}
			is.err(at, code, "volume "+v.VolumeSlug+": "+x.Message)
		}
		if persist != nil {
			checkPersistContent(v, sd, persist, vp, is)
			return true
		}
		for _, e := range sd.Tree {
			if e.Render == "expressions" && e.Sha256 != "" {
				renders.add(e.Sha256, vp+".seed.entries["+e.Path+"]")
			}
		}
		return true
	case "single":
		if kind != "file" {
			is.err(vp+".seed", "V_VOLUME_SEED", "a folder volume has a tree of content (kind tree), not a single file")
			return true
		}
		at := vp + ".seed"
		if !RE.Sha256.MatchString(sd.Sha256) {
			is.err(at+".sha256", "V_FORMAT", "upload the content")
		}
		if sd.Size < 0 || sd.Size > model.VolumeContentFileMaxBytes {
			is.err(at+".size", "V_TOO_LARGE", fmt.Sprintf("the file is 0 B – %d MiB", model.VolumeContentFileMaxBytes/1048576))
		}
		if !volcontent.ReFileMode.MatchString(sd.Mode) {
			is.err(at+".mode", "V_FORMAT", "mode must look like 0644")
		}
		if sd.Render != "none" && sd.Render != "expressions" {
			is.err(at+".render", "V_ENUM", "render must be none | expressions")
		} else if sd.Render == "expressions" && sd.Sha256 != "" {
			renders.add(sd.Sha256, at)
		}
		return true
	}
	return false
}

// checkPersistContent is TS checkPersistContent (§21 #117): at most persist.maxBytes, nothing under the excludes.
func checkPersistContent(v *model.SiteVolume, sd *model.VolumeSeed, persist *model.VolumePersist, vp string, is *Issues) {
	var total int64
	for _, e := range sd.Tree {
		total += e.Size
	}
	if persist.MaxBytes > 0 && total > persist.MaxBytes {
		is.err(vp+".seed", "V_TOO_LARGE", fmt.Sprintf("volume %s: the content is %s — at most %s (Persist to cloud › size cap)", v.VolumeSlug, volcontent.FormatBytes(total), volcontent.FormatBytes(persist.MaxBytes)))
	}
	for _, pat := range persist.Exclude {
		if volcontent.ExcludePatternError(pat) != "" {
			return
		}
	}
	ex := volcontent.Compile(persist.Exclude)
	for _, e := range sd.Tree {
		if ex.Excluded(e.Path, false) {
			is.err(vp+".seed.entries["+e.Path+"]", "V_VOLUME_CONTENT_EXCLUDED", fmt.Sprintf("volume %s: %s is excluded from the persisted content (it could never come back from the site)", v.VolumeSlug, e.Path))
		}
	}
	for _, d := range sd.Dirs {
		if ex.Excluded(d, true) {
			is.err(vp+".seed.entries["+d+"]", "V_VOLUME_CONTENT_EXCLUDED", fmt.Sprintf("volume %s: folder %s is excluded from the persisted content", v.VolumeSlug, d))
		}
	}
}

// checkPersist is TS checkPersist (SiteVolume.persist: folder volumes only; excludes, size cap, auto).
func checkPersist(v *model.SiteVolume, vp string, is *Issues) {
	p := v.Persist
	if p == nil {
		return
	}
	at := vp + ".persist"
	if v.VolumeKind() != "dir" {
		is.err(at, "V_VOLUME_PERSIST", fmt.Sprintf("volume %s is a single file: persist to cloud needs a folder volume", v.VolumeSlug))
	}
	if len(p.Exclude) > model.VolumeExcludeMax {
		is.err(at+".exclude", "V_TOO_MANY", fmt.Sprintf("at most %d exclude patterns", model.VolumeExcludeMax))
	}
	for i, pat := range p.Exclude {
		if e := volcontent.ExcludePatternError(pat); e != "" {
			is.err(fmt.Sprintf("%s.exclude[%d]", at, i), "V_VOLUME_PERSIST", e)
		}
	}
	for _, d := range dupes(p.Exclude) {
		is.err(at+".exclude", "V_DUPLICATE", fmt.Sprintf("exclude pattern %s is listed twice", d))
	}
	if p.MaxBytes < model.VolumePersistMinBytes || p.MaxBytes > model.VolumePersistMaxBytes {
		is.err(at+".maxBytes", "V_RANGE", fmt.Sprintf("the snapshot size cap is %d – %d MiB", model.VolumePersistMinBytes/1048576, model.VolumePersistMaxBytes/1048576))
	}
}

var reEntriesWhere = regexp.MustCompile(`^.*\.seed\.entries\[|\]$`)

// checkContentRenders renders every 'expressions' entry once per site when the contents are known (V_FILE_RENDER).
func checkContentRenders(cfg *model.Config, ctx Context, siteSlug string, siteMap map[string]expr.SiteRef, renders *ContentRenders, disabledRef func(src, path string), is *Issues) {
	if ctx.BlobContents == nil || renders == nil || len(renders.order) == 0 {
		return
	}
	fctx := SiteFileExprContext(cfg, ctx.Services, ctx.Images, ctx.DeploymentID, ctx.SecretsSet, siteSlug, siteMap)
	for _, sha := range renders.order {
		content, ok := ctx.BlobContents[sha]
		if !ok {
			continue
		}
		out := RenderSiteFile(content, fctx)
		if out.Err != nil && out.Err.Code == expr.EDeferred {
			continue
		}
		for _, w := range renders.where[sha] {
			disabledRef(content, w)
			if out.Err != nil {
				is.Err(w, "V_FILE_RENDER", fmt.Sprintf("%s:%d:%d: %s %s (site %s)", reEntriesWhere.ReplaceAllString(w, ""), out.Err.Line, out.Err.Col, out.Err.Code, out.Err.Message, siteSlug), true)
			}
		}
	}
}

// checkFileInWritableVolume: a File is static and read-only — never mounted on / inside a writable volume or host
// mount of the same container (agent mode: a warning; else an error, publish level when an expression is involved).
func checkFileInWritableVolume(all []EffectiveMount, res map[string]expr.Resolved, at string, is *Issues) {
	type eff struct {
		path string
		expr bool
		ok   bool
	}
	effOf := func(m EffectiveMount) eff {
		rr, has := res[m.Key]
		p := m.MountPath
		if has && rr.Err == nil && !rr.Result.Deferred {
			p = rr.Result.Value
		}
		x := expr.IsExpr(m.MountPath)
		return eff{strings.TrimRight(p, "/"), x, !x || (has && rr.Err == nil && !rr.Result.Deferred)}
	}
	for _, fm := range all {
		if fm.Source.Kind != "file" {
			continue
		}
		f := effOf(fm)
		if !f.ok || f.path == "" {
			continue
		}
		for _, wm := range all {
			if wm.ReadOnly || (wm.Source.Kind != "volume" && wm.Source.Kind != "host") {
				continue
			}
			w := effOf(wm)
			if !w.ok || w.path == "" || !(f.path == w.path || strings.HasPrefix(f.path, w.path+"/")) {
				continue
			}
			what, fix := "volume "+wm.Source.VolumeSlug, ""
			if wm.Source.Kind == "volume" {
				rel := strings.TrimPrefix(f.path[len(w.path):], "/")
				if rel == "" {
					rel = "(the mount itself)"
				}
				fix = fmt.Sprintf("put it into the volume's own content (Volume → Content) as %s, mount the file outside %s, or mount %s read-only", rel, w.path, what)
			} else {
				what = "host folder " + wm.Source.Path
				fix = fmt.Sprintf("mount the file outside %s, or mount the host folder read-only", w.path)
			}
			var fp string
			if fm.ImageVolume != nil {
				fp = at + ".volumes[" + *fm.ImageVolume + "].source.fileSlug"
			} else {
				fp = at + ".volumes[" + fm.Key[7:len(fm.Key)-1] + "].source.fileSlug"
			}
			msg := fmt.Sprintf("file %s is mounted at %s inside the writable %s (%s) — Files are static and read-only and are never mounted on a writable volume: %s", fm.Source.FileSlug, f.path, what, w.path, fix)
			if is.Mode == ModeAgent {
				is.Warn(fp, "V_FILE_IN_WRITABLE_VOLUME", msg)
			} else {
				is.Err(fp, "V_FILE_IN_WRITABLE_VOLUME", msg, f.expr || w.expr)
			}
			break
		}
	}
}

// checkLocalVolumeContent: a volume's own content (non-archive seeds) and its persist setting are cloud configuration.
func checkLocalVolumeContent(o *model.Site, s *model.Site, is *Issues) {
	for _, v := range s.Config.VolumeList() {
		v := v
		ov := o.Config.FindVolume(v.VolumeSlug)
		if ov == nil {
			continue // a volume added on the site
		}
		if !sameCanonical(ov.Persist, v.Persist) {
			is.err("sites["+s.SiteSlug+"].config.volumes["+v.VolumeSlug+"]", "V_LOCAL_VOLUME_CONTENT", fmt.Sprintf("volume %s: its persist-to-cloud setting changes only in the cloud (Volume → Content)", v.VolumeSlug))
		}
	}
}
