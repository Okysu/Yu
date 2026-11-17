package yu

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type dagA struct{ V int }
type dagB struct{ V int }
type dagC struct{ V int }
type dagDep struct{ Name string }
type dagSingleton struct{}

func dagSkillA(req Request) dagA                            { return dagA{V: 1} }
func dagSkillA2(req Request) dagA                           { return dagA{V: 2} }
func dagSkillB(req Request) dagB                            { return dagB{V: 2} }
func dagSkillFinalA(a dagA) Response                        { return Text(200, "final") }
func dagSkillNeedsC(req Request, c dagC) Response           { return Text(200, "c") }
func dagSkillCycleA(b dagB) dagA                            { return dagA{} }
func dagSkillCycleB(a dagA) dagB                            { return dagB{} }
func dagSkillPanic(req Request) dagA                        { panic("boom") }
func dagSkillUseDep(d *dagDep, req Request) Response        { return Text(200, d.Name) }
func dagSkillNeedsResponse(r Response) Response             { return Text(200, "x") }
func dagSkillVariadic(req Request, vals ...string) Response { return Text(200, "v") }
func dagSkillErrNotLast(req Request) (error, dagA)          { return nil, dagA{} }
func dagSkillRespZero(req Request) Response                 { return Response{} }
func dagSkillResp1(req Request) Response                    { return Text(200, "one") }
func dagSkillResp2(req Request) Response                    { return Text(200, "two") }

func dagSkillMerge(a dagA, b dagB) Response {
	return Text(200, fmt.Sprintf("a=%d,b=%d", a.V, b.V))
}

func dagSkillSlowA(req Request) dagA            { time.Sleep(200 * time.Millisecond); return dagA{} }
func dagSkillSlowB(req Request) dagB            { time.Sleep(200 * time.Millisecond); return dagB{} }
func dagSkillMergeSlow(a dagA, b dagB) Response { return Text(200, "merged") }

func dagSkillCtxAware(req Request) Response {
	select {
	case <-time.After(time.Second):
		return Text(200, "late")
	case <-req.Context.Done():
		return Response{}
	}
}

func dagSkillHasDeadline(req Request) Response {
	if _, ok := req.Context.Deadline(); ok {
		return Text(200, "yes")
	}
	return Text(200, "no")
}

func dagSkillErr(req Request) (Response, error) {
	return Response{}, errors.New("skill failed")
}

func dagSkillReturnsRequest(req Request) (Request, Response) {
	return req, Text(200, "shadowed")
}

func dagSkillReturnsContext(req Request) (context.Context, Response) {
	return req.Context, Text(200, "shadowed")
}

func dagSkillReturnsSingleton(req Request) (dagSingleton, Response) {
	return dagSingleton{}, Text(200, "shadowed")
}

func expectPanicContains(t *testing.T, substr string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected a panic containing %q", substr)
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, substr) {
			t.Fatalf("panic = %v, want a message containing %q", r, substr)
		}
	}()
	fn()
}

func expectPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected a compile-time panic")
		}
	}()
	fn()
}

func TestCompileBuildsWaves(t *testing.T) {
	dag := CompileDAG("GET-/w", nil, dagSkillA, dagSkillB, dagSkillMerge)

	if len(dag.Waves) != 2 {
		t.Fatalf("waves = %d, want 2", len(dag.Waves))
	}
	if len(dag.Waves[0]) != 2 || len(dag.Waves[1]) != 1 {
		t.Fatalf("wave sizes = %d/%d, want 2/1", len(dag.Waves[0]), len(dag.Waves[1]))
	}
	if len(dag.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(dag.Nodes))
	}
}

func TestRunParallelMerge(t *testing.T) {
	dag := CompileDAG("GET-/w", nil, dagSkillA, dagSkillB, dagSkillMerge)

	resp, err := dag.Run(context.Background(), nil, Request{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.String() != "a=1,b=2" {
		t.Fatalf("body = %q", resp.String())
	}
}

func TestCompileRequiresResponseProducer(t *testing.T) {
	expectPanic(t, func() {
		CompileDAG("GET-/w", nil, dagSkillA)
	})
}

func TestCompileRejectsMultipleResponseInSameWave(t *testing.T) {
	expectPanic(t, func() {
		CompileDAG("GET-/w", nil, dagSkillResp1, dagSkillResp2)
	})
}

func TestRunAllowsOneResponseProducerPerWave(t *testing.T) {
	hit := func(req Request) (Response, dagC) { return Text(200, "hit"), dagC{} }
	fallbackRan := false
	fallback := func(c dagC) Response {
		fallbackRan = true
		return Text(200, "fallback")
	}

	dag := CompileDAG("GET-/cache", nil, hit, fallback)
	if len(dag.Waves) != 2 {
		t.Fatalf("waves = %d, want 2", len(dag.Waves))
	}

	resp, err := dag.Run(context.Background(), nil, Request{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.String() != "hit" {
		t.Fatalf("body = %q, want hit", resp.String())
	}
	if fallbackRan {
		t.Fatal("later wave must not run after an earlier wave produced a response")
	}

	miss := func(req Request) (Response, dagC) { return Response{}, dagC{} }
	dag = CompileDAG("GET-/cache", nil, miss, fallback)
	fallbackRan = false

	resp, err = dag.Run(context.Background(), nil, Request{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.String() != "fallback" {
		t.Fatalf("body = %q, want fallback", resp.String())
	}
	if !fallbackRan {
		t.Fatal("later wave should run when the earlier wave produced no response")
	}
}

func TestCompileRejectsDuplicateProducers(t *testing.T) {
	expectPanic(t, func() {
		CompileDAG("GET-/w", nil, dagSkillA, dagSkillA2, dagSkillFinalA)
	})
}

func TestCompileRejectsMissingDependency(t *testing.T) {
	expectPanic(t, func() {
		CompileDAG("GET-/w", nil, dagSkillNeedsC)
	})
}

func TestCompileRejectsCyclicDependencies(t *testing.T) {
	expectPanic(t, func() {
		CompileDAG("GET-/w", nil, dagSkillCycleA, dagSkillCycleB, dagSkillFinalA)
	})
}

func TestCompileRejectsVariadicSkill(t *testing.T) {
	expectPanic(t, func() {
		CompileDAG("GET-/w", nil, dagSkillVariadic)
	})
}

func TestCompileRejectsMisplacedError(t *testing.T) {
	expectPanic(t, func() {
		CompileDAG("GET-/w", nil, dagSkillErrNotLast)
	})
}

func TestCompileRejectsResponseAsInput(t *testing.T) {
	expectPanic(t, func() {
		CompileDAG("GET-/w", nil, dagSkillResp1, dagSkillNeedsResponse)
	})
}

func TestCompileRejectsShadowingRequest(t *testing.T) {
	expectPanicContains(t, "yu.Request", func() {
		CompileDAG("GET-/w", nil, dagSkillReturnsRequest)
	})
}

func TestCompileRejectsShadowingContext(t *testing.T) {
	expectPanicContains(t, "context.Context", func() {
		CompileDAG("GET-/w", nil, dagSkillReturnsContext)
	})
}

func TestCompileRejectsShadowingSingleton(t *testing.T) {
	singletons := map[reflect.Type]reflect.Value{
		reflect.TypeOf(dagSingleton{}): reflect.ValueOf(dagSingleton{}),
	}

	expectPanicContains(t, "singleton", func() {
		CompileDAG("GET-/w", singletons, dagSkillReturnsSingleton)
	})
}

func TestRunRecoversFromPanic(t *testing.T) {
	dag := CompileDAG("GET-/w", nil, dagSkillPanic, dagSkillFinalA)

	_, err := dag.Run(context.Background(), nil, Request{})
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("err = %v, want a recovered panic", err)
	}
}

func TestRunRecoversFromPanicInParallelWave(t *testing.T) {
	dag := CompileDAG("GET-/w", nil, dagSkillPanic, dagSkillB, dagSkillMerge)

	_, err := dag.Run(context.Background(), nil, Request{})
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("err = %v, want a recovered panic", err)
	}
}

func TestRunTimeoutReturnsErrRouteTimeout(t *testing.T) {
	dag := CompileDAG("GET-/w", nil, dagSkillCtxAware)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := dag.Run(ctx, nil, Request{})
	if !errors.Is(err, ErrRouteTimeout) {
		t.Fatalf("err = %v, want ErrRouteTimeout", err)
	}
}

func TestRunTimeoutAbortsParallelWave(t *testing.T) {
	dag := CompileDAG("GET-/w", nil, dagSkillSlowA, dagSkillSlowB, dagSkillMergeSlow)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := dag.Run(ctx, nil, Request{})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrRouteTimeout) {
		t.Fatalf("err = %v, want ErrRouteTimeout", err)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("parallel wave waited for slow skills: %v", elapsed)
	}
}

func TestRunPropagatesDeadline(t *testing.T) {
	dag := CompileDAG("GET-/w", nil, dagSkillHasDeadline)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resp, err := dag.Run(ctx, nil, Request{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.String() != "yes" {
		t.Fatalf("skill did not receive a deadline-bearing context")
	}
}

func TestRunInjectsSingletons(t *testing.T) {
	dep := &dagDep{Name: "singleton"}
	singletons := map[reflect.Type]reflect.Value{
		reflect.TypeOf(dep): reflect.ValueOf(dep),
	}

	dag := CompileDAG("GET-/w", singletons, dagSkillUseDep)

	resp, err := dag.Run(context.Background(), singletons, Request{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.String() != "singleton" {
		t.Fatalf("body = %q", resp.String())
	}
}

func TestRunReturnsSkillError(t *testing.T) {
	dag := CompileDAG("GET-/w", nil, dagSkillErr)

	_, err := dag.Run(context.Background(), nil, Request{})
	if err == nil || !strings.Contains(err.Error(), "skill failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunWithoutResponseReturnsError(t *testing.T) {
	dag := CompileDAG("GET-/w", nil, dagSkillRespZero)

	_, err := dag.Run(context.Background(), nil, Request{})
	if err == nil || !strings.Contains(err.Error(), "without producing Response") {
		t.Fatalf("err = %v", err)
	}
}
