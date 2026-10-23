package fakecloud

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

// reqCtx describes the caller of an agent endpoint.
type reqCtx struct {
	signer   string // verified site slug ("" for install endpoints)
	relayVia string // relaying site ("" = direct)
}

func setCommon(w http.ResponseWriter) {
	w.Header().Set("X-OPM-Server-Time", strconv.FormatInt(time.Now().Unix(), 10))
	w.Header().Set("X-Request-Id", randID())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeErr(w http.ResponseWriter, code, msg string, details map[string]any) {
	st := model.ErrorHTTPStatus[code]
	if st == 0 {
		st = 500
	}
	writeJSON(w, st, model.APIErrorBody{Error: model.APIError{Code: code, Message: msg, Details: details}})
}

func header(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// ServeHTTP is the entry point for direct requests.
func (c *Cloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if site := r.Header.Get("X-OPM-Site"); site != "" {
		slug := site
		if i := strings.Index(site, "/"); i >= 0 {
			slug = site[i+1:]
		}
		c.mu.Lock()
		blocked := c.blocked[slug]
		c.mu.Unlock()
		if blocked {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			panic(http.ErrAbortHandler)
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	setCommon(w)
	if err != nil {
		writeErr(w, model.ErrInvalid, "cannot read body", nil)
		return
	}
	c.mu.Lock()
	c.requests = append(c.requests, r.URL.Path)
	c.mu.Unlock()
	if r.URL.Path == "/v1/agent/enroll" && r.Method == http.MethodPost {
		c.enroll(w, r, body)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/agent/") {
		writeErr(w, model.ErrNotFound, "no route "+r.Method+" "+r.URL.Path, nil)
		return
	}
	hdr := map[string]string{}
	for k := range r.Header {
		hdr[k] = r.Header.Get(k)
	}
	slug, reason, msg := c.verify(r.Method, r.URL.RequestURI(), hdr, body)
	if reason != "" {
		writeErr(w, model.ErrUnauthenticated, msg, map[string]any{"reason": reason})
		return
	}
	c.route(w, r, body, reqCtx{signer: slug})
}

// verify checks a signed agent request (§4.4). Returns the signer slug or a failure reason + message.
func (c *Cloud) verify(method, uri string, h map[string]string, body []byte) (string, string, string) {
	site := header(h, "X-OPM-Site")
	tsS, nonce, sig := header(h, "X-OPM-Ts"), header(h, "X-OPM-Nonce"), header(h, "X-OPM-Sig")
	parts := strings.SplitN(site, "/", 2)
	if len(parts) != 2 || parts[0] != c.DeploymentID {
		return "", "unknown-site", "unknown site " + site
	}
	slug := parts[1]
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.agents[slug]
	if !ok || c.deleted {
		return "", "unknown-site", "site " + slug + " is not enrolled"
	}
	ts, err := strconv.ParseInt(tsS, 10, 64)
	if err != nil {
		return "", "clock", "bad X-OPM-Ts"
	}
	if d := time.Now().Unix() - ts; d > 60 || d < -60 {
		return "", "clock", fmt.Sprintf("clock skew %d s", d)
	}
	pub, err := base64.StdEncoding.DecodeString(a.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", "unknown-site", "bad public key"
	}
	sb, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", "signature", "bad signature encoding"
	}
	str := canonical.SignedRequestString(method, uri, ts, nonce, sha256Hex(body))
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(str), sb) {
		return "", "signature", "signature does not verify"
	}
	now := time.Now()
	for k, exp := range c.nonces {
		if now.After(exp) {
			delete(c.nonces, k)
		}
	}
	nk := slug + "/" + nonce
	if _, seen := c.nonces[nk]; seen || nonce == "" {
		return "", "nonce", "nonce replayed"
	}
	c.nonces[nk] = now.Add(5 * time.Minute)
	return slug, "", ""
}

var (
	reProposal       = regexp.MustCompile(`^/v1/agent/proposals/([^/]+)$`)
	reProposalStatus = regexp.MustCompile(`^/v1/agent/proposals/([^/]+)/status$`)
	reRevision       = regexp.MustCompile(`^/v1/agent/revisions/([^/]+)$`)
	reManifest       = regexp.MustCompile(`^/v1/agent/images/([^/]+)/([^/]+)/manifest$`)
	reBlob           = regexp.MustCompile(`^/v1/agent/blobs/([^/]+)$`)
	reTransferStatus = regexp.MustCompile(`^/v1/agent/transfers/([^/]+)/status$`)
	reTransferDl     = regexp.MustCompile(`^/v1/agent/transfers/([^/]+)/download$`)
	reUpload         = regexp.MustCompile(`^/v1/agent/uploads/([^/]+)$`)
	reUploadPart     = regexp.MustCompile(`^/v1/agent/uploads/([^/]+)/parts/(\d+)$`)
	reUploadComplete = regexp.MustCompile(`^/v1/agent/uploads/([^/]+)/complete$`)
)

func (c *Cloud) route(w http.ResponseWriter, r *http.Request, body []byte, rc reqCtx) {
	c.mu.Lock()
	if a, ok := c.agents[rc.signer]; ok {
		if rc.relayVia == "" {
			a.RelayVia = nil
		} else {
			v := rc.relayVia
			a.RelayVia = &v
		}
	}
	c.mu.Unlock()
	p, m := r.URL.Path, r.Method
	var g []string
	switch {
	case m == "GET" && p == "/v1/agent/poll":
		c.poll(w, r, rc)
	case m == "POST" && p == "/v1/agent/heartbeat":
		c.heartbeat(w, body, rc)
	case m == "POST" && p == "/v1/agent/head":
		c.reportHead(w, body)
	case m == "POST" && p == "/v1/agent/proposals":
		c.registerLocal(w, body)
	case m == "POST" && p == "/v1/agent/activities":
		c.uploadActivities(w, body, rc)
	case m == "GET" && p == "/v1/agent/transfers":
		c.listTransfers(w, r, rc)
	case m == "POST" && p == "/v1/agent/transfers":
		c.createTransfer(w, body, rc)
	case m == "GET" && p == "/v1/agent/release":
		c.agentRelease(w, r)
	case m == "GET" && p == "/v1/agent/binary":
		c.agentBinary(w, r)
	case m == "POST" && p == "/v1/agent/relay":
		c.relay(w, r, body, rc)
	case m == "POST" && match(reProposalStatus, p, &g):
		c.proposalStatus(w, g[1], body)
	case m == "GET" && match(reProposal, p, &g):
		c.getProposal(w, g[1])
	case m == "GET" && match(reRevision, p, &g):
		c.getRevision(w, g[1])
	case m == "GET" && match(reManifest, p, &g):
		c.manifest(w, g[1], g[2])
	case m == "GET" && match(reBlob, p, &g):
		c.blob(w, r, g[1])
	case m == "POST" && match(reTransferStatus, p, &g):
		c.transferStatus(w, g[1], body)
	case m == "GET" && match(reTransferDl, p, &g):
		c.transferDownload(w, r, g[1], rc)
	case m == "GET" && match(reUpload, p, &g):
		c.getUpload(w, g[1], rc)
	case m == "PUT" && match(reUploadPart, p, &g):
		c.putPart(w, g[1], g[2], body, rc)
	case m == "POST" && match(reUploadComplete, p, &g):
		c.completeUpload(w, g[1], rc)
	default:
		if c.routeTunnels(w, r, body, rc) { // §21 #125
			return
		}
		if c.routeBranch(w, r, body, rc) { // §21 #104
			return
		}
		if c.routeFiles(w, r, body, rc) { // §21 #123
			return
		}
		if !c.routeVolumeContent(w, r, body, rc) { // §21 #101
			writeErr(w, model.ErrNotFound, "no route "+m+" "+p, nil)
		}
	}
}

func match(re *regexp.Regexp, p string, g *[]string) bool {
	*g = re.FindStringSubmatch(p)
	return *g != nil
}

func decode(w http.ResponseWriter, body []byte, v any) bool {
	if err := json.Unmarshal(body, v); err != nil {
		writeErr(w, model.ErrInvalid, "bad JSON: "+err.Error(), nil)
		return false
	}
	return true
}

// ───────────────────────── enroll

func (c *Cloud) enroll(w http.ResponseWriter, r *http.Request, body []byte) {
	auth := r.Header.Get("Authorization")
	tok := strings.TrimPrefix(auth, "Bearer ")
	if !strings.HasPrefix(tok, "opi_") {
		writeErr(w, model.ErrUnauthenticated, "install token required", nil)
		return
	}
	var req model.EnrollRequest
	if !decode(w, body, &req) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tokens[sha256Hex([]byte(tok))]
	if !ok {
		writeErr(w, model.ErrUnauthenticated, "unknown install token", nil)
		return
	}
	if t.usedAt != nil || time.Now().After(t.expiresAt) || c.deleted {
		writeErr(w, model.ErrGone, "install token used or expired", nil)
		return
	}
	pub, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		writeErr(w, model.ErrInvalid, "publicKey must be 32 bytes base64", nil)
		return
	}
	if _, exists := c.agents[t.siteSlug]; exists {
		writeErr(w, model.ErrConflict, "site is enrolled; revoke first", nil)
		return
	}
	now := time.Now()
	t.usedAt = &now
	ts := nowTs()
	c.agents[t.siteSlug] = &model.AgentDoc{SiteSlug: t.siteSlug, PublicKey: req.PublicKey, EnrolledAt: ts, AgentVersion: req.AgentVersion, LastSeenAt: ts, Online: true, IP: req.IP, Hostname: req.Hostname, AdvertiseHost: req.AdvertiseHost, AgentPort: req.AgentPort, OS: req.OS, Arch: req.Arch, KeyVersion: c.accessKey.Version, PeersSeen: []model.PeerSeen{}, DetectedAddresses: addrs(req.DetectedAddresses)}
	c.epoch++
	c.cloudActivity("install.enrolled", t.siteSlug+" enrolled", map[string]any{"siteSlug": t.siteSlug, "agentVersion": req.AgentVersion})
	writeJSON(w, 201, model.EnrollResponse{DeploymentID: c.DeploymentID, SiteSlug: t.siteSlug, Peers: c.peers(), AccessKey: c.accessKey, ServerTime: ts, ConsoleURL: c.consoleURL, DeploymentLabel: c.depLabel})
}

// ───────────────────────── poll + heartbeat

func (c *Cloud) view() model.CloudView {
	v := model.CloudView{FetchedAt: nowTs(), Source: "cloud", Cursor: strconv.Itoa(c.epoch), DeploymentID: c.DeploymentID, Deleted: c.deleted, Proposals: []model.ProposalMeta{}, TransfersToSite: []model.TransferMeta{}, AccessKey: c.accessKey, Peers: c.peers(), ConsoleURL: c.consoleURL, DeploymentLabel: c.depLabel,
		AgentRelease: c.viewRelease(), AgentUpgrades: c.viewUpgrades()}
	var open, term []*model.Proposal
	for _, p := range c.proposals {
		if isOpen(p.Status) {
			open = append(open, p)
		} else {
			term = append(term, p)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].RevID > open[j].RevID })
	sort.Slice(term, func(i, j int) bool { return term[i].RevID > term[j].RevID })
	if len(term) > 5 {
		term = term[:5]
	}
	for _, p := range append(open, term...) {
		v.Proposals = append(v.Proposals, model.ProposalMeta{RevID: p.RevID, ParentRevID: p.ParentRevID, Seq: p.Seq, Summary: p.Summary, Status: p.Status, Origin: p.Origin, Kind: p.Kind, CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy})
	}
	ids := make([]string, 0, len(c.transfers))
	for id := range c.transfers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t := c.transfers[id]
		if t.Direction == "to-site" && t.Status == "ready" {
			v.TransfersToSite = append(v.TransfersToSite, model.TransferMeta{TransferID: t.TransferID, SiteSlug: t.SiteSlug, Direction: t.Direction, FileName: t.FileName, SizeBytes: t.SizeBytes, Sha256: t.Sha256, Status: t.Status, CreatedAt: t.CreatedAt, CreatedBy: t.CreatedBy, Note: t.Note})
		}
		if t.Direction == "to-site" && t.Status == "expired" {
			v.DeletedTransfersToSite = append(v.DeletedTransfersToSite, model.DeletedTransfer{TransferID: t.TransferID, SiteSlug: t.SiteSlug})
		}
	}
	if c.head != nil {
		v.DraftHead = &model.RevRef{RevID: c.head.RevID, Seq: c.head.Seq}
	}
	v.VolumeContent = c.viewVolumeContent() // §21 #101
	c.viewFiles(&v)                         // §21 #123
	c.viewTunnels(&v)                       // §21 #125
	v.Branch = c.viewBranch()               // §21 #104
	return v
}

func (c *Cloud) poll(w http.ResponseWriter, r *http.Request, rc reqCtx) {
	q := r.URL.Query()
	cursor := q.Get("cursor")
	wait := 25
	if s := q.Get("wait"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			wait = n
		}
	}
	if wait > 50 {
		wait = 50
	}
	if wait < 0 {
		wait = 0
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		c.mu.Lock()
		if a, ok := c.agents[rc.signer]; ok {
			a.LastSeenAt = nowTs()
			a.Online = true
		}
		if cursor != strconv.Itoa(c.epoch) || wait == 0 || !time.Now().Before(deadline) {
			v := c.view()
			c.mu.Unlock()
			writeJSON(w, 200, v)
			return
		}
		c.mu.Unlock()
		select {
		case <-r.Context().Done():
			return
		case <-c.closing:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (c *Cloud) heartbeat(w http.ResponseWriter, body []byte, rc reqCtx) {
	var req model.HeartbeatRequest
	if !decode(w, body, &req) {
		return
	}
	if req.SiteSlug != rc.signer {
		writeErr(w, model.ErrInvalid, "siteSlug must equal the signer", nil)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.agents[rc.signer]
	if a.AdvertiseHost != req.AdvertiseHost || a.AgentPort != req.AgentPort {
		c.epoch++
	}
	a.LastSeenAt, a.Online = nowTs(), true
	c.agentTimes[rc.signer] = req.AgentTime
	a.AgentVersion, a.HeadRevID, a.Staging = req.AgentVersion, req.HeadRevID, req.Staging
	a.PeersSeen = req.PeersSeen
	c.storeVolumeSync(rc.signer, req.VolumeSync) // §21 #101
	if req.Branch != nil {
		c.brState().reports[rc.signer] = req.Branch // §21 #104
	}
	if a.PeersSeen == nil {
		a.PeersSeen = []model.PeerSeen{}
	}
	m := req.Metrics
	a.Metrics, a.Edge, a.KeyVersion = &m, req.Edge, req.KeyVersion
	a.AdvertiseHost, a.AgentPort, a.IP, a.Hostname = req.AdvertiseHost, req.AgentPort, req.IP, req.Hostname
	if req.DetectedAddresses != nil {
		a.DetectedAddresses = addrs(req.DetectedAddresses)
	}
	if req.GuiPorts != nil {
		a.GuiPorts = req.GuiPorts
	}
	c.noteUpgrade(a, req.Upgrade, req.AgentVersion) // §21 #90
	writeJSON(w, 200, model.HeartbeatResponse{Ok: true, ServerTime: nowTs()})
}

// ───────────────────────── revisions + proposals

func (c *Cloud) reportHead(w http.ResponseWriter, body []byte) {
	var req model.ReportHeadRequest
	if !decode(w, body, &req) {
		return
	}
	rev := req.Revision
	if h, _ := canonical.HashCanonical(rev.Config); h != rev.Hash {
		writeErr(w, model.ErrInvalid, "revision hash mismatch", map[string]any{"expected": h})
		return
	}
	if h, _ := canonical.HashCanonical(rev.Catalog); h != rev.CatalogHash {
		writeErr(w, model.ErrInvalid, "catalog hash mismatch", map[string]any{"expected": h})
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if rev.ParentRevID == nil {
		if rev.Seq != 1 {
			writeErr(w, model.ErrInvalid, "a revision without parent must have seq 1", nil)
			return
		}
	} else {
		par, ok := c.revisions[*rev.ParentRevID]
		if ok && rev.Seq != par.Seq+1 {
			writeErr(w, model.ErrInvalid, "seq must be parent.seq + 1", nil)
			return
		}
		if !ok && (c.head == nil || rev.Seq <= 1) {
			writeErr(w, model.ErrInvalid, "parent revision unknown", nil)
			return
		}
	}
	if c.head != nil && rev.Seq == c.head.Seq && rev.RevID != c.head.RevID {
		writeErr(w, model.ErrConflict, "another revision has this seq (fork)", nil)
		return
	}
	_, had := c.revisions[rev.RevID]
	if !had {
		c.revisions[rev.RevID] = rev
	}
	changed := false
	if c.head == nil || rev.Seq > c.head.Seq {
		c.head = &model.HeadRef{RevID: rev.RevID, Seq: rev.Seq, Hash: rev.Hash, ReportedAt: nowTs()}
		changed = true
		c.epoch++
		for id, p := range c.proposals {
			if id == rev.RevID {
				if p.Status != model.ProposalApplied {
					p.Status = model.ProposalApplied
					p.UpdatedAt = nowTs()
				}
				continue
			}
			if isOpen(p.Status) && (p.ParentRevID == nil || *p.ParentRevID != rev.RevID) {
				p.Status = model.ProposalStale
				p.UpdatedAt = nowTs()
				c.cloudActivity("proposal.stale", "stale", map[string]any{"revId": id, "headRevId": rev.RevID})
			}
		}
	}
	writeJSON(w, 200, model.ReportHeadResponse{Stored: !had, HeadChanged: changed})
}

func (c *Cloud) getProposal(w http.ResponseWriter, revID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.proposals[revID]
	rev, ok2 := c.revisions[revID]
	if !ok || !ok2 {
		writeErr(w, model.ErrNotFound, "proposal "+revID+" not found", nil)
		return
	}
	writeJSON(w, 200, model.AgentProposalResponse{Proposal: copyProposal(p), Revision: rev})
}

func (c *Cloud) getRevision(w http.ResponseWriter, revID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rev, ok := c.revisions[revID]
	if !ok {
		writeErr(w, model.ErrNotFound, "revision "+revID+" not found", nil)
		return
	}
	writeJSON(w, 200, model.RevisionResponse{Revision: rev})
}

func (c *Cloud) registerLocal(w http.ResponseWriter, body []byte) {
	var req model.RegisterLocalProposalRequest
	if !decode(w, body, &req) {
		return
	}
	if req.Proposal.RevID == "" || req.Proposal.RevID != req.Revision.RevID {
		writeErr(w, model.ErrInvalid, "proposal.revId must equal revision.revId", nil)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.proposals[req.Proposal.RevID]; ok {
		writeJSON(w, 200, model.StoredResponse{Stored: false})
		return
	}
	if _, ok := c.revisions[req.Revision.RevID]; !ok {
		c.revisions[req.Revision.RevID] = req.Revision
	}
	p := req.Proposal
	p.Origin = "agent"
	if p.Sites == nil {
		p.Sites = map[string]model.SiteStaging{}
	}
	c.supersedeOlder(p.RevID)
	c.proposals[p.RevID] = &p
	c.epoch++
	writeJSON(w, 200, model.StoredResponse{Stored: true})
}

func (c *Cloud) proposalStatus(w http.ResponseWriter, revID string, body []byte) {
	var req model.ProposalStatusReport
	if !decode(w, body, &req) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.proposals[revID]
	if !ok {
		writeErr(w, model.ErrNotFound, "proposal "+revID+" not found", nil)
		return
	}
	before, _ := json.Marshal(p)
	if req.Staging != "" || req.Progress != nil || req.Error.Set {
		s := p.Sites[req.SiteSlug]
		if req.Staging != "" {
			s.Staging = req.Staging
		}
		if s.Staging == "" {
			s.Staging = model.StagingPending
		}
		if req.Progress != nil {
			s.Progress = *req.Progress
		}
		if req.Error.Set {
			s.Error = req.Error.Value
		}
		now := nowTs()
		s.UpdatedAt = &now
		p.Sites[req.SiteSlug] = s
	}
	if req.Status != "" {
		p.Status = req.Status
		if req.ApprovedBy != "" {
			v := req.ApprovedBy
			p.ApprovedBy = &v
		}
		if req.ApprovedAt != "" {
			v := req.ApprovedAt
			p.ApprovedAt = &v
		}
		if req.UpdateID != "" {
			v := req.UpdateID
			p.UpdateID = &v
		}
		if req.Result != nil {
			r := *req.Result
			p.Result = &r
		}
	} else {
		recompute(p)
	}
	after, _ := json.Marshal(p)
	if !bytes.Equal(before, after) {
		p.UpdatedAt = nowTs()
		c.epoch++
	}
	writeJSON(w, 200, model.ProposalStatusResponse{Ok: true, Proposal: copyProposal(p)})
}

// ───────────────────────── images + blobs

func (c *Cloud) manifest(w http.ResponseWriter, serviceID, tag string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := serviceID + ":" + tag
	for _, rev := range c.revisions {
		if im, ok := rev.Catalog.Images[key]; ok {
			layers := im.Layers
			if layers == nil {
				layers = []model.ImageLayer{}
			}
			cfgSize := int64(len(c.blobs[strings.TrimPrefix(im.ConfigDigest, "sha256:")]))
			writeJSON(w, 200, model.ImageManifestResponse{ServiceSlug: serviceID, Version: tag, Digest: im.Digest, Platform: im.Platform, MediaType: im.ManifestMediaType, Config: model.ImageConfigRef{Digest: im.ConfigDigest, SizeBytes: cfgSize}, Layers: layers})
			return
		}
	}
	if im, ok := c.commitImage(key); ok { // §21 #104 images of the deployment branch
		layers := im.Layers
		if layers == nil {
			layers = []model.ImageLayer{}
		}
		cfgSize := int64(len(c.blobs[strings.TrimPrefix(im.ConfigDigest, "sha256:")]))
		writeJSON(w, 200, model.ImageManifestResponse{ServiceSlug: serviceID, Version: tag, Digest: im.Digest, Platform: im.Platform, MediaType: im.ManifestMediaType, Config: model.ImageConfigRef{Digest: im.ConfigDigest, SizeBytes: cfgSize}, Layers: layers})
		return
	}
	writeErr(w, model.ErrNotFound, "image "+key+" not found", nil)
}

func (c *Cloud) blob(w http.ResponseWriter, r *http.Request, param string) {
	h := strings.ToLower(strings.TrimPrefix(param, "sha256:"))
	c.mu.Lock()
	data, ok := c.blobs[h]
	c.mu.Unlock()
	if !ok {
		writeErr(w, model.ErrNotFound, "blob "+param+" not found", nil)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// ───────────────────────── activities

func (c *Cloud) uploadActivities(w http.ResponseWriter, body []byte, rc reqCtx) {
	var req model.AgentActivitiesRequest
	if !decode(w, body, &req) {
		return
	}
	if req.SiteSlug != rc.signer {
		writeErr(w, model.ErrInvalid, "siteSlug must equal the signer", nil)
		return
	}
	if len(req.Entries) > 500 {
		writeErr(w, model.ErrInvalid, "at most 500 entries", nil)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	log := c.activities[rc.signer]
	if log == nil {
		log = map[int]model.Activity{}
		c.activities[rc.signer] = log
	}
	ack := func() int {
		n := 0
		for {
			if _, ok := log[n+1]; !ok {
				return n
			}
			n++
		}
	}
	res := model.AgentActivitiesResponse{Errors: []model.ActivityError{}}
	cur := ack()
	if len(req.Entries) > 0 {
		for i := 1; i < len(req.Entries); i++ {
			if req.Entries[i].Seq != req.Entries[i-1].Seq+1 {
				writeErr(w, model.ErrInvalid, "entries must be contiguous ascending seq", nil)
				return
			}
		}
		if req.Entries[0].Seq < 1 || req.Entries[0].Seq > cur+1 {
			writeErr(w, model.ErrInvalid, fmt.Sprintf("missing seq %d", cur+1), nil)
			return
		}
		broken := false
		for i, e := range req.Entries {
			if broken {
				res.Rejected++
				res.Errors = append(res.Errors, model.ActivityError{ActivityID: e.ActivityID, Reason: "after a broken entry"})
				continue
			}
			var wantPrev *string
			if e.Seq > 1 {
				var h string
				if i > 0 {
					h = req.Entries[i-1].Hash
				} else {
					h = log[e.Seq-1].Hash
				}
				wantPrev = &h
			}
			reason := ""
			if e.SiteSlug != rc.signer || e.DeploymentID != c.DeploymentID || e.ActivityID != e.SiteSlug+":"+strconv.Itoa(e.Seq) {
				reason = "wrong site, deployment or activityId"
			} else if (wantPrev == nil) != (e.PrevHash == nil) || (wantPrev != nil && *wantPrev != *e.PrevHash) {
				reason = "prevHash does not chain"
			} else if h, err := canonical.ActivityHash(activityFields(e)); err != nil || h != e.Hash {
				reason = "hash mismatch"
			}
			if reason != "" {
				broken = true
				res.Rejected++
				res.Errors = append(res.Errors, model.ActivityError{ActivityID: e.ActivityID, Reason: reason})
				continue
			}
			if _, exists := log[e.Seq]; exists {
				res.Skipped++
				continue
			}
			via, at := "pipe", nowTs()
			e.UploadedVia, e.UploadedAt = &via, &at
			log[e.Seq] = e
			res.Inserted++
		}
	}
	res.AckSeq = ack()
	if a, ok := c.agents[rc.signer]; ok {
		a.AckSeq = res.AckSeq
	}
	writeJSON(w, 200, res)
}

// ───────────────────────── transfers + uploads

func (c *Cloud) listTransfers(w http.ResponseWriter, r *http.Request, rc reqCtx) {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := []model.Transfer{}
	for _, t := range c.transfers {
		if t.SiteSlug == rc.signer && t.Direction == "to-site" && (t.Status == "ready" || t.Status == "saved-on-server" || t.Status == "downloaded") {
			items = append(items, *t)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	writeJSON(w, 200, model.ListResponse[model.Transfer]{Items: items})
}

func (c *Cloud) createTransfer(w http.ResponseWriter, body []byte, rc reqCtx) {
	var req model.AgentCreateTransferRequest
	if !decode(w, body, &req) {
		return
	}
	if req.FileName == "" || len(req.FileName) > 255 || strings.ContainsAny(req.FileName, "/\\\x00") {
		writeErr(w, model.ErrInvalid, "bad fileName", nil)
		return
	}
	if req.SizeBytes < 0 {
		writeErr(w, model.ErrInvalid, "bad sizeBytes", nil)
		return
	}
	if req.SizeBytes > model.TransferMax {
		writeErr(w, model.ErrTooLarge, "at most 2 GiB", nil)
		return
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(req.Sha256) {
		writeErr(w, model.ErrInvalid, "bad sha256", nil)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tid, uid := randID(), randID()
	now := nowTs()
	t := &model.Transfer{TransferID: tid, DeploymentID: c.DeploymentID, SiteSlug: rc.signer, Direction: "to-cloud", FileName: req.FileName, SizeBytes: req.SizeBytes, Sha256: req.Sha256, StoragePath: "transfers/" + c.DeploymentID + "/" + tid, UploadID: &uid, CreatedAt: now, CreatedBy: req.Actor, Note: req.Note, Status: "uploading"}
	count := int((req.SizeBytes + model.UploadPartSize - 1) / model.UploadPartSize)
	if count < 1 {
		count = 1
	}
	dep := c.DeploymentID
	up := &upload{creator: rc.signer, parts: map[int][]byte{}, up: model.Upload{UploadID: uid, Purpose: "transfer", DeploymentID: &dep, TransferID: &tid, SizeBytes: req.SizeBytes, Sha256: req.Sha256, PartSize: model.UploadPartSize, PartCount: count, PartsReceived: []int{}, Status: "open", CreatedAt: now, CreatedBy: rc.signer, ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(tsLayout)}}
	c.transfers[tid] = t
	c.uploads[uid] = up
	writeJSON(w, 201, model.CreateTransferResponse{Transfer: *t, Upload: up.up})
}

func (c *Cloud) findUpload(w http.ResponseWriter, id string, rc reqCtx) *upload {
	up, ok := c.uploads[id]
	if !ok || up.creator != rc.signer {
		writeErr(w, model.ErrNotFound, "upload "+id+" not found", nil)
		return nil
	}
	if up.up.Status == "failed" {
		writeErr(w, model.ErrGone, "upload failed", nil)
		return nil
	}
	return up
}

func (c *Cloud) getUpload(w http.ResponseWriter, id string, rc reqCtx) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if up := c.findUpload(w, id, rc); up != nil {
		writeJSON(w, 200, up.up)
	}
}

func (c *Cloud) putPart(w http.ResponseWriter, id, idxS string, body []byte, rc reqCtx) {
	c.mu.Lock()
	defer c.mu.Unlock()
	up := c.findUpload(w, id, rc)
	if up == nil {
		return
	}
	idx, _ := strconv.Atoi(idxS)
	if idx < 0 || idx >= up.up.PartCount {
		writeErr(w, model.ErrInvalid, "part index out of range", nil)
		return
	}
	want := up.up.PartSize
	if idx == up.up.PartCount-1 {
		want = up.up.SizeBytes - up.up.PartSize*int64(up.up.PartCount-1)
	}
	if int64(len(body)) != want {
		writeErr(w, model.ErrInvalid, fmt.Sprintf("part %d must be %d bytes, got %d", idx, want, len(body)), nil)
		return
	}
	up.parts[idx] = append([]byte(nil), body...)
	up.up.PartsReceived = sortedKeys(up.parts)
	writeJSON(w, 200, model.UploadPartResponse{Received: up.up.PartsReceived})
}

func sortedKeys(m map[int][]byte) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func (c *Cloud) completeUpload(w http.ResponseWriter, id string, rc reqCtx) {
	c.mu.Lock()
	defer c.mu.Unlock()
	up := c.findUpload(w, id, rc)
	if up == nil {
		return
	}
	var missing []any
	var buf bytes.Buffer
	for i := 0; i < up.up.PartCount; i++ {
		p, ok := up.parts[i]
		if !ok {
			missing = append(missing, float64(i))
		}
		buf.Write(p)
	}
	if len(missing) > 0 {
		writeErr(w, model.ErrInvalid, "parts missing", map[string]any{"missing": missing})
		return
	}
	data := buf.Bytes()
	if int64(len(data)) != up.up.SizeBytes || "sha256:"+sha256Hex(data) != up.up.Sha256 {
		up.up.Status = "failed"
		msg := "size or sha256 mismatch"
		up.up.Error = &msg
		writeErr(w, model.ErrInvalid, msg, nil)
		return
	}
	up.up.Status = "complete"
	resp := model.CompleteUploadResponse{Upload: up.up}
	if up.up.Purpose == "file" && up.up.TransferID != nil { // §21 #123
		c.completeFileUpload(*up.up.TransferID, data)
		writeJSON(w, 200, resp)
		return
	}
	if up.up.TransferID != nil {
		if t, ok := c.transfers[*up.up.TransferID]; ok {
			t.Status = "ready"
			c.transferDat[t.TransferID] = append([]byte(nil), data...)
			tc := *t
			resp.Transfer = &tc
			c.cloudActivity("transfer.sent", t.FileName, map[string]any{"transferId": t.TransferID, "direction": "to-cloud"})
		}
	}
	writeJSON(w, 200, resp)
}

func (c *Cloud) transferStatus(w http.ResponseWriter, id string, body []byte) {
	var req model.AgentTransferStatusRequest
	if !decode(w, body, &req) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.transfers[id]
	if !ok {
		writeErr(w, model.ErrNotFound, "transfer "+id+" not found", nil)
		return
	}
	switch req.Status {
	case "saved-on-server", "downloaded", "ready", "fetched":
	default:
		writeErr(w, model.ErrInvalid, "bad status", nil)
		return
	}
	if req.Status == "fetched" || req.Status == "saved-on-server" || req.Status == "downloaded" {
		if t.Direction != "to-site" {
			writeErr(w, model.ErrInvalid, req.Status+" is reported only for to-site transfers", nil)
			return
		}
		if t.FetchedAt == nil {
			now := nowTs()
			t.FetchedAt = &now
		}
	}
	if req.Status == "fetched" { // §21 #100: no status change, no activity
		writeJSON(w, 200, model.OkResponse{Ok: true})
		return
	}
	t.Status = req.Status
	if req.SavedPath != "" {
		p := req.SavedPath
		t.SavedPath = &p
	}
	typ := "transfer.received"
	if req.Status == "saved-on-server" {
		typ = "transfer.saved"
	}
	c.cloudActivity(typ, t.FileName, map[string]any{"transferId": id, "direction": t.Direction})
	writeJSON(w, 200, model.OkResponse{Ok: true})
}

func (c *Cloud) transferDownload(w http.ResponseWriter, r *http.Request, id string, rc reqCtx) {
	c.mu.Lock()
	t, ok := c.transfers[id]
	var data []byte
	var status string
	if ok {
		data, status = c.transferDat[id], t.Status
		ok = t.SiteSlug == rc.signer && t.Direction == "to-site"
	}
	c.mu.Unlock()
	if !ok {
		writeErr(w, model.ErrNotFound, "transfer "+id+" not found", nil)
		return
	}
	if status == "expired" {
		writeErr(w, model.ErrGone, "transfer expired", nil)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// ───────────────────────── relay

func (c *Cloud) relay(w http.ResponseWriter, r *http.Request, body []byte, rc reqCtx) {
	var req model.RelayRequest
	if !decode(w, body, &req) {
		return
	}
	var inner []byte
	if req.Request.BodyB64 != nil {
		b, err := base64.StdEncoding.DecodeString(*req.Request.BodyB64)
		if err != nil {
			writeErr(w, model.ErrInvalid, "bodyB64 is not base64", nil)
			return
		}
		inner = b
	}
	if !strings.HasPrefix(req.Request.Path, "/v1/agent/") || req.Request.Path == "/v1/agent/relay" {
		writeErr(w, model.ErrInvalid, "inner path must be an agent endpoint", nil)
		return
	}
	rec := httptest.NewRecorder()
	setCommon(rec)
	slug, reason, msg := c.verify(req.Request.Method, req.Request.Path, req.Request.Headers, inner)
	if reason == "" && slug != req.ForSite {
		reason, msg = "unknown-site", "inner signer is not forSite"
	}
	if reason != "" {
		writeErr(rec, model.ErrUnauthenticated, msg, map[string]any{"reason": reason})
	} else {
		ir := httptest.NewRequest(req.Request.Method, req.Request.Path, bytes.NewReader(inner)).WithContext(r.Context())
		for k, v := range req.Request.Headers {
			ir.Header.Set(k, v)
		}
		c.mu.Lock()
		c.requests = append(c.requests, ir.URL.Path)
		c.mu.Unlock()
		c.route(rec, ir, inner, reqCtx{signer: slug, relayVia: rc.signer})
	}
	hdr := map[string]string{}
	for k := range rec.Header() {
		hdr[k] = rec.Header().Get(k)
	}
	b64 := base64.StdEncoding.EncodeToString(rec.Body.Bytes())
	writeJSON(w, 200, model.RelayResponse{Status: rec.Code, Headers: hdr, BodyB64: &b64})
}

// addrs copies detectedAddresses (nil → [] for old agents).
func addrs(l []string) []string {
	out := []string{}
	return append(out, l...)
}
