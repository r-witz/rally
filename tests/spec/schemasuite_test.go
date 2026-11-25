package spec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/r-witz/rally"
)

type schemaTest struct {
	desc, data string
	valid      bool
	skip       string
}

type schemaCase struct {
	file, desc string
	skip       string
	register   func(api *rally.API, path, id string)
	tests      []schemaTest
}

// deviations are the suite tests rally fails on purpose, each by a row of
// class A of docs/PROFILE.md: valid values that the Go type cannot hold.
// The value is the row; "doc" marks those where the document cannot say
// it either, so that it disagrees with the behaviour too.
var deviations = map[string]struct {
	row string
	doc bool
}{
	"optional/bignum.json / integer / a bignum is an integer":                                                           {"2.2", false},
	"optional/bignum.json / integer / a negative bignum is an integer":                                                  {"2.2", false},
	"format-date-time.json / validation of date-time strings / a second fraction of fifteen nines is valid":             {"3.7", true},
	"format-date-time.json / validation of date-time strings / a valid date-time with a leap second, UTC":               {"3.5", true},
	"format-date-time.json / validation of date-time strings / a valid date-time with a leap second, with minus offset": {"3.5", true},
}

func register[T any](api *rally.API, path, id string) {
	rally.Register(api, rally.Operation{Method: http.MethodPost, Path: path, OperationID: id},
		func(context.Context, *struct{ Body T }) (*struct{}, error) { return &struct{}{}, nil })
}

// TestJSONSchemaSuite runs the JSON-Schema-Test-Suite cases (dialect
// 2020-12) that can be expressed as Go types: each instance is sent as
// {"v": instance}, and must be accepted (2xx) when valid and rejected with
// a 4xx when invalid.
func TestJSONSchemaSuite(t *testing.T) {
	skipped := map[string]int{}
	run, pass, docMismatch, deviated := 0, 0, 0, 0
	used := map[string]bool{}
	for i, c := range schemaSuite {
		if c.skip != "" {
			skipped[c.skip] += len(c.tests)
			continue
		}
		mux := http.NewServeMux()
		api := rally.New(mux, rally.Config{})
		path := fmt.Sprintf("/c/%d", i)
		if msg := registers(func() { c.register(api, path, fmt.Sprintf("c%d", i)) }); msg != "" {
			for range c.tests {
				run++
			}
			t.Errorf("%s / %s: registration panicked: %s", c.file, c.desc, msg)
			continue
		}
		raw, err := api.OpenAPIJSON()
		if err != nil {
			t.Errorf("%s / %s: no document: %v", c.file, c.desc, err)
			continue
		}
		body, err := parseDocument(t, raw).schema("paths", path, "post", "requestBody", "content", "application/json", "schema")
		if errors.Is(err, errNoNode) {
			skipped["pattern without node, the reference engine"] += len(c.tests)
			continue
		}
		if err != nil {
			t.Errorf("%s / %s: the documented schema does not compile: %v", c.file, c.desc, err)
			continue
		}
		for _, tc := range c.tests {
			if tc.skip != "" {
				skipped[tc.skip]++
				continue
			}
			run++
			instance := []byte(`{"v":` + tc.data + `}`)
			r := do(mux, http.MethodPost, path, "application/json", instance)
			accepted := r.status >= 200 && r.status < 300
			key := c.file + " / " + c.desc + " / " + tc.desc
			dev, deviates := deviations[key]
			if deviates {
				used[key] = true
				if accepted == tc.valid {
					t.Errorf("%s: listed as a deviation (profile row %s) but rally conforms", key, dev.row)
				}
				if documentSays := validates(body, instance); documentSays == accepted && dev.doc {
					t.Errorf("%s: listed as a document deviation but the document agrees", key)
				}
				deviated++
				continue
			}
			if documentSays := validates(body, instance); documentSays != accepted {
				docMismatch++
				t.Errorf("%s / %s / %s: data %s: the document says valid=%v, rally accepted=%v", c.file, c.desc, tc.desc, tc.data, documentSays, accepted)
			}
			if accepted == tc.valid {
				pass++
				continue
			}
			t.Errorf("%s / %s / %s: data %s: accepted=%v, want %v (status %d: %.300s)", c.file, c.desc, tc.desc, tc.data, accepted, tc.valid, r.status, r.body)
		}
	}
	for key := range deviations {
		if !used[key] {
			t.Errorf("deviation %q matches no test", key)
		}
	}
	total := run
	var reasons []string
	for r, n := range skipped {
		total += n
		reasons = append(reasons, fmt.Sprintf("%5d  %s", n, r))
	}
	sort.Strings(reasons)
	t.Logf("JSON-Schema-Test-Suite (2020-12): %d tests, %d run, %d pass, %d deviations (profile class A), %d fail, %d skipped; document and behaviour disagree on %d:\n%s",
		total, run, pass, deviated, run-pass-deviated, total-run, docMismatch, strings.Join(reasons, "\n"))
}

func registers(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}
