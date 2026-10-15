package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/docker"
)

// ───────────────────────── manual stops (§21 #89 d)
//
// A container stopped from the toolbox is "stopped manually": the GUI shows it as such (not failed) and the toolbox
// never restarts it. The mark ends with a start / restart (toolbox or anything else that starts it) or when a new
// revision becomes the head (the next update recreates / starts containers as the config says). The agent has no
// reconciler that restarts stopped containers; Docker's restart policy does not restart a stopped container either.

type manualStop struct {
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	HeadSeq int       `json:"headSeq"`
}

type manualStops struct {
	mu   sync.Mutex
	path string
	m    map[string]manualStop
}

func loadManualStops(path string) *manualStops {
	ms := &manualStops{path: path, m: map[string]manualStop{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &ms.m)
	}
	return ms
}

func (ms *manualStops) saveLocked() {
	_ = os.MkdirAll(filepath.Dir(ms.path), 0o700)
	b, _ := json.Marshal(ms.m)
	_ = os.WriteFile(ms.path, b, 0o600)
}

func (ms *manualStops) set(ref string, v manualStop) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.m[ref] = v
	ms.saveLocked()
}

func (ms *manualStops) clear(ref string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if _, ok := ms.m[ref]; ok {
		delete(ms.m, ref)
		ms.saveLocked()
	}
}

// get returns the mark if it is still valid (same head seq, container not running).
func (ms *manualStops) get(ref string, headSeq int, running bool) *manualStop {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	v, ok := ms.m[ref]
	if !ok {
		return nil
	}
	if running || v.HeadSeq != headSeq {
		delete(ms.m, ref)
		ms.saveLocked()
		return nil
	}
	return &v
}

// ManuallyStopped reports whether a container is marked stopped manually (for the agent's status page / gate).
func (s *Service) ManuallyStopped(containerSlug string) bool {
	s.stops.mu.Lock()
	v, ok := s.stops.m[containerSlug]
	s.stops.mu.Unlock()
	return ok && v.HeadSeq == s.h.HeadSeq()
}

// ───────────────────────── container rows

func (s *Service) target(ctx context.Context, ref string) (*Target, *Error) {
	ts, err := s.h.Targets(ctx)
	if err != nil {
		return nil, unavailable(err)
	}
	for i := range ts {
		if ts[i].Ref == ref {
			return &ts[i], nil
		}
	}
	return nil, notFound("no container %q on site %s", ref, s.h.Site())
}

func (s *Service) row(ctx context.Context, t Target, list map[string]docker.ContainerSummary) map[string]any {
	row := map[string]any{"siteSlug": s.h.Site(), "ref": t.Ref, "containerSlug": t.ContainerSlug, "name": t.Name, "id": nil, "serviceSlug": t.ServiceSlug,
		"image": t.Image, "failedCopy": t.FailedCopy, "state": "missing", "status": "", "health": orStr(t.Health, "none"), "startedAt": nil, "finishedAt": nil,
		"exitCode": nil, "restartCount": 0, "oomKilled": false, "manuallyStopped": nil, "expectedRunning": t.Enabled && !t.FailedCopy && t.Kind != "job",
		"ports": []map[string]any{}, "cpuPct": nil, "memBytes": nil, "memLimitBytes": nil}
	ports := []map[string]any{}
	for _, p := range t.Ports {
		ports = append(ports, map[string]any{"containerPort": p.ContainerPort, "hostPort": p.HostPort, "protocol": orStr(p.Protocol, "tcp")})
	}
	row["ports"] = ports
	if t.ID == "" {
		return row
	}
	row["id"] = short(t.ID)
	running := false
	if cj, err := s.h.Docker().ContainerInspect(ctx, t.ID); err == nil {
		row["state"] = cj.State.Status
		running = cj.State.Running
		if !strings.HasPrefix(cj.State.StartedAt, "0001") && cj.State.StartedAt != "" {
			row["startedAt"] = cj.State.StartedAt
		}
		if !strings.HasPrefix(cj.State.FinishedAt, "0001") && cj.State.FinishedAt != "" {
			row["finishedAt"] = cj.State.FinishedAt
		}
		if !cj.State.Running {
			row["exitCode"] = cj.State.ExitCode
		}
		row["restartCount"] = cj.RestartCount
		row["oomKilled"] = cj.State.OOMKilled
		if t.Name == "" {
			row["name"] = strings.TrimPrefix(cj.Name, "/")
		}
	}
	if cs, ok := list[t.ID]; ok {
		row["status"] = cs.Status
	}
	if ms := s.stops.get(t.Ref, s.h.HeadSeq(), running); ms != nil {
		var actor *string
		if ms.Actor != "" {
			a := ms.Actor
			actor = &a
		}
		row["manuallyStopped"] = map[string]any{"at": ts(ms.At), "actor": actor}
	}
	if smp := s.Metrics.Latest(t.Ref); smp != nil && running {
		row["cpuPct"], row["memBytes"], row["memLimitBytes"] = smp.CPUPct, smp.MemBytes, smp.MemLimitBytes
	}
	return row
}

func short(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func orStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (s *Service) summaries(ctx context.Context) map[string]docker.ContainerSummary {
	out := map[string]docker.ContainerSummary{}
	if list, err := s.h.Docker().ContainerList(ctx, true); err == nil {
		for _, c := range list {
			out[c.ID] = c
		}
	}
	return out
}

func (s *Service) handleContainers(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	ts, err := s.h.Targets(r.Context())
	if err != nil {
		return nil, 0, unavailable(err)
	}
	list := s.summaries(r.Context())
	items := make([]map[string]any, len(ts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, t := range ts {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			defer s.h.Log().Recover("tools", "container row "+t.ID)
			sem <- struct{}{}
			defer func() { <-sem }()
			items[i] = s.row(r.Context(), t, list)
		}(i, t)
	}
	wg.Wait()
	return map[string]any{"items": items}, 200, nil
}

type actionReq struct {
	Reason string `json:"reason"`
}

func (s *Service) handleAction(kind string) Handler {
	return func(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
		if e := s.rl.allow("action", c); e != nil {
			return nil, 0, e
		}
		var req actionReq
		if e := readBody(r, &req); e != nil {
			return nil, 0, e
		}
		if len(req.Reason) > 300 {
			return nil, 0, invalid("reason: at most 300 characters")
		}
		t, e := s.target(r.Context(), r.PathValue("ref"))
		if e != nil {
			return nil, 0, e
		}
		if t.FailedCopy {
			return nil, 0, invalid("%s is a kept copy of a failed update: it cannot be started or stopped", t.Ref)
		}
		if t.ID == "" {
			return nil, 0, &Error{Status: 409, Code: "conflict", Msg: t.Ref + " has no container on this site (missing)"}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		dc := s.h.Docker()
		var err error
		typ, verb := "", ""
		data := map[string]any{"containerSlug": t.ContainerSlug, "ref": t.Ref, "siteSlug": s.h.Site()}
		if req.Reason != "" {
			data["reason"] = req.Reason
		}
		switch kind {
		case "restart":
			err = dc.ContainerRestart(ctx, t.ID, -1)
			typ, verb = "container.restarted", "restarted"
			s.stops.clear(t.Ref)
		case "stop":
			err = dc.ContainerStop(ctx, t.ID, -1)
			typ, verb = "container.stopped", "stopped"
			data["expectedRunning"] = t.Enabled && t.Kind != "job"
			if err == nil {
				s.stops.set(t.Ref, manualStop{At: time.Now(), Actor: c.Actor, HeadSeq: s.h.HeadSeq()})
			}
		case "start":
			err = dc.ContainerStart(ctx, t.ID)
			typ, verb = "container.started", "started"
			s.stops.clear(t.Ref)
		}
		if err != nil {
			s.h.Log().Warn("tools", "container action failed", "action", kind, "container", t.Ref, "error", err.Error())
			return nil, 0, unavailable(err)
		}
		s.activity(c, typ, fmt.Sprintf("container %s %s on %s", t.Ref, verb, s.h.Site()), data)
		s.h.Log().Info("tools", "container "+verb, "container", t.Ref, "actor", c.Actor, "via", c.Via)
		return map[string]any{"ok": true, "container": s.row(r.Context(), *t, s.summaries(r.Context()))}, 200, nil
	}
}

// ───────────────────────── inspect / top / events

var sensitiveEnv = regexp.MustCompile(`(?i)(pass|secret|token|key|credential|private|auth|cookie|session|dsn|conn)`)

func (s *Service) handleInspect(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	t, e := s.target(r.Context(), r.PathValue("ref"))
	if e != nil {
		return nil, 0, e
	}
	if t.ID == "" {
		return nil, 0, notFound("%s has no container on this site (missing)", t.Ref)
	}
	raw, err := s.h.Docker().InspectRaw(r.Context(), t.ID)
	if err != nil {
		return nil, 0, unavailable(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, 0, unavailable(err)
	}
	secrets := append(append([]string{}, t.SecretValues...), s.h.SecretValues()...)
	secretEnv := map[string]bool{}
	for _, n := range t.SecretEnv {
		secretEnv[n] = true
	}
	masked := []string{}
	if cfg, ok := doc["Config"].(map[string]any); ok {
		if env, ok := cfg["Env"].([]any); ok {
			for i, x := range env {
				kv, _ := x.(string)
				k, v, _ := strings.Cut(kv, "=")
				if v != "" && (secretEnv[k] || sensitiveEnv.MatchString(k) || MaskString(v, secrets) != v) {
					env[i] = k + "=" + MaskText
					masked = append(masked, k)
				}
			}
		}
	}
	doc = maskAll(doc, secrets).(map[string]any)
	healthLog := []map[string]any{}
	if st, ok := doc["State"].(map[string]any); ok {
		if h, ok := st["Health"].(map[string]any); ok {
			if logs, ok := h["Log"].([]any); ok {
				for _, x := range logs {
					if m, ok := x.(map[string]any); ok {
						healthLog = append(healthLog, map[string]any{"start": m["Start"], "end": m["End"], "exitCode": m["ExitCode"], "output": m["Output"]})
					}
				}
			}
		}
	}
	checks := []map[string]any{}
	for _, ch := range s.h.Checks(t.ContainerSlug) {
		checks = append(checks, map[string]any{"at": ts(ch.At), "ok": ch.OK, "message": MaskString(ch.Message, secrets)})
	}
	sort.Strings(masked)
	return map[string]any{"ref": t.Ref, "inspect": doc, "maskedEnv": masked, "healthLog": healthLog, "checks": checks}, 200, nil
}

// maskAll masks secret values in every string of a JSON document.
func maskAll(v any, secrets []string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			x[k] = maskAll(vv, secrets)
		}
		return x
	case []any:
		for i := range x {
			x[i] = maskAll(x[i], secrets)
		}
		return x
	case string:
		return MaskString(x, secrets)
	}
	return v
}

func (s *Service) handleTop(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	t, e := s.target(r.Context(), r.PathValue("ref"))
	if e != nil {
		return nil, 0, e
	}
	if t.ID == "" {
		return nil, 0, notFound("%s has no container on this site (missing)", t.Ref)
	}
	top, err := s.h.Docker().ContainerTop(r.Context(), t.ID, "")
	if err != nil {
		if strings.Contains(err.Error(), "is not running") || strings.Contains(err.Error(), "409") {
			return nil, 0, &Error{Status: 409, Code: "conflict", Msg: t.Ref + " is not running"}
		}
		return nil, 0, unavailable(err)
	}
	if top.Titles == nil {
		top.Titles = []string{}
	}
	if top.Processes == nil {
		top.Processes = [][]string{}
	}
	secrets := s.h.SecretValues()
	for _, p := range top.Processes {
		for i := range p {
			p[i] = MaskString(p[i], secrets)
		}
	}
	return map[string]any{"ref": t.Ref, "titles": top.Titles, "processes": top.Processes}, 200, nil
}

func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	t, e := s.target(r.Context(), r.PathValue("ref"))
	if e != nil {
		return nil, 0, e
	}
	now := time.Now()
	since := now.Add(-24 * time.Hour)
	if q := r.URL.Query().Get("since"); q != "" {
		if tt, err := time.Parse(time.RFC3339Nano, q); err == nil && tt.After(since) {
			since = tt
		}
	}
	name := t.Name
	filters := map[string][]string{"type": {"container"}}
	if t.ID != "" {
		filters["container"] = []string{t.ID}
	} else if name != "" {
		filters["container"] = []string{name}
	} else {
		return map[string]any{"items": []any{}}, 200, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	evs, err := s.h.Docker().Events(ctx, since, now, filters)
	if err != nil {
		return nil, 0, unavailable(err)
	}
	items := []map[string]any{}
	for i := len(evs) - 1; i >= 0 && len(items) < 1000; i-- {
		ev := evs[i]
		tm := time.Unix(ev.Time, 0)
		if ev.TimeNano > 0 {
			tm = time.Unix(0, ev.TimeNano)
		}
		attrs := map[string]string{}
		for k, v := range ev.Actor.Attributes {
			if strings.HasPrefix(k, "opm.") || k == "image" || k == "name" || k == "exitCode" || k == "signal" || k == "execID" {
				attrs[k] = v
			}
		}
		if strings.HasPrefix(ev.Action, "exec_") { // exec commands may carry arguments (secrets)
			ev.Action = MaskString(ev.Action, s.h.SecretValues())
		}
		items = append(items, map[string]any{"time": ts(tm), "type": ev.Type, "action": ev.Action, "attributes": attrs})
	}
	return map[string]any{"items": items}, 200, nil
}
