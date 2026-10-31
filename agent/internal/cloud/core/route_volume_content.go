package core

import (
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// VcCol is vcCol of routes/volume-content.ts.
func VcCol(deploymentID string) *driver.CollectionRef {
	return cloud.Col.Deployments().Doc(deploymentID).Collection("volumeContent")
}

// VcKey is vcKey.
func VcKey(siteSlug, volumeSlug string) string { return siteSlug + ":" + volumeSlug }

// StoreTree is storeTree: the canonical tree JSON as files/<hex>; the tree hash.
func StoreTree(tree any) (string, error) {
	input, err := shared.VolumeTreeHashInput(tree)
	if err != nil {
		return "", err
	}
	hex := cloud.Sha256Hex([]byte(input))
	ok, err := Exists(Paths.File(hex))
	if err != nil {
		return "", err
	}
	if !ok {
		if err := WriteBytes(Paths.File(hex), []byte(input), "application/json"); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex, nil
}

// ReadTree is readTree: nil when not stored.
func ReadTree(treeHash string) (*doc.Map, error) {
	t, ok, err := ReadText(Paths.File(cloud.HexOf(treeHash)))
	if err != nil || !ok {
		return nil, err
	}
	v, err := doc.ParseString(t)
	if err != nil {
		return nil, err
	}
	return shared.NormalizeVolumeTree(v)
}

// VolumeContentCloudItems is volumeContentCloudItems: every volume with a pending push or an open snapshot request.
func VolumeContentCloudItems(deploymentID string) ([]any, error) {
	q, err := VcCol(deploymentID).Where("_open", "==", true).Get()
	if err != nil {
		return nil, err
	}
	var l []*doc.Map
	for _, d := range q.Docs {
		x := d.Data()
		var push any
		if p := x.Map("push"); p.Val("status") == "pending" {
			push = p
		}
		i := doc.Obj("siteSlug", present(x, "siteSlug"), "volumeSlug", present(x, "volumeSlug"), "push", push, "snapshotRequest", or(x, "snapshotRequest", nil))
		if doc.Truthy(push) || i.Truthy("snapshotRequest") {
			l = append(l, i)
		}
	}
	sort.SliceStable(l, func(i, j int) bool {
		a, b := l[i], l[j]
		if a.Str("siteSlug") == b.Str("siteSlug") {
			return a.Str("volumeSlug") < b.Str("volumeSlug")
		}
		return a.Str("siteSlug") < b.Str("siteSlug")
	})
	out := make([]any, len(l))
	for i, x := range l {
		out[i] = x
	}
	return out, nil
}

type pushBlobHit struct {
	at    time.Time
	hexes map[string]bool
}

var (
	pushBlobMu    sync.Mutex
	pushBlobCache = map[string]*pushBlobHit{}
)

// PushBlobAllowed is pushBlobAllowed: the tree-JSON + entry blobs of a PENDING push's version and its base version, for
// the signing site only. Cached per deployment epoch for 60 s.
func PushBlobAllowed(deploymentID, siteSlug string, epoch float64, hex string) (bool, error) {
	ck := deploymentID + ":" + siteSlug + ":" + JSString(epoch)
	pushBlobMu.Lock()
	hit := pushBlobCache[ck]
	pushBlobMu.Unlock()
	if hit == nil || time.Since(hit.at) > time.Minute {
		hexes := map[string]bool{}
		q, err := VcCol(deploymentID).Where("siteSlug", "==", siteSlug).Where("_open", "==", true).Get()
		if err != nil {
			return false, err
		}
		for _, d := range q.Docs {
			p := d.Data().Map("push")
			if p.Val("status") != "pending" {
				continue
			}
			for _, k := range []string{"treeHash", "baseTreeHash"} {
				th := p.Str(k)
				if th == "" {
					continue
				}
				hexes[cloud.HexOf(th)] = true
				t, err := ReadTree(th)
				if err != nil {
					return false, err
				}
				for _, e := range mapsOf(t.Arr("entries")) {
					hexes[cloud.HexOf(e.Str("sha256"))] = true
				}
			}
		}
		hit = &pushBlobHit{time.Now(), hexes}
		pushBlobMu.Lock()
		pushBlobCache[ck] = hit
		if len(pushBlobCache) > 500 {
			for k, x := range pushBlobCache {
				if time.Since(x.at) > time.Minute {
					delete(pushBlobCache, k)
				}
			}
		}
		pushBlobMu.Unlock()
	}
	return hit.hexes[hex], nil
}

var volumeSyncStates = []string{"in-sync", "live-changed", "cloud-ahead", "conflict", "never-snapshotted", "pending-upload", "error"}

func shaOrNull(v any) bool {
	if nullish(v) {
		return true
	}
	s, ok := v.(string)
	return ok && shared.RE.Sha256.MatchString(s)
}

// VolumeSyncError is volumeSyncError: the HeartbeatRequest.volumeSync shape check, "" = ok.
func VolumeSyncError(v any) string {
	if nullish(v) {
		return ""
	}
	l, ok := v.([]any)
	if !ok {
		return "volumeSync must be a list"
	}
	if len(l) > 200 {
		return "volumeSync has at most 200 entries"
	}
	for _, x := range l {
		r, ok := x.(*doc.Map)
		if !ok || r == nil {
			return "volumeSync entries must be objects"
		}
		if s, ok := r.StrOK("volumeSlug"); !ok || !shared.RE.VolumeSlug.MatchString(s) {
			return "volumeSync[].volumeSlug must be a volume slug"
		}
		if !hasStr(volumeSyncStates, r.Val("state")) {
			return "volumeSync[].state must be " + strings.Join(volumeSyncStates, " | ")
		}
		if !shaOrNull(present(r, "liveTreeHash")) || !shaOrNull(present(r, "snapshotTreeHash")) {
			return `volumeSync[] tree hashes must be "sha256:" + 64 hex or null`
		}
		if c := present(r, "liveChanged"); c != doc.Undefined {
			if _, ok := c.(bool); !ok {
				return "volumeSync[].liveChanged must be a boolean"
			}
		}
		if q := present(r, "queued"); q != doc.Undefined && (!isInt(q) || q.(float64) < 0) {
			return "volumeSync[].queued must be an integer ≥ 0"
		}
		if s := present(r, "scannedAt"); !nullish(s) {
			str, ok := s.(string)
			if ok {
				_, ok = doc.ParseISO(str)
			}
			if !ok || doc.JSLen(str) > 40 {
				return "volumeSync[].scannedAt must be an ISO time or null"
			}
		}
		if e := present(r, "lastError"); !nullish(e) {
			if _, ok := e.(string); !ok {
				return "volumeSync[].lastError must be a string or null"
			}
		}
	}
	return ""
}

const liveRefreshMs = 10 * 60_000

func liveKey(l *doc.Map) string {
	return doc.MarshalString([]any{present(l, "state"), or(l, "liveTreeHash", nil), or(l, "snapshotTreeHash", nil), l.Truthy("liveChanged"), or(l, "queued", 0.0), or(l, "lastError", nil)})
}

// StoreVolumeSync is storeVolumeSync: HeartbeatRequest.volumeSync → state.live per volume. list: a validated list or nil.
func StoreVolumeSync(deploymentID, siteSlug string, list any, now string) error {
	l, ok := list.([]any)
	if !ok || len(l) == 0 {
		return nil
	}
	refs := make([]*driver.DocRef, len(l))
	for i, x := range l {
		refs[i] = VcCol(deploymentID).Doc(VcKey(siteSlug, x.(*doc.Map).Str("volumeSlug")))
	}
	stored, err := cloud.DB().GetAll(refs...)
	if err != nil {
		return err
	}
	b := cloud.DB().Batch()
	writes := 0
	nowMs, nowOK := doc.ParseISO(now)
	for i, x := range l {
		r := x.(*doc.Map)
		var lastErr any
		if s, ok := r.StrOK("lastError"); ok {
			lastErr = doc.JSSlice(s, 2000)
		}
		slug := present(r, "volumeSlug")
		live := doc.Obj("volumeSlug", slug, "state", present(r, "state"), "liveTreeHash", or(r, "liveTreeHash", nil), "snapshotTreeHash", or(r, "snapshotTreeHash", nil), "liveChanged", r.Truthy("liveChanged"), "queued", or(r, "queued", 0.0), "scannedAt", or(r, "scannedAt", nil), "lastError", lastErr, "reportedAt", now)
		if prev := stored[i].Data().Map("live"); prev != nil && liveKey(prev) == liveKey(live) {
			at, ok := doc.ParseISO(JSString(present(prev, "reportedAt")))
			if ok && nowOK && nowMs-at < liveRefreshMs {
				continue
			}
		}
		writes++
		b.Set(refs[i], doc.Obj("deploymentId", deploymentID, "siteSlug", siteSlug, "volumeSlug", slug, "live", live), driver.Merge)
	}
	if writes > 0 {
		return b.Commit()
	}
	return nil
}

func validSha(v any) bool {
	s, ok := v.(string)
	return ok && shared.RE.Sha256.MatchString(s)
}

// SeedBlobContents is seedBlobContents: sha256 → text of every tree / single seed entry with render 'expressions'.
func SeedBlobContents(config *doc.Map) (*doc.Map, error) {
	var shas []string
	seen := map[string]bool{}
	add := func(v any) {
		if s := v.(string); !seen[s] {
			seen[s] = true
			shas = append(shas, s)
		}
	}
	for _, s := range mapsOf(config.Arr("sites")) {
		for _, v := range mapsOf(s.Map("config").Arr("volumes")) {
			sd := v.Map("seed")
			if sd.Val("kind") == "tree" {
				for _, e := range mapsOf(sd.Arr("entries")) {
					if e.Val("render") == "expressions" && validSha(e.Val("sha256")) {
						add(e.Val("sha256"))
					}
				}
			}
			if sd.Val("kind") == "single" && sd.Val("render") == "expressions" && validSha(sd.Val("sha256")) {
				add(sd.Val("sha256"))
			}
		}
	}
	texts, err := Pool(shas, 10, func(sha string) (any, error) {
		p := Paths.File(cloud.HexOf(sha))
		size, err := SizeOf(p)
		if err != nil || size < 0 || size > int64(shared.VOLUME_CONTENT_LIMITS.EditorTextMaxBytes) {
			return nil, err
		}
		b, err := cloud.Blobs().Get(p)
		if err != nil || b == nil || !shared.LooksLikeText(b) {
			return nil, err
		}
		return Utf8(b), nil
	})
	if err != nil {
		return nil, err
	}
	out := doc.New()
	for i, t := range texts {
		if t != nil {
			out.Set(shas[i], t)
		}
	}
	return out, nil
}
