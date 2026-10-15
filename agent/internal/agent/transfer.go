package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/store"
)

func (a *Agent) putTransfer(t *model.Transfer, localPath string) {
	_, _ = a.db.Exec(`INSERT INTO transfers(transferId, direction, status, createdAt, data, localPath) VALUES(?,?,?,?,?,?)
		ON CONFLICT(transferId) DO UPDATE SET status = excluded.status, data = excluded.data, localPath = COALESCE(NULLIF(excluded.localPath, ''), transfers.localPath)`,
		t.TransferID, t.Direction, t.Status, t.CreatedAt, store.JSON(t), localPath)
}

func (a *Agent) transfer(id string) (*model.Transfer, string, bool) {
	var raw string
	var lp sql.NullString
	if err := a.db.QueryRow(`SELECT data, localPath FROM transfers WHERE transferId = ?`, id).Scan(&raw, &lp); err != nil {
		return nil, "", false
	}
	var t model.Transfer
	if json.Unmarshal([]byte(raw), &t) != nil {
		return nil, "", false
	}
	return &t, lp.String, true
}

func (a *Agent) transfers(direction string) []model.Transfer {
	rows, err := a.db.Query(`SELECT data, localPath FROM transfers WHERE direction = ? ORDER BY createdAt DESC`, direction)
	if err != nil {
		return []model.Transfer{}
	}
	defer rows.Close()
	out := []model.Transfer{}
	for rows.Next() {
		var raw string
		var lp sql.NullString
		var t model.Transfer
		if rows.Scan(&raw, &lp) == nil && json.Unmarshal([]byte(raw), &t) == nil {
			t.ExpiresAt = nil // transfers never expire; ignore a date stored by an older agent
			a.withDelivery(&t, lp.String)
			out = append(out, t)
		}
	}
	return out
}

// ───────────────────────── §21 #100 store-and-forward delivery state

// xferProg is the in-memory delivery state of one transfer (lost on restart: the next attempt re-reads the cloud upload).
type xferProg struct {
	active    bool
	bytesDone int64
	attempts  int
	lastError string
	lastAt    time.Time
	nextAt    time.Time
}

type xferTracker struct {
	mu sync.Mutex
	m  map[string]*xferProg
}

func (x *xferTracker) get(id string) *xferProg {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.m == nil {
		x.m = map[string]*xferProg{}
	}
	p := x.m[id]
	if p == nil {
		p = &xferProg{}
		x.m[id] = p
	}
	return p
}

func (x *xferTracker) snapshot(id string) (xferProg, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if p := x.m[id]; p != nil {
		return *p, true
	}
	return xferProg{}, false
}

func (x *xferTracker) update(id string, fn func(p *xferProg)) {
	p := x.get(id)
	x.mu.Lock()
	fn(p)
	x.mu.Unlock()
}

func (x *xferTracker) rename(from, to string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if p := x.m[from]; p != nil {
		x.m[to] = p
		delete(x.m, from)
	}
}

// due reports whether a transfer may be attempted now (backoff after failures).
func (x *xferTracker) due(id string) bool {
	p, ok := x.snapshot(id)
	return !ok || p.nextAt.IsZero() || !time.Now().Before(p.nextAt)
}

// failed records a failed attempt: backoff period × 2^(attempts-1), max TransfersMax (permanent refusals: max at once).
func (a *Agent) xferFailed(id string, err error, permanent bool) {
	a.xfer.update(id, func(p *xferProg) {
		p.active = false
		p.attempts++
		p.lastError = trim200(err.Error())
		p.lastAt = time.Now()
		d := a.tm.Transfers << uint(min(p.attempts-1, 10))
		if d > a.tm.TransfersMax || d <= 0 || permanent {
			d = a.tm.TransfersMax
		}
		p.nextAt = time.Now().Add(d)
	})
}

func (a *Agent) xferOK(id string) {
	a.xfer.update(id, func(p *xferProg) {
		p.active = false
		p.attempts = 0
		p.lastError = ""
		p.lastAt = time.Now()
		p.nextAt = time.Time{}
	})
}

// withDelivery adds the §21 #100 delivery block to an agent-local transfer.
func (a *Agent) withDelivery(t *model.Transfer, localPath string) {
	d := &model.TransferDelivery{}
	p, _ := a.xfer.snapshot(t.TransferID)
	if t.Direction == "to-cloud" {
		switch {
		case t.Status == "uploading" && p.active:
			d.Phase = "uploading"
		case t.Status == "uploading":
			d.Phase = "queued"
		default:
			d.Phase = "stored"
			p.bytesDone = t.SizeBytes
		}
	} else {
		switch {
		case localPath != "":
			d.Phase = "on-site"
			p.bytesDone = t.SizeBytes
		case p.active:
			d.Phase = "downloading"
		default:
			d.Phase = "pending"
		}
	}
	d.BytesDone = p.bytesDone
	if d.Phase == "uploading" || d.Phase == "queued" || d.Phase == "downloading" || d.Phase == "pending" {
		d.Attempts = p.attempts
		if p.lastError != "" {
			e := p.lastError
			d.LastError = &e
		}
		if !p.nextAt.IsZero() && time.Now().Before(p.nextAt) {
			n := tsOf(p.nextAt)
			d.NextAttemptAt = &n
		}
	}
	if !p.lastAt.IsZero() {
		l := tsOf(p.lastAt)
		d.LastAttemptAt = &l
	}
	t.Delivery = d
}

// noteTransferToSite mirrors a to-site transfer from the cloud view (download queued). A file that is already on the
// site but was never reported as fetched (agents < 0.11.2 did not report it) is reported now.
func (a *Agent) noteTransferToSite(m model.TransferMeta) {
	if cur, lp, ok := a.transfer(m.TransferID); ok {
		changed := false
		if cur.Status == "ready" && m.Status != cur.Status && m.Status != "" {
			cur.Status = m.Status
			changed = true
		}
		if lp != "" && cur.FetchedAt == nil {
			a.reportFetched(cur)
			changed = true
		}
		if changed {
			a.putTransfer(cur, "")
		}
		return
	}
	t := model.Transfer{TransferID: m.TransferID, DeploymentID: a.dep, SiteSlug: m.SiteSlug, Direction: "to-site", FileName: m.FileName, SizeBytes: m.SizeBytes,
		Sha256: m.Sha256, CreatedAt: m.CreatedAt, CreatedBy: m.CreatedBy, Note: m.Note, Status: m.Status}
	a.putTransfer(&t, "")
}

// dropDeletedTransfer removes a to-site transfer the cloud deleted: the Receive row and the agent's own copy (the
// downloaded file and any partial download in the data dir). A copy saved on the server (Save on server) is kept.
func (a *Agent) dropDeletedTransfer(id string) {
	t, _, ok := a.transfer(id)
	if !ok || t.Direction != "to-site" {
		return
	}
	own := filepath.Join(a.dataDir, "transfers", id)
	_ = os.Remove(own)
	_ = os.Remove(own + ".part")
	_, _ = a.db.Exec(`DELETE FROM transfers WHERE transferId = ?`, id)
	a.xfer.mu.Lock()
	delete(a.xfer.m, id)
	a.xfer.mu.Unlock()
	data := map[string]any{"transferId": id, "direction": "to-site", "fileName": t.FileName, "sizeBytes": t.SizeBytes}
	summary := "file " + t.FileName + " removed: it was deleted in the cloud"
	if t.SavedPath != nil {
		data["savedPath"] = *t.SavedPath
		summary += " (the copy saved at " + *t.SavedPath + " is kept)"
	}
	a.activity(nil, "agent", "transfer.deleted", trim200(summary), data)
	a.log.Info("transfer", "received file deleted in the cloud; removed", "transferId", id, "fileName", t.FileName)
}

// reportFetched marks a to-site transfer as stored on this site and tells the cloud (outbox; idempotent).
func (a *Agent) reportFetched(t *model.Transfer) {
	now := nowTs()
	t.FetchedAt = &now
	a.enqueue("transfer-status", "transfer-fetched:"+t.TransferID, "POST", "/v1/agent/transfers/"+t.TransferID+"/status", model.AgentTransferStatusRequest{Status: "fetched", Actor: "system"})
}

func (a *Agent) transfersOnce(ctx context.Context) {
	if !a.enrolled {
		return
	}
	// to-site downloads (the cloud keeps the file; we fetch it whenever we are online)
	for _, id := range a.transferIDs(`SELECT transferId FROM transfers WHERE direction = 'to-site' AND (localPath IS NULL OR localPath = '') AND status != 'expired' ORDER BY createdAt`) {
		if ctx.Err() != nil {
			return
		}
		t, _, ok := a.transfer(id)
		if !ok || !a.xfer.due(id) {
			continue
		}
		dst := filepath.Join(a.dataDir, "transfers", id)
		a.xfer.update(id, func(p *xferProg) { p.active = true; p.lastAt = time.Now() })
		err := a.cloudDownload(ctx, "/v1/agent/transfers/"+id+"/download", dst, hexOf(t.Sha256), t.SizeBytes, func(n int64) {
			a.xfer.update(id, func(p *xferProg) { p.bytesDone = n })
		})
		if err != nil {
			ae := asAPIError(err)
			gone := ae != nil && (ae.Status == 404 || ae.Status == 410)
			a.xferFailed(id, err, ae != nil && !gone && ae.Status != 429 && ae.Status < 500)
			a.log.Warn("transfer", "download failed (retrying)", "transferId", id, "error", err.Error())
			if gone {
				t.Status = "expired"
				a.putTransfer(t, "")
			}
			continue
		}
		a.xferOK(id)
		a.reportFetched(t)
		a.putTransfer(t, dst)
		a.log.Info("transfer", "received file from the cloud", "transferId", id, "fileName", t.FileName)
	}
	// to-cloud uploads (stored on this site first; the cloud stores it whether anyone looks or not)
	for _, id := range a.transferIDs(`SELECT transferId FROM transfers WHERE direction = 'to-cloud' AND status = 'uploading' ORDER BY createdAt`) {
		if ctx.Err() != nil {
			return
		}
		if !a.xfer.due(id) {
			continue
		}
		if err := a.uploadTransfer(ctx, id); err != nil {
			a.log.Warn("transfer", "upload failed (retrying)", "transferId", id, "error", err.Error())
		}
	}
}

func (a *Agent) transferIDs(q string) []string {
	rows, err := a.db.Query(q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// permanentErr: the cloud refused (4xx other than 401 / 408 / 429) — retried at the longest backoff only.
func permanentErr(err error) bool {
	ae := asAPIError(err)
	return ae != nil && ae.Status >= 400 && ae.Status < 500 && ae.Status != 401 && ae.Status != 408 && ae.Status != 429
}

// uploadTransfer creates the cloud transfer (if needed) and uploads the missing parts (direct or relay, never the
// tunnel: tunnel request bodies are ≤ 4 MiB and an upload part is 8 MiB). Resumable: the cloud upload lists the parts it has.
func (a *Agent) uploadTransfer(ctx context.Context, id string) (rerr error) {
	t, lp, ok := a.transfer(id)
	if !ok || lp == "" {
		return fmt.Errorf("transfer %s has no local file", id)
	}
	cur := id
	defer func() {
		if rerr != nil {
			a.xferFailed(cur, rerr, permanentErr(rerr))
		}
	}()
	a.xfer.update(cur, func(p *xferProg) { p.lastAt = time.Now() })
	if strings.HasPrefix(t.TransferID, "local-") {
		var res model.CreateTransferResponse
		err := a.cloudJSON(ctx, "POST", "/v1/agent/transfers", model.AgentCreateTransferRequest{FileName: t.FileName, SizeBytes: t.SizeBytes, Sha256: t.Sha256, Note: t.Note, Actor: t.CreatedBy}, &res, callOpts{noTunnel: true, timeout: 60 * time.Second})
		if err != nil {
			return err
		}
		_, _ = a.db.Exec(`DELETE FROM transfers WHERE transferId = ?`, t.TransferID)
		nt := res.Transfer
		nt.SourcePath = t.SourcePath
		final := filepath.Join(a.dataDir, "transfers", nt.TransferID)
		if os.Rename(lp, final) == nil {
			lp = final
		}
		a.putTransfer(&nt, lp)
		a.xfer.rename(t.TransferID, nt.TransferID)
		a.log.Info("transfer", "cloud transfer created", "localId", t.TransferID, "transferId", nt.TransferID)
		t = &nt
		cur = nt.TransferID
	}
	if t.UploadID == nil {
		return fmt.Errorf("transfer %s has no upload", t.TransferID)
	}
	var up model.Upload
	if err := a.cloudJSON(ctx, "GET", "/v1/agent/uploads/"+*t.UploadID, nil, &up, callOpts{noTunnel: true}); err != nil {
		if ae := asAPIError(err); ae != nil && (ae.Status == 404 || ae.Status == 410) {
			// the cloud upload expired (24 h) or failed: start again with a new cloud transfer, the file is still here
			_, _ = a.db.Exec(`DELETE FROM transfers WHERE transferId = ?`, t.TransferID)
			nt := *t
			nt.TransferID = "local-" + newID()
			nt.UploadID = nil
			nt.Status = "uploading"
			a.putTransfer(&nt, lp)
			a.xfer.rename(t.TransferID, nt.TransferID)
			cur = nt.TransferID
			a.log.Warn("transfer", "cloud upload expired; starting a new one", "transferId", t.TransferID)
			return fmt.Errorf("the cloud upload expired; starting again")
		}
		return err
	}
	have := map[int]bool{}
	for _, p := range up.PartsReceived {
		have[p] = true
	}
	ps := up.PartSize
	if ps <= 0 {
		ps = model.UploadPartSize
	}
	partBytes := func(i int) int64 {
		if int64(i+1)*ps > t.SizeBytes {
			return t.SizeBytes - int64(i)*ps
		}
		return ps
	}
	var done int64
	for i := range have {
		done += partBytes(i)
	}
	a.xfer.update(cur, func(p *xferProg) { p.active = true; p.bytesDone = done })
	f, err := os.Open(lp)
	if err != nil {
		return err
	}
	defer f.Close()
	for i := 0; i < up.PartCount; i++ {
		if have[i] {
			continue
		}
		buf := make([]byte, ps)
		n, err := f.ReadAt(buf, int64(i)*ps)
		if err != nil && err != io.EOF {
			return err
		}
		resp, err := a.cloudCall(ctx, "PUT", fmt.Sprintf("/v1/agent/uploads/%s/parts/%d", *t.UploadID, i), buf[:n], callOpts{noTunnel: true, timeout: 5 * time.Minute, extraHdr: map[string]string{"Content-Type": "application/octet-stream"}})
		if err != nil {
			return err
		}
		if resp.Status >= 300 {
			ae := &apiError{Status: resp.Status}
			_ = json.Unmarshal(resp.Body, &ae.Body)
			return ae
		}
		done += int64(n)
		a.xfer.update(cur, func(p *xferProg) { p.bytesDone = done })
	}
	var fin model.CompleteUploadResponse
	if err := a.cloudJSON(ctx, "POST", "/v1/agent/uploads/"+*t.UploadID+"/complete", nil, &fin, callOpts{noTunnel: true, timeout: 5 * time.Minute}); err != nil {
		return err
	}
	src := t.SourcePath
	if fin.Transfer != nil {
		t = fin.Transfer
		t.SourcePath = src
	}
	t.Status = "ready"
	a.putTransfer(t, lp)
	a.xferOK(cur)
	a.enqueue("transfer-status", "transfer:"+t.TransferID, "POST", "/v1/agent/transfers/"+t.TransferID+"/status", model.AgentTransferStatusRequest{Status: "ready", Actor: t.CreatedBy})
	a.log.Info("transfer", "sent file to the cloud", "transferId", t.TransferID, "fileName", t.FileName)
	return nil
}

func validFileName(n string) bool {
	return n != "" && len(n) <= 255 && !strings.ContainsAny(n, "/\\\x00")
}

// newOutgoing stores a to-cloud file on this site and queues it (§21 #100 store-and-forward: the request never waits
// for the cloud; the transfers loop creates the cloud transfer and uploads it whenever a route exists).
func (a *Agent) newOutgoing(rc *reqCtx, tmp, fileName, note string, size int64, sha, sourcePath string) (*model.Transfer, *httpError) {
	id := "local-" + newID()
	dst := filepath.Join(a.dataDir, "transfers", id)
	if err := os.Rename(tmp, dst); err != nil {
		if err := copyFile(tmp, dst); err != nil {
			return nil, herr(model.ErrInternal, err.Error(), nil)
		}
		_ = os.Remove(tmp)
	}
	t := model.Transfer{TransferID: id, DeploymentID: a.dep, SiteSlug: a.site, Direction: "to-cloud", FileName: fileName, SizeBytes: size, Sha256: "sha256:" + sha,
		CreatedAt: nowTs(), CreatedBy: rc.actor, Note: note, Status: "uploading"}
	source := "browser"
	if sourcePath != "" {
		t.SourcePath = &sourcePath
		source = "path"
	}
	a.putTransfer(&t, dst)
	data := map[string]any{"transferId": t.TransferID, "direction": "to-cloud", "fileName": fileName, "sizeBytes": size, "source": source}
	summary := "file " + fileName + " sent to the cloud (queued on the site)"
	if sourcePath != "" {
		data["sourcePath"] = sourcePath
		summary = "file " + sourcePath + " sent to the cloud (queued on the site)"
	}
	a.activity(rc.actorPtr(), "agent", "transfer.sent", trim200(summary), actorData(rc, data))
	a.withDelivery(&t, dst)
	a.kickLoop("transfers")
	return &t, nil
}

func (a *Agent) handleListTransfers(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	d := r.URL.Query().Get("direction")
	if d != "to-site" && d != "to-cloud" {
		return nil, 0, invalid("direction must be to-site or to-cloud", nil)
	}
	return model.ListResponse[model.Transfer]{Items: a.transfers(d)}, 200, nil
}

func (a *Agent) handleSendFromBrowser(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	name := r.URL.Query().Get("fileName")
	note := r.URL.Query().Get("note")
	if !validFileName(name) {
		return nil, 0, invalid("fileName is required (1–255 characters, no / \\ NUL)", nil)
	}
	if len([]rune(note)) > 500 {
		return nil, 0, invalid("note: at most 500 characters", nil)
	}
	f, err := os.CreateTemp(filepath.Join(a.dataDir, "tmp"), "send-*")
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	var src io.Reader = r.Body
	if rc.body != nil || rc.bodyFile != "" {
		rd, _ := rc.reader()
		defer rd.Close()
		src = rd
	}
	n, err := io.Copy(f, io.LimitReader(src, model.TransferMax+1))
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return nil, 0, herr(model.ErrInvalid, err.Error(), nil)
	}
	if n > model.TransferMax {
		os.Remove(f.Name())
		return nil, 0, herr(model.ErrTooLarge, "at most 2 GiB", nil)
	}
	sum, _ := fileSha256(f.Name())
	t, e := a.newOutgoing(rc, f.Name(), name, note, n, sum, "")
	if e != nil {
		return nil, 0, e
	}
	return t, 202, nil
}

func (a *Agent) handleSendFromPath(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.SendFromPathRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if !filepath.IsAbs(req.Path) || strings.ContainsRune(req.Path, 0) {
		return nil, 0, invalid("path must be absolute", []model.ValidationIssue{{Path: "path", Code: "V_FORMAT", Message: "absolute path", Severity: "error"}})
	}
	if len([]rune(req.Note)) > 500 {
		return nil, 0, invalid("note: at most 500 characters", nil)
	}
	p := filepath.Clean(req.Path)
	// §21 #100 safety policy (requested path and its resolved target)
	if why := a.sendBlockReason(p); why != "" {
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
		return nil, 0, invalid("folders cannot be sent — pick a file (or make a .tar.gz of the folder on the server first)", []model.ValidationIssue{{Path: "path", Code: "V_TYPE", Message: "a folder", Severity: "error"}})
	}
	if !st.Mode().IsRegular() {
		return nil, 0, invalid("not a regular file", nil)
	}
	if st.Size() > model.TransferMax {
		return nil, 0, herr(model.ErrTooLarge, "at most 2 GiB", nil)
	}
	tmp := filepath.Join(a.dataDir, "tmp", "send-"+newID())
	if err := copyFile(p, tmp); err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, fs.ErrPermission) {
			return nil, 0, herr(model.ErrForbidden, "the agent (user "+processUser()+") cannot read "+p, map[string]any{"path": p})
		}
		return nil, 0, invalid("cannot read the file: "+err.Error(), nil)
	}
	sum, _ := fileSha256(tmp)
	size := st.Size()
	if fi, err := os.Stat(tmp); err == nil {
		size = fi.Size() // the copy is what is sent (the source may have changed meanwhile)
	}
	if size > model.TransferMax {
		_ = os.Remove(tmp)
		return nil, 0, herr(model.ErrTooLarge, "at most 2 GiB", nil)
	}
	t, e := a.newOutgoing(rc, tmp, filepath.Base(p), req.Note, size, sum, p)
	if e != nil {
		return nil, 0, e
	}
	return t, 202, nil
}

func (a *Agent) handleSaveTransfer(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	id := r.PathValue("transferId")
	var req model.SaveTransferRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	t, lp, ok := a.transfer(id)
	if !ok || t.Direction != "to-site" {
		return nil, 0, herr(model.ErrNotFound, "no received file "+id, nil)
	}
	if lp == "" {
		return nil, 0, herr(model.ErrUnavailable, "the file is still downloading", nil)
	}
	if !filepath.IsAbs(req.Path) {
		return nil, 0, invalid("path must be absolute", []model.ValidationIssue{{Path: "path", Code: "V_FORMAT", Message: "absolute path", Severity: "error"}})
	}
	if st, err := os.Stat(filepath.Dir(req.Path)); err != nil || !st.IsDir() {
		return nil, 0, invalid("the parent directory does not exist", nil)
	}
	if _, err := os.Lstat(req.Path); err == nil {
		return nil, 0, herr(model.ErrConflict, req.Path+" already exists", nil)
	}
	tmp := req.Path + ".opm-" + newID()
	if err := copyFile(lp, tmp); err != nil {
		return nil, 0, invalid("cannot write: "+err.Error(), nil)
	}
	_ = os.Chmod(tmp, 0o640)
	if os.Geteuid() == 0 {
		_ = os.Chown(tmp, 0, 0)
	}
	if err := os.Rename(tmp, req.Path); err != nil {
		os.Remove(tmp)
		return nil, 0, invalid("cannot write: "+err.Error(), nil)
	}
	t.Status = "saved-on-server"
	t.SavedPath = &req.Path
	a.putTransfer(t, "")
	a.enqueue("transfer-status", "transfer:"+id, "POST", "/v1/agent/transfers/"+id+"/status", model.AgentTransferStatusRequest{Status: "saved-on-server", SavedPath: req.Path, Actor: rc.actor})
	a.activity(rc.actorPtr(), "agent", "transfer.saved", "file "+t.FileName+" saved to "+req.Path, actorData(rc, map[string]any{"transferId": id, "direction": "to-site", "fileName": t.FileName, "savedPath": req.Path}))
	a.withDelivery(t, lp)
	return t, 200, nil
}

func (a *Agent) handleDownloadTransfer(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	id := r.PathValue("transferId")
	t, lp, ok := a.transfer(id)
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "no transfer "+id, nil)
	}
	if lp == "" {
		return nil, 0, herr(model.ErrUnavailable, "the file is not on this site (yet)", nil)
	}
	f, err := os.Open(lp)
	if err != nil {
		return nil, 0, herr(model.ErrNotFound, err.Error(), nil)
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", t.FileName))
	http.ServeContent(w, r, t.FileName, st.ModTime(), f)
	if t.Direction == "to-site" && r.Header.Get("Range") == "" {
		if t.Status == "ready" {
			t.Status = "downloaded"
			a.putTransfer(t, "")
			a.enqueue("transfer-status", "transfer:"+id, "POST", "/v1/agent/transfers/"+id+"/status", model.AgentTransferStatusRequest{Status: "downloaded", Actor: rc.actor})
		}
		a.activity(rc.actorPtr(), "agent", "transfer.received", "file "+t.FileName+" downloaded in the browser", actorData(rc, map[string]any{"transferId": id, "direction": "to-site", "fileName": t.FileName, "sizeBytes": t.SizeBytes}))
	}
	return nil, 0, nil
}
