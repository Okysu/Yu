package yu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strings"
)

// Engine is the core of the Yu framework.
type Engine struct {
	*RouterGroup
	router     *router
	singletons map[reflect.Type]reflect.Value // global singletons
}

// Request is an immutable request packet. Its containers are unexported and
// only exposed through read-only accessors, so skills running in the same wave
// cannot mutate shared state by accident.
type Request struct {
	Method     string
	Path       string
	RawQuery   string
	Context    context.Context
	RemoteAddr string

	query  map[string][]string
	params map[string]string
	header http.Header
	body   []byte
}

// Param returns the dynamic route parameter by key (e.g., ":id" or "*filepath").
func (r Request) Param(key string) string {
	return r.params[key]
}

// QueryDefault returns the first query value or a default.
func (r Request) QueryDefault(key, def string) string {
	if vals, ok := r.query[key]; ok && len(vals) > 0 {
		return vals[0]
	}
	return def
}

// Get returns the first value of the named request header.
func (r Request) Get(key string) string {
	return r.header.Get(key)
}

// Body returns the raw request body. It must be treated as read-only.
func (r Request) Body() []byte {
	return r.body
}

// BindJSON decodes the request body into dst.
func (r Request) BindJSON(dst any) error {
	return json.Unmarshal(r.body, dst)
}

// Response is the final immutable response packet produced by the business pipeline.
type Response struct {
	Status int
	Header map[string]string
	Body   []byte
}

// JSON creates a JSON response. A marshalling failure is reported as 500
// instead of silently returning an empty body.
func JSON(status int, data any) Response {
	b, err := json.Marshal(data)
	if err != nil {
		return Response{
			Status: http.StatusInternalServerError,
			Header: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
			Body:   []byte("yu: failed to marshal JSON response: " + err.Error()),
		}
	}
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

// New returns a new Engine.
func New() *Engine {
	engine := &Engine{
		router:     newRouter(),
		singletons: make(map[reflect.Type]reflect.Value),
	}
	engine.RouterGroup = &RouterGroup{engine: engine}
	return engine
}

// Provide registers global singletons into the engine.
func (engine *Engine) Provide(items ...any) {
	for _, item := range items {
		if item == nil {
			panic("yu: cannot provide nil singleton")
		}

		v := reflect.ValueOf(item)
		switch v.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
			if v.IsNil() {
				panic(fmt.Sprintf("yu: cannot provide nil singleton for type %v", v.Type()))
			}
		}

		if _, exists := engine.singletons[v.Type()]; exists {
			panic(fmt.Sprintf("yu: singleton %v is already provided", v.Type()))
		}
		engine.singletons[v.Type()] = v
	}
}

// Run starts the HTTP server on the given address.
func (engine *Engine) Run(addr string) error {
	return http.ListenAndServe(addr, engine)
}

// Close releases the partition schedulers created for partitioned routes.
// It is safe to call more than once. In-flight requests are not interrupted;
// tasks already queued in a lane are drained by its worker.
func (engine *Engine) Close() {
	seen := make(map[*PartitionScheduler]struct{})
	for _, entry := range engine.router.entries {
		if entry.scheduler == nil {
			continue
		}
		if _, ok := seen[entry.scheduler]; ok {
			continue
		}
		seen[entry.scheduler] = struct{}{}
		entry.scheduler.Close()
	}
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
	if cfg.inlineWaveLimit > 0 {
		dag.SetInlineWaveLimit(cfg.inlineWaveLimit)
	}

	var scheduler *PartitionScheduler
	modeDesc := "Lock-Free Parallel"
	if cfg.keyFn != nil {
		scheduler = NewPartitionScheduler(cfg.workers, cfg.keyFn)
		modeDesc = fmt.Sprintf("Sharded(%d lanes)", cfg.workers)
	}

	log.Printf("[Yu Compiler] Route %-22s -> %d wave(s) | Mode: %s", key, len(dag.Waves), modeDesc)
	engine.router.addRoute(method, pattern, &routeEntry{
		dag:       dag,
		scheduler: scheduler,
		timeout:   cfg.timeout,
		bodyLimit: cfg.bodyLimit,
	})
}

// addRawRoute registers a plain net/http handler. Raw routes bypass the DAG,
// route options and group nodes.
func (engine *Engine) addRawRoute(method, pattern string, handler http.Handler) {
	key := method + "-" + pattern
	log.Printf("[Yu Compiler] Route %-22s -> raw http.Handler", key)
	engine.router.addRoute(method, pattern, &routeEntry{raw: handler})
}

// ServeHTTP implements http.Handler.
func (engine *Engine) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	entry, params := engine.router.getRoute(req.Method, req.URL.Path)
	if entry == nil {
		if allowed := engine.router.allowedMethods(req.URL.Path); len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		http.NotFound(w, req)
		return
	}

	// Raw handlers receive the untouched request. Path parameters are exposed
	// through the standard net/http accessor (Go 1.22+), so third-party
	// handlers can read them without knowing about Yu.
	if entry.raw != nil {
		for name, value := range params {
			req.SetPathValue(name, value)
		}
		entry.raw.ServeHTTP(w, req)
		return
	}

	// freeze request into immutable value
	var bodyBytes []byte
	if req.Body != nil {
		defer req.Body.Close()

		var reader io.Reader = req.Body
		if entry.bodyLimit > 0 {
			reader = http.MaxBytesReader(w, req.Body, entry.bodyLimit)
		}

		var readErr error
		bodyBytes, readErr = io.ReadAll(reader)
		if readErr != nil {
			var maxErr *http.MaxBytesError
			if errors.As(readErr, &maxErr) {
				http.Error(w, "413 Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "400 Bad Request: failed to read request body", http.StatusBadRequest)
			return
		}
	}

	// Parse the query string only when one is present.
	var query map[string][]string
	if req.URL.RawQuery != "" {
		query = req.URL.Query()
	}

	frozenReq := Request{
		Method:     req.Method,
		Path:       req.URL.Path,
		RawQuery:   req.URL.RawQuery,
		query:      query,
		params:     params,
		header:     req.Header.Clone(),
		body:       bodyBytes,
		RemoteAddr: req.RemoteAddr,
		Context:    req.Context(),
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
		resp, err = entry.scheduler.Dispatch(ctx, frozenReq, execFn)
	} else {
		resp, err = execFn()
	}

	if err != nil {
		if errors.Is(err, ErrRouteTimeout) {
			http.Error(w, "504 Gateway Timeout: "+err.Error(), http.StatusGatewayTimeout)
			return
		}
		if errors.Is(err, ErrSchedulerClosed) {
			http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
			return
		}

		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.Status >= 400 && httpErr.Status <= 599 {
			log.Printf("[Yu Error] %s %s: %v", req.Method, req.URL.Path, err)
			http.Error(w, http.StatusText(httpErr.Status), httpErr.Status)
			return
		}

		// Keep internal details in the server log, not in the response body.
		log.Printf("[Yu Error] %s %s: %v", req.Method, req.URL.Path, err)
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
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
