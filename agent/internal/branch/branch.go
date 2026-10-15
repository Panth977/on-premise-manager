// Package branch is the Go port of shared/src/branch.ts (SPEC-final §21 #104): items, 3-way / two-way plans,
// cherry-pick apply, revert / reset, uncommitted counts, commit stats and hashes. It works on generic JSON trees
// (what encoding/json decodes into any) so that every value compares exactly like the TS code compares parsed JSON;
// shared/testdata/branch-vectors.json is the contract (TestVectors).
package branch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

// Kinds.
const (
	KAdd      = "add"
	KRemove   = "remove"
	KChange   = "change"
	KTheirs   = "theirs"
	KOurs     = "ours"
	KConflict = "conflict"
	KSkipped  = "skipped"
)

// MergeKinds is TS MERGE_KINDS.
var MergeKinds = []string{KAdd, KRemove, KChange, KTheirs, KOurs, KConflict, KSkipped}

// SourceChangeKinds is TS SOURCE_CHANGE_KINDS.
var SourceChangeKinds = []string{KTheirs, KAdd, KChange, KConflict, KSkipped}

const contentNote = "starter content — written only into new / empty volumes (persist-to-cloud volumes: use Send to site)"

// Slot is one side of an item (Has=false: absent).
type Slot struct {
	Has bool
	V   any
}

var none = Slot{}

func slot(v any, ok bool) Slot {
	if !ok {
		return none
	}
	return Slot{Has: true, V: v}
}

// ───────────────────────── JSON helpers

// ToTree converts any value (a model.Config, a struct …) to a generic JSON tree.
func ToTree(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var x any
	if err := json.Unmarshal(b, &x); err != nil {
		panic(err)
	}
	return x
}

// FromTree decodes a tree into out.
func FromTree(t any, out any) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// Canon is the canonical JSON of a tree (fallback: plain JSON).
func Canon(v any) string {
	s, err := canonical.Canonicalize(v)
	if err != nil {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return s
}

func clone(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = clone(e)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = clone(e)
		}
		return l
	}
	return v
}

func same(a, b Slot) bool { return a.Has == b.Has && (!a.Has || Canon(a.V) == Canon(b.V)) }

func isScalar(v any) bool {
	switch v.(type) {
	case nil, string, float64, bool, json.Number, int:
		return true
	}
	return false
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
func arr(v any) []any {
	l, _ := v.([]any)
	return l
}
func str(v any) string {
	s, _ := v.(string)
	return s
}

// get returns m[k] and whether the key exists with a non-undefined value (JSON has no undefined: present = ok).
func get(m map[string]any, k string) (any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m[k]
	return v, ok
}

// orNull is TS `x ?? null`.
func orNull(m map[string]any, k string) any {
	v, ok := get(m, k)
	if !ok {
		return nil
	}
	return v
}

func keyOfField(f string) func(any) string {
	return func(x any) string { return str(obj(x)[f]) }
}

func byKey(list []any, keyOf func(any) string) map[string]any {
	m := map[string]any{}
	for _, x := range list {
		m[keyOf(x)] = x
	}
	return m
}

func keysOf(lists [][]any, keyOf func(any) string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, x := range l {
			k := keyOf(x)
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// upsert inserts / replaces / removes a keyed item; new items go after their predecessor in the source order.
func upsert(list []any, keyOf func(any) string, key string, value any, has bool, order []string) []any {
	i := -1
	for j, x := range list {
		if keyOf(x) == key {
			i = j
			break
		}
	}
	if !has {
		if i >= 0 {
			list = append(list[:i:i], list[i+1:]...)
		}
		return list
	}
	if i >= 0 {
		list[i] = value
		return list
	}
	at := -1
	for j, k := range order {
		if k == key {
			at = j
			break
		}
	}
	insert := func(pos int) []any {
		out := make([]any, 0, len(list)+1)
		out = append(out, list[:pos]...)
		out = append(out, value)
		return append(out, list[pos:]...)
	}
	if at >= 0 {
		for k := at - 1; k >= 0; k-- {
			for j, x := range list {
				if keyOf(x) == order[k] {
					return insert(j + 1)
				}
			}
		}
		for k := at + 1; k < len(order); k++ {
			for j, x := range list {
				if keyOf(x) == order[k] {
					return insert(j)
				}
			}
		}
	}
	return append(list, value)
}

func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// ───────────────────────── hashes

// ItemHash is TS itemHash (cyrb53 over the canonical JSON, UTF-16 code units; "-" = absent).
func ItemHash(s Slot) string {
	if !s.Has {
		return "-"
	}
	units := utf16.Encode([]rune(Canon(s.V)))
	h1, h2 := uint32(0xdeadbeef), uint32(0x41c6ce57)
	for _, ch := range units {
		h1 = (h1 ^ uint32(ch)) * 2654435761
		h2 = (h2 ^ uint32(ch)) * 1597334677
	}
	h1 = ((h1 ^ (h1 >> 16)) * 2246822507) ^ ((h2 ^ (h2 >> 13)) * 3266489909)
	h2 = ((h2 ^ (h2 >> 16)) * 2246822507) ^ ((h1 ^ (h1 >> 13)) * 3266489909)
	return fmt.Sprintf("%08x%08x", h2, h1)
}

// ConfigHash is TS configHash: "sha256:" + hex(sha256(JCS(config))).
func ConfigHash(cfg any) string { return canonical.Sha256Tagged([]byte(Canon(ToTree(cfg)))) }

// SecretValueMarker is TS secretValueMarker.
func SecretValueMarker(v any) any {
	if v == nil {
		return nil
	}
	s := fmt.Sprint(v)
	if s == "" {
		return nil
	}
	return canonical.Sha256Tagged([]byte(s))
}

// MarkSecretValues is TS markSecretValues (merge bases never store plain secret values).
func MarkSecretValues(c model.Config) model.Config {
	out := c
	out.Secrets = append([]model.SecretDef{}, c.Secrets...)
	for i := range out.Secrets {
		var v *string
		if m := SecretValueMarker(derefAny(out.Secrets[i].Value)); m != nil {
			s := m.(string)
			v = &s
		}
		out.Secrets[i].Value = v
	}
	return out
}

func derefAny(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// ───────────────────────── units

// Options are TS ItemizeOptions + PlanOptions.
type Options struct {
	Scope         string
	SecretValues  bool
	Skipped       []model.MergeSkip
	NoPreselectFF bool
	PreselectAll  bool
	Values        map[string]any
}

type unit struct {
	key      string
	section  string
	siteSlug *string
	label    string
	b, t, o  Slot
	set      func(cfg map[string]any, s Slot, src map[string]any)
	refs     func(src map[string]any) []string
	blob     func(s Slot) *string
	blobMeta *model.MergeBlob
	note     string
	secret   bool
}

func strPtr(s string) *string { return &s }

func varDef(v map[string]any) map[string]any {
	out := map[string]any{}
	for k, e := range v {
		if k != "value" {
			out[k] = e
		}
	}
	return out
}

func secretDef(s map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"secretSlug", "label", "description", "required"} {
		if v, ok := s[k]; ok {
			out[k] = v
		}
	}
	out["generate"] = orNull(s, "generate")
	return out
}

var exprRefRE = regexp.MustCompile(`\{\{[^}]*?\b(var|secret|file)\.([A-Za-z0-9_.-]+)`)

func exprRefs(v any) []string {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case map[string]any, []any:
		s = Canon(x)
	default:
		return nil
	}
	var out []string
	for _, m := range exprRefRE.FindAllStringSubmatch(s, -1) {
		slug := strings.Split(strings.TrimRight(m[2], "."), ".")[0]
		switch m[1] {
		case "var":
			out = append(out, "variables/"+slug)
		case "secret":
			out = append(out, "secrets/"+slug)
		default:
			out = append(out, "files/"+slug)
		}
	}
	return out
}

func mountRefs(ss string, mounts any) []string {
	var out []string
	for _, m := range arr(mounts) {
		mm := obj(m)
		src := obj(mm["source"])
		if src == nil {
			continue
		}
		switch str(src["kind"]) {
		case "volume":
			if v := str(src["volumeSlug"]); v != "" {
				out = append(out, "sites/"+ss+"/volumes/"+v)
			}
		case "file":
			if v := str(src["fileSlug"]); v != "" {
				out = append(out, "files/"+v)
			}
		case "secret":
			if v := str(src["secretSlug"]); v != "" {
				out = append(out, "secrets/"+v)
			}
		}
		out = append(out, exprRefs(mm["mountPath"])...)
	}
	return out
}

func containerRefs(ss string, c map[string]any) []string {
	if c == nil {
		return nil
	}
	out := mountRefs(ss, c["volumes"])
	for _, d := range arr(c["depends"]) {
		out = append(out, "sites/"+ss+"/containers/"+str(obj(d)["containerSlug"]))
	}
	for _, e := range arr(c["env"]) {
		em := obj(e)
		if r := str(em["secretRef"]); r != "" {
			out = append(out, "secrets/"+r)
		}
		out = append(out, exprRefs(em["value"])...)
	}
	out = append(out, exprRefs(c["command"])...)
	out = append(out, exprRefs(c["entrypoint"])...)
	return out
}

func siteOf(cfg map[string]any, slug string) map[string]any {
	for _, s := range arr(cfg["sites"]) {
		if str(obj(s)["siteSlug"]) == slug {
			return obj(s)
		}
	}
	return nil
}
func sconfig(site map[string]any) map[string]any { return obj(site["config"]) }
func findIn(list any, field, key string) map[string]any {
	for _, x := range arr(list) {
		if str(obj(x)[field]) == key {
			return obj(x)
		}
	}
	return nil
}
func containerOf(cfg map[string]any, site, c string) map[string]any {
	s := siteOf(cfg, site)
	if s == nil {
		return nil
	}
	return findIn(sconfig(s)["containers"], "containerSlug", c)
}
func volumeOf(cfg map[string]any, site, v string) map[string]any {
	s := siteOf(cfg, site)
	if s == nil {
		return nil
	}
	return findIn(sconfig(s)["volumes"], "volumeSlug", v)
}

// listField returns cfg[k] as a list, creating it (`??= []`) when create is set.
func listField(m map[string]any, k string, create bool) []any {
	l, ok := m[k].([]any)
	if !ok && create {
		l = []any{}
		m[k] = l
	}
	return l
}

func orderOf(list []any, keyOf func(any) string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, keyOf(x))
	}
	return out
}

var siteSettings = []string{"label", "host", "agentPort", "agentGuiAliasPort", "notFoundPageId", "depends"}
var edgeFields = []string{"httpPort", "httpsPort", "maxBodyBytes"}
var containerFields = []string{"image", "enabled", "command", "entrypoint", "restart", "resources", "healthcheck", "depends", "ports", "volumes"}

func containerField(c map[string]any, f string) Slot {
	if c == nil {
		return none
	}
	if f == "image" {
		return Slot{true, map[string]any{"serviceSlug": orUndef(c, "serviceSlug"), "version": orUndef(c, "version")}}
	}
	return Slot{true, orNull(c, f)}
}

// orUndef keeps an absent key absent inside built objects (JSON.stringify drops undefined members); used where the TS
// builds an object from possibly missing fields. Absent → nil here; canonical then writes null, which only differs for
// malformed input (containers always carry serviceSlug + version).
func orUndef(m map[string]any, k string) any { return m[k] }

func volumeDef(v map[string]any) (any, bool) {
	if v == nil {
		return nil, false
	}
	seed := obj(v["seed"])
	if seed != nil && (str(seed["kind"]) == "tree" || str(seed["kind"]) == "single") {
		out := map[string]any{}
		for k, e := range v {
			out[k] = e
		}
		out["seed"] = map[string]any{"kind": seed["kind"]}
		return out, true
	}
	return v, true
}

func isContentSeed(v map[string]any) bool {
	seed := obj(v["seed"])
	return seed != nil && (str(seed["kind"]) == "tree" || str(seed["kind"]) == "single")
}

func buildUnits(base, theirs, ours map[string]any, opt Options) []*unit {
	B := base
	if B == nil {
		B = map[string]any{"variables": []any{}, "secrets": []any{}, "files": []any{}, "sites": []any{}}
	}
	var units []*unit
	push := func(u *unit) { units = append(units, u) }

	// variables
	{
		kf := keyOfField("variableSlug")
		bm, tm, om := byKey(arr(B["variables"]), kf), byKey(arr(theirs["variables"]), kf), byKey(arr(ours["variables"]), kf)
		order := orderOf(arr(theirs["variables"]), kf)
		for _, slug := range keysOf([][]any{arr(B["variables"]), arr(theirs["variables"]), arr(ours["variables"])}, kf) {
			slug := slug
			bv, bok := bm[slug]
			tv, tok := tm[slug]
			ov, ook := om[slug]
			def := func(v any, ok bool) Slot {
				if !ok {
					return none
				}
				return Slot{true, varDef(obj(v))}
			}
			push(&unit{key: "variables/" + slug, section: "variables", label: "Variable " + slug, b: def(bv, bok), t: def(tv, tok), o: def(ov, ook),
				set: func(cfg map[string]any, s Slot, _ map[string]any) {
					list := arr(cfg["variables"])
					cur := findIn(list, "variableSlug", slug)
					var next map[string]any
					if s.Has {
						next = obj(clone(s.V))
						if cur != nil {
							next["value"] = cur["value"]
						} else {
							next["value"] = clone(orNull(obj(tv), "value"))
						}
					}
					cfg["variables"] = upsert(list, kf, slug, next, s.Has, order)
				}})
			if tok && ook {
				var b Slot
				if bok {
					b = Slot{true, orNull(obj(bv), "value")}
				}
				push(&unit{key: "variables/" + slug + "/value", section: "variables", label: "Variable " + slug + " — value", b: b, t: Slot{true, orNull(obj(tv), "value")}, o: Slot{true, orNull(obj(ov), "value")},
					set: func(cfg map[string]any, s Slot, _ map[string]any) {
						if cur := findIn(cfg["variables"], "variableSlug", slug); cur != nil && s.Has {
							cur["value"] = clone(s.V)
						}
					}})
			}
		}
	}
	// secrets
	{
		kf := keyOfField("secretSlug")
		bm, tm, om := byKey(arr(B["secrets"]), kf), byKey(arr(theirs["secrets"]), kf), byKey(arr(ours["secrets"]), kf)
		order := orderOf(arr(theirs["secrets"]), kf)
		for _, slug := range keysOf([][]any{arr(B["secrets"]), arr(theirs["secrets"]), arr(ours["secrets"])}, kf) {
			slug := slug
			b, bok := bm[slug]
			t, tok := tm[slug]
			o, ook := om[slug]
			def := func(v any, ok bool) Slot {
				if !ok {
					return none
				}
				return Slot{true, secretDef(obj(v))}
			}
			push(&unit{key: "secrets/" + slug, section: "secrets", label: "Secret " + slug, b: def(b, bok), t: def(t, tok), o: def(o, ook),
				set: func(cfg map[string]any, s Slot, src map[string]any) {
					list := arr(cfg["secrets"])
					cur := findIn(list, "secretSlug", slug)
					srcSecret := findIn(src["secrets"], "secretSlug", slug)
					var value any
					if cur != nil {
						value = cur["value"]
					} else if opt.SecretValues && srcSecret != nil {
						if sv, ok := srcSecret["value"].(string); ok && !strings.HasPrefix(sv, "sha256:") {
							value = sv
						}
					}
					var rotated any
					if cur != nil {
						rotated = cur["rotatedAt"]
					} else if value != nil {
						rotated = orNull(srcSecret, "rotatedAt")
					}
					var next map[string]any
					if s.Has {
						next = obj(clone(s.V))
						next["value"] = value
						next["rotatedAt"] = rotated
					}
					cfg["secrets"] = upsert(list, kf, slug, next, s.Has, order)
				}})
			if opt.SecretValues && tok && ook {
				mv := func(v any, ok bool) Slot {
					if !ok {
						return none
					}
					x := obj(v)["value"]
					if sv, ok := x.(string); ok && strings.HasPrefix(sv, "sha256:") {
						return Slot{true, sv}
					}
					return Slot{true, SecretValueMarker(x)}
				}
				push(&unit{key: "secrets/" + slug + "/value", section: "secrets", label: "Secret " + slug + " — value", b: mv(b, bok), t: mv(t, tok), o: mv(o, ook), secret: true,
					set: func(cfg map[string]any, s Slot, src map[string]any) {
						cur := findIn(cfg["secrets"], "secretSlug", slug)
						srcSecret := findIn(src["secrets"], "secretSlug", slug)
						if cur == nil || !s.Has || srcSecret == nil {
							return
						}
						cur["value"] = srcSecret["value"]
						if r := srcSecret["rotatedAt"]; r != nil {
							cur["rotatedAt"] = r
						} else if cur["rotatedAt"] == nil {
							cur["rotatedAt"] = nil
						}
					}})
			}
		}
	}
	// files
	{
		kf := keyOfField("fileSlug")
		bm, tm, om := byKey(arr(B["files"]), kf), byKey(arr(theirs["files"]), kf), byKey(arr(ours["files"]), kf)
		order := orderOf(arr(theirs["files"]), kf)
		for _, slug := range keysOf([][]any{arr(B["files"]), arr(theirs["files"]), arr(ours["files"])}, kf) {
			slug := slug
			bv, bok := bm[slug]
			tv, tok := tm[slug]
			ov, ook := om[slug]
			push(&unit{key: "files/" + slug, section: "files", label: "File " + slug, b: slot(bv, bok), t: slot(tv, tok), o: slot(ov, ook),
				set: func(cfg map[string]any, s Slot, _ map[string]any) {
					cfg["files"] = upsert(arr(cfg["files"]), kf, slug, clone(s.V), s.Has, order)
				},
				blob: func(s Slot) *string {
					if !s.Has {
						return nil
					}
					if c := obj(obj(s.V)["content"]); c != nil {
						if h, ok := c["sha256"].(string); ok {
							return &h
						}
					}
					return nil
				},
				blobMeta: &model.MergeBlob{FileSlug: slug}})
		}
	}
	// sites
	{
		kf := keyOfField("siteSlug")
		bm, tm, om := byKey(arr(B["sites"]), kf), byKey(arr(theirs["sites"]), kf), byKey(arr(ours["sites"]), kf)
		order := orderOf(arr(theirs["sites"]), kf)
		for _, ss := range keysOf([][]any{arr(B["sites"]), arr(theirs["sites"]), arr(ours["sites"])}, kf) {
			ss := ss
			if opt.Scope != "" && ss != opt.Scope {
				continue
			}
			b, bok := bm[ss]
			t, tok := tm[ss]
			o, ook := om[ss]
			if !tok || !ook {
				push(&unit{key: "sites/" + ss, section: "sites", siteSlug: strPtr(ss), label: "Site " + ss, b: slot(b, bok), t: slot(t, tok), o: slot(o, ook),
					set: func(cfg map[string]any, s Slot, _ map[string]any) {
						cfg["sites"] = upsert(arr(cfg["sites"]), kf, ss, clone(s.V), s.Has, order)
					},
					refs: func(src map[string]any) []string {
						var out []string
						site := siteOf(src, ss)
						if site == nil {
							return nil
						}
						for _, c := range arr(sconfig(site)["containers"]) {
							for _, k := range containerRefs(ss, obj(c)) {
								if !strings.HasPrefix(k, "sites/"+ss+"/") {
									out = append(out, k)
								}
							}
						}
						return out
					}})
				continue
			}
			var bs map[string]any
			if bok {
				bs = obj(b)
			}
			siteUnits(ss, bs, obj(t), obj(o), push)
		}
	}
	return units
}

func siteUnits(ss string, b, t, o map[string]any, push func(*unit)) {
	pre := "sites/" + ss
	sp := strPtr(ss)
	for _, f := range siteSettings {
		f := f
		get := func(s map[string]any) Slot {
			if s == nil {
				return none
			}
			if f == "label" {
				return Slot{true, s["label"]}
			}
			return Slot{true, orNull(sconfig(s), f)}
		}
		u := &unit{key: pre + "/settings/" + f, section: "sites", siteSlug: sp, label: ss + " · " + f, b: get(b), t: get(t), o: get(o),
			set: func(cfg map[string]any, s Slot, _ map[string]any) {
				site := siteOf(cfg, ss)
				if site == nil || !s.Has {
					return
				}
				if f == "label" {
					site["label"] = s.V
				} else {
					sconfig(site)[f] = clone(s.V)
				}
			}}
		if f != "notFoundPageId" && f != "depends" {
			u.refs = func(src map[string]any) []string {
				site := siteOf(src, ss)
				if site == nil {
					return nil
				}
				return exprRefs(sconfig(site)[f])
			}
		}
		push(u)
	}
	for _, f := range edgeFields {
		f := f
		get := func(s map[string]any) Slot {
			if s == nil {
				return none
			}
			return Slot{true, orNull(obj(sconfig(s)["edge"]), f)}
		}
		push(&unit{key: pre + "/edge/" + f, section: "edge", siteSlug: sp, label: ss + " · edge " + f, b: get(b), t: get(t), o: get(o),
			set: func(cfg map[string]any, s Slot, _ map[string]any) {
				if site := siteOf(cfg, ss); site != nil && s.Has {
					obj(sconfig(site)["edge"])[f] = clone(s.V)
				}
			}})
	}
	type listSel func(site map[string]any, create bool) (container map[string]any, field string)
	keyed := func(bl, tl, ol []any, field, section, seg string, label func(string) string, sel listSel, refs func(x map[string]any) []string) {
		kf := keyOfField(field)
		bm, tm, om := byKey(bl, kf), byKey(tl, kf), byKey(ol, kf)
		order := orderOf(tl, kf)
		for _, k := range keysOf([][]any{bl, tl, ol}, kf) {
			k := k
			bv, bok := bm[k]
			tv, tok := tm[k]
			ov, ook := om[k]
			u := &unit{key: pre + "/" + seg + "/" + k, section: section, siteSlug: sp, label: label(k), b: slot(bv, bok), t: slot(tv, tok), o: slot(ov, ook),
				set: func(cfg map[string]any, s Slot, _ map[string]any) {
					site := siteOf(cfg, ss)
					if site == nil {
						return
					}
					m, f := sel(site, true)
					m[f] = upsert(listField(m, f, true), kf, k, clone(s.V), s.Has, order)
				}}
			if refs != nil {
				u.refs = func(map[string]any) []string {
					if tv, ok := tm[k]; ok {
						return refs(obj(tv))
					}
					return nil
				}
			}
			push(u)
		}
	}
	edgeOf := func(s map[string]any) map[string]any {
		if s == nil {
			return nil
		}
		return obj(sconfig(s)["edge"])
	}
	edgeSel := func(f string) listSel {
		return func(site map[string]any, _ bool) (map[string]any, string) { return obj(sconfig(site)["edge"]), f }
	}
	keyed(arr(edgeOf(b)["hosts"]), arr(edgeOf(t)["hosts"]), arr(edgeOf(o)["hosts"]), "hostname", "edge", "edge/hosts", func(k string) string { return ss + " · edge host " + k }, edgeSel("hosts"), func(h map[string]any) []string {
		tls := obj(h["tls"])
		var out []string
		if v := str(tls["certFileSlug"]); v != "" {
			out = append(out, "files/"+v)
		}
		if v := str(tls["keyFileSlug"]); v != "" {
			out = append(out, "files/"+v)
		}
		if v := str(tls["keySecretSlug"]); v != "" {
			out = append(out, "secrets/"+v)
		}
		return out
	})
	keyed(arr(edgeOf(b)["routes"]), arr(edgeOf(t)["routes"]), arr(edgeOf(o)["routes"]), "routeId", "edge", "edge/routes", func(k string) string { return ss + " · edge route " + k }, edgeSel("routes"), func(r map[string]any) []string {
		var out []string
		a := obj(r["action"])
		if str(a["type"]) == "proxy" && str(a["containerSlug"]) != "" {
			out = append(out, pre+"/containers/"+str(a["containerSlug"]))
		}
		for _, h := range arr(r["hosts"]) {
			out = append(out, pre+"/edge/hosts/"+str(h))
		}
		return out
	})
	cfgOf := func(s map[string]any) map[string]any {
		if s == nil {
			return nil
		}
		return sconfig(s)
	}
	// volumes
	{
		kf := keyOfField("volumeSlug")
		bl, tl, ol := arr(cfgOf(b)["volumes"]), arr(cfgOf(t)["volumes"]), arr(cfgOf(o)["volumes"])
		bm, tm, om := byKey(bl, kf), byKey(tl, kf), byKey(ol, kf)
		order := orderOf(tl, kf)
		for _, vs := range keysOf([][]any{bl, tl, ol}, kf) {
			vs := vs
			bv, bok := bm[vs]
			tv, tok := tm[vs]
			ov, ook := om[vs]
			vpre := pre + "/volumes/" + vs
			if !tok || !ook {
				u := &unit{key: vpre, section: "volumes", siteSlug: sp, label: ss + " · volume " + vs, b: slot(bv, bok), t: slot(tv, tok), o: slot(ov, ook),
					set: func(cfg map[string]any, s Slot, _ map[string]any) {
						if site := siteOf(cfg, ss); site != nil {
							c := sconfig(site)
							c["volumes"] = upsert(listField(c, "volumes", true), kf, vs, clone(s.V), s.Has, order)
						}
					}}
				if !tok {
					u.note = "the data folder is kept on the server (orphan); only the definition is removed"
				}
				push(u)
				continue
			}
			tvm := obj(tv)
			def := func(v any, ok bool) Slot {
				if !ok {
					return none
				}
				x, _ := volumeDef(obj(v))
				return Slot{true, x}
			}
			push(&unit{key: vpre, section: "volumes", siteSlug: sp, label: ss + " · volume " + vs, b: def(bv, bok), t: def(tv, tok), o: def(ov, ook),
				set: func(cfg map[string]any, s Slot, _ map[string]any) {
					site := siteOf(cfg, ss)
					if site == nil || !s.Has {
						return
					}
					c := sconfig(site)
					cur := findIn(c["volumes"], "volumeSlug", vs)
					next := obj(clone(tvm))
					ns := obj(next["seed"])
					var cs map[string]any
					if cur != nil {
						cs = obj(cur["seed"])
					}
					if ns != nil && str(ns["kind"]) == "tree" {
						entries, dirs := []any{}, []any{}
						if cs != nil && str(cs["kind"]) == "tree" {
							entries = arr(clone(orEmpty(cs["entries"])))
							dirs = arr(clone(orEmpty(cs["dirs"])))
						}
						ns["entries"], ns["dirs"] = entries, dirs
					} else if ns != nil && str(ns["kind"]) == "single" && cs != nil && str(cs["kind"]) == "single" {
						for _, k := range []string{"sha256", "size", "mode", "render"} {
							ns[k] = cs[k]
						}
					}
					c["volumes"] = upsert(listField(c, "volumes", true), kf, vs, next, true, order)
				}})
			ovm := obj(ov)
			if isContentSeed(tvm) && isContentSeed(ovm) && str(obj(tvm["seed"])["kind"]) == str(obj(ovm["seed"])["kind"]) {
				var bvm map[string]any
				if bok {
					bvm = obj(bv)
				}
				contentUnits(ss, vs, bvm, tvm, ovm, push)
			}
		}
	}
	devSel := func(site map[string]any, _ bool) (map[string]any, string) { return sconfig(site), "devTools" }
	keyed(arr(cfgOf(b)["devTools"]), arr(cfgOf(t)["devTools"]), arr(cfgOf(o)["devTools"]), "presetSlug", "devTools", "devtools", func(k string) string { return ss + " · dev tool " + k }, devSel, func(p map[string]any) []string { return exprRefs(p) })
	// containers
	kf := keyOfField("containerSlug")
	bl, tl, ol := arr(cfgOf(b)["containers"]), arr(cfgOf(t)["containers"]), arr(cfgOf(o)["containers"])
	bm, tm, om := byKey(bl, kf), byKey(tl, kf), byKey(ol, kf)
	order := orderOf(tl, kf)
	for _, cs := range keysOf([][]any{bl, tl, ol}, kf) {
		cs := cs
		bc, bok := bm[cs]
		tc, tok := tm[cs]
		oc, ook := om[cs]
		cpre := pre + "/containers/" + cs
		if !tok || !ook {
			u := &unit{key: cpre, section: "containers", siteSlug: sp, label: ss + " · container " + cs, b: slot(bc, bok), t: slot(tc, tok), o: slot(oc, ook),
				set: func(cfg map[string]any, s Slot, _ map[string]any) {
					if site := siteOf(cfg, ss); site != nil {
						c := sconfig(site)
						c["containers"] = upsert(arr(c["containers"]), kf, cs, clone(s.V), s.Has, order)
					}
				},
				refs: func(map[string]any) []string { return containerRefs(ss, obj(tc)) }}
			if !tok {
				u.note = "the container is stopped and removed on the site; its data folders stay"
			}
			push(u)
			continue
		}
		var bcm map[string]any
		if bok {
			bcm = obj(bc)
		}
		tcm, ocm := obj(tc), obj(oc)
		for _, f := range containerFields {
			f := f
			u := &unit{key: cpre + "/" + f, section: "containers", siteSlug: sp, label: ss + " · " + cs + " · " + f, b: containerField(bcm, f), t: containerField(tcm, f), o: containerField(ocm, f),
				set: func(cfg map[string]any, s Slot, _ map[string]any) {
					c := containerOf(cfg, ss, cs)
					if c == nil || !s.Has {
						return
					}
					if f == "image" {
						v := obj(s.V)
						c["serviceSlug"], c["version"] = v["serviceSlug"], v["version"]
					} else {
						c[f] = clone(s.V)
					}
				}}
			switch f {
			case "volumes":
				u.refs = func(map[string]any) []string { return mountRefs(ss, tcm["volumes"]) }
			case "depends":
				u.refs = func(map[string]any) []string {
					var out []string
					for _, d := range arr(tcm["depends"]) {
						out = append(out, pre+"/containers/"+str(obj(d)["containerSlug"]))
					}
					return out
				}
			case "command", "entrypoint":
				u.refs = func(map[string]any) []string { return exprRefs(tcm[f]) }
			}
			push(u)
		}
		envVal := func(e any, ok bool) Slot {
			if !ok {
				return none
			}
			em := obj(e)
			if r := str(em["secretRef"]); r != "" {
				return Slot{true, map[string]any{"secretRef": r}}
			}
			return Slot{true, orNull(em, "value")}
		}
		ekf := keyOfField("name")
		ebl, etl, eol := arr(bcm["env"]), arr(tcm["env"]), arr(ocm["env"])
		ebm, etm, eom := byKey(ebl, ekf), byKey(etl, ekf), byKey(eol, ekf)
		eorder := orderOf(etl, ekf)
		for _, name := range keysOf([][]any{ebl, etl, eol}, ekf) {
			name := name
			be, beok := ebm[name]
			te, teok := etm[name]
			oe, oeok := eom[name]
			push(&unit{key: cpre + "/env/" + name, section: "containers", siteSlug: sp, label: ss + " · " + cs + " · env " + name, b: envVal(be, beok), t: envVal(te, teok), o: envVal(oe, oeok),
				set: func(cfg map[string]any, s Slot, _ map[string]any) {
					c := containerOf(cfg, ss, cs)
					if c == nil {
						return
					}
					var row map[string]any
					if s.Has {
						if m, ok := s.V.(map[string]any); ok && m != nil {
							row = map[string]any{"name": name, "value": nil, "secretRef": m["secretRef"]}
						} else if s.V == nil {
							row = map[string]any{"name": name, "value": nil, "secretRef": nil}
						} else {
							row = map[string]any{"name": name, "value": fmt.Sprint(s.V), "secretRef": nil}
						}
					}
					c["env"] = upsert(arr(c["env"]), ekf, name, row, s.Has, eorder)
				},
				refs: func(map[string]any) []string {
					e := obj(etm[name])
					if e == nil {
						return nil
					}
					var out []string
					if r := str(e["secretRef"]); r != "" {
						out = append(out, "secrets/"+r)
					}
					return append(out, exprRefs(e["value"])...)
				}})
		}
	}
}

func orEmpty(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}

func contentUnits(ss, vs string, bv, tv, ov map[string]any, push func(*unit)) {
	vpre := "sites/" + ss + "/volumes/" + vs + "/content"
	sp := strPtr(ss)
	blobOf := func(s Slot) *string {
		if !s.Has {
			return nil
		}
		if h, ok := obj(s.V)["sha256"].(string); ok {
			return &h
		}
		return nil
	}
	if str(obj(tv["seed"])["kind"]) == "single" {
		one := func(v map[string]any) Slot {
			s := obj(v["seed"])
			if s == nil || str(s["kind"]) != "single" {
				return none
			}
			return Slot{true, map[string]any{"sha256": s["sha256"], "size": s["size"], "mode": s["mode"], "render": s["render"]}}
		}
		push(&unit{key: vpre, section: "volumes", siteSlug: sp, label: ss + " · volume " + vs + " · content", b: one(bv), t: one(tv), o: one(ov),
			set: func(cfg map[string]any, s Slot, _ map[string]any) {
				v := volumeOf(cfg, ss, vs)
				if v == nil || !s.Has {
					return
				}
				seed := obj(v["seed"])
				if seed == nil || str(seed["kind"]) != "single" {
					return
				}
				for k, e := range obj(clone(s.V)) {
					seed[k] = e
				}
			}, blob: blobOf, blobMeta: &model.MergeBlob{VolumeSlug: vs, Path: strPtr("")}, note: contentNote})
		return
	}
	entries := func(v map[string]any) []any {
		s := obj(v["seed"])
		if s != nil && str(s["kind"]) == "tree" {
			return arr(s["entries"])
		}
		return nil
	}
	dirs := func(v map[string]any) []any {
		s := obj(v["seed"])
		if s != nil && str(s["kind"]) == "tree" {
			return arr(s["dirs"])
		}
		return nil
	}
	pkf := keyOfField("path")
	ebm, etm, eom := byKey(entries(bv), pkf), byKey(entries(tv), pkf), byKey(entries(ov), pkf)
	ent := func(e any, ok bool) Slot {
		if !ok {
			return none
		}
		m := obj(e)
		return Slot{true, map[string]any{"sha256": m["sha256"], "size": m["size"], "mode": m["mode"], "render": m["render"]}}
	}
	for _, p := range keysOf([][]any{entries(bv), entries(tv), entries(ov)}, pkf) {
		p := p
		be, bok := ebm[p]
		te, tok := etm[p]
		oe, ook := eom[p]
		push(&unit{key: vpre + "/" + p, section: "volumes", siteSlug: sp, label: ss + " · volume " + vs + " · " + p, b: ent(be, bok), t: ent(te, tok), o: ent(oe, ook),
			set: func(cfg map[string]any, s Slot, _ map[string]any) {
				v := volumeOf(cfg, ss, vs)
				if v == nil {
					return
				}
				seed := obj(v["seed"])
				if seed == nil || str(seed["kind"]) != "tree" {
					return
				}
				list := listField(seed, "entries", true)
				i := -1
				for j, e := range list {
					if str(obj(e)["path"]) == p {
						i = j
					}
				}
				if !s.Has {
					if i >= 0 {
						list = append(list[:i:i], list[i+1:]...)
					}
					seed["entries"] = list
					return
				}
				e := map[string]any{"path": p}
				for k, x := range obj(clone(s.V)) {
					e[k] = x
				}
				if i >= 0 {
					list[i] = e
				} else {
					list = append(list, e)
				}
				sort.SliceStable(list, func(a, b int) bool { return lessUTF16(str(obj(list[a])["path"]), str(obj(list[b])["path"])) })
				seed["entries"] = list
			}, blob: blobOf, blobMeta: &model.MergeBlob{VolumeSlug: vs, Path: strPtr(p)}, note: contentNote})
	}
	set := func(v map[string]any) map[string]bool {
		m := map[string]bool{}
		for _, d := range dirs(v) {
			m[str(d)] = true
		}
		return m
	}
	bd, td, od := set(bv), set(tv), set(ov)
	for _, d := range keysOf([][]any{dirs(bv), dirs(tv), dirs(ov)}, func(x any) string { return str(x) }) {
		d := d
		ds := func(m map[string]bool) Slot {
			if m[d] {
				return Slot{true, "empty folder"}
			}
			return none
		}
		var b Slot
		if bv != nil {
			b = ds(bd)
		}
		push(&unit{key: vpre + "/" + d + "/", section: "volumes", siteSlug: sp, label: ss + " · volume " + vs + " · " + d + "/", b: b, t: ds(td), o: ds(od),
			set: func(cfg map[string]any, s Slot, _ map[string]any) {
				v := volumeOf(cfg, ss, vs)
				if v == nil {
					return
				}
				seed := obj(v["seed"])
				if seed == nil || str(seed["kind"]) != "tree" {
					return
				}
				list := listField(seed, "dirs", true)
				i := -1
				for j, x := range list {
					if str(x) == d {
						i = j
					}
				}
				if !s.Has && i >= 0 {
					list = append(list[:i:i], list[i+1:]...)
				}
				if s.Has && i < 0 {
					list = append(list, d)
					sort.SliceStable(list, func(a, b int) bool { return lessUTF16(str(list[a]), str(list[b])) })
				}
				seed["dirs"] = list
			}, note: contentNote})
	}
}

// ───────────────────────── classification + plan

// Classify is TS classifyItem (b nil = two-way); "" = equal (nothing to show).
func Classify(b *Slot, t, o Slot) string {
	if same(t, o) {
		return ""
	}
	if b == nil {
		if t.Has && !o.Has {
			return KAdd
		}
		if !t.Has && o.Has {
			return KRemove
		}
		return KChange
	}
	tb, ob := same(t, *b), same(o, *b)
	if tb {
		return KOurs
	}
	if ob {
		return KTheirs
	}
	return KConflict
}

func effectOf(t, o Slot) string {
	if t.Has && !o.Has {
		return "add"
	}
	if !t.Has && o.Has {
		return "remove"
	}
	return "change"
}

const devtoolMask = "••••"

func maskPreset(p any) any {
	m := obj(p)
	if m == nil {
		return p
	}
	auth := obj(m["auth"])
	if auth == nil {
		return p
	}
	lit := func(k string) bool { return str(obj(auth[k])["kind"]) == "literal" }
	if !lit("password") && !lit("token") {
		return p
	}
	na := map[string]any{}
	for k, v := range auth {
		na[k] = v
	}
	for _, k := range []string{"password", "token"} {
		if lit(k) {
			na[k] = map[string]any{"kind": "literal", "value": devtoolMask}
		}
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	out["auth"] = na
	return out
}

func shown(u *unit, s Slot) model.MaskedSlot {
	if !s.Has {
		return model.MaskedSlot{Has: false, Value: nil}
	}
	if u.secret {
		if s.V == nil {
			return model.MaskedSlot{Has: true, Value: "not set"}
		}
		return model.MaskedSlot{Has: true, Value: "set"}
	}
	if u.section == "devTools" {
		return model.MaskedSlot{Has: true, Value: maskPreset(s.V)}
	}
	if strings.HasPrefix(u.key, "sites/") && !strings.Contains(u.key[len("sites/"):], "/") {
		if site := obj(s.V); site != nil {
			c := obj(site["config"])
			if l := arr(c["devTools"]); len(l) > 0 {
				ml := make([]any, len(l))
				for i, p := range l {
					ml[i] = maskPreset(p)
				}
				nc := map[string]any{}
				for k, v := range c {
					nc[k] = v
				}
				nc["devTools"] = ml
				ns := map[string]any{}
				for k, v := range site {
					ns[k] = v
				}
				ns["config"] = nc
				return model.MaskedSlot{Has: true, Value: ns}
			}
		}
	}
	return model.MaskedSlot{Has: true, Value: s.V}
}

func toItem(u *unit, kind string, hasBase bool, skip *model.MergeSkip) model.MergeItem {
	it := model.MergeItem{Key: u.key, Section: u.section, SiteSlug: u.siteSlug, Label: u.label, Kind: kind, Effect: effectOf(u.t, u.o),
		Target: shown(u, u.o), Source: shown(u, u.t), Requires: []string{}, SourceHash: ItemHash(u.t)}
	if hasBase {
		b := shown(u, u.b)
		it.Base = &b
	}
	it.Scalar = !u.secret && (!u.t.Has || isScalar(u.t.V)) && (!u.o.Has || isScalar(u.o.V)) && (u.t.Has || u.o.Has)
	if u.blob != nil {
		bl := model.MergeBlob{Target: u.blob(u.o), Source: u.blob(u.t)}
		if hasBase {
			bl.Base = u.blob(u.b)
		}
		if u.blobMeta != nil {
			bl.VolumeSlug, bl.Path, bl.FileSlug = u.blobMeta.VolumeSlug, u.blobMeta.Path, u.blobMeta.FileSlug
		}
		it.Blob = &bl
	}
	if u.note != "" && (it.Effect == "remove" || strings.Contains(u.key, "/content")) {
		it.Note = u.note
	}
	if skip != nil {
		it.SkippedAt = skip.SkippedAt
	}
	return it
}

type planned struct {
	items []model.MergeItem
	byKey map[string]*unit
}

func asMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return obj(ToTree(v))
}

func planUnits(base, source, target any, opt Options) planned {
	bm, sm, tm := asMap(base), asMap(source), asMap(target)
	skips := map[string]model.MergeSkip{}
	for _, s := range opt.Skipped {
		skips[s.Key] = s
	}
	units := buildUnits(bm, sm, tm, opt)
	p := planned{byKey: map[string]*unit{}}
	for _, u := range units {
		var bp *Slot
		if bm != nil {
			b := u.b
			bp = &b
		}
		k := Classify(bp, u.t, u.o)
		if k == "" {
			continue
		}
		var sk *model.MergeSkip
		if s, ok := skips[u.key]; ok && (k == KOurs || k == KTheirs || k == KConflict) && s.TheirsHash == ItemHash(u.t) {
			k = KSkipped
			sk = &s
		}
		p.items = append(p.items, toItem(u, k, bm != nil, sk))
		p.byKey[u.key] = u
	}
	present := map[string]int{}
	for i, it := range p.items {
		present[it.Key] = i
	}
	for i := range p.items {
		it := &p.items[i]
		u := p.byKey[it.Key]
		if u.refs == nil || it.Effect == "remove" {
			continue
		}
		seen := map[string]bool{}
		for _, k := range u.refs(sm) {
			if k == it.Key || seen[k] {
				continue
			}
			seen[k] = true
			if j, ok := present[k]; ok && p.items[j].Effect == "add" {
				it.Requires = append(it.Requires, k)
			}
		}
	}
	ff := IsFastForward(p.items)
	for i := range p.items {
		if opt.PreselectAll {
			p.items[i].Preselect = true
		} else if ff && !opt.NoPreselectFF {
			p.items[i].Preselect = true
		}
	}
	if p.items == nil {
		p.items = []model.MergeItem{}
	}
	return p
}

// IsFastForward is TS isFastForward.
func IsFastForward(items []model.MergeItem) bool {
	if len(items) == 0 {
		return false
	}
	for _, i := range items {
		if i.Kind != KTheirs && i.Kind != KAdd {
			return false
		}
	}
	return true
}

// Plan is TS planMerge (base nil = two-way). base / source / target: model.Config, ConfigView or trees.
func Plan(base, source, target any, opt Options) []model.MergeItem {
	return planUnits(base, source, target, opt).items
}

// Count is TS countMerge.
func Count(items []model.MergeItem) map[string]int {
	c := map[string]int{}
	for _, k := range MergeKinds {
		c[k] = 0
	}
	for _, i := range items {
		c[i.Kind]++
	}
	return c
}

func isSourceChange(k string) bool {
	for _, x := range SourceChangeKinds {
		if x == k {
			return true
		}
	}
	return false
}

// ApplyResult is TS ApplyMergeResult (Config = the result tree).
type ApplyResult struct {
	Config     map[string]any
	Taken      []string
	NotTaken   []model.MergeItem
	Unresolved []string
	Errors     []model.ChoiceError
	Items      []model.MergeItem
	Kind       string // merge | cherry-pick
}

// Apply is TS applyMerge: the chosen items applied to a copy of the TARGET (plain secrets).
func Apply(base, source, target any, choices []model.MergeChoice, opt Options) ApplyResult {
	o2 := opt
	o2.NoPreselectFF = true
	p := planUnits(base, source, target, o2)
	sm := asMap(source)
	cfg := obj(clone(asMap(target)))
	chosen := map[string]model.MergeChoice{}
	var order []string
	r := ApplyResult{Taken: []string{}, NotTaken: []model.MergeItem{}, Unresolved: []string{}, Errors: []model.ChoiceError{}}
	for _, c := range choices {
		if _, dup := chosen[c.Key]; dup {
			r.Errors = append(r.Errors, model.ChoiceError{Key: c.Key, Message: "chosen twice"})
		} else {
			order = append(order, c.Key)
		}
		chosen[c.Key] = c
	}
	for _, it := range p.items {
		u := p.byKey[it.Key]
		c, ok := chosen[it.Key]
		if !ok || c.Take == "target" {
			if it.Kind == KConflict && !ok {
				r.Unresolved = append(r.Unresolved, it.Key)
			}
			if isSourceChange(it.Kind) {
				r.NotTaken = append(r.NotTaken, it)
			}
			continue
		}
		switch c.Take {
		case "source":
			u.set(cfg, u.t, sm)
		case "custom":
			if !it.Scalar || !isScalar(normNum(c.Value)) {
				r.Errors = append(r.Errors, model.ChoiceError{Key: it.Key, Message: "a custom value is only possible for a scalar item (string, number, true / false, null)"})
				continue
			}
			u.set(cfg, Slot{true, normNum(c.Value)}, sm)
		default:
			r.Errors = append(r.Errors, model.ChoiceError{Key: it.Key, Message: "take must be source, target or custom"})
			continue
		}
		r.Taken = append(r.Taken, it.Key)
	}
	for _, k := range order {
		if _, ok := p.byKey[k]; !ok {
			r.Errors = append(r.Errors, model.ChoiceError{Key: k, Message: "no such change in the plan (a branch moved — refresh)"})
		}
	}
	slugs := make([]string, 0, len(opt.Values))
	for s := range opt.Values {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		if v := findIn(cfg["variables"], "variableSlug", slug); v != nil {
			v["value"] = clone(ToTree(opt.Values[slug]))
		} else {
			r.Errors = append(r.Errors, model.ChoiceError{Key: "variables/" + slug + "/value", Message: "variable " + slug + " is not in the result"})
		}
	}
	for _, s := range arr(cfg["sites"]) {
		c := sconfig(obj(s))
		if l, ok := c["devTools"].([]any); ok && len(l) == 0 {
			delete(c, "devTools")
		}
	}
	r.Config, r.Items = cfg, p.items
	r.Kind = "merge"
	taken := map[string]bool{}
	for _, k := range r.Taken {
		taken[k] = true
	}
	for _, it := range p.items {
		if isSourceChange(it.Kind) && !taken[it.Key] {
			r.Kind = "cherry-pick"
			break
		}
	}
	return r
}

// normNum converts json.Number / ints from callers into float64 like JSON.parse.
func normNum(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return v
}

// NextSkips is TS nextSkips.
func NextSkips(notTaken []model.MergeItem, previous []model.MergeSkip, sourceSeq *int, now, by string) []model.MergeSkip {
	prev := map[string]model.MergeSkip{}
	for _, s := range previous {
		prev[s.Key] = s
	}
	out := []model.MergeSkip{}
	for _, c := range notTaken {
		if c.Kind == KOurs || c.Kind == KRemove {
			continue
		}
		if p, ok := prev[c.Key]; ok && p.TheirsHash == c.SourceHash {
			out = append(out, p)
			continue
		}
		out = append(out, model.MergeSkip{Key: c.Key, TheirsHash: c.SourceHash, SourceSeq: sourceSeq, SkippedAt: now, SkippedBy: by})
	}
	return out
}

func emptyConfig() map[string]any {
	return map[string]any{"variables": []any{}, "secrets": []any{}, "files": []any{}, "sites": []any{}}
}

// Uncommitted is TS uncommittedItems (two-way head → working).
func Uncommitted(head, working any, opt Options) []model.MergeItem {
	if head == nil {
		head = emptyConfig()
	}
	opt.NoPreselectFF = true
	opt.Skipped = nil
	return Plan(nil, working, head, opt)
}

// Stats is TS commitStats.
func Stats(parent, config any, opt Options) model.CommitStats {
	items := Uncommitted(parent, config, opt)
	st := model.CommitStats{Keys: []string{}}
	for _, i := range items {
		switch i.Effect {
		case "add":
			st.Added++
		case "remove":
			st.Removed++
		default:
			st.Changed++
		}
	}
	for i, it := range items {
		if i >= model.CommitStatsKeysMax {
			break
		}
		st.Keys = append(st.Keys, it.Key)
	}
	st.Truncated = len(items) > model.CommitStatsKeysMax
	return st
}

// RevertPlan is TS revertPlan.
func RevertPlan(commit, parent, head any, opt Options) []model.MergeItem {
	if parent == nil {
		parent = emptyConfig()
	}
	opt.NoPreselectFF = true
	items := Plan(commit, parent, head, opt)
	out := []model.MergeItem{}
	for _, i := range items {
		if i.Kind == KOurs {
			continue
		}
		if i.Kind == KTheirs {
			i.Preselect = true
		}
		out = append(out, i)
	}
	return out
}

// ApplyRevert is TS applyRevert.
func ApplyRevert(commit, parent, head any, choices []model.MergeChoice, opt Options) ApplyResult {
	if parent == nil {
		parent = emptyConfig()
	}
	all := Plan(commit, parent, head, opt)
	picked := map[string]model.MergeChoice{}
	for _, c := range choices {
		picked[c.Key] = c
	}
	var eff []model.MergeChoice
	for _, i := range all {
		if i.Kind == KOurs {
			continue
		}
		if c, ok := picked[i.Key]; ok {
			eff = append(eff, c)
		} else if i.Kind == KTheirs {
			eff = append(eff, model.MergeChoice{Key: i.Key, Take: "source"})
		}
	}
	return Apply(commit, parent, head, eff, opt)
}

// ResetPlan is TS resetPlan.
func ResetPlan(older, head any, opt Options) []model.MergeItem {
	opt.PreselectAll = true
	return Plan(nil, older, head, opt)
}

// WithReferenceSites is TS withReferenceSites: other sites of own are replaced by those of refs.
func WithReferenceSites(own, refs any, siteSlug string) (map[string]any, []string) {
	out := obj(clone(asMap(own)))
	rm := asMap(refs)
	updated := []string{}
	var mine map[string]any
	for _, s := range arr(out["sites"]) {
		if str(obj(s)["siteSlug"]) == siteSlug {
			mine = obj(s)
		}
	}
	next := []any{}
	placed := false
	for _, s := range arr(rm["sites"]) {
		sl := str(obj(s)["siteSlug"])
		if sl == siteSlug {
			if mine != nil {
				next = append(next, mine)
				placed = true
			}
			continue
		}
		prev := siteOf(out, sl)
		if prev == nil || Canon(prev) != Canon(s) {
			updated = append(updated, sl)
		}
		next = append(next, clone(s))
	}
	if mine != nil && !placed {
		next = append(next, mine)
	}
	for _, s := range arr(out["sites"]) {
		sl := str(obj(s)["siteSlug"])
		if sl != siteSlug && siteOf(rm, sl) == nil {
			updated = append(updated, sl)
		}
	}
	out["sites"] = next
	return out, updated
}

// Missing is TS mergeMissing.
func Missing(cfg any) model.MergeMissing {
	m := asMap(cfg)
	out := model.MergeMissing{Variables: []model.MergeMissingVariable{}, Secrets: []model.MergeMissingSecret{}}
	for _, v := range arr(m["variables"]) {
		vm := obj(v)
		if b, _ := vm["required"].(bool); b && (vm["value"] == nil || vm["value"] == "") {
			var opts []string
			for _, o := range arr(vm["options"]) {
				opts = append(opts, str(o))
			}
			out.Variables = append(out.Variables, model.MergeMissingVariable{VariableSlug: str(vm["variableSlug"]), Label: str(vm["label"]), Type: str(vm["type"]), Options: opts, Required: true})
		}
	}
	for _, s := range arr(m["secrets"]) {
		sm := obj(s)
		req, _ := sm["required"].(bool)
		if req && sm["generate"] == nil {
			set := sm["value"] != nil && sm["value"] != ""
			if is, ok := sm["isSet"].(bool); ok {
				set = is
			}
			if !set {
				out.Secrets = append(out.Secrets, model.MergeMissingSecret{SecretSlug: str(sm["secretSlug"]), Label: str(sm["label"])})
			}
		}
	}
	return out
}

// Summary is the first line of a commit message (TS commitSummary).
func Summary(message string) string {
	return strings.TrimSpace(strings.Split(strings.ReplaceAll(strings.TrimSpace(message), "\r\n", "\n"), "\n")[0])
}

// CheckMessage is TS checkCommitMessage ("" = ok).
func CheckMessage(message string) string {
	m := strings.TrimSpace(message)
	if m == "" {
		return "a commit message is required"
	}
	if n := len(utf16.Encode([]rune(m))); n > model.CommitMessageMax {
		return fmt.Sprintf("the commit message is %d characters (max %d)", n, model.CommitMessageMax)
	}
	first := Summary(m)
	if first == "" {
		return "the first line (the summary) must not be empty"
	}
	if n := len(utf16.Encode([]rune(first))); n > model.CommitSummaryMax {
		return fmt.Sprintf("the first line is %d characters (max %d)", n, model.CommitSummaryMax)
	}
	return ""
}

// SourcePicks is TS sourcePicks.
func SourcePicks(taken []string, kind string) (any, bool) {
	if kind == "merge" {
		return "all", false
	}
	if len(taken) > model.CommitPicksMax {
		return append([]string{}, taken[:model.CommitPicksMax]...), true
	}
	return append([]string{}, taken...), false
}

// ToConfig converts a result tree to a model.Config.
func ToConfig(t any) (model.Config, error) {
	var c model.Config
	err := FromTree(t, &c)
	return c, err
}
