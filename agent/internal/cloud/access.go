package cloud

import (
	"fmt"
	"strconv"
	"strings"

	"opm/agent/internal/cloud/doc"
)

func itoa(n int) string { return strconv.Itoa(n) }

// Scope of an access rule.
type Scope struct {
	Kind          string // all | tags | deployments
	TagSlugs      []string
	DeploymentIDs []string
}

// AccessRule is one rule of an account.
type AccessRule struct {
	Resource string
	Actions  []string
	Scope    Scope
}

// AccessTarget is what a deployment permission is checked against.
type AccessTarget struct {
	DeploymentID       string
	TagSlugs           []string
	ManagerAccountUIDs []string
}

// AccessDecision is can()'s result.
type AccessDecision struct {
	Allowed   bool
	Reason    string
	RuleIndex int // -1 = null
}

// Account is the API view of an account (accountView): typed fields for permission checks, Doc for the JSON.
type Account struct {
	UID      string
	Email    *string
	Disabled bool
	Access   []AccessRule
	Doc      *doc.Map
}

// JSValue implements doc.JSValue.
func (a *Account) JSValue() any { return a.Doc }

// ParseAccessRules reads the access field of an account.
func ParseAccessRules(l []any) []AccessRule {
	out := make([]AccessRule, 0, len(l))
	for _, x := range l {
		m, ok := x.(*doc.Map)
		if !ok {
			continue
		}
		s := m.Map("scope")
		out = append(out, AccessRule{Resource: m.Str("resource"), Actions: m.Strs("actions"), Scope: Scope{Kind: s.Str("kind"), TagSlugs: s.Strs("tagSlugs"), DeploymentIDs: s.Strs("deploymentIds")}})
	}
	return out
}

// AccountView is views.ts accountView: metadata only, never the stored credential values.
func AccountView(d *doc.Map) *Account {
	o := Pub(d)
	var pw, link any
	if x := o.Map("lastSetPassword"); x != nil {
		pw = doc.Obj("setAt", x.Val("setAt"), "setBy", x.Val("setBy"))
	}
	if x := o.Map("lastResetLink"); x != nil {
		link = doc.Obj("setAt", x.Val("setAt"), "setBy", x.Val("setBy"), "expiresAt", x.Val("expiresAt"))
	}
	o.Set("lastSetPassword", pw).Set("lastResetLink", link)
	return &Account{UID: o.Str("uid"), Email: o.StrPtr("email"), Disabled: o.Truthy("disabled"), Access: ParseAccessRules(o.Arr("access")), Doc: o}
}

func anyIn(a, b []string) bool {
	for _, x := range a {
		if has(b, x) {
			return true
		}
	}
	return false
}

func scopeContains(s Scope, t *AccessTarget) bool {
	if s.Kind == "all" {
		return true
	}
	if t == nil {
		return false
	}
	if s.Kind == "tags" {
		return anyIn(s.TagSlugs, t.TagSlugs)
	}
	return has(s.DeploymentIDs, t.DeploymentID)
}

// Can evaluates one permission (shared/src/access.ts can). Deny by default.
func Can(a *Account, resource, action string, t *AccessTarget) AccessDecision {
	if a == nil {
		return AccessDecision{false, "not logged in", -1}
	}
	if a.Disabled {
		return AccessDecision{false, "account disabled", -1}
	}
	if action == "read" && has(implicitRead, resource) {
		return AccessDecision{true, "catalog read", -1}
	}
	if t != nil && has(t.ManagerAccountUIDs, a.UID) {
		if strings.HasPrefix(resource, "deployment.") || (resource == "deployments" && action == "read") {
			return AccessDecision{true, "manager of the deployment", -1}
		}
	}
	dev := has(developerResources, resource)
	for i, r := range a.Access {
		if r.Resource != resource || !has(r.Actions, action) {
			continue
		}
		if dev {
			if r.Scope.Kind == "all" {
				return AccessDecision{true, fmt.Sprintf("rule #%d (scope all)", i+1), i}
			}
			continue
		}
		if scopeContains(r.Scope, t) {
			return AccessDecision{true, fmt.Sprintf("rule #%d (%s)", i+1, r.Scope.Kind), i}
		}
	}
	on := ""
	if t != nil {
		on = " on " + t.DeploymentID
	}
	return AccessDecision{false, "no rule gives " + resource + "." + action + on, -1}
}

// Allowed is allowed().
func Allowed(a *Account, resource, action string, t *AccessTarget) bool {
	return Can(a, resource, action, t).Allowed
}

// CanSeeDeployment: can the account see the deployment at all?
func CanSeeDeployment(a *Account, t *AccessTarget) bool {
	if Allowed(a, "deployments", "read", t) {
		return true
	}
	for _, r := range []string{"deployment.setup", "deployment.config", "deployment.secrets", "deployment.activities", "deployment.transfers"} {
		if Allowed(a, r, "read", t) {
			return true
		}
	}
	return false
}

// IsAdmin: has accounts.admin with scope all.
func IsAdmin(a *Account) bool { return Allowed(a, "accounts", "admin", nil) }

// FullAdminRules: every resource, every valid action, scope all.
func FullAdminRules() []any {
	out := make([]any, len(allResources))
	for i, r := range allResources {
		acts := make([]any, len(validActions[r]))
		for j, a := range validActions[r] {
			acts[j] = a
		}
		out[i] = doc.Obj("resource", r, "actions", acts, "scope", doc.Obj("kind", "all"))
	}
	return out
}

// ValidateAccessRules validates access rules (POST/PATCH accounts). existingTags / existingDeployments: nil = not checked.
func ValidateAccessRules(rules any, existingTags, existingDeployments []string) []Issue {
	out := []Issue{}
	e := func(path, code, msg string) {
		out = append(out, Issue{Path: path, Code: code, Message: msg, Severity: "error"})
	}
	list, ok := rules.([]any)
	if !ok {
		e("access", "V_TYPE", "access must be a list")
		return out
	}
	if len(list) > 100 {
		e("access", "V_TOO_MANY", "at most 100 rules")
	}
	for i, x := range list {
		p := fmt.Sprintf("access[%d]", i)
		r, ok := x.(*doc.Map)
		if !ok {
			if arr, isArr := x.([]any); isArr {
				_ = arr
				r = doc.New() // an array is an object in JS: it fails at resource
			} else {
				e(p, "V_TYPE", "a rule must be an object")
				continue
			}
		}
		res, isStr := r.StrOK("resource")
		if !isStr || !has(allResources, res) {
			shown := "undefined"
			if v, had := r.Get("resource"); had {
				shown = jsString(v)
			}
			e(p+".resource", "V_ENUM", `unknown resource "`+shown+`"`)
			continue
		}
		acts := r.Arr("actions")
		if acts == nil || len(acts) < 1 || len(acts) > 4 {
			e(p+".actions", "V_LENGTH", "actions must have 1–4 items")
		} else {
			seen := map[string]bool{}
			dup := false
			for _, a := range acts {
				k := doc.MarshalString(a)
				dup = dup || seen[k]
				seen[k] = true
			}
			if dup {
				e(p+".actions", "V_DUPLICATE", "actions must be unique")
			}
			for _, a := range acts {
				if s, isS := a.(string); !isS || !has(validActions[res], s) {
					e(p+".actions", "V_ACCESS_ACTION", `"`+jsString(a)+`" is not valid on `+res+" (use "+strings.Join(validActions[res], ", ")+")")
				}
			}
		}
		s := r.Map("scope")
		kind := s.Str("kind")
		if s == nil || (kind != "all" && kind != "tags" && kind != "deployments") {
			e(p+".scope", "V_ENUM", "scope.kind must be all | tags | deployments")
			continue
		}
		if has(developerResources, res) && kind != "all" {
			e(p+".scope", "V_ACCESS_SCOPE", res+" needs scope all")
		}
		if kind == "tags" {
			l := s.Arr("tagSlugs")
			if l == nil || len(l) < 1 || len(l) > 50 {
				e(p+".scope.tagSlugs", "V_LENGTH", "tagSlugs must have 1–50 items")
			} else if existingTags != nil {
				for _, t := range l {
					if x, _ := t.(string); !has(existingTags, x) {
						e(p+".scope.tagSlugs", "V_REF", `tag "`+jsString(t)+`" does not exist`)
					}
				}
			}
		}
		if kind == "deployments" {
			l := s.Arr("deploymentIds")
			if l == nil || len(l) < 1 || len(l) > 200 {
				e(p+".scope.deploymentIds", "V_LENGTH", "deploymentIds must have 1–200 items")
			} else if existingDeployments != nil {
				for _, d := range l {
					if x, _ := d.(string); !has(existingDeployments, x) {
						e(p+".scope.deploymentIds", "V_REF", `deployment "`+jsString(d)+`" does not exist`)
					}
				}
			}
		}
	}
	return out
}

// jsString is String(v) for JSON values.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case *doc.Map:
		return "[object Object]"
	case []any:
		p := make([]string, len(x))
		for i, y := range x {
			if y != nil {
				p[i] = jsString(y)
			}
		}
		return strings.Join(p, ",")
	}
	return doc.MarshalString(v)
}
