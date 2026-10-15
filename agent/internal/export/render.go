package export

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"opm/agent/internal/canonical"
	"opm/agent/internal/driver"
	"opm/agent/internal/edgecfg"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// The deferred inputs of a site (values that exist only on the VM) and one render pass with a given value for each.
//
// A pass resolves EVERYTHING the agent would resolve (driver.Build per container, the files rendered once per site, the
// starter content, the edge) with a value per deferred input: sentinel set A, sentinel set B (different text, length and
// case), the display form "${NAME}", or the real values (runtime). An output that is the same in A and B does not
// depend on a deferred input; an env / compose value whose A and B forms become equal once every sentinel is replaced by
// ${NAME} is a pure substitution (compose interpolation); anything else is rendered on the VM by opm-render init.

// deferredInput is one .env value.
type deferredInput struct {
	Name, Kind, Slug, Description string
}

func envName(kind, slug string) string {
	up := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(slug))
	switch kind {
	case "secret":
		return "OPM_SECRET_" + up
	case "var":
		return "OPM_VAR_" + up
	case "site-host":
		return "OPM_SITE_" + up + "_HOST"
	}
	return up
}

// SiteHostEnv is this site's own address when its host is auto.
const SiteHostEnv = "OPM_SITE_HOST"

func sentinelA(name string) string  { return "«OpmA:" + name + ":aMpO»" }
func sentinelB(name string) string  { return "‹oPMb~" + name + "~bmPo~›" }
func displayVal(name string) string { return "${" + name + "}" }

// cloneConfig deep-copies a config (JSON round trip keeps every optional key exactly).
func cloneConfig(c *model.Config) *model.Config {
	b, _ := json.Marshal(c)
	var out model.Config
	_ = json.Unmarshal(b, &out)
	return &out
}

func siteOf(cfg *model.Config, slug string) *model.Site {
	for i := range cfg.Sites {
		if cfg.Sites[i].SiteSlug == slug {
			return &cfg.Sites[i]
		}
	}
	return nil
}

func hexOf(sha string) string { return strings.TrimPrefix(sha, "sha256:") }

// inputs lists the deferred inputs of a site: every secret, the site hosts that are auto, required variables without a
// value (templates).
func (x *exporter) inputs(site string) []deferredInput {
	var out []deferredInput
	for _, s := range x.cfg.Secrets {
		d := s.Description
		if d == "" {
			d = s.Label
		}
		if s.Generate != nil {
			d = strings.TrimSpace(d + fmt.Sprintf(" (generated in OPM: %s, %d characters — e.g. openssl rand -hex %d)", s.Generate.Charset, s.Generate.Length, (s.Generate.Length+1)/2))
		}
		out = append(out, deferredInput{envName("secret", s.SecretSlug), "secret", s.SecretSlug, d})
	}
	hosts := validate.ResolveSiteHosts(x.cfg, x.req.DeploymentID, validate.HostOptions{Detected: map[string]string{}})
	for _, s := range x.cfg.Sites {
		if h := hosts[s.SiteSlug]; h.Auto {
			if s.SiteSlug == site {
				out = append(out, deferredInput{SiteHostEnv, "site-host", s.SiteSlug, "the address of THIS machine (site " + s.SiteSlug + " has host auto): IP or DNS name other sites and users reach it on"})
			} else {
				out = append(out, deferredInput{envName("site-host", s.SiteSlug), "site-host", s.SiteSlug, "the address of site " + s.SiteSlug + " (host auto in OPM)"})
			}
		}
	}
	for _, v := range x.cfg.Variables {
		if str, ok := v.Value.V.(string); v.Required && (v.Value.V == nil || (ok && str == "")) {
			d := v.Description
			if d == "" {
				d = v.Label
			}
			out = append(out, deferredInput{envName("var", v.VariableSlug), "var", v.VariableSlug, strings.TrimSpace(d + " (required variable, set at deployment; type " + v.Type + ")")})
		}
	}
	return out
}

// pass is the resolved state of one site with one set of values.
type pass struct {
	values    map[string]string // name → value
	cfg       *model.Config
	builts    []*driver.Built // in the site's container order (nil entry = build failed)
	buildErr  map[string]string
	files     map[string]driver.SiteFile // fileSlug → rendered (Bytes) / blob (Src) copy
	seeds     map[string]map[string][]byte
	seedErr   map[string]string
	edge      map[string]any
	edgeJSON  string
	edgeErr   string
	edgeNote  []edgecfg.Note
	tls       map[string][2]string // host → cert, key
	page404   string
	page503   string
	httpPort  int
	httpsPort int // 0 = no TLS listener
}

func (x *exporter) valuesFor(site string, f func(name string) string) map[string]string {
	out := map[string]string{}
	for _, d := range x.inputs(site) {
		out[d.Name] = f(d.Name)
	}
	return out
}

func (x *exporter) catalogServices() map[string]string {
	m := map[string]string{}
	for k := range x.req.Catalog.Services {
		m[k] = k
	}
	// images of the catalog also name their service (a catalog without services entries still builds)
	for _, im := range x.req.Catalog.Images {
		m[im.ServiceSlug] = im.ServiceSlug
	}
	return m
}

func (x *exporter) readBlob(hex string) ([]byte, error) {
	if t, ok := x.req.Texts[hex]; ok {
		return []byte(t), nil
	}
	return nil, fmt.Errorf("content sha256:%s is not in the export (texts)", hex)
}

// run resolves one site with the given values.
func (x *exporter) run(site string, values map[string]string) *pass {
	p := &pass{values: values, buildErr: map[string]string{}, files: map[string]driver.SiteFile{}, seeds: map[string]map[string][]byte{}, seedErr: map[string]string{}, tls: map[string][2]string{}}
	cfg := cloneConfig(x.cfg)
	for i := range cfg.Secrets {
		v := values[envName("secret", cfg.Secrets[i].SecretSlug)]
		cfg.Secrets[i].Value = &v
	}
	for i := range cfg.Variables {
		v := &cfg.Variables[i]
		if val, ok := values[envName("var", v.VariableSlug)]; ok {
			v.Value = model.VariableValue{V: val}
		}
	}
	p.cfg = cfg
	detected := map[string]string{}
	for _, s := range cfg.Sites {
		if s.SiteSlug == site {
			if v, ok := values[SiteHostEnv]; ok {
				detected[s.SiteSlug] = v
			}
		} else if v, ok := values[envName("site-host", s.SiteSlug)]; ok {
			detected[s.SiteSlug] = v
		}
	}
	hosts := validate.ResolveSiteHosts(cfg, x.req.DeploymentID, validate.HostOptions{Detected: detected})
	sc := siteOf(cfg, site)
	if sc == nil {
		return p
	}
	// the normalized site, as driver.Build sees it
	if norm := validate.NormalizeConfigVolumes(cfg, x.req.Catalog.Images); norm != cfg {
		if ns := siteOf(norm, site); ns != nil {
			sc = ns
		}
		cfg = norm
		p.cfg = cfg
	}
	sm := driver.SiteMapFor(cfg, &x.req.Catalog, x.req.DeploymentID, hosts)
	for i := range sc.Config.Containers {
		c := &sc.Config.Containers[i]
		b, err := driver.Build(driver.BuildParams{DataDir: "/opm-export", DeploymentID: x.req.DeploymentID, SiteSlug: site, RevID: x.req.Commit.ID,
			Config: cfg, Catalog: &x.req.Catalog, Container: c, SiteMap: sm, Hosts: hosts, SkipHostCheck: true, ReadBlob: x.readBlob, Names: driver.NamesPlain})
		if err != nil {
			p.buildErr[c.ContainerSlug] = err.Error()
			p.builts = append(p.builts, nil)
			continue
		}
		for _, f := range b.SiteFiles {
			p.files[f.FileSlug] = f
		}
		p.builts = append(p.builts, b)
	}
	// starter content (rendered once per site, like the agent's contentBytes)
	images := driver.CatalogImages(&x.req.Catalog)
	fctx := validate.SiteFileExprContext(cfg, x.catalogServices(), images, x.req.DeploymentID, nil, site, sm)
	for _, v := range sc.Config.VolumeList() {
		entries := x.seedEntries(site, &v)
		for _, e := range entries {
			if e.render != "expressions" {
				continue
			}
			raw, err := x.readBlob(hexOf(e.sha))
			if err != nil {
				p.seedErr[v.VolumeSlug] = err.Error()
				continue
			}
			out := validate.RenderSiteFile(string(raw), fctx)
			if out.Err != nil {
				p.seedErr[v.VolumeSlug] = fmt.Sprintf("V_FILE_RENDER %s/%s:%d:%d: %s %s", v.VolumeSlug, e.path, out.Err.Line, out.Err.Col, out.Err.Code, out.Err.Message)
				continue
			}
			if p.seeds[v.VolumeSlug] == nil {
				p.seeds[v.VolumeSlug] = map[string][]byte{}
			}
			p.seeds[v.VolumeSlug][e.path] = []byte(out.Value)
		}
	}
	// edge (the agent's InputFromConfig + the standard-modules builder)
	reverse := map[string]string{}
	for k, v := range values {
		reverse[v] = k
	}
	ports := map[string]map[string]int{}
	for _, b := range p.builts {
		if b == nil {
			continue
		}
		m := map[string]int{}
		for _, pr := range b.Ports {
			m[pr.Name] = pr.ContainerPort
		}
		ports[b.ContainerSlug] = m
	}
	in, notes, err := edgecfg.InputFromConfig(edgecfg.InputParams{Instance: "export", DeploymentID: x.req.DeploymentID, SiteSlug: site, Config: cfg, Catalog: &x.req.Catalog, Hosts: hosts,
		Upstreams: func(slug string) map[string]int { return ports[slug] },
		FileContent: func(hex string) ([]byte, bool) {
			b, err := x.readBlob(hex)
			return b, err == nil
		},
		DNSConfig: func(provider, secret string) (map[string]any, error) {
			if n, ok := reverse[secret]; ok {
				return edgecfg.DNSProviderConfig(provider, "{env."+n+"}")
			}
			return edgecfg.DNSProviderConfig(provider, secret)
		}})
	if err != nil {
		p.edgeErr = err.Error()
		return p
	}
	if in == nil {
		return p
	}
	for h, pem := range in.FilesPEM {
		p.tls[h] = [2]string{pem.Cert, pem.Key}
	}
	p.page404, p.page503 = in.NotFound404, in.NotFound503
	if p.page404 == "" {
		p.page404 = edgecfg.BuiltIn404
	}
	if p.page503 == "" {
		if p.page404 != edgecfg.BuiltIn404 {
			p.page503 = p.page404
		} else {
			p.page503 = edgecfg.BuiltIn503
		}
	}
	p.httpPort = in.HTTPPort
	if in.HTTPSPort != nil && in.AnyTLS() {
		p.httpsPort = *in.HTTPSPort
	}
	cfgJSON, more := edgecfg.BuildStandard(in, edgecfg.StandardOptions{StorageRoot: "/data", TLSDir: "/opm-runtime/tls"})
	p.edge = cfgJSON
	p.edgeNote = append(notes, more...)
	s, err := canonical.Canonicalize(cfgJSON)
	if err != nil {
		p.edgeErr = err.Error()
	}
	p.edgeJSON = s
	return p
}

// seedEntry is one starter-content file of a volume (tree / single / legacy files / file seeds, or the persist snapshot
// on agent branches).
type seedEntry struct {
	path, sha, mode, render string
	size                    int64
}

// seedSource: what the volume starts with on a new machine: the persist snapshot (agent / uncommitted branches), else
// the config's starter content. kind "" = nothing; dirs = empty folders.
func (x *exporter) seedSource(site string, v *model.SiteVolume) (kind string, entries []seedEntry, dirs []string, archive *model.VolumeSeed) {
	if snap, ok := x.req.Persist[site][v.VolumeSlug]; ok && v.VolumeKind() == "dir" {
		for _, e := range snap.Tree.Entries {
			entries = append(entries, seedEntry{e.Path, e.Sha256, e.Mode, "none", e.Size})
		}
		return "snapshot", entries, snap.Tree.Dirs, nil
	}
	sd := v.SeedOf()
	if sd == nil {
		return "", nil, nil, nil
	}
	fileOf := func(slug string) *model.FileDef {
		for i := range x.cfg.Files {
			if x.cfg.Files[i].FileSlug == slug {
				return &x.cfg.Files[i]
			}
		}
		return nil
	}
	switch sd.Kind {
	case "tree":
		for _, e := range sd.Tree {
			r := e.Render
			if r == "" {
				r = "none"
			}
			entries = append(entries, seedEntry{e.Path, e.Sha256, e.Mode, r, e.Size})
		}
		return "tree", entries, sd.Dirs, nil
	case "single":
		r := sd.Render
		if r == "" {
			r = "none"
		}
		return "single", []seedEntry{{"", sd.Sha256, sd.Mode, r, sd.Size}}, nil, nil
	case "files":
		for _, e := range sd.Entries {
			if f := fileOf(e.FileSlug); f != nil && f.Content != nil {
				entries = append(entries, seedEntry{e.Path, f.Content.Sha256, "0644", f.RenderMode(), f.Content.SizeBytes})
			}
		}
		return "tree", entries, nil, nil
	case "file":
		if f := fileOf(sd.FileSlug); f != nil && f.Content != nil {
			return "single", []seedEntry{{"", f.Content.Sha256, "0644", f.RenderMode(), f.Content.SizeBytes}}, nil, nil
		}
	case "archive":
		return "archive", nil, nil, sd
	}
	return "", nil, nil, nil
}

func (x *exporter) seedEntries(site string, v *model.SiteVolume) []seedEntry {
	_, e, _, _ := x.seedSource(site, v)
	return e
}

// fingerprint of every output of a pass (used to find which deferred inputs a site uses).
func (p *pass) fingerprint() string {
	var b strings.Builder
	for _, bt := range p.builts {
		if bt == nil {
			b.WriteString("nil\n")
			continue
		}
		s, _ := canonical.Canonicalize(bt.Spec)
		b.WriteString(s + "\n")
		keys := make([]string, 0, len(bt.SecretFiles))
		for k := range bt.SecretFiles {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(k + "=" + string(bt.SecretFiles[k]) + "\n")
		}
	}
	fk := make([]string, 0, len(p.files))
	for k := range p.files {
		fk = append(fk, k)
	}
	sort.Strings(fk)
	for _, k := range fk {
		b.WriteString(k + "=" + p.files[k].Sha + "\n")
	}
	vk := make([]string, 0, len(p.seeds))
	for k := range p.seeds {
		vk = append(vk, k)
	}
	sort.Strings(vk)
	for _, k := range vk {
		pk := make([]string, 0, len(p.seeds[k]))
		for q := range p.seeds[k] {
			pk = append(pk, q)
		}
		sort.Strings(pk)
		for _, q := range pk {
			b.WriteString(k + "/" + q + "=" + canonical.Sha256Hex(p.seeds[k][q]) + "\n")
		}
	}
	b.WriteString(p.edgeJSON)
	hk := make([]string, 0, len(p.tls))
	for h := range p.tls {
		hk = append(hk, h)
	}
	sort.Strings(hk)
	for _, h := range hk {
		b.WriteString(h + p.tls[h][0] + p.tls[h][1])
	}
	return b.String()
}
