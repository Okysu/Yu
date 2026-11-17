package yu

import (
	"strings"
	"time"
)

type routeEntry struct {
	dag       *CompiledDAG
	scheduler *PartitionScheduler
	timeout   time.Duration
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
	parts := parsePattern(pattern)
	key := method + "-" + pattern

	if _, ok := r.roots[method]; !ok {
		r.roots[method] = &node{}
	}
	r.roots[method].insert(pattern, parts, 0)
	r.entries[key] = entry
}

// getRoute returns route for method and path
func (r *router) getRoute(method string, path string) (*node, map[string]string) {
	searchParts := parsePattern(path)
	params := make(map[string]string)
	root, ok := r.roots[method]
	if !ok {
		return nil, nil
	}
	n := root.search(searchParts, 0)
	if n != nil {
		parts := parsePattern(n.pattern)
		for index, part := range parts {
			if part[0] == ':' {
				params[part[1:]] = searchParts[index]
			}
			if part[0] == '*' && len(part) > 1 {
				params[part[1:]] = strings.Join(searchParts[index:], "/")
				break
			}
		}
		return n, params
	}
	return nil, nil
}
