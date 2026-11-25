package rally

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

type paramIn uint8

const (
	inPath paramIn = iota + 1
	inQuery
	inHeader
)

var inNames = [...]string{inPath: "path", inQuery: "query", inHeader: "header"}

var paramTags = [...][]string{
	inPath:   slices.Concat([]string{"path"}, docTags, ruleTags),
	inQuery:  slices.Concat([]string{"query", "default", "required"}, docTags, ruleTags),
	inHeader: slices.Concat([]string{"header", "default", "required"}, docTags, ruleTags),
}

type paramBinder struct {
	in       paramIn
	name     string
	key      string
	index    []int
	required bool
	missing  string
	def      string
	hasDef   bool
	one      func(d *decodeState, s string, v reflect.Value)
	many     func(d *decodeState, values []string, v reflect.Value)
}

func paramLocation(f reflect.StructField) (in paramIn, name string) {
	for in := inPath; in <= inHeader; in++ {
		if v := f.Tag.Get(inNames[in]); v != "" {
			name, opts, _ := strings.Cut(v, ",")
			if opts != "" {
				panic(fmt.Errorf("rally: unknown option %q on %s parameter %q", opts, inNames[in], name))
			}
			return in, name
		}
	}
	return 0, ""
}

func newParamBinder(reg *registry, op *Operation, f reflect.StructField, index []int) (paramBinder, *param) {
	in, name := paramLocation(f)
	where := fmt.Sprintf("%s parameter %q of %s %s", inNames[in], name, op.Method, op.Path)
	if !f.IsExported() {
		panic(fmt.Errorf("rally: %s is the unexported field %s, which rally cannot set: export it", where, f.Name))
	}
	if in == inPath && !slices.Contains(pathParams(op.Path), name) {
		panic(fmt.Errorf("rally: %s is not in the path", where))
	}
	rules, items := parseRules(where, f.Tag, f.Type, paramTags[in])
	b := paramBinder{
		in:       in,
		name:     name,
		index:    index,
		required: in == inPath || f.Tag.Get("required") == "true",
		missing:  "required " + inNames[in] + " parameter is missing",
	}
	if in == inHeader {
		b.key = checkHeaderName(name, where)
		if field, ok := movedHeaders[b.key]; ok {
			panic(fmt.Errorf("rally: %s: net/http moves %s out of the request headers, read Request.%s in a middleware", where, b.key, field))
		}
	}

	t := f.Type
	s := reg.valueSchema(t, "", false, rules, items)
	if kindOf(t) == kindSlice {
		if in != inQuery {
			panic(fmt.Errorf("rally: %s: only query parameters can be lists", where))
		}
		b.many = sliceParamSetter(rules, paramSetter(where, t.Elem(), items))
	} else {
		b.one = paramSetter(where, t, rules)
	}
	if def, ok := f.Tag.Lookup("default"); ok {
		if b.required {
			panic(fmt.Errorf("rally: the default of %s can never apply: the parameter is required", where))
		}
		b.def, b.hasDef = def, true
		s.Default = b.staticValue(t, where, "default", def)
	}
	if example, ok := f.Tag.Lookup("example"); ok {
		s.Examples = []any{b.staticValue(t, where, "example", example)}
	}
	return b, &param{Name: name, In: inNames[in], Description: f.Tag.Get("description"), Required: b.required, Schema: s}
}

func (b *paramBinder) staticValue(t reflect.Type, where, name, s string) json.RawMessage {
	v := reflect.New(t).Elem()
	d := &decodeState{}
	if b.many != nil {
		b.many(d, splitCSV(s), v)
	} else {
		b.one(d, s, v)
	}
	if len(d.errs) > 0 {
		panic(fmt.Errorf("rally: invalid %s %q for %s: %s", name, s, where, d.errs[0].Detail))
	}
	return json.RawMessage(jsonValue(where, t, name, s))
}

type queryValue struct {
	s     string
	ok    bool
	items []string
}

func (p *plan) bindParams(d *decodeState, r *http.Request, in reflect.Value) {
	var query []queryValue
	if r.URL.RawQuery != "" {
		query = p.parseQuery(d, r.URL.RawQuery)
	}
	for i := range p.params {
		b := &p.params[i]
		d.in, d.param = inNames[b.in], b.name
		var s string
		var items []string
		present := false
		switch b.in {
		case inPath:
			s = r.PathValue(b.name)
			present = s != ""
		case inQuery:
			if query != nil {
				s, present, items = query[i].s, query[i].ok, query[i].items
			}
		case inHeader:
			v := r.Header[b.key]
			if len(v) > 1 {
				d.addErr("duplicate", "repeated header")
				continue
			}
			if present = len(v) == 1; present {
				s = v[0]
			}
		}
		if !present {
			if !b.hasDef {
				if b.required {
					d.addErr("required", b.missing)
				}
				continue
			}
			s = b.def
		}
		v := fieldByIndex(in, b.index)
		if b.many != nil {
			if !present {
				items = splitCSV(s)
			}
			b.many(d, items, v)
		} else {
			b.one(d, s, v)
		}
	}
	d.in, d.param = "", ""
}

func (p *plan) parseQuery(d *decodeState, raw string) []queryValue {
	values := slices.Grow(d.query[:0], len(p.params))[:len(p.params)]
	clear(values)
	d.query = values
	d.in = "query"
	for raw != "" {
		var pair string
		pair, raw, _ = strings.Cut(raw, "&")
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		i := p.queryParam(k)
		d.param = ""
		if i >= 0 {
			d.param = p.params[i].name
		}
		if i < 0 {
			if slices.ContainsFunc(p.securityKeys, func(key string) bool { return queryKeyIs(k, key) }) {
				continue
			}
			if name, ok := unescapeQuery(d, k); ok {
				d.param = name
				d.addErr("additionalProperties", "unknown query parameter")
			}
			continue
		}
		s, ok := unescapeQuery(d, v)
		switch {
		case !ok:
		case p.params[i].many != nil:
			values[i].ok = true
			values[i].items = append(values[i].items, s)
		case values[i].ok:
			d.addErr("duplicate", "repeated query parameter")
		default:
			values[i] = queryValue{s: s, ok: true}
		}
	}
	return values
}

func (p *plan) queryParam(rawKey string) int {
	for i := range p.params {
		if p.params[i].in == inQuery && queryKeyIs(rawKey, p.params[i].name) {
			return i
		}
	}
	return -1
}

func unescapeQuery(d *decodeState, s string) (string, bool) {
	u, err := url.QueryUnescape(s)
	switch {
	case err != nil:
		d.addErr("syntax", "invalid percent-encoding in query string")
		return "", false
	case !utf8.ValidString(u):
		d.addErr("syntax", "invalid UTF-8 in query string")
		return "", false
	}
	return u, true
}

var movedHeaders = map[string]string{"Host": "Host", "Transfer-Encoding": "TransferEncoding"}

func checkHeaderName(name, where string) string {
	valid := name != "" && !strings.ContainsFunc(name, func(c rune) bool {
		return !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c))
	})
	if !valid {
		panic(fmt.Errorf("rally: %q is not a valid header name (%s)", name, where))
	}
	return http.CanonicalHeaderKey(name)
}

func queryKeyIs(raw, key string) bool {
	j := 0
	for i := 0; i < len(raw); i, j = i+1, j+1 {
		c := raw[i]
		switch c {
		case '+':
			c = ' '
		case '%':
			hi, lo := unhex(raw, i+1), unhex(raw, i+2)
			if hi < 0 || lo < 0 {
				return false
			}
			c, i = byte(hi<<4|lo), i+2
		}
		if j == len(key) || key[j] != c {
			return false
		}
	}
	return j == len(key)
}

func unhex(s string, i int) int {
	if i < len(s) {
		switch c := s[i]; {
		case '0' <= c && c <= '9':
			return int(c - '0')
		case 'a' <= c && c <= 'f':
			return int(c - 'a' + 10)
		case 'A' <= c && c <= 'F':
			return int(c - 'A' + 10)
		}
	}
	return -1
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func paramSetter(where string, t reflect.Type, r *rules) func(d *decodeState, s string, v reflect.Value) {
	switch kindOf(t) {
	case kindPointer:
		et := t.Elem()
		elem := paramSetter(where, et, r)
		return func(d *decodeState, s string, v reflect.Value) {
			p := reflect.New(et)
			elem(d, s, p.Elem())
			v.Set(p)
		}
	case kindText:
		if t == timeType {
			return func(d *decodeState, s string, v reflect.Value) { setTime(d, []byte(s), v) }
		}
		if readsText(t) {
			text := newTextSetter(t, r)
			return func(d *decodeState, s string, v reflect.Value) { text.set(d, []byte(s), v) }
		}
	case kindString:
	case kindBool:
		return func(d *decodeState, s string, v reflect.Value) {
			if s != "true" && s != "false" {
				d.addErr("type", "invalid boolean")
				return
			}
			v.SetBool(s == "true")
		}
	case kindInt:
		return intParamSetter(newIntSetter[int64](t, r))
	case kindUint:
		return intParamSetter(newIntSetter[uint64](t, r))
	case kindFloat:
		f := &floatSetter{bits: t.Bits(), rules: numberRules(r)}
		return func(d *decodeState, s string, v reflect.Value) {
			if _, ok := parseDecimal(s); !ok || !f.set(d, []byte(s), v) {
				d.addErr("type", "invalid float")
			}
		}
	default:
		panic(fmt.Errorf("rally: unsupported type %s for %s", t, where))
	}
	r = stringRules(r)
	return func(d *decodeState, s string, v reflect.Value) { setString(d, s, v, r) }
}

func intParamSetter[T int64 | uint64](n *intSetter[T]) func(d *decodeState, s string, v reflect.Value) {
	return func(d *decodeState, s string, v reflect.Value) {
		if _, ok := parseDecimal(s); ok {
			n.set(d, []byte(s), !strings.ContainsAny(s, ".eE"), v)
		} else {
			d.addErr("type", "invalid integer")
		}
	}
}

func sliceParamSetter(r *rules, elem func(*decodeState, string, reflect.Value)) func(*decodeState, []string, reflect.Value) {
	limit := -1
	if r != nil && r.maxItems != nil {
		limit = *r.maxItems
	}
	return func(d *decodeState, values []string, v reflect.Value) {
		tooMany := limit >= 0 && len(values) > limit
		if tooMany {
			values = values[:limit]
		}
		v.Grow(len(values))
		v.SetLen(len(values))
		for i, s := range values {
			if len(d.errs) == maxErrors {
				break
			}
			elem(d, s, v.Index(i))
		}
		if tooMany {
			d.addErr("maxItems", r.msgMaxItems)
		}
		if r != nil {
			r.checkArray(d, v)
		}
	}
}
