// internal/resources/thread_safe_cache.go
//
// Package resources provides a thread-safe cache for API Gateway resource IDs.
//
// # Thread-Safe Resource Cache
//
// The ThreadSafeResourceCache provides concurrent-safe caching of API Gateway
// resource IDs with in-flight request coordination. This prevents duplicate
// resource creation when multiple goroutines attempt to create the same resource
// simultaneously.
//
// Key features:
//   - Read-write mutex for efficient concurrent reads
//   - In-flight coordination using completion channels
//   - Automatic deduplication of concurrent creation requests
//
// Requirements: 2.1, 2.2, 2.3
package resources

import (
	"context"
	"sync"
)

// ThreadSafeResourceCache provides thread-safe caching of API Gateway resource IDs
// with in-flight request coordination to prevent duplicate resource creation.
type ThreadSafeResourceCache struct {
	mu       sync.RWMutex
	cache    map[string]string        // cacheKey -> resourceId
	inFlight map[string]chan struct{} // cacheKey -> completion channel
}

// NewThreadSafeResourceCache creates a new ThreadSafeResourceCache instance.
func NewThreadSafeResourceCache() *ThreadSafeResourceCache {
	return &ThreadSafeResourceCache{
		cache:    make(map[string]string),
		inFlight: make(map[string]chan struct{}),
	}
}


// GetOrCreate returns a cached resource ID or coordinates creation.
// If another goroutine is creating the resource, this blocks until complete.
// The createFn is only called once per unique cacheKey, even with concurrent callers.
func (c *ThreadSafeResourceCache) GetOrCreate(ctx context.Context, cacheKey string, createFn func() (string, error)) (string, error) {
	// Fast path: check cache with read lock
	c.mu.RLock()
	if resourceId, exists := c.cache[cacheKey]; exists {
		c.mu.RUnlock()
		return resourceId, nil
	}
	// Check if creation is in-flight
	if waitCh, inFlight := c.inFlight[cacheKey]; inFlight {
		c.mu.RUnlock()
		// Wait for the in-flight creation to complete
		select {
		case <-waitCh:
			// Creation completed, get the result from cache
			c.mu.RLock()
			resourceId := c.cache[cacheKey]
			c.mu.RUnlock()
			return resourceId, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	c.mu.RUnlock()

	// Slow path: need to create the resource
	c.mu.Lock()
	// Double-check after acquiring write lock
	if resourceId, exists := c.cache[cacheKey]; exists {
		c.mu.Unlock()
		return resourceId, nil
	}
	// Check again if someone else started creation while we were waiting for the lock
	if waitCh, inFlight := c.inFlight[cacheKey]; inFlight {
		c.mu.Unlock()
		select {
		case <-waitCh:
			c.mu.RLock()
			resourceId := c.cache[cacheKey]
			c.mu.RUnlock()
			return resourceId, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	// Register in-flight creation
	completionCh := make(chan struct{})
	c.inFlight[cacheKey] = completionCh
	c.mu.Unlock()

	// Call the creation function (outside of lock)
	resourceId, err := createFn()

	// Update cache and signal completion
	c.mu.Lock()
	if err == nil {
		c.cache[cacheKey] = resourceId
	}
	delete(c.inFlight, cacheKey)
	close(completionCh)
	c.mu.Unlock()

	return resourceId, err
}


// Get returns a cached resource ID if it exists.
// Returns the resource ID and true if found, empty string and false otherwise.
func (c *ThreadSafeResourceCache) Get(cacheKey string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	resourceId, exists := c.cache[cacheKey]
	return resourceId, exists
}

// Set stores a resource ID in the cache.
// This is useful for caching externally retrieved resource IDs (e.g., after a 409 conflict).
func (c *ThreadSafeResourceCache) Set(cacheKey string, resourceId string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[cacheKey] = resourceId
}
