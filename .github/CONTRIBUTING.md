# Contributing

Open an issue before a large change, so that we agree on the approach first:
rally stays small on purpose.

## Tests

```sh
go test ./...               # rally and its pattern engine
cd tests && go test ./...   # specification suites, and checks behind the benchmarks
```

`tests` is a separate module, so that its dependencies never reach rally's
users. Node.js, when installed, is the reference for regular expressions there.
CI also runs the tests with `-race`, and each fuzzer for 60 seconds;
[docs/DESIGN.md](../docs/DESIGN.md#testing) lists the commands.

## Rules

- No dependency outside the standard library.
- Every behaviour follows a specification, or is a choice listed in
  [docs/PROFILE.md](../docs/PROFILE.md) with the tests that check it.
- A change to the request path must not add allocations (`TestRallyAllocations`)
  or measurable time.
- Comments: godoc on exported identifiers only.
- `gofmt`, `go vet` and `staticcheck` pass.
