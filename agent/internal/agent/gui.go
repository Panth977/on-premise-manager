package agent

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"opm/agent/internal/webui"
)

const agent404HTML = `<!doctype html><html><head><meta charset="utf-8"><title>OPM agent · 404</title></head>
<body style="font-family:sans-serif;text-align:center;padding:10vh"><h1>OPM agent · 404</h1>
<p>This page does not exist.</p><p><a href="/_opm/app">Back to /_opm/app</a></p></body></html>`

func (a *Agent) agent404(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, agent404HTML)
}

// ServeAgent serves /_opm/* for a surface (edge | gui-alias | agent-port). It implements edge.Host.
func (a *Agent) ServeAgent(w http.ResponseWriter, r *http.Request, surface string) {
	p := r.URL.Path
	if w.Header().Get("X-Request-Id") == "" {
		w.Header().Set("X-Request-Id", newID())
	}
	switch {
	case p == "/_opm/healthz":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
		return
	case strings.HasPrefix(p, "/_opm/api/"):
		a.apiOnce.Do(func() { a.apiH = a.apiHandler() })
		r = withRC(r, &reqCtx{surface: surface, ip: clientIP(r), ua: r.UserAgent()})
		a.apiH.ServeHTTP(w, r)
		return
	case p == "/_opm" || p == "/_opm/":
		http.Redirect(w, r, "/_opm/app", http.StatusFound)
		return
	}
	a.serveGUI(w, r)
}

// Ready implements edge.Host.
func (a *Agent) Ready(serviceID string) bool { return a.drv.Ready(serviceID) }

var guiTypes = map[string]string{".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
	".json": "application/json", ".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon", ".woff2": "font/woff2"}

// sendGUIFile serves an embedded file (precompressed .br / .gz sibling when accepted).
func sendGUIFile(w http.ResponseWriter, r *http.Request, rel, cache string) bool {
	dist := webui.Dist()
	if st, err := fs.Stat(dist, rel); err != nil || st.IsDir() {
		return false
	}
	ae := r.Header.Get("Accept-Encoding")
	name, enc := rel, ""
	if strings.Contains(ae, "br") {
		if _, err := fs.Stat(dist, rel+".br"); err == nil {
			name, enc = rel+".br", "br"
		}
	}
	if enc == "" && strings.Contains(ae, "gzip") {
		if _, err := fs.Stat(dist, rel+".gz"); err == nil {
			name, enc = rel+".gz", "gzip"
		}
	}
	b, err := fs.ReadFile(dist, name)
	if err != nil {
		return false
	}
	ct := guiTypes[filepath.Ext(rel)]
	if ct == "" {
		if ct = mime.TypeByExtension(filepath.Ext(rel)); ct == "" {
			ct = "application/octet-stream"
		}
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", cache)
	h.Set("Vary", "Accept-Encoding")
	h.Set("X-Content-Type-Options", "nosniff")
	if enc != "" {
		h.Set("Content-Encoding", enc)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
	return true
}

// serveGUI mirrors agent/gui/tests/serve.mjs (§7.1).
func (a *Agent) serveGUI(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/_opm/assets/"):
		rel := strings.TrimPrefix(path.Clean(p), "/_opm/")
		cache := "no-cache"
		if strings.HasPrefix(p, "/_opm/assets/immutable/") {
			cache = "public, max-age=31536000, immutable"
		}
		if !strings.HasPrefix(rel, "assets/") || !sendGUIFile(w, r, rel, cache) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "not found")
		}
		return
	case p == "/_opm/login" || p == "/_opm/app" || strings.HasPrefix(p, "/_opm/app/"):
		if sendGUIFile(w, r, "index.html", "no-cache") {
			return
		}
	}
	a.agent404(w, r)
}
