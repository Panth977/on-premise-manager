package devtools

// HTTP client (Postman-like) run from the site server: environment {{name}} + OPM expressions resolved on the site,
// connection auth / default headers, httptrace timing, ≤ 10 redirects (chain returned), tlsInsecure, response body up
// to maxResponseBytes (inline ≤ inlineResponseBytes, the rest via results/{id}/body). Never the agent's own API
// (/_opm/api, /_opm/peer on any host), never an opm_session* cookie, never the browser's cookies / headers.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

type httpClients struct {
	secure, insecure *http.Transport
}

func newTransport(insecure bool, serverName string) *http.Transport {
	return &http.Transport{
		Proxy:                 nil, // the site server's own network, never a proxy from the agent environment
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecure, ServerName: serverName},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    false,
	}
}

func (s *Service) connectHTTP(rc *resolvedConn) *client {
	sn := deref(rc.def.Options.TLSServerName)
	hc := &httpClients{secure: newTransport(rc.def.Options.TLSInsecure, sn), insecure: newTransport(true, sn)}
	return &client{kind: "http", value: hc, close: func() { hc.secure.CloseIdleConnections(); hc.insecure.CloseIdleConnections() }}
}

// isAgentAPIPath: /_opm/api/* and /_opm/peer/* (and the bare prefixes) on any host.
func isAgentAPIPath(p string) bool {
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	p = strings.ToLower(path.Clean("/" + p))
	for _, pre := range []string{"/_opm/api", "/_opm/peer"} {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

// dropSessionCookies removes opm_session* pairs from a Cookie header value ("" = nothing left).
func dropSessionCookies(v string) string {
	var keep []string
	for _, part := range strings.Split(v, ";") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		name, _, _ := strings.Cut(p, "=")
		if strings.HasPrefix(strings.TrimSpace(name), "opm_session") {
			continue
		}
		keep = append(keep, p)
	}
	return strings.Join(keep, "; ")
}

// placeholders resolves {{name}} (environment) then OPM expressions ({{ secret.x }} …) on the site.
type placeholders struct {
	s      *Service
	vars   map[string]string
	masker *Masker
}

func (p *placeholders) sub(text, what string) (string, *Error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	t := ApplyEnvironment(text, p.vars)
	var ferr *Error
	out := rePlaceholder.ReplaceAllStringFunc(t, func(m string) string {
		if ferr != nil {
			return m
		}
		inner := rePlaceholder.FindStringSubmatch(m)[1]
		if !IsOpmPlaceholder(inner) {
			return m // an unknown {{name}} is sent as is
		}
		v, secret, e := p.s.resolveValue("{{ "+strings.TrimSpace(inner)+" }}", what)
		if e != nil {
			ferr = e
			return m
		}
		if secret {
			p.masker = p.masker.Add(v)
		}
		return v
	})
	return out, ferr
}

func (s *Service) environmentVars(id string) (map[string]string, []string, *Error) {
	e, err := s.getEnv(id)
	if err != nil {
		return nil, nil, notFound("environment %s does not exist", id)
	}
	vars := map[string]string{}
	var secrets []string
	for _, v := range e.Env.Variables {
		if !v.Enabled {
			continue
		}
		val := v.Value
		if v.Secret {
			val = e.Secrets[v.Name]
			secrets = append(secrets, val)
		}
		vars[v.Name] = val
	}
	return vars, secrets, nil
}

func isTextType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "xml") || strings.Contains(ct, "javascript") ||
		strings.Contains(ct, "x-www-form-urlencoded") || strings.Contains(ct, "yaml") || strings.Contains(ct, "csv") || strings.Contains(ct, "graphql")
}

func sensitiveHeader(k string) bool {
	return inList(SensitiveHeaders, strings.ToLower(k)) || SecretNameRe.MatchString(k)
}

func maskHeaderValue(k, v string) string {
	if !sensitiveHeader(k) {
		return v
	}
	if sp := strings.IndexByte(v, ' '); sp > 0 && reAuthScheme.MatchString(v[:sp]) {
		return v[:sp] + " " + Redacted
	}
	return Redacted
}

func (s *Service) handleExecuteHTTP(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.connOfKind(r.PathValue("connectionId"), "http")
	if e != nil {
		return nil, 0, e
	}
	var req ExecuteHTTPRequest
	if e := readBody(r, &req, 48<<20); e != nil {
		return nil, 0, e
	}
	spec := req.Request
	method := strings.ToUpper(strings.TrimSpace(spec.Method))
	if method == "" {
		method = "GET"
	}
	if !inList([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}, method) {
		return nil, 0, invalid("method must be GET | POST | PUT | PATCH | DELETE | HEAD | OPTIONS")
	}
	if d.ReadOnly && !inList([]string{"GET", "HEAD", "OPTIONS"}, method) {
		return nil, 0, forbidden("%q is read-only: only GET, HEAD and OPTIONS are sent", d.Name)
	}
	if e := s.allow("execute", c); e != nil {
		return nil, 0, e
	}
	start := time.Now()
	masker := NewMasker(s.h.SecretValues())
	ql := &queryLog{d: d, op: "http", statements: 1, start: start, queryID: req.QueryID, target: targetText(d.Target),
		statement: TruncateUTF8(masker.Mask(redactHTTPRaw(method, spec.URL, nil, 0)), StatementLogMax)}
	fail := func(e *Error) (any, int, error) {
		ql.errText = e.Msg
		s.logQuery(c, ql, masker)
		return nil, 0, e
	}
	rc, e := s.resolve(r.Context(), d)
	if e != nil {
		return fail(e)
	}
	masker = rc.masker
	ql.target = rc.targetText
	s.noteTarget(d.ID, rc.targetText)
	vars := map[string]string{}
	if req.EnvironmentID != nil && *req.EnvironmentID != "" {
		ev, sec, e := s.environmentVars(*req.EnvironmentID)
		if e != nil {
			return fail(e)
		}
		vars = ev
		masker = masker.Add(sec...)
	}
	for k, v := range req.Variables {
		vars[k] = v
	}
	ph := &placeholders{s: s, vars: vars, masker: masker}
	rawURL, e := ph.sub(strings.TrimSpace(spec.URL), "url")
	if e != nil {
		return fail(e)
	}
	if !(strings.HasPrefix(strings.ToLower(rawURL), "http://") || strings.HasPrefix(strings.ToLower(rawURL), "https://")) {
		if rc.baseURL == "" {
			return fail(invalid("a relative URL needs a connection with a container / host / url target"))
		}
		if rawURL != "" && !strings.HasPrefix(rawURL, "?") {
			rawURL = strings.TrimRight(rc.baseURL, "/") + "/" + strings.TrimLeft(rawURL, "/")
		} else {
			rawURL = rc.baseURL + rawURL
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fail(invalid("%q is not an absolute http(s) URL", masker.Mask(rawURL)))
	}
	extra := url.Values{}
	for _, kv := range spec.Query {
		if !kv.Enabled || kv.Name == "" {
			continue
		}
		n, e := ph.sub(kv.Name, "query "+kv.Name)
		if e != nil {
			return fail(e)
		}
		v, e := ph.sub(kv.Value, "query "+kv.Name)
		if e != nil {
			return fail(e)
		}
		extra.Add(n, v)
	}
	// connection / request auth
	type authT struct{ scheme, user, pass, token, header, in string }
	au := authT{scheme: d.Auth.HTTPScheme, user: rc.user, pass: rc.password, token: rc.token, header: rc.apiKeyHeader, in: "header"}
	if spec.Auth != nil {
		au = authT{scheme: spec.Auth.Scheme, user: deref(spec.Auth.Username), pass: deref(spec.Auth.Password), token: deref(spec.Auth.Token), header: deref(spec.Auth.APIKeyHeader), in: spec.Auth.APIKeyIn}
		for _, f := range []*string{&au.user, &au.pass, &au.token, &au.header} {
			v, e := ph.sub(*f, "auth")
			if e != nil {
				return fail(e)
			}
			*f = v
		}
		ph.masker = ph.masker.Add(au.pass, au.token)
	}
	if au.header == "" {
		au.header = "X-API-Key"
	}
	if au.scheme == "api-key" && au.in == "query" {
		extra.Set(au.header, au.token)
	}
	if enc := extra.Encode(); enc != "" {
		if u.RawQuery != "" {
			u.RawQuery += "&"
		}
		u.RawQuery += enc
	}
	if isAgentAPIPath(u.Path) {
		return fail(forbidden("%s is the agent's own API: the dev tools never call /_opm/api or /_opm/peer", u.Path))
	}
	// body
	var body []byte
	ctype := ""
	b := spec.Body
	switch b.Kind {
	case "", "none":
	case "json", "text":
		t, e := ph.sub(b.Text, "body")
		if e != nil {
			return fail(e)
		}
		body = []byte(t)
		if b.Kind == "json" {
			ctype = "application/json"
		} else if b.ContentType != nil && *b.ContentType != "" {
			ctype = *b.ContentType
		} else {
			ctype = "text/plain; charset=utf-8"
		}
	case "form":
		vals := url.Values{}
		for _, f := range b.Fields {
			if !f.Enabled {
				continue
			}
			v, e := ph.sub(f.Value, "form "+f.Name)
			if e != nil {
				return fail(e)
			}
			vals.Add(f.Name, v)
		}
		body, ctype = []byte(vals.Encode()), "application/x-www-form-urlencoded"
	case "multipart":
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for _, f := range b.Fields {
			if !f.Enabled {
				continue
			}
			var val []byte
			if f.Base64 {
				x, err := base64.StdEncoding.DecodeString(f.Value)
				if err != nil {
					return fail(invalid("multipart field %s: invalid base64", f.Name))
				}
				val = x
			} else {
				v, e := ph.sub(f.Value, "multipart "+f.Name)
				if e != nil {
					return fail(e)
				}
				val = []byte(v)
			}
			var pw io.Writer
			if f.FileName != nil && *f.FileName != "" {
				pw, err = mw.CreateFormFile(f.Name, *f.FileName)
			} else {
				pw, err = mw.CreateFormField(f.Name)
			}
			if err != nil {
				return fail(invalid("multipart: %s", err.Error()))
			}
			_, _ = pw.Write(val)
		}
		_ = mw.Close()
		body, ctype = buf.Bytes(), mw.FormDataContentType()
	case "binary":
		x, err := base64.StdEncoding.DecodeString(b.Base64)
		if err != nil {
			return fail(invalid("binary body: invalid base64"))
		}
		body, ctype = x, "application/octet-stream"
		if b.ContentType != nil && *b.ContentType != "" {
			ctype = *b.ContentType
		}
	default:
		return fail(invalid("body.kind must be none | json | text | form | multipart | binary"))
	}
	ctx, done, e := s.begin(r.Context(), c, d.ID, req.QueryID, s.timeoutFor(spec.TimeoutSec, rc))
	if e != nil {
		return nil, 0, e
	}
	defer done()
	hreq, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return fail(invalid("request: %s", masker.Mask(err.Error())))
	}
	hreq.Header = http.Header{}
	hreq.Header.Set("User-Agent", "opm-devtools")
	set := func(k, v string) {
		if strings.EqualFold(k, "Cookie") {
			v = dropSessionCookies(v)
			if v == "" {
				return
			}
		}
		if strings.EqualFold(k, "Host") {
			hreq.Host = v
			return
		}
		hreq.Header.Set(k, v)
	}
	for _, h := range rc.headers {
		set(h[0], h[1])
	}
	if ctype != "" {
		hreq.Header.Set("Content-Type", ctype)
	}
	for _, h := range spec.Headers {
		if !h.Enabled || strings.TrimSpace(h.Name) == "" {
			continue
		}
		v, e := ph.sub(h.Value, "header "+h.Name)
		if e != nil {
			return fail(e)
		}
		set(h.Name, v)
	}
	switch au.scheme {
	case "basic":
		hreq.SetBasicAuth(au.user, au.pass)
	case "bearer":
		hreq.Header.Set("Authorization", "Bearer "+au.token)
	case "api-key":
		if au.in != "query" {
			hreq.Header.Set(au.header, au.token)
		}
	}
	masker = ph.masker
	// what was sent (masked) → the activity statement + the response
	var sentHdr [][2]string
	for k, vs := range hreq.Header {
		for _, v := range vs {
			sentHdr = append(sentHdr, [2]string{k, v})
		}
	}
	sort.Slice(sentHdr, func(i, j int) bool { return sentHdr[i][0] < sentHdr[j][0] })
	ql.statement = TruncateUTF8(masker.Mask(redactHTTPRaw(method, u.String(), sentHdr, int64(len(body)))), StatementLogMax)
	maskedHdr := make([][2]string, len(sentHdr))
	for i, h := range sentHdr {
		maskedHdr[i] = [2]string{h[0], masker.Mask(maskHeaderValue(h[0], h[1]))}
	}
	maskedURL := masker.Mask(strings.SplitN(redactHTTPRaw(method, u.String(), nil, 0), " ", 2)[1])
	out := ExecuteHTTPResponse{QueryID: req.QueryID, Redirects: []HTTPRedirect{}, Headers: [][2]string{}, Cookies: []HTTPCookie{},
		Sent: HTTPSent{Method: method, URL: maskedURL, Headers: maskedHdr, BodyBytes: int64(len(body))}}
	follow := true
	if d.Options.FollowRedirects != nil {
		follow = *d.Options.FollowRedirects
	}
	if spec.FollowRedirects != nil {
		follow = *spec.FollowRedirects
	}
	insecure := d.Options.TLSInsecure
	if spec.TLSInsecure != nil {
		insecure = *spec.TLSInsecure
	}
	// timing (first occurrence of each phase)
	var tDNS0, tDNS1, tConn0, tConn1, tTLS0, tTLS1, tFirst time.Time
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { setOnce(&tDNS0) },
		DNSDone:              func(httptrace.DNSDoneInfo) { setOnce(&tDNS1) },
		ConnectStart:         func(string, string) { setOnce(&tConn0) },
		ConnectDone:          func(string, string, error) { setOnce(&tConn1) },
		TLSHandshakeStart:    func() { setOnce(&tTLS0) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { setOnce(&tTLS1) },
		GotFirstResponseByte: func() { tFirst = time.Now() },
	}
	hreq = hreq.WithContext(httptrace.WithClientTrace(ctx, trace))
	reqStart := time.Now()
	var resp *http.Response
	var bodyBytes []byte
	bodyCut := false
	var redirectRefused error
	_, err = s.withClient(ctx, rc, func(cl *client) error {
		hc := cl.value.(*httpClients)
		tr := hc.secure
		if insecure {
			tr = hc.insecure
		}
		out.Redirects = []HTTPRedirect{}
		hcl := &http.Client{Transport: tr, CheckRedirect: func(nr *http.Request, via []*http.Request) error {
			if !follow {
				return http.ErrUseLastResponse
			}
			st := 0
			if nr.Response != nil {
				st = nr.Response.StatusCode
			}
			out.Redirects = append(out.Redirects, HTTPRedirect{Status: st, URL: masker.Mask(nr.URL.String())})
			if isAgentAPIPath(nr.URL.Path) {
				redirectRefused = fmt.Errorf("redirect to %s refused: the agent's own API", nr.URL.Path)
				return redirectRefused
			}
			if len(via) > 10 {
				return http.ErrUseLastResponse
			}
			nr.Header.Del("Cookie")
			return nil
		}}
		if len(body) > 0 {
			hreq.Body = io.NopCloser(bytes.NewReader(body))
			hreq.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		}
		rsp, err := hcl.Do(hreq)
		if err != nil {
			return err
		}
		resp = rsp
		maxB := s.limits().MaxResponseBytes
		bodyBytes, err = io.ReadAll(io.LimitReader(rsp.Body, maxB+1))
		rsp.Body.Close()
		if int64(len(bodyBytes)) > maxB {
			bodyBytes, bodyCut = bodyBytes[:maxB], true
		}
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil // a body cut by the server: keep what arrived
		}
		return nil
	})
	end := time.Now()
	ms := func(a, b time.Time) *int64 {
		if a.IsZero() || b.IsZero() {
			return nil
		}
		v := b.Sub(a).Milliseconds()
		return &v
	}
	out.Timing = HTTPTiming{DNSMs: ms(tDNS0, tDNS1), ConnectMs: ms(tConn0, tConn1), TLSMs: ms(tTLS0, tTLS1), TTFBMs: ms(reqStart, tFirst), DownloadMs: ms(tFirst, end), TotalMs: end.Sub(reqStart).Milliseconds()}
	if err != nil || resp == nil {
		msg := "request failed"
		if err != nil {
			msg = masker.Mask(err.Error())
		}
		if redirectRefused != nil {
			msg = redirectRefused.Error()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg = "timed out: " + msg
		}
		out.Error = &msg
		ql.errText = msg
		out.ActivityID = s.logQuery(c, ql, masker)
		return out, 200, nil
	}
	out.OK = true
	st := resp.StatusCode
	out.Status = &st
	stText := strings.TrimSpace(strings.TrimPrefix(resp.Status, fmt.Sprint(st)))
	if stText == "" {
		stText = http.StatusText(st)
	}
	out.StatusText = &stText
	out.Protocol = strPtr(resp.Proto)
	fu := masker.Mask(resp.Request.URL.String())
	out.FinalURL = &fu
	var hb int64
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range resp.Header[k] {
			out.Headers = append(out.Headers, [2]string{k, v})
			hb += int64(len(k) + len(v) + 4)
		}
	}
	out.HeaderBytes = hb
	for _, ck := range resp.Cookies() {
		hc := HTTPCookie{Name: ck.Name, Value: ck.Value, Domain: strOrNil(ck.Domain), Path: strOrNil(ck.Path), HTTPOnly: ck.HttpOnly, Secure: ck.Secure}
		if !ck.Expires.IsZero() {
			hc.Expires = strPtr(ts(ck.Expires))
		}
		out.Cookies = append(out.Cookies, hc)
	}
	ct := resp.Header.Get("Content-Type")
	out.ContentType = strOrNil(ct)
	out.SizeBytes = int64(len(bodyBytes))
	out.BodyTruncated = bodyCut
	if int64(len(bodyBytes)) <= s.limits().InlineResponseBytes {
		if isTextType(ct) || ct == "" {
			out.BodyText, out.BodyBase64 = textOrBase64(bodyBytes)
		} else {
			b64 := base64.StdEncoding.EncodeToString(bodyBytes)
			out.BodyBase64 = &b64
		}
	}
	if len(bodyBytes) > 0 {
		name := path.Base(resp.Request.URL.Path)
		if name == "/" || name == "." || name == "" {
			name = "response"
		}
		id := s.results.put(&result{owner: c.key(), body: bodyBytes, contentType: ct, fileName: safeFileName(name)})
		out.ResultID = &id
	}
	ql.ok, ql.status = true, &st
	out.ActivityID = s.logQuery(c, ql, masker)
	return out, 200, nil
}

func setOnce(t *time.Time) {
	if t.IsZero() {
		*t = time.Now()
	}
}

// pingHTTP is the Test of an HTTP connection: one GET of the base URL (any status = reachable).
func (s *Service) pingHTTP(ctx context.Context, rc *resolvedConn, cl *client) (string, error) {
	if rc.baseURL == "" {
		return "", errors.New("no base URL")
	}
	u, err := url.Parse(rc.baseURL)
	if err != nil {
		return "", err
	}
	if isAgentAPIPath(u.Path) {
		return "", errors.New("the agent's own API is never called")
	}
	hc := cl.value.(*httpClients)
	req, _ := http.NewRequestWithContext(ctx, "GET", rc.baseURL, nil)
	req.Header.Set("User-Agent", "opm-devtools")
	resp, err := (&http.Client{Transport: hc.secure, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return "", err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	return resp.Header.Get("Server"), nil
}
