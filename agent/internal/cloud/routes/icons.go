package routes

import (
	"strconv"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

func init() {
	cloud.Handlers(map[string]cloud.Handler{"listIcons": listIcons, "uploadIcon": uploadIcon, "deleteIcon": deleteIcon, "getIconContent": getIconContent})
}

// iconNeedAnyWrite: a rule with write / publish / admin on any resource and any scope, or being a manager of a
// (non-deleted) deployment — everyone who can edit something that shows an icon.
func iconNeedAnyWrite(c *cloud.Ctx) error {
	a := c.Account
	if !a.Disabled {
		for _, r := range a.Access {
			for _, x := range r.Actions {
				if x == "write" || x == "publish" || x == "admin" {
					return nil
				}
			}
		}
	}
	q, err := cloud.Col.Deployments().Where("managerAccountUids", "array-contains", a.UID).Limit(5).Get()
	if err != nil {
		return err
	}
	for _, d := range q.Docs {
		if !d.Data().Truthy("deleted") {
			return nil
		}
	}
	return cloud.E.Forbidden("needs a write permission on any resource")
}

func listIcons(c *cloud.Ctx) (any, error) {
	q, err := cloud.Col.Icons().Select("iconId", "name", "contentType", "sizeBytes", "sha256", "createdBy", "createdAt").Get()
	if err != nil {
		return nil, err
	}
	usage, err := core.IconUsage()
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(q.Docs))
	for _, d := range q.Docs {
		items = append(items, cloud.Pub(d.Data()).Set("usedBy", float64(len(usage[d.ID()]))))
	}
	aSort(items, func(a, b *doc.Map) int {
		if n := aLocaleCompare(b.Str("createdAt"), a.Str("createdAt")); n != 0 {
			return n
		}
		return aLocaleCompare(b.Str("iconId"), a.Str("iconId"))
	})
	return doc.Obj("items", items), nil
}

func uploadIcon(c *cloud.Ctx) (any, error) {
	if err := iconNeedAnyWrite(c); err != nil {
		return nil, err
	}
	ct, _, _ := strings.Cut(c.Headers["content-type"], ";")
	ct = strings.ToLower(doc.JSTrim(ct))
	if !core.IsIconContentType(ct) {
		return nil, cloud.E.Bad("contentType", "V_ENUM", "Content-Type must be image/png, image/svg+xml or image/webp")
	}
	if len(c.Body) > shared.ICON_MAX_BYTES {
		return nil, cloud.E.TooLarge("the icon is " + strconv.Itoa(len(c.Body)) + " bytes (max " + strconv.Itoa(shared.ICON_MAX_BYTES) + ")")
	}
	if len(c.Body) == 0 {
		return nil, cloud.E.Bad("body", "V_REQUIRED", "the body is empty")
	}
	sniffed := core.SniffIcon(c.Body)
	if sniffed == "" {
		return nil, cloud.E.Bad("body", "V_FORMAT", "the file is not a PNG, SVG or WebP image")
	}
	if sniffed != ct {
		return nil, cloud.E.Bad("contentType", "V_FORMAT", "the file is "+sniffed+", but Content-Type says "+ct)
	}
	data := c.Body
	if sniffed == "image/svg+xml" {
		s, err := core.SanitizeSvg(core.Utf8(c.Body))
		if err != nil {
			if e, ok := err.(*core.SvgError); ok {
				return nil, cloud.E.Bad("body", "V_FORMAT", "not a valid SVG: "+e.Message)
			}
			return nil, err
		}
		data = []byte(s)
		if len(data) == 0 || len(data) > shared.ICON_MAX_BYTES {
			return nil, cloud.E.Bad("body", "V_FORMAT", "the sanitized SVG is empty or too large")
		}
	}
	raw, given := c.Query.Lookup("name")
	if !given {
		raw = "icon"
	}
	name := doc.JSTrim(raw)
	if given && (name == "" || doc.JSLen(name) > 80) {
		return nil, cloud.E.Bad("name", "V_LENGTH", "name must be 1–80 characters")
	}
	if name == "" {
		name = "icon"
	}
	sha256 := cloud.Sha256Of(data)
	ref := cloud.Col.Icons().Doc()
	out, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		dup, err := tx.Query(cloud.Col.Icons().Where("sha256", "==", sha256).Limit(1))
		if err != nil {
			return nil, err
		}
		if !dup.Empty() {
			return doc.Obj("icon", cloud.Pub(dup.Docs[0].Data()), "existing", true), nil
		}
		icon := doc.Obj("iconId", ref.ID(), "name", name, "contentType", sniffed, "sizeBytes", float64(len(data)), "sha256", sha256, "createdBy", c.Account.UID, "createdAt", doc.NowISO())
		tx.Create(ref, icon.Shallow().Set("_bytes", doc.Bytes(data)))
		return doc.Obj("icon", icon, "existing", false), nil
	})
	if err != nil {
		return nil, err
	}
	icon := out.Map("icon")
	hint := cloud.AuditHint{}
	if c.Audit != nil {
		hint = *c.Audit
	}
	hint.Target = "icons/" + icon.Str("iconId")
	hint.Data = doc.Obj("existing", out.Val("existing"), "name", icon.Val("name"), "contentType", icon.Val("contentType"), "sizeBytes", icon.Val("sizeBytes"), "sha256", icon.Val("sha256"))
	c.Audit = &hint
	return out, nil
}

func deleteIcon(c *cloud.Ctx) (any, error) {
	if err := iconNeedAnyWrite(c); err != nil {
		return nil, err
	}
	id := c.Params["iconId"]
	ref := cloud.Col.Icons().Doc(id)
	s, err := ref.Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("icon not found")
	}
	usage, err := core.IconUsage()
	if err != nil {
		return nil, err
	}
	if users := usage[id]; len(users) > 0 {
		what := "entities"
		if len(users) == 1 {
			what = "entity"
		}
		return nil, cloud.E.Conflict("the icon is used by "+strconv.Itoa(len(users))+" "+what, doc.Obj("users", users))
	}
	if err := ref.Delete(); err != nil {
		return nil, err
	}
	return doc.Obj("deleted", true), nil
}

func getIconContent(c *cloud.Ctx) (any, error) {
	s, err := cloud.Col.Icons().Doc(c.Params["iconId"]).Get()
	if err != nil {
		return nil, err
	}
	if !s.Exists() {
		return nil, cloud.E.NotFound("icon not found")
	}
	d := s.Data()
	data, _ := d.Val("_bytes").(doc.Bytes)
	headers := map[string]string{
		"Content-Type":           d.Str("contentType"),
		"Content-Length":         strconv.Itoa(len(data)),
		"Cache-Control":          "public, max-age=31536000, immutable",
		"X-Content-Type-Options": "nosniff",
		"ETag":                   `"` + strings.TrimPrefix(d.Str("sha256"), "sha256:") + `"`,
	}
	if d.Val("contentType") == "image/svg+xml" {
		headers["Content-Security-Policy"] = "default-src 'none'; style-src 'unsafe-inline'; sandbox"
	}
	return &cloud.Reply{Status: 200, Headers: headers, Body: []byte(data)}, nil
}
