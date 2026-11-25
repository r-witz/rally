# Design

How rally works, for contributors. The [README](../README.md) is the user documentation.

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

Coverage of the root package is 100 % for `go test .` and `tests/spec` together (`-coverpkg`). `BenchmarkHugeNumbers` keeps 1 MiB numbers linear: `multipleOf` on a million digits takes milliseconds.

The encoder is compared byte-for-byte with `encoding/json` (nil slices and maps aside), including 20,000 random floats and strings. Pattern tests compare with results from Node (`new RegExp(p, "u")`) and with `regexp` where the dialects agree.

Registration panics are covered by one table (`TestRegistrationPanics`): a new rule adds a row there.

Tests are kept when they catch a bug no other test catches. This is measured by mutation: each mutant changes one operator, constant, condition or statement of the library, and the tests that fail are recorded. On 2026-11-25, 88.6 % of the root package's mutants and 87.9 % of `internal/pattern`'s were caught; tests that caught nothing alone were removed, except those checking a property mutation cannot produce (no echo of rejected values, memory safety under GC).

## Conventions

- No comments in the code except godoc on exported identifiers. Explanations belong here.
- A removed or unsupported tag stays in `unsupportedTags` with a hint, so that it panics instead of being ignored.

## Planned: formats

Specified, not built. The rows keep their numbers and name the tests that will prove them; once built, each moves to [PROFILE.md](PROFILE.md). **Read** says what each row was checked against on 2026-11-24: the cited section, the JSON-Schema-Test-Suite cases of the format, or both; "to confirm" marks what still has to be read before building.

**Model.** A `format` tag on a `string` field (or on a list of strings, for its items) checks the value against the format and documents `"format": "…"`. The value is kept as written: no lower-casing, no trimming. Typed fields keep their own formats: `time.Time` is `date-time` with the limits of rows 3.5-3.7, `uuid.UUID` is `uuid` (row 3.9). Formats run after `minLength`/`maxLength` and before `pattern`, and their error code is `format`.

**Assertion.** JSON Schema 2020-12 makes `format` an annotation by default and lets an implementation assert it as an option (Validation §7.2.1); the stricter rules of §7.2.2 and §7.2.3 bind only a schema that declares the Format-Assertion vocabulary, which rally's document does not (it uses the OpenAPI 3.1 dialect). The tag is the author's choice to assert; a client reading the document learns the format without having to check it.

**Which validator decides.** The JSON-Schema-Test-Suite decides what a format accepts. In the document-against-behaviour check (row 10.3), the independent validator's own format checks are trusted only where they agree with the suite; the cases where they differ are listed with the suite case that settles them.

| # | Behaviour | Reference | Class | Read | Test | Status |
|---|---|---|---|---|---|---|
| 12.1 | `format` is accepted on `string` fields and on lists of strings; on any other type, and on a typed field (`time.Time`, `uuid.UUID`, text types), it panics at registration. | rally policy | P | — | `TestFormatRegistration` | 📋 planned |
| 12.2 | An unknown format name panics at registration, with the supported names (a misspelling gets "did you mean"). | rally policy, as Validation §7.2.3 asks of assertion | P | §7.2.3 | `TestFormatRegistration` | 📋 planned |
| 12.3 | Only formats implemented in full are accepted: no "best effort", no format that always passes. | rally policy, as Validation §7.2.2 asks of assertion | P | §7.2.2 | `TestFormatRegistration` | 📋 planned |
| 12.4 | `date-time`: RFC 3339 `date-time`, `T`/`Z` in either case. A string keeps what `time.Time` cannot hold (`-00:00`, any number of fractional digits, leap seconds). A leap second is valid when the time, converted to UTC, is 23:59:60, on any date: RFC 3339 §5.7 allows it only at the end of a month in which a leap second occurs (a table that grows with IERS announcements), and the suite settles on the time alone. | RFC 3339 §5.6, §5.7; Validation §7.3.1 | C | section and suite | `TestJSONSchemaSuite` (`format-date-time.json`), `TestFormats` | 📋 planned |
| 12.5 | `date`: RFC 3339 `full-date`, with the real number of days in each month. | RFC 3339 §5.6, §5.7 | R | section and suite | `TestJSONSchemaSuite` (`format-date.json`), `TestFormats` | 📋 planned |
| 12.6 | `time`: RFC 3339 `full-time`, offset required, `-00:00` valid; a leap second as in 12.4 (23:59:60 in UTC). The suite marks leap seconds in `time` invalid for a release after 2020-12; rally follows the 2020-12 cases (`optional/format-time.json`). | RFC 3339 §5.6, §5.7 | C | section and suite | `TestJSONSchemaSuite` (`format-time.json`, `optional/format-time.json`), `TestFormats` | 📋 planned |
| 12.7 | `duration`: the `duration` ABNF of RFC 3339 Appendix A: `P` then date and/or `T` time elements in order, weeks alone (`P2W`, not `P1Y2W`), no fraction, no sign, any number of digits (`P01D` is valid). | RFC 3339 Appendix A | R | suite; Appendix A to confirm | `TestJSONSchemaSuite` (`format-duration.json`), `TestFormats` | 📋 planned |
| 12.8 | `email`: the RFC 5321 `Mailbox` grammar: dot-atom or quoted-string local part, domain or address literal (`[127.0.0.1]`, `[IPv6:::1]`), ASCII only, no comments, no display name, no surrounding space. No length limit: RFC 5321 §4.5.3.1 gives sizes every implementation must accept and says implementations should impose no limits. | RFC 5321 §4.1.2, §4.1.3, §4.5.3.1 | R | §4.5.3.1 and suite; §4.1.2-§4.1.3 to confirm | `TestJSONSchemaSuite` (`format-email.json`), `TestFormats` | 📋 planned |
| 12.9 | `hostname`: RFC 1123 labels (letters, digits and hyphens, a hyphen at neither end, a digit allowed first), at most 63 octets per label and 253 characters in all (255 octets in the DNS wire form), no trailing dot, ASCII only. A label starting with `xn--` must be a valid A-label: Punycode that decodes to a valid IDNA2008 U-label (the suite rejects `xn--hello-txk`). | RFC 1123 §2.1; RFC 1035 §2.3.4; RFC 5891 §4.4; RFC 5890 §2.3.2.1 | R | RFC 1123 §2.1, RFC 1035 §2.3.4, RFC 5891 §4.4 and suite | `TestJSONSchemaSuite` (`format-hostname.json`), `TestFormats` | 📋 planned |
| 12.10 | `idn-hostname`: a `hostname`, or labels that are valid IDNA2008 U-labels. Needs the RFC 5892 derived property tables, the CONTEXTJ and CONTEXTO rules and the Bidi rule, which Go's `unicode` package does not give directly. | RFC 5890 §2.3.2.3; RFC 5892; RFC 5893 | R | to confirm (suite not read) | `TestJSONSchemaSuite` (`format-idn-hostname.json`) | 📋 planned, largest format |
| 12.11 | `idn-email`: the RFC 6531 `Mailbox`: `email` extended with UTF-8, an `idn-hostname` domain. The suite has cases to check before building (`"@example.com"` and a very long local part are valid there). | RFC 6531 §3.3 | R | §3.3 opening and suite; grammar to confirm | `TestJSONSchemaSuite` (`format-idn-email.json`) | 📋 planned, after 12.10 |
| 12.12 | `ipv4`: dotted-quad, four `decbyte` of 1 to 3 ASCII digits whose value is 0-255. Leading zeros are valid (`010.0.0.1`): RFC 2673 defines `decbyte = 1*3DIGIT`, and the suite says so. Some resolvers read them as octal; an API that needs to exclude them adds a `pattern`. | RFC 2673 §3.2 | R | section and suite | `TestJSONSchemaSuite` (`format-ipv4.json`), `TestFormats` | 📋 planned |
| 12.13 | `ipv6`: the three text forms of RFC 4291 §2.2 (eight groups, `::` once, a trailing dotted-quad whose octets have no leading zero), hex digits in either case; no zone (`%eth1`), no prefix length, no brackets. | RFC 4291 §2.2 | R | section opening and suite | `TestJSONSchemaSuite` (`format-ipv6.json`), `TestFormats` | 📋 planned |
| 12.14 | `uri`: an RFC 3986 `URI` (scheme required): well-formed percent-encodings, nothing outside the grammar, IP literals checked (`IPv6address`, `IPvFuture`, `dec-octet` without leading zero), `port = *DIGIT`. A `reg-name` host is not checked as a hostname (`http://999.999.999.999/` is valid). | RFC 3986 §3, §3.2.2, §3.2.3 | R | ABNF lines and suite | `TestJSONSchemaSuite` (`format-uri.json`), `TestFormats` | 📋 planned |
| 12.15 | `uri-reference`: an RFC 3986 `URI-reference` (a URI or a relative reference, including `""` and `//`). | RFC 3986 §4.1 | R | ABNF line and suite | `TestJSONSchemaSuite` (`format-uri-reference.json`) | 📋 planned |
| 12.16 | `iri`, `iri-reference`: as 12.14 and 12.15 with the `ucschar` and `iprivate` ranges of RFC 3987. | RFC 3987 §2.2 | R | ABNF lines and suite | `TestJSONSchemaSuite` (`format-iri.json`, `format-iri-reference.json`) | 📋 planned |
| 12.17 | `uuid` on a string: 8-4-4-4-12 hexadecimal digits in either case, any version and variant; no braces, no URN. | Validation §7.3.5 (cites RFC 4122, replaced by RFC 9562 §4) | R | suite | `TestJSONSchemaSuite` (`format-uuid.json`), `TestFormats` | 📋 planned |
| 12.18 | `uri-template`: an RFC 6570 template of any level: literals (with percent-encodings) and expressions with an operator, variable names (dotted, percent-encoded) and modifiers (`:` prefix of 1 to 9999, `*` explode). | RFC 6570 §2 | R | `max-length` rule and suite | `TestJSONSchemaSuite` (`format-uri-template.json`) | 📋 planned |
| 12.19 | `json-pointer`: an RFC 6901 pointer: empty, or `/` reference tokens where `~` is followed by `0` or `1`. | RFC 6901 §3 | R | ABNF and suite | `TestJSONSchemaSuite` (`format-json-pointer.json`) | 📋 planned |
| 12.20 | `relative-json-pointer`: a non-negative integer without leading zeros, then `#` or a JSON pointer. | draft-handrews-relative-json-pointer-01 | R | suite; draft to confirm | `TestJSONSchemaSuite` (`format-relative-json-pointer.json`) | 📋 planned |
| 12.21 | `regex`: a valid ECMA-262 regular expression in Unicode mode, checked for syntax only: lookbehind, named groups and backreferences are valid here although the `pattern` tag rejects them (row 3.3); Python-style `(?P<n>)` and `(?i)` are not. | Validation §7.3.8 (cites ECMA-262 11th edition §21.2.1) | R | suite | `TestJSONSchemaSuite` (`format-regex.json`, `optional/format-regex.json`) | 📋 planned |
| 12.22 | Formats of the OpenAPI Format Registry that JSON Schema does not define (`date-time-local`, `ipv4-cidr`, `language`, `media-range`, `http-date`, `sf-*`…) are not supported: the registry says tools need not implement them; each would first need its rows here. | OAS Format Registry | S | registry page | `TestFormatRegistration` | 📋 planned |
| 12.23 | Every format runs in time linear in the length of the value and allocates nothing for a valid ASCII value. | row 13.3 | P | — | `BenchmarkFormats` | 📋 planned |

Order of work proposed, by usefulness and size: 12.1-12.3 and the dates (12.4-12.7, from the RFC 3339 parser), then `ipv4`, `ipv6`, `uuid`, `json-pointer`, `relative-json-pointer` (small grammars), `email`, `uri`, `uri-reference`, `uri-template`, `regex` (a syntax mode of `internal/pattern`), then `hostname`, `iri`, and last `idn-hostname` and `idn-email`, which need the IDNA2008 tables. The suite has 19 (`regex`) to 102 (`idn-hostname`) tests per format in its `format-*.json` files.

## Performance

The goal is that every value gets the same treatment: no input of a given size costs much more than another, and no change makes the library slower without a measurement saying so.

**Protocol**:

- The conditions are checked and written with every figure: power profile, AC, `uptime`, the processes using CPU.
- Two builds are compared by alternating their binaries (A B, B A…) on a pinned core, looking at medians and minimums. When the machine is not quiet, the figures are also scaled by an unchanged reference measured in the same run, and the report says the check should be redone on a quiet machine.
- Over TCP, two builds are compared in short alternating runs of the load test restricted to the rally server. The README tables come from a full-suite run; figures from a solo run, from another day or from another thermal state are not compared at the few-percent level (expect about ±2 % from one day to the next).
- A change is accepted with no regression beyond benchmark noise (about 3 %), and code is added for performance only for a measured gain of at least 10 % on a benchmark. A correctness fix may cost more, when the cost is measured and stated.

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 13.1 | Allocations per request stay at 14 (Complex), 4 (GetUser), 7 (CreateUser) in the `bench` module. | protocol above | P | `TestAllocations` | 📋 planned (read from `-benchmem` on 2026-11-24: 14, 4, 7) |
| 13.2 | The README tables (in process and over TCP, against Huma, Gin and the stdlib) are measured on v2 with the protocol above, and the conditions are written next to them. | protocol above | P | `BenchmarkSequential`, `TestLoad` | ✅ 2026-11-24 |
| 13.3 | Decoding a body costs time linear in its size for every input class: long strings, strings full of escapes, non-ASCII text, numbers with many digits or large exponents (done: `BenchmarkHugeNumbers`, 1 MiB in about 2 ms), many members, nesting up to the limit, lists past `maxItems`, `uniqueItems` on long lists, large enums, patterns on long strings, formats. Proposed bound, to agree on: at most 4 times the time of a plain body of the same size. | rally policy | P | `BenchmarkWorstCases`, `BenchmarkHugeNumbers` | 📋 planned |
| 13.4 | Parsing a query string costs time linear in its length, whatever the number of pairs (up to 10,000) and of declared parameters. | rally policy | P | `BenchmarkWorstCases` | 📋 planned |
| 13.5 | Encoding a response costs time linear in its size, except maps, whose keys are sorted (n log n). | rally policy | P | `BenchmarkWorstCases` | 📋 planned |
| 13.6 | Registering an operation takes time linear in the size of its types and tags; patterns compile in time bounded by their table limit (4096 states). | rally policy | P | `BenchmarkRegister` | 📋 planned |
