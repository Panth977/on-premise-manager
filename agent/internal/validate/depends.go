package validate

// Go mirror of shared/src/depends.ts (SPEC-final §15.5, §21 #93): implicit depends (containers.<slug>.* expressions),
// suggested depends (literal hostname mentions), the APPLY start order and the DNS-failure log hint.
// Vectors: shared/testdata/depends-vectors.json (depends_test.go).

import (
	"regexp"
	"sort"
	"strings"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// InferredDepend is TS InferredDepend.
type InferredDepend struct {
	ContainerSlug string   `json:"containerSlug"`
	From          []string `json:"from"`
}

// ContainerDepends is TS ContainerDepends.
type ContainerDepends struct {
	ContainerSlug string           `json:"containerSlug"`
	Explicit      []string         `json:"explicit"`
	Implicit      []InferredDepend `json:"implicit"`
	Suggested     []InferredDepend `json:"suggested"`
}

// DependsInput is TS DependsInput.
type DependsInput struct {
	Site         *model.SiteConfig
	Images       map[string]CatalogImage // serviceSlug:version
	Files        []model.FileDef
	FileContents map[string]string
}

var (
	hostKeyRe   = regexp.MustCompile(`(?i)(host|server|broker|addr|endpoint|upstream|url|uri|dsn)`)
	urlAuthRe   = regexp.MustCompile("[A-Za-z][A-Za-z0-9+.-]*://(?:[^\\s/@\"'`]*@)?$")
	keyCtxRe    = regexp.MustCompile(`([A-Za-z0-9_]+)["']?\s*[:=]\s*["']?$`)
	exprBlockRe = regexp.MustCompile(`\{\{[\s\S]*?\}\}`)
	ansiRe      = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|[ -/]*[0-~])?")
)

func isHostnameCh(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}
func isLabelCh(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}
func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// LiteralHostMentions is TS literalHostMentions: the slugs mentioned as a hostname (URL authority, host:port, or after a
// host-like key), ordered by the position of the mention.
func LiteralHostMentions(text string, slugs []string) []string {
	type hit struct {
		slug string
		pos  int
	}
	var found []hit
	seen := map[string]bool{}
	for _, slug := range slugs {
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		from := 0
		for {
			k := strings.Index(text[from:], slug)
			if k < 0 {
				break
			}
			i := from + k
			from = i + 1
			j := i + len(slug)
			if i > 0 && isHostnameCh(text[i-1]) {
				continue
			}
			if j < len(text) && isLabelCh(text[j]) {
				continue
			}
			if j < len(text) && text[j] == '.' && j+1 < len(text) && isAlnum(text[j+1]) {
				continue
			}
			before := text[strings.LastIndexByte(text[:i], '\n')+1 : i]
			url := urlAuthRe.MatchString(before)
			hostPort := false
			if j < len(text) && text[j] == ':' {
				n := 0
				for n < len(text)-j-1 && text[j+1+n] >= '0' && text[j+1+n] <= '9' {
					n++
				}
				hostPort = n >= 1 && n <= 5
			}
			key := false
			if m := keyCtxRe.FindStringSubmatch(before); m != nil {
				key = hostKeyRe.MatchString(m[1])
			}
			if url || hostPort || key {
				found = append(found, hit{slug, i})
				break
			}
		}
	}
	sort.SliceStable(found, func(a, b int) bool {
		if found[a].pos != found[b].pos {
			return found[a].pos < found[b].pos
		}
		return found[a].slug < found[b].slug
	})
	out := make([]string, 0, len(found))
	for _, h := range found {
		out = append(out, h.slug)
	}
	return out
}

// ContainerRefs is TS containerRefs: the containers.<slug> slugs of an expression value (syntax errors → none).
func ContainerRefs(value string) []string {
	if !strings.Contains(value, "{{") {
		return nil
	}
	refs, err := expr.ExprRefs(value)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range refs {
		parts := strings.Split(r, ".")
		if len(parts) >= 3 && parts[0] == "containers" && !contains(out, parts[1]) {
			out = append(out, parts[1])
		}
	}
	return out
}

func addInferred(list []InferredDepend, slug, where string) []InferredDepend {
	for i := range list {
		if list[i].ContainerSlug == slug {
			if !contains(list[i].From, where) {
				list[i].From = append(list[i].From, where)
			}
			return list
		}
	}
	return append(list, InferredDepend{ContainerSlug: slug, From: []string{where}})
}

// InferDepends is TS inferDepends: explicit, implicit and suggested depends of every enabled container (declared order).
func InferDepends(in DependsInput) []ContainerDepends {
	var enabled []string
	for _, c := range in.Site.Containers {
		if c.Enabled {
			enabled = append(enabled, c.ContainerSlug)
		}
	}
	out := []ContainerDepends{}
	for ci := range in.Site.Containers {
		c := &in.Site.Containers[ci]
		if !c.Enabled {
			continue
		}
		explicit := []string{}
		for _, d := range c.Depends {
			explicit = append(explicit, d.ContainerSlug)
		}
		implicit, suggested := []InferredDepend{}, []InferredDepend{}
		var others []string
		for _, s := range enabled {
			if s != c.ContainerSlug {
				others = append(others, s)
			}
		}
		scan := func(where, value string, exprs bool, literal string) {
			if exprs {
				for _, r := range ContainerRefs(value) {
					if r != c.ContainerSlug && contains(others, r) && !contains(explicit, r) {
						implicit = addInferred(implicit, r, where)
					}
				}
			}
			for _, r := range LiteralHostMentions(literal, others) {
				if !contains(explicit, r) {
					suggested = addInferred(suggested, r, where)
				}
			}
		}
		noBlocks := func(s string) string { return exprBlockRe.ReplaceAllString(s, " ") }
		given := map[string]bool{}
		for _, e := range c.Env {
			given[e.Name] = true
			if e.Value != nil {
				scan("env "+e.Name, *e.Value, true, e.Name+"="+noBlocks(*e.Value))
			}
		}
		if img, ok := in.Images[ImageKey(c.ServiceSlug, c.Version)]; ok {
			for _, ie := range img.Env {
				if given[ie.Name] || ie.Value == nil {
					continue
				}
				if v, ok := ie.Value.Default(); ok {
					scan("image env "+ie.Name, v, true, ie.Name+"="+noBlocks(v))
				}
			}
		}
		seenFile := map[string]bool{}
		for _, m := range c.Volumes {
			if m.Source.Kind != "file" || seenFile[m.Source.FileSlug] {
				continue
			}
			slug := m.Source.FileSlug
			seenFile[slug] = true
			var f *model.FileDef
			for i := range in.Files {
				if in.Files[i].FileSlug == slug {
					f = &in.Files[i]
				}
			}
			content, ok := in.FileContents[slug]
			if f == nil || f.Editor == "binary" || !ok {
				continue
			}
			if f.RenderMode() == "expressions" {
				scan("file "+slug, content, true, noBlocks(content))
			} else {
				scan("file "+slug, content, false, content)
			}
		}
		imp := map[string]bool{}
		for _, d := range implicit {
			imp[d.ContainerSlug] = true
		}
		sug := []InferredDepend{}
		for _, d := range suggested {
			if !imp[d.ContainerSlug] {
				sug = append(sug, d)
			}
		}
		out = append(out, ContainerDepends{ContainerSlug: c.ContainerSlug, Explicit: explicit, Implicit: implicit, Suggested: sug})
	}
	return out
}

// StartOrder is TS startOrder: the APPLY start order of the enabled containers (referenced first, ties in the declared
// order; a reference cycle falls back to the declared order and is reported in cycle).
func StartOrder(sc *model.SiteConfig, deps []ContainerDepends) (order, cycle []string) {
	var cs []string
	idx := map[string]int{}
	for _, c := range sc.Containers {
		if _, dup := idx[c.ContainerSlug]; c.Enabled && !dup { // duplicate slugs are V_DUPLICATE errors elsewhere
			idx[c.ContainerSlug] = len(cs)
			cs = append(cs, c.ContainerSlug)
		}
	}
	edges := map[string]map[string]bool{}
	explicitOf := map[string]map[string]bool{}
	for _, s := range cs {
		edges[s], explicitOf[s] = map[string]bool{}, map[string]bool{}
	}
	for _, d := range deps {
		e, ok := edges[d.ContainerSlug]
		if !ok {
			continue
		}
		for _, x := range d.Explicit {
			if _, ok := idx[x]; ok && x != d.ContainerSlug {
				e[x] = true
				explicitOf[d.ContainerSlug][x] = true
			}
		}
		for _, l := range [][]InferredDepend{d.Implicit, d.Suggested} {
			for _, x := range l {
				if _, ok := idx[x.ContainerSlug]; ok && x.ContainerSlug != d.ContainerSlug {
					e[x.ContainerSlug] = true
				}
			}
		}
	}
	placed := map[string]bool{}
	inCycle := map[string]bool{}
	order = []string{}
	all := func(m map[string]bool) bool {
		for x := range m {
			if !placed[x] {
				return false
			}
		}
		return true
	}
	for len(order) < len(cs) {
		var left []string
		for _, s := range cs {
			if !placed[s] {
				left = append(left, s)
			}
		}
		next := ""
		for _, s := range left {
			if all(edges[s]) {
				next = s
				break
			}
		}
		if next == "" {
			for _, s := range left {
				inCycle[s] = true
			}
			for _, s := range left {
				if all(explicitOf[s]) {
					next = s
					break
				}
			}
			if next == "" {
				next = left[0]
			}
		}
		placed[next] = true
		order = append(order, next)
	}
	cycle = []string{}
	for _, s := range cs {
		if inCycle[s] {
			cycle = append(cycle, s)
		}
	}
	return order, cycle
}

// WaitDepends is TS waitDepends: explicit (enabled targets) + implicit.
func WaitDepends(d ContainerDepends, enabled []string) []string {
	out := []string{}
	for _, x := range d.Explicit {
		if contains(enabled, x) && x != d.ContainerSlug && !contains(out, x) {
			out = append(out, x)
		}
	}
	for _, x := range d.Implicit {
		if !contains(out, x.ContainerSlug) {
			out = append(out, x.ContainerSlug)
		}
	}
	return out
}

// DNSFailureHint is TS DnsFailureHint.
type DNSFailureHint struct {
	ContainerSlug string `json:"containerSlug"`
	Code          string `json:"code"`
}

type dnsRe struct {
	re   *regexp.Regexp
	code func(m []string) string
	host int
}

var dnsRes = []dnsRe{
	{regexp.MustCompile(`getaddrinfo (EAI_AGAIN|ENOTFOUND) ([A-Za-z0-9._-]+)`), func(m []string) string { return m[1] }, 2},
	{regexp.MustCompile(`lookup ([A-Za-z0-9._-]+)(?: on [^\s:]+:\d+)?: (no such host|server misbehaving|Temporary failure in name resolution)`), func(m []string) string { return m[2] }, 1},
	{regexp.MustCompile(`Could not resolve host:? ['"]?([A-Za-z0-9._-]+)`), func([]string) string { return "Could not resolve host" }, 1},
	{regexp.MustCompile(`([A-Za-z0-9._-]+): (Name or service not known|Temporary failure in name resolution|nodename nor servname provided, or not known)`), func(m []string) string { return m[2] }, 1},
}
var hostlessRe = regexp.MustCompile(`(EAI_AGAIN|ENOTFOUND|Name or service not known|Temporary failure in name resolution|no such host)`)

// StripANSI removes escape sequences (for log pattern matching).
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// DNSFailureHintOf is TS dnsFailureHint: the newest log line with a DNS failure naming another container of the site.
func DNSFailureHintOf(lines, slugs []string, subject string) *DNSFailureHint {
	var others []string
	for _, s := range slugs {
		if s != subject {
			others = append(others, s)
		}
	}
	clean := make([]string, len(lines))
	for i, l := range lines {
		clean[i] = StripANSI(l)
	}
	for i := len(clean) - 1; i >= 0; i-- {
		l := clean[i]
		for _, d := range dnsRes {
			if m := d.re.FindStringSubmatch(l); m != nil && contains(others, m[d.host]) {
				return &DNSFailureHint{ContainerSlug: m[d.host], Code: d.code(m)}
			}
		}
		if h := hostlessRe.FindStringSubmatch(l); h != nil {
			lo, hi := i-3, i+3
			if lo < 0 {
				lo = 0
			}
			if hi > len(clean)-1 {
				hi = len(clean) - 1
			}
			for k := lo; k <= hi; k++ {
				if near := LiteralHostMentions(clean[k], others); len(near) > 0 {
					return &DNSFailureHint{ContainerSlug: near[0], Code: h[1]}
				}
			}
		}
	}
	return nil
}

// DNSFailureMessage is TS dnsFailureMessage.
func DNSFailureMessage(subject string, h *DNSFailureHint) string {
	return subject + " could not resolve `" + h.ContainerSlug + "` (" + h.Code + ") — is " + h.ContainerSlug + " running/healthy? add it to Depends on"
}
