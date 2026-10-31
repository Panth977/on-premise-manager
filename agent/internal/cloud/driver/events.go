package driver

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/cloud/doc"
)

// DocEvent is one document change delivered to a trigger.
type DocEvent struct {
	Path   string
	Params map[string]string
	Before *doc.Map
	After  *doc.Map
}

// DocTrigger is a document trigger with its handler.
type DocTrigger struct {
	OutboxTrigger
	Handler func(e DocEvent) error
}

// ScheduledJob is a named job ("every day HH:MM" | "every N minutes", UTC).
type ScheduledJob struct {
	Name           string
	Schedule       string
	TimeoutSeconds int
	Run            func(ctx context.Context) error
}

var placeholder = regexp.MustCompile(`^\{([A-Za-z0-9_]+)\}$`)

// MatchPattern: placeholders of a trigger pattern matched against a document path; nil = no match.
func MatchPattern(pattern, path string) map[string]string {
	p := strings.Split(pattern, "/")
	s := strings.Split(path, "/")
	if len(p) != len(s) {
		return nil
	}
	out := map[string]string{}
	for i := range p {
		if m := placeholder.FindStringSubmatch(p[i]); m != nil {
			out[m[1]] = s[i]
		} else if p[i] != s[i] {
			return nil
		}
	}
	return out
}

var (
	everyDay = regexp.MustCompile(`^every day (\d{2}):(\d{2})$`)
	everyN   = regexp.MustCompile(`^every (\d+) minutes?$`)
)

// NextRun is the next run (ms) of a schedule after `from`.
func NextRun(schedule string, from int64) (int64, error) {
	if m := everyDay.FindStringSubmatch(schedule); m != nil {
		d := time.UnixMilli(from).UTC()
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		t := time.Date(d.Year(), d.Month(), d.Day(), h, mi, 0, 0, time.UTC).UnixMilli()
		if t <= from {
			t += 86_400_000
		}
		return t, nil
	}
	if m := everyN.FindStringSubmatch(schedule); m != nil {
		n, _ := strconv.Atoi(m[1])
		step := int64(n) * 60_000
		return from/step*step + step, nil
	}
	return 0, fmt.Errorf(`unsupported schedule "%s"`, schedule)
}

const leaseMs = 15 * 60_000

type outRow struct {
	seq           int64
	trigger, path string
	before, after *string
}

func minAgeMs() int64 {
	n, _ := strconv.ParseFloat(os.Getenv("OPM_OUTBOX_MIN_AGE_MS"), 64)
	if n < 0 || n != n {
		return 0
	}
	return int64(n)
}

// Dispatcher is the running outbox dispatcher.
type Dispatcher struct {
	store       *Store
	concurrency int
	byName      map[string]DocTrigger
	skip        string // SQL list of triggers nobody handles yet: their rows wait

	mu        sync.Mutex
	dirty     map[string]bool
	order     []string
	seen      map[string]string
	elsewhere map[string]bool
	running   int
	stopped   bool
	pumping   bool
	done      chan struct{}
	wg        sync.WaitGroup
	unsub     func()
}

// StartOutboxDispatcher is startOutboxDispatcher (default concurrency 8, poll 1 s). Rows of unregistered triggers
// that the store itself writes (no handler ported yet) stay in the outbox; other unknown rows are dropped, as in TS.
func StartOutboxDispatcher(s *Store, triggers []DocTrigger, concurrency int, poll time.Duration) *Dispatcher {
	if concurrency < 1 {
		concurrency = 8
	}
	if poll <= 0 {
		poll = time.Second
	}
	d := &Dispatcher{store: s, concurrency: concurrency, byName: map[string]DocTrigger{}, dirty: map[string]bool{}, seen: map[string]string{}, elsewhere: map[string]bool{}, done: make(chan struct{})}
	for _, t := range triggers {
		d.byName[t.Name] = t
	}
	var skip []string
	for _, t := range s.Triggers {
		if _, ok := d.byName[t.Name]; !ok {
			skip = append(skip, "'"+strings.ReplaceAll(t.Name, "'", "''")+"'")
		}
	}
	if len(skip) > 0 {
		d.skip = " AND trigger NOT IN (" + strings.Join(skip, ",") + ")"
	}
	d.unsub = s.onOutbox(func(key string) {
		d.mark(key)
		go d.pump()
	})
	d.poll()
	go func() {
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			select {
			case <-d.done:
				return
			case <-t.C:
				d.poll()
			}
		}
	}()
	return d
}

func (d *Dispatcher) mark(key string) {
	d.mu.Lock()
	if !d.dirty[key] {
		d.dirty[key] = true
		d.order = append(d.order, key)
	}
	d.mu.Unlock()
}

func sig(file string) string {
	var b strings.Builder
	for _, f := range []string{file, file + "-wal"} {
		if st, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%d:%d;", st.ModTime().UnixNano(), st.Size())
		} else {
			b.WriteString("-;")
		}
	}
	return b.String()
}

func (d *Dispatcher) claim(key string, n int) ([]outRow, error) {
	h, err := d.store.Files.handle(key, false)
	if err != nil || h == nil {
		return nil, err
	}
	defer d.store.Files.release(h)
	now := time.Now().UnixMilli()
	rows, err := h.db.QueryContext(bg, "UPDATE _outbox SET leaseUntil = ? WHERE seq IN (SELECT seq FROM _outbox WHERE (leaseUntil IS NULL OR leaseUntil < ?) AND createdMs <= ?"+d.skip+" ORDER BY seq LIMIT ?) RETURNING seq, trigger, path, before, after", now+leaseMs, now, now-minAgeMs(), n)
	if err != nil {
		if isBusy(err) {
			d.mark(key)
			return nil, nil
		}
		return nil, err
	}
	var out []outRow
	for rows.Next() {
		var r outRow
		if err := rows.Scan(&r.seq, &r.trigger, &r.path, &r.before, &r.after); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		if isBusy(err) {
			d.mark(key)
			return out, nil
		}
		return nil, err
	}
	if len(out) < n {
		var left int
		if h.db.QueryRowContext(bg, "SELECT count(*) FROM _outbox WHERE 1=1"+d.skip).Scan(&left) == nil {
			d.mu.Lock()
			if left > len(out) {
				d.elsewhere[key] = true
			} else {
				delete(d.elsewhere, key)
			}
			d.mu.Unlock()
		}
	}
	return out, nil
}

func (d *Dispatcher) finish(key string, seq int64) {
	h, err := d.store.Files.handle(key, false)
	if err == nil && h != nil {
		defer d.store.Files.release(h)
		_, err = h.db.ExecContext(bg, "DELETE FROM _outbox WHERE seq = ?", seq)
	}
	if err != nil {
		Log.Warn("outbox: delete failed (the row re-runs when its lease expires)", "file", key, "seq", seq, "err", err.Error())
	}
}

func (d *Dispatcher) run(key string, r outRow) {
	t, ok := d.byName[r.trigger]
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			d.finish(key, r.seq)
			d.mu.Lock()
			d.running--
			d.mu.Unlock()
			d.mark(key)
			d.pump()
		}()
		if !ok {
			Log.Warn("outbox: no handler for trigger (row dropped)", "trigger", r.trigger, "path", r.path)
			return
		}
		e := DocEvent{Path: r.path, Params: MatchPattern(t.Pattern, r.path)}
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("panic: %v", p)
				}
			}()
			if r.before != nil {
				e.Before = mustDecode(*r.before)
			}
			if r.after != nil {
				e.After = mustDecode(*r.after)
			}
			err = t.Handler(e)
		}()
		if err != nil {
			Log.Error("trigger "+t.Name+" failed", "path", r.path, "err", err.Error())
		}
	}()
}

func (d *Dispatcher) pump() {
	d.mu.Lock()
	if d.pumping || d.stopped {
		d.mu.Unlock()
		return
	}
	d.pumping = true
	defer func() {
		d.mu.Lock()
		d.pumping = false
		d.mu.Unlock()
	}()
	for len(d.order) > 0 && d.running < d.concurrency {
		key := d.order[0]
		d.order = d.order[1:]
		delete(d.dirty, key)
		room := d.concurrency - d.running
		d.running += room // reserved while claiming
		d.mu.Unlock()
		rows, err := d.claim(key, room)
		if err != nil {
			Log.Error("outbox dispatcher error", "err", err.Error())
		}
		d.mu.Lock()
		d.running -= room - len(rows)
		for _, r := range rows {
			d.run(key, r)
		}
		if len(rows) > 0 && d.running >= d.concurrency && !d.dirty[key] {
			d.dirty[key] = true
			d.order = append(d.order, key)
		}
	}
	d.mu.Unlock()
}

func (d *Dispatcher) poll() {
	keys, err := d.store.Files.AllKeys()
	if err != nil {
		Log.Error("outbox poll error", "err", err.Error())
		return
	}
	for _, k := range keys {
		f, err := d.store.Files.fileOf(k)
		if err != nil {
			continue
		}
		s := sig(f)
		d.mu.Lock()
		ch := d.seen[k] != s || d.elsewhere[k]
		d.seen[k] = s
		d.mu.Unlock()
		if ch {
			d.mark(k)
		}
	}
	d.pump()
}

func (d *Dispatcher) pendingRows() bool {
	keys, err := d.store.Files.AllKeys()
	if err != nil {
		return false
	}
	for _, k := range keys {
		h, err := d.store.Files.handle(k, false)
		if err != nil || h == nil {
			continue
		}
		var x int
		e := h.db.QueryRowContext(bg, "SELECT 1 FROM _outbox WHERE 1=1"+d.skip+" LIMIT 1").Scan(&x)
		d.store.Files.release(h)
		if e == nil {
			return true
		}
	}
	return false
}

func (d *Dispatcher) busy() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running > 0
}

// Stop stops dispatching and waits for the running handlers.
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	d.mu.Unlock()
	close(d.done)
	d.unsub()
	d.wg.Wait()
}

// Idle returns when nothing is pending or running.
func (d *Dispatcher) Idle() {
	for {
		for d.busy() {
			time.Sleep(time.Millisecond)
		}
		if !d.pendingRows() {
			if !d.busy() && !d.pendingRows() {
				return
			}
			continue
		}
		keys, _ := d.store.Files.AllKeys()
		for _, k := range keys {
			d.mark(k)
		}
		d.pump()
		time.Sleep(5 * time.Millisecond)
	}
}

// SweepDocTTL deletes agentNonces.expiresAt / rateLimits.expireAt older than now. Returns the count.
func SweepDocTTL(s *Store, now int64) (int, error) {
	n := 0
	for _, x := range [][2]string{{"agentNonces", "expiresAt"}, {"rateLimits", "expireAt"}} {
		for {
			snap, err := s.Collection(x[0]).Where(x[1], "<", doc.TimestampFromMillis(now)).Select().Limit(500).Get()
			if err != nil {
				return n, err
			}
			if snap.Empty() {
				break
			}
			b := s.Batch()
			for _, d := range snap.Docs {
				b.Delete(d.Ref())
			}
			if err := b.Commit(); err != nil {
				return n, err
			}
			n += snap.Size()
			if snap.Size() < 500 {
				break
			}
		}
	}
	return n, nil
}
