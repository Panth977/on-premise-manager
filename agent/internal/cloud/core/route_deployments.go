package core

import (
	"sort"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

func pick(m *doc.Map, keys ...string) *doc.Map {
	out := doc.New()
	for _, k := range keys {
		out.Set(k, present(m, k))
	}
	return out
}

// ProposalMeta is proposalMeta of routes/deployments.ts.
func ProposalMeta(p *doc.Map) *doc.Map {
	return pick(p, "revId", "parentRevId", "seq", "summary", "status", "origin", "kind", "createdAt", "createdBy")
}

// RevisionSummary is revisionSummary.
func RevisionSummary(r *doc.Map) *doc.Map {
	return pick(r, "revId", "seq", "parentRevId", "origin", "actor", "createdAt", "hash", "summary", "kind")
}

// LoadRevision is loadRevision: nil when missing (tx may be nil).
func LoadRevision(deploymentID, revID string, tx *driver.Tx) (*doc.Map, error) {
	s, err := getIn(tx, cloud.Col.Revisions(deploymentID).Doc(revID))
	if err != nil || !s.Exists() {
		return nil, err
	}
	return cloud.DecodeRevision(s.Data()), nil
}

// OpenProposals is openProposals: newest first (tx may be nil).
func OpenProposals(deploymentID string, tx *driver.Tx) ([]*doc.Map, error) {
	q := cloud.Col.Proposals(deploymentID).Where("status", "in", OPEN_STATUSES)
	var s *driver.QuerySnap
	var err error
	if tx != nil {
		s, err = tx.Query(q)
	} else {
		s, err = q.Get()
	}
	if err != nil {
		return nil, err
	}
	out := make([]*doc.Map, len(s.Docs))
	for i, d := range s.Docs {
		out[i] = d.Data()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Str("revId") > out[j].Str("revId") })
	return out, nil
}

func genSecret(gen *doc.Map) string {
	return cloud.GenerateSecret(gen.Str("charset"), int(gen.Int("length")))
}

// MergeSecrets is mergeSecrets: SecretDefInput items merged with the stored values (plainValue semantics, generateNow).
func MergeSecrets(items, current []any) ([]any, error) {
	now := doc.NowISO()
	out := make([]any, len(items))
	for i, x := range items {
		it := x.(*doc.Map)
		slug := it.Val("secretSlug")
		var cur *doc.Map
		for _, s := range mapsOf(current) {
			if s.Val("secretSlug") == slug {
				cur = s
				break
			}
		}
		def := it.Without("plainValue", "generateNow", "value", "rotatedAt", "isSet")
		l, err := NormSecretDefs([]any{def})
		if err != nil {
			return nil, err
		}
		base := l[0].(*doc.Map)
		gen := base.Map("generate")
		at := "secrets[" + JSString(present(it, "secretSlug")) + "]"
		plain, hasPlain := it.Get("plainValue")
		if plain == doc.Undefined {
			hasPlain = false
		}
		var value any
		switch {
		case it.Truthy("generateNow"):
			if !base.Truthy("generate") {
				return nil, cloud.E.Bad(at+".generateNow", "V_REQUIRED", "generateNow needs a generate rule")
			}
			value = genSecret(gen)
		case !hasPlain:
			if cur != nil {
				value = present(cur, "value")
			} else if base.Truthy("generate") {
				value = genSecret(gen)
			}
		case plain == nil:
		default:
			s, ok := plain.(string)
			if !ok {
				return nil, cloud.E.Bad(at+".plainValue", "V_TYPE", "plainValue must be a string, null or absent")
			}
			value = s
		}
		var rotated any
		if cur == nil || or(cur, "value", nil) != value {
			if value != nil {
				rotated = now
			}
		} else {
			rotated = or(cur, "rotatedAt", nil)
		}
		out[i] = base.Shallow().Set("value", value).Set("rotatedAt", rotated)
	}
	return out, nil
}
