package agent

// §21 #112 per-volume automatic backups. A site volume may carry backupPolicy { schedule, keep }:
//   - absent  → today's behaviour: the volume is in the daily scheduled site snapshot (its 7 newest kept);
//   - present → the volume leaves the scheduled site snapshot and gets its own snapshots (reason "volume-auto") on
//     that schedule; the `keep` newest of them are kept (never deleted by the size budget either).
// hourly / 6h backups are taken live (the containers keep running, like "Snapshot now"); daily / weekly stop the
// volume's containers around the archive, like the scheduled site snapshot. Every snapshot of the site that holds the
// volume (automatic, scheduled, pre-update, manual) is listed per volume and can restore the volume
// (restoreVolume source.kind = "snapshot": stop its containers, snapshot the current data first, restore, start).

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/model"
)

const volumeAutoReason = "volume-auto"

// volumeAutoNote is the note of an automatic volume backup (the volume it belongs to).
func volumeAutoNote(slug string) string { return "auto " + slug }

// volumeBackups lists this site's complete snapshots that hold the volume, newest first.
func (a *Agent) volumeBackups(slug string) []model.VolumeBackup {
	out := []model.VolumeBackup{}
	for _, s := range a.snapRows() { // newest first
		if !s.Complete {
			continue
		}
		part, ok := s.Sites[a.site]
		if !ok {
			continue
		}
		for _, v := range part.Volumes {
			if v.VolumeSlug != slug {
				continue
			}
			out = append(out, model.VolumeBackup{SnapshotID: s.SnapshotID, CreatedAt: s.CreatedAt, Reason: s.Reason, Note: s.Note,
				CreatedBy: s.CreatedBy, SizeBytes: v.SizeBytes, Auto: s.Reason == volumeAutoReason && s.Note == volumeAutoNote(slug)})
		}
	}
	return out
}

// lastAutoBackup: when the volume's own schedule last took a backup (zero = never).
func (a *Agent) lastAutoBackup(slug string) time.Time {
	for _, b := range a.volumeBackups(slug) {
		if b.Auto {
			return parseTs(b.CreatedAt)
		}
	}
	return time.Time{}
}

func (a *Agent) handleVolumeBackups(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	slug := r.PathValue("volumeSlug")
	t, e := a.volumeTarget(slug)
	if e != nil {
		return nil, 0, e
	}
	resp := model.VolumeBackupsResponse{VolumeSlug: slug, Policy: t.decl.BackupPolicyOf(), Explicit: t.decl.BackupPolicy != nil, Items: a.volumeBackups(slug)}
	if p := resp.Policy; resp.Explicit && p.Schedule != "off" && t.decl.Backup {
		next := a.lastAutoBackup(slug).Add(time.Duration(model.VolumeBackupSchedules[p.Schedule]) * time.Second)
		if next.Before(time.Now()) {
			next = time.Now()
		}
		ts := tsOf(next)
		resp.NextAt = &ts
	} else if !resp.Explicit && t.decl.Backup {
		ts := tsOf(nextScheduledAt(a.clock(), a.tm.ScheduledAtHour))
		resp.NextAt = &ts
	}
	return resp, 200, nil
}

// explicitBackupVolumes: the volumes of this site's head config with their own policy (they leave the scheduled
// site snapshot).
func explicitBackupVolumes(site *model.Site) map[string]bool {
	out := map[string]bool{}
	if site == nil || site.Config.Volumes == nil {
		return out
	}
	for _, v := range *site.Config.Volumes {
		if v.BackupPolicy != nil {
			out[v.VolumeSlug] = true
		}
	}
	return out
}

// volumeBackupLoop checks every 5 minutes which volumes are due for an automatic backup.
func (a *Agent) volumeBackupLoop(ctx context.Context) {
	cfg, _ := a.headConfig()
	site := siteOf(cfg, a.site)
	if site == nil || site.Config.Volumes == nil || a.lock() != nil {
		return
	}
	now := a.clock()
	for _, v := range *site.Config.Volumes {
		if ctx.Err() != nil {
			return
		}
		if v.BackupPolicy == nil || !v.Backup {
			continue
		}
		p := *v.BackupPolicy
		every := time.Duration(model.VolumeBackupSchedules[p.Schedule]) * time.Second
		if every <= 0 {
			continue
		}
		if last := a.lastAutoBackup(v.VolumeSlug); !last.IsZero() && now.Sub(last) < every {
			continue
		}
		if _, err := a.autoBackupVolume(ctx, v.VolumeSlug, p); err != nil {
			a.log.Warn("snapshot", "automatic volume backup not taken", "volumeSlug", v.VolumeSlug, "error", err.Error())
		}
	}
}

// autoBackupVolume takes one automatic backup of a volume and applies its keep.
func (a *Agent) autoBackupVolume(ctx context.Context, slug string, p model.VolumeBackupPolicy) (string, error) {
	t, e := a.volumeTarget(slug)
	if e != nil {
		return "", e
	}
	if !volExists(t.ref.Dir) {
		return "", nil // nothing to back up yet
	}
	if e := a.volGuard(slug); e != nil {
		return "", e // another action on the volume runs: next round
	}
	var around func(v volRef) func()
	if p.Schedule == "daily" || p.Schedule == "weekly" {
		ex, _ := a.drv.Existing(ctx)
		around = func(v volRef) func() {
			var stopped []string
			for _, cs := range v.Containers {
				if x, ok := ex[cs]; ok && x.Running {
					_ = a.drv.Stop(ctx, x.ID, -1)
					stopped = append(stopped, x.ID)
				}
			}
			return func() {
				for _, id := range stopped {
					if err := a.dc.ContainerStart(context.Background(), id); err != nil {
						a.log.Error("snapshot", "restart after volume backup failed", "volumeSlug", slug, "error", err.Error())
					}
				}
			}
		}
	}
	id := newID()
	part, err := a.createPart(ctx, id, volumeAutoReason, "system", volumeAutoNote(slug), a.headRevision(), t.cfg, []volRef{t.ref}, around)
	if err != nil {
		return "", err
	}
	a.activity(nil, "system", "snapshot.created", "automatic backup of volume "+slug+" ("+p.Schedule+")", map[string]any{"snapshotId": id, "reason": volumeAutoReason, "sizeBytes": part.SizeBytes, "volumes": []string{slug}})
	a.pruneVolumeAuto(slug, p.Keep)
	a.retentionBySize(id)
	return id, nil
}

// pruneVolumeAuto keeps the `keep` newest automatic backups of a volume.
func (a *Agent) pruneVolumeAuto(slug string, keep int) {
	if keep < 1 {
		keep = 1
	}
	n := 0
	for _, b := range a.volumeBackups(slug) {
		if !b.Auto {
			continue
		}
		n++
		if n > keep {
			if e := a.deleteOwnSnapshot(b.SnapshotID, nil); e == nil {
				a.log.Info("snapshot", "volume backup retention removed snapshot", "snapshotId", b.SnapshotID, "volumeSlug", slug)
			}
		}
	}
}

// protectedAutoBackups: the `keep` newest automatic backups of every volume with a policy (never deleted by the
// size budget).
func (a *Agent) protectedAutoBackups() map[string]bool {
	out := map[string]bool{}
	cfg, _ := a.headConfig()
	site := siteOf(cfg, a.site)
	if site == nil || site.Config.Volumes == nil {
		return out
	}
	for _, v := range *site.Config.Volumes {
		if v.BackupPolicy == nil {
			continue
		}
		n := 0
		for _, b := range a.volumeBackups(v.VolumeSlug) {
			if b.Auto && n < v.BackupPolicy.Keep {
				out[b.SnapshotID] = true
				n++
			}
		}
	}
	return out
}

// snapshotVolumeOf finds the archive of a volume in this site's part of a snapshot.
func (a *Agent) snapshotVolumeOf(snapshotID, slug string) (*model.SnapshotVolume, *httpError) {
	pi, err := a.loadPart(snapshotID)
	if err != nil {
		return nil, herr(model.ErrNotFound, err.Error(), nil)
	}
	for _, v := range pi.site.Volumes {
		if v.VolumeSlug == slug {
			cp := v
			return &cp, nil
		}
	}
	names := []string{}
	for _, v := range pi.site.Volumes {
		names = append(names, v.VolumeSlug)
	}
	sort.Strings(names)
	return nil, herr(model.ErrNotFound, "snapshot "+snapshotID+" has no archive of volume "+slug+" (it holds: "+strings.Join(names, ", ")+")", nil)
}
