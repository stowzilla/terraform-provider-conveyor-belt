// internal/resources/dependency_analyzer_integration_test.go
package resources

import (
	"context"
	"path/filepath"
	"testing"
)

// TestDependencyAnalyzer_RealWorldIntegration tests the dependency analyzer
// with realistic Ruby Lambda files to ensure it works with real-world patterns.
func TestDependencyAnalyzer_RealWorldIntegration(t *testing.T) {
	ctx := context.Background()
	
	// Use the realistic test data
	sourceDir := filepath.Join("testdata", "test_samples", "lambda")
	analyzer := NewDependencyAnalyzer(sourceDir)
	
	testCases := []struct {
		name               string
		lambda             string
		expectedDepCount   int
		expectedDeps       []string // relative paths for easier testing
	}{
		{
			name:             "user_get lambda with dependencies",
			lambda:           "user_get",
			expectedDepCount: 4,
			expectedDeps: []string{
				"helpers/response_helper.rb",
				"lib/database.rb", 
				"models/user.rb",
				"user_get.rb",
			},
		},
		{
			name:             "user_create lambda with dependencies", 
			lambda:           "user_create",
			expectedDepCount: 4,
			expectedDeps: []string{
				"helpers/response_helper.rb",
				"lib/database.rb",
				"models/user.rb", 
				"user_create.rb",
			},
		},
		{
			name:             "health_check lambda with no dependencies",
			lambda:           "health_check",
			expectedDepCount: 1,
			expectedDeps: []string{
				"health_check.rb",
			},
		},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			deps, err := analyzer.AnalyzeDependencies(ctx, tc.lambda)
			if err != nil {
				t.Fatalf("Analysis failed: %v", err)
			}
			
			if len(deps) != tc.expectedDepCount {
				t.Errorf("Expected %d dependencies, got %d: %v", tc.expectedDepCount, len(deps), deps)
			}
			
			// Convert absolute paths to relative for comparison
			basePath := filepath.Join("testdata", "test_samples", "lambda")
			absBasePath, _ := filepath.Abs(basePath)
			
			foundDeps := make(map[string]bool)
			for _, dep := range deps {
				relDep, err := filepath.Rel(absBasePath, dep)
				if err != nil {
					t.Errorf("Failed to get relative path for %s: %v", dep, err)
					continue
				}
				foundDeps[relDep] = true
			}
			
			// Check that all expected dependencies are found
			for _, expectedDep := range tc.expectedDeps {
				if !foundDeps[expectedDep] {
					t.Errorf("Expected dependency %s not found in: %v", expectedDep, deps)
				}
			}
		})
	}
}

// TestDependencyAnalyzer_AffectedLambdasIntegration tests the GetAffectedLambdas
// method with realistic file changes.
func TestDependencyAnalyzer_AffectedLambdasIntegration(t *testing.T) {
	ctx := context.Background()
	
	sourceDir := filepath.Join("testdata", "test_samples", "lambda")
	analyzer := NewDependencyAnalyzer(sourceDir)
	
	testCases := []struct {
		name            string
		changedFiles    []string
		expectedAffected []string
	}{
		{
			name:            "User model changed affects user lambdas",
			changedFiles:    []string{filepath.Join("testdata", "test_samples", "lambda", "models", "user.rb")},
			expectedAffected: []string{"user_create", "user_get"},
		},
		{
			name:            "Database lib changed affects user lambdas",
			changedFiles:    []string{filepath.Join("testdata", "test_samples", "lambda", "lib", "database.rb")},
			expectedAffected: []string{"user_create", "user_get"},
		},
		{
			name:            "Response helper changed affects user lambdas",
			changedFiles:    []string{filepath.Join("testdata", "test_samples", "lambda", "helpers", "response_helper.rb")},
			expectedAffected: []string{"user_create", "user_get"},
		},
		{
			name:            "Health check lambda changed affects only itself",
			changedFiles:    []string{filepath.Join("testdata", "test_samples", "lambda", "health_check.rb")},
			expectedAffected: []string{"health_check"},
		},
		{
			name:            "Multiple shared files changed affects user lambdas",
			changedFiles:    []string{
				filepath.Join("testdata", "test_samples", "lambda", "models", "user.rb"),
				filepath.Join("testdata", "test_samples", "lambda", "helpers", "response_helper.rb"),
			},
			expectedAffected: []string{"user_create", "user_get"},
		},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			affected, err := analyzer.GetAffectedLambdas(ctx, tc.changedFiles)
			if err != nil {
				t.Fatalf("GetAffectedLambdas failed: %v", err)
			}
			
			if len(affected) != len(tc.expectedAffected) {
				t.Errorf("Expected %d affected lambdas, got %d: %v", len(tc.expectedAffected), len(affected), affected)
			}
			
			affectedMap := make(map[string]bool)
			for _, lambda := range affected {
				affectedMap[lambda] = true
			}
			
			for _, expected := range tc.expectedAffected {
				if !affectedMap[expected] {
					t.Errorf("Expected lambda %s to be affected, but it wasn't in: %v", expected, affected)
				}
			}
		})
	}
}

// TestDependencyAnalyzer_CacheIntegration tests cache behavior with real files
func TestDependencyAnalyzer_CacheIntegration(t *testing.T) {
	ctx := context.Background()
	
	sourceDir := filepath.Join("testdata", "test_samples", "lambda")
	analyzer := NewDependencyAnalyzer(sourceDir)
	
	// First analysis should populate cache
	deps1, err := analyzer.AnalyzeDependencies(ctx, "user_get")
	if err != nil {
		t.Fatalf("First analysis failed: %v", err)
	}
	
	// Check cache is populated
	cached := analyzer.GetCachedDependencies("user_get")
	if cached == nil {
		t.Error("Expected cache to be populated after first analysis")
	}
	
	if len(cached) != len(deps1) {
		t.Errorf("Cached dependencies count mismatch: expected %d, got %d", len(deps1), len(cached))
	}
	
	// Second analysis should use cache (results should be identical)
	deps2, err := analyzer.AnalyzeDependencies(ctx, "user_get")
	if err != nil {
		t.Fatalf("Second analysis failed: %v", err)
	}
	
	if len(deps1) != len(deps2) {
		t.Errorf("Cache not working: different result counts %d vs %d", len(deps1), len(deps2))
	}
	
	// Verify all dependencies match
	for i, dep1 := range deps1 {
		if i >= len(deps2) || dep1 != deps2[i] {
			t.Errorf("Cache not working: dependencies differ at index %d: %s vs %s", i, dep1, deps2[i])
			break
		}
	}
}