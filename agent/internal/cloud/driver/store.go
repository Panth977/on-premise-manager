package driver

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	mrand "math/rand/v2"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/cloud/doc"
)

// OutboxTrigger is the pattern half of a document trigger: every process that writes adds outbox rows for these.
type OutboxTrigger struct {
	Name    string
	Kind    string // created | written
	Pattern string
}

const idChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// RandomFrom returns n crypto-random characters of a charset.
func RandomFrom(charset string, n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(charset)))
	for i := range b {
		x, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = charset[x.Int64()]
	}
	return string(b)
}

// AutoID is a new random 20-character document id.
func AutoID() string { return RandomFrom(idChars, 20) }

// ───────────────────────────────────────── snapshots

// DocSnap is a document read (exists or not); it is also one row of a QuerySnap.
type DocSnap struct {
	store  *Store
	path   string
	raw    *string
	fields []string // select(): non-nil = projection
}

// ID is the last path segment.
func (s *DocSnap) ID() string { return s.path[strings.LastIndexByte(s.path, '/')+1:] }

// Path is the full document path.
func (s *DocSnap) Path() string { return s.path }

// Ref is the document's reference.
func (s *DocSnap) Ref() *DocRef { return s.store.Doc(s.path) }

// Exists reports whether the document exists.
func (s *DocSnap) Exists() bool { return s.raw != nil }

// Data returns a fresh decoded copy of the document, nil when it does not exist.
func (s *DocSnap) Data() *doc.Map {
	if s.raw == nil {
		return nil
	}
	d := mustDecode(*s.raw)
	if s.fields != nil {
		return project(d, s.fields)
	}
	return d
}

// Get returns the value at a dotted field path, nil when missing.
func (s *DocSnap) Get(field string) any {
	v, _ := s.Data().Path(field)
	return v
}

// Raw is the stored JSON (nil when missing).
func (s *DocSnap) Raw() *string { return s.raw }

func (s *DocSnap) full() *doc.Map {
	if s.raw == nil {
		return nil
	}
	return mustDecode(*s.raw)
}

// QuerySnap is a query result.
type QuerySnap struct{ Docs []*DocSnap }

// Empty reports no documents.
func (q *QuerySnap) Empty() bool { return len(q.Docs) == 0 }

// Size is the number of documents.
func (q *QuerySnap) Size() int { return len(q.Docs) }

// ───────────────────────────────────────── queries

// WhereOp values: == in array-contains < <= > >=
const (
	Asc  = "asc"
	Desc = "desc"
)

type filter struct {
	field, op string
	value     any
}
type order struct{ field, dir string }
type target struct {
	kind string // col | deployments | group
	loc  *colLoc
	id   string
}
type qspec struct {
	target  target
	filters []filter
	orders  []order
	limit   *int
	cursor  []any
	cursorS *DocSnap
	hasCur  bool
	sel     []string
}

const nameField = "__name__"

func isRange(op string) bool { return op == "<" || op == "<=" || op == ">" || op == ">=" }

type row struct {
	path string
	raw  string
	keys []any
}

// Query is an immutable query (every builder method returns a new one). A bad argument surfaces at Get.
type Query struct {
	store *Store
	spec  qspec
	err   error
}

func (q *Query) with(f func(*qspec)) *Query {
	n := &Query{store: q.store, spec: q.spec, err: q.err}
	f(&n.spec)
	return n
}
func (q *Query) fail(err error) *Query {
	n := &Query{store: q.store, spec: q.spec, err: q.err}
	if n.err == nil {
		n.err = err
	}
	return n
}

// Where adds a filter. value: nil, string, number, bool, Timestamp; for "in" a slice of those.
func (q *Query) Where(field, op string, value any) *Query {
	if _, err := checkField(field); err != nil {
		return q.fail(err)
	}
	switch op {
	case "==", "in", "array-contains", "<", "<=", ">", ">=":
	default:
		return q.fail(bad("unsupported operator %s", op))
	}
	v, err := doc.From(value)
	if err != nil {
		return q.fail(bad("%s", err.Error()))
	}
	if op == "in" {
		arr, ok := v.([]any)
		if !ok || len(arr) == 0 || len(arr) > 30 {
			return q.fail(bad("'in' needs a non-empty array of at most 30 values"))
		}
		for _, x := range arr {
			if _, err := kindOf(x); err != nil {
				return q.fail(err)
			}
		}
	} else if _, err := kindOf(v); err != nil {
		return q.fail(err)
	}
	return q.with(func(s *qspec) { s.filters = append(s.filters[:len(s.filters):len(s.filters)], filter{field, op, v}) })
}

// OrderBy adds an ordering (default asc).
func (q *Query) OrderBy(field string, dir ...string) *Query {
	if _, err := checkField(field); err != nil {
		return q.fail(err)
	}
	d := Asc
	if len(dir) > 0 {
		d = dir[0]
	}
	return q.with(func(s *qspec) { s.orders = append(s.orders[:len(s.orders):len(s.orders)], order{field, d}) })
}

// Limit caps the result.
func (q *Query) Limit(n int) *Query { return q.with(func(s *qspec) { s.limit = &n }) }

// StartAfter continues after these orderBy values.
func (q *Query) StartAfter(values ...any) *Query {
	norm := make([]any, len(values))
	for i, v := range values {
		x, err := doc.From(v)
		if err != nil {
			return q.fail(bad("%s", err.Error()))
		}
		norm[i] = x
	}
	return q.with(func(s *qspec) { s.cursor, s.cursorS, s.hasCur = norm, nil, true })
}

// StartAfterSnap continues after a snapshot (of the previous page's last document).
func (q *Query) StartAfterSnap(snap *DocSnap) *Query {
	return q.with(func(s *qspec) { s.cursor, s.cursorS, s.hasCur = nil, snap, true })
}

// Select projects: Data() holds only these fields (none = existence only, Data() is {}).
func (q *Query) Select(fields ...string) *Query {
	for _, f := range fields {
		if _, err := checkField(f); err != nil {
			return q.fail(err)
		}
	}
	sel := append([]string{}, fields...)
	return q.with(func(s *qspec) { s.sel = sel })
}

// Get runs the query.
func (q *Query) Get() (*QuerySnap, error) {
	if q.err != nil {
		return nil, q.err
	}
	rows, _, err := q.store.runQuery(&q.spec, nil)
	if err != nil {
		return nil, err
	}
	return q.store.qsnap(rows, q.spec.sel), nil
}

func (s *Store) qsnap(rows []row, sel []string) *QuerySnap {
	out := &QuerySnap{Docs: make([]*DocSnap, len(rows))}
	for i := range rows {
		out.Docs[i] = &DocSnap{store: s, path: rows[i].path, raw: &rows[i].raw, fields: sel}
	}
	return out
}

// CollectionRef is a collection (a Query over all its documents).
type CollectionRef struct {
	*Query
	Path string
}

// ID is the last path segment.
func (c *CollectionRef) ID() string { return c.Path[strings.LastIndexByte(c.Path, '/')+1:] }

// Parent is the document this collection hangs under; nil for a root collection.
func (c *CollectionRef) Parent() *DocRef {
	i := strings.LastIndexByte(c.Path, '/')
	if i < 0 {
		return nil
	}
	return c.store.Doc(c.Path[:i])
}

// Doc references a document; no id = a new random 20-character id.
func (c *CollectionRef) Doc(id ...string) *DocRef {
	if len(id) > 0 {
		return c.store.Doc(c.Path + "/" + id[0])
	}
	return c.store.Doc(c.Path + "/" + AutoID())
}

// ───────────────────────────────────────── writes

type writeOp struct {
	kind string // set | merge | create | update | delete
	path string
	data *doc.Map
	err  error
}

type readRec struct {
	keys  []string
	check func(tx *txState) (bool, error)
}

// SetOption of Set.
type SetOption int

// Merge is { merge: true }.
const Merge SetOption = 1

func mkOp(kind string, ref *DocRef, data any, opts ...SetOption) writeOp {
	if len(opts) > 0 && opts[0] == Merge {
		kind = "merge"
	}
	op := writeOp{kind: kind, path: ref.Path, err: ref.err}
	if kind != "delete" && op.err == nil {
		m, err := doc.ToMap(data)
		if err != nil {
			op.err = bad("%s", err.Error())
		}
		op.data = m
	}
	return op
}

// DocRef references a document. A bad path surfaces at the first operation (Err tells earlier).
type DocRef struct {
	store *Store
	Path  string
	err   error
}

// Err is the path error, if any.
func (r *DocRef) Err() error { return r.err }

// ID is the last path segment.
func (r *DocRef) ID() string { return r.Path[strings.LastIndexByte(r.Path, '/')+1:] }

// Parent is the collection of the document.
func (r *DocRef) Parent() *CollectionRef {
	return r.store.Collection(r.Path[:strings.LastIndexByte(r.Path, '/')])
}

// Collection references a sub-collection.
func (r *DocRef) Collection(id string) *CollectionRef { return r.store.Collection(r.Path + "/" + id) }

// Get reads the document.
func (r *DocRef) Get() (*DocSnap, error) {
	if r.err != nil {
		return nil, r.err
	}
	raw, err := r.store.readRaw(r.Path, nil)
	if err != nil {
		return nil, err
	}
	return &DocSnap{store: r.store, path: r.Path, raw: raw}, nil
}

// Set writes the document (replace; Merge = deep merge).
func (r *DocRef) Set(data any, opts ...SetOption) error {
	return r.store.write([]writeOp{mkOp("set", r, data, opts...)})
}

// Create writes a new document; already-exists when it exists.
func (r *DocRef) Create(data any) error { return r.store.write([]writeOp{mkOp("create", r, data)}) }

// Update changes fields (dotted paths); not-found when the document is missing.
func (r *DocRef) Update(data any) error { return r.store.write([]writeOp{mkOp("update", r, data)}) }

// Delete removes the document (missing is fine).
func (r *DocRef) Delete() error { return r.store.write([]writeOp{mkOp("delete", r, nil)}) }

// Tx is a transaction: reads first, then buffered writes (a bad write surfaces at commit).
type Tx struct {
	store  *Store
	reads  []readRec
	writes []writeOp
}

func (t *Tx) noReadAfterWrite() error {
	if len(t.writes) > 0 {
		return bad("Firestore transactions require all reads to be executed before all writes.")
	}
	return nil
}

func (t *Tx) readDoc(r *DocRef) (*DocSnap, error) {
	if r.err != nil {
		return nil, r.err
	}
	raw, err := t.store.readRaw(r.Path, nil)
	if err != nil {
		return nil, err
	}
	loc, err := locateDoc(r.Path)
	if err != nil {
		return nil, err
	}
	path := r.Path
	t.reads = append(t.reads, readRec{keys: []string{loc.key}, check: func(tx *txState) (bool, error) {
		cur, err := t.store.readRaw(path, tx)
		if err != nil {
			return false, err
		}
		if cur == nil || raw == nil {
			return cur == nil && raw == nil, nil
		}
		return *cur == *raw, nil
	}})
	return &DocSnap{store: t.store, path: r.Path, raw: raw}, nil
}

// Get reads a document.
func (t *Tx) Get(r *DocRef) (*DocSnap, error) {
	if err := t.noReadAfterWrite(); err != nil {
		return nil, err
	}
	return t.readDoc(r)
}

// GetAll reads documents, keeping order.
func (t *Tx) GetAll(refs ...*DocRef) ([]*DocSnap, error) {
	if err := t.noReadAfterWrite(); err != nil {
		return nil, err
	}
	out := make([]*DocSnap, len(refs))
	for i, r := range refs {
		s, err := t.readDoc(r)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

// Query runs a query in the transaction (tx.get(query)). Pass a *Query or a collection's .Query.
func (t *Tx) Query(q *Query) (*QuerySnap, error) {
	if err := t.noReadAfterWrite(); err != nil {
		return nil, err
	}
	if q.err != nil {
		return nil, q.err
	}
	rows, keys, err := t.store.runQuery(&q.spec, nil)
	if err != nil {
		return nil, err
	}
	// multi-file queries also lock the global file: a common lock for transactions whose file sets differ
	if q.spec.target.kind != "col" {
		seen := false
		for _, k := range keys {
			seen = seen || k == Global
		}
		if !seen {
			keys = append(keys, Global)
		}
	}
	spec := q.spec
	t.reads = append(t.reads, readRec{keys: keys, check: func(tx *txState) (bool, error) {
		now, _, err := t.store.runQuery(&spec, tx)
		if err != nil {
			return false, err
		}
		if len(now) != len(rows) {
			return false, nil
		}
		for i := range now {
			if now[i].path != rows[i].path || now[i].raw != rows[i].raw {
				return false, nil
			}
		}
		return true, nil
	}})
	return t.store.qsnap(rows, q.spec.sel), nil
}

// Set buffers a set.
func (t *Tx) Set(r *DocRef, data any, opts ...SetOption) *Tx {
	t.writes = append(t.writes, mkOp("set", r, data, opts...))
	return t
}

// Create buffers a create.
func (t *Tx) Create(r *DocRef, data any) *Tx {
	t.writes = append(t.writes, mkOp("create", r, data))
	return t
}

// Update buffers an update.
func (t *Tx) Update(r *DocRef, data any) *Tx {
	t.writes = append(t.writes, mkOp("update", r, data))
	return t
}

// Delete buffers a delete.
func (t *Tx) Delete(r *DocRef) *Tx {
	t.writes = append(t.writes, mkOp("delete", r, nil))
	return t
}

// WriteBatch is an atomic batch.
type WriteBatch struct {
	store *Store
	ops   []writeOp
}

// Set adds a set.
func (b *WriteBatch) Set(r *DocRef, data any, opts ...SetOption) *WriteBatch {
	b.ops = append(b.ops, mkOp("set", r, data, opts...))
	return b
}

// Create adds a create.
func (b *WriteBatch) Create(r *DocRef, data any) *WriteBatch {
	b.ops = append(b.ops, mkOp("create", r, data))
	return b
}

// Update adds an update.
func (b *WriteBatch) Update(r *DocRef, data any) *WriteBatch {
	b.ops = append(b.ops, mkOp("update", r, data))
	return b
}

// Delete adds a delete.
func (b *WriteBatch) Delete(r *DocRef) *WriteBatch {
	b.ops = append(b.ops, mkOp("delete", r, nil))
	return b
}

// Commit commits the batch atomically.
func (b *WriteBatch) Commit() error {
	if len(b.ops) == 0 {
		return nil
	}
	return b.store.write(b.ops)
}

// BulkWriter does independent parallel writes; each settles on its own.
type BulkWriter struct {
	store *Store
	wg    sync.WaitGroup
	mu    sync.Mutex
	onErr func(err error, failedAttempts int) bool
}

// BulkResult is the outcome of one bulk write.
type BulkResult struct {
	done chan struct{}
	err  error
}

// Wait waits for the write and returns its error.
func (r *BulkResult) Wait() error {
	<-r.done
	return r.err
}

func (w *BulkWriter) run(op writeOp) *BulkResult {
	r := &BulkResult{done: make(chan struct{})}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer close(r.done)
		for attempt := 1; ; attempt++ {
			err := w.store.write([]writeOp{op})
			if err == nil {
				return
			}
			w.mu.Lock()
			cb := w.onErr
			w.mu.Unlock()
			retry := false
			if cb != nil {
				retry = cb(err, attempt)
			} else if c := StoreErrorCode(err); (c == Aborted || c == Unavailable) && attempt < 10 {
				retry = true
			}
			if !retry {
				r.err = err
				return
			}
			d := 10 << attempt
			if d > 1000 {
				d = 1000
			}
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
	}()
	return r
}

// Create queues a create.
func (w *BulkWriter) Create(r *DocRef, data any) *BulkResult { return w.run(mkOp("create", r, data)) }

// Set queues a set.
func (w *BulkWriter) Set(r *DocRef, data any, opts ...SetOption) *BulkResult {
	return w.run(mkOp("set", r, data, opts...))
}

// Update queues an update.
func (w *BulkWriter) Update(r *DocRef, data any) *BulkResult { return w.run(mkOp("update", r, data)) }

// Delete queues a delete.
func (w *BulkWriter) Delete(r *DocRef) *BulkResult { return w.run(mkOp("delete", r, nil)) }

// OnWriteError sets the retry policy (true = retry).
func (w *BulkWriter) OnWriteError(cb func(err error, failedAttempts int) bool) {
	w.mu.Lock()
	w.onErr = cb
	w.mu.Unlock()
}

// Close waits for every queued write.
func (w *BulkWriter) Close() { w.wg.Wait() }

// ───────────────────────────────────────── the store

func rank(x any) int {
	switch x.(type) {
	case nil:
		return 0
	case float64:
		return 1
	}
	return 2
}

// sqlCmp: NULL < numbers < strings in UTF-8 byte order (the order SQLite uses).
func sqlCmp(a, b any) int {
	ra, rb := rank(a), rank(b)
	if ra != rb {
		return ra - rb
	}
	switch ra {
	case 0:
		return 0
	case 1:
		x, y := a.(float64), b.(float64)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return 0
	}
	return strings.Compare(a.(string), b.(string))
}

// Store is the SQLite document store: one global file + one file per deployment, Firestore semantics.
type Store struct {
	Files    *Files
	Triggers []OutboxTrigger
	trace    bool

	lmu       sync.Mutex
	listeners map[int]func(key string)
	lnext     int
}

func newStore(dataDir string, reg IndexRegistry, strict bool, triggers []OutboxTrigger) (*Store, error) {
	f, err := newFiles(dataDir, reg, strict)
	if err != nil {
		return nil, err
	}
	return &Store{Files: f, Triggers: triggers, trace: os.Getenv("OPM_LOCAL_TRACE_XTX") == "1", listeners: map[int]func(string){}}, nil
}

func (s *Store) onOutbox(fn func(key string)) (remove func()) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	id := s.lnext
	s.lnext++
	s.listeners[id] = fn
	return func() {
		s.lmu.Lock()
		delete(s.listeners, id)
		s.lmu.Unlock()
	}
}

// Collection references a collection.
func (s *Store) Collection(path string) *CollectionRef {
	c := &CollectionRef{Path: path, Query: &Query{store: s}}
	loc, err := locateCollection(path)
	if err != nil {
		c.Query.err = err
		return c
	}
	if loc != nil {
		c.Query.spec.target = target{kind: "col", loc: loc}
	} else {
		c.Query.spec.target = target{kind: "deployments"}
	}
	return c
}

// Doc references a document.
func (s *Store) Doc(path string) *DocRef {
	_, err := locateDoc(path)
	return &DocRef{store: s, Path: path, err: err}
}

var groupID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// CollectionGroup queries every collection with this id, in every file.
func (s *Store) CollectionGroup(id string) *Query {
	q := &Query{store: s, spec: qspec{target: target{kind: "group", id: id}}}
	if !groupID.MatchString(id) {
		q.err = bad("bad collection id %s", id)
	}
	return q
}

// GetAll reads documents, keeping order.
func (s *Store) GetAll(refs ...*DocRef) ([]*DocSnap, error) {
	out := make([]*DocSnap, len(refs))
	for i, r := range refs {
		x, err := r.Get()
		if err != nil {
			return nil, err
		}
		out[i] = x
	}
	return out, nil
}

// Batch starts a batch.
func (s *Store) Batch() *WriteBatch { return &WriteBatch{store: s} }

// NewBulkWriter starts a bulk writer.
func (s *Store) NewBulkWriter() *BulkWriter { return &BulkWriter{store: s} }

func jitter(lo, hi int) time.Duration {
	return time.Duration(lo+mrand.IntN(hi-lo)) * time.Millisecond
}

// TxAttempts sets maxAttempts of a transaction (default 5).
type TxAttempts int

// RunTransaction is runTransaction: fn may run more than once on contention; its error aborts without a retry.
func (s *Store) RunTransaction(fn func(tx *Tx) error, opts ...TxAttempts) error {
	max := 5
	if len(opts) > 0 {
		max = int(opts[0])
	}
	for attempt := 1; ; attempt++ {
		tx := &Tx{store: s}
		if err := fn(tx); err != nil {
			return err
		}
		ok, err := s.commit(tx.reads, tx.writes)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if attempt >= max {
			return storeErr(Aborted, "transaction aborted after "+strconv.Itoa(attempt)+" attempts (contention)")
		}
		cap := 250
		if attempt < 6 && 5<<attempt < cap {
			cap = 5 << attempt
		}
		time.Sleep(jitter(1, cap+2))
	}
}

// RunTx is RunTransaction with a result value.
func RunTx[T any](s *Store, fn func(tx *Tx) (T, error), opts ...TxAttempts) (T, error) {
	var out T
	err := s.RunTransaction(func(tx *Tx) error {
		v, err := fn(tx)
		if err == nil {
			out = v
		}
		return err
	}, opts...)
	return out, err
}

// Close closes every file.
func (s *Store) Close() error {
	s.Files.closeAll()
	return nil
}

// querierFor is the connection a read uses: the commit's pinned one, else the pool.
func querierFor(h *handle, tx *txState) querier {
	if c := tx.conn(h.key); c != nil {
		return c
	}
	return h.db
}

func (s *Store) readRaw(path string, tx *txState) (*string, error) {
	loc, err := locateDoc(path)
	if err != nil {
		return nil, err
	}
	h, err := s.Files.handle(loc.key, false)
	if err != nil || h == nil {
		return nil, err
	}
	defer s.Files.release(h)
	ok, err := s.Files.ensure(h, loc.table, false, tx)
	if err != nil || !ok {
		return nil, err
	}
	return readIn(querierFor(h, tx), loc.table, path)
}

func readIn(qr querier, table, path string) (*string, error) {
	var v string
	err := qr.QueryRowContext(bg, "SELECT value FROM "+q(table)+" WHERE id = ?", path).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Store) effectiveOrders(spec *qspec) []order {
	o := append([]order{}, spec.orders...)
	if len(o) == 0 {
		for _, f := range spec.filters {
			if isRange(f.op) {
				o = append(o, order{f.field, Asc})
				break
			}
		}
	}
	dir := Asc
	if len(o) > 0 {
		dir = o[len(o)-1].dir
	}
	return append(o, order{nameField, dir})
}

// runQuery returns the rows + the file keys the query looked at.
func (s *Store) runQuery(spec *qspec, tx *txState) ([]row, []string, error) {
	type tg struct {
		key, table string
		parent     *string
	}
	var targets []tg
	switch spec.target.kind {
	case "col":
		l := spec.target.loc
		targets = append(targets, tg{l.key, l.table, &l.parent})
	case "deployments":
		keys, err := s.Files.DeploymentKeys()
		if err != nil {
			return nil, nil, err
		}
		p := "deployments"
		for _, k := range keys {
			targets = append(targets, tg{k, "deployment", &p})
		}
	default:
		keys, err := s.Files.AllKeys()
		if err != nil {
			return nil, nil, err
		}
		for _, k := range keys {
			h, err := s.Files.handle(k, false)
			if err != nil {
				return nil, nil, err
			}
			if h == nil {
				continue
			}
			tb, err := s.Files.tables(querierFor(h, tx))
			s.Files.release(h)
			if err != nil {
				return nil, nil, err
			}
			for _, t := range tb {
				if tableCollectionID(t) == spec.target.id {
					targets = append(targets, tg{k, t, nil})
				}
			}
		}
	}
	orders := s.effectiveOrders(spec)
	var keys []string
	seen := map[string]bool{}
	for _, t := range targets {
		if !seen[t.key] {
			seen[t.key] = true
			keys = append(keys, t.key)
		}
	}
	var rows []row
	for _, t := range targets {
		h, err := s.Files.handle(t.key, false)
		if err != nil {
			return nil, nil, err
		}
		if h == nil {
			continue
		}
		ok, err := s.Files.ensure(h, t.table, false, tx)
		if err == nil && ok {
			var r []row
			r, err = s.queryTable(h, t.table, t.parent, spec, orders, tx)
			rows = append(rows, r...)
		}
		s.Files.release(h)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(targets) > 1 {
		sort.SliceStable(rows, func(i, j int) bool {
			for x := range orders {
				if c := sqlCmp(rows[i].keys[x], rows[j].keys[x]); c != 0 {
					if orders[x].dir == Asc {
						return c < 0
					}
					return c > 0
				}
			}
			return false
		})
		if spec.limit != nil && len(rows) > *spec.limit {
			rows = rows[:*spec.limit]
		}
	}
	return rows, keys, nil
}

// expr: SQL expression of a field in a table: its ix column, else the JSON fallback (an error in strict mode).
func (s *Store) expr(table, field string) (string, error) {
	if field == nameField {
		return "id", nil
	}
	for _, f := range s.Files.scalarFields(table) {
		if f == field {
			return q(IxColumn(field)), nil
		}
	}
	if s.Files.strict {
		return "", storeErr(FailedPrecondition, `local store (strict): no index column for "`+table+"."+field+`" — add it to adapters/local/registry.ts`)
	}
	return ixSQL(field), nil
}

func guard(k valueKind, field, e string) string {
	p := jsonPath(field)
	switch k {
	case kString:
		return "typeof(" + e + ") = 'text'"
	case kNumber:
		return "json_type(value, '" + p + "') IN ('integer', 'real')"
	case kBoolean:
		return "json_type(value, '" + p + "') IN ('true', 'false')"
	case kTimestamp:
		return "json_type(value, '" + p + "') = 'object'"
	}
	return "json_type(value, '" + p + "') = 'null'"
}

func (s *Store) queryTable(h *handle, table string, parent *string, spec *qspec, orders []order, tx *txState) ([]row, error) {
	var where []string
	var binds []any
	if parent != nil {
		where = append(where, "parent = ?")
		binds = append(binds, *parent)
	}
	for _, f := range spec.filters {
		if f.op == "array-contains" {
			if s.Files.strict {
				found := false
				for _, a := range s.Files.registry[table].Array {
					found = found || a == f.field
				}
				if !found {
					return nil, storeErr(FailedPrecondition, `local store (strict): "`+table+"."+f.field+`" is not registered as an array field — add it to adapters/local/registry.ts`)
				}
			}
			p := jsonPath(f.field)
			k, err := kindOf(f.value)
			if err != nil {
				return nil, err
			}
			var jc string
			switch k {
			case kString:
				jc = "j.type = 'text' AND j.value = ?"
			case kNumber:
				jc = "j.type IN ('integer', 'real') AND j.value = ?"
			case kBoolean:
				if b, _ := f.value.(bool); b {
					jc = "j.type = 'true'"
				} else {
					jc = "j.type = 'false'"
				}
			case kNull:
				jc = "j.type = 'null'"
			default:
				jc = `j.type = 'object' AND json_extract(j.value, '$."$timestamp"."seconds"') * 1000 + json_extract(j.value, '$."$timestamp"."nanos"') / 1000000 = ?`
			}
			where = append(where, "json_type(value, '"+p+"') = 'array' AND EXISTS (SELECT 1 FROM json_each(t.value, '"+p+"') AS j WHERE "+jc+")")
			if k != kBoolean && k != kNull {
				binds = append(binds, ixOf(f.value))
			}
			continue
		}
		e, err := s.expr(table, f.field)
		if err != nil {
			return nil, err
		}
		if f.op == "in" {
			var kinds []valueKind
			by := map[valueKind][]any{}
			for _, v := range f.value.([]any) {
				k, _ := kindOf(v)
				if _, ok := by[k]; !ok {
					kinds = append(kinds, k)
				}
				by[k] = append(by[k], v)
			}
			var parts []string
			for _, k := range kinds {
				if k == kNull {
					parts = append(parts, guard(kNull, f.field, e))
					continue
				}
				vs := by[k]
				parts = append(parts, "("+e+" IN ("+strings.TrimSuffix(strings.Repeat("?, ", len(vs)), ", ")+") AND "+guard(k, f.field, e)+")")
				for _, v := range vs {
					binds = append(binds, ixOf(v))
				}
			}
			where = append(where, "("+strings.Join(parts, " OR ")+")")
			continue
		}
		k, err := kindOf(f.value)
		if err != nil {
			return nil, err
		}
		if k == kNull {
			// Firestore: == null matches null values; range on null matches nothing useful
			if f.op == "==" {
				where = append(where, guard(kNull, f.field, e))
			} else {
				where = append(where, "0")
			}
			continue
		}
		op := f.op
		if op == "==" {
			op = "="
		}
		where = append(where, e+" "+op+" ? AND "+guard(k, f.field, e))
		binds = append(binds, ixOf(f.value))
	}
	if spec.hasCur {
		var vals []any
		n := 0
		if spec.cursorS != nil {
			d := spec.cursorS.full()
			if d == nil {
				return nil, bad("startAfter: the cursor document does not exist")
			}
			for _, o := range orders {
				if o.field == nameField {
					vals = append(vals, spec.cursorS.path)
				} else {
					v, _ := getField(d, o.field)
					vals = append(vals, ixOf(v))
				}
			}
			n = len(orders)
		} else {
			n = len(spec.cursor)
			if n > len(orders) {
				return nil, bad("startAfter: more cursor values than orderBy fields")
			}
			for _, v := range spec.cursor {
				if _, err := kindOf(v); err != nil {
					return nil, err
				}
				vals = append(vals, ixOf(v))
			}
		}
		var ors []string
		for i := 0; i < n; i++ {
			var parts []string
			for j := 0; j < i; j++ {
				e, err := s.expr(table, orders[j].field)
				if err != nil {
					return nil, err
				}
				parts = append(parts, e+" IS ?")
				binds = append(binds, vals[j])
			}
			e, err := s.expr(table, orders[i].field)
			if err != nil {
				return nil, err
			}
			v := vals[i]
			if orders[i].dir == Asc {
				if v == nil {
					parts = append(parts, e+" IS NOT NULL")
				} else {
					parts = append(parts, e+" > ?")
					binds = append(binds, v)
				}
			} else if v == nil {
				parts = append(parts, "0")
			} else {
				parts = append(parts, "("+e+" < ? OR "+e+" IS NULL)")
				binds = append(binds, v)
			}
			ors = append(ors, "("+strings.Join(parts, " AND ")+")")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	sel := make([]string, len(orders))
	by := make([]string, len(orders))
	for i, o := range orders {
		e, err := s.expr(table, o.field)
		if err != nil {
			return nil, err
		}
		sel[i] = e + " AS k" + strconv.Itoa(i)
		dir := "ASC"
		if o.dir != Asc {
			dir = "DESC"
		}
		by[i] = "k" + strconv.Itoa(i) + " " + dir
	}
	sqlText := "SELECT id, value, " + strings.Join(sel, ", ") + " FROM " + q(table) + " AS t"
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY " + strings.Join(by, ", ")
	if spec.limit != nil {
		sqlText += " LIMIT ?"
		binds = append(binds, int64(*spec.limit))
	}
	rows, err := querierFor(h, tx).QueryContext(bg, sqlText, binds...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	n := len(orders)
	for rows.Next() {
		r := row{keys: make([]any, n)}
		dest := make([]any, n+2)
		dest[0], dest[1] = &r.path, &r.raw
		for i := 0; i < n; i++ {
			dest[i+2] = &r.keys[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		for i, k := range r.keys {
			switch x := k.(type) {
			case int64:
				r.keys[i] = float64(x)
			case []byte:
				r.keys[i] = string(x)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// write: non-transactional write(s) (one atomic commit); retried on lock timeouts.
func (s *Store) write(ops []writeOp) error {
	for attempt := 1; ; attempt++ {
		ok, err := s.commit(nil, ops)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if attempt >= 5 {
			return storeErr(Unavailable, "database busy")
		}
		time.Sleep(jitter(5, 50*attempt))
	}
}

// commit: in-process locks + BEGIN IMMEDIATE on every involved file (deployment files sorted by id, global LAST),
// re-validate the reads, apply the writes + outbox rows, COMMIT deployment files first and global last. false = a read
// changed or a lock timed out (the caller re-runs). Write errors (create / update preconditions) are returned.
func (s *Store) commit(reads []readRec, writes []writeOp) (bool, error) {
	if len(reads) == 0 && len(writes) == 0 {
		return true, nil
	}
	locs := make([]*docLoc, len(writes))
	writeKeys := map[string]bool{}
	all := map[string]bool{}
	for i, w := range writes {
		if w.err != nil {
			return false, w.err
		}
		l, err := locateDoc(w.path)
		if err != nil {
			return false, err
		}
		locs[i] = l
		writeKeys[l.key] = true
		all[l.key] = true
	}
	for _, r := range reads {
		for _, k := range r.keys {
			all[k] = true
		}
	}
	var keys []string
	for k := range all {
		if k != Global {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if all[Global] {
		keys = append(keys, Global)
	}
	if s.trace && len(keys) > 1 {
		paths := make([]string, len(writes))
		for i, w := range writes {
			paths[i] = w.path
		}
		Log.Info("local cross-db tx", "files", keys, "paths", paths)
	}
	for _, k := range keys {
		l := s.Files.lock(k)
		l.Lock()
		defer l.Unlock()
	}
	tx := &txState{conns: map[string]*sql.Conn{}, ensured: map[string]map[string]bool{}}
	handles := map[string]*handle{}
	var begun []string
	defer func() {
		for _, h := range handles {
			s.Files.release(h)
		}
	}()
	rollback := func() {
		for i := len(begun) - 1; i >= 0; i-- {
			if c := tx.conns[begun[i]]; c != nil {
				_, _ = c.ExecContext(bg, "ROLLBACK")
			}
		}
		begun = nil
	}
	defer func() {
		// a conn goes back to the pool: never with a transaction open (ROLLBACK after a COMMIT is a harmless error)
		rollback()
		for _, c := range tx.conns {
			_ = c.Close()
		}
	}()
	fail := func(err error) (bool, error) {
		rollback()
		if isBusy(err) {
			return false, nil
		}
		return false, err
	}
	for _, k := range keys {
		h, err := s.Files.handle(k, writeKeys[k])
		if err != nil {
			return fail(err)
		}
		if h == nil {
			continue
		}
		handles[k] = h
		c, err := h.db.Conn(bg)
		if err != nil {
			return fail(err)
		}
		tx.conns[k] = c
		if _, err := c.ExecContext(bg, "BEGIN IMMEDIATE"); err != nil {
			return fail(err)
		}
		begun = append(begun, k)
	}
	for _, r := range reads {
		ok, err := r.check(tx)
		if err != nil {
			return fail(err)
		}
		if !ok {
			rollback()
			return false, nil
		}
	}
	now := doc.Now()
	before := map[string]*string{}
	after := map[string]*string{}
	var touched []string
	for i, w := range writes {
		loc := locs[i]
		h := handles[loc.key]
		if _, err := s.Files.ensure(h, loc.table, true, tx); err != nil {
			return fail(err)
		}
		c := tx.conns[loc.key]
		curRaw, had := after[w.path]
		if !had {
			var err error
			if curRaw, err = readIn(c, loc.table, w.path); err != nil {
				return fail(err)
			}
		}
		if _, ok := before[w.path]; !ok {
			before[w.path] = curRaw
			touched = append(touched, w.path)
		}
		var cur *doc.Map
		if curRaw != nil {
			var err error
			if cur, err = DecodeDoc(*curRaw); err != nil {
				return fail(err)
			}
		}
		var next *doc.Map
		var err error
		switch w.kind {
		case "set":
			next, err = applySet(w.data, now)
		case "merge":
			next, err = applyMerge(cur, w.data, now)
		case "create":
			if cur != nil {
				err = storeErr(AlreadyExists, "Document already exists: "+w.path)
			} else {
				next, err = applySet(w.data, now)
			}
		case "update":
			if cur == nil {
				err = storeErr(NotFound, "No document to update: "+w.path)
			} else {
				next, err = applyUpdate(cur, w.data, now)
			}
		}
		if err != nil {
			return fail(err)
		}
		t := q(loc.table)
		if next == nil {
			if _, err := c.ExecContext(bg, "DELETE FROM "+t+" WHERE id = ?", w.path); err != nil {
				return fail(err)
			}
			after[w.path] = nil
			continue
		}
		raw, err := EncodeDoc(next)
		if err != nil {
			return fail(err)
		}
		fields := s.Files.scalarFields(loc.table)
		cols := []string{q("id"), q("parent"), q("value")}
		args := []any{w.path, loc.parent, raw}
		for _, f := range fields {
			cols = append(cols, q(IxColumn(f)))
			v, _ := getField(next, f)
			args = append(args, ixOf(v))
		}
		sets := make([]string, 0, len(cols)-1)
		for _, col := range cols[1:] {
			sets = append(sets, col+" = excluded."+col)
		}
		sqlText := "INSERT INTO " + t + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ") ON CONFLICT(id) DO UPDATE SET " + strings.Join(sets, ", ")
		if _, err := c.ExecContext(bg, sqlText, args...); err != nil {
			return fail(err)
		}
		after[w.path] = &raw
	}
	// outbox rows: one per changed document per matching trigger, in the same SQLite transaction
	woke := map[string]bool{}
	if len(s.Triggers) > 0 {
		ms := time.Now().UnixMilli()
		for _, path := range touched {
			b, a := before[path], after[path]
			if (a == nil && b == nil) || (a != nil && b != nil && *a == *b) {
				continue
			}
			for _, tr := range s.Triggers {
				if tr.Kind == "created" && !(b == nil && a != nil) {
					continue
				}
				if MatchPattern(tr.Pattern, path) == nil {
					continue
				}
				l, _ := locateDoc(path)
				if _, err := tx.conns[l.key].ExecContext(bg, "INSERT INTO _outbox (trigger, path, before, after, createdMs) VALUES (?, ?, ?, ?, ?)", tr.Name, path, nullable(b), nullable(a), ms); err != nil {
					return fail(err)
				}
				woke[l.key] = true
			}
		}
	}
	// deployment files first, global last (keys are in that order)
	for len(begun) > 0 {
		if _, err := tx.conns[begun[0]].ExecContext(bg, "COMMIT"); err != nil {
			return fail(err)
		}
		k := begun[0]
		begun = begun[1:]
		for t := range tx.ensured[k] {
			handles[k].setEnsured(t)
		}
	}
	if len(woke) > 0 {
		s.lmu.Lock()
		fns := make([]func(string), 0, len(s.listeners))
		for _, fn := range s.listeners {
			fns = append(fns, fn)
		}
		s.lmu.Unlock()
		for k := range woke {
			for _, fn := range fns {
				fn(k)
			}
		}
	}
	return true, nil
}

func nullable(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// ───────────────────────────────────────── enumeration (migration tool, backups)

type fileDoc struct{ path, raw string }

func (s *Store) fileDocs(key string) ([]fileDoc, error) {
	h, err := s.Files.handle(key, false)
	if err != nil || h == nil {
		return nil, err
	}
	defer s.Files.release(h)
	tables, err := s.Files.tables(h.db)
	if err != nil || len(tables) == 0 {
		return nil, err
	}
	parts := make([]string, len(tables))
	for i, t := range tables {
		parts[i] = "SELECT id, value FROM " + q(t)
	}
	rows, err := h.db.QueryContext(bg, strings.Join(parts, " UNION ALL ")+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fileDoc
	for rows.Next() {
		var d fileDoc
		if err := rows.Scan(&d.path, &d.raw); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AllDocuments calls fn for every document of every file, ordered by path (UTF-8 order).
func (s *Store) AllDocuments(fn func(path string, data *doc.Map) error) error {
	keys, err := s.Files.AllKeys()
	if err != nil {
		return err
	}
	var all []fileDoc
	for _, k := range keys {
		d, err := s.fileDocs(k)
		if err != nil {
			return err
		}
		all = append(all, d...)
	}
	sort.Slice(all, func(i, j int) bool { return bytes.Compare([]byte(all[i].path), []byte(all[j].path)) < 0 })
	for _, d := range all {
		m, err := DecodeDoc(d.raw)
		if err != nil {
			return err
		}
		if err := fn(d.path, m); err != nil {
			return err
		}
	}
	return nil
}

// CountDocuments counts every document of every file.
func (s *Store) CountDocuments() (int, error) {
	keys, err := s.Files.AllKeys()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, k := range keys {
		h, err := s.Files.handle(k, false)
		if err != nil {
			return 0, err
		}
		if h == nil {
			continue
		}
		tb, err := s.Files.tables(h.db)
		for _, t := range tb {
			var n int
			if err == nil {
				err = h.db.QueryRowContext(bg, "SELECT count(*) FROM "+q(t)).Scan(&n)
			}
			total += n
		}
		s.Files.release(h)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}
