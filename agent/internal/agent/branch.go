package agent

// §21 #104 agent branch: commits, cloud head, merges, history, push to the cloud mirror.
// docs-v2/branches-commits.md §3.5–§3.7, §5, §7.2. §21 #105 (§15): no working copy — GUI edits live in the browser and
// are committed with { config, baseCommitId } (working_commit.go); "uncommitted" = drift only.
//
// Mapping onto the existing engine: a commit of this site IS a revision (revId = commitId); kv `head` = the branch
// head; a merge / commit-with-edits / revert / reset / discard is an UpdatePlan of kind "merge" whose COMMIT step writes
// the commit row (commitBranchAt). Nothing is written to history when the apply fails (rollback, no commit).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/branch"
	"opm/agent/internal/canonical"
	"opm/agent/internal/diff"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/store"
	"opm/agent/internal/validate"
)

// ───────────────────────── mode

// branchMode: the cloud speaks branches (CloudView.branch seen once; sticky in kv so an offline restart keeps it).
// Without it (old cloud, the legacy mesh tests) the agent keeps the proposal flow unchanged (decision BR-G2).
func (a *Agent) branchMode() bool {
	if v := a.cloudView(); v != nil && v.Branch != nil {
		return true
	}
	var on bool
	ok, _ := a.db.GetKV("branchMode", &on)
	return ok && on
}

func (a *Agent) enterBranchMode() {
	var on bool
	if ok, _ := a.db.GetKV("branchMode", &on); ok && on {
		return
	}
	if h := a.head(); h != nil {
		_ = a.db.SetKV("legacyHead", *h) // the legacy (proposal) chain ends here for this site
	}
	_ = a.db.SetKV("branchMode", true)
	a.migrateCommits()
	a.convertLocalProposals()
	a.startLegacyWorkingMigration() // §21 #105 J13 (converted local proposals)
	a.pushAllCommits()
	a.log.Info("branch", "branch mode on (the cloud serves the deployment branch)")
}

// ───────────────────────── commits (local branch)

func scanCommit(row interface{ Scan(...any) error }) (*model.Commit, error) {
	var c model.Commit
	var parent, source, revertOf, resetTo, rollbackOf, catalog, catalogHash, pushed sql.NullString
	var author, cfg, stats, data string
	if err := row.Scan(&c.CommitID, &c.Seq, &parent, &c.Kind, &c.Message, &author, &c.CreatedAt, &source, &revertOf, &resetTo, &rollbackOf, &cfg, &c.Hash, &catalog, &catalogHash, &stats, &data, &pushed); err != nil {
		return nil, err
	}
	if parent.Valid {
		p := parent.String
		c.ParentID = &p
	}
	_ = json.Unmarshal([]byte(author), &c.Author)
	if source.Valid {
		_ = json.Unmarshal([]byte(source.String), &c.Source)
	}
	if revertOf.Valid {
		_ = json.Unmarshal([]byte(revertOf.String), &c.RevertOf)
	}
	if resetTo.Valid {
		_ = json.Unmarshal([]byte(resetTo.String), &c.ResetTo)
	}
	if rollbackOf.Valid {
		_ = json.Unmarshal([]byte(rollbackOf.String), &c.RollbackOf)
	}
	if err := json.Unmarshal([]byte(cfg), &c.Config); err != nil {
		return nil, err
	}
	if catalog.Valid && catalog.String != "" && catalog.String != "null" {
		var cat model.RevisionCatalog
		if json.Unmarshal([]byte(catalog.String), &cat) == nil {
			c.Catalog = &cat
		}
	}
	if catalogHash.Valid {
		h := catalogHash.String
		c.CatalogHash = &h
	}
	_ = json.Unmarshal([]byte(stats), &c.Stats)
	_ = json.Unmarshal([]byte(data), &c.Data)
	if c.Stats.Keys == nil {
		c.Stats.Keys = []string{}
	}
	return &c, nil
}

const commitCols = `commitId, seq, parentId, kind, message, author, createdAt, source, revertOf, resetTo, rollbackOf, config, hash, catalog, catalogHash, stats, data, pushedAt`

func jsonOrNil(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case *model.CommitSource:
		if x == nil {
			return nil
		}
	case *model.CommitSeqRef:
		if x == nil {
			return nil
		}
	case *model.SnapshotRefOnly:
		if x == nil {
			return nil
		}
	case *model.RevisionCatalog:
		if x == nil {
			return nil
		}
	}
	return store.JSON(v)
}

func (a *Agent) putCommit(c *model.Commit) error {
	_, err := a.db.Exec(`INSERT INTO commits(`+commitCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL) ON CONFLICT(commitId) DO NOTHING`,
		c.CommitID, c.Seq, store.NullStr(c.ParentID), c.Kind, c.Message, store.JSON(c.Author), c.CreatedAt, jsonOrNil(c.Source), jsonOrNil(c.RevertOf), jsonOrNil(c.ResetTo), jsonOrNil(c.RollbackOf),
		store.JSON(c.Config), c.Hash, jsonOrNil(c.Catalog), store.NullStr(c.CatalogHash), store.JSON(c.Stats), store.JSON(c.Data))
	return err
}

func (a *Agent) commitByID(id string) (*model.Commit, bool) {
	c, err := scanCommit(a.db.QueryRow(`SELECT `+commitCols+` FROM commits WHERE commitId = ?`, id))
	if err == nil && c.Branch.Kind == "" {
		c.Branch = a.ownBranch() // the commits table holds only this site's branch (no branch column)
	}
	return c, err == nil
}

// headCommit is the commit of the branch head (migrating the revision chain first when needed).
func (a *Agent) headCommit() *model.Commit {
	h := a.head()
	if h == nil {
		return nil
	}
	if c, ok := a.commitByID(h.RevID); ok {
		return c
	}
	a.migrateCommits()
	c, _ := a.commitByID(h.RevID)
	return c
}

func (a *Agent) commitPage(beforeSeq, limit int) ([]model.CommitSummary, *string) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + commitCols + ` FROM commits`
	args := []any{}
	if beforeSeq > 0 {
		q += ` WHERE seq < ?`
		args = append(args, beforeSeq)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := a.db.Query(q, args...)
	if err != nil {
		return []model.CommitSummary{}, nil
	}
	var list []*model.Commit
	for rows.Next() {
		if c, err := scanCommit(rows); err == nil {
			if c.Branch.Kind == "" {
				c.Branch = a.ownBranch() // the commits table holds only this site's branch (no branch column)
			}
			list = append(list, c)
		}
	}
	rows.Close()
	out := []model.CommitSummary{}
	var next *string
	for i, c := range list {
		if i == limit {
			s := strconv.Itoa(list[i-1].Seq)
			next = &s
			break
		}
		out = append(out, c.Summary())
	}
	return out, next
}

// commitFromRevision converts a stored revision (pre-0.14 or legacy) into a migrated commit.
func (a *Agent) commitFromRevision(r *model.Revision, parent *model.Revision) *model.Commit {
	msg := strings.TrimSpace(r.Summary)
	if msg == "" {
		msg = fmt.Sprintf("migrated revision %d", r.Seq)
	}
	kind := "migrated"
	email := r.Actor
	if email == "" {
		email = "system"
	}
	akind := "person"
	if !strings.Contains(email, "@") {
		akind = "system" // cloud uid / system (the cloud mirror resolves uids)
	}
	var pc any
	if parent != nil {
		pc = parent.Config
	}
	cat := r.Catalog
	ch := r.CatalogHash
	return &model.Commit{CommitID: r.RevID, Branch: a.ownBranch(), Seq: r.Seq, ParentID: r.ParentRevID, Kind: kind, Message: msg,
		Author: model.CommitAuthor{Kind: akind, Email: email}, CreatedAt: r.CreatedAt, Config: r.Config, Hash: r.Hash, Catalog: &cat, CatalogHash: &ch,
		Stats: branch.Stats(pc, r.Config, branch.Options{SecretValues: true, Scope: a.site}), Data: model.CommitData{MigratedFrom: &model.MigratedFrom{RevID: r.RevID, Seq: r.Seq}}}
}

func (a *Agent) ownBranch() model.BranchRef {
	return model.BranchRef{Kind: "agent", DeploymentID: a.dep, SiteSlug: a.site}
}

// migrateCommits (idempotent): every revision of the head chain without a commit row becomes a migrated commit
// (commitId = revId, §21 #104 §5.1); the deployment merge base starts at the head.
func (a *Agent) migrateCommits() {
	h := a.head()
	if h == nil {
		return
	}
	var chain []*model.Revision
	id := h.RevID
	for id != "" && len(chain) < 100000 {
		if _, ok := a.commitByID(id); ok {
			break
		}
		r, ok := a.revision(id)
		if !ok {
			break
		}
		chain = append(chain, r)
		if r.ParentRevID == nil {
			break
		}
		id = *r.ParentRevID
	}
	for i := len(chain) - 1; i >= 0; i-- {
		var parent *model.Revision
		if i+1 < len(chain) {
			parent = chain[i+1]
		} else if chain[i].ParentRevID != nil {
			parent, _ = a.revision(*chain[i].ParentRevID)
		}
		c := a.commitFromRevision(chain[i], parent)
		if err := a.putCommit(c); err != nil {
			a.log.Warn("branch", "migrate revision", "revId", chain[i].RevID, "error", err.Error())
			continue
		}
		a.enqueueCommitPush(c)
	}
	if _, ok := a.mergeBase(); !ok {
		if r := a.headRevision(); r != nil {
			seq := r.Seq
			a.setMergeBase(&mergeBaseRow{SourceCommitID: &r.RevID, SourceSeq: &seq, TargetCommitID: &r.RevID, Base: branch.MarkSecretValues(r.Config), Skipped: []model.MergeSkip{}, UpdatedBy: "system"})
		}
	}
}

// convertLocalProposals: open LOCAL proposals on the head become uncommitted working edits (§21 #104 §5.1).
func (a *Agent) convertLocalProposals() {
	h := a.head()
	for _, p := range a.openProposals() {
		if p.Origin != "agent" || h == nil || p.ParentRevID == nil || *p.ParentRevID != h.RevID {
			continue
		}
		rev, ok := a.revision(p.RevID)
		if !ok {
			continue
		}
		a.saveLegacyWorking(rev.Config, p.CreatedBy) // §21 #105: auto-committed (or dropped) by the start-up migration
		p.Status = model.ProposalSuperseded
		p.UpdatedAt = nowTs()
		a.putProposal(&p)
		a.stopStaging(p.RevID)
		a.log.Info("branch", "local proposal became uncommitted changes", "revId", p.RevID)
	}
}

// ───────────────────────── merge base (this branch ← the deployment branch)

type mergeBaseRow struct {
	SourceCommitID *string
	SourceSeq      *int
	TargetCommitID *string
	Base           model.Config
	Skipped        []model.MergeSkip
	UpdatedAt      string
	UpdatedBy      string
}

func (a *Agent) mergeBase() (*mergeBaseRow, bool) {
	var m mergeBaseRow
	var sc, tc sql.NullString
	var ss sql.NullInt64
	var base, skipped string
	if err := a.db.QueryRow(`SELECT sourceCommitId, sourceSeq, targetCommitId, base, skipped, updatedAt, updatedBy FROM merge_bases WHERE sourceKey = 'deployment'`).Scan(&sc, &ss, &tc, &base, &skipped, &m.UpdatedAt, &m.UpdatedBy); err != nil {
		return nil, false
	}
	if sc.Valid {
		s := sc.String
		m.SourceCommitID = &s
	}
	if ss.Valid {
		n := int(ss.Int64)
		m.SourceSeq = &n
	}
	if tc.Valid {
		s := tc.String
		m.TargetCommitID = &s
	}
	_ = json.Unmarshal([]byte(base), &m.Base)
	_ = json.Unmarshal([]byte(skipped), &m.Skipped)
	return &m, true
}

func (a *Agent) setMergeBase(m *mergeBaseRow) {
	if m.Skipped == nil {
		m.Skipped = []model.MergeSkip{}
	}
	var seq any
	if m.SourceSeq != nil {
		seq = *m.SourceSeq
	}
	_, err := a.db.Exec(`INSERT INTO merge_bases(sourceKey, sourceCommitId, sourceSeq, targetCommitId, base, skipped, updatedAt, updatedBy) VALUES('deployment',?,?,?,?,?,?,?)
		ON CONFLICT(sourceKey) DO UPDATE SET sourceCommitId=excluded.sourceCommitId, sourceSeq=excluded.sourceSeq, targetCommitId=excluded.targetCommitId, base=excluded.base, skipped=excluded.skipped, updatedAt=excluded.updatedAt, updatedBy=excluded.updatedBy`,
		store.NullStr(m.SourceCommitID), seq, store.NullStr(m.TargetCommitID), store.JSON(m.Base), store.JSON(m.Skipped), nowTs(), m.UpdatedBy)
	if err != nil {
		a.log.Warn("branch", "merge base", "error", err.Error())
	}
}

func (a *Agent) mergedFrom() *model.CommitSeqRef {
	if m, ok := a.mergeBase(); ok && m.SourceCommitID != nil && m.SourceSeq != nil {
		return &model.CommitSeqRef{CommitID: *m.SourceCommitID, Seq: *m.SourceSeq}
	}
	return nil
}

// ───────────────────────── cloud commits (the deployment branch as this site knows it)

func (a *Agent) putCloudCommit(c *model.Commit, via string) {
	var cfg any
	if c.Config.Sites != nil || c.Config.Variables != nil {
		cfg = store.JSON(c.Config)
	}
	_, _ = a.db.Exec(`INSERT INTO cloud_commits(commitId, seq, parentId, kind, message, author, createdAt, source, config, hash, catalog, catalogHash, data, fetchedAt, via)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(commitId) DO UPDATE SET config = COALESCE(excluded.config, cloud_commits.config),
		catalog = COALESCE(excluded.catalog, cloud_commits.catalog), catalogHash = COALESCE(excluded.catalogHash, cloud_commits.catalogHash),
		fetchedAt = COALESCE(excluded.fetchedAt, cloud_commits.fetchedAt), via = COALESCE(excluded.via, cloud_commits.via)`,
		c.CommitID, c.Seq, store.NullStr(c.ParentID), c.Kind, c.Message, store.JSON(c.Author), c.CreatedAt, jsonOrNil(c.Source), cfg, c.Hash, jsonOrNil(c.Catalog), store.NullStr(c.CatalogHash), store.JSON(c.Data), nullIfEmpty(fetchedAtOf(cfg)), nullIfEmpty(via))
}

func fetchedAtOf(cfg any) string {
	if cfg == nil {
		return ""
	}
	return nowTs()
}

func (a *Agent) putCloudMeta(r model.CommitRef) {
	_, _ = a.db.Exec(`INSERT INTO cloud_commits(commitId, seq, kind, message, author, createdAt, hash, data) VALUES(?,?,?,?,?,?,?,'{}') ON CONFLICT(commitId) DO NOTHING`,
		r.CommitID, r.Seq, r.Kind, r.Message, store.JSON(r.Author), r.CreatedAt, r.Hash)
}

// cloudCommit returns a deployment commit with its config (false when only the meta is cached).
func (a *Agent) cloudCommit(id string) (*model.Commit, bool) {
	var c model.Commit
	var parent, source, cfg, catalog, catalogHash sql.NullString
	var author, data string
	err := a.db.QueryRow(`SELECT commitId, seq, parentId, kind, message, author, createdAt, source, config, hash, catalog, catalogHash, data FROM cloud_commits WHERE commitId = ?`, id).
		Scan(&c.CommitID, &c.Seq, &parent, &c.Kind, &c.Message, &author, &c.CreatedAt, &source, &cfg, &c.Hash, &catalog, &catalogHash, &data)
	if err != nil || !cfg.Valid {
		return nil, false
	}
	if parent.Valid {
		p := parent.String
		c.ParentID = &p
	}
	_ = json.Unmarshal([]byte(author), &c.Author)
	if source.Valid {
		_ = json.Unmarshal([]byte(source.String), &c.Source)
	}
	if json.Unmarshal([]byte(cfg.String), &c.Config) != nil {
		return nil, false
	}
	if catalog.Valid {
		var cat model.RevisionCatalog
		if json.Unmarshal([]byte(catalog.String), &cat) == nil {
			c.Catalog = &cat
		}
	}
	if catalogHash.Valid {
		h := catalogHash.String
		c.CatalogHash = &h
	}
	_ = json.Unmarshal([]byte(data), &c.Data)
	c.Branch = model.BranchRef{Kind: "deployment", DeploymentID: a.dep}
	return &c, true
}

func (a *Agent) cloudCommitMetas(limit int) []model.CommitRef {
	rows, err := a.db.Query(`SELECT commitId, seq, kind, message, author, createdAt, hash FROM cloud_commits ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return []model.CommitRef{}
	}
	defer rows.Close()
	out := []model.CommitRef{}
	for rows.Next() {
		var r model.CommitRef
		var author string
		if rows.Scan(&r.CommitID, &r.Seq, &r.Kind, &r.Message, &author, &r.CreatedAt, &r.Hash) == nil {
			_ = json.Unmarshal([]byte(author), &r.Author)
			out = append(out, r)
		}
	}
	return out
}

func verifyCommit(c *model.Commit) error {
	if h := branch.ConfigHash(c.Config); h != c.Hash {
		return fmt.Errorf("commit %s: config hash mismatch", c.CommitID)
	}
	if c.Catalog != nil && c.CatalogHash != nil {
		if h, _ := canonical.HashCanonical(c.Catalog); h != *c.CatalogHash {
			return fmt.Errorf("commit %s: catalog hash mismatch", c.CommitID)
		}
	}
	return nil
}

// ensureCloudCommit fetches a deployment commit (cloud, else a peer) and caches it.
func (a *Agent) ensureCloudCommit(ctx context.Context, id string) (*model.Commit, error) {
	if c, ok := a.cloudCommit(id); ok {
		return c, nil
	}
	var resp model.AgentCommitResponse
	err := a.cloudJSON(ctx, "GET", "/v1/agent/commits/"+id, nil, &resp, callOpts{})
	via := "cloud"
	if err != nil {
		var last error = err
		for _, p := range a.reachablePeers() {
			var pr model.AgentCommitResponse
			if e := a.peerJSON(ctx, p, "GET", "/_opm/peer/cloud-commits/"+id, nil, &pr, 30*time.Second); e == nil {
				resp, via, last = pr, "peer:"+p, nil
				break
			} else {
				last = e
			}
		}
		if last != nil {
			return nil, last
		}
	}
	c := resp.Commit
	if err := verifyCommit(&c); err != nil {
		return nil, err
	}
	a.putCloudCommit(&c, via)
	cc, _ := a.cloudCommit(id)
	return cc, nil
}

// reconcileBranch adopts CloudView.branch: metas, the head commit (fetched + staged advisory for this site).
func (a *Agent) reconcileBranch(ctx context.Context, v *model.CloudView) {
	if v.Branch == nil {
		return
	}
	a.enterBranchMode()
	_ = a.db.SetKV("cloudHead", v.Branch)
	for _, r := range v.Branch.Recent {
		a.putCloudMeta(r)
	}
	if v.Branch.Head == nil {
		return
	}
	a.putCloudMeta(v.Branch.Head.CommitRef)
	id := v.Branch.Head.CommitID
	a.goLoop("cloud-head", func(c context.Context) {
		cc, err := a.ensureCloudCommit(c, id)
		if err != nil {
			a.log.Warn("branch", "cloud head fetch", "commitId", id, "error", err.Error())
			return
		}
		a.stageCloudCommit(cc)
	})
}

func (a *Agent) cloudHeadRef() *model.CloudHeadRef {
	var b model.CloudBranchView
	if ok, _ := a.db.GetKV("cloudHead", &b); ok {
		return b.Head
	}
	return nil
}

func (a *Agent) cloudRecent() []model.CommitRef {
	var b model.CloudBranchView
	if ok, _ := a.db.GetKV("cloudHead", &b); ok && b.Recent != nil {
		return b.Recent
	}
	return a.cloudCommitMetas(model.CloudRecentCommits)
}

// commitRevision is the engine revision of a commit (revId = commitId).
func commitRevision(c *model.Commit, actor string) *model.Revision {
	cat := emptyCatalog()
	if c.Catalog != nil {
		cc := *c.Catalog
		cat = &cc
	}
	normalizeCatalog(cat)
	ch, _ := canonical.HashCanonical(cat)
	kind := "change"
	var rb *model.RollbackRef
	if c.Kind == "rollback" && c.RollbackOf != nil {
		kind = "rollback"
		rb = &model.RollbackRef{SnapshotID: c.RollbackOf.SnapshotID, Seq: c.Seq - 1}
	}
	return &model.Revision{RevID: c.CommitID, Seq: c.Seq, ParentRevID: c.ParentID, Origin: "agent", Actor: actor, CreatedAt: c.CreatedAt, Config: c.Config, Hash: c.Hash,
		Catalog: *cat, CatalogHash: ch, Summary: trim200(branch.Summary(c.Message)), Kind: kind, RollbackOf: rb}
}

// stageCloudCommit stages a cloud commit for this site (images, files, docker load, dry run) so a merge applies fast.
// The revision is stored under the cloud commitId; it is a staging object only (branchRev: no proposal reports).
func (a *Agent) stageCloudCommit(c *model.Commit) {
	rev := commitRevision(c, c.Author.Email)
	if existing, ok := a.revision(rev.RevID); !ok {
		if err := a.putRevision(rev); err != nil {
			a.log.Warn("branch", "stage cloud head", "error", err.Error())
			return
		}
	} else {
		rev = existing
	}
	a.markBranchRev(rev.RevID)
	if row, ok := a.staging(rev.RevID, a.site); ok && (row.Staging == model.StagingReady || row.Staging == model.StagingDownloading) {
		return
	}
	a.startStaging(rev.RevID)
}

func (a *Agent) markBranchRev(id string) {
	a.mu.Lock()
	if a.branchRevs == nil {
		a.branchRevs = map[string]bool{}
	}
	a.branchRevs[id] = true
	a.mu.Unlock()
}

func (a *Agent) isBranchRev(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.branchRevs[id]
}

// ───────────────────────── push to the cloud mirror

func (a *Agent) enqueueCommitPush(c *model.Commit) {
	if !a.branchMode() {
		return
	}
	a.enqueue("branch-push", "branch:"+c.CommitID, "POST", "/v1/agent/branch/commits", model.PushAgentBranchRequest{Commits: []model.Commit{*c}})
}

// pushAllCommits queues every commit not pushed yet (entering branch mode; oldest first, FIFO outbox).
func (a *Agent) pushAllCommits() {
	rows, err := a.db.Query(`SELECT ` + commitCols + ` FROM commits ORDER BY seq ASC`)
	if err != nil {
		return
	}
	var list []*model.Commit
	for rows.Next() {
		if c, err := scanCommit(rows); err == nil {
			if c.Branch.Kind == "" {
				c.Branch = a.ownBranch() // the commits table holds only this site's branch (no branch column)
			}
			list = append(list, c)
		}
	}
	rows.Close()
	for _, c := range list {
		c.Branch = a.ownBranch()
		a.enqueueCommitPush(c)
	}
}

// branchReport is HeartbeatRequest.branch / PeerPing.branch.
func (a *Agent) branchReport() *model.AgentBranchReport {
	if !a.branchMode() {
		return nil
	}
	r := &model.AgentBranchReport{MergedFrom: a.mergedFrom(), Uncommitted: a.uncommittedCounts()}
	if c := a.headCommit(); c != nil {
		ref := c.Ref()
		r.Head = &ref
	}
	return r
}

// ───────────────────────── §21 #105: no working copy on the agent (GUI edits live in the browser)
//
// kv `workingCopy` / table `working_items` were the 0.14.0 saved GUI edits. 0.14.1 never writes them except
// convertLocalProposals (pre-0.14 local proposals entering branch mode): the start-up migration (working_migrate.go)
// auto-commits them through the apply engine or drops them to a backup file, once.

type workingCopy struct {
	BaseCommitID *string      `json:"baseCommitId"`
	Config       model.Config `json:"config"`
	Keys         []string     `json:"keys"`
	UpdatedAt    string       `json:"updatedAt"`
	UpdatedBy    string       `json:"updatedBy"`
}

// legacyWorking: the 0.14.0 saved edits (nil = none).
func (a *Agent) legacyWorking() *workingCopy {
	var w workingCopy
	if ok, _ := a.db.GetKV("workingCopy", &w); ok && len(w.Keys) > 0 {
		return &w
	}
	return nil
}

func (a *Agent) headRaw() (*model.Config, *model.RevisionCatalog) {
	if r := a.headRevision(); r != nil {
		return &r.Config, &r.Catalog
	}
	c := validate.EmptyConfig()
	return &c, emptyCatalog()
}

// saveLegacyWorking stores edits for the start-up migration (only for pre-0.14 local proposals).
func (a *Agent) saveLegacyWorking(cfg model.Config, by string) {
	head, _ := a.headRaw()
	var keys []string
	for _, it := range branch.Uncommitted(*head, cfg, branch.Options{SecretValues: true, Scope: a.site}) {
		keys = append(keys, it.Key)
	}
	if len(keys) == 0 {
		return
	}
	var base *string
	if h := a.head(); h != nil {
		id := h.RevID
		base = &id
	}
	_ = a.db.SetKV("workingCopy", workingCopy{BaseCommitID: base, Config: cfg, Keys: keys, UpdatedAt: nowTs(), UpdatedBy: by})
}

// clearLegacyWorking drops kv workingCopy + working_items (the migration's single clean-up).
func (a *Agent) clearLegacyWorking() {
	_ = a.db.DelKV("workingCopy")
	_, _ = a.db.Exec(`DELETE FROM working_items`)
}

// ───────────────────────── uncommitted = drift ⊕ edits

type uncommittedView struct {
	combined model.Config
	items    []model.DriftItem
	notes    []model.DriftNote
	report   *driftScan
	content  map[string]bool // keys of persist-volume content items (never blocking, §21 #117)
}

func (a *Agent) uncommittedNow() *uncommittedView {
	head, _ := a.headRaw()
	opt := branch.Options{SecretValues: true, Scope: a.site}
	u := &uncommittedView{combined: *head, items: []model.DriftItem{}, notes: []model.DriftNote{}, content: map[string]bool{}}
	ds := a.lastDrift()
	u.report = ds
	cur := any(*head)
	origin := map[string]model.DriftItem{}
	if ds != nil && ds.HeadCommitID != nil && a.head() != nil && *ds.HeadCommitID == a.head().RevID {
		var ch []model.MergeChoice
		for _, it := range ds.Items {
			origin[it.Key] = it
			ch = append(ch, model.MergeChoice{Key: it.Key, Take: "source"})
		}
		if len(ch) > 0 {
			r := branch.Apply(nil, ds.LiveConfig, *head, ch, opt)
			cur = r.Config
		}
		accepted := a.acceptedFingerprints()
		for _, n := range ds.Notes {
			if !accepted[n.Fingerprint] {
				u.notes = append(u.notes, n)
			}
		}
	}
	cfg, err := branch.ToConfig(cur)
	if err == nil {
		u.combined = cfg
	}
	now := nowTs()
	for _, it := range branch.Uncommitted(*head, u.combined, opt) {
		di := model.DriftItem{MergeItem: it, Origin: "docker", Detail: "changed on the server", ObservedAt: now}
		if o, ok := origin[it.Key]; ok {
			di.Origin, di.Detail, di.ObservedAt = o.Origin, o.Detail, o.ObservedAt
		}
		u.items = append(u.items, di)
		if a.isPersistContentKey(it.Key) {
			u.content[it.Key] = true
		}
	}
	return u
}

func (a *Agent) uncommittedCounts() model.AgentBranchUncommitted {
	u := a.uncommittedNow()
	out := model.AgentBranchUncommitted{Notes: len(u.notes)}
	out.Drift = len(u.items) // §21 #105: Working is always 0 (GUI edits live in the browser)
	if u.report != nil {
		s := u.report.ScannedAt
		out.ScannedAt = &s
	}
	return out
}

// blocking: uncommitted items / notes that refuse a merge (the content of a persist volume never blocks: a merge takes
// the server's side as "ours", §21 #117 d).
func (u *uncommittedView) blocking() int {
	n := len(u.notes)
	for _, it := range u.items {
		if !u.content[it.Key] {
			n++
		}
	}
	return n
}

func (a *Agent) acceptedFingerprints() map[string]bool {
	out := map[string]bool{}
	rows, err := a.db.Query(`SELECT fingerprint FROM drift_accepts`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var f string
		if rows.Scan(&f) == nil {
			out[f] = true
		}
	}
	return out
}

// driftErr: 409 conflict while changes made outside the agent are uncommitted (§21 #105 J12).
func driftErr(u *uncommittedView) *httpError {
	drift := 0
	for _, it := range u.items {
		if !u.content[it.Key] {
			drift++
		}
	}
	return herr(model.ErrConflict, fmt.Sprintf("%d changes made on the server outside the agent are not committed — commit or discard them first", u.blocking()),
		map[string]any{"reason": model.ReasonUncommittedDrift, "drift": drift, "notes": len(u.notes), "uncommitted": u.blocking()})
}

// staleHead: 409 stale when the browser / plan head is not this site's head (§21 #105 J11).
func (a *Agent) staleHead(msg string) *httpError {
	var head any
	if c := a.headCommit(); c != nil {
		head = model.CommitRef{CommitID: c.CommitID, Seq: c.Seq, Hash: c.Hash, Message: c.Message, Author: c.Author, CreatedAt: c.CreatedAt, Kind: c.Kind}
	}
	return herr(model.ErrStale, msg, map[string]any{"reason": model.ReasonHeadMoved, "head": head})
}

// ───────────────────────── views

func (a *Agent) commitDetail(c *model.Commit, parentCfg *model.Config) model.CommitDetail {
	var p any
	if parentCfg != nil {
		p = *parentCfg
	}
	changes := branch.Uncommitted(p, c.Config, branch.Options{SecretValues: true})
	return model.CommitDetail{Commit: commitView(c), Changes: changes}
}

func commitView(c *model.Commit) model.CommitView {
	return model.CommitView{CommitID: c.CommitID, Branch: c.Branch, Seq: c.Seq, ParentID: c.ParentID, Kind: c.Kind, Message: c.Message, Author: c.Author, CreatedAt: c.CreatedAt,
		Source: c.Source, RevertOf: c.RevertOf, ResetTo: c.ResetTo, RollbackOf: c.RollbackOf, Config: maskConfig(&c.Config), Hash: c.Hash, Catalog: c.Catalog, CatalogHash: c.CatalogHash, Stats: c.Stats, Data: c.Data}
}

// aheadBehind: behind = cloud commits newer than the merged one; ahead = local commits after the last merge from the
// cloud; differs = scope items between the cloud head and this head.
func (a *Agent) aheadBehind() (ahead, behind, differs int) {
	mf := a.mergedFrom()
	ch := a.cloudHeadRef()
	if ch != nil {
		if mf == nil {
			behind = ch.Seq
		} else if ch.Seq > mf.Seq {
			behind = ch.Seq - mf.Seq
		}
	}
	if m, ok := a.mergeBase(); ok && m.TargetCommitID != nil {
		if base, ok := a.commitByID(*m.TargetCommitID); ok {
			_ = a.db.QueryRow(`SELECT COUNT(*) FROM commits WHERE seq > ? AND kind NOT IN ('migrated')`, base.Seq).Scan(&ahead)
		}
	}
	if ch != nil {
		if cc, ok := a.cloudCommit(ch.CommitID); ok {
			head, _ := a.headRaw()
			differs = len(branch.Uncommitted(*head, cc.Config, branch.Options{SecretValues: true, Scope: a.site}))
		}
	}
	return
}

func (a *Agent) currentBranchRun() *model.UpdateRun {
	rows, err := a.db.Query(`SELECT updateId FROM updates WHERE json_extract(plan, '$.kind') = 'merge' ORDER BY updateId DESC LIMIT 1`)
	if err != nil {
		return nil
	}
	var id string
	if rows.Next() {
		_ = rows.Scan(&id)
	}
	rows.Close()
	if id == "" {
		return nil
	}
	if r, ok := a.loadRun(id); ok && (r.FinishedAt == nil || time.Since(parseTs(*r.FinishedAt)) < 10*time.Minute) {
		return r
	}
	return nil
}

func (a *Agent) handleBranchState(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	st := model.AgentBranchState{SiteSlug: a.site, Cloud: model.AgentBranchCloud{Head: a.cloudHeadRef(), Recent: a.cloudRecent(), Staged: map[string]model.SiteStaging{}},
		Local: model.AgentBranchLocal{Commits: []model.CommitSummary{}}, Uncommitted: a.uncommittedCounts(), Sites: []model.AgentBranchSite{}}
	if st.Cloud.Head != nil {
		if row, ok := a.staging(st.Cloud.Head.CommitID, a.site); ok {
			st.Cloud.Staged[a.site] = row.SiteStaging
		}
	}
	if c := a.headCommit(); c != nil {
		ref := c.Ref()
		st.Local.Head = &ref
	}
	st.Local.Commits, _ = a.commitPage(0, 10)
	st.Local.MergedFrom = a.mergedFrom()
	st.Local.Ahead, st.Local.Behind, st.Local.Differs = a.aheadBehind()
	st.Run = a.currentBranchRun()
	st.Sites = a.branchSites(r.Context())
	return st, 200, nil
}

// selfBranchSite is this site's entry of AgentBranchState.sites (+ the peer branch route).
func (a *Agent) selfBranchSite() model.AgentBranchSite {
	rep := a.branchReport()
	v := a.version
	s := model.AgentBranchSite{SiteSlug: a.site, Reachable: true, AgentVersion: &v}
	if rep != nil {
		s.Head, s.MergedFrom = rep.Head, rep.MergedFrom
		u := rep.Uncommitted
		s.Uncommitted = &u
	}
	ahead, behind, _ := a.aheadBehind()
	s.StrictlyBehind = behind > 0 && ahead == 0
	reason := ""
	switch {
	case s.Uncommitted != nil && s.Uncommitted.Working+s.Uncommitted.Drift+s.Uncommitted.Notes > 0:
		reason = "changes made on the server are not committed — commit or discard them on this site"
	case ahead > 0:
		reason = "has local commits since its last merge — merge it on its own Branch page"
	case behind == 0:
		reason = "up to date"
	}
	s.CanFastForward = reason == ""
	if reason != "" {
		s.Reason = &reason
	}
	return s
}

func (a *Agent) branchSites(ctx context.Context) []model.AgentBranchSite {
	out := []model.AgentBranchSite{a.selfBranchSite()}
	for _, slug := range a.enrolledSites() {
		if slug == a.site {
			continue
		}
		s := model.AgentBranchSite{SiteSlug: slug}
		var pb model.PeerBranchResponse
		if contains(a.reachablePeers(), slug) {
			if err := a.peerJSON(ctx, slug, "GET", "/_opm/peer/branch", nil, &pb, 5*time.Second); err == nil {
				s.Reachable = true
				v := pb.AgentVersion
				s.AgentVersion = &v
				s.Head, s.MergedFrom, s.StrictlyBehind, s.CanFastForward, s.Reason = pb.Head, pb.MergedFrom, pb.StrictlyBehind, pb.CanFastForward, pb.Reason
				u := pb.Uncommitted
				s.Uncommitted = &u
			} else {
				msg := "needs agent ≥ " + model.BranchesMinAgent + " (or did not answer)"
				s.Reachable = true
				s.Reason = &msg
			}
		} else {
			msg := "unreachable"
			s.Reason = &msg
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SiteSlug < out[j].SiteSlug })
	return out
}

func (a *Agent) handleBranchCommits(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	before, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	items, next := a.commitPage(before, qInt(r, "limit", 50))
	return model.PageResponse[model.CommitSummary]{Items: items, Cursor: next}, 200, nil
}

func (a *Agent) handleBranchCommit(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	c, ok := a.commitByID(r.PathValue("commitId"))
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "commit not found", nil)
	}
	c.Branch = a.ownBranch()
	var pc *model.Config
	if c.ParentID != nil {
		if p, ok := a.commitByID(*c.ParentID); ok {
			pc = &p.Config
		}
	}
	return a.commitDetail(c, pc), 200, nil
}

func (a *Agent) handleCloudCommit(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	c, err := a.ensureCloudCommit(r.Context(), r.PathValue("commitId"))
	if err != nil {
		return nil, 0, herr(model.ErrUnavailable, "cloud commit not available: "+err.Error(), nil)
	}
	var pc *model.Config
	if c.ParentID != nil {
		if p, ok := a.cloudCommit(*c.ParentID); ok {
			pc = &p.Config
		}
	}
	return a.commitDetail(c, pc), 200, nil
}

func (a *Agent) handleUncommitted(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	u := a.uncommittedNow()
	out := model.UncommittedResponse{Items: u.items, Notes: u.notes}
	if u.report != nil {
		s := u.report.ScannedAt
		out.ScannedAt = &s
	}
	if h := a.head(); h != nil {
		id := h.RevID
		out.HeadCommitID = &id
	}
	return out, 200, nil
}

// handlePutWorking: REMOVED §21 #105 — GUI edits live in the browser until Commit (410 gone).
func (a *Agent) handlePutWorking(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return nil, 0, herr(model.ErrGone, "the agent keeps no working copy since 0.14.1: edits stay in the browser until Commit", map[string]any{"hint": "POST /_opm/api/branch/commit { message, baseCommitId, config }"})
}

// ───────────────────────── merge machinery

func mergeCatalogs(primary, secondary *model.RevisionCatalog) model.RevisionCatalog {
	out := model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	for _, c := range []*model.RevisionCatalog{secondary, primary} {
		if c == nil {
			continue
		}
		for k, v := range c.Services {
			out.Services[k] = v
		}
		for k, v := range c.Images {
			out.Images[k] = v
		}
		for k, v := range c.NotFoundPages {
			out.NotFoundPages[k] = v
		}
	}
	return out
}

// pruneCatalog keeps the entries the config uses (services + images of containers, referenced 404 pages).
func pruneCatalog(cfg *model.Config, cat model.RevisionCatalog) model.RevisionCatalog {
	out := model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	for _, s := range cfg.Sites {
		for _, c := range s.Config.Containers {
			if v, ok := cat.Services[c.ServiceSlug]; ok {
				out.Services[c.ServiceSlug] = v
			}
			k := c.ServiceSlug + ":" + c.Version
			if v, ok := cat.Images[k]; ok {
				out.Images[k] = v
			}
		}
		if s.Config.NotFoundPageID != nil {
			if v, ok := cat.NotFoundPages[*s.Config.NotFoundPageID]; ok {
				out.NotFoundPages[*s.Config.NotFoundPageID] = v
			}
		}
	}
	return out
}

// newBranchRevision builds the next revision of this branch (parent = head) for a config.
func (a *Agent) newBranchRevision(cfg model.Config, cat model.RevisionCatalog, actor, message string, kind string) *model.Revision {
	normalizeConfig(&cfg)
	normalizeCatalog(&cat)
	hash, _ := canonical.HashCanonical(cfg)
	ch, _ := canonical.HashCanonical(cat)
	seq := 1
	var parent *string
	if h := a.head(); h != nil {
		seq = h.Seq + 1
		p := h.RevID
		parent = &p
	}
	if kind == "" {
		kind = "change"
	}
	return &model.Revision{RevID: newID(), Seq: seq, ParentRevID: parent, Origin: "agent", Actor: actor, CreatedAt: nowTs(), Config: cfg, Hash: hash, Catalog: cat, CatalogHash: ch, Summary: trim200(branch.Summary(message)), Kind: kind}
}

func (a *Agent) author(actor string) model.CommitAuthor {
	s := a.site
	if actor == "" {
		return model.CommitAuthor{Kind: "system", Email: "system", SiteSlug: &s}
	}
	return model.CommitAuthor{Kind: "person", Email: actor, SiteSlug: &s}
}

// commitMetaFor builds the commit meta of a revision of this branch.
func (a *Agent) commitMetaFor(rev *model.Revision, kind, message, actor string, src *model.CommitSource, data model.CommitData) model.CommitMeta {
	var pc any
	if rev.ParentRevID != nil {
		if p, ok := a.revision(*rev.ParentRevID); ok {
			pc = p.Config
		}
	}
	ch := rev.CatalogHash
	return model.CommitMeta{CommitID: rev.RevID, Branch: a.ownBranch(), Seq: rev.Seq, ParentID: rev.ParentRevID, Kind: kind, Message: strings.TrimSpace(message), Author: a.author(actor),
		CreatedAt: rev.CreatedAt, Source: src, Hash: rev.Hash, CatalogHash: &ch, Stats: branch.Stats(pc, rev.Config, branch.Options{SecretValues: true, Scope: a.site}), Data: data}
}

// mergeRun holds the side effects a COMMIT applies on this site (merge base, working keys, accepted drift).
type mergeRun struct {
	ClearKeys []string `json:"clearKeys,omitempty"` // 0.14.0 (working copy keys); unused since §21 #105
	// LegacyWorking: this run auto-commits the 0.14.0 saved edits (§21 #105 J13, working_migrate.go)
	LegacyWorking bool              `json:"legacyWorking,omitempty"`
	Accept        []model.DriftNote `json:"accept"`
	Skipped       []model.MergeSkip `json:"skipped"`
	SourceCommit  *string           `json:"sourceCommit"`
	Adopt         []string          `json:"adopt"`
	Actor         string            `json:"actor"`
}

func (a *Agent) saveMergeRun(id string, m mergeRun) {
	_, _ = a.db.Exec(`INSERT OR REPLACE INTO merge_runs(runId, plan, state, startedAt) VALUES(?,?, 'running', ?)`, id, store.JSON(m), nowTs())
}

func (a *Agent) loadMergeRun(id string) (*mergeRun, bool) {
	var raw string
	if err := a.db.QueryRow(`SELECT plan FROM merge_runs WHERE runId = ?`, id).Scan(&raw); err != nil {
		return nil, false
	}
	var m mergeRun
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil, false
	}
	return &m, true
}

// commitBranchAt runs inside commitSite (COMMIT step): the commit row + merge base + working copy + push + activity.
func (a *Agent) commitBranchAt(plan *model.UpdatePlan, rev *model.Revision, meta model.CommitMeta) {
	cat := rev.Catalog
	c := &model.Commit{CommitID: meta.CommitID, Branch: a.ownBranch(), Seq: rev.Seq, ParentID: rev.ParentRevID, Kind: meta.Kind, Message: meta.Message, Author: meta.Author, CreatedAt: meta.CreatedAt,
		Source: meta.Source, RevertOf: meta.RevertOf, ResetTo: meta.ResetTo, RollbackOf: meta.RollbackOf, Config: rev.Config, Hash: rev.Hash, Catalog: &cat, CatalogHash: &rev.CatalogHash, Stats: meta.Stats, Data: meta.Data}
	id := plan.UpdateID
	c.Data.UpdateID = &id
	if err := a.putCommit(c); err != nil {
		a.log.Error("branch", "commit row", "error", err.Error())
		return
	}
	mr, _ := a.loadMergeRun(plan.UpdateID)
	actor := ""
	if mr != nil {
		actor = mr.Actor
	}
	if c.Source != nil && c.Source.Branch.Kind == "deployment" && c.Source.CommitID != nil {
		if src, ok := a.cloudCommit(*c.Source.CommitID); ok {
			var skipped []model.MergeSkip
			if mr != nil {
				skipped = mr.Skipped
			}
			seq := src.Seq
			a.setMergeBase(&mergeBaseRow{SourceCommitID: &src.CommitID, SourceSeq: &seq, TargetCommitID: &c.CommitID, Base: branch.MarkSecretValues(src.Config), Skipped: skipped, UpdatedBy: actorOr(actor)})
		}
	}
	if mr != nil {
		if mr.LegacyWorking {
			a.legacyWorkingDone(c.CommitID)
		}
		for _, n := range mr.Accept {
			_, _ = a.db.Exec(`INSERT OR REPLACE INTO drift_accepts(fingerprint, commitId, acceptedAt, acceptedBy) VALUES(?,?,?,?)`, n.Fingerprint, c.CommitID, nowTs(), actorOr(actor))
		}
		a.adopt(mr.Adopt)
		_, _ = a.db.Exec(`UPDATE merge_runs SET state = 'committed', finishedAt = ? WHERE runId = ?`, nowTs(), plan.UpdateID)
	}
	a.branchActivity(c, actor)
	a.enqueueCommitPush(c)
	a.kickLoop("drift")
}

func actorOr(s string) string {
	if s == "" {
		return "system"
	}
	return s
}

// branchActivity writes the activity of a new commit (§21 #104 §11).
func (a *Agent) branchActivity(c *model.Commit, actor string) {
	typ := map[string]string{"merge": "branch.merged", "cherry-pick": "branch.cherry-picked", "revert": "branch.reverted", "reset": "branch.reset", "drift": "drift.committed", "rollback": "branch.committed"}[c.Kind]
	if typ == "" {
		typ = "branch.committed"
	}
	data := map[string]any{"branch": a.ownBranch().Key(), "commitId": c.CommitID, "seq": c.Seq, "message": trim200(branch.Summary(c.Message)), "keys": firstN(c.Stats.Keys, 50)}
	picks, skipped := 0, 0
	if c.Source != nil {
		data["source"] = c.Source.Branch.Key()
		data["sourceCommitId"] = c.Source.CommitID
		if l, ok := c.Source.Picks.([]string); ok {
			picks = len(l)
		} else if l, ok := c.Source.Picks.([]any); ok {
			picks = len(l)
		} else {
			picks = -1 // all
		}
		skipped = c.Source.Skipped
	}
	data["picks"], data["skipped"] = picks, skipped
	if c.Data.SecretValues.Any() {
		data["secretValues"] = c.Data.SecretValues // slugs only (§21 #105 J7)
	}
	if c.Data.AutoCommitted != nil {
		data["autoCommitted"] = c.Data.AutoCommitted
	}
	var ap *string
	origin := "system"
	if actor != "" {
		ap = &actor
		origin = "agent"
	}
	a.activity(ap, origin, typ, trim200(fmt.Sprintf("seq %d %s: %s", c.Seq, c.Kind, branch.Summary(c.Message))), data)
	if c.Kind == "drift" || len(c.Data.AcceptedDrift) > 0 {
		fps := []string{}
		for _, d := range c.Data.AcceptedDrift {
			fps = append(fps, d.Fingerprint)
		}
		if len(fps) > 0 {
			a.activity(ap, origin, "drift.accepted", fmt.Sprintf("%d server changes accepted", len(fps)), map[string]any{"fingerprints": fps, "commitId": c.CommitID})
		}
	}
}

func firstN(l []string, n int) []string {
	if len(l) > n {
		return l[:n]
	}
	if l == nil {
		return []string{}
	}
	return l
}

// mergeFailedHere: the apply of a merge plan failed on this site and was rolled back — no commit (§21 #104 D7).
func (a *Agent) mergeFailedHere(plan *model.UpdatePlan) {
	_, _ = a.db.Exec(`UPDATE merge_runs SET state = 'failed', finishedAt = ? WHERE runId = ?`, nowTs(), plan.UpdateID)
	msg := ""
	if st := a.siteStateOf(plan.UpdateID); st != nil && st.Error != nil {
		msg = *st.Error
	}
	a.activity(nil, "system", "branch.merge-failed", trim200("merge failed and was rolled back on "+a.site+": "+msg), map[string]any{"updateId": plan.UpdateID, "siteSlug": a.site, "commitId": plan.RevisionFor(a.site).RevID, "error": msg})
	if mr, ok := a.loadMergeRun(plan.UpdateID); ok && mr != nil && mr.LegacyWorking {
		a.legacyWorkingFailed("the apply failed and was rolled back: " + msg)
	}
}

// startBranchApply runs a single- or multi-site merge plan with this agent as coordinator (no intent wait, D8).
func (a *Agent) startBranchApply(targets map[string]model.UpdatePlanTarget, commits map[string]model.CommitMeta, mr *mergeRun, actor string) (*model.UpdateRun, *httpError) {
	return a.startBranchPlan("merge", nil, targets, commits, mr, actor, nil)
}

// startBranchPlan: kind merge (merges, commits with edits, discards, reverts, resets, fast-forward) or rollback
// (snapshot restore with per-site targets, branch mode).
func (a *Agent) startBranchPlan(kind string, targetSnapshot *string, targets map[string]model.UpdatePlanTarget, commits map[string]model.CommitMeta, mr *mergeRun, actor string, ff *model.FastForwardUse) (*model.UpdateRun, *httpError) {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	if why := a.anyVolumeOp(); why != "" { // §21 #96: a volume restore / copy in flight would be corrupted
		return nil, herr(model.ErrConflict, why+"; start when it has finished (or stop that action)", nil)
	}
	// §21 #110 never refused: a running update is stopped first (no roll back; this one converges the sites)
	a.supersedeRunning(a.ctx, "a new "+kind+" by "+actor, &actor)
	sites := make([]string, 0, len(targets))
	for s := range targets {
		sites = append(sites, s)
	}
	sort.Strings(sites)
	order := append([]string{}, sites...)
	self := targets[a.site]
	if _, ok := targets[a.site]; !ok {
		self = targets[sites[0]]
	}
	now := nowTs()
	plan := &model.UpdatePlan{UpdateID: newID(), Kind: kind, Revision: self.Revision, FromRevID: self.FromRevID, PreSnapshotID: newID(), TargetSnapshotID: targetSnapshot, Sites: sites, Order: order,
		Coordinator: a.site, ApprovedBy: actor, ApprovedAt: now, Targets: targets, Commits: commits}
	if mr != nil {
		a.saveMergeRun(plan.UpdateID, *mr)
	}
	run := &model.UpdateRun{UpdateID: plan.UpdateID, Kind: kind, FastForward: ff, RevID: self.Revision.RevID, Coordinator: a.site, ApprovedBy: actor, ApprovedAt: now, Phase: model.StepLock, Sites: map[string]model.UpdateSiteState{}, StartedAt: now}
	for _, s := range sites {
		run.Sites[s] = model.UpdateSiteState{UpdateID: plan.UpdateID, SiteSlug: s, State: "idle", Containers: []model.UpdateContainerState{}}
	}
	a.savePlan(plan)
	a.saveRun(run)
	a.log.Info("branch", "merge apply started", "updateId", plan.UpdateID, "sites", sites)
	a.goLoop("coordinate", func(c context.Context) {
		c, done := a.updateCtx(plan.UpdateID, c)
		defer done()
		a.coordinate(c, plan, run, nil, false)
		if r, ok := a.loadRun(plan.UpdateID); ok && r.Result != nil && !r.Result.Ok {
			if st, ok := a.loadMergeRun(plan.UpdateID); ok && st != nil {
				_, _ = a.db.Exec(`UPDATE merge_runs SET state = 'failed', finishedAt = ?, error = ? WHERE runId = ? AND state = 'running'`, nowTs(), r.Result.Message, plan.UpdateID)
			}
		}
	})
	cp := *run
	return &cp, nil
}

// stageRevisionNow stages a merge result on this site and waits (validate, blobs, docker load, files, dry run).
func (a *Agent) stageRevisionNow(ctx context.Context, rev *model.Revision) *httpError {
	a.markBranchRev(rev.RevID)
	if err := a.putRevision(rev); err != nil {
		return invalid(err.Error(), nil)
	}
	// not cancelled by the request: a first staging can pull images for many minutes, and a browser / proxy / client
	// timeout (undici: 300 s) used to cancel the downloads, so the merge never started. The handler now always finishes;
	// the GUI follows the run in the branch state.
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
	defer cancel()
	a.stage(c, rev.RevID)
	row, ok := a.staging(rev.RevID, a.site)
	if !ok || row.Staging != model.StagingReady {
		msg := "staging did not finish"
		if ok && row.Error != nil {
			msg = *row.Error
		}
		var issues []model.ValidationIssue
		if d := a.localStagingDetail(rev.RevID); d != nil && len(d.Issues) > 0 {
			issues = d.Issues
		}
		if issues == nil {
			issues = []model.ValidationIssue{{Path: "", Code: "V_STAGING", Message: msg, Severity: "error"}}
		}
		return invalid("the result cannot run on "+a.site+": "+msg, issues)
	}
	return nil
}

func checkMsg(msg string) *httpError {
	if e := branch.CheckMessage(msg); e != "" {
		return invalid(e, []model.ValidationIssue{{Path: "message", Code: "V_COMMIT_MESSAGE", Message: e, Severity: "error"}})
	}
	return nil
}

type mergeComputed struct {
	src      *model.Commit
	result   model.Config
	catalog  model.RevisionCatalog
	apply    branch.ApplyResult
	plan     []model.MergeItem
	base     *mergeBaseRow
	updated  []string
	baseKind string
	live     map[string]string // §21 #117 live tree hash per persist volume (the apply refuses when it changed)
}

// computeMerge applies choices of a cloud commit onto this head (scope: shared + this site; other sites = references).
func (a *Agent) computeMerge(ctx context.Context, sourceCommitID string, choices []model.MergeChoice, values map[string]model.VariableValue) (*mergeComputed, *httpError) {
	src, err := a.ensureCloudCommit(ctx, sourceCommitID)
	if err != nil {
		return nil, herr(model.ErrNotFound, "cloud commit "+sourceCommitID+" is not available on this site: "+err.Error(), nil)
	}
	head, headCat := a.headRaw()
	// §21 #117 "ours" of a persist volume = its live tree: server changes are kept, both-changed files are conflicts
	head, live := a.liveContentConfig(head)
	mc := &mergeComputed{src: src, baseKind: "none", live: live}
	opt := branch.Options{SecretValues: true, Scope: a.site}
	var base any
	if m, ok := a.mergeBase(); ok {
		mc.base, base, opt.Skipped, mc.baseKind = m, m.Base, m.Skipped, "record"
	}
	if values != nil {
		opt.Values = map[string]any{}
		for k, v := range values {
			opt.Values[k] = v
		}
	}
	mc.plan = branch.Plan(base, src.Config, *head, opt)
	mc.apply = branch.Apply(base, src.Config, *head, choices, opt)
	tree, updated := branch.WithReferenceSites(mc.apply.Config, src.Config, a.site)
	mc.updated = updated
	cfg, err := branch.ToConfig(tree)
	if err != nil {
		return nil, invalid("result: "+err.Error(), nil)
	}
	mc.result = cfg
	mc.catalog = pruneCatalog(&cfg, mergeCatalogs(src.Catalog, headCat))
	return mc, nil
}

func (a *Agent) mergePlanOf(mc *mergeComputed, diffOnly bool) model.MergePlan {
	u := a.uncommittedNow()
	srcRef := mc.src.Ref()
	p := model.MergePlan{Source: model.MergePlanSource{Point: model.BranchPoint{Branch: model.BranchRef{Kind: "deployment", DeploymentID: a.dep}, At: map[string]any{"commitId": mc.src.CommitID}}, Label: fmt.Sprintf("cloud@%d", mc.src.Seq), Commit: &srcRef},
		Target: model.MergePlanTarget{Branch: a.ownBranch(), Label: a.site, WorkingClean: u.blocking() == 0, Uncommitted: u.blocking()},
		Base:   model.MergePlanBase{Kind: mc.baseKind, Label: "no common history"}, Items: mc.plan, Counts: branch.Count(mc.plan), FastForward: branch.IsFastForward(mc.plan),
		References: &model.MergeReferences{UpdatedSites: mc.updated}, DiffOnly: diffOnly}
	if mc.base != nil {
		p.Base.SourceCommitID = mc.base.SourceCommitID
		p.Base.Label = "last merged cloud commit"
		if mc.base.SourceSeq != nil {
			p.Base.Label = fmt.Sprintf("cloud@%d (last merge)", *mc.base.SourceSeq)
		}
	}
	if c := a.headCommit(); c != nil {
		r := c.Ref()
		p.Target.Head = &r
	}
	if p.References.UpdatedSites == nil {
		p.References.UpdatedSites = []string{}
	}
	return p
}

func (a *Agent) handleCompareLocal(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req model.LocalCompareRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	srcID := ""
	switch s := req.Source.(type) {
	case string:
		if ch := a.cloudHeadRef(); ch != nil {
			srcID = ch.CommitID
		}
	case map[string]any:
		if v, ok := s["cloudCommitId"].(string); ok {
			srcID = v
		}
		if v, ok := s["localCommitId"].(string); ok {
			// a local commit compared with the head (read-only)
			c, ok := a.commitByID(v)
			if !ok {
				return nil, 0, herr(model.ErrNotFound, "commit not found", nil)
			}
			head, _ := a.headRaw()
			items := branch.Uncommitted(*head, c.Config, branch.Options{SecretValues: true, Scope: a.site})
			ref := c.Ref()
			p := model.MergePlan{Source: model.MergePlanSource{Point: model.BranchPoint{Branch: a.ownBranch(), At: map[string]any{"commitId": v}}, Label: fmt.Sprintf("%s@%d", a.site, c.Seq), Commit: &ref},
				Target: model.MergePlanTarget{Branch: a.ownBranch(), Label: a.site, WorkingClean: true}, Base: model.MergePlanBase{Kind: "none", Label: "two-way"},
				Items: items, Counts: branch.Count(items), DiffOnly: true}
			return p, 200, nil
		}
	}
	if srcID == "" {
		return nil, 0, herr(model.ErrNotFound, "no cloud commit yet", nil)
	}
	mc, e := a.computeMerge(r.Context(), srcID, nil, nil)
	if e != nil {
		return nil, 0, e
	}
	return a.mergePlanOf(mc, req.DiffOnly), 200, nil
}

func (a *Agent) previewOf(ctx context.Context, mc *mergeComputed, withStaging bool) model.MergePreview {
	head, _ := a.headRaw()
	cat := mc.catalog
	pv := model.MergePreview{Result: maskConfig(&mc.result), Changes: diff.Configs(head, &mc.result, nil), Validation: model.PreviewValidation{Errors: []model.ValidationIssue{}, Warnings: []model.ValidationIssue{}},
		Unresolved: mc.apply.Unresolved, Missing: branch.Missing(mc.result), ChoiceErrors: mc.apply.Errors, Kind: mc.apply.Kind, Taken: len(mc.apply.Taken), Skipped: len(mc.apply.NotTaken), Warnings: []string{}}
	if len(mc.apply.Taken) == 0 && len(mc.updated) == 0 {
		// nothing picked: say so (validating the unchanged / empty result only gives misleading errors such as
		// "a deployment needs at least one site" before the first merge)
		pv.Validation.Errors = append(pv.Validation.Errors, model.ValidationIssue{Path: "choices", Code: "V_NO_CHANGE", Message: "nothing picked: pick at least one change to merge", Severity: "error"})
	} else {
		is := validate.ValidateConfig(&mc.result, a.validationContext(&cat))
		for _, x := range is.List {
			if x.Severity == "error" {
				pv.Validation.Errors = append(pv.Validation.Errors, x)
			} else if x.Severity == "warning" {
				pv.Validation.Warnings = append(pv.Validation.Warnings, x)
			}
		}
	}
	if pv.Changes == nil {
		pv.Changes = []model.DiffChange{}
	}
	pv.Runtime = a.runtimeActions(ctx, &mc.result, &cat)
	if len(mc.updated) > 0 {
		pv.Warnings = append(pv.Warnings, "references to other sites updated from the cloud commit: "+strings.Join(mc.updated, ", "))
	}
	return pv
}

// runtimeActions: what the apply engine would do per container of this site (keep / recreate / create / remove).
func (a *Agent) runtimeActions(ctx context.Context, cfg *model.Config, cat *model.RevisionCatalog) []model.RuntimeAction {
	out := []model.RuntimeAction{}
	site := siteOf(cfg, a.site)
	ex, err := a.drv.Existing(ctx)
	if err != nil {
		return out
	}
	hash, _ := canonical.HashCanonical(cfg)
	rev := &model.Revision{RevID: "preview", Config: *cfg, Catalog: *cat, Hash: hash}
	seen := map[string]bool{}
	if site != nil {
		// render the files for real, like the apply does: a deferred render has no content hash, so every container that
		// mounts a rendered file would show "recreate" although the apply keeps it. Deferred only when a file blob is not on
		// the site yet (those containers then show "recreate": the content cannot be compared before staging).
		builts, err := a.buildSiteOpts(rev, site, false)
		if err != nil {
			builts, err = a.buildSiteOpts(rev, site, true)
		}
		if err == nil {
			for _, b := range builts {
				seen[b.ContainerSlug] = true
				act := "create"
				if e, ok := ex[b.ContainerSlug]; ok {
					act = "recreate"
					if driver.Keeps(b, e) {
						act = "keep"
					}
				}
				if !b.Enabled {
					act = "stop"
				}
				out = append(out, model.RuntimeAction{ContainerSlug: b.ContainerSlug, Action: act})
			}
		}
	}
	for slug := range ex {
		if !seen[slug] {
			out = append(out, model.RuntimeAction{ContainerSlug: slug, Action: "remove"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContainerSlug < out[j].ContainerSlug })
	return out
}

func (a *Agent) handlePreviewLocalMerge(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req model.LocalMergeRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	mc, e := a.computeMerge(r.Context(), req.SourceCommitID, req.Choices, req.Values)
	if e != nil {
		return nil, 0, e
	}
	return a.previewOf(r.Context(), mc, false), 200, nil
}

func (a *Agent) handleMergeLocal(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.LocalMergeRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if e := checkMsg(req.Message); e != nil {
		return nil, 0, e
	}
	if h := a.head(); (h == nil) != (req.TargetHead == nil) || (h != nil && req.TargetHead != nil && h.RevID != *req.TargetHead) {
		return nil, 0, a.staleHead("this site's head moved; compare again")
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	a.rescanDriftNow(r.Context())
	if u := a.uncommittedNow(); u.blocking() > 0 {
		return nil, 0, driftErr(u)
	}
	mc, e := a.computeMerge(r.Context(), req.SourceCommitID, req.Choices, req.Values)
	if e != nil {
		return nil, 0, e
	}
	if len(mc.apply.Unresolved) > 0 {
		return nil, 0, herr(model.ErrInvalid, "decide every conflict first", map[string]any{"code": "V_MERGE_UNRESOLVED", "unresolved": mc.apply.Unresolved})
	}
	if len(mc.apply.Errors) > 0 {
		return nil, 0, herr(model.ErrInvalid, mc.apply.Errors[0].Message, map[string]any{"code": "V_MERGE_CHOICE", "choiceErrors": mc.apply.Errors})
	}
	if len(mc.apply.Taken) == 0 && len(mc.updated) == 0 {
		return nil, 0, invalid("nothing picked", []model.ValidationIssue{{Path: "choices", Code: "V_NO_CHANGE", Message: "nothing picked: pick at least one change to merge", Severity: "error"}})
	}
	rev := a.newBranchRevision(mc.result, mc.catalog, rc.actor, req.Message, "change")
	a.setContentApply(rev.RevID, contentApply{Live: mc.live})
	if e := a.stageRevisionNow(r.Context(), rev); e != nil {
		return nil, 0, e
	}
	picks, trunc := branch.SourcePicks(mc.apply.Taken, mc.apply.Kind)
	seq := mc.src.Seq
	src := &model.CommitSource{Branch: model.BranchRef{Kind: "deployment", DeploymentID: a.dep}, CommitID: &mc.src.CommitID, Seq: &seq, Label: "cloud", Picks: picks, Skipped: len(mc.apply.NotTaken), Truncated: trunc}
	meta := a.commitMetaFor(rev, mc.apply.Kind, req.Message, rc.actor, src, model.CommitData{})
	var prev []model.MergeSkip
	if mc.base != nil {
		prev = mc.base.Skipped
	}
	mr := &mergeRun{Skipped: branch.NextSkips(mc.apply.NotTaken, prev, &seq, nowTs(), rc.actor), SourceCommit: &mc.src.CommitID, Actor: rc.actor}
	run, he := a.startBranchApply(map[string]model.UpdatePlanTarget{a.site: {Revision: *rev, FromRevID: rev.ParentRevID}}, map[string]model.CommitMeta{a.site: meta}, mr, rc.actor)
	if he != nil {
		return nil, 0, he
	}
	return run, 202, nil
}

// handleFastForwardAll: one coordinated update moving every selected clean, strictly-behind site to a cloud commit.
func (a *Agent) handleFastForwardAll(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.FastForwardAllRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if e := checkMsg(req.Message); e != nil {
		return nil, 0, e
	}
	if len(req.Sites) == 0 {
		return nil, 0, invalid("choose at least one site", nil)
	}
	src, err := a.ensureCloudCommit(r.Context(), req.SourceCommitID)
	if err != nil {
		return nil, 0, herr(model.ErrNotFound, "cloud commit not available: "+err.Error(), nil)
	}
	sites := map[string]model.AgentBranchSite{}
	for _, s := range a.branchSites(r.Context()) {
		sites[s.SiteSlug] = s
	}
	targets := map[string]model.UpdatePlanTarget{}
	commits := map[string]model.CommitMeta{}
	cat := model.RevisionCatalog{}
	if src.Catalog != nil {
		cat = *src.Catalog
	}
	normalizeCatalog(&cat)
	ch, _ := canonical.HashCanonical(cat)
	seq := src.Seq
	for _, slug := range req.Sites {
		s, ok := sites[slug]
		if !ok {
			return nil, 0, invalid("unknown site "+slug, nil)
		}
		if !s.CanFastForward {
			why := "cannot fast-forward"
			if s.Reason != nil {
				why = *s.Reason
			}
			return nil, 0, herr(model.ErrConflict, slug+": "+why, map[string]any{"siteSlug": slug, "reason": why})
		}
		var parent *string
		pseq := 0
		if s.Head != nil {
			p := s.Head.CommitID
			parent, pseq = &p, s.Head.Seq
		}
		rev := model.Revision{RevID: newID(), Seq: pseq + 1, ParentRevID: parent, Origin: "agent", Actor: rc.actor, CreatedAt: nowTs(), Config: src.Config, Hash: src.Hash, Catalog: cat, CatalogHash: ch, Summary: trim200(branch.Summary(req.Message)), Kind: "change"}
		if slug == a.site {
			if e := a.contentDriftConflict(&src.Config); e != nil { // §21 #117 d
				return nil, 0, e
			}
		}
		targets[slug] = model.UpdatePlanTarget{Revision: rev, FromRevID: parent}
		stats := model.CommitStats{Keys: []string{}}
		if slug == a.site {
			head, _ := a.headRaw()
			stats = branch.Stats(*head, src.Config, branch.Options{SecretValues: true, Scope: slug})
		}
		sitec := slug
		commits[slug] = model.CommitMeta{CommitID: rev.RevID, Branch: model.BranchRef{Kind: "agent", DeploymentID: a.dep, SiteSlug: slug}, Seq: rev.Seq, ParentID: parent, Kind: "merge", Message: strings.TrimSpace(req.Message),
			Author: model.CommitAuthor{Kind: "person", Email: rc.actor, SiteSlug: &sitec}, CreatedAt: rev.CreatedAt, Hash: rev.Hash, CatalogHash: &ch, Stats: stats,
			Source: &model.CommitSource{Branch: model.BranchRef{Kind: "deployment", DeploymentID: a.dep}, CommitID: &src.CommitID, Seq: &seq, Label: "cloud", Picks: "all"}}
	}
	if t, ok := targets[a.site]; ok {
		rv := t.Revision
		if e := a.stageRevisionNow(r.Context(), &rv); e != nil {
			return nil, 0, e
		}
	}
	run, he := a.startBranchApply(targets, commits, &mergeRun{SourceCommit: &src.CommitID, Actor: rc.actor}, rc.actor)
	if he != nil {
		return nil, 0, he
	}
	return run, 202, nil
}

// ───────────────────────── local commit / discard / revert / reset

func (a *Agent) handleCommitLocal(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.LocalCommitRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	// §21 #107: the stage of this commit attempt is removed whatever the outcome (promoted blobs moved out first)
	defer a.dropStage(req.StageID)
	if e := checkMsg(req.Message); e != nil {
		return nil, 0, e
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	if req.Config != nil {
		if len(req.Keys)+len(req.Accept)+len(req.TakeBytes) > 0 {
			return nil, 0, invalid("commit either GUI edits (config) or server changes (keys / accept / takeBytes), not both", []model.ValidationIssue{{Path: "config", Code: "V_COMMIT_MODE", Message: "config cannot be combined with keys, accept or takeBytes", Severity: "error"}})
		}
		return a.commitEdits(r, rc, &req)
	}
	hr := a.headRevision()
	if hr == nil {
		return nil, 0, herr(model.ErrStale, "this site has no head yet; merge the cloud commit first", nil)
	}
	u := a.uncommittedNow()
	want := map[string]bool{}
	for _, k := range req.Keys {
		want[k] = true
	}
	all := len(req.Keys) == 0
	var choices []model.MergeChoice
	var editKeys, driftKeys []string
	var adopt []string
	for _, it := range u.items {
		if !all && !want[it.Key] {
			continue
		}
		choices = append(choices, model.MergeChoice{Key: it.Key, Take: "source"})
		driftKeys = append(driftKeys, it.Key)
	}
	if u.report != nil {
		for _, k := range driftKeys {
			adopt = append(adopt, u.report.Adopt[k]...)
		}
	}
	notes := map[string]model.DriftNote{}
	for _, n := range u.notes {
		notes[n.Fingerprint] = n
	}
	var accepted []model.DriftNote
	for _, fp := range req.Accept {
		n, ok := notes[fp]
		if !ok {
			return nil, 0, invalid("unknown drift note "+fp, nil)
		}
		accepted = append(accepted, n)
	}
	cfg := hr.Config
	cat := hr.Catalog
	// take-bytes: an edited rendered file becomes the new File content (render none)
	for _, fp := range req.TakeBytes {
		if u.report == nil || u.report.TakeBytes[fp] == nil {
			return nil, 0, invalid("no edited bytes for "+fp, nil)
		}
		tb := u.report.TakeBytes[fp]
		for i := range cfg.Files {
			if cfg.Files[i].FileSlug == tb.FileSlug {
				cfg.Files[i].Content = &model.BlobRef{Sha256: "sha256:" + tb.Sha, SizeBytes: tb.Size}
				cfg.Files[i].Render = model.Some("none")
			}
		}
		if n, ok := notes[fp]; ok {
			accepted = append(accepted, n)
		}
		editKeys = append(editKeys, "files/"+tb.FileSlug)
	}
	if len(choices) == 0 && len(accepted) == 0 && len(req.TakeBytes) == 0 {
		return nil, 0, invalid("nothing to commit", []model.ValidationIssue{{Path: "keys", Code: "V_NO_CHANGE", Message: "nothing uncommitted", Severity: "error"}})
	}
	if len(choices) > 0 {
		res := branch.Apply(nil, u.combined, cfg, choices, branch.Options{SecretValues: true, Scope: a.site})
		c2, err := branch.ToConfig(res.Config)
		if err != nil {
			return nil, 0, invalid(err.Error(), nil)
		}
		cfg = c2
	}
	if u.report != nil && len(u.report.LocalCatalog.Images) > 0 {
		cat = mergeCatalogs(&cat, &u.report.LocalCatalog)
	}
	cat = pruneCatalog(&cfg, mergeCatalogs(&cat, &hr.Catalog))
	data := model.CommitData{}
	for _, n := range accepted {
		data.AcceptedDrift = append(data.AcceptedDrift, model.AcceptedDrift{Fingerprint: n.Fingerprint, Label: n.Label, Detail: n.Detail})
	}
	kind := "drift"
	if len(editKeys) > 0 {
		kind = "change"
	}
	rev := a.newBranchRevision(cfg, cat, rc.actor, req.Message, "change")
	mr := &mergeRun{Accept: accepted, Adopt: adopt, Actor: rc.actor}
	if len(editKeys) == 0 {
		// drift (+ accepted notes / content): nothing to apply — the server already runs it
		if err := a.putRevision(rev); err != nil {
			return nil, 0, herr(model.ErrInternal, err.Error(), nil)
		}
		meta := a.commitMetaFor(rev, kind, req.Message, rc.actor, nil, data)
		plan := &model.UpdatePlan{UpdateID: newID(), Kind: "merge", Revision: *rev, Commits: map[string]model.CommitMeta{a.site: meta}}
		a.saveMergeRun(plan.UpdateID, *mr)
		if err := a.db.SetKV("head", model.HeadHash{RevID: rev.RevID, Seq: rev.Seq, Hash: rev.Hash}); err != nil {
			return nil, 0, herr(model.ErrInternal, err.Error(), nil)
		}
		a.commitBranchAt(plan, rev, meta)
		a.activity(rc.actorPtr(), "agent", "drift.committed", fmt.Sprintf("%d server changes committed (seq %d)", len(driftKeys), rev.Seq), actorData(rc, map[string]any{"commitId": rev.RevID, "keys": firstN(driftKeys, 50)}))
		c, _ := a.commitByID(rev.RevID)
		a.kickLoop("ping")
		return model.LocalCommitResponse{Commit: c}, 201, nil
	}
	if e := a.stageRevisionNow(r.Context(), rev); e != nil {
		return nil, 0, e
	}
	meta := a.commitMetaFor(rev, kind, req.Message, rc.actor, nil, data)
	run, he := a.startBranchApply(map[string]model.UpdatePlanTarget{a.site: {Revision: *rev, FromRevID: rev.ParentRevID}}, map[string]model.CommitMeta{a.site: meta}, mr, rc.actor)
	if he != nil {
		return nil, 0, he
	}
	return model.LocalCommitResponse{Run: run}, 202, nil
}

func (a *Agent) handleDiscardLocal(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.LocalDiscardRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	u := a.uncommittedNow()
	// §21 #105: only drift (changes made outside the agent); GUI edits are discarded in the browser
	var drift []string
	byKey := map[string]model.DriftItem{}
	for _, it := range u.items {
		byKey[it.Key] = it
	}
	for _, k := range req.Keys {
		if _, ok := byKey[k]; !ok {
			return nil, 0, invalid("no uncommitted change "+k, nil)
		}
		drift = append(drift, k)
	}
	if len(drift) == 0 && len(req.Notes) == 0 {
		return model.RunResponse{}, 200, nil
	}
	// re-apply the head: remove hand-made containers of these items, then the engine converges the site
	if u.report != nil {
		for _, k := range drift {
			for _, id := range u.report.Remove[k] {
				_ = a.drv.StopRemove(r.Context(), id, 10)
			}
		}
		for _, fp := range req.Notes {
			for _, id := range u.report.Remove[fp] {
				_ = a.drv.StopRemove(r.Context(), id, 10)
			}
		}
	}
	// §21 #117 discarded content changes: the committed content is written back over them
	if hr := a.headRevision(); hr != nil {
		force := map[string]bool{}
		for _, k := range drift {
			if a.isPersistContentKey(k) {
				rest := strings.TrimPrefix(k, "sites/"+a.site+"/volumes/")
				force[rest[:strings.Index(rest, "/content")]] = true
			}
		}
		if len(force) > 0 {
			a.setContentApply(hr.RevID, contentApply{Force: force})
		}
	}
	run, he := a.reapplyHead(rc.actor)
	if he != nil {
		return nil, 0, he
	}
	a.activity(rc.actorPtr(), "agent", "drift.discarded", fmt.Sprintf("%d server changes discarded (re-applied)", len(drift)+len(req.Notes)), actorData(rc, map[string]any{"keys": firstN(append(drift, req.Notes...), 50), "updateId": run.UpdateID}))
	return model.RunResponse{Run: run}, 202, nil
}

// reapplyHead runs the head revision again on this site (no new commit: COMMIT is idempotent for the same head).
func (a *Agent) reapplyHead(actor string) (*model.UpdateRun, *httpError) {
	hr := a.headRevision()
	if hr == nil {
		return nil, herr(model.ErrStale, "no head", nil)
	}
	a.markBranchRev(hr.RevID)
	// the per-site file copies are re-staged so APPLY can swap them in
	c, cancel := context.WithTimeout(a.ctx, 15*time.Minute)
	a.stage(c, hr.RevID)
	cancel()
	return a.startBranchApply(map[string]model.UpdatePlanTarget{a.site: {Revision: *hr, FromRevID: &hr.RevID}}, nil, nil, actor)
}

func (a *Agent) handleRevertLocal(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return a.revertOrReset(w, r, false)
}

func (a *Agent) handleResetLocal(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return a.revertOrReset(w, r, true)
}

func (a *Agent) revertOrReset(w http.ResponseWriter, r *http.Request, reset bool) (any, int, *httpError) {
	rc := getRC(r)
	var req model.LocalRevertRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if e := checkMsg(req.Message); e != nil {
		return nil, 0, e
	}
	h := a.head()
	if h == nil || req.TargetHead == nil || *req.TargetHead != h.RevID {
		return nil, 0, a.staleHead("this site's head moved; reload")
	}
	if u := a.uncommittedNow(); u.blocking() > 0 {
		return nil, 0, driftErr(u)
	}
	c, ok := a.commitByID(req.CommitID)
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "commit not found", nil)
	}
	head, headCat := a.headRaw()
	opt := branch.Options{SecretValues: true, Scope: a.site}
	var cfg model.Config
	var catSrc *model.RevisionCatalog = c.Catalog
	meta := model.CommitMeta{}
	kind := "revert"
	if reset {
		kind = "reset"
		cfg = c.Config
		if cfg.Sites == nil {
			return nil, 0, invalid("empty commit", nil)
		}
	} else {
		if c.ParentID == nil {
			return nil, 0, invalid("the first commit cannot be reverted (nothing before it)", nil)
		}
		p, ok := a.commitByID(*c.ParentID)
		if !ok {
			return nil, 0, herr(model.ErrNotFound, "parent commit not found", nil)
		}
		res := branch.ApplyRevert(c.Config, p.Config, *head, req.Choices, opt)
		if len(res.Unresolved) > 0 {
			return nil, 0, herr(model.ErrInvalid, "decide every conflict first", map[string]any{"code": "V_MERGE_UNRESOLVED", "unresolved": res.Unresolved})
		}
		c2, err := branch.ToConfig(res.Config)
		if err != nil {
			return nil, 0, invalid(err.Error(), nil)
		}
		cfg = c2
		catSrc = p.Catalog
	}
	cat := pruneCatalog(&cfg, mergeCatalogs(catSrc, headCat))
	if e := a.contentDriftConflict(&cfg); e != nil { // §21 #117 d
		return nil, 0, e
	}
	rev := a.newBranchRevision(cfg, cat, rc.actor, req.Message, "change")
	if rev.Hash == h.Hash {
		return nil, 0, invalid("nothing changes", []model.ValidationIssue{{Path: "", Code: "V_NO_CHANGE", Message: "the result equals the head", Severity: "error"}})
	}
	if e := a.stageRevisionNow(r.Context(), rev); e != nil {
		return nil, 0, e
	}
	meta = a.commitMetaFor(rev, kind, req.Message, rc.actor, nil, model.CommitData{})
	ref := &model.CommitSeqRef{CommitID: c.CommitID, Seq: c.Seq}
	if reset {
		meta.ResetTo = ref
	} else {
		meta.RevertOf = ref
	}
	run, he := a.startBranchApply(map[string]model.UpdatePlanTarget{a.site: {Revision: *rev, FromRevID: rev.ParentRevID}}, map[string]model.CommitMeta{a.site: meta}, &mergeRun{Actor: rc.actor}, rc.actor)
	if he != nil {
		return nil, 0, he
	}
	return run, 202, nil
}

// ───────────────────────── legacy (§21 #104 §9.3)

func (a *Agent) markLegacyParticipant(updateID string) {
	_ = a.db.SetKV("legacy:"+updateID, true)
}

func (a *Agent) isLegacyParticipant(updateID string) bool {
	var on bool
	ok, _ := a.db.GetKV("legacy:"+updateID, &on)
	return ok && on
}

// legacyParticipantStep: a legacy (proposal) update of an old coordinator never touches a 0.14 site's containers;
// the site only takes the lock so the old protocol keeps its guarantees, then records the legacy head.
func (a *Agent) legacyParticipantStep(plan *model.UpdatePlan, step string) error {
	switch step {
	case model.StepLock:
		now := nowTs()
		return a.db.SetKV("lock", model.UpdateLock{UpdateID: plan.UpdateID, Kind: plan.Kind, RevID: plan.Revision.RevID, Coordinator: plan.Coordinator, StartedAt: now, LastContactAt: now})
	case model.StepCommit:
		_ = a.db.SetKV("legacyHead", model.HeadHash{RevID: plan.Revision.RevID, Seq: plan.Revision.Seq, Hash: plan.Revision.Hash})
		fallthrough
	case model.StepRollback, model.StepUnlock:
		if l := a.lock(); l != nil && l.UpdateID == plan.UpdateID {
			_ = a.db.DelKV("lock")
		}
		a.finishSiteUpdate(plan.UpdateID)
		_ = a.db.DelKV("legacy:" + plan.UpdateID)
	}
	a.log.Info("branch", "legacy update: no-op participant", "updateId", plan.UpdateID, "step", step)
	return nil
}

// legacyGone answers the proposal endpoints that branches replaced (410 gone, hint: the Branch page).
func (a *Agent) legacyGone(fn apiFn) apiFn {
	return func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
		if a.branchMode() {
			return nil, 0, herr(model.ErrGone, "proposals were replaced by branches (agent ≥ 0.14): use the Branch page", map[string]any{"hint": "Branch page"})
		}
		return fn(w, r)
	}
}

// ───────────────────────── registration

// branchRoutes: every branch endpoint, path relative to /_opm/api. site = also served per site (registerSiteBranchAPI):
// /_opm/api/sites/{site}/branch/… on any site runs the same handler on that site (self → local, other → relayed signed
// to its /_opm/peer/site/branch/…, the person forwarded as actor). Commit (GUI working copy), working and fast-forward
// (coordinated across sites) stay on the agent the person is logged in to only.
func (a *Agent) branchRoutes() []struct {
	pattern string
	kind    authKind
	fn      apiFn
	site    bool
} {
	return []struct {
		pattern string
		kind    authKind
		fn      apiFn
		site    bool
	}{
		{"GET /branch", authSession, a.handleBranchState, true},
		// §21 #113 what happens with changes made in Docker outside OPM (ask | auto-commit, autocommit.go)
		{"GET /branch/drift-policy", authSession, a.handleGetDriftPolicy, true},
		{"PUT /branch/drift-policy", authActor, a.handlePutDriftPolicy, true},
		{"GET /branch/commits", authSession, a.handleBranchCommits, true},
		{"GET /branch/commits/{commitId}", authSession, a.handleBranchCommit, true},
		{"GET /branch/cloud-commits/{commitId}", authSession, a.handleCloudCommit, true},
		{"GET /branch/uncommitted", authSession, a.handleUncommitted, true},
		{"PUT /branch/working", authActor, a.handlePutWorking, false},
		{"POST /branch/drift/rescan", authActor, a.handleRescanDrift, true},
		{"POST /branch/commit", authActor, a.handleCommitLocal, false},
		// the same commit, per site: only the changes made on that server (drift keys / notes), never browser edits
		{"POST /branch/commit-drift", authActor, a.handleSiteCommitDrift, true},
		{"POST /branch/discard", authActor, a.handleDiscardLocal, true},
		{"POST /branch/compare", authSession, a.handleCompareLocal, true},
		{"POST /branch/merge/preview", authSession, a.handlePreviewLocalMerge, true},
		{"POST /branch/merge", authActor, a.handleMergeLocal, true},
		{"POST /branch/fast-forward", authActor, a.handleFastForwardAll, false},
		{"POST /branch/revert", authActor, a.handleRevertLocal, true},
		{"GET /branch/blobs/{sha256}", authSession, a.handleBranchBlob, true},
		{"POST /branch/reset", authActor, a.handleResetLocal, true},
		// the run of a per-site merge / revert / reset (UpdateProgress of the Branch page of that site)
		{"GET /updates/current", authSession, a.handleCurrentUpdate, true},
		{"GET /updates/{updateId}", authSession, a.handleGetUpdate, true},
		{"POST /updates/{updateId}/abandon", authActor, a.handleAbandon, true},
	}
}

func (a *Agent) registerBranchAPI(h func(pattern string, kind authKind, fn apiFn)) {
	for _, rt := range a.branchRoutes() {
		if strings.HasPrefix(rt.pattern[strings.Index(rt.pattern, " ")+1:], "/branch") {
			m, p, _ := strings.Cut(rt.pattern, " ")
			h(m+" /_opm/api"+p, rt.kind, rt.fn)
		}
	}
}

// handleSiteCommitDrift: POST …/sites/{site}/branch/commit-drift — handleCommitLocal for the drift mode only. Browser
// edits (config / stageId) are committed on the agent the person is logged in to (their staged blobs live there).
func (a *Agent) handleSiteCommitDrift(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var raw []byte
	if rc.body != nil || rc.bodyFile != "" {
		rd, err := rc.reader()
		if err != nil {
			return nil, 0, herr(model.ErrInternal, err.Error(), nil)
		}
		raw, err = io.ReadAll(io.LimitReader(rd, 10<<20))
		rd.Close()
		if err != nil {
			return nil, 0, herr(model.ErrInvalid, err.Error(), nil)
		}
	} else {
		b, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
		if err != nil {
			return nil, 0, herr(model.ErrInvalid, err.Error(), nil)
		}
		raw = b
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	var peek struct {
		Config  json.RawMessage `json:"config"`
		StageID string          `json:"stageId"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return nil, 0, herr(model.ErrInvalid, "body: "+err.Error(), nil)
	}
	if (len(peek.Config) > 0 && string(peek.Config) != "null") || peek.StageID != "" {
		return nil, 0, herr(model.ErrInvalid, "a per-site commit records only the changes made on that server (keys / accept / takeBytes); commit browser edits on the agent you are logged in to", nil)
	}
	return a.handleCommitLocal(w, r)
}

// registerSiteBranchAPI: the per-site branch (§21 #111) under /_opm/api/sites/{site}/… and /_opm/peer/site/….
func (a *Agent) registerSiteBranchAPI(h func(pattern string, kind authKind, fn apiFn)) {
	for _, rt := range a.branchRoutes() {
		if rt.site {
			h(rt.pattern, rt.kind, rt.fn)
		}
	}
}

func (a *Agent) peerCloudCommit(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	c, ok := a.cloudCommit(r.PathValue("commitId"))
	if !ok {
		return nil, herr(model.ErrNotFound, "cloud commit not cached here", nil)
	}
	if caller := getRC(r).peer; caller != nil && len(c.Config.Secrets) > 0 {
		slugs := []string{}
		for _, s := range c.Config.Secrets {
			slugs = append(slugs, s.SecretSlug)
		}
		a.activity(nil, "system", "secret.fetched", "cloud commit with secrets sent to site "+caller.Site, map[string]any{"siteSlug": caller.Site, "secretSlugs": slugs, "commitId": c.CommitID})
	}
	return model.AgentCommitResponse{Commit: *c}, nil
}

func (a *Agent) peerBranch(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	s := a.selfBranchSite()
	out := model.PeerBranchResponse{SiteSlug: a.site, AgentVersion: a.version, StrictlyBehind: s.StrictlyBehind, CanFastForward: s.CanFastForward, Reason: s.Reason}
	if rep := a.branchReport(); rep != nil {
		out.AgentBranchReport = *rep
	}
	return out, nil
}

// legacyHead is the head old (< 0.14) peers see in the ping: the last legacy revision of this site (§21 #104 §9.3).
func (a *Agent) legacyHead() *model.HeadHash {
	var h model.HeadHash
	if ok, _ := a.db.GetKV("legacyHead", &h); ok && h.RevID != "" {
		return &h
	}
	return nil
}

// siteHeadOf is a site's branch head (self: kv head; peers: their ping's branch head, else its head).
func (a *Agent) siteHeadOf(slug string) *model.HeadHash {
	if slug == a.site {
		return a.head()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if st := a.peerState[slug]; st != nil && st.ping != nil {
		if st.ping.Branch != nil && st.ping.Branch.Head != nil {
			h := st.ping.Branch.Head
			return &model.HeadHash{RevID: h.CommitID, Seq: h.Seq, Hash: h.Hash}
		}
		return st.ping.Head
	}
	return nil
}

// branchRollback (branch mode): a snapshot rollback is one coordinated update with a per-site target (each site's own
// snapshot config on top of its own head) and a commit of kind rollback on every site (message required).
func (a *Agent) branchRollback(ctx context.Context, snapshotID string, rev *model.Revision, sum *model.SnapshotSummary, message string, rc *reqCtx) (*model.UpdateRun, *httpError) {
	doc := a.mergedSnapshotDoc(ctx, snapshotID)
	enrolled := a.enrolledSites()
	targets := map[string]model.UpdatePlanTarget{}
	commits := map[string]model.CommitMeta{}
	for _, s := range siteSlugs(&rev.Config) {
		if !contains(enrolled, s) && s != a.site {
			continue
		}
		cfg := rev.Config
		if part, ok := doc.Sites[s]; ok && (part.Config.Sites != nil) {
			cfg = part.Config
			normalizeConfig(&cfg)
		}
		hash, _ := canonical.HashCanonical(cfg)
		var parent *string
		seq := 1
		if h := a.siteHeadOf(s); h != nil {
			p := h.RevID
			parent, seq = &p, h.Seq+1
		}
		t := model.Revision{RevID: newID(), Seq: seq, ParentRevID: parent, Origin: "agent", Actor: rc.actor, CreatedAt: nowTs(), Config: cfg, Hash: hash, Catalog: rev.Catalog, CatalogHash: rev.CatalogHash,
			Summary: trim200(branch.Summary(message)), Kind: "rollback", RollbackOf: &model.RollbackRef{SnapshotID: snapshotID, Seq: sum.Seq}}
		targets[s] = model.UpdatePlanTarget{Revision: t, FromRevID: parent}
		site := s
		stats := model.CommitStats{Keys: []string{}}
		if s == a.site {
			head, _ := a.headRaw()
			stats = branch.Stats(*head, cfg, branch.Options{SecretValues: true, Scope: s})
		}
		ch := t.CatalogHash
		commits[s] = model.CommitMeta{CommitID: t.RevID, Branch: model.BranchRef{Kind: "agent", DeploymentID: a.dep, SiteSlug: s}, Seq: seq, ParentID: parent, Kind: "rollback", Message: strings.TrimSpace(message),
			Author: model.CommitAuthor{Kind: "person", Email: rc.actor, SiteSlug: &site}, CreatedAt: t.CreatedAt, RollbackOf: &model.SnapshotRefOnly{SnapshotID: snapshotID}, Hash: hash, CatalogHash: &ch, Stats: stats,
			Data: model.CommitData{FastForward: rc.ff}}
	}
	if len(targets) == 0 {
		return nil, invalid("no site to roll back", nil)
	}
	if t, ok := targets[a.site]; ok {
		if err := a.putRevision(&t.Revision); err != nil {
			return nil, herr(model.ErrInternal, err.Error(), nil)
		}
	}
	id := snapshotID
	return a.startBranchPlan("rollback", &id, targets, commits, &mergeRun{Actor: rc.actor}, rc.actor, rc.ff)
}

// branchBlobMax: getBranchBlob serves at most 8 MiB (text diffs of files / volume content).
const branchBlobMax = 8 << 20

var hexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// handleBranchBlob: file / volume-content bytes by sha256 — the local blob store, else a peer / the cloud (smart
// distribution: peer copy → fetcher → GET /v1/agent/blobs). ≤ 8 MiB (413 too-large), else not-found.
func (a *Agent) handleBranchBlob(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	h := strings.ToLower(strings.TrimPrefix(r.PathValue("sha256"), "sha256:"))
	if !hexRe.MatchString(h) {
		return nil, 0, herr(model.ErrNotFound, "not a sha256", nil)
	}
	if !a.haveBlob(h) {
		c, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		err := a.ensureBlob(c, blobNeed{Hex: h, Kind: "file", Ref: model.FetchRef{}}, func(string, string) {})
		cancel()
		if err != nil || !a.haveBlob(h) {
			return nil, 0, herr(model.ErrNotFound, "blob not on this site, a peer or the cloud", nil)
		}
	}
	p, _ := a.blobPath(h)
	st, err := os.Stat(p)
	if err != nil {
		return nil, 0, herr(model.ErrNotFound, "blob not found", nil)
	}
	if st.Size() > branchBlobMax {
		return nil, 0, herr(model.ErrTooLarge, fmt.Sprintf("the file is %d bytes (at most %d are shown)", st.Size(), branchBlobMax), map[string]any{"sizeBytes": st.Size(), "maxBytes": branchBlobMax})
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", `"sha256:`+h+`"`)
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, "", st.ModTime(), f)
	return nil, -1, nil
}
