package rally

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"
	"weak"
)

func where(e ErrorDetail) string {
	if e.Parameter != "" {
		return e.In + "." + e.Parameter
	}
	return e.In + e.Pointer
}

func describe(errs []ErrorDetail) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = where(e) + ": " + e.Code + ": " + e.Detail
	}
	return out
}

type complexSub struct {
	ID   int    `json:"id" minimum:"1"`
	Name string `json:"name" minLength:"3" maxLength:"20"`
}

type complexBody struct {
	String    string                `json:"string" minLength:"1" maxLength:"100" pattern:"^[a-zA-Z ]+$"`
	Int       int                   `json:"int" minimum:"1" maximum:"1000"`
	Float     float64               `json:"float" minimum:"0" maximum:"1"`
	Bool      bool                  `json:"bool"`
	Time      time.Time             `json:"time"`
	Slice     []string              `json:"slice" minItems:"1" maxItems:"10" uniqueItems:"true"`
	Sub       complexSub            `json:"sub"`
	SubSlice  []complexSub          `json:"sub_slice" minItems:"1" maxItems:"5"`
	Map       map[string]complexSub `json:"map" maxProperties:"10"`
	Recursive *complexBody          `json:"recursive,omitempty"`
}

type upperText string

func (u upperText) MarshalText() ([]byte, error) { return []byte(strings.ToUpper(string(u))), nil }

func TestDecodeEdges(t *testing.T) {
	type s struct {
		T  time.Time       `json:"t"`
		C  code            `json:"c" maxLength:"3"`
		U  upperText       `json:"u"`
		B  *bool           `json:"b,omitempty"`
		F  *float64        `json:"f,omitempty"`
		L  *[]int          `json:"l,omitempty"`
		M  map[string]int  `json:"m"`
		S  struct{ X int } `json:"s"`
		UI []uint          `json:"ui" uniqueItems:"true"`
	}
	for in, want := range map[string][]string{
		`{"t":1,"c":"abc","u":"x","m":{},"s":{"X":1},"ui":[]}`:                                               {"body/t: type: expected string"},
		`{"t":"2020-01-0xT00:00:00Z","c":"abc","u":"x","m":{},"s":{"X":1},"ui":[]}`:                          {"body/t: format: expected string to be RFC 3339 date-time"},
		`{"t":"2020-01-01T00:00:00+0a:00","c":"abc","u":"x","m":{},"s":{"X":1},"ui":[]}`:                     {"body/t: format: expected string to be RFC 3339 date-time"},
		`{"t":"2019-02-29T00:00:00Z","c":1,"u":"x","m":{},"s":{"X":1},"ui":[]}`:                              {"body/t: format: expected string to be RFC 3339 date-time", "body/c: type: expected string"},
		`{"t":"2020-02-29T00:00:00Z","c":"abcd","u":1,"m":[],"s":[],"ui":[1,1]}`:                             {"body/c: maxLength: expected length <= 3", "body/u: type: expected string", "body/m: type: expected object", "body/s: type: expected object", "body/ui: uniqueItems: expected array items to be unique"},
		`{"t":"2020-02-29T00:00:00Z","c":"a","u":"x","b":null,"f":null,"l":null,"m":{},"s":{"X":1},"ui":[]}`: {"body/b: type: expected boolean", "body/f: type: expected number", "body/l: type: expected array"},
		`{"t":"2020-02-29T00:00:00Z","c":"a","u":"x","m":{"a":1},"s":{"X":1},"ui":[1,2]}`:                    nil,
	} {
		_, errs, err := ourDecode[s](t, in)
		if err != nil || !slices.Equal(describe(errs), want) {
			t.Errorf("%s:\n got %q\nwant %q (%v)", in, describe(errs), want, err)
		}
	}
	type m struct {
		M map[string]int `json:"m"`
	}
	if _, _, err := ourDecode[m](t, `{"m":{"a":1,2:3}}`); err == nil {
		t.Error("map with a number key: no syntax error")
	}
	one := 1
	r := &rules{maxProperties: &one}
	r.precompute()
	var mv map[string]int
	d := &decodeState{}
	if err := d.decode(compileDecoder(reflect.TypeFor[map[string]int](), r, nil, false), []byte(`{"a":1,"b":2,3:4}`), reflect.ValueOf(&mv).Elem(), "body"); err == nil {
		t.Error("skipped members with a number key: no syntax error")
	}
	defer func() {
		if r := recover(); r != "boom" {
			t.Errorf("a panic in UnmarshalText must propagate, got %v", r)
		}
	}()
	_, _, _ = ourDecode[struct {
		C code `json:"c"`
	}](t, `{"c":"panic"}`)
}

func TestWideStruct(t *testing.T) {
	var fields []reflect.StructField
	for i := range 130 {
		fields = append(fields, reflect.StructField{Name: "F" + strconv.Itoa(i), Type: reflect.TypeFor[int](), Tag: reflect.StructTag(`json:"f` + strconv.Itoa(i) + `,omitempty"`)})
	}
	typ := reflect.StructOf(fields)
	v := reflect.New(typ).Elem()
	d := &decodeState{}
	if err := d.decode(decoderFor(typ), []byte(`{"f129":7,"f0":1,"zz":2}`), v, "body"); err != nil || v.Field(129).Int() != 7 || describe(d.errs)[0] != "body/zz: additionalProperties: unexpected property" {
		t.Errorf("err=%v errs=%v", err, d.errs)
	}
}

// specToV2 rewrites what the specs allow and json/v2 rejects, so that it can
// serve as the reference: integers written with a fraction or an exponent
// (JSON Schema Validation section 6.1.1) become plain integers, and "time"
// members are upper-cased ("t" and "z", RFC 3339 section 5.6).
func specToV2(data []byte) []byte {
	dec := jsontext.NewDecoder(bytes.NewReader(data), jsontext.AllowDuplicateNames(true))
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out, jsontext.AllowDuplicateNames(true))
	type frame struct {
		obj bool
		n   int
	}
	var stack []frame
	name := ""
	for {
		tok, err := dec.ReadToken()
		if err == io.EOF {
			return out.Bytes()
		}
		if err != nil {
			return data
		}
		k := tok.Kind()
		isName := k == '"' && len(stack) > 0 && stack[len(stack)-1].obj && stack[len(stack)-1].n%2 == 0
		switch {
		case isName:
			name = tok.String()
			err = enc.WriteToken(tok)
		case k == '0' && strings.ContainsAny(tok.String(), ".eE"):
			s := tok.String()
			if d, ok := parseDecimal(s); ok && d.integral() {
				if mag, fits, _ := d.magnitude(); fits {
					s = strconv.FormatUint(mag, 10)
					if strings.HasPrefix(tok.String(), "-") {
						s = "-" + s
					}
				}
			}
			err = enc.WriteValue(jsontext.Value(s))
		case k == '"' && name == "time":
			err = enc.WriteToken(jsontext.String(strings.ToUpper(tok.String())))
		default:
			err = enc.WriteToken(tok)
		}
		if err != nil {
			return data
		}
		switch k {
		case '{', '[':
			stack = append(stack, frame{obj: k == '{'})
		case '}', ']':
			stack = stack[:len(stack)-1]
			fallthrough
		default:
			if len(stack) > 0 {
				stack[len(stack)-1].n++
			}
		}
	}
}

func skipDecoder(d *decodeState, _ reflect.Value) { d.skipValue() }

var fuzzSeeds = []string{
	complexReq,
	`{"a":[1,2,{"b":null}],"c":"🎉"}`, `-0.5e+10`, `[]`, `{}`, `"é"`, "\"\xff\"",
	`[1,]`, `01`, ` true `, `nul`, `{"string":"x","int":1}`, `{"sub":{"id":1,"name":"abc"},"sub":{}}`,
	`{"time":"2023-01-01T00:00:00+05:30"}`, `{"float":1e400}`, `"\ud800"`, `[[[[]]]]`,
	`{"string":"A","int":1,"float":0,"bool":true,"time":"0000-01-01t00:00:00Z","slice":[""],"sub":{"id":1,"name":"000"},"sub_slice":[{"id":1,"name":"000"}],"map":{}}`,
	`{"string": "A", "int":10, "float":1, "bool": true, "time": "0000-01-01T0:00:00Z","slice":[""],"sub":{"id":1,"name":"000"},"sub_slice":[{"id":1,"name":"000"}],"map":{}}`,
}

func FuzzScanner(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		d := &decodeState{}
		err := d.decode(skipDecoder, data, reflect.Value{}, "")
		valid := jsontext.Value(data).IsValid(jsontext.AllowDuplicateNames(true))
		if (err == nil) != valid {
			t.Fatalf("%q: our error=%v, stdlib valid=%v", data, err, valid)
		}
	})
}

func FuzzDecode(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	dec := decoderFor(reflect.TypeFor[complexBody]())
	f.Fuzz(func(t *testing.T, data []byte) {
		var ours complexBody
		d := &decodeState{}
		if err := d.decode(dec, data, reflect.ValueOf(&ours).Elem(), "body"); err != nil || len(d.errs) > 0 {
			return
		}
		var std complexBody
		if err := jsonv2.Unmarshal(specToV2(data), &std); err != nil {
			t.Fatalf("accepted here but rejected by json/v2 (%v): %q", err, data)
		}
		a, _ := json.Marshal(ours)
		b, _ := json.Marshal(std)
		if string(a) != string(b) {
			t.Fatalf("decoded differently from json/v2:\n ours %s\n  std %s\ninput %q", a, b, data)
		}
		got, err := ourEncode(t, ours)
		if err != nil {
			t.Fatal(err)
		}
		if want := stdEncode(t, ours); got != want {
			t.Fatalf("encoded differently:\n got %s\nwant %s", got, want)
		}
	})
}

func decoderFor(t reflect.Type) decFunc {
	return compileDecoder(t, nil, nil, t.Kind() == reflect.Pointer)
}

func ourDecode[T any](t testing.TB, data string) (T, []ErrorDetail, error) {
	t.Helper()
	var v T
	d := &decodeState{}
	err := d.decode(decoderFor(reflect.TypeFor[T]()), []byte(data), reflect.ValueOf(&v).Elem(), "body")
	return v, d.errs, err
}

type color string

type kitchen struct {
	Embedded
	S         string              `json:"s"`
	SEmpty    string              `json:"s_empty,omitempty"`
	B         bool                `json:"b"`
	I         int                 `json:"i"`
	I8        int8                `json:"i8"`
	I16       int16               `json:"i16"`
	I32       int32               `json:"i32"`
	I64       int64               `json:"i64"`
	U         uint                `json:"u"`
	U8        uint8               `json:"u8"`
	U16       uint16              `json:"u16"`
	U32       uint32              `json:"u32"`
	U64       uint64              `json:"u64"`
	F32       float32             `json:"f32"`
	F64       float64             `json:"f64"`
	P         *string             `json:"p"`
	PNil      *string             `json:"p_nil"`
	POmit     *int                `json:"p_omit,omitempty"`
	Sl        []int               `json:"sl"`
	SlEmpty   []string            `json:"sl_empty"`
	SlOmit    []string            `json:"sl_omit,omitempty"`
	M         map[string]int      `json:"m"`
	MStruct   map[string]Embedded `json:"m_struct"`
	T         time.Time           `json:"t"`
	TZero     time.Time           `json:"t_zero,omitzero"`
	Text      textType            `json:"text"`
	TextPtr   *textType           `json:"text_ptr"`
	Nested    *kitchen            `json:"nested,omitempty"`
	Named     color               `json:"named"`
	ZeroF     float64             `json:"zero_f,omitzero"`
	NegZero   float64             `json:"neg_zero,omitzero"`
	EmptyNegZ float64             `json:"empty_neg_zero,omitempty"`
	Dash      int                 `json:"-,"`
	Skip      int                 `json:"-"`
	NoTag     string
	unexp     int
}

func sampleKitchen() kitchen {
	s := "pointed"
	return kitchen{
		Embedded: Embedded{E1: "e", E2: 2},
		S:        "hé \"quoted\" \\ <b>&</b> \n\t\u2028\u2029 \x01 🎉",
		B:        true,
		I:        -42, I8: math.MinInt8, I16: math.MaxInt16, I32: math.MinInt32, I64: math.MaxInt64,
		U: 42, U8: math.MaxUint8, U16: math.MaxUint16, U32: math.MaxUint32, U64: math.MaxUint64,
		F32: 3.1415927, F64: 1e21,
		P:       &s,
		Sl:      []int{1, 2, 3},
		SlEmpty: []string{},
		M:       map[string]int{"b": 2, "a": 1, "c": 3},
		MStruct: map[string]Embedded{"x": {E1: "y"}},
		T:       time.Date(2024, 2, 29, 13, 14, 15, 123456789, time.FixedZone("", 3600)),
		Text:    textType{"txt"},
		TextPtr: &textType{"ptr"},
		Nested:  &kitchen{SlEmpty: []string{"x"}, Sl: []int{}, M: map[string]int{}, MStruct: map[string]Embedded{}},
		Named:   "red",
		NegZero: math.Copysign(0, -1), EmptyNegZ: math.Copysign(0, -1),
		Dash:  5,
		NoTag: "notag",
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	want := sampleKitchen()
	want.TZero = time.Time{}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, errs, err := ourDecode[kitchen](t, string(data))
	if err != nil || len(errs) > 0 {
		t.Fatalf("err=%v errs=%v", err, errs)
	}
	gotJSON, _ := json.Marshal(got)
	if string(gotJSON) != string(data) {
		t.Errorf("round trip mismatch\n got: %s\nwant: %s", gotJSON, data)
	}
}

func TestDecodeScalars(t *testing.T) {
	type nums struct {
		I8  int8    `json:"i8"`
		U8  uint8   `json:"u8"`
		I64 int64   `json:"i64"`
		U64 uint64  `json:"u64"`
		F32 float32 `json:"f32"`
		F64 float64 `json:"f64"`
	}
	ok := []struct {
		in   string
		want nums
	}{
		{`{"i8":-128,"u8":255,"i64":-9223372036854775808,"u64":18446744073709551615,"f32":1.5,"f64":-1e-300}`,
			nums{-128, 255, math.MinInt64, math.MaxUint64, 1.5, -1e-300}},
		{" {\n\t\"i8\" : 0 , \"u8\":0,\"i64\":9223372036854775807,\"u64\":0,\"f32\":0,\"f64\":1E+2 } ",
			nums{0, 0, math.MaxInt64, 0, 0, 100}},
		{`{"i8":1e2,"u8":-0,"i64":-9.223372036854775808e18,"u64":1.8446744073709551615e19,"f32":0,"f64":0}`,
			nums{100, 0, math.MinInt64, math.MaxUint64, 0, 0}},
		{`{"i8":1.0,"u8":0.5e1,"i64":-0.0,"u64":10000000000000000000e-19,"f32":0,"f64":0}`,
			nums{1, 5, 0, 1, 0, 0}},
	}
	for _, c := range ok {
		got, errs, err := ourDecode[nums](t, c.in)
		if err != nil || len(errs) > 0 || got != c.want {
			t.Errorf("%s: got %+v errs=%v err=%v", c.in, got, errs, err)
		}
	}
	bad := map[string]string{
		`{"i8":128,"u8":0,"i64":0,"u64":0,"f32":0,"f64":0}`:                      "body/i8: expected number <= 127",
		`{"i8":0,"u8":-1,"i64":0,"u64":0,"f32":0,"f64":0}`:                       "body/u8: expected number >= 0",
		`{"i8":0,"u8":0,"i64":9223372036854775808,"u64":0,"f32":0,"f64":0}`:      "body/i64: expected number <= 9223372036854775807",
		`{"i8":0,"u8":0,"i64":0,"u64":18446744073709551616,"f32":0,"f64":0}`:     "body/u64: expected number <= 18446744073709551615",
		`{"i8":1.5,"u8":0,"i64":0,"u64":0,"f32":0,"f64":0}`:                      "body/i8: expected integer",
		`{"i8":1.275e2,"u8":0,"i64":0,"u64":0,"f32":0,"f64":0}`:                  "body/i8: expected integer",
		`{"i8":1.28e2,"u8":0,"i64":0,"u64":0,"f32":0,"f64":0}`:                   "body/i8: expected number <= 127",
		`{"i8":0,"u8":0,"i64":0,"u64":1.8446744073709551616e19,"f32":0,"f64":0}`: "body/u64: expected number <= 18446744073709551615",
		`{"i8":0,"u8":0,"i64":-9.223372036854775809e18,"u64":0,"f32":0,"f64":0}`: "body/i64: expected number >= -9223372036854775808",
		`{"i8":"1","u8":0,"i64":0,"u64":0,"f32":0,"f64":0}`:                      "body/i8: expected integer",
		`{"i8":0,"u8":0,"i64":0,"u64":0,"f32":1e39,"f64":0}`:                     "body/f32: expected number within range",
		`{"i8":0,"u8":0,"i64":0,"u64":0,"f32":0,"f64":null}`:                     "body/f64: expected number",
		`{"i8":0,"u8":0,"i64":0,"u64":0,"f32":0}`:                                "body/f64: expected required property f64 to be present",
		`{"i8":0,"u8":0,"i64":0,"u64":0,"f32":0,"f64":0,"F64":1}`:                "body/F64: unexpected property",
		`{"i8":0,"u8":0,"i64":0,"u64":0,"f32":0,"f64":0,"f64":1}`:                "body/f64: duplicate property",
		`{"i8":0,"u8":0,"i64":0,"u64":0,"f32":0,"f64":0,"\u0066\u00364":1}`:      "body/f64: duplicate property",
		`{"i8":true,"u8":[],"i64":{},"u64":"","f32":false,"f64":0}`:              "body/i8: expected integer",
	}
	for in, want := range bad {
		_, errs, err := ourDecode[nums](t, in)
		if err != nil {
			t.Errorf("%s: unexpected syntax error %v", in, err)
			continue
		}
		if len(errs) == 0 || where(errs[0])+": "+errs[0].Detail != want {
			t.Errorf("%s:\n got %v\nwant %s", in, errs, want)
		}
	}
}

func TestDecodeStrings(t *testing.T) {
	cases := map[string]string{
		`"plain"`:                    "plain",
		`"\"\\\/\b\f\n\r\t"`:         "\"\\/\b\f\n\r\t",
		`"\u00e9\u20AC\ud83c\udf89"`: "é€🎉",
		`"é€🎉"`:                      "é€🎉",
		`"\u0000"`:                   "\x00",
		`"mixed \u00e9 and é"`:       "mixed é and é",
		`""`:                         "",
	}
	for in, want := range cases {
		got, errs, err := ourDecode[string](t, in)
		if err != nil || len(errs) > 0 || got != want {
			t.Errorf("%s: got %q errs=%v err=%v", in, got, errs, err)
		}
	}
	invalid := []string{`"abc`, `"\x"`, `"\u12"`, `"\ud800"`, `"\udc00"`, `"\ud800\u0041"`, "\"\xff\"", "\"\x01\"", `"a"x`, `abc`, ``, ` `, `"a" "b"`}
	for _, in := range invalid {
		if _, _, err := ourDecode[string](t, in); err == nil {
			t.Errorf("%q: expected a syntax error", in)
		}
	}
}

func TestStringsAtAllOffsets(t *testing.T) {
	const plain = "abcdefghijklmnopqrstuvwxyz0123456789"
	for _, sp := range []string{`"`, `\`, "\n", "\x01", "\x1f", "\x7f", "/", "<", "é", "🎉", "\xe2\x80\xa8"} {
		for pre := 0; pre <= 20; pre++ {
			for post := 0; post <= 10; post++ {
				s := plain[:pre] + sp + plain[:post]
				enc := string(appendString(nil, s))
				if want := stdEncode(t, s); enc != want {
					t.Fatalf("encode %q: got %s want %s", s, enc, want)
				}
				got, errs, err := ourDecode[[]string](t, "["+enc+`,"x"]`)
				if err != nil || len(errs) > 0 || len(got) != 2 || got[0] != s {
					t.Fatalf("decode %s: got %q %v %v", enc, got, errs, err)
				}
			}
		}
	}
	for _, bad := range []string{"\n", "\x00", "\x1f", "\xff", "\xc3", "\xed\xa0\x80"} {
		for pre := 0; pre <= 20; pre++ {
			data := `["` + plain[:pre] + bad + plain[:3] + `","x"]`
			if _, _, err := ourDecode[[]string](t, data); err == nil {
				t.Fatalf("%q accepted", data)
			}
		}
	}
}

func TestDecodeSyntax(t *testing.T) {
	type s struct {
		A int `json:"a"`
	}
	valid := []string{`{"a":1}`, `{"a":[1,2,{"b":null}],"c":{"d":[[]]}}`, `{"a":-0.5e-3,"b":true,"c":false}`, " \t\n\r{\"a\" :\n0}\n"}
	for _, in := range valid {
		if _, _, err := ourDecode[s](t, in); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
	invalid := []string{`{"a":1,}`, `{"a":1 "b":2}`, `{"a"}`, `{"a":01}`, `{"a":1.}`, `{"a":.5}`, `{"a":1e}`, `{"a":-}`,
		`{"a":tru}`, `{"a":nul}`, `{"a":[1,]}`, `{"a":[1 2]}`, `{a:1}`, `{"a":1}}`, `{"a":1`, `{"a":+1}`, `{"a":NaN}`}
	for _, in := range invalid {
		if _, _, err := ourDecode[s](t, in); err == nil {
			t.Errorf("%s: expected a syntax error", in)
		}
	}
	deep := strings.Repeat("[", maxDepth+1) + strings.Repeat("]", maxDepth+1)
	if _, _, err := ourDecode[s](t, `{"b":`+deep+`}`); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Errorf("expected a depth error, got %v", err)
	}
}

func TestDecodeNullsAndPointers(t *testing.T) {
	type sub struct {
		X int `json:"x"`
	}
	type s struct {
		PS   *string   `json:"ps"`
		PSub *sub      `json:"psub,omitempty"`
		Sl   []*int    `json:"sl"`
		T    time.Time `json:"t,omitzero"`
	}
	got, errs, err := ourDecode[s](t, `{"ps":null,"sl":[1,null]}`)
	if err != nil || len(errs) > 0 {
		t.Fatalf("errs=%v err=%v", errs, err)
	}
	if got.PS != nil || len(got.Sl) != 2 || *got.Sl[0] != 1 || got.Sl[1] != nil {
		t.Errorf("got %+v", got)
	}
	_, errs, _ = ourDecode[s](t, `{"ps":"a","sl":null,"psub":null}`)
	if got, want := describe(errs), []string{"body/sl: type: expected array", "body/psub: type: expected object"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeDefaults(t *testing.T) {
	type s struct {
		Name  string   `json:"name,omitempty" default:"anon"`
		Count int      `json:"count,omitempty" default:"10"`
		Tags  []string `json:"tags,omitempty" default:"a,b"`
		Ptr   *int     `json:"ptr,omitempty" default:"3"`
	}
	got, errs, err := ourDecode[s](t, `{}`)
	if err != nil || len(errs) > 0 {
		t.Fatal(errs, err)
	}
	if got.Name != "anon" || got.Count != 10 || !reflect.DeepEqual(got.Tags, []string{"a", "b"}) || got.Ptr == nil || *got.Ptr != 3 {
		t.Fatalf("got %+v", got)
	}
	got.Tags[0] = "mutated"
	*got.Ptr = 99
	again, _, _ := ourDecode[s](t, `{"count":0}`)
	if again.Tags[0] != "a" || *again.Ptr != 3 || again.Count != 0 {
		t.Errorf("defaults are shared or override explicit values: %+v", again)
	}
}

func TestDecodeMaxErrors(t *testing.T) {
	type s struct {
		Items []int `json:"items" maximum:"0"`
	}
	in := `{"items":[` + strings.TrimSuffix(strings.Repeat("1,", 1000), ",") + `]}`
	_, errs, err := ourDecode[s](t, in)
	if err != nil || len(errs) != maxErrors {
		t.Errorf("got %d errors (%v), want %d", len(errs), err, maxErrors)
	}

	in = `{"` + strings.Repeat("k", 4096) + `":[` + strings.TrimSuffix(strings.Repeat(`"x",`, 50), ",") + `]}`
	_, errs, err = ourDecode[map[string][]int](t, in)
	if err != nil || len(errs) != 4 {
		t.Errorf("got %d errors (%v), want 4: their pointers pass %d bytes", len(errs), err, maxPointerBytes)
	}
}

func TestDecodeRecursiveAndMaps(t *testing.T) {
	type node struct {
		Name     string           `json:"name"`
		Children []*node          `json:"children,omitempty"`
		Attrs    map[string]*node `json:"attrs,omitempty"`
	}
	got, errs, err := ourDecode[node](t, `{"name":"root","children":[{"name":"a","children":[{"name":"b"}]}],"attrs":{"x":{"name":"y"}}}`)
	if err != nil || len(errs) > 0 {
		t.Fatal(errs, err)
	}
	if got.Children[0].Children[0].Name != "b" || got.Attrs["x"].Name != "y" {
		t.Errorf("got %+v", got)
	}
	_, errs, _ = ourDecode[node](t, `{"name":"r","children":[{"children":[{}]}],"attrs":{"a":{"name":"1"},"a":{"name":"2"}}}`)
	want := []string{
		"body/children/0/children/0/name: required: expected required property name to be present",
		"body/children/0/name: required: expected required property name to be present",
		"body/attrs/a: duplicate: duplicate property",
	}
	if got := describe(errs); !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestDecodeTypedValues(t *testing.T) {
	type s struct {
		ID  uuid.UUID `json:"id"`
		At  time.Time `json:"at"`
		Len string    `json:"len" maxLength:"3"`
	}
	for _, u := range []string{
		"f81d4fae-7dec-11d0-a765-00a0c91e6bf6",
		"000003e8-b1d2-21f0-8000-325096b39f47",
		"5df41881-3aed-3515-88a7-2f4a814cf09e",
		"919108f7-52d1-4320-9bac-f847db4148a8",
		"2ed6657d-e927-568b-95e1-2665a8aea6a2",
		"1ec9414c-232a-6b00-b3c8-9e6bdeced846",
		"017f22e2-79b0-7cc3-98c4-dc0c0c07398f",
		"320c3d4d-cc00-875b-8ec9-32d5f69181c0",
		"00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		"F81D4FAE-7DEC-11D0-A765-00A0C91E6BF6",
	} {
		v, errs, err := ourDecode[s](t, `{"id":"`+u+`","at":"2026-11-08T10:00:00Z","len":"ééé"}`)
		if err != nil || len(errs) > 0 || v.ID != uuid.MustParse(u) {
			t.Errorf("%s: %v %v %v", u, v.ID, errs, err)
		}
	}
	for _, u := range []string{"{f81d4fae-7dec-11d0-a765-00a0c91e6bf6}", "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6", "f81d4fae7dec11d0a76500a0c91e6bf6"} {
		if _, errs, _ := ourDecode[s](t, `{"id":"`+u+`","at":"2026-11-08T10:00:00Z","len":""}`); len(errs) != 1 || errs[0].Code != "format" {
			t.Errorf("%s: %v, want a format error: only the hyphenated form is documented", u, errs)
		}
	}
	_, errs, _ := ourDecode[s](t, `{"id":"f81d4fae-7dec-11d0-a765-00a0c91e6bfg","at":"2026-11-08","len":"éééé"}`)
	if len(errs) != 3 || errs[0].Code != "format" || errs[1].Code != "format" || errs[2].Code != "maxLength" {
		t.Errorf("expected format, format and maxLength errors, got %v", errs)
	}
}

func TestErrorPointers(t *testing.T) {
	type sub struct {
		N int `json:"n" maximum:"1"`
	}
	type s struct {
		M map[string]sub `json:"m"`
		L []sub          `json:"l"`
	}
	_, errs, _ := ourDecode[s](t, `{"m":{"a/b":{"n":2},"c~d":{"n":2}},"l":[{"n":1},{"n":2}]}`)
	want := []string{"body/m/a~1b/n: maximum: expected number <= 1", "body/m/c~0d/n: maximum: expected number <= 1", "body/l/1/n: maximum: expected number <= 1"}
	if got := describe(errs); !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	_, errs, _ = ourDecode[s](t, `[]`)
	if got := describe(errs); !slices.Equal(got, []string{"body: type: expected object"}) {
		t.Errorf("whole body error: %q", got)
	}
}

func TestDecodePropertyOrder(t *testing.T) {
	type s struct {
		A int `json:"a"`
		B int `json:"b"`
		C int `json:"c"`
	}
	for _, data := range []string{`{"a":1,"b":2,"c":3}`, `{"c":3,"b":2,"a":1}`, `{"b":2,"c":3,"a":1}`} {
		if got, errs, err := ourDecode[s](t, data); err != nil || len(errs) > 0 || got != (s{1, 2, 3}) {
			t.Errorf("%s: %+v %v %v", data, got, errs, err)
		}
	}
	_, errs, _ := ourDecode[s](t, `{"a":1,"b":2,"x":0,"c":3,"b":4}`)
	if got, want := describe(errs), []string{"body/x: additionalProperties: unexpected property", "body/b: duplicate: duplicate property"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeKeyFastPath(t *testing.T) {
	type inner struct {
		X int `json:"x"`
	}
	type target struct {
		A     int    `json:"a"`
		AB    string `json:"ab"`
		Quote string `json:"q\"uote"`
		Acc   int    `json:"é"`
		Space string `json:"with space"`
		In    inner  `json:"in"`
		RO    int    `json:"ro"`
		Req   int    `json:"req"`
		Def   string `json:"def" default:"d"`
	}
	typ := reflect.TypeFor[target]()
	dec := decoderFor(typ)
	sd := structDecs[typ]
	keys := make([]string, len(sd.fields))
	for i, f := range sd.fields {
		keys[i] = f.key
	}
	if want := []string{`"a":`, `"ab":`, "", "", `"with space":`, `"in":`, `"ro":`, `"req":`, `"def":`}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys %q, want %q", keys, want)
	}
	setKeys := func(on bool) {
		for i := range sd.fields {
			sd.fields[i].key = ""
			if on {
				sd.fields[i].key = keys[i]
			}
		}
	}
	defer setKeys(true)

	type result struct {
		v    target
		errs []ErrorDetail
		err  string
	}
	run := func(data string) (r result) {
		d := &decodeState{}
		if err := d.decode(dec, []byte(data), reflect.ValueOf(&r.v).Elem(), "body"); err != nil {
			r.err = err.Error()
		}
		r.errs = d.errs
		return r
	}

	names := []string{`"a"`, `"ab"`, `"q\"uote"`, `"é"`, `"with space"`, `"in"`, `"ro"`, `"req"`, `"def"`}
	values := []string{`1`, `"v"`, `"w"`, `2`, `"s"`, `{"x":3}`, `4`, `5`, `"e"`}
	colons := []string{":", ":", ":", ":", " :", ": ", ":\n", "\t:", ":  "}
	seps := []string{",", ",", ",", ", ", ",\n"}
	const garbage = "\"\\:,{}[] \x00\xff\x7fa"
	r := rand.New(rand.NewPCG(11, 12))
	spell := func(name string) string {
		switch r.IntN(14) {
		case 0:
			return `"\u00` + fmt.Sprintf("%02x", name[1]) + name[2:]
		case 1:
			return strings.ToUpper(name)
		case 2:
			return name[:len(name)-1] + `x"`
		case 3:
			return name[:len(name)-2] + `"`
		case 4:
			return `"zz"`
		}
		return name
	}
	for n := range 40_000 {
		var b strings.Builder
		b.WriteString([]string{"{", "{", "{ ", "\n{\n"}[r.IntN(4)])
		var order []int
		for i := range names {
			if r.IntN(10) > 0 {
				order = append(order, i)
			}
		}
		if r.IntN(4) == 0 {
			r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		} else if r.IntN(3) == 0 && len(order) > 0 {
			i, j := r.IntN(len(order)), r.IntN(len(order))
			order[i], order[j] = order[j], order[i]
		}
		if r.IntN(6) == 0 && len(order) > 0 {
			order = append(order, order[r.IntN(len(order))])
		}
		for k, i := range order {
			if k > 0 {
				b.WriteString(seps[r.IntN(len(seps))])
			}
			name := names[i]
			if n%3 != 0 {
				name = spell(name)
			}
			if r.IntN(8) == 0 {
				b.WriteString(`"zz":null,`)
			}
			b.WriteString(name + colons[r.IntN(len(colons))] + values[i])
		}
		b.WriteString([]string{"}", "}", " }", "\n}\n", ""}[r.IntN(5)])
		text := []byte(b.String())
		for range r.IntN(3) / 2 {
			text[r.IntN(len(text))] = garbage[r.IntN(len(garbage))]
		}
		if r.IntN(10) == 0 {
			text = text[:r.IntN(len(text)+1)]
		}

		setKeys(true)
		fast := run(string(text))
		setKeys(false)
		slow := run(string(text))
		if !reflect.DeepEqual(fast, slow) {
			t.Fatalf("%q\n fast path: %+v\n general:   %+v", text, fast, slow)
		}
	}
}

func TestDecodeLongSlices(t *testing.T) {
	type elem struct {
		S string   `json:"s"`
		P *int     `json:"p"`
		L []string `json:"l"`
	}
	var b strings.Builder
	b.WriteByte('[')
	for i := range 1000 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"s":"item %d","p":%d,"l":["%d","x"]}`, i, i, i)
	}
	b.WriteByte(']')
	got, errs, err := ourDecode[[]elem](t, b.String())
	runtime.GC()
	var want []elem
	if err := json.Unmarshal([]byte(b.String()), &want); err != nil {
		t.Fatal(err)
	}
	if err != nil || len(errs) > 0 || !reflect.DeepEqual(got, want) {
		t.Errorf("decoded differently from encoding/json (%v %v)", errs, err)
	}
}

func TestMaps(t *testing.T) {
	type item struct {
		Name string `json:"name,omitempty"`
		N    int    `json:"n,omitempty"`
	}
	type node struct {
		Items map[string]item `json:"items"`
		Kids  map[string]node `json:"kids,omitempty"`
	}
	big := map[string]item{}
	for i := range maxPooledMapItems + 10 {
		big[fmt.Sprint(i)] = item{N: i}
	}
	for i, v := range []node{
		{Items: map[string]item{"b": {Name: "y"}, "a": {Name: "x", N: 1}}, Kids: map[string]node{
			"k": {Items: map[string]item{"c": {}}},
			"j": {Items: map[string]item{}, Kids: map[string]node{"deep": {Items: map[string]item{"z": {N: 2}, "y": {}}}}},
		}},
		{Items: map[string]item{"c": {}}},
		{Items: big},
		{Items: map[string]item{"d": {N: 4}}},
	} {
		text, err := ourEncode(t, v)
		if want := stdEncode(t, v); err != nil || text != want {
			t.Fatalf("case %d: encoded %.80s %v, want %.80s", i, text, err, want)
		}
		got, errs, err := ourDecode[node](t, text)
		if err != nil || len(errs) > 0 || !reflect.DeepEqual(got, v) {
			t.Fatalf("case %d: decoded %.80v %v %v", i, got, errs, err)
		}
	}
	if got, err := ourEncode(t, map[string]map[string]int{"x": nil}); err != nil || got != `{"x":{}}` {
		t.Errorf("nil map encoded as %s %v", got, err)
	}
	existing := map[string]item{"old": {N: 1}}
	d := &decodeState{}
	err := d.decode(decoderFor(reflect.TypeFor[map[string]item]()), []byte(`{"new":{"n":2},"empty":{}}`), reflect.ValueOf(&existing).Elem(), "body")
	if want := (map[string]item{"old": {N: 1}, "new": {N: 2}, "empty": {}}); err != nil || len(d.errs) > 0 || !reflect.DeepEqual(existing, want) {
		t.Errorf("decoded into an existing map: %v %v %v", existing, d.errs, err)
	}
}

func TestMapPoolsDoNotRetain(t *testing.T) {
	type leaf struct {
		S string `json:"s"`
	}
	var enc, dec weak.Pointer[leaf]
	func() {
		l := &leaf{S: "x"}
		enc = weak.Make(l)
		m := map[string]*leaf{"a": l, "b": {S: "y"}}
		var e encodeState
		if err := e.encode(compileEncoder(reflect.TypeFor[map[string]*leaf]()), reflect.ValueOf(&m).Elem()); err != nil || string(e.buf) != `{"a":{"s":"x"},"b":{"s":"y"}}` {
			t.Fatal(string(e.buf), err)
		}
		e.buf = nil
	}()
	func() {
		m, errs, err := ourDecode[map[string]*leaf](t, `{"a":{"s":"x"},"b":{"s":"y"}}`)
		if err != nil || len(errs) > 0 || len(m) != 2 {
			t.Fatal(m, errs, err)
		}
		dec = weak.Make(m["b"])
	}()
	runtime.GC()
	if enc.Value() != nil {
		t.Error("the encoder's pooled item keeps the map alive")
	}
	if dec.Value() != nil {
		t.Error("the decoder's pooled item keeps a decoded value alive")
	}
}

func BenchmarkHugeNumbers(b *testing.B) {
	type s struct {
		F float64 `json:"f" multipleOf:"0.01"`
		I int64   `json:"i" multipleOf:"7"`
	}
	digits := strings.Repeat("7", 1<<20-100)
	for name, body := range map[string]string{
		"float":    `{"f":1.` + digits + `,"i":7}`,
		"int":      `{"f":1,"i":` + digits + `}`,
		"exponent": `{"f":1e999999999,"i":7e999999999}`,
	} {
		b.Run(name, func(b *testing.B) {
			dec := decoderFor(reflect.TypeFor[s]())
			for b.Loop() {
				var v s
				d := &decodeState{}
				_ = d.decode(dec, []byte(body), reflect.ValueOf(&v).Elem(), "body")
			}
		})
	}
}

type textType struct{ v string }

func (x textType) MarshalText() ([]byte, error) { return []byte("T:" + x.v), nil }

func (x *textType) UnmarshalText(b []byte) error {
	x.v = strings.TrimPrefix(string(b), "T:")
	return nil
}

type Embedded struct {
	E1 string `json:"e1"`
	E2 int
}

const complexReq = `{"string": "Benchmark", "int": 100, "float": 0.1, "bool": true, "time": "2023-01-01T00:00:00Z", "slice": ["a"], "sub": {"id": 1, "name": "SubItem"}, "sub_slice": [{"id": 1, "name": "Item"}], "map": {"k": {"id": 1, "name": "val"}}}`
