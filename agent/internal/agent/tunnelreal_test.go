package agent

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	_ "opm/agent/internal/cloud/routes" // the handlers of the real cloud
	"opm/agent/internal/sign"
)

// The real cloud (Dispatch, signature, nonces, blob streaming with redirects on) behind the recording tab.
func TestTunnelRealCloud(t *testing.T) {
	dir := t.TempDir()
	pub, sk, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	strict := true
	p, err := driver.Open(driver.Config{DataDir: dir, PublicURL: "http://x.test", Strict: &strict})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	cloud.SetPorts(p)
	srv := httptest.NewServer(cloud.NewHandler(&cloud.CloudConfig{BodyLimit: 64 << 20, ConsoleURL: "http://x.test"}, p))
	t.Cleanup(srv.Close)
	t.Setenv("OPM_DATA_DIR", dir)
	t.Setenv("OPM_API_BASE", srv.URL)
	t.Setenv("OPM_RELEASE_PRIVATE_KEY", base64.StdEncoding.EncodeToString(sk.Seed()))
	t.Setenv("OPM_BLOB_REDIRECT", "1")

	a := newTestAgent(t)
	a.cfg.APIBase = srv.URL
	a.cfg.ReleasePublicKey = base64.StdEncoding.EncodeToString(pub)
	a.dep = "D1"
	a.enrolled = true
	a.directFailAt = time.Now() // no direct route for the next 15 s
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(cloud.Col.Deployments().Doc("D1").Set(doc.Obj("deploymentId", "D1", "label", secretMarker, "deleted", false, "agentEpoch", 1.0)))
	must(cloud.Col.Agents("D1").Doc(a.site).Set(doc.Obj("publicKey", sign.PublicKeyB64(a.pub), "enrolledAt", "x")))
	data := bytes.Repeat([]byte(secretMarker+"-"), 300_000)
	must(cloud.Blobs().Put("transfers/t1", data, driver.PutOptions{ContentType: "application/octet-stream"}))
	must(cloud.Col.Transfers("D1").Doc("t1").Set(doc.Obj("siteSlug", a.site, "direction", "to-site", "status", "ready", "storagePath", "transfers/t1", "fileName", "x.bin", "sha256", "sha256:ab")))

	b := startTab(t, a)
	ctx := callCtx(t)
	poll, err := a.cloudCall(ctx, "GET", "/v1/agent/poll?wait=0", nil, callOpts{timeout: 5 * time.Second})
	must(err)
	if poll.Status != 200 || !strings.Contains(string(poll.Body), `"deploymentId":"D1"`) {
		t.Fatalf("poll %d %s", poll.Status, poll.Body)
	}
	dl, err := a.cloudCall(ctx, "GET", "/v1/agent/transfers/t1/download", nil, callOpts{timeout: 15 * time.Second})
	must(err)
	if dl.Status != 200 || !bytes.Equal(dl.Body, data) {
		t.Fatalf("download %d %d (a 302 here would hand the tab a signed URL)", dl.Status, len(dl.Body))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range []string{secretMarker, "/v1/agent/poll", "/v1/agent/transfers", "X-OPM-Sig", "X-OPM-Nonce", "X-OPM-Site", "x-opm-sig"} {
		if bytes.Contains(b.seen.Bytes(), []byte(s)) {
			t.Errorf("the tab saw %q", s)
		}
	}
	for _, u := range b.urls {
		if u != "POST "+srv.URL+"/v1/agent/sealed" && u != "GET "+srv.URL+"/v1/agent/tunnel-key" {
			t.Errorf("the tab was asked for %s", u)
		}
	}
}
