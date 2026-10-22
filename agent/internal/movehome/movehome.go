// Package movehome moves the agent home (agent.json dataDir: opm.db, blobs, snapshots, the auto volumes, file and
// secret copies, certificates, logs) to another folder — usually another disk (SPEC §21 #119). The agent prepares it
// (checks, stops this site's containers, writes the state file) and hands over to `opm-agent move-home`, which runs
// while the agent is stopped: same filesystem → one rename; another filesystem → `cp -a`, a verification of every file,
// then the old folder is renamed `<home>.moved-<time>` (kept until a person deletes it). agent.json dataDir and the
// systemd units follow; the agent's next start finishes the move (paths in opm.db, the containers re-applied).
package movehome

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// StateFile is written into the home being moved (and copied with it).
const StateFile = "move-home.json"

// State is the progress of one move (TS MoveHomeState).
type State struct {
	MoveID     string   `json:"moveId"`
	Status     string   `json:"status"` // prepared | copying | copied | done | failed
	From       string   `json:"from"`
	To         string   `json:"to"`
	Mode       string   `json:"mode"` // rename | copy
	Actor      string   `json:"actor"`
	StartedAt  string   `json:"startedAt"`
	FinishedAt *string  `json:"finishedAt"`
	SizeBytes  int64    `json:"sizeBytes"`
	Files      int      `json:"files"`
	Stopped    []string `json:"stopped"` // containerSlugs stopped for the move
	OldCopy    *string  `json:"oldCopy"` // the old home kept after a copy (delete it to free the space)
	Error      *string  `json:"error"`
	Restarted  bool     `json:"restarted,omitempty"` // a failed move: its stopped containers were started again
}

// ReadState reads <home>/move-home.json (nil = none).
func ReadState(home string) *State {
	b, err := os.ReadFile(filepath.Join(home, StateFile))
	if err != nil {
		return nil
	}
	var s State
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

// WriteState writes <home>/move-home.json atomically.
func WriteState(home string, s *State) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := filepath.Join(home, StateFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(home, StateFile))
}

// Fs is one filesystem: total / free bytes and its device id (the same id = the same filesystem).
type Fs struct {
	Dev   uint64
	Total int64
	Free  int64
}

// FsOf: the filesystem holding p (p or its nearest existing parent).
func FsOf(p string) (Fs, bool) {
	for d := p; ; d = filepath.Dir(d) {
		var st syscall.Statfs_t
		var fi syscall.Stat_t
		if syscall.Stat(d, &fi) == nil && syscall.Statfs(d, &st) == nil {
			return Fs{Dev: uint64(fi.Dev), Total: int64(st.Blocks) * int64(st.Bsize), Free: int64(st.Bavail) * int64(st.Bsize)}, true
		}
		if d == filepath.Dir(d) {
			return Fs{}, false
		}
	}
}

// Size walks a tree: regular file bytes + count (symlinks / special files are not followed).
func Size(root string) (int64, int, error) {
	var n int64
	var c int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
				c++
			}
		}
		return nil
	})
	return n, c, err
}

// Check validates a move of `from` (size bytes) to `to`: errors refuse it, warnings are shown.
func Check(from, to string, size int64, floor func(total int64) int64) (sameFs bool, target Fs, errs, warns []string) {
	if !filepath.IsAbs(to) {
		return false, Fs{}, []string{"the new folder must be an absolute path (e.g. /data/opm)"}, nil
	}
	to = filepath.Clean(to)
	from = filepath.Clean(from)
	switch {
	case to == from:
		errs = append(errs, "that is the current folder")
	case strings.HasPrefix(to+"/", from+"/"):
		errs = append(errs, "the new folder cannot be inside the current one")
	case strings.HasPrefix(from+"/", to+"/"):
		errs = append(errs, "the new folder cannot contain the current one")
	}
	if to == "/" {
		errs = append(errs, "not the root folder: give the agent home a folder of its own, e.g. /data/opm")
	}
	parent := filepath.Dir(to)
	if st, err := os.Stat(parent); err != nil || !st.IsDir() {
		errs = append(errs, fmt.Sprintf("the parent folder %s does not exist (create it, or mount the disk first)", parent))
	}
	if st, err := os.Lstat(to); err == nil {
		if !st.IsDir() {
			errs = append(errs, to+" exists and is not a folder")
		} else if ents, _ := os.ReadDir(to); len(ents) > 0 {
			errs = append(errs, to+" is not empty")
		}
	}
	src, ok1 := FsOf(from)
	dst, ok2 := FsOf(to)
	if !ok1 || !ok2 {
		errs = append(errs, "cannot read the filesystems")
		return false, dst, errs, warns
	}
	sameFs = src.Dev == dst.Dev
	if sameFs {
		warns = append(warns, "the new folder is on the SAME disk: the move is a rename and frees no space")
	} else if need := size + size/20 + floor(dst.Total); dst.Free < need {
		errs = append(errs, fmt.Sprintf("not enough free space there: %s free, the agent home needs %s (+ 5 %% and the free-space floor)", human(dst.Free), human(size)))
	}
	return sameFs, dst, errs, warns
}

func human(n int64) string {
	f := float64(n)
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", f/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", f/(1<<20))
	}
	return fmt.Sprintf("%d KiB", n>>10)
}

// Options of Run (the `opm-agent move-home` process; the agent is stopped).
type Options struct {
	ConfigPath string
	From, To   string
	Stop       func() error // stop the agent service
	Start      func() error // start it again (also after a failure)
	Units      func(dataDir string) error
	Log        func(format string, a ...any)
	Now        func() time.Time
}

// Run moves the home. On failure the agent keeps the old home (agent.json untouched, a partial copy removed), the
// failure is recorded in the old home's state file and the agent is started again.
func Run(o Options) error {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	st := ReadState(o.From)
	if st == nil || st.Status != "prepared" || filepath.Clean(st.To) != filepath.Clean(o.To) {
		return fmt.Errorf("no prepared move of %s to %s (start it from the agent GUI)", o.From, o.To)
	}
	fail := func(err error, cleanup bool) error {
		if cleanup && st.Mode == "copy" {
			_ = os.RemoveAll(o.To)
		}
		msg := err.Error()
		ts := o.Now().UTC().Format(time.RFC3339)
		st.Status, st.Error, st.FinishedAt = "failed", &msg, &ts
		_ = WriteState(o.From, st)
		o.Log("move failed: %s", msg)
		if o.Start != nil {
			_ = o.Start()
		}
		return err
	}
	if o.Stop != nil {
		if err := o.Stop(); err != nil {
			return fail(fmt.Errorf("stop the agent: %w", err), false)
		}
	}
	st.Status = "copying"
	_ = WriteState(o.From, st)
	if st.Mode == "rename" {
		if err := os.Rename(o.From, o.To); err != nil {
			return fail(fmt.Errorf("rename: %w", err), false)
		}
	} else {
		info, err := os.Stat(o.From)
		if err != nil {
			return fail(err, false)
		}
		if err := os.MkdirAll(o.To, info.Mode().Perm()); err != nil {
			return fail(err, false)
		}
		var eb bytes.Buffer
		cmd := exec.Command("cp", "-a", o.From+"/.", o.To+"/")
		cmd.Stderr = &eb
		o.Log("copying %s → %s", o.From, o.To)
		if err := cmd.Run(); err != nil {
			return fail(fmt.Errorf("cp -a: %v: %s", err, strings.TrimSpace(eb.String())), true)
		}
		sa, ca, err1 := Size(o.From)
		sb, cb, err2 := Size(o.To)
		if err1 != nil || err2 != nil || sa != sb || ca != cb {
			return fail(fmt.Errorf("the copy does not match: %d files / %d bytes vs %d / %d", ca, sa, cb, sb), true)
		}
		st.SizeBytes, st.Files = sa, ca
	}
	if err := SetDataDir(o.ConfigPath, o.To); err != nil {
		if st.Mode == "rename" {
			_ = os.Rename(o.To, o.From)
		}
		return fail(fmt.Errorf("agent.json: %w", err), true)
	}
	if st.Mode == "copy" {
		old := fmt.Sprintf("%s.moved-%s", o.From, o.Now().UTC().Format("20060102-150405"))
		if err := os.Rename(o.From, old); err == nil {
			st.OldCopy = &old
		} else {
			o.Log("the old home stays at %s: %v", o.From, err)
			st.OldCopy = &o.From
		}
	}
	st.Status = "copied"
	if err := WriteState(o.To, st); err != nil {
		o.Log("state: %v", err)
	}
	if o.Units != nil {
		if err := o.Units(o.To); err != nil {
			o.Log("systemd units: %v", err)
		}
	}
	if o.Start != nil {
		if err := o.Start(); err != nil {
			return fmt.Errorf("moved, but the agent did not start: %w", err)
		}
	}
	o.Log("moved %s → %s", o.From, o.To)
	return nil
}

// SetDataDir sets agent.json dataDir, keeping every other key and the file mode (a .bak copy first).
func SetDataDir(configPath, dir string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	v, _ := json.Marshal(dir)
	m["dataDir"] = v
	out, _ := json.MarshalIndent(m, "", "  ")
	perm := os.FileMode(0o600)
	if st, err := os.Stat(configPath); err == nil {
		perm = st.Mode().Perm()
	}
	_ = os.WriteFile(configPath+".bak", raw, perm)
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), perm); err != nil {
		return err
	}
	return os.Rename(tmp, configPath)
}
