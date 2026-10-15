package agent

// §21 #107 (agent 0.15.1): ONE Commit = ONE transaction, including the bytes. The agent GUI keeps new File content in
// the browser; at Commit it opens a stage (POST /_opm/api/stages → which blobs this agent is missing), uploads only
// those (PUT /_opm/api/stages/{id}/blobs/{sha256}, verified) into <dataDir>/staging/<stageId>/<hex>, then commits
// { message, baseCommitId, config, stageId }. commitEdits verifies every NEW blob (config vs head) is in the blob store
// or the stage (size + sha256; else 422 V_BLOB_MISSING details.missing), PROMOTES the staged blobs into the blob store
// (the same path a local upload takes, so peers fetch them) before the apply engine stages anything, and removes the
// stage whatever the outcome. Nothing reaches the blob store before Commit. Stages older than 24 h are removed at
// start and by the hourly housekeeping.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/model"
)

var reStageID = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
var reHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

const stageBlobMax = 50 << 20
const stageArchiveMax = 200 << 20

type stageMeta struct {
	StageID   string           `json:"stageId"`
	CreatedAt string           `json:"createdAt"`
	ExpiresAt string           `json:"expiresAt"`
	SessionID string           `json:"sessionId"`
	Actor     string           `json:"actor"`
	Blobs     []model.BlobNeed `json:"blobs"`
}

func (a *Agent) stagingDir() string            { return filepath.Join(a.dataDir, "staging") }
func (a *Agent) stageDir(id string) string     { return filepath.Join(a.stagingDir(), id) }
func (a *Agent) stageFile(id, h string) string { return filepath.Join(a.stageDir(id), h) }

func hexOfSha(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "sha256:")
}

func (a *Agent) loadStage(id string) (*stageMeta, *httpError) {
	if !reStageID.MatchString(id) {
		return nil, herr(model.ErrNotFound, "stage not found", nil)
	}
	b, err := os.ReadFile(filepath.Join(a.stageDir(id), "stage.json"))
	if err != nil {
		return nil, herr(model.ErrNotFound, "stage not found", nil)
	}
	var m stageMeta
	if json.Unmarshal(b, &m) != nil {
		return nil, herr(model.ErrNotFound, "stage not found", nil)
	}
	return &m, nil
}

func (a *Agent) stageOwned(r *http.Request, id string) (*stageMeta, *httpError) {
	m, he := a.loadStage(id)
	if he != nil {
		return nil, he
	}
	rc := getRC(r)
	if m.SessionID != "" && rc.sessionID != "" && m.SessionID != rc.sessionID {
		return nil, herr(model.ErrNotFound, "stage not found", nil)
	}
	if t, err := time.Parse(time.RFC3339Nano, m.ExpiresAt); err == nil && time.Now().After(t) {
		return nil, herr(model.ErrGone, "the stage expired — commit again", nil)
	}
	return m, nil
}

func (a *Agent) handleCreateStage(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req model.CreateStageRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if len(req.Blobs) > model.StageMaxBlobs {
		return nil, 0, invalid("too many blobs in one commit", []model.ValidationIssue{{Path: "blobs", Code: "V_TOO_MANY", Message: "at most 5000 new blobs per commit", Severity: "error"}})
	}
	var total int64
	var issues []model.ValidationIssue
	seen := map[string]bool{}
	var blobs []model.BlobNeed
	for i, b := range req.Blobs {
		h := hexOfSha(b.Sha256)
		if !reHex64.MatchString(h) || !strings.HasPrefix(b.Sha256, "sha256:") {
			issues = append(issues, model.ValidationIssue{Path: "blobs[" + itoa(i) + "].sha256", Code: "V_FORMAT", Message: `sha256 must be "sha256:" + 64 hex`, Severity: "error"})
			continue
		}
		if b.SizeBytes < 0 || b.SizeBytes > stageArchiveMax {
			issues = append(issues, model.ValidationIssue{Path: "blobs[" + itoa(i) + "].sizeBytes", Code: "V_RANGE", Message: "sizeBytes must be 0–209715200", Severity: "error"})
			continue
		}
		if seen[h] {
			continue
		}
		seen[h] = true
		total += b.SizeBytes
		blobs = append(blobs, model.BlobNeed{Sha256: "sha256:" + h, SizeBytes: b.SizeBytes})
	}
	if len(issues) > 0 {
		return nil, 0, invalid("bad blob list: "+issues[0].Message, issues)
	}
	if total > model.StageMaxBytes {
		return nil, 0, herr(model.ErrTooLarge, "at most 1 GiB of new content per commit", nil)
	}
	rc := getRC(r)
	now := time.Now()
	m := stageMeta{StageID: newID(), CreatedAt: tsOf(now), ExpiresAt: tsOf(now.Add(model.StageTTLSeconds * time.Second)), SessionID: rc.sessionID, Actor: rc.actor, Blobs: blobs}
	if m.Blobs == nil {
		m.Blobs = []model.BlobNeed{}
	}
	if err := os.MkdirAll(a.stageDir(m.StageID), 0o700); err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(a.stageDir(m.StageID), "stage.json"), b, 0o600); err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	out := model.Stage{StageID: m.StageID, ExpiresAt: m.ExpiresAt, Missing: []model.BlobNeed{}}
	for _, x := range blobs {
		if a.haveBlob(hexOfSha(x.Sha256)) {
			out.Present++
		} else {
			out.Missing = append(out.Missing, x)
		}
	}
	return out, 201, nil
}

func (a *Agent) handlePutStageBlob(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	m, he := a.stageOwned(r, r.PathValue("stageId"))
	if he != nil {
		return nil, 0, he
	}
	h := hexOfSha(r.PathValue("sha256"))
	if !reHex64.MatchString(h) {
		return nil, 0, invalid(`sha256 must be "sha256:" + 64 hex`, []model.ValidationIssue{{Path: "sha256", Code: "V_FORMAT", Message: "bad sha256", Severity: "error"}})
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, stageArchiveMax+1))
	if err != nil {
		return nil, 0, herr(model.ErrInvalid, err.Error(), nil)
	}
	if len(data) > stageArchiveMax {
		return nil, 0, herr(model.ErrTooLarge, "at most 200 MiB", nil)
	}
	sum := bytesSha256Hex(data)
	if sum != h {
		return nil, 0, invalid("sha256 mismatch: body is sha256:"+sum, []model.ValidationIssue{{Path: "sha256", Code: "V_FORMAT", Message: "sha256 mismatch: body is sha256:" + sum, Severity: "error"}})
	}
	if a.haveBlob(h) {
		return model.StageBlobResponse{Sha256: "sha256:" + h, SizeBytes: int64(len(data)), Staged: false}, 200, nil
	}
	tmp := a.stageFile(m.StageID, h) + ".part-" + newID()
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	if err := os.Rename(tmp, a.stageFile(m.StageID, h)); err != nil {
		_ = os.Remove(tmp)
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	return model.StageBlobResponse{Sha256: "sha256:" + h, SizeBytes: int64(len(data)), Staged: true}, 200, nil
}

func (a *Agent) handleDeleteStage(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("stageId")
	if _, he := a.loadStage(id); he != nil {
		return nil, 0, he
	}
	a.dropStage(id)
	return model.Deleted{Deleted: true}, 200, nil
}

func (a *Agent) dropStage(id string) {
	if id == "" || !reStageID.MatchString(id) {
		return
	}
	_ = os.RemoveAll(a.stageDir(id))
}

// cleanupStages removes stages older than the TTL (start + hourly housekeeping).
func (a *Agent) cleanupStages() {
	ents, _ := os.ReadDir(a.stagingDir())
	for _, e := range ents {
		i, err := e.Info()
		if err != nil {
			continue
		}
		old := time.Since(i.ModTime()) > model.StageTTLSeconds*time.Second
		if m, he := a.loadStage(e.Name()); he == nil {
			if t, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err == nil {
				old = time.Since(t) > model.StageTTLSeconds*time.Second
			}
		}
		if old {
			_ = os.RemoveAll(filepath.Join(a.stagingDir(), e.Name()))
		}
	}
}

// promoteStage checks that every NEW blob of cfg (not referenced by head) is in the blob store or the stage (size +
// sha256) and moves the staged ones into the blob store. 422 V_BLOB_MISSING (details.missing) otherwise.
func (a *Agent) promoteStage(stageID string, head *model.Config, cfg *model.Config) *httpError {
	needs := model.NewConfigBlobs(head, cfg)
	if stageID != "" {
		if _, he := a.loadStage(stageID); he != nil && len(needs) > 0 {
			stageID = "" // gone / expired: blobs must already be stored
		}
	}
	var missing []model.BlobNeed
	type promo struct {
		h    string
		path string
	}
	var todo []promo
	for _, n := range needs {
		h := hexOfSha(n.Sha256)
		if a.haveBlob(h) {
			continue
		}
		if stageID != "" {
			p := a.stageFile(stageID, h)
			if st, err := os.Stat(p); err == nil && st.Size() == n.SizeBytes {
				if got, err := fileSha256(p); err == nil && got == h {
					todo = append(todo, promo{h, p})
					continue
				}
			}
		}
		missing = append(missing, n)
	}
	if len(missing) > 0 {
		issues := make([]model.ValidationIssue, 0, len(missing))
		for _, m := range missing {
			issues = append(issues, model.ValidationIssue{Path: "config", Code: "V_BLOB_MISSING", Message: "content " + short(m.Sha256) + " was not uploaded (commit again)", Severity: "error"})
		}
		return herr(model.ErrInvalid, "the content of "+itoa(len(missing))+" file(s) was not uploaded — commit again", map[string]any{"errors": issues, "missing": missing})
	}
	for _, p := range todo {
		tmp := filepath.Join(a.dataDir, "tmp", "blob-"+newID())
		_ = os.MkdirAll(filepath.Dir(tmp), 0o755)
		if err := os.Rename(p.path, tmp); err != nil {
			return herr(model.ErrInternal, err.Error(), nil)
		}
		if err := a.storeBlobFile(tmp, p.h, "file", "local"); err != nil {
			return herr(model.ErrInternal, err.Error(), nil)
		}
	}
	return nil
}

func bytesSha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func itoa(n int) string { return strconv.Itoa(n) }
