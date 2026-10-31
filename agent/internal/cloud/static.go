package cloud

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

var staticTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8", ".svg": "image/svg+xml", ".png": "image/png",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".ico": "image/x-icon",
	".txt": "text/plain; charset=utf-8", ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf",
	".map": "application/json; charset=utf-8", ".webmanifest": "application/manifest+json", ".wasm": "application/wasm",
	".mp4": "video/mp4", ".webm": "video/webm",
}

// ServeStatic is serveStatic: the built frontend with SPA fallback. false = not handled.
func ServeStatic(w http.ResponseWriter, root, method, rawURL string) bool {
	if method != "GET" && method != "HEAD" {
		return false
	}
	head := w.Header()
	plain := func(status int, body string) bool {
		driver.SetHeader(w, "Content-Type", "text/plain")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		return true
	}
	p, _, _ := strings.Cut(rawURL, "?")
	rel, err := doc.DecodeURIComponent(p)
	if err != nil {
		return plain(400, "bad path")
	}
	full := filepath.Join(root, "."+rel)
	if !strings.HasPrefix(full, filepath.Clean(root)) {
		return plain(404, "not found")
	}
	file := full
	st, err := os.Stat(file)
	if err == nil && st.IsDir() {
		file = filepath.Join(file, "index.html")
		st, err = os.Stat(file)
	}
	if err != nil || !st.Mode().IsRegular() {
		// SPA fallback
		file = filepath.Join(root, "index.html")
		if st, err = os.Stat(file); err != nil {
			return false
		}
	}
	ext := strings.ToLower(filepath.Ext(file))
	ct, ok := staticTypes[ext]
	if !ok {
		ct = "application/octet-stream"
	}
	cc := "no-cache"
	if strings.Contains(file, "/_app/immutable/") {
		cc = "public, max-age=31536000, immutable"
	}
	driver.SetHeader(w, "Content-Type", ct)
	driver.SetHeader(w, "Content-Length", strconv.FormatInt(st.Size(), 10))
	driver.SetHeader(w, "Cache-Control", cc)
	driver.SetHeader(w, "X-Content-Type-Options", "nosniff")
	if method == "HEAD" {
		w.WriteHeader(200)
		return true
	}
	f, err := os.Open(file)
	if err != nil {
		delete(head, "Content-Length")
		driver.Log.Warn("stream error", "error", err.Error(), "path", rawURL)
		panic(http.ErrAbortHandler)
	}
	defer f.Close()
	w.WriteHeader(200)
	_, _ = io.Copy(w, f)
	return true
}
