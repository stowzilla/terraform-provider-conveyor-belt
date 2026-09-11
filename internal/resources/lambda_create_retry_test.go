// internal/resources/lambda_create_retry_test.go
package resources

import (
	"fmt"
	"testing"
	"time"
)

func TestLambdaCreateBackoffIsCappedExponential(t *testing.T) {
	// attempt 0 -> base; then doubles until it hits the cap and stays there.
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, lambdaCreateMaxBackoff}, // 16s clamps to cap
		{5, lambdaCreateMaxBackoff},
		{14, lambdaCreateMaxBackoff},
	}

	for _, tc := range cases {
		if got := lambdaCreateBackoff(tc.attempt); got != tc.want {
			t.Errorf("lambdaCreateBackoff(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestLambdaCreateBudgetCoversIamPropagation(t *testing.T) {
	// The whole point of the fix: total backoff across all attempts must be well
	// over a minute so a cold environment's fresh IAM role has time to propagate.
	// The previous linear loop only covered ~55s and fell short.
	var total time.Duration
	for attempt := 0; attempt < lambdaCreateMaxAttempts; attempt++ {
		total += lambdaCreateBackoff(attempt)
	}

	if total < 90*time.Second {
		t.Errorf("total retry budget = %s, want >= 90s to clear IAM propagation", total)
	}
}

func TestIsRoleNotYetPropagatedErr(t *testing.T) {
	retryable := []string{
		"InvalidParameterValueException: The role defined for the function cannot be assumed by Lambda.",
		"InvalidParameterValueException: The KMS key is invalid for CreateGrant",
		"InvalidParameterValueException: The provided ARN does not refer to a valid principal",
	}
	for _, msg := range retryable {
		if !isRoleNotYetPropagatedErr(fmt.Errorf("%s", msg)) {
			t.Errorf("expected retryable for %q", msg)
		}
	}

	notRetryable := []string{
		"AccessDeniedException: not authorized",
		"ResourceConflictException: Function already exist",
		"",
	}
	for _, msg := range notRetryable {
		if isRoleNotYetPropagatedErr(fmt.Errorf("%s", msg)) {
			t.Errorf("expected non-retryable for %q", msg)
		}
	}

	if isRoleNotYetPropagatedErr(nil) {
		t.Error("nil error must not be retryable")
	}
}

func TestFreshRolePropagationDelayIsSaneAndSmall(t *testing.T) {
	// The pre-flight pause must be positive so it actually waits for propagation.
	if freshRolePropagationDelay <= 0 {
		t.Fatalf("freshRolePropagationDelay = %s, want > 0", freshRolePropagationDelay)
	}

	// It is a one-time nicety, not the primary safety net. It should stay well
	// below the full capped-exponential CreateFunction retry budget so the retry
	// loop remains the dominant mechanism for slow propagation.
	var retryBudget time.Duration
	for attempt := 0; attempt < lambdaCreateMaxAttempts; attempt++ {
		retryBudget += lambdaCreateBackoff(attempt)
	}
	if freshRolePropagationDelay >= retryBudget {
		t.Errorf("freshRolePropagationDelay = %s, want << retry budget %s", freshRolePropagationDelay, retryBudget)
	}

	// Guard against someone bumping it into "adds noticeable latency to every
	// apply that creates a role" territory.
	if freshRolePropagationDelay > 15*time.Second {
		t.Errorf("freshRolePropagationDelay = %s is too large; keep the pre-flight pause modest", freshRolePropagationDelay)
	}
}

func TestIsSignatureExpiredErr(t *testing.T) {
	if !isSignatureExpiredErr(fmt.Errorf("InvalidSignatureException: Signature expired")) {
		t.Error("expected signature-expired match")
	}
	if isSignatureExpiredErr(fmt.Errorf("some other error")) {
		t.Error("unexpected signature-expired match")
	}
	if isSignatureExpiredErr(nil) {
		t.Error("nil error must not match")
	}
}
