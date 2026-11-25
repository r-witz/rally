package rally

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode"
)

const schemaRefPrefix = "#/components/schemas/"

type registry struct {
	schemas map[string]*schema
	types   map[string]reflect.Type
	uses    map[reflect.Type]typeUse
	claims  map[reflect.Type]typeUse
	input   bool
	hidden  bool
}

type typeUse struct {
	input bool
	route string
}

func newRegistry() *registry {
	return &registry{schemas: map[string]*schema{}, types: map[string]reflect.Type{}, uses: map[reflect.Type]typeUse{}}
}

func (r *registry) claimBody(t reflect.Type, route, where string, bounded, embedded bool) {
	for k := kindOf(t); k == kindPointer || k == kindSlice || k == kindMap; k = kindOf(t) {
		if r.input && k == kindSlice && !bounded {
			panic(fmt.Errorf("rally: %s is a list in a request body without maxItems: bound it (wrap a list of lists or a map of lists in a struct)", where))
		}
		if r.input && k == kindMap && !bounded {
			panic(fmt.Errorf("rally: %s is a map in a request body without maxProperties: bound it (wrap a list of maps or a map of maps in a struct)", where))
		}
		bounded = bounded && k == kindPointer
		t = t.Elem()
	}
	if kindOf(t) != kindStruct {
		return
	}
	if t.Name() != "" {
		use, ok := r.uses[t]
		if !ok {
			use, ok = r.claims[t]
		}
		if ok && use.input != r.input {
			in, out := route, use.route
			if !r.input {
				in, out = use.route, route
			}
			panic(fmt.Errorf("rally: %s is used in the request body of %s and in the response body of %s: "+
				"requests and responses need their own types (fields can be shared through a struct embedded in both, except fields of struct type)", t, in, out))
		}
		if ok {
			return
		}
		r.claims[t] = typeUse{r.input, route}
	}
	for _, f := range fieldsOf(t) {
		responseOnly := !r.input && len(f.index) == 1 && (t.Name() != "" || !embedded)
		if responseOnly && f.def != nil {
			panic(fmt.Errorf("rally: the default of %s.%s has no effect: %s is only used in responses", t, f.goName, t))
		}
		if responseOnly && (withoutEnum(f.rules) != nil || withoutEnum(f.items) != nil) {
			panic(fmt.Errorf("rally: the validation tags of %s.%s have no effect: %s is only used in responses, where only enum is checked", t, f.goName, t))
		}
		r.claimBody(f.typ, route, t.String()+"."+f.goName, sizeBounded(f.rules), embedded || len(f.index) > 1)
	}
}

func (r *registry) schema(t reflect.Type, hint string) *schema {
	switch kindOf(t) {
	case kindText:
		switch t {
		case timeType:
			return &schema{Type: "string", Format: "date-time"}
		case uuidType:
			return &schema{Type: "string", Format: "uuid"}
		}
		return &schema{Type: "string"}
	case kindString:
		return &schema{Type: "string"}
	case kindBool:
		return &schema{Type: "boolean"}
	case kindInt, kindUint:
		return intSchema(t)
	case kindFloat:
		if t.Kind() == reflect.Float32 {
			return &schema{Type: "number", Format: "float"}
		}
		return &schema{Type: "number", Format: "double"}
	case kindPointer:
		return r.schema(t.Elem(), hint)
	case kindSlice:
		return &schema{Type: "array", Items: r.itemSchema(t.Elem())}
	case kindMap:
		return &schema{Type: "object", AdditionalProperties: r.itemSchema(t.Elem())}
	}
	return r.structRef(t, hint)
}

func intSchema(t reflect.Type) *schema {
	bits, signed := t.Bits(), kindOf(t) == kindInt
	s := &schema{Type: "integer", Format: "int64"}
	if bits < 32 || bits == 32 && signed {
		s.Format = "int32"
	}
	if !signed && bits == 64 {
		s.Format = ""
	}
	lo, hi := intBounds(t)
	s.Minimum, s.Maximum = json.Number(lo), json.Number(hi)
	return s
}

func (r *registry) itemSchema(t reflect.Type) *schema {
	return r.valueSchema(t, "", kindOf(t) == kindPointer, nil, nil)
}

func (r *registry) valueSchema(t reflect.Type, hint string, nullable bool, rules, items *rules) *schema {
	s := r.schema(t, hint)
	if nullable {
		if s.Ref != "" {
			s = &schema{AnyOf: []*schema{s, {Type: "null"}}}
		} else {
			s.Nullable = true
		}
	}
	applyRules(s, rules)
	if s.Items != nil {
		applyRules(s.Items, items)
	}
	return s
}

func applyRules(s *schema, r *rules) {
	if r == nil {
		return
	}
	if r.minimum != nil && (s.Minimum == "" || r.minimum.dec.cmp(decimalOf(string(s.Minimum))) > 0) {
		s.Minimum = json.Number(r.minimum.text)
	}
	if r.maximum != nil && (s.Maximum == "" || r.maximum.dec.cmp(decimalOf(string(s.Maximum))) < 0) {
		s.Maximum = json.Number(r.maximum.text)
	}
	s.MinLength, s.MaxLength, s.Pattern = r.minLength, r.maxLength, r.pattern
	s.MinItems, s.MaxItems, s.UniqueItems = r.minItems, r.maxItems, r.unique != nil
	s.MinProperties, s.MaxProperties = r.minProperties, r.maxProperties
	if r.exclusiveMinimum != nil {
		s.ExclusiveMinimum = json.Number(r.exclusiveMinimum.text)
	}
	if r.exclusiveMaximum != nil {
		s.ExclusiveMaximum = json.Number(r.exclusiveMaximum.text)
	}
	if r.multipleOf != nil {
		s.MultipleOf = json.Number(r.multipleOf.text)
	}
	if r.enum.values != nil {
		s.Enum = r.enum.values
		if s.Nullable {
			s.Enum = append(slices.Clip(r.enum.values), nil)
		}
	}
}

func (r *registry) structRef(t reflect.Type, hint string) *schema {
	name := schemaName(t)
	switch {
	case name == "" && hint == "":
		return r.structSchema(t)
	case name == "":
		name = pascal(hint)
	}
	r.add(name, t)
	return &schema{Ref: schemaRefPrefix + name}
}

func (r *registry) add(name string, t reflect.Type) {
	r.claim(name, t)
	if _, ok := r.schemas[name]; !ok {
		s := &schema{}
		r.schemas[name] = s
		*s = *r.structSchema(t)
	}
}

func (r *registry) claim(name string, t reflect.Type) {
	if !r.hidden && !validSchemaName(name) {
		panic(fmt.Errorf("rally: schema name %q of %s is not valid in OpenAPI, which allows ASCII letters, digits, \".\", \"_\" and \"-\": "+
			"rename the type, or the operation of an anonymous body", name, t))
	}
	if other, ok := r.types[name]; ok && other != t {
		panic(fmt.Errorf("rally: schema name %q is used by both %s and %s, rename one of them", name, other, t))
	}
	r.types[name] = t
}

func (r *registry) structSchema(t reflect.Type) *schema {
	s := &schema{Type: "object", Properties: map[string]*schema{}, AdditionalProperties: false}
	for _, f := range fieldsOf(t) {
		s.Properties[f.name] = r.fieldSchema(f)
		s.propertyOrder = append(s.propertyOrder, f.name)
		if r.input && f.required || !r.input && omitFunc(f) == nil {
			s.Required = append(s.Required, f.name)
		}
	}
	return s
}

func (r *registry) fieldSchema(f *field) *schema {
	rules, items, def := f.rules, f.items, f.def
	if !r.input {
		rules, items, def = enumOnly(f.rules), enumOnly(f.items), nil
	}
	s := r.valueSchema(f.typ, "", f.nullable, rules, items)
	if def != nil {
		s.Default = json.RawMessage(def)
	}
	s.Description = f.description
	if f.example != nil {
		s.Examples = []any{json.RawMessage(f.example)}
	}
	return s
}

func validSchemaName(name string) bool {
	for i := 0; i < len(name); i++ {
		if c := name[i]; !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return name != ""
}

func schemaName(t reflect.Type) string {
	name := t.Name()
	i := strings.IndexByte(name, '[')
	if i < 0 {
		return name
	}
	var b strings.Builder
	b.WriteString(name[:i])
	for part := range strings.FieldsFuncSeq(name[i:], func(r rune) bool {
		return r == '[' || r == ']' || r == ',' || r == ' ' || r == '*'
	}) {
		if j := strings.LastIndexByte(part, '.'); j >= 0 {
			part = part[j+1:]
		}
		b.WriteString(pascal(part))
	}
	return b.String()
}

func pascal(s string) string {
	var b strings.Builder
	upper := true
	for _, c := range s {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) {
			upper = true
			continue
		}
		if upper {
			c = unicode.ToUpper(c)
			upper = false
		}
		b.WriteRune(c)
	}
	return b.String()
}
