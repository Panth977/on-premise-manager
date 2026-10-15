package agent

import (
	"archive/tar"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

// StatusSummary is the offline status of the CLI (`opm-agent status`).
func (a *Agent) StatusSummary() map[string]any {
	hash, kv := a.keyHash()
	peers := []map[string]any{}
	for _, p := range a.peerRows() {
		peers = append(peers, map[string]any{"siteSlug": p.SiteSlug, "advertiseHost": p.AdvertiseHost, "agentPort": p.AgentPort, "lastAnswerAt": p.LastAnswerAt})
	}
	return map[string]any{"deploymentId": a.dep, "siteSlug": a.site, "enrolled": a.enrolled, "head": a.head(), "lock": a.lock(),
		"hasAccessKey": hash != "", "keyVersion": kv, "peers": peers, "openProposals": len(a.openProposals()), "lastSeq": a.lastSeq(), "ackSeq": a.kvInt("ackSeq")}
}

// LocalSnapshots lists this site's snapshot parts.
func (a *Agent) LocalSnapshots() []model.SnapshotSummary { return a.ownSnapshotSummaries() }

// ExportSnapshot writes an .opmsnap with this site's part only (offline export), signed by this site.
func (a *Agent) ExportSnapshot(w io.Writer, id string, includeImages bool) error {
	p, err := a.loadPart(id)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Join(a.dataDir, "tmp"), "export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	pt := filepath.Join(tmp, "part.tar")
	f, err := os.Create(pt)
	if err != nil {
		return err
	}
	if err := a.writePartTar(f, id, includeImages); err != nil {
		f.Close()
		return err
	}
	f.Close()
	content := filepath.Join(tmp, "content")
	names, err := untarTo(pt, content)
	if err != nil {
		return err
	}
	sortParts(names)
	parts := []model.BundlePart{}
	for _, n := range names {
		fp := filepath.Join(content, filepath.FromSlash(n))
		st, err := os.Stat(fp)
		if err != nil {
			return err
		}
		s, _ := fileSha256(fp)
		parts = append(parts, model.BundlePart{Path: n, SizeBytes: st.Size(), Sha256: "sha256:" + s})
	}
	snap := p.snap
	images := []model.SnapImageBlobs{}
	for _, im := range p.site.Images {
		images = append(images, model.SnapImageBlobs{ServiceSlug: im.ServiceSlug, Version: im.Version, Digest: im.Digest, ConfigDigest: im.ConfigDigest, Blobs: []string{im.Digest, im.ConfigDigest}})
	}
	man := model.OpmsnapManifest{Format: "opmsnap/1", DeploymentID: a.dep, Snapshot: snap, Parts: parts, IncludeImages: includeImages, Images: images,
		CreatedAt: nowTs(), CreatedBy: "cli", SignedBy: model.SignedBy{SiteSlug: a.site, PublicKey: sign.PublicKeyB64(a.pub)}}
	mj, err := canonical.Canonicalize(toAny(man))
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	if err := tarBytes(tw, "manifest.json", []byte(mj)); err != nil {
		return err
	}
	for _, pp := range parts {
		if err := tarFile(tw, filepath.Join(content, filepath.FromSlash(pp.Path)), pp.Path); err != nil {
			return err
		}
	}
	if err := tarBytes(tw, "signature.sig", []byte(base64.RawURLEncoding.EncodeToString(ed25519.Sign(a.key, []byte(mj))))); err != nil {
		return err
	}
	return tw.Close()
}

// ImportSnapshotFile verifies an .opmsnap and stores this site's part (offline import).
func (a *Agent) ImportSnapshotFile(path, actor string) error {
	work, err := os.MkdirTemp(filepath.Join(a.dataDir, "tmp"), "import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	im, he := a.readOpmsnap(path, work)
	if he != nil {
		return fmt.Errorf("%s: %s", he.code, he.msg)
	}
	if _, ok := im.man.Snapshot.Sites[a.site]; !ok {
		return fmt.Errorf("the file has no part for site %s", a.site)
	}
	if im.man.Snapshot.Revision != nil && verifyRevision(im.man.Snapshot.Revision) == nil {
		_ = a.putRevision(im.man.Snapshot.Revision)
	}
	if he := a.storeImportedPart(im, a.site, actor); he != nil {
		return fmt.Errorf("%s: %s", he.code, he.msg)
	}
	a.activity(&actor, "agent", "snapshot.imported", "snapshot imported (CLI)", map[string]any{"snapshotId": im.man.Snapshot.SnapshotID, "reason": "imported"})
	return nil
}
