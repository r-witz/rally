package rally

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	logs := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return logs
}

func TestErrorHelpers(t *testing.T) {
	for want, m := range map[int]*ErrorModel{
		400: Error400BadRequest("x"), 401: Error401Unauthorized("x"), 403: Error403Forbidden("x"), 404: Error404NotFound("x"),
		409: Error409Conflict("x"), 412: Error412PreconditionFailed("x"), 422: Error422UnprocessableEntity("x"),
		429: Error429TooManyRequests("x"), 500: Error500InternalServerError("x"), 503: Error503ServiceUnavailable("x"),
	} {
		if m.Status != want || m.Title != http.StatusText(want) || m.Error() != "x" {
			t.Errorf("%d: %+v", want, m)
		}
	}
}

const internalError = `{"title":"Internal Server Error","status":500,"detail":"unexpected error occurred"}`

func TestErrors(t *testing.T) {
	logs := captureLogs(t)
	mux, api := newAPI()
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Authorization") {
			case "":
				err := Error401Unauthorized("missing token")
				err.Headers = http.Header{"WWW-Authenticate": {"Bearer"}}
				api.WriteError(w, r, fmt.Errorf("auth: %w", err))
			case "broken":
				api.WriteError(w, r, errors.New("token store down"))
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
	secured := api.Group("", auth)
	Register(secured, Operation{Method: http.MethodGet, Path: "/missing", OperationID: "get-missing"}, func(ctx context.Context, in *struct{}) (*struct{}, error) {
		return nil, Error404NotFound("no such thing")
	})
	Register(secured, Operation{Method: http.MethodGet, Path: "/internal", OperationID: "get-internal"}, func(ctx context.Context, in *struct{}) (*struct{}, error) {
		return nil, errors.New("db password is hunter2")
	})
	Register(secured, Operation{Method: http.MethodGet, Path: "/cached", OperationID: "get-cached"}, func(ctx context.Context, in *struct{}) (*struct{}, error) {
		return nil, NewError(http.StatusNotModified, "")
	})
	Register(secured, Operation{Method: http.MethodPost, Path: "/form", OperationID: "post-form"}, func(ctx context.Context, in *struct{}) (*struct{}, error) {
		return nil, NewError(http.StatusResetContent, "reset")
	})
	for _, c := range []struct {
		target, token string
		status        int
		body, logged  string
	}{
		{"/missing", "", 401, `{"title":"Unauthorized","status":401,"detail":"missing token"}`, ""},
		{"/missing", "broken", 500, internalError, "token store down"},
		{"/missing", "ok", 404, `{"title":"Not Found","status":404,"detail":"no such thing"}`, ""},
		{"/internal", "ok", 500, internalError, "hunter2"},
		{"/cached", "ok", 304, "", ""},
		{"/form", "ok", 205, "", ""},
	} {
		logs.Reset()
		method := http.MethodGet
		if c.target == "/form" {
			method = http.MethodPost
		}
		w := do(mux, method, c.target, "", "Authorization", c.token)
		if w.Code != c.status || w.Body.String() != c.body || (w.Header().Get("Content-Type") == "application/problem+json") != (c.body != "") ||
			(w.Header().Get("WWW-Authenticate") == "Bearer") != (c.status == 401) {
			t.Errorf("%s with token %q: %d %v %s", c.target, c.token, w.Code, w.Header(), w.Body.String())
		}
		if (c.logged == "") != (logs.Len() == 0) || !strings.Contains(logs.String(), c.logged) {
			t.Errorf("%s with token %q: logs %q, want the cause %q and only for a 500", c.target, c.token, logs.String(), c.logged)
		}
	}
}

func TestPanics(t *testing.T) {
	logs := captureLogs(t)
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodGet, Path: "/handler", OperationID: "get-handler"}, func(ctx context.Context, in *struct{}) (*struct{}, error) {
		panic("secret state")
	})
	Register(api, Operation{Method: http.MethodGet, Path: "/abort", OperationID: "get-abort"}, func(ctx context.Context, in *struct{}) (*struct{}, error) {
		panic(http.ErrAbortHandler)
	})

	if w := do(mux, "GET", "/handler", ""); w.Code != 500 || w.Body.String() != internalError {
		t.Errorf("panic: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "msg=panic") || !strings.Contains(logs.String(), "stack=") {
		t.Errorf("panic not logged: %s", logs.String())
	}
	logs.Reset()
	func() {
		defer func() {
			if v := recover(); v != http.ErrAbortHandler {
				t.Errorf("abort: recovered %v, want http.ErrAbortHandler", v)
			}
		}()
		do(mux, "GET", "/abort", "")
	}()
	if logs.Len() != 0 {
		t.Errorf("abort: logs %q", logs.String())
	}
}

func TestInvalidStatuses(t *testing.T) {
	logs := captureLogs(t)
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodGet, Path: "/output", OperationID: "get-output", Statuses: []int{201}}, func(ctx context.Context, in *struct{}) (*struct {
		Status int
		ETag   string `header:"ETag"`
		Body   string
	}, error) {
		return &struct {
			Status int
			ETag   string `header:"ETag"`
			Body   string
		}{Status: 1000, ETag: "v1", Body: strings.Repeat("x", 3000)}, nil
	})
	Register(api, Operation{Method: http.MethodGet, Path: "/nil", OperationID: "get-nil"}, func(ctx context.Context, in *struct{}) (*struct{ Body string }, error) {
		return nil, nil
	})
	Register(api, Operation{Method: http.MethodGet, Path: "/error", OperationID: "get-error"}, func(ctx context.Context, in *struct{}) (*struct{}, error) {
		return nil, NewError(0, "zero")
	})
	if w := do(mux, "GET", "/output", ""); w.Code != 500 || w.Body.String() != internalError || w.Header().Get("ETag") != "" || !strings.Contains(logs.String(), "status 1000 is not in Operation.Statuses") {
		t.Errorf("output: %d %v %s, logs %q", w.Code, w.Header(), w.Body.String(), logs.String())
	}
	if w := do(mux, "GET", "/nil", ""); w.Code != 500 || w.Body.String() != internalError || !strings.Contains(logs.String(), "nil output") {
		t.Errorf("nil output: %d %s, logs %q", w.Code, w.Body.String(), logs.String())
	}
	if w := do(mux, "GET", "/error", ""); w.Code != 500 || w.Body.String() != `{"title":"Internal Server Error","status":500,"detail":"zero"}` {
		t.Errorf("error: %d %s", w.Code, w.Body.String())
	}
}

func TestNewError(t *testing.T) {
	detail := ErrorDetail{Detail: "too long", Code: "maxLength", In: "body", Pointer: "/name"}
	m := NewError(http.StatusUnprocessableEntity, "invalid", errors.New("plain"), nil, fmt.Errorf("wrapped: %w", &detail))
	if want := []ErrorDetail{{Detail: "plain"}, detail}; m.Title != "Unprocessable Entity" || m.Error() != "invalid" || !slices.Equal(m.Errors, want) {
		t.Errorf("got %+v", m)
	}
	for want, e := range map[string]ErrorDetail{
		"too long (body /name)":       detail,
		"missing (query parameter q)": {Detail: "missing", In: "query", Parameter: "q"},
		"malformed (body)":            {Detail: "malformed", In: "body"},
		"plain":                       {Detail: "plain"},
	} {
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
