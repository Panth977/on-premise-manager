package cloud

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"opm/agent/internal/cloud/doc"
)

// Port of shared/src/audit.ts. A missing value (undefined) is doc.Undefined.

// Audit limits.
const (
	AuditMaxChanges    = 200
	AuditMaxValueBytes = 2048
	SecretMask         = "***"
)

var (
	secretKey = regexp.MustCompile(`(?i)password|secret|token|accesskey|privatekey|resetlink`)
	safeKey   = regexp.MustCompile(`(?i)(slug|slugs|id|ids|version|at|by|count|fingerprint|^tokenhash)$`)
	idKeys    = []string{"siteSlug", "variableSlug", "secretSlug", "fileSlug", "serviceSlug", "routeId", "hostname", "name", "tagSlug", "resource", "uid", "path"}
	identRe   = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$-]*$`)
)

type actx struct {
	parent  any
	listKey *string
	scope   bool
}

// asObj: isPlainObject of audit.ts is true for class instances too: a Timestamp's own properties are what it iterates.
func asObj(v any) *doc.Map {
	switch x := v.(type) {
	case *doc.Map:
		return x
	case doc.Timestamp:
		return doc.Obj("seconds", x.Seconds, "nanoseconds", x.Nanos)
	}
	return nil
}

func isSecretField(key string, parent any, listKey *string, scope bool) bool {
	if scope && !safeKey.MatchString(key) {
		return true
	}
	if (key == "value" || key == "plainValue") && listKey != nil && *listKey == "secrets" {
		return true
	}
	if key == "value" {
		if p := asObj(parent); p != nil && p.Val("secret") == true {
			return true
		}
	}
	return secretKey.MatchString(key) && !safeKey.MatchString(key)
}

func isPrim(v any) bool {
	if _, ok := v.(string); ok {
		return true
	}
	_, ok := doc.AsNumber(v)
	return ok
}
func isUndef(v any) bool { return v == doc.Undefined }
func isObjV(v any) bool {
	if asObj(v) != nil {
		return true
	}
	_, ok := v.([]any)
	return ok
}
func maskValue(v any) any {
	if v == nil || isUndef(v) || v == "" {
		if isUndef(v) {
			return nil
		}
		return v
	}
	if isPrim(v) {
		return SecretMask
	}
	return v
}
func childScope(c actx, key *string) bool {
	return c.scope || (key != nil && secretKey.MatchString(*key) && !safeKey.MatchString(*key))
}

func redactIn(v any, c actx, key *string) any {
	if key != nil && isSecretField(*key, c.parent, c.listKey, c.scope) && isPrim(v) {
		return maskValue(v)
	}
	if a, ok := v.([]any); ok {
		out := make([]any, len(a))
		for i, x := range a {
			out[i] = redactIn(x, actx{parent: a, listKey: key, scope: c.scope}, nil)
		}
		return out
	}
	if m := asObj(v); m != nil {
		out := doc.New()
		scope := childScope(c, key)
		var lk *string
		if c.listKey != nil && key == nil {
			lk = c.listKey
		}
		for _, k := range m.Keys() {
			x, _ := m.Get(k)
			k := k
			out.Set(k, redactIn(x, actx{parent: m, listKey: lk, scope: scope}, &k))
		}
		return out
	}
	return v
}

// RedactSecrets is a deep copy with every secret value replaced by "***".
func RedactSecrets(v any) any { return redactIn(v, actx{}, nil) }

// CapAuditValue: values whose JSON is larger than 2 KB become { $truncated, bytes, preview }.
func CapAuditValue(v any, max int) any {
	if isUndef(v) {
		return nil
	}
	s, err := doc.Marshal(v)
	if err != nil || len(s) <= max {
		return v
	}
	return doc.Obj("$truncated", true, "bytes", len(s), "preview", doc.JSSlice(string(s), 2000))
}

func idKeyOf(a, b []any) string {
	all := append(append([]any{}, a...), b...)
	if len(all) == 0 {
		return ""
	}
	for _, x := range all {
		if asObj(x) == nil {
			return ""
		}
	}
	for _, k := range idKeys {
		ok := true
		for _, list := range [][]any{a, b} {
			seen := map[string]bool{}
			for _, x := range list {
				s, isS := asObj(x).StrOK(k)
				if !isS || seen[s] {
					ok = false
					break
				}
				seen[s] = true
			}
		}
		if ok {
			return k
		}
	}
	return ""
}

func joinPath(path, key string) string {
	if path == "" {
		if identRe.MatchString(key) {
			return key
		}
		return "[" + doc.Quote(key) + "]"
	}
	if identRe.MatchString(key) {
		return path + "." + key
	}
	return path + "[" + doc.Quote(key) + "]"
}

func typeOf(v any) string {
	switch v.(type) {
	case nil, *doc.Map, []any, doc.Timestamp, doc.Bytes:
		return "object"
	case string:
		return "string"
	case bool:
		return "boolean"
	}
	if isUndef(v) {
		return "undefined"
	}
	return "number"
}

func same(a, b any) bool {
	if a == nil || b == nil || isUndef(a) || isUndef(b) {
		return (a == nil && b == nil) || (isUndef(a) && isUndef(b))
	}
	ta, tb := typeOf(a), typeOf(b)
	if ta != tb {
		return false
	}
	if ta != "object" {
		x, okx := doc.AsNumber(a)
		y, oky := doc.AsNumber(b)
		if okx && oky {
			return x == y
		}
		return a == b
	}
	return doc.MarshalString(a) == doc.MarshalString(b)
}

// AuditChange is one changed leaf ({path, before, after, secret?}); a *doc.Map in AuditDiffResult.
type AuditDiffResult struct {
	Changes []any
	Total   int
}

// AuditDiff is auditDiff with the default options: deep diff of two documents (nil = did not exist).
func AuditDiff(before, after any) AuditDiffResult {
	ignore := map[string]bool{"updatedAt": true, "updatedBy": true, "cloudActivitySeq": true}
	out := []any{}
	total := 0
	emit := func(path string, a, b any, c actx, key *string) {
		total++
		if len(out) >= AuditMaxChanges {
			return
		}
		secret := key != nil && isSecretField(*key, c.parent, c.listKey, c.scope) && (isPrim(a) || isPrim(b)) && !isObjV(a) && !isObjV(b)
		if secret {
			out = append(out, doc.Obj("path", path, "before", maskValue(a), "after", maskValue(b), "secret", true))
			return
		}
		nn := func(v any) any {
			if isUndef(v) {
				return nil
			}
			return v
		}
		out = append(out, doc.Obj("path", path, "before", CapAuditValue(redactIn(nn(a), c, key), AuditMaxValueBytes), "after", CapAuditValue(redactIn(nn(b), c, key), AuditMaxValueBytes)))
	}
	get := func(m *doc.Map, k string) any {
		if v, ok := m.Get(k); ok {
			return v
		}
		return doc.Undefined
	}
	var walk func(path string, a, b any, c actx, key *string)
	walk = func(path string, a, b any, c actx, key *string) {
		if same(a, b) {
			return
		}
		if key != nil && isSecretField(*key, c.parent, c.listKey, c.scope) && (isPrim(a) || isPrim(b)) {
			emit(path, a, b, c, key)
			return
		}
		ma, mb := asObj(a), asObj(b)
		if ma != nil && mb != nil {
			set := map[string]bool{}
			keys := []string{}
			for _, k := range append(ma.Keys(), mb.Keys()...) {
				if !set[k] {
					set[k] = true
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			var childList *string
			if c.listKey != nil && key == nil {
				childList = c.listKey
			}
			scope := childScope(c, key)
			for _, k := range keys {
				if path == "" && ignore[k] {
					continue
				}
				k := k
				var parent any = b
				x, y := get(ma, k), get(mb, k)
				if isUndef(y) {
					parent = a
				}
				walk(joinPath(path, k), x, y, actx{parent: parent, listKey: childList, scope: scope}, &k)
			}
			return
		}
		la, oka := a.([]any)
		lb, okb := b.([]any)
		if oka && okb {
			if idk := idKeyOf(la, lb); idk != "" {
				mapA, mapB := map[string]any{}, map[string]any{}
				var orderAll []string
				for _, x := range la {
					id := asObj(x).Str(idk)
					mapA[id] = x
					orderAll = append(orderAll, id)
				}
				var idsB []string
				for _, x := range lb {
					id := asObj(x).Str(idk)
					mapB[id] = x
					idsB = append(idsB, id)
				}
				for _, id := range idsB {
					if _, ok := mapA[id]; !ok {
						orderAll = append(orderAll, id)
					}
				}
				for _, id := range orderAll {
					x, okx := mapA[id]
					y, oky := mapB[id]
					if !okx {
						x = doc.Undefined
					}
					if !oky {
						y = doc.Undefined
					}
					walk(path+"["+id+"]", x, y, actx{parent: b, listKey: key, scope: c.scope}, nil)
				}
				var oa, ob []any
				for _, x := range la {
					id := asObj(x).Str(idk)
					if _, ok := mapB[id]; ok {
						oa = append(oa, id)
					}
				}
				for _, id := range idsB {
					if _, ok := mapA[id]; ok {
						ob = append(ob, id)
					}
				}
				if fmt.Sprint(oa...) != fmt.Sprint(ob...) || doc.MarshalString(oa) != doc.MarshalString(ob) {
					emit(path+"[*order]", oa, ob, c, nil)
				}
				return
			}
			n := len(la)
			if len(lb) > n {
				n = len(lb)
			}
			at := func(l []any, i int) any {
				if i < len(l) {
					return l[i]
				}
				return doc.Undefined
			}
			for i := 0; i < n; i++ {
				walk(fmt.Sprintf("%s[%d]", path, i), at(la, i), at(lb, i), actx{parent: b, listKey: key, scope: c.scope}, nil)
			}
			return
		}
		emit(path, a, b, c, key)
	}
	nullish := func(v any) bool { return v == nil || isUndef(v) || v == (*doc.Map)(nil) }
	aN, bN := nullish(before), nullish(after)
	ma, mb := asObj(before), asObj(after)
	switch {
	case aN && mb != nil && !bN:
		for _, k := range mb.SortedKeys() {
			if !ignore[k] {
				k := k
				emit(k, nil, mb.Val(k), actx{parent: mb}, &k)
			}
		}
	case bN && ma != nil && !aN:
		for _, k := range ma.SortedKeys() {
			if !ignore[k] {
				k := k
				emit(k, ma.Val(k), nil, actx{parent: ma}, &k)
			}
		}
	default:
		if aN {
			before = nil
		}
		if bN {
			after = nil
		}
		walk("", before, after, actx{}, nil)
	}
	return AuditDiffResult{out, total}
}

// AuditActionMatches: action filter: exact, or a prefix ending in ".".
func AuditActionMatches(action, filter string) bool {
	if strings.HasSuffix(filter, ".") {
		return strings.HasPrefix(action, filter)
	}
	return action == filter
}

// AuditActionKeys: "template.updated" → ["template.", "template.updated"].
func AuditActionKeys(action string) []any { return TypeKeys(action) }
