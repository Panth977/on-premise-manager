package agent

// §21 #112 disk guards. The agent's own files (snapshots, blobs, volume copies, transfers, opm.db) share the
// filesystem with the live volumes of the apps, so:
//   - a free-space FLOOR (min(5 % of the filesystem, 2 GiB)) is kept: a new snapshot / volume copy / blob fetch that
//     would go below it is refused with a clear error, an activity and a status warning — the apps' own writes must
//     never be the ones that fail;
//   - snapshots stay within a SIZE BUDGET (agent.json snapshotBudgetPct, default 30 % of the filesystem) on top of the
//     count retention: the oldest scheduled, then pre-update, then automatic volume backups are removed first. Never
//     removed: the newest snapshot of each reason, the pre-update snapshot of a running update, the `keep` newest
//     automatic backups of every volume, manual and imported snapshots. When protected snapshots alone exceed the
//     budget, status warns instead of deleting them.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"opm/agent/internal/model"
)

// DiskFloorFor: the free space the agent never uses (min(5 % of total, 2 GiB)).
func DiskFloorFor(total int64) int64 {
	f := total / 20
	if f > 2<<30 {
		f = 2 << 30
	}
	return f
}

type diskWarnings struct {
	mu       sync.Mutex
	floor    *diskWarn // the last refusal (below the floor)
	budget   *diskWarn // protected snapshots exceed the budget
	lastActs map[string]time.Time
}

type diskWarn struct {
	At      time.Time
	Message string
}

var diskWarnState = &diskWarnings{lastActs: map[string]time.Time{}}

func (a *Agent) diskTotals() (total, free int64) {
	t, f := diskOf(a.dataDir)
	return int64(t), int64(f)
}

// checkDiskFor refuses to write need bytes for `what` when it would go below the floor.
func (a *Agent) checkDiskFor(need int64, what string) error {
	total, free := a.diskTotals()
	if total <= 0 {
		return nil
	}
	floor := DiskFloorFor(total)
	if free-need >= floor {
		return nil
	}
	msg := fmt.Sprintf("not enough free disk for %s: %s free, it needs %s and the agent always leaves %s free for the apps", what, humanBytes(free), humanBytes(need), humanBytes(floor))
	diskWarnState.mu.Lock()
	diskWarnState.floor = &diskWarn{At: time.Now(), Message: msg}
	last := diskWarnState.lastActs["floor"]
	if time.Since(last) > time.Hour {
		diskWarnState.lastActs["floor"] = time.Now()
	}
	diskWarnState.mu.Unlock()
	if time.Since(last) > time.Hour {
		a.activity(nil, "system", "disk.low", msg, map[string]any{"freeBytes": free, "needBytes": need, "floorBytes": floor, "what": what})
	}
	a.log.Warn("disk", "write refused: below the free-space floor", "what", what, "freeBytes", free, "needBytes", need, "floorBytes", floor)
	return fmt.Errorf("%s", msg)
}

// snapshotBudget: agent.json snapshotBudgetPct (1 … 90, default 30) of the agent-home filesystem.
func (a *Agent) snapshotBudget() int64 {
	total, _ := a.diskTotals()
	pct := 30
	if p := a.cfg.SnapshotBudgetPct; p != nil && *p >= 1 && *p <= 90 {
		pct = *p
	}
	return total * int64(pct) / 100
}

// retentionBySize removes the oldest unprotected snapshots while the snapshots exceed the budget.
func (a *Agent) retentionBySize(justTaken string) {
	budget := a.snapshotBudget()
	if budget <= 0 {
		return
	}
	rows := a.snapRows() // newest first
	var used int64
	for _, s := range rows {
		used += s.SizeBytes
	}
	if used <= budget {
		a.setBudgetWarn("")
		return
	}
	protect := a.protectedAutoBackups()
	protect[justTaken] = true
	newest := map[string]bool{}
	for _, s := range rows {
		if !newest[s.Reason] {
			newest[s.Reason] = true
			protect[s.SnapshotID] = true // the newest of each reason (the newest pre-change snapshot included)
		}
	}
	// candidates: oldest first, scheduled before pre-update before automatic volume backups; manual / imported never
	order := map[string]int{"scheduled": 0, "pre-update": 1, volumeAutoReason: 2}
	var cands []snapRow
	for _, s := range rows {
		if _, ok := order[s.Reason]; ok && !protect[s.SnapshotID] && !a.protectedSnapshot(s.SnapshotID) {
			cands = append(cands, s)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if order[cands[i].Reason] != order[cands[j].Reason] {
			return order[cands[i].Reason] < order[cands[j].Reason]
		}
		return cands[i].CreatedAt < cands[j].CreatedAt
	})
	for _, s := range cands {
		if used <= budget {
			break
		}
		if e := a.deleteOwnSnapshot(s.SnapshotID, nil); e == nil {
			used -= s.SizeBytes
			a.log.Info("snapshot", "size budget removed snapshot", "snapshotId", s.SnapshotID, "reason", s.Reason, "sizeBytes", s.SizeBytes)
		}
	}
	if used > budget {
		a.setBudgetWarn(fmt.Sprintf("snapshots use %s, over the budget of %s: the rest are protected (newest of each kind, automatic backups a volume keeps, manual and imported) — delete some under Backups or lower a volume's keep", humanBytes(used), humanBytes(budget)))
		return
	}
	a.setBudgetWarn("")
}

func (a *Agent) setBudgetWarn(msg string) {
	diskWarnState.mu.Lock()
	defer diskWarnState.mu.Unlock()
	if msg == "" {
		diskWarnState.budget = nil
		return
	}
	diskWarnState.budget = &diskWarn{At: time.Now(), Message: msg}
}

// diskWarningTexts: the current disk warnings (status, self-metrics). A floor refusal shows for an hour.
func (a *Agent) diskWarningTexts() []string {
	diskWarnState.mu.Lock()
	defer diskWarnState.mu.Unlock()
	var out []string
	if w := diskWarnState.floor; w != nil && time.Since(w.At) < time.Hour {
		out = append(out, w.Message)
	}
	if w := diskWarnState.budget; w != nil {
		out = append(out, w.Message)
	}
	return out
}

// pathSize sums the regular files under p (a file: its size; missing: 0).
func pathSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	if !st.IsDir() {
		return st.Size()
	}
	return dirSize(p)
}

// ownDiskUse: the agent's own files (self-metrics).
func (a *Agent) ownDiskUse() map[string]int64 {
	total, free := a.diskTotals()
	db := pathSize(filepath.Join(a.dataDir, "opm.db")) + pathSize(filepath.Join(a.dataDir, "opm.db-wal"))
	return map[string]int64{
		"opmDbBytes":          db,
		"metricsDbBytes":      pathSize(filepath.Join(a.dataDir, "metrics.db")) + pathSize(filepath.Join(a.dataDir, "metrics.db-wal")),
		"blobsBytes":          pathSize(filepath.Join(a.dataDir, "blobs")),
		"snapshotsBytes":      pathSize(filepath.Join(a.dataDir, "snapshots")),
		"snapshotBudgetBytes": a.snapshotBudget(),
		"logsBytes":           pathSize(filepath.Join(a.dataDir, "logs")),
		"freeBytes":           free,
		"totalBytes":          total,
	}
}

var _ = model.ErrConflict
