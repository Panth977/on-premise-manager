// Package sdsock consumes systemd socket activation (SPEC-final §21 #91 f): opm-agent.socket holds the default edge
// port, agentPort and guiAliasPort while the agent restarts (the kernel queues connections instead of refusing them);
// the agent takes the inherited fds (LISTEN_PID / LISTEN_FDS / LISTEN_FDNAMES, first fd 3) and serves on dups of them.
// Ports without an inherited socket are bound normally (the fallback when not socket-activated).
//
// systemd applies FileDescriptorName= to every socket of one .socket unit, so the agent matches inherited sockets by
// their LOCAL PORT (getsockname), not by name; the names are informational.
package sdsock

import (
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Socket is one inherited listening socket.
type Socket struct {
	Name string
	Port int
	File *os.File // kept open for the life of the process (every Listen returns a dup)
}

var (
	once      sync.Once
	inherited []Socket
)

const listenFdsStart = 3

// Parse reads the activation env (the caller passes env lookups + the first fd; tests use real fds).
func Parse(getenv func(string) string, pid int, first int) []Socket {
	if p, err := strconv.Atoi(getenv("LISTEN_PID")); err != nil || p != pid {
		return nil
	}
	n, err := strconv.Atoi(getenv("LISTEN_FDS"))
	if err != nil || n <= 0 {
		return nil
	}
	names := strings.Split(getenv("LISTEN_FDNAMES"), ":")
	var out []Socket
	for i := 0; i < n; i++ {
		fd := first + i
		syscall.CloseOnExec(fd)
		name := ""
		if i < len(names) {
			name = names[i]
		}
		f := os.NewFile(uintptr(fd), "systemd-socket-"+name)
		if f == nil {
			continue
		}
		port := 0
		if sa, err := syscall.Getsockname(fd); err == nil {
			switch a := sa.(type) {
			case *syscall.SockaddrInet4:
				port = a.Port
			case *syscall.SockaddrInet6:
				port = a.Port
			}
		}
		out = append(out, Socket{Name: name, Port: port, File: f})
	}
	return out
}

// Inherited returns the sockets passed by systemd to this process (parsed once; the env is cleared so children do
// not see it).
func Inherited() []Socket {
	once.Do(func() {
		inherited = Parse(os.Getenv, os.Getpid(), listenFdsStart)
		for _, k := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
			_ = os.Unsetenv(k)
		}
	})
	return inherited
}

// Activated reports whether this process got sockets from systemd.
func Activated() bool { return len(Inherited()) > 0 }

// SetForTest replaces the inherited sockets (tests).
func SetForTest(s []Socket) {
	once.Do(func() {})
	inherited = s
}

// Listen returns a TCP listener on port: a dup of the inherited socket for that port, else a fresh bind. inherited
// reports which one.
func Listen(port int) (net.Listener, bool, error) {
	for _, s := range Inherited() {
		if s.Port == port && port != 0 {
			ln, err := net.FileListener(s.File)
			if err == nil {
				return ln, true, nil
			}
		}
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	return ln, false, err
}
