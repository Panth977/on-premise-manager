package core

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

// BLOB_FILE_MAX is BLOB_FILE_MAX (also exported as STAGE_FILE_MAX).
const (
	BLOB_FILE_MAX  = 50 * 1024 * 1024
	STAGE_FILE_MAX = BLOB_FILE_MAX
)

// StagesCol is stagesCol.
func StagesCol() *driver.CollectionRef { return cloud.DB().Collection("stages") }

// StagePrefix is stagePrefix.
func StagePrefix(stageID string) string { return "staging/" + stageID + "/" }

// Pool is pool: f over items with at most n running at once; results keep their order, the first error wins.
func Pool[T, R any](items []T, n int, f func(T) (R, error)) ([]R, error) {
	out := make([]R, len(items))
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		next  int
		first error
	)
	for w := 0; w < min(n, len(items)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				k := next
				next++
				stop := first != nil
				mu.Unlock()
				if k >= len(items) || stop {
					return
				}
				r, err := f(items[k])
				mu.Lock()
				if err != nil && first == nil {
					first = err
				}
				out[k] = r
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out, first
}

// InStore is inStore: the blob is in the store (files/<hex>, or the legacy registry blob path).
func InStore(hex string) (bool, error) {
	if ok, err := Exists(Paths.File(hex)); ok || err != nil {
		return ok, err
	}
	return Exists(Paths.Blob(hex))
}

// ParseBlobList is parseBlobList: BlobNeed maps {sha256, sizeBytes}.
func ParseBlobList(v any) ([]any, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, cloud.E.Bad("blobs", "V_TYPE", "blobs must be a list of { sha256, sizeBytes }")
	}
	if len(l) > shared.STAGE_MAX_BLOBS {
		return nil, cloud.E.Bad("blobs", "V_TOO_MANY", fmt.Sprintf("at most %d blobs per commit", shared.STAGE_MAX_BLOBS))
	}
	seen := doc.New()
	for i, x := range l {
		o, _ := x.(*doc.Map)
		_, isArr := x.([]any)
		if o == nil && !isArr {
			return nil, cloud.E.Bad(fmt.Sprintf("blobs[%d]", i), "V_TYPE", "each blob must be { sha256, sizeBytes }")
		}
		sha, ok := o.StrOK("sha256")
		if !ok || !shared.RE.Sha256.MatchString(sha) {
			return nil, cloud.E.Bad(fmt.Sprintf("blobs[%d].sha256", i), "V_FORMAT", `sha256 must be "sha256:" + 64 lowercase hex`)
		}
		size := present(o, "sizeBytes")
		if !isInt(size) || size.(float64) < 0 {
			return nil, cloud.E.Bad(fmt.Sprintf("blobs[%d].sizeBytes", i), "V_RANGE", "sizeBytes must be an integer ≥ 0")
		}
		if size.(float64) > shared.SEED_ARCHIVE_MAX {
			return nil, cloud.E.TooLarge(fmt.Sprintf("blobs[%d]: at most %d bytes", i, int64(shared.SEED_ARCHIVE_MAX)))
		}
		seen.Set(sha, doc.Obj("sha256", sha, "sizeBytes", size))
	}
	out, total := []any{}, 0.0
	seen.Each(func(_ string, v any) {
		out = append(out, v)
		total += v.(*doc.Map).Num("sizeBytes")
	})
	if total > shared.STAGE_MAX_BYTES {
		return nil, cloud.E.TooLarge(fmt.Sprintf("at most %d new bytes per commit", int64(shared.STAGE_MAX_BYTES)))
	}
	return out, nil
}

func needInStore(b any) (bool, error) { return InStore(cloud.HexOf(b.(*doc.Map).Str("sha256"))) }

// OpenStage is openStage: the Stage answer {stageId, expiresAt, missing, present}.
func OpenStage(uid string, blobs []any) (*doc.Map, error) {
	stageID := ulid.New()
	expires := doc.IsoPlus(shared.STAGE_TTL_MS)
	if err := StagesCol().Doc(stageID).Create(doc.Obj("stageId", stageID, "createdBy", uid, "createdAt", doc.NowISO(), "expiresAt", expires, "blobs", len(blobs))); err != nil {
		return nil, err
	}
	have, err := Pool(blobs, 25, needInStore)
	if err != nil {
		return nil, err
	}
	missing := []any{}
	for i, b := range blobs {
		if !have[i] {
			missing = append(missing, b)
		}
	}
	return doc.Obj("stageId", stageID, "expiresAt", expires, "missing", missing, "present", len(blobs)-len(missing)), nil
}

func stageIDOK(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && shared.RE_STAGE_ID.MatchString(s)
}

const stageIDMsg = "stageId must be a stage id (POST /v1/stages)"

// LoadStage is loadStage: the caller's open stage (404 for another person's or an unknown id; 410 expired).
func LoadStage(stageID any, uid string, allowExpired bool) (*doc.Map, error) {
	id, ok := stageIDOK(stageID)
	if !ok {
		return nil, cloud.E.Bad("stageId", "V_FORMAT", stageIDMsg)
	}
	s, err := StagesCol().Doc(id).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() || s.Data().Val("createdBy") != uid {
		return nil, cloud.E.NotFound("stage not found")
	}
	d := s.Data()
	if exp, ok := doc.ParseISO(d.Str("expiresAt")); ok && !allowExpired && exp <= time.Now().UnixMilli() {
		return nil, cloud.E.Gone("the stage expired — commit again (the content is still in your browser)")
	}
	return d, nil
}

// WriteStaged is writeStaged: one verified blob into the stage (metadata opmSha256 = the server-computed hash).
func WriteStaged(stageID, hex string, data []byte) error {
	return cloud.Blobs().Put(shared.StagingObject(stageID, hex), data, driver.PutOptions{ContentType: "application/octet-stream", CacheControl: "private, max-age=0", Meta: map[string]string{"opmSha256": hex}})
}

// MarkStaged is markStaged.
func MarkStaged(path, hex string) error {
	return cloud.Blobs().SetMeta(path, map[string]*string{"opmSha256": &hex})
}

// DropStage is dropStage: delete a stage (objects + doc); only the creator's stage (uid "" = anyone's). Never fails.
func DropStage(stageID, uid string) {
	if _, ok := stageIDOK(stageID); !ok {
		return
	}
	s, err := StagesCol().Doc(stageID).Get()
	if err != nil {
		return
	}
	if uid != "" && s.Exists() && s.Data().Val("createdBy") != uid {
		return
	}
	_ = RemovePrefix(StagePrefix(stageID))
	if s.Exists() {
		_ = StagesCol().Doc(stageID).Delete()
	}
}

// RefPaths is refPaths: where each sha is referenced in a config (issue paths).
func RefPaths(config *doc.Map) map[string]string {
	m := map[string]string{}
	put := func(sha any, p string) {
		if s, ok := sha.(string); ok {
			if _, has := m[s]; !has {
				m[s] = p
			}
		}
	}
	for _, f := range mapsOf(config.Arr("files")) {
		if c := f.Map("content"); c != nil {
			put(c.Val("sha256"), "files["+JSString(present(f, "fileSlug"))+"].content")
		}
	}
	for _, s := range mapsOf(config.Arr("sites")) {
		for _, v := range mapsOf(s.Map("config").Arr("volumes")) {
			seed := v.Map("seed")
			at := "sites[" + JSString(present(s, "siteSlug")) + "].config.volumes[" + JSString(present(v, "volumeSlug")) + "].seed"
			switch seed.Val("kind") {
			case "tree":
				for _, x := range seed.Arr("entries") {
					e, _ := x.(*doc.Map)
					put(e.Val("sha256"), at+".entries["+JSString(present(e, "path"))+"]")
				}
			case "single":
				put(seed.Val("sha256"), at)
			case "archive":
				put(seed.Val("blobSha256"), at)
			}
		}
	}
	return m
}

// TestFault is testFault: emulator only, x-opm-test-fault simulates a crash at that point of a commit ("" = none).
func TestFault(c *cloud.Ctx) string {
	if !cloud.Env.Emulator() {
		return ""
	}
	if v := c.Headers["x-opm-test-fault"]; v == "after-verify" || v == "after-promote" {
		return v
	}
	return ""
}

func crash(at string) error { return cloud.E.New("internal", "test fault: crash "+at, nil, nil) }

// EnsureBlobsOpts of EnsureBlobs. Needs: BlobNeed maps; StageID, Fault: "" = none; Paths: sha → issue path.
type EnsureBlobsOpts struct {
	Needs   []any
	StageID string
	UID     string
	Fault   string
	Paths   map[string]string
}

type blobTodo struct {
	need *doc.Map
	hex  string
	path string
}
type blobMissing struct {
	need *doc.Map
	why  string
}

// EnsureBlobs is ensureBlobs: verify every needed blob (store, else the stage: size + sha256) and promote the staged ones
// into the store. 422 invalid V_BLOB_MISSING (details.missing) when one is neither stored nor staged correctly. Returns
// the number promoted.
func EnsureBlobs(o EnsureBlobsOpts) (int, error) {
	if o.StageID != "" {
		if _, err := LoadStage(o.StageID, o.UID, false); err != nil {
			return 0, err
		}
	}
	stored, err := Pool(o.Needs, 25, needInStore)
	if err != nil {
		return 0, err
	}
	var mu sync.Mutex
	var todo []blobTodo
	var missing []blobMissing
	miss := func(n *doc.Map, why string) (struct{}, error) {
		mu.Lock()
		defer mu.Unlock()
		missing = append(missing, blobMissing{n, why})
		return struct{}{}, nil
	}
	idx := make([]int, len(o.Needs))
	for i := range idx {
		idx[i] = i
	}
	_, err = Pool(idx, 10, func(i int) (struct{}, error) {
		if stored[i] {
			return struct{}{}, nil
		}
		n := o.Needs[i].(*doc.Map)
		hex := cloud.HexOf(n.Str("sha256"))
		if o.StageID == "" || !cloud.IsHex64(hex) {
			return miss(n, "not uploaded")
		}
		path := shared.StagingObject(o.StageID, hex)
		m, err := cloud.Blobs().Head(path)
		if err != nil {
			return struct{}{}, err
		}
		if m == nil {
			return miss(n, "not uploaded")
		}
		if float64(m.Size) != n.Num("sizeBytes") {
			return miss(n, fmt.Sprintf("staged %d bytes, the config says %s", m.Size, JSString(n.Val("sizeBytes"))))
		}
		sha, ok := m.Meta["opmSha256"]
		if !ok {
			if sha, _, err = HashObject(path); err != nil {
				return struct{}{}, err
			}
		}
		if sha != hex {
			_ = Remove(path)
			return miss(n, "staged bytes do not match the sha256")
		}
		mu.Lock()
		todo = append(todo, blobTodo{n, hex, path})
		mu.Unlock()
		return struct{}{}, nil
	})
	if err != nil {
		return 0, err
	}
	if len(missing) > 0 {
		sort.SliceStable(missing, func(i, j int) bool { return missing[i].need.Str("sha256") < missing[j].need.Str("sha256") })
		issues := make([]cloud.Issue, len(missing))
		needs := make([]any, len(missing))
		for i, x := range missing {
			sha := x.need.Str("sha256")
			p, ok := o.Paths[sha]
			if !ok {
				p = "config"
			}
			issues[i] = cloud.Issue{Path: p, Code: "V_BLOB_MISSING", Message: "content " + doc.JSSlice(sha, 19) + "… (" + JSString(x.need.Val("sizeBytes")) + " bytes): " + x.why + " — upload it with the commit (POST /v1/stages)", Severity: "error"}
			needs[i] = x.need
		}
		word := "s are"
		if len(missing) == 1 {
			word = " is"
		}
		e := cloud.E.Invalid(issues, fmt.Sprintf("%d file content%s missing: %s", len(missing), word, missing[0].why))
		e.Details.Set("missing", needs)
		return 0, e
	}
	if o.Fault == "after-verify" {
		return 0, crash("after-verify")
	}
	_, err = Pool(todo, 10, func(t blobTodo) (struct{}, error) {
		dst := Paths.File(t.hex)
		if ok, err := Exists(dst); ok || err != nil {
			return struct{}{}, err
		}
		if cloud.Blobs().Copy(t.path, dst) != nil {
			b, err := cloud.Blobs().Get(t.path)
			if err != nil {
				return struct{}{}, err
			}
			if b == nil {
				return struct{}{}, errors.New("staged object " + t.path + " disappeared")
			}
			return struct{}{}, cloud.Blobs().Put(dst, b, driver.PutOptions{ContentType: "application/octet-stream", CacheControl: "private, max-age=0"})
		}
		return struct{}{}, nil
	})
	if err != nil {
		return 0, err
	}
	if o.Fault == "after-promote" {
		return 0, crash("after-promote")
	}
	return len(todo), nil
}

// CommitBlobs is commitBlobs: the commit check of `config` against the head (nil = none), the hook for commitTo.
func CommitBlobs(c *cloud.Ctx, head *doc.Map, stageID string) func(config *doc.Map) error {
	return func(config *doc.Map) error {
		var base any
		if head != nil {
			base = head
		}
		needs, err := shared.NewConfigBlobs(base, config)
		if err != nil || len(needs) == 0 {
			return err
		}
		_, err = EnsureBlobs(EnsureBlobsOpts{Needs: needs, StageID: stageID, UID: c.Account.UID, Fault: TestFault(c), Paths: RefPaths(config)})
		return err
	}
}

// OptStageId is optStageId: "" = undefined.
func OptStageId(b *doc.Map) (string, error) {
	v := present(b, "stageId")
	if nullish(v) {
		return "", nil
	}
	s, ok := stageIDOK(v)
	if !ok {
		return "", cloud.E.Bad("stageId", "V_FORMAT", stageIDMsg)
	}
	return s, nil
}

// CleanupStages is cleanupStages: daily job, expired stage docs + their objects; now in ms.
func CleanupStages(now int64) (int, error) {
	q, err := StagesCol().Where("expiresAt", "<=", doc.ISO(time.UnixMilli(now))).Limit(500).Get()
	if err != nil {
		return 0, err
	}
	for _, d := range q.Docs {
		_ = RemovePrefix(StagePrefix(d.ID()))
		_ = d.Ref().Delete()
	}
	return q.Size(), nil
}
