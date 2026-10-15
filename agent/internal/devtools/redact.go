package devtools

// Go ports of redactStatement / redactHttpRequest / truncateUtf8 / devToolConfirmOk / isOpmPlaceholder /
// applyEnvironment (shared/src/devtools.ts) + the agent-side masking of known secret values.

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Redacted is TS REDACTED.
const Redacted = "••••••"

// StatementLogMax is TS STATEMENT_LOG_MAX.
const StatementLogMax = 4096

// SensitiveHeaders is TS SENSITIVE_HEADERS.
var SensitiveHeaders = []string{"authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "x-auth-token", "x-access-token"}

// SecretNameRe is TS SECRET_NAME_RE.
var SecretNameRe = regexp.MustCompile(`(?i)(pass(word|wd)?|secret|token|api[-_]?key|apikey|auth|credential|private|session|cookie|signature|sig|access[-_]?key|client[-_]?secret|dsn)`)

var (
	reSQLPassword   = regexp.MustCompile(`(?i)(\bPASSWORD\s*=?\s*)('(?:[^']|'')*'|"(?:[^"]|"")*"|\$[A-Za-z_]*\$[\s\S]*?\$[A-Za-z_]*\$)`)
	reSQLIdentified = regexp.MustCompile(`(?i)(\bIDENTIFIED\s+BY\s+)('(?:[^']|'')*'|"(?:[^"]|"")*")`)
	reSQLConnPw     = regexp.MustCompile(`(?i)(\bpassword\s*=\s*)([^\s'";]+)`)
	reRedisPwParam  = regexp.MustCompile(`(?i)pass|auth|secret|key`)
	reRedisNeedsQ   = regexp.MustCompile(`[\s"']`)
	reURLUserinfo   = regexp.MustCompile(`^(\w+://)([^/@\s]*):([^/@\s]*)@`)
	reAuthScheme    = regexp.MustCompile(`(?i)^(basic|bearer|digest|token|apikey)$`)
	rePlaceholder   = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
)

// Truncated is TS { text, truncated }.
type Truncated struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// TruncateUTF8 is TS truncateUtf8.
func TruncateUTF8(s string, max int) Truncated {
	if len(s) <= max {
		return Truncated{Text: s}
	}
	cut := max - 3
	for cut > 0 && s[cut]&0xc0 == 0x80 {
		cut--
	}
	if cut < 0 {
		cut = 0
	}
	return Truncated{Text: strings.ToValidUTF8(s[:cut], "�") + "…", Truncated: true}
}

// jsonStringify mimics JSON.stringify of a string.
func jsonStringify(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// redactStatementRaw is redactStatement without the truncation.
func redactStatementRaw(kind, text string) string {
	t := text
	switch kind {
	case "postgres", "sqlite", "cassandra":
		t = reSQLPassword.ReplaceAllString(t, "${1}'"+Redacted+"'")
		t = reSQLIdentified.ReplaceAllString(t, "${1}'"+Redacted+"'")
		t = reSQLConnPw.ReplaceAllString(t, "${1}"+Redacted)
	case "redis":
		argv, ok := ParseRedisCommandLine(t)
		if ok && len(argv) > 0 {
			t = strings.Join(quoteRedisArgv(RedactRedisArgv(argv)), " ")
		}
	}
	return t
}

func quoteRedisArgv(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || reRedisNeedsQ.MatchString(a) {
			out[i] = jsonStringify(a)
		} else {
			out[i] = a
		}
	}
	return out
}

// RedactRedisArgv masks credentials in a Redis argv (AUTH, HELLO … AUTH, CONFIG SET requirepass …, ACL SETUSER >pw,
// MIGRATE … AUTH / AUTH2). The input is not changed.
func RedactRedisArgv(in []string) []string {
	argv := append([]string(nil), in...)
	up := make([]string, len(argv))
	for i, a := range argv {
		up[i] = strings.ToUpper(a)
	}
	red := func(i int) {
		if i < len(argv) {
			argv[i] = Redacted
		}
	}
	indexOf := func(s string) int {
		for i, x := range up {
			if x == s {
				return i
			}
		}
		return -1
	}
	if up[0] == "AUTH" {
		for i := 1; i < len(argv); i++ {
			red(i)
		}
	}
	if up[0] == "HELLO" {
		if k := indexOf("AUTH"); k > 0 {
			red(k + 2)
		}
	}
	if up[0] == "CONFIG" && len(up) > 1 && up[1] == "SET" {
		for i := 2; i+1 < len(argv); i += 2 {
			if reRedisPwParam.MatchString(argv[i]) {
				red(i + 1)
			}
		}
	}
	if up[0] == "ACL" && len(up) > 1 && up[1] == "SETUSER" {
		for i := 3; i < len(argv); i++ {
			if a := argv[i]; a != "" && strings.ContainsRune(">#<!", rune(a[0])) {
				argv[i] = a[:1] + Redacted
			}
		}
	}
	if up[0] == "MIGRATE" {
		if k := indexOf("AUTH"); k > 0 {
			red(k + 1)
		}
		if k := indexOf("AUTH2"); k > 0 {
			red(k + 2)
		}
	}
	return argv
}

// RedactStatement is TS redactStatement.
func RedactStatement(kind, text string) Truncated {
	return TruncateUTF8(redactStatementRaw(kind, text), StatementLogMax)
}

// redactHTTPRaw is redactHttpRequest without the truncation.
func redactHTTPRaw(method, u string, headers [][2]string, bodyBytes int64) string {
	if q := strings.IndexByte(u, '?'); q >= 0 {
		base := u[:q]
		parts := strings.Split(u[q+1:], "&")
		for i, kv := range parts {
			e := strings.IndexByte(kv, '=')
			k := kv
			if e >= 0 {
				k = kv[:e]
			}
			name := k
			if d, err := url.PathUnescape(strings.ReplaceAll(k, "+", " ")); err == nil && utf8.ValidString(d) {
				name = d
			}
			if e >= 0 && SecretNameRe.MatchString(name) {
				parts[i] = k + "=" + Redacted
			}
		}
		u = base + "?" + strings.Join(parts, "&")
	}
	u = reURLUserinfo.ReplaceAllString(u, "${1}${2}:"+Redacted+"@")
	lines := []string{strings.ToUpper(method) + " " + u}
	for _, h := range headers {
		k, v := h[0], h[1]
		if !(inList(SensitiveHeaders, strings.ToLower(k)) || SecretNameRe.MatchString(k)) {
			lines = append(lines, k+": "+v)
			continue
		}
		sp := strings.IndexByte(v, ' ')
		if sp > 0 && reAuthScheme.MatchString(v[:sp]) {
			lines = append(lines, k+": "+v[:sp]+" "+Redacted)
		} else {
			lines = append(lines, k+": "+Redacted)
		}
	}
	if bodyBytes > 0 {
		lines = append(lines, fmt.Sprintf("(body %d bytes)", bodyBytes))
	}
	return strings.Join(lines, "\n")
}

// RedactHTTPRequest is TS redactHttpRequest.
func RedactHTTPRequest(method, u string, headers [][2]string, bodyBytes int64) Truncated {
	return TruncateUTF8(redactHTTPRaw(method, u, headers, bodyBytes), StatementLogMax)
}

// Masker replaces known secret values (≥ 4 bytes; longest first) with REDACTED.
type Masker struct{ values []string }

// NewMasker builds a masker from secret values (shorter than 4 bytes are ignored).
func NewMasker(values ...[]string) *Masker {
	seen := map[string]bool{}
	m := &Masker{}
	for _, vs := range values {
		for _, v := range vs {
			if len(v) >= 4 && !seen[v] {
				seen[v] = true
				m.values = append(m.values, v)
			}
		}
	}
	sort.Slice(m.values, func(i, j int) bool { return len(m.values[i]) > len(m.values[j]) })
	return m
}

// Add adds values.
func (m *Masker) Add(vs ...string) *Masker {
	return NewMasker(m.values, vs)
}

// Mask masks every known value in s.
func (m *Masker) Mask(s string) string {
	if m == nil {
		return s
	}
	for _, v := range m.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Redacted)
		}
	}
	return s
}

// LogStatement is the activity text of a statement: redacted, known secrets masked, ≤ 4 KiB.
func LogStatement(kind, text string, m *Masker) Truncated {
	return TruncateUTF8(m.Mask(redactStatementRaw(kind, text)), StatementLogMax)
}

// LogError masks an error text and cuts it at 500 characters.
func LogError(err string, m *Masker) string {
	s := m.Mask(err)
	if utf8.RuneCountInString(s) > 500 {
		r := []rune(s)
		s = string(r[:499]) + "…"
	}
	return s
}

// ConfirmOk is TS devToolConfirmOk.
func ConfirmOk(connectionName, typed string) bool {
	n := jsTrim(connectionName)
	return n != "" && jsTrim(typed) == n
}

// OpmExprRoots is TS OPM_EXPR_ROOTS.
var OpmExprRoots = []string{"var", "secret", "site", "self", "deployment", "containers", "container", "file"}

// IsOpmPlaceholder is TS isOpmPlaceholder.
func IsOpmPlaceholder(inner string) bool {
	t := jsTrim(inner)
	for _, r := range OpmExprRoots {
		if t == r || strings.HasPrefix(t, r+".") || strings.HasPrefix(t, r+"[") {
			return true
		}
	}
	return strings.Contains(t, "|")
}

// ApplyEnvironment is TS applyEnvironment.
func ApplyEnvironment(text string, vars map[string]string) string {
	return rePlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		inner := rePlaceholder.FindStringSubmatch(m)[1]
		if IsOpmPlaceholder(inner) {
			return m
		}
		if v, ok := vars[jsTrim(inner)]; ok {
			return v
		}
		return m
	})
}
