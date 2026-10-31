// Package shared is the Go port of the pure logic of shared/src the cloud routes use. It works on doc values, so unknown
// fields and key order survive exactly as in the TS spreads. A TS throw is a Go error with the same message.
package shared

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud/doc"
)

// ─────────────────────────────────────────── JS semantics on doc values (the normalizers run on unvalidated input)

// jThrown is a TS TypeError on its way to the exported function (jCatch makes it an error).
type jThrown struct{ msg string }

func jThrow(format string, a ...any) { panic(jThrown{fmt.Sprintf(format, a...)}) }

func jCatch(err *error) {
	if r := recover(); r != nil {
		t, ok := r.(jThrown)
		if !ok {
			panic(r)
		}
		*err = errors.New(t.msg)
	}
}

// jNull is `v === null || v === undefined`.
func jNull(v any) bool {
	if m, ok := v.(*doc.Map); ok {
		return m == nil
	}
	return v == nil || v == doc.Undefined
}

// jGet is v.k; null and undefined throw like V8.
func jGet(v any, k string) any {
	switch x := v.(type) {
	case nil:
		jThrow("Cannot read properties of null (reading '%s')", k)
	case *doc.Map:
		if x == nil {
			jThrow("Cannot read properties of null (reading '%s')", k)
		}
		if y, ok := x.Get(k); ok {
			return y
		}
	case []any:
		if k == "length" {
			return float64(len(x))
		}
	case string:
		if k == "length" {
			return float64(doc.JSLen(x))
		}
	default:
		if v == doc.Undefined {
			jThrow("Cannot read properties of undefined (reading '%s')", k)
		}
	}
	return doc.Undefined
}

// jOpt is v?.k.
func jOpt(v any, k string) any {
	if jNull(v) {
		return doc.Undefined
	}
	return jGet(v, k)
}

// jCoal is v ?? alt.
func jCoal(v, alt any) any {
	if jNull(v) {
		return alt
	}
	return v
}

// jIsObj is `typeof v === 'object' && v !== null`.
func jIsObj(v any) bool {
	switch x := v.(type) {
	case *doc.Map:
		return x != nil
	case []any:
		return true
	}
	return false
}

// jPlainObj is isObj of validate.ts (an object, not an array).
func jPlainObj(v any) bool {
	m, ok := v.(*doc.Map)
	return ok && m != nil
}

func jNotFn(v any, method, src string) {
	if jNull(v) {
		jGet(v, method)
	}
	jThrow("%s.%s is not a function", src, method)
}

// jArr is the receiver of v.map / v.filter / …: not an array → the TypeError of `src.method(…)`.
func jArr(v any, method, src string) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	jNotFn(v, method, src)
	return nil
}

// jIter is the receiver of `for (… of v)`.
func jIter(v any, src string) []any {
	switch x := v.(type) {
	case []any:
		return x
	case string:
		out := make([]any, 0, len(x))
		for _, r := range x {
			out = append(out, string(r))
		}
		return out
	}
	if !strings.ContainsAny(src, " ?(") { // V8 names a plain variable, else the value
		jThrow("%s is not iterable", src)
	}
	switch x := v.(type) {
	case float64:
		jThrow("number %s is not iterable (cannot read property Symbol(Symbol.iterator))", jStr(x))
	case bool:
		jThrow("boolean %v is not iterable (cannot read property Symbol(Symbol.iterator))", x)
	}
	jThrow("object is not iterable (cannot read property Symbol(Symbol.iterator))")
	return nil
}

// jSpread is { ...out, ...v }.
func jSpread(out *doc.Map, v any) *doc.Map {
	switch x := v.(type) {
	case *doc.Map:
		out.Spread(x)
	case []any:
		for i, y := range x {
			out.Set(strconv.Itoa(i), y)
		}
	case string:
		i := 0
		for _, r := range x {
			out.Set(strconv.Itoa(i), string(r))
			i++
			if r >= 0x10000 {
				i++
			}
		}
	}
	return out
}

// jStr is String(v) / `${v}`.
func jStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case *doc.Map:
		if x == nil {
			return "null"
		}
		return "[object Object]"
	case []any:
		return jJoin(x, ",")
	}
	if v == doc.Undefined {
		return "undefined"
	}
	if f, ok := doc.AsNumber(v); ok {
		if s, err := canonical.FormatNumber(f); err == nil {
			return s
		}
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}

// jJoin is a.join(sep).
func jJoin(a []any, sep string) string {
	s := make([]string, len(a))
	for i, v := range a {
		if !jNull(v) {
			s[i] = jStr(v)
		}
	}
	return strings.Join(s, sep)
}

// jKey is the identity of a value for ===, Set and Map (objects by reference).
func jKey(v any) string {
	switch x := v.(type) {
	case nil:
		return "n"
	case string:
		return "s" + x
	case bool:
		return "b" + strconv.FormatBool(x)
	case *doc.Map:
		return fmt.Sprintf("p%p", x)
	case []any:
		if len(x) == 0 {
			return "e" // no address to tell two empty arrays apart: jEq and the dupes treat them as distinct
		}
		return fmt.Sprintf("a%p", &x[0])
	}
	if v == doc.Undefined {
		return "u"
	}
	if f, ok := doc.AsNumber(v); ok {
		return "f" + strconv.FormatFloat(f+0, 'g', -1, 64)
	}
	return fmt.Sprintf("o%v", v)
}

// jEq is ===.
func jEq(a, b any) bool {
	if x, ok := a.([]any); ok && len(x) == 0 {
		return false
	}
	return jKey(a) == jKey(b)
}

// jIs is `v === s` for a string.
func jIs(v any, s string) bool {
	x, ok := v.(string)
	return ok && x == s
}

// jIsOneOf is `list.includes(v)` for strings.
func jIsOneOf(v any, list ...string) bool {
	s, ok := v.(string)
	return ok && slices.Contains(list, s)
}

// jDupes is dupes() of validate.ts: the set of keys used more than once.
func jDupes(items []any, key func(any) any) map[string]bool {
	seen := map[string]bool{}
	d := map[string]bool{}
	for _, it := range items {
		k := jKey(key(it))
		if seen[k] && k != "e" {
			d[k] = true
		}
		seen[k] = true
	}
	return d
}

// jProtoKey reports a key every JS object inherits from Object.prototype (`table[key]` is truthy, not undefined).
func jProtoKey(k string) bool {
	switch k {
	case "constructor", "__proto__", "__defineGetter__", "__defineSetter__", "__lookupGetter__", "__lookupSetter__", "hasOwnProperty", "isPrototypeOf", "propertyIsEnumerable", "toLocaleString", "toString", "valueOf":
		return true
	}
	return false
}

// jCmp16 is the order of JS < on strings (UTF-16 code units).
func jCmp16(a, b string) int {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	return slices.Compare(x, y)
}

// jLine is the "." of a JS regex without the s flag.
const jLine = `[^\n\r\x{2028}\x{2029}]`

// jWS is the content of \s in a JS regex, for use inside a character class.
const jWS = `\t\n\x0b\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

func jHasSpace(s string) bool { return strings.IndexFunc(s, doc.IsJSSpace) >= 0 }

// jLower is toLowerCase (Go's one differs on U+0130).
func jLower(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "İ", "i̇"))
}

// ─────────────────────────────────────────── defaults

// EmptyConfig is emptyConfig.
func EmptyConfig() *doc.Map {
	return doc.Obj("variables", []any{}, "secrets", []any{}, "files", []any{}, "sites", []any{})
}

// EmptyResponseHeaders is emptyResponseHeaders.
func EmptyResponseHeaders() *doc.Map {
	return doc.Obj("set", doc.New(), "add", doc.New(), "remove", []any{}, "always", []any{})
}

// EDGE_DEFAULTS is the part of EDGE_DEFAULTS normalizeEdge uses.
var EDGE_DEFAULTS = struct{ ConnectTimeoutSec, SendTimeoutSec, ReadTimeoutSec float64 }{60, 60, 60}

// NewEdgeHost is newEdgeHost.
func NewEdgeHost(hostname string) *doc.Map {
	return doc.Obj("hostname", hostname, "tls", doc.Obj("mode", "none"), "redirectHttpToHttps", false, "responseHeaders", EmptyResponseHeaders())
}

// DefaultEdge is defaultEdge.
func DefaultEdge() *doc.Map {
	return doc.Obj("httpPort", 80.0, "httpsPort", 443.0, "maxBodyBytes", nil, "hosts", []any{NewEdgeHost("*")}, "routes", []any{})
}

// NewProxyAction is newProxyAction.
func NewProxyAction(containerSlug, portName string) *doc.Map {
	return doc.Obj("type", "proxy", "containerSlug", containerSlug, "portName", portName, "stripPrefix", false, "rewrite", nil, "addPrefix", nil,
		"setHeaders", doc.New(), "removeHeaders", []any{}, "websocket", true,
		"connectTimeoutSec", EDGE_DEFAULTS.ConnectTimeoutSec, "sendTimeoutSec", EDGE_DEFAULTS.SendTimeoutSec, "readTimeoutSec", EDGE_DEFAULTS.ReadTimeoutSec,
		"requestBuffering", false, "responseBuffering", false, "maxBodyBytes", nil)
}

// NewEdgeRoute is newEdgeRoute.
func NewEdgeRoute(routeID, path string, action any, order float64) *doc.Map {
	return doc.Obj("routeId", routeID, "hosts", []any{}, "match", doc.Obj("kind", "prefix", "path", path, "stopRegex", false, "methods", nil), "action", action, "order", order, "responseHeaders", nil)
}

// DefaultSiteConfig is defaultSiteConfig (host nil = auto).
func DefaultSiteConfig(host any) *doc.Map {
	return doc.Obj("host", host, "agentPort", 7080.0, "agentGuiAliasPort", 8080.0, "volumes", []any{}, "containers", []any{}, "depends", []any{}, "notFoundPageId", nil, "edge", DefaultEdge())
}

// DefaultVolumeSlug is defaultVolumeSlug.
func DefaultVolumeSlug(containerSlug, imageVolume string) string {
	return doc.JSSlice(containerSlug+"-"+imageVolume, 100)
}

// NewSiteVolume is newSiteVolume with its default arguments (volumeSlug is not checked: legacy names may be anything).
func NewSiteVolume(volumeSlug any) *doc.Map {
	return doc.Obj("volumeSlug", volumeSlug, "label", volumeSlug, "description", "", "backup", true, "location", doc.Obj("kind", "auto"), "kind", "dir", "seed", nil)
}

// ─────────────────────────────────────────── normalizers

// FileRenderMode is fileRenderMode ("none" | "expressions", or a stored invalid value).
func FileRenderMode(f *doc.Map) any { return jFileRenderMode(f) }

func jFileRenderMode(f any) any {
	if jIs(jGet(f, "editor"), "binary") {
		return "none"
	}
	return jCoal(jGet(f, "render"), "expressions")
}

// NormalizeLocation is normalizeLocation.
func NormalizeLocation(l any) *doc.Map {
	if !doc.Truthy(l) || !jIsObj(l) || !jIs(jGet(l, "kind"), "path") {
		return doc.Obj("kind", "auto")
	}
	return doc.Obj("kind", "path", "path", jGet(l, "path"))
}

// NormalizeFileDef is normalizeFileDef.
func NormalizeFileDef(f any) (out *doc.Map, err error) {
	defer jCatch(&err)
	return jNormalizeFileDef(f), nil
}

func jNormalizeFileDef(f any) *doc.Map {
	out := jSpread(doc.New(), f)
	out.Set("render", jFileRenderMode(f))
	return out.Set("location", NormalizeLocation(jGet(f, "location")))
}

// NormalizeImageEnv is normalizeImageEnv.
func NormalizeImageEnv(e any) (out *doc.Map, err error) {
	defer jCatch(&err)
	return jNormalizeImageEnv(e), nil
}

func jNormalizeImageEnv(raw any) *doc.Map {
	out := doc.Obj("name", jGet(raw, "name"), "required", jGet(raw, "required"), "secret", jGet(raw, "secret"), "description", jCoal(jGet(raw, "description"), ""), "options", jCoal(jGet(raw, "options"), nil))
	value := jGet(raw, "value")
	if a, ok := value.([]any); ok {
		list := []any{}
		for _, x := range a {
			if _, isStr := x.(string); isStr {
				list = append(list, x)
			}
		}
		out.Set("options", jCoal(jGet(raw, "options"), list))
		if len(list) > 0 {
			out.Set("value", list[0])
		}
	} else if !jNull(value) {
		out.Set("value", value)
	}
	return out
}

// NormalizeImageVolume is normalizeImageVolume.
func NormalizeImageVolume(v any) (out *doc.Map, err error) {
	defer jCatch(&err)
	name, mountPath, kind := jGet(v, "name"), jGet(v, "mountPath"), jGet(v, "kind")
	if jNull(kind) {
		kind = InferImageVolumeKind(mountPath)
	}
	return doc.Obj("name", name, "mountPath", mountPath, "kind", kind, "description", jCoal(jGet(v, "description"), "")), nil
}

// NormalizeVolumeTree is normalizeVolumeTree.
func NormalizeVolumeTree(t any) (out *doc.Map, err error) {
	defer jCatch(&err)
	return jNormalizeVolumeTree(t), nil
}

func jAncestorDirs(p string) []string {
	var out []string
	for i, c := range []byte(p) {
		if c == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}

func jNormalizeVolumeTree(t any) *doc.Map {
	seen := map[string]bool{}
	entries := []any{}
	for _, e := range jIter(jCoal(jOpt(t, "entries"), []any{}), "(t?.entries ?? [])") {
		if !doc.Truthy(e) {
			continue
		}
		p, ok := jGet(e, "path").(string)
		if !ok || seen[p] {
			continue
		}
		seen[p] = true
		entries = append(entries, doc.Obj("path", p, "sha256", jGet(e, "sha256"), "size", jGet(e, "size"), "mode", jCoal(jGet(e, "mode"), DEFAULT_FILE_MODE), "render", jCoal(jGet(e, "render"), "none")))
	}
	slices.SortStableFunc(entries, func(a, b any) int { return jCmp16(a.(*doc.Map).Str("path"), b.(*doc.Map).Str("path")) })
	implied := map[string]bool{}
	for _, e := range entries {
		for _, d := range jAncestorDirs(e.(*doc.Map).Str("path")) {
			implied[d] = true
		}
	}
	var rawDirs []string
	rawSeen := map[string]bool{}
	for _, d := range jArr(jCoal(jOpt(t, "dirs"), []any{}), "filter", "((intermediate value) ?? [])") {
		if s, ok := d.(string); ok && s != "" && !rawSeen[s] {
			rawSeen[s] = true
			rawDirs = append(rawDirs, s)
		}
	}
	for _, d := range rawDirs {
		for _, a := range jAncestorDirs(d) {
			implied[a] = true
		}
	}
	var dirs []string
	for _, d := range rawDirs {
		if !implied[d] && !seen[d] {
			dirs = append(dirs, d)
		}
	}
	slices.SortStableFunc(dirs, jCmp16)
	outDirs := make([]any, len(dirs))
	for i, d := range dirs {
		outDirs[i] = d
	}
	return doc.Obj("entries", entries, "dirs", outDirs)
}

// NormalizeSiteVolume is normalizeSiteVolume.
func NormalizeSiteVolume(v any) (out *doc.Map, err error) {
	defer jCatch(&err)
	return jNormalizeSiteVolume(v), nil
}

func jNormalizeSiteVolume(v any) *doc.Map {
	vseed := jGet(v, "seed")
	var seed any
	if jIs(jOpt(vseed, "kind"), "tree") {
		seed = doc.Obj("kind", "tree").Spread(jNormalizeVolumeTree(vseed))
	} else {
		seed = jCoal(vseed, nil)
	}
	vp := jGet(v, "persist")
	slug := jGet(v, "volumeSlug")
	out := doc.Obj("volumeSlug", slug, "label", jCoal(jGet(v, "label"), slug), "description", jCoal(jGet(v, "description"), ""), "backup", jCoal(jGet(v, "backup"), true), "location", NormalizeLocation(jGet(v, "location")), "kind", jCoal(jGet(v, "kind"), "dir"), "seed", seed)
	if doc.Truthy(vp) {
		ex, ok := jGet(vp, "exclude").([]any)
		if !ok {
			ex = []any{}
		}
		out.Set("persist", doc.Obj("exclude", ex, "maxBytes", jCoal(jGet(vp, "maxBytes"), float64(VOLUME_CONTENT_LIMITS.PersistDefaultBytes)), "auto", jCoal(jGet(vp, "auto"), true)))
	}
	return out
}

func jNormalizeVolumeSource(src any) (any, bool) {
	if !doc.Truthy(src) || !jIsObj(src) {
		return doc.Obj("kind", "none"), false
	}
	if jIs(jGet(src, "kind"), "data") {
		return doc.Obj("kind", "volume", "volumeSlug", jGet(src, "name")), true
	}
	return src, false
}

func jFind(list []any, pred func(any) bool) any {
	for _, x := range list {
		if pred(x) {
			return x
		}
	}
	return doc.Undefined
}

// NormalizeContainer is normalizeContainer; img nil = unknown image, implicitVolumes = NormalizeContainerOptions.implicitVolumes.
func NormalizeContainer(raw any, img any, implicitVolumes bool) (out *doc.Map, err error) {
	defer jCatch(&err)
	return jNormalizeContainer(raw, img, implicitVolumes), nil
}

func jNormalizeContainer(raw, img any, implicitVolumes bool) *doc.Map {
	containerSlug := jCoal(jGet(raw, "containerSlug"), jGet(raw, "serviceSlug"))
	var ports []any
	if a, ok := jGet(raw, "ports").([]any); ok {
		ports = make([]any, len(a))
		for i, p := range a {
			ports[i] = doc.Obj("portName", jGet(p, "portName"), "hostPort", jCoal(jGet(p, "hostPort"), nil), "hostIp", jCoal(jGet(p, "hostIp"), "0.0.0.0"), "edge", jCoal(jGet(p, "edge"), true))
		}
	} else {
		a := jArr(jCoal(jGet(raw, "port"), []any{}), "map", "(raw.port ?? [])")
		ports = make([]any, len(a))
		for i, p := range a {
			ports[i] = doc.Obj("portName", jGet(p, "portName"), "hostPort", jCoal(jGet(p, "hostPort"), nil), "hostIp", "0.0.0.0", "edge", true)
		}
	}
	volumes := []any{}
	implicit := implicitVolumes
	slugOf := func(name any) string { return DefaultVolumeSlug(jStr(containerSlug), jStr(name)) }
	if a, ok := jGet(raw, "volumes").([]any); ok {
		for _, v := range a {
			source, legacy := jNormalizeVolumeSource(jGet(v, "source"))
			var ro any = false
			switch {
			case jIsOneOf(jGet(source, "kind"), "file", "secret"):
				ro = true
			case !legacy:
				ro = jCoal(jGet(v, "readOnly"), false)
			}
			volumes = append(volumes, doc.Obj("imageVolume", jCoal(jGet(v, "imageVolume"), nil), "mountPath", jCoal(jGet(v, "mountPath"), nil), "source", source, "readOnly", ro))
		}
	} else {
		implicit = true
		for _, v := range jIter(jCoal(jGet(raw, "volume"), []any{}), "(raw.volume ?? [])") {
			switch k := jGet(v, "kind"); {
			case jIs(k, "data"):
				var iv any = doc.Undefined
				if !jNull(img) {
					iv = jFind(jArr(jGet(img, "volumes"), "find", "img?.volumes"), func(x any) bool { return jEq(jGet(x, "name"), jGet(v, "name")) })
				}
				var mp any
				if !(doc.Truthy(iv) && jEq(jGet(iv, "mountPath"), jGet(v, "mountPath"))) {
					mp = jGet(v, "mountPath")
				}
				volumes = append(volumes, doc.Obj("imageVolume", jGet(v, "name"), "mountPath", mp, "source", doc.Obj("kind", "volume", "volumeSlug", slugOf(jGet(v, "name"))), "readOnly", false))
			case jIs(k, "file"):
				volumes = append(volumes, doc.Obj("imageVolume", nil, "mountPath", jGet(v, "mountPath"), "source", doc.Obj("kind", "file", "fileSlug", jGet(v, "fileSlug")), "readOnly", true))
			case jIs(k, "secret"):
				volumes = append(volumes, doc.Obj("imageVolume", nil, "mountPath", jGet(v, "mountPath"), "source", doc.Obj("kind", "secret", "secretSlug", jGet(v, "secretSlug")), "readOnly", true))
			}
		}
	}
	if implicit {
		for _, iv := range jIter(jCoal(jOpt(img, "volumes"), []any{}), "(img?.volumes ?? [])") {
			name := jGet(iv, "name")
			if !slices.ContainsFunc(volumes, func(x any) bool { return jEq(x.(*doc.Map).Val("imageVolume"), name) }) {
				volumes = append(volumes, doc.Obj("imageVolume", name, "mountPath", nil, "source", doc.Obj("kind", "volume", "volumeSlug", slugOf(name)), "readOnly", false))
			}
		}
	}
	deps := []any{}
	for _, d := range jArr(jCoal(jGet(raw, "depends"), []any{}), "map", "(raw.depends ?? [])") {
		deps = append(deps, doc.Obj("containerSlug", jCoal(jGet(d, "containerSlug"), jGet(d, "serviceSlug"))))
	}
	env := []any{}
	for _, e := range jArr(jCoal(jGet(raw, "env"), []any{}), "map", "(raw.env ?? [])") {
		env = append(env, doc.Obj("name", jGet(e, "name"), "value", jCoal(jGet(e, "value"), nil), "secretRef", jCoal(jGet(e, "secretRef"), nil)))
	}
	res := jGet(raw, "resources")
	return doc.Obj("containerSlug", containerSlug, "serviceSlug", jGet(raw, "serviceSlug"), "version", jGet(raw, "version"), "enabled", jCoal(jGet(raw, "enabled"), true),
		"env", env, "ports", ports, "volumes", volumes, "command", jCoal(jGet(raw, "command"), nil), "entrypoint", jCoal(jGet(raw, "entrypoint"), nil), "restart", jCoal(jGet(raw, "restart"), "always"),
		"resources", doc.Obj("cpus", jCoal(jOpt(res, "cpus"), nil), "memoryMb", jCoal(jOpt(res, "memoryMb"), nil)), "healthcheck", jCoal(jGet(raw, "healthcheck"), nil), "depends", deps)
}

func jResponseHeaders(h any) *doc.Map {
	x := jCoal(h, doc.New())
	return doc.Obj("set", jCoal(jGet(x, "set"), doc.New()), "add", jCoal(jGet(x, "add"), doc.New()), "remove", jCoal(jGet(x, "remove"), []any{}), "always", jCoal(jGet(x, "always"), []any{}))
}

// NormalizeEdge is normalizeEdge.
func NormalizeEdge(e any) (out *doc.Map, err error) {
	defer jCatch(&err)
	return jNormalizeEdge(e), nil
}

func jNormalizeEdge(e any) *doc.Map {
	d := DefaultEdge()
	httpPort := jCoal(jGet(e, "httpPort"), 80.0)
	httpsPort := jGet(e, "httpsPort")
	if httpsPort == doc.Undefined {
		httpsPort = 443.0
	}
	maxBody := jCoal(jGet(e, "maxBodyBytes"), nil)
	hosts := []any{}
	for _, h := range jArr(jCoal(jGet(e, "hosts"), d.Val("hosts")), "map", "(e.hosts ?? d.hosts)") {
		hosts = append(hosts, jSpread(doc.New(), h).Set("responseHeaders", jResponseHeaders(jGet(h, "responseHeaders"))))
	}
	routes := []any{}
	for _, r := range jArr(jCoal(jGet(e, "routes"), []any{}), "map", "(e.routes ?? [])") {
		routes = append(routes, jNormalizeRoute(r))
	}
	return doc.Obj("httpPort", httpPort, "httpsPort", httpsPort, "maxBodyBytes", maxBody, "hosts", hosts, "routes", routes)
}

func jNormalizeRoute(r any) *doc.Map {
	a0 := jGet(r, "action")
	action := a0
	if jIs(jOpt(a0, "type"), "proxy") {
		var legacyRead any = doc.Undefined
		if _, ok := jGet(a0, "timeoutSec").(float64); ok {
			legacyRead = jGet(a0, "timeoutSec")
		}
		rest := jSpread(doc.New(), a0).Delete("serviceSlug", "timeoutSec")
		action = NewProxyAction("", "").Spread(rest).
			Set("type", "proxy").
			Set("containerSlug", jCoal(jGet(a0, "containerSlug"), jCoal(jGet(a0, "serviceSlug"), ""))).
			Set("portName", jCoal(jGet(a0, "portName"), "")).
			Set("rewrite", jCoal(jGet(a0, "rewrite"), nil)).
			Set("addPrefix", jCoal(jGet(a0, "addPrefix"), nil)).
			Set("setHeaders", jCoal(jGet(a0, "setHeaders"), doc.New())).
			Set("removeHeaders", jCoal(jGet(a0, "removeHeaders"), []any{})).
			Set("websocket", jCoal(jGet(a0, "websocket"), true)).
			Set("readTimeoutSec", jCoal(jGet(a0, "readTimeoutSec"), jCoal(legacyRead, EDGE_DEFAULTS.ReadTimeoutSec))).
			Set("maxBodyBytes", jCoal(jGet(a0, "maxBodyBytes"), nil))
	}
	m := jCoal(jGet(r, "match"), doc.New())
	kind := jCoal(jGet(m, "kind"), "prefix")
	mrest := jSpread(doc.New(), m).Delete("pathPrefix")
	var stop any = false
	if jIs(kind, "prefix") {
		stop = jCoal(jGet(m, "stopRegex"), false)
	}
	methods := jGet(m, "methods")
	if !doc.Truthy(jOpt(methods, "length")) {
		methods = nil
	}
	match := mrest.Set("kind", kind).Set("path", jCoal(jGet(m, "path"), jCoal(jGet(m, "pathPrefix"), "/"))).Set("stopRegex", stop).Set("methods", methods)
	var rh any
	if doc.Truthy(jGet(r, "responseHeaders")) {
		rh = jResponseHeaders(jGet(r, "responseHeaders"))
	}
	return jSpread(doc.New(), r).Set("action", action).Set("hosts", jCoal(jGet(r, "hosts"), []any{})).Set("match", match).Set("responseHeaders", rh)
}

var jDevToolKinds = []string{"http", "postgres", "sqlite", "cassandra", "redis", "mqtt"}

var jDevToolLabel = map[string]string{"http": "HTTP client", "postgres": "PostgreSQL", "sqlite": "SQLite", "cassandra": "Cassandra (CQL)", "redis": "Redis", "mqtt": "MQTT"}

var jTargetKeys = map[string][]string{
	"container":      {"kind", "containerSlug", "portName"},
	"host":           {"kind", "host", "port"},
	"url":            {"kind", "url"},
	"volume-file":    {"kind", "volumeSlug", "path"},
	"container-file": {"kind", "containerSlug", "path"},
}

func jShapeCredential(c any) any {
	if !jPlainObj(c) {
		return jCoal(c, doc.Obj("kind", "none"))
	}
	switch k := jGet(c, "kind"); {
	case jIs(k, "none"):
		return doc.Obj("kind", "none")
	case jIs(k, "expr"):
		return doc.Obj("kind", "expr", "expr", jGet(c, "expr"))
	case jIs(k, "literal"):
		return doc.Obj("kind", "literal", "value", jGet(c, "value"))
	}
	return c
}

// jNormalizeDevToolPreset is normalizeDevToolPreset of devtools.ts + newDevToolPreset.
func jNormalizeDevToolPreset(p any, kind string) *doc.Map {
	var database, username any
	if kind == "redis" {
		database = "0"
	}
	if kind == "postgres" {
		username = "postgres"
	}
	target := doc.Obj("kind", "container", "containerSlug", "", "portName", "")
	if kind == "sqlite" {
		target = doc.Obj("kind", "volume-file", "volumeSlug", "", "path", "")
	}
	auth := doc.Obj("username", username, "password", doc.Obj("kind", "none"), "database", database, "clientId", nil, "httpScheme", "none", "token", doc.Obj("kind", "none"), "apiKeyHeader", nil)
	options := doc.Obj("tls", false, "tlsInsecure", false, "tlsServerName", nil, "sslMode", nil, "timeoutSec", nil, "maxRows", nil, "consistency", nil, "scheme", nil, "basePath", nil, "followRedirects", nil, "defaultHeaders", []any{}, "cleanSession", nil, "keepAliveSec", nil)
	base := doc.Obj("presetSlug", kind, "name", jDevToolLabel[kind], "description", "", "kind", kind, "target", target, "auth", auth, "options", options, "readOnly", false)
	out := base.Shallow().Spread(p.(*doc.Map))
	out.Set("target", jCoal(jGet(p, "target"), target))
	out.Set("auth", jSpread(auth, jCoal(jGet(p, "auth"), doc.New())))
	popts := jGet(p, "options")
	out.Set("options", jSpread(options, jCoal(popts, doc.New())).Set("defaultHeaders", jCoal(jOpt(popts, "defaultHeaders"), []any{})))
	return out
}

// NormalizeStoredDevToolPreset is normalizeStoredDevToolPreset.
func NormalizeStoredDevToolPreset(p any) (out any, err error) {
	defer jCatch(&err)
	return jNormalizeStoredDevToolPreset(p), nil
}

func jNormalizeStoredDevToolPreset(p any) any {
	if !jPlainObj(p) || !jIsOneOf(jGet(p, "kind"), jDevToolKinds...) {
		return p
	}
	n := jNormalizeDevToolPreset(p, jGet(p, "kind").(string))
	t := n.Val("target")
	target := t
	if jPlainObj(t) {
		if k, ok := jGet(t, "kind").(string); ok {
			if jProtoKey(k) {
				jThrow("keys is not iterable")
			}
			if keys := jTargetKeys[k]; keys != nil {
				o := doc.New()
				for _, x := range keys {
					o.Set(x, jGet(t, x))
				}
				target = o
			}
		}
	}
	var a, o any = doc.New(), doc.New()
	if jPlainObj(n.Val("auth")) {
		a = n.Val("auth")
	}
	if jPlainObj(n.Val("options")) {
		o = n.Val("options")
	}
	nul := func(from any, k string) any {
		v := jGet(from, k)
		if v == doc.Undefined {
			return nil
		}
		return v
	}
	headers := jCoal(jGet(o, "defaultHeaders"), []any{})
	if arr, ok := headers.([]any); ok {
		mapped := make([]any, len(arr))
		for i, h := range arr {
			mapped[i] = h
			if jPlainObj(h) {
				mapped[i] = doc.Obj("name", jGet(h, "name"), "value", jGet(h, "value"))
			}
		}
		headers = mapped
	}
	return doc.Obj("presetSlug", n.Val("presetSlug"), "name", n.Val("name"), "description", jCoal(n.Val("description"), ""), "kind", n.Val("kind"), "target", target,
		"auth", doc.Obj("username", nul(a, "username"), "password", jShapeCredential(jGet(a, "password")), "database", nul(a, "database"), "clientId", nul(a, "clientId"),
			"httpScheme", jCoal(jGet(a, "httpScheme"), "none"), "token", jShapeCredential(jGet(a, "token")), "apiKeyHeader", nul(a, "apiKeyHeader")),
		"options", doc.Obj("tls", jCoal(jGet(o, "tls"), false), "tlsInsecure", jCoal(jGet(o, "tlsInsecure"), false), "tlsServerName", nul(o, "tlsServerName"), "sslMode", nul(o, "sslMode"),
			"timeoutSec", nul(o, "timeoutSec"), "maxRows", nul(o, "maxRows"), "consistency", nul(o, "consistency"), "scheme", nul(o, "scheme"), "basePath", nul(o, "basePath"), "followRedirects", nul(o, "followRedirects"),
			"defaultHeaders", headers, "cleanSession", nul(o, "cleanSession"), "keepAliveSec", nul(o, "keepAliveSec")),
		"readOnly", jCoal(n.Val("readOnly"), false))
}

// NormalizeSiteDevTools is normalizeSiteDevTools: the object to spread ({} or { devTools }).
func NormalizeSiteDevTools(v any) (out *doc.Map, err error) {
	defer jCatch(&err)
	return jNormalizeSiteDevTools(v), nil
}

func jNormalizeSiteDevTools(v any) *doc.Map {
	if jNull(v) {
		return doc.New()
	}
	a, ok := v.([]any)
	if !ok {
		return doc.Obj("devTools", v)
	}
	if len(a) == 0 {
		return doc.New()
	}
	out := make([]any, len(a))
	for i, p := range a {
		out[i] = jNormalizeStoredDevToolPreset(p)
	}
	return doc.Obj("devTools", out)
}

// NormalizeSiteConfig is normalizeSiteConfig; images: "<service>:<version>" → { volumes }, nil = undefined.
func NormalizeSiteConfig(sc any, images *doc.Map) (out *doc.Map, err error) {
	defer jCatch(&err)
	return jNormalizeSiteConfig(sc, images), nil
}

func jNormalizeSiteConfig(sc any, images *doc.Map) *doc.Map {
	host := jGet(sc, "host")
	if s, ok := host.(string); jNull(host) || (ok && doc.JSTrim(s) == "") {
		host = nil
	}
	vols, isArr := jGet(sc, "volumes").([]any)
	preD := !isArr
	volumes := make([]any, 0, len(vols))
	for _, v := range vols {
		volumes = append(volumes, jNormalizeSiteVolume(v))
	}
	legacy := []any{}
	legacySeen := map[string]bool{}
	addLegacy := func(v any) {
		if k := jKey(v); !legacySeen[k] {
			legacySeen[k] = true
			legacy = append(legacy, v)
		}
	}
	containers := []any{}
	for _, c := range jArr(jCoal(jGet(sc, "containers"), []any{}), "map", "(sc.containers ?? [])") {
		rawVols, rawArr := jGet(c, "volumes").([]any)
		for _, v := range rawVols {
			k := jOpt(v, "source")
			if jIs(jOpt(k, "kind"), "data") {
				if s, ok := jGet(k, "name").(string); ok {
					addLegacy(s)
				}
			}
		}
		var img any
		if images != nil {
			img = jCoal(images.Val(ImageKey(jStr(jGet(c, "serviceSlug")), jStr(jGet(c, "version")))), nil)
		}
		n := jNormalizeContainer(c, img, preD)
		if preD || !rawArr {
			for _, v := range n.Arr("volumes") {
				src := v.(*doc.Map).Val("source")
				if jIs(jGet(src, "kind"), "volume") {
					addLegacy(jGet(src, "volumeSlug"))
				}
			}
		}
		containers = append(containers, n)
	}
	for _, name := range legacy {
		if !slices.ContainsFunc(volumes, func(v any) bool { return jEq(v.(*doc.Map).Val("volumeSlug"), name) }) {
			volumes = append(volumes, NewSiteVolume(name))
		}
	}
	agentPort := jCoal(jGet(sc, "agentPort"), 7080.0)
	alias := jGet(sc, "agentGuiAliasPort")
	if alias == doc.Undefined {
		alias = 8080.0
	}
	depends := jCoal(jGet(sc, "depends"), []any{})
	nf := jCoal(jGet(sc, "notFoundPageId"), nil)
	var edge any = DefaultEdge()
	if doc.Truthy(jGet(sc, "edge")) {
		edge = jNormalizeEdge(jGet(sc, "edge"))
	}
	out := doc.Obj("host", host, "agentPort", agentPort, "agentGuiAliasPort", alias, "volumes", volumes, "containers", containers, "depends", depends, "notFoundPageId", nf, "edge", edge)
	return out.Spread(jNormalizeSiteDevTools(jGet(sc, "devTools")))
}

// NormalizeConfig is normalizeConfig.
func NormalizeConfig(config any, images *doc.Map) (out *doc.Map, err error) {
	defer jCatch(&err)
	files := []any{}
	for _, f := range jArr(jGet(config, "files"), "map", "config.files") {
		files = append(files, jNormalizeFileDef(f))
	}
	sites := []any{}
	for _, s := range jArr(jGet(config, "sites"), "map", "config.sites") {
		sites = append(sites, jSpread(doc.New(), s).Set("config", jNormalizeSiteConfig(jGet(s, "config"), images)))
	}
	return jSpread(doc.New(), config).Set("files", files).Set("sites", sites), nil
}

// IsLegacyContainer is isLegacyContainer.
func IsLegacyContainer(raw any) bool {
	if !jIsObj(raw) {
		return false
	}
	c, _ := raw.(*doc.Map)
	_, ports := c.Val("ports").([]any)
	_, vols := c.Val("volumes").([]any)
	return !c.Has("containerSlug") || c.Val("containerSlug") == doc.Undefined || c.Has("port") || c.Has("volume") || !ports || !vols
}

// IsLegacySiteConfig is isLegacySiteConfig.
func IsLegacySiteConfig(sc any) (legacy bool, err error) {
	defer jCatch(&err)
	if !jIsObj(sc) {
		return false, nil
	}
	if _, ok := jGet(sc, "volumes").([]any); !ok {
		return true, nil
	}
	for _, c := range jArr(jCoal(jGet(sc, "containers"), []any{}), "some", "(x.containers ?? [])") {
		if IsLegacyContainer(c) {
			return true, nil
		}
		for _, v := range jArrOf(jGet(c, "volumes")) {
			if jIs(jOpt(jOpt(v, "source"), "kind"), "data") {
				return true, nil
			}
		}
	}
	return false, nil
}

func jArrOf(v any) []any {
	a, _ := v.([]any)
	return a
}
