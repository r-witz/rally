package rally

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"uuid"
)

func do(h http.Handler, method, target, body string, headers ...string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func problems(w *httptest.ResponseRecorder) []string {
	var m ErrorModel
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return describe(m.Errors)
}

type paramsInput struct {
	ID     uuid.UUID `path:"id"`
	Limit  int       `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Size   uint8     `query:"size" maximum:"5"`
	Cursor *int      `query:"cursor"`
	Tags   []string  `query:"tags" maxItems:"3" default:"x,y"`
	Ex     []int     `query:"ex" required:"true"`
	Since  time.Time `query:"since"`
	Auth   string    `header:"x-auth" required:"true"`
	Flag   bool      `query:"flag"`
	pagination
}

type paramsEcho struct {
	ID     uuid.UUID
	Limit  int
	Size   uint8
	Cursor *int
	Tags   []string
	Ex     []int
	Since  time.Time
	Auth   string
	Flag   bool
	Page   int
}

type paramsOutput struct {
	Body paramsEcho
}

func newParamsAPI() *http.ServeMux {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodGet, Path: "/items/{id}", OperationID: "get-items-id"}, func(ctx context.Context, in *paramsInput) (*paramsOutput, error) {
		return &paramsOutput{Body: paramsEcho{in.ID, in.Limit, in.Size, in.Cursor, in.Tags, in.Ex, in.Since, in.Auth, in.Flag, in.Page}}, nil
	})
	return mux
}

func TestParams(t *testing.T) {
	mux := newParamsAPI()
	w := do(mux, http.MethodGet, "/items/123e4567-e89b-12d3-a456-426614174000?size=3&cursor=5&tags=a&tags=b&ex=1&ex=2&since=2024-01-02T00:00:00Z&flag=true&page=3",
		"", "X-Auth", "secret")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	want := map[string]any{
		"ID": "123e4567-e89b-12d3-a456-426614174000", "Limit": 20.0, "Size": 3.0, "Cursor": 5.0, "Tags": []any{"a", "b"},
		"Ex": []any{1.0, 2.0}, "Since": "2024-01-02T00:00:00Z", "Auth": "secret",
		"Flag": true, "Page": 3.0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}

	w = do(mux, http.MethodGet, "/items/123e4567-e89b-12d3-a456-426614174000?ex=7&tags=", "", "X-Auth", "secret")
	got = nil
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != http.StatusOK || !reflect.DeepEqual(got["Ex"], []any{7.0}) || !reflect.DeepEqual(got["Tags"], []any{""}) {
		t.Errorf("one item, empty item: %d %s", w.Code, w.Body.String())
	}
}

func TestMalformedQueries(t *testing.T) {
	w := do(newParamsAPI(), http.MethodGet, "/items/123e4567-e89b-12d3-a456-426614174000?cursor=%zz&page=2;x=1&limit=5&limit=6&ex=1&x%2Dy=1&size=%ff&flag=1&%zz&since=",
		"", "X-Auth", "secret")
	want := []string{
		"query.cursor: syntax: invalid percent-encoding in query string",
		"query.limit: duplicate: repeated query parameter",
		"query.x-y: additionalProperties: unknown query parameter",
		"query.size: syntax: invalid UTF-8 in query string",
		"query: syntax: invalid percent-encoding in query string",
		"query.since: format: expected string to be RFC 3339 date-time",
		"query.flag: type: invalid boolean",
		"query.page: type: invalid integer",
	}
	if got := problems(w); w.Code != http.StatusBadRequest || !slices.Equal(got, want) {
		t.Errorf("status %d\ngot  %q\nwant %q", w.Code, got, want)
	}
	mux := http.NewServeMux()
	config := DefaultConfig("Keys", "1")
	config.SecuritySchemes = map[string]*SecurityScheme{"key": {Type: "apiKey", In: "query", Name: "api_key"}}
	api := New(mux, config)
	Register(api, Operation{Method: http.MethodGet, Path: "/health", OperationID: "get-health"}, noop)
	Register(api, Operation{Method: http.MethodGet, Path: "/me", OperationID: "get-me", Security: []map[string][]string{{"key": nil}}}, func(ctx context.Context, in *struct {
		ID string `header:"X-Id"`
	}) (*struct{}, error) {
		return nil, nil
	})
	if got := problems(do(mux, http.MethodGet, "/health?verbose=true&api_key=k", "")); !slices.Equal(got, []string{
		"query.verbose: additionalProperties: unknown query parameter", "query.api_key: additionalProperties: unknown query parameter",
	}) {
		t.Errorf("operation without parameters: %q", got)
	}
	if w := do(mux, http.MethodGet, "/me?api_key=k", ""); w.Code != http.StatusNoContent {
		t.Errorf("API key in the query: %d %s", w.Code, w.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header["X-Id"] = []string{"a", "b"}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if got := problems(w); w.Code != http.StatusBadRequest || !slices.Equal(got, []string{"header.X-Id: duplicate: repeated header"}) {
		t.Errorf("repeated header: %d %q", w.Code, got)
	}
	req = httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header["X-Id"] = []string{""}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("empty header: %d %s", w.Code, w.Body.String())
	}
}

func TestParamErrors(t *testing.T) {
	w := do(newParamsAPI(), http.MethodGet, "/items/nope?limit=0&size=9&cursor=x&tags=a&tags=b&tags=c&tags=d&since=yesterday&flag=maybe", "")
	want := []string{
		"path.id: format: expected string to be a UUID",
		"query.limit: minimum: expected number >= 1",
		"query.size: maximum: expected number <= 5",
		"query.cursor: type: invalid integer",
		"query.tags: maxItems: expected array length <= 3",
		"query.ex: required: required query parameter is missing",
		"query.since: format: expected string to be RFC 3339 date-time",
		"header.x-auth: required: required header parameter is missing",
		"query.flag: type: invalid boolean",
	}
	if got := problems(w); w.Code != http.StatusBadRequest || !slices.Equal(got, want) {
		t.Errorf("status %d\ngot  %q\nwant %q", w.Code, got, want)
	}
}

func TestParamErrorsCapped(t *testing.T) {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x"}, func(ctx context.Context, in *struct {
		IDs  []int `query:"ids"`
		Body struct {
			N int `json:"n"`
		}
	}) (*struct{}, error) {
		return nil, nil
	})
	for _, body := range []string{`{"n":1}`, `{"n":"x"}`} {
		w := do(mux, http.MethodPost, "/x?ids="+strings.Repeat("x&ids=", 999)+"x", body)
		var got ErrorModel
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != http.StatusBadRequest || len(got.Errors) != maxErrors {
			t.Errorf("body %s: status %d, %d errors, want 400 and %d", body, w.Code, len(got.Errors), maxErrors)
		}
	}
}

func TestIntegerParameters(t *testing.T) {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x"}, func(ctx context.Context, in *struct {
		I int64    `query:"i"`
		U uint     `query:"u"`
		L []string `query:"l" maxItems:"2" minLength:"2"`
	}) (*struct{ Body string }, error) {
		return &struct{ Body string }{fmt.Sprint(in.I, in.U, in.L)}, nil
	})
	for q, want := range map[string]string{
		"i=-42&u=7&l=ab&l=cd":       `"-42 7 [ab cd]"`,
		"i=2.0&u=7e0":               `"2 7 []"`,
		"i=-0.5e1&u=1E1":            `"-5 10 []"`,
		"l=a%2Cb&l=c+d":             `"0 0 [a,b c d]"`,
		"l=a;b&l=c%3Bd":             `"0 0 [a;b c;d]"`,
		"i=9223372036854775807&u=0": `"9223372036854775807 0 []"`,
	} {
		if w := do(mux, http.MethodGet, "/x?"+q, ""); w.Code != 200 || w.Body.String() != want {
			t.Errorf("%s: %d %s, want %s", q, w.Code, w.Body.String(), want)
		}
	}
	want := []string{
		"query.i: type: invalid integer", "query.u: type: invalid integer", "query.l: minLength: expected length >= 2",
		"query.l: maxItems: expected array length <= 2",
	}
	if got := problems(do(mux, http.MethodGet, "/x?i=007&u=%2B5&l=a&l=bc&l=d&l=e&l=f", "")); !slices.Equal(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestTooManyQueryParameters(t *testing.T) {
	w := do(newParamsAPI(), http.MethodGet, "/items/123e4567-e89b-12d3-a456-426614174000?limit=5"+strings.Repeat("&", maxQueryParams), "", "X-Auth", "secret")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "too many query parameters") {
		t.Errorf("status %d: %s", w.Code, w.Body.String())
	}
}

func FuzzQuery(f *testing.F) {
	for _, raw := range []string{
		"", "a=1", "a=1&a=2&b=3", "a", "a=", "=1", "&&a=1&&", "a=1;b=2&a=3", "a=%zz&a=4", "%61=5&a=6",
		"a+b=c+d&a%20b=e", "a=%E2%82%AC&a=x%3Dy", "b=1&a=1&a=2", "a=1&a=2;&a=3", "a%=1&a=2", "%&%25", "a=1&a=2&b=%41+B;c", "a=%ff", "=%0", "\x80",
	} {
		f.Add(raw)
	}
	f.Fuzz(checkQuery)
}

// checkQuery compares parseQuery with url.ParseQuery, the WHATWG
// application/x-www-form-urlencoded parser except that it rejects ";",
// which WHATWG (and so OpenAPI 3.2) reads as data: ";" is escaped first.
func checkQuery(t *testing.T, raw string) {
	want, err := url.ParseQuery(strings.ReplaceAll(raw, ";", "%3B"))
	p := &plan{}
	for _, k := range []string{"a", "b", "a b"} {
		p.params = append(p.params, paramBinder{in: inQuery, name: k})
	}
	for k := range want {
		if utf8.ValidString(k) && !slices.ContainsFunc(p.params, func(b paramBinder) bool { return b.name == k }) {
			p.params = append(p.params, paramBinder{in: inQuery, name: k})
		}
	}
	d := &decodeState{}
	got := p.parseQuery(d, raw)
	codes := map[string][]string{}
	for _, e := range d.errs {
		codes[e.Code] = append(codes[e.Code], e.Parameter)
	}
	invalidUTF8 := false
	for k, vs := range want {
		invalidUTF8 = invalidUTF8 || !utf8.ValidString(k) || slices.ContainsFunc(vs, func(v string) bool { return !utf8.ValidString(v) })
	}
	if err != nil {
		if len(d.errs) == 0 {
			t.Fatalf("%q: ParseQuery error %v, accepted here", raw, err)
		}
		return
	}
	if invalidUTF8 != (codes["syntax"] != nil) {
		t.Fatalf("%q: invalid UTF-8 %v, our errors %v", raw, invalidUTF8, d.errs)
	}
	if invalidUTF8 {
		return
	}
	if codes["additionalProperties"] != nil {
		t.Fatalf("%q: unknown parameters %v", raw, codes["additionalProperties"])
	}
	for i, b := range p.params {
		vs := want[b.name]
		dup := slices.Contains(codes["duplicate"], b.name)
		if len(vs) > 1 != dup || len(vs) > 0 != got[i].ok || len(vs) > 0 && got[i].s != vs[0] {
			t.Fatalf("%q, key %q: got %+v duplicate %v, want %q", raw, b.name, got[i], dup, vs)
		}
	}
}
