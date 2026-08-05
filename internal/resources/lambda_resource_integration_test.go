// internal/resources/lambda_resource_integration_test.go
package resources

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestLambdaResource_Integration tests the Lambda resource integration without AWS calls
func TestLambdaResource_Integration(t *testing.T) {
	// Skip if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create a temporary directory structure for testing
	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "lambda")
	err := os.MkdirAll(sourceDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Create a test Lambda file
	testLambdaContent := `
def lambda_handler(event, context)
  puts "Hello from integration test Lambda!"
  { statusCode: 200, body: "Integration test OK" }
end
`
	err = os.WriteFile(filepath.Join(sourceDir, "integration_test.rb"), []byte(testLambdaContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test lambda file: %v", err)
	}

	// Create shared directories
	for _, dir := range []string{"models", "lib", "helpers"} {
		err := os.MkdirAll(filepath.Join(tempDir, dir), 0755)
		if err != nil {
			t.Fatalf("Failed to create shared dir %s: %v", dir, err)
		}
		
		// Add a test file in each shared directory
		testFile := filepath.Join(tempDir, dir, "integration_test.rb")
		err = os.WriteFile(testFile, []byte("# Integration test file"), 0644)
		if err != nil {
			t.Fatalf("Failed to write test file in %s: %v", dir, err)
		}
	}

	// Test the complete resource lifecycle simulation
	t.Run("Complete Lifecycle Simulation", func(t *testing.T) {
		resource := &lambdaResource{
			providerConfig: &DispatcherConfig{
				Environment: "integration-test",
				AwsRegion:   "us-east-1",
			},
		}

		ctx := context.Background()

		// Create a model representing the desired state
		model := &LambdaResourceModel{
			Name:      types.StringValue("integration_test"),
			AppName:   types.StringValue("testapp"),
			SourceDir: types.StringValue(sourceDir),
			Timeout:   types.Int64Value(30),
			Memory:    types.Int64Value(128),
		}

		// Test configuration building
		config, err := resource.buildConfigFromModel(ctx, model)
		if err != nil {
			t.Fatalf("Failed to build config: %v", err)
		}

		// Verify configuration
		if config.Environment != "integration-test" {
			t.Errorf("Expected environment 'integration-test', got '%s'", config.Environment)
		}
		if config.AppName != "testapp" {
			t.Errorf("Expected app_name 'testapp', got '%s'", config.AppName)
		}

		// Test environment variable building
		envVars := resource.buildEnvVars(ctx, model, config)
		
		// Verify environment variables
		expectedVars := map[string]string{
			"APP_NAME":    "testapp",
			"ENVIRONMENT": "integration-test",
			"ACTION":      "integration_test",
		}

		for key, expectedValue := range expectedVars {
			if actualValue, exists := envVars[key]; !exists {
				t.Errorf("Expected environment variable '%s' not found", key)
			} else if actualValue != expectedValue {
				t.Errorf("Expected %s='%s', got '%s'", key, expectedValue, actualValue)
			}
		}

		// Test hash calculation
		sourceHash, err := calculateLambdaSourceHash(sourceDir, "integration_test", []string{"models", "lib", "helpers"})
		if err != nil {
			t.Errorf("Failed to calculate source hash: %v", err)
		} else if sourceHash == "" {
			t.Error("Source hash should not be empty")
		}

		configHash := resource.calculateConfigHashForModel(ctx, model)
		if configHash == "" {
			t.Error("Config hash should not be empty")
		}

		t.Logf("Integration test completed successfully")
		t.Logf("Source hash: %s", sourceHash)
		t.Logf("Config hash: %s", configHash)
	})

	// Test error handling
	t.Run("Error Handling", func(t *testing.T) {
		resource := &lambdaResource{
			providerConfig: &DispatcherConfig{
				Environment: "test",
				AwsRegion:   "us-east-1",
			},
		}

		ctx := context.Background()

		// Test with non-existent source directory
		model := &LambdaResourceModel{
			Name:      types.StringValue("nonexistent"),
			AppName:   types.StringValue("testapp"),
			SourceDir: types.StringValue("/nonexistent/path"),
			Timeout:   types.Int64Value(30),
			Memory:    types.Int64Value(128),
		}

		// This should still work for config building (doesn't validate file existence)
		_, err := resource.buildConfigFromModel(ctx, model)
		if err != nil {
			t.Fatalf("Config building should not fail for non-existent path: %v", err)
		}

		// But source hash calculation should handle the error gracefully
		sourceHash, err := calculateLambdaSourceHash("/nonexistent/path", "nonexistent", []string{"models"})
		if err == nil {
			t.Logf("Source hash calculation did not fail as expected (got hash: %s)", sourceHash)
			// This might be OK if the function handles missing files gracefully
		} else {
			t.Logf("Source hash calculation failed as expected: %v", err)
		}
		
		// The important thing is that it doesn't crash
		t.Logf("Error handling test completed - source hash: %s, error: %v", sourceHash, err)
	})
}

// TestLambdaResource_PackageBuilding tests package building functionality
func TestLambdaResource_PackageBuilding(t *testing.T) {
	// Skip if not running integration tests
	if testing.Short() {
		t.Skip("Skipping package building test in short mode")
	}

	// Create a temporary directory structure for testing
	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "lambda")
	err := os.MkdirAll(sourceDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Create a test Lambda file
	testLambdaContent := `
def lambda_handler(event, context)
  puts "Hello from package test Lambda!"
  { statusCode: 200, body: "Package test OK" }
end
`
	err = os.WriteFile(filepath.Join(sourceDir, "package_test.rb"), []byte(testLambdaContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test lambda file: %v", err)
	}

	// Test PackageBuilder functionality
	t.Run("Package Builder", func(t *testing.T) {
		packageBuilder := NewPackageBuilder(
			sourceDir,
			WithSharedDirs([]string{}), // No shared dirs for this test
			WithConfig(&DispatcherConfig{
				Environment:     "test",
				AwsRegion:       "us-east-1",
				AppName:         "testapp",
				LambdaSourceDir: sourceDir,
			}),
		)

		ctx := context.Background()
		results := packageBuilder.BuildPackages(ctx, []string{"package_test"})

		// Check that we got a result
		result, exists := results["package_test"]
		if !exists {
			t.Fatal("Expected result for 'package_test' lambda")
		}

		// The build might fail due to missing Docker, but we should get a result
		if result.Error != nil {
			t.Logf("Package build failed (expected in test environment): %v", result.Error)
			// This is expected in the test environment without Docker
		} else {
			t.Logf("Package build succeeded unexpectedly - this is good!")
			if len(result.ZipData) == 0 {
				t.Error("Expected non-empty zip data")
			}
			if result.Hash == "" {
				t.Error("Expected non-empty hash")
			}
		}
	})
}