package routes

// Port of the handlers of backend/src/routes/agent-branches.ts (§21 #104 agent side of branches: commit fetches and the
// agent-branch push). Its helpers live in core/route_agent_branches.go.

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

const abPushMax = 20

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"getAgentCommit":        getAgentCommit,
		"listAgentCloudCommits": listAgentCloudCommits,
		"pushAgentBranch":       pushAgentBranch,
	})
}

func getAgentCommit(c *cloud.Ctx) (any, error) {
	cm, err := core.AgentCommit(c.Agent.DeploymentID, c.Agent.SiteSlug, c.Params["commitId"])
	if err != nil {
		return nil, err
	}
	return doc.Obj("commit", cm), nil
}

func listAgentCloudCommits(c *cloud.Ctx) (any, error) {
	n, ok := 10.0, true
	if s, has := c.Query.Lookup("limit"); has {
		n, ok = cloud.JSNumber(s)
	}
	limit := 10
	if ok && agIsInt(n) && n > 0 {
		limit = int(math.Min(50, n))
	}
	q, err := core.Bcol.DeploymentCommits(c.Agent.DeploymentID).OrderBy("seq", "desc").Select("commitId", "seq", "hash", "message", "author", "createdAt", "kind").Limit(limit).Get()
	if err != nil {
		return nil, err
	}
	items := make([]any, len(q.Docs))
	for i, d := range q.Docs {
		items[i] = shared.CommitRef(d.Data())
	}
	return doc.Obj("items", items), nil
}

// abSortSeq is x.seq - y.seq as a sort key: a null commit is a TypeError in the comparator, any other value without a
// numeric seq compares as NaN (treated as equal).
func abSortSeq(v any) float64 {
	if v == nil {
		panic("Cannot read properties of null (reading 'seq')")
	}
	m, _ := v.(*doc.Map)
	if f, ok := m.Val("seq").(float64); ok {
		return f
	}
	return math.NaN()
}

func pushAgentBranch(c *cloud.Ctx) (any, error) {
	a := c.Agent
	deploymentID, siteSlug, agent := a.DeploymentID, a.SiteSlug, a.Agent
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	raw, ok := b.Val("commits").([]any)
	if !ok || len(raw) == 0 {
		return nil, cloud.E.Bad("commits", "V_REQUIRED", "commits must be a non-empty list")
	}
	if len(raw) > abPushMax {
		return nil, cloud.E.Bad("commits", "V_LENGTH", fmt.Sprintf("at most %d commits per push", abPushMax))
	}
	own := doc.Obj("kind", "agent", "deploymentId", deploymentID, "siteSlug", siteSlug)
	sorted := append([]any{}, raw...)
	if len(sorted) > 1 {
		sort.SliceStable(sorted, func(i, j int) bool { return abSortSeq(sorted[i]) < abSortSeq(sorted[j]) })
	}
	commits := make([]*doc.Map, len(sorted))
	for i, x := range sorted {
		at := fmt.Sprintf("commits[%d]", i)
		cm, _ := x.(*doc.Map)
		_, isID := cm.StrOK("commitId")
		if seq := cm.Val("seq"); !agObjectLike(x) || !isID || !agIsInt(seq) || seq.(float64) < 1 {
			return nil, cloud.E.Bad(at, "V_TYPE", "a commit needs commitId, seq ≥ 1, config, hash")
		}
		br := cm.Map("branch")
		if br == nil || br.Val("kind") != any("agent") || br.Val("deploymentId") != any(deploymentID) || br.Val("siteSlug") != any(siteSlug) {
			return nil, cloud.E.Bad(at+".branch", "V_REF", "the branch must be this site (agent:<deploymentId>/<siteSlug>)")
		}
		if m, ok := cm.StrOK("message"); !ok || doc.JSTrim(m) == "" {
			return nil, cloud.E.Bad(at+".message", "V_COMMIT_MESSAGE", "a commit message is required")
		}
		e, err := shared.VerifyCommitLink(cm, nil)
		if err != nil {
			return nil, err
		}
		if e != nil {
			return nil, cloud.E.Bad(at+".hash", "V_FORMAT", core.JSString(e))
		}
		commits[i] = cm
	}
	external := map[string]bool{}
	for _, cm := range commits {
		ext, err := core.StoreCommitBody(cm)
		if err != nil {
			return nil, err
		}
		external[cm.Str("commitId")] = ext
	}
	inBatch := func(id any) *doc.Map {
		for _, x := range commits {
			if agPresent(x, "commitId") == id {
				return x
			}
		}
		return nil
	}
	// the list survives a transaction retry, as in TS
	stored := []any{}
	head, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (any, error) {
		coll := core.Bcol.AgentCommits(deploymentID, siteSlug)
		brRef := core.Bcol.AgentBranches(deploymentID).Doc(siteSlug)
		depRef := cloud.Col.Deployments().Doc(deploymentID)
		existing := make([]*driver.DocSnap, len(commits))
		for i, cm := range commits {
			if existing[i], err = tx.Get(coll.Doc(cm.Str("commitId"))); err != nil {
				return nil, err
			}
		}
		bs, err := tx.Get(brRef)
		if err != nil {
			return nil, err
		}
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		parents := make([]*driver.DocSnap, len(commits))
		for i, cm := range commits {
			if p := agPresent(cm, "parentId"); doc.Truthy(p) && inBatch(p) == nil {
				id, ok := p.(string)
				if !ok {
					return nil, errors.New("parentId is not a document id")
				}
				if parents[i], err = tx.Get(coll.Doc(id)); err != nil {
					return nil, err
				}
			}
		}
		sameSeq := make([]*driver.QuerySnap, len(commits))
		for i, cm := range commits {
			if sameSeq[i], err = tx.Query(coll.Where("seq", "==", cm.Val("seq")).Limit(1)); err != nil {
				return nil, err
			}
		}
		known := map[string]float64{}
		for i, cm := range commits {
			id := cm.Str("commitId")
			seq := cm.Num("seq")
			if ex := existing[i]; ex.Exists() {
				if agPresent(ex.Data(), "hash") != agPresent(cm, "hash") {
					return nil, cloud.E.Conflict("commit " + id + " is stored with another hash")
				}
				known[id] = seq
				continue
			}
			for _, d := range sameSeq[i].Docs {
				if d.ID() != id {
					return nil, cloud.E.Conflict("fork: seq "+core.JSString(seq)+" is already "+d.ID(), doc.Obj("current", d.ID()))
				}
			}
			if p := agPresent(cm, "parentId"); doc.Truthy(p) {
				var pseq *float64
				pid, _ := p.(string)
				if s, ok := known[pid]; ok {
					pseq = &s
				} else if x := inBatch(p); x != nil {
					s := x.Num("seq")
					pseq = &s
				} else if parents[i] != nil && parents[i].Exists() {
					s := parents[i].Data().Num("seq")
					pseq = &s
				}
				if pseq == nil {
					// the first pushed commit after a migration may name a parent that was never pushed
					if !cm.Map("data").Truthy("migratedFrom") {
						return nil, cloud.E.Bad("commits."+id+".parentId", "V_REF", "unknown parent "+core.JSString(p)+" (push the parent first)")
					}
				} else if seq != *pseq+1 {
					return nil, cloud.E.Bad("commits."+id+".seq", "V_RANGE", "seq must be parent.seq + 1 = "+core.JSString(*pseq+1))
				}
			} else if seq != 1 {
				return nil, cloud.E.Bad("commits."+id+".seq", "V_RANGE", "a commit without parent must have seq 1")
			}
			known[id] = seq
			clean := doc.Obj("commitId", id, "branch", own, "seq", seq, "parentId", agOr(cm, "parentId", nil), "kind", agPresent(cm, "kind"), "message", doc.JSTrim(cm.Str("message")), "author", agPresent(cm, "author"), "createdAt", agPresent(cm, "createdAt"), "source", agOr(cm, "source", nil), "revertOf", agOr(cm, "revertOf", nil), "resetTo", agOr(cm, "resetTo", nil), "rollbackOf", agOr(cm, "rollbackOf", nil), "config", agPresent(cm, "config"), "hash", agPresent(cm, "hash"), "catalog", agOr(cm, "catalog", nil), "catalogHash", agOr(cm, "catalogHash", nil), "stats", agOr(cm, "stats", doc.Obj("added", 0.0, "changed", 0.0, "removed", 0.0, "keys", []any{}, "truncated", false)), "data", agOr(cm, "data", doc.New()))
			tx.Create(coll.Doc(id), core.EncodeCommit(clean, external[id]))
			stored = append(stored, id)
		}
		var cur *doc.Map
		if bs.Exists() {
			cur = bs.Data()
		}
		newestTop := commits[len(commits)-1]
		var headRef *doc.Map
		if cur.Map("head") == nil || cur.Truthy("legacy") || newestTop.Num("seq") >= cur.Map("head").Num("seq") {
			headRef = shared.CommitRef(newestTop)
		} else {
			headRef = cur.Map("head")
		}
		upd := doc.Obj("siteSlug", siteSlug, "head", headRef, "legacy", false, "pushedAt", doc.NowISO(), "agentVersion", agOr(agent, "agentVersion", nil))
		for i := len(commits) - 1; i >= 0; i-- {
			src := commits[i].Map("source")
			if src.Map("branch").Val("kind") == any("deployment") && src.Truthy("commitId") && agPresent(src, "seq") != nil {
				upd.Set("mergedFrom", doc.Obj("commitId", agPresent(src, "commitId"), "seq", agPresent(src, "seq")))
				break
			}
		}
		tx.Set(brRef, upd, driver.Merge)
		if len(stored) > 0 {
			dep := cloud.DecodeDeployment(ds.Data())
			log := core.NewCloudLog(deploymentID, dep)
			ids := stored
			if len(ids) > 20 {
				ids = ids[:20]
			}
			log.Add(tx, core.CloudEntry{Type: "agent-branch.pushed", Summary: fmt.Sprintf("Site %s pushed %d commit(s); head seq %s: %s", siteSlug, len(stored), core.JSString(agPresent(headRef, "seq")), shared.CommitSummary(headRef.Str("message"))), Data: doc.Obj("siteSlug", siteSlug, "commitIds", ids, "head", agPresent(headRef, "commitId")), Origin: "agent"})
			tx.Update(depRef, log.DepUpdate())
		}
		return headRef, nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("stored", stored, "head", head), nil
}
