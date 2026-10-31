package core

import (
	"sort"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/shared"
)

// ONLINE_MS is ONLINE_MS.
const ONLINE_MS = 90_000

// IsSecretSet is isSecretSet.
func IsSecretSet(s *doc.Map) bool {
	v := s.Val("value")
	return !nullish(v) && v != ""
}

// MaskSecret is maskSecret.
func MaskSecret(s *doc.Map) *doc.Map {
	set := IsSecretSet(s)
	var v any
	if set {
		v = shared.MASK
	}
	return s.Shallow().Set("value", v).Set("isSet", set)
}

// MaskConfig is maskConfig.
func MaskConfig(c *doc.Map) *doc.Map {
	l := []any{}
	for _, s := range c.Arr("secrets") {
		l = append(l, MaskSecret(s.(*doc.Map)))
	}
	return c.Shallow().Set("secrets", l)
}

// RegistryView is registryView.
func RegistryView(r *doc.Map) *doc.Map {
	return cloud.Pub(r).Set("password", shared.MASK).Set("isSet", r.Truthy("password"))
}

// DeploymentView is deploymentView: the access key plaintext and hash never leave through a view.
func DeploymentView(d *doc.Map) *doc.Map {
	rest := d.Without("draft", "srcTemplate", "accessKeyHash", "accessKey", "_cloudLastHash")
	out := cloud.Pub(rest).Set("draft", MaskConfig(d.Map("draft"))).Set("srcTemplate", SrcTemplateView(d.Map("srcTemplate")))
	if rest.Truthy("branch") {
		out.Set("branch", rest.Map("branch").Without("uncommitted"))
	}
	return out
}

// SrcTemplateView is srcTemplateView: nil for nil.
func SrcTemplateView(s *doc.Map) any {
	if s == nil {
		return nil
	}
	return doc.Obj("templateId", present(s, "templateId"), "templateVersion", present(s, "templateVersion"), "copiedAt", present(s, "copiedAt"), "label", or(s.Map("snapshot"), "label", ""), "baseVersion", present(s, "baseVersion"), "lastSyncedAt", present(s, "lastSyncedAt"), "lastSyncedBy", present(s, "lastSyncedBy"), "skippedCount", len(s.Arr("skipped")))
}

// RevisionView is revisionView.
func RevisionView(r *doc.Map) *doc.Map { return r.Shallow().Set("config", MaskConfig(r.Map("config"))) }

// AgentView is agentView.
func AgentView(a *doc.Map) *doc.Map {
	p := cloud.Pub(a)
	addrs, ok := p.Val("detectedAddresses").([]any)
	if !ok {
		addrs = []any{}
	}
	seen, valid := doc.ParseISO(a.Str("lastSeenAt"))
	return p.Set("detectedAddresses", addrs).Set("online", valid && time.Now().UnixMilli()-seen < ONLINE_MS)
}

// AccountView is accountView: credential metadata only, the values come from the reveal endpoints.
func AccountView(d *doc.Map) *doc.Map {
	p := cloud.Pub(d)
	var pw, link any
	if m := p.Map("lastSetPassword"); m != nil {
		pw = doc.Obj("setAt", present(m, "setAt"), "setBy", present(m, "setBy"))
	}
	if m := p.Map("lastResetLink"); m != nil {
		link = doc.Obj("setAt", present(m, "setAt"), "setBy", present(m, "setBy"), "expiresAt", present(m, "expiresAt"))
	}
	return p.Set("lastSetPassword", pw).Set("lastResetLink", link)
}

// sortedNums is [...l].sort((a, b) => a - b).
func sortedNums(l []any) []any {
	out := append([]any{}, l...)
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := doc.AsNumber(out[i])
		b, _ := doc.AsNumber(out[j])
		return a < b
	})
	return out
}
