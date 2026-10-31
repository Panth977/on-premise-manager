package apitest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

const wgOwner = "opm-test"

// wgBool is a condition for wgUntil.
func wgBool(b bool) J { return J{b} }

// wgUntil is until(): polls every 400 ms until f gives a truthy value (90 s). A failed API call inside f counts as "not yet".
func wgUntil(t testing.TB, what string, f func() J) J {
	t.Helper()
	t0 := time.Now()
	for {
		if v := f(); v.Truthy() {
			return v
		}
		if time.Since(t0) > 90*time.Second {
			t.Fatalf("timeout waiting for %s", what)
		}
		Sleep(400)
	}
}

func wgMirrorBranch(t testing.TB, kind, slug, site string) string {
	t.Helper()
	b, err := core.MirrorBranchName(kind, slug, site)
	NoErr(t, err)
	return b
}

func wgMs(t testing.TB, iso J) int64 {
	t.Helper()
	ms, ok := doc.ParseISO(iso.Str())
	True(t, ok, "bad date %s", iso)
	return ms
}

func wgOrderKey(createdAt string, seq int, commitID string) string {
	return fmt.Sprintf("%s|%010d|%s", createdAt, seq, commitID)
}

func wgQueue() *driver.CollectionRef { return DB.Collection("githubMirror/config/queue") }

func wgBranchDoc(gitBranch string) *driver.DocRef {
	return DB.Collection("githubMirror/config/branches").Doc(strings.ReplaceAll(gitBranch, "/", "~"))
}

func wgSnapJ(t testing.TB, r *driver.DocRef) J {
	t.Helper()
	return Doc(t, r.Path)
}

func wgCount(t testing.TB, q *driver.Query) int {
	t.Helper()
	s, err := q.Get()
	NoErr(t, err)
	return s.Size()
}

// wgMirror is the state the tests of the file share.
type wgMirror struct {
	fake *fakeGithub
	repo string
	good string
	cfg  *driver.DocRef

	admin                       *Person
	depID, tplID                string
	tplBranch, depBranch, depSl string
}

func (m *wgMirror) gitRepo() *fakeRepo { return m.fake.repo(wgOwner, m.repo) }

func (m *wgMirror) setCfg(t testing.TB, data A) {
	t.Helper()
	NoErr(t, m.cfg.Set(data, driver.Merge))
}

func (m *wgMirror) ready(t testing.TB) {
	t.Helper()
	if m.admin == nil {
		t.Fatal("the enable test did not run")
	}
}

func (m *wgMirror) rows(admin *Person) J {
	r, e := admin.Real.Try("listGithubMirrorBranches", nil)
	if e != nil {
		return J{Undefined}
	}
	return r.At("items")
}

func (m *wgMirror) row(admin *Person, b string) J { return m.rows(admin).Find("gitBranch", b) }

// synced: n < 0 = any number of commits.
func (m *wgMirror) synced(t testing.TB, admin *Person, b string, n int) J {
	t.Helper()
	what := b + " synced"
	if n > 0 {
		what = fmt.Sprintf("%s with %d commits", what, n)
	}
	return wgUntil(t, what, func() J {
		r := m.row(admin, b)
		if !r.Truthy() || r.At("status").Str() != "synced" || r.At("queue.pending").Truthy() || r.At("queue.failed").Truthy() {
			return J{nil}
		}
		if n >= 0 && len(m.fake.log(m.gitRepo(), b)) != n {
			return J{nil}
		}
		return r
	})
}

func (m *wgMirror) editDeployment(t testing.TB, admin *Person, id string, edit func(J), message string) J {
	t.Helper()
	d := admin.Real.Call(t, "getDeployment", A{"deploymentId": id})
	input := DepInput(t, admin.Real, id)
	edit(input)
	return admin.Real.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": message, "baseCommitId": d.At("branch.head.commitId"), "config": input}})
}

// wgNextTheme: the next allowed theme value (enum green | yellow | blue | red): every call changes the config.
func wgNextTheme(c J) {
	themes := []string{"green", "yellow", "blue", "red"}
	v := c.At("variables").Find("variableSlug", "theme")
	v.Map()["value"] = themes[(slices.Index(themes, v.At("value").Str())+1)%len(themes)]
}

func wgTrailers(c fakeCommit) map[string]string { return core.ParseMirrorTrailers(c.message) }

func wgSome(j J, f func(J) bool) bool { return j.FindF(f).Exists() }

// Port of backend/test/32-github-mirror.test.ts. The tree always comes from the real Go exporter (_stubExporter false); the
// TS file fell back to the stub when the wasm was not built.
func TestGithubMirror(t *testing.T) {
	fake := newFakeGithub()
	m := &wgMirror{fake: fake, repo: Uniq("mirror"), cfg: DB.Collection("githubMirror").Doc("config")}
	GOOD := "github_pat_11GOOD" + strings.Repeat("x", 40) + "GOOD"
	RO := "github_pat_11READ" + strings.Repeat("y", 40) + "READ"
	NOUSER := "github_pat_11NOUS" + strings.Repeat("z", 40) + "NOUS"
	m.good = GOOD
	repoName := m.repo
	const realExporter = true
	t.Cleanup(func() {
		m.setCfg(t, A{"enabled": false})
		fake.stop()
	})
	fake.start()

	t.Run("settings: validation, audit of every change, token masked everywhere, reveal 60 s (audited, no-store, rate-limited), test connection", func(t *testing.T) {
		fake.addRepo(wgOwner, repoName)
		fake.addToken(GOOD, fakeToken{login: "mirror-bot", write: true})
		fake.addToken(RO, fakeToken{login: "reader", write: false})
		fake.addToken(NOUSER, fakeToken{login: "fg", repos: []string{wgOwner + "/" + repoName}, write: true, noUser: true})
		// test hooks (OPM_TEST_HOOKS only): the fake GitHub, the real exporter, only this file's entities
		m.setCfg(t, A{"_apiBase": fake.base, "_stubExporter": !realExporter, "_scope": L{}, "enabled": false})
		admin := NewPerson(t, "admin")
		viewer := NewPerson(t, L{A{"resource": "accounts", "actions": L{"read"}, "scope": A{"kind": "all"}}})
		api := admin.Real
		viewer.Real.Rejects(t, "getGithubMirror", nil, "forbidden", 403)
		api.Rejects(t, "patchGithubMirror", A{"body": A{"owner": "-bad"}}, "invalid", 422)
		api.Rejects(t, "patchGithubMirror", A{"body": A{"repo": "a/b"}}, "invalid", 422)
		api.Rejects(t, "patchGithubMirror", A{"body": A{"token": "short"}}, "invalid", 422)
		api.Rejects(t, "patchGithubMirror", A{"body": A{"owner": wgOwner, "repo": repoName, "enabled": true}}, "invalid", 422) // no token yet
		api.Rejects(t, "patchGithubMirror", A{"body": A{"nope": 1}}, "invalid", 422)
		v1 := api.Call(t, "patchGithubMirror", A{"body": A{"owner": wgOwner, "repo": repoName, "token": GOOD}})
		Eq(t, v1.At("configured"), true)
		Eq(t, v1.At("enabled"), false)
		Eq(t, v1.At("token"), "github_pat_•••GOOD")
		Eq(t, v1.At("repoUrl"), "https://github.com/"+wgOwner+"/"+repoName)
		NotContains(t, v1.String(), GOOD)
		NotContains(t, api.Call(t, "getGithubMirror", nil).String(), GOOD)
		// audit: every change, the token never in clear
		au := api.Call(t, "listAuditLog", A{"query": A{"action": "github."}})
		upd := au.At("items").FindF(func(x J) bool {
			return x.At("action").Str() == "github.updated" && x.At("outcome").Str() == "ok"
		})
		True(t, upd.Truthy(), "github.updated")
		Eq(t, upd.At("data.tokenReplaced"), true)
		True(t, wgSome(au.At("items"), func(x J) bool {
			return x.At("action").Str() == "github.updated" && x.At("outcome").Str() == "error"
		}), "failed changes are audited too")
		NotContains(t, au.At("items").String(), GOOD, "no token in the audit log")
		// reveal: value, no-store, audited
		reveal := Opt{Headers: map[string]string{"Authorization": admin.Bearer}}
		r := Raw(t, "POST", "/v1/github-mirror/reveal-token", reveal)
		Eq(t, r.Status, 200)
		Eq(t, r.Header("cache-control"), "no-store")
		Eq(t, r.J.At("value"), GOOD)
		Eq(t, r.J.At("hideAfterSec"), 60)
		au2 := api.Call(t, "listAuditLog", A{"query": A{"action": "github.token-revealed"}})
		True(t, au2.At("items").Find("actor", admin.UID).Exists())
		NotContains(t, au2.At("items").String(), GOOD)
		viewer.Real.Rejects(t, "revealGithubToken", nil, "forbidden", 403)
		// test connection: ok (empty repo), read-only token, token without /user, missing repo, bad token
		t1 := api.Call(t, "testGithubMirror", nil)
		Eq(t, t1.At("ok"), true, t1.At("checks").String())
		Eq(t, t1.At("repo.empty"), true)
		Eq(t, t1.At("login"), "mirror-bot")
		True(t, api.Call(t, "getGithubMirror", nil).At("lastTest.ok").Truthy())
		api.Call(t, "patchGithubMirror", A{"body": A{"token": RO}})
		t2 := api.Call(t, "testGithubMirror", nil)
		Eq(t, t2.At("ok"), false)
		Eq(t, t2.At("checks").Find("name", "contents-write").At("ok"), false)
		api.Call(t, "patchGithubMirror", A{"body": A{"token": NOUSER}})
		t3 := api.Call(t, "testGithubMirror", nil)
		Eq(t, t3.At("ok"), true, t3.At("checks").String())
		Eq(t, t3.At("login"), nil)
		api.Call(t, "patchGithubMirror", A{"body": A{"token": GOOD, "repo": "does-not-exist"}})
		t4 := api.Call(t, "testGithubMirror", nil)
		Eq(t, t4.At("checks").Find("name", "repo").At("ok"), false)
		api.Call(t, "patchGithubMirror", A{"body": A{"token": "github_pat_11BAD" + strings.Repeat("q", 40)}})
		t5 := api.Call(t, "testGithubMirror", nil)
		Eq(t, t5.At("checks").Find("name", "token").At("ok"), false)
		tested := api.Call(t, "listAuditLog", A{"query": A{"action": "github.tested"}})
		True(t, tested.At("items").Len() >= 5)
		api.Call(t, "patchGithubMirror", A{"body": A{"token": GOOD, "repo": repoName}})
		// remove the token → not configured; never visible
		v2 := api.Call(t, "patchGithubMirror", A{"body": A{"token": nil}})
		Eq(t, v2.At("tokenSet"), false)
		api.Rejects(t, "revealGithubToken", nil, "not-found", 404)
		rm := api.Call(t, "listAuditLog", A{"query": A{"action": "github.updated"}}).At("items.0")
		Eq(t, rm.At("data.tokenRemoved"), true)
		api.Call(t, "patchGithubMirror", A{"body": A{"token": GOOD}})
		// sync while disabled → 409
		api.Rejects(t, "syncGithubMirror", A{"body": A{}}, "conflict", 409)
		// rate limit (30 / min per uid)
		limited := false
		for i := 0; i < 31 && !limited; i++ {
			if Raw(t, "POST", "/v1/github-mirror/reveal-token", reveal).Status == 429 {
				limited = true
			}
		}
		True(t, limited, "reveal is rate-limited")
	})

	t.Run("enable: empty repo initialized, every OPM commit = one git commit (message + trailers, author, date), deterministic tree = git write-tree", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		h := HelloDeployment(t, admin, "Mirror "+Uniq("d"))
		id, templateID := h.ID(), h.TemplateID
		m.setCfg(t, A{"_scope": L{templateID, id}})
		api := admin.Real
		tp := api.Call(t, "getTemplate", A{"templateId": templateID})
		Match(t, tp.At("slug"), `^[a-z][a-z0-9-]{1,39}$`)
		d := api.Call(t, "getDeployment", A{"deploymentId": id})
		Match(t, d.At("slug"), `^mirror-`)
		tplBranch := wgMirrorBranch(t, "template", tp.At("slug").Str(), "")
		depBranch := wgMirrorBranch(t, "deployment", d.At("slug").Str(), "")
		v := api.Call(t, "patchGithubMirror", A{"body": A{"enabled": true}})
		Eq(t, v.At("enabled"), true)
		repo := m.gitRepo()
		tplCommits := api.Call(t, "listTemplateCommits", A{"templateId": templateID}).At("items").Arr()
		sort.SliceStable(tplCommits, func(i, j int) bool { return tplCommits[i].At("seq").Num() < tplCommits[j].At("seq").Num() })
		lastCommit := tplCommits[len(tplCommits)-1]
		m.synced(t, admin, tplBranch, len(tplCommits))
		m.synced(t, admin, depBranch, 1)
		// main initialized once (empty repository)
		True(t, fake.ref(repo, "heads/main") != "", "main README")
		Contains(t, fake.checkout(repo, "main")["README.md"].bytes, "OPM mirror")
		True(t, api.Call(t, "getGithubMirror", nil).At("initializedAt").Truthy())
		// one git commit per OPM commit, oldest first, same message + trailers + author + date
		log := fake.log(repo, tplBranch)
		slices.Reverse(log)
		gotIDs, wantIDs := L{}, L{}
		for i := range log {
			gotIDs = append(gotIDs, wgTrailers(log[i])["OPM-Commit"])
		}
		for _, c := range tplCommits {
			wantIDs = append(wantIDs, c.At("commitId").V)
		}
		Eq(t, gotIDs, wantIDs)
		for i, g := range log {
			o := tplCommits[i]
			True(t, strings.HasPrefix(g.message, doc.JSTrim(o.At("message").Str())), "same message")
			tr := wgTrailers(g)
			Eq(t, tr["OPM-Branch"], "template:"+templateID)
			Eq(t, tr["OPM-Seq"], strconv.Itoa(o.At("seq").Int()))
			Contains(t, g.author, "<"+admin.Email+">", g.author)
			True(t, strings.HasSuffix(g.author, fmt.Sprintf("%d +0000", wgMs(t, o.At("createdAt"))/1000)), "author date = createdAt")
			Eq(t, g.author, g.committer, "committer = author (deterministic)")
			wantParents := 1
			if i == 0 {
				wantParents = 0
			}
			Eq(t, len(g.parents), wantParents)
		}
		// tree content
		files := fake.checkout(repo, tplBranch)
		for _, p := range []string{"README.md", ".gitignore"} {
			_, ok := files[p]
			True(t, ok, p)
		}
		Contains(t, files["README.md"].bytes, lastCommit.At("commitId").Str(), "the README names the commit")
		hasPrefix := func(pre string) bool {
			for p := range files {
				if strings.HasPrefix(p, pre) {
					return true
				}
			}
			return false
		}
		False(t, hasPrefix("opm/"), "no OPM files in a Docker-only tree (§21 #115)")
		True(t, hasPrefix("sites/"), "sites exported")
		// deployment branch: secret values never pushed
		depFiles := fake.checkout(repo, depBranch)
		secret := api.Call(t, "revealSecret", A{"deploymentId": id, "secretSlug": "api-token"}).At("value").Str()
		for p, f := range depFiles {
			False(t, bytes.Contains(f.bytes, []byte(secret)), "secret value in "+p)
		}
		// the pushed tree = `git write-tree` of the same files (real git object ids)
		dir := t.TempDir()
		for p, f := range files {
			full := filepath.Join(dir, p)
			NoErr(t, os.MkdirAll(filepath.Dir(full), 0o755))
			NoErr(t, os.WriteFile(full, f.bytes, 0o644))
			if f.mode == "100755" {
				NoErr(t, os.Chmod(full, 0o755))
			}
		}
		git := func(a ...string) string {
			c := exec.Command("git", a...)
			c.Dir = dir
			c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			out, err := c.CombinedOutput()
			NoErr(t, err)
			return strings.TrimSpace(string(out))
		}
		git("init", "-q")
		git("add", "-A", "-f")
		Eq(t, git("write-tree"), log[len(log)-1].tree, "tree sha = git write-tree")
		// the status row
		r := m.row(admin, tplBranch)
		Eq(t, r.At("gitSha"), log[len(log)-1].sha)
		Eq(t, r.At("gitUrl"), "https://github.com/"+wgOwner+"/"+repoName+"/commit/"+r.At("gitSha").Str())
		Eq(t, r.At("last.opmId"), lastCommit.At("commitId"))
		NotContains(t, m.rows(admin).String(), GOOD)
		m.admin, m.depID, m.tplID, m.tplBranch, m.depBranch, m.depSl = admin, id, templateID, tplBranch, depBranch, d.At("slug").Str()
	})

	t.Run("order + idempotency: commits pushed in seq order; re-enqueue / crash between ref update and state write → no duplicate", func(t *testing.T) {
		m.ready(t)
		admin, depID, depBranch := m.admin, m.depID, m.depBranch
		api := admin.Real
		repo := m.gitRepo()
		// pause pushing, commit twice, unpause: both arrive, in seq order
		m.setCfg(t, A{"pausedUntil": ISO(time.Now().Add(time.Hour)), "pauseReason": "test"})
		m.editDeployment(t, admin, depID, wgNextTheme, "theme red")
		c3 := m.editDeployment(t, admin, depID, wgNextTheme, "theme blue")
		wgUntil(t, "2 queued items", func() J {
			return wgBool(wgCount(t, wgQueue().Where("gitBranch", "==", depBranch).Where("state", "==", "pending")) >= 2)
		})
		s1 := api.Call(t, "syncGithubMirror", A{"body": A{}})
		True(t, s1.At("branches").Num() >= 2)
		m.synced(t, admin, depBranch, 3)
		seqs := L{}
		dlog := fake.log(repo, depBranch)
		for i := len(dlog) - 1; i >= 0; i-- {
			seqs = append(seqs, wgTrailers(dlog[i])["OPM-Seq"])
		}
		Eq(t, seqs, L{"1", "2", "3"})
		Eq(t, strings.Split(dlog[0].message, "\n")[0], "theme blue")
		// re-enqueue the same commit (a duplicate trigger) → skipped, no new git commit
		headSha := fake.ref(repo, "heads/"+depBranch)
		c3ID, c3At, c3Seq := c3.At("commitId").Str(), c3.At("createdAt").Str(), c3.At("seq").Int()
		ref := wgQueue().Doc(strings.ReplaceAll(depBranch, "/", "~") + "__commit__again-" + c3ID)
		NoErr(t, ref.Set(A{"itemId": ref.ID(), "gitBranch": depBranch, "branch": A{"kind": "deployment", "templateId": nil, "deploymentId": depID, "siteSlug": nil}, "op": "commit", "opmId": c3ID, "seq": c3Seq, "orderKey": wgOrderKey(c3At, c3Seq, c3ID), "state": "pending", "attempts": 0, "nextAttemptAt": Now(), "createdAt": Now(), "startedAt": nil, "doneAt": nil, "gitSha": nil, "lastError": nil, "reason": "test"}))
		wgUntil(t, "duplicate item processed", func() J {
			s := wgSnapJ(t, ref).At("state").Str()
			return wgBool(s == "skipped" || s == "done")
		})
		Eq(t, wgSnapJ(t, ref).At("state"), "skipped")
		Eq(t, fake.ref(repo, "heads/"+depBranch), headSha)
		// crash: the ref moved but the state write was lost → the trailer of the remote head is adopted
		bdoc := wgBranchDoc(depBranch)
		prev := fake.log(repo, depBranch)[1]
		prevKey := Doc(t, "deployments/"+depID+"/commits/"+wgTrailers(prev)["OPM-Commit"])
		NoErr(t, bdoc.Set(A{"gitSha": prev.sha, "_lastOrderKey": wgOrderKey(prevKey.At("createdAt").Str(), prevKey.At("seq").Int(), prevKey.At("commitId").Str())}, driver.Merge))
		api.Call(t, "syncGithubMirror", A{"body": A{"gitBranch": depBranch}})
		m.synced(t, admin, depBranch, 3)
		Eq(t, fake.ref(repo, "heads/"+depBranch), headSha, "no duplicate commit after the recovered crash")
		Eq(t, wgSnapJ(t, bdoc).At("gitSha"), headSha)
	})

	t.Run("retry / backoff on 5xx, pause on 401 and on rate limits, Sync now resumes", func(t *testing.T) {
		m.ready(t)
		admin, depID, depBranch := m.admin, m.depID, m.depBranch
		api := admin.Real
		repo := m.gitRepo()
		fake.fail(`/git/commits$`, 502, 1, fakeFaultOpts{method: "POST"})
		c4 := m.editDeployment(t, admin, depID, wgNextTheme, "theme green")
		failed := wgUntil(t, "failed item", func() J {
			q, err := wgQueue().Where("gitBranch", "==", depBranch).Where("state", "==", "failed").Get()
			NoErr(t, err)
			if q.Empty() {
				return J{nil}
			}
			return JOf(q.Docs[0].Data())
		})
		Eq(t, failed.At("attempts"), 1)
		wait := wgMs(t, failed.At("nextAttemptAt")) - wgMs(t, failed.At("lastError.at"))
		True(t, wait >= 25_000 && wait <= 35_000, "backoff ≈ 30 s (%d)", wait)
		Eq(t, failed.At("lastError.status"), 502)
		r1 := m.row(admin, depBranch)
		Eq(t, r1.At("status"), "error")
		Eq(t, r1.At("queue.failed"), 1)
		pushFailed := api.Call(t, "listAuditLog", A{"query": A{"action": "github.push-failed"}})
		True(t, pushFailed.At("items").Find("data.gitBranch", depBranch).Exists())
		api.Call(t, "syncGithubMirror", A{"body": A{}})
		m.synced(t, admin, depBranch, 4)
		Eq(t, wgTrailers(fake.log(repo, depBranch)[0])["OPM-Commit"], c4.At("commitId"))
		// 401 → the whole mirror pauses
		fake.fail(`/git/trees$`, 401, 1, fakeFaultOpts{method: "POST"})
		m.editDeployment(t, admin, depID, wgNextTheme, "theme pink")
		paused := wgUntil(t, "paused", func() J {
			g, e := api.Try("getGithubMirror", nil)
			if e != nil || !g.At("pausedUntil").Truthy() {
				return J{nil}
			}
			return g
		})
		Match(t, paused.At("pauseReason"), `refused the token`)
		api.Call(t, "syncGithubMirror", A{"body": A{}})
		m.synced(t, admin, depBranch, 5)
		Eq(t, api.Call(t, "getGithubMirror", nil).At("pausedUntil"), nil)
		// primary rate limit (403 + remaining 0) → paused until the reset
		fake.fail(`/git/blobs$`, 403, 1, fakeFaultOpts{method: "POST", headers: map[string]string{"x-ratelimit-remaining": "0", "x-ratelimit-reset": strconv.FormatInt(time.Now().Unix()+1800, 10)}, body: `{"message":"API rate limit exceeded"}`})
		fake.fail(`/git/blobs/[0-9a-f]+$`, 404, 50, fakeFaultOpts{method: "GET"}) // force blob uploads (cache misses)
		for _, d := range ListDocs(t, "githubMirror/config/blobs") {
			NoErr(t, DB.Collection("githubMirror/config/blobs").Doc(d.ID).Delete())
		}
		m.editDeployment(t, admin, depID, wgNextTheme, "theme cyan")
		rateRe := regexp.MustCompile(`rate limit`)
		rl := wgUntil(t, "rate-limit pause", func() J {
			g, e := api.Try("getGithubMirror", nil)
			if e != nil || !g.At("pausedUntil").Truthy() || !rateRe.MatchString(g.At("pauseReason").Str()) {
				return J{nil}
			}
			return g
		})
		True(t, wgMs(t, rl.At("pausedUntil")) > time.Now().UnixMilli()+1500_000)
		fake.clearFaults()
		api.Call(t, "syncGithubMirror", A{"body": A{}})
		m.synced(t, admin, depBranch, 6)
	})

	t.Run("Resync everything: rebuilt from OPM history with the same shas", func(t *testing.T) {
		m.ready(t)
		api := m.admin.Real
		repo := m.gitRepo()
		beforeT, beforeD := fake.ref(repo, "heads/"+m.tplBranch), fake.ref(repo, "heads/"+m.depBranch)
		r := api.Call(t, "resyncGithubMirror", A{"body": A{}})
		True(t, r.At("enqueued").Num() >= 2)
		wgUntil(t, "rebuild items done", func() J {
			return wgBool(wgCount(t, wgQueue().Where("op", "==", "rebuild").Where("state", "in", L{"pending", "failed", "running"})) == 0)
		})
		Eq(t, fake.ref(repo, "heads/"+m.tplBranch), beforeT, "template branch: same sha")
		Eq(t, fake.ref(repo, "heads/"+m.depBranch), beforeD, "deployment branch: same sha")
		True(t, api.Call(t, "listAuditLog", A{"query": A{"action": "github.resynced"}}).At("items").Len() >= 1)
	})

	t.Run("files above the size limit are skipped with .skipped.txt + a note", func(t *testing.T) {
		m.ready(t)
		admin, depID, depBranch := m.admin, m.depID, m.depBranch
		repo := m.gitRepo()
		m.setCfg(t, A{"_maxFileBytes": 8}) // the hello banner File is larger
		m.editDeployment(t, admin, depID, wgNextTheme, "theme orange")
		m.synced(t, admin, depBranch, 7)
		files := fake.checkout(repo, depBranch)
		skippedFile := ""
		for p := range files {
			if strings.HasSuffix(p, ".skipped.txt") {
				skippedFile = p
			}
		}
		True(t, skippedFile != "", "a .skipped.txt")
		Match(t, files[skippedFile].bytes, `bytes`)
		r := m.row(admin, depBranch)
		True(t, r.At("notes").Find("code", "SKIPPED_LARGE").Exists())
		m.setCfg(t, A{"_maxFileBytes": nil})
	})

	t.Run("agent branch (commits + persist snapshots), drift → uncommitted branch reset + delete, heartbeat catch-up, all four branch kinds", func(t *testing.T) {
		m.ready(t)
		admin, depID, depSlug := m.admin, m.depID, m.depSl
		repo := m.gitRepo()
		app := EnrollSite(t, admin, depID, "app")
		UpdateDoc(t, "deployments/"+depID+"/agents/app", A{"agentVersion": "0.15.0-test"})
		head := admin.Real.Call(t, "getDeployment", A{"deploymentId": depID}).At("branch.head")
		got := app.API.Call(t, "getAgentCommit", A{"commitId": head.At("commitId")})
		own := A{"kind": "agent", "deploymentId": depID, "siteSlug": "app"}
		author := A{"kind": "person", "email": "ops@site.test", "uid": nil, "name": "Site Ops", "siteSlug": "app"}
		a1 := wgMakeCommit(t, A{"commitId": ulid.New(), "branch": own, "parent": nil, "kind": "merge", "message": fmt.Sprintf("merge cloud seq %d", head.At("seq").Int()), "author": author, "createdAt": Now(), "config": got.At("commit.config"), "catalog": got.At("commit.catalog"), "source": A{"branch": A{"kind": "deployment", "deploymentId": depID}, "commitId": head.At("commitId"), "seq": head.At("seq"), "label": "x", "picks": "all", "skipped": 0}, "scope": "app"})
		app.API.Call(t, "pushAgentBranch", A{"body": A{"commits": L{a1}}})
		agentBranch := wgMirrorBranch(t, "agent", depSlug, "app")
		m.synced(t, admin, agentBranch, 1)
		g1 := fake.log(repo, agentBranch)[0]
		Eq(t, wgTrailers(g1)["OPM-Commit"], a1.At("commitId"))
		True(t, strings.HasPrefix(g1.author, "Site Ops <ops@site.test>"))
		for p := range fake.checkout(repo, agentBranch) {
			True(t, !strings.HasPrefix(p, "sites/") || strings.HasPrefix(p, "sites/app/"), "agent branch: only its own site")
		}
		// a persist-to-cloud snapshot → a commit on the agent branch under sites/app/volumes/<vol>/
		appCfg := got.At("commit.config.sites").Find("siteSlug", "app").At("config")
		vol := "data"
		if v := appCfg.At("volumes.0.volumeSlug"); !v.IsNil() {
			vol = v.Str()
		}
		body := []byte("{\"flows\":[1,2,3]}\n")
		fhex := Sha256Hex(body)
		Bucket.Save(t, "files/"+fhex, body)
		tree := A{"entries": L{A{"path": "flows.json", "sha256": "sha256:" + fhex, "size": len(body), "mode": "0644", "render": "none"}}, "dirs": L{}}
		tjson, err := canonical.Canonicalize(Plain(tree))
		NoErr(t, err)
		thex := Sha256Hex(tjson)
		Bucket.Save(t, "files/"+thex, tjson)
		versionID := ulid.New()
		key := "app:" + vol
		vc := DB.Collection("deployments").Doc(depID).Collection("volumeContent").Doc(key)
		NoErr(t, vc.Set(A{"siteSlug": "app", "volumeSlug": vol}, driver.Merge))
		NoErr(t, vc.Collection("versions").Doc(versionID).Set(A{"versionId": versionID, "seq": 1, "origin": "snapshot", "treeHash": "sha256:" + thex, "createdAt": Now(), "createdBy": "agent:app", "deploymentId": depID, "siteSlug": "app", "volumeSlug": vol, "trigger": "manual-agent", "fileCount": 1, "dirCount": 0, "totalBytes": len(body), "baseVersionId": nil, "restoredFromVersionId": nil, "note": "", "excludedCount": 0, "skippedCount": 0, "skipped": L{}, "agentSnapshotId": ulid.New(), "requestId": nil, "pushId": nil, "changes": A{"added": 1, "changed": 0, "removed": 0}}))
		m.synced(t, admin, agentBranch, 2)
		g2 := fake.log(repo, agentBranch)[0]
		Eq(t, wgTrailers(g2)["OPM-Volume-Version"], versionID)
		Match(t, g2.message, `^Volume snapshot app/`)
		if !realExporter || appCfg.At("volumes").Find("volumeSlug", vol).Exists() {
			f, ok := fake.checkout(repo, agentBranch)["sites/app/volumes/"+vol+"/flows.json"]
			True(t, ok, "snapshot file in the tree")
			Eq(t, string(f.bytes), string(body))
		}
		// drift → uncommitted/<site>: one commit on top of the agent branch head; a new fingerprint resets it; clear deletes it
		unc := wgMirrorBranch(t, "uncommitted", depSlug, "app")
		live := got.At("commit.config").Clone()
		wgPush(live.At("sites").Find("siteSlug", "app").At("config.containers.0"), "env", A{"name": "HOTFIX", "value": "on", "secretRef": nil})
		liveView := live.Clone()
		viewSecrets := L{}
		for _, s := range liveView.At("secrets").Arr() {
			var value any
			if s.At("value").Truthy() {
				value = shared.MASK
			}
			s.Map()["isSet"], s.Map()["value"] = s.At("value").Truthy(), value
			viewSecrets = append(viewSecrets, s.V)
		}
		liveView.Map()["secrets"] = viewSecrets
		a1ID := a1.At("commitId")
		hotfix := L{A{"key": "sites/app/containers/x/env/HOTFIX", "origin": "docker", "detail": "added by hand"}}
		// secret values are refused
		app.API.Rejects(t, "agentPushDrift", A{"body": A{"headCommitId": a1ID, "scannedAt": Now(), "fingerprint": "f0", "items": L{A{"key": "k", "origin": "docker", "detail": ""}}, "notes": L{}, "liveConfig": live}}, "invalid", 422)
		p1 := app.API.Call(t, "agentPushDrift", A{"body": A{"headCommitId": a1ID, "scannedAt": Now(), "fingerprint": "f1", "items": hotfix, "notes": L{}, "liveConfig": liveView}})
		Eq(t, p1, A{"stored": true, "mirrored": true})
		Eq(t, app.API.Call(t, "agentPushDrift", A{"body": A{"headCommitId": a1ID, "scannedAt": Now(), "fingerprint": "f1", "items": hotfix, "notes": L{}, "liveConfig": liveView}}), A{"stored": false, "mirrored": false}, "same fingerprint = no change")
		wgUntil(t, "uncommitted branch", func() J { return J{fake.ref(repo, "heads/"+unc)} })
		u1 := fake.log(repo, unc)
		Eq(t, u1[0].parents[0], fake.ref(repo, "heads/"+agentBranch), "drift commit on top of the agent branch head")
		Eq(t, wgTrailers(u1[0])["OPM-Drift"], "f1")
		Match(t, u1[0].message, `^Uncommitted changes on app \(1 items\)`)
		app.API.Call(t, "agentPushDrift", A{"body": A{"headCommitId": a1ID, "scannedAt": ISO(time.Now().Add(time.Second)), "fingerprint": "f2", "items": L{A{"key": "a", "origin": "docker", "detail": ""}, A{"key": "b", "origin": "docker", "detail": ""}}, "notes": L{}, "liveConfig": liveView}})
		wgUntil(t, "uncommitted reset", func() J {
			h := fake.ref(repo, "heads/"+unc)
			return wgBool(h != "" && wgTrailers(fake.commit(repo, h))["OPM-Drift"] == "f2")
		})
		Eq(t, len(fake.log(repo, unc)), 3, "reset: still ONE drift commit on the agent branch (2) — not stacked")
		// all four kinds in the status table
		all := m.rows(admin)
		four := []string{m.tplBranch, m.depBranch, agentBranch, unc}
		for _, b := range four {
			True(t, all.Find("gitBranch", b).Exists(), b)
		}
		kinds := []string{}
		for _, r := range all.Arr() {
			if k := r.At("branch.kind").Str(); slices.Contains(four, r.At("gitBranch").Str()) && !slices.Contains(kinds, k) {
				kinds = append(kinds, k)
			}
		}
		sort.Strings(kinds)
		Eq(t, kinds, L{"agent", "deployment", "template", "uncommitted"})
		// cleared → deleted
		app.API.Call(t, "agentPushDrift", A{"body": A{"headCommitId": a1ID, "scannedAt": ISO(time.Now().Add(2 * time.Second)), "fingerprint": "clear", "items": L{}, "notes": L{}, "liveConfig": nil}})
		wgUntil(t, "uncommitted deleted", func() J { return wgBool(fake.ref(repo, "heads/"+unc) == "") })
		wgUntil(t, "row deleted", func() J { return wgBool(m.row(admin, unc).At("status").Str() == "deleted") })
		// heartbeat: the agent head is not the mirrored one → catch-up item (agent online)
		NoErr(t, wgBranchDoc(agentBranch).Set(A{"last": A{"op": "commit", "opmId": "other", "seq": 1, "message": "", "at": ""}}, driver.Merge))
		app.API.Call(t, "heartbeat", A{"body": A{"siteSlug": "app", "agentVersion": "0.15.0-test", "headRevId": nil, "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0, "memPct": 0, "diskPct": 0}, "edge": nil, "keyVersion": 1, "advertiseHost": "127.0.0.1", "agentPort": 7080, "ip": "127.0.0.1", "hostname": "h", "branch": A{"head": A{"commitId": a1ID, "seq": 1, "hash": a1.At("hash"), "message": a1.At("message"), "author": a1.At("author"), "createdAt": a1.At("createdAt"), "kind": a1.At("kind")}, "mergedFrom": nil, "uncommitted": nil}}})
		wgUntil(t, "agent-online item", func() J {
			q, err := wgQueue().Where("reason", "==", "agent-online").Get()
			NoErr(t, err)
			for _, d := range q.Docs {
				if JOf(d.Data()).At("gitBranch").Str() == agentBranch {
					return wgBool(true)
				}
			}
			return wgBool(false)
		})
	})

	t.Run("queue listing + cleanup (disable)", func(t *testing.T) {
		m.ready(t)
		api := m.admin.Real
		q := api.Call(t, "listGithubMirrorQueue", A{"query": A{"limit": 50}})
		True(t, q.At("items").Len() > 0)
		NotContains(t, q.String(), GOOD)
		api.Call(t, "patchGithubMirror", A{"body": A{"enabled": false}})
		m.setCfg(t, A{"_scope": L{}})
	})
}
