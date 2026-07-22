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
//	  env_vars:
//	    WELCOME_TITLE: "Hello"
//	    COGNITO_POOL_ID: ref(cognito_user_pool_id)
//	  dynamodb_tables:
//	    slots:
//	      permissions: [BatchWriteItem]
//	      indexes:
//	        SponsorIndex:
//	          permissions: [Query]
//
//	dev:
//	  <<: *default
//	  memory_size: 256
//
//	prod:
//	  <<: *default
//	  memory_size: 1024
func loadLambdaConfigFromDir(dir string, environment string, envRefs map[string]string, appName string, awsRegion string, awsAccountId string) (map[string]interface{}, error) {
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

		config, err := loadSingleLambdaConfig(filePath, environment, envRefs, appName, awsRegion, awsAccountId)
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
func loadSingleLambdaConfig(filePath string, environment string, envRefs map[string]string, appName string, awsRegion string, awsAccountId string) (map[string]interface{}, error) {
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
	return convertToLambdaConfig(merged, envRefs, appName, environment, awsRegion, awsAccountId), nil
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
func convertToLambdaConfig(merged map[string]interface{}, envRefs map[string]string, appName, environment, awsRegion, awsAccountId string) map[string]interface{} {
	result := make(map[string]interface{})

	// Direct passthrough fields
	simpleKeys := []string{
		"timeout", "memory_size",
		"s3_buckets", "ses_emails",
		"sns_triggers", "sqs_triggers",
		"reserved_concurrency", "ephemeral_storage",
		"vpc_config",
	}

	for _, key := range simpleKeys {
		if val, exists := merged[key]; exists {
			result[key] = val
		}
	}

	// Handle env_vars: resolve ref() markers using envRefs map
	if envVarsRaw, exists := merged["env_vars"]; exists {
		if envVarsMap, ok := envVarsRaw.(map[string]interface{}); ok {
			resolved := resolveEnvVars(envVarsMap, envRefs)
			if len(resolved) > 0 {
				result["env_vars"] = resolved
			}
		}
	}

	// Handle dynamodb_tables: convert map-style YAML to array-of-objects format
	if tablesRaw, exists := merged["dynamodb_tables"]; exists {
		if tablesMap, ok := tablesRaw.(map[string]interface{}); ok {
			tables := convertDynamoDBTables(tablesMap, appName, environment, awsRegion, awsAccountId)
			if len(tables) > 0 {
				result["dynamodb_tables"] = tables
			}
		}
	}

	return result
}

// resolveEnvVars processes env_vars, resolving ref() markers from the envRefs map.
// Plain string values pass through unchanged.
func resolveEnvVars(envVars map[string]interface{}, envRefs map[string]string) map[string]interface{} {
	result := make(map[string]interface{})
	for key, val := range envVars {
		strVal, ok := val.(string)
		if !ok {
			result[key] = val
			continue
		}

		// Check for ref(name) pattern
		if refName, isRef := parseRefMarker(strVal); isRef {
			if resolvedVal, exists := envRefs[refName]; exists {
				result[key] = resolvedVal
			} else {
				// ref not found in envRefs — leave as empty string (will be caught at plan time)
				result[key] = ""
			}
		} else {
			result[key] = strVal
		}
	}
	return result
}

// parseRefMarker checks if a string matches the ref(name) pattern.
// Returns the reference name and true if it matches, or empty string and false otherwise.
func parseRefMarker(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "ref(") && strings.HasSuffix(s, ")") {
		name := s[4 : len(s)-1]
		name = strings.TrimSpace(name)
		if name != "" {
			return name, true
		}
	}
	return "", false
}

// convertDynamoDBTables converts the map-style YAML DynamoDB config into the
// array-of-objects format that the provider's IAM processing expects.
//
// Input YAML format:
//
//	dynamodb_tables:
//	  slots:
//	    permissions: [BatchWriteItem]
//	    indexes:
//	      SponsorIndex:
//	        permissions: [Query]
//	  users:
//	    permissions: [BatchGetItem]
//
// Output format (array of objects):
//
//	[
//	  {"table_arn": "arn:aws:dynamodb:...:table/app-env-slots", "permissions": ["dynamodb:BatchWriteItem"]},
//	  {"table_arn": "arn:aws:dynamodb:...:table/app-env-slots/index/SponsorIndex", "permissions": ["dynamodb:Query"]},
//	  {"table_arn": "arn:aws:dynamodb:...:table/app-env-users", "permissions": ["dynamodb:BatchGetItem"]},
//	]
func convertDynamoDBTables(tablesMap map[string]interface{}, appName, environment, awsRegion, awsAccountId string) []interface{} {
	var result []interface{}

	// Sort table names for deterministic output
	tableNames := make([]string, 0, len(tablesMap))
	for name := range tablesMap {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)

	for _, tableName := range tableNames {
		tableConfigRaw := tablesMap[tableName]

		// Build the table ARN from convention
		tableArn := fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s-%s-%s",
			awsRegion, awsAccountId, appName, environment, tableName)

		// Shorthand form: table_name: [Permission1, Permission2]
		// Value is a list — treat as permissions only.
		if permsList, ok := tableConfigRaw.([]interface{}); ok {
			permissions := normalizePermissions(permsList)
			if len(permissions) > 0 {
				entry := map[string]interface{}{
					"table_arn":   tableArn,
					"permissions": toInterfaceSlice(permissions),
				}
				result = append(result, entry)
			}
			continue
		}

		// Expanded form: table_name: {permissions: [...], indexes: {...}}
		tableConfig, ok := tableConfigRaw.(map[string]interface{})
		if !ok {
			// nil or unsupported type — skip
			continue
		}

		// Get table-level permissions (may be nil if only indexes are needed)
		if permsRaw, exists := tableConfig["permissions"]; exists && permsRaw != nil {
			permissions := normalizePermissions(permsRaw)
			if len(permissions) > 0 {
				entry := map[string]interface{}{
					"table_arn":   tableArn,
					"permissions": toInterfaceSlice(permissions),
				}
				result = append(result, entry)
			}
		}

		// Handle indexes
		if indexesRaw, exists := tableConfig["indexes"]; exists {
			if indexesMap, ok := indexesRaw.(map[string]interface{}); ok {
				// Sort index names for deterministic output
				indexNames := make([]string, 0, len(indexesMap))
				for name := range indexesMap {
					indexNames = append(indexNames, name)
				}
				sort.Strings(indexNames)

				for _, indexName := range indexNames {
					indexConfigRaw := indexesMap[indexName]
					indexArn := fmt.Sprintf("%s/index/%s", tableArn, indexName)

					// Shorthand: IndexName: [Query]
					if permsList, ok := indexConfigRaw.([]interface{}); ok {
						permissions := normalizePermissions(permsList)
						if len(permissions) > 0 {
							entry := map[string]interface{}{
								"table_arn":   indexArn,
								"permissions": toInterfaceSlice(permissions),
							}
							result = append(result, entry)
						}
						continue
					}

					// Expanded: IndexName: {permissions: [Query]}
					indexConfig, ok := indexConfigRaw.(map[string]interface{})
					if !ok {
						continue
					}

					if permsRaw, exists := indexConfig["permissions"]; exists {
						permissions := normalizePermissions(permsRaw)
						if len(permissions) > 0 {
							entry := map[string]interface{}{
								"table_arn":   indexArn,
								"permissions": toInterfaceSlice(permissions),
							}
							result = append(result, entry)
						}
					}
				}
			}
		}
	}

	return result
}

// normalizePermissions takes a YAML permissions value (list of strings like "BatchWriteItem")
// and normalizes them to the full "dynamodb:Action" format the provider expects.
func normalizePermissions(permsRaw interface{}) []string {
	var rawPerms []string

	switch v := permsRaw.(type) {
	case []interface{}:
		for _, p := range v {
			if s, ok := p.(string); ok {
				rawPerms = append(rawPerms, s)
			}
		}
	case []string:
		rawPerms = v
	default:
		return nil
	}

	var result []string
	for _, perm := range rawPerms {
		// Add "dynamodb:" prefix if not already present
		if !strings.Contains(perm, ":") {
			perm = "dynamodb:" + perm
		}
		result = append(result, perm)
	}
	sort.Strings(result)
	return result
}

// toInterfaceSlice converts a []string to []interface{} for consistent map storage.
func toInterfaceSlice(strs []string) []interface{} {
	result := make([]interface{}, len(strs))
	for i, s := range strs {
		result[i] = s
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
		// Use extractMapValue which handles both plain maps and Terraform framework types
		yamlMap, yamlOk := extractMapValue(yamlVal)
		tfMap, tfOk := extractMapValue(tfVal)

		if yamlOk && tfOk {
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
// dynamodb_tables from both sources are concatenated (both TF and YAML tables are needed).
func deepMergeLambdaEntry(base, override map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range base {
		result[k] = v
	}
	for k, v := range override {
		switch k {
		case "env_vars":
			// Merge env_vars maps (TF overrides per key)
			baseEnvVars, _ := extractMapValue(result["env_vars"])
			overrideEnvVars, _ := extractMapValue(v)
			if baseEnvVars != nil && overrideEnvVars != nil {
				merged := make(map[string]interface{})
				for ek, ev := range baseEnvVars {
					merged[ek] = ev
				}
				for ek, ev := range overrideEnvVars {
					merged[ek] = ev
				}
				result[k] = merged
			} else if overrideEnvVars != nil {
				result[k] = overrideEnvVars
			} else {
				result[k] = v
			}
		case "dynamodb_tables":
			// Concatenate dynamodb_tables (both sources may have valid entries)
			baseTables := toSliceInterface(result["dynamodb_tables"])
			overrideTables := toSliceInterface(v)
			result[k] = append(baseTables, overrideTables...)
		default:
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

// toSliceInterface attempts to convert an interface{} to []interface{}.
func toSliceInterface(val interface{}) []interface{} {
	if val == nil {
		return nil
	}
	if s, ok := val.([]interface{}); ok {
		return s
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
