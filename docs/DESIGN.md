# Design

How rally works, for contributors. The [README](../README.md) and the [guide](GUIDE.md) are the user documentation.

## Principles

- **Strict.** Misuse panics at registration, with the fix in the message, rather than being silently ignored. Every invalid request gets a precise error.
- **One source of truth.** The type model in `model.go` drives decoding, validation, encoding and the OpenAPI document, so they cannot drift.
- **Work at registration, not per request.** Each operation is compiled into closures over `reflect.Value` with precomputed field indexes, messages and tables.
- **Simple.** One way to do each thing, `net/http` only, no dependencies, no `unsafe`, no code generation step for users.
- **Measured.** A change to the request path must not cost allocations (rally: Complex 14, GetUser 4, CreateUser 7) or measurable time. Compare interleaved runs against the previous build.

## Layout

| File | Role |
|---|---|
| `api.go` | `New`, `Config`, groups, the 404/405 fallback, the document and `/docs` handlers. |
| `register.go` | `Register`, `Operation`, and `compile`, which turns an operation into its `plan` and its part of the document. |
| `serve.go` | The request path: `ServeHTTP`, pooled state, `readInput`, `readBody`, `Accept` negotiation, `writeOutput`. |
| `model.go` | Type kinds, JSON fields of a struct, tag checking (allowed, unsupported, misspelled). |
| `params.go` | Path, query and header parameters: query parsing, binders, setters. |
| `validate.go` | Rules parsed from tags, satisfiability checks, the scalar setters shared by parameters and JSON. |
| `number.go` | Exact decimal values of JSON numbers, for integers written `1.0` or `1e2` and for exact bound and enum comparisons. |
| `json_decode.go` | The scanner and per-type decoders, which validate while decoding. |
| `json_encode.go` | Per-type encoders, and the `enum` check of response fields. Output is byte-for-byte `encoding/json` with HTML escaping off, except that nil slices and maps are written `[]` and `{}`. |
| `schema.go`, `openapi.go` | Schema registry (component names, one direction per type) and document types. |
| `errors.go` | `ErrorModel`, `ErrorDetail`, error writing, panic recovery. |
| `internal/pattern` | The ECMAScript pattern engine behind the `pattern` tag. |

## Registration

`Register` takes the registration mutex and runs `compile`, which:

1. checks the `Operation`: method, path, statuses, security, documentable path;
2. walks the input struct (`compileInput`): parameters become `paramBinder`s, and `Body` gets a decoder, a request schema and a claim of its types as request types;
3. walks the output struct (`compileOutput`): `Status`, response headers, and `Body` with an encoder and a response schema;
4. documents every status the operation can send.

Tags are checked against the table for their place (`fieldTags`, `bodyTags`, `paramTags`...), so a tag where it has no effect panics. `default` and `example` are rendered to JSON by one function (`jsonValue`) and checked against the rules of their field or parameter. Decoders, encoders and field lists are cached per type. A failed build is removed from the cache, so a recovered panic leaves no half-built entry.

**One direction per type.** `registry.claimBody` walks every struct type reachable from a body and records whether it is a request or a response type. A type claimed both ways panics. The same walk enforces `maxItems` on request lists, and rejects defaults and rules other than `enum` declared in response-only types, except in structs reached through an embedded field, which may be shared with requests: there they are left out of the response schema. `enum` is kept in response schemas and checked by the encoder (`enumEncoder`).

## Request path

`ServeHTTP` takes a pooled `state` (decode and encode buffers), then:

1. `readInput` parses the query once (`parseQuery`, which reports unknown, repeated and malformed keys), binds the parameters, then reads the body up to `MaxBodyBytes` and decodes it;
2. errors are collected with their location, up to 100 and 16 KiB of pointers, and answered with a 400 (parameters, syntax) or a 422 (body schema);
3. the handler runs; `writeOutput` checks the status against the declared ones, encodes the body into the pooled buffer and writes it in one call.

Syntax errors and the error cap unwind with `panic`/`recover` inside the decoder, which keeps the hot path free of error checks. Handler panics are recovered into a logged 500.

## Decoder

The decoder is its own scanner over the raw bytes: no tokens, no intermediate values, one pass. It follows RFC 8259 and the I-JSON profile of RFC 7493: duplicate keys, invalid UTF-8 and unknown properties are reported and names are case-sensitive. Numbers are compared by their exact decimal value (`number.go`); float64 decides unless it equals the bound. Every behaviour is a row of [PROFILE.md](PROFILE.md), checked by `tests/spec`.

Strings are scanned 8 bytes at a time while they are plain ASCII. Struct keys are first matched as raw bytes against the next declared field, the order clients usually send them in. Items past `maxItems` are skipped without being decoded.

## Patterns

`internal/pattern` parses the ECMAScript dialect that JSON Schema names (Unicode mode) and compiles it into a byte-level table, read forward or backward, of up to 4096 states. A pattern without a table is simulated. Either way, matching takes time linear in the string.

Literal patterns, the shortest possible match and a required character are checked before scanning. Lookaround, backreferences and named groups are not supported, and panic at registration.

## Testing

Each source file has its test file next to it (`serve.go`, `serve_test.go`); `example_test.go` holds the runnable examples shown on pkg.go.dev. Tests that need other modules live in the separate `tests` module, so that rally keeps no dependency:

| Directory | What it checks |
|---|---|
| `tests/spec` | The specifications: JSONTestSuite, JSON-Schema-Test-Suite, RFC 3339 vectors, the OpenAPI schema, each row of [PROFILE.md](PROFILE.md), and every request and response against the document rally serves. |
| `tests/bench` | Benchmarks against Huma, Gin and the standard library, after checking that they all answer the same. |

```sh
go test ./...                                                   # unit and differential tests
go test -race -short ./...                                      # -short shrinks the random pattern comparisons
go test -run '^$' -fuzz FuzzDecode -fuzztime 60s                # decoder against encoding/json/v2, after specToV2
go test -run '^$' -fuzz FuzzScanner -fuzztime 60s               # scanner against jsontext
go test -run '^$' -fuzz FuzzQuery -fuzztime 60s                 # query parsing against url.ParseQuery (WHATWG, with ; as data)
go test -run '^$' -fuzz FuzzDecimal -fuzztime 60s               # exact decimals (cmp, integral, multipleOf) against math/big.Rat
go test -run '^$' -fuzz FuzzPattern -fuzztime 60s ./internal/pattern   # tables against simulation and regexp
cd tests && go test ./...                                       # spec and bench (Node, when installed, is the regex reference)
```

Coverage of the root package is 100 % for `go test .` and `tests/spec` together (`-coverpkg`). `BenchmarkHugeNumbers` keeps 1 MiB numbers linear: `multipleOf` on a million digits takes milliseconds. `TestRallyAllocations` (`tests/bench`, skipped under `-race`) fails when a request allocates more or less than the numbers quoted in [BENCHMARKS.md](BENCHMARKS.md).

The encoder is compared byte-for-byte with `encoding/json` (nil slices and maps aside), including 20,000 random floats and strings. Pattern tests compare with results from Node (`new RegExp(p, "u")`) and with `regexp` where the dialects agree.

Registration panics are covered by one table (`TestRegistrationPanics`): a new rule adds a row there.

Tests are kept when they catch a bug no other test catches. This is measured by mutation: each mutant changes one operator, constant, condition or statement of the library, and the tests that fail are recorded. On 2026-11-25, 88.6 % of the root package's mutants and 87.9 % of `internal/pattern`'s were caught; tests that caught nothing alone were removed, except those checking a property mutation cannot produce (no echo of rejected values, memory safety under GC).

## Conventions

- No comments in the code except godoc on exported identifiers. Explanations belong here.
- A removed or unsupported tag stays in `unsupportedTags` with a hint, so that it panics instead of being ignored.

## Measuring performance

The allocation counts are guarded by `TestRallyAllocations`; time is measured by hand, before and after a change:

- The conditions are checked and written with every figure: power profile, AC, `uptime`, the processes using CPU.
- Two builds are compared by alternating their binaries (A B, B A…) on a pinned core, looking at medians and minimums. When the machine is not quiet, the figures are also scaled by an unchanged reference measured in the same run, and the report says the check should be redone on a quiet machine.
- Over TCP, two builds are compared in short alternating runs of the load test (`BENCH_LOAD=1`). The tables in [BENCHMARKS.md](BENCHMARKS.md) come from a full-suite run; figures from a solo run, from another day or from another thermal state are not compared at the few-percent level (expect about ±2 % from one day to the next).
- A change is accepted with no regression beyond benchmark noise (about 3 %), and code is added for performance only for a measured gain of at least 10 % on a benchmark. A correctness fix may cost more, when the cost is measured and stated.
