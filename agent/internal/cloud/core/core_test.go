package core

import (
	"testing"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

func TestImportTrigger(t *testing.T) {
	calls := 0
	orig := runImport
	runImport = func(s, v string) {
		calls++
		if s != "web" || v != "1" {
			t.Fatal(s, v)
		}
	}
	defer func() { runImport = orig }()
	st := func(s string) *doc.Map { return doc.Obj("import", doc.Obj("status", s)) }
	for _, c := range []struct {
		before, after *doc.Map
		want          int
	}{{nil, st("pending"), 1}, {st("failed"), st("pending"), 1}, {st("pending"), st("pending"), 0}, {nil, st("ready"), 0}, {st("pending"), nil, 0}} {
		calls = 0
		if err := onImageWritten(driver.DocEvent{Params: map[string]string{"serviceSlug": "web", "version": "1"}, Before: c.before, After: c.after}); err != nil || calls != c.want {
			t.Fatal(err, calls, c.want)
		}
	}
}
