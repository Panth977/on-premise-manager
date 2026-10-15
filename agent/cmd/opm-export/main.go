// Command opm-export is the §21 #106 exporter for the cloud: stdin = ExportRequest JSON, stdout = ExportResult JSON
// (shared/src/github-mirror.ts). Built for GOOS=wasip1 GOARCH=wasm and run by Cloud Functions with node:wasi; also
// builds natively (tests, scripts). Exit 1 + a message on stderr on error.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"opm/agent/internal/export"
)

func main() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fail(err)
	}
	var req export.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		fail(fmt.Errorf("bad request JSON: %w", err))
	}
	res, err := export.Export(req, export.Mode{})
	if err != nil {
		fail(err)
	}
	w := bufio.NewWriterSize(os.Stdout, 1<<20)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		fail(err)
	}
	if err := w.Flush(); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "opm-export:", err)
	os.Exit(1)
}
