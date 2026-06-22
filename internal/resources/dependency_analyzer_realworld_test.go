// internal/resources/dependency_analyzer_realworld_test.go
package resources

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestDependencyAnalyzer_RealWorldPatterns tests the dependency analyzer
// with patterns found in actual Lambda files from the user's codebase.
func TestDependencyAnalyzer_RealWorldPatterns(t *testing.T) {
	ctx := context.Background()
	
	tempDir, err := os.MkdirTemp("", "dep-analyzer-realworld-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create directory structure similar to real Lambda project
	dirs := []string{
		"lib",
		"helpers", 
		"controllers/customer",
		"controllers/ops",
		"models/concerns",
	}
	
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(tempDir, dir), 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", dir, err)
		}
	}

	// Create files with real-world patterns
	files := map[string]string{
		// Lambda base with complex autoloading (simplified)
		"lib/lambda_base.rb": `require 'json'
require 'aws-sdk-dynamodb'
require_relative '../helpers/observability'

module LambdaBase
  def cors_headers
    { 'Access-Control-Allow-Origin' => '*' }
  end
end
`,
		
		// Helper file
		"helpers/observability.rb": `module Observability
  def self.init_logger
    puts "Logger initialized"
  end
end
`,

		// Application controller with relative requires
		"controllers/application_controller.rb": `require_relative '../lib/lambda_base'
require_relative '../helpers/observability'

class ApplicationController
  include LambdaBase
end
`,

		// Customer controller inheriting from application controller
		"controllers/customer/base_controller.rb": `require_relative '../application_controller'

module CustomerControllers
  class BaseController < ApplicationController
    def authenticate!
      # auth logic
    end
  end
end
`,

		// Concern with module definition
		"models/concerns/timestampable.rb": `module Concerns
  module Timestampable
    def set_timestamps
      # timestamp logic
    end
  end
end
`,

		// Customer Lambda with conditional requires (like stripe.rb pattern)
		"customer.rb": `require_relative 'lib/lambda_base'
require_relative 'controllers/customer/base_controller'

include LambdaBase

def execute(path:, body:, event:)
  route_to_controller(path, event, body)
end

private

def route_to_controller(path, event, body)
  if path.start_with?('/profile')
    require_relative 'controllers/customer/profile_controller'
    controller = CustomerControllers::ProfileController.new
    controller.handle_request
  end
end
`,

		// Profile controller (conditionally required)
		"controllers/customer/profile_controller.rb": `require_relative 'base_controller'
require_relative '../../models/concerns/timestampable'

module CustomerControllers
  class ProfileController < BaseController
    include Concerns::Timestampable
    
    def handle_request
      # profile logic
    end
  end
end
`,

		// Ops Lambda with different pattern
		"ops.rb": `require_relative 'lib/lambda_base'
require_relative 'controllers/ops/customers_controller'

include LambdaBase

def execute(path:, body:, event:)
  controller = OpsControllers::CustomersController.new
  controller.dispatch(:index)
end
`,

		// Ops controller
		"controllers/ops/customers_controller.rb": `require_relative '../application_controller'

module OpsControllers
  class CustomersController < ApplicationController
    def dispatch(lambda)
      # ops logic
    end
  end
end
`,
	}

	// Write all files
	for filePath, content := range files {
		fullPath := filepath.Join(tempDir, filePath)
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write file %s: %v", filePath, err)
		}
	}

	analyzer := NewDependencyAnalyzer(tempDir)

	testCases := []struct {
		name               string
		lambda             string
		expectedMinDeps    int
		mustIncludePaths   []string // relative paths that must be included
	}{
		{
			name:            "customer lambda with conditional requires",
			lambda:          "customer",
			expectedMinDeps: 5, // customer.rb + lambda_base.rb + base_controller.rb + application_controller.rb + observability.rb
			mustIncludePaths: []string{
				"customer.rb",
				"lib/lambda_base.rb",
				"controllers/customer/base_controller.rb",
				"controllers/application_controller.rb",
				"helpers/observability.rb",
			},
		},
		{
			name:            "ops lambda with direct requires",
			lambda:          "ops", 
			expectedMinDeps: 4, // ops.rb + lambda_base.rb + customers_controller.rb + application_controller.rb + observability.rb
			mustIncludePaths: []string{
				"ops.rb",
				"lib/lambda_base.rb",
				"controllers/ops/customers_controller.rb",
				"controllers/application_controller.rb",
				"helpers/observability.rb",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			deps, err := analyzer.AnalyzeDependencies(ctx, tc.lambda)
			if err != nil {
				t.Fatalf("Analysis failed: %v", err)
			}

			if len(deps) < tc.expectedMinDeps {
				t.Errorf("Expected at least %d dependencies, got %d: %v", tc.expectedMinDeps, len(deps), deps)
			}

			// Convert absolute paths to relative for comparison
			absBasePath, _ := filepath.Abs(tempDir)
			foundPaths := make(map[string]bool)
			for _, dep := range deps {
				relDep, err := filepath.Rel(absBasePath, dep)
				if err != nil {
					t.Errorf("Failed to get relative path for %s: %v", dep, err)
					continue
				}
				foundPaths[relDep] = true
			}

			// Check that all required paths are found
			for _, requiredPath := range tc.mustIncludePaths {
				if !foundPaths[requiredPath] {
					t.Errorf("Expected path %s not found in dependencies: %v", requiredPath, deps)
				}
			}
		})
	}
}

// TestDependencyAnalyzer_ConditionalRequireDetection tests that conditional requires
// inside methods are still detected by the regex-based parser.
func TestDependencyAnalyzer_ConditionalRequireDetection(t *testing.T) {
	ctx := context.Background()
	
	tempDir, err := os.MkdirTemp("", "dep-analyzer-conditional-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a controller that will be conditionally required
	controllerDir := filepath.Join(tempDir, "controllers")
	if err := os.MkdirAll(controllerDir, 0755); err != nil {
		t.Fatalf("Failed to create controller dir: %v", err)
	}
	
	controllerFile := filepath.Join(controllerDir, "dynamic_controller.rb")
	if err := os.WriteFile(controllerFile, []byte("class DynamicController; end"), 0644); err != nil {
		t.Fatalf("Failed to write controller file: %v", err)
	}

	// Create lambda with various conditional require patterns found in real code
	lambdaContent := `def execute(path:, body:, event:)
  method = event['httpMethod']
  
  # Pattern 1: Inside if statement (like stripe.rb)
  if path.start_with?('/api')
    require_relative 'controllers/dynamic_controller'
    controller = DynamicController.new
  end
  
  # Pattern 2: Inside method definition
  route_to_controller(method, path, event, body)
end

private

def route_to_controller(method, path, event, body)
  return route_dynamic(method, path, event, body) if path.start_with?('/dynamic')
  nil
end

def route_dynamic(method, path, event, body)
  # Pattern 3: Inside nested method
  require_relative 'controllers/dynamic_controller'
  controller = DynamicController.new(event: event, body: body)
  controller.handle_request
end
`
	lambdaFile := filepath.Join(tempDir, "conditional_lambda.rb")
	if err := os.WriteFile(lambdaFile, []byte(lambdaContent), 0644); err != nil {
		t.Fatalf("Failed to write lambda file: %v", err)
	}

	analyzer := NewDependencyAnalyzer(tempDir)
	deps, err := analyzer.AnalyzeDependencies(ctx, "conditional_lambda")
	if err != nil {
		t.Fatalf("Analysis failed: %v", err)
	}

	// Should detect the require_relative even though it's inside methods/conditionals
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
	
	// Should find the controller file even though it appears in multiple require statements
	// (our regex should find all occurrences)
	if len(deps) < 2 {
		t.Errorf("Expected at least 2 dependencies (lambda + controller), got: %v", deps)
	}
}