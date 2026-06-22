// internal/resources/parallel_route_processor.go
//
// Package resources provides parallel route processing for API Gateway operations.
//
// # Parallel Route Processing
//
// This file implements parallel route processing for API Gateway resources, methods,
// and integrations. The processing happens in two phases:
//
// Phase 1 - Resource Creation:
//   - Builds a dependency graph of path segments
//   - Creates resources level-by-level (parents before children)
//   - Uses thread-safe caching to prevent duplicate resource creation
//   - Handles 409 Conflict errors by fetching existing resources
//
// Phase 2 - Method/Integration Creation:
//   - Creates methods and Lambda integrations in parallel
//   - No dependencies between routes at this phase
//   - Includes CORS OPTIONS method creation
//
// # Concurrency Control
//
// Concurrency is controlled via the RouteProcessingConcurrency configuration field.
// If not set, it falls back to DockerBuildConcurrency, then CPU count.
// A semaphore-based approach limits concurrent AWS API calls to prevent rate limiting.
//
// # Error Handling
//
// The processor continues processing on non-fatal errors and aggregates all errors
// into a RouteProcessingResult. Rate limit errors (429) trigger exponential backoff
// retries. Conflict errors (409) trigger resource lookup and caching.
//
// # Observability
//
// Progress logging is provided at configurable intervals, including:
//   - Start/completion of each phase
//   - Progress updates with estimated remaining time
//   - Error summaries
//
// Requirements: 1.1, 1.2, 1.3, 1.4, 2.1, 2.3, 2.4, 3.1, 3.2, 3.3, 3.4, 4.1, 4.2, 4.3, 4.4, 5.1, 5.2, 6.1, 6.2, 6.3, 6.4, 7.1, 7.2, 7.3, 7.4
package resources

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigatewayTypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// RetryConfig holds configuration for retry logic with exponential backoff.
// Requirements: 3.2
type RetryConfig struct {
	MaxRetries     int           // Maximum number of retry attempts (default: 10)
	InitialBackoff time.Duration // Initial backoff duration (default: 500ms)
	MaxBackoff     time.Duration // Maximum backoff duration (default: 15s)
	BackoffFactor  float64       // Multiplier for exponential backoff (default: 2.0)
}

// DefaultRetryConfig returns the default retry configuration.
// Uses conservative defaults to handle AWS API Gateway rate limits.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:     10,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     15 * time.Second,
		BackoffFactor:  2.0,
	}
}

// ParallelRouteProcessor handles concurrent route processing within an API Gateway.
// It processes routes in two phases:
// 1. Resource Phase: Create all unique path segment resources (respects parent-child dependencies)
// 2. Method Phase: Create all methods and integrations in parallel (no dependencies between routes)
//
// Requirements: 1.1, 1.2, 1.3
type ParallelRouteProcessor struct {
	client        *apigateway.Client
	config        *DispatcherConfig
	concurrency   int
	resourceCache *ThreadSafeResourceCache
	retryConfig   *RetryConfig

	// Metrics for observability
	activeWorkers int64 // Atomic counter for active concurrent operations

	// Cognito authorizer cache to prevent duplicate creation
	cognitoAuthorizerCache map[string]string // apiId -> authorizerId
	cognitoAuthorizerMu    sync.Mutex
}

// NewParallelRouteProcessor creates a new processor with the given concurrency limit.
// If concurrency is <= 0, it defaults to RouteProcessingConcurrency, then DockerBuildConcurrency,
// and finally a conservative default of 2 to avoid API Gateway rate limits.
//
// Requirements: 1.1, 1.2, 1.3, 3.4
func NewParallelRouteProcessor(client *apigateway.Client, config *DispatcherConfig, concurrency int) *ParallelRouteProcessor {
	if concurrency <= 0 {
		// Use RouteProcessingConcurrency if set
		if config != nil && config.RouteProcessingConcurrency > 0 {
			concurrency = config.RouteProcessingConcurrency
		} else if config != nil && config.DockerBuildConcurrency > 0 {
			// Fall back to DockerBuildConcurrency, capped at 10
			concurrency = config.DockerBuildConcurrency
			if concurrency > 10 {
				concurrency = 10
			}
		} else {
			// Default to 5 for API Gateway rate limits
			concurrency = 5
		}
	}

	return &ParallelRouteProcessor{
		client:                 client,
		config:                 config,
		concurrency:            concurrency,
		resourceCache:          NewThreadSafeResourceCache(),
		retryConfig:            DefaultRetryConfig(),
		cognitoAuthorizerCache: make(map[string]string),
	}
}

// SetRetryConfig allows customizing the retry configuration.
func (p *ParallelRouteProcessor) SetRetryConfig(config *RetryConfig) {
	if config != nil {
		p.retryConfig = config
	}
}

// GetConcurrency returns the configured concurrency limit.
func (p *ParallelRouteProcessor) GetConcurrency() int {
	return p.concurrency
}

// GetActiveWorkers returns the current number of active concurrent operations.
// This is useful for testing and observability.
func (p *ParallelRouteProcessor) GetActiveWorkers() int64 {
	return atomic.LoadInt64(&p.activeWorkers)
}


// ProcessRoutes processes all routes for an API Gateway in parallel.
// It returns a summary of results including any errors.
//
// The processing happens in two phases:
// 1. Resource Phase: Create all unique path segment resources with dependency ordering
// 2. Method Phase: Create all methods and integrations in parallel
//
// Requirements: 1.1, 1.2, 1.4, 4.1, 4.3, 4.4, 5.1, 5.2
func (p *ParallelRouteProcessor) ProcessRoutes(ctx context.Context, apiId string, routes []utils.Route, lambdaFunctions map[string]string) (*RouteProcessingResult, error) {
	startTime := time.Now()
	result := NewRouteProcessingResult()

	if len(routes) == 0 {
		result.TotalDuration = time.Since(startTime)
		return result, nil
	}

	// Log start of parallel processing
	// Requirements: 7.1, 7.2
	utils.Info(ctx, "[PARALLEL_ROUTE] Starting parallel route processing", map[string]interface{}{
		"api_id":      apiId,
		"route_count": len(routes),
		"concurrency": p.concurrency,
	})

	// Get root resource ID
	rootResourceId, err := p.getRootResourceId(ctx, apiId)
	if err != nil {
		return nil, fmt.Errorf("failed to get root resource: %w", err)
	}

	// Phase 1: Create resources with dependency ordering
	// Requirements: 4.1, 4.3, 4.4
	resourceErrors := p.processPhase1Resources(ctx, apiId, rootResourceId, routes)
	for _, routeErr := range resourceErrors {
		result.AddError(routeErr.Route, routeErr.Error, routeErr.Phase)
	}

	// Phase 2: Create methods and integrations in parallel
	// Requirements: 1.1, 1.2
	methodResults, methodErrors := p.processPhase2Methods(ctx, apiId, rootResourceId, routes, lambdaFunctions)
	for _, routeResult := range methodResults {
		if routeResult.Success {
			result.AddSuccess(routeResult.Route, routeResult.ResourceId, routeResult.Duration)
		}
	}
	for _, routeErr := range methodErrors {
		result.AddError(routeErr.Route, routeErr.Error, routeErr.Phase)
	}

	result.TotalDuration = time.Since(startTime)

	// Log completion summary
	// Requirements: 7.1, 7.4
	utils.Info(ctx, "[PARALLEL_ROUTE] Parallel route processing completed", map[string]interface{}{
		"api_id":        apiId,
		"total_routes":  len(routes),
		"success_count": result.SuccessCount,
		"failure_count": result.FailureCount,
		"duration_ms":   result.TotalDuration.Milliseconds(),
	})

	return result, nil
}

// getRootResourceId retrieves the root resource ID for an API Gateway.
func (p *ParallelRouteProcessor) getRootResourceId(ctx context.Context, apiId string) (string, error) {
	var rootResourceId string

	// Wrap in retry logic since this can be rate limited during parallel gateway creation
	err := p.retryWithBackoff(ctx, func() error {
		var position *string

		for {
			getResourcesInput := &apigateway.GetResourcesInput{
				RestApiId: aws.String(apiId),
				Position:  position,
			}

			getResourcesOutput, err := p.client.GetResources(ctx, getResourcesInput)
			if err != nil {
				return fmt.Errorf("failed to get resources: %w", err)
			}

			// Look for root resource (PathPart is nil OR Path is "/")
			for _, resource := range getResourcesOutput.Items {
				if resource.PathPart == nil || (resource.Path != nil && *resource.Path == "/") {
					rootResourceId = *resource.Id
					break
				}
			}

			if rootResourceId != "" {
				break
			}

			if getResourcesOutput.Position == nil || *getResourcesOutput.Position == "" {
				break
			}
			position = getResourcesOutput.Position
		}

		if rootResourceId == "" {
			return fmt.Errorf("could not find root resource")
		}

		return nil
	})

	if err != nil {
		return "", err
	}

	return rootResourceId, nil
}

// processPhase1Resources creates all unique path segment resources with dependency ordering.
// It builds a dependency graph and processes resources level-by-level to ensure
// parent resources are created before children.
//
// Requirements: 4.1, 4.2, 4.3, 4.4, 7.1, 7.2, 7.3, 7.4
func (p *ParallelRouteProcessor) processPhase1Resources(ctx context.Context, apiId, rootResourceId string, routes []utils.Route) []RouteError {
	var errors []RouteError

	// Build dependency graph
	graph := BuildPathDependencyGraph(routes, "")

	// Get creation order (topological sort - parents before children)
	creationOrder := graph.GetCreationOrder()

	if len(creationOrder) == 0 {
		return errors
	}

	phaseStartTime := time.Now()

	// Log start of Phase 1
	// Requirements: 7.1, 7.2
	utils.Info(ctx, "[PARALLEL_ROUTE] Phase 1: Creating resources with dependency ordering", map[string]interface{}{
		"unique_paths": len(creationOrder),
		"concurrency":  p.concurrency,
	})

	// Group paths by level (depth) for level-by-level processing
	levels := p.groupPathsByLevel(creationOrder)

	// Track progress across all levels
	// Requirements: 7.3
	totalPaths := len(creationOrder)
	processedPaths := 0

	// Process each level in order (parents before children)
	for levelNum, levelPaths := range levels {
		levelStartTime := time.Now()

		utils.Info(ctx, "[PARALLEL_ROUTE] Processing resource level", map[string]interface{}{
			"level":           levelNum,
			"path_count":      len(levelPaths),
			"total_processed": processedPaths,
			"total_paths":     totalPaths,
		})

		levelErrors := p.processResourceLevel(ctx, apiId, rootResourceId, levelPaths, graph)
		errors = append(errors, levelErrors...)

		processedPaths += len(levelPaths)
		levelDuration := time.Since(levelStartTime)

		// Log progress with timing
		// Requirements: 7.3
		elapsed := time.Since(phaseStartTime)
		percentComplete := float64(processedPaths) / float64(totalPaths) * 100
		var estimatedRemaining time.Duration
		if processedPaths > 0 {
			avgTimePerPath := elapsed / time.Duration(processedPaths)
			remainingPaths := totalPaths - processedPaths
			estimatedRemaining = avgTimePerPath * time.Duration(remainingPaths)
		}

		utils.Info(ctx, "[PARALLEL_ROUTE] Resource level completed", map[string]interface{}{
			"level":               levelNum,
			"level_duration_ms":   levelDuration.Milliseconds(),
			"processed":           processedPaths,
			"total":               totalPaths,
			"percent_complete":    fmt.Sprintf("%.1f%%", percentComplete),
			"elapsed_ms":          elapsed.Milliseconds(),
			"estimated_remaining": estimatedRemaining.String(),
			"errors_so_far":       len(errors),
		})
	}

	// Log Phase 1 completion summary
	// Requirements: 7.4
	phaseDuration := time.Since(phaseStartTime)
	utils.Info(ctx, "[PARALLEL_ROUTE] Phase 1 completed", map[string]interface{}{
		"total_paths":   totalPaths,
		"duration_ms":   phaseDuration.Milliseconds(),
		"error_count":   len(errors),
		"levels_count":  len(levels),
	})

	return errors
}

// groupPathsByLevel groups paths by their depth level for level-by-level processing.
func (p *ParallelRouteProcessor) groupPathsByLevel(paths []string) [][]string {
	if len(paths) == 0 {
		return nil
	}

	// Group by depth (number of segments)
	levelMap := make(map[int][]string)
	maxLevel := 0

	for _, path := range paths {
		segments := strings.Split(strings.Trim(path, "/"), "/")
		level := len(segments)
		levelMap[level] = append(levelMap[level], path)
		if level > maxLevel {
			maxLevel = level
		}
	}

	// Convert to ordered slice
	levels := make([][]string, 0, maxLevel)
	for i := 1; i <= maxLevel; i++ {
		if paths, exists := levelMap[i]; exists {
			levels = append(levels, paths)
		}
	}

	return levels
}

// processResourceLevel processes all paths at a single level concurrently.
// Requirements: 1.1, 1.2, 4.3
func (p *ParallelRouteProcessor) processResourceLevel(ctx context.Context, apiId, rootResourceId string, paths []string, graph *PathDependencyGraph) []RouteError {
	var errors []RouteError
	var errorsMu sync.Mutex

	// Semaphore for concurrency control
	sem := make(chan struct{}, p.concurrency)
	var wg sync.WaitGroup

	for _, path := range paths {
		wg.Add(1)
		go func(fullPath string) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			atomic.AddInt64(&p.activeWorkers, 1)
			defer func() {
				atomic.AddInt64(&p.activeWorkers, -1)
				<-sem
			}()

			// Get segment info from graph
			segment := graph.GetSegment(fullPath)
			if segment == nil {
				return
			}

			// Determine parent resource ID
			parentResourceId := rootResourceId
			if segment.ParentPath != "" {
				// Get parent from cache
				parentCacheKey := fmt.Sprintf("%s:path:%s", apiId, segment.ParentPath)
				cachedParentId, exists := p.resourceCache.Get(parentCacheKey)
				if !exists {
					// Parent not in cache - this is a bug, parent should have been created in previous level
					// Try to fetch it from AWS as a recovery mechanism
					fetchedParentId, fetchErr := p.fetchResourceByPath(ctx, apiId, segment.ParentPath)
					if fetchErr != nil {
						errorsMu.Lock()
						for _, route := range segment.Routes {
							errors = append(errors, RouteError{
								Route: route,
								Error: fmt.Errorf("parent resource %s not found in cache and could not be fetched from AWS: %w", segment.ParentPath, fetchErr),
								Phase: "resource",
							})
						}
						errorsMu.Unlock()
						return
					}
					// Cache the fetched parent for future use
					p.resourceCache.Set(parentCacheKey, fetchedParentId)
					parentResourceId = fetchedParentId
					utils.Warn(ctx, "[PARALLEL_ROUTE] Parent resource not in cache, fetched from AWS", map[string]interface{}{
						"parent_path":   segment.ParentPath,
						"parent_id":     fetchedParentId,
						"child_path":    fullPath,
					})
				} else {
					parentResourceId = cachedParentId
				}
			}

			// Create resource using cache to prevent duplicates
			cacheKey := fmt.Sprintf("%s:%s:%s", apiId, parentResourceId, segment.PathPart)
			pathCacheKey := fmt.Sprintf("%s:path:%s", apiId, fullPath)

			resourceId, err := p.resourceCache.GetOrCreate(ctx, cacheKey, func() (string, error) {
				resId, createErr := p.createResourceWithRetry(ctx, apiId, parentResourceId, segment.PathPart)
				return resId, createErr
			})

			if err != nil {
				errorsMu.Lock()
				// Add error for each route that terminates at this segment
				for _, route := range segment.Routes {
					errors = append(errors, RouteError{
						Route: route,
						Error: fmt.Errorf("failed to create resource for path %s: %w", fullPath, err),
						Phase: "resource",
					})
				}
				errorsMu.Unlock()
			} else {
				// Always cache by full path for Phase 2 lookups, regardless of whether
				// the resource was just created or already existed in cache
				p.resourceCache.Set(pathCacheKey, resourceId)
			}
		}(path)
	}

	wg.Wait()
	return errors
}

// isConflictError checks if an error is a 409 Conflict error.
// Requirements: 2.4
func (p *ParallelRouteProcessor) isConflictError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "ConflictException") ||
		strings.Contains(errStr, "409") ||
		strings.Contains(errStr, "already exists")
}

// createResourceWithRetry creates an API Gateway resource with retry logic for rate limits and conflicts.
// Requirements: 2.4, 3.2, 3.3
func (p *ParallelRouteProcessor) createResourceWithRetry(ctx context.Context, apiId, parentId, pathPart string) (string, error) {
	var lastErr error

	for attempt := 0; attempt <= p.retryConfig.MaxRetries; attempt++ {
		createResourceInput := &apigateway.CreateResourceInput{
			RestApiId: aws.String(apiId),
			ParentId:  aws.String(parentId),
			PathPart:  aws.String(pathPart),
		}

		createResourceOutput, err := p.client.CreateResource(ctx, createResourceInput)
		if err == nil {
			return *createResourceOutput.Id, nil
		}

		lastErr = err

		// Handle 409 Conflict - resource already exists
		// Requirements: 2.4
		if p.isConflictError(err) {
			existingId, fetchErr := p.fetchExistingResource(ctx, apiId, parentId, pathPart)
			if fetchErr == nil {
				utils.Info(ctx, "[PARALLEL_ROUTE] Recovered from 409 conflict, using existing resource", map[string]interface{}{
					"api_id":      apiId,
					"parent_id":   parentId,
					"path_part":   pathPart,
					"resource_id": existingId,
				})
				return existingId, nil
			}
			// If we can't fetch, log warning and continue with retry
			utils.Warn(ctx, "[PARALLEL_ROUTE] Conflict detected but couldn't fetch existing resource", map[string]interface{}{
				"api_id":    apiId,
				"parent_id": parentId,
				"path_part": pathPart,
				"error":     fetchErr.Error(),
			})
			lastErr = fmt.Errorf("conflict but couldn't fetch existing resource: %w", fetchErr)
			// Don't retry on conflict - if we can't fetch, something is wrong
			return "", lastErr
		}

		// Handle rate limiting
		// Requirements: 3.2, 3.3
		if p.isRateLimitError(err) {
			if attempt < p.retryConfig.MaxRetries {
				backoff := p.calculateBackoff(attempt)
				utils.Warn(ctx, "[PARALLEL_ROUTE] Rate limited, retrying", map[string]interface{}{
					"path_part": pathPart,
					"attempt":   attempt + 1,
					"backoff":   backoff.String(),
				})
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(backoff):
					continue
				}
			}
		} else {
			// For non-rate-limit, non-conflict errors, fail immediately
			return "", err
		}
	}

	return "", fmt.Errorf("failed after %d retries: %w", p.retryConfig.MaxRetries, lastErr)
}

// fetchExistingResource fetches an existing resource by parent ID and path part with retry logic.
// Requirements: 2.4, 3.2
func (p *ParallelRouteProcessor) fetchExistingResource(ctx context.Context, apiId, parentId, pathPart string) (string, error) {
	var lastErr error

	for attempt := 0; attempt <= p.retryConfig.MaxRetries; attempt++ {
		resourceId, err := p.fetchExistingResourceOnce(ctx, apiId, parentId, pathPart)
		if err == nil {
			return resourceId, nil
		}

		lastErr = err

		// Retry on rate limit errors
		if p.isRateLimitError(err) && attempt < p.retryConfig.MaxRetries {
			backoff := p.calculateBackoff(attempt)
			utils.Warn(ctx, "[PARALLEL_ROUTE] Rate limited fetching existing resource, retrying", map[string]interface{}{
				"path_part": pathPart,
				"attempt":   attempt + 1,
				"backoff":   backoff.String(),
			})
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		// Non-rate-limit error, return immediately
		return "", err
	}

	return "", fmt.Errorf("failed after %d retries: %w", p.retryConfig.MaxRetries, lastErr)
}

// fetchExistingResourceOnce fetches an existing resource by parent ID and path part (single attempt).
func (p *ParallelRouteProcessor) fetchExistingResourceOnce(ctx context.Context, apiId, parentId, pathPart string) (string, error) {
	var position *string

	for {
		getResourcesInput := &apigateway.GetResourcesInput{
			RestApiId: aws.String(apiId),
			Limit:     aws.Int32(500),
			Position:  position,
		}

		getResourcesOutput, err := p.client.GetResources(ctx, getResourcesInput)
		if err != nil {
			return "", err
		}

		for _, resource := range getResourcesOutput.Items {
			if resource.ParentId != nil && *resource.ParentId == parentId &&
				resource.PathPart != nil && *resource.PathPart == pathPart {
				return *resource.Id, nil
			}
		}

		if getResourcesOutput.Position == nil || *getResourcesOutput.Position == "" {
			break
		}
		position = getResourcesOutput.Position
	}

	return "", fmt.Errorf("resource not found: parent=%s, pathPart=%s", parentId, pathPart)
}


// processPhase2Methods creates methods and integrations for all routes in parallel.
// Requirements: 1.1, 1.2, 1.4, 5.1, 5.2, 7.1, 7.2, 7.3, 7.4
func (p *ParallelRouteProcessor) processPhase2Methods(ctx context.Context, apiId, rootResourceId string, routes []utils.Route, lambdaFunctions map[string]string) ([]RouteResult, []RouteError) {
	var results []RouteResult
	var errors []RouteError
	var resultsMu sync.Mutex
	var errorsMu sync.Mutex

	phaseStartTime := time.Now()

	// Log start of Phase 2
	// Requirements: 7.1, 7.2
	utils.Info(ctx, "[PARALLEL_ROUTE] Phase 2: Creating methods and integrations in parallel", map[string]interface{}{
		"route_count": len(routes),
		"concurrency": p.concurrency,
	})

	// Semaphore for concurrency control
	sem := make(chan struct{}, p.concurrency)
	var wg sync.WaitGroup

	// Progress tracking
	// Requirements: 7.3
	var processedCount int64
	totalRoutes := len(routes)
	progressInterval := totalRoutes / 10
	if progressInterval < 1 {
		progressInterval = 1
	}

	for _, route := range routes {
		wg.Add(1)
		go func(r utils.Route) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			atomic.AddInt64(&p.activeWorkers, 1)
			routeStart := time.Now()

			defer func() {
				atomic.AddInt64(&p.activeWorkers, -1)
				<-sem

				// Log progress with timing
				// Requirements: 7.3
				count := atomic.AddInt64(&processedCount, 1)
				if int(count)%progressInterval == 0 || int(count) == totalRoutes {
					elapsed := time.Since(phaseStartTime)
					percentComplete := float64(count) / float64(totalRoutes) * 100

					// Calculate estimated remaining time
					var estimatedRemaining time.Duration
					if count > 0 {
						avgTimePerRoute := elapsed / time.Duration(count)
						remainingRoutes := int64(totalRoutes) - count
						estimatedRemaining = avgTimePerRoute * time.Duration(remainingRoutes)
					}

					utils.Info(ctx, "[PARALLEL_ROUTE] Progress update", map[string]interface{}{
						"processed":           count,
						"total":               totalRoutes,
						"percent_complete":    fmt.Sprintf("%.1f%%", percentComplete),
						"elapsed_ms":          elapsed.Milliseconds(),
						"estimated_remaining": estimatedRemaining.String(),
					})
				}
			}()

			// Get resource ID for this route's path
			resourceId, err := p.getResourceIdForPath(ctx, apiId, rootResourceId, r.Path, r.Gateway)
			if err != nil {
				errorsMu.Lock()
				errors = append(errors, RouteError{
					Route: r,
					Error: fmt.Errorf("failed to get resource ID for path %s: %w", r.Path, err),
					Phase: "method",
				})
				errorsMu.Unlock()
				return
			}

			// Create method and integration
			err = p.createMethodAndIntegrationWithRetry(ctx, apiId, resourceId, r, lambdaFunctions)
			duration := time.Since(routeStart)

			if err != nil {
				errorsMu.Lock()
				errors = append(errors, RouteError{
					Route: r,
					Error: err,
					Phase: "method",
				})
				errorsMu.Unlock()
			} else {
				resultsMu.Lock()
				results = append(results, RouteResult{
					Route:      r,
					Success:    true,
					ResourceId: resourceId,
					Duration:   duration,
				})
				resultsMu.Unlock()

				// Create OPTIONS method for CORS - only if method creation succeeded
				if corsErr := p.createCorsOptionsMethodWithRetry(ctx, apiId, resourceId, routes, r.Path); corsErr != nil {
					utils.Warn(ctx, "[PARALLEL_ROUTE] Failed to create OPTIONS method for CORS", map[string]interface{}{
						"route":       r.Name,
						"path":        r.Path,
						"resource_id": resourceId,
						"error":       corsErr.Error(),
					})
					// Record as error so terraform knows something went wrong
					errorsMu.Lock()
					errors = append(errors, RouteError{
						Route: r,
						Error: fmt.Errorf("OPTIONS method creation failed: %w", corsErr),
						Phase: "cors",
					})
					errorsMu.Unlock()
				}
			}
		}(route)
	}

	wg.Wait()

	// Log Phase 2 completion summary
	// Requirements: 7.4
	phaseDuration := time.Since(phaseStartTime)
	utils.Info(ctx, "[PARALLEL_ROUTE] Phase 2 completed", map[string]interface{}{
		"total_routes":  totalRoutes,
		"success_count": len(results),
		"error_count":   len(errors),
		"duration_ms":   phaseDuration.Milliseconds(),
	})

	return results, errors
}

// getResourceIdForPath retrieves the resource ID for a given path.
func (p *ParallelRouteProcessor) getResourceIdForPath(ctx context.Context, apiId, rootResourceId, path, gateway string) (string, error) {
	pathSegments := ParsePathSegments(path, gateway)

	if len(pathSegments) == 0 {
		return rootResourceId, nil
	}

	// Build full path and check cache
	currentPath := ""
	for _, segment := range pathSegments {
		if currentPath == "" {
			currentPath = "/" + segment
		} else {
			currentPath = currentPath + "/" + segment
		}
	}

	pathCacheKey := fmt.Sprintf("%s:path:%s", apiId, currentPath)
	if cachedId, exists := p.resourceCache.Get(pathCacheKey); exists {
		return cachedId, nil
	}

	// If not in cache, fetch from AWS
	return p.fetchResourceByPath(ctx, apiId, currentPath)
}

// fetchResourceByPath fetches a resource ID by its full path with retry logic.
func (p *ParallelRouteProcessor) fetchResourceByPath(ctx context.Context, apiId, path string) (string, error) {
	var lastErr error

	for attempt := 0; attempt <= p.retryConfig.MaxRetries; attempt++ {
		resourceId, err := p.fetchResourceByPathOnce(ctx, apiId, path)
		if err == nil {
			return resourceId, nil
		}

		lastErr = err

		// Retry on rate limit errors
		if p.isRateLimitError(err) && attempt < p.retryConfig.MaxRetries {
			backoff := p.calculateBackoff(attempt)
			utils.Warn(ctx, "[PARALLEL_ROUTE] Rate limited fetching resource by path, retrying", map[string]interface{}{
				"path":    path,
				"attempt": attempt + 1,
				"backoff": backoff.String(),
			})
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		// Non-rate-limit error, return immediately
		return "", err
	}

	return "", fmt.Errorf("failed after %d retries: %w", p.retryConfig.MaxRetries, lastErr)
}

// fetchResourceByPathOnce fetches a resource ID by its full path (single attempt).
func (p *ParallelRouteProcessor) fetchResourceByPathOnce(ctx context.Context, apiId, path string) (string, error) {
	var position *string

	for {
		getResourcesInput := &apigateway.GetResourcesInput{
			RestApiId: aws.String(apiId),
			Limit:     aws.Int32(500),
			Position:  position,
		}

		getResourcesOutput, err := p.client.GetResources(ctx, getResourcesInput)
		if err != nil {
			return "", err
		}

		for _, resource := range getResourcesOutput.Items {
			if resource.Path != nil && *resource.Path == path {
				return *resource.Id, nil
			}
		}

		if getResourcesOutput.Position == nil || *getResourcesOutput.Position == "" {
			break
		}
		position = getResourcesOutput.Position
	}

	return "", fmt.Errorf("resource not found for path: %s", path)
}

// createMethodAndIntegrationWithRetry creates a method and Lambda integration with retry logic.
// Requirements: 3.2
func (p *ParallelRouteProcessor) createMethodAndIntegrationWithRetry(ctx context.Context, apiId, resourceId string, route utils.Route, lambdaFunctions map[string]string) error {
	return p.retryWithBackoff(ctx, func() error {
		return p.createMethodAndIntegration(ctx, apiId, resourceId, route, lambdaFunctions)
	})
}

// createMethodAndIntegration creates a method and Lambda integration for a route.
func (p *ParallelRouteProcessor) createMethodAndIntegration(ctx context.Context, apiId, resourceId string, route utils.Route, lambdaFunctions map[string]string) error {
	// Get Lambda ARN
	lambdaArn, exists := lambdaFunctions[route.Lambda]
	if !exists {
		lambdaFunctionName := fmt.Sprintf("%s-%s-%s", p.config.AppName, p.config.Environment, route.Lambda)
		lambdaArn = fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", p.config.AwsRegion, p.config.AwsAccountId, lambdaFunctionName)
	}

	// Determine authorization type
	authorization := "NONE"
	var cognitoAuthorizerId string
	var err error

	if route.Auth == "cognito" {
		authorization = "COGNITO_USER_POOLS"
		cognitoAuthorizerId, err = p.getOrCreateCognitoAuthorizer(ctx, apiId)
		if err != nil {
			return fmt.Errorf("failed to create Cognito authorizer: %w", err)
		}
	} else if route.Auth == "iam" {
		authorization = "AWS_IAM"
	}

	// Check if method already exists
	getMethodInput := &apigateway.GetMethodInput{
		RestApiId:  aws.String(apiId),
		ResourceId: aws.String(resourceId),
		HttpMethod: aws.String(route.Verb),
	}

	existingMethod, err := p.client.GetMethod(ctx, getMethodInput)
	if err == nil {
		// Method exists - check if it needs update
		needsUpdate := false
		if existingMethod.AuthorizationType == nil || *existingMethod.AuthorizationType != authorization {
			needsUpdate = true
		}
		if route.Auth == "cognito" && (existingMethod.AuthorizerId == nil || *existingMethod.AuthorizerId != cognitoAuthorizerId) {
			needsUpdate = true
		}

		if !needsUpdate {
			// Just ensure integration exists
			return p.ensureIntegrationExists(ctx, apiId, resourceId, route, lambdaArn)
		}

		// Update method
		return p.updateMethodAndIntegration(ctx, apiId, resourceId, route, authorization, cognitoAuthorizerId, lambdaArn)
	}

	// Create new method
	createMethodInput := &apigateway.PutMethodInput{
		RestApiId:         aws.String(apiId),
		ResourceId:        aws.String(resourceId),
		HttpMethod:        aws.String(route.Verb),
		AuthorizationType: aws.String(authorization),
	}

	if route.Auth == "cognito" && cognitoAuthorizerId != "" {
		createMethodInput.AuthorizerId = aws.String(cognitoAuthorizerId)
		createMethodInput.RequestParameters = map[string]bool{
			"method.request.header.Authorization": true,
		}
	}

	_, err = p.client.PutMethod(ctx, createMethodInput)
	if err != nil {
		// Handle 409 Conflict - method was created by another goroutine
		if p.isConflictError(err) {
			utils.Info(ctx, "[PARALLEL_ROUTE] Method already exists (concurrent creation), ensuring integration", map[string]interface{}{
				"resource_id": resourceId,
				"http_method": route.Verb,
				"path":        route.Path,
			})
			// Method exists, just ensure integration is set up
			return p.ensureIntegrationExists(ctx, apiId, resourceId, route, lambdaArn)
		}
		return fmt.Errorf("failed to create method: %w", err)
	}

	return p.ensureIntegrationExists(ctx, apiId, resourceId, route, lambdaArn)
}

// updateMethodAndIntegration updates an existing method using patch operations.
func (p *ParallelRouteProcessor) updateMethodAndIntegration(ctx context.Context, apiId, resourceId string, route utils.Route, authorization, cognitoAuthorizerId, lambdaArn string) error {
	patchOps := []apigatewayTypes.PatchOperation{
		{
			Op:    apigatewayTypes.OpReplace,
			Path:  aws.String("/authorizationType"),
			Value: aws.String(authorization),
		},
	}

	if route.Auth == "cognito" && cognitoAuthorizerId != "" {
		patchOps = append(patchOps, apigatewayTypes.PatchOperation{
			Op:    apigatewayTypes.OpReplace,
			Path:  aws.String("/authorizerId"),
			Value: aws.String(cognitoAuthorizerId),
		})
	}

	updateMethodInput := &apigateway.UpdateMethodInput{
		RestApiId:       aws.String(apiId),
		ResourceId:      aws.String(resourceId),
		HttpMethod:      aws.String(route.Verb),
		PatchOperations: patchOps,
	}

	_, err := p.client.UpdateMethod(ctx, updateMethodInput)
	if err != nil {
		return fmt.Errorf("failed to update method: %w", err)
	}

	return p.ensureIntegrationExists(ctx, apiId, resourceId, route, lambdaArn)
}

// ensureIntegrationExists creates or updates the Lambda integration.
func (p *ParallelRouteProcessor) ensureIntegrationExists(ctx context.Context, apiId, resourceId string, route utils.Route, lambdaArn string) error {
	integrationUri := fmt.Sprintf("arn:aws:apigateway:%s:lambda:path/2015-03-31/functions/%s/invocations", p.config.AwsRegion, lambdaArn)

	// Check if integration already exists
	getIntegrationInput := &apigateway.GetIntegrationInput{
		RestApiId:  aws.String(apiId),
		ResourceId: aws.String(resourceId),
		HttpMethod: aws.String(route.Verb),
	}

	existingIntegration, err := p.client.GetIntegration(ctx, getIntegrationInput)
	if err == nil && existingIntegration.Uri != nil && *existingIntegration.Uri == integrationUri {
		// Integration already exists with correct config
		p.createMethodResponseWithCors(ctx, apiId, resourceId, route.Verb)
		return nil
	}

	// Create or update integration
	createIntegrationInput := &apigateway.PutIntegrationInput{
		RestApiId:             aws.String(apiId),
		ResourceId:            aws.String(resourceId),
		HttpMethod:            aws.String(route.Verb),
		Type:                  apigatewayTypes.IntegrationTypeAwsProxy,
		IntegrationHttpMethod: aws.String("POST"),
		Uri:                   aws.String(integrationUri),
	}

	_, err = p.client.PutIntegration(ctx, createIntegrationInput)
	if err != nil {
		return fmt.Errorf("failed to create/update integration: %w", err)
	}

	p.createMethodResponseWithCors(ctx, apiId, resourceId, route.Verb)
	return nil
}

// getOrCreateCognitoAuthorizer gets or creates a Cognito User Pool authorizer.
// Uses a mutex-protected cache to prevent duplicate creation when called concurrently.
func (p *ParallelRouteProcessor) getOrCreateCognitoAuthorizer(ctx context.Context, apiId string) (string, error) {
	// Check cache first (with lock)
	p.cognitoAuthorizerMu.Lock()
	if cachedId, exists := p.cognitoAuthorizerCache[apiId]; exists {
		p.cognitoAuthorizerMu.Unlock()
		return cachedId, nil
	}
	// Keep lock held while we create/fetch to prevent races
	defer p.cognitoAuthorizerMu.Unlock()

	cognitoArns := p.config.CognitoUserPoolArns
	if len(cognitoArns) == 0 {
		return "", fmt.Errorf("no Cognito User Pool ARNs configured")
	}

	// Check if authorizer already exists in AWS
	listAuthorizersInput := &apigateway.GetAuthorizersInput{
		RestApiId: aws.String(apiId),
	}

	getAuthorizersOutput, err := p.client.GetAuthorizers(ctx, listAuthorizersInput)
	if err == nil {
		for _, authorizer := range getAuthorizersOutput.Items {
			if authorizer.Type == apigatewayTypes.AuthorizerTypeCognitoUserPools &&
				authorizer.Name != nil && *authorizer.Name == "CognitoUserPoolAuthorizer" {
				// Cache and return existing authorizer
				p.cognitoAuthorizerCache[apiId] = *authorizer.Id
				return *authorizer.Id, nil
			}
		}
	}

	// Create new authorizer
	createAuthorizerInput := &apigateway.CreateAuthorizerInput{
		RestApiId:      aws.String(apiId),
		Name:           aws.String("CognitoUserPoolAuthorizer"),
		Type:           apigatewayTypes.AuthorizerTypeCognitoUserPools,
		ProviderARNs:   cognitoArns,
		IdentitySource: aws.String("method.request.header.Authorization"),
	}

	createAuthorizerOutput, err := p.client.CreateAuthorizer(ctx, createAuthorizerInput)
	if err != nil {
		// Check if it's a duplicate error (race condition with another process)
		if strings.Contains(err.Error(), "already exists") {
			// Try to fetch it again
			getAuthorizersOutput, fetchErr := p.client.GetAuthorizers(ctx, listAuthorizersInput)
			if fetchErr == nil {
				for _, authorizer := range getAuthorizersOutput.Items {
					if authorizer.Type == apigatewayTypes.AuthorizerTypeCognitoUserPools &&
						authorizer.Name != nil && *authorizer.Name == "CognitoUserPoolAuthorizer" {
						p.cognitoAuthorizerCache[apiId] = *authorizer.Id
						return *authorizer.Id, nil
					}
				}
			}
		}
		return "", err
	}

	// Cache the new authorizer ID
	p.cognitoAuthorizerCache[apiId] = *createAuthorizerOutput.Id
	return *createAuthorizerOutput.Id, nil
}

// createMethodResponseWithCors adds CORS headers to method responses.
func (p *ParallelRouteProcessor) createMethodResponseWithCors(ctx context.Context, apiId, resourceId, httpMethod string) {
	frontendUrl := GetCORSOriginForConfig(p.config)

	putMethodResponseInput := &apigateway.PutMethodResponseInput{
		RestApiId:  aws.String(apiId),
		ResourceId: aws.String(resourceId),
		HttpMethod: aws.String(httpMethod),
		StatusCode: aws.String("200"),
		ResponseParameters: map[string]bool{
			"method.response.header.Access-Control-Allow-Origin": true,
		},
	}

	p.client.PutMethodResponse(ctx, putMethodResponseInput)

	putIntegrationResponseInput := &apigateway.PutIntegrationResponseInput{
		RestApiId:  aws.String(apiId),
		ResourceId: aws.String(resourceId),
		HttpMethod: aws.String(httpMethod),
		StatusCode: aws.String("200"),
		ResponseParameters: map[string]string{
			"method.response.header.Access-Control-Allow-Origin": fmt.Sprintf("'%s'", frontendUrl),
		},
	}

	p.client.PutIntegrationResponse(ctx, putIntegrationResponseInput)
}

// createCorsOptionsMethodWithRetry creates an OPTIONS method for CORS with retry logic.
// Returns an error if the OPTIONS method could not be created after all retries.
// Uses the resource cache to coordinate concurrent creation attempts for the same resource.
func (p *ParallelRouteProcessor) createCorsOptionsMethodWithRetry(ctx context.Context, apiId, resourceId string, routes []utils.Route, path string) error {
	// Use cache key to coordinate OPTIONS creation across concurrent routes sharing the same resource
	// This prevents race conditions where multiple routes try to create OPTIONS for the same path
	cacheKey := fmt.Sprintf("%s:options:%s", apiId, resourceId)

	_, err := p.resourceCache.GetOrCreate(ctx, cacheKey, func() (string, error) {
		// The actual OPTIONS creation with retry logic
		createErr := p.retryWithBackoff(ctx, func() error {
			return p.createCorsOptionsMethod(ctx, apiId, resourceId, routes, path)
		})
		if createErr != nil {
			return "", createErr
		}
		// Return resourceId as the "created" value (we just need a non-empty string for success)
		return resourceId, nil
	})

	return err
}

func (p *ParallelRouteProcessor) createCorsOptionsMethod(ctx context.Context, apiId, resourceId string, routes []utils.Route, path string) error {
	// Check if OPTIONS method already exists
	getMethodInput := &apigateway.GetMethodInput{
		RestApiId:  aws.String(apiId),
		ResourceId: aws.String(resourceId),
		HttpMethod: aws.String("OPTIONS"),
	}

	existingMethod, err := p.client.GetMethod(ctx, getMethodInput)
	if err == nil {
		// OPTIONS method exists - but check if it has an integration
		// A method without integration will cause deployment failures
		if existingMethod.MethodIntegration == nil {
			utils.Info(ctx, "OPTIONS method exists but has no integration - recreating", map[string]interface{}{
				"resource_id": resourceId,
			})
			// Delete the incomplete method and recreate it
			_, delErr := p.client.DeleteMethod(ctx, &apigateway.DeleteMethodInput{
				RestApiId:  aws.String(apiId),
				ResourceId: aws.String(resourceId),
				HttpMethod: aws.String("OPTIONS"),
			})
			if delErr != nil {
				return fmt.Errorf("failed to delete incomplete OPTIONS method: %w", delErr)
			}
			// Fall through to create the method properly
		} else {
			// OPTIONS method exists with integration - but we need to verify
			// the method response and integration response also exist
			// These are required for CORS to work properly
			needsRepair := false

			// Check if method response exists
			_, methodRespErr := p.client.GetMethodResponse(ctx, &apigateway.GetMethodResponseInput{
				RestApiId:  aws.String(apiId),
				ResourceId: aws.String(resourceId),
				HttpMethod: aws.String("OPTIONS"),
				StatusCode: aws.String("200"),
			})
			if methodRespErr != nil {
				utils.Info(ctx, "OPTIONS method missing method response - will repair", map[string]interface{}{
					"resource_id": resourceId,
				})
				needsRepair = true
			}

			// Check if integration response exists
			integResp, integRespErr := p.client.GetIntegrationResponse(ctx, &apigateway.GetIntegrationResponseInput{
				RestApiId:  aws.String(apiId),
				ResourceId: aws.String(resourceId),
				HttpMethod: aws.String("OPTIONS"),
				StatusCode: aws.String("200"),
			})
			if integRespErr != nil {
				utils.Info(ctx, "OPTIONS method missing integration response - will repair", map[string]interface{}{
					"resource_id": resourceId,
				})
				needsRepair = true
			}

			if needsRepair {
				// Create the missing responses
				return p.ensureCorsResponses(ctx, apiId, resourceId, routes, path)
			}

			// Responses exist — verify CORS origin header is current
			expectedOrigin := "'" + GetCORSOriginForConfig(p.config) + "'"
			if integResp != nil && integResp.ResponseParameters != nil {
				currentOrigin := integResp.ResponseParameters["method.response.header.Access-Control-Allow-Origin"]
				if currentOrigin != expectedOrigin {
					utils.Info(ctx, "OPTIONS CORS origin outdated - updating", map[string]interface{}{
						"resource_id": resourceId,
						"current":     currentOrigin,
						"expected":    expectedOrigin,
					})
					return p.ensureCorsResponses(ctx, apiId, resourceId, routes, path)
				}
			}

			// All good - OPTIONS method is complete
			return nil
		}
	}

	// Create OPTIONS method
	createMethodInput := &apigateway.PutMethodInput{
		RestApiId:         aws.String(apiId),
		ResourceId:        aws.String(resourceId),
		HttpMethod:        aws.String("OPTIONS"),
		AuthorizationType: aws.String("NONE"),
	}

	_, err = p.client.PutMethod(ctx, createMethodInput)
	if err != nil {
		return err
	}

	// Create mock integration
	putIntegrationInput := &apigateway.PutIntegrationInput{
		RestApiId:  aws.String(apiId),
		ResourceId: aws.String(resourceId),
		HttpMethod: aws.String("OPTIONS"),
		Type:       apigatewayTypes.IntegrationTypeMock,
		RequestTemplates: map[string]string{
			"application/json": `{"statusCode": 200}`,
		},
	}

	_, err = p.client.PutIntegration(ctx, putIntegrationInput)
	if err != nil {
		return err
	}

	// Create method response and integration response
	return p.ensureCorsResponses(ctx, apiId, resourceId, routes, path)
}

// ensureCorsResponses creates or updates the method response and integration response for CORS.
func (p *ParallelRouteProcessor) ensureCorsResponses(ctx context.Context, apiId, resourceId string, routes []utils.Route, path string) error {
	// Create method response
	putMethodResponseInput := &apigateway.PutMethodResponseInput{
		RestApiId:  aws.String(apiId),
		ResourceId: aws.String(resourceId),
		HttpMethod: aws.String("OPTIONS"),
		StatusCode: aws.String("200"),
		ResponseParameters: map[string]bool{
			"method.response.header.Access-Control-Allow-Origin":  true,
			"method.response.header.Access-Control-Allow-Methods": true,
			"method.response.header.Access-Control-Allow-Headers": true,
			"method.response.header.Access-Control-Max-Age":       true,
		},
	}

	_, err := p.client.PutMethodResponse(ctx, putMethodResponseInput)
	if err != nil {
		return err
	}

	// Create integration response with CORS headers
	frontendUrl := GetCORSOriginForConfig(p.config)

	putIntegrationResponseInput := &apigateway.PutIntegrationResponseInput{
		RestApiId:  aws.String(apiId),
		ResourceId: aws.String(resourceId),
		HttpMethod: aws.String("OPTIONS"),
		StatusCode: aws.String("200"),
		ResponseParameters: map[string]string{
			"method.response.header.Access-Control-Allow-Origin":  fmt.Sprintf("'%s'", frontendUrl),
			"method.response.header.Access-Control-Allow-Methods": func() string {
				methods := utils.GetMethodsForPath(routes, path)
				methodSet := make(map[string]bool)
				for _, m := range methods {
					methodSet[m] = true
				}
				methodSet["OPTIONS"] = true
				sorted := make([]string, 0, len(methodSet))
				for m := range methodSet {
					sorted = append(sorted, m)
				}
				sort.Strings(sorted)
				return "'" + strings.Join(sorted, ",") + "'"
			}(),
			"method.response.header.Access-Control-Allow-Headers": "'Content-Type,X-Amz-Date,Authorization,X-Api-Key,X-Amz-Security-Token'",
			"method.response.header.Access-Control-Max-Age":       "'86400'",
		},
		ResponseTemplates: map[string]string{
			"application/json": `{}`,
		},
	}

	_, err = p.client.PutIntegrationResponse(ctx, putIntegrationResponseInput)
	return err
}

// retryWithBackoff executes a function with exponential backoff retry logic.
// Requirements: 3.2, 3.3
func (p *ParallelRouteProcessor) retryWithBackoff(ctx context.Context, fn func() error) error {
	var lastErr error

	for attempt := 0; attempt <= p.retryConfig.MaxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if it's a rate limit error
		if !p.isRateLimitError(err) {
			return err
		}

		// Log warning on rate limiting
		// Requirements: 3.3
		if attempt < p.retryConfig.MaxRetries {
			backoff := p.calculateBackoff(attempt)
			utils.Warn(ctx, "[PARALLEL_ROUTE] Rate limited during method/integration creation, retrying", map[string]interface{}{
				"attempt":    attempt + 1,
				"max_retries": p.retryConfig.MaxRetries,
				"backoff":    backoff.String(),
				"error":      err.Error(),
			})
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}
	}

	return fmt.Errorf("failed after %d retries: %w", p.retryConfig.MaxRetries, lastErr)
}

// isRateLimitError checks if an error is a rate limit error.
// Requirements: 3.2, 3.3
func (p *ParallelRouteProcessor) isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "TooManyRequestsException") ||
		strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "Rate exceeded") ||
		strings.Contains(errStr, "Throttling")
}

// calculateBackoff calculates the backoff duration for a given attempt with jitter.
// Jitter prevents thundering herd when multiple gateways retry simultaneously.
// Requirements: 3.2
func (p *ParallelRouteProcessor) calculateBackoff(attempt int) time.Duration {
	backoff := float64(p.retryConfig.InitialBackoff)
	for i := 0; i < attempt; i++ {
		backoff *= p.retryConfig.BackoffFactor
	}

	if time.Duration(backoff) > p.retryConfig.MaxBackoff {
		backoff = float64(p.retryConfig.MaxBackoff)
	}

	// Add jitter: randomize between 50%-100% of the calculated backoff
	// This spreads out retries across competing gateways
	jittered := backoff * (0.5 + rand.Float64()*0.5)

	return time.Duration(jittered)
}
