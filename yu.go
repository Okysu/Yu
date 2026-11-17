package yu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
)

// Engine is the core of the Yu framework.
type Engine struct {
	*RouterGroup
	router     *router
	groups     []*RouterGroup
	singletons map[reflect.Type]reflect.Value // global singletons
}

// Request is an immutable request packet (value passed, naturally thread-safe).
type Request struct {
	Method     string
	Path       string
	RawQuery   string
	Query      map[string][]string
	Params     map[string]string
	Header     map[string][]string
	Body       []byte
	RemoteAddr string
	Context    context.Context
}

// Param returns the dynamic route parameter by key (e.g., ":id" or "*filepath").
func (r Request) Param(key string) string {
	if val, ok := r.Params[key]; ok {
		return val
	}
	return ""
}

// QueryDefault returns the first query value or a default.
func (r Request) QueryDefault(key, def string) string {
	if vals, ok := r.Query[key]; ok && len(vals) > 0 {
		return vals[0]
	}
	return def
}

// BindJSON decodes the request body into dst.
func (r Request) BindJSON(dst any) error {
	return json.Unmarshal(r.Body, dst)
}

// Response is the final immutable response packet produced by the business pipeline.
type Response struct {
	Status int
	Header map[string]string
	Body   []byte
}

// JSON creates a JSON response.
func JSON(status int, data any) Response {
	b, _ := json.Marshal(data)
	return Response{
		Status: status,
		Header: map[string]string{"Content-Type": "application/json; charset=utf-8"},
		Body:   b,
	}
}

// Text creates a plain text response.
func Text(status int, body string) Response {
	return Response{
		Status: status,
		Header: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		Body:   []byte(body),
	}
}

// String returns the response body as a string.
func (r Response) String() string {
	return string(r.Body)
}

// Engine is the core of the Yu framework.
func New() *Engine {
	engine := &Engine{
		router:     newRouter(),
		singletons: make(map[reflect.Type]reflect.Value),
	}
	engine.RouterGroup = &RouterGroup{engine: engine}
	engine.groups = []*RouterGroup{engine.RouterGroup}
	return engine
}

// Provide registers global singletons into the engine.
func (engine *Engine) Provide(items ...any) {
	for _, item := range items {
		if item == nil {
			panic("yu: cannot provide nil singleton")
		}
		v := reflect.ValueOf(item)
		engine.singletons[v.Type()] = v
	}
}

// Run starts the HTTP server on the given address.
func (engine *Engine) Run(addr string) error {
	return http.ListenAndServe(addr, engine)
}

// addRoute adds a route with a compiled DAG pipeline.
func (engine *Engine) addRoute(method, pattern string, items ...any) {
	key := method + "-" + pattern
	var skillFuncs []any
	var cfg routeConfig

	for _, item := range items {
		if opt, ok := item.(RouteOption); ok {
			opt.apply(&cfg)
		} else {
			skillFuncs = append(skillFuncs, item)
		}
	}

	dag := CompileDAG(key, engine.singletons, skillFuncs...)
	var scheduler *PartitionScheduler
	modeDesc := "Lock-Free Parallel"
	if cfg.keyFn != nil {
		scheduler = NewPartitionScheduler(cfg.workers, cfg.keyFn)
		modeDesc = fmt.Sprintf("Sharded(%d lanes)", cfg.workers)
	}

	fmt.Printf("[Yu Compiler] Route %-22s -> %d wave(s) | Mode: %s\n", key, len(dag.Waves), modeDesc)
	engine.router.addRoute(method, pattern, &routeEntry{
		dag:       dag,
		scheduler: scheduler,
		timeout:   cfg.timeout,
	})
}

// ServeHTTP implements http.Handler.
func (engine *Engine) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	n, params := engine.router.getRoute(req.Method, req.URL.Path)
	if n == nil {
		http.NotFound(w, req)
		return
	}

	key := req.Method + "-" + n.pattern
	entry := engine.router.entries[key]

	// freeze request into immutable value
	var bodyBytes []byte
	if req.Body != nil {
		defer req.Body.Close()
		bodyBytes, _ = io.ReadAll(req.Body)
	}

	frozenReq := Request{
		Method:     req.Method,
		Path:       req.URL.Path,
		Header:     req.Header.Clone(),
		RawQuery:   req.URL.RawQuery,
		Query:      req.URL.Query(),
		Body:       bodyBytes,
		Context:    req.Context(),
		RemoteAddr: req.RemoteAddr,
		Params:     params,
	}

	ctx := req.Context()
	if entry.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, entry.timeout)
		defer cancel()
	}

	execFn := func() (Response, error) {
		return entry.dag.Run(ctx, engine.singletons, frozenReq)
	}

	var resp Response
	var err error
	if entry.scheduler != nil {
		resp, err = entry.scheduler.Dispatch(frozenReq, execFn)
	} else {
		resp, err = execFn()
	}

	if err != nil {
		if errors.Is(err, ErrRouteTimeout) {
			http.Error(w, "504 Gateway Timeout: "+err.Error(), http.StatusGatewayTimeout)
			return
		}
		http.Error(w, fmt.Sprintf("500 Internal Server Error: %v", err), http.StatusInternalServerError)
		return
	}

	for k, v := range resp.Header {
		w.Header().Set(k, v)
	}
	if resp.Status == 0 {
		resp.Status = http.StatusOK
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}
