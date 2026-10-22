// Package store opens the agent SQLite database (<dataDir>/opm.db, WAL, pure-Go driver modernc.org/sqlite) and
// creates the tables of SPEC-final §2.5 (+ an outbox for queued cloud reports). Times are unix seconds unless a
// column is a TEXT ts; JSON columns hold canonical-or-plain JSON text.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB. One connection (SQLite serialises writers anyway); callers must not hold a *sql.Rows open while
// issuing another query.
type DB struct {
	*sql.DB
	Path string
	// OnError is called with every failed Exec (§21 #112: a full disk or a broken file is reported, never only
	// swallowed by the many `_, _ = db.Exec` call sites)
	OnError func(error)
}

// Exec runs a statement; a failure also goes to OnError.
func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	r, err := d.DB.Exec(query, args...)
	if err != nil && d.OnError != nil {
		d.OnError(err)
	}
	return r, err
}

const schema = `
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS revisions (
  revId TEXT PRIMARY KEY, seq INTEGER NOT NULL, parentRevId TEXT, origin TEXT NOT NULL, actor TEXT NOT NULL,
  createdAt TEXT NOT NULL, config TEXT NOT NULL, hash TEXT NOT NULL, catalog TEXT NOT NULL, catalogHash TEXT NOT NULL,
  summary TEXT NOT NULL, kind TEXT NOT NULL, rollbackOf TEXT);
CREATE INDEX IF NOT EXISTS revisions_seq ON revisions(seq);
CREATE TABLE IF NOT EXISTS proposals (revId TEXT PRIMARY KEY, status TEXT NOT NULL, createdAt TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS staging (
  revId TEXT NOT NULL, siteSlug TEXT NOT NULL, staging TEXT NOT NULL, progress REAL NOT NULL, error TEXT,
  detail TEXT, updatedAt INTEGER NOT NULL, PRIMARY KEY (revId, siteSlug));
CREATE TABLE IF NOT EXISTS blobs (
  sha256 TEXT PRIMARY KEY, sizeBytes INTEGER NOT NULL, kind TEXT NOT NULL, source TEXT NOT NULL,
  haveSince INTEGER NOT NULL, path TEXT NOT NULL, lastUsedAt INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS snapshots (
  snapshotId TEXT PRIMARY KEY, createdAt TEXT NOT NULL, reason TEXT NOT NULL, revId TEXT, seq INTEGER NOT NULL,
  manifest TEXT NOT NULL, sizeBytes INTEGER NOT NULL, createdBy TEXT NOT NULL, note TEXT NOT NULL, complete INTEGER NOT NULL,
  error TEXT);
CREATE TABLE IF NOT EXISTS activities (
  seq INTEGER PRIMARY KEY, activityId TEXT NOT NULL, ts TEXT NOT NULL, actor TEXT, origin TEXT NOT NULL, type TEXT NOT NULL,
  summary TEXT NOT NULL, data TEXT NOT NULL, prevHash TEXT, hash TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS activities_ts ON activities(ts);
CREATE TABLE IF NOT EXISTS actors (email TEXT PRIMARY KEY, lastUsedAt TEXT NOT NULL, count INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (
  sessionId TEXT PRIMARY KEY, createdAt INTEGER NOT NULL, expiresAt INTEGER NOT NULL, keyVersion INTEGER NOT NULL,
  ip TEXT NOT NULL, userAgent TEXT NOT NULL, loggedOutAt INTEGER);
CREATE TABLE IF NOT EXISTS intents (
  intentId TEXT PRIMARY KEY, kind TEXT NOT NULL, revId TEXT, snapshotId TEXT, actor TEXT NOT NULL, sessionId TEXT NOT NULL,
  notBefore INTEGER NOT NULL, expiresAt INTEGER NOT NULL, usedAt INTEGER);
CREATE TABLE IF NOT EXISTS intent_waits (intentId TEXT PRIMARY KEY, state TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS updates (
  updateId TEXT PRIMARY KEY, plan TEXT NOT NULL, run TEXT, site TEXT, startedAt INTEGER NOT NULL, finishedAt INTEGER);
CREATE TABLE IF NOT EXISTS peers (
  siteSlug TEXT PRIMARY KEY, publicKey TEXT NOT NULL, advertiseHost TEXT NOT NULL, agentPort INTEGER NOT NULL,
  enrolledAt TEXT NOT NULL, lastAnswerAt INTEGER, rttMs REAL, reachable INTEGER NOT NULL DEFAULT 0,
  hasInternet INTEGER NOT NULL DEFAULT 0, lastPing TEXT);
CREATE TABLE IF NOT EXISTS cloud_view (id INTEGER PRIMARY KEY, fetchedAt TEXT NOT NULL, source TEXT NOT NULL, cursor TEXT NOT NULL, view TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS transfers (transferId TEXT PRIMARY KEY, direction TEXT NOT NULL, status TEXT NOT NULL, createdAt TEXT NOT NULL, data TEXT NOT NULL, localPath TEXT);
CREATE TABLE IF NOT EXISTS files (fileId TEXT PRIMARY KEY, data TEXT NOT NULL, localPath TEXT, listedBy TEXT NOT NULL, updatedAt INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS tunnel_leases (leaseId TEXT PRIMARY KEY, status TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS uptime_raw (
  ts INTEGER NOT NULL, subject TEXT NOT NULL, state TEXT NOT NULL, cpu REAL, memMb REAL, restarts INTEGER,
  PRIMARY KEY (ts, subject));
CREATE TABLE IF NOT EXISTS uptime_hourly (
  hour INTEGER NOT NULL, subject TEXT NOT NULL, upFraction REAL NOT NULL, downSeconds INTEGER NOT NULL, samples INTEGER NOT NULL,
  PRIMARY KEY (hour, subject));
CREATE TABLE IF NOT EXISTS debug_index (day TEXT PRIMARY KEY, file TEXT NOT NULL, sizeBytes INTEGER NOT NULL, firstTs TEXT, lastTs TEXT, lines INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS login_limits (ip TEXT PRIMARY KEY, fails INTEGER NOT NULL, windowStart INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS nonces (key TEXT PRIMARY KEY, expiresAt INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS volume_snapshots (snapshotId TEXT PRIMARY KEY, volumeSlug TEXT NOT NULL, takenAt TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS volume_snapshots_vol ON volume_snapshots(volumeSlug, takenAt);
CREATE TABLE IF NOT EXISTS outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, dedupe TEXT, method TEXT NOT NULL, path TEXT NOT NULL,
  body BLOB, createdAt INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, nextAt INTEGER NOT NULL, lastError TEXT);
CREATE INDEX IF NOT EXISTS outbox_dedupe ON outbox(dedupe);
-- §21 #104 branches + commits (schema 2)
CREATE TABLE IF NOT EXISTS commits (
  commitId TEXT PRIMARY KEY, seq INTEGER NOT NULL UNIQUE, parentId TEXT, kind TEXT NOT NULL, message TEXT NOT NULL,
  author TEXT NOT NULL, createdAt TEXT NOT NULL, source TEXT, revertOf TEXT, resetTo TEXT, rollbackOf TEXT,
  config TEXT NOT NULL, hash TEXT NOT NULL, catalog TEXT, catalogHash TEXT, stats TEXT NOT NULL, data TEXT NOT NULL, pushedAt TEXT);
CREATE TABLE IF NOT EXISTS cloud_commits (
  commitId TEXT PRIMARY KEY, seq INTEGER NOT NULL, parentId TEXT, kind TEXT NOT NULL, message TEXT NOT NULL, author TEXT NOT NULL,
  createdAt TEXT NOT NULL, source TEXT, config TEXT, hash TEXT NOT NULL, catalog TEXT, catalogHash TEXT, data TEXT NOT NULL,
  fetchedAt TEXT, via TEXT);
CREATE TABLE IF NOT EXISTS working_items (key TEXT PRIMARY KEY, has INTEGER NOT NULL, value TEXT, updatedAt TEXT NOT NULL, updatedBy TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS drift (id INTEGER PRIMARY KEY, scannedAt TEXT NOT NULL, headCommitId TEXT, report TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS drift_accepts (fingerprint TEXT PRIMARY KEY, commitId TEXT, acceptedAt TEXT NOT NULL, acceptedBy TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS merge_bases (
  sourceKey TEXT PRIMARY KEY, sourceCommitId TEXT, sourceSeq INTEGER, targetCommitId TEXT, base TEXT NOT NULL, skipped TEXT NOT NULL,
  updatedAt TEXT NOT NULL, updatedBy TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS merge_runs (runId TEXT PRIMARY KEY, plan TEXT NOT NULL, state TEXT NOT NULL, startedAt TEXT NOT NULL, finishedAt TEXT, error TEXT);
`

// Open opens (and creates) the database file.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// §21 #91 schema versioning: refuse a newer DB; back up an older one before migrating it
	ver, hasTables, lastAgent, err := inspect(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if ver > SchemaVersion {
		return nil, &ErrNewerSchema{Path: path, DBSchema: ver}
	}
	if ver < MinSchema {
		return nil, fmt.Errorf("%s has schema %d, older than this agent opens (%d): restore a newer backup", path, ver, MinSchema)
	}
	migrate := ver < SchemaVersion
	if migrate && hasTables {
		label := lastAgent
		if label == "" {
			label = fmt.Sprintf("schema-%d", ver)
		}
		if _, err := BackupDB(path, label); err != nil {
			return nil, fmt.Errorf("back up %s before the schema migration: %w", path, err)
		}
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if migrate {
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
			db.Close()
			return nil, fmt.Errorf("schema version: %w", err)
		}
	}
	_ = os.Chmod(path, 0o600)
	return &DB{DB: db, Path: path}, nil
}

// UserVersion returns PRAGMA user_version of the open DB.
func (d *DB) UserVersion() int {
	var v int
	_ = d.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v
}

// GetKV decodes kv[key] into out; false when missing.
func (d *DB) GetKV(key string, out any) (bool, error) {
	var s string
	err := d.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(s), out)
}

// SetKV stores v as JSON.
func (d *DB) SetKV(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = d.Exec(`INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b))
	return err
}

// DelKV removes a key.
func (d *DB) DelKV(key string) error {
	_, err := d.Exec(`DELETE FROM kv WHERE key = ?`, key)
	return err
}

// JSON marshals v to a string (panics never; returns "null" on error).
func JSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// NullStr returns a sql value for an optional string.
func NullStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
