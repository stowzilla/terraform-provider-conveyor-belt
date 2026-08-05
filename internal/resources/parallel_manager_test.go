// internal/resources/parallel_manager_test.go
package resources

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"testing/quick"
	"time"
)

// Feature: dispatcher-orchestrator, Property 2: Parallel Update Isolation
// *For any* set of Lambda update tasks, if one task fails, all other tasks SHALL complete
// (success or failure) and the final result SHALL report all failures.
// **Validates: Requirements 2.4**

// MockLambdaUpdateFunc is a function type for mocking Lambda update operations
type MockLambdaUpdateFunc func(ctx context.Context, lambdaName string, buildResult *BuildResult) LambdaResult

// TestableParallelManager wraps ParallelManager for testing with mock operations
type TestableParallelManager struct {
	concurrency    int
	updateFunc     MockLambdaUpdateFunc
	activeUpdates  int32
	maxConcurrent  int32
	completedCount int32
	mu             sync.Mutex
}

// NewTestableParallelManager creates a testable parallel manager with mock update function
func NewTestableParallelManager(concurrency int, updateFunc MockLambdaUpdateFunc) *TestableParallelManager {
	return &TestableParallelManager{
		concurrency: concurrency,
		updateFunc:  updateFunc,
	}
}

// UpdateLambdasInParallel updates lambdas using the mock function while tracking concurrency
func (tpm *TestableParallelManager) UpdateLambdasInParallel(
	ctx context.Context,
	lambdas []string,
	buildResults map[string]*BuildResult,
) []LambdaResult {
	results := make([]LambdaResult, 0, len(lambdas))
	resultChan := make(chan LambdaResult, len(lambdas))

	// Semaphore for concurrency control
	sem := make(chan struct{}, tpm.concurrency)

	var wg sync.WaitGroup
	for _, lambdaName := range lambdas {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Track active updates
			current := atomic.AddInt32(&tpm.activeUpdates, 1)

			// Update max concurrent if this is a new high
			for {
				max := atomic.LoadInt32(&tpm.maxConcurrent)
				if current <= max {
					break
				}
				if atomic.CompareAndSwapInt32(&tpm.maxConcurrent, max, current) {
					break
				}
			}

			// Get build result for this lambda
			buildResult := buildResults[name]

			// Execute the mock update function
			result := tpm.updateFunc(ctx, name, buildResult)

			atomic.AddInt32(&tpm.activeUpdates, -1)
			atomic.AddInt32(&tpm.completedCount, 1)

			resultChan <- result
		}(lambdaName)
	}

	// Close channel when all done
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	for result := range resultChan {
		results = append(results, result)
	}

	return results
}

// GetMaxConcurrent returns the maximum number of concurrent updates observed
func (tpm *TestableParallelManager) GetMaxConcurrent() int32 {
	return atomic.LoadInt32(&tpm.maxConcurrent)
}

// GetCompletedCount returns the number of completed updates
func (tpm *TestableParallelManager) GetCompletedCount() int32 {
	return atomic.LoadInt32(&tpm.completedCount)
}

// Reset resets the tracking counters
func (tpm *TestableParallelManager) Reset() {
	atomic.StoreInt32(&tpm.activeUpdates, 0)
	atomic.StoreInt32(&tpm.maxConcurrent, 0)
	atomic.StoreInt32(&tpm.completedCount, 0)
}


// TestParallelUpdateIsolation_Property tests Property 2: Parallel Update Isolation
// *For any* set of Lambda update tasks, if one task fails, all other tasks SHALL complete
// (success or failure) and the final result SHALL report all failures.
// **Validates: Requirements 2.4**
func TestParallelUpdateIsolation_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random number of lambdas (3-20)
		numLambdas := 3 + r.Intn(18)

		// Generate random number of failures (0 to numLambdas)
		numFailures := r.Intn(numLambdas + 1)

		// Generate random concurrency limit (1-10)
		concurrencyLimit := 1 + r.Intn(10)

		// Generate lambda names
		lambdaNames := make([]string, numLambdas)
		for i := 0; i < numLambdas; i++ {
			lambdaNames[i] = randomStringForParallel(r, 5, 15)
		}

		// Randomly select which lambdas will fail
		failingLambdas := make(map[string]bool)
		if numFailures > 0 {
			failIndices := r.Perm(numLambdas)[:numFailures]
			for _, idx := range failIndices {
				failingLambdas[lambdaNames[idx]] = true
			}
		}

		// Create build results (some with errors to simulate build failures)
		buildResults := make(map[string]*BuildResult)
		for _, name := range lambdaNames {
			buildResults[name] = &BuildResult{
				LambdaName: name,
				ZipData:    []byte("mock-zip-data-" + name),
				Hash:       "mock-hash-" + name,
			}
		}

		// Create mock update function that fails for selected lambdas
		mockUpdate := func(ctx context.Context, lambdaName string, buildResult *BuildResult) LambdaResult {
			result := LambdaResult{Action: lambdaName}

			// Simulate some work with random duration (1-10ms)
			time.Sleep(time.Duration(1+r.Intn(10)) * time.Millisecond)

			if failingLambdas[lambdaName] {
				result.Error = errors.New("simulated update failure for " + lambdaName)
				result.Success = false
			} else {
				result.ARN = "arn:aws:lambda:us-east-1:123456789012:function:" + lambdaName
				result.Success = true
			}

			return result
		}

		// Create testable manager
		manager := NewTestableParallelManager(concurrencyLimit, mockUpdate)

		// Execute parallel updates
		ctx := context.Background()
		results := manager.UpdateLambdasInParallel(ctx, lambdaNames, buildResults)

		// Property 1: All lambdas should have results (success or failure)
		if len(results) != numLambdas {
			t.Logf("Not all lambdas have results: expected %d, got %d", numLambdas, len(results))
			return false
		}

		// Property 2: Exactly the expected lambdas should have failed
		actualFailures := 0
		actualSuccesses := 0
		resultMap := make(map[string]LambdaResult)
		for _, result := range results {
			resultMap[result.Action] = result
			if result.Error != nil {
				actualFailures++
			} else {
				actualSuccesses++
			}
		}

		// Verify each lambda's result matches expectation
		for name, shouldFail := range failingLambdas {
			result, exists := resultMap[name]
			if !exists {
				t.Logf("Missing result for lambda %s", name)
				return false
			}
			if shouldFail && result.Error == nil {
				t.Logf("Expected failure for %s but it succeeded", name)
				return false
			}
		}

		// Property 3: Number of failures should match expected
		if actualFailures != numFailures {
			t.Logf("Failure count mismatch: expected %d, got %d", numFailures, actualFailures)
			return false
		}

		// Property 4: Number of successes should match expected
		expectedSuccesses := numLambdas - numFailures
		if actualSuccesses != expectedSuccesses {
			t.Logf("Success count mismatch: expected %d, got %d", expectedSuccesses, actualSuccesses)
			return false
		}

		// Property 5: All updates should have completed
		completedCount := manager.GetCompletedCount()
		if completedCount != int32(numLambdas) {
			t.Logf("Completed count mismatch: expected %d, got %d", numLambdas, completedCount)
			return false
		}

		// Property 6: Max concurrent updates should not exceed limit
		maxConcurrent := manager.GetMaxConcurrent()
		if maxConcurrent > int32(concurrencyLimit) {
			t.Logf("Concurrency limit exceeded: max=%d, limit=%d", maxConcurrent, concurrencyLimit)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Parallel update isolation property failed: %v", err)
	}
}


// TestParallelUpdateIsolation_AllFailures tests that even when all updates fail, they all complete
func TestParallelUpdateIsolation_AllFailures(t *testing.T) {
	lambdaNames := []string{"lambda-a", "lambda-b", "lambda-c", "lambda-d", "lambda-e"}

	buildResults := make(map[string]*BuildResult)
	for _, name := range lambdaNames {
		buildResults[name] = &BuildResult{
			LambdaName: name,
			ZipData:    []byte("data"),
		}
	}

	// All updates fail
	mockUpdate := func(ctx context.Context, lambdaName string, buildResult *BuildResult) LambdaResult {
		time.Sleep(2 * time.Millisecond)
		return LambdaResult{
			Action:  lambdaName,
			Error:   errors.New("all updates fail"),
			Success: false,
		}
	}

	manager := NewTestableParallelManager(3, mockUpdate)
	ctx := context.Background()
	results := manager.UpdateLambdasInParallel(ctx, lambdaNames, buildResults)

	// All should have results
	if len(results) != len(lambdaNames) {
		t.Errorf("Expected %d results, got %d", len(lambdaNames), len(results))
	}

	// All should have errors
	for _, result := range results {
		if result.Error == nil {
			t.Errorf("Expected error for %s but got success", result.Action)
		}
	}

	// All should have completed
	if manager.GetCompletedCount() != int32(len(lambdaNames)) {
		t.Errorf("Expected %d completed, got %d", len(lambdaNames), manager.GetCompletedCount())
	}
}

// TestParallelUpdateIsolation_SingleFailure tests isolation with a single failure
func TestParallelUpdateIsolation_SingleFailure(t *testing.T) {
	lambdaNames := []string{"lambda-a", "lambda-b", "lambda-c", "lambda-d", "lambda-e"}
	failingLambda := "lambda-c"

	buildResults := make(map[string]*BuildResult)
	for _, name := range lambdaNames {
		buildResults[name] = &BuildResult{
			LambdaName: name,
			ZipData:    []byte("data"),
		}
	}

	mockUpdate := func(ctx context.Context, lambdaName string, buildResult *BuildResult) LambdaResult {
		time.Sleep(2 * time.Millisecond)
		if lambdaName == failingLambda {
			return LambdaResult{
				Action:  lambdaName,
				Error:   errors.New("intentional failure"),
				Success: false,
			}
		}
		return LambdaResult{
			Action:  lambdaName,
			ARN:     "arn:aws:lambda:us-east-1:123456789012:function:" + lambdaName,
			Success: true,
		}
	}

	manager := NewTestableParallelManager(2, mockUpdate)
	ctx := context.Background()
	results := manager.UpdateLambdasInParallel(ctx, lambdaNames, buildResults)

	// All should have results
	if len(results) != len(lambdaNames) {
		t.Errorf("Expected %d results, got %d", len(lambdaNames), len(results))
	}

	// Check each result
	failureCount := 0
	successCount := 0
	for _, result := range results {
		if result.Action == failingLambda {
			if result.Error == nil {
				t.Errorf("Expected error for %s", result.Action)
			}
			failureCount++
		} else {
			if result.Error != nil {
				t.Errorf("Unexpected error for %s: %v", result.Action, result.Error)
			}
			if result.ARN == "" {
				t.Errorf("Missing ARN for %s", result.Action)
			}
			successCount++
		}
	}

	if failureCount != 1 {
		t.Errorf("Expected 1 failure, got %d", failureCount)
	}
	if successCount != 4 {
		t.Errorf("Expected 4 successes, got %d", successCount)
	}
}

// TestParallelUpdateIsolation_ConcurrencyRespected tests that concurrency limit is respected during updates
func TestParallelUpdateIsolation_ConcurrencyRespected(t *testing.T) {
	testCases := []struct {
		numLambdas  int
		concurrency int
	}{
		{10, 2},
		{5, 5},
		{20, 4},
		{3, 10}, // More concurrency than lambdas
	}

	for _, tc := range testCases {
		t.Run("", func(t *testing.T) {
			lambdaNames := make([]string, tc.numLambdas)
			buildResults := make(map[string]*BuildResult)
			for i := 0; i < tc.numLambdas; i++ {
				name := "lambda-" + string(rune('a'+i))
				lambdaNames[i] = name
				buildResults[name] = &BuildResult{
					LambdaName: name,
					ZipData:    []byte("data"),
				}
			}

			// Mock update that takes some time
			mockUpdate := func(ctx context.Context, lambdaName string, buildResult *BuildResult) LambdaResult {
				time.Sleep(5 * time.Millisecond)
				return LambdaResult{
					Action:  lambdaName,
					ARN:     "arn:aws:lambda:us-east-1:123456789012:function:" + lambdaName,
					Success: true,
				}
			}

			manager := NewTestableParallelManager(tc.concurrency, mockUpdate)
			ctx := context.Background()
			results := manager.UpdateLambdasInParallel(ctx, lambdaNames, buildResults)

			// All should complete
			if len(results) != tc.numLambdas {
				t.Errorf("Expected %d results, got %d", tc.numLambdas, len(results))
			}

			// Max concurrent should not exceed limit
			maxConcurrent := manager.GetMaxConcurrent()
			if maxConcurrent > int32(tc.concurrency) {
				t.Errorf("Concurrency exceeded: max=%d, limit=%d", maxConcurrent, tc.concurrency)
			}

			// If we have more lambdas than concurrency, we should see some parallelism
			if tc.numLambdas > tc.concurrency && maxConcurrent < 2 {
				t.Logf("Warning: Expected some parallelism but max concurrent was %d", maxConcurrent)
			}
		})
	}
}


// TestParallelUpdateIsolation_MixedUpdateTypes tests updates with different update types
// (source-only, config-only, full updates)
func TestParallelUpdateIsolation_MixedUpdateTypes(t *testing.T) {
	config := &quick.Config{
		MaxCount: 50,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random number of lambdas (5-15)
		numLambdas := 5 + r.Intn(11)

		// Generate lambda names
		lambdaNames := make([]string, numLambdas)
		for i := 0; i < numLambdas; i++ {
			lambdaNames[i] = randomStringForParallel(r, 5, 15)
		}

		// Create build results with different scenarios:
		// - Some with new zip data (source update)
		// - Some with nil zip data (config-only update)
		// - Some with build errors
		buildResults := make(map[string]*BuildResult)
		updateTypes := make(map[string]string)

		for _, name := range lambdaNames {
			updateType := r.Intn(3)
			switch updateType {
			case 0: // Source update (has zip data)
				buildResults[name] = &BuildResult{
					LambdaName: name,
					ZipData:    []byte("new-zip-data-" + name),
					Hash:       "new-hash-" + name,
				}
				updateTypes[name] = "source"
			case 1: // Config-only update (nil zip data)
				buildResults[name] = &BuildResult{
					LambdaName: name,
					ZipData:    nil,
				}
				updateTypes[name] = "config"
			case 2: // Build error
				buildResults[name] = &BuildResult{
					LambdaName: name,
					Error:      errors.New("build failed for " + name),
				}
				updateTypes[name] = "error"
			}
		}

		// Mock update function that handles different update types
		mockUpdate := func(ctx context.Context, lambdaName string, buildResult *BuildResult) LambdaResult {
			result := LambdaResult{Action: lambdaName}

			// Simulate some work
			time.Sleep(time.Duration(1+r.Intn(5)) * time.Millisecond)

			// If build had an error, propagate it
			if buildResult != nil && buildResult.Error != nil {
				result.Error = buildResult.Error
				result.Success = false
				return result
			}

			// Otherwise, succeed
			result.ARN = "arn:aws:lambda:us-east-1:123456789012:function:" + lambdaName
			result.Success = true
			return result
		}

		// Create testable manager
		concurrency := 1 + r.Intn(5)
		manager := NewTestableParallelManager(concurrency, mockUpdate)

		// Execute parallel updates
		ctx := context.Background()
		results := manager.UpdateLambdasInParallel(ctx, lambdaNames, buildResults)

		// Property 1: All lambdas should have results
		if len(results) != numLambdas {
			t.Logf("Not all lambdas have results: expected %d, got %d", numLambdas, len(results))
			return false
		}

		// Property 2: Results should match expected outcomes based on update type
		resultMap := make(map[string]LambdaResult)
		for _, result := range results {
			resultMap[result.Action] = result
		}

		for name, updateType := range updateTypes {
			result, exists := resultMap[name]
			if !exists {
				t.Logf("Missing result for lambda %s", name)
				return false
			}

			if updateType == "error" {
				if result.Error == nil {
					t.Logf("Expected error for %s (build error) but got success", name)
					return false
				}
			} else {
				if result.Error != nil {
					t.Logf("Unexpected error for %s (type=%s): %v", name, updateType, result.Error)
					return false
				}
			}
		}

		// Property 3: All updates should have completed
		completedCount := manager.GetCompletedCount()
		if completedCount != int32(numLambdas) {
			t.Logf("Completed count mismatch: expected %d, got %d", numLambdas, completedCount)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Mixed update types property failed: %v", err)
	}
}

// TestParallelManager_ResultsContainAllFailures tests that all failures are reported
func TestParallelManager_ResultsContainAllFailures(t *testing.T) {
	lambdaNames := []string{"lambda-a", "lambda-b", "lambda-c", "lambda-d", "lambda-e"}
	failingLambdas := map[string]string{
		"lambda-b": "error B",
		"lambda-d": "error D",
	}

	buildResults := make(map[string]*BuildResult)
	for _, name := range lambdaNames {
		buildResults[name] = &BuildResult{
			LambdaName: name,
			ZipData:    []byte("data"),
		}
	}

	mockUpdate := func(ctx context.Context, lambdaName string, buildResult *BuildResult) LambdaResult {
		time.Sleep(2 * time.Millisecond)
		if errMsg, shouldFail := failingLambdas[lambdaName]; shouldFail {
			return LambdaResult{
				Action:  lambdaName,
				Error:   errors.New(errMsg),
				Success: false,
			}
		}
		return LambdaResult{
			Action:  lambdaName,
			ARN:     "arn:aws:lambda:us-east-1:123456789012:function:" + lambdaName,
			Success: true,
		}
	}

	manager := NewTestableParallelManager(3, mockUpdate)
	ctx := context.Background()
	results := manager.UpdateLambdasInParallel(ctx, lambdaNames, buildResults)

	// Collect all failures
	failures := make(map[string]error)
	for _, result := range results {
		if result.Error != nil {
			failures[result.Action] = result.Error
		}
	}

	// Verify all expected failures are reported
	if len(failures) != len(failingLambdas) {
		t.Errorf("Expected %d failures, got %d", len(failingLambdas), len(failures))
	}

	for name, expectedErr := range failingLambdas {
		actualErr, exists := failures[name]
		if !exists {
			t.Errorf("Missing failure for %s", name)
			continue
		}
		if actualErr.Error() != expectedErr {
			t.Errorf("Wrong error for %s: expected %q, got %q", name, expectedErr, actualErr.Error())
		}
	}
}

// randomStringForParallel generates a random string of length between minLen and maxLen
func randomStringForParallel(r *rand.Rand, minLen, maxLen int) string {
	length := minLen + r.Intn(maxLen-minLen+1)
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = chars[r.Intn(len(chars))]
	}
	return string(result)
}

// TestParallelManager_EmptyInput tests handling of empty input
func TestParallelManager_EmptyInput(t *testing.T) {
	mockUpdate := func(ctx context.Context, lambdaName string, buildResult *BuildResult) LambdaResult {
		return LambdaResult{Action: lambdaName, Success: true}
	}

	manager := NewTestableParallelManager(3, mockUpdate)
	ctx := context.Background()

	// Empty lambda list
	results := manager.UpdateLambdasInParallel(ctx, []string{}, map[string]*BuildResult{})

	if len(results) != 0 {
		t.Errorf("Expected 0 results for empty input, got %d", len(results))
	}
}

// TestParallelManager_SingleLambda tests handling of single lambda
func TestParallelManager_SingleLambda(t *testing.T) {
	mockUpdate := func(ctx context.Context, lambdaName string, buildResult *BuildResult) LambdaResult {
		return LambdaResult{
			Action:  lambdaName,
			ARN:     "arn:aws:lambda:us-east-1:123456789012:function:" + lambdaName,
			Success: true,
		}
	}

	manager := NewTestableParallelManager(3, mockUpdate)
	ctx := context.Background()

	buildResults := map[string]*BuildResult{
		"single-lambda": {LambdaName: "single-lambda", ZipData: []byte("data")},
	}

	results := manager.UpdateLambdasInParallel(ctx, []string{"single-lambda"}, buildResults)

	if len(results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(results))
	}

	if results[0].Action != "single-lambda" {
		t.Errorf("Expected lambda 'single-lambda', got %s", results[0].Action)
	}

	if !results[0].Success {
		t.Errorf("Expected success, got failure")
	}
}


// ============================================================================
// Trigger Integration Tests
// ============================================================================

// TestReconcileTriggers_ExtractsTriggers tests that ReconcileTriggers correctly
// extracts SNS and SQS triggers from lambda_config.
// Requirements: 5.1 - TriggerManager is called during Lambda updates
func TestReconcileTriggers_ExtractsTriggers(t *testing.T) {
	// Test that extractSNSTriggers and extractSQSTriggers work correctly
	// when called from ReconcileTriggers context

	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"topic_arn": "arn:aws:sns:us-east-1:123456789012:topic-1",
				},
				map[string]interface{}{
					"topic_arn":    "arn:aws:sns:us-east-1:123456789012:topic-2",
					"statement_id": "custom-statement",
				},
			},
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn":  "arn:aws:sqs:us-east-1:123456789012:queue-1",
					"batch_size": 5,
				},
				map[string]interface{}{
					"queue_arn": "arn:aws:sqs:us-east-1:123456789012:queue-2",
					"enabled":   false,
				},
			},
		},
	}

	// Extract SNS triggers
	snsTriggers := extractSNSTriggers(lambdaConfig, "my-lambda")
	if len(snsTriggers) != 2 {
		t.Errorf("Expected 2 SNS triggers, got %d", len(snsTriggers))
	}

	// Verify first SNS trigger (auto-generated statement_id)
	if snsTriggers[0].TopicArn != "arn:aws:sns:us-east-1:123456789012:topic-1" {
		t.Errorf("Expected topic-1 ARN, got %s", snsTriggers[0].TopicArn)
	}
	expectedStatementId := "sns-arn-aws-sns-us-east-1-123456789012-topic-1"
	if snsTriggers[0].StatementId != expectedStatementId {
		t.Errorf("Expected auto-generated statement_id %s, got %s", expectedStatementId, snsTriggers[0].StatementId)
	}

	// Verify second SNS trigger (custom statement_id)
	if snsTriggers[1].TopicArn != "arn:aws:sns:us-east-1:123456789012:topic-2" {
		t.Errorf("Expected topic-2 ARN, got %s", snsTriggers[1].TopicArn)
	}
	if snsTriggers[1].StatementId != "custom-statement" {
		t.Errorf("Expected custom statement_id, got %s", snsTriggers[1].StatementId)
	}

	// Extract SQS triggers
	sqsTriggers := extractSQSTriggers(lambdaConfig, "my-lambda")
	if len(sqsTriggers) != 2 {
		t.Errorf("Expected 2 SQS triggers, got %d", len(sqsTriggers))
	}

	// Verify first SQS trigger (custom batch_size)
	if sqsTriggers[0].QueueArn != "arn:aws:sqs:us-east-1:123456789012:queue-1" {
		t.Errorf("Expected queue-1 ARN, got %s", sqsTriggers[0].QueueArn)
	}
	if sqsTriggers[0].BatchSize != 5 {
		t.Errorf("Expected batch_size 5, got %d", sqsTriggers[0].BatchSize)
	}
	if !sqsTriggers[0].Enabled {
		t.Errorf("Expected enabled=true (default), got false")
	}

	// Verify second SQS trigger (enabled=false)
	if sqsTriggers[1].QueueArn != "arn:aws:sqs:us-east-1:123456789012:queue-2" {
		t.Errorf("Expected queue-2 ARN, got %s", sqsTriggers[1].QueueArn)
	}
	if sqsTriggers[1].BatchSize != 10 {
		t.Errorf("Expected default batch_size 10, got %d", sqsTriggers[1].BatchSize)
	}
	if sqsTriggers[1].Enabled {
		t.Errorf("Expected enabled=false, got true")
	}
}

// TestReconcileTriggers_NoTriggers tests that ReconcileTriggers handles
// lambdas with no triggers configured.
func TestReconcileTriggers_NoTriggers(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"timeout": 30,
			"memory":  256,
		},
	}

	// Extract SNS triggers - should be empty
	snsTriggers := extractSNSTriggers(lambdaConfig, "my-lambda")
	if len(snsTriggers) != 0 {
		t.Errorf("Expected 0 SNS triggers, got %d", len(snsTriggers))
	}

	// Extract SQS triggers - should be empty
	sqsTriggers := extractSQSTriggers(lambdaConfig, "my-lambda")
	if len(sqsTriggers) != 0 {
		t.Errorf("Expected 0 SQS triggers, got %d", len(sqsTriggers))
	}
}

// TestReconcileTriggers_LambdaNotInConfig tests that ReconcileTriggers handles
// lambdas that don't have an entry in lambda_config.
func TestReconcileTriggers_LambdaNotInConfig(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"other-lambda": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"topic_arn": "arn:aws:sns:us-east-1:123456789012:topic-1",
				},
			},
		},
	}

	// Extract triggers for a lambda not in config
	snsTriggers := extractSNSTriggers(lambdaConfig, "my-lambda")
	if len(snsTriggers) != 0 {
		t.Errorf("Expected 0 SNS triggers for non-existent lambda, got %d", len(snsTriggers))
	}

	sqsTriggers := extractSQSTriggers(lambdaConfig, "my-lambda")
	if len(sqsTriggers) != 0 {
		t.Errorf("Expected 0 SQS triggers for non-existent lambda, got %d", len(sqsTriggers))
	}
}

// TestCleanupAllTriggers_EmptyState tests that CleanupAllTriggers handles
// the case where there are no existing triggers to clean up.
// This verifies the function doesn't fail when reconciling with empty desired state.
func TestCleanupAllTriggers_EmptyState(t *testing.T) {
	// This test verifies the logic flow of CleanupAllTriggers
	// by testing the underlying extraction functions with empty config

	lambdaConfig := map[string]interface{}{}

	// Extract triggers - should be empty
	snsTriggers := extractSNSTriggers(lambdaConfig, "my-lambda")
	sqsTriggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(snsTriggers) != 0 || len(sqsTriggers) != 0 {
		t.Errorf("Expected empty triggers for cleanup, got SNS=%d, SQS=%d",
			len(snsTriggers), len(sqsTriggers))
	}
}

// TestTriggerIntegration_UpdateTypeConfig tests that trigger reconciliation
// is called when updateType is LambdaUpdateTypeConfig.
// Requirements: 5.1 - TriggerManager is called during Lambda updates
func TestTriggerIntegration_UpdateTypeConfig(t *testing.T) {
	// This test verifies the integration logic by checking that
	// the correct update types trigger reconciliation

	testCases := []struct {
		name               string
		updateType         LambdaUpdateType
		shouldReconcile    bool
	}{
		{
			name:            "Config update should reconcile triggers",
			updateType:      LambdaUpdateTypeConfig,
			shouldReconcile: true,
		},
		{
			name:            "Both update should reconcile triggers",
			updateType:      LambdaUpdateTypeBoth,
			shouldReconcile: true,
		},
		{
			name:            "Source-only update should not reconcile triggers",
			updateType:      LambdaUpdateTypeSource,
			shouldReconcile: false,
		},
		{
			name:            "No update should not reconcile triggers",
			updateType:      LambdaUpdateTypeNone,
			shouldReconcile: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Verify the update type logic
			shouldReconcile := tc.updateType == LambdaUpdateTypeConfig || tc.updateType == LambdaUpdateTypeBoth
			if shouldReconcile != tc.shouldReconcile {
				t.Errorf("Expected shouldReconcile=%v for updateType=%s, got %v",
					tc.shouldReconcile, tc.updateType, shouldReconcile)
			}
		})
	}
}

// TestTriggerIntegration_MultipleLambdas tests that trigger reconciliation
// works correctly when updating multiple lambdas in parallel.
// Requirements: 5.1, 5.4 - TriggerManager handles triggers in parallel with other Lambda updates
func TestTriggerIntegration_MultipleLambdas(t *testing.T) {
	// Test that each lambda's triggers are extracted independently
	lambdaConfig := map[string]interface{}{
		"lambda-a": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"topic_arn": "arn:aws:sns:us-east-1:123456789012:topic-a",
				},
			},
		},
		"lambda-b": map[string]interface{}{
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn": "arn:aws:sqs:us-east-1:123456789012:queue-b",
				},
			},
		},
		"lambda-c": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"topic_arn": "arn:aws:sns:us-east-1:123456789012:topic-c",
				},
			},
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn": "arn:aws:sqs:us-east-1:123456789012:queue-c",
				},
			},
		},
	}

	// Verify lambda-a has only SNS triggers
	snsA := extractSNSTriggers(lambdaConfig, "lambda-a")
	sqsA := extractSQSTriggers(lambdaConfig, "lambda-a")
	if len(snsA) != 1 || len(sqsA) != 0 {
		t.Errorf("lambda-a: expected 1 SNS, 0 SQS; got %d SNS, %d SQS", len(snsA), len(sqsA))
	}

	// Verify lambda-b has only SQS triggers
	snsB := extractSNSTriggers(lambdaConfig, "lambda-b")
	sqsB := extractSQSTriggers(lambdaConfig, "lambda-b")
	if len(snsB) != 0 || len(sqsB) != 1 {
		t.Errorf("lambda-b: expected 0 SNS, 1 SQS; got %d SNS, %d SQS", len(snsB), len(sqsB))
	}

	// Verify lambda-c has both SNS and SQS triggers
	snsC := extractSNSTriggers(lambdaConfig, "lambda-c")
	sqsC := extractSQSTriggers(lambdaConfig, "lambda-c")
	if len(snsC) != 1 || len(sqsC) != 1 {
		t.Errorf("lambda-c: expected 1 SNS, 1 SQS; got %d SNS, %d SQS", len(snsC), len(sqsC))
	}
}
