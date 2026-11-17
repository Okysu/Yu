package yu

import (
	"context"
	"errors"
	"fmt"
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
)

// FuncNode wraps static metadata of a single atomic skill (reflected once at startup, zero metadata parsing overhead at runtime)
type FuncNode struct {
	Name     string
	Fn       reflect.Value
	InTypes  []reflect.Type
	OutTypes []reflect.Type
}

type CompiledDAG struct {
	RouteKey string
	Nodes    []*FuncNode
	Waves    [][]*FuncNode // Execution waves for parallel scheduling
}

// CompileDAG compiles a DAG into a CompiledDAG
func CompileDAG(routeKey string, singletons map[reflect.Type]reflect.Value, rawFuncs ...any) *CompiledDAG {
	if len(rawFuncs) == 0 {
		panic(fmt.Sprintf("yu: route [%s] must have at least one skill node", routeKey))
	}

	nodes := make([]*FuncNode, 0, len(rawFuncs))
	availableTypes := make(map[reflect.Type]bool)

	// pre-register global singletons
	availableTypes[reflect.TypeOf(Request{})] = true
	availableTypes[ctxType] = true
	for t := range singletons {
		availableTypes[t] = true
	}

	producesResp := false

	// parse all nodes
	for i, raw := range rawFuncs {
		v := reflect.ValueOf(raw)
		if v.Kind() != reflect.Func {
			panic(fmt.Sprintf("yu: route [%s] node #%d is not a function", routeKey, i))
		}

		t := v.Type()

		inTypes := make([]reflect.Type, t.NumIn())
		for j := 0; j < t.NumIn(); j++ {
			inTypes[j] = t.In(j)
		}

		outTypes := make([]reflect.Type, t.NumOut())
		for j := 0; j < t.NumOut(); j++ {
			out := t.Out(j)
			outTypes[j] = out
			if out == respType {
				producesResp = true
			}
		}

		nodes = append(nodes, &FuncNode{
			Name:     getFuncName(v),
			Fn:       v,
			InTypes:  inTypes,
			OutTypes: outTypes,
		})
	}

	if !producesResp {
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
				if out != errorType {
					availableTypes[out] = true
				}
			}
		}

		waves = append(waves, currentWave)
		pending = nextPending
	}

	return &CompiledDAG{
		RouteKey: routeKey,
		Nodes:    nodes,
		Waves:    waves,
	}
}

// Run executes the compiled DAG for a single request using concurrent wave scheduling,
// propagating timeouts/cancellations and recovering from panics across worker goroutines.
func (dag *CompiledDAG) Run(ctx context.Context, singletons map[reflect.Type]reflect.Value, req Request) (Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Attach the latest context (with timeout/cancellation signals) to the frozen request
	// so skills can propagate it downstream to database or HTTP clients.
	req.Context = ctx

	blackboard := make(map[reflect.Type]reflect.Value, len(singletons)+len(dag.Nodes)+2)
	for k, v := range singletons {
		blackboard[k] = v
	}
	blackboard[reflect.TypeOf(req)] = reflect.ValueOf(req)
	blackboard[ctxType] = reflect.ValueOf(ctx)

	for _, wave := range dag.Waves {
		// Check for request timeout or client disconnection before entering the next wave.
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return Response{}, ErrRouteTimeout
			}
			return Response{}, err
		}

		// Fast path for single-node waves.
		if len(wave) == 1 {
			n := wave[0]
			args := make([]reflect.Value, len(n.InTypes))
			for i, inType := range n.InTypes {
				args[i] = blackboard[inType]
			}
			outs, err := safeCall(n, args)
			if err != nil {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return Response{}, ErrRouteTimeout
				}
				return Response{}, err
			}
			for _, out := range outs {
				if out.Type() == respType {
					r := out.Interface().(Response)
					if r.Status != 0 || len(r.Body) > 0 {
						return r, nil
					}
				} else {
					blackboard[out.Type()] = out
				}
			}
			continue
		}

		// Concurrent execution path for multi-node waves.
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
				args := make([]reflect.Value, len(n.InTypes))
				for j, inType := range n.InTypes {
					args[j] = blackboard[inType]
				}
				// Guard each child goroutine with safeCall to capture panics and errors.
				outs, callErr := safeCall(n, args)
				results[i] = nodeResult{outs: outs, err: callErr}
			}(idx, node)
		}

		// Wait for all child goroutines in this wave to complete, or abort early on cancellation/timeout.
		doneCh := make(chan struct{})
		go func() {
			wg.Wait()
			close(doneCh)
		}()

		select {
		case <-doneCh:
		case <-ctx.Done():
			cancel()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Response{}, ErrRouteTimeout
			}
			return Response{}, ctx.Err()
		}

		for _, res := range results {
			if res.err != nil {
				cancel()
				return Response{}, res.err
			}
			for _, out := range res.outs {
				if out.Type() == respType {
					r := out.Interface().(Response)
					if r.Status != 0 || len(r.Body) > 0 {
						cancel()
						return r, nil
					}
				} else {
					blackboard[out.Type()] = out
				}
			}
		}
	}

	if respVal, ok := blackboard[respType]; ok {
		return respVal.Interface().(Response), nil
	}
	return Response{}, fmt.Errorf("yu: pipeline finished without producing Response")
}

// execNode executes a single node via the fast path without goroutine overhead.
func execNode(n *FuncNode, blackboard map[reflect.Type]reflect.Value) (Response, bool, error) {
	args := make([]reflect.Value, len(n.InTypes))
	for i, inType := range n.InTypes {
		args[i] = blackboard[inType]
	}
	outs := n.Fn.Call(args)

	if len(outs) > 0 && outs[len(outs)-1].Type() == errorType {
		if !outs[len(outs)-1].IsNil() {
			return Response{}, false, outs[len(outs)-1].Interface().(error)
		}
		outs = outs[:len(outs)-1]
	}

	for _, out := range outs {
		if out.Type() == respType {
			r := out.Interface().(Response)
			if r.Status != 0 || len(r.Body) > 0 {
				return r, true, nil
			}
		} else {
			blackboard[out.Type()] = out
		}
	}
	return Response{}, false, nil
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
			fmt.Printf("[Yu Recovery] Panic recovered in skill [%s]: %v\n%s\n", n.Name, r, debug.Stack())
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
