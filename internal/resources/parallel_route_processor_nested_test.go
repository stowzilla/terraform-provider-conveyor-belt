package resources

import (
	"testing"

	"terraform-provider-conveyor-belt/internal/utils"
)

// TestNestedRoutePathCaching tests that nested routes like /employees/me
// have their path cache keys properly set for Phase 2 lookups.
func TestNestedRoutePathCaching(t *testing.T) {
	// Test routes that mirror the real-world issue
	routes := []utils.Route{
		{Name: "employees_index", Verb: "GET", Path: "/employees", Gateway: "ops", Lambda: "ops"},
		{Name: "employees_create", Verb: "POST", Path: "/employees", Gateway: "ops", Lambda: "ops"},
		{Name: "employees_me", Verb: "GET", Path: "/employees/me", Gateway: "ops", Lambda: "ops"},
		{Name: "employees_show", Verb: "GET", Path: "/employees/{employee_id}", Gateway: "ops", Lambda: "ops"},
		{Name: "containers_index", Verb: "GET", Path: "/containers", Gateway: "ops", Lambda: "ops"},
		{Name: "containers_show", Verb: "GET", Path: "/containers/{container_id}", Gateway: "ops", Lambda: "ops"},
		{Name: "containers_contents", Verb: "GET", Path: "/containers/{container_id}/contents", Gateway: "ops", Lambda: "ops"},
	}

	// Build dependency graph
	graph := BuildPathDependencyGraph(routes, "ops")

	// Verify all expected paths are in the graph
	expectedPaths := []string{
		"/employees",
		"/employees/me",
		"/employees/{employee_id}",
		"/containers",
		"/containers/{container_id}",
		"/containers/{container_id}/contents",
	}

	for _, path := range expectedPaths {
		segment := graph.GetSegment(path)
		if segment == nil {
			t.Errorf("Expected path %s to be in graph, but it wasn't", path)
		}
	}

	// Verify creation order has parents before children
	order := graph.GetCreationOrder()
	positionMap := make(map[string]int)
	for i, path := range order {
		positionMap[path] = i
	}

	// /employees must come before /employees/me
	if positionMap["/employees"] >= positionMap["/employees/me"] {
		t.Errorf("/employees (pos %d) should come before /employees/me (pos %d)",
			positionMap["/employees"], positionMap["/employees/me"])
	}

	// /employees must come before /employees/{employee_id}
	if positionMap["/employees"] >= positionMap["/employees/{employee_id}"] {
		t.Errorf("/employees (pos %d) should come before /employees/{employee_id} (pos %d)",
			positionMap["/employees"], positionMap["/employees/{employee_id}"])
	}

	// /containers must come before /containers/{container_id}
	if positionMap["/containers"] >= positionMap["/containers/{container_id}"] {
		t.Errorf("/containers (pos %d) should come before /containers/{container_id} (pos %d)",
			positionMap["/containers"], positionMap["/containers/{container_id}"])
	}

	// /containers/{container_id} must come before /containers/{container_id}/contents
	if positionMap["/containers/{container_id}"] >= positionMap["/containers/{container_id}/contents"] {
		t.Errorf("/containers/{container_id} (pos %d) should come before /containers/{container_id}/contents (pos %d)",
			positionMap["/containers/{container_id}"], positionMap["/containers/{container_id}/contents"])
	}

	t.Logf("Creation order: %v", order)
}

// TestPathCacheKeyConsistency tests that path cache keys are set correctly
// regardless of whether the resource was just created or already existed.
func TestPathCacheKeyConsistency(t *testing.T) {
	cache := NewThreadSafeResourceCache()

	// Simulate Phase 1: Create resources
	// First, create /employees
	employeesKey := "api123:root123:employees"
	employeesPathKey := "api123:path:/employees"
	employeesId := "resource-employees"

	// Simulate GetOrCreate for /employees
	resultId, err := cache.GetOrCreate(nil, employeesKey, func() (string, error) {
		return employeesId, nil
	})
	if err != nil {
		t.Fatalf("Failed to create /employees: %v", err)
	}
	// Set path cache key (this is what the fix does)
	cache.Set(employeesPathKey, resultId)

	// Now create /employees/me
	meKey := "api123:resource-employees:me"
	mePathKey := "api123:path:/employees/me"
	meId := "resource-me"

	// First, look up parent from path cache
	parentId, exists := cache.Get(employeesPathKey)
	if !exists {
		t.Fatalf("Parent path cache key not found: %s", employeesPathKey)
	}
	if parentId != employeesId {
		t.Errorf("Parent ID mismatch: got %s, expected %s", parentId, employeesId)
	}

	// Create /employees/me
	resultId, err = cache.GetOrCreate(nil, meKey, func() (string, error) {
		return meId, nil
	})
	if err != nil {
		t.Fatalf("Failed to create /employees/me: %v", err)
	}
	// Set path cache key
	cache.Set(mePathKey, resultId)

	// Verify Phase 2 can look up /employees/me by path
	lookedUpId, exists := cache.Get(mePathKey)
	if !exists {
		t.Fatalf("Path cache key not found for /employees/me: %s", mePathKey)
	}
	if lookedUpId != meId {
		t.Errorf("Looked up ID mismatch: got %s, expected %s", lookedUpId, meId)
	}

	// Now simulate the scenario where /employees already exists in cache
	// (e.g., from a previous run or conflict recovery)
	// The path cache key should still be set

	// Clear path cache to simulate the bug scenario
	cache2 := NewThreadSafeResourceCache()

	// Pre-populate the resource cache (simulating existing resource)
	cache2.Set(employeesKey, employeesId)

	// Now try GetOrCreate - it should return cached value without calling createFn
	callCount := 0
	resultId, err = cache2.GetOrCreate(nil, employeesKey, func() (string, error) {
		callCount++
		return "should-not-be-called", nil
	})
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	if callCount > 0 {
		t.Errorf("createFn should not have been called for cached resource")
	}
	if resultId != employeesId {
		t.Errorf("Result ID mismatch: got %s, expected %s", resultId, employeesId)
	}

	// The fix: Always set path cache key after GetOrCreate, regardless of whether
	// the resource was just created or already existed
	cache2.Set(employeesPathKey, resultId)

	// Now verify the path cache key is set
	lookedUpId, exists = cache2.Get(employeesPathKey)
	if !exists {
		t.Fatalf("Path cache key should be set after GetOrCreate")
	}
	if lookedUpId != employeesId {
		t.Errorf("Path cache key value mismatch: got %s, expected %s", lookedUpId, employeesId)
	}
}
