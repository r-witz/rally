# rally specification profile

This file is the specification of rally: every behaviour visible to a client or to a reader of the OpenAPI document, the reference it comes from, and the test that proves it. A behaviour that is not here is a bug, or a missing row.

**Rule.** Reject what the reference calls invalid, accept what it calls valid. Where the reference leaves a choice, take the strictest option, unless clients overwhelmingly rely on a convention, which is then followed and cited.

**Classes.**

| Class | Meaning |
|---|---|
| **R** | Required by the cited reference (MUST, or the grammar). |
| **S** | The reference leaves a choice (MAY, SHOULD, or silence); the strict option is taken. Candidate for loosening. |
| **C** | The reference leaves a choice; a convention is followed because clients rely on it. Evidence is cited. |
| **A** | Valid per the reference, but the Go type cannot hold the value: rejected ("option A"). Shown in the document where JSON Schema can express it, and listed in the allow-list of the document-against-behaviour test otherwise. |
| **P** | rally policy, not a wire behaviour (registration checks, limits). |

**Status**: ✅ conforms, checked by the test named in the row.

**References.** RFC 8259 (JSON), RFC 7493 (I-JSON), RFC 3339 (date-time), RFC 3986 (URI), RFC 9110 (HTTP semantics), RFC 9457 (problem details), RFC 6901 (JSON Pointer), RFC 6839 (+json), RFC 9562 (UUID), RFC 10008 (QUERY), [JSON Schema 2020-12](https://json-schema.org/specification-links#2020-12) Core and Validation, [OpenAPI 3.2.1](https://spec.openapis.org/oas/v3.2.1.html) and [3.1.2](https://spec.openapis.org/oas/v3.1.2.html), [WHATWG URL](https://url.spec.whatwg.org/#urlencoded-parsing). RFC texts: `https://www.rfc-editor.org/rfc/rfcNNNN`.

**Test suites** (`tests/spec/`, in the separate `tests` module so that rally keeps no dependency): JSONTestSuite (318 documents), JSON-Schema-Test-Suite (2020-12 cases that Go types can express), RFC 3339 vectors, and a document-against-behaviour check: every instance sent to rally is also validated against the schema rally documents, by an independent validator (santhosh-tekuri/jsonschema, ECMAScript patterns evaluated by V8 through Node). They disagree only on the rows listed in 10.3. `TestProfile` checks that every row names tests that exist. The regex reference is Node: `TestJSONSchemaSuite` skips the pattern cases without it, and says so.

## 1. JSON request bodies

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 1.1 | The body is parsed by the RFC 8259 grammar; any other text is a 400 `syntax` error. | RFC 8259 §2-§7 | R | `TestJSONTestSuite` (`y_`, `n_`) | ✅ |
| 1.2 | Text that is not UTF-8, and lone surrogates, raw or escaped (`\uD800`), are rejected. | RFC 8259 §8.1, §8.2; RFC 7493 §2.1 | R | `TestJSONTestSuite` (`i_string_*`) | ✅ |
| 1.3 | A byte order mark is rejected. | RFC 8259 §8.1 (a parser MAY ignore it) | S | `TestJSONTestSuite` (`i_structure_UTF-8_BOM_empty_object`) | ✅ |
| 1.4 | Duplicate member names are rejected (`duplicate`). | RFC 8259 §4 (SHOULD be unique); RFC 7493 §2.3 (MUST NOT) | R | `TestProtocol`, `TestDecodeScalars` | ✅ |
| 1.5 | Member names match exactly: `NAME` is not `name`. | RFC 8259 §8.3 | R | `TestProtocol`, `TestDecodeScalars` | ✅ |
| 1.6 | Nothing may follow the top-level value. | RFC 8259 §2 (JSON-text) | R | `TestJSONTestSuite` (`n_structure_*`) | ✅ |
| 1.7 | Nesting deeper than 10,000 levels is rejected. | RFC 8259 §9 (limits allowed) | P | `TestJSONTestSuite` (`n_structure_100000_opening_arrays`) | ✅ |
| 1.8 | Unknown members are rejected (`additionalProperties`); the document says `additionalProperties: false`. | JSON Schema allows open objects; Azure guidelines (DO fail), Zalando #109, JSON:API | S | `TestProtocol`, `TestJSONSchemaSuite` | ✅ |
| 1.9 | A member is required unless `omitempty`, `omitzero` or a default; `null` is accepted only by a pointer without `omitempty`. | JSON Schema Validation §6.5.3 (required), §6.1.1 (type) | R | `TestProtocol`, `TestJSONSchemaSuite` | ✅ |
| 1.10 | Bodies over `MaxBodyBytes` (1 MiB by default) get a 413. | RFC 9110 §15.5.14 | P | `TestProtocol`, `TestBodyReading` | ✅ |
| 1.11 | Every problem is reported, up to 100, in one response. | — | P | `TestProtocol`, `TestDecodeMaxErrors` | ✅ |
| 1.12 | `minProperties` and `maxProperties` apply to maps; every map in a request body needs `maxProperties` (members past it are skipped, not decoded), as every list needs `maxItems` (row 11.3). | JSON Schema Validation §6.5.1, §6.5.2 | R, P | `TestExclusiveBoundsAndMultipleOf`, `TestJSONSchemaSuite` | ✅ |
| 1.13 | A body is required unless `Body` is a pointer; an optional body may be absent or `null` (the field is nil), and `required:"true"` on a pointer body rejects both. The document says the same (`required`, nullable or not). | OAS 3.1.2 Request Body Object (`required`) | R | `TestBodies` | ✅ |

## 2. Numbers

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 2.1 | An integer field accepts any number with a zero fractional part: `1.0`, `1e2`, `0.5e1`; `1.5` is a `type` error. | JSON Schema Validation §6.1.1; OAS 3.1.2 Appendix B ("a convenience defined mathematically") | R | `TestJSONSchemaSuite` (`type.json`), `TestProtocol`, `TestDecodeScalars` | ✅ |
| 2.2 | A number outside the range of its Go type is rejected; the document gives the range (`minimum`/`maximum`, including for `int64` and `uint64`). | RFC 7493 §2.2; Go types | A | `TestJSONSchemaSuite` (`optional/bignum.json`), `TestProtocol` | ✅ |
| 2.3 | `minimum`, `maximum` and `enum` compare the exact decimal value written by the client, not its float64 rounding. | JSON Schema Validation §6.2.2-§6.2.5, §6.1.2 | R | `TestJSONSchemaSuite` (`optional/bignum.json`), `FuzzDecimal` | ✅ |
| 2.4 | Rule values in the document are written exactly as in the tag (`0.123456789123456789`, not `0.12345678912345679`). | the document says what is enforced | R | `TestJSONSchemaSuite` (document against behaviour) | ✅ |
| 2.5 | Rule tags accept integers written with a zero fraction (`maxLength:"2.0"`). | JSON Schema Validation §6.3.1 (non-negative integer, by §6.1.1) | R | `TestJSONSchemaSuite` (`maxLength.json`, `minLength.json`) | ✅ |
| 2.6 | `exclusiveMinimum` and `exclusiveMaximum` are numbers, compared exactly like `minimum` and `maximum`. | JSON Schema Validation §6.2.3, §6.2.5 | R | `TestJSONSchemaSuite`, `TestExclusiveBoundsAndMultipleOf` | ✅ |
| 2.7 | `multipleOf` (greater than 0) holds when the division gives an integer, computed exactly: `0.0075` is a multiple of `0.0001`, `1e308` of `0.5`. | JSON Schema Validation §6.2.1 | R | `TestJSONSchemaSuite` (`multipleOf.json`), `TestMultipleOf`, `FuzzDecimal`, `BenchmarkHugeNumbers` | ✅ |
| 2.8 | `uniqueItems` on a list of floats compares the float64 values: two distinct decimals that round to the same float64 (`0.1` and `0.10000000000000000001`) count as equal and the list is rejected. | JSON Schema Validation §6.4.3 (distinct numbers); the Go type cannot hold the difference | A | `TestRules` | ✅ |

## 3. Strings and formats

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 3.1 | `minLength`/`maxLength` count code points. | JSON Schema Validation §6.3.1 | R | `TestJSONSchemaSuite` (`minLength.json`, `maxLength.json`) | ✅ |
| 3.2 | `pattern` is an ECMA-262 regular expression in Unicode mode, not anchored. | JSON Schema Core §6.4, Validation §6.3.3 | R | `TestJSONSchemaSuite` (`pattern.json`, `optional/ecmascript-regex.json`), `TestPatternsFollowECMAScript` | ✅ |
| 3.3 | Patterns rally cannot match in linear time (lookaround, backreferences) panic at registration. Everything in the interoperable subset is supported, and more (classes, escapes, Unicode properties). | JSON Schema Core §6.4: patterns SHOULD be ECMA-262; authors SHOULD limit themselves to a subset (characters, classes, quantifiers, anchors, groups, alternation) that has neither lookaround nor backreferences | S | `TestPatternsRejected` | ✅ |
| 3.4 | `time.Time` is an RFC 3339 `date-time`; `t` and `z` may be lower case; values are written back upper case. | RFC 3339 §5.6 and its NOTE | R | `TestRFC3339`, `TestJSONSchemaSuite` | ✅ |
| 3.5 | A leap second (`23:59:60`) is rejected. | RFC 3339 §5.6 (valid); `time.Time` cannot hold it | A | `TestRFC3339`, `TestJSONSchemaSuite` | ✅ rejects |
| 3.6 | `-00:00` (unknown local offset) is rejected. | RFC 3339 §4.3 (differs from `Z`); `time.Time` cannot tell them apart | A | `TestRFC3339` | ✅ |
| 3.7 | More than 9 fractional digits are rejected. | RFC 3339 §5.6 (`1*DIGIT`); `time.Time` keeps 9 | A | `TestRFC3339`, `TestJSONSchemaSuite` | ✅ |
| 3.8 | A space instead of `T` is rejected. | RFC 3339 §5.6 NOTE (an application choice) | S | `TestRFC3339` | ✅ |
| 3.9 | `uuid.UUID` accepts the 8-4-4-4-12 hex form, in either case; writes it lower case; rejects braces, URNs and other forms. | RFC 9562 §4 (case-insensitive on input, lower case on output) | R | `TestProtocol` | ✅ |

## 4. Query parameters

OpenAPI 3.1.2, Parameter Object, [URL Percent-Encoding](https://spec.openapis.org/oas/v3.1.2.html#url-percent-encoding): query strings MUST parse and percent-decode with the RFC 1866 `application/x-www-form-urlencoded` rules, including `+` as a space (3.2.1 names the WHATWG URL Standard, which agrees on these points).

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 4.1 | `+` is a space; `%2B` is a plus. | OAS 3.1.2 URL Percent-Encoding (MUST, RFC 1866 rules) | R | `TestProtocol`, `FuzzQuery` | ✅ |
| 4.2 | A list parameter is sent as repeated keys, `?tag=a&tag=b` (`style: form`, `explode: true`); `tag[]` is an unknown parameter. | OAS 3.1.2 Parameter Object (`form`, `explode` true by default); followed by URLSearchParams, requests, httpx, Go, Dart, dio, OkHttp, Retrofit, Spring, openapi-generator, hey-api, openapi-fetch | C | `TestProtocol`, `TestParams` | ✅ |
| 4.3 | A scalar parameter present twice is a 400 (`duplicate`). | silent in OAS and RFCs; frameworks disagree (first, last, joined) | S | `TestProtocol`, `TestMalformedQueries` | ✅ |
| 4.4 | An unknown parameter is a 400 (`additionalProperties`). | silent in OAS; Azure (DO fail), Zalando #109 (SHOULD), JSON:API (MUST) | S | `TestProtocol`, `TestMalformedQueries` | ✅ |
| 4.5 | A malformed percent-encoding (`%zz`) is a 400. | RFC 3986 §2.1 (WHATWG would keep it literally) | S | `TestProtocol`, `TestMalformedQueries` | ✅ |
| 4.6 | Text that is not UTF-8 after decoding is a 400. | WHATWG would replace it with U+FFFD; strict option | S | `TestProtocol`, `TestMalformedQueries` | ✅ |
| 4.7 | `;` is ordinary data: `?n=1;x=2` is `n` = `1;x=2`. | RFC 1866 §8.2.1, which OAS 3.1.2 requires, separates pairs with `&` only (so does WHATWG; Go's `url.ParseQuery` rejects `;`) | R | `TestProtocol`, `TestIntegerParameters`, `FuzzQuery` | ✅ |
| 4.8 | `?q=` is the empty string: valid for a string, a 400 for any other type. `allowEmptyValue` is not used. | OAS 3.1.2 (`allowEmptyValue` NOT RECOMMENDED); Google APIs answer 400 for an empty integer | S | `TestProtocol` | ✅ |
| 4.9 | Booleans are `true` or `false`, nothing else. | no standard (OAS 3.2.1 says so); Azure guidelines: `true`/`false` | S | `TestProtocol`, `TestParamErrors` | ✅ |
| 4.10 | Numbers in parameters use the JSON number grammar (no `+`, no leading zeros), and an integer parameter accepts `2.0` and `2e0`, as a body does (row 2.1). | RFC 8259 §6 (a choice: there is no standard for numbers in URLs); OAS 3.1.2 Appendix B (integer is mathematical) | S | `TestProtocol`, `TestIntegerParameters` | ✅ |

## 5. Path and header parameters

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 5.1 | Path parameters are percent-decoded once by ServeMux and always required. | OAS 3.1.2 Parameter Object (path parameters required) | R | `TestProtocol` | ✅ |
| 5.2 | A header parameter sent in several field lines is a 400 (`duplicate`). | RFC 9110 §5.3 (a sender MUST NOT repeat a field that is not a list) | S | `TestProtocol`, `TestMalformedQueries` | ✅ |
| 5.3 | An empty header is the empty value, as `?q=` (row 4.8). | RFC 9110 §5.5 (empty field values are legal) | R | `TestProtocol` | ✅ |

## 6. Content negotiation

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 6.1 | A body with a `Content-Type` other than `application/json` or `*/*+json` gets a 415. | RFC 9110 §15.5.16; RFC 6839 §3.1 | R | `TestProtocol`, `TestIsJSONMediaType` | ✅ |
| 6.2 | A body without `Content-Type` gets a 415. | RFC 9110 §8.3 (MAY assume octet-stream); Spring, ASP.NET Core, Echo answer 415 | S | `TestProtocol` | ✅ |
| 6.3 | A `charset` parameter is ignored. | RFC 8259 §11 ("no effect on compliant recipients") | R | `TestProtocol` | ✅ |
| 6.4 | A request whose `Accept` excludes `application/json` gets a 406 on operations that answer with a body; the most specific matching media range decides. Errors are sent as `application/problem+json` whatever `Accept` says. | RFC 9110 §12.4.1 (406 or disregard), §12.5.1 | S | `TestProtocol`, `TestUnknownRoutes` | ✅ |
| 6.5 | Responses with a body are `application/json`, errors `application/problem+json`, without `charset`. | RFC 8259 §11; RFC 9457 §3 | R | `TestProtocol` | ✅ |

## 7. Methods and routing

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 7.1 | A path that exists with other methods gets a 405 with the complete `Allow` list. | RFC 9110 §15.5.6 (MUST send Allow) | R | `TestProtocol`, `TestUnknownRoutes` | ✅ |
| 7.2 | `HEAD` is served for every `GET`, without a body. | RFC 9110 §9.1, §9.3.2 | R | `TestProtocol` | ✅ |
| 7.3 | `OPTIONS` on an existing path gets a 204 with `Allow`; CORS preflights are left to a middleware wrapping the mux. | RFC 9110 §9.3.7 (servers need not implement OPTIONS; when they do, they SHOULD send Allow) | S | `TestProtocol`, `TestUnknownRoutes` | ✅ |
| 7.4 | `QUERY` operations can be registered only hidden: OpenAPI 3.1 cannot document them (row 10.1). | RFC 10008; OAS 3.1.2 has no `query` field | P | `TestProtocol`, `TestRegistrationPanics` | ✅ |
| 7.5 | A path ending with `/` panics at registration (no redirect, no subtree). | rally policy; Go 1.26 redirects with 307 | P | `TestRegistrationPanics` | ✅ |

## 8. Responses

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 8.1 | Response bodies are I-JSON: UTF-8 (invalid bytes become U+FFFD), unique member names, finite numbers (NaN and infinities are a 500). | RFC 7493 §2 | R | `TestEncodeStringsMatch`, `TestOutputs` | ✅ |
| 8.2 | A `time.Time` whose offset has seconds (`LMT`) is written in UTC, since RFC 3339 offsets have minutes only. | RFC 3339 §5.6 (time-numoffset) | R | `TestProtocol`, `TestEncodeEdges` | ✅ |
| 8.3 | The response schema marks as required what is always written; nil slices and maps are `[]` and `{}`. | the document says what is sent | R | `TestResponsesMatchDocument` | ✅ |
| 8.4 | `enum` is the only rule checked on responses: the response schema lists it, and a value outside it is a logged 500, so a client can rely on it. Other rules and defaults are left out of response schemas. | the document says what is sent | P | `TestDescriptionsExamplesAndResponseEnums`, `TestRegistrationPanics` | ✅ |

## 9. Errors (RFC 9457)

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 9.1 | Every error is `application/problem+json`; `status` equals the HTTP status; without `type` (about:blank), `title` is the status phrase. | RFC 9457 §3.1, §4.2.1 | R | `TestProtocol` | ✅ |
| 9.2 | The `errors` extension lists each problem with `detail`, `code` (the JSON Schema keyword, or `additionalProperties`, `duplicate`, `format`, `syntax`), `in`, and `pointer` (RFC 6901) or `parameter`. | RFC 9457 §3.2 (extensions); §3 example uses `errors` with `pointer` | S | `TestProtocol` | ✅ |
| 9.3 | `pointer` is a plain RFC 6901 pointer (`/age`), not the URI fragment form of the RFC 9457 example (`#/age`): the member is already about the body. | RFC 6901 §5; RFC 9457 §3 (non-normative example) | S | `TestProtocol` | ✅ |
| 9.4 | Rejected values are never echoed. | — | P | `TestProtocol`, `TestNoEcho` | ✅ |

## 10. OpenAPI document

| # | Behaviour | Reference | Class | Test | Status |
|---|---|---|---|---|---|
| 10.1 | The document is OpenAPI 3.1.2. | OAS 3.1.2; see the note below | C | `TestDocumentServed` | ✅ |
| 10.2 | The document validates against the official schema (`schema-base`, with the dialect, so that schemas are checked too). | OAS schema 3.1 `2026-11-05`, dialect `2024-11-10` | R | `TestJSONSchemaSuite`, `TestResponsesMatchDocument` | ✅ |
| 10.3 | Every request body the server accepts validates against the documented schema, and every one it rejects does not, except the listed rows: class A (2.2 float overflow, 2.8, 3.5, 3.6, 3.7) and JSON text rules that schemas cannot express (1.4). Parameters are not compared. | the document says what is enforced | R | `TestJSONSchemaSuite`, `TestProtocol`, `TestRFC3339` | ✅ |
| 10.4 | Each response sent validates against the documented response schema. | idem | R | `TestResponsesMatchDocument` | ✅ |
| 10.5 | `description` documents a body field, a parameter or a response header; `example` gives one value, checked against the rules of its field at registration and written as JSON Schema `examples`. | JSON Schema Validation §9.1, §9.5; OAS 3.1.2 Parameter Object, Header Object | R | `TestDescriptionsExamplesAndResponseEnums`, `TestRegistrationPanics` | ✅ |

**Note on 10.1.** OpenAPI 3.2 can declare `QUERY`; 3.1 cannot (its schema rejects a `query` key). But openapi-generator (all its generators, through swagger-parser), @apidevtools/swagger-parser and openapi-python-client refuse any document whose version is 3.2, even one that uses only 3.1 features; Scalar, Swagger UI, Redoc, oapi-codegen, ogen, orval, hey-api, openapi-typescript and swagger_dart_code_generator accept it (tested 2026-11-23). rally therefore writes 3.1.2, which every tool reads.

## 11. Registration (rally policy)

| # | Behaviour | Class | Test | Status |
|---|---|---|---|---|
| 11.1 | Misspelled, unsupported or misplaced tags panic, with the fix in the message. | P | `TestRegistrationPanics`, `TestTags` | ✅ |
| 11.2 | Rules that no value can pass panic. | P | `TestRegistrationPanics` | ✅ |
| 11.3 | Every list in a request body needs `maxItems`, every map `maxProperties`. | P | `TestRegistrationPanics` | ✅ |
| 11.4 | A struct type is used in requests or in responses, not both. | P | `TestOneDirectionPerType` | ✅ |
| 11.5 | `New` panics for a second API on the same mux, with the fix in the message. | P | `TestNewPanics` | ✅ |
