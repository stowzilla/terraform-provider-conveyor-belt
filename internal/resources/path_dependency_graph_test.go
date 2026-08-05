// internal/resources/path_dependency_graph_test.go
package resources

import (
	"math/rand"
	"strings"
	"testing"
	"testing/quick"

	"terraform-provider-conveyor-belt/internal/utils"
)

// Feature: parallel-route-processing, Property 7: Dependency Graph Correctness
// *For any* set of routes, the dependency graph SHALL correctly identify all parent-child
// relationships between path segments and produce a valid topological ordering.
// **Validates: Requirements 4.2**

// TestDependencyGraphCorrectness_Property tests Property 7: Dependency Graph Correctness
// For any set of routes, verify parent paths appear before child paths in creation order.
func TestDependencyGraphCorrectness_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random routes
		routes := generateRandomRoutesForGraph(r, 1+r.Intn(20))

		// Build dependency graph
		graph := BuildPathDependencyGraph(routes, "test-gateway")

		// Get creation order
		order := graph.GetCreationOrder()

		// Build a position map for quick lookup
		positionMap := make(map[string]int)
		for i, path := range order {
			positionMap[path] = i
		}

		// Property: For every segment in the graph, its parent must appear before it in the order
		for _, path := range order {
			node := graph.GetSegment(path)
			if node == nil {
				t.Logf("Segment %s not found in graph", path)
				return false
			}

			// If this node has a parent, verify parent comes before child
			if node.ParentPath != "" {
				parentPos, parentExists := positionMap[node.ParentPath]
				childPos := positionMap[path]

				if !parentExists {
					t.Logf("Parent path %s not in order for child %s", node.ParentPath, path)
					return false
				}

				if parentPos >= childPos {
					t.Logf("Parent %s (pos %d) does not come before child %s (pos %d)",
						node.ParentPath, parentPos, path, childPos)
					return false
				}
			}
		}

		// Property: All segments in the graph must appear in the order
		if len(order) != graph.Size() {
			t.Logf("Order length %d does not match graph size %d", len(order), graph.Size())
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Dependency graph correctness property failed: %v", err)
	}
}


// Feature: parallel-route-processing, Property 6: Parent-Before-Child Ordering
// *For any* route path with multiple segments, all parent path segment resources
// SHALL be created before their child segment resources.
// **Validates: Requirements 4.1, 4.4**

// TestParentBeforeChildOrdering_Property tests Property 6: Parent-Before-Child Ordering
// For any route path, verify all parent segments are ordered before children.
func TestParentBeforeChildOrdering_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random routes with varying path depths
		routes := generateRandomRoutesWithDepth(r, 1+r.Intn(15), 1+r.Intn(5))

		// Build dependency graph
		graph := BuildPathDependencyGraph(routes, "test-gateway")

		// Get creation order
		order := graph.GetCreationOrder()

		// Build a position map
		positionMap := make(map[string]int)
		for i, path := range order {
			positionMap[path] = i
		}

		// For each route, verify all its path segments appear in correct order
		for _, route := range routes {
			segments := parsePathSegments(route.Path)
			if len(segments) == 0 {
				continue
			}

			// Build all intermediate paths for this route
			var paths []string
			currentPath := ""
			for _, segment := range segments {
				if currentPath == "" {
					currentPath = "/" + segment
				} else {
					currentPath = currentPath + "/" + segment
				}
				paths = append(paths, currentPath)
			}

			// Verify each path appears before its children
			for i := 0; i < len(paths)-1; i++ {
				parentPath := paths[i]
				childPath := paths[i+1]

				parentPos, parentExists := positionMap[parentPath]
				childPos, childExists := positionMap[childPath]

				if !parentExists {
					t.Logf("Parent path %s not in order", parentPath)
					return false
				}
				if !childExists {
					t.Logf("Child path %s not in order", childPath)
					return false
				}

				if parentPos >= childPos {
					t.Logf("Parent %s (pos %d) does not come before child %s (pos %d)",
						parentPath, parentPos, childPath, childPos)
					return false
				}
			}
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Parent-before-child ordering property failed: %v", err)
	}
}

// Helper functions for generating random test data

// generateRandomRoutesForGraph generates a random set of routes for dependency graph testing.
// Uses realistic path segments to create meaningful hierarchies.
func generateRandomRoutesForGraph(r *rand.Rand, count int) []utils.Route {
	routes := make([]utils.Route, count)
	pathSegments := []string{"users", "orders", "products", "items", "reviews", "comments", "tags", "categories"}
	pathParams := []string{"{id}", "{userId}", "{orderId}", "{productId}"}
	verbs := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

	for i := 0; i < count; i++ {
		// Generate random path with 1-4 segments
		numSegments := 1 + r.Intn(4)
		var pathParts []string
		for j := 0; j < numSegments; j++ {
			if r.Float32() < 0.3 && j > 0 {
				// 30% chance of path parameter (not for first segment)
				pathParts = append(pathParts, pathParams[r.Intn(len(pathParams))])
			} else {
				pathParts = append(pathParts, pathSegments[r.Intn(len(pathSegments))])
			}
		}

		routes[i] = utils.Route{
			Name:    randomString(r, 5, 10),
			Verb:    verbs[r.Intn(len(verbs))],
			Path:    "/" + strings.Join(pathParts, "/"),
			Gateway: "api",
			Lambda:  randomString(r, 5, 10),
		}
	}

	return routes
}

// generateRandomRoutesWithDepth generates routes with controlled path depth for testing.
func generateRandomRoutesWithDepth(r *rand.Rand, count int, maxDepth int) []utils.Route {
	routes := make([]utils.Route, count)
	pathSegments := []string{"users", "orders", "products", "items", "reviews", "comments"}
	pathParams := []string{"{id}", "{userId}", "{orderId}"}
	verbs := []string{"GET", "POST", "PUT", "DELETE"}

	for i := 0; i < count; i++ {
		// Generate path with depth between 1 and maxDepth
		depth := 1 + r.Intn(maxDepth)
		var pathParts []string
		for j := 0; j < depth; j++ {
			if r.Float32() < 0.25 && j > 0 {
				pathParts = append(pathParts, pathParams[r.Intn(len(pathParams))])
			} else {
				pathParts = append(pathParts, pathSegments[r.Intn(len(pathSegments))])
			}
		}

		routes[i] = utils.Route{
			Name:    randomString(r, 5, 10),
			Verb:    verbs[r.Intn(len(verbs))],
			Path:    "/" + strings.Join(pathParts, "/"),
			Gateway: "api",
			Lambda:  randomString(r, 5, 10),
		}
	}

	return routes
}

// Unit tests for specific scenarios

func TestBuildPathDependencyGraph_EmptyRoutes(t *testing.T) {
	graph := BuildPathDependencyGraph([]utils.Route{}, "api")

	if graph.Size() != 0 {
		t.Errorf("Expected empty graph, got size %d", graph.Size())
	}

	order := graph.GetCreationOrder()
	if len(order) != 0 {
		t.Errorf("Expected empty order, got %v", order)
	}
}

func TestBuildPathDependencyGraph_SingleRoute(t *testing.T) {
	routes := []utils.Route{
		{Name: "get_users", Verb: "GET", Path: "/users", Gateway: "api", Lambda: "users"},
	}

	graph := BuildPathDependencyGraph(routes, "api")

	if graph.Size() != 1 {
		t.Errorf("Expected 1 segment, got %d", graph.Size())
	}

	order := graph.GetCreationOrder()
	if len(order) != 1 || order[0] != "/users" {
		t.Errorf("Expected [/users], got %v", order)
	}
}

func TestBuildPathDependencyGraph_NestedPaths(t *testing.T) {
	routes := []utils.Route{
		{Name: "get_user_orders", Verb: "GET", Path: "/users/{id}/orders", Gateway: "api", Lambda: "user_orders"},
	}

	graph := BuildPathDependencyGraph(routes, "api")

	// Should have 3 segments: /users, /users/{id}, /users/{id}/orders
	if graph.Size() != 3 {
		t.Errorf("Expected 3 segments, got %d", graph.Size())
	}

	order := graph.GetCreationOrder()
	expected := []string{"/users", "/users/{id}", "/users/{id}/orders"}

	if len(order) != len(expected) {
		t.Errorf("Expected %d segments in order, got %d", len(expected), len(order))
	}

	for i, path := range expected {
		if order[i] != path {
			t.Errorf("Expected order[%d] = %s, got %s", i, path, order[i])
		}
	}
}

func TestBuildPathDependencyGraph_SharedPaths(t *testing.T) {
	routes := []utils.Route{
		{Name: "get_users", Verb: "GET", Path: "/users", Gateway: "api", Lambda: "users"},
		{Name: "get_user", Verb: "GET", Path: "/users/{id}", Gateway: "api", Lambda: "user"},
		{Name: "get_user_orders", Verb: "GET", Path: "/users/{id}/orders", Gateway: "api", Lambda: "user_orders"},
		{Name: "get_orders", Verb: "GET", Path: "/orders", Gateway: "api", Lambda: "orders"},
	}

	graph := BuildPathDependencyGraph(routes, "api")

	// Should have 4 unique segments: /users, /users/{id}, /users/{id}/orders, /orders
	if graph.Size() != 4 {
		t.Errorf("Expected 4 segments, got %d", graph.Size())
	}

	// Verify roots
	roots := graph.GetRoots()
	if len(roots) != 2 {
		t.Errorf("Expected 2 roots, got %d", len(roots))
	}

	// Verify order has parents before children
	order := graph.GetCreationOrder()
	positionMap := make(map[string]int)
	for i, path := range order {
		positionMap[path] = i
	}

	// /users must come before /users/{id}
	if positionMap["/users"] >= positionMap["/users/{id}"] {
		t.Errorf("/users should come before /users/{id}")
	}

	// /users/{id} must come before /users/{id}/orders
	if positionMap["/users/{id}"] >= positionMap["/users/{id}/orders"] {
		t.Errorf("/users/{id} should come before /users/{id}/orders")
	}
}

func TestBuildPathDependencyGraph_RoutesAtSegment(t *testing.T) {
	routes := []utils.Route{
		{Name: "get_users", Verb: "GET", Path: "/users", Gateway: "api", Lambda: "users_get"},
		{Name: "post_users", Verb: "POST", Path: "/users", Gateway: "api", Lambda: "users_post"},
		{Name: "get_user", Verb: "GET", Path: "/users/{id}", Gateway: "api", Lambda: "user_get"},
	}

	graph := BuildPathDependencyGraph(routes, "api")

	// Check that /users segment has 2 routes
	usersNode := graph.GetSegment("/users")
	if usersNode == nil {
		t.Fatal("Expected /users segment to exist")
	}
	if len(usersNode.Routes) != 2 {
		t.Errorf("Expected 2 routes at /users, got %d", len(usersNode.Routes))
	}

	// Check that /users/{id} segment has 1 route
	userIdNode := graph.GetSegment("/users/{id}")
	if userIdNode == nil {
		t.Fatal("Expected /users/{id} segment to exist")
	}
	if len(userIdNode.Routes) != 1 {
		t.Errorf("Expected 1 route at /users/{id}, got %d", len(userIdNode.Routes))
	}
}
