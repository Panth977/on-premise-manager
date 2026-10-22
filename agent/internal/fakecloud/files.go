package fakecloud

// §21 #123 shared files (enabled by EnableFiles so the legacy transfer tests keep their cloud view).

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"opm/agent/internal/model"
)

type fileState struct {
	files    map[string]*model.SharedFile
	data     map[string][]byte
	on       bool
	lastSeen map[string]time.Time
}

var (
	reFileHeld     = regexp.MustCompile(`^/v1/agent/files/([^/]+)/held$`)
	reFileUpload   = regexp.MustCompile(`^/v1/agent/files/([^/]+)/upload$`)
	reFileFetch    = regexp.MustCompile(`^/v1/agent/files/([^/]+)/fetch$`)
	reFileDownload = regexp.MustCompile(`^/v1/agent/files/([^/]+)/download$`)
	reFileUnpub    = regexp.MustCompile(`^/v1/agent/files/([^/]+)/unpublish$`)
)

func (c *Cloud) fs() *fileState {
	if c.filesSt == nil {
		c.filesSt = &fileState{files: map[string]*model.SharedFile{}, data: map[string][]byte{}, lastSeen: map[string]time.Time{}}
	}
	return c.filesSt
}

// EnableFiles makes the cloud view carry files / fileFetches (a new cloud).
func (c *Cloud) EnableFiles() {
	c.mu.Lock()
	c.fs().on = true
	c.epoch++
	c.mu.Unlock()
}

// PublishFile publishes a file in the cloud (as a console browser upload).
func (c *Cloud) PublishFile(name string, data []byte) model.SharedFile {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := randID()
	f := &model.SharedFile{FileID: id, FileName: name, SizeBytes: int64(len(data)), Sha256: "sha256:" + sha256Hex(data), PublishedAt: nowTs(), PublishedBy: "console@acme.example",
		Origin: model.SharedFileOrigin{Kind: "cloud"}, State: "ready", Cloud: model.SharedFileCloud{Stored: true}, Sites: []string{}}
	c.fs().files[id] = f
	c.fs().data[id] = append([]byte(nil), data...)
	c.epoch++
	return *f
}

// File returns a copy of a file's record.
func (c *Cloud) File(id string) (model.SharedFile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.fs().files[id]
	if !ok {
		return model.SharedFile{}, false
	}
	return *f, true
}

// FileData returns the cloud copy.
func (c *Cloud) FileData(id string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fs().data[id]
}

// RequestFileToCloud is the console's "Get into the cloud": the first holding site is asked to upload.
func (c *Cloud) RequestFileToCloud(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestFetch(id, "console@acme.example", "")
}

func (c *Cloud) requestFetch(id, by, exclude string) bool {
	f, ok := c.fs().files[id]
	if !ok || f.Cloud.Stored || f.Cloud.Fetch != nil {
		return ok
	}
	var from *string
	for _, s := range f.Sites {
		if s != exclude {
			x := s
			from = &x
			break
		}
	}
	if from == nil {
		return false
	}
	f.Cloud.Fetch = &model.SharedFileCloudFetch{RequestedAt: nowTs(), RequestedBy: by, From: from}
	c.epoch++
	return true
}

func (c *Cloud) viewFiles(v *model.CloudView) {
	st := c.fs()
	if !st.on {
		return
	}
	v.Files = []model.SharedFile{}
	v.FileFetches = []model.FileFetchItem{}
	ids := make([]string, 0, len(st.files))
	for id := range st.files {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		f := st.files[id]
		v.Files = append(v.Files, *f)
		if f.Cloud.Fetch != nil && f.Cloud.Fetch.From != nil && !f.Cloud.Stored && f.State == "ready" {
			v.FileFetches = append(v.FileFetches, model.FileFetchItem{FileID: id, From: *f.Cloud.Fetch.From})
		}
	}
}

func (c *Cloud) routeFiles(w http.ResponseWriter, r *http.Request, body []byte, rc reqCtx) bool {
	p, m := r.URL.Path, r.Method
	var g []string
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.fs()
	get := func(id string) *model.SharedFile {
		f, ok := st.files[id]
		if !ok {
			writeErr(w, model.ErrNotFound, "file "+id+" not found", nil)
			return nil
		}
		return f
	}
	switch {
	case m == "POST" && p == "/v1/agent/files":
		var req model.AgentRegisterFileRequest
		if json.Unmarshal(body, &req) != nil || req.FileID == "" {
			writeErr(w, model.ErrInvalid, "bad request", nil)
			return true
		}
		f, ok := st.files[req.FileID]
		if !ok {
			f = &model.SharedFile{FileID: req.FileID, FileName: req.FileName, SizeBytes: req.SizeBytes, Sha256: req.Sha256, Note: req.Note, PublishedAt: req.PublishedAt, PublishedBy: req.PublishedBy,
				Origin: model.SharedFileOrigin{Kind: "site", SiteSlug: rc.signer, Via: req.Via, SourcePath: req.SourcePath}, State: "ready", Sites: []string{rc.signer}}
			st.files[req.FileID] = f
			c.epoch++
		}
		writeJSON(w, 200, f)
	case m == "POST" && match(reFileHeld, p, &g):
		f := get(g[1])
		if f == nil {
			return true
		}
		var req model.AgentFileHeldRequest
		_ = json.Unmarshal(body, &req)
		out := []string{}
		for _, s := range f.Sites {
			if s != rc.signer {
				out = append(out, s)
			}
		}
		if req.Held {
			out = append(out, rc.signer)
		}
		f.Sites = out
		c.epoch++
		writeJSON(w, 200, map[string]bool{"ok": true})
	case m == "POST" && match(reFileFetch, p, &g):
		f := get(g[1])
		if f == nil {
			return true
		}
		if f.State == "unpublished" {
			writeErr(w, model.ErrGone, "unpublished", nil)
			return true
		}
		if !c.requestFetch(g[1], "site:"+rc.signer, rc.signer) && !f.Cloud.Stored {
			writeErr(w, model.ErrConflict, "no site holds it", nil)
			return true
		}
		writeJSON(w, 200, f)
	case m == "POST" && match(reFileUpload, p, &g):
		f := get(g[1])
		if f == nil {
			return true
		}
		uid := randID()
		count := int((f.SizeBytes + model.UploadPartSize - 1) / model.UploadPartSize)
		if count < 1 {
			count = 1
		}
		dep := c.DeploymentID
		id := f.FileID
		now := nowTs()
		up := &upload{creator: rc.signer, parts: map[int][]byte{}, up: model.Upload{UploadID: uid, Purpose: "file", DeploymentID: &dep, TransferID: &id, SizeBytes: f.SizeBytes, Sha256: f.Sha256,
			PartSize: model.UploadPartSize, PartCount: count, PartsReceived: []int{}, Status: "open", CreatedAt: now, CreatedBy: rc.signer, ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(tsLayout)}}
		c.uploads[uid] = up
		if f.Cloud.Fetch != nil {
			f.Cloud.Fetch.StartedAt = &now
		}
		writeJSON(w, 200, model.AgentFileUploadResponse{Upload: up.up})
	case m == "GET" && match(reFileDownload, p, &g):
		f := get(g[1])
		if f == nil {
			return true
		}
		d, ok := st.data[g[1]]
		if !ok || !f.Cloud.Stored {
			writeErr(w, model.ErrConflict, "no cloud copy", nil)
			return true
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(d)))
		w.WriteHeader(200)
		_, _ = w.Write(d)
	case m == "POST" && match(reFileUnpub, p, &g):
		f := get(g[1])
		if f == nil {
			return true
		}
		if f.State != "unpublished" {
			now := nowTs()
			var req model.AgentFileActorRequest
			_ = json.Unmarshal(body, &req)
			f.State, f.UnpublishedAt, f.UnpublishedBy = "unpublished", &now, &req.Actor
			f.Cloud = model.SharedFileCloud{}
			delete(st.data, g[1])
			c.epoch++
		}
		writeJSON(w, 200, model.Deleted{Deleted: true})
	default:
		return false
	}
	return true
}

// completeFileUpload: an upload of purpose file finished (called under c.mu).
func (c *Cloud) completeFileUpload(id string, data []byte) {
	st := c.fs()
	f, ok := st.files[id]
	if !ok || f.State == "unpublished" {
		return
	}
	st.data[id] = append([]byte(nil), data...)
	f.Cloud = model.SharedFileCloud{Stored: true}
	c.epoch++
}
