package shared

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/expr"
	"opm/agent/internal/validate"
)

// RE is RE: validate.RE with the patterns whose Go meaning differs from JS replaced (\s, \S and /i of the JS regexes).
var RE = validate.RE

func init() {
	RE.Email = regexp.MustCompile(`^[^@` + jWS + `]+@[^@` + jWS + `]+\.[^@` + jWS + `]+$`)
	RE.HTTPSURL = regexp.MustCompile(`^https://[^` + jWS + `/?#]+$`)
	RE.HTTPSURLAny = regexp.MustCompile(`^https://[^` + jWS + `]+$`)
	RE.Picture = regexp.MustCompile(`^(lucide:[a-z0-9-]+|upload:[A-Za-z0-9]{20}|https://[^` + jWS + `]+)$`)
	// (?i) in Go also folds U+017F and U+212A into s and k
	RE.DNSName = regexp.MustCompile(`^(\*\.)?[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)
}

// LIMITS is LIMITS.
var LIMITS = validate.Limits

// RESERVED_SITE_SLUGS is RESERVED_SITE_SLUGS.
var RESERVED_SITE_SLUGS = validate.ReservedSiteSlugs

// RESERVED_CONTAINER_SLUGS is RESERVED_CONTAINER_SLUGS.
var RESERVED_CONTAINER_SLUGS = []string{"new"}

// EDGE_METHODS is EDGE_METHODS.
var EDGE_METHODS = validate.EdgeMethods

// Issues is the issue collector of validate.ts.
type Issues struct {
	Mode  string // template | draft | publish | agent
	items []*doc.Map
}

// NewIssues is new Issues(mode).
func NewIssues(mode string) *Issues { return &Issues{Mode: mode} }

func (is *Issues) add(path, code, message, severity string) {
	is.items = append(is.items, doc.Obj("path", path, "code", code, "message", message, "severity", severity))
}

// Err is err(path, code, message, level = 'save'); level "publish" is a warning in template / draft mode.
func (is *Issues) Err(path, code, message string, level ...string) {
	if code == "E_DEFERRED" {
		return
	}
	strict := is.Mode == "publish" || is.Mode == "agent"
	if (len(level) == 0 || level[0] == "save") || strict {
		is.add(path, code, message, "error")
	} else {
		is.add(path, code, message, "warning")
	}
}

// Warn is warn.
func (is *Issues) Warn(path, code, message string) {
	if code != "E_DEFERRED" {
		is.add(path, code, message, "warning")
	}
}

// Info is info.
func (is *Issues) Info(path, code, message string) { is.add(path, code, message, "info") }

// List is the list getter: []*doc.Map as []any, { path, code, message, severity }.
func (is *Issues) List() []any {
	out := make([]any, len(is.items))
	for i, x := range is.items {
		out[i] = x
	}
	return out
}

// Errors is the errors getter.
func (is *Issues) Errors() []any {
	out := []any{}
	for _, x := range is.items {
		if x.Str("severity") == "error" {
			out = append(out, x)
		}
	}
	return out
}

// OK is the ok getter.
func (is *Issues) OK() bool { return len(is.Errors()) == 0 }

func jIsInt(v any) bool {
	f, ok := v.(float64)
	return ok && f == math.Trunc(f) && f-f == 0
}

// CheckLabel is checkLabel (max 0 = LIMITS.Label).
func CheckLabel(v any, path string, is *Issues, max int) {
	if max == 0 {
		max = LIMITS.Label
	}
	s, ok := v.(string)
	if !ok || doc.JSTrim(s) == "" {
		is.Err(path, "V_REQUIRED", "required")
		return
	}
	if doc.JSLen(s) > max {
		is.Err(path, "V_LENGTH", "at most "+strconv.Itoa(max)+" characters")
	}
}

// CheckDescription is checkDescription (v = doc.Undefined when absent).
func CheckDescription(v any, path string, is *Issues) {
	if v == doc.Undefined {
		return
	}
	s, ok := v.(string)
	if !ok {
		is.Err(path, "V_TYPE", "must be a string")
		return
	}
	if doc.JSLen(s) > LIMITS.Description {
		is.Err(path, "V_LENGTH", "at most "+strconv.Itoa(LIMITS.Description)+" characters")
	}
}

// CheckSlug is checkSlug.
func CheckSlug(v any, path string, is *Issues) {
	if s, ok := v.(string); !ok || !RE.Slug.MatchString(s) {
		is.Err(path, "V_FORMAT", `"`+jStr(v)+`" is not a slug (^[a-z][a-z0-9-]{1,39}$)`)
	}
}

// CheckIcon is checkIcon.
func CheckIcon(v any, path string, is *Issues) {
	if jNull(v) {
		return
	}
	if s, ok := v.(string); !ok || !RE.Icon.MatchString(s) {
		is.Err(path, "V_FORMAT", "icon must be null, lucide:<name> or upload:<iconId>")
	}
}

// CheckPicture is checkPicture.
func CheckPicture(v any, path string, is *Issues) {
	if jNull(v) {
		return
	}
	if s, ok := v.(string); !ok || !RE.Picture.MatchString(s) {
		is.Err(path, "V_FORMAT", "must be null, lucide:<name>, upload:<iconId> or an https URL")
	}
}

// IconUploadId is iconUploadId (nil = null).
func IconUploadId(v any) *string {
	if s, ok := v.(string); ok && strings.HasPrefix(s, "upload:") {
		id := s[7:]
		return &id
	}
	return nil
}

// CheckPort is checkPort (min 1 is the TS default).
func CheckPort(v any, path string, is *Issues, min int) {
	f, _ := v.(float64)
	if !jIsInt(v) || f < float64(min) || f > 65535 {
		is.Err(path, "V_RANGE", jStr(v)+" must be an integer "+strconv.Itoa(min)+"–65535")
	}
}

// IsSafeMountPath is isSafeMountPath.
func IsSafeMountPath(v string) bool {
	return strings.HasPrefix(v, "/") && v != "/" && !jHasSpace(v) && !strings.Contains(v, "..") && doc.JSLen(v) <= 1024
}

func jCheckExprField(v string, f expr.Field, path string, is *Issues) {
	for _, e := range expr.CheckExpr(v, f) {
		is.Err(path, e.Code, e.Message+" (at "+strconv.Itoa(e.Pos)+")")
	}
}

func jCheckMountPath(v any, path string, is *Issues) {
	s, ok := v.(string)
	if ok && expr.IsExpr(s) {
		if !strings.HasPrefix(s, "/") {
			is.Err(path, "V_PATH", `mount path must be absolute: start with "/" (e.g. /app/config/{{ container.env.NODE_ENV }}.ts)`)
		}
		if doc.JSLen(s) > 1024 {
			is.Err(path, "V_LENGTH", "at most 1024 characters")
		}
		jCheckExprField(s, expr.FieldMountPath, path, is)
		return
	}
	if !ok || !IsSafeMountPath(s) {
		is.Err(path, "V_MOUNT", `mount path must be absolute, not "/", without spaces or ".."`)
	}
}

var jReRe2Bad = regexp.MustCompile(`\(\?[=!]|\(\?<[=!]|\\[1-9]`)

// CheckRe2 is checkRe2.
func CheckRe2(pattern any, path string, is *Issues) {
	s, ok := pattern.(string)
	if !ok {
		is.Err(path, "V_TYPE", "pattern must be a string")
		return
	}
	if jReRe2Bad.MatchString(s) {
		is.Err(path, "V_PATTERN", "look-around and back-references are not RE2")
		return
	}
	if msg := jsRegexError(s); msg != "" {
		is.Err(path, "V_PATTERN", "invalid regular expression: "+msg)
	}
}

func jItemAt(base string, item any, i int) string {
	return base + "[" + jStr(jCoal(jOpt(item, "name"), float64(i))) + "]"
}

func jName(v any) any { return jGet(v, "name") }

// ValidateImagePorts is validateImagePorts.
func ValidateImagePorts(ports any, is *Issues, base string) (err error) {
	defer jCatch(&err)
	jValidateImagePorts(ports, is, base)
	return nil
}

func jValidateImagePorts(ports any, is *Issues, base string) {
	list, ok := ports.([]any)
	if !ok {
		is.Err(base, "V_TYPE", "must be a list")
		return
	}
	if len(list) > LIMITS.PortsPerImage {
		is.Err(base, "V_TOO_MANY", "at most "+strconv.Itoa(LIMITS.PortsPerImage))
	}
	d := jDupes(list, jName)
	for i, p := range list {
		at := jItemAt(base, p, i)
		CheckSlug(jGet(p, "name"), at+".name", is)
		if d[jKey(jGet(p, "name"))] {
			is.Err(at+".name", "V_DUPLICATE", `port name "`+jStr(jGet(p, "name"))+`" is used twice`)
		}
		switch v := jGet(p, "port").(type) {
		case float64:
			CheckPort(v, at+".port", is, 1)
		case string:
			if expr.IsExpr(v) {
				jCheckExprField(v, expr.FieldImagePort, at+".port", is)
			} else {
				is.Err(at+".port", "V_TYPE", "port must be a number or one whole {{ }} block")
			}
		default:
			is.Err(at+".port", "V_TYPE", "port must be a number or one whole {{ }} block")
		}
		if !jIsOneOf(jGet(p, "protocol"), "tcp", "udp") {
			is.Err(at+".protocol", "V_ENUM", "protocol must be tcp | udp")
		}
		CheckDescription(jGet(p, "description"), at+".description", is)
	}
}

// ValidateImageEnv is validateImageEnv.
func ValidateImageEnv(env any, is *Issues, base string) (err error) {
	defer jCatch(&err)
	jValidateImageEnv(env, is, base)
	return nil
}

func jValidateImageEnv(env any, is *Issues, base string) {
	raw, ok := env.([]any)
	if !ok {
		is.Err(base, "V_TYPE", "must be a list")
		return
	}
	list := make([]any, len(raw))
	for i, e := range raw {
		list[i] = e
		if doc.Truthy(e) && jIsObj(e) {
			list[i] = jNormalizeImageEnv(e)
		}
	}
	d := jDupes(list, jName)
	for i, e := range list {
		at := jItemAt(base, e, i)
		name := jGet(e, "name")
		if s, ok := name.(string); !ok || !RE.EnvName.MatchString(s) {
			is.Err(at+".name", "V_FORMAT", `"`+jStr(name)+`" does not match ^[A-Z_][A-Z0-9_]*$`)
		}
		if d[jKey(name)] {
			is.Err(at+".name", "V_DUPLICATE", `env "`+jStr(name)+`" is defined twice`)
		}
		if _, ok := jGet(e, "required").(bool); !ok {
			is.Err(at+".required", "V_TYPE", "required must be true / false")
		}
		if _, ok := jGet(e, "secret").(bool); !ok {
			is.Err(at+".secret", "V_TYPE", "secret must be true / false")
		}
		secret := doc.Truthy(jGet(e, "secret"))
		opts := jGet(e, "options")
		if !jNull(opts) {
			arr, isArr := opts.([]any)
			var strs []string
			allStr := isArr
			for _, x := range arr {
				s, ok := x.(string)
				allStr = allStr && ok
				strs = append(strs, s)
			}
			if !allStr {
				is.Err(at+".options", "V_TYPE", "options must be a list of strings or null")
			} else {
				if len(strs) < 1 || len(strs) > LIMITS.ImageEnvList {
					is.Err(at+".options", "V_LENGTH", "a choice needs 1–"+strconv.Itoa(LIMITS.ImageEnvList)+" options")
				}
				sorted := slices.Sorted(slices.Values(strs))
				if len(slices.Compact(sorted)) != len(strs) {
					is.Err(at+".options", "V_DUPLICATE", "options must be unique")
				}
				if slices.ContainsFunc(strs, func(x string) bool { return x == "" || doc.JSLen(x) > LIMITS.Value }) {
					is.Err(at+".options", "V_LENGTH", "options must be 1–"+strconv.Itoa(LIMITS.Value)+" characters")
				}
				if slices.ContainsFunc(strs, expr.IsExpr) {
					is.Err(at+".options", "V_FORMAT", "options are plain values (no {{ }})")
				}
				if secret {
					is.Err(at+".options", "V_IMAGE_ENV", "a secret env cannot have options")
				}
			}
		}
		if val := jGet(e, "value"); val != doc.Undefined {
			if secret {
				is.Err(at+".value", "V_IMAGE_ENV", "a secret env cannot have a default value")
			}
			if s, ok := val.(string); ok {
				if doc.JSLen(s) > LIMITS.Value {
					is.Err(at+".value", "V_LENGTH", "at most "+strconv.Itoa(LIMITS.Value)+" characters")
				}
				if arr, isArr := opts.([]any); isArr {
					if !jIncludes(arr, s) {
						is.Err(at+".value", "V_OPTION", `the default "`+s+`" must be one of the options (`+jJoin(arr, ", ")+`)`)
					}
				} else {
					jCheckExprField(s, expr.FieldImageEnv, at+".value", is)
				}
			} else {
				is.Err(at+".value", "V_TYPE", "value must be a string")
			}
		}
		CheckDescription(jGet(e, "description"), at+".description", is)
	}
}

// ValidateImageVolumes is validateImageVolumes.
func ValidateImageVolumes(vols any, is *Issues, base string) (err error) {
	defer jCatch(&err)
	jValidateImageVolumes(vols, is, base)
	return nil
}

func jValidateImageVolumes(vols any, is *Issues, base string) {
	list, ok := vols.([]any)
	if !ok {
		is.Err(base, "V_TYPE", "must be a list")
		return
	}
	d := jDupes(list, jName)
	dm := jDupes(list, func(v any) any { return jGet(v, "mountPath") })
	for i, v := range list {
		at := jItemAt(base, v, i)
		name, mp := jGet(v, "name"), jGet(v, "mountPath")
		CheckSlug(name, at+".name", is)
		if d[jKey(name)] {
			is.Err(at+".name", "V_DUPLICATE", `volume "`+jStr(name)+`" is defined twice`)
		}
		jCheckMountPath(mp, at+".mountPath", is)
		if dm[jKey(mp)] {
			is.Err(at+".mountPath", "V_MOUNT", "mount path "+jStr(mp)+" is used twice")
		}
		if k := jGet(v, "kind"); k != doc.Undefined && !jIsOneOf(k, "dir", "file") {
			is.Err(at+".kind", "V_ENUM", "kind must be dir (Folder) | file (File)")
		} else if s, ok := mp.(string); jImageVolumeKind(v) == "file" && ok && strings.HasSuffix(s, "/") {
			is.Err(at+".mountPath", "V_VOLUME_KIND", "image volume "+jStr(name)+` is a File: its mount path must not end with "/" (or set it to Folder)`)
		}
		CheckDescription(jGet(v, "description"), at+".description", is)
	}
}

var jHealthAllowed = map[string][]string{
	"http": {"portName", "path", "expectStatus"},
	"tcp":  {"portName"},
	"exec": {"command", "expectExitCode"},
	"log":  {"pattern"},
}

// ValidateHealthCheck is validateHealthCheck; portNames are the names as found (any value).
func ValidateHealthCheck(hc any, portNames []any, is *Issues, base string) (err error) {
	defer jCatch(&err)
	jValidateHealthCheck(hc, portNames, is, base)
	return nil
}

func jValidateHealthCheck(h any, portNames []any, is *Issues, base string) {
	if jNull(h) {
		return
	}
	typ := jGet(h, "type")
	key := jStr(typ)
	allowed, known := jHealthAllowed[key]
	proto := jProtoKey(key)
	if !known && !proto {
		is.Err(base+".type", "V_ENUM", "type must be http | tcp | exec | log")
		return
	}
	common := []string{"type", "intervalSec", "timeoutSec", "retries", "startPeriodSec"}
	if m, ok := h.(*doc.Map); ok {
		for _, k := range m.Keys() {
			if !slices.Contains(common, k) {
				if proto {
					jThrow("allowed[h.type].includes is not a function")
				}
				if !slices.Contains(allowed, k) {
					is.Err(base+"."+k, "V_HEALTHCHECK", "field "+k+" is not allowed for type "+jStr(typ))
				}
			}
		}
	} else if a, ok := h.([]any); ok && len(a) > 0 && (known || proto) {
		for i := range a {
			k := strconv.Itoa(i)
			if proto {
				jThrow("allowed[h.type].includes is not a function")
			}
			is.Err(base+"."+k, "V_HEALTHCHECK", "field "+k+" is not allowed for type "+jStr(typ))
		}
	}
	rng := func(k string, min, max int) {
		f, _ := jGet(h, k).(float64)
		if !jIsInt(jGet(h, k)) || f < float64(min) || f > float64(max) {
			is.Err(base+"."+k, "V_RANGE", k+" must be an integer "+strconv.Itoa(min)+"–"+strconv.Itoa(max))
		}
	}
	rng("intervalSec", 1, 3600)
	rng("timeoutSec", 1, 600)
	rng("retries", 1, 100)
	rng("startPeriodSec", 0, 3600)
	if jIsOneOf(typ, "http", "tcp") {
		if pn := jGet(h, "portName"); !jIncludes(portNames, pn) {
			names := jJoin(portNames, ", ")
			if names == "" {
				names = "none"
			}
			is.Err(base+".portName", "V_REF", `"`+jStr(pn)+`" is not one of the image ports (`+names+`)`)
		}
	}
	if jIs(typ, "http") {
		if s, ok := jGet(h, "path").(string); !ok || !strings.HasPrefix(s, "/") {
			is.Err(base+".path", "V_FORMAT", "path must start with /")
		}
		rng("expectStatus", 100, 599)
	}
	if jIs(typ, "exec") {
		c, ok := jGet(h, "command").([]any)
		if !ok || len(c) < 1 || len(c) > 50 || slices.ContainsFunc(c, func(x any) bool { _, s := x.(string); return !s }) {
			is.Err(base+".command", "V_LENGTH", "command must be 1–50 strings")
		}
		rng("expectExitCode", 0, 255)
	}
	if jIs(typ, "log") {
		CheckRe2(jGet(h, "pattern"), base+".pattern", is)
	}
}

// ValidateCommand is validateCommand.
func ValidateCommand(v any, path string, is *Issues) {
	if jNull(v) {
		return
	}
	a, ok := v.([]any)
	if !ok || len(a) > LIMITS.CommandItems || slices.ContainsFunc(a, func(x any) bool {
		s, isStr := x.(string)
		return !isStr || doc.JSLen(s) > LIMITS.Value
	}) {
		is.Err(path, "V_TYPE", "must be null or a list of ≤ "+strconv.Itoa(LIMITS.CommandItems)+" strings")
	}
}

var jReUser = regexp.MustCompile(`^[A-Za-z0-9_.-]+(:[A-Za-z0-9_.-]+)?$`)

// ValidateImageSpec is validateImageSpec.
func ValidateImageSpec(b *doc.Map, is *Issues) (err error) {
	defer jCatch(&err)
	jValidateImageSpec(b, is)
	return nil
}

func jValidateImageSpec(b any, is *Issues) {
	if k := jGet(b, "kind"); k != doc.Undefined && !jIsOneOf(k, "service", "job") {
		is.Err("kind", "V_ENUM", "kind must be service | job")
	}
	ValidateCommand(jGet(b, "entrypoint"), "entrypoint", is)
	ValidateCommand(jGet(b, "command"), "command", is)
	if w := jGet(b, "workdir"); !jNull(w) {
		if s, ok := w.(string); !ok || !strings.HasPrefix(s, "/") {
			is.Err("workdir", "V_FORMAT", "workdir must be an absolute path")
		}
	}
	if u := jGet(b, "user"); !jNull(u) {
		if s, ok := u.(string); !ok || !jReUser.MatchString(s) {
			is.Err("user", "V_FORMAT", `user must be "name" or "uid:gid"`)
		}
	}
	jValidateImagePorts(jCoal(jGet(b, "ports"), []any{}), is, "ports")
	jValidateImageEnv(jCoal(jGet(b, "env"), []any{}), is, "env")
	jValidateImageVolumes(jCoal(jGet(b, "volumes"), []any{}), is, "volumes")
	names := []any{}
	for _, p := range jArrOf(jGet(b, "ports")) {
		names = append(names, jOpt(p, "name"))
	}
	jValidateHealthCheck(jCoal(jGet(b, "healthcheck"), nil), names, is, "healthcheck")
	if s := jGet(b, "stopTimeoutSec"); s != doc.Undefined {
		f, _ := s.(float64)
		if !jIsInt(s) || f < 1 || f > 600 {
			is.Err("stopTimeoutSec", "V_RANGE", "stopTimeoutSec must be 1–600")
		}
	}
	CheckDescription(jGet(b, "description"), "description", is)
}

// ValidateRegisterImage is validateRegisterImage.
func ValidateRegisterImage(b *doc.Map) (is *Issues, err error) {
	defer jCatch(&err)
	is = NewIssues("publish")
	ver := jGet(b, "version")
	if s, ok := ver.(string); !ok || !RE.Version.MatchString(s) {
		is.Err("version", "V_FORMAT", `version "`+jStr(jCoal(ver, ""))+`" must look like v1, v2 or 3.1.0 (lower case, ^[a-z0-9][a-z0-9._-]{0,63}$)`)
	}
	if pr := ParseImageReference(jGet(b, "reference")); !pr.Bool("ok") {
		is.Err("reference", "V_REFERENCE", pr.Str("error"))
	}
	if r := jGet(b, "registryId"); !jNull(r) {
		if s, ok := r.(string); !ok || s == "" {
			is.Err("registryId", "V_TYPE", "registryId must be an id or null (public)")
		}
	}
	if p := jGet(b, "platform"); p != doc.Undefined && !jIsOneOf(p, "linux/amd64", "linux/arm64") {
		is.Err("platform", "V_ENUM", "platform must be linux/amd64 | linux/arm64")
	}
	if !jIsOneOf(jGet(b, "mode"), "copy", "gateway") {
		is.Err("mode", "V_ENUM", "mode must be copy | gateway")
	}
	if !jIsOneOf(jGet(b, "kind"), "service", "job") {
		is.Err("kind", "V_ENUM", "kind must be service | job")
	}
	jValidateImageSpec(b.Shallow().Set("kind", doc.Undefined), is)
	return is, nil
}

func jUnits(r []rune) []uint16 {
	out := make([]uint16, len(r))
	for i, x := range r {
		out[i] = uint16(x)
	}
	return out
}

// jsRegexError is the message of new RegExp(pattern) (no flags, Annex B syntax), "" when the pattern compiles.
func jsRegexError(p string) string {
	msg := jsRegexScan(p)
	if msg == "" {
		return ""
	}
	return "Invalid regular expression: /" + p + "/: " + msg
}

// jsRegexScan walks a pattern the way V8's parser does in non-unicode mode and returns its error text.
func jsRegexScan(p string) string {
	type group struct{ quantifiable bool }
	src := make([]rune, 0, len(p)) // a non-unicode regex is read in UTF-16 code units
	for _, u := range utf16.Encode([]rune(p)) {
		src = append(src, rune(u))
	}
	n := len(src)
	var stack []group
	lastAtom := false // the previous term can take a quantifier
	names := map[string]bool{}
	hasNamed := strings.Contains(p, "(?<") && regexp.MustCompile(`\(\?<[^=!]`).MatchString(p)
	var refs []string
	readName := func(i int) (string, int, bool) { // src[i] follows "<"
		j := i
		for j < n && src[j] != '>' {
			r, w := src[j], 1
			if utf16.IsSurrogate(r) && j+1 < n {
				r, w = utf16.DecodeRune(r, src[j+1]), 2
			}
			ok := r == '_' || r == '$' || unicode.IsLetter(r) || (j > i && (unicode.IsDigit(r) || unicode.IsMark(r) || r == 0x200c || r == 0x200d))
			if !ok {
				return "", j, false
			}
			j += w
		}
		if j >= n || j == i {
			return "", j, false
		}
		return string(utf16.Decode(jUnits(src[i:j]))), j + 1, true
	}
	quant := func(i int) (int, int, int, bool) { // {n}, {n,}, {n,m} at src[i] == '{'
		j := i + 1
		lo, hi, has := 0, -1, false
		for j < n && src[j] >= '0' && src[j] <= '9' {
			if lo < 1<<30 {
				lo = lo*10 + int(src[j]-'0')
			}
			j++
			has = true
		}
		if !has {
			return 0, 0, 0, false
		}
		if j < n && src[j] == ',' {
			j++
			if j < n && src[j] >= '0' && src[j] <= '9' {
				hi = 0
				for j < n && src[j] >= '0' && src[j] <= '9' {
					if hi < 1<<30 {
						hi = hi*10 + int(src[j]-'0')
					}
					j++
				}
			}
		} else {
			hi = lo
		}
		if j < n && src[j] == '}' {
			return lo, hi, j + 1, true
		}
		return 0, 0, 0, false
	}
	for i := 0; i < n; {
		c := src[i]
		switch c {
		case '|':
			lastAtom = false
			i++
		case '^', '$':
			lastAtom = false
			i++
		case '(':
			i++
			if i < n && src[i] == '?' {
				if i+1 >= n {
					return "Invalid group"
				}
				switch src[i+1] {
				case ':':
					i += 2
				case '=', '!':
					i += 2
				case '<':
					if i+2 < n && (src[i+2] == '=' || src[i+2] == '!') {
						i += 3
						break
					}
					name, j, ok := readName(i + 2)
					if !ok {
						return "Invalid capture group name"
					}
					if names[name] {
						return "Duplicate capture group name"
					}
					names[name] = true
					i = j
				default:
					return "Invalid group"
				}
			}
			stack = append(stack, group{true})
			lastAtom = false
		case ')':
			if len(stack) == 0 {
				return "Unmatched ')'"
			}
			stack = stack[:len(stack)-1]
			lastAtom = true
			i++
		case '*', '+', '?':
			if !lastAtom {
				return "Nothing to repeat"
			}
			i++
			if i < n && src[i] == '?' {
				i++
			}
			lastAtom = false
		case '{':
			if lo, hi, j, ok := quant(i); ok {
				if !lastAtom {
					return "Nothing to repeat"
				}
				if hi >= 0 && hi < lo {
					return "numbers out of order in {} quantifier"
				}
				i = j
				if i < n && src[i] == '?' {
					i++
				}
				lastAtom = false
			} else {
				lastAtom = true
				i++
			}
		case '[':
			i++
			if i < n && src[i] == '^' {
				i++
			}
			prev := rune(-1) // the single character before a "-"
			inRange := false
			closed := false
			for i < n {
				r := src[i]
				if r == ']' {
					closed = true
					i++
					break
				}
				cur := r
				single := true
				if r == '\\' {
					i++
					if i >= n {
						return "\\ at end of pattern"
					}
					cur, single, i = jsClassEscape(src, i)
				} else {
					i++
				}
				if inRange {
					if single && prev >= 0 && cur < prev {
						return "Range out of order in character class"
					}
					inRange = false
					prev = -1
					continue
				}
				if i < n && src[i] == '-' && i+1 < n && src[i+1] != ']' {
					prev = cur
					if !single {
						prev = -1
					}
					inRange = true
					i++
					continue
				}
				prev = -1
			}
			if !closed {
				return "Unterminated character class"
			}
			lastAtom = true
		case '\\':
			i++
			if i >= n {
				return "\\ at end of pattern"
			}
			switch e := src[i]; e {
			case 'b', 'B':
				lastAtom = false
				i++
			case 'k':
				i++
				if hasNamed {
					if i >= n || src[i] != '<' {
						return "Invalid named reference"
					}
					name, j, ok := readName(i + 1)
					if !ok {
						return "Invalid named reference"
					}
					refs = append(refs, name)
					i = j
				}
				lastAtom = true
			default:
				i++
				lastAtom = true
			}
		default:
			i++
			lastAtom = true
		}
	}
	if len(stack) > 0 {
		return "Unterminated group"
	}
	for _, r := range refs {
		if !names[r] {
			return "Invalid named capture referenced"
		}
	}
	return ""
}

// jsClassEscape reads the escape at src[i] (after "\") inside a class: the character, whether it is a single character
// (\d \w \s are sets) and the next index.
func jsClassEscape(src []rune, i int) (rune, bool, int) {
	n := len(src)
	hex := func(from, count int) (rune, bool) {
		if from+count > n {
			return 0, false
		}
		v, err := strconv.ParseUint(string(src[from:from+count]), 16, 32)
		return rune(v), err == nil
	}
	switch e := src[i]; e {
	case 'd', 'D', 's', 'S', 'w', 'W':
		return 0, false, i + 1
	case 'b':
		return 8, true, i + 1
	case 'n':
		return '\n', true, i + 1
	case 't':
		return '\t', true, i + 1
	case 'r':
		return '\r', true, i + 1
	case 'f':
		return '\f', true, i + 1
	case 'v':
		return '\v', true, i + 1
	case 'x':
		if v, ok := hex(i+1, 2); ok {
			return v, true, i + 3
		}
		return 'x', true, i + 1
	case 'u':
		if v, ok := hex(i+1, 4); ok {
			return v, true, i + 5
		}
		return 'u', true, i + 1
	case 'c':
		if i+1 < n && (src[i+1] >= 'a' && src[i+1] <= 'z' || src[i+1] >= 'A' && src[i+1] <= 'Z') {
			return src[i+1] & 31, true, i + 2
		}
		return '\\', true, i
	default:
		if e >= '0' && e <= '7' {
			v, j := rune(0), i
			for j < n && j < i+3 && src[j] >= '0' && src[j] <= '7' && v*8+(src[j]-'0') <= 255 {
				v = v*8 + (src[j] - '0')
				j++
			}
			return v, true, j
		}
		return e, true, i + 1
	}
}
