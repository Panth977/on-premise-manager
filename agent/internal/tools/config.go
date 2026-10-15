package tools

import (
	"bufio"
	"encoding/json"
	"os"
	"os/user"
	"runtime"
	"strings"
)

// Config is the `tools` + `hostShell` part of agent.json (§19.2, §21 #89 b):
//
//	"tools":     { "hostShell": true, "containerExec": true }
//	"hostShell": { "user": "root", "shell": "/bin/bash" }
//
// Both switches default to true. hostShell.user defaults to the user the agent runs as (root in production);
// hostShell.shell defaults to that user's login shell (/etc/passwd), else /bin/bash, else /bin/sh.
type Config struct {
	HostShell     bool
	ContainerExec bool
	ShellUser     string // "" = the agent user
	Shell         string // "" = login shell
	Path          string // agent.json path ("" when the agent runs without one)
}

type fileConfig struct {
	Tools *struct {
		HostShell     *bool `json:"hostShell"`
		ContainerExec *bool `json:"containerExec"`
	} `json:"tools"`
	HostShell *struct {
		User  string `json:"user"`
		Shell string `json:"shell"`
	} `json:"hostShell"`
}

// LoadConfig reads the tools switches from agent.json (missing file / keys = defaults).
func LoadConfig(path string) Config {
	c := Config{HostShell: true, ContainerExec: true, Path: path}
	if path == "" {
		return c
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var fc fileConfig
	if json.Unmarshal(b, &fc) != nil {
		return c
	}
	if fc.Tools != nil {
		if fc.Tools.HostShell != nil {
			c.HostShell = *fc.Tools.HostShell
		}
		if fc.Tools.ContainerExec != nil {
			c.ContainerExec = *fc.Tools.ContainerExec
		}
	}
	if fc.HostShell != nil {
		c.ShellUser = strings.TrimSpace(fc.HostShell.User)
		c.Shell = strings.TrimSpace(fc.HostShell.Shell)
	}
	return c
}

// agentUser is the name of the user the agent runs as.
func agentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "unknown"
}

// shellFor resolves (user, shell) for the host shell.
func (c Config) shellFor() (string, string) {
	name := c.ShellUser
	if name == "" {
		name = agentUser()
	}
	if c.Shell != "" {
		return name, c.Shell
	}
	if sh := passwdShell(name); sh != "" && !strings.HasSuffix(sh, "nologin") && !strings.HasSuffix(sh, "/false") {
		return name, sh
	}
	if runtime.GOOS == "darwin" {
		if v := os.Getenv("SHELL"); v != "" && name == agentUser() {
			return name, v
		}
	}
	for _, sh := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			return name, sh
		}
	}
	return name, "/bin/sh"
}

// passwdShell reads the login shell of a user from /etc/passwd.
func passwdShell(name string) string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Split(sc.Text(), ":")
		if len(fs) >= 7 && fs[0] == name {
			return fs[6]
		}
	}
	return ""
}

// hostShellReason explains why the host shell is unavailable ("" = available).
func (c Config) hostShellReason() string {
	if !c.HostShell {
		return `disabled in agent.json ("tools": {"hostShell": false})`
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "not supported on " + runtime.GOOS
	}
	return ""
}

func (c Config) containerExecReason() string {
	if !c.ContainerExec {
		return `disabled in agent.json ("tools": {"containerExec": false})`
	}
	return ""
}
