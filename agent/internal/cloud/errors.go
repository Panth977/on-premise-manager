package cloud

import (
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/model"
)

// Issue is a ValidationIssue.
type Issue = model.ValidationIssue

// AuditActor: an authenticated person without a usable account (403) is still audited with this actor.
type AuditActor struct {
	UID   string
	Email *string
}

// ApiError is a client-visible error: { error: { code, message, details } }.
type ApiError struct {
	Code       string
	Message    string
	Details    *doc.Map
	Headers    map[string]string
	AuditActor *AuditActor
}

func (e *ApiError) Error() string { return e.Message }

// Status is ERROR_HTTP_STATUS[code] ?? 500.
func (e *ApiError) Status() int {
	if s, ok := errorHTTPStatus[e.Code]; ok {
		return s
	}
	return 500
}

// E holds the constructors of core/errors.ts. An optional *doc.Map is the details object.
var E errFns

type errFns struct{}

func mk(code, msg, def string, d []*doc.Map) *ApiError {
	if msg == "" {
		msg = def
	}
	e := &ApiError{Code: code, Message: msg, Details: doc.New()}
	if len(d) > 0 && d[0] != nil {
		e.Details = d[0]
	}
	return e
}

// New is new ApiError(code, message, details, headers).
func (errFns) New(code, msg string, details *doc.Map, headers map[string]string) *ApiError {
	if details == nil {
		details = doc.New()
	}
	return &ApiError{Code: code, Message: msg, Details: details, Headers: headers}
}
func (errFns) Unauth(msg string, d ...*doc.Map) *ApiError {
	return mk("unauthenticated", msg, "not signed in", d)
}
func (errFns) Forbidden(msg string, d ...*doc.Map) *ApiError {
	return mk("forbidden", msg, "forbidden", d)
}
func (errFns) NotFound(what string) *ApiError { return mk("not-found", what, "not found", nil) }
func (errFns) Conflict(msg string, d ...*doc.Map) *ApiError {
	return mk("conflict", msg, "", d)
}
func (errFns) Gone(msg string, d ...*doc.Map) *ApiError { return mk("gone", msg, "", d) }
func (errFns) TooLarge(msg string) *ApiError            { return mk("too-large", msg, "", nil) }
func (errFns) Unavailable(msg string, d ...*doc.Map) *ApiError {
	return mk("unavailable", msg, "", d)
}
func (errFns) Stale(msg string, d ...*doc.Map) *ApiError { return mk("stale", msg, "", d) }

// Invalid is E.invalid(list, message?).
func (errFns) Invalid(list []Issue, msg ...string) *ApiError {
	m := "invalid request"
	if len(list) > 0 {
		m = list[0].Message
	}
	if len(msg) > 0 {
		m = msg[0]
	}
	return &ApiError{Code: "invalid", Message: m, Details: doc.Obj("errors", list)}
}

// InvalidMsg is E.invalid(string).
func (e errFns) InvalidMsg(msg string) *ApiError {
	return e.Invalid([]Issue{{Path: "", Code: "V_TYPE", Message: msg, Severity: "error"}})
}

// Bad is the one-issue shortcut.
func (errFns) Bad(path, code, msg string) *ApiError {
	return &ApiError{Code: "invalid", Message: msg, Details: doc.Obj("errors", []Issue{{Path: path, Code: code, Message: msg, Severity: "error"}})}
}
