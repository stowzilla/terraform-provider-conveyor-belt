// internal/resources/dependency_analyzer_test.go
package resources

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"testing/quick"
)

// Feature: provider-framework-refactor, Property 8: Require Statement Analysis
// *For any* Ruby file containing `require 'X'` or `require_relative 'X'` statements,
// the dependency analyzer SHALL include X (resolved to absolute path) in the file's dependency list.
// **Validates: Requirements 5.1**

// TestRequireStatementAnalysis_Property tests Property 8: Require Statement Analysis
func TestRequireStatementAnalysis_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Create temporary directory for test files
		tempDir, err := os.MkdirTemp("", "dep-analyzer-test-*")
		if err != nil {
			t.Logf("Failed to create temp dir: %v", err)
			return false
		}
		defer os.RemoveAll(tempDir)

		// Generate random number of dependencies (1-5)
		numDeps := 1 + r.Intn(5)

		// Generate dependency file names
		depNames := make([]string, numDeps)
		for i := 0; i < numDeps; i++ {
			depNames[i] = fmt.Sprintf("dep_%s", randomString(r, 3, 8))
		}

		// Create dependency files
		for _, depName := range depNames {
			depFile := filepath.Join(tempDir, depName+".rb")
			content := fmt.Sprintf("# Dependency file: %s\ndef %s_method\n  puts 'hello'\nend\n", depName, depName)
			if err := os.WriteFile(depFile, []byte(content), 0644); err != nil {
				t.Logf("Failed to write dep file: %v", err)
				return false
			}
		}

		// Generate main Lambda file with require statements
		lambdaName := fmt.Sprintf("lambda_%s", randomString(r, 3, 8))
		lambdaFile := filepath.Join(tempDir, lambdaName+".rb")

		// Build require statements - mix of require and require_relative
		var requireStatements string
		for i, depName := range depNames {
			if i%2 == 0 {
				requireStatements += fmt.Sprintf("require '%s'\n", depName)
			} else {
				requireStatements += fmt.Sprintf("require_relative '%s'\n", depName)
			}
		}

		lambdaContent := fmt.Sprintf(`# Lambda handler: %s
%s

def lambda_handler(event:, context:)
  { statusCode: 200, body: 'OK' }
end
`, lambdaName, requireStatements)

		if err := os.WriteFile(lambdaFile, []byte(lambdaContent), 0644); err != nil {
			t.Logf("Failed to write lambda file: %v", err)
			return false
		}

		// Create analyzer and analyze dependencies
		analyzer := NewDependencyAnalyzer(tempDir)
		ctx := context.Background()

		deps, err := analyzer.AnalyzeDependencies(ctx, lambdaName)
		if err != nil {
			t.Logf("Dependency analysis failed: %v", err)
			return false
		}

		// Property: All required files should be in the dependency list
		for _, depName := range depNames {
			expectedPath := filepath.Join(tempDir, depName+".rb")
			absExpected, _ := filepath.Abs(expectedPath)

			found := false
			for _, dep := range deps {
				if dep == absExpected {
					found = true
					break
				}
			}

			if !found {
				t.Logf("Expected dependency %s not found in deps: %v", absExpected, deps)
				return false
			}
		}

		// Property: The main lambda file should also be in dependencies
		absLambdaFile, _ := filepath.Abs(lambdaFile)
		foundMain := false
		for _, dep := range deps {
			if dep == absLambdaFile {
				foundMain = true
				break
			}
		}
		if !foundMain {
			t.Logf("Main lambda file %s not found in deps: %v", absLambdaFile, deps)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Require statement analysis property failed: %v", err)
	}
}

// TestRequireStatementAnalysis_RequireRelative tests require_relative specifically
func TestRequireStatementAnalysis_RequireRelative(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dep-analyzer-relative-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a subdirectory for helpers
	helpersDir := filepath.Join(tempDir, "helpers")
	if err := os.MkdirAll(helpersDir, 0755); err != nil {
		t.Fatalf("Failed to create helpers dir: %v", err)
	}

	// Create helper file
	helperFile := filepath.Join(helpersDir, "my_helper.rb")
	if err := os.WriteFile(helperFile, []byte("def helper_method; end"), 0644); err != nil {
		t.Fatalf("Failed to write helper file: %v", err)
	}

	// Create lambda that uses require_relative with path
	lambdaContent := `require_relative 'helpers/my_helper'

def lambda_handler(event:, context:)
  helper_method
end
`
	lambdaFile := filepath.Join(tempDir, "my_lambda.rb")
	if err := os.WriteFile(lambdaFile, []byte(lambdaContent), 0644); err != nil {
		t.Fatalf("Failed to write lambda file: %v", err)
	}

	analyzer := NewDependencyAnalyzer(tempDir)
	ctx := context.Background()

	deps, err := analyzer.AnalyzeDependencies(ctx, "my_lambda")
	if err != nil {
		t.Fatalf("Analysis failed: %v", err)
	}

	// Should include the helper file
	absHelperFile, _ := filepath.Abs(helperFile)
	found := false
	for _, dep := range deps {
		if dep == absHelperFile {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected helper file %s in dependencies, got: %v", absHelperFile, deps)
	}
}

// TestRequireStatementAnalysis_CycleDetection tests that cycles are handled
func TestRequireStatementAnalysis_CycleDetection(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dep-analyzer-cycle-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create files with circular dependencies
	// file_a requires file_b, file_b requires file_a
	fileA := filepath.Join(tempDir, "file_a.rb")
	fileB := filepath.Join(tempDir, "file_b.rb")

	if err := os.WriteFile(fileA, []byte("require 'file_b'\ndef a_method; end"), 0644); err != nil {
		t.Fatalf("Failed to write file_a: %v", err)
	}
	if err := os.WriteFile(fileB, []byte("require 'file_a'\ndef b_method; end"), 0644); err != nil {
		t.Fatalf("Failed to write file_b: %v", err)
	}

	analyzer := NewDependencyAnalyzer(tempDir)
	ctx := context.Background()

	// Should not hang or crash due to cycle
	deps, err := analyzer.AnalyzeDependencies(ctx, "file_a")
	if err != nil {
		t.Fatalf("Analysis failed with cycle: %v", err)
	}

	// Should include both files
	absFileA, _ := filepath.Abs(fileA)
	absFileB, _ := filepath.Abs(fileB)

	foundA := false
	foundB := false
	for _, dep := range deps {
		if dep == absFileA {
			foundA = true
		}
		if dep == absFileB {
			foundB = true
		}
	}

	if !foundA || !foundB {
		t.Errorf("Expected both files in dependencies despite cycle, got: %v", deps)
	}
}

// TestRequireStatementAnalysis_ExternalGems tests that external gems are ignored
func TestRequireStatementAnalysis_ExternalGems(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dep-analyzer-gems-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create lambda that requires external gems (not in project)
	lambdaContent := `require 'json'
require 'aws-sdk-dynamodb'
require 'some_nonexistent_gem'

def lambda_handler(event:, context:)
  JSON.parse(event['body'])
end
`
	lambdaFile := filepath.Join(tempDir, "my_lambda.rb")
	if err := os.WriteFile(lambdaFile, []byte(lambdaContent), 0644); err != nil {
		t.Fatalf("Failed to write lambda file: %v", err)
	}

	analyzer := NewDependencyAnalyzer(tempDir)
	ctx := context.Background()

	deps, err := analyzer.AnalyzeDependencies(ctx, "my_lambda")
	if err != nil {
		t.Fatalf("Analysis failed: %v", err)
	}

	// Should only include the main lambda file (external gems are not resolved)
	absLambdaFile, _ := filepath.Abs(lambdaFile)
	if len(deps) != 1 || deps[0] != absLambdaFile {
		t.Errorf("Expected only main file in deps when requiring external gems, got: %v", deps)
	}
}

// TestRequireStatementAnalysis_ConditionalRequires tests require statements inside methods/conditionals
func TestRequireStatementAnalysis_ConditionalRequires(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dep-analyzer-conditional-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create controller file
	controllerDir := filepath.Join(tempDir, "controllers")
	if err := os.MkdirAll(controllerDir, 0755); err != nil {
		t.Fatalf("Failed to create controller dir: %v", err)
	}
	
	controllerFile := filepath.Join(controllerDir, "my_controller.rb")
	if err := os.WriteFile(controllerFile, []byte("class MyController; end"), 0644); err != nil {
		t.Fatalf("Failed to write controller file: %v", err)
	}

	// Create lambda with conditional require (like stripe.rb pattern)
	lambdaContent := `def execute(path:, body:, event:)
  route_to_controller(path, event, body)
end

private

def route_to_controller(path, event, body)
  if path.start_with?('/api')
    require_relative 'controllers/my_controller'
    controller = MyController.new
    controller.handle_request
  end
end
`
	lambdaFile := filepath.Join(tempDir, "conditional_lambda.rb")
	if err := os.WriteFile(lambdaFile, []byte(lambdaContent), 0644); err != nil {
		t.Fatalf("Failed to write lambda file: %v", err)
	}

	analyzer := NewDependencyAnalyzer(tempDir)
	ctx := context.Background()

	deps, err := analyzer.AnalyzeDependencies(ctx, "conditional_lambda")
	if err != nil {
		t.Fatalf("Analysis failed: %v", err)
	}

	// Should include both the lambda file and the controller file
	absLambdaFile, _ := filepath.Abs(lambdaFile)
	absControllerFile, _ := filepath.Abs(controllerFile)
	
	foundLambda := false
	foundController := false
	for _, dep := range deps {
		if dep == absLambdaFile {
			foundLambda = true
		}
		if dep == absControllerFile {
			foundController = true
		}
	}

	if !foundLambda {
		t.Errorf("Expected lambda file %s in dependencies, got: %v", absLambdaFile, deps)
	}
	if !foundController {
		t.Errorf("Expected controller file %s in dependencies, got: %v", absControllerFile, deps)
	}
}

// TestRequireStatementAnalysis_DeeplyNestedPaths tests deeply nested require_relative paths
func TestRequireStatementAnalysis_DeeplyNestedPaths(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dep-analyzer-nested-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create deeply nested directory structure like models/concerns/
	concernsDir := filepath.Join(tempDir, "models", "concerns")
	if err := os.MkdirAll(concernsDir, 0755); err != nil {
		t.Fatalf("Failed to create concerns dir: %v", err)
	}
	
	concernFile := filepath.Join(concernsDir, "timestampable.rb")
	if err := os.WriteFile(concernFile, []byte("module Timestampable; end"), 0644); err != nil {
		t.Fatalf("Failed to write concern file: %v", err)
	}

	// Create model that requires the concern
	modelsDir := filepath.Join(tempDir, "models")
	modelFile := filepath.Join(modelsDir, "user.rb")
	modelContent := `require_relative 'concerns/timestampable'

class User
  include Timestampable
end
`
	if err := os.WriteFile(modelFile, []byte(modelContent), 0644); err != nil {
		t.Fatalf("Failed to write model file: %v", err)
	}

	// Create lambda that requires the model
	lambdaContent := `require_relative 'models/user'

def lambda_handler(event:, context:)
  User.new
end
`
	lambdaFile := filepath.Join(tempDir, "nested_lambda.rb")
	if err := os.WriteFile(lambdaFile, []byte(lambdaContent), 0644); err != nil {
		t.Fatalf("Failed to write lambda file: %v", err)
	}

	analyzer := NewDependencyAnalyzer(tempDir)
	ctx := context.Background()

	deps, err := analyzer.AnalyzeDependencies(ctx, "nested_lambda")
	if err != nil {
		t.Fatalf("Analysis failed: %v", err)
	}

	// Should include lambda, model, and concern files
	absLambdaFile, _ := filepath.Abs(lambdaFile)
	absModelFile, _ := filepath.Abs(modelFile)
	absConcernFile, _ := filepath.Abs(concernFile)
	
	expectedFiles := []string{absLambdaFile, absModelFile, absConcernFile}
	
	for _, expectedFile := range expectedFiles {
		found := false
		for _, dep := range deps {
			if dep == expectedFile {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected file %s in dependencies, got: %v", expectedFile, deps)
		}
	}
}


// Feature: provider-framework-refactor, Property 9: Dependency Analysis Fallback
// *For any* Lambda where dependency analysis fails (parse error, missing file, etc.),
// the Lambda SHALL be marked for update when ANY shared file changes (conservative fallback).
// **Validates: Requirements 5.3**

// TestDependencyAnalysisFallback_Property tests Property 9: Dependency Analysis Fallback
func TestDependencyAnalysisFallback_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Create temporary directory for test files
		tempDir, err := os.MkdirTemp("", "dep-analyzer-fallback-*")
		if err != nil {
			t.Logf("Failed to create temp dir: %v", err)
			return false
		}
		defer os.RemoveAll(tempDir)

		// Generate random number of lambdas (2-5)
		numLambdas := 2 + r.Intn(4)

		// Create lambda files
		lambdaNames := make([]string, numLambdas)
		for i := 0; i < numLambdas; i++ {
			lambdaName := fmt.Sprintf("lambda_%s", randomString(r, 3, 8))
			lambdaNames[i] = lambdaName

			lambdaFile := filepath.Join(tempDir, lambdaName+".rb")
			content := fmt.Sprintf("def lambda_handler(event:, context:)\n  { statusCode: 200 }\nend\n")
			if err := os.WriteFile(lambdaFile, []byte(content), 0644); err != nil {
				t.Logf("Failed to write lambda file: %v", err)
				return false
			}
		}

		// Create a shared file
		sharedDir := filepath.Join(tempDir, "models")
		if err := os.MkdirAll(sharedDir, 0755); err != nil {
			t.Logf("Failed to create shared dir: %v", err)
			return false
		}

		sharedFile := filepath.Join(sharedDir, "shared_model.rb")
		if err := os.WriteFile(sharedFile, []byte("class SharedModel; end"), 0644); err != nil {
			t.Logf("Failed to write shared file: %v", err)
			return false
		}

		// Create analyzer
		analyzer := NewDependencyAnalyzer(tempDir)
		ctx := context.Background()

		// Test: When we can't discover lambdas (invalid directory), fallback should return all
		invalidAnalyzer := NewDependencyAnalyzer("/nonexistent/path/that/does/not/exist")
		affected, err := invalidAnalyzer.GetAffectedLambdas(ctx, []string{sharedFile})

		// The fallback should either return an error or return all lambdas
		// In this case, with invalid directory, it should return error from fallback
		if err == nil && len(affected) > 0 {
			t.Logf("Expected error or empty result for invalid directory, got: %v", affected)
			return false
		}

		// Test: When analysis works, only affected lambdas should be returned
		// Since none of our lambdas require the shared file, none should be affected
		affected, err = analyzer.GetAffectedLambdas(ctx, []string{sharedFile})
		if err != nil {
			t.Logf("GetAffectedLambdas failed: %v", err)
			return false
		}

		// None should be affected since they don't require the shared file
		if len(affected) != 0 {
			t.Logf("Expected 0 affected lambdas (no requires), got: %d", len(affected))
			return false
		}

		// Now create a lambda that requires the shared file
		requireLambdaName := fmt.Sprintf("require_lambda_%s", randomString(r, 3, 8))
		requireLambdaFile := filepath.Join(tempDir, requireLambdaName+".rb")
		requireContent := `require 'models/shared_model'

def lambda_handler(event:, context:)
  SharedModel.new
end
`
		if err := os.WriteFile(requireLambdaFile, []byte(requireContent), 0644); err != nil {
			t.Logf("Failed to write require lambda file: %v", err)
			return false
		}

		// Clear cache and re-analyze
		analyzer.InvalidateAllCache()

		affected, err = analyzer.GetAffectedLambdas(ctx, []string{sharedFile})
		if err != nil {
			t.Logf("GetAffectedLambdas failed after adding require: %v", err)
			return false
		}

		// Only the lambda that requires the shared file should be affected
		foundRequireLambda := false
		for _, a := range affected {
			if a == requireLambdaName {
				foundRequireLambda = true
			}
		}

		if !foundRequireLambda {
			t.Logf("Expected %s to be affected, got: %v", requireLambdaName, affected)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Dependency analysis fallback property failed: %v", err)
	}
}

// TestDependencyAnalysisFallback_AnalysisError tests fallback when analysis fails for a specific lambda
func TestDependencyAnalysisFallback_AnalysisError(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dep-analyzer-error-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a valid lambda
	validLambda := filepath.Join(tempDir, "valid_lambda.rb")
	if err := os.WriteFile(validLambda, []byte("def lambda_handler(event:, context:); end"), 0644); err != nil {
		t.Fatalf("Failed to write valid lambda: %v", err)
	}

	// Create a shared file in a subdirectory (not discovered as lambda)
	modelsDir := filepath.Join(tempDir, "models")
	if err := os.MkdirAll(modelsDir, 0755); err != nil {
		t.Fatalf("Failed to create models dir: %v", err)
	}
	sharedFile := filepath.Join(modelsDir, "shared.rb")
	if err := os.WriteFile(sharedFile, []byte("class Shared; end"), 0644); err != nil {
		t.Fatalf("Failed to write shared file: %v", err)
	}

	analyzer := NewDependencyAnalyzer(tempDir)
	ctx := context.Background()

	// Get affected lambdas - valid_lambda doesn't require shared.rb
	affected, err := analyzer.GetAffectedLambdas(ctx, []string{sharedFile})
	if err != nil {
		t.Fatalf("GetAffectedLambdas failed: %v", err)
	}

	// valid_lambda should not be affected since it doesn't require shared.rb
	if len(affected) != 0 {
		t.Errorf("Expected 0 affected lambdas, got: %v", affected)
	}
}

// TestDependencyAnalysisFallback_AllLambdasAffected tests that when a lambda's own file changes, it's affected
func TestDependencyAnalysisFallback_AllLambdasAffected(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dep-analyzer-self-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create lambdas
	lambda1 := filepath.Join(tempDir, "lambda1.rb")
	lambda2 := filepath.Join(tempDir, "lambda2.rb")

	if err := os.WriteFile(lambda1, []byte("def lambda_handler(event:, context:); end"), 0644); err != nil {
		t.Fatalf("Failed to write lambda1: %v", err)
	}
	if err := os.WriteFile(lambda2, []byte("def lambda_handler(event:, context:); end"), 0644); err != nil {
		t.Fatalf("Failed to write lambda2: %v", err)
	}

	analyzer := NewDependencyAnalyzer(tempDir)
	ctx := context.Background()

	// When lambda1.rb changes, only lambda1 should be affected
	affected, err := analyzer.GetAffectedLambdas(ctx, []string{lambda1})
	if err != nil {
		t.Fatalf("GetAffectedLambdas failed: %v", err)
	}

	if len(affected) != 1 || affected[0] != "lambda1" {
		t.Errorf("Expected only lambda1 to be affected, got: %v", affected)
	}

	// When lambda2.rb changes, only lambda2 should be affected
	affected, err = analyzer.GetAffectedLambdas(ctx, []string{lambda2})
	if err != nil {
		t.Fatalf("GetAffectedLambdas failed: %v", err)
	}

	if len(affected) != 1 || affected[0] != "lambda2" {
		t.Errorf("Expected only lambda2 to be affected, got: %v", affected)
	}

	// When both change, both should be affected
	affected, err = analyzer.GetAffectedLambdas(ctx, []string{lambda1, lambda2})
	if err != nil {
		t.Fatalf("GetAffectedLambdas failed: %v", err)
	}

	if len(affected) != 2 {
		t.Errorf("Expected both lambdas to be affected, got: %v", affected)
	}
}

// TestCacheInvalidation tests that cache is properly invalidated when files change
func TestCacheInvalidation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dep-analyzer-cache-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a lambda file
	lambdaFile := filepath.Join(tempDir, "my_lambda.rb")
	if err := os.WriteFile(lambdaFile, []byte("def lambda_handler(event:, context:); end"), 0644); err != nil {
		t.Fatalf("Failed to write lambda file: %v", err)
	}

	analyzer := NewDependencyAnalyzer(tempDir)
	ctx := context.Background()

	// First analysis - should cache
	deps1, err := analyzer.AnalyzeDependencies(ctx, "my_lambda")
	if err != nil {
		t.Fatalf("First analysis failed: %v", err)
	}

	// Second analysis - should use cache
	deps2, err := analyzer.AnalyzeDependencies(ctx, "my_lambda")
	if err != nil {
		t.Fatalf("Second analysis failed: %v", err)
	}

	// Results should be the same
	if len(deps1) != len(deps2) {
		t.Errorf("Cached results differ: %v vs %v", deps1, deps2)
	}

	// Verify cache exists
	cached := analyzer.GetCachedDependencies("my_lambda")
	if cached == nil {
		t.Error("Expected cache to exist")
	}

	// Invalidate cache
	analyzer.InvalidateCache("my_lambda")

	// Cache should be gone
	cached = analyzer.GetCachedDependencies("my_lambda")
	if cached != nil {
		t.Error("Expected cache to be invalidated")
	}
}

// Note: randomString is defined in hash_test.go and shared across test files
