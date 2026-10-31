package driver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/cloud/doc"
)

// BlobMeta is head().
type BlobMeta struct {
	Size        int64
	ContentType string
	Meta        map[string]string
	CreatedMs   int64
	UpdatedMs   int64
}

// PutOptions of Put / WriteStream.
type PutOptions struct {
	ContentType  string
	CacheControl string
	Meta         map[string]string
	// MetaMap, when set, wins over Meta and keeps the key order of the sidecar.
	MetaMap *doc.Map
}

// ListedBlob is one row of List.
type ListedBlob struct {
	Path      string
	Size      int64
	CreatedMs int64
}

// LifecycleRule of Sweep.
type LifecycleRule struct {
	Prefixes []string
	AgeDays  int
}

// BlobError carries the code of the TS error (400 bad path, 404 no such object).
type BlobError struct {
	Code int
	Msg  string
}

func (e *BlobError) Error() string { return e.Msg }

func notFound(p string) error { return &BlobError{404, "No such object: " + p} }

// CheckBlobPath: relative, no empty / "." / ".." segments, no backslashes or NUL.
func CheckBlobPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return &BlobError{400, fmt.Sprintf(`invalid object path "%s"`, p)}
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." {
			return &BlobError{400, fmt.Sprintf(`invalid object path "%s"`, p)}
		}
	}
	return nil
}

func secretFile(file string) ([]byte, error) {
	if b, err := os.ReadFile(file); err == nil {
		return b, nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = f.Write(key)
		f.Close()
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return os.ReadFile(file)
}

// LoadSecret: a secret from env (utf8) or a generated 32-byte key file (0600) under <dataDir>/secrets.
func LoadSecret(envName, dataDir, name string) ([]byte, error) {
	if v := os.Getenv(envName); v != "" {
		if doc.JSLen(v) < 16 {
			return nil, fmt.Errorf("%s must be at least 16 characters", envName)
		}
		return []byte(v), nil
	}
	return secretFile(filepath.Join(dataDir, "secrets", name))
}

// BlobStore is the filesystem object store: <dataDir>/blobs/<path>, sidecar <dataDir>/blobmeta/<path>.json, atomic
// writes through <dataDir>/tmp. CanCompose is false.
type BlobStore struct {
	DataDir   string
	PublicURL string
	Root      string
	metaRoot  string
	tmp       string
	keyMu     sync.Mutex
	key       []byte
}

func newBlobStore(dataDir, publicURL string) (*BlobStore, error) {
	b := &BlobStore{DataDir: dataDir, PublicURL: publicURL, Root: filepath.Join(dataDir, "blobs"), metaRoot: filepath.Join(dataDir, "blobmeta"), tmp: filepath.Join(dataDir, "tmp")}
	for _, d := range []string{b.Root, b.metaRoot, b.tmp} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (b *BlobStore) file(p string) (string, error) {
	if err := CheckBlobPath(p); err != nil {
		return "", err
	}
	return filepath.Join(b.Root, p), nil
}
func (b *BlobStore) side(p string) (string, error) {
	if err := CheckBlobPath(p); err != nil {
		return "", err
	}
	return filepath.Join(b.metaRoot, p+".json"), nil
}
func (b *BlobStore) tmpName() string {
	r := make([]byte, 8)
	_, _ = rand.Read(r)
	return filepath.Join(b.tmp, strconv.FormatInt(time.Now().UnixMilli(), 10)+"-"+hex.EncodeToString(r))
}

// readSide: the sidecar document, nil when missing or corrupt.
func (b *BlobStore) readSide(p string) *doc.Map {
	f, err := b.side(p)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	m, err := doc.ParseMap(raw)
	if err != nil {
		return nil
	}
	return m
}
func (b *BlobStore) writeSide(p string, s *doc.Map) error {
	f, err := b.side(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o777); err != nil {
		return err
	}
	t := b.tmpName()
	raw, err := doc.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(t, raw, 0o666); err != nil {
		return err
	}
	return os.Rename(t, f)
}

func metaOf(o PutOptions) *doc.Map {
	if o.MetaMap != nil {
		return o.MetaMap
	}
	m := doc.New()
	keys := make([]string, 0, len(o.Meta))
	for k := range o.Meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m.Set(k, o.Meta[k])
	}
	return m
}

// publish moves a finished temp file into place (sidecar first, so a visible object always has its metadata).
func (b *BlobStore) publish(tmp, p string, o PutOptions) error {
	now := time.Now().UnixMilli()
	ct := o.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	s := doc.Obj("contentType", ct)
	if o.CacheControl != "" {
		s.Set("cacheControl", o.CacheControl)
	}
	s.Set("meta", metaOf(o)).Set("createdMs", now).Set("updatedMs", now)
	if err := b.writeSide(p, s); err != nil {
		return err
	}
	f, err := b.file(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o777); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// Put writes an object.
func (b *BlobStore) Put(path string, data []byte, o PutOptions) error {
	if err := CheckBlobPath(path); err != nil {
		return err
	}
	t := b.tmpName()
	err := os.WriteFile(t, data, 0o666)
	if err == nil {
		err = b.publish(t, path, o)
	}
	if err != nil {
		_ = os.Remove(t)
	}
	return err
}

// BlobWriter is a write stream: Close publishes the object, Abort drops it.
type BlobWriter struct {
	b    *BlobStore
	f    *os.File
	tmp  string
	path string
	o    PutOptions
	done bool
}

// Write appends bytes.
func (w *BlobWriter) Write(p []byte) (int, error) { return w.f.Write(p) }

// Close finishes the stream: the object becomes visible.
func (w *BlobWriter) Close() error {
	if w.done {
		return nil
	}
	w.done = true
	err := w.f.Close()
	if err == nil {
		err = w.b.publish(w.tmp, w.path, w.o)
	}
	if err != nil {
		_ = os.Remove(w.tmp)
	}
	return err
}

// Abort drops the stream; nothing becomes visible.
func (w *BlobWriter) Abort() {
	if w.done {
		return
	}
	w.done = true
	_ = w.f.Close()
	_ = os.Remove(w.tmp)
}

// WriteStream opens a write stream.
func (b *BlobStore) WriteStream(path string, o PutOptions) (*BlobWriter, error) {
	if err := CheckBlobPath(path); err != nil {
		return nil, err
	}
	t := b.tmpName()
	f, err := os.OpenFile(t, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, err
	}
	return &BlobWriter{b: b, f: f, tmp: t, path: path, o: o}, nil
}

func missing(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var pe *fs.PathError
	return errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "not a directory")
}

// Get returns the whole object, nil when missing.
func (b *BlobStore) Get(path string) ([]byte, error) {
	f, err := b.file(path)
	if err != nil {
		return nil, err
	}
	out, err := os.ReadFile(f)
	if err != nil {
		if missing(err) {
			return nil, nil
		}
		return nil, err
	}
	if out == nil {
		out = []byte{}
	}
	return out, nil
}

type rangeReader struct {
	io.Reader
	f *os.File
}

func (r *rangeReader) Close() error { return r.f.Close() }

// Stream reads an object; Range is an inclusive byte range like HTTP Range (nil = all).
func (b *BlobStore) Stream(path string, rng *[2]int64) (io.ReadCloser, error) {
	p, err := b.file(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if missing(err) {
			return nil, notFound(path)
		}
		return nil, err
	}
	if rng == nil {
		return f, nil
	}
	if _, err := f.Seek(rng[0], io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &rangeReader{io.LimitReader(f, rng[1]-rng[0]+1), f}, nil
}

// Head returns the metadata, nil when missing.
func (b *BlobStore) Head(path string) (*BlobMeta, error) {
	p, err := b.file(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return nil, nil
	}
	s := b.readSide(path)
	m := &BlobMeta{Size: st.Size(), ContentType: "application/octet-stream", Meta: map[string]string{}, CreatedMs: st.ModTime().UnixMilli(), UpdatedMs: st.ModTime().UnixMilli()}
	if s != nil {
		if v, ok := s.StrOK("contentType"); ok {
			m.ContentType = v
		}
		s.Map("meta").Each(func(k string, v any) {
			if x, ok := v.(string); ok {
				m.Meta[k] = x
			}
		})
		if v, ok := s.NumOK("createdMs"); ok {
			m.CreatedMs = int64(v)
		}
		if v, ok := s.NumOK("updatedMs"); ok {
			m.UpdatedMs = int64(v)
		}
	}
	return m, nil
}

// CacheControl is the cacheControl of an object ("" = none).
func (b *BlobStore) CacheControl(path string) string { return b.readSide(path).Str("cacheControl") }

// SetMeta merges metadata (a nil value deletes a key).
func (b *BlobStore) SetMeta(path string, meta map[string]*string) error {
	h, err := b.Head(path)
	if err != nil {
		return err
	}
	if h == nil {
		return notFound(path)
	}
	s := b.readSide(path)
	if s == nil {
		s = doc.Obj("contentType", h.ContentType, "meta", doc.New(), "createdMs", h.CreatedMs, "updatedMs", h.UpdatedMs)
	}
	m := s.Map("meta").Shallow()
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if meta[k] == nil {
			m.Delete(k)
		} else {
			m.Set(k, *meta[k])
		}
	}
	return b.writeSide(path, s.Set("meta", m).Set("updatedMs", time.Now().UnixMilli()))
}

func prune(dir, stop string) {
	for strings.HasPrefix(dir, stop) && dir != stop {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// Delete removes an object (missing is ignored).
func (b *BlobStore) Delete(path string) error {
	f, err := b.file(path)
	if err != nil {
		return err
	}
	side, _ := b.side(path)
	_ = os.Remove(f)
	_ = os.Remove(side)
	prune(filepath.Dir(f), b.Root)
	prune(filepath.Dir(side), b.metaRoot)
	return nil
}

// DeletePrefix removes every object under a raw string prefix.
func (b *BlobStore) DeletePrefix(prefix string) error {
	l, err := b.List(prefix)
	if err != nil {
		return err
	}
	for _, x := range l {
		if err := b.Delete(x.Path); err != nil {
			return err
		}
	}
	return nil
}

// List returns every object whose path starts with the raw string prefix ("" = all), sorted by path.
func (b *BlobStore) List(prefix string) ([]ListedBlob, error) {
	base := ""
	if i := strings.LastIndexByte(prefix, '/'); i >= 0 {
		base = prefix[:i]
	}
	if base != "" {
		if err := CheckBlobPath(base); err != nil {
			return nil, err
		}
	}
	var out []ListedBlob
	var walk func(rel string)
	walk = func(rel string) {
		ents, err := os.ReadDir(filepath.Join(b.Root, rel))
		if err != nil {
			return
		}
		for _, e := range ents {
			p := e.Name()
			if rel != "" {
				p = rel + "/" + e.Name()
			}
			if e.IsDir() {
				if strings.HasPrefix(p, prefix) || strings.HasPrefix(prefix, p+"/") {
					walk(p)
				}
			} else if e.Type().IsRegular() && strings.HasPrefix(p, prefix) {
				st, err := os.Stat(filepath.Join(b.Root, p))
				if err != nil {
					continue
				}
				c := st.ModTime().UnixMilli()
				if v, ok := b.readSide(p).NumOK("createdMs"); ok {
					c = int64(v)
				}
				out = append(out, ListedBlob{Path: p, Size: st.Size(), CreatedMs: c})
			}
		}
	}
	walk(base)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Copy copies an object with its metadata.
func (b *BlobStore) Copy(src, dst string) error {
	s := b.readSide(src)
	sf, err := b.file(src)
	if err != nil {
		return err
	}
	t := b.tmpName()
	in, err := os.Open(sf)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return notFound(src)
		}
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(t, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if e := out.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = b.publish(t, dst, PutOptions{ContentType: s.Str("contentType"), CacheControl: s.Str("cacheControl"), MetaMap: s.Map("meta")})
	}
	if err != nil {
		_ = os.Remove(t)
	}
	return err
}

func (b *BlobStore) secret() ([]byte, error) {
	b.keyMu.Lock()
	defer b.keyMu.Unlock()
	if b.key == nil {
		k, err := LoadSecret("OPM_BLOB_SECRET", b.DataDir, "blob-hmac.key")
		if err != nil {
			return nil, err
		}
		b.key = k
	}
	return b.key, nil
}

// Sign is the signature of a read URL. exp is the decimal text of the expiry.
func (b *BlobStore) Sign(path, exp, disposition string) (string, error) {
	k, err := b.secret()
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(path + "\n" + exp + "\n" + disposition))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

// Verify checks a signature.
func (b *BlobStore) Verify(path, exp, disposition, sig string) bool {
	want, err := b.Sign(path, exp, disposition)
	return err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}

// SignedReadURL is a short-lived download URL. expires may be negative (tests).
func (b *BlobStore) SignedReadURL(path string, expires time.Duration, disposition string) (string, error) {
	if err := CheckBlobPath(path); err != nil {
		return "", err
	}
	exp := strconv.FormatInt(time.Now().Add(expires).UnixMilli(), 10)
	sig, err := b.Sign(path, exp, disposition)
	if err != nil {
		return "", err
	}
	segs := strings.Split(path, "/")
	for i := range segs {
		segs[i] = doc.EncodeURIComponent(segs[i])
	}
	u := b.PublicURL + "/_blob/" + strings.Join(segs, "/") + "?exp=" + exp + "&sig=" + sig
	if disposition != "" {
		u += "&rd=" + doc.EncodeURIComponent(disposition)
	}
	return u, nil
}

// Sweep applies the lifecycle rules and removes stale temp files. Returns how many objects were deleted.
func (b *BlobStore) Sweep(rules []LifecycleRule, now int64) (int, error) {
	n := 0
	for _, r := range rules {
		cutoff := now - int64(r.AgeDays)*86_400_000
		for _, p := range r.Prefixes {
			l, err := b.List(p)
			if err != nil {
				return n, err
			}
			for _, x := range l {
				if x.CreatedMs < cutoff {
					if err := b.Delete(x.Path); err != nil {
						return n, err
					}
					n++
				}
			}
		}
	}
	ents, _ := os.ReadDir(b.tmp)
	for _, e := range ents {
		if st, err := e.Info(); err == nil && st.ModTime().UnixMilli() < now-86_400_000 {
			_ = os.Remove(filepath.Join(b.tmp, e.Name()))
		}
	}
	return n, nil
}

// ───────────────────────────────────────── HTTP (/_blob/…)

const blobExpose = "Content-Type, Content-Length, ETag, Content-Disposition, Range, Accept-Ranges, Content-Range"

// SetHeader sets a response header with exactly this spelling (Go would canonicalize X-OPM-… otherwise).
func SetHeader(w http.ResponseWriter, k, v string) { w.Header()[k] = []string{v} }

var rangeRe = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

// ServeBlob serves GET / HEAD /_blob/<path>?exp&sig[&rd] (signed read URLs); CORS like storage.cors.json.
func ServeBlob(w http.ResponseWriter, r *http.Request, b *BlobStore, allowedOrigins []string) {
	head := w.Header()
	head["Content-Type"] = nil // never sniffed
	origin := r.Header.Get("Origin")
	cors := false
	for _, o := range allowedOrigins {
		if o == "*" || o == origin {
			cors = origin != ""
		}
	}
	if cors {
		SetHeader(w, "Access-Control-Allow-Origin", origin)
		SetHeader(w, "Access-Control-Expose-Headers", blobExpose)
		SetHeader(w, "Vary", "Origin")
	}
	fail := func(status int, msg string) {
		SetHeader(w, "Content-Type", "text/plain; charset=utf-8")
		SetHeader(w, "Cache-Control", "no-store")
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, msg)
		}
	}
	if r.Method == http.MethodOptions {
		if cors {
			SetHeader(w, "Access-Control-Allow-Methods", "GET, HEAD")
			h := r.Header.Get("Access-Control-Request-Headers")
			if h == "" {
				h = "Range"
			}
			SetHeader(w, "Access-Control-Allow-Headers", h)
			SetHeader(w, "Access-Control-Max-Age", "3600")
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		SetHeader(w, "Allow", "GET, HEAD, OPTIONS")
		fail(405, "method not allowed")
		return
	}
	raw := r.RequestURI
	pathPart, query, _ := strings.Cut(raw, "?")
	i := strings.Index(pathPart, "/_blob/")
	if i < 0 {
		fail(404, "not found")
		return
	}
	segs := strings.Split(pathPart[i+7:], "/")
	for j, s := range segs {
		d, err := doc.DecodeURIComponent(s)
		if err != nil {
			fail(400, "bad object path")
			return
		}
		segs[j] = d
	}
	path := strings.Join(segs, "/")
	if CheckBlobPath(path) != nil {
		fail(400, "bad object path")
		return
	}
	q := doc.ParseSearch(query)
	expS, sig, rd := q.Get("exp"), q.Get("sig"), q.Get("rd")
	exp, err := strconv.ParseFloat(expS, 64)
	if expS == "" {
		exp, err = 0, nil // Number(null) = 0
	}
	if err != nil || exp != exp || sig == "" || !b.Verify(path, formatExp(exp), rd, sig) {
		fail(403, "invalid signature")
		return
	}
	if exp < float64(time.Now().UnixMilli()) {
		fail(403, "expired")
		return
	}
	h, err := b.Head(path)
	if err != nil || h == nil {
		fail(404, "no such object")
		return
	}
	SetHeader(w, "Content-Type", h.ContentType)
	SetHeader(w, "Accept-Ranges", "bytes")
	SetHeader(w, "ETag", `"`+strconv.FormatInt(h.Size, 16)+"-"+strconv.FormatInt(h.UpdatedMs, 16)+`"`)
	SetHeader(w, "Last-Modified", time.UnixMilli(h.UpdatedMs).UTC().Format(http.TimeFormat))
	cc := b.CacheControl(path)
	if cc == "" {
		cc = "private, max-age=0"
	}
	SetHeader(w, "Cache-Control", cc)
	SetHeader(w, "X-Content-Type-Options", "nosniff")
	if rd != "" {
		SetHeader(w, "Content-Disposition", rd)
	}
	serve := func(status int, rng *[2]int64, length int64) {
		SetHeader(w, "Content-Length", strconv.FormatInt(length, 10))
		if r.Method == http.MethodHead || length == 0 {
			w.WriteHeader(status)
			return
		}
		s, err := b.Stream(path, rng)
		if err != nil {
			head.Del("Content-Length")
			fail(500, "internal error")
			return
		}
		defer s.Close()
		w.WriteHeader(status)
		_, _ = io.Copy(w, s)
	}
	if rg := strings.TrimSpace(r.Header.Get("Range")); rg != "" {
		if m := rangeRe.FindStringSubmatch(rg); m != nil && (m[1] != "" || m[2] != "") {
			var start, end int64
			if m[1] == "" {
				n, _ := strconv.ParseInt(m[2], 10, 64)
				start = h.Size - n
				if start < 0 {
					start = 0
				}
				end = h.Size - 1
				if n == 0 {
					start = h.Size
				}
			} else {
				start, _ = strconv.ParseInt(m[1], 10, 64)
				end = h.Size - 1
				if m[2] != "" {
					if e, _ := strconv.ParseInt(m[2], 10, 64); e < end {
						end = e
					}
				}
			}
			if start >= h.Size || start > end {
				for _, k := range []string{"Content-Type", "ETag", "Last-Modified", "Cache-Control", "X-Content-Type-Options", "Content-Disposition"} {
					delete(head, k)
				}
				head["Content-Type"] = nil
				SetHeader(w, "Content-Range", "bytes */"+strconv.FormatInt(h.Size, 10))
				SetHeader(w, "Accept-Ranges", "bytes")
				w.WriteHeader(416)
				return
			}
			SetHeader(w, "Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, h.Size))
			serve(206, &[2]int64{start, end}, end-start+1)
			return
		}
		// multi-range / malformed: serve the whole object (RFC 9110 allows ignoring Range)
	}
	serve(200, nil, h.Size)
}

func formatExp(f float64) string {
	if f == float64(int64(f)) && f < 1e21 && f > -1e21 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
