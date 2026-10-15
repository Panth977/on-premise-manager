package selfupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Controller stops / starts the agent for a rollback (the watcher runs outside the agent's lifetime).
type Controller interface {
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
}

// SystemdController controls opm-agent.service with systemctl.
type SystemdController struct{ Unit string }

func (s SystemdController) unit() string {
	if s.Unit == "" {
		return UnitName
	}
	return s.Unit
}

// Stop runs systemctl stop.
func (s SystemdController) Stop(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "systemctl", "stop", s.unit()).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl stop %s: %v: %s", s.unit(), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Start runs systemctl start (reset-failed first: a crash loop may have hit the start limit).
func (s SystemdController) Start(ctx context.Context) error {
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", s.unit()).Run()
	if out, err := exec.CommandContext(ctx, "systemctl", "start", s.unit()).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl start %s: %v: %s", s.unit(), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ExecController controls an agent that runs without a supervisor: stop = SIGTERM (then SIGKILL) to the pid, start = a
// detached process with the recorded argv (the binary path is argv[0] / Binary).
type ExecController struct {
	PID    int
	Binary string
	Argv   []string
	LogTo  string // stdout + stderr of the started agent (appended)
}

// Stop terminates the pid (no error when it is already gone).
func (e ExecController) Stop(ctx context.Context) error {
	if e.PID <= 1 || !alive(e.PID) {
		return nil
	}
	_ = syscall.Kill(e.PID, syscall.SIGTERM)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(e.PID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	_ = syscall.Kill(e.PID, syscall.SIGKILL)
	time.Sleep(300 * time.Millisecond)
	return nil
}

// Start starts the binary detached (own session).
func (e ExecController) Start(ctx context.Context) error {
	args := []string{}
	if len(e.Argv) > 1 {
		args = e.Argv[1:]
	}
	cmd := exec.Command(e.Binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	if e.LogTo != "" {
		if f, err := os.OpenFile(e.LogTo, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			defer f.Close()
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	// a zombie child counts as gone
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 2 && f[2] == "Z" {
			return false
		}
	}
	return true
}

// ControllerFor returns the controller recorded in pending.json.
func ControllerFor(p *Pending, logTo string) Controller {
	if p.Supervisor == "systemd" {
		return SystemdController{Unit: p.Unit}
	}
	bin := p.Binary
	return ExecController{PID: p.PID, Binary: bin, Argv: p.Argv, LogTo: logTo}
}

// WatchOptions configure Watch.
type WatchOptions struct {
	DataDir string
	Binary  string // default pending.binary
	ID      string // default pending.upgradeId
	Poll    time.Duration
	Now     func() time.Time
	Ctl     Controller // default ControllerFor(pending)
	Log     io.Writer
}

// Watch outcomes.
const (
	WatchHealthy    = "healthy"     // the new agent wrote the healthy marker
	WatchResolved   = "resolved"    // pending.json disappeared / belongs to another upgrade
	WatchRolledBack = "rolled-back" // this watcher restored opm-agent.prev
	WatchNoPrev     = "no-prev"     // rollback needed but opm-agent.prev is missing
)

// Watch waits for the new agent to report healthy until pending.deadline; on timeout it rolls back.
func Watch(ctx context.Context, o WatchOptions) (string, error) {
	paths := For(o.DataDir)
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Poll <= 0 {
		o.Poll = time.Second
	}
	logf := func(format string, a ...any) {
		if o.Log != nil {
			fmt.Fprintf(o.Log, "%s upgrade-watch: %s\n", Ts(o.Now()), fmt.Sprintf(format, a...))
		}
	}
	_ = WriteFileAtomic(paths.WatchPID(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	defer func() {
		if b, err := os.ReadFile(paths.WatchPID()); err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid()) {
			_ = os.Remove(paths.WatchPID())
		}
	}()
	p := paths.ReadPending()
	if p == nil {
		logf("no pending upgrade")
		return WatchResolved, nil
	}
	if o.ID == "" {
		o.ID = p.UpgradeID
	}
	if p.UpgradeID != o.ID {
		logf("pending.json belongs to upgrade %s, not %s", p.UpgradeID, o.ID)
		return WatchResolved, nil
	}
	if p.Deadline <= 0 {
		logf("upgrade %s has no deadline (no automatic restart): nothing to watch", p.UpgradeID)
		return WatchResolved, nil
	}
	logf("watching upgrade %s (%s → %s) until %s", p.UpgradeID, p.From, p.To, Ts(time.Unix(p.Deadline, 0)))
	lastInfo := ""
	okPolls := 0
	for {
		cur := paths.ReadPending()
		if cur == nil || cur.UpgradeID != o.ID {
			logf("pending upgrade resolved")
			return WatchResolved, nil
		}
		if paths.IsHealthy(o.ID) {
			logf("agent %s reported healthy", p.To)
			return WatchHealthy, nil
		}
		v, ok := infoVersion(ctx, p.InfoURL)
		if v != "" && v != lastInfo && v != p.From { // the old agent may still answer right after the restart request
			lastInfo = v
			logf("local %s answers version %s (ok %v)", p.InfoURL, v, ok)
		}
		// §21 #91: GET /_opm/api/health ok with the target version (3 polls in a row) counts as healthy too
		if ok && v == p.To {
			okPolls++
			if okPolls >= 3 {
				logf("agent %s answers health ok", p.To)
				return WatchHealthy, nil
			}
		} else {
			okPolls = 0
		}
		if o.Now().Unix() >= p.Deadline {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(o.Poll):
		}
	}
	ctl := o.Ctl
	if ctl == nil {
		ctl = ControllerFor(p, "")
	}
	reason := fmt.Sprintf("agent %s did not report healthy within %d s after the restart", p.To, deadlineWindow(p))
	if lastInfo != "" {
		reason += " (the local agent answered version " + lastInfo + ")"
	}
	res, err := Rollback(ctx, paths, o.Binary, o.ID, "watcher", reason, ctl, o.Now)
	logf("rollback: %s %v", res, err)
	return res, err
}

func deadlineWindow(p *Pending) int64 {
	if t, err := time.Parse(time.RFC3339Nano, p.RestartedAt); err == nil {
		return p.Deadline - t.Unix()
	}
	return 60
}

// infoVersion reads the version from GET /_opm/api/health ({ok, version}) or /_opm/api/info ({agentVersion});
// ok = a health answer with ok:true.
func infoVersion(ctx context.Context, url string) (string, bool) {
	if url == "" {
		return "", false
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", url, nil)
	if err != nil {
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var v struct {
		AgentVersion string `json:"agentVersion"`
		Version      string `json:"version"`
		Ok           bool   `json:"ok"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v) != nil {
		return "", false
	}
	if v.Version != "" {
		return v.Version, v.Ok && resp.StatusCode == 200
	}
	return v.AgentVersion, false
}

// Rollback restores opm-agent.prev for upgrade id (shared by the watcher; the boot guard does the same in sh): under
// the rollback lock it re-checks pending + healthy + the binary sha256, stops the agent, restores, writes
// rolled-back.json, removes pending.json and starts the agent again. Idempotent: a second caller finds nothing to do.
func Rollback(ctx context.Context, paths Paths, binary, id, by, reason string, ctl Controller, now func() time.Time) (string, error) {
	if now == nil {
		now = time.Now
	}
	unlock, ok := paths.TryLock()
	if !ok {
		return WatchResolved, errors.New("another rollback holds the lock")
	}
	defer unlock()
	p := paths.ReadPending()
	if p == nil || p.UpgradeID != id {
		return WatchResolved, nil
	}
	if paths.IsHealthy(id) {
		return WatchHealthy, nil
	}
	if binary == "" {
		binary = p.Binary
	}
	if _, err := os.Stat(PrevPath(binary)); err != nil {
		return WatchNoPrev, fmt.Errorf("%w: agent %s is not healthy and %s does not exist — reinstall with the install command", ErrNoPrev, p.To, PrevPath(binary))
	}
	if cur, err := Sha256File(binary); err == nil && p.ToSha256 != "" && cur != p.ToSha256 {
		// the binary is not the new release anymore (restored by the guard, or replaced by hand)
		if prevSha, _ := Sha256File(PrevPath(binary)); prevSha == cur {
			if _, err := os.Stat(paths.RolledBack()); err != nil {
				_ = WriteJSON(paths.RolledBack(), RolledBack{UpgradeID: id, From: p.From, To: p.To, Restored: p.From, Reason: reason, By: by, At: Ts(now())})
			}
		}
		_ = os.Remove(paths.Pending())
		return WatchResolved, nil
	}
	if ctl != nil {
		if err := ctl.Stop(ctx); err != nil {
			// continue: restoring the binary is what matters; the start below (or the guard) brings it back
			_ = err
		}
	}
	if err := Restore(binary); err != nil {
		return "", err
	}
	if err := WriteJSON(paths.RolledBack(), RolledBack{UpgradeID: id, From: p.From, To: p.To, Restored: p.From, Reason: reason, By: by, At: Ts(now())}); err != nil {
		return "", err
	}
	_ = os.Remove(paths.Pending())
	if ctl != nil {
		if err := ctl.Start(ctx); err != nil {
			return WatchRolledBack, fmt.Errorf("restored %s but could not start it: %w", PrevPath(binary), err)
		}
	}
	return WatchRolledBack, nil
}

// SpawnWatcher starts `<prev> upgrade-watch …` outside the agent's lifetime: a transient systemd unit
// (systemd-run --unit opm-agent-upgrade-watch-<id> --collect) under systemd, else a detached setsid process.
func SpawnWatcher(prev string, args []string, systemd bool, id, logTo string) error {
	if systemd {
		if _, err := exec.LookPath("systemd-run"); err == nil {
			full := append([]string{"--quiet", "--collect", "--unit", "opm-agent-upgrade-watch-" + id, prev}, args...)
			if out, err := exec.Command("systemd-run", full...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		}
	}
	cmd := exec.Command(prev, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if logTo != "" {
		if f, err := os.OpenFile(logTo, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			defer f.Close()
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// UnderSystemd reports whether this process runs as a systemd service (INVOCATION_ID is set by systemd ≥ 232) on a
// systemd host.
func UnderSystemd() bool {
	return os.Getenv("INVOCATION_ID") != "" && SystemdAvailable()
}

// SystemctlShow returns ActiveState and SubState of a unit ("" when unknown).
func SystemctlShow(unit string) (string, string) {
	out, err := exec.Command("systemctl", "show", unit, "--property=ActiveState,SubState").Output()
	if err != nil {
		return "", ""
	}
	var act, sub string
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "ActiveState="); ok {
			act = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(l, "SubState="); ok {
			sub = strings.TrimSpace(v)
		}
	}
	return act, sub
}
