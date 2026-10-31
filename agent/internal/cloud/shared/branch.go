package shared

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/cloud/doc"
)

// Port of shared/src/branch.ts (SPEC-final §21 #104) on doc values: same items, same key order. The agent twin is
// agent/internal/branch; where the two differ this file follows the TS.

// BRANCHES_MIN_AGENT is TS BRANCHES_MIN_AGENT.
const BRANCHES_MIN_AGENT = "0.14.0"

// COMMIT_MESSAGE_MAX is TS COMMIT_MESSAGE_MAX.
const COMMIT_MESSAGE_MAX = 2000

// COMMIT_SUMMARY_MAX is TS COMMIT_SUMMARY_MAX.
const COMMIT_SUMMARY_MAX = 200

// COMMIT_PICKS_MAX is TS COMMIT_PICKS_MAX.
const COMMIT_PICKS_MAX = 500

// COMMIT_STATS_KEYS_MAX is TS COMMIT_STATS_KEYS_MAX.
const COMMIT_STATS_KEYS_MAX = 200

// CLOUD_RECENT_COMMITS is TS CLOUD_RECENT_COMMITS.
const CLOUD_RECENT_COMMITS = 10

// CONTENT_NOTE_STARTER is TS CONTENT_NOTE_STARTER.
const CONTENT_NOTE_STARTER = "starter content — written only into new / empty volumes"

// CONTENT_NOTE_PERSIST is TS CONTENT_NOTE_PERSIST.
const CONTENT_NOTE_PERSIST = "persist-to-cloud content — written into the volume on the site when merged"

// COMMIT_KINDS is TS COMMIT_KINDS.
var COMMIT_KINDS = []string{"create", "change", "merge", "cherry-pick", "revert", "reset", "drift", "rollback", "migrated", "legacy"}

// MERGE_KINDS is TS MERGE_KINDS.
var MERGE_KINDS = []string{"add", "remove", "change", "theirs", "ours", "conflict", "skipped"}

// SOURCE_CHANGE_KINDS is TS SOURCE_CHANGE_KINDS.
var SOURCE_CHANGE_KINDS = []string{"theirs", "add", "change", "conflict", "skipped"}

// SITE_SETTINGS is TS SITE_SETTINGS.
var SITE_SETTINGS = []string{"label", "host", "agentPort", "agentGuiAliasPort", "notFoundPageId", "depends"}

// EDGE_FIELDS is TS EDGE_FIELDS.
var EDGE_FIELDS = []string{"httpPort", "httpsPort", "maxBodyBytes"}

// CONTAINER_FIELDS is TS CONTAINER_FIELDS.
var CONTAINER_FIELDS = []string{"image", "enabled", "command", "entrypoint", "restart", "resources", "healthcheck", "depends", "ports", "volumes"}

// CreateMessage is TS createMessage (seq nil = no seq; doc.Undefined prints like the TS template).
func CreateMessage(label string, seq any) string {
	if seq == nil {
		return "created from " + label
	}
	return "created from " + label + "@" + brJSString(seq)
}

// ── keys + labels

// BranchKey is TS branchKey.
func BranchKey(b *doc.Map) string {
	switch b.Val("kind") {
	case "template":
		return "template:" + brJSString(brUnd(b, "templateId"))
	case "deployment":
		return "deployment:" + brJSString(brUnd(b, "deploymentId"))
	}
	return "agent:" + brJSString(brUnd(b, "deploymentId")) + "/" + brJSString(brUnd(b, "siteSlug"))
}

// Go's \s is ASCII only: the class spells out the JS \s.
var brBranchKeyRE = regexp.MustCompile(`^(template|deployment|agent):([^/\t\n\x0B\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+)(?:/([a-z][a-z0-9-]{1,39}))?$`)

// ParseBranchKey is TS parseBranchKey (nil = TS null).
func ParseBranchKey(s string) *doc.Map {
	m := brBranchKeyRE.FindStringSubmatch(doc.JSTrim(s))
	if m == nil {
		return nil
	}
	switch {
	case m[1] == "template" && m[3] == "":
		return doc.Obj("kind", "template", "templateId", m[2])
	case m[1] == "deployment" && m[3] == "":
		return doc.Obj("kind", "deployment", "deploymentId", m[2])
	case m[1] == "agent" && m[3] != "":
		return doc.Obj("kind", "agent", "deploymentId", m[2], "siteSlug", m[3])
	}
	return nil
}

// BranchKeyDocId is TS branchKeyDocId.
func BranchKeyDocId(key string) string { return strings.ReplaceAll(key, "/", "~") }

// SameBranch is TS sameBranch.
func SameBranch(a, b *doc.Map) bool { return BranchKey(a) == BranchKey(b) }

// PointLabel is TS pointLabel (seq nil / doc.Undefined = no seq).
func PointLabel(label string, seq any) string {
	if brIsNullish(seq) {
		return label
	}
	return label + "@" + brJSString(seq)
}

// ParsePointParam is TS parsePointParam (nil = TS null).
func ParsePointParam(s string) *doc.Map {
	at := strings.LastIndex(s, "@")
	key := s
	if at > 0 {
		key = s[:at]
	}
	branch := ParseBranchKey(key)
	if branch == nil {
		return nil
	}
	rest := ""
	if at > 0 {
		rest = s[at+1:]
	}
	switch rest {
	case "", "head":
		return doc.Obj("branch", branch, "at", "head")
	case "working":
		return doc.Obj("branch", branch, "at", "working")
	}
	return doc.Obj("branch", branch, "at", doc.Obj("commitId", rest))
}

// PointParam is TS pointParam.
func PointParam(p *doc.Map) string {
	key := BranchKey(p.Map("branch"))
	switch p.Val("at") {
	case "head":
		return key
	case "working":
		return key + "@working"
	}
	return key + "@" + brJSString(brUnd(p.Map("at"), "commitId"))
}

// ── messages

// CommitSummary is TS commitSummary.
func CommitSummary(message string) string {
	line, _, _ := strings.Cut(doc.JSTrim(message), "\n")
	return doc.JSTrim(strings.TrimSuffix(line, "\r"))
}

func brCommitIssue(path, message string) []any {
	return []any{doc.Obj("code", "V_COMMIT_MESSAGE", "path", path, "message", message, "severity", "error")}
}

// CheckCommitMessage is TS checkCommitMessage (path "" = the TS default "message").
func CheckCommitMessage(message any, path string) []any {
	if path == "" {
		path = "message"
	}
	s, ok := message.(string)
	m := doc.JSTrim(s)
	if !ok || m == "" {
		return brCommitIssue(path, "a commit message is required")
	}
	if n := doc.JSLen(m); n > COMMIT_MESSAGE_MAX {
		return brCommitIssue(path, fmt.Sprintf("the commit message is %d characters (max %d)", n, COMMIT_MESSAGE_MAX))
	}
	first := CommitSummary(m)
	if first == "" {
		return brCommitIssue(path, "the first line (the summary) must not be empty")
	}
	if n := doc.JSLen(first); n > COMMIT_SUMMARY_MAX {
		return brCommitIssue(path, fmt.Sprintf("the first line is %d characters (max %d)", n, COMMIT_SUMMARY_MAX))
	}
	return []any{}
}

// ── hashing

// Sha256HexOf is TS sha256HexOf.
func Sha256HexOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ConfigHash is TS configHash (error = the CanonicalError of a non-finite number).
func ConfigHash(config *doc.Map) (string, error) {
	s, err := brCanonicalize(config)
	if err != nil {
		return "", err
	}
	return "sha256:" + Sha256HexOf(s), nil
}

// CatalogHashOf is TS catalogHashOf (error = the CanonicalError of a non-finite number).
func CatalogHashOf(catalog any) (string, error) {
	s, err := brCanonicalize(catalog)
	if err != nil {
		return "", err
	}
	return "sha256:" + Sha256HexOf(s), nil
}

// SecretValueMarker is TS secretValueMarker (nil or a "sha256:" string).
func SecretValueMarker(v any) any {
	if brEmptyVal(v) {
		return nil
	}
	return "sha256:" + Sha256HexOf(brJSString(v))
}

// MarkSecretValues is TS markSecretValues.
func MarkSecretValues(c *doc.Map) *doc.Map {
	secrets := make([]any, 0, len(c.Arr("secrets")))
	for _, x := range c.Arr("secrets") {
		s, _ := x.(*doc.Map)
		secrets = append(secrets, s.Shallow().Set("value", SecretValueMarker(s.Val("value"))))
	}
	return c.Shallow().Set("secrets", secrets)
}

// ItemHash is TS itemHash (a Slot, a {has, v} object, or a plain value; doc.Undefined = absent).
func ItemHash(s any) string { return brSlotHash(s) }

// ── units

type brUnit struct {
	key, section string
	siteSlug     any
	label        string
	b, t, o      Slot // base, source, target
	set          func(cfg *doc.Map, s Slot, src *doc.Map)
	refs         func(src *doc.Map) []string
	blob         func(s Slot) any
	blobMeta     *doc.Map
	note         string
	secretValue  bool
}

func brVarDef(v *doc.Map) Slot {
	if v == nil {
		return brNone
	}
	return Slot{true, v.Without("value")}
}

func brSecretDef(s *doc.Map) Slot {
	if s == nil {
		return brNone
	}
	return Slot{true, doc.Obj("secretSlug", brUnd(s, "secretSlug"), "label", brUnd(s, "label"), "description", brUnd(s, "description"), "required", brUnd(s, "required"), "generate", brNul(s, "generate"))}
}

// brTaken is { ...clone(s.v), value: cur ? cur.value : clone(tv?.value ?? null) } of a taken variable definition.
func brTaken(v any, cur, tv *doc.Map) *doc.Map {
	n, _ := brClone(v).(*doc.Map)
	if cur != nil {
		return n.Set("value", brUnd(cur, "value"))
	}
	return n.Set("value", brClone(brNul(tv, "value")))
}

// brTakenValue is s.has ? clone(s.v) : undefined.
func brTakenValue(s Slot) any {
	if !s.Has {
		return doc.Undefined
	}
	return brClone(s.V)
}

func brSettingSlot(s *doc.Map, f string) Slot {
	switch {
	case s == nil:
		return brNone
	case f == "label":
		return brSlot(brUnd(s, "label"))
	}
	return brSlot(brNul(s.Map("config"), f))
}

func brSetSetting(site *doc.Map, f string, s Slot) {
	if site == nil || !s.Has {
		return
	}
	if f == "label" {
		site.Set("label", s.V)
	} else if c := site.Map("config"); c != nil {
		c.Set(f, brClone(s.V))
	}
}

func brEdgeSlot(s *doc.Map, f string) Slot {
	if s == nil {
		return brNone
	}
	return brSlot(brNul(s.Map("config").Map("edge"), f))
}

func brSetEdge(site *doc.Map, f string, s Slot) {
	if e := site.Map("config").Map("edge"); e != nil && s.Has {
		e.Set(f, brClone(s.V))
	}
}

// brEdgeList / brConfigList pick the list a keyed item lives in: owner, field, ??= [].
func brEdgeList(f string) func(*doc.Map) (*doc.Map, string, bool) {
	return func(site *doc.Map) (*doc.Map, string, bool) { return site.Map("config").Map("edge"), f, false }
}

func brConfigList(f string) func(*doc.Map) (*doc.Map, string, bool) {
	return func(site *doc.Map) (*doc.Map, string, bool) { return site.Map("config"), f, true }
}

func brKeyedSet(site *doc.Map, listOf func(*doc.Map) (*doc.Map, string, bool), kf brKeyOf, k string, s Slot, order []string) {
	owner, f, init := listOf(site)
	if owner == nil {
		return
	}
	list := owner.Arr(f)
	if init {
		list = brListInit(owner, f)
	}
	owner.Set(f, brUpsert(list, kf, k, brTakenValue(s), order))
}

func brContainerField(c *doc.Map, f string) Slot {
	if c == nil {
		return brNone
	}
	if f == "image" {
		return Slot{true, doc.Obj("serviceSlug", brUnd(c, "serviceSlug"), "version", brUnd(c, "version"))}
	}
	return brSlot(brNul(c, f))
}

func brSetContainerField(c *doc.Map, f string, s Slot) {
	if c == nil || !s.Has {
		return
	}
	if f == "image" {
		v, _ := s.V.(*doc.Map)
		c.Set("serviceSlug", brUnd(v, "serviceSlug"))
		c.Set("version", brUnd(v, "version"))
	} else {
		c.Set(f, brClone(s.V))
	}
}

func brEnvVal(x any) Slot {
	e, _ := x.(*doc.Map)
	if e == nil {
		return brNone
	}
	if doc.Truthy(e.Val("secretRef")) {
		return brSlot(doc.Obj("secretRef", brUnd(e, "secretRef")))
	}
	return brSlot(brNul(e, "value"))
}

func brSetEnv(c *doc.Map, name string, s Slot, order []string) {
	if c == nil {
		return
	}
	var row any = doc.Undefined
	if s.Has {
		switch {
		case brIsObject(s.V):
			m, _ := s.V.(*doc.Map)
			row = doc.Obj("name", name, "value", nil, "secretRef", brUnd(m, "secretRef"))
		case s.V == nil:
			row = doc.Obj("name", name, "value", nil, "secretRef", nil)
		default:
			row = doc.Obj("name", name, "value", brJSString(s.V), "secretRef", nil)
		}
	}
	c.Set("env", brUpsert(c.Arr("env"), brField("name"), name, row, order))
}

// the volume definition without its own content (tree / single seeds → a marker; content entries are separate items)
func brVolumeDef(v *doc.Map) any {
	if v == nil {
		return doc.Undefined
	}
	if seed := v.Map("seed"); seed != nil && (seed.Val("kind") == any("tree") || seed.Val("kind") == any("single")) {
		return v.Shallow().Set("seed", doc.Obj("kind", brUnd(seed, "kind")))
	}
	return v
}

// §21 #117 a persist-to-cloud volume with seed null as the same volume with an empty tree
func brPersistTree(v *doc.Map) *doc.Map {
	if v != nil && doc.Truthy(v.Val("persist")) && !doc.Truthy(v.Val("seed")) {
		return v.Shallow().Set("seed", brEmptyTree())
	}
	return v
}

func brEmptyTree() *doc.Map { return doc.Obj("kind", "tree", "entries", []any{}, "dirs", []any{}) }

// the tree seed of v to write content items into; a persist volume without content gets an empty tree
func brContentTreeOf(v *doc.Map) *doc.Map {
	if v == nil {
		return nil
	}
	if !doc.Truthy(v.Val("seed")) && doc.Truthy(v.Val("persist")) {
		v.Set("seed", brEmptyTree())
	}
	if seed := v.Map("seed"); seed.Val("kind") == any("tree") {
		return seed
	}
	return nil
}

func brIsContentSeed(v *doc.Map) bool {
	k := v.Map("seed").Val("kind")
	return k == any("tree") || k == any("single")
}

// references inside expressions / mounts → item keys (requires)
var brExprRef = regexp.MustCompile(`\{\{[^}]*?\b(var|secret|file)\.([A-Za-z0-9_.-]+)`)

func brExprRefs(v any) []string {
	var s string
	if x, ok := v.(string); ok {
		s = x
	} else if brIsObject(v) {
		s = brCanon(v)
	}
	var out []string
	for _, m := range brExprRef.FindAllStringSubmatch(s, -1) {
		slug, _, _ := strings.Cut(strings.TrimRight(m[2], "."), ".")
		switch m[1] {
		case "var":
			out = append(out, "variables/"+slug)
		case "secret":
			out = append(out, "secrets/"+slug)
		default:
			out = append(out, "files/"+slug)
		}
	}
	return out
}

func brMountRefs(ss string, mounts []any) []string {
	var out []string
	for _, x := range mounts {
		m, _ := x.(*doc.Map)
		if !doc.Truthy(m.Val("source")) {
			continue
		}
		src := m.Map("source")
		kind := src.Val("kind")
		if kind == any("volume") && doc.Truthy(src.Val("volumeSlug")) {
			out = append(out, "sites/"+ss+"/volumes/"+brJSString(src.Val("volumeSlug")))
		}
		if kind == any("file") && doc.Truthy(src.Val("fileSlug")) {
			out = append(out, "files/"+brJSString(src.Val("fileSlug")))
		}
		if kind == any("secret") && doc.Truthy(src.Val("secretSlug")) {
			out = append(out, "secrets/"+brJSString(src.Val("secretSlug")))
		}
		out = append(out, brExprRefs(m.Val("mountPath"))...)
	}
	return out
}

func brDependRefs(pre string, c *doc.Map) []string {
	var out []string
	for _, x := range c.Arr("depends") {
		d, _ := x.(*doc.Map)
		out = append(out, pre+"/containers/"+brJSString(brUnd(d, "containerSlug")))
	}
	return out
}

func brEnvRefs(e *doc.Map) []string {
	var out []string
	if doc.Truthy(e.Val("secretRef")) {
		out = append(out, "secrets/"+brJSString(e.Val("secretRef")))
	}
	return append(out, brExprRefs(e.Val("value"))...)
}

func brContainerRefs(ss string, c *doc.Map) []string {
	if c == nil {
		return nil
	}
	out := brMountRefs(ss, c.Arr("volumes"))
	out = append(out, brDependRefs("sites/"+ss, c)...)
	for _, x := range c.Arr("env") {
		e, _ := x.(*doc.Map)
		out = append(out, brEnvRefs(e)...)
	}
	out = append(out, brExprRefs(c.Val("command"))...)
	return append(out, brExprRefs(c.Val("entrypoint"))...)
}

func brBuildUnits(base, theirs, ours, opts *doc.Map) []*brUnit {
	B := base
	if B == nil {
		B = brEmptyConfig()
	}
	var units []*brUnit
	push := func(u *brUnit) { units = append(units, u) }
	secretValues := doc.Truthy(opts.Val("secretValues"))
	scope := opts.Val("scope")

	// variables: definition + value
	{
		kf := brField("variableSlug")
		bl, tl, ol := B.Arr("variables"), theirs.Arr("variables"), ours.Arr("variables")
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, slug := range brKeysOf(kf, bl, tl, ol) {
			bv, _ := bm[slug].(*doc.Map)
			tv, _ := tm[slug].(*doc.Map)
			ov, _ := om[slug].(*doc.Map)
			push(&brUnit{key: "variables/" + slug, section: "variables", label: "Variable " + slug, b: brVarDef(bv), t: brVarDef(tv), o: brVarDef(ov),
				set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
					list := cfg.Arr("variables")
					cur := brFind(list, "variableSlug", slug)
					// a taken definition keeps the target value; a NEW variable starts with the source value
					var next any = doc.Undefined
					if s.Has {
						next = brTaken(s.V, cur, tv)
					}
					cfg.Set("variables", brUpsert(list, kf, slug, next, order))
				}})
			// the value is its own item when the variable exists on both sides (a new / removed variable carries its value)
			if tv != nil && ov != nil {
				b := brNone
				if bv != nil {
					b = brSlot(brNul(bv, "value"))
				}
				push(&brUnit{key: "variables/" + slug + "/value", section: "variables", label: "Variable " + slug + " — value", b: b, t: brSlot(brNul(tv, "value")), o: brSlot(brNul(ov, "value")),
					set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
						if cur := brFind(cfg.Arr("variables"), "variableSlug", slug); cur != nil && s.Has {
							cur.Set("value", brClone(s.V))
						}
					}})
			}
		}
	}
	// secrets: definitions (+ values when both sides hold them)
	{
		kf := brField("secretSlug")
		bl, tl, ol := B.Arr("secrets"), theirs.Arr("secrets"), ours.Arr("secrets")
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, slug := range brKeysOf(kf, bl, tl, ol) {
			b, _ := bm[slug].(*doc.Map)
			t, _ := tm[slug].(*doc.Map)
			o, _ := om[slug].(*doc.Map)
			push(&brUnit{key: "secrets/" + slug, section: "secrets", label: "Secret " + slug, b: brSecretDef(b), t: brSecretDef(t), o: brSecretDef(o),
				set: func(cfg *doc.Map, s Slot, src *doc.Map) {
					list := cfg.Arr("secrets")
					cur := brFind(list, "secretSlug", slug)
					srcSecret := brFind(src.Arr("secrets"), "secretSlug", slug)
					// a taken definition keeps the target value; a NEW secret takes the source value when values are compared
					// (deployment / agent), else starts without one (the cloud generates it when it has a generate rule)
					var value, rotatedAt any
					if cur != nil {
						value, rotatedAt = brUnd(cur, "value"), brUnd(cur, "rotatedAt")
					} else {
						if sv, ok := srcSecret.Val("value").(string); ok && secretValues && !strings.HasPrefix(sv, "sha256:") {
							value = sv
						}
						if value != nil {
							rotatedAt = brNul(srcSecret, "rotatedAt")
						}
					}
					var next any = doc.Undefined
					if s.Has {
						n, _ := brClone(s.V).(*doc.Map)
						next = n.Set("value", value).Set("rotatedAt", rotatedAt)
					}
					cfg.Set("secrets", brUpsert(list, kf, slug, next, order))
				}})
			if secretValues && t != nil && o != nil {
				mv := func(x *doc.Map) Slot {
					if x == nil {
						return brNone
					}
					if sv, ok := x.Val("value").(string); ok && strings.HasPrefix(sv, "sha256:") {
						return brSlot(sv)
					}
					return brSlot(SecretValueMarker(x.Val("value")))
				}
				push(&brUnit{key: "secrets/" + slug + "/value", section: "secrets", label: "Secret " + slug + " — value", b: mv(b), t: mv(t), o: mv(o), secretValue: true,
					set: func(cfg *doc.Map, s Slot, src *doc.Map) {
						cur := brFind(cfg.Arr("secrets"), "secretSlug", slug)
						srcSecret := brFind(src.Arr("secrets"), "secretSlug", slug)
						if cur == nil || !s.Has || srcSecret == nil {
							return
						}
						cur.Set("value", brUnd(srcSecret, "value"))
						rotated := brNullish(srcSecret.Val("rotatedAt"))
						if rotated == nil {
							rotated = brNul(cur, "rotatedAt")
						}
						cur.Set("rotatedAt", rotated)
					}})
			}
		}
	}
	// files: metadata + content ref
	{
		kf := brField("fileSlug")
		bl, tl, ol := B.Arr("files"), theirs.Arr("files"), ours.Arr("files")
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, slug := range brKeysOf(kf, bl, tl, ol) {
			push(&brUnit{key: "files/" + slug, section: "files", label: "File " + slug, b: brSlot(bm.get(slug)), t: brSlot(tm.get(slug)), o: brSlot(om.get(slug)),
				set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
					cfg.Set("files", brUpsert(cfg.Arr("files"), kf, slug, brTakenValue(s), order))
				},
				blob: func(s Slot) any {
					if !s.Has {
						return nil
					}
					v, _ := s.V.(*doc.Map)
					return brNul(v.Map("content"), "sha256")
				},
				blobMeta: doc.Obj("fileSlug", slug)})
		}
	}
	// sites
	{
		kf := brField("siteSlug")
		bl, tl, ol := B.Arr("sites"), theirs.Arr("sites"), ours.Arr("sites")
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, ss := range brKeysOf(kf, bl, tl, ol) {
			if doc.Truthy(scope) && ss != brJSString(scope) {
				continue
			}
			t, _ := tm[ss].(*doc.Map)
			o, _ := om[ss].(*doc.Map)
			if t == nil || o == nil {
				push(&brUnit{key: "sites/" + ss, section: "sites", siteSlug: ss, label: "Site " + ss, b: brSlot(bm.get(ss)), t: brSlot(tm.get(ss)), o: brSlot(om.get(ss)),
					set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
						cfg.Set("sites", brUpsert(cfg.Arr("sites"), kf, ss, brTakenValue(s), order))
					},
					refs: func(src *doc.Map) []string {
						var out []string
						for _, x := range brSiteOf(src, ss).Map("config").Arr("containers") {
							c, _ := x.(*doc.Map)
							for _, k := range brContainerRefs(ss, c) {
								if !strings.HasPrefix(k, "sites/"+ss+"/") {
									out = append(out, k)
								}
							}
						}
						return out
					}})
				continue
			}
			b, _ := bm[ss].(*doc.Map)
			brSiteUnits(ss, b, t, o, push)
		}
	}
	return units
}

func brSiteUnits(ss string, b, t, o *doc.Map, push func(*brUnit)) {
	pre := "sites/" + ss
	for _, f := range SITE_SETTINGS {
		get := func(s *doc.Map) Slot { return brSettingSlot(s, f) }
		u := &brUnit{key: pre + "/settings/" + f, section: "sites", siteSlug: ss, label: ss + " · " + f, b: get(b), t: get(t), o: get(o),
			set: func(cfg *doc.Map, s Slot, _ *doc.Map) { brSetSetting(brSiteOf(cfg, ss), f, s) }}
		if f != "notFoundPageId" && f != "depends" {
			u.refs = func(src *doc.Map) []string { return brExprRefs(brSiteOf(src, ss).Map("config").Val(f)) }
		}
		push(u)
	}
	for _, f := range EDGE_FIELDS {
		get := func(s *doc.Map) Slot { return brEdgeSlot(s, f) }
		push(&brUnit{key: pre + "/edge/" + f, section: "edge", siteSlug: ss, label: ss + " · edge " + f, b: get(b), t: get(t), o: get(o),
			set: func(cfg *doc.Map, s Slot, _ *doc.Map) { brSetEdge(brSiteOf(cfg, ss), f, s) }})
	}
	keyed := func(bl, tl, ol []any, field, section, seg string, label func(string) string, listOf func(*doc.Map) (*doc.Map, string, bool), refs func(x *doc.Map) []string) {
		kf := brField(field)
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, k := range brKeysOf(kf, bl, tl, ol) {
			tv, _ := tm[k].(*doc.Map)
			push(&brUnit{key: pre + "/" + seg + "/" + k, section: section, siteSlug: ss, label: label(k), b: brSlot(bm.get(k)), t: brSlot(tm.get(k)), o: brSlot(om.get(k)),
				set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
					if site := brSiteOf(cfg, ss); site != nil {
						brKeyedSet(site, listOf, kf, k, s, order)
					}
				},
				refs: func(*doc.Map) []string {
					if tv != nil {
						return refs(tv)
					}
					return nil
				}})
		}
	}
	bc, tc, oc := b.Map("config"), t.Map("config"), o.Map("config")
	keyed(bc.Map("edge").Arr("hosts"), tc.Map("edge").Arr("hosts"), oc.Map("edge").Arr("hosts"), "hostname", "edge", "edge/hosts", func(k string) string { return ss + " · edge host " + k }, brEdgeList("hosts"), func(h *doc.Map) []string {
		tls := h.Map("tls")
		var out []string
		if v := tls.Val("certFileSlug"); doc.Truthy(v) {
			out = append(out, "files/"+brJSString(v))
		}
		if v := tls.Val("keyFileSlug"); doc.Truthy(v) {
			out = append(out, "files/"+brJSString(v))
		}
		if v := tls.Val("keySecretSlug"); doc.Truthy(v) {
			out = append(out, "secrets/"+brJSString(v))
		}
		return out
	})
	keyed(bc.Map("edge").Arr("routes"), tc.Map("edge").Arr("routes"), oc.Map("edge").Arr("routes"), "routeId", "edge", "edge/routes", func(k string) string { return ss + " · edge route " + k }, brEdgeList("routes"), func(r *doc.Map) []string {
		var out []string
		a := r.Map("action")
		if a.Val("type") == any("proxy") && doc.Truthy(a.Val("containerSlug")) {
			out = append(out, pre+"/containers/"+brJSString(a.Val("containerSlug")))
		}
		for _, h := range r.Arr("hosts") {
			out = append(out, pre+"/edge/hosts/"+brJSString(h))
		}
		return out
	})
	// volumes: whole when one side lacks it; else definition (without own content) + content entries
	{
		kf := brField("volumeSlug")
		bl, tl, ol := bc.Arr("volumes"), tc.Arr("volumes"), oc.Arr("volumes")
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, vs := range brKeysOf(kf, bl, tl, ol) {
			bv, _ := bm[vs].(*doc.Map)
			tv, _ := tm[vs].(*doc.Map)
			ov, _ := om[vs].(*doc.Map)
			vpre := pre + "/volumes/" + vs
			if tv == nil || ov == nil {
				u := &brUnit{key: vpre, section: "volumes", siteSlug: ss, label: ss + " · volume " + vs, b: brSlot(bm.get(vs)), t: brSlot(tm.get(vs)), o: brSlot(om.get(vs)),
					set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
						if site := brSiteOf(cfg, ss); site != nil {
							brKeyedSet(site, brConfigList("volumes"), kf, vs, s, order)
						}
					}}
				if tv == nil {
					u.note = "the data folder is kept on the server (orphan); only the definition is removed"
				}
				push(u)
				continue
			}
			// §21 #117 a persist volume without content (seed null) is an empty tree: its files are content items
			pbv, ptv, pov := brPersistTree(bv), brPersistTree(tv), brPersistTree(ov)
			push(&brUnit{key: vpre, section: "volumes", siteSlug: ss, label: ss + " · volume " + vs, b: brSlot(brVolumeDef(pbv)), t: brSlot(brVolumeDef(ptv)), o: brSlot(brVolumeDef(pov)),
				set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
					site := brSiteOf(cfg, ss)
					if site == nil || !s.Has {
						return
					}
					cfgS := site.Map("config")
					cur := brFind(cfgS.Arr("volumes"), "volumeSlug", vs)
					next := brCloneMap(ptv)
					ns, cs := next.Map("seed"), cur.Map("seed")
					// keep the target's own content entries (content items move them one by one); a target without a tree has
					// no content items: it takes the source's tree whole
					if ns.Val("kind") == any("tree") && cs.Val("kind") == any("tree") {
						ns.Set("entries", brClone(brEmptyIfNull(cs.Val("entries")))).Set("dirs", brClone(brEmptyIfNull(cs.Val("dirs"))))
					} else if ns.Val("kind") == any("single") && cs.Val("kind") == any("single") {
						for _, k := range []string{"sha256", "size", "mode", "render"} {
							ns.Set(k, brUnd(cs, k))
						}
					}
					cfgS.Set("volumes", brUpsert(brListInit(cfgS, "volumes"), kf, vs, next, order))
				}})
			if brIsContentSeed(ptv) && brIsContentSeed(pov) && ptv.Map("seed").Val("kind") == pov.Map("seed").Val("kind") {
				brContentUnits(ss, vs, pbv, ptv, pov, push)
			}
		}
	}
	keyed(bc.Arr("devTools"), tc.Arr("devTools"), oc.Arr("devTools"), "presetSlug", "devTools", "devtools", func(k string) string { return ss + " · dev tool " + k }, brConfigList("devTools"), func(p *doc.Map) []string { return brExprRefs(p) })
	// containers
	kf := brField("containerSlug")
	bl, tl, ol := bc.Arr("containers"), tc.Arr("containers"), oc.Arr("containers")
	bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
	order := brOrder(tl, kf)
	for _, cs := range brKeysOf(kf, bl, tl, ol) {
		bcm, _ := bm[cs].(*doc.Map)
		tcm, _ := tm[cs].(*doc.Map)
		ocm, _ := om[cs].(*doc.Map)
		cpre := pre + "/containers/" + cs
		if tcm == nil || ocm == nil {
			u := &brUnit{key: cpre, section: "containers", siteSlug: ss, label: ss + " · container " + cs, b: brSlot(bm.get(cs)), t: brSlot(tm.get(cs)), o: brSlot(om.get(cs)),
				set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
					if site := brSiteOf(cfg, ss); site != nil {
						c := site.Map("config")
						c.Set("containers", brUpsert(c.Arr("containers"), kf, cs, brTakenValue(s), order))
					}
				},
				refs: func(*doc.Map) []string { return brContainerRefs(ss, tcm) }}
			if tcm == nil {
				u.note = "the container is stopped and removed on the site; its data folders stay"
			}
			push(u)
			continue
		}
		for _, f := range CONTAINER_FIELDS {
			u := &brUnit{key: cpre + "/" + f, section: "containers", siteSlug: ss, label: ss + " · " + cs + " · " + f, b: brContainerField(bcm, f), t: brContainerField(tcm, f), o: brContainerField(ocm, f),
				set: func(cfg *doc.Map, s Slot, _ *doc.Map) { brSetContainerField(brContainerOf(cfg, ss, cs), f, s) }}
			switch f {
			case "volumes":
				u.refs = func(*doc.Map) []string { return brMountRefs(ss, tcm.Arr("volumes")) }
			case "depends":
				u.refs = func(*doc.Map) []string { return brDependRefs(pre, tcm) }
			case "command", "entrypoint":
				u.refs = func(*doc.Map) []string { return brExprRefs(tcm.Val(f)) }
			}
			push(u)
		}
		ekf := brField("name")
		ebl, etl, eol := bcm.Arr("env"), tcm.Arr("env"), ocm.Arr("env")
		ebm, etm, eom := brByKey(ebl, ekf), brByKey(etl, ekf), brByKey(eol, ekf)
		eorder := brOrder(etl, ekf)
		for _, name := range brKeysOf(ekf, ebl, etl, eol) {
			te, _ := etm[name].(*doc.Map)
			push(&brUnit{key: cpre + "/env/" + name, section: "containers", siteSlug: ss, label: ss + " · " + cs + " · env " + name, b: brEnvVal(ebm.get(name)), t: brEnvVal(etm.get(name)), o: brEnvVal(eom.get(name)),
				set: func(cfg *doc.Map, s Slot, _ *doc.Map) { brSetEnv(brContainerOf(cfg, ss, cs), name, s, eorder) },
				refs: func(*doc.Map) []string {
					if te == nil {
						return nil
					}
					return brEnvRefs(te)
				}})
		}
	}
}

func brEmptyIfNull(v any) any {
	if brIsNullish(v) {
		return []any{}
	}
	return v
}

func brContentSlot(e *doc.Map) Slot {
	return brSlot(doc.Obj("sha256", brUnd(e, "sha256"), "size", brUnd(e, "size"), "mode", brUnd(e, "mode"), "render", brUnd(e, "render")))
}

func brContentBlob(s Slot) any {
	if !s.Has {
		return nil
	}
	v, _ := s.V.(*doc.Map)
	return brNul(v, "sha256")
}

// §21 #101 starter content entries of a volume present on both sides with the same content kind
func brContentUnits(ss, vs string, bv, tv, ov *doc.Map, push func(*brUnit)) {
	vpre := "sites/" + ss + "/volumes/" + vs + "/content"
	// §21 #117 a persist volume's content is committed content (written into the live volume on merge)
	note := CONTENT_NOTE_STARTER
	if doc.Truthy(tv.Val("persist")) || doc.Truthy(ov.Val("persist")) {
		note = CONTENT_NOTE_PERSIST
	}
	if tv.Map("seed").Val("kind") == any("single") {
		one := func(v *doc.Map) Slot {
			if s := v.Map("seed"); s.Val("kind") == any("single") {
				return brContentSlot(s)
			}
			return brNone
		}
		push(&brUnit{key: vpre, section: "volumes", siteSlug: ss, label: ss + " · volume " + vs + " · content", b: one(bv), t: one(tv), o: one(ov),
			set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
				seed := brVolumeOf(cfg, ss, vs).Map("seed")
				if !s.Has || seed.Val("kind") != any("single") {
					return
				}
				v, _ := brClone(s.V).(*doc.Map)
				seed.Spread(v)
			},
			blob: brContentBlob, blobMeta: doc.Obj("volumeSlug", vs, "path", ""), note: note})
		return
	}
	entries := func(v *doc.Map) []any {
		if s := v.Map("seed"); s.Val("kind") == any("tree") {
			return s.Arr("entries")
		}
		return nil
	}
	dirs := func(v *doc.Map) []any {
		if s := v.Map("seed"); s.Val("kind") == any("tree") {
			return s.Arr("dirs")
		}
		return nil
	}
	pkf := brField("path")
	ebm, etm, eom := brByKey(entries(bv), pkf), brByKey(entries(tv), pkf), brByKey(entries(ov), pkf)
	ent := func(x any) Slot {
		e, _ := x.(*doc.Map)
		if e == nil {
			return brNone
		}
		return brContentSlot(e)
	}
	for _, p := range brKeysOf(pkf, entries(bv), entries(tv), entries(ov)) {
		push(&brUnit{key: vpre + "/" + p, section: "volumes", siteSlug: ss, label: ss + " · volume " + vs + " · " + p, b: ent(ebm.get(p)), t: ent(etm.get(p)), o: ent(eom.get(p)),
			set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
				seed := brContentTreeOf(brVolumeOf(cfg, ss, vs))
				if seed == nil {
					return
				}
				list := brListInit(seed, "entries")
				i := brIndex(list, pkf, p)
				if !s.Has {
					if i >= 0 {
						seed.Set("entries", append(list[:i:i], list[i+1:]...))
					}
					return
				}
				v, _ := brClone(s.V).(*doc.Map)
				e := doc.Obj("path", p).Spread(v)
				if i >= 0 {
					list[i] = e
				} else {
					list = append(list, e)
				}
				sort.SliceStable(list, func(a, b int) bool { return brLessUTF16(pkf(list[a]), pkf(list[b])) })
				seed.Set("entries", list)
			},
			blob: brContentBlob, blobMeta: doc.Obj("volumeSlug", vs, "path", p), note: note})
	}
	dset := func(v *doc.Map) map[string]bool {
		m := map[string]bool{}
		for _, d := range dirs(v) {
			m[brSelf(d)] = true
		}
		return m
	}
	bd, td, od := dset(bv), dset(tv), dset(ov)
	for _, d := range brKeysOf(brSelf, dirs(bv), dirs(tv), dirs(ov)) {
		ds := func(set map[string]bool) Slot {
			if set[d] {
				return brSlot("empty folder")
			}
			return brNone
		}
		b := brNone
		if bv != nil {
			b = ds(bd)
		}
		push(&brUnit{key: vpre + "/" + d + "/", section: "volumes", siteSlug: ss, label: ss + " · volume " + vs + " · " + d + "/", b: b, t: ds(td), o: ds(od),
			set: func(cfg *doc.Map, s Slot, _ *doc.Map) {
				seed := brContentTreeOf(brVolumeOf(cfg, ss, vs))
				if seed == nil {
					return
				}
				list := brListInit(seed, "dirs")
				i := brIndex(list, brSelf, d)
				if !s.Has && i >= 0 {
					seed.Set("dirs", append(list[:i:i], list[i+1:]...))
				}
				if s.Has && i < 0 {
					list = append(list, d)
					sort.SliceStable(list, func(a, b int) bool { return brLessUTF16(brSelf(list[a]), brSelf(list[b])) })
					seed.Set("dirs", list)
				}
			},
			note: note})
	}
}

// ── classification

func brClassify(hasBase bool, b, t, o Slot) string {
	if brSame(t, o) {
		return ""
	}
	if !hasBase {
		if t.Has && !o.Has {
			return "add"
		}
		if !t.Has && o.Has {
			return "remove"
		}
		return "change"
	}
	if brSame(t, b) {
		return "ours"
	}
	if brSame(o, b) {
		return "theirs"
	}
	return "conflict"
}

// ClassifyItem is TS classifyItem (b nil = two-way; nil result = source and target are equal).
func ClassifyItem(b *Slot, t, o Slot) any {
	var k string
	if b == nil {
		k = brClassify(false, brNone, t, o)
	} else {
		k = brClassify(true, *b, t, o)
	}
	if k == "" {
		return nil
	}
	return k
}

func brEffectOf(t, o Slot) string {
	if t.Has && !o.Has {
		return "add"
	}
	if !t.Has && o.Has {
		return "remove"
	}
	return "change"
}

func brMasked(has bool, value any) *doc.Map { return doc.Obj("has", has, "value", value) }

func brShown(u *brUnit, s Slot) *doc.Map {
	if !s.Has {
		return brMasked(false, nil)
	}
	if u.secretValue {
		if s.V == nil {
			return brMasked(true, "not set")
		}
		return brMasked(true, "set")
	}
	if u.section == "devTools" && brIsObject(s.V) {
		return brMasked(true, MaskDevToolLiterals(s.V))
	}
	if strings.HasPrefix(u.key, "sites/") && !strings.Contains(u.key[len("sites/"):], "/") && brIsObject(s.V) {
		return brMasked(true, brMaskSiteDevTools(s.V))
	}
	return brMasked(true, s.V)
}

func brMaskSiteDevTools(v any) any {
	s, _ := v.(*doc.Map)
	c := s.Map("config")
	l := c.Arr("devTools")
	if len(l) == 0 {
		return v
	}
	ml := make([]any, len(l))
	for i, p := range l {
		ml[i] = MaskDevToolLiterals(p)
	}
	return s.Shallow().Set("config", c.Shallow().Set("devTools", ml))
}

func brToItem(u *brUnit, kind string, hasBase bool, skip *doc.Map) *doc.Map {
	var base any
	if hasBase {
		base = brShown(u, u.b)
	}
	effect := brEffectOf(u.t, u.o)
	it := doc.Obj(
		"key", u.key, "section", u.section, "siteSlug", u.siteSlug, "label", u.label, "kind", kind, "effect", effect,
		"base", base, "target", brShown(u, u.o), "source", brShown(u, u.t), "preselect", false,
		"scalar", !u.secretValue && (!u.t.Has || brIsScalar(u.t.V)) && (!u.o.Has || brIsScalar(u.o.V)) && (u.t.Has || u.o.Has),
		"requires", []any{}, "locked", nil, "sourceHash", ItemHash(u.t),
	)
	if u.blob != nil {
		var bb any
		if hasBase {
			bb = u.blob(u.b)
		}
		it.Set("blob", doc.Obj("base", bb, "target", u.blob(u.o), "source", u.blob(u.t)).Spread(u.blobMeta))
	}
	if u.note != "" && (effect == "remove" || strings.Contains(u.key, "/content")) {
		it.Set("note", u.note)
	}
	if skip != nil {
		it.Set("skippedAt", brUnd(skip, "skippedAt"))
	}
	return it
}

type brPlanned struct {
	items []any
	byKey map[string]*brUnit
}

func brPlanUnits(base, source, target, opts *doc.Map) brPlanned {
	skips := brSkipsByKey(opts.Arr("skipped"))
	p := brPlanned{items: []any{}, byKey: map[string]*brUnit{}}
	for _, u := range brBuildUnits(base, source, target, opts) {
		k := brClassify(base != nil, u.b, u.t, u.o)
		if k == "" {
			continue
		}
		s := skips[u.key]
		// a source change NOT taken in an earlier merge: the base moved to the source, so it comes back as "ours"
		// (#94 rule); a theirs / conflict item with the same source value is the same skipped change
		skipped := (k == "ours" || k == "theirs" || k == "conflict") && s != nil && brEq(brUnd(s, "theirsHash"), ItemHash(u.t))
		if skipped {
			k = "skipped"
		} else {
			s = nil
		}
		p.items = append(p.items, brToItem(u, k, base != nil, s))
		p.byKey[u.key] = u
	}
	// requires: only keys that are items of this plan and are not yet satisfied in the target
	effects := map[string]string{}
	for _, x := range p.items {
		it := x.(*doc.Map)
		if _, ok := effects[it.Str("key")]; !ok {
			effects[it.Str("key")] = it.Str("effect")
		}
	}
	for _, x := range p.items {
		it := x.(*doc.Map)
		key := it.Str("key")
		u := p.byKey[key]
		if u.refs == nil || it.Str("effect") == "remove" {
			continue
		}
		req := []any{}
		seen := map[string]bool{}
		for _, k := range u.refs(source) {
			if seen[k] {
				continue
			}
			seen[k] = true
			// a reference needs the item only when the TARGET lacks the thing (taking it adds it)
			if k != key && effects[k] == "add" {
				req = append(req, k)
			}
		}
		it.Set("requires", req)
	}
	ff := IsFastForward(p.items)
	for _, x := range p.items {
		if doc.Truthy(opts.Val("preselectAll")) || (ff && opts.Val("preselectFastForward") != any(false)) {
			x.(*doc.Map).Set("preselect", true)
		}
	}
	return p
}

// IsFastForward is TS isFastForward.
func IsFastForward(items []any) bool {
	for _, x := range items {
		m, _ := x.(*doc.Map)
		if k := m.Str("kind"); k != "theirs" && k != "add" {
			return false
		}
	}
	return len(items) > 0
}

// PlanMerge is TS planMerge (base nil = two-way; opts: PlanOptions, may be nil).
func PlanMerge(base, source, target, opts *doc.Map) []any {
	return brPlanUnits(base, source, target, opts).items
}

// CountMerge is TS countMerge.
func CountMerge(items []any) *doc.Map {
	c := doc.New()
	for _, k := range MERGE_KINDS {
		c.Set(k, 0.0)
	}
	for _, x := range items {
		m, _ := x.(*doc.Map)
		c.Set(m.Str("kind"), c.Num(m.Str("kind"))+1)
	}
	return c
}

// CleanSourceChoices is TS cleanSourceChoices.
func CleanSourceChoices(items []any) []any {
	out := []any{}
	for _, x := range items {
		i, _ := x.(*doc.Map)
		k := i.Str("kind")
		if (k == "theirs" || k == "add" || k == "change" || k == "skipped") && !doc.Truthy(i.Val("locked")) {
			out = append(out, doc.Obj("key", brUnd(i, "key"), "take", "source"))
		}
	}
	return out
}

// RemovalChoices is TS removalChoices.
func RemovalChoices(items []any) []any {
	out := []any{}
	for _, x := range items {
		i, _ := x.(*doc.Map)
		if i.Str("effect") == "remove" && !doc.Truthy(i.Val("locked")) && i.Str("kind") != "conflict" {
			out = append(out, doc.Obj("key", brUnd(i, "key"), "take", "source"))
		}
	}
	return out
}

func brChoiceError(key, message string) *doc.Map { return doc.Obj("key", key, "message", message) }

// brChosen reads the choices: last one per key, Map insertion order, "chosen twice" errors.
func brChosen(choices []any, errs *[]any) (map[string]*doc.Map, []string) {
	chosen := map[string]*doc.Map{}
	var order []string
	for _, x := range choices {
		c, _ := x.(*doc.Map)
		key, ok := c.Val("key").(string)
		if !ok {
			continue
		}
		if _, dup := chosen[key]; dup {
			*errs = append(*errs, brChoiceError(key, "chosen twice"))
		} else {
			order = append(order, key)
		}
		chosen[key] = c
	}
	return chosen, order
}

func brDropEmptyDevTools(cfg *doc.Map) {
	for _, x := range cfg.Arr("sites") {
		s, _ := x.(*doc.Map)
		c := s.Map("config")
		if l, ok := c.Val("devTools").([]any); ok && len(l) == 0 {
			c.Delete("devTools")
		}
	}
}

// ApplyMerge is TS applyMerge: { config, taken, notTaken, unresolved, errors, items, kind } (opts: PlanOptions + values).
func ApplyMerge(base, source, target *doc.Map, choices []any, opts *doc.Map) *doc.Map {
	planned := brPlanUnits(base, source, target, opts.Shallow().Set("preselectFastForward", false))
	cfg := brCloneMap(target)
	errs := []any{}
	chosen, chosenOrder := brChosen(choices, &errs)
	taken, notTaken, unresolved := []any{}, []any{}, []any{}
	takenSet := map[string]bool{}
	for _, x := range planned.items {
		it := x.(*doc.Map)
		key := it.Str("key")
		u := planned.byKey[key]
		choice := chosen[key]
		if choice == nil || choice.Val("take") == any("target") {
			if it.Str("kind") == "conflict" && choice == nil {
				unresolved = append(unresolved, key)
			}
			if brContains(SOURCE_CHANGE_KINDS, it.Str("kind")) {
				notTaken = append(notTaken, it)
			}
			continue
		}
		switch choice.Val("take") {
		case "source":
			u.set(cfg, u.t, source)
		case "custom":
			v := brUnd(choice, "value")
			if !it.Bool("scalar") || !brIsScalar(v) {
				errs = append(errs, brChoiceError(key, "a custom value is only possible for a scalar item (string, number, true / false, null)"))
				continue
			}
			u.set(cfg, Slot{Has: true, V: v}, source)
		default:
			errs = append(errs, brChoiceError(key, "take must be source, target or custom"))
			continue
		}
		taken = append(taken, key)
		takenSet[key] = true
	}
	for _, k := range chosenOrder {
		if _, ok := planned.byKey[k]; !ok {
			errs = append(errs, brChoiceError(k, "no such change in the plan (a branch moved — refresh)"))
		}
	}
	opts.Map("values").Each(func(slug string, value any) {
		if v := brFind(cfg.Arr("variables"), "variableSlug", slug); v != nil {
			v.Set("value", brClone(value))
		} else {
			errs = append(errs, brChoiceError("variables/"+slug+"/value", "variable "+slug+" is not in the result"))
		}
	})
	brDropEmptyDevTools(cfg)
	kind := "merge"
	for _, x := range planned.items {
		it := x.(*doc.Map)
		if brContains(SOURCE_CHANGE_KINDS, it.Str("kind")) && !takenSet[it.Str("key")] {
			kind = "cherry-pick"
			break
		}
	}
	return doc.Obj("config", cfg, "taken", taken, "notTaken", notTaken, "unresolved", unresolved, "errors", errs, "items", planned.items, "kind", kind)
}

// NextSkips is TS nextSkips (sourceSeq: number or nil).
func NextSkips(notTaken, previous []any, sourceSeq any, now, by string) []any {
	prev := brSkipsByKey(previous)
	out := []any{}
	for _, x := range notTaken {
		c, _ := x.(*doc.Map)
		if k := c.Str("kind"); k == "ours" || k == "remove" {
			continue
		}
		if p := prev[brJSString(brUnd(c, "key"))]; p != nil && brEq(brUnd(p, "theirsHash"), brUnd(c, "sourceHash")) {
			out = append(out, p)
			continue
		}
		out = append(out, doc.Obj("key", brUnd(c, "key"), "theirsHash", brUnd(c, "sourceHash"), "sourceSeq", sourceSeq, "skippedAt", now, "skippedBy", by))
	}
	return out
}

// MergeMissing is TS mergeMissing.
func MergeMissing(cfg *doc.Map) *doc.Map { return brMissing(cfg) }

// ── working copy / history helpers

// UncommittedItems is TS uncommittedItems (head nil = empty config).
func UncommittedItems(head, working, opts *doc.Map) []any {
	if head == nil {
		head = brEmptyConfig()
	}
	return PlanMerge(nil, working, head, opts.Shallow().Set("preselectFastForward", false))
}

// UncommittedCount is TS uncommittedCount.
func UncommittedCount(head, working, opts *doc.Map) int {
	return len(UncommittedItems(head, working, opts))
}

// CommitStats is TS commitStats.
func CommitStats(parent, config, opts *doc.Map) *doc.Map {
	items := UncommittedItems(parent, config, opts)
	var added, changed, removed float64
	keys := []any{}
	for i, x := range items {
		it, _ := x.(*doc.Map)
		switch it.Str("effect") {
		case "add":
			added++
		case "remove":
			removed++
		default:
			changed++
		}
		if i < COMMIT_STATS_KEYS_MAX {
			keys = append(keys, brUnd(it, "key"))
		}
	}
	return doc.Obj("added", added, "changed", changed, "removed", removed, "keys", keys, "truncated", len(items) > COMMIT_STATS_KEYS_MAX)
}

// RevertPlan is TS revertPlan (parentConfig nil = empty config).
func RevertPlan(commitConfig, parentConfig, head, opts *doc.Map) []any {
	if parentConfig == nil {
		parentConfig = brEmptyConfig()
	}
	items := PlanMerge(commitConfig, parentConfig, head, opts.Shallow().Set("preselectFastForward", false))
	out := []any{}
	for _, x := range items {
		i, _ := x.(*doc.Map)
		if i.Str("kind") == "theirs" {
			i.Set("preselect", true)
		}
		// "ours" = a later commit changed something the reverted commit did not touch: nothing to undo
		if i.Str("kind") != "ours" {
			out = append(out, i)
		}
	}
	return out
}

func brPicked(choices []any) map[string]*doc.Map {
	picked := map[string]*doc.Map{}
	for _, x := range choices {
		if c, ok := x.(*doc.Map); ok {
			picked[brJSString(brUnd(c, "key"))] = c
		}
	}
	return picked
}

// ApplyRevert is TS applyRevert.
func ApplyRevert(commitConfig, parentConfig, head *doc.Map, choices []any, opts *doc.Map) *doc.Map {
	if parentConfig == nil {
		parentConfig = brEmptyConfig()
	}
	all := PlanMerge(commitConfig, parentConfig, head, opts)
	picked := brPicked(choices)
	// clean undo items are taken unless the caller said otherwise; conflicts need a choice
	eff := []any{}
	for _, x := range all {
		i, _ := x.(*doc.Map)
		if i.Str("kind") == "ours" {
			continue
		}
		if c := picked[i.Str("key")]; c != nil {
			eff = append(eff, c)
		} else if i.Str("kind") == "theirs" {
			eff = append(eff, doc.Obj("key", i.Str("key"), "take", "source"))
		}
	}
	return ApplyMerge(commitConfig, parentConfig, head, eff, opts)
}

// ResetPlan is TS resetPlan.
func ResetPlan(olderConfig, head, opts *doc.Map) []any {
	return PlanMerge(nil, olderConfig, head, opts.Shallow().Set("preselectAll", true))
}

// RebaseChanges is TS rebaseChanges: { config, items, conflicts, taken, clean, errors }.
func RebaseChanges(base, mine, head *doc.Map, choices []any, opts *doc.Map) *doc.Map {
	items := PlanMerge(base, mine, head, opts.Shallow().Set("preselectFastForward", false))
	chosen := brPicked(choices)
	eff := []any{}
	for _, x := range items {
		i, _ := x.(*doc.Map)
		k := i.Str("kind")
		if c := chosen[i.Str("key")]; c != nil {
			eff = append(eff, c)
		} else if k == "theirs" || k == "skipped" || (base == nil && (k == "add" || k == "change")) {
			eff = append(eff, doc.Obj("key", i.Str("key"), "take", "source"))
		}
	}
	r := ApplyMerge(base, mine, head, eff, opts)
	conflicts := []any{}
	for _, x := range items {
		i, _ := x.(*doc.Map)
		if i.Str("kind") == "conflict" && chosen[i.Str("key")] == nil {
			conflicts = append(conflicts, i)
		}
	}
	return doc.Obj("config", r.Val("config"), "items", items, "conflicts", conflicts, "taken", r.Val("taken"),
		"clean", len(r.Arr("unresolved")) == 0 && len(r.Arr("errors")) == 0, "errors", r.Val("errors"))
}

// SecretValueChanges is TS secretValueChanges.
func SecretValueChanges(before, after []any, generated []string) *doc.Map {
	var set, gen, cleared []string
	for _, x := range after {
		a, _ := x.(*doc.Map)
		slug := brJSString(brUnd(a, "secretSlug"))
		bv := brNul(brFind(before, "secretSlug", slug), "value")
		av := brUnd(a, "value")
		if brEmptyVal(av) {
			if !brEmptyVal(bv) {
				cleared = append(cleared, slug)
			}
		} else if brContains(generated, slug) {
			gen = append(gen, slug)
		} else if !brEq(av, bv) {
			set = append(set, slug)
		}
	}
	brSortStrings(set)
	brSortStrings(gen)
	brSortStrings(cleared)
	return doc.Obj("set", brStrs(set), "generated", brStrs(gen), "cleared", brStrs(cleared))
}

// HasSecretValueChanges is TS hasSecretValueChanges (c may be nil).
func HasSecretValueChanges(c *doc.Map) bool {
	return c != nil && len(c.Arr("set"))+len(c.Arr("generated"))+len(c.Arr("cleared")) > 0
}

// WithReferenceSites is TS withReferenceSites: { config, updatedSites }.
func WithReferenceSites(own, refs *doc.Map, siteSlug string) *doc.Map {
	out := brCloneMap(own)
	updated := []any{}
	mine := brFind(out.Arr("sites"), "siteSlug", siteSlug)
	next := []any{}
	placed := false
	for _, x := range refs.Arr("sites") {
		s, _ := x.(*doc.Map)
		slug := brJSString(brUnd(s, "siteSlug"))
		if slug == siteSlug {
			if mine != nil {
				next = append(next, mine)
				placed = true
			}
			continue
		}
		prev := brFind(out.Arr("sites"), "siteSlug", slug)
		if prev == nil || brCanon(prev) != brCanon(s) {
			updated = append(updated, brUnd(s, "siteSlug"))
		}
		next = append(next, brClone(s))
	}
	if mine != nil && !placed {
		next = append(next, mine)
	}
	for _, x := range out.Arr("sites") {
		s, _ := x.(*doc.Map)
		slug := brJSString(brUnd(s, "siteSlug"))
		if slug != siteSlug && brFind(refs.Arr("sites"), "siteSlug", slug) == nil {
			updated = append(updated, brUnd(s, "siteSlug"))
		}
	}
	out.Set("sites", next)
	return doc.Obj("config", out, "updatedSites", updated)
}

var brSuggestPrefix = regexp.MustCompile(`^Variable |^Secret |^File `)

// SuggestMessage is TS suggestMessage (prefix "" = the TS default).
func SuggestMessage(items []any, prefix string) string {
	if len(items) == 0 {
		return doc.JSTrim(prefix)
	}
	n := func(e string) int {
		c := 0
		for _, x := range items {
			m, _ := x.(*doc.Map)
			if m.Str("effect") == e {
				c++
			}
		}
		return c
	}
	var parts []string
	if c := n("change"); c > 0 {
		parts = append(parts, strconv.Itoa(c)+" changed")
	}
	if c := n("add"); c > 0 {
		parts = append(parts, strconv.Itoa(c)+" added")
	}
	if c := n("remove"); c > 0 {
		parts = append(parts, strconv.Itoa(c)+" removed")
	}
	var names []string
	for i, x := range items {
		if i == 3 {
			break
		}
		m, _ := x.(*doc.Map)
		label := m.Str("label")
		if loc := brSuggestPrefix.FindStringIndex(label); loc != nil {
			label = label[loc[1]:]
		}
		names = append(names, label)
	}
	s := prefix + strings.Join(parts, " · ") + ": " + strings.Join(names, ", ")
	if len(items) > 3 {
		s += ", …"
	}
	if doc.JSLen(s) > COMMIT_SUMMARY_MAX {
		return doc.JSSlice(s, COMMIT_SUMMARY_MAX-1) + "…"
	}
	return s
}

// ── agent versions

// brParseInt is parseInt(x, 10) || 0.
func brParseInt(x string) float64 {
	x = strings.TrimLeftFunc(x, doc.IsJSSpace)
	i := 0
	for i < len(x) && x[i] >= '0' && x[i] <= '9' {
		i++
	}
	f, err := strconv.ParseFloat(x[:i], 64)
	if err != nil && i == 0 {
		return 0
	}
	return f
}

func brVerParts(v string) [3]float64 {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	out := [3]float64{math.NaN(), math.NaN(), math.NaN()} // a missing part is undefined: every comparison false
	for i, p := range strings.Split(v, ".") {
		if i < 3 {
			out[i] = brParseInt(p)
		}
	}
	return out
}

// SupportsBranches is TS supportsBranches (version: string or nil).
func SupportsBranches(version any) bool {
	if !doc.Truthy(version) {
		return false
	}
	v := brVerParts(brJSString(version))
	m := brVerParts(BRANCHES_MIN_AGENT)
	if v[0] != m[0] {
		return v[0] > m[0]
	}
	if v[1] != m[1] {
		return v[1] > m[1]
	}
	return v[2] >= m[2]
}

// NeedsAgentMessage is TS needsAgentMessage (version: string or nil).
func NeedsAgentMessage(siteSlug string, version any) string {
	v := "unknown"
	if !brIsNullish(version) {
		v = brJSString(version)
	}
	return siteSlug + " runs agent " + v + " — needs agent ≥ " + BRANCHES_MIN_AGENT + " for agent branches; it still receives this commit as a proposal (approve it in its agent GUI, whole commit only)"
}

// ── commits

// MakeCommit is TS makeCommit; i: NewCommitInput (error = the CanonicalError of a non-finite number in the config / catalog).
func MakeCommit(i *doc.Map) (*doc.Map, error) {
	secretValues := i.Map("branch").Val("kind") != any("template")
	parent := i.Map("parent")
	config := i.Map("config")
	hash, err := ConfigHash(config)
	if err != nil {
		return nil, err
	}
	var catalogHash any
	if doc.Truthy(i.Val("catalog")) {
		if catalogHash, err = CatalogHashOf(i.Val("catalog")); err != nil {
			return nil, err
		}
	}
	data := brNul(i, "data")
	if data == nil {
		data = doc.New()
	}
	return doc.Obj(
		"commitId", brUnd(i, "commitId"), "branch", brUnd(i, "branch"), "seq", parent.Num("seq")+1, "parentId", brNul(parent, "commitId"),
		"kind", brUnd(i, "kind"), "message", doc.JSTrim(i.Str("message")), "author", brUnd(i, "author"), "createdAt", brUnd(i, "createdAt"),
		"source", brNul(i, "source"), "revertOf", brNul(i, "revertOf"), "resetTo", brNul(i, "resetTo"), "rollbackOf", brNul(i, "rollbackOf"),
		"config", config, "hash", hash, "catalog", brNul(i, "catalog"), "catalogHash", catalogHash,
		"stats", CommitStats(parent.Map("config"), config, doc.Obj("secretValues", secretValues, "scope", brNul(i, "scope"))),
		"data", data,
	), nil
}

// CommitRef is TS commitRef.
func CommitRef(c *doc.Map) *doc.Map {
	return brPick(c, "commitId", "seq", "hash", "message", "author", "createdAt", "kind")
}

// CommitSummaryOf is TS commitSummaryOf.
func CommitSummaryOf(c *doc.Map) *doc.Map {
	return brPick(c, "commitId", "branch", "seq", "parentId", "kind", "message", "author", "createdAt", "source", "revertOf", "resetTo", "rollbackOf", "hash", "stats")
}

// SourcePicks is TS sourcePicks.
func SourcePicks(taken []string, kind string) *doc.Map {
	if kind == "merge" {
		return doc.Obj("picks", "all")
	}
	if len(taken) > COMMIT_PICKS_MAX {
		return doc.Obj("picks", brStrs(taken[:COMMIT_PICKS_MAX]), "truncated", true)
	}
	return doc.Obj("picks", brStrs(taken))
}

// VerifyCommitLink is TS verifyCommitLink: the problem as a string, nil when the link is good (error: a non-finite number).
func VerifyCommitLink(c, parent *doc.Map) (any, error) {
	id := brJSString(brUnd(c, "commitId"))
	hash, err := ConfigHash(c.Map("config"))
	if err != nil {
		return nil, err
	}
	if !brEq(hash, brUnd(c, "hash")) {
		return "commit " + id + ": hash does not match its config", nil
	}
	if doc.Truthy(c.Val("catalog")) && doc.Truthy(c.Val("catalogHash")) {
		h, err := CatalogHashOf(c.Val("catalog"))
		if err != nil {
			return nil, err
		}
		if !brEq(h, c.Val("catalogHash")) {
			return "commit " + id + ": catalogHash does not match its catalog", nil
		}
	}
	if parent != nil {
		if !brEq(brUnd(c, "parentId"), brUnd(parent, "commitId")) {
			return "commit " + id + ": parent " + brJSString(brUnd(c, "parentId")) + " is not " + brJSString(brUnd(parent, "commitId")), nil
		}
		want, ok := parent.NumOK("seq")
		if !ok {
			want = math.NaN()
		}
		want++
		if !brEq(brUnd(c, "seq"), want) {
			return "commit " + id + ": seq " + brJSString(brUnd(c, "seq")) + " is not " + brNumString(want), nil
		}
	}
	return nil, nil
}

// MaskCommit is TS maskCommit.
func MaskCommit(c *doc.Map, maskConfig func(*doc.Map) *doc.Map) *doc.Map {
	return c.Shallow().Set("config", maskConfig(c.Map("config")))
}
