package devtools

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/expr"
)

// fakeHost is a devtools.Host on a temp SQLite (activities table like the agent's) and an expression context with
// secrets / variables.
type fakeHost struct {
	t       *testing.T
	dir     string
	db      *sql.DB
	head    *Head
	ectx    *expr.Context
	secrets []string
	dc      *docker.Client
	mu      sync.Mutex
	seq     int
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "opm.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE activities (seq INTEGER PRIMARY KEY, activityId TEXT NOT NULL, ts TEXT NOT NULL, actor TEXT, origin TEXT NOT NULL, type TEXT NOT NULL,
	  summary TEXT NOT NULL, data TEXT NOT NULL, prevHash TEXT, hash TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	pw := "s3cret-Pa55word"
	ctx := &expr.Context{Var: map[string]any{"db_user": "app", "api-host": "example.invalid"}, Secret: map[string]*string{"pg-pass": &pw, "api-token": strp("tok-ABCDEF-123456")}}
	ctx.Deployment.ID = "D1"
	ctx.Self.Slug = "app"
	return &fakeHost{t: t, dir: dir, db: db, ectx: ctx, secrets: []string{pw, "tok-ABCDEF-123456"}}
}

func strp(s string) *string { return &s }

func (h *fakeHost) Site() string           { return "app" }
func (h *fakeHost) DataDir() string        { return h.dir }
func (h *fakeHost) ConfigPath() string     { return "" }
func (h *fakeHost) DB() *sql.DB            { return h.db }
func (h *fakeHost) Docker() *docker.Client { return h.dc }
func (h *fakeHost) Log() *dlog.Logger      { return nil }
func (h *fakeHost) Head() *Head            { return h.head }
func (h *fakeHost) SecretValues() []string { return h.secrets }
func (h *fakeHost) SessionActor(string) string {
	return ""
}
func (h *fakeHost) ResolveExpr(src string) (*expr.Result, error) {
	return expr.Resolve(src, h.ectx, expr.FieldDevtool)
}
func (h *fakeHost) Activity(actor *string, typ, summary string, data map[string]any) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	id := fmt.Sprintf("app:%d", h.seq)
	b, _ := json.Marshal(data)
	_, err := h.db.Exec(`INSERT INTO activities(seq, activityId, ts, actor, origin, type, summary, data, hash) VALUES(?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),?,?,?,?,?,?)`,
		h.seq, id, actor, "agent", typ, summary, string(b), "x")
	if err != nil {
		h.t.Errorf("activity: %v", err)
	}
	return id
}

// activities returns type → data JSON (in order).
func (h *fakeHost) activities(prefix string) [][2]string {
	rows, err := h.db.Query(`SELECT type, data FROM activities WHERE type LIKE ? ORDER BY seq`, prefix+"%")
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var t, d string
		_ = rows.Scan(&t, &d)
		out = append(out, [2]string{t, d})
	}
	return out
}

// testServer mounts the routes with a header-based caller (X-Session, X-Actor) the way the agent wrapper does.
func testServer(t *testing.T, s *Service) *httptest.Server {
	mux := http.NewServeMux()
	for _, rt := range s.Routes() {
		rt := rt
		mux.HandleFunc(rt.Method+" "+rt.Pattern, func(w http.ResponseWriter, r *http.Request) {
			c := Caller{SessionID: r.Header.Get("X-Session"), Actor: r.Header.Get("X-Actor")}
			if c.SessionID == "" {
				w.WriteHeader(401)
				return
			}
			if rt.Auth == AuthActor && c.Actor == "" {
				w.WriteHeader(401)
				return
			}
			v, st, err := rt.Fn(w, r, c)
			if err != nil {
				e := err.(*Error)
				if ra, ok := e.Details["retryAfterSec"]; ok {
					w.Header().Set("Retry-After", fmt.Sprint(ra))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(e.Status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": e.Code, "message": e.Msg, "details": e.Details}})
				return
			}
			if v == nil {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(st)
			_ = json.NewEncoder(w).Encode(v)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type client2 struct {
	t       *testing.T
	base    string
	session string
	actor   string
}

func (c *client2) do(method, path string, body any, out any) (int, http.Header, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r, _ := http.NewRequest(method, c.base+"/devtools"+path, rd)
	if c.session != "" {
		r.Header.Set("X-Session", c.session)
	}
	if c.actor != "" {
		r.Header.Set("X-Actor", c.actor)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, resp.Header, raw
}

func mustContainNone(t *testing.T, where string, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			t.Fatalf("%s leaks %q: %s", where, s, text)
		}
	}
}
