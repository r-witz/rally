package rally

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestUnsignedRules(t *testing.T) {
	type s struct {
		U uint16 `json:"u" minimum:"2.5" maximum:"1e30" exclusiveMaximum:"9" enum:"3,4,8"`
		V uint8  `json:"v" minimum:"-5"`
	}
	for in, want := range map[string][]string{
		`{"u":3,"v":0}`:   nil,
		`{"u":2,"v":0}`:   {"body/u: minimum: expected number >= 2.5", "body/u: enum: expected value to be one of \"3, 4, 8\""},
		`{"u":9,"v":0}`:   {"body/u: exclusiveMaximum: expected number < 9", "body/u: enum: expected value to be one of \"3, 4, 8\""},
		`{"u":3.5,"v":0}`: {"body/u: type: expected integer"},
		`{"u":5,"v":0}`:   {"body/u: enum: expected value to be one of \"3, 4, 8\""},
	} {
		_, errs, err := ourDecode[s](t, in)
		if err != nil || !slices.Equal(describe(errs), want) {
			t.Errorf("%s:\n got %q\nwant %q (%v)", in, describe(errs), want, err)
		}
	}
	type i struct {
		I int8 `json:"i" enum:"1,2"`
	}
	if _, errs, _ := ourDecode[i](t, `{"i":3}`); len(errs) != 1 || errs[0].Code != "enum" {
		t.Errorf("int enum: %v", errs)
	}
	for _, c := range []struct {
		s    string
		up   bool
		want uint64
		ok   bool
	}{{"-1", true, 0, true}, {"1e30", false, 255, true}, {"254.5", true, 255, true}, {"255.5", true, 255, false}, {"0.5", false, 0, true}} {
		got, ok := intAtMost[uint64](decimalOf(c.s), false, decimal{}, decimalOf("255"))
		if c.up {
			got, ok = intAtLeast[uint64](decimalOf(c.s), false, decimal{}, decimalOf("255"))
		}
		if got != c.want || ok != c.ok {
			t.Errorf("bound(%s, %v) = %d, %v, want %d, %v", c.s, c.up, got, ok, c.want, c.ok)
		}
	}
}

func TestIntegerExclusiveAndSteps(t *testing.T) {
	type s struct {
		A int64  `json:"a" exclusiveMinimum:"9223372036854775806"`
		B int8   `json:"b" exclusiveMaximum:"1e9" exclusiveMinimum:"-1e9" multipleOf:"0.5"`
		C uint64 `json:"c" exclusiveMinimum:"-3" exclusiveMaximum:"1e30" multipleOf:"1e20"`
		D uint16 `json:"d" exclusiveMaximum:"10.5" multipleOf:"0.25"`
		E int    `json:"e" multipleOf:"0.0000000000000000000000000000001234567890123456789"`
	}
	for in, want := range map[string][]string{
		`{"a":9223372036854775807,"b":-128,"c":0,"d":10,"e":0}`: nil,
		`{"a":9223372036854775806,"b":1,"c":1,"d":11,"e":1}`:    {"body/a: exclusiveMinimum: expected number > 9223372036854775806", "body/c: multipleOf: expected number to be a multiple of 1e20", "body/d: exclusiveMaximum: expected number < 10.5", "body/e: multipleOf: expected number to be a multiple of 0.0000000000000000000000000000001234567890123456789"},
	} {
		_, errs, err := ourDecode[s](t, in)
		if err != nil || !slices.Equal(describe(errs), want) {
			t.Errorf("%s:\n got %q\nwant %q (%v)", in, describe(errs), want, err)
		}
	}
}

func TestRules(t *testing.T) {
	type rules struct {
		Below  float64   `json:"below,omitempty" maximum:"9.5" enum:"2.5,9.5"`
		Level  uint8     `json:"level,omitempty" enum:"1,2"`
		Code   string    `json:"code,omitempty" maxLength:"2" pattern:"^[a-z]*$"`
		Ints   []int     `json:"ints,omitempty" minItems:"2" maxItems:"3" uniqueItems:"true"`
		Flags  []bool    `json:"flags,omitempty" uniqueItems:"true" maxItems:"9"`
		Ratios []float64 `json:"ratios,omitempty" uniqueItems:"true"`
	}
	const unique = "expected array items to be unique"
	for in, want := range map[string]string{
		`{"below":9.5,"level":2,"code":"ab","ints":[1,2,3],"flags":[true,false],"ratios":[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16]}`: "",
		`{"below":10}`:          `body/below: maximum: expected number <= 9.5; body/below: enum: expected value to be one of "2.5, 9.5"`,
		`{"level":3}`:           `body/level: enum: expected value to be one of "1, 2"`,
		`{"code":"abc"}`:        "body/code: maxLength: expected length <= 2",
		`{"code":"A"}`:          "body/code: pattern: expected string to match pattern ^[a-z]*$",
		`{"ints":[1]}`:          "body/ints: minItems: expected array length >= 2",
		`{"ints":[1,2,3,4]}`:    "body/ints: maxItems: expected array length <= 3",
		`{"ints":[1,2,1]}`:      "body/ints: uniqueItems: " + unique,
		`{"flags":[true,true]}`: "body/flags: uniqueItems: " + unique,
		`{"ratios":[0.5,0.5]}`:  "body/ratios: uniqueItems: " + unique,
		`{"ratios":[0.1,0.10000000000000000001]}`:                 "body/ratios: uniqueItems: " + unique,
		`{"ratios":[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,0]}`: "body/ratios: uniqueItems: " + unique,
	} {
		_, errs, err := ourDecode[rules](t, in)
		if got := strings.Join(describe(errs), "; "); err != nil || got != want {
			t.Errorf("%s: got %q %v, want %q", in, got, err, want)
		}
	}
	r := newRegistry()
	r.input = true
	r.schema(reflect.TypeFor[rules](), "rules")
	b, _ := json.Marshal(r.schemas["rules"].Properties)
	want := `{"below":{"type":"number","format":"double","enum":[2.5,9.5],"maximum":9.5},` +
		`"code":{"type":"string","maxLength":2,"pattern":"^[a-z]*$"},` +
		`"flags":{"type":"array","items":{"type":"boolean"},"maxItems":9,"uniqueItems":true},` +
		`"ints":{"type":"array","items":{"type":"integer","format":"int64","minimum":-9223372036854775808,"maximum":9223372036854775807},"minItems":2,"maxItems":3,"uniqueItems":true},` +
		`"level":{"type":"integer","format":"int32","enum":[1,2],"minimum":0,"maximum":255},` +
		`"ratios":{"type":"array","items":{"type":"number","format":"double"},"uniqueItems":true}}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

func TestFloatRules(t *testing.T) {
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodPost, Path: "/x", OperationID: "post-x"}, func(ctx context.Context, in *struct {
		Rate float32 `query:"rate" maximum:"0.1"`
		Any  float64 `query:"any" minimum:"0"`
		Body struct {
			Ratio float32 `json:"ratio" minimum:"0.7" enum:"0.7,0.9"`
		}
	}) (*struct{}, error) {
		return nil, nil
	})
	for _, c := range []struct {
		query, body string
		want        int
	}{
		{"rate=0.1&any=1e3", `{"ratio":0.7}`, 204},
		{"", `{"ratio":0.9}`, 204},
		{"", `{"ratio":0.8}`, 422},
		{"rate=0.11", `{"ratio":0.7}`, 400},
		{"any=NaN", `{"ratio":0.7}`, 400},
		{"any=-Inf", `{"ratio":0.7}`, 400},
		{"any=0x1p4", `{"ratio":0.7}`, 400},
	} {
		if w := do(mux, http.MethodPost, "/x?"+c.query, c.body); w.Code != c.want {
			t.Errorf("%s %s: status %d, want %d: %s", c.query, c.body, w.Code, c.want, w.Body.String())
		}
	}
}

func TestPatternSkippedAfterLength(t *testing.T) {
	type s struct {
		S string `json:"s" maxLength:"3" pattern:"^b"`
	}
	_, errs, _ := ourDecode[s](t, `{"s":"aaaa"}`)
	if want := []string{"body/s: maxLength: expected length <= 3"}; !slices.Equal(describe(errs), want) {
		t.Errorf("got %q, want %q", describe(errs), want)
	}
}

func TestExclusiveBoundsAndMultipleOf(t *testing.T) {
	type s struct {
		I int            `json:"i" exclusiveMinimum:"1.5" exclusiveMaximum:"10" multipleOf:"2"`
		U uint8          `json:"u" exclusiveMinimum:"0" multipleOf:"0.5"`
		F float64        `json:"f" exclusiveMinimum:"0.1" exclusiveMaximum:"1" multipleOf:"0.1"`
		M map[string]int `json:"m" minProperties:"1" maxProperties:"2"`
	}
	for in, want := range map[string][]string{
		`{"i":2,"u":1,"f":0.2,"m":{"a":1}}`:                      nil,
		`{"i":8,"u":255,"f":0.9,"m":{"a":1,"b":2}}`:              nil,
		`{"i":1,"u":0,"f":0.1,"m":{}}`:                           {"body/i: exclusiveMinimum: expected number > 1.5", "body/i: multipleOf: expected number to be a multiple of 2", "body/u: exclusiveMinimum: expected number > 0", "body/f: exclusiveMinimum: expected number > 0.1", "body/m: minProperties: expected object to have >= 1 properties"},
		`{"i":10,"u":1,"f":1,"m":{"a":1,"b":2,"c":3}}`:           {"body/i: exclusiveMaximum: expected number < 10", "body/f: exclusiveMaximum: expected number < 1", "body/m: maxProperties: expected object to have <= 2 properties"},
		`{"i":3,"u":1,"f":0.25,"m":{"a":1}}`:                     {"body/i: multipleOf: expected number to be a multiple of 2", "body/f: multipleOf: expected number to be a multiple of 0.1"},
		`{"i":2,"u":1,"f":0.1000000000000000000001,"m":{"a":1}}`: {"body/f: multipleOf: expected number to be a multiple of 0.1"},
	} {
		_, errs, err := ourDecode[s](t, in)
		if err != nil || !slices.Equal(describe(errs), want) {
			t.Errorf("%s:\n got %q\nwant %q (%v)", in, describe(errs), want, err)
		}
	}
}
