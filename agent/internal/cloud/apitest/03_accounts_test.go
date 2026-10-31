package apitest

import (
	"strings"
	"testing"

	"opm/agent/internal/cloud"
)

// Port of backend/test/03-accounts.test.ts.
func TestAccounts(t *testing.T) {
	t.Run("accounts: create (generated password + audit), email conflict, validation", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		email := Uniq("new") + "@Opm.Test"
		r := admin.API.Call(t, "createAccount", A{"body": A{"name": "New Person", "email": email, "phone": "+919876543210"}})
		Eq(t, r.At("account.email"), strings.ToLower(email))
		Eq(t, r.At("account.createdBy"), admin.UID)
		Match(t, r.At("value"), `^[A-Za-z0-9]{20}$`)
		Eq(t, r.At("hideAfterSec"), 60)
		Eq(t, r.At("account.lastSetPassword.setBy"), admin.UID)
		False(t, strings.Contains(r.At("account").String(), r.At("value").Str()))
		admin.API.Rejects(t, "createAccount", A{"body": A{"name": "Dup", "email": email}}, "conflict", 409)
		admin.API.Rejects(t, "createAccount", A{"body": A{"name": "Bad", "email": "not-an-email"}}, "invalid")
		admin.API.Rejects(t, "createAccount", A{"body": A{"name": "Bad", "email": Uniq("p") + "@x.io", "phone": "12345"}}, "invalid")
		admin.API.Rejects(t, "createAccount", A{"body": A{"name": "Bad", "email": Uniq("p") + "@x.io", "access": L{A{"resource": "registries", "actions": L{"read"}, "scope": A{"kind": "tags", "tagSlugs": L{"nope"}}}}}}, "invalid")
		uid := r.At("account.uid").Str()
		audit := admin.API.Call(t, "listAuditLog", A{})
		True(t, waSome(audit.At("items"), func(a J) bool {
			return a.At("action").Str() == "account.created" && a.At("target").Str() == "accounts/"+uid
		}))
		reset := admin.API.Call(t, "resetAccountPassword", A{"uid": uid})
		Match(t, reset.At("value"), `^https?://`)
		Eq(t, reset.At("passwordSetLink"), reset.At("value"))
		all := admin.API.Call(t, "listAccounts", A{})
		True(t, waSome(all.At("items"), func(a J) bool { return a.At("uid").Str() == uid }))
	})

	t.Run("accounts: own profile always; access needs accounts.admin; get own always", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		me := NewPerson(t, L{})
		Eq(t, me.API.Call(t, "getAccount", A{"uid": me.UID}).At("uid"), me.UID)
		me.API.Rejects(t, "getAccount", A{"uid": admin.UID}, "forbidden")
		Eq(t, me.API.Call(t, "patchAccount", A{"uid": me.UID, "body": A{"name": "Renamed", "img": "https://x.example/a.png"}}).At("name"), "Renamed")
		me.API.Rejects(t, "patchAccount", A{"uid": me.UID, "body": A{"access": cloud.FullAdminRules()}}, "forbidden")
		me.API.Rejects(t, "patchAccount", A{"uid": admin.UID, "body": A{"name": "hack"}}, "forbidden")
		me.API.Rejects(t, "patchAccount", A{"uid": me.UID, "body": A{"email": "x@y.z"}}, "invalid")
		p := admin.API.Call(t, "patchAccount", A{"uid": me.UID, "body": A{"access": L{A{"resource": "templates", "actions": L{"read"}, "scope": A{"kind": "all"}}}}})
		Eq(t, p.At("access").Len(), 1)
		True(t, me.API.Call(t, "listTemplates", A{}).At("items").Truthy())
		audit := admin.API.Call(t, "listAuditLog", A{})
		True(t, waSome(audit.At("items"), func(a J) bool {
			return a.At("action").Str() == "account.access-changed" && a.At("target").Str() == "accounts/"+me.UID
		}))
		admin.API.Rejects(t, "patchAccount", A{"uid": me.UID, "body": A{"access": L{A{"resource": "templates", "actions": L{"publish"}, "scope": A{"kind": "all"}}}}}, "invalid")
	})

	t.Run("accounts: disable / enable; self and last admin protected; disabled tokens rejected", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		admin.API.Rejects(t, "disableAccount", A{"uid": admin.UID, "body": A{"disabled": true}}, "conflict")
		other := NewPerson(t, L{})
		d := admin.API.Call(t, "disableAccount", A{"uid": other.UID, "body": A{"disabled": true}})
		Eq(t, d.At("disabled"), true)
		other.API.Rejects(t, "getMe", A{}, "forbidden")
		audit := admin.API.Call(t, "listAuditLog", A{})
		True(t, waSome(audit.At("items"), func(a J) bool {
			return a.At("action").Str() == "account.disabled" && a.At("target").Str() == "accounts/"+other.UID
		}))
		admin.API.Call(t, "disableAccount", A{"uid": other.UID, "body": A{"disabled": false}})
		// last-admin rule: make every other admin non-admin in a scratch view → simulate with a lone admin
		saved := []DocJ{}
		for _, x := range ListDocs(t, "accounts") {
			isAdmin := waSome(x.Data.At("access"), func(r J) bool {
				return r.At("resource").Str() == "accounts" && waSome(r.At("actions"), func(a J) bool { return a.Str() == "admin" })
			})
			if isAdmin && !x.Data.At("disabled").Truthy() && x.ID != admin.UID {
				saved = append(saved, x)
			}
		}
		defer func() {
			for _, s := range saved {
				UpdateDoc(t, "accounts/"+s.ID, A{"disabled": s.Data.At("disabled").Bool()})
			}
		}()
		for _, s := range saved {
			UpdateDoc(t, "accounts/"+s.ID, A{"disabled": true})
		}
		NewPerson(t, L{A{"resource": "accounts", "actions": L{"read", "write"}, "scope": A{"kind": "all"}}})
		admin.API.Rejects(t, "patchAccount", A{"uid": admin.UID, "body": A{"access": L{}}}, "conflict")
		other2 := NewPerson(t, "admin")
		// now there are 2 admins → removing one is fine
		admin.API.Call(t, "patchAccount", A{"uid": other2.UID, "body": A{"access": L{}}})
	})

	t.Run("audit log paging with cursor", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		for i := 0; i < 3; i++ {
			admin.API.Call(t, "createAccount", A{"body": A{"name": "P", "email": Uniq("pg") + "@opm.test"}})
		}
		p1 := admin.API.Call(t, "listAuditLog", A{"query": A{"limit": 2}})
		Eq(t, p1.At("items").Len(), 2)
		True(t, p1.At("cursor").Truthy())
		p2 := admin.API.Call(t, "listAuditLog", A{"query": A{"limit": 2, "cursor": p1.At("cursor")}})
		True(t, p2.At("items").Len() >= 1)
		NotEq(t, p2.At("items.0.auditId"), p1.At("items.1.auditId"))
		True(t, p2.At("items.0.ts").Str() <= p1.At("items.1.ts").Str())
	})
}
