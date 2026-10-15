package tools

// §21 #96 volume manager — file operations inside a volume (edit a text file, upload, create a folder, rename / move,
// delete). Paths are relative to the volume root ("/" separated); no step may pass through a symlink (a link is
// handled as itself, never followed); the volume root itself can never be moved or deleted (Empty does that). Writes
// are refused (409 locked) while an update runs or a volume action (empty / copy / restore) holds the volume. New files
// and folders get the owner of their parent folder so the container that owns the data can use them.

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const uploadMax = 2 << 30

func init() { rateLimits["volwrite"] = 120 }

// forgetSizes drops the cached sizes of a volume (after a write).
func forgetSizes(root string) {
	sizeMu.Lock()
	for k := range sizeCache {
		if k == root || strings.HasPrefix(k, root+string(filepath.Separator)) {
			delete(sizeCache, k)
		}
	}
	sizeMu.Unlock()
}

// ForgetVolumeSizes is used by the agent after a volume action.
func ForgetVolumeSizes(root string) { forgetSizes(root) }

// noSymlinkBetween: every existing component from root (exclusive) to abs's parent (inclusive) is a real folder.
func noSymlinkBetween(root, abs string) *Error {
	rel, err := filepath.Rel(root, filepath.Dir(abs))
	if err != nil || strings.HasPrefix(rel, "..") {
		return invalid("path escapes the volume")
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		st, err := os.Lstat(cur)
		if err != nil {
			return nil // the rest does not exist yet (created by mkdir -p)
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return invalid("path passes through the symlink %s", strings.TrimPrefix(cur, root))
		}
		if !st.IsDir() {
			return invalid("%s is not a folder", strings.TrimPrefix(cur, root))
		}
	}
	return nil
}

func ownerOf(p string) (int, int, bool) {
	st, err := os.Lstat(p)
	if err != nil {
		return 0, 0, false
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(s.Uid), int(s.Gid), true
	}
	return 0, 0, false
}

// inheritOwner chowns p to the owner of its parent (best effort: an agent that is not root keeps its own uid).
func inheritOwner(p string) {
	if uid, gid, ok := ownerOf(filepath.Dir(p)); ok {
		_ = os.Lchown(p, uid, gid)
	}
}

// mkdirAllOwned creates the missing folders up to dir, each owned like its parent.
func mkdirAllOwned(root, dir string) error {
	if st, err := os.Lstat(dir); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s is not a folder", strings.TrimPrefix(dir, root))
		}
		return nil
	}
	if dir == root || len(dir) < len(root) {
		return fmt.Errorf("the volume folder is missing")
	}
	if err := mkdirAllOwned(root, filepath.Dir(dir)); err != nil {
		return err
	}
	mode := fs.FileMode(0o755)
	if st, err := os.Stat(filepath.Dir(dir)); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.Mkdir(dir, mode); err != nil && !os.IsExist(err) {
		return err
	}
	inheritOwner(dir)
	return nil
}

// writeTarget resolves a path for writing in a volume (dir volumes: not the root; file volumes: only the root).
func (s *Service) writeTarget(r *http.Request, c Caller, p string, allowRoot bool) (*Volume, string, string, *Error) {
	if e := s.rl.allow("volwrite", c); e != nil {
		return nil, "", "", e
	}
	return s.writeTargetNoRate(r, p, allowRoot)
}

func (s *Service) writeTargetNoRate(r *http.Request, p string, allowRoot bool) (*Volume, string, string, *Error) {
	v, e := s.volume(r.PathValue("volumeSlug"))
	if e != nil {
		return nil, "", "", e
	}
	if why := s.h.VolumeBusy(v.Slug); why != "" {
		return nil, "", "", errf(409, "locked", "volume %s is busy: %s", v.Slug, why)
	}
	abs, rel, e := resolve(v.Path, p)
	if e != nil {
		return nil, "", "", e
	}
	if orStr(v.Kind, "dir") == "file" {
		if rel != "/" || !allowRoot {
			return nil, "", "", invalid("volume %s is a single file: only its content can be replaced (or use Empty)", v.Slug)
		}
		return v, v.Path, rel, nil
	}
	if rel == "/" {
		return nil, "", "", invalid("not the volume folder itself (use Empty / Restore for the whole volume)")
	}
	if st, err := os.Stat(v.Path); err != nil || !st.IsDir() {
		return nil, "", "", notFound("the folder of volume %s does not exist yet", v.Slug)
	}
	if e := noSymlinkBetween(v.Path, abs); e != nil {
		return nil, "", "", e
	}
	return v, abs, rel, nil
}

func (s *Service) entryOf(abs, rel string) map[string]any {
	st, err := os.Lstat(abs)
	if err != nil {
		return nil
	}
	return s.entry(abs, rel, st, time.Now().Add(time.Second))
}

// atomicWrite writes src into abs via a temp file in the same folder: an existing file keeps its mode + owner, a new
// one gets mode 0644 and the parent's owner.
func atomicWrite(abs string, src io.Reader, limit int64) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".opm-upload-*")
	if err != nil {
		return 0, err
	}
	n, cerr := io.Copy(tmp, io.LimitReader(src, limit+1))
	if cerr == nil && n > limit {
		cerr = errTooLarge
	}
	if cerr == nil {
		cerr = tmp.Sync()
	}
	tmp.Close()
	if cerr != nil {
		os.Remove(tmp.Name())
		return n, cerr
	}
	if st, err := os.Lstat(abs); err == nil && st.Mode().IsRegular() {
		_ = os.Chmod(tmp.Name(), st.Mode().Perm())
		if uid, gid, ok := ownerOf(abs); ok {
			_ = os.Lchown(tmp.Name(), uid, gid)
		}
	} else {
		_ = os.Chmod(tmp.Name(), 0o644)
		inheritOwner(tmp.Name())
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		os.Remove(tmp.Name())
		return n, err
	}
	return n, nil
}

var errTooLarge = fmt.Errorf("too large")

func (s *Service) handleVolumeWrite(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	var req struct {
		Path      string  `json:"path"`
		Text      *string `json:"text"`
		BaseMtime *string `json:"baseMtime"`
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20)) // 1 MiB of text may be ~6 MiB of JSON escapes
	if err != nil {
		return nil, 0, invalid("read body: %s", err.Error())
	}
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, 0, invalid("invalid JSON body: %s", err.Error())
	}
	if req.Text == nil {
		return nil, 0, invalid("text is required")
	}
	if len(*req.Text) > viewMax {
		return nil, 0, errf(413, "too-large", "text files up to 1 MiB can be edited here; upload bigger files")
	}
	if !utf8.ValidString(*req.Text) {
		return nil, 0, invalid("text must be UTF-8")
	}
	v, abs, rel, e := s.writeTarget(r, c, req.Path, true)
	if e != nil {
		return nil, 0, e
	}
	st, err := os.Lstat(abs)
	exists := err == nil
	switch {
	case exists && !st.Mode().IsRegular():
		return nil, 0, errf(409, "conflict", "%s exists and is not a regular file", rel)
	case req.BaseMtime == nil && exists:
		return nil, 0, &Error{Status: 409, Code: "conflict", Msg: fmt.Sprintf("%s already exists", rel), Details: map[string]any{"mtime": ts(st.ModTime())}}
	case req.BaseMtime != nil && !exists:
		return nil, 0, errf(409, "conflict", "%s was deleted since you opened it", rel)
	case req.BaseMtime != nil && ts(st.ModTime()) != *req.BaseMtime:
		return nil, 0, &Error{Status: 409, Code: "conflict", Msg: fmt.Sprintf("%s changed since you opened it (reload to see the new content)", rel), Details: map[string]any{"mtime": ts(st.ModTime())}}
	}
	if !exists {
		if err := mkdirAllOwned(v.Path, filepath.Dir(abs)); err != nil {
			return nil, 0, invalid("%s", err.Error())
		}
	}
	n, err := atomicWrite(abs, strings.NewReader(*req.Text), viewMax)
	if err != nil {
		return nil, 0, errf(403, "forbidden", "cannot write %s: %s", rel, err.Error())
	}
	forgetSizes(v.Path)
	s.activity(c, "volume.file-written", fmt.Sprintf("%s:%s %s (%d bytes) on %s", v.Slug, rel, map[bool]string{true: "created", false: "edited"}[!exists], n, s.h.Site()),
		map[string]any{"volumeSlug": v.Slug, "path": rel, "sizeBytes": n, "created": !exists, "siteSlug": s.h.Site()})
	return map[string]any{"entry": s.entryOf(abs, rel)}, 200, nil
}

func (s *Service) handleVolumeUpload(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	q := r.URL.Query()
	v, abs, rel, e := s.writeTarget(r, c, q.Get("path"), true)
	if e != nil {
		return nil, 0, e
	}
	if r.ContentLength > uploadMax {
		return nil, 0, errf(413, "too-large", "files up to 2 GiB can be uploaded here")
	}
	overwrite := q.Get("overwrite") == "1" || q.Get("overwrite") == "true"
	st, err := os.Lstat(abs)
	exists := err == nil
	if exists && (!st.Mode().IsRegular() || !overwrite) {
		return nil, 0, &Error{Status: 409, Code: "conflict", Msg: fmt.Sprintf("%s already exists", rel), Details: map[string]any{"type": map[bool]string{true: "file", false: "other"}[st.Mode().IsRegular()]}}
	}
	if !exists {
		if err := mkdirAllOwned(v.Path, filepath.Dir(abs)); err != nil {
			return nil, 0, invalid("%s", err.Error())
		}
	}
	n, err := atomicWrite(abs, r.Body, uploadMax)
	if err == errTooLarge {
		return nil, 0, errf(413, "too-large", "files up to 2 GiB can be uploaded here")
	}
	if err != nil {
		return nil, 0, errf(403, "forbidden", "cannot write %s: %s", rel, err.Error())
	}
	forgetSizes(v.Path)
	s.activity(c, "volume.file-uploaded", fmt.Sprintf("%s:%s uploaded (%d bytes) on %s", v.Slug, rel, n, s.h.Site()),
		map[string]any{"volumeSlug": v.Slug, "path": rel, "sizeBytes": n, "overwritten": exists, "siteSlug": s.h.Site()})
	return map[string]any{"entry": s.entryOf(abs, rel)}, 200, nil
}

func (s *Service) handleVolumeMkdir(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	var req struct {
		Path string `json:"path"`
	}
	if e := readBody(r, &req); e != nil {
		return nil, 0, e
	}
	v, abs, rel, e := s.writeTarget(r, c, req.Path, false)
	if e != nil {
		return nil, 0, e
	}
	if _, err := os.Lstat(abs); err == nil {
		return nil, 0, errf(409, "conflict", "%s already exists", rel)
	}
	if err := mkdirAllOwned(v.Path, abs); err != nil {
		return nil, 0, invalid("%s", err.Error())
	}
	s.activity(c, "volume.file-folder-created", fmt.Sprintf("%s:%s folder created on %s", v.Slug, rel, s.h.Site()), map[string]any{"volumeSlug": v.Slug, "path": rel, "siteSlug": s.h.Site()})
	return map[string]any{"entry": s.entryOf(abs, rel)}, 200, nil
}

func (s *Service) handleVolumeMove(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	var req struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Overwrite bool   `json:"overwrite"`
	}
	if e := readBody(r, &req); e != nil {
		return nil, 0, e
	}
	v, from, fromRel, e := s.writeTarget(r, c, req.From, false)
	if e != nil {
		return nil, 0, e
	}
	to, toRel, e := resolve(v.Path, req.To)
	if e != nil {
		return nil, 0, e
	}
	if toRel == "/" {
		return nil, 0, invalid("not onto the volume folder itself")
	}
	if e := noSymlinkBetween(v.Path, to); e != nil {
		return nil, 0, e
	}
	fst, err := os.Lstat(from)
	if err != nil {
		return nil, 0, notFound("%s does not exist", fromRel)
	}
	if toRel == fromRel {
		return nil, 0, invalid("the new path is the same")
	}
	if fst.IsDir() && strings.HasPrefix(toRel+"/", fromRel+"/") {
		return nil, 0, invalid("a folder cannot move into itself")
	}
	overwritten := false
	if tst, err := os.Lstat(to); err == nil {
		if tst.IsDir() || !req.Overwrite || fst.IsDir() {
			return nil, 0, errf(409, "conflict", "%s already exists", toRel)
		}
		overwritten = true
	}
	if err := mkdirAllOwned(v.Path, filepath.Dir(to)); err != nil {
		return nil, 0, invalid("%s", err.Error())
	}
	if err := os.Rename(from, to); err != nil {
		return nil, 0, errf(403, "forbidden", "cannot move: %s", err.Error())
	}
	forgetSizes(v.Path)
	s.activity(c, "volume.file-moved", fmt.Sprintf("%s:%s → %s on %s", v.Slug, fromRel, toRel, s.h.Site()),
		map[string]any{"volumeSlug": v.Slug, "from": fromRel, "to": toRel, "overwritten": overwritten, "siteSlug": s.h.Site()})
	return map[string]any{"entry": s.entryOf(to, toRel)}, 200, nil
}

func (s *Service) handleVolumeDelete(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	var req struct {
		Paths   []string `json:"paths"`
		Confirm string   `json:"confirm"`
	}
	if e := readBody(r, &req); e != nil {
		return nil, 0, e
	}
	if req.Confirm != "DELETE" {
		return nil, 0, invalid(`type DELETE to confirm`)
	}
	if len(req.Paths) == 0 || len(req.Paths) > 1000 {
		return nil, 0, invalid("1–1000 paths")
	}
	var v *Volume
	type item struct{ abs, rel string }
	var items []item
	if e := s.rl.allow("volwrite", c); e != nil {
		return nil, 0, e
	}
	for _, p := range req.Paths {
		vv, abs, rel, e := s.writeTargetNoRate(r, p, false)
		if e != nil {
			return nil, 0, e
		}
		if _, err := os.Lstat(abs); err != nil {
			return nil, 0, notFound("%s does not exist", rel)
		}
		v = vv
		items = append(items, item{abs, rel})
	}
	n := 0
	var rels []string
	for _, it := range items {
		_ = filepath.WalkDir(it.abs, func(string, fs.DirEntry, error) error { n++; return nil })
		if err := os.RemoveAll(it.abs); err != nil {
			return nil, 0, errf(403, "forbidden", "cannot delete %s: %s", it.rel, err.Error())
		}
		rels = append(rels, it.rel)
	}
	forgetSizes(v.Path)
	sum := fmt.Sprintf("%s: %d entries deleted (%s) on %s", v.Slug, n, strings.Join(rels, ", "), s.h.Site())
	if len(sum) > 200 {
		sum = fmt.Sprintf("%s: %d entries deleted in %d paths on %s", v.Slug, n, len(rels), s.h.Site())
	}
	s.activity(c, "volume.file-deleted", sum, map[string]any{"volumeSlug": v.Slug, "paths": rels, "entries": n, "siteSlug": s.h.Site()})
	return map[string]any{"deleted": n}, 200, nil
}
