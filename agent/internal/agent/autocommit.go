package agent

// §21 #113 Docker is the source of truth: a change made outside OPM (docker run / compose / CLI edits) is read by the
// drift scan (§21 #104 f) and shown as uncommitted. The site's drift policy decides what happens next:
//   ask          (default) — it stays uncommitted until a person commits or discards it (Branch page);
//   auto-commit  — once the same changes are stable for autoCommitStable and no update runs, the agent commits them
//                  itself, author kind "unknown" ("UNKNOWN (changed in Docker)"). Persist-to-cloud volume content and
//                  drift notes (changes OPM cannot express) still wait for a person.
// A drift commit applies nothing (the server already runs it). Rolling it back = History → revert / reset, which runs
// the update engine and takes its pre-update snapshot first like every other change.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/branch"
	"opm/agent/internal/model"
)

const driftPolicyKVKey = "drift-policy"

// autoCommitStable: how long the same drift must stay before an auto-commit (Docker / compose often run in steps).
var autoCommitStable = 30 * time.Second

// UnknownAuthorName is the author name of an auto-commit of Docker changes.
const UnknownAuthorName = "UNKNOWN (changed in Docker)"

// DriftPolicy is GET / PUT /_opm/api/branch/drift-policy.
type DriftPolicy struct {
	Mode string `json:"mode"` // ask | auto-commit
}

func (a *Agent) driftPolicy() DriftPolicy {
	p := DriftPolicy{Mode: "ask"}
	_, _ = a.db.GetKV(driftPolicyKVKey, &p)
	if p.Mode != "auto-commit" {
		p.Mode = "ask"
	}
	return p
}

func (a *Agent) handleGetDriftPolicy(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return a.driftPolicy(), 200, nil
}

func (a *Agent) handlePutDriftPolicy(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var p DriftPolicy
	if e := readJSON(r, &p); e != nil {
		return nil, 0, e
	}
	if p.Mode != "ask" && p.Mode != "auto-commit" {
		return nil, 0, invalid("mode is ask or auto-commit", []model.ValidationIssue{{Path: "mode", Code: "V_ENUM", Message: "ask | auto-commit", Severity: "error"}})
	}
	if err := a.db.SetKV(driftPolicyKVKey, p); err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	a.log.Info("branch", "drift policy", "mode", p.Mode, "actor", rc.actor)
	a.kickLoop("drift")
	return p, 200, nil
}

type autoCommitState struct {
	mu    sync.Mutex
	sig   string
	since time.Time
}

var autoStates sync.Map // *Agent → *autoCommitState

func (a *Agent) autoState() *autoCommitState {
	v, _ := autoStates.LoadOrStore(a, &autoCommitState{})
	return v.(*autoCommitState)
}

// autoCommitKeys: the uncommitted drift items an auto-commit takes (not persist-to-cloud volume content).
func autoCommitKeys(items []model.DriftItem) []string {
	var keys []string
	for _, it := range items {
		if it.Origin == "volume-content" { // §21 #117 a person commits content changes
			continue
		}
		keys = append(keys, it.Key)
	}
	sort.Strings(keys)
	return keys
}

// maybeAutoCommitDrift runs after every drift scan.
func (a *Agent) maybeAutoCommitDrift(ctx context.Context, now time.Time) *model.Commit {
	if a.driftPolicy().Mode != "auto-commit" || !a.branchMode() || a.lock() != nil {
		return nil
	}
	u := a.uncommittedNow()
	keys := autoCommitKeys(u.items)
	st := a.autoState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(keys) == 0 {
		st.sig, st.since = "", time.Time{}
		return nil
	}
	sig := strings.Join(keys, ",")
	if u.report != nil {
		sig += "|" + u.report.ScannedAt
		parts := []string{}
		for _, it := range u.items {
			parts = append(parts, it.Key+"="+it.SourceHash)
		}
		sort.Strings(parts)
		sig = strings.Join(parts, ",")
	}
	if sig != st.sig {
		st.sig, st.since = sig, now
		return nil
	}
	if now.Sub(st.since) < autoCommitStable {
		return nil
	}
	c, he := a.commitDriftAs(keys, unknownActor, fmt.Sprintf("%s: %d change%s", UnknownAuthorName, len(keys), plural(len(keys))))
	if he != nil {
		a.log.Warn("branch", "auto-commit of Docker changes failed", "error", he.msg)
		st.since = now // retry after another stable period
		return nil
	}
	st.sig, st.since = "", time.Time{}
	return c
}

// unknownActor marks the drift commit of an auto-commit (author kind "unknown").
const unknownActor = "\x00unknown"

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// commitDriftAs commits drift items without applying anything (the server already runs them) — the drift half of
// handleCommitLocal for the auto-commit.
func (a *Agent) commitDriftAs(keys []string, actor, message string) (*model.Commit, *httpError) {
	hr := a.headRevision()
	if hr == nil {
		return nil, herr(model.ErrStale, "this site has no head yet", nil)
	}
	u := a.uncommittedNow()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var choices []model.MergeChoice
	var adopt, driftKeys []string
	for _, it := range u.items {
		if want[it.Key] {
			choices = append(choices, model.MergeChoice{Key: it.Key, Take: "source"})
			driftKeys = append(driftKeys, it.Key)
		}
	}
	if len(choices) == 0 {
		return nil, invalid("nothing to commit", nil)
	}
	if u.report != nil {
		for _, k := range driftKeys {
			adopt = append(adopt, u.report.Adopt[k]...)
		}
	}
	res := branch.Apply(nil, u.combined, hr.Config, choices, branch.Options{SecretValues: true, Scope: a.site})
	cfg, err := branch.ToConfig(res.Config)
	if err != nil {
		return nil, invalid(err.Error(), nil)
	}
	cat := hr.Catalog
	if u.report != nil && len(u.report.LocalCatalog.Images) > 0 {
		cat = mergeCatalogs(&cat, &u.report.LocalCatalog)
	}
	cat = pruneCatalog(&cfg, mergeCatalogs(&cat, &hr.Catalog))
	actorName := actor
	if actor == unknownActor {
		actorName = "UNKNOWN"
	}
	rev := a.newBranchRevision(cfg, cat, actorName, message, "change")
	if err := a.putRevision(rev); err != nil {
		return nil, herr(model.ErrInternal, err.Error(), nil)
	}
	meta := a.commitMetaFor(rev, "drift", message, actorName, nil, model.CommitData{})
	if actor == unknownActor {
		meta.Author = a.unknownAuthor()
	}
	plan := &model.UpdatePlan{UpdateID: newID(), Kind: "merge", Revision: *rev, Commits: map[string]model.CommitMeta{a.site: meta}}
	a.saveMergeRun(plan.UpdateID, mergeRun{Adopt: adopt, Actor: actorName})
	if err := a.db.SetKV("head", model.HeadHash{RevID: rev.RevID, Seq: rev.Seq, Hash: rev.Hash}); err != nil {
		return nil, herr(model.ErrInternal, err.Error(), nil)
	}
	a.commitBranchAt(plan, rev, meta)
	a.activity(nil, "system", "drift.committed", fmt.Sprintf("%d server changes auto-committed as %s (seq %d)", len(driftKeys), UnknownAuthorName, rev.Seq), map[string]any{"commitId": rev.RevID, "keys": firstN(driftKeys, 50), "auto": true})
	c, _ := a.commitByID(rev.RevID)
	a.kickLoop("ping")
	return c, nil
}

// unknownAuthor is the author of an auto-commit of Docker changes (§21 #113).
func (a *Agent) unknownAuthor() model.CommitAuthor {
	s := a.site
	n := UnknownAuthorName
	return model.CommitAuthor{Kind: "unknown", Email: "UNKNOWN", Name: &n, SiteSlug: &s}
}
