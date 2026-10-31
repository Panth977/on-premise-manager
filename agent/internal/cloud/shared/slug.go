package shared

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"opm/agent/internal/cloud/doc"
)

var jReNotSlug = regexp.MustCompile(`[^a-z0-9]+`)

// SlugifyLabel is slugifyLabel of github-mirror.ts.
func SlugifyLabel(label, prefix string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(label) {
		if r < 0x300 || r > 0x36F {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	s := strings.Trim(jReNotSlug.ReplaceAllString(b.String(), "-"), "-")
	if s == "" {
		s = prefix + "1"
	} else if s[0] < 'a' || s[0] > 'z' {
		s = prefix + s
	}
	s = strings.TrimRight(doc.JSSlice(s, 40), "-")
	if doc.JSLen(s) < 2 {
		s = doc.JSSlice(s+"-1", 40)
	}
	return s
}

// UniqueSlug is uniqueSlug of github-mirror.ts.
func UniqueSlug(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		suf := "-" + strconv.Itoa(n)
		s := strings.TrimRight(doc.JSSlice(base, 40-len(suf)), "-") + suf
		if !taken[s] {
			return s
		}
	}
}
