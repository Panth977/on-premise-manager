// Package dlog is the agent debug log (SPEC-final §13.4): <dataDir>/logs/agent-YYYY-MM-DD.jsonl, one DebugLine per
// line, rotated daily, kept 7 days / 200 MB. Field values are redacted when the field name contains password,
// secret, token or key, and every registered secret value is replaced wherever it appears.
package dlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/model"
)

// Levels in order.
var levels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// Modules (§13.4).
var Modules = []string{"cloudlink", "mesh", "stager", "distrib", "expr", "driver", "edge", "health", "coordinator", "snapshot", "pipe", "transfer", "tunnel", "auth", "gui"}

const (
	keepDays   = 7
	maxTotal   = 200 << 20
	redacted   = "[redacted]"
	fileGlob   = "agent-*.jsonl"
	timeLayout = "2006-01-02"
)

// Logger writes JSONL debug lines.
type Logger struct {
	dir     string
	min     int
	echo    io.Writer
	mu      sync.Mutex
	f       *os.File
	day     string
	secrets map[string]bool
	now     func() time.Time
}

// New creates a logger writing into dir. level: debug|info|warn|error (default info). echo (may be nil) gets warn+ lines.
func New(dir, level string, echo io.Writer) *Logger {
	lv, ok := levels[strings.ToLower(level)]
	if !ok {
		lv = 1
	}
	_ = os.MkdirAll(dir, 0o700)
	return &Logger{dir: dir, min: lv, echo: echo, secrets: map[string]bool{}, now: time.Now}
}

// Dir is the log directory.
func (l *Logger) Dir() string { return l.dir }

// AddSecrets registers secret-tainted values (never written).
func (l *Logger) AddSecrets(vals ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.secrets) > 4096 { // §21 #112 bounded: rotated values pile up; the current ones are added again on every build
		l.secrets = map[string]bool{}
	}
	for _, v := range vals {
		if len(v) >= 4 {
			l.secrets[v] = true
		}
	}
}

// Scrub replaces every registered secret value in s (container logs in update diagnostics, §21 #87).
func (l *Logger) Scrub(s string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.scrub(s)
}

func sensitiveName(k string) bool {
	k = strings.ToLower(k)
	for _, w := range []string{"password", "secret", "token", "key"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

func (l *Logger) scrub(s string) string {
	for v := range l.secrets {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, redacted)
		}
	}
	return s
}

func (l *Logger) scrubValue(v any) any {
	switch x := v.(type) {
	case string:
		return l.scrub(x)
	case error:
		return l.scrub(x.Error())
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			if sensitiveName(k) {
				out[k] = redacted
			} else {
				out[k] = l.scrubValue(vv)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = l.scrubValue(vv)
		}
		return out
	case nil, bool, int, int64, float64, uint64:
		return x
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return l.scrub(fmt.Sprint(x))
		}
		var g any
		if json.Unmarshal(b, &g) != nil {
			return l.scrub(string(b))
		}
		return l.scrubValue(g)
	}
}

// Log writes one line. kv: alternating key, value.
func (l *Logger) Log(level, module, msg string, kv ...any) {
	if l == nil {
		return
	}
	lv, ok := levels[level]
	if !ok {
		lv, level = 1, "info"
	}
	if lv < l.min {
		return
	}
	fields := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		k := fmt.Sprint(kv[i])
		fields[k] = kv[i+1]
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, v := range fields {
		if sensitiveName(k) {
			fields[k] = redacted
		} else {
			fields[k] = l.scrubValue(v)
		}
	}
	now := l.now().UTC()
	line := model.DebugLine{Ts: now.Format(time.RFC3339Nano), Level: level, Module: module, Msg: l.scrub(msg), Fields: fields}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	day := now.Format(timeLayout)
	if l.f == nil || l.day != day {
		if l.f != nil {
			l.f.Close()
		}
		f, err := os.OpenFile(filepath.Join(l.dir, "agent-"+day+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		l.f, l.day = f, day
		go l.Rotate()
	}
	_, _ = l.f.Write(append(b, '\n'))
	if l.echo != nil && lv >= 2 {
		fmt.Fprintf(l.echo, "%s %s [%s] %s %s\n", line.Ts, strings.ToUpper(level), module, line.Msg, compact(fields))
	}
}

func compact(f map[string]any) string {
	if len(f) == 0 {
		return ""
	}
	b, _ := json.Marshal(f)
	return string(b)
}

// Debug / Info / Warn / Error are shortcuts.
func (l *Logger) Debug(module, msg string, kv ...any) { l.Log("debug", module, msg, kv...) }
func (l *Logger) Info(module, msg string, kv ...any)  { l.Log("info", module, msg, kv...) }
func (l *Logger) Warn(module, msg string, kv ...any)  { l.Log("warn", module, msg, kv...) }
func (l *Logger) Error(module, msg string, kv ...any) { l.Log("error", module, msg, kv...) }

// Recover is deferred at the top of a goroutine (`defer log.Recover("metrics", "sample")`): a panic is logged with
// its stack and the goroutine ends — it never takes the whole agent (and its edge) down (§21 #112). Nil-safe.
func (l *Logger) Recover(module, what string) {
	r := recover()
	if r == nil {
		return
	}
	buf := make([]byte, 8192)
	n := runtime.Stack(buf, false)
	if l == nil {
		fmt.Fprintf(os.Stderr, "opm-agent: panic in %s/%s: %v\n%s\n", module, what, r, buf[:n])
		return
	}
	l.Error(module, "goroutine panic (recovered)", "where", what, "panic", fmt.Sprint(r), "stack", string(buf[:n]))
}

// Close closes the current file.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// DayFile describes one log file.
type DayFile struct {
	Day       string
	Path      string
	SizeBytes int64
}

// Files lists the log files, oldest first.
func (l *Logger) Files() []DayFile {
	m, _ := filepath.Glob(filepath.Join(l.dir, fileGlob))
	sort.Strings(m)
	out := make([]DayFile, 0, len(m))
	for _, p := range m {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "agent-"), ".jsonl")
		out = append(out, DayFile{Day: day, Path: p, SizeBytes: st.Size()})
	}
	return out
}

// Rotate deletes files older than 7 days and the oldest ones while the total exceeds 200 MB.
func (l *Logger) Rotate() {
	files := l.Files()
	cut := l.now().UTC().AddDate(0, 0, -keepDays).Format(timeLayout)
	var total int64
	for _, f := range files {
		total += f.SizeBytes
	}
	for _, f := range files {
		if f.Day == l.currentDay() {
			continue
		}
		if f.Day < cut || total > maxTotal {
			if os.Remove(f.Path) == nil {
				total -= f.SizeBytes
			}
		}
	}
}

func (l *Logger) currentDay() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.day
}

// Query filters debug lines.
type Query struct {
	Level  string // minimum level
	Module string
	Since  time.Time
	Until  time.Time
	Q      string // case-insensitive substring of msg or fields
	Limit  int
}

// Read returns matching lines newest first (limit default 500, max 5000).
func (l *Logger) Read(q Query) []model.DebugLine {
	if q.Limit <= 0 {
		q.Limit = 500
	}
	if q.Limit > 5000 {
		q.Limit = 5000
	}
	minLv := 0
	if v, ok := levels[q.Level]; ok {
		minLv = v
	}
	needle := strings.ToLower(q.Q)
	files := l.Files()
	out := []model.DebugLine{}
	for i := len(files) - 1; i >= 0 && len(out) < q.Limit; i-- {
		f := files[i]
		if !q.Since.IsZero() && f.Day < q.Since.UTC().Format(timeLayout) {
			break
		}
		// §21 #112 read the file backwards in chunks (newest first) and stop at the limit: a 200 MB day is never loaded
		eachLineReverse(f.Path, func(ln []byte) bool {
			var d model.DebugLine
			if json.Unmarshal(ln, &d) != nil {
				return true
			}
			if levels[d.Level] < minLv || (q.Module != "" && d.Module != q.Module) {
				return true
			}
			t, _ := time.Parse(time.RFC3339Nano, d.Ts)
			if !q.Since.IsZero() && t.Before(q.Since) {
				return true
			}
			if !q.Until.IsZero() && t.After(q.Until) {
				return true
			}
			if needle != "" && !strings.Contains(strings.ToLower(string(ln)), needle) {
				return true
			}
			if d.Fields == nil {
				d.Fields = map[string]any{}
			}
			out = append(out, d)
			return len(out) < q.Limit
		})
	}
	return out
}

// eachLineReverse calls fn with every non-empty line of path, last line first, until fn returns false. Memory: one
// 64 KiB chunk + the current partial line (lines over 4 MiB are skipped).
func eachLineReverse(path string, fn func([]byte) bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	const chunk = 64 * 1024
	const maxLine = 4 * 1024 * 1024
	pos := st.Size()
	var carry []byte // the start of a line whose end was in the chunk read before (later in the file)
	buf := make([]byte, chunk)
	for pos > 0 {
		n := int64(chunk)
		if pos < n {
			n = pos
		}
		pos -= n
		if _, err := f.ReadAt(buf[:n], pos); err != nil && err != io.EOF {
			return
		}
		data := append(append([]byte(nil), buf[:n]...), carry...)
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			if ln := data[i+1:]; len(ln) > 0 && len(ln) <= maxLine {
				if !fn(ln) {
					return
				}
			}
			data = data[:i]
		}
		carry = data
		if len(carry) > maxLine {
			carry = nil // an oversized line: dropped
		}
	}
	if len(carry) > 0 {
		fn(carry)
	}
}

// eachLine calls fn with every non-empty line of path in order (streamed).
func eachLine(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if b := sc.Bytes(); len(b) > 0 {
			if err := fn(b); err != nil {
				return err
			}
		}
	}
	return nil
}

// Export writes the raw lines between from and to (inclusive, chronological) to w.
func (l *Logger) Export(w io.Writer, from, to time.Time) (int64, error) {
	var n int64
	for _, f := range l.Files() {
		if !from.IsZero() && f.Day < from.UTC().Format(timeLayout) {
			continue
		}
		if !to.IsZero() && f.Day > to.UTC().Format(timeLayout) {
			continue
		}
		err := eachLine(f.Path, func(ln []byte) error {
			var d struct {
				Ts string `json:"ts"`
			}
			if json.Unmarshal(ln, &d) != nil {
				return nil
			}
			t, _ := time.Parse(time.RFC3339Nano, d.Ts)
			if (!from.IsZero() && t.Before(from)) || (!to.IsZero() && t.After(to)) {
				return nil
			}
			k, err := w.Write(append(ln, '\n'))
			n += int64(k)
			return err
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
