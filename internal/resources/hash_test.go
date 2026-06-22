// internal/resources/hash_test.go
package resources

import (
	"math/rand"
	"os"
	"reflect"
	"sort"
	"testing"
	"testing/quick"

	"terraform-provider-conveyor-belt/internal/utils"
)

// Feature: provider-framework-refactor, Property: Hash Determinism
// Test that same inputs always produce same hash
// **Validates: Requirements 10.4**

// generateRandomRoute creates a random Route for property testing
func generateRandomRoute(r *rand.Rand) utils.Route {
	verbs := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	auths := []string{"none", "cognito"}

	// Generate random tables
	numTables := r.Intn(4)
	tables := make([]string, numTables)
	for i := 0; i < numTables; i++ {
		tables[i] = randomString(r, 5, 15)
	}

	return utils.Route{
		Name:    randomString(r, 3, 20),
		Verb:    verbs[r.Intn(len(verbs))],
		Path:    "/" + randomString(r, 3, 10) + "/" + randomString(r, 3, 10),
		Gateway: randomString(r, 3, 15),
		Lambda:  randomString(r, 3, 15),
		Auth:    auths[r.Intn(len(auths))],
		Tables:  tables,
	}
}

// randomString generates a random alphanumeric string
func randomString(r *rand.Rand, minLen, maxLen int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	length := minLen + r.Intn(maxLen-minLen+1)
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[r.Intn(len(charset))]
	}
	return string(result)
}

// generateRandomRoutes creates a slice of random routes
func generateRandomRoutes(r *rand.Rand, count int) []utils.Route {
	routes := make([]utils.Route, count)
	for i := 0; i < count; i++ {
		routes[i] = generateRandomRoute(r)
	}
	return routes
}

// TestHashDeterminism_ConfigHash tests that calculateConfigHash produces
// the same hash for the same inputs, regardless of call order.
// Property: For any routes and tables, hash(input) == hash(input)
func TestHashDeterminism_ConfigHash(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random routes
		numRoutes := 1 + r.Intn(10)
		routes := generateRandomRoutes(r, numRoutes)

		// Generate random tables
		numReadOnly := r.Intn(5)
		numReadWrite := r.Intn(5)
		readOnlyTables := make([]string, numReadOnly)
		readWriteTables := make([]string, numReadWrite)
		for i := 0; i < numReadOnly; i++ {
			readOnlyTables[i] = randomString(r, 5, 15)
		}
		for i := 0; i < numReadWrite; i++ {
			readWriteTables[i] = randomString(r, 5, 15)
		}

		// Empty lambda config for this test
		lambdaConfig := make(map[string]interface{})

		// Calculate hash twice with same inputs
		hash1, err1 := calculateConfigHash(routes, nil, lambdaConfig, readOnlyTables, readWriteTables)
		hash2, err2 := calculateConfigHash(routes, nil, lambdaConfig, readOnlyTables, readWriteTables)

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating hash: err1=%v, err2=%v", err1, err2)
			return false
		}

		if hash1 != hash2 {
			t.Logf("Hash mismatch: hash1=%s, hash2=%s", hash1, hash2)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Hash determinism property failed: %v", err)
	}
}

// TestHashDeterminism_GatewayHash tests that calculateGatewayHash produces
// the same hash for the same routes.
// Property: For any routes, hash(routes) == hash(routes)
func TestHashDeterminism_GatewayHash(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random routes
		numRoutes := 1 + r.Intn(10)
		routes := generateRandomRoutes(r, numRoutes)

		// Calculate hash twice with same inputs
		hash1, err1 := calculateGatewayHash(routes, "")
		hash2, err2 := calculateGatewayHash(routes, "")

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating hash: err1=%v, err2=%v", err1, err2)
			return false
		}

		if hash1 != hash2 {
			t.Logf("Hash mismatch: hash1=%s, hash2=%s", hash1, hash2)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Gateway hash determinism property failed: %v", err)
	}
}

// TestHashDeterminism_RouteOrderIndependence tests that hash is the same
// regardless of the order routes are provided.
// Property: For any routes, hash(routes) == hash(shuffle(routes))
func TestHashDeterminism_RouteOrderIndependence(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random routes
		numRoutes := 2 + r.Intn(10) // At least 2 routes to make shuffling meaningful
		routes := generateRandomRoutes(r, numRoutes)

		// Create a shuffled copy
		shuffledRoutes := make([]utils.Route, len(routes))
		copy(shuffledRoutes, routes)
		r.Shuffle(len(shuffledRoutes), func(i, j int) {
			shuffledRoutes[i], shuffledRoutes[j] = shuffledRoutes[j], shuffledRoutes[i]
		})

		// Calculate hash for both orderings
		hash1, err1 := calculateGatewayHash(routes, "")
		hash2, err2 := calculateGatewayHash(shuffledRoutes, "")

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating hash: err1=%v, err2=%v", err1, err2)
			return false
		}

		if hash1 != hash2 {
			t.Logf("Hash differs for different route orders: hash1=%s, hash2=%s", hash1, hash2)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Route order independence property failed: %v", err)
	}
}

// TestHashDeterminism_TableOrderIndependence tests that hash is the same
// regardless of the order tables are provided within routes.
// Property: For any route with tables, hash(route) == hash(route with shuffled tables)
func TestHashDeterminism_TableOrderIndependence(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate routes with tables
		numRoutes := 1 + r.Intn(5)
		routes := make([]utils.Route, numRoutes)
		for i := 0; i < numRoutes; i++ {
			route := generateRandomRoute(r)
			// Ensure at least 2 tables for meaningful shuffle
			numTables := 2 + r.Intn(4)
			route.Tables = make([]string, numTables)
			for j := 0; j < numTables; j++ {
				route.Tables[j] = randomString(r, 5, 15)
			}
			routes[i] = route
		}

		// Create a copy with shuffled tables
		shuffledRoutes := make([]utils.Route, len(routes))
		for i, route := range routes {
			shuffledRoutes[i] = route
			shuffledRoutes[i].Tables = make([]string, len(route.Tables))
			copy(shuffledRoutes[i].Tables, route.Tables)
			r.Shuffle(len(shuffledRoutes[i].Tables), func(a, b int) {
				shuffledRoutes[i].Tables[a], shuffledRoutes[i].Tables[b] = shuffledRoutes[i].Tables[b], shuffledRoutes[i].Tables[a]
			})
		}

		// Calculate hash for both
		hash1, err1 := calculateGatewayHash(routes, "")
		hash2, err2 := calculateGatewayHash(shuffledRoutes, "")

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating hash: err1=%v, err2=%v", err1, err2)
			return false
		}

		if hash1 != hash2 {
			t.Logf("Hash differs for different table orders: hash1=%s, hash2=%s", hash1, hash2)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Table order independence property failed: %v", err)
	}
}

// TestHashDeterminism_DifferentInputsDifferentHashes tests that different
// inputs produce different hashes (collision resistance).
// Property: For any two different route sets, hash(routes1) != hash(routes2) (with high probability)
func TestHashDeterminism_DifferentInputsDifferentHashes(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate two different route sets
		numRoutes1 := 1 + r.Intn(5)
		numRoutes2 := 1 + r.Intn(5)
		routes1 := generateRandomRoutes(r, numRoutes1)
		routes2 := generateRandomRoutes(r, numRoutes2)

		// Skip if routes happen to be identical (very unlikely but possible)
		if reflect.DeepEqual(sortedRoutes(routes1), sortedRoutes(routes2)) {
			return true // Skip this case
		}

		hash1, err1 := calculateGatewayHash(routes1, "")
		hash2, err2 := calculateGatewayHash(routes2, "")

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating hash: err1=%v, err2=%v", err1, err2)
			return false
		}

		// Different inputs should produce different hashes
		if hash1 == hash2 {
			t.Logf("Hash collision detected: routes1=%v, routes2=%v, hash=%s", routes1, routes2, hash1)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Different inputs different hashes property failed: %v", err)
	}
}

// sortedRoutes returns a sorted copy of routes for comparison
func sortedRoutes(routes []utils.Route) []utils.Route {
	sorted := make([]utils.Route, len(routes))
	copy(sorted, routes)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Gateway != sorted[j].Gateway {
			return sorted[i].Gateway < sorted[j].Gateway
		}
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		return sorted[i].Verb < sorted[j].Verb
	})
	return sorted
}


// Feature: trigger-lifecycle-management, Property 7: Hash Includes Triggers
// *For any* two Lambda configurations that differ only in their `sns_triggers` or
// `sqs_triggers`, the calculated config hash SHALL be different.
// **Validates: Requirements 3.1, 3.2**

// TestHashIncludesTriggers_SNS tests that SNS trigger changes affect the config hash.
func TestHashIncludesTriggers_SNS(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		lambdaName := randomString(r, 5, 15)
		routes := []utils.Route{{Lambda: lambdaName}}
		layerArns := []string{}
		readOnlyTables := []string{}
		readWriteTables := []string{}

		// Generate random SNS trigger config
		topicArn1 := "arn:aws:sns:us-east-1:123456789012:topic-" + randomString(r, 5, 10)
		topicArn2 := "arn:aws:sns:us-east-1:123456789012:topic-" + randomString(r, 5, 10)

		// Ensure topic ARNs are different
		for topicArn1 == topicArn2 {
			topicArn2 = "arn:aws:sns:us-east-1:123456789012:topic-" + randomString(r, 5, 10)
		}

		// Config without SNS triggers
		lambdaConfigNoTriggers := map[string]interface{}{
			lambdaName: map[string]interface{}{},
		}

		// Config with one SNS trigger
		lambdaConfigOneTrigger := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sns_triggers": []map[string]interface{}{
					{"topic_arn": topicArn1},
				},
			},
		}

		// Config with different SNS trigger
		lambdaConfigDifferentTrigger := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sns_triggers": []map[string]interface{}{
					{"topic_arn": topicArn2},
				},
			},
		}

		// Config with two SNS triggers
		lambdaConfigTwoTriggers := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sns_triggers": []map[string]interface{}{
					{"topic_arn": topicArn1},
					{"topic_arn": topicArn2},
				},
			},
		}

		// Calculate hashes
		hashNoTriggers, err1 := calculateLambdaConfigHash(routes, lambdaConfigNoTriggers, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hashOneTrigger, err2 := calculateLambdaConfigHash(routes, lambdaConfigOneTrigger, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hashDifferentTrigger, err3 := calculateLambdaConfigHash(routes, lambdaConfigDifferentTrigger, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hashTwoTriggers, err4 := calculateLambdaConfigHash(routes, lambdaConfigTwoTriggers, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)

		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			t.Logf("Error calculating hashes: err1=%v, err2=%v, err3=%v, err4=%v", err1, err2, err3, err4)
			return false
		}

		// Property: Adding SNS trigger should change hash
		if hashNoTriggers == hashOneTrigger {
			t.Logf("Hash did not change when SNS trigger was added: noTriggers=%s, oneTrigger=%s", hashNoTriggers, hashOneTrigger)
			return false
		}

		// Property: Different SNS trigger should produce different hash
		if hashOneTrigger == hashDifferentTrigger {
			t.Logf("Hash did not change for different SNS trigger: trigger1=%s, trigger2=%s", hashOneTrigger, hashDifferentTrigger)
			return false
		}

		// Property: Adding second trigger should change hash
		if hashOneTrigger == hashTwoTriggers {
			t.Logf("Hash did not change when second SNS trigger was added: one=%s, two=%s", hashOneTrigger, hashTwoTriggers)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Hash includes SNS triggers property failed: %v", err)
	}
}

// TestHashIncludesTriggers_SQS tests that SQS trigger changes affect the config hash.
func TestHashIncludesTriggers_SQS(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		lambdaName := randomString(r, 5, 15)
		routes := []utils.Route{{Lambda: lambdaName}}
		layerArns := []string{}
		readOnlyTables := []string{}
		readWriteTables := []string{}

		// Generate random SQS queue ARNs
		queueArn1 := "arn:aws:sqs:us-east-1:123456789012:queue-" + randomString(r, 5, 10)
		queueArn2 := "arn:aws:sqs:us-east-1:123456789012:queue-" + randomString(r, 5, 10)

		// Ensure queue ARNs are different
		for queueArn1 == queueArn2 {
			queueArn2 = "arn:aws:sqs:us-east-1:123456789012:queue-" + randomString(r, 5, 10)
		}

		// Config without SQS triggers
		lambdaConfigNoTriggers := map[string]interface{}{
			lambdaName: map[string]interface{}{},
		}

		// Config with one SQS trigger
		lambdaConfigOneTrigger := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sqs_triggers": []map[string]interface{}{
					{"queue_arn": queueArn1, "batch_size": 10},
				},
			},
		}

		// Config with different SQS trigger
		lambdaConfigDifferentTrigger := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sqs_triggers": []map[string]interface{}{
					{"queue_arn": queueArn2, "batch_size": 10},
				},
			},
		}

		// Config with different batch_size
		lambdaConfigDifferentBatchSize := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sqs_triggers": []map[string]interface{}{
					{"queue_arn": queueArn1, "batch_size": 50},
				},
			},
		}

		// Calculate hashes
		hashNoTriggers, err1 := calculateLambdaConfigHash(routes, lambdaConfigNoTriggers, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hashOneTrigger, err2 := calculateLambdaConfigHash(routes, lambdaConfigOneTrigger, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hashDifferentTrigger, err3 := calculateLambdaConfigHash(routes, lambdaConfigDifferentTrigger, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)
		hashDifferentBatchSize, err4 := calculateLambdaConfigHash(routes, lambdaConfigDifferentBatchSize, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)

		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			t.Logf("Error calculating hashes: err1=%v, err2=%v, err3=%v, err4=%v", err1, err2, err3, err4)
			return false
		}

		// Property: Adding SQS trigger should change hash
		if hashNoTriggers == hashOneTrigger {
			t.Logf("Hash did not change when SQS trigger was added: noTriggers=%s, oneTrigger=%s", hashNoTriggers, hashOneTrigger)
			return false
		}

		// Property: Different SQS trigger should produce different hash
		if hashOneTrigger == hashDifferentTrigger {
			t.Logf("Hash did not change for different SQS trigger: trigger1=%s, trigger2=%s", hashOneTrigger, hashDifferentTrigger)
			return false
		}

		// Property: Different batch_size should produce different hash
		if hashOneTrigger == hashDifferentBatchSize {
			t.Logf("Hash did not change for different batch_size: size10=%s, size50=%s", hashOneTrigger, hashDifferentBatchSize)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Hash includes SQS triggers property failed: %v", err)
	}
}

// Feature: trigger-lifecycle-management, Property 8: Config-Only Update Detection
// *For any* Lambda where only trigger configuration changes (source code unchanged),
// the detected update type SHALL be `LambdaUpdateTypeConfig`, not `LambdaUpdateTypeSource`
// or `LambdaUpdateTypeBoth`.
// **Validates: Requirements 3.3**

// TestConfigOnlyUpdateDetection_TriggerChanges tests that trigger-only changes
// are detected as config-only updates (not source changes).
func TestConfigOnlyUpdateDetection_TriggerChanges(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		lambdaName := randomString(r, 5, 15)
		routes := []utils.Route{{Lambda: lambdaName}}
		layerArns := []string{}
		readOnlyTables := []string{}
		readWriteTables := []string{}

		// Generate random trigger config
		topicArn := "arn:aws:sns:us-east-1:123456789012:topic-" + randomString(r, 5, 10)
		queueArn := "arn:aws:sqs:us-east-1:123456789012:queue-" + randomString(r, 5, 10)

		// Old config: no triggers
		oldLambdaConfig := map[string]interface{}{
			lambdaName: map[string]interface{}{},
		}

		// New config: with triggers
		newLambdaConfig := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sns_triggers": []map[string]interface{}{
					{"topic_arn": topicArn},
				},
				"sqs_triggers": []map[string]interface{}{
					{"queue_arn": queueArn, "batch_size": 10},
				},
			},
		}

		// Calculate config hashes (these should differ)
		oldConfigHash, err1 := calculateLambdaConfigHash(routes, oldLambdaConfig, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)
		newConfigHash, err2 := calculateLambdaConfigHash(routes, newLambdaConfig, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating config hashes: err1=%v, err2=%v", err1, err2)
			return false
		}

		// Simulate source hashes (same for both - source didn't change)
		sourceHash := "abc123" + randomString(r, 10, 20)

		// Create hash maps for diff calculation
		oldLambdaHashes := map[string]string{lambdaName: oldConfigHash + sourceHash}
		newLambdaHashes := map[string]string{lambdaName: newConfigHash + sourceHash}
		oldSourceHashes := map[string]string{lambdaName: sourceHash}
		newSourceHashes := map[string]string{lambdaName: sourceHash}
		oldConfigHashes := map[string]string{lambdaName: oldConfigHash}
		newConfigHashes := map[string]string{lambdaName: newConfigHash}

		// Calculate diff
		diff := calculateResourceDiff(
			map[string]string{}, // oldGatewayHashes
			map[string]string{}, // newGatewayHashes
			oldLambdaHashes,
			newLambdaHashes,
			oldSourceHashes,
			newSourceHashes,
			oldConfigHashes,
			newConfigHashes,
			routes,
		)

		// Property: Config hash should have changed
		if oldConfigHash == newConfigHash {
			t.Logf("Config hash did not change when triggers were added")
			return false
		}

		// Property: Lambda should be in ModifiedLambdas
		if _, exists := diff.ModifiedLambdas[lambdaName]; !exists {
			t.Logf("Lambda not detected as modified when triggers changed")
			return false
		}

		// Property: Lambda should be in ConfigOnlyChangedLambdas (not SourceChangedLambdas)
		if _, exists := diff.ConfigOnlyChangedLambdas[lambdaName]; !exists {
			t.Logf("Lambda not detected as config-only change when only triggers changed")
			return false
		}

		// Property: Lambda should NOT be in SourceChangedLambdas
		if _, exists := diff.SourceChangedLambdas[lambdaName]; exists {
			t.Logf("Lambda incorrectly detected as source change when only triggers changed")
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Config-only update detection property failed: %v", err)
	}
}

// TestConfigOnlyUpdateDetection_TriggerRemoval tests that removing triggers
// is detected as a config-only change.
func TestConfigOnlyUpdateDetection_TriggerRemoval(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		lambdaName := randomString(r, 5, 15)
		routes := []utils.Route{{Lambda: lambdaName}}
		layerArns := []string{}
		readOnlyTables := []string{}
		readWriteTables := []string{}

		// Generate random trigger config
		topicArn := "arn:aws:sns:us-east-1:123456789012:topic-" + randomString(r, 5, 10)

		// Old config: with triggers
		oldLambdaConfig := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sns_triggers": []map[string]interface{}{
					{"topic_arn": topicArn},
				},
			},
		}

		// New config: no triggers
		newLambdaConfig := map[string]interface{}{
			lambdaName: map[string]interface{}{},
		}

		// Calculate config hashes
		oldConfigHash, err1 := calculateLambdaConfigHash(routes, oldLambdaConfig, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)
		newConfigHash, err2 := calculateLambdaConfigHash(routes, newLambdaConfig, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating config hashes: err1=%v, err2=%v", err1, err2)
			return false
		}

		// Simulate source hashes (same for both)
		sourceHash := "xyz789" + randomString(r, 10, 20)

		// Create hash maps for diff calculation
		oldLambdaHashes := map[string]string{lambdaName: oldConfigHash + sourceHash}
		newLambdaHashes := map[string]string{lambdaName: newConfigHash + sourceHash}
		oldSourceHashes := map[string]string{lambdaName: sourceHash}
		newSourceHashes := map[string]string{lambdaName: sourceHash}
		oldConfigHashes := map[string]string{lambdaName: oldConfigHash}
		newConfigHashes := map[string]string{lambdaName: newConfigHash}

		// Calculate diff
		diff := calculateResourceDiff(
			map[string]string{},
			map[string]string{},
			oldLambdaHashes,
			newLambdaHashes,
			oldSourceHashes,
			newSourceHashes,
			oldConfigHashes,
			newConfigHashes,
			routes,
		)

		// Property: Config hash should have changed
		if oldConfigHash == newConfigHash {
			t.Logf("Config hash did not change when triggers were removed")
			return false
		}

		// Property: Lambda should be in ConfigOnlyChangedLambdas
		if _, exists := diff.ConfigOnlyChangedLambdas[lambdaName]; !exists {
			t.Logf("Lambda not detected as config-only change when triggers were removed")
			return false
		}

		// Property: Lambda should NOT be in SourceChangedLambdas
		if _, exists := diff.SourceChangedLambdas[lambdaName]; exists {
			t.Logf("Lambda incorrectly detected as source change when only triggers were removed")
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Config-only update detection (trigger removal) property failed: %v", err)
	}
}

// TestConfigOnlyUpdateDetection_TriggerModification tests that modifying trigger
// attributes (like batch_size) is detected as a config-only change.
func TestConfigOnlyUpdateDetection_TriggerModification(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		lambdaName := randomString(r, 5, 15)
		routes := []utils.Route{{Lambda: lambdaName}}
		layerArns := []string{}
		readOnlyTables := []string{}
		readWriteTables := []string{}

		// Generate random queue ARN
		queueArn := "arn:aws:sqs:us-east-1:123456789012:queue-" + randomString(r, 5, 10)

		// Generate two different batch sizes
		batchSize1 := r.Intn(100) + 1
		batchSize2 := r.Intn(100) + 1
		for batchSize1 == batchSize2 {
			batchSize2 = r.Intn(100) + 1
		}

		// Old config: with batch_size1
		oldLambdaConfig := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sqs_triggers": []map[string]interface{}{
					{"queue_arn": queueArn, "batch_size": batchSize1},
				},
			},
		}

		// New config: with batch_size2
		newLambdaConfig := map[string]interface{}{
			lambdaName: map[string]interface{}{
				"sqs_triggers": []map[string]interface{}{
					{"queue_arn": queueArn, "batch_size": batchSize2},
				},
			},
		}

		// Calculate config hashes
		oldConfigHash, err1 := calculateLambdaConfigHash(routes, oldLambdaConfig, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)
		newConfigHash, err2 := calculateLambdaConfigHash(routes, newLambdaConfig, lambdaName, layerArns, nil, readOnlyTables, readWriteTables, nil)

		if err1 != nil || err2 != nil {
			t.Logf("Error calculating config hashes: err1=%v, err2=%v", err1, err2)
			return false
		}

		// Simulate source hashes (same for both)
		sourceHash := "def456" + randomString(r, 10, 20)

		// Create hash maps for diff calculation
		oldLambdaHashes := map[string]string{lambdaName: oldConfigHash + sourceHash}
		newLambdaHashes := map[string]string{lambdaName: newConfigHash + sourceHash}
		oldSourceHashes := map[string]string{lambdaName: sourceHash}
		newSourceHashes := map[string]string{lambdaName: sourceHash}
		oldConfigHashes := map[string]string{lambdaName: oldConfigHash}
		newConfigHashes := map[string]string{lambdaName: newConfigHash}

		// Calculate diff
		diff := calculateResourceDiff(
			map[string]string{},
			map[string]string{},
			oldLambdaHashes,
			newLambdaHashes,
			oldSourceHashes,
			newSourceHashes,
			oldConfigHashes,
			newConfigHashes,
			routes,
		)

		// Property: Config hash should have changed
		if oldConfigHash == newConfigHash {
			t.Logf("Config hash did not change when batch_size was modified")
			return false
		}

		// Property: Lambda should be in ConfigOnlyChangedLambdas
		if _, exists := diff.ConfigOnlyChangedLambdas[lambdaName]; !exists {
			t.Logf("Lambda not detected as config-only change when batch_size was modified")
			return false
		}

		// Property: Lambda should NOT be in SourceChangedLambdas
		if _, exists := diff.SourceChangedLambdas[lambdaName]; exists {
			t.Logf("Lambda incorrectly detected as source change when only batch_size was modified")
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Config-only update detection (trigger modification) property failed: %v", err)
	}
}

// TestHashStability_NilVsEmptySlices tests that nil and empty slices produce
// the same hash. This prevents phantom updates when a field transitions between
// nil (unset) and empty (set but no elements) across Terraform plan/apply cycles.
func TestHashStability_NilVsEmptySlices(t *testing.T) {
	routes := []utils.Route{
		{Name: "test", Verb: "GET", Path: "/test", Gateway: "api", Lambda: "handler", Auth: "none"},
	}
	lambdaConfig := map[string]interface{}{}

	// Hash with nil slices (simulates unset optional fields)
	hash1, err := calculateLambdaHash(routes, lambdaConfig, "handler", "/tmp/nonexistent", nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Failed to calculate hash with nil slices: %v", err)
	}

	// Hash with empty slices (simulates set-but-empty optional fields)
	hash2, err := calculateLambdaHash(routes, lambdaConfig, "handler", "/tmp/nonexistent", []string{}, []string{}, nil, []string{}, []string{}, []string{})
	if err != nil {
		t.Fatalf("Failed to calculate hash with empty slices: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("Hash differs between nil and empty slices:\n  nil slices:   %s\n  empty slices: %s", hash1, hash2)
	}
}

// TestHashStability_NilVsEmptyConfigSlices tests that nil and empty slices
// produce the same config hash.
func TestHashStability_NilVsEmptyConfigSlices(t *testing.T) {
	routes := []utils.Route{
		{Name: "test", Verb: "GET", Path: "/test", Gateway: "api", Lambda: "handler", Auth: "none"},
	}
	lambdaConfig := map[string]interface{}{}

	// Config hash with nil slices
	hash1, err := calculateLambdaConfigHash(routes, lambdaConfig, "handler", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Failed to calculate config hash with nil slices: %v", err)
	}

	// Config hash with empty slices
	hash2, err := calculateLambdaConfigHash(routes, lambdaConfig, "handler", []string{}, nil, []string{}, []string{}, []string{})
	if err != nil {
		t.Fatalf("Failed to calculate config hash with empty slices: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("Config hash differs between nil and empty slices:\n  nil slices:   %s\n  empty slices: %s", hash1, hash2)
	}
}

// TestHashStability_FileFiltering tests that hashDirectoryContents ignores
// non-source files like .DS_Store and editor temp files.
func TestHashStability_FileFiltering(t *testing.T) {
	// Create a temp directory with source files
	dir := t.TempDir()
	os.WriteFile(dir+"/handler.rb", []byte("def handler; end"), 0644)
	os.WriteFile(dir+"/helper.rb", []byte("module Helper; end"), 0644)

	hash1, err := hashDirectoryContents(dir)
	if err != nil {
		t.Fatalf("Failed to hash directory: %v", err)
	}

	// Add a .DS_Store file — hash should not change
	os.WriteFile(dir+"/.DS_Store", []byte("Bud1\x00"), 0644)

	hash2, err := hashDirectoryContents(dir)
	if err != nil {
		t.Fatalf("Failed to hash directory after .DS_Store: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("Hash changed after adding .DS_Store:\n  before: %s\n  after:  %s", hash1, hash2)
	}

	// Add an editor swap file — hash should not change
	os.WriteFile(dir+"/.handler.rb.swp", []byte("swap data"), 0644)

	hash3, err := hashDirectoryContents(dir)
	if err != nil {
		t.Fatalf("Failed to hash directory after .swp: %v", err)
	}

	if hash1 != hash3 {
		t.Errorf("Hash changed after adding .swp file:\n  before: %s\n  after:  %s", hash1, hash3)
	}
}

// Test that expectedDeployedModelFingerprint is deterministic and order-independent
func TestExpectedDeployedModelFingerprint_Determinism(t *testing.T) {
	models := []utils.ModelDefinition{
		{
			Name:        "create_item_customer",
			Description: "Create item request",
			Properties: map[string]utils.ModelProperty{
				"name":        {Type: "string"},
				"description": {Type: "string"},
			},
			Required: []string{"name"},
		},
		{
			Name:        "update_item_customer",
			Description: "Update item request",
			Properties: map[string]utils.ModelProperty{
				"name": {Type: "string"},
			},
		},
	}

	hash1, err := expectedDeployedModelFingerprint(models)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reverse order
	reversed := []utils.ModelDefinition{models[1], models[0]}
	hash2, err := expectedDeployedModelFingerprint(reversed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("fingerprint should be order-independent: %s != %s", hash1, hash2)
	}

	if hash1 == "" {
		t.Error("fingerprint should not be empty for non-empty models")
	}
}

// Test that empty models return empty fingerprint
func TestExpectedDeployedModelFingerprint_Empty(t *testing.T) {
	hash, err := expectedDeployedModelFingerprint(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash != "" {
		t.Errorf("expected empty fingerprint for nil models, got %s", hash)
	}
}

// Test that different models produce different fingerprints
func TestExpectedDeployedModelFingerprint_DifferentModels(t *testing.T) {
	models1 := []utils.ModelDefinition{
		{Name: "create_item", Properties: map[string]utils.ModelProperty{"name": {Type: "string"}}},
	}
	models2 := []utils.ModelDefinition{
		{Name: "create_item", Properties: map[string]utils.ModelProperty{"name": {Type: "integer"}}},
	}

	hash1, _ := expectedDeployedModelFingerprint(models1)
	hash2, _ := expectedDeployedModelFingerprint(models2)

	if hash1 == hash2 {
		t.Error("different models should produce different fingerprints")
	}
}

// TestCalculateAllLambdaHashes_IncludesAllProvidedLambdas verifies that the hash
// functions return entries for every lambda in the provided list, even if some
// lambdas don't have .rb files on disk. This prevents "Provider produced
// inconsistent result after apply" errors where ModifyPlan and Update disagree
// on which lambdas exist in the hash maps.
func TestCalculateAllLambdaHashes_IncludesAllProvidedLambdas(t *testing.T) {
	// Create a temp dir with only one .rb file
	tmpDir, err := os.MkdirTemp("", "lambda-hash-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create one lambda file on disk
	os.WriteFile(tmpDir+"/customer.rb", []byte("# customer lambda"), 0644)

	// Provide a list that includes a lambda NOT on disk (standalone from lambda_config)
	lambdas := []string{"customer", "legal_documents"}
	routes := []utils.Route{}
	lambdaConfig := map[string]interface{}{
		"legal_documents": map[string]interface{}{"timeout": 60},
	}

	t.Run("calculateAllLambdaHashes includes all provided lambdas", func(t *testing.T) {
		hashes, err := calculateAllLambdaHashes(lambdas, routes, lambdaConfig, tmpDir, nil, nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, l := range lambdas {
			if _, ok := hashes[l]; !ok {
				t.Errorf("missing hash for lambda %q", l)
			}
		}
	})

	t.Run("calculateAllLambdaSourceHashes includes all provided lambdas", func(t *testing.T) {
		hashes, err := calculateAllLambdaSourceHashes(lambdas, tmpDir, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, l := range lambdas {
			if _, ok := hashes[l]; !ok {
				t.Errorf("missing hash for lambda %q", l)
			}
		}
	})

	t.Run("calculateAllLambdaConfigHashes includes all provided lambdas", func(t *testing.T) {
		hashes, err := calculateAllLambdaConfigHashes(lambdas, routes, lambdaConfig, nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, l := range lambdas {
			if _, ok := hashes[l]; !ok {
				t.Errorf("missing hash for lambda %q", l)
			}
		}
	})
}
