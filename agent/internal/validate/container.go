package validate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// NormalizeImageEnv is TS normalizeImageEnv: legacy `value: string[]` (allowed values, first = default) becomes
// Options + Value = the first item; missing options → null (Options.Set = true, nil). Pure (the input is not changed).
func NormalizeImageEnv(e model.ImageEnv) model.ImageEnv {
	out := model.ImageEnv{Name: e.Name, Required: e.Required, Secret: e.Secret, Description: e.Description, Options: model.Null[[]string]()}
	if e.Options.Value != nil {
		out.Options = model.Some(append([]string{}, *e.Options.Value...))
	}
	if e.Value != nil && e.Value.List != nil {
		list := append([]string{}, e.Value.List...)
		if e.Options.Value == nil {
			out.Options = model.Some(list)
		}
		if len(list) > 0 {
			v := list[0]
			out.Value = &model.ImageEnvValue{Str: &v}
		}
	} else if e.Value != nil && e.Value.Str != nil {
		v := *e.Value.Str
		out.Value = &model.ImageEnvValue{Str: &v}
	}
	return out
}

// NormalizeImageEnvs normalises a list (nil stays nil).
func NormalizeImageEnvs(env []model.ImageEnv) []model.ImageEnv {
	if env == nil {
		return nil
	}
	out := make([]model.ImageEnv, len(env))
	for i, e := range env {
		out[i] = NormalizeImageEnv(e)
	}
	return out
}

// ReservedContainerSlugs collide with GUI routes (…/containers/new).
var ReservedContainerSlugs = []string{"new"}

// DefaultVolumeSlug is TS defaultVolumeSlug: `<containerSlug>-<imageVolume>` (≤ 100 chars), the suggested volumeSlug
// of "+ New data volume…" (and the name a pre-round-D implicit image volume had).
func DefaultVolumeSlug(containerSlug, imageVolume string) string {
	s := containerSlug + "-" + imageVolume
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100])
	}
	return s
}

// NewSiteVolume is TS newSiteVolume (label "" → the volumeSlug, backup on by default in callers).
func NewSiteVolume(volumeSlug, label, description string, backup bool) model.SiteVolume {
	if label == "" {
		label = volumeSlug
	}
	dir := "dir"
	return model.SiteVolume{VolumeSlug: volumeSlug, Label: label, Description: description, Backup: backup, Location: AutoLocation(), Kind: &dir, Seed: model.Null[model.VolumeSeed]()}
}

// SeedArchiveMax is TS SEED_ARCHIVE_MAX.
const SeedArchiveMax = 200 * 1024 * 1024

var seedPathRe = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+/)*[A-Za-z0-9._-]+$`)

// SeedPathError is TS seedPathError ("" = valid).
func SeedPathError(p string) string {
	switch {
	case p == "":
		return "enter a path inside the volume, e.g. flows.json or lib/settings.js"
	case utf16Len(p) > 255:
		return "at most 255 characters"
	case strings.HasPrefix(p, "/"):
		return `the path is relative to the volume: no leading "/"`
	case !seedPathRe.MatchString(p):
		return `letters, digits, . _ - and "/" between folders only`
	}
	for _, x := range strings.Split(p, "/") {
		if x == "." || x == ".." {
			return `"." and ".." are not allowed`
		}
	}
	return ""
}

// FileVolumeRenameWarning is TS FILE_VOLUME_RENAME_WARNING.
const FileVolumeRenameWarning = "Apps that save by writing a temp file and renaming it (e.g. Node-RED flows.json) need a folder volume — a single-file Docker mount breaks on rename. Use a folder volume seeded with the files instead."

var archiveNameRe = regexp.MustCompile(`(?i)\.(tar\.gz|tgz)$`)

// checkSeed is TS checkSeed (§21 #101: tree / single + persist in volcontent.go; legacy File seeds only in agent mode).
func checkSeed(v *model.SiteVolume, vp string, cfg *model.Config, is *Issues, renders *[]string, contentRenders *ContentRenders) {
	checkPersist(v, vp, is)
	checkBackupPolicy(v, vp, is) // §21 #112
	sd := v.SeedOf()
	if sd == nil {
		return
	}
	if sd.Kind != "tree" && sd.Kind != "single" && sd.Kind != "files" && sd.Kind != "file" && sd.Kind != "archive" {
		is.err(vp+".seed", "V_VOLUME_SEED", "seed must be null, tree, single or archive")
		return
	}
	if checkContentSeed(v, sd, vp, is, contentRenders) {
		return
	}
	kind := v.VolumeKind()
	if sd.Kind == "file" && kind != "file" {
		is.err(vp+".seed", "V_VOLUME_SEED", "a folder volume starts from files or an archive (a single file seed is for single-file volumes)")
		return
	}
	if sd.Kind != "file" && kind == "file" {
		is.err(vp+".seed", "V_VOLUME_SEED", "a single-file volume starts from one file")
		return
	}
	fileRef := func(slug, at string) {
		var f *model.FileDef
		for k := range cfg.Files {
			if cfg.Files[k].FileSlug == slug {
				f = &cfg.Files[k]
			}
		}
		if f == nil {
			if slug != "" {
				is.err(at, "V_REF", fmt.Sprintf("file %s does not exist", slug))
			} else {
				is.err(at, "V_REQUIRED", "pick the file with the starter content")
			}
			return
		}
		if f.Content == nil {
			is.Err(at, "V_FILE_CONTENT", fmt.Sprintf("file %s has no content", f.FileSlug), true)
		}
		if f.RenderMode() == "expressions" && !contains(*renders, f.FileSlug) {
			*renders = append(*renders, f.FileSlug)
		}
	}
	switch sd.Kind {
	case "file":
		fileRef(sd.FileSlug, vp+".seed.fileSlug")
	case "files":
		if len(sd.Entries) < 1 || len(sd.Entries) > 100 {
			is.err(vp+".seed.entries", "V_VOLUME_SEED", "starter files need 1–100 entries")
			return
		}
		for i, e := range sd.Entries {
			at := fmt.Sprintf("%s.seed.entries[%d]", vp, i)
			first := i
			for k, q := range sd.Entries {
				if q.Path == e.Path {
					first = k
					break
				}
			}
			nested := false
			for k, q := range sd.Entries {
				if k != i && strings.HasPrefix(q.Path, e.Path+"/") {
					nested = true
				}
			}
			if pe := SeedPathError(e.Path); pe != "" {
				is.err(at+".path", "V_VOLUME_SEED", pe)
			} else if first != i {
				is.err(at+".path", "V_DUPLICATE", e.Path+" is listed twice")
			} else if nested {
				is.err(at+".path", "V_VOLUME_SEED", e.Path+" is a file, but another entry puts files inside it")
			}
			fileRef(e.FileSlug, at+".fileSlug")
		}
	default:
		if !RE.Sha256.MatchString(string(sd.BlobSha256)) {
			is.err(vp+".seed.blobSha256", "V_FORMAT", "upload the archive (.tar.gz)")
		}
		if !archiveNameRe.MatchString(sd.Name) || utf16Len(sd.Name) > 200 {
			is.err(vp+".seed.name", "V_VOLUME_SEED", "the archive must be a .tar.gz / .tgz")
		}
		if sd.Size < 1 || sd.Size > SeedArchiveMax {
			is.err(vp+".seed.size", "V_VOLUME_SEED", fmt.Sprintf("the archive must be 1 B – %d MiB", SeedArchiveMax/1048576))
		}
	}
}

// VolumeUse is TS VolumeUse: one mount of a site volume (MountPath as written, nil = the image volume path).
type VolumeUse struct {
	ContainerSlug string
	ImageVolume   *string
	MountPath     *string
	ReadOnly      bool
	Enabled       bool
}

// VolumeUsers is TS volumeUsers: volumeSlug → the mounts that use it (every container, enabled or not). Only kind
// volume counts (a legacy data source is converted by NormalizeSiteConfigVolumes first).
func VolumeUsers(sc *model.SiteConfig) map[string][]VolumeUse {
	out := map[string][]VolumeUse{}
	for _, c := range sc.Containers {
		for _, v := range c.Volumes {
			if v.Source.Kind == "volume" {
				n := v.Source.VolumeSlug
				out[n] = append(out[n], VolumeUse{ContainerSlug: c.ContainerSlug, ImageVolume: v.ImageVolume, MountPath: v.MountPath, ReadOnly: v.ReadOnly, Enabled: c.Enabled})
			}
		}
	}
	return out
}

// UndeclaredVolumes is TS undeclaredVolumes: volumeSlugs that mounts use but the site does not declare (first-use order).
func UndeclaredVolumes(sc *model.SiteConfig) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range sc.Containers {
		for _, v := range c.Volumes {
			n := v.Source.VolumeSlug
			if v.Source.Kind != "volume" || seen[n] {
				continue
			}
			seen[n] = true
			if sc.FindVolume(n) == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

var volSlugBad = regexp.MustCompile(`[^a-z0-9_.-]+`)
var volSlugLead = regexp.MustCompile(`^[^a-z0-9]+`)

// SuggestVolumeSlug is TS suggestVolumeSlug: base, base-2, base-3 … among the taken ones.
func SuggestVolumeSlug(base string, taken []string) string {
	b := volSlugLead.ReplaceAllString(volSlugBad.ReplaceAllString(strings.ToLower(base), "-"), "")
	if len(b) > 100 {
		b = b[:100]
	}
	if b == "" {
		b = "data"
	}
	if !contains(taken, b) {
		return b
	}
	for i := 2; ; i++ {
		suf := fmt.Sprintf("-%d", i)
		bb := b
		if len(bb) > 100-len(suf) {
			bb = bb[:100-len(suf)]
		}
		if cand := bb + suf; !contains(taken, cand) {
			return cand
		}
	}
}

// IsLegacySiteConfig is TS isLegacySiteConfig: no `volumes` list, or a mount with the old source kind "data".
func IsLegacySiteConfig(sc *model.SiteConfig) bool {
	if sc.Volumes == nil {
		return true
	}
	for _, c := range sc.Containers {
		for _, v := range c.Volumes {
			if v.Source.Kind == "data" {
				return true
			}
		}
	}
	return false
}

// NormalizeSiteConfigVolumes is the volume part of TS normalizeSiteConfig (pure; returns a deep copy): a config without
// `volumes` (pre-round-D) gets explicit rows `{kind:volume, volumeSlug:<container>-<iv>}` for image volumes without a
// row (image known), every legacy `{kind:data,name}` becomes `{kind:volume, volumeSlug:name}` with readOnly false, and
// every such volume is declared (label = volumeSlug, backup true). A current config is returned unchanged (same values).
func NormalizeSiteConfigVolumes(sc model.SiteConfig, images map[string]model.ImageSpec) model.SiteConfig {
	if !IsLegacySiteConfig(&sc) {
		return sc
	}
	var cp model.SiteConfig
	b, _ := json.Marshal(sc)
	_ = json.Unmarshal(b, &cp)
	preD := cp.Volumes == nil
	vols := append([]model.SiteVolume{}, cp.VolumeList()...)
	names := []string{}
	add := func(n string) {
		if !contains(names, n) {
			names = append(names, n)
		}
	}
	for ci := range cp.Containers {
		c := &cp.Containers[ci]
		for i := range c.Volumes {
			v := &c.Volumes[i]
			if v.Source.Kind == "data" {
				add(v.Source.Name)
				v.Source = model.VolumeSource{Kind: "volume", VolumeSlug: v.Source.Name}
				v.ReadOnly = false
			} else if preD && v.Source.Kind == "volume" {
				add(v.Source.VolumeSlug)
			}
		}
		if preD {
			if img, ok := images[ImageKey(c.ServiceSlug, c.Version)]; ok {
				for _, iv := range img.Volumes {
					has := false
					for _, v := range c.Volumes {
						if v.ImageVolume != nil && *v.ImageVolume == iv.Name {
							has = true
						}
					}
					if !has {
						n, slug := iv.Name, DefaultVolumeSlug(c.ContainerSlug, iv.Name)
						c.Volumes = append(c.Volumes, model.ContainerMount{ImageVolume: &n, Source: model.VolumeSource{Kind: "volume", VolumeSlug: slug}})
						add(slug)
					}
				}
			}
		}
	}
	for _, n := range names {
		found := false
		for _, v := range vols {
			if v.VolumeSlug == n {
				found = true
			}
		}
		if !found {
			vols = append(vols, NewSiteVolume(n, "", "", true))
		}
	}
	cp.Volumes = &vols
	return cp
}

// NormalizeConfigVolumes applies NormalizeSiteConfigVolumes to every site. It returns cfg itself when no site is
// legacy, else a copy (the stored revision is never changed: its hash covers the original shape).
func NormalizeConfigVolumes(cfg *model.Config, images map[string]model.ImageSpec) *model.Config {
	legacy := false
	for i := range cfg.Sites {
		if IsLegacySiteConfig(&cfg.Sites[i].Config) {
			legacy = true
		}
	}
	if !legacy {
		return cfg
	}
	cp := *cfg
	cp.Sites = make([]model.Site, len(cfg.Sites))
	for i, s := range cfg.Sites {
		s.Config = NormalizeSiteConfigVolumes(s.Config, images)
		cp.Sites[i] = s
	}
	return &cp
}

// SuggestContainerSlug is TS suggestContainerSlug: the service slug, then <slug>-2, -3 … (≤ 40 chars).
func SuggestContainerSlug(serviceSlug string, taken []string) string {
	if !contains(taken, serviceSlug) && !contains(ReservedContainerSlugs, serviceSlug) {
		return serviceSlug
	}
	if contains(ReservedContainerSlugs, serviceSlug) {
		taken = append(append([]string{}, taken...), serviceSlug)
	}
	for i := 2; ; i++ {
		suf := fmt.Sprintf("-%d", i)
		base := serviceSlug
		if len(base) > 40-len(suf) {
			base = base[:40-len(suf)]
		}
		cand := strings.TrimRight(base, "-") + suf
		if !contains(taken, cand) {
			return cand
		}
	}
}

// EffectiveMount is TS EffectiveMount: one mount of a container (image-volume rows with source none are left out).
type EffectiveMount struct {
	Key         string  // "image:<name>" (image-volume rows, explicit or implicit) or "volume[i]" (extra mounts)
	ImageVolume *string // nil = extra mount
	MountPath   string  // as written (may hold {{ }})
	Source      model.VolumeSource
	ReadOnly    bool
}

// ContainerMounts is TS containerMounts: the image-volume rows (in image volume order) + the extra mounts. An image
// volume without a row, or with source none, is not mounted (round D: there is no implicit data volume).
func ContainerMounts(c *model.Container, imgVolumes []model.ImageVolume) []EffectiveMount {
	out := []EffectiveMount{}
	for _, iv := range imgVolumes {
		var row *model.ContainerMount
		for i := range c.Volumes {
			if c.Volumes[i].ImageVolume != nil && *c.Volumes[i].ImageVolume == iv.Name {
				row = &c.Volumes[i]
				break
			}
		}
		if row == nil || row.Source.Kind == "" || row.Source.Kind == "none" {
			continue
		}
		src := row.Source
		ro := src.Kind == "file" || src.Kind == "secret" || row.ReadOnly
		mp := iv.MountPath
		if row.MountPath != nil {
			mp = *row.MountPath
		}
		name := iv.Name
		out = append(out, EffectiveMount{Key: "image:" + iv.Name, ImageVolume: &name, MountPath: mp, Source: src, ReadOnly: ro})
	}
	for i, v := range c.Volumes {
		if v.ImageVolume != nil || v.Source.Kind == "" || v.Source.Kind == "none" {
			continue
		}
		ro := v.Source.Kind == "file" || v.Source.Kind == "secret" || v.ReadOnly
		mp := ""
		if v.MountPath != nil {
			mp = *v.MountPath
		}
		out = append(out, EffectiveMount{Key: fmt.Sprintf("volume[%d]", i), MountPath: mp, Source: v.Source, ReadOnly: ro})
	}
	return out
}

// EffectivePort is a port row of a container for one image port (+ the image protocol).
type EffectivePort struct {
	model.ContainerPort
	Protocol string
}

// ContainerPorts is TS containerPorts: one row per image port (missing row = not published, hostIp 0.0.0.0, edge true).
func ContainerPorts(c *model.Container, imgPorts []model.ImagePort) []EffectivePort {
	out := []EffectivePort{}
	for _, ip := range imgPorts {
		p := model.ContainerPort{PortName: ip.Name, HostIP: "0.0.0.0", Edge: true}
		for _, r := range c.Ports {
			if r.PortName == ip.Name {
				p = r
				if p.HostIP == "" {
					p.HostIP = "0.0.0.0"
				}
				break
			}
		}
		out = append(out, EffectivePort{ContainerPort: p, Protocol: ip.Protocol})
	}
	return out
}

// ContainerSources is TS containerSources: every container env (field env; secretRef → the secret value), every image
// env not set by the container (field imageEnv, its default or nil), and every effective mount (ContainerMounts keys).
// secrets: slug → plain value; nil = use SecretDef.Value of cfg.
func ContainerSources(cfg *model.Config, c *model.Container, imgEnv []model.ImageEnv, imgVolumes []model.ImageVolume, secrets map[string]*string) ([]expr.EnvSource, []expr.MountSource) {
	sec := secrets
	if sec == nil {
		sec = map[string]*string{}
		for _, s := range cfg.Secrets {
			sec[s.SecretSlug] = s.Value
		}
	}
	env := []expr.EnvSource{}
	given := map[string]bool{}
	for _, e := range c.Env {
		given[e.Name] = true
		if e.SecretRef != nil {
			env = append(env, expr.EnvSource{Name: e.Name, Field: expr.FieldEnv, SecretRef: &expr.SecretRefVal{Value: sec[*e.SecretRef]}})
		} else {
			env = append(env, expr.EnvSource{Name: e.Name, Src: e.Value, Field: expr.FieldEnv})
		}
	}
	for _, raw := range imgEnv {
		ie := NormalizeImageEnv(raw)
		if given[ie.Name] {
			continue
		}
		var src *string
		if ie.Value != nil && ie.Value.Str != nil {
			v := *ie.Value.Str
			src = &v
		}
		env = append(env, expr.EnvSource{Name: ie.Name, Src: src, Field: expr.FieldImageEnv})
	}
	mounts := []expr.MountSource{}
	for _, m := range ContainerMounts(c, imgVolumes) {
		ms := expr.MountSource{Key: m.Key, Src: m.MountPath}
		if m.Source.Kind == "file" {
			fs := m.Source.FileSlug
			ms.FileSlug = &fs
		}
		mounts = append(mounts, ms)
	}
	return env, mounts
}

// ContainerConfig is the result of ResolveContainerConfig.
type ContainerConfig struct {
	Env        []expr.EnvSource
	Mounts     []expr.MountSource
	Resolution *expr.ContainerResolution
}

// ResolveContainerConfig is TS resolveContainerConfig: resolves the env vars and mount paths of one container in
// dependency order (container.env.<NAME>, file.<slug>.path; E_CYCLE). The secret values come from ectx.Secret
// (nil → SecretDef.Value). The driver uses the resolved values at APPLY.
func ResolveContainerConfig(cfg *model.Config, c *model.Container, imgEnv []model.ImageEnv, imgVolumes []model.ImageVolume, ectx *expr.Context) *ContainerConfig {
	env, mounts := ContainerSources(cfg, c, imgEnv, imgVolumes, ectx.Secret)
	return &ContainerConfig{Env: env, Mounts: mounts, Resolution: expr.ResolveContainer(ectx, env, mounts)}
}

// ───────────────────────── files with expressions (render per container)

// NormalizeFileDef is TS normalizeFileDef: render = RenderMode().
func NormalizeFileDef(f model.FileDef) model.FileDef {
	f.Render = model.Some(f.RenderMode())
	loc := f.FileLocation()
	f.Location = &loc
	return f
}

// FileExprIssue is TS FileExprIssue (1-based line:col).
type FileExprIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Pos     int    `json:"pos"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
}

func fileIssue(src string, e *expr.Error) FileExprIssue {
	l, c := expr.LineCol(src, e.Pos)
	return FileExprIssue{Code: e.Code, Message: e.Message, Pos: e.Pos, Line: l, Col: c}
}

// CheckFileContent is TS checkFileContent: syntax, filters, roots and path shapes of every block (no values).
func CheckFileContent(src string) []FileExprIssue {
	out := []FileExprIssue{}
	segs, err := expr.ParseTemplate(src)
	if err != nil {
		return []FileExprIssue{fileIssue(src, asExprErr(err))}
	}
	cps := []rune(src)
	for _, s := range segs {
		if !s.IsBlock {
			continue
		}
		text := string(cps[s.Start:s.End])
		for _, e := range expr.CheckExpr(text, expr.FieldFile) {
			out = append(out, fileIssue(src, &expr.Error{Code: e.Code, Message: e.Message, Pos: s.Start + e.Pos}))
		}
	}
	return out
}

var jsonMimeRe = regexp.MustCompile(`(?i)^application/([a-z0-9.+-]*\+)?json$`)

// IsJSONMime is TS isJsonMime (application/json or +json).
func IsJSONMime(mime string) bool { return jsonMimeRe.MatchString(mime) }

// JSONError returns "" or the JSON parse error of text (V_FILE_JSON).
func JSONError(text string) string {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return err.Error()
	}
	return ""
}

// DuplicateContainer is TS duplicateContainer: a copy under a new name (host ports cleared, a volume named
// `<old>-<imageVolume>` becomes `<new>-<imageVolume>` (the caller declares it), every other source kept (shared), a
// depends on the new name dropped).
func DuplicateContainer(c model.Container, containerSlug string) model.Container {
	b, _ := json.Marshal(c)
	var cp model.Container
	_ = json.Unmarshal(b, &cp)
	cp.ContainerSlug = containerSlug
	for i := range cp.Ports {
		cp.Ports[i].HostPort = model.NumOrExpr{}
	}
	for i, v := range cp.Volumes {
		if v.ImageVolume != nil && v.Source.Kind == "volume" && v.Source.VolumeSlug == DefaultVolumeSlug(c.ContainerSlug, *v.ImageVolume) {
			cp.Volumes[i].Source = model.VolumeSource{Kind: "volume", VolumeSlug: DefaultVolumeSlug(containerSlug, *v.ImageVolume)}
		}
	}
	deps := []model.ContainerRef{}
	for _, d := range cp.Depends {
		if d.ContainerSlug != containerSlug {
			deps = append(deps, d)
		}
	}
	cp.Depends = deps
	return cp
}

// NormalizeRoundE returns a copy of cfg in the round E shape (TS normalizeSiteConfig): site volumes with kind (dir) and
// seed (null), edges without their decoded bytes (marshalled in the round E shape). The stored revision is untouched.
func NormalizeRoundE(cfg *model.Config) *model.Config {
	cp := *cfg
	cp.Sites = make([]model.Site, len(cfg.Sites))
	for i, s := range cfg.Sites {
		if s.Config.Volumes != nil {
			vols := append([]model.SiteVolume{}, (*s.Config.Volumes)...)
			for k := range vols {
				if vols[k].Kind == nil {
					d := "dir"
					vols[k].Kind = &d
				}
				if !vols[k].Seed.Set {
					vols[k].Seed = model.Null[model.VolumeSeed]()
				}
			}
			s.Config.Volumes = &vols
		}
		s.Config.Edge = s.Config.Edge.Normalized()
		cp.Sites[i] = s
	}
	return &cp
}
