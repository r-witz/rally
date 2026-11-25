package rally

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"
)

type pagination struct {
	Page int `query:"page" default:"1"`
}

func TestDocumentedErrors(t *testing.T) {
	mux, api := newAPI()
	type body struct {
		Name string `json:"name"`
		Note string `json:"note,omitempty"`
	}
	Register(api, Operation{Method: http.MethodPost, Path: "/p/{id}", OperationID: "post-p-id", Errors: []int{404}}, func(ctx context.Context, in *struct {
		ID   int `path:"id"`
		Body body
	}) (*struct{}, error) {
		return nil, errors.New("boom")
	})
	Register(api, Operation{Method: http.MethodPost, Path: "/b", OperationID: "post-b", MaxBodyBytes: 32}, func(ctx context.Context, in *struct{ Body body }) (*struct{}, error) {
		return nil, nil
	})
	Register(api, Operation{Method: http.MethodPost, Path: "/n", OperationID: "post-n", Errors: []int{422}}, noop)
	Register(api, Operation{Method: http.MethodPost, Path: "/g", OperationID: "post-g"}, noop)
	for path, want := range map[string][]string{
		"/p/{id}": {"204", "400", "404", "413", "415", "422", "500"},
		"/b":      {"204", "400", "413", "415", "422", "500"},
		"/n":      {"204", "400", "422", "500"},
		"/g":      {"204", "400", "500"},
	} {
		responses := api.doc.Paths[path]["post"].Responses
		if got := slices.Sorted(maps.Keys(responses)); !slices.Equal(got, want) {
			t.Errorf("%s: responses %v, want %v", path, got, want)
		}
		if r := responses["500"]; r.Description != "Internal Server Error" || r.Content["application/problem+json"].Schema.Ref != schemaRefPrefix+"ErrorModel" {
			t.Errorf("%s 500: %+v", path, r)
		}
	}
	for _, c := range []struct {
		code             int
		target, body, ct string
	}{
		{400, "/p/x", `{"name":"a"}`, ""},
		{413, "/p/1", `{"name":"` + strings.Repeat("a", 1<<20) + `"}`, ""},
		{415, "/p/1", `{"name":"a"}`, "text/plain"},
		{422, "/p/1", `{}`, ""},
		{500, "/p/1", `{"name":"a"}`, ""},
		{400, "/b", `{"name":`, ""},
		{400, "/b", ``, ""},
		{413, "/b", `{"name":"` + strings.Repeat("a", 40) + `"}`, ""},
		{204, "/b", `{"name":"a"}`, "application/merge-patch+json"},
	} {
		var headers []string
		if c.ct != "" {
			headers = []string{"Content-Type", c.ct}
		}
		if w := do(mux, http.MethodPost, c.target, c.body, headers...); w.Code != c.code {
			t.Errorf("%s %s: status %d, want %d", c.target, c.body, w.Code, c.code)
		}
	}
	if got := problems(do(mux, http.MethodPost, "/b", `{"name":`)); !slices.Equal(got, []string{"body: syntax: invalid JSON: unexpected end of JSON input at offset 8"}) {
		t.Errorf("malformed body: %q", got)
	}
}

func registerBody[T any](api *API) {
	Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x"}, func(ctx context.Context, in *struct{ Body T }) (*struct{}, error) { return nil, nil })
}

func registerInput[I any](api *API) {
	Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x"}, func(ctx context.Context, in *I) (*struct{}, error) { return nil, nil })
}

func registerOutput[O any](api *API) {
	Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x"}, func(ctx context.Context, in *struct{}) (*O, error) { return nil, nil })
}

type unmarshalOnly struct{ a, b int }

func (u *unmarshalOnly) UnmarshalText([]byte) error { return nil }

func TestRegistrationPanics(t *testing.T) {
	for _, c := range []struct {
		name, want string
		register   func(api *API)
	}{
		{"multipleOf of 0", "expected a number greater than 0", registerBody[struct {
			N int `json:"n" multipleOf:"0"`
		}]},
		{"no integer between exclusive bounds", "no value can pass them", registerBody[struct {
			N int `json:"n" exclusiveMinimum:"1" exclusiveMaximum:"2"`
		}]},
		{"no integer between a minimum and an exclusive maximum", "no value can pass them", registerBody[struct {
			N int `json:"n" minimum:"5.2" exclusiveMaximum:"6"`
		}]},
		{"exclusive minimum at the top of int64", "no value can pass them", registerBody[struct {
			N int64 `json:"n" exclusiveMinimum:"9223372036854775807"`
		}]},
		{"exclusive maximum at the bottom of int64", "no value can pass them", registerBody[struct {
			N int64 `json:"n" exclusiveMaximum:"-9223372036854775808"`
		}]},
		{"exclusive minimum at the top of uint8", "no value can pass them", registerBody[struct {
			U uint8 `json:"u" exclusiveMinimum:"255"`
		}]},
		{"exclusive maximum of 0 on uint", "no value can pass them", registerBody[struct {
			U uint `json:"u" exclusiveMaximum:"0"`
		}]},
		{"exclusive bounds equal", "no value can pass them", registerBody[struct {
			F float64 `json:"f" exclusiveMinimum:"1" maximum:"1"`
		}]},
		{"enum at an exclusive maximum", "does not pass its other rules", registerBody[struct {
			N int `json:"n" exclusiveMaximum:"3" enum:"2,3"`
		}]},
		{"enum at an exclusive minimum", "does not pass its other rules", registerBody[struct {
			F float64 `json:"f" exclusiveMinimum:"0.5" enum:"0.5,1"`
		}]},
		{"enum not a multiple", "does not pass its other rules", registerBody[struct {
			N int `json:"n" multipleOf:"2" enum:"2,3"`
		}]},
		{"map without maxProperties", "without maxProperties", registerBody[struct {
			M map[string]int `json:"m"`
		}]},
		{"nullable", "a pointer accepts null, unless omitempty", registerBody[struct {
			N *int `json:"n" nullable:"true"`
		}]},
		{"hidden", "the hidden tag is not supported", registerBody[struct {
			N int `json:"n" hidden:"true"`
		}]},
		{"additionalProperties", "unknown properties are always rejected", registerBody[struct {
			_ struct{} `additionalProperties:"true"`
		}]},
		{"maxProperties on a number", "tag maxProperties is not valid", registerBody[struct {
			N int `json:"n" maxProperties:"2"`
		}]},
		{"minProperties above maxProperties", "no value can pass them", registerBody[struct {
			M map[string]int `json:"m" minProperties:"3" maxProperties:"2"`
		}]},
		{"cookie", "read cookies in a middleware", registerInput[struct {
			S string `cookie:"s"`
		}]},
		{"pattern on a time", "tag pattern is not valid", registerBody[struct {
			T time.Time `json:"t" pattern:"^2"`
		}]},
		{"enum on a time parameter", "tag enum is not valid", registerInput[struct {
			T time.Time `query:"t" enum:"x"`
		}]},
		{"interface", "interface types", registerBody[struct {
			A any `json:"a"`
		}]},
		{"bytes", "base64", registerBody[struct {
			B []byte `json:"b"`
		}]},
		{"array", "use a slice", registerBody[struct {
			A [2]int `json:"a"`
		}]},
		{"custom JSON", "custom JSON methods", registerBody[struct {
			J json.Number `json:"j"`
			R json.RawMessage
		}]},
		{"map key", "map keys must be strings", registerBody[struct {
			M map[int]string `json:"m"`
		}]},
		{"pointer to pointer", "pointers to pointers", registerBody[struct {
			P **int `json:"p"`
		}]},
		{"default on an object", "default is not supported", registerBody[struct {
			S struct{ A int } `json:"s,omitempty" default:"{}"`
		}]},
		{"header list", "only query parameters can be lists", registerInput[struct {
			H []string `header:"h"`
		}]},
		{"response header list", "must be a string", registerOutput[struct {
			L []string `header:"Link"`
		}]},
		{"typed response header", "must be a string", registerOutput[struct {
			Count int `header:"X-Count"`
		}]},
		{"text response header", "must be a string", registerOutput[struct {
			H unmarshalOnly `header:"X-H"`
		}]},
		{"timeFormat on a parameter", "timeFormat tag is not supported", registerInput[struct {
			Since time.Time `query:"since" timeFormat:"2006-01-02"`
		}]},
		{"lookahead in a pattern", "(?: is the only supported group", registerBody[struct {
			S string `json:"s" pattern:"^(?=.*[0-9]).+$"`
		}]},
		{"backreference in a pattern", "backreferences are not supported", registerBody[struct {
			S string `json:"s" pattern:"^(a|b)\\1$"`
		}]},
		{"Go syntax in a pattern", "(?: is the only supported group", registerBody[struct {
			S string `json:"s" pattern:"(?i)^[a-z]+$"`
		}]},
		{"Unicode property that is not a category or a script", "unsupported Unicode property Emoji", registerBody[struct {
			S string `json:"s" pattern:"^\\p{Emoji}$"`
		}]},
		{"typo in a parameter tag", "is not used", registerInput[struct {
			Limit int `qeury:"limit"`
		}]},
		{"stray output field", "is not used", registerOutput[struct {
			Count int
		}]},
		{"unknown parameter option", `unknown option "form"`, registerInput[struct {
			S []string `query:"s,form"`
		}]},
		{"parameter not in the path", "is not in the path", registerInput[struct {
			ID string `path:"id"`
		}]},
		{"parameter declared twice", `query parameter "q" of GET /x is declared twice`, registerInput[struct {
			A string `query:"q"`
			B string `query:"q"`
		}]},
		{"header declared twice", `header parameter "X-Token" of GET /x is declared twice`, registerInput[struct {
			A string `header:"x-token"`
			B string `header:"X-Token"`
		}]},
		{"rule on the wrong type", "tag minLength is not valid", registerBody[struct {
			N int `json:"n" minLength:"1"`
		}]},
		{"invalid default", `invalid default "many"`, registerInput[struct {
			N int `query:"n" default:"many"`
		}]},
		{"required on a body field", "the required tag is not supported on", registerBody[struct {
			N string `json:"n" required:"true"`
		}]},
		{"default on a required parameter", "the parameter is required", registerInput[struct {
			N string `query:"n" required:"true" default:"x"`
		}]},
		{"json string option", `the json ",string" option is not supported`, registerBody[struct {
			N int `json:"n,string"`
		}]},
		{"operation ID missing", "OperationID is required", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/x"}, noop)
		}},
		{"operation ID taken", `OperationID "get-x" is already used by GET /x`, func(api *API) {
			registerInput[struct{}](api)
			Register(api, Operation{Method: http.MethodGet, Path: "/y", OperationID: "get-x"}, noop)
		}},
		{"method missing", "method and path are required", func(api *API) {
			Register(api, Operation{Path: "/x", OperationID: "get-x"}, noop)
		}},
		{"method OpenAPI cannot document", "hide the operation", func(api *API) {
			Register(api, Operation{Method: "PROPFIND", Path: "/x", OperationID: "propfind-x"}, noop)
		}},
		{"input that is not a struct", "input and output must be structs", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x"}, func(context.Context, *int) (*struct{}, error) { return nil, nil })
		}},
		{"embedded pointer", "embed the struct by value", registerBody[struct{ *Embedded }]},
		{"duplicate JSON property", `duplicate JSON property "E2"`, func(api *API) {
			type other struct{ E2 int }
			registerBody[struct {
				Embedded
				other
			}](api)
		}},
		{"status that is not an int", "Status must be an int", registerOutput[struct{ Status string }]},
		{"explode option", `unknown option "explode"`, registerInput[struct {
			Q []string `query:"q,explode"`
		}]},
		{"required on a path parameter", "the required tag has no effect on path parameter", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/x/{id}", OperationID: "get-x"}, func(ctx context.Context, in *struct {
				ID string `path:"id" required:"false"`
			}) (*struct{}, error) {
				return nil, nil
			})
		}},
		{"JSON list default", "list values are comma-separated", registerBody[struct {
			L []string `json:"l,omitempty" default:"[\"a\"]"`
		}]},
		{"invalid required value", `invalid required tag "yes"`, registerInput[struct {
			Q string `query:"q" required:"yes"`
		}]},
		{"schema name used twice", `schema name "item" is used by both`, func(api *API) {
			{
				type item struct{ A int }
				registerBody[struct{ I item }](api)
			}
			type item struct{ B int }
			Register(api, Operation{Method: http.MethodGet, Path: "/b", OperationID: "get-b"}, func(ctx context.Context, in *struct{}) (*struct{ Body item }, error) {
				return nil, nil
			})
		}},
		{"readOnly", "the readOnly tag is not supported (query parameter", registerInput[struct {
			Q string `query:"q" readOnly:"true"`
		}]},
		{"default on the request body", "the default tag has no effect on the body", func(api *API) {
			Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x"}, func(ctx context.Context, in *struct {
				Body *string `default:"x"`
			}) (*struct{}, error) {
				return nil, nil
			})
		}},
		{"parameter tag in a body", "the query tag has no effect on", registerBody[struct {
			Q string `json:"q" query:"q"`
		}]},
		{"writeOnly", "the writeOnly tag is not supported", registerBody[struct {
			S string `json:"s" writeOnly:"true"`
		}]},
		{"typo of an unsupported tag", "the readOnly tag is not supported", registerBody[struct {
			S string `json:"s" readonly:"true"`
		}]},
		{"rule on the response body", "the maxLength tag has no effect on", registerOutput[struct {
			Body string `maxLength:"3"`
		}]},
		{"rule on a response header", "the pattern tag has no effect on", registerOutput[struct {
			ETag string `header:"ETag" pattern:"^a"`
		}]},
		{"tag on the status", "the required tag has no effect on", registerOutput[struct {
			Status int `required:"true"`
		}]},
		{"rule on an ignored field", "the minLength tag has no effect on", registerBody[struct {
			S string `json:"-" minLength:"1"`
		}]},
		{"recursive type without a struct", "recursive without a struct", func(api *API) {
			type tree map[string][]tree
			registerBody[tree](api)
		}},
		{"schema name OpenAPI rejects", "is not valid in OpenAPI", func(api *API) {
			type café struct{ N int }
			registerBody[café](api)
		}},
		{"success status outside 200-399", "DefaultStatus and Statuses must be between 200 and 399", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x", DefaultStatus: 404}, noop)
		}},
		{"error status outside 400-599", "Errors must be between 400 and 599", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x", Errors: []int{201}}, noop)
		}},
		{"default status without a body", "a 204 response has no body", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x", DefaultStatus: 204}, func(ctx context.Context, in *struct{}) (*struct{ Body string }, error) {
				return nil, nil
			})
		}},
		{"statuses without a Status field", "Statuses needs a Status field", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x", Statuses: []int{201}}, noop)
		}},
		{"Status field without statuses", "list the statuses the handler sets in Operation.Statuses", registerOutput[struct{ Status int }]},
		{"unexported parameter", "is the unexported field limit", registerInput[struct {
			limit int `query:"limit"`
		}]},
		{"embedded pointer with parameters", "embed the struct by value", registerInput[struct{ *pagination }]},
		{"NaN bound", "expected a JSON number", registerBody[struct {
			F float64 `json:"f" maximum:"NaN"`
		}]},
		{"infinite enum", `invalid enum value "Inf"`, registerBody[struct {
			F float64 `json:"f" enum:"1,Inf"`
		}]},
		{"minimum above maximum", "contradict each other", registerBody[struct {
			N int `json:"n" minimum:"10" maximum:"1"`
		}]},
		{"minimum above the type", "contradict each other or its type", registerInput[struct {
			N int8 `query:"n" minimum:"200"`
		}]},
		{"minLength above maxLength", "contradict each other", registerBody[struct {
			S string `json:"s" minLength:"5" maxLength:"2"`
		}]},
		{"minItems above maxItems", "contradict each other", registerBody[struct {
			L []int `json:"l" minItems:"3" maxItems:"2"`
		}]},
		{"enum outside the type", "the enum value 300", registerBody[struct {
			N int8 `json:"n" enum:"1,300"`
		}]},
		{"enum breaking another rule", "the enum value abc", registerBody[struct {
			S string `json:"s" enum:"ab,abc" maxLength:"2"`
		}]},
		{"embedded text type", "give the embedded field a name", registerBody[struct {
			textType
			Name string `json:"name"`
		}]},
		{"text map key", "map keys must be plain strings", registerBody[struct {
			M map[textKey]int `json:"m"`
		}]},
		{"invalid header name", `"X Id" is not a valid header name`, registerInput[struct {
			H string `header:"X Id"`
		}]},
		{"Host header", "read Request.Host", registerInput[struct {
			H string `header:"host"`
		}]},
		{"response header declared twice", "is declared twice", registerOutput[struct {
			A string `header:"ETag"`
			B string `header:"etag"`
		}]},
		{"undeclared security scheme", `security scheme "oauth", which is not in Config.SecuritySchemes`, func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x", Security: []map[string][]string{{"oauth": nil}}}, noop)
		}},
		{"documented QUERY", "OpenAPI cannot document the method QUERY", func(api *API) {
			Register(api, Operation{Method: "QUERY", Path: "/search", OperationID: "search"}, noop)
		}},
		{"path ending with a slash", "matches every path below it", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/items/", OperationID: "get-items"}, noop)
		}},
		{"paths differing by parameter names", "differ only by parameter names", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/a/{id}", OperationID: "get-a"}, noop)
			Register(api, Operation{Method: http.MethodDelete, Path: "/a/{name}", OperationID: "delete-a"}, noop)
		}},
		{"unbounded request list", "rally.listBody.Tags is a list in a request body without maxItems", registerBody[listBody]},
		{"unbounded request body list", "the body of POST /x is a list in a request body without maxItems", registerBody[[]int]},
		{"list of lists", "without maxItems", registerBody[struct {
			L [][]int `json:"l" maxItems:"2"`
		}]},
		{"channel", "is not supported", registerBody[struct {
			C chan int `json:"c"`
		}]},
		{"struct parameter", "unsupported type", registerInput[struct {
			S struct{ X int } `query:"s"`
		}]},
		{"default of the wrong type", "invalid default", registerBody[struct {
			N int `json:"n" default:"x"`
		}]},
		{"default breaking a rule", "invalid default", registerBody[struct {
			N int `json:"n" default:"0" minimum:"1"`
		}]},
		{"no unsigned integer between fractional bounds", "no value can pass them", registerBody[struct {
			U uint8 `json:"u" minimum:"1.2" maximum:"1.8"`
		}]},
		{"response text type without MarshalText", "no MarshalText", registerOutput[struct{ Body unmarshalOnly }]},
		{"map of lists", "without maxItems", registerBody[struct {
			M map[string][]int `json:"m" maxProperties:"2"`
		}]},
		{"rule in a response type", "only used in responses, where only enum is checked", registerOutput[struct{ Body ruleBody }]},
		{"example breaking a rule", "invalid example", registerBody[struct {
			N int `json:"n" example:"0" minimum:"1"`
		}]},
		{"parameter example of the wrong type", `invalid example "x"`, registerInput[struct {
			N int `query:"n" example:"x"`
		}]},
		{"example on a struct", "example is not supported on", registerBody[struct {
			S struct{} `json:"s" example:"x"`
		}]},
		{"description on the body", "the description tag has no effect on the body", func(api *API) {
			Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x"}, func(ctx context.Context, in *struct {
				Body struct{} `description:"x"`
			}) (*struct{}, error) {
				return nil, nil
			})
		}},
		{"default in an anonymous response body", "has no effect", registerOutput[struct {
			Body struct {
				S string `json:"s,omitempty" default:"x"`
			}
		}]},
		{"bound outside int64", "contradict each other or its type", registerBody[struct {
			N int64 `json:"n" minimum:"1e19"`
		}]},
		{"bound outside float32", "contradict each other or its type", registerBody[struct {
			F float32 `json:"f" maximum:"-1e39"`
		}]},
		{"string rule on a UUID", "tag maxLength is not valid", registerBody[struct {
			ID uuid.UUID `json:"id" maxLength:"10"`
		}]},
		{"enum value a text type rejects", "the enum value nope", registerBody[struct {
			A netip.Addr `json:"a" enum:"10.0.0.1,nope"`
		}]},
		{"Transfer-Encoding header", "read Request.TransferEncoding", registerInput[struct {
			H string `header:"Transfer-Encoding"`
		}]},
		{"default status 205 with a body", "a 205 response has no body", func(api *API) {
			Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x", DefaultStatus: 205}, func(ctx context.Context, in *struct{}) (*struct{ Body string }, error) {
				return nil, nil
			})
		}},
		{"tag on an embedded struct", "the minimum tag has no effect on", registerInput[struct {
			pagination `minimum:"5"`
		}]},
		{"optional body that is not a pointer", "can only be optional if Body is a pointer", func(api *API) {
			Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x"}, func(ctx context.Context, in *struct {
				Body struct{ N int } `required:"false"`
			}) (*struct{}, error) {
				return nil, nil
			})
		}},
		{"path without a leading slash", "does not start with /", func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "api.example.com/x", OperationID: "get-x"}, noop)
		}},
		{"routes OpenAPI cannot tell apart", `documents it as GET /a/{x}, like operation "one"`, func(api *API) {
			Register(api, Operation{Method: http.MethodGet, Path: "/a/{x}", OperationID: "one"}, noop)
			Register(api, Operation{Method: http.MethodGet, Path: "/a/{x...}", OperationID: "rest"}, noop)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if v := recover(); v == nil || !strings.Contains(fmt.Sprint(v), c.want) {
					t.Errorf("panic %q, want it to contain %q", v, c.want)
				}
			}()
			c.register(New(http.NewServeMux(), DefaultConfig("Unsupported", "1")))
		})
	}
}

type listBody struct {
	Tags []string `json:"tags"`
}

type ruleBody struct {
	S string `json:"s" maxLength:"3"`
}

type textKey string

func (k *textKey) UnmarshalText(b []byte) error { *k = textKey(b); return nil }

type textList []textList

func (l textList) MarshalText() ([]byte, error) { return nil, nil }

func (l *textList) UnmarshalText([]byte) error { return nil }

func TestRegistrationAccepts(t *testing.T) {
	type café struct{ N int }
	api := New(http.NewServeMux(), DefaultConfig("Accepted", "1"))
	registerBody[struct {
		L []textList          `json:"l" maxItems:"1"`
		M map[string]textList `json:"m" maxProperties:"1"`
	}](api)
	Register(api, Operation{Method: http.MethodPost, Path: "/hidden", OperationID: "post-hidden", Hidden: true}, func(ctx context.Context, in *struct{ Body café }) (*struct{}, error) {
		return nil, nil
	})
	registerInput[struct {
		ID prefixedID `query:"id"`
	}](api)
	Register(api, Operation{Method: http.MethodPut, Path: "/shared", OperationID: "put-shared"}, func(ctx context.Context, in *struct{ Body sharedInput }) (*struct{ Body sharedOutput }, error) {
		return nil, nil
	})
}

type sharedFields struct {
	Stats struct {
		N int `json:"n" minimum:"0"`
	} `json:"stats"`
}

type sharedInput struct{ sharedFields }

type sharedOutput struct {
	ID int `json:"id"`
	sharedFields
}

type prefixedID struct{ uuid.UUID }

func (p prefixedID) MarshalText() ([]byte, error) { return []byte("id-" + p.String()), nil }

func (p *prefixedID) UnmarshalText(b []byte) error {
	return p.UUID.UnmarshalText(bytes.TrimPrefix(b, []byte("id-")))
}

type marshalOnly struct{ n int }

func (m marshalOnly) MarshalText() ([]byte, error) {
	return strconv.AppendInt(nil, int64(m.n), 10), nil
}

func TestFailedRegistrationIsNotCached(t *testing.T) {
	type unreadable struct {
		Name string      `json:"name"`
		M    marshalOnly `json:"m"`
		Last string      `json:"last"`
	}
	for attempt := range 2 {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("attempt %d: expected a panic", attempt)
				}
			}()
			api := New(http.NewServeMux(), DefaultConfig("Cache", "1"))
			Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x"}, func(ctx context.Context, in *struct{ Body unreadable }) (*struct{}, error) {
				return nil, nil
			})
		}()
	}
}
