package export

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"opm/agent/internal/canonical"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/volcontent"
)

// exporter holds one export run.
type exporter struct {
	req   *Request
	cfg   *model.Config // the request config with every secret value stripped (null)
	files map[string]File
	notes []Note
	env   []EnvVar
	steps map[string]*step // derived values recorded by the symbolic hook (every pass of every site)
}

// siteState is the classified result of one site (passes A + B) and what the machine-side scripts must do.
type siteState struct {
	site     string
	a, b     *pass
	used     map[string]bool // deferred inputs whose value changes an output (fingerprint passes)
	inputs   []deferredInput
	fileDef  map[string]bool // fileSlug → depends on a deferred input (template, rendered by setup.sh)
	edgeDef  bool
	seedDef  map[string]map[string]bool // volume → path → deferred
	names    map[string]bool            // every input / derived name the site's outputs reference
	installs []install                  // setup.sh: files placed under .runtime/ with their mode
	seeds    []seedPlan                 // init.sh: starter content of the volumes
	hashes   []fileHash                 // setup.sh: the hash of the files each service mounts (label files.sha256)
}

// fileHash: .env FILES_SHA_<SERVICE> = sha256 of the files (paths relative to the site dir) the service mounts.
type fileHash struct {
	Name  string
	Paths []string
}

// install is one file setup.sh writes: Src (git path, relative to the site dir) → Dst (under .runtime/), rendered when
// Template (@@{NAME}@@ placeholders) or copied byte for byte, then chmod Mode.
type install struct {
	Src, Dst string
	Template bool
	Mode     os.FileMode
}

func strip(cfg model.Config) *model.Config {
	c := cloneConfig(&cfg)
	for i := range c.Secrets {
		c.Secrets[i].Value = nil
	}
	return c
}

func newExporter(req *Request) (*exporter, error) {
	if req.Format != Format {
		return nil, fmt.Errorf("export format %d is not supported (this build: %d)", req.Format, Format)
	}
	if req.DeploymentID == "" {
		return nil, fmt.Errorf("deploymentId is empty")
	}
	r := *req
	r.Config = *strip(req.Config)
	if r.Texts == nil {
		r.Texts = map[string]string{}
	}
	if r.Images == nil {
		r.Images = map[string]ImageRef{}
	}
	if r.Persist == nil {
		r.Persist = map[string]map[string]PersistSnapshot{}
	}
	if r.SecretsSet == nil {
		r.SecretsSet = []string{}
	}
	if r.Catalog.Images == nil {
		r.Catalog.Images = map[string]model.ImageSpec{}
	}
	if r.Catalog.Services == nil {
		r.Catalog.Services = map[string]model.CatalogService{}
	}
	if r.Catalog.NotFoundPages == nil {
		r.Catalog.NotFoundPages = map[string]model.CatalogNotFoundPage{}
	}
	return &exporter{req: &r, cfg: &r.Config, files: map[string]File{}, steps: map[string]*step{}}, nil
}

func (x *exporter) note(site, code, msg string) {
	var s *string
	if site != "" {
		s = &site
	}
	x.notes = append(x.notes, Note{Code: code, Site: s, Message: x.clean(msg)})
}

// clean replaces sentinels in a message by ${NAME}.
func (x *exporter) clean(s string) string {
	if !strings.ContainsAny(s, "«‹") {
		return s
	}
	return canonicalSentinels(s)
}

func (x *exporter) text(path, content string, exec bool) {
	mode := "100644"
	if exec {
		mode = "100755"
	}
	t := content
	x.files[path] = File{Path: path, Mode: mode, Text: &t, Size: int64(len(content))}
}

func (x *exporter) bytesFile(path string, b []byte, exec bool) {
	if utf8.Valid(b) {
		x.text(path, string(b), exec)
		return
	}
	mode := "100644"
	if exec {
		mode = "100755"
	}
	e := base64.StdEncoding.EncodeToString(b)
	x.files[path] = File{Path: path, Mode: mode, Base64: &e, Size: int64(len(b))}
}

func (x *exporter) blob(path, sha string, size int64, exec bool) {
	mode := "100644"
	if exec {
		mode = "100755"
	}
	h := hexOf(sha)
	x.files[path] = File{Path: path, Mode: mode, Blob: &h, Size: size}
}

// prettyJSON is the canonical (JCS key order) JSON of v, indented by 2, with a trailing newline.
func prettyJSON(v any) (string, error) {
	s, err := canonical.Canonicalize(v)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return "", err
	}
	return buf.String() + "\n", nil
}

// sub: the compose form of a value resolved with sentinels A (a) and B (b): `$` escaped, sentinels → ${NAME:?}. ok =
// both forms agree (always, now that filters on deferred values are symbolic; false would mean an unknown dependency).
func (x *exporter) sub(st *siteState, a, b string) (string, bool) {
	namesIn(a, st.names)
	aa := reSentA.ReplaceAllString(strings.ReplaceAll(a, "$", "$$"), "$${$1:?run ./deploy.sh}")
	bb := reSentB.ReplaceAllString(strings.ReplaceAll(b, "$", "$$"), "$${$1:?run ./deploy.sh}")
	return aa, aa == bb
}

// template turns a pass-A output into a setup.sh template and checks it against pass B (same placeholders).
func (x *exporter) template(st *siteState, what string, a, b []byte, jsonCtx bool) string {
	t := tokens(string(a), jsonCtx)
	if b != nil {
		repl := "@@{$1}@@"
		if jsonCtx {
			repl = "@@{$1|json}@@"
		}
		if reSentB.ReplaceAllString(string(b), repl) != t {
			x.note(st.site, "VALUE_DERIVED", what+" depends on a machine value in a way the export cannot express: check it after ./deploy.sh")
		}
	}
	namesIn(string(a), st.names)
	return t
}

// Export runs the export.
func Export(req Request, mode Mode) (Result, error) {
	x, err := newExporter(&req)
	if err != nil {
		return Result{}, err
	}
	var out Result
	var ferr error
	withSymbolic(x.steps, func() {
		sites := append([]string(nil), x.req.Sites...)
		if len(sites) == 0 {
			for _, s := range x.cfg.Sites {
				sites = append(sites, s.SiteSlug)
			}
		}
		var states []*siteState
		for _, s := range sites {
			if siteOf(x.cfg, s) == nil {
				x.note(s, "SITE_MISSING", "site "+s+" is not in the config: not exported")
				continue
			}
			st := x.classify(s)
			x.siteTree(st)
			states = append(states, st)
		}
		ferr = x.common(states)
	})
	if ferr != nil {
		return Result{}, ferr
	}
	out = Result{Format: Format, Files: make([]File, 0, len(x.files)), Notes: x.notesOut(), Env: x.env}
	for _, f := range x.files {
		out.Files = append(out.Files, f)
	}
	sort.Slice(out.Files, func(i, j int) bool { return volcontent.CompareUTF16(out.Files[i].Path, out.Files[j].Path) < 0 })
	if out.Env == nil {
		out.Env = []EnvVar{}
	}
	return out, nil
}

func (x *exporter) notesOut() []Note {
	if x.notes == nil {
		return []Note{}
	}
	// stable + unique
	seen := map[string]bool{}
	var out []Note
	for _, n := range x.notes {
		k := n.Code + "|" + n.Message
		if n.Site != nil {
			k += "|" + *n.Site
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, n)
		}
	}
	return out
}

// classify runs passes A and B and finds what depends on which deferred input.
func (x *exporter) classify(site string) *siteState {
	st := &siteState{site: site, used: map[string]bool{}, fileDef: map[string]bool{}, seedDef: map[string]map[string]bool{}, names: map[string]bool{}}
	st.inputs = x.inputs(site)
	va := x.valuesFor(site, sentinelA)
	vb := x.valuesFor(site, sentinelB)
	st.a = x.run(site, va)
	st.b = x.run(site, vb)
	fa := st.a.fingerprint()
	for _, d := range st.inputs {
		m := map[string]string{}
		for k, v := range va {
			m[k] = v
		}
		m[d.Name] = vb[d.Name]
		if x.run(site, m).fingerprint() != fa {
			st.used[d.Name] = true
		}
	}
	for slug, fa := range st.a.files {
		if fb, ok := st.b.files[slug]; !ok || fb.Sha != fa.Sha {
			st.fileDef[slug] = true
		}
	}
	st.edgeDef = st.a.edgeJSON != st.b.edgeJSON
	for vol, m := range st.a.seeds {
		for p, ba := range m {
			if string(st.b.seeds[vol][p]) != string(ba) {
				if st.seedDef[vol] == nil {
					st.seedDef[vol] = map[string]bool{}
				}
				st.seedDef[vol][p] = true
			}
		}
	}
	return st
}

func envMap(list []string) map[string]string {
	m := map[string]string{}
	for _, kv := range list {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fmtSecs(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "s" }

func execBits(mode string) bool {
	v, err := strconv.ParseUint(mode, 8, 32)
	return err == nil && v&0o111 != 0
}

// volumeBind is where a declared volume lives in the compose project: a named volume (dir + auto) or a bind path.
type volumeBind struct {
	named    bool
	source   string // named: volume key; bind: host path (relative to the site dir or absolute)
	fileKind bool
}

func volumeBindOf(v *model.SiteVolume) volumeBind {
	loc := v.VolumeLocation()
	file := v.VolumeKind() == "file"
	if loc.Kind == "path" {
		p := loc.Path
		if strings.HasPrefix(p, "./") {
			p = "./.data/" + strings.TrimPrefix(p, "./")
		}
		return volumeBind{source: p, fileKind: file}
	}
	if file {
		return volumeBind{source: "./.data/volumes/" + v.VolumeSlug, fileKind: true}
	}
	return volumeBind{named: true, source: v.VolumeSlug}
}

// initVolumeTarget is where init.sh sees a declared volume: folder volumes at /vol/<slug>; single-file volumes at
// /data/… (relative paths) or /vol/<slug>.parent/<base> (absolute paths: the parent folder is mounted).
func initVolumeTarget(v *model.SiteVolume) (dir string, file string) {
	vb := volumeBindOf(v)
	if !vb.fileKind {
		return "/vol/" + v.VolumeSlug, ""
	}
	if !strings.HasPrefix(vb.source, "/") {
		return "", "/data/" + strings.TrimPrefix(vb.source, "./.data/")
	}
	base := vb.source[strings.LastIndex(vb.source, "/")+1:]
	return "", "/vol/" + v.VolumeSlug + ".parent/" + base
}

func parentOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

// siteTree writes sites/<site>/… (compose, scripts, site.env.example, files, volumes, edge, README).
func (x *exporter) siteTree(st *siteState) {
	site := st.site
	dir := "sites/" + site + "/"
	sc := siteOf(st.a.cfg, site)
	for c, msg := range st.a.buildErr {
		x.note(site, "BUILD_FAILED", fmt.Sprintf("container %s is not exported: %s", c, msg))
	}
	for v, msg := range st.a.seedErr {
		x.note(site, "SEED_FAILED", fmt.Sprintf("starter content of volume %s: %s", v, msg))
	}
	if st.a.edgeErr != "" {
		x.note(site, "EDGE_FAILED", "edge not exported: "+st.a.edgeErr)
	}
	for _, n := range st.a.edgeNote {
		x.note(site, n.Code, n.Message)
	}
	// files: in git as files/<slug> (or files/<slug>.template when it uses a machine value); setup.sh places every one
	// under .runtime/files/ with the agent's mode (0444, 0400 when a secret is in clear)
	for _, slug := range sortedKeys(st.a.files) {
		f := st.a.files[slug]
		var def *model.FileDef
		for i := range x.cfg.Files {
			if x.cfg.Files[i].FileSlug == slug {
				def = &x.cfg.Files[i]
			}
		}
		if def == nil || def.Content == nil {
			continue
		}
		if def.Content.SizeBytes > MaxFileBytes {
			x.note(site, "SKIPPED_LARGE", fmt.Sprintf("file %s (%d bytes) is larger than 100 MB: not pushed", slug, def.Content.SizeBytes))
			continue
		}
		if def.FileLocation().Kind == "path" {
			x.note(site, "FILE_LOCATION", fmt.Sprintf("file %s has the location %s on the agent; the compose export mounts it from .runtime/files", slug, def.FileLocation().Path))
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o444
		}
		switch {
		case st.fileDef[slug]:
			var bb []byte
			if fb, ok := st.b.files[slug]; ok {
				bb = fb.Bytes
			}
			x.text(dir+"files/"+slug+".template", x.template(st, "file "+slug, f.Bytes, bb, false), false)
			st.installs = append(st.installs, install{Src: "files/" + slug + ".template", Dst: "files/" + slug, Template: true, Mode: mode})
		case f.Src != "":
			x.blob(dir+"files/"+slug, def.Content.Sha256, def.Content.SizeBytes, false)
			st.installs = append(st.installs, install{Src: "files/" + slug, Dst: "files/" + slug, Mode: mode})
		default:
			x.bytesFile(dir+"files/"+slug, f.Bytes, false)
			st.installs = append(st.installs, install{Src: "files/" + slug, Dst: "files/" + slug, Mode: mode})
		}
	}
	// secrets mounted as files: .runtime/secrets/<slug> (0400) = the secret's value from site.env
	secretMounts := map[string]bool{}
	for _, b := range st.a.builts {
		if b == nil {
			continue
		}
		for _, m := range b.Spec.Mounts {
			if m.Origin.Kind == "secret" {
				secretMounts[m.Origin.Slug] = true
			}
		}
	}
	for _, slug := range sortedKeys(secretMounts) {
		n := envName("secret", slug)
		st.names[n] = true
		x.text(dir+"templates/secrets/"+slug+".template", "@@{"+n+"}@@", false)
		st.installs = append(st.installs, install{Src: "templates/secrets/" + slug + ".template", Dst: "secrets/" + slug, Template: true, Mode: 0o400})
	}
	// starter content
	for _, v := range sc.Config.VolumeList() {
		v := v
		kind, entries, dirs, arch := x.seedSource(site, &v)
		dirT, fileT := initVolumeTarget(&v)
		plan := seedPlan{Volume: v.VolumeSlug, FileKind: v.VolumeKind() == "file", DirPath: dirT, FilePath: fileT, Source: kind, Dirs: dirs}
		var skipped []string
		switch kind {
		case "tree", "snapshot":
			for _, e := range entries {
				if e.size > MaxFileBytes {
					skipped = append(skipped, fmt.Sprintf("%s\t%d bytes\tlarger than 100 MB", e.path, e.size))
					continue
				}
				p := dir + "volumes/" + v.VolumeSlug + "/" + e.path
				sf := seedFile{Path: e.path, Mode: parseMode(e.mode)}
				if e.render == "expressions" {
					if st.seedDef[v.VolumeSlug][e.path] {
						rel := "volumes/" + v.VolumeSlug + "/" + e.path
						x.text(dir+"templates/"+rel+".template", x.template(st, "starter file "+v.VolumeSlug+"/"+e.path, st.a.seeds[v.VolumeSlug][e.path], st.b.seeds[v.VolumeSlug][e.path], false), false)
						st.installs = append(st.installs, install{Src: "templates/" + rel + ".template", Dst: "seed/" + v.VolumeSlug + "/" + e.path, Template: true, Mode: sf.Mode})
						sf.Rendered = true
					} else if b, ok := st.a.seeds[v.VolumeSlug][e.path]; ok {
						x.bytesFile(p, b, execBits(e.mode))
					}
				} else {
					x.blob(p, e.sha, e.size, execBits(e.mode))
				}
				plan.Files = append(plan.Files, sf)
			}
		case "single":
			e := entries[0]
			p := dir + "volumes/" + v.VolumeSlug
			sf := seedFile{Mode: parseMode(e.mode)}
			if e.render == "expressions" {
				if st.seedDef[v.VolumeSlug][""] {
					x.text(dir+"templates/volumes/"+v.VolumeSlug+".template", x.template(st, "starter file of "+v.VolumeSlug, st.a.seeds[v.VolumeSlug][""], st.b.seeds[v.VolumeSlug][""], false), false)
					st.installs = append(st.installs, install{Src: "templates/volumes/" + v.VolumeSlug + ".template", Dst: "seed/" + v.VolumeSlug, Template: true, Mode: sf.Mode})
					sf.Rendered = true
				} else if b, ok := st.a.seeds[v.VolumeSlug][""]; ok {
					x.bytesFile(p, b, execBits(e.mode))
				}
				plan.Files = append(plan.Files, sf)
			} else if e.size <= MaxFileBytes {
				x.blob(p, e.sha, e.size, execBits(e.mode))
				plan.Files = append(plan.Files, sf)
			} else {
				skipped = append(skipped, fmt.Sprintf("(single file)\t%d bytes\tlarger than 100 MB", e.size))
			}
		case "archive":
			if arch.Size <= MaxFileBytes {
				x.blob(dir+"volumes/"+v.VolumeSlug+".seed.tar.gz", arch.BlobSha256, arch.Size, false)
				plan.Archive = "volumes/" + v.VolumeSlug + ".seed.tar.gz"
			} else {
				skipped = append(skipped, fmt.Sprintf("%s\t%d bytes\tlarger than 100 MB", arch.Name, arch.Size))
			}
		}
		if len(skipped) > 0 {
			x.text(dir+"volumes/"+v.VolumeSlug+".skipped.txt", "# files of volume "+v.VolumeSlug+" not pushed to git (GitHub refuses files > 100 MB)\n"+strings.Join(skipped, "\n")+"\n", false)
			x.note(site, "SKIPPED_LARGE", fmt.Sprintf("volume %s: %d file(s) larger than 100 MB not pushed", v.VolumeSlug, len(skipped)))
			plan.Skipped = len(skipped)
		}
		if kind == "snapshot" {
			s := x.req.Persist[site][v.VolumeSlug]
			x.note(site, "PERSIST_SNAPSHOT", fmt.Sprintf("volume %s starts with the persist-to-cloud snapshot #%d (%s)", v.VolumeSlug, s.Seq, s.CreatedAt))
		}
		if len(st.seedDef[v.VolumeSlug]) > 0 {
			x.note(site, "SEED_RENDERED_AT_DEPLOY", fmt.Sprintf("volume %s: %d starter file(s) use machine values and are rendered by ./deploy.sh", v.VolumeSlug, len(st.seedDef[v.VolumeSlug])))
		}
		st.seeds = append(st.seeds, plan)
	}
	// edge
	if st.a.edge != nil {
		if st.edgeDef {
			sb, _ := canonicalBytes(st.b.edge)
			sa, _ := prettyJSON(st.a.edge)
			pb := ""
			if sb != nil {
				pb, _ = prettyJSON(st.b.edge)
			}
			x.text(dir+"edge/caddy.template.json", x.template(st, "the edge config", []byte(sa), []byte(pb), true), false)
			st.installs = append(st.installs, install{Src: "edge/caddy.template.json", Dst: "edge/caddy.json", Template: true, Mode: 0o444})
		} else if s, err := prettyJSON(st.a.edge); err == nil {
			x.text(dir+"edge/caddy.json", s, false)
		}
		x.text(dir+"edge/pages/404.html", x.clean(st.a.page404), false)
		x.text(dir+"edge/pages/503.html", x.clean(st.a.page503), false)
		for _, h := range sortedKeys(st.a.tls) {
			pa, pb := st.a.tls[h], st.b.tls[h]
			x.text(dir+"edge/tls/"+h+".crt.template", x.template(st, "the TLS certificate of "+h, []byte(pa[0]), []byte(pb[0]), false), false)
			x.text(dir+"edge/tls/"+h+".key.template", x.template(st, "the TLS key of "+h, []byte(pa[1]), []byte(pb[1]), false), false)
			st.installs = append(st.installs,
				install{Src: "edge/tls/" + h + ".crt.template", Dst: "tls/" + h + ".crt", Template: true, Mode: 0o444},
				install{Src: "edge/tls/" + h + ".key.template", Dst: "tls/" + h + ".key", Template: true, Mode: 0o400})
		}
		if df := dnsDockerfile(sc); df != "" {
			x.text(dir+"edge/Dockerfile", df, false)
		}
	}
	x.text(dir+"docker-compose.yml", x.compose(st, sc), false)
	// what site.env must hold: the deferred inputs the outputs use (directly or through derived values)
	steps := x.closure(st.names)
	need := map[string]bool{}
	for n := range st.used {
		need[n] = true
	}
	for n := range st.names {
		if x.steps[n] == nil {
			need[n] = true
		}
	}
	for _, s := range steps {
		for _, r := range refsOf(s.Input) {
			if x.steps[r] == nil {
				need[r] = true
			}
		}
	}
	var required []deferredInput
	for _, d := range st.inputs {
		if need[d.Name] {
			required = append(required, d)
		}
	}
	for _, s := range steps {
		x.text(dir+"templates/steps/"+s.Name+".in", tokensCanonical(s.Input), false)
	}
	x.text(dir+"site.env.example", x.siteEnvExample(st, required), false)
	x.text(dir+"setup.sh", x.setupScript(st, required, steps), true)
	x.text(dir+"init.sh", x.initScript(st), true)
	x.text(dir+"deploy.sh", deployScript, true)
	x.text(dir+"README.md", x.siteReadme(st, required), false)
	for _, d := range required {
		x.env = append(x.env, EnvVar{Name: d.Name, Site: site, Kind: d.Kind, Required: true, Description: d.Description})
	}
	x.env = append(x.env,
		EnvVar{Name: "PORTAINER_ADMIN_PASSWORD", Site: site, Kind: "other", Required: true, Description: "password of the Portainer admin user (12 characters or more; PORTAINER=off skips Portainer)"},
		EnvVar{Name: "OPM_EDGE_HTTP_PORT", Site: site, Kind: "edge-port", Required: false, Description: "host port of the edge (default: the config's edge httpPort)"})
	if st.a.httpsPort != 0 {
		x.env = append(x.env, EnvVar{Name: "OPM_EDGE_HTTPS_PORT", Site: site, Kind: "edge-port", Required: false, Description: "host port of the edge TLS listener (default: the config's edge httpsPort)"})
	}
}

func canonicalBytes(v map[string]any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	s, err := canonical.Canonicalize(v)
	return []byte(s), err
}

// tokensCanonical: a step input (${NAME} placeholders) as a setup.sh template.
func tokensCanonical(s string) string { return reRef.ReplaceAllString(s, "@@{$1}@@") }

func parseMode(m string) os.FileMode {
	var v uint32
	if _, err := fmt.Sscanf(m, "%o", &v); err != nil || !volcontent.ReFileMode.MatchString(m) {
		return 0o644
	}
	return os.FileMode(v)
}

// dnsDockerfile: a caddy build with the caddy-dns modules the site's ACME dns-01 hosts need ("" = none).
func dnsDockerfile(sc *model.Site) string {
	provs := map[string]bool{}
	for _, h := range sc.Config.Edge.Hosts {
		if h.TLS.Mode == "acme" && h.TLS.Challenge == "dns-01" && h.TLS.DNSProvider != nil {
			provs[*h.TLS.DNSProvider] = true
		}
	}
	if len(provs) == 0 {
		return ""
	}
	var with []string
	for _, p := range sortedKeys(provs) {
		with = append(with, "--with github.com/caddy-dns/"+p)
	}
	return "# the official caddy image + the DNS provider modules of the ACME dns-01 hosts\n" +
		"FROM caddy:2.11.7-builder AS build\nRUN xcaddy build " + strings.Join(with, " ") + "\n" +
		"FROM " + EdgeImage + "\nCOPY --from=build /usr/bin/caddy /usr/bin/caddy\n"
}

func (x *exporter) siteEnvExample(st *siteState, required []deferredInput) string {
	var b strings.Builder
	b.WriteString("# Site " + st.site + ": the values that are not in git (secrets, this machine's address, ...).\n")
	b.WriteString("#   cp site.env.example site.env, fill every value, then ./deploy.sh   (site.env is never committed)\n")
	b.WriteString("# One NAME=value per line; the value is taken exactly as written (no quotes needed).\n")
	for _, d := range required {
		b.WriteString("\n")
		for _, l := range strings.Split(strings.TrimSpace(d.Kind+": "+d.Description), "\n") {
			b.WriteString("# " + l + "\n")
		}
		b.WriteString(d.Name + "=\n")
	}
	b.WriteString("\n# Portainer, the Docker GUI of this machine: https://<this machine>:9443, user admin.\n")
	b.WriteString("# The admin password (12 characters or more). Set PORTAINER=off to run without Portainer.\n")
	b.WriteString("PORTAINER_ADMIN_PASSWORD=\n")
	b.WriteString("# PORTAINER_HTTPS_PORT=9443\n")
	b.WriteString("# PORTAINER=off\n")
	b.WriteString("\n# optional: publish the edge on other host ports\n")
	b.WriteString(fmt.Sprintf("# OPM_EDGE_HTTP_PORT=%d\n", portOr(st.a.httpPort, 80)))
	if st.a.httpsPort != 0 {
		b.WriteString(fmt.Sprintf("# OPM_EDGE_HTTPS_PORT=%d\n", st.a.httpsPort))
	}
	return b.String()
}

func portOr(p, d int) int {
	if p == 0 {
		return d
	}
	return p
}

// common writes the repository-level files.
func (x *exporter) common(states []*siteState) error {
	x.text(".gitignore", "# per-machine values and runtime state never belong in git\n**/site.env\n**/.env\n**/.runtime/\n**/.data/\n", false)
	x.text("README.md", x.readme(states), false)
	return nil
}

// keep the imports used by helpers in other files of the package
var _ = driver.NamesPlain
