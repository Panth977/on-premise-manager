package routes

import (
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"listRegistries": listRegistries, "createRegistry": createRegistry, "getRegistry": getRegistry, "patchRegistry": patchRegistry, "deleteRegistry": deleteRegistry,
		"revealRegistryPassword": revealRegistryPassword, "testRegistry": testRegistry, "listRegistryRepositories": listRegistryRepositories, "listRegistryTags": listRegistryTags,
		"listServices": listServices, "createService": createService, "getService": getService, "patchService": patchService, "deleteService": deleteService, "serviceUsage": serviceUsage,
		"listImages": listImages, "inspectImage": inspectImage, "registerImage": registerImage, "getImage": getImage, "patchImage": patchImage, "deleteImage": deleteImage, "retryImport": retryImport, "imageUsage": imageUsage,
		"listNotFoundPages": listNotFoundPages, "createNotFoundPage": createNotFoundPage, "getNotFoundPage": getNotFoundPage, "deleteNotFoundPage": deleteNotFoundPage,
		"listTags": listTags, "createTag": createTag, "patchTag": patchTag, "deleteTag": deleteTag,
	})
}

// ───────────────────────────────────────── helpers of the route files catalog, icons, templates, blobs, transfers, files

// aFld is m[k]: doc.Undefined when the key is missing.
func aFld(m *doc.Map, k string) any {
	if v, ok := m.Get(k); ok {
		return v
	}
	return doc.Undefined
}

// aNullish is `v === null || v === undefined`.
func aNullish(v any) bool { return v == nil || v == doc.Undefined }

// aOr is m[k] ?? def.
func aOr(m *doc.Map, k string, def any) any {
	if v := aFld(m, k); !aNullish(v) {
		return v
	}
	return def
}

// aIsInt is Number.isInteger.
func aIsInt(v any) bool {
	f, ok := v.(float64)
	return ok && f == math.Trunc(f) && !math.IsInf(f, 0)
}

// aProp is x[k] of a parsed JSON item that is known to be an object (or an array: no such key).
func aProp(x any, k string) any {
	if m, ok := x.(*doc.Map); ok {
		return aFld(m, k)
	}
	return doc.Undefined
}

// aIsObject is !!x && typeof x === 'object'.
func aIsObject(x any) bool {
	switch m := x.(type) {
	case *doc.Map:
		return m != nil
	case []any:
		return true
	}
	return false
}

// aKeys is Object.keys of an object or an array.
func aKeys(x any) []string {
	switch m := x.(type) {
	case *doc.Map:
		return m.Keys()
	case []any:
		out := make([]string, len(m))
		for i := range m {
			out[i] = strconv.Itoa(i)
		}
		return out
	}
	return nil
}

// aEmail is c.account!.email.
func aEmail(c *cloud.Ctx) string {
	if c.Account.Email != nil {
		return *c.Account.Email
	}
	return ""
}

// aEmailOr is c.account!.email ?? def.
func aEmailOr(c *cloud.Ctx, def any) any {
	if c.Account.Email != nil {
		return *c.Account.Email
	}
	return def
}

// aMaskSecrets is secrets.map((s) => ({ ...s, value: null, rotatedAt: null })).
func aMaskSecrets(l []any) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s.(*doc.Map).Shallow().Set("value", nil).Set("rotatedAt", nil)
	}
	return out
}

var (
	aCollator = collate.New(language.AmericanEnglish)
	aCollMu   sync.Mutex
)

// aLocaleCompare is a.localeCompare(b).
func aLocaleCompare(a, b string) int {
	aCollMu.Lock()
	defer aCollMu.Unlock()
	return aCollator.CompareString(a, b)
}

// aSort is a stable sort of a list of objects.
func aSort(l []any, cmp func(a, b *doc.Map) int) {
	sort.SliceStable(l, func(i, j int) bool { return cmp(l[i].(*doc.Map), l[j].(*doc.Map)) < 0 })
}

// aSortBy sorts by a string field with localeCompare.
func aSortBy(l []any, k string) {
	aSort(l, func(a, b *doc.Map) int { return aLocaleCompare(a.Str(k), b.Str(k)) })
}

// ───────────────────────────────────────── catalog helpers

func catAudited(uid string) *doc.Map {
	t := doc.NowISO()
	return doc.Obj("createdAt", t, "createdBy", uid, "updatedAt", t, "updatedBy", uid)
}
func catTouched(uid string) *doc.Map { return doc.Obj("updatedAt", doc.NowISO(), "updatedBy", uid) }

var catPatchImageKeys = []string{"reference", "registryId", "kind", "entrypoint", "command", "workdir", "user", "ports", "env", "volumes", "healthcheck", "stopTimeoutSec", "description", "renames", "renameEverywhere"}

// catHas is the has(o, k) of catalog.ts.
func catHas(x any, k string) bool {
	m, ok := x.(*doc.Map)
	return ok && m.Has(k)
}

// catPartial: partial.length && partial.every(Boolean), partial.some(Boolean) of a ports / env list.
func catPartial(l []any, k1, k2 string) (all, some bool) {
	all = len(l) > 0
	for _, x := range l {
		if aIsObject(x) && !catHas(x, k1) && !catHas(x, k2) {
			some = true
		} else {
			all = false
		}
	}
	return all, some
}

// catExtra is Object.keys(x).filter(not allowed).
func catExtra(x any, allowed ...string) []string {
	var out []string
next:
	for _, k := range aKeys(x) {
		for _, a := range allowed {
			if k == a {
				continue next
			}
		}
		out = append(out, k)
	}
	return out
}

// catFindName is list.find((x) => x.name === name).
func catFindName(l []any, name any) *doc.Map {
	s, ok := name.(string)
	if !ok {
		return nil
	}
	for _, x := range l {
		if m, ok := x.(*doc.Map); ok && m.Val("name") == s {
			return m
		}
	}
	return nil
}

// catNameOrIndex is `${x.name ?? i}`.
func catNameOrIndex(name any, i int) string {
	if aNullish(name) {
		return strconv.Itoa(i)
	}
	return core.JSString(name)
}

// catApplyImageEdits is applyImageEdits: merge a PatchImageRequest into next (no validation of values here —
// validateImageSpec runs on the result). ports / env / volumes: full lists replace; the legacy partial forms (every port
// item only { name, description }, every env item without required + secret) merge by name (unknown name → V_REF). Mixed
// forms → V_TYPE.
func catApplyImageEdits(next, p *doc.Map) error {
	v := &cloud.V{}
	for _, k := range []string{"kind", "entrypoint", "command"} {
		if x, ok := p.Get(k); ok {
			next.Set(k, x)
		}
	}
	for _, k := range []string{"workdir", "user"} {
		if x, ok := p.Get(k); ok {
			if x == "" {
				x = nil
			}
			next.Set(k, x)
		}
	}
	for _, k := range []string{"description", "healthcheck", "stopTimeoutSec"} {
		if x, ok := p.Get(k); ok {
			next.Set(k, x)
		}
	}
	if x, ok := p.Get("ports"); ok {
		if err := catEditPorts(v, next, x); err != nil {
			return err
		}
	}
	if x, ok := p.Get("env"); ok {
		if err := catEditEnv(v, next, x); err != nil {
			return err
		}
	}
	if x, ok := p.Get("volumes"); ok {
		if l, isList := x.([]any); !isList {
			v.Err("volumes", "V_TYPE", "volumes must be a list")
		} else {
			// kind: kept as sent (validateImageSpec checks it); absent → inferred from the path after validation (§21 #83)
			out := make([]any, len(l))
			for i, e := range l {
				out[i] = e
				if aIsObject(e) {
					d := aProp(e, "description")
					if aNullish(d) {
						d = ""
					}
					out[i] = doc.Obj("name", aProp(e, "name"), "mountPath", aProp(e, "mountPath"), "kind", aProp(e, "kind"), "description", d)
				}
			}
			next.Set("volumes", out)
		}
	}
	if x, ok := p.Get("renames"); ok {
		if _, isList := x.([]any); !isList {
			v.Err("renames", "V_TYPE", "renames must be a list of { kind, from, to }")
		}
	}
	return v.ThrowIf()
}

func catEditPorts(v *cloud.V, next *doc.Map, x any) error {
	l, ok := x.([]any)
	if !ok {
		v.Err("ports", "V_TYPE", "ports must be a list")
		return nil
	}
	all, some := catPartial(l, "port", "protocol")
	switch {
	case all:
		for i, pp := range l {
			name := aProp(pp, "name")
			if extra := catExtra(pp, "name", "description"); len(extra) > 0 {
				v.Err("ports["+catNameOrIndex(name, i)+"]", "V_TYPE", "unknown fields "+strings.Join(extra, ", "))
				continue
			}
			t := catFindName(next.Arr("ports"), name)
			if t == nil {
				v.Err("ports["+core.JSString(name)+"]", "V_REF", "unknown port "+core.JSString(name))
				continue
			}
			t.Set("description", aProp(pp, "description"))
		}
	case some:
		v.Err("ports", "V_TYPE", "ports: send the full list ({ name, port, protocol, description }) or only { name, description } items, not both")
	default:
		out := make([]any, len(l))
		for i, e := range l {
			out[i] = e
			if aIsObject(e) {
				d := aProp(e, "description")
				if aNullish(d) {
					d = ""
				}
				out[i] = doc.Obj("name", aProp(e, "name"), "port", aProp(e, "port"), "protocol", aProp(e, "protocol"), "description", d)
			}
		}
		next.Set("ports", out)
	}
	return nil
}

func catEditEnv(v *cloud.V, next *doc.Map, x any) error {
	l, ok := x.([]any)
	if !ok {
		v.Err("env", "V_TYPE", "env must be a list")
		return nil
	}
	all, some := catPartial(l, "required", "secret")
	switch {
	case all:
		for i, pe := range l {
			name := aProp(pe, "name")
			if extra := catExtra(pe, "name", "value", "options", "description"); len(extra) > 0 {
				v.Err("env["+catNameOrIndex(name, i)+"]", "V_TYPE", "unknown fields "+strings.Join(extra, ", "))
				continue
			}
			t := catFindName(next.Arr("env"), name)
			if t == nil {
				v.Err("env["+core.JSString(name)+"]", "V_REF", "unknown env "+core.JSString(name))
				continue
			}
			options := aProp(pe, "options")
			if options != doc.Undefined {
				t.Set("options", options)
			}
			val := aProp(pe, "value")
			if val == nil {
				t.Delete("value")
			} else if _, isList := val.([]any); isList {
				n, err := shared.NormalizeImageEnv(t.Shallow().Set("value", val))
				if err != nil {
					return err
				}
				if aNullish(options) {
					options = aFld(n, "options")
				}
				t.Set("options", options)
				if nv := aFld(n, "value"); nv != doc.Undefined {
					t.Set("value", nv)
				}
			} else if val != doc.Undefined {
				t.Set("value", val)
			}
			if d := aProp(pe, "description"); d != doc.Undefined {
				t.Set("description", d)
			}
		}
	case some:
		v.Err("env", "V_TYPE", "env: send the full list (with required + secret) or only partial { name, value?, options?, description? } items, not both")
	default:
		out := make([]any, len(l))
		for i, e := range l {
			out[i] = e
			if !aIsObject(e) {
				continue
			}
			val, options, d := aProp(e, "value"), aProp(e, "options"), aProp(e, "description")
			if aNullish(val) {
				val = doc.Undefined
			}
			if aNullish(options) {
				options = nil
			}
			if aNullish(d) {
				d = ""
			}
			o, err := shared.NormalizeImageEnv(doc.Obj("name", aProp(e, "name"), "value", val, "options", options, "required", aProp(e, "required"), "secret", aProp(e, "secret"), "description", d))
			if err != nil {
				return err
			}
			out[i] = o
		}
		next.Set("env", out)
	}
	return nil
}

// ───────────────────────────────────────── registries

func catValidateRegistryFields(v *cloud.V, b *doc.Map, create bool) {
	if create || b.Has("label") {
		cloud.VLabel(v, aFld(b, "label"), "label", cloud.Limits["label"], true)
	}
	if b.Has("url") {
		if s, ok := b.StrOK("url"); !ok || !shared.RE.HTTPSURL.MatchString(strings.TrimSuffix(s, "/")) {
			v.Err("url", "V_FORMAT", "url must be an https URL without a path (e.g. https://registry-1.docker.io)")
		}
	}
	// "" + "" = anonymous (public images)
	if create || b.Has("username") {
		cloud.VString(v, aFld(b, "username"), "username", 0, 255)
	}
	if create || b.Has("password") {
		cloud.VString(v, aFld(b, "password"), "password", 0, 8192)
	}
}

// catCheckRegistryPair: username and password are both empty (anonymous) or both set.
func catCheckRegistryPair(username, password string) error {
	if (username == "") != (password == "") {
		path := "username"
		if username != "" {
			path = "password"
		}
		return cloud.E.Bad(path, "V_REQUIRED", "username and password are both required (or both empty for anonymous access)")
	}
	return nil
}

func catLoadRegistry(id string) (*doc.Map, error) {
	s, err := cloud.Col.Registries().Doc(id).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("registry not found")
	}
	return s.Data(), nil
}

// catCreds is the registry document as the RegistryCreds it is used as.
func catCreds(r *doc.Map) core.RegistryCreds {
	return core.RegistryCreds{URL: r.Str("url"), Username: r.Str("username"), Password: r.Str("password")}
}

// catRegistryRepository is registryRepository: a repository an image of this registry uses (scope for the default login
// test), or "".
func catRegistryRepository(registryID string) (string, error) {
	imgs, err := cloud.DB().CollectionGroup("images").Where("registryId", "==", registryID).Limit(1).Get()
	if err != nil || imgs.Empty() {
		return "", err
	}
	return imgs.Docs[0].Data().Map("source").Str("repository"), nil
}

func catVRepository(v *cloud.V, val any, path string, required bool) {
	if aNullish(val) {
		if required {
			v.Err(path, "V_REQUIRED", path+" is required")
		}
		return
	}
	if s, ok := val.(string); !ok || !shared.RE.Repository.MatchString(s) || doc.JSLen(s) > 255 {
		v.Err(path, "V_FORMAT", path+` must look like "library/redis", "acme/api" or "project/repo/image" (lower case)`)
	}
}

func listRegistries(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "registries", "read", nil); err != nil {
		return nil, err
	}
	q, err := cloud.Col.Registries().Get()
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(q.Docs))
	for _, d := range q.Docs {
		items = append(items, core.RegistryView(d.Data()))
	}
	aSortBy(items, "label")
	return doc.Obj("items", items), nil
}

func createRegistry(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "registries", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"label", "url", "username", "password"}); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	catValidateRegistryFields(v, b, true)
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	if err := catCheckRegistryPair(b.Str("username"), b.Str("password")); err != nil {
		return nil, err
	}
	ref := cloud.Col.Registries().Doc()
	url := "https://registry-1.docker.io"
	if s, ok := b.StrOK("url"); ok {
		url = strings.TrimSuffix(s, "/")
	}
	d := doc.Obj("registryId", ref.ID(), "label", doc.JSTrim(b.Str("label")), "url", url, "username", b.Val("username"), "password", b.Val("password"), "lastCheck", nil).Spread(catAudited(c.Account.UID))
	if err := ref.Create(d); err != nil {
		return nil, err
	}
	return core.RegistryView(d), nil
}

func getRegistry(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "registries", "read", nil); err != nil {
		return nil, err
	}
	r, err := catLoadRegistry(c.Params["registryId"])
	if err != nil {
		return nil, err
	}
	return core.RegistryView(r), nil
}

func patchRegistry(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "registries", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"label", "url", "username", "password"}, "registryId", "lastCheck", "createdAt", "createdBy"); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	catValidateRegistryFields(v, b, false)
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	ref := cloud.Col.Registries().Doc(c.Params["registryId"])
	out, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, cloud.E.NotFound("registry not found")
		}
		cur := s.Data()
		next := cur.Shallow().Spread(catTouched(c.Account.UID))
		if b.Has("label") {
			next.Set("label", doc.JSTrim(b.Str("label")))
		}
		if b.Has("url") {
			next.Set("url", strings.TrimSuffix(b.Str("url"), "/"))
		}
		if b.Has("username") {
			next.Set("username", b.Val("username"))
		}
		if b.Has("password") {
			next.Set("password", b.Val("password"))
		}
		if err := catCheckRegistryPair(next.Str("username"), next.Str("password")); err != nil {
			return nil, err
		}
		if next.Val("url") != cur.Val("url") || next.Val("username") != cur.Val("username") || next.Val("password") != cur.Val("password") {
			next.Set("lastCheck", nil)
		}
		tx.Set(ref, next)
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	return core.RegistryView(out), nil
}

func deleteRegistry(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "registries", "write", nil); err != nil {
		return nil, err
	}
	id := c.Params["registryId"]
	if _, err := catLoadRegistry(id); err != nil {
		return nil, err
	}
	imgs, err := cloud.DB().CollectionGroup("images").Where("registryId", "==", id).Get()
	if err != nil {
		return nil, err
	}
	if !imgs.Empty() {
		usedBy := make([]any, 0, len(imgs.Docs))
		for _, d := range imgs.Docs {
			usedBy = append(usedBy, doc.Obj("serviceSlug", d.Ref().Parent().Parent().ID(), "version", d.ID()))
		}
		return nil, cloud.E.Conflict("the registry is used by images", doc.Obj("usedBy", usedBy))
	}
	if err := cloud.Col.Registries().Doc(id).Delete(); err != nil {
		return nil, err
	}
	return doc.Obj("deleted", true), nil
}

func revealRegistryPassword(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "registries", "write", nil); err != nil {
		return nil, err
	}
	r, err := catLoadRegistry(c.Params["registryId"])
	if err != nil {
		return nil, err
	}
	// audited by the router hook (registry.password-revealed); no-store + rate limit in the router (§21 #84)
	return doc.Obj("value", aFld(r, "password"), "password", aFld(r, "password"), "hideAfterSec", float64(shared.REVEAL_SECONDS)), nil
}

func testRegistry(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "registries", "write", nil); err != nil {
		return nil, err
	}
	id := c.Params["registryId"]
	// optional JSON body { repository?: string } (not in the shared contract: body 'none'): check pull access to it
	asked := ""
	if len(c.Body) > 0 {
		b, err := c.BodyMap()
		if err != nil {
			return nil, err
		}
		if err := cloud.OnlyKeys(b, []string{"repository"}); err != nil {
			return nil, err
		}
		v := &cloud.V{}
		catVRepository(v, aFld(b, "repository"), "repository", false)
		if err := v.ThrowIf(); err != nil {
			return nil, err
		}
		asked = b.Str("repository")
	}
	r, err := catLoadRegistry(id)
	if err != nil {
		return nil, err
	}
	repo := asked
	if repo == "" {
		if repo, err = catRegistryRepository(id); err != nil {
			return nil, err
		}
	}
	scope := ""
	if repo != "" {
		scope = "repository:" + core.NormalizeRepository(r.Str("url"), repo) + ":pull"
	}
	creds := catCreds(r)
	res := core.NewRegistryClient(creds, 0).Test(scope, asked != "")
	// Docker Hub: GET /v2/ answers 200 for any token; the access claim of a repository token is the real check
	if res.Truthy("accessDenied") {
		res.Set("message", core.AccessHint(creds, res.Str("message"), core.BrowseOpts{}))
	}
	at := doc.NowISO()
	if err := cloud.Col.Registries().Doc(id).Update(doc.Obj("lastCheck", doc.Obj("at", at, "ok", res.Val("ok"), "message", res.Val("message")))); err != nil {
		return nil, err
	}
	return doc.Obj("ok", res.Val("ok"), "message", res.Val("message"), "at", at), nil
}

// catQ is (c.query.get('q') ?? ”).trim().slice(0, 200).
func catQ(c *cloud.Ctx) string { return doc.JSSlice(doc.JSTrim(c.Query.Get("q")), 200) }

func listRegistryRepositories(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "write", nil); err != nil {
		return nil, err
	}
	r, err := catLoadRegistry(c.Params["registryId"])
	if err != nil {
		return nil, err
	}
	return core.ListRepositories(catCreds(r), catQ(c), core.BrowseOpts{}), nil
}

func listRegistryTags(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "write", nil); err != nil {
		return nil, err
	}
	repository := doc.JSTrim(c.Query.Get("repository"))
	v := &cloud.V{}
	catVRepository(v, repository, "repository", true)
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	r, err := catLoadRegistry(c.Params["registryId"])
	if err != nil {
		return nil, err
	}
	return core.ListTags(catCreds(r), core.NormalizeRepository(r.Str("url"), repository), catQ(c), core.BrowseOpts{}), nil
}

// ───────────────────────────────────────── services

func catValidateServiceFields(v *cloud.V, b *doc.Map, create bool) error {
	if create || b.Has("label") {
		cloud.VLabel(v, aFld(b, "label"), "label", cloud.Limits["label"], true)
	}
	if create {
		cloud.VSlug(v, aFld(b, "serviceSlug"), "serviceSlug")
	}
	core.VIconFormat(v, aFld(b, "icon"), "icon", false)
	cloud.VDescription(v, aFld(b, "description"), "description")
	if v.OK() {
		return core.VIconRefs(v, []core.IconField{{Path: "icon", Value: aFld(b, "icon")}})
	}
	return nil
}

func catLoadService(slug string) (*doc.Map, error) {
	s, err := cloud.Col.Services().Doc(slug).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("service not found")
	}
	return core.ServiceView(s.Data(), s.ID()), nil
}

func catLoadImage(serviceSlug, version string) (*doc.Map, error) {
	s, err := cloud.Col.Images(serviceSlug).Doc(version).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("image not found")
	}
	return core.ImageView(s.Data(), s.ID(), serviceSlug)
}

// catSource is what resolveSource returns.
type catSource struct {
	source, reg *doc.Map
	registryID  any
}

// catResolveSource is resolveSource: parse a reference + resolve registryId (undefined → match by host, first by label;
// null → public). nil + no error: an issue was added to v.
func catResolveSource(v *cloud.V, reference, registryID any) (*catSource, error) {
	pr := shared.ParseImageReference(reference)
	if !pr.Bool("ok") {
		v.Err("reference", "V_REFERENCE", pr.Str("error"))
		return nil, nil
	}
	parts := pr.Map("parts")
	s, isStr := registryID.(string)
	if !aNullish(registryID) && (!isStr || s == "") {
		v.Err("registryId", "V_TYPE", "registryId must be a registry id or null")
		return nil, nil
	}
	var reg *doc.Map
	if isStr {
		r, err := catLoadRegistry(s)
		if err != nil {
			return nil, err
		}
		reg = r
		if h := shared.RegistryHost(reg.Str("url")); h != parts.Str("host") {
			v.Err("registryId", "V_REF", `the registry "`+reg.Str("label")+`" is for `+h+", but the reference is on "+parts.Str("host"))
			return nil, nil
		}
	} else if registryID == doc.Undefined {
		q, err := cloud.Col.Registries().Get()
		if err != nil {
			return nil, err
		}
		all := make([]any, 0, len(q.Docs))
		for _, d := range q.Docs {
			all = append(all, d.Data())
		}
		m, err := shared.MatchRegistries(parts.Str("host"), all)
		if err != nil {
			return nil, err
		}
		if len(m) > 0 {
			reg, _ = m[0].(*doc.Map)
		}
	}
	out := &catSource{source: parts, reg: reg}
	if reg != nil {
		out.registryID = aOr(reg, "registryId", nil)
	}
	return out, nil
}

// ───────────────────────────────────────── inspect helpers (§6.1)

var (
	catReExposed    = regexp.MustCompile(`^([0-9]+)(?:/(tcp|udp))?$`)
	catReVolBad     = regexp.MustCompile(`[^a-z0-9-]+`)
	catReVolLead    = regexp.MustCompile(`^[^a-z]+`)
	catReVolTail    = regexp.MustCompile(`-+$`)
	catRePlatform   = regexp.MustCompile(`platform .* not in the image`)
	catLowerSpecial = strings.NewReplacer("İ", "i̇") // the one case toLowerCase and strings.ToLower differ on a-z
)

// catLenTruthy is x?.length ? x : null.
func catLenTruthy(x any) any {
	switch l := x.(type) {
	case []any:
		if len(l) > 0 {
			return x
		}
	case string:
		if l != "" {
			return x
		}
	}
	return nil
}

// catPrefillFromConfig is prefillFromConfig.
func catPrefillFromConfig(cfg *doc.Map) (*doc.Map, error) {
	c := cfg.Map("config")
	ports := []any{}
	portName := func(n string) bool {
		for _, p := range ports {
			if p.(*doc.Map).Val("name") == n {
				return true
			}
		}
		return false
	}
	for _, k := range c.Map("ExposedPorts").Keys() {
		m := catReExposed.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		port, _ := strconv.ParseFloat(m[1], 64)
		protocol := m[2]
		if protocol == "" {
			protocol = "tcp"
		}
		name := "p" + core.JSString(port)
		if protocol == "udp" && portName(name) {
			name += "-udp"
		}
		if port >= 1 && port <= 65535 && !portName(name) {
			ports = append(ports, doc.Obj("name", name, "port", port, "protocol", protocol, "description", ""))
		}
	}
	env := []any{}
	for _, x := range c.Arr("Env") {
		kv, ok := x.(string)
		if !ok {
			return nil, errors.New("kv.indexOf is not a function")
		}
		name, value, _ := strings.Cut(kv, "=")
		if !shared.RE.EnvName.MatchString(name) || catFindName(env, name) != nil {
			continue
		}
		env = append(env, doc.Obj("name", name, "value", doc.JSSlice(value, cloud.Limits["value"]), "required", false, "secret", false, "description", ""))
	}
	// volume names from the path ("/var/lib/data" → "data"), so they do not read like image versions (v1); unique, slug-safe
	used := map[string]bool{}
	volumes := []any{}
	for i, mountPath := range c.Map("Volumes").Keys() {
		last := ""
		for _, p := range strings.Split(mountPath, "/") {
			if p != "" {
				last = p
			}
		}
		base := strings.ToLower(catLowerSpecial.Replace(last))
		base = catReVolBad.ReplaceAllString(base, "-")
		base = catReVolLead.ReplaceAllString(base, "")
		base = catReVolTail.ReplaceAllString(base, "")
		if len(base) > 32 {
			base = base[:32]
		}
		if len(base) < 2 {
			base = "volume-" + strconv.Itoa(i+1)
		}
		name := base
		for n := 2; used[name]; n++ {
			name = base + "-" + strconv.Itoa(n)
		}
		used[name] = true
		volumes = append(volumes, doc.Obj("name", name, "mountPath", mountPath, "kind", "dir", "description", "")) // docker VOLUME entries are folders (§21 #83)
	}
	var workdir, user any
	if c.Truthy("WorkingDir") {
		workdir = c.Val("WorkingDir")
	}
	if c.Truthy("User") {
		user = c.Val("User")
	}
	return doc.Obj("entrypoint", catLenTruthy(c.Val("Entrypoint")), "command", catLenTruthy(c.Val("Cmd")), "workdir", workdir, "user", user, "ports", ports, "env", env, "volumes", volumes), nil
}

func catRegistryErr(err error) error {
	if a := core.IsRegistryAccessError(err); a != nil {
		return cloud.E.Unavailable(a.Message, doc.Obj("repository", a.Repository))
	}
	var e *core.RegistryError
	if errors.As(err, &e) {
		if e.Status == 404 {
			return cloud.E.NotFound(e.Message)
		}
		if catRePlatform.MatchString(e.Message) {
			return cloud.E.Bad("platform", "V_ENUM", e.Message)
		}
		return cloud.E.Unavailable(e.Message)
	}
	return err
}

func listServices(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "services", "read", nil); err != nil {
		return nil, err
	}
	svc, err := cloud.Col.Services().Get()
	if err != nil {
		return nil, err
	}
	imgs, err := cloud.DB().CollectionGroup("images").Select().Get()
	if err != nil {
		return nil, err
	}
	entries, _, err := core.ScanUsage(false)
	if err != nil {
		return nil, err
	}
	counts := map[string]float64{}
	for _, d := range imgs.Docs {
		counts[d.Ref().Parent().Parent().ID()]++
	}
	usedBy := map[string]map[string]bool{}
	for _, u := range entries {
		if u.Val("kind") == "deployment" && u.Truthy("serviceSlug") {
			s := u.Str("serviceSlug")
			if usedBy[s] == nil {
				usedBy[s] = map[string]bool{}
			}
			usedBy[s][u.Str("id")] = true
		}
	}
	items := make([]any, 0, len(svc.Docs))
	for _, d := range svc.Docs {
		s := core.ServiceView(d.Data(), d.ID())
		items = append(items, s.Shallow().Set("imageCount", counts[d.ID()]).Set("usedBy", float64(len(usedBy[s.Str("serviceSlug")]))))
	}
	aSortBy(items, "label")
	return doc.Obj("items", items), nil
}

func createService(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "services", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"label", "serviceSlug", "icon", "description"}); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	if err := catValidateServiceFields(v, b, true); err != nil {
		return nil, err
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	slug := b.Str("serviceSlug")
	d := doc.Obj("serviceSlug", slug, "label", doc.JSTrim(b.Str("label")), "icon", aOr(b, "icon", nil), "description", aOr(b, "description", "")).Spread(catAudited(c.Account.UID))
	if err := cloud.Col.Services().Doc(slug).Create(d); err != nil {
		if driver.IsAlreadyExists(err) {
			return nil, cloud.E.Conflict(`service "`+slug+`" exists`, doc.Obj("field", "serviceSlug"))
		}
		return nil, err
	}
	return d, nil
}

func getService(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "services", "read", nil); err != nil {
		return nil, err
	}
	return catLoadService(c.Params["serviceSlug"])
}

func patchService(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "services", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"label", "icon", "description"}, "serviceSlug", "createdAt", "createdBy"); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	if err := catValidateServiceFields(v, b, false); err != nil {
		return nil, err
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	ref := cloud.Col.Services().Doc(c.Params["serviceSlug"])
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, cloud.E.NotFound("service not found")
		}
		next := core.ServiceView(s.Data(), s.ID()).Shallow().Spread(catTouched(c.Account.UID))
		if b.Has("label") {
			next.Set("label", doc.JSTrim(b.Str("label")))
		}
		if b.Has("icon") {
			next.Set("icon", b.Val("icon"))
		}
		if b.Has("description") {
			next.Set("description", b.Val("description"))
		}
		tx.Set(ref, next)
		return next, nil
	})
}

func deleteService(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "services", "write", nil); err != nil {
		return nil, err
	}
	ref := cloud.Col.Services().Doc(c.Params["serviceSlug"])
	err := cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		s, err := tx.Get(ref)
		if err != nil {
			return err
		}
		if !s.Exists() {
			return cloud.E.NotFound("service not found")
		}
		imgs, err := tx.Query(ref.Collection("images").Limit(1))
		if err != nil {
			return err
		}
		if !imgs.Empty() {
			return cloud.E.Conflict("delete the images of the service first")
		}
		tx.Delete(ref)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("deleted", true), nil
}

func serviceUsage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "services", "read", nil); err != nil {
		return nil, err
	}
	slug := c.Params["serviceSlug"]
	if _, err := catLoadService(slug); err != nil {
		return nil, err
	}
	entries, _, err := core.ScanUsage(false)
	if err != nil {
		return nil, err
	}
	return doc.Obj("items", core.DedupeUsage(catFilter(entries, func(u *doc.Map) bool { return u.Val("serviceSlug") == slug }))), nil
}

func catFilter(l []*doc.Map, f func(*doc.Map) bool) []*doc.Map {
	out := []*doc.Map{}
	for _, x := range l {
		if f(x) {
			out = append(out, x)
		}
	}
	return out
}

// ───────────────────────────────────────── images

func listImages(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "read", nil); err != nil {
		return nil, err
	}
	slug := c.Params["serviceSlug"]
	if _, err := catLoadService(slug); err != nil {
		return nil, err
	}
	q, err := cloud.Col.Images(slug).OrderBy("createdAt", "desc").Get()
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(q.Docs))
	for _, d := range q.Docs {
		i, err := core.ImageView(d.Data(), d.ID(), slug)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return doc.Obj("items", items), nil
}

func inspectImage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"reference", "registryId", "platform"}); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	pl := aFld(b, "platform")
	if pl != doc.Undefined && pl != "linux/amd64" && pl != "linux/arm64" {
		v.Err("platform", "V_ENUM", "platform must be linux/amd64 | linux/arm64")
	}
	if _, err := catLoadService(c.Params["serviceSlug"]); err != nil {
		return nil, err
	}
	src, err := catResolveSource(v, aFld(b, "reference"), aFld(b, "registryId"))
	if err != nil {
		return nil, err
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	platform := "linux/amd64"
	if s, ok := pl.(string); ok {
		platform = s
	}
	res, err := catInspect(src, platform)
	if err != nil {
		return nil, catRegistryErr(err)
	}
	return res, nil
}

func catInspect(src *catSource, platform string) (*doc.Map, error) {
	creds, err := core.SourceCreds(src.source, src.reg)
	if err != nil {
		return nil, err
	}
	cl := core.NewRegistryClient(creds, 0)
	repo := src.source.Str("repository")
	pm, err := core.WithAccessHint(creds, func() (*core.ManifestResult, error) {
		return cl.PlatformManifest(repo, core.SourceRef(src.source), platform)
	}, core.BrowseOpts{})
	if err != nil {
		return nil, err
	}
	raw, err := cl.BlobBytes(repo, pm.Manifest.Map("config").Str("digest"))
	if err != nil {
		return nil, err
	}
	cfg, err := doc.ParseMap([]byte(core.Utf8(raw)))
	if err != nil {
		return nil, err
	}
	_, arch, _ := strings.Cut(platform, "/")
	if cfg.Truthy("architecture") && cfg.Val("architecture") != arch {
		return nil, &core.RegistryError{Message: "platform " + platform + " not in the image (it is " + core.JSString(aFld(cfg, "os")) + "/" + core.JSString(aFld(cfg, "architecture")) + ")"}
	}
	size := 0.0
	layers := pm.Manifest.Arr("layers")
	for _, l := range layers {
		if n, ok := aOr(l.(*doc.Map), "size", 0.0).(float64); ok {
			size += n
		}
	}
	pre, err := catPrefillFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	return doc.Obj("digest", pm.Digest, "platform", platform, "sizeBytes", size, "layers", float64(len(layers))).Spread(pre), nil
}

func registerImage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"version", "reference", "registryId", "platform", "mode", "kind", "entrypoint", "command", "workdir", "user", "ports", "env", "volumes", "healthcheck", "stopTimeoutSec", "description"}); err != nil {
		return nil, err
	}
	is, err := shared.ValidateRegisterImage(b)
	if err != nil {
		return nil, err
	}
	if !is.OK() {
		return nil, cloud.E.Invalid(core.IssuesOf(is.Errors()))
	}
	serviceSlug := c.Params["serviceSlug"]
	if _, err := catLoadService(serviceSlug); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	src, err := catResolveSource(v, aFld(b, "reference"), aFld(b, "registryId"))
	if err != nil {
		return nil, err
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	version := b.Str("version")
	ref := cloud.Col.Images(serviceSlug).Doc(version)
	ports := []any{}
	for _, p := range b.Arr("ports") {
		ports = append(ports, doc.Obj("name", aProp(p, "name"), "port", aProp(p, "port"), "protocol", aProp(p, "protocol"), "description", aOr(p.(*doc.Map), "description", "")))
	}
	env := []any{}
	for _, x := range b.Arr("env") {
		e := x.(*doc.Map)
		n, err := shared.NormalizeImageEnv(doc.Obj("name", aFld(e, "name"), "value", aFld(e, "value"), "options", aOr(e, "options", nil), "required", doc.Truthy(aFld(e, "required")), "secret", doc.Truthy(aFld(e, "secret")), "description", aOr(e, "description", "")))
		if err != nil {
			return nil, err
		}
		env = append(env, n)
	}
	volumes := []any{}
	for _, x := range b.Arr("volumes") {
		e := x.(*doc.Map)
		n, err := shared.NormalizeImageVolume(doc.Obj("name", aFld(e, "name"), "mountPath", aFld(e, "mountPath"), "kind", aFld(e, "kind"), "description", aOr(e, "description", ""))) // kind explicit (absent → inferred, §21 #83)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, n)
	}
	d := doc.Obj(
		"version", version, "serviceSlug", serviceSlug, "reference", doc.JSTrim(b.Str("reference")), "source", src.source, "registryId", src.registryID, "digest", aFld(src.source, "digest"),
		"platform", aOr(b, "platform", "linux/amd64"),
		"import", doc.Obj("mode", aFld(b, "mode"), "status", "pending", "error", nil, "sizeBytes", nil, "manifestMediaType", nil, "configDigest", nil, "layers", []any{}, "startedAt", nil, "finishedAt", nil),
		"kind", aFld(b, "kind"), "entrypoint", aOr(b, "entrypoint", nil), "command", aOr(b, "command", nil), "workdir", aOr(b, "workdir", nil), "user", aOr(b, "user", nil),
		"ports", ports, "env", env, "volumes", volumes, "healthcheck", aOr(b, "healthcheck", nil), "stopTimeoutSec", aOr(b, "stopTimeoutSec", 30.0), "description", aOr(b, "description", ""),
	).Spread(catAudited(c.Account.UID))
	if err := ref.Create(d); err != nil {
		if driver.IsAlreadyExists(err) {
			return nil, cloud.E.Conflict("version "+version+" exists", doc.Obj("field", "version", "reason", "version-exists"))
		}
		return nil, err
	}
	return d, nil
}

func getImage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "read", nil); err != nil {
		return nil, err
	}
	return catLoadImage(c.Params["serviceSlug"], c.Params["version"])
}

// catTouchedTpl is one template rewritten by rename everywhere.
type catTouchedTpl struct {
	ref       *driver.DocRef
	t, before *doc.Map
	count     float64
	commit    any
}

type catPatched struct {
	next, ch     *doc.Map
	touchedTpl   []*catTouchedTpl
	notRewritten []any
}

func catUsesImage(cfg *doc.Map, slug, version string) bool {
	for _, st := range cfg.Arr("sites") {
		for _, k := range st.(*doc.Map).Map("config").Arr("containers") {
			m := k.(*doc.Map)
			if m.Val("serviceSlug") == slug && m.Val("version") == version {
				return true
			}
		}
	}
	return false
}

func patchImage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "write", nil); err != nil {
		return nil, err
	}
	p, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(p, catPatchImageKeys, "version", "serviceSlug", "source", "digest", "platform", "import", "mode", "layers", "createdAt", "createdBy", "updatedAt", "updatedBy"); err != nil {
		return nil, err
	}
	if re := aFld(p, "renameEverywhere"); re != doc.Undefined {
		if _, ok := re.(bool); !ok {
			return nil, cloud.E.Bad("renameEverywhere", "V_TYPE", "renameEverywhere must be true / false")
		}
	}
	slug, version := c.Params["serviceSlug"], c.Params["version"]
	ref := cloud.Col.Images(slug).Doc(version)
	// the source (reference / registry) can be fixed only while the import failed (then retry-import)
	var src *catSource
	if p.Has("reference") || p.Has("registryId") {
		cur0, err := catLoadImage(slug, version)
		if err != nil {
			return nil, err
		}
		sv := &cloud.V{}
		regID := aFld(p, "registryId")
		if regID == doc.Undefined && !p.Has("reference") {
			regID = aFld(cur0, "registryId")
		}
		if src, err = catResolveSource(sv, aOr(p, "reference", aFld(cur0, "reference")), regID); err != nil {
			return nil, err
		}
		if err := sv.ThrowIf(); err != nil {
			return nil, err
		}
	}
	uid := c.Account.UID
	res, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*catPatched, error) { return catPatchTx(c, tx, ref, p, src, slug, version, uid) })
	if err != nil {
		return nil, err
	}
	next, ch := res.next, res.ch
	data := doc.New()
	for _, k := range []string{"renamed", "removed", "added", "portRetargeted"} {
		if len(ch.Arr(k)) > 0 {
			data.Set(k, ch.Arr(k))
		}
	}
	if len(res.touchedTpl) > 0 || len(res.notRewritten) > 0 {
		templates := []any{}
		for _, x := range res.touchedTpl {
			templates = append(templates, doc.Obj("templateId", aFld(x.t, "templateId"), "references", x.count, "commit", x.commit))
		}
		data.Set("renamedEverywhere", doc.Obj("templates", templates, "deploymentsNotRewritten", res.notRewritten))
	}
	c.Audit = &cloud.AuditHint{Data: data}
	// one root audit entry per rewritten template (deployment drafts are never rewritten, §21 #94)
	for _, x := range res.touchedTpl {
		ver := x.t.Num("version") + 1
		diff := cloud.AuditDiff(x.before, x.t.Shallow().Set("version", ver))
		var email, ip, ua any
		if c.Account.Email != nil {
			email = *c.Account.Email
		}
		if c.IP != "" {
			ip = c.IP
		}
		if s := doc.JSSlice(c.Headers["user-agent"], 300); s != "" {
			ua = s
		}
		_, _ = cloud.WriteAuditEntry(doc.Obj(
			"op", c.Op, "actor", uid, "actorEmail", email, "action", "template.committed", "outcome", "ok", "status", 200.0, "errorCode", nil, "errorMessage", nil,
			"targetType", "template", "target", "templates/"+x.t.Str("templateId"), "requestId", c.RequestID, "ip", ip, "userAgent", ua, "changes", diff.Changes, "changesTotal", float64(diff.Total),
			"data", doc.Obj("reason", "image-rename-everywhere", "image", slug+":"+version, "renames", ch.Arr("renamed"), "references", x.count, "version", ver, "commit", x.commit),
		))
	}
	return next, nil
}

func catPatchTx(c *cloud.Ctx, tx *driver.Tx, ref *driver.DocRef, p *doc.Map, src *catSource, slug, version, uid string) (*catPatched, error) {
	s, err := tx.Get(ref)
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("image not found")
	}
	cur, err := core.ImageView(s.Data(), s.ID(), slug)
	if err != nil {
		return nil, err
	}
	envs, err := catNormEnv(cur.Arr("env"))
	if err != nil {
		return nil, err
	}
	cur.Set("env", envs)
	next := cloud.Clone(cur).(*doc.Map)
	if src != nil {
		reference := doc.JSTrim(core.JSString(aOr(p, "reference", aFld(cur, "reference"))))
		changed := next.Val("reference") != reference || next.Val("registryId") != src.registryID
		status := cur.Map("import").Val("status")
		if changed && status != "failed" {
			field := "registryId"
			if p.Has("reference") {
				field = "reference"
			}
			return nil, cloud.E.Conflict("reference / registryId can change only while the import failed (status "+core.JSString(status)+")", doc.Obj("field", field))
		}
		if changed {
			next.Set("reference", reference).Set("source", src.source).Set("registryId", src.registryID).Set("digest", aFld(src.source, "digest"))
		}
	}
	if err := catApplyImageEdits(next, p); err != nil {
		return nil, err
	}
	// the same rules as registration (shared validateImageSpec) on the merged image
	is := shared.NewIssues("publish")
	if err := shared.ValidateImageSpec(next, is); err != nil {
		return nil, err
	}
	renamesIn := aFld(p, "renames")
	if renamesIn == nil {
		renamesIn = doc.Undefined
	}
	ch, err := shared.ImageNameChanges(cur, next, renamesIn)
	if err != nil {
		return nil, err
	}
	if !is.OK() || len(ch.Arr("issues")) > 0 {
		return nil, cloud.E.Invalid(core.IssuesOf(append(is.Errors(), ch.Arr("issues")...)))
	}
	if envs, err = catNormEnv(next.Arr("env")); err != nil {
		return nil, err
	}
	next.Set("env", envs)
	vols := make([]any, len(next.Arr("volumes")))
	for i, x := range next.Arr("volumes") {
		if vols[i], err = shared.NormalizeImageVolume(x); err != nil {
			return nil, err
		}
	}
	next.Set("volumes", vols)

	// ── impact: renamed / removed names still used by templates + deployment drafts (heads keep their ImageSpec copy).
	// §21 #94: deployments are DETACHED from templates — rename everywhere rewrites TEMPLATES only; the drafts that use
	// a renamed name are listed (details / audit) and need a Template sync or a manual edit.
	changes := []*doc.Map{}
	for _, x := range ch.Arr("renamed") {
		r := x.(*doc.Map)
		changes = append(changes, doc.Obj("kind", aFld(r, "kind"), "name", aFld(r, "from"), "change", "renamed", "to", aFld(r, "to")))
	}
	for _, x := range ch.Arr("removed") {
		r := x.(*doc.Map)
		changes = append(changes, doc.Obj("kind", aFld(r, "kind"), "name", aFld(r, "name"), "change", "removed", "to", nil))
	}
	out := &catPatched{next: next, ch: ch, notRewritten: []any{}}
	if len(changes) > 0 {
		if err := catImpact(c, tx, p, ch, changes, out, slug, version, uid); err != nil {
			return nil, err
		}
	}
	next.Spread(catTouched(uid))
	tx.Set(ref, next)
	return out, nil
}

func catNormEnv(l []any) ([]any, error) {
	out := make([]any, len(l))
	for i, x := range l {
		n, err := shared.NormalizeImageEnv(x)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

func catImpact(c *cloud.Ctx, tx *driver.Tx, p, ch *doc.Map, changes []*doc.Map, out *catPatched, slug, version, uid string) error {
	type tplUse struct {
		ref *driver.DocRef
		t   *doc.Map
	}
	tq, err := tx.Query(cloud.Col.Templates().Query)
	if err != nil {
		return err
	}
	dq, err := tx.Query(cloud.Col.Deployments().Where("deleted", "==", false))
	if err != nil {
		return err
	}
	tpls := []tplUse{}
	for _, d := range tq.Docs {
		if t := cloud.DecodeTemplate(d.Data()); catUsesImage(t, slug, version) {
			tpls = append(tpls, tplUse{d.Ref(), t})
		}
	}
	deps := []*doc.Map{}
	for _, d := range dq.Docs {
		if x := cloud.DecodeDeployment(d.Data()); catUsesImage(x.Map("draft"), slug, version) {
			deps = append(deps, x)
		}
	}
	items := make([]*doc.Map, 0, len(changes))
	for _, x := range changes {
		usedBy := []any{}
		for _, u := range tpls {
			uses, err := shared.ImageNameUses(u.t, slug, version, x.Str("kind"), x.Str("name"))
			if err != nil {
				return err
			}
			for _, e := range uses {
				usedBy = append(usedBy, doc.Obj("kind", "template", "templateId", aFld(u.t, "templateId"), "label", aFld(u.t, "label"), "source", "template").Spread(e.(*doc.Map)))
			}
		}
		for _, d := range deps {
			uses, err := shared.ImageNameUses(d.Map("draft"), slug, version, x.Str("kind"), x.Str("name"))
			if err != nil {
				return err
			}
			for _, e := range uses {
				usedBy = append(usedBy, doc.Obj("kind", "deployment", "deploymentId", aFld(d, "deploymentId"), "label", aFld(d, "label"), "source", "draft").Spread(e.(*doc.Map)))
			}
		}
		items = append(items, x.Shallow().Set("usedBy", usedBy))
	}
	everywhere := doc.Truthy(aFld(p, "renameEverywhere"))
	blocking := []any{}
	removed := 0
	for _, x := range items {
		if len(x.Arr("usedBy")) > 0 && (x.Val("change") == "removed" || !everywhere) {
			blocking = append(blocking, x)
			if x.Val("change") == "removed" {
				removed++
			}
		}
	}
	if len(blocking) > 0 {
		verb, tail := "s are", " — rename everywhere (rewrites templates only; deployment drafts keep the old names until a Template sync or a manual edit), or cancel"
		if len(blocking) == 1 {
			verb = " is"
		}
		if removed > 0 {
			tail = " (removals stay blocked until those containers stop using them)"
		}
		return cloud.E.Conflict(strconv.Itoa(len(blocking))+" renamed / removed name"+verb+" still used by templates or deployment drafts"+tail, doc.Obj("reason", "in-use", "items", blocking))
	}
	renames := []any{}
	for _, x := range ch.Arr("renamed") {
		r := x.(*doc.Map)
		for _, i := range items {
			if i.Val("change") == "renamed" && i.Val("kind") == r.Val("kind") && i.Val("name") == r.Val("from") && len(i.Arr("usedBy")) > 0 {
				renames = append(renames, r)
				break
			}
		}
	}
	if !everywhere || len(renames) == 0 {
		return nil
	}
	issues := []any{}
	for _, x := range tpls {
		r, err := shared.RenameImageNames(x.t, slug, version, renames)
		if err != nil {
			return err
		}
		for _, i := range r.Arr("issues") {
			m := i.(*doc.Map)
			issues = append(issues, m.Shallow().Set("path", "templates["+x.t.Str("templateId")+"]."+m.Str("path")))
		}
		if r.Num("count") != 0 {
			out.touchedTpl = append(out.touchedTpl, &catTouchedTpl{ref: x.ref, t: r.Map("config"), before: x.t, count: r.Num("count")})
		}
	}
	// deployment drafts are never rewritten (§21 #94): count their references for the audit entry
	for _, d := range deps {
		n := 0.0
		for _, i := range items {
			if i.Val("change") != "renamed" {
				continue
			}
			for _, u := range i.Arr("usedBy") {
				m := u.(*doc.Map)
				if m.Val("kind") == "deployment" && m.Val("deploymentId") == d.Val("deploymentId") {
					n++
				}
			}
		}
		if n != 0 {
			out.notRewritten = append(out.notRewritten, doc.Obj("deploymentId", aFld(d, "deploymentId"), "label", aFld(d, "label"), "references", n))
		}
	}
	if len(issues) > 0 {
		return cloud.E.Invalid(core.IssuesOf(issues), issues[0].(*doc.Map).Str("message"))
	}
	// §21 #105: no working copy — the rewrite is ONE commit per affected template (the person as author)
	heads := make([]any, len(out.touchedTpl))
	for i, x := range out.touchedTpl {
		if h := x.t.Map("branch").Map("head"); h != nil {
			head, err := core.LoadCommit(doc.Obj("kind", "template", "templateId", aFld(x.t, "templateId")), h.Str("commitId"), tx, true)
			if err != nil {
				return err
			}
			if head != nil {
				heads[i] = head
			}
		}
	}
	now := doc.NowISO()
	parts := make([]string, len(renames))
	for i, x := range renames {
		r := x.(*doc.Map)
		parts[i] = slug + ":" + version + " " + r.Str("kind") + " " + r.Str("from") + " → " + r.Str("to")
	}
	message := doc.JSSlice("Rename everywhere: "+strings.Join(parts, ", "), 200)
	for i, x := range out.touchedTpl {
		id := aFld(x.t, "templateId")
		config := doc.Obj("variables", aFld(x.t, "variables"), "secrets", aMaskSecrets(x.t.Arr("secrets")), "files", aFld(x.t, "files"), "sites", aFld(x.t, "sites"))
		commit, err := shared.MakeCommit(doc.Obj("commitId", core.NewCommitId(), "branch", doc.Obj("kind", "template", "templateId", id), "parent", heads[i], "kind", "change", "message", message, "author", core.PersonAuthor(c), "createdAt", now, "config", config, "catalog", nil, "source", nil))
		if err != nil {
			return err
		}
		external, err := core.StoreCommitBody(commit)
		if err != nil {
			return err
		}
		core.WriteCommit(tx, commit, external)
		old := x.t.Map("branch")
		branch := doc.Obj("head", shared.CommitRef(commit), "origin", aOr(old, "origin", nil), "commitCount", old.Num("commitCount")+1)
		x.commit = doc.Obj("commitId", commit.Val("commitId"), "seq", commit.Val("seq"))
		tx.Set(x.ref, cloud.EncodeTemplate(x.t.Shallow().Set("version", x.t.Num("version")+1).Set("updatedAt", now).Set("updatedBy", uid).Set("branch", branch)))
	}
	return nil
}

func deleteImage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "write", nil); err != nil {
		return nil, err
	}
	slug, version := c.Params["serviceSlug"], c.Params["version"]
	if _, err := catLoadImage(slug, version); err != nil {
		return nil, err
	}
	entries, _, err := core.ScanUsage(true)
	if err != nil {
		return nil, err
	}
	used := catFilter(entries, func(u *doc.Map) bool { return u.Val("serviceSlug") == slug && u.Val("version") == version })
	if len(used) > 0 {
		return nil, cloud.E.Conflict("the image is in use", doc.Obj("usedBy", core.DedupeUsage(used)))
	}
	if err := cloud.Col.Images(slug).Doc(version).Delete(); err != nil {
		return nil, err
	}
	return doc.Obj("deleted", true), nil
}

func retryImport(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "write", nil); err != nil {
		return nil, err
	}
	slug := c.Params["serviceSlug"]
	ref := cloud.Col.Images(slug).Doc(c.Params["version"])
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, cloud.E.NotFound("image not found")
		}
		cur := s.Data()
		imp := cur.Map("import")
		if imp.Val("status") != "failed" {
			return nil, cloud.E.Conflict("import status is " + core.JSString(aFld(imp, "status")) + " (retry only from failed)")
		}
		tx.Update(ref, doc.Obj("import.status", "pending", "import.error", nil, "import.startedAt", nil, "import.finishedAt", nil).Spread(catTouched(c.Account.UID)))
		imp2 := imp.Shallow().Set("status", "pending").Set("error", nil).Set("startedAt", nil).Set("finishedAt", nil)
		return core.ImageView(cur.Shallow().Set("import", imp2).Spread(catTouched(c.Account.UID)), s.ID(), slug)
	})
}

func imageUsage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "images", "read", nil); err != nil {
		return nil, err
	}
	slug, version := c.Params["serviceSlug"], c.Params["version"]
	if _, err := catLoadImage(slug, version); err != nil {
		return nil, err
	}
	entries, _, err := core.ScanUsage(false)
	if err != nil {
		return nil, err
	}
	return doc.Obj("items", core.DedupeUsage(catFilter(entries, func(u *doc.Map) bool {
		return u.Val("serviceSlug") == slug && u.Val("version") == version && u.Val("source") != "proposal"
	}))), nil
}

// ───────────────────────────────────────── not-found pages

func listNotFoundPages(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "notFoundPages", "read", nil); err != nil {
		return nil, err
	}
	q, err := cloud.Col.NotFoundPages().Get()
	if err != nil {
		return nil, err
	}
	entries, _, err := core.ScanUsage(false)
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(q.Docs))
	for _, d := range q.Docs {
		p := d.Data()
		used := map[string]bool{}
		for _, u := range entries {
			if u.Val("notFoundPageId") == d.ID() {
				used[core.JSString(aFld(u, "kind"))+":"+core.JSString(aFld(u, "id"))] = true
			}
		}
		items = append(items, doc.Obj("notFoundPageId", aFld(p, "notFoundPageId"), "label", aFld(p, "label"), "createdAt", aFld(p, "createdAt"), "createdBy", aFld(p, "createdBy"), "usedBy", float64(len(used)), "sizeBytes", aOr(p, "_sizeBytes", float64(len(p.Str("htmlCode"))))))
	}
	aSortBy(items, "label")
	return doc.Obj("items", items), nil
}

func createNotFoundPage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "notFoundPages", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"label", "htmlCode"}); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	cloud.VLabel(v, aFld(b, "label"), "label", cloud.Limits["label"], true)
	html, ok := b.StrOK("htmlCode")
	if !ok {
		v.Err("htmlCode", "V_REQUIRED", "htmlCode is required")
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	size := len(html)
	if max := cloud.Limits["notFoundPageBytes"]; size > max {
		return nil, cloud.E.TooLarge("htmlCode is " + strconv.Itoa(size) + " bytes (max " + strconv.Itoa(max) + ")")
	}
	ref := cloud.Col.NotFoundPages().Doc()
	d := doc.Obj("notFoundPageId", ref.ID(), "label", doc.JSTrim(b.Str("label")), "htmlCode", html, "createdAt", doc.NowISO(), "createdBy", c.Account.UID)
	if err := ref.Create(d.Shallow().Set("_sizeBytes", float64(size))); err != nil {
		return nil, err
	}
	return d, nil
}

func getNotFoundPage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "notFoundPages", "read", nil); err != nil {
		return nil, err
	}
	s, err := cloud.Col.NotFoundPages().Doc(c.Params["notFoundPageId"]).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("not-found page not found")
	}
	entries, _, err := core.ScanUsage(false)
	if err != nil {
		return nil, err
	}
	usedBy := core.DedupeUsage(catFilter(entries, func(u *doc.Map) bool { return u.Val("notFoundPageId") == s.ID() }))
	return cloud.Pub(s.Data()).Set("usedBy", usedBy), nil
}

func deleteNotFoundPage(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "notFoundPages", "write", nil); err != nil {
		return nil, err
	}
	id := c.Params["notFoundPageId"]
	s, err := cloud.Col.NotFoundPages().Doc(id).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("not-found page not found")
	}
	entries, _, err := core.ScanUsage(false)
	if err != nil {
		return nil, err
	}
	used := catFilter(entries, func(u *doc.Map) bool { return u.Val("notFoundPageId") == id })
	if len(used) > 0 {
		return nil, cloud.E.Conflict("the page is in use", doc.Obj("usedBy", core.DedupeUsage(used)))
	}
	if err := s.Ref().Delete(); err != nil {
		return nil, err
	}
	return doc.Obj("deleted", true), nil
}

// ───────────────────────────────────────── tags

func listTags(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "tags", "read", nil); err != nil {
		return nil, err
	}
	q, err := cloud.Col.Tags().Get()
	if err != nil {
		return nil, err
	}
	deps, err := cloud.Col.Deployments().Where("deleted", "==", false).Select("tagSlugs").Get()
	if err != nil {
		return nil, err
	}
	counts := map[string]float64{}
	for _, d := range deps.Docs {
		for _, t := range d.Data().Strs("tagSlugs") {
			counts[t]++
		}
	}
	items := make([]any, 0, len(q.Docs))
	for _, d := range q.Docs {
		items = append(items, cloud.Pub(d.Data()).Set("deployments", counts[d.ID()]))
	}
	aSortBy(items, "label")
	return doc.Obj("items", items), nil
}

func catVColor(v *cloud.V, val any) {
	if s, ok := val.(string); !ok || !shared.RE.Color.MatchString(s) {
		v.Err("color", "V_FORMAT", "color must match ^#[0-9a-f]{6}$")
	}
}

func createTag(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "tags", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"tagSlug", "label", "icon", "color"}); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	cloud.VSlug(v, aFld(b, "tagSlug"), "tagSlug")
	cloud.VLabel(v, aFld(b, "label"), "label", cloud.Limits["label"], true)
	core.VIconFormat(v, aFld(b, "icon"), "icon", false)
	catVColor(v, aFld(b, "color"))
	if v.OK() {
		if err := core.VIconRefs(v, []core.IconField{{Path: "icon", Value: aFld(b, "icon")}}); err != nil {
			return nil, err
		}
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	slug := b.Str("tagSlug")
	d := doc.Obj("tagSlug", slug, "label", doc.JSTrim(b.Str("label")), "icon", aOr(b, "icon", nil), "color", b.Val("color")).Spread(catAudited(c.Account.UID))
	if err := cloud.Col.Tags().Doc(slug).Create(d); err != nil {
		if driver.IsAlreadyExists(err) {
			return nil, cloud.E.Conflict(`tag "`+slug+`" exists`, doc.Obj("field", "tagSlug"))
		}
		return nil, err
	}
	return d, nil
}

func patchTag(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "tags", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"label", "icon", "color"}, "tagSlug"); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	cloud.VLabel(v, aFld(b, "label"), "label", cloud.Limits["label"], false)
	core.VIconFormat(v, aFld(b, "icon"), "icon", false)
	if b.Has("color") {
		catVColor(v, aFld(b, "color"))
	}
	if v.OK() {
		if err := core.VIconRefs(v, []core.IconField{{Path: "icon", Value: aFld(b, "icon")}}); err != nil {
			return nil, err
		}
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	ref := cloud.Col.Tags().Doc(c.Params["tagSlug"])
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, cloud.E.NotFound("tag not found")
		}
		next := s.Data().Shallow().Spread(catTouched(c.Account.UID))
		if b.Has("label") {
			next.Set("label", doc.JSTrim(b.Str("label")))
		}
		if b.Has("icon") {
			next.Set("icon", b.Val("icon"))
		}
		if b.Has("color") {
			next.Set("color", b.Val("color"))
		}
		tx.Set(ref, next)
		return next, nil
	})
}

func deleteTag(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "tags", "write", nil); err != nil {
		return nil, err
	}
	slug := c.Params["tagSlug"]
	ref := cloud.Col.Tags().Doc(slug)
	s, err := ref.Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("tag not found")
	}
	deps, err := cloud.Col.Deployments().Where("tagSlugs", "array-contains", slug).Get()
	if err != nil {
		return nil, err
	}
	accounts, err := cloud.Col.Accounts().Get()
	if err != nil {
		return nil, err
	}
	type write struct {
		ref  *driver.DocRef
		data *doc.Map
	}
	writes := []write{}
	accessRules := 0.0
	for _, d := range deps.Docs {
		writes = append(writes, write{d.Ref(), doc.Obj("tagSlugs", doc.ArrayRemove(slug))})
	}
	for _, a := range accounts.Docs {
		changed := false
		access := []any{}
		for _, x := range a.Data().Arr("access") {
			r := x.(*doc.Map)
			scope := r.Map("scope")
			has := false
			rest := []any{}
			for _, t := range scope.Arr("tagSlugs") {
				if t == slug {
					has = true
				} else {
					rest = append(rest, t)
				}
			}
			if scope.Val("kind") == "tags" && has {
				changed = true
				accessRules++
				if len(rest) > 0 {
					access = append(access, r.Shallow().Set("scope", doc.Obj("kind", "tags", "tagSlugs", rest)))
				}
			} else {
				access = append(access, r)
			}
		}
		if changed {
			writes = append(writes, write{a.Ref(), doc.Obj("access", access)})
		}
	}
	for i := 0; i < len(writes); i += 500 {
		batch := cloud.DB().Batch()
		for _, w := range writes[i:min(i+500, len(writes))] {
			batch.Update(w.ref, w.data)
		}
		if err := batch.Commit(); err != nil {
			return nil, err
		}
	}
	if err := ref.Delete(); err != nil {
		return nil, err
	}
	return doc.Obj("deleted", true, "removedFrom", doc.Obj("deployments", float64(deps.Size()), "accessRules", accessRules)), nil
}
