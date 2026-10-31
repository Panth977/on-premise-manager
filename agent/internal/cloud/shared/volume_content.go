package shared

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud/doc"
)

// VOLUME_CONTENT_LIMITS is VOLUME_CONTENT_LIMITS.
var VOLUME_CONTENT_LIMITS = struct {
	FileMaxBytes, SnapshotFileMaxBytes, SeedMaxEntries, SeedMaxDirs, SeedMaxTotalBytes, VersionMaxEntries, VersionMaxDirs, EditorTextMaxBytes, PathMaxBytes, SegmentMaxBytes, PersistMinBytes, PersistMaxBytes, PersistDefaultBytes, ExcludeMax, ExcludePatternMax, NoteMax, VersionsKept int
}{50 * 1024 * 1024, 24 * 1024 * 1024, 1000, 1000, 200 * 1024 * 1024, 10000, 5000, 2 * 1024 * 1024, 1024, 255, 1024 * 1024, 200 * 1024 * 1024, 50 * 1024 * 1024, 50, 200, 500, 200}

// Value exports of shared/src/volume-content.ts.
const (
	// VOLUME_SEED_MARKER is VOLUME_SEED_MARKER.
	VOLUME_SEED_MARKER = ".opm-seeded"
	// VOLUME_CONTENT_MIN_AGENT is VOLUME_CONTENT_MIN_AGENT.
	VOLUME_CONTENT_MIN_AGENT = "0.12.0"
	// VOLUME_CONTENT_COMMIT_MIN_AGENT is VOLUME_CONTENT_COMMIT_MIN_AGENT.
	VOLUME_CONTENT_COMMIT_MIN_AGENT = "0.19.0"
	// VOLUME_SYNC_SCAN_SEC is VOLUME_SYNC_SCAN_SEC.
	VOLUME_SYNC_SCAN_SEC = 60
	// VOLUME_SYNC_QUIET_SEC is VOLUME_SYNC_QUIET_SEC.
	VOLUME_SYNC_QUIET_SEC = 120
	// VOLUME_SYNC_MIN_INTERVAL_SEC is VOLUME_SYNC_MIN_INTERVAL_SEC.
	VOLUME_SYNC_MIN_INTERVAL_SEC = 900
	// VOLUME_CONTENT_APPLY_WAIT_SEC is VOLUME_CONTENT_APPLY_WAIT_SEC.
	VOLUME_CONTENT_APPLY_WAIT_SEC = 60
	// DEFAULT_FILE_MODE is DEFAULT_FILE_MODE.
	DEFAULT_FILE_MODE = "0644"
)

// VOLUME_ALWAYS_EXCLUDED is VOLUME_ALWAYS_EXCLUDED.
var VOLUME_ALWAYS_EXCLUDED = []string{VOLUME_SEED_MARKER}

// VOLUME_PERSIST_DEFAULT_EXCLUDE is VOLUME_PERSIST_DEFAULT_EXCLUDE.
var VOLUME_PERSIST_DEFAULT_EXCLUDE = []string{"node_modules/", ".npm/", ".cache/", "*.log"}

// RE_FILE_MODE is RE_FILE_MODE.
var RE_FILE_MODE = regexp.MustCompile(`^0[0-7]{3}$`)

// VOLUME_SYNC_STATE_LABEL is VOLUME_SYNC_STATE_LABEL.
var VOLUME_SYNC_STATE_LABEL = map[string]string{"in-sync": "In sync", "live-changed": "Live changed", "cloud-ahead": "Cloud ahead", "conflict": "Conflict", "never-snapshotted": "No snapshot yet", "pending-upload": "Waiting to upload", "error": "Error"}

// EMPTY_VOLUME_TREE is EMPTY_VOLUME_TREE (a fresh object).
func EMPTY_VOLUME_TREE() *doc.Map { return doc.Obj("entries", []any{}, "dirs", []any{}) }

func jStrs(s []string) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

// DefaultVolumePersist is defaultVolumePersist.
func DefaultVolumePersist() *doc.Map {
	return doc.Obj("exclude", jStrs(VOLUME_PERSIST_DEFAULT_EXCLUDE), "maxBytes", float64(VOLUME_CONTENT_LIMITS.PersistDefaultBytes), "auto", true)
}

// VolumePersist is volumePersist (nil = null).
func VolumePersist(v any) any { return jCoal(jOpt(v, "persist"), nil) }

// UsesVolumeContent is usesVolumeContent.
func UsesVolumeContent(sc any) (uses bool, err error) {
	defer jCatch(&err)
	for _, v := range jArr(jCoal(jOpt(sc, "volumes"), []any{}), "some", "((intermediate value) ?? [])") {
		if doc.Truthy(v) && (doc.Truthy(jGet(v, "persist")) || jIsOneOf(jOpt(jGet(v, "seed"), "kind"), "tree", "single")) {
			return true, nil
		}
	}
	return false, nil
}

// IsLegacyFileSeed is isLegacyFileSeed.
func IsLegacyFileSeed(seed any) bool {
	return doc.Truthy(seed) && jIsOneOf(jGet(seed, "kind"), "files", "file")
}

func jBadChar(p string) bool {
	return strings.IndexFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' }) >= 0
}

// ContentPathError is contentPathError (nil = valid).
func ContentPathError(p any) *string {
	e := func(s string) *string { return &s }
	s, ok := p.(string)
	if !ok || s == "" {
		return e("the path is empty")
	}
	L := VOLUME_CONTENT_LIMITS
	if len(s) > L.PathMaxBytes {
		return e("the path is longer than " + strconv.Itoa(L.PathMaxBytes) + " bytes")
	}
	if jBadChar(s) {
		return e(`the path holds a control character or "\"`)
	}
	if strings.HasPrefix(s, "/") {
		return e(`the path must be relative (no leading "/")`)
	}
	if strings.HasSuffix(s, "/") {
		return e(`the path must not end with "/"`)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" {
			return e(`the path has an empty segment ("//")`)
		}
		if seg == "." || seg == ".." {
			return e(`the path must not contain "." or ".." segments`)
		}
		if len(seg) > L.SegmentMaxBytes {
			return e("a name is longer than " + strconv.Itoa(L.SegmentMaxBytes) + " bytes")
		}
	}
	if s == VOLUME_SEED_MARKER {
		return e(VOLUME_SEED_MARKER + " is reserved (the agent's seed marker)")
	}
	return nil
}

// ParentDir is parentDir.
func ParentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// BaseName is baseName.
func BaseName(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

// JoinPath is joinPath.
func JoinPath(dir, name string) string {
	if dir != "" {
		return dir + "/" + name
	}
	return name
}

// AncestorDirs is ancestorDirs.
func AncestorDirs(p string) []string {
	out := jAncestorDirs(p)
	if out == nil {
		return []string{}
	}
	return out
}

func jIsUnder(p, dir string) bool { return strings.HasPrefix(p, dir+"/") }

// VolumeTreeHashInput is volumeTreeHashInput.
func VolumeTreeHashInput(t any) (s string, err error) {
	defer jCatch(&err)
	raw, err := doc.Marshal(jNormalizeVolumeTree(t))
	if err != nil {
		return "", err
	}
	return canonical.CanonicalizeJSON(raw)
}

// VolumeTreeHash is volumeTreeHash ("sha256:" + hex).
func VolumeTreeHash(t any) (string, error) {
	s, err := VolumeTreeHashInput(t)
	if err != nil {
		return "", err
	}
	return canonical.Sha256Tagged([]byte(s)), nil
}

func jEntries(t *doc.Map) []*doc.Map {
	out := []*doc.Map{}
	for _, e := range t.Arr("entries") {
		if m, ok := e.(*doc.Map); ok {
			out = append(out, m)
		}
	}
	return out
}

// TreeStats is treeStats: { fileCount, dirCount, totalBytes }.
func TreeStats(t *doc.Map) *doc.Map {
	dirs := map[string]bool{}
	for _, d := range t.Strs("dirs") {
		dirs[d] = true
	}
	total := 0.0
	for _, e := range jEntries(t) {
		for _, d := range jAncestorDirs(e.Str("path")) {
			dirs[d] = true
		}
		total += e.Num("size")
	}
	return doc.Obj("fileCount", float64(len(t.Arr("entries"))), "dirCount", float64(len(dirs)), "totalBytes", total)
}

// VolumeTreeIssues is volumeTreeIssues: [{ path, code, message }].
func VolumeTreeIssues(t any, forSeed bool) (out []any, err error) {
	defer jCatch(&err)
	out = []any{}
	issue := func(path, code, message string) {
		out = append(out, doc.Obj("path", path, "code", code, "message", message))
	}
	entries, ok1 := jOpt(t, "entries").([]any)
	dirs, ok2 := jOpt(t, "dirs").([]any)
	if !doc.Truthy(t) || !jIsObj(t) || !ok1 || !ok2 {
		issue("", "V_VOLUME_CONTENT", "the content needs entries[] and dirs[]")
		return out, nil
	}
	L := VOLUME_CONTENT_LIMITS
	maxE, maxD := L.VersionMaxEntries, L.VersionMaxDirs
	if forSeed {
		maxE, maxD = L.SeedMaxEntries, L.SeedMaxDirs
	}
	if len(entries) > maxE {
		issue("", "V_TOO_MANY", "at most "+strconv.Itoa(maxE)+" files")
	}
	if len(dirs) > maxD {
		issue("", "V_TOO_MANY", "at most "+strconv.Itoa(maxD)+" empty folders")
	}
	files := map[string]bool{}
	var order []string
	total := 0.0
	for _, e := range entries {
		pv := jOpt(e, "path")
		if pe := ContentPathError(pv); pe != nil {
			issue(jStr(jCoal(pv, "")), "V_VOLUME_CONTENT_PATH", *pe)
			continue
		}
		p := pv.(string)
		if files[p] {
			issue(p, "V_DUPLICATE", p+" is listed twice")
		} else {
			order = append(order, p)
		}
		files[p] = true
		if s, ok := jGet(e, "sha256").(string); !ok || !jReSha.MatchString(s) {
			issue(p, "V_FORMAT", p+": upload the content (sha256)")
		}
		n, _ := jGet(e, "size").(float64)
		if !jIsInt(jGet(e, "size")) || n < 0 || n > float64(L.FileMaxBytes) {
			issue(p, "V_TOO_LARGE", p+": a file is 0 B – "+strconv.Itoa(L.FileMaxBytes/1048576)+" MiB")
		} else {
			total += n
		}
		if m, ok := jGet(e, "mode").(string); !ok || !RE_FILE_MODE.MatchString(m) {
			issue(p, "V_FORMAT", p+": mode must look like 0644")
		}
		if r := jGet(e, "render"); !jIsOneOf(r, "none", "expressions") {
			issue(p, "V_FORMAT", p+": render must be none | expressions")
		} else if !forSeed && jIs(r, "expressions") {
			issue(p, "V_VOLUME_CONTENT", p+": only starter content renders expressions — persisted content is literal (render none)")
		}
	}
	for _, p := range order {
		for _, d := range jAncestorDirs(p) {
			if files[d] {
				issue(d, "V_VOLUME_CONTENT", d+" is a file, but "+p+" is inside it")
			}
		}
	}
	dirSet := map[string]bool{}
	for _, dv := range dirs {
		pe := ContentPathError(dv)
		if pe != nil {
			issue(jStr(jCoal(dv, "")), "V_VOLUME_CONTENT_PATH", "folder: "+*pe)
			continue
		}
		d := dv.(string)
		switch {
		case files[d]:
			issue(d, "V_VOLUME_CONTENT", d+" is a file and a folder")
		case dirSet[d]:
			issue(d, "V_DUPLICATE", "folder "+d+" is listed twice")
		default:
			dirSet[d] = true
			for _, a := range jAncestorDirs(d) {
				if files[a] {
					issue(a, "V_VOLUME_CONTENT", a+" is a file, but folder "+d+" is inside it")
				}
			}
		}
	}
	if forSeed && total > float64(L.SeedMaxTotalBytes) {
		issue("", "V_TOO_LARGE", "starter content is at most "+strconv.Itoa(L.SeedMaxTotalBytes/1048576)+" MiB in total")
	}
	return out, nil
}

// ExcludePatternError is excludePatternError (nil = valid).
func ExcludePatternError(p any) *string {
	e := func(s string) *string { return &s }
	s, ok := p.(string)
	if !ok || doc.JSTrim(s) == "" {
		return e("an exclude pattern is empty")
	}
	if s != doc.JSTrim(s) {
		return e("an exclude pattern must not start or end with spaces")
	}
	L := VOLUME_CONTENT_LIMITS
	if doc.JSLen(s) > L.ExcludePatternMax {
		return e("an exclude pattern is longer than " + strconv.Itoa(L.ExcludePatternMax) + " characters")
	}
	if jBadChar(s) {
		return e(`an exclude pattern holds a control character or "\"`)
	}
	if strings.HasPrefix(s, "!") {
		return e(`negated patterns ("!x") are not supported`)
	}
	if strings.ContainsAny(s, "[]") {
		return e("character classes ([…]) are not supported")
	}
	body := strings.TrimSuffix(strings.TrimPrefix(s, "/"), "/")
	if body == "" || slices.ContainsFunc(strings.Split(body, "/"), func(x string) bool { return x == "" || x == "." || x == ".." }) {
		return e(`an exclude pattern has an empty, "." or ".." segment`)
	}
	return nil
}

// CompiledPattern is one item of compileExcludes.
type CompiledPattern struct {
	Re       *regexp.Regexp
	DirOnly  bool
	Anchored bool
}

func jGlobToRe(g string, anchored bool) *regexp.Regexp {
	var s strings.Builder
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && i+1 < len(g) && g[i+1] == '*' && anchored:
			if i+2 < len(g) && g[i+2] == '/' {
				s.WriteString(`(?:.*/)?`)
				i += 2
			} else {
				s.WriteString(`.*`)
				i++
			}
		case c == '*':
			s.WriteString(`[^/]*`)
			if i+1 < len(g) && g[i+1] == '*' {
				i++
			}
		case c == '?':
			s.WriteString(`[^/]`)
		default:
			_, n := utf8.DecodeRuneInString(g[i:])
			s.WriteString(regexp.QuoteMeta(g[i : i+n]))
			i += n - 1
		}
	}
	return regexp.MustCompile(`(?s)^` + s.String() + `$`)
}

// CompileExcludes is compileExcludes.
func CompileExcludes(patterns []string) []CompiledPattern {
	out := []CompiledPattern{}
	for _, raw := range append([]string{"/" + VOLUME_SEED_MARKER}, patterns...) {
		if ExcludePatternError(strings.TrimPrefix(raw, "/")) != nil && raw != "/"+VOLUME_SEED_MARKER {
			continue
		}
		body := strings.TrimSuffix(raw, "/")
		anchored := strings.Contains(body, "/")
		out = append(out, CompiledPattern{jGlobToRe(strings.TrimPrefix(body, "/"), anchored), strings.HasSuffix(raw, "/"), anchored})
	}
	return out
}

// IsExcluded is isExcluded.
func IsExcluded(path string, isDir bool, compiled []CompiledPattern) bool {
	own := func(p string, dir bool) bool {
		for _, c := range compiled {
			if c.DirOnly && !dir {
				continue
			}
			if c.Anchored && c.Re.MatchString(p) || !c.Anchored && c.Re.MatchString(BaseName(p)) {
				return true
			}
		}
		return false
	}
	for _, a := range jAncestorDirs(path) {
		if own(a, true) {
			return true
		}
	}
	return own(path, isDir)
}

// FilterExcluded is filterExcluded.
func FilterExcluded(t *doc.Map, patterns []string) *doc.Map {
	c := CompileExcludes(patterns)
	entries, dirs := []any{}, []any{}
	for _, e := range jEntries(t) {
		if !IsExcluded(e.Str("path"), false, c) {
			entries = append(entries, e)
		}
	}
	for _, d := range t.Strs("dirs") {
		if !IsExcluded(d, true, c) {
			dirs = append(dirs, d)
		}
	}
	return jNormalizeVolumeTree(doc.Obj("entries", entries, "dirs", dirs))
}

func jEntryMap(t *doc.Map) map[string]*doc.Map {
	out := map[string]*doc.Map{}
	for _, e := range jEntries(t) {
		out[e.Str("path")] = e
	}
	return out
}

func jSide(e *doc.Map) any {
	if e == nil {
		return nil
	}
	return doc.Obj("sha256", e.Val("sha256"), "size", e.Val("size"), "mode", e.Val("mode"))
}

func jSameEntry(a, b *doc.Map) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return jEq(a.Val("sha256"), b.Val("sha256")) && jEq(a.Val("mode"), b.Val("mode"))
}

func jOpOf(base, x *doc.Map) string {
	switch {
	case jSameEntry(base, x):
		return "none"
	case base == nil:
		return "added"
	case x == nil:
		return "removed"
	}
	return "changed"
}

func jSortedPaths(ms ...map[string]*doc.Map) []string {
	var paths []string
	seen := map[string]bool{}
	for _, m := range ms {
		for p := range m {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	slices.SortFunc(paths, jCmp16)
	return paths
}

// PlanVolumePush is planVolumePush (base nil = unknown).
func PlanVolumePush(base, live, cloud *doc.Map) []any {
	B, L, C := jEntryMap(base), jEntryMap(live), jEntryMap(cloud)
	out := []any{}
	for _, p := range jSortedPaths(L, C) {
		b, l, c := B[p], L[p], C[p]
		if jSameEntry(l, c) {
			continue
		}
		var liveOp, cloudOp, kind string
		if base != nil {
			liveOp, cloudOp = jOpOf(b, l), jOpOf(b, c)
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
		out = append(out, doc.Obj("path", p, "kind", kind, "liveOp", liveOp, "cloudOp", cloudOp, "base", jSide(b), "live", jSide(l), "cloud", jSide(c)))
	}
	return out
}

// MergeVolumePush is mergeVolumePush.
func MergeVolumePush(base, live, cloud *doc.Map, resolution string, choices *doc.Map) (*doc.Map, error) {
	if resolution == "overwrite" {
		return jNormalizeVolumeTree(cloud), nil
	}
	if resolution == "keep-live" {
		return jNormalizeVolumeTree(live), nil
	}
	out := jEntryMap(live)
	C := jEntryMap(cloud)
	for _, x := range PlanVolumePush(base, live, cloud) {
		it := x.(*doc.Map)
		p, take := it.Str("path"), "live"
		switch it.Str("kind") {
		case "cloud":
			take = "cloud"
		case "conflict":
			take = choices.Str(p)
		}
		if take != "cloud" && take != "live" {
			return nil, errors.New("conflict on " + p + ": choose cloud or live")
		}
		if take == "cloud" {
			if c := C[p]; c != nil {
				out[p] = c
			} else {
				delete(out, p)
			}
		}
	}
	has := func(t *doc.Map) map[string]bool {
		m := map[string]bool{}
		for _, d := range t.Strs("dirs") {
			m[d] = true
		}
		return m
	}
	bd, ld, cd := has(base), has(live), has(cloud)
	dirs := []any{}
	for _, list := range [][]string{live.Strs("dirs"), cloud.Strs("dirs")} {
		for _, d := range list {
			if ld[d] && !(bd[d] && !cd[d]) || cd[d] && !(bd[d] && !ld[d]) {
				dirs = append(dirs, d)
			}
		}
	}
	entries := []any{}
	for _, p := range jSortedPaths(out) {
		entries = append(entries, out[p])
	}
	return jNormalizeVolumeTree(doc.Obj("entries", entries, "dirs", dirs)), nil
}

// DiffVolumeTrees is diffVolumeTrees: [{ path, kind, before, after }].
func DiffVolumeTrees(a, b *doc.Map) []any {
	A, B := jEntryMap(a), jEntryMap(b)
	out := []any{}
	val := func(m *doc.Map) any {
		if m == nil {
			return nil
		}
		return m
	}
	for _, p := range jSortedPaths(A, B) {
		x, y := A[p], B[p]
		if x != nil && y != nil && jSameEntry(x, y) && jEq(x.Val("render"), y.Val("render")) {
			continue
		}
		kind := "changed"
		if x == nil {
			kind = "added"
		} else if y == nil {
			kind = "removed"
		}
		out = append(out, doc.Obj("path", p, "kind", kind, "before", val(x), "after", val(y)))
	}
	return out
}

// CountTreeChanges is countTreeChanges: { added, removed, modified }.
func CountTreeChanges(a, b *doc.Map) *doc.Map {
	c := doc.Obj("added", 0.0, "removed", 0.0, "modified", 0.0)
	for _, d := range DiffVolumeTrees(a, b) {
		k := d.(*doc.Map).Str("kind")
		if k == "changed" {
			k = "modified"
		}
		c.Set(k, c.Num(k)+1)
	}
	return c
}

// TreeHasFile is treeHasFile.
func TreeHasFile(t *doc.Map, p string) bool {
	return slices.ContainsFunc(jEntries(t), func(e *doc.Map) bool { return e.Str("path") == p })
}

// TreeHasDir is treeHasDir.
func TreeHasDir(t *doc.Map, p string) bool {
	return slices.Contains(t.Strs("dirs"), p) || slices.ContainsFunc(jEntries(t), func(e *doc.Map) bool { return jIsUnder(e.Str("path"), p) }) ||
		slices.ContainsFunc(t.Strs("dirs"), func(d string) bool { return jIsUnder(d, p) })
}

func jAssertFree(t *doc.Map, p string) {
	if pe := ContentPathError(p); pe != nil {
		jThrow("%s", *pe)
	}
	if TreeHasFile(t, p) || TreeHasDir(t, p) {
		jThrow("%s already exists", p)
	}
	for _, a := range jAncestorDirs(p) {
		if TreeHasFile(t, a) {
			jThrow("%s is a file", a)
		}
	}
}

// TreePutFile is treePutFile (the error is a TreeEditError message).
func TreePutFile(t, e *doc.Map, overwrite bool) (out *doc.Map, err error) {
	defer jCatch(&err)
	p := e.Str("path")
	entries := []any{}
	found := false
	for _, x := range jEntries(t) {
		if x.Str("path") == p && !found {
			found = true
			if !overwrite {
				jThrow("%s already exists", p)
			}
			entries = append(entries, e.Shallow())
			continue
		}
		entries = append(entries, x)
	}
	if !found {
		jAssertFree(t, p)
		entries = append(entries, e.Shallow())
	}
	return jNormalizeVolumeTree(doc.Obj("entries", entries, "dirs", t.Val("dirs"))), nil
}

// TreeMkdir is treeMkdir.
func TreeMkdir(t *doc.Map, p string) (out *doc.Map, err error) {
	defer jCatch(&err)
	jAssertFree(t, p)
	return jNormalizeVolumeTree(doc.Obj("entries", t.Val("entries"), "dirs", append(slices.Clone(t.Arr("dirs")), p))), nil
}

// TreeDelete is treeDelete.
func TreeDelete(t *doc.Map, p string) *doc.Map {
	keepParent := ParentDir(p)
	entries, dirs := []any{}, []any{}
	for _, e := range jEntries(t) {
		if x := e.Str("path"); x != p && !jIsUnder(x, p) {
			entries = append(entries, e)
		}
	}
	for _, d := range t.Strs("dirs") {
		if d != p && !jIsUnder(d, p) {
			dirs = append(dirs, d)
		}
	}
	n := jNormalizeVolumeTree(doc.Obj("entries", entries, "dirs", dirs))
	if keepParent != "" && !TreeHasDir(n, keepParent) {
		return jNormalizeVolumeTree(doc.Obj("entries", n.Val("entries"), "dirs", append(slices.Clone(n.Arr("dirs")), keepParent)))
	}
	return n
}

// TreeMove is treeMove.
func TreeMove(t *doc.Map, from, to string) (out *doc.Map, err error) {
	defer jCatch(&err)
	if from == to {
		return t, nil
	}
	if jIsUnder(to, from) {
		jThrow("a folder cannot be moved into itself")
	}
	isFile := TreeHasFile(t, from)
	if !isFile && !TreeHasDir(t, from) {
		jThrow("%s does not exist", from)
	}
	rest := TreeDelete(t, from)
	jAssertFree(rest, to)
	re := func(p string) string {
		if p == from {
			return to
		}
		if jIsUnder(p, from) {
			return to + p[len(from):]
		}
		return p
	}
	entries, dirs := []any{}, []any{}
	for _, e := range jEntries(t) {
		if x := e.Str("path"); x == from || jIsUnder(x, from) {
			entries = append(entries, e.Shallow().Set("path", re(x)))
		}
	}
	for _, d := range t.Strs("dirs") {
		if d == from || jIsUnder(d, from) {
			dirs = append(dirs, re(d))
		}
	}
	if !isFile && len(entries) == 0 && len(dirs) == 0 {
		dirs = append(dirs, to)
	}
	return jNormalizeVolumeTree(doc.Obj("entries", append(slices.Clone(rest.Arr("entries")), entries...), "dirs", append(slices.Clone(rest.Arr("dirs")), dirs...))), nil
}

// DuplicateName is duplicateName.
func DuplicateName(t *doc.Map, p string) string {
	dir, name := ParentDir(p), BaseName(p)
	stem, ext := name, ""
	if TreeHasFile(t, p) {
		if dot := strings.LastIndexByte(name, '.'); dot > 0 {
			stem, ext = name[:dot], name[dot:]
		}
	}
	for i := 1; ; i++ {
		n := ""
		if i != 1 {
			n = " " + strconv.Itoa(i)
		}
		cand := JoinPath(dir, stem+" copy"+n+ext)
		if !TreeHasFile(t, cand) && !TreeHasDir(t, cand) {
			return cand
		}
	}
}

// TreeDuplicate is treeDuplicate ("" for to = duplicateName).
func TreeDuplicate(t *doc.Map, p, to string) (out *doc.Map, err error) {
	defer jCatch(&err)
	if to == "" {
		to = DuplicateName(t, p)
	}
	jAssertFree(t, to)
	if TreeHasFile(t, p) {
		src := jFind(t.Arr("entries"), func(e any) bool { return jIs(jGet(e, "path"), p) }).(*doc.Map)
		return TreePutFile(t, src.Shallow().Set("path", to), false)
	}
	if !TreeHasDir(t, p) {
		jThrow("%s does not exist", p)
	}
	re := func(x string) string { return to + x[len(p):] }
	entries := slices.Clone(t.Arr("entries"))
	for _, e := range jEntries(t) {
		if jIsUnder(e.Str("path"), p) {
			entries = append(entries, e.Shallow().Set("path", re(e.Str("path"))))
		}
	}
	dirs := slices.Clone(t.Arr("dirs"))
	had := false
	for _, d := range t.Strs("dirs") {
		had = had || d == p
		if d == p || jIsUnder(d, p) {
			dirs = append(dirs, re(d))
		}
	}
	if !had {
		dirs = append(dirs, to)
	}
	return jNormalizeVolumeTree(doc.Obj("entries", entries, "dirs", dirs)), nil
}

// TreeNodes is treeNodes: [{ name, path, dir, entry, children }].
func TreeNodes(t *doc.Map) []any {
	type node struct {
		name, path string
		dir        bool
		entry      any
		children   []*node
	}
	root := &node{dir: true}
	dirs := map[string]*node{"": root}
	var ensure func(d string) *node
	ensure = func(d string) *node {
		if n := dirs[d]; n != nil {
			return n
		}
		n := &node{name: BaseName(d), path: d, dir: true}
		dirs[d] = n
		p := ensure(ParentDir(d))
		p.children = append(p.children, n)
		return n
	}
	for _, d := range t.Strs("dirs") {
		ensure(d)
	}
	for _, e := range jEntries(t) {
		p := ensure(ParentDir(e.Str("path")))
		p.children = append(p.children, &node{name: BaseName(e.Str("path")), path: e.Str("path"), entry: e})
	}
	var conv func(n *node) []any
	conv = func(n *node) []any {
		slices.SortStableFunc(n.children, func(a, b *node) int {
			if a.dir != b.dir {
				if a.dir {
					return -1
				}
				return 1
			}
			return jCmp16(a.name, b.name)
		})
		out := make([]any, len(n.children))
		for i, c := range n.children {
			out[i] = doc.Obj("name", c.name, "path", c.path, "dir", c.dir, "entry", c.entry, "children", conv(c))
		}
		return out
	}
	return conv(root)
}

var jLangExt = map[string]string{
	"json": "json", "jsonc": "json", "json5": "json", "map": "json",
	"js": "javascript", "mjs": "javascript", "cjs": "javascript", "jsx": "javascript",
	"ts": "typescript", "mts": "typescript", "cts": "typescript", "tsx": "typescript",
	"yml": "yaml", "yaml": "yaml",
	"html": "html", "htm": "html", "svelte": "html", "vue": "html",
	"css": "css", "scss": "css", "less": "css",
	"md": "markdown", "markdown": "markdown",
	"xml": "xml", "svg": "xml",
	"py":  "python",
	"sql": "sql",
	"sh":  "shell", "bash": "shell", "zsh": "shell",
	"conf": "ini", "cfg": "ini", "ini": "ini", "toml": "ini", "env": "ini", "properties": "ini",
}

// LanguageForPath is languageForPath (nil = null).
func LanguageForPath(p string) *string {
	n := jLower(BaseName(p))
	ext := ""
	if i := strings.LastIndexByte(n, '.'); i >= 0 {
		ext = n[i+1:]
	}
	var lang string
	switch {
	case n == ".babelrc" || n == ".eslintrc":
		lang = "json"
	case jLangExt[ext] != "":
		lang = jLangExt[ext]
	case n == "dockerfile":
		lang = "shell"
	case strings.HasPrefix(n, ".env"):
		lang = "ini"
	default:
		return nil
	}
	return &lang
}

// LooksLikeText is looksLikeText.
func LooksLikeText(b []byte) bool {
	head := b[:min(len(b), 8192)]
	if slices.Contains(head, 0) {
		return false
	}
	if len(head) < len(b) {
		i, n := len(head)-1, 0
		for i >= 0 && n < 4 && head[i]&0xc0 == 0x80 {
			i--
			n++
		}
		if i >= 0 && head[i] >= 0xc0 {
			head = head[:i]
		}
	}
	return utf8.Valid(head)
}

// FormatMode is formatMode.
func FormatMode(mode string) string {
	if !RE_FILE_MODE.MatchString(mode) {
		return mode
	}
	var s strings.Builder
	for _, c := range mode[1:] {
		b := c - '0'
		for i, l := range "rwx" {
			if b&(4>>i) != 0 {
				s.WriteRune(l)
			} else {
				s.WriteByte('-')
			}
		}
	}
	return s.String()
}

// FormatContentBytes is formatContentBytes.
func FormatContentBytes(n float64) string {
	d := func(unit float64, limit float64) int {
		if n < limit {
			return 1
		}
		return 0
	}
	switch {
	case n < 1024:
		return jNum(n) + " B"
	case n < 1048576:
		return jFixed(n/1024, d(1024, 10240)) + " KiB"
	case n < 1073741824:
		return jFixed(n/1048576, d(1048576, 10485760)) + " MiB"
	}
	return jFixed(n/1073741824, 1) + " GiB"
}

// CloudSyncState is cloudSyncState.
func CloudSyncState(s *doc.Map) string {
	live, snap := s.Map("live"), s.Map("lastSnapshot")
	var pending *doc.Map
	if push := s.Map("push"); push.Str("status") == "pending" {
		pending = push
	}
	liveHash := jCoal(live.Val("liveTreeHash"), jCoal(snap.Val("treeHash"), nil))
	if pending != nil {
		if pending.Truthy("baseTreeHash") && jEq(liveHash, pending.Val("baseTreeHash")) {
			return "cloud-ahead"
		}
		if jEq(liveHash, pending.Val("treeHash")) {
			return "in-sync"
		}
		if live.Str("state") == "cloud-ahead" && jEq(live.Val("liveTreeHash"), liveHash) {
			return "cloud-ahead"
		}
		return "conflict"
	}
	switch {
	case snap == nil && !live.Truthy("snapshotTreeHash"):
		return "never-snapshotted"
	case live.Str("state") == "error":
		return "error"
	case live.Truthy("queued"):
		return "pending-upload"
	case live.Truthy("liveChanged"):
		return "live-changed"
	}
	return "in-sync"
}

// MigrateVolumeFileSeeds is migrateVolumeFileSeeds: { config, changed, lines, notes, removedFiles, moved }; imageVolumes
// returns the image's volumes ({ name, mountPath }), nil = unknown.
func MigrateVolumeFileSeeds(config *doc.Map, imageVolumes func(serviceSlug, version string) []any) (result *doc.Map, err error) {
	defer jCatch(&err)
	cfg := config.Clone()
	lines, notes, moved := []any{}, []any{}, []any{}
	var touched []string
	touch := func(s string) {
		if !slices.Contains(touched, s) {
			touched = append(touched, s)
		}
	}
	fileOf := func(slug string) *doc.Map {
		for _, f := range cfg.Arr("files") {
			if jIs(jGet(f, "fileSlug"), slug) {
				return f.(*doc.Map)
			}
		}
		return nil
	}
	renderOf := func(f *doc.Map) string {
		if f.Str("editor") == "binary" {
			return jStr(jCoal(f.Val("render"), "none"))
		}
		return jStr(jCoal(f.Val("render"), "expressions"))
	}
	sha19 := func(c *doc.Map) string { return doc.JSSlice(c.Str("sha256"), 19) }
	content := func(f *doc.Map) *doc.Map {
		if f == nil {
			return nil
		}
		return f.Map("content")
	}
	for _, sx := range jArr(jGet(cfg, "sites"), "for", "cfg.sites") {
		site := sx.(*doc.Map)
		ss := site.Str("siteSlug")
		sc := site.Map("config")
		for _, vx := range sc.Arr("volumes") {
			v := vx.(*doc.Map)
			sd := v.Map("seed")
			if sd == nil || (sd.Str("kind") != "files" && sd.Str("kind") != "file") {
				continue
			}
			vs := v.Str("volumeSlug")
			if sd.Str("kind") == "file" {
				slug := sd.Str("fileSlug")
				touch(slug)
				f := fileOf(slug)
				c := content(f)
				if c == nil {
					notes = append(notes, "site "+ss+": volume "+vs+" started from file "+slug+", which has no content — starter content removed (starts empty)")
					v.Set("seed", nil)
				} else {
					v.Set("seed", doc.Obj("kind", "single", "sha256", c.Val("sha256"), "size", c.Val("sizeBytes"), "mode", DEFAULT_FILE_MODE, "render", renderOf(f)))
					lines = append(lines, "site "+ss+": volume "+vs+" (single file) ← content of file "+slug+" ("+jStr(c.Val("sizeBytes"))+" B, "+sha19(c)+"…, render "+renderOf(f)+")")
				}
				continue
			}
			entries := []any{}
			for _, ex := range sd.Arr("entries") {
				e := ex.(*doc.Map)
				slug := e.Str("fileSlug")
				touch(slug)
				f := fileOf(slug)
				c := content(f)
				if c == nil {
					notes = append(notes, "site "+ss+": volume "+vs+" entry "+e.Str("path")+" ← file "+slug+" has no content — entry dropped")
					continue
				}
				entries = append(entries, doc.Obj("path", e.Val("path"), "sha256", c.Val("sha256"), "size", c.Val("sizeBytes"), "mode", DEFAULT_FILE_MODE, "render", renderOf(f)))
				lines = append(lines, "site "+ss+": volume "+vs+" content "+e.Str("path")+" ← content of file "+slug+" ("+jStr(c.Val("sizeBytes"))+" B, "+sha19(c)+"…, render "+renderOf(f)+")")
			}
			if len(entries) > 0 {
				v.Set("seed", doc.Obj("kind", "tree").Spread(jNormalizeVolumeTree(doc.Obj("entries", entries, "dirs", []any{}))))
			} else {
				v.Set("seed", nil)
			}
		}
		for _, cx := range sc.Arr("containers") {
			c := cx.(*doc.Map)
			ivs := imageVolumes(c.Str("serviceSlug"), c.Str("version"))
			pathOf := func(m *doc.Map) string {
				if m.Truthy("mountPath") {
					return m.Str("mountPath")
				}
				if !m.Truthy("imageVolume") {
					return ""
				}
				if iv, ok := jFind(ivs, func(x any) bool { return jEq(jGet(x, "name"), m.Val("imageVolume")) }).(*doc.Map); ok {
					return iv.Str("mountPath")
				}
				return ""
			}
			type wr struct {
				m    *doc.Map
				path string
			}
			var writable []wr
			for _, mx := range c.Arr("volumes") {
				m := mx.(*doc.Map)
				k := m.Map("source").Str("kind")
				if (k == "volume" || k == "host") && jEq(m.Val("readOnly"), false) {
					if p := pathOf(m); p != "" && !strings.Contains(p, "{{") {
						writable = append(writable, wr{m, p})
					}
				}
			}
			keep := []any{}
			cs := c.Str("containerSlug")
			for _, mx := range c.Arr("volumes") {
				m := mx.(*doc.Map)
				src := m.Map("source")
				if src.Str("kind") != "file" {
					keep = append(keep, m)
					continue
				}
				fp := pathOf(m)
				if fp == "" {
					if m.Truthy("imageVolume") && ivs == nil {
						notes = append(notes, "site "+ss+": "+cs+" mounts file "+src.Str("fileSlug")+" on image volume "+m.Str("imageVolume")+" — image "+c.Str("serviceSlug")+":"+c.Str("version")+" unknown, not checked")
					}
					keep = append(keep, m)
					continue
				}
				if strings.Contains(fp, "{{") {
					keep = append(keep, m)
					continue
				}
				var host *wr
				for i := range writable {
					w := strings.TrimRight(writable[i].path, "/")
					if fp == w || strings.HasPrefix(fp, w+"/") {
						host = &writable[i]
						break
					}
				}
				if host == nil {
					keep = append(keep, m)
					continue
				}
				fileSlug := src.Str("fileSlug")
				where := "site " + ss + ": " + cs + " mounts file " + fileSlug + " at " + fp + " inside the writable "
				hs := host.m.Map("source")
				if hs.Str("kind") != "volume" {
					notes = append(notes, where+"host folder "+host.path+" — move the mount path outside it or mount the folder read-only (left as is)")
					keep = append(keep, m)
					continue
				}
				volumeSlug := hs.Str("volumeSlug")
				var vol *doc.Map
				for _, x := range sc.Arr("volumes") {
					if x.(*doc.Map).Str("volumeSlug") == volumeSlug {
						vol = x.(*doc.Map)
						break
					}
				}
				rel := fp[len(strings.TrimRight(host.path, "/"))+1:]
				f := fileOf(fileSlug)
				cont := content(f)
				relErr := ContentPathError(rel)
				if vol == nil || vol.Str("kind") == "file" || relErr != nil || (vol.Map("seed") != nil && vol.Map("seed").Str("kind") != "tree") || cont == nil {
					why := ""
					switch {
					case vol == nil:
						why = "volume not declared"
					case vol.Str("kind") == "file":
						why = "single-file volume"
					case relErr != nil:
						why = *relErr
					case cont == nil:
						why = "the file has no content"
					default:
						why = "starter content is " + vol.Map("seed").Str("kind")
					}
					notes = append(notes, where+"volume "+volumeSlug+" ("+host.path+") — cannot move it into the volume's content ("+why+"); fix it by hand (left as is)")
					keep = append(keep, m)
					continue
				}
				entries, dirs := []any{}, []any{}
				if seed := vol.Map("seed"); seed != nil {
					entries, dirs = slices.Clone(seed.Arr("entries")), seed.Arr("dirs")
				}
				if slices.ContainsFunc(entries, func(e any) bool { return jIs(jGet(e, "path"), rel) }) {
					notes = append(notes, where+"volume "+volumeSlug+": its content already has "+rel+" — kept the content's version")
				} else {
					entries = append(entries, doc.Obj("path", rel, "sha256", cont.Val("sha256"), "size", cont.Val("sizeBytes"), "mode", DEFAULT_FILE_MODE, "render", renderOf(f)))
				}
				vol.Set("seed", doc.Obj("kind", "tree").Spread(jNormalizeVolumeTree(doc.Obj("entries", entries, "dirs", dirs))))
				touch(fileSlug)
				moved = append(moved, doc.Obj("siteSlug", ss, "containerSlug", cs, "fileSlug", fileSlug, "volumeSlug", volumeSlug, "path", rel))
				tail := where + "volume " + volumeSlug + " (" + host.path + ") → moved into the volume's content as " + rel + " (" + jStr(cont.Val("sizeBytes")) + " B, render " + renderOf(f) + "); "
				if m.Truthy("imageVolume") {
					keep = append(keep, m.Shallow().Set("source", doc.Obj("kind", "none")).Set("readOnly", false).Set("mountPath", m.Val("mountPath")))
					lines = append(lines, tail+"image volume "+m.Str("imageVolume")+" → Not mounted (the volume provides it)")
				} else {
					lines = append(lines, tail+"extra mount removed")
				}
			}
			c.Set("volumes", keep)
		}
	}
	removed := []any{}
	sitesText := doc.MarshalString(cfg.Val("sites"))
	for _, slug := range touched {
		quoted := doc.MarshalString(slug)
		if strings.Contains(sitesText, `"fileSlug":`+quoted) || strings.Contains(sitesText, `"certFileSlug":`+quoted) || jHasFileRef(sitesText, slug) || fileOf(slug) == nil {
			continue
		}
		cfg.Set("files", slices.DeleteFunc(slices.Clone(cfg.Arr("files")), func(f any) bool { return jIs(jGet(f, "fileSlug"), slug) }))
		removed = append(removed, slug)
		lines = append(lines, "file "+slug+" removed: nothing mounts it any more (its content lives in the volume's own content now)")
	}
	for _, slug := range touched {
		if !jIncludes(removed, slug) && fileOf(slug) != nil {
			lines = append(lines, "file "+slug+" kept: still mounted read-only (or referenced) elsewhere")
		}
	}
	changed := len(lines) > 0 || slices.ContainsFunc(notes, func(n any) bool {
		return strings.Contains(n.(string), "removed") || strings.Contains(n.(string), "dropped")
	})
	return doc.Obj("config", cfg, "changed", changed, "lines", lines, "notes", notes, "removedFiles", removed, "moved", moved), nil
}

// jHasFileRef is /file\.<slug>(?![a-z0-9-])/.test(text).
func jHasFileRef(text, slug string) bool {
	ref := "file." + slug
	for i := 0; ; {
		j := strings.Index(text[i:], ref)
		if j < 0 {
			return false
		}
		i += j + len(ref)
		if i == len(text) || !(text[i] >= 'a' && text[i] <= 'z' || text[i] >= '0' && text[i] <= '9' || text[i] == '-') {
			return true
		}
		i -= len(ref) - 1
	}
}
