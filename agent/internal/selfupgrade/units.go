package selfupgrade

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/model"
)

// The systemd units of the agent — ONE source of truth (SPEC-final §19.2, §21 #90 c, §21 #91 f), written by
// `opm-agent systemd-units --install` (install script, `opm-agent install`, the GUI config editor for socket ports).

const (
	DefaultUnitDir = "/etc/systemd/system"
	ServiceFile    = "opm-agent.service"
	SocketFile     = "opm-agent.socket"
	SocketFDName   = "opm"
	// MinSocketSystemd: FileDescriptorName (227) + the restart behaviour we rely on; older systemd → no socket unit.
	MinSocketSystemd = 235
)

// ServiceUnit is the §19.2 unit (binary + agent.json path of this install). socket = Requires/After opm-agent.socket.
func ServiceUnit(binary, configPath string, socket bool) string {
	if binary == "" {
		binary = DefaultBinary
	}
	if configPath == "" {
		configPath = "/etc/opm/agent.json"
	}
	// §21 #112: Wants (not Requires) docker — restarting or stopping Docker no longer stops the agent and its edge; the
	// Docker client reconnects by itself. NotifyAccess=main lets the agent turn the systemd watchdog on itself
	// (sd_notify WATCHDOG_USEC + WATCHDOG=1): an older binary after a roll back sends nothing and has no watchdog.
	// MemoryHigh throttles (never kills) the agent above twice its Go memory limit.
	after, wants, requires := "After=network-online.target docker.service", "Wants=network-online.target docker.service", ""
	if socket {
		after += " " + SocketFile
		requires = "Requires=" + SocketFile + "\n"
	}
	mem := ""
	if h := MemoryHighFor(hostMemBytes()); h > 0 {
		mem = "MemoryHigh=" + strconv.FormatInt(h>>20, 10) + "M\n"
	}
	return "[Unit]\nDescription=On-Prem Manager agent\n" + after + "\n" + wants + "\n" + requires +
		"[Service]\nType=simple\nExecStart=" + binary + " run --config " + configPath + "\nRestart=always\nRestartSec=5\nLimitNOFILE=65536\nKillMode=process\nNotifyAccess=main\n" + mem +
		"[Install]\nWantedBy=multi-user.target\n"
}

// MemoryHighFor: twice the agent's default Go memory limit (min(25 % RAM, 1 GiB), ≥ 256 MiB), at most half the RAM;
// 0 = unknown RAM (no MemoryHigh line).
func MemoryHighFor(mem int64) int64 {
	if mem <= 0 {
		return 0
	}
	const mib = 1 << 20
	l := mem / 4
	if l > 1024*mib {
		l = 1024 * mib
	}
	if l < 256*mib {
		l = 256 * mib
	}
	h := 2 * l
	if h > mem/2 {
		h = mem / 2
	}
	return h
}

// hostMemBytes: MemTotal of /proc/meminfo (0 off Linux / unknown); tests override it.
var hostMemBytes = func() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(ln, "MemTotal:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			return kb * 1024
		}
	}
	return 0
}

// SocketUnit holds the given ports for the agent while it restarts (one unit; the agent matches fds by port).
func SocketUnit(ports []int) string {
	var b strings.Builder
	b.WriteString("# Written by `opm-agent systemd-units --install` (OPM SPEC §21 #91 f): systemd holds the default edge port, the\n")
	b.WriteString("# agent port and the GUI alias port, so connections wait in the kernel queue while the agent restarts.\n")
	b.WriteString("[Unit]\nDescription=On-Prem Manager agent sockets (edge, agent port, GUI alias)\n[Socket]\n")
	for _, p := range ports {
		fmt.Fprintf(&b, "ListenStream=%d\n", p)
	}
	b.WriteString("FileDescriptorName=" + SocketFDName + "\nService=" + ServiceFile + "\nReusePort=false\n[Install]\nWantedBy=sockets.target\n")
	return b.String()
}

// SocketPorts: agent.json edgeHttpPort (default 80, none when null), agentPort (default 7080), guiAliasPort (default
// 8080, none when null) — distinct, sorted.
func SocketPorts(c model.AgentConfigFile) []int {
	seen := map[int]bool{}
	add := func(p int) {
		if p > 0 && p < 65536 {
			seen[p] = true
		}
	}
	if !c.EdgeHTTPPort.Set {
		add(80)
	} else if c.EdgeHTTPPort.Value != nil {
		add(*c.EdgeHTTPPort.Value)
	}
	if c.AgentPort > 0 {
		add(c.AgentPort)
	} else {
		add(7080)
	}
	if !c.GuiAliasPort.Set {
		add(8080)
	} else if c.GuiAliasPort.Value != nil {
		add(*c.GuiAliasPort.Value)
	}
	out := []int{}
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// SystemdVersion returns the systemd version (`systemctl --version` first line), 0 when unknown.
func SystemdVersion() int {
	out, err := exec.Command("systemctl", "--version").Output()
	if err != nil {
		return 0
	}
	m := regexp.MustCompile(`systemd (\d+)`).FindSubmatch(out)
	if m == nil {
		return 0
	}
	v, _ := strconv.Atoi(string(m[1]))
	return v
}

// UnitOptions configure InstallUnits.
type UnitOptions struct {
	ConfigPath string // agent.json (ExecStart, socket ports)
	Binary     string
	DataDir    string
	Socket     bool  // install opm-agent.socket (false = remove an existing one)
	Ports      []int // socket ports (default: from agent.json)
	UnitDir    string
	ScriptPath string // guard script
	NoReload   bool
	Force      bool // tests: write into UnitDir without root / systemd checks and without systemctl
}

// UnitResult reports what InstallUnits did.
type UnitResult struct {
	Skipped        string
	Written        []string
	Removed        []string
	Socket         bool // the socket unit is installed now
	SocketPorts    []int
	Notes          []string
	Reloaded       bool
	SocketEnabled  bool
	SocketDisabled bool
}

func writeIfChanged(path string, data []byte, perm os.FileMode) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, WriteFileAtomic(path, data, perm)
}

// SocketInstalled reports whether <unitDir>/opm-agent.socket exists.
func SocketInstalled(unitDir string) bool {
	if unitDir == "" {
		unitDir = DefaultUnitDir
	}
	_, err := os.Stat(filepath.Join(unitDir, SocketFile))
	return err == nil
}

// InstallUnits writes opm-agent.service (+ opm-agent.socket, or removes it) + the guard drop-in; idempotent.
func InstallUnits(o UnitOptions) (UnitResult, error) {
	var res UnitResult
	if o.UnitDir == "" {
		o.UnitDir = DefaultUnitDir
	}
	if o.Binary == "" {
		o.Binary = DefaultBinary
	}
	if o.ConfigPath == "" {
		o.ConfigPath = "/etc/opm/agent.json"
	}
	if !o.Force {
		switch {
		case runtime.GOOS != "linux":
			res.Skipped = "not Linux: no systemd units"
			return res, nil
		case os.Geteuid() != 0:
			res.Skipped = "not root: run it with sudo"
			return res, nil
		case !SystemdAvailable():
			res.Skipped = "no systemd on this host: start `opm-agent run` yourself"
			return res, nil
		}
		if o.Socket {
			if v := SystemdVersion(); v > 0 && v < MinSocketSystemd {
				o.Socket = false
				res.Notes = append(res.Notes, fmt.Sprintf("systemd %d < %d: no socket activation (the agent binds its ports itself)", v, MinSocketSystemd))
			}
		}
	}
	if o.Socket && len(o.Ports) == 0 {
		var c model.AgentConfigFile
		if ok, err := ReadJSON(o.ConfigPath, &c); err != nil || !ok {
			return res, fmt.Errorf("read %s for the socket ports: %v", o.ConfigPath, err)
		}
		o.Ports = SocketPorts(c)
	}
	svcPath, sockPath := filepath.Join(o.UnitDir, ServiceFile), filepath.Join(o.UnitDir, SocketFile)
	hadSocket := SocketInstalled(o.UnitDir)
	changed := false
	if c, err := writeIfChanged(svcPath, []byte(ServiceUnit(o.Binary, o.ConfigPath, o.Socket)), 0o644); err != nil {
		return res, err
	} else if c {
		changed = true
		res.Written = append(res.Written, svcPath)
	}
	g, err := InstallGuard(GuardOptions{DataDir: o.DataDir, Binary: o.Binary, ScriptPath: o.ScriptPath, DropInDir: filepath.Join(o.UnitDir, ServiceFile+".d"), NoReload: true, Force: true})
	if err != nil {
		return res, err
	}
	if g.Changed {
		changed = true
		res.Written = append(res.Written, g.DropInPath)
	}
	systemctl := func(args ...string) error {
		if o.Force {
			return nil
		}
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
		}
		return nil
	}
	if o.Socket {
		c, err := writeIfChanged(sockPath, []byte(SocketUnit(o.Ports)), 0o644)
		if err != nil {
			return res, err
		}
		if c {
			changed = true
			res.Written = append(res.Written, sockPath)
		}
		res.Socket, res.SocketPorts = true, o.Ports
	}
	if changed && !o.NoReload {
		if err := systemctl("daemon-reload"); err != nil {
			return res, err
		}
		res.Reloaded = !o.Force
	}
	if o.Socket && !hadSocket {
		if err := systemctl("enable", SocketFile); err != nil {
			res.Notes = append(res.Notes, err.Error())
		} else {
			res.SocketEnabled = true
		}
	}
	if !o.Socket && hadSocket {
		if err := systemctl("disable", "--now", SocketFile); err != nil {
			res.Notes = append(res.Notes, err.Error())
		}
		res.SocketDisabled = true
		if err := os.Remove(sockPath); err == nil {
			res.Removed = append(res.Removed, sockPath)
		}
		if !o.NoReload {
			_ = systemctl("daemon-reload")
		}
	}
	return res, nil
}
