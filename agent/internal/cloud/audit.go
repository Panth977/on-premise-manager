package cloud

import (
	"errors"
	"regexp"
	"strings"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// AuditHint is what a handler may set on c.Audit to refine the entry.
type AuditHint struct {
	Action     string
	Target     string
	TargetType string
	Data       *doc.Map
	// Skip: no entry at all
	Skip bool
}

type auditSpec struct{ action, targetType string }

var deploymentInternal = regexp.MustCompile(`^/v1/deployments/[^/]+/.+`)

// isAudited: human non-GET request outside the deployment scope (and not exempt), or a deployment credential op.
func isAudited(op, method, auth, path string) bool {
	if auth != "human" || method == "GET" || has(auditExemptOps, op) {
		return false
	}
	return !deploymentInternal.MatchString(path) || has(auditDeploymentOps, op)
}

func specOf(op string) auditSpec {
	if s, ok := auditOps[op]; ok {
		return s
	}
	return auditSpec{op, "system"}
}

func bodyOf(c *Ctx) *doc.Map {
	if c.Def.Body != "json" {
		return doc.New()
	}
	v, err := c.JSON()
	if err != nil {
		return doc.New()
	}
	if m, ok := v.(*doc.Map); ok {
		return m
	}
	return doc.New()
}

// astr: typeof v === 'string' && v ? v : null ("" = null).
func astr(v any) string {
	s, _ := v.(string)
	return s
}

// rget: a nested field of a handler result ("undefined" for anything else).
func rget(o *doc.Map, keys ...string) any {
	var cur any = o
	for _, k := range keys {
		m, ok := cur.(*doc.Map)
		if !ok || m == nil {
			return nil
		}
		cur = m.Val(k)
	}
	return cur
}

// resultMap: a handler result as a document (nil for *Reply and for values that are not objects).
func resultMap(result any) *doc.Map {
	if result == nil {
		return nil
	}
	if _, ok := result.(*Reply); ok {
		return nil
	}
	v, err := doc.From(result)
	if err != nil {
		return nil
	}
	m, _ := v.(*doc.Map)
	return m
}

// docPathOf: document path of the target ("" = no document). result is set after success.
func docPathOf(op string, c *Ctx, r *doc.Map) string {
	p := c.Params
	b := bodyOf(c)
	id := func(prefix string, keys ...string) string {
		if s := astr(rget(r, keys...)); s != "" {
			return prefix + s
		}
		return ""
	}
	switch op {
	case "createRegistry":
		return id("registries/", "registryId")
	case "patchRegistry", "deleteRegistry", "testRegistry", "revealRegistryPassword":
		return "registries/" + p["registryId"]
	case "createService":
		return id("services/", "serviceSlug")
	case "patchService", "deleteService":
		return "services/" + p["serviceSlug"]
	case "registerImage":
		v := astr(rget(r, "version"))
		if v == "" {
			v = astr(b.Val("version"))
		}
		if v != "" && r != nil {
			return "services/" + p["serviceSlug"] + "/images/" + v
		}
		return ""
	case "patchImage", "deleteImage", "retryImport":
		return "services/" + p["serviceSlug"] + "/images/" + p["version"]
	case "createNotFoundPage":
		return id("notFoundPages/", "notFoundPageId")
	case "deleteNotFoundPage":
		return "notFoundPages/" + p["notFoundPageId"]
	case "createTemplate":
		return id("templates/", "templateId")
	case "patchTemplate", "deleteTemplate", "commitTemplate", "discardTemplateWorking":
		return "templates/" + p["templateId"]
	case "createTag":
		return id("tags/", "tagSlug")
	case "patchTag", "deleteTag":
		return "tags/" + p["tagSlug"]
	case "createAccount":
		return id("accounts/", "account", "uid")
	case "patchAccount", "disableAccount", "resetAccountPassword", "setAccountPassword", "revealAccountPassword", "revealAccountResetLink":
		return "accounts/" + p["uid"]
	case "createDeployment":
		return id("deployments/", "deployment", "deploymentId")
	case "patchDeployment", "deleteDeployment", "commitDeployment", "discardDeploymentWorking":
		return "deployments/" + p["deploymentId"]
	case "createUpload":
		return id("uploads/", "uploadId")
	case "completeUpload":
		return "uploads/" + p["uploadId"]
	case "uploadIcon":
		// a dedupe hit (existing: true) changes nothing: no document diff (target set by the handler)
		if s := astr(rget(r, "icon", "iconId")); s != "" && rget(r, "existing") != true {
			return "icons/" + s
		}
		return ""
	case "deleteIcon":
		return "icons/" + p["iconId"]
	case "patchGithubMirror", "testGithubMirror", "revealGithubToken", "syncGithubMirror", "resyncGithubMirror":
		return "githubMirror/config"
	}
	return ""
}

func fallbackTarget(op string, c *Ctx, r *doc.Map, spec auditSpec) string {
	b := bodyOf(c)
	switch op {
	case "inspectImage":
		return "services/" + c.Params["serviceSlug"] + "/images"
	case "registerImage":
		if s := astr(b.Val("version")); s != "" {
			return "services/" + c.Params["serviceSlug"] + "/images/" + s
		}
		return "services/" + c.Params["serviceSlug"] + "/images"
	case "uploadBlob":
		if s := astr(rget(r, "sha256")); s != "" {
			return "blobs/" + s
		}
		return "blobs"
	case "createTag":
		if s := astr(b.Val("tagSlug")); s != "" {
			return "tags/" + s
		}
		return "tags"
	case "createAccount":
		return "accounts"
	case "createRegistry":
		return "registries"
	case "createService":
		if s := astr(b.Val("serviceSlug")); s != "" {
			return "services/" + s
		}
		return "services"
	case "createNotFoundPage":
		return "notFoundPages"
	case "createTemplate":
		return "templates"
	case "createDeployment":
		return "deployments"
	case "createUpload":
		return "uploads"
	case "uploadIcon":
		return "icons"
	}
	if has(auditDeploymentOps, op) {
		return "deployments/" + c.Params["deploymentId"]
	}
	return spec.targetType
}

// ReadAuditDoc reads a target document as the API sees it (codecs applied, internal "_" fields removed).
func ReadAuditDoc(path string) (*doc.Map, error) {
	s, err := DB().Doc(path).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, nil
	}
	raw := s.Data()
	top, _, _ := strings.Cut(path, "/")
	switch top {
	case "githubMirror":
		// the token is a credential: only "set / not set" reaches the audit diff; lastTest is noise
		p := Pub(raw)
		var tok any
		if p.Truthy("token") {
			tok = "***"
		}
		return p.Without("token", "lastTest").Set("token", tok), nil
	case "templates":
		return Pub(DecodeTemplate(raw)), nil
	case "deployments":
		return Pub(DecodeDeployment(raw).Without("accessKey")), nil // the plaintext key is never diffed
	}
	return Pub(raw), nil
}

func defaultData(op string, c *Ctx, r *doc.Map) *doc.Map {
	b := bodyOf(c)
	nz := func(v any, def any) any {
		if v == nil {
			return def
		}
		return v
	}
	switch op {
	case "testRegistry":
		if r != nil {
			return doc.Obj("ok", r.Val("ok"), "message", r.Val("message"))
		}
	case "revealRegistryPassword":
		return doc.Obj("registryId", c.Params["registryId"])
	case "inspectImage":
		m := doc.Obj("reference", b.Val("reference"), "registryId", b.Val("registryId"), "platform", nz(b.Val("platform"), "linux/amd64"))
		if r != nil {
			m.Set("digest", undefOr(r, "digest")).Set("layers", undefOr(r, "layers")).Set("sizeBytes", undefOr(r, "sizeBytes"))
		}
		return m
	case "deleteTag":
		if r != nil {
			return doc.Obj("removedFrom", undefOr(r, "removedFrom"))
		}
	case "createInstallToken":
		if s, ok := b.StrOK("siteSlug"); ok {
			return doc.Obj("siteSlug", s)
		}
	case "revealInstallCommand":
		return doc.Obj("tokenHash", c.Params["tokenHash"])
	case "revealUpgradeCommand", "regenerateUpgradeCommand":
		return doc.Obj("siteSlug", c.Params["siteSlug"])
	case "revealSecret", "regenerateSecret":
		return doc.Obj("secretSlug", c.Params["secretSlug"])
	case "uploadBlob":
		if r != nil {
			return doc.Obj("sha256", undefOr(r, "sha256"), "sizeBytes", undefOr(r, "sizeBytes"))
		}
		return doc.Obj("sizeBytes", len(c.Body))
	case "createUpload":
		return doc.Obj("purpose", b.Val("purpose"), "sizeBytes", b.Val("sizeBytes"), "sha256", b.Val("sha256"))
	case "uploadIcon":
		var ct, name any
		if v, ok := c.Headers["content-type"]; ok {
			ct = v
		}
		if v, ok := c.Query.Lookup("name"); ok {
			name = v
		}
		return doc.Obj("contentType", ct, "sizeBytes", len(c.Body), "name", name)
	}
	return doc.New()
}

func undefOr(m *doc.Map, k string) any {
	if v, ok := m.Get(k); ok {
		return v
	}
	return doc.Undefined
}

type auditPre struct {
	docPath string
	before  *doc.Map
}

// auditBegin: before the handler, read the target document (never fails).
func auditBegin(c *Ctx) *auditPre {
	p := docPathOf(c.Op, c, nil)
	var before *doc.Map
	if p != "" {
		var err error
		if before, err = ReadAuditDoc(p); err != nil {
			driver.Log.Error("audit: before-read failed", "requestId", c.RequestID, "docPath", p, "error", err.Error())
		}
	}
	return &auditPre{p, before}
}

func strOrNull(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// auditFinish: after the handler (success or failure) build + write the entry. Never fails. 401 (no actor) → nothing.
func auditFinish(c *Ctx, pre *auditPre, result any, herr error, okStatus int) {
	defer func() {
		if p := recover(); p != nil {
			driver.Log.Error("audit: write failed", "requestId", c.RequestID, "op", c.Op, "error", p)
		}
	}()
	var ae *ApiError
	isAPI := errors.As(herr, &ae)
	var actor *AuditActor
	if isAPI {
		actor = ae.AuditActor
	}
	uid := ""
	var email any
	if c.Account != nil {
		uid = c.Account.UID
		email = strOrNull(c.Account.Email)
	} else if actor != nil {
		uid = actor.UID
		email = strOrNull(actor.Email)
	}
	if uid == "" {
		return
	}
	spec := specOf(c.Op)
	hint := c.Audit
	if hint == nil {
		hint = &AuditHint{}
	}
	if hint.Skip {
		return
	}
	ok := herr == nil
	status := okStatus
	var errorCode, errorMessage any
	if !ok {
		status, errorCode, errorMessage = 500, "internal", "internal error"
		if isAPI {
			status, errorCode, errorMessage = ae.Status(), ae.Code, doc.JSSlice(ae.Message, 500)
		}
	}
	var r *doc.Map
	if ok {
		r = resultMap(result)
	}
	docPath := ""
	if pre != nil {
		docPath = pre.docPath
	}
	if ok && docPath == "" {
		docPath = docPathOf(c.Op, c, r)
	}
	var before, after *doc.Map
	if pre != nil {
		before = pre.before
	}
	if docPath != "" && pre != nil {
		var err error
		if after, err = ReadAuditDoc(docPath); err != nil {
			driver.Log.Error("audit: after-read failed", "requestId", c.RequestID, "docPath", docPath, "error", err.Error())
			after = before
		}
	}
	if pre == nil {
		before, after = nil, nil
	}
	var ba, aa any
	if before != nil {
		ba = before
	}
	if after != nil {
		aa = after
	}
	diff := AuditDiff(ba, aa)
	data := defaultData(c.Op, c, r).Shallow()
	if hint.Data != nil {
		data.Spread(hint.Data)
	}
	if c.Op == "completeUpload" && (after != nil || before != nil) {
		u := after
		if u == nil {
			u = before
		}
		d2 := doc.Obj("partCount", undefOr(u, "partCount"), "purpose", undefOr(u, "purpose"), "sizeBytes", undefOr(u, "sizeBytes"), "sha256", undefOr(u, "sha256"), "deploymentId", u.Val("deploymentId"), "transferId", u.Val("transferId"))
		data = d2.Spread(data)
	}
	action := hint.Action
	if action == "" {
		action = spec.action
	}
	tt := hint.TargetType
	if tt == "" {
		tt = spec.targetType
	}
	target := hint.Target
	if target == "" {
		target = docPath
	}
	if target == "" {
		target = fallbackTarget(c.Op, c, r, spec)
	}
	outcome := "ok"
	if !ok {
		outcome = "error"
	}
	var ip, ua any
	if c.IP != "" {
		ip = c.IP
	}
	if s := doc.JSSlice(c.Headers["user-agent"], 300); s != "" {
		ua = s
	}
	_, err := WriteAuditEntry(doc.Obj("op", c.Op, "actor", uid, "actorEmail", email, "action", action, "outcome", outcome, "status", status, "errorCode", errorCode, "errorMessage", errorMessage, "targetType", tt, "target", target, "requestId", c.RequestID, "ip", ip, "userAgent", ua, "changes", diff.Changes, "changesTotal", diff.Total, "data", data))
	if err != nil {
		driver.Log.Error("audit: write failed", "requestId", c.RequestID, "op", c.Op, "error", err.Error())
	}
}

// WriteAuditEntry writes one entry (data redacted + capped; changes must come from AuditDiff). e has the fields of
// Omit<AuditLogEntry, 'auditId' | 'ts'>, in that order.
func WriteAuditEntry(e *doc.Map) (*doc.Map, error) {
	ref := Col.AuditLog().Doc()
	var src any = e.Map("data")
	if e.Map("data") == nil {
		src = doc.New()
	}
	data := RedactSecrets(src).(*doc.Map)
	for _, k := range data.Keys() {
		v, _ := data.Get(k)
		data.Set(k, CapAuditValue(v, AuditMaxValueBytes))
	}
	entry := doc.Obj("auditId", ref.ID(), "ts", doc.NowISO()).Spread(e).Set("data", data)
	if err := ref.Create(EncodeAudit(entry)); err != nil {
		return nil, err
	}
	return entry, nil
}

// AuditSystemOpts of AuditSystem. Before / After: Undefined by default (no diff); set HasDiff to diff them.
type AuditSystemOpts struct {
	Job, Action, TargetType, Target string
	OK                              bool
	ErrorMessage                    *string
	HasDiff                         bool
	Before, After                   *doc.Map
	Data                            *doc.Map
}

// AuditSystem writes a system entry (actor "system"), e.g. image import results and the daily job. Never fails.
func AuditSystem(o AuditSystemOpts) {
	diff := AuditDiffResult{Changes: []any{}}
	if o.HasDiff {
		var a, b any
		if o.Before != nil {
			a = o.Before
		}
		if o.After != nil {
			b = o.After
		}
		diff = AuditDiff(a, b)
	}
	var code, msg any
	outcome, status := "ok", 200
	if !o.OK {
		outcome, status, code = "error", 500, "internal"
		if o.ErrorMessage != nil {
			msg = doc.JSSlice(*o.ErrorMessage, 500)
		}
	}
	data := o.Data
	if data == nil {
		data = doc.New()
	}
	_, err := WriteAuditEntry(doc.Obj("op", "system:"+o.Job, "actor", "system", "actorEmail", nil, "action", o.Action, "outcome", outcome, "status", status, "errorCode", code, "errorMessage", msg, "targetType", o.TargetType, "target", o.Target, "requestId", nil, "ip", nil, "userAgent", nil, "changes", diff.Changes, "changesTotal", diff.Total, "data", data))
	if err != nil {
		driver.Log.Error("audit: system write failed", "job", o.Job, "action", o.Action, "error", err.Error())
	}
}

// EncodeAudit is encodeAudit.
func EncodeAudit(e *doc.Map) *doc.Map {
	rest := e.Without("changes", "data")
	var ch any = e.Val("changes")
	if ch == nil {
		ch = []any{}
	}
	var data any = e.Val("data")
	if data == nil {
		data = doc.New()
	}
	return rest.Set("_changes", doc.MarshalString(ch)).Set("_data", doc.MarshalString(data)).Set("_actionKeys", AuditActionKeys(e.Str("action")))
}

// DecodeAudit is decodeAudit: stored doc → entry.
func DecodeAudit(d *doc.Map) *doc.Map {
	r := d.Without("_changes", "_data", "_actionKeys")
	changes := any([]any{})
	if s, ok := d.StrOK("_changes"); ok {
		changes = mustParse(s)
	}
	strZ := func(k, def string) any {
		if v := r.Val(k); v != nil {
			return v
		}
		return def
	}
	null := func(k string) any { return r.Val(k) }
	tt := r.Val("targetType")
	if tt == nil {
		tt = "registry"
		if strings.HasPrefix(r.Str("target"), "accounts/") {
			tt = "account"
		}
	}
	status := any(float64(200))
	if v := r.Val("status"); v != nil {
		status = v
	}
	total := any(float64(0))
	if l, ok := changes.([]any); ok {
		total = float64(len(l))
	}
	if v := r.Val("changesTotal"); v != nil {
		total = v
	}
	var data any = doc.New()
	if s, ok := d.StrOK("_data"); ok {
		data = mustParse(s)
	} else if v := r.Val("data"); v != nil {
		data = v
	}
	return doc.Obj("auditId", undefOr(r, "auditId"), "ts", undefOr(r, "ts"), "op", strZ("op", ""), "actor", strZ("actor", ""), "actorEmail", null("actorEmail"), "action", strZ("action", ""), "outcome", strZ("outcome", "ok"), "status", status, "errorCode", null("errorCode"), "errorMessage", null("errorMessage"), "targetType", tt, "target", strZ("target", ""), "requestId", null("requestId"), "ip", null("ip"), "userAgent", null("userAgent"), "changes", changes, "changesTotal", total, "data", data)
}
