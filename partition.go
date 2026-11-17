package yu

import (
	"hash/fnv"
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
	keyFn   PartitionKeyFunc
	workers int
	timeout time.Duration
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
type PartitionScheduler struct {
	lanes []chan partitionTask
	keyFn PartitionKeyFunc
}

// NewPartitionScheduler initializes a PartitionScheduler with the specified number of worker
// lanes and key extraction function.
func NewPartitionScheduler(workers int, keyFn PartitionKeyFunc) *PartitionScheduler {
	lanes := make([]chan partitionTask, workers)
	for i := 0; i < workers; i++ {
		// Each channel includes a buffer queue to smoothly absorb traffic bursts.
		ch := make(chan partitionTask, 256)
		lanes[i] = ch
		go func(queue <-chan partitionTask) {
			for task := range queue {
				resp, err := task.fn()
				task.done <- partitionResult{resp: resp, err: err}
			}
		}(ch)
	}
	return &PartitionScheduler{
		lanes: lanes,
		keyFn: keyFn,
	}
}

// Dispatch routes a request to its designated worker queue based on the hashed key
// and blocks until execution completes.
func (ps *PartitionScheduler) Dispatch(req Request, exec func() (Response, error)) (Response, error) {
	key := ps.keyFn(req)
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	laneIdx := int(h.Sum32()) % len(ps.lanes)

	done := make(chan partitionResult, 1)
	ps.lanes[laneIdx] <- partitionTask{
		fn:   exec,
		done: done,
	}

	res := <-done
	return res.resp, res.err
}
