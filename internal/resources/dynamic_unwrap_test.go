package resources

import (
	"math/big"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// TestExtractMapValue_TypesDynamic verifies that extractMapValue correctly unwraps
// types.Dynamic values, which is what DynamicAttribute produces for nested objects.
// This is the root cause of the per-lambda env_vars bug.
func TestExtractMapValue_TypesDynamic(t *testing.T) {
	// Build an inner object (simulating lambda_config.ops)
	innerAttrs := map[string]attr.Value{
		"timeout": types.NumberValue(big.NewFloat(60)),
		"env_vars": types.ObjectValueMust(
			map[string]attr.Type{
				"CLOUDFRONT_KEY_PAIR_ID": basetypes.StringType{},
				"IMAGES_BUCKET_NAME":    basetypes.StringType{},
			},
			map[string]attr.Value{
				"CLOUDFRONT_KEY_PAIR_ID": types.StringValue("KXYZ123"),
				"IMAGES_BUCKET_NAME":    types.StringValue("my-images-bucket"),
			},
		),
	}
	innerAttrTypes := map[string]attr.Type{
		"timeout": basetypes.NumberType{},
		"env_vars": types.ObjectType{
			AttrTypes: map[string]attr.Type{
				"CLOUDFRONT_KEY_PAIR_ID": basetypes.StringType{},
				"IMAGES_BUCKET_NAME":    basetypes.StringType{},
			},
		},
	}
	innerObj, diags := types.ObjectValue(innerAttrTypes, innerAttrs)
	if diags.HasError() {
		t.Fatalf("failed to create inner object: %v", diags)
	}

	// Wrap in types.Dynamic (this is what DynamicAttribute produces)
	dynamicVal := types.DynamicValue(innerObj)

	// extractMapValue should unwrap the Dynamic and return the object's attributes
	result, ok := extractMapValue(dynamicVal)
	if !ok {
		t.Fatal("extractMapValue returned false for types.Dynamic wrapping types.Object")
	}

	if _, exists := result["timeout"]; !exists {
		t.Error("expected 'timeout' key in extracted map")
	}
	if _, exists := result["env_vars"]; !exists {
		t.Error("expected 'env_vars' key in extracted map")
	}

	// Now extract env_vars (which is a types.Object inside the map)
	envVarsMap, ok := extractMapValue(result["env_vars"])
	if !ok {
		t.Fatal("extractMapValue returned false for nested env_vars object")
	}

	// Extract string values
	cfKeyPairId, ok := extractStringValue(envVarsMap["CLOUDFRONT_KEY_PAIR_ID"])
	if !ok {
		t.Fatal("extractStringValue returned false for CLOUDFRONT_KEY_PAIR_ID")
	}
	if cfKeyPairId != "KXYZ123" {
		t.Errorf("expected CLOUDFRONT_KEY_PAIR_ID='KXYZ123', got '%s'", cfKeyPairId)
	}

	bucketName, ok := extractStringValue(envVarsMap["IMAGES_BUCKET_NAME"])
	if !ok {
		t.Fatal("extractStringValue returned false for IMAGES_BUCKET_NAME")
	}
	if bucketName != "my-images-bucket" {
		t.Errorf("expected IMAGES_BUCKET_NAME='my-images-bucket', got '%s'", bucketName)
	}
}

// TestExtractStringValue_TypesDynamic verifies that extractStringValue correctly
// unwraps types.Dynamic wrapping a types.String.
func TestExtractStringValue_TypesDynamic(t *testing.T) {
	// Wrap a string in types.Dynamic
	dynamicStr := types.DynamicValue(types.StringValue("hello-world"))

	val, ok := extractStringValue(dynamicStr)
	if !ok {
		t.Fatal("extractStringValue returned false for types.Dynamic wrapping types.String")
	}
	if val != "hello-world" {
		t.Errorf("expected 'hello-world', got '%s'", val)
	}
}

// TestExtractInt64Value_TypesDynamic verifies that extractInt64Value correctly
// unwraps types.Dynamic wrapping a types.Number.
func TestExtractInt64Value_TypesDynamic(t *testing.T) {
	dynamicNum := types.DynamicValue(types.NumberValue(big.NewFloat(512)))

	val, ok := extractInt64Value(dynamicNum)
	if !ok {
		t.Fatal("extractInt64Value returned false for types.Dynamic wrapping types.Number")
	}
	if val != 512 {
		t.Errorf("expected 512, got %d", val)
	}
}

// TestBuildEnvVars_WithDynamicWrappedConfig simulates the real-world scenario where
// lambda_config values are wrapped in types.Dynamic (as produced by DynamicAttribute).
// This is the exact scenario that caused the per-lambda env_vars bug.
func TestBuildEnvVars_WithDynamicWrappedConfig(t *testing.T) {
	pm := &ParallelManager{
		config: &DispatcherConfig{
			AppName:     "myapp",
			Environment: "prod",
		},
	}

	// Build shared config as types.Object wrapped in types.Dynamic
	sharedEnvVarsObj := types.ObjectValueMust(
		map[string]attr.Type{
			"JWT_SECRET":           basetypes.StringType{},
			"COGNITO_USER_POOL_ID": basetypes.StringType{},
		},
		map[string]attr.Value{
			"JWT_SECRET":           types.StringValue("secret123"),
			"COGNITO_USER_POOL_ID": types.StringValue("us-east-1_abc"),
		},
	)
	sharedObj := types.ObjectValueMust(
		map[string]attr.Type{
			"env_vars": sharedEnvVarsObj.Type(nil),
		},
		map[string]attr.Value{
			"env_vars": sharedEnvVarsObj,
		},
	)
	sharedDynamic := types.DynamicValue(sharedObj)

	// Build per-lambda config as types.Object wrapped in types.Dynamic
	opsEnvVarsObj := types.ObjectValueMust(
		map[string]attr.Type{
			"CLOUDFRONT_KEY_PAIR_ID": basetypes.StringType{},
			"IMAGES_BUCKET_NAME":    basetypes.StringType{},
		},
		map[string]attr.Value{
			"CLOUDFRONT_KEY_PAIR_ID": types.StringValue("KXYZ123"),
			"IMAGES_BUCKET_NAME":    types.StringValue("my-images-bucket"),
		},
	)
	opsObj := types.ObjectValueMust(
		map[string]attr.Type{
			"env_vars": opsEnvVarsObj.Type(nil),
		},
		map[string]attr.Value{
			"env_vars": opsEnvVarsObj,
		},
	)
	opsDynamic := types.DynamicValue(opsObj)

	// This is what extractLambdaConfig produces for a DynamicAttribute
	lambdaConfig := map[string]interface{}{
		"shared": sharedDynamic,
		"ops":    opsDynamic,
	}

	envVars := pm.buildEnvVars("ops", nil, lambdaConfig)

	// Verify shared env vars are present
	if envVars["JWT_SECRET"] != "secret123" {
		t.Errorf("expected JWT_SECRET='secret123', got '%s'", envVars["JWT_SECRET"])
	}
	if envVars["COGNITO_USER_POOL_ID"] != "us-east-1_abc" {
		t.Errorf("expected COGNITO_USER_POOL_ID='us-east-1_abc', got '%s'", envVars["COGNITO_USER_POOL_ID"])
	}

	// Verify per-lambda env vars are present (this was the bug)
	if envVars["CLOUDFRONT_KEY_PAIR_ID"] != "KXYZ123" {
		t.Errorf("expected CLOUDFRONT_KEY_PAIR_ID='KXYZ123', got '%s'", envVars["CLOUDFRONT_KEY_PAIR_ID"])
	}
	if envVars["IMAGES_BUCKET_NAME"] != "my-images-bucket" {
		t.Errorf("expected IMAGES_BUCKET_NAME='my-images-bucket', got '%s'", envVars["IMAGES_BUCKET_NAME"])
	}
}

// TestResolveEnvVarsForAction_WithDynamicWrappedConfig tests the DispatcherConfig method
// with types.Dynamic wrapped values.
func TestResolveEnvVarsForAction_WithDynamicWrappedConfig(t *testing.T) {
	// Build shared config wrapped in types.Dynamic
	sharedEnvVarsObj := types.ObjectValueMust(
		map[string]attr.Type{
			"SHARED_VAR": basetypes.StringType{},
		},
		map[string]attr.Value{
			"SHARED_VAR": types.StringValue("shared_value"),
		},
	)
	sharedObj := types.ObjectValueMust(
		map[string]attr.Type{
			"env_vars": sharedEnvVarsObj.Type(nil),
		},
		map[string]attr.Value{
			"env_vars": sharedEnvVarsObj,
		},
	)

	// Build per-lambda config wrapped in types.Dynamic
	lambdaEnvVarsObj := types.ObjectValueMust(
		map[string]attr.Type{
			"LAMBDA_VAR": basetypes.StringType{},
			"SHARED_VAR": basetypes.StringType{}, // Override shared
		},
		map[string]attr.Value{
			"LAMBDA_VAR": types.StringValue("lambda_value"),
			"SHARED_VAR": types.StringValue("overridden_by_lambda"),
		},
	)
	lambdaObj := types.ObjectValueMust(
		map[string]attr.Type{
			"env_vars": lambdaEnvVarsObj.Type(nil),
		},
		map[string]attr.Value{
			"env_vars": lambdaEnvVarsObj,
		},
	)

	config := &DispatcherConfig{
		LambdaConfig: map[string]interface{}{
			"shared":    types.DynamicValue(sharedObj),
			"my_lambda": types.DynamicValue(lambdaObj),
		},
	}

	autoInjected := map[string]string{
		"APP_NAME":    "testapp",
		"ENVIRONMENT": "dev",
	}

	result := config.ResolveEnvVarsForAction("my_lambda", autoInjected)

	// Auto-injected vars should be present
	if result["APP_NAME"] != "testapp" {
		t.Errorf("expected APP_NAME='testapp', got '%s'", result["APP_NAME"])
	}

	// Shared var should be overridden by lambda-specific
	if result["SHARED_VAR"] != "overridden_by_lambda" {
		t.Errorf("expected SHARED_VAR='overridden_by_lambda', got '%s'", result["SHARED_VAR"])
	}

	// Lambda-specific var should be present
	if result["LAMBDA_VAR"] != "lambda_value" {
		t.Errorf("expected LAMBDA_VAR='lambda_value', got '%s'", result["LAMBDA_VAR"])
	}
}

// TestExtractMapValue_NullDynamic verifies that null/unknown Dynamic values are handled gracefully.
func TestExtractMapValue_NullDynamic(t *testing.T) {
	nullDynamic := types.DynamicNull()
	_, ok := extractMapValue(nullDynamic)
	if ok {
		t.Error("extractMapValue should return false for null types.Dynamic")
	}

	unknownDynamic := types.DynamicUnknown()
	_, ok = extractMapValue(unknownDynamic)
	if ok {
		t.Error("extractMapValue should return false for unknown types.Dynamic")
	}
}

// TestExtractStringValue_NullDynamic verifies that null/unknown Dynamic values are handled gracefully.
func TestExtractStringValue_NullDynamic(t *testing.T) {
	nullDynamic := types.DynamicNull()
	_, ok := extractStringValue(nullDynamic)
	if ok {
		t.Error("extractStringValue should return false for null types.Dynamic")
	}

	unknownDynamic := types.DynamicUnknown()
	_, ok = extractStringValue(unknownDynamic)
	if ok {
		t.Error("extractStringValue should return false for unknown types.Dynamic")
	}
}
