# Advanced usage

This page covers the rules that make the DAG deterministic and the patterns
that follow from them.

## Execution semantics and their limits

Three rules explain most surprises:

1. **A concurrent wave fails fast, but cannot roll back.** The first skill in a
   wave that returns an error or a non-zero `Response` cancels the wave and the
   request returns without waiting for the slowest sibling. Siblings already
   running are only stopped if they observe the context. Single-node and
   inlined waves run sequentially, so nothing is cancelled mid-wave.
2. **Ordering comes from types, not registration.** A guard that only takes
   `yu.Request` lands in the same wave as any business skill that also only
   takes `yu.Request`. A 401 from the guard does not prevent the business skill
   from executing. Make the business skill consume a capability the guard
   produces:

   ```go
   type AuthUser struct{ ID string }

   func authenticate(req yu.Request) (AuthUser, error)       // wave 0
   func loadItems(req yu.Request, u AuthUser) ([]Item, error) // wave 1
   ```

   If authentication fails, the route stops before `loadItems` runs. Group
   nodes are ordinary DAG nodes; they are not middleware and add no barrier.
3. **Cancellation is cooperative.** A timeout or client disconnect closes the
   context; the engine observes it between waves and while waiting for a
   multi-node wave. It cannot preempt a running Go function, and it does not
   roll back side effects from waves that already completed. See
   [Timeouts and cancellation](#timeouts-and-cancellation).

## Causality is type identity

The engine keys the blackboard by Go type. That gives three rules:

1. **Every data type has exactly one producer.** Two skills returning the same
   type is a compile error, because downstream skills could not tell which
   value they received.
2. **A refined value needs a new type.** If one stage transforms `Price` into a
   discounted price, declare a distinct type. The type name is the causal
   identity of the value.
3. **`yu.Response` is a terminal signal, not data.** It may be produced but not
   consumed, and a wave may contain at most one producer because waves run
   concurrently.

```go
type Price struct{ Cents int }
type DiscountedPrice Price

func fetchPrice(req yu.Request) Price                // wave 0
func applyDiscount(p Price) DiscountedPrice          // wave 1
func checkout(p DiscountedPrice) yu.Response         // wave 2
```

Registering `applyDiscount` to return `Price` instead would fail at startup
with a duplicate-producer error. That is intentional: without a new type the
graph hides a version of the value, and consumers in different waves would
silently observe different values.

## Early exit across waves

Multiple `yu.Response` producers are allowed **in different waves**. This
expresses "return on cache hit, fall through on miss" without racing:

```go
type CacheMiss struct{ Miss bool }

func cacheLookup(req yu.Request) (yu.Response, CacheMiss) {
	// return yu.JSON(200, cached), CacheMiss{} on a hit
	return yu.Response{}, CacheMiss{Miss: true}
}

func loadFromDB(miss CacheMiss, req yu.Request) (Item, error) {
	// A hit already terminated the route; only misses reach this skill.
	return Item{ID: req.Param("id")}, nil
}

func reply(item Item) yu.Response {
	return yu.JSON(200, item)
}
```

Wave 0 contains `cacheLookup`. A non-zero response ends the request. Otherwise
`CacheMiss` is published, `loadFromDB` runs in wave 1, and so on.

Two `Response` producers in the **same** wave are rejected at startup: they
would run concurrently and produce a non-deterministic terminal signal.

## Groups and `Use`

Group nodes are **not middleware**. They are merged into the same type-driven
DAG as route nodes, so their position is decided by types, not registration
order:

```go
type RequestID string

func requestID(req yu.Request) RequestID {
	return RequestID(req.Get("X-Request-ID"))
}

api := engine.Group("/api", requestID)
api.POST("/items/:id", parseItem, storeItem, reply)
```

`requestID` runs in the first wave and its output is available to any skill in
the route that asks for `RequestID`.

`Use` appends nodes to a group for routes registered afterwards. Child groups
copy the parent's node list when they are created.

## Partitioned execution

`PartitionBy` pins requests to worker lanes by key. Same key ⇒ serialized;
different keys ⇒ parallel:

```go
engine.POST("/items/:id",
	yu.PartitionBy(func(req yu.Request) string {
		return req.Param("id")
	}, 16), // number of lanes; default 32
	parseItem, storeItem, reply,
)
```

The scheduler respects the request context both while a task waits in its lane
queue and while it runs. A timeout is never stretched by a backlogged lane.
Skill panics are recovered per task, so a shard worker never dies.

Scheduler semantics worth knowing:

- It is created **per route**. The same key on two different routes is not
  mutually exclusive.
- Lanes are hash buckets, not one queue per key. Different keys can map to the
  same lane and queue behind each other; choose `workers` accordingly.
- Each lane has a bounded queue (256). `Dispatch` blocks when it is full, or
  returns early when the request context is cancelled.
- `Engine.Close` closes every route scheduler. New dispatches return
  `ErrSchedulerClosed` (`503` over HTTP); tasks already buffered are drained by
  their workers before the lane exits.

## Timeouts and cancellation

`WithTimeout` derives a context with a deadline and passes it to the pipeline.
Skills can read it through `req.Context` or take `context.Context` directly:

```go
func slowQuery(ctx context.Context, req yu.Request) (Rows, error) {
	rows, err := db.QueryContext(ctx, "...", req.Param("id"))
	return rows, err
}

engine.GET("/report/:id", yu.WithTimeout(500*time.Millisecond), slowQuery, render)
```

Cancellation is cooperative, and wave shape matters:

- While a concurrent wave runs, the engine waits on completion, the first
  terminal signal, and the context. The client receives `504` at the deadline;
  sibling goroutines that ignore the context keep running until they finish.
- A single-node wave on a route with a deadline runs on its own goroutine and
  is raced against the context, so the client receives `504` at the deadline
  even if the skill ignores it. Waves inlined with
  `WithInlineWaveLimit(2+)` remain synchronous and are not preempted.
- Abandoned goroutines cannot be killed. Skills that perform I/O must watch
  `req.Context` or take `context.Context` directly.
- Completed waves are not rolled back. If a write must not survive a timeout,
  use a transaction or check the context before committing.
- A client disconnect cancels the request context; the pipeline returns at the
  next cancellation point.

A skill that answers cancellation with a zero `yu.Response` is still reported
as a timeout, not as an empty pipeline.

## Request body limits

Body limits are opt-in per route:

```go
engine.POST("/upload", yu.WithBodyLimit(1<<20), storeUpload)
```

Larger bodies are rejected with `413 Request Entity Too Large` before any skill
runs.

## Singletons

`Provide` registers values by their concrete type:

```go
engine.Provide(db, logger, clock)
```

Rules:

- Register before `Run`.
- Duplicate types panic.
- Typed-nil values panic.
- Interfaces are keyed by their dynamic type, so a skill that declares an
  interface parameter will not receive a concrete singleton. Declare the
  concrete type in your skills, or provide a wrapper struct.

## Errors and panics

`error` must be the last return value. A non-nil error aborts the route; by
default it returns `500`, unless it is an `*HTTPError` (see below). The full
error is written to the server log, not to the response body.

A panic in a skill is recovered, logged with a stack trace, and converted into
an error for that request only. Other requests, other waves and partition lanes
are unaffected.

Skills can fail with a status code instead of a bare 500:

```go
func validate(req yu.Request) (Input, error) {
	if req.QueryDefault("token", "") == "" {
		return Input{}, &yu.HTTPError{Status: 401, Err: errors.New("missing token")}
	}
	return Input{}, nil
}
```

`ServeHTTP` maps `*HTTPError` to its status (400–599) and logs the wrapped
error; the client receives only the status text. This changes the status code,
not the ordering: a sibling in the same wave still runs unless it observes the
cancelled context.

## HTTP-layer middleware

Cross-cutting concerns belong outside the engine. `*yu.Engine` implements
`http.Handler`, so ordinary net/http middleware composes:

```go
handler := withAccessLog(withCORS(engine))
http.ListenAndServe(":8080", handler)
```

CORS, compression, access logs and metrics all fit here; see
[`examples/middleware`](../examples/middleware). The engine deliberately has no
middleware concept inside the DAG: nodes are causal skills, not wrappers. A
wrapper can observe the final status and headers, but it cannot reorder or
cancel DAG nodes.

## Graceful shutdown

`Engine.Run` is a convenience wrapper around `http.ListenAndServe`, which has no
shutdown handle. Run your own server when you need a controlled stop:

```go
srv := &http.Server{Addr: ":8080", Handler: engine}
go func() { _ = srv.ListenAndServe() }()

// On SIGTERM:
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
_ = srv.Shutdown(ctx)
engine.Close()
```

`Engine.Close` stops accepting new partition dispatches and drains queued
tasks; it does not interrupt in-flight requests. Call it after `Shutdown`
returns.

## Testing

Skills are ordinary functions, so most of your logic can be tested directly:

```go
func TestSanitize(t *testing.T) {
	if got := sanitize(Item{Value: -1}); got.Value != 0 {
		t.Fatalf("got %+v", got)
	}
}
```

For the HTTP layer, use `httptest` against the engine:

```go
func TestRoute(t *testing.T) {
	engine := yu.New()
	engine.GET("/hello/:name", greet)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/hello/yu", nil))

	if w.Code != 200 || w.Body.String() != "hello, yu" {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}
```

Because `Request` containers are private, construct requests through
`httptest.NewRequest`; there is no way for a skill to mutate shared request
state.

## Debugging the topology

```go
engine.EnableDebugDAG("/debug/dag")
```

- `/debug/dag` renders waves and type signatures.
- `/debug/dag?format=json` returns `[]RouteTopologyDTO`.

The inspector never lists itself.

## Inline waves

By default only single-node waves run inline; multi-node waves spawn one
goroutine per skill. If a wave is CPU-trivial, goroutine setup can cost more
than the work it overlaps:

```go
engine.GET("/tiny", yu.WithInlineWaveLimit(2), skillA, skillB, respond)
```

The default is `1`. The measured trade-off with two skills:

| wave | parallel | inline |
|---|---:|---:|
| trivial CPU skills | 3.3 µs | **0.96 µs** |
| 2 ms I/O calls | **2.14 ms** | 4.24 ms |

Raise the limit only when the wave contains no I/O. The engine cannot infer
cost at compile time, which is why serializing small waves is opt-in rather
than a default. A single-node wave on a route with a deadline always uses the
concurrent path so the timeout can fire.

## Performance notes

- Keep skills pure and allocation-light; the engine adds reflection calls and
  one blackboard map per request.
- Use `PartitionBy` for writes that must be ordered by key, not as a global
  lock.
- Intra-request parallelism pays off for independent I/O or CPU work; at high
  concurrency, the Go scheduler already has plenty of runnable work, so measure
  before assuming a win.
- The [benchmarks](../benchmarks) directory compares the same business routes
  against several popular Go frameworks.
