package core

import (
	"errors"
	"sort"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

// COMMIT_INLINE_MAX is COMMIT_INLINE_MAX.
const COMMIT_INLINE_MAX = 850 * 1024

// Bcol is the bcol object of branches.ts.
var Bcol bcolT

type bcolT struct{}

func (bcolT) TemplateCommits(t string) *driver.CollectionRef {
	return cloud.Col.Templates().Doc(t).Collection("commits")
}
func (bcolT) DeploymentCommits(d string) *driver.CollectionRef {
	return cloud.Col.Deployments().Doc(d).Collection("commits")
}
func (bcolT) AgentBranches(d string) *driver.CollectionRef {
	return cloud.Col.Deployments().Doc(d).Collection("agentBranches")
}
func (bcolT) AgentCommits(d, s string) *driver.CollectionRef {
	return cloud.Col.Deployments().Doc(d).Collection("agentBranches").Doc(s).Collection("commits")
}
func (bcolT) MergeBases(b *doc.Map) *driver.CollectionRef {
	if b.Val("kind") == "template" {
		return cloud.Col.Templates().Doc(b.Str("templateId")).Collection("mergeBases")
	}
	return cloud.Col.Deployments().Doc(b.Str("deploymentId")).Collection("mergeBases")
}

// CommitsOf is commitsOf.
func CommitsOf(b *doc.Map) *driver.CollectionRef {
	switch b.Val("kind") {
	case "template":
		return Bcol.TemplateCommits(b.Str("templateId"))
	case "deployment":
		return Bcol.DeploymentCommits(b.Str("deploymentId"))
	}
	return Bcol.AgentCommits(b.Str("deploymentId"), b.Str("siteSlug"))
}

func commitBodyPath(hash string) string {
	return "commits/" + strings.TrimPrefix(hash, "sha256:") + ".json"
}

// EncodeCommit is encodeCommit.
func EncodeCommit(c *doc.Map, external bool) *doc.Map {
	out := c.Without("config", "catalog", "data").Set("branchKey", shared.BranchKey(c.Map("branch"))).Set("_data", doc.MarshalString(or(c, "data", doc.New())))
	if external {
		return out.Set("_configRef", commitBodyPath(c.Str("hash")))
	}
	out.Set("_config", doc.MarshalString(present(c, "config")))
	if c.Truthy("catalog") {
		return out.Set("_catalog", doc.MarshalString(c.Val("catalog")))
	}
	return out.Set("_catalog", nil)
}

// StoreCommitBody is storeCommitBody: writes the JSON of a big commit to Storage first; true when external.
func StoreCommitBody(c *doc.Map) (bool, error) {
	body := doc.MarshalString(doc.Obj("config", present(c, "config"), "catalog", present(c, "catalog")))
	if doc.JSLen(body) <= COMMIT_INLINE_MAX {
		return false, nil
	}
	return true, cloud.Blobs().Put(commitBodyPath(c.Str("hash")), []byte(body), driver.PutOptions{ContentType: "application/json"})
}

func parseOr(s string, def any) (any, error) {
	if s == "" {
		return def, nil
	}
	return doc.ParseString(s)
}

// DecodeCommit is decodeCommit; human: normalize the stored config.
func DecodeCommit(d *doc.Map, human bool) (*doc.Map, error) {
	var config *doc.Map
	var catalog any
	if ref := d.Str("_configRef"); ref != "" {
		buf, err := cloud.Blobs().Get(ref)
		if err != nil {
			return nil, err
		}
		if buf == nil {
			return nil, errors.New("commit body " + ref + " is not stored")
		}
		j, err := doc.ParseMap(buf)
		if err != nil {
			return nil, err
		}
		config, _ = j.Val("config").(*doc.Map)
		catalog = j.Val("catalog")
	} else {
		v, err := parseOr(d.Str("_config"), nil)
		if err != nil {
			return nil, err
		}
		if config, _ = v.(*doc.Map); v == nil {
			config = shared.EmptyConfig()
		}
		if catalog, err = parseOr(d.Str("_catalog"), nil); err != nil {
			return nil, err
		}
	}
	if human {
		config = NormalizeStored(config, nil)
	}
	data, err := parseOr(d.Str("_data"), doc.New())
	if err != nil {
		return nil, err
	}
	return d.Without("_config", "_catalog", "_configRef", "_data", "branchKey").Set("config", config).Set("catalog", catalog).Set("data", data), nil
}

// SummaryOfDoc is summaryOfDoc: CommitSummary straight from the stored doc.
func SummaryOfDoc(d *doc.Map) *doc.Map {
	return doc.Obj("commitId", present(d, "commitId"), "branch", present(d, "branch"), "seq", present(d, "seq"), "parentId", present(d, "parentId"), "kind", present(d, "kind"), "message", present(d, "message"), "author", present(d, "author"), "createdAt", present(d, "createdAt"), "source", or(d, "source", nil), "revertOf", or(d, "revertOf", nil), "resetTo", or(d, "resetTo", nil), "rollbackOf", or(d, "rollbackOf", nil), "hash", present(d, "hash"), "stats", present(d, "stats"))
}

func getIn(tx *driver.Tx, ref *driver.DocRef) (*driver.DocSnap, error) {
	if tx != nil {
		return tx.Get(ref)
	}
	return ref.Get()
}

// LoadCommit is loadCommit: nil when missing (tx may be nil).
func LoadCommit(b *doc.Map, commitID string, tx *driver.Tx, human bool) (*doc.Map, error) {
	s, err := getIn(tx, CommitsOf(b).Doc(commitID))
	if err != nil || !s.Exists() {
		return nil, err
	}
	return DecodeCommit(s.Data(), human)
}

// SiteRunningCommit is siteRunningCommit: {config, seq, deploymentSeq}, nil = unknown.
func SiteRunningCommit(deploymentID, siteSlug, headID string) (*doc.Map, error) {
	if headID == "" {
		return nil, nil
	}
	rev, err := cloud.Col.Revisions(deploymentID).Doc(headID).Get()
	if err != nil {
		return nil, err
	}
	if rev.Exists() {
		r := cloud.DecodeRevision(rev.Data())
		return doc.Obj("config", present(r, "config"), "seq", present(r, "seq"), "deploymentSeq", present(r, "seq")), nil
	}
	ac, err := LoadCommit(doc.Obj("kind", "agent", "deploymentId", deploymentID, "siteSlug", siteSlug), headID, nil, false)
	if err != nil {
		return nil, err
	}
	if ac != nil {
		var seq any
		src := ac.Map("source")
		if src.Map("branch").Val("kind") == "deployment" {
			seq = or(src, "seq", nil)
		}
		if seq == nil {
			d, err := ReadAgentBranch(deploymentID, siteSlug, nil)
			if err != nil {
				return nil, err
			}
			if d != nil && d.Map("head").Val("commitId") == headID {
				seq = or(d.Map("mergedFrom"), "seq", nil)
			}
		}
		return doc.Obj("config", present(ac, "config"), "seq", present(ac, "seq"), "deploymentSeq", seq), nil
	}
	dc, err := LoadCommit(doc.Obj("kind", "deployment", "deploymentId", deploymentID), headID, nil, false)
	if err != nil || dc == nil {
		return nil, err
	}
	return doc.Obj("config", present(dc, "config"), "seq", present(dc, "seq"), "deploymentSeq", present(dc, "seq")), nil
}

// DeploymentHeadConfig is deploymentHeadConfig: the branch head config, else the legacy head revision; nil = none.
func DeploymentHeadConfig(dep *doc.Map) (*doc.Map, error) {
	id := dep.Str("deploymentId")
	if h := dep.Map("branch").Map("head"); h != nil {
		cm, err := LoadCommit(doc.Obj("kind", "deployment", "deploymentId", id), h.Str("commitId"), nil, false)
		if err != nil {
			return nil, err
		}
		if cm != nil {
			return cm.Map("config"), nil
		}
	}
	if h := dep.Map("head"); h != nil {
		r, err := cloud.Col.Revisions(id).Doc(h.Str("revId")).Get()
		if err != nil {
			return nil, err
		}
		if r.Exists() {
			return cloud.DecodeRevision(r.Data()).Map("config"), nil
		}
	}
	return nil, nil
}

// WriteCommit is writeCommit.
func WriteCommit(tx *driver.Tx, c *doc.Map, external bool) {
	tx.Create(CommitsOf(c.Map("branch")).Doc(c.Str("commitId")), EncodeCommit(c, external))
}

// EncodeMergeBase is encodeMergeBase.
func EncodeMergeBase(m *doc.Map) *doc.Map {
	return m.Without("base", "skipped").Set("_base", doc.MarshalString(present(m, "base"))).Set("_skipped", doc.MarshalString(or(m, "skipped", []any{})))
}

// DecodeMergeBase is decodeMergeBase.
func DecodeMergeBase(d *doc.Map) (*doc.Map, error) {
	base := shared.EmptyConfig()
	if s := d.Str("_base"); s != "" {
		v, err := doc.ParseMap([]byte(s))
		if err != nil {
			return nil, err
		}
		base = NormalizeStored(v, nil)
	}
	skipped, err := parseOr(d.Str("_skipped"), []any{})
	if err != nil {
		return nil, err
	}
	return d.Without("_base", "_skipped").Set("base", base).Set("skipped", skipped), nil
}

// LoadMergeBase is loadMergeBase: nil when missing (tx may be nil).
func LoadMergeBase(target, source *doc.Map, tx *driver.Tx) (*doc.Map, error) {
	s, err := getIn(tx, Bcol.MergeBases(target).Doc(shared.BranchKeyDocId(shared.BranchKey(source))))
	if err != nil || !s.Exists() {
		return nil, err
	}
	return DecodeMergeBase(s.Data())
}

// SetMergeBase is setMergeBase.
func SetMergeBase(tx *driver.Tx, target, m *doc.Map) {
	tx.Set(Bcol.MergeBases(target).Doc(shared.BranchKeyDocId(m.Str("sourceKey"))), EncodeMergeBase(m))
}

// PersonAuthor is personAuthor.
func PersonAuthor(c *cloud.Ctx) *doc.Map {
	a := c.Account
	var email any = a.UID
	if a.Email != nil {
		email = *a.Email
	}
	return doc.Obj("kind", "person", "email", email, "uid", a.UID, "name", or(a.Doc, "name", nil), "siteSlug", nil)
}

// SYSTEM_AUTHOR is SYSTEM_AUTHOR (a fresh map).
func SYSTEM_AUTHOR() *doc.Map {
	return doc.Obj("kind", "system", "email", "system", "uid", nil, "name", nil, "siteSlug", nil)
}

// IsTemplateLike is isTemplateLike.
func IsTemplateLike(b *doc.Map) bool { return b.Val("kind") == "template" }

// SecretValuesFor is secretValuesFor: compare secret values (deployment / agent on both sides).
func SecretValuesFor(a, b *doc.Map) bool {
	return a.Val("kind") != "template" && b.Val("kind") != "template"
}

// ScopeFor is scopeFor: agent branches compare only the shared sections + their own site; string or nil.
func ScopeFor(a, b *doc.Map) any {
	if a.Val("kind") == "agent" {
		return present(a, "siteSlug")
	}
	if b.Val("kind") == "agent" {
		return present(b, "siteSlug")
	}
	return nil
}

// NextBranchState is nextBranchState (prev may be nil).
func NextBranchState(prev, head *doc.Map) *doc.Map {
	return doc.Obj("head", shared.CommitRef(head), "origin", or(prev, "origin", nil), "commitCount", prev.Num("commitCount")+1)
}

// StripUncommitted is stripUncommitted.
func StripUncommitted(b *doc.Map) *doc.Map { return b.Without("uncommitted") }

// CountUncommitted is countUncommitted (head may be nil).
func CountUncommitted(kind string, head, working *doc.Map) int {
	return shared.UncommittedCount(head, working, doc.Obj("secretValues", kind == "deployment"))
}

// NeedsCompat is needsCompat: compat is needed unless a site is enrolled and every enrolled site runs agent ≥ 0.14.
func NeedsCompat(deploymentID string, tx *driver.Tx) (bool, error) {
	q := cloud.Col.Agents(deploymentID).Select("agentVersion")
	var s *driver.QuerySnap
	var err error
	if tx != nil {
		s, err = tx.Query(q)
	} else {
		s, err = q.Get()
	}
	if err != nil {
		return false, err
	}
	if len(s.Docs) == 0 {
		return true, nil
	}
	for _, d := range s.Docs {
		if !shared.SupportsBranches(or(d.Data(), "agentVersion", nil)) {
			return true, nil
		}
	}
	return false, nil
}

// CompatPrep is CompatPrep.
type CompatPrep struct {
	Needed bool
	Open   []*doc.Map
}

// PrepCompat is prepCompat: reads for WriteCompat; call before any write in the transaction.
func PrepCompat(tx *driver.Tx, deploymentID string) (*CompatPrep, error) {
	needed, err := NeedsCompat(deploymentID, tx)
	if err != nil || !needed {
		return &CompatPrep{Needed: false}, err
	}
	s, err := tx.Query(cloud.Col.Proposals(deploymentID).Where("status", "in", OPEN_STATUSES))
	if err != nil {
		return nil, err
	}
	p := &CompatPrep{Needed: true}
	for _, d := range s.Docs {
		p.Open = append(p.Open, d.Data())
	}
	return p, nil
}

// WriteCompat is writeCompat: the old publish for agents < 0.14; nil (null) when skipped, else {revId, seq}.
func WriteCompat(tx *driver.Tx, dep, commit *doc.Map, prep *CompatPrep, log *CloudLog, blocked bool, actor string) *doc.Map {
	if !prep.Needed || blocked || !commit.Truthy("catalog") {
		return nil
	}
	for _, p := range prep.Open {
		if s := p.Val("status"); s == "approved" || s == "applying" {
			return nil
		}
	}
	now := doc.NowISO()
	head := dep.Map("head")
	seq := head.Num("seq") + 1
	parent := or(head, "revId", nil)
	config := commit.Map("config")
	summary := doc.JSSlice(shared.CommitSummary(commit.Str("message")), 200)
	revID := commit.Str("commitId")
	revision := doc.Obj("revId", revID, "seq", seq, "parentRevId", parent, "origin", "cloud", "actor", actor, "createdAt", now, "config", config, "hash", present(commit, "hash"), "catalog", present(commit, "catalog"), "catalogHash", present(commit, "catalogHash"), "summary", summary, "kind", "change", "rollbackOf", nil)
	var slugs []string
	seen := map[string]bool{}
	for _, x := range config.Arr("sites") {
		if s := x.(*doc.Map).Str("siteSlug"); !seen[s] {
			seen[s] = true
			slugs = append(slugs, s)
		}
	}
	sort.Strings(slugs)
	sites := doc.New()
	for _, s := range slugs {
		sites.Set(s, doc.Obj("staging", "pending", "progress", 0.0, "error", nil, "updatedAt", nil))
	}
	proposal := doc.Obj("revId", revID, "parentRevId", parent, "seq", seq, "summary", summary, "origin", "cloud", "kind", "change", "createdAt", now, "createdBy", actor, "status", "published", "sites", sites, "approvedBy", nil, "approvedAt", nil, "updateId", nil, "result", nil, "supersededBy", nil, "updatedAt", now)
	depID := dep.Str("deploymentId")
	for _, p := range prep.Open {
		if s := p.Val("status"); s == "published" || s == "staging" || s == "ready" {
			tx.Update(cloud.Col.Proposals(depID).Doc(p.Str("revId")), doc.Obj("status", "superseded", "supersededBy", revID, "updatedAt", now))
			log.Add(tx, CloudEntry{Type: "proposal.superseded", Summary: "Proposal seq " + JSString(p.Val("seq")) + " superseded by seq " + JSString(seq), Data: doc.Obj("revId", present(p, "revId"), "byRevId", revID), Actor: actor})
		}
	}
	tx.Create(cloud.Col.Revisions(depID).Doc(revID), cloud.EncodeRevision(revision))
	tx.Create(cloud.Col.Proposals(depID).Doc(revID), proposal)
	log.Add(tx, CloudEntry{Type: "proposal.published", Summary: "Compat proposal seq " + JSString(seq) + " (agents < 0.14): " + summary, Data: doc.Obj("revId", revID, "parentRevId", parent, "seq", seq, "summary", summary, "commitId", revID, "compat", true), Actor: actor})
	return doc.Obj("revId", revID, "seq", seq)
}

// ReadAgentBranch is readAgentBranch: nil when missing (tx may be nil).
func ReadAgentBranch(deploymentID, siteSlug string, tx *driver.Tx) (*doc.Map, error) {
	s, err := getIn(tx, Bcol.AgentBranches(deploymentID).Doc(siteSlug))
	if err != nil || !s.Exists() {
		return nil, err
	}
	return s.Data(), nil
}

// NewCommitId is newCommitId.
func NewCommitId() string { return ulid.New() }

// NowTs is nowTs.
func NowTs() string { return doc.NowISO() }
