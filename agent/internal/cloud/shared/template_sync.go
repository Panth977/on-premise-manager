package shared

import (
	"opm/agent/internal/cloud/doc"
)

// Port of shared/src/template-sync.ts: the manual 3-way review template → deployment draft (§21 #94).

// SyncSlot is TS SyncSlot.
type SyncSlot = Slot

// SYNC_TAKEABLE is TS SYNC_TAKEABLE.
var SYNC_TAKEABLE = []string{"theirs", "conflict", "skipped", "default"}

// TemplateSyncHash is TS templateSyncHash (a Slot, a {has, v} object, or a plain value; doc.Undefined = absent).
func TemplateSyncHash(s any) string { return brSlotHash(s) }

type syUnit struct {
	key, section string
	siteSlug     any
	label        string
	value        bool // role 'value': a variable default (kind "default", never a conflict)
	b, t, o      Slot
	set          func(cfg *doc.Map, s Slot)
}

func syBuildUnits(base, theirs, ours *doc.Map) []*syUnit {
	B := base
	if B == nil {
		B = brEmptyConfig()
	}
	var units []*syUnit
	push := func(u *syUnit) { units = append(units, u) }

	// variables: definition + (opt-in) default value
	{
		kf := brField("variableSlug")
		bl, tl, ol := B.Arr("variables"), theirs.Arr("variables"), ours.Arr("variables")
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, slug := range brKeysOf(kf, bl, tl, ol) {
			bv, _ := bm[slug].(*doc.Map)
			tv, _ := tm[slug].(*doc.Map)
			ov, _ := om[slug].(*doc.Map)
			push(&syUnit{key: "variables/" + slug, section: "variables", label: "Variable " + slug, b: brVarDef(bv), t: brVarDef(tv), o: brVarDef(ov),
				set: func(cfg *doc.Map, s Slot) {
					list := cfg.Arr("variables")
					cur := brFind(list, "variableSlug", slug)
					var next any = doc.Undefined
					if s.Has {
						next = brTaken(s.V, cur, tv)
					}
					cfg.Set("variables", brUpsert(list, kf, slug, next, order))
				}})
			if tv != nil && ov != nil {
				tb := true // the template changed its default (or no base)
				if bv != nil {
					tb = brCanon(brNul(bv, "value")) != brCanon(brNul(tv, "value"))
				}
				if tb && brCanon(brNul(tv, "value")) != brCanon(brNul(ov, "value")) {
					b := brNone
					if bv != nil {
						b = brSlot(brNul(bv, "value"))
					}
					push(&syUnit{key: "variables/" + slug + "/value", section: "variables", label: "Variable " + slug + " — value", value: true, b: b, t: brSlot(brNul(tv, "value")), o: brSlot(brNul(ov, "value")),
						set: func(cfg *doc.Map, s Slot) {
							if cur := brFind(cfg.Arr("variables"), "variableSlug", slug); cur != nil && s.Has {
								cur.Set("value", brClone(s.V))
							}
						}})
				}
			}
		}
	}
	// secrets: definitions + generate rules only
	{
		kf := brField("secretSlug")
		bl, tl, ol := B.Arr("secrets"), theirs.Arr("secrets"), ours.Arr("secrets")
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, slug := range brKeysOf(kf, bl, tl, ol) {
			b, _ := bm[slug].(*doc.Map)
			t, _ := tm[slug].(*doc.Map)
			o, _ := om[slug].(*doc.Map)
			push(&syUnit{key: "secrets/" + slug, section: "secrets", label: "Secret " + slug, b: brSecretDef(b), t: brSecretDef(t), o: brSecretDef(o),
				set: func(cfg *doc.Map, s Slot) {
					list := cfg.Arr("secrets")
					cur := brFind(list, "secretSlug", slug)
					// values never come from the template: keep the draft value; a new secret starts without one
					var next any = doc.Undefined
					if s.Has {
						var value, rotated any
						if cur != nil {
							value, rotated = brUnd(cur, "value"), brUnd(cur, "rotatedAt")
						}
						n, _ := brClone(s.V).(*doc.Map)
						next = n.Set("value", value).Set("rotatedAt", rotated)
					}
					cfg.Set("secrets", brUpsert(list, kf, slug, next, order))
				}})
		}
	}
	// files: metadata + content ref
	{
		kf := brField("fileSlug")
		bl, tl, ol := B.Arr("files"), theirs.Arr("files"), ours.Arr("files")
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, slug := range brKeysOf(kf, bl, tl, ol) {
			push(&syUnit{key: "files/" + slug, section: "files", label: "File " + slug, b: brSlot(bm.get(slug)), t: brSlot(tm.get(slug)), o: brSlot(om.get(slug)),
				set: func(cfg *doc.Map, s Slot) {
					cfg.Set("files", brUpsert(cfg.Arr("files"), kf, slug, brTakenValue(s), order))
				}})
		}
	}
	// sites
	{
		kf := brField("siteSlug")
		bl, tl, ol := B.Arr("sites"), theirs.Arr("sites"), ours.Arr("sites")
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, ss := range brKeysOf(kf, bl, tl, ol) {
			t, _ := tm[ss].(*doc.Map)
			o, _ := om[ss].(*doc.Map)
			if t == nil || o == nil {
				push(&syUnit{key: "sites/" + ss, section: "sites", siteSlug: ss, label: "Site " + ss, b: brSlot(bm.get(ss)), t: brSlot(tm.get(ss)), o: brSlot(om.get(ss)),
					set: func(cfg *doc.Map, s Slot) {
						cfg.Set("sites", brUpsert(cfg.Arr("sites"), kf, ss, brTakenValue(s), order))
					}})
				continue
			}
			b, _ := bm[ss].(*doc.Map)
			sySiteUnits(ss, b, t, o, push)
		}
	}
	return units
}

func sySiteUnits(ss string, b, t, o *doc.Map, push func(*syUnit)) {
	pre := "sites/" + ss
	for _, f := range SITE_SETTINGS {
		get := func(s *doc.Map) Slot { return brSettingSlot(s, f) }
		push(&syUnit{key: pre + "/settings/" + f, section: "sites", siteSlug: ss, label: ss + " · " + f, b: get(b), t: get(t), o: get(o),
			set: func(cfg *doc.Map, s Slot) { brSetSetting(brSiteOf(cfg, ss), f, s) }})
	}
	for _, f := range EDGE_FIELDS {
		get := func(s *doc.Map) Slot { return brEdgeSlot(s, f) }
		push(&syUnit{key: pre + "/edge/" + f, section: "edge", siteSlug: ss, label: ss + " · edge " + f, b: get(b), t: get(t), o: get(o),
			set: func(cfg *doc.Map, s Slot) { brSetEdge(brSiteOf(cfg, ss), f, s) }})
	}
	keyed := func(bl, tl, ol []any, field, section, seg string, label func(string) string, listOf func(site *doc.Map) (*doc.Map, string, bool)) {
		kf := brField(field)
		bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
		order := brOrder(tl, kf)
		for _, k := range brKeysOf(kf, bl, tl, ol) {
			push(&syUnit{key: pre + "/" + seg + "/" + k, section: section, siteSlug: ss, label: label(k), b: brSlot(bm.get(k)), t: brSlot(tm.get(k)), o: brSlot(om.get(k)),
				set: func(cfg *doc.Map, s Slot) {
					if site := brSiteOf(cfg, ss); site != nil {
						brKeyedSet(site, listOf, kf, k, s, order)
					}
				}})
		}
	}
	bc, tc, oc := b.Map("config"), t.Map("config"), o.Map("config")
	keyed(bc.Map("edge").Arr("hosts"), tc.Map("edge").Arr("hosts"), oc.Map("edge").Arr("hosts"), "hostname", "edge", "edge/hosts", func(k string) string { return ss + " · edge host " + k }, brEdgeList("hosts"))
	keyed(bc.Map("edge").Arr("routes"), tc.Map("edge").Arr("routes"), oc.Map("edge").Arr("routes"), "routeId", "edge", "edge/routes", func(k string) string { return ss + " · edge route " + k }, brEdgeList("routes"))
	keyed(bc.Arr("volumes"), tc.Arr("volumes"), oc.Arr("volumes"), "volumeSlug", "volumes", "volumes", func(k string) string { return ss + " · volume " + k }, brConfigList("volumes"))
	keyed(bc.Arr("devTools"), tc.Arr("devTools"), oc.Arr("devTools"), "presetSlug", "devTools", "devtools", func(k string) string { return ss + " · dev tool " + k }, brConfigList("devTools"))
	// containers
	kf := brField("containerSlug")
	bl, tl, ol := bc.Arr("containers"), tc.Arr("containers"), oc.Arr("containers")
	bm, tm, om := brByKey(bl, kf), brByKey(tl, kf), brByKey(ol, kf)
	order := brOrder(tl, kf)
	for _, cs := range brKeysOf(kf, bl, tl, ol) {
		bcm, _ := bm[cs].(*doc.Map)
		tcm, _ := tm[cs].(*doc.Map)
		ocm, _ := om[cs].(*doc.Map)
		cpre := pre + "/containers/" + cs
		if tcm == nil || ocm == nil {
			push(&syUnit{key: cpre, section: "containers", siteSlug: ss, label: ss + " · container " + cs, b: brSlot(bm.get(cs)), t: brSlot(tm.get(cs)), o: brSlot(om.get(cs)),
				set: func(cfg *doc.Map, s Slot) {
					if site := brSiteOf(cfg, ss); site != nil {
						c := site.Map("config")
						c.Set("containers", brUpsert(c.Arr("containers"), kf, cs, brTakenValue(s), order))
					}
				}})
			continue
		}
		for _, f := range CONTAINER_FIELDS {
			push(&syUnit{key: cpre + "/" + f, section: "containers", siteSlug: ss, label: ss + " · " + cs + " · " + f, b: brContainerField(bcm, f), t: brContainerField(tcm, f), o: brContainerField(ocm, f),
				set: func(cfg *doc.Map, s Slot) { brSetContainerField(brContainerOf(cfg, ss, cs), f, s) }})
		}
		// env rows compare as their value (a string / null) or { secretRef } — like the Diff page
		ekf := brField("name")
		ebl, etl, eol := bcm.Arr("env"), tcm.Arr("env"), ocm.Arr("env")
		ebm, etm, eom := brByKey(ebl, ekf), brByKey(etl, ekf), brByKey(eol, ekf)
		eorder := brOrder(etl, ekf)
		for _, name := range brKeysOf(ekf, ebl, etl, eol) {
			push(&syUnit{key: cpre + "/env/" + name, section: "containers", siteSlug: ss, label: ss + " · " + cs + " · env " + name, b: brEnvVal(ebm.get(name)), t: brEnvVal(etm.get(name)), o: brEnvVal(eom.get(name)),
				set: func(cfg *doc.Map, s Slot) { brSetEnv(brContainerOf(cfg, ss, cs), name, s, eorder) }})
		}
	}
}

func syOp(from, to Slot) string {
	switch {
	case !from.Has && to.Has:
		return "added"
	case from.Has && !to.Has:
		return "removed"
	case !brSame(from, to):
		return "changed"
	}
	return "none"
}

func syClassify(b, t, o Slot) string {
	tb, ob := brSame(t, b), brSame(o, b)
	switch {
	case tb && ob:
		return ""
	case tb:
		return "ours"
	case ob:
		return "theirs"
	case brSame(t, o):
		return "same"
	}
	return "conflict"
}

// ClassifySync is TS classifySync (nil = neither side changed the item).
func ClassifySync(b, t, o Slot) any {
	if k := syClassify(b, t, o); k != "" {
		return k
	}
	return nil
}

func syKindOf(u *syUnit, skips map[string]*doc.Map) string {
	if u.value {
		return "default"
	}
	k := syClassify(u.b, u.t, u.o)
	if k == "ours" {
		if s := skips[u.key]; s != nil && brEq(brUnd(s, "theirsHash"), TemplateSyncHash(u.t)) {
			return "skipped"
		}
	}
	return k
}

func syShown(u *syUnit, s Slot) any {
	if !s.Has {
		return nil
	}
	if u.section == "devTools" && brIsObject(s.V) {
		return MaskDevToolLiterals(s.V)
	}
	return s.V
}

func syToChange(u *syUnit, kind string, skip *doc.Map) *doc.Map {
	ch := doc.Obj(
		"key", u.key, "section", u.section, "siteSlug", u.siteSlug, "label", u.label, "kind", kind,
		"theirsOp", syOp(u.b, u.t), "oursOp", syOp(u.b, u.o),
		// §21 #103: a preset's static password / token value is never shown (compared raw: theirsHash / kinds)
		"base", syShown(u, u.b), "theirs", syShown(u, u.t), "ours", syShown(u, u.o),
		"present", doc.Obj("base", u.b.Has, "theirs", u.t.Has, "ours", u.o.Has),
		"scalar", (!u.t.Has || brIsScalar(u.t.V)) && (!u.o.Has || brIsScalar(u.o.V)) && (u.t.Has || u.o.Has),
		"theirsHash", TemplateSyncHash(u.t),
	)
	if kind == "skipped" && skip != nil {
		ch.Set("skippedAt", brUnd(skip, "skippedAt"))
	}
	return ch
}

func brSkipsByKey(l []any) map[string]*doc.Map {
	out := map[string]*doc.Map{}
	for _, x := range l {
		if s, ok := x.(*doc.Map); ok {
			out[brJSString(brUnd(s, "key"))] = s
		}
	}
	return out
}

// PlanTemplateSync is TS planTemplateSync (base nil = no base; skipped = TemplateSyncSkip records).
func PlanTemplateSync(base, theirs, ours *doc.Map, skipped []any) []any {
	skips := brSkipsByKey(skipped)
	out := []any{}
	for _, u := range syBuildUnits(base, theirs, ours) {
		if k := syKindOf(u, skips); k != "" {
			out = append(out, syToChange(u, k, skips[u.key]))
		}
	}
	return out
}

// CountTemplateSync is TS countTemplateSync.
func CountTemplateSync(changes []any) *doc.Map {
	counts := map[string]float64{}
	for _, x := range changes {
		m, _ := x.(*doc.Map)
		counts[m.Str("kind")]++
	}
	return doc.Obj("theirs", counts["theirs"], "ours", counts["ours"], "same", counts["same"], "conflict", counts["conflict"], "skipped", counts["skipped"], "default", counts["default"],
		"templateChanges", counts["theirs"]+counts["conflict"]+counts["same"]+counts["default"])
}

// CleanTemplateChoices is TS cleanTemplateChoices.
func CleanTemplateChoices(changes []any) []any {
	out := []any{}
	for _, x := range changes {
		c, _ := x.(*doc.Map)
		if c.Str("kind") == "theirs" {
			out = append(out, doc.Obj("key", brUnd(c, "key"), "take", "theirs"))
		}
	}
	return out
}

// ApplyTemplateSync is TS applyTemplateSync (opts: { skipped?, values? }, may be nil).
func ApplyTemplateSync(base, theirs, ours *doc.Map, choices []any, opts *doc.Map) *doc.Map {
	skips := brSkipsByKey(opts.Arr("skipped"))
	units := syBuildUnits(base, theirs, ours)
	cfg := brCloneMap(ours)
	errs := []any{}
	chosen, chosenOrder := brChosen(choices, &errs)
	taken, notTaken, unresolved, plan := []any{}, []any{}, []any{}, []any{}
	seen := map[string]bool{}
	for _, u := range units {
		kind := syKindOf(u, skips)
		if kind == "" {
			continue
		}
		ch := syToChange(u, kind, skips[u.key])
		plan = append(plan, ch)
		seen[u.key] = true
		choice := chosen[u.key]
		if !brContains(SYNC_TAKEABLE, kind) {
			continue // ours / same: nothing to take
		}
		if choice == nil || choice.Val("take") == any("ours") {
			if kind == "conflict" && choice == nil {
				unresolved = append(unresolved, u.key)
			}
			if kind != "default" {
				notTaken = append(notTaken, ch)
			}
			continue
		}
		switch choice.Val("take") {
		case "theirs":
			u.set(cfg, u.t)
		case "custom":
			v := brUnd(choice, "value")
			if !ch.Bool("scalar") || !brIsScalar(v) {
				errs = append(errs, brChoiceError(u.key, "a merged value is only possible for a scalar item (string, number, true / false, null)"))
				continue
			}
			u.set(cfg, Slot{Has: true, V: v})
		default:
			errs = append(errs, brChoiceError(u.key, "take must be theirs, ours or custom"))
			continue
		}
		taken = append(taken, u.key)
	}
	for _, k := range chosenOrder {
		if !seen[k] {
			errs = append(errs, brChoiceError(k, "no such change in the plan (the template or the draft changed — refresh)"))
		}
	}
	opts.Map("values").Each(func(slug string, value any) {
		if v := brFind(cfg.Arr("variables"), "variableSlug", slug); v != nil {
			v.Set("value", brClone(value))
		} else {
			errs = append(errs, brChoiceError("variables/"+slug+"/value", "variable "+slug+" is not in the resulting draft"))
		}
	})
	// §21 #97 a: an empty preset list is never written (the key stays absent)
	brDropEmptyDevTools(cfg)
	return doc.Obj("config", cfg, "taken", taken, "notTaken", notTaken, "unresolved", unresolved, "errors", errs, "plan", plan)
}

// TemplateSyncMissing is TS templateSyncMissing.
func TemplateSyncMissing(cfg *doc.Map) *doc.Map { return brMissing(cfg) }

// NextSkipRecords is TS nextSkipRecords.
func NextSkipRecords(notTaken, previous []any, templateVersion any, now, uid string) []any {
	prev := brSkipsByKey(previous)
	out := make([]any, 0, len(notTaken))
	for _, x := range notTaken {
		c, _ := x.(*doc.Map)
		if p := prev[brJSString(brUnd(c, "key"))]; p != nil && brEq(brUnd(p, "theirsHash"), brUnd(c, "theirsHash")) {
			out = append(out, p)
			continue
		}
		out = append(out, doc.Obj("key", brUnd(c, "key"), "theirsHash", brUnd(c, "theirsHash"), "templateVersion", templateVersion, "skippedAt", now, "skippedBy", uid))
	}
	return out
}

// TemplateConfig is TS templateConfig.
func TemplateConfig(t *doc.Map) *doc.Map { return brPick(t, "variables", "secrets", "files", "sites") }
