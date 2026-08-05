// internal/resources/thread_safe_cache_test.go
package resources

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"testing/quick"
	"time"
)

// Feature: parallel-route-processing, Property 3: Thread-Safe Cache Operations
// *For any* concurrent access pattern to the Resource_Cache, all operations SHALL complete
// without data races and maintain cache consistency.
// **Validates: Requirements 2.2**

// TestThreadSafeCacheOperations_Property tests Property 3: Thread-Safe Cache Operations
// For any concurrent GetOrCreate calls for the same key, exactly one creation should occur.
func TestThreadSafeCacheOperations_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random number of concurrent callers (2-50)
		numCallers := 2 + r.Intn(49)

		// Generate random cache key
		cacheKey := randomCacheKey(r)

		// Track how many times the creation function is called
		var creationCount int32

		// Expected resource ID
		expectedResourceId := fmt.Sprintf("resource-%s-%d", cacheKey, seed)

		// Create cache
		cache := NewThreadSafeResourceCache()

		// Create function that tracks calls
		createFn := func() (string, error) {
			atomic.AddInt32(&creationCount, 1)
			// Simulate some work
			time.Sleep(time.Duration(1+r.Intn(5)) * time.Millisecond)
			return expectedResourceId, nil
		}

		// Launch concurrent callers
		var wg sync.WaitGroup
		results := make(chan string, numCallers)
		errors := make(chan error, numCallers)

		ctx := context.Background()

		for i := 0; i < numCallers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resourceId, err := cache.GetOrCreate(ctx, cacheKey, createFn)
				if err != nil {
					errors <- err
				} else {
					results <- resourceId
				}
			}()
		}

		wg.Wait()
		close(results)
		close(errors)

		// Property 1: No errors should occur
		for err := range errors {
			t.Logf("Unexpected error: %v", err)
			return false
		}

		// Property 2: All callers should get the same resource ID
		var resultCount int
		for resourceId := range results {
			resultCount++
			if resourceId != expectedResourceId {
				t.Logf("Wrong resource ID: expected %s, got %s", expectedResourceId, resourceId)
				return false
			}
		}

		// Property 3: All callers should have received a result
		if resultCount != numCallers {
			t.Logf("Not all callers got results: expected %d, got %d", numCallers, resultCount)
			return false
		}

		// Property 4: Creation function should be called exactly once
		actualCreationCount := atomic.LoadInt32(&creationCount)
		if actualCreationCount != 1 {
			t.Logf("Creation function called %d times, expected 1", actualCreationCount)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Thread-safe cache operations property failed: %v", err)
	}
}


// TestThreadSafeCacheOperations_MultipleKeys tests concurrent access with multiple different keys
func TestThreadSafeCacheOperations_MultipleKeys(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random number of unique keys (3-20)
		numKeys := 3 + r.Intn(18)

		// Generate random number of callers per key (2-10)
		callersPerKey := 2 + r.Intn(9)

		// Generate cache keys
		cacheKeys := make([]string, numKeys)
		for i := 0; i < numKeys; i++ {
			cacheKeys[i] = fmt.Sprintf("key-%d-%s", i, randomCacheKey(r))
		}

		// Track creation counts per key
		creationCounts := make(map[string]*int32)
		for _, key := range cacheKeys {
			var count int32
			creationCounts[key] = &count
		}

		// Create cache
		cache := NewThreadSafeResourceCache()

		// Launch concurrent callers for each key
		var wg sync.WaitGroup
		type result struct {
			key        string
			resourceId string
			err        error
		}
		results := make(chan result, numKeys*callersPerKey)

		ctx := context.Background()

		for _, key := range cacheKeys {
			for i := 0; i < callersPerKey; i++ {
				wg.Add(1)
				go func(cacheKey string) {
					defer wg.Done()

					createFn := func() (string, error) {
						atomic.AddInt32(creationCounts[cacheKey], 1)
						time.Sleep(time.Duration(1+r.Intn(3)) * time.Millisecond)
						return fmt.Sprintf("resource-for-%s", cacheKey), nil
					}

					resourceId, err := cache.GetOrCreate(ctx, cacheKey, createFn)
					results <- result{key: cacheKey, resourceId: resourceId, err: err}
				}(key)
			}
		}

		wg.Wait()
		close(results)

		// Collect results by key
		resultsByKey := make(map[string][]string)
		for res := range results {
			if res.err != nil {
				t.Logf("Unexpected error for key %s: %v", res.key, res.err)
				return false
			}
			resultsByKey[res.key] = append(resultsByKey[res.key], res.resourceId)
		}

		// Property 1: Each key should have exactly callersPerKey results
		for key, keyResults := range resultsByKey {
			if len(keyResults) != callersPerKey {
				t.Logf("Key %s has %d results, expected %d", key, len(keyResults), callersPerKey)
				return false
			}
		}

		// Property 2: All results for a key should be identical
		for key, keyResults := range resultsByKey {
			expectedId := fmt.Sprintf("resource-for-%s", key)
			for _, resourceId := range keyResults {
				if resourceId != expectedId {
					t.Logf("Key %s has inconsistent results: expected %s, got %s", key, expectedId, resourceId)
					return false
				}
			}
		}

		// Property 3: Each key's creation function should be called exactly once
		for key, countPtr := range creationCounts {
			count := atomic.LoadInt32(countPtr)
			if count != 1 {
				t.Logf("Key %s creation function called %d times, expected 1", key, count)
				return false
			}
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Thread-safe cache multiple keys property failed: %v", err)
	}
}

// TestThreadSafeCache_GetAndSet tests the Get and Set methods
func TestThreadSafeCache_GetAndSet(t *testing.T) {
	cache := NewThreadSafeResourceCache()

	// Test Get on empty cache
	resourceId, exists := cache.Get("nonexistent")
	if exists {
		t.Errorf("Expected Get to return false for nonexistent key")
	}
	if resourceId != "" {
		t.Errorf("Expected empty string for nonexistent key, got %s", resourceId)
	}

	// Test Set and Get
	cache.Set("key1", "resource1")
	resourceId, exists = cache.Get("key1")
	if !exists {
		t.Errorf("Expected Get to return true for existing key")
	}
	if resourceId != "resource1" {
		t.Errorf("Expected resource1, got %s", resourceId)
	}

	// Test overwrite with Set
	cache.Set("key1", "resource1-updated")
	resourceId, exists = cache.Get("key1")
	if !exists {
		t.Errorf("Expected Get to return true after update")
	}
	if resourceId != "resource1-updated" {
		t.Errorf("Expected resource1-updated, got %s", resourceId)
	}
}

// TestThreadSafeCache_GetOrCreateWithCachedValue tests GetOrCreate when value is already cached
func TestThreadSafeCache_GetOrCreateWithCachedValue(t *testing.T) {
	cache := NewThreadSafeResourceCache()

	// Pre-populate cache
	cache.Set("existing-key", "existing-resource")

	// Track if createFn is called
	var createCalled bool
	createFn := func() (string, error) {
		createCalled = true
		return "new-resource", nil
	}

	ctx := context.Background()
	resourceId, err := cache.GetOrCreate(ctx, "existing-key", createFn)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resourceId != "existing-resource" {
		t.Errorf("Expected existing-resource, got %s", resourceId)
	}
	if createCalled {
		t.Errorf("createFn should not be called when value is cached")
	}
}

// TestThreadSafeCache_ContextCancellation tests that GetOrCreate respects context cancellation
func TestThreadSafeCache_ContextCancellation(t *testing.T) {
	cache := NewThreadSafeResourceCache()

	// Start a long-running creation
	ctx1 := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		cache.GetOrCreate(ctx1, "slow-key", func() (string, error) {
			time.Sleep(100 * time.Millisecond)
			return "slow-resource", nil
		})
	}()

	// Give the first goroutine time to start
	time.Sleep(10 * time.Millisecond)

	// Try to get the same key with a cancelled context
	ctx2, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := cache.GetOrCreate(ctx2, "slow-key", func() (string, error) {
		return "should-not-be-called", nil
	})

	if err != context.Canceled {
		t.Errorf("Expected context.Canceled error, got %v", err)
	}

	wg.Wait()
}

// TestThreadSafeCache_ConcurrentSetAndGet tests concurrent Set and Get operations
func TestThreadSafeCache_ConcurrentSetAndGet(t *testing.T) {
	cache := NewThreadSafeResourceCache()

	var wg sync.WaitGroup
	numOperations := 100

	// Concurrent writers
	for i := 0; i < numOperations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", idx%10)
			value := fmt.Sprintf("value-%d", idx)
			cache.Set(key, value)
		}(i)
	}

	// Concurrent readers
	for i := 0; i < numOperations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", idx%10)
			cache.Get(key)
		}(i)
	}

	wg.Wait()

	// Verify cache is in a consistent state (no panics, all keys accessible)
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key-%d", i)
		_, _ = cache.Get(key)
	}
}

// randomCacheKey generates a random cache key for testing
func randomCacheKey(r *rand.Rand) string {
	length := 5 + r.Intn(11)
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = chars[r.Intn(len(chars))]
	}
	return string(result)
}
