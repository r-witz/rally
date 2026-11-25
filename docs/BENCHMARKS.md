# Benchmarks

The [benchmark suite](../tests/bench) serves the same three endpoints with rally, Huma v2.40.0 (also without its `$schema` link transformer, "Huma no links"), Gin v1.12.0 (validator v10.30.5 `binding` tags expressing the same rules), and hand-written `net/http` + `encoding/json` **without any validation**, as a reference. Before timing anything, tests check that they all return the same responses and reject the same invalid inputs.

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

## Why no routing benchmark

Gin and others publish the [Go HTTP routing benchmark](https://github.com/gin-gonic/go-http-routing-benchmark): 203 GitHub API routes, each answered by an empty handler. It measures how fast a router finds the handler, and nothing else: no parameter is converted, no body is read, decoded or validated, no response is encoded. rally does not route by itself (it registers its operations on `http.ServeMux`), so that benchmark would measure the standard library, and leave out everything rally does. The benchmarks above serve whole requests instead.
