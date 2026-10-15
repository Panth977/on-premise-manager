package fakecloud

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"time"

	"opm/agent/internal/model"
)

type fakeRelease struct {
	version, os, arch string
	bin               []byte
	sha, sig, pub     string
	notes             *string
}

// SetAgentRelease publishes the pinned agent release served by GET /v1/agent/release + /v1/agent/binary (§21 #86),
// signed with key (the release signing key).
func (c *Cloud) SetAgentRelease(version, goos, arch string, bin []byte, key ed25519.PrivateKey) {
	sum := sha256.Sum256(bin)
	sha := hex.EncodeToString(sum[:])
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(sha)))
	pub := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	c.mu.Lock()
	c.release = &fakeRelease{version: version, os: goos, arch: arch, bin: bin, sha: sha, sig: sig, pub: pub}
	c.epoch++
	c.mu.Unlock()
}

// SetAgentReleaseNotes sets the release notes (AgentReleaseResponse.notes, CloudView.agentRelease.notes).
func (c *Cloud) SetAgentReleaseNotes(notes string) {
	c.mu.Lock()
	if c.release != nil {
		c.release.notes = &notes
	}
	c.epoch++
	c.mu.Unlock()
}

// RequestAgentUpgrade queues a cloud "Upgrade agent" request for a site (§21 #90 f; CloudView.agentUpgrades).
func (c *Cloud) RequestAgentUpgrade(siteSlug, version, by string) model.AgentUpgradeRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	r := model.AgentUpgradeRequest{RequestID: c.newULID(), SiteSlug: siteSlug, Version: version, RequestedBy: by, RequestedByUID: "uid-" + by,
		RequestedAt: now.Format(tsLayout), ExpiresAt: now.Add(model.AgentUpgradeRequestTTLSec * time.Second).Format(tsLayout)}
	if a := c.agents[siteSlug]; a != nil {
		a.UpgradeRequest = &r
	}
	c.epoch++
	return r
}

// UpgradeReports returns every heartbeat `upgrade` a site sent (in order).
func (c *Cloud) UpgradeReports(siteSlug string) []model.AgentUpgradeProgress {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.AgentUpgradeProgress{}, c.upgradeReports[siteSlug]...)
}

// PendingUpgradeRequest returns the queued request of a site (nil once the heartbeat reported its end).
func (c *Cloud) PendingUpgradeRequest(siteSlug string) *model.AgentUpgradeRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a := c.agents[siteSlug]; a != nil && a.UpgradeRequest != nil {
		r := *a.UpgradeRequest
		return &r
	}
	return nil
}

// noteUpgrade stores a heartbeat `upgrade` and clears the request when it ended (c.mu held).
func (c *Cloud) noteUpgrade(a *model.AgentDoc, p *model.AgentUpgradeProgress, agentVersion string) {
	if p != nil {
		cp := *p
		a.Upgrade = &cp
		c.upgradeReports[a.SiteSlug] = append(c.upgradeReports[a.SiteSlug], cp)
	}
	if r := a.UpgradeRequest; r != nil {
		ended := p != nil && p.RequestID != nil && *p.RequestID == r.RequestID && (p.Phase == model.UpgradeDone || p.Phase == model.UpgradeFailed || p.Phase == model.UpgradeRolledBack)
		if ended || (agentVersion != "" && model.CompareAgentVersions(agentVersion, r.Version) >= 0) {
			a.UpgradeRequest = nil
			c.epoch++
		}
	}
}

func (c *Cloud) viewRelease() *model.AgentReleaseInfo {
	if c.release == nil {
		return nil
	}
	return &model.AgentReleaseInfo{Version: c.release.version, Notes: c.release.notes}
}

func (c *Cloud) viewUpgrades() []model.AgentUpgradeRequest {
	out := []model.AgentUpgradeRequest{}
	slugs := make([]string, 0, len(c.agents))
	for s := range c.agents {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, s := range slugs {
		if r := c.agents[s].UpgradeRequest; r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// SetConsole sets the consoleUrl + deploymentLabel the fake cloud sends in the enroll answer and the cloud view.
func (c *Cloud) SetConsole(consoleURL, label string) {
	c.mu.Lock()
	c.consoleURL, c.depLabel = &consoleURL, &label
	c.mu.Unlock()
}

func (c *Cloud) agentRelease(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c.mu.Lock()
	rel := c.release
	c.mu.Unlock()
	if rel == nil || q.Get("os") != rel.os || q.Get("arch") != rel.arch {
		writeErr(w, model.ErrNotFound, "no agent release for "+q.Get("os")+"/"+q.Get("arch"), nil)
		return
	}
	v := url.Values{"os": {rel.os}, "arch": {rel.arch}, "version": {rel.version}}
	size := int64(len(rel.bin))
	writeJSON(w, 200, model.AgentReleaseResponse{Version: rel.version, OS: rel.os, Arch: rel.arch, Sha256: rel.sha, Signature: rel.sig,
		ReleasePublicKey: rel.pub, BinaryPath: "/v1/agent/binary?" + v.Encode(), Notes: rel.notes, SizeBytes: &size})
}

func (c *Cloud) agentBinary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c.mu.Lock()
	rel := c.release
	c.mu.Unlock()
	if rel == nil || q.Get("os") != rel.os || q.Get("arch") != rel.arch || q.Get("version") != rel.version {
		writeErr(w, model.ErrNotFound, "agent binary not uploaded", nil)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-OPM-Sha256", rel.sha)
	w.Header().Set("X-OPM-Signature", rel.sig)
	w.Header().Set("X-OPM-Agent-Version", rel.version)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(rel.bin)) // Range-resumable (§21 #90)
}
