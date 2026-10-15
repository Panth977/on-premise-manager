package dlog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogRedactQuery(t *testing.T) {
	dir := t.TempDir()
	var echo bytes.Buffer
	l := New(dir, "debug", &echo)
	l.AddSecrets("SuperSecretValue")
	l.Info("edge", "reloaded", "hosts", 2, "apiToken", "abc", "env", "X=SuperSecretValue")
	l.Warn("driver", "container SuperSecretValue failed", "n", 1)
	l.Debug("mesh", "ping")
	l.Close()
	raw, _ := os.ReadFile(filepath.Join(dir, "agent-"+time.Now().UTC().Format("2006-01-02")+".jsonl"))
	if strings.Contains(string(raw), "SuperSecretValue") || strings.Contains(string(raw), `"abc"`) {
		t.Fatalf("secret leaked: %s", raw)
	}
	lines := l.Read(Query{})
	if len(lines) != 3 || lines[0].Module != "mesh" {
		t.Fatalf("lines %+v", lines)
	}
	if got := l.Read(Query{Level: "warn"}); len(got) != 1 || got[0].Module != "driver" {
		t.Fatalf("warn %+v", got)
	}
	if got := l.Read(Query{Module: "edge"}); len(got) != 1 {
		t.Fatal("module filter")
	}
	if got := l.Read(Query{Q: "PING"}); len(got) != 1 {
		t.Fatal("q filter")
	}
	if !strings.Contains(echo.String(), "WARN") || strings.Contains(echo.String(), "reloaded") {
		t.Fatalf("echo %s", echo.String())
	}
	var buf bytes.Buffer
	if n, _ := l.Export(&buf, time.Time{}, time.Time{}); n == 0 {
		t.Fatal("export empty")
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "agent-2000-01-01.jsonl")
	_ = os.WriteFile(old, []byte("{}\n"), 0o600)
	l := New(dir, "info", nil)
	l.Info("gui", "x")
	l.Rotate()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old file kept")
	}
}
