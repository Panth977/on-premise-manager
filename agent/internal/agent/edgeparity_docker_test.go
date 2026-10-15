package agent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"opm/agent/internal/docker"
	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
)

const parityHost = "env.client.example.gov.in"

// parityUpstreams are the upstream containers of e2e/fixtures/client-nginx.conf (containerSlug → container port).
var parityUpstreams = []struct {
	slug string
	port int
}{
	{"ez-integration", 8080}, {"ez-dc", 8080}, {"ez-worker", 8080}, {"ez-pg-listen", 8080}, {"ez-terminal", 8080}, {"ez-migrate", 8080},
	{"ez-admin", 8080}, {"northwind-admin", 3000}, {"ez-heatmap", 8080}, {"pg-admin", 80}, {"nodered", 1880}, {"northwind-web", 80},
}

func repoRoot(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// buildTestsrv builds e2e/edge-parity/testsrv (static linux binary) into the local image opm-edge-testsrv:1.
func buildTestsrv(t *testing.T) {
	t.Helper()
	src := filepath.Join(repoRoot(t), "e2e", "edge-parity", "testsrv")
	ctxDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(ctxDir, "testsrv"), ".")
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off", "GOFLAGS=")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build testsrv: %v %s", err, b)
	}
	df, _ := os.ReadFile(filepath.Join(src, "Dockerfile"))
	_ = os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), df, 0o644)
	if b, err := exec.Command("docker", "build", "-q", "-t", "opm-edge-testsrv:1", ctxDir).CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v %s", err, b)
	}
}

// parityEdge imports the client nginx config with the shared importer (node e2e/edge-parity/import.mjs); falls back to
// edges.client of shared/testdata/edge-vectors.json when node / the importer are not available.
func parityEdge(t *testing.T) (model.Edge, string) {
	t.Helper()
	root := repoRoot(t)
	out := filepath.Join(t.TempDir(), "edge.json")
	cmd := exec.Command("node", filepath.Join(root, "e2e", "edge-parity", "import.mjs"), filepath.Join(root, "e2e", "fixtures", "client-nginx.conf"), out)
	if b, err := cmd.CombinedOutput(); err == nil {
		var x struct {
			Edge model.Edge `json:"edge"`
		}
		raw, _ := os.ReadFile(out)
		if err := json.Unmarshal(raw, &x); err == nil && len(x.Edge.Routes) > 0 {
			return x.Edge.Normalized(), "importer (" + strings.TrimSpace(string(b)) + ")"
		}
	} else {
		t.Logf("importer not used (%v): %s — falling back to edge-vectors.json edges.client", err, strings.TrimSpace(string(b)))
	}
	b, err := os.ReadFile(filepath.Join(root, "shared", "testdata", "edge-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Edges map[string]model.Edge `json:"edges"`
	}
	_ = json.Unmarshal(b, &v)
	return v.Edges["client"].Normalized(), "edge-vectors.json (fallback)"
}

func selfSignedPEM(t *testing.T, host string) (string, string, *x509.CertPool) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	kb, _ := x509.MarshalECPrivateKey(k)
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})), pool
}

func tarGz(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

// TestEdgeParityDocker: the client nginx config imported into an OPM edge, applied by a real agent (fake cloud, real
// Docker) with one test container per fixture upstream; every client vector, SSE, websocket, uploads + 413, Range,
// s3 headers, TLS files on the https port + HTTP on the http port. Also round E volume kinds + starter content.
func TestEdgeParityDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dc := docker.New(docker.DefaultHost(""))
	if err := dc.Ping(context.Background()); err != nil {
		t.Skip("no docker: ", err)
	}
	t0 := time.Now()
	buildTestsrv(t)
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".opm-agent-test", "parity-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	dep := "DepParity" + strconv.FormatInt(time.Now().Unix()%1000000000, 10)
	fc := fakecloud.Start(dep)
	defer fc.Close()
	fc.SetAccessKey(testKey)
	ts := loadDockerImage(t, fc, "opm-edge-testsrv:1")
	bb := loadBusybox(t, fc)
	defer func() {
		cs, _ := dc.ContainerList(context.Background(), true, "opm.deployment="+dep)
		for _, c := range cs {
			_ = dc.ContainerRemove(context.Background(), c.ID, true)
		}
		_ = dc.NetworkRemove(context.Background(), "opm_"+dep+"_edge")
	}()
	edge, source := parityEdge(t)
	t.Logf("edge from %s: %d routes", source, len(edge.Routes))
	s := &site{slug: "edge", agentPort: freePort(t), alias: freePort(t), httpP: freePort(t)}
	httpsP := freePort(t)
	edge.HTTPPort, edge.HTTPSPort = s.httpP, &httpsP
	for i := range edge.Routes {
		if edge.Routes[i].RouteID == "x-ez-terminal-real-time" {
			edge.Routes[i].Action.ReadTimeoutSec = 2 // SSE: an upstream idle for 3 s is cut, data every 500 ms is not
		}
	}
	certPEM, keyPEM, pool := selfSignedPEM(t, parityHost)
	certHex := fc.PutBlob([]byte(certPEM))

	// images: one service per container port (same testsrv blobs) + busybox for the volume checks
	mkSpec := func(img testImage, slug string, port int, health string) model.ImageSpec {
		var total int64
		for _, l := range img.layers {
			total += l.SizeBytes
		}
		return model.ImageSpec{ServiceSlug: slug, Version: "1", Digest: "sha256:" + img.manifestHex, ConfigDigest: "sha256:" + img.configHex,
			Platform: "linux/" + runtime.GOARCH, ManifestMediaType: img.mediaType, Layers: img.layers, SizeBytes: total, Kind: "service",
			Ports: []model.ImagePort{{Name: "http", Port: num(float64(port)), Protocol: "tcp"}}, Env: []model.ImageEnv{}, Volumes: []model.ImageVolume{},
			Healthcheck: &model.HealthCheck{Type: "http", PortName: "http", Path: health, ExpectStatus: 200, IntervalSec: 1, TimeoutSec: 2, Retries: 5, StartPeriodSec: 1}, StopTimeoutSec: 2}
	}
	cat := model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	for _, p := range []int{80, 1880, 3000, 8080} {
		slug := fmt.Sprintf("ts%d", p)
		cat.Services[slug] = model.CatalogService{ServiceSlug: slug, Label: slug}
		cat.Images[slug+":1"] = mkSpec(ts, slug, p, "/healthz")
	}
	cat.Services["bb"] = model.CatalogService{ServiceSlug: "bb", Label: "busybox"}
	bbSpec := mkSpec(bb, "bb", 8080, "/ok")
	bbSpec.Command = []string{"sh"}
	// §21 #83: the image declares /data/flows.json as a FILE image volume (the user's node-red case); the single-file
	// volume "single" is mounted on that image volume row (no own mount path)
	fileKind := "file"
	bbSpec.Volumes = []model.ImageVolume{{Name: "flow", MountPath: "/data/flows.json", Kind: &fileKind}}
	cat.Images["bb:1"] = bbSpec

	// round E volumes: "red" (dir) seeded from 2 files (one rendered) + "arc" (dir) seeded from a .tar.gz + "one" (a
	// single-file volume, seeded) + "single" (a single file, no seed)
	flows := []byte(`[{"id":"flow-{{ self.slug }}"}]`)
	settings := []byte("module.exports = {}\n")
	flowsHex, settingsHex := fc.PutBlob(flows), fc.PutBlob(settings)
	arch := tarGz(t, map[string]string{"lib/a.txt": "from archive", "b.txt": "bee"})
	archHex := fc.PutBlob(arch)
	dir, file := "dir", "file"
	mkConfig := func(env string, redSeed model.VolumeSeed) model.Config {
		var cs []model.Container
		for _, u := range parityUpstreams {
			cs = append(cs, model.Container{ContainerSlug: u.slug, ServiceSlug: fmt.Sprintf("ts%d", u.port), Version: "1", Enabled: true,
				Env:   []model.ContainerEnv{{Name: "PORT", Value: ptr(strconv.Itoa(u.port))}, {Name: "NAME", Value: ptr(u.slug)}},
				Ports: []model.ContainerPort{{PortName: "http", HostIP: "0.0.0.0", Edge: true}}, Volumes: []model.ContainerMount{}, Restart: "always", Depends: []model.ContainerRef{}})
		}
		cs = append(cs, model.Container{ContainerSlug: "vol", ServiceSlug: "bb", Version: "1", Enabled: true, Env: []model.ContainerEnv{{Name: "GEN", Value: ptr(env)}},
			Ports: []model.ContainerPort{{PortName: "http", HostIP: "0.0.0.0", Edge: false}},
			Volumes: []model.ContainerMount{
				{MountPath: ptr("/red"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "red"}},
				{MountPath: ptr("/arc"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "arc"}},
				{MountPath: ptr("/data/one.json"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "one"}},
				{ImageVolume: ptr("flow"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "single"}},
			},
			Command: []string{"sh", "-c", `mkdir -p /www && echo ok > /www/ok && httpd -f -p 8080 -h /www`}, Restart: "always", Depends: []model.ContainerRef{}})
		return model.Config{
			Variables: []model.Variable{},
			Secrets:   []model.SecretDef{{SecretSlug: "client-key", Label: "Key", Required: true, Value: ptr(keyPEM)}},
			Files: []model.FileDef{
				{FileSlug: "client-crt", Label: "Cert", Mime: "application/x-pem-file", Editor: "text", MaxBytes: 65536, Render: model.Some("none"), Content: &model.BlobRef{Sha256: "sha256:" + certHex, SizeBytes: int64(len(certPEM))}},
				{FileSlug: "flows", Label: "Flows", Mime: "application/json", Editor: "text", MaxBytes: 65536, Render: model.Some("expressions"), Content: &model.BlobRef{Sha256: "sha256:" + flowsHex, SizeBytes: int64(len(flows))}},
				{FileSlug: "settings", Label: "Settings", Mime: "text/plain", Editor: "text", MaxBytes: 65536, Render: model.Some("none"), Content: &model.BlobRef{Sha256: "sha256:" + settingsHex, SizeBytes: int64(len(settings))}},
			},
			Sites: []model.Site{{SiteSlug: "edge", Label: "Edge", Config: model.SiteConfig{Host: ptr("127.0.0.1"), AgentPort: s.agentPort, AgentGuiAliasPort: &s.alias,
				Volumes: &[]model.SiteVolume{
					{VolumeSlug: "red", Label: "red", Backup: true, Kind: &dir, Seed: model.Some(redSeed)},
					{VolumeSlug: "arc", Label: "arc", Backup: true, Kind: &dir, Seed: model.Some(model.VolumeSeed{Kind: "archive", BlobSha256: model.Sha256("sha256:" + archHex), Name: "seed.tar.gz", Size: int64(len(arch))})},
					{VolumeSlug: "one", Label: "one", Backup: true, Kind: &file, Seed: model.Some(model.VolumeSeed{Kind: "file", FileSlug: "settings"})},
					{VolumeSlug: "single", Label: "single", Backup: true, Kind: &file, Seed: model.Null[model.VolumeSeed]()},
				},
				Containers: cs, Depends: []model.SiteRef{}, Edge: edge}}},
		}
	}
	seed1 := model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "flows.json", FileSlug: "flows"}, {Path: "lib/settings.js", FileSlug: "settings"}}}

	// agent
	dirA := filepath.Join(root, "edge")
	tok := filepath.Join(dirA, "install-token")
	_ = os.MkdirAll(dirA, 0o700)
	_ = os.WriteFile(tok, []byte(fc.CreateInstallToken("edge")), 0o600)
	acfg := model.AgentConfigFile{APIBase: fc.URL, DeploymentID: dep, SiteSlug: "edge", DataDir: filepath.Join(dirA, "data"), AgentPort: s.agentPort,
		GuiAliasPort: model.Some(s.alias), AdvertiseHost: "127.0.0.1", InstallTokenFile: tok,
		EdgeHTTPPort: model.Null[int](), ContainerNames: "prefixed"} // §21 #88: test containers share the host
	a, err := New(Options{Config: acfg, Timings: fastTimings(), ApproveWait: time.Second, CompleteTimeout: 90 * time.Second, LogLevel: "debug", DisableSchedule: true})
	if err != nil {
		t.Fatal(err)
	}
	s.a = a
	jar, _ := cookiejar.New(nil)
	s.cl = &http.Client{Jar: jar, Timeout: 60 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "enroll", 30*time.Second, func() bool { _, ok := fc.Agent("edge"); return ok })
	waitFor(t, "key hash", 20*time.Second, func() bool { h, _ := a.keyHash(); return h != "" })
	if c, _ := s.api(t, "POST", "/_opm/api/login", model.LoginRequest{AccessKey: testKey}, nil); c != 200 {
		t.Fatalf("login %d", c)
	}
	var ar model.ActorResponse
	if c, b := s.api(t, "POST", "/_opm/api/actor", model.ActorRequest{Email: "ops@example.com"}, &ar); c != 200 {
		t.Fatalf("actor %d %s", c, b)
	}
	s.actor = ar.ActorToken
	approve := func(revID string) model.UpdateRun {
		t.Helper()
		var det model.ProposalDetailResponse
		waitFor(t, "gate open", 240*time.Second, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/proposals/"+revID, nil, &det)
			if c == 200 && det.Proposal.Sites["edge"].Staging == model.StagingFailed {
				t.Fatalf("staging failed: %v", det.Proposal.Sites["edge"].Error)
			}
			return c == 200 && det.Gate.Open
		})
		var st model.ApproveStartResponse
		if c, b := s.api(t, "POST", "/_opm/api/proposals/"+revID+"/approve/start", nil, &st); c != 201 {
			t.Fatalf("approve/start %d %s", c, b)
		}
		time.Sleep(1100 * time.Millisecond)
		var res model.ApproveResponse
		if c, b := s.api(t, "POST", "/_opm/api/proposals/"+revID+"/approve", model.ApproveRequest{IntentID: st.Intent.IntentID, Typed: "ACCEPT"}, &res); c != 201 {
			t.Fatalf("approve %d %s", c, b)
		}
		var run model.UpdateRun
		waitFor(t, "update finished", 300*time.Second, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/updates/"+res.UpdateID, nil, &run)
			return c == 200 && run.FinishedAt != nil
		})
		if run.Result == nil || !run.Result.Ok {
			t.Fatalf("update failed: %+v", run.Result)
		}
		return run
	}
	rev1, _ := fc.Publish(mkConfig("1", seed1), cat, "edge parity")
	approve(rev1.RevID)
	t.Logf("applied in %v (incl. image build)", time.Since(t0).Round(time.Second))

	base := fmt.Sprintf("http://127.0.0.1:%d", s.httpP)
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 60 * time.Second}
	do := func(method, path string, body io.Reader, size int64, hdr map[string]string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, body)
		req.Host = parityHost
		if body != nil {
			req.ContentLength = size
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	type echoT struct {
		Name, URI, Host string
		Headers         map[string]string
	}
	echo := func(b string) echoT {
		var e echoT
		_ = json.Unmarshal([]byte(b), &e)
		return e
	}

	// 1. every client vector of shared/testdata/edge-vectors.json, on the real edge
	vb, _ := os.ReadFile(filepath.Join(repoRoot(t), "shared", "testdata", "edge-vectors.json"))
	var vec struct {
		Cases []struct {
			Edge, URL, Method string
			BodyBytes         *int64
			Upgrade           bool
			Expect            struct {
				Status      int
				Kind        string
				Location    *string
				UpstreamURL *string `json:"upstreamUrl"`
			}
		}
	}
	_ = json.Unmarshal(vb, &vec)
	nVec := 0
	for _, c := range vec.Cases {
		if c.Edge != "client" {
			continue
		}
		u, _ := url.Parse(c.URL)
		path := u.RequestURI()
		method := c.Method
		if method == "" {
			method = "GET"
		}
		if u.Scheme == "https" {
			continue // checked below with the TLS client
		}
		nVec++
		switch {
		case c.Upgrade && c.Expect.Status == 200:
			cfgW, _ := websocket.NewConfig("ws://"+parityHost+path, "http://"+parityHost)
			conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", s.httpP))
			if err != nil {
				t.Fatal(err)
			}
			ws, err := websocket.NewClient(cfgW, conn)
			if err != nil {
				t.Fatalf("websocket %s: %v", path, err)
			}
			for i := 0; i < 3; i++ {
				msg := fmt.Sprintf("m%d", i)
				_ = websocket.Message.Send(ws, msg)
				var got string
				if err := websocket.Message.Receive(ws, &got); err != nil || got != msg {
					t.Fatalf("websocket echo %q %v", got, err)
				}
			}
			ws.Close()
			continue
		case c.Upgrade:
			r, _ := do(method, path, nil, 0, map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==", "Sec-WebSocket-Version": "13"})
			if r.StatusCode != c.Expect.Status {
				t.Errorf("%s upgrade: %d want %d", path, r.StatusCode, c.Expect.Status)
			}
			continue
		}
		var body io.Reader
		var size int64
		if c.BodyBytes != nil {
			size = *c.BodyBytes
			body = io.LimitReader(zeros{}, size)
		}
		r, b := do(method, path, body, size, nil)
		if r.StatusCode != c.Expect.Status {
			t.Errorf("%s %s: %d want %d (%s)", method, path, r.StatusCode, c.Expect.Status, b)
			continue
		}
		if c.Expect.Location != nil && r.Header.Get("Location") != *c.Expect.Location {
			t.Errorf("%s: Location %q want %q", path, r.Header.Get("Location"), *c.Expect.Location)
		}
		if c.Expect.Kind == "proxy" && strings.Contains(path, "/real-time/") {
			if r.Header.Get("Content-Type") != "text/event-stream" || !strings.HasPrefix(b, "data: 0") {
				t.Errorf("%s: SSE upstream: %v %q", path, r.Header, b)
			}
		} else if c.Expect.Kind == "proxy" && c.Expect.UpstreamURL != nil {
			want, _ := url.Parse(*c.Expect.UpstreamURL)
			e := echo(b)
			if e.Name != want.Hostname() || e.URI != want.RequestURI() {
				t.Errorf("%s: upstream %s%s want %s%s", path, e.Name, e.URI, want.Hostname(), want.RequestURI())
			}
		}
	}
	t.Logf("client vectors on the real edge: %d", nVec)

	// 2. request headers (pg-admin: X-Script-Name; Host kept; X-Real-IP / XFF / XFP)
	r, b := do("GET", "/x/pg-admin/browser/", nil, 0, nil)
	if e := echo(b); r.StatusCode != 200 || e.Headers["X-Script-Name"] != "/x/pg-admin" || e.Host != parityHost || e.Headers["X-Real-Ip"] == "" || e.Headers["X-Forwarded-For"] == "" || e.Headers["X-Forwarded-Proto"] != "http" {
		t.Errorf("pg-admin headers: %d %s", r.StatusCode, b)
	}
	// 3. s3: hide + set; server level no-cache headers elsewhere; Range
	r, _ = do("GET", "/x/ez-terminal/s3/file.bin", nil, 0, nil)
	if r.StatusCode != 200 || strings.Join(r.Header.Values("Cache-Control"), "|") != "public, max-age=3600, must-revalidate" || r.Header.Get("Pragma") != "" || r.Header.Get("Expires") != "" {
		t.Errorf("s3 headers: %d %v", r.StatusCode, r.Header)
	}
	r, _ = do("GET", "/x/ez-admin/z", nil, 0, nil)
	if r.Header.Get("Cache-Control") != "no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0" || r.Header.Get("Pragma") != "no-cache" || r.Header.Get("Expires") != "0" {
		t.Errorf("server-level headers: %v", r.Header)
	}
	r, b = do("GET", "/x/ez-admin/s3/file.bin", nil, 0, map[string]string{"Range": "bytes=10-19"})
	if r.StatusCode != 206 || r.Header.Get("Content-Range") != "bytes 10-19/1000" || b != "0123456789" {
		t.Errorf("range: %d %s %q", r.StatusCode, r.Header.Get("Content-Range"), b)
	}
	// 4. SSE: first event < 1 s, data every 500 ms not cut (read timeout 2 s), idle 3 s cut
	{
		start := time.Now()
		req, _ := http.NewRequest("GET", base+"/x/ez-terminal/real-time/stream", nil)
		req.Host = parityHost
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(resp.Body)
		line, _ := br.ReadString('\n')
		if line != "data: 0\n" || time.Since(start) > time.Second {
			t.Errorf("SSE first event %q after %v", line, time.Since(start))
		}
		rest, _ := io.ReadAll(br)
		resp.Body.Close()
		if !strings.Contains(string(rest), "data: 9") {
			t.Errorf("SSE stream cut: %q", rest)
		}
		req, _ = http.NewRequest("GET", base+"/x/ez-terminal/real-time/stream?gap=3s", nil)
		req.Host = parityHost
		resp, err = cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		rest, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(rest), "data: 1") {
			t.Errorf("idle SSE not cut: %q", rest)
		}
	}
	// 5. uploads: 100 MB streamed; Content-Length over 100 MiB → 413; chunked over → 413
	{
		const size = 100 * 1024 * 1024
		r, b := do("PUT", "/x/ez-terminal/s3/upload", io.LimitReader(zeros{}, size), size, nil)
		if r.StatusCode != 200 || !strings.Contains(b, fmt.Sprintf(`"bytes":%d`, size)) {
			t.Errorf("100 MB upload: %d %s", r.StatusCode, b)
		}
		r, _ = do("PUT", "/x/ez-terminal/s3/upload", io.LimitReader(zeros{}, 104857601), 104857601, nil)
		if r.StatusCode != 413 {
			t.Errorf("413 by Content-Length: %d", r.StatusCode)
		}
		r, _ = do("POST", "/x/ez-admin/upload", io.NopCloser(io.LimitReader(zeros{}, 104857601)), -1, nil)
		if r.StatusCode != 413 {
			t.Errorf("413 chunked: %d", r.StatusCode)
		}
	}
	// 6. TLS files on the https port + plain HTTP on the http port, no redirect
	{
		hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: parityHost},
			DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(c, "tcp", fmt.Sprintf("127.0.0.1:%d", httpsP))
			}}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		var resp *http.Response
		waitFor(t, "https", 20*time.Second, func() bool {
			var err error
			resp, err = hc.Get("https://" + parityHost + "/x/admin/login")
			return err == nil
		})
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if e := echo(string(b)); resp.StatusCode != 200 || e.Name != "northwind-admin" || e.URI != "/login" || e.Headers["X-Forwarded-Proto"] != "https" {
			t.Errorf("https: %d %s", resp.StatusCode, b)
		}
		if r, b := do("GET", "/x/admin/login", nil, 0, nil); r.StatusCode != 200 || echo(b).URI != "/login" {
			t.Errorf("http on the same host: %d %s", r.StatusCode, b)
		}
	}

	// 7. round E volumes: seeded dir (2 files, one rendered) + archive + single-file seeded + single-file empty
	vname := "opm_" + dep + "_edge_vol"
	cat1 := func(p string) string { return execCat(t, dc, vname, p) }
	if got := cat1("/red/flows.json"); got != `[{"id":"flow-edge"}]` {
		t.Fatalf("seeded flows.json %q", got)
	}
	if got := cat1("/red/lib/settings.js"); got != "module.exports = {}" {
		t.Fatalf("seeded settings %q", got)
	}
	if got := cat1("/arc/lib/a.txt"); got != "from archive" {
		t.Fatalf("archive seed %q", got)
	}
	if got := cat1("/data/one.json"); got != "module.exports = {}" {
		t.Fatalf("file seed %q", got)
	}
	vc, _ := dc.ContainerInspect(context.Background(), vname)
	exec1 := func(cmd string) (int, string) {
		code, out, err := dc.Exec(context.Background(), vc.ID, []string{"sh", "-c", cmd})
		if err != nil {
			t.Fatal(err)
		}
		return code, out
	}
	if code, out := exec1(`echo edited > /red/flows.json && echo single-data > /data/flows.json`); code != 0 {
		t.Fatalf("container writes: %d %s", code, out)
	}
	// Node-RED-like save (temp file + rename over the mounted file) on a single-file volume FAILS (EBUSY)
	code, out := exec1(`echo new > /data/flows.json.tmp && mv /data/flows.json.tmp /data/flows.json`)
	t.Logf("rename over a single-file volume: exit %d %q", code, strings.TrimSpace(out))
	if code == 0 {
		t.Errorf("rename over a bind-mounted file should fail (EBUSY), got exit 0")
	}
	if got := cat1("/data/flows.json"); got != "single-data" {
		t.Fatalf("single file after failed rename %q", got)
	}
	// a new revision with another seed + a changed env (recreate): seeded data untouched, single file persisted
	rev2, _ := fc.Publish(mkConfig("2", model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "other.txt", FileSlug: "settings"}}}), cat, "seed changed")
	approve(rev2.RevID)
	vc2, _ := dc.ContainerInspect(context.Background(), vname)
	if vc2.ID == vc.ID {
		t.Fatal("vol was not recreated")
	}
	vc = vc2
	if got := cat1("/red/flows.json"); got != "edited" {
		t.Fatalf("changed seed overwrote data: %q", got)
	}
	if code, _ := exec1(`test -e /red/other.txt`); code == 0 {
		t.Fatal("changed seed applied to an existing volume")
	}
	if got := cat1("/data/flows.json"); got != "single-data" {
		t.Fatalf("single-file volume across a recreate: %q", got)
	}
	// manual snapshot holds the single-file volume
	var cs model.CreateSnapshotResponse
	if c, b := s.api(t, "POST", "/_opm/api/snapshots", model.CreateSnapshotRequest{Note: "parity"}, &cs); c != 202 && c != 201 {
		t.Fatalf("snapshot: %d %s", c, b)
	}
	waitFor(t, "snapshot part", 60*time.Second, func() bool { return a.hasSnapshotPart(cs.SnapshotID) })
	part, err := a.loadPart(cs.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range part.site.Volumes {
		found = found || v.VolumeSlug == "single"
	}
	if !found {
		t.Fatalf("single-file volume not in the snapshot: %+v", part.site.Volumes)
	}
	// reset to starter content: 60 s intent (1 s in the test) + ACCEPT → snapshot first, then the seed again
	var st model.ApproveStartResponse
	if c, b := s.api(t, "POST", "/_opm/api/sites/edge/volumes/red/reset/start", nil, &st); c != 201 || st.Intent.Kind != "volume-reset" || st.Intent.Volume == nil {
		t.Fatalf("reset/start: %d %s", c, b)
	}
	if c, _ := s.api(t, "POST", "/_opm/api/sites/edge/volumes/single/reset/start", nil, nil); c != 422 {
		t.Fatalf("reset of a volume without seed: %d", c)
	}
	time.Sleep(1100 * time.Millisecond)
	var rr model.ResetVolumeResponse
	if c, b := s.api(t, "POST", "/_opm/api/sites/edge/volumes/red/reset", model.ApproveRequest{IntentID: st.Intent.IntentID, Typed: "ACCEPT"}, &rr); c != 201 {
		t.Fatalf("reset: %d %s", c, b)
	}
	if rr.SnapshotID == "" || rr.Entries != 1 || !a.hasSnapshotPart(rr.SnapshotID) {
		t.Fatalf("reset response %+v", rr)
	}
	waitFor(t, "vol running again", 30*time.Second, func() bool {
		cj, err := dc.ContainerInspect(context.Background(), vname)
		return err == nil && cj.State.Running
	})
	if got := cat1("/red/other.txt"); got != "module.exports = {}" {
		t.Fatalf("after reset (current seed): %q", got)
	}
	if code, _ := exec1(`test -e /red/flows.json`); code == 0 {
		t.Fatal("reset did not wipe the volume")
	}
	var vols model.SiteVolumesResponse
	if c, _ := s.api(t, "GET", "/_opm/api/sites/edge/volumes", nil, &vols); c != 200 {
		t.Fatal("volumes api")
	}
	for _, v := range vols.Volumes {
		if v.VolumeSlug == "red" && (v.Kind != "dir" || v.Seeded == nil || v.Seed == nil) {
			t.Errorf("red status %+v", v)
		}
		if v.VolumeSlug == "single" && (v.Kind != "file" || v.Seeded != nil) {
			t.Errorf("single status %+v", v)
		}
	}
	t.Logf("edge parity docker test: %v", time.Since(t0).Round(time.Second))
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
