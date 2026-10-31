package shared

import (
	"math"
	"regexp"
	"slices"
	"strings"

	"opm/agent/internal/cloud/doc"
)

// Value exports of shared/src/staging.ts.
const (
	// STAGE_TTL_MS is STAGE_TTL_MS.
	STAGE_TTL_MS = 24 * 3600_000
	// STAGE_MAX_BLOBS is STAGE_MAX_BLOBS.
	STAGE_MAX_BLOBS = 5000
	// STAGE_MAX_BYTES is STAGE_MAX_BYTES.
	STAGE_MAX_BYTES = 1024 * 1024 * 1024
	// STAGING_PREFIX is STAGING_PREFIX.
	STAGING_PREFIX = "staging/"
)

// RE_STAGE_ID is RE_STAGE_ID.
var RE_STAGE_ID = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

var jReSha = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// StagingObject is stagingObject.
func StagingObject(stageID, hex string) string { return STAGING_PREFIX + stageID + "/" + hex }

// BlobHex is blobHex.
func BlobHex(s string) string { return strings.TrimPrefix(s, "sha256:") }

// ConfigBlobs is configBlobs: [{ sha256, sizeBytes }] unique, sorted.
func ConfigBlobs(config any) (out []any, err error) {
	defer jCatch(&err)
	return jConfigBlobs(config), nil
}

func jConfigBlobs(config any) []any {
	out := []any{}
	if !doc.Truthy(config) {
		return out
	}
	seen := map[string]bool{}
	add := func(sha, size any) {
		s, ok := sha.(string)
		if !ok {
			return
		}
		if !strings.HasPrefix(s, "sha256:") {
			s = "sha256:" + s
		}
		if !jReSha.MatchString(s) || seen[s] {
			return
		}
		seen[s] = true
		n, ok := size.(float64)
		if !ok || math.IsInf(n, 0) || math.IsNaN(n) {
			n = 0
		}
		out = append(out, doc.Obj("sha256", s, "sizeBytes", n))
	}
	for _, f := range jList(jGet(config, "files"), "config.files ?? []") {
		if c := jOpt(f, "content"); doc.Truthy(c) {
			add(jGet(c, "sha256"), jGet(c, "sizeBytes"))
		}
	}
	for _, site := range jList(jGet(config, "sites"), "config.sites ?? []") {
		for _, v := range jList(jOpt(jOpt(site, "config"), "volumes"), "sc?.volumes ?? []") {
			seed := jOpt(v, "seed")
			if !doc.Truthy(seed) {
				continue
			}
			switch k := jGet(seed, "kind"); {
			case jIs(k, "tree"):
				for _, e := range jList(jGet(seed, "entries"), "seed.entries ?? []") {
					add(jOpt(e, "sha256"), jOpt(e, "size"))
				}
			case jIs(k, "single"):
				add(jGet(seed, "sha256"), jGet(seed, "size"))
			case jIs(k, "archive"):
				add(jGet(seed, "blobSha256"), jGet(seed, "size"))
			}
		}
	}
	slices.SortFunc(out, func(a, b any) int { return strings.Compare(a.(*doc.Map).Str("sha256"), b.(*doc.Map).Str("sha256")) })
	return out
}

// NewConfigBlobs is newConfigBlobs.
func NewConfigBlobs(base, next any) (out []any, err error) {
	defer jCatch(&err)
	have := map[string]bool{}
	for _, b := range jConfigBlobs(base) {
		have[b.(*doc.Map).Str("sha256")] = true
	}
	out = []any{}
	for _, b := range jConfigBlobs(next) {
		if !have[b.(*doc.Map).Str("sha256")] {
			out = append(out, b)
		}
	}
	return out, nil
}

// TotalBytes is totalBytes.
func TotalBytes(l []any) float64 {
	n := 0.0
	for _, b := range l {
		if f, ok := b.(*doc.Map).NumOK("sizeBytes"); ok {
			n += f
		}
	}
	return n
}

// StageProgressText is stageProgressText (nil = null).
func StageProgressText(p *doc.Map) *string {
	if p == nil {
		return nil
	}
	text := ""
	switch p.Str("phase") {
	case "checking":
		text = "Checking content…"
	case "committing":
		text = "Committing…"
	default:
		mb := func(n float64) string {
			switch {
			case n < 1024:
				return jNum(math.Max(0, n)) + " B"
			case n < 1024*1024:
				return jNum(jRound(n/1024)) + " KB"
			case n < 10*1048576:
				return jFixed(n/1048576, 1) + " MB"
			}
			return jFixed(n/1048576, 0) + " MB"
		}
		s := "s"
		if jEq(p.Val("total"), 1.0) {
			s = ""
		}
		text = "Uploading " + jStr(p.Val("done")) + " of " + jStr(p.Val("total")) + " file" + s + " · " + mb(p.Num("bytesDone")) + " of " + mb(p.Num("bytesTotal"))
	}
	return &text
}
