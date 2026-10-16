package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/export"
	"opm/agent/internal/export/exporttest"
	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
)

// composeCmd finds docker compose: $OPM_COMPOSE_BIN, `docker compose`, docker-compose on PATH ("" = none).
func composeCmd() (string, []string) {
	if b := os.Getenv("OPM_COMPOSE_BIN"); b != "" {
		return b, nil
	}
	if exec.Command("docker", "compose", "version").Run() == nil {
		return "docker", []string{"compose"}
	}
	if p, err := exec.LookPath("docker-compose"); err == nil {
		return p, nil
	}
	return "", nil
}

// running-state capture of one side (agent / compose)
type eqContainer struct {
	Image      string            `json:"image"`
	Env        map[string]string `json:"env"`
	Entrypoint []string          `json:"entrypoint"`
	Cmd        []string          `json:"cmd"`
	User       string            `json:"user"`
	WorkingDir string            `json:"workingDir"`
	Mounts     map[string]string `json:"mounts"` // target → "ro|rw sha256:… (file) | dir:{path:sha}"
	Aliases    []string          `json:"aliases"`
	Ports      []string          `json:"ports"` // published (not the agent's loopback ephemeral ones)
	Restart    string            `json:"restart"`
	NanoCpus   int64             `json:"nanoCpus"`
	Memory     int64             `json:"memory"`
	StopSec    int               `json:"stopSec"`
	ExtraHosts []string          `json:"extraHosts"`
	Labels     map[string]string `json:"labels"` // the opm identity labels
	StartedAt  time.Time         `json:"startedAt"`
	HealthyAt  *time.Time        `json:"healthyAt,omitempty"`
}

type eqResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type eqState struct {
	Containers map[string]eqContainer `json:"containers"`
	Edge       map[string]eqResponse  `json:"edge"`
	MQTT       map[string]bool        `json:"mqtt"`
}

func dockerJSON(t *testing.T, args ...string) []map[string]any {
	t.Helper()
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		t.Fatalf("docker %v: %v", args, err)
	}
	var v []map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("docker %v: %v", args, err)
	}
	return v
}

// copyOut is the content of a path in a container (`docker cp` → tar): file → sha256, folder → path → sha256 + modes.
func copyOut(t *testing.T, container, path string) string {
	t.Helper()
	out, err := exec.Command("docker", "cp", container+":"+path, "-").Output()
	if err != nil {
		return "unreadable"
	}
	tr := tar.NewReader(bytes.NewReader(out))
	files := map[string]string{}
	base := ""
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		if base == "" {
			base = strings.TrimSuffix(h.Name, "/")
		}
		name := strings.TrimPrefix(strings.TrimPrefix(h.Name, base), "/")
		switch h.Typeflag {
		case tar.TypeReg:
			s := sha256.Sum256(b)
			v := fmt.Sprintf("%o:%s", h.Mode&0o777, hex.EncodeToString(s[:8]))
			if strings.HasSuffix(name, ".opm-seeded") { // timestamp inside: compare the facts only
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				v = fmt.Sprintf("marker source=%v entries=%v", m["source"], m["entries"])
			}
			files[name] = v
		case tar.TypeDir:
			if name != "" {
				files[name+"/"] = "dir"
			}
		}
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + files[k] + ";")
	}
	return b.String()
}

func strs(v any) []string {
	a, _ := v.([]any)
	out := []string{}
	for _, x := range a {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// imageContent identifies an image by its content: the diff ids of its layers + the image config (entrypoint, cmd,
// env, exposed ports, user, workdir). With the containerd image store a tag pulled as a multi-arch index and the same
// platform image loaded by the agent have different image ids.
func imageContent(t *testing.T, id string) string {
	t.Helper()
	js := dockerJSON(t, "image", "inspect", id)[0]
	cfg := js["Config"].(map[string]any)
	b, _ := json.Marshal([]any{js["RootFS"].(map[string]any)["Layers"], cfg["Entrypoint"], cfg["Cmd"], cfg["Env"], cfg["ExposedPorts"], cfg["User"], cfg["WorkingDir"], js["Architecture"]})
	s := sha256.Sum256(b)
	return "content:" + hex.EncodeToString(s[:12])
}

// captureContainers inspects the OPM containers (by name = containerSlug).
func captureContainers(t *testing.T, names []string, network string, configuredPorts map[int]bool) map[string]eqContainer {
	t.Helper()
	out := map[string]eqContainer{}
	for _, n := range names {
		js := dockerJSON(t, "inspect", n)[0]
		cfg := js["Config"].(map[string]any)
		hc := js["HostConfig"].(map[string]any)
		st := js["State"].(map[string]any)
		c := eqContainer{Image: imageContent(t, fmt.Sprint(js["Image"])), Env: map[string]string{}, Entrypoint: strs(cfg["Entrypoint"]), Cmd: strs(cfg["Cmd"]), User: fmt.Sprint(cfg["User"]),
			WorkingDir: fmt.Sprint(cfg["WorkingDir"]), Mounts: map[string]string{}, Labels: map[string]string{}, ExtraHosts: strs(hc["ExtraHosts"])}
		for _, kv := range strs(cfg["Env"]) {
			if i := strings.IndexByte(kv, '='); i > 0 {
				c.Env[kv[:i]] = kv[i+1:]
			}
		}
		delete(c.Env, "PATH")
		delete(c.Env, "HOSTNAME")
		for _, m := range js["Mounts"].([]any) {
			mm := m.(map[string]any)
			dst := fmt.Sprint(mm["Destination"])
			if dst == export.ToolsDir {
				continue // the compose health-check tool (static busybox, documented)
			}
			mode := "rw"
			if rw, _ := mm["RW"].(bool); !rw {
				mode = "ro"
			}
			c.Mounts[dst] = mode + " " + copyOut(t, n, dst)
		}
		if nets, ok := js["NetworkSettings"].(map[string]any)["Networks"].(map[string]any); ok {
			if nw, ok := nets[network].(map[string]any); ok {
				seen := map[string]bool{}
				for _, al := range strs(nw["Aliases"]) { // compose lists the service name / container name again
					if !seen[al] {
						seen[al] = true
						c.Aliases = append(c.Aliases, al)
					}
				}
				sort.Strings(c.Aliases)
			}
		}
		if pb, ok := hc["PortBindings"].(map[string]any); ok {
			for k, v := range pb {
				for _, b := range v.([]any) {
					bm := b.(map[string]any)
					hp, _ := strconv.Atoi(fmt.Sprint(bm["HostPort"]))
					if !configuredPorts[hp] {
						continue // the agent's loopback ephemeral publication (edge upstream + health checks)
					}
					c.Ports = append(c.Ports, fmt.Sprintf("%s:%d->%s", bm["HostIp"], hp, k))
				}
			}
			sort.Strings(c.Ports)
		}
		if rp, ok := hc["RestartPolicy"].(map[string]any); ok {
			c.Restart = fmt.Sprint(rp["Name"])
		}
		c.NanoCpus = int64(hc["NanoCpus"].(float64))
		c.Memory = int64(hc["Memory"].(float64))
		if s, ok := cfg["StopTimeout"].(float64); ok {
			c.StopSec = int(s)
		}
		for k, v := range cfg["Labels"].(map[string]any) {
			if k == "opm.deployment" || k == "opm.site" || k == "opm.container" || k == "opm.service" {
				c.Labels[k] = fmt.Sprint(v)
			}
		}
		c.StartedAt, _ = time.Parse(time.RFC3339Nano, fmt.Sprint(st["StartedAt"]))
		if h, ok := st["Health"].(map[string]any); ok {
			for _, l := range h["Log"].([]any) {
				lm := l.(map[string]any)
				if code, _ := lm["ExitCode"].(float64); code == 0 {
					at, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(lm["End"]))
					c.HealthyAt = &at
					break
				}
			}
		}
		out[n] = c
	}
	return out
}

type eqReq struct {
	name, method, path string
	body               int
	hdr                map[string]string
}

func captureEdge(t *testing.T, port int, reqs []eqReq) map[string]eqResponse {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 30 * time.Second}
	out := map[string]eqResponse{}
	for _, r := range reqs {
		var body io.Reader
		if r.body > 0 {
			body = bytes.NewReader(bytes.Repeat([]byte("x"), r.body))
		}
		req, _ := http.NewRequest(r.method, fmt.Sprintf("http://127.0.0.1:%d%s", port, r.path), body)
		req.Host = "edge.example.test"
		for k, v := range r.hdr {
			req.Header.Set(k, v)
		}
		resp, err := cl.Do(req)
		if err != nil {
			out[r.name] = eqResponse{Status: -1, Body: err.Error()}
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		h := map[string]string{}
		for _, k := range []string{"Location", "Content-Type", "X-Frame-Options", "X-Opm-Site", "X-Exact"} {
			if v := resp.Header.Get(k); v != "" {
				h[k] = v
			}
		}
		bs := string(b)
		var echo map[string]any
		if json.Unmarshal(b, &echo) == nil && echo["headers"] != nil {
			hd := echo["headers"].(map[string]any)
			for _, k := range []string{"X-Real-Ip", "X-Forwarded-For"} { // the remote address the edge sees (documented)
				if _, ok := hd[k]; ok {
					hd[k] = "<remote>"
				}
			}
			delete(hd, "Accept-Encoding")
			nb, _ := json.Marshal(echo)
			bs = string(nb)
		}
		out[r.name] = eqResponse{Status: resp.StatusCode, Headers: h, Body: bs}
	}
	return out
}

func mqttLogin(port int, user, pw string) bool {
	o := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port)).SetUsername(user).SetPassword(pw).SetClientID("opm-eq-" + strconv.Itoa(int(time.Now().UnixNano()%100000))).SetConnectTimeout(5 * time.Second).SetAutoReconnect(false)
	c := mqtt.NewClient(o)
	tok := c.Connect()
	if !tok.WaitTimeout(8*time.Second) || tok.Error() != nil {
		return false
	}
	c.Disconnect(100)
	return true
}

// TestComposeEquivalenceDocker (§21 #106 PROOF): one site brought up by the real agent (fake cloud, real Docker) and the
// SAME commit brought up by `docker compose up` from the exported tree on the same Docker; containers, images, env,
// mounts (content), network aliases, start / healthy order, published ports, restart / resources and the edge answers
// of every route must match (only the documented non-equivalences are normalised). Mosquitto's password file is
// rendered from a secret with mosquitto_passwd; a folder volume and a single-file volume are seeded.
func TestComposeEquivalenceDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dc := docker.New(docker.DefaultHost(""))
	if err := dc.Ping(context.Background()); err != nil {
		t.Skip("no docker: ", err)
	}
	cbin, cargs := composeCmd()
	if cbin == "" {
		t.Skip("no docker compose (set OPM_COMPOSE_BIN)")
	}
	if exec.Command("docker", "image", "inspect", "eclipse-mosquitto:2").Run() != nil {
		if out, err := exec.Command("docker", "pull", "eclipse-mosquitto:2").CombinedOutput(); err != nil {
			t.Skipf("pull eclipse-mosquitto:2: %v %s", err, out)
		}
	}
	buildTestsrv(t)
	repo := repoRoot(t)
	stamp := time.Now().Format("20060102-150405")
	outDir := filepath.Join(repo, "test-results-compose-equiv", stamp)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".opm-agent-test", "eq-"+stamp)
	_ = os.MkdirAll(root, 0o755)
	defer os.RemoveAll(root)
	dep := "DepEq" + strconv.FormatInt(time.Now().Unix()%1000000000, 10)
	const siteSlug = "site-01"
	network := driver.NetworkName(dep, siteSlug)
	fc := fakecloud.Start(dep)
	defer fc.Close()
	fc.SetAccessKey(testKey)

	// images (the same local images on both sides; the agent loads them from the fake cloud's blobs)
	ts := loadDockerImage(t, fc, "opm-edge-testsrv:1")
	mq := loadDockerImage(t, fc, "eclipse-mosquitto:2")
	spec := func(img testImage, svc, ver string) model.ImageSpec {
		var total int64
		for _, l := range img.layers {
			total += l.SizeBytes
		}
		return model.ImageSpec{ServiceSlug: svc, Version: ver, Digest: "sha256:" + img.manifestHex, ConfigDigest: "sha256:" + img.configHex, Platform: "linux/" + runtime.GOARCH,
			ManifestMediaType: img.mediaType, Layers: img.layers, SizeBytes: total, Kind: "service", Env: []model.ImageEnv{}, Volumes: []model.ImageVolume{}}
	}
	mqs := spec(mq, "mosquitto", "2")
	mqs.Entrypoint, mqs.Command, mqs.StopTimeoutSec = []string{"/docker-entrypoint.sh"}, []string{"/usr/sbin/mosquitto", "-c", "/mosquitto/config/mosquitto.conf"}, 5
	mqs.Ports = []model.ImagePort{{Name: "mqtt", Port: num(1883), Protocol: "tcp"}}
	tss := spec(ts, "testsrv", "1")
	tss.Entrypoint, tss.StopTimeoutSec = []string{"/testsrv"}, 2
	tss.Ports = []model.ImagePort{{Name: "http", Port: num(8080), Protocol: "tcp"}}
	s := &site{slug: siteSlug, agentPort: freePort(t), alias: freePort(t), httpP: freePort(t)}
	mqttPort, webPort := freePort(t), freePort(t)
	fx := exporttest.Build(exporttest.Options{DeploymentID: dep, Site: siteSlug, Images: map[string]model.ImageSpec{"mosquitto:2": mqs, "testsrv:1": tss},
		Pull: map[string]string{"mosquitto:2": "eclipse-mosquitto:2", "testsrv:1": "opm-edge-testsrv:1"}, EdgePort: s.httpP, MQTTPort: mqttPort, WebPort: webPort,
		AgentPort: s.agentPort, AliasPort: s.alias})
	for _, b := range fx.Blobs {
		fc.PutBlob(b)
	}
	names := []string{fx.Slugs.MQTT, fx.Slugs.Web, fx.Slugs.Web2}
	cleanup := func() {
		for _, n := range names {
			_ = exec.Command("docker", "rm", "-f", n).Run()
		}
		cs, _ := dc.ContainerList(context.Background(), true, "opm.deployment="+dep)
		for _, c := range cs {
			_ = dc.ContainerRemove(context.Background(), c.ID, true)
		}
		_ = dc.NetworkRemove(context.Background(), network)
	}
	cleanup()
	defer cleanup()

	// ── A. the agent
	dirA := filepath.Join(root, siteSlug)
	tok := filepath.Join(dirA, "install-token")
	_ = os.MkdirAll(dirA, 0o700)
	_ = os.WriteFile(tok, []byte(fc.CreateInstallToken(siteSlug)), 0o600)
	acfg := model.AgentConfigFile{APIBase: fc.URL, DeploymentID: dep, SiteSlug: siteSlug, DataDir: filepath.Join(dirA, "data"), AgentPort: s.agentPort,
		GuiAliasPort: model.Some(s.alias), AdvertiseHost: "127.0.0.1", InstallTokenFile: tok, EdgeHTTPPort: model.Null[int](), ContainerNames: "plain"}
	a, err := New(Options{Config: acfg, Timings: fastTimings(), ApproveWait: time.Second, CompleteTimeout: 120 * time.Second, LogLevel: "debug", DisableSchedule: true})
	if err != nil {
		t.Fatal(err)
	}
	s.a = a
	jar, _ := cookiejar.New(nil)
	s.cl = &http.Client{Jar: jar, Timeout: 10 * time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	stopped := false
	stopAgent := func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	defer stopAgent()
	waitFor(t, "enroll", 30*time.Second, func() bool { _, ok := fc.Agent(siteSlug); return ok })
	waitFor(t, "key hash", 20*time.Second, func() bool { h, _ := a.keyHash(); return h != "" })
	if c, _ := s.api(t, "POST", "/_opm/api/login", model.LoginRequest{AccessKey: testKey}, nil); c != 200 {
		t.Fatalf("login %d", c)
	}
	var ar model.ActorResponse
	if c, b := s.api(t, "POST", "/_opm/api/actor", model.ActorRequest{Email: "ops@example.com"}, &ar); c != 200 {
		t.Fatalf("actor %d %s", c, b)
	}
	s.actor = ar.ActorToken
	cm := fc.Commit(fx.Config, fx.Catalog, "compose equivalence fixture")
	waitFor(t, "cloud head on the site", 60*time.Second, func() bool {
		var st model.AgentBranchState
		c, _ := s.api(t, "GET", "/_opm/api/branch", nil, &st)
		return c == 200 && st.Cloud.Head != nil && st.Cloud.Head.CommitID == cm.CommitID
	})
	var plan model.MergePlan
	if c, b := s.api(t, "POST", "/_opm/api/branch/compare", model.LocalCompareRequest{Source: map[string]any{"cloudCommitId": cm.CommitID}}, &plan); c != 200 {
		t.Fatalf("compare %d %s", c, b)
	}
	var ch []model.MergeChoice
	for _, it := range plan.Items {
		ch = append(ch, model.MergeChoice{Key: it.Key, Take: "source"})
	}
	var run model.UpdateRun
	for i := 0; ; i++ {
		c, b := s.api(t, "POST", "/_opm/api/branch/merge", model.LocalMergeRequest{SourceCommitID: cm.CommitID, Choices: ch, Message: "take the fixture"}, &run)
		if c == 202 {
			break
		}
		// the Caddy local CA of a fresh data dir is created concurrently (known race, see TestDevmeshBranches): retry
		if i < 5 && strings.Contains(string(b), "pki") {
			time.Sleep(2 * time.Second)
			continue
		}
		t.Fatalf("merge %d %s", c, b)
	}
	waitFor(t, "merge applied", 6*time.Minute, func() bool {
		c, _ := s.api(t, "GET", "/_opm/api/updates/"+run.UpdateID, nil, &run)
		return c == 200 && run.FinishedAt != nil
	})
	if run.Result == nil || !run.Result.Ok {
		t.Fatalf("agent apply failed: %+v", run.Result)
	}
	reqs := []eqReq{
		{"app", "GET", "/app/x?y=1", 0, nil},
		{"app-auto-redirect", "GET", "/app", 0, nil},
		{"api-strip+header", "GET", "/api/v1/devices?limit=5", 0, nil},
		{"exact", "GET", "/exact", 0, nil},
		{"exact-not-prefix", "GET", "/exact/more", 0, nil},
		{"regex", "GET", "/items/42", 0, nil},
		{"regex-miss", "GET", "/items/abc", 0, nil},
		{"redirect-keep-path", "GET", "/old/a/b?q=1", 0, nil},
		{"rewrite", "GET", "/rw/hello?z=2", 0, nil},
		{"body-ok", "POST", "/small/upload", 100, nil},
		{"body-413", "POST", "/small/upload", 4096, nil},
		{"ws-off-400", "GET", "/nows/ws", 0, map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==", "Sec-WebSocket-Version": "13"}},
		{"ws-off-plain", "GET", "/nows/x", 0, nil},
		{"not-found-page", "GET", "/nothing/here", 0, nil},
		{"upstream-404", "GET", "/app/404", 0, nil},
	}
	configured := map[int]bool{mqttPort: true, webPort: true}
	agentState := eqState{Containers: captureContainers(t, names, network, configured), Edge: captureEdge(t, s.httpP, reqs),
		MQTT: map[string]bool{"secret-password": mqttLogin(mqttPort, "northwind", exporttest.MQTTPassword), "wrong-password": mqttLogin(mqttPort, "northwind", "nope")}}
	stopAgent()
	cleanup() // the agent's containers + network go; the compose side recreates them under the same names

	// ── B. the export of the same commit + docker compose up
	req := fx.Req
	req.Commit.ID = cm.CommitID
	res, err := export.Export(req, export.Mode{})
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(outDir, "tree")
	if err := exporttest.WriteTree(res, tree, fx.Blobs); err != nil {
		t.Fatal(err)
	}
	siteDir := filepath.Join(tree, "sites", siteSlug)
	// site.env exactly as an operator writes it (no quotes); Portainer on a free port with its admin password
	portainerPort := freePort(t)
	const portainerPassword = "eq-portainer-Pass#2026"
	var envf strings.Builder
	for _, k := range []string{"OPM_SECRET_API_TOKEN", "OPM_SECRET_DB_PASSWORD", "OPM_SECRET_MQTT_PASSWORD"} {
		envf.WriteString(k + "=" + fx.Env[k] + "\n")
	}
	envf.WriteString("PORTAINER_ADMIN_PASSWORD=" + portainerPassword + "\nPORTAINER_HTTPS_PORT=" + strconv.Itoa(portainerPort) + "\n")
	_ = os.WriteFile(filepath.Join(siteDir, "site.env"), []byte(envf.String()), 0o600)
	deploy := func(args ...string) string {
		t.Helper()
		c := exec.Command("sh", append([]string{"./deploy.sh"}, args...)...)
		c.Dir = siteDir
		if cbin != "docker" { // deploy.sh finds `docker compose` or docker-compose itself; OPM_COMPOSE_BIN is for this test only
			c.Env = append(os.Environ(), "PATH="+filepath.Dir(cbin)+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		out, err := c.CombinedOutput()
		if err != nil {
			logs := exec.Command(cbin, append(append([]string{}, cargs...), "logs", "--no-color")...)
			logs.Dir = siteDir
			lo, _ := logs.CombinedOutput()
			t.Fatalf("./deploy.sh %v: %v\n%s\nlogs:\n%s", args, err, out, lo)
		}
		return string(out)
	}
	defer func() {
		c := exec.Command(cbin, append(append([]string{}, cargs...), "down", "-v", "--remove-orphans")...)
		c.Dir = siteDir
		_ = c.Run()
	}()
	t0 := time.Now()
	t.Logf("./deploy.sh:\n%s", deploy("--wait", "--wait-timeout", "240"))
	evOut, _ := exec.Command("docker", "events", "--since", t0.Add(-time.Second).Format(time.RFC3339Nano), "--until", time.Now().Add(time.Second).Format(time.RFC3339Nano),
		"--filter", "type=container", "--format", "{{json .}}").Output()
	starts, healthy := map[string]int64{}, map[string]int64{}
	for _, l := range strings.Split(strings.TrimSpace(string(evOut)), "\n") {
		var e struct {
			Action   string `json:"Action"`
			TimeNano int64  `json:"timeNano"`
			Actor    struct {
				Attributes map[string]string `json:"Attributes"`
			} `json:"Actor"`
		}
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		n := e.Actor.Attributes["name"]
		if e.Action == "start" && starts[n] == 0 {
			starts[n] = e.TimeNano
		}
		if strings.HasPrefix(e.Action, "health_status: healthy") && healthy[n] == 0 {
			healthy[n] = e.TimeNano
		}
	}
	t.Logf("deploy.sh (compose up --wait): %v", time.Since(t0).Round(time.Second))
	composeState := eqState{Containers: captureContainers(t, names, network, configured), Edge: captureEdge(t, s.httpP, reqs),
		MQTT: map[string]bool{"secret-password": mqttLogin(mqttPort, "northwind", exporttest.MQTTPassword), "wrong-password": mqttLogin(mqttPort, "northwind", "nope")}}

	// ── compare
	var report strings.Builder
	fail := 0
	check := func(aspect string, av, bv any, note string) {
		aj, _ := json.Marshal(av)
		bj, _ := json.Marshal(bv)
		res := "equal"
		if string(aj) != string(bj) {
			res = "DIFFERS"
			fail++
			t.Errorf("%s differs:\n agent:   %s\n compose: %s", aspect, aj, bj)
		}
		if note != "" {
			res += " (" + note + ")"
		}
		fmt.Fprintf(&report, "| %s | %s |\n", aspect, res)
	}
	// the Mosquitto password file: mosquitto_passwd (official image) salts at random, so its bytes differ from the
	// agent's; the format is compared here and the password itself by the MQTT logins below
	reHash := regexp.MustCompile(`(northwind:\$7\$101\$)[A-Za-z0-9+/]+\$[A-Za-z0-9+/=]+`)
	for _, st := range []*eqState{&agentState, &composeState} {
		c := st.Containers[fx.Slugs.MQTT]
		if v, ok := c.Mounts["/mosquitto/config/pwfile"]; ok {
			c.Mounts["/mosquitto/config/pwfile"] = regexp.MustCompile(`:[0-9a-f]{16};`).ReplaceAllString(v, ":<salted hash>;")
		}
		st.Containers[fx.Slugs.MQTT] = c
	}
	if b, err := exec.Command("docker", "exec", fx.Slugs.MQTT, "cat", "/mosquitto/config/pwfile").Output(); err != nil || !reHash.Match(b) {
		t.Errorf("compose pwfile %q %v", b, err)
	}
	for _, n := range names {
		ac, cc := agentState.Containers[n], composeState.Containers[n]
		check(n+" image id", ac.Image, cc.Image, "")
		check(n+" env", ac.Env, cc.Env, map[bool]string{true: "derived TOKEN_HASH computed by deploy.sh (busybox sha256sum)", false: ""}[n == fx.Slugs.Web])
		check(n+" entrypoint + cmd", []any{ac.Entrypoint, ac.Cmd}, []any{cc.Entrypoint, cc.Cmd}, "")
		check(n+" user / workdir", []string{ac.User, ac.WorkingDir}, []string{cc.User, cc.WorkingDir}, "")
		check(n+" mounts (target, ro/rw, content + modes)", ac.Mounts, cc.Mounts, map[bool]string{true: "health-check tool /.tools excluded; pwfile: salted hash", false: "health-check tool /.tools excluded"}[n == fx.Slugs.MQTT])
		check(n+" network aliases", ac.Aliases, cc.Aliases, "")
		check(n+" published ports", ac.Ports, cc.Ports, "agent loopback-ephemeral publications excluded")
		check(n+" restart / cpus / memory / stop timeout / extra hosts", []any{ac.Restart, ac.NanoCpus, ac.Memory, ac.StopSec, ac.ExtraHosts}, []any{cc.Restart, cc.NanoCpus, cc.Memory, cc.StopSec, cc.ExtraHosts}, "")
		check(n+" no OPM labels on the compose side (no agent manages it)", map[string]string{}, cc.Labels, fmt.Sprintf("agent: %d opm.* labels", len(ac.Labels)))
	}
	order := func(st eqState) []string {
		m, w, w2 := st.Containers[fx.Slugs.MQTT], st.Containers[fx.Slugs.Web], st.Containers[fx.Slugs.Web2]
		return []string{fmt.Sprintf("web after mqtt started: %v", w.StartedAt.After(m.StartedAt)), fmt.Sprintf("web-2 after web started: %v", w2.StartedAt.After(w.StartedAt))}
	}
	check("start order (mqtt → web → web-2)", order(agentState), order(composeState), "")
	healthyOrder := healthy[fx.Slugs.MQTT] > 0 && starts[fx.Slugs.Web] >= healthy[fx.Slugs.MQTT] && healthy[fx.Slugs.Web] > 0 && starts[fx.Slugs.Web2] >= healthy[fx.Slugs.Web]
	t.Logf("compose events: start %v healthy %v", starts, healthy)
	check("compose: dependents start after the dependency is healthy", true, healthyOrder, "agent: readiness wait before starting the dependent")
	keys := make([]string, 0, len(reqs))
	for _, r := range reqs {
		keys = append(keys, r.name)
	}
	for _, k := range keys {
		check("edge "+k, agentState.Edge[k], composeState.Edge[k], "")
	}
	check("mqtt login (secret password ok, wrong refused)", agentState.MQTT, composeState.MQTT, "")
	if !composeState.MQTT["secret-password"] || composeState.MQTT["wrong-password"] {
		t.Errorf("mqtt logins: %v", composeState.MQTT)
	}
	// Portainer: up with the admin password of site.env (no first-run setup, no 5-minute timeout)
	pcl := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // Portainer's self-signed certificate
	portainerOK := false
	for i := 0; i < 60 && !portainerOK; i++ {
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": portainerPassword})
		if resp, err := pcl.Post(fmt.Sprintf("https://127.0.0.1:%d/api/auth", portainerPort), "application/json", bytes.NewReader(body)); err == nil {
			var r struct {
				JWT string `json:"jwt"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&r)
			resp.Body.Close()
			portainerOK = resp.StatusCode == 200 && r.JWT != ""
		}
		if !portainerOK {
			time.Sleep(time.Second)
		}
	}
	check("portainer: admin login with the site.env password", true, portainerOK, "compose only (the Docker GUI)")

	// second `up`: no re-seed (seed-once rule)
	vol := "compose-equiv-" + siteSlug + "_data"
	if out, err := exec.Command("docker", "run", "--rm", "-v", vol+":/d", "busybox:1.36", "sh", "-c", "echo changed > /d/hello.txt").CombinedOutput(); err != nil {
		t.Fatalf("edit volume: %v %s", err, out)
	}
	deploy("--wait", "--wait-timeout", "120")
	got := copyOut(t, fx.Slugs.Web2, "/data/hello.txt")
	s2 := sha256.Sum256([]byte("changed\n"))
	reseed := !strings.Contains(got, hex.EncodeToString(s2[:8]))
	check("second up keeps edited volume data (no re-seed)", false, reseed, "")

	// an update that only changes a mounted file (a merged cloud commit): deploy.sh recreates exactly the services that
	// mount it (label files.sha256), like the agent restarts a container whose files changed
	startedOf := func(n string) string {
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.StartedAt}}", n).Output()
		return strings.TrimSpace(string(out))
	}
	before := map[string]string{}
	for _, n := range names {
		before[n] = startedOf(n)
	}
	cfgPath := filepath.Join(siteDir, "files", "app-config")
	old, _ := os.ReadFile(cfgPath)
	newCfg := strings.Replace(string(old), "{", `{"updated":true,`, 1)
	if err := os.WriteFile(cfgPath, []byte(newCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	deploy("--wait", "--wait-timeout", "120")
	gotCfg, _ := exec.Command("docker", "exec", fx.Slugs.Web, export.ToolsDir+"/busybox", "cat", "/etc/app/config.json").Output() // testsrv has no shell tools
	check("update: the changed file reaches the container", newCfg, string(gotCfg), "")
	check("update: only the service mounting it is recreated", []bool{false, true, false},
		[]bool{startedOf(fx.Slugs.MQTT) != before[fx.Slugs.MQTT], startedOf(fx.Slugs.Web) != before[fx.Slugs.Web], startedOf(fx.Slugs.Web2) != before[fx.Slugs.Web2]}, "mqtt, web, web-2")

	raw, _ := json.MarshalIndent(map[string]any{"agent": agentState, "compose": composeState}, "", "  ")
	_ = os.WriteFile(filepath.Join(outDir, "capture.json"), raw, 0o644)
	_ = os.WriteFile(filepath.Join(outDir, "report.md"), []byte("| aspect | result |\n|---|---|\n"+report.String()), 0o644)
	t.Logf("comparison (%d differences), tree + capture in %s:\n| aspect | result |\n|---|---|\n%s", fail, outDir, report.String())
	t.Logf("export notes: %+v", res.Notes)
}
