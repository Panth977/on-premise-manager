package agent

import (
	"testing"

	"opm/agent/internal/model"
)

// O5: only a 401 unknown-site / signature answer means the site key is revoked; a 403 is a per-call refusal.
func TestNoteCloudResultRevokedOnlyOn401(t *testing.T) {
	a := newTestAgent(t)
	revoked := func() bool {
		a.self.mu.Lock()
		defer a.self.mu.Unlock()
		return a.self.revoked != nil
	}
	errBody := func(code, reason, msg string) model.APIErrorBody {
		return model.APIErrorBody{Error: model.APIError{Code: code, Message: msg, Details: map[string]any{"reason": reason}}}
	}
	a.noteCloudResult(403, errBody(model.ErrForbidden, "", "the site runs no persist-to-cloud volume"))
	if revoked() {
		t.Fatal("a 403 must not mark the key revoked")
	}
	a.noteCloudResult(401, errBody(model.ErrUnauthenticated, "clock", "clock skew"))
	if revoked() {
		t.Fatal("a 401 clock refusal must not mark the key revoked")
	}
	a.noteCloudResult(401, errBody(model.ErrUnauthenticated, "unknown-site", "site app is not enrolled"))
	if !revoked() {
		t.Fatal("401 unknown-site must mark the key revoked")
	}
	a.noteCloudResult(200, model.APIErrorBody{})
	if revoked() {
		t.Fatal("a 2xx clears revoked")
	}
	a.noteCloudResult(401, errBody(model.ErrUnauthenticated, "signature", "bad signature"))
	if !revoked() {
		t.Fatal("401 signature must mark the key revoked")
	}
}

// O4: the 5-second staging progress tick never reports a branch object (not a proposal) to the cloud.
func TestStagingTickSkipsBranchRevs(t *testing.T) {
	a := newTestAgent(t)
	count := func(rev string) int {
		var n int
		_ = a.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE kind = 'proposal-status' AND path = ?`, "/v1/agent/proposals/"+rev+"/status").Scan(&n)
		return n
	}
	a.markBranchRev("BRANCHREV")
	a.reportStagingTick("BRANCHREV")
	if n := count("BRANCHREV"); n != 0 {
		t.Fatalf("branch rev reported %d progress status(es)", n)
	}
	a.reportStagingTick("PROPOSALREV")
	if n := count("PROPOSALREV"); n != 1 {
		t.Fatalf("proposal rev: want 1 progress status, got %d", n)
	}
}
