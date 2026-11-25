package rally

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Operation describes an API operation: its route, options and
// documentation.
type Operation struct {
	// Method is the HTTP method (GET, POST...).
	Method string

	// Path is an http.ServeMux pattern path: "/notes/{id}" for a path
	// parameter, "/files/{path...}" for the rest of the path, "/{$}" for the
	// root alone. It cannot end with "/" unless the operation is hidden.
	Path string

	// OperationID is required and unique: generated clients name their
	// methods after it, so it is written by hand to stay stable.
	OperationID string

	// Summary, Description, Tags and Deprecated document the operation.
	Summary     string
	Description string
	Tags        []string
	Deprecated  bool

	// Security overrides Config.Security in the document. An empty,
	// non-nil list documents an operation that needs no authentication.
	Security []map[string][]string

	// DefaultStatus is the success status when the output has no Status
	// field (or it is 0). Defaults to 200 with a body or for HEAD, 204
	// otherwise.
	DefaultStatus int

	// Statuses lists the other success statuses the handler sets through the
	// output's Status field. They are documented with the same body, except
	// 204, 205 and 304, which are sent without one.
	Statuses []int

	// MaxBodyBytes overrides Config.MaxBodyBytes for this operation.
	MaxBodyBytes int64

	// Errors lists the error statuses (400 to 599) that handlers and
	// middlewares send, to document them. Those rally sends itself are
	// documented anyway.
	Errors []int

	// Hidden operations are served but not documented.
	Hidden bool

	// Middlewares wrap this operation's handler (outermost first).
	Middlewares []func(http.Handler) http.Handler
}

// Register adds an operation, the only way to do so. The handler receives the
// decoded and validated input and returns the output, or an error (see
// ErrorModel). Method, Path and OperationID are required.
//
// The input struct may have path, query and header parameters
// (`path:"id"`...) and a Body field decoded from JSON. The output struct may
// have a Status int field, response headers (`header:"ETag"`) and a Body
// field encoded as JSON.
//
// Everything is analyzed here, once: misconfigurations panic at startup, and
// requests only go through precompiled functions.
func Register[I, O any](api *API, op Operation, handler func(context.Context, *I) (*O, error)) {
	p := api.compile(&op, reflect.TypeFor[I](), reflect.TypeFor[O]())
	api.handle(&op, &operationHandler[I, O]{plan: p, handler: handler})
}

func (a *API) handle(op *Operation, h http.Handler) {
	for i := len(op.Middlewares) - 1; i >= 0; i-- {
		h = op.Middlewares[i](h)
	}
	for i := len(a.middlewares) - 1; i >= 0; i-- {
		h = a.middlewares[i](h)
	}
	a.mux.Handle(op.Method+" "+op.Path, h)
}

type headerWriter struct {
	key   string
	index []int
}

type plan struct {
	params []paramBinder

	hasBody      bool
	bodyIndex    int
	bodyDec      decFunc
	bodyRequired bool
	maxBody      int64

	securityKeys []string

	defaultStatus int
	statuses      []int
	hasStatus     bool
	statusIndex   int
	headers       []headerWriter
	hasOut        bool
	outIndex      int
	outEnc        encFunc
}

var registration sync.Mutex

var docMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func (a *API) compile(op *Operation, in, out reflect.Type) *plan {
	registration.Lock()
	defer registration.Unlock()
	if op.Method == "" || op.Path == "" {
		panic("rally: operation method and path are required")
	}
	op.Method = strings.ToUpper(op.Method)
	op.Path = a.prefix + op.Path
	route := op.Method + " " + op.Path
	if in.Kind() != reflect.Struct || out.Kind() != reflect.Struct {
		panic(fmt.Errorf("rally: %s: input and output must be structs", route))
	}
	if op.OperationID == "" {
		panic(fmt.Errorf("rally: %s: OperationID is required", route))
	}
	if prev, ok := a.operationIDs[op.OperationID]; ok {
		panic(fmt.Errorf("rally: %s: OperationID %q is already used by %s", route, op.OperationID, prev))
	}
	method := strings.ToLower(op.Method)
	if !op.Hidden && !slices.Contains(docMethods, method) {
		panic(fmt.Errorf("rally: %s: OpenAPI cannot document the method %s, hide the operation", route, op.Method))
	}
	if !op.Hidden && !strings.HasPrefix(op.Path, "/") {
		panic(fmt.Errorf("rally: %s: OpenAPI cannot document a path that does not start with /, hide the operation", route))
	}
	if !op.Hidden && strings.HasSuffix(op.Path, "/") {
		panic(fmt.Errorf("rally: %s matches every path below it: end it with {$} to match only this path, or hide the operation", route))
	}
	if op.DefaultStatus != 0 && !successStatus(op.DefaultStatus) || slices.ContainsFunc(op.Statuses, func(c int) bool { return !successStatus(c) }) {
		panic(fmt.Errorf("rally: %s: DefaultStatus and Statuses must be between 200 and 399", route))
	}
	if slices.ContainsFunc(op.Errors, func(c int) bool { return c < 400 || c > 599 }) {
		panic(fmt.Errorf("rally: %s: Errors must be between 400 and 599", route))
	}

	reg := a.registry
	if op.Hidden {
		reg = newRegistry()
		reg.hidden, reg.uses = true, a.registry.uses
	}
	reg.claims = map[reflect.Type]typeUse{}
	doc := &opDoc{
		OperationID: op.OperationID, Summary: op.Summary, Description: op.Description, Tags: op.Tags,
		Deprecated: op.Deprecated, Security: securityDoc(route, op.Security, a.config.SecuritySchemes), Responses: map[string]*response{},
	}
	security := op.Security
	if security == nil {
		security = a.config.Security
	}
	p := &plan{maxBody: op.MaxBodyBytes, securityKeys: queryKeys(security, a.config.SecuritySchemes)}
	if p.maxBody == 0 {
		p.maxBody = a.config.MaxBodyBytes
	}
	p.compileInput(reg, op, doc, in)
	p.compileOutput(reg, op, doc, out)
	documentErrors(reg, op, doc, p)
	if !op.Hidden {
		path := openAPIPath(op.Path)
		template := pathTemplate(path)
		if other, ok := a.templates[template]; ok && other != path {
			panic(fmt.Errorf("rally: %s: OpenAPI cannot tell %s and %s apart, which differ only by parameter names: use the same names", route, path, other))
		}
		a.templates[template] = path
		if other := a.doc.Paths[path][method]; other != nil {
			panic(fmt.Errorf("rally: %s: OpenAPI documents it as %s %s, like operation %q", route, op.Method, path, other.OperationID))
		}
		if a.doc.Paths[path] == nil {
			a.doc.Paths[path] = map[string]*opDoc{}
		}
		a.doc.Paths[path][method] = doc
		a.spec = nil
	}
	maps.Copy(reg.uses, reg.claims)
	a.operationIDs[op.OperationID] = route
	return p
}

func successStatus(c int) bool { return c >= 200 && c <= 399 }

func bodiless(status int) bool {
	return status == http.StatusNoContent || status == http.StatusResetContent || status == http.StatusNotModified
}

func openAPIPath(p string) string {
	return strings.ReplaceAll(strings.ReplaceAll(p, "...}", "}"), "{$}", "")
}

func pathTemplate(path string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(path, '{')
		j := strings.IndexByte(path, '}')
		if i < 0 || j < i {
			b.WriteString(path)
			return b.String()
		}
		b.WriteString(path[:i])
		b.WriteString("{}")
		path = path[j+1:]
	}
}

func queryKeys(reqs []map[string][]string, schemes map[string]*SecurityScheme) []string {
	var keys []string
	for _, req := range reqs {
		for name := range req {
			if s := schemes[name]; s.Type == "apiKey" && s.In == "query" && !slices.Contains(keys, s.Name) {
				keys = append(keys, s.Name)
			}
		}
	}
	return keys
}

func securityDoc(where string, reqs []map[string][]string, schemes map[string]*SecurityScheme) []map[string][]string {
	if reqs == nil {
		return nil
	}
	doc := make([]map[string][]string, len(reqs))
	for i, req := range reqs {
		doc[i] = make(map[string][]string, len(req))
		for name, scopes := range req {
			if schemes[name] == nil {
				panic(fmt.Errorf("rally: %s uses the security scheme %q, which is not in Config.SecuritySchemes", where, name))
			}
			doc[i][name] = append([]string{}, scopes...)
		}
	}
	return doc
}

func (p *plan) compileInput(reg *registry, op *Operation, doc *opDoc, t reflect.Type) {
	reg.input = true
	defer func() { reg.input = false }()
	walkFields(t, func(f reflect.StructField, index []int) bool {
		top := len(index) == 1
		if in, _ := paramLocation(f); in != 0 {
			b, d := newParamBinder(reg, op, f, index)
			for _, other := range p.params {
				if other.in == b.in && (other.name == b.name || b.key != "" && other.key == b.key) {
					panic(fmt.Errorf("rally: %s parameter %q of %s %s is declared twice", inNames[b.in], b.name, op.Method, op.Path))
				}
			}
			p.params = append(p.params, b)
			doc.Parameters = append(doc.Parameters, d)
			return false
		}
		if f.Name != "Body" || !top {
			return strayField(t, f)
		}
		where := "the body of " + op.Method + " " + op.Path
		rules, items := parseRules(where, f.Tag, f.Type, bodyTags)
		pointer := f.Type.Kind() == reflect.Pointer
		if !pointer && f.Tag.Get("required") == "false" {
			panic(fmt.Errorf("rally: %s can only be optional if Body is a pointer", where))
		}
		reg.claimBody(f.Type, op.Method+" "+op.Path, where, sizeBounded(rules), false)
		p.hasBody, p.bodyIndex = true, index[0]
		p.bodyRequired = !pointer || f.Tag.Get("required") == "true"
		nullable := !p.bodyRequired
		p.bodyDec = compileDecoder(f.Type, rules, items, nullable)
		doc.RequestBody = &requestBody{
			Required: p.bodyRequired,
			Content:  jsonContent(reg.valueSchema(f.Type, op.OperationID+"Request", nullable, rules, items)),
		}
		return false
	})
	for _, name := range pathParams(op.Path) {
		found := false
		for _, b := range p.params {
			found = found || (b.in == inPath && b.name == name)
		}
		if !found {
			doc.Parameters = append(doc.Parameters, &param{Name: name, In: "path", Required: true, Schema: &schema{Type: "string"}})
		}
	}
}

func walkFields(t reflect.Type, fn func(f reflect.StructField, index []int) bool) {
	var walk func(t reflect.Type, base []int)
	walk = func(t reflect.Type, base []int) {
		for i := range t.NumField() {
			f := t.Field(i)
			index := append(slices.Clip(base), i)
			if fn(f, index) && f.Type.Kind() == reflect.Struct {
				walk(f.Type, index)
			}
		}
	}
	walk(t, nil)
}

func strayField(t reflect.Type, f reflect.StructField) bool {
	checkTags(t.String()+"."+f.Name, f.Tag, ignoredTags)
	if f.Anonymous && f.Type.Kind() == reflect.Struct {
		return true
	}
	if f.Anonymous && f.Type.Kind() == reflect.Pointer {
		panic(fmt.Errorf("rally: embedded pointer %s in %s is not supported, embed the struct by value", f.Type, t))
	}
	if f.IsExported() {
		panic(fmt.Errorf("rally: field %s of %s is not used: it is not Body, Status, a parameter or a header", f.Name, t))
	}
	return false
}

func pathParams(path string) []string {
	var names []string
	for {
		i := strings.IndexByte(path, '{')
		j := strings.IndexByte(path, '}')
		if i < 0 || j < i {
			return names
		}
		if name := strings.TrimSuffix(path[i+1:j], "..."); name != "$" {
			names = append(names, name)
		}
		path = path[j+1:]
	}
}

func (p *plan) compileOutput(reg *registry, op *Operation, doc *opDoc, t reflect.Type) {
	resp := &response{}
	walkFields(t, func(f reflect.StructField, index []int) bool {
		top := len(index) == 1
		where := t.String() + "." + f.Name
		switch name := f.Tag.Get("header"); {
		case name != "":
			checkTags(where, f.Tag, responseHeaderTags)
			key := checkHeaderName(name, where)
			if kindOf(f.Type) != kindString {
				panic(fmt.Errorf("rally: response header %s of %s %s must be a string: format its value in the handler", name, op.Method, op.Path))
			}
			if slices.ContainsFunc(p.headers, func(h headerWriter) bool { return h.key == key }) {
				panic(fmt.Errorf("rally: response header %s of %s %s is declared twice", name, op.Method, op.Path))
			}
			p.headers = append(p.headers, headerWriter{key: key, index: index})
			if resp.Headers == nil {
				resp.Headers = map[string]*header{}
			}
			s := &schema{Type: "string"}
			if example, ok := f.Tag.Lookup("example"); ok {
				s.Examples = []any{example}
			}
			resp.Headers[name] = &header{Description: f.Tag.Get("description"), Schema: s}
		case f.Name == "Status" && top:
			checkTags(where, f.Tag, nil)
			if f.Type.Kind() != reflect.Int {
				panic(fmt.Errorf("rally: %s.Status must be an int", t))
			}
			p.hasStatus, p.statusIndex = true, index[0]
		case f.Name == "Body" && top:
			checkTags(where, f.Tag, nil)
			reg.claimBody(f.Type, op.Method+" "+op.Path, where, false, false)
			p.hasOut, p.outIndex = true, index[0]
			p.outEnc = compileEncoder(f.Type)
			resp.Content = jsonContent(reg.valueSchema(f.Type, op.OperationID+"Response", f.Type.Kind() == reflect.Pointer, nil, nil))
		default:
			return strayField(t, f)
		}
		return false
	})
	p.defaultStatus = op.DefaultStatus
	if p.defaultStatus == 0 {
		p.defaultStatus = http.StatusOK
		if !p.hasOut && op.Method != http.MethodHead {
			p.defaultStatus = http.StatusNoContent
		}
	}
	if p.hasOut && bodiless(p.defaultStatus) {
		panic(fmt.Errorf("rally: %s %s: a %d response has no body, but %s has a Body", op.Method, op.Path, p.defaultStatus, t))
	}
	if len(op.Statuses) > 0 && !p.hasStatus {
		panic(fmt.Errorf("rally: %s %s: Statuses needs a Status field in %s", op.Method, op.Path, t))
	}
	if p.hasStatus && len(op.Statuses) == 0 {
		panic(fmt.Errorf("rally: %s.Status can only be the default status %d: list the statuses the handler sets in Operation.Statuses", t, p.defaultStatus))
	}
	p.statuses = slices.Clone(op.Statuses)
	for _, c := range append([]int{p.defaultStatus}, op.Statuses...) {
		r := *resp
		r.Description = http.StatusText(c)
		if bodiless(c) {
			r.Content = nil
		}
		doc.Responses[strconv.Itoa(c)] = &r
	}
}

func jsonContent(s *schema) map[string]*mediaType {
	return map[string]*mediaType{"application/json": {Schema: s}}
}

func documentErrors(reg *registry, op *Operation, doc *opDoc, p *plan) {
	errSchema := reg.schema(errorModelType, "")
	codes := append(slices.Clip(op.Errors), http.StatusBadRequest)
	if p.hasOut {
		codes = append(codes, http.StatusNotAcceptable)
	}
	if p.hasBody {
		codes = append(codes, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity)
	}
	codes = append(codes, http.StatusInternalServerError)
	for _, c := range codes {
		if doc.Responses[strconv.Itoa(c)] == nil {
			doc.Responses[strconv.Itoa(c)] = &response{Description: http.StatusText(c), Content: map[string]*mediaType{
				"application/problem+json": {Schema: errSchema},
			}}
		}
	}
}
