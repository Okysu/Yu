package yu

import "strings"

// RouterGroup represents a group of routes with a common prefix.
type RouterGroup struct {
	prefix string
	node   []any
	parent *RouterGroup
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
	mergedNodes := make([]any, 0, len(group.node)+len(nodes))
	mergedNodes = append(mergedNodes, group.node...)
	mergedNodes = append(mergedNodes, nodes...)

	newGroup := &RouterGroup{
		prefix: joinPaths(group.prefix, relativePath),
		node:   mergedNodes,
		parent: group,
		engine: group.engine,
	}

	group.engine.groups = append(group.engine.groups, newGroup)
	return newGroup
}

// Use adds middleware to the group.
func (group *RouterGroup) Use(node ...any) *RouterGroup {
	group.node = append(group.node, node...)
	return group
}

// addRoute adds a route to the group.
func (group *RouterGroup) addRoute(method, comp string, handlers ...any) {
	pattern := joinPaths(group.prefix, comp)

	allNodes := make([]any, 0, len(group.node)+len(handlers))
	allNodes = append(allNodes, group.node...)
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

// PUT adds a PUT route to the group.
func (group *RouterGroup) DELETE(pattern string, nodes ...any) {
	group.addRoute("DELETE", pattern, nodes...)
}

// DELETE adds a DELETE route to the group.
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
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
	for _, m := range methods {
		group.addRoute(m, pattern, nodes...)
	}
}
