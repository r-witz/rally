// Package rally builds strict JSON HTTP APIs on net/http from typed Go
// handlers, and generates their OpenAPI 3.1.2 document from the same code.
//
// Each operation is analyzed once, when it is registered: misuse panics there
// with the fix in the message, and each type gets functions that bind its
// parameters, decode and validate its JSON body in a single pass, and encode
// its response. Requests that break the rules get an RFC 9457 problem listing
// every error. The document says exactly what is enforced.
//
//	mux := http.NewServeMux()
//	api := rally.New(mux, rally.DefaultConfig("My API", "1.0.0"))
//
//	rally.Register(api, rally.Operation{
//		Method:      http.MethodGet,
//		Path:        "/greetings/{name}",
//		OperationID: "get-greeting", // names the method of generated clients
//	}, func(ctx context.Context, in *struct {
//		Name string `path:"name" maxLength:"30"`
//	}) (*struct{ Body Greeting }, error) {
//		return &struct{ Body Greeting }{Greeting{Message: "Hello, " + in.Name}}, nil
//	})
//
//	http.ListenAndServe(":8888", mux)
//
// The document is served at /openapi.json (with a reference UI at /docs) and
// is available as API.OpenAPIJSON, e.g. to check it into the repository.
package rally
