package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

// The tab protocol itself, in clear (tunnelPlaintext on an agent without a release key); sealing: tunnelsealed_test.go.
func TestTunnelProtocol(t *testing.T) {
	a := newTestAgent(t)
	a.cfg.TunnelPlaintext = true
	sid := "S1"
	exp := time.Now().Add(time.Hour).Unix()
	_, _ = a.db.Exec(`INSERT INTO sessions(sessionId, createdAt, expiresAt, keyVersion, ip, userAgent) VALUES(?,?,?,?,?,?)`, sid, time.Now().Unix(), exp, 1, "", "")
	tok := sign.MakeActorToken(a.key, model.ActorTokenClaims{Email: "ops@example.com", Sid: sid, Dep: a.dep, Site: a.site, Exp: exp})
	h := a.handlerFor("gui-alias")
	call := func(method, path string, body []byte, out any) int {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Cookie", "opm_session_app="+sid)
		r.Header.Set("Authorization", "Actor "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if out != nil {
			_ = json.Unmarshal(rec.Body.Bytes(), out)
		}
		return rec.Code
	}
	var lr model.TunnelLeaseResponse
	if c := call("POST", "/_opm/api/tunnel/start", []byte(`{"userAgent":"test"}`), &lr); c != 201 {
		t.Fatalf("start %d", c)
	}
	if c := call("POST", "/_opm/api/tunnel/start", []byte(`{}`), nil); c != 409 {
		t.Fatalf("second lease %d", c)
	}
	type res struct {
		r   *cloudResp
		err error
	}
	ch := make(chan res, 2)
	go func() {
		r, err := a.tunnelCall(context.Background(), model.RelayInner{Method: "GET", Path: "/v1/agent/poll?wait=0", Headers: map[string]string{"X-OPM-Site": "x"}}, 10*time.Second)
		ch <- res{r, err}
	}()
	var next model.TunnelNextResponse
	if c := call("GET", "/_opm/api/tunnel/next?leaseId="+lr.Lease.LeaseID, nil, &next); c != 200 || len(next.Requests) != 1 {
		t.Fatalf("next %d %+v", c, next)
	}
	rq := next.Requests[0]
	if rq.URL != "http://127.0.0.1:1/v1/agent/poll?wait=0" {
		t.Fatalf("url %s", rq.URL)
	}
	body := bytes.Repeat([]byte("x"), 100)
	b, _ := json.Marshal(model.TunnelResult{LeaseID: lr.Lease.LeaseID, ReqID: rq.ReqID, Status: 200, Headers: map[string]string{"X-OPM-Server-Time": "1"}, Chunked: true})
	if c := call("POST", "/_opm/api/tunnel/result", b, nil); c != 200 {
		t.Fatalf("result %d", c)
	}
	if c := call("POST", "/_opm/api/tunnel/result/"+rq.ReqID+"/chunk?leaseId="+lr.Lease.LeaseID+"&index=1", body[:50], nil); c != 422 {
		t.Fatalf("out of order chunk %d", c)
	}
	call("POST", "/_opm/api/tunnel/result/"+rq.ReqID+"/chunk?leaseId="+lr.Lease.LeaseID+"&index=0", body[:50], nil)
	call("POST", "/_opm/api/tunnel/result/"+rq.ReqID+"/chunk?leaseId="+lr.Lease.LeaseID+"&index=1", body[50:], nil)
	sum := sha256.Sum256(body)
	cb, _ := json.Marshal(model.TunnelCompleteRequest{LeaseID: lr.Lease.LeaseID, Parts: 2, Sha256: "sha256:" + hex.EncodeToString(sum[:])})
	if c := call("POST", "/_opm/api/tunnel/result/"+rq.ReqID+"/complete", cb, nil); c != 200 {
		t.Fatalf("complete %d", c)
	}
	got := <-ch
	if got.err != nil || got.r.Status != 200 || !bytes.Equal(got.r.Body, body) {
		t.Fatalf("tunnel call %+v %v", got.r, got.err)
	}
	// expiry after no poll
	a.tm.TunnelIdle = 0
	a.tunnelExpiry(context.Background())
	if a.tun.active() != nil {
		t.Fatal("lease should expire")
	}
	if items, _ := a.activitiesPage(0, 10, "internet.tunnel", ""); len(items) != 3 {
		t.Fatalf("tunnel activities %d", len(items)) // started, plaintext, expired
	}
}
