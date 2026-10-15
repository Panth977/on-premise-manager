//go:build darwin

package tools

import (
	"bytes"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo terminal pair (master, slave) without cgo (posix_openpt / grantpt / unlockpt / ptsname).
func openPTY() (*os.File, *os.File, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	if err := ioctlNoArg(fd, unix.TIOCPTYGRANT); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("grantpt: %w", err)
	}
	if err := ioctlNoArg(fd, unix.TIOCPTYUNLK); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}
	buf := make([]byte, 128)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("ptsname: %w", e)
	}
	name := string(buf[:bytes.IndexByte(buf, 0)])
	sfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open %s: %w", name, err)
	}
	return master, os.NewFile(uintptr(sfd), name), nil
}

func ioctlNoArg(fd int, req uint) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), 0); e != 0 {
		return e
	}
	return nil
}
