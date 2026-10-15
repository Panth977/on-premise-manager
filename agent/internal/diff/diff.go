// Package diff is the Go port of shared/src/diff.ts (config diff). Secrets never show values.
package diff

import (
	"encoding/json"
	"sort"
	"strings"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
	"opm/agent/internal/volcontent"
)

func j(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type secX struct {
	slug    string
	defJSON string
	state   string // set | not set
	value   *string
	rotated *string
	view    bool
}

type cfgX struct {
	variables []model.Variable
	secrets   []secX
	files     []model.FileDef
	sites     []model.Site
}

func plainState(v *string) string {
	if v != nil && *v != "" {
		return "set"
	}
	return "not set"
}

func fromConfig(c *model.Config) cfgX {
	if c == nil {
		return cfgX{}
	}
	x := cfgX{variables: c.Variables, files: c.Files, sites: c.Sites}
	for _, s := range c.Secrets {
		d := s
		d.Value, d.RotatedAt = nil, nil
		x.secrets = append(x.secrets, secX{slug: s.SecretSlug, defJSON: j(d), state: plainState(s.Value), value: s.Value, rotated: s.RotatedAt})
	}
	return x
}

func fromView(c *model.ConfigView) cfgX {
	if c == nil {
		return cfgX{}
	}
	x := cfgX{variables: c.Variables, files: c.Files, sites: c.Sites}
	for _, s := range c.Secrets {
		d := model.SecretDef{SecretSlug: s.SecretSlug, Label: s.Label, Description: s.Description, Required: s.Required, Generate: s.Generate}
		st := "not set"
		if s.IsSet {
			st = "set"
		}
		x.secrets = append(x.secrets, secX{slug: s.SecretSlug, defJSON: j(d), state: st, value: s.Value, rotated: s.RotatedAt, view: true})
	}
	return x
}

// Configs diffs two plain configs (before nil = empty). serviceSlug nil = identity.
func Configs(before, after *model.Config, serviceSlug func(string) string) []model.DiffChange {
	return run(fromConfig(before), fromConfig(after), serviceSlug, nil)
}

// ConfigViews diffs two masked configs (isSet semantics; secretChanged optional).
func ConfigViews(before, after *model.ConfigView, serviceSlug func(string) string, secretChanged func(string) bool) []model.DiffChange {
	return run(fromView(before), fromView(after), serviceSlug, secretChanged)
}

func strp(s string) *string { return &s }

func eqp(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// opt converts typed nil pointers / slices into an untyped nil.
func opt[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func run(b, a cfgX, slug func(string) string, secretChanged func(string) bool) []model.DiffChange {
	if slug == nil {
		slug = func(s string) string { return s }
	}
	out := []model.DiffChange{}
	push := func(section string, site *string, path, kind string, before, after any, note ...string) {
		var blob *model.DiffBlob
		if w, ok := after.(withBlob); ok { // §21 #101 per-file volume content change
			after, blob = w.after, w.blob
		}
		out = append(out, model.DiffChange{Section: section, SiteSlug: site, Path: path, Kind: kind, Before: before, After: after, Note: strings.Join(note, " · "), Blob: blob})
	}
	// variables
	for _, v := range a.variables {
		var o *model.Variable
		for i := range b.variables {
			if b.variables[i].VariableSlug == v.VariableSlug {
				o = &b.variables[i]
			}
		}
		if o == nil {
			push("variables", nil, v.VariableSlug, "added", nil, v.Value.V)
		} else if j(*o) != j(v) {
			oz, vz := *o, v
			oz.Value, vz.Value = model.VariableValue{V: 0.0}, model.VariableValue{V: 0.0}
			if o.Type != v.Type || j(oz) != j(vz) {
				push("variables", nil, v.VariableSlug, "changed", *o, v)
			} else {
				push("variables", nil, v.VariableSlug, "changed", o.Value.V, v.Value.V)
			}
		}
	}
	for _, o := range b.variables {
		found := false
		for _, x := range a.variables {
			if x.VariableSlug == o.VariableSlug {
				found = true
			}
		}
		if !found {
			push("variables", nil, o.VariableSlug, "removed", o.Value.V, nil)
		}
	}
	// secrets
	for _, s := range a.secrets {
		var o *secX
		for i := range b.secrets {
			if b.secrets[i].slug == s.slug {
				o = &b.secrets[i]
			}
		}
		if o == nil {
			push("secrets", nil, s.slug, "added", nil, s.state)
			continue
		}
		defChanged := o.defJSON != s.defJSON
		var valueChanged bool
		switch {
		case secretChanged != nil:
			valueChanged = secretChanged(s.slug)
		case s.view:
			valueChanged = o.state != s.state || !eqp(o.rotated, s.rotated)
		default:
			valueChanged = !eqp(o.value, s.value)
		}
		if defChanged || valueChanged {
			after := "not changed"
			if valueChanged {
				after = "changed"
			}
			push("secrets", nil, s.slug, "changed", o.state, after)
		}
	}
	for _, o := range b.secrets {
		found := false
		for _, x := range a.secrets {
			if x.slug == o.slug {
				found = true
			}
		}
		if !found {
			push("secrets", nil, o.slug, "removed", o.state, nil)
		}
	}
	// files
	for _, f := range a.files {
		var o *model.FileDef
		for i := range b.files {
			if b.files[i].FileSlug == f.FileSlug {
				o = &b.files[i]
			}
		}
		if o == nil {
			push("files", nil, f.FileSlug, "added", nil, opt(f.Content))
		} else if j(normFile(*o)) != j(normFile(f)) {
			oz, fz := normFile(*o), normFile(f)
			oz.Content, fz.Content = nil, nil
			if j(oz) == j(fz) {
				push("files", nil, f.FileSlug, "changed", opt(o.Content), opt(f.Content))
			} else {
				push("files", nil, f.FileSlug, "changed", *o, f)
			}
		}
	}
	for _, o := range b.files {
		found := false
		for _, x := range a.files {
			if x.FileSlug == o.FileSlug {
				found = true
			}
		}
		if !found {
			push("files", nil, o.FileSlug, "removed", opt(o.Content), nil)
		}
	}
	// sites
	for _, s := range a.sites {
		var o *model.Site
		for i := range b.sites {
			if b.sites[i].SiteSlug == s.SiteSlug {
				o = &b.sites[i]
			}
		}
		ss := strp(s.SiteSlug)
		if o == nil {
			push("sites", ss, s.SiteSlug, "added", nil, map[string]any{"label": s.Label, "host": s.Config.Host})
			for _, v := range s.Config.VolumeList() {
				push("volumes", ss, s.SiteSlug+"/volumes/"+v.VolumeSlug, "added", nil, v)
			}
			for _, c := range s.Config.Containers {
				push("containers", ss, s.SiteSlug+"/"+slug(c.ContainerSlug), "added", nil, map[string]any{"service": c.ServiceSlug, "version": c.Version})
			}
			diffDevTools(nil, s.Config.DevToolList(), s.SiteSlug, push)
			continue
		}
		diffSite(o, &s, slug, push)
		diffDevTools(o.Config.DevToolList(), s.Config.DevToolList(), s.SiteSlug, push)
	}
	for _, o := range b.sites {
		found := false
		for _, x := range a.sites {
			if x.SiteSlug == o.SiteSlug {
				found = true
			}
		}
		if !found {
			push("sites", strp(o.SiteSlug), o.SiteSlug, "removed", map[string]any{"label": o.Label, "host": o.Config.Host}, nil)
			diffDevTools(o.Config.DevToolList(), nil, o.SiteSlug, push)
		}
	}
	return out
}

type pushFn func(section string, site *string, path, kind string, before, after any, note ...string)

func diffSite(o, s *model.Site, slug func(string) string, push pushFn) {
	ss := s.SiteSlug
	sp := strp(ss)
	if o.Label != s.Label {
		push("sites", sp, ss+"/label", "changed", o.Label, s.Label)
	}
	oc, sc := &o.Config, &s.Config
	fields := []struct {
		k    string
		a, b any
	}{
		{"host", oc.Host, sc.Host},
		{"agentPort", oc.AgentPort, sc.AgentPort},
		{"agentGuiAliasPort", opt(oc.AgentGuiAliasPort), opt(sc.AgentGuiAliasPort)},
		{"notFoundPageId", opt(oc.NotFoundPageID), opt(sc.NotFoundPageID)},
		{"depends", oc.Depends, sc.Depends},
	}
	for _, f := range fields {
		if j(f.a) != j(f.b) {
			push("sites", sp, ss+"/"+f.k, "changed", f.a, f.b)
		}
	}
	// declared volumes (round D), keyed by volumeSlug
	ov, sv := normVols(oc.VolumeList()), normVols(sc.VolumeList())
	for _, v := range sv {
		var x *model.SiteVolume
		for i := range ov {
			if ov[i].VolumeSlug == v.VolumeSlug {
				x = &ov[i]
			}
		}
		p := ss + "/volumes/" + v.VolumeSlug
		if x == nil {
			push("volumes", sp, p, "added", nil, v)
			diffVolumeContent(nil, &v, ss, sp, push)
		} else if j(*x) != j(v) {
			notes := []string{}
			if j(x.SeedOf()) != j(v.SeedOf()) {
				notes = append(notes, VolumeSeedChangedNote)
			}
			if x.VolumeKind() != v.VolumeKind() {
				notes = append(notes, VolumeKindChangedNote)
			}
			// §21 #101 own content (tree / single) is listed per file; the volume row changes only for the other fields
			xo, vo := contentOf(x.SeedOf()), contentOf(v.SeedOf())
			perFile := xo != nil || vo != nil
			if !perFile || j(restOf(*x, perFile)) != j(restOf(v, perFile)) || (xo != nil) != (vo != nil) {
				push("volumes", sp, p, "changed", *x, v, notes...)
			}
			if perFile {
				diffVolumeContent(x, &v, ss, sp, push)
			}
		}
	}
	for _, x := range ov {
		if sc.FindVolume(x.VolumeSlug) == nil { // removed
			push("volumes", sp, ss+"/volumes/"+x.VolumeSlug, "removed", x, nil)
		}
	}
	oe, se := &oc.Edge, &sc.Edge
	if oe.HTTPPort != se.HTTPPort {
		push("edge", sp, ss+"/edge/httpPort", "changed", oe.HTTPPort, se.HTTPPort)
	}
	if j(oe.HTTPSPort) != j(se.HTTPSPort) {
		push("edge", sp, ss+"/edge/httpsPort", "changed", opt(oe.HTTPSPort), opt(se.HTTPSPort))
	}
	for _, h := range se.Hosts {
		var x *model.EdgeHost
		for i := range oe.Hosts {
			if oe.Hosts[i].Hostname == h.Hostname {
				x = &oe.Hosts[i]
			}
		}
		p := ss + "/edge/hosts/" + h.Hostname
		if x == nil {
			push("edge", sp, p, "added", nil, h)
		} else if j(*x) != j(h) {
			push("edge", sp, p, "changed", *x, h)
		}
	}
	for _, x := range oe.Hosts {
		found := false
		for _, y := range se.Hosts {
			if y.Hostname == x.Hostname {
				found = true
			}
		}
		if !found {
			push("edge", sp, ss+"/edge/hosts/"+x.Hostname, "removed", x, nil)
		}
	}
	for _, r := range se.Routes {
		var x *model.EdgeRoute
		for i := range oe.Routes {
			if oe.Routes[i].RouteID == r.RouteID {
				x = &oe.Routes[i]
			}
		}
		p := ss + "/edge/routes/" + r.RouteID
		if x == nil {
			push("edge", sp, p, "added", nil, r)
		} else if j(*x) != j(r) {
			push("edge", sp, p, "changed", *x, r)
		}
	}
	for _, x := range oe.Routes {
		found := false
		for _, y := range se.Routes {
			if y.RouteID == x.RouteID {
				found = true
			}
		}
		if !found {
			push("edge", sp, ss+"/edge/routes/"+x.RouteID, "removed", x, nil)
		}
	}
	for _, c := range sc.Containers {
		var x *model.Container
		for i := range oc.Containers {
			if oc.Containers[i].ContainerSlug == c.ContainerSlug {
				x = &oc.Containers[i]
			}
		}
		base := ss + "/" + slug(c.ContainerSlug)
		if x == nil {
			push("containers", sp, base, "added", nil, map[string]any{"service": c.ServiceSlug, "version": c.Version})
			continue
		}
		diffContainer(x, &c, base, sp, push)
	}
	for _, x := range oc.Containers {
		found := false
		for _, y := range sc.Containers {
			if y.ContainerSlug == x.ContainerSlug {
				found = true
			}
		}
		if !found {
			push("containers", sp, ss+"/"+slug(x.ContainerSlug), "removed", map[string]any{"service": x.ServiceSlug, "version": x.Version}, nil)
		}
	}
}

func cmdAny(c []string) any {
	if c == nil {
		return nil
	}
	return c
}

func hcAny(h *model.HealthCheck) any {
	if h == nil {
		return nil
	}
	return h
}

func envVal(e model.ContainerEnv) any {
	if e.SecretRef != nil && *e.SecretRef != "" {
		return map[string]any{"secretRef": *e.SecretRef}
	}
	return opt(e.Value)
}

func diffContainer(o, c *model.Container, base string, sp *string, push pushFn) {
	fields := []struct {
		k    string
		a, b any
	}{
		{"serviceSlug", o.ServiceSlug, c.ServiceSlug},
		{"version", o.Version, c.Version},
		{"enabled", o.Enabled, c.Enabled},
		{"command", cmdAny(o.Command), cmdAny(c.Command)},
		{"entrypoint", cmdAny(o.Entrypoint), cmdAny(c.Entrypoint)},
		{"restart", o.Restart, c.Restart},
		{"resources", o.Resources, c.Resources},
		{"healthcheck", hcAny(o.Healthcheck), hcAny(c.Healthcheck)},
		{"depends", o.Depends, c.Depends},
		{"volumes", o.Volumes, c.Volumes},
		{"ports", o.Ports, c.Ports},
	}
	for _, f := range fields {
		if j(f.a) != j(f.b) {
			push("containers", sp, base+"/"+f.k, "changed", f.a, f.b)
		}
	}
	for _, e := range c.Env {
		var x *model.ContainerEnv
		for i := range o.Env {
			if o.Env[i].Name == e.Name {
				x = &o.Env[i]
			}
		}
		p := base + "/env/" + e.Name
		if x == nil {
			push("containers", sp, p, "added", nil, envVal(e))
		} else if j(*x) != j(e) {
			push("containers", sp, p, "changed", envVal(*x), envVal(e))
		}
	}
	for _, x := range o.Env {
		found := false
		for _, y := range c.Env {
			if y.Name == x.Name {
				found = true
			}
		}
		if !found {
			push("containers", sp, base+"/env/"+x.Name, "removed", envVal(x), nil)
		}
	}
}

// normFile / normVols: a missing render / location (older documents) equals the default, so it is not a change.
func normFile(f model.FileDef) model.FileDef {
	if !f.Render.Set {
		f.Render = model.Some(f.RenderMode())
	}
	l := f.FileLocation()
	f.Location = &l
	return f
}

func normVols(vs []model.SiteVolume) []model.SiteVolume {
	out := make([]model.SiteVolume, len(vs))
	for i, v := range vs {
		l := v.VolumeLocation()
		v.Location = &l
		k := v.VolumeKind()
		v.Kind = &k
		if !v.Seed.Set {
			v.Seed = model.Null[model.VolumeSeed]()
		}
		out[i] = v
	}
	return out
}

// Review hints for site volume changes (TS VOLUME_SEED_CHANGED_NOTE / VOLUME_KIND_CHANGED_NOTE, round E).
const (
	VolumeSeedChangedNote = "seed changed — applies only to new / empty volumes (existing data is never touched; use \"Reset to starter content…\" on the site)"
	VolumeKindChangedNote = "kind changed — a site refuses this when the volume already holds data (use a new volume name instead)"
)

// ───────────────────────── §21 #101 volume content, per file

// VolumeContentChangedNote is TS VOLUME_CONTENT_CHANGED_NOTE.
const VolumeContentChangedNote = "starter content — written only into new / empty volumes (persist-to-cloud volumes: use Send to site)"

type withBlob struct {
	after any
	blob  *model.DiffBlob
}

// contentOf: a tree / single seed as a tree (single = one entry at the path ""), else nil.
func contentOf(sd *model.VolumeSeed) *model.VolumeTree {
	if sd == nil {
		return nil
	}
	switch sd.Kind {
	case "tree":
		t := sd.TreeOf()
		return &t
	case "single":
		return &model.VolumeTree{Entries: []model.VolumeTreeEntry{{Path: "", Sha256: sd.Sha256, Size: sd.Size, Mode: sd.Mode, Render: sd.Render}}}
	}
	return nil
}

// restOf: the volume with its own content replaced by the marker "content" (per-file diffs list the content).
func restOf(v model.SiteVolume, perFile bool) any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if contentOf(v.SeedOf()) != nil && perFile {
		m["seed"] = "content"
	}
	return m
}

func diffVolumeContent(x, v *model.SiteVolume, ss string, sp *string, push pushFn) {
	var a, b *model.VolumeTree
	if x != nil {
		a = contentOf(x.SeedOf())
	}
	b = contentOf(v.SeedOf())
	if a == nil && b == nil {
		return
	}
	A, B := map[string]*model.VolumeTreeEntry{}, map[string]*model.VolumeTreeEntry{}
	var paths []string
	add := func(m map[string]*model.VolumeTreeEntry, t *model.VolumeTree) {
		if t == nil {
			return
		}
		for i := range t.Entries {
			e := &t.Entries[i]
			if _, ok := A[e.Path]; !ok {
				if _, ok2 := B[e.Path]; !ok2 {
					paths = append(paths, e.Path)
				}
			}
			m[e.Path] = e
		}
	}
	add(A, a)
	add(B, b)
	sort.SliceStable(paths, func(i, j int) bool { return volcontent.CompareUTF16(paths[i], paths[j]) < 0 })
	base := ss + "/volumes/" + v.VolumeSlug + "/content"
	shaOf := func(e *model.VolumeTreeEntry) *model.Sha256 {
		if e == nil {
			return nil
		}
		s := e.Sha256
		return &s
	}
	for _, p := range paths {
		e0, e1 := A[p], B[p]
		if e0 != nil && e1 != nil && e0.Sha256 == e1.Sha256 && e0.Mode == e1.Mode && e0.Render == e1.Render {
			continue
		}
		kind := "changed"
		if e0 == nil {
			kind = "added"
		} else if e1 == nil {
			kind = "removed"
		}
		path := base
		if p != "" {
			path += "/" + p
		}
		var before, after any
		if e0 != nil {
			before = *e0
		}
		if e1 != nil {
			after = *e1
		}
		push("volumes", sp, path, kind, before, withBlob{after, &model.DiffBlob{VolumeSlug: v.VolumeSlug, Path: p, Before: shaOf(e0), After: shaOf(e1)}}, VolumeContentChangedNote)
	}
	da, db := map[string]bool{}, map[string]bool{}
	if a != nil {
		for _, d := range a.Dirs {
			da[d] = true
		}
	}
	if b != nil {
		for _, d := range b.Dirs {
			db[d] = true
		}
	}
	var added, removed []string
	for d := range db {
		if !da[d] {
			added = append(added, d)
		}
	}
	for d := range da {
		if !db[d] {
			removed = append(removed, d)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	for _, d := range added {
		push("volumes", sp, base+"/"+d+"/", "added", nil, "empty folder", VolumeContentChangedNote)
	}
	for _, d := range removed {
		push("volumes", sp, base+"/"+d+"/", "removed", "empty folder", nil, VolumeContentChangedNote)
	}
}

// DevToolValueChangedNote is TS DEVTOOL_VALUE_CHANGED_NOTE (§21 #103).
const DevToolValueChangedNote = "static password / token value changed (hidden)"

// maskedPreset is TS maskDevToolLiterals: a static password / token value shown as "••••" (never in clear).
func maskedPreset(p model.DevToolPreset) model.DevToolPreset {
	if !p.HasLiteral() {
		return p
	}
	m := func(c model.DevToolCredential) model.DevToolCredential {
		if c.Kind == "literal" {
			v := "••••"
			return model.DevToolCredential{Kind: "literal", Value: &v}
		}
		return c
	}
	q := p.Detached()
	q.Auth.Password, q.Auth.Token = m(q.Auth.Password), m(q.Auth.Token)
	return q
}

func literalOf(c model.DevToolCredential) *string {
	if c.Kind == "literal" {
		v := model.Str(c.Value)
		return &v
	}
	return nil
}

// diffDevTools is TS diffDevTools (§21 #97 d, #103): presets keyed by presetSlug, path <site>/devtools/<presetSlug>;
// compared raw, shown with static values masked.
func diffDevTools(ol, sl []model.DevToolPreset, ss string, push pushFn) {
	sp := strp(ss)
	for _, p := range sl {
		var x *model.DevToolPreset
		for i := range ol {
			if ol[i].PresetSlug == p.PresetSlug {
				x = &ol[i]
			}
		}
		path := ss + "/devtools/" + p.PresetSlug
		if x == nil {
			push("devTools", sp, path, "added", nil, maskedPreset(p))
			continue
		}
		if cj(*x) == cj(p) {
			continue
		}
		note := []string{}
		for _, f := range []func(model.DevToolPreset) model.DevToolCredential{
			func(q model.DevToolPreset) model.DevToolCredential { return q.Auth.Password },
			func(q model.DevToolPreset) model.DevToolCredential { return q.Auth.Token },
		} {
			a, b := literalOf(f(*x)), literalOf(f(p))
			if a != nil && b != nil && *a != *b && len(note) == 0 {
				note = append(note, DevToolValueChangedNote)
			}
		}
		push("devTools", sp, path, "changed", maskedPreset(*x), maskedPreset(p), note...)
	}
	for _, x := range ol {
		found := false
		for _, y := range sl {
			if y.PresetSlug == x.PresetSlug {
				found = true
			}
		}
		if !found {
			push("devTools", sp, ss+"/devtools/"+x.PresetSlug, "removed", maskedPreset(x), nil)
		}
	}
}

// cj is the canonical JSON of v (TS cj).
func cj(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s, err := canonical.CanonicalizeJSON(b)
	if err != nil {
		return string(b)
	}
	return s
}
