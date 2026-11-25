package spec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/r-witz/rally"
)

type protoIn struct {
	Tags []string `query:"tag" maxItems:"5"`
	N    int      `query:"n"`
	F    float64  `query:"f"`
	S    string   `query:"s" description:"Any text." example:"a b"`
	B    bool     `query:"b"`
	H    string   `header:"X-Thing"`
}

type protoOut struct {
	Tags []string `json:"tags"`
	N    int      `json:"n"`
	F    float64  `json:"f"`
	S    string   `json:"s"`
	B    bool     `json:"b"`
	H    string   `json:"h"`
}

type protoBody struct {
	Age  int       `json:"age"`
	Name string    `json:"name,omitempty" maxLength:"3" description:"A short name." example:"Bob"`
	ID   uuid.UUID `json:"id,omitzero"`
	F    float64   `json:"f,omitempty"`
	G    float32   `json:"g,omitempty"`
	List []int     `json:"list,omitempty" maxItems:"200"`
}

type protoBodyOut struct {
	Age  int       `json:"age"`
	Name string    `json:"name,omitempty"`
	ID   uuid.UUID `json:"id,omitzero"`
	F    float64   `json:"f,omitempty"`
	G    float32   `json:"g,omitempty"`
	List []int     `json:"list,omitempty"`
}

func protoAPI() (*http.ServeMux, *rally.API) {
	mux := http.NewServeMux()
	api := rally.New(mux, rally.Config{MaxBodyBytes: 1000})
	rally.Register(api, rally.Operation{Method: http.MethodGet, Path: "/q", OperationID: "q"},
		func(_ context.Context, in *protoIn) (*struct{ Body protoOut }, error) {
			return &struct{ Body protoOut }{protoOut{in.Tags, in.N, in.F, in.S, in.B, in.H}}, nil
		})
	rally.Register(api, rally.Operation{Method: http.MethodPost, Path: "/b", OperationID: "b"},
		func(_ context.Context, in *struct{ Body protoBody }) (*struct{ Body protoBodyOut }, error) {
			return &struct{ Body protoBodyOut }{protoBodyOut(in.Body)}, nil
		})
	rally.Register(api, rally.Operation{Method: http.MethodGet, Path: "/lmt", OperationID: "lmt"},
		func(context.Context, *struct{}) (*struct{ Body time.Time }, error) {
			loc := time.FixedZone("LMT", 561)
			return &struct{ Body time.Time }{time.Date(1900, 1, 1, 0, 9, 21, 0, loc)}, nil
		})
	rally.Register(api, rally.Operation{Method: http.MethodGet, Path: "/p/{id}", OperationID: "p"},
		func(_ context.Context, in *struct {
			ID string `path:"id"`
		}) (*struct{ Body string }, error) {
			return &struct{ Body string }{in.ID}, nil
		})
	rally.Register(api, rally.Operation{Method: "QUERY", Path: "/search", OperationID: "search", Hidden: true},
		func(_ context.Context, in *struct{ Body protoQuery }) (*struct{ Body protoBodyOut }, error) {
			return &struct{ Body protoBodyOut }{protoBodyOut{Age: in.Body.Age}}, nil
		})
	return mux, api
}

type protoQuery struct {
	Age int `json:"age"`
}

// TestProtocol checks the rows of docs/PROFILE.md about query strings,
// headers, content negotiation, methods and errors.
func TestProtocol(t *testing.T) {
	h, api := protoAPI()
	raw, err := api.OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	bodySchema, err := parseDocument(t, raw).schema("paths", "/b", "post", "requestBody", "content", "application/json", "schema")
	if err != nil {
		t.Fatal(err)
	}
	type req struct {
		row, method, target, ct, body string
		header                        map[string][]string
		status                        int
		want                          string // a substring of the response body, or of "Allow: " + the header
		deviation                     string // the row of a body the document accepts and rally rejects: class A, or a JSON text rule that schemas cannot express
	}
	for _, c := range []req{
		{row: "4.1", method: "GET", target: "/q?s=a+b%2Bc", status: 200, want: `"s":"a b+c"`},
		{row: "5.1", method: "GET", target: "/p/a%20b%2Fc+d", status: 200, want: `"a b/c+d"`},
		{row: "6.1", method: "POST", target: "/b", ct: "application/xml", body: `{"age":1}`, status: 415},
		{row: "6.3", method: "POST", target: "/b", ct: "application/json; charset=latin1", body: `{"age":1}`, status: 200},
		{row: "9.2", method: "GET", target: "/q?n=x", status: 400, want: `"in":"query","parameter":"n"`},
		{row: "4.2", method: "GET", target: "/q?tag=a&tag=b,c", status: 200, want: `"tags":["a","b,c"]`},
		{row: "4.2", method: "GET", target: "/q?tag%5B%5D=a", status: 400, want: `"code":"additionalProperties"`},
		{row: "4.3", method: "GET", target: "/q?n=1&n=2", status: 400, want: `"code":"duplicate"`},
		{row: "4.4", method: "GET", target: "/q?utm_source=x", status: 400, want: `"code":"additionalProperties"`},
		{row: "4.5", method: "GET", target: "/q?s=%zz", status: 400, want: `"code":"syntax"`},
		{row: "4.6", method: "GET", target: "/q?s=%ff", status: 400, want: `"code":"syntax"`},
		{row: "4.7", method: "GET", target: "/q?s=a;b", status: 200, want: `"s":"a;b"`},
		{row: "4.8", method: "GET", target: "/q?s=", status: 200, want: `"s":""`},
		{row: "4.8", method: "GET", target: "/q?n=", status: 400, want: `"parameter":"n"`},
		{row: "4.9", method: "GET", target: "/q?b=true", status: 200, want: `"b":true`},
		{row: "4.9", method: "GET", target: "/q?b=1", status: 400, want: `"parameter":"b"`},
		{row: "4.10", method: "GET", target: "/q?n=2.0&f=1e3", status: 200, want: `"n":2,"f":1000`},
		{row: "4.10", method: "GET", target: "/q?n=2.5", status: 400, want: `"code":"type"`},
		{row: "4.10", method: "GET", target: "/q?n=%2B2", status: 400, want: `"parameter":"n"`},
		{row: "5.2", method: "GET", target: "/q", header: map[string][]string{"X-Thing": {"a", "b"}}, status: 400, want: `"code":"duplicate"`},
		{row: "5.3", method: "GET", target: "/q", header: map[string][]string{"X-Thing": {""}}, status: 200, want: `"h":""`},
		{row: "6.1", method: "POST", target: "/b", ct: "text/plain", body: `{"age":1}`, status: 415},
		{row: "6.1", method: "POST", target: "/b", ct: "application/merge-patch+json", body: `{"age":1}`, status: 200},
		{row: "6.2", method: "POST", target: "/b", body: `{"age":1}`, status: 415},
		{row: "6.3", method: "POST", target: "/b", ct: "application/json; charset=utf-8", body: `{"age":1}`, status: 200},
		{row: "6.4", method: "GET", target: "/q", header: map[string][]string{"Accept": {"text/html"}}, status: 406},
		{row: "6.4", method: "GET", target: "/q", header: map[string][]string{"Accept": {"text/html,*/*;q=0.8"}}, status: 200},
		{row: "7.1", method: "DELETE", target: "/q", status: 405, want: "Allow: GET, HEAD, OPTIONS"},
		{row: "7.2", method: "HEAD", target: "/q", status: 200},
		{row: "7.3", method: "OPTIONS", target: "/b", status: 204, want: "Allow: POST, OPTIONS"},
		{row: "8.2", method: "GET", target: "/lmt", status: 200, want: `"1900-01-01T00:00:00Z"`},
		{row: "2.1", method: "POST", target: "/b", ct: "application/json", body: `{"age":36.0}`, status: 200, want: `{"age":36}`},
		{row: "2.1", method: "POST", target: "/b", ct: "application/json", body: `{"age":3.65e1}`, status: 422, want: `"code":"type"`},
		{row: "2.2", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"f":1e400}`, status: 422, deviation: "2.2"},
		{row: "2.2", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"g":1e39}`, status: 422, deviation: "2.2"},
		{row: "2.2", method: "POST", target: "/b", ct: "application/json", body: `{"age":9223372036854775808}`, status: 422, want: `"code":"maximum"`},
		{row: "1.4", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"age":2}`, status: 422, want: `"code":"duplicate"`, deviation: "1.4"},
		{row: "1.5", method: "POST", target: "/b", ct: "application/json", body: `{"Age":1}`, status: 422, want: `"code":"additionalProperties"`},
		{row: "1.8", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"admin":true}`, status: 422, want: `"pointer":"/admin"`},
		{row: "1.9", method: "POST", target: "/b", ct: "application/json", body: `{"name":"a"}`, status: 422, want: `"code":"required"`},
		{row: "1.9", method: "POST", target: "/b", ct: "application/json", body: `{"age":null}`, status: 422, want: `"code":"type"`},
		{row: "1.10", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"name":"` + strings.Repeat("x", 1000) + `"}`, status: 413},
		{row: "1.11", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"list":[` + strings.Repeat(`"x",`, 150) + `1]}`, status: 422, want: strings.Repeat(`"code":"type"`, 1)},
		{row: "3.9", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"id":"123E4567-E89B-12D3-A456-426614174000"}`, status: 200, want: `"id":"123e4567-e89b-12d3-a456-426614174000"`},
		{row: "3.9", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"id":"{123e4567-e89b-12d3-a456-426614174000}"}`, status: 422, want: `"code":"format"`},
		{row: "3.9", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"id":"urn:uuid:123e4567-e89b-12d3-a456-426614174000"}`, status: 422, want: `"code":"format"`},
		{row: "3.9", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"id":"123e4567e89b12d3a456426614174000"}`, status: 422, want: `"code":"format"`},
		{row: "9.4", method: "POST", target: "/b", ct: "application/json", body: `{"age":1,"name":"secret"}`, status: 422, want: `"code":"maxLength"`},
		{row: "7.4", method: "QUERY", target: "/search", ct: "application/json", body: `{"age":3}`, status: 200, want: `"age":3`},
		{row: "6.5", method: "POST", target: "/b", ct: "application/json", body: `{"age":1}`, status: 200, want: `"age":1`},
		{row: "9.1", method: "POST", target: "/b", ct: "application/json", body: `{"age":"x"}`, status: 422, want: `"pointer":"/age"`},
	} {
		r := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
		if c.body == "" {
			r.Body = http.NoBody
		}
		if c.ct != "" {
			r.Header.Set("Content-Type", c.ct)
		}
		for k, v := range c.header {
			r.Header[k] = v
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		got := w.Body.String()
		if strings.HasPrefix(c.want, "Allow: ") {
			got = "Allow: " + w.Header().Get("Allow")
		}
		if w.Code != c.status || !strings.Contains(got, c.want) {
			t.Errorf("row %s: %s %s: %d %s, want %d and %q", c.row, c.method, c.target, w.Code, got, c.status, c.want)
		}
		if c.row == "9.4" && strings.Contains(w.Body.String(), "secret") {
			t.Errorf("row 9.4: the rejected value is echoed: %s", w.Body.String())
		}
		if c.row == "1.11" && strings.Count(w.Body.String(), `"code"`) != 100 {
			t.Errorf("row 1.11: %d errors, want 100", strings.Count(w.Body.String(), `"code"`))
		}
		if c.row == "6.5" && w.Header().Get("Content-Type") != "application/json" {
			t.Errorf("row 6.5: Content-Type %q", w.Header().Get("Content-Type"))
		}
		if c.target == "/b" && c.ct == "application/json" && w.Code != http.StatusRequestEntityTooLarge {
			accepted := w.Code < 300
			if documented := validates(bodySchema, []byte(c.body)); documented != accepted && c.deviation == "" {
				t.Errorf("row %s: %s: the document says valid=%v, rally accepted=%v", c.row, c.body, documented, accepted)
			} else if documented == accepted && c.deviation != "" {
				t.Errorf("row %s: %s: listed as a deviation of row %s, but the document agrees", c.row, c.body, c.deviation)
			}
		}
		if w.Code >= 400 && c.method != "HEAD" {
			var p struct{ Status int }
			if w.Header().Get("Content-Type") != "application/problem+json" || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Status != w.Code {
				t.Errorf("row 9.1: %s %s: not a problem with status %d: %v %s", c.method, c.target, w.Code, w.Header(), w.Body.String())
			}
		}
	}
}
