package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSchemaVersioning(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "opm.db")
	// a fresh DB: no backup, user_version = SchemaVersion
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.UserVersion() != SchemaVersion {
		t.Fatal("user_version")
	}
	_ = d.SetKV("lastAgentVersion", "0.9.0-10080438")
	if _, err := d.Exec(`PRAGMA user_version = 0`); err != nil { // pretend: an unversioned pre-0.10 DB
		t.Fatal(err)
	}
	d.Close()
	if _, err := os.Stat(filepath.Join(dir, "backup")); err == nil {
		t.Fatal("a fresh DB must not be backed up")
	}
	// unversioned → backup labelled with the previous agent version, then migrated
	d, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "backup"))
	if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "0.9.0-10080438-") || d.UserVersion() != SchemaVersion {
		t.Fatalf("backup %v", ents)
	}
	if _, err := os.Stat(filepath.Join(dir, "backup", ents[0].Name(), "opm.db")); err != nil {
		t.Fatal("backup lacks opm.db")
	}
	var ok bool
	var s string
	if ok, _ = d.GetKV("lastAgentVersion", &s); !ok {
		t.Fatal("data kept")
	}
	// a newer DB is refused
	_, _ = d.Exec(`PRAGMA user_version = 99`)
	d.Close()
	_, err = Open(p)
	var ne *ErrNewerSchema
	if !errors.As(err, &ne) || ne.DBSchema != 99 || !strings.Contains(err.Error(), "newer than this agent") {
		t.Fatalf("newer: %v", err)
	}
	raw, _ := sql.Open("sqlite", "file:"+p)
	_, _ = raw.Exec(`PRAGMA user_version = 1`)
	raw.Close()
}

func TestPruneBackups(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 8; i++ {
		d := filepath.Join(root, string(rune('a'+i)))
		_ = os.MkdirAll(d, 0o700)
		tm := timeAt(i)
		_ = os.Chtimes(d, tm, tm)
	}
	PruneBackups(root, 5)
	ents, _ := os.ReadDir(root)
	if len(ents) != 5 || ents[0].Name() != "d" {
		t.Fatalf("%v", ents)
	}
}

func timeAt(i int) (t time.Time) { return time.Unix(1700000000+int64(i)*60, 0) }
