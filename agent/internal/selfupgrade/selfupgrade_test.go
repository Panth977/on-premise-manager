package selfupgrade

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeCtl struct{ stops, starts int }

func (f *fakeCtl) Stop(context.Context) error  { f.stops++; return nil }
func (f *fakeCtl) Start(context.Context) error { f.starts++; return nil }

// setup: a data dir + a "binary" that is the new release, .prev = the old one, and pending.json.
func setup(t *testing.T, deadline int64) (Paths, string, *Pending) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "opm-agent")
	_ = os.MkdirAll(filepath.Dir(bin), 0o755)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho opm-agent 0.10.0-1 test/test\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(NewPath(bin), []byte("#!/bin/sh\necho opm-agent 0.10.0-2 test/test\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	from, _ := Sha256File(bin)
	if err := Swap(bin); err != nil {
		t.Fatal(err)
	}
	to, _ := Sha256File(bin)
	paths := For(filepath.Join(dir, "data"))
	p := &Pending{UpgradeID: "01UPG", From: "0.10.0-1", To: "0.10.0-2", FromSha256: from, ToSha256: to, Binary: bin, Prev: PrevPath(bin),
		Deadline: deadline, RestartedAt: Ts(time.Now()), Supervisor: "exec", DataDir: filepath.Join(dir, "data")}
	if err := WriteJSON(paths.Pending(), p); err != nil {
		t.Fatal(err)
	}
	return paths, bin, p
}

func TestSwapRestoreSelfTest(t *testing.T) {
	_, bin, p := setup(t, time.Now().Add(time.Minute).Unix())
	if cur, _ := Sha256File(bin); cur != p.ToSha256 {
		t.Fatal("binary is not the new one")
	}
	if prev, _ := Sha256File(PrevPath(bin)); prev != p.FromSha256 {
		t.Fatal(".prev is not the old binary")
	}
	if _, err := os.Stat(NewPath(bin)); !os.IsNotExist(err) {
		t.Fatal(".new must be gone after the swap")
	}
	if err := SelfTest(context.Background(), bin, "0.10.0-2"); err != nil {
		t.Fatal(err)
	}
	if err := SelfTest(context.Background(), bin, "0.10.0-3"); err == nil || !strings.Contains(err.Error(), "expected opm-agent 0.10.0-3") {
		t.Fatalf("self-test mismatch: %v", err)
	}
	if err := Restore(bin); err != nil {
		t.Fatal(err)
	}
	if cur, _ := Sha256File(bin); cur != p.FromSha256 {
		t.Fatal("restore did not bring back the old binary")
	}
	if st, _ := os.Stat(bin); st.Mode().Perm()&0o111 == 0 {
		t.Fatal("restored binary must be executable")
	}
}

func TestWatchHealthy(t *testing.T) {
	paths, _, p := setup(t, time.Now().Add(time.Minute).Unix())
	go func() { time.Sleep(300 * time.Millisecond); _ = paths.MarkHealthy(p.UpgradeID) }()
	ctl := &fakeCtl{}
	res, err := Watch(context.Background(), WatchOptions{DataDir: p.DataDir, Poll: 50 * time.Millisecond, Ctl: ctl})
	if err != nil || res != WatchHealthy || ctl.stops != 0 {
		t.Fatalf("%s %v %+v", res, err, ctl)
	}
	if _, err := os.Stat(paths.WatchPID()); !os.IsNotExist(err) {
		t.Fatal("watch.pid must be removed")
	}
}

func TestWatchRollsBackOnce(t *testing.T) {
	paths, bin, p := setup(t, time.Now().Add(400*time.Millisecond).Unix())
	ctl := &fakeCtl{}
	res, err := Watch(context.Background(), WatchOptions{DataDir: p.DataDir, Poll: 50 * time.Millisecond, Ctl: ctl})
	if err != nil || res != WatchRolledBack || ctl.stops != 1 || ctl.starts != 1 {
		t.Fatalf("%s %v %+v", res, err, ctl)
	}
	if cur, _ := Sha256File(bin); cur != p.FromSha256 {
		t.Fatal("not restored")
	}
	var rb RolledBack
	if ok, _ := ReadJSON(paths.RolledBack(), &rb); !ok || rb.By != "watcher" || rb.To != "0.10.0-2" || rb.Restored != "0.10.0-1" {
		t.Fatalf("rolled-back.json %+v", rb)
	}
	if paths.ReadPending() != nil {
		t.Fatal("pending.json must be removed")
	}
	// a second rollback (e.g. the guard after a reboot) finds nothing to do
	res, err = Rollback(context.Background(), paths, bin, p.UpgradeID, "guard", "x", ctl, nil)
	if res != WatchResolved || err != nil || ctl.stops != 1 {
		t.Fatalf("second rollback: %s %v %+v", res, err, ctl)
	}
}

func TestWatchNoPrev(t *testing.T) {
	paths, bin, p := setup(t, time.Now().Unix()-1)
	_ = os.Remove(PrevPath(bin))
	ctl := &fakeCtl{}
	res, err := Watch(context.Background(), WatchOptions{DataDir: p.DataDir, Poll: 10 * time.Millisecond, Ctl: ctl})
	if res != WatchNoPrev || err == nil || !strings.Contains(err.Error(), "does not exist") || ctl.stops != 0 {
		t.Fatalf("%s %v %+v", res, err, ctl)
	}
	if cur, _ := Sha256File(bin); cur != p.ToSha256 {
		t.Fatal("the binary must not change")
	}
	if paths.ReadPending() == nil {
		t.Fatal("pending stays (the agent shows the error)")
	}
}

func TestLockPreventsDoubleRollback(t *testing.T) {
	paths, bin, p := setup(t, time.Now().Unix()-1)
	unlock, ok := paths.TryLock()
	if !ok {
		t.Fatal("lock")
	}
	ctl := &fakeCtl{}
	if res, err := Rollback(context.Background(), paths, bin, p.UpgradeID, "watcher", "x", ctl, nil); err == nil || res != WatchResolved || ctl.stops != 0 {
		t.Fatalf("locked: %s %v", res, err)
	}
	unlock()
	if res, err := Rollback(context.Background(), paths, bin, p.UpgradeID, "watcher", "x", ctl, nil); err != nil || res != WatchRolledBack {
		t.Fatalf("after unlock: %s %v", res, err)
	}
}

// The real boot guard script against a temp dir with fake binaries.
func runGuard(t *testing.T, script string, p *Pending) string {
	t.Helper()
	out, err := exec.Command("/bin/sh", script, p.DataDir, p.Binary).CombinedOutput()
	if err != nil {
		t.Fatalf("guard exit: %v %s", err, out)
	}
	return string(out)
}

func TestBootGuardScript(t *testing.T) {
	script := filepath.Join(t.TempDir(), "upgrade-guard.sh")
	if err := os.WriteFile(script, GuardScript, 0o755); err != nil {
		t.Fatal(err)
	}
	if sc, err := exec.LookPath("shellcheck"); err == nil {
		if out, err := exec.Command(sc, "-s", "sh", script).CombinedOutput(); err != nil {
			t.Fatalf("shellcheck: %s", out)
		}
	} else {
		t.Log("shellcheck not installed: skipped the lint")
	}

	t.Run("deadline ahead: nothing restored", func(t *testing.T) {
		paths, bin, p := setup(t, time.Now().Add(time.Hour).Unix())
		runGuard(t, script, p)
		if cur, _ := Sha256File(bin); cur != p.ToSha256 || paths.ReadPending() == nil {
			t.Fatal("must not roll back before the deadline")
		}
	})
	t.Run("crash loop past the deadline: restored", func(t *testing.T) {
		paths, bin, p := setup(t, time.Now().Unix()-5)
		out := runGuard(t, script, p)
		if cur, _ := Sha256File(bin); cur != p.FromSha256 {
			t.Fatalf("not restored: %s", out)
		}
		var rb RolledBack
		if ok, err := ReadJSON(paths.RolledBack(), &rb); !ok || err != nil || rb.By != "guard" || rb.UpgradeID != p.UpgradeID || rb.From != "0.10.0-1" || rb.To != "0.10.0-2" {
			t.Fatalf("rolled-back.json %+v %v (%s)", rb, err, out)
		}
		if paths.ReadPending() != nil {
			t.Fatal("pending.json must be removed")
		}
		if _, err := os.Stat(paths.Lock()); !os.IsNotExist(err) {
			t.Fatal("lock must be released")
		}
		// idempotent: a second start does nothing
		before, _ := os.ReadFile(paths.RolledBack())
		runGuard(t, script, p)
		after, _ := os.ReadFile(paths.RolledBack())
		if string(before) != string(after) {
			t.Fatal("second guard run changed rolled-back.json")
		}
	})
	t.Run("healthy: nothing restored", func(t *testing.T) {
		paths, bin, p := setup(t, time.Now().Unix()-5)
		_ = paths.MarkHealthy(p.UpgradeID)
		runGuard(t, script, p)
		if cur, _ := Sha256File(bin); cur != p.ToSha256 {
			t.Fatal("healthy upgrade must not be rolled back")
		}
	})
	t.Run("healthy marker of another upgrade does not count", func(t *testing.T) {
		_, bin, p := setup(t, time.Now().Unix()-5)
		_ = For(p.DataDir).MarkHealthy("01OTHER")
		runGuard(t, script, p)
		if cur, _ := Sha256File(bin); cur != p.FromSha256 {
			t.Fatal("must roll back")
		}
	})
	t.Run("already restored by the watcher: pending cleaned", func(t *testing.T) {
		paths, bin, p := setup(t, time.Now().Unix()-5)
		_ = Restore(bin)
		runGuard(t, script, p)
		if paths.ReadPending() != nil {
			t.Fatal("pending must be removed")
		}
		var rb RolledBack
		if ok, _ := ReadJSON(paths.RolledBack(), &rb); !ok || rb.By != "guard" {
			t.Fatal("rolled-back.json must be written when missing")
		}
	})
	t.Run("prev missing: no rollback, clear message", func(t *testing.T) {
		paths, bin, p := setup(t, time.Now().Unix()-5)
		_ = os.Remove(PrevPath(bin))
		out := runGuard(t, script, p)
		if !strings.Contains(out, "is missing: cannot roll back") || paths.ReadPending() == nil {
			t.Fatalf("output %q", out)
		}
	})
	t.Run("no deadline (manual restart)", func(t *testing.T) {
		paths, bin, p := setup(t, 0)
		runGuard(t, script, p)
		if cur, _ := Sha256File(bin); cur != p.ToSha256 || paths.ReadPending() == nil {
			t.Fatal("deadline 0 must be ignored")
		}
	})
	t.Run("no pending", func(t *testing.T) {
		dir := t.TempDir()
		runGuard(t, script, &Pending{DataDir: dir, Binary: filepath.Join(dir, "x")})
	})
}

func TestInstallGuardIdempotent(t *testing.T) {
	dir := t.TempDir()
	o := GuardOptions{DataDir: "/var/lib/opm", Binary: "/usr/local/bin/opm-agent", ScriptPath: filepath.Join(dir, "lib/upgrade-guard.sh"), DropInDir: filepath.Join(dir, "opm-agent.service.d"), Force: true}
	r, err := InstallGuard(o)
	if err != nil || !r.Installed || !r.Changed {
		t.Fatalf("%+v %v", r, err)
	}
	b, _ := os.ReadFile(r.DropInPath)
	if !strings.Contains(string(b), "ExecStartPre=-/bin/sh "+o.ScriptPath+" /var/lib/opm /usr/local/bin/opm-agent") {
		t.Fatal(string(b))
	}
	if st, _ := os.Stat(o.ScriptPath); st.Mode().Perm() != 0o755 {
		t.Fatal("script mode")
	}
	r, err = InstallGuard(o)
	if err != nil || r.Changed {
		t.Fatalf("second install must be a no-op: %+v %v", r, err)
	}
	if ok, _, _ := GuardInstalled(o.ScriptPath, o.DropInDir); !ok {
		t.Fatal("GuardInstalled")
	}
	if r, _ := InstallGuard(GuardOptions{ScriptPath: filepath.Join(dir, "x.sh"), DropInDir: filepath.Join(dir, "y")}); r.Skipped == "" && os.Geteuid() != 0 {
		t.Fatal("non-root install must be skipped")
	}
}
