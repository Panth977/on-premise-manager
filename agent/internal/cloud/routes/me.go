// Package routes holds the ported route files of backend/src/routes: each file registers its ops by name in init().
// agent/cmd/opm-cloud imports this package for the side effect.
package routes

import (
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
)

func init() { cloud.Register("getMe", getMe) }

// getMe: the caller's account; lastLoginAt is written at most once per hour.
func getMe(c *cloud.Ctx) (any, error) {
	acc := c.Account
	account := acc.Doc
	var last int64
	if s := account.Str("lastLoginAt"); s != "" {
		last, _ = doc.ParseISO(s)
	}
	if time.Now().UnixMilli()-last > 3_600_000 {
		now := doc.NowISO()
		if err := cloud.Col.Accounts().Doc(acc.UID).Update(doc.Obj("lastLoginAt", now)); err != nil {
			return nil, err
		}
		account = account.Shallow().Set("lastLoginAt", now)
	}
	return doc.Obj("account", cloud.Pub(account), "isAdmin", cloud.IsAdmin(acc)), nil
}
