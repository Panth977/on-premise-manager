package core

import (
	"fmt"

	"opm/agent/internal/cloud"
)

// ClampLimit is clampLimit of routes/accounts.ts. sendMail of that file is cloud.SendMail.
func ClampLimit(c *cloud.Ctx, def, max int) (int, error) {
	s, ok := c.Query.Lookup("limit")
	if !ok || s == "" {
		return def, nil
	}
	n, ok := cloud.JSNumber(s)
	if !ok || !isInt(n) || n < 1 {
		return 0, cloud.E.Bad("limit", "V_RANGE", fmt.Sprintf("limit must be 1–%d", max))
	}
	if n > float64(max) {
		return max, nil
	}
	return int(n), nil
}
