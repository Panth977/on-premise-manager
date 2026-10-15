package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	mrand "math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
)

// ───────────────────────── harness

type testImage struct {
	manifestHex, configHex string
	layers                 []model.ImageLayer
	mediaType              string
}

// loadBusybox exports busybox:1.36 with `docker save` and returns its blobs.
func loadBusybox(t *testing.T, fc *fakecloud.Cloud) testImage {
	t.Helper()
	return loadDockerImage(t, fc, "busybox:1.36")
}

// loadDockerImage exports a local image with `docker save` and puts its blobs into the fake cloud.
func loadDockerImage(t *testing.T, fc *fakecloud.Cloud, ref string) testImage {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "img.tar")
	if b, err := exec.Command("docker", "save", ref, "-o", out).CombinedOutput(); err != nil {
		t.Skipf("docker save %s: %v %s", ref, err, b)
	}
	f, _ := os.Open(out)
	defer f.Close()
	tr := tar.NewReader(f)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = b
	}
	var man []struct {
		Config string
		Layers []string
	}
	if err := json.Unmarshal(files["manifest.json"], &man); err != nil || len(man) == 0 {
		t.Fatal("bad docker save output")
	}
	ti := testImage{configHex: strings.TrimPrefix(man[0].Config, "blobs/sha256/"), mediaType: "application/vnd.oci.image.manifest.v1+json"}
	// find the platform manifest that references the config
	for name, b := range files {
		if !strings.HasPrefix(name, "blobs/sha256/") {
			continue
		}
		var m struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers []struct {
				Digest    string `json:"digest"`
				Size      int64  `json:"size"`
				MediaType string `json:"mediaType"`
			} `json:"layers"`
		}
		if json.Unmarshal(b, &m) == nil && m.Config.Digest == "sha256:"+ti.configHex {
			ti.manifestHex = strings.TrimPrefix(name, "blobs/sha256/")
			for _, l := range m.Layers {
				ti.layers = append(ti.layers, model.ImageLayer{Digest: l.Digest, SizeBytes: l.Size, MediaType: l.MediaType})
			}
		}
	}
	if ti.manifestHex == "" {
		// docker-archive only: synthesize layer list from manifest.json
		for _, l := range man[0].Layers {
			h := strings.TrimPrefix(l, "blobs/sha256/")
			ti.layers = append(ti.layers, model.ImageLayer{Digest: "sha256:" + h, SizeBytes: int64(len(files[l])), MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"})
		}
		mb, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]any{"digest": "sha256:" + ti.configHex}})
		ti.manifestHex = fc.PutBlob(mb)
	} else {
		fc.PutBlob(files["blobs/sha256/"+ti.manifestHex])
	}
	fc.PutBlob(files["blobs/sha256/"+ti.configHex])
	for _, l := range ti.layers {
		fc.PutBlob(files["blobs/sha256/"+strings.TrimPrefix(l.Digest, "sha256:")])
	}
	return ti
}

var (
	portMu    sync.Mutex
	portTaken = map[int]bool{}
)

// freePort picks a free TCP port OUTSIDE the OS ephemeral range (macOS / Linux hand out 32768–65535 to outgoing
// connections, so a port picked with ":0" could be taken again before the agent binds it): a random port in
// 20000–29999 that is free on 0.0.0.0 and 127.0.0.1 (TCP and UDP) and not handed out before in this process.
func freePort(t *testing.T) int {
	t.Helper()
	portMu.Lock()
	defer portMu.Unlock()
	for i := 0; i < 2000; i++ {
		p := 20000 + mrand.Intn(10000)
		if portTaken[p] {
			continue
		}
		ok := true
		for _, addr := range []string{fmt.Sprintf("0.0.0.0:%d", p), fmt.Sprintf("127.0.0.1:%d", p)} {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				ok = false
				break
			}
			l.Close()
		}
		if ok {
			if u, err := net.ListenPacket("udp", fmt.Sprintf(":%d", p)); err == nil {
				u.Close()
			} else {
				ok = false
			}
		}
		if ok {
			portTaken[p] = true
			return p
		}
	}
	t.Fatal("no free port in 20000–29999")
	return 0
}

func fastTimings() Timings {
	tm := DefaultTimings()
	tm.PollWait = 2
	tm.Heartbeat = 2 * time.Second
	tm.Ping = time.Second
	tm.PingTimeout = 2 * time.Second
	tm.ViewExchange = 2 * time.Second
	tm.Actors = 3 * time.Second
	tm.Outbox = 300 * time.Millisecond
	tm.Pipe = time.Second
	tm.Sampler = 2 * time.Second
	tm.Rollup = 10 * time.Second
	tm.Reachable = 10 * time.Second
	tm.Unreachable = 20 * time.Second
	tm.StepPoll = 300 * time.Millisecond
	tm.Monitor = time.Second
	tm.EnrollRetry = time.Second
	tm.FetchPoll = 500 * time.Millisecond
	tm.StageRetryMin = time.Second
	tm.StageRetryMax = 5 * time.Second
	tm.HaveCache = time.Second
	tm.OldPortGrace = 5 * time.Second
	return tm
}

type site struct {
	a                       *Agent
	slug                    string
	agentPort, alias, httpP int
	cl                      *http.Client
	actor                   string
	cancel                  context.CancelFunc
	done                    chan struct{}
}

func (s *site) api(t *testing.T, method, path string, body any, out any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if b, ok := body.([]byte); ok {
		rd = bytes.NewReader(b)
	} else if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", s.alias, path), rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.actor != "" {
		req.Header.Set("Authorization", "Actor "+s.actor)
	}
	resp, err := s.cl.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: decode %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode, b
}

func waitFor(t *testing.T, what string, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func httpGet(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 10 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func ptr[T any](v T) *T { return &v }

func num(v float64) model.NumOrExpr  { return model.NumOrExpr{Num: &v} }
func exprN(s string) model.NumOrExpr { return model.NumOrExpr{Expr: &s} }

// ───────────────────────── the test

const testKey = "ABCDEFGHIJKLMNOPQRSTUVWX" // 24 chars A–Z2–7

func TestTwoSiteMesh(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dc := docker.New(docker.DefaultHost(""))
	if err := dc.Ping(context.Background()); err != nil {
		t.Skip("no docker: ", err)
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".opm-agent-test", time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	dep := "DepMeshTest" + strconv.FormatInt(time.Now().Unix()%1000000000, 10)
	fc := fakecloud.Start(dep)
	defer fc.Close()
	fc.SetAccessKey(testKey)
	img := loadBusybox(t, fc)
	// remove a stale opm/cfg tag so docker load runs
	_ = dc.ImageRemove(context.Background(), "opm/cfg:"+img.configHex)
	defer func() {
		cs, _ := dc.ContainerList(context.Background(), true, "opm.deployment="+dep)
		for _, c := range cs {
			_ = dc.ContainerRemove(context.Background(), c.ID, true)
		}
		_ = dc.NetworkRemove(context.Background(), "opm_"+dep+"_app")
		_ = dc.NetworkRemove(context.Background(), "opm_"+dep+"_db")
		for _, ref := range []string{"api:1", "web:1", "web:bad", "kv:1"} {
			_ = dc.ImageRemove(context.Background(), ref)
		}
	}()

	// catalog: three services on the same busybox image
	svcAPI, svcWeb, svcKV := "api", "web", "kv"
	spec := func(sid, slug, tag, hpath string, vols []model.ImageVolume) model.ImageSpec {
		var total int64
		for _, l := range img.layers {
			total += l.SizeBytes
		}
		hc := &model.HealthCheck{Type: "http", PortName: "http", Path: hpath, ExpectStatus: 200, IntervalSec: 1, TimeoutSec: 2, Retries: 3, StartPeriodSec: 1}
		if vols == nil {
			vols = []model.ImageVolume{}
		}
		_ = sid
		return model.ImageSpec{ServiceSlug: slug, Version: tag, Digest: "sha256:" + img.manifestHex, ConfigDigest: "sha256:" + img.configHex,
			Platform: "linux/" + runtime.GOARCH, ManifestMediaType: img.mediaType, Layers: img.layers, SizeBytes: total, Kind: "service",
			Entrypoint: nil, Command: []string{"sh"}, Ports: []model.ImagePort{{Name: "http", Port: num(8080), Protocol: "tcp", Description: ""}},
			Env: []model.ImageEnv{}, Volumes: vols, Healthcheck: hc, StopTimeoutSec: 2}
	}
	cat := model.RevisionCatalog{
		Services: map[string]model.CatalogService{
			svcAPI: {ServiceSlug: svcAPI, Label: "API", Icon: icon("server")},
			svcWeb: {ServiceSlug: svcWeb, Label: "Web", Icon: nil},
			svcKV:  {ServiceSlug: svcKV, Label: "KV", Icon: icon("lucide:database")},
		},
		Images: map[string]model.ImageSpec{
			svcAPI + ":1":   spec(svcAPI, "api", "1", "/healthz", nil),
			svcWeb + ":1":   spec(svcWeb, "web", "1", "/index.html", nil),
			svcWeb + ":bad": spec(svcWeb, "web", "bad", "/missing", nil),
			svcKV + ":1":    spec(svcKV, "kv", "1", "/count", []model.ImageVolume{{Name: "data", MountPath: "/data", Description: ""}}),
		},
		NotFoundPages: map[string]model.CatalogNotFoundPage{"NfpTest0000000000001": {NotFoundPageID: "NfpTest0000000000001", Label: "Test 404", HTMLCode: "<h1>Test 404</h1>"}},
	}
	// phase U: the kv data volume is mounted at a path built from a choice env (default "data" → /data)
	kvImg := cat.Images[svcKV+":1"]
	kvImg.Env = []model.ImageEnv{{Name: "DATA_DIR", Value: &model.ImageEnvValue{Str: ptr("data")}, Options: model.Some([]string{"data", "alt"}), Description: ""}}
	kvImg.Volumes = []model.ImageVolume{{Name: "data", MountPath: "/{{ container.env.DATA_DIR }}", Description: ""}}
	cat.Images[svcKV+":1"] = kvImg
	indexHTML := []byte("<h1>hello mesh</h1>\n")
	idxHex := fc.PutBlob(indexHTML)

	app := &site{slug: "app", agentPort: freePort(t), alias: freePort(t), httpP: freePort(t)}
	db := &site{slug: "db", agentPort: freePort(t), alias: freePort(t), httpP: freePort(t)}
	kvPort := freePort(t)

	web2Port := freePort(t)
	// round D part 2: a template file rendered ONCE per site (secret inside) mounted by web + web-2 (one host copy), and a
	// JSON template with a custom "./" location
	confSrc := []byte("site={{ self.slug }} token={{ secret.token }} db={{ site.db.host }}:{{ site.db.port.kv.http }} lit={{ \"{{\" }}x}}\n")
	confHex := fc.PutBlob(confSrc)
	jsonSrc := []byte(`{"site": "{{ self.slug }}", "db": "{{ site.db.host }}"}`)
	jsonHex := fc.PutBlob(jsonSrc)

	// round D: declared site volumes. db: kv-data (backup) mounted at the kv image volume + scratch (backup OFF) at
	// /scratch; app: shared (backup) mounted at /shared by the containers in sharedOn.
	mkConfigD := func(theme, webTag string, sharedOn ...string) model.Config {
		sh := func(s string) []string { return []string{"sh", "-c", s} }
		port := func(name string, hp model.NumOrExpr, ip string) []model.ContainerPort {
			return []model.ContainerPort{{PortName: name, HostPort: hp, HostIP: ip, Edge: true}}
		}
		fileMount := func(slug, path string) model.ContainerMount {
			return model.ContainerMount{MountPath: ptr(path), Source: model.VolumeSource{Kind: "file", FileSlug: slug}, ReadOnly: true}
		}
		shared := func(c string, ms ...model.ContainerMount) []model.ContainerMount {
			out := append([]model.ContainerMount{}, ms...)
			for _, x := range sharedOn {
				if x == c {
					out = append(out, model.ContainerMount{MountPath: ptr("/shared"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "shared"}})
				}
			}
			return out
		}
		webCmd := sh(`mkdir -p /srv && cp $IDX /srv/index.html && echo "$THEME" > /srv/theme.txt && echo "$UPSTREAM" > /srv/upstream.txt && httpd -f -p 8080 -h /srv`)
		return model.Config{
			Variables: []model.Variable{
				{VariableSlug: "theme", Label: "Theme", Type: "enum", Value: model.VariableValue{V: theme}, Required: true, Options: []string{"green", "yellow", "blue"}},
				{VariableSlug: "kv-port", Label: "KV port", Type: "number", Value: model.VariableValue{V: float64(kvPort)}, Required: true},
				{VariableSlug: "web2-port", Label: "Web 2 port", Type: "number", Value: model.VariableValue{V: float64(web2Port)}, Required: true},
			},
			Secrets: []model.SecretDef{{SecretSlug: "token", Label: "Token", Required: true, Value: ptr("s3cr3t-value-123"), RotatedAt: ptr(nowTs())}},
			Files: []model.FileDef{
				{FileSlug: "index", Label: "Index", Mime: "text/html", Editor: "text", MaxBytes: 4096, Render: model.Some("none"), Content: &model.BlobRef{Sha256: "sha256:" + idxHex, SizeBytes: int64(len(indexHTML))}},
				{FileSlug: "conf", Label: "Conf", Mime: "text/plain", Editor: "text", MaxBytes: 4096, Render: model.Some("expressions"), Content: &model.BlobRef{Sha256: "sha256:" + confHex, SizeBytes: int64(len(confSrc))}},
				{FileSlug: "cfgjson", Label: "JSON", Mime: "application/json", Editor: "text", MaxBytes: 4096, Render: model.Some("expressions"), Content: &model.BlobRef{Sha256: "sha256:" + jsonHex, SizeBytes: int64(len(jsonSrc))},
					Location: &model.StorageLocation{Kind: "path", Path: "./cfg/opm.json"}},
			},
			Sites: []model.Site{
				// db: host null = auto → its advertiseHost (127.0.0.1); app's env uses {{ site.db.host }}
				{SiteSlug: "db", Label: "DB", Config: model.SiteConfig{Host: nil, AgentPort: db.agentPort, AgentGuiAliasPort: &db.alias,
					Volumes: &[]model.SiteVolume{{VolumeSlug: "kv-data", Label: "KV data", Description: "the counter", Backup: true}, {VolumeSlug: "scratch", Label: "Scratch", Description: "", Backup: false}},
					Containers: []model.Container{
						{ContainerSlug: svcKV, ServiceSlug: svcKV, Version: "1", Enabled: true, Env: []model.ContainerEnv{}, Volumes: []model.ContainerMount{
							{ImageVolume: ptr("data"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "kv-data"}},
							{MountPath: ptr("/scratch"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "scratch"}}},
							Ports:   port("http", exprN("{{ var.kv-port }}"), "0.0.0.0"),
							Command: sh(`[ -f /data/count ] || echo 0 > /data/count; httpd -f -p 8080 -h /data`), Restart: "always", Depends: []model.ContainerRef{}},
					}, Depends: []model.SiteRef{}, Edge: model.Edge{HTTPPort: db.httpP, Hosts: []model.EdgeHost{{Hostname: "*", TLS: model.EdgeTls{Mode: "none"}}}, Routes: []model.EdgeRoute{}}}},
				{SiteSlug: "app", Label: "App", Config: model.SiteConfig{Host: ptr("127.0.0.1"), AgentPort: app.agentPort, AgentGuiAliasPort: &app.alias, NotFoundPageID: ptr("NfpTest0000000000001"),
					Depends: []model.SiteRef{{SiteSlug: "db"}},
					Volumes: &[]model.SiteVolume{{VolumeSlug: "shared", Label: "Shared", Description: "", Backup: true, Location: &model.StorageLocation{Kind: "path", Path: "./data/shared"}}},
					Containers: []model.Container{
						{ContainerSlug: svcAPI, ServiceSlug: svcAPI, Version: "1", Enabled: true, Env: []model.ContainerEnv{{Name: "TOKEN", SecretRef: ptr("token")}},
							Volumes: shared(svcAPI), Ports: port("http", model.NumOrExpr{}, "0.0.0.0"),
							Command: sh(`echo ok > /tmp/healthz; echo -n "$TOKEN" | wc -c > /tmp/tokenlen; httpd -f -p 8080 -h /tmp`), Restart: "always", Depends: []model.ContainerRef{}},
						{ContainerSlug: svcWeb, ServiceSlug: svcWeb, Version: webTag, Enabled: true,
							Env:     []model.ContainerEnv{{Name: "THEME", Value: ptr("{{ var.theme }}")}, {Name: "UPSTREAM", Value: ptr("{{ site.db.host }}:{{ site.db.port.kv.http }}")}, {Name: "IDX", Value: ptr("{{ file.index.path }}")}},
							Volumes: shared(svcWeb, fileMount("index", "/www/index.html"), fileMount("conf", "/etc/opm-conf.txt")),
							Ports:   port("http", model.NumOrExpr{}, "0.0.0.0"),
							Command: webCmd, Restart: "always", Depends: []model.ContainerRef{{ContainerSlug: svcAPI}}},
						// the SAME image (web:1) a second time: another name, env, host port (127.0.0.1) and a JSON template
						{ContainerSlug: "web-2", ServiceSlug: svcWeb, Version: "1", Enabled: true,
							Env:     []model.ContainerEnv{{Name: "THEME", Value: ptr("purple")}, {Name: "UPSTREAM", Value: ptr("{{ site.db.host }}")}, {Name: "IDX", Value: ptr("{{ file.index.path }}")}},
							Volumes: shared("web-2", fileMount("index", "/www/index.html"), fileMount("conf", "/etc/opm-conf.txt"), fileMount("cfgjson", "/etc/opm.json")),
							Ports:   port("http", exprN("{{ var.web2-port }}"), "127.0.0.1"),
							Command: webCmd, Restart: "always", Depends: []model.ContainerRef{}},
					},
					Edge: model.Edge{HTTPPort: app.httpP, Hosts: []model.EdgeHost{{Hostname: "nf.test", TLS: model.EdgeTls{Mode: "none"}}, {Hostname: "*", TLS: model.EdgeTls{Mode: "none"}}}, Routes: []model.EdgeRoute{
						{RouteID: "api", Hosts: []string{}, Order: 1, Match: model.EdgeMatch{Kind: "prefix", Path: "/api"}, Action: model.EdgeRouteAction{Type: "proxy", ContainerSlug: svcAPI, PortName: "http", StripPrefix: true, SetHeaders: model.MapOf("X-Opm-Site", "{{ self.slug }}"), RemoveHeaders: []string{}, ConnectTimeoutSec: 10, SendTimeoutSec: 10, ReadTimeoutSec: 10}},
						{RouteID: "old", Hosts: []string{}, Order: 2, Match: model.EdgeMatch{Kind: "prefix", Path: "/old"}, Action: model.EdgeRouteAction{Type: "redirect", To: "/", Status: 308}},
						{RouteID: "two", Hosts: []string{}, Order: 3, Match: model.EdgeMatch{Kind: "prefix", Path: "/two"}, Action: model.EdgeRouteAction{Type: "proxy", ContainerSlug: "web-2", PortName: "http", StripPrefix: true, SetHeaders: model.HeaderMap{}, RemoveHeaders: []string{}, ConnectTimeoutSec: 10, SendTimeoutSec: 10, ReadTimeoutSec: 10}},
						{RouteID: "web", Hosts: []string{"*"}, Order: 4, Match: model.EdgeMatch{Kind: "prefix", Path: "/"}, Action: model.EdgeRouteAction{Type: "proxy", ContainerSlug: svcWeb, PortName: "http", SetHeaders: model.HeaderMap{}, RemoveHeaders: []string{}, ConnectTimeoutSec: 10, SendTimeoutSec: 10, ReadTimeoutSec: 10}},
					}}}},
			},
		}
	}

	mkConfig := func(theme, webTag string) model.Config { return mkConfigD(theme, webTag, svcAPI, "web-2") }

	// a pre-round-C data dir: volumes/<serviceSlug>/<name>/ is moved to vol/<serviceSlug>-<name>/ on the first apply
	// a part-1 volume dir <agentHome>/vol/kv-data/ is moved to its location <agentHome>/volumes/kv-data/ on the first apply
	legacyKV := filepath.Join(root, "db", "data", "vol", "kv-data")
	if err := os.MkdirAll(legacyKV, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(legacyKV, "count"), []byte("5\n"), 0o644)
	// an orphan directory under vol/ (declared by no config): listed with declared=false, never backed up
	_ = os.MkdirAll(filepath.Join(root, "db", "data", "vol", "old-orphan"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "db", "data", "vol", "old-orphan", "x"), []byte("12345"), 0o644)

	start := func(s *site) {
		dir := filepath.Join(root, s.slug)
		tok := filepath.Join(dir, "install-token")
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(tok, []byte(fc.CreateInstallToken(s.slug)), 0o600)
		cfg := model.AgentConfigFile{APIBase: fc.URL, DeploymentID: dep, SiteSlug: s.slug, DataDir: filepath.Join(dir, "data"), AgentPort: s.agentPort,
			GuiAliasPort: model.Some(s.alias), AdvertiseHost: "127.0.0.1", InstallTokenFile: tok,
			EdgeHTTPPort: model.Some(s.httpP), ContainerNames: "prefixed"} // §21 #88: several sites share one Docker host; §21 #86: the default edge runs on the port the head edge will use
		a, err := New(Options{Config: cfg, Timings: fastTimings(), ApproveWait: time.Second, CompleteTimeout: 60 * time.Second, LogLevel: "debug", DisableSchedule: true})
		if err != nil {
			t.Fatal(err)
		}
		s.a = a
		jar, _ := cookiejar.New(nil)
		s.cl = &http.Client{Jar: jar, Timeout: 60 * time.Second}
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel, s.done = cancel, make(chan struct{})
		go func() { _ = a.Run(ctx); close(s.done) }()
	}
	stop := func(s *site) {
		s.cancel()
		<-s.done
	}
	start(db)
	start(app)
	defer func() {
		stop(app)
		stop(db)
		if t.Failed() {
			for _, s := range []*site{app, db} {
				files, _ := filepath.Glob(filepath.Join(root, s.slug, "data", "logs", "*.jsonl"))
				for _, f := range files {
					b, _ := os.ReadFile(f)
					lines := strings.Split(string(b), "\n")
					for _, l := range lines {
						if strings.Contains(l, `"level":"warn"`) || strings.Contains(l, `"level":"error"`) {
							t.Logf("[%s] %s", s.slug, l)
						}
					}
				}
			}
		}
	}()

	// 1. enroll + mesh
	waitFor(t, "enroll", 30*time.Second, func() bool {
		_, ok1 := fc.Agent("app")
		_, ok2 := fc.Agent("db")
		return ok1 && ok2
	})
	waitFor(t, "mesh reachable", 30*time.Second, func() bool { return app.a.reachable("db") && db.a.reachable("app") })
	if code, body, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/_opm/", app.alias)); code != 302 && code != 200 {
		t.Fatalf("GUI before head: %d %s", code, body)
	}
	// §21 #86: the edge is up before the first head (built-in default edge on the edge port)
	if code, _, hdr := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/", app.httpP)); code != 302 || hdr.Get("Location") != "/_opm/app" {
		t.Fatalf("default edge / before head: %d", code)
	}
	if code, body, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/nope", app.httpP)); code != 404 || !strings.Contains(body, "no applied configuration yet") {
		t.Fatalf("default edge not-found before head: %d %s", code, body)
	}
	if code, _, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/_opm/healthz", db.httpP)); code != 200 {
		t.Fatalf("db default edge GUI before head: %d", code)
	}
	waitFor(t, "guiPorts in the heartbeat", 20*time.Second, func() bool {
		d, ok := fc.Agent("app")
		return ok && len(d.GuiPorts) > 0 && d.GuiPorts[0].Kind == "edge" && d.GuiPorts[0].Port == app.httpP
	})

	// 2. login (wrong key once), actor
	login := func(s *site, key string) int {
		code, _ := s.api(t, "POST", "/_opm/api/login", model.LoginRequest{AccessKey: key}, nil)
		return code
	}
	waitFor(t, "key hash on both sites", 20*time.Second, func() bool { h1, _ := app.a.keyHash(); h2, _ := db.a.keyHash(); return h1 != "" && h2 != "" })
	if c := login(app, "AAAAAAAAAAAAAAAAAAAAAAAA"); c != 401 {
		t.Fatalf("wrong key: %d", c)
	}
	for _, s := range []*site{app, db} {
		if c := login(s, strings.ToLower(testKey[:4])+"-"+testKey[4:]); c != 200 {
			t.Fatalf("login %s: %d", s.slug, c)
		}
		var ar model.ActorResponse
		if c, b := s.api(t, "POST", "/_opm/api/actor", model.ActorRequest{Email: " Ops@Example.com "}, &ar); c != 200 {
			t.Fatalf("actor: %d %s", c, b)
		}
		s.actor = ar.ActorToken
		if ar.Email != "ops@example.com" {
			t.Fatalf("email %s", ar.Email)
		}
	}

	approve := func(s *site, revID string) model.UpdateRun {
		t.Helper()
		var det model.ProposalDetailResponse
		waitFor(t, "gate open for "+revID, 120*time.Second, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/proposals/"+revID, nil, &det)
			return c == 200 && det.Gate.Open
		})
		var st model.ApproveStartResponse
		if c, b := s.api(t, "POST", "/_opm/api/proposals/"+revID+"/approve/start", nil, &st); c != 201 {
			t.Fatalf("approve/start: %d %s", c, b)
		}
		if c, _ := s.api(t, "POST", "/_opm/api/proposals/"+revID+"/approve", model.ApproveRequest{IntentID: st.Intent.IntentID, Typed: "ACCEPT"}, nil); c != 422 {
			t.Fatalf("approve before notBefore should be invalid, got %d", c)
		}
		time.Sleep(1100 * time.Millisecond)
		if c, _ := s.api(t, "POST", "/_opm/api/proposals/"+revID+"/approve", model.ApproveRequest{IntentID: st.Intent.IntentID, Typed: "accept"}, nil); c != 422 {
			t.Fatalf("wrong typed word should be invalid, got %d", c)
		}
		var ar model.ApproveResponse
		if c, b := s.api(t, "POST", "/_opm/api/proposals/"+revID+"/approve", model.ApproveRequest{IntentID: st.Intent.IntentID, Typed: "ACCEPT"}, &ar); c != 201 {
			t.Fatalf("approve: %d %s", c, b)
		}
		var run model.UpdateRun
		waitFor(t, "update "+ar.UpdateID+" finished", 240*time.Second, func() bool {
			c, _ := s.api(t, "GET", "/_opm/api/updates/"+ar.UpdateID, nil, &run)
			return c == 200 && run.FinishedAt != nil
		})
		return run
	}
	edgeGet := func(path string) (int, string) {
		// a connection the edge closes while its config swaps right after an APPLY (status 0, "EOF") is retried
		// for up to 5 s: the assertion is about the answer, not about one TCP connection
		var c int
		var b string
		for i := 0; i < 20; i++ {
			c, b, _ = httpGet(t, fmt.Sprintf("http://127.0.0.1:%d%s", app.httpP, path))
			if c != 0 {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		return c, strings.TrimSpace(b)
	}

	// 3. release 1
	rev1, _ := fc.Publish(mkConfig("green", "1"), cat, "release 1")
	run := approve(app, rev1.RevID)
	if run.Result == nil || !run.Result.Ok {
		t.Fatalf("release 1 failed: %+v", run.Result)
	}
	for _, s := range []*site{app, db} {
		if h := s.a.head(); h == nil || h.Seq != 1 {
			t.Fatalf("%s head %+v", s.slug, h)
		}
	}
	if c, b := edgeGet("/"); c != 200 || !strings.Contains(b, "hello mesh") {
		t.Fatalf("GET / → %d %s", c, b)
	}
	if c, b := edgeGet("/theme.txt"); c != 200 || b != "green" {
		t.Fatalf("theme → %d %q", c, b)
	}
	if c, b := edgeGet("/upstream.txt"); c != 200 || b != fmt.Sprintf("127.0.0.1:%d", kvPort) {
		t.Fatalf("upstream → %d %q", c, b)
	}
	if c, b := edgeGet("/api/tokenlen"); c != 200 || b != "16" {
		t.Fatalf("api/tokenlen → %d %q (secretRef env)", c, b)
	}
	{
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/nope/x", app.httpP), nil)
		req.Host = "NF.test"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 404 || !strings.Contains(string(b), "Test 404") {
			t.Fatalf("not found → %d %s", resp.StatusCode, b)
		}
	}
	if c, _, h := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/old/x", app.httpP)); c != 308 || h.Get("Location") != "/" {
		t.Fatalf("redirect → %d %v", c, h.Get("Location"))
	}
	if c, b := edgeGet("/_opm/nope"); c != 404 || !strings.Contains(b, "OPM agent") {
		t.Fatalf("agent 404 → %d %s", c, b)
	}
	if c, b, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/anything", db.httpP)); c != 404 || !strings.Contains(b, "This page does not exist") {
		t.Fatalf("db edge → %d %s", c, b)
	}
	if c, _, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/_opm/api/info", app.httpP)); c != 200 {
		t.Fatalf("GUI API on the edge → %d", c)
	}
	if c, b, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/count", kvPort)); c != 200 || strings.TrimSpace(b) != "5" {
		t.Fatalf("kv host port → %d %q (legacy volume moved to vol/kv-data)", c, b)
	}
	if _, err := os.Stat(legacyKV); !os.IsNotExist(err) {
		t.Fatalf("legacy volume dir still there: %v", err)
	}
	roundCChecks(t, dc, dep, app, db, kvPort, web2Port, edgeGet)
	roundDShared(t, dc, dep, app, db)
	// readable image names: <serviceSlug>:<version> points to the verified opm/cfg:<configHex> image; containers run from it
	cfgIm, err := dc.ImageInspect(context.Background(), "opm/cfg:"+img.configHex)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"api:1", "web:1", "kv:1"} {
		im, err := dc.ImageInspect(context.Background(), ref)
		if err != nil || im.ID != cfgIm.ID {
			t.Fatalf("readable tag %s: %v (id %v, want %s)", ref, err, im, cfgIm.ID)
		}
	}
	if cs, _ := dc.ContainerList(context.Background(), true, "opm.deployment="+dep, "opm.service=kv"); len(cs) != 1 || cs[0].Image != "kv:1" {
		t.Fatalf("kv container image: %+v", cs)
	}
	if cj, err := dc.ContainerInspect(context.Background(), "opm_"+dep+"_db_kv"); err != nil || !mountedAt(cj, "/data") {
		t.Fatalf("kv mount /data (from /{{ container.env.DATA_DIR }}): %v %+v", err, cj)
	}
	waitFor(t, "cloud head seq 1", 20*time.Second, func() bool { h := fc.Head(); return h != nil && h.Seq == 1 })
	waitFor(t, "cloud proposal applied", 20*time.Second, func() bool { p, _ := fc.Proposal(rev1.RevID); return p.Status == model.ProposalApplied })
	waitFor(t, "update.applied piped", 30*time.Second, func() bool {
		for _, x := range fc.Activities("app") {
			if x.Type == "update.applied" {
				return true
			}
		}
		return false
	})
	// blobs: one site downloaded from the cloud, the other copied from the peer (or both cloud when racing)
	var srcs []string
	for _, s := range []*site{app, db} {
		if src := s.a.blobSource(img.configHex); src != nil {
			srcs = append(srcs, s.slug+"="+*src)
		}
	}
	t.Logf("config blob sources: %v", srcs)

	// container ids for the recreate check
	ids := func() map[string]string {
		m := map[string]string{}
		for _, s := range []*site{app, db} {
			ex, _ := s.a.drv.Existing(context.Background())
			for sid, e := range ex {
				m[sid] = e.ID
			}
		}
		return m
	}
	ids1 := ids()

	// 4. data in the kv volume + manual snapshot S1
	kvDir := filepath.Join(root, "db", "data", "volumes", "kv-data")
	if err := os.WriteFile(filepath.Join(kvDir, "count"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sharedDir := filepath.Join(root, "app", "data", "data", "shared") // custom location "./data/shared"
	scratchDir := filepath.Join(root, "db", "data", "volumes", "scratch")
	_ = os.WriteFile(filepath.Join(sharedDir, "hello.txt"), []byte("s1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(scratchDir, "tmp.txt"), []byte("never backed up\n"), 0o644)
	var cs model.CreateSnapshotResponse
	if c, b := app.api(t, "POST", "/_opm/api/snapshots", model.CreateSnapshotRequest{Note: "count 3"}, &cs); c != 202 {
		t.Fatalf("snapshot: %d %s", c, b)
	}
	var sd model.SnapshotDetailResponse
	waitFor(t, "snapshot S1 complete", 60*time.Second, func() bool {
		c, _ := app.api(t, "GET", "/_opm/api/snapshots/"+cs.SnapshotID, nil, &sd)
		return c == 200 && len(sd.Summary.Sites) == 2 && len(sd.Summary.MissingSites) == 0
	})
	waitFor(t, "kv restarted after snapshot", 30*time.Second, func() bool {
		c, b, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/count", kvPort))
		return c == 200 && strings.TrimSpace(b) == "3"
	})
	// round D: exactly the backup = true volumes are archived (scratch has data but backup off; host paths never)
	if got := snapVolumes(sd.Snapshot); got != "app/shared,db/kv-data" {
		t.Fatalf("S1 volumes %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "db", "data", "snapshots", cs.SnapshotID, "sites", "db", "volumes", "scratch.tar.zst")); !os.IsNotExist(err) {
		t.Fatalf("scratch archived: %v", err)
	}
	var dvol model.SiteVolumesResponse
	if c, b := app.api(t, "GET", "/_opm/api/sites/db/volumes", nil, &dvol); c != 200 {
		t.Fatalf("db volumes: %d %s", c, b)
	}
	if v := findVol(dvol, "kv-data"); v == nil || v.LastSnapshot == nil || v.LastSnapshot.SnapshotID != cs.SnapshotID {
		t.Fatalf("kv-data last snapshot: %+v", v)
	}
	if v := findVol(dvol, "scratch"); v == nil || v.LastSnapshot != nil || v.Backup {
		t.Fatalf("scratch: %+v", v)
	}

	// 5. release 2: theme yellow → only web recreated
	rev2, _ := fc.Publish(mkConfig("yellow", "1"), cat, "theme green → yellow")
	run = approve(app, rev2.RevID)
	if run.Result == nil || !run.Result.Ok {
		t.Fatalf("release 2 failed: %+v", run.Result)
	}
	if c, b := edgeGet("/theme.txt"); c != 200 || b != "yellow" {
		t.Fatalf("theme after release 2 → %d %q", c, b)
	}
	ids2 := ids()
	if ids2[svcAPI] != ids1[svcAPI] || ids2[svcKV] != ids1[svcKV] || ids2[svcWeb] == ids1[svcWeb] || ids2["web-2"] != ids1["web-2"] || ids2["web-2"] == "" {
		t.Fatalf("recreate rule: before %v after %v", ids1, ids2)
	}

	// 6. failing release → automatic rollback
	rev3, _ := fc.Publish(mkConfig("blue", "bad"), cat, "broken web")
	run = approve(app, rev3.RevID)
	if run.Result == nil || run.Result.Ok || !strings.Contains(run.Result.Message, "apply failed on app") {
		t.Fatalf("release 3 should fail at apply on app: %+v", run.Result)
	}
	if h := app.a.head(); h == nil || h.Seq != 2 {
		t.Fatalf("head after rollback %+v", h)
	}
	waitFor(t, "web back after rollback", 60*time.Second, func() bool { c, b := edgeGet("/theme.txt"); return c == 200 && b == "yellow" })
	if p, _ := app.a.proposal(rev3.RevID); p.Status != model.ProposalFailedRolledBack {
		t.Fatalf("proposal status %s", p.Status)
	}
	waitFor(t, "cloud sees failed-rolled-back", 20*time.Second, func() bool { p, _ := fc.Proposal(rev3.RevID); return p.Status == model.ProposalFailedRolledBack })
	hasAct := func(s *site, typ string) bool {
		items, _ := s.a.activitiesPage(0, 1000, typ, "")
		return len(items) > 0
	}
	if !hasAct(app, "update.failed") || !hasAct(app, "update.rolled-back") || !hasAct(db, "update.rolled-back") {
		t.Fatal("rollback activities missing")
	}

	// 6b. round D: the shared volume moves from api to web (same directory, data kept). The pre-update snapshot holds
	// exactly the backup = true volumes of containers that change: shared (api + web-2 stopped / recreated), not kv-data.
	rev4, _ := fc.Publish(mkConfigD("yellow", "1", svcWeb, "web-2"), cat, "shared volume: api → web")
	idsB := ids()
	run = approve(app, rev4.RevID)
	if run.Result == nil || !run.Result.Ok {
		t.Fatalf("move release failed: %+v", run.Result)
	}
	idsM := ids()
	if idsM[svcKV] != idsB[svcKV] || idsM[svcAPI] == idsB[svcAPI] || idsM[svcWeb] == idsB[svcWeb] {
		t.Fatalf("move: before %v after %v", idsB, idsM)
	}
	if got := execCat(t, dc, "opm_"+dep+"_app_"+svcWeb, "/shared/hello.txt"); got != "s1" {
		t.Fatalf("web after the move sees %q (the volume keeps its data)", got)
	}
	if code, _, _ := dc.Exec(context.Background(), idsM[svcAPI], []string{"test", "-e", "/shared/hello.txt"}); code == 0 {
		t.Fatal("api must not mount shared any more")
	}
	{
		var list model.ListResponse[model.SnapshotSummary]
		app.api(t, "GET", "/_opm/api/snapshots", nil, &list)
		var pre string
		for _, x := range list.Items {
			if x.Reason == "pre-update" && pre == "" {
				pre = x.SnapshotID
			}
		}
		var pd model.SnapshotDetailResponse
		if c, _ := app.api(t, "GET", "/_opm/api/snapshots/"+pre, nil, &pd); c != 200 || snapVolumes(pd.Snapshot) != "app/shared" {
			t.Fatalf("pre-update snapshot of the move %s: %q", pre, snapVolumes(pd.Snapshot))
		}
	}
	_ = os.WriteFile(filepath.Join(sharedDir, "hello.txt"), []byte("after-move\n"), 0o644)

	// 7. rollback to S1 (theme green, count 3)
	_ = os.WriteFile(filepath.Join(kvDir, "count"), []byte("9\n"), 0o644)
	var plan model.RollbackPlanResponse
	waitFor(t, "rollback gate open", 30*time.Second, func() bool {
		c, _ := app.api(t, "GET", "/_opm/api/snapshots/"+cs.SnapshotID+"/plan", nil, &plan)
		return c == 200 && plan.Gate.Open
	})
	var rs model.ApproveStartResponse
	{ // plan: restored by volumeSlug; scratch (backup off) is kept
		rv := []string{}
		for _, v := range plan.VolumesRestored {
			rv = append(rv, v.SiteSlug+"/"+v.VolumeSlug)
		}
		kept := []string{}
		for _, v := range plan.VolumesKept {
			kept = append(kept, fmt.Sprintf("%s/%s:%v", v.SiteSlug, v.VolumeSlug, v.Backup))
		}
		if strings.Join(rv, ",") != "app/shared,db/kv-data" || strings.Join(kept, ",") != "db/scratch:false" {
			t.Fatalf("rollback plan volumes: restored %v kept %v", rv, kept)
		}
	}
	if c, b := app.api(t, "POST", "/_opm/api/snapshots/"+cs.SnapshotID+"/rollback/start", nil, &rs); c != 201 {
		t.Fatalf("rollback/start: %d %s", c, b)
	}
	time.Sleep(1100 * time.Millisecond)
	var rr model.ApproveResponse
	if c, b := app.api(t, "POST", "/_opm/api/snapshots/"+cs.SnapshotID+"/rollback", model.ApproveRequest{IntentID: rs.Intent.IntentID, Typed: "ACCEPT"}, &rr); c != 201 {
		t.Fatalf("rollback: %d %s", c, b)
	}
	waitFor(t, "rollback finished", 240*time.Second, func() bool {
		c, _ := app.api(t, "GET", "/_opm/api/updates/"+rr.UpdateID, nil, &run)
		return c == 200 && run.FinishedAt != nil
	})
	if run.Result == nil || !run.Result.Ok {
		t.Fatalf("rollback to S1 failed: %+v", run.Result)
	}
	if h := db.a.head(); h == nil || h.Seq != 4 {
		t.Fatalf("db head after rollback %+v", h)
	}
	if hr := app.a.headRevision(); hr.Kind != "rollback" {
		t.Fatalf("kind %s", hr.Kind)
	}
	if c, b := edgeGet("/theme.txt"); c != 200 || b != "green" {
		t.Fatalf("theme after rollback → %d %q", c, b)
	}
	if c, b, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/count", kvPort)); c != 200 || strings.TrimSpace(b) != "3" {
		t.Fatalf("kv count after rollback → %d %q", c, b)
	}
	// round D: the shared volume (moved to web after S1) is restored by volumeSlug; S1's config mounts it on api again
	if b, _ := os.ReadFile(filepath.Join(sharedDir, "hello.txt")); strings.TrimSpace(string(b)) != "s1" {
		t.Fatalf("shared after rollback %q", b)
	}
	if got := execCat(t, dc, "opm_"+dep+"_app_"+svcAPI, "/shared/hello.txt"); got != "s1" {
		t.Fatalf("api after rollback sees %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(scratchDir, "tmp.txt")); strings.TrimSpace(string(b)) != "never backed up" {
		t.Fatalf("scratch (not in the snapshot) must stay untouched: %q", b)
	}

	// 8. local proposal on db (theme blue), db coordinates
	var cfgResp model.AgentConfigResponse
	db.api(t, "GET", "/_opm/api/config", nil, &cfgResp)
	in := model.ConfigInput{Variables: cfgResp.Config.Variables, Files: cfgResp.Config.Files, Sites: cfgResp.Config.Sites}
	for i := range in.Variables {
		if in.Variables[i].VariableSlug == "theme" {
			in.Variables[i].Value = model.VariableValue{V: "blue"}
		}
	}
	for _, s := range cfgResp.Config.Secrets {
		in.Secrets = append(in.Secrets, model.SecretDefInput{SecretSlug: s.SecretSlug, Label: s.Label, Description: s.Description, Required: s.Required, Generate: s.Generate})
	}
	var prev model.LocalProposalPreviewResponse
	if c, b := db.api(t, "POST", "/_opm/api/proposals/local/preview", model.LocalProposalRequest{BaseRevID: cfgResp.Head.RevID, Summary: "x", Config: in}, &prev); c != 200 || !prev.Validation.Ok || len(prev.Changes) != 1 {
		t.Fatalf("preview: %d %s", c, b)
	}
	var lp model.LocalProposalResponse
	if c, b := db.api(t, "POST", "/_opm/api/proposals/local", model.LocalProposalRequest{BaseRevID: cfgResp.Head.RevID, Summary: "theme blue (local)", Config: in}, &lp); c != 201 {
		t.Fatalf("local proposal: %d %s", c, b)
	}
	waitFor(t, "local proposal on app", 20*time.Second, func() bool { _, ok := app.a.proposal(lp.Proposal.RevID); return ok })
	run = approve(db, lp.Proposal.RevID)
	if run.Result == nil || !run.Result.Ok {
		t.Fatalf("local change failed: %+v", run.Result)
	}
	if c, b := edgeGet("/theme.txt"); c != 200 || b != "blue" {
		t.Fatalf("theme after local change → %d %q", c, b)
	}
	waitFor(t, "cloud head seq 5 (origin agent)", 20*time.Second, func() bool { h := fc.Head(); return h != nil && h.Seq == 5 })

	// 9. snapshot download → import conflict → delete → import
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/_opm/api/snapshots/%s/download", app.alias, cs.SnapshotID), nil)
	resp, err := app.cl.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("download: %v %v", err, resp)
	}
	snapFile, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("X-OPM-Warning") != "contains-secrets" {
		t.Fatal("warning header missing")
	}
	if c, _ := db.api(t, "POST", "/_opm/api/snapshots/import", snapFile, nil); c != 409 {
		t.Fatalf("import of an existing id should conflict: %d", c)
	}
	if c, b := db.api(t, "DELETE", "/_opm/api/snapshots/"+cs.SnapshotID, nil, nil); c != 200 {
		t.Fatalf("delete: %d %s", c, b)
	}
	var imp model.SnapshotSummary
	if c, b := db.api(t, "POST", "/_opm/api/snapshots/import", snapFile, &imp); c != 201 {
		t.Fatalf("import: %d %s", c, b)
	}
	waitFor(t, "imported snapshot complete on both", 30*time.Second, func() bool {
		c, _ := db.api(t, "GET", "/_opm/api/snapshots/"+cs.SnapshotID, nil, &sd)
		return c == 200 && len(sd.Summary.Sites) == 2 && sd.Summary.Reason == "imported"
	})
	tampered := append([]byte{}, snapFile...)
	if i := bytes.Index(tampered, []byte("count 3")); i > 0 {
		tampered[i] = 'C'
		if c, _ := db.api(t, "POST", "/_opm/api/snapshots/import", tampered, nil); c != 422 {
			t.Fatalf("tampered import should be invalid: %d", c)
		}
	}

	// 10. activity bundle (.opmlog) of app incl. debug
	req, _ = http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/_opm/api/sites/app/activities/download?includeDebug=true", app.alias), nil)
	req.Header.Set("Authorization", "Actor "+app.actor)
	resp, err = app.cl.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("opmlog: %v %v", err, resp)
	}
	logTar, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	names := []string{}
	tr := tar.NewReader(bytes.NewReader(logTar))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	if len(names) != 4 || names[0] != "manifest.json" || names[1] != "activities.ndjson" || names[3] != "signature.sig" {
		t.Fatalf("opmlog entries %v", names)
	}
	if bytes.Contains(logTar, []byte("s3cr3t-value-123")) {
		t.Fatal("secret value in the activity bundle")
	}

	// 11. merged views over the mesh
	var stl model.ListResponse[model.SiteStatus]
	if c, b := app.api(t, "GET", "/_opm/api/sites/*/status", nil, &stl); c != 200 || len(stl.Items) != 2 {
		t.Fatalf("status *: %d %s", c, b)
	}
	var acts model.PageResponse[model.Activity]
	if c, _ := app.api(t, "GET", "/_opm/api/sites/db/activities?limit=5", nil, &acts); c != 200 || len(acts.Items) != 5 || acts.Items[0].SiteSlug != "db" {
		t.Fatalf("forwarded activities: %d %+v", c, acts.Items)
	}
	var ov model.OverviewResponse
	if c, _ := app.api(t, "GET", "/_opm/api/overview", nil, &ov); c != 200 || len(ov.Sites) != 2 || ov.Head == nil || ov.Head.Seq != 5 {
		t.Fatalf("overview: %+v", ov)
	}
	var logs model.ContainerLogsResponse
	if c, b := app.api(t, "GET", "/_opm/api/sites/db/containers/"+svcKV+"/logs?tail=20", nil, &logs); c != 200 || logs.ContainerSlug != svcKV || logs.ServiceSlug != svcKV {
		t.Fatalf("logs: %d %s", c, b)
	}
	if c, b := app.api(t, "GET", "/_opm/api/sites/app/containers/web-2/logs?tail=20", nil, &logs); c != 200 || logs.ContainerSlug != "web-2" || logs.ServiceSlug != svcWeb {
		t.Fatalf("logs web-2: %d %s", c, b)
	}
	foundW2 := false
	for _, st := range stl.Items {
		for _, cs := range st.Containers {
			if cs.ContainerSlug == "web-2" && cs.ServiceSlug == svcWeb && cs.Image == "web:1" && cs.Managed {
				foundW2 = true
			}
		}
	}
	if !foundW2 {
		t.Fatalf("status: web-2 missing: %+v", stl.Items)
	}
	// rendered file contents (with the secret) never reach the logs, the activities or the snapshot metadata
	for _, s := range []*site{app, db} {
		files, _ := filepath.Glob(filepath.Join(root, s.slug, "data", "logs", "*"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			if bytes.Contains(b, []byte("s3cr3t-value-123")) || bytes.Contains(b, []byte("site=app token=")) {
				t.Fatalf("rendered content / secret in the debug log %s", f)
			}
		}
		items, _ := s.a.activitiesPage(0, 5000, "", "")
		ab, _ := json.Marshal(items)
		if bytes.Contains(ab, []byte("s3cr3t-value-123")) || bytes.Contains(ab, []byte("site=app token=")) {
			t.Fatalf("rendered content in the activities of %s", s.slug)
		}
		mans, _ := filepath.Glob(filepath.Join(root, s.slug, "data", "snapshots", "*", "sites", "*", "*.json"))
		for _, f := range mans {
			b, _ := os.ReadFile(f)
			if bytes.Contains(b, []byte("site=app token=")) { // (the snapshot config holds the plain secret values by design)
				t.Fatalf("rendered content in snapshot metadata %s", f)
			}
		}
	}

	// 11b. scheduled snapshot: only the first reachable head-config site (app) starts it; parts on both sites
	if _, started := db.a.scheduledSnapshotOnce(context.Background()); started {
		t.Fatal("db is not first in slug order")
	}
	schedID, started := app.a.scheduledSnapshotOnce(context.Background())
	if !started {
		t.Fatal("app should start the scheduled snapshot")
	}
	waitFor(t, "scheduled snapshot on both sites", 60*time.Second, func() bool {
		c, _ := app.api(t, "GET", "/_opm/api/snapshots/"+schedID, nil, &sd)
		return c == 200 && sd.Summary.Reason == "scheduled" && len(sd.Summary.Sites) == 2
	})

	// 12a. relay: db loses its direct internet → its cloud calls go through app
	fc.BlockSite("db", true)
	waitFor(t, "db heartbeat relayed via app", 60*time.Second, func() bool {
		d, ok := fc.Agent("db")
		return ok && d.RelayVia != nil && *d.RelayVia == "app"
	})
	// 12b. transfers: cloud → db (download + save on the server), db → cloud (upload)
	tr0 := fc.AddTransferToSite("db", "notes.txt", []byte("file for db\n"), "hello")
	waitFor(t, "to-site file downloaded on db (via relay)", 60*time.Second, func() bool { _, lp, ok := db.a.transfer(tr0.TransferID); return ok && lp != "" })
	savePath := filepath.Join(root, "saved-notes.txt")
	var saved model.Transfer
	if c, b := app.api(t, "POST", "/_opm/api/sites/db/transfers/"+tr0.TransferID+"/save", model.SaveTransferRequest{Path: savePath}, &saved); c != 200 || saved.Status != "saved-on-server" {
		t.Fatalf("save (forwarded to db): %d %s", c, b)
	}
	if b, _ := os.ReadFile(savePath); string(b) != "file for db\n" {
		t.Fatalf("saved content %q", b)
	}
	if c, _ := app.api(t, "POST", "/_opm/api/sites/db/transfers/"+tr0.TransferID+"/save", model.SaveTransferRequest{Path: savePath}, nil); c != 409 {
		t.Fatalf("save over an existing file should conflict: %d", c)
	}
	fc.BlockSite("db", false)
	var sent model.Transfer
	if c, b := app.api(t, "POST", "/_opm/api/sites/db/transfers/upload?fileName=report.txt&note=n", []byte("report from db"), &sent); c != 202 {
		t.Fatalf("send from browser: %d %s", c, b)
	}
	waitFor(t, "to-cloud transfer ready", 60*time.Second, func() bool {
		for _, x := range fc.Transfers() {
			if x.Direction == "to-cloud" && x.FileName == "report.txt" && x.Status == "ready" {
				_, data, _ := fc.Transfer(x.TransferID)
				return string(data) == "report from db"
			}
		}
		return false
	})

	// 12. access key rotation: old key fails, new key works, open sessions keep working
	newKey := "ZYXWVUTSRQPONMLKJIHGFEDC"
	fc.SetAccessKey(newKey)
	waitFor(t, "key version 2 on both", 30*time.Second, func() bool {
		_, v1 := app.a.keyHash()
		_, v2 := db.a.keyHash()
		return v1 == 2 && v2 == 2
	})
	if c, _ := app.api(t, "GET", "/_opm/api/session", nil, nil); c != 200 {
		t.Fatalf("old session should still work: %d", c)
	}
	jar, _ := cookiejar.New(nil)
	fresh := &site{alias: app.alias, cl: &http.Client{Jar: jar}}
	if c := login(fresh, testKey); c != 401 {
		t.Fatalf("old key after rotation: %d", c)
	}
	if c := login(fresh, newKey); c != 200 {
		t.Fatalf("new key: %d", c)
	}
	// 13. round D part 2: a file template that uses container.* fails staging (E_NOT_ALLOWED: files are rendered once per
	// site and shared by every container)
	badSrc := []byte("x={{ container.env.THEME }}\n")
	badHex := fc.PutBlob(badSrc)
	badCfg := mkConfig("blue", "1")
	for i := range badCfg.Files {
		if badCfg.Files[i].FileSlug == "conf" {
			badCfg.Files[i].Content = &model.BlobRef{Sha256: "sha256:" + badHex, SizeBytes: int64(len(badSrc))}
		}
	}
	badRev, _ := fc.Publish(badCfg, cat, "file with container.env")
	waitFor(t, "staging fails with E_NOT_ALLOWED", 90*time.Second, func() bool {
		p, ok := app.a.proposal(badRev.RevID)
		if !ok {
			return false
		}
		st := p.Sites["app"]
		return st.Staging == model.StagingFailed && st.Error != nil && strings.Contains(*st.Error, "E_NOT_ALLOWED") && strings.Contains(*st.Error, "files are shared by all containers")
	})
	// 14. §21 #91 f: an agent restart never touches the app containers (ids + State.StartedAt unchanged)
	snapC := func() map[string]string {
		out := map[string]string{}
		cs, err := dc.ContainerList(context.Background(), true, "opm.deployment="+dep, "opm.site=db")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			cj, err := dc.ContainerInspect(context.Background(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			out[c.ID] = cj.State.Status + "@" + cj.State.StartedAt
		}
		return out
	}
	before := snapC()
	if len(before) == 0 {
		t.Fatal("db runs no OPM container")
	}
	stop(db)
	start(db)
	waitFor(t, "db health after the restart", 30*time.Second, func() bool {
		c, _, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/_opm/api/health", db.agentPort))
		return c == 200
	})
	time.Sleep(3 * time.Second) // monitor + reconcile loops ran
	if after := snapC(); !reflect.DeepEqual(before, after) {
		t.Fatalf("containers changed across an agent restart:\nbefore %v\nafter  %v", before, after)
	}
	t.Logf("agent restart: %d db containers kept (same id + StartedAt)", len(before))
	_ = sha256.New
	_ = hex.EncodeToString
}

func icon(s string) *model.Icon { return &s }

// roundCChecks: the same image twice on one site (names, aliases, labels, env, host ports, edge routes), the auto
// host of db used by app's env, and template files rendered per container (bytes inside the container, mode 0400).
func roundCChecks(t *testing.T, dc *docker.Client, dep string, app, db *site, kvPort, web2Port int, edgeGet func(string) (int, string)) {
	t.Helper()
	ctx := context.Background()
	if c, b := edgeGet("/two/theme.txt"); c != 200 || b != "purple" {
		t.Fatalf("edge route to web-2 → %d %q", c, b)
	}
	if c, b := edgeGet("/theme.txt"); c != 200 || b != "green" {
		t.Fatalf("edge route to web → %d %q", c, b)
	}
	if c, b, _ := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/theme.txt", web2Port)); c != 200 || strings.TrimSpace(b) != "purple" {
		t.Fatalf("web-2 host port (127.0.0.1) → %d %q", c, b)
	}
	if c, b := edgeGet("/two/upstream.txt"); c != 200 || b != "127.0.0.1" {
		t.Fatalf("site.db.host (auto = advertiseHost) → %d %q", c, b)
	}
	w1, err := dc.ContainerInspect(ctx, "opm_"+dep+"_app_web")
	if err != nil {
		t.Fatal(err)
	}
	w2, err := dc.ContainerInspect(ctx, "opm_"+dep+"_app_web-2")
	if err != nil {
		t.Fatal(err)
	}
	if w1.ID == w2.ID || w2.Config.Labels["opm.container"] != "web-2" || w2.Config.Labels["opm.service"] != "web" || w1.Config.Labels["opm.container"] != "web" || w2.Config.Image != "web:1" || w1.Config.Image != "web:1" {
		t.Fatalf("instances: %+v / %+v", w1.Config, w2.Config)
	}
	if cs, _ := dc.ContainerList(ctx, true, "opm.deployment="+dep, "opm.service=web"); len(cs) != 2 {
		t.Fatalf("two containers of service web: %d", len(cs))
	}
	cat := func(id, path string) string {
		code, out, err := dc.Exec(ctx, id, []string{"cat", path})
		if err != nil || code != 0 {
			t.Fatalf("cat %s: %d %v %s", path, code, err, out)
		}
		return out
	}
	// one copy per site: web and web-2 see the same rendered bytes from ONE host file <agentHome>/files/conf (0400:
	// secret inside); no per-container copies
	want := fmt.Sprintf("site=app token=s3cr3t-value-123 db=127.0.0.1:%d lit={{x}}\n", kvPort)
	if got := cat(w1.ID, "/etc/opm-conf.txt"); got != want {
		t.Fatalf("rendered conf in web: %q", got)
	}
	if got := cat(w2.ID, "/etc/opm-conf.txt"); got != want {
		t.Fatalf("rendered conf in web-2: %q", got)
	}
	confHost := filepath.Join(app.a.dataDir, "files", "conf")
	if b, err := os.ReadFile(confHost); err != nil || string(b) != want {
		t.Fatalf("one host copy %s: %v %q", confHost, err, b)
	}
	srcOf := func(cj *docker.ContainerJSON, target string) string {
		for _, m := range cj.Mounts {
			if m.Destination == target {
				return m.Source
			}
		}
		return ""
	}
	if s1, s2 := srcOf(w1, "/etc/opm-conf.txt"), srcOf(w2, "/etc/opm-conf.txt"); s1 == "" || s1 != s2 || srcOf(w1, "/www/index.html") != srcOf(w2, "/www/index.html") {
		t.Fatalf("both containers must bind the same host file: %q %q", s1, s2)
	}
	if ents, _ := os.ReadDir(filepath.Join(app.a.dataDir, "files")); len(ents) != 2 { // conf + index (cfgjson lives in ./cfg)
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("files/ must hold exactly one copy per mounted file: %v", names)
	}
	if st, err := os.Stat(filepath.Join(app.a.dataDir, "files", "index")); err != nil || st.Mode().Perm() != 0o444 {
		t.Fatalf("render none copy mode: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(app.a.dataDir, "rendered")); !os.IsNotExist(err) {
		t.Fatalf("no per-container rendered copies: %v", err)
	}
	// a "./" location: <agentHome>/cfg/opm.json
	var js map[string]string
	if got := cat(w2.ID, "/etc/opm.json"); json.Unmarshal([]byte(strings.TrimSpace(got)), &js) != nil || js["site"] != "app" || js["db"] != "127.0.0.1" {
		t.Fatalf("rendered JSON in web-2: %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(app.a.dataDir, "cfg", "opm.json")); err != nil || !strings.Contains(string(b), `"site": "app"`) {
		t.Fatalf("./cfg/opm.json on the host: %v %q", err, b)
	}
	if srcOf(w2, "/etc/opm.json") != filepath.Join(app.a.dataDir, "cfg", "opm.json") {
		t.Fatalf("custom location bind %q", srcOf(w2, "/etc/opm.json"))
	}
	// file blobs live in blobs/files, not in files/
	if ents, _ := os.ReadDir(filepath.Join(app.a.dataDir, "blobs", "files")); len(ents) == 0 {
		t.Fatal("blobs/files is empty")
	}
	// read-only mount + 0400 on the host
	if code, _, _ := dc.Exec(ctx, w2.ID, []string{"sh", "-c", "echo x > /etc/opm-conf.txt"}); code == 0 {
		t.Fatal("rendered file must be mounted read-only")
	}
	st, err := os.Stat(confHost)
	if err != nil || st.Mode().Perm() != 0o400 {
		t.Fatalf("rendered file on the host: %v %v", st, err)
	}
	// readable image shared by both instances, docker load once
	_ = db
}

func mountedAt(cj *docker.ContainerJSON, target string) bool {
	for _, m := range cj.Mounts {
		if m.Destination == target {
			return true
		}
	}
	return false
}

// snapVolumes lists "<site>/<volumeSlug>" of every part of a snapshot (sorted).
func snapVolumes(s model.Snapshot) string {
	var out []string
	for site, p := range s.Sites {
		for _, v := range p.Volumes {
			out = append(out, site+"/"+v.VolumeSlug)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func findVol(r model.SiteVolumesResponse, slug string) *model.SiteVolumeStatus {
	for i := range r.Volumes {
		if r.Volumes[i].VolumeSlug == slug {
			return &r.Volumes[i]
		}
	}
	return nil
}

func execCat(t *testing.T, dc *docker.Client, name, path string) string {
	t.Helper()
	cj, err := dc.ContainerInspect(context.Background(), name)
	if err != nil {
		t.Fatalf("inspect %s: %v", name, err)
	}
	code, out, err := dc.Exec(context.Background(), cj.ID, []string{"cat", path})
	if err != nil || code != 0 {
		t.Fatalf("cat %s in %s: %d %v %s", path, name, code, err, out)
	}
	return strings.TrimSpace(out)
}

// roundDShared: two containers share one declared volume (api writes, web-2 reads the same file); the siteVolumes
// endpoint (local and through the peer mirror) lists declared volumes, resolved mount paths, users, orphans.
func roundDShared(t *testing.T, dc *docker.Client, dep string, app, db *site) {
	t.Helper()
	ctx := context.Background()
	api, err := dc.ContainerInspect(ctx, "opm_"+dep+"_app_api")
	if err != nil {
		t.Fatal(err)
	}
	if code, out, err := dc.Exec(ctx, api.ID, []string{"sh", "-c", "echo from-api > /shared/hello.txt"}); err != nil || code != 0 {
		t.Fatalf("write in api: %d %v %s", code, err, out)
	}
	if got := execCat(t, dc, "opm_"+dep+"_app_web-2", "/shared/hello.txt"); got != "from-api" {
		t.Fatalf("web-2 sees %q (shared volume)", got)
	}
	if b, _ := os.ReadFile(filepath.Join(app.a.dataDir, "data", "shared", "hello.txt")); strings.TrimSpace(string(b)) != "from-api" {
		t.Fatalf("host dir vol/shared: %q", b)
	}
	// a volume mount is read-write by default (round-C bug: shown read-only)
	w2, _ := dc.ContainerInspect(ctx, "opm_"+dep+"_app_web-2")
	for _, m := range w2.Mounts {
		if m.Destination == "/shared" && !m.RW {
			t.Fatal("/shared must be read-write")
		}
	}
	var av model.SiteVolumesResponse
	if c, b := app.api(t, "GET", "/_opm/api/sites/app/volumes", nil, &av); c != 200 {
		t.Fatalf("app volumes: %d %s", c, b)
	}
	sh := findVol(av, "shared")
	if sh == nil || !sh.Declared || !sh.Backup || sh.Label != "Shared" || len(sh.UsedBy) != 2 || sh.UsedBy[0].ContainerSlug != "api" || sh.UsedBy[1].ContainerSlug != "web-2" || sh.UsedBy[0].MountPath != "/shared" || sh.SizeBytes == nil {
		t.Fatalf("app volumes: %+v", av)
	}
	if c, _ := app.api(t, "GET", "/_opm/api/sites/*/volumes", nil, nil); c != 404 {
		t.Fatalf("volumes for * must be 404: %d", c)
	}
	var dv model.SiteVolumesResponse
	if c, b := app.api(t, "GET", "/_opm/api/sites/db/volumes", nil, &dv); c != 200 || dv.SiteSlug != "db" {
		t.Fatalf("db volumes via the peer mirror: %d %s", c, b)
	}
	kv := findVol(dv, "kv-data")
	if kv == nil || len(kv.UsedBy) != 1 || kv.UsedBy[0].MountPath != "/data" || kv.UsedBy[0].ImageVolume == nil || *kv.UsedBy[0].ImageVolume != "data" || kv.SizeBytes == nil || *kv.SizeBytes == 0 {
		t.Fatalf("kv-data (mount path resolved from /{{ container.env.DATA_DIR }}): %+v", kv)
	}
	if v := findVol(dv, "scratch"); v == nil || v.Backup || !v.Declared || len(v.UsedBy) != 1 || v.UsedBy[0].MountPath != "/scratch" {
		t.Fatalf("scratch: %+v", v)
	}
	if v := findVol(dv, "old-orphan"); v == nil || v.Declared || v.Backup || v.SizeBytes == nil || *v.SizeBytes != 5 || len(v.UsedBy) != 0 {
		t.Fatalf("orphan: %+v", v)
	}
	if len(dv.HostPaths) != 0 || dv.MeasuredAt == "" {
		t.Fatalf("db host paths / measuredAt: %+v", dv)
	}
}
