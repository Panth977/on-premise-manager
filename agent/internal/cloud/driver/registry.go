package driver

import (
	"strings"

	"opm/agent/internal/cloud/doc"
)

// TableIndexSpec is one entry of the index registry (registry.ts). nil and empty lists differ: the _schema hash is
// JSON.stringify of the entry, and an omitted property is not an empty list.
type TableIndexSpec struct {
	Scalar    []string
	Array     []string
	Composite [][]string
	Group     []string
}

// IndexRegistry maps a table name to its spec.
type IndexRegistry map[string]TableIndexSpec

var actTail = []string{"_tsMs", "seq"}

func t(tail []string, head ...string) []string { return append(head, tail...) }

// DefaultRegistry is INDEX_REGISTRY. TestRegistryMatchesTS compares it (and the hashes) with registry.ts.
var DefaultRegistry = IndexRegistry{
	// global file
	"templates":          {Scalar: []string{"slug"}},
	"services__images":   {Scalar: []string{"registryId", "createdAt"}, Group: []string{"registryId"}},
	"templates__commits": {Scalar: []string{"seq"}},
	"auditLog": {
		Scalar:    []string{"ts", "target", "actor", "actorEmail", "targetType", "outcome", "action", "requestId", "op"},
		Array:     []string{"_actionKeys"},
		Composite: [][]string{{"target", "ts"}, {"actor", "ts"}, {"actorEmail", "ts"}, {"targetType", "ts"}, {"outcome", "ts"}},
	},
	"icons":               {Scalar: []string{"sha256"}},
	"uploads":             {Scalar: []string{"expiresAt"}},
	"stages":              {Scalar: []string{"expiresAt"}},
	"agentNonces":         {Scalar: []string{"expiresAt"}},
	"rateLimits":          {Scalar: []string{"expireAt", "uid"}},
	"githubMirror__queue": {Scalar: []string{"gitBranch", "state", "createdAt", "op", "reason"}, Composite: [][]string{{"gitBranch", "state"}}},
	"authRefresh":         {Scalar: []string{"uid", "family", "expiresAt"}},
	"authReset":           {Scalar: []string{"uid", "expiresAt"}},
	"authOidcState":       {Scalar: []string{"expiresAt"}},
	"authOidcCode":        {Scalar: []string{"expiresAt"}},
	// deployment files
	"deployment": {
		Scalar:    []string{"deleted", "updatedAt", "slug", "agentUpgradeRun.status"},
		Array:     []string{"tagSlugs", "managerAccountUids"},
		Composite: [][]string{{"deleted", "updatedAt"}},
	},
	"commits":                {Scalar: []string{"seq"}},
	"agentBranches__commits": {Scalar: []string{"seq"}},
	"revisions":              {Scalar: []string{"seq"}},
	"proposals":              {Scalar: []string{"status", "revId"}, Group: []string{"status"}},
	"agents":                 {Scalar: []string{"upgradeRequest.expiresAt"}, Group: []string{"upgradeRequest.expiresAt"}},
	"installTokens":          {Scalar: []string{"tokenHash", "expiresAt", "siteSlug"}, Group: []string{"tokenHash", "expiresAt"}},
	"activities": {
		Scalar:    []string{"_tsMs", "seq", "siteSlug", "actor", "origin", "uploadedVia", "type"},
		Array:     []string{"_typeKeys"},
		Composite: [][]string{actTail, t(actTail, "siteSlug"), t(actTail, "actor"), t(actTail, "origin"), t(actTail, "uploadedVia")},
	},
	"transfers": {
		Scalar:    []string{"siteSlug", "direction", "status", "createdAt", "expiresAt"},
		Composite: [][]string{{"siteSlug", "direction", "createdAt"}, {"direction", "status"}},
		Group:     []string{"expiresAt"},
	},
	"files":                   {Scalar: []string{"state", "unpublishedAt"}},
	"uptime":                  {Scalar: []string{"date"}, Group: []string{"date"}},
	"volumeContent":           {Scalar: []string{"_open", "siteSlug", "versionCount"}, Composite: [][]string{{"siteSlug", "_open"}}, Group: []string{"versionCount"}},
	"volumeContent__versions": {Scalar: []string{"origin", "seq", "agentSnapshotId"}},
}

// IxColumn is ixColumn: `ix_<field>` with dots as underscores.
func IxColumn(field string) string { return "ix_" + strings.ReplaceAll(field, ".", "_") }

func uniq(lists ...[]string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, l := range lists {
		for _, s := range l {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// MergeRegistry is mergeRegistry (extra entries get all four lists, like the TS result).
func MergeRegistry(base, extra IndexRegistry) IndexRegistry {
	if extra == nil {
		return base
	}
	out := IndexRegistry{}
	for k, v := range base {
		out[k] = v
	}
	for t, s := range extra {
		b := out[t]
		out[t] = TableIndexSpec{
			Scalar:    uniq(b.Scalar, s.Scalar),
			Array:     uniq(b.Array, s.Array),
			Composite: append(append([][]string{}, b.Composite...), s.Composite...),
			Group:     uniq(b.Group, s.Group),
		}
	}
	return out
}

// specJSON is JSON.stringify(registry[table] ?? {}).
func specJSON(s TableIndexSpec) string {
	m := doc.New()
	if s.Scalar != nil {
		m.Set("scalar", s.Scalar)
	}
	if s.Array != nil {
		m.Set("array", s.Array)
	}
	if s.Composite != nil {
		c := make([]any, len(s.Composite))
		for i, x := range s.Composite {
			c[i] = x
		}
		m.Set("composite", c)
	}
	if s.Group != nil {
		m.Set("group", s.Group)
	}
	return doc.MarshalString(m)
}
