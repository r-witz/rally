package spec

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "regenerate the generated test files")

// TestGenerateSchemaSuite turns the JSON-Schema-Test-Suite cases into Go
// types (zz_schemasuite_test.go), since rally reads its schemas from Go
// types. A case whose schema cannot be expressed as a Go type with rally tags
// is kept with the reason, so that the skipped share stays visible.
//
//	go test -run TestGenerateSchemaSuite -update
func TestGenerateSchemaSuite(t *testing.T) {
	if !*update {
		t.Skip("run with -update to regenerate")
	}
	files, err := filepath.Glob("testdata/JSON-Schema-Test-Suite/tests/*.json")
	if err != nil {
		t.Fatal(err)
	}
	optional, _ := filepath.Glob("testdata/JSON-Schema-Test-Suite/tests/optional/*.json")
	files = append(files, optional...)
	sort.Strings(files)
	g := &generator{}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Tests []struct {
				Description   string
				Compatibility string
				Tags          []string
				Schema        json.RawMessage
				Tests         []struct {
					Description string
					Data        json.RawMessage
					Valid       bool
				}
			}
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		name := strings.TrimPrefix(path, "testdata/JSON-Schema-Test-Suite/tests/")
		for _, c := range f.Tests {
			if !compatible(c.Compatibility, 2020) {
				continue
			}
			sc := genCase{file: name, desc: c.Description}
			var schema any
			dec := json.NewDecoder(bytes.NewReader(c.Schema))
			dec.UseNumber()
			if err := dec.Decode(&schema); err != nil {
				t.Fatal(err)
			}
			typ, jt, reason := "", "", ""
			switch {
			case slices.Contains(c.Tags, "format-annotation"):
				reason = "format is asserted (format-assertion behaviour), not an annotation"
			default:
				typ, jt, reason = g.root(schema)
			}
			sc.skip = reason
			if reason == "" {
				sc.typ = typ
			}
			for _, tc := range c.Tests {
				st := genTest{desc: tc.Description, data: string(tc.Data), valid: tc.Valid}
				if reason == "" && jt != "" {
					if it := instanceType(tc.Data); it != jt && !(jt == "number" && it == "integer") {
						st.skip = "the schema has no type and accepts a " + it + "; a Go field holds one type"
					}
				}
				if reason == "" && st.skip == "" && g.openObjects[typ] && tc.Valid && hasExtraProperties(schema, tc.Data) {
					st.skip = "objects are closed (profile): the instance has a property the schema does not declare"
				}
				sc.tests = append(sc.tests, st)
			}
			g.cases = append(g.cases, sc)
		}
	}
	src := g.source()
	out, err := format.Source(src)
	if err != nil {
		os.WriteFile("zz_schemasuite_test.go", src, 0o644)
		t.Fatalf("gofmt: %v", err)
	}
	if err := os.WriteFile("zz_schemasuite_test.go", out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func compatible(c string, version int) bool {
	if c == "" {
		return true
	}
	ok := true
	for _, con := range strings.Split(c, ",") {
		op := ""
		switch {
		case strings.HasPrefix(con, "<="):
			op, con = "<=", con[2:]
		case strings.HasPrefix(con, "="):
			op, con = "=", con[1:]
		}
		v, _ := strconv.Atoi(con)
		switch op {
		case "":
			ok = version >= v
		case "<=":
			ok = version <= v
		case "=":
			ok = version == v
		}
		if version <= v {
			break
		}
	}
	return ok
}

func instanceType(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	_ = dec.Decode(&v)
	return jsonType(v)
}

func jsonType(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err == nil && f == math.Trunc(f) && !math.IsInf(f, 0) {
			return "integer"
		}
		return "number"
	}
	return "unknown"
}

func hasExtraProperties(schema any, raw json.RawMessage) bool {
	var v any
	_ = json.Unmarshal(raw, &v)
	var walk func(s any, v any) bool
	walk = func(s any, v any) bool {
		sm, _ := s.(map[string]any)
		switch v := v.(type) {
		case map[string]any:
			props, _ := sm["properties"].(map[string]any)
			if _, isMap := sm["additionalProperties"].(map[string]any); isMap {
				return false
			}
			for k, x := range v {
				ps, ok := props[k]
				if !ok {
					return true
				}
				if walk(ps, x) {
					return true
				}
			}
		case []any:
			for _, x := range v {
				if walk(sm["items"], x) {
					return true
				}
			}
		}
		return false
	}
	return walk(schema, v)
}

type genTest struct {
	desc, data string
	valid      bool
	skip       string
}

type genCase struct {
	file, desc, typ, skip string
	tests                 []genTest
}

type generator struct {
	cases       []genCase
	decls       []string
	n           int
	imports     map[string]bool
	openObjects map[string]bool
}

// ignored keywords are annotations, or unknown keywords, which JSON Schema
// says to ignore.
var ignoredKeywords = []string{"$comment", "title", "description", "examples", "default", "unknown-keyword", "unknown/keyword", "deprecated", "readOnly", "writeOnly"}

// tagKeywords are the keywords rally reads from a struct tag of the same name.
var tagKeywords = []string{"minLength", "maxLength", "pattern", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "enum", "minItems", "maxItems", "uniqueItems", "minProperties", "maxProperties"}

func (g *generator) root(schema any) (typ, jsonType, reason string) {
	g.n++
	name := fmt.Sprintf("s%04d", g.n)
	ft, tags, jt, reason := g.value(schema, name+"V")
	if reason != "" {
		return "", "", reason
	}
	g.decls = append(g.decls, fmt.Sprintf("type %s struct { V %s `json:\"v\"%s` }", name, ft, tags))
	if g.openObjects[name+"V"] {
		g.openObjects[name] = true
	}
	return name, jt, ""
}

// value returns the Go type and rally tags for a schema, and the JSON type
// the schema implies when it has no "type" (instances of other types are then
// valid for the schema but cannot be held by the Go field).
func (g *generator) value(schema any, name string) (goType, tags, impliedType, reason string) {
	if g.openObjects == nil {
		g.openObjects, g.imports = map[string]bool{}, map[string]bool{}
	}
	if b, ok := schema.(bool); ok {
		return "", "", "", fmt.Sprintf("boolean schema %v", b)
	}
	s, ok := schema.(map[string]any)
	if !ok {
		return "", "", "", "schema is not an object"
	}
	for _, k := range slices.Sorted(maps.Keys(s)) {
		if slices.Contains(ignoredKeywords, k) || slices.Contains(tagKeywords, k) {
			continue
		}
		switch k {
		case "type", "items", "properties", "required", "additionalProperties", "format":
			continue
		}
		return "", "", "", "keyword " + k + " not supported"
	}
	typ, _ := s["type"].(string)
	nullable := false
	if list, isList := s["type"].([]any); isList {
		if len(list) != 2 || list[1] != "null" {
			return "", "", "", "a list of types other than [T, \"null\"] is not supported"
		}
		typ, _ = list[0].(string)
		nullable = true
	}
	implied := ""
	if typ == "" {
		switch {
		case hasAny(s, "minLength", "maxLength", "pattern", "format"):
			typ = "string"
		case hasAny(s, "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf"):
			typ = "number"
		case hasAny(s, "items", "minItems", "maxItems", "uniqueItems"):
			typ = "array"
		case hasAny(s, "properties", "required", "additionalProperties", "minProperties", "maxProperties"):
			typ = "object"
		case hasAny(s, "enum"):
			typ = enumType(s["enum"])
		}
		if typ == "" {
			return "", "", "", "schema without type or typed keyword"
		}
		implied = typ
	}
	var tagParts []string
	for _, k := range tagKeywords {
		v, ok := s[k]
		if !ok || k == "uniqueItems" && v == false {
			continue
		}
		tv, r := tagValue(k, v, typ)
		if r != "" {
			return "", "", "", r
		}
		tagParts = append(tagParts, fmt.Sprintf("%s:%q", k, tv))
	}
	switch typ {
	case "string":
		goType = "string"
		if f, ok := s["format"].(string); ok {
			switch f {
			case "date-time":
				goType, g.imports["time"] = "time.Time", true
			case "uuid":
				goType, g.imports["uuid"] = "uuid.UUID", true
			default:
				return "", "", "", "format " + f + " not supported"
			}
			if len(tagParts) > 0 {
				return "", "", "", "string rules on format " + f + " are not supported"
			}
		}
	case "integer":
		goType = "int64"
	case "number":
		goType = "float64"
	case "boolean":
		goType = "bool"
	case "array":
		items, ok := s["items"]
		if !ok {
			return "", "", "", "array without items schema"
		}
		et, etags, _, r := g.value(items, name+"I")
		if r != "" {
			return "", "", "", r
		}
		if strings.HasPrefix(et, "[]") {
			return "", "", "", "nested arrays need a struct in between in rally"
		}
		if etags != "" {
			return "", "", "", "rules on array items need item tags"
		}
		goType = "[]" + et
		if _, ok := s["maxItems"]; !ok {
			tagParts = append(tagParts, `maxItems:"1000000"`)
		}
		if g.openObjects[et] {
			g.openObjects[name] = true
		}
	case "object":
		goType, reason = g.object(s, name)
		if reason != "" {
			return "", "", "", reason
		}
		if !strings.HasPrefix(goType, "map[") && hasAny(s, "minProperties", "maxProperties") {
			return "", "", "", "minProperties and maxProperties apply to maps only"
		}
		if _, ok := s["maxProperties"]; !ok && strings.HasPrefix(goType, "map[") {
			tagParts = append(tagParts, `maxProperties:"1000000"`)
		}
	default:
		return "", "", "", "type " + typ + " not supported"
	}
	if _, ok := s["format"]; ok && typ != "string" {
		return "", "", "", "format on a non-string type"
	}
	if len(tagParts) > 0 {
		tags = " " + strings.Join(tagParts, " ")
	}
	if nullable {
		goType = "*" + goType
	}
	return goType, tags, implied, ""
}

func (g *generator) object(s map[string]any, name string) (string, string) {
	props, _ := s["properties"].(map[string]any)
	var required []string
	if rs, ok := s["required"].([]any); ok {
		for _, r := range rs {
			rn, _ := r.(string)
			if _, ok := props[rn]; !ok {
				return "", "required property " + strconv.Quote(rn) + " without a schema"
			}
			required = append(required, rn)
		}
	}
	switch ap := s["additionalProperties"].(type) {
	case nil:
		g.openObjects[name] = true
	case bool:
		if ap {
			return "", "additionalProperties true is not supported (objects are closed)"
		}
	case map[string]any:
		if len(props) > 0 {
			return "", "additionalProperties with properties is not supported"
		}
		vt, vtags, _, r := g.value(ap, name+"M")
		if r != "" {
			return "", r
		}
		if vtags != "" {
			return "", "rules on map values are not supported"
		}
		return "map[string]" + vt, ""
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var fields []string
	for i, k := range keys {
		if strings.ContainsAny(k, "\",` \\") || k == "" || k == "-" {
			return "", "property name " + strconv.Quote(k) + " cannot be written in a json tag"
		}
		ft, tags, implied, r := g.value(props[k], fmt.Sprintf("%sP%d", name, i))
		if r != "" {
			return "", r
		}
		if implied != "" {
			return "", "property " + strconv.Quote(k) + " has no type"
		}
		if g.openObjects[ft] {
			g.openObjects[name] = true
		}
		opt := ",omitempty"
		if slices.Contains(required, k) {
			opt = ""
		}
		fields = append(fields, fmt.Sprintf("F%d %s `json:%q%s`", i, ft, k+opt, tags))
	}
	g.decls = append(g.decls, fmt.Sprintf("type %s struct {\n%s\n}", name, strings.Join(fields, "\n")))
	return name, ""
}

func hasAny(s map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := s[k]; ok {
			return true
		}
	}
	return false
}

func enumType(v any) string {
	values, _ := v.([]any)
	typ := ""
	for _, x := range values {
		t := jsonType(x)
		if t == "integer" {
			t = "number"
		}
		if typ != "" && t != typ {
			return ""
		}
		typ = t
	}
	if typ != "string" && typ != "number" {
		return ""
	}
	return typ
}

func tagValue(k string, v any, typ string) (string, string) {
	switch k {
	case "pattern":
		s, _ := v.(string)
		return s, ""
	case "uniqueItems":
		return "true", ""
	case "enum":
		values, _ := v.([]any)
		var parts []string
		for _, x := range values {
			switch x := x.(type) {
			case string:
				if strings.Contains(x, ",") {
					return "", "enum values with a comma cannot be written in a tag"
				}
				if typ != "string" {
					return "", "enum value of another type than the schema"
				}
				parts = append(parts, x)
			case json.Number:
				if typ != "number" && typ != "integer" {
					return "", "enum value of another type than the schema"
				}
				parts = append(parts, string(x))
			default:
				return "", "enum of " + jsonType(x) + " values is not supported"
			}
		}
		return strings.Join(parts, ","), ""
	}
	n, ok := v.(json.Number)
	if !ok {
		return "", k + " is not a number"
	}
	return string(n), ""
}

func (g *generator) source() []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by TestGenerateSchemaSuite; DO NOT EDIT.\n\npackage spec\n\n")
	if len(g.imports) > 0 {
		b.WriteString("import (\n")
		for _, imp := range []string{"time", "uuid"} {
			if g.imports[imp] {
				fmt.Fprintf(&b, "%q\n", imp)
			}
		}
		b.WriteString(")\n\n")
	}
	for _, d := range g.decls {
		b.WriteString(d + "\n\n")
	}
	b.WriteString("var schemaSuite = []schemaCase{\n")
	for _, c := range g.cases {
		fmt.Fprintf(&b, "{file: %q, desc: %q, ", c.file, c.desc)
		if c.skip != "" {
			fmt.Fprintf(&b, "skip: %q, ", c.skip)
		} else {
			fmt.Fprintf(&b, "register: register[%s], ", c.typ)
		}
		b.WriteString("tests: []schemaTest{\n")
		for _, t := range c.tests {
			fmt.Fprintf(&b, "{desc: %q, data: %q, valid: %v", t.desc, t.data, t.valid)
			if t.skip != "" {
				fmt.Fprintf(&b, ", skip: %q", t.skip)
			}
			b.WriteString("},\n")
		}
		b.WriteString("}},\n")
	}
	b.WriteString("}\n")
	return b.Bytes()
}
