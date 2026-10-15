package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/tools/accesslog"
)

// ───────────────────────── SSE

type sse struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	mu     sync.Mutex
	dirty  bool
	closed bool
}

// close stops every later write (goroutines may still try after the handler returned).
func (s *sse) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func newSSE(w http.ResponseWriter) *sse {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	s := &sse{w: w, rc: http.NewResponseController(w)}
	_, _ = io.WriteString(w, "retry: 3000\n\n")
	_ = s.rc.Flush()
	return s
}

func (s *sse) event(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	s.dirty = true
	_, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b)
	return err
}

func (s *sse) flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	if !s.dirty {
		return nil
	}
	s.dirty = false
	return s.rc.Flush()
}

func (s *sse) ping() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	if _, err := io.WriteString(s.w, ": ping\n\n"); err != nil {
		return err
	}
	return s.rc.Flush()
}

// pump flushes every 100 ms and pings every 15 s until ctx ends.
func (s *sse) pump(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.flush(); err != nil {
				return
			}
			if time.Since(last) > 15*time.Second {
				last = time.Now()
				if s.ping() != nil {
					return
				}
			}
		}
	}
}

// ───────────────────────── query parsing

type logQuery struct {
	refs           []string
	all            bool
	stdout, stderr bool
	since, until   string // docker form (unix seconds with fraction)
	sinceT, untilT time.Time
	tail           int
	follow         bool
	q              string
	level          string
}

func parseTime(v string) (time.Time, string, bool) {
	if v == "" {
		return time.Time{}, "", true
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond()), true
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		sec := int64(f)
		ns := int64((f - float64(sec)) * 1e9)
		return time.Unix(sec, ns), v, true
	}
	return time.Time{}, "", false
}

func parseLogQuery(r *http.Request, defTail, maxTail int) (*logQuery, *Error) {
	q := r.URL.Query()
	lq := &logQuery{stdout: q.Get("stdout") != "0" && q.Get("stdout") != "false", stderr: q.Get("stderr") != "0" && q.Get("stderr") != "false",
		follow: q.Get("follow") == "1" || q.Get("follow") == "true", q: q.Get("q"), level: q.Get("level")}
	c := strings.TrimSpace(q.Get("containers"))
	if c == "" || c == "*" {
		lq.all = true
	} else {
		for _, x := range strings.Split(c, ",") {
			if x = strings.TrimSpace(x); x != "" {
				lq.refs = append(lq.refs, x)
			}
		}
	}
	var ok bool
	if lq.sinceT, lq.since, ok = parseTime(q.Get("since")); !ok {
		return nil, invalid("since: ISO time or unix seconds")
	}
	if lq.untilT, lq.until, ok = parseTime(q.Get("until")); !ok {
		return nil, invalid("until: ISO time or unix seconds")
	}
	if lq.until != "" {
		lq.follow = false
	}
	lq.tail = defTail
	if v := q.Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, invalid("tail: a number ≥ 0 (0 = all)")
		}
		lq.tail = n
	}
	if lq.tail == 0 || lq.tail > maxTail {
		lq.tail = maxTail
	}
	return lq, nil
}

func (s *Service) logTargets(ctx context.Context, lq *logQuery) ([]Target, *Error) {
	ts, err := s.h.Targets(ctx)
	if err != nil {
		return nil, unavailable(err)
	}
	var out []Target
	if lq.all {
		for _, t := range ts {
			if t.ID != "" && !t.FailedCopy {
				out = append(out, t)
			}
		}
		return out, nil
	}
	byRef := map[string]Target{}
	for _, t := range ts {
		byRef[t.Ref] = t
	}
	for _, r := range lq.refs {
		t, ok := byRef[r]
		if !ok {
			return nil, notFound("no container %q on site %s", r, s.h.Site())
		}
		if t.ID == "" {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func ttyOf(ctx context.Context, dc *docker.Client, id string) bool {
	raw, err := dc.InspectRaw(ctx, id)
	if err != nil {
		return false
	}
	var x struct {
		Config struct {
			Tty bool `json:"Tty"`
		} `json:"Config"`
	}
	_ = json.Unmarshal(raw, &x)
	return x.Config.Tty
}

type tline struct {
	ref string
	ln  docker.LogLine
	t   time.Time
}

func parseDockerTs(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// ───────────────────────── container logs

func (s *Service) handleLogStream(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	lq, e := parseLogQuery(r, 500, 10000)
	if e != nil {
		return nil, 0, e
	}
	targets, e := s.logTargets(r.Context(), lq)
	if e != nil {
		return nil, 0, e
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if lq.follow {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, 6*time.Hour)
		defer stop()
	}
	out := newSSE(w)
	defer out.close()
	refs := []string{}
	for _, t := range targets {
		refs = append(refs, t.Ref)
	}
	_ = out.event("meta", map[string]any{"containers": refs, "follow": lq.follow, "tail": lq.tail})
	_ = out.flush()
	go out.pump(ctx)
	secrets := s.h.SecretValues()
	dc := s.h.Docker()
	lines := make(chan tline, 1024)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			defer s.h.Log().Recover("tools", "logs "+t.ID)
			lr, err := dc.LogsStream(ctx, t.ID, docker.LogOptions{Stdout: lq.stdout, Stderr: lq.stderr, Since: lq.since, Until: lq.until, Tail: lq.tail, Follow: lq.follow}, ttyOf(ctx, dc, t.ID))
			if err != nil {
				_ = out.event("error", map[string]any{"message": t.Ref + ": " + err.Error(), "container": t.Ref})
				return
			}
			defer lr.Close()
			go func() { <-ctx.Done(); lr.Close() }()
			for {
				ln, err := lr.Next()
				if err != nil {
					return
				}
				select {
				case lines <- tline{ref: t.Ref, ln: ln, t: parseDockerTs(ln.Ts)}:
				case <-ctx.Done():
					return
				}
			}
		}(t)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	emit := func(tl tline) error {
		txt := tl.ln.Text
		if len(secrets) > 0 {
			txt = MaskString(txt, secrets)
		}
		return out.event("line", map[string]any{"c": tl.ref, "s": tl.ln.Stream, "t": tl.ln.Ts, "x": txt})
	}
	// initial burst: collect until 200 ms without a new line (or 2 s), merge by timestamp, then pass through
	var burst []tline
	quiet := time.NewTimer(300 * time.Millisecond)
	hard := time.After(2 * time.Second)
collect:
	for {
		select {
		case tl := <-lines:
			burst = append(burst, tl)
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(200 * time.Millisecond)
		case <-quiet.C:
			break collect
		case <-hard:
			break collect
		case <-done:
			for {
				select {
				case tl := <-lines:
					burst = append(burst, tl)
				default:
					break collect
				}
			}
		case <-ctx.Done():
			return nil, 0, nil
		}
	}
	sort.SliceStable(burst, func(i, j int) bool { return burst[i].t.Before(burst[j].t) })
	for _, tl := range burst {
		if emit(tl) != nil {
			return nil, 0, nil
		}
	}
	_ = out.flush()
	for {
		select {
		case tl := <-lines:
			if emit(tl) != nil {
				return nil, 0, nil
			}
		case <-done:
			for {
				select {
				case tl := <-lines:
					_ = emit(tl)
				default:
					reason := "complete"
					if lq.follow {
						reason = "containers stopped"
					}
					_ = out.event("end", map[string]any{"reason": reason})
					_ = out.flush()
					return nil, 0, nil
				}
			}
		case <-ctx.Done():
			if r.Context().Err() == nil {
				_ = out.event("end", map[string]any{"reason": "stream time limit (6 h); reconnect"})
				_ = out.flush()
			}
			return nil, 0, nil
		}
	}
}

func (s *Service) handleLogDownload(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	lq, e := parseLogQuery(r, 10000, 100000)
	if e != nil {
		return nil, 0, e
	}
	lq.follow = false
	targets, e := s.logTargets(r.Context(), lq)
	if e != nil {
		return nil, 0, e
	}
	dc := s.h.Docker()
	var all []tline
	for _, t := range targets {
		lr, err := dc.LogsStream(r.Context(), t.ID, docker.LogOptions{Stdout: lq.stdout, Stderr: lq.stderr, Since: lq.since, Until: lq.until, Tail: lq.tail}, ttyOf(r.Context(), dc, t.ID))
		if err != nil {
			return nil, 0, unavailable(err)
		}
		for {
			ln, err := lr.Next()
			if err != nil {
				break
			}
			all = append(all, tline{ref: t.Ref, ln: ln, t: parseDockerTs(ln.Ts)})
		}
		lr.Close()
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	name := "all"
	if !lq.all && len(lq.refs) <= 3 {
		name = strings.Join(lq.refs, "+")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="opm-%s-%s-%s.log"`, s.h.Site(), safeName(name), time.Now().UTC().Format("20060102-1504")))
	w.Header().Set("Cache-Control", "no-store")
	bw := bufio.NewWriterSize(w, 64*1024)
	secrets := s.h.SecretValues()
	for _, tl := range all {
		fmt.Fprintf(bw, "%s %s %s %s\n", tl.ln.Ts, tl.ref, tl.ln.Stream, MaskString(tl.ln.Text, secrets))
	}
	_ = bw.Flush()
	return nil, 0, nil
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '+' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, s)
}

// ───────────────────────── file tails (agent log, access log)

// tailLines returns the last n lines of a file (≤ 32 MiB read).
func tailLines(path string, n int) ([][]byte, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0
	}
	size := st.Size()
	start := size - 32<<20
	if start < 0 {
		start = 0
	}
	_, _ = f.Seek(start, io.SeekStart)
	b, _ := io.ReadAll(f)
	parts := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if start > 0 && len(parts) > 0 {
		parts = parts[1:] // partial first line
	}
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	out := make([][]byte, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, []byte(p))
		}
	}
	return out, size
}

// follower polls a file for appended lines; current() names the file (it may change: a new day / rotation).
type follower struct {
	current func() string
	path    string
	off     int64
	partial []byte
}

func (fl *follower) poll() [][]byte {
	p := fl.current()
	if p != fl.path {
		fl.path, fl.off, fl.partial = p, 0, nil
	}
	f, err := os.Open(fl.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	if st.Size() < fl.off { // rotated / truncated
		fl.off, fl.partial = 0, nil
	}
	if st.Size() == fl.off {
		return nil
	}
	_, _ = f.Seek(fl.off, io.SeekStart)
	b, _ := io.ReadAll(io.LimitReader(f, 8<<20))
	fl.off += int64(len(b))
	data := append(fl.partial, b...)
	var out [][]byte
	for {
		i := strings.IndexByte(string(data), '\n')
		if i < 0 {
			break
		}
		if i > 0 {
			out = append(out, append([]byte(nil), data[:i]...))
		}
		data = data[i+1:]
	}
	fl.partial = append([]byte(nil), data...)
	return out
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

func (s *Service) streamFile(w http.ResponseWriter, r *http.Request, initial func(n int) ([][]byte, string), current func() string, keep func([]byte) (any, bool)) {
	lq, e := parseLogQuery(r, 500, 5000)
	if e != nil {
		writeToolErr(w, e)
		return
	}
	ctx := r.Context()
	if lq.follow {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 6*time.Hour)
		defer cancel()
	}
	out := newSSE(w)
	defer out.close()
	_ = out.event("meta", map[string]any{"follow": lq.follow, "tail": lq.tail})
	go out.pump(ctx)
	lines, path := initial(lq.tail)
	for _, b := range lines {
		if v, ok := keep(b); ok {
			if out.event("line", v) != nil {
				return
			}
		}
	}
	_ = out.flush()
	if !lq.follow {
		_ = out.event("end", map[string]any{"reason": "complete"})
		_ = out.flush()
		return
	}
	fl := &follower{current: current, path: path}
	if st, err := os.Stat(path); err == nil {
		fl.off = st.Size()
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				_ = out.event("end", map[string]any{"reason": "stream time limit (6 h); reconnect"})
				_ = out.flush()
			}
			return
		case <-t.C:
			for _, b := range fl.poll() {
				if v, ok := keep(b); ok {
					if out.event("line", v) != nil {
						return
					}
				}
			}
		}
	}
}

func writeToolErr(w http.ResponseWriter, e *Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.Status)
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": e.Code, "message": e.Msg, "details": e.Details}})
	_, _ = w.Write(b)
}

func (s *Service) agentLogFile() string {
	files := s.h.Log().Files()
	if len(files) == 0 {
		return filepath.Join(s.h.Log().Dir(), "agent-"+time.Now().UTC().Format("2006-01-02")+".jsonl")
	}
	return files[len(files)-1].Path
}

func (s *Service) handleAgentLog(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	q := r.URL.Query()
	minLv := levelRank[q.Get("level")]
	needle := strings.ToLower(q.Get("q"))
	keep := func(b []byte) (any, bool) {
		var d struct {
			Ts     string         `json:"ts"`
			Level  string         `json:"level"`
			Module string         `json:"module"`
			Msg    string         `json:"msg"`
			Fields map[string]any `json:"fields"`
		}
		if json.Unmarshal(b, &d) != nil || levelRank[d.Level] < minLv {
			return nil, false
		}
		if needle != "" && !strings.Contains(strings.ToLower(string(b)), needle) {
			return nil, false
		}
		if d.Fields == nil {
			d.Fields = map[string]any{}
		}
		return map[string]any{"t": d.Ts, "level": d.Level, "module": d.Module, "msg": d.Msg, "fields": d.Fields}, true
	}
	initial := func(n int) ([][]byte, string) {
		files := s.h.Log().Files()
		var out [][]byte
		path := s.agentLogFile()
		// newest files first until n matching lines
		for i := len(files) - 1; i >= 0 && len(out) < n; i-- {
			ls, _ := tailLines(files[i].Path, 20000)
			var keepL [][]byte
			for _, b := range ls {
				if _, ok := keep(b); ok {
					keepL = append(keepL, b)
				}
			}
			out = append(keepL, out...)
		}
		if len(out) > n {
			out = out[len(out)-n:]
		}
		return out, path
	}
	s.streamFile(w, r, initial, s.agentLogFile, keep)
	return nil, 0, nil
}

func (s *Service) accessLogPath() string {
	return filepath.Join(s.h.DataDir(), "logs", accesslog.FileName)
}

func (s *Service) handleAccessLog(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	needle := strings.ToLower(r.URL.Query().Get("q"))
	keep := func(b []byte) (any, bool) {
		if needle != "" && !strings.Contains(strings.ToLower(string(b)), needle) {
			return nil, false
		}
		var ln accesslog.Line
		if json.Unmarshal(b, &ln) != nil {
			return nil, false
		}
		return ln, true
	}
	path := s.accessLogPath()
	initial := func(n int) ([][]byte, string) {
		var out [][]byte
		for _, p := range []string{path, path + ".1"} {
			if len(out) >= n {
				break
			}
			ls, _ := tailLines(p, 20000)
			var kl [][]byte
			for _, b := range ls {
				if _, ok := keep(b); ok {
					kl = append(kl, b)
				}
			}
			out = append(kl, out...)
		}
		if len(out) > n {
			out = out[len(out)-n:]
		}
		return out, path
	}
	s.streamFile(w, r, initial, func() string { return path }, keep)
	return nil, 0, nil
}
