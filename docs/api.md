# API reference

Package import path:

```go
import yu "github.com/Okysu/Yu"
```

## Engine

```go
type Engine struct {
	*RouterGroup
	// unexported fields
}

func New() *Engine

func (engine *Engine) Provide(items ...any)
func (engine *Engine) Run(addr string) error
func (engine *Engine) ServeHTTP(w http.ResponseWriter, req *http.Request)
func (engine *Engine) EnableDebugDAG(path string)
```

`Engine` embeds `*RouterGroup`, so every route method is available directly on
the engine.

| Method | Description |
|---|---|
| `New` | Creates an engine with an empty router and singleton registry. |
| `Provide` | Registers global singletons by concrete type. Panics on nil, typed-nil, or duplicate types. Call before `Run`. |
| `Run` | Starts `http.ListenAndServe(addr, engine)`. |
| `ServeHTTP` | Standard `http.Handler` entry point; useful with `httptest`. |
| `EnableDebugDAG` | Registers a topology inspector at `path`. `?format=json` returns structured data. The route is flagged internal and excluded from the listing. |

## RouterGroup

```go
type RouterGroup struct {
	// unexported fields
}

func (group *RouterGroup) Group(relativePath string, nodes ...any) *RouterGroup
func (group *RouterGroup) Use(nodes ...any) *RouterGroup

func (group *RouterGroup) GET(pattern string, nodes ...any)
func (group *RouterGroup) POST(pattern string, nodes ...any)
func (group *RouterGroup) PUT(pattern string, nodes ...any)
func (group *RouterGroup) DELETE(pattern string, nodes ...any)
func (group *RouterGroup) PATCH(pattern string, nodes ...any)
func (group *RouterGroup) HEAD(pattern string, nodes ...any)
func (group *RouterGroup) OPTIONS(pattern string, nodes ...any)
func (group *RouterGroup) Any(pattern string, nodes ...any)
```

Route methods accept skills and `RouteOption` values in any order; options are
extracted and applied to the route.

Group nodes are merged into the same DAG as route nodes. They are not
middleware and have no registration-order semantics.

`Any` registers the route for `GET`, `POST`, `PUT`, `DELETE`, `PATCH`, `HEAD`
and `OPTIONS`.

### Patterns

| Pattern | Example | Matches |
|---|---|---|
| static | `/users/me` | `/users/me` |
| parameter | `/users/:id` | `/users/42` (`req.Param("id")`) |
| catch-all | `/files/*path` | `/files/a/b.txt` (`req.Param("path") == "a/b.txt"`) |

Static segments win over parameters at the same position. A catch-all must be
the last segment. Trailing slashes are ignored. Two different wildcards at the
same position panic at registration time.

If a path matches under a different method, the engine responds `405` with an
`Allow` header; otherwise `404`.

## Request

```go
type Request struct {
	Method     string
	Path       string
	RawQuery   string
	Context    context.Context
	RemoteAddr string
	// unexported query, params, header, body
}

func (r Request) Param(key string) string
func (r Request) QueryDefault(key, def string) string
func (r Request) Get(key string) string
func (r Request) Body() []byte
func (r Request) BindJSON(dst any) error
```

- `Param` returns a path parameter or `""`.
- `QueryDefault` returns the first query value or `def`.
- `Get` returns the first header value; header lookup is case-insensitive.
- `Body` returns the raw body. Treat it as read-only.
- `BindJSON` decodes the raw body into `dst`.
- `Context` carries the route timeout and client-disconnect cancellation.

The request is frozen before execution. Containers are unexported so skills
cannot mutate shared request state.

## Response

```go
type Response struct {
	Status int
	Header map[string]string
	Body   []byte
}

func JSON(status int, data any) Response
func Text(status int, body string) Response
func (r Response) String() string
```

A zero `Response` (`Status == 0` and empty body) means "continue the pipeline".
A non-zero response terminates the route and becomes the HTTP response.

`JSON` returns a `500` response if marshalling fails instead of silently
sending an empty body.

If a response has `Status == 0` but a non-empty body, `ServeHTTP` sends `200`.

## Route options

```go
type RouteOption interface {
	apply(cfg *routeConfig)
}

func WithTimeout(d time.Duration) RouteOption
func WithBodyLimit(maxBytes int64) RouteOption
func PartitionBy(keyFn PartitionKeyFunc, workers ...int) RouteOption
```

| Option | Default | Behavior |
|---|---|---|
| `WithTimeout` | no timeout | Derives a deadline context; expiry returns `504`. |
| `WithBodyLimit` | unlimited | Rejects larger bodies with `413` before running skills. `<= 0` disables the limit. |
| `PartitionBy` | off | Serializes same-key requests through one of `workers` lanes (default `32`). |
| `WithInlineWaveLimit(n)` | `1` | Runs waves containing at most `n` skills sequentially, starting no goroutines. |

`WithInlineWaveLimit` trades parallelism for scheduling overhead. The default
of `1` only affects the single-node fast path. Raise it for CPU-trivial waves;
do not raise it when a small wave contains I/O, because those calls would be
serialized.

## Partitioning

```go
type PartitionKeyFunc func(req Request) string

type PartitionScheduler struct {
	// unexported fields
}

func NewPartitionScheduler(workers int, keyFn PartitionKeyFunc) *PartitionScheduler

func (ps *PartitionScheduler) Dispatch(
	ctx context.Context,
	req Request,
	exec func() (Response, error),
) (Response, error)
```

`NewPartitionScheduler` defaults `workers <= 0` to `1`. `Dispatch` waits for a
lane slot and for execution, honouring `ctx` in both phases. Skill panics
inside a lane are recovered per task.

Most applications should use `PartitionBy` rather than constructing a
scheduler directly.

## Compilation

```go
type FuncNode struct {
	Name     string
	Fn       reflect.Value
	InTypes  []reflect.Type
	OutTypes []reflect.Type
}

type CompiledDAG struct {
	RouteKey string
	Nodes    []*FuncNode
	Waves    [][]*FuncNode
}

func CompileDAG(
	routeKey string,
	singletons map[reflect.Type]reflect.Value,
	rawFuncs ...any,
) *CompiledDAG

func (dag *CompiledDAG) Run(
	ctx context.Context,
	singletons map[reflect.Type]reflect.Value,
	req Request,
) (Response, error)

func (dag *CompiledDAG) SetInlineWaveLimit(n int)
```

`CompileDAG` is the low-level compiler used by route registration. It panics on
invalid graphs; `Run` recovers skill panics and maps deadline expiry to
`ErrRouteTimeout`.

`Waves` is the execution plan: skills within a wave are safe to run
concurrently; later waves consume earlier outputs.

`SetInlineWaveLimit` runs waves with at most `n` skills sequentially. It must be
called before the DAG is used concurrently; values below `1` are treated as `1`.
The route-level equivalent is `WithInlineWaveLimit`.

## Errors

```go
var ErrRouteTimeout = errors.New("yu: route execution timed out")
```

`ServeHTTP` maps `ErrRouteTimeout` to `504 Gateway Timeout`. Other errors map
to `500 Internal Server Error`; details are written to the server log only.

## Debug types

```go
type RouteTopologyDTO struct {
	RouteKey   string      `json:"route_key"`
	Mode       string      `json:"mode"`
	TotalWaves int         `json:"total_waves"`
	Waves      [][]NodeDTO `json:"waves"`
}

type NodeDTO struct {
	Name    string   `json:"name"`
	Inputs  []string `json:"inputs"`
	Outputs []string `json:"outputs"`
}
```

## Compile-time guarantees

`CompileDAG` panics when:

- no skill produces `yu.Response`
- a data type has more than one producer
- a wave has more than one `yu.Response` producer
- `yu.Response` is used as an input
- `error` is not the last return value
- a skill is variadic
- a dependency cannot be resolved or the graph is cyclic
- a route pattern is empty

`router.addRoute` also panics on conflicting wildcard segments.
