package spec

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r-witz/rally"
)

// RFC 3339 section 5.6 (ABNF), with the profile's decisions:
//   - "T" and "Z" may be lower case (section 5.6 NOTE): accepted, written back upper case;
//   - a leap second (time-second 60) is valid but time.Time cannot hold it: rejected (profile: option A);
//   - "-00:00" (section 4.3, unknown local offset) differs from "Z" but time.Time cannot tell them apart: rejected (option A);
//   - more than 9 fractional digits are valid but time.Time keeps 9: rejected (option A);
//   - a space instead of "T" is allowed by the NOTE "for readability" only by applications that choose it: rejected (strict).
var rfc3339Cases = []struct {
	in     string
	valid  bool
	out    string // the value written back, when valid
	reason string
}{
	{"1985-04-12T23:20:50.52Z", true, "1985-04-12T23:20:50.52Z", "§5.8 example"},
	{"1996-12-19T16:39:57-08:00", true, "1996-12-19T16:39:57-08:00", "§5.8 example"},
	{"1937-01-01T12:00:27.87+00:20", true, "1937-01-01T12:00:27.87+00:20", "§5.8 example"},
	{"1985-04-12t23:20:50.52z", true, "1985-04-12T23:20:50.52Z", "§5.6 NOTE: lower case t and z"},
	{"2000-02-29T00:00:00Z", true, "2000-02-29T00:00:00Z", "§5.7 leap year"},
	{"0000-01-01T00:00:00Z", true, "0000-01-01T00:00:00Z", "§5.6 date-fullyear = 4DIGIT"},
	{"9999-12-31T23:59:59.999999999+23:59", true, "9999-12-31T23:59:59.999999999+23:59", "§5.6 time-numoffset hour 23, minute 59"},
	{"2020-01-01T00:00:00.000Z", true, "2020-01-01T00:00:00Z", "fraction of zeros"},
	{"1990-12-31T23:59:60Z", false, "", "§5.6 time-second 60: valid, but time.Time cannot hold a leap second (option A)"},
	{"1990-12-31T15:59:60-08:00", false, "", "§5.6 time-second 60 with an offset (option A)"},
	{"2020-01-01T00:00:00-00:00", false, "", "§4.3 unknown local offset, which time.Time cannot tell from Z (option A)"},
	{"2020-01-01T00:00:00.1234567891Z", false, "", "§5.6 time-secfrac = \".\" 1*DIGIT: valid, but time.Time keeps 9 digits (option A)"},
	{"2020-01-01 00:00:00Z", false, "", "§5.6 NOTE: a space is an application choice; the profile requires T"},
	{"2020-01-01T00:00:00", false, "", "§5.6 time-offset is required"},
	{"2020-01-01T00:00Z", false, "", "§5.6 time-second is required"},
	{"2020-01-01T00:00:00,5Z", false, "", "§5.6 time-secfrac starts with \".\""},
	{"2020-01-01T00:00:00.Z", false, "", "§5.6 time-secfrac needs a digit"},
	{"2020-01-01T24:00:00Z", false, "", "§5.6 time-hour 00-23"},
	{"2020-01-01T00:60:00Z", false, "", "§5.6 time-minute 00-59"},
	{"2020-01-01T00:00:61Z", false, "", "§5.6 time-second 00-60"},
	{"2020-13-01T00:00:00Z", false, "", "§5.6 date-month 01-12"},
	{"2020-00-01T00:00:00Z", false, "", "§5.6 date-month 01-12"},
	{"2021-02-29T00:00:00Z", false, "", "§5.7 February 29 of a non-leap year"},
	{"1900-02-29T00:00:00Z", false, "", "§5.7 1900 is not a leap year"},
	{"2020-04-31T00:00:00Z", false, "", "§5.7 April has 30 days"},
	{"2020-01-01T00:00:00+24:00", false, "", "§5.6 time-numoffset hour 00-23"},
	{"2020-01-01T00:00:00+01:60", false, "", "§5.6 time-numoffset minute 00-59"},
	{"2020-01-01T00:00:00+0100", false, "", "§5.6 time-numoffset has a colon"},
	{"2020-01-01T00:00:00+01", false, "", "§5.6 time-numoffset has minutes"},
	{"20200101T000000Z", false, "", "§5.6 extended format only"},
	{"2020-1-01T00:00:00Z", false, "", "§5.6 date-month = 2DIGIT"},
	{"+2020-01-01T00:00:00Z", false, "", "§5.6 date-fullyear = 4DIGIT"},
	{"2020-01-01T00:00:00Z ", false, "", "trailing data"},
	{"2020-01-01T00:00:00.5\x00Z", false, "", "control character"},
	{"２０２０-01-01T00:00:00Z", false, "", "DIGIT is ASCII (RFC 5234)"},
}

func TestRFC3339(t *testing.T) {
	type in struct {
		T time.Time `json:"t"`
	}
	type out struct {
		T time.Time `json:"t"`
	}
	mux := http.NewServeMux()
	api := rally.New(mux, rally.Config{})
	rally.Register(api, rally.Operation{Method: http.MethodPost, Path: "/t", OperationID: "t"},
		func(_ context.Context, i *struct{ Body in }) (*struct{ Body out }, error) {
			return &struct{ Body out }{out{i.Body.T}}, nil
		})
	raw, err := api.OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	schema, err := parseDocument(t, raw).schema("paths", "/t", "post", "requestBody", "content", "application/json", "schema")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rfc3339Cases {
		body, _ := json.Marshal(map[string]string{"t": c.in})
		r := do(mux, http.MethodPost, "/t", "application/json", body)
		accepted := r.status == http.StatusOK
		optionA := strings.Contains(c.reason, "option A")
		if documented := validates(schema, body); documented != accepted && !optionA || documented == accepted && optionA {
			t.Errorf("%q (%s): the document says valid=%v, rally accepted=%v", c.in, c.reason, documented, accepted)
		}
		if accepted != c.valid {
			t.Errorf("%q (%s): accepted=%v, want %v (status %d: %.200s)", c.in, c.reason, accepted, c.valid, r.status, r.body)
			continue
		}
		if accepted {
			var got struct{ T string }
			_ = json.Unmarshal(r.body, &got)
			if got.T != c.out {
				t.Errorf("%q (%s): written back as %q, want %q", c.in, c.reason, got.T, c.out)
			}
		}
	}
}
