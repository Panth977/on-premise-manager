package core

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

func init() {
	cloud.VerifySchema = func(db *driver.Store, requireBranches, requireNoWorkingCopy bool) ([]string, error) {
		return VerifySchema(db, VerifyOptions{RequireBranches: requireBranches, RequireNoWorkingCopy: requireNoWorkingCopy})
	}
}

// VerifyOptions are the two checks a data dir may be excused from.
type VerifyOptions struct {
	// RequireBranches: every template / deployment has `branch`.
	RequireBranches bool
	// RequireNoWorkingCopy: no `branch.uncommitted`, and the template config fields / deployment draft equal the head
	// commit config (they are a copy of the head, written only by commits).
	RequireNoWorkingCopy bool
}

var (
	reVerifyOldKey    = regexp.MustCompile(`"serviceId"\s*:`)
	reVerifyHTTPS     = regexp.MustCompile(`^https://`)
	reVerifyGHOwner   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	reVerifyGHRepo    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	reVerifyAccessKey = regexp.MustCompile(`^[A-Z2-7]{4}(-[A-Z2-7]{4}){5}$`)
)

// VerifySchema reads every document and checks it against the data model (shared types, the validators, the simple-body
// rules of the API). It returns one "<path>: <message>" line per violation; an error means the data could not be read
// at all.
//
// Checked:
//   - registries: username / password pair.
//   - services: doc id = serviceSlug, no serviceId / slug / repository / defaultRepository, icon.
//   - images: doc id = version, serviceSlug = parent, reference parses and equals source, registryId exists and matches
//     the host, env normalized + ValidateImageEnv, every volume has kind dir | file + ValidateImageVolumes.
//   - templates: icon, body (ValidateConfig, mode template), slug, branch.
//   - deployments: image, slug, draft (ValidateConfig, mode draft, save-level errors), srcTemplate snapshot + merge base,
//     access key, install tokens, branch, agent branches, revisions.
//   - revisions: no serviceId / tag containers, catalog keyed <serviceSlug>:<version>, hash / catalogHash.
//   - tags / accounts: icon / img; accounts hold their stored-credential fields. icons: fields + bytes.
//   - the githubMirror settings, and the obsolete collection serviceSlugs (empty).
//
// Every config (template body, draft, srcTemplate snapshot, revision config) must have instance containers (containerSlug
// + ports + volumes lists, no `port` / `volume`, depends {containerSlug}), proxy actions naming containerSlug, files with
// render ('expressions' | 'none'; binary = none) and a location, SiteConfig.host string | null (not ""); a `volumes` list
// on each site whose entries have a location, kind 'dir' | 'file' and seed (null | object); mounts of declared volumes
// only, no source kind `data`, file / secret mounts read-only. The edge must be what NormalizeEdge leaves unchanged (these
// shape checks report a legacy edge once, in place of the validator's field-by-field errors). devTools is absent or a
// non-empty list of presets in the stored shape, a static credential a non-empty string. Templates, drafts and
// srcTemplate configs (not revisions, which are immutable history) have no File seed, tree seeds in canonical form, and
// `persist` absent or exactly { exclude, maxBytes, auto } on a folder volume.
func VerifySchema(db *driver.Store, opts VerifyOptions) (out []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			f, ok := r.(verifyFail)
			if !ok {
				panic(r)
			}
			out, err = nil, f.err
		}
	}()
	v := &verifier{db: db, opts: opts, out: []string{}, regs: map[string]*doc.Map{}, services: doc.New(), images: doc.New(), legacyEdge: map[string]map[string]bool{}}
	v.run()
	return v.out, nil
}

// verifyFail carries an error that ends the whole run (a read that failed, a stored JSON text that does not parse).
type verifyFail struct{ err error }

func vMust[T any](x T, err error) T {
	if err != nil {
		panic(verifyFail{err})
	}
	return x
}

type verifier struct {
	db         *driver.Store
	opts       VerifyOptions
	out        []string
	iconIDs    map[string]bool
	regs       map[string]*doc.Map
	services   *doc.Map
	images     *doc.Map
	pages      []any
	legacyEdge map[string]map[string]bool // document path → site slugs whose edge is legacy (their validator errors are skipped)
}

func (v *verifier) bad(path, msg string) { v.out = append(v.out, path+": "+msg) }

func (v *verifier) docs(q vQuery) []*driver.DocSnap { return vMust(q.Get()).Docs }

type vQuery interface {
	Get() (*driver.QuerySnap, error)
}

// ── JS semantics of the values: a missing key is doc.Undefined, distinct from null

// vGet is m[k] (m may be nil).
func vGet(m *doc.Map, k string) any {
	if x, ok := m.Get(k); ok {
		return x
	}
	return doc.Undefined
}

func vObj(x any) *doc.Map {
	m, _ := x.(*doc.Map)
	return m
}

func vIsObj(x any) bool { return vObj(x) != nil }

func vNull(x any) bool { return x == nil || x == doc.Undefined }

// vCoal is x ?? def.
func vCoal(x, def any) any {
	if vNull(x) {
		return def
	}
	return x
}

// vSame is x === y of document values (an array or object is only ever itself).
func vSame(x, y any) bool {
	switch x.(type) {
	case []any, *doc.Map, doc.Bytes:
		return false
	}
	switch y.(type) {
	case []any, *doc.Map, doc.Bytes:
		return false
	}
	return x == y
}

func vStr(m *doc.Map, k string) string { return shared.JSString(vGet(m, k)) }

func vIsStr(m *doc.Map, k string) bool { _, ok := m.Val(k).(string); return ok }

func vIsNum(m *doc.Map, k string) bool { _, ok := m.NumOK(k); return ok }

func vJSON(x any) string { return doc.MarshalString(x) }

func vCanon(x any) string { return vMust(shared.Canonicalize(x)) }

func vParse(s string) any { return vMust(doc.ParseString(s)) }

func vHash(x any) string { return vMust(shared.CatalogHashOf(x)) }

// ── icons

func (v *verifier) iconIs(path string, x any, picture bool) {
	is := shared.NewIssues("draft")
	if picture {
		shared.CheckPicture(x, "icon", is)
	} else {
		shared.CheckIcon(x, "icon", is)
	}
	for _, e := range is.Errors() {
		v.bad(path, e.(*doc.Map).Str("message"))
	}
}

func (v *verifier) iconRef(path string, x any) {
	if s, ok := x.(string); ok && strings.HasPrefix(s, "upload:") && !v.iconIDs[s[7:]] {
		v.bad(path, "icon "+s+" does not exist")
	}
}

func (v *verifier) run() {
	v.iconIDs = map[string]bool{}
	for _, d := range v.docs(v.db.Collection("icons").Select()) {
		v.iconIDs[d.ID()] = true
	}
	v.registries()
	v.servicesAndImages()
	for _, d := range v.docs(v.db.Collection("serviceSlugs").Select()) {
		v.bad("serviceSlugs/"+d.ID(), "obsolete collection")
	}
	for _, d := range v.docs(v.db.Collection("notFoundPages").Select()) {
		v.pages = append(v.pages, d.ID())
	}
	if v.pages == nil {
		v.pages = []any{}
	}
	v.entitySlugs()
	v.githubMirror()
	v.templates()
	v.deployments()
	for _, d := range v.docs(v.db.Collection("tags")) {
		p := "tags/" + d.ID()
		v.iconIs(p, vCoal(vGet(d.Data(), "icon"), nil), false)
		v.iconRef(p, vGet(d.Data(), "icon"))
	}
	v.accounts()
	v.icons()
}

func (v *verifier) registries() {
	for _, d := range v.docs(v.db.Collection("registries")) {
		r := d.Data()
		v.regs[d.ID()] = r
		p := "registries/" + d.ID()
		if !vSame(vGet(r, "registryId"), d.ID()) {
			v.bad(p, "registryId ≠ doc id")
		}
		if u, ok := r.Val("url").(string); !ok || !reVerifyHTTPS.MatchString(u) {
			v.bad(p, "url must be https")
		}
		if !vIsStr(r, "username") || !vIsStr(r, "password") {
			v.bad(p, "username / password must be strings")
		} else if (r.Str("username") == "") != (r.Str("password") == "") {
			v.bad(p, "username and password must both be set or both be empty")
		}
	}
}

func (v *verifier) servicesAndImages() {
	for _, d := range v.docs(v.db.Collection("services")) {
		s := d.Data()
		p := "services/" + d.ID()
		v.services.Set(d.ID(), doc.Obj("label", vGet(s, "label")))
		if !shared.RE.Slug.MatchString(d.ID()) {
			v.bad(p, "doc id is not a service slug")
		}
		if !vSame(vGet(s, "serviceSlug"), d.ID()) {
			v.bad(p, "serviceSlug ≠ doc id")
		}
		for _, k := range []string{"serviceId", "slug", "repository", "defaultRepository"} {
			if s.Has(k) {
				v.bad(p, "obsolete field "+k)
			}
		}
		if !vIsStr(s, "label") || s.Str("label") == "" {
			v.bad(p, "label missing")
		}
		v.iconIs(p, vCoal(vGet(s, "icon"), nil), false)
		v.iconRef(p, vGet(s, "icon"))
		for _, id := range v.docs(d.Ref().Collection("images")) {
			v.image(d.ID(), p, id)
		}
	}
}

func (v *verifier) image(slug, p string, id *driver.DocSnap) {
	i := id.Data()
	ip := p + "/images/" + id.ID()
	if !shared.RE.Version.MatchString(id.ID()) {
		v.bad(ip, "doc id is not a version")
	}
	if !vSame(vGet(i, "version"), id.ID()) {
		v.bad(ip, "version ≠ doc id")
	}
	if !vSame(vGet(i, "serviceSlug"), slug) {
		v.bad(ip, "serviceSlug ≠ parent")
	}
	for _, k := range []string{"serviceId", "tag", "repository"} {
		if i.Has(k) {
			v.bad(ip, "obsolete field "+k)
		}
	}
	pr := shared.ParseImageReference(vGet(i, "reference"))
	if !pr.Bool("ok") {
		v.bad(ip, "reference: "+pr.Str("error"))
	} else if vCanon(pr.Val("parts")) != vCanon(vCoal(vGet(i, "source"), nil)) {
		v.bad(ip, "source ≠ parseImageReference(reference)")
	}
	rid := vGet(i, "registryId")
	if s, isStr := rid.(string); isStr {
		if r, ok := v.regs[s]; !ok {
			v.bad(ip, "registry "+s+" does not exist")
		} else if h := shared.RegistryHost(r.Str("url")); pr.Bool("ok") && h != pr.Map("parts").Str("host") {
			v.bad(ip, "registry host "+h+" ≠ reference host "+pr.Map("parts").Str("host"))
		}
	} else if rid != nil {
		v.bad(ip, "registryId must be an id or null")
	}
	if imp := i.Map("import"); imp == nil || !slices.Contains([]string{"pending", "copying", "ready", "failed"}, imp.Str("status")) {
		v.bad(ip, "import.status invalid")
	}
	env := i.Arr("env")
	if env == nil {
		v.bad(ip, "env must be a list")
		env = []any{}
	}
	for _, x := range env {
		e := vObj(x)
		if !e.Has("options") {
			v.bad(ip, "env "+vStr(e, "name")+": options missing (normalize)")
		}
		if _, isList := e.Val("value").([]any); isList {
			v.bad(ip, "env "+vStr(e, "name")+": legacy list value")
		}
		if vCanon(vMust(shared.NormalizeImageEnv(x))) != vCanon(x) {
			v.bad(ip, "env "+vStr(e, "name")+": not normalized")
		}
	}
	is := shared.NewIssues("publish")
	vMust(0, shared.ValidateImageEnv(env, is, "env"))
	if vols := i.Arr("volumes"); vols == nil {
		v.bad(ip, "volumes must be a list")
	} else {
		for _, x := range vols {
			o := vObj(x)
			if o == nil {
				v.bad(ip, "volume ?: kind missing / not dir | file")
			} else if k := vGet(o, "kind"); !vSame(k, "dir") && !vSame(k, "file") {
				v.bad(ip, "volume "+vStr(o, "name")+": kind missing / not dir | file")
			}
		}
		vMust(0, shared.ValidateImageVolumes(vols, is, "volumes"))
	}
	for _, x := range is.Errors() {
		e := x.(*doc.Map)
		if !(strings.HasSuffix(e.Str("path"), ".kind") && e.Str("code") == "V_ENUM") {
			v.bad(ip, e.Str("path")+": "+e.Str("message"))
		}
	}
	v.images.Set(shared.ImageKey(slug, id.ID()), doc.Obj(
		"kind", vGet(i, "kind"),
		"ports", vCoal(vGet(i, "ports"), []any{}),
		"env", env,
		"volumes", vCoal(vGet(i, "volumes"), []any{}),
		"healthcheck", vCoal(vGet(i, "healthcheck"), nil),
		"importStatus", vGet(i.Map("import"), "status"),
	))
}

func (v *verifier) noOldKeys(path string, x any) {
	if reVerifyOldKey.MatchString(vJSON(vCoal(x, nil))) {
		v.bad(path, "contains serviceId (old shape)")
	}
}

// locationIs: a storage location is present ({kind:auto} | {kind:path, path}) and passes CheckLocation.
func (v *verifier) locationIs(path, what string, l any, kind string) {
	if vNull(l) {
		v.bad(path, what+": location missing")
		return
	}
	is := shared.NewIssues("draft")
	shared.CheckLocation(l, kind, "location", is)
	for _, e := range is.Errors() {
		v.bad(path, what+": "+e.(*doc.Map).Str("message"))
	}
}

// edgeShape lists what is wrong with the edge of one site config (empty = current shape).
func edgeShape(sp string, edge any) []string {
	if edge == doc.Undefined {
		return nil // a config without edge gets the default edge on read
	}
	e := vObj(edge)
	if e == nil {
		return []string{sp + ": edge must be an object"}
	}
	var out []string
	if !e.Has("maxBodyBytes") {
		out = append(out, sp+": edge maxBodyBytes missing")
	}
	for _, x := range e.Arr("hosts") {
		h := vObj(x)
		rh := h.Map("responseHeaders")
		if rh == nil || rh.Map("set") == nil || rh.Map("add") == nil || rh.Arr("remove") == nil || rh.Arr("always") == nil {
			name := "?"
			if h != nil {
				name = vStr(h, "hostname")
			}
			out = append(out, sp+" host "+name+": responseHeaders {set, add, remove, always} missing")
		}
	}
	for _, x := range e.Arr("routes") {
		r := vObj(x)
		if r == nil {
			continue
		}
		rp := sp + " route " + vStr(r, "routeId")
		m := r.Map("match")
		if m.Has("pathPrefix") {
			out = append(out, rp+": legacy match {pathPrefix} (must be {kind, path, stopRegex})")
		} else if _, isBool := m.Val("stopRegex").(bool); !vIsStr(m, "kind") || !vIsStr(m, "path") || !isBool {
			out = append(out, rp+": match must have kind, path, stopRegex")
		}
		if !r.Has("responseHeaders") {
			out = append(out, rp+": responseHeaders missing (null = inherit host)")
		}
		if a := r.Map("action"); a.Val("type") == "proxy" {
			if a.Has("timeoutSec") {
				out = append(out, rp+": legacy proxy timeoutSec (must be readTimeoutSec)")
			}
			var miss []string
			for _, k := range []string{"connectTimeoutSec", "sendTimeoutSec", "readTimeoutSec", "rewrite", "requestBuffering", "responseBuffering", "maxBodyBytes", "websocket"} {
				if !a.Has(k) && !(a.Has("timeoutSec") && strings.HasSuffix(k, "TimeoutSec")) {
					miss = append(miss, k)
				}
			}
			if len(miss) > 0 {
				out = append(out, rp+": proxy fields missing: "+strings.Join(miss, ", "))
			}
		}
	}
	if len(out) == 0 {
		if n, err := shared.NormalizeEdge(e); err != nil {
			out = append(out, sp+": edge cannot be normalized ("+err.Error()+")")
		} else if vCanon(n) != vCanon(e) {
			out = append(out, sp+": edge not normalized (normalizeEdge changes it)")
		}
	}
	return out
}

// shapeConfig checks the stored shape of a config, independently of ValidateConfig, so that a legacy document is reported
// precisely.
func (v *verifier) shapeConfig(path string, cfg any) {
	c := vObj(cfg)
	if c == nil {
		return
	}
	for _, x := range c.Arr("files") {
		f := vObj(x)
		if f == nil {
			continue
		}
		render := vGet(f, "render")
		if !vSame(render, "expressions") && !vSame(render, "none") {
			v.bad(path, "file "+vStr(f, "fileSlug")+": render missing / invalid ("+vJSON(vCoal(render, nil))+")")
		} else if f.Val("editor") == "binary" && !vSame(render, "none") {
			v.bad(path, "file "+vStr(f, "fileSlug")+": binary file with render "+shared.JSString(render))
		}
		v.locationIs(path, "file "+vStr(f, "fileSlug"), vGet(f, "location"), "files")
	}
	for _, x := range c.Arr("sites") {
		site := vObj(x)
		if site == nil || site.Map("config") == nil {
			continue
		}
		v.shapeSite(path, site, site.Map("config"))
	}
}

func (v *verifier) shapeSite(path string, site, sc *doc.Map) {
	sp := "site " + vStr(site, "siteSlug")
	if host, isStr := sc.Val("host").(string); !isStr && !vSame(vGet(sc, "host"), nil) {
		v.bad(path, sp+": host must be a string or null")
	} else if isStr && doc.JSTrim(host) == "" {
		v.bad(path, sp+`: host "" (must be null = auto)`)
	}
	declared := map[string]bool{}
	vols, hasVols := sc.Val("volumes").([]any)
	if !hasVols {
		v.bad(path, sp+": volumes list missing (pre-round-D site config)")
	} else {
		for _, x := range vols {
			o := vObj(x)
			if o == nil || !vIsStr(o, "volumeSlug") {
				continue
			}
			slug := o.Str("volumeSlug")
			declared[slug] = true
			v.locationIs(path, sp+" volume "+slug, vGet(o, "location"), "volumes")
			kind := vGet(o, "kind")
			if !vSame(kind, "dir") && !vSame(kind, "file") {
				v.bad(path, sp+" volume "+slug+": kind missing / invalid ("+vJSON(vCoal(kind, nil))+")")
			}
			if seed := vGet(o, "seed"); !o.Has("seed") || (seed != nil && !vIsObj(seed)) {
				v.bad(path, sp+" volume "+slug+": seed missing / invalid (null = starts empty)")
			}
		}
	}
	for _, x := range sc.Arr("containers") {
		c := vObj(x)
		if c == nil {
			continue
		}
		v.shapeContainer(path, sp, c, sc, hasVols, declared)
	}
	if sc.Has("devTools") {
		v.shapeDevTools(path, sp, sc.Val("devTools"))
	}
	ev := edgeShape(sp, vGet(sc, "edge"))
	for _, m := range ev {
		v.bad(path, m+"")
	}
	if len(ev) > 0 {
		if v.legacyEdge[path] == nil {
			v.legacyEdge[path] = map[string]bool{}
		}
		v.legacyEdge[path][vStr(site, "siteSlug")] = true
	}
	for _, x := range sc.Map("edge").Arr("routes") {
		r := vObj(x)
		a := r.Map("action")
		if a.Val("type") == "proxy" && (!vIsStr(a, "containerSlug") || a.Has("serviceSlug")) {
			v.bad(path, sp+" route "+vStr(r, "routeId")+": proxy action must name containerSlug (not serviceSlug)")
		}
	}
}

func (v *verifier) shapeContainer(path, sp string, c, sc *doc.Map, hasVols bool, declared map[string]bool) {
	cp := sp + " container " + shared.JSString(vCoal(vGet(c, "containerSlug"), vGet(c, "serviceSlug")))
	for i, x := range c.Arr("volumes") {
		o := vObj(x)
		src := o.Map("source")
		if src == nil {
			continue
		}
		mp := cp + " extra mount #" + strconv.Itoa(i+1)
		if doc.Truthy(vGet(o, "imageVolume")) {
			mp = cp + " image volume " + vStr(o, "imageVolume")
		}
		kind := src.Val("kind")
		if kind == "data" {
			v.bad(path, mp+": legacy source kind data ("+vStr(src, "name")+") — must be a declared volume")
		} else if slug := vStr(src, "volumeSlug"); kind == "volume" && hasVols && !declared[slug] {
			v.bad(path, mp+": volume "+slug+" is not declared on the site")
		}
		if (kind == "file" || kind == "secret") && o.Val("readOnly") != true {
			v.bad(path, mp+": "+shared.JSString(kind)+" mount must be readOnly true")
		}
	}
	if s, ok := c.Val("containerSlug").(string); !ok || !shared.RE.Slug.MatchString(s) {
		v.bad(path, cp+": containerSlug missing / invalid (legacy one-per-service shape)")
	}
	if c.Has("port") {
		v.bad(path, cp+": legacy field port")
	}
	if c.Has("volume") {
		v.bad(path, cp+": legacy field volume")
	}
	if _, ok := c.Val("ports").([]any); !ok {
		v.bad(path, cp+": ports must be a list")
	}
	if vols, ok := c.Val("volumes").([]any); !ok {
		v.bad(path, cp+": volumes must be a list")
	} else {
		for _, x := range vols {
			o := vObj(x)
			if o == nil || o.Map("source") == nil || !o.Has("imageVolume") {
				v.bad(path, cp+": volume row without imageVolume / source")
			}
		}
	}
	for _, x := range c.Arr("depends") {
		d := vObj(x)
		if d == nil || !vIsStr(d, "containerSlug") || d.Has("serviceSlug") {
			v.bad(path, cp+": depends entry must be {containerSlug}")
		}
	}
}

// shapeDevTools: devTools is omitted when empty (agents < 0.11 re-hash without it); presets in the exact stored shape.
func (v *verifier) shapeDevTools(path, sp string, tools any) {
	list, ok := tools.([]any)
	if !ok {
		v.bad(path, sp+": devTools must be a list of presets")
		return
	}
	if len(list) == 0 {
		v.bad(path, sp+": devTools is [] (an empty list must be omitted — it changes the revision hash for agents < 0.11)")
		return
	}
	for _, pr := range list {
		p := vObj(pr)
		pp := sp + " dev tool ?"
		if p != nil {
			pp = sp + " dev tool " + vStr(p, "presetSlug")
		}
		if p == nil {
			v.bad(path, pp+": must be an object")
		} else if vCanon(vMust(shared.NormalizeStoredDevToolPreset(pr))) != vCanon(pr) {
			v.bad(path, pp+": not in the stored shape (normalizeStoredDevToolPreset changes it: missing / unknown keys)")
		} else if badStaticCredential(p.Map("auth")) {
			v.bad(path, pp+": static credential value must be a non-empty string")
		}
	}
}

// badStaticCredential: a static (literal) credential is allowed, but it is a non-empty string (never the mask or null).
func badStaticCredential(auth *doc.Map) bool {
	for _, k := range []string{"password", "token"} {
		if c := auth.Map(k); c != nil && c.Val("kind") == "literal" {
			if s, ok := c.Val("value").(string); !ok || s == "" {
				return true
			}
		}
	}
	return false
}

// shapeVolumeContent checks the volume content of a template / draft / srcTemplate config (not revisions: they keep what
// they were published with).
func (v *verifier) shapeVolumeContent(path string, cfg any) {
	c := vObj(cfg)
	if c == nil {
		return
	}
	for _, x := range c.Arr("sites") {
		site := vObj(x)
		if site == nil || site.Map("config") == nil {
			continue
		}
		for _, y := range site.Map("config").Arr("volumes") {
			o := vObj(y)
			if o == nil {
				continue
			}
			vp := "site " + vStr(site, "siteSlug") + " volume " + vStr(o, "volumeSlug")
			sd := vGet(o, "seed")
			if shared.IsLegacyFileSeed(vCoal(sd, nil)) {
				v.bad(path, vp+": legacy starter content from Files (seed "+vStr(vObj(sd), "kind")+")")
			}
			if seed := vObj(sd); seed != nil && seed.Val("kind") == "tree" {
				if seed.Arr("entries") == nil || seed.Arr("dirs") == nil {
					v.bad(path, vp+": tree seed needs entries[] and dirs[]")
				} else if vCanon(doc.Obj("entries", seed.Val("entries"), "dirs", seed.Val("dirs"))) != vCanon(vMust(shared.NormalizeVolumeTree(seed))) {
					v.bad(path, vp+": tree seed is not canonical (normalizeVolumeTree changes it)")
				}
				for _, k := range seed.Keys() {
					if k != "kind" && k != "entries" && k != "dirs" {
						v.bad(path, vp+": tree seed has an unknown field "+k)
					}
				}
			}
			if o.Has("persist") {
				v.shapePersist(path, vp, o)
			}
		}
	}
}

func (v *verifier) shapePersist(path, vp string, o *doc.Map) {
	p := o.Map("persist")
	if p == nil {
		v.bad(path, vp+": persist must be absent (off) or { exclude, maxBytes, auto } (got "+vJSON(o.Val("persist"))+")")
		return
	}
	exclude, isList := p.Val("exclude").([]any)
	allStr := isList
	for _, x := range exclude {
		if _, ok := x.(string); !ok {
			allStr = false
		}
	}
	if _, isBool := p.Val("auto").(bool); !allStr || !vIsNum(p, "maxBytes") || !isBool {
		v.bad(path, vp+": persist must be { exclude: string[], maxBytes: number, auto: boolean }")
	}
	var extra []string
	for _, k := range p.Keys() {
		if k != "exclude" && k != "maxBytes" && k != "auto" {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		v.bad(path, vp+": persist has unknown field(s) "+strings.Join(extra, ", "))
	}
	if o.Val("kind") == "file" {
		v.bad(path, vp+": persist on a single-file volume")
	}
}

// reportedByShape: validator errors already reported, more precisely, by shapeConfig / shapeVolumeContent.
func reportedByShape(e *doc.Map) bool {
	code, msg, path := e.Str("code"), e.Str("message"), e.Str("path")
	return code == "V_VOLUME_UNDECLARED" || code == "V_READONLY" || (code == "V_REQUIRED" && strings.HasSuffix(path, ".volumes")) ||
		(code == "V_ENUM" && strings.Contains(msg, `"data"`)) || (code == "V_LOCATION" && !strings.Contains(msg, "same place or nested")) ||
		code == "V_VOLUME_KIND" || code == "V_VOLUME_SEED_FILE_REF"
}

func (v *verifier) configErrors(path string, cfg any, mode, dep string) {
	v.noOldKeys(path, cfg)
	v.shapeConfig(path, cfg)
	v.shapeVolumeContent(path, cfg)
	is, err := shared.ValidateConfig(vObj(cfg), doc.Obj("mode", mode, "deploymentId", dep, "services", v.services, "images", v.images, "notFoundPageIds", v.pages))
	if err != nil {
		v.bad(path, "validateConfig threw: "+err.Error())
		return
	}
	for _, x := range is.Errors() {
		e := x.(*doc.Map)
		if reportedByShape(e) || v.inLegacyEdge(path, e.Str("path")) {
			continue
		}
		v.bad(path, e.Str("path")+": "+e.Str("code")+" "+e.Str("message"))
	}
}

// inLegacyEdge: a legacy edge is reported once by edgeShape, not again field by field by the validator.
func (v *verifier) inLegacyEdge(path, errPath string) bool {
	for s := range v.legacyEdge[path] {
		b := "sites[" + s + "].config.edge"
		if errPath == b || strings.HasPrefix(errPath, b+".") {
			return true
		}
	}
	return false
}

type verifyCommit struct {
	id string
	d  *doc.Map
}

// commitChain checks that commits form one linear chain (seq 1..n, parentId = the previous, hash = sha256(JCS(config)),
// message, kind, catalog on deployment commits). Agent-branch mirrors are chains too, but legacy / migrated commits may
// name a parent that is not mirrored.
func (v *verifier) commitChain(cp string, coll *driver.CollectionRef, needCatalog, mirror bool) []verifyCommit {
	var chain []verifyCommit
	for _, c := range v.docs(coll.OrderBy("seq", "asc")) {
		chain = append(chain, verifyCommit{c.ID(), c.Data()})
	}
	for i, c := range chain {
		d := c.d
		p := cp + "/" + c.id
		if !vSame(vGet(d, "commitId"), c.id) {
			v.bad(p, "commitId ≠ doc id")
		}
		if m, ok := d.Val("message").(string); !ok || doc.JSTrim(m) == "" {
			v.bad(p, "message missing")
		}
		if k, ok := d.Val("kind").(string); !ok || !slices.Contains(shared.COMMIT_KINDS, k) {
			v.bad(p, "kind "+vStr(d, "kind")+" unknown")
		}
		seq, seqOK := d.NumOK("seq")
		if i == 0 && !mirror && (!seqOK || seq != 1 || !vSame(vGet(d, "parentId"), nil)) {
			v.bad(p, "the first commit must be seq 1 without parent")
		}
		if i > 0 {
			prev := chain[i-1]
			ps, psOK := prev.d.NumOK("seq")
			if !seqOK || !psOK || seq != ps+1 || !vSame(vGet(d, "parentId"), prev.id) {
				if k := d.Val("kind"); !(mirror && (k == "legacy" || k == "migrated")) {
					v.bad(p, "seq "+vStr(d, "seq")+" / parent "+vStr(d, "parentId")+" do not follow "+prev.id+" seq "+vStr(prev.d, "seq"))
				}
			}
		}
		if cfg, ok := d.Val("_config").(string); ok {
			if !vSame(vHash(vParse(cfg)), vGet(d, "hash")) {
				v.bad(p, "hash ≠ sha256(JCS(config))")
			}
			if needCatalog {
				if cat, ok := d.Val("_catalog").(string); !ok || !vSame(vHash(vParse(cat)), vGet(d, "catalogHash")) {
					v.bad(p, "catalog / catalogHash missing or wrong")
				}
			}
		} else if !vIsStr(d, "_configRef") {
			v.bad(p, "_config / _configRef missing")
		}
	}
	return chain
}

// branchIs checks `branch` { head, origin, commitCount } of a template / deployment (no `uncommitted`, and the config copy
// = the head commit config, with RequireNoWorkingCopy), its commits (head = the newest) and its merge bases.
func (v *verifier) branchIs(p string, x *doc.Map, ref *driver.DocRef, kind string) {
	bv := vGet(x, "branch")
	if !doc.Truthy(bv) {
		if v.opts.RequireBranches {
			v.bad(p, "branch missing")
		}
		return
	}
	b := vObj(bv)
	if !vIsNum(b, "commitCount") {
		v.bad(p, "branch.commitCount must be a number")
	}
	if v.opts.RequireNoWorkingCopy && b.Has("uncommitted") {
		v.bad(p, "branch.uncommitted is still stored (there is no working copy since §21 #105)")
	} else if !v.opts.RequireNoWorkingCopy && b.Has("uncommitted") && !vIsNum(b, "uncommitted") {
		v.bad(p, "branch.uncommitted must be a number")
	}
	chain := v.commitChain(p+"/commits", ref.Collection("commits"), kind == "deployment", false)
	headID := vCoal(vGet(b.Map("head"), "commitId"), nil)
	if v.opts.RequireNoWorkingCopy {
		v.workingCopy(p, x, ref, kind, headID)
	}
	var newest any
	if len(chain) > 0 {
		newest = chain[len(chain)-1].id
	}
	if !vSame(newest, headID) {
		v.bad(p, "branch.head "+shared.JSString(headID)+" is not the newest commit "+shared.JSString(newest))
	}
	if n, ok := b.NumOK("commitCount"); !ok || n != float64(len(chain)) {
		v.bad(p, "branch.commitCount "+vStr(b, "commitCount")+" ≠ "+strconv.Itoa(len(chain))+" commits")
	}
	if o := vGet(b, "origin"); !vNull(o) && vObj(o).Map("branch") == nil {
		v.bad(p, "branch.origin must be null or { branch, commitId, seq, label }")
	}
	for _, m := range v.docs(ref.Collection("mergeBases")) {
		v.mergeBase(p+"/mergeBases/"+m.ID(), m.Data())
	}
}

func (v *verifier) workingCopy(p string, x *doc.Map, ref *driver.DocRef, kind string, headID any) {
	field := "_draft"
	if kind == "template" {
		field = "_body"
	}
	raw, ok := x.Val(field).(string)
	if !ok {
		return
	}
	parsed := vParse(raw)
	if !doc.Truthy(parsed) {
		return
	}
	cp := vObj(parsed)
	if !doc.Truthy(headID) {
		if len(cp.Arr("variables")) > 0 || len(cp.Arr("secrets")) > 0 || len(cp.Arr("files")) > 0 || len(cp.Arr("sites")) > 0 {
			v.bad(p, "no head commit but a non-empty config copy (uncommitted changes)")
		}
		return
	}
	hs := vMust(ref.Collection("commits").Doc(shared.JSString(headID)).Get())
	hd := hs.Data()
	cfg, ok := hd.Val("_config").(string)
	if !ok || vSame(vHash(parsed), vGet(hd, "hash")) {
		return
	}
	head := vObj(vParse(cfg))
	if n := len(shared.UncommittedItems(head, cp, doc.Obj("secretValues", kind == "deployment"))); n > 0 {
		v.bad(p, "the config copy differs from the head commit seq "+vStr(hd, "seq")+" ("+strconv.Itoa(n)+" item(s) — uncommitted changes)")
	}
}

func (v *verifier) mergeBase(mp string, mb *doc.Map) {
	if k, ok := mb.Val("sourceKey").(string); !ok || shared.ParseBranchKey(k) == nil {
		v.bad(mp, "sourceKey is not a branch key")
	}
	base, err := doc.ParseString(shared.JSString(vGet(mb, "_base")))
	if err != nil {
		v.bad(mp, "_base / _skipped are not JSON")
		return
	}
	bo := vObj(base)
	if bo == nil || bo.Arr("sites") == nil {
		v.bad(mp, "_base is not a config")
	} else {
		secrets, isList := vCoal(vGet(bo, "secrets"), []any{}).([]any)
		if !isList {
			v.bad(mp, "_base / _skipped are not JSON")
			return
		}
		for _, x := range secrets {
			if x == nil {
				v.bad(mp, "_base / _skipped are not JSON")
				return
			}
			if s, ok := vObj(x).Val("value").(string); ok && !strings.HasPrefix(s, "sha256:") {
				v.bad(mp, "_base holds a plain secret value")
				break
			}
		}
	}
	sk, err := doc.ParseString(shared.JSString(vCoal(vGet(mb, "_skipped"), "[]")))
	if err != nil {
		v.bad(mp, "_base / _skipped are not JSON")
		return
	}
	list, isList := sk.([]any)
	if !isList {
		v.bad(mp, "_skipped must be [{ key, theirsHash, … }]")
		return
	}
	for _, x := range list {
		if x == nil {
			v.bad(mp, "_base / _skipped are not JSON")
			return
		}
		if !validSkip(x) {
			v.bad(mp, "_skipped must be [{ key, theirsHash, … }]")
			return
		}
	}
}

func validSkip(x any) bool {
	r := vObj(x)
	return vIsStr(r, "key") && vIsStr(r, "theirsHash")
}

// entitySlugs: template / deployment slugs are present, valid and unique per collection (deleted deployments included).
func (v *verifier) entitySlugs() {
	for _, coll := range []string{"templates", "deployments"} {
		seen := map[string]string{}
		for _, d := range v.docs(v.db.Collection(coll).Select("slug")) {
			sl := vGet(d.Data(), "slug")
			s, ok := sl.(string)
			if !ok || !shared.RE.Slug.MatchString(s) {
				v.bad(coll+"/"+d.ID(), "slug missing or invalid ("+vJSON(vCoal(sl, nil))+")")
			} else if o, dup := seen[s]; dup {
				v.bad(coll+"/"+d.ID(), "slug "+s+" is also used by "+coll+"/"+o)
			} else {
				seen[s] = d.ID()
			}
		}
	}
}

// githubMirror: owner / repo format, enabled ⇒ configured.
func (v *verifier) githubMirror() {
	const p = "githubMirror/config"
	g := vMust(v.db.Collection("githubMirror").Doc("config").Get()).Data()
	if g == nil {
		return
	}
	if g.Truthy("owner") && !reVerifyGHOwner.MatchString(vStr(g, "owner")) {
		v.bad(p, "owner has a bad format")
	}
	if g.Truthy("repo") && !reVerifyGHRepo.MatchString(vStr(g, "repo")) {
		v.bad(p, "repo has a bad format")
	}
	if g.Truthy("enabled") && !(g.Truthy("owner") && g.Truthy("repo") && g.Truthy("token")) {
		v.bad(p, "enabled without owner / repo / token")
	}
}

func (v *verifier) templates() {
	for _, d := range v.docs(v.db.Collection("templates")) {
		t := d.Data()
		p := "templates/" + d.ID()
		v.iconIs(p, vCoal(vGet(t, "icon"), nil), false)
		v.iconRef(p, vGet(t, "icon"))
		var body any
		if s, ok := t.Val("_body").(string); ok {
			body = vParse(s)
		}
		if !doc.Truthy(body) {
			v.bad(p, "_body missing")
		} else {
			v.configErrors(p, body, "template", "template")
		}
		v.branchIs(p, t, d.Ref(), "template")
	}
}

func (v *verifier) installTokens(p string, ref *driver.DocRef) {
	enrolledAt := map[string]any{}
	for _, a := range v.docs(ref.Collection("agents")) {
		enrolledAt[a.ID()] = vGet(a.Data(), "enrolledAt")
	}
	for _, t := range v.docs(ref.Collection("installTokens")) {
		tk := t.Data()
		tp := p + "/installTokens/" + t.ID()
		var current any
		if slug, ok := tk.Val("siteSlug").(string); ok {
			current = vCoal(enrolledAt[slug], nil)
		}
		kind, token := vGet(tk, "kind"), vGet(tk, "token")
		maintenance := kind == "maintenance"
		if kind != doc.Undefined && kind != "install" && !maintenance {
			v.bad(tp, "kind must be install | maintenance")
		}
		if maintenance && (!vSame(vGet(tk, "expiresAt"), nil) || !vIsStr(tk, "boundEnrolledAt") || tk.Truthy("usedAt")) {
			v.bad(tp, "a maintenance token has expiresAt null, a boundEnrolledAt and no usedAt")
		}
		s, isStr := token.(string)
		switch {
		case !tk.Has("token"):
			v.bad(tp, "token missing")
		case token != nil && (!isStr || !strings.HasPrefix(s, "opi_")):
			v.bad(tp, `token must be "opi_…" or null`)
		case token != nil && tk.Truthy("revokedAt"):
			v.bad(tp, "a revoked token still holds its plaintext")
		case token != nil && tk.Truthy("usedAt") && !vSame(vGet(tk, "usedAt"), current):
			v.bad(tp, "a used token of an ended enrollment still holds its plaintext")
		case token != nil && maintenance && !vSame(vGet(tk, "boundEnrolledAt"), current):
			v.bad(tp, "a maintenance token of an ended enrollment still holds its plaintext")
		}
	}
}

func (v *verifier) deployments() {
	for _, d := range v.docs(v.db.Collection("deployments")) {
		x := d.Data()
		p := "deployments/" + d.ID()
		v.iconIs(p, vCoal(vGet(x, "image"), nil), true)
		v.iconRef(p, vGet(x, "image"))
		if !x.Has("accessKey") {
			v.bad(p, "accessKey missing")
		} else if k := x.Val("accessKey"); k != nil {
			if s, ok := k.(string); !ok || !reVerifyAccessKey.MatchString(s) {
				v.bad(p, `accessKey must be "XXXX-…" (24 base32 chars, grouped) or null`)
			}
		}
		v.installTokens(p, d.Ref())
		draft, hasDraft := x.Val("_draft").(string)
		if hasDraft && !x.Truthy("deleted") {
			v.configErrors(p+" draft", vParse(draft), "draft", d.ID())
		}
		if s, ok := x.Val("_srcSnapshot").(string); ok {
			v.storedConfig(p+" srcTemplate", vParse(s))
		}
		src := x.Truthy("srcTemplate")
		if s, ok := x.Val("_srcBase").(string); ok && src {
			v.storedConfig(p+" srcTemplate base", vParse(s))
			if !vIsNum(x.Map("srcTemplate"), "baseVersion") {
				v.bad(p, "srcTemplate.baseVersion missing next to _srcBase")
			}
		}
		if sk := vGet(x, "_srcSkipped"); src && !vNull(sk) {
			list, isList := vParse(shared.JSString(sk)).([]any)
			if !isList || slices.ContainsFunc(list, func(x any) bool { return !validSkip(x) }) {
				v.bad(p, "_srcSkipped must be [{ key, theirsHash, … }]")
			}
		}
		if !src && (x.Truthy("_srcBase") || x.Truthy("_srcSkipped")) {
			v.bad(p, "unlinked deployment still holds _srcBase / _srcSkipped")
		}
		if hasDraft && x.Truthy("deleted") {
			v.shapeConfig(p+" draft", vParse(draft))
			v.shapeVolumeContent(p+" draft", vParse(draft))
		}
		v.branchIs(p, x, d.Ref(), "deployment")
		for _, ab := range v.docs(d.Ref().Collection("agentBranches")) {
			a := ab.Data()
			ap := p + "/agentBranches/" + ab.ID()
			if !vSame(vGet(a, "siteSlug"), ab.ID()) {
				v.bad(ap, "siteSlug ≠ doc id")
			}
			if _, ok := a.Val("legacy").(bool); !ok {
				v.bad(ap, "legacy must be a boolean")
			}
			v.commitChain(ap+"/commits", ab.Ref().Collection("commits"), false, true)
		}
		for _, r := range v.docs(d.Ref().Collection("revisions")) {
			v.revision(p+"/revisions/"+r.ID(), r.Data())
		}
	}
}

// storedConfig: the checks of a config that is kept but never validated (srcTemplate snapshot and merge base).
func (v *verifier) storedConfig(path string, cfg any) {
	v.noOldKeys(path, cfg)
	v.shapeConfig(path, cfg)
	v.shapeVolumeContent(path, cfg)
}

func (v *verifier) revision(rp string, rv *doc.Map) {
	cfgS, ok1 := rv.Val("_config").(string)
	catS, ok2 := rv.Val("_catalog").(string)
	if !ok1 || !ok2 {
		v.bad(rp, "_config / _catalog missing")
		return
	}
	config, catalog := vParse(cfgS), vParse(catS)
	v.noOldKeys(rp, config)
	v.shapeConfig(rp, config)
	v.noOldKeys(rp+" catalog", catalog)
	if !vSame(vGet(rv, "hash"), vHash(config)) {
		v.bad(rp, "hash ≠ sha256(JCS(config))")
	}
	if !vSame(vGet(rv, "catalogHash"), vHash(catalog)) {
		v.bad(rp, "catalogHash ≠ sha256(JCS(catalog))")
	}
	cat := vObj(catalog)
	for _, x := range vObj(config).Arr("sites") {
		site := vObj(x)
		for _, y := range site.Map("config").Arr("containers") {
			c := vObj(y)
			if !vIsStr(c, "serviceSlug") || !vIsStr(c, "version") {
				v.bad(rp, "site "+vStr(site, "siteSlug")+": container without serviceSlug / version")
			} else if key := shared.ImageKey(c.Str("serviceSlug"), c.Str("version")); !cat.Map("images").Truthy(key) {
				v.bad(rp, "catalog has no image "+c.Str("serviceSlug")+":"+c.Str("version"))
			}
		}
	}
	cat.Map("services").Each(func(k string, x any) {
		if !vSame(vGet(vObj(x), "serviceSlug"), k) {
			v.bad(rp, "catalog service "+k+": serviceSlug ≠ key")
		}
	})
	cat.Map("images").Each(func(k string, x any) {
		o := vObj(x)
		if vStr(o, "serviceSlug")+":"+vStr(o, "version") != k {
			v.bad(rp, "catalog image "+k+": serviceSlug:version ≠ key")
		}
	})
}

func (v *verifier) accounts() {
	for _, d := range v.docs(v.db.Collection("accounts")) {
		a := d.Data()
		p := "accounts/" + d.ID()
		v.iconIs(p, vCoal(vGet(a, "img"), nil), true)
		v.iconRef(p, vGet(a, "img"))
		for _, k := range []string{"lastSetPassword", "lastResetLink"} {
			if !a.Has(k) {
				v.bad(p, k+" missing")
			} else if x := a.Val(k); x != nil {
				if o := vObj(x); o == nil || !vIsStr(o, "value") || !vIsStr(o, "setAt") || !vIsStr(o, "setBy") {
					v.bad(p, k+" must be { value, setAt, setBy, … } or null")
				}
			}
		}
	}
}

func (v *verifier) icons() {
	for _, d := range v.docs(v.db.Collection("icons")) {
		i := d.Data()
		p := "icons/" + d.ID()
		if !vSame(vGet(i, "iconId"), d.ID()) {
			v.bad(p, "iconId ≠ doc id")
		}
		if ct, ok := i.Val("contentType").(string); !ok || !slices.Contains(shared.ICON_CONTENT_TYPES, ct) {
			v.bad(p, "contentType")
		}
		b, _ := i.Val("_bytes").(doc.Bytes)
		if n, ok := i.NumOK("sizeBytes"); len(b) == 0 || len(b) > shared.ICON_MAX_BYTES || !ok || n != float64(len(b)) {
			v.bad(p, "bytes missing / size mismatch")
		}
	}
}
