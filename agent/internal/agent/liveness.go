package agent

// §21 #112 liveness: every periodic loop records when it started and finished a tick; the systemd watchdog
// (WatchdogSec in the unit, sd_notify WATCHDOG=1) is fed only while the critical loops make progress, so a hung agent
// (a deadlocked store, a call that never returns) is restarted by systemd instead of staying up doing nothing. A
// failed store write is recorded (storeFailure) and shown in status / self-metrics instead of being lost silently.
// The Go memory limit (GOMEMLIMIT, else min(25 % of RAM, 1 GiB), never below 256 MiB) keeps the heap from growing
// unchecked after a spike.

import (
	"bufio"
	"context"
	"net"
	"os"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

type loopBeat struct {
	period    time.Duration
	start     time.Time
	end       time.Time
	running   bool
	ticks     int64
	panics    int64
	lastPanic string
}

type storeFailure struct {
	At    time.Time
	Error string
	Count int64
}

// criticalLoops must keep ticking for the watchdog to be fed (short, store-bound work; long jobs are not in this set)
var criticalLoops = map[string]bool{"heartbeat": true, "outbox": true, "sampler": true, "watchdog": true, "tunnel": true}

func (a *Agent) beatStart(name string, period time.Duration) {
	a.beatMu.Lock()
	defer a.beatMu.Unlock()
	if a.beats == nil {
		a.beats = map[string]*loopBeat{}
	}
	b := a.beats[name]
	if b == nil {
		b = &loopBeat{}
		a.beats[name] = b
	}
	b.period, b.start, b.running = period, time.Now(), true
}

func (a *Agent) beatEnd(name string, panicked string) {
	a.beatMu.Lock()
	defer a.beatMu.Unlock()
	b := a.beats[name]
	if b == nil {
		return
	}
	b.end, b.running = time.Now(), false
	b.ticks++
	if panicked != "" {
		b.panics++
		b.lastPanic = panicked
	}
}

// stuckLoops: critical loops whose current tick runs longer than its budget (max(10 min, 3 periods)).
func (a *Agent) stuckLoops(now time.Time) []string {
	a.beatMu.Lock()
	defer a.beatMu.Unlock()
	var out []string
	for name, b := range a.beats {
		if !criticalLoops[name] || !b.running {
			continue
		}
		budget := 10 * time.Minute
		if 3*b.period > budget {
			budget = 3 * b.period
		}
		if now.Sub(b.start) > budget {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// loopStates for self-metrics.
func (a *Agent) loopStates() []map[string]any {
	a.beatMu.Lock()
	defer a.beatMu.Unlock()
	names := make([]string, 0, len(a.beats))
	for n := range a.beats {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		b := a.beats[n]
		m := map[string]any{"name": n, "periodSec": b.period.Seconds(), "ticks": b.ticks, "running": b.running, "panics": b.panics}
		if !b.end.IsZero() {
			m["lastEnd"] = tsOf(b.end)
			if !b.start.IsZero() && !b.running {
				m["lastMs"] = b.end.Sub(b.start).Milliseconds()
			}
		}
		if b.running {
			m["runningForMs"] = time.Since(b.start).Milliseconds()
		}
		if b.lastPanic != "" {
			m["lastPanic"] = b.lastPanic
		}
		out = append(out, m)
	}
	return out
}

// sdNotify sends one state line to systemd (no-op without NOTIFY_SOCKET).
func sdNotify(state string) bool {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return false
	}
	if strings.HasPrefix(addr, "@") {
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return false
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err == nil
}

// WatchdogTimeout is the systemd watchdog the agent asks for when the unit did not set one (WATCHDOG_USEC).
var WatchdogTimeout = 3 * time.Minute

// watchdogInterval: how often to feed (half the timeout). The unit's WatchdogSec wins; else, under systemd
// (NOTIFY_SOCKET, the unit has NotifyAccess=main), the agent turns the watchdog on itself with WATCHDOG_USEC= so an
// older binary (a roll back) never runs under a watchdog it cannot feed. 0 = not under systemd.
func watchdogInterval() time.Duration {
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0
	}
	if us, err := strconv.ParseInt(os.Getenv("WATCHDOG_USEC"), 10, 64); err == nil && us > 0 {
		return time.Duration(us) * time.Microsecond / 2
	}
	if os.Getenv("NOTIFY_SOCKET") == "" {
		return 0
	}
	if !sdNotify("WATCHDOG_USEC=" + strconv.FormatInt(WatchdogTimeout.Microseconds(), 10)) {
		return 0
	}
	return WatchdogTimeout / 2
}

// watchdogFeeder feeds systemd while no critical loop is stuck; once one is stuck it stops (systemd restarts the
// agent after the watchdog timeout; the containers keep running).
func (a *Agent) watchdogFeeder() {
	iv := watchdogInterval()
	if iv <= 0 {
		return
	}
	sdNotify("READY=1")
	sdNotify("WATCHDOG=1")
	a.goLoop("sd-watchdog", func(c context.Context) {
		t := time.NewTicker(iv)
		defer t.Stop()
		warned := false
		for {
			select {
			case <-c.Done():
				return
			case <-t.C:
			}
			if stuck := a.stuckLoops(time.Now()); len(stuck) > 0 {
				if !warned {
					warned = true
					a.log.Error("gui", "agent loops stuck: no longer feeding the systemd watchdog (systemd restarts the agent; containers keep running)", "loops", strings.Join(stuck, ","))
				}
				continue
			}
			warned = false
			sdNotify("WATCHDOG=1")
		}
	})
}

// noteStoreError records a failed store write (store.DB.OnError).
func (a *Agent) noteStoreError(err error) {
	cur := a.storeErr.Load()
	n := int64(1)
	if cur != nil {
		n = cur.Count + 1
	}
	a.storeErr.Store(&storeFailure{At: time.Now(), Error: err.Error(), Count: n})
	if n == 1 || n%100 == 0 {
		a.log.Error("store", "a write to opm.db failed (full disk or broken file?)", "error", err.Error(), "failures", n)
	}
}

// storeHealth: the last write failure of the past 10 minutes (nil = healthy).
func (a *Agent) storeHealth() *storeFailure {
	f := a.storeErr.Load()
	if f == nil || time.Since(f.At) > 10*time.Minute {
		return nil
	}
	return f
}

// hostMemTotal reads MemTotal from /proc/meminfo (0 when unknown).
func hostMemTotal() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			return kb * 1024
		}
	}
	return 0
}

// MemoryLimitFor is the default Go memory limit for a host with mem bytes of RAM: min(25 %, 1 GiB), ≥ 256 MiB.
func MemoryLimitFor(mem int64) int64 {
	const mib = 1 << 20
	l := mem / 4
	if l > 1024*mib {
		l = 1024 * mib
	}
	if l < 256*mib {
		l = 256 * mib
	}
	return l
}

// ApplyMemoryLimit sets the Go soft memory limit unless GOMEMLIMIT is set (the runtime already honours it).
// Returns the limit in bytes (-1 = left to GOMEMLIMIT / unknown host RAM).
func ApplyMemoryLimit() int64 {
	if os.Getenv("GOMEMLIMIT") != "" {
		return debug.SetMemoryLimit(-1)
	}
	mem := hostMemTotal()
	if mem <= 0 {
		return -1
	}
	l := MemoryLimitFor(mem)
	debug.SetMemoryLimit(l)
	return l
}
