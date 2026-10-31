package core

import (
	"fmt"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
)

// DEPS_MANIFEST and DETECTED_ADDRESSES_MAX are the constants of routes/install.ts.
const (
	DEPS_MANIFEST          = "deps/manifest.json"
	DETECTED_ADDRESSES_MAX = 32
)

// LoadDepsManifest is loadDepsManifest: nil = none.
func LoadDepsManifest() *doc.Map {
	t, ok, err := ReadText(DEPS_MANIFEST)
	if err != nil || !ok || t == "" {
		return nil
	}
	m, err := doc.ParseMap([]byte(t))
	if err != nil || !m.Map("docker").Truthy("files") {
		return nil
	}
	return m
}

// ResolveAgentVersion is resolveAgentVersion: requested "" = null.
func ResolveAgentVersion(requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	if v := cloud.Env.AgentVersion(); v != "" {
		return v, nil
	}
	t, _, err := ReadText(AgentLatest)
	if err != nil {
		return "", err
	}
	latest := doc.JSTrim(t)
	if latest == "" {
		return "", cloud.E.NotFound("no agent release is uploaded (agent/latest missing)")
	}
	return latest, nil
}

// DetectedAddressesError is detectedAddressesError: "" = ok.
func DetectedAddressesError(v any) string {
	if nullish(v) {
		return ""
	}
	l, ok := v.([]any)
	if !ok {
		return "detectedAddresses must be a list of strings"
	}
	if len(l) > DETECTED_ADDRESSES_MAX {
		return fmt.Sprintf("detectedAddresses has at most %d entries", DETECTED_ADDRESSES_MAX)
	}
	for _, x := range l {
		if s, ok := x.(string); !ok || s == "" || doc.JSLen(s) > 255 {
			return "detectedAddresses entries must be strings of 1–255 characters"
		}
	}
	return ""
}

// DetectedAddressesOf is detectedAddressesOf.
func DetectedAddressesOf(v any) []any {
	if l, ok := v.([]any); ok {
		return append([]any{}, l...)
	}
	return []any{}
}

// PeerInfo is peerInfo.
func PeerInfo(a *doc.Map) *doc.Map {
	return doc.Obj("siteSlug", present(a, "siteSlug"), "publicKey", present(a, "publicKey"), "advertiseHost", present(a, "advertiseHost"), "agentPort", present(a, "agentPort"), "enrolledAt", present(a, "enrolledAt"))
}
