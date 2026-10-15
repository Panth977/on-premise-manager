package agent

import (
	"archive/tar"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/store"
)

// ───────────────────────── part tar (peer GET /_opm/peer/snapshots/{id}/part)

// writePartTar writes sites/<self>/… entries (+ images/blobs/sha256/… with images).
func (a *Agent) writePartTar(w io.Writer, id string, includeImages bool) error {
	p, err := a.loadPart(id)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	if err := tarFile(tw, filepath.Join(p.dir, "sites", a.site, "config.json"), "sites/"+a.site+"/config.json"); err != nil {
		return err
	}
	for _, v := range p.site.Volumes {
		if err := tarFile(tw, filepath.Join(p.dir, filepath.FromSlash(v.Path)), v.Path); err != nil {
			return err
		}
	}
	if includeImages {
		for _, h := range a.partImageBlobs(p) {
			if bp, ok := a.blobPath(h); ok {
				if err := tarFile(tw, bp, "images/blobs/sha256/"+h); err != nil {
					return err
				}
			}
		}
	}
	return tw.Close()
}

func (a *Agent) partImageBlobs(p *partInfo) []string {
	var out []string
	seen := map[string]bool{}
	var cat *model.RevisionCatalog
	if p.snap.Revision != nil {
		cat = &p.snap.Revision.Catalog
	}
	for _, im := range p.site.Images {
		hs := []string{hexOf(im.Digest), hexOf(im.ConfigDigest)}
		if cat != nil {
			if spec, ok := cat.Images[im.ServiceSlug+":"+im.Version]; ok {
				for _, l := range spec.Layers {
					hs = append(hs, hexOf(l.Digest))
				}
			}
		}
		for _, h := range hs {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	return out
}

func tarFile(tw *tar.Writer, src, name string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime(), Format: tar.FormatUSTAR}); err != nil {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime()}); err != nil {
			return err
		}
	}
	_, err = io.Copy(tw, f)
	return err
}

func tarBytes(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Now(), Format: tar.FormatUSTAR}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func (a *Agent) servePeerSnapshotPart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("snapshotId")
	if !a.hasSnapshotPart(id) {
		writeErr(w, herr(model.ErrNotFound, "no complete part on this site", nil))
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	if err := a.writePartTar(w, id, qBool(r, "includeImages")); err != nil {
		a.log.Error("snapshot", "part stream failed", "snapshotId", id, "error", err.Error())
	}
}

// ───────────────────────── .opmsnap download (§13.1)

func (a *Agent) handleDownloadSnapshot(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	id := r.PathValue("snapshotId")
	incl := qBool(r, "includeImages")
	sum, per := a.findSnapshot(r.Context(), id)
	if sum == nil {
		return nil, 0, herr(model.ErrNotFound, "snapshot not found", nil)
	}
	if len(sum.MissingSites) > 0 {
		return nil, 0, herr(model.ErrUnavailable, "parts missing on: "+strings.Join(sum.MissingSites, ", "), nil)
	}
	tmp, err := os.MkdirTemp(filepath.Join(a.dataDir, "tmp"), "opmsnap-")
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	defer os.RemoveAll(tmp)
	// collect every site's part tar into tmp
	for _, site := range sum.Sites {
		pt := filepath.Join(tmp, "part-"+site+".tar")
		if site == a.site {
			f, err := os.Create(pt)
			if err != nil {
				return nil, 0, herr(model.ErrInternal, err.Error(), nil)
			}
			err = a.writePartTar(f, id, incl)
			f.Close()
			if err != nil {
				return nil, 0, herr(model.ErrInternal, err.Error(), nil)
			}
			continue
		}
		if !a.reachable(site) {
			return nil, 0, herr(model.ErrUnavailable, "site "+site+" is not reachable", nil)
		}
		q := ""
		if incl {
			q = "?includeImages=1"
		}
		resp, done, err := a.peerStream(r.Context(), site, "GET", "/_opm/peer/snapshots/"+id+"/part"+q, nil, nil, 60*time.Minute)
		if err != nil || resp.StatusCode != 200 {
			if done != nil {
				done()
			}
			return nil, 0, herr(model.ErrUnavailable, "site "+site+": part not available", nil)
		}
		f, _ := os.Create(pt)
		_, err = io.Copy(f, resp.Body)
		f.Close()
		done()
		if err != nil {
			return nil, 0, herr(model.ErrUnavailable, "site "+site+": "+err.Error(), nil)
		}
	}
	_ = per
	// unpack into tmp/content and build the manifest
	content := filepath.Join(tmp, "content")
	var names []string
	images := map[string]bool{}
	for _, site := range sum.Sites {
		ns, err := untarTo(filepath.Join(tmp, "part-"+site+".tar"), content)
		if err != nil {
			return nil, 0, herr(model.ErrInternal, err.Error(), nil)
		}
		for _, n := range ns {
			if strings.HasPrefix(n, "images/") {
				if images[n] {
					continue
				}
				images[n] = true
			}
			names = append(names, n)
		}
	}
	snap := a.mergedSnapshotDoc(r.Context(), id)
	snap.Note, snap.CreatedBy = sum.Note, sum.CreatedBy
	for _, site := range sum.Sites {
		var part model.SnapshotSite
		raw, err := os.ReadFile(filepath.Join(content, "sites", site, "config.json"))
		if err == nil && json.Unmarshal(raw, &part) == nil {
			snap.Sites[site] = part
		}
	}
	sortParts(names)
	parts := []model.BundlePart{}
	for _, n := range names {
		p := filepath.Join(content, filepath.FromSlash(n))
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		sum, _ := fileSha256(p)
		parts = append(parts, model.BundlePart{Path: n, SizeBytes: st.Size(), Sha256: "sha256:" + sum})
	}
	imgs := []model.SnapImageBlobs{}
	if snap.Revision != nil {
		seen := map[string]bool{}
		for _, site := range snap.Sites {
			for _, im := range site.Images {
				k := im.ServiceSlug + ":" + im.Version
				if seen[k] {
					continue
				}
				seen[k] = true
				b := []string{im.Digest, im.ConfigDigest}
				if spec, ok := snap.Revision.Catalog.Images[k]; ok {
					for _, l := range spec.Layers {
						b = append(b, l.Digest)
					}
				}
				imgs = append(imgs, model.SnapImageBlobs{ServiceSlug: im.ServiceSlug, Version: im.Version, Digest: im.Digest, ConfigDigest: im.ConfigDigest, Blobs: b})
			}
		}
	}
	man := model.OpmsnapManifest{Format: "opmsnap/1", DeploymentID: a.dep, Snapshot: snap, Parts: parts, IncludeImages: incl, Images: imgs,
		CreatedAt: nowTs(), CreatedBy: rc.actor, SignedBy: model.SignedBy{SiteSlug: a.site, PublicKey: sign.PublicKeyB64(a.pub)}}
	mj, err := canonical.Canonicalize(toAny(man))
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(a.key, []byte(mj)))
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", a.dep+"-"+id+".opmsnap"))
	w.Header().Set("X-OPM-Warning", "contains-secrets")
	tw := tar.NewWriter(w)
	_ = tarBytes(tw, "manifest.json", []byte(mj))
	for _, p := range parts {
		if err := tarFile(tw, filepath.Join(content, filepath.FromSlash(p.Path)), p.Path); err != nil {
			a.log.Error("snapshot", "download stream failed", "error", err.Error())
			return nil, 0, nil
		}
	}
	_ = tarBytes(tw, "signature.sig", []byte(sig))
	_ = tw.Close()
	a.activity(rc.actorPtr(), "agent", "snapshot.downloaded", "snapshot "+id+" downloaded", actorData(rc, map[string]any{"snapshotId": id, "includeImages": incl}))
	return nil, 0, nil
}

// sortParts: sites/<slug>/config.json, then volumes, then images.
func sortParts(names []string) {
	rank := func(n string) int {
		switch {
		case strings.HasSuffix(n, "/config.json"):
			return 0
		case strings.HasPrefix(n, "sites/"):
			return 1
		}
		return 2
	}
	sort.SliceStable(names, func(i, j int) bool {
		if rank(names[i]) != rank(names[j]) {
			return rank(names[i]) < rank(names[j])
		}
		return names[i] < names[j]
	})
}

func untarTo(src, dir string) ([]string, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return untarReader(f, dir, nil)
}

// untarReader extracts regular files (safe paths only); returns names in order. hook (optional) sees each name first.
func untarReader(r io.Reader, dir string, hook func(name string, hdr *tar.Header, tr *tar.Reader) (bool, error)) ([]string, error) {
	tr := tar.NewReader(r)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return names, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+hdr.Name)), "/")
		if name != hdr.Name || strings.Contains(name, "..") {
			return names, fmt.Errorf("unsafe entry %q", hdr.Name)
		}
		if hook != nil {
			handled, err := hook(name, hdr, tr)
			if err != nil {
				return names, err
			}
			if handled {
				names = append(names, name)
				continue
			}
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return names, err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return names, err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return names, err
		}
		names = append(names, name)
	}
}

// ───────────────────────── import (§13.1)

type importedSnap struct {
	man     model.OpmsnapManifest
	manRaw  []byte
	sig     string
	content string
}

// readOpmsnap parses + verifies an .opmsnap file (signature, every part, no extra entries).
func (a *Agent) readOpmsnap(path, workDir string) (*importedSnap, *httpError) {
	f, err := os.Open(path)
	if err != nil {
		return nil, herr(model.ErrInternal, err.Error(), nil)
	}
	defer f.Close()
	content := filepath.Join(workDir, "content")
	im := &importedSnap{content: content}
	first := true
	names, err := untarReader(f, content, func(name string, hdr *tar.Header, tr *tar.Reader) (bool, error) {
		if first {
			first = false
			if name != "manifest.json" {
				return false, fmt.Errorf("the first entry must be manifest.json")
			}
			b, err := io.ReadAll(io.LimitReader(tr, 64<<20))
			if err != nil {
				return false, err
			}
			im.manRaw = b
			return true, json.Unmarshal(b, &im.man)
		}
		if name == "signature.sig" {
			b, err := io.ReadAll(io.LimitReader(tr, 1024))
			im.sig = strings.TrimSpace(string(b))
			return true, err
		}
		return false, nil
	})
	if err != nil {
		return nil, invalid("not an .opmsnap file: "+err.Error(), nil)
	}
	if im.man.Format != "opmsnap/1" {
		return nil, invalid("format must be opmsnap/1", nil)
	}
	if im.man.DeploymentID != a.dep {
		return nil, invalid("the file is for deployment "+im.man.DeploymentID, nil)
	}
	// signer must be an enrolled site of this deployment with that key
	row, ok := a.peerRow(im.man.SignedBy.SiteSlug)
	keyOK := ok && row.PublicKey == im.man.SignedBy.PublicKey
	if !keyOK && im.man.SignedBy.SiteSlug == a.site && im.man.SignedBy.PublicKey == sign.PublicKeyB64(a.pub) {
		keyOK = true
	}
	if !keyOK {
		return nil, invalid("the signer "+im.man.SignedBy.SiteSlug+" is not an enrolled site of this deployment", nil)
	}
	pub, err := sign.ParsePublicKey(im.man.SignedBy.PublicKey)
	if err != nil || !verifyRaw(pub, string(im.manRaw), im.sig) {
		return nil, invalid("signature.sig does not match manifest.json", nil)
	}
	want := map[string]model.BundlePart{}
	for _, p := range im.man.Parts {
		want[p.Path] = p
	}
	seen := map[string]bool{}
	for _, n := range names {
		if n == "manifest.json" || n == "signature.sig" {
			continue
		}
		p, ok := want[n]
		if !ok {
			return nil, invalid("extra entry "+n, nil)
		}
		fp := filepath.Join(content, filepath.FromSlash(n))
		st, err := os.Stat(fp)
		if err != nil {
			return nil, invalid("entry "+n+" missing", nil)
		}
		sum, _ := fileSha256(fp)
		if st.Size() != p.SizeBytes || "sha256:"+sum != p.Sha256 {
			return nil, invalid("entry "+n+" does not match the manifest (size / sha256)", nil)
		}
		seen[n] = true
	}
	for n := range want {
		if !seen[n] {
			return nil, invalid("part "+n+" is missing", nil)
		}
	}
	if im.sig == "" {
		return nil, invalid("signature.sig is missing", nil)
	}
	return im, nil
}

func (a *Agent) imageKnown(digest string, inFile map[string]bool) bool {
	h := hexOf(digest)
	if inFile[h] || a.haveBlob(h) {
		return true
	}
	rows, err := a.db.Query(`SELECT catalog FROM revisions`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) == nil && strings.Contains(raw, digest) {
			return true
		}
	}
	return false
}

func (a *Agent) handleImportSnapshot(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	work, err := os.MkdirTemp(filepath.Join(a.dataDir, "tmp"), "import-")
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	defer os.RemoveAll(work)
	file := filepath.Join(work, "in.opmsnap")
	f, err := os.Create(file)
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	var src io.Reader = r.Body
	if rcr, err := rc.reader(); err == nil && (rc.body != nil || rc.bodyFile != "") {
		defer rcr.Close()
		src = rcr
	}
	n, err := io.Copy(f, io.LimitReader(src, 8<<30+1))
	f.Close()
	if err != nil {
		return nil, 0, herr(model.ErrInvalid, err.Error(), nil)
	}
	if n > 8<<30 {
		return nil, 0, herr(model.ErrTooLarge, "at most 8 GiB", nil)
	}
	im, he := a.readOpmsnap(file, work)
	if he != nil {
		return nil, 0, he
	}
	id := im.man.Snapshot.SnapshotID
	if sum, _ := a.findSnapshot(r.Context(), id); sum != nil {
		return nil, 0, herr(model.ErrConflict, "snapshot "+id+" already exists", nil)
	}
	inFile := map[string]bool{}
	for _, p := range im.man.Parts {
		if strings.HasPrefix(p.Path, "images/blobs/sha256/") {
			inFile[strings.TrimPrefix(p.Path, "images/blobs/sha256/")] = true
		}
	}
	for _, img := range im.man.Images {
		for _, d := range append([]string{img.Digest, img.ConfigDigest}, img.Blobs...) {
			if !a.imageKnown(d, inFile) {
				return nil, 0, invalid("image "+img.ServiceSlug+":"+img.Version+" missing ("+short(d)+")", nil)
			}
		}
	}
	// image blobs → local store
	kinds := map[string]string{}
	for _, img := range im.man.Images {
		kinds[hexOf(img.Digest)] = "manifest"
		kinds[hexOf(img.ConfigDigest)] = "config"
	}
	for h := range inFile {
		if !a.haveBlob(h) {
			k := kinds[h]
			if k == "" {
				k = "layer"
			}
			tmp := filepath.Join(a.dataDir, "tmp", "imp-"+h)
			if err := copyFile(filepath.Join(im.content, "images", "blobs", "sha256", h), tmp); err == nil {
				_ = a.storeBlobFile(tmp, h, k, "local")
			}
		}
	}
	if im.man.Snapshot.Revision != nil {
		if err := verifyRevision(im.man.Snapshot.Revision); err == nil {
			_ = a.putRevision(im.man.Snapshot.Revision)
		}
	}
	// own part + other sites' parts
	for site := range im.man.Snapshot.Sites {
		if site == a.site {
			if e := a.storeImportedPart(im, site, rc.actor); e != nil {
				return nil, 0, e
			}
			continue
		}
		pt := filepath.Join(work, "part-"+site+".tar")
		if err := a.buildPartTarFromImport(im, site, pt); err != nil {
			return nil, 0, herr(model.ErrInternal, err.Error(), nil)
		}
		if err := a.pushPart(r.Context(), site, id, pt, rc.actor); err != nil {
			pend := filepath.Join(a.snapDir(id), "pending")
			_ = os.MkdirAll(pend, 0o700)
			_ = copyFile(pt, filepath.Join(pend, site+".tar"))
			_ = os.WriteFile(filepath.Join(pend, site+".by"), []byte(rc.actor), 0o600)
			a.log.Warn("snapshot", "part kept for a later push", "site", site, "error", err.Error())
		}
	}
	a.activity(rc.actorPtr(), "agent", "snapshot.imported", "snapshot "+id+" imported", actorData(rc, map[string]any{"snapshotId": id, "reason": "imported"}))
	sum, _ := a.findSnapshot(r.Context(), id)
	if sum == nil {
		s := model.SnapshotSummary{SnapshotID: id, CreatedAt: im.man.Snapshot.CreatedAt, Reason: "imported", RevID: im.man.Snapshot.RevID, Seq: im.man.Snapshot.Seq,
			CreatedBy: rc.actor, Note: im.man.Snapshot.Note, Sites: []string{}, MissingSites: []string{}}
		sum = &s
	}
	return sum, 201, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// storeImportedPart stores this site's part from a verified import.
func (a *Agent) storeImportedPart(im *importedSnap, site, actor string) *httpError {
	id := im.man.Snapshot.SnapshotID
	if a.hasSnapshotPart(id) {
		return herr(model.ErrConflict, "snapshot "+id+" already exists on "+a.site, nil)
	}
	part, ok := im.man.Snapshot.Sites[site]
	if !ok {
		return invalid("no part for "+site, nil)
	}
	dir := a.snapDir(id)
	_ = os.MkdirAll(filepath.Join(dir, "sites", site), 0o700)
	for _, v := range part.Volumes {
		if err := copyFile(filepath.Join(im.content, filepath.FromSlash(v.Path)), filepath.Join(dir, filepath.FromSlash(v.Path))); err != nil {
			return herr(model.ErrInternal, err.Error(), nil)
		}
	}
	snap := im.man.Snapshot
	snap.Sites = map[string]model.SnapshotSite{site: part}
	snap.Reason = "imported"
	snap.CreatedBy = actor
	if err := a.writePartFiles(id, &snap, &part); err != nil {
		return herr(model.ErrInternal, err.Error(), nil)
	}
	_, err := a.db.Exec(`INSERT INTO snapshots(snapshotId, createdAt, reason, revId, seq, manifest, sizeBytes, createdBy, note, complete) VALUES(?,?,?,?,?,?,?,?,?,1)`,
		id, snap.CreatedAt, "imported", store.NullStr(snap.RevID), snap.Seq, store.JSON(snap), part.SizeBytes, actor, snap.Note)
	if err != nil {
		return herr(model.ErrConflict, "snapshot "+id+" already exists", nil)
	}
	return nil
}

// buildPartTarFromImport writes manifest.json + the site's entries + signature.sig.
func (a *Agent) buildPartTarFromImport(im *importedSnap, site, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	if err := tarBytes(tw, "manifest.json", im.manRaw); err != nil {
		return err
	}
	for _, p := range im.man.Parts {
		if strings.HasPrefix(p.Path, "sites/"+site+"/") {
			if err := tarFile(tw, filepath.Join(im.content, filepath.FromSlash(p.Path)), p.Path); err != nil {
				return err
			}
		}
	}
	if err := tarBytes(tw, "signature.sig", []byte(im.sig)); err != nil {
		return err
	}
	return tw.Close()
}

func (a *Agent) pushPart(ctx context.Context, site, id, tarPath, actor string) error {
	if !a.reachable(site) {
		return fmt.Errorf("site %s is not reachable", site)
	}
	hdr := map[string]string{"Content-Type": "application/x-tar"}
	if actor != "" {
		hdr["X-OPM-Actor"] = actor
	}
	resp, done, err := a.peerUploadFile(ctx, site, "PUT", "/_opm/peer/snapshots/"+id+"/part", tarPath, hdr, 60*time.Minute)
	if err != nil {
		return err
	}
	defer done()
	if resp.StatusCode == 409 {
		return nil // already there
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("site %s: %d %s", site, resp.StatusCode, string(b))
	}
	return nil
}

// handlePutSnapshotPart: PUT /_opm/peer/snapshots/{id}/part (manifest first, verified).
func (a *Agent) handlePutSnapshotPart(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	rc := getRC(r)
	id := r.PathValue("snapshotId")
	if a.hasSnapshotPart(id) {
		return nil, herr(model.ErrConflict, "snapshot "+id+" already exists on "+a.site, nil)
	}
	work, err := os.MkdirTemp(filepath.Join(a.dataDir, "tmp"), "part-")
	if err != nil {
		return nil, herr(model.ErrInternal, err.Error(), nil)
	}
	defer os.RemoveAll(work)
	file := filepath.Join(work, "part.tar")
	rd, err := rc.reader()
	if err != nil {
		return nil, herr(model.ErrInternal, err.Error(), nil)
	}
	out, _ := os.Create(file)
	_, err = io.Copy(out, rd)
	rd.Close()
	out.Close()
	if err != nil {
		return nil, herr(model.ErrInvalid, err.Error(), nil)
	}
	// verify like an import but only for this site's parts
	f, _ := os.Open(file)
	content := filepath.Join(work, "content")
	im := &importedSnap{content: content}
	first := true
	names, err := untarReader(f, content, func(name string, hdr *tar.Header, tr *tar.Reader) (bool, error) {
		if first {
			first = false
			if name != "manifest.json" {
				return false, fmt.Errorf("the first entry must be manifest.json")
			}
			b, err := io.ReadAll(io.LimitReader(tr, 64<<20))
			im.manRaw = b
			if err != nil {
				return false, err
			}
			return true, json.Unmarshal(b, &im.man)
		}
		if name == "signature.sig" {
			b, err := io.ReadAll(io.LimitReader(tr, 1024))
			im.sig = strings.TrimSpace(string(b))
			return true, err
		}
		return false, nil
	})
	f.Close()
	if err != nil {
		return nil, invalid(err.Error(), nil)
	}
	if im.man.DeploymentID != a.dep || im.man.Snapshot.SnapshotID != id {
		return nil, invalid("the part is for another deployment / snapshot", nil)
	}
	pub, err := sign.ParsePublicKey(im.man.SignedBy.PublicKey)
	row, ok := a.peerRow(im.man.SignedBy.SiteSlug)
	if err != nil || !ok || row.PublicKey != im.man.SignedBy.PublicKey || !verifyRaw(pub, string(im.manRaw), im.sig) {
		return nil, invalid("manifest signature invalid", nil)
	}
	want := map[string]model.BundlePart{}
	for _, p := range im.man.Parts {
		if strings.HasPrefix(p.Path, "sites/"+a.site+"/") {
			want[p.Path] = p
		}
	}
	for _, n := range names {
		if n == "manifest.json" || n == "signature.sig" {
			continue
		}
		p, ok := want[n]
		if !ok {
			return nil, invalid("extra entry "+n, nil)
		}
		fp := filepath.Join(content, filepath.FromSlash(n))
		st, _ := os.Stat(fp)
		sum, _ := fileSha256(fp)
		if st == nil || st.Size() != p.SizeBytes || "sha256:"+sum != p.Sha256 {
			return nil, invalid("entry "+n+" does not match the manifest", nil)
		}
		delete(want, n)
	}
	if len(want) > 0 {
		return nil, invalid("entries missing", nil)
	}
	if im.man.Snapshot.Revision != nil && verifyRevision(im.man.Snapshot.Revision) == nil {
		_ = a.putRevision(im.man.Snapshot.Revision)
	}
	actor := rc.actor
	if e := a.storeImportedPart(im, a.site, actor); e != nil {
		return nil, e
	}
	a.activity(rc.actorPtr(), "agent", "snapshot.imported", "snapshot "+id+" part imported", map[string]any{"snapshotId": id, "reason": "imported"})
	return model.StoredResponse{Stored: true}, nil
}

// pushPendingParts retries parts kept for unreachable sites.
func (a *Agent) pushPendingParts(ctx context.Context) {
	dirs, _ := filepath.Glob(filepath.Join(a.dataDir, "snapshots", "*", "pending"))
	for _, d := range dirs {
		id := filepath.Base(filepath.Dir(d))
		files, _ := filepath.Glob(filepath.Join(d, "*.tar"))
		for _, f := range files {
			site := strings.TrimSuffix(filepath.Base(f), ".tar")
			by, _ := os.ReadFile(filepath.Join(d, site+".by"))
			if err := a.pushPart(ctx, site, id, f, string(by)); err == nil {
				_ = os.Remove(f)
				_ = os.Remove(filepath.Join(d, site+".by"))
				a.log.Info("snapshot", "pending part pushed", "snapshotId", id, "site", site)
			}
		}
		if rest, _ := filepath.Glob(filepath.Join(d, "*.tar")); len(rest) == 0 {
			_ = os.RemoveAll(d)
		}
	}
}
