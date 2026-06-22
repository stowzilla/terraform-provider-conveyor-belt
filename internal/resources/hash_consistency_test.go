package resources

import (
	"testing"

	"terraform-provider-conveyor-belt/internal/utils"
)

// TestConfigHashConsistency_PlanVsApply verifies that calculateAllLambdaConfigHashes
// (used in ModifyPlan) and calculateLambdaConfigHash called with all routes
// (the old Update path) produce identical results. This is the regression test
// for GitHub issue #11: inconsistent lambda_config_hashes between plan and apply.
func TestConfigHashConsistency_PlanVsApply(t *testing.T) {
	routes := []utils.Route{
		{Name: "items_index", Verb: "GET", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito", Tables: []string{"inventory", "containers"}},
		{Name: "items_search", Verb: "GET", Path: "/items/search", Gateway: "customer", Lambda: "customer", Auth: "cognito", Tables: []string{"inventory"}},
		{Name: "health", Verb: "GET", Path: "/health", Gateway: "ops", Lambda: "ops", Auth: "cognito"},
		{Name: "containers_index", Verb: "GET", Path: "/containers", Gateway: "ops", Lambda: "ops", Auth: "cognito", Tables: []string{"containers"}},
		{Name: "signup", Verb: "POST", Path: "/signup", Gateway: "onboarding", Lambda: "onboarding", Auth: "none"},
		{Name: "contact", Verb: "POST", Path: "/contact", Gateway: "onboarding", Lambda: "onboarding", Auth: "none"},
	}

	lambdaConfig := map[string]interface{}{
		"shared": map[string]interface{}{
			"env_vars": map[string]interface{}{
				"LOG_LEVEL": "info",
			},
		},
		"customer": map[string]interface{}{
			"timeout":     int64(60),
			"memory_size": int64(512),
			"env_vars": map[string]interface{}{
				"CACHE_TTL": "300",
			},
		},
		// standalone lambda (no routes)
		"background_worker": map[string]interface{}{
			"timeout":     int64(300),
			"memory_size": int64(1024),
		},
	}

	lambdas := []string{"background_worker", "customer", "onboarding", "ops"}
	readOnlyTables := []string{"config"}
	readWriteTables := []string{"audit_log"}
	sharedIamPolicyArns := []string{"arn:aws:iam::123456789:policy/secrets-access"}

	// Path 1: calculateAllLambdaConfigHashes (ModifyPlan path — pre-filters routes by lambda)
	planHashes, err := calculateAllLambdaConfigHashes(lambdas, routes, lambdaConfig, nil, nil, readOnlyTables, readWriteTables, sharedIamPolicyArns)
	if err != nil {
		t.Fatalf("calculateAllLambdaConfigHashes failed: %v", err)
	}

	// Path 2: calculateLambdaConfigHash with ALL routes (old Update path — no pre-filtering)
	applyHashes := make(map[string]string)
	for _, lambda := range lambdas {
		h, err := calculateLambdaConfigHash(routes, lambdaConfig, lambda, nil, nil, readOnlyTables, readWriteTables, sharedIamPolicyArns)
		if err != nil {
			t.Fatalf("calculateLambdaConfigHash failed for %s: %v", lambda, err)
		}
		applyHashes[lambda] = h
	}

	for _, lambda := range lambdas {
		if planHashes[lambda] != applyHashes[lambda] {
			t.Errorf("Hash mismatch for %s: plan=%s apply=%s", lambda, planHashes[lambda], applyHashes[lambda])
		}
	}
}

// TestLambdaHashConsistency_PlanVsApply verifies that calculateAllLambdaHashes
// (used in ModifyPlan) and calculateLambdaHash called with all routes
// (the old Update path) produce identical results.
func TestLambdaHashConsistency_PlanVsApply(t *testing.T) {
	routes := []utils.Route{
		{Name: "items_index", Verb: "GET", Path: "/items", Gateway: "customer", Lambda: "customer", Auth: "cognito", Tables: []string{"inventory", "containers"}},
		{Name: "health", Verb: "GET", Path: "/health", Gateway: "ops", Lambda: "ops", Auth: "cognito"},
		{Name: "signup", Verb: "POST", Path: "/signup", Gateway: "onboarding", Lambda: "onboarding", Auth: "none"},
	}

	lambdaConfig := map[string]interface{}{
		"shared": map[string]interface{}{
			"env_vars": map[string]interface{}{
				"LOG_LEVEL": "info",
			},
		},
	}

	lambdas := []string{"customer", "onboarding", "ops"}
	sourceDir := "testdata/test_samples/lambda"
	sharedDirs := []string{"models", "lib", "helpers", "templates"}

	// Path 1: calculateAllLambdaHashes (pre-filters routes)
	planHashes, err := calculateAllLambdaHashes(lambdas, routes, lambdaConfig, sourceDir, sharedDirs, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("calculateAllLambdaHashes failed: %v", err)
	}

	// Path 2: calculateLambdaHash with ALL routes (no pre-filtering)
	applyHashes := make(map[string]string)
	for _, lambda := range lambdas {
		h, err := calculateLambdaHash(routes, lambdaConfig, lambda, sourceDir, sharedDirs, nil, nil, nil, nil, nil)
		if err != nil {
			// Skip lambdas without source dirs (expected for test data)
			continue
		}
		applyHashes[lambda] = h
	}

	for _, lambda := range lambdas {
		ph, pOk := planHashes[lambda]
		ah, aOk := applyHashes[lambda]
		if pOk && aOk && ph != ah {
			t.Errorf("Hash mismatch for %s: plan=%s apply=%s", lambda, ph, ah)
		}
	}
}

// TestHashConsistency_UnknownInputsProduceDifferentHashes verifies that computing
// hashes with empty sharedIamPolicyArns (what ModifyPlan did when the value was unknown)
// produces DIFFERENT hashes than computing with actual ARNs (what Update computes).
// This is the root cause of the "inconsistent result after apply" error:
// Plan computes with empty slice, Apply computes with real ARNs → mismatch.
func TestHashConsistency_UnknownInputsProduceDifferentHashes(t *testing.T) {
	routes := []utils.Route{
		{Name: "health", Verb: "GET", Path: "/health", Gateway: "ops", Lambda: "ops", Auth: "cognito"},
	}
	lambdaConfig := map[string]interface{}{}
	lambdas := []string{"ops"}

	// Simulate Plan: sharedIamPolicyArns is unknown → old code used empty slice
	emptyHashes, err := calculateAllLambdaConfigHashes(lambdas, routes, lambdaConfig, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("hash with empty arns failed: %v", err)
	}

	// Simulate Apply: sharedIamPolicyArns is now resolved
	realArns := []string{"arn:aws:iam::123456789:policy/worker-invoke"}
	realHashes, err := calculateAllLambdaConfigHashes(lambdas, routes, lambdaConfig, nil, nil, nil, nil, realArns)
	if err != nil {
		t.Fatalf("hash with real arns failed: %v", err)
	}

	for _, lambda := range lambdas {
		if emptyHashes[lambda] == realHashes[lambda] {
			t.Errorf("Expected different hashes for %s when sharedIamPolicyArns changes from empty to populated, but got same: %s", lambda, emptyHashes[lambda])
		}
	}
}
