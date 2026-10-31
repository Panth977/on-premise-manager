package shared

import (
	"regexp"
	"slices"
	"strings"

	"opm/agent/internal/cloud/doc"
)

// IMAGE_IDENTITY_FIELDS is IMAGE_IDENTITY_FIELDS.
var IMAGE_IDENTITY_FIELDS = []string{"version", "serviceSlug", "source", "digest", "platform", "import", "createdAt", "createdBy", "updatedAt", "updatedBy"}

// IMAGE_EDITABLE_FIELDS is IMAGE_EDITABLE_FIELDS.
var IMAGE_EDITABLE_FIELDS = []string{"kind", "entrypoint", "command", "workdir", "user", "ports", "env", "volumes", "healthcheck", "stopTimeoutSec", "description"}

// IMAGE_NAME_KINDS is IMAGE_NAME_KINDS.
var IMAGE_NAME_KINDS = []string{"port", "env", "volume"}

var jNameList = map[string]string{"port": "ports", "env": "env", "volume": "volumes"}

func jNames(img any, kind string) []any {
	list := jArr(jGet(img, jNameList[kind]), "map", "before[LIST[kind]]")
	out := make([]any, len(list))
	for i, x := range list {
		out[i] = jGet(x, "name")
	}
	return out
}

func jIncludes(list []any, v any) bool {
	return slices.ContainsFunc(list, func(x any) bool { return jEq(x, v) })
}

// ImageNameChanges is imageNameChanges; pass doc.Undefined for renames when the field is absent (null is an issue, as in TS).
func ImageNameChanges(before, after *doc.Map, renames any) (out *doc.Map, err error) {
	defer jCatch(&err)
	issues := []any{}
	bad := func(path, message string) {
		issues = append(issues, doc.Obj("path", path, "code", "V_RENAME", "message", message, "severity", "error"))
	}
	if renames == doc.Undefined {
		renames = []any{}
	}
	list, ok := renames.([]any)
	if !ok {
		bad("renames", "renames must be a list of { kind, from, to }")
		list = nil
	}
	renamed, removed, added, retargeted := []any{}, []any{}, []any{}, []any{}
	for i, r := range list {
		at := "renames[" + jStr(float64(i)) + "]"
		if !doc.Truthy(r) || !jIsObj(r) || !jIsOneOf(jGet(r, "kind"), IMAGE_NAME_KINDS...) {
			bad(at, "a rename is { kind: port | env | volume, from, to }")
			continue
		}
		kind, _ := jGet(r, "kind").(string)
		from, okf := jGet(r, "from").(string)
		to, okt := jGet(r, "to").(string)
		if !okf || !okt {
			bad(at, "a rename is { kind: port | env | volume, from, to }")
			continue
		}
		oldNames, newNames := jNames(before, kind), jNames(after, kind)
		if from == to {
			bad(at, kind+" "+from+": from and to are the same")
			continue
		}
		if !jIncludes(oldNames, from) {
			bad(at, kind+` "`+from+`" is not a `+kind+` of the image`)
			continue
		}
		if !jIncludes(newNames, to) {
			bad(at, kind+` "`+to+`" is not in the new `+jNameList[kind]+` list`)
			continue
		}
		twice := slices.ContainsFunc(list[:i], func(x any) bool {
			return jIs(jOpt(x, "kind"), kind) && (jEq(jGet(x, "from"), from) || jEq(jGet(x, "to"), to))
		})
		if twice {
			bad(at, kind+" "+from+" → "+to+": renamed twice")
			continue
		}
		renamed = append(renamed, doc.Obj("kind", kind, "from", from, "to", to))
	}
	for _, kind := range IMAGE_NAME_KINDS {
		oldNames := jNames(before, kind)
		used := map[string]bool{}
		for _, n := range jNames(after, kind) {
			var origin any = doc.Undefined
			for _, r := range renamed {
				if rm := r.(*doc.Map); rm.Str("kind") == kind && jIs(n, rm.Str("to")) {
					origin = rm.Val("from")
					break
				}
			}
			if origin == doc.Undefined && jIncludes(oldNames, n) {
				origin = n
			}
			if origin == doc.Undefined {
				added = append(added, doc.Obj("kind", kind, "name", n))
				continue
			}
			if used[jKey(origin)] {
				bad("renames", kind+` "`+jStr(origin)+`" is both kept and renamed — rename it or keep it, not both`)
			}
			used[jKey(origin)] = true
			if kind == "port" {
				a := jFind(before.Arr("ports"), func(p any) bool { return jEq(jGet(p, "name"), origin) })
				b := jFind(after.Arr("ports"), func(p any) bool { return jEq(jGet(p, "name"), n) })
				if jStr(jGet(a, "port")) != jStr(jGet(b, "port")) || !jEq(jGet(a, "protocol"), jGet(b, "protocol")) {
					retargeted = append(retargeted, doc.Obj("name", n, "from", jStr(jGet(a, "port"))+"/"+jStr(jGet(a, "protocol")), "to", jStr(jGet(b, "port"))+"/"+jStr(jGet(b, "protocol"))))
				}
			}
		}
		for _, o := range oldNames {
			if !used[jKey(o)] {
				removed = append(removed, doc.Obj("kind", kind, "name", o))
			}
		}
	}
	return doc.Obj("renamed", renamed, "removed", removed, "added", added, "portRetargeted", retargeted, "issues", issues), nil
}

var jReBlock = regexp.MustCompile(`\{\{[^\n]*?\}\}`)

// jRefBefore is the look-behind class, jRefAfter the look-ahead class of the reference regexes.
func jRefBefore(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}
func jRefAfter(c byte) bool { return jRefBefore(c) && c != '.' }

// jRefScan replaces, left to right, every occurrence of a candidate (tried in order, as a regex alternation with the two
// lookarounds; Go has no look-behind).
func jRefScan(s string, cands []string, repl func(c int) string) string {
	var b strings.Builder
	i := 0
scan:
	for i < len(s) {
		if i == 0 || !jRefBefore(s[i-1]) {
			for n, c := range cands {
				if strings.HasPrefix(s[i:], c) && (i+len(c) == len(s) || !jRefAfter(s[i+len(c)])) {
					b.WriteString(repl(n))
					i += len(c)
					continue scan
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func jHasRef(text any, cand string) bool {
	s, ok := text.(string)
	if !ok || !strings.Contains(s, "{{") {
		return false
	}
	for _, block := range jReBlock.FindAllString(s, -1) {
		if jRefScan(block, []string{cand}, func(int) string { return "\x00" }) != block {
			return true
		}
	}
	return false
}

func jMapBlocks(text string, f func(string) string) string {
	return jReBlock.ReplaceAllStringFunc(text, f)
}

func jSlugOf(c any) any { return jCoal(jGet(c, "containerSlug"), jGet(c, "serviceSlug")) }

func jIsOf(c any, serviceSlug, version string) bool {
	return jIs(jGet(c, "serviceSlug"), serviceSlug) && jIs(jGet(c, "version"), version)
}

func jList(v any, src string) []any { return jIter(jCoal(v, []any{}), src) }

// ImageNameUses is imageNameUses: [{ siteSlug, containerSlug, field }].
func ImageNameUses(cfg *doc.Map, serviceSlug, version, kind, name string) (out []any, err error) {
	defer jCatch(&err)
	out = []any{}
	sites := jList(jGet(cfg, "sites"), "sites")
	for _, site := range sites {
		sc := jGet(site, "config")
		for _, c := range jList(jOpt(sc, "containers"), "sc?.containers ?? []") {
			if !jIsOf(c, serviceSlug, version) {
				continue
			}
			cs := jSlugOf(c)
			at := func(field string) {
				out = append(out, doc.Obj("siteSlug", jGet(site, "siteSlug"), "containerSlug", cs, "field", field))
			}
			switch kind {
			case "port":
				for _, p := range jList(jGet(c, "ports"), "(c.ports ?? [])") {
					if jIs(jGet(p, "portName"), name) {
						at("ports[" + name + "]")
						break
					}
				}
				hc := jGet(c, "healthcheck")
				if doc.Truthy(hc) && jIsOneOf(jGet(hc, "type"), "http", "tcp") && jIs(jGet(hc, "portName"), name) {
					at("healthcheck.portName")
				}
				for _, r := range jList(jOpt(jGet(sc, "edge"), "routes"), "sc.edge?.routes ?? []") {
					act := jGet(r, "action")
					if jIs(jOpt(act, "type"), "proxy") && jEq(jGet(act, "containerSlug"), cs) && jIs(jGet(act, "portName"), name) {
						at("edge.routes[" + jStr(jGet(r, "routeId")) + "].portName")
					}
				}
				ref := "site." + jStr(jGet(site, "siteSlug")) + ".port." + jStr(cs) + "." + name
				for _, s2 := range sites {
					for _, c2 := range jList(jOpt(jGet(s2, "config"), "containers"), "s2.config?.containers ?? []") {
						for _, e := range jList(jGet(c2, "env"), "c2.env ?? []") {
							if jHasRef(jGet(e, "value"), ref) {
								out = append(out, doc.Obj("siteSlug", jGet(s2, "siteSlug"), "containerSlug", jSlugOf(c2), "field", "env["+jStr(jGet(e, "name"))+"].value ("+ref+")"))
							}
						}
					}
				}
			case "env":
				envs := jList(jGet(c, "env"), "(c.env ?? [])")
				for _, e := range envs {
					if jIs(jGet(e, "name"), name) {
						at("env[" + name + "]")
						break
					}
				}
				ref := "container.env." + name
				for _, e := range envs {
					if jHasRef(jGet(e, "value"), ref) {
						at("env[" + jStr(jGet(e, "name")) + "].value")
					}
				}
				for i, m := range jList(jGet(c, "volumes"), "(c.volumes ?? [])") {
					if jHasRef(jGet(m, "mountPath"), ref) {
						at("volumes[" + jStr(jCoal(jGet(m, "imageVolume"), float64(i))) + "].mountPath")
					}
				}
			default:
				for _, m := range jList(jGet(c, "volumes"), "(c.volumes ?? [])") {
					if jIs(jGet(m, "imageVolume"), name) {
						at("volumes[" + name + "]")
						break
					}
				}
			}
		}
	}
	return out, nil
}

// jRenames is new Map(renames.filter(kind).map([from, to])): keys in first-seen order, last value.
type jRenames struct {
	keys []string
	to   map[string]string
}

func jRenameMap(renames []any, kind string) jRenames {
	m := jRenames{to: map[string]string{}}
	for _, r := range renames {
		if !jIs(jGet(r, "kind"), kind) {
			continue
		}
		from, to := jStr(jGet(r, "from")), jStr(jGet(r, "to"))
		if _, ok := m.to[from]; !ok {
			m.keys = append(m.keys, from)
		}
		m.to[from] = to
	}
	return m
}

func (m jRenames) has(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	_, ok = m.to[s]
	return ok
}

func jSet(obj any, k string, v any) {
	if m, ok := obj.(*doc.Map); ok && m != nil {
		m.Set(k, v)
	}
}

// jEnvFix rewrites container.env.<OLD> inside the {{ }} blocks of a text.
func jEnvFix(envs jRenames) func(string) string {
	cands := make([]string, len(envs.keys))
	for i, k := range envs.keys {
		cands[i] = "container.env." + k
	}
	return func(t string) string {
		return jMapBlocks(t, func(b string) string {
			return jRefScan(b, cands, func(n int) string { return "container.env." + envs.to[envs.keys[n]] })
		})
	}
}

// RenameImageNames is renameImageNames: { config, count, issues } (config is a deep copy).
func RenameImageNames(cfg *doc.Map, serviceSlug, version string, renames []any) (out *doc.Map, err error) {
	defer jCatch(&err)
	next := cfg.Clone()
	issues := []any{}
	count := 0.0
	ports, envs, vols := jRenameMap(renames, "port"), jRenameMap(renames, "env"), jRenameMap(renames, "volume")
	done := func() *doc.Map { return doc.Obj("config", next, "count", count, "issues", issues) }
	if len(ports.keys) == 0 && len(envs.keys) == 0 && len(vols.keys) == 0 {
		return done(), nil
	}
	sites := jList(jGet(next, "sites"), "sites")
	for _, site := range sites {
		sc := jGet(site, "config")
		for _, c := range jList(jOpt(sc, "containers"), "sc?.containers ?? []") {
			if !jIsOf(c, serviceSlug, version) {
				continue
			}
			cs := jSlugOf(c)
			for _, p := range jList(jGet(c, "ports"), "c.ports ?? []") {
				if pn := jGet(p, "portName"); ports.has(pn) {
					jSet(p, "portName", ports.to[pn.(string)])
					count++
				}
			}
			hc := jGet(c, "healthcheck")
			if doc.Truthy(hc) && jIsOneOf(jGet(hc, "type"), "http", "tcp") && ports.has(jGet(hc, "portName")) {
				jSet(hc, "portName", ports.to[jGet(hc, "portName").(string)])
				count++
			}
			for _, r := range jList(jOpt(jGet(sc, "edge"), "routes"), "sc.edge?.routes ?? []") {
				act := jGet(r, "action")
				if jIs(jOpt(act, "type"), "proxy") && jEq(jGet(act, "containerSlug"), cs) && ports.has(jGet(act, "portName")) {
					jSet(act, "portName", ports.to[jGet(act, "portName").(string)])
					count++
				}
			}
			names := map[string]bool{}
			env := jList(jGet(c, "env"), "c.env ?? []")
			for _, e := range env {
				names[jKey(jGet(e, "name"))] = true
			}
			for _, e := range env {
				n := jGet(e, "name")
				if !envs.has(n) {
					continue
				}
				to := envs.to[n.(string)]
				if names[jKey(to)] && !envs.has(to) {
					issues = append(issues, doc.Obj("path", "sites["+jStr(jGet(site, "siteSlug"))+"].config.containers["+jStr(cs)+"].env["+to+"]", "code", "V_DUPLICATE",
						"message", "container "+jStr(cs)+" on site "+jStr(jGet(site, "siteSlug"))+" already sets "+to+": rename "+n.(string)+" there first", "severity", "error"))
					continue
				}
				jSet(e, "name", to)
				count++
			}
			mounts := jList(jGet(c, "volumes"), "c.volumes ?? []")
			for _, m := range mounts {
				if iv := jGet(m, "imageVolume"); doc.Truthy(iv) && vols.has(iv) {
					jSet(m, "imageVolume", vols.to[iv.(string)])
					count++
				}
			}
			if len(envs.keys) > 0 {
				fix := jEnvFix(envs)
				rewrite := func(obj any, k string) {
					if t, ok := jGet(obj, k).(string); ok && strings.Contains(t, "{{") {
						if v := fix(t); v != t {
							jSet(obj, k, v)
							count++
						}
					}
				}
				for _, e := range env {
					rewrite(e, "value")
				}
				for _, m := range mounts {
					rewrite(m, "mountPath")
				}
			}
		}
	}
	if len(ports.keys) > 0 {
		var prefixes []string
		for _, site := range sites {
			for _, c := range jList(jOpt(jGet(site, "config"), "containers"), "site.config?.containers ?? []") {
				if jIsOf(c, serviceSlug, version) {
					prefixes = append(prefixes, "site."+jStr(jGet(site, "siteSlug"))+".port."+jStr(jSlugOf(c)))
				}
			}
		}
		if len(prefixes) > 0 {
			var cands, repl []string
			for _, pre := range prefixes {
				for _, p := range ports.keys {
					cands = append(cands, pre+"."+p)
					repl = append(repl, pre+"."+ports.to[p])
				}
			}
			for _, s2 := range sites {
				for _, c2 := range jList(jOpt(jGet(s2, "config"), "containers"), "s2.config?.containers ?? []") {
					for _, e := range jList(jGet(c2, "env"), "c2.env ?? []") {
						if t, ok := jGet(e, "value").(string); ok && strings.Contains(t, "{{") {
							v := jMapBlocks(t, func(b string) string { return jRefScan(b, cands, func(n int) string { return repl[n] }) })
							if v != t {
								jSet(e, "value", v)
								count++
							}
						}
					}
				}
			}
		}
	}
	return done(), nil
}

// RenameImageSelfRefs is renameImageSelfRefs (img itself when nothing is renamed).
func RenameImageSelfRefs(img *doc.Map, renames []any) (out *doc.Map, err error) {
	defer jCatch(&err)
	envs := jRenameMap(renames, "env")
	if len(envs.keys) == 0 {
		return img, nil
	}
	f := jEnvFix(envs)
	fix := func(t any) any {
		s, ok := t.(string)
		if !ok {
			if jNull(t) {
				jGet(t, "includes")
			}
			jThrow("t.includes is not a function")
		}
		if strings.Contains(s, "{{") {
			return f(s)
		}
		return s
	}
	env := []any{}
	for _, e := range jArr(jGet(img, "env"), "map", "img.env") {
		if v, ok := jGet(e, "value").(string); ok {
			e = jSpread(doc.New(), e).Set("value", fix(v))
		}
		env = append(env, e)
	}
	vols := []any{}
	for _, v := range jArr(jGet(img, "volumes"), "map", "img.volumes") {
		vols = append(vols, jSpread(doc.New(), v).Set("mountPath", fix(jGet(v, "mountPath"))))
	}
	return img.Shallow().Set("env", env).Set("volumes", vols), nil
}

// ImageNames is imageNames.
func ImageNames(i *doc.Map) *doc.Map {
	return doc.Obj("ports", i.Val("ports"), "env", i.Val("env"), "volumes", i.Val("volumes"))
}
