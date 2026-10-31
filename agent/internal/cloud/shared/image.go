package shared

import (
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud/doc"
)

// ImageKey is imageKey.
func ImageKey(serviceSlug, version string) string { return serviceSlug + ":" + version }

var (
	jReBlocks    = regexp.MustCompile(`\{\{(?s:.*?)\}\}`)
	jReKindName  = regexp.MustCompile(`^[^.]` + jLine + `*\.([A-Za-z0-9]{1,10})$`)
	jReHasLetter = regexp.MustCompile(`[A-Za-z]`)
	jReScheme    = regexp.MustCompile(`^[a-zA-Z]+://`)
	jReHTTP      = regexp.MustCompile(`^[hH][tT][tT][pP][sS]?://`)
	jRePathTail  = regexp.MustCompile(`/` + jLine + `*$`)
	jReVersion   = regexp.MustCompile(`^(` + jLine + `*?)(\d+)([^\d]*)$`)
)

// InferImageVolumeKind is inferImageVolumeKind: "dir" | "file".
func InferImageVolumeKind(mountPath any) string {
	p, ok := mountPath.(string)
	if !ok || strings.HasSuffix(p, "/") {
		return "dir"
	}
	plain := jReBlocks.ReplaceAllString(p, "x")
	m := jReKindName.FindStringSubmatch(plain[strings.LastIndexByte(plain, '/')+1:])
	if m != nil && jReHasLetter.MatchString(m[1]) && jLower(m[1]) != "d" {
		return "file"
	}
	return "dir"
}

// ImageVolumeKind is imageVolumeKind.
func ImageVolumeKind(v any) (kind string, err error) {
	defer jCatch(&err)
	return jImageVolumeKind(v), nil
}

func jImageVolumeKind(v any) string {
	k := jGet(v, "kind")
	if jIsOneOf(k, "dir", "file") {
		return k.(string)
	}
	return InferImageVolumeKind(jGet(v, "mountPath"))
}

var jDockerHubHosts = []string{"docker.io", "index.docker.io", "registry-1.docker.io"}

func jRefErr(msg string) *doc.Map { return doc.Obj("ok", false, "error", msg) }

// ParseImageReference is parseImageReference: { ok: true, parts: { host, repository, tag, digest } } or { ok: false, error }.
func ParseImageReference(ref any) *doc.Map {
	s, ok := ref.(string)
	if !ok || doc.JSTrim(s) == "" {
		return jRefErr("enter an image reference, e.g. docker.io/library/redis:7.4 or ghcr.io/acme/api:1.2.0")
	}
	r := doc.JSTrim(s)
	if doc.JSLen(r) > 512 {
		return jRefErr("at most 512 characters")
	}
	if jHasSpace(r) {
		return jRefErr("an image reference has no spaces")
	}
	r = jReScheme.ReplaceAllString(r, "")
	var digest, tag any
	if at := strings.IndexByte(r, '@'); at >= 0 {
		d := r[at+1:]
		digest, r = d, r[:at]
		if !RE.Sha256.MatchString(d) {
			return jRefErr(`digest "` + d + `" must be sha256: + 64 lower-case hex`)
		}
	}
	if colon := strings.LastIndexByte(r, ':'); colon > strings.LastIndexByte(r, '/') {
		t := r[colon+1:]
		tag, r = t, r[:colon]
		if !RE.Tag.MatchString(t) {
			return jRefErr(`tag "` + t + `" is not a docker tag (letters, digits, _ . -; at most 128; not starting with . or -)`)
		}
	}
	host := "docker.io"
	first, _, _ := strings.Cut(r, "/")
	if strings.Contains(r, "/") && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		host = jLower(first)
		r = r[len(first)+1:]
	}
	if slices.Contains(jDockerHubHosts, host) {
		host = "docker.io"
	}
	if host == "docker.io" && r != "" && !strings.Contains(r, "/") {
		r = "library/" + r
	}
	if r == "" {
		return jRefErr("the repository is missing, e.g. docker.io/northwind/northwind:1.0")
	}
	if !RE.Repository.MatchString(r) {
		return jRefErr(`repository "` + r + `" is not valid: lower-case letters, digits and . _ - separated by /`)
	}
	if tag == nil && digest == nil {
		tag = "latest"
	}
	return doc.Obj("ok", true, "parts", doc.Obj("host", host, "repository", r, "tag", tag, "digest", digest))
}

// FormatImageReference is formatImageReference.
func FormatImageReference(p *doc.Map) string {
	s := jStr(p.Val("host")) + "/" + jStr(p.Val("repository"))
	if p.Truthy("tag") {
		s += ":" + jStr(p.Val("tag"))
	}
	if p.Truthy("digest") {
		s += "@" + jStr(p.Val("digest"))
	}
	return s
}

// RegistryHost is registryHost.
func RegistryHost(url string) string {
	h := jReHTTP.ReplaceAllString(url, "")
	if loc := jRePathTail.FindStringIndex(h); loc != nil {
		h = h[:loc[0]]
	}
	h = jLower(h)
	if slices.Contains(jDockerHubHosts, h) {
		return "docker.io"
	}
	return h
}

// MatchRegistries is matchRegistries (sorted by label like localeCompare).
func MatchRegistries(host string, registries []any) (out []any, err error) {
	defer jCatch(&err)
	out = []any{}
	for _, r := range registries {
		if u, ok := jGet(r, "url").(string); ok {
			if RegistryHost(u) == host {
				out = append(out, r)
			}
		} else {
			jThrow("url.replace is not a function")
		}
	}
	c := collate.New(language.AmericanEnglish)
	slices.SortStableFunc(out, func(a, b any) int { return c.CompareString(jStr(jGet(a, "label")), jStr(jGet(b, "label"))) })
	return out, nil
}

// NextVersion is nextVersion.
func NextVersion(latest string, taken []string) string {
	cand := "v1"
	if latest != "" {
		if m := jReVersion.FindStringSubmatch(latest); m != nil {
			n, _ := strconv.ParseFloat(m[2], 64)
			cand = m[1] + jNum(n+1) + m[3]
		} else {
			cand = latest + "-2"
		}
	}
	for slices.Contains(taken, cand) {
		cand = NextVersion(cand, nil)
	}
	return cand
}

// jNum is String(number).
func jNum(f float64) string {
	if s, err := canonical.FormatNumber(f); err == nil {
		return s
	}
	return jStr(f)
}

// jFixed is Number.prototype.toFixed (exact, ties away from zero; Go rounds exact ties to even).
func jFixed(x float64, digits int) string {
	if x < 0 {
		return "-" + jFixed(-x, digits)
	}
	r := new(big.Rat).SetFloat64(x)
	if r == nil {
		return jNum(x)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	r.Mul(r, new(big.Rat).SetInt(scale))
	r.Add(r, big.NewRat(1, 2))
	n := new(big.Int).Div(r.Num(), r.Denom()).String()
	if digits == 0 {
		return n
	}
	for len(n) <= digits {
		n = "0" + n
	}
	return n[:len(n)-digits] + "." + n[len(n)-digits:]
}
