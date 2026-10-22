package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"opm/agent/internal/movehome"
)

// §21 #119: the check refuses a folder inside the home; after a move into this home the start
// hook rewrites the absolute paths kept in opm.db and records agent.home-moved; a failed move starts its containers.
func TestMoveHomeAgentSide(t *testing.T) {
	a := newVSAgent(t)
	ck := a.checkMove(filepath.Join(a.dataDir, "inner"))
	if ck.Ok || len(ck.Errors) == 0 {
		t.Fatalf("inside the home: %+v", ck)
	}
	if !strings.Contains(strings.Join(ck.Errors, ";"), "inside the current one") {
		t.Fatalf("inside the home must be named: %v", ck.Errors)
	}
	old := "/old/opm"
	_, _ = a.db.Exec(`INSERT INTO blobs(sha256, sizeBytes, kind, source, haveSince, path, lastUsedAt) VALUES('x', 1, 'file', 'cloud', 0, ?, 0)`, old+"/blobs/files/ab")
	_, _ = a.db.Exec(`INSERT INTO blobs(sha256, sizeBytes, kind, source, haveSince, path, lastUsedAt) VALUES('y', 1, 'file', 'cloud', 0, '/elsewhere/z', 0)`)
	if err := movehome.WriteState(a.dataDir, &movehome.State{MoveID: "m", Status: "copied", From: old, To: a.dataDir, Mode: "copy", Stopped: []string{}}); err != nil {
		t.Fatal(err)
	}
	a.finishHomeMove()
	var p1, p2 string
	_ = a.db.QueryRow(`SELECT path FROM blobs WHERE sha256 = 'x'`).Scan(&p1)
	_ = a.db.QueryRow(`SELECT path FROM blobs WHERE sha256 = 'y'`).Scan(&p2)
	if p1 != filepath.Clean(a.dataDir)+"/blobs/files/ab" || p2 != "/elsewhere/z" {
		t.Fatalf("paths %q %q", p1, p2)
	}
	if st := movehome.ReadState(a.dataDir); st == nil || st.Status != "done" {
		t.Fatalf("state %+v", st)
	}
	if len(activityData(t, a, "agent.home-moved")) != 1 {
		t.Fatal("agent.home-moved")
	}
	a.finishHomeMove() // done: nothing again
	if len(activityData(t, a, "agent.home-moved")) != 1 {
		t.Fatal("once")
	}
}
