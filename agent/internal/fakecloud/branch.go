package fakecloud

// §21 #104 deployment branch + agent branch mirror (the agent endpoints of BRANCH_CLOUD_API).

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"opm/agent/internal/branch"
	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

type fakeBranch struct {
	commits  []model.Commit                      // the deployment branch, oldest first
	agent    map[string][]model.Commit           // agent branch mirror per site
	reports  map[string]*model.AgentBranchReport // heartbeat branch reports
	disabled bool
	drift    map[string][]model.AgentDriftPushRequest // §21 #106 drift pushes per site, in arrival order
}

// DriftPushes returns the drift reports a site pushed (POST /v1/agent/branch/drift), oldest first.
func (c *Cloud) DriftPushes(site string) []model.AgentDriftPushRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.AgentDriftPushRequest(nil), c.brState().drift[site]...)
}

func (c *Cloud) brState() *fakeBranch {
	if c.br == nil {
		c.br = &fakeBranch{agent: map[string][]model.Commit{}, reports: map[string]*model.AgentBranchReport{}, drift: map[string][]model.AgentDriftPushRequest{}}
	}
	return c.br
}

// Commit appends a commit to the deployment branch (= a release the sites see; no proposal).
func (c *Cloud) Commit(cfg model.Config, cat model.RevisionCatalog, message string) model.Commit {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.brState()
	id := c.newULID()
	ch, _ := canonical.HashCanonical(cat)
	var parent *string
	var pc any
	seq := 1
	if n := len(b.commits); n > 0 {
		p := b.commits[n-1].CommitID
		parent, seq, pc = &p, b.commits[n-1].Seq+1, b.commits[n-1].Config
	}
	uid := "fake-uid"
	cm := model.Commit{CommitID: id, Branch: model.BranchRef{Kind: "deployment", DeploymentID: c.DeploymentID}, Seq: seq, ParentID: parent, Kind: "change", Message: message,
		Author: model.CommitAuthor{Kind: "person", Email: "dev@opm.test", UID: &uid}, CreatedAt: nowTs(), Config: cfg, Hash: branch.ConfigHash(cfg), Catalog: &cat, CatalogHash: &ch,
		Stats: branch.Stats(pc, cfg, branch.Options{SecretValues: true}), Data: model.CommitData{}}
	if seq == 1 {
		cm.Kind = "create"
	}
	b.commits = append(b.commits, cm)
	c.epoch++
	c.cloudActivity("branch.committed", message, map[string]any{"commitId": id, "seq": float64(seq)})
	return cm
}

// DisableBranches makes the fake cloud an old cloud (no CloudView.branch).
func (c *Cloud) DisableBranches(off bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.brState().disabled = off
	c.epoch++
}

// CloudCommits returns the deployment branch (oldest first).
func (c *Cloud) CloudCommits() []model.Commit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.Commit{}, c.brState().commits...)
}

// AgentBranch returns the pushed commits of a site (oldest first).
func (c *Cloud) AgentBranch(site string) []model.Commit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.Commit{}, c.brState().agent[site]...)
}

// AgentBranchReport is the last heartbeat branch report of a site.
func (c *Cloud) AgentBranchReport(site string) *model.AgentBranchReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.brState().reports[site]
}

func (c *Cloud) viewBranch() *model.CloudBranchView {
	b := c.brState()
	if b.disabled || len(b.commits) == 0 {
		return nil
	}
	last := b.commits[len(b.commits)-1]
	v := &model.CloudBranchView{Head: &model.CloudHeadRef{CommitRef: last.Ref(), CatalogHash: last.CatalogHash}, Recent: []model.CommitRef{}}
	for i := len(b.commits) - 1; i >= 0 && len(v.Recent) < model.CloudRecentCommits; i-- {
		v.Recent = append(v.Recent, b.commits[i].Ref())
	}
	return v
}

func (c *Cloud) commitImage(key string) (model.ImageSpec, bool) {
	b := c.brState()
	for i := len(b.commits) - 1; i >= 0; i-- {
		if b.commits[i].Catalog != nil {
			if im, ok := b.commits[i].Catalog.Images[key]; ok {
				return im, true
			}
		}
	}
	return model.ImageSpec{}, false
}

var reAgentCommit = regexp.MustCompile(`^/v1/agent/commits/([^/]+)$`)

// routeBranch answers the branch agent endpoints (false = not a branch route).
func (c *Cloud) routeBranch(w http.ResponseWriter, r *http.Request, body []byte, rc reqCtx) bool {
	p, m := r.URL.Path, r.Method
	var g []string
	switch {
	case m == "GET" && p == "/v1/agent/commits":
		c.mu.Lock()
		b := c.brState()
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 50 {
			limit = 20
		}
		out := []model.CommitRef{}
		for i := len(b.commits) - 1; i >= 0 && len(out) < limit; i-- {
			out = append(out, b.commits[i].Ref())
		}
		c.mu.Unlock()
		writeJSON(w, 200, model.ListResponse[model.CommitRef]{Items: out})
	case m == "GET" && match(reAgentCommit, p, &g):
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, cm := range c.brState().commits {
			if cm.CommitID == g[1] {
				writeJSON(w, 200, model.AgentCommitResponse{Commit: cm})
				return true
			}
		}
		writeErr(w, model.ErrNotFound, "commit not found", nil)
	case m == "POST" && p == "/v1/agent/branch/drift":
		var req model.AgentDriftPushRequest
		if !decode(w, body, &req) {
			return true
		}
		if req.Fingerprint == "" {
			writeErr(w, model.ErrInvalid, "fingerprint required", nil)
			return true
		}
		if req.LiveConfig != nil {
			for _, s := range req.LiveConfig.Secrets {
				if s.Value != nil && *s.Value != model.Mask {
					writeErr(w, model.ErrInvalid, "a secret value must never be sent", nil)
					return true
				}
			}
		}
		c.mu.Lock()
		b := c.brState()
		b.drift[rc.signer] = append(b.drift[rc.signer], req)
		c.mu.Unlock()
		writeJSON(w, 200, model.AgentDriftPushResponse{Stored: true, Mirrored: false})
	case m == "POST" && p == "/v1/agent/branch/commits":
		var req model.PushAgentBranchRequest
		if !decode(w, body, &req) {
			return true
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		b := c.brState()
		list := b.agent[rc.signer]
		stored := []string{}
		for _, cm := range req.Commits {
			if cm.Branch.Kind != "agent" || cm.Branch.SiteSlug != rc.signer {
				writeErr(w, model.ErrInvalid, "the branch of a pushed commit must be the signer's agent branch", nil)
				return true
			}
			if branch.ConfigHash(cm.Config) != cm.Hash {
				writeErr(w, model.ErrInvalid, "commit "+cm.CommitID+": hash does not match its config", nil)
				return true
			}
			dup := false
			for _, x := range list {
				if x.CommitID == cm.CommitID {
					dup = true
				} else if x.Seq == cm.Seq {
					writeErr(w, model.ErrConflict, "fork: seq already taken", nil)
					return true
				}
			}
			if dup {
				continue
			}
			if cm.ParentID != nil {
				ok := false
				for _, x := range list {
					if x.CommitID == *cm.ParentID && x.Seq == cm.Seq-1 {
						ok = true
					}
				}
				if !ok && len(list) > 0 && cm.Data.MigratedFrom == nil {
					writeErr(w, model.ErrInvalid, "commit "+cm.CommitID+": parent not stored", nil)
					return true
				}
			}
			list = append(list, cm)
			stored = append(stored, cm.CommitID)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Seq < list[j].Seq })
		b.agent[rc.signer] = list
		var head *model.CommitRef
		if n := len(list); n > 0 {
			r := list[n-1].Ref()
			head = &r
		}
		if len(stored) > 0 {
			c.cloudActivity("agent-branch.pushed", rc.signer+" pushed "+strconv.Itoa(len(stored))+" commits", map[string]any{"siteSlug": rc.signer, "commitIds": stored})
		}
		writeJSON(w, 200, model.PushAgentBranchResponse{Stored: stored, Head: head})
	default:
		return false
	}
	return true
}
