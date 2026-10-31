package routes

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// Accounts + root audit log (routes/accounts.ts). sendMail is cloud.SendMail, clampLimit is core.ClampLimit.

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"listAccounts":           listAccounts,
		"createAccount":          createAccount,
		"getAccount":             getAccount,
		"patchAccount":           patchAccount,
		"disableAccount":         disableAccount,
		"resetAccountPassword":   resetAccountPassword,
		"revealAccountResetLink": revealAccountResetLink,
		"setAccountPassword":     setAccountPassword,
		"revealAccountPassword":  revealAccountPassword,
		"listAuditLog":           listAuditLog,
	})
}

// Small helpers of these route files.

// fld is m[k] (doc.Undefined when missing).
func fld(m *doc.Map, k string) any {
	if v, ok := m.Get(k); ok {
		return v
	}
	return doc.Undefined
}

// fldOr is m[k] ?? def.
func fldOr(m *doc.Map, k string, def any) any {
	if v, ok := m.Get(k); ok && v != nil && v != doc.Undefined {
		return v
	}
	return def
}

// mapOrNull: a nil map is JSON null.
func mapOrNull(m *doc.Map) any {
	if m == nil {
		return nil
	}
	return m
}

// mapsIn is the object items of a list.
func mapsIn(l []any) []*doc.Map {
	out := make([]*doc.Map, 0, len(l))
	for _, x := range l {
		if m, ok := x.(*doc.Map); ok {
			out = append(out, m)
		}
	}
	return out
}

func strList(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

func isoNow(t int64) string { return doc.ISO(time.UnixMilli(t)) }

// findBy is list.find(x => x[key] === val).
func findBy(l []any, key, val string) *doc.Map {
	for _, m := range mapsIn(l) {
		if m.Val(key) == val {
			return m
		}
	}
	return nil
}

// reActivityFilter is the query check of an audit action and an activity type.
var reActivityFilter = regexp.MustCompile(`^[a-z]+(-[a-z]+)*(\.[a-z-]+)*\.?$`)

func queryIssues(msgs []string) []cloud.Issue {
	out := make([]cloud.Issue, len(msgs))
	for i, m := range msgs {
		out[i] = cloud.Issue{Path: "query", Code: "V_FORMAT", Message: m, Severity: "error"}
	}
	return out
}

func validISO(s string) bool {
	_, ok := doc.ParseISO(s)
	return ok
}

var (
	collOnce sync.Once
	collMu   sync.Mutex
	coll     *collate.Collator
)

// localeCompare is a.localeCompare(b).
func localeCompare(a, b string) int {
	collOnce.Do(func() { coll = collate.New(language.AmericanEnglish) })
	collMu.Lock()
	defer collMu.Unlock()
	return coll.CompareString(a, b)
}

const (
	RESET_LINK_TTL_MS       = 3600_000
	ACCOUNT_PASSWORD_LENGTH = 20
)

func existingRefs() (tags, deps []string, err error) {
	t, err := cloud.Col.Tags().Select().Get()
	if err != nil {
		return nil, nil, err
	}
	d, err := cloud.Col.Deployments().Select().Get()
	if err != nil {
		return nil, nil, err
	}
	tags, deps = []string{}, []string{}
	for _, s := range t.Docs {
		tags = append(tags, s.ID())
	}
	for _, s := range d.Docs {
		deps = append(deps, s.ID())
	}
	return tags, deps, nil
}

func profileChecks(v *cloud.V, b *doc.Map, create bool) {
	if create || b.Has("name") {
		cloud.VLabel(v, fld(b, "name"), "name", cloud.Limits["label"], true)
	}
	if b.Has("phone") && b.Val("phone") != nil {
		if s, ok := b.StrOK("phone"); !ok || !shared.RE.E164.MatchString(s) {
			v.Err("phone", "V_FORMAT", "phone must be E.164 (e.g. +919876543210) or null")
		}
	}
	core.VIconFormat(v, fld(b, "img"), "img", true)
}

func adminCountExcept(uid string) (int, error) {
	q, err := cloud.Col.Accounts().Get()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range q.Docs {
		a := cloud.AccountView(d.Data())
		if a.UID != uid && !a.Disabled && cloud.IsAdmin(a) {
			n++
		}
	}
	return n, nil
}

// asDoc: accounts/{uid} as stored (pre-round-G docs lack the credential fields → null).
func asDoc(d *doc.Map) *doc.Map {
	return d.Shallow().Set("lastSetPassword", fldOr(d, "lastSetPassword", nil)).Set("lastResetLink", fldOr(d, "lastResetLink", nil))
}

func loadAccountDoc(uid string) (*doc.Map, error) {
	s, err := cloud.Col.Accounts().Doc(uid).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("account not found")
	}
	return asDoc(s.Data()), nil
}

// dateMs is Date.parse of what these fields hold: ISO, or tokensValidAfterTime (toUTCString).
func dateMs(s string) (int64, bool) {
	if t, err := time.Parse(http.TimeFormat, s); err == nil {
		return t.UnixMilli(), true
	}
	return doc.ParseISO(s)
}

// isoOrNull: "" = null.
func isoOrNull(t string) string {
	if t == "" {
		return ""
	}
	ms, ok := dateMs(t)
	if !ok {
		return ""
	}
	return isoNow(ms)
}

// validAfterOf: Auth tokensValidAfterTime (ISO) as a document value; it moves when the person changes the password,
// uses a reset link or tokens are revoked.
func validAfterOf(uid string) (any, error) {
	u, err := cloud.AuthAdmin().GetUser(uid)
	if err != nil {
		return nil, err
	}
	if s := isoOrNull(u.TokensValidAfterTime); s != "" {
		return s, nil
	}
	return nil, nil
}

// moved: stored "" = null.
func moved(stored, current string) bool {
	if current == "" {
		return false
	}
	if stored == "" {
		return true
	}
	c, ok1 := dateMs(current)
	s, ok2 := dateMs(stored)
	return ok1 && ok2 && c > s
}

// credCheck: why a stored credential was cleared ("" = it was not): changed | expired | no-user.
type credCheck struct{ pw, link string }

func checkCreds(d *doc.Map, user *driver.AuthUser, now int64) credCheck {
	current := ""
	if user != nil {
		current = isoOrNull(user.TokensValidAfterTime)
	}
	var out credCheck
	if p := d.Map("lastSetPassword"); p != nil {
		if user == nil {
			out.pw = "no-user"
		} else if moved(p.Str("validAfter"), current) {
			out.pw = "changed"
		}
	}
	if l := d.Map("lastResetLink"); l != nil {
		if user == nil {
			out.link = "no-user"
		} else if moved(l.Str("validAfter"), current) {
			out.link = "changed"
		} else if e, ok := doc.ParseISO(l.Str("expiresAt")); ok && e <= now {
			out.link = "expired"
		}
	}
	return out
}

// refreshCreds is the clearing rule (§21 #84 d): a stored password / reset link whose Auth tokensValidAfterTime moved since
// it was stored, an expired link, or a missing Auth user → set to null (persisted, and in docs). Returns why each was cleared.
func refreshCreds(docs []*doc.Map) (map[string]credCheck, error) {
	out := map[string]credCheck{}
	var cand []*doc.Map
	for _, d := range docs {
		if d.Truthy("lastSetPassword") || d.Truthy("lastResetLink") {
			cand = append(cand, d)
		}
	}
	for i := 0; i < len(cand); i += 100 {
		end := min(i+100, len(cand))
		chunk := cand[i:end]
		ids := make([]string, len(chunk))
		for j, d := range chunk {
			ids[j] = d.Str("uid")
		}
		r, err := cloud.AuthAdmin().GetUsers(ids)
		if err != nil {
			return nil, err
		}
		users := map[string]*driver.AuthUser{}
		for _, u := range r {
			users[u.UID] = u
		}
		for _, d := range chunk {
			uid := d.Str("uid")
			chk := checkCreds(d, users[uid], time.Now().UnixMilli())
			out[uid] = chk
			upd := doc.New()
			if chk.pw != "" {
				upd.Set("lastSetPassword", nil)
				d.Set("lastSetPassword", nil)
			}
			if chk.link != "" {
				upd.Set("lastResetLink", nil)
				d.Set("lastResetLink", nil)
			}
			if upd.Len() > 0 {
				if err := cloud.Col.Accounts().Doc(uid).Update(upd); err != nil {
					driver.Log.Warn("account credential clear failed", "uid", uid, "error", err.Error())
				}
			}
		}
	}
	return out, nil
}

func newPassword() string { return cloud.GenerateSecret("alnum", ACCOUNT_PASSWORD_LENGTH) }

func resetLinkResponse(l *doc.Map) *doc.Map {
	return doc.Obj("value", fld(l, "value"), "passwordSetLink", fld(l, "value"), "hideAfterSec", shared.REVEAL_SECONDS, "createdAt", fld(l, "setAt"), "createdBy", fld(l, "setBy"), "expiresAt", fld(l, "expiresAt"))
}

func listAccounts(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "accounts", "read", nil); err != nil {
		return nil, err
	}
	q, err := cloud.Col.Accounts().Get()
	if err != nil {
		return nil, err
	}
	docs := make([]*doc.Map, len(q.Docs))
	for i, d := range q.Docs {
		docs[i] = asDoc(d.Data())
	}
	if _, err := refreshCreds(docs); err != nil {
		return nil, err
	}
	views := make([]*doc.Map, len(docs))
	for i, d := range docs {
		views[i] = core.AccountView(d)
	}
	sort.SliceStable(views, func(i, j int) bool { return localeCompare(views[i].Str("name"), views[j].Str("name")) < 0 })
	items := make([]any, len(views))
	for i, v := range views {
		items[i] = v
	}
	return doc.Obj("items", items), nil
}

// jsLength is v.length of a JSON value: Undefined when it has none.
func jsLength(v any) any {
	switch x := v.(type) {
	case []any:
		return float64(len(x))
	case string:
		return float64(doc.JSLen(x))
	case *doc.Map:
		return fld(x, "length")
	}
	return doc.Undefined
}

func createAccount(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "accounts", "admin", nil); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"name", "email", "phone", "img", "access"}); err != nil {
		return nil, err
	}
	v := &cloud.V{}
	profileChecks(v, b, true)
	if v.OK() {
		if err := core.VIconRefs(v, []core.IconField{{Path: "img", Value: fld(b, "img")}}); err != nil {
			return nil, err
		}
	}
	email := ""
	if s, ok := b.StrOK("email"); ok {
		email = strings.ToLower(doc.JSTrim(s))
	}
	if !shared.RE.Email.MatchString(email) || doc.JSLen(email) > 254 {
		v.Err("email", "V_FORMAT", "email is not valid")
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	access := fldOr(b, "access", []any{})
	rules := jsLength(access)
	auditData := func(set bool) *doc.Map {
		d := doc.Obj("email", email)
		if rules != doc.Undefined {
			d.Set("rules", rules)
		}
		return d.Set("passwordSet", set)
	}
	c.Audit = &cloud.AuditHint{Data: auditData(false)}
	if doc.Truthy(rules) {
		tags, deps, err := existingRefs()
		if err != nil {
			return nil, err
		}
		if issues := cloud.ValidateAccessRules(access, tags, deps); len(issues) > 0 {
			return nil, cloud.E.Invalid(issues)
		}
	}
	// §21 #84: a generated password, stored (revealAccountPassword) until the person changes it; no set-password link
	password := newPassword()
	name := doc.JSTrim(b.Str("name"))
	user, err := cloud.AuthAdmin().CreateUser(driver.CreateUserInput{Email: email, Password: &password, DisplayName: &name, EmailVerified: false})
	if err != nil {
		switch driver.AuthErrorCode(err) {
		case "auth/email-already-exists":
			return nil, cloud.E.Conflict("an account with "+email+" exists", doc.Obj("field", "email"))
		case "auth/invalid-email":
			return nil, cloud.E.Bad("email", "V_FORMAT", "email is not valid")
		}
		return nil, err
	}
	now := doc.NowISO()
	validAfter, err := validAfterOf(user.UID)
	if err != nil {
		return nil, err
	}
	me := c.Account.UID
	d := doc.Obj(
		"uid", user.UID, "img", fldOr(b, "img", nil), "name", name, "email", email, "phone", fldOr(b, "phone", nil), "access", access,
		"disabled", false, "createdAt", now, "createdBy", me, "lastLoginAt", nil,
		"lastSetPassword", doc.Obj("value", password, "setAt", now, "setBy", me, "validAfter", validAfter), "lastResetLink", nil,
	)
	if err := cloud.Col.Accounts().Doc(user.UID).Create(d); err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: auditData(true)}
	return doc.Obj("account", core.AccountView(d), "value", password, "hideAfterSec", shared.REVEAL_SECONDS), nil
}

func getAccount(c *cloud.Ctx) (any, error) {
	uid := c.Params["uid"]
	if uid != c.Account.UID {
		if err := cloud.Need(c, "accounts", "read", nil); err != nil {
			return nil, err
		}
	}
	d, err := loadAccountDoc(uid)
	if err != nil {
		return nil, err
	}
	if _, err := refreshCreds([]*doc.Map{d}); err != nil {
		return nil, err
	}
	return core.AccountView(d), nil
}

func patchAccount(c *cloud.Ctx) (any, error) {
	uid := c.Params["uid"]
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"name", "phone", "img", "access"}, "uid", "email", "disabled", "createdAt", "createdBy", "lastLoginAt", "lastSetPassword", "lastResetLink"); err != nil {
		return nil, err
	}
	hasAccess := b.Has("access")
	// audit action: access-changed when the request changes access rules, else updated (refined below)
	if hasAccess {
		c.Audit = &cloud.AuditHint{Action: "account.access-changed"}
	} else {
		c.Audit = &cloud.AuditHint{Action: "account.updated"}
	}
	own := uid == c.Account.UID
	if hasAccess {
		if err := cloud.Need(c, "accounts", "admin", nil); err != nil {
			return nil, err
		}
	}
	if !own && (b.Has("name") || b.Has("phone") || b.Has("img")) {
		if err := cloud.Need(c, "accounts", "write", nil); err != nil {
			return nil, err
		}
	}
	v := &cloud.V{}
	profileChecks(v, b, false)
	if v.OK() {
		if err := core.VIconRefs(v, []core.IconField{{Path: "img", Value: fld(b, "img")}}); err != nil {
			return nil, err
		}
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	if hasAccess {
		tags, deps, err := existingRefs()
		if err != nil {
			return nil, err
		}
		if issues := cloud.ValidateAccessRules(b.Val("access"), tags, deps); len(issues) > 0 {
			return nil, cloud.E.Invalid(issues)
		}
	}
	stored, err := loadAccountDoc(uid)
	if err != nil {
		return nil, err
	}
	cur := core.AccountView(stored)
	next := cur.Shallow()
	upd := doc.New()
	name := ""
	if b.Has("name") {
		name = doc.JSTrim(b.Str("name"))
		upd.Set("name", name)
		next.Set("name", name)
	}
	for _, k := range []string{"phone", "img", "access"} {
		if b.Has(k) {
			upd.Set(k, b.Val(k))
			next.Set(k, b.Val(k))
		}
	}
	if hasAccess {
		curA := cloud.AccountView(cur)
		if cloud.IsAdmin(curA) && !cloud.IsAdmin(cloud.AccountView(next)) && !cur.Truthy("disabled") {
			n, err := adminCountExcept(uid)
			if err != nil {
				return nil, err
			}
			if n == 0 {
				return nil, cloud.E.Conflict("the last account with accounts.admin cannot lose it")
			}
		}
		if doc.MarshalString(cur.Val("access")) == doc.MarshalString(next.Val("access")) {
			c.Audit = &cloud.AuditHint{Action: "account.updated"}
		}
	}
	// field update only: the stored credentials (lastSetPassword / lastResetLink values) stay untouched
	if upd.Len() > 0 {
		if err := cloud.Col.Accounts().Doc(uid).Update(upd); err != nil {
			return nil, err
		}
	}
	if b.Has("name") {
		_, _ = cloud.AuthAdmin().UpdateUser(uid, driver.UpdateUserInput{DisplayName: &name})
	}
	return next, nil
}

func disableAccount(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "accounts", "admin", nil); err != nil {
		return nil, err
	}
	uid := c.Params["uid"]
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"disabled"}); err != nil {
		return nil, err
	}
	dis, isBool := b.Val("disabled").(bool)
	if isBool && !dis {
		c.Audit = &cloud.AuditHint{Action: "account.enabled"}
	}
	if !isBool {
		return nil, cloud.E.Bad("disabled", "V_TYPE", "disabled must be true / false")
	}
	stored, err := loadAccountDoc(uid)
	if err != nil {
		return nil, err
	}
	cur := core.AccountView(stored)
	if dis && uid == c.Account.UID {
		return nil, cloud.E.Conflict("you cannot disable yourself")
	}
	if dis && !cur.Truthy("disabled") && cloud.IsAdmin(cloud.AccountView(cur)) {
		n, err := adminCountExcept(uid)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, cloud.E.Conflict("the last account with accounts.admin cannot be disabled")
		}
	}
	next := cur.Shallow().Set("disabled", dis)
	if err := cloud.Col.Accounts().Doc(uid).Update(doc.Obj("disabled", dis)); err != nil {
		return nil, err
	}
	if _, err := cloud.AuthAdmin().UpdateUser(uid, driver.UpdateUserInput{Disabled: &dis}); err != nil && driver.AuthErrorCode(err) != "auth/user-not-found" {
		return nil, err
	}
	if dis {
		_ = cloud.AuthAdmin().RevokeRefreshTokens(uid)
	}
	return next, nil
}

// setAccountPassword: "Set new password…" (§21 #84): generated 20-char alnum password; the person's sessions end.
func setAccountPassword(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "accounts", "admin", nil); err != nil {
		return nil, err
	}
	uid := c.Params["uid"]
	acc, err := loadAccountDoc(uid)
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("email", fld(acc, "email"))}
	password := newPassword()
	_, err = cloud.AuthAdmin().UpdateUser(uid, driver.UpdateUserInput{Password: &password})
	if err == nil {
		err = cloud.AuthAdmin().RevokeRefreshTokens(uid)
	}
	if err != nil {
		if driver.AuthErrorCode(err) == "auth/user-not-found" {
			return nil, cloud.E.NotFound("the sign-in user of this account does not exist")
		}
		return nil, err
	}
	// validAfter is read AFTER the change: only a later move (the person changes it) clears the stored value
	validAfter, err := validAfterOf(uid)
	if err != nil {
		return nil, err
	}
	setAt := doc.NowISO()
	pw := doc.Obj("value", password, "setAt", setAt, "setBy", c.Account.UID, "validAfter", validAfter)
	if err := cloud.Col.Accounts().Doc(uid).Update(doc.Obj("lastSetPassword", pw)); err != nil {
		return nil, err
	}
	return doc.Obj("value", password, "hideAfterSec", shared.REVEAL_SECONDS, "setAt", setAt, "setBy", c.Account.UID), nil
}

func revealAccountPassword(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "accounts", "admin", nil); err != nil {
		return nil, err
	}
	uid := c.Params["uid"]
	acc, err := loadAccountDoc(uid)
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("email", fld(acc, "email"))}
	r, err := refreshCreds([]*doc.Map{acc})
	if err != nil {
		return nil, err
	}
	why := r[uid].pw
	pw := acc.Map("lastSetPassword")
	if pw == nil {
		if why == "changed" {
			return nil, cloud.E.Gone("the person changed their password since it was set — set a new password to view one", doc.Obj("reason", "changed"))
		}
		reason := "not-stored"
		if why == "no-user" {
			reason = "no-user"
		}
		return nil, cloud.E.Gone("no password is stored for this account — set a new password to view one", doc.Obj("reason", reason))
	}
	return doc.Obj("value", fld(pw, "value"), "hideAfterSec", shared.REVEAL_SECONDS, "setAt", fld(pw, "setAt"), "setBy", fld(pw, "setBy")), nil
}

// resetAccountPassword: a new password-reset link any time (§21 #84): stored 1 h as lastResetLink.
func resetAccountPassword(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "accounts", "admin", nil); err != nil {
		return nil, err
	}
	uid := c.Params["uid"]
	acc, err := loadAccountDoc(uid)
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("email", fld(acc, "email"), "mailed", cloud.Env.SmtpURL() != "")}
	link, err := cloud.AuthAdmin().GeneratePasswordResetLink(acc.Str("email"))
	if err != nil {
		if code := driver.AuthErrorCode(err); code == "auth/user-not-found" || code == "auth/email-not-found" {
			return nil, cloud.E.NotFound("the sign-in user of this account does not exist")
		}
		return nil, err
	}
	now := doc.NowISO()
	expires := doc.IsoPlus(RESET_LINK_TTL_MS)
	validAfter, err := validAfterOf(uid)
	if err != nil {
		return nil, err
	}
	stored := doc.Obj("value", link, "setAt", now, "setBy", c.Account.UID, "expiresAt", expires, "validAfter", validAfter)
	if err := cloud.Col.Accounts().Doc(uid).Update(doc.Obj("lastResetLink", stored)); err != nil {
		return nil, err
	}
	// decision (round G): the reset link is still e-mailed when OPM_SMTP_URL is configured (account creation mails nothing)
	cloud.SendMail(acc.Str("email"), "Reset your On-Prem Manager password", "Set a new password here (valid for 1 hour):\n"+link+"\n")
	return resetLinkResponse(stored), nil
}

func revealAccountResetLink(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "accounts", "admin", nil); err != nil {
		return nil, err
	}
	uid := c.Params["uid"]
	acc, err := loadAccountDoc(uid)
	if err != nil {
		return nil, err
	}
	c.Audit = &cloud.AuditHint{Data: doc.Obj("email", fld(acc, "email"))}
	r, err := refreshCreds([]*doc.Map{acc})
	if err != nil {
		return nil, err
	}
	why := r[uid].link
	link := acc.Map("lastResetLink")
	if link == nil {
		if why == "changed" {
			return nil, cloud.E.Gone("the reset link was used (or the password changed) — create a new link", doc.Obj("reason", "used"))
		}
		if why == "expired" {
			return nil, cloud.E.Gone("the reset link expired — create a new link", doc.Obj("reason", "expired"))
		}
		reason := "not-stored"
		if why == "no-user" {
			reason = "no-user"
		}
		return nil, cloud.E.Gone("no reset link is stored for this account — create a new link", doc.Obj("reason", reason))
	}
	return resetLinkResponse(link), nil
}

// filterRun is the "one filter in the store, the others in memory" query of the audit log and the activities.
type filterRun struct {
	q    *driver.Query
	mem  []func(*doc.Map) bool
	used bool
}

func (f *filterRun) use(field string, val any, op string, pred func(*doc.Map) bool) {
	if !f.used {
		f.q = f.q.Where(field, op, val)
		f.used = true
	} else {
		f.mem = append(f.mem, pred)
	}
}

// page runs the over-fetching loop; decode turns a stored doc into the item.
func (f *filterRun) page(after *driver.DocSnap, limit int, decode func(*doc.Map) *doc.Map) ([]*doc.Map, error) {
	items := []*doc.Map{}
	exhausted := false
	pageSize := limit + 1
	if len(f.mem) > 0 {
		pageSize = 500
	}
	for round := 0; round < 20 && len(items) <= limit && !exhausted; round++ {
		q := f.q
		if after != nil {
			q = q.StartAfterSnap(after)
		}
		page, err := q.Limit(pageSize).Get()
		if err != nil {
			return nil, err
		}
		if len(page.Docs) < pageSize {
			exhausted = true
		}
		for _, d := range page.Docs {
			after = d
			e := decode(d.Data())
			keep := true
			for _, p := range f.mem {
				keep = keep && p(e)
			}
			if keep {
				items = append(items, e)
			}
			if len(items) > limit {
				break
			}
		}
	}
	return items, nil
}

// pageOf is { items: shown, cursor } of a filterRun page.
func pageOf(items []*doc.Map, limit int, idKey string) *doc.Map {
	n := min(limit, len(items))
	shown := make([]any, n)
	for i := range n {
		shown[i] = items[i]
	}
	var cursor any
	if len(items) > limit {
		cursor = fld(items[n-1], idKey)
	}
	return doc.Obj("items", shown, "cursor", cursor)
}

func listAuditLog(c *cloud.Ctx) (any, error) {
	if err := cloud.Need(c, "accounts", "admin", nil); err != nil {
		return nil, err
	}
	q0 := c.Query
	limit, err := core.ClampLimit(c, 100, 500)
	if err != nil {
		return nil, err
	}
	action, actor, targetType, target, outcome, from, to := q0.Get("action"), q0.Get("actor"), q0.Get("targetType"), q0.Get("target"), q0.Get("outcome"), q0.Get("from"), q0.Get("to")
	var errs []string
	if action != "" && !reActivityFilter.MatchString(action) {
		errs = append(errs, `action must be an audit action or a prefix ending in "."`)
	}
	if targetType != "" {
		found := false
		for _, t := range shared.AUDIT_TARGET_TYPES {
			found = found || t == targetType
		}
		if !found {
			errs = append(errs, "targetType must be one of "+strings.Join(shared.AUDIT_TARGET_TYPES, " | "))
		}
	}
	if outcome != "" && outcome != "ok" && outcome != "error" {
		errs = append(errs, "outcome must be ok | error")
	}
	if from != "" && !validISO(from) {
		errs = append(errs, "from must be an ISO time")
	}
	if to != "" && !validISO(to) {
		errs = append(errs, "to must be an ISO time")
	}
	if len(errs) > 0 {
		return nil, cloud.E.Invalid(queryIssues(errs))
	}
	// ONE equality filter runs in the store, the others in memory (over-fetch)
	f := &filterRun{q: cloud.Col.AuditLog().Query}
	actorField, actorVal := "actor", actor
	if strings.Contains(actor, "@") {
		actorField, actorVal = "actorEmail", strings.ToLower(actor)
	}
	if target != "" {
		f.use("target", target, "==", func(e *doc.Map) bool { return e.Val("target") == target })
	}
	if actor != "" {
		f.use(actorField, actorVal, "==", func(e *doc.Map) bool { return e.Val(actorField) == actorVal })
	}
	if action != "" {
		f.use("_actionKeys", action, "array-contains", func(e *doc.Map) bool { return cloud.AuditActionMatches(e.Str("action"), action) })
	}
	if targetType != "" {
		f.use("targetType", targetType, "==", func(e *doc.Map) bool { return e.Val("targetType") == targetType })
	}
	if outcome != "" {
		f.use("outcome", outcome, "==", func(e *doc.Map) bool { return e.Val("outcome") == outcome })
	}
	if from != "" {
		ms, _ := doc.ParseISO(from)
		f.q = f.q.Where("ts", ">=", isoNow(ms))
	}
	if to != "" {
		ms, _ := doc.ParseISO(to)
		f.q = f.q.Where("ts", "<=", isoNow(ms))
	}
	f.q = f.q.OrderBy("ts", driver.Desc)
	var after *driver.DocSnap
	if cursor := q0.Get("cursor"); cursor != "" {
		if after, err = cloud.Col.AuditLog().Doc(cursor).Get(); err != nil {
			return nil, err
		}
		if !after.Exists() {
			return nil, cloud.E.Bad("cursor", "V_FORMAT", "bad cursor")
		}
	}
	items, err := f.page(after, limit, cloud.DecodeAudit)
	if err != nil {
		return nil, err
	}
	return pageOf(items, limit, "auditId"), nil
}
