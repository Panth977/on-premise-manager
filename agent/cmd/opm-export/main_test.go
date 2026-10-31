package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"opm/agent/internal/export"
	"opm/agent/internal/export/exporttest"
)

// TestBinaryMatchesLibrary: the command answers byte for byte what the exporter answers.
func TestBinaryMatchesLibrary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the command")
	}
	bin := filepath.Join(t.TempDir(), "opm-export")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	fx := exporttest.Build(exporttest.Options{})
	raw, _ := json.Marshal(fx.Req)
	c := exec.Command(bin)
	c.Stdin = bytes.NewReader(raw)
	got, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	res, err := export.Export(fx.Req, export.Mode{})
	if err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	enc := json.NewEncoder(&want)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(res)
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("output differs from the library (%d vs %d bytes)", len(got), want.Len())
	}
}
