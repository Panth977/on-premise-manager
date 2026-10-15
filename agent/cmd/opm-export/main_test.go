package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"opm/agent/internal/export"
	"opm/agent/internal/export/exporttest"
)

// runWasm is the Cloud Functions path: node:wasi runs opm-export.wasm with stdin = the request, stdout = the result.
const runWasm = `
const { WASI } = require('node:wasi');
const fs = require('node:fs');
const [wasm, inPath, outPath] = process.argv.slice(2);
const stdin = fs.openSync(inPath, 'r'), stdout = fs.openSync(outPath, 'w');
const wasi = new WASI({ version: 'preview1', args: ['opm-export'], env: {}, stdin, stdout, stderr: 2, returnOnExit: true });
(async () => {
  const mod = await WebAssembly.compile(fs.readFileSync(wasm));
  const inst = await WebAssembly.instantiate(mod, wasi.getImportObject());
  const code = wasi.start(inst);
  fs.closeSync(stdout);
  process.exit(code);
})().catch((e) => { console.error(e); process.exit(3); });
`

// TestWasmMatchesNative: the wasip1 build under node:wasi answers byte for byte what the native exporter answers.
func TestWasmMatchesNative(t *testing.T) {
	if testing.Short() {
		t.Skip("builds wasm")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	wasm := filepath.Join(dir, "opm-export.wasm")
	b := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -buildid=", "-o", wasm, ".")
	b.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("build wasm: %v %s", err, out)
	}
	fx := exporttest.Build(exporttest.Options{})
	raw, _ := json.Marshal(fx.Req)
	in, out, js := filepath.Join(dir, "in.json"), filepath.Join(dir, "out.json"), filepath.Join(dir, "run.cjs")
	_ = os.WriteFile(in, raw, 0o600)
	_ = os.WriteFile(js, []byte(runWasm), 0o600)
	if o, err := exec.Command("node", "--no-warnings", js, wasm, in, out).CombinedOutput(); err != nil {
		t.Fatalf("node wasi: %v %s", err, o)
	}
	got, _ := os.ReadFile(out)
	res, err := export.Export(fx.Req, export.Mode{})
	if err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	enc := json.NewEncoder(&want)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(res)
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("wasm output differs from native (%d vs %d bytes)", len(got), want.Len())
	}
	st, _ := os.Stat(wasm)
	t.Logf("opm-export.wasm %d bytes; output %d bytes, %d files — identical to the native export", st.Size(), len(got), len(res.Files))
}
