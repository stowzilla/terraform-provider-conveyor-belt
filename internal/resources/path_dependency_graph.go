// internal/resources/path_dependency_graph.go
//
// Package resources provides path dependency analysis for API Gateway resources.
//
// # Path Dependency Graph
//
// The PathDependencyGraph analyzes route paths and builds a dependency graph
// for resource creation ordering. This ensures parent path segments are created
// before their children, which is required by AWS API Gateway.
//
// Example path hierarchy:
//
//	/users           (level 1)
//	/users/{id}      (level 2, depends on /users)
//	/users/{id}/orders (level 3, depends on /users/{id})
//
// The graph provides:
//   - Topological ordering for resource creation
//   - Level-by-level grouping for parallel processing
//   - Route-to-segment mapping for error reporting
//
// Requirements: 4.1, 4.2, 4.3, 4.4
package resources

import (
	"strings"

	"terraform-provider-conveyor-belt/internal/utils"
)

// PathSegmentNode represents a single path segment in the dependency graph.
// Each node corresponds to a unique path segment in the API Gateway resource hierarchy.
type PathSegmentNode struct {
	FullPath   string             // Full path from root (e.g., "/users/{id}/orders")
	PathPart   string             // Just this segment (e.g., "orders")
	ParentPath string             // Parent's full path (e.g., "/users/{id}"), empty for root children
	Children   []*PathSegmentNode // Child segments
	Routes     []utils.Route      // Routes that terminate at this segment
}

// PathDependencyGraph represents dependencies between path segments for an API Gateway.
// It enables topological ordering to ensure parent resources are created before children.
type PathDependencyGraph struct {
	segments map[string]*PathSegmentNode // full path -> node
	roots    []*PathSegmentNode          // segments with no parent (direct children of root "/")
}

// NewPathDependencyGraph creates an empty PathDependencyGraph.
func NewPathDependencyGraph() *PathDependencyGraph {
	return &PathDependencyGraph{
		segments: make(map[string]*PathSegmentNode),
		roots:    make([]*PathSegmentNode, 0),
	}
}


// BuildPathDependencyGraph constructs a dependency graph from a list of routes.
// It parses each route path into segments and builds a tree structure linking parents to children.
// The gateway parameter is used for filtering routes if needed (currently unused but available for future use).
func BuildPathDependencyGraph(routes []utils.Route, gateway string) *PathDependencyGraph {
	graph := NewPathDependencyGraph()

	for _, route := range routes {
		// Parse path into segments
		segments := parsePathSegments(route.Path)
		if len(segments) == 0 {
			continue
		}

		// Build path hierarchy for this route
		currentPath := ""
		var parentNode *PathSegmentNode

		for i, segment := range segments {
			// Build the full path for this segment
			if currentPath == "" {
				currentPath = "/" + segment
			} else {
				currentPath = currentPath + "/" + segment
			}

			// Check if we already have this segment
			node, exists := graph.segments[currentPath]
			if !exists {
				// Create new node
				parentPath := ""
				if i > 0 {
					// Parent is the previous path
					parentPath = strings.TrimSuffix(currentPath, "/"+segment)
				}

				node = &PathSegmentNode{
					FullPath:   currentPath,
					PathPart:   segment,
					ParentPath: parentPath,
					Children:   make([]*PathSegmentNode, 0),
					Routes:     make([]utils.Route, 0),
				}
				graph.segments[currentPath] = node

				// Link to parent or add to roots
				if parentNode != nil {
					parentNode.Children = append(parentNode.Children, node)
				} else {
					// This is a root-level segment (direct child of "/")
					graph.roots = append(graph.roots, node)
				}
			}

			parentNode = node
		}

		// Add the route to the terminal segment (the last segment in the path)
		if parentNode != nil {
			parentNode.Routes = append(parentNode.Routes, route)
		}
	}

	return graph
}

// parsePathSegments splits a path into segments.
// Example: "/users/{id}/orders" → ["users", "{id}", "orders"]
func parsePathSegments(path string) []string {
	// Remove leading slash and split
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return []string{}
	}
	return strings.Split(path, "/")
}


// GetCreationOrder returns path segments in topological order where parents come before children.
// Uses BFS from roots to ensure level-by-level ordering, which guarantees that all parent
// resources are created before their children.
func (g *PathDependencyGraph) GetCreationOrder() []string {
	if len(g.roots) == 0 {
		return []string{}
	}

	order := make([]string, 0, len(g.segments))

	// BFS queue starting with root nodes
	queue := make([]*PathSegmentNode, len(g.roots))
	copy(queue, g.roots)

	for len(queue) > 0 {
		// Dequeue the first node
		node := queue[0]
		queue = queue[1:]

		// Add this node's path to the order
		order = append(order, node.FullPath)

		// Enqueue all children
		queue = append(queue, node.Children...)
	}

	return order
}

// GetSegment returns the PathSegmentNode for a given full path, or nil if not found.
func (g *PathDependencyGraph) GetSegment(fullPath string) *PathSegmentNode {
	return g.segments[fullPath]
}

// GetRoots returns the root-level segments (direct children of "/").
func (g *PathDependencyGraph) GetRoots() []*PathSegmentNode {
	return g.roots
}

// Size returns the total number of unique path segments in the graph.
func (g *PathDependencyGraph) Size() int {
	return len(g.segments)
}
