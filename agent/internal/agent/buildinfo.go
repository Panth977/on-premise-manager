package agent

import (
	"runtime"

	"opm/agent/internal/model"
	"opm/agent/internal/store"
)

// Build information (make dist ldflags, SPEC §21 #90 a): -X opm/agent/internal/agent.Version=… / .Commit=… / .BuiltAt=….
var (
	// Commit is the git commit of the build (git rev-parse --short HEAD); "" for dev builds.
	Commit = ""
	// BuiltAt is the RFC 3339 UTC build time (date -u); "" for dev builds.
	BuiltAt = ""
	// TestBrokenRun is set ONLY by the upgrade smoke test (agent/test/upgrade-smoke) to build a broken release whose
	// `run` exits at once (it still answers `version`, so it passes the self-test and must be rolled back). Empty in
	// every real build.
	TestBrokenRun = ""
)

// BuildMeta is `opm-agent version --json` (TS AgentBuildMeta, §21 #91).
func BuildMeta() model.AgentBuildMeta {
	m := model.AgentBuildMeta{Version: Version, Schema: store.SchemaVersion, MinSchema: store.MinSchema, OS: runtime.GOOS, Arch: runtime.GOARCH}
	if Commit != "" {
		c := Commit
		m.Commit = &c
	}
	if BuiltAt != "" {
		b := BuiltAt
		m.BuiltAt = &b
	}
	return m
}

// VersionLine is the `opm-agent version` output: "opm-agent <version> <os>/<arch> (commit …, built …, go…)". The
// upgrade self-test reads field 2.
func VersionLine(goos, goarch, goVersion string) string {
	c, b := Commit, BuiltAt
	if c == "" {
		c = "dev"
	}
	if b == "" {
		b = "unknown"
	}
	return "opm-agent " + Version + " " + goos + "/" + goarch + " (commit " + c + ", built " + b + ", " + goVersion + ")"
}
