package routes

// Port of backend/src/routes/branches.ts (§21 #104, #105: commits on template / deployment branches, compare, merge,
// revert, reset). asBranchRef, asPoint, resolveBranch, resolvePoint, createSource, originOf and agentCommit live in
// core/route_branches.go. Helpers of this file are prefixed br.

import (
	"errors"
	"sort"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"listTemplateCommits":      listTemplateCommits,
		"getTemplateCommit":        getTemplateCommit,
		"commitTemplate":           commitTemplate,
		"discardTemplateWorking":   discardWorking,
		"listDeploymentCommits":    listDeploymentCommits,
		"getDeploymentCommit":      getDeploymentCommit,
		"commitDeployment":         commitDeployment,
		"discardDeploymentWorking": discardWorking,
		"listAgentBranches":        listAgentBranches,
		"listAgentBranchCommits":   listAgentBranchCommits,
		"getAgentBranchCommit":     getAgentBranchCommit,
		"compareBranches":          compareBranches,
		"previewMerge":             previewMerge,
		"merge":                    merge,
		"revertCommit":             revertCommit,
		"resetToCommit":            resetToCommit,
	})
}

func brOrNil(m *doc.Map, k string) any { return agOr(m, k, nil) }

func brRefOrNil(cm *doc.Map) any {
	if cm == nil {
		return nil
	}
	return shared.CommitRef(cm)
}

// brHeadSeq is `head?.seq ?? 0`.
func brHeadSeq(head *doc.Map) any { return agOr(head, "seq", 0.0) }

// ─────────────────────────────────────────── body parsing

func brAsChoices(v any) ([]any, error) {
	if v == any(doc.Undefined) {
		return []any{}, nil
	}
	l, ok := v.([]any)
	if !ok {
		return nil, cloud.E.Bad("choices", "V_TYPE", "choices must be a list")
	}
	out := make([]any, len(l))
	for i, x := range l {
		at := "choices[" + core.JSString(float64(i)) + "]"
		m, _ := x.(*doc.Map)
		key, ok := m.StrOK("key")
		if !agObjectLike(x) || !ok {
			return nil, cloud.E.Bad(at, "V_TYPE", "a choice is { key, take, value? }")
		}
		take := m.Val("take")
		if !agIn([]string{"source", "target", "custom"}, take) {
			return nil, cloud.E.Bad(at+".take", "V_ENUM", "take must be source | target | custom")
		}
		ch := doc.Obj("key", key, "take", take)
		if take == any("custom") {
			ch.Set("value", agPresent(m, "value"))
		}
		out[i] = ch
	}
	return out, nil
}

func brAsValues(v any) (*doc.Map, error) {
	if agNullish(v) {
		return nil, nil
	}
	m, ok := v.(*doc.Map)
	if !ok {
		return nil, cloud.E.Bad("values", "V_TYPE", "values must be an object")
	}
	return m, nil
}

func brAsSecretValues(v any, target *doc.Map) (*doc.Map, error) {
	if agNullish(v) {
		return nil, nil
	}
	const msg = "secretValues must be an object of strings"
	m, ok := v.(*doc.Map)
	if !ok {
		return nil, cloud.E.Bad("secretValues", "V_TYPE", msg)
	}
	out := doc.New()
	good := true
	m.Each(func(k string, x any) {
		s, ok := x.(string)
		good = good && ok
		if ok && s != "" {
			out.Set(k, s)
		}
	})
	if !good {
		return nil, cloud.E.Bad("secretValues", "V_TYPE", msg)
	}
	if out.Len() == 0 {
		return nil, nil
	}
	if target.Val("kind") != any("deployment") {
		return nil, cloud.E.Bad("secretValues", "V_TEMPLATE_SECRET_VALUE", "secret values can only be set when merging into a deployment")
	}
	return out, nil
}

func brNeedMessage(m any) (string, error) {
	if issues := shared.CheckCommitMessage(m, ""); len(issues) > 0 {
		return "", agInvalid(issues)
	}
	return doc.JSTrim(m.(string)), nil
}

// ─────────────────────────────────────────── §4.5 base selection

type brBase struct {
	base    *doc.Map
	skipped []any
	info    *doc.Map
}

func brBaseFrom(cm *doc.Map, label string) *brBase {
	return &brBase{cm.Map("config"), []any{}, doc.Obj("kind", "origin", "sourceCommitId", agPresent(cm, "commitId"), "label", shared.PointLabel(label, agPresent(cm, "seq")))}
}

func brSelectBase(source *core.Resolved, target *core.Resolved) (*brBase, error) {
	rec, err := core.LoadMergeBase(target.Ref, source.Ref, nil)
	if err != nil {
		return nil, err
	}
	if rec != nil {
		skipped, _ := agOr(rec, "skipped", []any{}).([]any)
		return &brBase{rec.Map("base"), skipped, doc.Obj("kind", "record", "sourceCommitId", agPresent(rec, "sourceCommitId"), "label", agPresent(rec, "label"))}, nil
	}
	load := func(ref *doc.Map, id string) (*doc.Map, error) { return core.LoadCommit(ref, id, nil, true) }
	if o, _ := target.Origin.(*doc.Map); o != nil && shared.SameBranch(o.Map("branch"), source.Ref) && o.Truthy("commitId") {
		cm, err := load(source.Ref, o.Str("commitId"))
		if err != nil {
			return nil, err
		}
		if cm != nil {
			return brBaseFrom(cm, o.Str("label")), nil
		}
	}
	if so, _ := source.Origin.(*doc.Map); so != nil && shared.SameBranch(so.Map("branch"), target.Ref) && so.Truthy("commitId") {
		cm, err := load(target.Ref, so.Str("commitId"))
		if err != nil {
			return nil, err
		}
		if cm != nil {
			return brBaseFrom(cm, target.Label), nil
		}
	}
	// agent branch → its deployment: the deployment commit the site last merged
	sk, tk := source.Ref.Val("kind"), target.Ref.Val("kind")
	if sk == any("agent") && tk == any("deployment") && source.Ref.Val("deploymentId") == target.Ref.Val("deploymentId") && source.Agent.Truthy("mergedFrom") {
		cm, err := load(target.Ref, source.Agent.Map("mergedFrom").Str("commitId"))
		if err != nil {
			return nil, err
		}
		if cm != nil {
			return brBaseFrom(cm, target.Label), nil
		}
	}
	if sk == any("deployment") && tk == any("agent") && target.Agent.Truthy("mergedFrom") {
		cm, err := load(source.Ref, target.Agent.Map("mergedFrom").Str("commitId"))
		if err != nil {
			return nil, err
		}
		if cm != nil {
			return brBaseFrom(cm, source.Label), nil
		}
	}
	return &brBase{nil, []any{}, doc.Obj("kind", "none", "sourceCommitId", nil, "label", "no common history")}, nil
}

// brLockItems: permission locks of secret items (§3.3).
func brLockItems(c *cloud.Ctx, items []any, source, target *core.Resolved) {
	srcRead := true
	if source.Ref.Val("kind") != any("template") {
		srcRead = cloud.Has(c, "deployment.secrets", "read", cloud.TargetOf(source.Dep))
	}
	tgtWrite := false
	if target.Ref.Val("kind") == any("template") {
		tgtWrite = cloud.Has(c, "templates", "write", nil)
	} else if target.Dep != nil {
		tgtWrite = cloud.Has(c, "deployment.secrets", "write", cloud.TargetOf(target.Dep))
	}
	for _, x := range items {
		it := x.(*doc.Map)
		if it.Val("section") != any("secrets") {
			continue
		}
		if strings.HasSuffix(it.Str("key"), "/value") {
			if !srcRead || !tgtWrite {
				it.Set("locked", "needs deployment.secrets.read on the source and deployment.secrets.write on the target")
			}
		} else if target.Ref.Val("kind") == any("deployment") && !tgtWrite {
			it.Set("locked", "needs deployment.secrets.write on the target")
		}
	}
}

type brPlanned struct {
	source *core.ResolvedPoint
	target *core.Resolved
	base   *brBase
	opts   *doc.Map
}

func brPlanFor(c *cloud.Ctx, sp, tref *doc.Map, mode string) (*brPlanned, error) {
	source, err := core.ResolvePoint(c, sp)
	if err != nil {
		return nil, err
	}
	target, err := core.ResolveBranch(c, tref, mode)
	if err != nil {
		return nil, err
	}
	if shared.SameBranch(source.R.Ref, target.Ref) && mode == "write" {
		return nil, cloud.E.Bad("source", "V_REF", "source and target are the same branch (use revert / reset)")
	}
	base, err := brSelectBase(source.R, target)
	if err != nil {
		return nil, err
	}
	return &brPlanned{source, target, base, doc.Obj("secretValues", core.SecretValuesFor(source.R.Ref, target.Ref), "scope", core.ScopeFor(source.R.Ref, target.Ref))}, nil
}

// ─────────────────────────────────────────── deployment / template commit writer

// DeployCheck is DeployCheck: lists of issue maps.
type DeployCheck struct{ Blockers, Warnings []any }

func brNoChange() *doc.Map {
	return agIssue("", "V_NO_CHANGE", "nothing to commit: the result equals the head")
}

func brDeploymentCheck(dep, config, head *doc.Map, cat *core.Catalog) (*DeployCheck, error) {
	deploymentID := dep.Str("deploymentId")
	detected, err := core.LoadDetectedHosts(deploymentID)
	if err != nil {
		return nil, err
	}
	blobContents, err := core.SeedBlobContents(config)
	if err != nil {
		return nil, err
	}
	res := core.SafeValidate(config, core.ValidationCtx("publish", deploymentID, cat, nil, detected).Set("blobContents", blobContents))
	blockers := append([]any{}, res.Errors...)
	if head != nil && len(shared.UncommittedItems(head.Map("config"), config, doc.Obj("secretValues", true))) == 0 {
		blockers = append(blockers, brNoChange())
	}
	// agent-version notes (§21 #104 j): old sites still get the commit as a proposal
	agents, err := cloud.Col.Agents(deploymentID).Select("agentVersion").Get()
	if err != nil {
		return nil, err
	}
	warnings := append([]any{}, res.Warnings...)
	for _, a := range agents.Docs {
		v := agOr(a.Data(), "agentVersion", nil)
		if !shared.SupportsBranches(v) {
			shown := "unknown"
			if v != nil {
				shown = core.JSString(v)
			}
			warnings = append(warnings, doc.Obj("path", "sites["+a.ID()+"]", "code", "W_AGENT_BRANCHES", "message", a.ID()+" runs agent "+shown+" — needs agent ≥ 0.14 for agent branches; it still receives this commit as a proposal (approve it in its agent GUI, whole commit only)", "severity", "warning"))
		}
	}
	return &DeployCheck{blockers, warnings}, nil
}

func brTemplateCheck(config, head *doc.Map, cat *core.Catalog) *DeployCheck {
	res := core.SafeValidate(config, core.ValidationCtx("template", "template", cat, nil, nil))
	blockers := append([]any{}, res.Errors...)
	if head != nil && len(shared.UncommittedItems(head.Map("config"), config, nil)) == 0 {
		blockers = append(blockers, brNoChange())
	}
	return &DeployCheck{blockers, res.Warnings}
}

// brFillGeneratedSecrets: a deployment result: secrets the merge ADDED with a generate rule get a value (the create rule).
func brFillGeneratedSecrets(cfg *doc.Map, now string) []string {
	out := []string{}
	for _, x := range cfg.Arr("secrets") {
		s := x.(*doc.Map)
		if !core.IsSecretSet(s) && s.Truthy("generate") {
			g := s.Map("generate")
			s.Set("value", cloud.GenerateSecret(g.Str("charset"), int(g.Int("length")))).Set("rotatedAt", now)
			out = append(out, s.Str("secretSlug"))
		}
	}
	return out
}

// WriteSpec is WriteSpec. ExpectHead: commit id or nil.
type WriteSpec struct {
	Kind, Message string
	Config        *doc.Map
	Source        *doc.Map
	// SourceCatalog: §21 #113 the catalog of the merged source commit (its site images are valid images of the result)
	SourceCatalog *doc.Map
	RevertOf      *doc.Map
	ResetTo       *doc.Map
	// MergeBase: the merge base record to write (merges), without targetCommitId
	MergeBase  *doc.Map
	ExpectHead any
	Activity   string
	// Generated: §21 #105 slugs whose secret value the commit sets / generates / clears
	Generated       []string
	ActivitySummary string
	ActivityData    *doc.Map
	Data            *doc.Map
	// BeforeWrite: §21 #107 after validation, BEFORE anything is written: verify + promote the commit's staged blobs
	BeforeWrite func(config *doc.Map) error
}

func brHeadID(head *doc.Map) any { return agOr(head, "commitId", nil) }

func brStaleMoved(t *core.Resolved) error {
	return cloud.E.Stale(t.Label+" moved (head is now seq "+core.JSString(brHeadSeq(t.Head))+") — rebase your changes and commit again", doc.Obj("reason", "head-moved", "head", brRefOrNil(t.Head)))
}

func brCanon(v any) (string, error) { return shared.Canonicalize(v) }

// CommitTo is commitTo: write one commit on a template or deployment branch (validation, catalog, compat, working copy :=
// result). cat may be nil.
func CommitTo(c *cloud.Ctx, target *core.Resolved, spec *WriteSpec, cat *core.Catalog) (*doc.Map, *DeployCheck, error) {
	// §21 #113 site images of the head (kept by later commits) and of the merged source are valid images
	if cat == nil {
		var err error
		if cat, err = core.LoadCatalog(); err != nil {
			return nil, nil, err
		}
	}
	headCatalog := target.Head.Map("catalog")
	catalog0 := core.WithSiteImages(cat, headCatalog, spec.SourceCatalog)
	now := doc.NowISO()
	isDeployment := target.Ref.Val("kind") == any("deployment")
	if target.Ref.Val("kind") == any("agent") {
		return nil, nil, cloud.E.Forbidden("agent branches are written only by their agent")
	}
	if brHeadID(target.Head) != spec.ExpectHead {
		return nil, nil, brStaleMoved(target)
	}
	config := cloud.Clone(spec.Config).(*doc.Map)
	generated := append([]string{}, spec.Generated...)
	seen := map[string]bool{}
	for _, g := range generated {
		seen[g] = true
	}
	if isDeployment {
		var err error
		if config, err = shared.NormalizeConfig(config, core.ImageVolumes(catalog0)); err != nil {
			return nil, nil, err
		}
		for _, s := range brFillGeneratedSecrets(config, now) {
			if !seen[s] {
				seen[s] = true
				generated = append(generated, s)
			}
		}
	} else {
		secrets := []any{}
		for _, s := range config.Arr("secrets") {
			secrets = append(secrets, s.(*doc.Map).Shallow().Set("value", nil).Set("rotatedAt", nil))
		}
		config.Set("secrets", secrets)
	}
	var secretValues *doc.Map
	if isDeployment {
		before, _ := agOr(target.Head.Map("config"), "secrets", []any{}).([]any)
		secretValues = shared.SecretValueChanges(before, config.Arr("secrets"), generated)
	}
	var chk *DeployCheck
	if isDeployment {
		var err error
		if chk, err = brDeploymentCheck(target.Dep, config, target.Head, catalog0); err != nil {
			return nil, nil, err
		}
	} else {
		chk = brTemplateCheck(config, target.Head, catalog0)
	}
	if len(chk.Blockers) > 0 {
		return nil, nil, agInvalid(chk.Blockers, "cannot commit: "+chk.Blockers[0].(*doc.Map).Str("message"))
	}
	if spec.BeforeWrite != nil {
		if err := spec.BeforeWrite(config); err != nil {
			return nil, nil, err
		}
	}
	var catalog any
	if isDeployment {
		cg, err := core.BuildRevisionCatalog(config, catalog0)
		if err != nil {
			return nil, nil, err
		}
		catalog = agMapOrNil(cg)
	}
	data := doc.New().Spread(spec.Data)
	if shared.HasSecretValueChanges(secretValues) {
		data.Set("secretValues", secretValues)
	}
	commit, err := shared.MakeCommit(doc.Obj("commitId", core.NewCommitId(), "branch", target.Ref, "parent", agMapOrNil(target.Head), "kind", spec.Kind, "message", spec.Message, "author", core.PersonAuthor(c), "createdAt", now, "config", config, "catalog", catalog, "source", agMapOrNil(spec.Source), "revertOf", agMapOrNil(spec.RevertOf), "resetTo", agMapOrNil(spec.ResetTo), "data", data))
	if err != nil {
		return nil, nil, err
	}
	external, err := core.StoreCommitBody(commit)
	if err != nil {
		return nil, nil, err
	}
	uid := c.Account.UID
	setBase := func(tx *driver.Tx) {
		if spec.MergeBase != nil {
			core.SetMergeBase(tx, target.Ref, spec.MergeBase.Shallow().Set("targetCommitId", agPresent(commit, "commitId")))
		}
	}
	if !isDeployment {
		ref := cloud.Col.Templates().Doc(target.Ref.Str("templateId"))
		err := cloud.DB().RunTransaction(func(tx *driver.Tx) error {
			s, err := tx.Get(ref)
			if err != nil {
				return err
			}
			if !s.Exists() {
				return cloud.E.NotFound("template not found")
			}
			cur := cloud.DecodeTemplate(s.Data())
			curBranch := cur.Map("branch")
			if brHeadID(curBranch.Map("head")) != spec.ExpectHead {
				return cloud.E.Stale("the template moved — rebase your changes and commit again", doc.Obj("reason", "head-moved", "head", agOr(curBranch, "head", nil)))
			}
			core.WriteCommit(tx, commit, external)
			branch := doc.Obj("head", shared.CommitRef(commit), "origin", agOr(curBranch, "origin", nil), "commitCount", curBranch.Num("commitCount")+1)
			a, err := brCanon(shared.TemplateConfig(cur))
			if err != nil {
				return err
			}
			b, err := brCanon(config)
			if err != nil {
				return err
			}
			version := cur.Num("version")
			if a != b {
				version++
			}
			tx.Set(ref, cloud.EncodeTemplate(cur.Shallow().Spread(config).Set("version", version).Set("updatedAt", now).Set("updatedBy", uid).Set("branch", branch)))
			setBase(tx)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		return commit, chk, nil
	}
	depID := target.Ref.Str("deploymentId")
	ref := cloud.Col.Deployments().Doc(depID)
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		s, err := tx.Get(ref)
		if err != nil {
			return err
		}
		dep := cloud.DecodeDeployment(s.Data())
		if dep.Truthy("deleted") {
			return cloud.E.NotFound("deployment not found")
		}
		curBranch := dep.Map("branch")
		if brHeadID(curBranch.Map("head")) != spec.ExpectHead {
			return cloud.E.Stale("the deployment moved — rebase your changes and commit again", doc.Obj("reason", "head-moved", "head", agOr(curBranch, "head", nil)))
		}
		prep, err := core.PrepCompat(tx, depID)
		if err != nil {
			return err
		}
		log := core.NewCloudLog(depID, dep)
		compat := core.WriteCompat(tx, dep, commit, prep, log, false, uid)
		commit.Set("data", commit.Map("data").Shallow().Set("compat", agMapOrNil(compat)))
		core.WriteCommit(tx, commit, external)
		logData := doc.Obj("branch", shared.BranchKey(target.Ref), "commitId", agPresent(commit, "commitId"), "seq", agPresent(commit, "seq"), "message", shared.CommitSummary(commit.Str("message")), "kind", agPresent(commit, "kind"))
		if src := commit.Map("source"); src != nil {
			var picks any = "all"
			if src.Val("picks") != any("all") {
				picks = float64(len(src.Arr("picks")))
			}
			logData.Set("source", shared.BranchKey(src.Map("branch"))).Set("sourceCommitId", agPresent(src, "commitId")).Set("picks", picks).Set("skipped", agPresent(src, "skipped"))
		}
		if compat != nil {
			logData.Set("compat", compat)
		}
		if sv := commit.Map("data").Val("secretValues"); doc.Truthy(sv) {
			logData.Set("secretValues", sv)
		}
		logData.Spread(spec.ActivityData)
		log.Add(tx, core.CloudEntry{Type: spec.Activity, Summary: doc.JSSlice(spec.ActivitySummary, 300), Data: logData, Actor: uid})
		branch := doc.Obj("head", shared.CommitRef(commit), "origin", agOr(curBranch, "origin", nil), "commitCount", curBranch.Num("commitCount")+1)
		a, err := brCanon(dep.Val("draft"))
		if err != nil {
			return err
		}
		b, err := brCanon(config)
		if err != nil {
			return err
		}
		draftVersion := dep.Num("draftVersion")
		if a != b {
			draftVersion++
		}
		tx.Update(ref, cloud.DraftField(config).Set("draftVersion", draftVersion).Set("branch", branch).Set("agentEpoch", dep.Num("agentEpoch")+1).Spread(log.DepUpdate()).Set("updatedAt", now).Set("updatedBy", uid))
		setBase(tx)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return commit, chk, nil
}

// ─────────────────────────────────────────── views

func brListCommitsOf(c *cloud.Ctx, ref *doc.Map) (any, error) {
	limit, err := core.ClampLimit(c, 50, 200)
	if err != nil {
		return nil, err
	}
	q := core.CommitsOf(ref).OrderBy("seq", "desc")
	if cursor := c.Query.Get("cursor"); cursor != "" {
		s, err := core.CommitsOf(ref).Doc(cursor).Get()
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, cloud.E.Bad("cursor", "V_FORMAT", "bad cursor")
		}
		q = q.StartAfterSnap(s)
	}
	r, err := q.Select("commitId", "branch", "seq", "parentId", "kind", "message", "author", "createdAt", "source", "revertOf", "resetTo", "rollbackOf", "hash", "stats").Limit(limit + 1).Get()
	if err != nil {
		return nil, err
	}
	docs := r.Docs
	var next any
	if len(docs) > limit {
		docs = docs[:limit]
		next = docs[limit-1].ID()
	}
	items := make([]any, len(docs))
	for i, d := range docs {
		items[i] = core.SummaryOfDoc(d.Data())
	}
	return doc.Obj("items", items, "cursor", next), nil
}

func brCommitDetail(ref *doc.Map, commitID string, scope any) (any, error) {
	cm, err := core.LoadCommit(ref, commitID, nil, true)
	if err != nil {
		return nil, err
	}
	if cm == nil {
		return nil, cloud.E.NotFound("commit not found")
	}
	var parent *doc.Map
	if cm.Truthy("parentId") {
		if parent, err = core.LoadCommit(ref, cm.Str("parentId"), nil, true); err != nil {
			return nil, err
		}
	}
	changes := shared.UncommittedItems(parent.Map("config"), cm.Map("config"), doc.Obj("secretValues", ref.Val("kind") != any("template"), "scope", scope))
	return doc.Obj("commit", brCommitView(cm), "changes", changes), nil
}

func brPlanResponse(p *brPlanned, items []any, diffOnly bool) *doc.Map {
	var at any = "head"
	if p.source.Commit != nil {
		at = doc.Obj("commitId", agPresent(p.source.Commit, "commitId"))
	}
	return doc.Obj(
		"source", doc.Obj("point", doc.Obj("branch", p.source.R.Ref, "at", at), "label", p.source.Label, "commit", brRefOrNil(p.source.Commit)),
		"target", doc.Obj("branch", p.target.Ref, "label", p.target.Label, "head", brRefOrNil(p.target.Head), "workingClean", p.target.Uncommitted == 0, "uncommitted", p.target.Uncommitted),
		"base", p.base.info,
		"items", items,
		"counts", shared.CountMerge(items),
		"fastForward", shared.IsFastForward(items),
		"diffOnly", diffOnly,
	)
}

func brTargetCfg(p *brPlanned) *doc.Map {
	if p.target.Head != nil {
		return p.target.Head.Map("config")
	}
	return shared.EmptyConfig()
}

// brMergeResult: apply a request to the plan (shared by preview + merge).
func brMergeResult(c *cloud.Ctx, b *doc.Map, mode string) (*brPlanned, *doc.Map, error) {
	sp, err := core.AsPoint(agPresent(b, "source"), "source")
	if err != nil {
		return nil, nil, err
	}
	tref, err := core.AsBranchRef(agPresent(b, "target"), "target")
	if err != nil {
		return nil, nil, err
	}
	secretValues, err := brAsSecretValues(agPresent(b, "secretValues"), tref)
	if err != nil {
		return nil, nil, err
	}
	p, err := brPlanFor(c, sp, tref, mode)
	if err != nil {
		return nil, nil, err
	}
	kind := tref.Val("kind")
	if p.target.Head == nil && kind != any("template") && kind != any("deployment") {
		return nil, nil, cloud.E.NotFound("the target has no commit yet")
	}
	if th := agPresent(b, "targetHead"); th != any(doc.Undefined) && agOr(b, "targetHead", nil) != brHeadID(p.target.Head) {
		return nil, nil, cloud.E.Stale(p.target.Label+" moved (head is now seq "+core.JSString(brHeadSeq(p.target.Head))+") — refresh", doc.Obj("head", brRefOrNil(p.target.Head)))
	}
	if sc := agPresent(b, "sourceCommit"); sp.Val("at") == any("head") && !agNullish(sc) && sc != brHeadID(p.source.Commit) {
		return nil, nil, cloud.E.Conflict(p.source.R.Label+" moved — refresh", doc.Obj("reason", "source-changed", "head", brRefOrNil(p.source.Commit)))
	}
	choices, err := brAsChoices(agPresent(b, "choices"))
	if err != nil {
		return nil, nil, err
	}
	values, err := brAsValues(agPresent(b, "values"))
	if err != nil {
		return nil, nil, err
	}
	targetCfg := brTargetCfg(p)
	opts := p.opts.Shallow().Set("skipped", p.base.skipped)
	items := shared.PlanMerge(p.base.base, p.source.Config, targetCfg, opts)
	brLockItems(c, items, p.source.R, p.target)
	locked := map[string]string{}
	for _, x := range items {
		if it := x.(*doc.Map); it.Truthy("locked") {
			locked[it.Str("key")] = core.JSString(it.Val("locked"))
		}
	}
	for _, x := range choices {
		ch := x.(*doc.Map)
		if l, ok := locked[ch.Str("key")]; ok && ch.Val("take") != any("target") {
			return nil, nil, cloud.E.Forbidden(ch.Str("key") + ": " + l)
		}
	}
	mergeOpts := opts.Shallow()
	if values != nil {
		mergeOpts.Set("values", values)
	}
	r := shared.ApplyMerge(p.base.base, p.source.Config, targetCfg, choices, mergeOpts)
	if secretValues != nil {
		if mode == "write" {
			if err := cloud.NeedDep(c, p.target.Dep, "deployment.secrets", "write"); err != nil {
				return nil, nil, err
			}
		}
		now := doc.NowISO()
		errs := r.Arr("errors")
		secretValues.Each(func(slug string, value any) {
			var s *doc.Map
			for _, x := range r.Map("config").Arr("secrets") {
				if m := x.(*doc.Map); m.Val("secretSlug") == any(slug) {
					s = m
					break
				}
			}
			if s == nil {
				errs = append(errs, doc.Obj("key", "secrets/"+slug+"/value", "message", "secret "+slug+" is not in the result"))
			} else {
				s.Set("value", value).Set("rotatedAt", now)
			}
		})
		r.Set("errors", errs)
	}
	return p, r, nil
}

// ─────────────────────────────────────────── §21 #105 direct commits

func brAsBase(b *doc.Map) (any, error) {
	v, ok := b.Get("baseCommitId")
	if !ok {
		return nil, cloud.E.Bad("baseCommitId", "V_REQUIRED", "baseCommitId (the head commit your changes were made on, or null) is required")
	}
	if v != nil {
		if s, ok := v.(string); !ok || s == "" {
			return nil, cloud.E.Bad("baseCommitId", "V_TYPE", "baseCommitId must be a commit id or null")
		}
	}
	return v, nil
}

func brStaleCheck(t *core.Resolved, base any) error {
	if brHeadID(t.Head) != base {
		return brStaleMoved(t)
	}
	return nil
}

// brSaveLevel: the save-level check of the old section writes, on the change set AS SENT (before normalizeConfig).
func brSaveLevel(config *doc.Map, mode, deploymentID string, cat *core.Catalog) error {
	res := core.SafeValidate(config, core.ValidationCtx(mode, deploymentID, cat, nil, nil))
	if len(res.Errors) > 0 {
		return agInvalid(res.Errors, "cannot save: "+res.Errors[0].(*doc.Map).Str("message"))
	}
	return nil
}

// brObjectOf is an object spread of a typeof-object value: an array gives its index keys.
func brObjectOf(v any) *doc.Map {
	switch x := v.(type) {
	case *doc.Map:
		return x
	case []any:
		out := doc.New()
		for i, e := range x {
			out.Set(core.JSString(float64(i)), e)
		}
		return out
	}
	return doc.New()
}

// ConfigFromInput is configFromInput: the browser change set → a full Config, normalized exactly like the pre-#105 section
// writes. Secrets are SecretDefInput merged with the BASE HEAD values (plainValue absent = keep, null = clear, string =
// set, generateNow). Templates hold no values (a string plainValue / generateNow → 422). kind: template | deployment.
func ConfigFromInput(kind string, input any, baseSecrets []any, cat *core.Catalog) (*doc.Map, []string, error) {
	o, ok := input.(*doc.Map)
	if !ok || o == nil {
		return nil, nil, cloud.E.Bad("config", "V_REQUIRED", "config (the full resulting config: variables, secrets, files, sites) is required")
	}
	for _, k := range []string{"variables", "secrets", "files", "sites"} {
		if _, ok := o.Val(k).([]any); !ok {
			return nil, nil, cloud.E.Bad("config."+k, "V_TYPE", "config."+k+" must be a list")
		}
	}
	isTemplate := kind == "template"
	input2 := o.Arr("secrets")
	for i, x := range input2 {
		at := "config.secrets[" + core.JSString(float64(i)) + "]"
		if !agObjectLike(x) {
			return nil, nil, cloud.E.Bad(at, "V_TYPE", "each secret must be an object")
		}
		sx, _ := x.(*doc.Map)
		pv, isStr := sx.StrOK("plainValue")
		if isTemplate && ((isStr && pv != "") || sx.Truthy("generateNow")) {
			return nil, nil, cloud.E.Bad(at+".plainValue", "V_TEMPLATE_SECRET_VALUE", "templates hold no secret values (set them in a deployment)")
		}
	}
	items := make([]any, len(input2))
	generated := []string{}
	for i, x := range input2 {
		it := brObjectOf(x).Without("value", "rotatedAt")
		items[i] = it
		inBase := false
		for _, s := range baseSecrets {
			inBase = inBase || s.(*doc.Map).Val("secretSlug") == agPresent(it, "secretSlug")
		}
		if it.Val("generateNow") == any(true) || (agPresent(it, "plainValue") == any(doc.Undefined) && !inBase && it.Truthy("generate")) {
			generated = append(generated, it.Str("secretSlug"))
		}
	}
	var secrets []any
	var err error
	if isTemplate {
		defs := make([]any, len(items))
		for i, x := range items {
			defs[i] = x.(*doc.Map).Without("plainValue", "generateNow")
		}
		norm, err := core.NormSecretDefs(defs)
		if err != nil {
			return nil, nil, err
		}
		secrets = make([]any, len(norm))
		for i, s := range norm {
			secrets[i] = s.(*doc.Map).Shallow().Set("value", nil).Set("rotatedAt", nil)
		}
	} else if secrets, err = core.MergeSecrets(items, baseSecrets); err != nil {
		return nil, nil, err
	}
	variables, err := core.NormVariables(o.Val("variables"))
	if err != nil {
		return nil, nil, err
	}
	files, err := core.NormFiles(o.Val("files"))
	if err != nil {
		return nil, nil, err
	}
	sites, err := core.NormSites(o.Val("sites"), core.ImageVolumes(cat))
	if err != nil {
		return nil, nil, err
	}
	config := doc.Obj("variables", variables, "secrets", secrets, "files", files, "sites", sites)
	if isTemplate {
		return config, []string{}, nil
	}
	return config, generated, nil
}

// brCommitView is commitView: O1, every human response carrying a commit masks its secret values.
func brCommitView(cm *doc.Map) *doc.Map {
	return cm.Shallow().Set("config", core.MaskConfig(cm.Map("config")))
}

func brSecretDefs(l []any) (string, error) {
	out := make([]any, len(l))
	for i, x := range l {
		out[i] = x.(*doc.Map).Without("value", "rotatedAt")
	}
	return brCanon(out)
}

func brSummaryOf(message string) string { return shared.CommitSummary(message) }

// brCommitDeploymentBody: §21 #105 + #107 the deployment commit (the handler wraps it so the stage is always dropped).
func brCommitDeploymentBody(c *cloud.Ctx, b *doc.Map, stageID string) (*doc.Map, error) {
	message, err := brNeedMessage(agPresent(b, "message"))
	if err != nil {
		return nil, err
	}
	base, err := brAsBase(b)
	if err != nil {
		return nil, err
	}
	t, err := core.ResolveBranch(c, doc.Obj("kind", "deployment", "deploymentId", c.Params["deploymentId"]), "write")
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("message", brSummaryOf(message))}
	if err := brStaleCheck(t, base); err != nil {
		return nil, err
	}
	cat, err := core.LoadCatalog()
	if err != nil {
		return nil, err
	}
	var headCfg *doc.Map
	if t.Head != nil {
		headCfg = t.Head.Map("config")
	}
	cat = core.WithSiteImages(cat, t.Head.Map("catalog")) // §21 #113 site images the head already runs
	baseSecrets := t.Dep.Map("draft").Arr("secrets")
	if headCfg != nil && !agNullish(agPresent(headCfg, "secrets")) {
		baseSecrets = headCfg.Arr("secrets")
	}
	config, generated, err := ConfigFromInput("deployment", agPresent(b, "config"), baseSecrets, cat)
	if err != nil {
		return nil, err
	}
	if err := brSaveLevel(config, "draft", t.Ref.Str("deploymentId"), cat); err != nil {
		return nil, err
	}
	// secret definitions or values changed → deployment.secrets.write (as the old secrets section write)
	before, _ := agOr(headCfg, "secrets", []any{}).([]any)
	vals := shared.SecretValueChanges(before, config.Arr("secrets"), generated)
	d1, err := brSecretDefs(before)
	if err != nil {
		return nil, err
	}
	d2, err := brSecretDefs(config.Arr("secrets"))
	if err != nil {
		return nil, err
	}
	if d1 != d2 || shared.HasSecretValueChanges(vals) {
		if err := cloud.NeedDep(c, t.Dep, "deployment.secrets", "write"); err != nil {
			return nil, err
		}
	}
	commit, _, err := CommitTo(c, t, &WriteSpec{Kind: "change", Message: message, Config: config, Generated: generated, ExpectHead: base, Activity: "branch.committed", ActivitySummary: "Committed seq " + core.JSString(t.Head.Num("seq")+1) + ": " + brSummaryOf(message), BeforeWrite: core.CommitBlobs(c, headCfg, stageID)}, cat)
	if err != nil {
		return nil, err
	}
	data := doc.Obj("commitId", agPresent(commit, "commitId"), "seq", agPresent(commit, "seq"), "message", brSummaryOf(message), "compat", agOr(commit.Map("data"), "compat", nil))
	if sv := commit.Map("data").Val("secretValues"); doc.Truthy(sv) {
		data.Set("secretValues", sv)
	}
	c.Audit = &cloud.AuditHint{Data: data}
	return commit, nil
}

// ─────────────────────────────────────────── handlers

func brTemplateRef(c *cloud.Ctx) *doc.Map {
	return doc.Obj("kind", "template", "templateId", c.Params["templateId"])
}

func listTemplateCommits(c *cloud.Ctx) (any, error) {
	ref := brTemplateRef(c)
	if _, err := core.ResolveBranch(c, ref, "read"); err != nil {
		return nil, err
	}
	return brListCommitsOf(c, ref)
}

func getTemplateCommit(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "templates", "read", nil); err != nil {
		return nil, err
	}
	return brCommitDetail(brTemplateRef(c), c.Params["commitId"], nil)
}

func brCommitTemplateBody(c *cloud.Ctx, b *doc.Map, stageID string) (*doc.Map, error) {
	message, err := brNeedMessage(agPresent(b, "message"))
	if err != nil {
		return nil, err
	}
	base, err := brAsBase(b)
	if err != nil {
		return nil, err
	}
	t, err := core.ResolveBranch(c, brTemplateRef(c), "write")
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("message", brSummaryOf(message))}
	if err := brStaleCheck(t, base); err != nil {
		return nil, err
	}
	cat, err := core.LoadCatalog()
	if err != nil {
		return nil, err
	}
	config, _, err := ConfigFromInput("template", agPresent(b, "config"), []any{}, cat)
	if err != nil {
		return nil, err
	}
	if err := brSaveLevel(config, "template", "template", cat); err != nil {
		return nil, err
	}
	var headCfg *doc.Map
	if t.Head != nil {
		headCfg = t.Head.Map("config")
	}
	commit, _, err := CommitTo(c, t, &WriteSpec{Kind: "change", Message: message, Config: config, ExpectHead: base, Activity: "branch.committed", BeforeWrite: core.CommitBlobs(c, headCfg, stageID)}, cat)
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("commitId", agPresent(commit, "commitId"), "seq", agPresent(commit, "seq"), "message", brSummaryOf(message))}
	return brCommitView(commit), nil
}

var brCommitKeys = []string{"message", "baseCommitId", "config", "stageId"}

func commitTemplate(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, brCommitKeys); err != nil {
		return nil, err
	}
	stageID, err := core.OptStageId(b)
	if err != nil {
		return nil, err
	}
	// §21 #107: one transaction incl. the bytes — the stage is deleted whatever happens (promoted blobs stay in the store)
	defer core.DropStage(stageID, c.Account.UID)
	return brCommitTemplateBody(c, b, stageID)
}

func discardWorking(c *cloud.Ctx) (any, error) {
	return nil, cloud.E.Gone("there is no working copy (§21 #105): pending changes live in the browser and are discarded there", doc.Obj("hint", "discard in the browser"))
}

func brDeploymentRef(id string) *doc.Map { return doc.Obj("kind", "deployment", "deploymentId", id) }

func listDeploymentCommits(c *cloud.Ctx) (any, error) {
	ref := brDeploymentRef(c.Params["deploymentId"])
	if _, err := core.ResolveBranch(c, ref, "read"); err != nil {
		return nil, err
	}
	return brListCommitsOf(c, ref)
}

// brReadDep is loadDep + needDep deployment.config read.
func brReadDep(c *cloud.Ctx) (*doc.Map, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "read"); err != nil {
		return nil, err
	}
	return dep, nil
}

func getDeploymentCommit(c *cloud.Ctx) (any, error) {
	dep, err := brReadDep(c)
	if err != nil {
		return nil, err
	}
	return brCommitDetail(brDeploymentRef(dep.Str("deploymentId")), c.Params["commitId"], nil)
}

func commitDeployment(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, brCommitKeys); err != nil {
		return nil, err
	}
	stageID, err := core.OptStageId(b)
	if err != nil {
		return nil, err
	}
	defer core.DropStage(stageID, c.Account.UID)
	commit, err := brCommitDeploymentBody(c, b, stageID)
	if err != nil {
		return nil, err
	}
	return brCommitView(commit), nil
}

func listAgentBranches(c *cloud.Ctx) (any, error) {
	dep, err := brReadDep(c)
	if err != nil {
		return nil, err
	}
	id := dep.Str("deploymentId")
	agents, err := cloud.Col.Agents(id).Get()
	if err != nil {
		return nil, err
	}
	branches, err := core.Bcol.AgentBranches(id).Get()
	if err != nil {
		return nil, err
	}
	docs := map[string]*doc.Map{}
	versions := map[string]any{}
	seen := map[string]bool{}
	slugs := []string{}
	for _, d := range branches.Docs {
		docs[d.ID()] = d.Data()
		if !seen[d.ID()] {
			seen[d.ID()] = true
			slugs = append(slugs, d.ID())
		}
	}
	for _, d := range agents.Docs {
		versions[d.ID()] = agOr(d.Data(), "agentVersion", nil)
		if !seen[d.ID()] {
			seen[d.ID()] = true
			slugs = append(slugs, d.ID())
		}
	}
	sort.Strings(slugs)
	depRef := brDeploymentRef(id)
	var depHead *doc.Map
	if h := dep.Map("branch").Map("head"); h != nil {
		if depHead, err = core.LoadCommit(depRef, h.Str("commitId"), nil, true); err != nil {
			return nil, err
		}
	}
	items := []any{}
	for _, s := range slugs {
		d := docs[s]
		if d == nil {
			d = doc.Obj("siteSlug", s, "head", nil, "legacy", !shared.SupportsBranches(versions[s]), "mergedFrom", nil, "uncommitted", nil, "pushedAt", nil, "agentVersion", versions[s])
		}
		agentVersion := versions[s]
		if agentVersion == nil {
			agentVersion = agOr(d, "agentVersion", nil)
		}
		behind, ahead, differs := 0.0, 0.0, 0.0
		if depHead != nil {
			if n := depHead.Num("seq") - d.Map("mergedFrom").Num("seq"); n > 0 {
				behind = n
			}
		}
		if h := d.Map("head"); h != nil {
			aref := depRef
			if !d.Truthy("legacy") {
				aref = doc.Obj("kind", "agent", "deploymentId", id, "siteSlug", s)
			}
			ah, err := core.LoadCommit(aref, h.Str("commitId"), nil, true)
			if err != nil {
				return nil, err
			}
			if ah != nil && depHead != nil {
				diff := shared.UncommittedItems(depHead.Map("config"), ah.Map("config"), doc.Obj("secretValues", true, "scope", s))
				differs = float64(len(diff))
				if !d.Truthy("legacy") && len(diff) > 0 {
					keys := map[string]bool{}
					for _, i := range diff {
						keys[i.(*doc.Map).Str("key")] = true
					}
					after, err := core.Bcol.AgentCommits(id, s).Where("seq", ">", 0).OrderBy("seq", "desc").Limit(50).Get()
					if err != nil {
						return nil, err
					}
					for _, x := range after.Docs {
						cm := x.Data()
						if cm.Map("source").Map("branch").Val("kind") == any("deployment") {
							break
						}
						for _, k := range cm.Map("stats").Strs("keys") {
							if keys[k] {
								ahead++
								break
							}
						}
					}
				}
			}
		}
		supported := shared.SupportsBranches(agentVersion)
		items = append(items, d.Shallow().Set("siteSlug", s).Set("agentVersion", agentVersion).Set("legacy", !supported).Set("behind", behind).Set("ahead", ahead).Set("differs", differs).Set("needsAgent014", !supported))
	}
	return doc.Obj("items", items), nil
}

func brAgentRef(dep *doc.Map, c *cloud.Ctx) *doc.Map {
	return doc.Obj("kind", "agent", "deploymentId", agPresent(dep, "deploymentId"), "siteSlug", c.Params["siteSlug"])
}

func listAgentBranchCommits(c *cloud.Ctx) (any, error) {
	dep, err := brReadDep(c)
	if err != nil {
		return nil, err
	}
	return brListCommitsOf(c, brAgentRef(dep, c))
}

func getAgentBranchCommit(c *cloud.Ctx) (any, error) {
	dep, err := brReadDep(c)
	if err != nil {
		return nil, err
	}
	return brCommitDetail(brAgentRef(dep, c), c.Params["commitId"], c.Params["siteSlug"])
}

func compareBranches(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"source", "target", "diffOnly"}); err != nil {
		return nil, err
	}
	sp, err := core.AsPoint(agPresent(b, "source"), "source")
	if err != nil {
		return nil, err
	}
	target := agPresent(b, "target")
	if b.Val("diffOnly") == any(true) {
		// two points, read only: two-way, no locks
		var tp *doc.Map
		if m, ok := target.(*doc.Map); ok && m.Has("branch") {
			if tp, err = core.AsPoint(target, "target"); err != nil {
				return nil, err
			}
		} else {
			br, err := core.AsBranchRef(target, "target")
			if err != nil {
				return nil, err
			}
			tp = doc.Obj("branch", br, "at", "head")
		}
		s, err := core.ResolvePoint(c, sp)
		if err != nil {
			return nil, err
		}
		t, err := core.ResolvePoint(c, tp)
		if err != nil {
			return nil, err
		}
		opts := doc.Obj("secretValues", core.SecretValuesFor(s.R.Ref, t.R.Ref), "scope", core.ScopeFor(s.R.Ref, t.R.Ref), "preselectFastForward", false)
		items := shared.PlanMerge(nil, s.Config, t.Config, opts)
		return doc.Obj(
			"source", doc.Obj("point", sp, "label", s.Label, "commit", brRefOrNil(s.Commit)),
			"target", doc.Obj("branch", t.R.Ref, "label", t.Label, "head", brRefOrNil(t.Commit), "workingClean", t.R.Uncommitted == 0, "uncommitted", t.R.Uncommitted),
			"base", doc.Obj("kind", "none", "sourceCommitId", nil, "label", "read-only compare"),
			"items", items,
			"counts", shared.CountMerge(items),
			"fastForward", false,
			"diffOnly", true,
		), nil
	}
	tref, err := core.AsBranchRef(target, "target")
	if err != nil {
		return nil, err
	}
	p, err := brPlanFor(c, sp, tref, "read")
	if err != nil {
		return nil, err
	}
	items := shared.PlanMerge(p.base.base, p.source.Config, brTargetCfg(p), p.opts.Shallow().Set("skipped", p.base.skipped))
	brLockItems(c, items, p.source.R, p.target)
	isAgent := tref.Val("kind") == any("agent")
	if isAgent {
		for _, i := range items {
			i.(*doc.Map).Set("locked", "agent branches are written only by their agent")
		}
	}
	return brPlanResponse(p, items, isAgent), nil
}

var brMergeKeys = []string{"source", "target", "targetHead", "sourceCommit", "choices", "values", "secretValues", "message"}

func previewMerge(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, brMergeKeys); err != nil { // message: ignored (same body as merge)
		return nil, err
	}
	p, r, err := brMergeResult(c, b, "read")
	if err != nil {
		return nil, err
	}
	now := doc.NowISO()
	result := cloud.Clone(r.Val("config")).(*doc.Map)
	isDeployment := p.target.Ref.Val("kind") == any("deployment")
	if isDeployment {
		brFillGeneratedSecrets(result, now)
	}
	cat, err := core.LoadCatalog()
	if err != nil {
		return nil, err
	}
	cat = core.WithSiteImages(cat, p.target.Head.Map("catalog"), p.source.Commit.Map("catalog"))
	// UX: nothing picked → say so (the checks of an unchanged result only give misleading errors)
	var chk *DeployCheck
	switch {
	case len(r.Arr("taken")) == 0:
		chk = &DeployCheck{[]any{agIssue("choices", "V_NO_CHANGE", "nothing picked: choose at least one change to merge")}, []any{}}
	case isDeployment && p.target.Dep != nil:
		if chk, err = brDeploymentCheck(p.target.Dep, result, p.target.Head, cat); err != nil {
			return nil, err
		}
	default:
		chk = brTemplateCheck(result, p.target.Head, cat)
	}
	var headCfg *doc.Map
	if p.target.Head != nil {
		headCfg = p.target.Head.Map("config")
	}
	secretOf := func(cfg *doc.Map, slug string) any {
		for _, x := range cfg.Arr("secrets") {
			if s := x.(*doc.Map); s.Val("secretSlug") == any(slug) {
				return agOr(s, "value", nil)
			}
		}
		return nil
	}
	changes := shared.DiffConfigs(headCfg, result, nil, func(slug string) bool { return secretOf(headCfg, slug) != secretOf(result, slug) })
	return doc.Obj(
		"result", core.MaskConfig(result),
		"changes", changes,
		"validation", doc.Obj("errors", chk.Blockers, "warnings", chk.Warnings),
		"unresolved", r.Val("unresolved"),
		"missing", shared.MergeMissing(result),
		"choiceErrors", r.Val("errors"),
		"kind", r.Val("kind"),
		"taken", float64(len(r.Arr("taken"))),
		"skipped", float64(len(r.Arr("notTaken"))),
		"warnings", []any{},
	), nil
}

func brAuditAction(ref *doc.Map, verb string) (action, targetType, target string) {
	if ref.Val("kind") == any("template") {
		return "template." + verb, "template", "templates/" + ref.Str("templateId")
	}
	return "deployment." + verb, "deployment", "deployments/" + ref.Str("deploymentId")
}

func merge(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, brMergeKeys); err != nil {
		return nil, err
	}
	message, err := brNeedMessage(agPresent(b, "message"))
	if err != nil {
		return nil, err
	}
	tref, err := core.AsBranchRef(agPresent(b, "target"), "target")
	if err != nil {
		return nil, err
	}
	action, targetType, target := brAuditAction(tref, "merged")
	c.Audit = &cloud.AuditHint{Action: action, TargetType: targetType, Target: target, Data: doc.Obj("source", agOr(brObjectOf(b.Val("source")), "branch", nil))}
	p, r, err := brMergeResult(c, b, "write")
	if err != nil {
		return nil, err
	}
	if errs := r.Arr("errors"); len(errs) > 0 {
		list := make([]any, len(errs))
		for i, x := range errs {
			e := x.(*doc.Map)
			list[i] = agIssue(e.Str("key"), "V_MERGE_CHOICE", e.Str("message"))
		}
		return nil, agInvalid(list)
	}
	if un := r.Arr("unresolved"); len(un) > 0 {
		list := make([]any, len(un))
		for i, x := range un {
			k := x.(string)
			list[i] = agIssue(k, "V_MERGE_UNRESOLVED", k+": a conflict needs a choice (keep target, take source or a custom value)")
		}
		return nil, agInvalid(list)
	}
	taken := r.Arr("taken")
	if len(taken) == 0 {
		return nil, cloud.E.Bad("choices", "V_NO_CHANGE", "nothing picked: choose at least one change to merge")
	}
	now := doc.NowISO()
	by := c.Account.UID
	if c.Account.Email != nil {
		by = *c.Account.Email
	}
	src := p.source
	srcID, srcSeq := agOr(src.Commit, "commitId", nil), agOr(src.Commit, "seq", nil)
	notTaken := r.Arr("notTaken")
	skipped := shared.NextSkips(notTaken, p.base.skipped, srcSeq, now, by)
	mb := doc.Obj("sourceKey", shared.BranchKey(src.R.Ref), "source", src.R.Ref, "sourceCommitId", srcID, "sourceSeq", srcSeq, "base", shared.MarkSecretValues(src.Config), "skipped", skipped, "label", src.Label, "updatedAt", now, "updatedBy", by)
	kind := r.Str("kind")
	takenStrs := make([]string, len(taken))
	for i, k := range taken {
		takenStrs[i] = k.(string)
	}
	source := doc.Obj("branch", src.R.Ref, "commitId", srcID, "seq", srcSeq, "label", src.R.Label).Spread(shared.SourcePicks(takenStrs, kind)).Set("skipped", float64(len(notTaken)))
	activity, verb := "branch.cherry-picked", "Cherry-picked"
	if kind == "merge" {
		activity, verb = "branch.merged", "Merged"
	}
	commit, _, err := CommitTo(c, p.target, &WriteSpec{
		Kind:            kind,
		Message:         message,
		Config:          r.Map("config"),
		Source:          source,
		SourceCatalog:   src.Commit.Map("catalog"),
		MergeBase:       mb,
		ExpectHead:      brHeadID(p.target.Head),
		Activity:        activity,
		ActivitySummary: verb + " " + core.JSString(float64(len(taken))) + " change(s) from " + src.Label + " (seq " + core.JSString(p.target.Head.Num("seq")+1) + "): " + brSummaryOf(message),
	}, nil)
	if err != nil {
		return nil, err
	}
	hint := *c.Audit
	hint.Action = action
	hint.Data = doc.Obj("kind", kind, "source", shared.BranchKey(src.R.Ref), "sourceCommitId", srcID, "commitId", agPresent(commit, "commitId"), "seq", agPresent(commit, "seq"), "taken", float64(len(taken)), "skipped", float64(len(notTaken)), "message", brSummaryOf(message))
	c.Audit = &hint
	return brCommitView(commit), nil
}

// brHistoryAudit is the shared start of revert and reset.
func brHistoryAudit(c *cloud.Ctx, ref *doc.Map, verb string) {
	action, targetType, target := brAuditAction(ref, verb)
	c.Audit = &cloud.AuditHint{Action: action, TargetType: targetType, Target: target}
}

func revertCommit(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"branch", "commitId", "message", "ifHead", "choices"}); err != nil {
		return nil, err
	}
	ref, err := core.AsBranchRef(agPresent(b, "branch"), "branch")
	if err != nil {
		return nil, err
	}
	message, err := brNeedMessage(agPresent(b, "message"))
	if err != nil {
		return nil, err
	}
	brHistoryAudit(c, ref, "reverted")
	t, err := core.ResolveBranch(c, ref, "write")
	if err != nil {
		return nil, err
	}
	commitID, ok := b.StrOK("commitId")
	if !ok {
		return nil, cloud.E.Bad("commitId", "V_REQUIRED", "commitId is required")
	}
	cm, err := core.LoadCommit(ref, commitID, nil, true)
	if err != nil {
		return nil, err
	}
	if cm == nil {
		return nil, cloud.E.NotFound("commit not found")
	}
	if !cm.Truthy("parentId") {
		return nil, cloud.E.Bad("commitId", "V_REVERT", "the first commit of a branch cannot be reverted (nothing before it) — use reset or edit")
	}
	parent, err := core.LoadCommit(ref, cm.Str("parentId"), nil, true)
	if err != nil {
		return nil, err
	}
	if agOr(b, "ifHead", nil) != brHeadID(t.Head) {
		return nil, cloud.E.Stale("the branch moved — refresh", doc.Obj("head", brRefOrNil(t.Head)))
	}
	if t.Head == nil {
		return nil, errors.New("branch without a head")
	}
	opts := doc.Obj("secretValues", ref.Val("kind") != any("template"))
	choices, err := brAsChoices(agPresent(b, "choices"))
	if err != nil {
		return nil, err
	}
	r := shared.ApplyRevert(cm.Map("config"), parent.Map("config"), t.Head.Map("config"), choices, opts)
	if un := r.Arr("unresolved"); len(un) > 0 {
		list := make([]any, len(un))
		for i, x := range un {
			k := x.(string)
			list[i] = agIssue(k, "V_MERGE_UNRESOLVED", k+": a later commit changed this too — choose")
		}
		return nil, agInvalid(list)
	}
	commit, _, err := CommitTo(c, t, &WriteSpec{Kind: "revert", Message: message, Config: r.Map("config"), RevertOf: doc.Obj("commitId", agPresent(cm, "commitId"), "seq", agPresent(cm, "seq")), ExpectHead: brHeadID(t.Head), Activity: "branch.reverted", ActivitySummary: "Reverted seq " + core.JSString(agPresent(cm, "seq")) + " (seq " + core.JSString(t.Head.Num("seq")+1) + "): " + brSummaryOf(message)}, nil)
	if err != nil {
		return nil, err
	}
	hint := *c.Audit
	hint.Data = doc.Obj("revertOf", agPresent(cm, "commitId"), "commitId", agPresent(commit, "commitId"), "seq", agPresent(commit, "seq"), "message", brSummaryOf(message))
	c.Audit = &hint
	return brCommitView(commit), nil
}

func resetToCommit(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"branch", "commitId", "message", "ifHead"}); err != nil {
		return nil, err
	}
	ref, err := core.AsBranchRef(agPresent(b, "branch"), "branch")
	if err != nil {
		return nil, err
	}
	message, err := brNeedMessage(agPresent(b, "message"))
	if err != nil {
		return nil, err
	}
	brHistoryAudit(c, ref, "reset")
	t, err := core.ResolveBranch(c, ref, "write")
	if err != nil {
		return nil, err
	}
	commitID, ok := b.StrOK("commitId")
	if !ok {
		return nil, cloud.E.Bad("commitId", "V_REQUIRED", "commitId is required")
	}
	cm, err := core.LoadCommit(ref, commitID, nil, false)
	if err != nil {
		return nil, err
	}
	if cm == nil {
		return nil, cloud.E.NotFound("commit not found")
	}
	if agOr(b, "ifHead", nil) != brHeadID(t.Head) {
		return nil, cloud.E.Stale("the branch moved — refresh", doc.Obj("head", brRefOrNil(t.Head)))
	}
	if t.Head == nil {
		return nil, errors.New("branch without a head")
	}
	commit, _, err := CommitTo(c, t, &WriteSpec{Kind: "reset", Message: message, Config: cm.Map("config"), ResetTo: doc.Obj("commitId", agPresent(cm, "commitId"), "seq", agPresent(cm, "seq")), ExpectHead: brHeadID(t.Head), Activity: "branch.reset", ActivitySummary: "Reset to seq " + core.JSString(agPresent(cm, "seq")) + " (seq " + core.JSString(t.Head.Num("seq")+1) + "): " + brSummaryOf(message)}, nil)
	if err != nil {
		return nil, err
	}
	hint := *c.Audit
	hint.Data = doc.Obj("resetTo", agPresent(cm, "commitId"), "commitId", agPresent(commit, "commitId"), "seq", agPresent(commit, "seq"), "message", brSummaryOf(message))
	c.Audit = &hint
	return brCommitView(commit), nil
}
