package agent

// Update failure diagnostics (SPEC-final §21 #87): before a rollback removes a container that failed APPLY / VERIFY, the
// agent captures its last 200 log lines (secrets redacted), the docker inspect state, the port map and the health check
// history, stores them in the update record (UpdateSiteState.containers[].failure), the update.failed activity (capped)
// and the debug log, and keeps the stopped container as `<name>-failed-<unix>` for `docker logs` until the next
// successful update of the site or 24 h.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
)

const (
	failureLogLines   = 200
	failureLineMax    = 2000
	failedKeepFor     = 24 * time.Hour
	activityLogLines  = 40
	activityFailBytes = 12 * 1024
)

// captureFailure collects the diagnostics of container sid (docker id may be "" when it was never created) and keeps
// the stopped container under a -failed- name.
func (a *Agent) captureFailure(sid string, b *driver.Built, id string, cause error) *model.ContainerFailure {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	f := &model.ContainerFailure{ContainerSlug: sid, Error: a.log.Scrub(cause.Error()), Ports: "none", LogTail: []string{}, HealthHistory: []model.HealthCheckRecord{}, CapturedAt: nowTs()}
	if b != nil {
		f.DockerName = b.Name
		for _, r := range a.drv.History(sid) {
			f.HealthHistory = append(f.HealthHistory, model.HealthCheckRecord{At: tsOf(r.At), OK: r.OK, Counted: r.Counted, Message: a.log.Scrub(r.Message)})
		}
	}
	if id == "" {
		return f
	}
	if cj, err := a.dc.ContainerInspect(ctx, id); err == nil {
		f.DockerName = strings.TrimPrefix(cj.Name, "/")
		f.State = &model.ContainerFailureState{Status: cj.State.Status, ExitCode: cj.State.ExitCode, OOMKilled: cj.State.OOMKilled, RestartCount: cj.RestartCount,
			Error: cj.State.Error, StartedAt: cj.State.StartedAt, FinishedAt: cj.State.FinishedAt}
		f.Ports = driver.PortMapText(cj)
	}
	if lines, err := a.dc.ContainerLogs(ctx, id, failureLogLines, ""); err == nil {
		for _, l := range lines {
			t := l.Text
			if len(t) > failureLineMax {
				t = t[:failureLineMax] + "…"
			}
			f.LogTail = append(f.LogTail, a.log.Scrub(strings.TrimSpace(l.Ts+" "+l.Stream+" "+t)))
		}
	} else {
		f.LogTail = append(f.LogTail, "(logs unavailable: "+err.Error()+")")
	}
	// keep it: stop (a restart policy would bring it back) and rename so the rollback can recreate the original name
	if f.DockerName != "" && !driver.IsFailedName(f.DockerName) {
		_ = a.drv.Stop(ctx, id, 10)
		kept := fmt.Sprintf("%s-failed-%d", f.DockerName, time.Now().Unix())
		if err := a.dc.ContainerRename(ctx, id, kept); err == nil {
			f.KeptAs = &kept
		} else {
			a.log.Warn("driver", "could not keep the failed container", "container", f.DockerName, "error", err.Error())
		}
	}
	a.drv.ForgetHealth(sid)
	tail := f.LogTail
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}
	st := map[string]any{}
	if f.State != nil {
		st = map[string]any{"exitCode": f.State.ExitCode, "oomKilled": f.State.OOMKilled, "restartCount": f.State.RestartCount, "status": f.State.Status}
	}
	a.log.Warn("driver", "container failed the update", "container", sid, "error", f.Error, "state", st, "ports", f.Ports, "keptAs", f.KeptAs, "logTail", strings.Join(tail, "\n"))
	return f
}

// setContainerFailure attaches diagnostics to the container row of an update site state.
func (a *Agent) setContainerFailure(st *model.UpdateSiteState, f *model.ContainerFailure) {
	if st == nil || f == nil {
		return
	}
	for i := range st.Containers {
		if st.Containers[i].ContainerSlug == f.ContainerSlug {
			st.Containers[i].Failure = f
			a.setSiteState(st)
			return
		}
	}
	e := f.Error
	st.Containers = append(st.Containers, model.UpdateContainerState{ContainerSlug: f.ContainerSlug, State: "failed", Error: &e, Failure: f})
	a.setSiteState(st)
}

// failureActivityData: the failures of a site state for the update.failed activity (≤ 12 KiB; the activity cap is 16).
func failureActivityData(st *model.UpdateSiteState) []map[string]any {
	if st == nil {
		return nil
	}
	var out []map[string]any
	for _, c := range st.Containers {
		if c.Failure == nil {
			continue
		}
		f := c.Failure
		m := map[string]any{"containerSlug": f.ContainerSlug, "error": f.Error, "ports": f.Ports, "keptAs": f.KeptAs}
		if f.Hint != nil {
			m["hint"] = *f.Hint // §21 #93
		}
		if f.State != nil {
			m["exitCode"], m["oomKilled"], m["restartCount"] = f.State.ExitCode, f.State.OOMKilled, f.State.RestartCount
		}
		tail := f.LogTail
		if len(tail) > activityLogLines {
			tail = tail[len(tail)-activityLogLines:]
		}
		m["logTail"] = append([]string(nil), tail...)
		out = append(out, m)
	}
	// size cap: halve the log tails until the list fits
	for n := activityLogLines; n > 0 && jsonLen(out) > activityFailBytes; n /= 2 {
		for _, m := range out {
			t := m["logTail"].([]string)
			if len(t) > n {
				m["logTail"] = t[len(t)-n:]
			}
			for i, l := range m["logTail"].([]string) {
				if len(l) > 300 {
					m["logTail"].([]string)[i] = l[:300] + "…"
				}
			}
		}
	}
	for jsonLen(out) > activityFailBytes && len(out) > 1 {
		out = out[:len(out)-1]
	}
	return out
}

func jsonLen(v any) int {
	b, _ := json.Marshal(v)
	return len(b)
}

// cleanupFailedContainers removes kept failed containers: all of them (after a successful update) or those older than
// failedKeepFor (housekeeping).
func (a *Agent) cleanupFailedContainers(ctx context.Context, all bool) {
	list, err := a.drv.FailedContainers(ctx)
	if err != nil {
		return
	}
	for id, name := range list {
		i := strings.LastIndex(name, "-failed-")
		ts, _ := strconv.ParseInt(name[i+len("-failed-"):], 10, 64)
		if !all && time.Since(time.Unix(ts, 0)) < failedKeepFor {
			continue
		}
		if err := a.dc.ContainerRemove(ctx, id, true); err == nil {
			a.log.Info("driver", "kept failed container removed", "container", name)
		}
	}
}
