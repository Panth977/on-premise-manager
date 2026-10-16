package export

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"

	"opm/agent/internal/expr"
)

// Symbolic evaluation: a filter applied to a value that holds a deferred input (a value that exists only on the machine,
// e.g. a secret) is not computed. It becomes a derived value OPM_D_<12 hex> = filter(input), recorded as a step;
// deploy.sh computes the steps on the machine with standard tools (busybox, the official mosquitto image). So every
// output of the export is the plain text of the config with ${NAME} placeholders: no OPM code runs on the machine.

// step is one derived value: Filter applied to Input (a template with ${NAME} placeholders of inputs / other steps).
type step struct {
	Name   string
	Filter string
	Args   []any
	Input  string
}

var (
	symMu  sync.Mutex // one export at a time owns the hook state
	symCur map[string]*step
)

var (
	reSentA = regexp.MustCompile(`«OpmA:([A-Z0-9_]+):aMpO»`)
	reSentB = regexp.MustCompile(`‹oPMb~([A-Z0-9_]+)~bmPo~›`)
)

func init() { expr.Symbolic = symbolic }

// canonical replaces every sentinel (either set) by ${NAME}.
func canonicalSentinels(s string) string {
	s = reSentA.ReplaceAllString(s, "$${$1}")
	return reSentB.ReplaceAllString(s, "$${$1}")
}

func symbolic(filter string, args []any, in string) (string, bool) {
	steps := symCur
	if steps == nil || !strings.ContainsAny(in, "«‹") {
		return "", false
	}
	hasA, hasB := reSentA.MatchString(in), reSentB.MatchString(in)
	if !hasA && !hasB {
		return "", false
	}
	canon := canonicalSentinels(in)
	a, _ := json.Marshal(args)
	h := sha256.Sum256([]byte(filter + "\x00" + string(a) + "\x00" + canon))
	name := "OPM_D_" + strings.ToUpper(hex.EncodeToString(h[:6]))
	if steps[name] == nil {
		steps[name] = &step{Name: name, Filter: filter, Args: args, Input: canon}
	}
	// set A only when the input holds set A only: a mixed input (the "is this input used" passes) must differ from A
	if hasA && !hasB {
		return sentinelA(name), true
	}
	return sentinelB(name), true
}

// withSymbolic runs fn with the hook recording into steps.
func withSymbolic(steps map[string]*step, fn func()) {
	symMu.Lock()
	defer symMu.Unlock()
	symCur = steps
	defer func() { symCur = nil }()
	fn()
}

var reRef = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

// refsOf lists the ${NAME} references of a canonical template.
func refsOf(s string) []string {
	var out []string
	for _, m := range reRef.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// closure: the steps needed by the names used (transitively, through step inputs), in dependency order.
func (x *exporter) closure(used map[string]bool) []*step {
	var order []*step
	seen := map[string]bool{}
	var visit func(n string)
	visit = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		s := x.steps[n]
		if s == nil {
			return
		}
		for _, r := range refsOf(s.Input) {
			visit(r)
		}
		order = append(order, s)
	}
	names := make([]string, 0, len(used))
	for n := range used {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		visit(n)
	}
	return order
}

// tokens turns a pass-A output into a deploy.sh template: every sentinel → @@{NAME}@@ (json = JSON-string escaped).
func tokens(s string, json bool) string {
	repl := "@@{$1}@@"
	if json {
		repl = "@@{$1|json}@@"
	}
	return reSentA.ReplaceAllString(s, repl)
}

// namesIn lists the names a pass-A output uses.
func namesIn(s string, into map[string]bool) {
	for _, m := range reSentA.FindAllStringSubmatch(s, -1) {
		into[m[1]] = true
	}
}
