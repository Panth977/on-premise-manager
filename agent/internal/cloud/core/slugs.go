package core

import (
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

func slugColl(kind string) *driver.CollectionRef {
	if kind == "template" {
		return cloud.Col.Templates()
	}
	return cloud.Col.Deployments()
}

func slugPrefix(kind string) string {
	if kind == "template" {
		return "t-"
	}
	return "d-"
}

// TakenSlugs is takenSlugs (kind: template | deployment; tx may be nil).
func TakenSlugs(kind string, tx *driver.Tx) (map[string]bool, error) {
	q := slugColl(kind).Select("slug")
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
	out := map[string]bool{}
	for _, d := range s.Docs {
		if v := d.Data().Str("slug"); v != "" {
			out[v] = true
		}
	}
	return out, nil
}

func validSlug(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && shared.RE.Slug.MatchString(s)
}

const slugFormatMsg = "slug must match ^[a-z][a-z0-9-]{1,39}$"

// NewSlug is newSlug: pass doc.Undefined or nil for no requested slug.
func NewSlug(tx *driver.Tx, kind, label string, requested any) (string, error) {
	taken, err := TakenSlugs(kind, tx)
	if err != nil {
		return "", err
	}
	if !nullish(requested) {
		s, ok := validSlug(requested)
		if !ok {
			return "", cloud.E.Bad("slug", "V_FORMAT", slugFormatMsg)
		}
		if taken[s] {
			return "", cloud.E.Conflict("slug "+s+" is already used by another "+kind, doc.Obj("reason", "slug-taken", "slug", s))
		}
		return s, nil
	}
	return shared.UniqueSlug(shared.SlugifyLabel(label, slugPrefix(kind)), taken), nil
}

// CheckRequestedSlug is checkRequestedSlug: pass doc.Undefined for a missing field.
func CheckRequestedSlug(v *cloud.V, requested any) {
	if requested == doc.Undefined {
		return
	}
	if _, ok := validSlug(requested); !ok {
		v.Err("slug", "V_FORMAT", slugFormatMsg)
	}
}

// EnsureSlug is ensureSlug: the stored slug; a legacy document gets one now.
func EnsureSlug(kind, id string) (string, error) {
	ref := slugColl(kind).Doc(id)
	s0, err := ref.Get()
	if err != nil {
		return "", err
	}
	if !s0.Exists() {
		return "", cloud.E.NotFound(kind + " not found")
	}
	if s, ok := validSlug(s0.Data().Val("slug")); ok {
		return s, nil
	}
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (string, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return "", err
		}
		if again, ok := validSlug(s.Data().Val("slug")); ok {
			return again, nil
		}
		taken, err := TakenSlugs(kind, tx)
		if err != nil {
			return "", err
		}
		slug := shared.UniqueSlug(shared.SlugifyLabel(JSString(or(s.Data(), "label", "")), slugPrefix(kind)), taken)
		tx.Update(ref, doc.Obj("slug", slug))
		return slug, nil
	})
}

// SlugOf is cloud.SlugOf: the read-side fallback of slugs.ts lives in codec.go.
var SlugOf = cloud.SlugOf
