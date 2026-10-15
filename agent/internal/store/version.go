package store

import (
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// SQLite schema versioning (SPEC-final §21 #91): opm.db PRAGMA user_version. SchemaVersion is the schema this build
// migrates to; MinSchema the oldest DB it opens (0 = unversioned DBs of agents before 0.10.0, which count as
// AGENT_LEGACY_SCHEMA 1 for the install script). Before a migration the DB files are copied to
// <dataDir>/backup/<previous agent version or "schema-N">-<yyyymmddThhmmssZ>/ (the 5 newest kept).
const (
	SchemaVersion = 2 // §21 #104 (0.14): commits, cloud_commits, working_items, drift, drift_accepts, merge_bases, merge_runs
	MinSchema     = 0
	// KeepBackups is how many DB backups stay in <dataDir>/backup.
	KeepBackups = 5
)

// ErrNewerSchema: the DB was written by a newer agent.
type ErrNewerSchema struct {
	Path     string
	DBSchema int
}

func (e *ErrNewerSchema) Error() string {
	return fmt.Sprintf("%s has schema %d, newer than this agent's schema %d: run the newer agent again (rerun the install command), or restore a database backup from %s",
		e.Path, e.DBSchema, SchemaVersion, filepath.Join(filepath.Dir(e.Path), "backup"))
}

var labelRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// inspect reads user_version, whether the DB has tables, and kv lastAgentVersion (best effort).
func inspect(path string) (version int, hasTables bool, lastAgent string, err error) {
	if _, serr := os.Stat(path); serr != nil {
		return 0, false, "", nil
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return 0, false, "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, false, "", err
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&n)
	hasTables = n > 0
	var s string
	if db.QueryRow(`SELECT value FROM kv WHERE key = 'lastAgentVersion'`).Scan(&s) == nil {
		lastAgent = labelRE.ReplaceAllString(trimQuotes(s), "_")
	}
	return version, hasTables, lastAgent, nil
}

func trimQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// BackupDB copies <path>(-wal, -shm) into <dir>/backup/<label>-<ts>/ and keeps the newest KeepBackups backups.
func BackupDB(path, label string) (string, error) {
	root := filepath.Join(filepath.Dir(path), "backup")
	dst := filepath.Join(root, label+"-"+time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return "", err
	}
	for _, suf := range []string{"", "-wal", "-shm"} {
		src := path + suf
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, filepath.Join(dst, filepath.Base(src))); err != nil {
			return "", err
		}
	}
	PruneBackups(root, KeepBackups)
	return dst, nil
}

// PruneBackups keeps the newest keep directories of root (by modification time).
func PruneBackups(root string, keep int) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type d struct {
		p string
		t time.Time
	}
	var ds []d
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			ds = append(ds, d{filepath.Join(root, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].t.After(ds[j].t) })
	for i := keep; i < len(ds); i++ {
		_ = os.RemoveAll(ds[i].p)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
