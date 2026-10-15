package agent

import (
	"net/http"
	"strings"

	"net/http/httptest"
	"opm/agent/internal/model"
	"testing"
)

// §21 #111 the per-site branch: the read + merge endpoints are site endpoints (served locally or relayed to the peer);
// the GUI working-copy commit and the coordinated fast-forward are not.
func TestSiteBranchRoutes(t *testing.T) {
	a := newTestAgent(t)
	mux, ok := a.buildSiteMux().(*http.ServeMux)
	if !ok {
		t.Fatal("site mux is not a ServeMux")
	}
	pattern := func(method, path string) string {
		_, p := mux.Handler(httptest.NewRequest(method, path, nil))
		return p
	}
	for _, c := range [][2]string{
		{"GET", "/branch"}, {"GET", "/branch/commits"}, {"GET", "/branch/commits/C1"}, {"GET", "/branch/cloud-commits/C1"},
		{"GET", "/branch/uncommitted"}, {"POST", "/branch/compare"}, {"POST", "/branch/merge/preview"}, {"POST", "/branch/merge"},
		{"POST", "/branch/discard"}, {"POST", "/branch/drift/rescan"}, {"POST", "/branch/revert"}, {"POST", "/branch/reset"},
		{"GET", "/branch/blobs/sha256:ab"}, {"POST", "/branch/commit-drift"}, {"GET", "/branch/drift-policy"}, {"PUT", "/branch/drift-policy"}, {"GET", "/updates/current"}, {"GET", "/updates/U1"}, {"POST", "/updates/U1/abandon"},
	} {
		if p := pattern(c[0], c[1]); p == "/" || p == "" {
			t.Errorf("%s %s is not a site endpoint (pattern %q)", c[0], c[1], p)
		}
	}
	for _, c := range [][2]string{{"POST", "/branch/commit"}, {"PUT", "/branch/working"}, {"POST", "/branch/fast-forward"}} {
		if p := pattern(c[0], c[1]); p != "/" {
			t.Errorf("%s %s must stay on the logged-in agent only (pattern %q)", c[0], c[1], p)
		}
	}
	// commit-drift refuses browser edits before anything runs
	for _, body := range []string{`{"message":"x","config":{"variables":[]}}`, `{"message":"x","stageId":"S1"}`} {
		req := httptest.NewRequest("POST", "/branch/commit-drift", strings.NewReader(body))
		req = withRC(req, &reqCtx{actor: "ops@example.com"})
		_, _, he := a.handleSiteCommitDrift(httptest.NewRecorder(), req)
		if he == nil || he.code != model.ErrInvalid {
			t.Errorf("commit-drift %s: want invalid, got %v", body, he)
		}
	}
	// the legacy /_opm/api/branch/* registration is unchanged
	var got []string
	a.registerBranchAPI(func(p string, _ authKind, _ apiFn) { got = append(got, p) })
	// + §21 #113 GET / PUT /branch/drift-policy
	if len(got) != 19 || got[0] != "GET /_opm/api/branch" || got[15] != "POST /_opm/api/branch/fast-forward" {
		t.Errorf("legacy branch routes: %v", got)
	}
}
