package agent

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/tunnelseal"
)

// secretMarker sits in a config, a request body and a download. Nothing the tab sees may contain it.
const secretMarker = "S3CRET-MARKER-9f27c1"

// tab plays the browser tab of §9.10: long-polls next, fetches, posts the result. It records everything a tab can see:
// the JSON of next, every URL, request and response header and body. A hook can change what the agent gets.
type tab struct {
	t     *testing.T
	a     *Agent
	h     http.Handler
	sid   string
	token string
	lease string
	mu    sync.Mutex
	seen  bytes.Buffer
	urls  []string
	// fetch replaces the network; nil = real fetch
	fetch func(q model.TunnelRequest) (int, map[string]string, []byte, string)
	stop  chan struct{}
}

func (b *tab) api(method, path string, body []byte) (int, []byte) {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Cookie", "opm_session_"+b.a.site+"="+b.sid)
	r.Header.Set("Authorization", "Actor "+b.token)
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, r)
	return rec.Code, rec.Body.Bytes()
}

func realFetch(q model.TunnelRequest) (int, map[string]string, []byte, string) {
	var body io.Reader
	if q.BodyB64 != nil {
		raw, err := base64.StdEncoding.DecodeString(*q.BodyB64)
		if err != nil {
			return 0, nil, nil, err.Error()
		}
		body = bytes.NewReader(raw)
	}
	rq, err := http.NewRequest(q.Method, q.URL, body)
	if err != nil {
		return 0, nil, nil, err.Error()
	}
	for k, v := range q.Headers {
		rq.Header.Set(k, v)
	}
	rs, err := http.DefaultClient.Do(rq)
	if err != nil {
		return 0, nil, nil, err.Error()
	}
	defer rs.Body.Close()
	b, err := io.ReadAll(rs.Body)
	if err != nil {
		return 0, nil, nil, err.Error()
	}
	h := map[string]string{}
	for k := range rs.Header {
		h[k] = rs.Header.Get(k)
	}
	return rs.StatusCode, h, b, ""
}

func startTab(t *testing.T, a *Agent) *tab {
	t.Helper()
	sid := newID()
	exp := time.Now().Add(time.Hour).Unix()
	if _, err := a.db.Exec(`INSERT INTO sessions(sessionId, createdAt, expiresAt, keyVersion, ip, userAgent) VALUES(?,?,?,?,?,?)`, sid, time.Now().Unix(), exp, 1, "", ""); err != nil {
		t.Fatal(err)
	}
	b := &tab{t: t, a: a, h: a.handlerFor("gui-alias"), sid: sid, stop: make(chan struct{}),
		token: sign.MakeActorToken(a.key, model.ActorTokenClaims{Email: "ops@example.com", Sid: sid, Dep: a.dep, Site: a.site, Exp: exp})}
	c, raw := b.api("POST", "/_opm/api/tunnel/start", []byte(`{"userAgent":"test"}`))
	if c != 201 {
		t.Fatalf("start %d %s", c, raw)
	}
	var lr model.TunnelLeaseResponse
	_ = json.Unmarshal(raw, &lr)
	b.lease = lr.Lease.LeaseID
	go b.loop()
	t.Cleanup(func() { close(b.stop) })
	return b
}

func (b *tab) loop() {
	for {
		select {
		case <-b.stop:
			return
		default:
		}
		// a short next: the handler blocks up to 25 s, so poll through a context-free helper with a timeout
		rq := httptest.NewRequest("GET", "/_opm/api/tunnel/next?leaseId="+b.lease, nil)
		rq.Header.Set("Cookie", "opm_session_"+b.a.site+"="+b.sid)
		rq.Header.Set("Authorization", "Actor "+b.token)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		rec := httptest.NewRecorder()
		b.h.ServeHTTP(rec, rq.WithContext(ctx))
		cancel()
		var next model.TunnelNextResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &next)
		b.mu.Lock()
		b.seen.Write(rec.Body.Bytes())
		b.mu.Unlock()
		for _, q := range next.Requests {
			go b.handle(q)
		}
	}
}

func (b *tab) handle(q model.TunnelRequest) {
	b.mu.Lock()
	b.urls = append(b.urls, q.Method+" "+q.URL)
	if q.BodyB64 != nil {
		raw, _ := base64.StdEncoding.DecodeString(*q.BodyB64)
		b.seen.Write(raw)
	}
	for k, v := range q.Headers {
		b.seen.WriteString(k + ": " + v + "\n")
	}
	fetch := b.fetch
	b.mu.Unlock()
	if fetch == nil {
		fetch = realFetch
	}
	status, hdr, body, errText := fetch(q)
	b.mu.Lock()
	b.seen.Write(body)
	for k, v := range hdr {
		if !strings.HasPrefix(k, "Access-Control-") { // the cloud's static CORS lists: names, no value of a request
			b.seen.WriteString(k + ": " + v + "\n")
		}
	}
	b.mu.Unlock()
	res := model.TunnelResult{LeaseID: b.lease, ReqID: q.ReqID, Status: status, Headers: hdr}
	if errText != "" {
		res.Error = &errText
	}
	if len(body) <= 4<<20 {
		s := base64.StdEncoding.EncodeToString(body)
		res.BodyB64 = &s
		rb, _ := json.Marshal(res)
		b.api("POST", "/_opm/api/tunnel/result", rb)
		return
	}
	res.Chunked = true
	rb, _ := json.Marshal(res)
	b.api("POST", "/_opm/api/tunnel/result", rb)
	parts := 0
	for off := 0; off < len(body); off += 4 << 20 {
		end := min(off+4<<20, len(body))
		b.api("POST", fmt.Sprintf("/_opm/api/tunnel/result/%s/chunk?leaseId=%s&index=%d", q.ReqID, b.lease, parts), body[off:end])
		parts++
	}
	sum := sha256.Sum256(body)
	cb, _ := json.Marshal(model.TunnelCompleteRequest{LeaseID: b.lease, Parts: parts, Sha256: "sha256:" + hex.EncodeToString(sum[:])})
	b.api("POST", "/_opm/api/tunnel/result/"+q.ReqID+"/complete", cb)
}

func (b *tab) setFetch(f func(q model.TunnelRequest) (int, map[string]string, []byte, string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fetch = f
}

func (b *tab) calls(substr string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, u := range b.urls {
		if strings.Contains(u, substr) {
			n++
		}
	}
	return n
}

// enrolledAgent is an agent enrolled in fc with no direct route to it.
func enrolledAgent(t *testing.T, fc *fakecloud.Cloud, slug string, pinned bool) *Agent {
	t.Helper()
	a := newTestAgent(t)
	a.cfg.APIBase = fc.URL
	a.dep = fc.DeploymentID
	a.site = slug
	a.cfg.InstallTokenFile = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(a.cfg.InstallTokenFile, []byte(fc.CreateInstallToken(slug)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Enroll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pinned {
		a.cfg.ReleasePublicKey = fc.TunnelReleaseKey()
	}
	fc.BlockSite(slug, true)
	a.mu.Lock()
	a.lastDirectOk, a.lastCloudOk = time.Time{}, time.Time{}
	a.mu.Unlock()
	return a
}

func sealedSetup(t *testing.T) (*fakecloud.Cloud, *Agent, *tab) {
	t.Helper()
	fc := fakecloud.Start("DepSeal")
	t.Cleanup(fc.Close)
	a := enrolledAgent(t, fc, "app", true)
	return fc, a, startTab(t, a)
}

func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func leaseError(a *Agent) string {
	l := a.tun.active()
	if l == nil || l.LastError == nil {
		return ""
	}
	return *l.LastError
}

// The tab is a recording proxy: real agents, a cloud with a known secret. Nothing it saw shows the secret, an inner
// path or a signature, through inline, chunked (> 4 MiB) and peer-relayed requests.
func TestTunnelTabSeesOnlyCiphertext(t *testing.T) {
	fc, a, b := sealedSetup(t)
	ctx := callCtx(t)
	v := secretMarker
	rev, _ := fc.Publish(model.Config{Secrets: []model.SecretDef{{SecretSlug: "db-pass", Value: &v}}, Sites: []model.Site{}}, model.RevisionCatalog{}, "config")

	// a config with the secret, read back
	var got struct {
		Revision model.Revision `json:"revision"`
	}
	if err := a.cloudJSON(ctx, "GET", "/v1/agent/revisions/"+rev.RevID, nil, &got, callOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(got.Revision.Config.Secrets) != 1 || *got.Revision.Config.Secrets[0].Value != secretMarker {
		t.Fatalf("revision %+v", got.Revision.Config.Secrets)
	}
	// a body going up
	if err := a.cloudJSON(ctx, "POST", "/v1/agent/activities", model.AgentActivitiesRequest{SiteSlug: "app", Entries: []model.Activity{}}, nil, callOpts{extraHdr: map[string]string{"X-Marker": secretMarker}}); err != nil {
		t.Fatal(err)
	}
	// one big answer: > 4 MiB in one request, the chunked path of the tab
	data := bytes.Repeat([]byte(secretMarker+"-"), 300_000)
	if len(data) < 5<<20 {
		t.Fatalf("test data %d", len(data))
	}
	tr := fc.AddTransferToSite("app", "big.bin", data, "")
	resp, err := a.cloudCall(ctx, "GET", "/v1/agent/transfers/"+tr.TransferID+"/download", nil, callOpts{timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || !bytes.Equal(resp.Body, data) || resp.Route != "tunnel" {
		t.Fatalf("download %d %d %s", resp.Status, len(resp.Body), resp.Route)
	}
	// the normal download: ranges
	dst := filepath.Join(t.TempDir(), "big.bin")
	if err := a.cloudDownload(ctx, "/v1/agent/transfers/"+tr.TransferID+"/download", dst, hexOf(tr.Sha256), tr.SizeBytes, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.ReadFile(dst); !bytes.Equal(st, data) {
		t.Fatal("ranged download differs")
	}
	// a peer with no route relays through the holder, which keeps its signature
	peer := enrolledAgent(t, fc, "db", true)
	path := "/v1/agent/revisions/" + rev.RevID
	inner := model.RelayInner{Method: "GET", Path: path, Headers: peer.signedHeaders("GET", path, nil)}
	body, _ := json.Marshal(model.PeerRelayRequest{Request: inner})
	r := httptest.NewRequest("POST", "/_opm/peer/relay", bytes.NewReader(body))
	rr, he := a.handlePeerRelay(httptest.NewRecorder(), withRC(r, &reqCtx{peer: &peerCaller{Site: "db"}}))
	if he != nil {
		t.Fatalf("relay: %+v", he)
	}
	out := rr.(model.RelayResponse)
	raw, _ := base64.StdEncoding.DecodeString(*out.BodyB64)
	if out.Status != 200 || !bytes.Contains(raw, []byte(secretMarker)) {
		t.Fatalf("relay %d %s", out.Status, raw)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	seen := b.seen.Bytes()
	urls := strings.Join(b.urls, "\n")
	if len(seen) < 5<<20 {
		t.Fatalf("the recording has %d bytes: the proxy saw too little", len(seen))
	}
	for _, s := range []string{secretMarker, base64.StdEncoding.EncodeToString([]byte(secretMarker)), "/v1/agent/revisions", "/v1/agent/activities", "/v1/agent/transfers", rev.RevID, tr.TransferID, "X-OPM-Sig", "X-OPM-Nonce", "X-OPM-Site", "X-Marker", "Range", "db-pass"} {
		if bytes.Contains(seen, []byte(s)) {
			t.Errorf("the tab saw %q", s)
		}
		if strings.Contains(urls, s) {
			t.Errorf("a URL shows %q", s)
		}
	}
	for _, u := range b.urls {
		if u != "POST "+fc.URL+"/v1/agent/sealed" && u != "GET "+fc.URL+"/v1/agent/tunnel-key" {
			t.Errorf("the tab was asked for %s", u)
		}
	}
	if fc.Requests("/v1/agent/transfers/") < 3 {
		t.Fatalf("inner downloads %d", fc.Requests("/v1/agent/transfers/"))
	}
}

func TestTunnelStateEncrypted(t *testing.T) {
	_, a, b := sealedSetup(t)
	c, raw := b.api("GET", "/_opm/api/tunnel", nil)
	var st model.TunnelStateResponse
	if err := json.Unmarshal(raw, &st); c != 200 || err != nil {
		t.Fatalf("%d %v", c, err)
	}
	if !st.Encrypted || st.Lease == nil || !st.Lease.Encrypted {
		t.Fatalf("%+v", st)
	}
	if items, _ := a.activitiesPage(0, 10, "internet.tunnel.plaintext", ""); len(items) != 0 {
		t.Fatal("no plaintext activity on a sealed lease")
	}
}

// A tab swapping the key in any way ends in a failed call and no request.
func TestTunnelTabSwapsKey(t *testing.T) {
	fc, a, b := sealedSetup(t)
	_, attackerRelease, _ := ed25519.GenerateKey(nil)
	x, _ := ecdh.X25519().GenerateKey(rand.Reader)
	origin, _ := tunnelseal.Origin(fc.URL)
	forged, _ := json.Marshal(tunnelseal.SignCert(attackerRelease, x.PublicKey().Bytes(), origin))
	// the genuine signature of the cloud's key with another key put in
	_, _, certRaw, _ := realFetch(model.TunnelRequest{Method: "GET", URL: fc.URL + "/v1/agent/tunnel-key"})
	var genuine tunnelseal.KeyCert
	_ = json.Unmarshal(certRaw, &genuine)
	swapped := genuine
	swapped.PublicKey = base64.StdEncoding.EncodeToString(x.PublicKey().Bytes())
	swapped.Kid = tunnelseal.Kid(x.PublicKey().Bytes())
	swappedRaw, _ := json.Marshal(swapped)
	for name, answer := range map[string][]byte{"forged": forged, "swapped": swappedRaw, "junk": []byte("{")} {
		b.setFetch(func(q model.TunnelRequest) (int, map[string]string, []byte, string) {
			return 200, map[string]string{}, answer, ""
		})
		_, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 3 * time.Second})
		if err == nil {
			t.Fatalf("%s: a swapped key was used", name)
		}
		if !strings.Contains(leaseError(a), "tunnel refused") {
			t.Fatalf("%s: lastError %q", name, leaseError(a))
		}
		if b.calls("/v1/agent/sealed") != 0 {
			t.Fatalf("%s: something was sealed to the swapped key", name)
		}
	}
	if b.calls("/v1/agent/poll") != 0 {
		t.Fatal("plaintext fallback")
	}
}

// A missing key cannot downgrade the agent: the call fails, with a reason.
func TestTunnelNoDowngrade(t *testing.T) {
	fc, a, b := sealedSetup(t)
	fc.SetSealedRoutes(false) // a cloud without the feature, or a tab answering 404
	_, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("no key: the call must fail")
	}
	if !strings.Contains(leaseError(a), "no tunnel key") {
		t.Fatalf("lastError %q", leaseError(a))
	}
	if b.calls("/v1/agent/poll") != 0 || fc.Requests("/v1/agent/poll") != 0 {
		t.Fatal("a request went out in clear")
	}
	// the cloud comes back: the same lease works and the error goes
	fc.SetSealedRoutes(true)
	if _, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if leaseError(a) != "" {
		t.Fatalf("stale lastError %q", leaseError(a))
	}
}

func TestTunnelKeyFetchFails(t *testing.T) {
	_, a, b := sealedSetup(t)
	b.setFetch(func(q model.TunnelRequest) (int, map[string]string, []byte, string) { return 0, nil, nil, "TypeError: failed" })
	if _, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 3 * time.Second}); err == nil {
		t.Fatal("no key: the call must fail")
	}
	if !strings.Contains(leaseError(a), "cannot fetch the cloud's tunnel key") {
		t.Fatalf("lastError %q", leaseError(a))
	}
}

// Without a pinned key the agent has nothing to verify with: fail closed, unless agent.json asks for clear text.
func TestTunnelNoPinnedKey(t *testing.T) {
	fc := fakecloud.Start("DepSeal")
	t.Cleanup(fc.Close)
	a := enrolledAgent(t, fc, "app", false)
	b := startTab(t, a)
	_, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 3 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "releasePublicKey") || !strings.Contains(leaseError(a), "releasePublicKey") {
		t.Fatalf("err %v lastError %q", err, leaseError(a))
	}
	if n := len(b.urls); n != 0 {
		t.Fatalf("the tab got %d requests", n)
	}

	p := enrolledAgent(t, fc, "db", false)
	p.cfg.TunnelPlaintext = true
	pb := startTab(t, p)
	fc.BlockSite("db", false) // the block goes by the signed site header, which a clear tab request shows
	p.mu.Lock()
	p.directFailAt = time.Now() // direct route down
	p.mu.Unlock()
	if _, err := p.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if pb.calls("/v1/agent/poll") != 1 {
		t.Fatalf("clear call: %v", pb.urls)
	}
	if items, _ := p.activitiesPage(0, 10, "internet.tunnel.plaintext", ""); len(items) != 1 {
		t.Fatalf("plaintext activity %d", len(items))
	}
	if st := p.tun.active(); st == nil || st.Encrypted {
		t.Fatalf("lease %+v", st)
	}

	// a pinned key beats tunnelPlaintext
	s := enrolledAgent(t, fc, "edge", true)
	s.cfg.TunnelPlaintext = true
	sb := startTab(t, s)
	if _, err := s.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if sb.calls("/v1/agent/poll") != 0 || sb.calls("/v1/agent/sealed") != 1 {
		t.Fatalf("pinned: %v", sb.urls)
	}
}

func isSealed(q model.TunnelRequest) bool { return strings.HasSuffix(q.URL, "/v1/agent/sealed") }

// A tab tampering with, cutting or replaying the answer: the call fails.
func TestTunnelTabTampersResponse(t *testing.T) {
	_, a, b := sealedSetup(t)
	mutate := func(f func([]byte) []byte) {
		b.setFetch(func(q model.TunnelRequest) (int, map[string]string, []byte, string) {
			st, h, body, e := realFetch(q)
			if isSealed(q) {
				body = f(bytes.Clone(body))
			}
			return st, h, body, e
		})
	}
	for name, f := range map[string]func([]byte) []byte{
		"bit flip":  func(d []byte) []byte { d[len(d)-1] ^= 1; return d },
		"truncated": func(d []byte) []byte { return d[:len(d)/2] },
		"empty":     func(d []byte) []byte { return nil },
		"extended":  func(d []byte) []byte { return append(d, 0, 0, 0, 0) },
	} {
		mutate(f)
		_, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second})
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if !strings.Contains(leaseError(a), "tunnel refused") {
			t.Fatalf("%s: lastError %q", name, leaseError(a))
		}
	}
	// a forged 200 in clear is not an answer either
	b.setFetch(func(q model.TunnelRequest) (int, map[string]string, []byte, string) {
		if isSealed(q) {
			return 200, map[string]string{"Content-Type": "application/json"}, []byte(`{"deploymentId":"x"}`), ""
		}
		return realFetch(q)
	})
	if _, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second}); err == nil {
		t.Fatal("a clear answer was accepted")
	}
}

// An old response for a new request: the ephemeral key differs.
func TestTunnelTabReplaysOldResponse(t *testing.T) {
	_, a, b := sealedSetup(t)
	var old []byte
	var hdr map[string]string
	b.setFetch(func(q model.TunnelRequest) (int, map[string]string, []byte, string) {
		st, h, body, e := realFetch(q)
		if isSealed(q) {
			if old != nil {
				return 200, hdr, old, ""
			}
			old, hdr = body, h
		}
		return st, h, body, e
	})
	if _, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second}); err == nil {
		t.Fatal("an old response was accepted for a new request")
	}
}

// A captured sealed request replayed by the tab: the inner nonce stops it before any handler.
func TestTunnelTabReplaysRequest(t *testing.T) {
	fc, a, b := sealedSetup(t)
	var captured model.TunnelRequest
	var mu sync.Mutex
	b.setFetch(func(q model.TunnelRequest) (int, map[string]string, []byte, string) {
		if isSealed(q) {
			mu.Lock()
			captured = q
			mu.Unlock()
		}
		return realFetch(q)
	})
	body, _ := json.Marshal(model.AgentActivitiesRequest{SiteSlug: "app", Entries: []model.Activity{}})
	if _, err := a.cloudCall(callCtx(t), "POST", "/v1/agent/activities", body, callOpts{timeout: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if fc.Requests("/v1/agent/activities") != 1 {
		t.Fatalf("handled %d", fc.Requests("/v1/agent/activities"))
	}
	mu.Lock()
	q := captured
	mu.Unlock()
	st, _, first, _ := realFetch(q)
	_, _, second, _ := realFetch(q)
	if st != 200 {
		t.Fatalf("replay status %d", st)
	}
	if fc.Requests("/v1/agent/activities") != 1 {
		t.Fatal("the replay reached the handler")
	}
	if bytes.Equal(first[:32], second[:32]) {
		t.Fatal("two answers to one request share their key")
	}
}

// The cloud key changes: the agent fetches and verifies it again, once, and the call goes through.
func TestTunnelKeyRotation(t *testing.T) {
	fc, a, b := sealedSetup(t)
	ctx := callCtx(t)
	call := func() error {
		_, err := a.cloudCall(ctx, "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second})
		return err
	}
	if err := call(); err != nil {
		t.Fatal(err)
	}
	if err := call(); err != nil {
		t.Fatal(err)
	}
	if n := b.calls("/v1/agent/tunnel-key"); n != 1 {
		t.Fatalf("key fetched %d times: not cached", n)
	}
	fc.RotateTunnelKey()
	if err := call(); err != nil {
		t.Fatal(err)
	}
	if n := b.calls("/v1/agent/tunnel-key"); n != 2 {
		t.Fatalf("after a rotation %d fetches", n)
	}
	// from the store after a restart, verified again
	a.tun.key = nil
	if err := call(); err != nil {
		t.Fatal(err)
	}
	if n := b.calls("/v1/agent/tunnel-key"); n != 2 {
		t.Fatalf("not read from the store: %d", n)
	}
	// a stored key that does not verify any more is not used
	a.tun.key = nil
	var c tunnelseal.KeyCert
	if ok, _ := a.db.GetKV(tunnelKeyKV, &c); !ok {
		t.Fatal("no stored key")
	}
	c.Sig = strings.Repeat("A", len(c.Sig))
	_ = a.db.SetKV(tunnelKeyKV, c)
	if err := call(); err != nil {
		t.Fatal(err)
	}
	if n := b.calls("/v1/agent/tunnel-key"); n != 3 {
		t.Fatalf("a bad stored key: %d", n)
	}
	// a tab answering tunnel-key-changed for ever costs one retry, then the call fails
	b.setFetch(func(q model.TunnelRequest) (int, map[string]string, []byte, string) {
		if isSealed(q) {
			return 409, map[string]string{}, []byte(`{"error":{"code":"tunnel-key-changed","message":"x"}}`), ""
		}
		return realFetch(q)
	})
	before := b.calls("/v1/agent/sealed")
	if err := call(); err == nil {
		t.Fatal("must fail")
	}
	if n := b.calls("/v1/agent/sealed") - before; n != 2 {
		t.Fatalf("attempts %d", n)
	}
}

// Headers and the server time count only from inside the encryption.
func TestTunnelHeadersFromInside(t *testing.T) {
	_, a, b := sealedSetup(t)
	b.setFetch(func(q model.TunnelRequest) (int, map[string]string, []byte, string) {
		st, h, body, e := realFetch(q)
		h["X-OPM-Server-Time"] = "1"
		h["X-Forged"] = "1"
		return st, h, body, e
	})
	r, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if r.Header.Get("X-Forged") != "" || r.Header.Get("X-OPM-Server-Time") == "1" {
		t.Fatalf("headers from the tab: %v", r.Header)
	}
	if off := a.offset(); off != 0 {
		t.Fatalf("clock offset %d from a tab's header", off)
	}
}

// An endless answer from a tab stops at the cap, without waiting for it.
func TestTunnelResponseCap(t *testing.T) {
	_, a, b := sealedSetup(t)
	b.setFetch(func(q model.TunnelRequest) (int, map[string]string, []byte, string) {
		if isSealed(q) {
			return 200, map[string]string{}, make([]byte, (maxSealedParts+2)*(4<<20)), ""
		}
		return realFetch(q)
	})
	t0 := time.Now()
	_, err := a.cloudCall(callCtx(t), "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 15 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("err %v", err)
	}
	if time.Since(t0) > 12*time.Second {
		t.Fatal("waited for the timeout instead of stopping at the cap")
	}
}
