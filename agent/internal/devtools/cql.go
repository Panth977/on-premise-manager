package devtools

// Cassandra (gocql): one session per connection talking to ONE endpoint (the address translator maps every
// discovered peer to it — dev tools never fan out to nodes the agent cannot reach, e.g. container IPs on macOS).
// Writes on a read-only connection are refused by the classifier (403); EXPLAIN → 422.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

func (s *Service) connectCQL(ctx context.Context, rc *resolvedConn) (*client, error) {
	ip := net.ParseIP(rc.host)
	if ip == nil {
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", rc.host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("resolve %s: %v", rc.host, err)
		}
		ip = ips[0]
	}
	port := rc.port
	cl := gocql.NewCluster(net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	cl.ProtoVersion = 4
	cl.DisableInitialHostLookup = true
	cl.AddressTranslator = gocql.AddressTranslatorFunc(func(net.IP, int) (net.IP, int) { return ip, port })
	cl.HostFilter = gocql.HostFilterFunc(func(h *gocql.HostInfo) bool { return true })
	cl.Timeout = rc.timeout(30)
	cl.ConnectTimeout = 10 * time.Second
	cl.NumConns = 1
	cl.Consistency = gocql.LocalOne
	if c := rc.def.Options.Consistency; c != nil && *c != "" {
		if v, err := gocql.ParseConsistencyWrapper(*c); err == nil {
			cl.Consistency = v
		}
	}
	if rc.database != "" {
		cl.Keyspace = rc.database
	}
	if rc.user != "" {
		cl.Authenticator = gocql.PasswordAuthenticator{Username: rc.user, Password: rc.password}
	}
	if rc.def.Options.TLS {
		cl.SslOpts = &gocql.SslOptions{Config: &tls.Config{InsecureSkipVerify: rc.def.Options.TLSInsecure, ServerName: deref(rc.def.Options.TLSServerName)},
			EnableHostVerification: !rc.def.Options.TLSInsecure}
	}
	cl.ReconnectInterval = 0
	cl.Logger = quietLogger{}
	type res struct {
		s   *gocql.Session
		err error
	}
	ch := make(chan res, 1)
	go func() {
		sess, err := cl.CreateSession()
		ch <- res{sess, err}
	}()
	var sess *gocql.Session
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, errors.New(rc.masker.Mask(r.err.Error()))
		}
		sess = r.s
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.s != nil {
				r.s.Close()
			}
		}()
		return nil, fmt.Errorf("connect %s: %w", rc.addr, ctx.Err())
	}
	var ver string
	if err := sess.Query("SELECT release_version FROM system.local").WithContext(ctx).Scan(&ver); err != nil {
		sess.Close()
		return nil, err
	}
	return &client{kind: "cassandra", version: "Cassandra " + ver, value: sess, close: sess.Close}, nil
}

type quietLogger struct{}

func (quietLogger) Print(...interface{})          {}
func (quietLogger) Printf(string, ...interface{}) {}
func (quietLogger) Println(...interface{})        {}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func cqlTypeName(ti gocql.TypeInfo) string {
	if ti == nil {
		return "unknown"
	}
	s := strings.ToLower(fmt.Sprint(ti))
	return strings.NewReplacer("(", "<", ")", ">").Replace(s)
}

func cqlParams(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			out[i] = int64(f)
		} else {
			out[i] = v
		}
	}
	return out
}

func (s *Service) execCQL(ctx context.Context, c Caller, rc *resolvedConn, cl *client, stmts []string, req *ExecuteSQLRequest, pageRows int) ([]StatementResult, error) {
	sess := cl.value.(*gocql.Session)
	if sess.Closed() {
		return nil, errors.New("session has been closed")
	}
	cons := gocql.Consistency(0)
	hasCons := false
	if req.Consistency != nil && *req.Consistency != "" {
		v, err := gocql.ParseConsistencyWrapper(*req.Consistency)
		if err != nil {
			return nil, invalid("unknown consistency %s", *req.Consistency)
		}
		cons, hasCons = v, true
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
		word := strings.ToUpper(firstWord(st))
		if word == "USE" {
			sr.Error = &StatementError{Message: "USE is not supported by the pooled session: set the connection's default keyspace (database) or prefix tables with the keyspace"}
			failed = true
			results = append(results, sr)
			continue
		}
		q := sess.Query(st, cqlParams(req.Params)...).WithContext(ctx)
		if hasCons {
			q = q.Consistency(cons)
		}
		ps := pageRows
		if ps > 5000 {
			ps = 5000
		}
		q = q.PageSize(ps)
		iter := q.Iter()
		buf := &rowBuffer{maxRows: lim.BufferRows, maxBytes: lim.MaxResultBytes}
		var cols []ResultColumn
		for _, ci := range iter.Columns() {
			tn := cqlTypeName(ci.TypeInfo)
			cols = append(cols, ResultColumn{Name: ci.Name, Type: tn, Display: displayOf(tn)})
		}
		if len(cols) > 0 {
			for {
				rd, err := iter.RowData()
				if err != nil {
					break
				}
				if !iter.Scan(rd.Values...) {
					break
				}
				row := make([]any, len(rd.Values))
				for j, v := range rd.Values {
					row[j] = normValue(v)
				}
				if !buf.add(row) {
					break
				}
			}
		}
		err := iter.Close()
		sr.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			if i == 0 && isConnErr(err) && ctx.Err() == nil {
				return nil, err
			}
			se := &StatementError{Message: err.Error()}
			var re gocql.RequestError
			if errors.As(err, &re) {
				code := fmt.Sprintf("0x%04x", re.Code())
				se.Code = &code
				se.Message = re.Message()
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				se.Message = "timed out: " + se.Message
			}
			sr.Error = se
			failed = true
			results = append(results, sr)
			continue
		}
		sr.OK = true
		tag := word
		if f := strings.Fields(strings.ToUpper(StripSQLComments(st))); len(f) > 1 && (word == "CREATE" || word == "DROP" || word == "ALTER") {
			tag = word + " " + f[1]
		}
		if len(cols) > 0 {
			sr.Type = "rows"
			s.finishRows(&sr, c, cols, buf, pageRows)
			tag = fmt.Sprintf("%s %d", word, sr.TotalRows)
		}
		sr.CommandTag = &tag
		results = append(results, sr)
	}
	return results, nil
}

func (s *Service) schemaCQL(ctx context.Context, rc *resolvedConn, cl *client) (*SchemaResponse, error) {
	sess := cl.value.(*gocql.Session)
	out := &SchemaResponse{Kind: "cassandra", ServerVersion: strOrNil(cl.version), Database: strOrNil(rc.database), Namespaces: []SchemaNamespace{}}
	idx := map[string]int{}
	iter := sess.Query("SELECT keyspace_name, replication FROM system_schema.keyspaces").WithContext(ctx).Iter()
	var ks string
	var repl map[string]string
	for iter.Scan(&ks, &repl) {
		meta := map[string]string{}
		for k, v := range repl {
			meta[k] = v
		}
		idx[ks] = len(out.Namespaces)
		out.Namespaces = append(out.Namespaces, SchemaNamespace{Name: ks, System: strings.HasPrefix(ks, "system"), Tables: []SchemaTable{}, Functions: []SchemaFunction{}, Meta: meta})
		repl = nil
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	sort.Slice(out.Namespaces, func(i, j int) bool { return out.Namespaces[i].Name < out.Namespaces[j].Name })
	for i, n := range out.Namespaces {
		idx[n.Name] = i
	}
	type tk struct{ ks, t string }
	tIdx := map[tk][2]int{}
	add := func(q, kind string) error {
		it := sess.Query(q).WithContext(ctx).Iter()
		var k, t string
		var comment string
		for it.Scan(&k, &t, &comment) {
			ni, ok := idx[k]
			if !ok {
				continue
			}
			if len(tIdx) >= 2000 {
				out.Truncated = true
				break
			}
			tIdx[tk{k, t}] = [2]int{ni, len(out.Namespaces[ni].Tables)}
			out.Namespaces[ni].Tables = append(out.Namespaces[ni].Tables, SchemaTable{Name: t, Kind: kind, Columns: []SchemaColumn{}, Comment: strOrNil(comment)})
		}
		return it.Close()
	}
	if err := add("SELECT keyspace_name, table_name, comment FROM system_schema.tables", "table"); err != nil {
		return nil, err
	}
	_ = add("SELECT keyspace_name, view_name, comment FROM system_schema.views", "materialized-view")
	type colRow struct {
		col  SchemaColumn
		pos  int
		kind string
	}
	byTable := map[tk][]colRow{}
	it := sess.Query("SELECT keyspace_name, table_name, column_name, type, kind, position FROM system_schema.columns").WithContext(ctx).Iter()
	var k, t, name, typ, kind string
	var pos int
	n := 0
	for it.Scan(&k, &t, &name, &typ, &kind, &pos) {
		n++
		if n > 20000 {
			out.Truncated = true
			break
		}
		role := kind
		if role == "clustering" || role == "partition_key" || role == "regular" || role == "static" {
			r := role
			byTable[tk{k, t}] = append(byTable[tk{k, t}], colRow{SchemaColumn{Name: name, Type: typ, Nullable: kind == "regular" || kind == "static", PrimaryKey: kind == "partition_key" || kind == "clustering", Role: &r}, pos, kind})
		}
	}
	_ = it.Close()
	order := map[string]int{"partition_key": 0, "clustering": 1, "static": 2, "regular": 3}
	for key, cols := range byTable {
		ix, ok := tIdx[key]
		if !ok {
			continue
		}
		sort.SliceStable(cols, func(i, j int) bool {
			if order[cols[i].kind] != order[cols[j].kind] {
				return order[cols[i].kind] < order[cols[j].kind]
			}
			if cols[i].pos != cols[j].pos {
				return cols[i].pos < cols[j].pos
			}
			return cols[i].col.Name < cols[j].col.Name
		})
		tb := &out.Namespaces[ix[0]].Tables[ix[1]]
		for _, c := range cols {
			tb.Columns = append(tb.Columns, c.col)
		}
	}
	fit := sess.Query("SELECT keyspace_name, function_name, argument_types, return_type FROM system_schema.functions").WithContext(ctx).Iter()
	var fk, fn, ret string
	var argt []string
	for fit.Scan(&fk, &fn, &argt, &ret) {
		if ni, ok := idx[fk]; ok {
			out.Namespaces[ni].Functions = append(out.Namespaces[ni].Functions, SchemaFunction{Name: fn, Kind: "function", Arguments: strings.Join(argt, ", "), Returns: ret})
		}
		argt = nil
	}
	_ = fit.Close()
	ait := sess.Query("SELECT keyspace_name, aggregate_name, argument_types, return_type FROM system_schema.aggregates").WithContext(ctx).Iter()
	for ait.Scan(&fk, &fn, &argt, &ret) {
		if ni, ok := idx[fk]; ok {
			out.Namespaces[ni].Functions = append(out.Namespaces[ni].Functions, SchemaFunction{Name: fn, Kind: "aggregate", Arguments: strings.Join(argt, ", "), Returns: ret})
		}
		argt = nil
	}
	_ = ait.Close()
	return out, nil
}

func cqlIdent(s string) string {
	if s == strings.ToLower(s) && !strings.ContainsAny(s, " -\"") && s != "" {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func (s *Service) tableDetailCQL(ctx context.Context, rc *resolvedConn, cl *client, ks, table string) (*TableDetailResponse, error) {
	sch, err := s.schemaCQL(ctx, rc, cl)
	if err != nil {
		return nil, err
	}
	var tb *SchemaTable
	for i := range sch.Namespaces {
		if sch.Namespaces[i].Name == ks {
			for j := range sch.Namespaces[i].Tables {
				if sch.Namespaces[i].Tables[j].Name == table {
					tb = &sch.Namespaces[i].Tables[j]
				}
			}
		}
	}
	if tb == nil {
		return nil, notFound("table %s.%s does not exist", ks, table)
	}
	sess := cl.value.(*gocql.Session)
	out := &TableDetailResponse{Namespace: ks, Table: table, Columns: tb.Columns, Indexes: []SchemaIndex{}, Constraints: []Constraint{}}
	it := sess.Query("SELECT index_name, kind, options FROM system_schema.indexes WHERE keyspace_name = ? AND table_name = ?", ks, table).WithContext(ctx).Iter()
	var name, kind string
	var opts map[string]string
	for it.Scan(&name, &kind, &opts) {
		def := fmt.Sprintf("CREATE INDEX %s ON %s.%s (%s)", cqlIdent(name), cqlIdent(ks), cqlIdent(table), opts["target"])
		if kind == "CUSTOM" {
			def = fmt.Sprintf("CREATE CUSTOM INDEX %s ON %s.%s (%s) USING '%s'", cqlIdent(name), cqlIdent(ks), cqlIdent(table), opts["target"], opts["class_name"])
		}
		out.Indexes = append(out.Indexes, SchemaIndex{Name: name, Definition: def})
		opts = nil
	}
	_ = it.Close()
	var pk, ck, lines []string
	for _, c := range tb.Columns {
		l := "  " + cqlIdent(c.Name) + " " + c.Type
		if c.Role != nil && *c.Role == "static" {
			l += " STATIC"
		}
		lines = append(lines, l)
		if c.Role != nil && *c.Role == "partition_key" {
			pk = append(pk, cqlIdent(c.Name))
		}
		if c.Role != nil && *c.Role == "clustering" {
			ck = append(ck, cqlIdent(c.Name))
		}
	}
	key := "(" + strings.Join(pk, ", ") + ")"
	if len(ck) > 0 {
		key += ", " + strings.Join(ck, ", ")
	}
	lines = append(lines, "  PRIMARY KEY ("+key+")")
	ddl := fmt.Sprintf("CREATE TABLE %s.%s (\n%s\n);", cqlIdent(ks), cqlIdent(table), strings.Join(lines, ",\n"))
	for _, ix := range out.Indexes {
		ddl += "\n" + ix.Definition + ";"
	}
	out.DDL = &ddl
	out.Constraints = append(out.Constraints, Constraint{Name: "primary key", Type: "PRIMARY KEY", Definition: "PRIMARY KEY (" + key + ")"})
	return out, nil
}
