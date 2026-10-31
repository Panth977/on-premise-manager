package core

import (
	"fmt"
	"sync"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/shared"
)

func init() {
	cloud.NormalizeStored = func(c *doc.Map) *doc.Map { return NormalizeStored(c, nil) }
	cloud.PrimeImageVolumes = func() error {
		PrimeImageVolumes()
		return nil
	}
}

func listOf(section string, items any) ([]*doc.Map, error) {
	l, ok := items.([]any)
	if !ok {
		return nil, cloud.E.Bad(section, "V_TYPE", section+" must be a list")
	}
	out := make([]*doc.Map, len(l))
	for i, x := range l {
		m, ok := x.(*doc.Map)
		if !ok || m == nil {
			p := fmt.Sprintf("%s[%d]", section, i)
			return nil, cloud.E.Bad(p, "V_TYPE", p+" must be an object")
		}
		out[i] = m
	}
	return out, nil
}

// undefNull is `v === undefined ? null : v` of a field.
func undefNull(m *doc.Map, k string) any {
	v := present(m, k)
	if v == doc.Undefined {
		return nil
	}
	return v
}

// NormVariables is normVariables.
func NormVariables(items any) ([]any, error) {
	l, err := listOf("variables", items)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(l))
	for i, v := range l {
		out[i] = v.Shallow().Set("description", or(v, "description", "")).Set("value", undefNull(v, "value")).Set("required", or(v, "required", false)).Set("options", or(v, "options", nil)).Set("validation", undefNull(v, "validation"))
	}
	return out, nil
}

// NormSecretDefs is normSecretDefs.
func NormSecretDefs(items any) ([]any, error) {
	l, err := listOf("secrets", items)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s.Shallow().Set("description", or(s, "description", "")).Set("required", or(s, "required", true)).Set("generate", or(s, "generate", nil)).Set("value", or(s, "value", nil)).Set("rotatedAt", or(s, "rotatedAt", nil))
	}
	return out, nil
}

func normLocation(l any, at string) (*doc.Map, error) {
	if nullish(l) {
		return doc.Obj("kind", "auto"), nil
	}
	if m, ok := l.(*doc.Map); ok && m != nil {
		if m.Val("kind") == "auto" {
			return doc.Obj("kind", "auto"), nil
		}
		if p, ok := m.StrOK("path"); ok && m.Val("kind") == "path" {
			return doc.Obj("kind", "path", "path", p), nil
		}
	}
	return nil, cloud.E.Bad(at, "V_LOCATION", `location must be { kind: "auto" } or { kind: "path", path: "./…" | "/abs/path" }`)
}

// orIndex is String(m[k] ?? i).
func orIndex(m *doc.Map, k string, i int) string { return JSString(or(m, k, float64(i))) }

// NormFiles is normFiles.
func NormFiles(items any) ([]any, error) {
	l, err := listOf("files", items)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(l))
	for i, f := range l {
		render := "expressions"
		if or(f, "editor", "text") == "binary" {
			render = "none"
		}
		loc, err := normLocation(present(f, "location"), "files["+orIndex(f, "fileSlug", i)+"].location")
		if err != nil {
			return nil, err
		}
		out[i] = f.Shallow().Set("description", or(f, "description", "")).Set("editor", or(f, "editor", "text")).Set("maxBytes", or(f, "maxBytes", 1048576.0)).Set("content", or(f, "content", nil)).Set("render", or(f, "render", render)).Set("location", loc)
	}
	return out, nil
}

// NormSites is normSites; images: "<service>:<version>" → {volumes}, nil = undefined.
func NormSites(items any, images *doc.Map) ([]any, error) {
	l, err := listOf("sites", items)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(l))
	for i, s := range l {
		cfg := present(s, "config")
		cm, isObj := cfg.(*doc.Map)
		isObj = isObj && cm != nil
		if cfg != doc.Undefined && !isObj {
			p := fmt.Sprintf("sites[%d].config", i)
			return nil, cloud.E.Bad(p, "V_TYPE", "config must be an object")
		}
		for _, v := range cm.Arr("volumes") {
			if vm, ok := v.(*doc.Map); ok && vm != nil {
				at := "sites[" + orIndex(s, "siteSlug", i) + "].config.volumes[" + JSString(present(vm, "volumeSlug")) + "].location"
				if _, err := normLocation(present(vm, "location"), at); err != nil {
					return nil, err
				}
			}
		}
		if !isObj {
			cm = doc.New()
		}
		sc, err := shared.NormalizeSiteConfig(cm, images)
		if err != nil {
			return nil, cloud.E.Bad("sites["+orIndex(s, "siteSlug", i)+"].config", "V_TYPE", "malformed site config ("+err.Error()+")")
		}
		out[i] = doc.Obj("siteSlug", present(s, "siteSlug"), "label", present(s, "label"), "config", sc)
	}
	return out, nil
}

// Validation is what SafeValidate returns: issue lists of *doc.Map.
type Validation struct{ Issues, Errors, Warnings []any }

// SafeValidate is safeValidate: validateConfig that reports malformed input as V_TYPE instead of failing.
func SafeValidate(config, ctx *doc.Map) *Validation {
	is, err := shared.ValidateConfig(config, ctx)
	if err != nil {
		i := doc.Obj("path", "", "code", "V_TYPE", "message", "malformed config ("+err.Error()+")", "severity", "error")
		return &Validation{[]any{i}, []any{i}, []any{}}
	}
	list := is.List()
	r := &Validation{Issues: list, Errors: []any{}, Warnings: []any{}}
	for _, x := range list {
		switch x.(*doc.Map).Str("severity") {
		case "error":
			r.Errors = append(r.Errors, x)
		case "warning":
			r.Warnings = append(r.Warnings, x)
		}
	}
	return r
}

var (
	ivMu     sync.Mutex
	ivCache  *doc.Map
	ivCached time.Time
)

// IMAGE_VOLUMES_TTL_MS is IMAGE_VOLUMES_TTL_MS.
const IMAGE_VOLUMES_TTL_MS = 15_000

// SetImageVolumes is setImageVolumes.
func SetImageVolumes(images *doc.Map) {
	ivMu.Lock()
	defer ivMu.Unlock()
	ivCache, ivCached = images, time.Now()
}

// ImageVolumesFresh is imageVolumesFresh.
func ImageVolumesFresh() bool {
	ivMu.Lock()
	defer ivMu.Unlock()
	return ivCache != nil && time.Since(ivCached) < IMAGE_VOLUMES_TTL_MS*time.Millisecond
}

// NormalizeStored is normalizeStored: images nil = the cached image volumes. Never fails: a malformed document is returned
// as it is.
func NormalizeStored(config, images *doc.Map) (out *doc.Map) {
	if images == nil {
		ivMu.Lock()
		images = ivCache
		ivMu.Unlock()
	}
	defer func() {
		if recover() != nil {
			out = config
		}
	}()
	if config == nil {
		return config
	}
	if _, ok := config.Val("files").([]any); !ok {
		return config
	}
	if _, ok := config.Val("sites").([]any); !ok {
		return config
	}
	r, err := shared.NormalizeConfig(config, images)
	if err != nil {
		return config
	}
	return r
}
