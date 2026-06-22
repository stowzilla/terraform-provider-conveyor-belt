// internal/resources/route_processing_result.go
//
// Package resources provides result types for parallel route processing.
//
// # Route Processing Results
//
// The RouteProcessingResult type aggregates results from parallel route processing,
// including success counts, failure counts, timing information, and detailed
// error information for each failed route.
//
// Key features:
//   - Aggregated success/failure counts
//   - Per-route timing information
//   - Detailed error information with phase identification
//   - Error summary generation for logging
//
// Requirements: 1.4, 5.1, 5.2
package resources

import (
	"fmt"
	"strings"
	"time"

	"terraform-provider-conveyor-belt/internal/utils"
)

// RouteProcessingResult contains the results of parallel route processing.
// It aggregates success and failure information from processing multiple routes.
type RouteProcessingResult struct {
	SuccessCount  int
	FailureCount  int
	TotalDuration time.Duration
	RouteResults  []RouteResult
	Errors        []RouteError
}

// RouteResult represents the result of processing a single route.
type RouteResult struct {
	Route      utils.Route
	Success    bool
	ResourceId string
	Duration   time.Duration
}

// RouteError represents an error that occurred processing a route.
type RouteError struct {
	Route utils.Route
	Error error
	Phase string // "resource" or "method"
}

// HasErrors returns true if any errors occurred during route processing.
func (r *RouteProcessingResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// ErrorSummary returns a human-readable summary of all errors.
// Returns an empty string if there are no errors.
func (r *RouteProcessingResult) ErrorSummary() string {
	if !r.HasErrors() {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d route(s) failed to process:\n", len(r.Errors)))

	for i, routeErr := range r.Errors {
		sb.WriteString(fmt.Sprintf("  %d. [%s] %s %s: %v\n",
			i+1,
			routeErr.Phase,
			routeErr.Route.Verb,
			routeErr.Route.Path,
			routeErr.Error,
		))
	}

	return sb.String()
}

// NewRouteProcessingResult creates a new RouteProcessingResult with initialized slices.
func NewRouteProcessingResult() *RouteProcessingResult {
	return &RouteProcessingResult{
		RouteResults: make([]RouteResult, 0),
		Errors:       make([]RouteError, 0),
	}
}

// AddSuccess records a successful route processing result.
func (r *RouteProcessingResult) AddSuccess(route utils.Route, resourceId string, duration time.Duration) {
	r.SuccessCount++
	r.RouteResults = append(r.RouteResults, RouteResult{
		Route:      route,
		Success:    true,
		ResourceId: resourceId,
		Duration:   duration,
	})
}

// AddError records a failed route processing result.
func (r *RouteProcessingResult) AddError(route utils.Route, err error, phase string) {
	r.FailureCount++
	r.Errors = append(r.Errors, RouteError{
		Route: route,
		Error: err,
		Phase: phase,
	})
	r.RouteResults = append(r.RouteResults, RouteResult{
		Route:   route,
		Success: false,
	})
}
