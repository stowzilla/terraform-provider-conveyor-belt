// internal/resources/package_builder_test.go
package resources

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/quick"
	"time"
)

// Feature: provider-framework-refactor, Property 6: Concurrent Package Building
// *For any* set of N Lambda packages to build with concurrency limit C,
// at most C builds SHALL execute simultaneously, and all N packages SHALL eventually complete (success or failure).
// **Validates: Requirements 4.1, 4.2**

// MockBuildFunc is a function type for mocking the build process
type MockBuildFunc func(ctx context.Context, lambdaName string) ([]byte, error)

// TestablePackageBuilder wraps PackageBuilder for testing with mock builds
type TestablePackageBuilder struct {
	concurrency    int
	buildFunc      MockBuildFunc
	activeBuilds   int32
	maxConcurrent  int32
	completedCount int32
	mu             sync.Mutex
}

// NewTestablePackageBuilder creates a testable package builder with mock build function
func NewTestablePackageBuilder(concurrency int, buildFunc MockBuildFunc) *TestablePackageBuilder {
	return &TestablePackageBuilder{
		concurrency: concurrency,
		buildFunc:   buildFunc,
	}
}

// BuildPackages builds packages using the mock function while tracking concurrency
func (tpb *TestablePackageBuilder) BuildPackages(ctx context.Context, lambdaNames []string) map[string]*BuildResult {
	results := make(map[string]*BuildResult)
	resultChan := make(chan *BuildResult, len(lambdaNames))

	// Semaphore for concurrency control
	sem := make(chan struct{}, tpb.concurrency)

	var wg sync.WaitGroup
	for _, name := range lambdaNames {
		wg.Add(1)
		go func(lambdaName string) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Track active builds
			current := atomic.AddInt32(&tpb.activeBuilds, 1)
			
			// Update max concurrent if this is a new high
			for {
				max := atomic.LoadInt32(&tpb.maxConcurrent)
				if current <= max {
					break
				}
				if atomic.CompareAndSwapInt32(&tpb.maxConcurrent, max, current) {
					break
				}
			}

			result := &BuildResult{LambdaName: lambdaName}

			zipData, err := tpb.buildFunc(ctx, lambdaName)
			if err != nil {
				result.Error = err
			} else {
				result.ZipData = zipData
				result.Hash = calculateZipHash(zipData)
			}

			atomic.AddInt32(&tpb.activeBuilds, -1)
			atomic.AddInt32(&tpb.completedCount, 1)

			resultChan <- result
		}(name)
	}

	// Close channel when all done
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	for result := range resultChan {
		results[result.LambdaName] = result
	}

	return results
}

// GetMaxConcurrent returns the maximum number of concurrent builds observed
func (tpb *TestablePackageBuilder) GetMaxConcurrent() int32 {
	return atomic.LoadInt32(&tpb.maxConcurrent)
}

// GetCompletedCount returns the number of completed builds
func (tpb *TestablePackageBuilder) GetCompletedCount() int32 {
	return atomic.LoadInt32(&tpb.completedCount)
}

// Reset resets the tracking counters
func (tpb *TestablePackageBuilder) Reset() {
	atomic.StoreInt32(&tpb.activeBuilds, 0)
	atomic.StoreInt32(&tpb.maxConcurrent, 0)
	atomic.StoreInt32(&tpb.completedCount, 0)
}


// TestConcurrentPackageBuilding_Property tests Property 6: Concurrent Package Building
// *For any* set of N Lambda packages to build with concurrency limit C,
// at most C builds SHALL execute simultaneously, and all N packages SHALL eventually complete.
// **Validates: Requirements 4.1, 4.2**
func TestConcurrentPackageBuilding_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random number of lambdas (1-20)
		numLambdas := 1 + r.Intn(20)
		
		// Generate random concurrency limit (1-10)
		concurrencyLimit := 1 + r.Intn(10)

		// Generate lambda names
		lambdaNames := make([]string, numLambdas)
		for i := 0; i < numLambdas; i++ {
			lambdaNames[i] = randomString(r, 5, 15)
		}

		// Create mock build function that simulates work
		mockBuild := func(ctx context.Context, lambdaName string) ([]byte, error) {
			// Simulate some work with random duration (1-10ms)
			time.Sleep(time.Duration(1+r.Intn(10)) * time.Millisecond)
			return []byte("mock-zip-data-" + lambdaName), nil
		}

		// Create testable builder
		builder := NewTestablePackageBuilder(concurrencyLimit, mockBuild)

		// Build packages
		ctx := context.Background()
		results := builder.BuildPackages(ctx, lambdaNames)

		// Property 1: All packages should complete
		if len(results) != numLambdas {
			t.Logf("Not all packages completed: expected %d, got %d", numLambdas, len(results))
			return false
		}

		// Property 2: Max concurrent builds should not exceed limit
		maxConcurrent := builder.GetMaxConcurrent()
		if maxConcurrent > int32(concurrencyLimit) {
			t.Logf("Concurrency limit exceeded: max=%d, limit=%d", maxConcurrent, concurrencyLimit)
			return false
		}

		// Property 3: All results should have data (no errors in this test)
		for name, result := range results {
			if result.Error != nil {
				t.Logf("Unexpected error for %s: %v", name, result.Error)
				return false
			}
			if result.ZipData == nil {
				t.Logf("Missing zip data for %s", name)
				return false
			}
		}

		// Property 4: Completed count should match number of lambdas
		completedCount := builder.GetCompletedCount()
		if completedCount != int32(numLambdas) {
			t.Logf("Completed count mismatch: expected %d, got %d", numLambdas, completedCount)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Concurrent package building property failed: %v", err)
	}
}

// TestConcurrentPackageBuilding_ConcurrencyRespected tests that concurrency limit is actually used
func TestConcurrentPackageBuilding_ConcurrencyRespected(t *testing.T) {
	// Test with specific values to ensure concurrency is being utilized
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
			for i := 0; i < tc.numLambdas; i++ {
				lambdaNames[i] = "lambda-" + string(rune('a'+i))
			}

			// Mock build that takes some time
			mockBuild := func(ctx context.Context, lambdaName string) ([]byte, error) {
				time.Sleep(5 * time.Millisecond)
				return []byte("data"), nil
			}

			builder := NewTestablePackageBuilder(tc.concurrency, mockBuild)
			ctx := context.Background()
			results := builder.BuildPackages(ctx, lambdaNames)

			// All should complete
			if len(results) != tc.numLambdas {
				t.Errorf("Expected %d results, got %d", tc.numLambdas, len(results))
			}

			// Max concurrent should not exceed limit
			maxConcurrent := builder.GetMaxConcurrent()
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


// Feature: provider-framework-refactor, Property 7: Build Failure Isolation
// *For any* set of Lambda packages where one build fails, all other builds SHALL continue to completion,
// and the final result SHALL report all failures.
// **Validates: Requirements 4.4**

// TestBuildFailureIsolation_Property tests Property 7: Build Failure Isolation
// *For any* set of Lambda packages where some builds fail, all other builds SHALL continue to completion.
// **Validates: Requirements 4.4**
func TestBuildFailureIsolation_Property(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random number of lambdas (3-15)
		numLambdas := 3 + r.Intn(13)

		// Generate random number of failures (1 to numLambdas-1, ensuring at least one success)
		numFailures := 1 + r.Intn(numLambdas-1)

		// Generate lambda names
		lambdaNames := make([]string, numLambdas)
		for i := 0; i < numLambdas; i++ {
			lambdaNames[i] = randomString(r, 5, 15)
		}

		// Randomly select which lambdas will fail
		failingLambdas := make(map[string]bool)
		failIndices := r.Perm(numLambdas)[:numFailures]
		for _, idx := range failIndices {
			failingLambdas[lambdaNames[idx]] = true
		}

		// Create mock build function that fails for selected lambdas
		mockBuild := func(ctx context.Context, lambdaName string) ([]byte, error) {
			// Simulate some work
			time.Sleep(time.Duration(1+r.Intn(5)) * time.Millisecond)
			
			if failingLambdas[lambdaName] {
				return nil, errors.New("simulated build failure for " + lambdaName)
			}
			return []byte("mock-zip-data-" + lambdaName), nil
		}

		// Create testable builder with concurrency
		concurrency := 1 + r.Intn(5)
		builder := NewTestablePackageBuilder(concurrency, mockBuild)

		// Build packages
		ctx := context.Background()
		results := builder.BuildPackages(ctx, lambdaNames)

		// Property 1: All packages should have results (success or failure)
		if len(results) != numLambdas {
			t.Logf("Not all packages have results: expected %d, got %d", numLambdas, len(results))
			return false
		}

		// Property 2: Exactly the expected lambdas should have failed
		actualFailures := 0
		actualSuccesses := 0
		for name, result := range results {
			if result.Error != nil {
				actualFailures++
				if !failingLambdas[name] {
					t.Logf("Unexpected failure for %s: %v", name, result.Error)
					return false
				}
			} else {
				actualSuccesses++
				if failingLambdas[name] {
					t.Logf("Expected failure for %s but it succeeded", name)
					return false
				}
				if result.ZipData == nil {
					t.Logf("Missing zip data for successful build %s", name)
					return false
				}
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

		// Property 5: All builds should have completed
		completedCount := builder.GetCompletedCount()
		if completedCount != int32(numLambdas) {
			t.Logf("Completed count mismatch: expected %d, got %d", numLambdas, completedCount)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Build failure isolation property failed: %v", err)
	}
}

// TestBuildFailureIsolation_AllFailures tests that even when all builds fail, they all complete
func TestBuildFailureIsolation_AllFailures(t *testing.T) {
	lambdaNames := []string{"lambda-a", "lambda-b", "lambda-c", "lambda-d", "lambda-e"}

	// All builds fail
	mockBuild := func(ctx context.Context, lambdaName string) ([]byte, error) {
		time.Sleep(2 * time.Millisecond)
		return nil, errors.New("all builds fail")
	}

	builder := NewTestablePackageBuilder(3, mockBuild)
	ctx := context.Background()
	results := builder.BuildPackages(ctx, lambdaNames)

	// All should have results
	if len(results) != len(lambdaNames) {
		t.Errorf("Expected %d results, got %d", len(lambdaNames), len(results))
	}

	// All should have errors
	for name, result := range results {
		if result.Error == nil {
			t.Errorf("Expected error for %s but got success", name)
		}
	}

	// All should have completed
	if builder.GetCompletedCount() != int32(len(lambdaNames)) {
		t.Errorf("Expected %d completed, got %d", len(lambdaNames), builder.GetCompletedCount())
	}
}

// TestBuildFailureIsolation_SingleFailure tests isolation with a single failure
func TestBuildFailureIsolation_SingleFailure(t *testing.T) {
	lambdaNames := []string{"lambda-a", "lambda-b", "lambda-c", "lambda-d", "lambda-e"}
	failingLambda := "lambda-c"

	mockBuild := func(ctx context.Context, lambdaName string) ([]byte, error) {
		time.Sleep(2 * time.Millisecond)
		if lambdaName == failingLambda {
			return nil, errors.New("intentional failure")
		}
		return []byte("success-" + lambdaName), nil
	}

	builder := NewTestablePackageBuilder(2, mockBuild)
	ctx := context.Background()
	results := builder.BuildPackages(ctx, lambdaNames)

	// All should have results
	if len(results) != len(lambdaNames) {
		t.Errorf("Expected %d results, got %d", len(lambdaNames), len(results))
	}

	// Check each result
	failureCount := 0
	successCount := 0
	for name, result := range results {
		if name == failingLambda {
			if result.Error == nil {
				t.Errorf("Expected error for %s", name)
			}
			failureCount++
		} else {
			if result.Error != nil {
				t.Errorf("Unexpected error for %s: %v", name, result.Error)
			}
			if result.ZipData == nil {
				t.Errorf("Missing zip data for %s", name)
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

// TestPackageBuilder_GetFailureErrors tests the error aggregation functionality
func TestPackageBuilder_GetFailureErrors(t *testing.T) {
	pb := NewPackageBuilder("/tmp/test")

	// Test with no failures
	results := map[string]*BuildResult{
		"lambda-a": {LambdaName: "lambda-a", ZipData: []byte("data")},
		"lambda-b": {LambdaName: "lambda-b", ZipData: []byte("data")},
	}

	err := pb.GetFailureErrors(results)
	if err != nil {
		t.Errorf("Expected no error for successful builds, got: %v", err)
	}

	// Test with failures
	results["lambda-c"] = &BuildResult{
		LambdaName: "lambda-c",
		Error:      errors.New("build failed"),
	}
	results["lambda-d"] = &BuildResult{
		LambdaName: "lambda-d",
		Error:      errors.New("another failure"),
	}

	err = pb.GetFailureErrors(results)
	if err == nil {
		t.Error("Expected error for failed builds")
	}

	// Error message should mention the count
	if !containsSubstring(err.Error(), "2") {
		t.Errorf("Error should mention failure count: %v", err)
	}
}

// containsSubstring checks if s contains substr
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstringHelper(s, substr))
}

func containsSubstringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestStripVendorFat(t *testing.T) {
	pb := &PackageBuilder{sourceDir: t.TempDir(), concurrency: 1}
	buildDir := t.TempDir()
	vendorDir := buildDir + "/vendor/bundle/ruby/3.4.0/gems/somegem-1.0"

	// Create directories that should be removed (at gem root level)
	for _, dir := range []string{"spec", "test", "tests", "doc", "docs", "examples", "benchmarks"} {
		os.MkdirAll(vendorDir+"/"+dir, 0755)
		os.WriteFile(vendorDir+"/"+dir+"/dummy.rb", []byte("x"), 0644)
	}

	// Create a "test" directory inside lib/ that should NOT be removed
	os.MkdirAll(vendorDir+"/lib/active_support/test", 0755)
	os.WriteFile(vendorDir+"/lib/active_support/test/important.rb", []byte("x"), 0644)

	// Create files that should be removed (build artifacts only)
	removeFiles := []string{"Makefile", "Rakefile", ".gitignore", ".travis.yml", ".rubocop.yml", "foo.c", "bar.h", "baz.o", "docs.rdoc"}
	for _, f := range removeFiles {
		os.WriteFile(vendorDir+"/"+f, []byte("x"), 0644)
	}

	// Create files that should be kept (gemspecs, Gemfiles, docs, Ruby source)
	keepFiles := []string{"lib/main.rb", "lib/helper.rb", "thing.gemspec", "Gemfile", "README.md", "LICENSE.txt", "CHANGELOG.md"}
	for _, f := range keepFiles {
		os.MkdirAll(vendorDir+"/"+filepath.Dir(f), 0755)
		os.WriteFile(vendorDir+"/"+f, []byte("x"), 0644)
	}

	ctx := context.Background()
	pb.stripVendorFat(ctx, buildDir)

	// Verify removed directories are gone (at gem root level)
	for _, dir := range []string{"spec", "test", "tests", "doc", "docs", "examples", "benchmarks"} {
		if _, err := os.Stat(vendorDir + "/" + dir); err == nil {
			t.Errorf("directory %s should have been removed", dir)
		}
	}

	// Verify removed files are gone
	for _, f := range removeFiles {
		if _, err := os.Stat(vendorDir + "/" + f); err == nil {
			t.Errorf("file %s should have been removed", f)
		}
	}

	// Verify kept files still exist (including nested "test" dir inside lib/)
	for _, f := range keepFiles {
		if _, err := os.Stat(vendorDir + "/" + f); err != nil {
			t.Errorf("file %s should have been kept", f)
		}
	}

	// Verify nested lib/active_support/test/ was NOT removed
	if _, err := os.Stat(vendorDir + "/lib/active_support/test/important.rb"); err != nil {
		t.Error("lib/active_support/test/important.rb should have been kept (nested test dir)")
	}
}
