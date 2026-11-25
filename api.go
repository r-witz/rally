package rally

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Config configures an API and the top of its OpenAPI document.
type Config struct {
	// Info, Servers, Tags and ExternalDocs are copied to the document.
	Info         Info
	Servers      []*Server
	Tags         []*Tag
	ExternalDocs *ExternalDocs

	// Security lists the default security requirements of the operations,
	// by name of SecuritySchemes. Query parameters named by an apiKey scheme
	// are accepted on the operations that require it.
	Security        []map[string][]string
	SecuritySchemes map[string]*SecurityScheme

	// OpenAPIPath is where the OpenAPI document is served as JSON. Empty
	// keeps the API surface private: the document stays available through
	// API.OpenAPIJSON.
	OpenAPIPath string

	// DocsPath is where the HTML API reference is served, if OpenAPIPath is
	// set. Empty disables it.
	DocsPath string

	// MaxBodyBytes limits request bodies; operations can override it.
	// Zero means 1 MiB.
	MaxBodyBytes int64
}

const defaultMaxBodyBytes = 1 << 20

// DefaultConfig returns a configuration serving the document at
// /openapi.json and the reference at /docs.
func DefaultConfig(title, version string) Config {
	return Config{
		Info:         Info{Title: title, Version: version},
		OpenAPIPath:  "/openapi.json",
		DocsPath:     "/docs",
		MaxBodyBytes: defaultMaxBodyBytes,
	}
}

// API registers operations on a ServeMux and documents them.
type API struct {
	*apiCore
	prefix      string
	middlewares []func(http.Handler) http.Handler
}

type apiCore struct {
	mux      *http.ServeMux
	config   Config
	doc      *document
	registry *registry
	spec     []byte

	operationIDs map[string]string
	templates    map[string]string
}

// New creates an API registering its routes on mux.
//
// New also registers the pattern "/": requests that match no route get an
// error in the API's format instead of the mux's plain text, a 405 with the
// Allow header when the path exists with other methods, a 404 otherwise. The
// mux is then the API's alone: registering "/" again, or a second API,
// panics.
func New(mux *http.ServeMux, config Config) *API {
	if _, pattern := mux.Handler(&http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"}}); pattern == "/" {
		panic("rally: the mux already serves the pattern \"/\", which rally.New registers: give each API its own mux")
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = defaultMaxBodyBytes
	}
	reg := newRegistry()
	c := &apiCore{mux: mux, config: config, registry: reg, operationIDs: map[string]string{}, templates: map[string]string{}, doc: &document{
		OpenAPI: "3.1.2", Info: config.Info, Servers: config.Servers, Paths: map[string]map[string]*opDoc{},
		Components: components{Schemas: reg.schemas, SecuritySchemes: config.SecuritySchemes},
		Security:   securityDoc("Config.Security", config.Security, config.SecuritySchemes), Tags: config.Tags, ExternalDocs: config.ExternalDocs,
	}}
	api := &API{apiCore: c}
	if config.OpenAPIPath != "" {
		mux.HandleFunc("GET "+config.OpenAPIPath, api.serveSpec)
		if config.DocsPath != "" {
			mux.Handle("GET "+config.DocsPath, docsHandler(config.Info.Title, config.OpenAPIPath))
		}
	}
	mux.Handle("/", notFoundHandler(mux))
	return api
}

// WriteError writes err as an error response in the API's format, as when a
// handler returns it: an *ErrorModel (NewError, the ErrorXXX helpers) is sent
// with its status, any other error is logged and sent as a 500. Middlewares
// use it so that clients get every error in the same format.
func (a *API) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, err)
}

func notFoundHandler(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var allowed []string
		probe := *r
		for _, m := range probedMethods {
			probe.Method = m
			if _, p := mux.Handler(&probe); p != "" && p != "/" {
				allowed = append(allowed, m)
			}
		}
		if len(allowed) == 0 {
			writeErrorBody(w, Error404NotFound("no operation matches this path"))
			return
		}
		w.Header().Set("Allow", strings.Join(append(allowed, http.MethodOptions), ", "))
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeErrorBody(w, NewError(http.StatusMethodNotAllowed, "method not allowed, see the Allow header"))
	})
}

var probedMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, "QUERY",
}

// Group returns an API registering operations under prefix and wrapped by
// middlewares (outermost first), sharing the same document.
func (a *API) Group(prefix string, middlewares ...func(http.Handler) http.Handler) *API {
	return &API{
		apiCore:     a.apiCore,
		prefix:      a.prefix + prefix,
		middlewares: append(slices.Clip(a.middlewares), middlewares...),
	}
}

// OpenAPIJSON returns the OpenAPI document as JSON, e.g. to write it to a
// file and check it in CI.
func (a *API) OpenAPIJSON() ([]byte, error) {
	registration.Lock()
	defer registration.Unlock()
	if a.spec == nil {
		b, err := json.Marshal(a.doc)
		if err != nil {
			return nil, err
		}
		a.spec = b
	}
	return a.spec, nil
}

func (a *API) serveSpec(w http.ResponseWriter, r *http.Request) {
	b, err := a.OpenAPIJSON()
	if err != nil {
		http.Error(w, "cannot render the OpenAPI document", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

const scalarScript = "https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.75.1/dist/browser/standalone.js"

const scalarConfig = `{"showDeveloperTools":"never","agent":{"disabled":true},"mcp":{"disabled":true}}`

func docsHandler(title, specPath string) http.Handler {
	body := []byte(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="referrer" content="no-referrer">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>` + html.EscapeString(title) + `</title>
  </head>
  <body>
    <script id="api-reference" data-url="` + html.EscapeString(specPath) + `" data-configuration="` + html.EscapeString(scalarConfig) + `"></script>
    <script src="` + scalarScript + `" crossorigin integrity="sha384-G+2Q2i9l3PPfmYkk0b4BAIUrs7xb/DNaTL1wCYYab4c9dIKlCyiqzC0Qx3eq9Wf6"></script>
  </body>
</html>`)
	const csp = "default-src 'none'; base-uri 'none'; connect-src 'self'; form-action 'none'; frame-ancestors 'none'; " +
		"sandbox allow-same-origin allow-scripts allow-popups allow-popups-to-escape-sandbox allow-downloads; " +
		"script-src " + scalarScript + "; style-src 'unsafe-inline'; font-src https://fonts.scalar.com"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", csp)
		_, _ = w.Write(body)
	})
}
