package shared

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"opm/agent/internal/cloud/doc"
)

func sharedDir(t testing.TB) string {
	_, f, _, _ := runtime.Caller(0)
	d := filepath.Join(filepath.Dir(f), "../../../../shared")
	if _, err := os.Stat(filepath.Join(d, "node_modules/tsx")); err != nil {
		t.Skip("shared/node_modules missing (cd shared && npm ci)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	return d
}

func tsx(t testing.TB, script string, args ...string) []byte {
	t.Helper()
	_, f, _, _ := runtime.Caller(0)
	cmd := exec.Command("node", append([]string{"--import", "tsx", "--no-warnings=ExperimentalWarning", filepath.Join(filepath.Dir(f), script)}, args...)...)
	cmd.Dir = sharedDir(t)
	var out, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &e
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", script, err, e.String())
	}
	return out.Bytes()
}

func brMapOf(v any) *doc.Map { m, _ := v.(*doc.Map); return m }
func arrOf(v any) []any      { a, _ := v.([]any); return a }
func strOf(v any) string     { s, _ := v.(string); return s }
func slotOf(v any) Slot {
	m := brMapOf(v)
	return Slot{Has: doc.Truthy(m.Val("has")), V: m.Val("v")}
}
func brStrsOf(v any) []string {
	out := []string{}
	for _, x := range arrOf(v) {
		out = append(out, x.(string))
	}
	return out
}

// runCall dispatches one vector to the Go function.
func runCall(fn string, a []any) (any, error) {
	m := func(i int) *doc.Map { return brMapOf(a[i]) }
	switch fn {
	case "planMerge":
		return PlanMerge(m(0), m(1), m(2), m(3)), nil
	case "applyMerge":
		return ApplyMerge(m(0), m(1), m(2), arrOf(a[3]), m(4)), nil
	case "rebaseChanges":
		return RebaseChanges(m(0), m(1), m(2), arrOf(a[3]), m(4)), nil
	case "nextSkips":
		return NextSkips(arrOf(a[0]), arrOf(a[1]), a[2], strOf(a[3]), strOf(a[4])), nil
	case "isFastForward":
		return IsFastForward(arrOf(a[0])), nil
	case "countMerge":
		return CountMerge(arrOf(a[0])), nil
	case "cleanSourceChoices":
		return CleanSourceChoices(arrOf(a[0])), nil
	case "removalChoices":
		return RemovalChoices(arrOf(a[0])), nil
	case "suggestMessage":
		return SuggestMessage(arrOf(a[0]), strOf(a[1])), nil
	case "uncommittedItems":
		return UncommittedItems(m(0), m(1), m(2)), nil
	case "uncommittedCount":
		return float64(UncommittedCount(m(0), m(1), m(2))), nil
	case "commitStats":
		return CommitStats(m(0), m(1), m(2)), nil
	case "revertPlan":
		return RevertPlan(m(0), m(1), m(2), m(3)), nil
	case "applyRevert":
		return ApplyRevert(m(0), m(1), m(2), arrOf(a[3]), m(4)), nil
	case "resetPlan":
		return ResetPlan(m(0), m(1), m(2)), nil
	case "mergeMissing":
		return MergeMissing(m(0)), nil
	case "secretValueChanges":
		return SecretValueChanges(arrOf(a[0]), arrOf(a[1]), brStrsOf(a[2])), nil
	case "withReferenceSites":
		return WithReferenceSites(m(0), m(1), strOf(a[2])), nil
	case "diffConfigs":
		return DiffConfigs(m(0), m(1), nil, nil), nil
	case "diffConfigsFns":
		l := brStrsOf(a[2])
		return DiffConfigs(m(0), m(1), func(s string) string { return "svc-" + s }, func(s string) bool { return brContains(l, s) }), nil
	case "planTemplateSync":
		return PlanTemplateSync(m(0), m(1), m(2), arrOf(a[3])), nil
	case "countTemplateSync":
		return CountTemplateSync(arrOf(a[0])), nil
	case "cleanTemplateChoices":
		return CleanTemplateChoices(arrOf(a[0])), nil
	case "nextSkipRecords":
		return NextSkipRecords(arrOf(a[0]), arrOf(a[1]), a[2], strOf(a[3]), strOf(a[4])), nil
	case "applyTemplateSync":
		return ApplyTemplateSync(m(0), m(1), m(2), arrOf(a[3]), m(4)), nil
	case "templateSyncMissing":
		return TemplateSyncMissing(m(0)), nil
	case "templateConfig":
		return TemplateConfig(m(0)), nil
	case "branchKey":
		return BranchKey(m(0)), nil
	case "branchKeyDocId":
		return BranchKeyDocId(strOf(a[0])), nil
	case "sameBranch":
		return SameBranch(m(0), m(1)), nil
	case "pointParam":
		return PointParam(m(0)), nil
	case "parseBranchKey":
		if r := ParseBranchKey(strOf(a[0])); r != nil {
			return r, nil
		}
		return nil, nil
	case "parsePointParam":
		if r := ParsePointParam(strOf(a[0])); r != nil {
			return r, nil
		}
		return nil, nil
	case "pointLabel":
		return PointLabel(strOf(a[0]), a[1]), nil
	case "createMessage":
		return CreateMessage(strOf(a[0]), a[1]), nil
	case "commitSummary":
		return CommitSummary(strOf(a[0])), nil
	case "checkCommitMessage":
		return CheckCommitMessage(a[0], strOf(a[1])), nil
	case "sha256HexOf":
		return Sha256HexOf(strOf(a[0])), nil
	case "configHash":
		return ConfigHash(m(0))
	case "catalogHashOf":
		return CatalogHashOf(a[0])
	case "markSecretValues":
		return MarkSecretValues(m(0)), nil
	case "secretValueMarker":
		return SecretValueMarker(a[0]), nil
	case "itemHash":
		return ItemHash(a[0]), nil
	case "templateSyncHash":
		return TemplateSyncHash(a[0]), nil
	case "classifySync":
		return ClassifySync(slotOf(a[0]), slotOf(a[1]), slotOf(a[2])), nil
	case "classifyItem":
		var b *Slot
		if a[0] != nil {
			s := slotOf(a[0])
			b = &s
		}
		return ClassifyItem(b, slotOf(a[1]), slotOf(a[2])), nil
	case "supportsBranches":
		return SupportsBranches(a[0]), nil
	case "needsAgentMessage":
		return NeedsAgentMessage(strOf(a[0]), a[1]), nil
	case "makeCommit":
		return MakeCommit(m(0))
	case "commitRef":
		return CommitRef(m(0)), nil
	case "commitSummaryOf":
		return CommitSummaryOf(m(0)), nil
	case "maskCommit":
		return MaskCommit(m(0), func(y *doc.Map) *doc.Map { return doc.Obj("masked", float64(y.Len())) }), nil
	case "verifyCommitLink":
		return VerifyCommitLink(m(0), m(1))
	case "sourcePicks":
		return SourcePicks(brStrsOf(a[0]), strOf(a[1])), nil
	case "hasSecretValueChanges":
		return HasSecretValueChanges(m(0)), nil
	case "maskDevToolLiterals":
		return MaskDevToolLiterals(a[0]), nil
	case "constants":
		return doc.Obj(
			"a", []any{BRANCHES_MIN_AGENT, COMMIT_MESSAGE_MAX, COMMIT_SUMMARY_MAX, COMMIT_PICKS_MAX, COMMIT_STATS_KEYS_MAX, CLOUD_RECENT_COMMITS},
			"k", []any{brStrs(COMMIT_KINDS), brStrs(MERGE_KINDS), brStrs(SOURCE_CHANGE_KINDS), brStrs(SITE_SETTINGS), brStrs(EDGE_FIELDS), brStrs(CONTAINER_FIELDS), brStrs(SYNC_TAKEABLE)},
			"n", []any{CONTENT_NOTE_STARTER, CONTENT_NOTE_PERSIST, DEVTOOL_VALUE_CHANGED_NOTE, DEVTOOL_VALUE_MASK, VOLUME_CONTENT_CHANGED_NOTE, VOLUME_SEED_CHANGED_NOTE, VOLUME_KIND_CHANGED_NOTE},
		), nil
	case "configHashInf":
		return ConfigHash(doc.Obj("variables", []any{doc.Obj("n", math.Inf(1))}))
	case "makeCommitInf":
		cfg := doc.Obj("variables", []any{doc.Obj("n", math.Inf(-1))}, "secrets", []any{}, "files", []any{}, "sites", []any{})
		return MakeCommit(doc.Obj("commitId", "C", "branch", doc.Obj("kind", "template", "templateId", "T1"), "parent", nil, "kind", "create", "message", "m", "createdAt", "T", "config", cfg))
	}
	return nil, fmt.Errorf("unknown vector function %s", fn)
}

// Every export of branch.ts, diff.ts and template-sync.ts against the live TS: same JSON bytes, so same key order.
func TestBranchVectors(t *testing.T) {
	v, err := doc.ParseMap(tsx(t, "testdata/branch_vectors.mts"))
	if err != nil {
		t.Fatal(err)
	}
	configs := v.Arr("configs")
	resolve := func(x any) any {
		if m := brMapOf(x); m != nil {
			if m.Has("$u") {
				return doc.Undefined
			}
			if m.Has("$c") {
				return doc.CloneValue(configs[int(m.Num("$c"))])
			}
		}
		return x
	}
	counts := map[string]int{}
	bad := 0
	for _, x := range v.Arr("calls") {
		c := x.(*doc.Map)
		fn := c.Str("fn")
		raw := c.Arr("args")
		args := make([]any, len(raw))
		for i, a := range raw {
			args[i] = resolve(a)
		}
		before := doc.MarshalString(args)
		got, err := func() (r any, e error) {
			defer func() {
				if p := recover(); p != nil {
					e = fmt.Errorf("panic: %v", p)
				}
			}()
			return runCall(fn, args)
		}()
		counts[fn]++
		var msg string
		switch {
		case c.Has("err"):
			if err == nil || err.Error() != c.Str("err") {
				msg = fmt.Sprintf("error: got %v, want %q", err, c.Str("err"))
			}
		case err != nil:
			msg = "error: " + err.Error()
		default:
			g, w := doc.MarshalString(got), doc.MarshalString(c.Val("out"))
			if g != w {
				i := 0
				for i < len(g) && i < len(w) && g[i] == w[i] {
					i++
				}
				from := max(0, i-160)
				msg = fmt.Sprintf("at byte %d\n got …%s\nwant …%s", i, clip(g, from, i+160), clip(w, from, i+160))
			}
		}
		if msg == "" && doc.MarshalString(args) != before {
			msg = "the call changed its arguments"
		}
		if msg != "" {
			bad++
			if bad <= 6 {
				t.Errorf("%s: %s", fn, msg)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d vectors differ", bad, len(v.Arr("calls")))
	}
	total, _ := json.Marshal(counts)
	t.Logf("%d vectors: %s", len(v.Arr("calls")), total)
	if len(counts) < 50 {
		t.Fatalf("only %d functions covered", len(counts))
	}
}

func clip(s string, a, b int) string {
	if b > len(s) {
		b = len(s)
	}
	if a > b {
		a = b
	}
	return strings.ToValidUTF8(s[a:b], "?")
}
