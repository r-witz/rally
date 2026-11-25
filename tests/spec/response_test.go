package spec

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
	"uuid"

	"github.com/r-witz/rally"
)

type respInner struct {
	Name string `json:"name"`
}

type respBody struct {
	ID       uuid.UUID            `json:"id"`
	At       time.Time            `json:"at"`
	Count    int64                `json:"count"`
	Small    uint8                `json:"small"`
	Ratio    float32              `json:"ratio"`
	Tags     []string             `json:"tags"`
	Labels   map[string]string    `json:"labels"`
	Inner    *respInner           `json:"inner"`
	Items    []respInner          `json:"items"`
	ByName   map[string]respInner `json:"by_name"`
	Note     string               `json:"note,omitempty"`
	Optional *int                 `json:"optional,omitzero"`
}

// TestResponsesMatchDocument checks rows 8.3 and 10.4 of docs/PROFILE.md:
// every response body validates against the schema the document gives for
// it, with nil slices, maps and pointers, omitted fields and extreme values.
func TestResponsesMatchDocument(t *testing.T) {
	one := 1
	bodies := []respBody{
		{},
		{ID: uuid.Max(), At: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -(23*3600+59*60))), Count: -1 << 63, Small: 255, Ratio: 3.4e38,
			Tags: []string{"", "é"}, Labels: map[string]string{"": "x"}, Inner: &respInner{"n"}, Items: []respInner{{}}, ByName: map[string]respInner{"k": {}}, Note: "n", Optional: &one},
		{At: time.Date(1900, 1, 1, 0, 0, 0, 0, time.FixedZone("LMT", 561)), Count: 1<<63 - 1, Ratio: -1e-45},
	}
	mux := http.NewServeMux()
	api := rally.New(mux, rally.Config{})
	for i := range bodies {
		rally.Register(api, rally.Operation{Method: http.MethodGet, Path: fmt.Sprintf("/r/%d", i), OperationID: fmt.Sprintf("r%d", i)},
			func(context.Context, *struct{}) (*struct{ Body respBody }, error) {
				return &struct{ Body respBody }{bodies[i]}, nil
			})
	}
	raw, err := api.OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	doc := parseDocument(t, raw)
	for i := range bodies {
		path := fmt.Sprintf("/r/%d", i)
		s, err := doc.schema("paths", path, "get", "responses", "200", "content", "application/json", "schema")
		if err != nil {
			t.Fatal(err)
		}
		r := do(mux, http.MethodGet, path, "", nil)
		if r.status != http.StatusOK || !validates(s, r.body) {
			t.Errorf("%s: status %d, body does not match the documented schema: %s", path, r.status, r.body)
		}
	}
}
