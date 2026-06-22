// internal/resources/api_gateway_operations_test.go
package resources

import (
	"reflect"
	"testing"
)

// TestParsePathSegments tests the ParsePathSegments function
// Path is used as-is, gateway name is used separately for base path mappings
func TestParsePathSegments(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		gateway  string
		expected []string
	}{
		// Basic path parsing - no stripping, path used as-is
		{
			name:     "simple path",
			path:     "/waitlist",
			gateway:  "ops",
			expected: []string{"waitlist"},
		},
		{
			name:     "path with gateway prefix",
			path:     "/ops/containers",
			gateway:  "ops",
			expected: []string{"ops", "containers"},
		},
		{
			name:     "multiple segments",
			path:     "/customer/orders/items",
			gateway:  "customer",
			expected: []string{"customer", "orders", "items"},
		},

		// Duplicate segments preserved
		{
			name:     "duplicate gateway name in path",
			path:     "/ops/ops/something",
			gateway:  "ops",
			expected: []string{"ops", "ops", "something"},
		},

		// Single segment paths
		{
			name:     "single segment path",
			path:     "/ops",
			gateway:  "ops",
			expected: []string{"ops"},
		},
		{
			name:     "single segment with trailing slash",
			path:     "/ops/",
			gateway:  "ops",
			expected: []string{"ops"},
		},

		// Path parameters preserved
		{
			name:     "path with single parameter",
			path:     "/users/{id}",
			gateway:  "ops",
			expected: []string{"users", "{id}"},
		},
		{
			name:     "path with parameter in middle",
			path:     "/users/{id}/items",
			gateway:  "ops",
			expected: []string{"users", "{id}", "items"},
		},
		{
			name:     "path with multiple parameters",
			path:     "/customer/{customerId}/orders/{orderId}",
			gateway:  "customer",
			expected: []string{"customer", "{customerId}", "orders", "{orderId}"},
		},

		// Edge cases
		{
			name:     "empty path",
			path:     "",
			gateway:  "ops",
			expected: []string{},
		},
		{
			name:     "root path only",
			path:     "/",
			gateway:  "ops",
			expected: []string{},
		},
		{
			name:     "path without leading slash",
			path:     "users/list",
			gateway:  "ops",
			expected: []string{"users", "list"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParsePathSegments(tt.path, tt.gateway)

			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("ParsePathSegments(%q, %q) = %v, want %v",
					tt.path, tt.gateway, result, tt.expected)
			}
		})
	}
}

// TestStripGatewayPrefix_BackwardCompatibility verifies that StripGatewayPrefix
// still works (now delegates to ParsePathSegments)
func TestStripGatewayPrefix_BackwardCompatibility(t *testing.T) {
	testCases := []struct {
		path     string
		gateway  string
		expected []string
	}{
		{"/api/{version}/users", "api", []string{"api", "{version}", "users"}},
		{"/v1/{resource}/{id}", "v1", []string{"v1", "{resource}", "{id}"}},
		{"/ops/{containerId}", "ops", []string{"ops", "{containerId}"}},
	}

	for _, tc := range testCases {
		result := StripGatewayPrefix(tc.path, tc.gateway)
		if !reflect.DeepEqual(result, tc.expected) {
			t.Errorf("StripGatewayPrefix(%q, %q) = %v, want %v",
				tc.path, tc.gateway, result, tc.expected)
		}
	}
}
