//go:build linux || darwin

package tools

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const ptySupported = true

// hostProc is a shell running on a PTY.
type hostProc struct {
	cmd    *exec.Cmd
	master *os.File
}

// startHostShell starts shell (login) as userName on a new PTY.
func startHostShell(userName, shell string, cols, rows int, extraEnv []string) (*hostProc, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	defer slave.Close()
	setWinsize(master, cols, rows)
	cmd := exec.Command(shell, "-l")
	home := "/"
	env := []string{"TERM=xterm-256color", "COLORTERM=truecolor", "LANG=C.UTF-8", "SHELL=" + shell,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	attr := &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	cur := agentUser()
	u, uerr := user.Lookup(userName)
	if uerr == nil {
		home = u.HomeDir
		if userName != cur {
			if os.Geteuid() != 0 {
				slave.Close()
				master.Close()
				return nil, fmt.Errorf("the agent runs as %s and cannot start a shell as %s (set hostShell.user to %s, or run the agent as root)", cur, userName, cur)
			}
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			var groups []uint32
			if gs, err := u.GroupIds(); err == nil {
				for _, g := range gs {
					if n, err := strconv.Atoi(g); err == nil {
						groups = append(groups, uint32(n))
					}
				}
			}
			attr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}
		}
	} else if userName != cur {
		slave.Close()
		master.Close()
		return nil, fmt.Errorf("host shell user %q does not exist on this server", userName)
	}
	env = append(env, "HOME="+home, "USER="+userName, "LOGNAME="+userName)
	env = append(env, extraEnv...)
	cmd.Env = env
	cmd.Dir = home
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		cmd.Dir = "/"
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, fmt.Errorf("start %s: %w", shell, err)
	}
	return &hostProc{cmd: cmd, master: master}, nil
}

func setWinsize(f *os.File, cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	_ = unix.IoctlSetWinsize(int(f.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
}

func (p *hostProc) Read(b []byte) (int, error)  { return p.master.Read(b) }
func (p *hostProc) Write(b []byte) (int, error) { return p.master.Write(b) }
func (p *hostProc) Resize(cols, rows int)       { setWinsize(p.master, cols, rows) }

// Kill hangs up the whole session (process group) and closes the PTY.
func (p *hostProc) Kill() {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGHUP)
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	}
	_ = p.master.Close()
}

// Wait returns the exit code (-1 when killed by a signal).
func (p *hostProc) Wait() int {
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(3 * time.Second): // a shell that ignored SIGHUP / SIGTERM
			if p.cmd.Process != nil {
				_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			}
		}
	}()
	err := p.cmd.Wait()
	close(done)
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}
