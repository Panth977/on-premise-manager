package cloud

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"opm/agent/internal/cloud/doc"
)

// NormalizeStored is normalizeStored of core/config.ts (legacy shapes → current, with the cached image volumes). Until
// core/config.ts is ported it returns the config unchanged; the port must set this var and PrimeImageVolumes.
var NormalizeStored = func(config *doc.Map) *doc.Map { return config }

func parseConfig(s string) *doc.Map {
	v, err := doc.ParseString(s)
	if err != nil {
		panic(err) // JSON.parse throws
	}
	m, _ := v.(*doc.Map)
	return m
}

func emptyConfig() *doc.Map {
	return doc.Obj("variables", []any{}, "secrets", []any{}, "files", []any{}, "sites", []any{})
}

// TemplateConfig is templateConfig: the four config sections of a template.
func TemplateConfig(t *doc.Map) *doc.Map {
	return doc.Obj("variables", t.Val("variables"), "secrets", t.Val("secrets"), "files", t.Val("files"), "sites", t.Val("sites"))
}

// SlugifyLabel is slugifyLabel of shared/src/github-mirror.ts.
func SlugifyLabel(label, prefix string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(label) {
		if r >= 0x300 && r <= 0x36F {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	s := strings.Trim(reNotSlug.ReplaceAllString(b.String(), "-"), "-")
	if s == "" {
		s = prefix + "1"
	} else if s[0] < 'a' || s[0] > 'z' {
		s = prefix + s
	}
	if len(s) > 40 {
		s = s[:40]
	}
	s = strings.TrimRight(s, "-")
	if len(s) < 2 {
		s += "-1"
	}
	return s
}

var reNotSlug = regexp.MustCompile(`[^a-z0-9]+`)

// SlugOf: the stored slug, else derived from the label (pre-migrate-k documents). kind: template | deployment.
func SlugOf(d *doc.Map, kind string) string {
	if s := d.Str("slug"); s != "" {
		return s
	}
	label := ""
	if v := d.Val("label"); v != nil {
		label = jsString(v)
	}
	if kind == "template" {
		return SlugifyLabel(label, "t-")
	}
	return SlugifyLabel(label, "d-")
}

// DecodeDeployment is decodeDeployment (key order as the TS object spread).
func DecodeDeployment(d *doc.Map) *doc.Map {
	rest := d.Without("_draft", "_srcSnapshot", "_srcBase", "_srcSkipped", "srcTemplate")
	var src any
	if st := d.Map("srcTemplate"); st != nil {
		var snapshot any
		if s := d.Str("_srcSnapshot"); s != "" {
			snapshot = NormalizeStored(parseConfig(s))
		}
		tv := st.Val("templateVersion")
		base, isNum := st.NumOK("baseVersion")
		var baseV any = tv
		if isNum {
			baseV = base
		}
		var baseSnap any
		if s := d.Str("_srcBase"); s != "" {
			baseSnap = NormalizeStored(parseConfig(s))
		} else if m, ok := snapshot.(*doc.Map); ok && m != nil {
			baseSnap = TemplateConfig(m)
		} else {
			baseSnap = emptyConfig()
		}
		skipped := []any{}
		if s := d.Str("_srcSkipped"); s != "" {
			v, err := doc.ParseString(s)
			if err != nil {
				panic(err)
			}
			skipped, _ = v.([]any)
		}
		src = doc.Obj("templateId", st.Val("templateId"), "templateVersion", tv, "copiedAt", st.Val("copiedAt"), "snapshot", snapshot, "baseVersion", baseV, "baseSnapshot", baseSnap, "lastSyncedAt", st.Val("lastSyncedAt"), "lastSyncedBy", st.Val("lastSyncedBy"), "skipped", skipped)
	}
	rest.Set("slug", SlugOf(rest, "deployment"))
	if s := d.Str("_draft"); s != "" {
		rest.Set("draft", NormalizeStored(parseConfig(s)))
	} else {
		rest.Set("draft", emptyConfig())
	}
	rest.Set("srcTemplate", src)
	rest.Set("_cloudLastHash", d.Val("_cloudLastHash"))
	return rest
}

// SrcTemplateFields is srcTemplateFields: the stored form of Deployment.srcTemplate.
func SrcTemplateFields(src *doc.Map) *doc.Map {
	if src == nil {
		return doc.Obj("srcTemplate", nil, "_srcSnapshot", nil, "_srcBase", nil, "_srcSkipped", nil)
	}
	skipped := src.Val("skipped")
	if skipped == nil {
		skipped = []any{}
	}
	return doc.Obj("srcTemplate", doc.Obj("templateId", src.Val("templateId"), "templateVersion", src.Val("templateVersion"), "copiedAt", src.Val("copiedAt"), "label", src.Map("snapshot").Str("label"), "baseVersion", src.Val("baseVersion"), "lastSyncedAt", src.Val("lastSyncedAt"), "lastSyncedBy", src.Val("lastSyncedBy")),
		"_srcSnapshot", doc.MarshalString(src.Val("snapshot")), "_srcBase", doc.MarshalString(src.Val("baseSnapshot")), "_srcSkipped", doc.MarshalString(skipped))
}

// EncodeDeployment is encodeDeployment.
func EncodeDeployment(d *doc.Map) *doc.Map {
	rest := d.Without("draft", "srcTemplate")
	rest.Set("_draft", doc.MarshalString(d.Val("draft")))
	return rest.Spread(SrcTemplateFields(d.Map("srcTemplate")))
}

// DraftField is draftField: the partial update of the draft.
func DraftField(draft *doc.Map) *doc.Map { return doc.Obj("_draft", doc.MarshalString(draft)) }

// DecodeTemplate is decodeTemplate.
func DecodeTemplate(d *doc.Map) *doc.Map {
	rest := d.Without("_body", "_siteSlugs")
	var body *doc.Map
	if s := d.Str("_body"); s != "" {
		body = parseConfig(s)
	} else {
		body = doc.Obj("variables", []any{}, "secrets", []any{}, "files", []any{}, "sites", []any{})
	}
	rest.Set("slug", SlugOf(rest, "template"))
	return rest.Spread(NormalizeStored(body))
}

// EncodeTemplate is encodeTemplate.
func EncodeTemplate(t *doc.Map) *doc.Map {
	sections := doc.Obj("variables", t.Val("variables"), "secrets", t.Val("secrets"), "files", t.Val("files"), "sites", t.Val("sites"))
	rest := t.Without("variables", "secrets", "files", "sites")
	slugs := []any{}
	for _, s := range t.Arr("sites") {
		if m, ok := s.(*doc.Map); ok {
			slugs = append(slugs, m.Val("siteSlug"))
		}
	}
	return rest.Set("_body", doc.MarshalString(sections)).Set("_siteSlugs", slugs)
}

// HumanRevision is humanRevision: human views normalize legacy shapes, the stored hash is kept.
func HumanRevision(r *doc.Map) *doc.Map {
	return r.Shallow().Set("config", NormalizeStored(r.Map("config")))
}

// DecodeRevision is decodeRevision (byte-exact: the hash covers the config).
func DecodeRevision(d *doc.Map) *doc.Map {
	rest := d.Without("_config", "_catalog")
	return rest.Set("config", mustParse(d.Str("_config"))).Set("catalog", mustParse(d.Str("_catalog")))
}

func mustParse(s string) any {
	v, err := doc.ParseString(s)
	if err != nil {
		panic(err)
	}
	return v
}

// EncodeRevision is encodeRevision.
func EncodeRevision(r *doc.Map) *doc.Map {
	rest := r.Without("config", "catalog")
	return rest.Set("_config", doc.MarshalString(r.Val("config"))).Set("_catalog", doc.MarshalString(r.Val("catalog")))
}

// DecodeActivity is decodeActivity.
func DecodeActivity(d *doc.Map) *doc.Map {
	rest := d.Without("_data", "_tsMs", "_typeKeys")
	if s := d.Str("_data"); s != "" {
		return rest.Set("data", mustParse(s))
	}
	return rest.Set("data", doc.New())
}

// TypeKeys: "update.applied" → ["update.", "update.applied"].
func TypeKeys(t string) []any {
	parts := strings.Split(t, ".")
	out := []any{}
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], ".")+".")
	}
	return append(out, t)
}

// EncodeActivity is encodeActivity.
func EncodeActivity(a *doc.Map) *doc.Map {
	rest := a.Without("data")
	data := a.Val("data")
	if data == nil {
		data = doc.New()
	}
	ms, _ := doc.ParseISO(a.Str("ts"))
	return rest.Set("_data", doc.MarshalString(data)).Set("_tsMs", ms).Set("_typeKeys", TypeKeys(a.Str("type")))
}
