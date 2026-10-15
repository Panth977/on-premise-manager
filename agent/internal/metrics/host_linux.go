//go:build linux

package metrics

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func readHost(hs *HostSample, prev *cpuTimes) {
	if fh, err := os.Open("/proc/stat"); err == nil {
		sc := bufio.NewScanner(fh)
		if sc.Scan() {
			fs := strings.Fields(sc.Text())
			var total, idle uint64
			for i, x := range fs[1:] {
				v, _ := strconv.ParseUint(x, 10, 64)
				if i >= 8 { // guest / guest_nice are already in user / nice
					break
				}
				total += v
				if i == 3 || i == 4 {
					idle += v
				}
			}
			if prev.total > 0 && total > prev.total {
				hs.CPUPct = f(round2(100 * (1 - float64(idle-prev.idle)/float64(total-prev.total))))
			}
			prev.idle, prev.total = idle, total
		}
		fh.Close()
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		kv := map[string]float64{}
		for _, ln := range strings.Split(string(b), "\n") {
			fs := strings.Fields(ln)
			if len(fs) >= 2 {
				v, _ := strconv.ParseFloat(fs[1], 64)
				kv[strings.TrimSuffix(fs[0], ":")] = v * 1024
			}
		}
		if kv["MemTotal"] > 0 {
			hs.MemTotalBytes = f(kv["MemTotal"])
			hs.MemUsedBytes = f(kv["MemTotal"] - kv["MemAvailable"])
		}
		hs.SwapTotalBytes = f(kv["SwapTotal"])
		hs.SwapUsedBytes = f(kv["SwapTotal"] - kv["SwapFree"])
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fs := strings.Fields(string(b))
		if len(fs) >= 3 {
			for i, p := range []**float64{&hs.Load1, &hs.Load5, &hs.Load15} {
				if v, err := strconv.ParseFloat(fs[i], 64); err == nil {
					*p = f(v)
				}
			}
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if fs := strings.Fields(string(b)); len(fs) > 0 {
			if v, err := strconv.ParseFloat(fs[0], 64); err == nil {
				hs.UptimeSec = f(v)
			}
		}
	}
}

func diskUsage(path string) (total, used float64, dev string, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, "", false
	}
	bs := float64(st.Bsize)
	total = float64(st.Blocks) * bs
	used = (float64(st.Blocks) - float64(st.Bfree)) * bs
	return total, used, fmt.Sprint(st.Fsid), true
}
