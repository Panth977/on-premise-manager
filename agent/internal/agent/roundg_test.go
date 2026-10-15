package agent

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/validate"
)

// Round G (SPEC §21 #84 e): /_opm/api/secrets/{slug}/reveal → hideAfterSec 60, Cache-Control no-store on success and
// errors, 30 reveals per session per fixed minute → 429 rate-limited {retryAfterSec} + Retry-After, activity
// secret.revealed without the value.
func TestRevealSecretRoundG(t *testing.T) {
	a := newTestAgent(t)
	now := time.Date(2026, 10, 8, 10, 0, 10, 0, time.UTC) // 10 s into a fixed minute
	a.clock = func() time.Time { return now }
	h := a.handlerFor("gui-alias")
	const secret = "s3cr3t-VALUE-never-logged"

	cfg := validate.EmptyConfig()
	cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: "app", Label: "app", Config: validate.DefaultSiteConfig(ptr("127.0.0.1"))})
	cfg.Secrets = append(cfg.Secrets, model.SecretDef{SecretSlug: "api-token", Label: "API token", Required: true, Value: ptr(secret)},
		model.SecretDef{SecretSlug: "empty", Label: "Empty", Required: false})
	cat := *emptyCatalog()
	hh, _ := canonical.HashCanonical(cfg)
	ch, _ := canonical.HashCanonical(cat)
	setHead(t, a, &model.Revision{RevID: newID(), Seq: 1, Origin: "cloud", Actor: "t", CreatedAt: nowTs(), Config: cfg, Hash: hh, Catalog: cat, CatalogHash: ch, Summary: "s", Kind: "change"})

	key := "ABCDEFGHIJKLMNOPQRSTUVWX"
	_ = a.db.SetKV("keyHash", model.AccessKeyRef{Hash: sign.HashAccessKey(key), Version: 1})
	do := func(path, body, cookie, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.RemoteAddr = "10.0.0.9:1234"
		if cookie != "" {
			r.Header.Set("Cookie", "opm_session_app="+cookie)
		}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	session := func(email string) (string, string) {
		rec := do("/_opm/api/login", `{"accessKey":"`+key+`"}`, "", "")
		if rec.Code != 200 {
			t.Fatalf("login %d %s", rec.Code, rec.Body.String())
		}
		ck := rec.Result().Cookies()[0].Value
		rec = do("/_opm/api/actor", `{"email":"`+email+`"}`, ck, "")
		var ar model.ActorResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &ar) != nil {
			t.Fatalf("actor %d %s", rec.Code, rec.Body.String())
		}
		return ck, "Actor " + ar.ActorToken
	}
	ck, auth := session("ops@acme.example")
	reveal := func(slug, ck, auth string) *httptest.ResponseRecorder {
		return do("/_opm/api/secrets/"+slug+"/reveal", "", ck, auth)
	}
	noStore := func(rec *httptest.ResponseRecorder, what string) {
		t.Helper()
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", what, rec.Header().Get("Cache-Control"))
		}
	}

	// errors carry no-store too
	rec := reveal("api-token", "", "")
	if rec.Code != 401 {
		t.Fatalf("no session → %d", rec.Code)
	}
	noStore(rec, "401")
	rec = reveal("nope", ck, auth)
	if rec.Code != 404 {
		t.Fatalf("unknown secret → %d", rec.Code)
	}
	noStore(rec, "404")
	rec = reveal("empty", ck, auth)
	if rec.Code != 404 {
		t.Fatalf("secret without value → %d", rec.Code)
	}

	// success: value + hideAfterSec 60 + no-store
	rec = reveal("api-token", ck, auth)
	var rv model.RevealSecretResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &rv) != nil || rv.Value != secret || rv.HideAfterSec != 60 {
		t.Fatalf("reveal → %d %s", rec.Code, rec.Body.String())
	}
	noStore(rec, "200")

	// activity secret.revealed, never the value
	rows, _ := a.activitiesPage(0, 100, "secret.revealed", "")
	if len(rows) != 1 {
		t.Fatalf("secret.revealed activities %d", len(rows))
	}
	b, _ := json.Marshal(rows[0])
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), `"secretSlug":"api-token"`) || !strings.Contains(string(b), "ops@acme.example") {
		t.Fatalf("activity %s", b)
	}

	// 3 requests so far count (2 × 404 + 1 × 200); 27 more are fine, the 31st is refused
	for i := 0; i < 27; i++ {
		if rec := reveal("api-token", ck, auth); rec.Code != 200 {
			t.Fatalf("reveal %d → %d", i+4, rec.Code)
		}
	}
	rec = reveal("api-token", ck, auth)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "50" || !strings.Contains(rec.Body.String(), `"code":"rate-limited"`) || !strings.Contains(rec.Body.String(), `"retryAfterSec":50`) {
		t.Fatalf("31st reveal → %d %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	noStore(rec, "429")
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatal("429 leaks the value")
	}

	// another session has its own budget
	ck2, auth2 := session("night@acme.example")
	if rec := reveal("api-token", ck2, auth2); rec.Code != 200 {
		t.Fatalf("other session → %d", rec.Code)
	}

	// the next fixed minute resets it
	now = now.Add(50 * time.Second)
	if rec := reveal("api-token", ck, auth); rec.Code != 200 {
		t.Fatalf("next minute → %d %s", rec.Code, rec.Body.String())
	}
	if rows, _ := a.activitiesPage(0, 100, "secret.revealed", ""); len(rows) != 30 {
		t.Fatalf("secret.revealed activities %d, want 30 (28 + other session + next minute)", len(rows))
	}
}
