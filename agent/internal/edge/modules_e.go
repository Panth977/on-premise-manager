package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"

	"opm/agent/internal/edgeres"
	"opm/agent/internal/model"
)

// Round E custom modules. The decisions use the same Go code as "Test a URL" (internal/edgeres).

// Vars set by opm_route (request context, also readable as {http.vars.<name>} placeholders).
const (
	VarRoute    = "opm_route"    // the routeId that answers (also set for a rewrite route that ended in 404 / 500)
	VarAuto     = "opm_auto"     // routeId of the proxy prefix route of an nginx auto-redirect
	VarLocation = "opm_location" // auto-redirect target
	VarURI      = "opm_uri"      // current path (decoded, after internal rewrites) = nginx $uri
	VarArgs     = "opm_args"     // current query = nginx $args
	VarIsArgs   = "opm_is_args"  // "?" or ""
	VarQuery    = "opm_query"    // "?args" or "" (OPM {query})
	VarCaps     = "opm_caps"     // []string captures of the final route's regex
)

func strVar(r *http.Request, k string) (string, bool) {
	v := caddyhttp.GetVar(r.Context(), k)
	s, ok := v.(string)
	return s, ok
}

// requestVars builds edgeres.Vars from the request (uri / args from the opm_route vars when set).
func requestVars(r *http.Request) edgeres.Vars {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	ra := r.RemoteAddr
	if h, _, err := net.SplitHostPort(ra); err == nil {
		ra = h
	}
	var port *string
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if _, p, err := net.SplitHostPort(la.String()); err == nil {
			port = &p
		}
	}
	hdr := map[string]string{}
	for k, v := range r.Header {
		hdr[strings.ToLower(k)] = strings.Join(v, ",")
	}
	uri := r.URL.Path
	if uri == "" {
		uri = "/"
	}
	v := edgeres.Vars{URI: uri, Args: r.URL.RawQuery, RequestURI: r.RequestURI, Host: host, Scheme: scheme, Captures: []string{}, RemoteAddr: &ra, ServerPort: port, Headers: hdr}
	if v.RequestURI == "" {
		v.RequestURI = r.URL.RequestURI()
	}
	if s, ok := strVar(r, VarURI); ok {
		v.URI = s
	}
	if s, ok := strVar(r, VarArgs); ok {
		v.Args = s
	}
	if c, ok := caddyhttp.GetVar(r.Context(), VarCaps).([]string); ok {
		v.Captures = c
	}
	return v
}

// ───────────────────────── opm_route

// RouteHandler is http.handlers.opm_route: nginx location selection (+ internal rewrites, auto-redirect) for one host.
type RouteHandler struct {
	Routes []model.EdgeRoute `json:"routes"`
}

// CaddyModule returns the module information.
func (RouteHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.opm_route", New: func() caddy.Module { return new(RouteHandler) }}
}

func (h RouteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	v := requestVars(r)
	res := edgeres.Walk(h.Routes, r.Method, &v)
	ctx := r.Context()
	caddyhttp.SetVar(ctx, VarURI, v.URI)
	caddyhttp.SetVar(ctx, VarArgs, v.Args)
	isArgs, q := "", ""
	if v.Args != "" {
		isArgs, q = "?", "?"+v.Args
	}
	caddyhttp.SetVar(ctx, VarIsArgs, isArgs)
	caddyhttp.SetVar(ctx, VarQuery, q)
	if res.Sel != nil {
		caddyhttp.SetVar(ctx, VarRoute, res.Sel.Route.RouteID)
	}
	switch res.Outcome {
	case edgeres.WalkNotFound:
		return caddyhttp.Error(http.StatusNotFound, fmt.Errorf("no route for %s", v.URI))
	case edgeres.WalkRewriteLoop:
		return caddyhttp.Error(http.StatusInternalServerError, fmt.Errorf("more than %d internal rewrites", edgeres.MaxRewrites))
	case edgeres.WalkAuto:
		caddyhttp.SetVar(ctx, VarAuto, res.Sel.Route.RouteID)
		caddyhttp.SetVar(ctx, VarLocation, edgeres.AutoRedirectLocation(&v))
	default:
		caddyhttp.SetVar(ctx, VarCaps, res.Sel.Captures)
	}
	return next.ServeHTTP(w, r)
}

// ───────────────────────── opm_redirect

// RedirectHandler is http.handlers.opm_redirect: a redirect route (variables + captures; keepPath) or, with auto, the
// nginx auto-redirect 301 to opm_location.
type RedirectHandler struct {
	To       string `json:"to,omitempty"`
	Status   int    `json:"status"`
	KeepPath bool   `json:"keepPath,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	Auto     bool   `json:"auto,omitempty"`
}

// CaddyModule returns the module information.
func (RedirectHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.opm_redirect", New: func() caddy.Module { return new(RedirectHandler) }}
}

func (h RedirectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	status := h.Status
	if status == 0 {
		status = http.StatusFound
	}
	var loc string
	if h.Auto {
		loc, _ = strVar(r, VarLocation)
	} else {
		v := requestVars(r)
		q := ""
		if v.Args != "" {
			q = "?" + v.Args
		}
		loc = edgeres.RedirectLocation(h.To, h.KeepPath, h.Prefix, v.URI, q, v.Host, &v)
	}
	w.Header().Set("Location", loc)
	w.WriteHeader(status)
	return nil
}

// ───────────────────────── opm_upstream

// UpstreamHandler is http.handlers.opm_upstream: the upstream path + query of a proxy route (edgeres.UpstreamPathQuery:
// rewrite … break or stripPrefix, then addPrefix). An unchanged path keeps the request's raw encoding.
type UpstreamHandler struct {
	Matched     string             `json:"matched"`
	StripPrefix bool               `json:"stripPrefix,omitempty"`
	Rewrite     *model.EdgeRewrite `json:"rewrite,omitempty"`
	AddPrefix   *string            `json:"addPrefix,omitempty"`
}

// CaddyModule returns the module information.
func (UpstreamHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.opm_upstream", New: func() caddy.Module { return new(UpstreamHandler) }}
}

func (h UpstreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	v := requestVars(r)
	a := model.EdgeRouteAction{Type: "proxy", StripPrefix: h.StripPrefix, Rewrite: h.Rewrite, AddPrefix: h.AddPrefix}
	hv := v
	hv.Captures = []string{}
	p, args, _ := edgeres.UpstreamPathQuery(&a, h.Matched, v.URI, hv)
	if p != r.URL.Path {
		r.URL.Path = p
		r.URL.RawPath = ""
	}
	if args != r.URL.RawQuery {
		r.URL.RawQuery = args
	}
	r.RequestURI = r.URL.RequestURI()
	return next.ServeHTTP(w, r)
}

// ───────────────────────── opm_body_limit

// ErrBodyTooLarge is returned by the limited request body.
var ErrBodyTooLarge = errors.New("request body is larger than the edge limit")

// BodyLimit is http.handlers.opm_body_limit (nginx client_max_body_size): Content-Length above max → 413 before the
// upstream is contacted; a chunked body that grows past max → 413 when nothing was sent to the client yet, else the
// connection is aborted.
type BodyLimit struct {
	Max int64 `json:"max"`
}

// CaddyModule returns the module information.
func (BodyLimit) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.opm_body_limit", New: func() caddy.Module { return new(BodyLimit) }}
}

type limitedBody struct {
	rc      io.ReadCloser
	left    int64
	tripped atomic.Bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.tripped.Load() {
		return 0, ErrBodyTooLarge
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.rc.Read(p)
	if int64(n) > b.left {
		b.tripped.Store(true)
		return 0, ErrBodyTooLarge
	}
	b.left -= int64(n)
	return n, err
}
func (b *limitedBody) Close() error { return b.rc.Close() }

type trackWriter struct {
	*caddyhttp.ResponseWriterWrapper
	wrote atomic.Bool
}

func (t *trackWriter) WriteHeader(s int) {
	if s >= 200 {
		t.wrote.Store(true)
	}
	t.ResponseWriterWrapper.WriteHeader(s)
}
func (t *trackWriter) Write(b []byte) (int, error) {
	t.wrote.Store(true)
	return t.ResponseWriterWrapper.Write(b)
}

func (h BodyLimit) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if h.Max <= 0 || r.Body == nil || r.Body == http.NoBody {
		return next.ServeHTTP(w, r)
	}
	if r.ContentLength > h.Max {
		return caddyhttp.Error(http.StatusRequestEntityTooLarge, fmt.Errorf("Content-Length %d is above the limit %d", r.ContentLength, h.Max))
	}
	if r.ContentLength >= 0 {
		return next.ServeHTTP(w, r)
	}
	lb := &limitedBody{rc: r.Body, left: h.Max}
	r.Body = lb
	tw := &trackWriter{ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w}}
	err := next.ServeHTTP(tw, r)
	if lb.tripped.Load() {
		if !tw.wrote.Load() {
			// the rest of the body is not read: the client connection cannot be reused
			w.Header().Set("Connection", "close")
			return caddyhttp.Error(http.StatusRequestEntityTooLarge, ErrBodyTooLarge)
		}
		panic(http.ErrAbortHandler)
	}
	return err
}

// ───────────────────────── transport opm_http

// OPMTransport is http.reverse_proxy.transport.opm_http: Caddy's http transport whose upstream connections carry nginx
// proxy_read_timeout / proxy_send_timeout semantics. read_idle_timeout: the time allowed between two reads from the
// upstream; it only runs while nothing is being written (a request body that is still being sent, or a websocket
// client that keeps sending, re-arms it — nginx re-arms the upstream timer on activity too). write_idle_timeout: the time
// allowed for one write to the upstream to make progress. Caddy's own read_timeout / write_timeout are plain per-call
// deadlines, which would cut a long upload while the transport waits for the response.
type OPMTransport struct {
	reverseproxy.HTTPTransport
	ReadIdleTimeout  caddy.Duration `json:"read_idle_timeout,omitempty"`
	WriteIdleTimeout caddy.Duration `json:"write_idle_timeout,omitempty"`
}

// CaddyModule returns the module information.
func (OPMTransport) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.reverse_proxy.transport.opm_http", New: func() caddy.Module { return new(OPMTransport) }}
}

// Provision provisions Caddy's transport, then wraps its dialer.
func (t *OPMTransport) Provision(ctx caddy.Context) error {
	if err := t.HTTPTransport.Provision(ctx); err != nil {
		return err
	}
	dial := t.HTTPTransport.Transport.DialContext
	rd, wr := time.Duration(t.ReadIdleTimeout), time.Duration(t.WriteIdleTimeout)
	if rd <= 0 && wr <= 0 {
		return nil
	}
	t.HTTPTransport.Transport.DialContext = func(c context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(c, network, addr)
		if err != nil {
			return nil, err
		}
		return &idleConn{Conn: conn, read: rd, write: wr}, nil
	}
	return nil
}

// idleConn implements the nginx-like idle timeouts on one upstream connection.
type idleConn struct {
	net.Conn
	read, write time.Duration
	writing     atomic.Int32
	lastWrite   atomic.Int64 // unix nanos of the last write activity
}

func (c *idleConn) Read(b []byte) (int, error) {
	for {
		if c.read > 0 {
			_ = c.Conn.SetReadDeadline(time.Now().Add(c.read))
		}
		n, err := c.Conn.Read(b)
		if n == 0 && err != nil && c.read > 0 {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if c.writing.Load() > 0 || time.Since(time.Unix(0, c.lastWrite.Load())) < c.read {
					continue // the request is still being sent: the read timer starts after the last write
				}
			}
		}
		return n, err
	}
}

func (c *idleConn) Write(b []byte) (int, error) {
	c.writing.Add(1)
	defer func() {
		c.lastWrite.Store(time.Now().UnixNano())
		c.writing.Add(-1)
	}()
	if c.write <= 0 {
		return c.Conn.Write(b)
	}
	total := 0
	for total < len(b) {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(c.write))
		n, err := c.Conn.Write(b[total:])
		total += n
		if err != nil {
			var ne net.Error
			if n > 0 && errors.As(err, &ne) && ne.Timeout() {
				c.lastWrite.Store(time.Now().UnixNano())
				continue // progress was made: the send timer restarts (idle between two writes)
			}
			return total, err
		}
	}
	return total, nil
}

// Interface guards.
var (
	_ caddyhttp.MiddlewareHandler = (*RouteHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*RedirectHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*UpstreamHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*BodyLimit)(nil)
	_ caddy.Provisioner           = (*OPMTransport)(nil)
	_ http.RoundTripper           = (*OPMTransport)(nil)
	_ caddy.CleanerUpper          = (*OPMTransport)(nil)
)
