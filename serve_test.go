package rally

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"uuid"
)

func newAPI() (*http.ServeMux, *API) {
	mux := http.NewServeMux()
	return mux, New(mux, DefaultConfig("Test API", "1.0.0"))
}

type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("connection reset")
	}
	n := min(f.n, len(p))
	for i := range n {
		p[i] = ' '
	}
	f.n -= n
	return n, nil
}

func TestBodyReading(t *testing.T) {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x", MaxBodyBytes: 100}, func(ctx context.Context, in *struct{ Body int }) (*struct{}, error) {
		return nil, nil
	})
	for _, c := range []struct {
		body   io.Reader
		length int64
		want   int
	}{
		{&failingReader{n: 10}, -1, http.StatusBadRequest},
		{&failingReader{n: 1000}, -1, http.StatusRequestEntityTooLarge},
		{strings.NewReader(strings.Repeat(" ", 99) + "1"), -1, http.StatusNoContent},
	} {
		r := httptest.NewRequest(http.MethodPost, "/x", c.body)
		r.ContentLength = c.length
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("status %d, want %d: %s", w.Code, c.want, w.Body.String())
		}
	}
	big, bigAPI := newAPI()
	Register(bigAPI, Operation{Method: http.MethodPost, Path: "/big", OperationID: "post-big", MaxBodyBytes: 4 << 20}, func(ctx context.Context, in *struct{ Body string }) (*struct{}, error) {
		return nil, nil
	})
	if w := do(big, http.MethodPost, "/big", `"`+strings.Repeat("a", 2<<20)+`"`); w.Code != http.StatusNoContent {
		t.Errorf("2 MiB body: %d", w.Code)
	}
}

func TestBodies(t *testing.T) {
	mux, api := newAPI()
	type in struct {
		Body *struct {
			Name string `json:"name"`
		}
	}
	Register(api, Operation{Method: http.MethodPost, Path: "/optional", OperationID: "post-optional"}, func(ctx context.Context, i *in) (*struct{ Body string }, error) {
		name := "<nil>"
		if i.Body != nil {
			name = i.Body.Name
		}
		return &struct{ Body string }{name}, nil
	})
	if w := do(mux, http.MethodPost, "/optional", ""); w.Body.String() != `"<nil>"` {
		t.Errorf("optional body: %d %s", w.Code, w.Body.String())
	}
	if w := do(mux, http.MethodPost, "/optional", `{"name":"x"}`); w.Body.String() != `"x"` {
		t.Errorf("optional body: %d %s", w.Code, w.Body.String())
	}
	if w := do(mux, http.MethodPost, "/optional", "null"); w.Body.String() != `"<nil>"` {
		t.Errorf("null optional body: %d %s", w.Code, w.Body.String())
	}
	type required struct {
		Body *struct {
			Name string `json:"name"`
		} `required:"true"`
	}
	Register(api, Operation{Method: http.MethodPost, Path: "/required", OperationID: "post-required"}, func(ctx context.Context, i *required) (*struct{ Body string }, error) {
		return &struct{ Body string }{i.Body.Name}, nil
	})
	for body, code := range map[string]int{"": 400, "null": 422, `{"name":"x"}`: 200} {
		if w := do(mux, http.MethodPost, "/required", body); w.Code != code {
			t.Errorf("required pointer body %q: %d %s", body, w.Code, w.Body.String())
		}
	}
	b, _ := api.OpenAPIJSON()
	if !strings.Contains(string(b), `"post-required","requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/PostRequiredRequest"}}},"required":true}`) {
		t.Errorf("a required pointer body is not nullable: %s", b)
	}
}

type headersOutput struct {
	Status  int
	ETag    string `header:"ETag"`
	Count   string `header:"X-Count"`
	Missing string `header:"X-Missing"`
	Body    struct {
		OK bool `json:"ok"`
	}
}

func TestOutputs(t *testing.T) {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodGet, Path: "/headers", OperationID: "get-headers", Statuses: []int{http.StatusAccepted}}, func(ctx context.Context, in *struct{}) (*headersOutput, error) {
		out := &headersOutput{Status: http.StatusAccepted, ETag: `"abc"`, Count: "3"}
		out.Body.OK = true
		return out, nil
	})
	Register(api, Operation{Method: http.MethodDelete, Path: "/nothing", OperationID: "delete-nothing"}, noop)
	Register(api, Operation{Method: http.MethodGet, Path: "/cached", OperationID: "get-cached", Statuses: []int{http.StatusNotModified}}, func(ctx context.Context, in *struct{}) (*headersOutput, error) {
		return &headersOutput{Status: http.StatusNotModified, ETag: `"abc"`}, nil
	})
	Register(api, Operation{Method: http.MethodGet, Path: "/large", OperationID: "get-large", DefaultStatus: http.StatusCreated}, func(ctx context.Context, in *struct{}) (*struct{ Body []string }, error) {
		return &struct{ Body []string }{slices.Repeat([]string{"0123456789"}, 500)}, nil
	})

	w := do(mux, http.MethodGet, "/headers", "")
	h := w.Header()
	if _, sent := h["X-Missing"]; w.Code != http.StatusAccepted || h.Get("ETag") != `"abc"` || h.Get("X-Count") != "3" ||
		sent || w.Body.String() != `{"ok":true}` {
		t.Errorf("headers: %d %v %s", w.Code, h, w.Body.String())
	}
	if w := do(mux, http.MethodDelete, "/nothing", ""); w.Code != http.StatusNoContent || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
		t.Errorf("no content: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if w := do(mux, http.MethodGet, "/cached", ""); w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" || w.Header().Get("ETag") != `"abc"` {
		t.Errorf("not modified: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if r := api.doc.Paths["/cached"]["get"].Responses; r["304"].Content != nil || r["304"].Headers == nil || r["200"].Content == nil {
		t.Errorf("documented 304: %+v", r)
	}
	if w := do(mux, http.MethodGet, "/large", ""); w.Code != http.StatusCreated || w.Header().Get("Content-Length") != "6501" {
		t.Errorf("large: %d, content-length %q", w.Code, w.Header().Get("Content-Length"))
	}
	documented := api.doc.Paths["/headers"]["get"].Responses["200"]
	if documented.Description != "OK" || len(documented.Headers) != 3 || api.doc.Paths["/large"]["get"].Responses["201"].Description != "Created" {
		t.Errorf("documented responses: %+v", api.doc.Paths)
	}

	Register(api, Operation{Method: http.MethodGet, Path: "/nan", OperationID: "get-nan"}, func(ctx context.Context, in *struct{}) (*struct {
		ETag string `header:"ETag"`
		Body float64
	}, error) {
		return &struct {
			ETag string `header:"ETag"`
			Body float64
		}{`"v1"`, math.NaN()}, nil
	})
	logs := captureLogs(t)
	w = do(mux, http.MethodGet, "/nan", "")
	if w.Code != http.StatusInternalServerError || w.Header().Get("ETag") != "" ||
		w.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(logs.String(), "NaN") {
		t.Errorf("unencodable: %d %v %s / logs %s", w.Code, w.Header(), w.Body.String(), logs.String())
	}
}

func TestNoEcho(t *testing.T) {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x"}, func(ctx context.Context, in *struct {
		IP   netip.Addr `query:"ip"`
		Body struct {
			IP netip.Addr `json:"ip"`
			ID uuid.UUID  `json:"id"`
		}
	}) (*struct{}, error) {
		return nil, nil
	})
	w := do(mux, http.MethodPost, "/x?ip=secret1", `{"ip":"secret2","id":"secret3"}`)
	want := []string{"query.ip: format: invalid value", "body/ip: format: invalid value", "body/id: format: expected string to be a UUID"}
	if w.Code != http.StatusBadRequest || !slices.Equal(problems(w), want) || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}

func TestIsJSONMediaType(t *testing.T) {
	for ct, want := range map[string]bool{
		"application/json": true, "application/json; charset=utf-8": true, " Application/JSON ": true,
		"application/problem+json": true, "application/merge-patch+json;charset=utf-8": true,
		"text/plain": false, "+json": false, "application/jsonx": false, "json": false, "": false,
	} {
		if got := isJSONMediaType(ct); got != want {
			t.Errorf("%q: got %v", ct, got)
		}
	}
}
