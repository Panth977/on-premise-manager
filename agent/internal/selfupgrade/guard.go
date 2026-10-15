package selfupgrade

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// GuardScript is the boot guard (upgrade-guard.sh); the agent is its only source (BRIEF-I cross-team agreement).
//
//go:embed upgrade-guard.sh
var GuardScript []byte

// Default install locations of the boot guard.
const (
	DefaultGuardScript = "/usr/local/lib/opm/upgrade-guard.sh"
	DefaultDropInDir   = "/etc/systemd/system/opm-agent.service.d"
	DropInName         = "upgrade-guard.conf"
	DefaultBinary      = "/usr/local/bin/opm-agent"
	DefaultDataDir     = "/var/lib/opm"
	UnitName           = "opm-agent"
)

// GuardOptions configure InstallGuard.
type GuardOptions struct {
	DataDir    string // default /var/lib/opm
	Binary     string // default /usr/local/bin/opm-agent
	ScriptPath string // default /usr/local/lib/opm/upgrade-guard.sh
	DropInDir  string // default /etc/systemd/system/opm-agent.service.d
	NoReload   bool   // skip systemctl daemon-reload
	// Force installs even when not root / no systemd (tests write into a temp dir).
	Force bool
}

func (o *GuardOptions) defaults() {
	if o.DataDir == "" {
		o.DataDir = DefaultDataDir
	}
	if o.Binary == "" {
		o.Binary = DefaultBinary
	}
	if o.ScriptPath == "" {
		o.ScriptPath = DefaultGuardScript
	}
	if o.DropInDir == "" {
		o.DropInDir = DefaultDropInDir
	}
}

// DropIn returns the drop-in content for the options.
func DropIn(o GuardOptions) string {
	o.defaults()
	return fmt.Sprintf("# Written by `opm-agent upgrade-guard --install` (OPM SPEC §21 #90 c): before every start, restore\n"+
		"# %s.prev when an upgraded agent never reported healthy within the deadline.\n"+
		"[Service]\nExecStartPre=-/bin/sh %s %s %s\n", o.Binary, o.ScriptPath, o.DataDir, o.Binary)
}

// GuardResult reports what InstallGuard did.
type GuardResult struct {
	Installed  bool // the script + drop-in are in place now
	Changed    bool // a file was written
	Reloaded   bool
	Skipped    string // why nothing was done (not root / no systemd / not linux)
	ScriptPath string
	DropInPath string
}

// SystemdAvailable reports whether the host runs systemd as init (/run/systemd/system exists) and has systemctl.
func SystemdAvailable() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if st, err := os.Stat("/run/systemd/system"); err != nil || !st.IsDir() {
		return false
	}
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// InstallGuard writes the guard script + the systemd drop-in (idempotent: unchanged files are not rewritten; a
// changed drop-in triggers `systemctl daemon-reload` unless NoReload). Not root / no systemd → Skipped, no error.
func InstallGuard(o GuardOptions) (GuardResult, error) {
	o.defaults()
	res := GuardResult{ScriptPath: o.ScriptPath, DropInPath: filepath.Join(o.DropInDir, DropInName)}
	if !o.Force {
		switch {
		case runtime.GOOS != "linux":
			res.Skipped = "not Linux: the boot guard needs systemd"
			return res, nil
		case os.Geteuid() != 0:
			res.Skipped = "not root: run it with sudo"
			return res, nil
		case !SystemdAvailable():
			res.Skipped = "no systemd on this host: the watcher alone covers upgrades"
			return res, nil
		}
	}
	changed := false
	write := func(path string, data []byte, perm os.FileMode) error {
		if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
			if st, err := os.Stat(path); err == nil && st.Mode().Perm() != perm {
				_ = os.Chmod(path, perm)
			}
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		changed = true
		return WriteFileAtomic(path, data, perm)
	}
	if err := write(o.ScriptPath, GuardScript, 0o755); err != nil {
		return res, fmt.Errorf("write %s: %w", o.ScriptPath, err)
	}
	if err := write(res.DropInPath, []byte(DropIn(o)), 0o644); err != nil {
		return res, fmt.Errorf("write %s: %w", res.DropInPath, err)
	}
	res.Installed, res.Changed = true, changed
	if changed && !o.NoReload && !o.Force {
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			return res, fmt.Errorf("systemctl daemon-reload: %v: %s", err, bytes.TrimSpace(out))
		}
		res.Reloaded = true
	}
	return res, nil
}

// GuardInstalled reports whether the script and the drop-in exist (default paths when empty).
func GuardInstalled(scriptPath, dropInDir string) (bool, string, string) {
	if scriptPath == "" {
		scriptPath = DefaultGuardScript
	}
	if dropInDir == "" {
		dropInDir = DefaultDropInDir
	}
	drop := filepath.Join(dropInDir, DropInName)
	_, e1 := os.Stat(scriptPath)
	_, e2 := os.Stat(drop)
	return e1 == nil && e2 == nil, scriptPath, drop
}
