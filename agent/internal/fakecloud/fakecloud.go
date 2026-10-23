// Package fakecloud is an in-memory fake of the OPM cloud AGENT endpoints (SPEC-final §4.2, §4.4, §4.5, §6, §6.1)
// used by the agent integration tests. It is not a production component.
package fakecloud

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

const tsLayout = "2006-01-02T15:04:05.000Z"

func nowTs() string { return time.Now().UTC().Format(tsLayout) }

type installToken struct {
	siteSlug  string
	expiresAt time.Time
	usedAt    *time.Time
}

type upload struct {
	up      model.Upload
	creator string
	parts   map[int][]byte
}

// Cloud is the fake cloud. Exported fields are read-only.
type Cloud struct {
	URL          string
	DeploymentID string

	srv     *httptest.Server
	closing chan struct{}

	mu      sync.Mutex
	release *fakeRelease // §21 #86 opm-agent upgrade
	// §21 #90 heartbeat `upgrade` reports per site
	upgradeReports map[string][]model.AgentUpgradeProgress
	// §21 #92 the raw agent clock of each site's last heartbeat (agentTime)
	agentTimes  map[string]string
	consoleURL  *string
	depLabel    *string
	epoch       int
	deleted     bool
	accessKey   model.AccessKeyRef
	tokens      map[string]*installToken // sha256 hex of token → token
	agents      map[string]*model.AgentDoc
	nonces      map[string]time.Time
	revisions   map[string]model.Revision
	proposals   map[string]*model.Proposal
	head        *model.HeadRef
	blobs       map[string][]byte // hex → bytes
	activities  map[string]map[int]model.Activity
	transfers   map[string]*model.Transfer
	transferDat map[string][]byte
	uploads     map[string]*upload
	filesSt     *fileState // §21 #123
	tunSt       *tunState  // §21 #125
	tunOnce     sync.Once
	blocked     map[string]bool
	requests    []string
	vc          map[string]*vcVol // §21 #101 volume content per "<site>:<volume>" (volcontent.go)
	br          *fakeBranch       // §21 #104 deployment branch + agent branch mirror (branch.go)

	ulidLast int64
	ulidRand [10]byte
	ulidMu   sync.Mutex
}

// Start starts the fake cloud on a local httptest server.
func Start(deploymentID string) *Cloud {
	c := &Cloud{
		DeploymentID:   deploymentID,
		closing:        make(chan struct{}),
		tokens:         map[string]*installToken{},
		agents:         map[string]*model.AgentDoc{},
		nonces:         map[string]time.Time{},
		revisions:      map[string]model.Revision{},
		proposals:      map[string]*model.Proposal{},
		blobs:          map[string][]byte{},
		activities:     map[string]map[int]model.Activity{},
		transfers:      map[string]*model.Transfer{},
		transferDat:    map[string][]byte{},
		uploads:        map[string]*upload{},
		blocked:        map[string]bool{},
		epoch:          1,
		upgradeReports: map[string][]model.AgentUpgradeProgress{},
		agentTimes:     map[string]string{},
	}
	c.srv = httptest.NewUnstartedServer(c)
	if listenAddr != "" {
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			panic("fakecloud: listen " + listenAddr + ": " + err.Error())
		}
		c.srv.Listener.Close()
		c.srv.Listener = ln
	}
	c.srv.Start()
	c.URL = c.srv.URL
	return c
}

// listenAddr: StartAt sets a fixed address (standalone cmd/opm-fakecloud); "" = a random local port.
var listenAddr string

// StartAt starts the fake cloud on a fixed address (e.g. 127.0.0.1:9999; the upgrade smoke test).
func StartAt(deploymentID, addr string) *Cloud {
	listenAddr = addr
	defer func() { listenAddr = "" }()
	return Start(deploymentID)
}

// Close stops the server (long polls are released first).
func (c *Cloud) Close() {
	select {
	case <-c.closing:
	default:
		close(c.closing)
	}
	c.srv.CloseClientConnections()
	c.srv.Close()
}

// ───────────────────────── helpers

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

const idChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randID() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = idChars[int(b[i])%len(idChars)]
	}
	return string(b)
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID returns a monotonic ULID (greater than every previous one of this cloud).
func (c *Cloud) newULID() string {
	c.ulidMu.Lock()
	defer c.ulidMu.Unlock()
	ms := time.Now().UnixMilli()
	if ms <= c.ulidLast {
		ms = c.ulidLast
		for i := 9; i >= 0; i-- {
			c.ulidRand[i]++
			if c.ulidRand[i] != 0 {
				break
			}
		}
	} else {
		_, _ = rand.Read(c.ulidRand[:])
		c.ulidRand[0] &= 0x7f // room to increment
	}
	c.ulidLast = ms
	var b [16]byte
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	copy(b[6:], c.ulidRand[:])
	// 128 bits → 26 chars (first char 3 bits)
	out := make([]byte, 26)
	var acc uint64
	bits := 0
	idx := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 {
			out[idx] = crockford[acc&31]
			idx--
			acc >>= 5
			bits -= 5
		}
	}
	for idx >= 0 {
		out[idx] = crockford[acc&31]
		acc >>= 5
		idx--
	}
	return string(out)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// NormalizeAccessKey upper-cases and keeps only A-Z2-7.
func NormalizeAccessKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hashAccessKey(plain string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(NormalizeAccessKey(plain)), salt, 3, 65536, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func isOpen(status string) bool { return !model.IsTerminal(status) }

func copyProposal(p *model.Proposal) model.Proposal {
	cp := *p
	cp.Sites = map[string]model.SiteStaging{}
	for k, v := range p.Sites {
		cp.Sites[k] = v
	}
	return cp
}

// cloudActivity appends to the `_cloud` log (hash chained). Caller holds mu.
func (c *Cloud) cloudActivity(typ, summary string, data map[string]any) {
	log := c.activities["_cloud"]
	if log == nil {
		log = map[int]model.Activity{}
		c.activities["_cloud"] = log
	}
	seq := len(log) + 1
	var prev *string
	if p, ok := log[seq-1]; ok {
		h := p.Hash
		prev = &h
	}
	if data == nil {
		data = map[string]any{}
	}
	a := model.Activity{ActivityID: "_cloud:" + strconv.Itoa(seq), DeploymentID: c.DeploymentID, SiteSlug: "_cloud", Seq: seq, Ts: nowTs(), Origin: "cloud", Type: typ, Summary: summary, Data: data, PrevHash: prev}
	h, _ := canonical.ActivityHash(activityFields(a))
	a.Hash = h
	log[seq] = a
}

func activityFields(a model.Activity) canonical.ActivityFields {
	var data any = a.Data
	if a.Data == nil {
		data = map[string]any{}
	}
	return canonical.ActivityFields{ActivityID: a.ActivityID, DeploymentID: a.DeploymentID, SiteSlug: a.SiteSlug, Seq: float64(a.Seq), Ts: a.Ts, Actor: a.Actor, Origin: a.Origin, Type: a.Type, Summary: a.Summary, Data: data, PrevHash: a.PrevHash}
}

func (c *Cloud) peers() []model.PeerInfo {
	out := []model.PeerInfo{}
	for _, a := range c.agents {
		out = append(out, model.PeerInfo{SiteSlug: a.SiteSlug, PublicKey: a.PublicKey, AdvertiseHost: a.AdvertiseHost, AgentPort: a.AgentPort, EnrolledAt: a.EnrolledAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SiteSlug < out[j].SiteSlug })
	return out
}

// ───────────────────────── exported API

// CreateInstallToken returns a fresh single-use 24 h install token for the site.
func (c *Cloud) CreateInstallToken(siteSlug string) string {
	tok := "opi_" + randB64(32)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[sha256Hex([]byte(tok))] = &installToken{siteSlug: siteSlug, expiresAt: time.Now().Add(24 * time.Hour)}
	c.cloudActivity("install-token.created", "install token for "+siteSlug, map[string]any{"siteSlug": siteSlug})
	return tok
}

// SetAccessKey hashes the normalised key (argon2id PHC), bumps the version and agentEpoch.
func (c *Cloud) SetAccessKey(plain string) model.AccessKeyRef {
	h := hashAccessKey(plain)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessKey = model.AccessKeyRef{Hash: h, Version: c.accessKey.Version + 1}
	c.epoch++
	c.cloudActivity("access-key.rotated", "access key rotated", map[string]any{"version": float64(c.accessKey.Version)})
	return c.accessKey
}

// PutBlob stores bytes content-addressed and returns the lowercase hex sha256.
func (c *Cloud) PutBlob(data []byte) string {
	h := sha256Hex(data)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blobs[h] = append([]byte(nil), data...)
	return h
}

// Publish creates a cloud revision + proposal (published) on top of the cloud head.
func (c *Cloud) Publish(cfg model.Config, cat model.RevisionCatalog, summary string) (model.Revision, model.Proposal) {
	revID := c.newULID()
	hash, _ := canonical.HashCanonical(cfg)
	catHash, _ := canonical.HashCanonical(cat)
	c.mu.Lock()
	defer c.mu.Unlock()
	var parent *string
	seq := 1
	sites := map[string]model.SiteStaging{}
	if c.head != nil {
		p := c.head.RevID
		parent = &p
		seq = c.head.Seq + 1
		if pr, ok := c.revisions[p]; ok {
			for _, s := range pr.Config.Sites {
				sites[s.SiteSlug] = model.SiteStaging{Staging: model.StagingPending}
			}
		}
	}
	for _, s := range cfg.Sites {
		sites[s.SiteSlug] = model.SiteStaging{Staging: model.StagingPending}
	}
	now := nowTs()
	rev := model.Revision{RevID: revID, Seq: seq, ParentRevID: parent, Origin: "cloud", Actor: "fake-uid", CreatedAt: now, Config: cfg, Hash: hash, Catalog: cat, CatalogHash: catHash, Summary: summary, Kind: "change"}
	c.revisions[revID] = rev
	prop := &model.Proposal{RevID: revID, ParentRevID: parent, Seq: seq, Summary: summary, Origin: "cloud", Kind: "change", CreatedAt: now, CreatedBy: "fake-uid", Status: model.ProposalPublished, Sites: sites, UpdatedAt: now}
	c.supersedeOlder(revID)
	c.proposals[revID] = prop
	c.epoch++
	c.cloudActivity("proposal.published", summary, map[string]any{"revId": revID, "seq": float64(seq), "summary": summary})
	return rev, copyProposal(prop)
}

// supersedeOlder marks open published|staging|ready proposals with a smaller revId superseded. Caller holds mu.
func (c *Cloud) supersedeOlder(revID string) {
	for id, p := range c.proposals {
		if id < revID && (p.Status == model.ProposalPublished || p.Status == model.ProposalStaging || p.Status == model.ProposalReady) {
			p.Status = model.ProposalSuperseded
			by := revID
			p.SupersededBy = &by
			p.UpdatedAt = nowTs()
			c.cloudActivity("proposal.superseded", "superseded", map[string]any{"revId": id, "byRevId": revID})
		}
	}
}

// Proposal returns one proposal.
func (c *Cloud) Proposal(revID string) (model.Proposal, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.proposals[revID]
	if !ok {
		return model.Proposal{}, false
	}
	return copyProposal(p), true
}

// Proposals returns every proposal, newest first.
func (c *Cloud) Proposals() []model.Proposal {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]model.Proposal, 0, len(c.proposals))
	for _, p := range c.proposals {
		out = append(out, copyProposal(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RevID > out[j].RevID })
	return out
}

// Revision returns one stored revision.
func (c *Cloud) Revision(revID string) (model.Revision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.revisions[revID]
	return r, ok
}

// Head returns the cloud's deployment head (nil = none).
func (c *Cloud) Head() *model.HeadRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.head == nil {
		return nil
	}
	h := *c.head
	return &h
}

// Agent returns the enrollment document of a site.
func (c *Cloud) Agent(siteSlug string) (model.AgentDoc, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.agents[siteSlug]
	if !ok {
		return model.AgentDoc{}, false
	}
	return *a, true
}

// Activities returns the stored activities of a site ("_cloud" = the cloud log), by seq asc.
func (c *Cloud) Activities(siteSlug string) []model.Activity {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []model.Activity{}
	for _, a := range c.activities[siteSlug] {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// AddTransferToSite creates a ready cloud → site transfer.
func (c *Cloud) AddTransferToSite(siteSlug, fileName string, data []byte, note string) model.Transfer {
	id := randID()
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &model.Transfer{TransferID: id, DeploymentID: c.DeploymentID, SiteSlug: siteSlug, Direction: "to-site", FileName: fileName, SizeBytes: int64(len(data)), Sha256: "sha256:" + sha256Hex(data), StoragePath: "transfers/" + c.DeploymentID + "/" + id, CreatedAt: nowTs(), CreatedBy: "fake-uid", Note: note, Status: "ready"}
	c.transfers[id] = t
	c.transferDat[id] = append([]byte(nil), data...)
	c.epoch++
	c.cloudActivity("transfer.sent", fileName, map[string]any{"transferId": id, "direction": "to-site"})
	return *t
}

// DeleteTransfer deletes a transfer as the cloud's Delete does (status expired, bytes removed, agents notified).
func (c *Cloud) DeleteTransfer(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.transfers[id]; ok {
		t.Status = "expired"
		delete(c.transferDat, id)
		c.epoch++
	}
}

// Transfer returns a transfer and its bytes (nil until uploaded).
func (c *Cloud) Transfer(id string) (model.Transfer, []byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.transfers[id]
	if !ok {
		return model.Transfer{}, nil, false
	}
	return *t, c.transferDat[id], true
}

// Transfers returns every transfer, newest first.
func (c *Cloud) Transfers() []model.Transfer {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []model.Transfer{}
	for _, t := range c.transfers {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// SetDeleted marks the deployment deleted (agents see it in the cloud view).
func (c *Cloud) SetDeleted(d bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = d
	c.epoch++
}

// BlockSite makes direct requests signed by the site fail (connection closed).
func (c *Cloud) BlockSite(siteSlug string, blocked bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocked[siteSlug] = blocked
}

// Requests counts handled requests whose path starts with prefix (relayed inner requests included).
func (c *Cloud) Requests(pathPrefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, p := range c.requests {
		if strings.HasPrefix(p, pathPrefix) {
			n++
		}
	}
	return n
}

// RevokeAgent deletes the enrollment of a site.
func (c *Cloud) RevokeAgent(siteSlug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.agents, siteSlug)
	c.epoch++
	c.cloudActivity("agent.revoked", siteSlug, map[string]any{"siteSlug": siteSlug})
}

// recompute applies the derived proposal status rules of §14.2. Caller holds mu.
func recompute(p *model.Proposal) {
	anyNonPending, allReady := false, len(p.Sites) > 0
	for _, s := range p.Sites {
		if s.Staging != model.StagingPending {
			anyNonPending = true
		}
		if s.Staging != model.StagingReady {
			allReady = false
		}
	}
	switch p.Status {
	case model.ProposalPublished:
		if anyNonPending {
			p.Status = model.ProposalStaging
			if allReady {
				p.Status = model.ProposalReady
			}
		}
	case model.ProposalStaging:
		if allReady {
			p.Status = model.ProposalReady
		}
	case model.ProposalReady:
		if !allReady {
			p.Status = model.ProposalStaging
		}
	}
}

// LastAgentTime is the `agentTime` of the site's last heartbeat ("" = none / not sent) — §21 #92.
func (c *Cloud) LastAgentTime(siteSlug string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agentTimes[siteSlug]
}
