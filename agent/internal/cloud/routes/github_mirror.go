package routes

import (
	"errors"
	"math"
	"sort"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// §21 #106 Admin → GitHub (GITHUB_MIRROR_CLOUD_API, perm accounts.admin) + the agent drift report. The token is a
// credential stored like a registry password (masked everywhere, revealGithubToken = CREDENTIAL_OPS). Every change is
// audited by the router hook (data refined here).

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"getGithubMirror":          getGithubMirror,
		"patchGithubMirror":        patchGithubMirror,
		"testGithubMirror":         testGithubMirror,
		"revealGithubToken":        revealGithubToken,
		"listGithubMirrorBranches": listGithubMirrorBranches,
		"listGithubMirrorQueue":    listGithubMirrorQueue,
		"syncGithubMirror":         func(c *cloud.Ctx) (any, error) { return syncLike(c, "catch-up") },
		"resyncGithubMirror":       func(c *cloud.Ctx) (any, error) { return syncLike(c, "rebuild") },
		"agentPushDrift":           agentPushDrift,
	})
}

func mirrorAdmin(c *cloud.Ctx) error { return cloud.Need(c, "accounts", "admin", nil) }

func openStates() []any { return []any{"pending", "failed", "running"} }

// clearCollection: delete every document of a collection (small: branch rows / queue items of ONE repository).
func clearCollection(ref *driver.CollectionRef) error {
	for {
		s, err := ref.Limit(400).Get()
		if err != nil {
			return err
		}
		if s.Empty() {
			return nil
		}
		b := cloud.DB().Batch()
		for _, d := range s.Docs {
			b.Delete(d.Ref())
		}
		if err := b.Commit(); err != nil {
			return err
		}
	}
}

func checkErr(err error) (msg string, status int) {
	var ge *core.GithubError
	if errors.As(err, &ge) {
		if ge.Status == 401 {
			return "the token is invalid or expired (401)", ge.Status
		}
		return ge.Message, ge.Status
	}
	return err.Error(), 0
}

// testConnection is testConnection.
func testConnection(m *doc.Map) *doc.Map {
	at := doc.NowISO()
	checks := []*doc.Map{}
	add := func(name string, ok bool, msg string) {
		checks = append(checks, doc.Obj("name", name, "ok", ok, "message", msg))
	}
	fail := func(name string, err error) {
		msg, _ := checkErr(err)
		add(name, false, msg)
	}
	cl := core.NewGithubClient(m.Str("token"), m.Str("owner"), m.Str("repo"), core.MirrorApiBaseOf(m))
	var repo, rate, login any
	if l, err := cl.GetUser(); err != nil {
		fail("token", err)
	} else {
		msg := "token accepted"
		if l != "" {
			login = l
			msg = "token of " + l
		}
		add("token", true, msg)
	}
	var pushPerm any = doc.Undefined
	empty := false
	r, err := cl.GetRepo()
	switch {
	case err != nil:
		fail("repo", err)
	case r == nil:
		add("repo", false, "repository "+m.Str("owner")+"/"+m.Str("repo")+" not found, or the token cannot see it (fine-grained token: select the repository)")
	default:
		if v, ok := r.Map("permissions").Get("push"); ok {
			pushPerm = v
		}
		empty, _ = cl.IsEmpty()
		repo = doc.Obj("fullName", r.Val("full_name"), "private", r.Val("private"), "defaultBranch", r.Val("default_branch"), "empty", empty, "htmlUrl", r.Val("html_url"))
		vis := "PUBLIC — anyone can read the configs"
		if r.Truthy("private") {
			vis = "private"
		}
		suffix := ""
		if empty {
			suffix = ", empty: OPM creates the first commit on main"
		}
		add("repo", true, core.JSString(r.Val("full_name"))+" ("+vis+suffix+")")
	}
	if repo != nil {
		if _, err := cl.Req("GET", "/repos/"+doc.EncodeURIComponent(m.Str("owner"))+"/"+doc.EncodeURIComponent(m.Str("repo"))+"/git/matching-refs/heads/", nil, core.GhOpts{Ok409: true}); err != nil {
			fail("contents-read", err)
		} else {
			add("contents-read", true, "Contents: read")
		}
		if empty {
			// the Git Data API refuses an empty repository: the write check uses the repository permission
			if pushPerm == false {
				add("contents-write", false, "the token has no write access (Contents: read and write)")
			} else {
				add("contents-write", true, "Contents: write (verified at the first push: the repository is empty)")
			}
		} else if _, err := cl.CreateBlob([]byte("opm mirror write check " + at + "\n")); err != nil { // an unreferenced blob (garbage-collected by GitHub)
			fail("contents-write", err)
			if _, st := checkErr(err); st == 403 || st == 404 {
				checks[len(checks)-1].Set("message", "the token has no write access to the repository (fine-grained token: Contents → Read and write)")
			}
		} else {
			add("contents-write", true, "Contents: read and write")
		}
	}
	if rl, err := cl.RateLimit(); err != nil {
		fail("rate", err)
	} else if rl == nil {
		add("rate", true, "unknown")
	} else {
		rate = rl
		add("rate", rl.Num("remaining") > 0, core.JSString(rl.Val("remaining"))+"/"+core.JSString(rl.Val("limit"))+" requests left (resets "+rl.Str("resetAt")+")")
	}
	ok := true
	l := make([]any, len(checks))
	for i, x := range checks {
		ok = ok && x.Truthy("ok")
		l[i] = x
	}
	return doc.Obj("ok", ok, "at", at, "checks", l, "repo", repo, "login", login, "rate", rate)
}

// branchRows: status rows: every mirrorable branch + stored rows (uncommitted / deleted entities).
func branchRows() ([]any, error) {
	targets, err := core.AllTargets()
	if err != nil {
		return nil, err
	}
	stored, err := core.Mcol.Branches().Get()
	if err != nil {
		return nil, err
	}
	queue, err := core.Mcol.Queue().Where("state", "in", openStates()).Get()
	if err != nil {
		return nil, err
	}
	rows := map[string]*doc.Map{}
	for _, d := range stored.Docs {
		r := d.Data().Without("_lastOrderKey", "_leaseId", "_lastSeq")
		// a lease renewal racing a branch-table clear (disable / repo change) can leave a fragment without gitBranch
		gb, ok := r.StrOK("gitBranch")
		if !ok || !r.Truthy("branch") {
			continue
		}
		rows[gb] = r
	}
	now := doc.NowISO()
	for _, t := range targets {
		if rows[t.GitBranch] == nil {
			rows[t.GitBranch] = core.BranchRow(t.GitBranch, t.Ref, t.Label, now)
		}
	}
	type qs struct {
		pending, failed int
		next            string
	}
	q := map[string]*qs{}
	for _, d := range queue.Docs {
		it := d.Data()
		gb := it.Str("gitBranch")
		x := q[gb]
		if x == nil {
			x = &qs{}
			q[gb] = x
		}
		if it.Val("state") == "failed" {
			x.failed++
		} else {
			x.pending++
		}
		if n := it.Str("nextAttemptAt"); x.next == "" || n < x.next {
			x.next = n
		}
	}
	out := make([]*doc.Map, 0, len(rows))
	for gb, r := range rows {
		x := q[gb]
		if x == nil {
			x = &qs{}
		}
		var next any
		if x.next != "" {
			next = x.next
		}
		r.Set("queue", doc.Obj("pending", x.pending, "failed", x.failed, "nextAttemptAt", next))
		out = append(out, r)
	}
	order := func(k any) int {
		switch k {
		case "template":
			return 0
		case "deployment":
			return 1
		case "agent":
			return 2
		case "uncommitted":
			return 3
		}
		return 9
	}
	entity := func(r *doc.Map) string {
		p := strings.Split(r.Str("gitBranch"), "/")
		if len(p) > 1 {
			return p[1]
		}
		return ""
	}
	// localeCompare on branch names ([a-z0-9/-]): ICU orders these like bytes ("-" < "/" < digits < letters)
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if c := strings.Compare(entity(x), entity(y)); c != 0 {
			return c < 0
		}
		if c := order(x.Map("branch").Val("kind")) - order(y.Map("branch").Val("kind")); c != 0 {
			return c < 0
		}
		return x.Str("gitBranch") < y.Str("gitBranch")
	})
	l := make([]any, len(out))
	for i, r := range out {
		l[i] = r
	}
	return l, nil
}

func syncLike(c *cloud.Ctx, op string) (any, error) {
	if err := mirrorAdmin(c); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"gitBranch"}); err != nil {
		return nil, err
	}
	var only string
	if v, has := b.Get("gitBranch"); has {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, cloud.E.Bad("gitBranch", "V_TYPE", "gitBranch must be a branch name")
		}
		only = s
	}
	m, err := core.LoadMirror()
	if err != nil {
		return nil, err
	}
	if !core.IsActive(m) {
		return nil, cloud.E.Conflict("the GitHub mirror is disabled (enable it with an owner, a repository and a token first)", doc.Obj("reason", "disabled"))
	}
	// Sync now / Resync: unpause, retry failed items at once
	if err := core.Mcol.Config().Set(doc.Obj("pausedUntil", nil, "pauseReason", nil), driver.Merge); err != nil {
		return nil, err
	}
	core.InvalidateMirrorCache()
	failed, err := core.Mcol.Queue().Where("state", "in", []any{"failed", "running"}).Get()
	if err != nil {
		return nil, err
	}
	for _, d := range failed.Docs {
		if only == "" || d.Data().Val("gitBranch") == only {
			if err := d.Ref().Update(doc.Obj("state", "pending", "nextAttemptAt", doc.NowISO())); err != nil {
				return nil, err
			}
		}
	}
	if err := core.RecoverStuck(); err != nil {
		return nil, err
	}
	reason := "sync"
	if op == "rebuild" {
		reason = "resync"
	}
	enqueued, branches, err := core.EnqueueAll(op, reason, only)
	if err != nil {
		return nil, err
	}
	if only != "" && branches == 0 {
		return nil, cloud.E.NotFound("no mirrored branch " + only)
	}
	processed, err := core.DrainDue(60_000)
	if err != nil {
		return nil, err
	}
	var gb any
	if only != "" {
		gb = only
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("op", op, "gitBranch", gb, "enqueued", enqueued, "branches", branches, "processed", processed)}
	return doc.Obj("enqueued", enqueued, "branches", branches, "processed", processed), nil
}

func getGithubMirror(c *cloud.Ctx) (any, error) {
	if err := mirrorAdmin(c); err != nil {
		return nil, err
	}
	m, err := core.LoadMirror()
	if err != nil {
		return nil, err
	}
	return core.MirrorView(m)
}

func patchGithubMirror(c *cloud.Ctx) (any, error) {
	if err := mirrorAdmin(c); err != nil {
		return nil, err
	}
	p, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(p, []string{"owner", "repo", "enabled", "token"}); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	owner, hasOwner := p.Get("owner")
	if hasOwner {
		if s, ok := owner.(string); !ok || !core.GithubOwnerRE.MatchString(s) {
			v.Err("owner", "V_FORMAT", `owner must be a GitHub user or organization name (letters, digits, "-", ≤ 39)`)
		}
	}
	repo, hasRepo := p.Get("repo")
	if hasRepo {
		if s, ok := repo.(string); !ok || !core.GithubRepoOK(s) {
			v.Err("repo", "V_FORMAT", `repo must be a repository name (letters, digits, ".", "_", "-", ≤ 100)`)
		}
	}
	enabled, hasEnabled := p.Get("enabled")
	if hasEnabled {
		if _, ok := enabled.(bool); !ok {
			v.Err("enabled", "V_TYPE", "enabled must be true or false")
		}
	}
	token, hasToken := p.Get("token")
	if hasToken && token != nil {
		if s, ok := token.(string); !ok || !core.GithubTokenRE.MatchString(doc.JSTrim(s)) {
			v.Err("token", "V_FORMAT", "token must be a GitHub token (20–255 printable characters, e.g. github_pat_…)")
		}
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	uid := c.Account.UID
	now := doc.NowISO()
	cur, err := core.LoadMirror()
	if err != nil {
		return nil, err
	}
	next := cur.Shallow()
	if hasOwner {
		next.Set("owner", owner)
	}
	if hasRepo {
		next.Set("repo", repo)
	}
	if hasEnabled {
		next.Set("enabled", enabled)
	}
	tokenStr, tokenIsStr := token.(string)
	if tokenIsStr {
		tokenStr = doc.JSTrim(tokenStr)
	}
	tokenReplaced := tokenIsStr && tokenStr != cur.Val("token")
	tokenRemoved := hasToken && token == nil && cur.Truthy("token")
	if tokenIsStr {
		next.Set("token", tokenStr)
	}
	if hasToken && token == nil {
		next.Set("token", nil)
	}
	if tokenReplaced || tokenRemoved {
		if tokenRemoved {
			next.Set("tokenSetAt", nil).Set("tokenSetBy", nil)
		} else {
			next.Set("tokenSetAt", now).Set("tokenSetBy", uid)
		}
	}
	if next.Truthy("enabled") && !core.IsConfigured(next) {
		return nil, cloud.E.Bad("enabled", "V_REQUIRED", "enabling the mirror needs the owner, the repository and a token")
	}
	repoChanged := (cur.Truthy("owner") || cur.Truthy("repo")) && (strings.ToLower(next.Str("owner")) != strings.ToLower(cur.Str("owner")) || strings.ToLower(next.Str("repo")) != strings.ToLower(cur.Str("repo")))
	if repoChanged {
		next.Set("initializedAt", nil).Set("lastTest", nil)
	}
	if repoChanged || tokenReplaced || tokenRemoved || hasEnabled {
		next.Set("pausedUntil", nil).Set("pauseReason", nil)
	}
	if !cur.Truthy("createdAt") {
		next.Set("createdAt", now).Set("createdBy", uid)
	}
	next.Set("updatedAt", now).Set("updatedBy", uid)
	store := next.Without("_apiBase", "_stubExporter", "_maxFileBytes", "_scope").Set("_repoKey", core.RepoKeyOf(next))
	if err := core.Mcol.Config().Set(store, driver.Merge); err != nil {
		return nil, err
	}
	core.InvalidateMirrorCache()
	if repoChanged {
		// the branch rows / queue / blob cache describe the OLD repository
		if err := clearCollection(core.Mcol.Queue()); err != nil {
			return nil, err
		}
		if err := clearCollection(core.Mcol.Branches()); err != nil {
			return nil, err
		}
	}
	turnedOn := core.IsActive(next) && (!core.IsActive(cur) || repoChanged)
	data := doc.Obj("tokenReplaced", tokenReplaced, "tokenRemoved", tokenRemoved, "repoChanged", repoChanged, "enabled", next.Val("enabled"))
	if turnedOn {
		n, _, err := core.EnqueueAll("catch-up", "enable", "")
		if err != nil {
			return nil, err
		}
		data.Set("catchUpQueued", n)
	}
	c.Audit = &cloud.AuditHint{Data: data}
	m, err := core.LoadMirror()
	if err != nil {
		return nil, err
	}
	return core.MirrorView(m)
}

func testGithubMirror(c *cloud.Ctx) (any, error) {
	if err := mirrorAdmin(c); err != nil {
		return nil, err
	}
	m, err := core.LoadMirror()
	if err != nil {
		return nil, err
	}
	if !core.IsConfigured(m) {
		return nil, cloud.E.Bad("", "V_REQUIRED", "set the owner, the repository and the token first")
	}
	r := testConnection(m)
	if err := core.Mcol.Config().Set(doc.Obj("lastTest", r), driver.Merge); err != nil {
		return nil, err
	}
	core.InvalidateMirrorCache()
	checks := []any{}
	for _, x := range r.Arr("checks") {
		st := "failed"
		k := x.(*doc.Map)
		if k.Truthy("ok") {
			st = "ok"
		}
		checks = append(checks, k.Str("name")+":"+st)
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("ok", r.Val("ok"), "checks", checks)}
	return r, nil
}

func revealGithubToken(c *cloud.Ctx) (any, error) {
	if err := mirrorAdmin(c); err != nil {
		return nil, err
	}
	m, err := core.LoadMirror()
	if err != nil {
		return nil, err
	}
	if !m.Truthy("token") {
		return nil, cloud.E.NotFound("no token is stored")
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("owner", m.Val("owner"), "repo", m.Val("repo"))}
	return doc.Obj("value", m.Val("token"), "hideAfterSec", shared.REVEAL_SECONDS), nil
}

func listGithubMirrorBranches(c *cloud.Ctx) (any, error) {
	if err := mirrorAdmin(c); err != nil {
		return nil, err
	}
	rows, err := branchRows()
	if err != nil {
		return nil, err
	}
	return doc.Obj("items", rows), nil
}

func listGithubMirrorQueue(c *cloud.Ctx) (any, error) {
	if err := mirrorAdmin(c); err != nil {
		return nil, err
	}
	gb, st := c.Query.Get("gitBranch"), c.Query.Get("state")
	n := numberOr(c, "limit", 100)
	if math.IsNaN(n) || n == 0 {
		n = 100
	}
	lim := int(math.Min(200, math.Max(1, n)))
	s, err := core.Mcol.Queue().OrderBy("createdAt", "desc").Limit(1000).Get()
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, d := range s.Docs {
		i := d.Data()
		if (gb == "" || i.Val("gitBranch") == gb) && (st == "" || i.Val("state") == st) && len(items) < lim {
			items = append(items, i)
		}
	}
	return doc.Obj("items", items), nil
}

// sliced is String(x ?? ”).slice(0, n).
func sliced(m *doc.Map, k string, n int) string {
	v := m.Val(k)
	if v == nil || v == doc.Undefined {
		return ""
	}
	return doc.JSSlice(core.JSString(v), n)
}

// agentPushDrift: agent: uncommitted drift (agent ≥ 0.15.0).
func agentPushDrift(c *cloud.Ctx) (any, error) {
	deploymentID, siteSlug := c.Agent.DeploymentID, c.Agent.SiteSlug
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"headCommitId", "scannedAt", "fingerprint", "items", "notes", "liveConfig"}); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	head, hasHead := b.Get("headCommitId")
	if !hasHead || head != nil {
		if s, ok := head.(string); !ok || doc.JSLen(s) > 64 {
			v.Err("headCommitId", "V_TYPE", "headCommitId must be a commit id or null")
		}
	}
	scannedAt, scanOK := b.StrOK("scannedAt")
	if scanOK {
		_, scanOK = doc.ParseISO(scannedAt)
	}
	if !scanOK {
		v.Err("scannedAt", "V_FORMAT", "scannedAt must be an ISO time")
	}
	fingerprint, fpOK := b.StrOK("fingerprint")
	if !fpOK || fingerprint == "" || doc.JSLen(fingerprint) > 200 {
		v.Err("fingerprint", "V_REQUIRED", "fingerprint is required (≤ 200)")
	}
	rawItems, itemsOK := b.Val("items").([]any)
	if !itemsOK || len(rawItems) > 2000 {
		v.Err("items", "V_TYPE", "items must be a list (≤ 2000)")
	}
	rawNotes, notesOK := b.Val("notes").([]any)
	if !notesOK || len(rawNotes) > 500 {
		v.Err("notes", "V_TYPE", "notes must be a list (≤ 500)")
	}
	liveV, hasLive := b.Get("liveConfig")
	liveCfg, liveIsMap := liveV.(*doc.Map)
	if !hasLive || liveV != nil {
		if !liveIsMap {
			v.Err("liveConfig", "V_TYPE", "liveConfig must be a config view or null")
		} else if _, ok := liveCfg.Val("sites").([]any); !ok {
			v.Err("liveConfig", "V_TYPE", "liveConfig must be a config view or null")
		}
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	items := make([]any, len(rawItems))
	for i, x := range rawItems {
		m, ok := x.(*doc.Map)
		if !ok && x == nil {
			return nil, errors.New("items: null entry")
		}
		origin := "docker"
		switch s := core.JSString(m.Val("origin")); s {
		case "docker", "file", "secret-file", "volume-content":
			origin = s
		}
		items[i] = doc.Obj("key", sliced(m, "key", 500), "origin", origin, "detail", sliced(m, "detail", 1000))
	}
	notes := make([]any, len(rawNotes))
	for i, x := range rawNotes {
		m, ok := x.(*doc.Map)
		if !ok && x == nil {
			return nil, errors.New("notes: null entry")
		}
		notes[i] = doc.Obj("fingerprint", sliced(m, "fingerprint", 200), "label", sliced(m, "label", 300), "detail", sliced(m, "detail", 1000))
	}
	cleared := len(items) == 0 && len(notes) == 0
	// the live config is a VIEW: secret values must be masked: refuse anything that looks like a value
	var live any
	if !cleared && liveCfg != nil {
		for _, s := range liveCfg.Arr("secrets") {
			m, _ := s.(*doc.Map)
			if x := m.Val("value"); x != nil && x != doc.Undefined && x != "•••" {
				return nil, cloud.E.Bad("liveConfig.secrets", "V_SECRET", `liveConfig must not hold secret values (masked "•••" or null)`)
			}
		}
		l := doc.MarshalString(liveCfg)
		if doc.JSLen(l) > 900_000 {
			return nil, cloud.E.TooLarge("liveConfig is larger than 900 KB")
		}
		live = l
	}
	ref := core.Bcol.AgentBranches(deploymentID).Doc(siteSlug)
	s, err := ref.Get()
	if err != nil {
		return nil, err
	}
	prev := s.Data().Map("drift")
	drift := doc.Obj("headCommitId", head, "scannedAt", scannedAt, "fingerprint", fingerprint, "items", items, "notes", notes, "receivedAt", doc.NowISO())
	if prev != nil && prev.Val("fingerprint") == fingerprint {
		a, err := shared.Canonicalize(prev.Arr("items"))
		if err != nil {
			return nil, err
		}
		b, err := shared.Canonicalize(items)
		if err != nil {
			return nil, err
		}
		if a == b {
			return doc.Obj("stored", false, "mirrored", false), nil
		}
	}
	if err := ref.Set(doc.Obj("siteSlug", siteSlug, "drift", drift, "_driftLive", live), driver.Merge); err != nil {
		return nil, err
	}
	mirrored := false
	core.MirrorHook("drift", func() error {
		t, err := core.TargetOf(core.RefOf("uncommitted", "", deploymentID, siteSlug))
		if err != nil {
			return err
		}
		wasCleared := prev == nil || (len(prev.Arr("items")) == 0 && len(prev.Arr("notes")) == 0)
		if cleared && wasCleared {
			return nil // nothing on GitHub to delete
		}
		op, opmID := "drift", cloud.Sha256Hex([]byte(fingerprint))[:40]
		if cleared {
			op, opmID = "drift-clear", "clear-"+cloud.Sha256Hex([]byte(scannedAt + fingerprint))[:16]
		}
		mirrored, err = core.Enqueue(t, core.EnqueueOpts{Op: op, OpmID: opmID, CreatedAt: scannedAt, Reason: "drift"})
		return err
	})
	return doc.Obj("stored", true, "mirrored", mirrored), nil
}
