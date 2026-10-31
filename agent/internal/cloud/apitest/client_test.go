package apitest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"opm/agent/internal/cloud"
)

var endpoints = func() map[string]cloud.Endpoint {
	m := map[string]cloud.Endpoint{}
	for _, e := range cloud.Endpoints() {
		m[e.Op] = e
	}
	return m
}()

// APIError is OpmApiError of shared/src/http.ts.
type APIError struct {
	Status  int
	Code    string
	Message string
	Details J
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s/%d: %s %s", e.Code, e.Status, e.Message, e.Details)
}

// AuthProvider returns the headers of one request.
type AuthProvider func(method, path string, body []byte) map[string]string

// Client is createCloudClient: operations by the name of the endpoint table.
type Client struct {
	Auth AuthProvider
	Base string // "" = the test server
}

// Bearer is a client with a fixed Authorization header value "Bearer <tok>".
func Bearer(tok string) *Client {
	return &Client{Auth: func(string, string, []byte) map[string]string {
		return map[string]string{"Authorization": "Bearer " + tok}
	}}
}

// Anon sends no credentials.
func Anon() *Client { return &Client{} }

var pathParam = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

func buildQuery(q any) string {
	m, _ := q.(A)
	if m == nil {
		return ""
	}
	v := url.Values{}
	for k, x := range m {
		switch y := x.(type) {
		case nil:
		case bool:
			if y {
				v.Set(k, "1")
			} else {
				v.Set(k, "0")
			}
		default:
			v.Set(k, fmt.Sprint(Plain(x)))
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + strings.ReplaceAll(v.Encode(), "+", "%20")
}

// Send calls an operation and returns the raw response (redirects followed, like fetch), or the error of a non-2xx.
// args: the path parameters by name, plus "query" (A), "body" (JSON op: anything JSON-shaped; raw op: []byte / string),
// "headers" (map[string]string), "contentType".
func (c *Client) Send(op string, args A) (*Resp, *APIError) {
	def, ok := endpoints[op]
	if !ok {
		panic("no operation " + op)
	}
	path := pathParam.ReplaceAllStringFunc(def.Path, func(m string) string {
		k := m[1 : len(m)-1]
		v, ok := args[k]
		if !ok || v == nil || v == "" {
			panic("missing path parameter " + k + " for " + def.Path)
		}
		return url.PathEscape(fmt.Sprint(Plain(v)))
	})
	path += buildQuery(args["query"])
	hdr := map[string]string{}
	if h, ok := args["headers"].(map[string]string); ok {
		for k, v := range h {
			hdr[k] = v
		}
	}
	var body []byte
	switch def.Body {
	case "json":
		b := args["body"]
		if b == nil {
			b = A{}
		}
		var err error
		if body, err = json.Marshal(Plain(b)); err != nil {
			panic(err)
		}
		hdr["Content-Type"] = "application/json"
	case "raw":
		switch x := args["body"].(type) {
		case []byte:
			body = x
		case string:
			body = []byte(x)
		case nil:
		default:
			panic(fmt.Sprintf("raw body of %s: %T", op, x))
		}
		hdr["Content-Type"] = "application/octet-stream"
		if ct, ok := args["contentType"].(string); ok {
			hdr["Content-Type"] = ct
		}
	}
	if c.Auth != nil {
		for k, v := range c.Auth(def.Method, path, body) {
			hdr[k] = v
		}
	}
	base := c.Base
	if base == "" {
		base = Base
	}
	req, err := http.NewRequest(def.Method, base+path, bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rs, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &APIError{Status: 0, Code: "network", Message: err.Error(), Details: J{A{}}}
	}
	r := readResp(rs)
	if r.Status < 200 || r.Status >= 300 {
		e := r.J.Key("error")
		ae := &APIError{Status: r.Status, Code: "internal", Message: rs.Status, Details: J{A{}}}
		if e.At("code").Truthy() {
			ae.Code = e.At("code").Str()
		}
		if e.At("message").Truthy() {
			ae.Message = e.At("message").Str()
		}
		if e.At("details").Truthy() {
			ae.Details = e.At("details")
		}
		return r, ae
	}
	return r, nil
}

// Try calls a JSON operation: its result, or the API error.
func (c *Client) Try(op string, args A) (J, *APIError) {
	r, e := c.Send(op, args)
	if e != nil {
		return J{Undefined}, e
	}
	return r.J, nil
}

// Call is the JSON operation that must succeed.
func (c *Client) Call(t testing.TB, op string, args A) J {
	t.Helper()
	j, e := c.Try(op, args)
	if e != nil {
		t.Fatalf("%s: %v", op, e)
	}
	return j
}

// Fetch is the raw operation (res raw / text) that must succeed.
func (c *Client) Fetch(t testing.TB, op string, args A) *Resp {
	t.Helper()
	r, e := c.Send(op, args)
	if e != nil {
		t.Fatalf("%s: %v", op, e)
	}
	return r
}

// Rejects is rejects(client.op(args), code, status?).
func (c *Client) Rejects(t testing.TB, op string, args A, code string, status ...int) *APIError {
	t.Helper()
	_, e := c.Try(op, args)
	return Rejected(t, e, code, status...)
}

// Rejected is rejects() on the error of a call already made.
func Rejected(t testing.TB, e *APIError, code string, status ...int) *APIError {
	t.Helper()
	if e == nil {
		t.Fatalf("expected error %s, but the call succeeded", code)
	}
	if e.Code != code || (len(status) > 0 && e.Status != status[0]) {
		t.Fatalf("expected %s %v, got %v", code, status, e)
	}
	return e
}

// Resp is a raw response.
type Resp struct {
	Status  int
	Headers http.Header
	Body    []byte
	J       J // the body as JSON (Undefined when it is none)
}

// Text is the body as a string.
func (r *Resp) Text() string { return string(r.Body) }

// Header is headers.get(name): "" when absent.
func (r *Resp) Header(name string) string { return r.Headers.Get(name) }

func readResp(rs *http.Response) *Resp {
	defer rs.Body.Close()
	b, _ := io.ReadAll(rs.Body)
	return &Resp{Status: rs.StatusCode, Headers: rs.Header, Body: b, J: ParseJ(b)}
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// Opt is the init of Raw.
type Opt struct {
	Headers map[string]string
	Body    []byte
}

// Raw is raw(): a plain request against the server, redirects not followed. path may be an absolute URL.
func Raw(t testing.TB, method, path string, o ...Opt) *Resp {
	t.Helper()
	var op Opt
	if len(o) > 0 {
		op = o[0]
	}
	u := path
	if !strings.HasPrefix(u, "http") {
		u = Base + path
	}
	var rd io.Reader
	if op.Body != nil {
		rd = bytes.NewReader(op.Body)
	}
	req, err := http.NewRequest(method, u, rd)
	NoErr(t, err)
	for k, v := range op.Headers {
		req.Header.Set(k, v)
	}
	rs, err := noRedirect.Do(req)
	NoErr(t, err)
	return readResp(rs)
}

// TinyScript is tinyScript(): §21 #91 an unusable install token gets 200 + a tiny script that prints why and exits 1.
func TinyScript(t testing.TB, r *Resp, re string) {
	t.Helper()
	if r.Status != 200 {
		t.Fatalf("expected 200 tiny script, got %d", r.Status)
	}
	b := r.Text()
	if !strings.HasPrefix(b, "#!/bin/sh") || !strings.HasSuffix(b, "\nexit 1\n") || !regexp.MustCompile(re).MatchString(b) {
		t.Fatalf("not the expected tiny script (%s):\n%s", re, b)
	}
	NotContains(t, b, "AGENT_VERSION=", "a real install script was served")
}

// Itoa is strconv.Itoa for short test bodies.
func Itoa(n int) string { return strconv.Itoa(n) }
