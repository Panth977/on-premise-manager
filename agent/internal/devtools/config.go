package devtools

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Limits is TS DevToolsLimits.
type Limits struct {
	StatementTimeoutSec int   `json:"statementTimeoutSec"`
	MaxRows             int   `json:"maxRows"`
	BufferRows          int   `json:"bufferRows"`
	MaxCellBytes        int   `json:"maxCellBytes"`
	MaxResultBytes      int64 `json:"maxResultBytes"`
	MaxResponseBytes    int64 `json:"maxResponseBytes"`
	InlineResponseBytes int64 `json:"inlineResponseBytes"`
	IdleTimeoutSec      int   `json:"idleTimeoutSec"`
	MaxConcurrent       int   `json:"maxConcurrent"`
	ExecutePerMinute    int   `json:"executePerMinute"`
	RevealPerMinute     int   `json:"revealPerMinute"`
	MqttStreamMaxSec    int   `json:"mqttStreamMaxSec"`
	HistoryDays         int   `json:"historyDays"`
}

// DefaultLimits is TS DEVTOOLS_DEFAULT_LIMITS.
var DefaultLimits = Limits{
	StatementTimeoutSec: 30, MaxRows: 1000, BufferRows: 10000, MaxCellBytes: 65536, MaxResultBytes: 64 << 20,
	MaxResponseBytes: 10 << 20, InlineResponseBytes: 2 << 20, IdleTimeoutSec: 600, MaxConcurrent: 8, ExecutePerMinute: 120,
	RevealPerMinute: 30, MqttStreamMaxSec: 6 * 3600, HistoryDays: 30,
}

// Config is agent.json `tools.devTools` (switch, default true) + `devTools` (limits).
type Config struct {
	Enabled bool
	Limits  Limits
	Path    string
}

type fileConfig struct {
	Tools *struct {
		DevTools *bool `json:"devTools"`
	} `json:"tools"`
	DevTools *struct {
		StatementTimeoutSec *int   `json:"statementTimeoutSec"`
		MaxRows             *int   `json:"maxRows"`
		BufferRows          *int   `json:"bufferRows"`
		MaxCellBytes        *int   `json:"maxCellBytes"`
		MaxResultBytes      *int64 `json:"maxResultBytes"`
		MaxResponseBytes    *int64 `json:"maxResponseBytes"`
		IdleTimeoutSec      *int   `json:"idleTimeoutSec"`
		MaxConcurrent       *int   `json:"maxConcurrent"`
	} `json:"devTools"`
}

func clampI(v *int, lo, hi int, def int) int {
	if v == nil {
		return def
	}
	if *v < lo {
		return lo
	}
	if *v > hi {
		return hi
	}
	return *v
}

func clamp64(v *int64, lo, hi int64, def int64) int64 {
	if v == nil {
		return def
	}
	if *v < lo {
		return lo
	}
	if *v > hi {
		return hi
	}
	return *v
}

// LoadConfig reads agent.json (missing file / keys = defaults; values are clamped to their ranges).
func LoadConfig(path string) Config {
	c := Config{Enabled: true, Limits: DefaultLimits, Path: path}
	if path == "" {
		return c
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var fc fileConfig
	if json.Unmarshal(b, &fc) != nil {
		return c
	}
	if fc.Tools != nil && fc.Tools.DevTools != nil {
		c.Enabled = *fc.Tools.DevTools
	}
	if d := fc.DevTools; d != nil {
		l := &c.Limits
		l.StatementTimeoutSec = clampI(d.StatementTimeoutSec, 1, 3600, l.StatementTimeoutSec)
		l.MaxRows = clampI(d.MaxRows, 1, 10000, l.MaxRows)
		l.BufferRows = clampI(d.BufferRows, 1, 100000, l.BufferRows)
		l.MaxCellBytes = clampI(d.MaxCellBytes, 64, 64<<20, l.MaxCellBytes)
		l.MaxResultBytes = clamp64(d.MaxResultBytes, 1<<20, 1<<30, l.MaxResultBytes)
		l.MaxResponseBytes = clamp64(d.MaxResponseBytes, 1<<10, 1<<30, l.MaxResponseBytes)
		l.IdleTimeoutSec = clampI(d.IdleTimeoutSec, 10, 7*24*3600, l.IdleTimeoutSec)
		l.MaxConcurrent = clampI(d.MaxConcurrent, 1, 64, l.MaxConcurrent)
		if l.BufferRows < l.MaxRows {
			l.BufferRows = l.MaxRows
		}
		if l.InlineResponseBytes > l.MaxResponseBytes {
			l.InlineResponseBytes = l.MaxResponseBytes
		}
	}
	return c
}

// Reason explains why dev tools are off ("" = on).
func (c Config) Reason() string {
	if !c.Enabled {
		return `dev tools are disabled in agent.json ("tools": {"devTools": false}); set it to true (Agent page → settings, or edit the file) and restart the agent`
	}
	return ""
}

// configWatcher reloads agent.json when its modification time changes (checked at most every 2 s).
type configWatcher struct {
	mu      sync.Mutex
	path    string
	cfg     Config
	mod     time.Time
	checked time.Time
}

func newConfigWatcher(path string) *configWatcher {
	w := &configWatcher{path: path, cfg: LoadConfig(path)}
	if st, err := os.Stat(path); err == nil {
		w.mod = st.ModTime()
	}
	w.checked = time.Now()
	return w
}

func (w *configWatcher) get() Config {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.path == "" || time.Since(w.checked) < 2*time.Second {
		return w.cfg
	}
	w.checked = time.Now()
	if st, err := os.Stat(w.path); err == nil && !st.ModTime().Equal(w.mod) {
		w.mod = st.ModTime()
		w.cfg = LoadConfig(w.path)
	}
	return w.cfg
}

func (w *configWatcher) set(c Config) {
	w.mu.Lock()
	w.cfg = c
	w.checked = time.Now().Add(time.Hour)
	w.mu.Unlock()
}
