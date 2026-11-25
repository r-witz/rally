package rally

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestOpenAPIDocumentation(t *testing.T) {
	config := DefaultConfig("Docs", "1")
	config.Security = []map[string][]string{{"bearer": nil}}
	config.SecuritySchemes = map[string]*SecurityScheme{"bearer": {Type: "http", Scheme: "bearer"}}
	api := New(http.NewServeMux(), config)
	Register(api, Operation{Method: http.MethodGet, Path: "/a/{x}/{y}", OperationID: "get-a-x-y", Errors: []int{404}, Security: []map[string][]string{}},
		func(ctx context.Context, in *struct {
			X int `path:"x"`
		}) (*struct{ Body []string }, error) {
			return nil, nil
		})
	type secret struct {
		Key string `json:"key"`
	}
	Register(api, Operation{Method: http.MethodGet, Path: "/internal", OperationID: "internal", Hidden: true},
		func(ctx context.Context, in *struct{}) (*struct{ Body secret }, error) { return nil, nil })
	op := api.doc.Paths["/a/{x}/{y}"]["get"]
	if len(op.Parameters) != 2 || op.Parameters[1].Name != "y" || op.Parameters[1].In != "path" {
		t.Errorf("undeclared path parameter not documented: %+v", op.Parameters)
	}
	if codes := slices.Sorted(maps.Keys(op.Responses)); !slices.Equal(codes, []string{"200", "400", "404", "406", "500"}) {
		t.Errorf("responses %v", codes)
	}
	b, err := api.OpenAPIJSON()
	if err != nil || !json.Valid(b) {
		t.Fatalf("invalid document: %v", err)
	}
	if !strings.Contains(string(b), `"security":[{"bearer":[]}]`) {
		t.Error("scopes must be a list, even when nil")
	}
	if !strings.Contains(string(b), `"security":[]`) {
		t.Error("an operation without security must say so: security: []")
	}
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "internal") {
		t.Errorf("hidden operation documented: %s", b)
	}
}

type kindFields struct {
	Kind string `json:"kind" enum:"dog,cat" maxLength:"3" description:"The species." example:"dog"`
}

type level struct{ name string }

func (l *level) UnmarshalText(b []byte) error { l.name = string(b); return nil }

func (l *level) MarshalText() ([]byte, error) { return []byte(l.name), nil }

type describedPet struct {
	kindFields
	Size   *int      `json:"size" enum:"1,2"`
	Labels []string  `json:"labels" enum:"x"`
	Codes  *[]string `json:"codes,omitempty" enum:"c"`
}

type describedOutput struct {
	ETag string `header:"ETag" description:"Version of the pet." example:"\"1\""`
	Body describedPet
}

func TestDescriptionsExamplesAndResponseEnums(t *testing.T) {
	logs := captureLogs(t)
	mux, api := newAPI()
	var reply describedOutput
	Register(api, Operation{Method: http.MethodPost, Path: "/pets/{id}", OperationID: "create-pet"}, func(ctx context.Context, in *struct {
		ID    int   `path:"id" description:"The pet ID." example:"7"`
		Level level `query:"level" default:"high" example:"low"`
		Body  struct {
			kindFields
			Tags []string `json:"tags,omitempty" maxItems:"2" enum:"a,b" example:"a,b"`
		}
	}) (*describedOutput, error) {
		out := reply
		return &out, nil
	})
	b, err := api.OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"name":"id","in":"path","description":"The pet ID.","required":true,"schema":{"type":"integer","format":"int64","examples":[7],`,
		`{"name":"level","in":"query","schema":{"type":"string","default":"high","examples":["low"]}}`,
		`"headers":{"ETag":{"description":"Version of the pet.","schema":{"type":"string","examples":["\"1\""]}}}`,
		`"properties":{"kind":{"type":"string","description":"The species.","examples":["dog"],"enum":["dog","cat"],"maxLength":3},"tags":{"type":"array","examples":[["a","b"]],"items":{"type":"string","enum":["a","b"]},"maxItems":2}}`,
		`"properties":{"kind":{"type":"string","description":"The species.","examples":["dog"],"enum":["dog","cat"]},"size":{"type":["integer","null"],"format":"int64","enum":[1,2,null],`,
		`"labels":{"type":"array","items":{"type":"string","enum":["x"]}},"codes":{"type":"array","items":{"type":"string","enum":["c"]}}}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("document lacks %s", want)
		}
	}
	two, three := 2, 3
	for _, c := range []struct {
		pet  describedPet
		code int
	}{
		{describedPet{kindFields{"cat"}, &two, []string{"x"}, &[]string{"c"}}, http.StatusOK},
		{describedPet{kindFields: kindFields{"dog"}}, http.StatusOK},
		{describedPet{}, http.StatusInternalServerError},
		{describedPet{kindFields{"dog"}, &three, nil, nil}, http.StatusInternalServerError},
		{describedPet{kindFields{"dog"}, nil, []string{"y"}, nil}, http.StatusInternalServerError},
		{describedPet{kindFields{"dog"}, nil, nil, &[]string{"d"}}, http.StatusInternalServerError},
	} {
		reply.Body = c.pet
		logs.Reset()
		if w := do(mux, http.MethodPost, "/pets/1", `{"kind":"dog"}`); w.Code != c.code || (c.code == http.StatusInternalServerError) != strings.Contains(logs.String(), "not one of its enum values") {
			t.Errorf("%+v: %d %s, logs %q", c.pet, w.Code, w.Body.String(), logs.String())
		}
	}
}
