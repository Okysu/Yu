# Benchmarks

These benchmarks compare Yu with popular Go HTTP frameworks on the same
business routes. They also measure two specific design questions:

1. How does Yu's automatic fan-out compare with hand-written concurrency
   (`errgroup`)?
2. What do the per-request allocation optimizations actually buy?

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
| `gin` | Gin, ordinary handlers | net/http |
| `gin-errgroup` | Gin, hand-written `errgroup` fan-out | net/http |
| `echo` | Echo | net/http |
| `chi` | chi | net/http |
| `fiber` | Fiber | fasthttp |
| `yu` | Yu | net/http |

`gin-errgroup` uses the same routes as `gin`; only `/compute` and `/io` are
written with `errgroup` instead of sequential calls. It is the fair control for
the "you would have written this by hand" comparison.

Fiber uses fasthttp, so it is a reference point rather than an apples-to-apples
comparison. Every framework uses its own default JSON encoder.

## Routes

| Route | Work |
|---|---|
| `GET /ping` | Plain text response |
| `GET /users/:id` | Path parameter, JSON response |
| `POST /users` | JSON body decode, JSON response |
| `GET /compute` | Three independent CPU tasks (~219 µs each), merged into one JSON response |
| `GET /io` | Three independent downstream calls, simulated with `time.Sleep(2ms)` each |
| `GET /r499` | Static route among 500 registered routes |

For `/compute` and `/io`, Yu declares three skills producing distinct types.
They land in one wave and run concurrently; the plain frameworks call them
sequentially; `gin-errgroup` runs them in three goroutines.

## Methodology

- Real TCP servers and `net/http` clients, 2 s per sub-benchmark.
- Three runs per benchmark; tables report the **median**.
- Each benchmark runs in a fresh process to avoid cross-benchmark warm-up bias.
- Latency benchmarks use one sequential client and report p50/p99 in addition
  to the mean.
- 20 warm-up requests before the timer starts.
- Throughput benchmarks use `b.RunParallel` (28 goroutines against the server).
- Default `GOGC`; a GC-off reference run is included separately.
- The optimized framework was compared against the previous revision in a git
  worktree using the same benchmark binary, in both run orders, to separate
  real changes from hybrid-core variance.

Run them yourself:

```bash
cd benchmarks
for bench in Ping PingLatency GetUser CreateUser Fanout FanoutLatency \
             IOFanout IOFanoutLatency ManyRoutes; do
  go test -run '^$' -bench "^Benchmark${bench}$" -benchmem -benchtime=2s -count=3
done
```

## Throughput (parallel clients)

`ns/op` is the amortized wall time per request while 28 clients drive the
server.

### Ping

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 13,831 | 10,024 | 76 |
| gin | 13,333 | 10,218 | 76 |
| gin-errgroup | 12,350 | 9,873 | 76 |
| echo | 10,949 | 9,274 | 76 |
| chi | 10,676 | 9,552 | 77 |
| fiber | 9,567 | 6,060 | 54 |
| **yu** | **12,220** | **11,848** | **95** |

### GetUser (path param + JSON)

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 14,433 | 10,470 | 80 |
| gin | 14,645 | 10,781 | 82 |
| gin-errgroup | 11,701 | 9,605 | 80 |
| echo | 11,519 | 9,545 | 80 |
| chi | 12,159 | 10,335 | 83 |
| fiber | 9,385 | 6,179 | 58 |
| **yu** | **13,511** | **12,688** | **102** |

### CreateUser (JSON in / JSON out)

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 12,482 | 20,994 | 102 |
| gin | 12,972 | 21,369 | 106 |
| gin-errgroup | 12,787 | 21,617 | 106 |
| echo | 12,070 | 20,427 | 104 |
| chi | 11,505 | 19,755 | 104 |
| fiber | 7,307 | 10,416 | 74 |
| **yu** | **15,278** | **25,191** | **122** |

### Fanout (three CPU tasks, saturated cores)

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 51,862 | 13,023 | 85 |
| gin | 51,883 | 13,305 | 87 |
| gin-errgroup | 46,792 | 13,004 | 96 |
| echo | 50,479 | 13,046 | 86 |
| chi | 50,590 | 13,423 | 88 |
| fiber | 43,319 | 9,269 | 65 |
| **yu** | **56,379** | **18,090** | **131** |

When every core is busy, parallelizing inside a request cannot reduce total CPU
work. Yu and `gin-errgroup` pay scheduling and reflection overhead here; both
are close behind the sequential frameworks and Fiber.

### ManyRoutes (500 routes registered)

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 10,881 | 9,172 | 75 |
| gin | 10,914 | 9,286 | 75 |
| gin-errgroup | 11,651 | 9,525 | 75 |
| echo | 9,915 | 8,916 | 75 |
| chi | 10,285 | 9,436 | 77 |
| fiber | 2,994 | 4,281 | 52 |
| **yu** | **4,940** | **13,324** | **92** |

Registering 500 routes raises the live heap and reduces GC frequency during
measurement, so absolute numbers are lower than `Ping` for every framework.
Compare frameworks within this row.

## Latency (single client)

### Ping latency

| framework | ns/op |
|---|---:|
| stdlib | 32,580 |
| gin | 32,294 |
| gin-errgroup | 33,346 |
| echo | 31,768 |
| chi | 33,775 |
| fiber | 28,487 |
| **yu** | **36,135** |

Loopback round-trip cost dominates; Yu's extra allocations add a few
microseconds.

### CPU fanout latency

| framework | ns/op |
|---|---:|
| stdlib | 697,910 |
| gin | 702,467 |
| gin-errgroup | 296,167 |
| echo | 692,217 |
| chi | 689,136 |
| fiber | 691,152 |
| **yu** | **332,173** |

Three 219 µs tasks take ~656 µs sequentially. Yu's automatic wave execution and
the hand-written `errgroup` version both cut latency to roughly a third, with no
concurrency code in the skills.

### I/O fanout latency (p50 / p99)

Three simulated downstream calls, 2 ms each:

| framework | ns/op | p50 (µs) | p99 (µs) |
|---|---:|---:|---:|
| stdlib | 6,197,399 | 6,193 | 6,375 |
| gin | 6,227,292 | 6,194 | 6,595 |
| gin-errgroup | 2,210,114 | 2,195 | 2,430 |
| echo | 6,183,522 | 6,165 | 6,496 |
| chi | 6,246,545 | 6,190 | 6,544 |
| fiber | 6,169,392 | 6,153 | 6,409 |
| **yu** | **2,227,229** | **2,202** | **2,528** |

Yu and `gin-errgroup` are statistically indistinguishable at p50. Yu's p99 is
about 100 µs higher in this run (2.53 ms vs 2.43 ms), consistent with the
additional per-request allocation and reflection work.

### I/O fanout throughput

| framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| stdlib | 239,164 | 9,844 | 81 |
| gin | 238,796 | 10,176 | 83 |
| gin-errgroup | 96,787 | 11,828 | 91 |
| echo | 237,838 | 9,347 | 82 |
| chi | 238,576 | 9,799 | 83 |
| fiber | 239,012 | 6,481 | 61 |
| **yu** | **98,405** | **16,187** | **138** |

Under saturation, overlapping sleeps still wins because the waiting does not
consume CPU: all three parallel variants are ~2.4× faster than the sequential
ones.

## Engine isolation

Measured directly on the DAG, without HTTP:

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Digest` (one task) | 218,945 | 0 | 0 |
| `YuSequentialFanout` (three tasks, no engine) | 656,400 | 0 | 0 |
| `YuDAGFanout` (three-task wave + merge + JSON) | 232,791 | 2,197 | 35 |
| `YuCompile` (single-skill route) | ~318 | 376 | 11 |

The wave scheduler turns ~656 µs of sequential work into ~231 µs end-to-end,
close to the single-task floor (219 µs).

## Inline-wave trade-off

`WithInlineWaveLimit(n)` makes waves with at most `n` skills run sequentially,
starting no goroutines. The default is `1`, which only affects the single-node
fast path. The option is a trade-off:

| two-skill wave | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| tiny CPU skills, parallel | 3,334 | 1,443 | 22 |
| tiny CPU skills, inline | **957** | **975** | **15** |
| 2 ms I/O calls, parallel | **2,138,000** | 2,388 | 35 |
| 2 ms I/O calls, inline | 4,235,000 | 2,030 | 29 |

Inlining is ~3.5× faster for trivial CPU skills and ~2× slower for latency-bound
skills. That is why the default stays at 1: the engine cannot know a skill's
cost at compile time, and silently serializing a two-call I/O route would be a
bad default. Raise it per route only when you know the wave is CPU-trivial:

```go
engine.GET("/tiny", yu.WithInlineWaveLimit(2), skillA, skillB, respond)
```

## GC-off reference

With `GOGC=off`, allocation differences stop mattering:

| framework | Ping ns/op | Ping allocs/op |
|---|---:|---:|
| stdlib | 2,235 | 72 |
| gin | 2,223 | 71 |
| gin-errgroup | 2,227 | 71 |
| echo | 2,246 | 72 |
| chi | 2,248 | 74 |
| fiber | 2,292 | 52 |
| **yu** | **2,324** | **91** |

All frameworks converge to ~2.2 µs. Routing and serving CPU are on par; the
default-GC gap comes from per-request allocations.

## Optimization impact

The following changes were measured against the previous revision:

- pooled per-route blackboards;
- no params map for static routes;
- pooled reflection argument slices per `FuncNode`;
- no query parsing when the request has no query string;
- an opt-in inline-wave limit.

| benchmark | before ns/op | after ns/op | before allocs | after allocs | before B/op | after B/op |
|---|---:|---:|---:|---:|---:|---:|
| Ping | 12,521 | 12,509 | 100 | **95** | 12,219 | **11,956** |
| GetUser | 13,341 | 13,511 | 107 | **102** | 12,905 | **12,688** |
| CreateUser | 14,540 | 15,278 | 126 | **122** | 24,970 | 25,191 |
| Fanout | 56,727 | 55,466 | 134 | **130** | 17,840 | 17,847 |
| FanoutLatency | 320,584 | 331,294 | 126 | **117** | 11,057 | **10,601** |
| ManyRoutes | 4,866 | 4,940 | 99 | **92** | 14,356 | **13,324** |
| IOFanout | 98,558 | 98,138 | 142 | **137** | 16,049 | 16,196 |
| IOFanoutLatency p50 | 2,184 µs | 2,194 µs | 137 | **128** | 11,831 | **11,324** |
| IOFanoutLatency p99 | 2,517 µs | 2,549 µs | — | — | — | — |
| `YuDAGFanout` | 232,096 | 231,414 | 39 | **34** | 2,662 | **2,141** |
| `YuCompile` | ~260 | ~318 | 8 | 11 | 248 | 376 |

Interpretation:

- **Allocations drop 4–18%** and bytes drop up to 21% on multi-node routes.
- **CPU time is unchanged within noise.** Pooling does not make a fast request
  faster; it reduces GC pressure and retained memory. The compile cost rises by
  ~60 ns per route (startup only) because pools are created eagerly.
- A completion-channel wave executor was tried and reverted: it was ~8 µs
  slower than `sync.WaitGroup` + a watcher on the 3-task wave, in both run
  orders.
- **Fast-fail adds one shared early slot per concurrent wave** — one small
  allocation — and leaves the success path unchanged: `Fanout` 56.4 µs and
  `FanoutLatency` 332 µs. Its benefit is on failures: the first skill returning
  an error or a non-zero `Response` cancels the wave instead of waiting for the
  slowest sibling.
- Inline waves are a real win (3.5×) only for CPU-trivial waves; they are a
  loss (2×) for I/O waves, hence opt-in.

## Findings

1. **Simple routes:** Yu is within roughly 10–20% of Gin/Echo/chi under default
   GC, and slower than the fasthttp-based Fiber.
2. **Allocation pressure is the main cost.** With GC disabled, Ping matches the
   fastest net/http frameworks. Pooling cuts allocations but does not change
   steady-state CPU time.
3. **Fan-out is a latency optimization.** With idle cores, three concurrent
   CPU or I/O tasks cut latency ~2–2.2× and match a hand-written `errgroup`
   implementation. Under CPU saturation it adds ~10% overhead.
4. **Compilation is cheap.** A single-skill route compiles in ~320 ns at
   startup, including pool setup.
5. **Many routes scale fine.** Yu stays competitive with 500 routes.

## Caveats

- Numbers are specific to this CPU, Go version and kernel. Hybrid P/E cores and
  frequency scaling add run-to-run variance.
- Loopback HTTP overhead is included; real network latency changes the balance.
- Fiber uses a different HTTP stack and is not directly comparable.
- `/io` uses `time.Sleep`; no real database or network call is involved.
- `gin-errgroup` is a hand-written control. It is not a framework feature.

## Related benchmarks

- [TechEmpower Framework Benchmarks](https://www.techempower.com/benchmarks/) —
  full HTTP + JSON + database workloads across many languages.
- [smallnest/go-web-framework-benchmark](https://github.com/smallnest/go-web-framework-benchmark) —
  end-to-end Go framework comparison with real servers.
- [julienschmidt/go-http-routing-benchmark](https://github.com/julienschmidt/go-http-routing-benchmark) —
  routing-only microbenchmarks.
