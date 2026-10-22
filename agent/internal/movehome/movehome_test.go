package movehome

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o640); err != nil {
		t.Fatal(err)
	}
}

// §21 #119: checks (inside / not empty / same disk), a copy (verified, agent.json dataDir set, old home kept as
// <home>.moved-…, the state in the new home), a rename, and a failure (agent.json untouched, partial copy removed).
func TestMoveHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "opm")
	write(t, filepath.Join(home, "opm.db"), "db")
	write(t, filepath.Join(home, "volumes", "data", "a.txt"), "hello")
	_ = os.Symlink("a.txt", filepath.Join(home, "volumes", "data", "link"))
	cfg := filepath.Join(root, "agent.json")
	write(t, cfg, `{"apiBase":"https://x","dataDir":"`+home+`","siteSlug":"app"}`)
	floor := func(int64) int64 { return 0 }

	if _, _, errs, _ := Check(home, filepath.Join(home, "sub"), 10, floor); len(errs) == 0 {
		t.Fatal("inside the home must be refused")
	}
	busy := filepath.Join(root, "busy")
	write(t, filepath.Join(busy, "x"), "x")
	if _, _, errs, _ := Check(home, busy, 10, floor); len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), "not empty") {
		t.Fatalf("not empty: %v", errs)
	}
	if _, _, errs, _ := Check(home, "relative/x", 10, floor); len(errs) == 0 {
		t.Fatal("relative path")
	}
	same, _, errs, warns := Check(home, filepath.Join(root, "new"), 10, floor)
	if !same || len(errs) != 0 || len(warns) == 0 {
		t.Fatalf("same disk: %v %v %v", same, errs, warns)
	}

	// copy
	to := filepath.Join(root, "new")
	if err := WriteState(home, &State{MoveID: "m1", Status: "prepared", From: home, To: to, Mode: "copy"}); err != nil {
		t.Fatal(err)
	}
	var stops, starts int
	var unitsDir string
	err := Run(Options{ConfigPath: cfg, From: home, To: to, Stop: func() error { stops++; return nil }, Start: func() error { starts++; return nil }, Units: func(d string) error { unitsDir = d; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "volumes", "data", "a.txt")); string(b) != "hello" {
		t.Fatal("content not copied")
	}
	if l, err := os.Readlink(filepath.Join(to, "volumes", "data", "link")); err != nil || l != "a.txt" {
		t.Fatalf("symlink kept: %q %v", l, err)
	}
	if st, _ := os.Stat(filepath.Join(to, "opm.db")); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
	var c map[string]any
	b, _ := os.ReadFile(cfg)
	_ = json.Unmarshal(b, &c)
	if c["dataDir"] != to || c["siteSlug"] != "app" {
		t.Fatalf("agent.json %v", c)
	}
	st := ReadState(to)
	if st == nil || st.Status != "copied" || st.OldCopy == nil || !strings.Contains(*st.OldCopy, ".moved-") || st.Files != 3 {
		t.Fatalf("state %+v", st)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("the old home must be renamed away")
	}
	if _, err := os.Stat(*st.OldCopy); err != nil {
		t.Fatal("old copy kept")
	}
	if stops != 1 || starts != 1 || unitsDir != to {
		t.Fatalf("stops %d starts %d units %q", stops, starts, unitsDir)
	}

	// rename (same filesystem)
	to2 := filepath.Join(root, "new2")
	st.Status, st.From, st.To, st.Mode = "prepared", to, to2, "rename"
	_ = WriteState(to, st)
	if err := Run(Options{ConfigPath: cfg, From: to, To: to2}); err != nil {
		t.Fatal(err)
	}
	if ReadState(to2).Status != "copied" {
		t.Fatal("rename state")
	}

	// failure: no prepared state → refused; a copy that fails → agent.json untouched, partial removed, agent started
	if err := Run(Options{ConfigPath: cfg, From: to2, To: filepath.Join(root, "x")}); err == nil {
		t.Fatal("no prepared move")
	}
	bad := filepath.Join(root, "nofolder", "deep") // parent missing → MkdirAll creates it; force a failure with a file as parent
	write(t, filepath.Join(root, "nofile"), "f")
	bad = filepath.Join(root, "nofile", "deep")
	_ = WriteState(to2, &State{MoveID: "m3", Status: "prepared", From: to2, To: bad, Mode: "copy"})
	starts = 0
	if err := Run(Options{ConfigPath: cfg, From: to2, To: bad, Start: func() error { starts++; return nil }}); err == nil {
		t.Fatal("the failing copy must fail")
	}
	b, _ = os.ReadFile(cfg)
	_ = json.Unmarshal(b, &c)
	if c["dataDir"] != to2 || starts != 1 || ReadState(to2).Status != "failed" {
		t.Fatalf("after failure: %v starts %d %+v", c["dataDir"], starts, ReadState(to2))
	}
}
