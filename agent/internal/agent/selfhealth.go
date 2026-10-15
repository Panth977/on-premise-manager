package agent

// GET /_opm/api/health (SPEC-final §21 #91, AgentHealthResponse; public; 503 with the same body while not ok): ok = the
// store answers + the agent port listener is bound + the startup finished. Used by the install script, the upgrade
// engine's health phase (selfboot.go) and the upgrade watcher.

import (
	"context"
	"net/http"
	"os"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sdsock"
	"opm/agent/internal/store"
)

func (a *Agent) health(ctx context.Context) model.AgentHealthResponse {
	h := model.AgentHealthResponse{Version: a.version, Schema: store.SchemaVersion, PID: os.Getpid(), StartedAt: tsOf(a.started), SocketActivated: sdsock.Activated(), Checks: []model.AgentHealthCheck{}}
	check := func(name string, ok bool, msg string) bool {
		c := model.AgentHealthCheck{Name: name, Ok: ok}
		if msg != "" {
			c.Message = &msg
		}
		h.Checks = append(h.Checks, c)
		return ok
	}
	ok := true
	var one int
	if err := a.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		ok = check("store", false, err.Error()) && ok
	} else {
		check("store", true, "")
	}
	a.mu.Lock()
	l := a.listeners["agent"]
	a.mu.Unlock()
	if l == nil {
		ok = check("listeners", false, "the agent port is not bound") && ok
	} else {
		check("listeners", true, "")
	}
	if !a.self.startupDone.Load() {
		ok = check("startup", false, "the agent is starting") && ok
	} else {
		check("startup", true, "")
	}
	h.Ok = ok
	// informational (do not change ok): cloud + docker
	a.mu.Lock()
	last := a.lastCloudOk
	a.mu.Unlock()
	h.Cloud = model.AgentHealthCloud{Route: a.route(), Connected: !last.IsZero() && !last.Before(a.started)}
	if !last.IsZero() {
		t := tsOf(last)
		h.Cloud.LastContactAt = &t
	}
	h.Docker.Ok = a.dockerOK(ctx)
	if h.Docker.Ok {
		check("docker", true, "")
	} else {
		check("docker", false, "the Docker daemon does not answer")
	}
	if h.Cloud.Connected {
		check("cloud", true, "")
	} else {
		check("cloud", false, "no cloud contact since the start")
	}
	return h
}

// dockerOK pings Docker (cached 5 s).
func (a *Agent) dockerOK(ctx context.Context) bool {
	s := a.self
	s.mu.Lock()
	if time.Since(s.dockerAt) < 5*time.Second {
		v := s.dockerOk
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ok := a.dc.Ping(cctx) == nil
	s.mu.Lock()
	s.dockerOk, s.dockerAt = ok, time.Now()
	s.mu.Unlock()
	return ok
}

func (a *Agent) handleHealth(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	h := a.health(r.Context())
	w.Header().Set("Cache-Control", "no-store")
	if !h.Ok {
		return h, 503, nil
	}
	return h, 200, nil
}
