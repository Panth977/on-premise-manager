package model

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"opm/agent/internal/canonical"
)

// §21 #101: a revision config with tree / single seeds + persist re-hashes to the TS hash (fixture generated with
// shared: testdata/gen-volume-content-config.mjs), survives a store round trip, and persist stays absent when off.
func TestVolumeContentRevisionHash(t *testing.T) {
	b, err := os.ReadFile("testdata/volume-content-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hash   string          `json:"hash"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(doc.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if h, _ := canonical.HashCanonical(cfg); h != doc.Hash {
		t.Fatalf("Go hash %s, TS hash %s", h, doc.Hash)
	}
	out, _ := json.Marshal(cfg)
	var again Config
	_ = json.Unmarshal(out, &again)
	if h, _ := canonical.HashCanonical(again); h != doc.Hash {
		t.Fatal("hash changed after a store round trip")
	}
	app := again.Sites[1].Config
	nr := app.FindVolume("nodered-data")
	sd := nr.SeedOf()
	if sd == nil || sd.Kind != "tree" || len(sd.Tree) != 3 || sd.Tree[1].Mode != "0755" || sd.Tree[2].Render != "expressions" || len(sd.Dirs) != 1 {
		t.Fatalf("tree seed %+v", sd)
	}
	if nr.Persist == nil || nr.Persist.MaxBytes != 52428800 || !nr.Persist.Auto || len(nr.Persist.Exclude) != 2 {
		t.Fatalf("persist %+v", nr.Persist)
	}
	one := app.FindVolume("one").SeedOf()
	if one.Kind != "single" || one.Size != 4 || one.Mode != "0600" || one.Render != "none" {
		t.Fatalf("single %+v", one)
	}
	if v := app.FindVolume("hello-api-data"); v.Persist != nil {
		t.Fatal("persist must be nil when off")
	}
	vb, _ := json.Marshal(*app.FindVolume("hello-api-data"))
	if strings.Contains(string(vb), "persist") {
		t.Fatal("persist key written for a volume without persist")
	}
}
