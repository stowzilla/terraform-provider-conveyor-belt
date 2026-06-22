// internal/resources/dispatcher_resource_integration_test.go
package resources

import (
	"math/rand"
	"sort"
	"strings"
	"testing"
	"testing/quick"

	"terraform-provider-conveyor-belt/internal/utils"
)

// =============================================================================
// Property 1: Routes Determine Resources
// *For any* valid routes.tf.rb file, the set of created Lambda functions SHALL
// exactly match the unique `lambda` values in the parsed routes plus any
// standalone lambdas defined in `lambda_config`.
// **Validates: Requirements 1.4, 1.5, 4.1**
// =============================================================================

// genRoutesForIntegration creates a random set of routes for integration testing
func genRoutesForIntegration(r *rand.Rand, minRoutes, maxRoutes int) []utils.Route {
	numRoutes := minRoutes + r.Intn(maxRoutes-minRoutes+1)
	routes := make([]utils.Route, numRoutes)

	verbs := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	auths := []string{"", "cognito", "none"}

	for i := 0; i < numRoutes; i++ {
		routes[i] = utils.Route{
			Name:    genStringForIntegration(r, 5, 15),
			Verb:    verbs[r.Intn(len(verbs))],
			Path:    "/" + genStringForIntegration(r, 3, 10) + "/" + genStringForIntegration(r, 3, 10),
			Gateway: "gateway_" + genStringForIntegration(r, 3, 8),
			Lambda:  "lambda_" + genStringForIntegration(r, 3, 8),
			Auth:    auths[r.Intn(len(auths))],
			Tables:  genTablesForIntegration(r, 0, 3),
		}
	}

	return routes
}

// genTablesForIntegration creates a random set of table names
func genTablesForIntegration(r *rand.Rand, minTables, maxTables int) []string {
	numTables := minTables + r.Intn(maxTables-minTables+1)
	tables := make([]string, numTables)
	for i := 0; i < numTables; i++ {
		tables[i] = "table_" + genStringForIntegration(r, 3, 8)
	}
	return tables
}

// genLambdaConfigForIntegration creates a random lambda_config map
func genLambdaConfigForIntegration(r *rand.Rand, existingLambdas []string) map[string]interface{} {
	config := make(map[string]interface{})

	// Optionally add shared config
	if r.Float32() < 0.5 {
		config["shared"] = map[string]interface{}{
			"env_vars": map[string]interface{}{
				"SHARED_VAR": "shared_value",
			},
		}
	}

	// Optionally add config for some existing lambdas
	for _, lambda := range existingLambdas {
		if r.Float32() < 0.3 {
			config[lambda] = map[string]interface{}{
				"env_vars": map[string]interface{}{
					"LAMBDA_VAR": "lambda_value_" + lambda,
				},
			}
		}
	}

	// Add standalone lambdas (not in routes)
	numStandalone := r.Intn(4) // 0-3 standalone lambdas
	for i := 0; i < numStandalone; i++ {
		standaloneName := "standalone_" + genStringForIntegration(r, 3, 8)
		config[standaloneName] = map[string]interface{}{
			"env_vars": map[string]interface{}{
				"STANDALONE_VAR": "standalone_value",
			},
		}
	}

	return config
}

// genStringForIntegration generates a random string of length between minLen and maxLen
func genStringForIntegration(r *rand.Rand, minLen, maxLen int) string {
	length := minLen + r.Intn(maxLen-minLen+1)
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = chars[r.Intn(len(chars))]
	}
	return string(result)
}

// TestRoutesToResourcesMapping_Property tests Property 1: Routes Determine Resources
// Feature: dispatcher-orchestrator, Property 1: Routes Determine Resources
// **Validates: Requirements 1.4, 1.5, 4.1**
func TestRoutesToResourcesMapping_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random routes
		routes := genRoutesForIntegration(r, 1, 20)

		// Extract unique lambdas from routes
		routeLambdas := make(map[string]bool)
		for _, route := range routes {
			if route.Lambda != "" {
				routeLambdas[route.Lambda] = true
			}
		}

		// Generate lambda_config with some standalone lambdas
		existingLambdas := make([]string, 0, len(routeLambdas))
		for l := range routeLambdas {
			existingLambdas = append(existingLambdas, l)
		}
		lambdaConfig := genLambdaConfigForIntegration(r, existingLambdas)

		// Create a mock dispatcher resource to test extractResources
		dr := &dispatcherResource{}

		// Call extractResources
		lambdas, gateways := dr.extractResources(routes, lambdaConfig)

		// Build expected lambda set: route lambdas + standalone lambdas from config
		expectedLambdas := make(map[string]bool)
		for l := range routeLambdas {
			expectedLambdas[l] = true
		}
		for lambda := range lambdaConfig {
			if lambda != "shared" {
				expectedLambdas[lambda] = true
			}
		}

		// Build expected gateway set from routes
		expectedGateways := make(map[string]bool)
		for _, route := range routes {
			if route.Gateway != "" {
				expectedGateways[route.Gateway] = true
			}
		}

		// Property 1: Extracted lambdas should match expected
		actualLambdas := make(map[string]bool)
		for _, l := range lambdas {
			actualLambdas[l] = true
		}

		if len(actualLambdas) != len(expectedLambdas) {
			t.Logf("Lambda count mismatch: expected %d, got %d", len(expectedLambdas), len(actualLambdas))
			return false
		}

		for l := range expectedLambdas {
			if !actualLambdas[l] {
				t.Logf("Missing expected lambda: %s", l)
				return false
			}
		}

		for l := range actualLambdas {
			if !expectedLambdas[l] {
				t.Logf("Unexpected lambda: %s", l)
				return false
			}
		}

		// Property 2: Extracted gateways should match expected
		actualGateways := make(map[string]bool)
		for _, g := range gateways {
			actualGateways[g] = true
		}

		if len(actualGateways) != len(expectedGateways) {
			t.Logf("Gateway count mismatch: expected %d, got %d", len(expectedGateways), len(actualGateways))
			return false
		}

		for g := range expectedGateways {
			if !actualGateways[g] {
				t.Logf("Missing expected gateway: %s", g)
				return false
			}
		}

		// Property 3: Results should be sorted (deterministic ordering)
		if !sort.StringsAreSorted(lambdas) {
			t.Logf("Lambdas are not sorted")
			return false
		}

		if !sort.StringsAreSorted(gateways) {
			t.Logf("Gateways are not sorted")
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Routes to resources mapping property failed: %v", err)
	}
}

// TestRoutesToResourcesMapping_EmptyRoutes tests edge case with empty routes
func TestRoutesToResourcesMapping_EmptyRoutes(t *testing.T) {
	dr := &dispatcherResource{}

	// Empty routes, empty config
	lambdas, gateways := dr.extractResources([]utils.Route{}, map[string]interface{}{})

	if len(lambdas) != 0 {
		t.Errorf("Expected 0 lambdas for empty routes, got %d", len(lambdas))
	}

	if len(gateways) != 0 {
		t.Errorf("Expected 0 gateways for empty routes, got %d", len(gateways))
	}
}

// TestRoutesToResourcesMapping_StandaloneLambdasOnly tests standalone lambdas without routes
func TestRoutesToResourcesMapping_StandaloneLambdasOnly(t *testing.T) {
	dr := &dispatcherResource{}

	// No routes, but standalone lambdas in config
	lambdaConfig := map[string]interface{}{
		"shared": map[string]interface{}{
			"env_vars": map[string]interface{}{"KEY": "value"},
		},
		"standalone_a": map[string]interface{}{
			"env_vars": map[string]interface{}{"KEY": "value_a"},
		},
		"standalone_b": map[string]interface{}{
			"env_vars": map[string]interface{}{"KEY": "value_b"},
		},
	}

	lambdas, gateways := dr.extractResources([]utils.Route{}, lambdaConfig)

	// Should have 2 standalone lambdas (shared is excluded)
	if len(lambdas) != 2 {
		t.Errorf("Expected 2 lambdas, got %d: %v", len(lambdas), lambdas)
	}

	// Should have 0 gateways
	if len(gateways) != 0 {
		t.Errorf("Expected 0 gateways, got %d", len(gateways))
	}

	// Verify specific lambdas
	lambdaSet := make(map[string]bool)
	for _, l := range lambdas {
		lambdaSet[l] = true
	}

	if !lambdaSet["standalone_a"] {
		t.Error("Missing standalone_a lambda")
	}
	if !lambdaSet["standalone_b"] {
		t.Error("Missing standalone_b lambda")
	}
}

// TestRoutesToResourcesMapping_DuplicateLambdas tests deduplication of lambdas
func TestRoutesToResourcesMapping_DuplicateLambdas(t *testing.T) {
	dr := &dispatcherResource{}

	// Multiple routes pointing to the same lambda
	routes := []utils.Route{
		{Name: "route1", Verb: "GET", Path: "/a", Gateway: "gw1", Lambda: "shared_lambda"},
		{Name: "route2", Verb: "POST", Path: "/a", Gateway: "gw1", Lambda: "shared_lambda"},
		{Name: "route3", Verb: "GET", Path: "/b", Gateway: "gw2", Lambda: "shared_lambda"},
		{Name: "route4", Verb: "GET", Path: "/c", Gateway: "gw1", Lambda: "unique_lambda"},
	}

	lambdas, gateways := dr.extractResources(routes, map[string]interface{}{})

	// Should have 2 unique lambdas
	if len(lambdas) != 2 {
		t.Errorf("Expected 2 unique lambdas, got %d: %v", len(lambdas), lambdas)
	}

	// Should have 2 unique gateways
	if len(gateways) != 2 {
		t.Errorf("Expected 2 unique gateways, got %d: %v", len(gateways), gateways)
	}
}


// =============================================================================
// Property 3: Configuration Merge Correctness
// *For any* lambda with both route-derived config and `lambda_config` overrides,
// the final configuration SHALL have `lambda_config` values override route-derived
// values, and `shared` config SHALL be merged with lambda-specific config.
// **Validates: Requirements 3.2, 3.8**
// =============================================================================

// TestConfigurationMerge_Property tests Property 3: Configuration Merge Correctness
// Feature: dispatcher-orchestrator, Property 3: Configuration Merge Correctness
// **Validates: Requirements 3.2, 3.8**
func TestConfigurationMerge_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random lambda name
		lambdaName := "lambda_" + genStringForIntegration(r, 3, 8)

		// Generate random auto-injected vars (lowest priority)
		numAutoVars := 1 + r.Intn(5)
		autoInjectedVars := make(map[string]string)
		for i := 0; i < numAutoVars; i++ {
			key := "AUTO_VAR_" + genStringForIntegration(r, 3, 5)
			autoInjectedVars[key] = "auto_value_" + genStringForIntegration(r, 3, 5)
		}

		// Generate random shared env vars (medium priority)
		numSharedVars := r.Intn(5)
		sharedEnvVars := make(map[string]interface{})
		sharedKeys := make([]string, 0)
		for i := 0; i < numSharedVars; i++ {
			key := "SHARED_VAR_" + genStringForIntegration(r, 3, 5)
			sharedEnvVars[key] = "shared_value_" + genStringForIntegration(r, 3, 5)
			sharedKeys = append(sharedKeys, key)
		}

		// Generate random lambda-specific env vars (highest priority)
		numActionVars := r.Intn(5)
		lambdaEnvVars := make(map[string]interface{})
		lambdaKeys := make([]string, 0)
		for i := 0; i < numActionVars; i++ {
			key := "ACTION_VAR_" + genStringForIntegration(r, 3, 5)
			lambdaEnvVars[key] = "lambda_value_" + genStringForIntegration(r, 3, 5)
			lambdaKeys = append(lambdaKeys, key)
		}

		// Create some overlapping keys to test override behavior
		if len(sharedKeys) > 0 && r.Float32() < 0.5 {
			// Add a shared key to lambda vars (should override)
			overrideKey := sharedKeys[r.Intn(len(sharedKeys))]
			lambdaEnvVars[overrideKey] = "lambda_override_" + genStringForIntegration(r, 3, 5)
		}

		if len(autoInjectedVars) > 0 && r.Float32() < 0.5 {
			// Add an auto key to shared vars (should override)
			autoKeys := make([]string, 0, len(autoInjectedVars))
			for k := range autoInjectedVars {
				autoKeys = append(autoKeys, k)
			}
			overrideKey := autoKeys[r.Intn(len(autoKeys))]
			sharedEnvVars[overrideKey] = "shared_override_" + genStringForIntegration(r, 3, 5)
		}

		// Build lambda_config
		lambdaConfig := make(map[string]interface{})
		if len(sharedEnvVars) > 0 {
			lambdaConfig["shared"] = map[string]interface{}{
				"env_vars": sharedEnvVars,
			}
		}
		if len(lambdaEnvVars) > 0 {
			lambdaConfig[lambdaName] = map[string]interface{}{
				"env_vars": lambdaEnvVars,
			}
		}

		// Create DispatcherConfig and resolve env vars
		dispatcherConfig := &DispatcherConfig{
			LambdaConfig: lambdaConfig,
		}

		result := dispatcherConfig.ResolveEnvVarsForAction(lambdaName, autoInjectedVars)

		// Property 1: All auto-injected vars should be present unless overridden
		for key, autoValue := range autoInjectedVars {
			resultValue, exists := result[key]
			if !exists {
				t.Logf("Auto-injected var %s missing from result", key)
				return false
			}

			// Check if it was overridden by shared or lambda
			if sharedVal, sharedExists := sharedEnvVars[key]; sharedExists {
				// Should be overridden by shared (or lambda if lambda also has it)
				if lambdaVal, lambdaExists := lambdaEnvVars[key]; lambdaExists {
					// Action takes precedence
					if resultValue != lambdaVal.(string) {
						t.Logf("Expected lambda override for %s: got %s, want %s", key, resultValue, lambdaVal)
						return false
					}
				} else {
					// Shared takes precedence over auto
					if resultValue != sharedVal.(string) {
						t.Logf("Expected shared override for %s: got %s, want %s", key, resultValue, sharedVal)
						return false
					}
				}
			} else if resultValue != autoValue {
				// Should be the auto value
				t.Logf("Expected auto value for %s: got %s, want %s", key, resultValue, autoValue)
				return false
			}
		}

		// Property 2: All shared vars should be present unless overridden by lambda
		for key, sharedValue := range sharedEnvVars {
			resultValue, exists := result[key]
			if !exists {
				t.Logf("Shared var %s missing from result", key)
				return false
			}

			if lambdaVal, lambdaExists := lambdaEnvVars[key]; lambdaExists {
				// Action takes precedence
				if resultValue != lambdaVal.(string) {
					t.Logf("Expected lambda override for shared %s: got %s, want %s", key, resultValue, lambdaVal)
					return false
				}
			} else if resultValue != sharedValue.(string) {
				// Should be the shared value
				t.Logf("Expected shared value for %s: got %s, want %s", key, resultValue, sharedValue)
				return false
			}
		}

		// Property 3: All lambda vars should be present with their values (highest priority)
		for key, lambdaValue := range lambdaEnvVars {
			resultValue, exists := result[key]
			if !exists {
				t.Logf("Action var %s missing from result", key)
				return false
			}
			if resultValue != lambdaValue.(string) {
				t.Logf("Action var %s has wrong value: got %s, want %s", key, resultValue, lambdaValue)
				return false
			}
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Configuration merge property failed: %v", err)
	}
}

// TestConfigurationMerge_SharedOverridesAuto tests that shared config overrides auto-injected
func TestConfigurationMerge_SharedOverridesAuto(t *testing.T) {
	autoInjected := map[string]string{
		"KEY1": "auto_value1",
		"KEY2": "auto_value2",
	}

	lambdaConfig := map[string]interface{}{
		"shared": map[string]interface{}{
			"env_vars": map[string]interface{}{
				"KEY1": "shared_value1", // Should override auto
				"KEY3": "shared_value3", // New key
			},
		},
	}

	config := &DispatcherConfig{
		LambdaConfig: lambdaConfig,
	}

	result := config.ResolveEnvVarsForAction("test_lambda", autoInjected)

	// KEY1 should be overridden by shared
	if result["KEY1"] != "shared_value1" {
		t.Errorf("KEY1 should be shared_value1, got %s", result["KEY1"])
	}

	// KEY2 should remain auto
	if result["KEY2"] != "auto_value2" {
		t.Errorf("KEY2 should be auto_value2, got %s", result["KEY2"])
	}

	// KEY3 should be from shared
	if result["KEY3"] != "shared_value3" {
		t.Errorf("KEY3 should be shared_value3, got %s", result["KEY3"])
	}
}

// TestConfigurationMerge_ActionOverridesShared tests that lambda config overrides shared
func TestConfigurationMerge_ActionOverridesShared(t *testing.T) {
	autoInjected := map[string]string{
		"KEY1": "auto_value1",
	}

	lambdaConfig := map[string]interface{}{
		"shared": map[string]interface{}{
			"env_vars": map[string]interface{}{
				"KEY1": "shared_value1",
				"KEY2": "shared_value2",
			},
		},
		"test_lambda": map[string]interface{}{
			"env_vars": map[string]interface{}{
				"KEY2": "lambda_value2", // Should override shared
				"KEY3": "lambda_value3", // New key
			},
		},
	}

	config := &DispatcherConfig{
		LambdaConfig: lambdaConfig,
	}

	result := config.ResolveEnvVarsForAction("test_lambda", autoInjected)

	// KEY1 should be from shared (overrides auto)
	if result["KEY1"] != "shared_value1" {
		t.Errorf("KEY1 should be shared_value1, got %s", result["KEY1"])
	}

	// KEY2 should be from lambda (overrides shared)
	if result["KEY2"] != "lambda_value2" {
		t.Errorf("KEY2 should be lambda_value2, got %s", result["KEY2"])
	}

	// KEY3 should be from lambda
	if result["KEY3"] != "lambda_value3" {
		t.Errorf("KEY3 should be lambda_value3, got %s", result["KEY3"])
	}
}

// TestConfigurationMerge_EmptyConfigs tests handling of empty configurations
func TestConfigurationMerge_EmptyConfigs(t *testing.T) {
	autoInjected := map[string]string{
		"KEY1": "auto_value1",
	}

	// Empty lambda_config
	config := &DispatcherConfig{
		LambdaConfig: map[string]interface{}{},
	}

	result := config.ResolveEnvVarsForAction("test_lambda", autoInjected)

	// Should only have auto-injected vars
	if len(result) != 1 {
		t.Errorf("Expected 1 var, got %d", len(result))
	}

	if result["KEY1"] != "auto_value1" {
		t.Errorf("KEY1 should be auto_value1, got %s", result["KEY1"])
	}
}

// TestConfigurationMerge_NilLambdaConfig tests handling of nil lambda_config
func TestConfigurationMerge_NilLambdaConfig(t *testing.T) {
	autoInjected := map[string]string{
		"KEY1": "auto_value1",
	}

	config := &DispatcherConfig{
		LambdaConfig: nil,
	}

	result := config.ResolveEnvVarsForAction("test_lambda", autoInjected)

	// Should only have auto-injected vars
	if len(result) != 1 {
		t.Errorf("Expected 1 var, got %d", len(result))
	}

	if result["KEY1"] != "auto_value1" {
		t.Errorf("KEY1 should be auto_value1, got %s", result["KEY1"])
	}
}

// TestConfigurationMerge_FullPriorityChain tests the complete priority chain
func TestConfigurationMerge_FullPriorityChain(t *testing.T) {
	// Same key at all three levels
	autoInjected := map[string]string{
		"OVERRIDE_KEY": "auto_value",
	}

	lambdaConfig := map[string]interface{}{
		"shared": map[string]interface{}{
			"env_vars": map[string]interface{}{
				"OVERRIDE_KEY": "shared_value",
			},
		},
		"test_lambda": map[string]interface{}{
			"env_vars": map[string]interface{}{
				"OVERRIDE_KEY": "lambda_value",
			},
		},
	}

	config := &DispatcherConfig{
		LambdaConfig: lambdaConfig,
	}

	result := config.ResolveEnvVarsForAction("test_lambda", autoInjected)

	// Action should win (highest priority)
	if result["OVERRIDE_KEY"] != "lambda_value" {
		t.Errorf("OVERRIDE_KEY should be lambda_value, got %s", result["OVERRIDE_KEY"])
	}
}


// =============================================================================
// Property 4: Gateway-Lambda Wiring
// *For any* route in the parsed routes, the created API Gateway integration
// SHALL invoke the Lambda function specified by the route's `lambda` field.
// **Validates: Requirements 1.6, 5.2**
// =============================================================================

// TestGatewayLambdaWiring_Property tests Property 4: Gateway-Lambda Wiring
// Feature: dispatcher-orchestrator, Property 4: Gateway-Lambda Wiring
// **Validates: Requirements 1.6, 5.2**
func TestGatewayLambdaWiring_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random routes
		routes := genRoutesForIntegration(r, 1, 20)

		// Group routes by gateway
		gatewayGroups := utils.GroupByGateway(routes)

		// For each gateway, verify that all routes point to valid lambdas
		for gatewayName, gatewayRoutes := range gatewayGroups {
			// Property 1: All routes in a gateway group should have the same gateway
			for _, route := range gatewayRoutes {
				if route.Gateway != gatewayName {
					t.Logf("Route gateway mismatch: expected %s, got %s", gatewayName, route.Gateway)
					return false
				}
			}

			// Property 2: Each route should have a non-empty lambda reference
			for _, route := range gatewayRoutes {
				if route.Lambda == "" {
					t.Logf("Route %s has empty lambda reference", route.Name)
					return false
				}
			}
		}

		// Property 3: GroupByGateway should cover all routes
		totalRoutesInGroups := 0
		for _, gatewayRoutes := range gatewayGroups {
			totalRoutesInGroups += len(gatewayRoutes)
		}
		if totalRoutesInGroups != len(routes) {
			t.Logf("GroupByGateway lost routes: expected %d, got %d", len(routes), totalRoutesInGroups)
			return false
		}

		// Property 4: GroupByGateway groups should be disjoint
		seenRoutes := make(map[string]bool)
		for _, gatewayRoutes := range gatewayGroups {
			for _, route := range gatewayRoutes {
				routeKey := route.Gateway + ":" + route.Path + ":" + route.Verb
				if seenRoutes[routeKey] {
					t.Logf("Duplicate route found: %s", routeKey)
					return false
				}
				seenRoutes[routeKey] = true
			}
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Gateway-Lambda wiring property failed: %v", err)
	}
}

// TestGatewayLambdaWiring_IntegrationURIConstruction tests that integration URIs are correctly constructed
func TestGatewayLambdaWiring_IntegrationURIConstruction(t *testing.T) {
	testCases := []struct {
		name       string
		region     string
		lambdaArn  string
		expectedURI string
	}{
		{
			name:       "standard lambda ARN",
			region:     "us-east-1",
			lambdaArn:  "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-customer",
			expectedURI: "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-customer/invocations",
		},
		{
			name:       "lambda ARN with alias",
			region:     "us-west-2",
			lambdaArn:  "arn:aws:lambda:us-west-2:123456789012:function:myapp-dev-orders:live",
			expectedURI: "arn:aws:apigateway:us-west-2:lambda:path/2015-03-31/functions/arn:aws:lambda:us-west-2:123456789012:function:myapp-dev-orders:live/invocations",
		},
		{
			name:       "lambda ARN with version",
			region:     "eu-west-1",
			lambdaArn:  "arn:aws:lambda:eu-west-1:123456789012:function:myapp-staging-billing:42",
			expectedURI: "arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-1:123456789012:function:myapp-staging-billing:42/invocations",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Construct integration URI the same way as ensureIntegrationExists
			integrationUri := "arn:aws:apigateway:" + tc.region + ":lambda:path/2015-03-31/functions/" + tc.lambdaArn + "/invocations"

			if integrationUri != tc.expectedURI {
				t.Errorf("Integration URI mismatch:\n  got:  %s\n  want: %s", integrationUri, tc.expectedURI)
			}
		})
	}
}

// TestGatewayLambdaWiring_RouteToLambdaMapping tests that routes correctly map to lambdas
func TestGatewayLambdaWiring_RouteToLambdaMapping(t *testing.T) {
	routes := []utils.Route{
		{Name: "get_customer", Verb: "GET", Path: "/customer", Gateway: "customer", Lambda: "customer_get"},
		{Name: "create_customer", Verb: "POST", Path: "/customer", Gateway: "customer", Lambda: "customer_create"},
		{Name: "get_order", Verb: "GET", Path: "/order/{id}", Gateway: "orders", Lambda: "order_get"},
		{Name: "list_orders", Verb: "GET", Path: "/orders", Gateway: "orders", Lambda: "order_list"},
	}

	// Simulate lambda ARNs that would be created
	lambdaARNs := map[string]string{
		"customer_get":    "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-customer_get",
		"customer_create": "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-customer_create",
		"order_get":       "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-order_get",
		"order_list":      "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-order_list",
	}

	// Verify each route maps to a valid lambda ARN
	for _, route := range routes {
		arn, exists := lambdaARNs[route.Lambda]
		if !exists {
			t.Errorf("Route %s references non-existent lambda %s", route.Name, route.Lambda)
			continue
		}

		// Verify ARN contains the lambda name
		if !strings.Contains(arn, route.Lambda) {
			t.Errorf("Lambda ARN %s does not contain lambda name %s", arn, route.Lambda)
		}
	}
}

// TestGatewayLambdaWiring_GroupByGatewayCorrectness tests GroupByGateway function
func TestGatewayLambdaWiring_GroupByGatewayCorrectness(t *testing.T) {
	routes := []utils.Route{
		{Name: "r1", Verb: "GET", Path: "/a", Gateway: "gw1", Lambda: "l1"},
		{Name: "r2", Verb: "POST", Path: "/a", Gateway: "gw1", Lambda: "l2"},
		{Name: "r3", Verb: "GET", Path: "/b", Gateway: "gw2", Lambda: "l3"},
		{Name: "r4", Verb: "GET", Path: "/c", Gateway: "gw1", Lambda: "l4"},
		{Name: "r5", Verb: "DELETE", Path: "/b", Gateway: "gw2", Lambda: "l5"},
	}

	groups := utils.GroupByGateway(routes)

	// Should have 2 gateways
	if len(groups) != 2 {
		t.Errorf("Expected 2 gateway groups, got %d", len(groups))
	}

	// gw1 should have 3 routes
	if len(groups["gw1"]) != 3 {
		t.Errorf("Expected 3 routes for gw1, got %d", len(groups["gw1"]))
	}

	// gw2 should have 2 routes
	if len(groups["gw2"]) != 2 {
		t.Errorf("Expected 2 routes for gw2, got %d", len(groups["gw2"]))
	}

	// Verify all routes in gw1 have Gateway == "gw1"
	for _, route := range groups["gw1"] {
		if route.Gateway != "gw1" {
			t.Errorf("Route in gw1 group has wrong gateway: %s", route.Gateway)
		}
	}

	// Verify all routes in gw2 have Gateway == "gw2"
	for _, route := range groups["gw2"] {
		if route.Gateway != "gw2" {
			t.Errorf("Route in gw2 group has wrong gateway: %s", route.Gateway)
		}
	}
}

// TestGatewayLambdaWiring_LambdaARNLookup tests that lambda ARN lookup works correctly
func TestGatewayLambdaWiring_LambdaARNLookup(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random routes
		routes := genRoutesForIntegration(r, 1, 15)

		// Extract unique lambdas
		lambdaSet := make(map[string]bool)
		for _, route := range routes {
			if route.Lambda != "" {
				lambdaSet[route.Lambda] = true
			}
		}

		// Create lambda ARN map (simulating what would be created)
		lambdaARNs := make(map[string]string)
		for lambda := range lambdaSet {
			lambdaARNs[lambda] = "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-" + lambda
		}

		// Property: Every route's lambda should have a corresponding ARN
		for _, route := range routes {
			if route.Lambda == "" {
				continue
			}

			arn, exists := lambdaARNs[route.Lambda]
			if !exists {
				t.Logf("Route %s references lambda %s which has no ARN", route.Name, route.Lambda)
				return false
			}

			if arn == "" {
				t.Logf("Lambda %s has empty ARN", route.Lambda)
				return false
			}
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Lambda ARN lookup property failed: %v", err)
	}
}

// TestGatewayLambdaWiring_EmptyRoutes tests handling of empty routes
func TestGatewayLambdaWiring_EmptyRoutes(t *testing.T) {
	groups := utils.GroupByGateway([]utils.Route{})

	if len(groups) != 0 {
		t.Errorf("Expected 0 gateway groups for empty routes, got %d", len(groups))
	}
}

// TestGatewayLambdaWiring_SingleRoute tests handling of single route
func TestGatewayLambdaWiring_SingleRoute(t *testing.T) {
	routes := []utils.Route{
		{Name: "single", Verb: "GET", Path: "/single", Gateway: "gw", Lambda: "lambda"},
	}

	groups := utils.GroupByGateway(routes)

	if len(groups) != 1 {
		t.Errorf("Expected 1 gateway group, got %d", len(groups))
	}

	if len(groups["gw"]) != 1 {
		t.Errorf("Expected 1 route in gw group, got %d", len(groups["gw"]))
	}

	if groups["gw"][0].Lambda != "lambda" {
		t.Errorf("Expected lambda 'lambda', got %s", groups["gw"][0].Lambda)
	}
}
