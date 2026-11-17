# Benchmarks

This directory is a separate Go module so Yu itself stays dependency-free.

It compares Yu with several popular Go frameworks on identical business routes:
plain text, path parameters, JSON in/out, a three-way CPU fan-out, a three-way
simulated I/O fan-out, and a 500-route lookup. Gin is also included with a
hand-written `errgroup` fan-out as the manual-concurrency control.

```bash
go test -run '^$' -bench . -benchmem -benchtime=2s -count=3
```

Focused runs:

```bash
go test -run '^$' -bench '^BenchmarkFanoutLatency$' -benchmem -count=3
go test -run '^$' -bench '^BenchmarkIOFanoutLatency$' -benchmem -benchtime=5s
go test -run '^$' -bench '^BenchmarkYu(TinyWave|IOWave)$' -benchmem -count=3
GOGC=off go test -run '^$' -bench '^BenchmarkPing$' -benchtime=2s
```

Methodology, results and interpretation live in
[`docs/benchmarks.md`](../docs/benchmarks.md).

Requirements: Go 1.26+, network access on the first run to download the
comparison frameworks.
