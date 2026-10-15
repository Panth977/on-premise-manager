package agent

// The agent self-upgrade engine (SPEC-final §21 #90 b): ONE code path for the GUI (POST …/agent/upgrade), the cloud
// request (CloudView.agentUpgrades), the mesh run (upgrade-all) and `opm-agent upgrade`:
//
//	download (signed GET /v1/agent/release + /v1/agent/binary, Range-resumable, relay works, progress = bytes / sizeBytes)
//	→ verify (Ed25519 release signature with agent.json releasePublicKey else the cloud key; sha256; self-test
//	  `opm-agent.new version` = the pinned version)
//	→ swap (opm-agent.new + fsync, current → opm-agent.prev, rename = atomic)
//	→ restart (pending.json + the known-good watcher; systemctl restart --no-block, else graceful stop + execve)
//	→ health (the NEW agent: selfboot.go).
//
// A failure before the swap leaves the old agent running (no restart). State: <agentHome>/upgrade/state.json +
// history.json (10). Rollback: the watcher + the boot guard (internal/selfupgrade).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/selfupgrade"
)

// UpgradeOptions configure an upgrade started from the CLI (`opm-agent upgrade`).
type UpgradeOptions struct {
	PublicKey string // --pubkey (source "flag")
	Force     bool   // reinstall the same version
	CheckOnly bool   // --check: release check only
	NoRestart bool   // --no-restart: swap only; the operator restarts (no watcher, no deadline)
	Progress  func(p model.AgentUpgradeProgress)
}

// UpgradeResult reports what the CLI upgrade did.
type UpgradeResult struct {
	From, To, Sha256 string
	Target           string
	UpToDate         bool
	CheckOnly        bool
	Restarted        bool
	Progress         *model.AgentUpgradeProgress
	Verification     *model.ReleaseVerification
}

// upgradeStart is one request to upgrade (all entry points).
type upgradeStart struct {
	Version   string // the pinned version the requester saw ("" = whatever is pinned: CLI)
	Force     bool
	Via       string // gui | cloud | cli | mesh
	Actor     *string
	RequestID *string
	RunID     *string
	SessionID string
	FlagKey   string
	NoRestart bool
	Sync      bool // run in the calling goroutine (CLI)
	Progress  func(p model.AgentUpgradeProgress)
}

// upgradeCurrent is the running or last attempt (state.json).
func (a *Agent) upgradeCurrent() *model.AgentUpgradeProgress {
	a.self.mu.Lock()
	defer a.self.mu.Unlock()
	if a.self.current == nil {
		return nil
	}
	cp := *a.self.current
	return &cp
}

// upgradeRunning returns the attempt that is in flight (this process runs it, or it waits for the restart / health).
func (a *Agent) upgradeRunning() *model.AgentUpgradeProgress {
	a.self.mu.Lock()
	defer a.self.mu.Unlock()
	c := a.self.current
	if c == nil || model.UpgradeTerminal(c.Phase) {
		return nil
	}
	if a.self.running {
		cp := *c
		return &cp
	}
	// not running in this process: a restart / health phase in progress (pending.json of this attempt) counts, and so
	// does an attempt another process (the CLI) updated within the last 2 min; a stale state does not (upgradeOnStart
	// marks an interrupted attempt failed)
	if pd := a.self.paths.ReadPending(); (pd != nil && pd.UpgradeID == c.UpgradeID) || time.Since(parseTs(c.UpdatedAt)) < 2*time.Minute {
		cp := *c
		return &cp
	}
	return nil
}

func (a *Agent) upgradeHistory() []model.AgentUpgradeProgress {
	var h []model.AgentUpgradeProgress
	_, _ = selfupgrade.ReadJSON(a.self.paths.History(), &h)
	if h == nil {
		h = []model.AgentUpgradeProgress{}
	}
	return h
}

// saveUpgrade persists the progress (state.json; terminal → history.json, newest first, ≤ 10). phaseChanged → an
// immediate heartbeat (the cloud sees every phase).
func (a *Agent) saveUpgrade(p *model.AgentUpgradeProgress, phaseChanged bool) {
	p.UpdatedAt = nowTs()
	if model.UpgradeTerminal(p.Phase) && p.FinishedAt == nil {
		f := p.UpdatedAt
		p.FinishedAt = &f
	}
	cp := *p
	a.self.mu.Lock()
	a.self.current = &cp
	a.self.lastSave = time.Now()
	a.self.mu.Unlock()
	if err := selfupgrade.WriteJSON(a.self.paths.State(), cp); err != nil {
		a.log.Error("gui", "upgrade state write failed", "error", err.Error())
	}
	if model.UpgradeTerminal(p.Phase) {
		h := a.upgradeHistory()
		out := []model.AgentUpgradeProgress{cp}
		for _, x := range h {
			if x.UpgradeID != cp.UpgradeID {
				out = append(out, x)
			}
		}
		if len(out) > 10 {
			out = out[:10]
		}
		_ = selfupgrade.WriteJSON(a.self.paths.History(), out)
	}
	if phaseChanged {
		a.log.Info("gui", "agent upgrade", "upgradeId", p.UpgradeID, "phase", p.Phase, "from", p.From, "to", p.To, "via", p.Via)
		a.kickLoop("heartbeat")
	}
}

func upgradeActivityData(p *model.AgentUpgradeProgress, extra map[string]any) map[string]any {
	d := map[string]any{"upgradeId": p.UpgradeID, "from": p.From, "to": p.To, "via": p.Via}
	if p.RequestID != nil {
		d["requestId"] = *p.RequestID
	}
	if p.RunID != nil {
		d["runId"] = *p.RunID
	}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

// failUpgrade ends an attempt with phase failed + agent.upgrade-failed.
func (a *Agent) failUpgrade(p *model.AgentUpgradeProgress, phase string, err error) {
	msg := err.Error()
	fp := phase
	p.Phase, p.Error, p.Progress, p.FailedPhase = model.UpgradeFailed, &msg, nil, &fp
	a.saveUpgrade(p, true)
	a.activity(p.Actor, "agent", "agent.upgrade-failed", fmt.Sprintf("agent upgrade to %s failed (%s): %s", p.To, phase, msg),
		upgradeActivityData(p, map[string]any{"phase": phase, "error": msg}))
}

// startUpgrade checks the preconditions and starts the phases (async unless Sync).
func (a *Agent) startUpgrade(ctx context.Context, s upgradeStart) (*model.AgentUpgradeProgress, *httpError) {
	if !a.enrolled {
		return nil, herr(model.ErrInvalid, "this site is not enrolled: enroll it with the install command first", nil)
	}
	// §21 #110 a person may upgrade while an update runs (the cloud-requested upgrade still waits: selfcloud.go)
	a.self.mu.Lock()
	if a.self.running {
		c := a.self.current
		a.self.mu.Unlock()
		msg := "an agent upgrade is running"
		if c != nil {
			msg += fmt.Sprintf(" (%s → %s, phase %s)", c.From, c.To, c.Phase)
		}
		return nil, herr(model.ErrConflict, msg, nil)
	}
	a.self.mu.Unlock()
	if p := a.upgradeRunning(); p != nil {
		return nil, herr(model.ErrConflict, fmt.Sprintf("an agent upgrade is running (%s → %s, phase %s)", p.From, p.To, p.Phase), nil)
	}
	pin := a.pinnedRelease()
	if pin == nil || s.Version == "" { // ask the cloud (also the CLI path)
		rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		rel, e := a.fetchRelease(rctx)
		cancel()
		if e != nil {
			return nil, e
		}
		pin = &model.AgentReleaseInfo{Version: rel.Version, Notes: rel.Notes}
	}
	if s.Version == "" {
		s.Version = pin.Version
	}
	if s.Version != pin.Version {
		return nil, herr(model.ErrStale, fmt.Sprintf("the pinned release is %s now (you asked for %s): reload and try again", pin.Version, s.Version), map[string]any{"pinned": pin.Version})
	}
	if s.Version == a.version && !s.Force {
		return nil, herr(model.ErrInvalid, "this agent runs "+a.version+" already (use force to reinstall it)", map[string]any{"version": a.version})
	}
	if a.self.binary == "" {
		return nil, herr(model.ErrUnavailable, "the agent binary path is unknown", nil)
	}
	now := nowTs()
	p := &model.AgentUpgradeProgress{UpgradeID: newID(), RequestID: s.RequestID, RunID: s.RunID, Via: s.Via, SiteSlug: a.site, From: a.version,
		To: s.Version, Phase: model.UpgradeDownload, Actor: s.Actor, StartedAt: now, UpdatedAt: now}
	a.self.mu.Lock()
	if a.self.running { // a concurrent start won the race
		a.self.mu.Unlock()
		return nil, herr(model.ErrConflict, "an agent upgrade is running", nil)
	}
	a.self.running = true
	a.self.mu.Unlock()
	// a fresh attempt: old markers go
	_ = os.Remove(a.self.paths.Healthy())
	a.saveUpgrade(p, true)
	data := upgradeActivityData(p, nil)
	if s.SessionID != "" {
		data["sessionId"] = s.SessionID
	}
	a.activity(p.Actor, "agent", "agent.upgrade-started", fmt.Sprintf("agent upgrade %s → %s started (%s)", p.From, p.To, p.Via), data)
	cp := *p
	run := func(c context.Context) {
		defer func() {
			a.self.mu.Lock()
			a.self.running = false
			a.self.mu.Unlock()
		}()
		a.runUpgrade(c, p, s)
	}
	if s.Sync {
		run(ctx)
		cp = *p
		return &cp, nil
	}
	bg := a.ctx
	if bg == nil {
		bg = context.Background()
	}
	go run(bg)
	return &cp, nil
}

// runUpgrade runs download → verify → swap → restart for p.
func (a *Agent) runUpgrade(ctx context.Context, p *model.AgentUpgradeProgress, s upgradeStart) {
	notify := func() {
		if s.Progress != nil {
			s.Progress(*p)
		}
	}
	setPhase := func(ph string) {
		p.Phase = ph
		if ph != model.UpgradeDownload {
			p.Progress = nil
		}
		a.saveUpgrade(p, true)
		notify()
	}
	// ── download
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	rel, he := a.fetchRelease(rctx)
	cancel()
	if he != nil {
		a.failUpgrade(p, model.UpgradeDownload, errors.New(he.msg))
		notify()
		return
	}
	if rel.Version != p.To {
		a.failUpgrade(p, model.UpgradeDownload, fmt.Errorf("the pinned release moved to %s; request again", rel.Version))
		notify()
		return
	}
	p.BytesTotal = rel.SizeBytes
	if p.BytesTotal != nil && *p.BytesTotal > 0 {
		z := 0.0
		p.Progress = &z
	}
	a.saveUpgrade(p, false)
	dlDir := a.self.paths.Download()
	_ = os.MkdirAll(dlDir, 0o700)
	name := fmt.Sprintf("opm-agent-%s-%s-%s", sanitizeFileName(rel.Version), runtime.GOOS, runtime.GOARCH)
	dl := filepath.Join(dlDir, name)
	if ents, err := os.ReadDir(dlDir); err == nil { // other versions' leftovers go (this version's .part is resumed)
		for _, e := range ents {
			if !strings.HasPrefix(e.Name(), name) {
				_ = os.Remove(filepath.Join(dlDir, e.Name()))
			}
		}
	}
	if sha, err := fileSha256(dl); err != nil || sha != rel.Sha256 {
		_ = os.Remove(dl)
		var size int64
		if rel.SizeBytes != nil {
			size = *rel.SizeBytes
		}
		progress := func(n int64) {
			p.BytesDone = n
			if p.BytesTotal != nil && *p.BytesTotal > 0 {
				f := float64(n) / float64(*p.BytesTotal)
				if f > 1 {
					f = 1
				}
				p.Progress = &f
			}
			a.self.mu.Lock()
			due := time.Since(a.self.lastSave) > time.Second
			a.self.mu.Unlock()
			if due {
				a.saveUpgrade(p, false)
				notify()
			}
		}
		dctx, dcancel := context.WithTimeout(ctx, 30*time.Minute)
		err := a.cloudDownload(dctx, rel.BinaryPath, dl, "", size, progress)
		dcancel()
		if err != nil {
			a.failUpgrade(p, model.UpgradeDownload, fmt.Errorf("download %s: %w", rel.Version, err))
			notify()
			return
		}
	}
	if st, err := os.Stat(dl); err == nil {
		p.BytesDone = st.Size()
		if p.BytesTotal == nil {
			n := st.Size()
			p.BytesTotal = &n
		}
		one := 1.0
		p.Progress = &one
	}
	// ── verify
	setPhase(model.UpgradeVerify)
	v := a.verifyReleaseSig(rel, s.FlagKey)
	p.Verification = &v
	failVerify := func(err error) {
		if v.Error == nil {
			e := err.Error()
			v.Error = &e
		}
		p.Verification = &v
		a.storeReleaseCheck(rel, &v)
		a.failUpgrade(p, model.UpgradeVerify, err)
		notify()
	}
	if !v.SignatureOk {
		failVerify(errors.New(*v.Error))
		return
	}
	sha, err := fileSha256(dl)
	ok := err == nil && sha == rel.Sha256
	v.Sha256Ok = &ok
	if !ok {
		_ = os.Remove(dl)
		got := sha
		if len(got) > 12 {
			got = got[:12]
		}
		failVerify(fmt.Errorf("sha256 of the download (%s…) is not the pinned %s…", got, rel.Sha256[:min(12, len(rel.Sha256))]))
		return
	}
	newP := selfupgrade.NewPath(a.self.binary)
	if err := selfupgrade.CopyFile(dl, newP, 0o755); err != nil {
		failVerify(fmt.Errorf("stage %s: %w", newP, err))
		return
	}
	if err := selfupgrade.SelfTest(ctx, newP, rel.Version); err != nil {
		f := false
		v.SelfTestOk = &f
		_ = os.Remove(newP)
		failVerify(err)
		return
	}
	t := true
	v.SelfTestOk = &t
	v.CheckedAt = nowTs()
	p.Verification = &v
	a.storeReleaseCheck(rel, &v)
	// ── swap
	setPhase(model.UpgradeSwap)
	systemd := a.self.sup.Kind() == "systemd"
	if systemd && os.Geteuid() == 0 && selfupgrade.SystemdAvailable() {
		if r, err := selfupgrade.InstallGuard(selfupgrade.GuardOptions{DataDir: a.dataDir, Binary: a.self.binary, ScriptPath: a.opt.GuardScript, DropInDir: a.opt.GuardDropIn}); err != nil {
			a.log.Warn("gui", "boot guard install failed (the watcher still covers the upgrade)", "error", err.Error())
		} else if r.Changed {
			a.log.Info("gui", "boot guard installed", "script", r.ScriptPath, "dropIn", r.DropInPath)
		}
	}
	fromSha, _ := fileSha256(a.self.binary)
	if err := selfupgrade.Swap(a.self.binary); err != nil {
		_ = os.Remove(newP)
		a.failUpgrade(p, model.UpgradeSwap, err)
		notify()
		return
	}
	_ = os.Remove(dl)
	// ── restart
	pd := selfupgrade.Pending{UpgradeID: p.UpgradeID, From: p.From, To: p.To, FromSha256: fromSha, ToSha256: rel.Sha256, Binary: a.self.binary,
		Prev: selfupgrade.PrevPath(a.self.binary), Supervisor: "exec", PID: os.Getpid(), Argv: os.Args, DataDir: a.dataDir,
		InfoURL: fmt.Sprintf("http://127.0.0.1:%d/_opm/api/health", a.currentAgentPort()), Via: p.Via, Actor: p.Actor, RequestID: p.RequestID, RunID: p.RunID}
	if systemd {
		pd.Supervisor, pd.Unit = "systemd", selfupgrade.UnitName
	}
	if s.NoRestart {
		pd.RestartedAt = nowTs() // no deadline: the operator restarts; the new agent finishes the upgrade when it starts
		if err := selfupgrade.WriteJSON(a.self.paths.Pending(), pd); err != nil {
			a.log.Warn("gui", "pending.json write failed", "error", err.Error())
		}
		p.Phase = model.UpgradeRestart
		a.saveUpgrade(p, true)
		notify()
		return
	}
	now := time.Now()
	pd.RestartedAt, pd.Deadline = tsOf(now), now.Add(a.self.health).Unix()
	_ = os.Remove(a.self.paths.RolledBack())
	if err := selfupgrade.WriteJSON(a.self.paths.Pending(), pd); err != nil {
		_ = selfupgrade.Restore(a.self.binary)
		a.failUpgrade(p, model.UpgradeRestart, fmt.Errorf("write pending.json: %w (old binary restored)", err))
		notify()
		return
	}
	args := []string{"upgrade-watch", "--data-dir", a.dataDir, "--binary", a.self.binary, "--id", p.UpgradeID}
	if err := a.self.spawn(pd.Prev, args, systemd, p.UpgradeID); err != nil {
		a.log.Warn("gui", "upgrade watcher did not start (the boot guard still covers a crash loop under systemd)", "error", err.Error())
	}
	setPhase(model.UpgradeRestart)
	hctx, hcancel := context.WithTimeout(ctx, 5*time.Second) // the cloud sees "restart" before this process goes away
	a.heartbeatOnce(hctx)
	hcancel()
	if _, err := a.self.sup.Restart(a); err != nil {
		_ = selfupgrade.Restore(a.self.binary)
		_ = os.Remove(a.self.paths.Pending())
		a.failUpgrade(p, model.UpgradeRestart, fmt.Errorf("%w (old binary restored, the agent keeps running)", err))
		notify()
		return
	}
}

func sanitizeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

// SelfUpgrade is `opm-agent upgrade`: the same engine, run synchronously in the CLI process.
func (a *Agent) SelfUpgrade(ctx context.Context, o UpgradeOptions) (*UpgradeResult, error) {
	if !a.enrolled {
		return nil, errors.New("this site is not enrolled yet: run the install command from the cloud Setup page")
	}
	rel, he := a.fetchRelease(ctx)
	if he != nil {
		return nil, errors.New(he.msg)
	}
	v := a.verifyReleaseSig(rel, o.PublicKey)
	a.storeReleaseCheck(rel, &v)
	res := &UpgradeResult{From: a.version, To: rel.Version, Sha256: rel.Sha256, Target: a.self.binary, Verification: &v}
	if !v.SignatureOk {
		return res, errors.New(*v.Error)
	}
	if cur, err := fileSha256(a.self.binary); err == nil && cur == rel.Sha256 && !o.Force {
		res.UpToDate = true
		return res, nil
	}
	if o.CheckOnly {
		res.CheckOnly = true
		return res, nil
	}
	force := o.Force || rel.Version == a.version // same version, other bytes (e.g. a rebuilt dev binary) = reinstall
	p, he := a.startUpgrade(ctx, upgradeStart{Version: rel.Version, Force: force, Via: "cli", FlagKey: o.PublicKey, NoRestart: o.NoRestart, Sync: true, Progress: o.Progress})
	if he != nil {
		return res, errors.New(he.msg)
	}
	res.Progress = p
	if p.Verification != nil {
		res.Verification = p.Verification
	}
	if p.Phase == model.UpgradeFailed {
		return res, errors.New(deref(p.Error))
	}
	res.Restarted = !o.NoRestart
	return res, nil
}
