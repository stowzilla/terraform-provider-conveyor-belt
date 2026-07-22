package resources

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLambdaConfigFromDir(t *testing.T) {
	// Create temp directory with YAML files
	dir := t.TempDir()

	// Write customer.yml
	customerYAML := `default: &default
  timeout: 60
  memory_size: 512
  env_keys:
    - IMAGES_BUCKET_NAME
    - STRIPE_KEY
  env_vars:
    STATIC_VAR: "hello"

dev:
  <<: *default
  memory_size: 256

prod:
  <<: *default
  memory_size: 1024
  timeout: 30
`
	if err := os.WriteFile(filepath.Join(dir, "customer.yml"), []byte(customerYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Write worker.yml
	workerYAML := `default:
  timeout: 300
  memory_size: 256
`
	if err := os.WriteFile(filepath.Join(dir, "worker.yml"), []byte(workerYAML), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("loads dev environment", func(t *testing.T) {
		config, err := loadLambdaConfigFromDir(dir, "dev")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if config == nil {
			t.Fatal("expected config, got nil")
		}

		// Check customer config for dev
		customer, ok := config["customer"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected customer config to be a map, got %T", config["customer"])
		}

		if timeout, ok := customer["timeout"].(int); !ok || timeout != 60 {
			t.Errorf("expected customer timeout=60 for dev, got %v", customer["timeout"])
		}

		if memory, ok := customer["memory_size"].(int); !ok || memory != 256 {
			t.Errorf("expected customer memory_size=256 for dev, got %v", customer["memory_size"])
		}

		// Check env_vars includes both static and env_keys
		envVars, ok := customer["env_vars"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected env_vars to be a map, got %T", customer["env_vars"])
		}

		if envVars["STATIC_VAR"] != "hello" {
			t.Errorf("expected STATIC_VAR=hello, got %v", envVars["STATIC_VAR"])
		}
		if envVars["IMAGES_BUCKET_NAME"] != "" {
			t.Errorf("expected IMAGES_BUCKET_NAME='', got %v", envVars["IMAGES_BUCKET_NAME"])
		}
		if envVars["STRIPE_KEY"] != "" {
			t.Errorf("expected STRIPE_KEY='', got %v", envVars["STRIPE_KEY"])
		}

		// Check worker config
		worker, ok := config["worker"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected worker config to be a map, got %T", config["worker"])
		}

		if timeout, ok := worker["timeout"].(int); !ok || timeout != 300 {
			t.Errorf("expected worker timeout=300, got %v", worker["timeout"])
		}
	})

	t.Run("loads prod environment with overrides", func(t *testing.T) {
		config, err := loadLambdaConfigFromDir(dir, "prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		customer, ok := config["customer"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected customer config to be a map, got %T", config["customer"])
		}

		if timeout, ok := customer["timeout"].(int); !ok || timeout != 30 {
			t.Errorf("expected customer timeout=30 for prod, got %v", customer["timeout"])
		}

		if memory, ok := customer["memory_size"].(int); !ok || memory != 1024 {
			t.Errorf("expected customer memory_size=1024 for prod, got %v", customer["memory_size"])
		}
	})

	t.Run("returns nil for empty dir path", func(t *testing.T) {
		config, err := loadLambdaConfigFromDir("", "dev")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if config != nil {
			t.Errorf("expected nil, got %v", config)
		}
	})

	t.Run("returns nil for nonexistent directory", func(t *testing.T) {
		config, err := loadLambdaConfigFromDir("/nonexistent/path", "dev")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if config != nil {
			t.Errorf("expected nil, got %v", config)
		}
	})
}

func TestMergeLambdaConfigs(t *testing.T) {
	t.Run("TF values override YAML values", func(t *testing.T) {
		yamlConfig := map[string]interface{}{
			"customer": map[string]interface{}{
				"timeout":     60,
				"memory_size": 256,
				"env_vars": map[string]interface{}{
					"IMAGES_BUCKET_NAME": "",
					"STATIC_VAR":         "from_yaml",
				},
			},
		}

		tfConfig := map[string]interface{}{
			"customer": map[string]interface{}{
				"env_vars": map[string]interface{}{
					"IMAGES_BUCKET_NAME": "my-actual-bucket",
				},
			},
		}

		merged := mergeLambdaConfigs(yamlConfig, tfConfig)
		customer := merged["customer"].(map[string]interface{})
		envVars := customer["env_vars"].(map[string]interface{})

		if envVars["IMAGES_BUCKET_NAME"] != "my-actual-bucket" {
			t.Errorf("expected TF value to override YAML, got %v", envVars["IMAGES_BUCKET_NAME"])
		}
		if envVars["STATIC_VAR"] != "from_yaml" {
			t.Errorf("expected YAML value to be preserved, got %v", envVars["STATIC_VAR"])
		}
		// timeout and memory from YAML should be preserved
		if customer["timeout"] != 60 {
			t.Errorf("expected timeout=60 from YAML, got %v", customer["timeout"])
		}
	})

	t.Run("nil YAML returns TF config", func(t *testing.T) {
		tfConfig := map[string]interface{}{"api": map[string]interface{}{"timeout": 30}}
		result := mergeLambdaConfigs(nil, tfConfig)
		if result["api"] == nil {
			t.Error("expected TF config to be returned")
		}
	})

	t.Run("nil TF returns YAML config", func(t *testing.T) {
		yamlConfig := map[string]interface{}{"api": map[string]interface{}{"timeout": 60}}
		result := mergeLambdaConfigs(yamlConfig, nil)
		if result["api"] == nil {
			t.Error("expected YAML config to be returned")
		}
	})

	t.Run("TF-only lambdas are passed through", func(t *testing.T) {
		yamlConfig := map[string]interface{}{
			"customer": map[string]interface{}{"timeout": 60},
		}
		tfConfig := map[string]interface{}{
			"ops": map[string]interface{}{"timeout": 30},
		}

		merged := mergeLambdaConfigs(yamlConfig, tfConfig)
		if merged["customer"] == nil {
			t.Error("expected customer from YAML")
		}
		if merged["ops"] == nil {
			t.Error("expected ops from TF")
		}
	})
}
