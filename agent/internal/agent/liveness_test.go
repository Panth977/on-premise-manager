package agent

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryLimitFor(t *testing.T) {
	const mib = 1 << 20
	for _, c := range []struct{ mem, want int64 }{{512 * mib, 256 * mib}, {2048 * mib, 512 * mib}, {8192 * mib, 1024 * mib}} {
		if got := MemoryLimitFor(c.mem); got != c.want {
			t.Errorf("MemoryLimitFor(%d MiB) = %d MiB, want %d", c.mem/mib, got/mib, c.want/mib)
		}
	}
}

func TestStuckLoopsAndStoreHealth(t *testing.T) {
	a := &Agent{}
	a.beatStart("heartbeat", 30*time.Second)
	a.beatEnd("heartbeat", "")
	a.beatStart("outbox", 5*time.Second)
	if s := a.stuckLoops(time.Now()); len(s) != 0 {
		t.Fatal(s)
	}
	// outbox runs for 11 minutes → stuck; a non-critical loop running long never is
	a.beatStart("pending", time.Minute)
	if s := a.stuckLoops(time.Now().Add(11 * time.Minute)); len(s) != 1 || s[0] != "outbox" {
		t.Fatal(s)
	}
	a.beatEnd("outbox", "boom")
	st := a.loopStates()
	if len(st) != 3 {
		t.Fatal(st)
	}
	for _, m := range st {
		if m["name"] == "outbox" && m["panics"].(int64) != 1 {
			t.Fatal(m)
		}
	}
	// store failures are recorded (the log is needed for the first one)
	a.log = nil
	if a.storeHealth() != nil {
		t.Fatal("healthy store reported a failure")
	}
	a.storeErr.Store(&storeFailure{At: time.Now(), Error: "disk full", Count: 3})
	if f := a.storeHealth(); f == nil || f.Count != 3 {
		t.Fatal(f)
	}
}

func TestSdNotifyWatchdog(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sd")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "n.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Skip(err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", sock)
	t.Setenv("WATCHDOG_USEC", "")
	t.Setenv("WATCHDOG_PID", "")
	if iv := watchdogInterval(); iv != WatchdogTimeout/2 {
		t.Fatal(iv)
	}
	buf := make([]byte, 128)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "WATCHDOG_USEC=180000000") {
		t.Fatalf("%q %v", buf[:n], err)
	}
	// the unit's WatchdogSec wins
	t.Setenv("WATCHDOG_USEC", "60000000")
	if iv := watchdogInterval(); iv != 30*time.Second {
		t.Fatal(iv)
	}
	// not under systemd: no watchdog
	t.Setenv("WATCHDOG_USEC", "")
	t.Setenv("NOTIFY_SOCKET", "")
	if iv := watchdogInterval(); iv != 0 {
		t.Fatal(iv)
	}
	_ = errors.New
}
