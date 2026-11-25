package rally

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGenericSchemaNames(t *testing.T) {
	type page[T any] struct {
		Items []T `json:"items"`
	}
	if got := schemaName(reflect.TypeFor[page[time.Time]]()); got != "pageTime" {
		t.Errorf("got %q", got)
	}
}

func TestRequestResponseSchemas(t *testing.T) {
	type owner struct {
		Name string `json:"name"`
	}
	type ownerInput struct {
		Name string `json:"name" minLength:"1"`
	}
	type petFields struct {
		Name   string   `json:"name" minLength:"1"`
		Status string   `json:"status,omitempty" default:"available"`
		Tags   []string `json:"tags,omitempty" maxItems:"10"`
	}
	type petInput struct {
		petFields
		Secret string      `json:"secret"`
		Owner  *ownerInput `json:"owner"`
	}
	type pet struct {
		ID string `json:"id"`
		petFields
		Owner  *owner `json:"owner"`
		Parent *pet   `json:"parent,omitempty"`
		Stats  struct {
			Visits int `json:"visits"`
		} `json:"stats"`
	}
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodPost, Path: "/pets", OperationID: "post-pets"}, func(ctx context.Context, in *struct{ Body petInput }) (*struct{ Body pet }, error) {
		return &struct{ Body pet }{pet{ID: "1", petFields: in.Body.petFields}}, nil
	})
	if w := do(mux, http.MethodPost, "/pets", `{"name":"Rex","secret":"s","owner":null}`); w.Code != http.StatusOK || w.Body.String() != `{"id":"1","name":"Rex","status":"available","owner":null,"stats":{"visits":0}}` {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	if w := do(mux, http.MethodPost, "/pets", `{"id":"9","name":"Rex","secret":"s","owner":null}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a request field missing from the request type must be rejected: %d %s", w.Code, w.Body.String())
	}
	if got := slices.Sorted(maps.Keys(api.registry.schemas)); !slices.Equal(got, []string{"ErrorDetail", "ErrorModel", "owner", "ownerInput", "pet", "petInput"}) {
		t.Errorf("components %v", got)
	}
	want := map[string]string{
		"petInput": `{"type":"object","additionalProperties":false,"required":["name","secret","owner"],"properties":{` +
			`"name":{"type":"string","minLength":1},"status":{"type":"string","default":"available"},"tags":{"type":"array","items":{"type":"string"},"maxItems":10},` +
			`"secret":{"type":"string"},"owner":{"anyOf":[{"$ref":"#/components/schemas/ownerInput"},{"type":"null"}]}}}`,
		"pet": `{"type":"object","additionalProperties":false,"required":["id","name","owner","stats"],"properties":{` +
			`"id":{"type":"string"},"name":{"type":"string"},"status":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},` +
			`"owner":{"anyOf":[{"$ref":"#/components/schemas/owner"},{"type":"null"}]},"parent":{"$ref":"#/components/schemas/pet"},` +
			`"stats":{"type":"object","additionalProperties":false,"required":["visits"],"properties":{"visits":{"type":"integer","format":"int64","minimum":-9223372036854775808,"maximum":9223372036854775807}}}}}`,
	}
	for name, w := range want {
		b, err := json.Marshal(api.registry.schemas[name])
		if err != nil || string(b) != w {
			t.Errorf("%s:\n got %s\nwant %s", name, b, w)
		}
	}
}

func TestIntegerBounds(t *testing.T) {
	r := newRegistry()
	r.input = true
	r.schema(reflect.TypeFor[struct {
		I8  int8   `json:"i8"`
		I32 int32  `json:"i32"`
		U16 uint16 `json:"u16"`
		U32 uint32 `json:"u32"`
		U64 uint64 `json:"u64"`
		Lim int8   `json:"lim" minimum:"-500" maximum:"10"`
	}](), "Ints")
	b, _ := json.Marshal(r.schemas["Ints"].Properties)
	want := `{"i32":{"type":"integer","format":"int32","minimum":-2147483648,"maximum":2147483647},` +
		`"i8":{"type":"integer","format":"int32","minimum":-128,"maximum":127},` +
		`"lim":{"type":"integer","format":"int32","minimum":-128,"maximum":10},` +
		`"u16":{"type":"integer","format":"int32","minimum":0,"maximum":65535},` +
		`"u32":{"type":"integer","format":"int64","minimum":0,"maximum":4294967295},` +
		`"u64":{"type":"integer","minimum":0,"maximum":18446744073709551615}}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

func TestOneDirectionPerType(t *testing.T) {
	type address struct {
		City string `json:"city"`
	}
	type user struct {
		ID   int      `json:"id"`
		Home *address `json:"home"`
	}
	type signup struct {
		Name string   `json:"name"`
		Home *address `json:"home"`
	}
	type withDefault struct {
		Lang string `json:"lang,omitempty" default:"fr"`
	}
	type shared struct {
		Name string   `json:"name"`
		Home *address `json:"home"`
	}
	type node struct {
		Next *node `json:"next"`
	}
	panicOf := func(register func(api *API)) (msg string) {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
			}
		}()
		register(New(http.NewServeMux(), DefaultConfig("Directions", "1")))
		return ""
	}
	post := func(api *API, path string, hidden bool, in func(api *API, op Operation)) {
		in(api, Operation{Method: http.MethodPost, Path: path, OperationID: "post" + strings.ReplaceAll(path, "/", "-"), Hidden: hidden})
	}
	request := func(api *API, op Operation) {
		Register(api, op, func(ctx context.Context, in *struct{ Body signup }) (*struct{}, error) { return nil, nil })
	}
	response := func(api *API, op Operation) {
		Register(api, op, func(ctx context.Context, in *struct{}) (*struct{ Body []user }, error) { return nil, nil })
	}
	for name, c := range map[string]struct {
		register func(api *API)
		want     string
	}{
		"same body": {func(api *API) {
			Register(api, Operation{Method: http.MethodPut, Path: "/users", OperationID: "put-users"}, func(ctx context.Context, in *struct{ Body user }) (*struct{ Body user }, error) {
				return nil, nil
			})
		}, "rally.user is used in the request body of PUT /users and in the response body of PUT /users: requests and responses need their own types"},
		"nested type": {func(api *API) {
			post(api, "/signup", false, request)
			post(api, "/list", false, response)
		}, "rally.address is used in the request body of POST /signup and in the response body of POST /list"},
		"hidden operation": {func(api *API) {
			post(api, "/list", false, response)
			post(api, "/signup", true, request)
		}, "rally.address is used in the request body of POST /signup and in the response body of POST /list"},
		"struct field of an embedded struct": {func(api *API) {
			Register(api, Operation{Method: http.MethodPost, Path: "/a", OperationID: "post-a"}, func(ctx context.Context, in *struct {
				Body struct{ shared }
			}) (*struct {
				Body struct {
					ID int `json:"id"`
					shared
				}
			}, error) {
				return nil, nil
			})
		}, "rally.address is used in the request body of POST /a and in the response body of POST /a"},
		"self reference": {func(api *API) {
			Register(api, Operation{Method: http.MethodPost, Path: "/a", OperationID: "post-a"}, func(ctx context.Context, in *struct{ Body node }) (*struct{}, error) {
				return nil, nil
			})
			Register(api, Operation{Method: http.MethodGet, Path: "/b", OperationID: "get-b"}, func(ctx context.Context, in *struct{}) (*struct{ Body []node }, error) {
				return nil, nil
			})
		}, "rally.node is used in the request body of POST /a and in the response body of GET /b"},
		"groups": {func(api *API) {
			post(api.Group("/v1"), "/signup", false, request)
			post(api.Group("/v2"), "/list", false, response)
		}, "rally.address is used in the request body of POST /v1/signup and in the response body of POST /v2/list"},
		"default in a response": {func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x"}, func(ctx context.Context, in *struct{}) (*struct{ Body withDefault }, error) {
				return nil, nil
			})
		}, "the default of rally.withDefault.Lang has no effect: rally.withDefault is only used in responses"},
	} {
		if got := panicOf(c.register); !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	got := panicOf(func(api *API) {
		_ = panicOf(func(*API) {
			Register(api, Operation{Method: http.MethodPost, Path: "/bad", OperationID: "post-bad"}, func(ctx context.Context, in *struct{ Body signup }) (*struct {
				Body []user `maxItems:"1"`
			}, error) {
				return nil, nil
			})
		})
		post(api, "/list", false, response)
	})
	if got != "" {
		t.Errorf("a failed registration must not claim its types: %s", got)
	}
}
