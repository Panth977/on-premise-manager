package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
)

// fakeHost implements Host for tests.
type fakeHost struct {
	site    string
	dir     string
	cfgPath string
	dc      *docker.Client
	log     *dlog.Logger
	mu      sync.Mutex
	targets []Target
	acts    []fakeAct
	secrets []string
	vols    []Volume
	locked  bool
	volBusy string
	seq     int
}

type fakeAct struct {
	Actor   string
	Type    string
	Summary string
	Data    map[string]any
}

func newFakeHost(t *testing.T) *fakeHost {
	dir := t.TempDir()
	return &fakeHost{site: "app", dir: dir, dc: docker.New(docker.DefaultHost("")), log: dlog.New(dir+"/logs", "debug", nil), seq: 3}
}

func (h *fakeHost) Site() string           { return h.site }
func (h *fakeHost) DataDir() string        { return h.dir }
func (h *fakeHost) ConfigPath() string     { return h.cfgPath }
func (h *fakeHost) Docker() *docker.Client { return h.dc }
func (h *fakeHost) Log() *dlog.Logger      { return h.log }
func (h *fakeHost) Targets(ctx context.Context) ([]Target, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Target(nil), h.targets...), nil
}
func (h *fakeHost) Activity(actor *string, typ, summary string, data map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := ""
	if actor != nil {
		a = *actor
	}
	h.acts = append(h.acts, fakeAct{Actor: a, Type: typ, Summary: summary, Data: data})
}
func (h *fakeHost) activities(typ string) []fakeAct {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []fakeAct
	for _, a := range h.acts {
		if a.Type == typ {
			out = append(out, a)
		}
	}
	return out
}
func (h *fakeHost) SecretValues() []string { return h.secrets }
func (h *fakeHost) Volumes() []Volume      { return h.vols }
func (h *fakeHost) HeadImages() map[string]bool {
	return map[string]bool{"opm-test-protected:v1": true}
}
func (h *fakeHost) HeadSeq() int             { return h.seq }
func (h *fakeHost) UpdateRunning() bool      { return h.locked }
func (h *fakeHost) VolumeBusy(string) string { return h.volBusy }
func (h *fakeHost) Checks(string) []Check    { return nil }
func (h *fakeHost) CallerOf(r *http.Request) Caller {
	return callerFromHeaders(r)
}

// callerFromHeaders: tests pass the caller as headers (the agent fills it from the session / actor token).
func callerFromHeaders(r *http.Request) Caller {
	return Caller{Actor: r.Header.Get("X-Test-Actor"), SessionID: r.Header.Get("X-Test-Session"), Via: r.Header.Get("X-Test-Via")}
}

// serve mounts the routes like the agent does (under /_opm/api/sites/<site>), without auth.
func serve(t *testing.T, s *Service) *httptest.Server {
	mux := http.NewServeMux()
	for _, rt := range s.Routes() {
		fn := rt.Fn
		mux.HandleFunc(rt.Method+" "+rt.Pattern, func(w http.ResponseWriter, r *http.Request) {
			v, st, err := fn(w, r, callerFromHeaders(r))
			if err != nil {
				e, ok := err.(*Error)
				if !ok {
					e = &Error{Status: 500, Code: "internal", Msg: err.Error()}
				}
				writeToolErr(w, e)
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
	prefix := "/_opm/api/sites/" + s.h.Site()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix+"/tools/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
			mux.ServeHTTP(w, r2)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type call struct {
	actor, session string
}

func doJSON(t *testing.T, srv *httptest.Server, c call, method, path string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, srv.URL+"/_opm/api/sites/app/tools"+path, rd)
	req.Header.Set("X-Test-Actor", c.actor)
	req.Header.Set("X-Test-Session", c.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}
