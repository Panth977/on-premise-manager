package validate

// §21 #97 b: Go mirror of validateSiteDevTools (shared/src/validate.ts): dev-tool presets of one site.
// Vectors: shared/testdata/devtools-validate-vectors.json (devtools_test.go). Type errors the TS validator reports
// (V_TYPE for a non-list / non-object) cannot reach Go: such a config does not decode into the model.

import (
	"fmt"
	"regexp"
	"strings"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// DevToolSlugRe is TS DEVTOOL_SLUG_RE.
var DevToolSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// DevToolExprRoots is TS DEVTOOL_EXPR_ROOTS (= the roots of field devtool).
var DevToolExprRoots = []string{"var", "secret", "site", "self", "deployment", "containers"}

// DevToolExprField is TS DEVTOOL_EXPR_FIELD: the expression field of every dev-tool value resolved on the site
// (§21 #99: field devtool — the roots of files + containers.<c>.env.<NAME>).
const DevToolExprField = expr.FieldDevtool

// DevToolHeadersMax is TS DEVTOOL_HEADERS_MAX.
const DevToolHeadersMax = 50

var (
	pgSSLModes     = []string{"disable", "prefer", "require", "verify-full"}
	cqlConsistency = []string{"ANY", "ONE", "TWO", "THREE", "QUORUM", "ALL", "LOCAL_QUORUM", "EACH_QUORUM", "LOCAL_ONE"}
	httpSchemes    = []string{"none", "basic", "bearer", "api-key"}
	redisDBRe      = regexp.MustCompile(`^(?:[0-9]|1[0-5])$`)
	httpURLRe      = regexp.MustCompile(`(?i)^https?://\S`)
)

// CheckDevToolExpr is TS checkDevToolExpr = expr.CheckExpr(src, field devtool): syntax + roots + path shapes (at most one error).
func CheckDevToolExpr(src string) []*expr.Error {
	return expr.CheckExpr(src, DevToolExprField)
}

// ContainerEnvIsSecret is TS containerEnvIsSecret (§21 #99): env NAME of c holds a secret (secretRef, or a value using
// secret.* — also through container.env.<OTHER>). Image defaults cannot hold secrets. Unknown name → false.
func ContainerEnvIsSecret(c *model.Container, name string, seen map[string]bool) bool {
	if seen == nil {
		seen = map[string]bool{}
	}
	if seen[name] {
		return false
	}
	seen[name] = true
	for _, e := range c.Env {
		if e.Name != name {
			continue
		}
		if e.SecretRef != nil {
			return true
		}
		if e.Value == nil || !expr.IsExpr(*e.Value) {
			return false
		}
		refs, err := expr.ExprRefs(*e.Value)
		if err != nil {
			return false
		}
		for _, r := range refs {
			if strings.HasPrefix(r, "secret.") {
				return true
			}
			if strings.HasPrefix(r, "container.env.") && ContainerEnvIsSecret(c, strings.TrimPrefix(r, "container.env."), seen) {
				return true
			}
		}
		return false
	}
	return false
}

// ContainerEnvNames is TS containerEnvNames (§21 #99): container env + image env names; imageKnown false = the image
// is not in the catalog (its env names are unknown).
func ContainerEnvNames(c *model.Container, img *CatalogImage) (names []string, imageKnown bool) {
	for _, e := range c.Env {
		if !contains(names, e.Name) {
			names = append(names, e.Name)
		}
	}
	if img != nil {
		for _, e := range img.Env {
			if !contains(names, e.Name) {
				names = append(names, e.Name)
			}
		}
	}
	return names, img != nil
}

// IsSafeRelativePath is TS isSafeRelativePath.
func IsSafeRelativePath(p string) bool {
	if p == "" || utf16Len(p) > 1024 {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) || strings.Contains(p, "{{") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

func nonEmptyP(p *string) bool { return p != nil && strings.TrimSpace(*p) != "" }

// ValidateSiteDevTools checks SiteConfig.devTools of one site (V_DEVTOOL_*, E_* of the expressions).
func ValidateSiteDevTools(site *model.Site, cfg *model.Config, images map[string]CatalogImage, is *Issues) {
	if site.Config.DevTools == nil {
		return
	}
	list := *site.Config.DevTools
	lp := "sites[" + site.SiteSlug + "].config.devTools"
	if len(list) > model.DevToolPresetsMax {
		is.err(lp, "V_DEVTOOL_MAX", fmt.Sprintf("at most %d dev-tool presets per site (%d)", model.DevToolPresetsMax, len(list)))
	}
	var slugs []string
	for _, p := range list {
		slugs = append(slugs, p.PresetSlug)
	}
	for _, d := range dupes(slugs) {
		is.err(lp+"["+d+"]", "V_DEVTOOL_DUP", fmt.Sprintf("preset %s is defined twice on site %s", d, site.SiteSlug))
	}
	secrets, vars := map[string]bool{}, map[string]bool{}
	for _, s := range cfg.Secrets {
		secrets[s.SecretSlug] = true
	}
	for _, v := range cfg.Variables {
		vars[v.VariableSlug] = true
	}
	for i := range list {
		validateDevToolPreset(site, &list[i], lp+"["+list[i].PresetSlug+"]", images, secrets, vars, is)
	}
}

func validateDevToolPreset(site *model.Site, p *model.DevToolPreset, pp string, images map[string]CatalogImage, secrets, vars map[string]bool, is *Issues) {
	sc := &site.Config
	if !DevToolSlugRe.MatchString(p.PresetSlug) {
		is.err(pp+".presetSlug", "V_DEVTOOL_SLUG", fmt.Sprintf(`preset slug "%s" must match ^[a-z0-9][a-z0-9-]{0,62}$`, p.PresetSlug))
	}
	checkLabel(p.Name, pp+".name", is)
	checkDescription(p.Description, pp+".description", is)
	if !contains(model.DevToolKinds, p.Kind) {
		is.err(pp+".kind", "V_DEVTOOL_KIND", fmt.Sprintf(`kind "%s" must be one of %s`, p.Kind, strings.Join(model.DevToolKinds, ", ")))
		return
	}
	kind := p.Kind
	findContainer := func(slug string) *model.Container {
		for i := range sc.Containers {
			if sc.Containers[i].ContainerSlug == slug {
				return &sc.Containers[i]
			}
		}
		return nil
	}
	imageOf := func(c *model.Container) *CatalogImage {
		if img, ok := images[ImageKey(c.ServiceSlug, c.Version)]; ok {
			return &img
		}
		return nil
	}
	exprField := func(v *string, path string) bool {
		if v == nil || !expr.IsExpr(*v) {
			return true
		}
		errs := CheckDevToolExpr(*v)
		for _, e := range errs {
			is.err(path, e.Code, fmt.Sprintf("%s (at %d)", e.Message, e.Pos))
		}
		if len(errs) > 0 {
			return false
		}
		refs, _ := expr.ExprRefs(*v)
		for _, ref := range refs {
			parts := strings.Split(ref, ".")
			if len(parts) < 2 || parts[1] == "" {
				continue
			}
			if parts[0] == "secret" && !secrets[parts[1]] {
				is.err(path, "V_DEVTOOL_SECRET", fmt.Sprintf("secret %s does not exist", parts[1]))
			}
			if parts[0] == "var" && !vars[parts[1]] {
				is.err(path, "V_DEVTOOL_VAR", fmt.Sprintf("variable %s does not exist", parts[1]))
			}
			if parts[0] == "containers" {
				// §21 #99: containers.<c>.* names a container of this site; .env.<NAME> an env of its image or the container
				c := findContainer(parts[1])
				if c == nil {
					is.err(path, "V_DEVTOOL_CONTAINER", fmt.Sprintf("container %s (in {{ %s }}) does not exist on site %s", parts[1], ref, site.SiteSlug))
				} else if len(parts) >= 4 && parts[2] == "env" && parts[3] != "" {
					names, known := ContainerEnvNames(c, imageOf(c))
					if !contains(names, parts[3]) && known {
						list := strings.Join(names, ", ")
						if list == "" {
							list = "none"
						}
						is.err(path, "V_DEVTOOL_ENV", fmt.Sprintf("container %s has no env var %s (its env: %s)", parts[1], parts[3], list))
					}
				}
			}
		}
		return true
	}
	// §21 #99 b: a password / token from a container env that holds a plain value → warning
	plainEnv := func(src, path, f string) {
		refs, err := expr.ExprRefs(src)
		if err != nil {
			return
		}
		for _, ref := range refs {
			parts := strings.Split(ref, ".")
			if len(parts) < 4 || parts[0] != "containers" || parts[2] != "env" || parts[1] == "" || parts[3] == "" {
				continue
			}
			c := findContainer(parts[1])
			if c == nil {
				continue
			}
			if names, _ := ContainerEnvNames(c, imageOf(c)); !contains(names, parts[3]) {
				continue
			}
			if !ContainerEnvIsSecret(c, parts[3], nil) {
				is.Warn(path, "V_DEVTOOL_PLAIN_ENV", fmt.Sprintf("env %s of container %s holds a plain value, not a secret: the %s is readable in the config — set that env from a secret (secretRef or {{ secret.x }})", parts[3], parts[1], f))
			}
		}
	}
	// target
	t := p.Target
	tp := pp + ".target"
	allowed := model.DevToolTargets[kind]
	containerOf := func(slug *string, at string) *model.Container {
		if !nonEmptyP(slug) {
			is.err(at+".containerSlug", "V_DEVTOOL_TARGET", "pick the container of this site")
			return nil
		}
		for i := range sc.Containers {
			if sc.Containers[i].ContainerSlug == *slug {
				c := &sc.Containers[i]
				if !c.Enabled {
					is.Warn(at+".containerSlug", "V_DEVTOOL_CONTAINER", fmt.Sprintf("container %s is disabled: the preset cannot connect until it runs", *slug))
				}
				return c
			}
		}
		is.err(at+".containerSlug", "V_DEVTOOL_CONTAINER", fmt.Sprintf("container %s does not exist on site %s", *slug, site.SiteSlug))
		return nil
	}
	switch {
	case !contains(allowed, t.Kind):
		is.err(tp, "V_DEVTOOL_TARGET", fmt.Sprintf("a %s preset target must be one of %s", kind, strings.Join(allowed, ", ")))
	case t.Kind == "container":
		c := containerOf(t.ContainerSlug, tp)
		if !nonEmptyP(t.PortName) {
			is.err(tp+".portName", "V_DEVTOOL_TARGET", "pick the port of the container")
		} else if c != nil {
			if img, ok := images[ImageKey(c.ServiceSlug, c.Version)]; ok {
				found := false
				var names []string
				for _, ep := range ContainerPorts(c, img.Ports) {
					if ep.PortName == *t.PortName {
						found = true
					}
				}
				for _, ip := range img.Ports {
					names = append(names, ip.Name)
				}
				if !found {
					ns := strings.Join(names, ", ")
					if ns == "" {
						ns = "no ports"
					}
					is.err(tp+".portName", "V_DEVTOOL_PORT", fmt.Sprintf("port %s is not a port of the image of %s (%s)", *t.PortName, c.ContainerSlug, ns))
				}
			}
		}
	case t.Kind == "host":
		h := model.Str(t.Host)
		if !nonEmptyP(t.Host) || strings.Contains(h, "{{") || utf16Len(h) > 253 || !(IsIPLiteral(h) || (RE.DNSName.MatchString(h) && !strings.HasPrefix(h, "*"))) {
			is.err(tp+".host", "V_DEVTOOL_TARGET", fmt.Sprintf(`host "%s" must be an IP or a DNS name (no {{ }})`, h))
		}
		if t.Port == nil || !isIntF(*t.Port) || *t.Port < 1 || *t.Port > 65535 {
			is.err(tp+".port", "V_DEVTOOL_TARGET", "port must be an integer 1–65535")
		}
	case t.Kind == "url":
		if t.URL == nil || !httpURLRe.MatchString(*t.URL) || utf16Len(*t.URL) > Limits.Value {
			is.err(tp+".url", "V_DEVTOOL_TARGET", "url must be an absolute http:// or https:// URL (≤ 4096 characters; may hold {{ var.x }})")
		} else {
			exprField(t.URL, tp+".url")
		}
	case t.Kind == "volume-file":
		if !nonEmptyP(t.VolumeSlug) {
			is.err(tp+".volumeSlug", "V_DEVTOOL_TARGET", "pick a declared volume of the site")
		} else {
			var v *model.SiteVolume
			for _, x := range sc.VolumeList() {
				if x.VolumeSlug == *t.VolumeSlug {
					x := x
					v = &x
				}
			}
			if v == nil {
				is.err(tp+".volumeSlug", "V_DEVTOOL_VOLUME", fmt.Sprintf("volume %s is not declared on site %s", *t.VolumeSlug, site.SiteSlug))
			} else if v.VolumeKind() == "file" {
				is.err(tp+".volumeSlug", "V_DEVTOOL_VOLUME", fmt.Sprintf("volume %s is a single-file volume: SQLite needs a folder volume (journal / WAL files live next to the database)", *t.VolumeSlug))
			}
		}
		if t.Path == nil || !IsSafeRelativePath(*t.Path) {
			is.err(tp+".path", "V_DEVTOOL_VOLUME", fmt.Sprintf(`path "%s" must be a relative path inside the volume (no leading "/", no "..")`, model.Str(t.Path)))
		}
	case t.Kind == "container-file":
		containerOf(t.ContainerSlug, tp)
		if t.Path == nil || !IsSafeMountPath(*t.Path) || strings.Contains(*t.Path, "{{") {
			is.err(tp+".path", "V_DEVTOOL_VOLUME", fmt.Sprintf(`path "%s" must be an absolute path in the container (not "/", no spaces, no "..")`, model.Str(t.Path)))
		}
	}
	// auth
	a := p.Auth
	ap := pp + ".auth"
	exprField(a.Username, ap+".username")
	exprField(a.Database, ap+".database")
	exprField(a.ClientID, ap+".clientId")
	exprField(a.APIKeyHeader, ap+".apiKeyHeader")
	cred := func(c model.DevToolCredential, f string) string {
		at := ap + "." + f
		switch c.Kind {
		case "", "none":
			return "none"
		case "literal":
			// §21 #103 (supersedes #97 b "never a literal"): a static value is allowed, with a non-blocking warning
			switch {
			case c.Value == nil || *c.Value == "":
				is.err(at, "V_DEVTOOL_AUTH", fmt.Sprintf("%s value must be a non-empty string (pick None for no %s)", f, f))
			case utf16Len(*c.Value) > Limits.Value:
				is.err(at, "V_DEVTOOL_AUTH", fmt.Sprintf("%s value must be at most %d characters", f, Limits.Value))
			default:
				is.Warn(at, "V_DEVTOOL_LITERAL", plainText(f))
			}
			return "literal"
		case "expr":
		default:
			is.err(at, "V_DEVTOOL_AUTH", f+".kind must be none, literal or expr")
			return ""
		}
		if c.Expr == nil || strings.TrimSpace(*c.Expr) == "" {
			is.err(at, "V_DEVTOOL_AUTH", f+".expr must be a non-empty expression")
			return "expr"
		}
		if !expr.IsExpr(*c.Expr) {
			is.Warn(at, "V_DEVTOOL_LITERAL", plainText(f)+" (an expression without {{ }} is a static value)")
			return "expr"
		}
		if exprField(c.Expr, at) {
			if refs, _ := expr.ExprRefs(*c.Expr); len(refs) == 0 {
				is.Warn(at, "V_DEVTOOL_LITERAL", plainText(f)+" (the expression references no secret or variable)")
			} else {
				plainEnv(*c.Expr, at, f)
			}
		}
		return "expr"
	}
	pw := cred(a.Password, "password")
	tk := cred(a.Token, "token")
	scheme := a.HTTPScheme
	if scheme == "" {
		scheme = "none"
	}
	if !contains(httpSchemes, scheme) {
		is.err(ap+".httpScheme", "V_DEVTOOL_AUTH", "httpScheme must be one of "+strings.Join(httpSchemes, ", "))
	}
	switch kind {
	case "postgres":
		if !nonEmptyP(a.Username) {
			is.err(ap+".username", "V_DEVTOOL_AUTH", "PostgreSQL needs a username")
		}
	case "cassandra":
		if nonEmptyP(a.Username) != (pw != "none" && pw != "") {
			is.err(ap, "V_DEVTOOL_AUTH", "Cassandra needs both username and password, or neither")
		}
	case "redis":
		if a.Database != nil && !redisDBRe.MatchString(*a.Database) {
			is.err(ap+".database", "V_DEVTOOL_AUTH", fmt.Sprintf(`Redis database must be "0"–"15" or null (got "%s")`, *a.Database))
		}
	case "http":
		if scheme == "basic" && !nonEmptyP(a.Username) {
			is.err(ap+".username", "V_DEVTOOL_AUTH", "basic auth needs a username")
		}
		if (scheme == "bearer" || scheme == "api-key") && tk == "none" {
			is.err(ap+".token", "V_DEVTOOL_AUTH", scheme+" auth needs a token (an expression such as {{ secret.api-token }})")
		}
		if scheme == "api-key" && a.APIKeyHeader != nil && !(RE.HeaderName.MatchString(*a.APIKeyHeader) || expr.IsExpr(*a.APIKeyHeader)) {
			is.err(ap+".apiKeyHeader", "V_DEVTOOL_AUTH", fmt.Sprintf(`"%s" is not a valid header name`, *a.APIKeyHeader))
		}
	case "mqtt":
		if a.ClientID != nil && len(*a.ClientID) > 65535 {
			is.err(ap+".clientId", "V_DEVTOOL_AUTH", "clientId must be at most 65535 bytes")
		}
	}
	if kind != "http" && (scheme != "none" || tk != "none" || a.APIKeyHeader != nil) {
		is.Warn(ap, "V_DEVTOOL_AUTH", "httpScheme / token / apiKeyHeader are only used by HTTP presets (ignored for "+kind+")")
	}
	if kind != "mqtt" && a.ClientID != nil {
		is.Warn(ap+".clientId", "V_DEVTOOL_AUTH", "clientId is only used by MQTT presets (ignored for "+kind+")")
	}
	if kind == "sqlite" && (a.Username != nil || pw != "none" || a.Database != nil) {
		is.Warn(ap, "V_DEVTOOL_AUTH", "SQLite has no username / password / database (ignored)")
	}
	// options
	o := p.Options
	op := pp + ".options"
	bad := func(f, msg string) { is.err(op+"."+f, "V_DEVTOOL_OPTION", msg) }
	intIn := func(f string, v *float64, lo, hi float64) {
		if v != nil && (!isIntF(*v) || *v < lo || *v > hi) {
			bad(f, fmt.Sprintf("%s must be an integer %s–%s or null (got %s)", f, numText(lo), numText(hi), numText(*v)))
		}
	}
	if o.SSLMode != nil && !contains(pgSSLModes, *o.SSLMode) {
		bad("sslMode", "sslMode must be one of "+strings.Join(pgSSLModes, ", ")+" or null")
	}
	if o.Consistency != nil && !contains(cqlConsistency, *o.Consistency) {
		bad("consistency", "consistency must be one of "+strings.Join(cqlConsistency, ", ")+" or null")
	}
	if o.Scheme != nil && *o.Scheme != "http" && *o.Scheme != "https" {
		bad("scheme", "scheme must be http, https or null")
	}
	intIn("timeoutSec", o.TimeoutSec, 1, 3600)
	intIn("maxRows", o.MaxRows, 1, 10000)
	intIn("keepAliveSec", o.KeepAliveSec, 0, 65535)
	if o.BasePath != nil && !strings.HasPrefix(*o.BasePath, "/") {
		bad("basePath", fmt.Sprintf(`basePath must start with "/" (got "%s")`, *o.BasePath))
	}
	if len(o.DefaultHeaders) > DevToolHeadersMax {
		bad("defaultHeaders", fmt.Sprintf("at most %d default headers", DevToolHeadersMax))
	}
	for i, h := range o.DefaultHeaders {
		hp := fmt.Sprintf("defaultHeaders[%d]", i)
		if !RE.HeaderName.MatchString(h.Name) {
			bad(hp+".name", fmt.Sprintf(`"%s" is not a valid header name`, h.Name))
		}
		if utf16Len(h.Value) > Limits.Value {
			bad(hp+".value", fmt.Sprintf("header value must be a string of at most %d characters", Limits.Value))
		} else {
			v := h.Value
			exprField(&v, op+"."+hp+".value")
		}
	}
}

// plainText is the §21 #103 warning of a static dev-tool credential (V_DEVTOOL_LITERAL).
func plainText(f string) string {
	return "the " + f + " is stored in plain text in the config — anyone who can read the config sees it; use a secret for production"
}
