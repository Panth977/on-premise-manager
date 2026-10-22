package agent

// §21 #123 shared files: one bucket per deployment (shared/src/files.ts). The listing reaches every site (cloud view;
// peers over the mesh, so sites without the cloud see each other's files); the bytes stay where they were published
// and move only when a person asks: a site gets a copy from a holding peer, else from the cloud, else it asks the
// cloud to fetch one first; the cloud gets a copy by asking a holding site (CloudView.fileFetches) to upload it.
// Copies live in <agent home>/shared-files/<fileId> (files/ holds the configured Files). Nothing is downloaded by itself.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/model"
)

type fileRow struct {
	f         model.SharedFile
	localPath string
	listedBy  string // cloud | peer | local
}

func (a *Agent) filesDir() string { return filepath.Join(a.dataDir, "shared-files") }

func (a *Agent) putFileRow(fr *fileRow) {
	if fr.f.Sites == nil {
		fr.f.Sites = []string{}
	}
	var lp any
	if fr.localPath != "" {
		lp = fr.localPath
	}
	b, _ := json.Marshal(fr.f)
	_, _ = a.db.Exec(`INSERT INTO files(fileId, data, localPath, listedBy, updatedAt) VALUES(?,?,?,?,?)
		ON CONFLICT(fileId) DO UPDATE SET data = excluded.data, localPath = excluded.localPath, listedBy = excluded.listedBy, updatedAt = excluded.updatedAt`,
		fr.f.FileID, string(b), lp, fr.listedBy, time.Now().Unix())
}

func (a *Agent) fileRow(id string) (*fileRow, bool) {
	var data, listedBy string
	var lp *string
	if err := a.db.QueryRow(`SELECT data, localPath, listedBy FROM files WHERE fileId = ?`, id).Scan(&data, &lp, &listedBy); err != nil {
		return nil, false
	}
	fr := &fileRow{listedBy: listedBy}
	if json.Unmarshal([]byte(data), &fr.f) != nil {
		return nil, false
	}
	if lp != nil {
		fr.localPath = *lp
	}
	return fr, true
}

func (a *Agent) fileRows() []*fileRow {
	rows, err := a.db.Query(`SELECT data, localPath, listedBy FROM files`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*fileRow
	for rows.Next() {
		var data, listedBy string
		var lp *string
		if rows.Scan(&data, &lp, &listedBy) != nil {
			continue
		}
		fr := &fileRow{listedBy: listedBy}
		if json.Unmarshal([]byte(data), &fr.f) != nil {
			continue
		}
		if lp != nil {
			fr.localPath = *lp
		}
		out = append(out, fr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].f.PublishedAt > out[j].f.PublishedAt })
	return out
}

// here: this site holds a complete copy
func (fr *fileRow) here() bool {
	if fr.localPath == "" {
		return false
	}
	st, err := os.Stat(fr.localPath)
	return err == nil && st.Size() == fr.f.SizeBytes
}

func addSite(l []string, s string) []string {
	for _, x := range l {
		if x == s {
			return l
		}
	}
	return append(append([]string{}, l...), s)
}

func removeSite(l []string, s string) []string {
	out := []string{}
	for _, x := range l {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// ───────────────────────── site jobs (getting a copy onto this site)

type fileJobs struct {
	mu   sync.Mutex
	jobs map[string]*model.SharedFileSiteJob
	up   map[string]bool // uploads to the cloud in flight
}

func (a *Agent) fjobs() *fileJobs {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fileJobs == nil {
		a.fileJobs = &fileJobs{jobs: map[string]*model.SharedFileSiteJob{}, up: map[string]bool{}}
	}
	return a.fileJobs
}

func (j *fileJobs) get(id string) *model.SharedFileSiteJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	if x := j.jobs[id]; x != nil {
		c := *x
		return &c
	}
	return nil
}

func (j *fileJobs) set(id string, fn func(*model.SharedFileSiteJob)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	x := j.jobs[id]
	if x == nil {
		x = &model.SharedFileSiteJob{Phase: "queued", StartedAt: nowTs()}
		j.jobs[id] = x
	}
	fn(x)
}

func (j *fileJobs) drop(id string) {
	j.mu.Lock()
	delete(j.jobs, id)
	j.mu.Unlock()
}

// siteView is a row as this site shows it.
func (a *Agent) siteView(fr *fileRow) model.SiteSharedFile {
	f := fr.f
	here := fr.here()
	if here {
		f.Sites = addSite(f.Sites, a.site)
	}
	if f.Sites == nil {
		f.Sites = []string{}
	}
	peers := []string{}
	reach := map[string]bool{}
	for _, p := range a.reachablePeers() {
		reach[p] = true
	}
	for _, s := range f.Sites {
		if s != a.site && reach[s] {
			peers = append(peers, s)
		}
	}
	return model.SiteSharedFile{SharedFile: f, Here: here, Job: a.fjobs().get(f.FileID), Sources: model.SharedFileSources{Peers: peers, Cloud: f.Cloud.Stored}, ListedBy: fr.listedBy}
}

// ───────────────────────── listing from the cloud view and the peers

// filesOnView merges the cloud view's listing (authoritative for what it lists) and starts the uploads it asks for.
func (a *Agent) filesOnView(v *model.CloudView) {
	if v.Files == nil {
		return // an older cloud
	}
	for _, cf := range v.Files {
		fr, ok := a.fileRow(cf.FileID)
		if !ok {
			fr = &fileRow{}
		}
		if ok && fr.f.State == "unpublished" && cf.State != "unpublished" {
			continue // unpublished here (offline); the tombstone reaches the cloud through the outbox
		}
		fr.f = cf
		fr.listedBy = "cloud"
		if cf.State == "unpublished" {
			a.dropLocalCopy(fr)
		}
		a.putFileRow(fr)
	}
	for _, ff := range v.FileFetches {
		if ff.From == a.site {
			id := ff.FileID
			a.goLoop("file-upload", func(c context.Context) { a.uploadFileToCloud(c, id) })
		}
	}
}

// peerFilesOnce merges what the reachable peers know (sites without the cloud learn each other's files this way) and
// forgets tombstones older than FILE_TOMBSTONE_DAYS.
func (a *Agent) peerFilesOnce(ctx context.Context) {
	cut := tsOf(time.Now().AddDate(0, 0, -model.FileTombstoneDays))
	for _, fr := range a.fileRows() {
		if fr.f.State == "unpublished" && fr.f.UnpublishedAt != nil && *fr.f.UnpublishedAt < cut {
			_, _ = a.db.Exec(`DELETE FROM files WHERE fileId = ?`, fr.f.FileID)
		}
	}
	for _, p := range a.reachablePeers() {
		var res model.PeerFilesResponse
		if err := a.peerJSON(ctx, p, "GET", "/_opm/peer/files", nil, &res, 20*time.Second); err != nil {
			continue
		}
		for _, pf := range res.Files {
			fr, ok := a.fileRow(pf.FileID)
			switch {
			case !ok:
				fr = &fileRow{f: pf, listedBy: "peer"}
				fr.f.Sites = removeSite(fr.f.Sites, a.site) // what the peer believes about us is not a copy here
				a.putFileRow(fr)
			case pf.State == "unpublished" && fr.f.State != "unpublished":
				fr.f.State, fr.f.UnpublishedAt, fr.f.UnpublishedBy = "unpublished", pf.UnpublishedAt, pf.UnpublishedBy
				a.dropLocalCopy(fr)
				a.putFileRow(fr)
			case fr.listedBy != "cloud":
				// peer knowledge only refines non-cloud rows: who holds a copy
				for _, s := range pf.Sites {
					if s != a.site {
						fr.f.Sites = addSite(fr.f.Sites, s)
					}
				}
				a.putFileRow(fr)
			}
		}
	}
}

func (a *Agent) dropLocalCopy(fr *fileRow) {
	if fr.localPath == "" {
		return
	}
	_ = os.Remove(fr.localPath)
	_ = os.Remove(fr.localPath + ".part")
	fr.localPath = ""
	fr.f.Sites = removeSite(fr.f.Sites, a.site)
}

// ───────────────────────── publishing on this site

func (a *Agent) publishLocal(rc *reqCtx, tmp, fileName string, note *string, size int64, shaHex, sourcePath string) (*model.SiteSharedFile, *httpError) {
	id := newID()
	if err := os.MkdirAll(a.filesDir(), 0o755); err != nil {
		return nil, herr(model.ErrInternal, err.Error(), nil)
	}
	dst := filepath.Join(a.filesDir(), id)
	if err := os.Rename(tmp, dst); err != nil {
		if err := copyFile(tmp, dst); err != nil {
			return nil, herr(model.ErrInternal, err.Error(), nil)
		}
		_ = os.Remove(tmp)
	}
	via := "browser"
	var sp *string
	if sourcePath != "" {
		via = "path"
		sp = &sourcePath
	}
	f := model.SharedFile{FileID: id, FileName: fileName, SizeBytes: size, Sha256: "sha256:" + shaHex, Note: note, PublishedAt: nowTs(), PublishedBy: rc.actor,
		Origin: model.SharedFileOrigin{Kind: "site", SiteSlug: a.site, Via: via, SourcePath: sp}, State: "ready", Sites: []string{a.site}}
	fr := &fileRow{f: f, localPath: dst, listedBy: "local"}
	a.putFileRow(fr)
	a.enqueue("file-register", "file-register:"+id, "POST", "/v1/agent/files", model.AgentRegisterFileRequest{FileID: id, FileName: fileName, SizeBytes: size,
		Sha256: f.Sha256, Note: note, PublishedAt: f.PublishedAt, PublishedBy: rc.actor, Via: via, SourcePath: sp})
	summary := "file " + fileName + " published from " + a.site
	if sourcePath != "" {
		summary = "file " + sourcePath + " published from " + a.site
	}
	a.activity(rc.actorPtr(), "agent", "file.published", trim200(summary), actorData(rc, map[string]any{"fileId": id, "fileName": fileName, "sizeBytes": size, "via": via, "sourcePath": sourcePath}))
	v := a.siteView(fr)
	return &v, nil
}

// ───────────────────────── getting a copy onto this site

func (a *Agent) startFetchToSite(rc *reqCtx, fr *fileRow) {
	id := fr.f.FileID
	j := a.fjobs()
	if x := j.get(id); x != nil && x.Phase != "failed" {
		return
	}
	j.set(id, func(x *model.SharedFileSiteJob) {
		*x = model.SharedFileSiteJob{Phase: "queued", StartedAt: nowTs()}
	})
	actor := rc.actor
	a.goLoop("file-fetch", func(c context.Context) { a.fetchFileToSite(c, id, actor) })
}

func (a *Agent) fetchFileToSite(ctx context.Context, id, actor string) {
	j := a.fjobs()
	fail := func(err error) {
		msg := err.Error()
		j.set(id, func(x *model.SharedFileSiteJob) { x.Phase, x.Error = "failed", &msg })
		a.log.Warn("files", "getting a copy failed", "fileId", id, "error", msg)
	}
	fr, ok := a.fileRow(id)
	if !ok || fr.f.State == "unpublished" {
		j.drop(id)
		return
	}
	if fr.here() {
		j.drop(id)
		return
	}
	if err := a.checkDiskFor(fr.f.SizeBytes, "the copy of file "+fr.f.FileName); err != nil {
		fail(err)
		return
	}
	dst := filepath.Join(a.filesDir(), id)
	_ = os.MkdirAll(a.filesDir(), 0o755)
	want := strings.TrimPrefix(fr.f.Sha256, "sha256:")
	progress := func(n int64) { j.set(id, func(x *model.SharedFileSiteJob) { x.BytesDone = n }) }
	from := ""
	var err error = errors.New("no site holding a copy answers and the cloud has none")
	// 1. a holding peer over the mesh (the listing may not know a copy made a moment ago: ask the reachable peers too)
	for _, p := range a.peerHolders(ctx, fr) {
		src := "peer:" + p
		j.set(id, func(x *model.SharedFileSiteJob) { x.Phase, x.From, x.Error = "downloading", &src, nil })
		if err = a.copyFileFromPeer(ctx, p, id, dst, want, progress); err == nil {
			from = src
			break
		}
		a.log.Warn("files", "peer copy failed", "fileId", id, "peer", p, "error", err.Error())
	}
	// 2. the cloud (asking it to fetch a copy first when it has none)
	if from == "" {
		asked := false
		deadline := time.Now().Add(30 * time.Minute)
		for ctx.Err() == nil && time.Now().Before(deadline) {
			cur, ok := a.fileRow(id)
			if !ok || cur.f.State == "unpublished" {
				j.drop(id)
				return
			}
			if cur.f.Cloud.Stored {
				src := "cloud"
				j.set(id, func(x *model.SharedFileSiteJob) { x.Phase, x.From, x.Error = "downloading", &src, nil })
				err = a.cloudDownload(ctx, "/v1/agent/files/"+id+"/download", dst, want, cur.f.SizeBytes, progress)
				if err == nil {
					from = "cloud"
				}
				break
			}
			if !asked {
				var f model.SharedFile
				if e := a.cloudJSON(ctx, "POST", "/v1/agent/files/"+id+"/fetch", model.AgentFileActorRequest{Actor: actor}, &f, callOpts{timeout: 30 * time.Second}); e != nil {
					err = fmt.Errorf("no holding site answers over the mesh and the cloud cannot fetch it: %w", e)
					break
				}
				asked = true
				cur.f = f
				cur.listedBy = "cloud"
				a.putFileRow(cur)
				j.set(id, func(x *model.SharedFileSiteJob) { x.Phase, x.From = "waiting-cloud", nil })
				a.kickLoop("poll")
				continue
			}
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			a.kickLoop("poll")
		}
	}
	if from == "" {
		fail(err)
		return
	}
	cur, ok := a.fileRow(id)
	if !ok || cur.f.State == "unpublished" {
		_ = os.Remove(dst)
		j.drop(id)
		return
	}
	cur.localPath = dst
	cur.f.Sites = addSite(cur.f.Sites, a.site)
	a.putFileRow(cur)
	j.drop(id)
	a.enqueue("file-held", "file-held:"+id, "POST", "/v1/agent/files/"+id+"/held", model.AgentFileHeldRequest{Held: true})
	a.activity(&actor, "agent", "file.fetched", trim200("file "+cur.f.FileName+" copied to "+a.site+" from "+from), map[string]any{"fileId": id, "fileName": cur.f.FileName, "where": a.site, "from": from})
}

// peerHolders: the reachable peers holding a copy — those the listing names first, then any other that says so.
func (a *Agent) peerHolders(ctx context.Context, fr *fileRow) []string {
	out := a.siteView(fr).Sources.Peers
	known := map[string]bool{}
	for _, p := range out {
		known[p] = true
	}
	for _, p := range a.reachablePeers() {
		if known[p] {
			continue
		}
		var res model.PeerFilesResponse
		if a.peerJSON(ctx, p, "GET", "/_opm/peer/files", nil, &res, 10*time.Second) != nil {
			continue
		}
		for _, f := range res.Files {
			if f.FileID == fr.f.FileID && f.State != "unpublished" && contains(f.Sites, p) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func (a *Agent) copyFileFromPeer(ctx context.Context, slug, id, dst, wantHex string, progress func(int64)) error {
	tmp := dst + ".part"
	var have int64
	if st, err := os.Stat(tmp); err == nil {
		have = st.Size()
	}
	hdr := map[string]string{}
	if have > 0 {
		hdr["Range"] = fmt.Sprintf("bytes=%d-", have)
	}
	resp, done, err := a.peerStream(ctx, slug, "GET", "/_opm/peer/files/"+id+"/content", nil, hdr, 2*time.Hour)
	if err != nil {
		return err
	}
	defer done()
	flag := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusOK:
		flag |= os.O_TRUNC
		have = 0
	case http.StatusPartialContent:
		flag |= os.O_APPEND
	default:
		return fmt.Errorf("peer %s: status %d", slug, resp.StatusCode)
	}
	f, err := os.OpenFile(tmp, flag, 0o600)
	if err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			have += int64(n)
			if progress != nil {
				progress(have)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	f.Close()
	got, err := fileSha256(tmp)
	if err != nil {
		return err
	}
	if got != wantHex {
		_ = os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch from %s", slug)
	}
	return os.Rename(tmp, dst)
}

// ───────────────────────── sending a copy to the cloud (it asked: CloudView.fileFetches)

func (a *Agent) uploadFileToCloud(ctx context.Context, id string) {
	j := a.fjobs()
	j.mu.Lock()
	if j.up[id] {
		j.mu.Unlock()
		return
	}
	j.up[id] = true
	j.mu.Unlock()
	defer func() {
		j.mu.Lock()
		delete(j.up, id)
		j.mu.Unlock()
	}()
	fr, ok := a.fileRow(id)
	if !ok || !fr.here() || fr.f.State == "unpublished" {
		return
	}
	var res model.AgentFileUploadResponse
	if err := a.cloudJSON(ctx, "POST", "/v1/agent/files/"+id+"/upload", nil, &res, callOpts{noTunnel: true, timeout: 60 * time.Second}); err != nil {
		a.log.Warn("files", "the cloud did not take the upload", "fileId", id, "error", err.Error())
		return
	}
	if err := a.uploadParts(ctx, res.Upload, fr.localPath, fr.f.SizeBytes, nil); err != nil {
		a.log.Warn("files", "upload to the cloud failed", "fileId", id, "error", err.Error())
		return
	}
	a.log.Info("files", "copy sent to the cloud", "fileId", id, "fileName", fr.f.FileName)
	a.kickLoop("poll")
}

// uploadParts sends the missing parts of an upload and completes it (resumable: the cloud lists the parts it has).
func (a *Agent) uploadParts(ctx context.Context, up model.Upload, localPath string, size int64, progress func(int64)) error {
	if cur := (model.Upload{}); a.cloudJSON(ctx, "GET", "/v1/agent/uploads/"+up.UploadID, nil, &cur, callOpts{noTunnel: true}) == nil {
		up = cur
	}
	have := map[int]bool{}
	for _, p := range up.PartsReceived {
		have[p] = true
	}
	ps := up.PartSize
	if ps <= 0 {
		ps = model.UploadPartSize
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var done int64
	for i := 0; i < up.PartCount; i++ {
		if have[i] {
			continue
		}
		buf := make([]byte, ps)
		n, err := f.ReadAt(buf, int64(i)*ps)
		if err != nil && err != io.EOF {
			return err
		}
		resp, err := a.cloudCall(ctx, "PUT", fmt.Sprintf("/v1/agent/uploads/%s/parts/%d", up.UploadID, i), buf[:n], callOpts{noTunnel: true, timeout: 5 * time.Minute, extraHdr: map[string]string{"Content-Type": "application/octet-stream"}})
		if err != nil {
			return err
		}
		if resp.Status >= 300 {
			ae := &apiError{Status: resp.Status}
			_ = json.Unmarshal(resp.Body, &ae.Body)
			return ae
		}
		done += int64(n)
		if progress != nil {
			progress(done)
		}
	}
	var fin model.CompleteUploadResponse
	return a.cloudJSON(ctx, "POST", "/v1/agent/uploads/"+up.UploadID+"/complete", nil, &fin, callOpts{noTunnel: true, timeout: 5 * time.Minute})
}

// ───────────────────────── legacy transfers → files (once, on start)

// migrateTransfersToFiles turns the copies of §21 #100 transfers on this site into shared files (fileId = transferId,
// as the cloud's migrate-m does); a never-uploaded outgoing transfer becomes a file published from this site.
func (a *Agent) migrateTransfersToFiles() {
	rows, err := a.db.Query(`SELECT transferId FROM transfers`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if len(ids) == 0 {
		return
	}
	_ = os.MkdirAll(a.filesDir(), 0o755)
	moved := 0
	for _, id := range ids {
		t, lp, ok := a.transfer(id)
		if !ok {
			continue
		}
		if t.Status == "expired" || lp == "" {
			_, _ = a.db.Exec(`DELETE FROM transfers WHERE transferId = ?`, id)
			continue
		}
		fileID := id
		local := strings.HasPrefix(id, "local-")
		if local {
			fileID = newID()
		}
		dst := filepath.Join(a.filesDir(), fileID)
		if err := os.Rename(lp, dst); err != nil {
			continue
		}
		var note *string
		if t.Note != "" {
			n := t.Note
			note = &n
		}
		origin := model.SharedFileOrigin{Kind: "cloud"}
		if t.Direction == "to-cloud" {
			via := "browser"
			if t.SourcePath != nil {
				via = "path"
			}
			origin = model.SharedFileOrigin{Kind: "site", SiteSlug: a.site, Via: via, SourcePath: t.SourcePath}
		}
		f := model.SharedFile{FileID: fileID, FileName: t.FileName, SizeBytes: t.SizeBytes, Sha256: t.Sha256, Note: note, PublishedAt: t.CreatedAt, PublishedBy: t.CreatedBy,
			Origin: origin, State: "ready", Cloud: model.SharedFileCloud{Stored: !local}, Sites: []string{a.site}}
		a.putFileRow(&fileRow{f: f, localPath: dst, listedBy: "local"})
		if local {
			a.enqueue("file-register", "file-register:"+fileID, "POST", "/v1/agent/files", model.AgentRegisterFileRequest{FileID: fileID, FileName: f.FileName, SizeBytes: f.SizeBytes,
				Sha256: f.Sha256, Note: note, PublishedAt: f.PublishedAt, PublishedBy: f.PublishedBy, Via: origin.Via, SourcePath: origin.SourcePath})
		} else {
			a.enqueue("file-held", "file-held:"+fileID, "POST", "/v1/agent/files/"+fileID+"/held", model.AgentFileHeldRequest{Held: true})
		}
		_, _ = a.db.Exec(`DELETE FROM transfers WHERE transferId = ?`, id)
		moved++
	}
	if moved > 0 {
		a.log.Info("files", "transfers turned into shared files", "count", moved)
	}
}

// ───────────────────────── HTTP (site mux; peers)

func (a *Agent) registerFileRoutes(h func(pattern string, kind authKind, fn apiFn)) {
	h("GET /files", authSession, a.handleListFiles)
	h("POST /files/upload", authActor, a.handlePublishFromBrowser)
	h("POST /files/from-path", authActor, a.handlePublishFromPath)
	h("POST /files/{fileId}/fetch", authActor, a.handleFetchFile)
	h("GET /files/{fileId}/download", authSession, a.handleDownloadFile)
	h("POST /files/{fileId}/save", authActor, a.handleSaveFile)
	h("DELETE /files/{fileId}/copy", authActor, a.handleDropCopy)
	h("DELETE /files/{fileId}", authActor, a.handleUnpublishFile)
}

func (a *Agent) handleListFiles(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	all := r.URL.Query().Get("unpublished") == "true" || r.URL.Query().Get("unpublished") == "1"
	out := []model.SiteSharedFile{}
	for _, fr := range a.fileRows() {
		if fr.f.State == "unpublished" && !all {
			continue
		}
		out = append(out, a.siteView(fr))
	}
	return model.ListResponse[model.SiteSharedFile]{Items: out}, 200, nil
}

func noteOf(s string) (*string, *httpError) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > model.FileNoteMax {
		return nil, invalid("note: at most 500 characters", nil)
	}
	if s == "" {
		return nil, nil
	}
	return &s, nil
}

func (a *Agent) handlePublishFromBrowser(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	name := r.URL.Query().Get("fileName")
	if !validFileName(name) {
		return nil, 0, invalid("fileName is required (1–255 characters, no / \\ NUL)", nil)
	}
	note, e := noteOf(r.URL.Query().Get("note"))
	if e != nil {
		return nil, 0, e
	}
	f, err := os.CreateTemp(filepath.Join(a.dataDir, "tmp"), "publish-*")
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	var src io.Reader = r.Body
	if rc.body != nil || rc.bodyFile != "" {
		rd, _ := rc.reader()
		defer rd.Close()
		src = rd
	}
	n, err := io.Copy(f, io.LimitReader(src, model.FileMax+1))
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return nil, 0, herr(model.ErrInvalid, err.Error(), nil)
	}
	if n > model.FileMax {
		os.Remove(f.Name())
		return nil, 0, herr(model.ErrTooLarge, "at most 2 GiB", nil)
	}
	if n == 0 {
		os.Remove(f.Name())
		return nil, 0, invalid("the file is empty", nil)
	}
	sum, _ := fileSha256(f.Name())
	v, he := a.publishLocal(rc, f.Name(), name, note, n, sum, "")
	if he != nil {
		os.Remove(f.Name())
		return nil, 0, he
	}
	return v, 201, nil
}

func (a *Agent) handlePublishFromPath(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.PublishFromPathRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	var noteS string
	if req.Note != nil {
		noteS = *req.Note
	}
	note, e := noteOf(noteS)
	if e != nil {
		return nil, 0, e
	}
	if !filepath.IsAbs(req.Path) || strings.ContainsRune(req.Path, 0) {
		return nil, 0, invalid("path must be absolute", []model.ValidationIssue{{Path: "path", Code: "V_FORMAT", Message: "absolute path", Severity: "error"}})
	}
	p := filepath.Clean(req.Path)
	if why := a.sendBlockReason(p); why != "" { // §21 #100 safety policy (requested path and its resolved target)
		return nil, 0, herr(model.ErrForbidden, p+": "+why, map[string]any{"path": p, "reason": why})
	}
	st, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, 0, herr(model.ErrForbidden, "the agent (user "+processUser()+") cannot read "+p, map[string]any{"path": p})
		}
		return nil, 0, herr(model.ErrNotFound, "no such file: "+p, nil)
	}
	if st.IsDir() {
		return nil, 0, invalid("folders cannot be published — pick a file (or make a .tar.gz of the folder on the server first)", []model.ValidationIssue{{Path: "path", Code: "V_TYPE", Message: "a folder", Severity: "error"}})
	}
	if !st.Mode().IsRegular() {
		return nil, 0, invalid("not a regular file", nil)
	}
	if st.Size() > model.FileMax {
		return nil, 0, herr(model.ErrTooLarge, "at most 2 GiB", nil)
	}
	if err := a.checkDiskFor(st.Size(), "the published file "+filepath.Base(p)); err != nil {
		return nil, 0, herr(model.ErrUnavailable, err.Error(), nil)
	}
	tmp := filepath.Join(a.dataDir, "tmp", "publish-"+newID())
	if err := copyFile(p, tmp); err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, fs.ErrPermission) {
			return nil, 0, herr(model.ErrForbidden, "the agent (user "+processUser()+") cannot read "+p, map[string]any{"path": p})
		}
		return nil, 0, invalid("cannot read the file: "+err.Error(), nil)
	}
	size := st.Size()
	if fi, err := os.Stat(tmp); err == nil {
		size = fi.Size() // the copy is what is published (the source may have changed meanwhile)
	}
	if size > model.FileMax {
		_ = os.Remove(tmp)
		return nil, 0, herr(model.ErrTooLarge, "at most 2 GiB", nil)
	}
	sum, _ := fileSha256(tmp)
	v, he := a.publishLocal(rc, tmp, filepath.Base(p), note, size, sum, p)
	if he != nil {
		_ = os.Remove(tmp)
		return nil, 0, he
	}
	return v, 201, nil
}

func (a *Agent) liveFile(id string) (*fileRow, *httpError) {
	fr, ok := a.fileRow(id)
	if !ok {
		return nil, herr(model.ErrNotFound, "no file "+id, nil)
	}
	if fr.f.State == "unpublished" {
		return nil, herr(model.ErrGone, "file "+fr.f.FileName+" was unpublished", nil)
	}
	return fr, nil
}

func (a *Agent) handleFetchFile(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	fr, e := a.liveFile(r.PathValue("fileId"))
	if e != nil {
		return nil, 0, e
	}
	if fr.f.State == "uploading" {
		return nil, 0, herr(model.ErrConflict, "the file is still being uploaded by its publisher", nil)
	}
	if !fr.here() {
		a.startFetchToSite(getRC(r), fr)
	}
	return a.siteView(fr), 202, nil
}

func (a *Agent) handleDownloadFile(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	fr, e := a.liveFile(r.PathValue("fileId"))
	if e != nil {
		return nil, 0, e
	}
	if !fr.here() {
		return nil, 0, herr(model.ErrConflict, "the file is not on "+a.site+" yet — get a copy first", nil)
	}
	f, err := os.Open(fr.localPath)
	if err != nil {
		return nil, 0, herr(model.ErrNotFound, err.Error(), nil)
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fr.f.FileName))
	http.ServeContent(w, r, fr.f.FileName, st.ModTime(), f)
	if r.Header.Get("Range") == "" {
		a.activity(rc.actorPtr(), "agent", "file.downloaded", trim200("file "+fr.f.FileName+" downloaded in the browser from "+a.site), actorData(rc, map[string]any{"fileId": fr.f.FileID, "fileName": fr.f.FileName, "where": a.site}))
	}
	return nil, 0, nil
}

func (a *Agent) handleSaveFile(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	fr, e := a.liveFile(r.PathValue("fileId"))
	if e != nil {
		return nil, 0, e
	}
	var req model.SaveFileRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if !fr.here() {
		return nil, 0, herr(model.ErrConflict, "the file is not on "+a.site+" yet — get a copy first", nil)
	}
	if !filepath.IsAbs(req.Path) || strings.ContainsRune(req.Path, 0) {
		return nil, 0, invalid("path must be absolute", []model.ValidationIssue{{Path: "path", Code: "V_FORMAT", Message: "absolute path", Severity: "error"}})
	}
	p := filepath.Clean(req.Path)
	if st, err := os.Stat(filepath.Dir(p)); err != nil || !st.IsDir() {
		return nil, 0, invalid("the parent directory does not exist", nil)
	}
	if _, err := os.Lstat(p); err == nil {
		return nil, 0, herr(model.ErrConflict, p+" already exists", nil)
	}
	tmp := p + ".opm-" + newID()
	if err := copyFile(fr.localPath, tmp); err != nil {
		return nil, 0, invalid("cannot write: "+err.Error(), nil)
	}
	_ = os.Chmod(tmp, 0o640)
	if os.Geteuid() == 0 {
		_ = os.Chown(tmp, 0, 0)
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return nil, 0, invalid("cannot write: "+err.Error(), nil)
	}
	a.activity(rc.actorPtr(), "agent", "file.saved", trim200("file "+fr.f.FileName+" saved to "+p+" on "+a.site), actorData(rc, map[string]any{"fileId": fr.f.FileID, "fileName": fr.f.FileName, "path": p}))
	return a.siteView(fr), 200, nil
}

func (a *Agent) handleDropCopy(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	fr, e := a.liveFile(r.PathValue("fileId"))
	if e != nil {
		return nil, 0, e
	}
	if !fr.here() {
		return a.siteView(fr), 200, nil
	}
	others := len(removeSite(fr.f.Sites, a.site))
	if !fr.f.Cloud.Stored && others == 0 {
		return nil, 0, herr(model.ErrConflict, "this is the only copy of "+fr.f.FileName+" — unpublish it instead, or get a copy elsewhere first", nil)
	}
	a.dropLocalCopy(fr)
	a.putFileRow(fr)
	a.enqueue("file-held", "file-held:"+fr.f.FileID, "POST", "/v1/agent/files/"+fr.f.FileID+"/held", model.AgentFileHeldRequest{Held: false})
	a.activity(rc.actorPtr(), "agent", "file.copy-dropped", trim200("the copy of "+fr.f.FileName+" on "+a.site+" was deleted"), actorData(rc, map[string]any{"fileId": fr.f.FileID, "fileName": fr.f.FileName}))
	return a.siteView(fr), 200, nil
}

func (a *Agent) handleUnpublishFile(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	fr, ok := a.fileRow(r.PathValue("fileId"))
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "no file "+r.PathValue("fileId"), nil)
	}
	if fr.f.State != "unpublished" {
		now := nowTs()
		actor := rc.actor
		fr.f.State, fr.f.UnpublishedAt, fr.f.UnpublishedBy = "unpublished", &now, &actor
		a.dropLocalCopy(fr)
		fr.f.Cloud = model.SharedFileCloud{}
		a.putFileRow(fr)
		a.fjobs().drop(fr.f.FileID)
		// the cloud now (when it answers), else through the outbox; peers learn it from the listing
		path := "/v1/agent/files/" + fr.f.FileID + "/unpublish"
		var out model.Deleted
		if err := a.cloudJSON(r.Context(), "POST", path, model.AgentFileActorRequest{Actor: actor}, &out, callOpts{timeout: 15 * time.Second}); err != nil {
			a.enqueue("file-unpublish", "file-unpublish:"+fr.f.FileID, "POST", path, model.AgentFileActorRequest{Actor: actor})
		}
		a.activity(rc.actorPtr(), "agent", "file.unpublished", trim200("file "+fr.f.FileName+" unpublished"), actorData(rc, map[string]any{"fileId": fr.f.FileID, "fileName": fr.f.FileName}))
	}
	return model.Deleted{Deleted: true}, 200, nil
}

// peer API: what this site knows, and the bytes of its copies
func (a *Agent) peerFiles(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	out := model.PeerFilesResponse{SiteSlug: a.site, Files: []model.SharedFile{}}
	for _, fr := range a.fileRows() {
		f := fr.f
		if fr.here() {
			f.Sites = addSite(f.Sites, a.site)
		} else {
			f.Sites = removeSite(f.Sites, a.site)
		}
		out.Files = append(out.Files, f)
	}
	return out, nil
}

func (a *Agent) servePeerFileContent(w http.ResponseWriter, r *http.Request) {
	fr, ok := a.fileRow(r.PathValue("fileId"))
	if !ok || fr.f.State == "unpublished" || !fr.here() {
		writeErr(w, herr(model.ErrNotFound, "no copy here", nil))
		return
	}
	f, err := os.Open(fr.localPath)
	if err != nil {
		writeErr(w, herr(model.ErrNotFound, "no copy here", nil))
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-OPM-Sha256", fr.f.Sha256)
	http.ServeContent(w, r, "", st.ModTime(), f)
}
