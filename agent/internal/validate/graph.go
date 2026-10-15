package validate

import (
	"sort"

	"opm/agent/internal/model"
)

// TopoOrder is a Kahn topological sort (dependencies first), ties broken by key. cycle is non-nil iff there is a cycle
// (then order is nil and cycle lists the nodes not ordered, in node order).
func TopoOrder(nodes []string, dependsOn func(string) []string, key func(string) string) (order []string, cycle []string) {
	if key == nil {
		key = func(n string) string { return n }
	}
	indeg := map[string]int{}
	users := map[string][]string{}
	for _, n := range nodes {
		indeg[n] = 0
		users[n] = nil
	}
	for _, n := range nodes {
		seen := map[string]bool{}
		for _, d := range dependsOn(n) {
			if seen[d] {
				continue
			}
			seen[d] = true
			if _, ok := indeg[d]; !ok || d == n {
				continue
			}
			indeg[n]++
			users[d] = append(users[d], n)
		}
	}
	less := func(r []string) func(i, j int) bool { return func(i, j int) bool { return key(r[i]) < key(r[j]) } }
	ready := []string{}
	for _, n := range nodes {
		if indeg[n] == 0 {
			ready = append(ready, n)
		}
	}
	sort.SliceStable(ready, less(ready))
	order = []string{}
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		order = append(order, n)
		for _, u := range users[n] {
			indeg[u]--
			if indeg[u] == 0 {
				ready = append(ready, u)
				sort.SliceStable(ready, less(ready))
			}
		}
	}
	if len(order) == len(nodes) {
		return order, nil
	}
	in := map[string]bool{}
	for _, n := range order {
		in[n] = true
	}
	cycle = []string{}
	for _, n := range nodes {
		if !in[n] {
			cycle = append(cycle, n)
		}
	}
	return nil, cycle
}

// SiteOrder is the site apply order (site depends first), tie-break by slug.
func SiteOrder(cfg *model.Config) (order, cycle []string) {
	by := map[string]*model.Site{}
	nodes := []string{}
	for i := range cfg.Sites {
		by[cfg.Sites[i].SiteSlug] = &cfg.Sites[i]
		nodes = append(nodes, cfg.Sites[i].SiteSlug)
	}
	return TopoOrder(nodes, func(n string) []string {
		out := []string{}
		for _, d := range by[n].Config.Depends {
			out = append(out, d.SiteSlug)
		}
		return out
	}, nil)
}

// ContainerOrder is the start order of the enabled containers of a site (serviceSlugs, sorted by slug on ties).
// ContainerOrder: start order inside a site (enabled containers, by containerSlug); services is unused (compat).
func ContainerOrder(sc *model.SiteConfig, _ map[string]string) (order, cycle []string) {
	by := map[string]*model.Container{}
	nodes := []string{}
	for i := range sc.Containers {
		c := &sc.Containers[i]
		if !c.Enabled {
			continue
		}
		by[c.ContainerSlug] = c
		nodes = append(nodes, c.ContainerSlug)
	}
	return TopoOrder(nodes, func(n string) []string {
		out := []string{}
		for _, d := range by[n].Depends {
			out = append(out, d.ContainerSlug)
		}
		return out
	}, func(n string) string { return n })
}
