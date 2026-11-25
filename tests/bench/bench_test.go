// Package bench compares rally with Huma v2, Gin (and a hand-written net/http
// baseline) on identical endpoints. The input/output types are shared: the
// same struct tags drive rally and Huma, and Gin reads the same rules from its
// own binding tags (go-playground/validator syntax) on the same types.
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/r-witz/rally"
)

type Sub struct {
	ID   int    `json:"id" minimum:"1" binding:"min=1"`
	Name string `json:"name" minLength:"3" maxLength:"20" binding:"min=3,max=20"`
}

type Complex struct {
	String    string         `json:"string" minLength:"1" maxLength:"100" pattern:"^[a-zA-Z ]+$" binding:"min=1,max=100,alphaspace"`
	Int       int            `json:"int" minimum:"1" maximum:"1000" binding:"min=1,max=1000"`
	Float     float64        `json:"float" minimum:"0" maximum:"1" binding:"min=0,max=1"`
	Bool      bool           `json:"bool"`
	Time      time.Time      `json:"time" binding:"required"`
	Slice     []string       `json:"slice" minItems:"1" maxItems:"10" uniqueItems:"true" binding:"min=1,max=10,unique"`
	Sub       Sub            `json:"sub"`
	SubSlice  []Sub          `json:"sub_slice" minItems:"1" maxItems:"5" binding:"min=1,max=5,dive"`
	Map       map[string]Sub `json:"map" maxProperties:"10" binding:"max=10,dive"`
	Recursive *Complex       `json:"recursive,omitempty"`
}

type ComplexInput struct {
	ID     string `path:"id"`
	Search string `query:"search" minLength:"3"`
	Sort   string `query:"sort" enum:"asc,desc" default:"asc"`
	Page   int    `query:"page" minimum:"1" default:"1"`
	Body   Complex
}

type SubResult struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ComplexResult struct {
	String    string               `json:"string"`
	Int       int                  `json:"int"`
	Float     float64              `json:"float"`
	Bool      bool                 `json:"bool"`
	Time      time.Time            `json:"time"`
	Slice     []string             `json:"slice"`
	Sub       SubResult            `json:"sub"`
	SubSlice  []SubResult          `json:"sub_slice"`
	Map       map[string]SubResult `json:"map"`
	Recursive *ComplexResult       `json:"recursive,omitempty"`
}

type ComplexOutput struct {
	Status int
	Body   struct {
		ID      string        `json:"id"`
		Result  ComplexResult `json:"result"`
		Message string        `json:"message"`
	}
}

const complexBody = `{"string": "Benchmark", "int": 100, "float": 0.1, "bool": true, "time": "2023-01-01T00:00:00Z", "slice": ["a"], "sub": {"id": 1, "name": "SubItem"}, "sub_slice": [{"id": 1, "name": "Item"}], "map": {"k": {"id": 1, "name": "val"}}}`

func complexHandler(_ context.Context, in *ComplexInput) (*ComplexOutput, error) {
	out := &ComplexOutput{Status: http.StatusCreated}
	out.Body.ID = in.ID
	out.Body.Result = complexResult(&in.Body)
	out.Body.Message = "Created"
	return out, nil
}

func complexResult(c *Complex) ComplexResult {
	r := ComplexResult{String: c.String, Int: c.Int, Float: c.Float, Bool: c.Bool, Time: c.Time, Slice: c.Slice, Sub: SubResult(c.Sub)}
	if c.SubSlice != nil {
		r.SubSlice = make([]SubResult, len(c.SubSlice))
		for i, s := range c.SubSlice {
			r.SubSlice[i] = SubResult(s)
		}
	}
	if c.Map != nil {
		r.Map = make(map[string]SubResult, len(c.Map))
		for k, s := range c.Map {
			r.Map[k] = SubResult(s)
		}
	}
	if c.Recursive != nil {
		rec := complexResult(c.Recursive)
		r.Recursive = &rec
	}
	return r
}

type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	Roles     []string  `json:"roles"`
}

type GetUserInput struct {
	ID string `path:"id" maxLength:"36" uri:"id" binding:"max=36"`
}

type GetUserOutput struct {
	Body User
}

var created = time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)

func getUser(_ context.Context, in *GetUserInput) (*GetUserOutput, error) {
	return &GetUserOutput{Body: User{ID: in.ID, Name: "Ada Lovelace", Email: "ada@example.com", CreatedAt: created, Roles: []string{"admin", "dev"}}}, nil
}

type CreateUser struct {
	Name  string   `json:"name" minLength:"1" maxLength:"64" binding:"min=1,max=64"`
	Email string   `json:"email" maxLength:"254" binding:"max=254"`
	Age   int      `json:"age" minimum:"0" maximum:"150" binding:"min=0,max=150"`
	Roles []string `json:"roles,omitempty" maxItems:"10" binding:"omitempty,max=10"`
}

type CreateUserInput struct {
	Body CreateUser
}

type CreateUserOutput struct {
	Status int
	Body   User
}

const createBody = `{"name":"Ada Lovelace","email":"ada@example.com","age":36,"roles":["admin","dev"]}`

func createUser(_ context.Context, in *CreateUserInput) (*CreateUserOutput, error) {
	return &CreateUserOutput{Status: http.StatusCreated, Body: User{ID: "u_1", Name: in.Body.Name, Email: in.Body.Email, CreatedAt: created, Roles: in.Body.Roles}}, nil
}

func humaServer(schemaLinks bool) http.Handler {
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("Bench", "1.0.0")
	if !schemaLinks {
		cfg.CreateHooks = nil
	}
	api := humago.New(mux, cfg)
	huma.Register(api, huma.Operation{Method: http.MethodPost, Path: "/complex/{id}"}, complexHandler)
	huma.Get(api, "/users/{id}", getUser)
	huma.Post(api, "/users", createUser)
	return mux
}

func rallyServer() http.Handler {
	mux := http.NewServeMux()
	api := rally.New(mux, rally.DefaultConfig("Bench", "1.0.0"))
	rally.Register(api, rally.Operation{Method: http.MethodPost, Path: "/complex/{id}", OperationID: "post-complex", Statuses: []int{http.StatusCreated}}, complexHandler)
	rally.Register(api, rally.Operation{Method: http.MethodGet, Path: "/users/{id}", OperationID: "get-user"}, getUser)
	rally.Register(api, rally.Operation{Method: http.MethodPost, Path: "/users", OperationID: "create-user", Statuses: []int{http.StatusCreated}}, createUser)
	return mux
}

func ginServer() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	binding.EnableDecoderDisallowUnknownFields = true
	r := gin.New()
	fail := func(c *gin.Context, err error) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
	r.POST("/complex/:id", func(c *gin.Context) {
		var q struct {
			Search string `form:"search" binding:"omitempty,min=3"`
			Sort   string `form:"sort,default=asc" binding:"oneof=asc desc"`
			Page   int    `form:"page,default=1" binding:"min=1"`
		}
		if err := c.ShouldBindQuery(&q); err != nil {
			fail(c, err)
			return
		}
		in := &ComplexInput{ID: c.Param("id"), Search: q.Search, Sort: q.Sort, Page: q.Page}
		if err := c.ShouldBindJSON(&in.Body); err != nil {
			fail(c, err)
			return
		}
		out, _ := complexHandler(c.Request.Context(), in)
		c.JSON(out.Status, out.Body)
	})
	r.GET("/users/:id", func(c *gin.Context) {
		var in GetUserInput
		if err := c.ShouldBindUri(&in); err != nil {
			fail(c, err)
			return
		}
		out, _ := getUser(c.Request.Context(), &in)
		c.JSON(http.StatusOK, out.Body)
	})
	r.POST("/users", func(c *gin.Context) {
		in := &CreateUserInput{}
		if err := c.ShouldBindJSON(&in.Body); err != nil {
			fail(c, err)
			return
		}
		out, _ := createUser(c.Request.Context(), in)
		c.JSON(out.Status, out.Body)
	})
	return r
}

func stdServer() http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /complex/{id}", func(w http.ResponseWriter, r *http.Request) {
		in := &ComplexInput{ID: r.PathValue("id"), Search: r.URL.Query().Get("search")}
		if err := json.NewDecoder(r.Body).Decode(&in.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, _ := complexHandler(r.Context(), in)
		writeJSON(w, out.Status, out.Body)
	})
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, _ := getUser(r.Context(), &GetUserInput{ID: r.PathValue("id")})
		writeJSON(w, http.StatusOK, out.Body)
	})
	mux.HandleFunc("POST /users", func(w http.ResponseWriter, r *http.Request) {
		in := &CreateUserInput{}
		if err := json.NewDecoder(r.Body).Decode(&in.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, _ := createUser(r.Context(), in)
		writeJSON(w, out.Status, out.Body)
	})
	return mux
}

type discardWriter struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (w *discardWriter) Header() http.Header { return w.h }
func (w *discardWriter) WriteHeader(s int)   { w.status = s }
func (w *discardWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

type body struct {
	*bytes.Reader
	raw []byte
}

func (body) Close() error { return nil }

type scenario struct {
	name, method, target, body string
	status                     int
}

var scenarios = []scenario{
	{"Complex", http.MethodPost, "/complex/123?search=test&page=2", complexBody, http.StatusCreated},
	{"GetUser", http.MethodGet, "/users/u_42", "", http.StatusOK},
	{"CreateUser", http.MethodPost, "/users", createBody, http.StatusCreated},
}

var servers = []struct {
	name string
	h    http.Handler
}{
	{"stdlib-novalidation", stdServer()},
	{"huma", humaServer(true)},
	{"huma-nolinks", humaServer(false)},
	{"gin", ginServer()},
	{"rally", rallyServer()},
}

func newRequest(s scenario) (*http.Request, *body) {
	r := httptest.NewRequest(s.method, s.target, nil)
	b := body{bytes.NewReader(nil), []byte(s.body)}
	if s.body != "" {
		r.Header.Set("Content-Type", "application/json")
		r.Body = b
		r.ContentLength = int64(len(s.body))
	}
	return r, &b
}

func serve(h http.Handler, r *http.Request, b *body, w *discardWriter) {
	b.Reset(b.raw)
	clear(w.h)
	w.status = 0
	w.body.Reset()
	h.ServeHTTP(w, r)
}

func BenchmarkSequential(b *testing.B) {
	for _, s := range scenarios {
		for _, srv := range servers {
			b.Run(s.name+"/"+srv.name, func(b *testing.B) {
				r, rb := newRequest(s)
				w := &discardWriter{h: http.Header{}}
				b.ReportAllocs()
				for b.Loop() {
					serve(srv.h, r, rb, w)
					if w.status != s.status {
						b.Fatalf("status %d: %s", w.status, w.body.String())
					}
				}
			})
		}
	}
}

func BenchmarkParallel(b *testing.B) {
	for _, s := range scenarios {
		for _, srv := range servers {
			b.Run(s.name+"/"+srv.name, func(b *testing.B) {
				b.ReportAllocs()
				b.RunParallel(func(pb *testing.PB) {
					r, rb := newRequest(s)
					w := &discardWriter{h: http.Header{}}
					for pb.Next() {
						serve(srv.h, r, rb, w)
						if w.status != s.status {
							b.Errorf("status %d: %s", w.status, w.body.String())
							return
						}
					}
				})
			})
		}
	}
}

func TestSameResponses(t *testing.T) {
	for _, s := range scenarios {
		var want map[string]any
		for i, srv := range servers[1:] {
			r, rb := newRequest(s)
			w := &discardWriter{h: http.Header{}}
			serve(srv.h, r, rb, w)
			if w.status != s.status {
				t.Fatalf("%s/%s: status %d: %s", s.name, srv.name, w.status, w.body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			delete(got, "$schema")
			if i == 0 {
				want = got
			} else if !reflect.DeepEqual(got, want) {
				t.Errorf("%s/%s differs:\n got %v\nwant %v", s.name, srv.name, got, want)
			}
		}
	}
}

func TestSameValidationErrors(t *testing.T) {
	bad := scenario{"Complex", http.MethodPost, "/complex/1?search=ab&sort=up",
		strings.NewReplacer(`"int": 100`, `"int": 0`, `"name": "SubItem"`, `"name": "S"`).Replace(complexBody), 0}
	status := map[string]int{"huma-nolinks": http.StatusUnprocessableEntity, "rally": http.StatusBadRequest}
	errs := map[string]map[string]bool{}
	for _, srv := range servers {
		if status[srv.name] == 0 {
			continue
		}
		r, rb := newRequest(bad)
		w := &discardWriter{h: http.Header{}}
		serve(srv.h, r, rb, w)
		if w.status != status[srv.name] {
			t.Fatalf("%s: status %d: %s", srv.name, w.status, w.body.String())
		}
		var got struct {
			Errors []struct{ Message, Location, Detail, Code, In, Pointer, Parameter string }
		}
		_ = json.Unmarshal(w.body.Bytes(), &got)
		errs[srv.name] = map[string]bool{}
		for _, e := range got.Errors {
			if e.In != "" {
				e.Location, e.Message = humaLocation(e.In, e.Pointer, e.Parameter, e.Code), e.Detail
			}
			errs[srv.name][e.Location+": "+e.Message] = true
		}
	}
	if len(errs["rally"]) == 0 || !reflect.DeepEqual(errs["huma-nolinks"], errs["rally"]) {
		t.Errorf("different validation errors:\n huma  %v\n rally %v", errs["huma-nolinks"], errs["rally"])
	}
}

func TestGinValidates(t *testing.T) {
	var gin, rally http.Handler
	for _, srv := range servers {
		switch srv.name {
		case "gin":
			gin = srv.h
		case "rally":
			rally = srv.h
		}
	}
	complexWith := func(from, to string) string {
		if !strings.Contains(complexBody, from) {
			t.Fatalf("%q not in the Complex body", from)
		}
		return strings.Replace(complexBody, from, to, 1)
	}
	const complexURL = "/complex/1?search=test"
	for _, c := range []struct{ name, method, target, body string }{
		{"pattern", "POST", complexURL, complexWith(`"Benchmark"`, `"Bench42"`)},
		{"minimum", "POST", complexURL, complexWith(`"int": 100`, `"int": 0`)},
		{"maximum", "POST", complexURL, complexWith(`"int": 100`, `"int": 1001`)},
		{"float minimum", "POST", complexURL, complexWith(`"float": 0.1`, `"float": -0.1`)},
		{"uniqueItems", "POST", complexURL, complexWith(`["a"]`, `["a","a"]`)},
		{"minItems", "POST", complexURL, complexWith(`["a"]`, `[]`)},
		{"nested struct", "POST", complexURL, complexWith(`"SubItem"`, `"S"`)},
		{"slice item", "POST", complexURL, complexWith(`"Item"`, `"I"`)},
		{"map value", "POST", complexURL, complexWith(`"val"`, `"v"`)},
		{"unknown property", "POST", complexURL, complexWith(`"bool": true`, `"bool": true, "extra": 1`)},
		{"query minLength", "POST", "/complex/1?search=ab", complexBody},
		{"query enum", "POST", "/complex/1?sort=up", complexBody},
		{"query minimum", "POST", "/complex/1?page=0", complexBody},
		{"path maxLength", "GET", "/users/" + strings.Repeat("x", 37), ""},
		{"body maxLength", "POST", "/users", strings.Replace(createBody, `"Ada Lovelace"`, `"`+strings.Repeat("x", 65)+`"`, 1)},
		{"body maximum", "POST", "/users", strings.Replace(createBody, `"age":36`, `"age":151`, 1)},
	} {
		s := scenario{c.name, c.method, c.target, c.body, 0}
		for name, h := range map[string]http.Handler{"gin": gin, "rally": rally} {
			r, rb := newRequest(s)
			w := &discardWriter{h: http.Header{}}
			serve(h, r, rb, w)
			if w.status < 400 || w.status >= 500 {
				t.Errorf("%s: %s accepted it: %d %s", c.name, name, w.status, w.body.String())
			}
		}
	}
}

func humaLocation(in, pointer, parameter, code string) string {
	if parameter != "" {
		return in + "." + parameter
	}
	loc := in
	segs := strings.Split(pointer, "/")[1:]
	if code == "required" {
		segs = segs[:len(segs)-1]
	}
	for _, s := range segs {
		if _, err := strconv.Atoi(s); err == nil {
			loc += "[" + s + "]"
		} else {
			loc += "." + s
		}
	}
	return loc
}
