// Package volcontent is the Go mirror of shared/src/volume-content.ts (§21 #101): content path rules, the canonical
// volume tree + its hash, the exclude matcher (gitignore-lite) and the 3-way plan / merge of a cloud push. Vectors:
// shared/testdata/volume-content-vectors.json (TestVectors).
package volcontent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

// ───────────────────────── paths

var badChars = regexp.MustCompile("[\x00-\x1f\x7f\\\\]")

// PathError is contentPathError: the error text for a content path, or "" when valid.
func PathError(p string) string {
	if p == "" {
		return "the path is empty"
	}
	if len(p) > model.VolumeContentPathMaxBytes {
		return fmt.Sprintf("the path is longer than %d bytes", model.VolumeContentPathMaxBytes)
	}
	if badChars.MatchString(p) {
		return `the path holds a control character or "\"`
	}
	if strings.HasPrefix(p, "/") {
		return `the path must be relative (no leading "/")`
	}
	if strings.HasSuffix(p, "/") {
		return `the path must not end with "/"`
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			return `the path has an empty segment ("//")`
		}
		if seg == "." || seg == ".." {
			return `the path must not contain "." or ".." segments`
		}
		if len(seg) > model.VolumeContentSegmentMaxBytes {
			return fmt.Sprintf("a name is longer than %d bytes", model.VolumeContentSegmentMaxBytes)
		}
	}
	if p == model.VolumeSeedMarker {
		return model.VolumeSeedMarker + " is reserved (the agent's seed marker)"
	}
	return ""
}

// ParentDir "a/b/c" → "a/b"; "a" → "".
func ParentDir(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

// BaseName "a/b/c" → "c".
func BaseName(p string) string { return p[strings.LastIndex(p, "/")+1:] }

// AncestorDirs "a/b/c" → ["a", "a/b"].
func AncestorDirs(p string) []string {
	var out []string
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}

func isUnder(p, dir string) bool { return strings.HasPrefix(p, dir+"/") }

// CompareUTF16 orders strings by UTF-16 code units (as JS sort / JCS).
func CompareUTF16(a, b string) int {
	if a == b {
		return 0
	}
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ua) < len(ub):
		return -1
	case len(ua) > len(ub):
		return 1
	}
	return 0
}

func sortUTF16(s []string) {
	sort.SliceStable(s, func(i, j int) bool { return CompareUTF16(s[i], s[j]) < 0 })
}

// ───────────────────────── canonical tree + hash

// Normalize is normalizeVolumeTree: entries sorted by path (first occurrence wins), missing mode → 0644, missing render
// → none; dirs deduplicated, sorted, only EMPTY folders.
func Normalize(t model.VolumeTree) model.VolumeTree {
	seen := map[string]bool{}
	entries := []model.VolumeTreeEntry{}
	for _, e := range t.Entries {
		if seen[e.Path] {
			continue
		}
		seen[e.Path] = true
		if e.Mode == "" {
			e.Mode = model.DefaultFileMode
		}
		if e.Render == "" {
			e.Render = "none"
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool { return CompareUTF16(entries[i].Path, entries[j].Path) < 0 })
	implied := map[string]bool{}
	for _, e := range entries {
		for _, d := range AncestorDirs(e.Path) {
			implied[d] = true
		}
	}
	var raw []string
	rs := map[string]bool{}
	for _, d := range t.Dirs {
		if d == "" || rs[d] {
			continue
		}
		rs[d] = true
		raw = append(raw, d)
	}
	for _, d := range raw {
		for _, a := range AncestorDirs(d) {
			implied[a] = true
		}
	}
	dirs := []string{}
	for _, d := range raw {
		if !implied[d] && !seen[d] {
			dirs = append(dirs, d)
		}
	}
	sortUTF16(dirs)
	return model.VolumeTree{Entries: entries, Dirs: dirs}
}

// HashInput is volumeTreeHashInput: JCS of the canonical tree.
func HashInput(t model.VolumeTree) string { return canonical.MustCanonicalize(toAny(Normalize(t))) }

// Hash is volumeTreeHash: "sha256:" + hex(sha256(HashInput)).
func Hash(t model.VolumeTree) model.Sha256 { return canonical.Sha256Tagged([]byte(HashInput(t))) }

func toAny(t model.VolumeTree) map[string]any {
	es := make([]any, 0, len(t.Entries))
	for _, e := range t.Entries {
		es = append(es, map[string]any{"path": e.Path, "sha256": e.Sha256, "size": float64(e.Size), "mode": e.Mode, "render": e.Render})
	}
	ds := make([]any, 0, len(t.Dirs))
	for _, d := range t.Dirs {
		ds = append(ds, d)
	}
	return map[string]any{"entries": es, "dirs": ds}
}

// Stats is treeStats.
func Stats(t model.VolumeTree) (files, dirs int, total int64) {
	ds := map[string]bool{}
	for _, d := range t.Dirs {
		ds[d] = true
	}
	for _, e := range t.Entries {
		for _, d := range AncestorDirs(e.Path) {
			ds[d] = true
		}
		total += e.Size
	}
	return len(t.Entries), len(ds), total
}

// TreeIssue is TS TreeIssue.
type TreeIssue struct {
	Path    string
	Code    string // V_VOLUME_CONTENT_PATH | V_DUPLICATE | V_VOLUME_CONTENT | V_FORMAT | V_TOO_MANY | V_TOO_LARGE
	Message string
}

var reSha = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ReFileMode is RE_FILE_MODE.
var ReFileMode = regexp.MustCompile(`^0[0-7]{3}$`)

// TreeIssues is volumeTreeIssues (forSeed: starter-content limits, else version limits). nil Entries / Dirs = missing.
func TreeIssues(t model.VolumeTree, forSeed bool) []TreeIssue {
	if t.Entries == nil || t.Dirs == nil {
		return []TreeIssue{{"", "V_VOLUME_CONTENT", "the content needs entries[] and dirs[]"}}
	}
	var out []TreeIssue
	maxE, maxD := model.VolumeContentVersionMaxEntries, model.VolumeContentVersionMaxDirs
	if forSeed {
		maxE, maxD = model.VolumeContentSeedMaxEntries, model.VolumeContentSeedMaxDirs
	}
	if len(t.Entries) > maxE {
		out = append(out, TreeIssue{"", "V_TOO_MANY", fmt.Sprintf("at most %d files", maxE)})
	}
	if len(t.Dirs) > maxD {
		out = append(out, TreeIssue{"", "V_TOO_MANY", fmt.Sprintf("at most %d empty folders", maxD)})
	}
	files := map[string]bool{}
	var order []string
	var total int64
	for _, e := range t.Entries {
		p := e.Path
		if pe := PathError(p); pe != "" {
			out = append(out, TreeIssue{p, "V_VOLUME_CONTENT_PATH", pe})
			continue
		}
		if files[p] {
			out = append(out, TreeIssue{p, "V_DUPLICATE", p + " is listed twice"})
		} else {
			order = append(order, p)
		}
		files[p] = true
		if !reSha.MatchString(e.Sha256) {
			out = append(out, TreeIssue{p, "V_FORMAT", p + ": upload the content (sha256)"})
		}
		if e.Size < 0 || e.Size > model.VolumeContentFileMaxBytes {
			out = append(out, TreeIssue{p, "V_TOO_LARGE", fmt.Sprintf("%s: a file is 0 B – %d MiB", p, model.VolumeContentFileMaxBytes/1048576)})
		} else {
			total += e.Size
		}
		if !ReFileMode.MatchString(e.Mode) {
			out = append(out, TreeIssue{p, "V_FORMAT", p + ": mode must look like 0644"})
		}
		if e.Render != "none" && e.Render != "expressions" {
			out = append(out, TreeIssue{p, "V_FORMAT", p + ": render must be none | expressions"})
		} else if !forSeed && e.Render == "expressions" {
			out = append(out, TreeIssue{p, "V_VOLUME_CONTENT", p + ": only starter content renders expressions — persisted content is literal (render none)"})
		}
	}
	for _, p := range order {
		for _, d := range AncestorDirs(p) {
			if files[d] {
				out = append(out, TreeIssue{d, "V_VOLUME_CONTENT", d + " is a file, but " + p + " is inside it"})
			}
		}
	}
	dirSet := map[string]bool{}
	for _, d := range t.Dirs {
		if pe := PathError(d); pe != "" {
			out = append(out, TreeIssue{d, "V_VOLUME_CONTENT_PATH", "folder: " + pe})
		} else if files[d] {
			out = append(out, TreeIssue{d, "V_VOLUME_CONTENT", d + " is a file and a folder"})
		} else if dirSet[d] {
			out = append(out, TreeIssue{d, "V_DUPLICATE", "folder " + d + " is listed twice"})
		} else {
			dirSet[d] = true
			for _, a := range AncestorDirs(d) {
				if files[a] {
					out = append(out, TreeIssue{a, "V_VOLUME_CONTENT", a + " is a file, but folder " + d + " is inside it"})
				}
			}
		}
	}
	if forSeed && total > model.VolumeContentSeedMaxTotalBytes {
		out = append(out, TreeIssue{"", "V_TOO_LARGE", fmt.Sprintf("starter content is at most %d MiB in total", model.VolumeContentSeedMaxTotalBytes/1048576)})
	}
	return out
}

// ───────────────────────── excludes (gitignore-lite)

// ExcludePatternError is excludePatternError ("" = valid).
func ExcludePatternError(p string) string {
	if strings.TrimSpace(p) == "" {
		return "an exclude pattern is empty"
	}
	if p != strings.TrimSpace(p) {
		return "an exclude pattern must not start or end with spaces"
	}
	if len(utf16.Encode([]rune(p))) > model.VolumeExcludePatternMax {
		return fmt.Sprintf("an exclude pattern is longer than %d characters", model.VolumeExcludePatternMax)
	}
	if badChars.MatchString(p) {
		return `an exclude pattern holds a control character or "\"`
	}
	if strings.HasPrefix(p, "!") {
		return `negated patterns ("!x") are not supported`
	}
	if strings.ContainsAny(p, "[]") {
		return "character classes ([…]) are not supported"
	}
	body := strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/")
	if body == "" {
		return `an exclude pattern has an empty, "." or ".." segment`
	}
	for _, s := range strings.Split(body, "/") {
		if s == "" || s == "." || s == ".." {
			return `an exclude pattern has an empty, "." or ".." segment`
		}
	}
	return ""
}

// Pattern is one compiled exclude pattern.
type Pattern struct {
	re       *regexp.Regexp
	dirOnly  bool
	anchored bool
}

// Excludes is a compiled exclude list (compileExcludes; always includes "/.opm-seeded").
type Excludes []Pattern

func globToRe(g string, anchored bool) *regexp.Regexp {
	var b strings.Builder
	rs := []rune(g)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '*':
			if i+1 < len(rs) && rs[i+1] == '*' && anchored {
				if i+2 < len(rs) && rs[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
				if i+1 < len(rs) && rs[i+1] == '*' {
					i++
				}
			}
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return regexp.MustCompile("(?s)^" + b.String() + "$")
}

// Compile is compileExcludes.
func Compile(patterns []string) Excludes {
	marker := "/" + model.VolumeSeedMarker
	var out Excludes
	for _, raw := range append([]string{marker}, patterns...) {
		if ExcludePatternError(strings.TrimPrefix(raw, "/")) != "" && raw != marker {
			continue
		}
		dirOnly := strings.HasSuffix(raw, "/")
		body := strings.TrimSuffix(raw, "/")
		anchored := strings.HasPrefix(body, "/") || strings.Contains(body, "/")
		body = strings.TrimPrefix(body, "/")
		out = append(out, Pattern{re: globToRe(body, anchored), dirOnly: dirOnly, anchored: anchored})
	}
	return out
}

func (x Excludes) own(p string, dir bool) bool {
	for _, c := range x {
		if c.dirOnly && !dir {
			continue
		}
		if c.anchored {
			if c.re.MatchString(p) {
				return true
			}
		} else if c.re.MatchString(BaseName(p)) {
			return true
		}
	}
	return false
}

// Excluded is isExcluded: the path (a file, or a folder when isDir) or an ancestor folder is excluded.
func (x Excludes) Excluded(p string, isDir bool) bool {
	for _, a := range AncestorDirs(p) {
		if x.own(a, true) {
			return true
		}
	}
	return x.own(p, isDir)
}

// FilterExcluded is filterExcluded.
func FilterExcluded(t model.VolumeTree, patterns []string) model.VolumeTree {
	c := Compile(patterns)
	out := model.VolumeTree{Entries: []model.VolumeTreeEntry{}, Dirs: []string{}}
	for _, e := range t.Entries {
		if !c.Excluded(e.Path, false) {
			out.Entries = append(out.Entries, e)
		}
	}
	for _, d := range t.Dirs {
		if !c.Excluded(d, true) {
			out.Dirs = append(out.Dirs, d)
		}
	}
	return Normalize(out)
}

// ───────────────────────── 3-way plan

func entryMap(t *model.VolumeTree) map[string]*model.VolumeTreeEntry {
	m := map[string]*model.VolumeTreeEntry{}
	if t == nil {
		return m
	}
	for i := range t.Entries {
		m[t.Entries[i].Path] = &t.Entries[i]
	}
	return m
}

func sideOf(e *model.VolumeTreeEntry) *model.VolumePlanSide {
	if e == nil {
		return nil
	}
	return &model.VolumePlanSide{Sha256: e.Sha256, Size: e.Size, Mode: e.Mode}
}

func sameEntry(a, b *model.VolumeTreeEntry) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Sha256 == b.Sha256 && a.Mode == b.Mode)
}

func opOf(base, x *model.VolumeTreeEntry) string {
	switch {
	case sameEntry(base, x):
		return "none"
	case base == nil:
		return "added"
	case x == nil:
		return "removed"
	}
	return "changed"
}

// Plan is planVolumePush: paths where live ≠ cloud, sorted; base nil = unknown (every difference is a conflict).
func Plan(base *model.VolumeTree, live, cloud model.VolumeTree) []model.VolumePlanItem {
	B, L, C := entryMap(base), entryMap(&live), entryMap(&cloud)
	set := map[string]bool{}
	var paths []string
	for _, m := range []map[string]*model.VolumeTreeEntry{L, C} {
		for p := range m {
			if !set[p] {
				set[p] = true
				paths = append(paths, p)
			}
		}
	}
	sortUTF16(paths)
	out := []model.VolumePlanItem{}
	for _, p := range paths {
		b, l, c := B[p], L[p], C[p]
		if sameEntry(l, c) {
			continue
		}
		var liveOp, cloudOp, kind string
		if base != nil {
			liveOp, cloudOp = opOf(b, l), opOf(b, c)
		} else {
			liveOp, cloudOp = "removed", "removed"
			if l != nil {
				liveOp = "changed"
			}
			if c != nil {
				cloudOp = "changed"
			}
		}
		switch {
		case base == nil:
			kind = "conflict"
		case liveOp == "none":
			kind = "cloud"
		case cloudOp == "none":
			kind = "live"
		default:
			kind = "conflict"
		}
		out = append(out, model.VolumePlanItem{Path: p, Kind: kind, LiveOp: liveOp, CloudOp: cloudOp, Base: sideOf(b), Live: sideOf(l), Cloud: sideOf(c)})
	}
	return out
}

// PlanCounts counts plan items by kind (all four kinds present).
func PlanCounts(items []model.VolumePlanItem) map[string]int {
	c := map[string]int{"cloud": 0, "live": 0, "same": 0, "conflict": 0}
	for _, it := range items {
		c[it.Kind]++
	}
	return c
}

// Merge is mergeVolumePush: the target tree of an apply (resolution overwrite | keep-live | per-file).
func Merge(base *model.VolumeTree, live, cloud model.VolumeTree, resolution string, choices map[string]string) (model.VolumeTree, error) {
	switch resolution {
	case "overwrite":
		return Normalize(cloud), nil
	case "keep-live":
		return Normalize(live), nil
	case "per-file":
	default:
		return model.VolumeTree{}, fmt.Errorf("resolution must be overwrite | keep-live | per-file")
	}
	plan := Plan(base, live, cloud)
	out := map[string]model.VolumeTreeEntry{}
	for _, e := range live.Entries {
		out[e.Path] = e
	}
	C := entryMap(&cloud)
	for _, it := range plan {
		take := "live"
		if it.Kind == "cloud" {
			take = "cloud"
		} else if it.Kind == "conflict" {
			take = choices[it.Path]
		}
		if take != "cloud" && take != "live" {
			return model.VolumeTree{}, fmt.Errorf("conflict on %s: choose cloud or live", it.Path)
		}
		if take == "cloud" {
			if c := C[it.Path]; c != nil {
				out[it.Path] = *c
			} else {
				delete(out, it.Path)
			}
		}
	}
	bd, ld, cd := map[string]bool{}, map[string]bool{}, map[string]bool{}
	if base != nil {
		for _, d := range base.Dirs {
			bd[d] = true
		}
	}
	var all []string
	for _, d := range live.Dirs {
		if !ld[d] {
			all = append(all, d)
		}
		ld[d] = true
	}
	for _, d := range cloud.Dirs {
		if !ld[d] && !cd[d] {
			all = append(all, d)
		}
		cd[d] = true
	}
	var dirs []string
	for _, d := range all {
		inBase := bd[d]
		keepLive := ld[d] && !(inBase && !cd[d])
		addCloud := cd[d] && !(inBase && !ld[d])
		if keepLive || addCloud {
			dirs = append(dirs, d)
		}
	}
	entries := make([]model.VolumeTreeEntry, 0, len(out))
	for _, e := range out {
		entries = append(entries, e)
	}
	return Normalize(model.VolumeTree{Entries: entries, Dirs: dirs}), nil
}

// TreeChange is one item of diffVolumeTrees.
type TreeChange struct {
	Path   string
	Kind   string // added | removed | changed
	Before *model.VolumeTreeEntry
	After  *model.VolumeTreeEntry
}

// DiffTrees is diffVolumeTrees (content, mode or render).
func DiffTrees(a, b *model.VolumeTree) []TreeChange {
	A, B := entryMap(a), entryMap(b)
	set := map[string]bool{}
	var paths []string
	for _, m := range []map[string]*model.VolumeTreeEntry{A, B} {
		for p := range m {
			if !set[p] {
				set[p] = true
				paths = append(paths, p)
			}
		}
	}
	sortUTF16(paths)
	var out []TreeChange
	for _, p := range paths {
		x, y := A[p], B[p]
		if x != nil && y != nil && x.Sha256 == y.Sha256 && x.Mode == y.Mode && x.Render == y.Render {
			continue
		}
		k := "changed"
		if x == nil {
			k = "added"
		} else if y == nil {
			k = "removed"
		}
		out = append(out, TreeChange{Path: p, Kind: k, Before: x, After: y})
	}
	return out
}

// FormatBytes is TS formatContentBytes ("512 B", "1.5 KiB", "12 MiB", …).
func FormatBytes(n int64) string {
	f := float64(n)
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1048576:
		if n < 10240 {
			return fmt.Sprintf("%.1f KiB", f/1024)
		}
		return fmt.Sprintf("%.0f KiB", f/1024)
	case n < 1073741824:
		if n < 10485760 {
			return fmt.Sprintf("%.1f MiB", f/1048576)
		}
		return fmt.Sprintf("%.0f MiB", f/1048576)
	}
	return fmt.Sprintf("%.1f GiB", f/1073741824)
}
