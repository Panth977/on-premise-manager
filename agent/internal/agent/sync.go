package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/store"
)

// ───────────────────────── enroll (§4.2)

func (a *Agent) enrollLoop(ctx context.Context) {
	if a.enrolled {
		return
	}
	if err := a.Enroll(ctx); err != nil {
		a.log.Warn("cloudlink", "enroll failed (retrying)", "error", err.Error())
	}
}

// Enroll consumes the install token (also used by the `enroll` subcommand).
func (a *Agent) Enroll(ctx context.Context) error {
	if a.enrolled {
		return nil
	}
	raw, err := os.ReadFile(a.cfg.InstallTokenFile)
	if err != nil {
		return fmt.Errorf("no install token (%s): %w", a.cfg.InstallTokenFile, err)
	}
	token := strings.TrimSpace(string(raw))
	req := model.EnrollRequest{PublicKey: sign.PublicKeyB64(a.pub), AgentVersion: a.version, Hostname: hostname(), IP: detectIP(),
		AdvertiseHost: a.cfg.AdvertiseHost, AgentPort: a.currentAgentPort(), OS: runtime.GOOS, Arch: runtime.GOARCH, DetectedAddresses: detectAddresses()}
	body, _ := json.Marshal(req)
	resp, err := a.directRaw(ctx, "POST", "/v1/agent/enroll", body, map[string]string{"Authorization": "Bearer " + token}, 30*time.Second)
	if err != nil {
		return err
	}
	if resp.Status >= 300 {
		ae := &apiError{Status: resp.Status}
		_ = json.Unmarshal(resp.Body, &ae.Body)
		return ae
	}
	var er model.EnrollResponse
	if err := json.Unmarshal(resp.Body, &er); err != nil {
		return err
	}
	if er.DeploymentID != a.dep || er.SiteSlug != a.site {
		return fmt.Errorf("enroll answer is for %s/%s, agent.json says %s/%s", er.DeploymentID, er.SiteSlug, a.dep, a.site)
	}
	a.markDirect(true)
	a.storeConsoleInfo(er.ConsoleURL, er.DeploymentLabel)
	a.storePeers(er.Peers)
	a.adoptKeyHash(er.AccessKey)
	_ = a.db.SetKV("enrolled", map[string]any{"deploymentId": er.DeploymentID, "siteSlug": er.SiteSlug, "enrolledAt": nowTs()})
	a.mu.Lock()
	a.enrolled = true
	a.mu.Unlock()
	if err := os.Remove(a.cfg.InstallTokenFile); err != nil {
		a.log.Warn("cloudlink", "could not delete the install token file", "error", err.Error())
	}
	a.activity(nil, "system", "install.enrolled", "site enrolled", map[string]any{"agentVersion": a.version,
		"publicKeyFingerprint": sign.Fingerprint(a.pub), "ip": detectIP(), "hostname": hostname()})
	a.log.Info("cloudlink", "enrolled", "fingerprint", sign.Fingerprint(a.pub))
	a.kickLoop("poll")
	a.kickLoop("heartbeat")
	a.kickLoop("ping")
	return nil
}

// ───────────────────────── poll (§9.1, §9.3)

func (a *Agent) cursor() string {
	var c string
	_, _ = a.db.GetKV("pollCursor", &c)
	return c
}

func (a *Agent) pollLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if !a.enrolled || a.isDeleted() {
			select {
			case <-ctx.Done():
				return
			case <-a.kick["poll"]:
			case <-time.After(2 * time.Second):
			}
			continue
		}
		wait := a.tm.PollWait
		if a.directDown() {
			wait = 0
		}
		path := "/v1/agent/poll?cursor=" + url.QueryEscape(a.cursor()) + "&wait=" + strconv.Itoa(wait)
		var view model.CloudView
		err := a.cloudJSON(ctx, "GET", path, nil, &view, callOpts{timeout: time.Duration(wait+20) * time.Second})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Debug("cloudlink", "poll failed", "error", err.Error(), "backoffSec", backoff.Seconds())
			a.noteCloudError(fmt.Errorf("poll: %w", err))
			select {
			case <-ctx.Done():
				return
			case <-a.kick["poll"]:
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
			continue
		}
		backoff = time.Second
		a.noteCloudError(nil)
		_ = a.db.SetKV("lastCloudOkAt", nowTs())
		view.Source = "cloud"
		a.adoptView(ctx, &view, "cloud")
		if wait == 0 {
			sleepCtx(ctx, 5*time.Second)
		}
	}
}

func (a *Agent) cloudView() *model.CloudView {
	var raw string
	if err := a.db.QueryRow(`SELECT view FROM cloud_view WHERE id = 1`).Scan(&raw); err != nil {
		return nil
	}
	var v model.CloudView
	if json.Unmarshal([]byte(raw), &v) != nil {
		return nil
	}
	return &v
}

// adoptView stores a view if it is newer (latest fetch wins) and reconciles.
func (a *Agent) adoptView(ctx context.Context, v *model.CloudView, source string) bool {
	if v.DeploymentID != a.dep {
		return false
	}
	cur := a.cloudView()
	if cur != nil {
		if source != "cloud" && !(parseTs(v.FetchedAt).After(parseTs(cur.FetchedAt))) {
			return false
		}
		if source == "cloud" && parseTs(v.FetchedAt).Before(parseTs(cur.FetchedAt)) {
			return false
		}
	}
	v.Source = source
	if v.Proposals == nil {
		v.Proposals = []model.ProposalMeta{}
	}
	if v.TransfersToSite == nil {
		v.TransfersToSite = []model.TransferMeta{}
	}
	if v.Peers == nil {
		v.Peers = []model.PeerInfo{}
	}
	_, err := a.db.Exec(`INSERT INTO cloud_view(id, fetchedAt, source, cursor, view) VALUES(1,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
		fetchedAt = excluded.fetchedAt, source = excluded.source, cursor = excluded.cursor, view = excluded.view`, v.FetchedAt, source, v.Cursor, store.JSON(v))
	if err != nil {
		return false
	}
	if source == "cloud" {
		_ = a.db.SetKV("pollCursor", v.Cursor)
	}
	a.storeConsoleInfo(v.ConsoleURL, v.DeploymentLabel)
	a.reconcile(ctx, v)
	return true
}

func (a *Agent) reconcile(ctx context.Context, v *model.CloudView) {
	// (1) peers
	a.storePeers(v.Peers)
	// (2) access key
	a.adoptKeyHash(v.AccessKey)
	// §21 #104 the deployment branch (enters branch mode; fetches + stages the cloud head)
	a.reconcileBranch(ctx, v)
	// (3) proposals
	for _, pm := range v.Proposals {
		local, ok := a.proposal(pm.RevID)
		if !ok {
			if model.IsTerminal(pm.Status) {
				continue
			}
			a.goLoop("fetch-proposal", func(c context.Context) { a.fetchProposal(c, pm) })
			continue
		}
		a.applyCloudStatus(local, pm.Status)
	}
	// (4) transfers to this site
	for _, t := range v.TransfersToSite {
		if t.SiteSlug == a.site {
			a.noteTransferToSite(t)
		}
	}
	for _, t := range v.DeletedTransfersToSite {
		if t.SiteSlug == a.site {
			a.dropDeletedTransfer(t.TransferID)
		}
	}
	a.kickLoop("transfers")
	// (5) §21 #90 cloud "Upgrade agent" requests for this site (selfcloud.go)
	a.handleCloudUpgrades(ctx, v)
	a.volSyncOnView(v) // §21 #101 cloud snapshot requests (vcsync.go)
	// (6) deleted
	if v.Deleted && !a.isDeleted() {
		a.mu.Lock()
		a.deleted = true
		a.mu.Unlock()
		_ = a.db.SetKV("deleted", true)
		a.log.Warn("cloudlink", "deployment deleted in the cloud; polling stops, containers keep running")
	}
}

func (a *Agent) isDeleted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deleted
}

// applyCloudStatus adopts authoritative statuses from the cloud view (terminal states are never reverted).
func (a *Agent) applyCloudStatus(p *model.Proposal, status string) {
	if model.IsTerminal(p.Status) || p.Status == status {
		return
	}
	switch status {
	case model.ProposalSuperseded, model.ProposalStale, model.ProposalApplied, model.ProposalFailedRolledBack:
		if status == model.ProposalApplied {
			if h := a.head(); h == nil || h.RevID != p.RevID {
				return // this site has not committed it (yet); the commit / abandon path decides
			}
		}
		p.Status = status
		p.UpdatedAt = nowTs()
		a.putProposal(p)
		a.stopStaging(p.RevID)
	case model.ProposalApproved, model.ProposalApplying:
		if p.Status == model.ProposalReady || p.Status == model.ProposalStaging || p.Status == model.ProposalApproved {
			p.Status = status
			p.UpdatedAt = nowTs()
			a.putProposal(p)
		}
	}
}

func (a *Agent) fetchProposal(ctx context.Context, pm model.ProposalMeta) {
	if _, ok := a.proposal(pm.RevID); ok {
		return
	}
	var rev *model.Revision
	var prop *model.Proposal
	var res model.AgentProposalResponse
	err := a.cloudJSON(ctx, "GET", "/v1/agent/proposals/"+pm.RevID, nil, &res, callOpts{timeout: 60 * time.Second})
	if err == nil {
		rev, prop = &res.Revision, &res.Proposal
	} else {
		// no cloud route: ask peers
		for _, p := range a.reachablePeers() {
			var pr model.PeerRevisionResponse
			if err2 := a.peerJSON(ctx, p, "GET", "/_opm/peer/revisions/"+pm.RevID, nil, &pr, 30*time.Second); err2 == nil {
				rev = &pr.Revision
				prop = &model.Proposal{RevID: pm.RevID, ParentRevID: pm.ParentRevID, Seq: pm.Seq, Summary: pm.Summary, Origin: pm.Origin,
					Kind: pm.Kind, CreatedAt: pm.CreatedAt, CreatedBy: pm.CreatedBy, Status: pm.Status, Sites: map[string]model.SiteStaging{}, UpdatedAt: nowTs()}
				break
			}
		}
	}
	if rev == nil {
		a.log.Warn("stager", "could not fetch proposal", "revId", pm.RevID, "error", fmt.Sprint(err))
		return
	}
	a.receiveProposal(prop, rev)
}

// receiveProposal stores a proposal + revision (cloud, peer push, local create) and applies the arrival rules (§9.4).
func (a *Agent) receiveProposal(p *model.Proposal, rev *model.Revision) bool {
	if _, ok := a.proposal(p.RevID); ok {
		return false
	}
	if err := a.putRevision(rev); err != nil {
		a.log.Error("stager", "revision rejected", "revId", rev.RevID, "error", err.Error())
		return false
	}
	if p.Sites == nil {
		p.Sites = map[string]model.SiteStaging{}
	}
	for _, s := range a.participatingSites(rev) {
		if _, ok := p.Sites[s]; !ok {
			p.Sites[s] = model.SiteStaging{Staging: model.StagingPending}
		}
	}
	if model.IsTerminal(p.Status) {
		a.putProposal(p)
		return true
	}
	head := a.head()
	if a.branchMode() {
		head = a.legacyHead()
	}
	parentOK := (head == nil && p.ParentRevID == nil) || (head != nil && p.ParentRevID != nil && *p.ParentRevID == head.RevID)
	if !parentOK && !(rev.Kind == "rollback") {
		p.Status = model.ProposalStale
		p.UpdatedAt = nowTs()
		a.putProposal(p)
		hr := ""
		if head != nil {
			hr = head.RevID
		}
		a.activity(nil, "system", "proposal.stale", fmt.Sprintf("proposal seq %d is stale (head moved)", p.Seq),
			map[string]any{"revId": p.RevID, "parentRevId": p.ParentRevID, "headRevId": hr})
		a.queueStatus(p.RevID, model.ProposalStatusReport{SiteSlug: a.site, Status: model.ProposalStale})
		return true
	}
	// supersede: one open proposal at a time, the greatest revId wins
	for _, q := range a.openProposals() {
		if q.RevID == p.RevID || q.Status == model.ProposalApproved || q.Status == model.ProposalApplying {
			continue
		}
		if q.RevID < p.RevID {
			a.supersede(&q, p.RevID)
		} else {
			a.supersede(p, q.RevID)
			a.putProposal(p)
			return true
		}
	}
	a.putProposal(p)
	a.log.Info("stager", "proposal received", "revId", p.RevID, "seq", p.Seq, "origin", p.Origin)
	if a.branchMode() {
		// §21 #104 §9.3 legacy participant: a compat proposal for agents < 0.14 is never applied here; this site reports
		// it staged so an old coordinator's gate can open, and answers its update steps as a no-op participant
		a.markBranchRev(p.RevID)
		a.setStaging(p.RevID, a.site, model.SiteStaging{Staging: model.StagingReady, Progress: 1}, nil)
		return true
	}
	if contains(a.participatingSites(rev), a.site) {
		a.startStaging(p.RevID)
	}
	return true
}

func (a *Agent) supersede(q *model.Proposal, by string) {
	q.Status = model.ProposalSuperseded
	q.SupersededBy = &by
	q.UpdatedAt = nowTs()
	a.putProposal(q)
	a.stopStaging(q.RevID)
	a.activity(nil, "system", "proposal.superseded", fmt.Sprintf("proposal seq %d superseded", q.Seq), map[string]any{"revId": q.RevID, "byRevId": by})
}

// ───────────────────────── peers + access key

func (a *Agent) storePeers(peers []model.PeerInfo) {
	if len(peers) == 0 {
		return
	}
	keep := map[string]bool{}
	for _, p := range peers {
		keep[p.SiteSlug] = true
		_, _ = a.db.Exec(`INSERT INTO peers(siteSlug, publicKey, advertiseHost, agentPort, enrolledAt) VALUES(?,?,?,?,?)
			ON CONFLICT(siteSlug) DO UPDATE SET publicKey = excluded.publicKey, advertiseHost = excluded.advertiseHost,
			agentPort = CASE WHEN peers.publicKey = excluded.publicKey AND peers.lastPing IS NOT NULL THEN peers.agentPort ELSE excluded.agentPort END,
			enrolledAt = excluded.enrolledAt`, p.SiteSlug, p.PublicKey, p.AdvertiseHost, p.AgentPort, p.EnrolledAt)
	}
	for _, p := range a.peerRows() {
		if !keep[p.SiteSlug] {
			_, _ = a.db.Exec(`DELETE FROM peers WHERE siteSlug = ?`, p.SiteSlug)
			a.mu.Lock()
			delete(a.peerState, p.SiteSlug)
			a.mu.Unlock()
		}
	}
}

type peerRow struct {
	model.PeerInfo
	LastAnswerAt int64
	RttMs        float64
	LastPing     *pingWire
}

func (a *Agent) peerRows() []peerRow {
	rows, err := a.db.Query(`SELECT siteSlug, publicKey, advertiseHost, agentPort, enrolledAt, lastAnswerAt, rttMs, lastPing FROM peers ORDER BY siteSlug`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []peerRow
	for rows.Next() {
		var r peerRow
		var la sql.NullInt64
		var rtt sql.NullFloat64
		var lp sql.NullString
		if rows.Scan(&r.SiteSlug, &r.PublicKey, &r.AdvertiseHost, &r.AgentPort, &r.EnrolledAt, &la, &rtt, &lp) != nil {
			continue
		}
		r.LastAnswerAt, r.RttMs = la.Int64, rtt.Float64
		if lp.Valid {
			var pw pingWire
			if json.Unmarshal([]byte(lp.String), &pw) == nil {
				r.LastPing = &pw
			}
		}
		out = append(out, r)
	}
	return out
}

func (a *Agent) peerRow(slug string) (*peerRow, bool) {
	for _, p := range a.peerRows() {
		if p.SiteSlug == slug {
			pp := p
			return &pp, true
		}
	}
	return nil, false
}

func (a *Agent) adoptKeyHash(k model.AccessKeyRef) {
	if k.Hash == "" {
		return
	}
	_, cur := a.keyHash()
	if k.Version > cur {
		_ = a.db.SetKV("keyHash", k)
		a.activity(nil, "system", "access-key.received", fmt.Sprintf("access key version %d received", k.Version), map[string]any{"version": k.Version})
		a.log.Info("auth", "access key hash adopted", "version", k.Version)
	}
}

// ───────────────────────── heartbeat (§9.9)

func (a *Agent) heartbeatOnce(ctx context.Context) {
	if !a.enrolled || a.isDeleted() {
		return
	}
	var hr *string
	if h := a.head(); h != nil {
		hr = &h.RevID
	}
	var hs *model.HeartbeatStaging
	open := a.openProposals()
	sort.Slice(open, func(i, j int) bool { return open[i].RevID > open[j].RevID })
	for _, p := range open {
		if row, ok := a.staging(p.RevID, a.site); ok {
			hs = &model.HeartbeatStaging{RevID: p.RevID, State: row.Staging, Progress: row.Progress}
			break
		}
	}
	seen := []model.PeerSeen{}
	for _, p := range a.peerRows() {
		if p.SiteSlug == a.site || p.LastAnswerAt == 0 {
			continue
		}
		seen = append(seen, model.PeerSeen{SiteSlug: p.SiteSlug, LastSeenAt: tsOf(time.Unix(p.LastAnswerAt, 0))})
	}
	m := a.hostMetrics()
	_, kv := a.keyHash()
	er := a.edgeReport()
	req := model.HeartbeatRequest{SiteSlug: a.site, AgentVersion: a.version, HeadRevID: hr, Staging: hs, PeersSeen: seen,
		Metrics: model.HostMetricsSummary{CPU: m.CPUPct / 100, MemPct: safeDiv(m.MemUsedMb, m.MemTotalMb), DiskPct: m.DiskUsedPct / 100},
		Edge:    &er, KeyVersion: kv, AdvertiseHost: a.cfg.AdvertiseHost, AgentPort: a.currentAgentPort(), IP: detectIP(), Hostname: hostname(),
		DetectedAddresses: detectAddresses()}
	req.GuiPorts = a.guiPorts()
	req.Upgrade = a.heartbeatUpgrade()  // §21 #90
	req.AgentTime = tsOf(time.Now())    // §21 #92 raw local clock (the cloud measures the skew)
	req.VolumeSync = a.volSyncReports() // §21 #101
	req.Branch = a.branchReport()       // §21 #104
	// §21 #112 the agent process itself, compact (memory, CPU since the last heartbeat, how many self-warnings)
	if rss, cpu, _, _, ok := readProcSelf(); ok {
		req.Metrics.AgentRSSBytes = &rss
		a.procMu.Lock()
		if prev := a.hbPrev; !prev.at.IsZero() && time.Since(prev.at) > time.Second {
			p := 100 * (cpu - prev.cpuT) / time.Since(prev.at).Seconds()
			req.Metrics.AgentCPUPct = &p
		}
		a.hbPrev = procSample{at: time.Now(), cpuT: cpu}
		a.procMu.Unlock()
	}
	req.Metrics.AgentWarnings = len(a.selfWarnings())
	var resp model.HeartbeatResponse
	if err := a.cloudJSON(ctx, "POST", "/v1/agent/heartbeat", req, &resp, callOpts{timeout: 30 * time.Second}); err != nil {
		a.log.Debug("cloudlink", "heartbeat failed", "error", err.Error())
		a.noteCloudError(fmt.Errorf("heartbeat: %w", err))
		return
	}
	_ = a.db.SetKV("lastHeartbeatAt", nowTs())
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// ───────────────────────── outbox (queued cloud reports, FIFO, idempotent)

func (a *Agent) enqueue(kind, dedupe, method, path string, body any) {
	b, _ := json.Marshal(body)
	if dedupe != "" {
		_, _ = a.db.Exec(`DELETE FROM outbox WHERE dedupe = ?`, dedupe)
	}
	_, err := a.db.Exec(`INSERT INTO outbox(kind, dedupe, method, path, body, createdAt, nextAt) VALUES(?,?,?,?,?,?,?)`,
		kind, nullIfEmpty(dedupe), method, path, b, time.Now().Unix(), 0)
	if err != nil {
		a.log.Error("cloudlink", "outbox insert failed", "error", err.Error())
	}
	a.kickLoop("outbox")
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (a *Agent) queueStatus(revID string, r model.ProposalStatusReport) {
	dedupe := ""
	if r.Status == "" {
		dedupe = "staging:" + revID
	}
	a.enqueue("proposal-status", dedupe, "POST", "/v1/agent/proposals/"+revID+"/status", r)
}

// §21 #112 outbox dead letters: parked with nextAt = deadLetterAt (never due), pruned after 30 days (dbmaint.go)
const (
	outboxDeadAfter       = 20
	deadLetterAt    int64 = 1 << 40
)

// deadLetters counts the parked outbox items (status / self-metrics).
func (a *Agent) deadLetters() int {
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE nextAt >= ?`, deadLetterAt).Scan(&n)
	return n
}

type outboxItem struct {
	id       int64
	kind     string
	method   string
	path     string
	body     []byte
	attempts int
}

func (a *Agent) outboxOnce(ctx context.Context) {
	if !a.enrolled || a.isDeleted() {
		return
	}
	for ctx.Err() == nil {
		var it outboxItem
		err := a.db.QueryRow(`SELECT id, kind, method, path, body, attempts FROM outbox WHERE nextAt <= ? ORDER BY id LIMIT 1`, time.Now().Unix()).
			Scan(&it.id, &it.kind, &it.method, &it.path, &it.body, &it.attempts)
		if err != nil {
			return
		}
		resp, err := a.cloudCall(ctx, it.method, it.path, it.body, callOpts{timeout: 60 * time.Second, noTunnel: it.kind == "upload"})
		if err == nil && (resp.Status < 300 || (resp.Status >= 400 && resp.Status < 500 && resp.Status != 429 && resp.Status != 401)) {
			if resp.Status >= 300 {
				a.log.Warn("cloudlink", "outbox item refused by the cloud (dropped)", "kind", it.kind, "path", pathOnly(it.path), "status", resp.Status, "body", string(resp.Body))
			}
			_, _ = a.db.Exec(`DELETE FROM outbox WHERE id = ?`, it.id)
			continue
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		} else {
			msg = fmt.Sprintf("HTTP %d", resp.Status)
		}
		if err == nil && resp.Status == 401 {
			a.cloudKeyRejected.Store(time.Now().Unix()) // §21 #112 status: "cloud key rejected" (local operation goes on)
		}
		// §21 #112: an item the cloud keeps failing with 5xx is parked (dead letter) after outboxDeadAfter tries, so it
		// never blocks every later report; network errors / 401 / 429 hit every item alike and keep the FIFO wait
		if err == nil && resp.Status >= 500 && it.attempts+1 >= outboxDeadAfter {
			a.log.Error("cloudlink", "outbox item parked after repeated cloud errors (dead letter, kept 30 days)", "kind", it.kind, "path", pathOnly(it.path), "status", resp.Status, "attempts", it.attempts+1)
			_, _ = a.db.Exec(`UPDATE outbox SET attempts = attempts + 1, nextAt = ?, lastError = ? WHERE id = ?`, deadLetterAt, msg, it.id)
			continue
		}
		backoff := time.Duration(1<<uint(min(it.attempts, 8))) * time.Second
		if backoff > 5*time.Minute {
			backoff = 5 * time.Minute
		}
		_, _ = a.db.Exec(`UPDATE outbox SET attempts = attempts + 1, nextAt = ?, lastError = ? WHERE id = ?`, time.Now().Add(backoff).Unix(), msg, it.id)
		return // FIFO: wait for the head item
	}
}

// ───────────────────────── activities pipe (§9.8)

func (a *Agent) pipeOnce(ctx context.Context) {
	if !a.enrolled || a.isDeleted() {
		return
	}
	var until string
	if ok, _ := a.db.GetKV("pipeRetryAt", &until); ok && time.Now().Before(parseTs(until)) {
		return
	}
	for ctx.Err() == nil {
		ack := a.kvInt("ackSeq")
		entries := a.activitiesRange(ack+1, 0, 500)
		if len(entries) == 0 {
			return
		}
		var resp model.AgentActivitiesResponse
		err := a.cloudJSON(ctx, "POST", "/v1/agent/activities", model.AgentActivitiesRequest{SiteSlug: a.site, Entries: entries}, &resp, callOpts{timeout: 60 * time.Second})
		if err != nil {
			if ae := asAPIError(err); ae != nil && ae.Status == 422 {
				a.log.Error("pipe", "cloud refused the activity chain (retry in 10 min)", "error", err.Error())
				_ = a.db.SetKV("pipeRetryAt", tsOf(time.Now().Add(10*time.Minute)))
			} else {
				a.log.Debug("pipe", "upload failed", "error", err.Error())
			}
			return
		}
		if resp.AckSeq <= ack {
			return
		}
		_ = a.db.SetKV("ackSeq", resp.AckSeq)
	}
}

var errNoRoute = errors.New("no cloud route")
