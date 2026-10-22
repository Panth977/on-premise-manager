package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/movehome"
)

// §21 #119 storage view + moving the agent home to another folder / disk (internal/movehome does the move while the
// agent is stopped: `opm-agent move-home`, started here with systemd-run).

type homeSize struct {
	mu      sync.Mutex
	bytes   int64
	files   int
	at      time.Time
	running bool
}

var homeSizes sync.Map // dataDir → *homeSize

// measureHome: the size of the agent home (walked in the background, kept 10 min).
func (a *Agent) measureHome() (*int64, int, *time.Time) {
	v, _ := homeSizes.LoadOrStore(a.dataDir, &homeSize{})
	h := v.(*homeSize)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.running && time.Since(h.at) > 10*time.Minute {
		h.running = true
		go func() {
			n, c, _ := movehome.Size(a.dataDir)
			h.mu.Lock()
			h.bytes, h.files, h.at, h.running = n, c, time.Now(), false
			h.mu.Unlock()
		}()
	}
	if h.at.IsZero() {
		return nil, 0, nil
	}
	n, at := h.bytes, h.at
	return &n, h.files, &at
}

// fixedInsideHome: volumes / files with a fixed location (kind path) inside the home — a move would leave them behind.
func (a *Agent) fixedInsideHome() []string {
	cfg, _ := a.headConfigRO()
	s := siteOf(cfg, a.site)
	if s == nil {
		return nil
	}
	home := filepath.Clean(a.dataDir) + "/"
	var out []string
	for _, v := range s.Config.VolumeList() {
		if v.Location != nil && v.Location.Normalized().Kind == "path" && strings.HasPrefix(driver.VolumePath(a.dataDir, &s.Config, v.VolumeSlug)+"/", home) {
			out = append(out, "volume "+v.VolumeSlug)
		}
	}
	for i := range cfg.Files {
		f := &cfg.Files[i]
		if f.Location != nil && f.Location.Normalized().Kind == "path" && strings.HasPrefix(driver.FilePath(a.dataDir, f)+"/", home) {
			out = append(out, "file "+f.FileSlug)
		}
	}
	return out
}

func (a *Agent) cannotMove() string {
	switch {
	case a.self.sup.Kind() != "systemd":
		return "the agent does not run under systemd here: stop it, move the folder and change agent.json dataDir by hand"
	case a.lock() != nil:
		return "an update is running"
	case a.opt.ConfigPath == "":
		return "the agent has no agent.json path"
	}
	if st := movehome.ReadState(a.dataDir); st != nil && (st.Status == "prepared" || st.Status == "copying") {
		return "a move is already running"
	}
	return ""
}

func (a *Agent) handleStorage(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	v := model.StorageView{Home: a.dataDir, Filesystems: []model.StorageFs{}}
	n, files, at := a.measureHome()
	v.HomeBytes, v.HomeFiles = n, files
	if at != nil {
		ts := tsOf(*at)
		v.MeasuredAt = &ts
	}
	type role struct{ path, name string }
	roles := []role{{a.dataDir, "agent home"}}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	if info, err := a.dc.Info(ctx); err == nil && info.DockerRootDir != "" {
		d := info.DockerRootDir
		v.DockerRootDir = &d
		if _, err := os.Stat(d); err == nil {
			roles = append(roles, role{d, "Docker data (images, container layers)"})
		}
	}
	cancel()
	cfg, _ := a.headConfigRO()
	if s := siteOf(cfg, a.site); s != nil {
		for _, vol := range s.Config.VolumeList() {
			if vol.Location != nil && vol.Location.Normalized().Kind == "path" {
				roles = append(roles, role{driver.VolumePath(a.dataDir, &s.Config, vol.VolumeSlug), "volume " + vol.VolumeSlug})
			}
		}
	}
	byDev := map[uint64]int{}
	for _, ro := range roles {
		fs, ok := movehome.FsOf(ro.path)
		if !ok {
			continue
		}
		if i, ok := byDev[fs.Dev]; ok {
			v.Filesystems[i].Roles = append(v.Filesystems[i].Roles, ro.name)
			continue
		}
		byDev[fs.Dev] = len(v.Filesystems)
		v.Filesystems = append(v.Filesystems, model.StorageFs{Path: ro.path, TotalBytes: fs.Total, FreeBytes: fs.Free, UsedBytes: fs.Total - fs.Free, FloorBytes: DiskFloorFor(fs.Total), Roles: []string{ro.name}})
	}
	if st := movehome.ReadState(a.dataDir); st != nil {
		v.Move = st
	}
	if why := a.cannotMove(); why != "" {
		v.CannotMove = &why
	} else {
		v.CanMove = true
	}
	return v, 200, nil
}

func (a *Agent) checkMove(to string) model.MoveHomeCheck {
	out := model.MoveHomeCheck{To: filepath.Clean(strings.TrimSpace(to)), Errors: []string{}, Warnings: []string{}, Containers: []string{}}
	n, _, _ := movehome.Size(a.dataDir)
	out.SizeBytes = n
	same, fs, errs, warns := movehome.Check(a.dataDir, out.To, n, DiskFloorFor)
	out.SameFilesystem, out.TargetFreeBytes, out.TargetTotalBytes = same, fs.Free, fs.Total
	out.Errors = append(out.Errors, errs...)
	out.Warnings = append(out.Warnings, warns...)
	if why := a.cannotMove(); why != "" {
		out.Errors = append(out.Errors, why)
	}
	for _, x := range a.fixedInsideHome() {
		out.Errors = append(out.Errors, x+" has a fixed location inside the agent home: change it to auto (or outside) first")
	}
	if ex, err := a.drv.Existing(context.Background()); err == nil {
		for slug := range ex {
			out.Containers = append(out.Containers, slug)
		}
		sort.Strings(out.Containers)
	}
	out.Ok = len(out.Errors) == 0
	return out
}

func (a *Agent) handleMoveHomeCheck(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req model.MoveHomeRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	return a.checkMove(req.To), 200, nil
}

func (a *Agent) handleMoveHome(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.MoveHomeRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if req.Typed != "MOVE" {
		return nil, 0, invalid("type MOVE to confirm", []model.ValidationIssue{{Path: "typed", Code: "V_CONFIRM", Message: "type MOVE", Severity: "error"}})
	}
	ck := a.checkMove(req.To)
	if !ck.Ok {
		return nil, 0, invalid(ck.Errors[0], nil)
	}
	mode := "copy"
	if ck.SameFilesystem {
		mode = "rename"
	}
	st := &movehome.State{MoveID: newID(), Status: "prepared", From: filepath.Clean(a.dataDir), To: ck.To, Mode: mode, Actor: rc.actor, StartedAt: nowTs(), SizeBytes: ck.SizeBytes, Stopped: []string{}}
	// the containers mount files under the home: stop them (the next start re-applies the head with the new paths)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if ex, err := a.drv.Existing(ctx); err == nil {
		for slug, e := range ex {
			if err := a.drv.Stop(ctx, e.ID, 30); err == nil {
				st.Stopped = append(st.Stopped, slug)
			}
		}
		sort.Strings(st.Stopped)
	}
	_, _ = a.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := movehome.WriteState(a.dataDir, st); err != nil {
		a.startStopped(st.Stopped)
		return nil, 0, herr(model.ErrInternal, "write the move state: "+err.Error(), nil)
	}
	a.activity(rc.actorPtr(), "agent", "agent.home-move-started", trim200(fmt.Sprintf("moving the agent home %s → %s (%s, %s)", st.From, st.To, mode, humanBytes(st.SizeBytes))),
		actorData(rc, map[string]any{"from": st.From, "to": st.To, "mode": mode, "sizeBytes": st.SizeBytes, "stopped": st.Stopped}))
	args := []string{"--quiet", "--collect", "--unit", "opm-agent-move-home-" + strings.ToLower(st.MoveID[len(st.MoveID)-8:]), a.self.binary, "move-home", "--config", a.opt.ConfigPath, "--from", st.From, "--to", st.To}
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		st.Status = "failed"
		msg := fmt.Sprintf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
		st.Error = &msg
		_ = movehome.WriteState(a.dataDir, st)
		a.startStopped(st.Stopped)
		return nil, 0, herr(model.ErrInternal, msg, nil)
	}
	return st, 202, nil
}

// startStopped starts containers stopped for a move that did not happen.
func (a *Agent) startStopped(slugs []string) {
	ex, err := a.drv.Existing(context.Background())
	if err != nil {
		return
	}
	for _, s := range slugs {
		if e, ok := ex[s]; ok {
			_ = a.dc.ContainerStart(context.Background(), e.ID)
		}
	}
}

// finishHomeMove runs at start: a move that copied into this home → opm.db paths, units, containers re-applied; a
// move that failed out of this home → its stopped containers started again.
func (a *Agent) finishHomeMove() {
	st := movehome.ReadState(a.dataDir)
	if st == nil {
		return
	}
	switch {
	case st.Status == "copied" && filepath.Clean(st.To) == filepath.Clean(a.dataDir):
		from, to := filepath.Clean(st.From), filepath.Clean(a.dataDir)
		for _, q := range []string{
			`UPDATE blobs SET path = ? || substr(path, ?) WHERE path LIKE ?`,
			`UPDATE debug_index SET file = ? || substr(file, ?) WHERE file LIKE ?`,
			`UPDATE transfers SET localPath = ? || substr(localPath, ?) WHERE localPath LIKE ?`,
			`UPDATE files SET localPath = ? || substr(localPath, ?) WHERE localPath LIKE ?`, // §21 #123
		} {
			if _, err := a.db.Exec(q, to, len(from)+1, from+"/%"); err != nil {
				a.log.Warn("gui", "move: path rewrite failed", "error", err.Error())
			}
		}
		now := nowTs()
		st.Status, st.FinishedAt = "done", &now
		_ = movehome.WriteState(a.dataDir, st)
		a.activity(nil, "agent", "agent.home-moved", trim200(fmt.Sprintf("agent home moved %s → %s", from, to)), map[string]any{"from": from, "to": to, "mode": st.Mode, "oldCopy": st.OldCopy, "sizeBytes": st.SizeBytes, "files": st.Files})
		a.log.Info("gui", "agent home moved", "from", from, "to", to)
		go func() {
			// the containers mount the new paths: re-apply the head (recreates them; started again)
			time.Sleep(5 * time.Second)
			if a.headRevision() == nil {
				a.startStopped(st.Stopped)
				return
			}
			if _, he := a.reapplyHead("system"); he != nil {
				a.log.Error("gui", "move: re-apply failed", "error", he.msg)
				a.startStopped(st.Stopped)
			}
		}()
	case st.Status == "failed" && filepath.Clean(st.From) == filepath.Clean(a.dataDir) && st.FinishedAt != nil && !st.Restarted:
		st.Restarted = true
		_ = movehome.WriteState(a.dataDir, st)
		msg := ""
		if st.Error != nil {
			msg = *st.Error
		}
		a.activity(nil, "agent", "agent.home-move-failed", trim200("moving the agent home failed: "+msg), map[string]any{"from": st.From, "to": st.To, "error": msg})
		go func() {
			time.Sleep(3 * time.Second)
			a.startStopped(st.Stopped)
		}()
	}
}

func (a *Agent) handleDeleteOldHome(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.MoveHomeRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if req.Typed != "DELETE" {
		return nil, 0, invalid("type DELETE to confirm", nil)
	}
	st := movehome.ReadState(a.dataDir)
	if st == nil || st.Status != "done" || st.OldCopy == nil {
		return nil, 0, herr(model.ErrNotFound, "no old copy of the agent home to delete", nil)
	}
	old := filepath.Clean(*st.OldCopy)
	if old == filepath.Clean(a.dataDir) || !strings.Contains(filepath.Base(old), ".moved-") || strings.HasPrefix(filepath.Clean(a.dataDir)+"/", old+"/") {
		return nil, 0, invalid("refusing to delete "+old, nil)
	}
	n, _, _ := movehome.Size(old)
	if err := os.RemoveAll(old); err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	st.OldCopy = nil
	_ = movehome.WriteState(a.dataDir, st)
	a.activity(rc.actorPtr(), "agent", "agent.home-old-deleted", trim200(fmt.Sprintf("old agent home %s deleted (%s freed)", old, humanBytes(n))), actorData(rc, map[string]any{"path": old, "sizeBytes": n}))
	return st, 200, nil
}
