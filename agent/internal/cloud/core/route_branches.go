package core

import (
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/shared"
)

// AsBranchRef is asBranchRef of routes/branches.ts.
func AsBranchRef(v any, path string) (*doc.Map, error) {
	if s, ok := v.(string); ok {
		if r := shared.ParseBranchKey(s); r != nil {
			return r, nil
		}
	} else if o, ok := v.(*doc.Map); ok && o != nil {
		t, d, s := o.Str("templateId"), o.Str("deploymentId"), o.Str("siteSlug")
		switch o.Val("kind") {
		case "template":
			if t != "" {
				return doc.Obj("kind", "template", "templateId", t), nil
			}
		case "deployment":
			if d != "" {
				return doc.Obj("kind", "deployment", "deploymentId", d), nil
			}
		case "agent":
			if d != "" && s != "" {
				return doc.Obj("kind", "agent", "deploymentId", d, "siteSlug", s), nil
			}
		}
	}
	return nil, cloud.E.Bad(path, "V_TYPE", path+" must be a branch ({ kind: template | deployment | agent, … } or a branch key)")
}

// AsPoint is asPoint: {branch, at}.
func AsPoint(v any, path string) (*doc.Map, error) {
	o, ok := v.(*doc.Map)
	if _, isArr := v.([]any); (!ok || o == nil) && !isArr {
		return nil, cloud.E.Bad(path, "V_TYPE", path+" must be { branch, at }")
	}
	branch, err := AsBranchRef(present(o, "branch"), path+".branch")
	if err != nil {
		return nil, err
	}
	at := or(o, "at", "head")
	if at == "head" || at == "working" {
		return doc.Obj("branch", branch, "at", at), nil
	}
	if m, ok := at.(*doc.Map); ok {
		if id, ok := m.StrOK("commitId"); ok {
			return doc.Obj("branch", branch, "at", doc.Obj("commitId", id)), nil
		}
	}
	return nil, cloud.E.Bad(path+".at", "V_TYPE", path+".at must be head | working | { commitId }")
}

// Resolved is Resolved. Origin: *doc.Map or nil. Dep, Tpl, Agent may be nil.
type Resolved struct {
	Ref         *doc.Map
	Label       string
	Head        *doc.Map
	Uncommitted float64
	Origin      any
	Dep, Tpl    *doc.Map
	Agent       *doc.Map
}

func labelOf(d *doc.Map) string { return JSString(present(d, "label")) }

// ResolveBranch is resolveBranch; mode: read | write.
func ResolveBranch(c *cloud.Ctx, ref *doc.Map, mode string) (*Resolved, error) {
	if ref.Val("kind") == "template" {
		action := "write"
		if mode == "read" {
			action = "read"
		}
		if err := cloud.Need(c, "templates", action, nil); err != nil {
			return nil, err
		}
		s, err := cloud.Col.Templates().Doc(ref.Str("templateId")).Get()
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, cloud.E.NotFound("template not found")
		}
		tpl := cloud.DecodeTemplate(s.Data())
		var head *doc.Map
		if h := tpl.Map("branch").Map("head"); h != nil {
			if head, err = LoadCommit(ref, h.Str("commitId"), nil, true); err != nil {
				return nil, err
			}
		}
		return &Resolved{Ref: ref, Label: labelOf(tpl), Head: head, Origin: or(tpl.Map("branch"), "origin", nil), Tpl: tpl}, nil
	}
	dep, err := cloud.LoadDep(c, ref.Str("deploymentId"))
	if err != nil {
		return nil, err
	}
	if ref.Val("kind") == "deployment" {
		action := "publish"
		if mode == "read" {
			action = "read"
		}
		if err := cloud.NeedDep(c, dep, "deployment.config", action); err != nil {
			return nil, err
		}
		var head *doc.Map
		if h := dep.Map("branch").Map("head"); h != nil {
			if head, err = LoadCommit(ref, h.Str("commitId"), nil, true); err != nil {
				return nil, err
			}
		}
		return &Resolved{Ref: ref, Label: labelOf(dep), Head: head, Origin: or(dep.Map("branch"), "origin", nil), Dep: dep}, nil
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "read"); err != nil {
		return nil, err
	}
	if mode == "write" {
		return nil, cloud.E.Forbidden("agent branches are written only by their agent (merge in the agent GUI of the site)")
	}
	id, site := dep.Str("deploymentId"), ref.Str("siteSlug")
	agent, err := ReadAgentBranch(id, site, nil)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		a, err := cloud.Col.Agents(id).Doc(site).Get()
		if err != nil {
			return nil, err
		}
		if !a.Exists() {
			return nil, cloud.E.NotFound("site " + site + " has no agent branch")
		}
	}
	var head *doc.Map
	if h := agent.Map("head"); h != nil {
		b := doc.Obj("kind", "deployment", "deploymentId", id)
		if !agent.Truthy("legacy") {
			b = ref
		}
		if head, err = LoadCommit(b, h.Str("commitId"), nil, true); err != nil {
			return nil, err
		}
	}
	unc := 0.0
	if u := agent.Map("uncommitted"); u != nil {
		unc = u.Num("working") + u.Num("drift") + u.Num("notes")
	}
	return &Resolved{Ref: ref, Label: labelOf(dep) + " · " + site, Head: head, Uncommitted: unc, Dep: dep, Agent: agent}, nil
}

// ResolvedPoint is what ResolvePoint returns. Commit may be nil.
type ResolvedPoint struct {
	R              *Resolved
	Config, Commit *doc.Map
	Label          string
}

// ResolvePoint is resolvePoint: the config + commit of a point (read permission).
func ResolvePoint(c *cloud.Ctx, p *doc.Map) (*ResolvedPoint, error) {
	branch := p.Map("branch")
	r, err := ResolveBranch(c, branch, "read")
	if err != nil {
		return nil, err
	}
	at := p.Val("at")
	if at == "working" {
		return nil, cloud.E.Bad("at", "V_ENUM", "there is no server working copy (§21 #105): pending changes live in the browser — compare them there, or use head / { commitId }")
	}
	if at == "head" {
		if r.Head == nil {
			return nil, cloud.E.NotFound(r.Label + " has no commit yet")
		}
		return &ResolvedPoint{r, r.Head.Map("config"), r.Head, shared.PointLabel(r.Label, r.Head.Val("seq"))}, nil
	}
	cm, err := LoadCommit(branch, p.Map("at").Str("commitId"), nil, true)
	if err != nil {
		return nil, err
	}
	if cm == nil {
		return nil, cloud.E.NotFound("commit not found")
	}
	return &ResolvedPoint{r, cm.Map("config"), cm, shared.PointLabel(r.Label, cm.Val("seq"))}, nil
}

// CreatedSource is what CreateSource returns.
type CreatedSource struct {
	Point *doc.Map
	ResolvedPoint
}

// CreateSource is createSource: `from` of a create request (read permission); templateId is the legacy shortcut for a
// template head. Pass doc.Undefined for a missing value; nil result = no source.
func CreateSource(c *cloud.Ctx, from, templateID any) (*CreatedSource, error) {
	if !nullish(from) {
		p, err := AsPoint(from, "from")
		if err != nil {
			return nil, err
		}
		s, err := ResolvePoint(c, p)
		if err != nil {
			if p.Map("branch").Val("kind") == "template" && p.Val("at") == "head" {
				r, rerr := ResolveBranch(c, p.Map("branch"), "read")
				if rerr != nil {
					return nil, rerr
				}
				if r.Head == nil && r.Tpl != nil {
					return &CreatedSource{p, ResolvedPoint{r, shared.TemplateConfig(r.Tpl), nil, r.Label}}, nil
				}
			}
			return nil, err
		}
		return &CreatedSource{p, *s}, nil
	}
	if id, ok := templateID.(string); ok && id != "" {
		branch := doc.Obj("kind", "template", "templateId", id)
		p := doc.Obj("branch", branch, "at", "head")
		r, err := ResolveBranch(c, branch, "read")
		if err != nil {
			return nil, err
		}
		if r.Head != nil {
			return &CreatedSource{p, ResolvedPoint{r, r.Head.Map("config"), r.Head, shared.PointLabel(r.Label, r.Head.Val("seq"))}}, nil
		}
		return &CreatedSource{p, ResolvedPoint{r, shared.TemplateConfig(r.Tpl), nil, r.Label}}, nil
	}
	return nil, nil
}

// OriginOf is originOf: origin + source + merge base of a new branch created from src.
func OriginOf(src *CreatedSource) (origin, source, mergeBase *doc.Map) {
	now := doc.NowISO()
	id, seq := or(src.Commit, "commitId", nil), or(src.Commit, "seq", nil)
	ref, label := src.R.Ref, src.R.Label
	origin = doc.Obj("branch", ref, "commitId", id, "seq", seq, "label", label)
	if src.R.Tpl != nil && src.Commit == nil {
		origin.Set("templateVersion", present(src.R.Tpl, "version"))
	}
	source = doc.Obj("branch", ref, "commitId", id, "seq", seq, "label", label, "picks", "all", "skipped", 0.0)
	mergeBase = doc.Obj("sourceKey", shared.BranchKey(ref), "source", ref, "sourceCommitId", id, "sourceSeq", seq, "base", shared.MarkSecretValues(src.Config), "skipped", []any{}, "label", src.Label, "updatedAt", now, "updatedBy", "create")
	return origin, source, mergeBase
}

// AgentCommit is agentCommit: agent-facing deployment commit fetch, plain config + secret.fetched (_cloud).
func AgentCommit(deploymentID, siteSlug, commitID string) (*doc.Map, error) {
	cm, err := LoadCommit(doc.Obj("kind", "deployment", "deploymentId", deploymentID), commitID, nil, false)
	if err != nil {
		return nil, err
	}
	if cm == nil {
		return nil, cloud.E.NotFound("commit not found")
	}
	secrets := cm.Map("config").Arr("secrets")
	slugs := []any{}
	for _, x := range secrets {
		s := x.(*doc.Map)
		if IsSecretSet(s) {
			slugs = append(slugs, present(s, "secretSlug"))
		}
	}
	if len(secrets) > 0 {
		e := CloudEntry{Type: "secret.fetched", Summary: "Site " + siteSlug + " fetched commit seq " + JSString(cm.Val("seq")) + " with " + JSString(float64(len(slugs))) + " secret value(s)", Data: doc.Obj("siteSlug", siteSlug, "secretSlugs", slugs, "commitId", present(cm, "commitId"))}
		if err := AppendCloud(deploymentID, e); err != nil {
			return nil, err
		}
	}
	return cm, nil
}
