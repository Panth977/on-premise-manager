package dlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// §21 #112 the reverse reader yields every line newest first across chunk boundaries and stops early.
func TestEachLineReverse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.jsonl")
	var b strings.Builder
	var want []string
	for i := 0; i < 5000; i++ {
		ln := fmt.Sprintf("line-%04d-%s", i, strings.Repeat("x", i%97))
		want = append(want, ln)
		b.WriteString(ln + "\n")
		if i%500 == 0 {
			b.WriteString("\n") // empty lines are skipped
		}
	}
	b.WriteString("tail-without-newline")
	want = append(want, "tail-without-newline")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	eachLineReverse(p, func(ln []byte) bool { got = append(got, string(ln)); return true })
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[len(want)-1-i] {
			t.Fatalf("line %d: %q, want %q", i, got[i], want[len(want)-1-i])
		}
	}
	n := 0
	eachLineReverse(p, func([]byte) bool { n++; return n < 3 })
	if n != 3 {
		t.Fatal(n)
	}
}
