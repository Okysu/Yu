# Benchmarks

These benchmarks compare Yu with popular Go HTTP frameworks on the same
business routes. The goal is to show both the costs and the benefits of a
type-driven DAG, not to declare a winner.

The harness lives in [`benchmarks/`](../benchmarks). It is a separate Go module
so the framework itself stays dependency-free.

## Environment

| | |
|---|---|
| CPU | Intel Core i7-14700KF (28 hardware threads) |
| OS | Linux 7.0.0 |
| Go | 1.26.6 linux/amd64 |
| GOMAXPROCS | 28 |
| Transport | loopback TCP, HTTP/1.1 keep-alive |
| Client | `net/http` with `MaxIdleConnsPerHost=4096`, compression off |

Frameworks compared:

| Label | Framework | HTTP stack |
|---|---|---|
| `stdlib` | `net/http` + Go 1.22 `ServeMux` | net/http |
| `gin` | Gin | net/http |
| `echo` | Echo | net/http |
| `chi` | chi | net/http |
| `fiber` | Fiber | fasthttp |
| `yu` | Yu | net/http |

Fiber uses fasthttp, so it is a reference point rather than an apples-to-apples
comparison. All JSON encoding uses each framework's default encoder.

## Routes

Every implementation serves the same business logic:

| Route | Work |
|---|---|
| `GET /ping` | Plain text response |
| `GET /users/:id` | Path parameter, JSON response |
| `POST /users` | JSON body decode, JSON response |
| `GET /compute` | Three independent CPU tasks, merged into one JSON response |
| `GET /r499` | Static route among 500 registered routes |

For `/compute`, Yu declares three skills producing distinct types. They land in
one wave and run concurrently; the other frameworks call the three functions
sequentially. That is the same business logic, expressed in each framework's
natural style.

Each task (`digest`) takes about **219 µs** on this machine.

## Methodology

- Real TCP servers, real `net/http` clients, 2 s per sub-benchmark.
- Three runs per benchmark; the table reports the **median**.
- Each benchmark runs in a fresh process to avoid cross-benchmark warm-up bias.
- 20 warm-up requests before the timer starts.
- Throughput benchmarks use `b.RunParallel` (28 goroutines against the server).
- Latency benchmarks use a single sequential client to keep most cores idle.
- Default `GOGC`. A GC-off run is included separately because allocation
  pressure turns out to dominate several results.

Run them yourself (each benchmark in a fresh process, matching the numbers
above):

```bash
cd benchmarks
for bench in Ping PingLatency GetUser CreateUser Fanout FanoutLatency ManyRoutes; do
  go test -run '^$' -bench "^Benchmark${bench}$" -benchmem -benchtime=2s -count=3
done
```

For a quick overview in one process:

```bash
cd benchmarks
go test -run '^$' -bench . -benchmem -benchtime=2s
```

## Throughput (parallel clients)

`ns/op` is the amortized wall time per request while 28 clients drive the
server.

### Ping

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 13,655 | 10,120 | 76 |
| gin | 12,579 | 10,052 | 76 |
| echo | 10,265 | 9,084 | 75 |
| chi | 10,865 | 9,609 | 77 |
| fiber | 9,384 | 6,022 | 54 |
| **yu** | **12,521** | **12,219** | **100** |

### GetUser (path param + JSON)

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 13,574 | 10,281 | 80 |
| gin | 14,341 | 10,735 | 82 |
| echo | 11,204 | 9,518 | 79 |
| chi | 11,596 | 10,279 | 83 |
| fiber | 9,406 | 6,196 | 58 |
| **yu** | **13,341** | **12,905** | **107** |

### CreateUser (JSON in / JSON out)

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 12,312 | 21,000 | 102 |
| gin | 12,749 | 21,768 | 106 |
| echo | 11,594 | 20,092 | 104 |
| chi | 12,732 | 22,318 | 105 |
| fiber | 7,379 | 9,199 | 73 |
| **yu** | **14,540** | **24,970** | **126** |

### Fanout (three CPU tasks, saturated cores)

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 50,934 | 13,035 | 85 |
| gin | 51,144 | 13,276 | 87 |
| echo | 50,112 | 13,152 | 86 |
| chi | 50,574 | 13,558 | 88 |
| fiber | 44,293 | 9,600 | 64 |
| **yu** | **56,727** | **17,840** | **134** |

Under saturation every core is busy, so parallelizing *inside* a request does
not reduce total CPU work; Yu pays goroutine and reflection overhead instead.
This is the expected trade-off.

### ManyRoutes (500 routes registered)

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 10,944 | 9,198 | 75 |
| gin | 10,703 | 9,148 | 75 |
| echo | 9,882 | 8,657 | 75 |
| chi | 10,383 | 9,192 | 76 |
| fiber | 2,854 | 4,261 | 52 |
| **yu** | **4,866** | **14,356** | **99** |

Note: registering 500 routes raises the live heap, which reduces GC frequency
during measurement. Absolute numbers are therefore lower than `Ping` for every
framework; compare frameworks within this row, not across rows.

## Latency (single client)

With one request in flight, most cores are idle and intra-request parallelism
can pay off.

### Ping latency

| framework | ns/op |
|---|---:|
| stdlib | 32,669 |
| gin | 33,295 |
| echo | 33,922 |
| chi | 33,185 |
| fiber | 28,265 |
| **yu** | **35,370** |

Loopback round-trip cost dominates; Yu's extra allocations add a few
microseconds.

### Fanout latency

| framework | ns/op |
|---|---:|
| stdlib | 691,629 |
| gin | 691,162 |
| echo | 691,861 |
| chi | 692,651 |
| fiber | 688,103 |
| **yu** | **320,584** |

The three tasks take ~657 µs sequentially. Yu's automatic fan-out cuts the
request latency to about **2.15×** below the others, with no concurrency code
in the skills.

## Engine isolation

Measured directly on the DAG, without HTTP:

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Digest` (one task) | 218,945 | 0 | 0 |
| `YuSequentialFanout` (three tasks, no engine) | 657,439 | 0 | 0 |
| `YuDAGFanout` (three-task wave + merge + JSON) | 232,096 | 2,662 | 39 |
| `YuCompile` (single-skill route) | ~260 | 248 | 8 |

The wave scheduler turns 657 µs of sequential work into 232 µs end-to-end,
close to the single-task floor (219 µs).

## GC-off reference

With `GOGC=off`, the per-request allocation differences stop mattering:

| framework | Ping ns/op | Ping allocs/op |
|---|---:|---:|
| stdlib | 2,262 | 72 |
| gin | 2,165 | 71 |
| echo | 2,180 | 72 |
| chi | 2,196 | 74 |
| fiber | 2,245 | 52 |
| **yu** | **2,261** | **97** |

All frameworks converge to ~2.2 µs. Routing and serving CPU are on par; the
gap under default GC comes from Yu's per-request allocations (blackboard map,
reflection argument slices).

## Findings

1. **Simple routes:** Yu is within roughly 10–20% of Gin/Echo/chi under default
   GC, and slightly slower than the fasthttp-based Fiber.
2. **Allocation pressure is the main cost.** Yu creates one blackboard map and
   reflection argument slices per request. With GC disabled, Ping matches the
   fastest net/http frameworks.
3. **Fan-out is a latency optimization, not a throughput one.** With idle
   cores, three concurrent skills are ~2.15× faster end-to-end. Under CPU
   saturation, the same fan-out adds ~12% overhead.
4. **Compilation is cheap.** A single-skill route compiles in ~260 ns at
   startup.
5. **Many routes scale fine.** Yu's trie plus precompiled pipelines stay
   competitive at 500 routes.

## Caveats

- Numbers are specific to this CPU, Go version and kernel. Hybrid P/E cores and
  frequency scaling add run-to-run variance.
- Loopback HTTP overhead is included; real network latency changes the balance.
- Fiber uses a different HTTP stack and is not directly comparable.
- No database or external I/O is involved; the `/compute` route simulates CPU
  work.

## Possible optimizations

The allocation gap suggests concrete follow-ups:

- pool the per-request blackboard per compiled route;
- avoid allocating the params map when a route has no path parameters;
- cache reflection argument slices per `FuncNode`;
- avoid spawning goroutines for waves whose skills are cheaper than the
  scheduling overhead.

## Related benchmarks

- [TechEmpower Framework Benchmarks](https://www.techempower.com/benchmarks/) —
  full HTTP + JSON + database workloads across many languages.
- [smallnest/go-web-framework-benchmark](https://github.com/smallnest/go-web-framework-benchmark) —
  end-to-end Go framework comparison with real servers.
- [julienschmidt/go-http-routing-benchmark](https://github.com/julienschmidt/go-http-routing-benchmark) —
  routing-only microbenchmarks.
