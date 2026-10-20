package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

type applyOpts struct {
	rev         *model.Revision // nil = empty config (no head)
	restoreFrom string          // snapshot id whose volumes (this site's part) are restored
	rollback    bool            // best effort: continue on container errors
	state       *model.UpdateSiteState
}

// setContainerState records the state of one container (sid = containerSlug; slug = serviceSlug, may be "").
func (a *Agent) setContainerState(st *model.UpdateSiteState, sid, slug, action, state string, err error) {
	if st == nil {
		return
	}
	var e *string
	if err != nil {
		s := err.Error()
		e = &s
	}
	for i := range st.Containers {
		if st.Containers[i].ContainerSlug == sid {
			st.Containers[i].Action, st.Containers[i].State, st.Containers[i].Error = action, state, e
			if slug != "" {
				st.Containers[i].ServiceSlug = slug
			}
			a.setSiteState(st)
			return
		}
	}
	st.Containers = append(st.Containers, model.UpdateContainerState{ContainerSlug: sid, ServiceSlug: slug, Action: action, State: state, Error: e})
	a.setSiteState(st)
}

// applySite brings this site's containers, edge and listeners to the given revision (§14.5 step 3).
func (a *Agent) applySite(ctx context.Context, o applyOpts) error {
	var cfg *model.Config
	var cat *model.RevisionCatalog
	seq := 0
	if o.rev != nil {
		cfg, cat, seq = revConfig(o.rev), &o.rev.Catalog, o.rev.Seq
	} else {
		c := validate.EmptyConfig()
		cfg, cat = &c, emptyCatalog()
	}
	site := siteOf(cfg, a.site)
	var builts []*driver.Built
	if site != nil {
		var err error
		if builts, err = a.buildSite(o.rev, site); err != nil {
			return err
		}
	}
	byID := map[string]*driver.Built{}
	for _, b := range builts {
		byID[b.ContainerSlug] = b
	}
	a.migrateContainerNames(ctx, "apply") // §21 #88: names follow the naming mode before the plan (no recreate)
	existing, err := a.drv.Existing(ctx)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	actions := driver.Plan(builts, existing)
	// site volumes to restore (by volumeSlug, into the volume's location whatever container mounts it now); every
	// container that mounts one is recreated
	volDir := func(slug string) string {
		if site != nil {
			return driver.VolumePath(a.dataDir, &site.Config, slug)
		}
		return driver.VolumeDir(a.dataDir, slug)
	}
	var restore []model.SnapshotVolume
	if o.restoreFrom != "" {
		part, err := a.loadPart(o.restoreFrom)
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", o.restoreFrom, err)
		}
		restore = part.site.Volumes
	}
	restoring := map[string]bool{}
	for _, v := range restore {
		restoring[volDir(v.VolumeSlug)] = true
	}
	for i := range actions {
		if actions[i].Kind != driver.ActKeep || actions[i].Built == nil {
			continue
		}
		for _, d := range actions[i].Built.DataDirs {
			if restoring[d] {
				actions[i].Kind = driver.ActRecreate
			}
		}
	}
	// §21 #117 persist volumes whose committed content changes: decided (and guarded against uncommitted server
	// changes) before anything stops; the containers that mount them are recreated around the write
	restoredSlugs := map[string]bool{}
	for _, v := range restore {
		restoredSlugs[v.VolumeSlug] = true
	}
	contents, err := a.contentChanges(o, cfg, site, restoredSlugs)
	if err != nil {
		return err
	}
	if len(contents) > 0 {
		writing := map[string]bool{}
		for _, c := range contents {
			writing[c.p.root] = true
		}
		for i := range actions {
			if actions[i].Kind != driver.ActKeep || actions[i].Built == nil {
				continue
			}
			for _, d := range actions[i].Built.DataDirs {
				if writing[d] {
					actions[i].Kind = driver.ActRecreate
				}
			}
		}
	}
	svcOf := func(ac driver.Action) string {
		if ac.Built != nil {
			return ac.Built.Service
		}
		if ac.Existing != nil {
			return ac.Existing.Service
		}
		return ""
	}
	for _, ac := range actions {
		a.setContainerState(o.state, ac.ContainerSlug, svcOf(ac), ac.Kind, "pending", nil)
	}
	var errs []string
	fail := func(sid, slug, action string, err error) error { // sid = containerSlug, slug = serviceSlug
		a.setContainerState(o.state, sid, slug, action, "failed", err)
		if o.rollback {
			errs = append(errs, err.Error())
			return nil
		}
		if sid != "" {
			return withService(err, sid)
		}
		return err
	}
	// 1. stop + remove (dependents first). §21 #93: the start order = referenced containers first (explicit, implicit
	// containers.<slug>.* and literal hostname mentions), ties in the declared order; a reference cycle → declared order
	order, waits := a.startPlan(cfg, cat, site, builts)
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	var stops []driver.Action
	for _, ac := range actions {
		if (ac.Kind == driver.ActRemove || ac.Kind == driver.ActRecreate) && ac.Existing != nil {
			stops = append(stops, ac)
		}
	}
	sort.Slice(stops, func(i, j int) bool { return pos[stops[i].ContainerSlug] > pos[stops[j].ContainerSlug] })
	for _, ac := range stops {
		a.setContainerState(o.state, ac.ContainerSlug, svcOf(ac), ac.Kind, "running", nil)
		timeout := 10
		if b := byID[ac.ContainerSlug]; b != nil {
			timeout = b.Spec.StopTimeoutSec
		}
		if err := a.drv.StopRemove(ctx, ac.Existing.ID, timeout); err != nil {
			if e := fail(ac.ContainerSlug, svcOf(ac), ac.Kind, fmt.Errorf("%s: stop/remove: %w", ac.Slug, err)); e != nil {
				return e
			}
		}
		a.drv.ForgetHealth(ac.ContainerSlug)
		if ac.Kind == driver.ActRemove {
			a.setContainerState(o.state, ac.ContainerSlug, svcOf(ac), ac.Kind, "ok", nil)
		}
	}
	// 2. volume directories: a volume whose location is new takes the data of its previous location (head) or of the
	// part-1 vol/<volumeSlug>; then the snapshot volumes are restored into their locations
	if site != nil {
		if err := a.moveLegacyVolumes(&site.Config); err != nil {
			if e := fail("", "", "recreate", err); e != nil {
				return e
			}
		}
	}
	if len(restore) > 0 {
		defer a.forgetVolumeSizes()
	}
	for _, v := range restore {
		dir := volDir(v.VolumeSlug)
		if err := a.restoreVolume(o.restoreFrom, v, dir); err != nil {
			sid := ""
			if len(v.Containers) > 0 {
				sid = v.Containers[0]
			}
			if e := fail(sid, "", "recreate", fmt.Errorf("restore volume %s: %w", v.VolumeSlug, err)); e != nil {
				return e
			}
		}
	}
	// 2b. starter content (round E): new / empty volumes that were never seeded, before any container starts
	if site != nil {
		skip := map[string]bool{}
		for _, v := range restore {
			skip[v.VolumeSlug] = true
		}
		if err := a.seedVolumes(cfg, cat, site, skip); err != nil {
			if e := fail("", "", "create", err); e != nil {
				return e
			}
		}
		if err := a.applyContent(ctx, contents, nil); err != nil { // §21 #117 fetch on merge (vccommit.go)
			if e := fail("", "", "create", err); e != nil {
				return e
			}
		}
		if prev := a.headRevision(); prev != nil && o.rev != nil && prev.RevID != o.rev.RevID { // §21 #101 (vcseed.go)
			a.fillPlaceholders(revConfig(prev), &prev.Catalog, cfg, cat, site)
		}
	}
	// 3. images
	for _, b := range builts {
		if !b.Enabled {
			continue
		}
		if b.Image.IsSiteImage() { // §21 #104 / #113 site image: tag it again, pull its reference when Docker lost it
			if _, err := driver.EnsureReadable(ctx, a.dc, b.Image); err != nil {
				if e := fail(b.ContainerSlug, b.Service, "create", fmt.Errorf("site image %s:%s: %w", b.Image.ServiceSlug, b.Image.Version, err)); e != nil {
					return e
				}
			}
			continue
		}
		present, err := driver.ImagePresent(ctx, a.dc, b.Image, a.blobPathOrDefault)
		if err != nil {
			return fmt.Errorf("docker: %w", err)
		}
		if !present {
			if err := driver.LoadImage(ctx, a.dc, b.Image, a.blobPathOrDefault); err != nil {
				if e := fail(b.ContainerSlug, b.Service, "create", fmt.Errorf("image %s:%s: %w", b.Image.ServiceSlug, b.Image.Version, err)); e != nil {
					return e
				}
			}
		}
	}
	// 4. start in the start order; a container waits only for its explicit + implicit depends to be completed, every other
	// readiness wait runs in parallel (§14.5 step 3, §21 #93 — the serial wait in slug order was the root cause of a
	// container waiting 300 s for a dependency that was created after it)
	actOf := map[string]driver.Action{}
	for _, ac := range actions {
		actOf[ac.ContainerSlug] = ac
	}
	revID := ""
	if o.rev != nil {
		revID = o.rev.RevID
	}
	siteSlugs := make([]string, 0, len(builts))
	for _, b := range builts {
		siteSlugs = append(siteSlugs, b.ContainerSlug)
	}
	var steps []driver.StartStep
	for _, sid := range order {
		b := byID[sid]
		ac, ok := actOf[sid]
		if b == nil || !ok {
			continue
		}
		a.setContainerState(o.state, sid, b.Service, ac.Kind, "running", nil)
		step := driver.StartStep{Slug: sid, Wait: waits[sid], Ready: func(ctx context.Context, id string) error { return a.drv.WaitCompleted(ctx, b, id) }}
		if ac.Kind == driver.ActKeep {
			step.ID = ac.Existing.ID
			if a.drv.Ready(sid) {
				step.Ready = func(context.Context, string) error { return nil }
			}
			// §21 #110 a kept service container that is not running (stopped by the pre-update snapshot of an update
			// that failed / was stopped / was killed) is started again: a roll back or repair must never leave it down
			if b.Enabled && b.Kind != "job" {
				exID := ac.Existing.ID
				step.Start = func(ctx context.Context) (string, error) {
					cj, err := a.dc.ContainerInspect(ctx, exID)
					if err != nil || cj.State.Running || cj.State.Restarting {
						return exID, nil
					}
					if err := a.dc.ContainerStart(ctx, exID); err != nil {
						return exID, fmt.Errorf("%s: start the kept container: %w", sid, err)
					}
					step := sid
					a.log.Info("apply", "started a kept container that was not running", "container", step)
					return exID, nil
				}
				if a.drv.Ready(sid) { // its last health may be stale: check again once started
					step.Ready = func(ctx context.Context, id string) error { return a.drv.WaitCompleted(ctx, b, id) }
				}
			}
		} else {
			step.Start = func(ctx context.Context) (string, error) { return a.drv.CreateStart(ctx, b, revID, seq) }
		}
		steps = append(steps, step)
	}
	var firstErr error
	driver.RunStarts(ctx, steps, !o.rollback, func(r driver.StartResult) {
		b := byID[r.Slug]
		ac := actOf[r.Slug]
		switch {
		case r.Err == nil:
			a.setContainerState(o.state, r.Slug, b.Service, ac.Kind, "ok", nil)
		case r.Phase == "cancelled":
			a.setContainerState(o.state, r.Slug, b.Service, ac.Kind, "pending", nil)
		default:
			err := r.Err
			if r.Phase == "dependency" {
				err = fmt.Errorf("%s: not started: a dependency failed (%s)", r.Slug, strings.TrimPrefix(r.Err.Error(), driver.ErrDependencyFailed.Error()+"\n"))
			} else if !o.rollback { // diagnostics + keep the failed container BEFORE the rollback removes it (§21 #87)
				f := a.captureFailure(r.Slug, b, r.ID, err)
				if h := validate.DNSFailureHintOf(f.LogTail, siteSlugs, r.Slug); h != nil { // §21 #93 log-pattern hint
					msg := validate.DNSFailureMessage(r.Slug, h)
					f.Hint = &msg
					err = fmt.Errorf("%s (%w)", msg, err)
				}
				a.setContainerFailure(o.state, f)
			}
			if e := fail(r.Slug, b.Service, ac.Kind, err); e != nil && firstErr == nil {
				firstErr = e
			}
		}
	})
	a.checkAliases(ctx, "apply") // §21 #93: every running managed container answers on its slug (repair + log)
	if firstErr != nil {
		return firstErr
	}
	// 5. not-found pages, edge, listeners
	_ = a.writeNotFoundPages(cat)
	a.mu.Lock()
	a.monitorFn = map[string]*driver.Built{}
	for _, b := range builts {
		if b.Enabled {
			a.monitorFn[b.ContainerSlug] = b
		}
	}
	a.mu.Unlock()
	if err := a.reloadEdge(cfg, cat, seq, !o.rollback); err != nil {
		if e := fail("", "edge", "", fmt.Errorf("edge: %w", err)); e != nil {
			return e
		}
	}
	ap, alias := a.cfg.AgentPort, a.cfg.GuiAliasPort.Value
	if site != nil {
		ap, alias = site.Config.AgentPort, site.Config.AgentGuiAliasPort
	}
	if err := a.applyListeners(ap, alias, !o.rollback); err != nil {
		if e := fail("", "listeners", "", fmt.Errorf("listeners: %w", err)); e != nil {
			return e
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	a.removeOldPerContainerCopies()
	return nil
}

// removeOldPerContainerCopies deletes the round-C per-container copies (rendered/<containerSlug>/ and
// secrets/<containerSlug>/ directories; they may hold secret content) once an apply succeeded with the one-copy-per-site
// layout (every container that mounted them was recreated onto the new copies). Secrets are files now, so a directory
// under secrets/ is always an old copy.
func (a *Agent) removeOldPerContainerCopies() {
	n := 0
	if _, err := os.Stat(filepath.Join(a.dataDir, "rendered")); err == nil {
		if os.RemoveAll(filepath.Join(a.dataDir, "rendered")) == nil {
			n++
		}
	}
	if ents, err := os.ReadDir(filepath.Join(a.dataDir, "secrets")); err == nil {
		for _, e := range ents {
			if e.IsDir() && os.RemoveAll(filepath.Join(a.dataDir, "secrets", e.Name())) == nil {
				n++
			}
		}
	}
	if n > 0 {
		a.log.Info("files", "old per-container file / secret copies removed", "count", n)
	}
}

type svcErr struct {
	err error
	sid string
}

func (e *svcErr) Error() string { return e.err.Error() }
func (e *svcErr) Unwrap() error { return e.err }

func withService(err error, sid string) error { return &svcErr{err, sid} }

func siteConfigOrEmpty(s *model.Site) *model.SiteConfig {
	if s == nil {
		sc := validate.DefaultSiteConfig(nil)
		return &sc
	}
	return &s.Config
}

// preUpdateSnapshot: archive the pre-update volumes (preUpdateVolumes: backup = true only) and stop the containers the
// update recreates or removes plus every container that mounts an archived volume (they stay stopped) (§13.2, §14.5
// step 2).
func (a *Agent) preUpdateSnapshot(ctx context.Context, plan *model.UpdatePlan, st *model.UpdateSiteState) error {
	if err := a.requireLock(plan); err != nil {
		return err
	}
	if a.hasSnapshotPart(plan.PreSnapshotID) {
		return nil // idempotent
	}
	headRev := a.headRevision()
	headCfg, headCat := a.headConfig()
	var builts []*driver.Built
	prev := plan.RevisionFor(a.site)
	if ts := siteOf(&prev.Config, a.site); ts != nil {
		var err error
		if builts, err = a.buildSite(prev, ts); err != nil {
			return err
		}
	}
	existing, err := a.drv.Existing(ctx)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	actions := driver.Plan(builts, existing)
	changed := map[string]bool{}
	newDirs := map[string]bool{} // volume dirs mounted by a container of the new config that is created or recreated
	for _, ac := range actions {
		if ac.Kind == driver.ActRecreate || ac.Kind == driver.ActRemove {
			changed[ac.ContainerSlug] = true
		}
		if (ac.Kind == driver.ActRecreate || ac.Kind == driver.ActCreate) && ac.Built != nil {
			for _, d := range ac.Built.DataDirs {
				newDirs[d] = true
			}
		}
	}
	restoring := map[string]bool{}
	if plan.Kind == "rollback" && plan.TargetSnapshotID != nil {
		if part, err := a.loadPart(*plan.TargetSnapshotID); err == nil {
			for _, v := range part.site.Volumes {
				restoring[v.VolumeSlug] = true
			}
		}
	}
	var vols []volRef
	if hs := siteOf(headCfg, a.site); hs != nil {
		vols = preUpdateVolumes(siteVolumes(a.dataDir, &hs.Config), changed, newDirs, restoring)
		// a volume is archived while no container writes to it: every head container that mounts it is stopped
		for _, v := range vols {
			for _, c := range v.Containers {
				changed[c] = true
			}
		}
		// a restored volume (rollback) recreates every container that mounts it
		for _, v := range siteVolumes(a.dataDir, &hs.Config) {
			if restoring[v.Name] {
				for _, c := range v.Containers {
					changed[c] = true
				}
			}
		}
	}
	// stop (reverse depends order) and keep stopped
	order, _ := validate.ContainerOrder(siteConfigOrEmpty(siteOf(headCfg, a.site)), svcSlugs(headCat))
	for i := len(order) - 1; i >= 0; i-- {
		sid := order[i]
		if ex, ok := existing[sid]; ok && changed[sid] && ex.Running {
			a.setContainerState(st, sid, ex.Slug, "stop", "running", nil)
			if err := a.drv.Stop(ctx, ex.ID, -1); err != nil {
				a.setContainerState(st, sid, ex.Slug, "stop", "failed", err)
				return fmt.Errorf("%s: stop: %w", ex.Slug, err)
			}
			a.setContainerState(st, sid, ex.Slug, "stop", "ok", nil)
		}
	}
	for sid, ex := range existing { // containers not in the head order (e.g. disabled) that change
		if changed[sid] && ex.Running && !contains(order, sid) {
			_ = a.drv.Stop(ctx, ex.ID, -1)
		}
	}
	snap, err := a.createPart(ctx, plan.PreSnapshotID, "pre-update", plan.ApprovedBy, "before seq "+fmt.Sprint(plan.RevisionFor(a.site).Seq), headRev, headCfg, vols, nil)
	if err != nil {
		return err
	}
	a.activity(nil, "system", "update.snapshot", "pre-update snapshot "+plan.PreSnapshotID, map[string]any{"revId": plan.RevisionFor(a.site).RevID, "snapshotId": plan.PreSnapshotID, "sizeBytes": snap.SizeBytes})
	a.retention(plan.PreSnapshotID)
	return nil
}

// monitorOnce keeps checking the running containers (readiness for the edge + status page) and reloads the edge
// when a loopback port changed.
func (a *Agent) monitorOnce(ctx context.Context) {
	a.mu.Lock()
	if len(a.monitorFn) == 0 {
		a.mu.Unlock()
		a.initMonitor(ctx)
		a.mu.Lock()
	}
	builts := make([]*driver.Built, 0, len(a.monitorFn))
	for _, b := range a.monitorFn {
		builts = append(builts, b)
	}
	a.mu.Unlock()
	if len(builts) == 0 {
		return
	}
	existing, err := a.drv.Existing(ctx)
	if err != nil {
		return
	}
	reload := false
	for _, b := range builts {
		ex, ok := existing[b.ContainerSlug]
		prev := a.drv.HealthOf(b.ContainerSlug)
		if !ok {
			a.drv.SetHealth(b.ContainerSlug, driver.Health{Health: "none", LastError: "container missing", LoopPorts: map[string]int{}})
			continue
		}
		loop, cj, err := a.drv.LoopPorts(ctx, ex.ID, b)
		if err != nil {
			continue
		}
		h := driver.Health{ContainerID: ex.ID, LoopPorts: loop, Restarts: cj.RestartCount, LastCheck: time.Now()}
		switch {
		case b.Kind == "job":
			h.Ready = !cj.State.Running && cj.State.ExitCode == 0
			h.Health = "none"
		case cj.State.Restarting:
			h.Ready, h.Health, h.LastError = false, "unhealthy", fmt.Sprintf("restarting: last exit code %d, %d restarts", cj.State.ExitCode, cj.RestartCount)
		case !cj.State.Running:
			h.Ready, h.Health, h.LastError = false, "unhealthy", fmt.Sprintf("exited with code %d", cj.State.ExitCode)
		case b.Health == nil:
			h.Ready, h.Health = true, "none"
		default:
			if time.Since(prev.LastCheck) < driver.Secs(b.Health.IntervalSec) && prev.ContainerID == ex.ID && prev.Health != "starting" {
				continue
			}
			if err := a.drv.CheckOnce(ctx, b, ex.ID, loop, time.Now().Add(-time.Hour)); err != nil {
				h.Ready, h.Health, h.LastError = false, "unhealthy", err.Error()
			} else {
				h.Ready, h.Health = true, "healthy"
			}
		}
		for pn, p := range loop {
			if prev.LoopPorts[pn] != p {
				reload = true
			}
		}
		a.drv.SetHealth(b.ContainerSlug, h)
	}
	if reload {
		a.log.Info("edge", "loopback ports changed; reloading the edge")
		a.refreshEdgeUpstreams()
	}
}

// initMonitor adopts the running OPM containers of the head (agent restart).
func (a *Agent) initMonitor(ctx context.Context) {
	h := a.headRevision()
	if h == nil {
		return
	}
	site := siteOf(&h.Config, a.site)
	if site == nil {
		return
	}
	builts, err := a.buildSite(h, site)
	if err != nil {
		return
	}
	stale := []string{}
	a.mu.Lock()
	for _, b := range builts {
		if b.Enabled {
			a.monitorFn[b.ContainerSlug] = b
			if driver.SiteFilesModeStale(b) {
				stale = append(stale, b.ContainerSlug)
			}
		}
	}
	heal := len(stale) > 0 && !a.modeHealDone
	if heal {
		a.modeHealDone = true
	}
	a.mu.Unlock()
	_ = os.MkdirAll(filepath.Join(a.dataDir, "vol"), 0o755)
	if heal {
		// a container kept by an older agent with a file of the wrong mode (only the mode changed: 0400 → 0444, nginx
		// 403): re-apply the head once — the containers concerned are recreated (driver.Keeps), the files rewritten
		a.goLoop("file-mode-heal", func(c context.Context) {
			run, he := a.reapplyHead("")
			if he != nil {
				a.log.Warn("apply", "file mode heal: re-apply failed", "containers", strings.Join(stale, ","), "err", he.Error())
				return
			}
			keys := make([]string, 0, len(stale))
			for _, s := range stale {
				keys = append(keys, "sites/"+a.site+"/containers/"+s)
			}
			a.activity(nil, "system", "drift.discarded", "re-applied the head: "+strings.Join(stale, ", ")+" mounted a file with an outdated mode", map[string]any{"keys": keys, "updateId": run.UpdateID})
		})
	}
}
