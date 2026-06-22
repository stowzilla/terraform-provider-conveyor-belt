// internal/resources/naming_test.go
package resources

import (
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
)

// Feature: provider-framework-refactor, Property 12: Resource Naming Correctness
// For any resource with app_name A, environment E, and name N, the AWS resource name:
// - SHALL be `{A}-{E}-{N}` (with appropriate type suffix)
// - SHALL NOT contain random characters
// - SHALL comply with AWS naming constraints
// - If exceeding length limits SHALL be truncated with a deterministic hash suffix
// **Validates: Requirements 9.1, 9.2, 9.3, 9.4**

// randomAlphanumericString generates a random alphanumeric string for testing
func randomAlphanumericString(r *rand.Rand, minLen, maxLen int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	length := minLen + r.Intn(maxLen-minLen+1)
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[r.Intn(len(charset))]
	}
	return string(result)
}

// TestNamingCorrectness_PatternFollowed tests that generated names follow the
// pattern {app_name}-{environment}-{name} for Lambda and Gateway resources.
// Property: For any valid inputs, the generated name follows the expected pattern
func TestNamingCorrectness_PatternFollowed(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random but valid name components
		appName := randomAlphanumericString(r, 3, 10)
		environment := randomAlphanumericString(r, 3, 8)
		name := randomAlphanumericString(r, 3, 15)

		// Test Lambda resource naming
		lambdaName := GenerateResourceName(appName, environment, name, ResourceTypeLambda)
		expectedPrefix := appName + "-" + environment + "-" + name

		// If not truncated, should match exactly
		if len(expectedPrefix) <= LambdaNameMaxLength {
			if lambdaName != expectedPrefix {
				t.Logf("Lambda name mismatch: expected %s, got %s", expectedPrefix, lambdaName)
				return false
			}
		}

		// Test Gateway resource naming (same pattern)
		gatewayName := GenerateResourceName(appName, environment, name, ResourceTypeGateway)
		if len(expectedPrefix) <= APIGatewayNameMaxLength {
			if gatewayName != expectedPrefix {
				t.Logf("Gateway name mismatch: expected %s, got %s", expectedPrefix, gatewayName)
				return false
			}
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Pattern followed property failed: %v", err)
	}
}

// TestNamingCorrectness_NoRandomSuffixes tests that generated names do not contain
// random suffixes - they are fully deterministic.
// Property: For any inputs, calling GenerateResourceName twice produces identical results
func TestNamingCorrectness_NoRandomSuffixes(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		appName := randomAlphanumericString(r, 3, 15)
		environment := randomAlphanumericString(r, 3, 10)
		name := randomAlphanumericString(r, 3, 20)

		resourceTypes := []ResourceType{
			ResourceTypeLambda,
			ResourceTypeGateway,
			ResourceTypeIAMRole,
			ResourceTypeLogGroup,
			ResourceTypeAlarm,
		}

		for _, rt := range resourceTypes {
			// Generate name twice
			name1 := GenerateResourceName(appName, environment, name, rt)
			name2 := GenerateResourceName(appName, environment, name, rt)

			// Names must be identical (no random component)
			if name1 != name2 {
				t.Logf("Non-deterministic naming for %s: %s != %s", rt, name1, name2)
				return false
			}
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("No random suffixes property failed: %v", err)
	}
}

// TestNamingCorrectness_AWSConstraintsCompliance tests that generated names comply
// with AWS naming constraints for each resource type.
// Property: For any inputs, generated names pass AWS validation
func TestNamingCorrectness_AWSConstraintsCompliance(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		appName := randomAlphanumericString(r, 3, 15)
		environment := randomAlphanumericString(r, 3, 10)
		name := randomAlphanumericString(r, 3, 20)

		// Test Lambda naming
		lambdaName := GenerateResourceName(appName, environment, name, ResourceTypeLambda)
		if err := ValidateLambdaName(lambdaName); err != nil {
			t.Logf("Lambda name validation failed: %s - %v", lambdaName, err)
			return false
		}

		// Test IAM Role naming
		iamRoleName := GenerateResourceName(appName, environment, name, ResourceTypeIAMRole)
		if err := ValidateIAMRoleName(iamRoleName); err != nil {
			t.Logf("IAM role name validation failed: %s - %v", iamRoleName, err)
			return false
		}

		// Test API Gateway naming
		gatewayName := GenerateResourceName(appName, environment, name, ResourceTypeGateway)
		if err := ValidateAPIGatewayName(gatewayName); err != nil {
			t.Logf("API Gateway name validation failed: %s - %v", gatewayName, err)
			return false
		}

		// Test CloudWatch Alarm naming
		alarmName := GenerateResourceName(appName, environment, name, ResourceTypeAlarm)
		if err := ValidateCloudWatchAlarmName(alarmName); err != nil {
			t.Logf("Alarm name validation failed: %s - %v", alarmName, err)
			return false
		}

		// Test Log Group naming
		logGroupName := GenerateResourceName(appName, environment, name, ResourceTypeLogGroup)
		if err := ValidateLogGroupName(logGroupName); err != nil {
			t.Logf("Log group name validation failed: %s - %v", logGroupName, err)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("AWS constraints compliance property failed: %v", err)
	}
}

// TestNamingCorrectness_TruncationWithHash tests that names exceeding AWS limits
// are truncated with a deterministic hash suffix.
// Property: For any name exceeding limits, truncation produces a valid name with hash
func TestNamingCorrectness_TruncationWithHash(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate long names that will exceed Lambda's 64 char limit
		appName := randomAlphanumericString(r, 20, 30)
		environment := randomAlphanumericString(r, 10, 15)
		name := randomAlphanumericString(r, 20, 30)

		// This should exceed 64 characters
		fullName := appName + "-" + environment + "-" + name

		if len(fullName) <= LambdaNameMaxLength {
			// Skip if not long enough to trigger truncation
			return true
		}

		lambdaName := GenerateResourceName(appName, environment, name, ResourceTypeLambda)

		// Verify truncation occurred
		if len(lambdaName) > LambdaNameMaxLength {
			t.Logf("Lambda name exceeds max length: %d > %d", len(lambdaName), LambdaNameMaxLength)
			return false
		}

		// Verify it's still valid
		if err := ValidateLambdaName(lambdaName); err != nil {
			t.Logf("Truncated Lambda name validation failed: %s - %v", lambdaName, err)
			return false
		}

		// Verify hash suffix is present (8 chars after last hyphen)
		lastHyphen := strings.LastIndex(lambdaName, "-")
		if lastHyphen == -1 {
			t.Logf("Truncated name missing hash suffix: %s", lambdaName)
			return false
		}

		hashSuffix := lambdaName[lastHyphen+1:]
		if len(hashSuffix) != HashSuffixLength {
			t.Logf("Hash suffix wrong length: expected %d, got %d", HashSuffixLength, len(hashSuffix))
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Truncation with hash property failed: %v", err)
	}
}

// TestNamingCorrectness_HashDeterminism tests that the hash suffix is deterministic
// based on the full intended name.
// Property: For any name, the hash suffix is always the same for the same input
func TestNamingCorrectness_HashDeterminism(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate long names that will trigger truncation
		appName := randomAlphanumericString(r, 20, 30)
		environment := randomAlphanumericString(r, 10, 15)
		name := randomAlphanumericString(r, 20, 30)

		// Generate the name multiple times
		name1 := GenerateResourceName(appName, environment, name, ResourceTypeLambda)
		name2 := GenerateResourceName(appName, environment, name, ResourceTypeLambda)
		name3 := GenerateResourceName(appName, environment, name, ResourceTypeLambda)

		// All should be identical
		if name1 != name2 || name2 != name3 {
			t.Logf("Hash not deterministic: %s, %s, %s", name1, name2, name3)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Hash determinism property failed: %v", err)
	}
}

// TestNamingCorrectness_DifferentInputsDifferentNames tests that different inputs
// produce different names (uniqueness).
// Property: For any two different input sets, the generated names are different
func TestNamingCorrectness_DifferentInputsDifferentNames(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate two different sets of inputs
		appName1 := randomAlphanumericString(r, 3, 15)
		environment1 := randomAlphanumericString(r, 3, 10)
		name1 := randomAlphanumericString(r, 3, 20)

		appName2 := randomAlphanumericString(r, 3, 15)
		environment2 := randomAlphanumericString(r, 3, 10)
		name2 := randomAlphanumericString(r, 3, 20)

		// Skip if inputs happen to be identical
		if appName1 == appName2 && environment1 == environment2 && name1 == name2 {
			return true
		}

		lambdaName1 := GenerateResourceName(appName1, environment1, name1, ResourceTypeLambda)
		lambdaName2 := GenerateResourceName(appName2, environment2, name2, ResourceTypeLambda)

		// Different inputs should produce different names
		if lambdaName1 == lambdaName2 {
			t.Logf("Name collision: %s == %s for different inputs", lambdaName1, lambdaName2)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Different inputs different names property failed: %v", err)
	}
}

// TestNamingCorrectness_IAMRolePattern tests that IAM role names follow the
// pattern {app_name}-{environment}-{name}-lambda-role
// Property: For any inputs, IAM role names include the "-lambda-role" suffix
func TestNamingCorrectness_IAMRolePattern(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		appName := randomAlphanumericString(r, 3, 10)
		environment := randomAlphanumericString(r, 3, 8)
		name := randomAlphanumericString(r, 3, 10)

		iamRoleName := GenerateResourceName(appName, environment, name, ResourceTypeIAMRole)

		// Should contain "-lambda-role" suffix (unless truncated)
		expectedSuffix := "-lambda-role"
		fullExpected := appName + "-" + environment + "-" + name + expectedSuffix

		if len(fullExpected) <= IAMRoleNameMaxLength {
			if !strings.HasSuffix(iamRoleName, expectedSuffix) {
				t.Logf("IAM role name missing suffix: %s", iamRoleName)
				return false
			}
		}

		// Should always be valid
		if err := ValidateIAMRoleName(iamRoleName); err != nil {
			t.Logf("IAM role name validation failed: %s - %v", iamRoleName, err)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("IAM role pattern property failed: %v", err)
	}
}

// TestSanitizeName tests the SanitizeName function
func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"my-app", "my-app"},
		{"my_app", "my-app"},
		{"My App", "my-app"},
		{"my--app", "my-app"},
		{"-my-app-", "my-app"},
		{"my@app!", "myapp"},
		{"MY_APP_NAME", "my-app-name"},
		{"  spaces  ", "spaces"},
		{"123-test", "123-test"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := SanitizeName(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestValidation tests the validation functions with specific examples
func TestValidation(t *testing.T) {
	t.Run("ValidateLambdaName", func(t *testing.T) {
		// Valid names
		validNames := []string{"my-lambda", "lambda123", "a", "my_lambda_func"}
		for _, name := range validNames {
			if err := ValidateLambdaName(name); err != nil {
				t.Errorf("ValidateLambdaName(%q) should be valid, got error: %v", name, err)
			}
		}

		// Invalid names
		invalidNames := []string{"", "-invalid", strings.Repeat("a", 65), "invalid name"}
		for _, name := range invalidNames {
			if err := ValidateLambdaName(name); err == nil {
				t.Errorf("ValidateLambdaName(%q) should be invalid", name)
			}
		}
	})

	t.Run("ValidateIAMRoleName", func(t *testing.T) {
		// Valid names
		validNames := []string{"my-role", "role123", "my_role", "role.name", "role@domain"}
		for _, name := range validNames {
			if err := ValidateIAMRoleName(name); err != nil {
				t.Errorf("ValidateIAMRoleName(%q) should be valid, got error: %v", name, err)
			}
		}

		// Invalid names
		invalidNames := []string{"", strings.Repeat("a", 65), "role name"}
		for _, name := range invalidNames {
			if err := ValidateIAMRoleName(name); err == nil {
				t.Errorf("ValidateIAMRoleName(%q) should be invalid", name)
			}
		}
	})
}

// TestTruncateWithHash tests the truncation function directly
func TestTruncateWithHash(t *testing.T) {
	t.Run("NoTruncationNeeded", func(t *testing.T) {
		name := "short-name"
		result := truncateWithHash(name, 64)
		if result != name {
			t.Errorf("truncateWithHash(%q, 64) = %q, want %q", name, result, name)
		}
	})

	t.Run("TruncationNeeded", func(t *testing.T) {
		name := strings.Repeat("a", 100)
		result := truncateWithHash(name, 64)

		if len(result) != 64 {
			t.Errorf("truncateWithHash result length = %d, want 64", len(result))
		}

		// Should end with hash suffix
		lastHyphen := strings.LastIndex(result, "-")
		if lastHyphen == -1 {
			t.Error("truncateWithHash result should contain hyphen before hash")
		}

		hashSuffix := result[lastHyphen+1:]
		if len(hashSuffix) != HashSuffixLength {
			t.Errorf("hash suffix length = %d, want %d", len(hashSuffix), HashSuffixLength)
		}
	})

	t.Run("DeterministicHash", func(t *testing.T) {
		name := strings.Repeat("a", 100)
		result1 := truncateWithHash(name, 64)
		result2 := truncateWithHash(name, 64)

		if result1 != result2 {
			t.Errorf("truncateWithHash not deterministic: %q != %q", result1, result2)
		}
	})

	t.Run("DifferentInputsDifferentHashes", func(t *testing.T) {
		name1 := strings.Repeat("a", 100)
		name2 := strings.Repeat("b", 100)
		result1 := truncateWithHash(name1, 64)
		result2 := truncateWithHash(name2, 64)

		if result1 == result2 {
			t.Errorf("truncateWithHash should produce different results for different inputs")
		}
	})
}
