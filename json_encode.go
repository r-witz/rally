package rally

import (
	"encoding"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type encFunc func(e *encodeState, v reflect.Value)

type encodeState struct {
	buf   []byte
	depth int
}

type encodeError struct{ err error }

const maxEncodeDepth = 2*maxDepth + 1

func (e *encodeState) fail(err error) { panic(encodeError{err}) }

func (e *encodeState) encode(enc encFunc, v reflect.Value) (err error) {
	defer func() {
		if r := recover(); r != nil {
			ee, ok := r.(encodeError)
			if !ok {
				panic(r)
			}
			err = ee.err
		}
	}()
	enc(e, v)
	return nil
}

func (e *encodeState) enter() {
	e.depth++
	if e.depth > maxEncodeDepth {
		e.fail(errors.New("json: maximum encoding depth exceeded (cyclic value?)"))
	}
}

var structEncs = map[reflect.Type]*structEnc{}

func compileEncoder(t reflect.Type) encFunc {
	switch kindOf(t) {
	case kindText:
		return textEncoder(t)
	case kindString:
		return encodeString
	case kindBool:
		return func(e *encodeState, v reflect.Value) { e.buf = strconv.AppendBool(e.buf, v.Bool()) }
	case kindInt:
		return func(e *encodeState, v reflect.Value) { e.buf = strconv.AppendInt(e.buf, v.Int(), 10) }
	case kindUint:
		return func(e *encodeState, v reflect.Value) { e.buf = strconv.AppendUint(e.buf, v.Uint(), 10) }
	case kindFloat:
		bits := t.Bits()
		return func(e *encodeState, v reflect.Value) { e.appendFloat(v.Float(), bits) }
	case kindPointer:
		return pointerEncoder(compileEncoder(t.Elem()))
	case kindSlice:
		return sliceEncoder(compileEncoder(t.Elem()))
	case kindMap:
		return mapEncoder(t, compileEncoder(t.Elem()))
	}
	return cached(structEncs, t, (*structEnc).build).encode
}

func encodeString(e *encodeState, v reflect.Value) { e.buf = appendString(e.buf, v.String()) }

func (e *encodeState) appendFloat(f float64, bits int) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		e.fail(fmt.Errorf("json: unsupported value: %s", strconv.FormatFloat(f, 'g', -1, bits)))
	}
	format := byte('f')
	if abs := math.Abs(f); abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) || bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	b := strconv.AppendFloat(e.buf, f, format, -1, bits)
	if format == 'e' {
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	e.buf = b
}

const hexDigits = "0123456789abcdef"

func appendString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	i := 0
	for len(s)-i >= 8 {
		if m := special8(load64(s[i:])); m != 0 {
			i += bits.TrailingZeros64(m) / 8
			break
		}
		i += 8
	}
	start := 0
	for i < len(s) {
		if b := s[i]; b < utf8.RuneSelf {
			if strSafe[b] {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '"', '\\':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, string(utf8.RuneError)...)
			i += size
			start = i
			continue
		}
		if r == 0x2028 || r == 0x2029 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

func load64(s string) uint64 {
	_ = s[7]
	return uint64(s[0]) | uint64(s[1])<<8 | uint64(s[2])<<16 | uint64(s[3])<<24 |
		uint64(s[4])<<32 | uint64(s[5])<<40 | uint64(s[6])<<48 | uint64(s[7])<<56
}

var textAppenderType = reflect.TypeFor[encoding.TextAppender]()

func textEncoder(t reflect.Type) encFunc {
	pt := reflect.PointerTo(t)
	switch {
	case t == timeType:
		return encodeTime
	case pt.Implements(textAppenderType):
		return func(e *encodeState, v reflect.Value) {
			start := len(e.buf)
			b, err := v.Addr().Interface().(encoding.TextAppender).AppendText(append(e.buf, '"'))
			if err != nil {
				e.fail(err)
			}
			e.buf = closeText(b, start)
		}
	case pt.Implements(textMarshalerType):
		return func(e *encodeState, v reflect.Value) {
			b, err := v.Addr().Interface().(encoding.TextMarshaler).MarshalText()
			if err != nil {
				e.fail(err)
			}
			start := len(e.buf)
			e.buf = closeText(append(append(e.buf, '"'), b...), start)
		}
	case t.Kind() == reflect.String:
		return encodeString
	}
	panic(fmt.Errorf("rally: %s has an UnmarshalText method but no MarshalText: it cannot be written in responses", t))
}

func closeText(b []byte, start int) []byte {
	for _, c := range b[start+1:] {
		if !strSafe[c] {
			return appendString(b[:start], string(b[start+1:]))
		}
	}
	return append(b, '"')
}

func encodeTime(e *encodeState, v reflect.Value) {
	t, _ := reflect.TypeAssert[time.Time](v)
	if _, off := t.Zone(); off%60 != 0 {
		t = t.UTC()
	}
	b, err := t.AppendText(append(e.buf, '"'))
	if err != nil {
		e.fail(err)
	}
	e.buf = append(b, '"')
}

func pointerEncoder(elem encFunc) encFunc {
	return func(e *encodeState, v reflect.Value) {
		if v.IsNil() {
			e.buf = append(e.buf, "null"...)
			return
		}
		e.enter()
		elem(e, v.Elem())
		e.depth--
	}
}

func sliceEncoder(elem encFunc) encFunc {
	return func(e *encodeState, v reflect.Value) {
		e.enter()
		e.buf = append(e.buf, '[')
		for i := range v.Len() {
			if i > 0 {
				e.buf = append(e.buf, ',')
			}
			elem(e, v.Index(i))
		}
		e.buf = append(e.buf, ']')
		e.depth--
	}
}

type mapItems struct {
	iter  reflect.MapIter
	key   reflect.Value
	keys  []string
	order []int
	vals  reflect.Value
}

const maxPooledMapItems = 1024

func mapEncoder(t reflect.Type, elem encFunc) encFunc {
	pool := sync.Pool{New: func() any {
		return &mapItems{key: reflect.New(t.Key()).Elem(), vals: reflect.New(reflect.SliceOf(t.Elem())).Elem()}
	}}
	return func(e *encodeState, v reflect.Value) {
		n := v.Len()
		if n == 0 {
			e.buf = append(e.buf, '{', '}')
			return
		}
		e.enter()
		m := pool.Get().(*mapItems)
		m.keys = slices.Grow(m.keys[:0], n)[:n]
		m.order = slices.Grow(m.order[:0], n)[:n]
		m.vals.Grow(n)
		m.vals.SetLen(n)
		m.iter.Reset(v)
		for i := 0; m.iter.Next(); i++ {
			m.key.SetIterKey(&m.iter)
			m.keys[i] = m.key.String()
			m.vals.Index(i).SetIterValue(&m.iter)
			m.order[i] = i
		}
		slices.SortFunc(m.order, func(a, b int) int { return strings.Compare(m.keys[a], m.keys[b]) })
		e.buf = append(e.buf, '{')
		for j, i := range m.order {
			if j > 0 {
				e.buf = append(e.buf, ',')
			}
			e.buf = appendString(e.buf, m.keys[i])
			e.buf = append(e.buf, ':')
			elem(e, m.vals.Index(i))
		}
		e.buf = append(e.buf, '}')
		e.depth--
		if n <= maxPooledMapItems {
			m.iter.Reset(reflect.Value{})
			m.key.SetZero()
			clear(m.keys)
			m.vals.Clear()
			m.vals.SetLen(0)
			pool.Put(m)
		}
	}
}

type structEnc struct {
	fields []encField
}

type encField struct {
	key   string
	index []int
	enc   encFunc
	omit  func(v reflect.Value) bool
}

func (se *structEnc) build(t reflect.Type) {
	for _, f := range fieldsOf(t) {
		se.fields = append(se.fields, encField{
			key:   jsonQuote(f.name) + ":",
			index: f.index,
			enc:   enumEncoder(f.typ, enumOnly(f.rules), enumOnly(f.items), t.String()+"."+f.goName),
			omit:  omitFunc(f),
		})
	}
}

func enumEncoder(t reflect.Type, value, items *rules, where string) encFunc {
	switch {
	case items != nil && kindOf(t) == kindPointer:
		return pointerEncoder(enumEncoder(t.Elem(), nil, items, where))
	case items != nil:
		return sliceEncoder(enumEncoder(t.Elem(), items, nil, where+" item"))
	case value == nil:
		return compileEncoder(t)
	}
	enc := compileEncoder(t)
	quoted := make([]string, len(value.enum.strs))
	for i, s := range value.enum.strs {
		quoted[i] = jsonQuote(s)
	}
	allowed := func(b []byte) bool {
		if string(b) == "null" {
			return true
		}
		for _, q := range quoted {
			if string(b) == q {
				return true
			}
		}
		d, ok := parseDecimal(string(b))
		return ok && slices.ContainsFunc(value.enum.nums, func(n bound) bool { return n.dec.cmp(d) == 0 })
	}
	return func(e *encodeState, v reflect.Value) {
		start := len(e.buf)
		enc(e, v)
		if !allowed(e.buf[start:]) {
			e.fail(fmt.Errorf("%s is %s, which is not one of its enum values", where, e.buf[start:]))
		}
	}
}

func (s *structEnc) encode(e *encodeState, v reflect.Value) {
	e.buf = append(e.buf, '{')
	first := true
	for i := range s.fields {
		f := &s.fields[i]
		fv := fieldByIndex(v, f.index)
		if f.omit != nil && f.omit(fv) {
			continue
		}
		if !first {
			e.buf = append(e.buf, ',')
		}
		first = false
		e.buf = append(e.buf, f.key...)
		f.enc(e, fv)
	}
	e.buf = append(e.buf, '}')
}

func omitFunc(f *field) func(v reflect.Value) bool {
	var empty, zero func(reflect.Value) bool
	if f.omitEmpty {
		switch k := f.typ.Kind(); {
		case k == reflect.String || k == reflect.Slice || k == reflect.Map:
			empty = func(v reflect.Value) bool { return v.Len() == 0 }
		case k >= reflect.Bool && k <= reflect.Float64 || k == reflect.Pointer:
			empty = reflect.Value.IsZero
		}
	}
	if f.omitZero {
		zero = isZeroFunc(f.typ)
	}
	switch {
	case empty != nil && zero != nil:
		return func(v reflect.Value) bool { return empty(v) || zero(v) }
	case empty != nil:
		return empty
	}
	return zero
}

type isZeroer interface{ IsZero() bool }

var isZeroerType = reflect.TypeFor[isZeroer]()

func isZeroFunc(t reflect.Type) func(reflect.Value) bool {
	switch {
	case t.Kind() == reflect.Pointer && t.Implements(isZeroerType):
		return func(v reflect.Value) bool { return v.IsNil() || v.Interface().(isZeroer).IsZero() }
	case t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(isZeroerType):
		return func(v reflect.Value) bool { return v.Addr().Interface().(isZeroer).IsZero() }
	}
	return reflect.Value.IsZero
}
