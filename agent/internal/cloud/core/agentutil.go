package core

import "opm/agent/internal/cloud/shared"

// CanonicalSafe is canonicalSafe of core/agentutil.ts: nil when the value has no canonical form. The other exports of that
// file are cloud.AsObject, cloud.HexOf, cloud.IsHex64, cloud.JcsHash, cloud.SameJSON and cloud.YMD.
func CanonicalSafe(v any) any {
	s, err := shared.Canonicalize(v)
	if err != nil {
		return nil
	}
	return s
}
