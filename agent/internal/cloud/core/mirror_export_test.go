package core

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"opm/agent/internal/export/exporttest"
)

const wasiRunner = `
const { WASI } = require('node:wasi');
const fs = require('node:fs');
(async () => {
  const [wasm, inP, outP] = process.argv.slice(1);
  const fin = fs.openSync(inP, 'r'), fout = fs.openSync(outP, 'w'), ferr = fs.openSync(outP + '.err', 'w');
  const wasi = new WASI({ version: 'preview1', args: ['opm-export'], env: {}, stdin: fin, stdout: fout, stderr: ferr, returnOnExit: true });
  const mod = await WebAssembly.compile(fs.readFileSync(wasm));
  const inst = await WebAssembly.instantiate(mod, wasi.getImportObject());
  process.exit(wasi.start(inst));
})();`

// OPM_EXPORT_WASM=<opm-export.wasm>: the in-process exporter answers the same bytes as the wasm build run the way
// mirror-export.ts ran it (node:wasi).
func TestExporterSameAsWasm(t *testing.T) {
	wasm := os.Getenv("OPM_EXPORT_WASM")
	if wasm == "" {
		t.Skip("OPM_EXPORT_WASM not set")
	}
	fx := exporttest.Build(exporttest.Options{})
	raw, err := json.Marshal(fx.Req)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in, out := dir+"/in.json", dir+"/out.json"
	if err := os.WriteFile(in, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", "--no-warnings", "-e", wasiRunner, wasm, in, out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wasm: %v\n%s", err, b)
	}
	want, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got, code, msg := runExport(raw)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the trees differ (%d / %d bytes)", len(got), len(want))
	}
	t.Logf("%d bytes identical", len(got))
}
