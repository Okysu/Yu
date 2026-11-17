package yu

import (
	"fmt"
	"strings"
)

type node struct {
	pattern  string      // full path
	part     string      // the part of the path, like, :lang, :id
	children []*node     // the children of the node
	isWild   bool        // is :lang or *filepath
	entry    *routeEntry // compiled route attached to the leaf node
}

// matchChild returns the child whose segment matches part exactly.
// Only exact matches are allowed so that registering a static segment
// (e.g. /user/new) never reuses an existing wildcard sibling (e.g. /user/:id).
func (n *node) matchChild(part string) *node {
	for _, child := range n.children {
		if child.part == part {
			return child
		}
	}
	return nil
}

// matchChildren returns all children matching part, with static segments
// ordered before wildcards so that a static route always wins over a
// wildcard route at the same position.
func (n *node) matchChildren(part string) []*node {
	matches := make([]*node, 0, len(n.children))
	for _, child := range n.children {
		if !child.isWild && child.part == part {
			matches = append(matches, child)
		}
	}
	for _, child := range n.children {
		if child.isWild {
			matches = append(matches, child)
		}
	}
	return matches
}

// insert inserts a node and returns the leaf carrying the full pattern.
func (n *node) insert(pattern string, parts []string, height int) *node {
	if len(parts) == height {
		n.pattern = pattern
		return n
	}

	part := parts[height]
	wild := part[0] == ':' || part[0] == '*'

	child := n.matchChild(part)
	if child == nil {
		if wild {
			// Two different wildcards at the same position would be
			// ambiguous (/user/:id vs /user/:name); reject them early.
			for _, sibling := range n.children {
				if sibling.isWild {
					panic(fmt.Sprintf("yu: conflicting wildcard segments %q and %q", sibling.part, part))
				}
			}
		}
		child = &node{part: part, isWild: wild}
		n.children = append(n.children, child)
	}
	return child.insert(pattern, parts, height+1)
}

// search searches a node.
func (n *node) search(parts []string, height int) *node {
	if len(parts) == height || strings.HasPrefix(n.part, "*") {
		if n.pattern == "" {
			return nil
		}
		return n
	}

	for _, child := range n.matchChildren(parts[height]) {
		if result := child.search(parts, height+1); result != nil {
			return result
		}
	}
	return nil
}
