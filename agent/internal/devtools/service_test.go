package devtools

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opm/agent/internal/model"
)

func f64(v float64) *float64 { return &v }

func pgInput(name string, pw model.DevToolCredential) ConnectionInput {
	return ConnectionInput{Name: name, Kind: "postgres", Target: model.DevToolTarget{Kind: "host", Host: strp("127.0.0.1"), Port: f64(1)},
		Auth:    model.DevToolAuth{Username: strp("postgres"), Password: pw, HTTPScheme: "none", Token: model.DevToolCredential{Kind: "none"}},
		Options: model.DevToolOptions{DefaultHeaders: []model.DevToolHeader{}}}
}

func TestSwitchOffAndAuth(t *testing.T) {
	h := newFakeHost(t)
	s := New(h)
	srv := testServer(t, s)
	c := &client2{t: t, base: srv.URL, session: "S1"}
	var info Info
	if st, _, _ := c.do("GET", "/info", nil, &info); st != 200 || !info.Enabled || info.Limits.MaxRows != 1000 || len(info.Kinds) != 6 {
		t.Fatalf("info %d %+v", st, info)
	}
	cfg := s.Config()
	cfg.Enabled = false
	s.SetConfig(cfg)
	if st, _, _ := c.do("GET", "/info", nil, &info); st != 200 || info.Enabled || info.Reason == nil {
		t.Fatalf("info off %d %+v", st, info)
	}
	var e struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if st, _, _ := c.do("GET", "/connections", nil, &e); st != 403 || e.Error.Code != "forbidden" || e.Error.Details["reason"] == nil {
		t.Fatalf("switch off → %d %+v", st, e)
	}
	c.actor = "ops@acme.example"
	if st, _, _ := c.do("POST", "/connections", pgInput("x", model.DevToolCredential{Kind: "none"}), nil); st != 403 {
		t.Fatalf("switch off write → %d", st)
	}
	// config file: tools.devTools false + limits
	p := filepath.Join(t.TempDir(), "agent.json")
	_ = os.WriteFile(p, []byte(`{"tools":{"devTools":false},"devTools":{"maxRows":50,"maxConcurrent":2,"statementTimeoutSec":99999}}`), 0o600)
	lc := LoadConfig(p)
	if lc.Enabled || lc.Limits.MaxRows != 50 || lc.Limits.MaxConcurrent != 2 || lc.Limits.StatementTimeoutSec != 3600 {
		t.Fatalf("config %+v", lc)
	}
	if !LoadConfig("").Enabled {
		t.Fatal("default on")
	}
}

func TestManualConnectionsMaskRevealPresets(t *testing.T) {
	h := newFakeHost(t)
	h.head = &Head{Seq: 3, RevID: "R3", Secrets: []string{"pg-pass", "api-token"}, Variables: []string{"db_user"},
		Presets: []model.DevToolPreset{{PresetSlug: "pg", Name: "Main DB", Kind: "postgres", Target: model.DevToolTarget{Kind: "host", Host: strp("127.0.0.1"), Port: f64(1)},
			Auth:    model.DevToolAuth{Username: strp("{{ var.db_user }}"), Password: model.DevToolCredential{Kind: "expr", Expr: strp("{{ secret.pg-pass }}")}, HTTPScheme: "none", Token: model.DevToolCredential{Kind: "none"}},
			Options: model.DevToolOptions{DefaultHeaders: []model.DevToolHeader{}}}}}
	s := New(h)
	srv := testServer(t, s)
	c := &client2{t: t, base: srv.URL, session: "S1", actor: "ops@acme.example"}
	const lit = "hunter2-literal-pw"
	var conn Connection
	st, _, raw := c.do("POST", "/connections", pgInput("Local PG", model.DevToolCredential{Kind: "literal", Value: strp(lit)}), &conn)
	if st != 201 || !strings.HasPrefix(conn.ID, "c_") || conn.Source != "manual" || conn.Auth.Password.Kind != "literal" || model.Str(conn.Auth.Password.Value) != model.Mask {
		t.Fatalf("create %d %s", st, raw)
	}
	mustContainNone(t, "create response", string(raw), lit)
	var list model.ListResponse[Connection]
	_, _, raw = c.do("GET", "/connections", nil, &list)
	mustContainNone(t, "list", string(raw), lit, "s3cret-Pa55word")
	if len(list.Items) != 2 || list.Items[0].ID != "p:pg" || list.Items[0].Source != "preset" || *list.Items[0].PresetRevSeq != 3 || list.Items[1].ID != conn.ID {
		t.Fatalf("list %s", raw)
	}
	var stored string
	_ = h.db.QueryRow(`SELECT def FROM devtools_connections WHERE id = ?`, conn.ID).Scan(&stored)
	mustContainNone(t, "def column", stored, lit)
	// MASK keeps the stored literal
	in := pgInput("Local PG 2", model.DevToolCredential{Kind: "literal", Value: strp(model.Mask)})
	if st, _, raw := c.do("PUT", "/connections/"+conn.ID, in, &conn); st != 200 || conn.Name != "Local PG 2" {
		t.Fatalf("update %d %s", st, raw)
	}
	var rv RevealResponse
	st, hdr, _ := c.do("POST", "/connections/"+conn.ID+"/reveal?field=password", nil, &rv)
	if st != 200 || rv.Value != lit || rv.HideAfterSec != 60 || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal %d %+v %v", st, rv, hdr)
	}
	// a new literal → credentialChanged
	in = pgInput("Local PG 2", model.DevToolCredential{Kind: "literal", Value: strp("new-literal-pw")})
	c.do("PUT", "/connections/"+conn.ID, in, nil)
	acts := h.activities("devtools.connection-updated")
	if len(acts) != 2 || !strings.Contains(acts[0][1], `"credentialChanged":false`) || !strings.Contains(acts[1][1], `"credentialChanged":true`) {
		t.Fatalf("updated activities %v", acts)
	}
	for _, a := range h.activities("devtools.") {
		mustContainNone(t, "activity", a[1], lit, "new-literal-pw")
	}
	// reveal: 30 per minute per session → 429 + Retry-After; logged
	n429 := 0
	for i := 0; i < 31; i++ {
		st, hdr, _ := c.do("POST", "/connections/"+conn.ID+"/reveal", nil, nil)
		if st == 429 {
			n429++
			if hdr.Get("Retry-After") == "" {
				t.Fatal("Retry-After missing")
			}
		}
	}
	if n429 != 2 { // 1 earlier + 31 = 32 → the last two refused
		t.Fatalf("429s %d", n429)
	}
	if n := len(h.activities("devtools.credential-revealed")); n != 30 {
		t.Fatalf("reveal activities %d", n)
	}
	// expressions are not revealed here
	if st, _, _ := c.do("POST", "/connections/p:pg/reveal", nil, nil); st != 422 {
		t.Fatalf("reveal expr %d", st)
	}
	// presets are read-only: update / delete → 409, duplicate → manual copy
	if st, _, _ := c.do("PUT", "/connections/p:pg", in, nil); st != 409 {
		t.Fatalf("preset update %d", st)
	}
	if st, _, _ := c.do("DELETE", "/connections/p:pg", nil, nil); st != 409 {
		t.Fatalf("preset delete %d", st)
	}
	var dup Connection
	if st, _, raw := c.do("POST", "/connections/p:pg/duplicate", map[string]string{"name": "Main DB (mine)"}, &dup); st != 201 || dup.Source != "manual" || dup.Auth.Password.Kind != "expr" {
		t.Fatalf("duplicate %d %s", st, raw)
	}
	if a := h.activities("devtools.connection-created"); len(a) != 2 || !strings.Contains(a[1][1], `"duplicatedFrom":"p:pg"`) {
		t.Fatalf("duplicate activity %v", a)
	}
	// validation: literal MASK without a stored value, unknown kind, preset rules
	if st, _, _ := c.do("POST", "/connections", pgInput("x", model.DevToolCredential{Kind: "literal", Value: strp(model.Mask)}), nil); st != 422 {
		t.Fatalf("MASK create %d", st)
	}
	bad := pgInput("", model.DevToolCredential{Kind: "none"})
	if st, _, raw := c.do("POST", "/connections", bad, nil); st != 422 || !strings.Contains(string(raw), "V_REQUIRED") {
		t.Fatalf("empty name %d %s", st, raw)
	}
	// test of a connection that refuses → ok false, logged
	var tr TestConnectionResponse
	if st, _, _ := c.do("POST", "/connections/"+conn.ID+"/test", nil, &tr); st != 200 || tr.OK || tr.Message == "" {
		t.Fatalf("test %d %+v", st, tr)
	}
	if a := h.activities("devtools.connection-tested"); len(a) != 1 || !strings.Contains(a[0][1], `"ok":false`) {
		t.Fatalf("tested %v", a)
	}
	// delete
	if st, _, _ := c.do("DELETE", "/connections/"+conn.ID, nil, nil); st != 200 {
		t.Fatalf("delete %d", st)
	}
	if st, _, _ := c.do("DELETE", "/connections/"+conn.ID, nil, nil); st != 404 {
		t.Fatalf("delete again %d", st)
	}
}

func execSQL(t *testing.T, c *client2, id string, req map[string]any) (int, ExecuteResponse, string) {
	t.Helper()
	var out ExecuteResponse
	st, _, raw := c.do("POST", "/connections/"+id+"/sql", req, &out)
	return st, out, string(raw)
}

func TestSQLiteEndToEnd(t *testing.T) {
	h := newFakeHost(t)
	vol := filepath.Join(h.dir, "vol", "data")
	_ = os.MkdirAll(filepath.Join(vol, "db"), 0o755)
	f := filepath.Join(vol, "db", "app.db")
	db, err := sql.Open("sqlite", "file:"+f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, blob BLOB)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_ = os.WriteFile(filepath.Join(h.dir, "outside.db"), nil, 0o600)
	_ = os.Symlink(filepath.Join(vol, "db"), filepath.Join(vol, "link"))
	h.head = &Head{Seq: 1, Volumes: []HeadVolume{{Slug: "data", Label: "Data", Kind: "dir", Path: vol}}}
	s := New(h)
	srv := testServer(t, s)
	c := &client2{t: t, base: srv.URL, session: "S1", actor: "ops@acme.example"}
	mk := func(name, path string, ro bool) string {
		in := ConnectionInput{Name: name, Kind: "sqlite", Target: model.DevToolTarget{Kind: "volume-file", VolumeSlug: strp("data"), Path: strp(path)}, ReadOnly: ro,
			Auth: model.DevToolAuth{Password: model.DevToolCredential{Kind: "none"}, Token: model.DevToolCredential{Kind: "none"}, HTTPScheme: "none"}}
		var conn Connection
		if st, _, raw := c.do("POST", "/connections", in, &conn); st != 201 {
			t.Fatalf("create %s: %d %s", name, st, raw)
		}
		return conn.ID
	}
	rw := mk("App DB", "db/app.db", false)
	ro := mk("App DB ro", "db/app.db", true)
	link := mk("Via link", "link/app.db", false)
	// multi statement + rows + rowsAffected
	st, out, raw := execSQL(t, c, rw, map[string]any{"queryId": "q1", "statement": "INSERT INTO t(name) VALUES ('a'), ('b'); SELECT id, name FROM t ORDER BY id; SELECT 's3cret-Pa55word' AS leak"})
	if st != 200 || !out.OK || len(out.Results) != 3 || *out.Results[0].RowsAffected != 2 || out.Results[1].TotalRows != 2 || out.Results[1].Rows[1][1] != "b" ||
		out.Results[1].Columns[1].Name != "name" || out.ActivityID == nil {
		t.Fatalf("exec %d %s", st, raw)
	}
	// params
	_, out, raw = execSQL(t, c, rw, map[string]any{"queryId": "q2", "statement": "SELECT name FROM t WHERE id = ?", "params": []any{2}})
	if len(out.Results) != 1 || out.Results[0].Rows[0][0] != "b" {
		t.Fatalf("params %s", raw)
	}
	// statement error = a result; the rest is skipped
	_, out, _ = execSQL(t, c, rw, map[string]any{"queryId": "q3", "statement": "SELECT nope FROM t; SELECT 1"})
	if out.OK || out.Results[0].OK || out.Results[0].Error == nil || out.Results[1].Error == nil || !strings.Contains(out.Results[1].Error.Message, "skipped") {
		t.Fatalf("errors %+v", out)
	}
	// paging + export: 2500 rows, first page 1000
	execSQL(t, c, rw, map[string]any{"queryId": "q4", "statement": "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 2500) INSERT INTO t(name) SELECT 'n' || i FROM n"})
	_, out, _ = execSQL(t, c, rw, map[string]any{"queryId": "q5", "statement": "SELECT id, name FROM t ORDER BY id"})
	r0 := out.Results[0]
	if r0.TotalRows != 2502 || len(r0.Rows) != 1000 || r0.ResultID == nil {
		t.Fatalf("paging %d %d", r0.TotalRows, len(r0.Rows))
	}
	var page ResultPageResponse
	if st, _, _ := c.do("GET", "/results/"+*r0.ResultID+"?offset=1000&limit=500", nil, &page); st != 200 || page.Offset != 1000 || len(page.Rows) != 500 || page.Rows[0][1] != "n999" {
		t.Fatalf("page %d %+v", st, page.Rows[:1])
	}
	other := &client2{t: t, base: srv.URL, session: "S2"}
	if st, _, _ := other.do("GET", "/results/"+*r0.ResultID+"?offset=0", nil, nil); st != 404 {
		t.Fatalf("other session reads the result: %d", st)
	}
	_, hdr, body := c.do("GET", "/results/"+*r0.ResultID+"/export?format=csv", nil, nil)
	if !strings.HasPrefix(string(body), "id,name\n1,a\n") || !strings.Contains(hdr.Get("Content-Disposition"), "attachment") || strings.Count(string(body), "\n") != 2503 {
		t.Fatalf("csv %q", string(body)[:40])
	}
	_, _, body = c.do("GET", "/results/"+*r0.ResultID+"/export?format=ndjson", nil, nil)
	if strings.Count(string(body), "\n") != 2502 || !strings.HasPrefix(string(body), `{"id":1,"name":"a"}`) {
		t.Fatalf("ndjson %q", string(body)[:40])
	}
	// big cell → CellTruncated + full value
	big := strings.Repeat("x", 70000)
	execSQL(t, c, rw, map[string]any{"queryId": "q6", "statement": "UPDATE t SET name = ? WHERE id = 1", "params": []any{big}})
	_, out, _ = execSQL(t, c, rw, map[string]any{"queryId": "q7", "statement": "SELECT name FROM t WHERE id = 1"})
	cell, _ := out.Results[0].Rows[0][0].(map[string]any)
	if cell["$truncated"] != true || cell["sizeBytes"].(float64) != 70000 {
		t.Fatalf("cell %v", out.Results[0].Rows[0][0])
	}
	_, _, body = c.do("GET", fmt.Sprintf("/results/%s/value?row=0&col=0", *out.Results[0].ResultID), nil, nil)
	if string(body) != big {
		t.Fatal("full value")
	}
	// explain
	_, out, raw = execSQL(t, c, rw, map[string]any{"queryId": "q8", "statement": "SELECT * FROM t WHERE id = 1", "explain": "plan"})
	if out.Results[0].Type != "explain" || out.Results[0].Plan == nil || !strings.Contains(out.Results[0].Plan.Text, "t") {
		t.Fatalf("explain %s", raw)
	}
	// destructive → 422 confirmRequired; with the name → runs
	var e struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if st, _, raw := c.do("POST", "/connections/"+rw+"/sql", map[string]any{"queryId": "q9", "statement": "DELETE FROM t"}, &e); st != 422 || e.Error.Details["confirmRequired"] != true {
		t.Fatalf("destructive %d %s", st, raw)
	}
	if st, out, raw := execSQL(t, c, rw, map[string]any{"queryId": "q10", "statement": "DELETE FROM t WHERE id > 2"}); st != 200 || *out.Results[0].RowsAffected != 2500 {
		t.Fatalf("delete where %d %s", st, raw)
	}
	// read-only: the write fails inside SQLite (query_only)
	_, out, _ = execSQL(t, c, ro, map[string]any{"queryId": "q11", "statement": "INSERT INTO t(name) VALUES ('ro')"})
	if out.OK || out.Results[0].Error == nil {
		t.Fatalf("read-only write must fail: %+v", out.Results[0])
	}
	if _, out, _ = execSQL(t, c, ro, map[string]any{"queryId": "q12", "statement": "SELECT count(*) FROM t"}); !out.OK {
		t.Fatal("read-only select")
	}
	// ATTACH: inside the volume ok, outside / URI refused
	_, out, _ = execSQL(t, c, rw, map[string]any{"queryId": "q13", "statement": "ATTACH '../../../outside.db' AS o"})
	if out.OK || !strings.Contains(out.Results[0].Error.Message, "outside the volume") {
		t.Fatalf("attach outside %+v", out.Results[0])
	}
	_, out, _ = execSQL(t, c, rw, map[string]any{"queryId": "q14", "statement": "ATTACH 'file:/etc/passwd?mode=ro' AS o"})
	if out.OK {
		t.Fatal("attach URI")
	}
	_, out, raw = execSQL(t, c, rw, map[string]any{"queryId": "q15", "statement": "ATTACH 'other.db' AS o; CREATE TABLE o.x(a); SELECT name FROM o.sqlite_master"})
	if !out.OK {
		t.Fatalf("attach inside %s", raw)
	}
	if _, err := os.Stat(filepath.Join(vol, "db", "other.db")); err != nil {
		t.Fatal("attached file not next to the database")
	}
	_, out, _ = execSQL(t, c, rw, map[string]any{"queryId": "q16", "statement": "VACUUM INTO '/tmp/x.db'"})
	if out.OK {
		t.Fatal("VACUUM INTO outside")
	}
	// a path through a symlink is refused
	if st, _, raw := c.do("POST", "/connections/"+link+"/sql", map[string]any{"queryId": "q17", "statement": "SELECT 1"}, nil); st != 422 || !strings.Contains(string(raw), "symlink") {
		t.Fatalf("symlink %d %s", st, raw)
	}
	// schema
	var sch SchemaResponse
	if st, _, raw := c.do("GET", "/connections/"+rw+"/schema", nil, &sch); st != 200 || sch.Kind != "sqlite" || len(sch.Namespaces) == 0 || sch.Namespaces[0].Tables[0].Name != "t" || len(sch.Namespaces[0].Tables[0].Columns) != 3 {
		t.Fatalf("schema %d %s", st, raw)
	}
	var td TableDetailResponse
	if st, _, raw := c.do("GET", "/connections/"+rw+"/schema/table?namespace=main&table=t", nil, &td); st != 200 || td.DDL == nil || !strings.Contains(*td.DDL, "CREATE TABLE t") {
		t.Fatalf("table detail %d %s", st, raw)
	}
	// activities: every execution logged, statements redacted, never result data / secrets
	acts := h.activities("devtools.query")
	if len(acts) < 15 {
		t.Fatalf("query activities %d", len(acts))
	}
	for _, a := range acts {
		mustContainNone(t, "query activity", a[1], "s3cret-Pa55word", big[:100], `"n999"`)
		var d map[string]any
		_ = json.Unmarshal([]byte(a[1]), &d)
		for _, k := range []string{"connectionId", "connectionName", "kind", "source", "target", "op", "statement", "statements", "durationMs", "ok", "readOnly", "destructive", "confirmed", "queryId", "sessionId"} {
			if _, ok := d[k]; !ok {
				t.Fatalf("activity misses %s: %s", k, a[1])
			}
		}
	}
	if !strings.Contains(acts[0][1], Redacted) {
		t.Fatalf("site secret not masked: %s", acts[0][1])
	}
	// history (newest first, filters, cursor)
	var hist model.PageResponse[HistoryItem]
	if st, _, raw := c.do("GET", "/history?connectionId="+ro+"&limit=1", nil, &hist); st != 200 || len(hist.Items) != 1 || hist.Cursor == nil || hist.Items[0].ConnectionID != ro {
		t.Fatalf("history %d %s", st, raw)
	}
	var hist2 model.PageResponse[HistoryItem]
	c.do("GET", "/history?connectionId="+ro+"&limit=5&cursor="+*hist.Cursor, nil, &hist2)
	if len(hist2.Items) != 1 || hist2.Cursor != nil || !hist2.Items[0].OK == hist.Items[0].OK && hist2.Items[0].ActivityID == hist.Items[0].ActivityID {
		t.Fatalf("history page 2 %+v", hist2)
	}
	var mine model.PageResponse[HistoryItem]
	other.do("GET", "/history?mine=1", nil, &mine)
	if len(mine.Items) != 0 {
		t.Fatalf("mine of another session %d", len(mine.Items))
	}
}

func TestHTTPExecute(t *testing.T) {
	var lastHdr http.Header
	var lastQuery string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/api/echo?x=1", http.StatusFound)
			return
		case "/to-agent":
			http.Redirect(w, r, "/_opm/api/info", http.StatusFound)
			return
		case "/bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0, 1, 2, 255})
			return
		}
		lastHdr, lastQuery = r.Header.Clone(), r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "v1", Path: "/", HttpOnly: true})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"path": r.URL.Path, "method": r.Method, "body": string(b)})
	}))
	defer up.Close()
	h := newFakeHost(t)
	h.head = &Head{Seq: 1, Secrets: []string{"api-token"}}
	s := New(h)
	srv := testServer(t, s)
	c := &client2{t: t, base: srv.URL, session: "S1", actor: "ops@acme.example"}
	in := ConnectionInput{Name: "API", Kind: "http", Target: model.DevToolTarget{Kind: "url", URL: strp(up.URL)},
		Auth:    model.DevToolAuth{HTTPScheme: "bearer", Password: model.DevToolCredential{Kind: "none"}, Token: model.DevToolCredential{Kind: "expr", Expr: strp("{{ secret.api-token }}")}},
		Options: model.DevToolOptions{BasePath: strp("/api"), DefaultHeaders: []model.DevToolHeader{{Name: "X-Site", Value: "{{ self.slug }}"}}}}
	var conn Connection
	if st, _, raw := c.do("POST", "/connections", in, &conn); st != 201 {
		t.Fatalf("create %d %s", st, raw)
	}
	var env Environment
	if st, _, raw := c.do("POST", "/environments", EnvironmentInput{Name: "dev", Variables: []EnvVar{{Name: "who", Value: "env-user", Enabled: true}, {Name: "apikey", Value: "env-secret-999", Secret: true, Enabled: true}}}, &env); st != 201 || env.Variables[1].Value != model.Mask {
		t.Fatalf("env %d %s", st, raw)
	}
	req := map[string]any{"queryId": "h1", "environmentId": env.ID, "variables": map[string]string{"inline": "v-inline"}, "request": map[string]any{
		"method": "POST", "url": "/echo?u={{who}}&k={{apikey}}&unknown={{nope}}", "query": []map[string]any{{"name": "i", "value": "{{inline}}", "enabled": true}},
		"headers": []map[string]any{{"name": "Cookie", "value": "opm_session_app=abc; keep=1; opm_session=x", "enabled": true}, {"name": "X-Tok", "value": "{{ secret.api-token }}", "enabled": true}},
		"body":    map[string]any{"kind": "json", "text": `{"a":"{{who}}"}`}, "auth": nil, "followRedirects": nil, "tlsInsecure": nil, "timeoutSec": nil}}
	var out ExecuteHTTPResponse
	st, _, raw := c.do("POST", "/connections/"+conn.ID+"/http", req, &out)
	if st != 200 || !out.OK || *out.Status != 200 || out.BodyText == nil || !strings.Contains(*out.BodyText, `"path":"/api/echo"`) || !strings.Contains(*out.BodyText, `env-user`) {
		t.Fatalf("http %d %s", st, raw)
	}
	if lastHdr.Get("Authorization") != "Bearer tok-ABCDEF-123456" || lastHdr.Get("X-Site") != "app" || lastHdr.Get("Cookie") != "keep=1" || lastHdr.Get("Content-Type") != "application/json" {
		t.Fatalf("sent headers %v", lastHdr)
	}
	if !strings.Contains(lastQuery, "u=env-user") || !strings.Contains(lastQuery, "k=env-secret-999") || !strings.Contains(lastQuery, "unknown={{nope}}") || !strings.Contains(lastQuery, "i=v-inline") {
		t.Fatalf("query %s", lastQuery)
	}
	if len(out.Cookies) != 1 || out.Cookies[0].Name != "sid" || out.ResultID == nil || out.Timing.TotalMs < 0 {
		t.Fatalf("response %s", raw)
	}
	mustContainNone(t, "response.sent", fmt.Sprint(out.Sent), "tok-ABCDEF-123456", "env-secret-999")
	acts := h.activities("devtools.query")
	if len(acts) != 1 {
		t.Fatalf("acts %v", acts)
	}
	mustContainNone(t, "http activity", acts[0][1], "tok-ABCDEF-123456", "env-secret-999", "env-user\"}") // body never logged
	if !strings.Contains(acts[0][1], `"status":200`) || !strings.Contains(acts[0][1], `"op":"http"`) {
		t.Fatalf("activity %s", acts[0][1])
	}
	// body download
	_, hdr, body := c.do("GET", "/results/"+*out.ResultID+"/body", nil, nil)
	if !strings.Contains(string(body), `"method":"POST"`) || hdr.Get("Content-Type") != "application/json" {
		t.Fatalf("body %s", body)
	}
	// redirects: chain returned; to the agent API refused
	get := func(url string) ExecuteHTTPResponse {
		var o ExecuteHTTPResponse
		c.do("POST", "/connections/"+conn.ID+"/http", map[string]any{"queryId": "h-" + url, "request": map[string]any{"method": "GET", "url": url, "query": []any{}, "headers": []any{}, "body": map[string]any{"kind": "none"}}}, &o)
		return o
	}
	o := get(up.URL + "/redirect")
	if !o.OK || len(o.Redirects) != 1 || o.Redirects[0].Status != 302 || !strings.HasSuffix(*o.FinalURL, "/api/echo?x=1") {
		t.Fatalf("redirect %+v", o)
	}
	if o = get(up.URL + "/to-agent"); o.OK || o.Error == nil {
		t.Fatalf("redirect to the agent API must fail: %+v", o)
	}
	if o = get(up.URL + "/bin"); o.BodyBase64 == nil || *o.BodyBase64 != "AAEC/w==" {
		t.Fatalf("binary %+v", o)
	}
	if st, _, _ := c.do("POST", "/connections/"+conn.ID+"/http", map[string]any{"queryId": "h9", "request": map[string]any{"method": "GET", "url": up.URL + "/_opm/api/info", "body": map[string]any{"kind": "none"}}}, nil); st != 403 {
		t.Fatalf("agent API %d", st)
	}
	if st, _, _ := c.do("POST", "/connections/"+conn.ID+"/http", map[string]any{"queryId": "h10", "request": map[string]any{"method": "GET", "url": "/x/{{ secret.missing }}", "body": map[string]any{"kind": "none"}}}, nil); st != 422 {
		t.Fatalf("bad expression %d", st)
	}
	// read-only: only GET / HEAD / OPTIONS
	in.ReadOnly = true
	c.do("PUT", "/connections/"+conn.ID, in, nil)
	if st, _, _ := c.do("POST", "/connections/"+conn.ID+"/http", map[string]any{"queryId": "h11", "request": map[string]any{"method": "DELETE", "url": "/x", "body": map[string]any{"kind": "none"}}}, nil); st != 403 {
		t.Fatalf("read-only DELETE %d", st)
	}
	// environment secret: masked in reads, revealable (logged)
	var envs model.ListResponse[Environment]
	_, _, raw = c.do("GET", "/environments", nil, &envs)
	mustContainNone(t, "environments", string(raw), "env-secret-999")
	var rv RevealResponse
	if st, _, _ := c.do("POST", "/environments/"+env.ID+"/reveal?name=apikey", nil, &rv); st != 200 || rv.Value != "env-secret-999" {
		t.Fatalf("env reveal %d %+v", st, rv)
	}
	if a := h.activities("devtools.credential-revealed"); len(a) != 1 || !strings.Contains(a[0][1], `"environmentId"`) {
		t.Fatalf("env reveal activity %v", a)
	}
	// saved items
	var sv Saved
	if st, _, raw := c.do("POST", "/saved", SavedInput{Kind: "http", Collection: "smoke", Name: "echo", Content: `{"method":"GET"}`}, &sv); st != 201 || !strings.HasPrefix(sv.ID, "s_") || *sv.CreatedBy != "ops@acme.example" {
		t.Fatalf("saved %d %s", st, raw)
	}
	var sl model.ListResponse[Saved]
	if c.do("GET", "/saved?kind=http", nil, &sl); len(sl.Items) != 1 {
		t.Fatal("saved list")
	}
	if st, _, _ := c.do("DELETE", "/saved/"+sv.ID, nil, nil); st != 200 {
		t.Fatal("saved delete")
	}
}

func TestRedisRefusals(t *testing.T) {
	h := newFakeHost(t)
	s := New(h)
	srv := testServer(t, s)
	c := &client2{t: t, base: srv.URL, session: "S1", actor: "ops@acme.example"}
	in := ConnectionInput{Name: "Cache", Kind: "redis", ReadOnly: true, Target: model.DevToolTarget{Kind: "host", Host: strp("127.0.0.1"), Port: f64(1)},
		Auth: model.DevToolAuth{Database: strp("0"), HTTPScheme: "none", Password: model.DevToolCredential{Kind: "none"}, Token: model.DevToolCredential{Kind: "none"}}}
	var conn Connection
	if st, _, raw := c.do("POST", "/connections", in, &conn); st != 201 {
		t.Fatalf("create %d %s", st, raw)
	}
	for cmd, want := range map[string]int{"MONITOR": 422, "SET a b": 403, "FLUSHALL": 403, "GET \"unterminated": 422} {
		if st, _, raw := c.do("POST", "/connections/"+conn.ID+"/redis", map[string]any{"queryId": "r", "line": cmd}, nil); st != want {
			t.Fatalf("%s → %d %s", cmd, st, raw)
		}
	}
	// a read on a dead server: 503, still logged (redacted)
	if st, _, _ := c.do("POST", "/connections/"+conn.ID+"/redis", map[string]any{"queryId": "r2", "command": []string{"GET", "k"}}, nil); st != 503 {
		t.Fatalf("dead server %d", st)
	}
	if a := h.activities("devtools.query"); len(a) != 1 || !strings.Contains(a[0][1], `"ok":false`) {
		t.Fatalf("activity %v", a)
	}
	// rate limit: 120 executions / minute / session
	s.rl.m["execute|S1|"] = nil
	n := 0
	for i := 0; i < 125; i++ {
		if st, _, _ := c.do("POST", "/connections/"+conn.ID+"/redis", map[string]any{"queryId": fmt.Sprint("x", i), "line": "SET a b"}, nil); st == 403 {
			n++
		}
	}
	if n != 125 { // refused before the limiter (read-only) — the limiter counts executions only
		t.Fatalf("read-only refusals %d", n)
	}
	in.ReadOnly = false
	c.do("PUT", "/connections/"+conn.ID, in, nil)
	got429 := false
	for i := 0; i < 125 && !got429; i++ {
		st, _, _ := c.do("POST", "/connections/"+conn.ID+"/redis", map[string]any{"queryId": fmt.Sprint("y", i), "line": "PING"}, nil)
		got429 = st == 429
	}
	if !got429 {
		t.Fatal("no 429 after 120 executions")
	}
}

// container-file: the path inside the container is mapped through its mounts to the host file; a path on no mount
// (container-local storage) → 422.
func TestSQLiteContainerFile(t *testing.T) {
	h := newFakeHost(t)
	src := filepath.Join(h.dir, "vol", "app-data")
	_ = os.MkdirAll(filepath.Join(src, "db"), 0o755)
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(src, "db", "app.db"))
	_, _ = db.Exec(`CREATE TABLE k (v TEXT); INSERT INTO k VALUES ('mapped')`)
	db.Close()
	slug := "app-data"
	h.head = &Head{Seq: 1, Containers: []HeadContainer{{ContainerSlug: "api", ServiceSlug: "api", Enabled: true,
		Mounts: []HeadMount{{Target: "/var/lib/app", Source: src, Kind: "volume", VolumeSlug: &slug}, {Target: "/run/secrets/x", Source: filepath.Join(h.dir, "secrets", "x"), Kind: "secret"}}}}}
	s := New(h)
	srv := testServer(t, s)
	c := &client2{t: t, base: srv.URL, session: "S1", actor: "ops@acme.example"}
	mk := func(p string) string {
		in := ConnectionInput{Name: "C " + p, Kind: "sqlite", Target: model.DevToolTarget{Kind: "container-file", ContainerSlug: strp("api"), Path: strp(p)},
			Auth: model.DevToolAuth{Password: model.DevToolCredential{Kind: "none"}, Token: model.DevToolCredential{Kind: "none"}, HTTPScheme: "none"}}
		var conn Connection
		if st, _, raw := c.do("POST", "/connections", in, &conn); st != 201 {
			t.Fatalf("create %d %s", st, raw)
		}
		return conn.ID
	}
	ok := mk("/var/lib/app/db/app.db")
	_, out, raw := execSQL(t, c, ok, map[string]any{"queryId": "c1", "statement": "SELECT v FROM k"})
	if !out.OK || out.Results[0].Rows[0][0] != "mapped" {
		t.Fatalf("mapped %s", raw)
	}
	for _, p := range []string{"/etc/app.db", "/run/secrets/x"} {
		id := mk(p)
		if st, _, raw := c.do("POST", "/connections/"+id+"/sql", map[string]any{"queryId": "c2", "statement": "SELECT 1"}, nil); st != 422 || !strings.Contains(string(raw), "not on a volume") {
			t.Fatalf("%s → %d %s", p, st, raw)
		}
	}
}
