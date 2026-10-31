package routes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/tunnelseal"
)

type sealedEnv struct {
	dir     string
	srv     *httptest.Server
	release string // base64 public key
	pk      ed25519.PrivateKey
	key     tunnelseal.KeyCert
	pub     []byte
	site    string // per test: the rate limit buckets are per process
	running atomic.Int32
}

const sealedDep = "D1"

func sealedSetup(t *testing.T, withRelease bool, limit int64) *sealedEnv {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("OPM_DATA_DIR", dir)
	t.Setenv("OPM_API_BASE", "https://Cloud.Example/")
	t.Setenv("OPM_RELEASE_PRIVATE_KEY", "")
	t.Setenv("OPM_BLOB_REDIRECT", "1")
	strict := true
	p, err := driver.Open(driver.Config{DataDir: dir, PublicURL: "http://x.test", Strict: &strict})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	cloud.SetPorts(p)
	e := &sealedEnv{dir: dir, site: "s" + strings.ToLower(driver.RandomFrom("abcdefghij", 10))}
	h := cloud.NewHandler(&cloud.CloudConfig{BodyLimit: limit, ConsoleURL: "http://x.test"}, p)
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.running.Add(1)
		defer e.running.Add(-1)
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(e.srv.Close)
	pub, pk, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	e.pk = pk
	if err := cloud.Col.Deployments().Doc(sealedDep).Set(doc.Obj("deploymentId", sealedDep, "deleted", false, "agentEpoch", 1.0)); err != nil {
		t.Fatal(err)
	}
	if err := cloud.Col.Agents(sealedDep).Doc(e.site).Set(doc.Obj("publicKey", sign.PublicKeyB64(pub), "enrolledAt", "x")); err != nil {
		t.Fatal(err)
	}
	if withRelease {
		e.writeRelease(t)
		e.fetchKey(t)
	}
	return e
}

func (e *sealedEnv) writeRelease(t *testing.T) {
	t.Helper()
	pub, sk, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	e.release = base64.StdEncoding.EncodeToString(pub)
	raw, _ := json.Marshal(map[string]string{"privateKeySeedB64": base64.StdEncoding.EncodeToString(sk.Seed()), "publicKeyB64": e.release})
	if err := os.MkdirAll(filepath.Join(e.dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "secrets", "release-key.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *sealedEnv) fetchKey(t *testing.T) {
	t.Helper()
	rs, err := http.Get(e.srv.URL + "/v1/agent/tunnel-key")
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Body.Close()
	if rs.StatusCode != 200 {
		t.Fatalf("tunnel-key %d", rs.StatusCode)
	}
	if err := json.NewDecoder(rs.Body).Decode(&e.key); err != nil {
		t.Fatal(err)
	}
	if e.pub, err = tunnelseal.VerifyCert(e.key, e.release, "https://cloud.example"); err != nil {
		t.Fatalf("%v %+v", err, e.key)
	}
}

// inner signs a request the way an agent does.
func (e *sealedEnv) inner(method, path string, body []byte) model.RelayInner {
	in := model.RelayInner{Method: method, Path: path, Headers: sign.SignRequest(e.pk, sealedDep+"/"+e.site, method, path, body, time.Now().Unix())}
	if len(body) > 0 {
		b := base64.StdEncoding.EncodeToString(body)
		in.BodyB64 = &b
		in.Headers["Content-Type"] = "application/json; charset=utf-8"
	}
	return in
}

func (e *sealedEnv) post(t *testing.T, env []byte, hdr ...string) (int, []byte, http.Header) {
	t.Helper()
	rq, _ := http.NewRequest("POST", e.srv.URL+"/v1/agent/sealed", bytes.NewReader(env))
	rq.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		rq.Header.Set(hdr[i], hdr[i+1])
	}
	rs, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Body.Close()
	b, err := io.ReadAll(rs.Body)
	if err != nil {
		t.Fatal(err)
	}
	return rs.StatusCode, b, rs.Header
}

func (e *sealedEnv) sealInner(t *testing.T, in model.RelayInner) ([]byte, []byte) {
	t.Helper()
	plain, _ := json.Marshal(in)
	env, kResp, err := tunnelseal.Seal(e.pub, plain)
	if err != nil {
		t.Fatal(err)
	}
	return env, kResp
}

func (e *sealedEnv) call(t *testing.T, in model.RelayInner, hdr ...string) *tunnelseal.Response {
	t.Helper()
	env, kResp := e.sealInner(t, in)
	st, raw, _ := e.post(t, env, hdr...)
	if st != 200 {
		t.Fatalf("sealed %d %s", st, raw)
	}
	r, err := tunnelseal.OpenResponse(kResp, raw, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTunnelKeyRoute(t *testing.T) {
	e := sealedSetup(t, false, 1<<20)
	rs, err := http.Get(e.srv.URL + "/v1/agent/tunnel-key")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rs.Body)
	rs.Body.Close()
	if rs.StatusCode != 404 || !strings.Contains(string(b), "not-available") {
		t.Fatalf("no release key: %d %s", rs.StatusCode, b)
	}
	if rs.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("open CORS")
	}
	if st, b, _ := e.post(t, []byte("{}")); st != 404 || !strings.Contains(string(b), "not-available") {
		t.Fatalf("sealed without a key: %d %s", st, b)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "secrets", "tunnel-x25519.key")); !os.IsNotExist(err) {
		t.Fatal("a key file without a release key")
	}
	if _, err := os.Stat(filepath.Join(e.dir, "secrets", "release-key.json")); !os.IsNotExist(err) {
		t.Fatal("the route must not create a release key")
	}
	// OPM_RELEASE_PRIVATE_KEY is honoured
	pub, sk, _ := ed25519.GenerateKey(nil)
	t.Setenv("OPM_RELEASE_PRIVATE_KEY", base64.StdEncoding.EncodeToString(sk.Seed()))
	e.release = base64.StdEncoding.EncodeToString(pub)
	e.fetchKey(t)
	if e.key.Origin != "https://cloud.example" || len(e.key.Kid) != 16 {
		t.Fatalf("%+v", e.key)
	}
	st, err := os.Stat(filepath.Join(e.dir, "secrets", "tunnel-x25519.key"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file %v %v", st, err)
	}
	first := e.key
	e.fetchKey(t)
	if first != e.key {
		t.Fatal("the key must be stable")
	}
	// the data dir's key
	t.Setenv("OPM_RELEASE_PRIVATE_KEY", "")
	e.writeRelease(t)
	e.fetchKey(t)
	if first.Kid != e.key.Kid {
		t.Fatal("the X25519 key does not depend on the release key")
	}
}

func TestSealedRequest(t *testing.T) {
	e := sealedSetup(t, true, 1<<20)
	// CORS
	rq, _ := http.NewRequest("OPTIONS", e.srv.URL+"/v1/agent/sealed", nil)
	rs, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	rs.Body.Close()
	if rs.StatusCode != 204 || rs.Header.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(rs.Header.Get("Access-Control-Allow-Headers"), "Content-Type") {
		t.Fatalf("preflight %d %v", rs.StatusCode, rs.Header)
	}
	env, kResp := e.sealInner(t, e.inner("GET", "/v1/agent/poll?wait=0", nil))
	st, raw, h := e.post(t, env)
	if st != 200 || h.Get("Content-Type") != "application/octet-stream" || h.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%d %v", st, h)
	}
	if bytes.Contains(raw, []byte("deploymentId")) {
		t.Fatal("plaintext in the answer")
	}
	r, err := tunnelseal.OpenResponse(kResp, raw, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != 200 || r.Headers["X-OPM-Server-Time"] == "" || !strings.Contains(string(r.Body), `"deploymentId":"D1"`) {
		t.Fatalf("%d %v %s", r.Status, r.Headers, r.Body)
	}
	// an inner POST with a body, an inner failure stays inside
	bad := e.call(t, e.inner("POST", "/v1/agent/heartbeat", []byte(`{}`)))
	if bad.Status == 200 {
		t.Fatalf("heartbeat {}: %d", bad.Status)
	}
	// a bad signature is the normal 401
	in := e.inner("GET", "/v1/agent/poll?wait=0", nil)
	in.Path = "/v1/agent/poll?wait=1"
	if r := e.call(t, in); r.Status != 401 || !strings.Contains(string(r.Body), "signature") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// A captured sealed request replayed by the tab: the inner nonce stops it and the answer is a fresh keystream.
func TestSealedReplay(t *testing.T) {
	e := sealedSetup(t, true, 1<<20)
	env, kResp := e.sealInner(t, e.inner("GET", "/v1/agent/poll?wait=0", nil))
	st, first, _ := e.post(t, env)
	if st != 200 {
		t.Fatal(st)
	}
	st, second, _ := e.post(t, env)
	if st != 200 {
		t.Fatal(st)
	}
	r, err := tunnelseal.OpenResponse(kResp, second, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != 401 || !strings.Contains(string(r.Body), "nonce") {
		t.Fatalf("replay: %d %s", r.Status, r.Body)
	}
	if bytes.Equal(first[:32], second[:32]) {
		t.Fatal("two answers with the same salt share a key and nonces")
	}
}

func TestSealedKeyErrors(t *testing.T) {
	e := sealedSetup(t, true, 1<<20)
	env, _ := e.sealInner(t, e.inner("GET", "/v1/agent/poll?wait=0", nil))
	var en tunnelseal.Envelope
	_ = json.Unmarshal(env, &en)
	unknown := en
	unknown.Kid = "0000000000000000"
	b, _ := json.Marshal(unknown)
	if st, raw, _ := e.post(t, b); st != 409 || !strings.Contains(string(raw), "tunnel-key-changed") {
		t.Fatalf("kid: %d %s", st, raw)
	}
	bad := en
	bad.Ct = base64.StdEncoding.EncodeToString(make([]byte, 40))
	b, _ = json.Marshal(bad)
	stBad, rawBad, _ := e.post(t, b)
	stJunk, rawJunk, _ := e.post(t, []byte("not json"))
	if stBad != 400 || stJunk != 400 || string(rawBad) != string(rawJunk) {
		t.Fatalf("undecryptable answers must not differ: %d %s / %d %s", stBad, rawBad, stJunk, rawJunk)
	}
	// sealed JSON that is no request
	plainJunk, kResp, err := tunnelseal.Seal(e.pub, []byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	_ = kResp
	if st, _, _ := e.post(t, plainJunk); st != 400 {
		t.Fatalf("sealed junk %d", st)
	}
}

// Anyone can seal to the public key, so the inner path must never reach a human, relay, install or recursive route, and
// the answer must be the one of an unknown route.
func TestSealedInnerRoutes(t *testing.T) {
	e := sealedSetup(t, true, 1<<20)
	unknown := e.call(t, e.inner("GET", "/v1/nope", nil))
	if unknown.Status != 404 {
		t.Fatalf("unknown %d", unknown.Status)
	}
	human := e.inner("GET", "/v1/me", nil)
	human.Headers["Authorization"] = "Bearer x"
	for _, in := range []model.RelayInner{
		human,
		e.inner("GET", "/v1/accounts", nil),
		e.inner("POST", "/v1/accounts", []byte(`{}`)),
		e.inner("POST", "/v1/agent/relay", []byte(`{}`)),
		e.inner("POST", "/v1/agent/enroll", []byte(`{}`)),
		e.inner("POST", "/v1/agent/sealed", []byte(`{}`)),
		e.inner("GET", "/v1/agent/tunnel-key", nil),
		e.inner("GET", "/v1/install/script", nil),
		e.inner("GET", "/v1/agent/../accounts", nil),
		e.inner("POST", "/auth/signin", []byte(`{}`)),
		e.inner("GET", "/healthz", nil),
		e.inner("GET", "", nil),
	} {
		r := e.call(t, in)
		if r.Status != 404 || !strings.Contains(string(r.Body), `"not-found"`) || !strings.Contains(string(r.Body), "no route") {
			t.Errorf("%s %s: %d %s", in.Method, in.Path, r.Status, r.Body)
		}
	}
}

// §21 #114: a tunnelled download streams, never 302; the direct request of the same object redirects.
func TestSealedDownloadStreams(t *testing.T) {
	e := sealedSetup(t, true, 1<<20)
	body := bytes.Repeat([]byte("blob-"), 1_200_000) // 6 MB
	if err := cloud.Blobs().Put("transfers/t1", body, driver.PutOptions{ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	if err := cloud.Col.Transfers(sealedDep).Doc("t1").Set(doc.Obj("siteSlug", e.site, "direction", "to-site", "status", "ready", "storagePath", "transfers/t1", "fileName", "x.bin", "sha256", "sha256:ab")); err != nil {
		t.Fatal(err)
	}
	path := "/v1/agent/transfers/t1/download"
	direct := e.inner("GET", path, nil)
	rq, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	for k, v := range direct.Headers {
		rq.Header.Set(k, v)
	}
	noFollow := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rs, err := noFollow.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	rs.Body.Close()
	if rs.StatusCode != 302 {
		t.Fatalf("direct: %d", rs.StatusCode)
	}
	r := e.call(t, e.inner("GET", path, nil)) // no Origin on the outer request
	if r.Status != 200 || !bytes.Equal(r.Body, body) || r.Headers["X-OPM-Sha256"] != "ab" {
		t.Fatalf("sealed: %d %d %v", r.Status, len(r.Body), r.Headers)
	}
	in := e.inner("GET", path, nil)
	in.Headers["Range"] = "bytes=10-19"
	if r := e.call(t, in, "Origin", "http://lan.example:8080"); r.Status != 206 || string(r.Body) != string(body[10:20]) {
		t.Fatalf("range: %d %q", r.Status, r.Body)
	}
}

// One sealed call costs one token of the site, no more: the whole burst fits.
func TestSealedRateLimitChargedOnce(t *testing.T) {
	e := sealedSetup(t, true, 1<<20)
	ok, limited := 0, 0
	for i := 0; i < 130; i++ {
		switch r := e.call(t, e.inner("GET", "/v1/agent/poll?wait=0", nil)); r.Status {
		case 200:
			ok++
		case 429:
			limited++
		default:
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
	if ok < 100 {
		t.Fatalf("only %d of the burst of 100 passed (%d limited): charged twice?", ok, limited)
	}
	if limited == 0 {
		t.Log("the bucket refilled faster than the test ran")
	}
}

func TestSealedBodyLimit(t *testing.T) {
	e := sealedSetup(t, true, 64<<10)
	env, _ := e.sealInner(t, e.inner("POST", "/v1/agent/heartbeat", make([]byte, 128<<10)))
	if st, _, _ := e.post(t, env); st != 413 {
		t.Fatalf("over the body limit: %d", st)
	}
}

// A long poll waits for the inner handler, and the tab going away cancels it.
func TestSealedLongPoll(t *testing.T) {
	e := sealedSetup(t, true, 1<<20)
	env, kResp := e.sealInner(t, e.inner("GET", "/v1/agent/poll?cursor=1&wait=2", nil))
	t0 := time.Now()
	st, raw, _ := e.post(t, env)
	if st != 200 {
		t.Fatal(st)
	}
	if d := time.Since(t0); d < 1500*time.Millisecond {
		t.Fatalf("answered after %v: the long poll did not wait", d)
	}
	if r, err := tunnelseal.OpenResponse(kResp, raw, 1<<30); err != nil || r.Status != 200 {
		t.Fatalf("%v", err)
	}
	env, _ = e.sealInner(t, e.inner("GET", "/v1/agent/poll?cursor=1&wait=50", nil))
	ctx, cancel := context.WithCancel(context.Background())
	rq, _ := http.NewRequestWithContext(ctx, "POST", e.srv.URL+"/v1/agent/sealed", bytes.NewReader(env))
	done := make(chan struct{})
	go func() {
		defer close(done)
		if rs, err := http.DefaultClient.Do(rq); err == nil {
			rs.Body.Close()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for e.running.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	deadline = time.Now().Add(5 * time.Second)
	for e.running.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the inner long poll kept running after the tab left")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An old agent: the plain route through a tab (Origin header, no sealing) works as before.
func TestOldAgentPlainTab(t *testing.T) {
	e := sealedSetup(t, true, 1<<20)
	in := e.inner("GET", "/v1/agent/poll?wait=0", nil)
	rq, _ := http.NewRequest("GET", e.srv.URL+in.Path, nil)
	for k, v := range in.Headers {
		rq.Header.Set(k, v)
	}
	rq.Header.Set("Origin", "http://lan.example:8080")
	rs, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Body.Close()
	if rs.StatusCode != 200 || rs.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%d %v", rs.StatusCode, rs.Header)
	}
}
