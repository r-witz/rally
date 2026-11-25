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

rally turns typed Go handlers into `net/http` routes. Every request is checked against your Go types before your handler runs, and the same types generate the OpenAPI document, so validation and documentation never drift apart.

- **Strict.** Bad requests never reach your handler. Unknown fields, duplicate keys, wrong types and broken rules are rejected, with every problem listed and located.
- **Fast.** 3 to 6 times faster than Huma, and faster than plain `encoding/json` without any validation ([benchmarks](#performance)).
- **Documented.** An OpenAPI 3.1 document and a reference page, generated from your types, ready for client generators.
- **Small.** Plain `net/http`, no dependencies, no code generation step.

## Example

```go
package main

import (
	"context"
	"net/http"

	"github.com/r-witz/rally"
)

type NoteInput struct {
	Title    string   `json:"title" minLength:"1" maxLength:"100"`
	Tags     []string `json:"tags,omitempty" maxItems:"5" uniqueItems:"true"`
	Priority string   `json:"priority,omitempty" enum:"low,normal,high" default:"normal"`
}

type Note struct {
	ID int `json:"id"`
	NoteInput
}

func main() {
	mux := http.NewServeMux()
	api := rally.New(mux, rally.DefaultConfig("Notes", "1.0.0"))

	rally.Register(api, rally.Operation{
		Method:        http.MethodPost,
		Path:          "/notes",
		OperationID:   "create-note",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct{ Body NoteInput }) (*struct{ Body Note }, error) {
		return &struct{ Body Note }{Note{ID: 1, NoteInput: in.Body}}, nil
	})

	http.ListenAndServe(":8888", mux) // OpenAPI at /openapi.json, reference at /docs
}
```

A valid request reaches the handler, with the default applied:

```console
$ curl --json '{"title": "Groceries", "tags": ["home"]}' localhost:8888/notes
{"id":1,"title":"Groceries","tags":["home"],"priority":"normal"}
```

An invalid one never does. Every problem is reported at once, with where it is:

```console
$ curl --json '{"title": "", "tags": ["home", "home"], "priority": "urgent", "color": "red"}' localhost:8888/notes
```

```json
{
  "title": "Unprocessable Entity",
  "status": 422,
  "detail": "validation failed",
  "errors": [
    {"detail": "expected length >= 1", "code": "minLength", "in": "body", "pointer": "/title"},
    {"detail": "expected array items to be unique", "code": "uniqueItems", "in": "body", "pointer": "/tags"},
    {"detail": "expected value to be one of \"low, normal, high\"", "code": "enum", "in": "body", "pointer": "/priority"},
    {"detail": "unexpected property", "code": "additionalProperties", "in": "body", "pointer": "/color"}
  ]
}
```

And `/openapi.json` describes exactly what the server enforces, from the same types:

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["title"],
  "properties": {
    "title": {"type": "string", "minLength": 1, "maxLength": 100},
    "tags": {"type": "array", "items": {"type": "string"}, "maxItems": 5, "uniqueItems": true},
    "priority": {"type": "string", "default": "normal", "enum": ["low", "normal", "high"]}
  }
}
```

## Install

```sh
go get github.com/r-witz/rally
```

Go 1.27 or later. rally serves JSON APIs on `http.ServeMux`; what it does not do is listed in [Limits](docs/GUIDE.md#limits).

## Performance

A `POST` with a validated body (Huma's own `BenchmarkFullRequest_Complex`: path and query parameters, a pattern, ranges, unique items, nested structs, a map, a timestamp), served by each framework with the same rules, after checking that all of them return the same responses and reject the same inputs:

| | Time per request | Allocations | Requests per second (TCP) |
|---|--:|--:|--:|
| **rally** | **2.16 µs** | **14** | **208k** |
| Gin | 7.00 µs (×3.2) | 55 | 122k |
| Huma | 11.7 µs (×5.4) | 88 | 85k |
| `net/http` + `encoding/json`, no validation | 4.40 µs (×2.0) | 27 | 157k |

On the smallest request (a `GET` returning a small object), the gap narrows to 1.5 to 3.4 times. Method, machine and every result: [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

## Documentation

- **[Guide](docs/GUIDE.md)**: everything rally does, feature by feature: operations, parameters and bodies, validation tags, errors, the OpenAPI document, middlewares, recipes and limits.
- **[pkg.go.dev](https://pkg.go.dev/github.com/r-witz/rally)**: the API reference, and runnable examples checked by `go test`.
- **[Specification profile](docs/PROFILE.md)**: how rally applies JSON, JSON Schema, RFC 3339, HTTP and OpenAPI, row by row, each with the test that checks it.
- **[Contributing](.github/CONTRIBUTING.md)** and **[design notes](docs/DESIGN.md)**.

## Why "rally"

In a rally, the route is set in advance and the crew follows it exactly, as fast as they can; leaving the road ends the run. rally does the same for your API: your Go types are the pace notes, read once at startup, and every request must follow them to the letter, in a single pass, at full speed. A request that leaves the route is rejected, with every place it went off (`/items/0/name`).

The logo is the name drawn as a stage route, from the start (the ring) to the car at the finish (the red dot).

## Credits

The programming model, struct tags and error messages follow [Huma](https://github.com/danielgtaylor/huma) (MIT License, Copyright 2020 Daniel G. Taylor). The `/docs` page uses [Scalar](https://github.com/scalar/scalar) (MIT License), loaded from jsDelivr.

## License

[MIT](LICENSE), Copyright (c) 2026 Raphaël Witz.
