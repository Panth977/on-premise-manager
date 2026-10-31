package shared

import (
	"strings"

	"opm/agent/internal/cloud/doc"
)

// Port of shared/src/diff.ts: the config diff (DiffChange rows, in the TS order, with the TS key order).

// DEVTOOL_VALUE_CHANGED_NOTE is TS DEVTOOL_VALUE_CHANGED_NOTE.
const DEVTOOL_VALUE_CHANGED_NOTE = "static password / token value changed (hidden)"

// DEVTOOL_VALUE_MASK is TS DEVTOOL_VALUE_MASK.
const DEVTOOL_VALUE_MASK = "••••"

// VOLUME_CONTENT_CHANGED_NOTE is TS VOLUME_CONTENT_CHANGED_NOTE.
const VOLUME_CONTENT_CHANGED_NOTE = "starter content — written only into new / empty volumes (persist-to-cloud volumes: use Send to site)"

// VOLUME_SEED_CHANGED_NOTE is TS VOLUME_SEED_CHANGED_NOTE.
const VOLUME_SEED_CHANGED_NOTE = "seed changed — applies only to new / empty volumes (existing data is never touched; use \"Reset to starter content…\" on the site)"

// VOLUME_KIND_CHANGED_NOTE is TS VOLUME_KIND_CHANGED_NOTE.
const VOLUME_KIND_CHANGED_NOTE = "kind changed — a site refuses this when the volume already holds data (use a new volume name instead)"

// dfJ is TS j (JSON.stringify): undefined gets a value no JSON text equals, so undefined === undefined stays true.
func dfJ(v any) string {
	if brIsUndef(v) {
		return "\x00undefined"
	}
	return doc.MarshalString(v)
}

func dfSecretState(s *doc.Map) string {
	if s == nil {
		return "not set"
	}
	if v, ok := s.Get("isSet"); ok {
		if b, isBool := v.(bool); isBool {
			if b {
				return "set"
			}
			return "not set"
		}
	}
	if brEmptyVal(s.Val("value")) {
		return "not set"
	}
	return "set"
}

func dfChange(section string, siteSlug any, path, kind string, before, after any) *doc.Map {
	return doc.Obj("section", section, "siteSlug", siteSlug, "path", path, "kind", kind, "before", before, "after", after)
}

func dfSome(list []any, f string, v any) bool {
	for _, x := range list {
		m, _ := x.(*doc.Map)
		if brEq(brUnd(m, f), v) {
			return true
		}
	}
	return false
}

func dfFind(list []any, f string, v any) *doc.Map {
	for _, x := range list {
		m, _ := x.(*doc.Map)
		if brEq(brUnd(m, f), v) {
			return m
		}
	}
	return nil
}

func dfWith(m *doc.Map, kv ...any) *doc.Map {
	o := m.Shallow()
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// DiffConfigs is TS diffConfigs (before nil = everything added; serviceSlug nil = identity; secretChanged optional).
func DiffConfigs(before, after *doc.Map, serviceSlug func(string) string, secretChanged func(string) bool) []any {
	out := []any{}
	b := before
	if b == nil {
		b = brEmptyConfig()
	}
	if serviceSlug == nil {
		serviceSlug = func(s string) string { return s }
	}
	push := func(c *doc.Map) { out = append(out, c) }
	// variables
	for _, x := range after.Arr("variables") {
		v := x.(*doc.Map)
		slug := brUnd(v, "variableSlug")
		path := brJSString(slug)
		o := dfFind(b.Arr("variables"), "variableSlug", slug)
		if o == nil {
			push(dfChange("variables", nil, path, "added", nil, brUnd(v, "value")))
		} else if dfJ(o) != dfJ(v) {
			if !brEq(brUnd(o, "type"), brUnd(v, "type")) || dfJ(dfWith(o, "value", 0.0)) != dfJ(dfWith(v, "value", 0.0)) {
				push(dfChange("variables", nil, path, "changed", o, v))
			} else {
				push(dfChange("variables", nil, path, "changed", brUnd(o, "value"), brUnd(v, "value")))
			}
		}
	}
	for _, x := range b.Arr("variables") {
		o := x.(*doc.Map)
		if !dfSome(after.Arr("variables"), "variableSlug", brUnd(o, "variableSlug")) {
			push(dfChange("variables", nil, brJSString(brUnd(o, "variableSlug")), "removed", brUnd(o, "value"), nil))
		}
	}
	// secrets
	for _, x := range after.Arr("secrets") {
		s := x.(*doc.Map)
		slug := brUnd(s, "secretSlug")
		path := brJSString(slug)
		o := dfFind(b.Arr("secrets"), "secretSlug", slug)
		if o == nil {
			push(dfChange("secrets", nil, path, "added", nil, dfSecretState(s)))
			continue
		}
		defChanged := dfJ(dfWith(o, "value", nil, "isSet", nil, "rotatedAt", nil)) != dfJ(dfWith(s, "value", nil, "isSet", nil, "rotatedAt", nil))
		var valueChanged bool
		switch {
		case secretChanged != nil:
			valueChanged = secretChanged(path)
		case s.Has("isSet"):
			valueChanged = dfSecretState(o) != dfSecretState(s) || !brEq(brUnd(o, "rotatedAt"), brUnd(s, "rotatedAt"))
		default:
			valueChanged = !brEq(brUnd(o, "value"), brUnd(s, "value"))
		}
		if defChanged || valueChanged {
			state := "not changed"
			if valueChanged {
				state = "changed"
			}
			push(dfChange("secrets", nil, path, "changed", dfSecretState(o), state))
		}
	}
	for _, x := range b.Arr("secrets") {
		o := x.(*doc.Map)
		if !dfSome(after.Arr("secrets"), "secretSlug", brUnd(o, "secretSlug")) {
			push(dfChange("secrets", nil, brJSString(brUnd(o, "secretSlug")), "removed", dfSecretState(o), nil))
		}
	}
	// files
	for _, x := range after.Arr("files") {
		f := x.(*doc.Map)
		slug := brUnd(f, "fileSlug")
		path := brJSString(slug)
		o := dfFind(b.Arr("files"), "fileSlug", slug)
		if o == nil {
			push(dfChange("files", nil, path, "added", nil, brUnd(f, "content")))
		} else if dfJ(o) != dfJ(f) {
			if dfJ(dfWith(o, "content", nil)) == dfJ(dfWith(f, "content", nil)) {
				push(dfChange("files", nil, path, "changed", brUnd(o, "content"), brUnd(f, "content")))
			} else {
				push(dfChange("files", nil, path, "changed", o, f))
			}
		}
	}
	for _, x := range b.Arr("files") {
		o := x.(*doc.Map)
		if !dfSome(after.Arr("files"), "fileSlug", brUnd(o, "fileSlug")) {
			push(dfChange("files", nil, brJSString(brUnd(o, "fileSlug")), "removed", brUnd(o, "content"), nil))
		}
	}
	// sites
	for _, x := range after.Arr("sites") {
		s := x.(*doc.Map)
		slug := brUnd(s, "siteSlug")
		ss := brJSString(slug)
		cfg := s.Map("config")
		o := dfFind(b.Arr("sites"), "siteSlug", slug)
		if o == nil {
			push(dfChange("sites", slug, ss, "added", nil, doc.Obj("label", brUnd(s, "label"), "host", brUnd(cfg, "host"))))
			for _, y := range cfg.Arr("volumes") {
				v := y.(*doc.Map)
				push(dfChange("volumes", slug, ss+"/volumes/"+brJSString(brUnd(v, "volumeSlug")), "added", nil, v))
			}
			for _, y := range cfg.Arr("containers") {
				c := y.(*doc.Map)
				push(dfChange("containers", slug, ss+"/"+serviceSlug(brJSString(brUnd(c, "containerSlug"))), "added", nil, doc.Obj("service", brUnd(c, "serviceSlug"), "version", brUnd(c, "version"))))
			}
			dfDevTools(nil, cfg.Arr("devTools"), slug, push)
			continue
		}
		dfSite(o, s, serviceSlug, push)
	}
	for _, x := range b.Arr("sites") {
		o := x.(*doc.Map)
		slug := brUnd(o, "siteSlug")
		if !dfSome(after.Arr("sites"), "siteSlug", slug) {
			push(dfChange("sites", slug, brJSString(slug), "removed", doc.Obj("label", brUnd(o, "label"), "host", brUnd(o.Map("config"), "host")), nil))
			dfDevTools(o.Map("config").Arr("devTools"), nil, slug, push)
		}
	}
	return out
}

func dfSite(o, s *doc.Map, slug func(string) string, push func(*doc.Map)) {
	sv0 := brUnd(s, "siteSlug")
	ss := brJSString(sv0)
	oc, sc := o.Map("config"), s.Map("config")
	if !brEq(brUnd(o, "label"), brUnd(s, "label")) {
		push(dfChange("sites", sv0, ss+"/label", "changed", brUnd(o, "label"), brUnd(s, "label")))
	}
	for _, k := range []string{"host", "agentPort", "agentGuiAliasPort", "notFoundPageId", "depends"} {
		if dfJ(brUnd(oc, k)) != dfJ(brUnd(sc, k)) {
			push(dfChange("sites", sv0, ss+"/"+k, "changed", brUnd(oc, k), brUnd(sc, k)))
		}
	}
	// declared volumes (round D), keyed by volumeSlug
	ov, sv := oc.Arr("volumes"), sc.Arr("volumes")
	for _, y := range sv {
		v := y.(*doc.Map)
		vs := brUnd(v, "volumeSlug")
		path := ss + "/volumes/" + brJSString(vs)
		x := dfFind(ov, "volumeSlug", vs)
		if x == nil {
			push(dfChange("volumes", sv0, path, "added", nil, v))
			dfVolumeContent(nil, v, sv0, push)
		} else if dfJ(x) != dfJ(v) {
			// §21 #101 own content (tree / single) is listed per file; the volume row changes only for the other fields
			xo, vo := dfContentOf(x.Val("seed")), dfContentOf(v.Val("seed"))
			perFile := xo != nil || vo != nil
			rest := func(y *doc.Map) *doc.Map {
				seed := brUnd(y, "seed")
				if dfContentOf(seed) != nil && perFile {
					seed = "content"
				}
				return dfWith(y, "seed", seed)
			}
			notes := []string{}
			if dfJ(brNul(x, "seed")) != dfJ(brNul(v, "seed")) {
				notes = append(notes, VOLUME_SEED_CHANGED_NOTE)
			}
			kindOr := func(m *doc.Map) any {
				if k := m.Val("kind"); !brIsNullish(k) {
					return k
				}
				return "dir"
			}
			if !brEq(kindOr(x), kindOr(v)) {
				notes = append(notes, VOLUME_KIND_CHANGED_NOTE)
			}
			if !perFile || dfJ(rest(x)) != dfJ(rest(v)) || (xo != nil) != (vo != nil) {
				c := dfChange("volumes", sv0, path, "changed", x, v)
				if len(notes) > 0 {
					c.Set("note", strings.Join(notes, " · "))
				}
				push(c)
			}
			if perFile {
				dfVolumeContent(x, v, sv0, push)
			}
		}
	}
	for _, y := range ov {
		x := y.(*doc.Map)
		if !dfSome(sv, "volumeSlug", brUnd(x, "volumeSlug")) {
			push(dfChange("volumes", sv0, ss+"/volumes/"+brJSString(brUnd(x, "volumeSlug")), "removed", x, nil))
		}
	}
	dfDevTools(oc.Arr("devTools"), sc.Arr("devTools"), sv0, push)
	// edge
	oe, se := oc.Map("edge"), sc.Map("edge")
	for _, k := range []string{"httpPort", "httpsPort"} {
		if !brEq(brUnd(oe, k), brUnd(se, k)) {
			push(dfChange("edge", sv0, ss+"/edge/"+k, "changed", brUnd(oe, k), brUnd(se, k)))
		}
	}
	edgeList := func(list, field string) {
		ol, sl := oe.Arr(list), se.Arr(list)
		for _, y := range sl {
			h := y.(*doc.Map)
			id := brUnd(h, field)
			path := ss + "/edge/" + list + "/" + brJSString(id)
			x := dfFind(ol, field, id)
			if x == nil {
				push(dfChange("edge", sv0, path, "added", nil, h))
			} else if dfJ(x) != dfJ(h) {
				push(dfChange("edge", sv0, path, "changed", x, h))
			}
		}
		for _, y := range ol {
			x := y.(*doc.Map)
			if !dfSome(sl, field, brUnd(x, field)) {
				push(dfChange("edge", sv0, ss+"/edge/"+list+"/"+brJSString(brUnd(x, field)), "removed", x, nil))
			}
		}
	}
	edgeList("hosts", "hostname")
	edgeList("routes", "routeId")
	// containers
	oconts, sconts := oc.Arr("containers"), sc.Arr("containers")
	for _, y := range sconts {
		c := y.(*doc.Map)
		cs := brUnd(c, "containerSlug")
		base := ss + "/" + slug(brJSString(cs))
		x := dfFind(oconts, "containerSlug", cs)
		if x == nil {
			push(dfChange("containers", sv0, base, "added", nil, doc.Obj("service", brUnd(c, "serviceSlug"), "version", brUnd(c, "version"))))
			continue
		}
		dfContainer(x, c, base, sv0, push)
	}
	for _, y := range oconts {
		x := y.(*doc.Map)
		if !dfSome(sconts, "containerSlug", brUnd(x, "containerSlug")) {
			push(dfChange("containers", sv0, ss+"/"+slug(brJSString(brUnd(x, "containerSlug"))), "removed", doc.Obj("service", brUnd(x, "serviceSlug"), "version", brUnd(x, "version")), nil))
		}
	}
}

func dfEnvShown(e *doc.Map) any {
	if doc.Truthy(e.Val("secretRef")) {
		return doc.Obj("secretRef", brUnd(e, "secretRef"))
	}
	return brUnd(e, "value")
}

func dfContainer(o, c *doc.Map, base string, ss any, push func(*doc.Map)) {
	for _, k := range []string{"serviceSlug", "version", "enabled", "command", "entrypoint", "restart", "resources", "healthcheck", "depends", "volumes", "ports"} {
		if dfJ(brUnd(o, k)) != dfJ(brUnd(c, k)) {
			push(dfChange("containers", ss, base+"/"+k, "changed", brUnd(o, k), brUnd(c, k)))
		}
	}
	for _, y := range c.Arr("env") {
		e := y.(*doc.Map)
		name := brUnd(e, "name")
		path := base + "/env/" + brJSString(name)
		x := dfFind(o.Arr("env"), "name", name)
		if x == nil {
			push(dfChange("containers", ss, path, "added", nil, dfEnvShown(e)))
		} else if dfJ(x) != dfJ(e) {
			push(dfChange("containers", ss, path, "changed", dfEnvShown(x), dfEnvShown(e)))
		}
	}
	for _, y := range o.Arr("env") {
		x := y.(*doc.Map)
		if !dfSome(c.Arr("env"), "name", brUnd(x, "name")) {
			push(dfChange("containers", ss, base+"/env/"+brJSString(brUnd(x, "name")), "removed", dfEnvShown(x), nil))
		}
	}
}

// MaskDevToolLiterals is TS maskDevToolLiterals.
func MaskDevToolLiterals(p any) any {
	m, ok := p.(*doc.Map)
	if !ok || m == nil || !doc.Truthy(m.Val("auth")) {
		return p
	}
	auth := m.Map("auth")
	literal := func(f string) bool { return auth.Map(f).Val("kind") == any("literal") }
	if !literal("password") && !literal("token") {
		return p
	}
	masked := func(f string) any {
		if literal(f) {
			return doc.Obj("kind", "literal", "value", DEVTOOL_VALUE_MASK)
		}
		return brUnd(auth, f)
	}
	return dfWith(m, "auth", dfWith(auth, "password", masked("password"), "token", masked("token")))
}

func dfDevTools(ol, sl []any, ss any, push func(*doc.Map)) {
	path := func(p any) string { return brJSString(ss) + "/devtools/" + brJSString(p) }
	lit := func(q *doc.Map, f string) any {
		c := q.Map("auth").Map(f)
		if c.Val("kind") == any("literal") {
			return brJSString(brUnd(c, "value"))
		}
		return nil
	}
	for _, y := range sl {
		p := y.(*doc.Map)
		slug := brUnd(p, "presetSlug")
		x := dfFind(ol, "presetSlug", slug)
		if x == nil {
			push(dfChange("devTools", ss, path(slug), "added", nil, MaskDevToolLiterals(p)))
		} else if brCanon(x) != brCanon(p) {
			c := dfChange("devTools", ss, path(slug), "changed", MaskDevToolLiterals(x), MaskDevToolLiterals(p))
			// both static and only the (hidden) value differs → say so, the masked rows look alike
			for _, f := range []string{"password", "token"} {
				if a, b := lit(x, f), lit(p, f); a != nil && b != nil && a != b {
					c.Set("note", DEVTOOL_VALUE_CHANGED_NOTE)
				}
			}
			push(c)
		}
	}
	for _, y := range ol {
		x := y.(*doc.Map)
		if !dfSome(sl, "presetSlug", brUnd(x, "presetSlug")) {
			push(dfChange("devTools", ss, path(brUnd(x, "presetSlug")), "removed", MaskDevToolLiterals(x), nil))
		}
	}
}

type dfTree struct{ entries, dirs []any }

// dfContentOf is the own content of a volume as a tree (single-file content = one entry at the path ""), else nil.
func dfContentOf(seed any) *dfTree {
	s, _ := seed.(*doc.Map)
	switch s.Val("kind") {
	case "tree":
		return &dfTree{s.Arr("entries"), s.Arr("dirs")}
	case "single":
		e := doc.Obj("path", "", "sha256", brUnd(s, "sha256"), "size", brUnd(s, "size"), "mode", brUnd(s, "mode"), "render", brUnd(s, "render"))
		return &dfTree{[]any{e}, nil}
	}
	return nil
}

func dfVolumeContent(x, v *doc.Map, ss any, push func(*doc.Map)) {
	a, b := dfContentOf(x.Val("seed")), dfContentOf(v.Val("seed"))
	if a == nil && b == nil {
		return
	}
	if a == nil {
		a = &dfTree{}
	}
	if b == nil {
		b = &dfTree{}
	}
	pkey := brField("path")
	A, B := brByKey(a.entries, pkey), brByKey(b.entries, pkey)
	paths := brKeysOf(pkey, a.entries, b.entries)
	brSortStrings(paths)
	vs := brJSString(brUnd(v, "volumeSlug"))
	pre := brJSString(ss) + "/volumes/" + vs + "/content"
	for _, p := range paths {
		e0, _ := A[p].(*doc.Map)
		e1, _ := B[p].(*doc.Map)
		if e0 != nil && e1 != nil && brEq(brUnd(e0, "sha256"), brUnd(e1, "sha256")) && brEq(brUnd(e0, "mode"), brUnd(e1, "mode")) && brEq(brUnd(e0, "render"), brUnd(e1, "render")) {
			continue
		}
		kind := "changed"
		if e0 == nil {
			kind = "added"
		} else if e1 == nil {
			kind = "removed"
		}
		path := pre
		if p != "" {
			path += "/" + p
		}
		var before, after any
		if e0 != nil {
			before = e0
		}
		if e1 != nil {
			after = e1
		}
		c := dfChange("volumes", ss, path, kind, before, after)
		c.Set("note", VOLUME_CONTENT_CHANGED_NOTE)
		c.Set("blob", doc.Obj("volumeSlug", brUnd(v, "volumeSlug"), "path", p, "before", brNul(e0, "sha256"), "after", brNul(e1, "sha256")))
		push(c)
	}
	onlyIn := func(l, other []any) []string {
		keep := map[string]bool{}
		for _, d := range other {
			keep[brSelf(d)] = true
		}
		out := []string{}
		for _, d := range brKeysOf(brSelf, l) {
			if !keep[d] {
				out = append(out, d)
			}
		}
		brSortStrings(out)
		return out
	}
	for _, d := range onlyIn(b.dirs, a.dirs) {
		c := dfChange("volumes", ss, pre+"/"+d+"/", "added", nil, "empty folder")
		push(c.Set("note", VOLUME_CONTENT_CHANGED_NOTE))
	}
	for _, d := range onlyIn(a.dirs, b.dirs) {
		c := dfChange("volumes", ss, pre+"/"+d+"/", "removed", "empty folder", nil)
		push(c.Set("note", VOLUME_CONTENT_CHANGED_NOTE))
	}
}
