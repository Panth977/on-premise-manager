package tools

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Transcripts (§21 #89 b): every terminal's OUTPUT is recorded as asciicast v2 under <agentHome>/transcripts/
// <sessionId>.cast (+ <sessionId>.json meta), 0600, kept 7 days (and ≤ 200 MiB in total), never uploaded. Input
// keystrokes are not recorded (passwords typed at a prompt are not echoed, so they are not in the output either).
// Known secret values of the site are masked before anything is written.

const (
	transcriptDays     = 7
	transcriptMaxBytes = 50 << 20  // per session (recording stops, the session continues)
	transcriptTotalCap = 200 << 20 // all transcripts of the site
)

// TranscriptMeta mirrors the shared TranscriptMeta.
type TranscriptMeta struct {
	ID          string   `json:"id"`
	SiteSlug    string   `json:"siteSlug"`
	Kind        string   `json:"kind"`
	Target      string   `json:"target"`
	Command     []string `json:"command"`
	Actor       *string  `json:"actor"`
	Via         *string  `json:"via"`
	StartedAt   string   `json:"startedAt"`
	EndedAt     *string  `json:"endedAt"`
	DurationSec *int64   `json:"durationSec"`
	ExitCode    *int     `json:"exitCode"`
	SizeBytes   int64    `json:"sizeBytes"`
	ExpiresAt   string   `json:"expiresAt"`
	Truncated   bool     `json:"truncated,omitempty"`
}

type transcript struct {
	mu      sync.Mutex
	dir     string
	meta    TranscriptMeta
	f       *os.File
	w       *bufio.Writer
	start   time.Time
	mask    *masker
	size    int64
	stopped bool
}

func openTranscript(dir string, meta TranscriptMeta, cols, rows int, secrets []string) (*transcript, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, meta.ID+".cast"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	t := &transcript{dir: dir, meta: meta, f: f, w: bufio.NewWriterSize(f, 32*1024), start: time.Now(), mask: newMasker(secrets)}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	hdr, _ := json.Marshal(map[string]any{"version": 2, "width": cols, "height": rows, "timestamp": t.start.Unix(),
		"title": meta.Kind + " · " + meta.Target, "env": map[string]string{"TERM": "xterm-256color"}})
	t.w.Write(hdr)
	t.w.WriteByte('\n')
	t.saveMeta()
	return t, nil
}

func (t *transcript) event(kind string, data []byte) {
	if len(data) == 0 {
		return
	}
	line, _ := json.Marshal([]any{float64(time.Since(t.start).Microseconds()) / 1e6, kind, string(data)})
	t.size += int64(len(line) + 1)
	if t.size > transcriptMaxBytes {
		if !t.stopped {
			t.stopped = true
			t.meta.Truncated = true
			msg, _ := json.Marshal([]any{float64(time.Since(t.start).Microseconds()) / 1e6, "o", "\r\n[opm: transcript size limit reached; recording stopped]\r\n"})
			t.w.Write(msg)
			t.w.WriteByte('\n')
		}
		return
	}
	t.w.Write(line)
	t.w.WriteByte('\n')
}

// Output records terminal output (masked).
func (t *transcript) Output(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.event("o", t.mask.Write(p))
}

// Resize records a resize.
func (t *transcript) Resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.event("r", []byte(itoa(cols)+"x"+itoa(rows)))
}

// Close finishes the file and the meta.
func (t *transcript) Close(exitCode *int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.event("o", t.mask.Flush())
	_ = t.w.Flush()
	_ = t.f.Close()
	end := time.Now()
	e := ts(end)
	d := int64(end.Sub(t.start).Seconds())
	t.meta.EndedAt, t.meta.DurationSec, t.meta.ExitCode = &e, &d, exitCode
	t.saveMeta()
}

func (t *transcript) flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.w.Flush()
}

func (t *transcript) saveMeta() {
	if st, err := os.Stat(filepath.Join(t.dir, t.meta.ID+".cast")); err == nil {
		t.meta.SizeBytes = st.Size()
	}
	b, _ := json.MarshalIndent(t.meta, "", "  ")
	_ = os.WriteFile(filepath.Join(t.dir, t.meta.ID+".json"), b, 0o600)
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// listTranscripts returns the metas, newest first.
func listTranscripts(dir string) []TranscriptMeta {
	ents, _ := os.ReadDir(dir)
	out := []TranscriptMeta{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m TranscriptMeta
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, m.ID+".cast")); err == nil {
			m.SizeBytes = st.Size()
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

// cleanupTranscriptDir removes transcripts older than 7 days, then the oldest ones above the total cap.
func cleanupTranscriptDir(dir string, open map[string]bool) int {
	list := listTranscripts(dir)
	cut := time.Now().Add(-transcriptDays * 24 * time.Hour)
	removed := 0
	var total int64
	for _, m := range list {
		total += m.SizeBytes
	}
	for i := len(list) - 1; i >= 0; i-- { // oldest first
		m := list[i]
		if open[m.ID] {
			continue
		}
		st, _ := time.Parse(time.RFC3339Nano, m.StartedAt)
		if st.Before(cut) || total > transcriptTotalCap {
			_ = os.Remove(filepath.Join(dir, m.ID+".cast"))
			_ = os.Remove(filepath.Join(dir, m.ID+".json"))
			total -= m.SizeBytes
			removed++
		}
	}
	return removed
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// castToText renders a .cast file as plain text (ANSI sequences removed, CRLF → LF).
func castToText(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	var sb strings.Builder
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		var ev []any
		if json.Unmarshal(sc.Bytes(), &ev) != nil || len(ev) < 3 {
			continue
		}
		if k, _ := ev[1].(string); k != "o" {
			continue
		}
		s, _ := ev[2].(string)
		sb.WriteString(s)
	}
	txt := ansiRe.ReplaceAllString(sb.String(), "")
	txt = strings.ReplaceAll(txt, "\r\n", "\n")
	txt = strings.ReplaceAll(txt, "\r", "")
	return []byte(txt), sc.Err()
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
