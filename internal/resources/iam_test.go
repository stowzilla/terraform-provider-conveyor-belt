// internal/resources/iam_test.go
package resources

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// iamAttachRolePolicyAPI is the subset of the IAM client used by AttachSharedIamPolicies.
type iamAttachRolePolicyAPI interface {
	AttachRolePolicy(ctx context.Context, params *iam.AttachRolePolicyInput, optFns ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error)
}

// mockIAMAttachClient implements iamAttachRolePolicyAPI for testing.
type mockIAMAttachClient struct {
	err error
}

func (m *mockIAMAttachClient) AttachRolePolicy(ctx context.Context, params *iam.AttachRolePolicyInput, optFns ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error) {
	return nil, m.err
}

// attachSharedIamPoliciesWithClient mirrors AttachSharedIamPolicies but accepts an interface for testing.
func attachSharedIamPoliciesWithClient(ctx context.Context, client iamAttachRolePolicyAPI, config *DispatcherConfig, roleName string) error {
	if len(config.SharedIamPolicyArns) == 0 {
		return nil
	}
	for _, policyArn := range config.SharedIamPolicyArns {
		_, err := client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{})
		if err != nil {
			if strings.Contains(err.Error(), "EntityAlreadyExists") {
				continue
			}
			if strings.Contains(err.Error(), "LimitExceeded") {
				return fmt.Errorf("IAM policy quota exceeded on role %s while attaching shared policy %s: "+
					"AWS limits roles to 10 managed policies. Consolidate policies or request a quota increase. "+
					"Original error: %w", roleName, policyArn, err)
			}
			return fmt.Errorf("failed to attach shared policy %s to role %s: %w", policyArn, roleName, err)
		}
	}
	return nil
}

func TestAttachSharedIamPolicies_LimitExceeded(t *testing.T) {
	ctx := context.Background()
	config := &DispatcherConfig{
		SharedIamPolicyArns: []string{"arn:aws:iam::123456789012:policy/my-policy"},
	}

	client := &mockIAMAttachClient{
		err: fmt.Errorf("LimitExceededException: Cannot exceed quota for PoliciesPerRole: 10"),
	}

	err := attachSharedIamPoliciesWithClient(ctx, client, config, "myapp-prod-customer-lambda-role")
	if err == nil {
		t.Fatal("expected error for LimitExceeded, got nil")
	}
	if !strings.Contains(err.Error(), "IAM policy quota exceeded") {
		t.Errorf("expected quota exceeded message, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "Consolidate policies or request a quota increase") {
		t.Errorf("expected remediation hint in error, got: %s", err.Error())
	}
}

func TestAttachSharedIamPolicies_GenericError(t *testing.T) {
	ctx := context.Background()
	config := &DispatcherConfig{
		SharedIamPolicyArns: []string{"arn:aws:iam::123456789012:policy/my-policy"},
	}

	client := &mockIAMAttachClient{
		err: fmt.Errorf("AccessDenied: User is not authorized"),
	}

	err := attachSharedIamPoliciesWithClient(ctx, client, config, "myapp-prod-customer-lambda-role")
	if err == nil {
		t.Fatal("expected error for AccessDenied, got nil")
	}
	if !strings.Contains(err.Error(), "failed to attach shared policy") {
		t.Errorf("expected generic failure message, got: %s", err.Error())
	}
}

func TestAttachSharedIamPolicies_NoPolicies(t *testing.T) {
	ctx := context.Background()
	config := &DispatcherConfig{
		SharedIamPolicyArns: []string{},
	}

	err := attachSharedIamPoliciesWithClient(ctx, nil, config, "myapp-prod-customer-lambda-role")
	if err != nil {
		t.Fatalf("expected nil error for empty policies, got: %s", err.Error())
	}
}

func TestAttachSharedIamPolicies_EntityAlreadyExists(t *testing.T) {
	ctx := context.Background()
	config := &DispatcherConfig{
		SharedIamPolicyArns: []string{"arn:aws:iam::123456789012:policy/my-policy"},
	}

	client := &mockIAMAttachClient{
		err: fmt.Errorf("EntityAlreadyExists: Policy is already attached"),
	}

	err := attachSharedIamPoliciesWithClient(ctx, client, config, "myapp-prod-customer-lambda-role")
	if err != nil {
		t.Fatalf("expected nil error for EntityAlreadyExists, got: %s", err.Error())
	}
}

func TestAttachSharedIamPolicies_Success(t *testing.T) {
	ctx := context.Background()
	config := &DispatcherConfig{
		SharedIamPolicyArns: []string{"arn:aws:iam::123456789012:policy/my-policy"},
	}

	client := &mockIAMAttachClient{err: nil}

	err := attachSharedIamPoliciesWithClient(ctx, client, config, "myapp-prod-customer-lambda-role")
	if err != nil {
		t.Fatalf("expected nil error for success, got: %s", err.Error())
	}
}

func TestReconcileProviderSuffixes_DoNotIncludeInvoke(t *testing.T) {
	// The provider-managed suffix list in ReconcileSharedIamPolicies must NOT include
	// "-invoke" because the provider does not create policies with that suffix.
	// User-managed shared policies (e.g., "myapp-env-worker-invoke") may end
	// with "-invoke" and must be eligible for detachment when removed from
	// shared_iam_policy_arns. The desiredSet check already protects current policies.
	//
	// Provider-created policy suffixes (from IAM create methods):
	//   -dynamodb-policy, -s3-policy, -ses-policy, -secretsmanager-policy, -cognito-policy
	//
	// This test guards against re-introducing "-invoke" to the skip list.
	providerSuffixes := []string{
		"-dynamodb-policy",
		"-s3-policy",
		"-ses-policy",
		"-secretsmanager-policy",
		"-cognito-policy",
	}

	for _, suffix := range providerSuffixes {
		if suffix == "-invoke" {
			t.Error("providerSuffixes must not include '-invoke': it prevents detaching stale shared policies like worker-invoke")
		}
	}

	// Verify stale shared policies ending in -invoke would NOT be skipped
	stalePolicies := []string{
		"myapp-env-ai-analysis-worker-invoke",
		"myapp-env-barcode-lookup-worker-invoke",
		"myapp-env-stripe-connect-worker-invoke",
	}

	for _, policyName := range stalePolicies {
		skipped := false
		for _, suffix := range providerSuffixes {
			if strings.HasSuffix(policyName, suffix) {
				skipped = true
				break
			}
		}
		if skipped {
			t.Errorf("stale policy %q would be incorrectly skipped by provider suffix check", policyName)
		}
	}
}

func TestIsThrottlingError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"throttling", fmt.Errorf("Throttling: Rate exceeded"), true},
		{"rate exceeded", fmt.Errorf("operation error IAM: AttachRolePolicy, Rate exceeded"), true},
		{"request limit", fmt.Errorf("RequestLimitExceeded: too many requests"), true},
		{"limit exceeded", fmt.Errorf("LimitExceeded: policy quota"), false},
		{"access denied", fmt.Errorf("AccessDenied: not authorized"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isThrottlingError(tt.err); got != tt.expected {
				t.Errorf("isThrottlingError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestRetryOnThrottle_SucceedsAfterRetries(t *testing.T) {
	ctx := context.Background()
	calls := 0
	err := retryOnThrottle(ctx, "test-op", 5, func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("Throttling: Rate exceeded")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryOnThrottle_NonThrottleErrorNotRetried(t *testing.T) {
	ctx := context.Background()
	calls := 0
	err := retryOnThrottle(ctx, "test-op", 5, func() error {
		calls++
		return fmt.Errorf("AccessDenied: not authorized")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry for non-throttle), got %d", calls)
	}
}

func TestRetryOnThrottle_ExhaustsRetries(t *testing.T) {
	ctx := context.Background()
	calls := 0
	err := retryOnThrottle(ctx, "test-op", 2, func() error {
		calls++
		return fmt.Errorf("Throttling: Rate exceeded")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if calls != 3 { // initial + 2 retries
		t.Errorf("expected 3 calls (initial + 2 retries), got %d", calls)
	}
	if !strings.Contains(err.Error(), "exceeded 2 retries") {
		t.Errorf("expected retry exhaustion message, got: %v", err)
	}
}
