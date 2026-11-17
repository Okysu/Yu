package yu

import (
	"net/http"
	"strings"
)

var httpMethods = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodDelete,
	http.MethodPatch,
	http.MethodHead,
	http.MethodOptions,
}

// RouterGroup represents a group of routes with a common prefix.
//
// Nodes registered on a group are not middleware: they are merged into the
// same type-driven DAG as the route's own nodes, and the engine orders them
// purely by type dependencies.
type RouterGroup struct {
	prefix string
	nodes  []any
	engine *Engine
}

// joinPaths joins two paths into a single path.
func joinPaths(absolutePath, relativePath string) string {
	if relativePath == "" {
		return absolutePath
	}
	finalPath := strings.TrimRight(absolutePath, "/") + "/" + strings.TrimLeft(relativePath, "/")
	if !strings.HasPrefix(finalPath, "/") {
		return "/" + finalPath
	}
	return finalPath
}

// Group creates a new RouterGroup with the given prefix and nodes.
func (group *RouterGroup) Group(relativePath string, nodes ...any) *RouterGroup {
	mergedNodes := make([]any, 0, len(group.nodes)+len(nodes))
	mergedNodes = append(mergedNodes, group.nodes...)
	mergedNodes = append(mergedNodes, nodes...)

	return &RouterGroup{
		prefix: joinPaths(group.prefix, relativePath),
		nodes:  mergedNodes,
		engine: group.engine,
	}
}

// Use adds nodes to the group. They join the same causal DAG as route nodes,
// so execution order is decided by types rather than by registration order.
// Routes registered before Use are not affected.
func (group *RouterGroup) Use(node ...any) *RouterGroup {
	group.nodes = append(group.nodes, node...)
	return group
}

// addRoute adds a route to the group.
func (group *RouterGroup) addRoute(method, comp string, handlers ...any) {
	pattern := joinPaths(group.prefix, comp)

	allNodes := make([]any, 0, len(group.nodes)+len(handlers))
	allNodes = append(allNodes, group.nodes...)
	allNodes = append(allNodes, handlers...)

	group.engine.addRoute(method, pattern, allNodes...)
}

// GET adds a GET route to the group.
func (group *RouterGroup) GET(pattern string, nodes ...any) {
	group.addRoute("GET", pattern, nodes...)
}

// POST adds a POST route to the group.
func (group *RouterGroup) POST(pattern string, nodes ...any) {
	group.addRoute("POST", pattern, nodes...)
}

// PUT adds a PUT route to the group.
func (group *RouterGroup) PUT(pattern string, nodes ...any) {
	group.addRoute("PUT", pattern, nodes...)
}

// DELETE adds a DELETE route to the group.
func (group *RouterGroup) DELETE(pattern string, nodes ...any) {
	group.addRoute("DELETE", pattern, nodes...)
}

// PATCH adds a PATCH route to the group.
func (group *RouterGroup) PATCH(pattern string, nodes ...any) {
	group.addRoute("PATCH", pattern, nodes...)
}

// HEAD adds a HEAD route to the group.
func (group *RouterGroup) HEAD(pattern string, nodes ...any) {
	group.addRoute("HEAD", pattern, nodes...)
}

// OPTIONS adds an OPTIONS route to the group.
func (group *RouterGroup) OPTIONS(pattern string, nodes ...any) {
	group.addRoute("OPTIONS", pattern, nodes...)
}

// Any adds a route for all HTTP methods to the group.
func (group *RouterGroup) Any(pattern string, nodes ...any) {
	for _, m := range httpMethods {
		group.addRoute(m, pattern, nodes...)
	}
}

// Raw registers a standard net/http handler for every HTTP method.
//
// Raw routes bypass the DAG entirely: group nodes, route options, timeouts,
// body limits and partition scheduling do not apply. The handler receives the
// original *http.Request, with path parameters exposed through
// req.PathValue(name) (Go 1.22+). This is the escape hatch for third-party
// handlers and for streaming responses such as SSE.
func (group *RouterGroup) Raw(pattern string, handler http.HandlerFunc) {
	group.addRawRoute(pattern, handler)
}

// Handle is Raw for an http.Handler value.
func (group *RouterGroup) Handle(pattern string, handler http.Handler) {
	group.addRawRoute(pattern, handler)
}

func (group *RouterGroup) addRawRoute(comp string, handler http.Handler) {
	pattern := joinPaths(group.prefix, comp)
	for _, method := range httpMethods {
		group.engine.addRawRoute(method, pattern, handler)
	}
}
