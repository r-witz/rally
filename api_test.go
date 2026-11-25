package rally

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func noop(context.Context, *struct{}) (*struct{}, error) { return nil, nil }

func TestDocumentServed(t *testing.T) {
	mux := newComplexAPI()
	var doc map[string]any
	w := do(mux, http.MethodGet, "/openapi.json", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || json.Unmarshal(w.Body.Bytes(), &doc) != nil || doc["openapi"] != "3.1.2" {
		t.Errorf("document: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	w = do(mux, http.MethodGet, "/docs", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Content-Security-Policy") == "" ||
		!strings.Contains(w.Body.String(), `id="api-reference"`) {
		t.Errorf("docs: %d %v", w.Code, w.Header())
	}
}

func TestZeroConfig(t *testing.T) {
	private := http.NewServeMux()
	api := New(private, Config{Info: Info{Title: "Private", Version: "1"}})
	Register(api, Operation{Method: http.MethodPost, Path: "/ping", OperationID: "post-ping"}, func(ctx context.Context, in *struct{ Body string }) (*struct{}, error) {
		return nil, nil
	})
	for _, path := range []string{"/openapi.json", "/docs"} {
		if w := do(private, http.MethodGet, path, ""); w.Code != http.StatusNotFound || !json.Valid(w.Body.Bytes()) {
			t.Errorf("%s served without OpenAPIPath: %d %q", path, w.Code, w.Body.String())
		}
	}
	for body, want := range map[string]int{`"ok"`: http.StatusNoContent, `"` + strings.Repeat("a", defaultMaxBodyBytes) + `"`: http.StatusRequestEntityTooLarge} {
		if w := do(private, http.MethodPost, "/ping", body); w.Code != want {
			t.Errorf("body of %d bytes with the default limit: status %d, want %d", len(body), w.Code, want)
		}
	}
	if b, err := api.OpenAPIJSON(); err != nil || !strings.Contains(string(b), "/ping") {
		t.Errorf("document unavailable: %v", err)
	}
	Register(api, Operation{Method: http.MethodGet, Path: "/later", OperationID: "get-later"}, noop)
	if b, _ := api.OpenAPIJSON(); !strings.Contains(string(b), "/later") {
		t.Error("an operation registered after the document was rendered is missing from it")
	}
}

func TestNewPanics(t *testing.T) {
	shared := http.NewServeMux()
	New(shared, Config{})
	for name, c := range map[string]struct {
		mux    *http.ServeMux
		config Config
		want   string
	}{
		"second API on a mux": {shared, DefaultConfig("Second", "1"), "give each API its own mux"},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if v := recover(); v == nil || !strings.Contains(fmt.Sprint(v), c.want) {
					t.Errorf("panic %q, want it to contain %q", v, c.want)
				}
			}()
			New(c.mux, c.config)
		})
	}
}

func TestGroupsAndMiddlewares(t *testing.T) {
	var trace []string
	mw := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				trace = append(trace, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	mux, api := newAPI()
	v1 := api.Group("/v1", mw("group"))
	Register(v1, Operation{Method: http.MethodGet, Path: "/ping", OperationID: "get-ping", Middlewares: []func(http.Handler) http.Handler{mw("op")}},
		func(ctx context.Context, in *struct{}) (*struct{ Body string }, error) {
			trace = append(trace, "handler")
			return &struct{ Body string }{"pong"}, nil
		})
	w := do(mux, http.MethodGet, "/v1/ping", "")
	if w.Body.String() != `"pong"` || !reflect.DeepEqual(trace, []string{"group", "op", "handler"}) {
		t.Errorf("%s %v", w.Body.String(), trace)
	}
	if api.doc.Paths["/v1/ping"]["get"] == nil {
		t.Errorf("group path not documented")
	}

	org := api.Group("/orgs/{org}")
	Register(org, Operation{Method: http.MethodGet, Path: "/name", OperationID: "get-org-name"},
		func(ctx context.Context, in *struct {
			Org string `path:"org" minLength:"2"`
		}) (*struct{ Body string }, error) {
			return &struct{ Body string }{in.Org}, nil
		})
	Register(org, Operation{Method: http.MethodDelete, Path: "/members", OperationID: "delete-org-members"}, noop)
	if w := do(mux, http.MethodGet, "/orgs/acme/name", ""); w.Body.String() != `"acme"` {
		t.Errorf("status %d: %s", w.Code, w.Body.String())
	}
	for path, method := range map[string]string{"/orgs/{org}/name": "get", "/orgs/{org}/members": "delete"} {
		if p := api.doc.Paths[path][method].Parameters; len(p) != 1 || p[0].Name != "org" {
			t.Errorf("%s: parameters %+v", path, p)
		}
	}
}

func TestUnknownRoutes(t *testing.T) {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodGet, Path: "/items/{id}", OperationID: "get-items-id"}, func(ctx context.Context, in *struct {
		ID int `path:"id"`
	}) (*struct{ Body int }, error) {
		return &struct{ Body int }{in.ID}, nil
	})
	Register(api, Operation{Method: http.MethodPost, Path: "/items", OperationID: "post-items"}, noop)

	for _, c := range []struct{ method, target, allow, body string }{
		{"GET", "/nope", "", `{"title":"Not Found","status":404,"detail":"no operation matches this path"}`},
		{"GET", "/items", "POST, OPTIONS", `{"title":"Method Not Allowed","status":405,"detail":"method not allowed, see the Allow header"}`},
		{"DELETE", "/items/3", "GET, HEAD, OPTIONS", `{"title":"Method Not Allowed","status":405,"detail":"method not allowed, see the Allow header"}`},
		{"POST", "/openapi.json", "GET, HEAD, OPTIONS", ""},
	} {
		w := do(mux, c.method, c.target, "")
		want := http.StatusNotFound
		if c.allow != "" {
			want = http.StatusMethodNotAllowed
		}
		if w.Code != want || w.Header().Get("Content-Type") != "application/problem+json" || w.Header().Get("Allow") != c.allow ||
			(c.body != "" && w.Body.String() != c.body) {
			t.Errorf("%s %s: %d %v %s", c.method, c.target, w.Code, w.Header(), w.Body.String())
		}
	}
	if w := do(mux, "OPTIONS", "/items/3", ""); w.Code != http.StatusNoContent || w.Header().Get("Allow") != "GET, HEAD, OPTIONS" || w.Body.Len() != 0 {
		t.Errorf("OPTIONS /items/3: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	if w := do(mux, "OPTIONS", "/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("OPTIONS /nope: %d", w.Code)
	}
	for accept, want := range map[string]int{
		"application/json": 200, "*/*": 200, "application/*": 200, "text/html, */*;q=0.8": 200, "application/json;q=0.5, text/html": 200,
		"text/html": 406, "application/json;q=0": 406, "*/*, application/json;q=0": 406, "application/*;q=0.000": 406, "application/xml, application/*;q=0, */*": 406,
	} {
		w := do(mux, "GET", "/items/3", "", "Accept", accept)
		if w.Code != want || want == 406 && w.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("Accept %q: %d %s, want %d", accept, w.Code, w.Body.String(), want)
		}
	}
	if w := do(mux, "GET", "/items/3", ""); w.Code != 200 || w.Body.String() != "3" {
		t.Errorf("GET /items/3: %d %s", w.Code, w.Body.String())
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error(`registering "/" again: no panic`)
			}
		}()
		mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
	}()
}

func TestDocumentError(t *testing.T) {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x"}, func(ctx context.Context, in *struct {
		C code `query:"c" default:"ok"`
	}) (*struct{}, error) {
		return nil, nil
	})
	api.doc.Paths["/x"]["get"].Parameters[0].Schema.Default = code{"bad"}
	if _, err := api.OpenAPIJSON(); err == nil {
		t.Error("OpenAPIJSON: no error")
	}
	if w := do(mux, http.MethodGet, "/openapi.json", ""); w.Code != http.StatusInternalServerError {
		t.Errorf("document: %d", w.Code)
	}
}

type complexInput struct {
	ID     string `path:"id"`
	Search string `query:"search" minLength:"3"`
	Sort   string `query:"sort" enum:"asc,desc" default:"asc"`
	Page   int    `query:"page" minimum:"1" default:"1"`
	Body   complexBody
}

type complexResult struct {
	String string    `json:"string"`
	Time   time.Time `json:"time"`
}

type complexOutput struct {
	Body struct {
		ID      string        `json:"id"`
		Result  complexResult `json:"result"`
		Message string        `json:"message"`
		Sort    string        `json:"sort"`
		Page    int           `json:"page"`
	}
}

func newComplexAPI() *http.ServeMux {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodPost, Path: "/complex/{id}", OperationID: "post-complex-id", DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *complexInput) (*complexOutput, error) {
			out := &complexOutput{}
			out.Body.ID = in.ID
			out.Body.Result = complexResult{in.Body.String, in.Body.Time}
			out.Body.Message = "Created"
			out.Body.Sort = in.Sort
			out.Body.Page = in.Page
			return out, nil
		})
	return mux
}
