// hello-api: counter backend for the OPM hello-world e2e (SPEC-final §20.2).
//
// env: PORT (8080), REDIS_ADDR (host:port; empty -> file store), DATA_DIR (/data),
// API_TOKEN, GREETING (hello), FAIL_HEALTH ("1" -> /healthz 500).
// GET /count -> {"count":N}; POST /count -> increment -> {"count":N};
// GET /info -> {"greeting","tokenLength","store","hostname"}; GET /healthz -> 200 ok.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

type store interface {
	get() (int64, error)
	incr() (int64, error)
	name() string
}

// ---- redis store (minimal RESP over TCP) ----

type redisStore struct {
	addr string
	mu   sync.Mutex
	conn net.Conn
	rd   *bufio.Reader
}

func (r *redisStore) name() string { return "redis" }

func (r *redisStore) dial() error {
	if r.conn != nil {
		return nil
	}
	c, err := net.DialTimeout("tcp", r.addr, 3*time.Second)
	if err != nil {
		return err
	}
	r.conn, r.rd = c, bufio.NewReader(c)
	return nil
}

func (r *redisStore) do(args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if err := r.dial(); err != nil {
			return "", err
		}
		v, err := r.roundTrip(args)
		if err == nil {
			return v, nil
		}
		var re redisErr
		if errors.As(err, &re) {
			return "", err
		}
		r.conn.Close()
		r.conn = nil
		if attempt == 1 {
			return "", err
		}
	}
	return "", errors.New("unreachable")
}

type redisErr string

func (e redisErr) Error() string { return "redis: " + string(e) }

func (r *redisStore) roundTrip(args []string) (string, error) {
	_ = r.conn.SetDeadline(time.Now().Add(5 * time.Second))
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := r.conn.Write([]byte(b.String())); err != nil {
		return "", err
	}
	line, err := r.rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("empty reply")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return "", redisErr(line[1:])
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return "", err
		}
		if n < 0 {
			return "", nil // nil bulk
		}
		buf := make([]byte, n+2)
		if _, err := ioReadFull(r.rd, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}
	return "", fmt.Errorf("unexpected reply %q", line)
}

func ioReadFull(rd *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := rd.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (r *redisStore) get() (int64, error) {
	v, err := r.do("GET", "hello:count")
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	return strconv.ParseInt(v, 10, 64)
}

func (r *redisStore) incr() (int64, error) {
	v, err := r.do("INCR", "hello:count")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// ---- file store ----

type fileStore struct {
	path string
	mu   sync.Mutex
}

func (f *fileStore) name() string { return "file" }

func (f *fileStore) read() (int64, error) {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

func (f *fileStore) get() (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.read()
}

func (f *fileStore) incr() (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.read()
	if err != nil {
		return 0, err
	}
	n++
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return 0, err
	}
	tmp := f.path + ".tmp"
	fh, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	if _, err := fh.WriteString(strconv.FormatInt(n, 10)); err != nil {
		fh.Close()
		return 0, err
	}
	if err := fh.Sync(); err != nil {
		fh.Close()
		return 0, err
	}
	fh.Close()
	if err := os.Rename(tmp, f.path); err != nil {
		return 0, err
	}
	if d, err := os.Open(filepath.Dir(f.path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return n, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		// tiny self-check usable as an exec health check (FROM scratch has no curl)
		resp, err := http.Get("http://127.0.0.1:" + env("PORT", "8080") + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}
	port := env("PORT", "8080")
	greeting := env("GREETING", "hello")
	token := os.Getenv("API_TOKEN")
	failHealth := os.Getenv("FAIL_HEALTH") == "1"
	hostname, _ := os.Hostname()

	var st store
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		st = &redisStore{addr: addr}
	} else {
		st = &fileStore{path: filepath.Join(env("DATA_DIR", "/data"), "count")}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) {
		var n int64
		var err error
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			n, err = st.get()
		case http.MethodPost:
			n, err = st.incr()
		default:
			w.Header().Set("Allow", "GET, POST")
			writeJSON(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		if err != nil {
			log.Printf("store %s: %v", st.name(), err)
			writeJSON(w, 503, map[string]string{"error": err.Error(), "store": st.name()})
			return
		}
		writeJSON(w, 200, map[string]int64{"count": n})
	})
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"greeting":    greeting,
			"tokenLength": len(token),
			"store":       st.name(),
			"hostname":    hostname,
		})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if failHealth {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("failing (FAIL_HEALTH=1)"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	// /health alias (convenience)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "healthz", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "not found", "path": r.URL.Path})
	})

	srv := &http.Server{Addr: ":" + port, Handler: logReq(mux), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("hello-api listening on :%s store=%s", port, st.name())
	log.Fatal(srv.ListenAndServe())
}

func logReq(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}
