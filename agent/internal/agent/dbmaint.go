package agent

// §21 #112 opm.db maintenance (every 6 h): one online backup per day (VACUUM INTO, the 3 newest kept — what
// store.OpenOrRecover restores when the file breaks), prune of rows nothing reads any more (updates finished more than
// 90 days ago, terminal proposals older than 90 days and their staging rows, dead outbox items older than 30 days),
// then a WAL checkpoint. Activities are never pruned (they form the hash chain the cloud verifies) and cached cloud
// commits are kept (merges may need them while the cloud is unreachable).

import (
	"context"
	"time"

	"opm/agent/internal/model"
)

const dbBackupsKept = 3

func (a *Agent) dbMaintenance(ctx context.Context) {
	now := time.Now()
	size := pathSize(a.db.Path) + pathSize(a.db.Path+"-wal")
	if err := a.checkDiskFor(size, "the daily opm.db backup"); err == nil {
		if p, err := a.db.WriteDailyBackup(now, dbBackupsKept); err != nil {
			a.log.Warn("store", "daily opm.db backup failed", "error", err.Error())
		} else if p != "" {
			a.log.Info("store", "daily opm.db backup", "path", p, "sizeBytes", pathSize(p))
		}
	}
	if ctx.Err() != nil {
		return
	}
	n := a.pruneStore(now)
	if n > 0 {
		a.log.Info("store", "pruned old rows", "rows", n)
	}
	_, _ = a.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
}

// pruneStore deletes rows nothing reads any more; returns how many.
func (a *Agent) pruneStore(now time.Time) int64 {
	var n int64
	del := func(q string, args ...any) {
		if r, err := a.db.Exec(q, args...); err == nil {
			if c, err := r.RowsAffected(); err == nil {
				n += c
			}
		}
	}
	old := now.Add(-90 * 24 * time.Hour)
	del(`DELETE FROM updates WHERE finishedAt IS NOT NULL AND finishedAt < ?`, old.Unix())
	// terminal proposals older than 90 days (and their staging rows)
	rows, err := a.db.Query(`SELECT revId, status, createdAt FROM proposals`)
	if err == nil {
		var ids []string
		for rows.Next() {
			var id, st, created string
			if rows.Scan(&id, &st, &created) == nil && model.IsTerminal(st) && parseTs(created).Before(old) {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			del(`DELETE FROM staging WHERE revId = ?`, id)
			del(`DELETE FROM proposals WHERE revId = ?`, id)
		}
	}
	del(`DELETE FROM outbox WHERE nextAt >= ? AND createdAt < ?`, deadLetterAt, now.Add(-30*24*time.Hour).Unix()) // dead letters (sync.go)
	return n
}
