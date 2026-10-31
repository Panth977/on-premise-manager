package routes

import (
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// Deployments, legacy draft endpoints, diff, proposals + revisions (routes/deployments.ts). proposalMeta, revisionSummary,
// loadRevision, openProposals and mergeSecrets are in core/route_deployments.go.

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"listDeployments":  listDeployments,
		"createDeployment": createDeployment,
		"getDeployment":    getDeployment,
		"patchDeployment":  patchDeployment,
		"deleteDeployment": deleteDeployment,
		"putDraftSection":  goneNoWorkingCopy,
		"putDraftSite":     goneNoWorkingCopy,
		"revealSecret":     revealSecret,
		"regenerateSecret": regenerateSecret,
		"getDiff":          getDiff,
		"publish":          publish,
		"resetDraftToHead": resetDraftToHead,
		"listProposals":    listProposals,
		"listRevisions":    listRevisions,
		"getRevision":      getRevision,
	})
}

func emptyCatalog() *doc.Map {
	return doc.Obj("services", doc.New(), "images", doc.New(), "notFoundPages", doc.New())
}

func slugMap(cat *core.Catalog, extra *doc.Map) func(string) string {
	return func(slug string) string {
		if v := cat.Services.Map(slug).Val("serviceSlug"); v != nil {
			s, _ := v.(string)
			return s
		}
		if v := extra.Map(slug).Val("serviceSlug"); v != nil {
			s, _ := v.(string)
			return s
		}
		return slug
	}
}

// PublishResult is publishCheck's result.
type PublishResult struct{ Changes, Blockers, Warnings []any }

// PublishCheck is publishCheck: blockers + warnings for publishing the draft (§6.1 Diff). head: { config, catalog } or nil;
// detected and blobContents may be nil.
func PublishCheck(dep, head *doc.Map, cat *core.Catalog, detected, blobContents *doc.Map) *PublishResult {
	// §21 #101 blobContents: texts of starter-content entries with render 'expressions' (V_FILE_RENDER, publish level)
	ctx := core.ValidationCtx("publish", dep.Str("deploymentId"), cat, nil, detected)
	if blobContents != nil {
		ctx.Set("blobContents", blobContents)
	}
	draft := dep.Map("draft")
	res := core.SafeValidate(draft, ctx)
	headCfg := head.Map("config")
	changes := shared.DiffConfigs(headCfg, draft, slugMap(cat, head.Map("catalog").Map("services")), func(slug string) bool {
		a := findBy(headCfg.Arr("secrets"), "secretSlug", slug)
		b := findBy(draft.Arr("secrets"), "secretSlug", slug)
		return fldOr(a, "value", nil) != fldOr(b, "value", nil)
	})
	// §21 #105: no working copy: `head` here is the PARENT of the head commit, the diff = what the newest commit changed
	return &PublishResult{changes, append([]any{}, res.Errors...), res.Warnings}
}

func summaries(deps []*doc.Map) ([]any, error) {
	out := make([]any, len(deps))
	for i, d := range deps {
		id := d.Str("deploymentId")
		agents, err := cloud.Col.Agents(id).Select("lastSeenAt").Get()
		if err != nil {
			return nil, err
		}
		open, err := core.OpenProposals(id, nil)
		if err != nil {
			return nil, err
		}
		draftSlugs := []any{}
		all := map[string]bool{}
		for _, s := range mapsIn(d.Map("draft").Arr("sites")) {
			draftSlugs = append(draftSlugs, fld(s, "siteSlug"))
			all[s.Str("siteSlug")] = true
		}
		online := 0
		now := time.Now().UnixMilli()
		for _, a := range agents.Docs {
			all[a.ID()] = true
			if t, ok := doc.ParseISO(a.Data().Str("lastSeenAt")); ok && now-t < 90_000 {
				online++
			}
		}
		var head, pending any
		if h := d.Map("head"); d.Truthy("head") {
			head = doc.Obj("revId", fld(h, "revId"), "seq", fld(h, "seq"))
		}
		if len(open) > 0 {
			p := open[0]
			pending = doc.Obj("revId", fld(p, "revId"), "seq", fld(p, "seq"), "status", fld(p, "status"))
		}
		out[i] = doc.Obj("deploymentId", fld(d, "deploymentId"), "slug", fld(d, "slug"), "label", fld(d, "label"), "image", fld(d, "image"), "tagSlugs", fld(d, "tagSlugs"), "siteSlugs", draftSlugs, "sitesOnline", online, "sitesTotal", len(all), "head", head, "pending", pending, "updatedAt", fld(d, "updatedAt"), "branch", fldOr(d, "branch", nil))
	}
	return out, nil
}

// nonUnique: new Set(list).size !== list.length of a list of strings.
func nonUnique(l []string) bool {
	seen := map[string]bool{}
	for _, s := range l {
		if seen[s] {
			return true
		}
		seen[s] = true
	}
	return false
}

func checkRefs(v *cloud.V, path string, col *driver.CollectionRef, ids []string, msg func(string) string) error {
	if len(ids) == 0 {
		return nil
	}
	refs := make([]*driver.DocRef, len(ids))
	for i, id := range ids {
		refs[i] = col.Doc(id)
	}
	snaps, err := cloud.DB().GetAll(refs...)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if !s.Exists() {
			v.Err(path, "V_REF", msg(s.ID()))
		}
	}
	return nil
}

// stringsOf: the items of a value that is a list of strings; ok=false for anything else. noEmpty also refuses "".
func stringsOf(val any, noEmpty bool) ([]string, bool) {
	l, ok := val.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(l))
	for i, x := range l {
		s, ok := x.(string)
		if !ok || (noEmpty && s == "") {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

// checkTagsManagers: pass doc.Undefined for a missing value.
func checkTagsManagers(v *cloud.V, tagSlugs, managers any) error {
	if tagSlugs != doc.Undefined {
		if l, ok := stringsOf(tagSlugs, false); !ok || len(l) > 20 {
			v.Err("tagSlugs", "V_LENGTH", "tagSlugs must be a list of ≤ 20 tag slugs")
		} else {
			if nonUnique(l) {
				v.Err("tagSlugs", "V_DUPLICATE", "tagSlugs must be unique")
			}
			if err := checkRefs(v, "tagSlugs", cloud.Col.Tags(), l, func(id string) string { return `tag "` + id + `" does not exist` }); err != nil {
				return err
			}
		}
	}
	if managers != doc.Undefined {
		if l, ok := stringsOf(managers, true); !ok || len(l) > 100 {
			v.Err("managerAccountUids", "V_TYPE", "managerAccountUids must be a list of account uids")
		} else {
			if nonUnique(l) {
				v.Err("managerAccountUids", "V_DUPLICATE", "managerAccountUids must be unique")
			}
			if err := checkRefs(v, "managerAccountUids", cloud.Col.Accounts(), l, func(id string) string { return `account "` + id + `" does not exist` }); err != nil {
				return err
			}
		}
	}
	return nil
}

func listDeployments(c *cloud.Ctx) (any, error) {
	limit, err := core.ClampLimit(c, 100, 500)
	if err != nil {
		return nil, err
	}
	q := cloud.Col.Deployments().Where("deleted", "==", false)
	if tag := c.Query.Get("tag"); tag != "" {
		q = q.Where("tagSlugs", "array-contains", tag)
	}
	q = q.OrderBy("updatedAt", driver.Desc)
	var after *driver.DocSnap
	if cursor := c.Query.Get("cursor"); cursor != "" {
		if after, err = cloud.Col.Deployments().Doc(cursor).Get(); err != nil {
			return nil, err
		}
		if !after.Exists() {
			return nil, cloud.E.Bad("cursor", "V_FORMAT", "bad cursor")
		}
	}
	visible := []*doc.Map{}
	exhausted := false
	for round := 0; round < 50 && len(visible) <= limit && !exhausted; round++ {
		pq := q
		if after != nil {
			pq = pq.StartAfterSnap(after)
		}
		page, err := pq.Limit(200).Get()
		if err != nil {
			return nil, err
		}
		if len(page.Docs) < 200 {
			exhausted = true
		}
		for _, d := range page.Docs {
			after = d
			dep := cloud.DecodeDeployment(d.Data())
			if cloud.CanSeeDeployment(c.Account, cloud.TargetOf(dep)) {
				visible = append(visible, dep)
			}
			if len(visible) > limit {
				break
			}
		}
	}
	shown := visible[:min(limit, len(visible))]
	items, err := summaries(shown)
	if err != nil {
		return nil, err
	}
	var cursor any
	if len(visible) > limit {
		cursor = fld(shown[len(shown)-1], "deploymentId")
	}
	return doc.Obj("items", items, "cursor", cursor), nil
}

func createDeployment(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"label", "slug", "templateId", "tagSlugs", "managerAccountUids", "image", "from", "message", "copySecretValues"}); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	cloud.VLabel(v, fld(b, "label"), "label", cloud.Limits["label"], true)
	core.VIconFormat(v, fld(b, "image"), "image", true)
	if v.OK() {
		if err := core.VIconRefs(v, []core.IconField{{Path: "image", Value: fld(b, "image")}}); err != nil {
			return nil, err
		}
	}
	if t := b.Val("templateId"); t != nil {
		if _, ok := t.(string); !ok {
			v.Err("templateId", "V_TYPE", "templateId must be a string or null")
		}
	}
	tagSlugs, managers := fld(b, "tagSlugs"), fld(b, "managerAccountUids")
	if tagSlugs == doc.Undefined {
		tagSlugs = []any{}
	}
	if managers == doc.Undefined {
		managers = []any{}
	}
	if err := checkTagsManagers(v, tagSlugs, managers); err != nil {
		return nil, err
	}
	core.CheckRequestedSlug(v, fld(b, "slug"))
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	tagList, _ := stringsOf(tagSlugs, false)
	if err := cloud.Need(c, "deployments", "write", &cloud.AccessTarget{DeploymentID: "", TagSlugs: tagList}); err != nil {
		return nil, err
	}
	if b.Has("message") {
		m, ok := b.StrOK("message")
		if !ok || doc.JSTrim(m) == "" || doc.JSLen(m) > 2000 {
			return nil, cloud.E.Bad("message", "V_COMMIT_MESSAGE", "message must be 1–2000 characters")
		}
	}
	// §21 #104 create from a template / deployment / agent branch (templateId = the legacy shortcut for a template head)
	src, err := core.CreateSource(c, fld(b, "from"), fld(b, "templateId"))
	if err != nil {
		return nil, err
	}
	var tpl *doc.Map
	if src != nil {
		tpl = src.R.Tpl
	}
	ref := cloud.Col.Deployments().Doc()
	id := ref.ID()
	now := doc.NowISO()
	var draft *doc.Map
	if src != nil {
		draft = cloud.Clone(src.Config).(*doc.Map)
	} else {
		draft = shared.EmptyConfig()
	}
	fromDeployment := src != nil && src.R.Ref.Val("kind") != "template"
	wantCopy := fromDeployment && b.Val("copySecretValues") == true
	copyValues := wantCopy && cloud.Has(c, "deployment.secrets", "read", cloud.TargetOf(src.R.Dep))
	if wantCopy && !copyValues {
		return nil, cloud.E.Forbidden("copySecretValues needs deployment.secrets.read on the source deployment")
	}
	secrets := []any{}
	for _, s := range mapsIn(draft.Arr("secrets")) {
		switch gen := s.Map("generate"); {
		case s.Truthy("generate"):
			secrets = append(secrets, s.Shallow().Set("value", cloud.GenerateSecret(gen.Str("charset"), int(gen.Int("length")))).Set("rotatedAt", now))
		case copyValues && s.Truthy("value"):
			secrets = append(secrets, s.Shallow().Set("rotatedAt", fldOr(s, "rotatedAt", now)))
		default:
			secrets = append(secrets, s.Shallow().Set("value", nil).Set("rotatedAt", nil))
		}
	}
	draft.Set("secrets", secrets)
	key := core.NewAccessKey()
	me := c.Account.UID
	var srcTemplate any
	if tpl != nil {
		srcTemplate = doc.Obj("templateId", fld(tpl, "templateId"), "templateVersion", fld(tpl, "version"), "snapshot", tpl.Clone().Spread(cloud.Clone(src.Config).(*doc.Map)), "copiedAt", now, "baseVersion", fld(tpl, "version"), "baseSnapshot", cloud.Clone(src.Config), "lastSyncedAt", nil, "lastSyncedBy", nil, "skipped", []any{})
	}
	dep := doc.Obj(
		"deploymentId", id, "slug", "", "label", doc.JSTrim(b.Str("label")), "image", fldOr(b, "image", nil), "tagSlugs", tagSlugs, "managerAccountUids", managers,
		// §21 #94 detached deep copy: the draft above + the snapshot / base below are copies; nothing reads the template later
		// §21 #104: the origin link lives in branch.origin (+ a merge base record); srcTemplate is legacy data (read by nothing new)
		"srcTemplate", srcTemplate, "draft", draft, "draftVersion", 0.0, "head", nil,
		"accessKey", key.Grouped, // §21 #84: stored (revealAccessKey); never in views / the agent cloud view
		"accessKeyHash", key.Hash, "accessKeyVersion", 1.0, "accessKeyRotatedAt", now, "accessKeyRotatedBy", me, "agentEpoch", 1.0, "cloudActivitySeq", 0.0, "deleted", false,
		"createdAt", now, "createdBy", me, "updatedAt", now, "updatedBy", me, "_cloudLastHash", nil,
	)
	// §21 #104 the create commit (seq 1): "created from <label>@<seq>"
	cat, err := core.LoadCatalog()
	if err != nil {
		return nil, err
	}
	config, err := shared.NormalizeConfig(cloud.Clone(draft), core.ImageVolumes(cat))
	if err != nil {
		return nil, err
	}
	dep.Set("draft", config)
	catalog, err := core.BuildRevisionCatalog(config, cat)
	if err != nil {
		return nil, err
	}
	var origin, source, mergeBase *doc.Map
	if src != nil {
		origin, source, mergeBase = core.OriginOf(src)
	}
	var message string
	if m, ok := b.StrOK("message"); ok && doc.JSTrim(m) != "" {
		message = doc.JSTrim(m)
	} else if src != nil {
		message = shared.CreateMessage(src.R.Label, fldOr(src.Commit, "seq", nil))
	} else {
		message = "created empty"
	}
	branchRef := doc.Obj("kind", "deployment", "deploymentId", id)
	commit, err := shared.MakeCommit(doc.Obj("commitId", core.NewCommitId(), "branch", branchRef, "parent", nil, "kind", "create", "message", message, "author", core.PersonAuthor(c), "createdAt", now, "config", config, "catalog", catalog, "source", mapOrNull(source)))
	if err != nil {
		return nil, err
	}
	external, err := core.StoreCommitBody(commit)
	if err != nil {
		return nil, err
	}
	blocked := len(core.SafeValidate(config, core.ValidationCtx("publish", id, cat, nil, nil)).Errors) > 0
	dep.Set("branch", doc.Obj("head", shared.CommitRef(commit), "origin", mapOrNull(origin), "commitCount", 1.0))
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		slug, err := core.NewSlug(tx, "deployment", dep.Str("label"), fld(b, "slug")) // §21 #106 (reads before writes)
		if err != nil {
			return err
		}
		dep.Set("slug", slug)
		prep, err := core.PrepCompat(tx, id)
		if err != nil {
			return err
		}
		log := core.NewCloudLog(id, dep)
		summary := `Deployment "` + dep.Str("label") + `" created`
		data := doc.Obj("label", dep.Val("label"))
		if tpl != nil {
			data.Set("templateId", fld(tpl, "templateId"))
		}
		if src != nil {
			summary += " from " + src.Label
			data.Set("from", source)
		}
		log.Add(tx, core.CloudEntry{Type: "deployment.created", Summary: summary, Data: data, Actor: me})
		compat := core.WriteCompat(tx, dep, commit, prep, log, blocked, me)
		commit.Set("data", commit.Map("data").Shallow().Set("compat", mapOrNull(compat)))
		core.WriteCommit(tx, commit, external)
		if src != nil {
			first, _, _ := strings.Cut(message, "\n")
			log.Add(tx, core.CloudEntry{Type: "branch.created", Summary: "Branch created from " + src.Label, Data: doc.Obj("branch", "deployment:"+id, "commitId", commit.Val("commitId"), "seq", 1.0, "message", first, "from", source), Actor: me})
			updatedBy := me
			if c.Account.Email != nil {
				updatedBy = *c.Account.Email
			}
			core.SetMergeBase(tx, branchRef, mergeBase.Shallow().Set("updatedBy", updatedBy).Set("targetCommitId", commit.Val("commitId")))
		}
		epoch := 1.0
		if compat != nil {
			epoch = 2.0
		}
		tx.Create(ref, cloud.EncodeDeployment(dep.Shallow().Spread(log.DepUpdate()).Set("agentEpoch", epoch)))
		dep.Spread(log.DepUpdate()).Set("agentEpoch", epoch)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("deployment", core.DeploymentView(dep), "accessKey", key.Grouped, "hideAfterSec", shared.REVEAL_SECONDS), nil
}

func getDeployment(c *cloud.Ctx) (any, error) {
	d, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	return core.DeploymentView(d), nil
}

func patchDeployment(c *cloud.Ctx) (any, error) {
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"label", "image", "tagSlugs", "managerAccountUids"}, "deploymentId", "slug", "draft", "draftVersion", "head", "agentEpoch", "deleted", "accessKeyVersion"); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	if b.Has("label") {
		cloud.VLabel(v, b.Val("label"), "label", cloud.Limits["label"], true)
	}
	core.VIconFormat(v, fld(b, "image"), "image", true)
	if v.OK() {
		if err := core.VIconRefs(v, []core.IconField{{Path: "image", Value: fld(b, "image")}}); err != nil {
			return nil, err
		}
	}
	if err := checkTagsManagers(v, fld(b, "tagSlugs"), fld(b, "managerAccountUids")); err != nil {
		return nil, err
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	cur, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, cur, "deployments", "write"); err != nil {
		return nil, err
	}
	if b.Has("tagSlugs") {
		t := *cloud.TargetOf(cur)
		t.TagSlugs, _ = stringsOf(b.Val("tagSlugs"), false)
		if err := cloud.Need(c, "deployments", "write", &t); err != nil {
			return nil, err
		}
	}
	ref := cloud.Col.Deployments().Doc(cur.Str("deploymentId"))
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (any, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		dep := cloud.DecodeDeployment(s.Data())
		if dep.Truthy("deleted") {
			return nil, cloud.E.NotFound("deployment not found")
		}
		changed := []any{}
		names := []string{}
		upd := doc.New()
		for _, k := range []string{"label", "image", "tagSlugs", "managerAccountUids"} {
			if !b.Has(k) {
				continue
			}
			val := b.Val(k)
			if k == "label" {
				val = doc.JSTrim(b.Str("label"))
			}
			if !dep.Has(k) || doc.MarshalString(val) != doc.MarshalString(dep.Val(k)) {
				changed = append(changed, k)
				names = append(names, k)
				upd.Set(k, val)
			}
		}
		if len(changed) == 0 {
			return core.DeploymentView(dep), nil
		}
		log := core.NewCloudLog(dep.Str("deploymentId"), dep)
		log.Add(tx, core.CloudEntry{Type: "deployment.updated", Summary: "Deployment updated (" + strings.Join(names, ", ") + ")", Data: doc.Obj("changed", changed), Actor: c.Account.UID})
		upd.Spread(log.DepUpdate()).Set("updatedAt", doc.NowISO()).Set("updatedBy", c.Account.UID)
		tx.Update(ref, upd)
		return core.DeploymentView(dep.Shallow().Spread(upd)), nil
	})
}

func deleteDeployment(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployments", "write"); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if b.Val("confirm") != dep.Val("label") {
		return nil, cloud.E.Bad("confirm", "V_FORMAT", "confirm must equal the deployment label")
	}
	ref := cloud.Col.Deployments().Doc(dep.Str("deploymentId"))
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		s, err := tx.Get(ref)
		if err != nil {
			return err
		}
		cur := cloud.DecodeDeployment(s.Data())
		if cur.Truthy("deleted") {
			return cloud.E.NotFound("deployment not found")
		}
		log := core.NewCloudLog(cur.Str("deploymentId"), cur)
		log.Add(tx, core.CloudEntry{Type: "deployment.deleted", Summary: `Deployment "` + cur.Str("label") + `" deleted`, Data: doc.Obj("label", fld(cur, "label")), Actor: c.Account.UID})
		tx.Update(ref, doc.Obj("deleted", true, "agentEpoch", cur.Num("agentEpoch")+1).Spread(log.DepUpdate()).Set("updatedAt", doc.NowISO()).Set("updatedBy", c.Account.UID))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("deleted", true), nil
}

const commitHint = "POST /v1/deployments/{deploymentId}/commits { message, baseCommitId, config }"

// goneNoWorkingCopy: §21 #105 removed: no server working copy, the editors commit.
func goneNoWorkingCopy(*cloud.Ctx) (any, error) {
	return nil, cloud.E.Gone("the deployment config is changed only by a commit (§21 #105): there is no draft / working copy any more", doc.Obj("hint", commitHint))
}

func secretValue(draft *doc.Map, slug string) (any, bool) {
	s := findBy(draft.Arr("secrets"), "secretSlug", slug)
	if s == nil {
		return nil, false
	}
	v := s.Val("value")
	return v, v != nil && v != ""
}

func revealSecret(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.secrets", "read"); err != nil {
		return nil, err
	}
	slug := c.Params["secretSlug"]
	if _, ok := secretValue(dep.Map("draft"), slug); !ok {
		return nil, cloud.E.NotFound("the secret has no value")
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("secretSlug", slug)}
	// the activity is written in the same transaction that re-reads the value (no reveal without its log entry)
	ref := cloud.Col.Deployments().Doc(dep.Str("deploymentId"))
	me := c.Account.UID
	value, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (any, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		cur := cloud.DecodeDeployment(s.Data())
		val, ok := secretValue(cur.Map("draft"), slug)
		if !ok {
			return nil, cloud.E.NotFound("the secret has no value")
		}
		log := core.NewCloudLog(cur.Str("deploymentId"), cur)
		log.Add(tx, core.CloudEntry{Type: "secret.revealed", Summary: "Secret " + slug + " revealed in the cloud", Data: doc.Obj("secretSlug", slug, "actor", me, "where", "cloud"), Actor: me})
		tx.Update(ref, log.DepUpdate())
		return val, nil
	})
	if err != nil {
		return nil, err
	}
	return doc.Obj("value", value, "hideAfterSec", shared.REVEAL_SECONDS), nil
}

// regenerateSecret: §21 #105 removed: "Generate new value" is a pending change (generateNow) written by the next commit.
func regenerateSecret(*cloud.Ctx) (any, error) {
	return nil, cloud.E.Gone("regenerate is a pending change now (§21 #105): set generateNow on the secret and commit — the commit generates the value", doc.Obj("hint", "POST /v1/deployments/{deploymentId}/commits { message, baseCommitId, config: { secrets: [{ …, generateNow: true }] } }"))
}

func getDiff(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "read"); err != nil {
		return nil, err
	}
	// §21 #105 (legacy endpoint): there is no working copy: the diff is what the HEAD commit changed vs its parent,
	// blockers / warnings = the head's publish-level validation (old clients see the current release)
	id := dep.Str("deploymentId")
	ref := doc.Obj("kind", "deployment", "deploymentId", id)
	var head, parent *doc.Map
	if h := dep.Map("branch").Map("head"); h != nil {
		if head, err = core.LoadCommit(ref, h.Str("commitId"), nil, true); err != nil {
			return nil, err
		}
	}
	if head.Truthy("parentId") {
		if parent, err = core.LoadCommit(ref, head.Str("parentId"), nil, true); err != nil {
			return nil, err
		}
	}
	cat0, err := core.LoadCatalog()
	if err != nil {
		return nil, err
	}
	detected, err := core.LoadDetectedHosts(id)
	if err != nil {
		return nil, err
	}
	blobContents, err := core.SeedBlobContents(dep.Map("draft"))
	if err != nil {
		return nil, err
	}
	cat := core.WithSiteImages(cat0, head.Map("catalog")) // §21 #113
	var base *doc.Map
	if parent != nil {
		base = doc.Obj("config", parent.Val("config"), "catalog", fldOr(parent, "catalog", emptyCatalog()))
	}
	chk := PublishCheck(dep, base, cat, detected, blobContents)
	var headView any
	if head != nil {
		headView = doc.Obj("revId", fld(head, "commitId"), "seq", fld(head, "seq"), "reportedAt", fld(head, "createdAt"))
	}
	return doc.Obj("head", headView, "draftVersion", fld(dep, "draftVersion"), "changes", chk.Changes, "publishable", len(chk.Blockers) == 0, "blockers", chk.Blockers, "warnings", chk.Warnings, "pending", []any{}), nil
}

// publish: §21 #104 removed (commit the working copy instead).
func publish(*cloud.Ctx) (any, error) {
	return nil, cloud.E.Gone("publish was replaced by commits (§21 #104): commit your changes — the newest commit is what the sites run", doc.Obj("hint", commitHint))
}

func resetDraftToHead(*cloud.Ctx) (any, error) {
	return nil, cloud.E.Gone("reset-to-head is gone (§21 #105): there is no working copy — pending changes live in the browser and are discarded there", doc.Obj("hint", "discard in the browser"))
}

func listProposals(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "read"); err != nil {
		return nil, err
	}
	limit, err := core.ClampLimit(c, 20, 200)
	if err != nil {
		return nil, err
	}
	q, err := cloud.Col.Proposals(dep.Str("deploymentId")).OrderBy("revId", driver.Desc).Limit(limit).Get()
	if err != nil {
		return nil, err
	}
	items := make([]any, len(q.Docs))
	for i, d := range q.Docs {
		items[i] = d.Data()
	}
	return doc.Obj("items", items), nil
}

func listRevisions(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "read"); err != nil {
		return nil, err
	}
	limit, err := core.ClampLimit(c, 100, 500)
	if err != nil {
		return nil, err
	}
	id := dep.Str("deploymentId")
	q := cloud.Col.Revisions(id).OrderBy("seq", driver.Desc).Select("revId", "seq", "parentRevId", "origin", "actor", "createdAt", "hash", "summary", "kind")
	if cursor := c.Query.Get("cursor"); cursor != "" {
		s, err := cloud.Col.Revisions(id).Doc(cursor).Get()
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, cloud.E.Bad("cursor", "V_FORMAT", "bad cursor")
		}
		q = q.StartAfterSnap(s)
	}
	r, err := q.Limit(limit + 1).Get()
	if err != nil {
		return nil, err
	}
	docs := r.Docs[:min(limit, len(r.Docs))]
	items := make([]any, len(docs))
	for i, d := range docs {
		items[i] = d.Data()
	}
	var cursor any
	if len(r.Docs) > limit {
		cursor = docs[len(docs)-1].ID()
	}
	return doc.Obj("items", items, "cursor", cursor), nil
}

func getRevision(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "read"); err != nil {
		return nil, err
	}
	r, err := core.LoadRevision(dep.Str("deploymentId"), c.Params["revId"], nil)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, cloud.E.NotFound("revision not found")
	}
	return core.RevisionView(cloud.HumanRevision(r)), nil
}
