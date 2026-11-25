package rally

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"reflect"
	"strconv"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"uuid"
)

const (
	maxDepth        = 10000
	maxErrors       = 100
	maxPointerBytes = 16 << 10
)

type decFunc func(d *decodeState, v reflect.Value)

type jsonSyntaxError struct {
	msg    string
	offset int
}

func (e *jsonSyntaxError) Error() string {
	return "invalid JSON: " + e.msg + " at offset " + strconv.Itoa(e.offset)
}

type tooManyErrors struct{}

type frame struct {
	key string
	idx int
}

type decodeState struct {
	buf     []byte
	pos     int
	depth   int
	inJSON  bool
	in      string
	param   string
	frames  []frame
	errs    []ErrorDetail
	scratch []byte
	body    []byte
	query   []queryValue

	pointerBytes int
}

func (d *decodeState) decode(dec decFunc, data []byte, v reflect.Value, in string) (err error) {
	d.buf, d.pos, d.depth, d.in, d.param, d.inJSON = data, 0, 0, in, "", true
	d.frames = d.frames[:0]
	defer func() {
		d.inJSON = false
		if r := recover(); r != nil {
			switch e := r.(type) {
			case *jsonSyntaxError:
				err = e
			case tooManyErrors:
			default:
				panic(r)
			}
		}
	}()
	d.beginValue()
	dec(d, v)
	d.skipWS()
	if d.pos < len(d.buf) {
		d.syntaxErr("unexpected data after top-level value")
	}
	return nil
}

func (d *decodeState) syntaxErr(msg string) {
	panic(&jsonSyntaxError{msg: msg, offset: d.pos})
}

const whitespace uint64 = 1<<' ' | 1<<'\t' | 1<<'\n' | 1<<'\r'

func (d *decodeState) skipWS() {
	for d.pos < len(d.buf) && d.buf[d.pos] <= ' ' && whitespace>>d.buf[d.pos]&1 != 0 {
		d.pos++
	}
}

func (d *decodeState) beginValue() {
	d.skipWS()
	if d.pos >= len(d.buf) {
		d.syntaxErr("unexpected end of JSON input")
	}
}

func (d *decodeState) expect(c byte) {
	if d.pos >= len(d.buf) || d.buf[d.pos] != c {
		d.syntaxErr("expected '" + string(c) + "'")
	}
	d.pos++
}

func (d *decodeState) endOfItem(end byte) bool {
	d.skipWS()
	if d.pos >= len(d.buf) {
		d.syntaxErr("unexpected end of JSON input")
	}
	switch d.buf[d.pos] {
	case ',':
		d.pos++
		d.skipWS()
		return false
	case end:
		d.pos++
		return true
	}
	d.syntaxErr("expected ',' or '" + string(end) + "'")
	return false
}

func (d *decodeState) open(end byte) bool {
	d.pos++
	d.enter()
	d.skipWS()
	if d.pos < len(d.buf) && d.buf[d.pos] == end {
		d.pos++
		d.depth--
		return false
	}
	return true
}

func (d *decodeState) enter() {
	d.depth++
	if d.depth > maxDepth {
		d.syntaxErr("exceeded max depth")
	}
}

func (d *decodeState) literal(lit string) {
	if len(d.buf)-d.pos < len(lit) || string(d.buf[d.pos:d.pos+len(lit)]) != lit {
		d.syntaxErr("invalid literal")
	}
	d.pos += len(lit)
}

var strSafe = func() (t [256]bool) {
	for c := 0x20; c < 0x80; c++ {
		t[c] = c != '"' && c != '\\'
	}
	return t
}()

func (d *decodeState) scanString() ([]byte, bool) {
	buf := d.buf
	start := d.pos + 1
	i := start
	for len(buf)-i >= 8 {
		if m := special8(binary.LittleEndian.Uint64(buf[i:])); m != 0 {
			i += bits.TrailingZeros64(m) / 8
			break
		}
		i += 8
	}
	escaped := false
	for {
		for i < len(buf) && strSafe[buf[i]] {
			i++
		}
		if i >= len(buf) {
			d.pos = i
			d.syntaxErr("unexpected end of JSON input in string")
		}
		switch c := buf[i]; {
		case c == '"':
			d.pos = i + 1
			return buf[start:i], escaped
		case c == '\\':
			escaped = true
			d.pos = i
			if i+1 >= len(buf) {
				d.syntaxErr("unexpected end of JSON input in string")
			}
			switch buf[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				r := hex4(buf[i+2:])
				i += 6
				switch {
				case r < 0:
					d.syntaxErr("invalid \\u escape in string")
				case r >= 0xDC00 && r <= 0xDFFF:
					d.syntaxErr("invalid surrogate in string")
				case r >= 0xD800 && r <= 0xDBFF:
					if i+1 >= len(buf) || buf[i] != '\\' || buf[i+1] != 'u' {
						d.syntaxErr("invalid surrogate in string")
					}
					if r2 := hex4(buf[i+2:]); r2 < 0xDC00 || r2 > 0xDFFF {
						d.syntaxErr("invalid surrogate in string")
					}
					i += 6
				}
			default:
				d.syntaxErr("invalid escape in string")
			}
		case c < 0x20:
			d.pos = i
			d.syntaxErr("invalid control character in string")
		default:
			r, size := utf8.DecodeRune(buf[i:])
			if r == utf8.RuneError && size == 1 {
				d.pos = i
				d.syntaxErr("invalid UTF-8 in string")
			}
			i += size
		}
	}
}

func special8(w uint64) uint64 {
	const ones = 0x0101010101010101
	return (w | (w - 0x20*ones) | ((w ^ '"'*ones) - ones) | ((w ^ '\\'*ones) - ones)) & (0x80 * ones)
}

func hex4(b []byte) rune {
	if len(b) < 4 {
		return -1
	}
	var r rune
	for _, c := range b[:4] {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c -= 'a' - 10
		case c >= 'A' && c <= 'F':
			c -= 'A' - 10
		default:
			return -1
		}
		r = r<<4 | rune(c)
	}
	return r
}

func appendUnescaped(dst, raw []byte) []byte {
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			j := i + 1
			for j < len(raw) && raw[j] != '\\' {
				j++
			}
			dst = append(dst, raw[i:j]...)
			i = j
			continue
		}
		switch c := raw[i+1]; c {
		case 'b':
			dst = append(dst, '\b')
		case 'f':
			dst = append(dst, '\f')
		case 'n':
			dst = append(dst, '\n')
		case 'r':
			dst = append(dst, '\r')
		case 't':
			dst = append(dst, '\t')
		case 'u':
			r := hex4(raw[i+2:])
			i += 6
			if utf16.IsSurrogate(r) {
				r = utf16.DecodeRune(r, hex4(raw[i+2:]))
				i += 6
			}
			dst = utf8.AppendRune(dst, r)
			continue
		default:
			dst = append(dst, c)
		}
		i += 2
	}
	return dst
}

func (d *decodeState) readRaw() []byte {
	raw, esc := d.scanString()
	if !esc {
		return raw
	}
	d.scratch = appendUnescaped(d.scratch[:0], raw)
	return d.scratch
}

func (d *decodeState) readString() string {
	return string(d.readRaw())
}

func (d *decodeState) scanNumber() (num []byte, isInt bool) {
	buf := d.buf
	i := d.pos
	start := i
	isInt = true
	if i < len(buf) && buf[i] == '-' {
		i++
	}
	switch {
	case i < len(buf) && buf[i] == '0':
		i++
	case i < len(buf) && buf[i] >= '1' && buf[i] <= '9':
		i++
		for i < len(buf) && isDigit(buf[i]) {
			i++
		}
	default:
		d.pos = i
		d.syntaxErr("invalid character in value")
	}
	if i < len(buf) && buf[i] == '.' {
		isInt = false
		i++
		if i >= len(buf) || !isDigit(buf[i]) {
			d.pos = i
			d.syntaxErr("invalid number")
		}
		for i < len(buf) && isDigit(buf[i]) {
			i++
		}
	}
	if i < len(buf) && (buf[i] == 'e' || buf[i] == 'E') {
		isInt = false
		i++
		if i < len(buf) && (buf[i] == '+' || buf[i] == '-') {
			i++
		}
		if i >= len(buf) || !isDigit(buf[i]) {
			d.pos = i
			d.syntaxErr("invalid number")
		}
		for i < len(buf) && isDigit(buf[i]) {
			i++
		}
	}
	d.pos = i
	return buf[start:i], isInt
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isNumberStart(c byte) bool { return c == '-' || c >= '0' && c <= '9' }

func (d *decodeState) skipValue() {
	switch d.buf[d.pos] {
	case '{':
		if d.open('}') {
			d.skipMembers()
			d.depth--
		}
	case '[':
		if d.open(']') {
			d.skipItems()
			d.depth--
		}
	case '"':
		d.scanString()
	case 't':
		d.literal("true")
	case 'f':
		d.literal("false")
	case 'n':
		d.literal("null")
	default:
		d.scanNumber()
	}
}

func (d *decodeState) skipItems() {
	for {
		d.beginValue()
		d.skipValue()
		if d.endOfItem(']') {
			return
		}
	}
}

func (d *decodeState) addErr(code, msg string) {
	if len(d.errs) < maxErrors {
		pointer := d.pointer()
		d.pointerBytes += len(pointer)
		d.errs = append(d.errs, ErrorDetail{Detail: msg, Code: code, In: d.in, Pointer: pointer, Parameter: d.param})
	}
	if d.inJSON && (len(d.errs) == maxErrors || d.pointerBytes > maxPointerBytes) {
		panic(tooManyErrors{})
	}
}

func (d *decodeState) pointer() string {
	if len(d.frames) == 0 {
		return ""
	}
	b := make([]byte, 0, 64)
	for _, f := range d.frames {
		b = append(b, '/')
		if f.idx >= 0 {
			b = strconv.AppendInt(b, int64(f.idx), 10)
			continue
		}
		for i := 0; i < len(f.key); i++ {
			switch c := f.key[i]; c {
			case '~':
				b = append(b, '~', '0')
			case '/':
				b = append(b, '~', '1')
			default:
				b = append(b, c)
			}
		}
	}
	return string(b)
}

func (d *decodeState) pushKey(k string) { d.frames = append(d.frames, frame{key: k, idx: -1}) }
func (d *decodeState) pushIndex(i int)  { d.frames = append(d.frames, frame{idx: i}) }
func (d *decodeState) pop()             { d.frames = d.frames[:len(d.frames)-1] }

func (d *decodeState) mismatch(msg string) {
	d.skipValue()
	d.addErr("type", msg)
}

func (d *decodeState) skipWithError(key, code, msg string) {
	d.pushKey(key)
	d.skipValue()
	d.addErr(code, msg)
	d.pop()
}

func cached[V any](cache map[reflect.Type]*V, t reflect.Type, build func(*V, reflect.Type)) *V {
	if v, ok := cache[t]; ok {
		return v
	}
	v := new(V)
	cache[t] = v
	built := false
	defer func() {
		if !built {
			delete(cache, t)
		}
	}()
	build(v, t)
	built = true
	return v
}

var structDecs = map[reflect.Type]*structDec{}

func compileDecoder(t reflect.Type, r, items *rules, nullable bool) decFunc {
	switch kindOf(t) {
	case kindText:
		return textDecoder(t, r)
	case kindString:
		return stringDecoder(r)
	case kindBool:
		return boolDecoder
	case kindInt:
		return intDecoder(newIntSetter[int64](t, r))
	case kindUint:
		return intDecoder(newIntSetter[uint64](t, r))
	case kindFloat:
		return floatDecoder(t, r)
	case kindPointer:
		return pointerDecoder(t.Elem(), compileDecoder(t.Elem(), r, items, false), nullable)
	case kindSlice:
		return sliceDecoder(t, compileDecoder(t.Elem(), items, nil, true), r)
	case kindMap:
		return mapDecoder(t, compileDecoder(t.Elem(), nil, nil, true), r)
	}
	return cached(structDecs, t, (*structDec).build).decode
}

func stringDecoder(r *rules) decFunc {
	r = stringRules(r)
	return func(d *decodeState, v reflect.Value) {
		if d.buf[d.pos] != '"' {
			d.mismatch("expected string")
			return
		}
		setString(d, d.readString(), v, r)
	}
}

func boolDecoder(d *decodeState, v reflect.Value) {
	switch d.buf[d.pos] {
	case 't':
		d.literal("true")
		v.SetBool(true)
	case 'f':
		d.literal("false")
		v.SetBool(false)
	default:
		d.mismatch("expected boolean")
	}
}

func intDecoder[T int64 | uint64](s *intSetter[T]) decFunc {
	return func(d *decodeState, v reflect.Value) {
		if !isNumberStart(d.buf[d.pos]) {
			d.mismatch("expected integer")
			return
		}
		num, isInt := d.scanNumber()
		s.set(d, num, isInt, v)
	}
}

func floatDecoder(t reflect.Type, r *rules) decFunc {
	s := &floatSetter{bits: t.Bits(), rules: numberRules(r)}
	return func(d *decodeState, v reflect.Value) {
		if !isNumberStart(d.buf[d.pos]) {
			d.mismatch("expected number")
			return
		}
		if num, _ := d.scanNumber(); !s.set(d, num, v) {
			d.addErr("type", "expected number within range")
		}
	}
}

const msgRFC3339 = "expected string to be RFC 3339 date-time"

func timeDecoder(d *decodeState, v reflect.Value) {
	if d.buf[d.pos] != '"' {
		d.mismatch("expected string")
		return
	}
	setTime(d, d.readRaw(), v)
}

func parseRFC3339(t *time.Time, b []byte) bool {
	if len(b) < len("2006-01-02T15:04:05Z") || b[4] != '-' || b[7] != '-' || b[10] != 'T' && b[10] != 't' || b[13] != ':' || b[16] != ':' {
		return false
	}
	num := func(i, n int) int {
		v := 0
		for _, c := range b[i : i+n] {
			if !isDigit(c) {
				return -1
			}
			v = v*10 + int(c-'0')
		}
		return v
	}
	year, month, day := num(0, 4), num(5, 2), num(8, 2)
	hour, minute, sec := num(11, 2), num(14, 2), num(17, 2)
	if year < 0 || month < 1 || month > 12 || day < 1 || day > daysIn(time.Month(month), year) || hour < 0 || hour > 23 || minute < 0 || minute > 59 || sec < 0 || sec > 59 {
		return false
	}
	i, nsec := 19, 0
	if b[i] == '.' {
		i++
		j := i
		for i < len(b) && isDigit(b[i]) {
			i++
		}
		if i == j || i-j > 9 {
			return false
		}
		nsec = num(j, i-j)
		for range 9 - (i - j) {
			nsec *= 10
		}
	}
	loc := time.UTC
	switch rest := b[i:]; {
	case len(rest) == 1 && (rest[0] == 'Z' || rest[0] == 'z'):
	case len(rest) == 6 && (rest[0] == '+' || rest[0] == '-') && rest[3] == ':':
		oh, om := num(i+1, 2), num(i+4, 2)
		if oh < 0 || oh > 23 || om < 0 || om > 59 || rest[0] == '-' && oh == 0 && om == 0 {
			return false
		}
		off := oh*3600 + om*60
		if rest[0] == '-' {
			off = -off
		}
		loc = time.FixedZone("", off)
	default:
		return false
	}
	*t = time.Date(year, time.Month(month), day, hour, minute, sec, nsec, loc)
	return true
}

func daysIn(m time.Month, year int) int {
	if m == time.February {
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	}
	return 31 - int(m-1)%7%2
}

var uuidType = reflect.TypeFor[uuid.UUID]()

func readsText(t reflect.Type) bool {
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return true
	}
	if t.Kind() != reflect.String {
		panic(fmt.Errorf("rally: %s has a MarshalText method but no UnmarshalText: it cannot be read from requests", t))
	}
	return false
}

func textDecoder(t reflect.Type, r *rules) decFunc {
	if t == timeType {
		return timeDecoder
	}
	if !readsText(t) {
		return stringDecoder(r)
	}
	s := newTextSetter(t, r)
	return func(d *decodeState, v reflect.Value) {
		if d.buf[d.pos] != '"' {
			d.mismatch("expected string")
			return
		}
		s.set(d, d.readRaw(), v)
	}
}

func pointerDecoder(elemT reflect.Type, elem decFunc, nullable bool) decFunc {
	msg := "unexpected null"
	switch kindOf(elemT) {
	case kindString, kindText:
		msg = "expected string"
	case kindBool:
		msg = "expected boolean"
	case kindInt, kindUint:
		msg = "expected integer"
	case kindFloat:
		msg = "expected number"
	case kindSlice:
		msg = "expected array"
	case kindMap, kindStruct:
		msg = "expected object"
	}
	return func(d *decodeState, v reflect.Value) {
		if d.buf[d.pos] == 'n' {
			if !nullable {
				d.mismatch(msg)
				return
			}
			d.literal("null")
			v.SetZero()
			return
		}
		if v.IsNil() {
			v.Set(reflect.New(elemT))
		}
		elem(d, v.Elem())
	}
}

func sliceDecoder(t reflect.Type, elem decFunc, r *rules) decFunc {
	empty := reflect.MakeSlice(t, 0, 0)
	limit := -1
	if r != nil && r.maxItems != nil {
		limit = *r.maxItems
	}
	return func(d *decodeState, v reflect.Value) {
		if d.buf[d.pos] != '[' {
			d.mismatch("expected array")
			return
		}
		v.Set(empty)
		n := 0
		if d.open(']') {
			for {
				if n == limit {
					d.skipItems()
					d.addErr("maxItems", r.msgMaxItems)
					break
				}
				if n == v.Len() {
					v.Grow(max(n, 4))
					v.SetLen(v.Cap())
				}
				d.beginValue()
				d.pushIndex(n)
				elem(d, v.Index(n))
				d.pop()
				n++
				if d.endOfItem(']') {
					break
				}
			}
			d.depth--
			v.SetLen(n)
		}
		if r != nil {
			r.checkArray(d, v)
		}
	}
}

type mapItem struct {
	key   reflect.Value
	value reflect.Value
}

func mapDecoder(t reflect.Type, elem decFunc, r *rules) decFunc {
	limit := -1
	if r != nil && r.maxProperties != nil {
		limit = *r.maxProperties
	}
	items := sync.Pool{New: func() any {
		return &mapItem{key: reflect.New(t.Key()).Elem(), value: reflect.New(t.Elem()).Elem()}
	}}
	return func(d *decodeState, v reflect.Value) {
		if d.buf[d.pos] != '{' {
			d.mismatch("expected object")
			return
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(t))
		}
		if d.open('}') {
			it := items.Get().(*mapItem)
			for {
				if v.Len() == limit {
					d.skipMembers()
					d.addErr("maxProperties", r.msgMaxProperties)
					break
				}
				if d.pos >= len(d.buf) || d.buf[d.pos] != '"' {
					d.syntaxErr("expected string for object key")
				}
				k := d.readString()
				d.skipWS()
				d.expect(':')
				d.beginValue()
				it.value.SetZero()
				d.pushKey(k)
				elem(d, it.value)
				it.key.SetString(k)
				n := v.Len()
				v.SetMapIndex(it.key, it.value)
				if v.Len() == n {
					d.addErr("duplicate", "duplicate property")
				}
				d.pop()
				if d.endOfItem('}') {
					break
				}
			}
			d.depth--
			it.key.SetZero()
			it.value.SetZero()
			items.Put(it)
		}
		if r != nil {
			r.checkMap(d, v.Len())
		}
	}
}

func (d *decodeState) skipMembers() {
	for {
		if d.pos >= len(d.buf) || d.buf[d.pos] != '"' {
			d.syntaxErr("expected string for object key")
		}
		d.scanString()
		d.skipWS()
		d.expect(':')
		d.beginValue()
		d.skipValue()
		if d.endOfItem('}') {
			return
		}
	}
}

type structDec struct {
	fields   []decField
	byName   map[string]int
	words    int
	required []int
	reqMsgs  []string
	defaults []defaultField
}

type decField struct {
	name  string
	key   string
	index []int
	dec   decFunc
}

type defaultField struct {
	idx int
	set func(v reflect.Value)
}

func (sd *structDec) build(t reflect.Type) {
	for i, f := range fieldsOf(t) {
		sd.fields = append(sd.fields, decField{
			name:  f.name,
			key:   quotedKey(f.name),
			index: f.index,
			dec:   compileDecoder(f.typ, f.rules, f.items, f.nullable),
		})
		if f.required {
			sd.required = append(sd.required, i)
			sd.reqMsgs = append(sd.reqMsgs, "expected required property "+f.name+" to be present")
		}
		if f.def != nil {
			sd.defaults = append(sd.defaults, defaultField{idx: i, set: defaultSetter(f.typ, f.def)})
		}
	}
	if len(sd.fields) > 16 {
		sd.byName = make(map[string]int, len(sd.fields))
		for i, f := range sd.fields {
			sd.byName[f.name] = i
		}
	}
	sd.words = (len(sd.fields) + 63) / 64
}

func quotedKey(name string) string {
	for i := 0; i < len(name); i++ {
		if !strSafe[name[i]] {
			return ""
		}
	}
	return `"` + name + `":`
}

func (s *structDec) lookup(key []byte, next int) int {
	if next < len(s.fields) && s.fields[next].name == string(key) {
		return next
	}
	if s.byName != nil {
		if i, ok := s.byName[string(key)]; ok {
			return i
		}
		return -1
	}
	for i := range s.fields {
		if s.fields[i].name == string(key) {
			return i
		}
	}
	return -1
}

func (s *structDec) decode(d *decodeState, v reflect.Value) {
	if d.buf[d.pos] != '{' {
		d.mismatch("expected object")
		return
	}
	var small [2]uint64
	seen := small[:]
	if s.words > len(small) {
		seen = make([]uint64, s.words)
	}
	if d.open('}') {
		next := 0
		for {
			i := -1
			var key []byte
			if next < len(s.fields) {
				if k := s.fields[next].key; k != "" && len(d.buf)-d.pos >= len(k) && string(d.buf[d.pos:d.pos+len(k)]) == k {
					d.pos += len(k)
					d.beginValue()
					i = next
				}
			}
			if i < 0 {
				if d.pos >= len(d.buf) || d.buf[d.pos] != '"' {
					d.syntaxErr("expected string for object key")
				}
				key = d.readRaw()
				d.skipWS()
				d.expect(':')
				d.beginValue()
				i = s.lookup(key, next)
			}
			next = i + 1
			switch {
			case i < 0:
				d.skipWithError(string(key), "additionalProperties", "unexpected property")
			case seen[i>>6]&(1<<(i&63)) != 0:
				d.skipWithError(s.fields[i].name, "duplicate", "duplicate property")
			default:
				seen[i>>6] |= 1 << (i & 63)
				f := &s.fields[i]
				d.pushKey(f.name)
				f.dec(d, fieldByIndex(v, f.index))
				d.pop()
			}
			if d.endOfItem('}') {
				break
			}
		}
		d.depth--
	}
	for k, i := range s.required {
		if seen[i>>6]&(1<<(i&63)) == 0 {
			d.pushKey(s.fields[i].name)
			d.addErr("required", s.reqMsgs[k])
			d.pop()
		}
	}
	for _, df := range s.defaults {
		if seen[df.idx>>6]&(1<<(df.idx&63)) == 0 {
			df.set(fieldByIndex(v, s.fields[df.idx].index))
		}
	}
}

func defaultSetter(t reflect.Type, raw []byte) func(v reflect.Value) {
	dec := compileDecoder(t, nil, nil, false)
	set := func(v reflect.Value) {
		s := getState()
		_ = s.dec.decode(dec, raw, v, "")
		putState(s)
	}
	switch kindOf(t) {
	case kindString, kindBool, kindInt, kindUint, kindFloat:
		def := reflect.New(t).Elem()
		set(def)
		return func(v reflect.Value) { v.Set(def) }
	}
	return set
}
