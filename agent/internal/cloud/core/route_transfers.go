package core

import (
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/shared"
)

// ValidateTransferBody is validateTransferBody of routes/transfers.ts.
func ValidateTransferBody(b *doc.Map) error {
	v := &cloud.V{}
	fn, ok := b.StrOK("fileName")
	if n := doc.JSLen(fn); !ok || n < 1 || n > 255 || strings.ContainsAny(fn, "/\\\x00") || fn == "." || fn == ".." {
		v.Err("fileName", "V_FORMAT", `fileName must be 1–255 characters without / \ or NUL`)
	}
	size := present(b, "sizeBytes")
	if !isInt(size) || size.(float64) < 0 {
		v.Err("sizeBytes", "V_RANGE", "sizeBytes must be an integer ≥ 0")
	}
	if s, ok := b.StrOK("sha256"); !ok || !shared.RE.Sha256.MatchString(s) {
		v.Err("sha256", "V_FORMAT", `sha256 must be "sha256:" + 64 hex`)
	}
	if n := present(b, "note"); n != doc.Undefined {
		if s, ok := n.(string); !ok || doc.JSLen(s) > 500 {
			v.Err("note", "V_LENGTH", "note must be ≤ 500 characters")
		}
	}
	if err := v.ThrowIf(); err != nil {
		return err
	}
	if b.Num("sizeBytes") > shared.TRANSFER_MAX {
		return cloud.E.TooLarge("transfers are at most " + JSString(float64(shared.TRANSFER_MAX)) + " bytes")
	}
	return nil
}

// NewTransferOpts of NewTransfer.
type NewTransferOpts struct {
	DeploymentID, SiteSlug, Direction string
	Body                              *doc.Map
	CreatedBy, UploadID               string
}

// NewTransfer is newTransfer.
func NewTransfer(o NewTransferOpts) *doc.Map {
	id := cloud.Col.Transfers(o.DeploymentID).Doc().ID()
	return doc.Obj("transferId", id, "deploymentId", o.DeploymentID, "siteSlug", o.SiteSlug, "direction", o.Direction, "fileName", present(o.Body, "fileName"), "sizeBytes", present(o.Body, "sizeBytes"), "sha256", present(o.Body, "sha256"), "storagePath", Paths.Transfer(o.DeploymentID, id), "uploadId", o.UploadID, "createdAt", doc.NowISO(), "createdBy", o.CreatedBy, "note", or(o.Body, "note", ""), "expiresAt", nil, "status", "uploading", "savedPath", nil, "error", nil, "fetchedAt", nil)
}
