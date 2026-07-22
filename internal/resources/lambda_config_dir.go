package resources

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// loadLambdaConfigFromDir reads all .yml files from a directory and merges them
// into a lambda_config-compatible map. Each file is named <lambda>.yml and uses
// a database.yml-like structure with environment-specific overrides:
//
//	default: &default
//	  timeout: 60
//	  memory_size: 512
//	  env_keys:
//	    - IMAGES_BUCKET_NAME
//
//	dev:
//	  <<: *default
//	  memory_size: 256
//
//	prod:
//	  <<: *default
//	  memory_size: 1024
func loadLambdaConfigFromDir(dir string, environment string) (map[string]interface{}, error) {
	if dir == "" {
		return nil, nil
	}

	// Resolve the directory path
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve lambda_config_dir path: %w", err)
	}

	info, err := os.Stat(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Directory doesn't exist — not an error, just no YAML config
		}
		return nil, fmt.Errorf("failed to stat lambda_config_dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("lambda_config_dir is not a directory: %s", absDir)
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read lambda_config_dir: %w", err)
	}

	result := make(map[string]interface{})

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}

		lambdaName := strings.TrimSuffix(strings.TrimSuffix(name, ".yml"), ".yaml")
		filePath := filepath.Join(absDir, name)

		config, err := loadSingleLambdaConfig(filePath, environment)
		if err != nil {
			return nil, fmt.Errorf("failed to load %s: %w", filePath, err)
		}

		if config != nil {
			result[lambdaName] = config
		}
	}

	return result, nil
}

// loadSingleLambdaConfig reads a single YAML file and resolves the config for
// the given environment. It merges default < environment-specific.
func loadSingleLambdaConfig(filePath string, environment string) (map[string]interface{}, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	if raw == nil {
		return nil, nil
	}

	// Get default config
	base := extractYAMLMap(raw, "default")

	// Get environment-specific config
	envOverride := extractYAMLMap(raw, environment)

	// Merge: default < environment
	merged := deepMergeYAML(base, envOverride)

	if len(merged) == 0 {
		return nil, nil
	}

	// Convert to lambda_config-compatible format
	return convertToLambdaConfig(merged), nil
}

// extractYAMLMap gets a map value from a parent map, handling type assertions.
func extractYAMLMap(parent map[string]interface{}, key string) map[string]interface{} {
	val, exists := parent[key]
	if !exists {
		return nil
	}
	if m, ok := val.(map[string]interface{}); ok {
		return m
	}
	return nil
}

// deepMergeYAML deep-merges two maps, with override taking precedence.
func deepMergeYAML(base, override map[string]interface{}) map[string]interface{} {
	if base == nil && override == nil {
		return nil
	}
	result := make(map[string]interface{})
	if base != nil {
		for k, v := range base {
			result[k] = v
		}
	}
	if override != nil {
		for k, v := range override {
			if existingMap, ok := result[k].(map[string]interface{}); ok {
				if newMap, ok := v.(map[string]interface{}); ok {
					result[k] = deepMergeYAML(existingMap, newMap)
					continue
				}
			}
			result[k] = v
		}
	}
	return result
}

// convertToLambdaConfig transforms YAML fields into the format that the
// provider's lambda_config processing expects.
func convertToLambdaConfig(merged map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Direct passthrough fields
	supportedKeys := []string{
		"timeout", "memory_size", "env_vars",
		"s3_buckets", "dynamodb_tables", "ses_emails",
		"sns_triggers", "sqs_triggers",
		"reserved_concurrency", "ephemeral_storage",
		"vpc_config",
	}

	for _, key := range supportedKeys {
		if val, exists := merged[key]; exists {
			result[key] = val
		}
	}

	// Handle env_keys: convert list of key names into env_vars entries with empty values.
	// These serve as declarations that Terraform should provide values for.
	// They get merged into env_vars.
	if envKeys, exists := merged["env_keys"]; exists {
		envVars := make(map[string]interface{})
		// Start with existing env_vars if present
		if existing, ok := result["env_vars"].(map[string]interface{}); ok {
			for k, v := range existing {
				envVars[k] = v
			}
		}
		// Add env_keys as empty-value entries (Terraform variable references)
		if keyList, ok := envKeys.([]interface{}); ok {
			for _, k := range keyList {
				if keyStr, ok := k.(string); ok {
					// Only add if not already set by env_vars
					if _, exists := envVars[keyStr]; !exists {
						envVars[keyStr] = ""
					}
				}
			}
		}
		if len(envVars) > 0 {
			result["env_vars"] = envVars
		}
	}

	return result
}

// mergeLambdaConfigs deep-merges YAML-based config with Terraform lambda_config.
// Terraform values take precedence (they contain dynamic references).
func mergeLambdaConfigs(yamlConfig, tfConfig map[string]interface{}) map[string]interface{} {
	if yamlConfig == nil {
		return tfConfig
	}
	if tfConfig == nil {
		return yamlConfig
	}

	result := make(map[string]interface{})

	// Start with all YAML keys
	for k, v := range yamlConfig {
		result[k] = v
	}

	// Override/merge with TF config
	for lambdaName, tfVal := range tfConfig {
		yamlVal, exists := result[lambdaName]
		if !exists {
			result[lambdaName] = tfVal
			continue
		}

		// Both exist — deep merge the lambda configs
		yamlMap := toMapInterface(yamlVal)
		tfMap := toMapInterface(tfVal)

		if yamlMap != nil && tfMap != nil {
			result[lambdaName] = deepMergeLambdaEntry(yamlMap, tfMap)
		} else {
			// TF wins if we can't merge
			result[lambdaName] = tfVal
		}
	}

	return result
}

// deepMergeLambdaEntry merges two lambda config entries.
// TF values override YAML, but env_vars are merged (TF env_vars override YAML env_vars per key).
func deepMergeLambdaEntry(base, override map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range base {
		result[k] = v
	}
	for k, v := range override {
		if k == "env_vars" {
			// Merge env_vars maps
			baseEnvVars := toMapInterface(result["env_vars"])
			overrideEnvVars := toMapInterface(v)
			if baseEnvVars != nil && overrideEnvVars != nil {
				merged := make(map[string]interface{})
				for ek, ev := range baseEnvVars {
					merged[ek] = ev
				}
				for ek, ev := range overrideEnvVars {
					merged[ek] = ev
				}
				result[k] = merged
			} else {
				result[k] = v
			}
		} else {
			result[k] = v
		}
	}
	return result
}

// toMapInterface attempts to convert an interface{} to map[string]interface{}.
func toMapInterface(val interface{}) map[string]interface{} {
	if val == nil {
		return nil
	}
	if m, ok := val.(map[string]interface{}); ok {
		return m
	}
	return nil
}

// sortedLambdaNames returns lambda names from config in sorted order for deterministic processing.
func sortedLambdaNames(config map[string]interface{}) []string {
	names := make([]string, 0, len(config))
	for k := range config {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
