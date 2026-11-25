package rally

import (
	"cmp"
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/r-witz/rally/internal/pattern"
)

type bound struct {
	text string
	dec  decimal
	f    float64
}

type rules struct {
	minimum, maximum                   *bound
	exclusiveMinimum, exclusiveMaximum *bound
	multipleOf                         *bound

	minLength, maxLength *int
	pattern              string
	compiled             *pattern.Pattern

	enum enumRule

	minItems, maxItems           *int
	unique                       func(v reflect.Value) bool
	minProperties, maxProperties *int

	msgMinimum, msgMaximum, msgExclusiveMinimum, msgExclusiveMaximum, msgMultipleOf string
	msgMinLength, msgMaxLength, msgPattern, msgMinItems, msgMaxItems                string
	msgMinProperties, msgMaxProperties                                              string
}

type enumRule struct {
	values []any
	strs   []string
	nums   []bound
	msg    string
}

const msgUniqueItems = "expected array items to be unique"

func stringRules(r *rules) *rules {
	if r != nil && (r.minLength != nil || r.maxLength != nil || r.compiled != nil || r.enum.strs != nil) {
		return r
	}
	return nil
}

func numberRules(r *rules) *rules {
	if r != nil && (r.minimum != nil || r.maximum != nil || r.exclusiveMinimum != nil || r.exclusiveMaximum != nil || r.multipleOf != nil || r.enum.nums != nil) {
		return r
	}
	return nil
}

func sizeBounded(r *rules) bool { return r != nil && (r.maxItems != nil || r.maxProperties != nil) }

func newBound(s string) (bound, bool) {
	d, ok := parseDecimal(s)
	if !ok {
		return bound{}, false
	}
	f, _ := strconv.ParseFloat(s, 64)
	return bound{text: s, dec: d, f: f}, true
}

func parseRules(where string, tag reflect.StructTag, t reflect.Type, allowed []string) (value, items *rules) {
	checkTags(where, tag, allowed)
	base := deref(t)
	isSlice := kindOf(base) == kindSlice
	isMap := kindOf(base) == kindMap
	scalar := base
	if isSlice {
		scalar = deref(base.Elem())
	}
	k := kindOf(scalar)
	isNum := k == kindInt || k == kindUint || k == kindFloat
	isStr := k == kindString || k == kindText && scalar != timeType && scalar != uuidType

	bad := func(name string) {
		panic(fmt.Errorf("rally: tag %s is not valid on %s of type %s", name, where, t))
	}
	numberTag := func(name string) *bound {
		v, ok := tag.Lookup(name)
		if !ok {
			return nil
		}
		if !isNum {
			bad(name)
		}
		b, ok := newBound(v)
		if !ok {
			panic(fmt.Errorf("rally: invalid %s tag %q on %s: expected a JSON number", name, v, where))
		}
		return &b
	}
	intTag := func(name string, valid bool) *int {
		v, ok := tag.Lookup(name)
		if !ok {
			return nil
		}
		if !valid {
			bad(name)
		}
		d, ok := parseDecimal(v)
		mag, fits, frac := d.magnitude()
		if !ok || d.neg || frac || !fits || mag > math.MaxInt32 {
			panic(fmt.Errorf("rally: invalid %s tag %q on %s: expected a non-negative integer", name, v, where))
		}
		n := int(mag)
		return &n
	}

	s := &rules{}
	s.minimum = numberTag("minimum")
	s.maximum = numberTag("maximum")
	s.exclusiveMinimum = numberTag("exclusiveMinimum")
	s.exclusiveMaximum = numberTag("exclusiveMaximum")
	if s.multipleOf = numberTag("multipleOf"); s.multipleOf != nil && s.multipleOf.dec.sign() <= 0 {
		panic(fmt.Errorf("rally: invalid multipleOf tag %q on %s: expected a number greater than 0", s.multipleOf.text, where))
	}
	s.minLength = intTag("minLength", isStr)
	s.maxLength = intTag("maxLength", isStr)
	if p, ok := tag.Lookup("pattern"); ok {
		if !isStr {
			bad("pattern")
		}
		compiled, err := pattern.Compile(p)
		if err != nil {
			panic(fmt.Errorf("rally: invalid pattern %q on %s: %v", p, where, err))
		}
		s.pattern, s.compiled = p, compiled
		s.msgPattern = "expected string to match pattern " + p
	}
	if e, ok := tag.Lookup("enum"); ok {
		for v := range strings.SplitSeq(e, ",") {
			switch {
			case isStr:
				s.enum.strs = append(s.enum.strs, v)
				s.enum.values = append(s.enum.values, v)
			case isNum:
				b, ok := newBound(v)
				if !ok {
					panic(fmt.Errorf("rally: invalid enum value %q on %s: expected a JSON number", v, where))
				}
				s.enum.nums = append(s.enum.nums, b)
				s.enum.values = append(s.enum.values, json.Number(v))
			default:
				bad("enum")
			}
		}
	}

	a := &rules{}
	a.minItems = intTag("minItems", isSlice)
	a.maxItems = intTag("maxItems", isSlice)
	a.minProperties = intTag("minProperties", isMap)
	a.maxProperties = intTag("maxProperties", isMap)
	if tag.Get("uniqueItems") == "true" {
		if !isSlice || base.Elem() != scalar {
			bad("uniqueItems")
		}
		if a.unique = uniqueChecker(scalar); a.unique == nil {
			panic(fmt.Errorf("rally: uniqueItems is only supported on slices of strings, numbers and booleans (%s)", where))
		}
	}

	s.precompute()
	a.precompute()
	checkSatisfiable(where, scalar, s, a)
	if isSlice {
		return a.orNil(), s.orNil()
	}
	if isMap {
		return a.orNil(), nil
	}
	return s.orNil(), nil
}

func typeRange(t reflect.Type) (lo, hi decimal) {
	if k := kindOf(t); k == kindInt || k == kindUint {
		lo, hi := intBounds(t)
		return decimalOf(lo), decimalOf(hi)
	}
	m := math.MaxFloat64
	if t.Bits() == 32 {
		m = math.MaxFloat32
	}
	return decimalOf(strconv.FormatFloat(-m, 'g', -1, 64)), decimalOf(strconv.FormatFloat(m, 'g', -1, 64))
}

func intBounds(t reflect.Type) (lo, hi string) {
	if kindOf(t) == kindUint {
		return "0", strconv.FormatUint(1<<(t.Bits()-1)<<1-1, 10)
	}
	return strconv.FormatInt(-1<<(t.Bits()-1), 10), strconv.FormatInt(1<<(t.Bits()-1)-1, 10)
}

func checkSatisfiable(where string, scalar reflect.Type, s, a *rules) {
	k := kindOf(scalar)
	fail := func() {
		panic(fmt.Errorf("rally: the rules of %s contradict each other or its type: no value can pass them", where))
	}
	if s.minLength != nil && s.maxLength != nil && *s.minLength > *s.maxLength ||
		a.minItems != nil && a.maxItems != nil && *a.minItems > *a.maxItems ||
		a.minProperties != nil && a.maxProperties != nil && *a.minProperties > *a.maxProperties {
		fail()
	}
	isNum := k == kindInt || k == kindUint || k == kindFloat
	var tlo, thi decimal
	if isNum {
		tlo, thi = typeRange(scalar)
		lo, hi := tlo, thi
		if s.minimum != nil && s.minimum.dec.cmp(lo) > 0 {
			lo = s.minimum.dec
		}
		if s.maximum != nil && s.maximum.dec.cmp(hi) < 0 {
			hi = s.maximum.dec
		}
		loOpen, hiOpen := false, false
		if b := s.exclusiveMinimum; b != nil && b.dec.cmp(lo) >= 0 {
			lo, loOpen = b.dec, true
		}
		if b := s.exclusiveMaximum; b != nil && b.dec.cmp(hi) <= 0 {
			hi, hiOpen = b.dec, true
		}
		if c := lo.cmp(hi); c > 0 || c == 0 && (loOpen || hiOpen) {
			fail()
		}
		if k == kindInt && newIntSetter[int64](scalar, s).empty || k == kindUint && newIntSetter[uint64](scalar, s).empty {
			fail()
		}
	}
	for _, e := range s.enum.nums {
		d := &decodeState{}
		s.checkFloat(d, e.f, []byte(e.text))
		if len(d.errs) > 0 || e.dec.cmp(tlo) < 0 || e.dec.cmp(thi) > 0 || k != kindFloat && !e.dec.integral() {
			panic(fmt.Errorf("rally: the enum value %s of %s does not pass its other rules or its type", e.text, where))
		}
	}
	others := &rules{minLength: s.minLength, maxLength: s.maxLength, compiled: s.compiled}
	d := &decodeState{}
	for _, v := range s.enum.strs {
		others.checkString(d, v)
		if k == kindText && readsText(scalar) {
			newTextSetter(scalar, nil).set(d, []byte(v), reflect.New(scalar).Elem())
		}
		if len(d.errs) > 0 {
			panic(fmt.Errorf("rally: the enum value %v of %s does not pass its other rules or its type: %s", v, where, d.errs[0].Code))
		}
	}
}

func enumOnly(r *rules) *rules {
	if r == nil || r.enum.values == nil {
		return nil
	}
	return &rules{enum: r.enum}
}

func withoutEnum(r *rules) *rules {
	if r == nil {
		return nil
	}
	c := *r
	c.enum = enumRule{}
	return c.orNil()
}

func (r *rules) orNil() *rules {
	if reflect.ValueOf(*r).IsZero() {
		return nil
	}
	return r
}

func (r *rules) precompute() {
	i := func(format string, v *int) string {
		if v == nil {
			return ""
		}
		return fmt.Sprintf(format, *v)
	}
	if r.minimum != nil {
		r.msgMinimum = "expected number >= " + r.minimum.text
	}
	if r.maximum != nil {
		r.msgMaximum = "expected number <= " + r.maximum.text
	}
	if r.exclusiveMinimum != nil {
		r.msgExclusiveMinimum = "expected number > " + r.exclusiveMinimum.text
	}
	if r.exclusiveMaximum != nil {
		r.msgExclusiveMaximum = "expected number < " + r.exclusiveMaximum.text
	}
	if r.multipleOf != nil {
		r.msgMultipleOf = "expected number to be a multiple of " + r.multipleOf.text
	}
	r.msgMinProperties = i("expected object to have >= %d properties", r.minProperties)
	r.msgMaxProperties = i("expected object to have <= %d properties", r.maxProperties)
	r.msgMinLength = i("expected length >= %d", r.minLength)
	r.msgMaxLength = i("expected length <= %d", r.maxLength)
	r.msgMinItems = i("expected array length >= %d", r.minItems)
	r.msgMaxItems = i("expected array length <= %d", r.maxItems)
	if len(r.enum.values) > 0 {
		parts := make([]string, len(r.enum.values))
		for i, v := range r.enum.values {
			parts[i] = fmt.Sprint(v)
		}
		r.enum.msg = `expected value to be one of "` + strings.Join(parts, ", ") + `"`
	}
}

func setString(d *decodeState, s string, v reflect.Value, r *rules) {
	v.SetString(s)
	if r != nil {
		r.checkString(d, s)
	}
}

func integerValue(num []byte, isInt bool) (mag uint64, neg, integral, fits bool) {
	if isInt {
		neg = num[0] == '-'
		digits := num
		if neg {
			digits = num[1:]
		}
		if len(digits) < 20 {
			for _, c := range digits {
				mag = mag*10 + uint64(c-'0')
			}
			return mag, neg, true, true
		}
		mag, err := strconv.ParseUint(string(digits), 10, 64)
		return mag, neg, true, err == nil
	}
	d := decimalOf(string(num))
	if !d.integral() {
		return 0, d.neg, false, true
	}
	mag, fits, _ = d.magnitude()
	return mag, d.neg, true, fits
}

type intSetter[T int64 | uint64] struct {
	signed             bool
	negLimit, posLimit uint64
	msgMin, msgMax     string
	lo, hi, exLo, exHi T
	msgLo, msgHi       string
	msgExLo, msgExHi   string
	empty              bool
	step               uint64
	hasStep            bool
	msgStep            string
	enum               []T
	msgEnum            string
}

func newIntSetter[T int64 | uint64](t reflect.Type, r *rules) *intSetter[T] {
	tlo, thi := typeRange(t)
	s := &intSetter[T]{signed: tlo.neg, lo: intOf[T](tlo), hi: intOf[T](thi)}
	s.exLo, s.exHi = s.lo, s.hi
	s.negLimit, s.posLimit = -uint64(s.lo), uint64(s.hi)
	s.msgMin, s.msgMax = "expected number >= "+intText(s.lo), "expected number <= "+intText(s.hi)
	if r == nil {
		return s
	}
	bound := func(b *bound, atLeast, strict bool) T {
		n, ok := intAtMost[T](b.dec, strict, tlo, thi)
		if atLeast {
			n, ok = intAtLeast[T](b.dec, strict, tlo, thi)
		}
		s.empty = s.empty || !ok
		return n
	}
	if r.minimum != nil {
		s.lo, s.msgLo = bound(r.minimum, true, false), r.msgMinimum
	}
	if r.maximum != nil {
		s.hi, s.msgHi = bound(r.maximum, false, false), r.msgMaximum
	}
	if r.exclusiveMinimum != nil {
		s.exLo, s.msgExLo = bound(r.exclusiveMinimum, true, true), r.msgExclusiveMinimum
	}
	if r.exclusiveMaximum != nil {
		s.exHi, s.msgExHi = bound(r.exclusiveMaximum, false, true), r.msgExclusiveMaximum
	}
	s.empty = s.empty || max(s.lo, s.exLo) > min(s.hi, s.exHi)
	for _, e := range r.enum.nums {
		n, _ := intAtLeast[T](e.dec, false, tlo, thi)
		s.enum = append(s.enum, n)
	}
	s.msgEnum = r.enum.msg
	if r.multipleOf != nil {
		s.step = r.multipleOf.dec.integerStep()
		s.hasStep, s.msgStep = true, r.msgMultipleOf
	}
	return s
}

func intText[T int64 | uint64](n T) string {
	if n < 0 {
		return strconv.FormatInt(int64(n), 10)
	}
	return strconv.FormatUint(uint64(n), 10)
}

func (s *intSetter[T]) set(d *decodeState, num []byte, isInt bool, v reflect.Value) {
	mag, neg, integral, fits := integerValue(num, isInt)
	switch {
	case !integral:
		d.addErr("type", "expected integer")
		return
	case neg && (!fits || mag > s.negLimit):
		d.addErr("minimum", s.msgMin)
		return
	case !neg && (!fits || mag > s.posLimit):
		d.addErr("maximum", s.msgMax)
		return
	}
	n := T(mag)
	if neg {
		n = -n
	}
	if s.signed {
		v.SetInt(int64(n))
	} else {
		v.SetUint(uint64(n))
	}
	if n < s.lo {
		d.addErr("minimum", s.msgLo)
	}
	if n > s.hi {
		d.addErr("maximum", s.msgHi)
	}
	if n < s.exLo {
		d.addErr("exclusiveMinimum", s.msgExLo)
	}
	if n > s.exHi {
		d.addErr("exclusiveMaximum", s.msgExHi)
	}
	if s.hasStep && (s.step == 0 && mag != 0 || s.step != 0 && mag%s.step != 0) {
		d.addErr("multipleOf", s.msgStep)
	}
	if s.enum != nil && !slices.Contains(s.enum, n) {
		d.addErr("enum", s.msgEnum)
	}
}

type floatSetter struct {
	bits  int
	rules *rules
}

func (s *floatSetter) set(d *decodeState, num []byte, v reflect.Value) bool {
	f, err := strconv.ParseFloat(string(num), s.bits)
	if err != nil {
		return false
	}
	v.SetFloat(f)
	if s.rules != nil {
		if s.bits == 32 {
			f, _ = strconv.ParseFloat(string(num), 64)
		}
		s.rules.checkFloat(d, f, num)
	}
	return true
}

func (r *rules) checkFloat(d *decodeState, f float64, num []byte) {
	compare := func(b bound) int {
		if f != b.f {
			return cmp.Compare(f, b.f)
		}
		return decimalOf(string(num)).cmp(b.dec)
	}
	if r.minimum != nil && compare(*r.minimum) < 0 {
		d.addErr("minimum", r.msgMinimum)
	}
	if r.maximum != nil && compare(*r.maximum) > 0 {
		d.addErr("maximum", r.msgMaximum)
	}
	if r.exclusiveMinimum != nil && compare(*r.exclusiveMinimum) <= 0 {
		d.addErr("exclusiveMinimum", r.msgExclusiveMinimum)
	}
	if r.exclusiveMaximum != nil && compare(*r.exclusiveMaximum) >= 0 {
		d.addErr("exclusiveMaximum", r.msgExclusiveMaximum)
	}
	if r.multipleOf != nil && !multipleOf(decimalOf(string(num)), r.multipleOf.dec) {
		d.addErr("multipleOf", r.msgMultipleOf)
	}
	if r.enum.nums != nil && !slices.ContainsFunc(r.enum.nums, func(e bound) bool { return compare(e) == 0 }) {
		d.addErr("enum", r.enum.msg)
	}
}

func setTime(d *decodeState, raw []byte, v reflect.Value) {
	t, _ := reflect.TypeAssert[*time.Time](v.Addr())
	if !parseRFC3339(t, raw) {
		d.addErr("format", msgRFC3339)
	}
}

type textSetter struct {
	msg   string
	uuid  bool
	rules *rules
}

func newTextSetter(t reflect.Type, r *rules) *textSetter {
	s := &textSetter{msg: "invalid value", rules: stringRules(r)}
	if t == uuidType {
		s.msg, s.uuid = "expected string to be a UUID", true
	}
	return s
}

func (s *textSetter) set(d *decodeState, raw []byte, v reflect.Value) {
	if s.rules != nil {
		s.rules.checkString(d, string(raw))
	}
	if s.uuid && !uuidShape(raw) || v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText(raw) != nil {
		d.addErr("format", s.msg)
	}
}

func uuidShape(b []byte) bool {
	return len(b) == 36 && b[8] == '-' && b[13] == '-' && b[18] == '-' && b[23] == '-'
}

func (r *rules) checkString(d *decodeState, s string) {
	lengthOK := true
	if r.minLength != nil || r.maxLength != nil {
		n := len(s)
		if r.maxLength != nil && n > *r.maxLength || r.minLength != nil && n < utf8.UTFMax**r.minLength {
			n = utf8.RuneCountInString(s)
		}
		if r.minLength != nil && n < *r.minLength {
			d.addErr("minLength", r.msgMinLength)
			lengthOK = false
		}
		if r.maxLength != nil && n > *r.maxLength {
			d.addErr("maxLength", r.msgMaxLength)
			lengthOK = false
		}
	}
	if lengthOK && r.compiled != nil && !r.compiled.Match(s) {
		d.addErr("pattern", r.msgPattern)
	}
	if r.enum.strs != nil && !slices.Contains(r.enum.strs, s) {
		d.addErr("enum", r.enum.msg)
	}
}

func (r *rules) checkMap(d *decodeState, n int) {
	if r.minProperties != nil && n < *r.minProperties {
		d.addErr("minProperties", r.msgMinProperties)
	}
}

func (r *rules) checkArray(d *decodeState, v reflect.Value) {
	n := v.Len()
	if r.minItems != nil && n < *r.minItems {
		d.addErr("minItems", r.msgMinItems)
	}
	if r.unique != nil && !r.unique(v) {
		d.addErr("uniqueItems", msgUniqueItems)
	}
}

func uniqueChecker(elem reflect.Type) func(v reflect.Value) bool {
	switch k := elem.Kind(); {
	case k == reflect.String:
		return uniqueBy(reflect.Value.String)
	case k == reflect.Bool:
		return uniqueBy(reflect.Value.Bool)
	case k >= reflect.Int && k <= reflect.Int64:
		return uniqueBy(reflect.Value.Int)
	case k >= reflect.Uint && k <= reflect.Uint64:
		return uniqueBy(reflect.Value.Uint)
	case k == reflect.Float32 || k == reflect.Float64:
		return uniqueBy(reflect.Value.Float)
	}
	return nil
}

func uniqueBy[T comparable](get func(reflect.Value) T) func(v reflect.Value) bool {
	return func(v reflect.Value) bool {
		n := v.Len()
		var buf [16]T
		if n <= len(buf) {
			for i := range n {
				buf[i] = get(v.Index(i))
				if slices.Contains(buf[:i], buf[i]) {
					return false
				}
			}
			return true
		}
		seen := make(map[T]struct{}, n)
		for i := range n {
			x := get(v.Index(i))
			if _, ok := seen[x]; ok {
				return false
			}
			seen[x] = struct{}{}
		}
		return true
	}
}
