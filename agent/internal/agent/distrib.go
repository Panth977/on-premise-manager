package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
)

type blobNeed struct {
	Hex  string
	Size int64
	Kind string // layer | config | manifest | file
	Ref  model.FetchRef
}

type fetchJob struct {
	mu     sync.Mutex
	done   chan struct{}
	err    error
	status string // queued | downloading | have | failed
	source string
}

func (a *Agent) blobFile(hexs, kind string) string {
	if kind == "file" {
		return driver.FileBlobPath(a.dataDir, hexs) // blobs/files/<hex> (round D part 2; files/ holds the per-site copies)
	}
	return filepath.Join(a.dataDir, "blobs", "sha256", hexs)
}

// blobPath returns the stored path of a blob (sha256 check already passed).
func (a *Agent) blobPath(hexs string) (string, bool) {
	var p string
	if err := a.db.QueryRow(`SELECT path FROM blobs WHERE sha256 = ?`, hexs).Scan(&p); err != nil {
		return "", false
	}
	if _, err := os.Stat(p); err != nil {
		_, _ = a.db.Exec(`DELETE FROM blobs WHERE sha256 = ?`, hexs)
		return "", false
	}
	return p, true
}

func (a *Agent) haveBlob(hexs string) bool {
	_, ok := a.blobPath(hexs)
	return ok
}

func (a *Agent) blobSource(hexs string) *string {
	var s string
	if err := a.db.QueryRow(`SELECT source FROM blobs WHERE sha256 = ?`, hexs).Scan(&s); err != nil {
		return nil
	}
	return &s
}

// storeBlobFile verifies tmp against hexs and moves it into place.
func (a *Agent) storeBlobFile(tmp, hexs, kind, source string) error {
	got, err := fileSha256(tmp)
	if err != nil {
		return err
	}
	if got != hexs {
		_ = os.Remove(tmp)
		return fmt.Errorf("blob %s: sha256 mismatch (got %s)", short(hexs), short(got))
	}
	st, err := os.Stat(tmp)
	if err != nil {
		return err
	}
	dst := a.blobFile(hexs, kind)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	_ = os.Chmod(dst, 0o644)
	now := time.Now().Unix()
	_, err = a.db.Exec(`INSERT INTO blobs(sha256, sizeBytes, kind, source, haveSince, path, lastUsedAt) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(sha256) DO UPDATE SET path = excluded.path, lastUsedAt = excluded.lastUsedAt`, hexs, st.Size(), kind, source, now, dst, now)
	a.mu.Lock()
	a.haveCache = map[string]*haveEntry{} // peers' caches are stale relative to us; reset ours too (cheap)
	a.mu.Unlock()
	return err
}

// storeBlobBytes stores in-memory content (local uploads).
func (a *Agent) storeBlobBytes(data []byte, kind, source string) (string, error) {
	sum := sha256.Sum256(data)
	hexs := hex.EncodeToString(sum[:])
	if a.haveBlob(hexs) {
		return hexs, nil
	}
	tmp := filepath.Join(a.dataDir, "tmp", "blob-"+newID())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	return hexs, a.storeBlobFile(tmp, hexs, kind, source)
}

func short(h string) string {
	h = strings.TrimPrefix(h, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func (a *Agent) haveList() model.HaveList {
	rows, err := a.db.Query(`SELECT sha256 FROM blobs ORDER BY sha256`)
	hl := model.HaveList{SiteSlug: a.site, GeneratedAt: nowTs(), Internet: a.hasInternet() || a.tun.active() != nil, Blobs: []string{}}
	if err != nil {
		return hl
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			hl.Blobs = append(hl.Blobs, "sha256:"+h)
		}
	}
	return hl
}

func (a *Agent) peerHave(ctx context.Context, slug string, fresh bool) map[string]bool {
	a.mu.Lock()
	e := a.haveCache[slug]
	a.mu.Unlock()
	if e != nil && !fresh && time.Since(e.at) < a.tm.HaveCache {
		return e.have
	}
	var hl model.HaveList
	if err := a.peerJSON(ctx, slug, "GET", "/_opm/peer/have", nil, &hl, 15*time.Second); err != nil {
		return nil
	}
	m := map[string]bool{}
	for _, b := range hl.Blobs {
		m[strings.TrimPrefix(b, "sha256:")] = true
	}
	a.mu.Lock()
	a.haveCache[slug] = &haveEntry{at: time.Now(), have: m, list: &hl}
	a.mu.Unlock()
	return m
}

// fetcherSet: reachable sites (incl. self) with internet (or holding the browser tunnel), ordered by rendezvous score.
func (a *Agent) fetcherSet(hexs string, exclude map[string]bool) []string {
	var cands []string
	if (a.hasInternet() || a.tun.active() != nil) && !exclude[a.site] {
		cands = append(cands, a.site)
	}
	for _, slug := range a.reachablePeers() {
		if exclude[slug] {
			continue
		}
		pw := a.lastPing(slug)
		if pw != nil && (pw.Internet || (pw.Tunnel != nil && pw.Tunnel.Status == "active")) {
			cands = append(cands, slug)
		}
	}
	score := func(s string) string {
		h := sha256.Sum256([]byte(hexs + ":" + s))
		return hex.EncodeToString(h[:])
	}
	sort.Slice(cands, func(i, j int) bool { return score(cands[i]) > score(cands[j]) })
	return cands
}

// ensureBlob makes a blob present locally (§9.7). state reports downloading / waiting.
func (a *Agent) ensureBlob(ctx context.Context, need blobNeed, state func(string, string)) error {
	if a.haveBlob(need.Hex) {
		_, _ = a.db.Exec(`UPDATE blobs SET lastUsedAt = ? WHERE sha256 = ?`, time.Now().Unix(), need.Hex)
		return nil
	}
	a.mu.Lock()
	job := a.fetching[need.Hex]
	owner := job == nil
	if owner {
		job = &fetchJob{done: make(chan struct{}), status: "queued"}
		a.fetching[need.Hex] = job
	}
	a.mu.Unlock()
	if !owner {
		select {
		case <-job.done:
			if a.haveBlob(need.Hex) {
				return nil
			}
			return job.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := a.fetchBlob(ctx, need, job, state)
	job.mu.Lock()
	job.err = err
	if err == nil {
		job.status = "have"
	} else {
		job.status = "failed"
	}
	job.mu.Unlock()
	close(job.done)
	a.mu.Lock()
	delete(a.fetching, need.Hex)
	a.mu.Unlock()
	return err
}

func (a *Agent) setJob(job *fetchJob, st string) {
	job.mu.Lock()
	job.status = st
	job.mu.Unlock()
}

func (a *Agent) fetchBlob(ctx context.Context, need blobNeed, job *fetchJob, state func(string, string)) error {
	if state == nil {
		state = func(string, string) {}
	}
	if err := a.checkDiskFor(need.Size, "a download ("+need.Kind+" "+need.Hex[:min(12, len(need.Hex))]+")"); err != nil { // §21 #112
		return err
	}
	for ctx.Err() == nil {
		// 1. peer copy first
		for _, slug := range a.reachablePeers() {
			if a.peerHave(ctx, slug, false)[need.Hex] {
				state("downloading", "peer:"+slug)
				a.setJob(job, "downloading")
				if err := a.copyFromPeer(ctx, slug, need); err == nil {
					a.log.Info("distrib", "blob copied from peer", "sha256", short(need.Hex), "peer", slug)
					return nil
				} else {
					a.log.Warn("distrib", "peer copy failed", "sha256", short(need.Hex), "peer", slug, "error", err.Error())
				}
			}
		}
		// 2. fetcher (rendezvous)
		exclude := map[string]bool{}
		for {
			fs := a.fetcherSet(need.Hex, exclude)
			if len(fs) == 0 {
				break
			}
			f := fs[0]
			if f == a.site {
				state("downloading", "cloud")
				a.setJob(job, "downloading")
				err := a.downloadFromCloud(ctx, need)
				if err == nil {
					a.log.Info("distrib", "blob downloaded from the cloud", "sha256", short(need.Hex), "kind", need.Kind)
					return nil
				}
				if ae := asAPIError(err); ae != nil && (ae.Status == 404 || ae.Status == 403) {
					// the cloud does not have it (e.g. a local file) → only peers can provide it; wait
					a.log.Debug("distrib", "cloud has no copy", "sha256", short(need.Hex), "status", ae.Status)
					exclude[f] = true
					continue
				}
				a.log.Warn("distrib", "cloud download failed", "sha256", short(need.Hex), "error", err.Error())
				exclude[f] = true
				continue
			}
			var fr model.FetchRequestResponse
			err := a.peerJSON(ctx, f, "POST", "/_opm/peer/fetch-request", model.FetchRequest{Sha256: "sha256:" + need.Hex, SizeBytes: need.Size, Kind: need.Kind, Ref: need.Ref}, &fr, 15*time.Second)
			if err != nil {
				if ae := asAPIError(err); ae != nil && ae.Status == 409 {
					if ff, _ := ae.Body.Error.Details["fetcher"].(string); ff != "" && ff != f && ff != a.site && !exclude[ff] {
						f = ff
						if err2 := a.peerJSON(ctx, f, "POST", "/_opm/peer/fetch-request", model.FetchRequest{Sha256: "sha256:" + need.Hex, SizeBytes: need.Size, Kind: need.Kind, Ref: need.Ref}, &fr, 15*time.Second); err2 != nil {
							exclude[f] = true
							continue
						}
					} else {
						exclude[f] = true
						continue
					}
				} else {
					exclude[f] = true
					continue
				}
			}
			state("downloading", "peer:"+f)
			a.setJob(job, "downloading")
			// poll the fetcher's have list, then copy
			deadline := time.Now().Add(30 * time.Minute)
			for time.Now().Before(deadline) && ctx.Err() == nil {
				if a.peerHave(ctx, f, true)[need.Hex] {
					if err := a.copyFromPeer(ctx, f, need); err == nil {
						return nil
					}
				}
				if !a.reachable(f) {
					break
				}
				var st model.FetchRequestResponse
				if err := a.peerJSON(ctx, f, "POST", "/_opm/peer/fetch-request", model.FetchRequest{Sha256: "sha256:" + need.Hex, SizeBytes: need.Size, Kind: need.Kind, Ref: need.Ref}, &st, 15*time.Second); err == nil && st.Status == "failed" {
					break
				}
				if !sleepCtx(ctx, a.tm.FetchPoll) {
					return ctx.Err()
				}
			}
			exclude[f] = true
		}
		// 3. nobody can fetch → wait
		state("waiting", "")
		a.setJob(job, "queued")
		if !sleepCtx(ctx, 10*time.Second) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func (a *Agent) copyFromPeer(ctx context.Context, slug string, need blobNeed) error {
	tmp := filepath.Join(a.dataDir, "tmp", "peer-"+need.Hex+".part")
	var have int64
	if st, err := os.Stat(tmp); err == nil {
		have = st.Size()
	}
	hdr := map[string]string{}
	if have > 0 {
		hdr["Range"] = fmt.Sprintf("bytes=%d-", have)
	}
	resp, done, err := a.peerStream(ctx, slug, "GET", "/_opm/peer/blobs/sha256:"+need.Hex, nil, hdr, 30*time.Minute)
	if err != nil {
		return err
	}
	defer done()
	flag := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusOK:
		flag |= os.O_TRUNC
	case http.StatusPartialContent:
		flag |= os.O_APPEND
	default:
		return fmt.Errorf("peer %s: blob status %d", slug, resp.StatusCode)
	}
	f, err := os.OpenFile(tmp, flag, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		return err
	}
	if err := a.storeBlobFile(tmp, need.Hex, need.Kind, "peer:"+slug); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (a *Agent) downloadFromCloud(ctx context.Context, need blobNeed) error {
	tmp := filepath.Join(a.dataDir, "tmp", "cloud-"+need.Hex)
	if err := a.cloudDownload(ctx, "/v1/agent/blobs/sha256:"+need.Hex, tmp, need.Hex, need.Size, nil); err != nil {
		return err
	}
	return a.storeBlobFile(tmp, need.Hex, need.Kind, "cloud")
}

// handleFetchRequest: POST /_opm/peer/fetch-request.
func (a *Agent) handleFetchRequest(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	var fr model.FetchRequest
	if e := readJSON(r, &fr); e != nil {
		return nil, e
	}
	h := strings.TrimPrefix(fr.Sha256, "sha256:")
	if len(h) != 64 {
		return nil, invalid("sha256 must be sha256:<64 hex>", nil)
	}
	if a.haveBlob(h) {
		return model.FetchRequestResponse{Fetcher: a.site, Status: "have"}, nil
	}
	a.mu.Lock()
	job := a.fetching[h]
	a.mu.Unlock()
	if job != nil {
		job.mu.Lock()
		st := job.status
		var es *string
		if job.err != nil {
			s := job.err.Error()
			es = &s
		}
		job.mu.Unlock()
		return model.FetchRequestResponse{Fetcher: a.site, Status: st, Error: es}, nil
	}
	if !a.hasInternet() && a.tun.active() == nil {
		return nil, herr(model.ErrNoInternet, "site "+a.site+" has no internet", nil)
	}
	fs := a.fetcherSet(h, nil)
	if len(fs) > 0 && fs[0] != a.site {
		return nil, herr(model.ErrConflict, "the fetcher of this blob is "+fs[0], map[string]any{"fetcher": fs[0]})
	}
	kind := fr.Kind
	if kind == "" {
		kind = "layer"
	}
	need := blobNeed{Hex: h, Size: fr.SizeBytes, Kind: kind, Ref: fr.Ref}
	a.goLoop("fetch", func(c context.Context) {
		if err := a.ensureBlob(c, need, nil); err != nil {
			a.log.Warn("distrib", "fetch for peer failed", "sha256", short(h), "error", err.Error())
		}
	})
	return model.FetchRequestResponse{Fetcher: a.site, Status: "queued"}, nil
}

// referencedBlobs returns hexes used by the head, open proposals and kept snapshots (never pruned).
func (a *Agent) referencedBlobs() map[string]bool {
	ref := map[string]bool{}
	addRev := func(r *model.Revision) {
		for _, img := range r.Catalog.Images {
			ref[strings.TrimPrefix(img.Digest, "sha256:")] = true
			ref[strings.TrimPrefix(img.ConfigDigest, "sha256:")] = true
			for _, l := range img.Layers {
				ref[strings.TrimPrefix(l.Digest, "sha256:")] = true
			}
		}
		for _, f := range r.Config.Files {
			if f.Content != nil {
				ref[strings.TrimPrefix(f.Content.Sha256, "sha256:")] = true
			}
		}
	}
	if r := a.headRevision(); r != nil {
		addRev(r)
	}
	for _, p := range a.openProposals() {
		if r, ok := a.revision(p.RevID); ok {
			addRev(r)
		}
	}
	for _, s := range a.snapshotRevisionIDs() {
		if r, ok := a.revision(s); ok {
			addRev(r)
		}
	}
	// §21 #116 the revision an unfinished update applies (it is not the head until it commits, and in branch mode no
	// proposal holds it) and the cloud commits staged for a merge: their images are loaded before a container uses them
	for _, r := range a.inFlightRevisions() {
		addRev(r)
	}
	a.volSyncReferencedBlobs(ref)        // §21 #101 queued / last volume snapshots
	if r := a.headRevision(); r != nil { // starter content (seeds) of the head: Reset to starter content needs them
		if s := siteOf(revConfig(r), a.site); s != nil {
			for _, n := range seedBlobs(revConfig(r), s, r.RevID) {
				ref[n.Hex] = true
			}
		}
	}
	return ref
}

// inFlightRevisions: the target revisions of the updates this site has not finished (plan stored, finishedAt NULL)
// and the cloud commits staged for a merge (stageCloudCommit).
func (a *Agent) inFlightRevisions() []*model.Revision {
	var out []*model.Revision
	rows, err := a.db.Query(`SELECT plan FROM updates WHERE finishedAt IS NULL`)
	if err == nil {
		for rows.Next() {
			var raw string
			var p model.UpdatePlan
			if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &p) == nil {
				r := p.Revision
				out = append(out, &r)
			}
		}
		rows.Close()
	}
	a.mu.Lock()
	staged := make([]string, 0, len(a.branchRevs))
	for id := range a.branchRevs {
		staged = append(staged, id)
	}
	a.mu.Unlock()
	for _, id := range staged {
		if r, ok := a.revision(id); ok {
			out = append(out, r)
		}
	}
	return out
}

func (a *Agent) pruneBlobs() {
	ref := a.referencedBlobs()
	rows, err := a.db.Query(`SELECT sha256, path FROM blobs WHERE lastUsedAt < ?`, time.Now().Add(-7*24*time.Hour).Unix())
	if err != nil {
		return
	}
	type bp struct{ h, p string }
	var del []bp
	for rows.Next() {
		var x bp
		if rows.Scan(&x.h, &x.p) == nil && !ref[x.h] {
			del = append(del, x)
		}
	}
	rows.Close()
	for _, x := range del {
		_ = os.Remove(x.p)
		_, _ = a.db.Exec(`DELETE FROM blobs WHERE sha256 = ?`, x.h)
		a.log.Info("distrib", "pruned unused blob", "sha256", short(x.h))
	}
}

// pruneImages removes the docker images that neither the head, an open proposal nor a kept snapshot references
// (config digest not in referencedBlobs) and that no container uses: both the readable <serviceSlug>:<version> tag(s)
// and the internal opm/cfg:<hex> tag are removed. Blobs stay until pruneBlobs removes them (images reload from blobs).
func (a *Agent) pruneImages(ctx context.Context) {
	if a.dc == nil {
		return
	}
	ref := a.referencedBlobs()
	imgs, err := a.dc.ImageList(ctx, "opm/cfg")
	if err != nil {
		return
	}
	cs, err := a.dc.ContainerList(ctx, true)
	if err != nil {
		return // without the container list nothing is known to be unused
	}
	used := map[string]bool{}
	for _, c := range cs {
		used[c.ImageID] = true
	}
	for _, im := range imgs {
		hex := ""
		for _, t := range im.RepoTags {
			if strings.HasPrefix(t, "opm/cfg:") {
				hex = strings.TrimPrefix(t, "opm/cfg:")
			}
		}
		if hex == "" || ref[hex] || used[im.ID] {
			continue
		}
		var tags []string
		for _, t := range im.RepoTags {
			if isReadableRef(t) {
				tags = append(tags, t)
			}
		}
		tags = append(tags, "opm/cfg:"+hex)
		for _, t := range tags {
			if err := a.dc.ImageRemove(ctx, t); err != nil && !errors.Is(err, docker.ErrNotFound) {
				a.log.Warn("distrib", "remove unused image tag failed", "image", t, "error", err.Error())
				continue
			}
			a.log.Info("distrib", "pruned unused image tag", "image", t)
		}
	}
}

var readableRepoRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// isReadableRef reports an OPM readable reference "<serviceSlug>:<version>" (one path component, no registry host).
func isReadableRef(t string) bool { return readableRepoRe.MatchString(t) }

var errWaiting = errors.New("waiting for a site with internet")
