package rally

import (
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"runtime/debug"
	"strings"
)

// ErrorDetail describes one problem with a request, e.g. a validation error.
// Following the example of RFC 9457, a problem in the body is located with a
// JSON Pointer; a problem in a parameter with its location and name, as in
// OpenAPI. The value sent by the client is not echoed.
type ErrorDetail struct {
	Detail string `json:"detail"` // explanation of the problem
	// Code is machine-readable: the JSON Schema keyword that failed (type,
	// required, minLength...), additionalProperties for an unknown property
	// or query parameter, duplicate for a repeated one, format for a value its
	// type rejects (time, UUID...), syntax for malformed JSON or query string.
	Code      string `json:"code,omitempty"`
	In        string `json:"in,omitempty"`        // body, path, query or header
	Pointer   string `json:"pointer,omitempty"`   // JSON Pointer (RFC 6901) in the body; absent for the whole body
	Parameter string `json:"parameter,omitempty"` // name of the parameter with the problem
}

// Error returns the detail and where the problem is.
func (e *ErrorDetail) Error() string {
	where := e.In
	switch {
	case e.Parameter != "":
		where += " parameter " + e.Parameter
	case e.Pointer != "":
		where = strings.TrimSpace(where + " " + e.Pointer)
	}
	if where == "" {
		return e.Detail
	}
	return e.Detail + " (" + where + ")"
}

// ErrorModel is the body of every error response, an RFC 9457 problem
// details object. Handlers return one, made with NewError or the ErrorXXX
// helpers, to send an error response; any other error is logged and sent as
// a 500.
type ErrorModel struct {
	Type     string        `json:"type,omitempty"`     // URI of documentation for the error
	Title    string        `json:"title,omitempty"`    // short summary of the problem type, the same for every occurrence
	Status   int           `json:"status,omitempty"`   // HTTP status code
	Detail   string        `json:"detail,omitempty"`   // explanation specific to this occurrence
	Instance string        `json:"instance,omitempty"` // URI of this occurrence
	Errors   []ErrorDetail `json:"errors,omitempty"`   // individual problems, e.g. validation errors

	// Headers are sent with the response, e.g. WWW-Authenticate with a 401.
	Headers http.Header `json:"-"`
}

// Error returns the detail.
func (e *ErrorModel) Error() string { return e.Detail }

// NewError returns an error response for handlers and middlewares (see
// API.WriteError) to return. Each of errs becomes an item of Errors; an
// *ErrorDetail is used as is.
func NewError(status int, msg string, errs ...error) *ErrorModel {
	m := &ErrorModel{Status: status, Title: http.StatusText(status), Detail: msg}
	for _, err := range errs {
		var d *ErrorDetail
		switch {
		case err == nil:
			continue
		case errors.As(err, &d):
			m.Errors = append(m.Errors, *d)
		default:
			m.Errors = append(m.Errors, ErrorDetail{Detail: err.Error()})
		}
	}
	return m
}

// Error400BadRequest returns NewError(http.StatusBadRequest, msg, errs...).
func Error400BadRequest(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusBadRequest, msg, errs...)
}

// Error401Unauthorized returns NewError(http.StatusUnauthorized, msg, errs...).
func Error401Unauthorized(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusUnauthorized, msg, errs...)
}

// Error403Forbidden returns NewError(http.StatusForbidden, msg, errs...).
func Error403Forbidden(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusForbidden, msg, errs...)
}

// Error404NotFound returns NewError(http.StatusNotFound, msg, errs...).
func Error404NotFound(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusNotFound, msg, errs...)
}

// Error409Conflict returns NewError(http.StatusConflict, msg, errs...).
func Error409Conflict(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusConflict, msg, errs...)
}

// Error412PreconditionFailed returns NewError(http.StatusPreconditionFailed, msg, errs...).
func Error412PreconditionFailed(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusPreconditionFailed, msg, errs...)
}

// Error422UnprocessableEntity returns NewError(http.StatusUnprocessableEntity, msg, errs...).
func Error422UnprocessableEntity(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusUnprocessableEntity, msg, errs...)
}

// Error429TooManyRequests returns NewError(http.StatusTooManyRequests, msg, errs...).
func Error429TooManyRequests(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusTooManyRequests, msg, errs...)
}

// Error500InternalServerError returns NewError(http.StatusInternalServerError, msg, errs...).
func Error500InternalServerError(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusInternalServerError, msg, errs...)
}

// Error503ServiceUnavailable returns NewError(http.StatusServiceUnavailable, msg, errs...).
func Error503ServiceUnavailable(msg string, errs ...error) *ErrorModel {
	return NewError(http.StatusServiceUnavailable, msg, errs...)
}

func recoverPanic(w http.ResponseWriter, r *http.Request) {
	v := recover()
	if v == nil {
		return
	}
	if v == http.ErrAbortHandler {
		panic(v)
	}
	slog.Default().ErrorContext(r.Context(), "panic", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
	writeErrorBody(w, NewError(http.StatusInternalServerError, "unexpected error occurred"))
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var m *ErrorModel
	if !errors.As(err, &m) {
		slog.Default().ErrorContext(r.Context(), "unhandled error", "method", r.Method, "path", r.URL.Path, "error", err)
		m = NewError(http.StatusInternalServerError, "unexpected error occurred")
	}
	writeErrorBody(w, m)
}

var (
	errorModelType     = reflect.TypeFor[ErrorModel]()
	errorModelEnc      = compileEncoder(errorModelType)
	problemContentType = []string{"application/problem+json"}
)

func writeErrorBody(w http.ResponseWriter, m *ErrorModel) {
	if m.Status < 200 || m.Status > 599 {
		valid := *m
		valid.Status, valid.Title = http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)
		m = &valid
	}
	h := w.Header()
	for k, vs := range m.Headers {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	status := m.Status
	if bodiless(status) {
		w.WriteHeader(status)
		return
	}
	s := getState()
	defer putState(s)
	_ = s.enc.encode(errorModelEnc, reflect.ValueOf(m).Elem())
	h["Content-Type"] = problemContentType
	w.WriteHeader(status)
	_, _ = w.Write(s.enc.buf)
}
