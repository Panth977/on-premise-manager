//go:build darwin

package metrics

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// readHost on macOS (developer machines only): memory from vm_stat, swap / load / boot time from sysctl; CPU % is
// approximated from the 1-minute load (no cgo for host_processor_info).
func readHost(hs *HostSample, prev *cpuTimes) {
	if v, err := unix.SysctlUint64("hw.memsize"); err == nil {
		hs.MemTotalBytes = f(float64(v))
	}
	if out, err := exec.Command("vm_stat").Output(); err == nil {
		pages := map[string]float64{}
		page := 16384.0
		for _, ln := range strings.Split(string(out), "\n") {
			if strings.Contains(ln, "page size of") {
				fs := strings.Fields(ln)
				for i, x := range fs {
					if x == "of" && i+1 < len(fs) {
						page, _ = strconv.ParseFloat(fs[i+1], 64)
					}
				}
				continue
			}
			k, v, ok := strings.Cut(ln, ":")
			if ok {
				n, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "."), 64)
				pages[strings.TrimSpace(k)] = n
			}
		}
		used := pages["Pages active"] + pages["Pages wired down"] + pages["Pages occupied by compressor"]
		hs.MemUsedBytes = f(used * page)
	}
	if b, err := unix.SysctlRaw("vm.swapusage"); err == nil && len(b) >= 24 {
		total := *(*uint64)(unsafe.Pointer(&b[0]))
		used := *(*uint64)(unsafe.Pointer(&b[16]))
		hs.SwapTotalBytes, hs.SwapUsedBytes = f(float64(total)), f(float64(used))
	}
	if b, err := unix.SysctlRaw("vm.loadavg"); err == nil && len(b) >= 24 {
		scale := float64(*(*int64)(unsafe.Pointer(&b[16])))
		if scale > 0 {
			l := []float64{float64(*(*uint32)(unsafe.Pointer(&b[0]))) / scale, float64(*(*uint32)(unsafe.Pointer(&b[4]))) / scale, float64(*(*uint32)(unsafe.Pointer(&b[8]))) / scale}
			hs.Load1, hs.Load5, hs.Load15 = f(round2(l[0])), f(round2(l[1])), f(round2(l[2]))
			pct := 100 * l[0] / float64(hs.CPUCount)
			if pct > 100 {
				pct = 100
			}
			hs.CPUPct = f(round2(pct))
		}
	}
	if tv, err := unix.SysctlTimeval("kern.boottime"); err == nil {
		hs.UptimeSec = f(float64(time.Now().Unix() - tv.Sec))
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
