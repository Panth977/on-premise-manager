package agent

// §21 #96 volume manager — long volume actions on THIS site: empty, copy (to a host path or into another volume),
// restore (from a copy or an uploaded .tar.gz), snapshot of one volume, and the list of copies. Every action is an
// operation (VolumeOp) that the GUI polls; one per volume at a time; refused while an update runs (and an update is
// refused while one runs). Containers that mount the volume and run are stopped in REVERSE start order and started
// again in start order (§15.5) — also when the action fails. Another site: the per-site routes are forwarded over the
// mesh like every other per-site call (the forwarding site logs volume.action-forwarded).

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/tools"
)

const (
	volumeCopiesDir   = "volume-copies"
	volumeOpsKeep     = 50
	restoreUploadTTL  = time.Hour
	volumeCopiesKVKey = "volumeCopies"
)

// systemDirs: a copy never goes there (TS SYSTEM_DIRS).
var systemDirs = []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/proc", "/root/.ssh", "/run", "/sbin", "/sys", "/usr", "/var/lib/docker", "/var/run"}

// ───────────────────────── state

func (a *Agent) volOpsPath() string { return filepath.Join(a.dataDir, "volume-ops.json") }

// loadVolumeOps reads the op history; an op that was running when the agent stopped is failed and the containers it
// had stopped are started again.
func (a *Agent) loadVolumeOps() {
	b, err := os.ReadFile(a.volOpsPath())
	if err != nil {
		return
	}
	var ops []*model.VolumeOp
	if json.Unmarshal(b, &ops) != nil {
		return
	}
	var interrupted []*model.VolumeOp
	a.volMu.Lock()
	for _, op := range ops {
		if op.FinishedAt == nil {
			msg := "the agent stopped during the action"
			op.Error, op.Phase, op.Ok = &msg, "failed", ptrBool(false)
			now := nowTs()
			op.FinishedAt = &now
			interrupted = append(interrupted, op)
		}
		a.volOps[op.OpID] = op
	}
	a.volMu.Unlock()
	for _, op := range interrupted {
		if len(op.Stopped) > 0 {
			ex, _ := a.drv.Existing(context.Background())
			for _, cs := range op.Stopped {
				if e, ok := ex[cs]; ok && !e.Running {
					_ = a.dc.ContainerStart(context.Background(), e.ID)
				}
			}
		}
		a.activity(nil, "system", "volume.op-failed", fmt.Sprintf("%s of volume %s interrupted (agent restart)", op.Kind, op.VolumeSlug),
			map[string]any{"volumeSlug": op.VolumeSlug, "siteSlug": a.site, "opId": op.OpID, "kind": op.Kind, "step": "agent-restart", "error": *op.Error, "stopped": op.Stopped})
	}
	a.saveVolumeOps()
}

func ptrBool(b bool) *bool { return &b }
func ptrStr(s string) *string {
	return &s
}

func (a *Agent) saveVolumeOps() {
	a.volMu.Lock()
	ops := make([]*model.VolumeOp, 0, len(a.volOps))
	for _, op := range a.volOps {
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].StartedAt > ops[j].StartedAt })
	if len(ops) > volumeOpsKeep {
		for _, op := range ops[volumeOpsKeep:] {
			if op.FinishedAt != nil {
				delete(a.volOps, op.OpID)
			}
		}
		ops = ops[:volumeOpsKeep]
	}
	b, _ := json.Marshal(ops)
	a.volMu.Unlock()
	_ = writeFileAtomic(a.volOpsPath(), b, 0o600)
}

func writeFileAtomic(p string, b []byte, mode fs.FileMode) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// volumeBusy: why a volume cannot change now ("" = free) — tools file writes use it too.
func (a *Agent) volumeBusy(slug string) string {
	a.volMu.Lock()
	defer a.volMu.Unlock()
	if id, ok := a.volBusy[slug]; ok {
		if op := a.volOps[id]; op != nil {
			return fmt.Sprintf("%s running (operation %s)", op.Kind, id)
		}
	}
	return ""
}

// anyVolumeOp: an update must not start while a volume action runs.
func (a *Agent) anyVolumeOp() string {
	a.volMu.Lock()
	defer a.volMu.Unlock()
	for slug, id := range a.volBusy {
		if op := a.volOps[id]; op != nil {
			return fmt.Sprintf("%s of volume %s is running", op.Kind, slug)
		}
	}
	a.volMu.Unlock()
	defer a.volMu.Lock()
	return a.anyContentBusy() // §21 #101
}

func (a *Agent) volRateOK(rc *reqCtx) *httpError {
	key := rc.sessionID
	now := time.Now()
	a.volMu.Lock()
	defer a.volMu.Unlock()
	var keep []time.Time
	for _, t := range a.volRate[key] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= model.VolumeOpRatePerMinute {
		retry := 60 - int(now.Sub(keep[0]).Seconds())
		if retry < 1 {
			retry = 1
		}
		a.volRate[key] = keep
		e := herr(model.ErrRateLimited, fmt.Sprintf("at most %d volume actions per minute; try again in %d s", model.VolumeOpRatePerMinute, retry), map[string]any{"retryAfterSec": retry})
		e.header = map[string]string{"Retry-After": fmt.Sprint(retry)}
		return e
	}
	a.volRate[key] = append(keep, now)
	return nil
}

// ───────────────────────── target volume

type volTarget struct {
	cfg  *model.Config
	cat  *model.RevisionCatalog
	site *model.Site
	decl *model.SiteVolume
	ref  volRef
}

func (a *Agent) volumeTarget(slug string) (*volTarget, *httpError) {
	cfg, cat := a.headConfig()
	site := siteOf(cfg, a.site)
	if site == nil {
		return nil, herr(model.ErrNotFound, "this site has no head config yet", nil)
	}
	v := site.Config.FindVolume(slug)
	if v == nil {
		return nil, herr(model.ErrNotFound, "site "+a.site+" declares no volume "+slug, nil)
	}
	for _, x := range siteVolumes(a.dataDir, &site.Config) {
		if x.Name == slug {
			return &volTarget{cfg: cfg, cat: cat, site: site, decl: v, ref: x}, nil
		}
	}
	return nil, herr(model.ErrNotFound, "volume "+slug+" not found", nil)
}

// guard: the volume free (§21 #110: a running update does not block).
func (a *Agent) volGuard(slugs ...string) *httpError {
	for _, s := range slugs {
		if why := a.volumeBusy(s); why != "" {
			return herr(model.ErrConflict, "volume "+s+" is busy: "+why, nil)
		}
		if why := a.contentBusy(s); why != "" { // §21 #101 an apply of a cloud version
			return herr(model.ErrConflict, "volume "+s+" is busy: "+why, nil)
		}
	}
	return nil
}

// ───────────────────────── containers: stop in reverse start order, start in start order

func (a *Agent) volumeStartOrder(t *volTarget) []string {
	order, _ := a.startPlan(t.cfg, t.cat, t.site, nil)
	return order
}

// stopUsers stops the running containers among slugs (reverse start order); returns the stopped slugs in START order.
func (a *Agent) stopUsers(ctx context.Context, t *volTarget, slugs []string) ([]string, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	ex, err := a.drv.Existing(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	order := a.volumeStartOrder(t)
	rank := map[string]int{}
	for i, s := range order {
		rank[s] = i
	}
	var run []string
	for _, cs := range slugs {
		if e, ok := ex[cs]; ok && e.Running && !contains(run, cs) {
			run = append(run, cs)
		}
	}
	sort.SliceStable(run, func(i, j int) bool { return rankOf(rank, run[i]) < rankOf(rank, run[j]) })
	for i := len(run) - 1; i >= 0; i-- {
		if err := a.drv.Stop(ctx, ex[run[i]].ID, -1); err != nil {
			return run[i+1:], fmt.Errorf("stop %s: %w", run[i], err)
		}
		a.log.Info("volume", "container stopped for a volume action", "container", run[i])
	}
	return run, nil
}

func rankOf(rank map[string]int, s string) int {
	if r, ok := rank[s]; ok {
		return r
	}
	return 1 << 20
}

// startUsers starts the stopped containers again (start order); errors are collected.
func (a *Agent) startUsers(stopped []string) error {
	ctx := context.Background()
	ex, _ := a.drv.Existing(ctx)
	var errs []string
	for _, cs := range stopped {
		e, ok := ex[cs]
		if !ok {
			errs = append(errs, cs+": missing")
			continue
		}
		if e.Running {
			continue
		}
		if err := a.dc.ContainerStart(ctx, e.ID); err != nil {
			errs = append(errs, cs+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// ───────────────────────── operation runner

type opCtx struct {
	a  *Agent
	op *model.VolumeOp
}

func (o *opCtx) step(name string, state string, detail string) {
	o.a.volMu.Lock()
	for i := range o.op.Steps {
		s := &o.op.Steps[i]
		if s.Name != name {
			continue
		}
		now := nowTs()
		if state == "running" {
			s.StartedAt = &now
			o.op.Phase = name
		}
		if state == "done" || state == "failed" || state == "skipped" {
			s.FinishedAt = &now
		}
		s.State = state
		if detail != "" {
			s.Detail = ptrStr(detail)
		}
	}
	o.a.volMu.Unlock()
	o.a.saveVolumeOps()
}

func (o *opCtx) set(f func()) {
	o.a.volMu.Lock()
	f()
	o.a.volMu.Unlock()
}

func (o *opCtx) progress(done, files int64) {
	o.a.volMu.Lock()
	o.op.Progress.BytesDone, o.op.Progress.Files = done, files
	o.a.volMu.Unlock()
}

func (o *opCtx) total(n int64) {
	o.a.volMu.Lock()
	o.op.Progress.BytesTotal = &n
	o.a.volMu.Unlock()
}

func (o *opCtx) warn(w string) {
	o.a.volMu.Lock()
	if len(o.op.Warnings) < 20 && !contains(o.op.Warnings, w) {
		o.op.Warnings = append(o.op.Warnings, w)
	}
	o.a.volMu.Unlock()
}

func (a *Agent) newVolumeOp(kind string, t *volTarget, actor string, steps []string) *model.VolumeOp {
	op := &model.VolumeOp{OpID: newID(), SiteSlug: a.site, VolumeSlug: t.ref.Name, VolumeKind: orDefault(t.ref.Kind, "dir"), Kind: kind, Actor: actor,
		StartedAt: nowTs(), Phase: "queued", Steps: []model.VolumeOpStep{}, Stopped: []string{}, Warnings: []string{}}
	for _, s := range steps {
		op.Steps = append(op.Steps, model.VolumeOpStep{Name: s, State: "pending"})
	}
	return op
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// claim registers the op and locks its volumes (false = a volume is busy).
func (a *Agent) claim(op *model.VolumeOp, slugs ...string) bool {
	a.volMu.Lock()
	for _, s := range slugs {
		if _, busy := a.volBusy[s]; busy {
			a.volMu.Unlock()
			return false
		}
	}
	for _, s := range slugs {
		a.volBusy[s] = op.OpID
	}
	a.volOps[op.OpID] = op
	a.volMu.Unlock()
	a.saveVolumeOps()
	return true
}

// run executes fn in the background; finish writes the result, the activity and unlocks.
func (a *Agent) runVolumeOp(op *model.VolumeOp, slugs []string, fn func(o *opCtx) (string, error), onOK func(o *opCtx) (typ, summary string, data map[string]any)) {
	o := &opCtx{a: a, op: op}
	go func() {
		start := time.Now()
		failedStep, err := func() (step string, err error) {
			defer func() {
				if p := recover(); p != nil {
					step, err = op.Phase, fmt.Errorf("panic: %v", p)
				}
			}()
			return fn(o)
		}()
		// always start what was stopped
		if len(op.Stopped) > 0 && hasStep(op, "start") {
			o.step("start", "running", strings.Join(op.Stopped, ", "))
			if serr := a.startUsers(op.Stopped); serr != nil {
				o.step("start", "failed", serr.Error())
				if err == nil {
					failedStep, err = "start", serr
				} else {
					o.warn("start again: " + serr.Error())
				}
			} else {
				o.step("start", "done", strings.Join(op.Stopped, ", ")+" started")
			}
		} else if hasStep(op, "start") {
			o.step("start", "skipped", "no container was running")
		}
		dur := time.Since(start).Milliseconds()
		a.volMu.Lock()
		now := nowTs()
		op.FinishedAt, op.DurationMs = &now, &dur
		for i := range op.Steps {
			if op.Steps[i].State == "pending" {
				op.Steps[i].State = "skipped"
			}
			if op.Steps[i].State == "running" {
				op.Steps[i].State = "failed"
			}
		}
		if err != nil {
			op.Ok, op.Phase, op.Error = ptrBool(false), "failed", ptrStr(err.Error())
		} else {
			op.Ok, op.Phase = ptrBool(true), "done"
		}
		for _, s := range slugs {
			if a.volBusy[s] == op.OpID {
				delete(a.volBusy, s)
			}
		}
		a.volMu.Unlock()
		a.saveVolumeOps()
		tools.ForgetVolumeSizes(a.dataDir)
		a.forgetVolumeSizes()
		var who *string
		if op.Actor != "" {
			who = &op.Actor
		}
		if err != nil {
			a.log.Warn("volume", "volume action failed", "op", op.OpID, "kind", op.Kind, "volume", op.VolumeSlug, "step", failedStep, "error", err.Error())
			a.activity(who, "agent", "volume.op-failed", trim200(fmt.Sprintf("%s of volume %s failed at %s: %s", op.Kind, op.VolumeSlug, failedStep, err.Error())),
				map[string]any{"volumeSlug": op.VolumeSlug, "siteSlug": a.site, "opId": op.OpID, "kind": op.Kind, "step": failedStep, "error": err.Error(), "stopped": op.Stopped, "durationMs": dur})
			return
		}
		typ, sum, data := onOK(o)
		data["volumeSlug"], data["siteSlug"], data["opId"], data["stopped"], data["durationMs"] = op.VolumeSlug, a.site, op.OpID, op.Stopped, dur
		if op.FastForward != nil {
			data["fastForward"] = op.FastForward
			sum += " · " + fastForwardLabel(op.FastForward)
		}
		a.activity(who, "agent", typ, trim200(sum), data)
	}()
}

func trim200(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:199]) + "…"
	}
	return s
}

func hasStep(op *model.VolumeOp, name string) bool {
	for _, s := range op.Steps {
		if s.Name == name {
			return true
		}
	}
	return false
}

// snapshotVolume: a manual snapshot of only this volume (as resetVolume does).
func (a *Agent) snapshotOneVolume(ctx context.Context, t *volTarget, actor, note string) (string, int64, error) {
	if !volExists(t.ref.Dir) {
		return "", 0, fmt.Errorf("volume %s has no data yet", t.ref.Name)
	}
	id := newID()
	part, err := a.createPart(ctx, id, "manual", actor, note, a.headRevision(), t.cfg, []volRef{t.ref}, nil)
	if err != nil {
		return "", 0, err
	}
	var who *string
	if actor != "" {
		who = &actor
	}
	a.activity(who, "agent", "snapshot.created", "snapshot "+id+" (manual, "+note+")", map[string]any{"snapshotId": id, "reason": "manual", "sizeBytes": part.SizeBytes, "volumes": []string{t.ref.Name}})
	return id, part.SizeBytes, nil
}

// ───────────────────────── file engine: size, empty, copy, extract

func treeSize(root string) (int64, int64) {
	var b, n int64
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				b += info.Size()
				n++
			}
		}
		return nil
	})
	return b, n
}

// emptyVolume deletes the contents; the folder (or the file, truncated to 0) keeps its owner and mode.
func emptyVolume(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.Mode().IsRegular() {
		return os.Truncate(dir, 0)
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is neither a folder nor a file", dir)
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range es {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

type copier struct {
	preserve bool
	ctx      context.Context
	o        *opCtx
	done     int64
	files    int64
	lastPush time.Time
}

func (c *copier) tick(n int64) {
	c.done += n
	if c.o != nil && time.Since(c.lastPush) > 200*time.Millisecond {
		c.lastPush = time.Now()
		c.o.progress(c.done, c.files)
	}
}

func (c *copier) warn(w string) {
	if c.o != nil {
		c.o.warn(w)
	}
}

func (c *copier) meta(dst string, info fs.FileInfo) {
	if !c.preserve {
		return
	}
	if s, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := os.Lchown(dst, int(s.Uid), int(s.Gid)); err != nil {
			c.warn("owner not kept (the agent is not root?): " + err.Error())
		}
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		_ = os.Chmod(dst, info.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky))
		_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
	}
}

func (c *copier) copyFile(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()|0o200)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		if c.ctx.Err() != nil {
			out.Close()
			return c.ctx.Err()
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			c.tick(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	c.files++
	c.meta(dst, info)
	if c.preserve {
		if w := copyXattrs(src, dst); w != "" {
			c.warn(w)
		}
	}
	if !c.preserve {
		_ = os.Chmod(dst, info.Mode().Perm())
	}
	return nil
}

// copyTree copies src (a folder or a file) to dst (must not exist, or be an empty folder for a folder source).
// Symlinks are copied as links (never followed); sockets / devices / fifos are skipped (warning).
func (c *copier) copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.Mode().IsRegular() {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return c.copyFile(src, dst, st)
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.Remove(dst)
		if err := os.Symlink(link, dst); err != nil {
			return err
		}
		c.meta(dst, st)
		return nil
	}
	if !st.IsDir() {
		c.warn("skipped special file: " + filepath.Base(src))
		return nil
	}
	type dirMeta struct {
		dst  string
		info fs.FileInfo
	}
	var dirs []dirMeta
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			c.warn("skipped (cannot read): " + strings.TrimPrefix(p, src))
			return nil
		}
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		switch {
		case info.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, dirMeta{target, info})
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return nil
			}
			_ = os.Remove(target)
			if err := os.Symlink(link, target); err != nil {
				return err
			}
			c.meta(target, info)
		case info.Mode().IsRegular():
			return c.copyFile(p, target, info)
		default:
			c.warn("skipped special file: " + rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// folder modes / owners / times last (a read-only folder must still receive its files)
	for i := len(dirs) - 1; i >= 0; i-- {
		c.meta(dirs[i].dst, dirs[i].info)
		if c.preserve {
			if w := copyXattrs(filepath.Join(src, strings.TrimPrefix(dirs[i].dst, dst)), dirs[i].dst); w != "" {
				c.warn(w)
			}
		}
		if !c.preserve {
			_ = os.Chmod(dirs[i].dst, dirs[i].info.Mode().Perm())
		}
	}
	if c.o != nil {
		c.o.progress(c.done, c.files)
	}
	return nil
}

// copyInto copies the CONTENTS of src into the existing folder (or file) dst (restore / copy into a volume).
func (c *copier) copyInto(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	dt, err := os.Lstat(dst)
	if err != nil {
		return err
	}
	if st.Mode().IsRegular() {
		if !dt.Mode().IsRegular() {
			return fmt.Errorf("a file cannot be restored into a folder volume")
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, in)
		c.tick(n)
		c.files++
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	}
	if !dt.IsDir() {
		return fmt.Errorf("a folder cannot be restored into a file volume")
	}
	es, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range es {
		if err := c.copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// tarSafe checks one archive entry name: relative, no "..", no NUL.
func tarSafe(name string) (string, bool) {
	n := strings.TrimPrefix(filepath.ToSlash(name), "./")
	if n == "" || n == "." {
		return "", true
	}
	if strings.HasPrefix(n, "/") || strings.ContainsRune(n, 0) {
		return "", false
	}
	for _, seg := range strings.Split(strings.TrimSuffix(n, "/"), "/") {
		if seg == ".." {
			return "", false
		}
	}
	return strings.TrimSuffix(n, "/"), true
}

// scanArchive validates a .tar.gz (names, types) and counts entries + the regular files.
func scanArchive(p string) (entries, files int, total int64, err error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("not a .tar.gz: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, 0, fmt.Errorf("broken archive: %w", err)
		}
		if _, ok := tarSafe(h.Name); !ok {
			return 0, 0, 0, fmt.Errorf("unsafe path in the archive: %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeDir, tar.TypeSymlink:
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		default:
			return 0, 0, 0, fmt.Errorf("the archive contains %q of an unsupported type (only files, folders and symlinks)", h.Name)
		}
		entries++
		if h.Typeflag == tar.TypeReg {
			files++
			total += h.Size
		}
	}
	return entries, files, total, nil
}

// extractInto extracts a checked .tar.gz into dst (a folder volume) — never through a symlink; or, for a file volume,
// the single regular file into dst.
func (c *copier) extractInto(archive, dst string, fileVolume bool) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	wrote := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		name, ok := tarSafe(h.Name)
		if !ok {
			return fmt.Errorf("unsafe path %q", h.Name)
		}
		if fileVolume {
			if h.Typeflag != tar.TypeReg {
				continue
			}
			if wrote {
				return fmt.Errorf("a file volume takes an archive with exactly one file")
			}
			out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				return err
			}
			n, err := io.Copy(out, tr)
			out.Close()
			c.tick(n)
			c.files++
			if err != nil {
				return err
			}
			wrote = true
			continue
		}
		if name == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		if err := noLinkOnPath(dst, target); err != nil {
			return err
		}
		mode := fs.FileMode(h.Mode).Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			_ = os.Chmod(target, mode)
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o200)
			if err != nil {
				return err
			}
			n, err := io.Copy(out, tr)
			out.Close()
			c.tick(n)
			c.files++
			if err != nil {
				return err
			}
			_ = os.Chmod(target, mode)
			_ = os.Chtimes(target, h.ModTime, h.ModTime)
		default:
			continue
		}
		if c.preserve {
			_ = os.Lchown(target, h.Uid, h.Gid)
		}
	}
	if fileVolume && !wrote {
		return fmt.Errorf("the archive contains no file")
	}
	if c.o != nil {
		c.o.progress(c.done, c.files)
	}
	return nil
}

// noLinkOnPath: no existing component between root and target's parent is a symlink.
func noLinkOnPath(root, target string) error {
	rel, err := filepath.Rel(root, filepath.Dir(target))
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("path escapes the volume")
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		st, err := os.Lstat(cur)
		if err != nil {
			return nil
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("the archive writes through the symlink %s", strings.TrimPrefix(cur, root))
		}
	}
	return nil
}

// ───────────────────────── copy target paths

func copyStamp(t time.Time) string { return t.UTC().Format("20060102-150405") }

func underOrSame(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// copyTargetPath resolves + checks a "Copy to path" target (TS copyPathProblem + symlinks + every volume path).
func (a *Agent) copyTargetPath(in *string, t *volTarget) (string, *httpError) {
	bad := func(msg string) *httpError {
		return invalid("target path: "+msg, []model.ValidationIssue{{Path: "target.path", Code: "V_PATH", Message: msg, Severity: "error"}})
	}
	home := filepath.Clean(a.dataDir)
	p := ""
	if in != nil {
		p = strings.TrimSpace(*in)
	}
	if p == "" {
		p = filepath.Join(home, volumeCopiesDir, t.ref.Name+"-"+copyStamp(time.Now()))
		for i := 2; ; i++ { // two copies in the same second
			if _, err := os.Lstat(p); err != nil {
				break
			}
			p = filepath.Join(home, volumeCopiesDir, fmt.Sprintf("%s-%s-%d", t.ref.Name, copyStamp(time.Now()), i))
		}
	} else if strings.HasPrefix(p, "./") {
		p = filepath.Join(home, p[2:])
	} else if !filepath.IsAbs(p) {
		return "", bad("use an absolute path, or ./… relative to the agent home")
	}
	if strings.ContainsRune(p, 0) {
		return "", bad("invalid character")
	}
	p = filepath.Clean(p)
	real := func(x string) string {
		if r, err := filepath.EvalSymlinks(x); err == nil {
			return r
		}
		return x
	}
	homes := []string{home, real(home)}
	vols := siteVolumes(a.dataDir, &t.site.Config)
	check := func(p string) *httpError {
		if p == "/" {
			return bad("not the root folder")
		}
		for _, s := range systemDirs {
			if underOrSame(p, s) || underOrSame(p, real(s)) {
				return bad("not inside " + s)
			}
		}
		for _, h := range homes {
			copies := filepath.Join(h, volumeCopiesDir)
			if underOrSame(p, h) && !strings.HasPrefix(p, copies+"/") {
				return bad("inside the agent home only under ./" + volumeCopiesDir + "/")
			}
		}
		for _, v := range vols {
			for _, vp := range []string{filepath.Clean(v.Dir), real(v.Dir)} {
				if underOrSame(p, vp) || underOrSame(vp, p) {
					return bad("inside (or around) the volume " + v.Name)
				}
			}
		}
		return nil
	}
	if e := check(p); e != nil {
		return "", e
	}
	// resolve the symlinks of the deepest existing ancestor and check the real place too
	anc := p
	var tail []string
	for {
		if _, err := os.Lstat(anc); err == nil {
			break
		}
		tail = append([]string{filepath.Base(anc)}, tail...)
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	if r, err := filepath.EvalSymlinks(anc); err == nil {
		if rp := filepath.Join(append([]string{r}, tail...)...); rp != p {
			if e := check(rp); e != nil {
				return "", e
			}
		}
	}
	if st, err := os.Lstat(p); err == nil {
		if !st.IsDir() {
			return "", bad(p + " already exists")
		}
		if es, _ := os.ReadDir(p); len(es) > 0 {
			return "", bad(p + " already exists and is not empty")
		}
	}
	return p, nil
}

// ───────────────────────── copies index (kv)

func (a *Agent) volumeCopies() []model.VolumeCopy {
	var cs []model.VolumeCopy
	_, _ = a.db.GetKV(volumeCopiesKVKey, &cs)
	return cs
}

func (a *Agent) saveVolumeCopies(cs []model.VolumeCopy) { _ = a.db.SetKV(volumeCopiesKVKey, cs) }

func (a *Agent) findCopy(id string) *model.VolumeCopy {
	for _, c := range a.volumeCopies() {
		if c.CopyID == id {
			c := c
			return &c
		}
	}
	return nil
}

// ───────────────────────── handlers

func (a *Agent) handleListVolumeOps(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	a.volMu.Lock()
	ops := make([]model.VolumeOp, 0, len(a.volOps))
	var raw []*model.VolumeOp
	for _, op := range a.volOps {
		raw = append(raw, op)
	}
	a.volMu.Unlock()
	for _, op := range raw {
		ops = append(ops, a.opView(op))
	}
	sort.Slice(ops, func(i, j int) bool {
		if (ops[i].FinishedAt == nil) != (ops[j].FinishedAt == nil) {
			return ops[i].FinishedAt == nil
		}
		return ops[i].StartedAt > ops[j].StartedAt
	})
	if v := r.URL.Query().Get("volume"); v != "" {
		var f []model.VolumeOp
		for _, op := range ops {
			if op.VolumeSlug == v {
				f = append(f, op)
			}
		}
		ops = f
	}
	if ops == nil {
		ops = []model.VolumeOp{}
	}
	return model.ListResponse[model.VolumeOp]{Items: ops}, 200, nil
}

func (a *Agent) handleGetVolumeOp(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	a.volMu.Lock()
	op := a.volOps[r.PathValue("opId")]
	a.volMu.Unlock()
	if op == nil {
		return nil, 0, herr(model.ErrNotFound, "no such volume operation on site "+a.site, nil)
	}
	return a.opView(op), 200, nil
}

// opView: a copy of an op taken under the lock (the runner keeps changing it).
func (a *Agent) opView(op *model.VolumeOp) model.VolumeOp {
	a.volMu.Lock()
	defer a.volMu.Unlock()
	cp := *op
	cp.Steps = append([]model.VolumeOpStep{}, op.Steps...)
	cp.Stopped = append([]string{}, op.Stopped...)
	cp.Warnings = append([]string{}, op.Warnings...)
	return cp
}

// volume-empty / volume-restore intents (60 s + fast-forward + typed slug)
func (a *Agent) volumeIntentStart(kind string) apiFn {
	return func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
		slug := r.PathValue("volumeSlug")
		if _, e := a.volumeTarget(slug); e != nil {
			return nil, 0, e
		}
		if e := a.volGuard(slug); e != nil {
			return nil, 0, e
		}
		in := a.createIntent(kind, &slug, nil, getRC(r))
		in.RevID = nil
		in.Volume = &model.IntentVolume{SiteSlug: a.site, VolumeSlug: slug}
		g := model.GateCheck{Open: true, Conditions: []model.GateCondition{{Key: "idle", Ok: true, Detail: "no update and no other action on this volume is running"}}, Sites: []string{a.site}}
		return model.ApproveStartResponse{Intent: in, Gate: g}, 201, nil
	}
}

func (a *Agent) handleEmptyVolume(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.VolumeEmptyRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	slug := r.PathValue("volumeSlug")
	t, e := a.volumeTarget(slug)
	if e != nil {
		return nil, 0, e
	}
	if e := a.checkIntentWord(req.IntentID, "volume-empty", slug, rc, req.Typed, slug); e != nil {
		return nil, 0, e
	}
	if e := a.volGuard(slug); e != nil {
		return nil, 0, e
	}
	if e := a.volRateOK(rc); e != nil {
		return nil, 0, e
	}
	steps := []string{"stop"}
	if req.Snapshot {
		steps = append(steps, "snapshot")
	}
	steps = append(steps, "empty", "start")
	op := a.newVolumeOp("empty", t, rc.actor, steps)
	if !a.claim(op, slug) {
		return nil, 0, herr(model.ErrConflict, "volume "+slug+" is busy", nil)
	}
	op.FastForward = a.markIntentUsed(req.IntentID, rc.actor)
	var before int64
	a.runVolumeOp(op, []string{slug}, func(o *opCtx) (string, error) {
		before, _ = treeSize(t.ref.Dir)
		o.total(before)
		o.step("stop", "running", "")
		stopped, err := a.stopUsers(a.ctx, t, t.ref.Containers)
		o.set(func() { op.Stopped = append([]string{}, stopped...) })
		if err != nil {
			o.step("stop", "failed", err.Error())
			return "stop", err
		}
		o.step("stop", "done", stoppedDetail(stopped))
		if req.Snapshot {
			o.step("snapshot", "running", "")
			if volExists(t.ref.Dir) {
				id, size, err := a.snapshotOneVolume(a.ctx, t, rc.actor, "before emptying "+slug)
				if err != nil {
					o.step("snapshot", "failed", err.Error())
					return "snapshot", fmt.Errorf("snapshot first: %w", err)
				}
				o.set(func() { op.SnapshotID = &id })
				o.step("snapshot", "done", fmt.Sprintf("snapshot %s (%s)", id, humanBytes(size)))
			} else {
				o.step("snapshot", "skipped", "the volume has no data yet")
			}
		}
		o.step("empty", "running", "")
		if err := emptyVolume(t.ref.Dir); err != nil {
			o.step("empty", "failed", err.Error())
			return "empty", err
		}
		o.progress(before, 0)
		o.step("empty", "done", humanBytes(before)+" deleted")
		res := before
		o.set(func() { op.ResultBytes = &res })
		return "", nil
	}, func(o *opCtx) (string, string, map[string]any) {
		return "volume.emptied", fmt.Sprintf("volume %s emptied on %s (%s deleted)", slug, a.site, humanBytes(before)),
			map[string]any{"snapshotId": op.SnapshotID, "bytesBefore": before}
	})
	return a.opView(op), 202, nil
}

func stoppedDetail(s []string) string {
	if len(s) == 0 {
		return "no container was running"
	}
	return "stopped " + strings.Join(s, ", ")
}

func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%.1f KiB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f MiB", float64(n)/(k*k))
	default:
		return fmt.Sprintf("%.2f GiB", float64(n)/(k*k*k))
	}
}

func (a *Agent) handleCopyVolume(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.VolumeCopyRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	slug := r.PathValue("volumeSlug")
	t, e := a.volumeTarget(slug)
	if e != nil {
		return nil, 0, e
	}
	if !volExists(t.ref.Dir) {
		return nil, 0, invalid("volume "+slug+" has no data yet", nil)
	}
	locks := []string{slug}
	var dst string
	var tv *volTarget
	opTarget := &model.VolumeOpTarget{Kind: req.Target.Kind}
	switch req.Target.Kind {
	case "path":
		p, e := a.copyTargetPath(req.Target.Path, t)
		if e != nil {
			return nil, 0, e
		}
		dst = p
		opTarget.Path = p
	case "volume":
		if req.Target.VolumeSlug == slug {
			return nil, 0, invalid("the target is the same volume", nil)
		}
		x, e := a.volumeTarget(req.Target.VolumeSlug)
		if e != nil {
			return nil, 0, e
		}
		if orDefault(x.ref.Kind, "dir") != orDefault(t.ref.Kind, "dir") {
			return nil, 0, invalid("the target volume is a "+orDefault(x.ref.Kind, "dir")+", the source a "+orDefault(t.ref.Kind, "dir"), nil)
		}
		if b, n := treeSize(x.ref.Dir); (b > 0 || n > 0 || nonEmptyDir(x.ref.Dir)) && (req.Target.Overwrite == nil || *req.Target.Overwrite != x.ref.Name) {
			return nil, 0, herr(model.ErrConflict, "volume "+x.ref.Name+" is not empty: type its name to overwrite it", map[string]any{"sizeBytes": b, "files": n})
		}
		tv = x
		dst = x.ref.Dir
		opTarget.Path, opTarget.VolumeSlug = dst, &x.ref.Name
		locks = append(locks, x.ref.Name)
	default:
		return nil, 0, invalid(`target.kind must be "path" or "volume"`, nil)
	}
	if e := a.volGuard(locks...); e != nil {
		return nil, 0, e
	}
	if e := a.volRateOK(rc); e != nil {
		return nil, 0, e
	}
	steps := []string{"stop", "copy", "start"}
	if tv != nil {
		steps = []string{"stop", "empty", "copy", "start"}
	}
	op := a.newVolumeOp("copy", t, rc.actor, steps)
	op.Target = opTarget
	if !a.claim(op, locks...) {
		return nil, 0, herr(model.ErrConflict, "a volume is busy", nil)
	}
	copyID := ""
	var size, files int64
	a.runVolumeOp(op, locks, func(o *opCtx) (string, error) {
		total, _ := treeSize(t.ref.Dir)
		o.total(total)
		users := []string{}
		if req.StopContainers {
			users = append(users, t.ref.Containers...)
		}
		if tv != nil { // the target's data is replaced: its containers always stop
			users = append(users, tv.ref.Containers...)
		}
		o.step("stop", "running", "")
		stopped, err := a.stopUsers(a.ctx, t, users)
		o.set(func() { op.Stopped = append([]string{}, stopped...) })
		if err != nil {
			o.step("stop", "failed", err.Error())
			return "stop", err
		}
		if !req.StopContainers && tv == nil {
			o.step("stop", "skipped", "containers keep running (copy while in use)")
		} else {
			o.step("stop", "done", stoppedDetail(stopped))
		}
		c := &copier{preserve: req.Preserve, ctx: a.ctx, o: o}
		if tv != nil {
			o.step("empty", "running", "")
			if err := emptyVolume(dst); err != nil {
				o.step("empty", "failed", err.Error())
				return "empty", err
			}
			o.step("empty", "done", "")
			o.step("copy", "running", "into volume "+tv.ref.Name)
			if !volExists(dst) {
				if t.ref.Kind == "file" {
					_ = os.MkdirAll(filepath.Dir(dst), 0o755)
					f, _ := os.Create(dst)
					if f != nil {
						f.Close()
					}
				} else {
					_ = os.MkdirAll(dst, 0o755)
				}
			}
			err = c.copyInto(t.ref.Dir, dst)
		} else {
			if strings.HasPrefix(dst, a.dataDir) { // §21 #112 the free-space floor (copies into the agent home)
				if derr := a.checkDiskFor(dirSize(t.ref.Dir), "the copy of volume "+t.ref.Name); derr != nil {
					o.step("copy", "failed", derr.Error())
					return "copy", derr
				}
			}
			o.step("copy", "running", dst)
			err = c.copyTree(t.ref.Dir, dst)
			if err != nil {
				_ = os.RemoveAll(dst) // never leave a half copy behind
			}
		}
		if err != nil {
			o.step("copy", "failed", err.Error())
			return "copy", err
		}
		size, files = c.done, c.files
		o.set(func() { op.ResultBytes = &size })
		o.step("copy", "done", fmt.Sprintf("%s in %d files → %s", humanBytes(size), files, dst))
		if tv == nil {
			copyID = newID()
			cs := a.volumeCopies()
			cs = append(cs, model.VolumeCopy{CopyID: copyID, SiteSlug: a.site, VolumeSlug: slug, VolumeKind: op.VolumeKind, Path: dst,
				InCopiesDir: strings.HasPrefix(dst, filepath.Join(filepath.Clean(a.dataDir), volumeCopiesDir)+"/"), CreatedAt: nowTs(), CreatedBy: rc.actor, OpID: &op.OpID})
			a.saveVolumeCopies(cs)
			o.set(func() { op.CopyID = &copyID })
		}
		return "", nil
	}, func(o *opCtx) (string, string, map[string]any) {
		where := dst
		if tv != nil {
			where = "volume " + tv.ref.Name
		}
		return "volume.copied", fmt.Sprintf("volume %s copied to %s on %s (%s)", slug, where, a.site, humanBytes(size)),
			map[string]any{"target": opTarget, "bytes": size, "files": files, "preserve": req.Preserve, "copyId": copyID}
	})
	return a.opView(op), 202, nil
}

func nonEmptyDir(p string) bool {
	es, err := os.ReadDir(p)
	return err == nil && len(es) > 0
}

func (a *Agent) restoreUploadDir() string { return filepath.Join(a.dataDir, "tmp", "volume-restore") }

func (a *Agent) handleRestoreUpload(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	slug := r.PathValue("volumeSlug")
	if _, e := a.volumeTarget(slug); e != nil {
		return nil, 0, e
	}
	if e := a.volRateOK(rc); e != nil {
		return nil, 0, e
	}
	dir := a.restoreUploadDir()
	_ = os.MkdirAll(dir, 0o700)
	// expired uploads go first
	if es, err := os.ReadDir(dir); err == nil {
		for _, e := range es {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > restoreUploadTTL {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	id := newID()
	p := filepath.Join(dir, id+".tar.gz")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	var src io.Reader = r.Body
	if rc.body != nil || rc.bodyFile != "" {
		rd, _ := rc.reader()
		defer rd.Close()
		src = rd
	}
	n, err := io.Copy(f, io.LimitReader(src, model.VolumeRestoreUploadMax+1))
	f.Close()
	if err != nil {
		os.Remove(p)
		return nil, 0, herr(model.ErrInvalid, err.Error(), nil)
	}
	if n > model.VolumeRestoreUploadMax {
		os.Remove(p)
		return nil, 0, herr(model.ErrTooLarge, "at most 2 GiB; copy bigger data to the server and restore from a copy", nil)
	}
	entries, _, _, err := scanArchive(p)
	if err != nil {
		os.Remove(p)
		return nil, 0, invalid(err.Error(), []model.ValidationIssue{{Path: "body", Code: "V_FORMAT", Message: err.Error(), Severity: "error"}})
	}
	_ = os.WriteFile(p+".meta", []byte(slug), 0o600)
	return model.VolumeRestoreUpload{UploadID: id, SizeBytes: n, Entries: entries, ExpiresAt: tsOf(time.Now().Add(restoreUploadTTL))}, 201, nil
}

func (a *Agent) handleRestoreVolume(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.VolumeRestoreRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	slug := r.PathValue("volumeSlug")
	t, e := a.volumeTarget(slug)
	if e != nil {
		return nil, 0, e
	}
	src := &model.VolumeOpSource{Kind: req.Source.Kind}
	isFile := orDefault(t.ref.Kind, "dir") == "file"
	var snapVol *model.SnapshotVolume // §21 #112 source.kind snapshot
	switch req.Source.Kind {
	case "copy":
		c := a.findCopy(req.Source.CopyID)
		if c == nil {
			return nil, 0, herr(model.ErrNotFound, "no such copy", nil)
		}
		st, err := os.Lstat(c.Path)
		if err != nil {
			return nil, 0, herr(model.ErrNotFound, "the copy at "+c.Path+" no longer exists", nil)
		}
		if st.Mode().IsRegular() != isFile {
			return nil, 0, invalid("the copy is a "+map[bool]string{true: "file", false: "folder"}[st.Mode().IsRegular()]+", the volume a "+orDefault(t.ref.Kind, "dir"), nil)
		}
		src.Ref, src.Path = c.CopyID, c.Path
	case "upload":
		if !regexp.MustCompile(`^[0-9A-Z]{26}$`).MatchString(req.Source.UploadID) {
			return nil, 0, invalid("bad uploadId", nil)
		}
		p := filepath.Join(a.restoreUploadDir(), req.Source.UploadID+".tar.gz")
		if _, err := os.Stat(p); err != nil {
			return nil, 0, herr(model.ErrNotFound, "the upload expired; upload the archive again", nil)
		}
		src.Ref, src.Path = req.Source.UploadID, p
	case "snapshot": // §21 #112 any snapshot on this site that holds the volume
		if req.Source.SnapshotID == "" {
			return nil, 0, invalid("source.snapshotId is required", nil)
		}
		sv, e := a.snapshotVolumeOf(req.Source.SnapshotID, slug)
		if e != nil {
			return nil, 0, e
		}
		snapVol = sv
		src.Ref, src.Path = req.Source.SnapshotID, filepath.Join(a.snapDir(req.Source.SnapshotID), filepath.FromSlash(sv.Path))
	default:
		return nil, 0, invalid(`source.kind must be "copy", "upload" or "snapshot"`, nil)
	}
	if e := a.checkIntentWord(req.IntentID, "volume-restore", slug, rc, req.Typed, slug); e != nil {
		return nil, 0, e
	}
	if e := a.volGuard(slug); e != nil {
		return nil, 0, e
	}
	if e := a.volRateOK(rc); e != nil {
		return nil, 0, e
	}
	op := a.newVolumeOp("restore", t, rc.actor, []string{"stop", "snapshot", "restore", "start"})
	op.Source = src
	if !a.claim(op, slug) {
		return nil, 0, herr(model.ErrConflict, "volume "+slug+" is busy", nil)
	}
	op.FastForward = a.markIntentUsed(req.IntentID, rc.actor)
	var size, files int64
	a.runVolumeOp(op, []string{slug}, func(o *opCtx) (string, error) {
		if src.Kind == "copy" {
			b, _ := treeSize(src.Path)
			o.total(b)
		} else if src.Kind == "snapshot" {
			o.total(snapVol.SizeBytes)
		} else if _, _, tot, err := scanArchive(src.Path); err == nil {
			o.total(tot)
		}
		o.step("stop", "running", "")
		stopped, err := a.stopUsers(a.ctx, t, t.ref.Containers)
		o.set(func() { op.Stopped = append([]string{}, stopped...) })
		if err != nil {
			o.step("stop", "failed", err.Error())
			return "stop", err
		}
		o.step("stop", "done", stoppedDetail(stopped))
		o.step("snapshot", "running", "")
		if volExists(t.ref.Dir) {
			id, sz, err := a.snapshotOneVolume(a.ctx, t, rc.actor, "before restore of "+slug)
			if err != nil {
				o.step("snapshot", "failed", err.Error())
				return "snapshot", fmt.Errorf("snapshot first: %w", err)
			}
			o.set(func() { op.SnapshotID = &id })
			o.step("snapshot", "done", fmt.Sprintf("snapshot %s (%s)", id, humanBytes(sz)))
		} else {
			o.step("snapshot", "skipped", "the volume has no data yet")
		}
		o.step("restore", "running", src.Path)
		if !volExists(t.ref.Dir) {
			if isFile {
				_ = os.MkdirAll(filepath.Dir(t.ref.Dir), 0o755)
				if f, err := os.Create(t.ref.Dir); err == nil {
					f.Close()
				}
			} else {
				_ = os.MkdirAll(t.ref.Dir, 0o755)
			}
		}
		if err := emptyVolume(t.ref.Dir); err != nil {
			o.step("restore", "failed", err.Error())
			return "restore", err
		}
		c := &copier{preserve: true, ctx: a.ctx, o: o}
		switch src.Kind {
		case "copy":
			err = c.copyInto(src.Path, t.ref.Dir)
		case "snapshot": // the snapshot archive (sha256 checked first)
			if err = a.restoreVolume(src.Ref, *snapVol, t.ref.Dir); err == nil {
				c.done, c.files = treeSize(t.ref.Dir)
			}
		default:
			err = c.extractInto(src.Path, t.ref.Dir, isFile)
		}
		if err != nil {
			o.step("restore", "failed", err.Error()+(map[bool]string{true: " — the snapshot " + derefStr(op.SnapshotID) + " holds the previous data", false: ""}[op.SnapshotID != nil]))
			return "restore", err
		}
		size, files = c.done, c.files
		o.set(func() { op.ResultBytes = &size })
		o.step("restore", "done", fmt.Sprintf("%s in %d files", humanBytes(size), files))
		if src.Kind == "upload" {
			_ = os.Remove(src.Path)
			_ = os.Remove(src.Path + ".meta")
		}
		return "", nil
	}, func(o *opCtx) (string, string, map[string]any) {
		from := "copy " + src.Path
		if src.Kind == "upload" {
			from = "an uploaded archive"
		}
		return "volume.restored", fmt.Sprintf("volume %s restored from %s on %s (%s)", slug, from, a.site, humanBytes(size)),
			map[string]any{"source": src, "snapshotId": op.SnapshotID, "bytes": size, "files": files}
	})
	return a.opView(op), 202, nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *Agent) handleSnapshotVolume(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.VolumeSnapshotRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	slug := r.PathValue("volumeSlug")
	t, e := a.volumeTarget(slug)
	if e != nil {
		return nil, 0, e
	}
	if e := a.volGuard(slug); e != nil {
		return nil, 0, e
	}
	if e := a.volRateOK(rc); e != nil {
		return nil, 0, e
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > 200 {
		return nil, 0, invalid("note: at most 200 characters", nil)
	}
	if note == "" {
		note = "volume " + slug
	}
	id, size, err := a.snapshotOneVolume(r.Context(), t, rc.actor, note)
	if err != nil {
		return nil, 0, invalid(err.Error(), nil)
	}
	return model.VolumeSnapshotResponse{SnapshotID: id, SizeBytes: size}, 201, nil
}

func (a *Agent) handleListVolumeCopies(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	cs := a.volumeCopies()
	out := make([]model.VolumeCopy, 0, len(cs))
	deadline := time.Now().Add(3 * time.Second)
	for i := len(cs) - 1; i >= 0; i-- {
		c := cs[i]
		if st, err := os.Lstat(c.Path); err == nil {
			c.Exists = true
			if time.Now().Before(deadline) {
				var b, n int64
				if st.Mode().IsRegular() {
					b, n = st.Size(), 1
				} else {
					b, n = treeSize(c.Path)
				}
				c.SizeBytes, c.Files = &b, &n
			}
		}
		if v := r.URL.Query().Get("volume"); v != "" && v != c.VolumeSlug {
			continue
		}
		out = append(out, c)
	}
	return model.ListResponse[model.VolumeCopy]{Items: out}, 200, nil
}

func (a *Agent) handleDeleteVolumeCopy(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.DeleteVolumeCopyRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	id := r.PathValue("copyId")
	c := a.findCopy(id)
	if c == nil {
		return nil, 0, herr(model.ErrNotFound, "no such copy", nil)
	}
	if req.Confirm != id {
		return nil, 0, invalid("type the copy id to confirm", []model.ValidationIssue{{Path: "confirm", Code: "V_CONFIRM", Message: "must be the copy id", Severity: "error"}})
	}
	a.volMu.Lock()
	for _, op := range a.volOps {
		if op.FinishedAt == nil && op.Source != nil && op.Source.Ref == id {
			a.volMu.Unlock()
			return nil, 0, herr(model.ErrConflict, "a restore from this copy is running", nil)
		}
	}
	a.volMu.Unlock()
	var size int64
	if st, err := os.Lstat(c.Path); err == nil {
		if st.Mode().IsRegular() {
			size = st.Size()
		} else {
			size, _ = treeSize(c.Path)
		}
		if err := os.RemoveAll(c.Path); err != nil {
			return nil, 0, herr(model.ErrForbidden, "cannot delete "+c.Path+": "+err.Error(), nil)
		}
	}
	var keep []model.VolumeCopy
	for _, x := range a.volumeCopies() {
		if x.CopyID != id {
			keep = append(keep, x)
		}
	}
	a.saveVolumeCopies(keep)
	a.activity(rc.actorPtr(), "agent", "volume.copy-deleted", trim200(fmt.Sprintf("copy of volume %s deleted on %s (%s, %s)", c.VolumeSlug, a.site, c.Path, humanBytes(size))),
		actorData(rc, map[string]any{"copyId": id, "volumeSlug": c.VolumeSlug, "path": c.Path, "sizeBytes": size, "siteSlug": a.site}))
	return model.Deleted{Deleted: true}, 200, nil
}
