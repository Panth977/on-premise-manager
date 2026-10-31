package cloud

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"opm/agent/internal/cloud/driver"
)

// LifecycleRules = backend/storage.lifecycle.json.
var LifecycleRules = []driver.LifecycleRule{{Prefixes: []string{"staging/"}, AgeDays: 1}}

var (
	regMu    sync.Mutex
	triggers = map[string]func(e driver.DocEvent) error{}
	jobs     []driver.ScheduledJob
	startup  []func() (stop func(), err error)
)

func lookupEnv(k string) (string, bool) { return os.LookupEnv(k) }

// RegisterTrigger attaches the handler of a TriggerSpecs name (app.ts TRIGGERS). Rows of triggers without a handler
// stay in the outbox until a binary with the handler runs.
func RegisterTrigger(name string, h func(e driver.DocEvent) error) {
	regMu.Lock()
	defer regMu.Unlock()
	triggers[name] = h
}

// RegisterJob adds a scheduled job (app.ts JOBS).
func RegisterJob(j driver.ScheduledJob) {
	regMu.Lock()
	defer regMu.Unlock()
	jobs = append(jobs, j)
}

// RegisterStartup adds work that runs after the listener setup (port tunnels: listen on every stored tunnel); its stop
// runs at shutdown.
func RegisterStartup(fn func() (stop func(), err error)) {
	regMu.Lock()
	defer regMu.Unlock()
	startup = append(startup, fn)
}

// InstallPorts opens the driver for a config and installs it.
func InstallPorts(c *CloudConfig) (*driver.Ports, error) {
	p, err := driver.Open(driver.Config{DataDir: c.DataDir, PublicURL: c.PublicURL, ConsoleURL: c.ConsoleURL})
	if err != nil {
		return nil, err
	}
	SetPorts(p)
	return p, nil
}

// Serve is serve() of server.ts: API + /auth + /_blob + jobs + outbox dispatcher + static frontend, until SIGTERM / SIGINT.
func Serve(cfg *CloudConfig) error {
	lp, err := InstallPorts(cfg)
	if err != nil {
		return err
	}
	if u := Unimplemented(); len(u) > 0 {
		driver.Log.Warn("opm-cloud: ops not ported", "count", len(u))
	}
	var stops []func()
	if cfg.RunJobs {
		regMu.Lock()
		tr := make([]driver.DocTrigger, 0, len(triggers))
		for _, s := range driver.TriggerSpecs {
			if h := triggers[s.Name]; h != nil {
				tr = append(tr, driver.DocTrigger{OutboxTrigger: s, Handler: h})
			}
		}
		all := append([]driver.ScheduledJob{}, jobs...)
		regMu.Unlock()
		d := driver.StartOutboxDispatcher(lp.Store, tr, 4, 0)
		stops = append(stops, d.Stop)
		all = append(all,
			driver.ScheduledJob{Name: "docTtl", Schedule: "every 10 minutes", TimeoutSeconds: 300, Run: func(context.Context) error {
				_, err := driver.SweepDocTTL(lp.Store, time.Now().UnixMilli())
				return err
			}},
			driver.ScheduledJob{Name: "blobLifecycle", Schedule: "every 60 minutes", TimeoutSeconds: 900, Run: func(context.Context) error {
				_, err := lp.Blobs.Sweep(LifecycleRules, time.Now().UnixMilli())
				return err
			}})
		s := StartScheduler(all, 0)
		stops = append(stops, s.Stop)
	}
	regMu.Lock()
	st := append([]func() (func(), error){}, startup...)
	regMu.Unlock()
	for _, fn := range st {
		stop, err := fn()
		if err != nil {
			return err
		}
		if stop != nil {
			stops = append(stops, stop)
		}
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	srv := NewServer(addr, NewHandler(cfg, lp))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	driver.Log.Info("opm-cloud listening", "version", Version(), "url", cfg.PublicURL, "port", cfg.Port, "dataDir", cfg.DataDir)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	done := make(chan struct{})
	go func() {
		s := <-sigs
		driver.Log.Info("opm-cloud stopping", "signal", s.String())
		time.AfterFunc(20*time.Second, func() { os.Exit(0) })
		cx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(cx)
		for _, stop := range stops {
			stop()
		}
		_ = lp.Close()
		close(done)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	<-done
	return nil
}
