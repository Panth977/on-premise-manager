package core

import (
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// OnAgentHeartbeat is onAgentHeartbeat of core/mirror-hooks.ts (set by the mirror port; best effort): headCommitId is a
// string or nil.
var OnAgentHeartbeat = func(deploymentID, siteSlug string, headCommitID any) {}

// CloudBranchView is cloudBranchView of routes/agent-branches.ts.
func CloudBranchView(deploymentID string, dep *doc.Map) (*doc.Map, error) {
	head := dep.Map("branch").Map("head")
	if head == nil {
		return doc.Obj("head", nil, "recent", []any{}), nil
	}
	q, err := Bcol.DeploymentCommits(deploymentID).OrderBy("seq", "desc").Select("commitId", "seq", "hash", "message", "author", "createdAt", "kind", "catalogHash").Limit(shared.CLOUD_RECENT_COMMITS).Get()
	if err != nil {
		return nil, err
	}
	recent := []any{}
	var h *doc.Map
	for _, d := range q.Docs {
		recent = append(recent, shared.CommitRef(d.Data()))
		if h == nil && d.ID() == head.Str("commitId") {
			h = d.Data()
		}
	}
	return doc.Obj("head", head.Shallow().Set("catalogHash", or(h, "catalogHash", nil)), "recent", recent), nil
}

// AllowedCommits is allowedCommits: {config, catalog} of the commits whose images / blobs a site may download.
func AllowedCommits(deploymentID string) ([]*doc.Map, error) {
	recent, err := Bcol.DeploymentCommits(deploymentID).OrderBy("seq", "desc").Limit(shared.CLOUD_RECENT_COMMITS + 1).Get()
	if err != nil {
		return nil, err
	}
	branches, err := Bcol.AgentBranches(deploymentID).Get()
	if err != nil {
		return nil, err
	}
	out := []*doc.Map{}
	add := func(d *doc.Map) error {
		cm, err := DecodeCommit(d, false)
		if err != nil {
			return err
		}
		out = append(out, doc.Obj("config", present(cm, "config"), "catalog", present(cm, "catalog")))
		return nil
	}
	for _, d := range recent.Docs {
		if err := add(d.Data()); err != nil {
			return nil, err
		}
	}
	for _, b := range branches.Docs {
		d := b.Data()
		if !d.Truthy("head") || d.Truthy("legacy") {
			continue
		}
		s, err := Bcol.AgentCommits(deploymentID, b.ID()).Doc(d.Map("head").Str("commitId")).Get()
		if err != nil {
			return nil, err
		}
		if s.Exists() {
			if err := add(s.Data()); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func cleanRef(h *doc.Map) *doc.Map {
	return doc.Obj("commitId", present(h, "commitId"), "seq", present(h, "seq"), "hash", present(h, "hash"), "message", doc.JSSlice(JSString(or(h, "message", "")), 2000), "author", present(h, "author"), "createdAt", present(h, "createdAt"), "kind", present(h, "kind"))
}

// StoreHeartbeatBranch is storeHeartbeatBranch: heartbeat.branch (agents ≥ 0.14) → agentBranches/{site}.
func StoreHeartbeatBranch(deploymentID, siteSlug string, rep any, agentVersion string) error {
	if nullish(rep) {
		return nil
	}
	r, _ := rep.(*doc.Map)
	if _, isArr := rep.([]any); r == nil && !isArr {
		return cloud.E.Bad("branch", "V_TYPE", "branch must be an object or null")
	}
	num := func(m *doc.Map, k string) float64 {
		if v := m.Val(k); isInt(v) && v.(float64) >= 0 {
			return v.(float64)
		}
		return 0
	}
	var unc any
	u := r.Val("uncommitted")
	um, _ := u.(*doc.Map)
	if _, isArr := u.([]any); (um != nil) || isArr {
		var scanned any
		if s, ok := um.StrOK("scannedAt"); ok {
			scanned = s
		}
		unc = doc.Obj("working", num(um, "working"), "drift", num(um, "drift"), "notes", num(um, "notes"), "scannedAt", scanned)
	}
	upd := doc.Obj("siteSlug", siteSlug, "legacy", false, "agentVersion", agentVersion, "uncommitted", unc)
	var headID any
	if h := r.Map("head"); h != nil {
		if id, ok := h.StrOK("commitId"); ok {
			upd.Set("head", cleanRef(h))
			headID = id
		}
	}
	if m := r.Map("mergedFrom"); m != nil {
		if id, ok := m.StrOK("commitId"); ok && isInt(m.Val("seq")) {
			upd.Set("mergedFrom", doc.Obj("commitId", id, "seq", m.Val("seq")))
		}
	}
	if err := Bcol.AgentBranches(deploymentID).Doc(siteSlug).Set(upd, driver.Merge); err != nil {
		return err
	}
	OnAgentHeartbeat(deploymentID, siteSlug, headID)
	return nil
}

// LegacyHeadMirror is legacyHeadMirror: reads now; the returned func (nil = nothing to do) writes.
func LegacyHeadMirror(tx *driver.Tx, deploymentID, siteSlug string, revision *doc.Map, agentVersion any) (func(), error) {
	if shared.SupportsBranches(agentVersion) {
		return nil, nil
	}
	revID := revision.Str("revId")
	s, err := tx.Get(Bcol.DeploymentCommits(deploymentID).Doc(revID))
	if err != nil {
		return nil, err
	}
	var head *doc.Map
	if s.Exists() {
		head = shared.CommitRef(s.Data())
	} else {
		msg := present(revision, "summary")
		if !doc.Truthy(msg) {
			msg = "revision " + JSString(present(revision, "seq"))
		}
		head = doc.Obj("commitId", revID, "seq", present(revision, "seq"), "hash", present(revision, "hash"), "message", msg, "author", doc.Obj("kind", "person", "email", present(revision, "actor"), "uid", nil, "name", nil, "siteSlug", nil), "createdAt", present(revision, "createdAt"), "kind", "legacy")
	}
	exists := s.Exists()
	return func() {
		var merged any
		if exists {
			merged = doc.Obj("commitId", present(head, "commitId"), "seq", present(head, "seq"))
		}
		tx.Set(Bcol.AgentBranches(deploymentID).Doc(siteSlug), doc.Obj("siteSlug", siteSlug, "head", head, "legacy", true, "mergedFrom", merged, "agentVersion", agentVersion, "pushedAt", doc.NowISO()), driver.Merge)
	}, nil
}
