package yu

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// PartitionKeyFunc defines how to extract a sharding routing key (such as item_id or user_id)
// from a frozen Request.
type PartitionKeyFunc func(req Request) string

// RouteOption attaches scheduling policies (such as PartitionBy) to a route.
type RouteOption interface {
	apply(cfg *routeConfig)
}

type routeConfig struct {
	keyFn           PartitionKeyFunc
	workers         int
	timeout         time.Duration
	bodyLimit       int64
	inlineWaveLimit int
}

type partitionOption struct {
	keyFn   PartitionKeyFunc
	workers int
}

func (p partitionOption) apply(cfg *routeConfig) {
	cfg.keyFn = p.keyFn
	cfg.workers = p.workers
}

// PartitionBy configures the route to execute partitioned by the hash of the specified key.
// By default, it initializes 32 independent lock-free worker queues.
func PartitionBy(keyFn PartitionKeyFunc, workers ...int) RouteOption {
	w := 32
	if len(workers) > 0 && workers[0] > 0 {
		w = workers[0]
	}
	return partitionOption{keyFn: keyFn, workers: w}
}

type timeoutOption struct {
	d time.Duration
}

func (t timeoutOption) apply(cfg *routeConfig) {
	cfg.timeout = t.d
}

// WithTimeout sets a timeout for the route execution.
func WithTimeout(d time.Duration) RouteOption {
	return timeoutOption{d: d}
}

type bodyLimitOption struct {
	maxBytes int64
}

func (o bodyLimitOption) apply(cfg *routeConfig) {
	cfg.bodyLimit = o.maxBytes
}

// WithBodyLimit caps the request body size for a route. Larger bodies are
// rejected with 413 Request Entity Too Large. maxBytes <= 0 disables the limit.
func WithBodyLimit(maxBytes int64) RouteOption {
	return bodyLimitOption{maxBytes: maxBytes}
}

type inlineWaveOption struct {
	maxNodes int
}

func (o inlineWaveOption) apply(cfg *routeConfig) {
	cfg.inlineWaveLimit = o.maxNodes
}

// WithInlineWaveLimit runs waves containing at most maxNodes skills
// sequentially instead of spawning goroutines. The default is 1 (only
// single-node waves are inlined). Raise it when tiny waves are dominated by
// scheduling overhead; keep it low when waves contain real I/O.
func WithInlineWaveLimit(maxNodes int) RouteOption {
	return inlineWaveOption{maxNodes: maxNodes}
}

type partitionTask struct {
	fn   func() (Response, error)
	done chan partitionResult
}

type partitionResult struct {
	resp Response
	err  error
}

// PartitionScheduler maintains a fixed pool of worker goroutines to guarantee serial execution
// for identical keys while executing distinct keys concurrently.
//
// A scheduler is created per partitioned route, so the same key on two routes
// is not mutually exclusive. Lanes are hash buckets: different keys may map to
// the same lane and queue behind each other.
type PartitionScheduler struct {
	lanes []chan partitionTask
	keyFn PartitionKeyFunc

	mu     sync.RWMutex
	closed bool
}

// NewPartitionScheduler initializes a PartitionScheduler with the specified number of worker
// lanes and key extraction function.
func NewPartitionScheduler(workers int, keyFn PartitionKeyFunc) *PartitionScheduler {
	if workers <= 0 {
		workers = 1
	}
	lanes := make([]chan partitionTask, workers)
	for i := 0; i < workers; i++ {
		// Each channel includes a buffer queue to smoothly absorb traffic bursts.
		ch := make(chan partitionTask, 256)
		lanes[i] = ch
		go func(queue <-chan partitionTask) {
			for task := range queue {
				task.done <- runPartitionTask(task.fn)
			}
		}(ch)
	}
	return &PartitionScheduler{
		lanes: lanes,
		keyFn: keyFn,
	}
}

// laneFor returns the lane index a key is pinned to.
func (ps *PartitionScheduler) laneFor(key string) int {
	return laneIndex(fnv32a(key), len(ps.lanes))
}

// laneIndex reduces a uint32 hash in unsigned space. Converting the hash to
// int first would produce a negative index on 32-bit platforms for hashes with
// the high bit set.
func laneIndex(h uint32, lanes int) int {
	return int(h % uint32(lanes))
}

// fnv32a is an allocation-free FNV-1a hash, matching hash/fnv.New32a.
func fnv32a(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

// ErrSchedulerClosed is returned when dispatching to a closed scheduler.
var ErrSchedulerClosed = errors.New("yu: partition scheduler is closed")

// Dispatch routes a request to its designated worker queue based on the hashed key
// and blocks until execution completes or ctx is cancelled.
//
// Cancellation is honoured while the task waits in the queue and while it runs,
// so a route timeout is never stretched by a backlogged lane.
func (ps *PartitionScheduler) Dispatch(ctx context.Context, req Request, exec func() (Response, error)) (Response, error) {
	lane := ps.lanes[ps.laneFor(ps.keyFn(req))]
	done := make(chan partitionResult, 1)

	ps.mu.RLock()
	if ps.closed {
		ps.mu.RUnlock()
		return Response{}, ErrSchedulerClosed
	}
	select {
	case lane <- partitionTask{fn: exec, done: done}:
		ps.mu.RUnlock()
	case <-ctx.Done():
		ps.mu.RUnlock()
		return Response{}, mapContextErr(ctx.Err())
	}

	select {
	case res := <-done:
		return res.resp, res.err
	case <-ctx.Done():
		// The task may still run later; done is buffered so the worker never blocks.
		return Response{}, mapContextErr(ctx.Err())
	}
}

// Close stops accepting new work and closes the lane queues. Each worker drains
// whatever is already buffered before exiting. Close is idempotent and waits
// for in-flight enqueues to finish.
func (ps *PartitionScheduler) Close() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.closed {
		return
	}
	ps.closed = true
	for _, lane := range ps.lanes {
		close(lane)
	}
}

// runPartitionTask isolates a lane from panics: a crashing task must not
// permanently kill the worker goroutine that serializes a shard.
func runPartitionTask(fn func() (Response, error)) (result partitionResult) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Yu Recovery] Panic recovered in partition worker: %v\n%s", r, debug.Stack())
			result = partitionResult{err: fmt.Errorf("yu: partition worker panicked: %v", r)}
		}
	}()
	resp, err := fn()
	return partitionResult{resp: resp, err: err}
}
