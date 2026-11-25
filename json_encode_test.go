package rally

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type code struct{ s string }

func (c *code) UnmarshalText(b []byte) error {
	if string(b) == "panic" {
		panic("boom")
	}
	c.s = string(b)
	return nil
}

func (c code) MarshalText() ([]byte, error) {
	if c.s == "bad" {
		return nil, errors.New("bad code")
	}
	return []byte(c.s), nil
}

type appendCode struct{ s string }

func (c *appendCode) UnmarshalText(b []byte) error { c.s = string(b); return nil }

func (c appendCode) AppendText(b []byte) ([]byte, error) {
	if c.s == "bad" {
		return nil, errors.New("bad code")
	}
	return append(b, c.s...), nil
}

func (c appendCode) MarshalText() ([]byte, error) { return c.AppendText(nil) }

type zeroer struct{ N int }

func (z *zeroer) IsZero() bool { return z.N == 0 }

func TestEncodeEdges(t *testing.T) {
	type cyclic struct {
		Next *cyclic `json:"next"`
	}
	c := &cyclic{}
	c.Next = c
	if _, err := ourEncode(t, c); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Errorf("cyclic value: %v", err)
	}
	for _, v := range []any{code{"bad"}, appendCode{"bad"}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := ourEncode(t, v); err == nil {
			t.Errorf("%v: no error", v)
		}
	}
	type both struct {
		A []int     `json:"a,omitempty,omitzero"`
		Z *zeroer   `json:"z,omitzero"`
		U upperText `json:"u"`
	}
	for _, c := range []struct {
		v    both
		want string
	}{
		{both{}, `{"u":""}`},
		{both{A: []int{}, Z: &zeroer{}}, `{"u":""}`},
		{both{A: []int{1}, Z: &zeroer{2}, U: "x"}, `{"a":[1],"z":{"N":2},"u":"X"}`},
	} {
		if got, err := ourEncode(t, c.v); err != nil || got != c.want {
			t.Errorf("%+v: got %s, want %s (%v)", c.v, got, c.want, err)
		}
	}
	if got, err := ourEncode(t, time.Date(2020, 1, 1, 0, 0, 0, 0, time.FixedZone("LMT", 61))); err != nil || got != `"2019-12-31T23:58:59Z"` {
		t.Errorf("offset with seconds: %s %v", got, err)
	}
}

type lowerText string

func (l *lowerText) UnmarshalText(b []byte) error {
	*l = lowerText(strings.ToLower(string(b)))
	return nil
}

type panicText struct{}

func (panicText) MarshalText() ([]byte, error) { panic("boom") }

func (*panicText) UnmarshalText([]byte) error { return nil }

func TestMoreEdges(t *testing.T) {
	if got, err := ourEncode(t, lowerText("A")); err != nil || got != `"A"` {
		t.Errorf("string type with UnmarshalText only: %s %v", got, err)
	}
	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Errorf("a panic in MarshalText must propagate, got %v", r)
			}
		}()
		_, _ = ourEncode(t, panicText{})
	}()
	type inner struct {
		A int `json:"a"`
	}
	type outer struct {
		inner
		A int `json:"a"`
	}
	if v, errs, err := ourDecode[outer](t, `{"a":2}`); err != nil || len(errs) > 0 || v.A != 2 || v.inner.A != 0 {
		t.Errorf("shallower field wins: %+v %v %v", v, errs, err)
	}
	for _, tag := range []string{`json:"f" bad`, `json:"f" x:"abc`, `json:"f" maximum12:"1"`} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: %v", tag, r)
				}
			}()
			checkTags("T.F", reflect.StructTag(tag), fieldTags)
		}()
	}
	mux, api := newAPI()
	Register(api, Operation{Method: http.MethodGet, Path: "/x", OperationID: "get-x"}, func(ctx context.Context, in *struct {
		hidden int
		N      int `query:"n"`
	}) (*struct{}, error) {
		return nil, nil
	})
	if w := do(mux, http.MethodGet, "/x?n=1", ""); w.Code != http.StatusNoContent {
		t.Errorf("unexported field: %d", w.Code)
	}
	type withDefault struct {
		S string `json:"s" default:"x"`
	}
	registerBody[withDefault](api)
	api.registry.schemas["withDefault"].Properties["s"].Default = code{"bad"}
	if _, err := api.OpenAPIJSON(); err == nil {
		t.Error("OpenAPIJSON: no error for a property")
	}
}

func stdEncode(t testing.TB, v any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encoding/json: %v", err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func ourEncode(t testing.TB, v any) (string, error) {
	t.Helper()
	rv := reflect.ValueOf(v)
	p := reflect.New(rv.Type())
	p.Elem().Set(rv)
	var e encodeState
	err := e.encode(compileEncoder(rv.Type()), p.Elem())
	return string(e.buf), err
}

func TestEncodeMatchesEncodingJSON(t *testing.T) {
	v := sampleKitchen()
	got, err := ourEncode(t, v)
	if err != nil {
		t.Fatal(err)
	}
	if want := stdEncode(t, v); got != want {
		t.Errorf("mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestEncodeFloatsMatch(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	values := []float64{0, 1, -1, 0.1, 1e-6, 1e-7, 1e20, 1e21, 123456789.125, math.MaxFloat64, math.SmallestNonzeroFloat64, 5e-324, -1e-9}
	for range 20000 {
		values = append(values, math.Float64frombits(r.Uint64()))
	}
	for _, f := range values {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		for _, v := range []any{f, float32(f)} {
			if f32, ok := v.(float32); ok && (math.IsInf(float64(f32), 0)) {
				continue
			}
			got, err := ourEncode(t, v)
			if err != nil {
				t.Fatal(err)
			}
			if want := stdEncode(t, v); got != want {
				t.Fatalf("%T %v: got %s want %s", v, v, got, want)
			}
		}
	}
	if _, err := ourEncode(t, math.NaN()); err == nil {
		t.Error("NaN must not encode")
	}
}

func TestEncodeStringsMatch(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	alphabet := []string{"a", "é", "\"", "\\", "/", "<", ">", "&", "\n", "\r", "\t", "\b", "\f", "\x00", "\x1f", "\x7f", "\u2028", "\u2029", "🎉", "\xff", "\xc3", "\xed\xa0\x80", "\u00a0"}
	for range 20000 {
		var b strings.Builder
		for range r.IntN(12) {
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		s := b.String()
		got, err := ourEncode(t, s)
		if err != nil {
			t.Fatal(err)
		}
		if want := stdEncode(t, replaceInvalidUTF8(s)); got != want {
			t.Fatalf("%q: got %s want %s", s, got, want)
		}
	}
}

func replaceInvalidUTF8(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
	}
	return b.String()
}

type appendText struct{ s string }

func (a appendText) AppendText(b []byte) ([]byte, error) { return append(b, a.s...), nil }

func (a appendText) MarshalText() ([]byte, error) { return []byte(a.s), nil }

func TestEncodeAppendText(t *testing.T) {
	for _, s := range []string{"plain", "", `q"uote`, "back\\slash", "é", "new\nline", "\xff"} {
		got, err := ourEncode(t, struct {
			V appendText `json:"v"`
		}{appendText{s}})
		if want := `{"v":` + stdEncode(t, replaceInvalidUTF8(s)) + `}`; err != nil || got != want {
			t.Errorf("%q: got %s %v, want %s", s, got, err, want)
		}
	}
}
