package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Options{Config: model.AgentConfigFile{APIBase: "http://127.0.0.1:1", DeploymentID: "DepUnit", SiteSlug: "app", DataDir: dir, AdvertiseHost: "127.0.0.1",
		AgentPort: freePort(t), GuiAliasPort: model.Some(freePort(t)), EdgeHTTPPort: model.Null[int]()}, Timings: fastTimings()})
	if err != nil {
		t.Fatal(err)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() { a.cancel(); a.db.Close() })
	return a
}

func TestGUIRouting(t *testing.T) {
	a := newTestAgent(t)
	cases := []struct {
		path   string
		status int
		want   string
	}{
		{"/", 302, ""}, {"/_opm", 302, ""}, {"/_opm/", 302, ""},
		{"/_opm/login", 200, "<"}, {"/_opm/app", 200, "<"}, {"/_opm/app/updates", 200, "<"},
		{"/_opm/nope", 404, "OPM agent"}, {"/_opm/assets/none.js", 404, "not found"},
		{"/_opm/healthz", 200, "ok"}, {"/_opm/peer/ping", 404, "OPM agent"}, {"/other", 404, "OPM agent"},
	}
	h := a.handlerFor("gui-alias")
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s → %d %q", c.path, rec.Code, rec.Body.String()[:min(60, rec.Body.Len())])
		}
		if c.path == "/_opm/app" && rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("index cache %q", rec.Header().Get("Cache-Control"))
		}
	}
	// API: info is public, overview needs a session
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_opm/api/info", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"surface":"gui-alias"`) {
		t.Fatalf("info %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_opm/api/overview", nil))
	if rec.Code != 401 {
		t.Fatalf("overview without session %d", rec.Code)
	}
}

func TestLoginRateLimitAndActor(t *testing.T) {
	a := newTestAgent(t)
	h := a.handlerFor("gui-alias")
	post := func(path, body, cookie, auth string) *httptest.ResponseRecorder {
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
	if rec := post("/_opm/api/login", `{"accessKey":"x"}`, "", ""); rec.Code != 503 {
		t.Fatalf("no key hash → %d", rec.Code)
	}
	key := "ABCDEFGHIJKLMNOPQRSTUVWX"
	_ = a.db.SetKV("keyHash", model.AccessKeyRef{Hash: sign.HashAccessKey(key), Version: 1})
	for i := 1; i <= 5; i++ {
		rec := post("/_opm/api/login", `{"accessKey":"AAAAAAAAAAAAAAAAAAAAAAAA"}`, "", "")
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"max":5`) {
			t.Fatalf("fail %d → %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if rec := post("/_opm/api/login", `{"accessKey":"`+key+`"}`, "", ""); rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "retryAfterSec") {
		t.Fatalf("rate limit → %d %s", rec.Code, rec.Body.String())
	}
	_, _ = a.db.Exec(`DELETE FROM login_limits`)
	rec := post("/_opm/api/login", `{"accessKey":"abcd efgh ijkl mnop qrst uvwx"}`, "", "")
	if rec.Code != 200 {
		t.Fatalf("login → %d %s", rec.Code, rec.Body.String())
	}
	ck := rec.Result().Cookies()[0]
	if ck.Name != "opm_session_app" || ck.Path != "/_opm" || !ck.HttpOnly || ck.MaxAge != 21600 {
		t.Fatalf("cookie %+v", ck)
	}
	// a stale legacy cookie of another agent on the same host does not shadow ours
	r := httptest.NewRequest("GET", "/_opm/api/session", nil)
	r.Header.Set("Cookie", "opm_session=OTHER-SITE; opm_session_app="+ck.Value)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != 200 {
		t.Fatalf("site cookie must win over the legacy one: %d", rr.Code)
	}
	if rec := post("/_opm/api/proposals/local/preview", `{}`, ck.Value, ""); rec.Code != 401 || !strings.Contains(rec.Body.String(), "actor-required") {
		t.Fatalf("no actor → %d %s", rec.Code, rec.Body.String())
	}
	rec = post("/_opm/api/actor", `{"email":"bad"}`, ck.Value, "")
	if rec.Code != 422 {
		t.Fatalf("bad email → %d", rec.Code)
	}
	rec = post("/_opm/api/actor", `{"email":"A@b.co"}`, ck.Value, "")
	if rec.Code != 200 {
		t.Fatalf("actor → %d", rec.Code)
	}
	if rows, _ := a.activitiesPage(0, 100, "login.failed", ""); len(rows) != 5 {
		t.Fatalf("login.failed activities %d", len(rows))
	}
}

func TestActivityChain(t *testing.T) {
	a := newTestAgent(t)
	for i := 0; i < 5; i++ {
		a.activity(nil, "system", "agent.started", "x", map[string]any{"n": i})
	}
	list := a.activitiesRange(1, 0, 0)
	if len(list) != 5 || list[0].PrevHash != nil || *list[1].PrevHash != list[0].Hash {
		t.Fatalf("chain %+v", list)
	}
	page, cur := a.activitiesPage(0, 2, "", "")
	if len(page) != 2 || page[0].Seq != 5 || cur == nil || *cur != "4" {
		t.Fatalf("page %+v %v", page, cur)
	}
	a.activity(nil, "system", "agent.started", "new", nil)
	page2, _ := a.activitiesPage(4, 2, "", "")
	if page2[0].Seq != 3 {
		t.Fatalf("cursor not stable: %+v", page2)
	}
}

func TestRecomputeStatusAndLocalConfig(t *testing.T) {
	a := newTestAgent(t)
	p := &model.Proposal{Status: model.ProposalPublished, Sites: map[string]model.SiteStaging{"a": {Staging: "pending"}, "b": {Staging: "pending"}}}
	a.recomputeStatus(p)
	if p.Status != model.ProposalPublished {
		t.Fatal(p.Status)
	}
	p.Sites["a"] = model.SiteStaging{Staging: "ready"}
	a.recomputeStatus(p)
	if p.Status != model.ProposalStaging {
		t.Fatal(p.Status)
	}
	p.Sites["b"] = model.SiteStaging{Staging: "ready"}
	a.recomputeStatus(p)
	if p.Status != model.ProposalReady {
		t.Fatal(p.Status)
	}
	head := model.Config{Secrets: []model.SecretDef{{SecretSlug: "s", Value: ptr("old")}, {SecretSlug: "g", Generate: &model.SecretGenerate{Charset: "hex", Length: 16}, Value: ptr("x")}}}
	gen := true
	in := model.ConfigInput{Secrets: []model.SecretDefInput{{SecretSlug: "s", PlainValue: model.Some("new")}, {SecretSlug: "g", Generate: head.Secrets[1].Generate, GenerateNow: &gen}}}
	out := buildLocalConfig(&head, in)
	if *out.Secrets[0].Value != "new" || out.Secrets[0].RotatedAt == nil || len(*out.Secrets[1].Value) != 16 {
		t.Fatalf("secrets %+v", out.Secrets)
	}
	keep := buildLocalConfig(&head, model.ConfigInput{Secrets: []model.SecretDefInput{{SecretSlug: "s"}}})
	if *keep.Secrets[0].Value != "old" {
		t.Fatal("keep")
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	src := t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "sub", "a.txt"), []byte("hello"), 0o640)
	_ = os.Symlink("sub/a.txt", filepath.Join(src, "link"))
	dst := filepath.Join(t.TempDir(), "v.tar.zst")
	size, sum, err := archiveDir(src, dst)
	if err != nil || size == 0 || len(sum) != 64 {
		t.Fatal(err)
	}
	out := t.TempDir()
	_ = os.WriteFile(filepath.Join(out, "stale"), []byte("x"), 0o644)
	if err := extractArchive(dst, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(out, "sub", "a.txt"))
	if string(b) != "hello" {
		t.Fatal("content")
	}
	if _, err := os.Stat(filepath.Join(out, "stale")); !os.IsNotExist(err) {
		t.Fatal("stale file kept")
	}
	if l, _ := os.Readlink(filepath.Join(out, "link")); l != "sub/a.txt" {
		t.Fatal("symlink")
	}
}

func TestFetcherRendezvous(t *testing.T) {
	a := newTestAgent(t)
	a.lastDirectOk = time.Now()
	fs := a.fetcherSet(strings.Repeat("a", 64), nil)
	if len(fs) != 1 || fs[0] != "app" {
		t.Fatalf("%v", fs)
	}
	_ = io.Discard
	_ = http.StatusOK
}
