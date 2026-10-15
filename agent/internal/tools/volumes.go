package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The volumes browser is READ-ONLY (§21 #89 e): list, view text ≤ 1 MiB, download a file or a tar.gz. Paths are
// relative to the volume root; ".." never escapes; symlinks are shown, never followed.

const (
	viewMax        = 1 << 20
	treeMaxEntries = 2000
	walkMaxEntries = 200000
	walkMaxTime    = 3 * time.Second
)

type sizeEntry struct {
	at        time.Time
	bytes     int64
	files     int64
	truncated bool
}

var (
	sizeMu    sync.Mutex
	sizeCache = map[string]sizeEntry{}
)

// dirSize walks a tree (no symlinks followed), capped by entries and time; cached 60 s.
func dirSize(root string) sizeEntry {
	sizeMu.Lock()
	if e, ok := sizeCache[root]; ok && time.Since(e.at) < 60*time.Second {
		sizeMu.Unlock()
		return e
	}
	sizeMu.Unlock()
	e := sizeEntry{at: time.Now()}
	start := time.Now()
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n++
		if n > walkMaxEntries || time.Since(start) > walkMaxTime {
			e.truncated = true
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				e.bytes += info.Size()
				e.files++
			}
		}
		return nil
	})
	sizeMu.Lock()
	sizeCache[root] = e
	sizeMu.Unlock()
	return e
}

func (s *Service) volume(slug string) (*Volume, *Error) {
	for _, v := range s.h.Volumes() {
		if v.Slug == slug {
			vv := v
			return &vv, nil
		}
	}
	return nil, notFound("no volume %q on site %s", slug, s.h.Site())
}

// resolve maps a request path to an absolute path inside the volume root.
func resolve(root, rel string) (string, string, *Error) {
	if strings.ContainsRune(rel, 0) {
		return "", "", invalid("path: invalid character")
	}
	clean := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	abs := filepath.Join(root, filepath.FromSlash(clean))
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", "", invalid("path escapes the volume")
	}
	return abs, clean, nil
}

func modeString(m fs.FileMode) string { return m.String() }

func (s *Service) handleVolumes(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	items := []map[string]any{}
	for _, v := range s.h.Volumes() {
		it := map[string]any{"siteSlug": s.h.Site(), "volumeSlug": v.Slug, "label": v.Label, "kind": orStr(v.Kind, "dir"), "hostPath": v.Path, "exists": false, "sizeBytes": nil,
			"fileCount": nil, "sizeTruncated": false, "usedBy": nonNil(v.UsedBy), "backup": v.Backup}
		if st, err := os.Lstat(v.Path); err == nil {
			it["exists"] = true
			if st.Mode().IsRegular() {
				it["sizeBytes"], it["fileCount"] = st.Size(), 1
			} else if st.IsDir() {
				e := dirSize(v.Path)
				it["sizeBytes"], it["fileCount"], it["sizeTruncated"] = e.bytes, e.files, e.truncated
			}
		}
		items = append(items, it)
	}
	return map[string]any{"items": items}, 200, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func isTextLike(p string, size int64) bool {
	if size > viewMax {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 8192)
	n, _ := io.ReadFull(f, b)
	b = b[:n]
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	// tolerate a multi-byte rune cut at the 8 KiB boundary
	for i := 0; i < 4 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return utf8.Valid(b)
}

// entry describes one item; dir sizes are computed while the request budget lasts (else null).
func (s *Service) entry(abs, rel string, info fs.FileInfo, deadline time.Time) map[string]any {
	typ := "other"
	var size any = int64(0)
	var link *string
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		typ = "symlink"
		if t, err := os.Readlink(abs); err == nil {
			link = &t
		}
	case info.IsDir():
		typ = "dir"
		size = nil
		if time.Now().Before(deadline) {
			size = dirSize(abs).bytes
		}
	case info.Mode().IsRegular():
		typ = "file"
		size = info.Size()
	}
	return map[string]any{"name": info.Name(), "path": rel, "type": typ, "sizeBytes": size, "mtime": ts(info.ModTime()),
		"mode": modeString(info.Mode()), "linkTarget": link, "textLike": typ == "file" && isTextLike(abs, info.Size())}
}

func (s *Service) handleVolumeTree(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	v, e := s.volume(r.PathValue("volumeSlug"))
	if e != nil {
		return nil, 0, e
	}
	abs, rel, e := resolve(v.Path, r.URL.Query().Get("path"))
	if e != nil {
		return nil, 0, e
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return nil, 0, notFound("%s does not exist in volume %s", rel, v.Slug)
	}
	entries := []map[string]any{}
	truncated := false
	if !st.IsDir() { // a single-file volume (or a file path): one entry
		entries = append(entries, s.entry(abs, rel, st, time.Now().Add(2*time.Second)))
		return map[string]any{"volumeSlug": v.Slug, "path": rel, "entries": entries, "truncated": false}, 200, nil
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, 0, errf(403, "forbidden", "cannot read %s: %s", rel, err.Error())
	}
	sort.Slice(ents, func(i, j int) bool {
		if ents[i].IsDir() != ents[j].IsDir() {
			return ents[i].IsDir()
		}
		return strings.ToLower(ents[i].Name()) < strings.ToLower(ents[j].Name())
	})
	deadline := time.Now().Add(2 * time.Second)
	for i, de := range ents {
		if i >= treeMaxEntries {
			truncated = true
			break
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, s.entry(filepath.Join(abs, de.Name()), path.Join(rel, de.Name()), info, deadline))
	}
	return map[string]any{"volumeSlug": v.Slug, "path": rel, "entries": entries, "truncated": truncated}, 200, nil
}

func (s *Service) handleVolumeFile(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	v, e := s.volume(r.PathValue("volumeSlug"))
	if e != nil {
		return nil, 0, e
	}
	abs, rel, e := resolve(v.Path, r.URL.Query().Get("path"))
	if e != nil {
		return nil, 0, e
	}
	st, err := os.Lstat(abs)
	if err != nil || !st.Mode().IsRegular() {
		return nil, 0, notFound("%s is not a file in volume %s", rel, v.Slug)
	}
	out := map[string]any{"volumeSlug": v.Slug, "path": rel, "sizeBytes": st.Size(), "mtime": ts(st.ModTime()), "binary": false, "text": nil}
	if st.Size() > viewMax {
		return out, 200, nil
	}
	if !isTextLike(abs, st.Size()) {
		out["binary"] = true
		return out, 200, nil
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, 0, errf(403, "forbidden", "cannot read %s: %s", rel, err.Error())
	}
	out["text"] = MaskString(string(b), s.h.SecretValues())
	return out, 200, nil
}

func (s *Service) handleVolumeTicket(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	v, e := s.volume(r.PathValue("volumeSlug"))
	if e != nil {
		return nil, 0, e
	}
	abs, rel, e := resolve(v.Path, r.URL.Query().Get("path"))
	if e != nil {
		return nil, 0, e
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return nil, 0, notFound("%s does not exist in volume %s", rel, v.Slug)
	}
	if e := s.rl.allow("download", c); e != nil {
		return nil, 0, e
	}
	kind := "archive"
	if st.Mode().IsRegular() {
		kind = "file"
	} else if !st.IsDir() {
		return nil, 0, invalid("%s is neither a file nor a folder", rel)
	}
	tk, exp := s.tickets.issue(&ticket{kind: "volume", ref: v.Slug, extra: map[string]string{"path": rel}, sessionID: c.SessionID, via: c.Via, actor: c.Actor}, 5*time.Minute)
	s.activity(c, "volume.downloaded", fmt.Sprintf("volume %s%s downloaded from %s (%s)", v.Slug, strings.TrimSuffix(rel, "/"), s.h.Site(), kind),
		map[string]any{"volumeSlug": v.Slug, "path": rel, "kind": kind, "siteSlug": s.h.Site()})
	q := "?ticket=" + tk
	return map[string]any{"url": "/_opm/api/sites/" + s.h.Site() + "/tools/volumes/" + v.Slug + "/download" + q, "expiresAt": ts(exp)}, 200, nil
}

func (s *Service) handleVolumeDownload(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	v, e := s.volume(r.PathValue("volumeSlug"))
	if e != nil {
		return nil, 0, e
	}
	tk, e := s.tickets.take(r.URL.Query().Get("ticket"), "volume", c)
	if e != nil {
		return nil, 0, e
	}
	if tk.ref != v.Slug {
		return nil, 0, errf(401, "unauthenticated", "the link is for another volume")
	}
	abs, rel, e := resolve(v.Path, tk.extra["path"])
	if e != nil {
		return nil, 0, e
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return nil, 0, notFound("%s does not exist in volume %s", rel, v.Slug)
	}
	stamp := time.Now().UTC().Format("20060102-1504")
	w.Header().Set("Cache-Control", "no-store")
	if st.Mode().IsRegular() {
		f, err := os.Open(abs)
		if err != nil {
			return nil, 0, errf(403, "forbidden", "cannot read %s: %s", rel, err.Error())
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(filepath.Base(abs))+`"`)
		http.ServeContent(w, r, "", st.ModTime(), f)
		return nil, 0, nil
	}
	name := "opm-" + s.h.Site() + "-" + v.Slug
	if rel != "/" {
		name += "-" + strings.ReplaceAll(strings.Trim(rel, "/"), "/", "-")
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(name)+"-"+stamp+`.tar.gz"`)
	if err := writeTarGz(r.Context(), w, abs); err != nil {
		s.h.Log().Warn("tools", "volume archive interrupted", "volume", v.Slug, "error", err.Error())
	}
	return nil, 0, nil
}

// writeTarGz streams a directory as tar.gz (regular files, dirs and symlinks; nothing followed).
func writeTarGz(ctx context.Context, w io.Writer, root string) error {
	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		} else if !info.IsDir() && !info.Mode().IsRegular() {
			return nil // sockets, devices, fifos
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			_, cerr := io.CopyN(tw, f, info.Size())
			f.Close()
			if cerr != nil {
				return cerr
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
