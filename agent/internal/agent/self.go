package agent

// Agent self-management (SPEC-final §7.5, §21 #90): everything `opm-agent` does on the command line is also in the
// GUI — version / status (GET …/agent), verify-release (POST …/agent/release/check), upgrade (selfupgrade.go,
// selfboot.go), restart, agent.json view + safe settings (selfconfig.go), support bundle (selfbundle.go), mesh
// "Upgrade all" (selfmesh.go) and the cloud upgrade request (selfcloud.go). Routes live in buildSiteMux (per site:
// self, forwarded over the mesh, "*" merged) and apiHandler (upgrade-all).

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/selfupgrade"
	"opm/agent/internal/sign"
)

// Supervisor restarts the agent process (systemd, or a re-exec of the same binary + argv).
type Supervisor interface {
	Kind() string                     // "systemd" | "none"
	Restart(a *Agent) (string, error) // via: "systemd" | "exec"
}

// WatchSpawner starts `<prev> upgrade-watch …` outside the agent's lifetime.
type WatchSpawner func(prev string, args []string, systemd bool, upgradeID string) error

type systemdSupervisor struct{}

// SystemdSupervisor restarts opm-agent.service with `systemctl restart --no-block` (also used by the CLI).
func SystemdSupervisor() Supervisor { return systemdSupervisor{} }

// NoRestartSupervisor never restarts (the CLI without systemd: the operator restarts the agent).
type NoRestartSupervisor struct{}

func (NoRestartSupervisor) Kind() string { return "none" }
func (NoRestartSupervisor) Restart(*Agent) (string, error) {
	return "", errors.New("no supervisor: restart the agent by hand")
}

func (systemdSupervisor) Kind() string { return "systemd" }
func (systemdSupervisor) Restart(a *Agent) (string, error) {
	if out, err := exec.Command("systemctl", "restart", "--no-block", selfupgrade.UnitName).CombinedOutput(); err != nil {
		return "systemd", fmt.Errorf("systemctl restart --no-block opm-agent: %v: %s", err, out)
	}
	return "systemd", nil
}

// execSupervisor: no supervisor → graceful stop, then cmd/opm-agent execve's the same path + argv (ReExecRequested).
type execSupervisor struct{}

func (execSupervisor) Kind() string { return "none" }
func (execSupervisor) Restart(a *Agent) (string, error) {
	a.self.reexec.Store(true)
	go func() {
		defer a.log.Recover("gui", "re-exec")
		time.Sleep(300 * time.Millisecond) // let the HTTP answer go out
		a.Stop()
	}()
	return "exec", nil
}

// ReExecRequested reports whether Run returned because the agent wants to execve itself (no systemd).
func (a *Agent) ReExecRequested() bool { return a.self.reexec.Load() }

// BinaryPath is the opm-agent binary this agent upgrades / re-executes.
func (a *Agent) BinaryPath() string { return a.self.binary }

// selfState is the self-management state of one agent.
type selfState struct {
	a        *Agent
	paths    selfupgrade.Paths
	binary   string
	sup      Supervisor
	spawn    WatchSpawner
	health   time.Duration
	reexec   atomic.Bool
	cfgHash  string // sha256 of agent.json at start (restartPending)
	mu       sync.Mutex
	running  bool                        // an upgrade runs in this process
	current  *model.AgentUpgradeProgress // cache of state.json
	lastSave time.Time
	cloudErr *string
	revoked  *string // the cloud refused the site key (401 unknown-site); message
	prevMemo struct {
		key, sha, version string
	}
	startupDone atomic.Bool // §21 #91 health: Run finished its startup
	restartCmd  []string    // §21 #91: a one-off restart command (socket + service after a socket port change)
	dockerOk    bool
	dockerAt    time.Time
	runCancel   bool // mesh run: cancel requested
	runMu       sync.Mutex
}

func newSelfState(a *Agent) *selfState {
	s := &selfState{a: a, paths: selfupgrade.For(a.dataDir), sup: a.opt.Supervisor, spawn: a.opt.SpawnWatcher, health: a.opt.UpgradeHealth}
	s.binary = a.opt.BinaryPath
	if s.binary == "" {
		if exe, err := os.Executable(); err == nil {
			if r, err := filepath.EvalSymlinks(exe); err == nil {
				exe = r
			}
			s.binary = exe
		}
	}
	if s.sup == nil {
		if selfupgrade.UnderSystemd() {
			s.sup = systemdSupervisor{}
		} else {
			s.sup = execSupervisor{}
		}
	}
	if s.spawn == nil {
		logTo := s.paths.WatchOut()
		s.spawn = func(prev string, args []string, systemd bool, id string) error {
			_ = os.MkdirAll(s.paths.Dir, 0o700)
			return selfupgrade.SpawnWatcher(prev, args, systemd, id, logTo)
		}
	}
	if s.health <= 0 {
		s.health = model.AgentUpgradeHealthSec * time.Second
	}
	_ = os.MkdirAll(s.paths.Dir, 0o700)
	if a.opt.ConfigPath != "" {
		s.cfgHash, _ = fileSha256(a.opt.ConfigPath)
	}
	var revoked string
	if ok, _ := a.db.GetKV("cloudRevoked", &revoked); ok && revoked != "" {
		s.revoked = &revoked
	}
	var cur model.AgentUpgradeProgress
	if ok, _ := selfupgrade.ReadJSON(s.paths.State(), &cur); ok && cur.UpgradeID != "" {
		s.current = &cur
	}
	return s
}

// ───────────────────────── cloud link state (lastError, revoked)

// noteCloudResult records the outcome of a cloud call: ONLY a 401 unknown-site / signature answer marks the site as
// revoked (the cloud no longer knows this site key — every agent-auth refusal is a 401); any 2xx clears it. A 403 is a
// per-call refusal (e.g. "the site runs no persist-to-cloud volume") and never means the key is revoked.
func (a *Agent) noteCloudResult(status int, body model.APIErrorBody) {
	s := a.self
	if s == nil {
		return
	}
	if status < 300 {
		s.mu.Lock()
		was := s.revoked != nil
		s.revoked = nil
		s.cloudErr = nil
		s.mu.Unlock()
		if was {
			_ = a.db.DelKV("cloudRevoked")
		}
		return
	}
	reason, _ := body.Error.Details["reason"].(string)
	if status == 401 && (reason == "unknown-site" || reason == "signature") {
		msg := body.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", status)
		}
		s.mu.Lock()
		changed := s.revoked == nil || *s.revoked != msg
		s.revoked = &msg
		s.mu.Unlock()
		if changed {
			_ = a.db.SetKV("cloudRevoked", msg)
			a.log.Warn("cloudlink", "the cloud refused this site's key (revoked?)", "status", status, "reason", reason, "error", msg)
		}
	}
}

func (a *Agent) noteCloudError(err error) {
	if a.self == nil {
		return
	}
	a.self.mu.Lock()
	defer a.self.mu.Unlock()
	if err == nil {
		a.self.cloudErr = nil
		return
	}
	m := err.Error()
	a.self.cloudErr = &m
}

// ───────────────────────── busy (update / rollback / snapshot / upgrade)

func (a *Agent) selfBusy() *model.AgentBusy {
	if l := a.lock(); l != nil {
		kind := l.Kind
		if kind != "rollback" {
			kind = "update"
		}
		return &model.AgentBusy{Kind: kind, ID: l.UpdateID, Since: l.StartedAt}
	}
	a.mu.Lock()
	for id := range a.snapBusy {
		a.mu.Unlock()
		return &model.AgentBusy{Kind: "snapshot", ID: id, Since: nowTs()}
	}
	a.mu.Unlock()
	if p := a.upgradeRunning(); p != nil {
		return &model.AgentBusy{Kind: "upgrade", ID: p.UpgradeID, Since: p.StartedAt}
	}
	return nil
}

// lockedErr: 409 locked while an update / rollback / snapshot runs.
func (a *Agent) lockedErr() *httpError {
	b := a.selfBusy()
	if b == nil || b.Kind == "upgrade" {
		return nil
	}
	return herr(model.ErrLocked, fmt.Sprintf("a %s is running (%s); try again when it finished", b.Kind, b.ID), map[string]any{"busy": b})
}

// ───────────────────────── GET /agent (version + status parity)

func (a *Agent) handleAgentSelf(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	info := a.selfInfo(r.Context())
	info.Self = getRC(r).peer == nil
	return info, 200, nil
}

func (a *Agent) selfInfo(ctx context.Context) model.AgentSelfInfo {
	s := a.self
	sp := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	info := model.AgentSelfInfo{SiteSlug: a.site, Self: true, Version: a.version, PID: os.Getpid(), StartedAt: tsOf(a.started),
		UptimeSec: int64(time.Since(a.started).Seconds()), DataDir: a.dataDir, BinaryPath: s.binary}
	info.Build = model.AgentBuildInfo{Version: a.version, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, Commit: sp(Commit), BuiltAt: sp(BuiltAt)}
	info.ConfigPath = sp(a.opt.ConfigPath)
	info.PreviousBinary = a.previousBinary(ctx)
	// ports
	er := a.edgeReport()
	a.mu.Lock()
	edge := model.AgentPortsEdge{Running: a.edgeRunning, DefaultEdge: a.edgeDefault, HTTPPort: a.edgeHTTP, HTTPSPort: a.edgeHTTPS, LastError: a.edgeErr}
	a.mu.Unlock()
	_ = er
	var defEdge *int
	if !a.cfg.EdgeHTTPPort.Set {
		v := 80
		defEdge = &v
	} else if a.cfg.EdgeHTTPPort.Value != nil && *a.cfg.EdgeHTTPPort.Value > 0 {
		v := *a.cfg.EdgeHTTPPort.Value
		defEdge = &v
	}
	info.Ports = model.AgentPortsInfo{AgentPort: a.currentAgentPort(), GuiAliasPort: a.currentAliasPort(), DefaultEdgeHTTPPort: defEdge, Edge: edge, GuiPorts: a.guiPorts()}
	if info.Ports.GuiPorts == nil {
		info.Ports.GuiPorts = []model.GuiPort{}
	}
	// docker
	info.Docker = model.AgentDockerInfo{Host: a.dc.Host}
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if v, err := a.dc.Version(dctx); err != nil {
		info.Docker.Error = sp(err.Error())
	} else {
		info.Docker.Ok = true
		info.Docker.Version = sp(v.Version)
		api := a.dc.APIVersion()
		if api == "" {
			if n, err := a.dc.Negotiate(dctx); err == nil {
				api = n
			} else {
				info.Docker.Ok = false
				info.Docker.Error = sp(err.Error())
			}
		}
		info.Docker.APIVersion = sp(api)
	}
	cancel()
	info.Supervisor = a.supervisorInfo()
	info.Enrollment = a.enrollmentInfo()
	info.Cloud = a.cloudInfo()
	info.Release = a.selfRelease()
	info.Upgrade = a.upgradeCurrent()
	info.Busy = a.selfBusy()
	cfgPath := a.opt.ConfigPath
	if cfgPath == "" {
		cfgPath = "/etc/opm/agent.json"
	}
	info.Uninstall = model.UninstallCommands(cfgPath, a.dataDir, s.binary, info.Supervisor.Kind == "systemd")
	return info
}

// previousBinary: opm-agent.prev (+ its version, cached by size + mtime → sha256 → `prev version`).
func (a *Agent) previousBinary(ctx context.Context) model.AgentPreviousBinary {
	s := a.self
	prev := selfupgrade.PrevPath(s.binary)
	out := model.AgentPreviousBinary{Path: prev}
	st, err := os.Stat(prev)
	if err != nil {
		return out
	}
	out.Exists = true
	key := fmt.Sprintf("%s|%d|%d", prev, st.Size(), st.ModTime().UnixNano())
	s.mu.Lock()
	memo := s.prevMemo
	s.mu.Unlock()
	if memo.key != key {
		sha, _ := selfupgrade.Sha256File(prev)
		ver := ""
		if memo.sha == sha && sha != "" {
			ver = memo.version
		} else if line, err := selfupgrade.RunVersion(ctx, prev); err == nil {
			ver = selfupgrade.ParseVersionLine(line)
		}
		memo.key, memo.sha, memo.version = key, sha, ver
		s.mu.Lock()
		s.prevMemo = memo
		s.mu.Unlock()
	}
	if memo.sha != "" {
		v := memo.sha
		out.Sha256 = &v
	}
	if memo.version != "" {
		v := memo.version
		out.Version = &v
	}
	return out
}

func (a *Agent) supervisorInfo() model.AgentSupervisorInfo {
	s := a.self
	out := model.AgentSupervisorInfo{Kind: s.sup.Kind()}
	installed, script, drop := selfupgrade.GuardInstalled(a.opt.GuardScript, a.opt.GuardDropIn)
	out.RestartGuard = model.AgentRestartGuard{Installed: installed}
	if installed {
		out.RestartGuard.ScriptPath, out.RestartGuard.DropInPath = &script, &drop
	}
	if out.Kind == "systemd" {
		u := selfupgrade.UnitName + ".service"
		out.Unit = &u
		if _, ok := s.sup.(systemdSupervisor); ok {
			act, sub := selfupgrade.SystemctlShow(selfupgrade.UnitName)
			if act != "" {
				out.ActiveState, out.SubState = &act, &sub
			}
		}
	}
	return out
}

// SSHFingerprint is "SHA256:" + base64 (no padding) of sha256(raw public key).
func SSHFingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}

func (a *Agent) enrollmentInfo() model.AgentEnrollmentInfo {
	_, kv := a.keyHash()
	out := model.AgentEnrollmentInfo{State: "not-enrolled", DeploymentID: a.dep, SiteSlug: a.site, APIBase: a.cfg.APIBase, KeyVersion: kv}
	fp, pk := SSHFingerprint(a.pub), sign.PublicKeyB64(a.pub)
	out.SiteKeyFingerprint, out.PublicKey = &fp, &pk
	var enr struct {
		EnrolledAt string `json:"enrolledAt"`
	}
	if ok, _ := a.db.GetKV("enrolled", &enr); ok && enr.EnrolledAt != "" {
		e := enr.EnrolledAt
		out.EnrolledAt = &e
	}
	a.self.mu.Lock()
	revoked := a.self.revoked
	a.self.mu.Unlock()
	stamp := time.Now().UTC().Format("20060102")
	switch {
	case a.enrolled && revoked == nil:
		out.State = "enrolled"
	case a.enrolled:
		out.State = "revoked"
		out.ReenrollHelp = []string{
			"# The cloud refused this site's key (" + *revoked + "): the site was revoked or enrolled again elsewhere.",
			"# 1. Cloud console → Setup → site " + a.site + ": revoke it (if not done yet), then create a new install command.",
			"sudo systemctl stop opm-agent",
			"sudo mv " + a.dataDir + " " + a.dataDir + ".revoked-" + stamp,
			"# 2. Paste and run the new install command from the Setup page (it enrolls this server again).",
		}
	default:
		out.ReenrollHelp = []string{
			"# This site is not enrolled yet: it enrolls on its own once the install token exists.",
			"# Cloud console → Setup → site " + a.site + " → Install command, then run it on this server:",
			"curl -fsSL '<install command URL from the Setup page>' | sudo sh",
			"# The token file is " + a.cfg.InstallTokenFile + " (deleted after the enrollment).",
		}
	}
	return out
}

func (a *Agent) cloudInfo() model.AgentCloudInfo {
	out := model.AgentCloudInfo{Route: a.route(), Internet: a.hasInternet()}
	if v := a.cloudView(); v != nil && v.FetchedAt != "" {
		f := v.FetchedAt
		out.LastFetchAt = &f
	}
	var hb string
	if ok, _ := a.db.GetKV("lastHeartbeatAt", &hb); ok && hb != "" {
		out.LastHeartbeatAt = &hb
	}
	a.self.mu.Lock()
	if a.self.cloudErr != nil {
		e := *a.self.cloudErr
		out.LastError = &e
	}
	if a.self.revoked != nil {
		e := "the cloud refused this site's key: " + *a.self.revoked
		out.LastError = &e
	}
	a.self.mu.Unlock()
	ci := a.consoleInfo()
	if ci.ConsoleURL == nil && a.cfg.ConsoleURL != "" {
		u := a.cfg.ConsoleURL
		ci.ConsoleURL = &u
	}
	out.ConsoleURL, out.DeploymentLabel = ci.ConsoleURL, ci.DeploymentLabel
	return out
}

// releaseCheckKV is the last release check (kv "releaseCheck").
type releaseCheckKV struct {
	Version      string                     `json:"version"`
	Notes        *string                    `json:"notes"`
	Sha256       string                     `json:"sha256"`
	SizeBytes    *int64                     `json:"sizeBytes"`
	Verification *model.ReleaseVerification `json:"verification"`
	CheckedAt    string                     `json:"checkedAt"`
}

func (a *Agent) lastReleaseCheck() *releaseCheckKV {
	var c releaseCheckKV
	if ok, _ := a.db.GetKV("releaseCheck", &c); ok && c.Version != "" {
		return &c
	}
	return nil
}

// pinnedRelease: the cloud view's agentRelease, else the last release check.
func (a *Agent) pinnedRelease() *model.AgentReleaseInfo {
	if v := a.cloudView(); v != nil && v.AgentRelease != nil && v.AgentRelease.Version != "" {
		r := *v.AgentRelease
		return &r
	}
	if c := a.lastReleaseCheck(); c != nil {
		return &model.AgentReleaseInfo{Version: c.Version, Notes: c.Notes}
	}
	return nil
}

func (a *Agent) selfRelease() *model.AgentSelfRelease {
	var rel *model.AgentSelfRelease
	if v := a.cloudView(); v != nil && v.AgentRelease != nil && v.AgentRelease.Version != "" {
		rel = &model.AgentSelfRelease{Version: v.AgentRelease.Version, Notes: v.AgentRelease.Notes, Source: "cloud-view"}
	}
	if c := a.lastReleaseCheck(); c != nil && (rel == nil || rel.Version == c.Version) {
		if rel == nil {
			rel = &model.AgentSelfRelease{Version: c.Version, Notes: c.Notes}
		}
		if rel.Notes == nil {
			rel.Notes = c.Notes
		}
		sha, at := c.Sha256, c.CheckedAt
		rel.Sha256, rel.CheckedAt, rel.Verification, rel.Source = &sha, &at, c.Verification, "release-check"
	}
	if rel == nil {
		return nil
	}
	rel.Newer = model.CompareAgentVersions(rel.Version, a.version) >= 0 // the pin is not OLDER (equal = up to date)
	rel.UpgradeAvailable = model.AgentUpgradeAvailable(a.version, rel.Version)
	return rel
}

// ───────────────────────── POST /agent/release/check (verify-release parity)

// fetchRelease: signed GET /v1/agent/release for this os/arch.
func (a *Agent) fetchRelease(ctx context.Context) (*model.AgentReleaseResponse, *httpError) {
	if !a.enrolled {
		return nil, herr(model.ErrInvalid, "this site is not enrolled yet: run the install command from the cloud Setup page", nil)
	}
	q := url.Values{"os": {runtime.GOOS}, "arch": {runtime.GOARCH}}
	var rel model.AgentReleaseResponse
	if err := a.cloudJSON(ctx, "GET", "/v1/agent/release?"+q.Encode(), nil, &rel, callOpts{timeout: 60 * time.Second}); err != nil {
		if ae := asAPIError(err); ae != nil && ae.Status == 404 {
			return nil, herr(model.ErrInvalid, "no agent release is pinned for "+runtime.GOOS+"/"+runtime.GOARCH+": "+ae.Body.Error.Message, nil)
		}
		if ae := asAPIError(err); ae != nil {
			return nil, herr(model.ErrUnavailable, "release info: "+ae.Error(), nil)
		}
		return nil, herr(model.ErrUnavailable, "the cloud is not reachable: "+err.Error(), nil)
	}
	return &rel, nil
}

// releaseKey picks the trusted release key: --pubkey (flag) > agent.json releasePublicKey > the cloud's key. A trusted
// key that differs from the cloud's refuses the release.
func (a *Agent) releaseKey(rel *model.AgentReleaseResponse, flagKey string) (key, source string, err error) {
	switch {
	case flagKey != "":
		key, source = flagKey, "flag"
	case a.cfg.ReleasePublicKey != "":
		key, source = a.cfg.ReleasePublicKey, "agent.json"
	default:
		return rel.ReleasePublicKey, "cloud", nil
	}
	if rel.ReleasePublicKey != "" && rel.ReleasePublicKey != key {
		return key, source, fmt.Errorf("the cloud's release key differs from the trusted key (%s releasePublicKey): refusing the release", source)
	}
	return key, source, nil
}

func keyFingerprint(b64 string) string {
	pub, err := sign.ParsePublicKey(b64)
	if err != nil {
		return ""
	}
	return SSHFingerprint(pub)
}

// verifyReleaseSig fills a ReleaseVerification with the signature result (sha256Ok / selfTestOk stay null).
func (a *Agent) verifyReleaseSig(rel *model.AgentReleaseResponse, flagKey string) model.ReleaseVerification {
	v := model.ReleaseVerification{Version: rel.Version, Sha256: rel.Sha256, CheckedAt: nowTs()}
	key, src, kerr := a.releaseKey(rel, flagKey)
	v.PublicKeySource, v.PublicKeyFingerprint = src, keyFingerprint(key)
	if kerr != nil {
		e := kerr.Error()
		v.Error = &e
		return v
	}
	if err := sign.VerifyRelease(key, rel.Sha256, rel.Signature); err != nil {
		e := fmt.Sprintf("release signature of %s: %v", rel.Version, err)
		v.Error = &e
		return v
	}
	v.SignatureOk = true
	return v
}

func (a *Agent) storeReleaseCheck(rel *model.AgentReleaseResponse, v *model.ReleaseVerification) {
	_ = a.db.SetKV("releaseCheck", releaseCheckKV{Version: rel.Version, Notes: rel.Notes, Sha256: rel.Sha256, SizeBytes: rel.SizeBytes, Verification: v, CheckedAt: v.CheckedAt})
}

func (a *Agent) handleCheckRelease(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	rel, e := a.fetchRelease(ctx)
	if e != nil {
		return nil, 0, e
	}
	v := a.verifyReleaseSig(rel, "")
	a.storeReleaseCheck(rel, &v)
	return a.selfRelease(), 200, nil
}

// ───────────────────────── POST /agent/restart

func (a *Agent) handleRestartAgent(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.RestartAgentRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if req.Confirm != a.site {
		return nil, 0, invalid("type the site slug "+a.site+" to confirm", []model.ValidationIssue{{Path: "confirm", Code: "V_CONFIRM", Message: "type " + a.site, Severity: "error"}})
	}
	if len(req.Reason) > 200 {
		req.Reason = req.Reason[:200]
	}
	// §21 #110 never blocked by a running update (the person decides)
	if p := a.upgradeRunning(); p != nil {
		return nil, 0, herr(model.ErrConflict, "an agent upgrade is running ("+p.To+", phase "+p.Phase+")", nil)
	}
	reason := req.Reason
	if reason == "" {
		reason = "restart from the agent GUI"
	}
	via := a.restartAgent(rc.actorPtr(), actorData(rc, nil), reason)
	return model.RestartAgentResponse{Restarting: true, Via: via}, 202, nil
}

// restartAgent writes agent.restarted and restarts after the answer went out (systemd, else re-exec).
func (a *Agent) restartAgent(actor *string, data map[string]any, reason string) string {
	via := "exec"
	if a.self.sup.Kind() == "systemd" {
		via = "systemd"
	}
	if data == nil {
		data = map[string]any{}
	}
	data["reason"], data["via"] = reason, via
	a.activity(actor, "agent", "agent.restarted", "agent restart requested: "+reason, data)
	a.log.Info("gui", "agent restart requested", "reason", reason, "via", via)
	a.self.mu.Lock()
	cmd := a.self.restartCmd
	a.self.restartCmd = nil
	a.self.mu.Unlock()
	go func() {
		defer a.log.Recover("gui", "restart")
		time.Sleep(300 * time.Millisecond)
		if len(cmd) > 0 {
			if out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput(); err != nil {
				a.log.Error("gui", "agent restart failed", "cmd", fmt.Sprint(cmd), "error", err.Error(), "output", string(out))
			}
			return
		}
		if _, err := a.self.sup.Restart(a); err != nil {
			a.log.Error("gui", "agent restart failed", "error", err.Error())
		}
	}()
	return via
}

// ───────────────────────── upgrade endpoints

func (a *Agent) handleStartUpgrade(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.StartAgentUpgradeRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if req.Confirm != a.site {
		return nil, 0, invalid("type the site slug "+a.site+" to confirm", []model.ValidationIssue{{Path: "confirm", Code: "V_CONFIRM", Message: "type " + a.site, Severity: "error"}})
	}
	if req.Version == "" {
		return nil, 0, invalid("version is required (the pinned version you saw)", []model.ValidationIssue{{Path: "version", Code: "V_REQUIRED", Message: "required", Severity: "error"}})
	}
	via := "gui"
	if rc.peer != nil {
		via = "mesh"
	}
	p, e := a.startUpgrade(r.Context(), upgradeStart{Version: req.Version, Force: req.Force, Via: via, Actor: rc.actorPtr(), SessionID: rc.sessionID})
	if e != nil {
		return nil, 0, e
	}
	return p, 202, nil
}

func (a *Agent) handleUpgradeState(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return model.AgentUpgradeStateResponse{Current: a.upgradeCurrent(), History: a.upgradeHistory()}, 200, nil
}

// registerSelfRoutes adds the per-site agent self-management routes (buildSiteMux).
func (a *Agent) registerSelfRoutes(h func(pattern string, kind authKind, fn apiFn)) {
	h("GET /agent", authSession, a.handleAgentSelf)
	h("POST /agent/release/check", authSession, a.handleCheckRelease)
	h("POST /agent/upgrade", authActor, a.handleStartUpgrade)
	h("GET /agent/upgrade", authSession, a.handleUpgradeState)
	h("POST /agent/restart", authActor, a.handleRestartAgent)
	h("GET /agent/config", authSession, a.handleAgentConfig)
	h("PATCH /agent/config", authActor, a.handlePatchAgentConfig)
	h("POST /agent/support-bundle", authActor, a.handleSupportBundleTicket)
	h("GET /agent/support-bundle/download", authSession, a.handleSupportBundleDownload)
}
