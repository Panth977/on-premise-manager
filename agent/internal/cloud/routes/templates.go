package routes

import (
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

func init() {
	cloud.Handlers(map[string]cloud.Handler{"listTemplates": listTemplates, "createTemplate": createTemplate, "getTemplate": getTemplate, "patchTemplate": patchTemplate, "deleteTemplate": deleteTemplate})
}

func tplLoad(id string) (*doc.Map, error) {
	s, err := cloud.Col.Templates().Doc(id).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("template not found")
	}
	return cloud.DecodeTemplate(s.Data()), nil
}

func listTemplates(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "templates", "read", nil); err != nil {
		return nil, err
	}
	q, err := cloud.Col.Templates().Get()
	if err != nil {
		return nil, err
	}
	deps, err := cloud.Col.Deployments().Where("deleted", "==", false).Select("srcTemplate", "branch").Get()
	if err != nil {
		return nil, err
	}
	used := map[string]float64{}
	for _, d := range deps.Docs {
		data := d.Data()
		ob := data.Map("branch").Map("origin").Map("branch")
		t := any(doc.Undefined)
		if ob.Val("kind") == "template" {
			t = aFld(ob, "templateId")
		}
		if aNullish(t) {
			t = aFld(data.Map("srcTemplate"), "templateId")
		}
		if s, ok := t.(string); ok && s != "" {
			used[s]++
		}
	}
	items := make([]any, 0, len(q.Docs))
	for _, d := range q.Docs {
		t := cloud.DecodeTemplate(d.Data())
		siteSlugs := []any{}
		for _, s := range t.Arr("sites") {
			siteSlugs = append(siteSlugs, aFld(s.(*doc.Map), "siteSlug"))
		}
		var branch any
		if b := t.Map("branch"); b != nil {
			branch = core.StripUncommitted(b)
		}
		items = append(items, doc.Obj(
			"templateId", aFld(t, "templateId"), "slug", aFld(t, "slug"), "label", aFld(t, "label"), "icon", aFld(t, "icon"), "description", aFld(t, "description"),
			"version", aFld(t, "version"), "updatedAt", aFld(t, "updatedAt"), "siteSlugs", siteSlugs,
			"counts", doc.Obj("variables", float64(len(t.Arr("variables"))), "secrets", float64(len(t.Arr("secrets"))), "files", float64(len(t.Arr("files"))), "sites", float64(len(t.Arr("sites")))),
			"usedBy", used[t.Str("templateId")], "branch", branch,
		))
	}
	aSortBy(items, "label")
	return doc.Obj("items", items), nil
}

func createTemplate(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "templates", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"label", "slug", "icon", "description", "from", "message"}); err != nil {
		return nil, err
	}
	if m := aFld(b, "message"); m != doc.Undefined {
		if s, ok := m.(string); !ok || doc.JSTrim(s) == "" || doc.JSLen(s) > 2000 {
			return nil, cloud.E.Bad("message", "V_COMMIT_MESSAGE", "message must be 1–2000 characters")
		}
	}
	v := &cloud.V{}
	cloud.VLabel(v, aFld(b, "label"), "label", cloud.Limits["label"], true)
	core.VIconFormat(v, aFld(b, "icon"), "icon", false)
	cloud.VDescription(v, aFld(b, "description"), "description")
	core.CheckRequestedSlug(v, aFld(b, "slug"))
	if v.OK() {
		if err := core.VIconRefs(v, []core.IconField{{Path: "icon", Value: aFld(b, "icon")}}); err != nil {
			return nil, err
		}
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	// §21 #104 create from a template / deployment / agent branch: deep copy, secret values dropped, generate rules kept
	src, err := core.CreateSource(c, aFld(b, "from"), doc.Undefined)
	if err != nil {
		return nil, err
	}
	ref := cloud.Col.Templates().Doc()
	t := doc.NowISO()
	var cfg *doc.Map
	if src != nil {
		cfg = cloud.Clone(src.Config).(*doc.Map)
	} else {
		cfg = doc.Obj("variables", []any{}, "secrets", []any{}, "files", []any{}, "sites", []any{})
	}
	cfg.Set("secrets", aMaskSecrets(cfg.Arr("secrets")))
	var origin, source any
	var mergeBase *doc.Map
	if src != nil {
		o, s, m := core.OriginOf(src)
		origin, source, mergeBase = o, s, m
	}
	message := "created empty"
	if s := b.Str("message"); doc.JSTrim(s) != "" {
		message = doc.JSTrim(s)
	} else if src != nil {
		var seq any
		if src.Commit != nil {
			seq = aOr(src.Commit, "seq", nil)
		}
		message = shared.CreateMessage(src.R.Label, seq)
	}
	branchRef := doc.Obj("kind", "template", "templateId", ref.ID())
	commit, err := shared.MakeCommit(doc.Obj("commitId", core.NewCommitId(), "branch", branchRef, "parent", nil, "kind", "create", "message", message, "author", core.PersonAuthor(c), "createdAt", t, "config", cfg, "source", source))
	if err != nil {
		return nil, err
	}
	external, err := core.StoreCommitBody(commit)
	if err != nil {
		return nil, err
	}
	branch := doc.Obj("head", shared.CommitRef(commit), "origin", origin, "commitCount", 1.0)
	uid := c.Account.UID
	tpl := doc.Obj("templateId", ref.ID(), "slug", "", "label", doc.JSTrim(b.Str("label")), "icon", aOr(b, "icon", nil), "description", aOr(b, "description", ""), "version", 1.0).
		Spread(cfg).Spread(doc.Obj("createdAt", t, "createdBy", uid, "updatedAt", t, "updatedBy", uid, "branch", branch))
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		slug, err := core.NewSlug(tx, "template", tpl.Str("label"), aFld(b, "slug")) // §21 #106 (reads before writes)
		if err != nil {
			return err
		}
		tpl.Set("slug", slug)
		core.WriteCommit(tx, commit, external)
		if mergeBase != nil {
			core.SetMergeBase(tx, branchRef, mergeBase.Shallow().Set("updatedBy", aEmailOr(c, uid)).Set("targetCommitId", commit.Val("commitId")))
		}
		tx.Create(ref, cloud.EncodeTemplate(tpl))
		return nil
	})
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("from", source, "commitId", commit.Val("commitId"))}
	return tpl, nil
}

func getTemplate(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "templates", "read", nil); err != nil {
		return nil, err
	}
	return tplLoad(c.Params["templateId"])
}

func patchTemplate(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "templates", "write", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"ifVersion", "label", "icon", "description", "variables", "secrets", "files", "sites"}, "templateId", "slug", "version", "createdAt", "createdBy"); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	if !aIsInt(aFld(b, "ifVersion")) {
		v.Err("ifVersion", "V_REQUIRED", "ifVersion is required")
	}
	cloud.VLabel(v, aFld(b, "label"), "label", cloud.Limits["label"], false)
	core.VIconFormat(v, aFld(b, "icon"), "icon", false)
	cloud.VDescription(v, aFld(b, "description"), "description")
	if v.OK() {
		if err := core.VIconRefs(v, []core.IconField{{Path: "icon", Value: aFld(b, "icon")}}); err != nil {
			return nil, err
		}
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	// §21 #105: the config changes only by a commit (no working copy)
	for _, k := range []string{"variables", "secrets", "files", "sites"} {
		if b.Has(k) {
			return nil, cloud.E.Gone("the template config is changed only by a commit (§21 #105): "+k+" cannot be patched", doc.Obj("hint", "POST /v1/templates/{templateId}/commits { message, baseCommitId, config }"))
		}
	}
	cat, err := core.LoadCatalog()
	if err != nil {
		return nil, err
	}
	ref := cloud.Col.Templates().Doc(c.Params["templateId"])
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, cloud.E.NotFound("template not found")
		}
		cur := cloud.DecodeTemplate(s.Data())
		if cur.Num("version") != b.Num("ifVersion") {
			return nil, cloud.E.Conflict("the template changed (version "+core.JSString(cur.Val("version"))+")", doc.Obj("current", cur.Val("version")))
		}
		next := cur.Shallow()
		if l, ok := b.StrOK("label"); ok {
			next.Set("label", doc.JSTrim(l))
		}
		if i := aFld(b, "icon"); i != doc.Undefined {
			next.Set("icon", i)
		} else {
			next.Set("icon", aOr(cur, "icon", nil))
		}
		next.Set("description", aOr(b, "description", aFld(cur, "description")))
		next.Set("version", cur.Num("version")+1)
		next.Set("updatedAt", doc.NowISO())
		next.Set("updatedBy", c.Account.UID)
		if br := next.Map("branch"); br != nil {
			next.Set("branch", core.StripUncommitted(br))
		}
		res := core.SafeValidate(doc.Obj("variables", aFld(next, "variables"), "secrets", aFld(next, "secrets"), "files", aFld(next, "files"), "sites", aFld(next, "sites")), core.ValidationCtx("template", "template", cat, nil, nil))
		tx.Set(ref, cloud.EncodeTemplate(next))
		return doc.Obj("template", next, "validation", doc.Obj("ok", true, "issues", res.Issues)), nil
	})
}

func deleteTemplate(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "templates", "write", nil); err != nil {
		return nil, err
	}
	id := c.Params["templateId"]
	if _, err := tplLoad(id); err != nil {
		return nil, err
	}
	if err := cloud.Col.Templates().Doc(id).Delete(); err != nil {
		return nil, err
	}
	return doc.Obj("deleted", true), nil
}
