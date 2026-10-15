package store

// §21 #112 a broken opm.db never keeps the agent (and its edge) down: OpenOrRecover checks the file
// (PRAGMA quick_check) and, when it cannot be opened or is corrupt, moves it aside (opm.db.broken-<unix>) and restores
// the newest daily backup (<dataDir>/backup/daily/opm-YYYYMMDD.db, written by the agent with VACUUM INTO). The caller
// records what happened. Without a usable backup the original error is returned (as before).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DailyBackupDir is where the daily online backups of opm.db live.
func DailyBackupDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "backup", "daily")
}

// QuickCheck runs PRAGMA quick_check ("" = ok).
func (d *DB) QuickCheck() string {
	var res string
	if err := d.DB.QueryRow(`PRAGMA quick_check`).Scan(&res); err != nil {
		return err.Error()
	}
	if res == "ok" {
		return ""
	}
	return res
}

// DailyBackups lists the daily backups, newest first.
func DailyBackups(dbPath string) []string {
	m, _ := filepath.Glob(filepath.Join(DailyBackupDir(dbPath), "opm-*.db"))
	sort.Sort(sort.Reverse(sort.StringSlice(m)))
	return m
}

// WriteDailyBackup writes today's online backup (VACUUM INTO; a consistent copy while the agent runs) and keeps the
// `keep` newest. Returns the path ("" = today's exists already).
func (d *DB) WriteDailyBackup(now time.Time, keep int) (string, error) {
	dir := DailyBackupDir(d.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "opm-"+now.Format("20060102")+".db")
	if _, err := os.Stat(p); err == nil {
		return "", nil
	}
	tmp := p + ".tmp"
	_ = os.Remove(tmp)
	if _, err := d.DB.Exec(`VACUUM INTO ?`, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	for i, old := range DailyBackups(d.Path) {
		if i >= keep {
			_ = os.Remove(old)
		}
	}
	return p, nil
}

// OpenOrRecover opens the database; a file that fails to open or fails quick_check is replaced by the newest daily
// backup. restored = the backup used ("" = the file was fine).
func OpenOrRecover(path string) (db *DB, restored string, err error) {
	db, err = Open(path)
	if err == nil {
		if bad := db.QuickCheck(); bad == "" {
			return db, "", nil
		} else {
			db.Close()
			err = fmt.Errorf("%s failed its integrity check: %s", path, firstLine(bad))
		}
	}
	if _, isNewer := err.(*ErrNewerSchema); isNewer {
		return nil, "", err // a newer agent's file: never replaced
	}
	backups := DailyBackups(path)
	if len(backups) == 0 {
		return nil, "", err
	}
	broken := fmt.Sprintf("%s.broken-%d", path, time.Now().Unix())
	if rerr := os.Rename(path, broken); rerr != nil && !os.IsNotExist(rerr) {
		return nil, "", fmt.Errorf("%w (and moving it aside failed: %v)", err, rerr)
	}
	_ = os.Rename(path+"-wal", broken+"-wal")
	_ = os.Rename(path+"-shm", broken+"-shm")
	for _, b := range backups {
		if cerr := copyFile(b, path); cerr != nil {
			continue
		}
		if db2, oerr := Open(path); oerr == nil {
			if db2.QuickCheck() == "" {
				return db2, b, nil
			}
			db2.Close()
		}
		_ = os.Remove(path)
	}
	return nil, "", fmt.Errorf("%w; no daily backup could be restored (the broken file is %s)", err, broken)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
