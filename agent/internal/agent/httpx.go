package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"opm/agent/internal/model"
	"opm/agent/internal/ulid"
)

type ctxKey int

const rcKey ctxKey = 1

// reqCtx carries the caller of a request (session + actor, or a trusted peer forwarding an actor).
type reqCtx struct {
	surface   string
	sessionID string
	expiresAt int64
	actor     string
	ip        string
	ua        string
	peer      *peerCaller
	body      []byte
	bodyFile  string
	bodyLen   int64
	bodyHash  string
	// ff (§21 #95): the fast-forward record of the intent this request confirmed (nil = waited at ×1)
	ff *model.FastForwardUse
}

func (rc *reqCtx) actorPtr() *string {
	if rc == nil || rc.actor == "" {
		return nil
	}
	s := rc.actor
	return &s
}

func (rc *reqCtx) reader() (io.ReadCloser, error) {
	if rc.bodyFile != "" {
		return os.Open(rc.bodyFile)
	}
	return io.NopCloser(bytes.NewReader(rc.body)), nil
}

func getRC(r *http.Request) *reqCtx {
	if rc, ok := r.Context().Value(rcKey).(*reqCtx); ok {
		return rc
	}
	return &reqCtx{}
}

func withRC(r *http.Request, rc *reqCtx) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), rcKey, rc))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status, b = 500, []byte(`{"error":{"code":"internal","message":"encode failed"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// httpError is an API error with code and details.
type httpError struct {
	code    string
	msg     string
	details map[string]any
	header  map[string]string
}

func (e *httpError) Error() string { return e.code + ": " + e.msg }

func herr(code, msg string, details map[string]any) *httpError {
	return &httpError{code: code, msg: msg, details: details}
}

func invalid(msg string, issues []model.ValidationIssue) *httpError {
	if issues == nil {
		issues = []model.ValidationIssue{{Path: "", Code: "V_TYPE", Message: msg, Severity: "error"}}
	}
	return herr(model.ErrInvalid, msg, map[string]any{"errors": issues})
}

func writeErr(w http.ResponseWriter, e *httpError) {
	st := model.ErrorHTTPStatus[e.code]
	if st == 0 {
		st = 500
	}
	for k, v := range e.header {
		w.Header().Set(k, v)
	}
	if e.code == model.ErrInternal {
		if e.details == nil {
			e.details = map[string]any{}
		}
		e.details["requestId"] = w.Header().Get("X-Request-Id")
	}
	writeJSON(w, st, model.APIErrorBody{Error: model.APIError{Code: e.code, Message: e.msg, Details: e.details}})
}

func toHTTPErr(err error) *httpError {
	var he *httpError
	if errors.As(err, &he) {
		return he
	}
	if ae := asAPIError(err); ae != nil && ae.Body.Error.Code != "" {
		return &httpError{code: ae.Body.Error.Code, msg: ae.Body.Error.Message, details: ae.Body.Error.Details}
	}
	if isNetErr(err) {
		return herr(model.ErrUnavailable, err.Error(), nil)
	}
	return herr(model.ErrInternal, err.Error(), nil)
}

func readJSON(r *http.Request, v any) *httpError {
	rc := getRC(r)
	var body io.Reader = io.LimitReader(r.Body, 10<<20)
	if rc.body != nil || rc.bodyFile != "" {
		rd, err := rc.reader()
		if err != nil {
			return herr(model.ErrInternal, err.Error(), nil)
		}
		defer rd.Close()
		body = rd
	}
	dec := json.NewDecoder(body)
	if err := dec.Decode(v); err != nil {
		return invalid("invalid JSON body: "+err.Error(), nil)
	}
	return nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func qInt(r *http.Request, k string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(k)); err == nil {
		return v
	}
	return def
}

func qBool(r *http.Request, k string) bool {
	v := strings.ToLower(r.URL.Query().Get(k))
	return v == "1" || v == "true" || v == "yes"
}

func reqID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", ulid.New())
		next.ServeHTTP(w, r)
	})
}

// spoolBody reads the body into memory (≤ 8 MiB) or a temp file, computing sha256 hex.
func (a *Agent) spoolBody(r *http.Request, max int64) (*reqCtx, *httpError) {
	rc := &reqCtx{}
	h := sha256.New()
	limit := max + 1
	if r.ContentLength >= 0 && r.ContentLength <= 8<<20 {
		b, err := io.ReadAll(io.LimitReader(r.Body, limit))
		if err != nil {
			return nil, herr(model.ErrInvalid, "read body: "+err.Error(), nil)
		}
		if int64(len(b)) > max {
			return nil, herr(model.ErrTooLarge, fmt.Sprintf("body larger than %d bytes", max), nil)
		}
		h.Write(b)
		rc.body, rc.bodyLen = b, int64(len(b))
		rc.bodyHash = hex.EncodeToString(h.Sum(nil))
		return rc, nil
	}
	f, err := os.CreateTemp(filepath.Join(a.dataDir, "tmp"), "body-*")
	if err != nil {
		return nil, herr(model.ErrInternal, err.Error(), nil)
	}
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r.Body, limit))
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return nil, herr(model.ErrInvalid, "read body: "+err.Error(), nil)
	}
	if n > max {
		os.Remove(f.Name())
		return nil, herr(model.ErrTooLarge, fmt.Sprintf("body larger than %d bytes", max), nil)
	}
	rc.bodyFile, rc.bodyLen = f.Name(), n
	rc.bodyHash = hex.EncodeToString(h.Sum(nil))
	return rc, nil
}

func (rc *reqCtx) cleanup() {
	if rc.bodyFile != "" {
		os.Remove(rc.bodyFile)
	}
}

// signedWriter buffers a JSON response so it can be signed (peer API).
type signedWriter struct {
	hdr    http.Header
	status int
	buf    bytes.Buffer
	raw    http.ResponseWriter
	direct bool
}

func (s *signedWriter) Header() http.Header { return s.hdr }
func (s *signedWriter) WriteHeader(st int) {
	if s.status == 0 {
		s.status = st
	}
}
func (s *signedWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = 200
	}
	return s.buf.Write(b)
}
