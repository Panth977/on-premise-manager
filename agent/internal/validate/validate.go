// Package validate is the Go port of shared/src/validate.ts (SPEC-final §11). Same codes, paths and severities.
package validate

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"opm/agent/internal/canonical"
	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// RE holds the shared patterns (TS RE).
var RE = struct {
	Slug, VolumeSlug, ID, Tag, Version, EnvName, Icon, Picture, Color, Sha256, ULID, RouteID, Email, E164, Repository, Mime, HTTPSURL, HTTPSURLAny, HeaderName, IPv4, DNSName, ActivityType *regexp.Regexp
}{
	Slug: regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`),
	// SiteVolume.volumeSlug (VolumeSource volume.volumeSlug): a declared data folder of a site (suggested `<containerSlug>-<imageVolume>`)
	VolumeSlug: regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,99}$`),
	ID:         regexp.MustCompile(`^[A-Za-z0-9]{20}$`),
	Tag:        regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`), // docker tag (inside an image reference)
	Version:    regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`),         // OPM image version (Image document id)
	EnvName:    regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`),
	// service / template / tag icons: "lucide:<name>", "upload:<iconId>" or a legacy bare Lucide name (null = default icon)
	Icon: regexp.MustCompile(`^(lucide:[a-z0-9-]+|upload:[A-Za-z0-9]{20}|[a-z0-9-]+)$`),
	// Account.img / Deployment.image: an icon reference or a legacy https URL
	Picture:      regexp.MustCompile(`^(lucide:[a-z0-9-]+|upload:[A-Za-z0-9]{20}|https://\S+)$`),
	Color:        regexp.MustCompile(`^#[0-9a-f]{6}$`),
	Sha256:       regexp.MustCompile(`^sha256:[0-9a-f]{64}$`),
	ULID:         regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`),
	RouteID:      regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`),
	Email:        regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`),
	E164:         regexp.MustCompile(`^\+[1-9]\d{6,14}$`),
	Repository:   regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*$`),
	Mime:         regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*/[a-z0-9][a-z0-9.+-]*$`),
	HTTPSURL:     regexp.MustCompile(`^https://[^\s/?#]+$`),
	HTTPSURLAny:  regexp.MustCompile(`^https://\S+$`),
	HeaderName:   regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$"),
	IPv4:         regexp.MustCompile(`^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$`),
	DNSName:      regexp.MustCompile(`(?i)^(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`),
	ActivityType: regexp.MustCompile(`^[a-z]+(-[a-z]+)*(\.[a-z-]+)*$`),
}

// ReservedSiteSlugs collide with agent GUI routes.
var ReservedSiteSlugs = []string{"backups", "files", "internet", "revisions", "secrets", "sites", "updates", "variables"}

// Limits is TS LIMITS.
var Limits = struct {
	Label, Description, Value, Variables, Secrets, Files, Sites, ContainersPerSite, EnvPerContainer, VolumesPerContainer, VolumesPerSite, PortsPerImage, Hosts, Routes, EnumOptions, ImageEnvList, SecretBytes, FileMaxBytes, NotFoundPageBytes, CommandItems, Summary, Note int
}{80, 2000, 4096, 200, 100, 100, 20, 50, 200, 50, 100, 50, 50, 200, 100, 50, 8192, 52428800, 262144, 100, 200, 500}

// EdgeMethods are the methods a route may match.
var EdgeMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// Mode is the validation mode.
type Mode string

const (
	ModeTemplate Mode = "template"
	ModeDraft    Mode = "draft"
	ModePublish  Mode = "publish"
	ModeAgent    Mode = "agent"
)

// Issues collects validation issues.
type Issues struct {
	Mode Mode
	List []model.ValidationIssue
}

// NewIssues returns an empty collector.
func NewIssues(m Mode) *Issues { return &Issues{Mode: m, List: []model.ValidationIssue{}} }

// Err adds an error; publish-level issues are warnings in template / draft mode.
func (is *Issues) Err(path, code, msg string, publishLevel bool) {
	if code == expr.EDeferred {
		return // set at deployment (required variable / secret): never an issue
	}
	strict := is.Mode == ModePublish || is.Mode == ModeAgent
	sev := "error"
	if publishLevel && !strict {
		sev = "warning"
	}
	is.List = append(is.List, model.ValidationIssue{Path: path, Code: code, Message: msg, Severity: sev})
}

func (is *Issues) err(path, code, msg string) { is.Err(path, code, msg, false) }

// Warn adds a warning.
func (is *Issues) Warn(path, code, msg string) {
	if code == expr.EDeferred {
		return
	}
	is.List = append(is.List, model.ValidationIssue{Path: path, Code: code, Message: msg, Severity: "warning"})
}

// Info adds an advice (severity info; never a warning or blocker).
func (is *Issues) Info(path, code, msg string) {
	is.List = append(is.List, model.ValidationIssue{Path: path, Code: code, Message: msg, Severity: "info"})
}

// Errors returns the issues with severity error.
func (is *Issues) Errors() []model.ValidationIssue {
	out := []model.ValidationIssue{}
	for _, x := range is.List {
		if x.Severity == "error" {
			out = append(out, x)
		}
	}
	return out
}

// Ok reports no errors.
func (is *Issues) Ok() bool { return len(is.Errors()) == 0 }

// Report returns the ValidationReport.
func (is *Issues) Report() model.ValidationReport {
	l := is.List
	if l == nil {
		l = []model.ValidationIssue{}
	}
	return model.ValidationReport{Ok: is.Ok(), Issues: l}
}

func isIntF(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) }
func numText(f float64) string {
	s, err := canonical.FormatNumber(f)
	if err != nil {
		return fmt.Sprint(f)
	}
	return s
}
func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func checkLabel(v, path string, is *Issues) {
	if strings.TrimSpace(v) == "" {
		is.err(path, "V_REQUIRED", "required")
		return
	}
	if utf16Len(v) > Limits.Label {
		is.err(path, "V_LENGTH", fmt.Sprintf("at most %d characters", Limits.Label))
	}
}
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}
func checkDescription(v, path string, is *Issues) {
	if utf16Len(v) > Limits.Description {
		is.err(path, "V_LENGTH", fmt.Sprintf("at most %d characters", Limits.Description))
	}
}
func checkSlug(v, path string, is *Issues) {
	if !RE.Slug.MatchString(v) {
		is.err(path, "V_FORMAT", fmt.Sprintf(`"%s" is not a slug (^[a-z][a-z0-9-]{1,39}$)`, v))
	}
}
func checkPortF(v float64, path string, is *Issues, min int) {
	if !isIntF(v) || v < float64(min) || v > 65535 {
		is.err(path, "V_RANGE", fmt.Sprintf("%s must be an integer %d–65535", numText(v), min))
	}
}
func checkPort(v int, path string, is *Issues, min int) { checkPortF(float64(v), path, is, min) }

var wsRe = regexp.MustCompile(`\s`)

// IsSafeMountPath reports a resolved mount path that is absolute, not "/", without whitespace or "..", ≤ 1024 (TS isSafeMountPath).
func IsSafeMountPath(v string) bool {
	return strings.HasPrefix(v, "/") && v != "/" && !wsRe.MatchString(v) && !strings.Contains(v, "..") && utf16Len(v) <= 1024
}

// checkMountPath checks a mount path as written (save level). Literal → V_MOUNT rules. With {{ }} → must start with a
// literal "/" (V_PATH) and be a valid expression for field mountPath; the resolved value is checked at publish (V_PATH).
func checkMountPath(v, path string, is *Issues) {
	if expr.IsExpr(v) {
		if !strings.HasPrefix(v, "/") {
			is.err(path, "V_PATH", `mount path must be absolute: start with "/" (e.g. /app/config/{{ container.env.NODE_ENV }}.ts)`)
		}
		if utf16Len(v) > 1024 {
			is.err(path, "V_LENGTH", "at most 1024 characters")
		}
		checkExprField(v, expr.FieldMountPath, path, is)
		return
	}
	if !IsSafeMountPath(v) {
		is.err(path, "V_MOUNT", `mount path must be absolute, not "/", without spaces or ".."`)
	}
}

// CheckIcon checks a service / template / tag icon: nil, "lucide:<name>", "upload:<iconId>" or a legacy bare Lucide name.
func CheckIcon(v *string, path string, is *Issues) {
	if v != nil && !RE.Icon.MatchString(*v) {
		is.err(path, "V_FORMAT", "icon must be null, lucide:<name> or upload:<iconId>")
	}
}

// CheckPicture checks Account.img / Deployment.image: nil, an icon reference or an https URL.
func CheckPicture(v *string, path string, is *Issues) {
	if v != nil && !RE.Picture.MatchString(*v) {
		is.err(path, "V_FORMAT", "must be null, lucide:<name>, upload:<iconId> or an https URL")
	}
}

var re2Bad = regexp.MustCompile(`\(\?[=!]|\(\?<[=!]|\\[1-9]`)

// CheckRe2 rejects look-around / back-references and invalid patterns.
func CheckRe2(pattern, path string, is *Issues) {
	if re2Bad.MatchString(pattern) {
		is.err(path, "V_PATTERN", "look-around and back-references are not RE2")
		return
	}
	if _, err := regexp.Compile(pattern); err != nil {
		is.err(path, "V_PATTERN", "invalid regular expression: "+err.Error())
	}
}

func dupes(keys []string) []string {
	seen := map[string]bool{}
	d := map[string]bool{}
	out := []string{}
	for _, k := range keys {
		if seen[k] && !d[k] {
			d[k] = true
			out = append(out, k)
		}
		seen[k] = true
	}
	return out
}

func asExprErr(err error) *expr.Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*expr.Error); ok {
		return e
	}
	return &expr.Error{Code: expr.ESyntax, Message: err.Error()}
}

func checkExprField(v string, f expr.Field, path string, is *Issues) {
	for _, e := range expr.CheckExpr(v, f) {
		is.err(path, e.Code, fmt.Sprintf("%s (at %d)", e.Message, e.Pos))
	}
}

// ───────────────────────── images

func validateImagePorts(ports []model.ImagePort, is *Issues, base string) {
	if len(ports) > Limits.PortsPerImage {
		is.err(base, "V_TOO_MANY", fmt.Sprintf("at most %d", Limits.PortsPerImage))
	}
	names := []string{}
	for _, p := range ports {
		names = append(names, p.Name)
	}
	d := dupes(names)
	for _, p := range ports {
		at := fmt.Sprintf("%s[%s]", base, p.Name)
		checkSlug(p.Name, at+".name", is)
		if contains(d, p.Name) {
			is.err(at+".name", "V_DUPLICATE", fmt.Sprintf(`port name "%s" is used twice`, p.Name))
		}
		if p.Port.Num != nil {
			checkPortF(*p.Port.Num, at+".port", is, 1)
		} else if p.Port.Expr != nil && expr.IsExpr(*p.Port.Expr) {
			checkExprField(*p.Port.Expr, expr.FieldImagePort, at+".port", is)
		} else {
			is.err(at+".port", "V_TYPE", "port must be a number or one whole {{ }} block")
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			is.err(at+".protocol", "V_ENUM", "protocol must be tcp | udp")
		}
		checkDescription(p.Description, at+".description", is)
	}
}

// ValidateImageEnv is TS validateImageEnv (items are normalised first: legacy value lists → options).
func ValidateImageEnv(env []model.ImageEnv, is *Issues, base string) {
	norm := make([]model.ImageEnv, len(env))
	for i, e := range env {
		norm[i] = NormalizeImageEnv(e)
	}
	env = norm
	names := []string{}
	for _, e := range env {
		names = append(names, e.Name)
	}
	d := dupes(names)
	for _, e := range env {
		at := fmt.Sprintf("%s[%s]", base, e.Name)
		if !RE.EnvName.MatchString(e.Name) {
			is.err(at+".name", "V_FORMAT", fmt.Sprintf(`"%s" does not match ^[A-Z_][A-Z0-9_]*$`, e.Name))
		}
		if contains(d, e.Name) {
			is.err(at+".name", "V_DUPLICATE", fmt.Sprintf(`env "%s" is defined twice`, e.Name))
		}
		opts := e.OptionList()
		if opts != nil {
			if len(opts) < 1 || len(opts) > Limits.ImageEnvList {
				is.err(at+".options", "V_LENGTH", fmt.Sprintf("a choice needs 1–%d options", Limits.ImageEnvList))
			}
			if len(dupes(opts)) > 0 {
				is.err(at+".options", "V_DUPLICATE", "options must be unique")
			}
			badLen, hasExpr := false, false
			for _, x := range opts {
				if x == "" || utf16Len(x) > Limits.Value {
					badLen = true
				}
				if expr.IsExpr(x) {
					hasExpr = true
				}
			}
			if badLen {
				is.err(at+".options", "V_LENGTH", fmt.Sprintf("options must be 1–%d characters", Limits.Value))
			}
			if hasExpr {
				is.err(at+".options", "V_FORMAT", "options are plain values (no {{ }})")
			}
			if e.Secret {
				is.err(at+".options", "V_IMAGE_ENV", "a secret env cannot have options")
			}
		}
		if e.Value != nil {
			if e.Secret {
				is.err(at+".value", "V_IMAGE_ENV", "a secret env cannot have a default value")
			}
			if e.Value.Str != nil {
				v := *e.Value.Str
				if utf16Len(v) > Limits.Value {
					is.err(at+".value", "V_LENGTH", fmt.Sprintf("at most %d characters", Limits.Value))
				}
				if opts != nil {
					if !contains(opts, v) {
						is.err(at+".value", "V_OPTION", fmt.Sprintf(`the default "%s" must be one of the options (%s)`, v, strings.Join(opts, ", ")))
					}
				} else {
					checkExprField(v, expr.FieldImageEnv, at+".value", is)
				}
			} else {
				is.err(at+".value", "V_TYPE", "value must be a string")
			}
		}
		checkDescription(e.Description, at+".description", is)
	}
}

var (
	reExprBlock = regexp.MustCompile(`\{\{[\s\S]*?\}\}`)
	reFileExt   = regexp.MustCompile(`^[^.].*\.([A-Za-z0-9]{1,10})$`)
	reHasLetter = regexp.MustCompile(`[A-Za-z]`)
)

// InferImageVolumeKind is TS inferImageVolumeKind (§21 #83): a last path segment ({{ }} blocks → "x") with an
// extension (a letter in it, not ".d") → file; else dir.
func InferImageVolumeKind(mountPath string) string {
	if strings.HasSuffix(mountPath, "/") {
		return "dir"
	}
	plain := reExprBlock.ReplaceAllString(mountPath, "x")
	last := plain[strings.LastIndex(plain, "/")+1:]
	m := reFileExt.FindStringSubmatch(last)
	if m != nil && reHasLetter.MatchString(m[1]) && strings.ToLower(m[1]) != "d" {
		return "file"
	}
	return "dir"
}

// ImageVolumeKind is TS imageVolumeKind: the stored kind, or (key absent) inferred from the mount path.
func ImageVolumeKind(v model.ImageVolume) string {
	if v.Kind != nil && (*v.Kind == "dir" || *v.Kind == "file") {
		return *v.Kind
	}
	return InferImageVolumeKind(v.MountPath)
}

// FileVolumeRenameInfo is TS FILE_VOLUME_RENAME_INFO.
const FileVolumeRenameInfo = "apps that save by rename (e.g. Node-RED flows.json) fail on single-file mounts (EBUSY) — use a folder volume seeded with the file"

// ValidateImageVolumes is TS validateImageVolumes (literal duplicate mount paths; resolved ones are checked per container).
func ValidateImageVolumes(vols []model.ImageVolume, is *Issues, base string) {
	names, mps := []string{}, []string{}
	for _, v := range vols {
		names = append(names, v.Name)
		mps = append(mps, v.MountPath)
	}
	d, dm := dupes(names), dupes(mps)
	for _, v := range vols {
		at := fmt.Sprintf("%s[%s]", base, v.Name)
		checkSlug(v.Name, at+".name", is)
		if contains(d, v.Name) {
			is.err(at+".name", "V_DUPLICATE", fmt.Sprintf(`volume "%s" is defined twice`, v.Name))
		}
		checkMountPath(v.MountPath, at+".mountPath", is)
		if contains(dm, v.MountPath) {
			is.err(at+".mountPath", "V_MOUNT", fmt.Sprintf("mount path %s is used twice", v.MountPath))
		}
		if v.Kind != nil && *v.Kind != "dir" && *v.Kind != "file" {
			is.err(at+".kind", "V_ENUM", "kind must be dir (Folder) | file (File)")
		} else if ImageVolumeKind(v) == "file" && strings.HasSuffix(v.MountPath, "/") {
			is.err(at+".mountPath", "V_VOLUME_KIND", fmt.Sprintf(`image volume %s is a File: its mount path must not end with "/" (or set it to Folder)`, v.Name))
		}
		checkDescription(v.Description, at+".description", is)
	}
}

// ValidateHealthCheck checks a health check (nil = none).
func ValidateHealthCheck(h *model.HealthCheck, portNames []string, is *Issues, base string) {
	if h == nil {
		return
	}
	switch h.Type {
	case "http", "tcp", "exec", "log":
	default:
		is.err(base+".type", "V_ENUM", "type must be http | tcp | exec | log")
		return
	}
	rng := func(k string, v float64, min, max float64) {
		if !isIntF(v) || v < min || v > max {
			is.err(base+"."+k, "V_RANGE", fmt.Sprintf("%s must be an integer %s–%s", k, numText(min), numText(max)))
		}
	}
	rng("intervalSec", h.IntervalSec, 1, 3600)
	rng("timeoutSec", h.TimeoutSec, 1, 600)
	rng("retries", h.Retries, 1, 100)
	rng("startPeriodSec", h.StartPeriodSec, 0, 3600)
	if h.Type == "http" || h.Type == "tcp" {
		if !contains(portNames, h.PortName) {
			pn := strings.Join(portNames, ", ")
			if pn == "" {
				pn = "none"
			}
			is.err(base+".portName", "V_REF", fmt.Sprintf(`"%s" is not one of the image ports (%s)`, h.PortName, pn))
		}
	}
	if h.Type == "http" {
		if !strings.HasPrefix(h.Path, "/") {
			is.err(base+".path", "V_FORMAT", "path must start with /")
		}
		rng("expectStatus", h.ExpectStatus, 100, 599)
	}
	if h.Type == "exec" {
		if len(h.Command) < 1 || len(h.Command) > 50 {
			is.err(base+".command", "V_LENGTH", "command must be 1–50 strings")
		}
		rng("expectExitCode", h.ExpectExitCode, 0, 255)
	}
	if h.Type == "log" {
		CheckRe2(h.Pattern, base+".pattern", is)
	}
}

func validateCommand(v []string, path string, is *Issues) {
	if v == nil {
		return
	}
	bad := len(v) > Limits.CommandItems
	for _, x := range v {
		if utf16Len(x) > Limits.Value {
			bad = true
		}
	}
	if bad {
		is.err(path, "V_TYPE", fmt.Sprintf("must be null or a list of ≤ %d strings", Limits.CommandItems))
	}
}

// ───────────────────────── variables, secrets, files

// VariableValueMatches reports whether value matches the variable type.
func VariableValueMatches(typ string, options []string, value any) bool {
	if value == nil {
		return true
	}
	switch typ {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		f, ok := value.(float64)
		return ok && !math.IsNaN(f) && !math.IsInf(f, 0)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "enum":
		s, ok := value.(string)
		return ok && options != nil && contains(options, s)
	case "list":
		l, ok := value.([]any)
		if !ok {
			return false
		}
		for _, x := range l {
			if _, ok := x.(string); !ok {
				return false
			}
		}
		return true
	case "json":
		switch value.(type) {
		case []any, map[string]any:
			return true
		}
		return false
	}
	return false
}

func validateVariable(v *model.Variable, base string, is *Issues) {
	checkSlug(v.VariableSlug, base+".variableSlug", is)
	checkLabel(v.Label, base+".label", is)
	checkDescription(v.Description, base+".description", is)
	if !contains([]string{"string", "number", "boolean", "enum", "list", "json"}, v.Type) {
		is.err(base+".type", "V_ENUM", "type must be string | number | boolean | enum | list | json")
		return
	}
	if v.Type == "enum" {
		if v.Options == nil || len(v.Options) < 1 || len(v.Options) > Limits.EnumOptions {
			is.err(base+".options", "V_LENGTH", fmt.Sprintf("enum needs 1–%d string options", Limits.EnumOptions))
		} else if len(dupes(v.Options)) > 0 {
			is.err(base+".options", "V_DUPLICATE", "options must be unique")
		}
	} else if v.Options != nil {
		is.err(base+".options", "V_TYPE", "options are only for type enum (use null)")
	}
	val := v.Validation
	if val != nil {
		if (val.Min != nil || val.Max != nil) && v.Type != "number" {
			is.err(base+".validation", "V_TYPE", "min / max are only for type number")
		}
		if val.Pattern != nil && v.Type != "string" {
			is.err(base+".validation", "V_TYPE", "pattern is only for type string")
		}
		if val.Min != nil && val.Max != nil && *val.Min > *val.Max {
			is.err(base+".validation", "V_RANGE", "min > max")
		}
		if val.Pattern != nil {
			CheckRe2(*val.Pattern, base+".validation.pattern", is)
		}
	}
	if !VariableValueMatches(v.Type, v.Options, v.Value.V) {
		extra := ""
		if v.Type == "enum" {
			extra = fmt.Sprintf(" (one of %s)", strings.Join(v.Options, ", "))
		}
		is.err(base+".value", "V_VALUE_TYPE", fmt.Sprintf("value does not match type %s%s", v.Type, extra))
		return
	}
	if v.Value.V != nil && val != nil {
		if v.Type == "number" {
			f := v.Value.V.(float64)
			if val.Min != nil && f < *val.Min {
				is.err(base+".value", "V_VALUE_RULE", "value must be ≥ "+numText(*val.Min))
			}
			if val.Max != nil && f > *val.Max {
				is.err(base+".value", "V_VALUE_RULE", "value must be ≤ "+numText(*val.Max))
			}
		}
		if v.Type == "string" && val.Pattern != nil && *val.Pattern != "" {
			if re, err := regexp.Compile(`^(?:` + *val.Pattern + `)$`); err == nil {
				if !re.MatchString(v.Value.V.(string)) {
					is.err(base+".value", "V_VALUE_RULE", "value does not match "+*val.Pattern)
				}
			}
		}
	}
	if s, ok := v.Value.V.(string); ok && v.Type == "string" && utf16Len(s) > Limits.Value {
		is.err(base+".value", "V_LENGTH", fmt.Sprintf("at most %d characters", Limits.Value))
	}
	s, ok := v.Value.V.(string)
	empty := v.Value.V == nil || (ok && s == "")
	// template: a required variable is filled in at deployment (the deploy form asks for it) → no issue at all
	if v.Required && empty && is.Mode != ModeTemplate {
		is.Err(base+".value", "V_VAR_REQUIRED", fmt.Sprintf("%s is required — set it before publishing", v.VariableSlug), true)
	}
	if !v.Required && empty && is.Mode == ModeTemplate {
		is.Info(base+".value", "V_VAR_OPTIONAL_NULL", fmt.Sprintf("optional var %s has no value; add | default(…) where it is used, or a template default", v.VariableSlug))
	}
}

func validateSecretDef(s *model.SecretDef, base string, is *Issues, template bool) {
	checkSlug(s.SecretSlug, base+".secretSlug", is)
	checkLabel(s.Label, base+".label", is)
	checkDescription(s.Description, base+".description", is)
	if s.Generate != nil {
		if !contains([]string{"alnum", "hex", "base64url"}, s.Generate.Charset) {
			is.err(base+".generate.charset", "V_ENUM", "charset must be alnum | hex | base64url")
		}
		if s.Generate.Length < 8 || s.Generate.Length > 128 {
			is.err(base+".generate.length", "V_RANGE", "length must be 8–128")
		}
	}
	if template && s.Value != nil {
		is.err(base+".value", "V_TEMPLATE_SECRET_VALUE", "templates never hold secret values")
	}
	if s.Value != nil && len(*s.Value) > Limits.SecretBytes {
		is.err(base+".value", "V_LENGTH", fmt.Sprintf("at most %d bytes", Limits.SecretBytes))
	}
	if !template && s.Required && (s.Value == nil || *s.Value == "") {
		is.Err(base+".value", "V_SECRET_REQUIRED", fmt.Sprintf("secret %s is required — set it before publishing", s.SecretSlug), true)
	}
}

func validateFileDef(f *model.FileDef, base string, is *Issues) {
	checkSlug(f.FileSlug, base+".fileSlug", is)
	checkLabel(f.Label, base+".label", is)
	checkDescription(f.Description, base+".description", is)
	if !RE.Mime.MatchString(f.Mime) {
		is.err(base+".mime", "V_FORMAT", "mime must be type/subtype")
	}
	if f.Editor != "text" && f.Editor != "binary" {
		is.err(base+".editor", "V_ENUM", "editor must be text | binary")
	}
	if f.Render.Set && (f.Render.Value == nil || (*f.Render.Value != "expressions" && *f.Render.Value != "none")) {
		is.err(base+".render", "V_ENUM", "render must be expressions | none")
	} else if f.Editor == "binary" && f.Render.Value != nil && *f.Render.Value == "expressions" {
		is.err(base+".render", "V_ENUM", "binary files are mounted as they are (render none)")
	}
	if f.MaxBytes < 1 || f.MaxBytes > int64(Limits.FileMaxBytes) {
		is.err(base+".maxBytes", "V_RANGE", fmt.Sprintf("maxBytes must be 1–%d", Limits.FileMaxBytes))
	}
	CheckLocation(f.Location, "files", base+".location", is)
	if f.Content != nil {
		if !RE.Sha256.MatchString(f.Content.Sha256) {
			is.err(base+".content.sha256", "V_FORMAT", `sha256 must be "sha256:" + 64 hex`)
		}
		if f.Content.SizeBytes < 0 {
			is.err(base+".content.sizeBytes", "V_RANGE", "sizeBytes must be ≥ 0")
		} else if f.Content.SizeBytes > f.MaxBytes {
			is.err(base+".content", "V_FILE_CONTENT", fmt.Sprintf("content is %d bytes; maxBytes is %d", f.Content.SizeBytes, f.MaxBytes))
		}
	}
}

// ───────────────────────── catalog view

// CatalogImage is the image view used for validation.
type CatalogImage struct {
	Kind         string
	Ports        []model.ImagePort
	Env          []model.ImageEnv
	Volumes      []model.ImageVolume
	Healthcheck  *model.HealthCheck
	ImportStatus string // "" = ready
}

// Context is TS ValidationContext.
type Context struct {
	Mode            Mode
	DeploymentID    string
	Services        map[string]string // serviceSlug → serviceSlug (the key IS the slug; value kept for lookups)
	Images          map[string]CatalogImage
	NotFoundPageIDs []string // nil = do not check
	SecretsSet      []string // nil = use SecretDef.Value
	// DetectedHosts: siteSlug → advertised / detected address (auto hosts). Agent: itself + its peers.
	DetectedHosts map[string]string
	// FileContents: fileSlug → text content of render 'expressions' files. When set (agent staging), every file is
	// rendered for every container that mounts it (V_FILE_RENDER / V_FILE_JSON).
	FileContents map[string]string
	// BlobContents (§21 #101): sha256 → text content of volume content entries with render 'expressions' (starter
	// content). When set, each is rendered once per site: V_FILE_RENDER at …volumes[v].seed.entries[path].
	BlobContents map[string]string
}

// ImageKey is serviceSlug + ":" + version.
func ImageKey(serviceSlug, version string) string { return serviceSlug + ":" + version }

// ContextFromCatalog builds a Context from a revision catalog.
func ContextFromCatalog(mode Mode, deploymentID string, cat *model.RevisionCatalog) Context {
	c := Context{Mode: mode, DeploymentID: deploymentID, Services: map[string]string{}, Images: map[string]CatalogImage{}, NotFoundPageIDs: []string{}}
	if cat == nil {
		return c
	}
	for slug := range cat.Services {
		c.Services[slug] = slug
	}
	for k, im := range cat.Images {
		c.Images[k] = CatalogImage{Kind: im.Kind, Ports: im.Ports, Env: im.Env, Volumes: im.Volumes, Healthcheck: im.Healthcheck}
	}
	for id := range cat.NotFoundPages {
		c.NotFoundPageIDs = append(c.NotFoundPageIDs, id)
	}
	sort.Strings(c.NotFoundPageIDs)
	return c
}
