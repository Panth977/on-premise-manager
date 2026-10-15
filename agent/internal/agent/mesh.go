package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/store"
)

// pingWire is PeerPing plus agent-internal extras (extra fields in agent DTOs are ignored by other readers).
type pingWire struct {
	model.PeerPing
	Links      map[string]*float64   `json:"links"`
	Tunnel     *model.TunnelLease    `json:"tunnel"`
	Containers model.ContainerCounts `json:"containers"`
	Host       *string               `json:"host"`
	Label      *string               `json:"label"`
	Snapshots  []string              `json:"snapshots"`
	// ActiveUpdates: updates this site coordinates that have not finished
	ActiveUpdates []string `json:"activeUpdates"`
	// Terminated: updates a person stopped on this site (§21 #110): a site still locked for one stops it too
	Terminated []string `json:"terminated,omitempty"`
}

type peerRuntime struct {
	ping        *pingWire
	lastAnswer  time.Time
	rtt         float64
	unreachable bool // site.unreachable written
	lastTry     time.Time
}

type haveEntry struct {
	at   time.Time
	have map[string]bool
	list *model.HaveList
}

func (a *Agent) peerAddr(slug string) (string, *peerRow, error) {
	p, ok := a.peerRow(slug)
	if !ok {
		return "", nil, fmt.Errorf("site %s is not enrolled", slug)
	}
	host := p.AdvertiseHost
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	port := p.AgentPort
	if p.LastPing != nil && p.LastPing.AgentPort != 0 {
		port = p.LastPing.AgentPort
	}
	return "http://" + host + ":" + strconv.Itoa(port), p, nil
}

var peerHTTP = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second}}

// peerRequest sends a signed request; body is fully known (signature covers it).
func (a *Agent) peerRequest(ctx context.Context, slug, method, path string, body []byte, hdr map[string]string, timeout time.Duration) (*http.Response, string, *peerRow, context.CancelFunc, error) {
	base, row, err := a.peerAddr(slug)
	if err != nil {
		return nil, "", nil, func() {}, err
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(cctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, "", nil, func() {}, err
	}
	sh := sign.SignRequest(a.key, a.dep+"/"+a.site, method, path, body, a.signTs())
	for k, v := range sh {
		req.Header.Set(k, v)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	resp, err := peerHTTP.Do(req)
	if err != nil {
		cancel()
		return nil, "", nil, func() {}, &netError{err}
	}
	return resp, sh[sign.HNonce], row, cancel, nil
}

// peerJSON: signed request + signed JSON response.
func (a *Agent) peerJSON(ctx context.Context, slug, method, path string, in, out any, timeout time.Duration) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = b
	}
	return a.peerJSONRaw(ctx, slug, method, path, body, nil, out, timeout)
}

func (a *Agent) peerJSONRaw(ctx context.Context, slug, method, path string, body []byte, hdr map[string]string, out any, timeout time.Duration) error {
	resp, nonce, row, cancel, err := a.peerRequest(ctx, slug, method, path, body, hdr, timeout)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return &netError{err}
	}
	pub, err := sign.ParsePublicKey(row.PublicKey)
	if err != nil {
		return &netError{err}
	}
	if resp.Header.Get(sign.HSig) == "" || resp.Header.Get(sign.HSite) != slug {
		return &netError{fmt.Errorf("peer %s: unsigned response (status %d)", slug, resp.StatusCode)}
	}
	if err := sign.VerifyResponse(pub, resp.Header.Get, resp.StatusCode, nonce, rb, time.Now().Unix()+a.offset()); err != nil {
		return &netError{fmt.Errorf("peer %s: %w", slug, err)}
	}
	if resp.StatusCode >= 300 {
		ae := &apiError{Status: resp.StatusCode}
		_ = json.Unmarshal(rb, &ae.Body)
		return ae
	}
	if out != nil && len(rb) > 0 {
		return json.Unmarshal(rb, out)
	}
	return nil
}

func (a *Agent) offset() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.clockOffset
}

// peerStream sends a signed request and returns the raw (unsigned) response; caller closes.
func (a *Agent) peerStream(ctx context.Context, slug, method, path string, body []byte, hdr map[string]string, timeout time.Duration) (*http.Response, func(), error) {
	resp, _, _, cancel, err := a.peerRequest(ctx, slug, method, path, body, hdr, timeout)
	if err != nil {
		return nil, func() {}, err
	}
	return resp, func() { resp.Body.Close(); cancel() }, nil
}

// peerUploadFile sends a file body (hash computed first; streamed).
func (a *Agent) peerUploadFile(ctx context.Context, slug, method, path, file string, hdr map[string]string, timeout time.Duration) (*http.Response, func(), error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, func() {}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		f.Close()
		return nil, func() {}, err
	}
	_, _ = f.Seek(0, 0)
	base, _, err := a.peerAddr(slug)
	if err != nil {
		f.Close()
		return nil, func() {}, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(cctx, method, base+path, f)
	if err != nil {
		f.Close()
		cancel()
		return nil, func() {}, err
	}
	req.ContentLength = n
	ts := a.signTs()
	nonce := sign.Nonce()
	sigStr := strings.ToUpper(method) + "\n" + path + "\n" + strconv.FormatInt(ts, 10) + "\n" + nonce + "\n" + hex.EncodeToString(h.Sum(nil))
	sh := signRaw(a, sigStr)
	req.Header.Set(sign.HSite, a.dep+"/"+a.site)
	req.Header.Set(sign.HTs, strconv.FormatInt(ts, 10))
	req.Header.Set(sign.HNonce, nonce)
	req.Header.Set(sign.HSig, sh)
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := peerHTTP.Do(req)
	f.Close()
	if err != nil {
		cancel()
		return nil, func() {}, &netError{err}
	}
	return resp, func() { resp.Body.Close(); cancel() }, nil
}

// ───────────────────────── reachability

func (a *Agent) reachable(slug string) bool {
	if slug == a.site {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.peerState[slug]
	return st != nil && !st.lastAnswer.IsZero() && time.Since(st.lastAnswer) < a.tm.Reachable
}

func (a *Agent) reachablePeers() []string {
	var out []string
	for _, p := range a.peerRows() {
		if p.SiteSlug != a.site && a.reachable(p.SiteSlug) {
			out = append(out, p.SiteSlug)
		}
	}
	sort.Strings(out)
	return out
}

func (a *Agent) enrolledSites() []string {
	var out []string
	for _, p := range a.peerRows() {
		out = append(out, p.SiteSlug)
	}
	if !contains(out, a.site) && a.enrolled {
		out = append(out, a.site)
	}
	sort.Strings(out)
	return out
}

// lastPing returns the latest ping of a peer (self: live).
func (a *Agent) lastPing(slug string) *pingWire {
	if slug == a.site {
		p := a.selfPing()
		return &p
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if st := a.peerState[slug]; st != nil && st.ping != nil {
		cp := *st.ping
		return &cp
	}
	return nil
}

// ───────────────────────── ping (§9.2)

func (a *Agent) selfPing() pingWire {
	h := a.head()
	if a.branchMode() {
		h = a.legacyHead() // §21 #104 §9.3: old peers gate legacy proposals on the legacy chain; branch heads ride pw.Branch
	}
	var lockPtr *model.UpdateLock
	if l := a.lock(); l != nil {
		lockPtr = l
	}
	props := []model.PeerProposalState{}
	for _, p := range a.openProposals() {
		st := model.SiteStaging{Staging: model.StagingPending}
		if row, ok := a.staging(p.RevID, a.site); ok {
			st = row.SiteStaging
		}
		props = append(props, model.PeerProposalState{RevID: p.RevID, Status: p.Status, Staging: st})
	}
	var fetched *string
	if v := a.cloudView(); v != nil {
		f := v.FetchedAt
		fetched = &f
	}
	_, kv := a.keyHash()
	a.mu.Lock()
	edgeRunning, eh, es, ee := a.edgeRunning, a.edgeHTTP, a.edgeHTTPS, a.edgeErr
	a.mu.Unlock()
	cfg, _ := a.headConfigRO() // read only (§21 #112: served to every peer every 10 s)
	inCfg := siteOf(cfg, a.site) != nil
	pw := pingWire{PeerPing: model.PeerPing{DeploymentID: a.dep, SiteSlug: a.site, AgentVersion: a.version, ServerTime: nowTs(),
		Head: h, Lock: lockPtr, Proposals: props, Internet: a.hasInternet(), CloudRoute: a.route(), CloudFetchedAt: fetched,
		KeyVersion: kv, AgentPort: a.currentAgentPort(), GuiAliasPort: a.currentAliasPort(),
		Edge: model.PeerEdge{Running: edgeRunning, HTTPPort: eh, HTTPSPort: es, LastError: ee}, InConfig: inCfg},
		Links: a.selfLinks(), Containers: a.containerCounts()}
	if l := a.tun.active(); l != nil {
		pw.Tunnel = l
	}
	if s := siteOf(cfg, a.site); s != nil {
		lbl := s.Label
		pw.Label = &lbl
		if hs := a.hostOf(cfg, a.site); hs != "" {
			pw.Host = &hs
		}
	}
	pw.Snapshots = a.snapshotIDs()
	pw.Branch = a.branchReport() // §21 #104
	pw.ActiveUpdates = a.activeUpdates()
	pw.Terminated = a.terminatedIDs()
	return pw
}

func (a *Agent) selfLinks() map[string]*float64 {
	out := map[string]*float64{}
	a.mu.Lock()
	defer a.mu.Unlock()
	for slug, st := range a.peerState {
		if !st.lastAnswer.IsZero() && time.Since(st.lastAnswer) < a.tm.Reachable {
			r := st.rtt
			out[slug] = &r
		} else {
			out[slug] = nil
		}
	}
	return out
}

func (a *Agent) pingAll(ctx context.Context) {
	if !a.enrolled {
		return
	}
	for _, p := range a.peerRows() {
		if p.SiteSlug == a.site {
			continue
		}
		slug := p.SiteSlug
		go func() {
			defer a.log.Recover("mesh", "ping "+slug)
			a.pingOne(ctx, slug)
		}()
	}
}

func (a *Agent) pingOne(ctx context.Context, slug string) {
	start := time.Now()
	var pw pingWire
	err := a.peerJSON(ctx, slug, "GET", "/_opm/peer/ping", nil, &pw, a.tm.PingTimeout)
	a.mu.Lock()
	st := a.peerState[slug]
	if st == nil {
		st = &peerRuntime{}
		a.peerState[slug] = st
	}
	st.lastTry = time.Now()
	wasUnreachable := st.unreachable
	stale := err == nil && st.ping != nil && parseTs(pw.ServerTime).Before(parseTs(st.ping.ServerTime))
	if stale {
		a.mu.Unlock()
		return // an older in-flight answer must not overwrite a newer one
	}
	if err == nil && pw.DeploymentID == a.dep && pw.SiteSlug == slug {
		st.ping = &pw
		st.lastAnswer = time.Now()
		st.rtt = float64(time.Since(start).Microseconds()) / 1000
		st.unreachable = false
	}
	last := st.lastAnswer
	becameUnreachable := !st.unreachable && err != nil && (last.IsZero() && time.Since(a.started) > a.tm.Unreachable || !last.IsZero() && time.Since(last) > a.tm.Unreachable)
	if becameUnreachable {
		st.unreachable = true
	}
	a.mu.Unlock()
	if err == nil {
		_, _ = a.db.Exec(`UPDATE peers SET lastAnswerAt = ?, rttMs = ?, reachable = 1, hasInternet = ?, lastPing = ? WHERE siteSlug = ?`,
			time.Now().Unix(), float64(time.Since(start).Microseconds())/1000, boolInt(pw.Internet), store.JSON(pw), slug)
		if wasUnreachable {
			a.activity(nil, "system", "site.reachable", "site "+slug+" is reachable again", map[string]any{"peerSlug": slug, "lastSeen": nowTs()})
		}
		a.onPeerPing(slug, &pw)
		return
	}
	_, _ = a.db.Exec(`UPDATE peers SET reachable = 0 WHERE siteSlug = ?`, slug)
	if becameUnreachable {
		ls := ""
		if !last.IsZero() {
			ls = tsOf(last)
		}
		a.activity(nil, "system", "site.unreachable", "site "+slug+" is unreachable", map[string]any{"peerSlug": slug, "lastSeen": ls})
		a.log.Warn("mesh", "peer unreachable", "peer", slug, "error", err.Error())
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// onPeerPing: peer staging states → staging rows + proposal site map; lock-follow logic for participants.
func (a *Agent) onPeerPing(slug string, pw *pingWire) {
	for _, ps := range pw.Proposals {
		a.setStaging(ps.RevID, slug, ps.Staging, nil)
		if p, ok := a.proposal(ps.RevID); ok && !model.IsTerminal(p.Status) {
			if cur, ok := p.Sites[slug]; !ok || cur.Staging != ps.Staging.Staging || cur.Progress != ps.Staging.Progress {
				p.Sites[slug] = ps.Staging
				a.recomputeStatus(p)
				a.putProposal(p)
			}
		}
	}
	a.followCoordinator(slug, pw)
}

// recomputeStatus derives published → staging → ready (§14.2) from the site map.
func (a *Agent) recomputeStatus(p *model.Proposal) {
	switch p.Status {
	case model.ProposalPublished, model.ProposalStaging, model.ProposalReady:
	default:
		return
	}
	all, anyNon := len(p.Sites) > 0, false
	for _, s := range p.Sites {
		if s.Staging != model.StagingReady {
			all = false
		}
		if s.Staging != model.StagingPending {
			anyNon = true
		}
	}
	ns := model.ProposalPublished
	if all {
		ns = model.ProposalReady
	} else if anyNon {
		ns = model.ProposalStaging
	}
	if ns != p.Status {
		p.Status = ns
		p.UpdatedAt = nowTs()
	}
}

// ───────────────────────── cloud view exchange + actors

func (a *Agent) exchangeViews(ctx context.Context) {
	cur := a.cloudView()
	for _, slug := range a.reachablePeers() {
		// §21 #112: the ping says when the peer fetched its view; only a newer one is worth the full exchange
		a.mu.Lock()
		var pinged bool
		var theirs *string
		if st := a.peerState[slug]; st != nil && st.ping != nil {
			pinged, theirs = true, st.ping.CloudFetchedAt
		}
		a.mu.Unlock()
		if pinged && (theirs == nil || (cur != nil && !parseTs(*theirs).After(parseTs(cur.FetchedAt)))) {
			continue
		}
		var v model.CloudView
		if err := a.peerJSON(ctx, slug, "GET", "/_opm/peer/cloud-view", nil, &v, 10*time.Second); err != nil {
			continue
		}
		if v.FetchedAt == "" {
			continue
		}
		a.adoptView(ctx, &v, "peer:"+slug)
	}
}

func (a *Agent) mergeActors(ctx context.Context) {
	for _, slug := range a.reachablePeers() {
		var r model.PeerActorsResponse
		if err := a.peerJSON(ctx, slug, "GET", "/_opm/peer/actors", nil, &r, 10*time.Second); err != nil {
			continue
		}
		for _, x := range r.Actors {
			_, _ = a.db.Exec(`INSERT INTO actors(email, lastUsedAt, count) VALUES(?,?,?) ON CONFLICT(email) DO UPDATE SET
				lastUsedAt = MAX(actors.lastUsedAt, excluded.lastUsedAt), count = MAX(actors.count, excluded.count)`, x.Email, x.LastUsedAt, x.Count)
		}
	}
}

func (a *Agent) actors(limit int) []model.ActorRow {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := a.db.Query(`SELECT email, lastUsedAt, count FROM actors ORDER BY lastUsedAt DESC LIMIT ?`, limit)
	if err != nil {
		return []model.ActorRow{}
	}
	defer rows.Close()
	out := []model.ActorRow{}
	for rows.Next() {
		var r model.ActorRow
		if rows.Scan(&r.Email, &r.LastUsedAt, &r.Count) == nil {
			out = append(out, r)
		}
	}
	return out
}

// ───────────────────────── peer request verification (server side)

type peerCaller struct {
	Site  string
	Nonce string
}

var errPeerAuth = errors.New("peer auth")

// verifyPeer checks a signed peer request (§4.4) and returns the caller.
func (a *Agent) verifyPeer(r *http.Request, body []byte, bodyHashHex string) (*peerCaller, *sign.VerifyError) {
	pr, err := sign.ParseHeaders(r.Header.Get)
	if err != nil {
		var ve *sign.VerifyError
		errors.As(err, &ve)
		return nil, ve
	}
	if pr.DeploymentID != a.dep {
		return nil, &sign.VerifyError{Reason: "unknown-site", Msg: "another deployment"}
	}
	row, ok := a.peerRow(pr.SiteSlug)
	if !ok {
		return nil, &sign.VerifyError{Reason: "unknown-site", Msg: "site " + pr.SiteSlug + " is not enrolled"}
	}
	pub, err := sign.ParsePublicKey(row.PublicKey)
	if err != nil {
		return nil, &sign.VerifyError{Reason: "unknown-site", Msg: "bad peer key"}
	}
	path := r.URL.RequestURI()
	now := time.Now().Unix() + a.offset()
	if bodyHashHex == "" {
		if err := sign.VerifyRequest(pub, pr, r.Method, path, body, now); err != nil {
			return nil, err.(*sign.VerifyError)
		}
	} else {
		if d := now - pr.Ts; d > 60 || d < -60 {
			return nil, &sign.VerifyError{Reason: "clock", Msg: fmt.Sprintf("clock skew %d s", d)}
		}
		s := strings.ToUpper(r.Method) + "\n" + path + "\n" + strconv.FormatInt(pr.Ts, 10) + "\n" + pr.Nonce + "\n" + bodyHashHex
		if !verifyRaw(pub, s, r.Header.Get(sign.HSig)) {
			return nil, &sign.VerifyError{Reason: "signature", Msg: "signature invalid"}
		}
	}
	key := pr.SiteSlug + "/" + pr.Nonce
	res, err := a.db.Exec(`INSERT INTO nonces(key, expiresAt) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, key, time.Now().Add(5*time.Minute).Unix())
	if err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, &sign.VerifyError{Reason: "nonce", Msg: "nonce replayed"}
		}
	}
	return &peerCaller{Site: pr.SiteSlug, Nonce: pr.Nonce}, nil
}
