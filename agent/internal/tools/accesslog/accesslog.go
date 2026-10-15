// Package accesslog is the edge access log (SPEC §21 #89 a): a Caddy middleware `http.handlers.opm_access_log` that
// the agent prepends to every edge server (AddTo), writing one JSON line per request to
// <agentHome>/logs/edge-access.log, rotated at 20 MiB, 5 old files kept (edge-access.log.1 … .5).
package accesslog

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// File name and rotation.
const (
	FileName = "edge-access.log"
	MaxBytes = 20 << 20
	Keep     = 5
)

func init() { caddy.RegisterModule(Handler{}) }

// Handler is http.handlers.opm_access_log.
type Handler struct {
	Dir string `json:"dir"`
	w   *Writer
}

// CaddyModule implements caddy.Module.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.opm_access_log", New: func() caddy.Module { return new(Handler) }}
}

// Provision opens (or reuses) the writer of the directory.
func (h *Handler) Provision(caddy.Context) error {
	if h.Dir == "" {
		return errors.New("opm_access_log: dir is required")
	}
	h.w = WriterFor(h.Dir)
	return nil
}

// Line is one access log entry (shared AccessLogLine).
type Line struct {
	T        string  `json:"t"`
	Remote   string  `json:"remote"`
	Host     string  `json:"host"`
	Method   string  `json:"method"`
	URI      string  `json:"uri"`
	Proto    string  `json:"proto"`
	Status   int     `json:"status"`
	Size     int64   `json:"size"`
	DurMs    float64 `json:"durMs"`
	UA       string  `json:"ua"`
	Upstream *string `json:"upstream"`
}

type recorder struct {
	*caddyhttp.ResponseWriterWrapper
	status int
	size   int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		r.status = code
	}
	r.ResponseWriterWrapper.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	n, err := r.ResponseWriterWrapper.Write(b)
	r.size += int64(n)
	return n, err
}

func (r *recorder) ReadFrom(src io.Reader) (int64, error) {
	return io.Copy(writerOnly{r}, src)
}

type writerOnly struct{ io.Writer }

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	start := time.Now()
	rec := &recorder{ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w}}
	err := next.ServeHTTP(rec, r)
	st := rec.status
	if err != nil {
		var he caddyhttp.HandlerError
		if errors.As(err, &he) && he.StatusCode != 0 {
			st = he.StatusCode
		} else if st == 0 {
			st = 500
		}
	}
	if st == 0 {
		st = 200
	}
	remote := r.RemoteAddr
	if host, _, e := net.SplitHostPort(remote); e == nil {
		remote = host
	}
	ln := Line{T: start.UTC().Format(time.RFC3339Nano), Remote: remote, Host: r.Host, Method: r.Method, URI: r.RequestURI, Proto: r.Proto,
		Status: st, Size: rec.size, DurMs: float64(time.Since(start).Microseconds()) / 1000, UA: r.UserAgent()}
	if repl, ok := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer); ok {
		if up := repl.ReplaceAll("{http.reverse_proxy.upstream.hostport}", ""); up != "" {
			ln.Upstream = &up
		}
	}
	if len(ln.UA) > 300 {
		ln.UA = ln.UA[:300]
	}
	if len(ln.URI) > 2048 {
		ln.URI = ln.URI[:2048]
	}
	if h.w != nil {
		h.w.Write(ln)
	}
	return err
}

// AddTo prepends the access log middleware to every server of a Caddy partial (map of server JSON objects).
func AddTo(servers map[string]any, dir string) {
	for _, v := range servers {
		srv, ok := v.(map[string]any)
		if !ok {
			continue
		}
		routes, _ := srv["routes"].([]any)
		mw := map[string]any{"handle": []any{map[string]any{"handler": "opm_access_log", "dir": dir}}}
		srv["routes"] = append([]any{mw}, routes...)
	}
}

// ───────────────────────── rotating writer

// Writer appends JSON lines to <dir>/edge-access.log with size rotation.
type Writer struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

var (
	writersMu sync.Mutex
	writers   = map[string]*Writer{}
)

// WriterFor returns the process-wide writer of a directory.
func WriterFor(dir string) *Writer {
	writersMu.Lock()
	defer writersMu.Unlock()
	if w := writers[dir]; w != nil {
		return w
	}
	w := &Writer{path: filepath.Join(dir, FileName)}
	writers[dir] = w
	return w
}

// Path is the current file.
func (w *Writer) Path() string { return w.path }

// Write appends one line (errors are dropped: logging never breaks a request).
func (w *Writer) Write(ln Line) {
	b, err := json.Marshal(ln)
	if err != nil {
		return
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		_ = os.MkdirAll(filepath.Dir(w.path), 0o700)
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		st, _ := f.Stat()
		w.f = f
		if st != nil {
			w.size = st.Size()
		}
	}
	if w.size+int64(len(b)) > MaxBytes {
		w.rotateLocked()
		if w.f == nil {
			return
		}
	}
	n, _ := w.f.Write(b)
	w.size += int64(n)
}

func (w *Writer) rotateLocked() {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	_ = os.Remove(w.path + "." + strconv.Itoa(Keep))
	for i := Keep - 1; i >= 1; i-- {
		_ = os.Rename(w.path+"."+strconv.Itoa(i), w.path+"."+strconv.Itoa(i+1))
	}
	_ = os.Rename(w.path, w.path+".1")
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	w.f, w.size = f, 0
}

// Interface guards.
var (
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
)
