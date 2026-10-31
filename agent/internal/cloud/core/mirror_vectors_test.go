package core

import (
	"os"
	"testing"

	"opm/agent/internal/cloud/doc"
)

// testdata/mirror_vectors.json: the helpers of shared/src (github-mirror, remote-gui, tunnels) run on these inputs by the TS.
func TestSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/mirror_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	all, err := doc.ParseMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := func(v any) string { x, _ := v.(string); return x }
	fromErr := func(v any, err error) any {
		if err != nil {
			return doc.Obj("err", err.Error())
		}
		return doc.Obj("ok", v)
	}
	ok := func(v any) any { return doc.Obj("ok", v) }
	mapStr := func(m map[string]string) any {
		o := doc.New()
		for k, v := range m {
			o.Set(k, v)
		}
		return o
	}
	fns := map[string]func(in any) any{
		"mask": func(in any) any { return ok(MaskGithubToken(s(in))) },
		"branch": func(in any) any {
			a := in.([]any)
			v, err := MirrorBranchName(s(a[0]), s(a[1]), s(a[2]))
			return fromErr(v, err)
		},
		"message": func(in any) any {
			return ok(MirrorCommitMessage(s(in), [2]string{"OPM-Commit", "c1"}, [2]string{"OPM-Seq", " 1\r\n2 "}))
		},
		"trailers": func(in any) any { return ok(mapStr(ParseMirrorTrailers(s(in)))) },
		"orderKey": func(in any) any { a := in.([]any); return ok(MirrorOrderKey(s(a[0]), a[1], s(a[2]))) },
		"itemId":   func(in any) any { a := in.([]any); return ok(MirrorItemId(s(a[0]), s(a[1]), s(a[2]))) },
		"backoff":  func(in any) any { return ok(float64(MirrorBackoffSec(int(in.(float64))))) },
		"owner":    func(in any) any { return ok(GithubOwnerRE.MatchString(s(in))) },
		"repo":     func(in any) any { return ok(GithubRepoOK(s(in))) },
		"token":    func(in any) any { return ok(GithubTokenRE.MatchString(s(in))) },
		"remotePath": func(in any) any {
			r := ParseRemoteGuiPath(s(in))
			if r == nil {
				return ok(nil)
			}
			return ok(doc.Obj("deploymentId", r.DeploymentID, "siteSlug", r.SiteSlug, "agentPath", r.AgentPath))
		},
		"rewrite":    func(in any) any { return ok(RewriteRemoteText(s(in), "/app/deployments/d/remote/s")) },
		"location":   func(in any) any { return ok(RewriteRemoteLocation(s(in), "/p")) },
		"rewritable": func(in any) any { return ok(IsRewritableType(s(in))) },
		"ports": func(in any) any {
			l, err := ParseTunnelPorts(s(in))
			out := []any{}
			for _, p := range l {
				out = append(out, float64(p))
			}
			return fromErr(out, err)
		},
		"validate": func(in any) any {
			if r := ValidatePortTunnel(in); r != "" {
				return ok(r)
			}
			return ok(nil)
		},
		"targetText": func(in any) any { return ok(PortTunnelTargetText(in.(*doc.Map))) },
	}
	n := 0
	for _, k := range all.Keys() {
		f := fns[k]
		if f == nil {
			t.Fatalf("no check for %s", k)
		}
		for _, row := range all.Arr(k) {
			m := row.(*doc.Map)
			got, want := f(m.Val("in")), m.Val("out")
			a, _ := doc.Marshal(got)
			b, _ := doc.Marshal(want)
			if !sameSorted(a, b) {
				t.Errorf("%s(%s): got %s want %s", k, doc.MarshalString(m.Val("in")), a, b)
			}
			n++
		}
	}
	if n < 100 {
		t.Fatalf("only %d vectors", n)
	}
}

// sameSorted compares JSON ignoring the order of object keys.
func sameSorted(a, b []byte) bool {
	x, _ := doc.Parse(a)
	y, _ := doc.Parse(b)
	p, _ := doc.MarshalIndent(keySorted(x), 0)
	q, _ := doc.MarshalIndent(keySorted(y), 0)
	return string(p) == string(q)
}

func keySorted(v any) any {
	switch x := v.(type) {
	case *doc.Map:
		o := doc.New()
		for _, k := range x.SortedKeys() {
			o.Set(k, keySorted(x.Val(k)))
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = keySorted(e)
		}
		return out
	}
	return v
}
