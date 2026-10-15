// Package expr is the Go port of shared/src/expr.ts (OPM {{ … }} expressions). Same grammar, same error codes,
// same positions (0-based Unicode code point indexes). Vectors: shared/testdata/expr-vectors.json.
package expr

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/canonical"
)

// Roots allowed in paths.
var Roots = []string{"var", "secret", "file", "site", "self", "deployment", "container", "containers"}

// Filters known by the engine.
var Filters = []string{"default", "join", "upper", "lower", "trim", "base64", "url", "int", "json", "sha256", "sha512", "mosquitto_passwd"}

// FilterMinAgent is TS FILTER_MIN_AGENT (§21 #103): filters added after the first agents shipped. An older agent
// refuses a revision that uses one (E_UNKNOWN_FILTER at staging); the cloud warns per site.
var FilterMinAgent = map[string]string{"sha256": "0.13.1", "sha512": "0.13.1", "mosquitto_passwd": "0.13.1"}

// PasswordFilters (§21 #103) are only allowed where secrets are (SecretFields): their input is normally a secret.
var PasswordFilters = []string{"mosquitto_passwd"}

// HashingFilters (§21 #103, TS HASHING_FILTERS) output a salted one-way password hash meant for a readable file: a secret
// that reaches a result ONLY through one of them sets Result.SecretHashed (still Secret / masked), and a rendered File
// with only such secrets is written 0444 (Mosquitto opens password_file after dropping to its own user).
var HashingFilters = []string{"mosquitto_passwd"}

// Field is the kind of field a value belongs to (TS ExprField).
type Field string

const (
	FieldEnv       Field = "env"
	FieldImageEnv  Field = "imageEnv"
	FieldHost      Field = "host"
	FieldHostPort  Field = "hostPort"
	FieldImagePort Field = "imagePort"
	FieldEdge      Field = "edge"
	FieldMountPath Field = "mountPath"
	// FieldFile is FileDef content with render 'expressions', rendered ONCE PER SITE (round D; multi-line text).
	FieldFile Field = "file"
	// FieldDevtool (§21 #99) is a dev-tool preset value (and agent dev-tool expressions), resolved on the site from the
	// head config: the only field where containers.<slug>.env.<NAME> is allowed (no container value can reference it, so
	// there are no cross-container cycles).
	FieldDevtool Field = "devtool"
)

// FieldRoots maps a field to its allowed roots (TS FIELD_ROOTS).
var FieldRoots = map[Field][]string{
	FieldEnv:       {"var", "secret", "file", "site", "self", "deployment", "container", "containers"},
	FieldImageEnv:  {"var", "file", "site", "self", "deployment", "container", "containers"},
	FieldHost:      {"var", "deployment"},
	FieldHostPort:  {"var", "deployment"},
	FieldImagePort: {"var", "deployment"},
	FieldEdge:      {"var", "deployment", "self"},
	// ImageVolume.mountPath and Container.volume[].mountPath (result: absolute path, not "/" — V_PATH in validate)
	FieldMountPath: {"var", "deployment", "self", "container"},
	// FileDef content with render 'expressions', resolved per mounting container (blocks stay on one line)
	// round D: rendered once per site (no container.*, no file.*); containers.* are site-level facts (§21 #88)
	FieldFile: {"var", "secret", "site", "self", "deployment", "containers"},
	// §21 #99 dev-tool presets (secrets allowed; containers.<slug>.env.<NAME> only here)
	FieldDevtool: {"var", "secret", "site", "self", "deployment", "containers"},
}

// SecretFields are the fields where secret.* and secret-valued container.env.<NAME> / containers.<slug>.env.<NAME>
// are allowed (TS SECRET_FIELDS).
var SecretFields = []Field{FieldEnv, FieldFile, FieldDevtool}

// ContainersEnvFields are the only fields where containers.<containerSlug>.env.<NAME> is allowed (TS CONTAINERS_ENV_FIELDS, §21 #99).
var ContainersEnvFields = []Field{FieldDevtool}

// IsSecretField reports whether f is in SecretFields.
func IsSecretField(f Field) bool {
	for _, x := range SecretFields {
		if x == f {
			return true
		}
	}
	return false
}

// FieldLabel is used in E_NOT_ALLOWED messages.
var FieldLabel = map[Field]string{
	FieldEnv:       "Container.env[].value",
	FieldImageEnv:  "ImageEnv.value",
	FieldHost:      "SiteConfig.host",
	FieldHostPort:  "Container.port[].hostPort",
	FieldImagePort: "ImagePort.port",
	FieldEdge:      "edge fields (hostname, redirect.to, setHeaders)",
	FieldMountPath: "volume mountPath",
	FieldFile:      "file content",
	FieldDevtool:   "dev-tool presets",
}

// IsNumeric reports whether the field is a number field (hostPort, imagePort).
func IsNumeric(f Field) bool { return f == FieldHostPort || f == FieldImagePort }

// Error codes.
const (
	ESyntax         = "E_SYNTAX"
	EUnknownPath    = "E_UNKNOWN_PATH"
	ENotAllowed     = "E_NOT_ALLOWED"
	EUnknownFilter  = "E_UNKNOWN_FILTER"
	EType           = "E_TYPE"
	ENull           = "E_NULL"
	ENoHostPort     = "E_NO_HOSTPORT"
	EFileNotMounted = "E_FILE_NOT_MOUNTED"
	ECycle          = "E_CYCLE"
	EDeferred       = "E_DEFERRED" // round E: depends on a required variable / secret set at deployment (never an issue)
)

// Error is an expression error (TS ExprError).
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Pos     int    `json:"pos"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s at %d: %s", e.Code, e.Pos, e.Message) }

func errf(code string, pos int, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), Pos: pos}
}

// Node types.
type (
	// Lit is a literal: string, float64 or bool.
	Lit struct {
		Value any
		Pos   int
	}
	// Path is root.seg.seg…
	Path struct {
		Segments []string
		Pos      int
	}
	// FilterNode is one "| name(args)".
	FilterNode struct {
		Name string
		Args []any
		Pos  int
	}
	// Segment is either text (IsBlock false) or a block.
	Segment struct {
		IsBlock bool
		Text    string
		Lit     *Lit
		Path    *Path
		Filters []FilterNode
		Start   int
		End     int
	}
)

// FileRef: Path nil = the container does not mount the file.
type FileRef struct {
	Path *string `json:"path"`
}

// SiteRef is one site in the context.
type SiteRef struct {
	Host *string                        `json:"host"`
	Port map[string]map[string]*float64 `json:"port"`
}

// EnvRef is the final value of one env var of the container (TS container.env[NAME]).
// Value nil = known name without a value; Secret = secretRef / secret.* value.
type EnvRef struct {
	Value  *string `json:"value"`
	Secret bool    `json:"secret"`
}

// ContainerRef is the container the value belongs to. Env: see TS ExprContext.container.env
// (ResolveContainer fills it lazily through hooks instead).
type ContainerRef struct {
	Slug string            `json:"slug"`
	Env  map[string]EnvRef `json:"env,omitempty"`
}

// Context mirrors TS ExprContext. Var values: nil, bool, float64, string, []any, map[string]any.
type Context struct {
	Deployment struct {
		ID string `json:"id"`
	} `json:"deployment"`
	Self struct {
		Slug string  `json:"slug"`
		Host *string `json:"host"` // nil = auto host not known yet → E_NULL
	} `json:"self"`
	Container *ContainerRef      `json:"container,omitempty"`
	Var       map[string]any     `json:"var"`
	Secret    map[string]*string `json:"secret,omitempty"`
	File      map[string]FileRef `json:"file,omitempty"`
	Site      map[string]SiteRef `json:"site,omitempty"`
	// Containers: containers.<containerSlug>.host / .port.<portName> (§21 #88), the containers of THIS site.
	Containers map[string]ContainerPeer `json:"containers,omitempty"`
}

// ContainerPeer is one container of the site for containers.<slug>.* (TS ContainerPeer): Host = the DNS name on the
// site network (the network alias = containerSlug); Port[portName] = the INTERNAL port of that image port (nil = does
// not resolve → E_NULL); Enabled false still resolves (validate warns V_CONTAINER_REF_DISABLED).
type ContainerPeer struct {
	Host    string              `json:"host"`
	Enabled bool                `json:"enabled"`
	Port    map[string]*float64 `json:"port"`
	// Env (§21 #99, field devtool only; validate.SiteDevtoolExprContext fills it): the FINAL env values of that
	// container (TS ContainerPeer.env). nil = no env known (any NAME → E_UNKNOWN_PATH).
	Env map[string]PeerEnv `json:"env,omitempty"`
}

// PeerEnv is one env var of another container (containers.<slug>.env.<NAME>, TS PeerEnv): Value nil = no value;
// Secret = secretRef / secret.* value; Error = the value failed to resolve (its code is re-raised at the reference).
type PeerEnv struct {
	Value  *string `json:"value"`
	Secret bool    `json:"secret"`
	Error  *Error  `json:"error,omitempty"`
}

// AutoHostPlaceholder is the cloud text for an unknown auto host ("<auto:slug>"); it never reaches a site.
func AutoHostPlaceholder(siteSlug string) string { return "<auto:" + siteSlug + ">" }

// Str returns a pointer to s (helper for Context hosts).
func Str(s string) *string { return &s }

// LineCol returns the 1-based line + column (code points) of a 0-based code-point position in src (TS exprLineCol).
func LineCol(src string, pos int) (line, col int) {
	line, col = 1, 1
	i := 0
	for _, r := range src {
		if i >= pos {
			break
		}
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
		i++
	}
	return
}

// Result mirrors TS ResolveResult. Number is set for numeric fields only.
type Result struct {
	Value  string   `json:"value"`
	Number *int     `json:"number"`
	Masked string   `json:"masked"`
	Secret bool     `json:"secret"`
	Refs   []string `json:"refs"`
	// Deferred (round E): a required-but-unset variable / secret (DeployPlaceholder) contributed.
	Deferred bool `json:"deferred,omitempty"`
	// SecretHashed (§21 #103): Secret, but every secret part went through a HashingFilters filter (no secret in clear).
	SecretHashed bool `json:"secretHashed,omitempty"`
}

// DeployPlaceholder is TS DEPLOY_PLACEHOLDER: the stand-in of a REQUIRED variable / secret without a value outside
// agent mode (template, draft, publish checks).
const DeployPlaceholder = "‹set at deployment›"

// IsDeferredValue is TS isDeferredValue.
func IsDeferredValue(v any) bool {
	s, ok := v.(string)
	return ok && strings.Contains(s, DeployPlaceholder)
}

// ───────────────────────────── parser

func isLetter(r rune) bool  { return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }
func isDigit(r rune) bool   { return r >= '0' && r <= '9' }
func isSegChar(r rune) bool { return isLetter(r) || isDigit(r) || r == '-' || r == '_' }
func isWs(r rune) bool      { return r == ' ' || r == '\t' }

type parser struct{ cp []rune }

func (p *parser) at(i int) rune {
	if i < 0 || i >= len(p.cp) {
		return 0
	}
	return p.cp[i]
}
func (p *parser) skipWs(i int) int {
	for i < len(p.cp) && isWs(p.cp[i]) {
		i++
	}
	return i
}
func (p *parser) noNewline(i int) *Error {
	if i < len(p.cp) && (p.cp[i] == '\n' || p.cp[i] == '\r') {
		return errf(ESyntax, i, "a {{ }} block cannot span lines")
	}
	return nil
}

func (p *parser) template() ([]Segment, *Error) {
	var out []Segment
	var buf []rune
	bufStart := 0
	n := len(p.cp)
	i := 0
	for i < n {
		if p.cp[i] == '{' && i+1 < n && p.cp[i+1] == '{' {
			if len(buf) > 0 {
				out = append(out, Segment{Text: string(buf), Start: bufStart})
			}
			buf = nil
			start := i
			seg, next, err := p.block(i)
			if err != nil {
				return nil, err
			}
			seg.Start = start
			out = append(out, seg)
			i = next
			bufStart = i
		} else {
			if len(buf) == 0 {
				bufStart = i
			}
			buf = append(buf, p.cp[i])
			i++
		}
	}
	if len(buf) > 0 {
		out = append(out, Segment{Text: string(buf), Start: bufStart})
	}
	return out, nil
}

func (p *parser) block(i int) (Segment, int, *Error) {
	i += 2
	i = p.skipWs(i)
	if e := p.noNewline(i); e != nil {
		return Segment{}, 0, e
	}
	lit, path, next, err := p.primary(i)
	if err != nil {
		return Segment{}, 0, err
	}
	i = next
	var filters []FilterNode
	for {
		i = p.skipWs(i)
		if e := p.noNewline(i); e != nil {
			return Segment{}, 0, e
		}
		if i >= len(p.cp) || p.cp[i] != '|' {
			break
		}
		i = p.skipWs(i + 1)
		if e := p.noNewline(i); e != nil {
			return Segment{}, 0, e
		}
		f, nx, err := p.filter(i)
		if err != nil {
			return Segment{}, 0, err
		}
		filters = append(filters, f)
		i = nx
	}
	if !(p.at(i) == '}' && p.at(i+1) == '}' && i+1 < len(p.cp)) {
		return Segment{}, 0, errf(ESyntax, i, `expected "}}"`)
	}
	i += 2
	return Segment{IsBlock: true, Lit: lit, Path: path, Filters: filters, End: i}, i, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (p *parser) primary(i int) (*Lit, *Path, int, *Error) {
	n := len(p.cp)
	if i < n && p.cp[i] == '"' {
		j := i + 1
		var v []rune
		for {
			if j >= n || p.cp[j] == '\n' || p.cp[j] == '\r' {
				return nil, nil, 0, errf(ESyntax, i, "unterminated string")
			}
			d := p.cp[j]
			if d == '"' {
				break
			}
			if d == '\\' {
				if j+1 < n && (p.cp[j+1] == '"' || p.cp[j+1] == '\\') {
					v = append(v, p.cp[j+1])
					j += 2
					continue
				}
				return nil, nil, 0, errf(ESyntax, j, `only \" and \\ are valid escapes`)
			}
			v = append(v, d)
			j++
		}
		return &Lit{Value: string(v), Pos: i}, nil, j + 1, nil
	}
	if i < n && (isDigit(p.cp[i]) || (p.cp[i] == '-' && i+1 < n && isDigit(p.cp[i+1]))) {
		j := i
		if p.cp[j] == '-' {
			j++
		}
		for j < n && isDigit(p.cp[j]) {
			j++
		}
		if j+1 < n && p.cp[j] == '.' && isDigit(p.cp[j+1]) {
			j++
			for j < n && isDigit(p.cp[j]) {
				j++
			}
		}
		f, _ := strconv.ParseFloat(string(p.cp[i:j]), 64)
		return &Lit{Value: f, Pos: i}, nil, j, nil
	}
	if i < n && isLetter(p.cp[i]) {
		j := i
		for j < n && isSegChar(p.cp[j]) {
			j++
		}
		first := string(p.cp[i:j])
		segs := []string{first}
		for j+1 < n && p.cp[j] == '.' && (isLetter(p.cp[j+1]) || isDigit(p.cp[j+1])) {
			s := j + 1
			j = s
			for j < n && isSegChar(p.cp[j]) {
				j++
			}
			segs = append(segs, string(p.cp[s:j]))
		}
		if len(segs) == 1 && (first == "true" || first == "false") {
			return &Lit{Value: first == "true", Pos: i}, nil, j, nil
		}
		if !contains(Roots, first) {
			return nil, nil, 0, errf(EUnknownPath, i, `unknown root "%s" (use %s)`, first, strings.Join(Roots, ", "))
		}
		if len(segs) == 1 {
			return nil, nil, 0, errf(ESyntax, j, `"%s" needs a "." and a name`, first)
		}
		return nil, &Path{Segments: segs, Pos: i}, j, nil
	}
	return nil, nil, 0, errf(ESyntax, i, "expected a path, a string, a number, or true / false")
}

func (p *parser) filter(i int) (FilterNode, int, *Error) {
	n := len(p.cp)
	j := i
	if !(j < n && p.cp[j] >= 'a' && p.cp[j] <= 'z') {
		return FilterNode{}, 0, errf(ESyntax, i, `expected a filter name after "|"`)
	}
	for j < n && ((p.cp[j] >= 'a' && p.cp[j] <= 'z') || isDigit(p.cp[j]) || p.cp[j] == '_') {
		j++
	}
	name := string(p.cp[i:j])
	if !contains(Filters, name) {
		return FilterNode{}, 0, errf(EUnknownFilter, i, `unknown filter "%s" (use %s)`, name, strings.Join(Filters, ", "))
	}
	node := FilterNode{Name: name, Pos: i}
	if j < n && p.cp[j] == '(' {
		j = p.skipWs(j + 1)
		if e := p.noNewline(j); e != nil {
			return FilterNode{}, 0, e
		}
		if p.at(j) != ')' || j >= n {
			for {
				lit, _, nx, err := p.primary(j)
				if err != nil {
					return FilterNode{}, 0, err
				}
				if lit == nil {
					return FilterNode{}, 0, errf(ESyntax, j, "filter arguments must be literals")
				}
				node.Args = append(node.Args, lit.Value)
				j = p.skipWs(nx)
				if e := p.noNewline(j); e != nil {
					return FilterNode{}, 0, e
				}
				if j < n && p.cp[j] == ',' {
					j = p.skipWs(j + 1)
					if e := p.noNewline(j); e != nil {
						return FilterNode{}, 0, e
					}
					continue
				}
				break
			}
		}
		if !(j < n && p.cp[j] == ')') {
			return FilterNode{}, 0, errf(ESyntax, j, `expected ")"`)
		}
		j++
	}
	if e := checkArity(node); e != nil {
		return FilterNode{}, 0, e
	}
	return node, j, nil
}

func checkArity(f FilterNode) *Error {
	n := len(f.Args)
	switch f.Name {
	case "default":
		if n != 1 {
			return errf(ESyntax, f.Pos, "default needs exactly 1 argument")
		}
		return nil
	case "join":
		if n > 1 {
			return errf(ESyntax, f.Pos, "join takes at most 1 argument")
		}
		if n == 1 {
			if _, ok := f.Args[0].(string); !ok {
				return errf(ESyntax, f.Pos, "join needs a string argument")
			}
		}
		return nil
	case "mosquitto_passwd":
		// mosquitto_passwd(format = 7, iterations = 101): 7 = sha512-pbkdf2 (Mosquitto 2.x), 6 = salted sha512 (1.x)
		if n > 2 {
			return errf(ESyntax, f.Pos, "mosquitto_passwd takes at most 2 arguments: (format 7 | 6, iterations)")
		}
		if n >= 1 && f.Args[0] != 7.0 && f.Args[0] != 6.0 {
			return errf(ESyntax, f.Pos, "mosquitto_passwd: the format must be 7 (Mosquitto 2.x, sha512-pbkdf2) or 6 (1.x, salted sha512)")
		}
		if n == 2 {
			if f.Args[0] == 6.0 {
				return errf(ESyntax, f.Pos, "mosquitto_passwd(6) has no iterations (salted sha512 runs once)")
			}
			it, ok := f.Args[1].(float64)
			if !ok || it != math.Trunc(it) || it < 1 || it > MosquittoMaxIterations {
				return errf(ESyntax, f.Pos, "mosquitto_passwd: iterations must be an integer 1–%d", MosquittoMaxIterations)
			}
		}
		return nil
	}
	if n != 0 {
		return errf(ESyntax, f.Pos, "%s takes no arguments", f.Name)
	}
	return nil
}

// ParseTemplate parses a value into segments.
func ParseTemplate(src string) ([]Segment, error) {
	p := &parser{cp: []rune(src)}
	segs, err := p.template()
	if err != nil {
		return nil, err
	}
	return segs, nil
}

// IsExpr reports whether s contains "{{".
func IsExpr(s string) bool { return strings.Contains(s, "{{") }

// ───────────────────────────── checks

func checkPathShape(p *Path) *Error {
	root, rest := p.Segments[0], p.Segments[1:]
	bad := errf(EUnknownPath, p.Pos, `unknown path "%s"`, strings.Join(p.Segments, "."))
	switch root {
	case "var", "secret":
		if len(rest) != 1 {
			return bad
		}
	case "file":
		if len(rest) != 2 || rest[1] != "path" {
			return bad
		}
	case "site":
		if len(rest) == 2 && rest[1] == "host" {
			return nil
		}
		if len(rest) == 4 && rest[1] == "port" {
			return nil
		}
		return bad
	case "self":
		if len(rest) != 1 || (rest[0] != "slug" && rest[0] != "host") {
			return bad
		}
	case "deployment":
		if len(rest) != 1 || rest[0] != "id" {
			return bad
		}
	case "container":
		if len(rest) == 2 && rest[0] == "env" {
			return nil
		}
		if len(rest) != 1 || rest[0] != "slug" {
			return bad
		}
	case "containers":
		if len(rest) == 2 && rest[1] == "host" {
			return nil
		}
		if len(rest) == 3 && rest[1] == "port" {
			return nil
		}
		if len(rest) == 3 && rest[1] == "env" { // §21 #99 (field devtool only, checkAllowed)
			return nil
		}
		return bad
	default:
		return bad
	}
	return nil
}

func checkAllowed(p *Path, f Field) *Error {
	root := p.Segments[0]
	if !contains(FieldRoots[f], root) {
		if f == FieldFile && (root == "container" || root == "file") {
			return errf(ENotAllowed, p.Pos, `"%s.*" is not allowed in %s: files are shared by all containers on the site — use a variable instead`, root, FieldLabel[f])
		}
		return errf(ENotAllowed, p.Pos, `"%s.*" is not allowed in %s`, root, FieldLabel[f])
	}
	// §21 #99: another container's env only in dev-tool presets (no cross-container cycles)
	if root == "containers" && len(p.Segments) > 2 && p.Segments[2] == "env" && !fieldIn(ContainersEnvFields, f) {
		return errf(ENotAllowed, p.Pos, `"containers.<container>.env.*" is only allowed in dev-tool presets, not in %s (it reads another container's env; use container.env.<NAME> for this container, or a variable)`, FieldLabel[f])
	}
	return nil
}

func fieldIn(list []Field, f Field) bool {
	for _, x := range list {
		if x == f {
			return true
		}
	}
	return false
}

func checkNumericShape(segs []Segment) *Error {
	blocks := 0
	extra := false
	for _, s := range segs {
		if s.IsBlock {
			blocks++
		} else if strings.TrimSpace(s.Text) != "" {
			extra = true
		}
	}
	if blocks != 1 || extra {
		return errf(EType, 0, "a number field must be ONE whole block, for example {{ var.api-port }}")
	}
	return nil
}

// CheckExpr validates without values (syntax, filters, allowed roots, path shapes, numeric form).
// Returns at most one error.
func CheckExpr(src string, f Field) []*Error {
	segs, err := ParseTemplate(src)
	if err != nil {
		return []*Error{err.(*Error)}
	}
	if IsNumeric(f) {
		if e := checkNumericShape(segs); e != nil {
			return []*Error{e}
		}
	}
	for _, s := range segs {
		if !s.IsBlock {
			continue
		}
		if s.Path != nil {
			if e := checkAllowed(s.Path, f); e != nil {
				return []*Error{e}
			}
			if e := checkPathShape(s.Path); e != nil {
				return []*Error{e}
			}
		}
		for _, fl := range s.Filters {
			if e := checkFilterAllowed(fl, f); e != nil {
				return []*Error{e}
			}
		}
	}
	return nil
}

// checkFilterAllowed: a password-hash filter outside SecretFields → E_NOT_ALLOWED at the filter name (§21 #103).
func checkFilterAllowed(fl FilterNode, f Field) *Error {
	if contains(PasswordFilters, fl.Name) && !IsSecretField(f) {
		return errf(ENotAllowed, fl.Pos, `filter "%s" is only allowed where secrets are (container env, file content, dev-tool presets), not in %s`, fl.Name, FieldLabel[f])
	}
	return nil
}

// ExprFilters returns the filter names a value uses (unique, in order of first use); nil when it does not parse.
func ExprFilters(src string) []string {
	segs, err := ParseTemplate(src)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, s := range segs {
		for _, fl := range s.Filters {
			if !contains(out, fl.Name) {
				out = append(out, fl.Name)
			}
		}
	}
	return out
}

// ExprRefs returns the paths used by a value.
func ExprRefs(src string) ([]string, error) {
	segs, err := ParseTemplate(src)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, s := range segs {
		if s.IsBlock && s.Path != nil {
			out = append(out, strings.Join(s.Path.Segments, "."))
		}
	}
	return out, nil
}

// ───────────────────────────── evaluator

// Hooks are the lazy lookups used by ResolveContainer (TS ResolveHooks): container.env.<NAME> and file.<slug>.path
// resolve other values of the same container on demand. A nil hook uses the plain Context data.
// A hook returns ok=false for an unknown name, or an error (*Error) for a failed / cyclic dependency.
type Hooks struct {
	Env  func(name string, p *Path) (*EnvRef, *Error)
	File func(slug string, p *Path) (*FileRef, *Error)
}

func lookup(p *Path, ctx *Context, f Field, hooks *Hooks) (any, bool, *Error) {
	if e := checkAllowed(p, f); e != nil {
		return nil, false, e
	}
	if e := checkPathShape(p); e != nil {
		return nil, false, e
	}
	root, rest := p.Segments[0], p.Segments[1:]
	bad := errf(EUnknownPath, p.Pos, `unknown path "%s"`, strings.Join(p.Segments, "."))
	switch root {
	case "var":
		v, ok := ctx.Var[rest[0]]
		if !ok {
			return nil, false, bad
		}
		return v, false, nil
	case "secret":
		v, ok := ctx.Secret[rest[0]]
		if !ok {
			return nil, false, bad
		}
		if v == nil {
			return nil, true, nil
		}
		return *v, true, nil
	case "file":
		var fr *FileRef
		if hooks != nil && hooks.File != nil {
			r, e := hooks.File(rest[0], p)
			if e != nil {
				return nil, false, e
			}
			fr = r
		} else if x, ok := ctx.File[rest[0]]; ok {
			fr = &x
		}
		if fr == nil {
			return nil, false, bad
		}
		if fr.Path == nil {
			return nil, false, errf(EFileNotMounted, p.Pos, `this container does not mount file "%s"`, rest[0])
		}
		return *fr.Path, false, nil
	case "site":
		st, ok := ctx.Site[rest[0]]
		if !ok {
			return nil, false, bad
		}
		if len(rest) == 2 {
			if st.Host == nil {
				return nil, false, errf(ENull, p.Pos, "site %s has host auto and has not reported its address yet (install the site, or set its host)", rest[0])
			}
			return *st.Host, false, nil
		}
		svc, ok := st.Port[rest[2]]
		if !ok {
			return nil, false, bad
		}
		hp, ok := svc[rest[3]]
		if !ok {
			return nil, false, bad
		}
		if hp == nil {
			return nil, false, errf(ENoHostPort, p.Pos, "site %s: %s.%s has hostPort null, so other sites cannot reach it", rest[0], rest[2], rest[3])
		}
		return *hp, false, nil
	case "self":
		if rest[0] == "slug" {
			return ctx.Self.Slug, false, nil
		}
		if ctx.Self.Host == nil {
			return nil, false, errf(ENull, p.Pos, "site %s has host auto and its address is not known yet", ctx.Self.Slug)
		}
		return *ctx.Self.Host, false, nil
	case "deployment":
		return ctx.Deployment.ID, false, nil
	case "containers":
		c, ok := ctx.Containers[rest[0]]
		if !ok {
			return nil, false, errf(EUnknownPath, p.Pos, `no container "%s" on this site`, rest[0])
		}
		if rest[1] == "host" {
			return c.Host, false, nil
		}
		if rest[1] == "env" {
			name := rest[2]
			e, ok := c.Env[name]
			if !ok {
				return nil, false, errf(EUnknownPath, p.Pos, `container %s has no env var "%s" (image env or container env)`, rest[0], name)
			}
			if e.Error != nil {
				return nil, false, errf(e.Error.Code, p.Pos, "containers.%s.env.%s: %s", rest[0], name, e.Error.Message)
			}
			if e.Secret && !IsSecretField(f) {
				return nil, false, errf(ENotAllowed, p.Pos, "env var %s of container %s holds a secret; secrets are not allowed in %s", name, rest[0], FieldLabel[f])
			}
			if e.Value == nil {
				return nil, e.Secret, nil
			}
			return *e.Value, e.Secret, nil
		}
		n, ok := c.Port[rest[2]]
		if !ok {
			names := make([]string, 0, len(c.Port))
			for k := range c.Port {
				names = append(names, k)
			}
			sort.Strings(names)
			list := strings.Join(names, ", ")
			if list == "" {
				list = "none"
			}
			return nil, false, errf(EUnknownPath, p.Pos, `container %s has no port "%s" (its image ports: %s)`, rest[0], rest[2], list)
		}
		if n == nil {
			return nil, false, errf(ENull, p.Pos, "port %s of container %s has no number (its image port does not resolve)", rest[2], rest[0])
		}
		return *n, false, nil
	case "container":
		if ctx.Container == nil {
			return nil, false, bad
		}
		if rest[0] == "slug" {
			return ctx.Container.Slug, false, nil
		}
		name := rest[1]
		var ev *EnvRef
		if hooks != nil && hooks.Env != nil {
			r, e := hooks.Env(name, p)
			if e != nil {
				return nil, false, e
			}
			ev = r
		} else if x, ok := ctx.Container.Env[name]; ok {
			ev = &x
		}
		if ev == nil {
			return nil, false, errf(EUnknownPath, p.Pos, `this container has no env var "%s" (image env or container env)`, name)
		}
		if ev.Secret && !IsSecretField(f) {
			return nil, false, errf(ENotAllowed, p.Pos, "env var %s holds a secret; secrets are not allowed in %s", name, FieldLabel[f])
		}
		if ev.Value == nil {
			return nil, ev.Secret, nil
		}
		return *ev.Value, ev.Secret, nil
	}
	return nil, false, bad
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	}
	return fmt.Sprintf("%T", v)
}

func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		s, err := canonical.FormatNumber(x)
		if err != nil {
			return "", false
		}
		return s, true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

func isStrNum(v any) bool {
	switch v.(type) {
	case string, float64:
		return true
	}
	return false
}

var intRe = regexp.MustCompile(`^\s*-?\d+(\.\d+)?\s*$`)
var numRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// URLEncode keeps A-Z a-z 0-9 - _ . ~ and percent-encodes every other UTF-8 byte (upper-case hex).
func URLEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func trunc(f float64) float64 {
	t := math.Trunc(f)
	if t == 0 {
		return 0 // -0 → 0
	}
	return t
}

func applyFilter(f FilterNode, v any, ctx *Context) (any, *Error) {
	typeErr := func() *Error { return errf(EType, f.Pos, `filter "%s" cannot take %s`, f.Name, typeName(v)) }
	switch f.Name {
	case "default":
		if v == nil || v == "" {
			return f.Args[0], nil
		}
		return v, nil
	case "join":
		list, ok := v.([]any)
		if !ok {
			return nil, typeErr()
		}
		sep := ","
		if len(f.Args) == 1 {
			sep = f.Args[0].(string)
		}
		parts := make([]string, 0, len(list))
		for _, x := range list {
			s, ok := scalarText(x)
			if !ok {
				return nil, errf(EType, f.Pos, `filter "join" cannot join %s items`, typeName(x))
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, sep), nil
	case "upper", "lower", "base64", "url":
		if !isStrNum(v) {
			return nil, typeErr()
		}
		s, _ := scalarText(v)
		switch f.Name {
		case "upper":
			return strings.ToUpper(s), nil
		case "lower":
			return strings.ToLower(s), nil
		case "base64":
			return base64.StdEncoding.EncodeToString([]byte(s)), nil
		default:
			return URLEncode(s), nil
		}
	case "trim":
		s, ok := v.(string)
		if !ok {
			return nil, typeErr()
		}
		return strings.TrimSpace(s), nil
	case "int":
		switch x := v.(type) {
		case float64:
			return trunc(x), nil
		case string:
			if !intRe.MatchString(x) {
				return nil, errf(EType, f.Pos, `"%s" is not an integer`, x)
			}
			n, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
			return trunc(n), nil
		}
		return nil, typeErr()
	case "json":
		s, err := canonical.Canonicalize(v)
		if err != nil {
			return nil, errf(EType, f.Pos, "%v", err)
		}
		return s, nil
	case "sha256", "sha512":
		if !isStrNum(v) {
			return nil, typeErr()
		}
		s, _ := scalarText(v)
		if f.Name == "sha256" {
			h := sha256.Sum256([]byte(s))
			return hex.EncodeToString(h[:]), nil
		}
		h := sha512.Sum512([]byte(s))
		return hex.EncodeToString(h[:]), nil
	case "mosquitto_passwd":
		if !isStrNum(v) {
			return nil, typeErr()
		}
		pw, _ := scalarText(v)
		if pw == "" {
			return nil, errf(EType, f.Pos, `filter "mosquitto_passwd" cannot hash an empty password`)
		}
		if strings.ContainsRune(pw, 0) {
			return nil, errf(EType, f.Pos, `filter "mosquitto_passwd" cannot hash a password with a NUL character`)
		}
		format, iterations := 7, MosquittoDefaultIterations
		if len(f.Args) >= 1 {
			format = int(f.Args[0].(float64))
		}
		if len(f.Args) == 2 {
			iterations = int(f.Args[1].(float64))
		}
		return MosquittoHash(pw, MosquittoSalt(pw, ctx.Deployment.ID, ctx.Self.Slug), format, iterations), nil
	}
	return nil, errf(EUnknownFilter, f.Pos, `unknown filter "%s"`, f.Name)
}

func toText(v any, pos int) (string, *Error) {
	switch v.(type) {
	case nil:
		return "", errf(ENull, pos, "the value is null; add | default(…)")
	case []any, map[string]any:
		return "", errf(EType, pos, "a list or an object needs | join(…) or | json")
	}
	s, ok := scalarText(v)
	if !ok {
		return "", errf(EType, pos, "cannot use %s as text", typeName(v))
	}
	return s, nil
}

// Resolve resolves one value for one field. The error, when non-nil, is a *Error.
func Resolve(src string, ctx *Context, f Field) (*Result, error) {
	return ResolveWithHooks(src, ctx, f, nil)
}

// ResolveWithHooks is Resolve with lazy lookups (TS resolve(src, ctx, field, hooks)).
func ResolveWithHooks(src string, ctx *Context, f Field, hooks *Hooks) (*Result, error) {
	segs, err := ParseTemplate(src)
	if err != nil {
		return nil, err
	}
	numeric := IsNumeric(f)
	if numeric {
		if e := checkNumericShape(segs); e != nil {
			return nil, e
		}
	}
	var out, masked strings.Builder
	secret := false
	deferred := false
	clearSecret := false // §21 #103: a secret reached the result without a password hash
	refs := []string{}
	for _, s := range segs {
		if !s.IsBlock {
			if !numeric {
				out.WriteString(s.Text)
				masked.WriteString(s.Text)
			}
			continue
		}
		var v any
		isSecret := false
		blockDeferred := false
		if s.Lit != nil {
			v = s.Lit.Value
		} else {
			val, sec, e := lookup(s.Path, ctx, f, hooks)
			if e != nil {
				return nil, e
			}
			v, isSecret = val, sec
			blockDeferred = IsDeferredValue(v)
			refs = append(refs, strings.Join(s.Path.Segments, "."))
		}
		deferErr := func(e *Error) error {
			if blockDeferred {
				return errf(EDeferred, e.Pos, "%s is set at deployment", refs[len(refs)-1])
			}
			return e
		}
		hashed := false
		for _, fl := range s.Filters { // §21 #103 (never deferred away)
			if e := checkFilterAllowed(fl, f); e != nil {
				return nil, e
			}
			if contains(HashingFilters, fl.Name) {
				hashed = true
			}
		}
		if isSecret && !hashed {
			clearSecret = true
		}
		for _, fl := range s.Filters {
			nv, e := applyFilter(fl, v, ctx)
			if e != nil {
				return nil, deferErr(e)
			}
			v = nv
		}
		txt, e := toText(v, s.Start)
		if e != nil {
			return nil, deferErr(e)
		}
		if blockDeferred && !IsDeferredValue(txt) {
			txt = DeployPlaceholder // e.g. | length, | int of the stand-in
		}
		if blockDeferred {
			deferred = true
		}
		out.WriteString(txt)
		if isSecret {
			masked.WriteString("••••")
			secret = true
		} else {
			masked.WriteString(txt)
		}
	}
	if numeric && deferred {
		return nil, errf(EDeferred, 0, "the number is set at deployment")
	}
	if numeric {
		raw := out.String()
		t := strings.TrimSpace(raw)
		n := math.NaN()
		if numRe.MatchString(t) {
			n, _ = strconv.ParseFloat(t, 64)
		}
		if math.IsNaN(n) || n != math.Trunc(n) || n < 1 || n > 65535 {
			return nil, errf(EType, 0, `"%s" is not an integer 1–65535`, raw)
		}
		iv := int(n)
		txt := strconv.Itoa(iv)
		m := txt
		if secret {
			m = "••••"
		}
		return &Result{Value: txt, Number: &iv, Masked: m, Secret: secret, Refs: refs}, nil
	}
	return &Result{Value: out.String(), Masked: masked.String(), Secret: secret, Refs: refs, Deferred: deferred, SecretHashed: secret && !clearSecret}, nil
}

// ResolveNumberField resolves Container.port[].hostPort / ImagePort.port values: nil → nil; float64/int → checked;
// string → must be one whole block.
func ResolveNumberField(v any, ctx *Context, f Field) (*int, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int:
		return ResolveNumberField(float64(x), ctx, f)
	case float64:
		if x != math.Trunc(x) || x < 1 || x > 65535 {
			return nil, errf(EType, 0, "%v is not an integer 1–65535", x)
		}
		i := int(x)
		return &i, nil
	case string:
		if !IsExpr(x) {
			return nil, errf(EType, 0, "a number field must be a number or ONE whole block, for example {{ var.api-port }}")
		}
		r, err := Resolve(x, ctx, f)
		if err != nil {
			return nil, err
		}
		return r.Number, nil
	}
	return nil, errf(EType, 0, "a number field must be a number or a string")
}
