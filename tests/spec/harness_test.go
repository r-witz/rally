package spec

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) problem(t *testing.T) problem {
	t.Helper()
	var p problem
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatalf("status %d: body is not a problem: %v: %s", r.status, err, r.body)
	}
	return p
}

type problem struct {
	Status int
	Detail string
	Errors []struct{ Detail, Code, In, Pointer, Parameter string }
}

func (p problem) hasCode(code string) bool {
	for _, e := range p.Errors {
		if e.Code == code {
			return true
		}
	}
	return false
}

func do(h http.Handler, method, target, contentType string, body []byte) response {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body == nil {
		r.Body = http.NoBody
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return response{w.Code, w.Header(), w.Body.Bytes()}
}
