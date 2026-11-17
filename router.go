package yu

import (
	"sort"
	"strings"
	"time"
)

type routeEntry struct {
	dag       *CompiledDAG
	scheduler *PartitionScheduler
	timeout   time.Duration
	bodyLimit int64
	internal  bool
}

type router struct {
	roots   map[string]*node
	entries map[string]*routeEntry
}

// newRouter returns a new router
func newRouter() *router {
	return &router{
		roots:   make(map[string]*node),
		entries: make(map[string]*routeEntry),
	}
}

// parsePattern parses pattern into parts
func parsePattern(pattern string) []string {
	vs := strings.Split(pattern, "/")
	parts := make([]string, 0)
	for _, item := range vs {
		if item != "" {
			parts = append(parts, item)
			if item[0] == '*' {
				break
			}
		}
	}
	return parts
}

// addRoute adds route to router
func (r *router) addRoute(method string, pattern string, entry *routeEntry) {
	if pattern == "" {
		panic("yu: route pattern must not be empty")
	}

	parts := parsePattern(pattern)
	key := method + "-" + pattern

	root, ok := r.roots[method]
	if !ok {
		root = &node{}
		r.roots[method] = root
	}

	leaf := root.insert(pattern, parts, 0)
	leaf.entry = entry
	r.entries[key] = entry
}

// getRoute returns the compiled entry and path parameters for the request.
func (r *router) getRoute(method string, path string) (*routeEntry, map[string]string) {
	root, ok := r.roots[method]
	if !ok {
		return nil, nil
	}

	searchParts := parsePattern(path)
	n := root.search(searchParts, 0)
	if n == nil || n.entry == nil {
		return nil, nil
	}

	params := make(map[string]string)
	for index, part := range parsePattern(n.pattern) {
		if index >= len(searchParts) {
			break
		}
		switch part[0] {
		case ':':
			params[part[1:]] = searchParts[index]
		case '*':
			if len(part) > 1 {
				params[part[1:]] = strings.Join(searchParts[index:], "/")
			}
			return n.entry, params
		}
	}
	return n.entry, params
}

// allowedMethods returns the sorted methods that have a route matching path.
func (r *router) allowedMethods(path string) []string {
	searchParts := parsePattern(path)
	methods := make([]string, 0, len(r.roots))
	for method, root := range r.roots {
		if n := root.search(searchParts, 0); n != nil && n.entry != nil {
			methods = append(methods, method)
		}
	}
	sort.Strings(methods)
	return methods
}

// markInternal flags a route so it is hidden from the topology inspector.
func (r *router) markInternal(method, pattern string) {
	if entry, ok := r.entries[method+"-"+pattern]; ok {
		entry.internal = true
	}
}
