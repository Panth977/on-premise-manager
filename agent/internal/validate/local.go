package validate

import (
	"encoding/json"
	"sort"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// sameCanonical compares two values by their canonical JSON (key order of decoded documents does not matter).
func sameCanonical(a, b any) bool {
	x, err1 := canonical.Canonicalize(a)
	y, err2 := canonical.Canonicalize(b)
	return err1 == nil && err2 == nil && x == y
}

func sameSet(a, b []string) bool {
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return sameJSON(x, y)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CheckLocalEdit compares the head config with a locally edited one (V_LOCAL_* errors). §21 #113: a site is standalone
// (Portainer-like): variables, secrets, files, containers (any field, incl. the image), volumes, edge, settings, labels
// and dev-tool presets may be added, changed and removed locally. Kept: the list of sites is the deployment's, and a
// volume's persist-to-cloud setting belongs to the cloud content versions (§21 #101, volcontent.go).
func CheckLocalEdit(before, after *model.Config) []model.ValidationIssue {
	is := NewIssues(ModeAgent)
	slugs := func(c *model.Config) []string {
		out := []string{}
		for _, s := range c.Sites {
			out = append(out, s.SiteSlug)
		}
		return out
	}
	if !sameSet(slugs(before), slugs(after)) {
		is.err("sites", "V_LOCAL_SITES", "sites cannot be added or removed on a site")
	}
	for _, s := range after.Sites {
		s := s
		for i := range before.Sites {
			if before.Sites[i].SiteSlug == s.SiteSlug {
				checkLocalVolumeContent(&before.Sites[i], &s, is)
			}
		}
	}
	return is.List
}
