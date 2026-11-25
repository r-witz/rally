package rally

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

var (
	timeType            = reflect.TypeFor[time.Time]()
	textMarshalerType   = reflect.TypeFor[encoding.TextMarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	jsonMarshalerType   = reflect.TypeFor[json.Marshaler]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

type kind uint8

const (
	kindString kind = iota + 1
	kindBool
	kindInt
	kindUint
	kindFloat
	kindText
	kindPointer
	kindSlice
	kindMap
	kindStruct
)

func kindOf(t reflect.Type) kind {
	pt := reflect.PointerTo(t)
	if t != timeType && (pt.Implements(jsonMarshalerType) || pt.Implements(jsonUnmarshalerType)) {
		panic(fmt.Errorf("rally: %s has custom JSON methods (MarshalJSON, UnmarshalJSON), which are not supported: "+
			"implement encoding.TextMarshaler and encoding.TextUnmarshaler to read and write it as a string", t))
	}
	if isText(t) {
		if t.Kind() == reflect.Struct && t.NumField() > 1 {
			for i := range t.NumField() {
				if f := t.Field(i); f.Anonymous && isText(deref(f.Type)) {
					panic(fmt.Errorf("rally: %s embeds %s, whose text methods make all of %s read and written as a string, so its other fields are ignored: give the embedded field a name", t, f.Type, t))
				}
			}
		}
		return kindText
	}
	switch t.Kind() {
	case reflect.String:
		return kindString
	case reflect.Bool:
		return kindBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return kindInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return kindUint
	case reflect.Float32, reflect.Float64:
		return kindFloat
	case reflect.Pointer:
		if t.Elem().Kind() == reflect.Pointer {
			panic(fmt.Errorf("rally: pointers to pointers such as %s are not supported", t))
		}
		return kindPointer
	case reflect.Slice:
		checkNotRecursive(t)
		if kindOf(t.Elem()) == kindUint && t.Elem().Kind() == reflect.Uint8 {
			panic(fmt.Errorf("rally: %s is not supported (encoding/json writes it in base64): use a string", t))
		}
		return kindSlice
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic(fmt.Errorf("rally: map keys must be strings, got %s", t))
		}
		if isText(t.Key()) {
			panic(fmt.Errorf("rally: map keys must be plain strings: the text methods of %s would not be used in %s", t.Key(), t))
		}
		checkNotRecursive(t)
		return kindMap
	case reflect.Struct:
		return kindStruct
	case reflect.Interface:
		panic(fmt.Errorf("rally: interface types such as %s are not supported: the schema of the value would be unknown", t))
	case reflect.Array:
		panic(fmt.Errorf("rally: arrays such as %s are not supported: use a slice", t))
	}
	panic(fmt.Errorf("rally: %s is not supported", t))
}

func isText(t reflect.Type) bool {
	pt := reflect.PointerTo(t)
	return pt.Implements(textMarshalerType) || pt.Implements(textUnmarshalerType)
}

func checkNotRecursive(t reflect.Type) {
	var seen []reflect.Type
	for e := t; (e.Kind() == reflect.Pointer || e.Kind() == reflect.Slice || e.Kind() == reflect.Map) && (e == t || !isText(e)); e = e.Elem() {
		if slices.Contains(seen, e) {
			panic(fmt.Errorf("rally: %s is recursive without a struct in the cycle, which is not supported: wrap it in a struct", t))
		}
		seen = append(seen, e)
	}
}

func fieldByIndex(v reflect.Value, index []int) reflect.Value {
	for _, i := range index {
		v = v.Field(i)
	}
	return v
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

type field struct {
	name      string
	goName    string
	index     []int
	typ       reflect.Type
	omitEmpty bool
	omitZero  bool
	required  bool
	nullable  bool

	rules *rules
	items *rules

	def         []byte
	description string
	example     []byte
}

var fieldsCache = map[reflect.Type][]*field{}

func fieldsOf(t reflect.Type) []*field {
	if fs, ok := fieldsCache[t]; ok {
		return fs
	}
	type candidate struct {
		f     *field
		depth int
	}
	byName := map[string]candidate{}
	var order []string
	var walk func(t reflect.Type, base []int)
	walk = func(t reflect.Type, base []int) {
		for i := range t.NumField() {
			sf := t.Field(i)
			where := t.String() + "." + sf.Name
			tag := sf.Tag.Get("json")
			if sf.Name == "_" || tag == "-" {
				checkTags(where, sf.Tag, ignoredTags)
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			if sf.Anonymous && name == "" {
				checkTags(where, sf.Tag, ignoredTags)
				if sf.Type.Kind() == reflect.Pointer && sf.Type.Elem().Kind() == reflect.Struct {
					panic(fmt.Errorf("rally: embedded pointer %s in %s is not supported, embed the struct by value", sf.Type, t))
				}
				if sf.Type.Kind() == reflect.Struct {
					walk(sf.Type, append(slices.Clip(base), i))
					continue
				}
			}
			if !sf.IsExported() {
				checkTags(where, sf.Tag, ignoredTags)
				continue
			}
			if name == "" {
				name = sf.Name
			}
			f := newField(where, sf, name, opts)
			f.index = append(slices.Clip(base), i)
			depth := len(base)
			switch prev, ok := byName[name]; {
			case !ok:
				byName[name] = candidate{f, depth}
				order = append(order, name)
			case depth < prev.depth:
				byName[name] = candidate{f, depth}
			case depth == prev.depth:
				panic(fmt.Errorf("rally: duplicate JSON property %q in %s", name, t))
			}
		}
	}
	walk(t, nil)
	fs := make([]*field, len(order))
	for i, name := range order {
		fs[i] = byName[name].f
	}
	fieldsCache[t] = fs
	return fs
}

func newField(where string, sf reflect.StructField, name, jsonOpts string) *field {
	f := &field{name: name, goName: sf.Name, typ: sf.Type}
	for opt := range strings.SplitSeq(jsonOpts, ",") {
		switch opt {
		case "omitempty":
			f.omitEmpty = true
		case "omitzero":
			f.omitZero = true
		case "string":
			panic(fmt.Errorf("rally: the json \",string\" option is not supported (%s)", where))
		}
	}
	tag := sf.Tag
	defTag, hasDefault := tag.Lookup("default")
	if _, ok := tag.Lookup("required"); ok {
		panic(fmt.Errorf("rally: the required tag is not supported on %s: a field is optional when it has omitempty, omitzero or a default", where))
	}
	f.required = !f.omitEmpty && !f.omitZero && !hasDefault
	f.nullable = sf.Type.Kind() == reflect.Pointer && !f.omitEmpty && !f.omitZero
	f.rules, f.items = parseRules(where, tag, sf.Type, fieldTags)
	if hasDefault {
		f.def = parseValue(where, f, "default", defTag)
	}
	if example, ok := tag.Lookup("example"); ok {
		f.example = parseValue(where, f, "example", example)
	}
	f.description = tag.Get("description")
	return f
}

func parseValue(where string, f *field, name, raw string) []byte {
	t := deref(f.typ)
	raw = jsonValue(where, t, name, raw)
	d := &decodeState{}
	err := d.decode(compileDecoder(t, f.rules, f.items, false), []byte(raw), reflect.New(t).Elem(), name)
	if err == nil && len(d.errs) > 0 {
		err = &d.errs[0]
	}
	if err != nil {
		panic(fmt.Errorf("rally: invalid %s %s on %s: %v", name, raw, where, err))
	}
	return []byte(raw)
}

func jsonValue(where string, t reflect.Type, name, raw string) string {
	t = deref(t)
	k := kindOf(t)
	if k == kindSlice {
		if strings.HasPrefix(raw, "[") {
			panic(fmt.Errorf("rally: invalid %s %s on %s: list values are comma-separated", name, raw, where))
		}
		k = kindOf(deref(t.Elem()))
		items := splitCSV(raw)
		if k == kindString || k == kindText {
			for i, item := range items {
				items[i] = jsonQuote(item)
			}
		}
		raw = "[" + strings.Join(items, ",") + "]"
	} else if k == kindString || k == kindText {
		raw = jsonQuote(raw)
	}
	if k == kindSlice || k == kindMap || k == kindStruct {
		panic(fmt.Errorf("rally: %s is not supported on %s: only on strings, numbers, booleans and their slices", name, where))
	}
	return raw
}

func jsonQuote(s string) string { return string(appendString(nil, s)) }

var ruleTags = []string{
	"pattern", "enum", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minLength", "maxLength", "minItems", "maxItems", "uniqueItems", "minProperties", "maxProperties",
}

var (
	docTags            = []string{"description", "example"}
	fieldTags          = slices.Concat([]string{"json", "default"}, docTags, ruleTags)
	bodyTags           = slices.Concat([]string{"required"}, ruleTags)
	responseHeaderTags = slices.Concat([]string{"header"}, docTags)
	ignoredTags        = []string{"json"}
	knownTags          = slices.Concat([]string{"path", "query", "header", "required"}, fieldTags)
)

var unsupportedTags = map[string]string{
	"format":               "use pattern; time.Time and uuid.UUID fields are documented with their format",
	"nullable":             "a pointer accepts null, unless omitempty",
	"cookie":               "read cookies in a middleware",
	"additionalProperties": "unknown properties are always rejected",
	"contentType":          "bodies are application/json",
	"timeFormat":           "time.Time values are RFC 3339, use a string for another format",
	"readOnly":             "give requests and responses their own types",
	"writeOnly":            "give requests and responses their own types",
	"patternDescription":   "the error message shows the pattern",
	"const":                "use enum with one value", "dependentRequired": "",
	"hidden": "", "encoding": "", "deprecated": "deprecate the whole operation with Operation.Deprecated",
	"doc":      "use description",
	"examples": "use example, which takes one value",
}

func checkTags(where string, tag reflect.StructTag, allowed []string) {
	for _, name := range tagNames(tag) {
		if slices.Contains(knownTags, name) {
			if !slices.Contains(allowed, name) {
				panic(fmt.Errorf("rally: the %s tag has no effect on %s", name, where))
			}
			switch v := tag.Get(name); {
			case name == "required" && v != "true" && v != "false":
				panic(fmt.Errorf("rally: invalid required tag %q on %s: expected true or false", v, where))
			case name == "uniqueItems" && v != "true":
				panic(fmt.Errorf("rally: invalid uniqueItems tag %q on %s: expected true, or no tag", v, where))
			}
			continue
		}
		if _, ok := unsupportedTags[name]; ok {
			unsupportedTag(name, where)
		}
		for _, known := range knownTags {
			if closeTo(name, known) {
				panic(fmt.Errorf("rally: unknown tag %q on %s, did you mean %q?", name, where, known))
			}
		}
		for unsupported := range unsupportedTags {
			if closeTo(name, unsupported) {
				unsupportedTag(unsupported, where)
			}
		}
	}
}

func tagNames(tag reflect.StructTag) []string {
	var names []string
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			break
		}
		names = append(names, string(tag[:i]))
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		tag = tag[i+1:]
	}
	return names
}

func oneEditApart(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	if len(a) < len(b) {
		return a[i:] == b[i+1:]
	}
	return a[i+1:] == b[i+1:] ||
		i+1 < len(a) && a[i] == b[i+1] && a[i+1] == b[i] && a[i+2:] == b[i+2:]
}

func closeTo(name, known string) bool {
	return strings.EqualFold(name, known) || len(known) >= 6 && oneEditApart(name, known)
}

func unsupportedTag(name, where string) {
	msg := fmt.Sprintf("rally: the %s tag is not supported (%s)", name, where)
	if hint := unsupportedTags[name]; hint != "" {
		msg += ": " + hint
	}
	panic(errors.New(msg))
}
