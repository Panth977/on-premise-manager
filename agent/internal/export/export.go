package export

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"opm/agent/internal/canonical"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
	"opm/agent/internal/volcontent"
)

// exporter holds one export run.
type exporter struct {
	req   *Request
	cfg   *model.Config // the request config with every secret value stripped (null)
	files map[string]File
	notes []Note
	env   []EnvVar
}

// siteState is the classified result of one site (passes A + B, used inputs).
type siteState struct {
	site     string
	a, b     *pass
	used     map[string]bool
	inputs   []deferredInput
	derived  map[string][]string // containerSlug → derived env names
	fileDef  map[string]bool     // fileSlug → deferred (rendered on the VM)
	edgeDef  bool
	seedDef  map[string]map[string]bool // volume → path → deferred
	services []string
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
	return &exporter{req: &r, cfg: &r.Config, files: map[string]File{}}, nil
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
	for _, site := range x.req.Sites {
		for _, d := range x.inputs(site) {
			s = strings.ReplaceAll(s, sentinelA(d.Name), "${"+d.Name+"}")
			s = strings.ReplaceAll(s, sentinelB(d.Name), "${"+d.Name+"}")
		}
	}
	return s
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

// sub: the compose form of a value resolved with sentinels A (a) and B (b): `$` escaped, sentinels → ${NAME:?}; ok =
// both forms agree (no deferred input, or a pure substitution).
func (x *exporter) sub(st *siteState, a, b string) (string, bool) {
	aa := strings.ReplaceAll(a, "$", "$$")
	bb := strings.ReplaceAll(b, "$", "$$")
	for _, d := range st.inputs {
		aa = strings.ReplaceAll(aa, sentinelA(d.Name), "${"+d.Name+":?}")
		bb = strings.ReplaceAll(bb, sentinelB(d.Name), "${"+d.Name+":?}")
	}
	return aa, aa == bb
}

// Export runs the export (see Mode).
func Export(req Request, mode Mode) (Result, error) {
	x, err := newExporter(&req)
	if err != nil {
		return Result{}, err
	}
	if mode.Runtime {
		files, err := x.runtimeFiles(mode.Site, mode.Values)
		if err != nil {
			return Result{}, err
		}
		return Result{Format: Format, Files: files, Notes: x.notesOut(), Env: []EnvVar{}}, nil
	}
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
	if err := x.common(states); err != nil {
		return Result{}, err
	}
	out := Result{Format: Format, Files: make([]File, 0, len(x.files)), Notes: x.notesOut(), Env: x.env}
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
	st := &siteState{site: site, used: map[string]bool{}, derived: map[string][]string{}, fileDef: map[string]bool{}, seedDef: map[string]map[string]bool{}}
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
	for i, ba := range st.a.builts {
		bb := st.b.builts[i]
		if ba == nil || bb == nil {
			continue
		}
		ea, eb := envMap(ba.Spec.Env), envMap(bb.Spec.Env)
		for _, k := range sortedKeys(ea) {
			if _, ok := x.sub(st, ea[k], eb[k]); !ok {
				st.derived[ba.ContainerSlug] = append(st.derived[ba.ContainerSlug], k)
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
			p = "./.opm-data/" + strings.TrimPrefix(p, "./")
		}
		return volumeBind{source: p, fileKind: file}
	}
	if file {
		return volumeBind{source: "./.opm-data/volumes/" + v.VolumeSlug, fileKind: true}
	}
	return volumeBind{named: true, source: v.VolumeSlug}
}

// InitVolumeTarget is where opm-init sees a declared volume: dir volumes at /opm-vol/<slug>; single-file volumes at
// /opm-vol/<slug>.parent/<base> (the parent folder is mounted; auto = /opm-data/volumes/<slug>).
func InitVolumeTarget(v *model.SiteVolume) (dir string, file string) {
	vb := volumeBindOf(v)
	if !vb.fileKind {
		return "/opm-vol/" + v.VolumeSlug, ""
	}
	if !strings.HasPrefix(vb.source, "/") {
		return "", "/opm-data/" + strings.TrimPrefix(vb.source, "./.opm-data/")
	}
	base := vb.source[strings.LastIndex(vb.source, "/")+1:]
	return "", "/opm-vol/" + v.VolumeSlug + ".parent/" + base
}

func parentOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

// siteTree writes sites/<site>/… (compose, .env.example, files, volumes, edge).
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
	// files
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
			x.note(site, "FILE_LOCATION", fmt.Sprintf("file %s has the location %s on the agent; the compose export mounts it from ./files", slug, def.FileLocation().Path))
		}
		switch {
		case st.fileDef[slug]:
			if t, ok := x.req.Texts[hexOf(def.Content.Sha256)]; ok {
				x.text(dir+"files/"+slug+".opm-template", t, false)
			}
		case f.Src != "":
			x.blob(dir+"files/"+slug, def.Content.Sha256, def.Content.SizeBytes, false)
		default:
			x.bytesFile(dir+"files/"+slug, f.Bytes, false)
		}
	}
	// starter content
	for _, v := range sc.Config.VolumeList() {
		v := v
		kind, entries, _, arch := x.seedSource(site, &v)
		var skipped []string
		switch kind {
		case "tree", "snapshot":
			for _, e := range entries {
				if e.size > MaxFileBytes {
					skipped = append(skipped, fmt.Sprintf("%s\t%d bytes\tlarger than 100 MB", e.path, e.size))
					continue
				}
				p := dir + "volumes/" + v.VolumeSlug + "/" + e.path
				if e.render == "expressions" {
					if st.seedDef[v.VolumeSlug][e.path] {
						continue // rendered on the VM (opm-render init)
					}
					if b, ok := st.a.seeds[v.VolumeSlug][e.path]; ok {
						x.bytesFile(p, b, execBits(e.mode))
					}
					continue
				}
				x.blob(p, e.sha, e.size, execBits(e.mode))
			}
		case "single":
			e := entries[0]
			p := dir + "volumes/" + v.VolumeSlug
			if e.render == "expressions" {
				if !st.seedDef[v.VolumeSlug][""] {
					if b, ok := st.a.seeds[v.VolumeSlug][""]; ok {
						x.bytesFile(p, b, execBits(e.mode))
					}
				}
			} else if e.size <= MaxFileBytes {
				x.blob(p, e.sha, e.size, execBits(e.mode))
			} else {
				skipped = append(skipped, fmt.Sprintf("(single file)\t%d bytes\tlarger than 100 MB", e.size))
			}
		case "archive":
			if arch.Size <= MaxFileBytes {
				x.blob(dir+"volumes/"+v.VolumeSlug+".opm-seed.tar.gz", arch.BlobSha256, arch.Size, false)
			} else {
				skipped = append(skipped, fmt.Sprintf("%s\t%d bytes\tlarger than 100 MB", arch.Name, arch.Size))
			}
		}
		if len(skipped) > 0 {
			x.text(dir+"volumes/"+v.VolumeSlug+".opm-skipped.txt", "# files of volume "+v.VolumeSlug+" not pushed to git (GitHub refuses files > 100 MB)\n"+strings.Join(skipped, "\n")+"\n", false)
			x.note(site, "SKIPPED_LARGE", fmt.Sprintf("volume %s: %d file(s) larger than 100 MB not pushed", v.VolumeSlug, len(skipped)))
		}
		if kind == "snapshot" {
			s := x.req.Persist[site][v.VolumeSlug]
			x.note(site, "PERSIST_SNAPSHOT", fmt.Sprintf("volume %s starts with the persist-to-cloud snapshot #%d (%s)", v.VolumeSlug, s.Seq, s.CreatedAt))
		}
		if len(st.seedDef[v.VolumeSlug]) > 0 {
			x.note(site, "SEED_RENDERED_AT_UP", fmt.Sprintf("volume %s: %d starter file(s) use .env values and are rendered by opm-init", v.VolumeSlug, len(st.seedDef[v.VolumeSlug])))
		}
	}
	// edge
	if st.a.edge != nil {
		if st.edgeDef {
			disp := x.run(site, x.valuesFor(site, displayVal))
			if s, err := prettyJSON(disp.edge); err == nil {
				x.text(dir+"edge/caddy.template.json", s, false)
			}
			x.note(site, "EDGE_RENDERED_AT_UP", "the edge config uses .env values: opm-init writes .opm-runtime/edge/caddy.json (edge/caddy.template.json shows it with ${NAME} placeholders)")
		} else if s, err := prettyJSON(st.a.edge); err == nil {
			x.text(dir+"edge/caddy.json", s, false)
		}
		x.text(dir+"edge/pages/404.html", x.clean(st.a.page404), false)
		x.text(dir+"edge/pages/503.html", x.clean(st.a.page503), false)
		if df := dnsDockerfile(sc); df != "" {
			x.text(dir+"edge/Dockerfile", df, false)
		}
	}
	x.text(dir+"docker-compose.yml", x.compose(st, sc), false)
	x.text(dir+".env.example", x.envExample(st), false)
	for _, d := range st.inputs {
		if !st.used[d.Name] {
			continue
		}
		x.env = append(x.env, EnvVar{Name: d.Name, Site: site, Kind: d.Kind, Required: true, Description: d.Description})
	}
	x.env = append(x.env, EnvVar{Name: "OPM_EDGE_HTTP_PORT", Site: site, Kind: "edge-port", Required: false, Description: "host port of the edge (default: the config's edge httpPort)"})
	if st.a.httpsPort != 0 {
		x.env = append(x.env, EnvVar{Name: "OPM_EDGE_HTTPS_PORT", Site: site, Kind: "edge-port", Required: false, Description: "host port of the edge TLS listener (default: the config's edge httpsPort)"})
	}
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
	return "# OPM compose export: the official caddy image + the DNS provider modules of the ACME dns-01 hosts\n" +
		"FROM caddy:2.11.7-builder AS build\nRUN xcaddy build " + strings.Join(with, " ") + "\n" +
		"FROM " + EdgeImage + "\nCOPY --from=build /usr/bin/caddy /usr/bin/caddy\n"
}

func (x *exporter) envExample(st *siteState) string {
	var b strings.Builder
	b.WriteString("# OPM compose export — the values of site " + st.site + " that are not in git.\n")
	b.WriteString("# cp .env.example .env, fill every value, then: docker compose up -d   (never commit .env)\n")
	any := false
	for _, d := range st.inputs {
		if !st.used[d.Name] {
			continue
		}
		any = true
		b.WriteString("\n")
		for _, l := range strings.Split(strings.TrimSpace(d.Kind+": "+d.Description), "\n") {
			b.WriteString("# " + l + "\n")
		}
		b.WriteString(d.Name + "=\n")
	}
	if !any {
		b.WriteString("\n# (this site needs no values)\n")
	}
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
	x.text(".gitignore", "# OPM compose export: per-machine values and runtime state never belong in git\n**/.env\n**/.opm-runtime/\n**/.opm-data/\n", false)
	var hash any
	if h, err := canonical.HashCanonical(x.req.Catalog); err == nil {
		hash = h
	}
	cfgDoc := map[string]any{"format": Format, "branch": x.req.Branch, "commit": x.req.Commit, "config": x.cfg, "catalogHash": hash}
	s, err := prettyJSON(cfgDoc)
	if err != nil {
		return err
	}
	x.text("opm/config.json", s, false)
	s, err = prettyJSON(x.req)
	if err != nil {
		return err
	}
	x.text("opm/export.json", s, false)
	x.text("opm/bin/opm-render", launcher, true)
	for _, a := range []struct {
		arch string
		bin  *ToolBin
	}{{"amd64", x.req.Tool.Amd64}, {"arm64", x.req.Tool.Arm64}} {
		if a.bin == nil {
			x.note("", "TOOL_MISSING", "opm-render for linux/"+a.arch+" is not in this export: opm-init cannot run on that architecture")
			continue
		}
		t := "linux-" + a.arch
		x.files["opm/bin/opm-render-linux-"+a.arch] = File{Path: "opm/bin/opm-render-linux-" + a.arch, Mode: "100755", Tool: &t, Size: a.bin.Size}
	}
	x.text("README.md", x.readme(states), false)
	return nil
}

const launcher = `#!/bin/sh
# OPM compose export: runs the opm-render binary of this machine's architecture (used by the opm-init service).
d=$(dirname "$0")
case "$(uname -m)" in
  x86_64|amd64) a=amd64 ;;
  aarch64|arm64) a=arm64 ;;
  *) echo "opm-render: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
exec "$d/opm-render-linux-$a" "$@"
`

// keep the imports used by helpers in other files of the package
var _ = validate.ImageKey
var _ = driver.NamesPlain
