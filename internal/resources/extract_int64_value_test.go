package resources

import (
	"math/big"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

func TestExtractInt64Value_TypesNumber(t *testing.T) {
	// types.Number is what DynamicAttribute produces for numeric values
	// This is the exact type that comes through extractMapValue -> lambdaConfig["timeout"]
	num := types.NumberValue(big.NewFloat(120))

	val, ok := extractInt64Value(num)
	if !ok {
		t.Fatal("extractInt64Value returned false for types.Number")
	}
	if val != 120 {
		t.Fatalf("expected 120, got %d", val)
	}
}

func TestExtractInt64Value_TypesInt64(t *testing.T) {
	num := types.Int64Value(512)

	val, ok := extractInt64Value(num)
	if !ok {
		t.Fatal("extractInt64Value returned false for types.Int64")
	}
	if val != 512 {
		t.Fatalf("expected 512, got %d", val)
	}
}

func TestExtractInt64Value_NullNumber(t *testing.T) {
	num := types.NumberNull()

	_, ok := extractInt64Value(num)
	if ok {
		t.Fatal("extractInt64Value should return false for null types.Number")
	}
}

func TestExtractInt64Value_NullInt64(t *testing.T) {
	num := types.Int64Null()

	_, ok := extractInt64Value(num)
	if ok {
		t.Fatal("extractInt64Value should return false for null types.Int64")
	}
}

func TestGetTimeoutAndMemory_WithTerraformTypes(t *testing.T) {
	// Simulate what extractMapValue returns when processing lambda_config
	// from a DynamicAttribute: nested types.Object with types.Number values
	pm := &ParallelManager{
		config: &DispatcherConfig{
			DefaultLambdaTimeout: 30,
			DefaultLambdaMemory:  128,
		},
	}

	// Build a lambdaConfig map that mirrors what extractLambdaConfig + extractMapValue produce
	// The inner config for "ai_analysis_worker" is a types.Object with Number attributes
	innerAttrs := map[string]attr.Value{
		"timeout":     types.NumberValue(big.NewFloat(120)),
		"memory_size": types.NumberValue(big.NewFloat(512)),
	}
	innerAttrTypes := map[string]attr.Type{
		"timeout":     basetypes.NumberType{},
		"memory_size": basetypes.NumberType{},
	}
	innerObj, diags := types.ObjectValue(innerAttrTypes, innerAttrs)
	if diags.HasError() {
		t.Fatalf("failed to create inner object: %v", diags)
	}

	lambdaConfig := map[string]interface{}{
		"ai_analysis_worker": innerObj,
	}

	timeout, memory := pm.getTimeoutAndMemory("ai_analysis_worker", lambdaConfig)

	if timeout != 120 {
		t.Errorf("expected timeout=120, got %d", timeout)
	}
	if memory != 512 {
		t.Errorf("expected memory=512, got %d", memory)
	}
}

func TestGetTimeoutAndMemory_SharedConfigWithTerraformTypes(t *testing.T) {
	pm := &ParallelManager{
		config: &DispatcherConfig{
			DefaultLambdaTimeout: 30,
			DefaultLambdaMemory:  128,
		},
	}

	// Shared config sets baseline
	sharedAttrs := map[string]attr.Value{
		"timeout":     types.NumberValue(big.NewFloat(60)),
		"memory_size": types.NumberValue(big.NewFloat(256)),
	}
	sharedAttrTypes := map[string]attr.Type{
		"timeout":     basetypes.NumberType{},
		"memory_size": basetypes.NumberType{},
	}
	sharedObj, _ := types.ObjectValue(sharedAttrTypes, sharedAttrs)

	// Lambda-specific config overrides only timeout
	lambdaAttrs := map[string]attr.Value{
		"timeout": types.NumberValue(big.NewFloat(120)),
	}
	lambdaAttrTypes := map[string]attr.Type{
		"timeout": basetypes.NumberType{},
	}
	lambdaObj, _ := types.ObjectValue(lambdaAttrTypes, lambdaAttrs)

	lambdaConfig := map[string]interface{}{
		"shared":              sharedObj,
		"ai_analysis_worker":  lambdaObj,
	}

	timeout, memory := pm.getTimeoutAndMemory("ai_analysis_worker", lambdaConfig)

	if timeout != 120 {
		t.Errorf("expected timeout=120 (lambda-specific override), got %d", timeout)
	}
	if memory != 256 {
		t.Errorf("expected memory=256 (from shared), got %d", memory)
	}
}

func TestGetTimeoutAndMemory_DefaultsWhenNoConfig(t *testing.T) {
	pm := &ParallelManager{
		config: &DispatcherConfig{
			DefaultLambdaTimeout: 30,
			DefaultLambdaMemory:  128,
		},
	}

	lambdaConfig := map[string]interface{}{}

	timeout, memory := pm.getTimeoutAndMemory("some_lambda", lambdaConfig)

	if timeout != 30 {
		t.Errorf("expected timeout=30 (provider default), got %d", timeout)
	}
	if memory != 128 {
		t.Errorf("expected memory=128 (provider default), got %d", memory)
	}
}
