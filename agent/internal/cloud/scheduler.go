package cloud

import (
	"context"
	"sync"
	"time"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

func schedDoc(name string) *driver.DocRef { return DB().Collection("opmScheduler").Doc(name) }

func claimJob(j driver.ScheduledJob, now int64, owner string) (bool, error) {
	return driver.RunTx(DB(), func(tx *driver.Tx) (bool, error) {
		s, err := tx.Get(schedDoc(j.Name))
		if err != nil {
			return false, err
		}
		d := s.Data()
		if d == nil {
			// first start: schedule from now (a fresh install does not run the daily job immediately)
			tx.Set(schedDoc(j.Name), doc.Obj("name", j.Name, "lastRunMs", now, "lastOk", nil, "lastError", nil, "leaseUntil", 0, "owner", nil))
			return false, nil
		}
		if d.Int("leaseUntil") > now {
			return false, nil
		}
		next, err := driver.NextRun(j.Schedule, d.Int("lastRunMs"))
		if err != nil {
			return false, err
		}
		if next > now {
			return false, nil
		}
		tx.Set(schedDoc(j.Name), d.Shallow().Set("leaseUntil", now+int64(j.TimeoutSeconds)*1000+60_000).Set("owner", owner))
		return true, nil
	})
}

// Scheduler is the running scheduler.
type Scheduler struct {
	stop    chan struct{}
	stopped bool
	mu      sync.Mutex
	running map[string]bool
	cancel  context.CancelFunc
}

// StartScheduler is startScheduler: each job's last run is a document (opmScheduler/{name}), claimed with a lease in a
// transaction, so a restart neither skips a missed run nor doubles one.
func StartScheduler(jobs []driver.ScheduledJob, tick time.Duration) *Scheduler {
	if tick <= 0 {
		tick = 15 * time.Second
	}
	cx, cancel := context.WithCancel(context.Background())
	s := &Scheduler{stop: make(chan struct{}), running: map[string]bool{}, cancel: cancel}
	owner := RandomUUID()
	run := func() {
		for _, j := range jobs {
			s.mu.Lock()
			skip := s.stopped || s.running[j.Name]
			s.mu.Unlock()
			if skip {
				continue
			}
			now := time.Now().UnixMilli()
			mine, err := claimJob(j, now, owner)
			if err != nil {
				driver.Log.Warn("scheduler claim failed", "job", j.Name, "error", err.Error())
			}
			if !mine {
				continue
			}
			s.mu.Lock()
			s.running[j.Name] = true
			s.mu.Unlock()
			go func() {
				t0 := time.Now()
				var lastErr any
				func() {
					defer func() {
						if p := recover(); p != nil {
							lastErr = "panic"
							driver.Log.Error("scheduled job failed", "job", j.Name, "error", p)
						}
					}()
					if err := j.Run(cx); err != nil {
						lastErr = err.Error()
						driver.Log.Error("scheduled job failed", "job", j.Name, "error", err.Error())
					}
				}()
				if err := schedDoc(j.Name).Set(doc.Obj("name", j.Name, "lastRunMs", now, "lastOk", lastErr == nil, "lastError", lastErr, "lastDurationMs", time.Since(t0).Milliseconds(), "leaseUntil", 0, "owner", nil)); err != nil {
					driver.Log.Warn("scheduler record failed", "job", j.Name, "error", err.Error())
				}
				s.mu.Lock()
				delete(s.running, j.Name)
				s.mu.Unlock()
			}()
		}
	}
	go func() {
		run()
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				run()
			}
		}
	}()
	return s
}

// Stop stops ticking and waits up to 30 s for running jobs.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()
	close(s.stop)
	for i := 0; i < 300; i++ {
		s.mu.Lock()
		n := len(s.running)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.cancel()
}
