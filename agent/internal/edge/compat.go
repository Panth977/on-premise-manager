package edge

import (
	"opm/agent/internal/edgecfg"
	"opm/agent/internal/model"
)

// The Caddy config builder lives in the caddy-free package internal/edgecfg (§21 #106: the exporter compiles it to
// wasip1). These aliases keep the agent API of this package unchanged.
type (
	PEMPair = edgecfg.PEMPair
	Input   = edgecfg.Input
	Partial = edgecfg.Partial
)

const (
	BuiltIn404 = edgecfg.BuiltIn404
	BuiltIn503 = edgecfg.BuiltIn503
	BuiltIn413 = edgecfg.BuiltIn413
	BuiltIn500 = edgecfg.BuiltIn500
)

// SortedHosts returns the hosts in priority order: exact, wildcard, "*".
func SortedHosts(hosts []model.EdgeHost) []model.EdgeHost { return edgecfg.SortedHosts(hosts) }

// Assemble merges partials into one Caddy JSON document.
func Assemble(storageRoot string, parts []*Partial) map[string]any {
	return edgecfg.Assemble(storageRoot, parts)
}

// Redact returns a copy of the config JSON with secrets removed.
func Redact(cfg map[string]any) any { return edgecfg.Redact(cfg) }

// ToPlaceholders maps an edge header value to Caddy placeholders.
func ToPlaceholders(v string) string { return edgecfg.ToPlaceholders(v) }

// RequestHeaderSet is the reverse_proxy headers.request.set of a proxy action.
func RequestHeaderSet(a *model.EdgeRouteAction) map[string][]string {
	return edgecfg.RequestHeaderSet(a)
}
