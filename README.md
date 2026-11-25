<h1 align="center">
  <img src="docs/assets/logo.svg#gh-light-mode-only" alt="rally" width="320">
  <nostyle><img src="docs/assets/logo-dark.svg#gh-dark-mode-only" alt="rally" width="320"></nostyle>
</h1>

<p align="center"><b>Strict, fast JSON APIs for Go, with the OpenAPI document generated from your types.</b></p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/r-witz/rally"><img src="https://pkg.go.dev/badge/github.com/r-witz/rally.svg" alt="Go Reference"></a>
  <img src="https://img.shields.io/badge/go-1.27%2B-00ADD8" alt="Go 1.27+">
  <img src="https://img.shields.io/badge/dependencies-none-brightgreen" alt="Dependencies: none">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="License: MIT"></a>
</p>

rally turns typed Go handlers into `net/http` routes. Every request is checked against your types and tags before your handler runs, and the same types produce an OpenAPI 3.1.2 document that says exactly what the server enforces.

- **Strict.** Unknown fields, duplicate keys, wrong types, broken rules: rejected, every problem listed with a JSON Schema keyword and a JSON Pointer. Misusing the library panics at startup with the fix in the message, instead of being silently ignored.
- **Fast.** Each operation is compiled once, at registration. A body is decoded and validated in a single pass, without `unsafe`. In process, rally is 3 to 6 times faster than Huma and faster than `encoding/json` without any validation; over TCP, it serves 1.2 to 2.4 times Huma's requests per second ([benchmarks](#benchmarks)).
- **Documented.** Validation, documentation and generated clients come from one type model, so they cannot drift. Behaviour follows the specifications (JSON, JSON Schema, RFC 3339, HTTP, OpenAPI) row by row: see the [specification profile](docs/PROFILE.md).

```sh
go get github.com/r-witz/rally
```

## Contents

- [Quick start](#quick-start)
- [Operations](#operations) · [Inputs](#inputs) · [Outputs](#outputs) · [One type per direction](#one-type-per-direction)
- [Validation tags](#validation-tags) · [Documentation tags](#documentation-tags)
- [What is rejected](#what-is-rejected) · [Errors](#errors)
- [The OpenAPI document](#the-openapi-document)
- [Middlewares and groups](#middlewares-and-groups) · [Recipes](#recipes)
- [Limits](#limits) · [Benchmarks](#benchmarks) · [Why "rally"](#why-rally) · [License](#license)

## Quick start

```go
package main

import (
	"context"
	"net/http"

	"github.com/r-witz/rally"
)

type Greeting struct {
	Message string `json:"message" description:"A friendly greeting."`
}

type GreetingInput struct {
	Name string `path:"name" maxLength:"30" example:"Ada"`
}

type GreetingOutput struct {
	Body Greeting
}

func main() {
	mux := http.NewServeMux()
	api := rally.New(mux, rally.DefaultConfig("My API", "1.0.0"))

	rally.Register(api, rally.Operation{
		Method:      http.MethodGet,
		Path:        "/greetings/{name}",
		OperationID: "get-greeting",
	}, func(ctx context.Context, in *GreetingInput) (*GreetingOutput, error) {
		return &GreetingOutput{Body: Greeting{Message: "Hello, " + in.Name + "!"}}, nil
	})

	http.ListenAndServe(":8888", mux) // document at /openapi.json, reference at /docs
}
```

```console
$ curl localhost:8888/greetings/Ada
{"message":"Hello, Ada!"}
$ curl localhost:8888/greetings/$(printf 'a%.0s' {1..31})
{"title":"Bad Request","status":400,"detail":"validation failed","errors":[{"detail":"expected length <= 30","code":"maxLength","in":"path","parameter":"name"}]}
```

More examples, checked by `go test` (a notes API with a 201, a `Location` header, a 422 and a 404; a group with an authentication middleware), are in [example_test.go](example_test.go) and on [pkg.go.dev](https://pkg.go.dev/github.com/r-witz/rally#pkg-examples).

## Operations

A handler is `func(context.Context, *Input) (*Output, error)`, and `rally.Register` is the only way to add one.

| `rally.Operation` field | |
|---|---|
| `Method`, `Path`, `OperationID` | Required. `Path` is an `http.ServeMux` pattern: `/notes/{id}`, `/files/{path...}`, `/{$}`. The operation ID is unique; generated clients name their methods after it. |
| `Summary`, `Description`, `Tags`, `Deprecated` | Documentation. |
| `DefaultStatus` | The success status: 200 with a body or for `HEAD`, 204 otherwise. |
| `Statuses` | Other success statuses the handler may set through `Status` (200 to 399). |
| `Errors` | Error statuses your handler or middlewares send (400 to 599), to document them. |
| `Security` | Security requirements, overriding `Config.Security`; an empty list documents an operation without authentication. rally documents them; checking credentials is a middleware's job. |
| `MaxBodyBytes` | Overrides `Config.MaxBodyBytes` (1 MiB by default). |
| `Hidden` | Served but not documented. |
| `Middlewares` | `func(http.Handler) http.Handler`, outermost first. |

## Inputs

```go
type ListNotesInput struct {
	Owner string    `path:"owner"`                             // path parameter, always required
	Tags  []string  `query:"tag" maxItems:"10"`                // a list: ?tag=a&tag=b
	Limit int       `query:"limit" default:"20" maximum:"100"` // 20 when absent
	Since time.Time `query:"since"`                            // RFC 3339
	Trace string    `header:"X-Trace-Id"`                      // a header
}
```

- **Parameters** are strings, booleans, numbers, `time.Time`, `encoding.TextUnmarshaler` types (`uuid.UUID` from the standard library, `netip.Addr`...) and pointers to those (nil when absent). Query parameters can also be slices, sent as repeated keys (`style: form`, `explode: true`, the OpenAPI default). `required:"true"` makes a query or header parameter required.
- **Embedded structs** are searched for parameters too, e.g. a shared `Pagination`.
- **`Body T`** is a required JSON body. `Body *T` is optional: absent or `null`, it is nil. Tagged `required:"true"`, it must be present and not `null`.

```go
type CreateNoteInput struct {
	Body NoteInput // required JSON body
}
```
- **`PATCH`** bodies use pointers with `omitempty`: an absent property leaves its field nil.

## Outputs

```go
type GetNoteOutput struct {
	Status int    // optional: DefaultStatus or one of Operation.Statuses
	ETag   string `header:"ETag"` // a response header, omitted when empty
	Body   Note   // JSON body, never sent with 204, 205 and 304
}
```

A `Status` outside `DefaultStatus` and `Statuses` is logged and answered with a 500. A handler returns `nil, nil` only when the output has no `Body`.

Responses are not validated, with one exception: `enum`. A response field with an `enum` is checked when it is written, and a value outside the list is logged and answered with a 500, so that generated clients can rely on the list (TypeScript unions, Dart enums). Any other rule or a `default` in a response type panics at registration; rules that reach a response through a struct shared by embedding are left out of the response schema, except `enum`.

## One type per direction

A struct type, nested ones included, is used in request bodies or in response bodies, never both. A client can only send what the request type declares (no `id` or `role` sneaks in), and a response only contains what the response type declares (no `password` leaks).

Share fields by embedding a struct in both:

```go
type NoteInput struct {
	Title string `json:"title" minLength:"1" maxLength:"100"`
}

type Note struct {
	ID int `json:"id"`
	NoteInput
	CreatedAt time.Time `json:"created_at"`
}
```

A field of struct type cannot be shared this way: give it a type per direction too (`Author`, `AuthorInput`).

## Validation tags

| Tag | On | |
|---|---|---|
| `json` | body fields | Name, `omitempty`, `omitzero`. A body field is required unless it has `omitempty`, `omitzero` or a `default`. |
| `default` | body fields, query and header parameters | Applies when the value is absent. Lists are comma-separated: `default:"a,b"`. |
| `required:"true"` | query and header parameters, `Body *T` | Path parameters are always required. |
| `enum:"a,b"` | strings, numbers, text types | Also checked on responses (see [Outputs](#outputs)). |
| `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum` | numbers | JSON numbers, compared exactly. An integer must also fit its Go type. |
| `multipleOf` | numbers | Greater than 0, checked exactly: `0.1` divides `0.3`. |
| `minLength`, `maxLength` | strings | Counted in Unicode code points. |
| `pattern` | strings | ECMAScript regular expression, not anchored unless it says `^` and `$`. Write backslashes twice: `pattern:"^\\d{4}$"`. |
| `minItems`, `maxItems`, `uniqueItems:"true"` | lists | **Every list in a request body needs `maxItems`**: it bounds the memory a request can take. |
| `minProperties`, `maxProperties` | maps | **Every map in a request body needs `maxProperties`**, for the same reason. |

- On a list, the scalar tags (`enum`, `pattern`, `minLength`...) apply to its items.
- `uuid.UUID` and `time.Time` are checked by their type and take no string tag.
- Patterns are compiled at registration and matched in time proportional to the string. Not supported: lookaround, backreferences, named groups, Unicode properties other than a category or a script, repetition counts over 1000, and Go-only syntax such as `(?i)`.
- Anything else, such as rules across fields, belongs in the handler: `return nil, rally.Error422UnprocessableEntity("...")`.

## Documentation tags

| Tag | On | |
|---|---|---|
| `description` | body fields, parameters, response headers | Shown in the reference and in the comments of generated clients. |
| `example` | the same | One value, written like a `default` (lists comma-separated) and **checked against the field's rules at registration**, so an example can never be wrong. It becomes the JSON Schema `examples` of the value. |

```go
type NoteInput struct {
	Title    string `json:"title" maxLength:"100" description:"Shown in lists." example:"Groceries"`
	Priority string `json:"priority,omitempty" enum:"low,normal,high" default:"normal" description:"Sort order of the note."`
}
```

## What is rejected

**At registration**, with a panic that says how to fix it:

- misspelled tags (`minLenght`), unsupported ones (`format`, `nullable`, `readOnly`, `const`, `doc`, `examples`...), and tags placed where they have no effect. Tags of other libraries are left alone: `validate` and `binding` (Gin) do not validate;
- rules that no value can pass (`minimum:"10" maximum:"1"`, `minimum:"200"` on an `int8`, an enum value that breaks `maxLength`), and defaults or examples that break their rules;
- a list in a request body without `maxItems`, a map without `maxProperties`, a type used in both directions;
- exported fields of input and output structs that are not a parameter, `Body`, `Status` or a header; unexported fields with a parameter tag; invalid header names; the `Host` header;
- unsupported types: interfaces, `[]byte`, arrays, custom `MarshalJSON`, maps with non-string keys...;
- a path ending with `/` (unless hidden), two paths that differ only by parameter names, an unknown security scheme, statuses out of range, `Statuses` without a `Status` field and the reverse;
- in `rally.New`: a second API on the same mux.

**At request time**, with a problem that lists every error (up to 100):

| Status | When |
|---|---|
| `400` | A parameter is unknown, repeated (unless it is a list), malformed (`%zz`, invalid UTF-8) or breaks its type or rules; a header is sent twice; the JSON is malformed; the body is missing. |
| `406` | `Accept` excludes `application/json` on an operation that answers with a body. |
| `413` | The body is larger than `MaxBodyBytes`. |
| `415` | A body has no `Content-Type`, or one that is neither `application/json` nor `+json`. |
| `422` | The body breaks its schema (a `400` when a parameter is invalid too; all errors are listed). |

How values are read:

- **JSON** follows RFC 8259 and I-JSON (RFC 7493): unknown and duplicate properties and invalid UTF-8 are rejected, and names are case-sensitive. `null` is only accepted by a pointer without `omitempty`/`omitzero`.
- **Numbers** are compared by their exact decimal value. An integer is any number without a fractional part (`36`, `36.0`, `3.6e1`), as JSON Schema says.
- **Timestamps** are RFC 3339 (`t` and `z` may be lower case). A leap second, the offset `-00:00` and more than 9 fractional digits are rejected, as `time.Time` cannot hold them.
- **Query strings** follow the form-urlencoded rules OpenAPI requires: `+` is a space and `;` is data. `?name=` and an empty header are empty values, validated as such. Booleans are `true` or `false`; numbers use the JSON grammar (`2`, `2.0`, `2e0`; not `+2` or `02`).

## Errors

Every error is an [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem (`application/problem+json`). Each item of `errors` has a `code`: the JSON Schema keyword that failed, or `additionalProperties` (unknown property or query parameter), `duplicate`, `format` (a value its type rejects) or `syntax`. Problems in the body are located with a JSON Pointer, problems in a parameter with `in` and `parameter`. Rejected values are never echoed.

```json
{
  "title": "Unprocessable Entity",
  "status": 422,
  "detail": "validation failed",
  "errors": [
    {"detail": "expected length >= 3", "code": "minLength", "in": "body", "pointer": "/items/1/name"},
    {"detail": "expected required property email to be present", "code": "required", "in": "body", "pointer": "/email"}
  ]
}
```

| Source | Behaviour |
|---|---|
| **Handlers** | Return a `*rally.ErrorModel` to choose the response: `rally.Error404NotFound("...")` and the other helpers, or `rally.NewError(status, msg, details...)`. Its `Headers` are sent too (`WWW-Authenticate`...). Any other error is logged with `slog` and answered with a generic 500. |
| **Middlewares** | `api.WriteError(w, r, err)` behaves the same way. |
| **Panics** | Logged with their stack and answered with a 500. |
| **Unknown routes** | A 404, or a 405 with `Allow`; `OPTIONS` on an existing path gets a 204 with `Allow`. `rally.New` registers the pattern `/` for this, so the mux belongs to one API. |

## The OpenAPI document

- Served at `Config.OpenAPIPath` (`/openapi.json`), with a [Scalar](https://github.com/scalar/scalar) reference at `Config.DocsPath` (`/docs`). Set `DocsPath` to `""` to drop the reference, `OpenAPIPath` to serve neither. `api.OpenAPIJSON()` returns the document, e.g. to commit it and diff it in CI.
- The reference page loads Scalar from jsDelivr (pinned version, subresource integrity), so the browser needs internet access.
- OpenAPI 3.1.2 (JSON Schema 2020-12), checked against the official schema. A `QUERY` operation must be hidden, as OpenAPI 3.1 cannot describe it.
- Named structs become components (`Page[User]` becomes `PageUser`); anonymous bodies are named after their operation (`CreateNoteRequest`).
- Request schemas show `required`, rules and defaults as the decoder applies them. Response schemas mark as `required` what is always written and list `enum`s, which are enforced. A pointer without `omitempty` is nullable; nil slices and maps are sent as `[]` and `{}`.
- Every status rally sends is documented: 400 and 500 on every operation, 406 with a response body, 413, 415 and 422 with a request body, plus your `DefaultStatus`, `Statuses` and `Errors`.
- Info, servers, tags and security schemes come from `rally.Config`. Query keys of `apiKey` schemes are accepted on the operations that use them.

On 2026-11-24, openapi-typescript 7.13 generated strict-mode TypeScript from a rally document, with descriptions and examples as JSDoc and response enums as unions (`"dog" | "cat" | "bird"`, `"happy" | "sleepy" | null`); openapi-generator 7.27 (`dart-dio`) generated Dart that compiles, with response enums as enum classes (`PetKindEnum`, `PetMoodEnum?`).

## Middlewares and groups

Middlewares are plain `func(http.Handler) http.Handler`, so existing ones (logging, tracing, auth, compression) apply as they are.

```go
v1 := api.Group("/v1", authMiddleware) // prefix and middlewares, same document
rally.Register(v1, rally.Operation{Method: http.MethodPost, Path: "/items", OperationID: "create-item",
	Middlewares: []func(http.Handler) http.Handler{rateLimit}}, createItem)
```

A middleware passes values to handlers through the request context (`r.WithContext`), and reports errors in the API's format with `api.WriteError`.

## Recipes

**CORS.** rally answers `OPTIONS` itself from the pattern `/`, which group middlewares do not wrap: wrap the whole mux, `http.ListenAndServe(addr, cors(mux))`.

**Timeouts.** rally limits body sizes but sets no deadlines: configure `ReadHeaderTimeout`, `ReadTimeout` and `WriteTimeout` on the `http.Server`.

**Uploads, downloads, streaming.** rally handles JSON only. Register other routes on the same mux; they take precedence over rally's `/` fallback but are not in the document, and `api.WriteError` keeps their errors in the same format:

```go
mux.HandleFunc("POST /files", func(w http.ResponseWriter, r *http.Request) { /* multipart */ })
```

**Several APIs or a mount prefix.** One mux holds one API. Mount each under a parent mux, and put the prefix in `Config.Servers` so that the document's paths resolve. The reference page reads the document at `OpenAPIPath` as written, so turn it off under a prefix:

```go
cfg := rally.DefaultConfig("Admin API", "1.0.0")
cfg.Servers = []*rally.Server{{URL: "/admin"}}
cfg.DocsPath = "" // the document stays at /admin/openapi.json
admin := http.NewServeMux()
rally.New(admin, cfg)
root.Handle("/admin/", http.StripPrefix("/admin", admin))
```

**Query strings in middlewares.** `;` is data for rally (`?a=1;b=2` is `a` = `1;b=2`), while Go's `r.URL.Query()` drops such pairs: a middleware reading the query that way can see less than the handler.

## Limits

rally is built for JSON APIs. It does not support:

- routers other than `http.ServeMux`, and several APIs on one mux;
- bodies other than JSON (forms, multipart, raw bytes), streaming responses, Server-Sent Events, WebSockets (see [Recipes](#recipes));
- free-form JSON (`any`, `map[string]any`, `json.RawMessage`), `oneOf`/`anyOf`, custom `MarshalJSON`;
- cookie parameters and repeated response headers (one `Set-Cookie` per response): use a middleware;
- `format`, `nullable`, `const` (use `enum`), `readOnly`, `writeOnly`, per-field `deprecated`;
- custom error formats, YAML or OpenAPI 3.0 output.

## Benchmarks

The [benchmark suite](tests/bench) serves the same three endpoints with rally, Huma v2.40.0 (also without its `$schema` link transformer, "Huma no links"), Gin v1.12.0 (validator v10.30.5 `binding` tags expressing the same rules), and hand-written `net/http` + `encoding/json` **without any validation**, as a reference. Before timing anything, tests check that they all return the same responses and reject the same invalid inputs.

- **Complex**: Huma's own `BenchmarkFullRequest_Complex`: `POST /complex/{id}?search=test&page=2` with a validated body (pattern, ranges, unique items, nested structs, lists, map, time), answered with a 201.
- **GetUser**: `GET /users/{id}`, returning a small object.
- **CreateUser**: `POST /users`, with a small validated body.

Measured on 2026-11-24 (commit `79bca1a`) on an Intel i9-13900H laptop (Linux, Go 1.27.4), on AC power, with the `performance` profile and no other program using CPU.

**In process** (`go test -bench Sequential`, pinned with `taskset -c 0-11` to the 12 P-core threads): median of 10 passes, each running every benchmark once so that servers alternate. In parentheses: how many times longer than rally, and allocations per request.

| | rally | Gin | Huma | Huma no links | stdlib, no validation |
|---|---|---|---|---|---|
| Complex | **2.16 µs** (14 allocs) | 7.00 µs (×3.2, 55) | 11.7 µs (×5.4, 88) | 11.2 µs (×5.2, 86) | 4.40 µs (×2.0, 27) |
| GetUser | **389 ns** (4 allocs) | 1.13 µs (×2.9, 11) | 1.32 µs (×3.4, 13) | 1.01 µs (×2.6, 11) | 600 ns (×1.5, 5) |
| CreateUser | **625 ns** (7 allocs) | 2.00 µs (×3.2, 17) | 3.91 µs (×6.2, 34) | 3.58 µs (×5.7, 32) | 1.56 µs (×2.5, 11) |

**Over TCP** (`BENCH_LOAD=1 go test -run TestLoad`): each server runs in its own process on 4 hardware threads; the client opens 64 keep-alive connections from 8 other threads. Median of three full 5-second runs, which stayed within 5.5 % of each other. CPU is the server's user and system time per request.

| | rally | Gin | Huma | Huma no links | stdlib, no validation |
|---|---|---|---|---|---|
| Complex | **208k req/s**, 17.2 µs CPU | 122k, 30.3 µs | 85k, 44.6 µs | 87k, 43.3 µs | 157k, 23.2 µs |
| GetUser | **297k req/s**, 11.6 µs CPU | 254k, 13.7 µs | 248k, 14.2 µs | 258k, 13.5 µs | 294k, 11.8 µs |
| CreateUser | **264k req/s**, 13.2 µs CPU | 212k, 16.8 µs | 157k, 23.4 µs | 164k, 22.3 µs | 225k, 15.8 µs |

The kernel and `net/http` take a fixed share of every request, so the gap narrows on tiny requests and widens with body size and validation. With a database behind each request, the framework is a small part of the total.

```sh
cd tests/bench
for i in $(seq 10); do taskset -c 0-11 go test -run '^$' -bench Sequential -benchmem -count 1; done
for i in 1 2 3; do BENCH_LOAD=1 go test -run TestLoad -v; done   # Linux (taskset); BENCH_SERVER_CPUS, BENCH_CLIENT_CPUS, BENCH_CONNS, BENCH_DURATION
```

## Why "rally"

In a rally, the route is set in advance and the crew follows it exactly, as fast as they can; leaving the road ends the run. rally does the same for your API: your Go types are the pace notes, read once at startup, and every request must follow them to the letter, in a single pass, at full speed. A request that leaves the route is rejected, with every place it went off (`/items/0/name`).

The logo is the name drawn as a stage route, from the start (the ring) to the car at the finish (the red dot).

## Credits

The programming model, struct tags and error messages follow [Huma](https://github.com/danielgtaylor/huma) (MIT License, Copyright 2020 Daniel G. Taylor). The `/docs` page uses [Scalar](https://github.com/scalar/scalar) (MIT License), loaded from jsDelivr.

## License

[MIT](LICENSE), Copyright (c) 2026 Raphaël Witz.
