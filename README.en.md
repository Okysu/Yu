# Yu

> Guide, don't block. Let causality drive itself.

English | [简体中文](README.md)

Yu is a type-driven HTTP framework for Go.

You write only lock-free pure functions — atomic skills. Hand them to a route and the engine compiles a topology at startup from their parameter and return types: **independent skills run in parallel across cores, and writes sharing a key are sharded into per-key queues**. No middleware, no service/controller split, no hand-written concurrency glue.

- A route is a set of plain functions whose causality is expressed by **Go types**
- Skills in the same wave run concurrently; later waves consume values from earlier ones
- Every data type has exactly one producer, so causality is never ambiguous
- `Response` is a terminal signal, not data: at most one producer per wave, with cross-wave early exit allowed
- A panicking skill only fails the current request; it never takes down the process or a shard lane
- Startup compilation: ambiguous causality, unreachable dependencies and illegal signatures fail fast

Requires Go 1.26+.

## Install

```bash
go get github.com/Okysu/Yu
```

## Documentation and examples

- [Quickstart](docs/quickstart.md)
- [Advanced usage](docs/advanced.md)
- [API reference](docs/api.md)
- [Benchmarks](docs/benchmarks.md)
- Runnable examples: [`examples/quickstart`](examples/quickstart),
  [`examples/advanced`](examples/advanced)

## Quick start

```go
package main

import (
	"database/sql"
	"time"

	yu "github.com/Okysu/Yu"
)

type User struct {
	ID   int
	Name string
}

type Stats struct {
	Orders int
}

func LoadUser(db *sql.DB, req yu.Request) (User, error) {
	// SELECT ... WHERE id = req.Param("id")
	return User{}, nil
}

func LoadStats(db *sql.DB, req yu.Request) (Stats, error) {
	// SELECT ...
	return Stats{}, nil
}

func Render(u User, s Stats) yu.Response {
	return yu.JSON(200, map[string]any{"user": u, "stats": s})
}

func main() {
	db, _ := sql.Open("postgres", "dsn")

	e := yu.New()
	e.Provide(db)

	e.GET("/users/:id",
		yu.WithTimeout(2*time.Second),
		LoadUser,  // wave 0
		LoadStats, // wave 0, concurrent with LoadUser
		Render,    // wave 1, needs User + Stats
	)

	e.Run(":8080")
}
```

`LoadUser` and `LoadStats` both need only `*sql.DB` and `yu.Request`, so they land in the same wave and run concurrently. `Render` consumes both outputs and moves to the next wave automatically. No dependency declarations, goroutines or locks.

## Core concepts

### Atomic skills

A skill is an ordinary function:

```go
func LoadUser(db *sql.DB, req yu.Request) (User, error)
```

- Inputs are resolved from a type-keyed blackboard: `yu.Request`, `context.Context`, global singletons, and upstream outputs
- Return values are published to the blackboard; `error` must be the last return value and aborts the pipeline when non-nil
- Returning a non-zero `yu.Response` terminates the route and becomes the response

### Execution model

1. **Compile time**: reflect over each skill's input/output types, topologically sort, and cut the result into waves
2. **Run time**: execute wave by wave — concurrent within a wave, ordered across waves; each wave sees the blackboard as it stood at the end of the previous wave
3. **Single assignment**: each data type has exactly one producer. To express a refined value, declare a new type — a type's identity is the identity of its causality
4. **Terminal signal**: `yu.Response` may only be produced, never consumed; at most one producer per wave; different waves may each have one, enabling patterns like "return on cache hit, otherwise fall through"

### Sharded scheduling

When same-key writes must be serialized, declare it:

```go
func ParseOrder(req yu.Request) (Order, error) { /* ... */ }
func SaveOrder(db *sql.DB, o Order) yu.Response { /* ... */ }

e.POST("/orders/:id",
	yu.PartitionBy(func(req yu.Request) string {
		return req.Param("id")
	}),
	ParseOrder,
	SaveOrder,
)
```

Requests with the same key are pinned to the same lane and run serially; different keys run in parallel. Cancellation is honoured both while queued and while running, so a route timeout is never stretched by a backlogged lane.

### Global singletons

```go
e.Provide(db, logger) // injected by concrete type
```

Register before `Run`; duplicate types and typed-nil values panic at startup.

## Request accessors

| Method | Description |
|---|---|
| `req.Param("id")` | Path parameter (`:id` / `*filepath`) |
| `req.QueryDefault("q", "def")` | First query value, or a default |
| `req.Get("Content-Type")` | First header value (case-insensitive) |
| `req.Body()` | Raw body (read-only contract) |
| `req.BindJSON(&dst)` | Decode the body as JSON |
| `req.Context` | Context carrying timeout/cancellation |

Containers are private and exposed only through read-only accessors, so skills in the same wave cannot corrupt shared maps.

## Routing

| Option | Description |
|---|---|
| `yu.WithTimeout(d)` | Per-route timeout; returns 504 when exceeded |
| `yu.WithBodyLimit(n)` | Request body cap; returns 413 when exceeded (unlimited by default) |
| `yu.WithInlineWaveLimit(n)` | Runs waves with at most `n` skills sequentially with no goroutines (default `1`, single-node only) |
| `yu.PartitionBy(keyFn, workers...)` | Sharded serial scheduling, 32 lanes by default |

Match precedence: static segment > `:param` > `*catch-all`. Trailing slashes are ignored; two different wildcards at the same position are rejected. A known path with the wrong method returns 405 with an `Allow` header, otherwise 404.

Nodes passed to `Group("/api", nodes...)` or `Use(nodes...)` are not middleware. They join the same type-driven DAG as route nodes, and ordering is decided purely by types.

## Compile-time rules

These panic at startup instead of producing wrong answers at runtime:

- A route has no skill producing `yu.Response`
- Two skills produce the same data type
- Two skills in the same wave produce `yu.Response`
- `yu.Response` appears as an input
- `error` is not the last return value
- A skill is variadic
- Dependencies are unresolvable or cyclic
- Two different wildcards appear at the same path position
- A route pattern is empty
- `Provide` is called twice for the same type, or with a typed-nil

## Observability

```go
e.EnableDebugDAG("/debug/dag")
```

Open `/debug/dag` to inspect each route's compiled wave topology; `/debug/dag?format=json` returns the same data structurally. The inspector itself is excluded from the listing.

## Project layout

```
yu.go         Engine, Request, Response, HTTP entry point
dag.go        skill reflection, topological compilation, wave scheduling, panic/timeout handling
http_error.go status-coded errors
router.go     trie routing and route entries
trie.go       route tree nodes
group.go      route groups
partition.go  sharded scheduler and route options
debug.go      topology visualisation
docs/         documentation: quickstart, advanced, API, benchmarks
examples/     runnable examples: quickstart, advanced, middleware
benchmarks/   comparison harness as a separate module
```

## Design constraints

- Skills must be pure: do not mutate `Request`, singletons or upstream values. The framework keeps containers read-only, but object state is your responsibility.
- A concurrent wave fails fast: the first skill returning an error or a non-zero `Response` cancels its siblings and returns immediately. Cancellation is cooperative: it cannot preempt a running function or roll back side effects that already started. Single-node and inlined waves run sequentially.
- Cross-cutting concerns (CORS, compression, access logs, metrics) live outside the engine: `*Engine` is an `http.Handler`, so standard net/http middleware composes around it.
- Skill panics are recovered and logged server-side; clients only receive a 500 without internal details.
- When one value needs to be refined in stages, declare distinct types:

```go
type Price struct{ Cents int }
type DiscountedPrice Price

func FetchPrice(req yu.Request) Price
func ApplyDiscount(p Price) DiscountedPrice
func Checkout(p DiscountedPrice) yu.Response
```

## License

[MIT](LICENSE)
