package agent

// §21 #112 self-metrics: what the agent process itself uses (not Docker, not the containers) — GET
// /_opm/api/sites/{site}/agent/process, shown in the "Agent process" card of the Agent page.

import (
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

type procSample struct {
	at   time.Time
	cpuT float64 // seconds of CPU (user + system) of the process
}

type procCalls struct {
	at    time.Time
	calls int64
}

// readProcSelf: RSS bytes, CPU seconds, threads, open fds of this process (Linux /proc; ok=false elsewhere).
func readProcSelf() (rss int64, cpuSec float64, threads, fds int, ok bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0, 0, 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, 0, 0, false
	}
	f := strings.Fields(s[i+1:]) // f[0] = state (field 3)
	if len(f) < 22 {
		return 0, 0, 0, 0, false
	}
	const hz = 100.0 // USER_HZ (100 on every Linux the agent runs on)
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	threads, _ = strconv.Atoi(f[17])
	pages, _ := strconv.ParseInt(f[21], 10, 64)
	rss = pages * int64(os.Getpagesize())
	if ents, err := os.ReadDir("/proc/self/fd"); err == nil {
		fds = len(ents)
	}
	return rss, (ut + st) / hz, threads, fds, true
}

func (a *Agent) processStats() map[string]any {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	now := time.Now()
	out := map[string]any{
		"siteSlug": a.site, "version": a.version, "pid": os.Getpid(), "uptimeSec": int64(now.Sub(a.started).Seconds()),
		"rssBytes": nil, "cpuPct": nil, "openFds": nil, "threads": nil,
		"heapInUseBytes": ms.HeapInuse, "heapSysBytes": ms.HeapSys, "memLimitBytes": debug.SetMemoryLimit(-1),
		"goroutines": runtime.NumGoroutine(), "gcCount": ms.NumGC, "gcPauseLastMs": float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6,
	}
	if rss, cpu, th, fds, ok := readProcSelf(); ok {
		out["rssBytes"], out["threads"], out["openFds"] = rss, th, fds
		a.procMu.Lock()
		if prev := a.procPrev; !prev.at.IsZero() && now.Sub(prev.at) > time.Second {
			out["cpuPct"] = 100 * (cpu - prev.cpuT) / now.Sub(prev.at).Seconds()
		}
		a.procPrev = procSample{at: now, cpuT: cpu}
		a.procMu.Unlock()
	}
	calls := int64(0)
	cache := map[string]any{"live": false, "hits": int64(0)}
	if a.dc != nil {
		calls = a.dc.Calls()
		cs := a.dc.CacheStats()
		cache["live"], cache["hits"] = cs.Live, cs.Hits
	}
	out["dockerCalls"], out["dockerCallsPerMin"] = calls, nil
	a.procMu.Lock()
	if prev := a.dockerPrev; !prev.at.IsZero() && now.Sub(prev.at) > 5*time.Second {
		out["dockerCallsPerMin"] = float64(calls-prev.calls) / now.Sub(prev.at).Minutes()
	}
	a.dockerPrev = procCalls{at: now, calls: calls}
	a.procMu.Unlock()
	out["dockerCacheLive"], out["dockerCacheHits"] = cache["live"], cache["hits"]
	st := map[string]any{"ok": true, "lastError": nil, "failures": int64(0)}
	if f := a.storeHealth(); f != nil {
		st = map[string]any{"ok": false, "lastError": f.Error, "failures": f.Count}
	}
	out["store"] = st
	out["disk"] = a.ownDiskUse()
	out["warnings"] = a.selfWarnings()
	out["loops"] = a.loopStates()
	return out
}

// selfWarnings: the agent's own problems (disk floor / budget, store writes, cloud key, dead letters, stuck loops).
func (a *Agent) selfWarnings() []string {
	w := append([]string{}, a.diskWarningTexts()...)
	if f := a.storeHealth(); f != nil {
		w = append(w, "writes to opm.db fail ("+f.Error+"): changes may not be recorded — check the disk")
	}
	if t := a.cloudKeyRejected.Load(); t > 0 && time.Since(time.Unix(t, 0)) < 15*time.Minute {
		w = append(w, "the cloud rejects this site's key (HTTP 401): reports wait until the site is enrolled again; local operation goes on")
	}
	if n := a.deadLetters(); n > 0 {
		w = append(w, strconv.Itoa(n)+" cloud report(s) kept failing and were parked (outbox dead letters)")
	}
	if s := a.stuckLoops(time.Now()); len(s) > 0 {
		w = append(w, "agent loops stuck: "+strings.Join(s, ", ")+" (systemd restarts the agent)")
	}
	return w
}

func (a *Agent) handleAgentProcess(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	return a.processStats(), 200, nil
}
