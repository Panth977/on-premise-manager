package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/export"
)

// The exporter: ExportRequest assembly (never a secret value) and the run of agent/internal/export. The request and the
// result take the same JSON path as agent/cmd/opm-export, so the tree is the same to the byte.

// RunExporter is runExporter: ExportRequest → ExportResult, in process.
func RunExporter(req *doc.Map) (*doc.Map, error) {
	raw, err := doc.Marshal(req)
	if err != nil {
		return nil, err
	}
	out, code, msg := runExport(raw)
	if code != 0 {
		return nil, fmt.Errorf("exporter failed (exit %d): %s", code, doc.JSSlice(msg, 2000))
	}
	res, err := doc.ParseMap(out)
	if err != nil {
		return nil, err
	}
	if res.Num("format") != MIRROR_EXPORT_FORMAT {
		return nil, fmt.Errorf("exporter format %s ≠ %d", JSString(res.Val("format")), MIRROR_EXPORT_FORMAT)
	}
	return res, nil
}

// runExport is main of agent/cmd/opm-export: exit code + stderr on failure; 2 = a panic.
func runExport(raw []byte) (out []byte, code int, msg string) {
	defer func() {
		if p := recover(); p != nil {
			out, code, msg = nil, 2, fmt.Sprintf("panic: %v\n\n%s", p, debug.Stack())
		}
	}()
	fail := func(err error) ([]byte, int, string) { return nil, 1, "opm-export: " + err.Error() + "\n" }
	var req export.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return fail(fmt.Errorf("bad request JSON: %w", err))
	}
	res, err := export.Export(req, export.Mode{})
	if err != nil {
		return fail(err)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		return fail(err)
	}
	return b.Bytes(), 0, ""
}

// StubExport is stubExport: EMULATOR ONLY stand-in, a deterministic minimal tree.
func StubExport(req *doc.Map) (*doc.Map, error) {
	if !cloud.Env.Emulator() {
		return nil, errors.New("the stub exporter exists only in the emulator")
	}
	var files []*doc.Map
	text := func(p, t string) {
		files = append(files, doc.Obj("path", p, "mode", "100644", "text", t, "size", len(t)))
	}
	br := req.Map("branch")
	config := req.Map("config")
	text("README.md", "# "+br.Str("label")+"\n\nGit branch `"+br.Str("gitBranch")+"` = the "+br.Str("kind")+" at commit `"+req.Map("commit").Str("id")+"` (stub export)\n")
	text(".gitignore", "**/site.env\n**/.env\n**/.runtime/\n**/.data/\n")
	for _, siteV := range req.Arr("sites") {
		site, _ := siteV.(string)
		var s *doc.Map
		for _, x := range mapsOf(config.Arr("sites")) {
			if x.Val("siteSlug") == site {
				s = x
				break
			}
		}
		if s == nil {
			continue
		}
		sc := s.Map("config")
		var svc strings.Builder
		for _, c := range mapsOf(sc.Arr("containers")) {
			key := shared.ImageKey(c.Str("serviceSlug"), JSString(c.Val("version")))
			pull := c.Str("serviceSlug") + ":" + JSString(c.Val("version"))
			if p := req.Map("images").Map(key).Val("pull"); !nullish(p) {
				pull = JSString(p)
			}
			svc.WriteString("  " + c.Str("containerSlug") + ":\n    image: " + pull + "\n")
		}
		text("sites/"+site+"/docker-compose.yml", "name: "+br.Str("entitySlug")+"-"+site+"\nservices:\n"+svc.String())
		var envs strings.Builder
		for _, x := range mapsOf(config.Arr("secrets")) {
			envs.WriteString("OPM_SECRET_" + strings.ReplaceAll(strings.ToUpper(x.Str("secretSlug")), "-", "_") + "=\n")
		}
		text("sites/"+site+"/site.env.example", envs.String())
		for _, f := range mapsOf(config.Arr("files")) {
			content := f.Map("content")
			if content == nil {
				continue
			}
			hex := cloud.HexOf(content.Str("sha256"))
			if f.Val("render") == "expressions" {
				text("sites/"+site+"/files/"+f.Str("fileSlug")+".template", req.Map("texts").Str(hex))
			} else {
				files = append(files, doc.Obj("path", "sites/"+site+"/files/"+f.Str("fileSlug"), "mode", "100644", "blob", hex, "size", content.Val("sizeBytes")))
			}
		}
		for _, v := range mapsOf(sc.Arr("volumes")) {
			vol := v.Str("volumeSlug")
			prefix := "sites/" + site + "/volumes/" + vol + "/"
			if snap := req.Map("persist").Map(site).Map(vol); snap != nil {
				for _, e := range mapsOf(snap.Map("tree").Arr("entries")) {
					mode := "100644"
					if e.Val("mode") == "0755" {
						mode = "100755"
					}
					files = append(files, doc.Obj("path", prefix+e.Str("path"), "mode", mode, "blob", cloud.HexOf(e.Str("sha256")), "size", e.Val("size")))
				}
			} else if seed := v.Map("seed"); seed.Val("kind") == "tree" {
				for _, e := range mapsOf(seed.Arr("entries")) {
					files = append(files, doc.Obj("path", prefix+e.Str("path"), "mode", "100644", "blob", cloud.HexOf(e.Str("sha256")), "size", e.Val("size")))
				}
			}
		}
	}
	sort.SliceStable(files, func(a, b int) bool { return CompareUtf16(files[a].Str("path"), files[b].Str("path")) < 0 })
	l := make([]any, len(files))
	for i, f := range files {
		l[i] = f
	}
	return doc.Obj("format", MIRROR_EXPORT_FORMAT, "files", l, "notes", []any{doc.Obj("code", "STUB", "site", nil, "message", "stub exporter (emulator test)")}, "env", []any{}), nil
}

// PersonOf is personOf; a: CommitAuthor or nil.
func PersonOf(a *doc.Map, siteSlug string) *doc.Map {
	if a.Val("kind") == "unknown" {
		// §21 #113 an agent auto-commit of changes made in Docker outside OPM
		name := "UNKNOWN (changed in Docker)"
		if n := doc.JSTrim(a.Str("name")); n != "" {
			name = n
		}
		site := a.Str("siteSlug")
		if site == "" {
			site = siteSlug
		}
		if site == "" {
			site = "unknown"
		}
		return doc.Obj("name", name, "email", "opm-site-"+site+"@users.noreply.opm")
	}
	email := a.Str("email")
	if a == nil || a.Val("kind") == "system" || email == "" || email == "system" {
		if siteSlug != "" && a.Truthy("siteSlug") {
			return MirrorSitePerson(siteSlug)
		}
		return doc.Obj("name", "OPM", "email", "opm@users.noreply.opm")
	}
	mail := email
	if !strings.Contains(email, "@") {
		mail = email + "@users.noreply.opm"
	}
	name := doc.JSTrim(a.Str("name"))
	if name == "" {
		name = email
	}
	return doc.Obj("name", name, "email", mail)
}

// StripSecrets is stripSecrets: config without any secret value + the sorted list of secrets that have one.
func StripSecrets(c *doc.Map) (*doc.Map, []string) {
	set := []string{}
	var secrets []any
	for _, s := range mapsOf(c.Arr("secrets")) {
		if v := s.Val("value"); !nullish(v) && v != "" {
			set = append(set, s.Str("secretSlug"))
		}
		secrets = append(secrets, s.Shallow().Set("value", nil))
	}
	if secrets == nil {
		secrets = []any{}
	}
	sort.Strings(set)
	return c.Shallow().Set("secrets", secrets), set
}

// LiveCatalogFor is liveCatalogFor: a revision catalog from the live catalog that tolerates missing / not-ready images.
func LiveCatalogFor(config *doc.Map, cat *Catalog) *doc.Map {
	services, images := doc.New(), doc.New()
	for _, site := range mapsOf(config.Arr("sites")) {
		for _, c := range mapsOf(site.Map("config").Arr("containers")) {
			key := shared.ImageKey(c.Str("serviceSlug"), JSString(c.Val("version")))
			if s := cat.Services.Map(c.Str("serviceSlug")); s != nil {
				services.Set(s.Str("serviceSlug"), doc.Obj("serviceSlug", s.Val("serviceSlug"), "label", s.Val("label"), "icon", nil))
			}
			if i := cat.Images.Map(key); i != nil {
				if spec, err := ImageSpec(i); err == nil {
					images.Set(key, spec)
				}
			}
		}
	}
	return doc.Obj("services", services, "images", images, "notFoundPages", doc.New())
}

func notFoundPagesFor(config, base *doc.Map) (*doc.Map, error) {
	out := doc.New()
	if b := base.Map("notFoundPages"); b != nil {
		out.Spread(b)
	}
	for _, s := range mapsOf(config.Arr("sites")) {
		id := s.Map("config").Str("notFoundPageId")
		if id == "" || out.Truthy(id) {
			continue
		}
		d, err := cloud.Col.NotFoundPages().Doc(id).Get()
		if err != nil {
			return nil, err
		}
		if d.Exists() {
			x := d.Data()
			lab, html := "", ""
			if v := x.Val("label"); !nullish(v) {
				lab = JSString(v)
			}
			if v := x.Val("htmlCode"); !nullish(v) {
				html = JSString(v)
			}
			out.Set(id, doc.Obj("notFoundPageId", id, "label", lab, "htmlCode", html))
		}
	}
	return out, nil
}

// ExportCtx is ExportCtx.
type ExportCtx struct {
	Cat               *Catalog
	RegistriesPrivate map[string]bool
	Texts             map[string]string
}

// NewExportCtx is newExportCtx.
func NewExportCtx() *ExportCtx { return &ExportCtx{Texts: map[string]string{}} }

func privateRegistries(x *ExportCtx) (map[string]bool, error) {
	if x.RegistriesPrivate != nil {
		return x.RegistriesPrivate, nil
	}
	m := map[string]bool{}
	q, err := cloud.Col.Registries().Select("username").Get()
	if err != nil {
		return nil, err
	}
	for _, d := range q.Docs {
		m[d.ID()] = d.Data().Truthy("username")
	}
	x.RegistriesPrivate = m
	return m, nil
}

// PullRef is pullRef; i may be nil.
func PullRef(i *doc.Map, fallback string) *doc.Map {
	platform := "linux/amd64"
	if v := i.Val("platform"); !nullish(v) {
		platform = JSString(v)
	}
	src := i.Map("source")
	if i == nil || !src.Truthy("repository") {
		return doc.Obj("pull", fallback, "registryHost", nil, "platform", platform)
	}
	host := src.Str("host")
	if host == "" {
		host = "docker.io"
	}
	digest := i.Val("digest")
	if nullish(digest) {
		digest = src.Val("digest")
	}
	pull := host + "/" + src.Str("repository")
	if src.Truthy("tag") {
		pull += ":" + JSString(src.Val("tag"))
	}
	if doc.Truthy(digest) {
		pull += "@" + JSString(digest)
	}
	return doc.Obj("pull", pull, "registryHost", host, "platform", platform)
}

// ExportInput is ExportInput. Catalog nil = the live catalog.
type ExportInput struct {
	Branch       *doc.Map
	Commit       *doc.Map
	DeploymentID string
	Config       *doc.Map
	Catalog      *doc.Map
	Sites        []any
	Persist      *doc.Map
	Repository   string
}

func hasAny(l []any, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// BuildRequest is buildRequest.
func BuildRequest(x *ExportCtx, i ExportInput) (*doc.Map, error) {
	if x.Cat == nil {
		cat, err := LoadCatalog()
		if err != nil {
			return nil, err
		}
		x.Cat = cat
	}
	config, secretsSet := StripSecrets(i.Config)
	catalog0 := i.Catalog
	if catalog0 == nil {
		catalog0 = LiveCatalogFor(config, x.Cat)
	}
	nf, err := notFoundPagesFor(config, catalog0)
	if err != nil {
		return nil, err
	}
	catalog := catalog0.Shallow().Set("notFoundPages", nf)
	priv, err := privateRegistries(x)
	if err != nil {
		return nil, err
	}
	images := doc.New()
	keys := catalog.Map("images").Keys()
	sort.Strings(keys)
	for _, key := range keys {
		live := x.Cat.Images.Map(key)
		spec := catalog.Map("images").Map(key)
		// §21 #113 a site image with a pullable reference: the compose file pulls that reference
		if ref := spec.Str("reference"); live == nil && !spec.Truthy("digest") && ref != "" {
			first, _, _ := strings.Cut(ref, "/")
			host := "docker.io"
			if strings.Contains(ref, "/") && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
				host = first
			}
			platform := spec.Str("platform")
			if platform == "" {
				platform = "linux/amd64"
			}
			images.Set(key, doc.Obj("pull", ref, "registryHost", host, "private", false, "platform", platform))
			continue
		}
		r := PullRef(live, key)
		images.Set(key, r.Shallow().Set("private", live.Truthy("registryId") && priv[live.Str("registryId")]))
	}
	// texts: every render-expressions blob of the exported sites
	want := map[string]bool{}
	files := mapsOf(config.Arr("files"))
	for _, f := range files {
		if f.Val("render") == "expressions" && f.Map("content") != nil {
			want[cloud.HexOf(f.Map("content").Str("sha256"))] = true
		}
	}
	// files-mode TLS certificates (any render): the exporter reads every blob it needs from texts
	for _, s := range mapsOf(config.Arr("sites")) {
		if !hasAny(i.Sites, s.Str("siteSlug")) {
			continue
		}
		for _, h := range mapsOf(s.Map("config").Map("edge").Arr("hosts")) {
			tls := h.Map("tls")
			if tls.Val("mode") != "files" {
				continue
			}
			for _, f := range files {
				if f.Val("fileSlug") == tls.Val("certFileSlug") {
					if f.Map("content") != nil {
						want[cloud.HexOf(f.Map("content").Str("sha256"))] = true
					}
					break
				}
			}
		}
	}
	for _, s := range mapsOf(config.Arr("sites")) {
		if !hasAny(i.Sites, s.Str("siteSlug")) {
			continue
		}
		for _, v := range mapsOf(s.Map("config").Arr("volumes")) {
			seed := v.Map("seed")
			if seed.Val("kind") == "tree" {
				for _, e := range mapsOf(seed.Arr("entries")) {
					if e.Val("render") == "expressions" {
						want[cloud.HexOf(e.Str("sha256"))] = true
					}
				}
			}
			if seed.Val("kind") == "single" && seed.Val("render") == "expressions" && seed.Truthy("sha256") {
				want[cloud.HexOf(seed.Str("sha256"))] = true
			}
		}
	}
	hexes := make([]string, 0, len(want))
	for h := range want {
		hexes = append(hexes, h)
	}
	sort.Strings(hexes)
	texts := doc.New()
	for _, hex := range hexes {
		t, ok := x.Texts[hex]
		if !ok {
			s, _, err := ReadText("files/" + hex)
			if err != nil {
				return nil, err
			}
			t = s
			x.Texts[hex] = t
		}
		texts.Set(hex, t)
	}
	return doc.Obj("format", MIRROR_EXPORT_FORMAT, "branch", i.Branch, "commit", i.Commit, "deploymentId", i.DeploymentID, "config", config, "secretsSet", strs(secretsSet), "catalog", catalog, "images", images, "texts", texts, "sites", i.Sites, "persist", i.Persist, "repository", i.Repository), nil
}

// ExportTree is exportTree.
func ExportTree(req *doc.Map, stub bool) (*doc.Map, error) {
	if stub && cloud.Env.Emulator() {
		return StubExport(req)
	}
	return RunExporter(req)
}

var reSiteDir = regexp.MustCompile(`^sites/([^/]+)/`)
