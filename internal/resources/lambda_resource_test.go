// internal/resources/lambda_resource_test.go
package resources

import (
	"context"
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// Feature: provider-framework-refactor, Property 1: Resource Independence
// Test that changes to one Lambda resource do not affect another Lambda resource.
// *For any* two `dispatcher_lambda` resources with different names, changes to one
// resource SHALL NOT trigger updates to the other resource.
// **Validates: Requirements 1.2**

// TestResourceIndependence_HashIsolation tests that hash calculations for different
// Lambda resources are independent - changing one Lambda's configuration does not
// affect another Lambda's hash.
func TestResourceIndependence_HashIsolation(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate two different Lambda names
		lambda1Name := randomString(r, 5, 15)
		lambda2Name := randomString(r, 5, 15)

		// Ensure names are different
		for lambda1Name == lambda2Name {
			lambda2Name = randomString(r, 5, 15)
		}

		// Generate random configuration for lambda1
		numTables1 := r.Intn(5)
		tables1 := make([]string, numTables1)
		for i := 0; i < numTables1; i++ {
			tables1[i] = randomString(r, 5, 15)
		}

		// Generate random configuration for lambda2
		numTables2 := r.Intn(5)
		tables2 := make([]string, numTables2)
		for i := 0; i < numTables2; i++ {
			tables2[i] = randomString(r, 5, 15)
		}

		// Create routes for each lambda
		routes1 := []utils.Route{{Lambda: lambda1Name, Tables: tables1}}
		routes2 := []utils.Route{{Lambda: lambda2Name, Tables: tables2}}

		// Calculate initial hashes for both lambdas
		lambdaConfig := make(map[string]interface{})
		layerArns := []string{}
		readOnlyTables := []string{}
		readWriteTables := []string{}

		// Note: We can't use calculateLambdaHash directly as it requires filesystem access
		// Instead, we test calculateLambdaConfigHash which tests the config isolation
		hash1Initial, err1 := calculateLambdaConfigHash(routes1, lambdaConfig, lambda1Name, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hash2Initial, err2 := calculateLambdaConfigHash(routes2, lambdaConfig, lambda2Name, layerArns, nil, readOnlyTables, readWriteTables, nil)

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating initial hashes: err1=%v, err2=%v", err1, err2)
			return false
		}

		// Now modify lambda1's configuration (add more tables)
		additionalTables := r.Intn(3) + 1
		for i := 0; i < additionalTables; i++ {
			tables1 = append(tables1, randomString(r, 5, 15))
		}
		routes1Modified := []utils.Route{{Lambda: lambda1Name, Tables: tables1}}

		// Recalculate hashes
		hash1Modified, err3 := calculateLambdaConfigHash(routes1Modified, lambdaConfig, lambda1Name, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hash2AfterChange, err4 := calculateLambdaConfigHash(routes2, lambdaConfig, lambda2Name, layerArns, nil, readOnlyTables, readWriteTables, nil)

		if err3 != nil || err4 != nil {
			t.Logf("Error calculating modified hashes: err3=%v, err4=%v", err3, err4)
			return false
		}

		// Property 1: Lambda1's hash should change (we modified it)
		if hash1Initial == hash1Modified && additionalTables > 0 {
			t.Logf("Lambda1 hash did not change after modification: initial=%s, modified=%s", hash1Initial, hash1Modified)
			return false
		}

		// Property 2: Lambda2's hash should NOT change (we didn't modify it)
		if hash2Initial != hash2AfterChange {
			t.Logf("Lambda2 hash changed when Lambda1 was modified: initial=%s, after=%s", hash2Initial, hash2AfterChange)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Resource independence property failed: %v", err)
	}
}

// TestResourceIndependence_EnvVarIsolation tests that environment variable changes
// to one Lambda do not affect another Lambda's hash.
func TestResourceIndependence_EnvVarIsolation(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate two different Lambda names
		lambda1Name := randomString(r, 5, 15)
		lambda2Name := randomString(r, 5, 15)

		// Ensure names are different
		for lambda1Name == lambda2Name {
			lambda2Name = randomString(r, 5, 15)
		}

		// Create lambda_config with env vars for lambda1 only
		lambdaConfig := map[string]interface{}{
			lambda1Name: map[string]interface{}{
				"env_vars": map[string]interface{}{
					"VAR1": randomString(r, 5, 20),
				},
			},
		}

		routes1 := []utils.Route{{Lambda: lambda1Name}}
		routes2 := []utils.Route{{Lambda: lambda2Name}}

		layerArns := []string{}
		readOnlyTables := []string{}
		readWriteTables := []string{}

		// Calculate initial hashes
		hash1Initial, err1 := calculateLambdaConfigHash(routes1, lambdaConfig, lambda1Name, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hash2Initial, err2 := calculateLambdaConfigHash(routes2, lambdaConfig, lambda2Name, layerArns, nil, readOnlyTables, readWriteTables, nil)

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating initial hashes: err1=%v, err2=%v", err1, err2)
			return false
		}

		// Modify lambda1's env vars
		lambdaConfig[lambda1Name] = map[string]interface{}{
			"env_vars": map[string]interface{}{
				"VAR1": randomString(r, 5, 20),
				"VAR2": randomString(r, 5, 20), // Add new var
			},
		}

		// Recalculate hashes
		hash1Modified, err3 := calculateLambdaConfigHash(routes1, lambdaConfig, lambda1Name, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hash2AfterChange, err4 := calculateLambdaConfigHash(routes2, lambdaConfig, lambda2Name, layerArns, nil, readOnlyTables, readWriteTables, nil)

		if err3 != nil || err4 != nil {
			t.Logf("Error calculating modified hashes: err3=%v, err4=%v", err3, err4)
			return false
		}

		// Lambda1's hash should change
		if hash1Initial == hash1Modified {
			t.Logf("Lambda1 hash did not change after env var modification")
			return false
		}

		// Lambda2's hash should NOT change
		if hash2Initial != hash2AfterChange {
			t.Logf("Lambda2 hash changed when Lambda1's env vars were modified: initial=%s, after=%s", hash2Initial, hash2AfterChange)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Env var isolation property failed: %v", err)
	}
}

// TestResourceIndependence_LayerIsolation tests that layer changes to one Lambda
// do not affect another Lambda's hash when using resource-level layers.
func TestResourceIndependence_LayerIsolation(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate two different Lambda names
		lambda1Name := randomString(r, 5, 15)
		lambda2Name := randomString(r, 5, 15)

		// Ensure names are different
		for lambda1Name == lambda2Name {
			lambda2Name = randomString(r, 5, 15)
		}

		routes1 := []utils.Route{{Lambda: lambda1Name}}
		routes2 := []utils.Route{{Lambda: lambda2Name}}

		lambdaConfig := make(map[string]interface{})
		readOnlyTables := []string{}
		readWriteTables := []string{}

		// Lambda1 has layers, Lambda2 does not
		lambda1Layers := []string{
			"arn:aws:lambda:us-east-1:123456789012:layer:layer1:1",
		}
		lambda2Layers := []string{}

		// Calculate initial hashes
		hash1Initial, err1 := calculateLambdaConfigHash(routes1, lambdaConfig, lambda1Name, lambda1Layers, nil, readOnlyTables, readWriteTables, nil)
		hash2Initial, err2 := calculateLambdaConfigHash(routes2, lambdaConfig, lambda2Name, lambda2Layers, nil, readOnlyTables, readWriteTables, nil)

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating initial hashes: err1=%v, err2=%v", err1, err2)
			return false
		}

		// Add a layer to lambda1
		lambda1Layers = append(lambda1Layers, "arn:aws:lambda:us-east-1:123456789012:layer:layer2:1")

		// Recalculate hashes
		hash1Modified, err3 := calculateLambdaConfigHash(routes1, lambdaConfig, lambda1Name, lambda1Layers, nil, readOnlyTables, readWriteTables, nil)
		hash2AfterChange, err4 := calculateLambdaConfigHash(routes2, lambdaConfig, lambda2Name, lambda2Layers, nil, readOnlyTables, readWriteTables, nil)

		if err3 != nil || err4 != nil {
			t.Logf("Error calculating modified hashes: err3=%v, err4=%v", err3, err4)
			return false
		}

		// Lambda1's hash should change
		if hash1Initial == hash1Modified {
			t.Logf("Lambda1 hash did not change after layer modification")
			return false
		}

		// Lambda2's hash should NOT change
		if hash2Initial != hash2AfterChange {
			t.Logf("Lambda2 hash changed when Lambda1's layers were modified: initial=%s, after=%s", hash2Initial, hash2AfterChange)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Layer isolation property failed: %v", err)
	}
}

// TestResourceIndependence_ModelConfigHashIsolation tests that the config hash
// calculation for LambdaResourceModel is isolated per resource.
func TestResourceIndependence_ModelConfigHashIsolation(t *testing.T) {
	ctx := context.Background()
	resource := &lambdaResource{}

	// Create two different Lambda resource models
	model1 := &LambdaResourceModel{
		Name:    types.StringValue("lambda1"),
		AppName: types.StringValue("myapp"),
		Timeout: types.Int64Value(30),
		Memory:  types.Int64Value(128),
	}

	model2 := &LambdaResourceModel{
		Name:    types.StringValue("lambda2"),
		AppName: types.StringValue("myapp"),
		Timeout: types.Int64Value(30),
		Memory:  types.Int64Value(128),
	}

	// Calculate initial hashes
	hash1Initial := resource.calculateConfigHashForModel(ctx, model1)
	hash2Initial := resource.calculateConfigHashForModel(ctx, model2)

	// Modify model1's timeout
	model1.Timeout = types.Int64Value(60)

	// Recalculate hashes
	hash1Modified := resource.calculateConfigHashForModel(ctx, model1)
	hash2AfterChange := resource.calculateConfigHashForModel(ctx, model2)

	// Model1's hash should change
	if hash1Initial == hash1Modified {
		t.Errorf("Model1 hash did not change after timeout modification: initial=%s, modified=%s", hash1Initial, hash1Modified)
	}

	// Model2's hash should NOT change
	if hash2Initial != hash2AfterChange {
		t.Errorf("Model2 hash changed when Model1 was modified: initial=%s, after=%s", hash2Initial, hash2AfterChange)
	}
}


// Feature: provider-framework-refactor, Property 11: Configuration Precedence
// *For any* configuration setting S, if S is specified at both provider level and
// resource level, the resource-level value SHALL be used. If only provider level
// is specified, that value SHALL be used.
// **Validates: Requirements 8.3, 8.5**

// TestConfigurationPrecedence_Property tests that configuration precedence is correctly
// applied: resource-level values override provider-level defaults.
func TestConfigurationPrecedence_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random provider defaults
		providerTimeout := int64(r.Intn(300) + 1)   // 1-300 seconds
		providerMemory := int64(r.Intn(3008) + 128) // 128-3136 MB

		// Generate random resource values (different from provider defaults)
		resourceTimeout := int64(r.Intn(300) + 1)
		resourceMemory := int64(r.Intn(3008) + 128)

		// Ensure resource values are different from provider defaults for testing
		for resourceTimeout == providerTimeout {
			resourceTimeout = int64(r.Intn(300) + 1)
		}
		for resourceMemory == providerMemory {
			resourceMemory = int64(r.Intn(3008) + 128)
		}

		// Create a lambdaResource with provider config
		resource := &lambdaResource{
			providerConfig: &DispatcherConfig{
				DefaultLambdaTimeout: providerTimeout,
				DefaultLambdaMemory:  providerMemory,
			},
		}

		// Test Case 1: Resource explicitly sets values - should use resource values
		model1 := &LambdaResourceModel{
			Timeout: types.Int64Value(resourceTimeout),
			Memory:  types.Int64Value(resourceMemory),
		}

		effectiveTimeout1 := resource.getEffectiveTimeout(model1)
		effectiveMemory1 := resource.getEffectiveMemory(model1)

		// When resource explicitly sets values, those should be used
		if effectiveTimeout1 != resourceTimeout {
			t.Logf("Case 1 - Timeout: expected resource value %d, got %d", resourceTimeout, effectiveTimeout1)
			return false
		}
		if effectiveMemory1 != resourceMemory {
			t.Logf("Case 1 - Memory: expected resource value %d, got %d", resourceMemory, effectiveMemory1)
			return false
		}

		// Test Case 2: Resource doesn't set values (null) - should use provider defaults
		model2 := &LambdaResourceModel{
			Timeout: types.Int64Null(), // Not set by user
			Memory:  types.Int64Null(), // Not set by user
		}

		effectiveTimeout2 := resource.getEffectiveTimeout(model2)
		effectiveMemory2 := resource.getEffectiveMemory(model2)

		// When resource doesn't set values, provider defaults should be used
		if effectiveTimeout2 != providerTimeout {
			t.Logf("Case 2 - Timeout: expected provider default %d, got %d", providerTimeout, effectiveTimeout2)
			return false
		}
		if effectiveMemory2 != providerMemory {
			t.Logf("Case 2 - Memory: expected provider default %d, got %d", providerMemory, effectiveMemory2)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Configuration precedence property failed: %v", err)
	}
}

// TestConfigurationPrecedence_NoProviderDefaults tests that when provider defaults
// are not set, hardcoded defaults are used.
func TestConfigurationPrecedence_NoProviderDefaults(t *testing.T) {
	// Create a lambdaResource with no provider defaults (zero values)
	resource := &lambdaResource{
		providerConfig: &DispatcherConfig{
			DefaultLambdaTimeout: 0, // Not set
			DefaultLambdaMemory:  0, // Not set
		},
	}

	// Resource doesn't set values (null)
	model := &LambdaResourceModel{
		Timeout: types.Int64Null(), // Not set by user
		Memory:  types.Int64Null(), // Not set by user
	}

	effectiveTimeout := resource.getEffectiveTimeout(model)
	effectiveMemory := resource.getEffectiveMemory(model)

	// When provider defaults are not set, hardcoded defaults should be used
	if effectiveTimeout != 30 {
		t.Errorf("Expected hardcoded default timeout 30, got %d", effectiveTimeout)
	}
	if effectiveMemory != 128 {
		t.Errorf("Expected hardcoded default memory 128, got %d", effectiveMemory)
	}
}

// TestConfigurationPrecedence_Tags tests that tag precedence is correctly applied:
// resource-level tags override provider-level default tags.
func TestConfigurationPrecedence_Tags(t *testing.T) {
	ctx := context.Background()

	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random provider default tags
		numProviderTags := r.Intn(5) + 1
		providerTags := make(map[string]string)
		for i := 0; i < numProviderTags; i++ {
			key := randomString(r, 3, 10)
			value := randomString(r, 5, 20)
			providerTags[key] = value
		}

		// Generate random resource tags (some overlapping, some new)
		numResourceTags := r.Intn(5) + 1
		resourceTags := make(map[string]string)

		// Add some overlapping keys with different values
		overlapCount := 0
		for key := range providerTags {
			if overlapCount >= numResourceTags/2 {
				break
			}
			resourceTags[key] = randomString(r, 5, 20) // Different value
			overlapCount++
		}

		// Add some new keys
		for i := overlapCount; i < numResourceTags; i++ {
			key := randomString(r, 3, 10)
			value := randomString(r, 5, 20)
			resourceTags[key] = value
		}

		// Create a lambdaResource with provider config
		resource := &lambdaResource{
			providerConfig: &DispatcherConfig{
				DefaultTags: providerTags,
			},
		}

		// Create model with resource tags
		resourceTagsValue, _ := types.MapValueFrom(ctx, types.StringType, resourceTags)
		model := &LambdaResourceModel{
			Tags: resourceTagsValue,
		}

		effectiveTags := resource.getEffectiveTags(ctx, model)

		// Verify: all provider tags should be present (unless overridden)
		for key, providerValue := range providerTags {
			if resourceValue, exists := resourceTags[key]; exists {
				// Resource tag should override provider tag
				if effectiveTags[key] != resourceValue {
					t.Logf("Tag %s: expected resource value %s, got %s", key, resourceValue, effectiveTags[key])
					return false
				}
			} else {
				// Provider tag should be present
				if effectiveTags[key] != providerValue {
					t.Logf("Tag %s: expected provider value %s, got %s", key, providerValue, effectiveTags[key])
					return false
				}
			}
		}

		// Verify: all resource tags should be present
		for key, resourceValue := range resourceTags {
			if effectiveTags[key] != resourceValue {
				t.Logf("Tag %s: expected resource value %s, got %s", key, resourceValue, effectiveTags[key])
				return false
			}
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Tag precedence property failed: %v", err)
	}
}

// TestConfigurationPrecedence_TagsNoResourceTags tests that when resource has no tags,
// provider default tags are used.
func TestConfigurationPrecedence_TagsNoResourceTags(t *testing.T) {
	ctx := context.Background()

	providerTags := map[string]string{
		"Environment": "production",
		"Team":        "platform",
		"Project":     "dispatcher",
	}

	resource := &lambdaResource{
		providerConfig: &DispatcherConfig{
			DefaultTags: providerTags,
		},
	}

	// Model with no tags (null)
	model := &LambdaResourceModel{
		Tags: types.MapNull(types.StringType),
	}

	effectiveTags := resource.getEffectiveTags(ctx, model)

	// All provider tags should be present
	for key, value := range providerTags {
		if effectiveTags[key] != value {
			t.Errorf("Tag %s: expected %s, got %s", key, value, effectiveTags[key])
		}
	}

	// No extra tags should be present
	if len(effectiveTags) != len(providerTags) {
		t.Errorf("Expected %d tags, got %d", len(providerTags), len(effectiveTags))
	}
}

// TestConfigurationPrecedence_TagsNoProviderTags tests that when provider has no default tags,
// only resource tags are used.
func TestConfigurationPrecedence_TagsNoProviderTags(t *testing.T) {
	ctx := context.Background()

	resourceTags := map[string]string{
		"Environment": "staging",
		"Owner":       "dev-team",
	}

	resource := &lambdaResource{
		providerConfig: &DispatcherConfig{
			DefaultTags: nil, // No provider default tags
		},
	}

	resourceTagsValue, _ := types.MapValueFrom(ctx, types.StringType, resourceTags)
	model := &LambdaResourceModel{
		Tags: resourceTagsValue,
	}

	effectiveTags := resource.getEffectiveTags(ctx, model)

	// All resource tags should be present
	for key, value := range resourceTags {
		if effectiveTags[key] != value {
			t.Errorf("Tag %s: expected %s, got %s", key, value, effectiveTags[key])
		}
	}

	// No extra tags should be present
	if len(effectiveTags) != len(resourceTags) {
		t.Errorf("Expected %d tags, got %d", len(resourceTags), len(effectiveTags))
	}
}
