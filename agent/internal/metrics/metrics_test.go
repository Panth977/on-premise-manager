package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"opm/agent/internal/dlog"
)

func newTestMonitor(t *testing.T, now *time.Time) *Monitor {
	dir := t.TempDir()
	m := New(filepath.Join(dir, "metrics.db"), dir, nil, nil, dlog.New(filepath.Join(dir, "logs"), "debug", nil))
	m.Clock = func() time.Time { return *now }
	t.Cleanup(m.Close)
	return m
}

// feed writes one raw sample per 15 s for subject between from and to with values from fn, rolling up as it goes.
func feed(t *testing.T, m *Monitor, now *time.Time, from, to time.Time, subject string, fn func(time.Time) row) {
	for ts := from; ts.Before(to); ts = ts.Add(15 * time.Second) {
		*now = ts
		r := fn(ts)
		r.subject = subject
		if err := m.store(ts, []row{r}); err != nil {
			t.Fatal(err)
		}
		m.rollup(ts)
	}
}

func TestRollupsSeriesAndRetention(t *testing.T) {
	base := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	now := base
	m := newTestMonitor(t, &now)
	// 3 hours of a container at a steady 10 % CPU, 100 MiB, 1 KiB/s rx
	feed(t, m, &now, base, base.Add(3*time.Hour), "container:api", func(time.Time) row {
		return row{cpu: f(10), cpuPeak: f(10), mem: f(100 << 20), memMax: f(1 << 30), rx: f(1024), rs: f(0), oom: f(0)}
	})
	s1, err := m.Series("app", "container:api", "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	if s1.StepSec != 15 || len(s1.T) < 230 || len(s1.T) > 250 {
		t.Fatalf("raw tier: step %d points %d (raw kept 1 h)", s1.StepSec, len(s1.T))
	}
	s24, _ := m.Series("app", "container:api", "24h", "cpuPct,memBytes")
	if s24.StepSec != 60 || len(s24.T) < 170 || len(s24.T) > 181 {
		t.Fatalf("minute tier: %d points", len(s24.T))
	}
	if v := s24.Series["cpuPct"][10]; v == nil || *v != 10 {
		t.Fatalf("rollup avg cpu %v", v)
	}
	s7, _ := m.Series("app", "container:api", "7d", "")
	if s7.StepSec != 900 || len(s7.T) < 10 || len(s7.T) > 12 {
		t.Fatalf("quarter tier: %d points", len(s7.T))
	}
	if _, err := m.Series("app", "container:api", "2w", ""); err == nil {
		t.Fatal("bad range")
	}
	if _, err := m.Series("app", "container:api", "1h", "nope"); err == nil {
		t.Fatal("bad metric")
	}
	// jump 8 days: everything beyond retention is pruned
	now = base.Add(8 * 24 * time.Hour)
	m.rollup(now)
	var n int
	_ = m.db.QueryRow(`SELECT count(*) FROM samples`).Scan(&n)
	if n != 0 {
		t.Fatalf("retention: %d rows left", n)
	}
}

func TestAlertsAmberRedAndDisk(t *testing.T) {
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	now := base
	m := newTestMonitor(t, &now)
	// hot: CPU 95 % for 10 min → red; warm: CPU 90 % only for the last 30 s → amber; mem 95 % for 6 min → red
	feed(t, m, &now, base, base.Add(10*time.Minute), "container:hot", func(ts time.Time) row {
		mem := 50.0
		if ts.After(base.Add(4 * time.Minute)) {
			mem = 95
		}
		return row{cpu: f(95), mem: f(mem), memMax: f(100)}
	})
	feed(t, m, &now, base, base.Add(10*time.Minute), "container:warm", func(ts time.Time) row {
		c := 20.0
		if ts.After(base.Add(10*time.Minute - 31*time.Second)) {
			c = 90
		}
		return row{cpu: f(c), mem: f(10), memMax: f(100)}
	})
	feed(t, m, &now, base.Add(10*time.Minute-15*time.Second), base.Add(10*time.Minute), "disk:/", func(time.Time) row {
		return row{mem: f(95), memMax: f(100)}
	})
	got := map[string]string{}
	for _, a := range m.alerts(now) {
		got[a.Subject+"|"+a.Metric] = a.Level
	}
	want := map[string]string{"container:hot|cpu": "red", "container:hot|mem": "red", "container:warm|cpu": "amber", "disk:/|disk": "red"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("alert %s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected alerts %v", got)
	}
}

func TestSummaryAndHostSample(t *testing.T) {
	now := time.Now()
	m := newTestMonitor(t, &now)
	m.SampleOnce(context.Background())
	now = now.Add(15 * time.Second)
	m.SampleOnce(context.Background())
	sum := m.Summary("app", nil)
	if sum.Host == nil || sum.Host.CPUCount == 0 || sum.Host.MemTotalBytes == nil || len(sum.Host.Disks) == 0 {
		t.Fatalf("host sample %+v", sum.Host)
	}
	if sum.IntervalSec != 15 || sum.Thresholds["cpuPct"] != 85 || sum.DBBytes == 0 {
		t.Fatalf("summary %+v", sum)
	}
	s, err := m.Series("app", "host", "1h", "")
	if err != nil || len(s.T) != 2 || len(s.Series["memUsedBytes"]) != 2 {
		t.Fatalf("host series %+v %v", s, err)
	}
}
