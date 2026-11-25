package spec

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/r-witz/rally"
)

// JSONTestSuite (https://github.com/nst/JSONTestSuite) classifies documents
// as y_ (must be accepted), n_ (must be rejected) and i_ (implementation
// defined). Every document is sent as a request body whose Go type is bool,
// so that a syntactically valid document fails only on its type: the oracle
// is whether the server reports a syntax error, not the status.
//
// The expected outcome of each i_ document is decided by docs/PROFILE.md
// (section JSON): RFC 8259 and RFC 7493 (I-JSON) are the references.
var jsonImplementationDefined = map[string]bool{
	// RFC 8259 §6 allows any number syntax; ranges are checked by the Go type.
	"i_number_double_huge_neg_exp.json":   true,
	"i_number_huge_exp.json":              true,
	"i_number_neg_int_huge_exp.json":      true,
	"i_number_pos_double_huge_exp.json":   true,
	"i_number_real_neg_overflow.json":     true,
	"i_number_real_pos_overflow.json":     true,
	"i_number_real_underflow.json":        true,
	"i_number_too_big_neg_int.json":       true,
	"i_number_too_big_pos_int.json":       true,
	"i_number_very_big_negative_int.json": true,
	"i_structure_500_nested_arrays.json":  true,
	// RFC 8259 §8.1: networked JSON MUST be UTF-8; RFC 7493 §2.1: I-JSON
	// MUST NOT contain invalid UTF-8 or lone surrogates (escaped or not).
	"i_object_key_lone_2nd_surrogate.json":                false,
	"i_string_1st_surrogate_but_2nd_missing.json":         false,
	"i_string_1st_valid_surrogate_2nd_invalid.json":       false,
	"i_string_UTF-16LE_with_BOM.json":                     false,
	"i_string_UTF-8_invalid_sequence.json":                false,
	"i_string_UTF8_surrogate_U+D800.json":                 false,
	"i_string_incomplete_surrogate_and_escape_valid.json": false,
	"i_string_incomplete_surrogate_pair.json":             false,
	"i_string_incomplete_surrogates_escape_valid.json":    false,
	"i_string_invalid_lonely_surrogate.json":              false,
	"i_string_invalid_surrogate.json":                     false,
	"i_string_invalid_utf-8.json":                         false,
	"i_string_inverted_surrogates_U+1D11E.json":           false,
	"i_string_iso_latin_1.json":                           false,
	"i_string_lone_second_surrogate.json":                 false,
	"i_string_lone_utf8_continuation_byte.json":           false,
	"i_string_not_in_unicode_range.json":                  false,
	"i_string_overlong_sequence_2_bytes.json":             false,
	"i_string_overlong_sequence_6_bytes.json":             false,
	"i_string_overlong_sequence_6_bytes_null.json":        false,
	"i_string_truncated-utf-8.json":                       false,
	"i_string_utf16BE_no_BOM.json":                        false,
	"i_string_utf16LE_no_BOM.json":                        false,
	// RFC 8259 §8.1: a parser MAY ignore a byte order mark; the profile
	// rejects it (strict option).
	"i_structure_UTF-8_BOM_empty_object.json": false,
}

func TestJSONTestSuite(t *testing.T) {
	mux := http.NewServeMux()
	api := rally.New(mux, rally.Config{})
	rally.Register(api, rally.Operation{Method: http.MethodPost, Path: "/doc", OperationID: "doc"},
		func(context.Context, *struct{ Body bool }) (*struct{}, error) { return &struct{}{}, nil })

	files, err := filepath.Glob("testdata/JSONTestSuite/test_parsing/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no JSONTestSuite files: %v", err)
	}
	sort.Strings(files)
	counts := map[string]int{}
	for _, path := range files {
		name := filepath.Base(path)
		var want bool
		switch name[0] {
		case 'y':
			want = true
		case 'n':
			want = false
		case 'i':
			w, ok := jsonImplementationDefined[name]
			if !ok {
				t.Errorf("%s: implementation-defined case without a profile decision", name)
				continue
			}
			want = w
		}
		doc, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		r := do(mux, http.MethodPost, "/doc", "application/json", doc)
		accepted := r.status != http.StatusRequestEntityTooLarge
		if r.status == http.StatusBadRequest {
			p := r.problem(t)
			accepted = !p.hasCode("syntax") && len(p.Errors) > 0
		}
		if accepted != want {
			counts["fail"]++
			t.Errorf("%s: accepted=%v, want %v (status %d: %.200s)", name, accepted, want, r.status, r.body)
		} else {
			counts["pass"]++
		}
	}
	t.Logf("JSONTestSuite: %d documents, %d pass, %d fail", len(files), counts["pass"], counts["fail"])
}
