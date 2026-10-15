package devtools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/model"
)

// queryLog is the data of one devtools.query activity (TS DevToolQueryActivity).
type queryLog struct {
	d            *connDef
	target       string
	op           string
	statement    Truncated
	statements   int
	start        time.Time
	rows         *int64
	rowsAffected *int64
	status       *int
	ok           bool
	errText      string
	destructive  bool
	confirmed    bool
	queryID      string
}

func (s *Service) logQuery(c Caller, q *queryLog, m *Masker) *string {
	var errV any
	if q.errText != "" {
		errV = LogError(q.errText, m)
	}
	data := map[string]any{"siteSlug": s.h.Site(), "connectionId": q.d.ID, "connectionName": q.d.Name, "kind": q.d.Kind, "source": q.d.Source,
		"target": q.target, "op": q.op, "statement": q.statement.Text, "statementTruncated": q.statement.Truncated, "statements": q.statements,
		"durationMs": time.Since(q.start).Milliseconds(), "rows": q.rows, "rowsAffected": q.rowsAffected, "status": q.status, "ok": q.ok, "error": errV,
		"readOnly": q.d.ReadOnly, "destructive": q.destructive, "confirmed": q.confirmed, "queryId": m.Mask(q.queryID)}
	res := "ok"
	if !q.ok {
		res = "failed"
	}
	first := strings.SplitN(q.statement.Text, "\n", 2)[0]
	if len(first) > 120 {
		first = TruncateUTF8(first, 120).Text
	}
	return s.activity(c, "devtools.query", fmt.Sprintf("dev tools: %s on %q → %s: %s", q.op, q.d.Name, res, first), data)
}

func (s *Service) connOfKind(id string, kinds ...string) (*connDef, *Error) {
	d, e := s.findConn(id)
	if e != nil {
		return nil, e
	}
	if !inList(kinds, d.Kind) {
		return nil, invalid("%q is a %s connection (this action needs %s)", d.Name, d.Kind, strings.Join(kinds, " / "))
	}
	return d, nil
}

func asError(err error) *Error {
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	return nil
}

// ───────────────────────── SQL / CQL

func (s *Service) handleExecuteSQL(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.connOfKind(r.PathValue("connectionId"), "postgres", "sqlite", "cassandra")
	if e != nil {
		return nil, 0, e
	}
	var req ExecuteSQLRequest
	if e := readBody(r, &req, 4<<20); e != nil {
		return nil, 0, e
	}
	stmts := SplitSQLStatements(req.Statement)
	if len(stmts) == 0 {
		return nil, 0, invalid("statement is empty")
	}
	if len(stmts) > 500 {
		return nil, 0, invalid("at most 500 statements per execution")
	}
	if len(req.Params) > 0 && len(stmts) > 1 {
		return nil, 0, invalid("params need exactly one statement (got %d)", len(stmts))
	}
	explain := req.Explain != nil && *req.Explain != ""
	if explain {
		if d.Kind == "cassandra" {
			return nil, 0, invalid("Cassandra has no EXPLAIN (use TRACING in cqlsh)")
		}
		if *req.Explain != "plan" && *req.Explain != "analyze" {
			return nil, 0, invalid("explain must be plan | analyze")
		}
		if len(stmts) > 1 {
			return nil, 0, invalid("explain needs exactly one statement")
		}
	}
	var reasons []string
	write := false
	for _, st := range stmts {
		cls := ClassifyStatement(d.Kind, st)
		if explain && d.Kind == "sqlite" {
			cls = StatementClass{Reasons: []string{}} // EXPLAIN QUERY PLAN never runs the statement
		}
		write = write || cls.Write
		for _, rs := range cls.Reasons {
			if !inList(reasons, rs) {
				reasons = append(reasons, rs)
			}
		}
	}
	if d.Kind == "cassandra" && d.ReadOnly && write {
		return nil, 0, forbidden("%q is read-only: write statements are refused", d.Name)
	}
	destructive := len(reasons) > 0
	if destructive && !ConfirmOk(d.Name, req.Confirm) {
		return nil, 0, confirmRequired(d.Name, reasons)
	}
	if e := s.allow("execute", c); e != nil {
		return nil, 0, e
	}
	op := map[string]string{"postgres": "sql", "sqlite": "sql", "cassandra": "cql"}[d.Kind]
	if explain {
		op = "explain"
	}
	ql := &queryLog{d: d, op: op, statements: len(stmts), start: time.Now(), destructive: destructive, confirmed: destructive, queryID: req.QueryID, target: targetText(d.Target)}
	masker := NewMasker(s.h.SecretValues())
	ql.statement = LogStatement(d.Kind, strings.Join(stmts, ";\n"), masker)
	rc, e := s.resolve(r.Context(), d)
	if e != nil {
		ql.errText = e.Msg
		s.logQuery(c, ql, masker)
		return nil, 0, e
	}
	masker = rc.masker
	ql.statement = LogStatement(d.Kind, strings.Join(stmts, ";\n"), masker)
	ql.target = rc.targetText
	s.noteTarget(d.ID, rc.targetText)
	timeout := s.timeoutFor(req.TimeoutSec, rc)
	pageRows := s.limits().MaxRows
	if d.Options.MaxRows != nil && *d.Options.MaxRows >= 1 {
		pageRows = int(*d.Options.MaxRows)
	}
	if req.MaxRows != nil && *req.MaxRows >= 1 && *req.MaxRows <= 10000 {
		pageRows = *req.MaxRows
	}
	ctx, done, e := s.begin(r.Context(), c, d.ID, req.QueryID, timeout)
	if e != nil {
		return nil, 0, e
	}
	defer done()
	if ql.queryID == "" {
		ql.queryID = req.QueryID
	}
	var results []StatementResult
	_, err := s.withClient(ctx, rc, func(cl *client) error {
		var err error
		switch d.Kind {
		case "postgres":
			results, err = s.execPG(ctx, c, rc, cl, stmts, &req, pageRows, timeout)
		case "sqlite":
			results, err = s.execSQLite(ctx, c, rc, cl, stmts, &req, pageRows)
		case "cassandra":
			results, err = s.execCQL(ctx, c, rc, cl, stmts, &req, pageRows)
		}
		return err
	})
	if err != nil {
		if de := asError(err); de != nil {
			return nil, 0, de
		}
		msg := masker.Mask(err.Error())
		if errors.Is(ctx.Err(), context.Canceled) {
			msg = "cancelled: " + msg
		}
		ql.errText = msg
		s.logQuery(c, ql, masker)
		return nil, 0, unavailable("%s: %s", d.Name, msg)
	}
	ok := true
	var rows, affected int64
	hasRows, hasAff := false, false
	for i := range results {
		sr := &results[i]
		if !sr.OK {
			ok = false
			if ql.errText == "" && sr.Error != nil {
				ql.errText = sr.Error.Message
			}
			if sr.Error != nil {
				sr.Error.Message = masker.Mask(sr.Error.Message)
			}
		}
		if sr.Type == "rows" {
			rows += int64(sr.TotalRows)
			hasRows = true
		}
		if sr.RowsAffected != nil {
			affected += *sr.RowsAffected
			hasAff = true
		}
	}
	if hasRows {
		ql.rows = &rows
	}
	if hasAff {
		ql.rowsAffected = &affected
	}
	ql.ok = ok
	actID := s.logQuery(c, ql, masker)
	return ExecuteResponse{QueryID: req.QueryID, ConnectionID: d.ID, OK: ok, DurationMs: time.Since(ql.start).Milliseconds(), Results: results, ActivityID: actID}, 200, nil
}

// ───────────────────────── Redis

func (s *Service) handleExecuteRedis(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.connOfKind(r.PathValue("connectionId"), "redis")
	if e != nil {
		return nil, 0, e
	}
	var req ExecuteRedisRequest
	if e := readBody(r, &req, 4<<20); e != nil {
		return nil, 0, e
	}
	argv := req.Command
	if len(argv) == 0 && req.Line != nil {
		a, ok := ParseRedisCommandLine(*req.Line)
		if !ok {
			return nil, 0, invalid("the command line has an unterminated quote")
		}
		argv = a
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, 0, invalid("command is empty")
	}
	cls := ClassifyRedis(argv)
	name := RedisCommandName(argv)
	if cls.Blocked {
		return nil, 0, &Error{Status: 422, Code: model.ErrInvalid, Msg: fmt.Sprintf("%s is not allowed in the dev tools (it blocks or hijacks the pooled connection, or administers the server)", name),
			Details: map[string]any{"blocked": true, "command": name}}
	}
	if d.ReadOnly && cls.Write {
		return nil, 0, forbidden("%q is read-only: %s is a write command", d.Name, name)
	}
	if cls.Destructive && !ConfirmOk(d.Name, req.Confirm) {
		return nil, 0, confirmRequired(d.Name, cls.Reasons)
	}
	if e := s.allow("execute", c); e != nil {
		return nil, 0, e
	}
	masker := NewMasker(s.h.SecretValues())
	redLine := func(m *Masker) Truncated {
		return TruncateUTF8(m.Mask(strings.Join(quoteRedisArgv(RedactRedisArgv(argv)), " ")), StatementLogMax)
	}
	ql := &queryLog{d: d, op: "redis", statements: 1, start: time.Now(), destructive: cls.Destructive, confirmed: cls.Destructive, queryID: req.QueryID, target: targetText(d.Target), statement: redLine(masker)}
	rc, e := s.resolve(r.Context(), d)
	if e != nil {
		ql.errText = e.Msg
		s.logQuery(c, ql, masker)
		return nil, 0, e
	}
	masker = rc.masker
	ql.statement, ql.target = redLine(masker), rc.targetText
	s.noteTarget(d.ID, rc.targetText)
	ctx, done, e := s.begin(r.Context(), c, d.ID, req.QueryID, s.timeoutFor(req.TimeoutSec, rc))
	if e != nil {
		return nil, 0, e
	}
	defer done()
	var val any
	var cmdErr error
	_, err := s.withClient(ctx, rc, func(cl *client) error {
		v, err := s.execRedis(ctx, cl, argv)
		val, cmdErr = v, nil
		if err != nil {
			if isConnErr(err) {
				return err
			}
			cmdErr = err
		}
		return nil
	})
	out := ExecuteRedisResponse{QueryID: req.QueryID, Command: ql.statement.Text}
	if err != nil {
		ql.errText = masker.Mask(err.Error())
		s.logQuery(c, ql, masker)
		return nil, 0, unavailable("%s: %s", d.Name, ql.errText)
	}
	maxCell := s.limits().MaxCellBytes
	switch {
	case cmdErr != nil && cmdErr.Error() == "redis: nil":
		out.OK = true
		out.Reply = &RedisReply{Type: "nil", Value: nil}
	case cmdErr != nil:
		msg := masker.Mask(cmdErr.Error())
		out.Error = &msg
		out.Reply = &RedisReply{Type: "error", Value: msg}
		ql.errText = msg
	default:
		out.OK = true
		rp := redisReply(val, maxCell)
		out.Reply = &rp
		if arr, ok := rp.Value.([]RedisReply); ok {
			n := int64(len(arr))
			ql.rows = &n
		}
		if n, ok := val.(int64); ok && cls.Write {
			ql.rowsAffected = &n
		}
	}
	ql.ok = out.OK
	out.DurationMs = time.Since(ql.start).Milliseconds()
	out.ActivityID = s.logQuery(c, ql, masker)
	return out, 200, nil
}

// ───────────────────────── cancel

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.findConn(r.PathValue("connectionId"))
	if e != nil {
		return nil, 0, e
	}
	var req CancelRequest
	if e := readBody(r, &req, 64<<10); e != nil {
		return nil, 0, e
	}
	if req.QueryID == "" {
		return nil, 0, invalid("queryId is required")
	}
	s.running.mu.Lock()
	ex := s.running.m[req.QueryID]
	if ex != nil && ex.connID != d.ID {
		ex = nil
	}
	s.running.mu.Unlock()
	if ex != nil {
		ex.cancel()
	}
	s.activity(c, "devtools.query-cancelled", fmt.Sprintf("dev tools: query %s on %q cancelled", req.QueryID, d.Name),
		map[string]any{"connectionId": d.ID, "connectionName": d.Name, "kind": d.Kind, "queryId": req.QueryID, "wasRunning": ex != nil})
	return map[string]bool{"cancelled": ex != nil}, 200, nil
}

// ───────────────────────── schema (cached 60 s; not logged — a read of metadata)

type schemaEntry struct {
	fp   string
	at   time.Time
	resp *SchemaResponse
}

type schemaCache struct {
	mu sync.Mutex
	m  map[string]schemaEntry
}

func newSchemaCache() *schemaCache { return &schemaCache{m: map[string]schemaEntry{}} }

func (sc *schemaCache) drop(id string) {
	sc.mu.Lock()
	delete(sc.m, id)
	sc.mu.Unlock()
}

func (s *Service) handleSchema(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.connOfKind(r.PathValue("connectionId"), "postgres", "sqlite", "cassandra")
	if e != nil {
		return nil, 0, e
	}
	rc, e := s.resolve(r.Context(), d)
	if e != nil {
		return nil, 0, e
	}
	s.noteTarget(d.ID, rc.targetText)
	refresh := r.URL.Query().Get("refresh")
	if refresh != "1" && refresh != "true" {
		s.schema.mu.Lock()
		en, ok := s.schema.m[d.ID]
		s.schema.mu.Unlock()
		if ok && en.fp == rc.fingerprint && time.Since(en.at) < 60*time.Second {
			return en.resp, 200, nil
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var resp *SchemaResponse
	_, err := s.withClient(ctx, rc, func(cl *client) error {
		var err error
		switch d.Kind {
		case "postgres":
			resp, err = s.schemaPG(ctx, cl)
		case "sqlite":
			resp, err = s.schemaSQLite(ctx, rc, cl)
		case "cassandra":
			resp, err = s.schemaCQL(ctx, rc, cl)
		}
		return err
	})
	if err != nil {
		if de := asError(err); de != nil {
			return nil, 0, de
		}
		return nil, 0, unavailable("%s: %s", d.Name, rc.masker.Mask(err.Error()))
	}
	resp.LoadedAt = ts(time.Now())
	s.schema.mu.Lock()
	s.schema.m[d.ID] = schemaEntry{fp: rc.fingerprint, at: time.Now(), resp: resp}
	s.schema.mu.Unlock()
	return resp, 200, nil
}

func (s *Service) handleTableDetail(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.connOfKind(r.PathValue("connectionId"), "postgres", "sqlite", "cassandra")
	if e != nil {
		return nil, 0, e
	}
	ns, table := r.URL.Query().Get("namespace"), r.URL.Query().Get("table")
	if table == "" {
		return nil, 0, invalid("table is required")
	}
	rc, e := s.resolve(r.Context(), d)
	if e != nil {
		return nil, 0, e
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var resp *TableDetailResponse
	_, err := s.withClient(ctx, rc, func(cl *client) error {
		var err error
		switch d.Kind {
		case "postgres":
			if ns == "" {
				ns = "public"
			}
			resp, err = s.tableDetailPG(ctx, cl, ns, table)
		case "sqlite":
			resp, err = s.tableDetailSQLite(ctx, cl, ns, table)
		case "cassandra":
			resp, err = s.tableDetailCQL(ctx, rc, cl, ns, table)
		}
		return err
	})
	if err != nil {
		if de := asError(err); de != nil {
			return nil, 0, de
		}
		return nil, 0, unavailable("%s: %s", d.Name, rc.masker.Mask(err.Error()))
	}
	return resp, 200, nil
}

// ping checks a fresh client (Test): every connector already ran a round trip (version query / PING / CONNACK);
// HTTP sends one GET of the base URL.
func (s *Service) ping(ctx context.Context, rc *resolvedConn, cl *client) error {
	if cl.kind == "http" {
		srv, err := s.pingHTTP(ctx, rc, cl)
		if err == nil && srv != "" {
			cl.version = srv
		}
		return err
	}
	return nil
}
