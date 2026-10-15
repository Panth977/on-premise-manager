// Package edgeres is the Go port of shared/src/edge-resolve.ts (round E, nginx location semantics): how the edge treats
// one request ("Test a URL"), and the decision code the Caddy modules run at request time (SelectRoute, Walk,
// UpstreamPathQuery, RedirectLocation, ExpandEdgeVars). The edge given must already have its expressions resolved.
// Shared vectors: shared/testdata/edge-vectors.json (TestVectors).
package edgeres

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"opm/agent/internal/model"
)

// Defaults mirror TS EDGE_DEFAULTS.
const (
	MaxRewrites        = 10
	RequestBufferBytes = 8 * 1024 * 1024
	MaxBodyLimit       = int64(1024 * 1024 * 1024 * 1024)
	MaxTimeoutSec      = 86400
	MaxConnectTimeout  = 3600
)

// AddHeaderStatuses: statuses nginx add_header (without "always") applies to.
var AddHeaderStatuses = []int{200, 201, 204, 206, 301, 302, 303, 304, 307, 308}

// HopByHopHeaders are managed by Caddy itself; setting them is ignored (V_EDGE_HOP).
var HopByHopHeaders = []string{"Connection", "Upgrade", "Keep-Alive", "Transfer-Encoding", "TE", "Trailer", "Proxy-Connection"}

// IsHopByHop reports a hop-by-hop header name (case-insensitive).
func IsHopByHop(name string) bool {
	for _, h := range HopByHopHeaders {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// ───────────────────────── regular expressions (RE2 ∩ JS)

var reCache sync.Map // string → *regexp.Regexp (nil = invalid)

// compileRaw compiles a route / rewrite regex (Go understands a leading "(?i)" itself). nil on error. Cached.
func compileRaw(src string) *regexp.Regexp {
	if v, ok := reCache.Load(src); ok {
		re, _ := v.(*regexp.Regexp)
		return re
	}
	re, err := regexp.Compile(src)
	if err != nil {
		reCache.Store(src, (*regexp.Regexp)(nil))
		return nil
	}
	reCache.Store(src, re)
	return re
}

// CheckEdgeRegex is TS checkEdgeRegex: "" when the pattern is valid in RE2 and JS with the same meaning, else a message.
func CheckEdgeRegex(src string) string {
	if src == "" {
		return "the regex is empty"
	}
	if utf16Len(src) > 1024 {
		return "the regex is longer than 1024 characters"
	}
	body := strings.TrimPrefix(src, "(?i)")
	inClass := false
	at := func(i int) byte {
		if i < len(body) {
			return body[i]
		}
		return 0
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '\\' {
			n := at(i + 1)
			switch {
			case n >= '1' && n <= '9':
				return fmt.Sprintf(`backreferences (\%c) are not supported (RE2 has none)`, n)
			case n == 'k':
				return `named backreferences (\k<…>) are not supported (RE2 has none)`
			case n != 0 && strings.IndexByte("AzZQEGhHRXCKNVoe", n) >= 0:
				return fmt.Sprintf(`\%c is not supported by both RE2 and JavaScript: write it another way (e.g. ^ $ instead of \A \z)`, n)
			case n == 'p' || n == 'P':
				return `\p{…} Unicode classes are not supported here (JavaScript needs Unicode mode)`
			case n == 'x' && at(i+2) == '{':
				return `\x{…} is not supported by JavaScript; use \xHH or the character itself`
			case n == 'u':
				return `\uHHHH is not supported by RE2; use the character itself`
			case n == 'c':
				return `\c control escapes are not supported by RE2`
			case n == '0':
				return `\0 is not supported by RE2; use \x00`
			}
			i++
			continue
		}
		if inClass {
			if c == ']' {
				inClass = false
			}
			continue
		}
		if c == '[' {
			if strings.HasPrefix(body[i:], "[[:") || strings.HasPrefix(body[i:], "[^[:") || strings.HasPrefix(body[i+1:], "[:") {
				return "POSIX classes like [[:alpha:]] are not supported by JavaScript; use [a-zA-Z]"
			}
			inClass = true
			if at(i+1) == '^' {
				i++
			}
			if at(i+1) == ']' {
				i++
			}
			continue
		}
		if c == '(' && at(i+1) == '?' {
			rest := body[i+2:]
			switch {
			case strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, "!"):
				return "lookahead (?= / (?! is not supported (RE2 has no lookaround)"
			case strings.HasPrefix(rest, "<=") || strings.HasPrefix(rest, "<!"):
				return "lookbehind (?<= / (?<! is not supported (RE2 has no lookaround)"
			case strings.HasPrefix(rest, ">"):
				return "atomic groups (?> are not supported (RE2)"
			case strings.HasPrefix(rest, "P<"):
				return "write named groups as (?<name>…) (JavaScript does not know (?P<name>…))"
			case strings.HasPrefix(rest, "<") || strings.HasPrefix(rest, ":"):
				continue
			}
			return "inline flags are not supported except a leading (?i) for the whole pattern"
		}
		if (c == '+' || c == '*' || c == '?' || c == '}') && at(i+1) == '+' {
			return "possessive quantifiers (++ *+ ?+) are not supported (RE2)"
		}
	}
	if _, err := regexp.Compile(src); err != nil {
		return "not a valid regex: " + strings.TrimPrefix(err.Error(), "error parsing regexp: ")
	}
	return ""
}

// ExecRegex is TS execRegex: the match + groups (missing groups = ""), nil when no match / invalid.
func ExecRegex(src, s string) []string {
	re := compileRaw(src)
	if re == nil {
		return nil
	}
	return re.FindStringSubmatch(s)
}

// ───────────────────────── variables (SPEC §12.6)

// Vars is TS EdgeVars. RemoteAddr / ServerPort / Headers nil = unknown (Test a URL).
type Vars struct {
	URI        string // current path (decoded; after internal rewrites)
	Args       string // current query without "?"
	RequestURI string // original path + query (not decoded)
	Host       string // request host without port
	Scheme     string
	Captures   []string
	RemoteAddr *string
	ServerPort *string
	Headers    map[string]string // lower-case names
}

var varRe = regexp.MustCompile(`\$([1-9])|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)|\{(path|query|host)\}`)

// VarToken is one piece of a template: literal text or a variable.
type VarToken struct {
	Literal string
	// Var: "1"…"9" (captures), "path" / "query" / "host" (placeholders), or the $name (uri, args, http_x_y, …).
	Var         string
	Placeholder bool // {path} {query} {host}
	Raw         string
}

// Tokenize splits a template like expandEdgeVars does (used for the Caddy placeholder mapping).
func Tokenize(t string) []VarToken {
	var out []VarToken
	last := 0
	for _, m := range varRe.FindAllStringSubmatchIndex(t, -1) {
		if m[0] > last {
			out = append(out, VarToken{Literal: t[last:m[0]], Raw: t[last:m[0]]})
		}
		raw := t[m[0]:m[1]]
		switch {
		case m[2] >= 0:
			out = append(out, VarToken{Var: t[m[2]:m[3]], Raw: raw})
		case m[4] >= 0:
			out = append(out, VarToken{Var: t[m[4]:m[5]], Raw: raw})
		case m[6] >= 0:
			out = append(out, VarToken{Var: t[m[6]:m[7]], Raw: raw})
		default:
			out = append(out, VarToken{Var: t[m[8]:m[9]], Placeholder: true, Raw: raw})
		}
		last = m[1]
	}
	if last < len(t) {
		out = append(out, VarToken{Literal: t[last:], Raw: t[last:]})
	}
	return out
}

// IsKnownVar reports a $name the edge expands (besides $1…$9).
func IsKnownVar(name string) bool {
	switch name {
	case "uri", "args", "query_string", "is_args", "request_uri", "host", "scheme", "remote_addr", "server_port":
		return true
	}
	return strings.HasPrefix(name, "http_") && len(name) > 5
}

func (v *Vars) named(name string) (string, bool) {
	switch name {
	case "uri":
		return v.URI, true
	case "args", "query_string":
		return v.Args, true
	case "is_args":
		if v.Args != "" {
			return "?", true
		}
		return "", true
	case "request_uri":
		return v.RequestURI, true
	case "host":
		return v.Host, true
	case "scheme":
		return v.Scheme, true
	case "remote_addr":
		if v.RemoteAddr == nil {
			return "{client-ip}", true
		}
		return *v.RemoteAddr, true
	case "server_port":
		if v.ServerPort == nil {
			return "", true
		}
		return *v.ServerPort, true
	}
	if strings.HasPrefix(name, "http_") && len(name) > 5 {
		h := strings.ToLower(strings.ReplaceAll(name[5:], "_", "-"))
		if x, ok := v.Headers[h]; ok {
			return x, true
		}
		if v.Headers == nil {
			return "{header " + h + "}", true
		}
		return "", true
	}
	return "", false
}

// ExpandEdgeVars is TS expandEdgeVars.
func ExpandEdgeVars(t string, v Vars) string {
	var b strings.Builder
	for _, tk := range Tokenize(t) {
		switch {
		case tk.Var == "":
			b.WriteString(tk.Literal)
		case tk.Placeholder:
			switch tk.Var {
			case "path":
				b.WriteString(v.URI)
			case "query":
				if v.Args != "" {
					b.WriteString("?" + v.Args)
				}
			case "host":
				b.WriteString(v.Host)
			}
		case len(tk.Var) == 1 && tk.Var[0] >= '1' && tk.Var[0] <= '9':
			n := int(tk.Var[0] - '0')
			if n < len(v.Captures) {
				b.WriteString(v.Captures[n])
			}
		default:
			if r, ok := v.named(tk.Var); ok {
				b.WriteString(r)
			} else {
				b.WriteString(tk.Raw)
			}
		}
	}
	return b.String()
}

// ───────────────────────── matching

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// PrefixHit: plain string prefix (nginx) — "/api" matches "/api", "/api/x" AND "/apix"; "/api/" does not match "/api".
func PrefixHit(prefix, path string) bool { return strings.HasPrefix(path, prefix) }

// MatchHost selects exact (case-insensitive) > wildcard "*.x.y" (one extra label) > "*". how: exact | wildcard | any.
func MatchHost(edge *model.Edge, hostname string) (*model.EdgeHost, string) {
	hn := strings.ToLower(hostname)
	for i := range edge.Hosts {
		if strings.ToLower(edge.Hosts[i].Hostname) == hn {
			return &edge.Hosts[i], "exact"
		}
	}
	for i := range edge.Hosts {
		n := strings.ToLower(edge.Hosts[i].Hostname)
		if !strings.HasPrefix(n, "*.") {
			continue
		}
		suffix := n[1:]
		if strings.HasSuffix(hn, suffix) && len(strings.Split(hn, ".")) == len(strings.Split(n, ".")) && len(hn) > len(suffix) {
			return &edge.Hosts[i], "wildcard"
		}
	}
	for i := range edge.Hosts {
		if edge.Hosts[i].Hostname == "*" {
			return &edge.Hosts[i], "any"
		}
	}
	return nil, ""
}

// MethodHit reports whether the route matches the method (null / [] = all).
func MethodHit(r *model.EdgeRoute, method string) bool {
	if len(r.Match.Methods) == 0 {
		return true
	}
	for _, m := range r.Match.Methods {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}

func kindOf(r *model.EdgeRoute) string {
	if r.Match.Kind == "" {
		return "prefix"
	}
	return r.Match.Kind
}

func byOrder(a, b *model.EdgeRoute) int {
	if a.Order != b.Order {
		return a.Order - b.Order
	}
	return strings.Compare(a.RouteID, b.RouteID)
}

func byLen(a, b *model.EdgeRoute) int {
	if d := utf16Len(b.Match.Path) - utf16Len(a.Match.Path); d != 0 {
		return d
	}
	return byOrder(a, b)
}

// HostRoutes are the routes of a host (route.hosts [] = all) in the given (edge) order.
func HostRoutes(edge *model.Edge, host *model.EdgeHost) []model.EdgeRoute {
	out := []model.EdgeRoute{}
	for _, r := range edge.Routes {
		ok := len(r.Hosts) == 0
		for _, x := range r.Hosts {
			if strings.EqualFold(x, host.Hostname) {
				ok = true
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

// RoutesForHost is TS routesForHost: the routes of a host in effective order.
func RoutesForHost(edge *model.Edge, host *model.EdgeHost) []model.EdgeRoute {
	rs := HostRoutes(edge, host)
	out := []model.EdgeRoute{}
	for _, o := range EffectiveRouteOrder(rs) {
		for _, r := range rs {
			if r.RouteID == o.RouteID {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// OrderEntry is TS EdgeOrderEntry.
type OrderEntry struct {
	RouteID string `json:"routeId"`
	Group   string `json:"group"` // exact | prefix-stop | regex | prefix
	Rank    int    `json:"rank"`
	Why     string `json:"why"`
}

func sortRoutes(rs []*model.EdgeRoute, cmp func(a, b *model.EdgeRoute) int) {
	sort.SliceStable(rs, func(i, j int) bool { return cmp(rs[i], rs[j]) < 0 })
}

// EffectiveRouteOrder is TS effectiveRouteOrder: exact, prefix-stop (longest first), regex (by order), prefix (longest first).
func EffectiveRouteOrder(routes []model.EdgeRoute) []OrderEntry {
	var exact, stop, rx, prefix []*model.EdgeRoute
	for i := range routes {
		r := &routes[i]
		switch k := kindOf(r); {
		case k == "exact":
			exact = append(exact, r)
		case k == "prefix" && r.Match.StopRegex:
			stop = append(stop, r)
		case k == "regex":
			rx = append(rx, r)
		case k == "prefix":
			prefix = append(prefix, r)
		}
	}
	sortRoutes(exact, func(a, b *model.EdgeRoute) int {
		if a.Match.Path != b.Match.Path {
			return strings.Compare(a.Match.Path, b.Match.Path)
		}
		return byOrder(a, b)
	})
	sortRoutes(stop, byLen)
	sortRoutes(rx, byOrder)
	sortRoutes(prefix, byLen)
	out := []OrderEntry{}
	push := func(r *model.EdgeRoute, g, why string) {
		out = append(out, OrderEntry{RouteID: r.RouteID, Group: g, Rank: len(out) + 1, Why: why})
	}
	for _, r := range exact {
		push(r, "exact", fmt.Sprintf("Exact match: wins whenever the path is exactly %s (checked first).", r.Match.Path))
	}
	for _, r := range stop {
		push(r, "prefix-stop", fmt.Sprintf(`Prefix %s with "stop regex" (nginx ^~): when it is the LONGEST matching prefix, regex routes are skipped and it wins.`, r.Match.Path))
	}
	for i, r := range rx {
		push(r, "regex", fmt.Sprintf(`Regex #%d (order %d): checked after the longest prefix is found; the first regex that matches wins (unless that prefix is "stop regex").`, i+1, r.Order))
	}
	for _, r := range prefix {
		c := ""
		if r.Match.Path == "/" {
			c = " (catch-all)"
		}
		push(r, "prefix", fmt.Sprintf("Prefix %s: the LONGEST matching prefix wins when no regex matches%s.", r.Match.Path, c))
	}
	return out
}

// Selection is TS RouteSelection.
type Selection struct {
	Route        *model.EdgeRoute
	Captures     []string
	Why          string
	AutoRedirect bool
}

// SelectRoute is TS selectRoute (nginx location selection over routes already filtered to the host).
func SelectRoute(routes []model.EdgeRoute, path, method string) *Selection {
	if method == "" {
		method = "GET"
	}
	var cand []*model.EdgeRoute
	for i := range routes {
		if MethodHit(&routes[i], method) {
			cand = append(cand, &routes[i])
		}
	}
	var exact, prefixes, rx, auto []*model.EdgeRoute
	for _, r := range cand {
		k := kindOf(r)
		if k == "exact" && r.Match.Path == path {
			exact = append(exact, r)
		}
		if k == "prefix" && PrefixHit(r.Match.Path, path) {
			prefixes = append(prefixes, r)
		}
		if k == "regex" {
			rx = append(rx, r)
		}
		if k == "prefix" && r.Action.Type == "proxy" && len(r.Match.Path) > 1 && r.Match.Path == path+"/" {
			auto = append(auto, r)
		}
	}
	if len(exact) > 0 {
		sortRoutes(exact, byOrder)
		return &Selection{Route: exact[0], Captures: []string{}, Why: "exact match " + exact[0].Match.Path}
	}
	sortRoutes(prefixes, byLen)
	var longest *model.EdgeRoute
	if len(prefixes) > 0 {
		longest = prefixes[0]
	}
	if longest == nil || utf16Len(longest.Match.Path) < utf16Len(path) {
		if len(auto) > 0 {
			sortRoutes(auto, byOrder)
			a := auto[0]
			return &Selection{Route: a, Captures: []string{}, AutoRedirect: true, Why: fmt.Sprintf("nginx auto-redirect: %s → %s/ (proxy prefix %s)", path, path, a.Match.Path)}
		}
	}
	if longest != nil && longest.Match.StopRegex {
		return &Selection{Route: longest, Captures: []string{}, Why: fmt.Sprintf(`longest prefix %s has "stop regex" (^~): regex routes skipped`, longest.Match.Path)}
	}
	sortRoutes(rx, byOrder)
	for _, r := range rx {
		if m := ExecRegex(r.Match.Path, path); m != nil {
			why := fmt.Sprintf("regex %s matches", r.Match.Path)
			if longest != nil {
				why += fmt.Sprintf(" (wins over the longest prefix %s)", longest.Match.Path)
			}
			return &Selection{Route: r, Captures: m, Why: why}
		}
	}
	if longest != nil {
		return &Selection{Route: longest, Captures: []string{}, Why: "longest matching prefix " + longest.Match.Path}
	}
	return nil
}

// ───────────────────────── paths

// RestAfterPrefix is the path after the matched prefix ("" when equal; full path for "/").
func RestAfterPrefix(prefix, path string) string {
	if prefix == "/" {
		return path
	}
	p := strings.TrimSuffix(prefix, "/")
	if len(p) > len(path) {
		return ""
	}
	return path[len(p):]
}

// SplitPathQuery splits "path?query"; no "?" → query nil (unchanged).
func SplitPathQuery(s string) (string, *string) {
	p, q := s, (*string)(nil)
	if i := strings.IndexByte(s, '?'); i >= 0 {
		p = s[:i]
		x := s[i+1:]
		q = &x
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p, q
}

// UpstreamPathQuery is TS upstreamPathQuery: rewrite (captures of that regex) or stripPrefix, then addPrefix.
// how: none | strip | rewrite.
func UpstreamPathQuery(a *model.EdgeRouteAction, matched, path string, v Vars) (string, string, string) {
	up, args, how := path, v.Args, "none"
	if a.Rewrite != nil {
		if m := ExecRegex(a.Rewrite.Regex, path); m != nil {
			vv := v
			vv.URI, vv.Captures = path, m
			p, q := SplitPathQuery(ExpandEdgeVars(a.Rewrite.Replacement, vv))
			up = p
			if q != nil {
				args = *q
			}
			how = "rewrite"
		}
	} else if a.StripPrefix {
		rest := RestAfterPrefix(matched, path)
		if strings.HasPrefix(rest, "/") {
			up = rest
		} else {
			up = "/" + rest
		}
		how = "strip"
	}
	if a.AddPrefix != nil && *a.AddPrefix != "" {
		up = strings.TrimSuffix(*a.AddPrefix, "/") + up
	}
	return up, args, how
}

// UpstreamPath is the legacy helper (no query).
func UpstreamPath(stripPrefix bool, addPrefix *string, prefix, path string) string {
	a := model.EdgeRouteAction{StripPrefix: stripPrefix, AddPrefix: addPrefix}
	p, _, _ := UpstreamPathQuery(&a, prefix, path, Vars{URI: path, RequestURI: path, Scheme: "http"})
	return p
}

// RedirectLocation is TS redirectLocation. extra overrides the vars built from path / query / host.
func RedirectLocation(to string, keepPath bool, prefix, path, query, host string, extra *Vars) string {
	args := strings.TrimPrefix(query, "?")
	v := Vars{URI: path, Args: args, RequestURI: path, Host: host, Scheme: "http", Captures: []string{}}
	if args != "" {
		v.RequestURI = path + "?" + args
	}
	if extra != nil {
		e := *extra
		e.URI, e.Host = path, host
		v = e
	}
	loc := ExpandEdgeVars(to, v)
	if keepPath {
		rest := RestAfterPrefix(prefix, path)
		if rest == "/" {
			rest = ""
		}
		q := ""
		if args != "" {
			q = "?" + args
		}
		loc = strings.TrimSuffix(loc, "/") + rest + q
	}
	return loc
}

var agentPathRe = regexp.MustCompile(`^/_opm/(app|login|install|assets)(/|$)`)

// IsKnownAgentPath reports a known agent GUI / API path under /_opm (else agent 404).
func IsKnownAgentPath(path string) bool {
	if path == "/_opm" || path == "/_opm/" {
		return true
	}
	if strings.HasPrefix(path, "/_opm/peer/") || path == "/_opm/peer" {
		return false
	}
	return agentPathRe.MatchString(path) || strings.HasPrefix(path, "/_opm/api/") || path == "/_opm/healthz"
}

// SafeDecode is TS safeDecode (decodeURIComponent; malformed → unchanged).
func SafeDecode(p string) string {
	d, err := url.PathUnescape(p)
	if err != nil || !utf8.ValidString(d) {
		return p
	}
	return d
}

// EffectiveResponseHeaders is TS effectiveResponseHeaders. from: route | host | none.
func EffectiveResponseHeaders(r *model.EdgeRoute, h *model.EdgeHost) (model.EdgeResponseHeaders, string) {
	if r != nil && r.ResponseHeaders != nil {
		return *r.ResponseHeaders, "route"
	}
	if h != nil && !h.ResponseHeaders.IsEmpty() {
		return h.ResponseHeaders, "host"
	}
	return model.EmptyResponseHeaders(), "none"
}

// EffectiveMaxBody is TS effectiveMaxBody: nil = unlimited; from: route | edge.
func EffectiveMaxBody(edgeMax *int64, a *model.EdgeRouteAction) (*int64, string) {
	if a.MaxBodyBytes != nil {
		if *a.MaxBodyBytes == 0 {
			return nil, "route"
		}
		v := *a.MaxBodyBytes
		return &v, "route"
	}
	if edgeMax == nil || *edgeMax == 0 {
		return nil, "edge"
	}
	v := *edgeMax
	return &v, "edge"
}

// FormatBytes is TS formatBytes.
func FormatBytes(n *int64) string {
	if n == nil {
		return "unlimited"
	}
	u := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i, x := 0, *n
	for x >= 1024 && i < len(u)-1 && x%1024 == 0 {
		x /= 1024
		i++
	}
	return fmt.Sprintf("%d %s", x, u[i])
}

// ───────────────────────── the walk (shared by Resolve and the Caddy opm_route handler)

// RewriteStep is TS EdgeRewriteStep.
type RewriteStep struct {
	RouteID string `json:"routeId"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// Walk outcomes.
const (
	WalkRoute       = "route"         // Sel is the final route (proxy / redirect)
	WalkAuto        = "auto-redirect" // Sel.Route is the proxy prefix route; 301 to path + "/"
	WalkNotFound    = "not-found"     // no route (Sel nil) or a rewrite regex did not match (Sel = the rewrite route)
	WalkRewriteLoop = "rewrite-loop"  // more than MaxRewrites internal rewrites (Sel = the rewrite route)
)

// WalkResult is the outcome of matching with internal rewrites. V holds the current uri / args after rewrites.
type WalkResult struct {
	Outcome  string
	Sel      *Selection
	Rewrites []RewriteStep
	Steps    []string
}

// Walk selects the route for v.URI / v.Args among the host routes, applying rewrite routes (nginx `rewrite … last`)
// until a proxy / redirect route is found. v is updated in place (uri / args).
func Walk(hostRoutes []model.EdgeRoute, method string, v *Vars) WalkResult {
	res := WalkResult{Rewrites: []RewriteStep{}}
	pq := func() string {
		if v.Args != "" {
			return v.URI + "?" + v.Args
		}
		return v.URI
	}
	for {
		sel := SelectRoute(hostRoutes, v.URI, method)
		res.Sel = sel
		if sel == nil {
			res.Outcome = WalkNotFound
			res.Steps = append(res.Steps, fmt.Sprintf("No route matches %s. The edge serves the site not-found page (notFoundPageId; built-in page if null).", v.URI))
			return res
		}
		r := sel.Route
		if sel.AutoRedirect {
			res.Outcome = WalkAuto
			res.Steps = append(res.Steps, fmt.Sprintf(`No route for %s, but proxy route %s has prefix %s: nginx answers 301 with a trailing "/".`, v.URI, r.RouteID, r.Match.Path))
			return res
		}
		res.Steps = append(res.Steps, fmt.Sprintf("Match: route %s — %s", r.RouteID, sel.Why))
		if r.Action.Type != "rewrite" {
			res.Outcome = WalkRoute
			return res
		}
		if len(res.Rewrites) >= MaxRewrites {
			res.Outcome = WalkRewriteLoop
			res.Steps = append(res.Steps, fmt.Sprintf("More than %d internal rewrites: the edge answers 500 (nginx does the same).", MaxRewrites))
			return res
		}
		mm := ExecRegex(r.Action.Regex, v.URI)
		if mm == nil {
			res.Outcome = WalkNotFound
			res.Steps = append(res.Steps, fmt.Sprintf("The rewrite regex %s does not match %s: not-found page.", r.Action.Regex, v.URI))
			return res
		}
		from := pq()
		vv := *v
		vv.Captures = mm
		p, q := SplitPathQuery(ExpandEdgeVars(r.Action.Replacement, vv))
		v.URI = p
		if q != nil {
			v.Args = *q
		}
		res.Rewrites = append(res.Rewrites, RewriteStep{RouteID: r.RouteID, From: from, To: pq()})
		res.Steps = append(res.Steps, fmt.Sprintf(`Rewrite (internal, like nginx "last"): %s → %s; the routes are matched again.`, from, pq()))
	}
}

// AutoRedirectLocation is the nginx auto-redirect target (path + "/" + "?args").
func AutoRedirectLocation(v *Vars) string {
	if v.Args != "" {
		return v.URI + "/?" + v.Args
	}
	return v.URI + "/"
}

// ───────────────────────── resolve

// Options is TS EdgeResolveOptions.
type Options struct {
	Method    string                    // default GET
	Ports     map[string]map[string]int // containerSlug → portName → container port
	Slugs     map[string]string         // unused (kept for compatibility)
	NotReady  map[string]bool           // containerSlugs not completed
	BodyBytes *int64                    // request body size (→ 413)
	Upgrade   bool                      // the request asks for a WebSocket upgrade (→ 400 when websocket false)
}

// Upstream is the proxy target.
type Upstream struct {
	ContainerSlug string `json:"containerSlug"`
	PortName      string `json:"portName"`
	ContainerPort *int   `json:"containerPort"`
	Path          string `json:"path"`
	URL           string `json:"url"`
}

// HeaderTrace is one request header of the trace.
type HeaderTrace struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"` // default | route
}

// UpstreamPathTrace is TS trace.upstreamPath.
type UpstreamPathTrace struct {
	How  string `json:"how"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Timeouts is TS trace.timeouts.
type Timeouts struct {
	ConnectSec float64 `json:"connectSec"`
	SendSec    float64 `json:"sendSec"`
	ReadSec    float64 `json:"readSec"`
}

// Buffering is TS trace.buffering.
type Buffering struct {
	Request  bool `json:"request"`
	Response bool `json:"response"`
}

// MatchTrace is TS trace.match.
type MatchTrace struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Why  string `json:"why"`
}

// Trace is TS EdgeTrace.
type Trace struct {
	Match                 MatchTrace                `json:"match"`
	UpstreamPath          *UpstreamPathTrace        `json:"upstreamPath"`
	RequestHeaders        []HeaderTrace             `json:"requestHeaders"`
	RemovedRequestHeaders []string                  `json:"removedRequestHeaders"`
	ResponseHeaders       model.EdgeResponseHeaders `json:"responseHeaders"`
	ResponseHeadersFrom   string                    `json:"responseHeadersFrom"`
	Timeouts              *Timeouts                 `json:"timeouts"`
	Buffering             *Buffering                `json:"buffering"`
	MaxBodyBytes          *int64                    `json:"maxBodyBytes"`
	MaxBodyFrom           string                    `json:"maxBodyFrom"`
	Websocket             *bool                     `json:"websocket"`
}

// Resolution is TS EdgeResolution. HostMatch / Agent "" are emitted as null.
type Resolution struct {
	Status       int              `json:"status"`
	Kind         string           `json:"kind"`
	Title        string           `json:"title"`
	Host         *model.EdgeHost  `json:"host"`
	HostMatch    string           `json:"hostMatch"`
	Route        *model.EdgeRoute `json:"route"`
	Location     *string          `json:"location"`
	Upstream     *Upstream        `json:"upstream"`
	Agent        string           `json:"agent"`
	Steps        []string         `json:"steps"`
	Rewrites     []RewriteStep    `json:"rewrites"`
	Captures     []string         `json:"captures"`
	AutoRedirect bool             `json:"autoRedirect"`
	Trace        *Trace           `json:"trace"`
	Order        []OrderEntry     `json:"order"`
}

// MarshalJSON emits null for empty hostMatch / agent and [] for nil lists.
func (r Resolution) MarshalJSON() ([]byte, error) {
	type alias Resolution
	c := alias(r)
	if c.Steps == nil {
		c.Steps = []string{}
	}
	if c.Rewrites == nil {
		c.Rewrites = []RewriteStep{}
	}
	if c.Captures == nil {
		c.Captures = []string{}
	}
	if c.Order == nil {
		c.Order = []OrderEntry{}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if r.HostMatch == "" {
		m["hostMatch"] = json.RawMessage("null")
	}
	if r.Agent == "" {
		m["agent"] = json.RawMessage("null")
	}
	return json.Marshal(m)
}

var schemeRe = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }

// removeDotSegments is RFC 3986 §5.2.4 (WHATWG URL resolves "." / ".." in http paths).
func removeDotSegments(p string) string {
	if !strings.Contains(p, ".") {
		return p
	}
	segs := strings.Split(p, "/")
	out := []string{}
	for i, s := range segs {
		last := i == len(segs)-1
		switch strings.ToLower(s) {
		case ".", "%2e":
			if last {
				out = append(out, "")
			}
		case "..", ".%2e", "%2e.", "%2e%2e":
			if len(out) > 1 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, s)
		}
	}
	r := strings.Join(out, "/")
	if !strings.HasPrefix(r, "/") {
		r = "/" + r
	}
	return r
}

// Resolve answers how the edge treats one request (TS resolveEdgeRequest).
func Resolve(edge *model.Edge, rawURL string, o Options) Resolution {
	method := strings.ToUpper(o.Method)
	if method == "" {
		method = "GET"
	}
	src := rawURL
	if !schemeRe.MatchString(src) {
		src = "http://" + src
	}
	u, err := url.Parse(src)
	bad := Resolution{Status: 0, Kind: "bad-url", Title: "Not a URL", Steps: []string{"Enter for example https://plant.acme.com/api/v1/x"}}
	if err != nil || u.Host == "" {
		return bad
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return Resolution{Status: 0, Kind: "bad-url", Title: "Only http and https", Steps: []string{}}
	}
	hn := strings.ToLower(strings.Trim(u.Hostname(), "[]"))
	rawQuery := ""
	if u.RawQuery != "" {
		rawQuery = "?" + u.RawQuery
	}
	rawPath := removeDotSegments(u.EscapedPath())
	if rawPath == "" {
		rawPath = "/"
	}
	port := 80
	if scheme == "https" {
		port = 443
	}
	defPort := port
	if ps := u.Port(); ps != "" {
		n, err := strconv.Atoi(ps)
		if err != nil || n > 65535 {
			return bad
		}
		port = n
	}
	uhost := strings.ToLower(u.Hostname())
	if strings.Contains(uhost, ":") {
		uhost = "[" + uhost + "]"
	}
	if port != defPort {
		uhost += ":" + strconv.Itoa(port)
	}
	var want *int
	if scheme == "https" {
		want = edge.HTTPSPort
	} else {
		want = &edge.HTTPPort
	}
	if want == nil || port != *want {
		hs := "off"
		if edge.HTTPSPort != nil {
			hs = strconv.Itoa(*edge.HTTPSPort)
		}
		return Resolution{Status: 0, Kind: "not-edge-port", Title: fmt.Sprintf("Port %d is not an edge port", port), Steps: []string{
			fmt.Sprintf("The edge listens on http %d and https %s.", edge.HTTPPort, hs),
			"agentPort and agentGuiAliasPort serve only /_opm/ and do not use these rules.",
		}}
	}
	host, how := MatchHost(edge, hn)
	if host == nil {
		if scheme == "https" {
			return Resolution{Status: 0, Kind: "tls-fail", Title: "TLS handshake fails", Steps: []string{fmt.Sprintf(`No entry in edge.hosts matches %s and there is no "*" host.`, hn), "There is no certificate for this name."}}
		}
		return Resolution{Status: 404, Kind: "not-found", Title: "404 · site not-found page", Steps: []string{fmt.Sprintf(`No entry in edge.hosts matches %s and there is no "*" host.`, hn), "The edge answers with the site not-found page."}}
	}
	at := Resolution{Host: host, HostMatch: how}
	steps := []string{fmt.Sprintf("Host %s → %s (%s) · TLS %s", hn, host.Hostname, how, host.TLS.Mode)}
	if scheme == "https" && host.TLS.Mode == "none" {
		at.Kind, at.Title = "tls-fail", "TLS handshake fails"
		at.Steps = append(steps, "TLS mode is none for this host, so the edge has no certificate for HTTPS.")
		return at
	}
	if scheme == "http" && host.RedirectHTTPToHTTPS && host.TLS.Mode != "none" && edge.HTTPSPort != nil {
		hp := ""
		if *edge.HTTPSPort != 443 {
			hp = ":" + strconv.Itoa(*edge.HTTPSPort)
		}
		h := strings.ToLower(u.Hostname())
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		at.Status, at.Kind, at.Title = 308, "redirect", "308 → HTTPS"
		at.Location = sp("https://" + h + hp + rawPath + rawQuery)
		at.Steps = append(steps, "redirectHttpToHttps is on for this host. Routes are not checked on HTTP.")
		return at
	}
	if rawPath == "/_opm" || strings.HasPrefix(rawPath, "/_opm/") {
		known := IsKnownAgentPath(rawPath)
		at.Kind = "agent"
		at.Steps = append(steps, "/_opm/ is reserved on every host. Routes are not checked.")
		if known {
			at.Status, at.Agent, at.Title = 200, "gui", "200 · agent GUI / API"
			at.Steps = append(at.Steps, "The agent answers (in process, not a container).")
		} else {
			at.Status, at.Agent, at.Title = 404, "agent-404", "404 · agent 404 page"
			at.Steps = append(at.Steps, "Unknown /_opm/ path → the AGENT 404 page (never the site page). /_opm/peer/ is only on agentPort.")
		}
		return at
	}
	hostRoutes := HostRoutes(edge, host)
	var vis []model.EdgeRoute
	for i := range hostRoutes {
		if MethodHit(&hostRoutes[i], method) {
			vis = append(vis, hostRoutes[i])
		}
	}
	at.Order = EffectiveRouteOrder(vis)
	ids := []string{}
	for _, x := range at.Order {
		ids = append(ids, x.RouteID)
	}
	list := strings.Join(ids, " → ")
	if list == "" {
		list = "none"
	}
	steps = append(steps, fmt.Sprintf("Routes for this host (%s), effective order: %s", method, list))

	v := Vars{URI: SafeDecode(rawPath), Args: strings.TrimPrefix(rawQuery, "?"), RequestURI: rawPath + rawQuery, Host: hn, Scheme: scheme, Captures: []string{}}
	w := Walk(hostRoutes, method, &v)
	steps = append(steps, w.Steps...)
	at.Rewrites = w.Rewrites
	switch w.Outcome {
	case WalkNotFound:
		if w.Sel != nil {
			at.Route = w.Sel.Route
		}
		at.Status, at.Kind, at.Title, at.Steps = 404, "not-found", "404 · site not-found page", steps
		return at
	case WalkRewriteLoop:
		at.Route = w.Sel.Route
		at.Status, at.Kind, at.Title, at.Steps = 500, "rewrite-loop", "500 · rewrite loop", steps
		return at
	case WalkAuto:
		at.Route = w.Sel.Route
		at.Status, at.Kind, at.Title, at.Steps = 301, "redirect", "301 · add trailing /", steps
		at.Location = sp(AutoRedirectLocation(&v))
		at.AutoRedirect = true
		return at
	}
	sel := w.Sel
	r := sel.Route
	a := &r.Action
	at.Route = r
	at.Captures = sel.Captures
	rh, rhFrom := EffectiveResponseHeaders(r, host)
	tr := &Trace{Match: MatchTrace{Kind: kindOf(r), Path: r.Match.Path, Why: sel.Why}, ResponseHeaders: rh, ResponseHeadersFrom: rhFrom,
		RequestHeaders: []HeaderTrace{}, RemovedRequestHeaders: []string{}, MaxBodyFrom: "edge"}
	if a.Type == "redirect" {
		q := ""
		if v.Args != "" {
			q = "?" + v.Args
		}
		ex := Vars{Args: v.Args, RequestURI: v.RequestURI, Scheme: scheme, Captures: sel.Captures}
		loc := RedirectLocation(a.To, a.KeepPath, r.Match.Path, v.URI, q, hn, &ex)
		at.Status, at.Kind, at.Title = a.Status, "redirect", fmt.Sprintf("%d redirect", a.Status)
		at.Location = sp(loc)
		if a.KeepPath {
			rest := RestAfterPrefix(r.Match.Path, v.URI)
			if rest == "" {
				rest = "/"
			}
			steps = append(steps, fmt.Sprintf("keepPath: the rest of the path (%s) and the query are added.", rest))
		} else {
			steps = append(steps, "Location "+loc)
		}
		at.Steps, at.Trace = steps, tr
		return at
	}
	mb, mbFrom := EffectiveMaxBody(edge.MaxBodyBytes, a)
	req := []HeaderTrace{
		{"Host", uhost, "default"},
		{"X-Real-IP", "{client-ip}", "default"},
		{"X-Forwarded-For", "{client-ip}", "default"},
		{"X-Forwarded-Proto", scheme, "default"},
		{"X-Forwarded-Host", uhost, "default"},
	}
	for _, kv := range a.SetHeaders {
		if IsHopByHop(kv.Name) {
			continue
		}
		hv := v
		hv.Captures = []string{}
		val := ExpandEdgeVars(kv.Value, hv)
		found := false
		for i := range req {
			if strings.EqualFold(req[i].Name, kv.Name) {
				req[i] = HeaderTrace{kv.Name, val, "route"}
				found = true
				break
			}
		}
		if !found {
			req = append(req, HeaderTrace{kv.Name, val, "route"})
		}
	}
	removed := append([]string{}, a.RemoveHeaders...)
	out := []HeaderTrace{}
	for _, h := range req {
		drop := false
		for _, x := range removed {
			if strings.EqualFold(x, h.Name) {
				drop = true
			}
		}
		if !drop {
			out = append(out, h)
		}
	}
	tr.RequestHeaders, tr.RemovedRequestHeaders = out, removed
	tr.Timeouts = &Timeouts{a.ConnectTimeoutSec, a.SendTimeoutSec, a.ReadTimeoutSec}
	tr.Buffering = &Buffering{a.RequestBuffering, a.ResponseBuffering}
	tr.MaxBodyBytes, tr.MaxBodyFrom = mb, mbFrom
	tr.Websocket = bp(a.Websocket)
	at.Trace = tr
	if o.Upgrade && !a.Websocket {
		at.Status, at.Kind, at.Title = 400, "ws-refused", "400 · WebSocket not allowed"
		at.Steps = append(steps, "The route has websocket off: an Upgrade: websocket request gets 400.")
		return at
	}
	if o.BodyBytes != nil && mb != nil && *o.BodyBytes > *mb {
		at.Status, at.Kind, at.Title = 413, "too-large", "413 · body too large"
		at.Steps = append(steps, fmt.Sprintf("The body (%d B) is above the limit %s (%s): 413 before the upstream is contacted.", *o.BodyBytes, FormatBytes(mb), mbFrom))
		return at
	}
	if o.NotReady[a.ContainerSlug] {
		at.Status, at.Kind, at.Title = 503, "not-ready", "503 · not-found page"
		at.Steps = append(steps, fmt.Sprintf("Container %s is not ready. The edge serves the not-found page with status 503.", a.ContainerSlug))
		return at
	}
	hv := v
	hv.Captures = []string{}
	up, args, uhow := UpstreamPathQuery(a, r.Match.Path, v.URI, hv)
	tr.UpstreamPath = &UpstreamPathTrace{How: uhow, From: v.URI, To: up}
	upq := up
	if args != "" {
		upq += "?" + args
	}
	var cport *int
	if m, ok := o.Ports[a.ContainerSlug]; ok {
		if p, ok := m[a.PortName]; ok {
			cport = &p
		}
	}
	cp := "?"
	if cport != nil {
		cp = strconv.Itoa(*cport)
	}
	u2 := fmt.Sprintf("http://%s:%s%s", a.ContainerSlug, cp, upq)
	at.Status, at.Kind, at.Title = 200, "proxy", "proxy → container"
	at.Upstream = &Upstream{ContainerSlug: a.ContainerSlug, PortName: a.PortName, ContainerPort: cport, Path: upq, URL: u2}
	s1 := "The path goes unchanged."
	switch uhow {
	case "strip":
		s1 = fmt.Sprintf("stripPrefix: %s → %s", v.URI, up)
	case "rewrite":
		s1 = fmt.Sprintf("rewrite %s → %s", a.Rewrite.Regex, up)
	}
	hdr := []string{}
	for _, h := range out {
		if h.Source == "route" {
			hdr = append(hdr, h.Name+": "+h.Value)
		}
	}
	s3 := "Request headers: Host kept, X-Real-IP, X-Forwarded-For/Proto/Host set"
	if len(hdr) > 0 {
		s3 += " · " + strings.Join(hdr, " · ")
	}
	if len(removed) > 0 {
		s3 += " · removed " + strings.Join(removed, ", ")
	}
	rhs := []string{}
	for _, kv := range rh.Set {
		rhs = append(rhs, "set "+kv.Name+": "+kv.Value)
	}
	for _, kv := range rh.Add {
		rhs = append(rhs, "add "+kv.Name+": "+kv.Value)
	}
	for _, k := range rh.Remove {
		rhs = append(rhs, "remove "+k)
	}
	s4 := "Response headers: none added."
	if len(rhs) > 0 {
		s4 = fmt.Sprintf("Response headers (%s): %s", rhFrom, strings.Join(rhs, " · "))
	}
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	s5 := fmt.Sprintf("Timeouts: connect %s s · send %s s · read %s s (idle between reads)", f(a.ConnectTimeoutSec), f(a.SendTimeoutSec), f(a.ReadTimeoutSec))
	bq, br := "streamed", "flushed at once"
	if a.RequestBuffering {
		bq = "buffered (≤ 8 MiB in memory, then streamed)"
	}
	if a.ResponseBuffering {
		br = "buffered"
	}
	s6 := fmt.Sprintf("Body: %s · limit %s (%s) · response %s (text/event-stream always flushes)", bq, FormatBytes(mb), mbFrom, br)
	s7 := "WebSocket upgrade allowed."
	if !a.Websocket {
		s7 = "WebSocket upgrade refused (400)."
	}
	at.Steps = append(steps, s1, "Upstream (site network, DNS = container name): "+u2, s3, s4, s5, s6, s7)
	return at
}

// VectorView is TS edgeVectorView (the part testdata/edge-vectors.json pins).
func VectorView(r Resolution) map[string]any {
	var routeID any
	if r.Route != nil {
		routeID = r.Route.RouteID
	}
	var loc, upURL any
	if r.Location != nil {
		loc = *r.Location
	}
	if r.Upstream != nil {
		upURL = r.Upstream.URL
	}
	order := []string{}
	for _, o := range r.Order {
		order = append(order, o.Group+":"+o.RouteID)
	}
	rw := r.Rewrites
	if rw == nil {
		rw = []RewriteStep{}
	}
	caps := r.Captures
	if caps == nil {
		caps = []string{}
	}
	var tr any
	if t := r.Trace; t != nil {
		tr = map[string]any{
			"matchKind": t.Match.Kind, "upstreamPath": t.UpstreamPath, "requestHeaders": t.RequestHeaders,
			"removedRequestHeaders": t.RemovedRequestHeaders, "responseHeaders": t.ResponseHeaders, "responseHeadersFrom": t.ResponseHeadersFrom,
			"timeouts": t.Timeouts, "buffering": t.Buffering, "maxBodyBytes": t.MaxBodyBytes, "maxBodyFrom": t.MaxBodyFrom, "websocket": t.Websocket,
		}
	}
	return map[string]any{"status": r.Status, "kind": r.Kind, "routeId": routeID, "location": loc, "upstreamUrl": upURL, "rewrites": rw,
		"captures": caps, "autoRedirect": r.AutoRedirect, "order": order, "trace": tr}
}
