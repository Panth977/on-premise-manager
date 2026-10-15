//go:build linux

package agent

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// portOwner names the process listening on a TCP port ("nginx (pid 812)") from /proc (needs root for other users'
// processes; §21 #86). nil when unknown.
func portOwner(port int) *string {
	inodes := map[string]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Scan() // header
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 10 || fs[3] != "0A" { // LISTEN
				continue
			}
			i := strings.LastIndex(fs[1], ":")
			if i < 0 {
				continue
			}
			p, err := strconv.ParseInt(fs[1][i+1:], 16, 32)
			if err == nil && int(p) == port && fs[9] != "0" {
				inodes["socket:["+fs[9]+"]"] = true
			}
		}
		fh.Close()
	}
	if len(inodes) == 0 {
		return nil
	}
	procs, _ := filepath.Glob("/proc/[0-9]*")
	for _, pd := range procs {
		fds, _ := filepath.Glob(pd + "/fd/*")
		for _, fd := range fds {
			if l, err := os.Readlink(fd); err == nil && inodes[l] {
				comm, _ := os.ReadFile(pd + "/comm")
				name := strings.TrimSpace(string(comm))
				if name == "" {
					name = "unknown"
				}
				s := fmt.Sprintf("%s (pid %s)", name, filepath.Base(pd))
				return &s
			}
		}
	}
	return nil
}
