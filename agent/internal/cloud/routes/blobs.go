package routes

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/shared"
)

// blobUploadMax is BLOB_UPLOAD_MAX.
const blobUploadMax = 50 * 1024 * 1024

func init() {
	cloud.Handlers(map[string]cloud.Handler{"uploadBlob": uploadBlob, "getBlob": getBlob, "createUpload": createUpload, "getUpload": getUpload, "putUploadPart": putUploadPart, "completeUpload": completeUpload, "createStage": createStage, "putStageBlob": putStageBlob, "deleteStage": deleteStage})
}

func blobCanUpload(c *cloud.Ctx) error {
	if cloud.Has(c, "templates", "write", nil) {
		return nil
	}
	ok, err := cloud.HasOnAnyDeployment(c.Account, "deployment.config", "write")
	if err != nil {
		return err
	}
	if !ok {
		return cloud.E.Forbidden("needs templates.write or deployment.config.write")
	}
	return nil
}

func blobCanDownload(c *cloud.Ctx) error {
	if cloud.Has(c, "templates", "read", nil) {
		return nil
	}
	ok, err := cloud.HasOnAnyDeployment(c.Account, "deployment.config", "read")
	if err != nil {
		return err
	}
	if !ok {
		return cloud.E.Forbidden("needs templates.read or deployment.config.read")
	}
	return nil
}

// blobParseSha is parseShaParam.
func blobParseSha(s string) (string, error) {
	hex := cloud.HexOf(s)
	if !cloud.IsHex64(hex) {
		return "", cloud.E.Bad("sha256", "V_FORMAT", `sha256 must be 64 lowercase hex (optionally "sha256:" prefixed)`)
	}
	return hex, nil
}

func blobHuman(c *cloud.Ctx) core.Creator { return core.Creator{UID: c.Account.UID} }

func uploadBlob(c *cloud.Ctx) (any, error) {
	if err := blobCanUpload(c); err != nil {
		return nil, err
	}
	if len(c.Body) > shared.SMALL_BLOB_MAX {
		return nil, cloud.E.TooLarge("raw body larger than " + strconv.Itoa(shared.SMALL_BLOB_MAX) + " bytes (use POST /v1/uploads)")
	}
	hex := cloud.Sha256Hex(c.Body)
	if claimed := c.Headers["x-opm-sha256"]; claimed != "" && cloud.HexOf(strings.ToLower(doc.JSTrim(claimed))) != hex {
		return nil, cloud.E.Bad("X-OPM-Sha256", "V_FORMAT", "sha256 mismatch: body is sha256:"+hex)
	}
	p := core.Paths.File(hex)
	there, err := core.Exists(p)
	if err != nil {
		return nil, err
	}
	if !there {
		if err := core.WriteBytes(p, c.Body, ""); err != nil {
			return nil, err
		}
	}
	return doc.Obj("sha256", "sha256:"+hex, "sizeBytes", float64(len(c.Body))), nil
}

func getBlob(c *cloud.Ctx) (any, error) {
	if err := blobCanDownload(c); err != nil {
		return nil, err
	}
	hex, err := blobParseSha(c.Params["sha256"])
	if err != nil {
		return nil, err
	}
	// round C (§21 #55): ≤ BLOB_STREAM_MAX → 200 stream (no cross-origin redirect → no bucket CORS needed); larger → 302.
	// Content-addressed: the ETag never changes for a sha256, so If-None-Match answers 304.
	etag := `"` + hex + `"`
	if inm := c.Headers["if-none-match"]; inm != "" {
		for _, t := range strings.Split(inm, ",") {
			if strings.TrimPrefix(doc.JSTrim(t), "W/") == etag {
				there, err := core.Exists(core.Paths.File(hex))
				if err != nil {
					return nil, err
				}
				if !there {
					return nil, cloud.E.NotFound("the content is not stored")
				}
				return &cloud.Reply{Status: 304, Headers: map[string]string{"ETag": etag, "Cache-Control": "private, max-age=300", "X-OPM-Sha256": hex}}, nil
			}
		}
	}
	streamMax := int64(shared.BLOB_STREAM_MAX)
	r, err := core.ServeObject(core.Paths.File(hex), core.ServeOpts{Range: c.Headers["range"], Headers: map[string]string{"X-OPM-Sha256": hex}, StreamMax: &streamMax})
	if err != nil {
		return nil, err
	}
	if r.Status != 302 {
		r.Headers["ETag"] = etag
		r.Headers["Cache-Control"] = "private, max-age=300"
	}
	return r, nil
}

func createUpload(c *cloud.Ctx) (any, error) {
	if err := blobCanUpload(c); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"purpose", "sizeBytes", "sha256", "stageId"}); err != nil {
		return nil, err
	}
	// §21 #107: into a commit stage (not the store); not audited — the commit is the one entry
	stageID := ""
	if s := aFld(b, "stageId"); !aNullish(s) {
		st, err := core.LoadStage(s, c.Account.UID, false)
		if err != nil {
			return nil, err
		}
		stageID = st.Str("stageId")
	}
	if stageID != "" {
		c.Audit = &cloud.AuditHint{Skip: true}
	}
	v := &cloud.V{}
	purpose := b.Val("purpose")
	if purpose != "blob" && purpose != "seed-archive" {
		v.Err("purpose", "V_ENUM", "purpose must be blob or seed-archive (transfers start with POST …/transfers, shared files with POST …/files)")
	}
	size := aFld(b, "sizeBytes")
	if !aIsInt(size) || b.Num("sizeBytes") < 0 {
		v.Err("sizeBytes", "V_RANGE", "sizeBytes must be an integer ≥ 0")
	}
	sha, ok := b.StrOK("sha256")
	if !ok || !shared.RE.Sha256.MatchString(sha) {
		v.Err("sha256", "V_FORMAT", `sha256 must be "sha256:" + 64 hex`)
	}
	if err := v.ThrowIf(); err != nil {
		return nil, err
	}
	// round E: a volume seed archive (.tar.gz) goes to the same content-addressed store (files/<hex>) with a larger limit;
	// the stored Upload keeps purpose 'blob' (UploadPurpose has no 'seed-archive') + internal _seedArchive
	seed := purpose == "seed-archive"
	limit, what := float64(blobUploadMax), "blobs"
	if seed {
		limit, what = shared.SEED_ARCHIVE_MAX, "seed archives"
	}
	sizeBytes := b.Num("sizeBytes")
	if sizeBytes > limit {
		return nil, cloud.E.TooLarge(fmt.Sprintf("%s are at most %d bytes", what, int64(limit)))
	}
	if seed && sizeBytes < 1 {
		return nil, cloud.E.Bad("sizeBytes", "V_RANGE", "a seed archive is at least 1 byte")
	}
	target := core.Paths.File(cloud.HexOf(sha))
	if stageID != "" {
		target = shared.StagingObject(stageID, cloud.HexOf(sha))
	}
	u := core.NewUpload(core.NewUploadOpts{Purpose: "blob", SeedArchive: seed, SizeBytes: sizeBytes, Sha256: sha, CreatedBy: c.Account.UID, Target: target, StageID: stageID})
	if err := cloud.Col.Uploads().Doc(u.Str("uploadId")).Create(u); err != nil {
		return nil, err
	}
	return core.UploadView(u), nil
}

func getUpload(c *cloud.Ctx) (any, error) {
	u, err := core.LoadUpload(c.Params["uploadId"], blobHuman(c), true)
	if err != nil {
		return nil, err
	}
	if u.Val("status") == "failed" {
		msg := "the upload failed"
		if u.Truthy("error") {
			msg += ": " + core.JSString(u.Val("error"))
		}
		return nil, cloud.E.Gone(msg)
	}
	if exp, ok := doc.ParseISO(u.Str("expiresAt")); u.Val("status") == "open" && ok && exp <= time.Now().UnixMilli() {
		return nil, cloud.E.Gone("the upload expired")
	}
	return core.UploadView(u), nil
}

func putUploadPart(c *cloud.Ctx) (any, error) {
	u, err := core.LoadUpload(c.Params["uploadId"], blobHuman(c), false)
	if err != nil {
		return nil, err
	}
	return core.PutPart(u, c.Params["index"], c.Body)
}

func completeUpload(c *cloud.Ctx) (any, error) {
	u, err := core.LoadUpload(c.Params["uploadId"], blobHuman(c), false)
	if err != nil {
		return nil, err
	}
	if u.Truthy("_stageId") {
		c.Audit = &cloud.AuditHint{Skip: true}
	}
	return core.CompleteUpload(u, core.Actor{ID: c.Account.UID, SiteSlug: nil})
}

// ── §21 #107 commit staging (never audited: AUDIT_EXEMPT_OPS)

func createStage(c *cloud.Ctx) (any, error) {
	if err := blobCanUpload(c); err != nil {
		return nil, err
	}
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := cloud.OnlyKeys(b, []string{"blobs"}); err != nil {
		return nil, err
	}
	l, err := core.ParseBlobList(aFld(b, "blobs"))
	if err != nil {
		return nil, err
	}
	return core.OpenStage(c.Account.UID, l)
}

func putStageBlob(c *cloud.Ctx) (any, error) {
	st, err := core.LoadStage(c.Params["stageId"], c.Account.UID, false)
	if err != nil {
		return nil, err
	}
	hex, err := blobParseSha(c.Params["sha256"])
	if err != nil {
		return nil, err
	}
	if len(c.Body) > shared.SMALL_BLOB_MAX {
		return nil, cloud.E.TooLarge("raw body larger than " + strconv.Itoa(shared.SMALL_BLOB_MAX) + " bytes (use POST /v1/uploads with stageId)")
	}
	if len(c.Body) > core.STAGE_FILE_MAX {
		return nil, cloud.E.TooLarge("at most " + strconv.Itoa(core.STAGE_FILE_MAX) + " bytes")
	}
	if got := cloud.Sha256Hex(c.Body); got != hex {
		return nil, cloud.E.Bad("sha256", "V_FORMAT", "sha256 mismatch: body is sha256:"+got)
	}
	in, err := core.InStore(hex)
	if err != nil {
		return nil, err
	}
	if in {
		return doc.Obj("sha256", "sha256:"+hex, "sizeBytes", float64(len(c.Body)), "staged", false), nil
	}
	if err := core.WriteStaged(st.Str("stageId"), hex, c.Body); err != nil {
		return nil, err
	}
	return doc.Obj("sha256", "sha256:"+hex, "sizeBytes", float64(len(c.Body)), "staged", true), nil
}

func deleteStage(c *cloud.Ctx) (any, error) {
	st, err := core.LoadStage(c.Params["stageId"], c.Account.UID, true)
	if err != nil {
		return nil, err
	}
	core.DropStage(st.Str("stageId"), c.Account.UID)
	return doc.Obj("deleted", true), nil
}
