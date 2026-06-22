// internal/resources/dispatcher_resource_test.go
package resources

import (
	"testing"
)

// TestParseImportID tests the parseImportID function
func TestParseImportID(t *testing.T) {
	tests := []struct {
		name            string
		importID        string
		expectedAppName string
		expectedEnv     string
		expectError     bool
		errorContains   string
	}{
		{
			name:            "simple app name and environment",
			importID:        "myapp-prod",
			expectedAppName: "myapp",
			expectedEnv:     "prod",
			expectError:     false,
		},
		{
			name:            "app name with hyphen",
			importID:        "my-app-prod",
			expectedAppName: "my-app",
			expectedEnv:     "prod",
			expectError:     false,
		},
		{
			name:            "app name with multiple hyphens",
			importID:        "my-cool-app-staging",
			expectedAppName: "my-cool-app",
			expectedEnv:     "staging",
			expectError:     false,
		},
		{
			name:            "short environment name",
			importID:        "api-dev",
			expectedAppName: "api",
			expectedEnv:     "dev",
			expectError:     false,
		},
		{
			name:            "long environment name",
			importID:        "service-development",
			expectedAppName: "service",
			expectedEnv:     "development",
			expectError:     false,
		},
		{
			name:          "empty import ID",
			importID:      "",
			expectError:   true,
			errorContains: "cannot be empty",
		},
		{
			name:          "no hyphen in import ID",
			importID:      "myappprod",
			expectError:   true,
			errorContains: "must contain at least one hyphen",
		},
		{
			name:          "only hyphen",
			importID:      "-",
			expectError:   true,
			errorContains: "app_name cannot be empty",
		},
		{
			name:          "hyphen at start",
			importID:      "-prod",
			expectError:   true,
			errorContains: "app_name cannot be empty",
		},
		{
			name:          "hyphen at end",
			importID:      "myapp-",
			expectError:   true,
			errorContains: "environment cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appName, env, err := parseImportID(tt.importID)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
					return
				}
				if tt.errorContains != "" && !importContainsString(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain '%s', got '%s'", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if appName != tt.expectedAppName {
				t.Errorf("expected appName '%s', got '%s'", tt.expectedAppName, appName)
			}

			if env != tt.expectedEnv {
				t.Errorf("expected environment '%s', got '%s'", tt.expectedEnv, env)
			}
		})
	}
}

// TestSplitImportID tests the legacy splitImportID function
func TestSplitImportID(t *testing.T) {
	tests := []struct {
		name          string
		importID      string
		expectedParts []string
	}{
		{
			name:          "simple app name and environment",
			importID:      "myapp-prod",
			expectedParts: []string{"myapp", "prod"},
		},
		{
			name:          "app name with hyphen",
			importID:      "my-app-prod",
			expectedParts: []string{"my-app", "prod"},
		},
		{
			name:          "no hyphen",
			importID:      "myappprod",
			expectedParts: []string{"myappprod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := splitImportID(tt.importID)

			if len(parts) != len(tt.expectedParts) {
				t.Errorf("expected %d parts, got %d", len(tt.expectedParts), len(parts))
				return
			}

			for i, expected := range tt.expectedParts {
				if parts[i] != expected {
					t.Errorf("expected part[%d] to be '%s', got '%s'", i, expected, parts[i])
				}
			}
		})
	}
}

// importContainsString checks if a string contains a substring
func importContainsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && importContainsSubstring(s, substr))
}

func importContainsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
