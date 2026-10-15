package devtools

// SQLite (modernc, pure Go): a file in a declared site volume or a container mount. read-only = mode=ro +
// PRAGMA query_only; ATTACH / VACUUM INTO only to files inside the same volume (rewritten to the absolute path).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func sqliteDSN(file string, readOnly bool) string {
	u := (&url.URL{Path: file}).EscapedPath()
	q := "?_pragma=busy_timeout(5000)"
	if readOnly {
		q += "&mode=ro&_pragma=query_only(1)"
	} else {
		q += "&mode=rw"
	}
	return "file:" + u + q
}

func (s *Service) connectSQLite(ctx context.Context, rc *resolvedConn) (*client, error) {
	if rc.file == "" {
		return nil, errors.New("no SQLite file")
	}
	st, err := os.Stat(rc.file)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", rc.targetText, err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a SQLite file", rc.targetText)
	}
	db, err := sql.Open("sqlite", sqliteDSN(rc.file, rc.def.ReadOnly))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	var ver string
	if err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&ver); err != nil {
		db.Close()
		return nil, err
	}
	// a file that is not a database fails here, not at the first query
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&n); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", rc.targetText, err)
	}
	return &client{kind: "sqlite", version: "SQLite " + ver, value: db, close: func() { db.Close() }}, nil
}

var (
	reAttach     = regexp.MustCompile(`(?is)^(\s*ATTACH\s+(?:DATABASE\s+)?)('(?:[^']|'')*'|"(?:[^"]|"")*")(\s+AS\s+.*)$`)
	reAttachAny  = regexp.MustCompile(`(?is)^\s*ATTACH\b`)
	reVacuumInto = regexp.MustCompile(`(?is)^(\s*VACUUM(?:\s+\S+)?\s+INTO\s+)('(?:[^']|'')*'|"(?:[^"]|"")*")(\s*)$`)
	reVacuumAny  = regexp.MustCompile(`(?is)^\s*VACUUM\b.*\bINTO\b`)
	reReturning  = regexp.MustCompile(`(?i)\bRETURNING\b`)
)

func sqlUnquote(q string) string {
	ch := q[:1]
	return strings.ReplaceAll(q[1:len(q)-1], ch+ch, ch)
}

func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// confineFile resolves a file of ATTACH / VACUUM INTO inside root (relative = next to the database file); the parents
// must exist and none may be a symlink.
func confineFile(rc *resolvedConn, p string) (string, error) {
	if p == "" || p == ":memory:" {
		return p, nil
	}
	if strings.HasPrefix(strings.ToLower(p), "file:") {
		return "", errors.New("URI file names are refused: use a path inside the same volume")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(rc.file), p)
	}
	p = filepath.Clean(p)
	root := filepath.Clean(rc.root)
	if !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the volume of this database (%s)", p, root)
	}
	rel := strings.TrimPrefix(p, root+string(filepath.Separator))
	cur := root
	parts := strings.Split(rel, string(filepath.Separator))
	for i, seg := range parts {
		cur = filepath.Join(cur, seg)
		st, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) && i == len(parts)-1 {
				return p, nil
			}
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s goes through a symlink (refused)", rel)
		}
	}
	return p, nil
}

// sqliteRewrite applies the ATTACH / VACUUM INTO confinement ("" error = ok).
func sqliteRewrite(rc *resolvedConn, st string) (string, error) {
	if m := reAttach.FindStringSubmatch(st); m != nil {
		p, err := confineFile(rc, sqlUnquote(m[2]))
		if err != nil {
			return "", err
		}
		return m[1] + sqlQuote(p) + m[3], nil
	}
	if reAttachAny.MatchString(st) {
		return "", errors.New("ATTACH needs a quoted file name: ATTACH 'other.db' AS x (a file inside the same volume)")
	}
	if m := reVacuumInto.FindStringSubmatch(st); m != nil {
		p, err := confineFile(rc, sqlUnquote(m[2]))
		if err != nil {
			return "", err
		}
		return m[1] + sqlQuote(p) + m[3], nil
	}
	if reVacuumAny.MatchString(st) {
		return "", errors.New("VACUUM INTO needs a quoted file name inside the same volume")
	}
	return st, nil
}

func (s *Service) execSQLite(ctx context.Context, c Caller, rc *resolvedConn, cl *client, stmts []string, req *ExecuteSQLRequest, pageRows int) ([]StatementResult, error) {
	db := cl.value.(*sql.DB)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	lim := s.limits()
	results := []StatementResult{}
	failed := false
	if req.Transaction {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return nil, err
		}
	}
	for i, st := range stmts {
		sr := StatementResult{Index: i, Statement: TruncateUTF8(st, StatementLogMax).Text, Type: "command", Columns: []ResultColumn{}, Rows: [][]any{}}
		if failed {
			sr.Error = &StatementError{Message: "skipped: an earlier statement failed"}
			results = append(results, sr)
			continue
		}
		start := time.Now()
		run, rerr := sqliteRewrite(rc, st)
		if rerr != nil {
			sr.Error = &StatementError{Message: rerr.Error()}
			failed = true
			results = append(results, sr)
			continue
		}
		explain := req.Explain != nil && *req.Explain != ""
		if explain {
			run = "EXPLAIN QUERY PLAN " + run
		}
		cls := ClassifyStatement("sqlite", st)
		params := make([]any, len(req.Params))
		for j, p := range req.Params {
			params[j] = pgParams([]any{p})[0]
		}
		useQuery := explain || !cls.Write || reReturning.MatchString(st) || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(StripSQLComments(st))), "PRAGMA")
		buf := &rowBuffer{maxRows: lim.BufferRows, maxBytes: lim.MaxResultBytes}
		var cols []ResultColumn
		var qerr error
		if useQuery {
			rows, err := conn.QueryContext(ctx, run, params...)
			if err != nil {
				qerr = err
			} else {
				cts, _ := rows.ColumnTypes()
				for _, ct := range cts {
					tn := ct.DatabaseTypeName()
					if tn == "" {
						tn = "any"
					}
					cols = append(cols, ResultColumn{Name: ct.Name(), Type: strings.ToLower(tn), Display: displayOf(tn)})
				}
				for rows.Next() {
					vals := make([]any, len(cols))
					ptrs := make([]any, len(cols))
					for j := range vals {
						ptrs[j] = &vals[j]
					}
					if err := rows.Scan(ptrs...); err != nil {
						qerr = err
						break
					}
					row := make([]any, len(vals))
					for j, v := range vals {
						row[j] = normValue(v)
					}
					if !buf.add(row) {
						break
					}
				}
				rows.Close()
				if qerr == nil {
					qerr = rows.Err()
				}
			}
		} else {
			res, err := conn.ExecContext(ctx, run, params...)
			if err != nil {
				qerr = err
			} else if n, err := res.RowsAffected(); err == nil {
				sr.RowsAffected = &n
			}
		}
		sr.DurationMs = time.Since(start).Milliseconds()
		if qerr != nil {
			msg := qerr.Error()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				msg = "timed out (interrupted): " + msg
			}
			sr.Error = &StatementError{Message: msg}
			failed = true
			results = append(results, sr)
			continue
		}
		sr.OK = true
		word := strings.ToUpper(firstWord(st))
		sr.CommandTag = strOrNil(word)
		if explain {
			sr.Type = "explain"
			sr.Plan = sqlitePlan(buf.rows)
		} else if len(cols) > 0 {
			sr.Type = "rows"
			s.finishRows(&sr, c, cols, buf, pageRows)
			tag := fmt.Sprintf("%s %d", word, sr.TotalRows)
			sr.CommandTag = &tag
		} else if sr.RowsAffected != nil && (word == "INSERT" || word == "UPDATE" || word == "DELETE" || word == "REPLACE") {
			tag := fmt.Sprintf("%s %d", word, *sr.RowsAffected)
			sr.CommandTag = &tag
		}
		results = append(results, sr)
	}
	if req.Transaction {
		end := "COMMIT"
		if failed {
			end = "ROLLBACK"
		}
		if _, err := conn.ExecContext(context.Background(), end); err != nil && !failed {
			results = append(results, StatementResult{Index: len(stmts), Statement: end, Type: "command", Columns: []ResultColumn{}, Rows: [][]any{}, Error: &StatementError{Message: err.Error()}})
		}
	}
	return results, nil
}

func firstWord(st string) string {
	f := strings.Fields(StripSQLComments(st))
	if len(f) == 0 {
		return ""
	}
	w := f[0]
	if i := strings.IndexAny(w, "(;"); i > 0 {
		w = w[:i]
	}
	return w
}

// sqlitePlan renders EXPLAIN QUERY PLAN rows (id, parent, notused, detail) as a tree.
func sqlitePlan(rows [][]any) *Plan {
	type node struct {
		id, parent int64
		detail     string
	}
	var nodes []node
	var js []any
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		id, _ := toInt64(r[0])
		parent, _ := toInt64(r[1])
		d := valueText(r[3])
		nodes = append(nodes, node{id, parent, d})
		js = append(js, map[string]any{"id": id, "parent": parent, "detail": d})
	}
	depth := map[int64]int{0: -1}
	var b strings.Builder
	b.WriteString("QUERY PLAN\n")
	for _, n := range nodes {
		d := depth[n.parent] + 1
		depth[n.id] = d
		b.WriteString(strings.Repeat("   ", d) + "`--" + n.detail + "\n")
	}
	if js == nil {
		js = []any{}
	}
	return &Plan{Text: strings.TrimRight(b.String(), "\n"), JSON: js}
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

func sqliteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func (s *Service) schemaSQLite(ctx context.Context, rc *resolvedConn, cl *client) (*SchemaResponse, error) {
	db := cl.value.(*sql.DB)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	out := &SchemaResponse{Kind: "sqlite", ServerVersion: strOrNil(cl.version), Database: strOrNil(rc.targetText), Namespaces: []SchemaNamespace{}}
	type dbi struct{ name, file string }
	var dbs []dbi
	rows, err := conn.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var seq int
		var name string
		var file sql.NullString
		if rows.Scan(&seq, &name, &file) == nil {
			dbs = append(dbs, dbi{name, file.String})
		}
	}
	rows.Close()
	total, totalCols := 0, 0
	for _, d := range dbs {
		ns := SchemaNamespace{Name: d.name, System: d.name == "temp", Tables: []SchemaTable{}, Functions: []SchemaFunction{}, Meta: map[string]string{}}
		if d.file != "" {
			ns.Meta["file"] = filepath.Base(d.file)
		}
		type tb struct{ name, typ string }
		var tbs []tb
		r2, err := conn.QueryContext(ctx, "SELECT name, type FROM "+sqliteIdent(d.name)+".sqlite_master WHERE type IN ('table','view') ORDER BY name")
		if err != nil {
			continue
		}
		for r2.Next() {
			var t tb
			if r2.Scan(&t.name, &t.typ) == nil {
				tbs = append(tbs, t)
			}
		}
		r2.Close()
		for _, t := range tbs {
			total++
			if total > 2000 {
				out.Truncated = true
				break
			}
			kind := "table"
			if t.typ == "view" {
				kind = "view"
			}
			st := SchemaTable{Name: t.name, Kind: kind, Columns: []SchemaColumn{}}
			cols, _ := sqliteColumns(ctx, conn, d.name, t.name)
			totalCols += len(cols)
			if totalCols > 20000 {
				out.Truncated = true
				break
			}
			st.Columns = cols
			ns.Tables = append(ns.Tables, st)
		}
		out.Namespaces = append(out.Namespaces, ns)
	}
	return out, nil
}

func sqliteColumns(ctx context.Context, conn *sql.Conn, dbName, table string) ([]SchemaColumn, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA "+sqliteIdent(dbName)+".table_xinfo("+sqliteIdent(table)+")")
	if err != nil {
		return []SchemaColumn{}, err
	}
	out := []SchemaColumn{}
	for rows.Next() {
		var cid, notnull, pk, hidden int
		var name string
		var typ, dflt sql.NullString
		if rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk, &hidden) != nil {
			continue
		}
		col := SchemaColumn{Name: name, Type: typ.String, Nullable: notnull == 0, PrimaryKey: pk > 0}
		if dflt.Valid {
			d := dflt.String
			col.Default = &d
		}
		out = append(out, col)
	}
	rows.Close()
	return out, nil
}

func (s *Service) tableDetailSQLite(ctx context.Context, cl *client, ns, table string) (*TableDetailResponse, error) {
	db := cl.value.(*sql.DB)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if ns == "" {
		ns = "main"
	}
	var ddl sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT sql FROM "+sqliteIdent(ns)+".sqlite_master WHERE name = ? AND type IN ('table','view')", table).Scan(&ddl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("table %s.%s does not exist", ns, table)
		}
		return nil, err
	}
	out := &TableDetailResponse{Namespace: ns, Table: table, Indexes: []SchemaIndex{}, Constraints: []Constraint{}}
	out.Columns, _ = sqliteColumns(ctx, conn, ns, table)
	if ddl.Valid {
		d := ddl.String
		out.DDL = &d
	}
	type ix struct {
		name   string
		unique bool
		origin string
	}
	var ixs []ix
	rows, err := conn.QueryContext(ctx, "PRAGMA "+sqliteIdent(ns)+".index_list("+sqliteIdent(table)+")")
	if err == nil {
		for rows.Next() {
			var seq, uniq, partial int
			var name, origin string
			if rows.Scan(&seq, &name, &uniq, &origin, &partial) == nil {
				ixs = append(ixs, ix{name, uniq == 1, origin})
			}
		}
		rows.Close()
	}
	for _, x := range ixs {
		var def sql.NullString
		_ = conn.QueryRowContext(ctx, "SELECT sql FROM "+sqliteIdent(ns)+".sqlite_master WHERE type = 'index' AND name = ?", x.name).Scan(&def)
		d := def.String
		if !def.Valid {
			d = fmt.Sprintf("(automatic index for %s)", map[string]string{"pk": "PRIMARY KEY", "u": "UNIQUE"}[x.origin])
		}
		out.Indexes = append(out.Indexes, SchemaIndex{Name: x.name, Definition: d, Unique: x.unique, Primary: x.origin == "pk"})
	}
	rows, err = conn.QueryContext(ctx, "PRAGMA "+sqliteIdent(ns)+".foreign_key_list("+sqliteIdent(table)+")")
	if err == nil {
		for rows.Next() {
			var id, seq int
			var tbl, from string
			var to sql.NullString
			var onUpd, onDel, match string
			if rows.Scan(&id, &seq, &tbl, &from, &to, &onUpd, &onDel, &match) == nil {
				out.Constraints = append(out.Constraints, Constraint{Name: fmt.Sprintf("fk_%d_%d", id, seq), Type: "FOREIGN KEY",
					Definition: fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s) ON UPDATE %s ON DELETE %s", from, tbl, to.String, onUpd, onDel)})
			}
		}
		rows.Close()
	}
	return out, nil
}
