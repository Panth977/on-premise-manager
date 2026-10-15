// Package metrics samples the host and the OPM containers every 15 s (Docker stats one-shot + inspect; /proc on
// Linux) and keeps them in <agentHome>/metrics.db (SQLite, separate from opm.db): raw samples 1 h, 1-minute rollups
// 24 h, 15-minute rollups 7 days; the file is capped (64 MiB) and incrementally vacuumed (SPEC §21 #89 c).
package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
)

// Thresholds (GUI badges only).
const (
	CPUPct     = 85.0
	MemPct     = 90.0
	DiskPct    = 90.0
	SustainSec = 300
)

// Retention per tier.
const (
	tierRaw     = 0
	tierMinute  = 1
	tierQuarter = 2
	rawKeep     = time.Hour + 2*time.Minute
	minuteKeep  = 24*time.Hour + 15*time.Minute
	quarterKeep = 7 * 24 * time.Hour
	dbCapBytes  = 64 << 20
)

// Target is a container to sample.
type Target struct {
	Ref string
	ID  string
}

// ContainerSample is the latest sample of a container (wire = shared ContainerSample).
type ContainerSample struct {
	Ref           string   `json:"ref"`
	T             string   `json:"t"`
	CPUPct        *float64 `json:"cpuPct"`
	MemBytes      *float64 `json:"memBytes"`
	MemLimitBytes *float64 `json:"memLimitBytes"`
	NetRxBps      *float64 `json:"netRxBps"`
	NetTxBps      *float64 `json:"netTxBps"`
	BlkReadBps    *float64 `json:"blkReadBps"`
	BlkWriteBps   *float64 `json:"blkWriteBps"`
	RestartCount  int      `json:"restartCount"`
	OOMKilled     bool     `json:"oomKilled"`
}

// Disk is one mount of the host sample.
type Disk struct {
	Mount      string  `json:"mount"`
	Label      string  `json:"label"`
	UsedBytes  float64 `json:"usedBytes"`
	TotalBytes float64 `json:"totalBytes"`
}

// HostSample is the latest host sample (wire = shared HostSample).
type HostSample struct {
	T              string   `json:"t"`
	CPUPct         *float64 `json:"cpuPct"`
	CPUCount       int      `json:"cpuCount"`
	MemUsedBytes   *float64 `json:"memUsedBytes"`
	MemTotalBytes  *float64 `json:"memTotalBytes"`
	SwapUsedBytes  *float64 `json:"swapUsedBytes"`
	SwapTotalBytes *float64 `json:"swapTotalBytes"`
	Load1          *float64 `json:"load1"`
	Load5          *float64 `json:"load5"`
	Load15         *float64 `json:"load15"`
	UptimeSec      *float64 `json:"uptimeSec"`
	Disks          []Disk   `json:"disks"`
}

// Monitor is the sampler + store of one site.
type Monitor struct {
	path     string
	home     string
	dc       *docker.Client
	targets  func(ctx context.Context) []Target
	log      *dlog.Logger
	Interval time.Duration
	Clock    func() time.Time

	dbOnce sync.Once
	db     *sql.DB
	dbErr  error

	mu      sync.Mutex
	prev    map[string]*docker.StatsSample
	latest  map[string]*ContainerSample
	host    *HostSample
	cpuPrev cpuTimes
	rolled1 int64 // next minute to roll up
	rolled2 int64 // next quarter to roll up
	lastVac time.Time
}

// New creates a monitor (the DB opens on first use).
func New(path, home string, dc *docker.Client, targets func(ctx context.Context) []Target, log *dlog.Logger) *Monitor {
	return &Monitor{path: path, home: home, dc: dc, targets: targets, log: log, Interval: 15 * time.Second, Clock: time.Now,
		prev: map[string]*docker.StatsSample{}, latest: map[string]*ContainerSample{}}
}

const schema = `
CREATE TABLE IF NOT EXISTS samples (
  tier INTEGER NOT NULL, ts INTEGER NOT NULL, subject TEXT NOT NULL,
  cpu REAL, cpuPeak REAL, mem REAL, memMax REAL, rx REAL, tx REAL, rd REAL, wr REAL,
  swap REAL, swapMax REAL, load1 REAL, load5 REAL, load15 REAL, restarts REAL, oom REAL,
  PRIMARY KEY (tier, subject, ts)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS samples_tier_ts ON samples(tier, ts);
`

func (m *Monitor) open() (*sql.DB, error) {
	m.dbOnce.Do(func() {
		if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
			m.dbErr = err
			return
		}
		q := url.Values{}
		q.Add("_pragma", "busy_timeout(10000)")
		q.Add("_pragma", "auto_vacuum(INCREMENTAL)") // must precede the first table
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(NORMAL)")
		db, err := sql.Open("sqlite", "file:"+m.path+"?"+q.Encode())
		if err != nil {
			m.dbErr = err
			return
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			m.dbErr = fmt.Errorf("metrics schema: %w", err)
			return
		}
		_ = os.Chmod(m.path, 0o600)
		m.db = db
	})
	return m.db, m.dbErr
}

// Close closes the DB.
func (m *Monitor) Close() {
	if m.db != nil {
		_ = m.db.Close()
	}
}

// Run samples every Interval until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	defer m.log.Recover("metrics", "run")
	if _, err := m.open(); err != nil {
		m.log.Error("metrics", "metrics store unavailable", "error", err.Error())
		return
	}
	m.SampleOnce(ctx)
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.Close()
			return
		case <-t.C:
			m.SampleOnce(ctx)
		}
	}
}

type row struct {
	subject                                                                        string
	cpu, cpuPeak, mem, memMax, rx, tx, rd, wr, swap, swapMax, l1, l5, l15, rs, oom *float64
}

func f(v float64) *float64 { return &v }

// SampleOnce takes one sample of the host and every target, stores it and rolls up.
func (m *Monitor) SampleOnce(ctx context.Context) {
	now := m.Clock()
	stamp := now.UTC().Format("2006-01-02T15:04:05.000Z")
	rows := []row{}
	// host
	hs := m.sampleHost(now)
	hs.T = stamp
	rows = append(rows, row{subject: "host", cpu: hs.CPUPct, cpuPeak: hs.CPUPct, mem: hs.MemUsedBytes, memMax: hs.MemTotalBytes, swap: hs.SwapUsedBytes,
		swapMax: hs.SwapTotalBytes, l1: hs.Load1, l5: hs.Load5, l15: hs.Load15})
	for _, d := range hs.Disks {
		rows = append(rows, row{subject: "disk:" + d.Mount, mem: f(d.UsedBytes), memMax: f(d.TotalBytes)})
	}
	// containers (≤ 4 in parallel; one-shot stats + inspect)
	var targets []Target
	if m.targets != nil {
		targets = m.targets(ctx)
	}
	type res struct {
		t  Target
		st *docker.StatsSample
		cj *docker.ContainerJSON
	}
	results := make([]res, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			defer m.log.Recover("metrics", "sample "+t.ID)
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			r := res{t: t}
			if cj, err := m.dc.ContainerInspect(cctx, t.ID); err == nil {
				r.cj = cj
				if cj.State.Running {
					if st, err := m.dc.StatsOneShot(cctx, t.ID); err == nil {
						r.st = st
					}
				}
			}
			results[i] = r
		}(i, t)
	}
	wg.Wait()
	m.mu.Lock()
	seen := map[string]bool{}
	for _, r := range results {
		if r.cj == nil {
			continue
		}
		seen[r.t.Ref] = true
		cs := &ContainerSample{Ref: r.t.Ref, T: stamp, RestartCount: r.cj.RestartCount, OOMKilled: r.cj.State.OOMKilled}
		if r.st != nil {
			key := r.t.Ref + "|" + r.t.ID
			p := m.prev[key]
			cs.MemBytes, cs.MemLimitBytes = f(float64(r.st.MemUsage)), f(float64(r.st.MemLimit))
			if p != nil {
				dt := r.st.Read.Sub(p.Read).Seconds()
				if dt <= 0 {
					dt = m.Interval.Seconds()
				}
				if r.st.CPUTotal >= p.CPUTotal {
					cd := float64(r.st.CPUTotal - p.CPUTotal)
					var pct float64
					if r.st.System > p.System && r.st.CPUs > 0 {
						pct = cd / float64(r.st.System-p.System) * float64(r.st.CPUs) * 100
					} else {
						pct = cd / (dt * 1e9) * 100
					}
					cs.CPUPct = f(round2(pct))
				}
				cs.NetRxBps, cs.NetTxBps = rate(r.st.NetRx, p.NetRx, dt), rate(r.st.NetTx, p.NetTx, dt)
				cs.BlkReadBps, cs.BlkWriteBps = rate(r.st.BlkRead, p.BlkRead, dt), rate(r.st.BlkWrite, p.BlkWrite, dt)
			}
			m.prev[key] = r.st
		}
		m.latest[r.t.Ref] = cs
		oom := 0.0
		if cs.OOMKilled {
			oom = 1
		}
		rows = append(rows, row{subject: "container:" + r.t.Ref, cpu: cs.CPUPct, cpuPeak: cs.CPUPct, mem: cs.MemBytes, memMax: cs.MemLimitBytes,
			rx: cs.NetRxBps, tx: cs.NetTxBps, rd: cs.BlkReadBps, wr: cs.BlkWriteBps, rs: f(float64(cs.RestartCount)), oom: f(oom)})
	}
	for ref := range m.latest {
		if !seen[ref] {
			delete(m.latest, ref)
		}
	}
	for key := range m.prev {
		if !seen[strings.SplitN(key, "|", 2)[0]] {
			delete(m.prev, key)
		}
	}
	m.host = &hs
	m.mu.Unlock()
	if err := m.store(now, rows); err != nil {
		m.log.Warn("metrics", "metrics sample not stored", "error", err.Error())
	}
	m.rollup(now)
}

func rate(cur, prev uint64, dt float64) *float64 {
	if cur < prev || dt <= 0 {
		return nil
	}
	return f(round2(float64(cur-prev) / dt))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (m *Monitor) store(now time.Time, rows []row) error {
	db, err := m.open()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT OR REPLACE INTO samples(tier, ts, subject, cpu, cpuPeak, mem, memMax, rx, tx, rd, wr, swap, swapMax, load1, load5, load15, restarts, oom)
		VALUES(0,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer st.Close()
	t := now.Unix()
	for _, r := range rows {
		if _, err := st.Exec(t, r.subject, r.cpu, r.cpuPeak, r.mem, r.memMax, r.rx, r.tx, r.rd, r.wr, r.swap, r.swapMax, r.l1, r.l5, r.l15, r.rs, r.oom); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

const rollCols = `avg(cpu), max(coalesce(cpuPeak, cpu)), avg(mem), max(memMax), avg(rx), avg(tx), avg(rd), avg(wr), avg(swap), max(swapMax),
	avg(load1), avg(load5), avg(load15), max(restarts), max(oom)`

// rollup builds the 1-minute and 15-minute rows of completed periods, prunes old rows and keeps the file ≤ 64 MiB.
func (m *Monitor) rollup(now time.Time) {
	db, err := m.open()
	if err != nil {
		return
	}
	n := now.Unix()
	m.mu.Lock()
	if m.rolled1 == 0 {
		var mx sql.NullInt64
		_ = db.QueryRow(`SELECT max(ts) FROM samples WHERE tier = 1`).Scan(&mx)
		if mx.Valid {
			m.rolled1 = mx.Int64 + 60
		} else {
			m.rolled1 = (n - int64(rawKeep.Seconds())) / 60 * 60
		}
		_ = db.QueryRow(`SELECT max(ts) FROM samples WHERE tier = 2`).Scan(&mx)
		if mx.Valid {
			m.rolled2 = mx.Int64 + 900
		} else {
			m.rolled2 = (n - int64(minuteKeep.Seconds())) / 900 * 900
		}
	}
	from1, to1 := m.rolled1, n/60*60
	from2, to2 := m.rolled2, n/900*900
	m.mu.Unlock()
	if to1 > from1 {
		if _, err := db.Exec(`INSERT OR REPLACE INTO samples SELECT 1, (ts/60)*60, subject, `+rollCols+` FROM samples WHERE tier = 0 AND ts >= ? AND ts < ? GROUP BY ts/60, subject`, from1, to1); err == nil {
			m.mu.Lock()
			m.rolled1 = to1
			m.mu.Unlock()
		}
	}
	if to2 > from2 {
		if _, err := db.Exec(`INSERT OR REPLACE INTO samples SELECT 2, (ts/900)*900, subject, `+rollCols+` FROM samples WHERE tier = 1 AND ts >= ? AND ts < ? GROUP BY ts/900, subject`, from2, to2); err == nil {
			m.mu.Lock()
			m.rolled2 = to2
			m.mu.Unlock()
		}
	}
	_, _ = db.Exec(`DELETE FROM samples WHERE (tier = 0 AND ts < ?) OR (tier = 1 AND ts < ?) OR (tier = 2 AND ts < ?)`,
		n-int64(rawKeep.Seconds()), n-int64(minuteKeep.Seconds()), n-int64(quarterKeep.Seconds()))
	m.mu.Lock()
	vac := now.Sub(m.lastVac) > time.Hour
	if vac {
		m.lastVac = now
	}
	m.mu.Unlock()
	if vac {
		if sz := m.DBBytes(); sz > dbCapBytes { // too many subjects: drop the oldest half of the history
			var mn, mx sql.NullInt64
			_ = db.QueryRow(`SELECT min(ts), max(ts) FROM samples`).Scan(&mn, &mx)
			if mn.Valid && mx.Valid {
				_, _ = db.Exec(`DELETE FROM samples WHERE ts < ?`, mn.Int64+(mx.Int64-mn.Int64)/2)
			}
			m.log.Warn("metrics", "metrics store above its cap; oldest half removed", "bytes", sz)
		}
		_, _ = db.Exec(`PRAGMA incremental_vacuum`)
		_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
}

// DBBytes is the size of the metrics store on disk (db + wal).
func (m *Monitor) DBBytes() int64 {
	var n int64
	for _, p := range []string{m.path, m.path + "-wal"} {
		if st, err := os.Stat(p); err == nil {
			n += st.Size()
		}
	}
	return n
}

// Latest returns the latest sample of a container (nil before the first sample).
func (m *Monitor) Latest(ref string) *ContainerSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.latest[ref]; c != nil {
		cp := *c
		return &cp
	}
	return nil
}

// Host returns the latest host sample.
func (m *Monitor) Host() *HostSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.host == nil {
		return nil
	}
	cp := *m.host
	return &cp
}

// ───────────────────────── summary + series

// Alert is a threshold badge (wire = shared MetricAlert).
type Alert struct {
	Subject string  `json:"subject"`
	Metric  string  `json:"metric"`
	Level   string  `json:"level"`
	Value   float64 `json:"value"`
	Since   *string `json:"since"`
	Message string  `json:"message"`
}

// Spark is the 60-minute sparkline of a container.
type Spark struct {
	CPU []*float64 `json:"cpu"`
	Mem []*float64 `json:"mem"`
}

// SummaryContainer is a ContainerSample + spark.
type SummaryContainer struct {
	ContainerSample
	Spark Spark `json:"spark"`
}

// Summary is the shared MetricsSummary.
type Summary struct {
	SiteSlug    string             `json:"siteSlug"`
	IntervalSec int                `json:"intervalSec"`
	SampledAt   *string            `json:"sampledAt"`
	Host        *HostSample        `json:"host"`
	Containers  []SummaryContainer `json:"containers"`
	Alerts      []Alert            `json:"alerts"`
	Thresholds  map[string]float64 `json:"thresholds"`
	DBBytes     int64              `json:"dbBytes"`
}

// Summary builds the monitoring page payload.
func (m *Monitor) Summary(site string, order []string) Summary {
	now := m.Clock()
	out := Summary{SiteSlug: site, IntervalSec: int(m.Interval.Seconds()), Containers: []SummaryContainer{}, Alerts: []Alert{},
		Thresholds: map[string]float64{"cpuPct": CPUPct, "memPct": MemPct, "diskPct": DiskPct, "sustainSec": SustainSec}, DBBytes: m.DBBytes()}
	out.Host = m.Host()
	if out.Host != nil {
		t := out.Host.T
		out.SampledAt = &t
	}
	m.mu.Lock()
	refs := make([]string, 0, len(m.latest))
	for r := range m.latest {
		refs = append(refs, r)
	}
	latest := map[string]ContainerSample{}
	for r, c := range m.latest {
		latest[r] = *c
	}
	m.mu.Unlock()
	pos := map[string]int{}
	for i, r := range order {
		pos[r] = i + 1
	}
	sort.Slice(refs, func(i, j int) bool {
		pi, pj := pos[refs[i]], pos[refs[j]]
		if pi == 0 {
			pi = 1 << 30
		}
		if pj == 0 {
			pj = 1 << 30
		}
		if pi != pj {
			return pi < pj
		}
		return refs[i] < refs[j]
	})
	sparks := m.sparks(now)
	for _, r := range refs {
		sc := SummaryContainer{ContainerSample: latest[r], Spark: sparks[r]}
		if sc.Spark.CPU == nil {
			sc.Spark = Spark{CPU: make([]*float64, 60), Mem: make([]*float64, 60)}
		}
		out.Containers = append(out.Containers, sc)
	}
	out.Alerts = m.alerts(now)
	return out
}

func (m *Monitor) sparks(now time.Time) map[string]Spark {
	out := map[string]Spark{}
	db, err := m.open()
	if err != nil {
		return out
	}
	end := now.Unix() / 60 * 60
	start := end - 59*60
	// the current (incomplete) minute comes from the raw samples
	rows, err := db.Query(`SELECT subject, ts, cpu, mem FROM samples WHERE tier = 1 AND subject LIKE 'container:%' AND ts >= ?
		UNION ALL SELECT subject, (ts/60)*60, avg(cpu), avg(mem) FROM samples WHERE tier = 0 AND subject LIKE 'container:%' AND ts >= ? GROUP BY subject, ts/60`, start, end)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var subj string
		var t int64
		var cpu, mem sql.NullFloat64
		if rows.Scan(&subj, &t, &cpu, &mem) != nil {
			continue
		}
		ref := strings.TrimPrefix(subj, "container:")
		sp, ok := out[ref]
		if !ok {
			sp = Spark{CPU: make([]*float64, 60), Mem: make([]*float64, 60)}
		}
		i := int((t - start) / 60)
		if i >= 0 && i < 60 {
			if cpu.Valid {
				sp.CPU[i] = f(round2(cpu.Float64))
			}
			if mem.Valid {
				sp.Mem[i] = f(math.Round(mem.Float64))
			}
		}
		out[ref] = sp
	}
	return out
}

// alerts: red when the whole last 5 min are above the threshold (cpu / mem), amber when only the latest sample is;
// disk: red above 90 %.
func (m *Monitor) alerts(now time.Time) []Alert {
	out := []Alert{}
	db, err := m.open()
	if err != nil {
		return out
	}
	rows, err := db.Query(`SELECT subject, ts, cpu, mem, memMax FROM samples WHERE tier = 0 AND ts >= ? ORDER BY subject, ts`, now.Add(-time.Hour).Unix())
	if err != nil {
		return out
	}
	type pt struct {
		t        int64
		cpu, mem *float64
	}
	series := map[string][]pt{}
	var subjects []string
	for rows.Next() {
		var subj string
		var t int64
		var cpu, mem, mx sql.NullFloat64
		if rows.Scan(&subj, &t, &cpu, &mem, &mx) != nil {
			continue
		}
		p := pt{t: t}
		if cpu.Valid {
			p.cpu = f(cpu.Float64)
		}
		if mem.Valid && mx.Valid && mx.Float64 > 0 {
			p.mem = f(100 * mem.Float64 / mx.Float64)
		}
		if _, ok := series[subj]; !ok {
			subjects = append(subjects, subj)
		}
		series[subj] = append(series[subj], p)
	}
	rows.Close()
	sort.Strings(subjects)
	cut := now.Unix() - int64(m.Interval.Seconds())*3 // the latest sample must be recent
	for _, subj := range subjects {
		ps := series[subj]
		if len(ps) == 0 || ps[len(ps)-1].t < cut {
			continue
		}
		last := ps[len(ps)-1]
		check := func(metric string, get func(pt) *float64, thr float64, label string) {
			v := get(last)
			if v == nil || *v <= thr {
				return
			}
			// run of samples above the threshold ending at the latest one
			start := last.t
			for i := len(ps) - 1; i >= 0; i-- {
				x := get(ps[i])
				if x == nil || *x <= thr {
					break
				}
				start = ps[i].t
			}
			level := "amber"
			if strings.HasPrefix(subj, "disk:") || last.t-start >= SustainSec-int64(m.Interval.Seconds()) {
				level = "red"
			}
			since := time.Unix(start, 0).UTC().Format("2006-01-02T15:04:05.000Z")
			name := subj
			if strings.HasPrefix(subj, "container:") {
				name = strings.TrimPrefix(subj, "container:")
			}
			out = append(out, Alert{Subject: subj, Metric: metric, Level: level, Value: round2(*v), Since: &since,
				Message: fmt.Sprintf("%s %s %.0f %% (> %.0f %%) for %s", name, label, *v, thr, (time.Duration(last.t-start+int64(m.Interval.Seconds())) * time.Second).String())})
		}
		if strings.HasPrefix(subj, "disk:") {
			check("disk", func(p pt) *float64 { return p.mem }, DiskPct, "disk")
			continue
		}
		check("cpu", func(p pt) *float64 { return p.cpu }, CPUPct, "CPU")
		check("mem", func(p pt) *float64 { return p.mem }, MemPct, "memory")
	}
	return out
}

// metric name (shared MetricName) → column.
var metricCols = map[string]string{"cpuPct": "cpu", "cpuPeak": "cpuPeak", "memBytes": "mem", "memLimitBytes": "memMax", "netRxBps": "rx", "netTxBps": "tx",
	"blkReadBps": "rd", "blkWriteBps": "wr", "restartCount": "restarts", "swapUsedBytes": "swap", "swapTotalBytes": "swapMax", "load1": "load1",
	"load5": "load5", "load15": "load15", "diskUsedBytes": "mem", "diskTotalBytes": "memMax", "memUsedBytes": "mem", "memTotalBytes": "memMax"}

// Series is the shared MetricsSeriesResponse.
type Series struct {
	SiteSlug string                `json:"siteSlug"`
	Subject  string                `json:"subject"`
	Range    string                `json:"range"`
	StepSec  int                   `json:"stepSec"`
	T        []int64               `json:"t"`
	Series   map[string][]*float64 `json:"series"`
}

// ErrBadRange is returned for an unknown range / subject / metric.
type ErrBadRange struct{ Msg string }

func (e *ErrBadRange) Error() string { return e.Msg }

// Series reads one subject. rng 1h → raw (15 s), 24h → 1 min, 7d → 15 min. metrics "" = the defaults of the subject.
func (m *Monitor) Series(site, subject, rng, metrics string) (*Series, error) {
	tier, step, span := 0, int(m.Interval.Seconds()), time.Hour
	switch rng {
	case "", "1h":
		rng = "1h"
	case "24h":
		tier, step, span = 1, 60, 24*time.Hour
	case "7d":
		tier, step, span = 2, 900, 7*24*time.Hour
	default:
		return nil, &ErrBadRange{"range must be 1h, 24h or 7d"}
	}
	if subject != "host" && !strings.HasPrefix(subject, "container:") && !strings.HasPrefix(subject, "disk:") {
		return nil, &ErrBadRange{"subject must be host, container:<ref> or disk:<mount>"}
	}
	var names []string
	if metrics != "" {
		names = strings.Split(metrics, ",")
	} else {
		switch {
		case subject == "host":
			names = []string{"cpuPct", "memUsedBytes", "memTotalBytes", "swapUsedBytes", "swapTotalBytes", "load1", "load5", "load15"}
		case strings.HasPrefix(subject, "disk:"):
			names = []string{"diskUsedBytes", "diskTotalBytes"}
		default:
			names = []string{"cpuPct", "cpuPeak", "memBytes", "memLimitBytes", "netRxBps", "netTxBps", "blkReadBps", "blkWriteBps", "restartCount"}
		}
	}
	cols := []string{}
	for _, n := range names {
		c, ok := metricCols[n]
		if !ok {
			return nil, &ErrBadRange{"unknown metric " + n}
		}
		cols = append(cols, c)
	}
	db, err := m.open()
	if err != nil {
		return nil, err
	}
	from := m.Clock().Add(-span).Unix()
	rows, err := db.Query(`SELECT ts, `+strings.Join(cols, ", ")+` FROM samples WHERE tier = ? AND subject = ? AND ts >= ? ORDER BY ts`, tier, subject, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &Series{SiteSlug: site, Subject: subject, Range: rng, StepSec: step, T: []int64{}, Series: map[string][]*float64{}}
	for _, n := range names {
		out.Series[n] = []*float64{}
	}
	for rows.Next() {
		vals := make([]sql.NullFloat64, len(cols))
		ptrs := []any{new(int64)}
		for i := range vals {
			ptrs = append(ptrs, &vals[i])
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		out.T = append(out.T, *(ptrs[0].(*int64)))
		for i, n := range names {
			var p *float64
			if vals[i].Valid {
				p = f(round2(vals[i].Float64))
			}
			out.Series[n] = append(out.Series[n], p)
		}
	}
	return out, nil
}

// ───────────────────────── host

type cpuTimes struct{ idle, total uint64 }

func (m *Monitor) sampleHost(now time.Time) HostSample {
	hs := HostSample{CPUCount: runtime.NumCPU(), Disks: []Disk{}}
	readHost(&hs, &m.cpuPrev)
	seen := map[string]bool{}
	for _, mp := range []struct{ path, label string }{{"/", "/"}, {m.home, "agentHome"}} {
		if mp.path == "" {
			continue
		}
		total, used, dev, ok := diskUsage(mp.path)
		if !ok {
			continue
		}
		if seen[dev] { // agentHome on the root file system: one card
			for i := range hs.Disks {
				if hs.Disks[i].Mount == "/" {
					hs.Disks[i].Label = "/ (agentHome " + mp.path + ")"
				}
			}
			continue
		}
		seen[dev] = true
		label := mp.label
		if mp.label == "agentHome" {
			label = "agentHome " + mp.path
		}
		hs.Disks = append(hs.Disks, Disk{Mount: mp.path, Label: label, UsedBytes: used, TotalBytes: total})
	}
	return hs
}
