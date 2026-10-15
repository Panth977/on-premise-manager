package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
	"opm/agent/internal/selfupgrade"
	"opm/agent/internal/sign"
	"opm/agent/internal/store"
)

// ───────────────────────── harness (SPEC §21 #90)

type fakeSup struct {
	mu       sync.Mutex
	restarts int
	ch       chan struct{}
	err      error
}

func newFakeSup() *fakeSup      { return &fakeSup{ch: make(chan struct{}, 10)} }
func (f *fakeSup) Kind() string { return "none" }
func (f *fakeSup) count() int   { f.mu.Lock(); defer f.mu.Unlock(); return f.restarts }
func (f *fakeSup) Restart(*Agent) (string, error) {
	f.mu.Lock()
	f.restarts++
	f.mu.Unlock()
	f.ch <- struct{}{}
	return "exec", f.err
}

type spawnRec struct {
	mu   sync.Mutex
	args [][]string
}

func (s *spawnRec) spawn(prev string, args []string, systemd bool, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.args = append(s.args, append([]string{prev}, args...))
	return nil
}

type upgEnv struct {
	t       *testing.T
	fc      *fakecloud.Cloud
	dir     string
	bin     string
	cfgPath string
	relKey  ed25519.PrivateKey
	sup     *fakeSup
	spawned *spawnRec
	port    int
	site    string
	cfg     model.AgentConfigFile
}

const accessKey = "ABCDEFGHIJKLMNOPQRSTUVWX"

func fakeBin(version string) []byte {
	return []byte("#!/bin/sh\necho opm-agent " + version + " test/test\n")
}

func newUpgEnv(t *testing.T, fc *fakecloud.Cloud, site string) *upgEnv {
	t.Helper()
	dir := t.TempDir()
	e := &upgEnv{t: t, fc: fc, dir: dir, bin: filepath.Join(dir, "bin", "opm-agent"), cfgPath: filepath.Join(dir, "agent.json"), sup: newFakeSup(), spawned: &spawnRec{}, port: freePort(t), site: site}
	_ = os.MkdirAll(filepath.Dir(e.bin), 0o755)
	if err := os.WriteFile(e.bin, fakeBin("0.10.0-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, e.relKey, _ = ed25519.GenerateKey(rand.Reader)
	tok := filepath.Join(dir, "install-token")
	_ = os.WriteFile(tok, []byte(fc.CreateInstallToken(site)), 0o600)
	e.cfg = model.AgentConfigFile{APIBase: fc.URL, DeploymentID: fc.DeploymentID, SiteSlug: site, DataDir: filepath.Join(dir, "data"), AdvertiseHost: "127.0.0.1",
		AgentPort: e.port, GuiAliasPort: model.Null[int](), EdgeHTTPPort: model.Null[int](), InstallTokenFile: tok, ContainerNames: "prefixed",
		ReleasePublicKey: base64.StdEncoding.EncodeToString(e.relKey.Public().(ed25519.PublicKey))}
	raw, _ := json.MarshalIndent(e.cfg, "", "  ")
	// an unknown key + a secret-looking key (masked view, preserved by PATCH)
	raw = []byte(strings.Replace(string(raw), "{", "{\n  \"zzUnknown\": {\"keep\": true},\n  \"proxyPassword\": \"hunter2-secret-1234\",", 1))
	if err := os.WriteFile(e.cfgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *upgEnv) release(version string) {
	e.fc.SetAgentRelease(version, runtime.GOOS, runtime.GOARCH, fakeBin(version), e.relKey)
}

func (e *upgEnv) options(version string) Options {
	return Options{Config: e.cfg, ConfigPath: e.cfgPath, Timings: fastTimings(), Version: version, BinaryPath: e.bin, Supervisor: e.sup,
		SpawnWatcher: e.spawned.spawn, UpgradeHealth: 3 * time.Second, DisableSchedule: true,
		GuardScript: filepath.Join(e.dir, "guard.sh"), GuardDropIn: filepath.Join(e.dir, "dropin")}
}

type running struct {
	a    *Agent
	done chan struct{}
}

func (r *running) stop(t *testing.T) {
	r.a.Stop()
	select {
	case <-r.done:
	case <-time.After(30 * time.Second):
		t.Fatal("agent did not stop")
	}
}

// start runs an agent (as the real process would) and waits until its agent port answers.
func (e *upgEnv) start(version string) *running {
	e.t.Helper()
	a, err := New(e.options(version))
	if err != nil {
		e.t.Fatal(err)
	}
	r := &running{a: a, done: make(chan struct{})}
	go func() { _ = a.Run(context.Background()); close(r.done) }()
	e.t.Cleanup(func() {
		a.Stop()
		select {
		case <-r.done:
		case <-time.After(30 * time.Second):
		}
	})
	waitUntil(e.t, 20*time.Second, "agent port", func() bool {
		resp, err := http.Get(e.url("/_opm/healthz"))
		if err == nil {
			resp.Body.Close()
		}
		return err == nil && resp.StatusCode == 200
	})
	return r
}

func (e *upgEnv) url(p string) string { return fmt.Sprintf("http://127.0.0.1:%d%s", e.port, p) }

func waitUntil(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	until := time.Now().Add(d)
	for time.Now().Before(until) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

type client struct {
	t      *testing.T
	base   string
	cookie string
	site   string
	auth   string
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if c.cookie != "" {
		req.Header.Set("Cookie", "opm_session_"+c.site+"="+c.cookie)
	}
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil && resp.StatusCode < 300 {
			c.t.Fatalf("%s %s: %v %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

// login sets the access key hash, logs in and declares the actor.
func login(t *testing.T, a *Agent, base string) *client {
	t.Helper()
	_ = a.db.SetKV("keyHash", model.AccessKeyRef{Hash: sign.HashAccessKey(accessKey), Version: 1})
	req, _ := http.NewRequest("POST", base+"/_opm/api/login", strings.NewReader(`{"accessKey":"`+accessKey+`"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login %v %v", err, resp)
	}
	resp.Body.Close()
	c := &client{t: t, base: base, site: a.site}
	for _, ck := range resp.Cookies() {
		if ck.Name == "opm_session_"+a.site {
			c.cookie = ck.Value
		}
	}
	var ar model.ActorResponse
	if st := c.do("POST", "/_opm/api/actor", map[string]string{"email": "ops@acme.example"}, &ar); st != 200 {
		t.Fatalf("actor %d", st)
	}
	c.auth = "Actor " + ar.ActorToken
	return c
}

func readState(t *testing.T, dataDir string) model.AgentUpgradeProgress {
	var p model.AgentUpgradeProgress
	_, _ = selfupgrade.ReadJSON(selfupgrade.For(dataDir).State(), &p)
	return p
}

func activitiesOf(a *Agent, typ string) []model.Activity {
	items, _ := a.activitiesPage(0, 1000, typ, "")
	return items
}

func fileIs(t *testing.T, p string, want []byte) bool {
	b, err := os.ReadFile(p)
	return err == nil && string(b) == string(want)
}

// ───────────────────────── tests

// Swap + restart + health OK; the session and the actor token survive the restart (SPEC §21 #90 b, d).
func TestUpgradeHealthySessionSurvives(t *testing.T) {
	fc := fakecloud.Start("DepUpg1")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	r1 := e.start("0.10.0-1")
	waitUntil(t, 20*time.Second, "enroll", func() bool { return r1.a.enrolled })
	c := login(t, r1.a, e.url(""))
	e.release("0.10.0-2")
	fc.SetAgentReleaseNotes("Fixes things.")
	waitUntil(t, 20*time.Second, "cloud view with agentRelease", func() bool { p := r1.a.pinnedRelease(); return p != nil && p.Version == "0.10.0-2" })

	var hl model.AgentHealthResponse
	if st := c.do("GET", "/_opm/api/health", nil, &hl); st != 200 || !hl.Ok || hl.Version != "0.10.0-1" || hl.Schema != store.SchemaVersion || !hl.Cloud.Connected || len(hl.Checks) < 3 {
		t.Fatalf("health %d %+v", st, hl)
	}
	var info model.AgentSelfInfo
	if st := c.do("GET", "/_opm/api/sites/app/agent", nil, &info); st != 200 {
		t.Fatalf("info %d", st)
	}
	if info.Version != "0.10.0-1" || !info.Self || info.Release == nil || info.Release.Version != "0.10.0-2" || !info.Release.UpgradeAvailable ||
		info.Release.Notes == nil || *info.Release.Notes != "Fixes things." || info.Enrollment.State != "enrolled" || info.BinaryPath != e.bin ||
		!strings.HasPrefix(*info.Enrollment.SiteKeyFingerprint, "SHA256:") || info.Build.GoVersion == "" || len(info.Uninstall) == 0 || info.ConfigPath == nil {
		t.Fatalf("info %+v", info)
	}
	var rel model.AgentSelfRelease
	if st := c.do("POST", "/_opm/api/sites/app/agent/release/check", nil, &rel); st != 200 || rel.Verification == nil || !rel.Verification.SignatureOk ||
		rel.Verification.PublicKeySource != "agent.json" || rel.Source != "release-check" || rel.Sha256 == nil {
		t.Fatalf("check %d %+v", st, rel)
	}
	var eb model.APIErrorBody
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "0.10.0-2", Confirm: "nope"}, &eb); st != 422 {
		t.Fatalf("bad confirm %d", st)
	}
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "0.10.0-9", Confirm: "app"}, &eb); st != 409 || eb.Error.Code != "stale" {
		t.Fatalf("stale %d %+v", st, eb)
	}
	noActor := &client{t: t, base: c.base, cookie: c.cookie, site: "app"}
	if st := noActor.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "0.10.0-2", Confirm: "app"}, &eb); st != 401 {
		t.Fatalf("without actor %d", st)
	}
	var p model.AgentUpgradeProgress
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "0.10.0-2", Confirm: "app"}, &p); st != 202 || p.Phase != "download" || p.Via != "gui" {
		t.Fatalf("start %d %+v", st, p)
	}
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "0.10.0-2", Confirm: "app"}, &eb); st != 409 || eb.Error.Code != "conflict" {
		t.Fatalf("second start %d %+v", st, eb)
	}
	select {
	case <-e.sup.ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("no restart; state %+v", readState(t, r1.a.dataDir))
	}
	st := readState(t, r1.a.dataDir)
	if st.Phase != "restart" || st.Verification == nil || st.Verification.Sha256Ok == nil || !*st.Verification.Sha256Ok || st.Verification.SelfTestOk == nil || !*st.Verification.SelfTestOk ||
		st.Actor == nil || *st.Actor != "ops@acme.example" || st.BytesTotal == nil || st.BytesDone != *st.BytesTotal {
		t.Fatalf("state at restart %+v", st)
	}
	if !fileIs(t, e.bin, fakeBin("0.10.0-2")) || !fileIs(t, e.bin+".prev", fakeBin("0.10.0-1")) {
		t.Fatal("swap: binary / .prev wrong")
	}
	pd := selfupgrade.For(r1.a.dataDir).ReadPending()
	if pd == nil || pd.To != "0.10.0-2" || pd.Deadline == 0 || pd.Actor == nil {
		t.Fatalf("pending %+v", pd)
	}
	e.spawned.mu.Lock()
	if len(e.spawned.args) != 1 || e.spawned.args[0][0] != e.bin+".prev" || e.spawned.args[0][1] != "upgrade-watch" {
		t.Fatalf("watcher %v", e.spawned.args)
	}
	e.spawned.mu.Unlock()
	if len(activitiesOf(r1.a, "agent.upgrade-started")) != 1 {
		t.Fatal("agent.upgrade-started")
	}
	r1.stop(t)

	// the new agent (same data dir + port) finishes the upgrade
	r2 := e.start("0.10.0-2")
	waitUntil(t, 20*time.Second, "upgrade done", func() bool { return readState(t, r2.a.dataDir).Phase == "done" })
	if !selfupgrade.For(r2.a.dataDir).IsHealthy(pd.UpgradeID) || selfupgrade.For(r2.a.dataDir).ReadPending() != nil {
		t.Fatal("healthy marker / pending")
	}
	ups := activitiesOf(r2.a, "agent.upgraded")
	if len(ups) != 1 || ups[0].Actor == nil || *ups[0].Actor != "ops@acme.example" || ups[0].Data["to"] != "0.10.0-2" {
		t.Fatalf("agent.upgraded %+v", ups)
	}
	// same cookie + same actor token on the new process
	if st := c.do("GET", "/_opm/api/sites/app/agent", nil, &info); st != 200 || info.Version != "0.10.0-2" || info.PreviousBinary.Version == nil || *info.PreviousBinary.Version != "0.10.0-1" {
		t.Fatalf("after restart %d %+v", st, info.PreviousBinary)
	}
	if st := c.do("POST", "/_opm/api/sites/app/agent/restart", model.RestartAgentRequest{Confirm: "wrong"}, &eb); st != 422 {
		t.Fatalf("actor token after restart: %d %+v", st, eb)
	}
	var us model.AgentUpgradeStateResponse
	if st := c.do("GET", "/_opm/api/sites/app/agent/upgrade", nil, &us); st != 200 || us.Current == nil || us.Current.Phase != "done" || len(us.History) != 1 {
		t.Fatalf("state %d %+v", st, us)
	}
	// the cloud saw the phases (immediate heartbeats)
	waitUntil(t, 15*time.Second, "heartbeat done", func() bool {
		for _, x := range fc.UpgradeReports("app") {
			if x.Phase == "done" {
				return true
			}
		}
		return false
	})
	phases := map[string]bool{}
	for _, x := range fc.UpgradeReports("app") {
		phases[x.Phase] = true
	}
	if !phases["restart"] {
		t.Fatalf("heartbeat phases %v", phases)
	}
	// already on it
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "0.10.0-2", Confirm: "app"}, &eb); st != 422 {
		t.Fatalf("already on it %d", st)
	}
}

// The new binary never becomes healthy → the watcher restores opm-agent.prev; the old agent reports
// agent.upgrade-failed + agent.rolled-back with the original actor.
func TestUpgradeWatcherRollback(t *testing.T) {
	fc := fakecloud.Start("DepUpg2")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	r1 := e.start("0.10.0-1")
	waitUntil(t, 20*time.Second, "enroll", func() bool { return r1.a.enrolled })
	c := login(t, r1.a, e.url(""))
	e.release("0.10.0-2")
	waitUntil(t, 20*time.Second, "pin", func() bool { p := r1.a.pinnedRelease(); return p != nil && p.Version == "0.10.0-2" })
	var p model.AgentUpgradeProgress
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "0.10.0-2", Confirm: "app"}, &p); st != 202 {
		t.Fatalf("start %d", st)
	}
	<-e.sup.ch
	dataDir := r1.a.dataDir
	r1.stop(t) // the "new" agent never comes up

	var old *running
	ctl := &startCtl{start: func() { old = e.start("0.10.0-1") }}
	res, err := selfupgrade.Watch(context.Background(), selfupgrade.WatchOptions{DataDir: dataDir, Poll: 100 * time.Millisecond, Ctl: ctl})
	if err != nil || res != selfupgrade.WatchRolledBack {
		t.Fatalf("watch %s %v", res, err)
	}
	if !fileIs(t, e.bin, fakeBin("0.10.0-1")) {
		t.Fatal("binary not restored")
	}
	waitUntil(t, 15*time.Second, "rolled-back state", func() bool { return readState(t, dataDir).Phase == "rolled-back" })
	st := readState(t, dataDir)
	if st.FailedPhase == nil || *st.FailedPhase != "restart" || st.Error == nil {
		t.Fatalf("state %+v", st)
	}
	// the state file is written before the two activities: wait for them (race fixed in round TX)
	waitUntil(t, 10*time.Second, "rollback activities", func() bool { return len(activitiesOf(old.a, "agent.rolled-back")) == 1 })
	f, rb := activitiesOf(old.a, "agent.upgrade-failed"), activitiesOf(old.a, "agent.rolled-back")
	if len(f) != 1 || len(rb) != 1 || rb[0].Actor == nil || *rb[0].Actor != "ops@acme.example" || rb[0].Data["by"] != "watcher" || rb[0].Data["to"] != "0.10.0-1" {
		t.Fatalf("activities %+v %+v", f, rb)
	}
	// once: a restart of the old agent does not report it again
	old.stop(t)
	r3 := e.start("0.10.0-1")
	time.Sleep(500 * time.Millisecond)
	if len(activitiesOf(r3.a, "agent.rolled-back")) != 1 {
		t.Fatal("rolled-back reported twice")
	}
}

type startCtl struct{ start func() }

func (s *startCtl) Stop(context.Context) error  { return nil }
func (s *startCtl) Start(context.Context) error { s.start(); return nil }

// Crash loop: the new binary exits at once; systemd restarts it until the boot guard (the REAL sh script) restores
// opm-agent.prev after the deadline.
func TestUpgradeBootGuardRollback(t *testing.T) {
	fc := fakecloud.Start("DepUpg3")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	r1 := e.start("0.10.0-1")
	waitUntil(t, 20*time.Second, "enroll", func() bool { return r1.a.enrolled })
	c := login(t, r1.a, e.url(""))
	e.release("0.10.0-2")
	waitUntil(t, 20*time.Second, "pin", func() bool { p := r1.a.pinnedRelease(); return p != nil && p.Version == "0.10.0-2" })
	var p model.AgentUpgradeProgress
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "0.10.0-2", Confirm: "app"}, &p); st != 202 {
		t.Fatalf("start %d", st)
	}
	<-e.sup.ch
	dataDir := r1.a.dataDir
	r1.stop(t)
	script := filepath.Join(t.TempDir(), "upgrade-guard.sh")
	_ = os.WriteFile(script, selfupgrade.GuardScript, 0o755)
	guard := func() {
		out, err := runCmd("/bin/sh", script, dataDir, e.bin)
		if err != nil {
			t.Fatalf("guard: %v %s", err, out)
		}
	}
	guard() // a crash-loop restart inside the window: nothing yet
	if !fileIs(t, e.bin, fakeBin("0.10.0-2")) {
		t.Fatal("guard rolled back before the deadline")
	}
	time.Sleep(3500 * time.Millisecond)
	guard() // past the deadline
	if !fileIs(t, e.bin, fakeBin("0.10.0-1")) {
		t.Fatal("guard did not restore .prev")
	}
	r2 := e.start("0.10.0-1")
	waitUntil(t, 15*time.Second, "rolled-back", func() bool { return readState(t, dataDir).Phase == "rolled-back" })
	waitUntil(t, 10*time.Second, "rollback activity", func() bool { return len(activitiesOf(r2.a, "agent.rolled-back")) == 1 })
	rb := activitiesOf(r2.a, "agent.rolled-back")
	if len(rb) != 1 || rb[0].Data["by"] != "guard" {
		t.Fatalf("%+v", rb)
	}
}

func runCmd(name string, args ...string) (string, error) {
	cmd := execCommand(name, args...)
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// Preconditions: locked during an update / snapshot, conflict during an upgrade, invalid when not enrolled; restart
// refused the same way.
func TestUpgradeRefusals(t *testing.T) {
	a := newTestAgent(t)
	sup := newFakeSup()
	a.self.sup = sup
	srv := newHTTPServer(a)
	defer srv.Close()
	c := login(t, a, srv.URL)
	var hl model.AgentHealthResponse
	if st := c.do("GET", "/_opm/api/health", nil, &hl); st != 503 || hl.Ok || hl.Version == "" {
		t.Fatalf("health before the startup %d %+v", st, hl)
	}
	var eb model.APIErrorBody
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "1.0.0", Confirm: "app"}, &eb); st != 422 || !strings.Contains(eb.Error.Message, "not enrolled") {
		t.Fatalf("not enrolled %d %+v", st, eb)
	}
	a.enrolled = true
	_ = a.db.SetKV("lock", model.UpdateLock{UpdateID: "U1", Kind: "update", RevID: "R", Coordinator: "app", StartedAt: nowTs()})
	// §21 #110 a running update never blocks an upgrade or a restart (the person decides)
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "1.0.0", Confirm: "app"}, &eb); st == 409 && eb.Error.Code == "locked" {
		t.Fatalf("upgrade locked by update %d %+v", st, eb)
	}
	if st := c.do("POST", "/_opm/api/sites/app/agent/restart", model.RestartAgentRequest{Confirm: "app"}, &eb); st != 202 {
		t.Fatalf("restart during an update %d %+v", st, eb)
	}
	select {
	case <-sup.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no restart during the update")
	}
	var info model.AgentSelfInfo
	if c.do("GET", "/_opm/api/sites/app/agent", nil, &info); info.Busy == nil || info.Busy.Kind != "update" {
		t.Fatalf("busy %+v", info.Busy)
	}
	_ = a.db.DelKV("lock")
	a.mu.Lock()
	a.snapBusy["S1"] = true
	a.mu.Unlock()
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "1.0.0", Confirm: "app"}, &eb); st == 409 && eb.Error.Code == "locked" {
		t.Fatalf("upgrade locked by a snapshot %d %+v", st, eb)
	}
	a.mu.Lock()
	delete(a.snapBusy, "S1")
	a.mu.Unlock()
	a.self.mu.Lock()
	a.self.running = true
	a.self.current = &model.AgentUpgradeProgress{UpgradeID: "X", Phase: "download", From: "a", To: "b", UpdatedAt: nowTs()}
	a.self.mu.Unlock()
	if st := c.do("POST", "/_opm/api/sites/app/agent/upgrade", model.StartAgentUpgradeRequest{Version: "1.0.0", Confirm: "app"}, &eb); st != 409 || eb.Error.Code != "conflict" {
		t.Fatalf("conflict %d %+v", st, eb)
	}
	if st := c.do("POST", "/_opm/api/sites/app/agent/restart", model.RestartAgentRequest{Confirm: "app"}, &eb); st != 409 || eb.Error.Code != "conflict" {
		t.Fatalf("restart during upgrade %d", st)
	}
	a.self.mu.Lock()
	a.self.running = false
	a.self.current = nil
	a.self.mu.Unlock()
	var rr model.RestartAgentResponse
	if st := c.do("POST", "/_opm/api/sites/app/agent/restart", model.RestartAgentRequest{Confirm: "app", Reason: "test"}, &rr); st != 202 || !rr.Restarting {
		t.Fatalf("restart %d", st)
	}
	select {
	case <-sup.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no restart")
	}
	if r := activitiesOf(a, "agent.restarted"); len(r) != 2 || r[0].Data["reason"] != "test" || r[0].Actor == nil {
		t.Fatalf("agent.restarted %+v", r)
	}
}

// Cloud request: picked once per requestId (also after a restart), progress in heartbeats, pinned-moved = failed
// without a download.
func TestUpgradeCloudRequest(t *testing.T) {
	fc := fakecloud.Start("DepUpg4")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	r1 := e.start("0.10.0-1")
	waitUntil(t, 20*time.Second, "enroll", func() bool { return r1.a.enrolled })
	e.release("0.10.0-2")
	// a request for a version that is not the pin
	bad := fc.RequestAgentUpgrade("app", "0.10.0-7", "boss@acme.example")
	waitUntil(t, 20*time.Second, "pinned-moved failure", func() bool {
		s := readState(t, r1.a.dataDir)
		return s.RequestID != nil && *s.RequestID == bad.RequestID && s.Phase == "failed"
	})
	if s := readState(t, r1.a.dataDir); !strings.Contains(deref(s.Error), "the pinned release moved to 0.10.0-2") || fc.Requests("/v1/agent/binary") != 0 {
		t.Fatalf("pinned moved: %+v binary requests %d", s, fc.Requests("/v1/agent/binary"))
	}
	waitUntil(t, 15*time.Second, "request cleared", func() bool { return fc.PendingUpgradeRequest("app") == nil })
	req := fc.RequestAgentUpgrade("app", "0.10.0-2", "boss@acme.example")
	select {
	case <-e.sup.ch:
	case <-time.After(40 * time.Second):
		t.Fatalf("no restart; state %+v", readState(t, r1.a.dataDir))
	}
	s := readState(t, r1.a.dataDir)
	if s.Via != "cloud" || s.RequestID == nil || *s.RequestID != req.RequestID || s.Actor == nil || *s.Actor != "boss@acme.example" {
		t.Fatalf("state %+v", s)
	}
	r1.stop(t)
	r2 := e.start("0.10.0-2")
	waitUntil(t, 20*time.Second, "done", func() bool { return readState(t, r2.a.dataDir).Phase == "done" })
	waitUntil(t, 20*time.Second, "request cleared by the done heartbeat", func() bool { return fc.PendingUpgradeRequest("app") == nil })
	phases := []string{}
	for _, x := range fc.UpgradeReports("app") {
		if x.RequestID != nil && *x.RequestID == req.RequestID && (len(phases) == 0 || phases[len(phases)-1] != x.Phase) {
			phases = append(phases, x.Phase)
		}
	}
	if len(phases) < 3 || phases[len(phases)-1] != "done" || !contains(phases, "restart") {
		t.Fatalf("heartbeat phases %v", phases)
	}
	if e.sup.count() != 1 {
		t.Fatalf("restarts %d", e.sup.count())
	}
	if !r2.a.handledRequest(req.RequestID) || !r2.a.handledRequest(bad.RequestID) {
		t.Fatal("handled-requests.json")
	}
	// §21 #92 every heartbeat carries the agent's raw clock (the cloud measures the skew from it)
	at, err := time.Parse(time.RFC3339Nano, fc.LastAgentTime("app"))
	if err != nil || time.Since(at).Abs() > time.Minute {
		t.Fatalf("heartbeat agentTime %q %v", fc.LastAgentTime("app"), err)
	}
}

// Config: masked view, validation, .bak, unknown keys + order kept, activity, restart.
func TestAgentConfigPatch(t *testing.T) {
	fc := fakecloud.Start("DepUpg5")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	a, err := New(e.options("0.10.0-1"))
	if err != nil {
		t.Fatal(err)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	defer func() { a.cancel(); a.db.Close() }()
	srv := newHTTPServer(a)
	defer srv.Close()
	c := login(t, a, srv.URL)
	orig, _ := os.ReadFile(e.cfgPath)
	var v model.AgentConfigView
	if st := c.do("GET", "/_opm/api/sites/app/agent/config", nil, &v); st != 200 || !v.Writable || v.RestartPending {
		t.Fatalf("view %d %+v", st, v)
	}
	mj, _ := json.Marshal(v.Masked)
	if !strings.Contains(string(mj), `"proxyPassword":"••••1234"`) || strings.Contains(string(mj), "hunter2") || !strings.Contains(string(mj), e.cfg.ReleasePublicKey) ||
		!strings.Contains(string(mj), `"installTokenFile"`) {
		t.Fatalf("masked %s", mj)
	}
	if v.Settings.EdgeHTTPPort != nil || v.Settings.GuiAliasPort != nil || v.Settings.ContainerNames != "prefixed" || !v.Settings.Tools.HostShell {
		t.Fatalf("settings %+v", v.Settings)
	}
	var eb model.APIErrorBody
	if st := c.do("PATCH", "/_opm/api/sites/app/agent/config", map[string]any{"settings": map[string]any{"edgeHttpPort": e.port}, "confirm": "app"}, &eb); st != 422 {
		t.Fatalf("clash %d %+v", st, eb)
	}
	if st := c.do("PATCH", "/_opm/api/sites/app/agent/config", map[string]any{"settings": map[string]any{"edgeHttpPort": 8081}, "confirm": "nope"}, &eb); st != 422 {
		t.Fatalf("confirm %d", st)
	}
	var pr model.PatchAgentConfigResponse
	if st := c.do("PATCH", "/_opm/api/sites/app/agent/config", map[string]any{"settings": map[string]any{"edgeHttpPort": 8081, "tools": map[string]any{"hostShell": false}}, "confirm": "app", "restart": false}, &pr); st != 200 ||
		strings.Join(pr.Changed, ",") != "tools.hostShell,edgeHttpPort" || pr.Restarting || !pr.Config.RestartPending || pr.Config.Settings.Tools.HostShell {
		t.Fatalf("patch %d %+v", st, pr)
	}
	if b, _ := os.ReadFile(e.cfgPath + ".bak"); string(b) != string(orig) {
		t.Fatal("agent.json.bak")
	}
	now, _ := os.ReadFile(e.cfgPath)
	if !strings.Contains(string(now), `"zzUnknown": {`) || strings.Index(string(now), "zzUnknown") > strings.Index(string(now), "apiBase") || !strings.Contains(string(now), "hunter2-secret-1234") {
		t.Fatalf("written %s", now)
	}
	var back model.AgentConfigFile
	if err := json.Unmarshal(now, &back); err != nil || back.EdgeHTTPPort.Value == nil || *back.EdgeHTTPPort.Value != 8081 || back.Tools == nil || *back.Tools.HostShell {
		t.Fatalf("parsed %+v %v", back, err)
	}
	if act := activitiesOf(a, "agent.config-changed"); len(act) != 1 || act[0].Actor == nil {
		t.Fatalf("activity %+v", act)
	}
	if strings.Contains(fmt.Sprint(activitiesOf(a, "agent.config-changed")[0].Data), "hunter2") {
		t.Fatal("secret in the activity")
	}
	sup := e.sup
	if st := c.do("PATCH", "/_opm/api/sites/app/agent/config", map[string]any{"settings": map[string]any{"containerNames": "plain"}, "confirm": "app"}, &pr); st != 200 || !pr.Restarting {
		t.Fatalf("patch+restart %d %+v", st, pr)
	}
	select {
	case <-sup.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no restart")
	}
	_ = os.Chmod(e.cfgPath, 0o444)
	defer os.Chmod(e.cfgPath, 0o644)
	if os.Geteuid() != 0 {
		if st := c.do("PATCH", "/_opm/api/sites/app/agent/config", map[string]any{"settings": map[string]any{"containerNames": "prefixed"}, "confirm": "app"}, &eb); st != 403 {
			t.Fatalf("read-only file %d", st)
		}
	}
}

// Support bundle: single-use 5-min link, the sections, masked agent.json, the activity.
func TestSupportBundle(t *testing.T) {
	fc := fakecloud.Start("DepUpg6")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	a, err := New(e.options("0.10.0-1"))
	if err != nil {
		t.Fatal(err)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	defer func() { a.cancel(); a.db.Close() }()
	a.log.Info("gui", "a debug line for the bundle")
	srv := newHTTPServer(a)
	defer srv.Close()
	c := login(t, a, srv.URL)
	var tk struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expiresAt"`
	}
	if st := c.do("POST", "/_opm/api/sites/app/agent/support-bundle", nil, &tk); st != 200 || !strings.Contains(tk.URL, "/agent/support-bundle/download?ticket=") {
		t.Fatalf("ticket %d %+v", st, tk)
	}
	req, _ := http.NewRequest("GET", srv.URL+tk.URL, nil)
	req.Header.Set("Cookie", "opm_session_app="+c.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("download %v %v", err, resp)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
	}
	resp.Body.Close()
	for _, want := range []string{"version.json", "agent.json", "debug/agent-24h.jsonl", "docker-info.json", "containers.json", "activities.jsonl", "status.json", "upgrade/current.json"} {
		if _, ok := files[want]; !ok {
			t.Fatalf("bundle lacks %s (%v)", want, keys(files))
		}
	}
	if strings.Contains(files["agent.json"], "hunter2") || !strings.Contains(files["agent.json"], "••••1234") || strings.Contains(files["version.json"], `"publicKey": "`) ||
		!strings.Contains(files["debug/agent-24h.jsonl"], "a debug line for the bundle") || !strings.Contains(files["activities.jsonl"], "login.succeeded") {
		t.Fatalf("contents: agent.json %s", files["agent.json"])
	}
	if act := activitiesOf(a, "agent.support-bundle-downloaded"); len(act) != 1 || act[0].Actor == nil || len(act[0].Data["sections"].([]any)) != len(model.SupportBundleSections) {
		t.Fatalf("activity %+v", act)
	}
	if st := c.do("GET", tk.URL, nil, nil); st != 401 {
		t.Fatalf("ticket reuse %d", st)
	}
}

func keys(m map[string]string) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Mesh "Upgrade all": db first (forwarded with the actor), the coordinator last; sequential; stop on the first failure;
// "*" merge of GET agent.
func TestUpgradeAllMesh(t *testing.T) {
	meshPollEvery = 200 * time.Millisecond
	defer func() { meshPollEvery = 2 * time.Second }()
	for _, failDB := range []bool{false, true} {
		t.Run(fmt.Sprintf("dbFails=%v", failDB), func(t *testing.T) { upgradeAllCase(t, failDB) })
	}
}

// swappable serves an agent port whose handler can be replaced (a "restarted" agent on the same port).
type swappable struct {
	mu sync.Mutex
	h  http.Handler
}

func (s *swappable) set(h http.Handler) { s.mu.Lock(); s.h = h; s.mu.Unlock() }
func (s *swappable) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := s.h
	s.mu.Unlock()
	h.ServeHTTP(w, r)
}

func upgradeAllCase(t *testing.T, failDB bool) {
	fc := fakecloud.Start("DepUpgMesh")
	defer fc.Close()
	type node struct {
		e   *upgEnv
		a   *Agent
		sw  *swappable
		srv *http.Server
	}
	mk := func(site string) *node {
		e := newUpgEnv(t, fc, site)
		n := &node{e: e, sw: &swappable{}}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", e.port))
		if err != nil {
			t.Fatal(err)
		}
		n.srv = &http.Server{Handler: n.sw}
		go func() { _ = n.srv.Serve(l) }()
		t.Cleanup(func() { n.srv.Close() })
		return n
	}
	app, db := mk("app"), mk("db")
	open := func(n *node, version string) *Agent {
		a, err := New(n.e.options(version))
		if err != nil {
			t.Fatal(err)
		}
		a.ctx, a.cancel = context.WithCancel(context.Background())
		t.Cleanup(func() { a.cancel() })
		n.a = a
		n.sw.set(a.handlerFor("agent"))
		// the test serves the agent port itself (swappable handler): mark it bound + the startup done for /_opm/api/health
		a.mu.Lock()
		a.listeners["agent"] = &listener{purpose: "agent", port: n.e.port, srv: &http.Server{}}
		a.mu.Unlock()
		a.self.startupDone.Store(true)
		return a
	}
	open(app, "0.10.0-1")
	open(db, "0.10.0-1")
	for _, n := range []*node{app, db} {
		if err := n.a.Enroll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// same release key for both sites
	db.e.relKey = app.e.relKey
	db.e.cfg.ReleasePublicKey = app.e.cfg.ReleasePublicKey
	db.a.cfg.ReleasePublicKey = app.e.cfg.ReleasePublicKey
	app.e.release("0.10.0-2")
	peers := []model.PeerInfo{{SiteSlug: "app", PublicKey: sign.PublicKeyB64(app.a.pub), AdvertiseHost: "127.0.0.1", AgentPort: app.e.port, EnrolledAt: nowTs()},
		{SiteSlug: "db", PublicKey: sign.PublicKeyB64(db.a.pub), AdvertiseHost: "127.0.0.1", AgentPort: db.e.port, EnrolledAt: nowTs()}}
	markReachable := func(a *Agent, peer, ver string) {
		a.storePeers(peers)
		a.mu.Lock()
		a.peerState[peer] = &peerRuntime{lastAnswer: time.Now().Add(time.Hour), ping: &pingWire{PeerPing: model.PeerPing{AgentVersion: ver}}}
		a.mu.Unlock()
	}
	markReachable(app.a, "db", "0.10.0-1")
	markReachable(db.a, "app", "0.10.0-1")
	// restarts: db / app come back as a new Agent instance on the same data dir + port
	go func() {
		<-db.e.sup.ch
		if failDB { // the new db never becomes healthy: the watcher rolls back
			dd := db.a.dataDir
			db.a.cancel()
			pd := selfupgrade.For(dd).ReadPending()
			for time.Now().Unix() < pd.Deadline {
				time.Sleep(100 * time.Millisecond)
			}
			_, _ = selfupgrade.Rollback(context.Background(), selfupgrade.For(dd), db.e.bin, pd.UpgradeID, "watcher", "not healthy", nil, nil)
			a := open(db, "0.10.0-1")
			a.upgradeOnStart(a.ctx)
			return
		}
		db.a.cancel()
		a := open(db, "0.10.0-2")
		a.upgradeOnStart(a.ctx)
	}()
	go func() {
		<-app.e.sup.ch
		app.a.cancel()
		a := open(app, "0.10.0-2")
		markReachable(a, "db", "0.10.0-2")
		a.upgradeOnStart(a.ctx)
	}()
	c := login(t, app.a, fmt.Sprintf("http://127.0.0.1:%d", app.e.port))
	var merged model.ListResponse[model.AgentSelfInfo]
	if st := c.do("GET", "/_opm/api/sites/*/agent", nil, &merged); st != 200 || len(merged.Items) != 2 {
		t.Fatalf("merged %d %+v", st, merged)
	}
	selfN := 0
	for _, it := range merged.Items {
		if it.Self {
			selfN++
		}
	}
	if selfN != 1 {
		t.Fatal("exactly one item is self")
	}
	var rs model.AgentUpgradeRunState
	var eb model.APIErrorBody
	if st := c.do("POST", "/_opm/api/agent/upgrade-all", model.StartUpgradeAllRequest{Version: "0.10.0-2", Confirm: "nope"}, &eb); st != 422 {
		t.Fatalf("confirm %d", st)
	}
	if st := c.do("POST", "/_opm/api/agent/upgrade-all", model.StartUpgradeAllRequest{Version: "0.10.0-2", Confirm: "UPGRADE"}, &rs); st != 202 || rs.Run == nil ||
		len(rs.Run.Sites) != 2 || rs.Run.Sites[0].SiteSlug != "db" || rs.Run.Sites[1].SiteSlug != "app" {
		t.Fatalf("start %d %+v", st, rs.Run)
	}
	runID := rs.Run.RunID
	want := "done"
	if failDB {
		want = "failed"
	}
	waitUntil(t, 60*time.Second, "run "+want, func() bool {
		var rf upgradeRunFile
		_, _ = selfupgrade.ReadJSON(selfupgrade.For(app.a.dataDir).Run(), &rf)
		return rf.Run.RunID == runID && rf.Run.Status == want
	})
	var rf upgradeRunFile
	_, _ = selfupgrade.ReadJSON(selfupgrade.For(app.a.dataDir).Run(), &rf)
	if failDB {
		if rf.Run.Sites[0].Status != "failed" || rf.Run.Sites[1].Status != "cancelled" || app.e.sup.count() != 0 {
			t.Fatalf("run %+v restarts %d", rf.Run.Sites, app.e.sup.count())
		}
		if st := readState(t, db.a.dataDir); st.Phase != "rolled-back" || st.Via != "mesh" || st.Actor == nil || *st.Actor != "ops@acme.example" {
			t.Fatalf("db state %+v", st)
		}
		return
	}
	if rf.Run.Sites[0].Status != "done" || rf.Run.Sites[1].Status != "done" || rf.Run.FinishedAt == nil {
		t.Fatalf("run %+v", rf.Run.Sites)
	}
	if readState(t, db.a.dataDir).Phase != "done" || readState(t, app.a.dataDir).Phase != "done" {
		t.Fatal("both sites done")
	}
	// db finished before app restarted (sequential, coordinator last)
	dbDone, appStarted := parseTs(*readState(t, db.a.dataDir).FinishedAt), parseTs(readState(t, app.a.dataDir).StartedAt)
	if !dbDone.Before(appStarted) {
		t.Fatalf("not sequential: db done %v, app started %v", dbDone, appStarted)
	}
}

var execCommand = exec.Command

func newHTTPServer(a *Agent) *httptest.Server { return httptest.NewServer(a.handlerFor("agent")) }
