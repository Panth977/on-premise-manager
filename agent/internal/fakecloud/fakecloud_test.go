package fakecloud

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

const dep = "DepTest0000000000001"

type site struct {
	slug string
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

func newSite(slug string) *site {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	return &site{slug: slug, priv: priv, pub: pub}
}

func nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// sign returns the X-OPM-* headers for a request.
func (s *site) sign(method, path string, body []byte, ts int64, n string) map[string]string {
	str := canonical.SignedRequestString(method, path, ts, n, sha256Hex(body))
	return map[string]string{
		"X-OPM-Site": dep + "/" + s.slug, "X-OPM-Ts": strconv.FormatInt(ts, 10), "X-OPM-Nonce": n,
		"X-OPM-Sig": base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.priv, []byte(str))),
	}
}

func do(t *testing.T, c *Cloud, method, path string, body []byte, h map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, c.URL+path, bytes.NewReader(body))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func (s *site) call(t *testing.T, c *Cloud, method, path string, v any) (*http.Response, []byte) {
	t.Helper()
	var body []byte
	if v != nil {
		body, _ = json.Marshal(v)
	}
	return do(t, c, method, path, body, s.sign(method, path, body, time.Now().Unix(), nonce()))
}

func enroll(t *testing.T, c *Cloud, s *site) {
	t.Helper()
	tok := c.CreateInstallToken(s.slug)
	body, _ := json.Marshal(model.EnrollRequest{PublicKey: base64.StdEncoding.EncodeToString(s.pub), AgentVersion: "test", AdvertiseHost: "127.0.0.1", AgentPort: 7080, OS: "linux", Arch: "arm64"})
	res, b := do(t, c, "POST", "/v1/agent/enroll", body, map[string]string{"Authorization": "Bearer " + tok})
	if res.StatusCode != 201 {
		t.Fatalf("enroll: %d %s", res.StatusCode, b)
	}
	res, _ = do(t, c, "POST", "/v1/agent/enroll", body, map[string]string{"Authorization": "Bearer " + tok})
	if res.StatusCode != 410 {
		t.Fatalf("second enroll: want 410, got %d", res.StatusCode)
	}
	if res.Header.Get("X-OPM-Server-Time") == "" {
		t.Fatal("missing X-OPM-Server-Time")
	}
}

func reason(t *testing.T, b []byte) string {
	var e model.APIErrorBody
	_ = json.Unmarshal(b, &e)
	r, _ := e.Error.Details["reason"].(string)
	return r
}

func testConfig(sites ...string) model.Config {
	cfg := model.Config{Variables: []model.Variable{}, Secrets: []model.SecretDef{}, Files: []model.FileDef{}, Sites: []model.Site{}}
	for _, s := range sites {
		cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: s, Label: s, Config: model.SiteConfig{Host: nil, AgentPort: 7080, Volumes: &[]model.SiteVolume{}, Containers: []model.Container{}, Depends: []model.SiteRef{}, Edge: model.Edge{HTTPPort: 80, Hosts: []model.EdgeHost{}, Routes: []model.EdgeRoute{}}}})
	}
	return cfg
}

func emptyCat() model.RevisionCatalog {
	return model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
}

func TestEnrollPollAuth(t *testing.T) {
	c := Start(dep)
	defer c.Close()
	ak := c.SetAccessKey("abcd-efgh-ijkl-mnop-qrst-uvwx")
	if ak.Version != 1 || !strings.HasPrefix(ak.Hash, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("access key %+v", ak)
	}
	a := newSite("app")
	enroll(t, c, a)

	res, b := a.call(t, c, "GET", "/v1/agent/poll?cursor=&wait=25", nil)
	if res.StatusCode != 200 {
		t.Fatalf("poll %d %s", res.StatusCode, b)
	}
	var v model.CloudView
	_ = json.Unmarshal(b, &v)
	if v.Source != "cloud" || len(v.Peers) != 1 || v.AccessKey.Version != 1 || v.Proposals == nil {
		t.Fatalf("view %+v", v)
	}
	// hold, then answer after publish
	go func() {
		time.Sleep(300 * time.Millisecond)
		c.Publish(testConfig("app"), emptyCat(), "release 1")
	}()
	start := time.Now()
	res, b = a.call(t, c, "GET", "/v1/agent/poll?cursor="+v.Cursor+"&wait=10", nil)
	if res.StatusCode != 200 || time.Since(start) < 250*time.Millisecond || time.Since(start) > 5*time.Second {
		t.Fatalf("long poll %d after %v", res.StatusCode, time.Since(start))
	}
	var v2 model.CloudView
	_ = json.Unmarshal(b, &v2)
	if v2.Cursor == v.Cursor || len(v2.Proposals) != 1 || v2.Proposals[0].Status != model.ProposalPublished {
		t.Fatalf("view2 %+v", v2)
	}

	// bad signature
	path := "/v1/agent/poll?wait=0"
	h := a.sign("GET", "/v1/agent/poll?wait=1", nil, time.Now().Unix(), nonce())
	res, b = do(t, c, "GET", path, nil, h)
	if res.StatusCode != 401 || reason(t, b) != "signature" || res.Header.Get("X-OPM-Server-Time") == "" {
		t.Fatalf("bad sig: %d %s", res.StatusCode, b)
	}
	// replay
	n := nonce()
	h = a.sign("GET", path, nil, time.Now().Unix(), n)
	if res, _ = do(t, c, "GET", path, nil, h); res.StatusCode != 200 {
		t.Fatalf("first: %d", res.StatusCode)
	}
	if res, b = do(t, c, "GET", path, nil, h); res.StatusCode != 401 || reason(t, b) != "nonce" {
		t.Fatalf("replay: %d %s", res.StatusCode, b)
	}
	// clock
	h = a.sign("GET", path, nil, time.Now().Unix()-120, nonce())
	if res, b = do(t, c, "GET", path, nil, h); res.StatusCode != 401 || reason(t, b) != "clock" {
		t.Fatalf("clock: %d %s", res.StatusCode, b)
	}
	// unknown site
	x := newSite("zzz")
	if res, b = x.call(t, c, "GET", path, nil); res.StatusCode != 401 || reason(t, b) != "unknown-site" {
		t.Fatalf("unknown: %d %s", res.StatusCode, b)
	}
	// heartbeat
	res, b = a.call(t, c, "POST", "/v1/agent/heartbeat", model.HeartbeatRequest{SiteSlug: "app", AgentVersion: "v2", AdvertiseHost: "127.0.0.1", AgentPort: 7080, KeyVersion: 1, Hostname: "h", DetectedAddresses: []string{"10.1.2.3", "h"}})
	if res.StatusCode != 200 {
		t.Fatalf("heartbeat %d %s", res.StatusCode, b)
	}
	if d, _ := c.Agent("app"); d.AgentVersion != "v2" || d.Hostname != "h" || strings.Join(d.DetectedAddresses, ",") != "10.1.2.3,h" {
		t.Fatalf("agent doc %+v", d)
	}
	if res, _ = a.call(t, c, "POST", "/v1/agent/heartbeat", model.HeartbeatRequest{SiteSlug: "db"}); res.StatusCode != 422 {
		t.Fatalf("heartbeat other site: %d", res.StatusCode)
	}
}

func mkActivity(seq int, prev *string, summary string) model.Activity {
	a := model.Activity{ActivityID: "app:" + strconv.Itoa(seq), DeploymentID: dep, SiteSlug: "app", Seq: seq, Ts: nowTs(), Origin: "system", Type: "agent.started", Summary: summary, Data: map[string]any{"n": float64(seq)}, PrevHash: prev}
	h, _ := canonical.ActivityHash(activityFields(a))
	a.Hash = h
	return a
}

func TestActivities(t *testing.T) {
	c := Start(dep)
	defer c.Close()
	a := newSite("app")
	enroll(t, c, a)
	a1 := mkActivity(1, nil, "one")
	a2 := mkActivity(2, &a1.Hash, "two")
	res, b := a.call(t, c, "POST", "/v1/agent/activities", model.AgentActivitiesRequest{SiteSlug: "app", Entries: []model.Activity{a1, a2}})
	var r model.AgentActivitiesResponse
	_ = json.Unmarshal(b, &r)
	if res.StatusCode != 200 || r.AckSeq != 2 || r.Inserted != 2 {
		t.Fatalf("upload %d %s", res.StatusCode, b)
	}
	// re-send → skipped
	_, b = a.call(t, c, "POST", "/v1/agent/activities", model.AgentActivitiesRequest{SiteSlug: "app", Entries: []model.Activity{a2}})
	_ = json.Unmarshal(b, &r)
	if r.Skipped != 1 || r.AckSeq != 2 {
		t.Fatalf("resend %s", b)
	}
	// tampered
	a3 := mkActivity(3, &a2.Hash, "three")
	a4 := mkActivity(4, &a3.Hash, "four")
	a3.Summary = "tampered"
	_, b = a.call(t, c, "POST", "/v1/agent/activities", model.AgentActivitiesRequest{SiteSlug: "app", Entries: []model.Activity{a3, a4}})
	_ = json.Unmarshal(b, &r)
	if r.Rejected != 2 || r.AckSeq != 2 || r.Inserted != 0 {
		t.Fatalf("tampered %s", b)
	}
	// gap
	a5 := mkActivity(5, &a4.Hash, "five")
	if res, _ = a.call(t, c, "POST", "/v1/agent/activities", model.AgentActivitiesRequest{SiteSlug: "app", Entries: []model.Activity{a5}}); res.StatusCode != 422 {
		t.Fatalf("gap: %d", res.StatusCode)
	}
	if n := len(c.Activities("app")); n != 2 {
		t.Fatalf("stored %d", n)
	}
	if ad, _ := c.Agent("app"); ad.AckSeq != 2 {
		t.Fatalf("ackSeq %d", ad.AckSeq)
	}
}

func TestRelayAndBlock(t *testing.T) {
	c := Start(dep)
	defer c.Close()
	app, db := newSite("app"), newSite("db")
	enroll(t, c, app)
	enroll(t, c, db)
	// db signs its heartbeat; app relays it
	hb, _ := json.Marshal(model.HeartbeatRequest{SiteSlug: "db", AgentVersion: "relayed", AdvertiseHost: "127.0.0.1", AgentPort: 7080})
	ih := db.sign("POST", "/v1/agent/heartbeat", hb, time.Now().Unix(), nonce())
	b64 := base64.StdEncoding.EncodeToString(hb)
	c.BlockSite("db", true)
	res, b := app.call(t, c, "POST", "/v1/agent/relay", model.RelayRequest{ForSite: "db", Request: model.RelayInner{Method: "POST", Path: "/v1/agent/heartbeat", Headers: ih, BodyB64: &b64}})
	if res.StatusCode != 200 {
		t.Fatalf("relay %d %s", res.StatusCode, b)
	}
	var rr model.RelayResponse
	_ = json.Unmarshal(b, &rr)
	if rr.Status != 200 || rr.Headers["X-Opm-Server-Time"] == "" {
		t.Fatalf("inner %+v", rr)
	}
	d, _ := c.Agent("db")
	if d.AgentVersion != "relayed" || d.RelayVia == nil || *d.RelayVia != "app" {
		t.Fatalf("db doc %+v", d)
	}
	// blocked direct call → connection closed
	body, _ := json.Marshal(model.HeartbeatRequest{SiteSlug: "db"})
	req, _ := http.NewRequest("POST", c.URL+"/v1/agent/heartbeat", bytes.NewReader(body))
	for k, v := range db.sign("POST", "/v1/agent/heartbeat", body, time.Now().Unix(), nonce()) {
		req.Header.Set(k, v)
	}
	if res, err := http.DefaultClient.Do(req); err == nil {
		res.Body.Close()
		t.Fatalf("blocked site got a response %d", res.StatusCode)
	}
	c.BlockSite("db", false)
	if res, _ = db.call(t, c, "GET", "/v1/agent/poll?wait=0", nil); res.StatusCode != 200 {
		t.Fatalf("unblocked: %d", res.StatusCode)
	}
	if d, _ = c.Agent("db"); d.RelayVia != nil {
		t.Fatal("relayVia not cleared on direct call")
	}
	if c.Requests("/v1/agent/heartbeat") < 1 {
		t.Fatal("request count")
	}
}

func TestHeadAndStatus(t *testing.T) {
	c := Start(dep)
	defer c.Close()
	a := newSite("app")
	enroll(t, c, a)
	r1, p1 := c.Publish(testConfig("app"), emptyCat(), "r1")
	if p1.Sites["app"].Staging != model.StagingPending {
		t.Fatalf("sites %+v", p1.Sites)
	}
	// status: published → staging → ready
	prog := 0.5
	res, b := a.call(t, c, "POST", "/v1/agent/proposals/"+r1.RevID+"/status", model.ProposalStatusReport{SiteSlug: "app", Staging: model.StagingDownloading, Progress: &prog})
	var sr model.ProposalStatusResponse
	_ = json.Unmarshal(b, &sr)
	if res.StatusCode != 200 || sr.Proposal.Status != model.ProposalStaging {
		t.Fatalf("staging: %d %s", res.StatusCode, b)
	}
	one := 1.0
	_, b = a.call(t, c, "POST", "/v1/agent/proposals/"+r1.RevID+"/status", model.ProposalStatusReport{SiteSlug: "app", Staging: model.StagingReady, Progress: &one, Error: model.Null[string]()})
	_ = json.Unmarshal(b, &sr)
	if sr.Proposal.Status != model.ProposalReady {
		t.Fatalf("ready: %s", b)
	}
	// get proposal + revision
	if res, _ = a.call(t, c, "GET", "/v1/agent/proposals/"+r1.RevID, nil); res.StatusCode != 200 {
		t.Fatalf("get proposal %d", res.StatusCode)
	}
	if res, _ = a.call(t, c, "GET", "/v1/agent/revisions/nope", nil); res.StatusCode != 404 {
		t.Fatalf("get revision %d", res.StatusCode)
	}
	// report head r1 → applied
	res, b = a.call(t, c, "POST", "/v1/agent/head", model.ReportHeadRequest{Revision: r1})
	if res.StatusCode != 200 {
		t.Fatalf("head %d %s", res.StatusCode, b)
	}
	if p, _ := c.Proposal(r1.RevID); p.Status != model.ProposalApplied {
		t.Fatalf("p1 %s", p.Status)
	}
	if h := c.Head(); h == nil || h.RevID != r1.RevID {
		t.Fatal("head")
	}
	// r2 (parent r1) published; a local r3 (parent r1) registered → r2 superseded; then head r3 → applied
	r2, _ := c.Publish(testConfig("app"), emptyCat(), "r2")
	cfg3 := testConfig("app")
	cfg3.Sites[0].Label = "changed"
	h3, _ := canonical.HashCanonical(cfg3)
	ch, _ := canonical.HashCanonical(emptyCat())
	parent := r1.RevID
	rev3 := model.Revision{RevID: c.newULID(), Seq: 2, ParentRevID: &parent, Origin: "agent", Actor: "ops@example.com", CreatedAt: nowTs(), Config: cfg3, Hash: h3, Catalog: emptyCat(), CatalogHash: ch, Summary: "local", Kind: "change"}
	prop3 := model.Proposal{RevID: rev3.RevID, ParentRevID: &parent, Seq: 2, Summary: "local", Origin: "agent", Kind: "change", CreatedAt: nowTs(), CreatedBy: "ops@example.com", Status: model.ProposalPublished, Sites: map[string]model.SiteStaging{"app": {Staging: model.StagingPending}}, UpdatedAt: nowTs()}
	res, b = a.call(t, c, "POST", "/v1/agent/proposals", model.RegisterLocalProposalRequest{Proposal: prop3, Revision: rev3})
	if res.StatusCode != 200 || !strings.Contains(string(b), `"stored":true`) {
		t.Fatalf("register %d %s", res.StatusCode, b)
	}
	if p, _ := c.Proposal(r2.RevID); p.Status != model.ProposalSuperseded {
		t.Fatalf("r2 %s", p.Status)
	}
	// an open r4 on parent r1 published after r3; head r3 makes r4 stale
	r4, _ := c.Publish(testConfig("app"), emptyCat(), "r4")
	_ = r4
	res, b = a.call(t, c, "POST", "/v1/agent/head", model.ReportHeadRequest{Revision: rev3})
	if res.StatusCode != 200 {
		t.Fatalf("head3 %d %s", res.StatusCode, b)
	}
	if p, _ := c.Proposal(rev3.RevID); p.Status != model.ProposalApplied {
		t.Fatalf("p3 %s", p.Status)
	}
	if p, _ := c.Proposal(r4.RevID); p.Status != model.ProposalStale {
		t.Fatalf("r4 %s", p.Status)
	}
	// bad hash
	bad := rev3
	bad.Hash = "sha256:00"
	if res, _ = a.call(t, c, "POST", "/v1/agent/head", model.ReportHeadRequest{Revision: bad}); res.StatusCode != 422 {
		t.Fatalf("bad hash %d", res.StatusCode)
	}
}

func TestBlobsTransfers(t *testing.T) {
	c := Start(dep)
	defer c.Close()
	a := newSite("app")
	enroll(t, c, a)
	h := c.PutBlob([]byte("hello blob"))
	path := "/v1/agent/blobs/sha256:" + h
	req, _ := http.NewRequest("GET", c.URL+path, nil)
	for k, v := range a.sign("GET", path, nil, time.Now().Unix(), nonce()) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Range", "bytes=6-")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || string(b) != "blob" {
		t.Fatalf("range %d %q", res.StatusCode, b)
	}
	// to-site transfer
	tr := c.AddTransferToSite("app", "a.txt", []byte("file"), "n")
	res, b = a.call(t, c, "GET", "/v1/agent/transfers?direction=to-site", nil)
	if res.StatusCode != 200 || !strings.Contains(string(b), tr.TransferID) {
		t.Fatalf("list %s", b)
	}
	if _, b = a.call(t, c, "GET", "/v1/agent/transfers/"+tr.TransferID+"/download", nil); string(b) != "file" {
		t.Fatalf("download %q", b)
	}
	if res, _ = a.call(t, c, "POST", "/v1/agent/transfers/"+tr.TransferID+"/status", model.AgentTransferStatusRequest{Status: "saved-on-server", SavedPath: "/tmp/a.txt", Actor: "x@y.z"}); res.StatusCode != 200 {
		t.Fatal("status")
	}
	// to-cloud upload
	data := []byte("to the cloud")
	res, b = a.call(t, c, "POST", "/v1/agent/transfers", model.AgentCreateTransferRequest{FileName: "b.bin", SizeBytes: int64(len(data)), Sha256: "sha256:" + sha256Hex(data), Actor: "x@y.z"})
	var ct model.CreateTransferResponse
	_ = json.Unmarshal(b, &ct)
	if res.StatusCode != 201 || ct.Upload.PartCount != 1 {
		t.Fatalf("create %d %s", res.StatusCode, b)
	}
	pp := "/v1/agent/uploads/" + ct.Upload.UploadID + "/parts/0"
	res, _ = do(t, c, "PUT", pp, data[:3], a.sign("PUT", pp, data[:3], time.Now().Unix(), nonce()))
	if res.StatusCode != 422 {
		t.Fatalf("short part %d", res.StatusCode)
	}
	if res, b = do(t, c, "PUT", pp, data, a.sign("PUT", pp, data, time.Now().Unix(), nonce())); res.StatusCode != 200 {
		t.Fatalf("part %d %s", res.StatusCode, b)
	}
	res, b = a.call(t, c, "POST", "/v1/agent/uploads/"+ct.Upload.UploadID+"/complete", nil)
	if res.StatusCode != 200 {
		t.Fatalf("complete %d %s", res.StatusCode, b)
	}
	if tt, d, _ := c.Transfer(ct.Transfer.TransferID); tt.Status != "ready" || string(d) != string(data) {
		t.Fatalf("transfer %+v", tt)
	}
}
