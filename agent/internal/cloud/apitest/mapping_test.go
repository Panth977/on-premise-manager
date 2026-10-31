package apitest

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// goSubtests lists the t.Run paths of the *_test.go files of this package, by their literal names.
func goSubtests(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	NoErr(t, err)
	fset := token.NewFileSet()
	out := map[string]string{}
	var walk func(n ast.Node, prefix []string, file string)
	walk = func(n ast.Node, prefix []string, file string) {
		ast.Inspect(n, func(x ast.Node) bool {
			c, ok := x.(*ast.CallExpr)
			if !ok || x == n {
				return true
			}
			s, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || s.Sel.Name != "Run" || len(c.Args) != 2 {
				return true
			}
			lit, ok := c.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(lit.Value)
			NoErr(t, err)
			p := append(append([]string{}, prefix...), name)
			out[strings.Join(p, " / ")] = file
			walk(c.Args[1], p, file)
			return false
		})
	}
	for _, f := range files {
		a, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		NoErr(t, err)
		for _, d := range a.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && strings.HasPrefix(fn.Name.Name, "Test") && fn.Body != nil {
				walk(fn.Body, nil, f)
			}
		}
	}
	return out
}

func tsvLines(t *testing.T, file string) [][]string {
	t.Helper()
	f, err := os.Open(file)
	NoErr(t, err)
	defer f.Close()
	var out [][]string
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		if l := s.Text(); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, strings.Split(l, "\t"))
		}
	}
	return out
}

// TestMappingNoTestLost holds the suite against the TypeScript one it replaced: every test of testdata/ts-tests.txt is a
// subtest with the same name path here, or a row of testdata/ts-dropped.txt with a stated reason. Nothing else is allowed.
func TestMappingNoTestLost(t *testing.T) {
	ts := tsvLines(t, "testdata/ts-tests.txt")
	if len(ts) < 200 {
		t.Fatalf("manifest too short: %d", len(ts))
	}
	known := map[string]bool{}
	for _, r := range ts {
		known[r[1]] = true
	}
	dropped := map[string]string{}
	for _, f := range mustGlob(t, "testdata/ts-dropped*.txt") {
		for _, r := range tsvLines(t, f) {
			if len(r) != 3 || strings.TrimSpace(r[2]) == "" {
				t.Fatalf("%s: a dropped test needs <file><TAB><path><TAB><reason>: %q", f, r)
			}
			if !known[r[1]] {
				t.Errorf("%s: dropped test is not in the manifest: %s", f, r[1])
			}
			dropped[r[1]] = r[2]
		}
	}
	got := goSubtests(t)
	var lost []string
	ported := 0
	for _, r := range ts {
		_, g := got[r[1]]
		_, d := dropped[r[1]]
		switch {
		case g && d:
			t.Errorf("both ported and dropped: %s", r[1])
		case g:
			ported++
		case !d:
			lost = append(lost, r[0]+": "+r[1])
		}
	}
	sort.Strings(lost)
	if len(lost) > 0 {
		t.Errorf("%d TypeScript tests have no Go test and no reason:\n  %s", len(lost), strings.Join(lost, "\n  "))
	}
	t.Logf("TypeScript tests %d: ported %d, dropped with a reason %d; Go subtests %d", len(ts), ported, len(dropped), len(got))
}

func mustGlob(t *testing.T, p string) []string {
	t.Helper()
	l, err := filepath.Glob(p)
	NoErr(t, err)
	return l
}
