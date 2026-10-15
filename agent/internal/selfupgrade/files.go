// Package selfupgrade holds the pieces of the agent self-upgrade (SPEC-final §21 #90 b, c) that must work without a
// running agent: the files in <agentHome>/upgrade/, the binary swap / restore, the self-test, the known-good watcher
// (`opm-agent.prev upgrade-watch`) and the systemd boot guard (embedded POSIX sh script + drop-in).
//
// Files in <agentHome>/upgrade/:
//
//	state.json             the current / last AgentUpgradeProgress (survives the restart)
//	history.json           the last 10 finished attempts, newest first
//	pending.json           written right before the restart; the new agent removes it once healthy
//	healthy                the new agent's "I am up" marker (content = upgradeId)
//	rolled-back.json       written by the watcher / the boot guard when they restored opm-agent.prev
//	run.json               the mesh "Upgrade all" run of this coordinator
//	handled-requests.json  cloud upgrade requestIds this site already took (once per requestId)
//	watch.pid              pid of the running watcher
//	rollback.lock/         mkdir lock shared by the watcher and the guard (no double rollback)
package selfupgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Paths are the files of <agentHome>/upgrade.
type Paths struct{ Dir string }

// For returns the upgrade paths of a data dir.
func For(dataDir string) Paths { return Paths{Dir: filepath.Join(dataDir, "upgrade")} }

func (p Paths) State() string      { return filepath.Join(p.Dir, "state.json") }
func (p Paths) History() string    { return filepath.Join(p.Dir, "history.json") }
func (p Paths) Pending() string    { return filepath.Join(p.Dir, "pending.json") }
func (p Paths) Healthy() string    { return filepath.Join(p.Dir, "healthy") }
func (p Paths) RolledBack() string { return filepath.Join(p.Dir, "rolled-back.json") }
func (p Paths) Run() string        { return filepath.Join(p.Dir, "run.json") }
func (p Paths) Handled() string    { return filepath.Join(p.Dir, "handled-requests.json") }
func (p Paths) WatchPID() string   { return filepath.Join(p.Dir, "watch.pid") }
func (p Paths) Lock() string       { return filepath.Join(p.Dir, "rollback.lock") }
func (p Paths) WatchLog() string   { return filepath.Join(p.Dir, "watch.log") }
func (p Paths) WatchOut() string   { return filepath.Join(p.Dir, "watch.stderr") } // raw output of a setsid watcher
func (p Paths) Download() string   { return filepath.Join(p.Dir, "download") }

// Pending is pending.json (one key per line: the boot guard reads it with sed).
type Pending struct {
	UpgradeID  string `json:"upgradeId"`
	From       string `json:"from"`
	To         string `json:"to"`
	FromSha256 string `json:"fromSha256"`
	ToSha256   string `json:"toSha256"`
	Binary     string `json:"binary"`
	Prev       string `json:"prev"`
	// Deadline: unix seconds; the new agent must be healthy before it. 0 = no deadline (CLI without a restart: the
	// operator restarts the agent; no watcher, the guard ignores it).
	Deadline    int64    `json:"deadline"`
	RestartedAt string   `json:"restartedAt"`
	Supervisor  string   `json:"supervisor"` // systemd | exec
	Unit        string   `json:"unit"`
	PID         int      `json:"pid"`
	Argv        []string `json:"argv"`
	DataDir     string   `json:"dataDir"`
	InfoURL     string   `json:"infoUrl"` // http://127.0.0.1:<agentPort>/_opm/api/info
	Via         string   `json:"via"`
	Actor       *string  `json:"actor"`
	RequestID   *string  `json:"requestId"`
	RunID       *string  `json:"runId"`
}

// RolledBack is rolled-back.json.
type RolledBack struct {
	UpgradeID string  `json:"upgradeId"`
	From      string  `json:"from"`     // the version that was running before (and is restored)
	To        string  `json:"to"`       // the failed target
	Restored  string  `json:"restored"` // = from
	Reason    string  `json:"reason"`
	By        string  `json:"by"` // watcher | guard
	At        string  `json:"at"`
	HandledAt *string `json:"handledAt,omitempty"`
}

// WriteJSON writes v atomically (temp file + fsync + rename) with mode 0600.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// WriteFileAtomic writes data to path via a temp file in the same directory, fsync and rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	syncDir(filepath.Dir(path))
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// ReadJSON reads path into v; ok=false when the file does not exist.
func ReadJSON(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

// ReadPending returns pending.json (nil when absent or unreadable).
func (p Paths) ReadPending() *Pending {
	var pd Pending
	if ok, err := ReadJSON(p.Pending(), &pd); !ok || err != nil || pd.UpgradeID == "" {
		return nil
	}
	return &pd
}

// IsHealthy reports whether the healthy marker belongs to upgradeID.
func (p Paths) IsHealthy(upgradeID string) bool {
	b, err := os.ReadFile(p.Healthy())
	return err == nil && strings.TrimSpace(string(b)) == upgradeID
}

// MarkHealthy writes the healthy marker.
func (p Paths) MarkHealthy(upgradeID string) error {
	return WriteFileAtomic(p.Healthy(), []byte(upgradeID+"\n"), 0o600)
}

// TryLock takes the rollback lock (mkdir); a lock older than 5 min is stale and taken over.
func (p Paths) TryLock() (func(), bool) {
	_ = os.MkdirAll(p.Dir, 0o700)
	if err := os.Mkdir(p.Lock(), 0o700); err != nil {
		st, serr := os.Stat(p.Lock())
		if serr != nil || time.Since(st.ModTime()) < 5*time.Minute {
			return func() {}, false
		}
		_ = os.Remove(p.Lock())
		if err := os.Mkdir(p.Lock(), 0o700); err != nil {
			return func() {}, false
		}
	}
	return func() { _ = os.Remove(p.Lock()) }, true
}

// Ts formats a time like the agent (UTC, milliseconds, Z).
func Ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
