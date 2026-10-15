//go:build !linux && !darwin

package metrics

func readHost(hs *HostSample, prev *cpuTimes) {}

func diskUsage(path string) (total, used float64, dev string, ok bool) { return 0, 0, "", false }
