// internal/resources/lambda_create_retry.go
package resources

import (
	"context"
	"strings"
	"time"

	"terraform-provider-conveyor-belt/internal/utils"
)

// Retry budget for Lambda CreateFunction when the freshly-created IAM execution
// role has not yet propagated to the Lambda service.
//
// IAM is eventually consistent: a role created milliseconds before CreateFunction
// is frequently not yet assumable by lambda.amazonaws.com. AWS routinely takes
// 30-60s (occasionally longer) to propagate a brand-new role. The previous loop
// used linear backoff (1+2+...+10 = ~55s) which was too short for cold
// environments and fell off the end of the loop, surfacing:
//
//	InvalidParameterValueException: The role defined for the function cannot be assumed by Lambda.
//
// These constants give a true capped-exponential backoff covering ~2 minutes,
// which comfortably clears IAM propagation for a first Lambda in a new env.
const (
	lambdaCreateMaxAttempts   = 15
	lambdaCreateBaseBackoff   = 1 * time.Second
	lambdaCreateMaxBackoff    = 15 * time.Second
	lambdaCreateBackoffFactor = 2
)

// freshRolePropagationDelay is a short, one-time pre-flight pause applied right
// after a brand-new Lambda execution role is created, before the first
// CreateFunction call. It lets the common cold-start propagation happen once, up
// front, instead of surfacing as several failed CreateFunction attempts and
// scary InvalidParameterValueException log lines.
//
// It is intentionally modest: the capped-exponential CreateFunction retry
// (see lambdaCreateBackoff) remains the real safety net for the rare case where
// propagation takes longer than this. This delay is ONLY applied when we
// actually created a new role — never on the EntityAlreadyExists/GetRole path,
// where the role is already propagated and any wait would be pure latency.
const freshRolePropagationDelay = 8 * time.Second

// waitForFreshRolePropagation pauses once for freshRolePropagationDelay after a
// newly created role, before the first CreateFunction attempt. Callers must only
// invoke this on the create path, not the already-exists path.
func waitForFreshRolePropagation(ctx context.Context, roleName string) {
	utils.Info(ctx, "Waiting for fresh IAM role to propagate before Lambda creation", map[string]interface{}{
		"role_name": roleName,
		"delay":     freshRolePropagationDelay.String(),
	})
	time.Sleep(freshRolePropagationDelay)
}

// isRoleNotYetPropagatedErr reports whether a CreateFunction error is a transient
// IAM propagation error that should be retried (role not yet assumable by Lambda,
// KMS grant not yet valid, or principal ARN not yet resolvable).
func isRoleNotYetPropagatedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "cannot be assumed by Lambda") ||
		strings.Contains(msg, "KMS key is invalid for CreateGrant") ||
		strings.Contains(msg, "ARN does not refer to a valid principal")
}

// isSignatureExpiredErr reports whether an error is a presigned-code upload
// signature expiry (large zip exceeded the 5-minute window).
func isSignatureExpiredErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "InvalidSignatureException") ||
		strings.Contains(msg, "Signature expired")
}

// lambdaCreateBackoff returns the capped-exponential sleep duration for a given
// zero-based attempt: base * factor^attempt, clamped to lambdaCreateMaxBackoff.
func lambdaCreateBackoff(attempt int) time.Duration {
	d := lambdaCreateBaseBackoff
	for i := 0; i < attempt; i++ {
		d *= lambdaCreateBackoffFactor
		if d >= lambdaCreateMaxBackoff {
			return lambdaCreateMaxBackoff
		}
	}
	if d > lambdaCreateMaxBackoff {
		return lambdaCreateMaxBackoff
	}
	return d
}

// waitForRolePropagation sleeps with capped-exponential backoff before the next
// CreateFunction attempt and logs the retry. Kept as a helper so both the
// parallel and single-resource create paths behave identically.
func waitForRolePropagation(ctx context.Context, lambdaName string, attempt int, err error) {
	backoff := lambdaCreateBackoff(attempt)
	utils.Info(ctx, "IAM role not yet propagated, retrying CreateFunction...", map[string]interface{}{
		"lambda":       lambdaName,
		"attempt":      attempt + 1,
		"max_attempts": lambdaCreateMaxAttempts,
		"backoff":      backoff.String(),
		"error_detail": err.Error(),
	})
	time.Sleep(backoff)
}
