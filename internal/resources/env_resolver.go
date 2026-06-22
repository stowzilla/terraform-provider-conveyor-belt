// internal/resources/env_resolver.go
package resources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// ResolveEnvVarsForAction resolves environment variables for a specific lambda
// Merges: auto-injected vars -> shared.env_vars -> lambda.env_vars
// Priority: lambda.env_vars > shared.env_vars > auto-injected
func (c *DispatcherConfig) ResolveEnvVarsForAction(lambda string, autoInjectedVars map[string]string) map[string]string {
	ctx := context.TODO()
	
	// Start with auto-injected vars (lowest priority)
	result := make(map[string]string)
	for k, v := range autoInjectedVars {
		result[k] = v
	}

	utils.Info(ctx, "Resolving env vars for lambda", map[string]interface{}{
		"lambda":              lambda,
		"lambda_config":       fmt.Sprintf("%+v", c.LambdaConfig),
		"auto_injected_count": len(autoInjectedVars),
	})

	// Apply shared env vars from lambda_config.shared.env_vars (medium priority)
	if sharedConfigRaw, exists := c.LambdaConfig["shared"]; exists {
		if sharedConfig, ok := extractMapValue(sharedConfigRaw); ok {
			if envVarsRaw, exists := sharedConfig["env_vars"]; exists {
				if envVars, ok := extractMapValue(envVarsRaw); ok {
					utils.Debug(ctx, "Applying shared env vars", map[string]interface{}{
						"var_count": len(envVars),
					})
					for key, value := range envVars {
						if strVal, ok := extractStringValue(value); ok {
							result[key] = strVal
						}
					}
				}
			}
		}
	}

	// Apply lambda-specific env vars from lambda_config.{lambda}.env_vars (highest priority)
	if lambdaConfigRaw, exists := c.LambdaConfig[lambda]; exists {
		utils.Info(ctx, "Found lambda-specific config", map[string]interface{}{
			"lambda": lambda,
			"config": fmt.Sprintf("%+v", lambdaConfigRaw),
			"type":   fmt.Sprintf("%T", lambdaConfigRaw),
		})
		
		if lambdaConfig, ok := extractMapValue(lambdaConfigRaw); ok {
			if envVarsRaw, exists := lambdaConfig["env_vars"]; exists {
				if envVars, ok := extractMapValue(envVarsRaw); ok {
					utils.Info(ctx, "Applying lambda-specific env vars", map[string]interface{}{
						"lambda":    lambda,
						"var_count": len(envVars),
					})
					
					for key, value := range envVars {
						if strVal, ok := extractStringValue(value); ok {
							utils.Info(ctx, "Adding lambda-specific env var", map[string]interface{}{
								"lambda": lambda,
								"key":    key,
								"value":  strVal,
							})
							result[key] = strVal
						} else {
							utils.Warn(ctx, "Failed to extract string value", map[string]interface{}{
								"lambda": lambda,
								"key":    key,
								"type":   fmt.Sprintf("%T", value),
							})
						}
					}
				}
			}
		} else {
			utils.Warn(ctx, "Failed to extract map from lambda config", map[string]interface{}{
				"lambda": lambda,
				"type":   fmt.Sprintf("%T", lambdaConfigRaw),
			})
		}
	} else {
		utils.Debug(ctx, "No lambda-specific config found", map[string]interface{}{
			"lambda": lambda,
		})
	}

	utils.Info(ctx, "Final resolved env vars", map[string]interface{}{
		"lambda":    lambda,
		"var_count": len(result),
		"vars":      result,
	})

	return result
}

// extractStringValue attempts to extract a string from a types.Value interface
func extractStringValue(value interface{}) (string, bool) {
	// Handle different possible types from the Terraform framework
	switch v := value.(type) {
	case types.Dynamic:
		// DynamicAttribute wraps values in types.Dynamic — unwrap and recurse
		if !v.IsNull() && !v.IsUnknown() {
			return extractStringValue(v.UnderlyingValue())
		}
	case types.String:
		if !v.IsNull() && !v.IsUnknown() {
			return v.ValueString(), true
		}
	case types.Number:
		if !v.IsNull() && !v.IsUnknown() {
			// Convert number to string
			return v.String(), true
		}
	case types.Bool:
		if !v.IsNull() && !v.IsUnknown() {
			if v.ValueBool() {
				return "true", true
			}
			return "false", true
		}
	case string:
		return v, true
	}
	return "", false
}

// extractMapValue attempts to extract a map from a types.Value interface
func extractMapValue(value interface{}) (map[string]interface{}, bool) {
	switch v := value.(type) {
	case types.Dynamic:
		// DynamicAttribute wraps values in types.Dynamic — unwrap and recurse
		if !v.IsNull() && !v.IsUnknown() {
			return extractMapValue(v.UnderlyingValue())
		}
	case types.Object:
		if !v.IsNull() && !v.IsUnknown() {
			// Convert types.Object attributes to map[string]interface{}
			result := make(map[string]interface{})
			for key, val := range v.Attributes() {
				result[key] = val
			}
			return result, true
		}
	case types.Map:
		if !v.IsNull() && !v.IsUnknown() {
			// Convert types.Map elements to map[string]interface{}
			result := make(map[string]interface{})
			for key, val := range v.Elements() {
				result[key] = val
			}
			return result, true
		}
	case map[string]interface{}:
		return v, true
	}
	return nil, false
}
