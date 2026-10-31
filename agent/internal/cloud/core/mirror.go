package core

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/ulid"
)

// ───────────────────────────────────────── the mirror part of shared/src/github-mirror.ts

// MIRROR_EXPORT_FORMAT is MIRROR_EXPORT_FORMAT.
const MIRROR_EXPORT_FORMAT = 2

// MIRROR_MAX_FILE_BYTES is MIRROR_MAX_FILE_BYTES.
const MIRROR_MAX_FILE_BYTES = 100 * 1024 * 1024

// MIRROR_DEFAULT_BRANCH is MIRROR_DEFAULT_BRANCH.
const MIRROR_DEFAULT_BRANCH = "main"

// git trailers (MIRROR_TRAILERS)
const (
	trailerCommit = "OPM-Commit"
	trailerBranch = "OPM-Branch"
	trailerSeq    = "OPM-Seq"
	trailerVolume = "OPM-Volume-Version"
	trailerDrift  = "OPM-Drift"
)

var mirrorBackoff = []int{30, 60, 120, 300, 900, 1800, 3600}

// MirrorBackoffSec is mirrorBackoffSec.
func MirrorBackoffSec(attempts int) int {
	i := min(len(mirrorBackoff)-1, max(0, attempts-1))
	return mirrorBackoff[i]
}

// GithubOwnerRE is GITHUB_OWNER_RE.
var GithubOwnerRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

var githubRepoRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// GithubRepoOK is GITHUB_REPO_RE.test.
func GithubRepoOK(s string) bool { return s != "." && s != ".." && githubRepoRE.MatchString(s) }

// GithubTokenRE is GITHUB_TOKEN_RE.
var GithubTokenRE = regexp.MustCompile(`^[\x21-\x7e]{20,255}$`)

var tokenPrefix = regexp.MustCompile(`^(github_pat_|ghp_|gho_|ghu_|ghs_|ghr_)`)

// MaskGithubToken is maskGithubToken: nil for no token.
func MaskGithubToken(token string) any {
	if token == "" {
		return nil
	}
	tail := token
	if u := utf16.Encode([]rune(token)); len(u) > 4 {
		tail = string(utf16.Decode(u[len(u)-4:]))
	}
	return tokenPrefix.FindString(token) + "•••" + tail
}

var entitySlugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

// MirrorBranchName is mirrorBranchName.
func MirrorBranchName(kind, entitySlug, siteSlug string) (string, error) {
	if !entitySlugRE.MatchString(entitySlug) {
		return "", fmt.Errorf("bad entity slug %s", doc.Quote(entitySlug))
	}
	switch kind {
	case "template":
		return "template/" + entitySlug, nil
	case "deployment":
		return "deployment/" + entitySlug + "/cloud", nil
	}
	if !entitySlugRE.MatchString(siteSlug) {
		q := "null"
		if siteSlug != "" {
			q = doc.Quote(siteSlug)
		}
		return "", fmt.Errorf("bad site slug %s", q)
	}
	return "deployment/" + entitySlug + "/" + kind + "/" + siteSlug, nil
}

// MirrorBranchDocId is mirrorBranchDocId.
func MirrorBranchDocId(gitBranch string) string { return strings.ReplaceAll(gitBranch, "/", "~") }

var reCRLF = regexp.MustCompile(`\r\n?`)
var reNewlines = regexp.MustCompile(`[\r\n]+`)

// MirrorCommitMessage is mirrorCommitMessage; trailers: key, value pairs.
func MirrorCommitMessage(message string, trailers ...[2]string) string {
	body := strings.TrimRightFunc(reCRLF.ReplaceAllString(message, "\n"), doc.IsJSSpace)
	lines := make([]string, len(trailers))
	for i, t := range trailers {
		lines[i] = t[0] + ": " + doc.JSTrim(reNewlines.ReplaceAllString(t[1], " "))
	}
	return body + "\n\n" + strings.Join(lines, "\n") + "\n"
}

var reParas = regexp.MustCompile(`\n[` + jsSpace + `]*\n`)
var reTrailer = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]*):[` + jsSpace + `]?([^\n\r\x{2028}\x{2029}]*)$`)

// ParseMirrorTrailers is parseMirrorTrailers.
func ParseMirrorTrailers(message string) map[string]string {
	paras := reParas.Split(doc.JSTrim(reCRLF.ReplaceAllString(message, "\n")), -1)
	out := map[string]string{}
	for _, line := range strings.Split(paras[len(paras)-1], "\n") {
		m := reTrailer.FindStringSubmatch(line)
		if m == nil {
			return map[string]string{}
		}
		out[m[1]] = m[2]
	}
	return out
}

// MirrorSitePerson is mirrorSitePerson.
func MirrorSitePerson(siteSlug string) *doc.Map {
	return doc.Obj("name", "OPM site "+siteSlug, "email", "opm-site-"+siteSlug+"@users.noreply.opm")
}

// MirrorOrderKey is mirrorOrderKey; seq: number or nil.
func MirrorOrderKey(createdAt string, seq any, opmID string) string {
	s := "0"
	if !nullish(seq) {
		s = JSString(seq)
	}
	if n := 10 - len(s); n > 0 {
		s = strings.Repeat("0", n) + s
	}
	return createdAt + "|" + s + "|" + opmID
}

var reItemID = regexp.MustCompile(`[^A-Za-z0-9~_.-]`)

// MirrorItemId is mirrorItemId.
func MirrorItemId(gitBranch, op, opmID string) string {
	if opmID == "" {
		opmID = "-"
	}
	return reItemID.ReplaceAllString(MirrorBranchDocId(gitBranch)+"__"+op+"__"+opmID, "_")
}

// CompareUtf16 is compareUtf16: the order of JS string comparison.
func CompareUtf16(a, b string) int {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return int(x[i]) - int(y[i])
		}
	}
	return len(x) - len(y)
}

// ───────────────────────────────────────── core/mirror.ts

type mcolT struct{}

// Mcol is mcol.
var Mcol mcolT

func (mcolT) Config() *driver.DocRef { return cloud.DB().Collection("githubMirror").Doc("config") }
func (m mcolT) Branches() *driver.CollectionRef {
	return m.Config().Collection("branches")
}
func (m mcolT) Queue() *driver.CollectionRef { return m.Config().Collection("queue") }
func (m mcolT) Blobs() *driver.CollectionRef { return m.Config().Collection("blobs") }

func emptyMirror() *doc.Map {
	return doc.Obj("enabled", false, "owner", "", "repo", "", "token", nil, "tokenSetAt", nil, "tokenSetBy", nil, "initializedAt", nil, "lastTest", nil, "pausedUntil", nil, "pauseReason", nil, "createdAt", "", "createdBy", "", "updatedAt", "", "updatedBy", "")
}

// LoadMirror is loadMirror.
func LoadMirror() (*doc.Map, error) {
	s, err := Mcol.Config().Get()
	if err != nil {
		return nil, err
	}
	m := emptyMirror()
	if d := s.Data(); d != nil {
		m.Spread(d)
	}
	return m, nil
}

var (
	mirrorCacheMu sync.Mutex
	mirrorCacheAt time.Time
	mirrorCacheM  *doc.Map
)

// MirrorCached is mirrorCached: cached ≤ 15 s per instance (hooks on hot paths: heartbeat).
func MirrorCached() (*doc.Map, error) {
	mirrorCacheMu.Lock()
	m, at := mirrorCacheM, mirrorCacheAt
	mirrorCacheMu.Unlock()
	if m != nil && time.Since(at) < 15*time.Second && !cloud.Env.Emulator() {
		return m, nil
	}
	m, err := LoadMirror()
	if err != nil {
		return nil, err
	}
	mirrorCacheMu.Lock()
	mirrorCacheM, mirrorCacheAt = m, time.Now()
	mirrorCacheMu.Unlock()
	return m, nil
}

// InvalidateMirrorCache is invalidateMirrorCache.
func InvalidateMirrorCache() {
	mirrorCacheMu.Lock()
	mirrorCacheM = nil
	mirrorCacheMu.Unlock()
}

// IsConfigured is isConfigured.
func IsConfigured(m *doc.Map) bool {
	return m.Truthy("owner") && m.Truthy("repo") && m.Truthy("token")
}

// IsActive is isActive.
func IsActive(m *doc.Map) bool { return m.Truthy("enabled") && IsConfigured(m) }

// RepoKeyOf is repoKeyOf.
func RepoKeyOf(m *doc.Map) string {
	return strings.ToLower(m.Str("owner")) + "/" + strings.ToLower(m.Str("repo"))
}

// MirrorApiBaseOf is apiBaseOf of core/mirror.ts.
func MirrorApiBaseOf(m *doc.Map) string {
	if cloud.Env.Emulator() && m.Truthy("_apiBase") {
		return m.Str("_apiBase")
	}
	return GITHUB_API_DEFAULT
}

// ClientOf is clientOf.
func ClientOf(m *doc.Map) *GithubClient {
	return NewGithubClient(m.Str("token"), m.Str("owner"), m.Str("repo"), MirrorApiBaseOf(m))
}

// RepoUrlOf is repoUrlOf.
func RepoUrlOf(m *doc.Map) any {
	if m.Truthy("owner") && m.Truthy("repo") {
		return "https://github.com/" + m.Str("owner") + "/" + m.Str("repo")
	}
	return nil
}

func orNull(m *doc.Map, k string) any {
	if m.Truthy(k) {
		return m.Val(k)
	}
	return nil
}

func openStates() []any { return []any{"pending", "failed", "running"} }

// MirrorView is mirrorView.
func MirrorView(m *doc.Map) (*doc.Map, error) {
	branches, err := Mcol.Branches().Select("status", "pushedAt").Get()
	if err != nil {
		return nil, err
	}
	pending, err := Mcol.Queue().Where("state", "in", openStates()).Select("state").Get()
	if err != nil {
		return nil, err
	}
	synced, queued, errs := 0, 0, 0
	var lastPushAt any
	for _, d := range branches.Docs {
		x := d.Data()
		switch x.Str("status") {
		case "synced":
			synced++
		case "queued", "pushing":
			queued++
		case "error", "paused":
			errs++
		}
		if p := x.Str("pushedAt"); p != "" {
			if l, ok := lastPushAt.(string); !ok || p > l {
				lastPushAt = p
			}
		}
	}
	return doc.Obj(
		"configured", IsConfigured(m),
		"enabled", m.Val("enabled"),
		"owner", m.Val("owner"),
		"repo", m.Val("repo"),
		"repoUrl", RepoUrlOf(m),
		"token", MaskGithubToken(m.Str("token")),
		"tokenSet", m.Truthy("token"),
		"tokenSetAt", m.Val("tokenSetAt"),
		"tokenSetBy", m.Val("tokenSetBy"),
		"initializedAt", m.Val("initializedAt"),
		"lastTest", m.Val("lastTest"),
		"pausedUntil", m.Val("pausedUntil"),
		"pauseReason", m.Val("pauseReason"),
		"summary", doc.Obj("branches", branches.Size(), "synced", synced, "queued", queued, "errors", errs, "pendingItems", pending.Size(), "lastPushAt", lastPushAt),
		"exportFormat", MIRROR_EXPORT_FORMAT,
		"updatedAt", orNull(m, "updatedAt"),
		"updatedBy", orNull(m, "updatedBy"),
	), nil
}

// MirrorTarget is MirrorTarget.
type MirrorTarget struct {
	GitBranch  string
	Ref        *doc.Map
	Label      string
	EntitySlug string
}

func strOrNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// RefOf is refOf.
func RefOf(kind, templateID, deploymentID, siteSlug string) *doc.Map {
	r := doc.Obj("kind", kind, "templateId", nil, "deploymentId", nil, "siteSlug", nil)
	if kind == "template" {
		r.Set("templateId", strOrNull(templateID))
	} else {
		r.Set("deploymentId", strOrNull(deploymentID))
	}
	if kind == "agent" || kind == "uncommitted" {
		r.Set("siteSlug", strOrNull(siteSlug))
	}
	return r
}

func labelOr(d *doc.Map, slug string) string {
	if v := d.Val("label"); !nullish(v) {
		return JSString(v)
	}
	return slug
}

// TargetOf is targetOf: the git branch of a template / deployment / agent / uncommitted branch (assigns the entity slug
// when missing).
func TargetOf(ref *doc.Map) (*MirrorTarget, error) {
	if ref.Val("kind") == "template" {
		slug, err := EnsureSlug("template", ref.Str("templateId"))
		if err != nil {
			return nil, err
		}
		t, err := cloud.Col.Templates().Doc(ref.Str("templateId")).Get()
		if err != nil {
			return nil, err
		}
		gb, err := MirrorBranchName("template", slug, "")
		if err != nil {
			return nil, err
		}
		return &MirrorTarget{gb, ref, labelOr(t.Data(), slug), slug}, nil
	}
	slug, err := EnsureSlug("deployment", ref.Str("deploymentId"))
	if err != nil {
		return nil, err
	}
	d, err := cloud.Col.Deployments().Doc(ref.Str("deploymentId")).Get()
	if err != nil {
		return nil, err
	}
	label := labelOr(d.Data(), slug)
	kind := ref.Str("kind")
	if kind == "deployment" {
		gb, err := MirrorBranchName("deployment", slug, "")
		if err != nil {
			return nil, err
		}
		return &MirrorTarget{gb, ref, label, slug}, nil
	}
	site := ref.Str("siteSlug")
	gb, err := MirrorBranchName(kind, slug, site)
	if err != nil {
		return nil, err
	}
	label += " · " + site
	if kind != "agent" {
		label += " (uncommitted)"
	}
	return &MirrorTarget{gb, ref, label, slug}, nil
}

// EnqueueOpts is EnqueueOpts. Seq: number or nil. At "" = due now.
type EnqueueOpts struct {
	Op, OpmID string
	Seq       any
	CreatedAt string
	Reason    string
	At        string
}

// Enqueue is enqueue: create a queue item (idempotent by itemId) + mark the branch queued. False when it already existed.
func Enqueue(t *MirrorTarget, o EnqueueOpts) (bool, error) {
	now := doc.NowISO()
	if cloud.Env.Emulator() {
		// EMULATOR test hook: only the entities a test lists (the emulator DB holds every earlier test's data)
		m, err := MirrorCached()
		if err != nil {
			return false, err
		}
		if sc, ok := m.Val("_scope").([]any); ok {
			id := t.Ref.Val("templateId")
			if nullish(id) {
				id = t.Ref.Val("deploymentId")
			}
			if nullish(id) {
				id = ""
			}
			found := false
			for _, x := range sc {
				found = found || x == id
			}
			if !found {
				return false, nil
			}
		}
	}
	itemID := MirrorItemId(t.GitBranch, o.Op, o.OpmID)
	at := o.At
	if at == "" {
		at = now
	}
	item := doc.Obj("itemId", itemID, "gitBranch", t.GitBranch, "branch", t.Ref, "op", o.Op, "opmId", o.OpmID, "seq", o.Seq, "orderKey", MirrorOrderKey(o.CreatedAt, o.Seq, o.OpmID), "state", "pending", "attempts", 0, "nextAttemptAt", at, "createdAt", now, "startedAt", nil, "doneAt", nil, "gitSha", nil, "lastError", nil, "reason", o.Reason)
	if err := Mcol.Queue().Doc(itemID).Create(item); err != nil {
		if driver.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	if err := TouchBranch(t, doc.Obj("status", "queued")); err != nil {
		return false, err
	}
	return true, nil
}

// BranchRow is the row of a branch without a state.
func BranchRow(gitBranch string, ref *doc.Map, label, now string) *doc.Map {
	return doc.Obj("gitBranch", gitBranch, "branch", ref, "label", label, "status", "never", "last", nil, "gitSha", nil, "gitUrl", nil, "treeUrl", nil, "pushedAt", nil, "queue", doc.Obj("pending", 0, "failed", 0, "nextAttemptAt", nil), "lastError", nil, "notes", []any{}, "leaseUntil", nil, "updatedAt", now)
}

// TouchBranch is touchBranch: create the branch row if missing / set fields.
func TouchBranch(t *MirrorTarget, patch *doc.Map) error {
	ref := Mcol.Branches().Doc(MirrorBranchDocId(t.GitBranch))
	return cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		s, err := tx.Get(ref)
		if err != nil {
			return err
		}
		now := doc.NowISO()
		row := BranchRow(t.GitBranch, t.Ref, t.Label, now)
		if !s.Exists() {
			tx.Create(ref, doc.New().Spread(row).Spread(patch).Set("label", t.Label).Set("updatedAt", now))
			return nil
		}
		cur := s.Data()
		// a running push keeps "pushing"
		p := patch.Shallow()
		if p.Val("status") == "queued" && cur.Val("status") == "pushing" {
			p.Delete("status")
		}
		// the worker may have leased the branch first (acquire = set merge: no gitBranch / branch): the mirrorQueue trigger
		// of the item Enqueue created just before can run before this → complete such a row with the fields it lacks
		fill := doc.New()
		row.Each(func(k string, v any) {
			if !cur.Has(k) {
				fill.Set(k, v)
			}
		})
		tx.Update(ref, fill.Spread(p).Set("label", t.Label).Set("updatedAt", now))
		return nil
	})
}

// MirrorHook is mirrorHook: best effort, never fails (a mirror problem must never fail an OPM write).
func MirrorHook(name string, f func() error) {
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("%v", p)
			}
		}()
		m, err := MirrorCached()
		if err != nil {
			return err
		}
		if !IsActive(m) {
			return nil
		}
		return f()
	}()
	if err != nil {
		driver.Log.Warn("github mirror hook failed", "hook", name, "error", err.Error())
	}
}

// RunId is runId: a run id for catch-up / rebuild items.
func RunId(p string) string { return p + "-" + ulid.New() }

// AllTargets is allTargets: templates, deployments (not deleted), agent branches of those, uncommitted ones with drift.
func AllTargets() ([]*MirrorTarget, error) {
	var out []*MirrorTarget
	tpls, err := cloud.Col.Templates().Select("label", "slug").Get()
	if err != nil {
		return nil, err
	}
	deps, err := cloud.Col.Deployments().Where("deleted", "==", false).Select("label", "slug").Get()
	if err != nil {
		return nil, err
	}
	add := func(ref *doc.Map) error {
		t, err := TargetOf(ref)
		if err == nil {
			out = append(out, t)
		}
		return err
	}
	for _, d := range tpls.Docs {
		if err := add(RefOf("template", d.ID(), "", "")); err != nil {
			return nil, err
		}
	}
	for _, d := range deps.Docs {
		if err := add(RefOf("deployment", "", d.ID(), "")); err != nil {
			return nil, err
		}
		abs, err := Bcol.AgentBranches(d.ID()).Get()
		if err != nil {
			return nil, err
		}
		for _, a := range abs.Docs {
			data := a.Data()
			if data.Truthy("head") || !data.Truthy("legacy") {
				if err := add(RefOf("agent", "", d.ID(), a.ID())); err != nil {
					return nil, err
				}
			}
			if drift := data.Map("drift"); drift != nil && (len(drift.Arr("items")) > 0 || len(drift.Arr("notes")) > 0) {
				if err := add(RefOf("uncommitted", "", d.ID(), a.ID())); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}

// EnqueueAll is enqueueAll: queue a catch-up (or rebuild) of every branch ("" only = all): items created, branches matched.
func EnqueueAll(op, reason, only string) (enqueued, branches int, err error) {
	all, err := AllTargets()
	if err != nil {
		return 0, 0, err
	}
	id := RunId(reason)
	for _, t := range all {
		if only != "" && t.GitBranch != only {
			continue
		}
		branches++
		kindOp := op
		if t.Ref.Val("kind") == "uncommitted" {
			kindOp = "drift"
		}
		ok, err := Enqueue(t, EnqueueOpts{Op: kindOp, OpmID: id, CreatedAt: "9999", Reason: reason})
		if err != nil {
			return 0, 0, err
		}
		if ok {
			enqueued++
		}
	}
	return enqueued, branches, nil
}
