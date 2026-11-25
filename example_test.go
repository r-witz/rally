package rally_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/r-witz/rally"
)

type NoteInput struct {
	Title    string `json:"title" minLength:"1" maxLength:"100" example:"Groceries"`
	Priority string `json:"priority,omitempty" enum:"low,normal,high" default:"normal" description:"Sort order in lists."`
}

type Note struct {
	ID int `json:"id"`
	NoteInput
}

func call(h http.Handler, method, target, body string, headers ...string) {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	fmt.Println(w.Code, w.Header().Get("Location"), strings.TrimSpace(w.Body.String()))
}

// A small notes API: a validated request body, a 201 with a Location
// header, a path parameter and a 404.
func Example() {
	var mu sync.Mutex
	notes := map[int]Note{}

	mux := http.NewServeMux()
	api := rally.New(mux, rally.DefaultConfig("Notes", "1.0.0"))

	rally.Register(api, rally.Operation{
		Method:        http.MethodPost,
		Path:          "/notes",
		OperationID:   "create-note",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct{ Body NoteInput }) (*struct {
		Location string `header:"Location"`
		Body     Note
	}, error) {
		mu.Lock()
		defer mu.Unlock()
		n := Note{ID: len(notes) + 1, NoteInput: in.Body}
		notes[n.ID] = n
		return &struct {
			Location string `header:"Location"`
			Body     Note
		}{fmt.Sprintf("/notes/%d", n.ID), n}, nil
	})

	rally.Register(api, rally.Operation{
		Method:      http.MethodGet,
		Path:        "/notes/{id}",
		OperationID: "get-note",
		Errors:      []int{http.StatusNotFound},
	}, func(ctx context.Context, in *struct {
		ID int `path:"id" minimum:"1"`
	}) (*struct{ Body Note }, error) {
		mu.Lock()
		defer mu.Unlock()
		n, ok := notes[in.ID]
		if !ok {
			return nil, rally.Error404NotFound("no such note")
		}
		return &struct{ Body Note }{n}, nil
	})

	call(mux, http.MethodPost, "/notes", `{"title":"Groceries"}`)
	call(mux, http.MethodPost, "/notes", `{"title":"","color":"red"}`)
	call(mux, http.MethodGet, "/notes/1", "")
	call(mux, http.MethodGet, "/notes/2", "")
	// Output:
	// 201 /notes/1 {"id":1,"title":"Groceries","priority":"normal"}
	// 422  {"title":"Unprocessable Entity","status":422,"detail":"validation failed","errors":[{"detail":"expected length >= 1","code":"minLength","in":"body","pointer":"/title"},{"detail":"unexpected property","code":"additionalProperties","in":"body","pointer":"/color"}]}
	// 200  {"id":1,"title":"Groceries","priority":"normal"}
	// 404  {"title":"Not Found","status":404,"detail":"no such note"}
}

// A group shares a path prefix and middlewares. The middleware answers in
// the API's error format with WriteError.
func ExampleAPI_Group() {
	mux := http.NewServeMux()
	api := rally.New(mux, rally.DefaultConfig("Admin", "1.0.0"))

	requireToken := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer secret" {
				err := rally.Error401Unauthorized("missing or invalid token")
				err.Headers = http.Header{"WWW-Authenticate": {"Bearer"}}
				api.WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	admin := api.Group("/admin", requireToken)

	rally.Register(admin, rally.Operation{
		Method:      http.MethodGet,
		Path:        "/stats",
		OperationID: "get-stats",
		Errors:      []int{http.StatusUnauthorized},
	}, func(ctx context.Context, in *struct{}) (*struct {
		Body struct {
			Notes int `json:"notes"`
		}
	}, error) {
		out := &struct {
			Body struct {
				Notes int `json:"notes"`
			}
		}{}
		out.Body.Notes = 42
		return out, nil
	})

	call(mux, http.MethodGet, "/admin/stats", "")
	call(mux, http.MethodGet, "/admin/stats", "", "Authorization", "Bearer secret")
	// Output:
	// 401  {"title":"Unauthorized","status":401,"detail":"missing or invalid token"}
	// 200  {"notes":42}
}
