package yu

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

var (
	respType  = reflect.TypeOf(Response{})
	errorType = reflect.TypeOf((*error)(nil)).Elem()
	ctxType   = reflect.TypeOf((*context.Context)(nil)).Elem()
	reqType   = reflect.TypeOf(Request{})
)

// FuncNode wraps static metadata of a single atomic skill (reflected once at startup, zero metadata parsing overhead at runtime)
type FuncNode struct {
	Name     string
	Fn       reflect.Value
	InTypes  []reflect.Type
	OutTypes []reflect.Type

	argsPool sync.Pool // recycles []reflect.Value argument slices per node
}

// CompiledDAG is the startup-compiled execution plan of a route.
//
// Waves are built by parsing type dependencies. Skills in the same wave run
// concurrently; skills in later waves consume the values produced by earlier
// waves. Every data type has exactly one producer, so causality is never
// ambiguous. Response is not data: it is the terminal signal, may only be
// produced (never consumed), and at most one skill per wave may produce it.
type CompiledDAG struct {
	RouteKey string
	Nodes    []*FuncNode
	Waves    [][]*FuncNode // Execution waves for parallel scheduling

	blackboards *blackboardPool
	// inlineWaveLimit runs waves with at most this many nodes sequentially.
	// The default (and minimum when unset) is 1, which only affects the
	// single-node fast path. Raising it trades parallelism for less
	// goroutine/scheduling overhead on tiny waves.
	inlineWaveLimit int
}

// blackboard holds the per-request type-keyed values passed between skills.
// Instances are pooled per DAG; values are cleared when returned.
type blackboard struct {
	values map[reflect.Type]reflect.Value
}

type blackboardPool struct {
	pool sync.Pool
}

func newBlackboardPool(size int) *blackboardPool {
	p := &blackboardPool{}
	p.pool.New = func() any {
		return &blackboard{values: make(map[reflect.Type]reflect.Value, size)}
	}
	return p
}

func (p *blackboardPool) get() *blackboard {
	return p.pool.Get().(*blackboard)
}

func (p *blackboardPool) put(b *blackboard) {
	// Drop references so pooled maps do not retain request bodies or outputs.
	clear(b.values)
	p.pool.Put(b)
}

// CompileDAG compiles a DAG into a CompiledDAG.
//
// Compilation is strict on purpose: ambiguous causality, impossible flows and
// non-deterministic terminal signals are rejected at startup instead of
// surfacing as wrong answers at runtime.
func CompileDAG(routeKey string, singletons map[reflect.Type]reflect.Value, rawFuncs ...any) *CompiledDAG {
	if len(rawFuncs) == 0 {
		panic(fmt.Sprintf("yu: route [%s] must have at least one skill node", routeKey))
	}

	nodes := make([]*FuncNode, 0, len(rawFuncs))
	availableTypes := make(map[reflect.Type]bool, len(singletons)+2)

	// pre-register global singletons
	availableTypes[reqType] = true
	availableTypes[ctxType] = true
	for t := range singletons {
		availableTypes[t] = true
	}

	// producers enforces single assignment per data type.
	producers := make(map[reflect.Type]string)
	responseProducers := 0

	// parse all nodes
	for i, raw := range rawFuncs {
		v := reflect.ValueOf(raw)
		if !v.IsValid() || v.Kind() != reflect.Func {
			panic(fmt.Sprintf("yu: route [%s] node #%d is not a function", routeKey, i))
		}

		t := v.Type()
		name := getFuncName(v)

		if t.IsVariadic() {
			panic(fmt.Sprintf("yu: route [%s] node #%d [%s] is variadic; skills must declare fixed parameters", routeKey, i, name))
		}

		inTypes := make([]reflect.Type, t.NumIn())
		for j := range inTypes {
			inTypes[j] = t.In(j)
		}

		outTypes := make([]reflect.Type, t.NumOut())
		for j := range outTypes {
			outTypes[j] = t.Out(j)
		}

		for _, in := range inTypes {
			if in == respType {
				panic(fmt.Sprintf(
					"yu: route [%s] node #%d [%s] takes yu.Response as input; Response is a terminal signal, not data",
					routeKey, i, name))
			}
		}

		for j, out := range outTypes {
			if out == errorType && j != len(outTypes)-1 {
				panic(fmt.Sprintf(
					"yu: route [%s] node #%d [%s] returns error before other values; error must be the last return value",
					routeKey, i, name))
			}
		}

		for _, out := range outTypes {
			if out == errorType {
				continue
			}
			if out == respType {
				responseProducers++
				continue
			}
			if out == reqType {
				panic(fmt.Sprintf(
					"yu: route [%s] node #%d [%s] produces yu.Request; request data is already provided and cannot be shadowed",
					routeKey, i, name))
			}
			if out == ctxType {
				panic(fmt.Sprintf(
					"yu: route [%s] node #%d [%s] produces context.Context; the request context is already provided and cannot be shadowed",
					routeKey, i, name))
			}
			if _, isSingleton := singletons[out]; isSingleton {
				panic(fmt.Sprintf(
					"yu: route [%s] node #%d [%s] produces %v, which collides with a provided singleton",
					routeKey, i, name, out))
			}
			if prev, ok := producers[out]; ok {
				panic(fmt.Sprintf(
					"yu: route [%s] output type %v is produced by both [%s] and [%s]; a type has exactly one producer — declare a distinct type for a refined value",
					routeKey, out, prev, name))
			}
			producers[out] = name
		}

		node := &FuncNode{
			Name:     name,
			Fn:       v,
			InTypes:  inTypes,
			OutTypes: outTypes,
		}
		node.argsPool.New = func() any {
			args := make([]reflect.Value, len(node.InTypes))
			return &args
		}
		nodes = append(nodes, node)
	}

	if responseProducers == 0 {
		panic(fmt.Sprintf("yu: route [%s] has no skill producing yu.Response", routeKey))
	}

	pending := make([]*FuncNode, len(nodes))
	copy(pending, nodes)

	var waves [][]*FuncNode

	// topological sort to build execution waves
	for len(pending) > 0 {
		var currentWave []*FuncNode
		var nextPending []*FuncNode

		for _, node := range pending {
			ready := true
			for _, in := range node.InTypes {
				if !availableTypes[in] {
					ready = false
					break
				}
			}
			if ready {
				currentWave = append(currentWave, node)
			} else {
				nextPending = append(nextPending, node)
			}
		}

		if len(currentWave) == 0 {
			var missingDetails []string
			for _, n := range nextPending {
				for _, in := range n.InTypes {
					if !availableTypes[in] {
						missingDetails = append(missingDetails, fmt.Sprintf("%s needs %v", n.Name, in))
					}
				}
			}
			panic(fmt.Sprintf("yu: route [%s] unresolvable or cyclic dependencies: %v", routeKey, missingDetails))
		}

		for _, node := range currentWave {
			for _, out := range node.OutTypes {
				if out != errorType && out != respType {
					availableTypes[out] = true
				}
			}
		}

		waves = append(waves, currentWave)
		pending = nextPending
	}

	// A wave executes concurrently, so it may only contain a single terminal
	// signal producer. Multiple producers across different waves are allowed:
	// an earlier wave can terminate the route before a later fallback wave runs.
	for waveIndex, wave := range waves {
		var waveProducers []string
		for _, n := range wave {
			for _, out := range n.OutTypes {
				if out == respType {
					waveProducers = append(waveProducers, n.Name)
					break
				}
			}
		}
		if len(waveProducers) > 1 {
			panic(fmt.Sprintf(
				"yu: route [%s] wave %d has multiple skills producing yu.Response: %v; a wave runs concurrently, so its terminal signal must be unique",
				routeKey, waveIndex, waveProducers))
		}
	}

	return &CompiledDAG{
		RouteKey:        routeKey,
		Nodes:           nodes,
		Waves:           waves,
		blackboards:     newBlackboardPool(len(singletons) + len(nodes) + 2),
		inlineWaveLimit: 1,
	}
}

// SetInlineWaveLimit runs waves containing at most n skills sequentially
// instead of spawning goroutines. It must be called before the DAG is used
// concurrently. Values below 1 are treated as 1 (only single-node waves are
// inlined).
func (dag *CompiledDAG) SetInlineWaveLimit(n int) {
	if n < 1 {
		n = 1
	}
	dag.inlineWaveLimit = n
}

// Run executes the compiled DAG for a single request using concurrent wave scheduling,
// propagating timeouts/cancellations and recovering from panics across worker goroutines.
func (dag *CompiledDAG) Run(ctx context.Context, singletons map[reflect.Type]reflect.Value, req Request) (Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Attach the latest context (with timeout/cancellation signals) to the frozen request
	// so skills can propagate it downstream to database or HTTP clients.
	req.Context = ctx

	var blackboard map[reflect.Type]reflect.Value

	// keepBoard is set when returning while wave goroutines may still read the
	// map; such a board is dropped instead of being cleared and pooled.
	keepBoard := false
	if dag.blackboards != nil {
		board := dag.blackboards.get()
		defer func() {
			if !keepBoard {
				dag.blackboards.put(board)
			}
		}()
		blackboard = board.values
	} else {
		blackboard = make(map[reflect.Type]reflect.Value, len(singletons)+len(dag.Nodes)+2)
	}

	for k, v := range singletons {
		blackboard[k] = v
	}
	blackboard[reqType] = reflect.ValueOf(req)
	blackboard[ctxType] = reflect.ValueOf(ctx)

	inlineLimit := dag.inlineWaveLimit
	if inlineLimit < 1 {
		inlineLimit = 1
	}

	for _, wave := range dag.Waves {
		// Check for request timeout or client disconnection before entering the next wave.
		if err := ctx.Err(); err != nil {
			return Response{}, mapContextErr(err)
		}

		// Sequential path: single-node waves and any wave explicitly capped by
		// WithInlineWaveLimit. No goroutine is started for these.
		if len(wave) <= inlineLimit {
			for _, n := range wave {
				args, err := n.acquireArgs(blackboard)
				if err != nil {
					return Response{}, err
				}
				outs, err := safeCall(n, *args)
				n.releaseArgs(args)
				if err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return Response{}, mapContextErr(ctxErr)
					}
					return Response{}, err
				}
				if resp, ok := absorbOutputs(outs, blackboard); ok {
					return resp, nil
				}
				// A context-aware skill may answer cancellation with a zero
				// Response; report that as a timeout rather than as a pipeline
				// that finished without a response.
				if err := ctx.Err(); err != nil {
					return Response{}, mapContextErr(err)
				}
			}
			continue
		}

		// Concurrent path.
		type nodeResult struct {
			outs []reflect.Value
			err  error
		}
		results := make([]nodeResult, len(wave))
		var wg sync.WaitGroup
		wg.Add(len(wave))

		for idx, node := range wave {
			go func(i int, n *FuncNode) {
				defer wg.Done()
				args, err := n.acquireArgs(blackboard)
				if err != nil {
					results[i] = nodeResult{err: err}
					return
				}
				// Guard each child goroutine with safeCall to capture panics and errors.
				outs, callErr := safeCall(n, *args)
				n.releaseArgs(args)
				results[i] = nodeResult{outs: outs, err: callErr}
			}(idx, node)
		}

		doneCh := make(chan struct{})
		go func() {
			wg.Wait()
			close(doneCh)
		}()

		// Wait for every node in this wave, or abort early on cancellation/timeout.
		select {
		case <-doneCh:
		case <-ctx.Done():
			keepBoard = true
			cancel()
			return Response{}, mapContextErr(ctx.Err())
		}

		for _, res := range results {
			if res.err != nil {
				// Capture the context error before cancelling, otherwise our own
				// cancel would mask a deadline with context.Canceled.
				ctxErr := ctx.Err()
				cancel()
				if ctxErr != nil {
					return Response{}, mapContextErr(ctxErr)
				}
				return Response{}, res.err
			}
			if resp, ok := absorbOutputs(res.outs, blackboard); ok {
				cancel()
				return resp, nil
			}
		}

		if err := ctx.Err(); err != nil {
			cancel()
			return Response{}, mapContextErr(err)
		}
	}

	return Response{}, fmt.Errorf("yu: pipeline finished without producing Response")
}

// acquireArgs fills a pooled argument slice for the node's declared inputs.
func (n *FuncNode) acquireArgs(blackboard map[reflect.Type]reflect.Value) (*[]reflect.Value, error) {
	argsPtr := n.argsPool.Get().(*[]reflect.Value)
	args := *argsPtr
	if len(args) != len(n.InTypes) {
		args = make([]reflect.Value, len(n.InTypes))
		*argsPtr = args
	}

	for i, inType := range n.InTypes {
		v, ok := blackboard[inType]
		if !ok {
			n.releaseArgs(argsPtr)
			return nil, fmt.Errorf("yu: missing dependency %v for skill [%s]", inType, n.Name)
		}
		args[i] = v
	}
	return argsPtr, nil
}

// releaseArgs clears references and returns the slice to the node's pool.
func (n *FuncNode) releaseArgs(argsPtr *[]reflect.Value) {
	for i := range *argsPtr {
		(*argsPtr)[i] = reflect.Value{}
	}
	n.argsPool.Put(argsPtr)
}

// absorbOutputs records non-Response outputs on the blackboard and reports a
// non-zero Response as the terminal result of the route.
func absorbOutputs(outs []reflect.Value, blackboard map[reflect.Type]reflect.Value) (Response, bool) {
	for _, out := range outs {
		if out.Type() == respType {
			r := out.Interface().(Response)
			if r.Status != 0 || len(r.Body) > 0 {
				return r, true
			}
			continue
		}
		blackboard[out.Type()] = out
	}
	return Response{}, false
}

// mapContextErr converts a deadline expiry into ErrRouteTimeout.
func mapContextErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrRouteTimeout
	}
	return err
}

// getFuncName extracts the short name of a function for logging and debugging.
func getFuncName(v reflect.Value) string {
	fullName := runtime.FuncForPC(v.Pointer()).Name()
	if idx := strings.LastIndex(fullName, "."); idx != -1 {
		return fullName[idx+1:]
	}
	return fullName
}

var ErrRouteTimeout = errors.New("yu: route execution timed out")

// safeCall avoids a single skill panic crashing the entire server, converting it into a standard error return.
func safeCall(n *FuncNode, args []reflect.Value) (outs []reflect.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Yu Recovery] Panic recovered in skill [%s]: %v\n%s", n.Name, r, debug.Stack())
			err = fmt.Errorf("skill [%s] panicked: %v", n.Name, r)
		}
	}()

	outs = n.Fn.Call(args)
	if len(outs) > 0 && outs[len(outs)-1].Type() == errorType {
		if !outs[len(outs)-1].IsNil() {
			err = outs[len(outs)-1].Interface().(error)
		}
		outs = outs[:len(outs)-1]
	}
	return outs, err
}
