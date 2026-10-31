package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

// GitHub mirror worker: drains the queue of ONE git branch at a time (lease on the branch doc), in orderKey order, and
// pushes with the Git Data API. A failure stops the branch (order!) with backoff; a rate limit or a refused token pauses
// the whole mirror.

const mirrorLeaseMs = 3 * 60_000

// ───────────────────────────────────────── history

type histEntry struct {
	Type, ID  string // commit | snapshot
	Seq       any
	CreatedAt string
	OrderKey  string
	// snapshot
	Site, Volume, TreeHash string
	FileCount              any
}

func opmBranchRef(ref *doc.Map) *doc.Map {
	switch ref.Val("kind") {
	case "template":
		return doc.Obj("kind", "template", "templateId", ref.Val("templateId"))
	case "deployment":
		return doc.Obj("kind", "deployment", "deploymentId", ref.Val("deploymentId"))
	}
	return doc.Obj("kind", "agent", "deploymentId", ref.Val("deploymentId"), "siteSlug", ref.Val("siteSlug"))
}

func history(t *MirrorTarget) ([]*histEntry, error) {
	r := t.Ref
	var out []*histEntry
	cs, err := CommitsOf(opmBranchRef(r)).OrderBy("seq", "asc").Select("commitId", "seq", "createdAt").Get()
	if err != nil {
		return nil, err
	}
	for _, d := range cs.Docs {
		x := d.Data()
		id, at := x.Str("commitId"), x.Str("createdAt")
		out = append(out, &histEntry{Type: "commit", ID: id, Seq: x.Val("seq"), CreatedAt: at, OrderKey: MirrorOrderKey(at, x.Val("seq"), id)})
	}
	if r.Val("kind") == "agent" {
		site := r.Str("siteSlug")
		vols, err := VcCol(r.Str("deploymentId")).Get()
		if err != nil {
			return nil, err
		}
		for _, v := range vols.Docs {
			if !strings.HasPrefix(v.ID(), site+":") {
				continue
			}
			vs, err := v.Ref().Collection("versions").Where("origin", "==", "snapshot").Select("versionId", "seq", "createdAt", "treeHash", "volumeSlug", "fileCount").Get()
			if err != nil {
				return nil, err
			}
			for _, d := range vs.Docs {
				x := d.Data()
				id, at := x.Str("versionId"), x.Str("createdAt")
				vol := x.Str("volumeSlug")
				if nullish(x.Val("volumeSlug")) {
					if p := strings.Split(v.ID(), ":"); len(p) > 1 {
						vol = p[1]
					}
				}
				fc := x.Val("fileCount")
				if nullish(fc) {
					fc = float64(0)
				}
				out = append(out, &histEntry{Type: "snapshot", ID: id, Seq: x.Val("seq"), CreatedAt: at, OrderKey: MirrorOrderKey(at, x.Val("seq"), id), Site: site, Volume: vol, TreeHash: x.Str("treeHash"), FileCount: fc})
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].OrderKey < out[b].OrderKey })
	return out, nil
}

// ───────────────────────────────────────── pushing

type push struct {
	cl *GithubClient
	m  *doc.Map
	x  *ExportCtx
	// sha256 hex of content → git sha (this run)
	known map[string]string
}

func maxFileBytes(m *doc.Map) float64 {
	if cloud.Env.Emulator() && m.Truthy("_maxFileBytes") {
		return m.Num("_maxFileBytes")
	}
	return MIRROR_MAX_FILE_BYTES
}

func gitShaFor(p *push, hex string, data func() ([]byte, error)) (string, error) {
	if hit := p.known[hex]; hit != "" {
		return hit, nil
	}
	repoKey := RepoKeyOf(p.m)
	cref := Mcol.Blobs().Doc(hex)
	c, err := cref.Get()
	if err != nil {
		return "", err
	}
	if c.Exists() && c.Data().Val("repoKey") == repoKey {
		sha := c.Data().Str("gitSha")
		p.known[hex] = sha
		return sha, nil
	}
	b, err := data()
	if err != nil {
		return "", err
	}
	sha := GitBlobSha(b)
	exists, err := p.cl.GetBlobExists(sha)
	if err != nil {
		return "", err
	}
	if !exists {
		got, err := p.cl.CreateBlob(b)
		if err != nil {
			return "", err
		}
		if got != sha {
			return "", fmt.Errorf("GitHub blob sha %s ≠ %s", got, sha)
		}
	}
	if err := cref.Set(doc.Obj("repoKey", repoKey, "gitSha", sha, "size", len(b), "at", doc.NowISO())); err != nil {
		return "", err
	}
	p.known[hex] = sha
	return sha, nil
}

func sha256OfBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// pushTree: ExportResult → git tree sha (+ notes, incl. skipped big files).
func pushTree(p *push, res *doc.Map) (string, []any, error) {
	max := maxFileBytes(p.m)
	maxText := JSString(max)
	notes := append([]any{}, res.Arr("notes")...)
	skipped := map[string][]string{}
	var dirs []string
	entries := []*doc.Map{}
	for _, f := range mapsOf(res.Arr("files")) {
		path := f.Str("path")
		if size := f.Num("size"); size > max {
			dir := ""
			rest := path
			if i := strings.LastIndexByte(path, '/'); i >= 0 {
				dir, rest = path[:i], path[i+1:]
			}
			if _, ok := skipped[dir]; !ok {
				dirs = append(dirs, dir)
			}
			skipped[dir] = append(skipped[dir], rest+"\t"+JSString(size)+" bytes")
			var site any
			if m := reSiteDir.FindStringSubmatch(path); m != nil {
				site = m[1]
			}
			notes = append(notes, doc.Obj("code", "SKIPPED_LARGE", "site", site, "message", path+" ("+JSString(size)+" bytes) is larger than "+maxText+" bytes and was not pushed"))
			continue
		}
		var sha string
		var err error
		if f.Has("blob") {
			h := cloud.HexOf(f.Str("blob"))
			sha, err = gitShaFor(p, h, func() ([]byte, error) {
				b, err := cloud.Blobs().Get("files/" + h)
				if err == nil && b == nil {
					err = errors.New("files/" + h + " is not stored")
				}
				return b, err
			})
		} else {
			var b []byte
			if t, ok := f.StrOK("text"); ok {
				b = []byte(t)
			} else if t, ok := f.StrOK("base64"); ok {
				b = doc.B64Decode(t)
			} else {
				b = []byte{}
			}
			sha, err = gitShaFor(p, sha256OfBytes(b), func() ([]byte, error) { return b, nil })
		}
		if err != nil {
			return "", nil, err
		}
		entries = append(entries, doc.Obj("path", path, "mode", f.Val("mode"), "type", "blob", "sha", sha))
	}
	for _, dir := range dirs {
		list := skipped[dir]
		sort.Slice(list, func(a, b int) bool { return CompareUtf16(list[a], list[b]) < 0 })
		b := []byte("# Files OPM did not push to GitHub (larger than " + maxText + " bytes; GitHub refuses files over 100 MB).\n# They exist in OPM (cloud Storage / the site) only.\n" + strings.Join(list, "\n") + "\n")
		sha, err := gitShaFor(p, sha256OfBytes(b), func() ([]byte, error) { return b, nil })
		if err != nil {
			return "", nil, err
		}
		path := ".skipped.txt"
		if dir != "" {
			path = dir + "/" + path
		}
		entries = append(entries, doc.Obj("path", path, "mode", "100644", "type", "blob", "sha", sha))
	}
	sort.SliceStable(entries, func(a, b int) bool { return CompareUtf16(entries[a].Str("path"), entries[b].Str("path")) < 0 })
	l := make([]any, len(entries))
	for i, e := range entries {
		l[i] = e
	}
	tree, err := p.cl.CreateTree(l)
	if err != nil {
		return "", nil, err
	}
	return tree, notes, nil
}

const mirrorReadme = "# OPM mirror\n\nThis repository is written by the On-Prem Manager (OPM) GitHub mirror.\n\nBranches:\n\n- `template/<slug>` — a template\n- `deployment/<slug>/cloud` — a deployment (what its sites should run)\n- `deployment/<slug>/agent/<site>` — the commits of one site (+ persist-to-cloud volume snapshots)\n- `deployment/<slug>/uncommitted/<site>` — changes made on that site outside OPM, not committed yet\n- `deployment/<slug>/site/<site>` — a site run with Docker only: written by that machine, never by the mirror\n\nEvery branch runs each site with Docker alone (docker compose + Caddy + Portainer, no agent): the last resort when the agent is not allowed. Open `sites/<site>/README.md` of a branch for the exact steps: clone, `git switch -c deployment/<slug>/site/<site>`, fill `site.env`, `./deploy.sh`, push; later `git merge` the cloud branch and `./deploy.sh` again.\n"

func ensureInitialized(p *push) error {
	if p.m.Truthy("initializedAt") {
		return nil
	}
	empty, err := p.cl.IsEmpty()
	if err != nil {
		return err
	}
	if empty {
		if err := p.cl.PutContents("README.md", []byte(mirrorReadme), "OPM mirror: initialize the repository", MIRROR_DEFAULT_BRANCH, doc.Obj("name", "OPM", "email", "opm@users.noreply.opm")); err != nil {
			return err
		}
	}
	at := doc.NowISO()
	if err := Mcol.Config().Set(doc.Obj("initializedAt", at), driver.Merge); err != nil {
		return err
	}
	p.m.Set("initializedAt", at)
	return nil
}

func branchMeta(t *MirrorTarget) *doc.Map {
	return doc.Obj("kind", t.Ref.Val("kind"), "gitBranch", t.GitBranch, "entitySlug", t.EntitySlug, "label", t.Label, "templateId", t.Ref.Val("templateId"), "deploymentId", t.Ref.Val("deploymentId"), "siteSlug", t.Ref.Val("siteSlug"))
}

func depIdFor(t *MirrorTarget) string {
	if t.Ref.Val("kind") == "template" {
		return "template-" + t.EntitySlug
	}
	return t.Ref.Str("deploymentId")
}

func sitesFor(t *MirrorTarget, c *doc.Map) []any {
	if k := t.Ref.Val("kind"); k == "agent" || k == "uncommitted" {
		return []any{t.Ref.Val("siteSlug")}
	}
	out := []any{}
	for _, s := range mapsOf(c.Arr("sites")) {
		out = append(out, s.Val("siteSlug"))
	}
	return out
}

type treeCache map[string]*doc.Map

// persistAt: the persist overlay of an agent branch at an entry: newest snapshot per volume up to (and incl.) orderKey.
func persistAt(t *MirrorTarget, hist []*histEntry, orderKey string, config *doc.Map, cache treeCache) (*doc.Map, error) {
	if k := t.Ref.Val("kind"); k != "agent" && k != "uncommitted" {
		return doc.New(), nil
	}
	site := t.Ref.Str("siteSlug")
	vols := map[string]bool{}
	for _, s := range mapsOf(config.Arr("sites")) {
		if s.Val("siteSlug") == site {
			for _, v := range mapsOf(s.Map("config").Arr("volumes")) {
				vols[v.Str("volumeSlug")] = true
			}
			break
		}
	}
	newest := map[string]*histEntry{}
	names := []string{}
	for _, e := range hist {
		if e.Type == "snapshot" && e.OrderKey <= orderKey && vols[e.Volume] {
			if newest[e.Volume] == nil {
				names = append(names, e.Volume)
			}
			newest[e.Volume] = e
		}
	}
	sort.Slice(names, func(a, b int) bool { return CompareUtf16(names[a], names[b]) < 0 })
	out := doc.New()
	for _, vol := range names {
		e := newest[vol]
		tree, ok := cache[e.TreeHash]
		if !ok {
			var err error
			if tree, err = ReadTree(e.TreeHash); err != nil {
				return nil, err
			}
			cache[e.TreeHash] = tree
		}
		if tree != nil {
			seq := e.Seq
			if nullish(seq) {
				seq = float64(0)
			}
			out.Set(vol, doc.Obj("versionId", e.ID, "seq", seq, "createdAt", e.CreatedAt, "treeHash", e.TreeHash, "tree", tree))
		}
	}
	if out.Len() == 0 {
		return doc.New(), nil
	}
	return doc.Obj(site, out), nil
}

type built struct {
	message string
	person  *doc.Map
	res     *doc.Map
}

func catalogMap(v any) *doc.Map {
	m, _ := v.(*doc.Map)
	return m
}

// buildEntry: export + commit metadata of one history entry (nil = nothing to push: a snapshot before any commit).
func buildEntry(p *push, t *MirrorTarget, hist []*histEntry, e *histEntry, commits map[string]*doc.Map, trees treeCache) (*built, error) {
	bref := opmBranchRef(t.Ref)
	site := t.Ref.Str("siteSlug")
	repository := p.m.Str("owner") + "/" + p.m.Str("repo")
	load := func(id string) (*doc.Map, error) {
		if c := commits[id]; c != nil {
			return c, nil
		}
		s, err := CommitsOf(bref).Doc(id).Get()
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, errors.New("commit " + id + " is not stored")
		}
		c, err := DecodeCommit(s.Data(), false)
		if err != nil {
			return nil, err
		}
		commits[id] = c
		return c, nil
	}
	if e.Type == "commit" {
		c, err := load(e.ID)
		if err != nil {
			return nil, err
		}
		config := c.Map("config")
		persist, err := persistAt(t, hist, e.OrderKey, config, trees)
		if err != nil {
			return nil, err
		}
		who := PersonOf(c.Map("author"), site)
		req, err := BuildRequest(p.x, ExportInput{
			Repository:   repository,
			Branch:       branchMeta(t),
			Commit:       doc.Obj("id", c.Val("commitId"), "seq", c.Val("seq"), "kind", c.Val("kind"), "message", c.Val("message"), "author", who, "createdAt", c.Val("createdAt"), "hash", c.Val("hash"), "parentId", c.Val("parentId")),
			DeploymentID: depIdFor(t),
			Config:       config,
			Catalog:      catalogMap(c.Val("catalog")),
			Sites:        sitesFor(t, config),
			Persist:      persist,
		})
		if err != nil {
			return nil, err
		}
		res, err := ExportTree(req, p.m.Truthy("_stubExporter"))
		if err != nil {
			return nil, err
		}
		message := MirrorCommitMessage(c.Str("message"),
			[2]string{trailerCommit, c.Str("commitId")},
			[2]string{trailerBranch, shared.BranchKey(bref)},
			[2]string{trailerSeq, JSString(c.Val("seq"))})
		return &built{message, who.Shallow().Set("date", c.Val("createdAt")), res}, nil
	}
	// snapshot: the config of the newest commit before it
	var base *histEntry
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Type == "commit" && hist[i].OrderKey < e.OrderKey {
			base = hist[i]
			break
		}
	}
	if base == nil {
		return nil, nil
	}
	c, err := load(base.ID)
	if err != nil {
		return nil, err
	}
	config := c.Map("config")
	who := MirrorSitePerson(e.Site)
	msg := "Volume snapshot " + e.Site + "/" + e.Volume + " #" + JSString(e.Seq) + " (" + JSString(e.FileCount) + " files)"
	persist, err := persistAt(t, hist, e.OrderKey, config, trees)
	if err != nil {
		return nil, err
	}
	req, err := BuildRequest(p.x, ExportInput{
		Repository:   repository,
		Branch:       branchMeta(t),
		Commit:       doc.Obj("id", e.ID, "seq", e.Seq, "kind", "volume-snapshot", "message", msg, "author", who, "createdAt", e.CreatedAt, "hash", strOrNull(e.TreeHash), "parentId", c.Val("commitId")),
		DeploymentID: depIdFor(t),
		Config:       config,
		Catalog:      catalogMap(c.Val("catalog")),
		Sites:        sitesFor(t, config),
		Persist:      persist,
	})
	if err != nil {
		return nil, err
	}
	res, err := ExportTree(req, p.m.Truthy("_stubExporter"))
	if err != nil {
		return nil, err
	}
	message := MirrorCommitMessage(msg, [2]string{trailerVolume, e.ID}, [2]string{trailerBranch, shared.BranchKey(bref)})
	return &built{message, who.Shallow().Set("date", e.CreatedAt), res}, nil
}

func bref(gitBranch string) *driver.DocRef { return Mcol.Branches().Doc(MirrorBranchDocId(gitBranch)) }

func branchURLs(m *doc.Map, gitBranch string, sha any) *doc.Map {
	repo := "https://github.com/" + m.Str("owner") + "/" + m.Str("repo")
	var gitURL any
	if doc.Truthy(sha) {
		gitURL = repo + "/commit/" + JSString(sha)
	}
	return doc.Obj("gitUrl", gitURL, "treeUrl", repo+"/tree/"+gitBranch)
}

func strNull(v any) any {
	if nullish(v) {
		return nil
	}
	return v
}

func notesHead(notes []any) []any {
	if len(notes) > 50 {
		notes = notes[:50]
	}
	return append([]any{}, notes...)
}

func saveState(p *push, t *MirrorTarget, sha string, last any, e *histEntry, message string, notes []any) error {
	summary, _, _ := strings.Cut(message, "\n")
	op := "volume-snapshot"
	if e.Type == "commit" {
		op = "commit"
	}
	return bref(t.GitBranch).Set(doc.Obj("gitSha", sha, "_lastOrderKey", last, "pushedAt", doc.NowISO(), "last", doc.Obj("op", op, "opmId", e.ID, "seq", e.Seq, "message", summary, "at", e.CreatedAt), "notes", notesHead(notes), "lastError", nil).Spread(branchURLs(p.m, t.GitBranch, sha)), driver.Merge)
}

// catchUp: push history entries after the branch state, up to upto ("" = all). Returns the number of git commits made.
func catchUp(p *push, t *MirrorTarget, upto string, rebuild bool) (int, error) {
	hist, err := history(t)
	if err != nil {
		return 0, err
	}
	s, err := bref(t.GitBranch).Get()
	if err != nil {
		return 0, err
	}
	st := s.Data()
	var last, parent any
	if !rebuild {
		last, parent = strNull(st.Val("_lastOrderKey")), strNull(st.Val("gitSha"))
	}
	remote, err := p.cl.GetRef(t.GitBranch)
	if err != nil {
		return 0, err
	}
	if !rebuild && remote != nil && remote != parent {
		// recovery: the remote head was written by us but the state write was lost (crash): adopt it via its trailers
		rc, err := p.cl.GetCommit(remote.(string))
		if err != nil {
			return 0, err
		}
		tr := ParseMirrorTrailers(rc.Str("message"))
		id, ok := tr[trailerCommit]
		if !ok {
			id, ok = tr[trailerVolume]
		}
		if ok && id != "" {
			for _, h := range hist {
				if h.ID == id {
					if l, isStr := last.(string); !isStr || l == "" || h.OrderKey > l {
						last = h.OrderKey
					}
					break
				}
			}
		}
		parent = remote // never rewrite a remote branch: continue on top of it
	}
	lastStr, _ := last.(string)
	var todo []*histEntry
	for _, h := range hist {
		if (lastStr == "" || h.OrderKey > lastStr) && (upto == "" || h.OrderKey <= upto) {
			todo = append(todo, h)
		}
	}
	if len(todo) == 0 && !rebuild {
		if remote != strNull(st.Val("gitSha")) || last != strNull(st.Val("_lastOrderKey")) {
			if err := bref(t.GitBranch).Set(doc.Obj("gitSha", remote, "_lastOrderKey", last).Spread(branchURLs(p.m, t.GitBranch, remote)), driver.Merge); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}
	if err := ensureInitialized(p); err != nil {
		return 0, err
	}
	commits := map[string]*doc.Map{}
	trees := treeCache{}
	made := 0
	notes := []any{}
	var lastEntry *histEntry
	run := todo
	if rebuild {
		run = hist
	}
	for _, e := range run {
		b, err := buildEntry(p, t, hist, e, commits, trees)
		if err != nil {
			return made, err
		}
		if b == nil {
			last = e.OrderKey
			continue
		}
		tree, n, err := pushTree(p, b.res)
		if err != nil {
			return made, err
		}
		notes = n
		parents := []any{}
		if parent != nil {
			parents = append(parents, parent)
		}
		sha, err := p.cl.CreateCommit(doc.Obj("message", b.message, "tree", tree, "parents", parents, "author", b.person, "committer", b.person))
		if err != nil {
			return made, err
		}
		if !rebuild {
			if err := p.cl.SetRef(t.GitBranch, sha, false); err != nil {
				return made, err
			}
		}
		parent = sha
		last = e.OrderKey
		lastEntry = e
		made++
		if !rebuild {
			if err := saveState(p, t, sha, last, e, b.message, notes); err != nil {
				return made, err
			}
		}
	}
	if rebuild {
		if parent != nil {
			if err := p.cl.SetRef(t.GitBranch, parent.(string), true); err != nil {
				return made, err
			}
		}
		if lastEntry != nil {
			if err := saveState(p, t, parent.(string), last, lastEntry, "", notes); err != nil {
				return made, err
			}
		}
	}
	return made, nil
}

// pushDrift: uncommitted/<site>: one snapshot commit of the drift on the agent branch head (force) or the ref deleted.
func pushDrift(p *push, t *MirrorTarget) (int, error) {
	dep := t.Ref.Str("deploymentId")
	site := t.Ref.Str("siteSlug")
	ab, err := Bcol.AgentBranches(dep).Doc(site).Get()
	if err != nil {
		return 0, err
	}
	d := ab.Data()
	drift := d.Map("drift")
	var live *doc.Map
	if s, ok := d.StrOK("_driftLive"); ok {
		if live, err = doc.ParseMap([]byte(s)); err != nil {
			return 0, err
		}
	}
	items, dnotes := drift.Arr("items"), drift.Arr("notes")
	if drift == nil || (len(items) == 0 && len(dnotes) == 0) || live == nil {
		if _, err := p.cl.DeleteRef(t.GitBranch); err != nil {
			return 0, err
		}
		fp := ""
		if drift != nil {
			fp = JSString(drift.Val("fingerprint"))
		}
		err := bref(t.GitBranch).Set(doc.Obj("status", "deleted", "gitSha", nil, "gitUrl", nil, "pushedAt", doc.NowISO(), "last", doc.Obj("op", "drift-clear", "opmId", fp, "seq", nil, "message", "drift committed or discarded: branch deleted", "at", doc.NowISO())), driver.Merge)
		return 1, err
	}
	fingerprint := drift.Str("fingerprint")
	st, err := bref(t.GitBranch).Get()
	if err != nil {
		return 0, err
	}
	if cur := st.Data(); cur.Map("last").Val("opmId") == fingerprint && cur.Val("status") != "deleted" {
		remote, err := p.cl.GetRef(t.GitBranch)
		if err != nil {
			return 0, err
		}
		if remote != nil {
			return 0, nil // same drift already pushed
		}
	}
	if err := ensureInitialized(p); err != nil {
		return 0, err
	}
	agentT, err := TargetOf(doc.Obj("kind", "agent", "templateId", nil, "deploymentId", dep, "siteSlug", site))
	if err != nil {
		return 0, err
	}
	as, err := bref(agentT.GitBranch).Get()
	if err != nil {
		return 0, err
	}
	parent := strNull(as.Data().Val("gitSha"))
	if parent == nil {
		if parent, err = p.cl.GetRef(agentT.GitBranch); err != nil {
			return 0, err
		}
	}
	secrets := []any{}
	for _, s := range mapsOf(live.Arr("secrets")) {
		var value any
		if s.Truthy("isSet") {
			value = "set"
		}
		secrets = append(secrets, s.Without("isSet", "value").Set("value", value))
	}
	config := live.Shallow().Set("secrets", secrets)
	var catalog *doc.Map
	head := strNull(drift.Val("headCommitId"))
	if h, ok := head.(string); ok && h != "" {
		hc, err := Bcol.AgentCommits(dep, site).Doc(h).Get()
		if err != nil {
			return 0, err
		}
		if hc.Exists() {
			c, err := DecodeCommit(hc.Data(), false)
			if err != nil {
				return 0, err
			}
			catalog = catalogMap(c.Val("catalog"))
		}
	}
	who := MirrorSitePerson(site)
	var keys []string
	for i, it := range items {
		if i == 50 {
			break
		}
		m, _ := it.(*doc.Map)
		keys = append(keys, "- "+JSString(m.Val("key")))
	}
	msg := "Uncommitted changes on " + site + " (" + strconv.Itoa(len(items)) + " items) — not committed in OPM\n\n" + strings.Join(keys, "\n")
	if len(items) > 50 {
		msg += "\n- … " + strconv.Itoa(len(items)-50) + " more"
	}
	if len(dnotes) > 0 {
		msg += "\n\n" + strconv.Itoa(len(dnotes)) + " drift note(s) (not expressible as config)"
	}
	scannedAt := drift.Val("scannedAt")
	req, err := BuildRequest(p.x, ExportInput{
		Repository:   p.m.Str("owner") + "/" + p.m.Str("repo"),
		Branch:       branchMeta(t),
		Commit:       doc.Obj("id", fingerprint, "seq", nil, "kind", "drift", "message", msg, "author", who, "createdAt", scannedAt, "hash", nil, "parentId", head),
		DeploymentID: dep,
		Config:       config,
		Catalog:      catalog,
		Sites:        []any{site},
		Persist:      doc.New(),
	})
	if err != nil {
		return 0, err
	}
	res, err := ExportTree(req, p.m.Truthy("_stubExporter"))
	if err != nil {
		return 0, err
	}
	tree, notes, err := pushTree(p, res)
	if err != nil {
		return 0, err
	}
	message := MirrorCommitMessage(msg, [2]string{trailerDrift, fingerprint}, [2]string{trailerBranch, shared.BranchKey(doc.Obj("kind", "agent", "deploymentId", dep, "siteSlug", site))})
	person := who.Shallow().Set("date", scannedAt)
	parents := []any{}
	if parent != nil {
		parents = append(parents, parent)
	}
	sha, err := p.cl.CreateCommit(doc.Obj("message", message, "tree", tree, "parents", parents, "author", person, "committer", person))
	if err != nil {
		return 0, err
	}
	if err := p.cl.SetRef(t.GitBranch, sha, true); err != nil {
		return 0, err
	}
	summary, _, _ := strings.Cut(msg, "\n")
	err = bref(t.GitBranch).Set(doc.Obj("gitSha", sha, "pushedAt", doc.NowISO(), "last", doc.Obj("op", "drift", "opmId", fingerprint, "seq", nil, "message", summary, "at", scannedAt), "notes", notesHead(notes), "lastError", nil).Spread(branchURLs(p.m, t.GitBranch, sha)), driver.Merge)
	return 1, err
}

// ───────────────────────────────────────── draining

func parsedAfterNow(s string) bool {
	t, ok := doc.ParseISO(s)
	return ok && t > time.Now().UnixMilli()
}

func acquire(gitBranch, leaseID string) (bool, error) {
	ref := bref(gitBranch)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (bool, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return false, err
		}
		cur := s.Data()
		if cur.Truthy("leaseUntil") && parsedAfterNow(cur.Str("leaseUntil")) && cur.Val("_leaseId") != leaseID {
			return false, nil
		}
		tx.Set(ref, doc.Obj("leaseUntil", doc.IsoPlus(mirrorLeaseMs), "_leaseId", leaseID, "status", "pushing"), driver.Merge)
		return true, nil
	})
}

func openItems(gitBranch string) ([]*doc.Map, error) {
	q, err := Mcol.Queue().Where("gitBranch", "==", gitBranch).Where("state", "in", openStates()).Get()
	if err != nil {
		return nil, err
	}
	out := make([]*doc.Map, len(q.Docs))
	for i, d := range q.Docs {
		out[i] = d.Data()
	}
	return out, nil
}

func release(gitBranch, leaseID string) error {
	ref := bref(gitBranch)
	items, err := openItems(gitBranch)
	if err != nil {
		return err
	}
	failed := 0
	var nexts []string
	for _, i := range items {
		if i.Val("state") == "failed" {
			failed++
		}
		nexts = append(nexts, i.Str("nextAttemptAt"))
	}
	m, err := LoadMirror()
	if err != nil {
		return err
	}
	paused := m.Truthy("pausedUntil") && parsedAfterNow(m.Str("pausedUntil"))
	return cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		s, err := tx.Get(ref)
		if err != nil {
			return err
		}
		cur := s.Data()
		if cur.Val("_leaseId") != leaseID {
			return nil
		}
		deleted := cur.Val("status") == "deleted"
		var status string
		switch {
		case deleted && len(items) == 0:
			status = "deleted"
		case failed > 0:
			status = "error"
			if paused {
				status = "paused"
			}
		case len(items) > 0:
			status = "queued"
			if paused {
				status = "paused"
			}
		case cur.Truthy("gitSha") || deleted:
			status = "synced"
			if deleted {
				status = "deleted"
			}
		default:
			status = "never"
		}
		var next any
		if len(nexts) > 0 {
			sort.Strings(nexts)
			next = nexts[0]
		}
		tx.Set(ref, doc.Obj("leaseUntil", nil, "_leaseId", nil, "status", status, "queue", doc.Obj("pending", len(items)-failed, "failed", failed, "nextAttemptAt", next), "updatedAt", doc.NowISO()), driver.Merge)
		return nil
	})
}

func pauseFor(e *GithubError) error {
	until := float64(time.Now().UnixMilli() + 3600_000)
	if e.RetryAt != nil {
		until = *e.RetryAt
	}
	iso := isoMillis(until)
	reason := "GitHub refused the token or the repository (" + JSString(e.StatusValue()) + "): " + doc.JSSlice(e.Message, 200) + " — fix the token / permissions, then Sync now"
	if e.RateLimited() {
		reason = "GitHub rate limit: pushing resumes at " + iso
	}
	err := Mcol.Config().Set(doc.Obj("pausedUntil", iso, "pauseReason", reason), driver.Merge)
	InvalidateMirrorCache()
	return err
}

// processItem processes one item (already marked running); a panic is a failed item, as an exception is in the TS.
func processItem(p *push, it *doc.Map) (state string, err error) {
	defer func() {
		if r := recover(); r != nil {
			driver.Log.Error("github mirror: item panicked", "stack", string(debug.Stack()))
			err = fmt.Errorf("%v", r)
		}
	}()
	t, err := TargetOf(it.Map("branch"))
	if err != nil {
		return "", err
	}
	if t.GitBranch != it.Str("gitBranch") {
		return "skipped", nil // the entity slug changed (never) / bad item
	}
	made := 0
	switch it.Str("op") {
	case "commit", "volume-snapshot":
		made, err = catchUp(p, t, it.Str("orderKey"), false)
	case "catch-up":
		made, err = catchUp(p, t, "", false)
	case "rebuild":
		made, err = catchUp(p, t, "", true)
	case "drift", "drift-clear":
		made, err = pushDrift(p, t)
	}
	if err != nil {
		return "", err
	}
	if made > 0 {
		return "done", nil
	}
	return "skipped", nil
}

func drainLoop(gitBranch string, started time.Time, budgetMs int64, processed *int) error {
	for {
		if time.Since(started).Milliseconds() > budgetMs {
			return nil
		}
		m, err := LoadMirror()
		if err != nil {
			return err
		}
		if !IsActive(m) {
			return nil
		}
		if m.Truthy("pausedUntil") && parsedAfterNow(m.Str("pausedUntil")) {
			return nil
		}
		items, err := openItems(gitBranch)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		sort.SliceStable(items, func(a, b int) bool {
			x, y := items[a].Str("orderKey"), items[b].Str("orderKey")
			if x != y {
				return x < y
			}
			return CompareUtf16(items[a].Str("itemId"), items[b].Str("itemId")) < 0
		})
		it := items[0]
		if parsedAfterNow(it.Str("nextAttemptAt")) {
			return nil // the oldest item waits (backoff): keep the order
		}
		p := &push{cl: ClientOf(m), m: m, x: NewExportCtx(), known: map[string]string{}}
		iref := Mcol.Queue().Doc(it.Str("itemId"))
		if err := iref.Update(doc.Obj("state", "running", "startedAt", doc.NowISO())); err != nil {
			return err
		}
		if err := bref(gitBranch).Set(doc.Obj("leaseUntil", doc.IsoPlus(mirrorLeaseMs)), driver.Merge); err != nil {
			return err
		}
		state, perr := processItem(p, it)
		if perr == nil {
			st, err := bref(gitBranch).Get()
			if err != nil {
				return err
			}
			if err := iref.Update(doc.Obj("state", state, "doneAt", doc.NowISO(), "gitSha", strNull(st.Data().Val("gitSha")), "lastError", nil)); err != nil {
				return err
			}
			*processed++
			continue
		}
		var ge *GithubError
		errors.As(perr, &ge)
		attempts := int(it.Num("attempts")) + 1
		var status any
		if ge != nil {
			status = ge.StatusValue()
		}
		msg := doc.JSSlice(perr.Error(), 1000)
		lastErr := doc.Obj("at", doc.NowISO(), "message", msg, "status", status)
		var next string
		if ge != nil && ge.RateLimited() {
			next = isoMillis(*ge.RetryAt)
		} else {
			next = doc.IsoPlus(int64(MirrorBackoffSec(attempts)) * 1000)
		}
		if err := iref.Update(doc.Obj("state", "failed", "attempts", attempts, "nextAttemptAt", next, "lastError", lastErr)); err != nil {
			return err
		}
		if err := bref(gitBranch).Set(doc.Obj("lastError", lastErr), driver.Merge); err != nil {
			return err
		}
		driver.Log.Warn("github mirror: push failed", "gitBranch", gitBranch, "op", it.Val("op"), "attempts", attempts, "error", msg)
		if attempts == 1 {
			cloud.AuditSystem(cloud.AuditSystemOpts{Job: "github-mirror", Action: "github.push-failed", TargetType: "github", Target: "githubMirror/branches/" + gitBranch, OK: false, ErrorMessage: &msg, Data: doc.Obj("gitBranch", gitBranch, "op", it.Val("op"), "opmId", it.Val("opmId"), "status", status)})
		}
		if ge != nil && (ge.RateLimited() || ge.Auth) {
			if err := pauseFor(ge); err != nil {
				return err
			}
		}
		return nil
	}
}

// DrainBranch is drainBranch: drain one branch (time budget). The number of items processed. Never fails.
func DrainBranch(gitBranch string, budgetMs int64) int {
	leaseID := ulid.New()
	started := time.Now()
	processed := 0
	ok, err := acquire(gitBranch, leaseID)
	if err != nil {
		driver.Log.Warn("github mirror: lease failed", "gitBranch", gitBranch, "error", err.Error())
		return 0
	}
	if !ok {
		return 0
	}
	defer func() {
		if err := release(gitBranch, leaseID); err != nil {
			driver.Log.Warn("github mirror: release failed", "gitBranch", gitBranch, "error", err.Error())
		}
	}()
	func() {
		defer func() {
			if r := recover(); r != nil {
				driver.Log.Error("github mirror: drain failed", "gitBranch", gitBranch, "error", fmt.Sprintf("%v\n%s", r, debug.Stack()))
			}
		}()
		if err := drainLoop(gitBranch, started, budgetMs, &processed); err != nil {
			driver.Log.Error("github mirror: drain failed", "gitBranch", gitBranch, "error", err.Error())
		}
	}()
	return processed
}

// DrainDue is drainDue: every branch with a due item (scheduler / Sync now).
func DrainDue(budgetMs int64) (int, error) {
	started := time.Now()
	m, err := LoadMirror()
	if err != nil {
		return 0, err
	}
	if !IsActive(m) {
		return 0, nil
	}
	if m.Truthy("pausedUntil") && parsedAfterNow(m.Str("pausedUntil")) {
		return 0, nil
	}
	q, err := Mcol.Queue().Where("state", "in", openStates()).Get()
	if err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	due := map[string]bool{}
	for _, d := range q.Docs {
		it := d.Data()
		// a "running" item whose worker died (lease expired) is retried
		if t, ok := doc.ParseISO(it.Str("nextAttemptAt")); ok && t <= now {
			due[it.Str("gitBranch")] = true
		}
	}
	names := make([]string, 0, len(due))
	for b := range due {
		names = append(names, b)
	}
	sort.Strings(names)
	n := 0
	for _, b := range names {
		left := budgetMs - time.Since(started).Milliseconds()
		if left < 20_000 {
			break
		}
		n += DrainBranch(b, left-10_000)
	}
	return n, nil
}

// RecoverStuck is recoverStuck: reset items stuck "running" by a dead worker (lease expired) back to pending.
func RecoverStuck() error {
	q, err := Mcol.Queue().Where("state", "==", "running").Get()
	if err != nil {
		return err
	}
	for _, d := range q.Docs {
		it := d.Data()
		b, err := bref(it.Str("gitBranch")).Get()
		if err != nil {
			return err
		}
		lease := b.Data().Str("leaseUntil")
		t, ok := doc.ParseISO(lease)
		if lease == "" || (ok && t < time.Now().UnixMilli()) {
			if err := d.Ref().Update(doc.Obj("state", "pending")); err != nil {
				return err
			}
		}
	}
	return nil
}
