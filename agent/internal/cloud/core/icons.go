package core

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

const bom = "\xef\xbb\xbf"

var reSvgTag = regexp.MustCompile(`<` + ciRe("svg") + `[` + jsSpace + `>/]`)

// SniffIcon is sniffIcon: the content type from the magic bytes (png, webp, svg text), "" = null.
func SniffIcon(b []byte) string {
	if len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "image/webp"
	}
	head := Utf8(b[:min(2048, len(b))])
	head = strings.TrimLeftFunc(strings.TrimPrefix(head, bom), doc.IsJSSpace)
	if (strings.HasPrefix(head, "<") && reSvgTag.MatchString(Utf8(b))) || strings.HasPrefix(head, "<svg") {
		return "image/svg+xml"
	}
	return ""
}

// IsIconContentType is isIconContentType.
func IsIconContentType(s string) bool {
	for _, x := range shared.ICON_CONTENT_TYPES {
		if x == s {
			return true
		}
	}
	return false
}

// SvgError is SvgError: the document is not a well-formed SVG (a client error); other errors of SanitizeSvg are 500s.
type SvgError struct{ Message string }

func (e *SvgError) Error() string { return e.Message }

func svgErr(msg string) error { return &SvgError{msg} }

var (
	dropElements = map[string]bool{"script": true, "foreignobject": true, "iframe": true, "embed": true, "object": true, "handler": true, "listener": true, "audio": true, "video": true, "canvas": true}
	reAttr       = regexp.MustCompile(`([^` + jsSpace + `=/>"']+)(?:[` + jsSpace + `]*=[` + jsSpace + `]*(?:"([^"]*)"|'([^']*)'|([^` + jsSpace + `"'>]+)))?`)
	safeData     = regexp.MustCompile(`^` + ciRe("data:image/") + `(?:` + ciRe("png") + `|[jJ][pP][eE]?[gG]|` + ciRe("gif") + `|` + ciRe("webp") + `);` + ciRe("base64") + `,[A-Za-z0-9+/=` + jsSpace + `]+$`)
	reHexEnt     = regexp.MustCompile(`&#[xX]([0-9a-fA-F]+);?`)
	reDecEnt     = regexp.MustCompile(`&#([0-9]+);?`)
	reColon      = regexp.MustCompile(ciRe("&colon;"))
	reTabNl      = regexp.MustCompile(ciRe("&tab;") + `|` + ciRe("&newline;"))
	reEntOK      = regexp.MustCompile(`^(?:` + ciRe("amp") + `|` + ciRe("lt") + `|` + ciRe("gt") + `|` + ciRe("quot") + `|` + ciRe("apos") + `|#[0-9]+|#[xX][0-9a-fA-F]+);`)
	reUnsafeWS   = regexp.MustCompile(`[` + jsSpace + `\x00-\x1f]`)
	reURL        = regexp.MustCompile(`url\(([^)]*)\)`)
	reTag        = regexp.MustCompile(`(?s)^([A-Za-z_][\w.:-]*)(.*)$`)
	reBadCSS     = regexp.MustCompile(ciRe("url") + `[` + jsSpace + `]*\(|` + ciRe("@import") + `|` + ciRe("expression") + `[` + jsSpace + `]*\(|` + ciRe("javascript:"))
	reEntity     = regexp.MustCompile(ciRe("<!entity"))
)

type codePointError struct{ text string }

// fromCodePoint is String.fromCodePoint: a RangeError (through panic, recovered in SanitizeSvg) past U+10FFFF.
func fromCodePoint(n uint64, ok bool, text string) string {
	if !ok || n > 0x10FFFF {
		panic(codePointError{"Invalid code point " + text})
	}
	return string(rune(n))
}

func unescapeAttr(v string) string {
	v = reHexEnt.ReplaceAllStringFunc(v, func(m string) string {
		h := reHexEnt.FindStringSubmatch(m)[1]
		n, err := strconv.ParseUint(h, 16, 64)
		return fromCodePoint(n, err == nil, h)
	})
	v = reDecEnt.ReplaceAllStringFunc(v, func(m string) string {
		d := reDecEnt.FindStringSubmatch(m)[1]
		n, err := strconv.ParseUint(d, 10, 64)
		return fromCodePoint(n, err == nil, d)
	})
	v = reColon.ReplaceAllString(v, ":")
	v = reTabNl.ReplaceAllString(v, "")
	return strings.ReplaceAll(v, "&amp;", "&")
}

func escAttr(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '&' && !reEntOK.MatchString(v[i+1:]) {
			b.WriteString("&amp;")
		} else {
			b.WriteByte(v[i])
		}
	}
	return strings.NewReplacer(`"`, "&quot;", "<", "&lt;").Replace(b.String())
}

func unsafeURLValue(v string) bool {
	u := strings.ToLower(reUnsafeWS.ReplaceAllString(unescapeAttr(v), ""))
	if strings.Contains(u, "javascript:") || strings.Contains(u, "vbscript:") || strings.Contains(u, "@import") {
		return true
	}
	for _, m := range reURL.FindAllStringSubmatch(u, -1) {
		x := m[1]
		if x != "" && (x[0] == '\'' || x[0] == '"') {
			x = x[1:]
		}
		if x != "" && (x[len(x)-1] == '\'' || x[len(x)-1] == '"') {
			x = x[:len(x)-1]
		}
		if !strings.HasPrefix(x, "#") && !safeData.MatchString(x) {
			return true
		}
	}
	return false
}

func cleanAttrs(raw string) string {
	var out strings.Builder
	for _, m := range reAttr.FindAllStringSubmatchIndex(raw, -1) {
		name := raw[m[2]:m[3]]
		lname := strings.ToLower(name)
		value, has := "", false
		for g := 4; g <= 8; g += 2 {
			if m[g] >= 0 {
				value, has = raw[m[g]:m[g+1]], true
				break
			}
		}
		if strings.HasPrefix(lname, "on") || !has {
			continue
		}
		if lname == "href" || strings.HasSuffix(lname, ":href") || lname == "src" {
			v := doc.JSTrim(unescapeAttr(value))
			if !strings.HasPrefix(v, "#") && !safeData.MatchString(v) {
				continue
			}
		}
		if !(lname == "xmlns" || strings.HasPrefix(lname, "xmlns:")) && unsafeURLValue(value) {
			continue
		}
		out.WriteString(" " + name + `="` + escAttr(value) + `"`)
	}
	return out.String()
}

func indexFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	if i := strings.Index(s[from:], sub); i >= 0 {
		return i + from
	}
	return -1
}

// indexFoldASCII finds an ASCII needle case-insensitively from a byte offset.
func indexFoldASCII(s, lowerNeedle string, from int) int {
	for i := from; i+len(lowerNeedle) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(lowerNeedle)], lowerNeedle) {
			return i
		}
	}
	return -1
}

// SanitizeSvg is sanitizeSvg: keeps the element tree, drops active content and external references. *SvgError when the
// document is not a well-formed SVG. A <style> whose end tag has no ">" is an *SvgError here (TS loops forever).
func SanitizeSvg(input string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			c, ok := r.(codePointError)
			if !ok {
				panic(r)
			}
			err = errors.New(c.text)
		}
	}()
	s := strings.TrimPrefix(input, bom)
	if reEntity.MatchString(s) {
		return "", svgErr("SVG with entity declarations is not allowed")
	}
	i := 0
	var out strings.Builder
	var stack []string
	drop := 0
	sawRoot, rootClosed := false, false
	addText := func(text string) error {
		if len(stack) > 0 {
			if drop == 0 {
				out.WriteString(text)
			}
		} else if doc.JSTrim(text) != "" {
			return svgErr("text outside the <svg> element")
		}
		return nil
	}
	for i < len(s) {
		lt := indexFrom(s, "<", i)
		if lt < 0 {
			if err := addText(s[i:]); err != nil {
				return "", err
			}
			break
		}
		if err := addText(s[i:lt]); err != nil {
			return "", err
		}
		rest := s[lt:]
		if strings.HasPrefix(rest, "<!--") {
			e := indexFrom(s, "-->", lt+4)
			if e < 0 {
				return "", svgErr("unterminated comment")
			}
			i = e + 3
			continue
		}
		if strings.HasPrefix(rest, "<![CDATA[") {
			e := indexFrom(s, "]]>", lt)
			if e < 0 {
				return "", svgErr("unterminated CDATA")
			}
			if len(stack) == 0 {
				return "", svgErr("CDATA outside the <svg> element")
			}
			if drop == 0 {
				out.WriteString(s[lt : e+3])
			}
			i = e + 3
			continue
		}
		if strings.HasPrefix(rest, "<?") {
			e := indexFrom(s, "?>", lt)
			if e < 0 {
				return "", svgErr("unterminated processing instruction")
			}
			i = e + 2
			continue
		}
		if strings.HasPrefix(rest, "<!") {
			e := indexFrom(s, ">", lt)
			if e < 0 || strings.Contains(s[lt:e], "[") {
				return "", svgErr("DOCTYPE with an internal subset is not allowed")
			}
			i = e + 1
			continue
		}
		j := lt + 1
		var q byte
		for ; j < len(s); j++ {
			ch := s[j]
			if q != 0 {
				if ch == q {
					q = 0
				}
			} else if ch == '"' || ch == '\'' {
				q = ch
			} else if ch == '>' {
				break
			}
		}
		if j >= len(s) {
			return "", svgErr("unterminated tag")
		}
		body := s[lt+1 : j]
		i = j + 1
		if strings.HasPrefix(body, "/") {
			name := doc.JSTrim(body[1:])
			top := ""
			if len(stack) > 0 {
				top = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
			if top == "" || top != name {
				return "", svgErr("mismatched </" + name + ">")
			}
			if drop > 0 {
				drop--
				continue
			}
			out.WriteString("</" + name + ">")
			if len(stack) == 0 {
				rootClosed = true
			}
			continue
		}
		m := reTag.FindStringSubmatch(body)
		if m == nil {
			return "", svgErr("malformed tag")
		}
		name, attrs := m[1], m[2]
		trimmed := strings.TrimRightFunc(attrs, doc.IsJSSpace)
		selfClose := strings.HasSuffix(trimmed, "/")
		if selfClose {
			attrs = trimmed[:len(trimmed)-1]
		}
		lname := strings.ToLower(name)
		if len(stack) == 0 {
			if rootClosed || sawRoot {
				return "", svgErr("more than one root element")
			}
			if lname != "svg" && !strings.HasSuffix(lname, ":svg") {
				return "", svgErr("the root element must be <svg>")
			}
			sawRoot = true
		}
		local := lname[strings.LastIndexByte(lname, ':')+1:]
		if drop > 0 || dropElements[local] {
			if !selfClose {
				stack = append(stack, name)
				drop++
			}
			continue
		}
		if local == "style" && !selfClose {
			closeAt := indexFoldASCII(s, "</"+lname, i)
			if closeAt < 0 {
				return "", svgErr("unterminated <style>")
			}
			css := s[i:closeAt]
			e := indexFrom(s, ">", closeAt)
			if e < 0 {
				return "", svgErr("unterminated <style>")
			}
			i = e + 1
			if reBadCSS.MatchString(css) {
				continue
			}
			out.WriteString("<" + name + cleanAttrs(attrs) + ">" + css + "</" + name + ">")
			continue
		}
		end := ">"
		if selfClose {
			end = "/>"
		}
		out.WriteString("<" + name + cleanAttrs(attrs) + end)
		if !selfClose {
			stack = append(stack, name)
		} else if len(stack) == 0 {
			rootClosed = true
		}
	}
	if !sawRoot {
		return "", svgErr("no <svg> element")
	}
	if len(stack) > 0 {
		return "", svgErr("unclosed <" + stack[len(stack)-1] + ">")
	}
	return out.String(), nil
}

// IconUsage is iconUsage: iconId → IconUser list of every entity that references "upload:<iconId>".
func IconUsage() (map[string][]any, error) {
	out := map[string][]any{}
	add := func(v any, typ, id string, label any) {
		if p := shared.IconUploadId(v); p != nil && *p != "" {
			out[*p] = append(out[*p], doc.Obj("type", typ, "id", id, "label", label))
		}
	}
	scan := func(q *driver.Query, field, labelField, typ string) error {
		s, err := q.Get()
		if err != nil {
			return err
		}
		for _, d := range s.Docs {
			add(d.Data().Val(field), typ, d.ID(), or(d.Data(), labelField, d.ID()))
		}
		return nil
	}
	if err := scan(cloud.Col.Services().Select("icon", "label"), "icon", "label", "service"); err != nil {
		return nil, err
	}
	tpl, err := cloud.Col.Templates().Get()
	if err != nil {
		return nil, err
	}
	for _, d := range tpl.Docs {
		t := cloud.DecodeTemplate(d.Data())
		add(t.Val("icon"), "template", d.ID(), present(t, "label"))
	}
	if err := scan(cloud.Col.Tags().Select("icon", "label"), "icon", "label", "tag"); err != nil {
		return nil, err
	}
	if err := scan(cloud.Col.Deployments().Where("deleted", "==", false).Select("image", "label"), "image", "label", "deployment"); err != nil {
		return nil, err
	}
	if err := scan(cloud.Col.Accounts().Select("img", "name"), "img", "name", "account"); err != nil {
		return nil, err
	}
	return out, nil
}

// VIconFormat is vIconFormat: format check (null / undefined ok). picture = Account.img / Deployment.image.
func VIconFormat(v *cloud.V, val any, path string, picture bool) {
	is := shared.NewIssues("draft")
	if picture {
		shared.CheckPicture(val, path, is)
	} else {
		shared.CheckIcon(val, path, is)
	}
	for _, i := range IssuesOf(is.Errors()) {
		v.Err(i.Path, i.Code, i.Message)
	}
}

// IconField is one { path, value } of VIconRefs.
type IconField struct {
	Path  string
	Value any
}

// VIconRefs is vIconRefs: "upload:<id>" values must reference an existing icon (V_REF).
func VIconRefs(v *cloud.V, fields []IconField) error {
	var want []IconField
	var ids []string
	var refs []*driver.DocRef
	for _, f := range fields {
		if p := shared.IconUploadId(f.Value); p != nil && *p != "" {
			want = append(want, f)
			ids = append(ids, *p)
			refs = append(refs, cloud.Col.Icons().Doc(*p))
		}
	}
	if len(want) == 0 {
		return nil
	}
	snaps, err := cloud.DB().GetAll(refs...)
	if err != nil {
		return err
	}
	for i, f := range want {
		if !snaps[i].Exists() {
			v.Err(f.Path, "V_REF", "icon "+ids[i]+" does not exist (upload it first)")
		}
	}
	return nil
}

// EmbedIcons is embedIcons: "upload:<id>" → "data:<contentType>;base64,…" (missing icon → nil).
func EmbedIcons(values []any) (map[string]any, error) {
	out := map[string]any{}
	seen := map[string]bool{}
	var refs []*driver.DocRef
	for _, v := range values {
		if p := shared.IconUploadId(v); p != nil && *p != "" && !seen[*p] {
			seen[*p] = true
			refs = append(refs, cloud.Col.Icons().Doc(*p))
		}
	}
	if len(refs) == 0 {
		return out, nil
	}
	snaps, err := cloud.DB().GetAll(refs...)
	if err != nil {
		return nil, err
	}
	for _, s := range snaps {
		k := "upload:" + s.ID()
		out[k] = nil
		if !s.Exists() {
			continue
		}
		d := s.Data()
		if b, ok := d.Val("_bytes").(doc.Bytes); ok {
			out[k] = "data:" + JSString(present(d, "contentType")) + ";base64," + base64.StdEncoding.EncodeToString(b)
		}
	}
	return out, nil
}
