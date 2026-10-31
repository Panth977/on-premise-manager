package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// open CORS: agent + install routes and the public icon bytes
var openCORS = regexp.MustCompile(`^/v1/((agent|install)/|icons/[^/]+/content$)`)

const (
	allowHeadersOpen  = "Content-Type, X-OPM-Site, X-OPM-Ts, X-OPM-Nonce, X-OPM-Sig, Authorization, Range"
	exposeOpen        = "X-OPM-Server-Time, X-OPM-Sha256, Content-Range, X-OPM-Signature, X-Request-Id"
	allowHeadersHuman = "Content-Type, Authorization, Range, X-OPM-Sha256, If-None-Match"
	exposeHuman       = "X-Request-Id, X-OPM-Sha256, Content-Range, Content-Disposition, Retry-After, X-OPM-Server-Time, ETag, Content-Length"
	corsMethods       = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
)

// CorsHeaders is corsHeaders of app.ts.
func CorsHeaders(path, origin string) map[string]string {
	if openCORS.MatchString(path) {
		return map[string]string{"Access-Control-Allow-Origin": "*", "Access-Control-Allow-Methods": corsMethods, "Access-Control-Allow-Headers": allowHeadersOpen, "Access-Control-Expose-Headers": exposeOpen, "Access-Control-Max-Age": "3600"}
	}
	if origin == "" {
		return map[string]string{}
	}
	allowed := Env.FrontendOrigins()
	if has(allowed, "*") || has(allowed, origin) {
		return map[string]string{"Access-Control-Allow-Origin": origin, "Vary": "Origin", "Access-Control-Allow-Methods": corsMethods, "Access-Control-Allow-Headers": allowHeadersHuman, "Access-Control-Expose-Headers": exposeHuman, "Access-Control-Max-Age": "3600"}
	}
	return map[string]string{"Vary": "Origin"}
}

// HandleAPI is handleApi: one /v1 request → reply (CORS + mount handling). rawURL may carry a mount prefix before /v1/.
func HandleAPI(method, rawURL string, headers map[string]string, body []byte, ip string, cx context.Context) *Reply {
	raw := rawURL
	if raw == "" {
		raw = "/"
	}
	i := strings.Index(raw, "/v1/")
	u, mount := raw, ""
	if i >= 0 {
		u = raw[i:]
	}
	if i > 0 {
		mount = raw[:i]
	}
	path, _, _ := strings.Cut(u, "?")
	cors := CorsHeaders(path, headers["origin"])
	if method == "OPTIONS" {
		return &Reply{204, cors, nil}
	}
	r := Dispatch(ReqLike{Method: method, URL: u, Headers: headers, Body: body, IP: ip, Context: cx, Mount: mount}, DispatchOpts{})
	for k, v := range r.Headers {
		cors[k] = v
	}
	r.Headers = cors
	return r
}

// node drops repeated values of these request headers (the first wins).
var singleHeaders = map[string]bool{"age": true, "authorization": true, "content-length": true, "content-type": true, "etag": true, "expires": true, "from": true, "host": true, "if-modified-since": true, "if-unmodified-since": true, "last-modified": true, "location": true, "max-forwards": true, "proxy-authorization": true, "referer": true, "retry-after": true, "server": true, "user-agent": true}

// RequestHeaders: lower-case names, repeated values joined like node's req.headers.
func RequestHeaders(r *http.Request) map[string]string {
	out := make(map[string]string, len(r.Header)+1)
	for k, v := range r.Header {
		n := strings.ToLower(k)
		switch {
		case singleHeaders[n]:
			out[n] = v[0]
		case n == "cookie":
			out[n] = strings.Join(v, "; ")
		default:
			out[n] = strings.Join(v, ", ")
		}
	}
	out["host"] = r.Host
	if _, ok := out["content-length"]; !ok && r.ContentLength >= 0 && r.Method != http.MethodGet && r.Method != http.MethodHead {
		out["content-length"] = strconv.FormatInt(r.ContentLength, 10)
	}
	return out
}

var errTooLarge = errors.New("too large")

func readBody(r *http.Request, limit int64) ([]byte, error) {
	if r.ContentLength > limit {
		return nil, errTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	return b, nil
}

// WriteReply sends a reply with exactly its header spelling; no sniffed Content-Type.
func WriteReply(w http.ResponseWriter, status int, headers map[string]string, body any, path string) {
	h := w.Header()
	h["Content-Type"] = nil
	for k, v := range headers {
		h[k] = []string{v}
	}
	var src io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		if _, ok := headers["Content-Length"]; !ok {
			h["Content-Length"] = []string{strconv.Itoa(len(b))}
		}
		src = strings.NewReader(b)
	case []byte:
		if _, ok := headers["Content-Length"]; !ok {
			h["Content-Length"] = []string{strconv.Itoa(len(b))}
		}
		src = strings.NewReader(string(b))
	case io.Reader:
		src = b
	default:
		panic(fmt.Sprintf("cloud: reply body %T", body))
	}
	if c, ok := src.(io.Closer); ok {
		defer c.Close()
	}
	w.WriteHeader(status)
	if src == nil {
		return
	}
	if _, err := io.Copy(w, src); err != nil {
		driver.Log.Warn("stream error", "error", err.Error(), "path", path)
		panic(http.ErrAbortHandler)
	}
}

// Version is OPM_CLOUD_VERSION ("dev" when unset).
func Version() string {
	if v, ok := lookupEnv("OPM_CLOUD_VERSION"); ok {
		return v
	}
	return "dev"
}

// RemoteGUI is serveRemoteGui + pipeRemoteStream of server/remote-gui.ts (a site's agent GUI through the cloud): true =
// answered. Set by its port; runs before /v1/ for GET /app/deployments/{id}/remote/{site}/….
var RemoteGUI func(w http.ResponseWriter, r *http.Request, path string, headers map[string]string) bool

// RemoteUpgrade is handleRemoteUpgrade: WebSocket upgrades of the remote agent GUI (hijack the connection); false =
// refused with 404.
var RemoteUpgrade func(w http.ResponseWriter, r *http.Request) bool

func clientIP(r *http.Request, trustProxy bool, headers map[string]string) string {
	if f := headers["x-forwarded-for"]; trustProxy && f != "" {
		first, _, _ := strings.Cut(f, ",")
		return doc.JSTrim(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isUpgrade(r *http.Request) bool {
	return r.Header.Get("Upgrade") != "" && strings.Contains(strings.ToLower(strings.Join(r.Header["Connection"], ",")), "upgrade")
}

// NewHandler is createServer: one port serves /v1, /auth, /_blob, /opm-config.json, /healthz, the remote GUI, the frontend.
func NewHandler(cfg *CloudConfig, p *driver.Ports) http.Handler {
	authOpts := driver.AuthHTTPOptions{Oidc: cfg.Oidc, ConsoleURL: cfg.ConsoleURL}
	if Env.SmtpURL() != "" {
		authOpts.SendMail = func(to, subject, text string) error {
			SendMail(to, subject, text)
			return nil
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawURL := r.RequestURI
		if rawURL == "" {
			rawURL = "/"
		}
		path, _, _ := strings.Cut(rawURL, "?")
		method := r.Method
		headers := RequestHeaders(r)
		ip := clientIP(r, cfg.TrustProxy, headers)
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				driver.Log.Error("opm-cloud request failed", "path", path, "error", fmt.Sprint(v))
				WriteReply(w, 500, map[string]string{"Content-Type": "application/json"}, `{"error":{"code":"internal","message":"internal error","details":{}}}`, path)
			}
		}()
		if isUpgrade(r) {
			if RemoteUpgrade == nil || !RemoteUpgrade(w, r) {
				if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
					_, _ = io.WriteString(c, "HTTP/1.1 404 Not Found\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
					c.Close()
				}
			}
			return
		}
		if RemoteGUI != nil && strings.HasPrefix(path, "/app/deployments/") && strings.Contains(path, "/remote/") {
			if RemoteGUI(w, r, path, headers) {
				return
			}
		}
		switch {
		case strings.Contains(path, "/v1/"):
			var body []byte
			if method != "GET" && method != "HEAD" && method != "OPTIONS" {
				var err error
				if body, err = readBody(r, cfg.BodyLimit); err != nil {
					if errors.Is(err, errTooLarge) {
						WriteReply(w, 413, map[string]string{"Content-Type": "application/json"}, doc.MarshalString(doc.Obj("error", doc.Obj("code", "too-large", "message", "request body over "+strconv.FormatInt(cfg.BodyLimit, 10)+" bytes", "details", doc.New()))), path)
						return
					}
					panic(err)
				}
			}
			out := HandleAPI(method, rawURL, headers, body, ip, r.Context())
			WriteReply(w, out.Status, out.Headers, out.Body, path)
		case path == "/healthz":
			WriteReply(w, 200, map[string]string{"Content-Type": "application/json", "Cache-Control": "no-store"}, doc.MarshalString(doc.Obj("ok", true, "adapter", "local", "version", Version())), path)
		case path == "/opm-config.json":
			var oidc any
			if cfg.Oidc != nil {
				oidc = doc.Obj("label", cfg.Oidc.Label)
			}
			conf := doc.Obj("auth", doc.Obj("provider", "local", "passwordReset", Env.SmtpURL() != "", "oidc", oidc))
			WriteReply(w, 200, map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store"}, doc.MarshalString(conf), path)
		case strings.HasPrefix(path, "/_blob/"):
			driver.ServeBlob(w, r, p.Blobs, cfg.FrontendOrigins)
		case strings.HasPrefix(path, "/auth/"):
			var body []byte
			if method != "GET" && method != "HEAD" {
				var err error
				if body, err = readBody(r, 1024*1024); err != nil {
					if errors.Is(err, errTooLarge) {
						WriteReply(w, 413, map[string]string{"Content-Type": "application/json"}, `{"error":{"code":"too-large","message":"body too large"}}`, path)
						return
					}
					panic(err)
				}
			}
			out := driver.HandleAuthHTTP(driver.AuthHTTPRequest{Method: method, URL: rawURL, Body: body, IP: ip}, p.Auth, authOpts)
			var b any
			if out.Body != nil {
				b = *out.Body
			}
			WriteReply(w, out.Status, out.Headers, b, path)
		default:
			if cfg.StaticDir != "" && ServeStatic(w, cfg.StaticDir, method, rawURL) {
				return
			}
			WriteReply(w, 404, map[string]string{"Content-Type": "text/plain; charset=utf-8"}, "not found", path)
		}
	})
}

// NewServer applies the timeouts of server.ts.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadTimeout: 15 * time.Minute, ReadHeaderTimeout: 70 * time.Second, IdleTimeout: 65 * time.Second}
}
