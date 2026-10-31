// Command opm-cloud is the OPM cloud as one process for a VM: the Go replacement of backend/src/server.ts.
//
//	opm-cloud [serve]            API (/v1) + /auth + /_blob + jobs + outbox dispatcher + static frontend
//	opm-cloud create-admin --email E [--password P] [--name N]
//	opm-cloud reset-link --email E
//	opm-cloud release-key
//	opm-cloud upload-agent --dir D --version V [--latest]
//	opm-cloud upload-deps [--docker-version 28.5.2] [--agent-version V] [--agent-notes T] [--from-dir D]
//	opm-cloud backup --out DIR
//	opm-cloud verify-schema [--require-branches] [--require-no-working-copy]   (both on by default)
package main

import (
	"fmt"
	"os"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/driver"
	_ "opm/agent/internal/cloud/routes"
)

func fail(err error) {
	driver.Log.Error("opm-cloud failed", "error", fmt.Sprintf("%+v", err))
	os.Exit(1)
}

func main() {
	argv := os.Args[1:]
	cmd := "serve"
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "--") {
		cmd = argv[0]
	}
	cfg, err := cloud.LoadConfig(os.Args)
	if err != nil {
		fail(err)
	}
	if err := cloud.ApplyEnv(cfg); err != nil {
		fail(err)
	}
	if cmd == "serve" {
		if err := cloud.Serve(cfg); err != nil {
			fail(err)
		}
		return
	}
	if _, err := cloud.InstallPorts(cfg); err != nil {
		fail(err)
	}
	code, err := cloud.RunOp(cmd, cfg, os.Args)
	if err != nil {
		fail(err)
	}
	os.Exit(code)
}
