package cloud

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/sign"
)

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func eq(t testing.TB, got, want any, msg ...any) {
	t.Helper()
	if doc.MarshalString(got) != doc.MarshalString(want) {
		t.Fatalf("%v\n got %s\nwant %s", msg, doc.MarshalString(got), doc.MarshalString(want))
	}
}

// sharedDir is shared/, the TypeScript the frontend uses: the vectors hold the Go port against it.
func sharedDir(t testing.TB) string {
	_, f, _, _ := runtime.Caller(0)
	d := filepath.Join(filepath.Dir(f), "../../../shared")
	if _, err := os.Stat(filepath.Join(d, "node_modules/tsx")); err != nil {
		t.Skip("shared/node_modules missing (cd shared && npm ci)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	return d
}

func tsx(t testing.TB, script string, args ...string) []byte {
	t.Helper()
	_, f, _, _ := runtime.Caller(0)
	cmd := exec.Command("node", append([]string{"--import", "tsx", "--no-warnings=ExperimentalWarning", filepath.Join(filepath.Dir(f), script)}, args...)...)
	cmd.Dir = sharedDir(t)
	var out, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &e
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", script, err, e.String())
	}
	return out.Bytes()
}

// The checked-in table is what the generator gives for shared/src today.
func TestGeneratedTableFresh(t *testing.T) {
	want := tsx(t, "gen/gen.mts", "--stdout")
	f := exec.Command("gofmt")
	f.Stdin = bytes.NewReader(want)
	formatted, err := f.Output()
	must(t, err)
	got, err := os.ReadFile("api_gen.go")
	must(t, err)
	if !bytes.Equal(got, formatted) {
		t.Fatal("api_gen.go is stale: cd shared && node --import tsx ../agent/internal/cloud/gen/gen.mts && gofmt -w ../agent/internal/cloud/api_gen.go")
	}
}

// Pure logic against the TS implementation: audit diff + redaction, access rules, slugs.
func TestVectors(t *testing.T) {
	v, err := doc.ParseMap(tsx(t, "testdata/vectors.mts"))
	must(t, err)
	each := func(k string, fn func(c *doc.Map)) {
		for _, x := range v.Arr(k) {
			fn(x.(*doc.Map))
		}
	}
	each("diffs", func(c *doc.Map) {
		r := AuditDiff(c.Val("a"), c.Val("b"))
		eq(t, doc.Obj("changes", r.Changes, "total", r.Total), c.Val("out"), c.Val("a"))
	})
	each("redact", func(c *doc.Map) { eq(t, RedactSecrets(c.Val("v")), c.Val("out")) })
	each("cap", func(c *doc.Map) { eq(t, CapAuditValue(c.Val("v"), AuditMaxValueBytes), c.Val("out")) })
	each("keys", func(c *doc.Map) { eq(t, AuditActionKeys(c.Str("v")), c.Val("out")) })
	each("checks", func(c *doc.Map) {
		a := c.Map("acc")
		acc := &Account{UID: a.Str("uid"), Disabled: a.Bool("disabled"), Access: ParseAccessRules(a.Arr("access"))}
		var tg *AccessTarget
		if m := c.Map("t"); m != nil {
			tg = &AccessTarget{m.Str("deploymentId"), m.Strs("tagSlugs"), m.Strs("managerAccountUids")}
		}
		o := Can(acc, c.Str("r"), c.Str("a"), tg)
		var idx any
		if o.RuleIndex >= 0 {
			idx = o.RuleIndex
		}
		eq(t, doc.Obj("allowed", o.Allowed, "reason", o.Reason, "ruleIndex", idx), c.Val("out"))
		if tg != nil {
			eq(t, CanSeeDeployment(acc, tg), c.Val("see"))
		}
		eq(t, IsAdmin(acc), c.Val("admin"))
	})
	each("rules", func(c *doc.Map) {
		eq(t, ValidateAccessRules(c.Val("v"), []string{"a"}, []string{"D1"}), c.Val("out"), c.Val("v"))
	})
	eq(t, FullAdminRules(), v.Val("admin"))
	each("slugs", func(c *doc.Map) {
		eq(t, SlugifyLabel(c.Str("v"), "t-"), c.Val("t"), c.Val("v"))
		eq(t, SlugifyLabel(c.Str("v"), "d-"), c.Val("d"), c.Val("v"))
	})
}

type env struct {
	p   *driver.Ports
	srv *httptest.Server
}

func setup(t *testing.T) *env {
	t.Setenv("OPM_FRONTEND_ORIGINS", "http://localhost:5173")
	p, err := driver.Open(driver.Config{DataDir: t.TempDir(), PublicURL: "http://x.test", Strict: boolp(true)})
	must(t, err)
	t.Cleanup(func() { p.Close() })
	SetPorts(p)
	srv := httptest.NewServer(NewHandler(&CloudConfig{BodyLimit: 1 << 20, FrontendOrigins: []string{"http://localhost:5173"}, ConsoleURL: "http://x.test"}, p))
	t.Cleanup(srv.Close)
	return &env{p, srv}
}
func boolp(b bool) *bool { return &b }

func (e *env) person(t *testing.T, access []any) (uid, bearer string) {
	email := strings.ToLower(driver.RandomFrom("abcdefgh", 8)) + "@opm.test"
	u, err := e.p.Auth.CreateUser(driver.CreateUserInput{Email: email, Password: driver.Ptr("pw-123456")})
	must(t, err)
	must(t, Col.Accounts().Doc(u.UID).Set(doc.Obj("uid", u.UID, "img", nil, "name", "N", "email", email, "phone", nil, "access", access, "disabled", false, "createdAt", doc.NowISO(), "createdBy", "bootstrap", "lastLoginAt", nil, "lastSetPassword", nil, "lastResetLink", nil)))
	d, err := e.p.Auth.UserByEmail(email)
	must(t, err)
	p, err := e.p.Auth.Issue(d, "")
	must(t, err)
	return u.UID, "Bearer " + p.IDToken
}

func (e *env) do(t *testing.T, method, path string, body string, hdr ...string) (*http.Response, *doc.Map) {
	t.Helper()
	r, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	must(t, err)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rs, err := http.DefaultClient.Do(r)
	must(t, err)
	defer rs.Body.Close()
	raw, _ := io.ReadAll(rs.Body)
	m, _ := doc.ParseMap(raw)
	return rs, m
}

func init() {
	Register("patchTag", func(c *Ctx) (any, error) {
		b, err := c.BodyMap()
		if err != nil {
			return nil, err
		}
		ref := Col.Tags().Doc(c.Params["tagSlug"])
		if err := ref.Set(doc.Obj("label", b.Val("label"), "password", "hunter2")); err != nil {
			return nil, err
		}
		c.Audit = &AuditHint{Data: doc.Obj("note", "n", "secret", "s")}
		return doc.Obj("z", 1, "a", 2), nil
	})
	Register("deleteTag", func(c *Ctx) (any, error) { return nil, E.Conflict("in use", doc.Obj("usedBy", 3)) })
	Register("revealRegistryPassword", func(c *Ctx) (any, error) { return doc.Obj("password", "x"), nil })
	Register("getNotFoundPage", func(c *Ctx) (any, error) { panic("boom") })
	Register("listTags", func(c *Ctx) (any, error) {
		return &Reply{202, map[string]string{"Content-Type": "text/x-t", "X-Thing": "1"}, strings.NewReader("raw " + c.Query.Get("q") + " " + c.Mount)}, nil
	})
}

func TestRouter(t *testing.T) {
	e := setup(t)
	uid, bearer := e.person(t, FullAdminRules())
	r, j := e.do(t, "GET", "/v1/definitely-not-here", "")
	eq(t, r.StatusCode, 404)
	eq(t, j.Map("error").Str("code"), "not-found")
	if r.Header["X-Request-Id"] == nil || r.Header.Get("X-Opm-Server-Time") == "" {
		t.Fatal(r.Header)
	}
	r, j = e.do(t, "PATCH", "/v1/not-found-pages/abc", "")
	eq(t, r.StatusCode, 405)
	eq(t, j.Map("error").Str("code"), "method-not-allowed")

	// unported op: 501 + op, after auth
	r, _ = e.do(t, "GET", "/v1/registries", "")
	eq(t, r.StatusCode, 401)
	r, j = e.do(t, "GET", "/v1/registries", "", "Authorization", bearer)
	eq(t, r.StatusCode, 501)
	eq(t, j.Map("error").Map("details").Str("op"), "listRegistries")
	if len(Unimplemented()) == 0 || has(Unimplemented(), "patchTag") {
		t.Fatal("unimplemented list")
	}

	// a Reply, a mount prefix, lenient query
	r, _ = e.do(t, "GET", "/mnt/x/v1/tags?q=a%20b+c", "", "Authorization", bearer)
	eq(t, r.StatusCode, 202)
	eq(t, r.Header.Get("X-Thing"), "1")
	eq(t, r.Header.Get("Content-Type"), "text/x-t")

	// panic → the 500 envelope with the requestId
	r, j = e.do(t, "GET", "/v1/not-found-pages/x", "", "Authorization", bearer)
	eq(t, r.StatusCode, 500)
	eq(t, j.Map("error").Str("code"), "internal")
	eq(t, j.Map("error").Map("details").Str("requestId"), r.Header.Get("X-Request-Id"))

	// bad path parameter
	r, j = e.do(t, "GET", "/v1/not-found-pages/%FF", "", "Authorization", bearer)
	eq(t, r.StatusCode, 422)
	eq(t, j.Map("error").Map("details").Arr("errors")[0].(*doc.Map).Str("code"), "V_FORMAT")

	// audited success: JSON key order, diff, redaction
	r, _ = e.do(t, "PATCH", "/v1/tags/t1", `{"label":"L"}`, "Authorization", bearer, "User-Agent", "ua")
	eq(t, r.StatusCode, 200)
	q, err := Col.AuditLog().Where("op", "==", "patchTag").Get()
	must(t, err)
	eq(t, q.Size(), 1)
	a := DecodeAudit(q.Docs[0].Data())
	eq(t, a.Keys(), []string{"auditId", "ts", "op", "actor", "actorEmail", "action", "outcome", "status", "errorCode", "errorMessage", "targetType", "target", "requestId", "ip", "userAgent", "changes", "changesTotal", "data"})
	eq(t, a.Str("actor"), uid)
	eq(t, a.Str("target"), "tags/t1")
	eq(t, a.Str("userAgent"), "ua")
	eq(t, a.Val("data"), doc.Obj("note", "n", "secret", "***"))
	eq(t, a.Val("changes"), []any{doc.Obj("path", "label", "before", nil, "after", "L"), doc.Obj("path", "password", "before", nil, "after", "***", "secret", true)})
	raw := q.Docs[0].Data()
	eq(t, raw.Val("_actionKeys"), AuditActionKeys(a.Str("action")))

	// audited failure
	r, j = e.do(t, "DELETE", "/v1/tags/t1", "", "Authorization", bearer)
	eq(t, r.StatusCode, 409)
	eq(t, j.Map("error").Map("details"), doc.Obj("usedBy", 3))
	q, _ = Col.AuditLog().Where("op", "==", "deleteTag").Get()
	a = DecodeAudit(q.Docs[0].Data())
	eq(t, []any{a.Val("outcome"), a.Val("status"), a.Val("errorCode"), a.Val("errorMessage")}, []any{"error", 409, "conflict", "in use"})

	// 401: no actor, no entry; 403 no account: entry with the actor
	r, _ = e.do(t, "PATCH", "/v1/tags/t2", `{}`)
	eq(t, r.StatusCode, 401)
	u, err := e.p.Auth.CreateUser(driver.CreateUserInput{Email: "noacc@opm.test", Password: driver.Ptr("pw-123456")})
	must(t, err)
	d, _ := e.p.Auth.UserByEmail("noacc@opm.test")
	pr, _ := e.p.Auth.Issue(d, "")
	r, _ = e.do(t, "PATCH", "/v1/tags/t2", `{}`, "Authorization", "Bearer "+pr.IDToken)
	eq(t, r.StatusCode, 403)
	q, _ = Col.AuditLog().Where("actor", "==", u.UID).Get()
	eq(t, q.Size(), 1)

	// bad JSON
	r, j = e.do(t, "PATCH", "/v1/tags/t1", `{`, "Authorization", bearer)
	eq(t, r.StatusCode, 422)
	eq(t, j.Map("error").Str("message"), "body is not valid JSON")

	// body limit
	r, j = e.do(t, "PATCH", "/v1/tags/t1", strings.Repeat("x", 2<<20), "Authorization", bearer)
	eq(t, r.StatusCode, 413)
	eq(t, j.Map("error").Str("code"), "too-large")
}

func TestCredentialRate(t *testing.T) {
	e := setup(t)
	t.Setenv("OPM_REVEAL_RATE", "2")
	_, bearer := e.person(t, FullAdminRules())
	for i := 0; i < 2; i++ {
		r, _ := e.do(t, "POST", "/v1/registries/r1/reveal-password", "", "Authorization", bearer)
		eq(t, r.StatusCode, 200)
		eq(t, r.Header.Get("Cache-Control"), "no-store")
		eq(t, r.Header.Get("Pragma"), "no-cache")
	}
	r, j := e.do(t, "POST", "/v1/registries/r1/reveal-password", "", "Authorization", bearer)
	eq(t, r.StatusCode, 429)
	eq(t, j.Map("error").Str("code"), "rate-limited")
	eq(t, r.Header.Get("Cache-Control"), "no-store")
	if r.Header.Get("Retry-After") == "" {
		t.Fatal("Retry-After")
	}
}

func TestRateLimitBucket(t *testing.T) {
	var err error
	for i := 0; i < 500 && err == nil; i++ {
		err = rateLimit("u:test-bucket")
	}
	var ae *ApiError
	if !errors.As(err, &ae) || ae.Code != "rate-limited" || ae.Status() != 429 || ae.Headers["Retry-After"] == "" {
		t.Fatal(err)
	}
}

func signedHeaders(pk ed25519.PrivateKey, site, method, path string, body []byte) map[string]string {
	h := sign.SignRequest(pk, site, method, path, body, time.Now().Unix())
	o := map[string]string{}
	for k, v := range h {
		o[strings.ToLower(k)] = v
	}
	return o
}

func TestAgentSignature(t *testing.T) {
	setup(t)
	pub, pk, err := ed25519.GenerateKey(nil)
	must(t, err)
	must(t, Col.Deployments().Doc("D1").Set(doc.Obj("deploymentId", "D1", "deleted", false, "slug", "d", "relayVia", nil)))
	must(t, Col.Agents("D1").Doc("site-a").Set(doc.Obj("publicKey", sign.PublicKeyB64(pub), "enrolledAt", "x", "relayVia", "other")))
	path := "/v1/agent/poll?x=1"
	body := []byte("{}")
	h := signedHeaders(pk, "D1/site-a", "POST", path, body)
	a, err := VerifyAgent(SignedParts{"POST", path, h, body}, false)
	must(t, err)
	eq(t, a.SiteSlug, "site-a")
	eq(t, a.Dep.Str("slug"), "d")
	reason := func(err error) string {
		var ae *ApiError
		if !errors.As(err, &ae) {
			t.Fatal(err)
		}
		return ae.Details.Str("reason")
	}
	_, err = VerifyAgent(SignedParts{"POST", path, h, body}, false)
	eq(t, reason(err), "nonce") // replay
	h2 := signedHeaders(pk, "D1/site-a", "POST", path, body)
	_, err = VerifyAgent(SignedParts{"POST", path, h2, []byte("x")}, false)
	eq(t, reason(err), "signature")
	_, err = VerifyAgent(SignedParts{"POST", path, h2, body}, false)
	must(t, err) // a bad signature did not burn the nonce
	h3 := signedHeaders(pk, "D1/nope-site", "POST", path, body)
	_, err = VerifyAgent(SignedParts{"POST", path, h3, body}, false)
	eq(t, reason(err), "unknown-site")
	h4 := signedHeaders(pk, "D1/site-a", "POST", path, body)
	h4["x-opm-ts"] = strconv.FormatInt(time.Now().Unix()-3600, 10)
	_, err = VerifyAgent(SignedParts{"POST", path, h4, body}, false)
	eq(t, reason(err), "clock")
	_, err = VerifyAgent(SignedParts{"POST", path, map[string]string{}, body}, false)
	eq(t, reason(err), "signature")
	must(t, Col.Deployments().Doc("D1").Update(doc.Obj("deleted", true)))
	h5 := signedHeaders(pk, "D1/site-a", "POST", path, body)
	_, err = VerifyAgent(SignedParts{"POST", path, h5, body}, false)
	eq(t, reason(err), "unknown-site")
	_, err = VerifyAgent(SignedParts{"POST", path, h5, body}, true)
	must(t, err)
}

func TestInstallToken(t *testing.T) {
	setup(t)
	mk := func(id, tok string, m *doc.Map) string {
		must(t, Col.InstallTokens("D1").Doc(id).Set(m.Set("tokenHash", Sha256Hex([]byte(tok)))))
		return "Bearer " + tok
	}
	must(t, Col.Deployments().Doc("D1").Set(doc.Obj("deleted", false)))
	ok := mk("a", "opi_good", doc.Obj("siteSlug", "s1", "expiresAt", doc.IsoPlus(60_000)))
	a, err := VerifyInstallToken(ok)
	must(t, err)
	eq(t, []string{a.DeploymentID, a.Mode, a.Kind}, []string{"D1", "install", "install"})
	_, err = VerifyInstallToken("Bearer opi_nope")
	var ae *ApiError
	if !errors.As(err, &ae) || ae.Status() != 401 {
		t.Fatal(err)
	}
	exp := mk("b", "opi_exp", doc.Obj("siteSlug", "s1", "expiresAt", doc.IsoPlus(-1000)))
	_, err = VerifyInstallToken(exp)
	if !errors.As(err, &ae) || ae.Status() != 410 || ae.Details.Str("reason") != "expired" {
		t.Fatal(err)
	}
	must(t, Col.Agents("D1").Doc("s1").Set(doc.Obj("enrolledAt", "T1")))
	used := mk("c", "opi_used", doc.Obj("siteSlug", "s1", "usedAt", "T1"))
	a, err = VerifyInstallToken(used)
	must(t, err)
	eq(t, a.Mode, "upgrade")
	mt := mk("d", "opi_maint", doc.Obj("siteSlug", "s1", "kind", "maintenance", "boundEnrolledAt", "T1"))
	a, err = VerifyInstallToken(mt)
	must(t, err)
	eq(t, []string{a.Mode, a.Kind}, []string{"upgrade", "maintenance"})
	must(t, Col.Agents("D1").Doc("s1").Update(doc.Obj("enrolledAt", "T2")))
	_, err = VerifyInstallToken(mt)
	if !errors.As(err, &ae) || ae.Details.Str("reason") != "enrollment-ended" {
		t.Fatal(err)
	}
}

func TestCodec(t *testing.T) {
	stored := doc.Obj("deploymentId", "D1", "slug", "s", "_draft", `{"variables":[1],"secrets":[],"files":[],"sites":[]}`, "srcTemplate", nil, "_srcSnapshot", nil, "_srcBase", nil, "_srcSkipped", nil, "x", 1)
	d := DecodeDeployment(stored)
	eq(t, d.Keys(), []string{"deploymentId", "slug", "x", "draft", "srcTemplate", "_cloudLastHash"})
	back := EncodeDeployment(d)
	eq(t, back.Keys(), []string{"deploymentId", "slug", "x", "_cloudLastHash", "_draft", "srcTemplate", "_srcSnapshot", "_srcBase", "_srcSkipped"})
	eq(t, back.Val("_draft"), stored.Val("_draft"))
	a := doc.Obj("ts", "2026-01-01T00:00:00.000Z", "type", "update.applied", "data", doc.Obj("x", 1))
	en := EncodeActivity(a)
	eq(t, en.Val("_typeKeys"), []any{"update.", "update.applied"})
	eq(t, en.Val("_tsMs"), 1767225600000)
	eq(t, DecodeActivity(en), a.Without("data").Set("data", doc.Obj("x", 1)))
}

func TestStaticAndHealth(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	must(t, os.MkdirAll(dir+"/_app/immutable", 0o777))
	must(t, os.WriteFile(dir+"/index.html", []byte("<html>"), 0o644))
	must(t, os.WriteFile(dir+"/_app/immutable/a.js", []byte("js"), 0o644))
	must(t, os.WriteFile(dir+"/x.unknown", []byte("u"), 0o644))
	srv := httptest.NewServer(NewHandler(&CloudConfig{StaticDir: dir, FrontendOrigins: []string{}, BodyLimit: 1 << 20, ConsoleURL: "http://c"}, e.p))
	defer srv.Close()
	get := func(p string) (*http.Response, string) {
		r, err := http.Get(srv.URL + p)
		must(t, err)
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r, string(b)
	}
	r, b := get("/")
	eq(t, b, "<html>")
	eq(t, r.Header.Get("Content-Type"), "text/html; charset=utf-8")
	eq(t, r.Header.Get("Cache-Control"), "no-cache")
	_, b = get("/some/spa/route?x=1")
	eq(t, b, "<html>")
	r, b = get("/_app/immutable/a.js")
	eq(t, b, "js")
	eq(t, r.Header.Get("Cache-Control"), "public, max-age=31536000, immutable")
	r, _ = get("/x.unknown")
	eq(t, r.Header.Get("Content-Type"), "application/octet-stream")
	r, b = get("/..%2f..%2fetc%2fpasswd")
	eq(t, []any{r.StatusCode, b}, []any{404, "not found"})
	r, b = get("/healthz")
	eq(t, b, `{"ok":true,"adapter":"local","version":"dev"}`)
	r, b = get("/opm-config.json")
	eq(t, b, `{"auth":{"provider":"local","passwordReset":false,"oidc":null}}`)
	rs, err := http.Post(srv.URL+"/nope", "text/plain", nil)
	must(t, err)
	eq(t, rs.StatusCode, 404)
}

func TestConfig(t *testing.T) {
	t.Setenv("OPM_ADAPTER", "firebase")
	t.Setenv("OPM_PUBLIC_URL", "https://opm.example/")
	t.Setenv("OPM_CONSOLE_URL", "")
	t.Setenv("OPM_DATA_DIR", t.TempDir())
	t.Setenv("OPM_FRONTEND_ORIGINS", "http://a, http://b")
	t.Setenv("OPM_OIDC_ISSUER", "https://accounts.google.com")
	t.Setenv("OPM_OIDC_CLIENT_ID", "c")
	t.Setenv("OPM_OIDC_CLIENT_SECRET", "s")
	f := filepath.Join(t.TempDir(), "c.json")
	must(t, os.WriteFile(f, []byte(`{"port":"9000","runJobs":false,"frontendOrigins":["http://c"]}`), 0o644))
	c, err := LoadConfig([]string{"opm-cloud", "--config", f})
	must(t, err)
	eq(t, c.PublicURL, "https://opm.example")
	eq(t, c.ConsoleURL, "https://opm.example")
	eq(t, c.Port, 9000)
	eq(t, c.RunJobs, false)
	eq(t, c.BlobRedirect, true)
	eq(t, c.FrontendOrigins, []string{"https://opm.example", "http://a", "http://b", "http://c"})
	eq(t, c.Oidc.Label, "Google")
	eq(t, c.Oidc.RedirectURI, "https://opm.example/auth/oidc/callback")
}
