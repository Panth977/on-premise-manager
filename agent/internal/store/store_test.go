package store

import (
	"path/filepath"
	"testing"
)

func TestKV(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "opm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v map[string]int
	ok, err := db.GetKV("x", &v)
	if err != nil || ok {
		t.Fatal("unexpected", ok, err)
	}
	if err := db.SetKV("x", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetKV("x", map[string]int{"a": 2}); err != nil {
		t.Fatal(err)
	}
	ok, err = db.GetKV("x", &v)
	if err != nil || !ok || v["a"] != 2 {
		t.Fatal(v, err)
	}
	_ = db.DelKV("x")
	ok, _ = db.GetKV("x", &v)
	if ok {
		t.Fatal("not deleted")
	}
	var mode string
	_ = db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal mode %s", mode)
	}
}
