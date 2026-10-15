package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/store"
)

type tunReq struct {
	req      model.TunnelRequest
	handedAt time.Time
	done     chan struct{}
	once     sync.Once
	result   *cloudResp
	err      error
	chunked  *model.TunnelResult
	parts    int
}

type tunnel struct {
	mu    sync.Mutex
	lease *model.TunnelLease
	reqs  []*tunReq
	byID  map[string]*tunReq
	wake  chan struct{}
	log   []tunLogEntry
}

type tunLogEntry struct {
	ReqID  string `json:"reqId"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Step   string `json:"step"`
}

func newTunnel() *tunnel { return &tunnel{byID: map[string]*tunReq{}, wake: make(chan struct{})} }

func (t *tunnel) active() *model.TunnelLease {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lease != nil && t.lease.Status == "active" {
		cp := *t.lease
		return &cp
	}
	return nil
}

func (t *tunnel) notify() {
	close(t.wake)
	t.wake = make(chan struct{})
}

func (r *tunReq) finish(res *cloudResp, err error) {
	r.once.Do(func() {
		r.result, r.err = res, err
		close(r.done)
	})
}

// tunnelCall sends an (already signed) cloud request through the browser tab holding the lease (§9.10).
func (a *Agent) tunnelCall(ctx context.Context, inner model.RelayInner, timeout time.Duration) (*cloudResp, error) {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	t := a.tun
	t.mu.Lock()
	if t.lease == nil || t.lease.Status != "active" {
		t.mu.Unlock()
		return nil, &netError{errors.New("no active browser tunnel")}
	}
	r := &tunReq{req: model.TunnelRequest{ReqID: newID(), Method: inner.Method, URL: a.apiURL(inner.Path), Headers: inner.Headers, BodyB64: inner.BodyB64,
		CreatedAt: nowTs(), TimeoutSec: int(timeout.Seconds())}, done: make(chan struct{})}
	t.reqs = append(t.reqs, r)
	t.byID[r.req.ReqID] = r
	t.log = append(t.log, tunLogEntry{ReqID: r.req.ReqID, Method: inner.Method, Path: pathOnly(inner.Path), Step: "queued"})
	if len(t.log) > 50 {
		t.log = t.log[len(t.log)-50:]
	}
	t.notify()
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.byID, r.req.ReqID)
		for i, x := range t.reqs {
			if x == r {
				t.reqs = append(t.reqs[:i], t.reqs[i+1:]...)
				break
			}
		}
		t.mu.Unlock()
		matches, _ := filepath.Glob(filepath.Join(a.dataDir, "tunnel", r.req.ReqID+".part*"))
		for _, m := range matches {
			os.Remove(m)
		}
	}()
	select {
	case <-r.done:
		if r.err != nil {
			return nil, &netError{r.err}
		}
		if r.result.Status == 0 {
			return nil, &netError{errors.New("tunnel: browser fetch failed")}
		}
		a.adoptServerTime(r.result.Header)
		return r.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, &netError{errors.New("tunnel: no result in time")}
	}
}

func (a *Agent) leaseHolder() (*model.TunnelLease, string) {
	if l := a.tun.active(); l != nil {
		return l, a.site
	}
	for _, s := range a.reachablePeers() {
		if pw := a.lastPing(s); pw != nil && pw.Tunnel != nil && pw.Tunnel.Status == "active" {
			return pw.Tunnel, s
		}
	}
	return nil, ""
}

func (a *Agent) handleTunnelState(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	l, holder := a.leaseHolder()
	out := model.TunnelStateResponse{Lease: l}
	if holder != "" {
		out.HolderSite = &holder
	}
	a.tun.mu.Lock()
	for _, x := range a.tun.reqs {
		select {
		case <-x.done:
		default:
			out.Pending++
		}
	}
	a.tun.mu.Unlock()
	return out, 200, nil
}

func (a *Agent) saveLease(l *model.TunnelLease) {
	_, _ = a.db.Exec(`INSERT INTO tunnel_leases(leaseId, status, data) VALUES(?,?,?) ON CONFLICT(leaseId) DO UPDATE SET status = excluded.status, data = excluded.data`, l.LeaseID, l.Status, store.JSON(l))
}

func (a *Agent) handleTunnelStart(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.TunnelStartRequest
	_ = readJSON(r, &req)
	if l, holder := a.leaseHolder(); l != nil {
		return nil, 0, herr(model.ErrConflict, "the browser tunnel is in use (site "+holder+")", map[string]any{"active": l})
	}
	ua := req.UserAgent
	if ua == "" {
		ua = rc.ua
	}
	now := nowTs()
	l := &model.TunnelLease{LeaseID: newID(), Actor: rc.actor, SiteSlug: a.site, UserAgent: ua, StartedAt: now, LastPollAt: now, Status: "active"}
	a.tun.mu.Lock()
	a.tun.lease = l
	cp := *l
	a.tun.mu.Unlock()
	a.saveLease(&cp)
	a.activity(rc.actorPtr(), "agent", "internet.tunnel.started", "browser internet tunnel started", actorData(rc, map[string]any{"leaseId": l.LeaseID, "actor": rc.actor, "userAgent": ua}))
	a.kickLoop("ping")
	return model.TunnelLeaseResponse{Lease: cp}, 201, nil
}

func (a *Agent) endLease(status string, extra map[string]any, rc *reqCtx) *model.TunnelLease {
	a.tun.mu.Lock()
	l := a.tun.lease
	if l == nil || l.Status != "active" {
		a.tun.mu.Unlock()
		return nil
	}
	l.Status = status
	cp := *l
	for _, x := range a.tun.reqs {
		x.finish(nil, errors.New("tunnel "+status))
	}
	a.tun.notify()
	a.tun.mu.Unlock()
	a.saveLease(&cp)
	data := map[string]any{"leaseId": cp.LeaseID, "reqs": cp.Reqs, "bytes": cp.Bytes}
	for k, v := range extra {
		data[k] = v
	}
	var actor *string
	if rc != nil {
		actor = rc.actorPtr()
		data = actorData(rc, data)
	}
	origin := "agent"
	if rc == nil {
		origin = "system"
	}
	a.activity(actor, origin, "internet.tunnel."+status, "browser internet tunnel "+status, data)
	a.kickLoop("ping")
	return &cp
}

func (a *Agent) handleTunnelStop(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req model.TunnelLeaseIDRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	cur := a.tun.active()
	if cur == nil || cur.LeaseID != req.LeaseID {
		return nil, 0, herr(model.ErrNotFound, "no such active lease on this site", nil)
	}
	l := a.endLease("stopped", nil, getRC(r))
	if l == nil {
		return nil, 0, herr(model.ErrConflict, "the lease is not active", nil)
	}
	return model.TunnelLeaseResponse{Lease: *l}, 200, nil
}

func (a *Agent) handleTunnelRevoke(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.TunnelLeaseIDRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	cur := a.tun.active()
	if cur != nil && cur.LeaseID == req.LeaseID {
		l := a.endLease("revoked", map[string]any{"revokedBy": rc.actor, "previousActor": cur.Actor}, rc)
		if l == nil {
			return nil, 0, herr(model.ErrConflict, "the lease is not active", nil)
		}
		return model.TunnelLeaseResponse{Lease: *l}, 200, nil
	}
	if l, holder := a.leaseHolder(); l != nil && l.LeaseID == req.LeaseID && holder != a.site {
		a.forwardSite(w, r, holder, "tunnel-revoke")
		return nil, 0, nil
	}
	return nil, 0, herr(model.ErrNotFound, "no such active lease", nil)
}

func (a *Agent) checkLease(id string) *httpError {
	cur := a.tun.active()
	if cur == nil || cur.LeaseID != id {
		return herr(model.ErrConflict, "this tab's tunnel lease is not active", nil)
	}
	return nil
}

func (a *Agent) handleTunnelNext(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.URL.Query().Get("leaseId")
	if e := a.checkLease(id); e != nil {
		return nil, 0, e
	}
	max := qInt(r, "max", 4)
	if max <= 0 || max > 4 {
		max = 4
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		t := a.tun
		t.mu.Lock()
		if t.lease == nil || t.lease.LeaseID != id || t.lease.Status != "active" {
			t.mu.Unlock()
			return nil, 0, herr(model.ErrConflict, "this tab's tunnel lease is not active", nil)
		}
		t.lease.LastPollAt = nowTs()
		var out []model.TunnelRequest
		for _, x := range t.reqs {
			if len(out) >= max {
				break
			}
			select {
			case <-x.done:
				continue
			default:
			}
			if x.handedAt.IsZero() || time.Since(x.handedAt) > time.Duration(x.req.TimeoutSec)*time.Second {
				x.handedAt = time.Now()
				out = append(out, x.req)
				for i := range t.log {
					if t.log[i].ReqID == x.req.ReqID {
						t.log[i].Step = "handed out"
					}
				}
			}
		}
		wake := t.wake
		t.mu.Unlock()
		if len(out) > 0 || time.Now().After(deadline) {
			if out == nil {
				out = []model.TunnelRequest{}
			}
			return model.TunnelNextResponse{Requests: out}, 200, nil
		}
		select {
		case <-wake:
		case <-time.After(time.Until(deadline)):
		case <-r.Context().Done():
			return model.TunnelNextResponse{Requests: []model.TunnelRequest{}}, 200, nil
		}
	}
}

func (a *Agent) deliver(x *tunReq, status int, headers map[string]string, body []byte) {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	a.tun.mu.Lock()
	if a.tun.lease != nil {
		a.tun.lease.Reqs++
		a.tun.lease.Bytes += int64(len(body))
	}
	for i := range a.tun.log {
		if a.tun.log[i].ReqID == x.req.ReqID {
			a.tun.log[i].Step = fmt.Sprintf("done %d", status)
		}
	}
	a.tun.mu.Unlock()
	x.finish(&cloudResp{Status: status, Header: h, Body: body, Route: "tunnel"}, nil)
}

func (a *Agent) handleTunnelResult(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var res model.TunnelResult
	if e := readJSON(r, &res); e != nil {
		return nil, 0, e
	}
	if e := a.checkLease(res.LeaseID); e != nil {
		return nil, 0, e
	}
	a.tun.mu.Lock()
	x := a.tun.byID[res.ReqID]
	a.tun.mu.Unlock()
	if x == nil {
		return nil, 0, herr(model.ErrNotFound, "unknown or finished request", nil)
	}
	select {
	case <-x.done:
		return model.AcceptedResponse{Accepted: false}, 200, nil
	default:
	}
	if res.Status == 0 {
		msg := "browser fetch failed"
		if res.Error != nil {
			msg = *res.Error
		}
		a.tun.mu.Lock()
		if a.tun.lease != nil {
			a.tun.lease.LastError = &msg
		}
		a.tun.mu.Unlock()
		x.finish(&cloudResp{Status: 0}, nil)
		return model.AcceptedResponse{Accepted: true}, 200, nil
	}
	if res.Chunked {
		a.tun.mu.Lock()
		cp := res
		x.chunked = &cp
		a.tun.mu.Unlock()
		return model.AcceptedResponse{Accepted: true}, 200, nil
	}
	var body []byte
	if res.BodyB64 != nil {
		b, err := base64.StdEncoding.DecodeString(*res.BodyB64)
		if err != nil {
			return nil, 0, invalid("bodyB64 is not base64", nil)
		}
		if len(b) > 4<<20 {
			return nil, 0, herr(model.ErrTooLarge, "inline bodies ≤ 4 MiB; use chunks", nil)
		}
		body = b
	}
	a.deliver(x, res.Status, res.Headers, body)
	return model.AcceptedResponse{Accepted: true}, 200, nil
}

func (a *Agent) handleTunnelChunk(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	if e := a.checkLease(r.URL.Query().Get("leaseId")); e != nil {
		return nil, 0, e
	}
	id := r.PathValue("reqId")
	a.tun.mu.Lock()
	x := a.tun.byID[id]
	a.tun.mu.Unlock()
	if x == nil {
		return nil, 0, herr(model.ErrNotFound, "unknown request", nil)
	}
	idx := qInt(r, "index", -1)
	if idx != x.parts {
		return nil, 0, invalid(fmt.Sprintf("expected chunk index %d", x.parts), nil)
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20+1))
	if err != nil || len(b) > 4<<20 {
		return nil, 0, invalid("a chunk is at most 4 MiB", nil)
	}
	if err := os.WriteFile(filepath.Join(a.dataDir, "tunnel", fmt.Sprintf("%s.part%d", id, idx)), b, 0o600); err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	x.parts++
	return model.TunnelChunkResponse{Received: x.parts}, 200, nil
}

func (a *Agent) handleTunnelComplete(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req model.TunnelCompleteRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if e := a.checkLease(req.LeaseID); e != nil {
		return nil, 0, e
	}
	id := r.PathValue("reqId")
	a.tun.mu.Lock()
	x := a.tun.byID[id]
	a.tun.mu.Unlock()
	if x == nil {
		return nil, 0, herr(model.ErrNotFound, "unknown request", nil)
	}
	if x.chunked == nil || req.Parts != x.parts {
		return nil, 0, invalid(fmt.Sprintf("have %d parts", x.parts), nil)
	}
	var body []byte
	h := sha256.New()
	for i := 0; i < x.parts; i++ {
		b, err := os.ReadFile(filepath.Join(a.dataDir, "tunnel", fmt.Sprintf("%s.part%d", id, i)))
		if err != nil {
			return nil, 0, invalid("part missing", nil)
		}
		body = append(body, b...)
		h.Write(b)
	}
	if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != req.Sha256 && hexOf(req.Sha256) != hex.EncodeToString(h.Sum(nil)) {
		return nil, 0, invalid("sha256 mismatch", nil)
	}
	a.deliver(x, x.chunked.Status, x.chunked.Headers, body)
	return model.AcceptedResponse{Accepted: true}, 200, nil
}

func (a *Agent) tunnelExpiry(ctx context.Context) {
	l := a.tun.active()
	if l == nil {
		return
	}
	if time.Since(parseTs(l.LastPollAt)) > a.tm.TunnelIdle {
		a.endLease("expired", map[string]any{"lastPollAt": l.LastPollAt}, nil)
		a.log.Info("tunnel", "lease expired", "leaseId", l.LeaseID)
	}
}
