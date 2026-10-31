package apitest

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/cloud/doc"
)

// Port of backend/test/fake-github.ts: a fake GitHub (local HTTP server) for the mirror tests: repos, user, rate_limit, Git
// Data API (blobs / trees / commits / refs) and contents PUT. Objects are REAL git objects (sha1 like git: blob / tree /
// commit), so a pushed tree can be compared with `git write-tree` and a branch can be checked out into files.
// Behaviour copied from GitHub where it matters: an EMPTY repository answers 409 "Git Repository is empty." to the Git Data
// API (only the Contents API can create the first commit); non-fast-forward ref updates without force → 422; rate limits
// (403 + x-ratelimit-remaining 0 / 429 + retry-after); permissions (no write → 403; unknown token → 401).

type fakeObject struct {
	typ  string // blob | tree | commit
	body []byte
}

type fakeRepo struct {
	owner, name   string
	private       bool
	objects       map[string]fakeObject
	refs          map[string]string // "heads/<branch>" → sha
	defaultBranch string            // "" = null
}

type fakeToken struct {
	login string
	// repos this token may see ("owner/name"); nil = all
	repos []string
	write bool
	// fine-grained tokens without user read: GET /user → 403
	noUser bool
}

type fakeFault struct {
	match   *regexp.Regexp
	method  string
	status  int
	times   int
	headers map[string]string
	body    string
}

// fakeFaultOpts are the options of fakeGithub.fail.
type fakeFaultOpts struct {
	method  string
	headers map[string]string
	body    string
}

type fakeCall struct {
	method, path string
	status       int
}

// fakeFile is one file of a checkout.
type fakeFile struct {
	bytes []byte
	mode  string
}

// fakeCommit is a parsed commit object.
type fakeCommit struct {
	sha, tree, message, author, committer string
	parents                               []string
}

type fakeTreeEntry struct{ mode, name, sha string }

// fakeGithub: the exported-to-test methods lock mu; the lower-case ones with a trailing "L" expect it held. net/http serves
// concurrently, node did not: one request at a time keeps the behaviour of the TS fake.
type fakeGithub struct {
	mu     sync.Mutex
	server *httptest.Server
	base   string
	repos  map[string]*fakeRepo
	tokens map[string]fakeToken
	faults []*fakeFault
	calls  []fakeCall
	limit  int
	remain int
}

func fakeSha1(typ string, body []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s %d\x00", typ, len(body))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func newFakeGithub() *fakeGithub {
	return &fakeGithub{repos: map[string]*fakeRepo{}, tokens: map[string]fakeToken{}, limit: 5000, remain: 5000}
}

func (f *fakeGithub) addRepo(owner, name string) *fakeRepo {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &fakeRepo{owner: owner, name: name, private: true, objects: map[string]fakeObject{}, refs: map[string]string{}}
	f.repos[strings.ToLower(owner+"/"+name)] = r
	return r
}

func (f *fakeGithub) addToken(token string, t fakeToken) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[token] = t
}

// fail makes the next `times` requests matching the path regex (+ method) fail with status.
func (f *fakeGithub) fail(match string, status, times int, o fakeFaultOpts) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, &fakeFault{match: regexp.MustCompile(match), method: o.method, status: status, times: times, headers: o.headers, body: o.body})
}

func (f *fakeGithub) clearFaults() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = nil
}

func (f *fakeGithub) repo(owner, name string) *fakeRepo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos[strings.ToLower(owner+"/"+name)]
}

func (f *fakeGithub) start() string {
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	f.base = f.server.URL
	return f.base
}

func (f *fakeGithub) stop() {
	if f.server != nil {
		f.server.Close()
	}
}

// ref is repo.refs.get(name) ("" = none).
func (f *fakeGithub) ref(r *fakeRepo, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return r.refs[name]
}

// checkout: the files of a commit / branch, path → bytes (+ mode).
func (f *fakeGithub) checkout(r *fakeRepo, branchOrSha string) map[string]fakeFile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkoutL(r, branchOrSha)
}

func (f *fakeGithub) commit(r *fakeRepo, sha string) fakeCommit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commitL(r, sha)
}

// log: the commits of a branch, newest first.
func (f *fakeGithub) log(r *fakeRepo, branch string) []fakeCommit {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []fakeCommit{}
	for sha := r.refs["heads/"+branch]; sha != ""; {
		c := f.commitL(r, sha)
		out = append(out, c)
		sha = ""
		if len(c.parents) > 0 {
			sha = c.parents[0]
		}
	}
	return out
}

func (f *fakeGithub) putL(r *fakeRepo, typ string, body []byte) string {
	sha := fakeSha1(typ, body)
	r.objects[sha] = fakeObject{typ, body}
	return sha
}

func (f *fakeGithub) checkoutL(r *fakeRepo, branchOrSha string) map[string]fakeFile {
	sha, ok := r.refs["heads/"+branchOrSha]
	if !ok {
		sha = branchOrSha
	}
	c := f.commitL(r, sha)
	out := map[string]fakeFile{}
	var walk func(tsha, prefix string)
	walk = func(tsha, prefix string) {
		for _, e := range f.readTreeL(r, tsha) {
			if e.mode == "40000" {
				walk(e.sha, prefix+e.name+"/")
			} else {
				out[prefix+e.name] = fakeFile{r.objects[e.sha].body, e.mode}
			}
		}
	}
	walk(c.tree, "")
	return out
}

func (f *fakeGithub) readTreeL(r *fakeRepo, sha string) []fakeTreeEntry {
	o, ok := r.objects[sha]
	if !ok || o.typ != "tree" {
		panic("no tree " + sha)
	}
	out := []fakeTreeEntry{}
	b := o.body
	for i := 0; i < len(b); {
		sp := i + bytes.IndexByte(b[i:], 0x20)
		nul := sp + bytes.IndexByte(b[sp:], 0)
		out = append(out, fakeTreeEntry{string(b[i:sp]), string(b[sp+1 : nul]), hex.EncodeToString(b[nul+1 : nul+21])})
		i = nul + 21
	}
	return out
}

func (f *fakeGithub) commitL(r *fakeRepo, sha string) fakeCommit {
	o, ok := r.objects[sha]
	if !ok || o.typ != "commit" {
		panic("no commit " + sha)
	}
	text := string(o.body)
	i := strings.Index(text, "\n\n")
	c := fakeCommit{sha: sha, message: text[i+2:]}
	for _, l := range strings.Split(text[:i], "\n") {
		switch {
		case strings.HasPrefix(l, "tree ") && c.tree == "":
			c.tree = l[5:]
		case strings.HasPrefix(l, "parent "):
			c.parents = append(c.parents, l[7:])
		case strings.HasPrefix(l, "author ") && c.author == "":
			c.author = l[7:]
		case strings.HasPrefix(l, "committer ") && c.committer == "":
			c.committer = l[10:]
		}
	}
	return c
}

func (f *fakeGithub) isAncestorL(r *fakeRepo, anc, of string) bool {
	seen := map[string]bool{}
	stack := []string{of}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s == anc {
			return true
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		if o, ok := r.objects[s]; ok && o.typ == "commit" {
			stack = append(stack, f.commitL(r, s).parents...)
		}
	}
	return false
}

type fakeTreeInput struct{ path, mode, sha string }

type fakeNode struct {
	files map[string]fakeTreeInput
	dirs  map[string]*fakeNode
}

// buildTreeL builds nested trees from flat entries (path with "/"), like GitHub POST /git/trees without base_tree.
func (f *fakeGithub) buildTreeL(r *fakeRepo, entries []fakeTreeInput) string {
	newNode := func() *fakeNode { return &fakeNode{map[string]fakeTreeInput{}, map[string]*fakeNode{}} }
	root := newNode()
	for _, e := range entries {
		parts := strings.Split(e.path, "/")
		n := root
		for _, p := range parts[:len(parts)-1] {
			if n.dirs[p] == nil {
				n.dirs[p] = newNode()
			}
			n = n.dirs[p]
		}
		n.files[parts[len(parts)-1]] = e
	}
	type item struct{ name, mode, sha, key string }
	var write func(n *fakeNode) string
	write = func(n *fakeNode) string {
		items := []item{}
		for name, e := range n.files {
			items = append(items, item{name, strings.TrimLeft(e.mode, "0"), e.sha, name})
		}
		for name, d := range n.dirs {
			items = append(items, item{name, "40000", write(d), name + "/"})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
		var body bytes.Buffer
		for _, x := range items {
			fmt.Fprintf(&body, "%s %s\x00", x.mode, x.name)
			raw, err := hex.DecodeString(x.sha)
			if err != nil {
				panic(err)
			}
			body.Write(raw)
		}
		return f.putL(r, "tree", body.Bytes())
	}
	return write(root)
}

func fakePerson(p J) string {
	ts := time.Now().Unix()
	if d := p.At("date"); d.Truthy() {
		ms, ok := doc.ParseISO(d.Str())
		if !ok {
			panic("bad date " + d.Str())
		}
		ts = ms / 1000
	}
	return fmt.Sprintf("%s <%s> %d +0000", p.At("name").Str(), p.At("email").Str(), ts)
}

func (f *fakeGithub) makeCommitL(r *fakeRepo, message, tree string, parents []string, author, committer J) string {
	if committer.IsNil() {
		committer = author
	}
	lines := []string{"tree " + tree}
	for _, p := range parents {
		lines = append(lines, "parent "+p)
	}
	lines = append(lines, "author "+fakePerson(author), "committer "+fakePerson(committer))
	return f.putL(r, "commit", []byte(strings.Join(lines, "\n")+"\n\n"+message))
}

func fakeB64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(s))
}

var (
	fakeRepoRe   = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)(/.*)?$`)
	fakeBlobRe   = regexp.MustCompile(`^blobs/([0-9a-f]{40})$`)
	fakeCommitRe = regexp.MustCompile(`^commits/([0-9a-f]{40})$`)
	fakeRefRe    = regexp.MustCompile(`^ref/heads/(.+)$`)
	fakeRefsRe   = regexp.MustCompile(`^refs/heads/(.+)$`)
	fakeBearerRe = regexp.MustCompile(`(?i)^Bearer\s+`)
)

// ── http

func (f *fakeGithub) sendL(w http.ResponseWriter, status int, body any, headers map[string]string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("x-ratelimit-limit", strconv.Itoa(f.limit))
	h.Set("x-ratelimit-remaining", strconv.Itoa(f.remain))
	h.Set("x-ratelimit-reset", strconv.FormatInt(time.Now().Unix()+3600, 10))
	for k, v := range headers {
		h.Set(k, v)
	}
	w.WriteHeader(status)
	if status != 204 {
		_, _ = w.Write([]byte(JOf(body).String()))
	}
}

func (f *fakeGithub) serve(w http.ResponseWriter, req *http.Request) {
	raw, err := io.ReadAll(req.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() {
		if e := recover(); e != nil {
			f.sendL(w, 500, A{"message": fmt.Sprint(e)}, nil)
		}
	}()
	if err != nil {
		panic(err)
	}
	f.handleL(w, req, raw)
}

func (f *fakeGithub) handleL(w http.ResponseWriter, req *http.Request, raw []byte) {
	path := req.URL.Path
	method := req.Method
	body := J{A{}}
	if len(raw) > 0 {
		if body = ParseJ(raw); !body.Exists() {
			panic("bad JSON body")
		}
	}
	done := func(status int, b any, h ...map[string]string) {
		f.calls = append(f.calls, fakeCall{method, path, status})
		var hdr map[string]string
		if len(h) > 0 {
			hdr = h[0]
		}
		f.sendL(w, status, b, hdr)
	}
	msg := func(m string) A { return A{"message": m} }
	for _, x := range f.faults {
		if x.times > 0 && x.match.MatchString(path) && (x.method == "" || x.method == method) {
			x.times--
			var b any = msg(fmt.Sprintf("injected %d", x.status))
			if x.body != "" {
				b = ParseJ([]byte(x.body))
			}
			done(x.status, b, x.headers)
			return
		}
	}
	tok, ok := f.tokens[fakeBearerRe.ReplaceAllString(req.Header.Get("Authorization"), "")]
	if !ok {
		done(401, msg("Bad credentials"))
		return
	}
	f.remain = max(0, f.remain-1)
	const noAccess = "Resource not accessible by personal access token"
	if path == "/user" {
		if tok.noUser {
			done(403, msg(noAccess))
		} else {
			done(200, A{"login": tok.login})
		}
		return
	}
	if path == "/rate_limit" {
		done(200, A{"resources": A{"core": A{"limit": f.limit, "remaining": f.remain, "reset": time.Now().Unix() + 3600}}})
		return
	}
	m := fakeRepoRe.FindStringSubmatch(path)
	if m == nil {
		done(404, msg("Not Found"))
		return
	}
	key := strings.ToLower(m[1] + "/" + m[2])
	r := f.repos[key]
	can := r != nil && tok.repos == nil
	if r != nil && !can {
		for _, x := range tok.repos {
			can = can || strings.ToLower(x) == key
		}
	}
	if r == nil || !can {
		done(404, msg("Not Found"))
		return
	}
	rest := m[3]
	empty := len(r.refs) == 0
	if rest == "" && method == "GET" {
		var def any
		if r.defaultBranch != "" {
			def = r.defaultBranch
		}
		size := 1
		if empty {
			size = 0
		}
		done(200, A{"full_name": r.owner + "/" + r.name, "private": r.private, "default_branch": def, "html_url": "https://github.com/" + r.owner + "/" + r.name, "size": size, "permissions": A{"pull": true, "push": tok.write, "admin": false}})
		return
	}
	if strings.HasPrefix(rest, "/contents/") && method == "PUT" {
		if !tok.write {
			done(403, msg(noAccess))
			return
		}
		p := rest[len("/contents/"):]
		branch := body.At("branch").Str()
		if body.At("branch").IsNil() {
			if branch = r.defaultBranch; branch == "" {
				branch = "main"
			}
		}
		parent := r.refs["heads/"+branch]
		content, err := fakeB64(body.At("content").Str())
		if err != nil {
			panic(err)
		}
		blob := f.putL(r, "blob", content)
		entries := []fakeTreeInput{}
		if parent != "" {
			for k, v := range f.checkoutL(r, parent) {
				if k != p {
					entries = append(entries, fakeTreeInput{k, v.mode, f.putL(r, "blob", v.bytes)})
				}
			}
		}
		entries = append(entries, fakeTreeInput{p, "100644", blob})
		tree := f.buildTreeL(r, entries)
		who := body.At("author")
		if who.IsNil() {
			who = JOf(A{"name": tok.login, "email": tok.login + "@users.noreply.github.com"})
		}
		committer := body.At("committer")
		if committer.IsNil() {
			committer = who
		}
		parents := []string{}
		if parent != "" {
			parents = append(parents, parent)
		}
		sha := f.makeCommitL(r, body.At("message").Str(), tree, parents, who, committer)
		r.refs["heads/"+branch] = sha
		if r.defaultBranch == "" {
			r.defaultBranch = branch
		}
		done(201, A{"content": A{"path": p}, "commit": A{"sha": sha}})
		return
	}
	if strings.HasPrefix(rest, "/git/") {
		if empty {
			done(409, msg("Git Repository is empty."))
			return
		}
		g := rest[5:]
		if g == "matching-refs/heads/" || g == "matching-refs/heads" {
			keys := make([]string, 0, len(r.refs))
			for k := range r.refs {
				if strings.HasPrefix(k, "heads/") {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			out := L{}
			for _, k := range keys {
				out = append(out, A{"ref": "refs/" + k, "object": A{"sha": r.refs[k]}})
			}
			done(200, out)
			return
		}
		if g == "blobs" && method == "POST" {
			if !tok.write {
				done(403, msg(noAccess))
				return
			}
			content := []byte(body.At("content").Str())
			if body.At("encoding").Str() == "base64" {
				var err error
				if content, err = fakeB64(body.At("content").Str()); err != nil {
					panic(err)
				}
			}
			done(201, A{"sha": f.putL(r, "blob", content)})
			return
		}
		if mm := fakeBlobRe.FindStringSubmatch(g); mm != nil && method == "GET" {
			if o, ok := r.objects[mm[1]]; ok && o.typ == "blob" {
				done(200, A{"sha": mm[1], "size": len(o.body)})
			} else {
				done(404, msg("Not Found"))
			}
			return
		}
		if g == "trees" && method == "POST" {
			if !tok.write {
				done(403, msg(noAccess))
				return
			}
			entries := []fakeTreeInput{}
			for _, e := range body.At("tree").Arr() {
				sha := e.At("sha").Str()
				if _, ok := r.objects[sha]; !ok {
					done(422, msg("tree.sha "+sha+" is not a valid blob ("+e.At("path").Str()+")"))
					return
				}
				entries = append(entries, fakeTreeInput{e.At("path").Str(), e.At("mode").Str(), sha})
			}
			done(201, A{"sha": f.buildTreeL(r, entries)})
			return
		}
		if g == "commits" && method == "POST" {
			if !tok.write {
				done(403, msg(noAccess))
				return
			}
			tree := body.At("tree").Str()
			if _, ok := r.objects[tree]; !ok {
				done(422, msg("Tree SHA does not exist"))
				return
			}
			parents := []string{}
			for _, p := range body.At("parents").Arr() {
				if _, ok := r.objects[p.Str()]; !ok {
					done(422, msg("Parent SHA does not exist"))
					return
				}
				parents = append(parents, p.Str())
			}
			done(201, A{"sha": f.makeCommitL(r, body.At("message").Str(), tree, parents, body.At("author"), body.At("committer"))})
			return
		}
		if mm := fakeCommitRe.FindStringSubmatch(g); mm != nil && method == "GET" {
			if o, ok := r.objects[mm[1]]; !ok || o.typ != "commit" {
				done(404, msg("Not Found"))
				return
			}
			c := f.commitL(r, mm[1])
			ps := L{}
			for _, p := range c.parents {
				ps = append(ps, A{"sha": p})
			}
			done(200, A{"sha": c.sha, "message": c.message, "tree": A{"sha": c.tree}, "parents": ps})
			return
		}
		if mm := fakeRefRe.FindStringSubmatch(g); mm != nil && method == "GET" {
			if sha := r.refs["heads/"+mm[1]]; sha != "" {
				done(200, A{"ref": "refs/heads/" + mm[1], "object": A{"sha": sha, "type": "commit"}})
			} else {
				done(404, msg("Not Found"))
			}
			return
		}
		if g == "refs" && method == "POST" {
			if !tok.write {
				done(403, msg(noAccess))
				return
			}
			name := strings.TrimPrefix(body.At("ref").Str(), "refs/")
			if _, has := r.refs[name]; has {
				done(422, msg("Reference already exists"))
				return
			}
			sha := body.At("sha").Str()
			if o, ok := r.objects[sha]; !ok || o.typ != "commit" {
				done(422, msg("Object does not exist"))
				return
			}
			// git: a ref cannot be both a file and a directory
			for k := range r.refs {
				if strings.HasPrefix(k, name+"/") || strings.HasPrefix(name, k+"/") {
					done(422, msg("Reference cannot be created: "+k+" exists"))
					return
				}
			}
			r.refs[name] = sha
			done(201, A{"ref": "refs/" + name, "object": A{"sha": sha}})
			return
		}
		if mm := fakeRefsRe.FindStringSubmatch(g); mm != nil && method == "PATCH" {
			if !tok.write {
				done(403, msg(noAccess))
				return
			}
			cur, has := r.refs["heads/"+mm[1]]
			if !has {
				done(422, msg("Reference does not exist"))
				return
			}
			sha := body.At("sha").Str()
			if !body.At("force").Truthy() && !f.isAncestorL(r, cur, sha) {
				done(422, msg("Update is not a fast forward"))
				return
			}
			r.refs["heads/"+mm[1]] = sha
			done(200, A{"ref": "refs/heads/" + mm[1], "object": A{"sha": sha}})
			return
		} else if mm != nil && method == "DELETE" {
			if !tok.write {
				done(403, msg(noAccess))
				return
			}
			if _, has := r.refs["heads/"+mm[1]]; !has {
				done(422, msg("Reference does not exist"))
				return
			}
			delete(r.refs, "heads/"+mm[1])
			done(204, nil)
			return
		}
	}
	done(404, msg("Not Found"))
}
