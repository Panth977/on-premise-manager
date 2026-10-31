package driver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	sqlite "modernc.org/sqlite"
)

// Global is the file key of the global DB.
const Global = "@global"

var (
	depID   = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
	collID  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:_[A-Za-z0-9]+)*$`)
	depFile = regexp.MustCompile(`^([A-Za-z0-9]{1,64})\.sqlite$`)
)

// colLoc is where a collection lives.
type colLoc struct {
	key    string
	table  string
	parent string
}
type docLoc struct {
	colLoc
	path string
	id   string
}

func checkSegs(path string) ([]string, error) {
	s := strings.Split(path, "/")
	for _, x := range s {
		if x == "" || x == "." || x == ".." || (len(x) >= 4 && strings.HasPrefix(x, "__") && strings.HasSuffix(x, "__")) || len(x) > 1500 {
			return nil, bad(`invalid path "%s"`, path)
		}
	}
	return s, nil
}

func tableOf(ids []string, path string) (string, error) {
	for _, c := range ids {
		if !collID.MatchString(c) {
			return "", bad(`unsupported collection id "%s" in "%s" (letters, digits, single "_")`, c, path)
		}
	}
	return strings.Join(ids, "__"), nil
}

func evens(s []string) []string {
	var out []string
	for i := 0; i < len(s); i += 2 {
		out = append(out, s[i])
	}
	return out
}

// locateCollection: collection path → file + table; the root "deployments" → nil (every deployment file).
func locateCollection(path string) (*colLoc, error) {
	s, err := checkSegs(path)
	if err != nil {
		return nil, err
	}
	if len(s)%2 != 1 {
		return nil, bad(`"%s" is not a collection path`, path)
	}
	if s[0] == "deployments" {
		if len(s) == 1 {
			return nil, nil
		}
		dep := s[1]
		if !depID.MatchString(dep) {
			return nil, bad(`deployment id "%s" must be [A-Za-z0-9]{1,64}`, dep)
		}
		ids := evens(s[2:])
		if ids[0] == "deployment" {
			return nil, bad(`collection id "deployment" is reserved under deployments/{id}`)
		}
		t, err := tableOf(ids, path)
		if err != nil {
			return nil, err
		}
		return &colLoc{key: dep, table: t, parent: path}, nil
	}
	t, err := tableOf(evens(s), path)
	if err != nil {
		return nil, err
	}
	return &colLoc{key: Global, table: t, parent: path}, nil
}

func locateDoc(path string) (*docLoc, error) {
	s, err := checkSegs(path)
	if err != nil {
		return nil, err
	}
	if len(s)%2 != 0 {
		return nil, bad(`"%s" is not a document path`, path)
	}
	parent := strings.Join(s[:len(s)-1], "/")
	id := s[len(s)-1]
	if s[0] == "deployments" && len(s) == 2 {
		if !depID.MatchString(id) {
			return nil, bad(`deployment id "%s" must be [A-Za-z0-9]{1,64}`, id)
		}
		return &docLoc{colLoc{key: id, table: "deployment", parent: parent}, path, id}, nil
	}
	l, err := locateCollection(parent)
	if err != nil {
		return nil, err
	}
	return &docLoc{*l, path, id}, nil
}

// tableCollectionID: the collection id a table holds (collectionGroup).
func tableCollectionID(t string) string {
	if t == "deployment" {
		return "deployments"
	}
	if i := strings.LastIndex(t, "__"); i >= 0 {
		return t[i+2:]
	}
	return t
}

func q(ident string) string { return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"` }

func jsonPath(field string) string {
	var b strings.Builder
	b.WriteByte('$')
	for _, s := range strings.Split(field, ".") {
		b.WriteString(`."` + s + `"`)
	}
	return b.String()
}

// ixSQL: the ix encoding computed in SQL from the JSON (fallback queries + backfill); equals ixOf.
func ixSQL(field string) string {
	p := jsonPath(field)
	ts := p + `."$timestamp"`
	return fmt.Sprintf(`(CASE json_type(value, '%[1]s') WHEN 'true' THEN 1 WHEN 'false' THEN 0 WHEN 'integer' THEN json_extract(value, '%[1]s') WHEN 'real' THEN json_extract(value, '%[1]s') WHEN 'text' THEN json_extract(value, '%[1]s') WHEN 'object' THEN (CASE WHEN json_type(value, '%[2]s') = 'object' THEN json_extract(value, '%[2]s."seconds"') * 1000 + json_extract(value, '%[2]s."nanos"') / 1000000 END) END)`, p, ts)
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	var e *sqlite.Error
	if errors.As(err, &e) {
		if c := e.Code() & 0xff; c == 5 || c == 6 {
			return true
		}
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "database is locked") || strings.Contains(m, "sqlite_busy") || strings.Contains(m, "database table is locked")
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var bg = context.Background()

// handle is one open file: a small connection pool. refs (under files.mu) keeps it from being evicted.
type handle struct {
	key  string
	file string
	db   *sql.DB
	mu   sync.Mutex
	// tables whose schema matches the registry (committed)
	ensured map[string]bool
	refs    int
	used    uint64
}

func (h *handle) isEnsured(t string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ensured[t]
}
func (h *handle) setEnsured(t string) {
	h.mu.Lock()
	h.ensured[t] = true
	h.mu.Unlock()
}

// Files is the SQLite file layer: path routing, handle cache (LRU), lazy tables + index-column upgrades.
type Files struct {
	dbDir    string
	registry IndexRegistry
	strict   bool
	cap      int

	mu      sync.Mutex
	handles map[string]*handle
	tick    uint64
	locks   map[string]*sync.Mutex
	verMu   sync.Mutex
	vers    map[string]string
}

func newFiles(dataDir string, reg IndexRegistry, strict bool) (*Files, error) {
	f := &Files{dbDir: filepath.Join(dataDir, "db"), registry: reg, strict: strict, cap: 256, handles: map[string]*handle{}, locks: map[string]*sync.Mutex{}, vers: map[string]string{}}
	if err := os.MkdirAll(filepath.Join(f.dbDir, "deployments"), 0o777); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *Files) fileOf(key string) (string, error) {
	if key == Global {
		return filepath.Join(f.dbDir, "global.sqlite"), nil
	}
	if !depID.MatchString(key) {
		return "", bad(`deployment id "%s" must be [A-Za-z0-9]{1,64}`, key)
	}
	return filepath.Join(f.dbDir, "deployments", key+".sqlite"), nil
}

// DeploymentKeys lists every deployment file key (sorted).
func (f *Files) DeploymentKeys() ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(f.dbDir, "deployments"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if m := depFile.FindStringSubmatch(e.Name()); m != nil {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out, nil
}

// AllKeys is every file key, global last.
func (f *Files) AllKeys() ([]string, error) {
	k, err := f.DeploymentKeys()
	if err != nil {
		return nil, err
	}
	return append(k, Global), nil
}

func dsn(file string) string {
	return "file:" + (&url.URL{Path: file}).EscapedPath() + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}

// handle returns an open handle (release it); nil when the file does not exist and !create.
func (f *Files) handle(key string, create bool) (*handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tick++
	if h := f.handles[key]; h != nil {
		h.refs++
		h.used = f.tick
		return h, nil
	}
	file, err := f.fileOf(key)
	if err != nil {
		return nil, err
	}
	if !create {
		if _, err := os.Stat(file); err != nil {
			return nil, nil
		}
	}
	db, err := sql.Open("sqlite", dsn(file))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(2)
	if _, err := db.ExecContext(bg, `CREATE TABLE IF NOT EXISTS _outbox (seq INTEGER PRIMARY KEY AUTOINCREMENT, trigger TEXT NOT NULL, path TEXT NOT NULL, before TEXT, after TEXT, createdMs INTEGER NOT NULL, leaseUntil INTEGER);
CREATE TABLE IF NOT EXISTS _schema (tbl TEXT PRIMARY KEY, version TEXT NOT NULL);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	h := &handle{key: key, file: file, db: db, ensured: map[string]bool{}, refs: 1, used: f.tick}
	f.handles[key] = h
	f.evict()
	return h, nil
}

func (f *Files) release(h *handle) {
	if h == nil {
		return
	}
	f.mu.Lock()
	h.refs--
	f.evict()
	f.mu.Unlock()
}

// evict closes least recently used handles over the cap (never one in use). Holds f.mu.
func (f *Files) evict() {
	for len(f.handles) > f.cap {
		var old *handle
		for _, h := range f.handles {
			if h.refs == 0 && (old == nil || h.used < old.used) {
				old = h
			}
		}
		if old == nil {
			return
		}
		delete(f.handles, old.key)
		_ = old.db.Close()
	}
}

// closeAll closes every handle.
func (f *Files) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, h := range f.handles {
		_ = h.db.Close()
		delete(f.handles, k)
	}
}

// lock is the in-process write lock of a file: goroutines queue here instead of spinning in SQLite's busy handler.
func (f *Files) lock(key string) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		f.locks[key] = l
	}
	return l
}

func (f *Files) tables(qr querier) ([]string, error) {
	rows, err := qr.QueryContext(bg, "SELECT name FROM sqlite_master WHERE type = 'table' AND substr(name, 1, 1) <> '_' AND name NOT LIKE 'sqlite%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func hash16(s string) string {
	x := sha256.Sum256([]byte(s))
	return hex.EncodeToString(x[:])
}

func (f *Files) version(table string) string {
	f.verMu.Lock()
	defer f.verMu.Unlock()
	v, ok := f.vers[table]
	if !ok {
		s := "{}"
		if spec, has := f.registry[table]; has {
			s = specJSON(spec)
		}
		v = hash16(s)[:16]
		f.vers[table] = v
	}
	return v
}

func (f *Files) scalarFields(table string) []string { return f.registry[table].Scalar }

// txState is the state of one commit: the pinned connection of every file it locked, tables ensured in it.
type txState struct {
	conns   map[string]*sql.Conn
	ensured map[string]map[string]bool
}

func (t *txState) conn(key string) *sql.Conn {
	if t == nil {
		return nil
	}
	return t.conns[key]
}

// ensure makes the table match the registry. create=false: false when the table does not exist. Inside a commit the
// DDL runs in its transaction, otherwise in an own BEGIN IMMEDIATE. Tables ensured inside a commit count only after it.
func (f *Files) ensure(h *handle, table string, create bool, tx *txState) (bool, error) {
	if h.isEnsured(table) {
		return true, nil
	}
	conn := tx.conn(h.key)
	if conn != nil && tx.ensured[h.key][table] {
		return true, nil
	}
	var qr querier = h.db
	if conn != nil {
		qr = conn
	}
	var x int
	err := qr.QueryRowContext(bg, "SELECT 1 AS x FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&x)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	exists := err == nil
	if !exists && !create {
		return false, nil
	}
	ver := f.version(table)
	if exists {
		var v string
		err := qr.QueryRowContext(bg, "SELECT version FROM _schema WHERE tbl = ?", table).Scan(&v)
		if err == nil && v == ver {
			h.setEnsured(table)
			return true, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	if conn != nil {
		if err := f.upgrade(conn, table, ver); err != nil {
			return false, err
		}
		if tx.ensured[h.key] == nil {
			tx.ensured[h.key] = map[string]bool{}
		}
		tx.ensured[h.key][table] = true
		return true, nil
	}
	if tx == nil {
		l := f.lock(h.key)
		l.Lock()
		defer l.Unlock()
	}
	own, err := h.db.Conn(bg)
	if err != nil {
		return false, err
	}
	defer own.Close()
	if _, err := own.ExecContext(bg, "BEGIN IMMEDIATE"); err != nil {
		return false, err
	}
	if err := f.upgrade(own, table, ver); err != nil {
		_, _ = own.ExecContext(bg, "ROLLBACK")
		return false, err
	}
	if _, err := own.ExecContext(bg, "COMMIT"); err != nil {
		return false, err
	}
	h.setEnsured(table)
	return true, nil
}

func (f *Files) upgrade(c *sql.Conn, table, ver string) error {
	t := q(table)
	if _, err := c.ExecContext(bg, "CREATE TABLE IF NOT EXISTS "+t+" (id TEXT PRIMARY KEY, parent TEXT NOT NULL, value TEXT NOT NULL)"); err != nil {
		return err
	}
	spec := f.registry[table]
	have := map[string]bool{}
	rows, err := c.QueryContext(bg, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		have[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, fd := range spec.Scalar {
		col := IxColumn(fd)
		if have[col] {
			continue
		}
		if _, err := c.ExecContext(bg, "ALTER TABLE "+t+" ADD COLUMN "+q(col)); err != nil {
			return err
		}
		if _, err := c.ExecContext(bg, "UPDATE "+t+" SET "+q(col)+" = "+ixSQL(fd)); err != nil {
			return err
		}
	}
	idx := func(cols []string) error {
		name := table + "__" + hash16(strings.Join(cols, ","))[:10]
		qc := make([]string, len(cols))
		for i, x := range cols {
			qc[i] = q(x)
		}
		_, err := c.ExecContext(bg, "CREATE INDEX IF NOT EXISTS "+q(name)+" ON "+t+" ("+strings.Join(qc, ", ")+")")
		return err
	}
	if err := idx([]string{"parent", "id"}); err != nil {
		return err
	}
	for _, fd := range spec.Scalar {
		if err := idx([]string{"parent", IxColumn(fd)}); err != nil {
			return err
		}
	}
	for _, cc := range spec.Composite {
		cols := []string{"parent"}
		for _, x := range cc {
			cols = append(cols, IxColumn(x))
		}
		if err := idx(cols); err != nil {
			return err
		}
	}
	for _, fd := range spec.Group {
		if err := idx([]string{IxColumn(fd)}); err != nil {
			return err
		}
	}
	_, err = c.ExecContext(bg, "INSERT INTO _schema (tbl, version) VALUES (?, ?) ON CONFLICT(tbl) DO UPDATE SET version = excluded.version", table, ver)
	return err
}
