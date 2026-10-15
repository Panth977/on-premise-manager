package devtools

// PostgreSQL (pgx v5, pgxpool max 4): read-only = BEGIN READ ONLY (the server refuses writes), statement_timeout,
// cancel = a cancel request (CancelRequestContextWatcherHandler), EXPLAIN (FORMAT JSON), schema introspection.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// notices routes NOTICE messages of a connection to the execution using it.
var notices sync.Map // *pgconn.PgConn → *[]string

func (s *Service) connectPG(ctx context.Context, rc *resolvedConn) (*client, error) {
	db := rc.database
	if db == "" {
		db = rc.user
	}
	mode := "prefer"
	if rc.def.Options.SSLMode != nil && *rc.def.Options.SSLMode != "" {
		mode = *rc.def.Options.SSLMode
	} else if rc.def.Options.TLS {
		mode = "require"
	}
	u := url.URL{Scheme: "postgres", Host: rc.addr, Path: "/" + db, User: url.UserPassword(rc.user, rc.password)}
	q := url.Values{"sslmode": {mode}, "connect_timeout": {"10"}, "application_name": {"opm-devtools"}}
	u.RawQuery = q.Encode()
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, errors.New(rc.masker.Mask(err.Error()))
	}
	cfg.MaxConns = 4
	cfg.MaxConnIdleTime = 5 * time.Minute
	cc := cfg.ConnConfig
	if cc.TLSConfig != nil {
		if sn := rc.def.Options.TLSServerName; sn != nil && *sn != "" {
			cc.TLSConfig.ServerName = *sn
		}
		if rc.def.Options.TLSInsecure {
			cc.TLSConfig.InsecureSkipVerify = true
		}
		for i := range cc.Fallbacks {
			if cc.Fallbacks[i].TLSConfig != nil && rc.def.Options.TLSInsecure {
				cc.Fallbacks[i].TLSConfig.InsecureSkipVerify = true
			}
		}
	}
	cc.BuildContextWatcherHandler = func(pc *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: pc, CancelRequestDelay: 0, DeadlineDelay: 5 * time.Second}
	}
	cc.OnNotice = func(pc *pgconn.PgConn, n *pgconn.Notice) {
		if v, ok := notices.Load(pc); ok {
			l := v.(*[]string)
			if len(*l) < 200 {
				*l = append(*l, n.Severity+": "+n.Message)
			}
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New(rc.masker.Mask(err.Error()))
	}
	var ver string
	if err := pool.QueryRow(ctx, "SHOW server_version").Scan(&ver); err != nil {
		pool.Close()
		return nil, errors.New(rc.masker.Mask(err.Error()))
	}
	return &client{kind: "postgres", version: "PostgreSQL " + ver, value: pool, close: pool.Close}, nil
}

// pgTxControl: statements a read-only connection refuses (they would leave the read-only transaction).
var pgTxControl = regexp.MustCompile(`(?i)^\s*(BEGIN|START|COMMIT|END|ROLLBACK|ABORT|PREPARE\s+TRANSACTION|DISCARD|RESET\s+(ALL|SESSION|ROLE|default_transaction)|SET\s+(SESSION\s+|LOCAL\s+)?(TRANSACTION|SESSION\s+CHARACTERISTICS|ROLE|SESSION\s+AUTHORIZATION|default_transaction_read_only|transaction_read_only))\b`)

func pgParams(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		switch x := v.(type) {
		case float64:
			if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
				out[i] = int64(x)
			} else {
				out[i] = x
			}
		case map[string]any, []any:
			b, _ := json.Marshal(x)
			out[i] = string(b)
		default:
			out[i] = v
		}
	}
	return out
}

var reTagCount = regexp.MustCompile(`^(INSERT \d+|UPDATE|DELETE|MERGE|COPY|SELECT|MOVE|FETCH)\b.*?(\d+)$`)

func tagRows(tag string) *int64 {
	m := reTagCount.FindStringSubmatch(tag)
	if m == nil {
		return nil
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// shiftPosition maps a server error position (1-based characters of the text sent) to the statement as the operator
// wrote it: minus the EXPLAIN prefix; nil when it falls inside the prefix.
func shiftPosition(pos *int, prefixLen int) *int {
	if pos == nil || prefixLen == 0 {
		return pos
	}
	p := *pos - prefixLen
	if p < 1 {
		return nil
	}
	return &p
}

func pgStmtError(err error) *StatementError {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		se := &StatementError{Message: pe.Message, Code: strOrNil(pe.Code), Detail: strOrNil(pe.Detail), Hint: strOrNil(pe.Hint)}
		if pe.Position > 0 {
			p := int(pe.Position)
			se.Position = &p
		}
		return se
	}
	return &StatementError{Message: err.Error()}
}

// execPG runs the statements on one pooled connection. A connection failure before the first statement ran is
// returned as an error (the pool reconnects once); statement errors are results.
func (s *Service) execPG(ctx context.Context, c Caller, rc *resolvedConn, cl *client, stmts []string, req *ExecuteSQLRequest, pageRows int, timeout time.Duration) ([]StatementResult, error) {
	pool := cl.value.(*pgxpool.Pool)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	pc := conn.Conn().PgConn()
	var nl []string
	notices.Store(pc, &nl)
	defer notices.Delete(pc)
	ms := timeout.Milliseconds()
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET statement_timeout = %d", ms), pgx.QueryExecModeSimpleProtocol); err != nil {
		if isConnErr(err) {
			conn.Conn().Close(context.Background())
			return nil, err
		}
	}
	readOnly := rc.def.ReadOnly
	inTx := readOnly || req.Transaction
	if inTx {
		begin := "BEGIN"
		if readOnly {
			begin = "BEGIN READ ONLY"
		}
		if _, err := conn.Exec(ctx, begin, pgx.QueryExecModeSimpleProtocol); err != nil {
			if isConnErr(err) {
				conn.Conn().Close(context.Background())
			}
			return nil, err
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", ms), pgx.QueryExecModeSimpleProtocol); err != nil {
			_, _ = conn.Exec(context.Background(), "ROLLBACK", pgx.QueryExecModeSimpleProtocol)
			return nil, err
		}
	}
	lim := s.limits()
	results := []StatementResult{}
	failed := false
	for i, st := range stmts {
		sr := StatementResult{Index: i, Statement: TruncateUTF8(st, StatementLogMax).Text, Type: "command", Columns: []ResultColumn{}, Rows: [][]any{}}
		if failed {
			sr.Error = &StatementError{Message: "skipped: an earlier statement failed"}
			results = append(results, sr)
			continue
		}
		start := time.Now()
		nl = nl[:0]
		if readOnly && pgTxControl.MatchString(st) {
			sr.Error = &StatementError{Message: "refused on a read-only connection: transaction / role control would leave the read-only transaction"}
			failed = true
			sr.DurationMs = 0
			results = append(results, sr)
			continue
		}
		sqlText := st
		prefixLen := 0
		if req.Explain != nil && *req.Explain != "" {
			opt := "FORMAT JSON"
			if *req.Explain == "analyze" {
				opt += ", ANALYZE, BUFFERS"
			}
			sqlText = "EXPLAIN (" + opt + ") " + st
			prefixLen = len("EXPLAIN (" + opt + ") ") // ASCII: characters = bytes
		}
		var args []any
		if len(req.Params) > 0 {
			args = pgParams(req.Params)
		} else {
			args = []any{pgx.QueryExecModeSimpleProtocol}
		}
		rows, qerr := conn.Query(ctx, sqlText, args...)
		buf := &rowBuffer{maxRows: lim.BufferRows, maxBytes: lim.MaxResultBytes}
		var cols []ResultColumn
		if qerr == nil {
			tm := conn.Conn().TypeMap()
			for _, fd := range rows.FieldDescriptions() {
				tn := fmt.Sprintf("oid:%d", fd.DataTypeOID)
				if t, ok := tm.TypeForOID(fd.DataTypeOID); ok {
					tn = t.Name
				}
				cols = append(cols, ResultColumn{Name: fd.Name, Type: tn, Display: displayOf(tn)})
			}
			for rows.Next() {
				vals, err := rows.Values()
				if err != nil {
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
			if qerr == nil {
				tag := rows.CommandTag().String()
				sr.CommandTag = strOrNil(tag)
				if len(cols) == 0 {
					sr.RowsAffected = tagRows(tag)
				}
			}
		}
		sr.DurationMs = time.Since(start).Milliseconds()
		if len(nl) > 0 {
			sr.Notices = append([]string(nil), nl...)
		}
		if qerr != nil {
			if i == 0 && isConnErr(qerr) && ctx.Err() == nil {
				conn.Conn().Close(context.Background())
				return nil, qerr
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				sr.Error = &StatementError{Message: fmt.Sprintf("timed out after %s (canceled)", timeout)}
			} else {
				sr.Error = pgStmtError(qerr)
				sr.Error.Position = shiftPosition(sr.Error.Position, prefixLen)
			}
			failed = true
			results = append(results, sr)
			continue
		}
		sr.OK = true
		if req.Explain != nil && *req.Explain != "" {
			sr.Type = "explain"
			var plan any
			if len(buf.rows) > 0 && len(buf.rows[0]) > 0 {
				switch v := buf.rows[0][0].(type) {
				case string:
					_ = json.Unmarshal([]byte(v), &plan)
				default:
					plan = v
				}
			}
			sr.Plan = &Plan{Text: renderPGPlan(plan), JSON: plan}
			sr.Columns = cols
			sr.Rows = [][]any{}
		} else if len(cols) > 0 {
			sr.Type = "rows"
			s.finishRows(&sr, c, cols, buf, pageRows)
		}
		results = append(results, sr)
	}
	if inTx {
		end := "COMMIT"
		if failed {
			end = "ROLLBACK"
		}
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if _, err := conn.Exec(cctx, end, pgx.QueryExecModeSimpleProtocol); err != nil && !failed {
			results = append(results, StatementResult{Index: len(stmts), Statement: end, Type: "command", Columns: []ResultColumn{}, Rows: [][]any{}, Error: pgStmtError(err)})
		}
		cancel()
	}
	return results, nil
}

// renderPGPlan renders an EXPLAIN (FORMAT JSON) plan as text (one execution gives both forms).
func renderPGPlan(plan any) string {
	arr, _ := plan.([]any)
	if len(arr) == 0 {
		return ""
	}
	top, _ := arr[0].(map[string]any)
	var b strings.Builder
	var walk func(n map[string]any, depth int)
	num := func(v any) string {
		if f, ok := v.(float64); ok {
			if f == math.Trunc(f) {
				return strconv.FormatFloat(f, 'f', 0, 64)
			}
			return strconv.FormatFloat(f, 'f', 2, 64)
		}
		return fmt.Sprint(v)
	}
	walk = func(n map[string]any, depth int) {
		ind := strings.Repeat("  ", depth)
		if depth > 0 {
			ind = strings.Repeat("  ", depth-1) + "->  "
		}
		line := fmt.Sprint(n["Node Type"])
		if rel, ok := n["Relation Name"]; ok {
			line += " on " + fmt.Sprint(rel)
			if al, ok := n["Alias"]; ok && al != rel {
				line += " " + fmt.Sprint(al)
			}
		}
		if idx, ok := n["Index Name"]; ok {
			line += " using " + fmt.Sprint(idx)
		}
		line += fmt.Sprintf("  (cost=%s..%s rows=%s width=%s)", num(n["Startup Cost"]), num(n["Total Cost"]), num(n["Plan Rows"]), num(n["Plan Width"]))
		if at, ok := n["Actual Total Time"]; ok {
			line += fmt.Sprintf(" (actual time=%s..%s rows=%s loops=%s)", num(n["Actual Startup Time"]), num(at), num(n["Actual Rows"]), num(n["Actual Loops"]))
		}
		b.WriteString(ind + line + "\n")
		for _, k := range []string{"Filter", "Index Cond", "Hash Cond", "Join Filter", "Sort Key", "Group Key"} {
			if v, ok := n[k]; ok {
				b.WriteString(strings.Repeat("  ", depth+1) + k + ": " + fmt.Sprint(v) + "\n")
			}
		}
		if kids, ok := n["Plans"].([]any); ok {
			for _, k := range kids {
				if m, ok := k.(map[string]any); ok {
					walk(m, depth+1)
				}
			}
		}
	}
	if p, ok := top["Plan"].(map[string]any); ok {
		walk(p, 0)
	}
	for _, k := range []string{"Planning Time", "Execution Time"} {
		if v, ok := top[k]; ok {
			b.WriteString(fmt.Sprintf("%s: %s ms\n", k, num(v)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ───────────────────────── schema

func quoteIdent(s string) string {
	if regexp.MustCompile(`^[a-z_][a-z0-9_]*$`).MatchString(s) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

var pgRelKind = map[string]string{"r": "table", "v": "view", "m": "materialized-view", "f": "foreign-table", "p": "partitioned-table"}

func (s *Service) schemaPG(ctx context.Context, cl *client) (*SchemaResponse, error) {
	pool := cl.value.(*pgxpool.Pool)
	out := &SchemaResponse{Kind: "postgres", ServerVersion: strOrNil(cl.version), Namespaces: []SchemaNamespace{}}
	var dbName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		return nil, err
	}
	out.Database = &dbName
	nsIdx := map[string]int{}
	rows, err := pool.Query(ctx, `SELECT nspname, pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname NOT LIKE 'pg_toast%' AND nspname NOT LIKE 'pg_temp%' ORDER BY nspname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n, owner string
		if rows.Scan(&n, &owner) == nil {
			nsIdx[n] = len(out.Namespaces)
			out.Namespaces = append(out.Namespaces, SchemaNamespace{Name: n, System: n == "pg_catalog" || n == "information_schema", Tables: []SchemaTable{}, Functions: []SchemaFunction{}, Meta: map[string]string{"owner": owner}})
		}
	}
	rows.Close()
	type tkey struct{ ns, t string }
	tIdx := map[tkey][2]int{}
	rows, err = pool.Query(ctx, `SELECT n.nspname, c.relname, c.relkind::text, c.reltuples::float8, pg_total_relation_size(c.oid), obj_description(c.oid, 'pg_class')
	  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	  WHERE c.relkind IN ('r','v','m','f','p') AND NOT c.relispartition AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%'
	  ORDER BY n.nspname, c.relname LIMIT 2001`)
	if err != nil {
		return nil, err
	}
	count := 0
	for rows.Next() {
		var ns, name, kind string
		var est float64
		var size int64
		var comment *string
		if rows.Scan(&ns, &name, &kind, &est, &size, &comment) != nil {
			continue
		}
		count++
		if count > 2000 {
			out.Truncated = true
			break
		}
		ni, ok := nsIdx[ns]
		if !ok {
			continue
		}
		t := SchemaTable{Name: name, Kind: pgRelKind[kind], Columns: []SchemaColumn{}, Comment: comment}
		if kind == "r" || kind == "m" || kind == "p" {
			if est < 0 {
				est = 0
			}
			e := est
			sz := size
			t.RowEstimate, t.SizeBytes = &e, &sz
		}
		tIdx[tkey{ns, name}] = [2]int{ni, len(out.Namespaces[ni].Tables)}
		out.Namespaces[ni].Tables = append(out.Namespaces[ni].Tables, t)
	}
	rows.Close()
	rows, err = pool.Query(ctx, `SELECT n.nspname, c.relname, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid),
	    EXISTS (SELECT 1 FROM pg_index x WHERE x.indrelid = c.oid AND x.indisprimary AND a.attnum = ANY(x.indkey)), col_description(c.oid, a.attnum)
	  FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
	  LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
	  WHERE a.attnum > 0 AND NOT a.attisdropped AND c.relkind IN ('r','v','m','f','p') AND NOT c.relispartition
	    AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%'
	  ORDER BY n.nspname, c.relname, a.attnum LIMIT 20001`)
	if err != nil {
		return nil, err
	}
	count = 0
	for rows.Next() {
		var ns, t string
		var col SchemaColumn
		if rows.Scan(&ns, &t, &col.Name, &col.Type, &col.Nullable, &col.Default, &col.PrimaryKey, &col.Comment) != nil {
			continue
		}
		count++
		if count > 20000 {
			out.Truncated = true
			break
		}
		if ix, ok := tIdx[tkey{ns, t}]; ok {
			tb := &out.Namespaces[ix[0]].Tables[ix[1]]
			tb.Columns = append(tb.Columns, col)
		}
	}
	rows.Close()
	rows, err = pool.Query(ctx, `SELECT n.nspname, p.proname, p.prokind::text, pg_get_function_identity_arguments(p.oid), coalesce(pg_get_function_result(p.oid), ''),
	    p.prorettype = 'trigger'::regtype
	  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
	  WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%' ORDER BY 1, 2 LIMIT 5000`)
	if err == nil {
		for rows.Next() {
			var ns, name, kind, args, ret string
			var trig bool
			if rows.Scan(&ns, &name, &kind, &args, &ret, &trig) != nil {
				continue
			}
			k := map[string]string{"f": "function", "p": "procedure", "a": "aggregate", "w": "function"}[kind]
			if trig {
				k = "trigger"
			}
			if ni, ok := nsIdx[ns]; ok {
				out.Namespaces[ni].Functions = append(out.Namespaces[ni].Functions, SchemaFunction{Name: name, Kind: k, Arguments: args, Returns: ret})
			}
		}
		rows.Close()
	}
	return out, nil
}

func (s *Service) tableDetailPG(ctx context.Context, cl *client, ns, table string) (*TableDetailResponse, error) {
	pool := cl.value.(*pgxpool.Pool)
	var oid uint32
	var kind string
	var est float64
	var size int64
	err := pool.QueryRow(ctx, `SELECT c.oid, c.relkind::text, c.reltuples::float8, pg_total_relation_size(c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`, ns, table).
		Scan(&oid, &kind, &est, &size)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("table %s.%s does not exist", ns, table)
		}
		return nil, err
	}
	out := &TableDetailResponse{Namespace: ns, Table: table, Columns: []SchemaColumn{}, Indexes: []SchemaIndex{}, Constraints: []Constraint{}}
	if kind == "r" || kind == "m" || kind == "p" {
		if est < 0 {
			est = 0
		}
		out.RowEstimate, out.SizeBytes = &est, &size
	}
	rows, err := pool.Query(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid),
	    EXISTS (SELECT 1 FROM pg_index x WHERE x.indrelid = a.attrelid AND x.indisprimary AND a.attnum = ANY(x.indkey)), col_description(a.attrelid, a.attnum)
	  FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
	  WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var col SchemaColumn
		if rows.Scan(&col.Name, &col.Type, &col.Nullable, &col.Default, &col.PrimaryKey, &col.Comment) == nil {
			out.Columns = append(out.Columns, col)
		}
	}
	rows.Close()
	rows, err = pool.Query(ctx, `SELECT i.relname, pg_get_indexdef(x.indexrelid), x.indisunique, x.indisprimary FROM pg_index x JOIN pg_class i ON i.oid = x.indexrelid WHERE x.indrelid = $1 ORDER BY i.relname`, oid)
	if err == nil {
		for rows.Next() {
			var ix SchemaIndex
			if rows.Scan(&ix.Name, &ix.Definition, &ix.Unique, &ix.Primary) == nil {
				out.Indexes = append(out.Indexes, ix)
			}
		}
		rows.Close()
	}
	conTypes := map[string]string{"p": "PRIMARY KEY", "f": "FOREIGN KEY", "u": "UNIQUE", "c": "CHECK", "x": "EXCLUDE", "t": "TRIGGER", "n": "NOT NULL"}
	rows, err = pool.Query(ctx, `SELECT conname, contype::text, pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = $1 ORDER BY contype, conname`, oid)
	if err == nil {
		for rows.Next() {
			var cn Constraint
			var t string
			if rows.Scan(&cn.Name, &t, &cn.Definition) == nil {
				cn.Type = conTypes[t]
				if cn.Type == "" {
					cn.Type = t
				}
				out.Constraints = append(out.Constraints, cn)
			}
		}
		rows.Close()
	}
	full := quoteIdent(ns) + "." + quoteIdent(table)
	var ddl string
	switch kind {
	case "v", "m":
		var def string
		if pool.QueryRow(ctx, `SELECT pg_get_viewdef($1::oid, true)`, oid).Scan(&def) == nil {
			w := "VIEW"
			if kind == "m" {
				w = "MATERIALIZED VIEW"
			}
			ddl = fmt.Sprintf("CREATE %s %s AS\n%s", w, full, strings.TrimSpace(def))
		}
	default:
		var lines []string
		for _, col := range out.Columns {
			l := "  " + quoteIdent(col.Name) + " " + col.Type
			if !col.Nullable {
				l += " NOT NULL"
			}
			if col.Default != nil {
				l += " DEFAULT " + *col.Default
			}
			lines = append(lines, l)
		}
		for _, cn := range out.Constraints {
			if cn.Type == "NOT NULL" {
				continue
			}
			lines = append(lines, "  CONSTRAINT "+quoteIdent(cn.Name)+" "+cn.Definition)
		}
		ddl = "CREATE TABLE " + full + " (\n" + strings.Join(lines, ",\n") + "\n);"
		for _, ix := range out.Indexes {
			isCon := false
			for _, cn := range out.Constraints {
				if cn.Name == ix.Name {
					isCon = true
				}
			}
			if !isCon {
				ddl += "\n" + ix.Definition + ";"
			}
		}
	}
	if ddl != "" {
		out.DDL = &ddl
	}
	return out, nil
}
