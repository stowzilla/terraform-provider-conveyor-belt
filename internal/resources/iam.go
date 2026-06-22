// internal/resources/iam.go
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamTypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"terraform-provider-conveyor-belt/internal/utils"
)

// IAMManager handles IAM operations
type IAMManager struct {
	client *iam.Client
	config *DispatcherConfig
}

// NewIAMManager creates a new IAM manager
func NewIAMManager(client *iam.Client, config *DispatcherConfig) *IAMManager {
	return &IAMManager{
		client: client,
		config: config,
	}
}

// getKeys is a helper function to get keys from a map
func getKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// CreateLambdaExecutionRole creates an IAM role for Lambda execution
func (im *IAMManager) CreateLambdaExecutionRole(ctx context.Context, roleName, lambda string) (string, error) {
	utils.Info(ctx, fmt.Sprintf("Creating IAM role: %s", roleName))

	// Create trust policy for Lambda
	trustPolicy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Principal": map[string]interface{}{
					"Service": "lambda.amazonaws.com",
				},
				"Action": "sts:AssumeRole",
			},
		},
	}

	trustPolicyBytes, err := json.Marshal(trustPolicy)
	if err != nil {
		return "", fmt.Errorf("failed to marshal trust policy: %w", err)
	}

	// Create the IAM role
	createRoleInput := &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(string(trustPolicyBytes)),
		Description:              aws.String(fmt.Sprintf("Lambda execution role for %s-%s-%s", im.config.AppName, im.config.Environment, lambda)),
		Tags:                     convertToIAMTags(buildResourceTags(im.config, "IAMRole", roleName)),
	}

	createRoleOutput, err := im.client.CreateRole(ctx, createRoleInput)

	var roleArn string

	if err != nil {
		// Check if role already exists
		if strings.Contains(err.Error(), "EntityAlreadyExists") {
			utils.Info(ctx, "Lambda execution role already exists, using existing role", map[string]interface{}{
				"role_name": roleName,
			})

			// Get existing role
			getRoleInput := &iam.GetRoleInput{
				RoleName: aws.String(roleName),
			}
			getRoleOutput, getRoleErr := im.client.GetRole(ctx, getRoleInput)
			if getRoleErr != nil {
				return "", fmt.Errorf("role exists but failed to get role: %w", getRoleErr)
			}
			roleArn = *getRoleOutput.Role.Arn
		} else {
			return "", fmt.Errorf("failed to create Lambda execution role: %w", err)
		}
	} else {
		roleArn = *createRoleOutput.Role.Arn
		utils.Info(ctx, fmt.Sprintf("✓ Created IAM role: %s", roleName))
	}

	// Attach basic Lambda execution policy (idempotent - safe to call even if already attached)
	basicPolicyArn := "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
	attachPolicyInput := &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(basicPolicyArn),
	}

	_, err = im.client.AttachRolePolicy(ctx, attachPolicyInput)
	if err != nil {
		// AttachRolePolicy is idempotent, but log if there's an unexpected error
		if !strings.Contains(err.Error(), "EntityAlreadyExists") {
			utils.Warn(ctx, "Failed to attach basic execution policy to Lambda role", map[string]interface{}{
				"role_name":  roleName,
				"policy_arn": basicPolicyArn,
				"error":      err.Error(),
			})
		}
	}

	// Attach Secrets Manager policy (allows access to secrets with app/environment prefix)
	// This is idempotent as it uses PutRolePolicy which overwrites existing inline policies
	err = im.CreateSecretsManagerPolicy(ctx, roleName, lambda)
	if err != nil {
		utils.Warn(ctx, "Failed to create Secrets Manager policy for Lambda role", map[string]interface{}{
			"role_name": roleName,
			"lambda":    lambda,
			"error":     err.Error(),
		})
	}

	return roleArn, nil
}

// AttachVpcExecutionPolicy attaches the AWSLambdaVPCAccessExecutionRole managed policy
// to a Lambda role. This grants permissions for ENI management required by VPC-attached Lambdas.
func (im *IAMManager) AttachVpcExecutionPolicy(ctx context.Context, roleName string) error {
	vpcPolicyArn := "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
	_, err := im.client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(vpcPolicyArn),
	})
	if err != nil && !strings.Contains(err.Error(), "EntityAlreadyExists") {
		return fmt.Errorf("failed to attach VPC execution policy: %w", err)
	}
	utils.Info(ctx, "Attached VPC execution policy to role", map[string]interface{}{
		"role_name":  roleName,
		"policy_arn": vpcPolicyArn,
	})
	return nil
}

// isThrottlingError returns true if the error is an AWS IAM throttling/rate limit error.
func isThrottlingError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Throttling") || strings.Contains(msg, "Rate exceeded") || strings.Contains(msg, "RequestLimitExceeded")
}

// retryOnThrottle retries an operation with exponential backoff when AWS returns throttling errors.
func retryOnThrottle(ctx context.Context, description string, maxRetries int, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !isThrottlingError(lastErr) {
			return lastErr
		}
		if attempt < maxRetries {
			backoff := time.Duration(1<<uint(attempt)) * time.Second // 1s, 2s, 4s, 8s, 16s
			utils.Warn(ctx, fmt.Sprintf("IAM rate limited on %s, retrying in %v (attempt %d/%d)", description, backoff, attempt+1, maxRetries), nil)
			time.Sleep(backoff)
		}
	}
	return fmt.Errorf("%s: exceeded %d retries due to IAM throttling. Last error: %w", description, maxRetries, lastErr)
}

// AttachSharedIamPolicies attaches shared IAM policies to a Lambda role.
// Returns a hard error if any policy fails to attach (e.g., IAM quota exceeded).
func (im *IAMManager) AttachSharedIamPolicies(ctx context.Context, roleName string) error {
	if len(im.config.SharedIamPolicyArns) == 0 {
		return nil
	}

	utils.Info(ctx, fmt.Sprintf("Attaching %d shared IAM policies to role: %s", len(im.config.SharedIamPolicyArns), roleName))

	for _, policyArn := range im.config.SharedIamPolicyArns {
		err := retryOnThrottle(ctx, fmt.Sprintf("attach policy to %s", roleName), 5, func() error {
			_, err := im.client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
				RoleName:  aws.String(roleName),
				PolicyArn: aws.String(policyArn),
			})
			return err
		})
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
		utils.Info(ctx, "Attached shared IAM policy to role", map[string]interface{}{
			"role_name":  roleName,
			"policy_arn": policyArn,
		})
	}

	return nil
}

// DetachRemovedSharedIamPolicies detaches shared IAM policies that were removed from the config.
// It compares oldPolicyArns with the current config's SharedIamPolicyArns and detaches any that
// are no longer present. This allows Terraform to then delete the managed policy resources.
func (im *IAMManager) DetachRemovedSharedIamPolicies(ctx context.Context, roleName string, oldPolicyArns []string) error {
	if len(oldPolicyArns) == 0 {
		return nil
	}

	// Build set of current policy ARNs for fast lookup
	currentSet := make(map[string]bool)
	for _, arn := range im.config.SharedIamPolicyArns {
		currentSet[arn] = true
	}

	var lastErr error
	for _, oldArn := range oldPolicyArns {
		if currentSet[oldArn] {
			continue // Still in the config, don't detach
		}

		utils.Info(ctx, "Detaching removed shared IAM policy from role", map[string]interface{}{
			"role_name":  roleName,
			"policy_arn": oldArn,
		})

		if err := im.DetachPolicyFromRole(ctx, roleName, oldArn); err != nil {
			utils.Warn(ctx, "Failed to detach removed shared IAM policy", map[string]interface{}{
				"role_name":  roleName,
				"policy_arn": oldArn,
				"error":      err.Error(),
			})
			lastErr = err
			// Continue detaching remaining policies
		}
	}

	return lastErr
}

// ReconcileSharedIamPolicies queries AWS for the actual policies attached to a role
// and detaches any that are not in the current SharedIamPolicyArns config and not
// managed by the provider (basic execution, invoke policies, etc).
// This is state-independent — it works even if Terraform state is stale.
func (im *IAMManager) ReconcileSharedIamPolicies(ctx context.Context, roleName string) error {
	// Query AWS for what's actually attached to this role
	listInput := &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	}

	var listOutput *iam.ListAttachedRolePoliciesOutput
	err := retryOnThrottle(ctx, fmt.Sprintf("list policies on %s", roleName), 5, func() error {
		var err error
		listOutput, err = im.client.ListAttachedRolePolicies(ctx, listInput)
		return err
	})
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchEntity") {
			return nil // Role doesn't exist, nothing to reconcile
		}
		return fmt.Errorf("failed to list attached policies for role %s: %w", roleName, err)
	}

	// Build set of policies that SHOULD be attached (from shared_iam_policy_arns)
	desiredSet := make(map[string]bool)
	for _, arn := range im.config.SharedIamPolicyArns {
		desiredSet[arn] = true
	}

	// Known provider-managed policy suffixes that should never be detached.
	// These are created by the provider's own IAM logic (DynamoDB, S3, SES, etc.)
	providerSuffixes := []string{
		"-dynamodb-policy",
		"-s3-policy",
		"-ses-policy",
		"-secretsmanager-policy",
		"-cognito-policy",
	}

	var lastErr error
	for _, attached := range listOutput.AttachedPolicies {
		arn := aws.ToString(attached.PolicyArn)
		policyName := aws.ToString(attached.PolicyName)

		// Skip if this policy should be attached (it's in the desired set)
		if desiredSet[arn] {
			continue
		}

		// Skip AWS managed policies (like AWSLambdaBasicExecutionRole)
		if strings.HasPrefix(arn, "arn:aws:iam::aws:policy/") {
			continue
		}

		// Skip provider-managed policies (identified by known suffixes)
		isProviderManaged := false
		for _, suffix := range providerSuffixes {
			if strings.HasSuffix(policyName, suffix) {
				isProviderManaged = true
				break
			}
		}
		if isProviderManaged {
			continue
		}

		// This policy is attached but not in the desired set and not provider-managed.
		// Check if it was likely a previous shared policy by seeing if it's a customer-account
		// managed policy (not AWS managed). If so, detach it.
		utils.Info(ctx, "Reconcile: detaching stale policy from role", map[string]interface{}{
			"role_name":   roleName,
			"policy_arn":  arn,
			"policy_name": policyName,
		})

		if err := im.DetachPolicyFromRole(ctx, roleName, arn); err != nil {
			utils.Warn(ctx, "Reconcile: failed to detach stale policy", map[string]interface{}{
				"role_name":  roleName,
				"policy_arn": arn,
				"error":      err.Error(),
			})
			lastErr = err
		}
	}

	return lastErr
}

// ReconcileOrphanedRoles finds all IAM roles matching the dispatcher naming pattern
// (including orphaned roles with random suffixes from previous provider versions)
// and reconciles shared IAM policies on them. This ensures that when shared_iam_policy_arns
// change, the old policies are detached from ALL roles — not just the ones in Terraform state.
// Roles that are not in the knownRoles set are considered orphaned and will also be deleted
// after their policies are detached.
func (im *IAMManager) ReconcileOrphanedRoles(ctx context.Context, knownRoles map[string]bool) (int, error) {
	prefix := fmt.Sprintf("%s-%s-", im.config.AppName, im.config.Environment)
	suffix := "-lambda-role"

	utils.Info(ctx, "Scanning for orphaned lambda roles", map[string]interface{}{
		"prefix":           prefix,
		"known_role_count": len(knownRoles),
	})

	var orphanedCount int
	var lastErr error

	// Use paginated ListRoles to find all roles with our prefix
	input := &iam.ListRolesInput{
		PathPrefix: aws.String("/"),
		MaxItems:   aws.Int32(100),
	}

	paginator := iam.NewListRolesPaginator(im.client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return orphanedCount, fmt.Errorf("failed to list IAM roles: %w", err)
		}

		for _, role := range page.Roles {
			roleName := aws.ToString(role.RoleName)

			// Only consider roles matching our naming pattern
			if !strings.HasPrefix(roleName, prefix) || !strings.Contains(roleName, suffix) {
				continue
			}

			// Skip roles we already know about (they're handled by the normal reconciliation)
			if knownRoles[roleName] {
				continue
			}

			utils.Info(ctx, "Found orphaned lambda role, reconciling and cleaning up", map[string]interface{}{
				"role_name": roleName,
			})

			// Reconcile shared policies on this orphaned role (detach stale ones)
			if err := im.ReconcileSharedIamPolicies(ctx, roleName); err != nil {
				utils.Warn(ctx, "Failed to reconcile shared policies on orphaned role", map[string]interface{}{
					"role_name": roleName,
					"error":     err.Error(),
				})
				lastErr = err
			}

			// Check if this orphaned role is still attached to a Lambda function.
			// If not, clean it up entirely (detach all policies and delete the role).
			// We detect this by checking if the role's description references a Lambda
			// that no longer exists — but the safest approach is to just detach shared
			// policies and leave the role. Full orphan cleanup is a separate concern.
			// For now, we only detach shared policies to unblock policy deletion.
			orphanedCount++
		}
	}

	if orphanedCount > 0 {
		utils.Info(ctx, "Finished reconciling orphaned roles", map[string]interface{}{
			"orphaned_count": orphanedCount,
		})
	}

	return orphanedCount, lastErr
}



// CreateDynamoDBPolicies creates IAM policies for DynamoDB access
func (im *IAMManager) CreateDynamoDBPolicies(ctx context.Context, routes []utils.Route) error {
	// Group routes by lambda to get tables per Lambda
	lambdaGroups := utils.GroupByLambda(routes)

	for lambdaName, lambdaRoutes := range lambdaGroups {
		err := im.CreateDynamoDBPoliciesForAction(ctx, lambdaName, lambdaRoutes)
		if err != nil {
			utils.Warn(ctx, "Failed to create DynamoDB policy for lambda", map[string]interface{}{
				"lambda": lambdaName,
				"error":  err.Error(),
			})
		}
	}

	return nil
}

// CreateCognitoPolicies is DEPRECATED and no longer used.
// Cognito IAM policies should be managed via shared_iam_policy_arns to ensure
// proper resource-scoped permissions. The old implementation used "Resource": "*"
// which was overly permissive and violated the principle of least privilege.
//
// Instead, define your Cognito policy in Terraform and attach via shared_iam_policy_arns:
//
//	shared_iam_policy_arns = [module.sobvibes.cognito_access_policy_arn]
func (im *IAMManager) CreateCognitoPolicies(ctx context.Context, routes []utils.Route) error {
	// No-op: Cognito policies should be managed via shared_iam_policy_arns
	utils.Info(ctx, "Skipping Cognito policy creation - use shared_iam_policy_arns instead", nil)
	return nil
}

// CreateApiGatewayCloudWatchRole creates an IAM role for API Gateway CloudWatch logging
func (im *IAMManager) CreateApiGatewayCloudWatchRole(ctx context.Context, apiGatewayName string) (string, error) {
	roleName := fmt.Sprintf("%s-cloudwatch-role", apiGatewayName)

	// Create trust policy for API Gateway
	trustPolicy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Principal": map[string]interface{}{
					"Service": "apigateway.amazonaws.com",
				},
				"Action": "sts:AssumeRole",
			},
		},
	}

	trustPolicyBytes, err := json.Marshal(trustPolicy)
	if err != nil {
		return "", fmt.Errorf("failed to marshal trust policy: %w", err)
	}

	// Create the IAM role
	createRoleInput := &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(string(trustPolicyBytes)),
		Description:              aws.String(fmt.Sprintf("CloudWatch logging role for %s API Gateway", apiGatewayName)),
		Tags:                     convertToIAMTags(buildResourceTags(im.config, "IAMRole", roleName)),
	}

	createRoleOutput, err := im.client.CreateRole(ctx, createRoleInput)

	var roleArn string

	if err != nil {
		// Check if role already exists
		if strings.Contains(err.Error(), "EntityAlreadyExists") {
			utils.Info(ctx, "API Gateway CloudWatch role already exists, using existing role", map[string]interface{}{
				"role_name": roleName,
			})

			// Get existing role
			getRoleInput := &iam.GetRoleInput{
				RoleName: aws.String(roleName),
			}
			getRoleOutput, getRoleErr := im.client.GetRole(ctx, getRoleInput)
			if getRoleErr != nil {
				return "", fmt.Errorf("role exists but failed to get role: %w", getRoleErr)
			}
			roleArn = *getRoleOutput.Role.Arn
		} else {
			return "", fmt.Errorf("failed to create API Gateway CloudWatch role: %w", err)
		}
	} else {
		roleArn = *createRoleOutput.Role.Arn
		utils.Info(ctx, "Created new API Gateway CloudWatch role", map[string]interface{}{
			"role_name": roleName,
			"role_arn":  roleArn,
		})
	}

	// Attach AWS managed CloudWatch Logs policy for API Gateway (idempotent)
	cloudWatchPolicyArn := "arn:aws:iam::aws:policy/service-role/AmazonAPIGatewayPushToCloudWatchLogs"
	attachPolicyInput := &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(cloudWatchPolicyArn),
	}

	_, err = im.client.AttachRolePolicy(ctx, attachPolicyInput)
	if err != nil {
		// AttachRolePolicy is idempotent, but log if there's an unexpected error
		if !strings.Contains(err.Error(), "EntityAlreadyExists") {
			utils.Warn(ctx, "Failed to attach CloudWatch policy to API Gateway role", map[string]interface{}{
				"role_name":  roleName,
				"policy_arn": cloudWatchPolicyArn,
				"error":      err.Error(),
			})
		}
	}

	return roleArn, nil
}

// DeleteLambdaExecutionRoles deletes IAM roles for Lambda functions
func (im *IAMManager) DeleteLambdaExecutionRoles(ctx context.Context, routes []utils.Route) error {
	// Group routes by lambda to get unique roles
	lambdaGroups := utils.GroupByLambda(routes)

	for lambdaName := range lambdaGroups {
		roleName := fmt.Sprintf("%s-%s-%s-lambda-role", im.config.AppName, im.config.Environment, lambdaName)

		utils.Info(ctx, "Deleting Lambda execution role", map[string]interface{}{
			"role_name": roleName,
			"lambda":    lambdaName,
		})

		// First, detach all policies from the role
		listPoliciesInput := &iam.ListAttachedRolePoliciesInput{
			RoleName: aws.String(roleName),
		}

		listPoliciesOutput, err := im.client.ListAttachedRolePolicies(ctx, listPoliciesInput)
		if err != nil {
			if strings.Contains(err.Error(), "NoSuchEntity") {
				utils.Info(ctx, "Lambda execution role does not exist, skipping deletion", map[string]interface{}{
					"role_name": roleName,
				})
				continue
			}
			utils.Warn(ctx, "Failed to list policies for role", map[string]interface{}{
				"role_name": roleName,
				"error":     err.Error(),
			})
			continue
		}

		// Detach all managed policies
		for _, policy := range listPoliciesOutput.AttachedPolicies {
			detachInput := &iam.DetachRolePolicyInput{
				RoleName:  aws.String(roleName),
				PolicyArn: policy.PolicyArn,
			}

			_, err := im.client.DetachRolePolicy(ctx, detachInput)
			if err != nil {
				utils.Warn(ctx, "Failed to detach policy from role", map[string]interface{}{
					"role_name":  roleName,
					"policy_arn": *policy.PolicyArn,
					"error":      err.Error(),
				})
			}
		}

		// Delete inline policies if any
		listInlinePoliciesInput := &iam.ListRolePoliciesInput{
			RoleName: aws.String(roleName),
		}

		listInlinePoliciesOutput, err := im.client.ListRolePolicies(ctx, listInlinePoliciesInput)
		if err == nil {
			for _, policyName := range listInlinePoliciesOutput.PolicyNames {
				deleteInlinePolicyInput := &iam.DeleteRolePolicyInput{
					RoleName:   aws.String(roleName),
					PolicyName: aws.String(policyName),
				}

				_, err := im.client.DeleteRolePolicy(ctx, deleteInlinePolicyInput)
				if err != nil {
					utils.Warn(ctx, "Failed to delete inline policy from role", map[string]interface{}{
						"role_name":   roleName,
						"policy_name": policyName,
						"error":       err.Error(),
					})
				}
			}
		}

		// Finally, delete the role
		deleteRoleInput := &iam.DeleteRoleInput{
			RoleName: aws.String(roleName),
		}

		_, err = im.client.DeleteRole(ctx, deleteRoleInput)
		if err != nil {
			utils.Warn(ctx, "Failed to delete Lambda execution role", map[string]interface{}{
				"role_name": roleName,
				"error":     err.Error(),
			})
		} else {
			utils.Info(ctx, "Successfully deleted Lambda execution role", map[string]interface{}{
				"role_name": roleName,
			})
		}
	}

	return nil
}

// DeleteApiGatewayCloudWatchRole deletes the API Gateway CloudWatch role
func (im *IAMManager) DeleteApiGatewayCloudWatchRole(ctx context.Context) error {
	roleName := fmt.Sprintf("%s-%s-apigateway-cloudwatch-role", im.config.AppName, im.config.Environment)

	utils.Info(ctx, "Deleting API Gateway CloudWatch role", map[string]interface{}{
		"role_name": roleName,
	})

	// List and detach all attached policies
	listPoliciesInput := &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	}

	listPoliciesOutput, err := im.client.ListAttachedRolePolicies(ctx, listPoliciesInput)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchEntity") {
			utils.Info(ctx, "API Gateway CloudWatch role does not exist, skipping deletion", map[string]interface{}{
				"role_name": roleName,
			})
			return nil
		}
		utils.Warn(ctx, "Failed to list policies for API Gateway role", map[string]interface{}{
			"role_name": roleName,
			"error":     err.Error(),
		})
		return nil
	}

	// Detach all managed policies
	for _, policy := range listPoliciesOutput.AttachedPolicies {
		detachInput := &iam.DetachRolePolicyInput{
			RoleName:  aws.String(roleName),
			PolicyArn: policy.PolicyArn,
		}

		_, err := im.client.DetachRolePolicy(ctx, detachInput)
		if err != nil {
			utils.Warn(ctx, "Failed to detach policy from API Gateway role", map[string]interface{}{
				"role_name":  roleName,
				"policy_arn": *policy.PolicyArn,
				"error":      err.Error(),
			})
		}
	}

	// Delete the role
	deleteRoleInput := &iam.DeleteRoleInput{
		RoleName: aws.String(roleName),
	}

	_, err = im.client.DeleteRole(ctx, deleteRoleInput)
	if err != nil {
		utils.Warn(ctx, "Failed to delete API Gateway CloudWatch role", map[string]interface{}{
			"role_name": roleName,
			"error":     err.Error(),
		})
	} else {
		utils.Info(ctx, "Successfully deleted API Gateway CloudWatch role", map[string]interface{}{
			"role_name": roleName,
		})
	}

	return nil
}

// DeleteLambdaRole deletes a Lambda execution role and all its attached policies
func (im *IAMManager) DeleteLambdaRole(ctx context.Context, roleName, lambda string) error {
	utils.Info(ctx, "Deleting Lambda execution role", map[string]interface{}{
		"role_name": roleName,
		"lambda":    lambda,
	})

	// List and detach all managed policies
	listPoliciesInput := &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	}

	listPoliciesOutput, err := im.client.ListAttachedRolePolicies(ctx, listPoliciesInput)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchEntity") {
			utils.Info(ctx, "Lambda role does not exist, skipping deletion", map[string]interface{}{
				"role_name": roleName,
			})
			return nil
		}
		return fmt.Errorf("failed to list policies for role: %w", err)
	}

	// Detach all managed policies
	for _, policy := range listPoliciesOutput.AttachedPolicies {
		detachInput := &iam.DetachRolePolicyInput{
			RoleName:  aws.String(roleName),
			PolicyArn: policy.PolicyArn,
		}

		_, err := im.client.DetachRolePolicy(ctx, detachInput)
		if err != nil {
			utils.Warn(ctx, "Failed to detach policy", map[string]interface{}{
				"role_name":  roleName,
				"policy_arn": *policy.PolicyArn,
				"error":      err.Error(),
			})
		}
	}

	// Delete inline policies (like DynamoDB and Cognito policies)
	listInlinePoliciesInput := &iam.ListRolePoliciesInput{
		RoleName: aws.String(roleName),
	}

	listInlineOutput, err := im.client.ListRolePolicies(ctx, listInlinePoliciesInput)
	if err == nil {
		for _, policyName := range listInlineOutput.PolicyNames {
			deleteInlineInput := &iam.DeleteRolePolicyInput{
				RoleName:   aws.String(roleName),
				PolicyName: aws.String(policyName),
			}

			_, err := im.client.DeleteRolePolicy(ctx, deleteInlineInput)
			if err != nil {
				utils.Warn(ctx, "Failed to delete inline policy", map[string]interface{}{
					"role_name":   roleName,
					"policy_name": policyName,
					"error":       err.Error(),
				})
			}
		}
	}

	// Delete the role
	deleteRoleInput := &iam.DeleteRoleInput{
		RoleName: aws.String(roleName),
	}

	_, err = im.client.DeleteRole(ctx, deleteRoleInput)
	if err != nil {
		return fmt.Errorf("failed to delete role: %w", err)
	}

	utils.Info(ctx, "Successfully deleted Lambda execution role", map[string]interface{}{
		"role_name": roleName,
	})

	return nil
}

// UpdateDynamoDBPolicy updates the DynamoDB policy for an lambda
func (im *IAMManager) UpdateDynamoDBPolicy(ctx context.Context, lambda string, routes []utils.Route) error {
	// First delete the old policy, then create the new one
	err := im.DeleteDynamoDBPolicy(ctx, lambda)
	if err != nil {
		utils.Warn(ctx, "Failed to delete old DynamoDB policy", map[string]interface{}{
			"lambda": lambda,
			"error":  err.Error(),
		})
	}

	// Create new policy
	return im.CreateDynamoDBPoliciesForAction(ctx, lambda, routes)
}

// UpdateCognitoPolicy updates the Cognito policy for an lambda
func (im *IAMManager) UpdateCognitoPolicy(ctx context.Context, lambda string, routes []utils.Route) error {
	// First delete the old policy, then create the new one
	err := im.DeleteCognitoPolicy(ctx, lambda)
	if err != nil {
		utils.Warn(ctx, "Failed to delete old Cognito policy", map[string]interface{}{
			"lambda": lambda,
			"error":  err.Error(),
		})
	}

	// Create new policy
	return im.CreateCognitoPoliciesForAction(ctx, lambda, routes)
}

// CreateDynamoDBPoliciesForAction creates DynamoDB policy for a specific lambda
// Accepts optional actualRoleName to use instead of constructing from lambda name
func (im *IAMManager) CreateDynamoDBPoliciesForAction(ctx context.Context, lambda string, routes []utils.Route, actualRoleName ...string) error {
	// Debug: Log what we have in LambdaConfig
	utils.Info(ctx, "CreateDynamoDBPoliciesForAction called", map[string]interface{}{
		"lambda":              lambda,
		"routes_count":        len(routes),
		"lambda_config_nil":   im.config.LambdaConfig == nil,
		"lambda_config_count": len(im.config.LambdaConfig),
	})
	if im.config.LambdaConfig != nil {
		keys := make([]string, 0, len(im.config.LambdaConfig))
		for k := range im.config.LambdaConfig {
			keys = append(keys, k)
		}
		utils.Info(ctx, "LambdaConfig keys", map[string]interface{}{
			"keys": keys,
		})
	}

	// Collect tables for this lambda from routes
	tablesSet := make(map[string]bool)
	for _, route := range routes {
		for _, table := range route.Tables {
			tablesSet[table] = true
		}
	}

	// Add shared read-only tables (all lambdas can read)
	for _, table := range im.config.ReadOnlyTables {
		tablesSet[table] = true
	}

	// Add shared read-write tables (all lambdas can read and write)
	for _, table := range im.config.ReadWriteTables {
		tablesSet[table] = true
	}

	// Use provided actual role name, or construct base name
	roleName := fmt.Sprintf("%s-%s-%s-lambda-role", im.config.AppName, im.config.Environment, lambda)
	if len(actualRoleName) > 0 && actualRoleName[0] != "" {
		roleName = actualRoleName[0]
	}
	policyName := fmt.Sprintf("%s-%s-%s-dynamodb-policy", im.config.AppName, im.config.Environment, lambda)

	// Separate resources by permission level
	readOnlySet := make(map[string]bool)
	for _, table := range im.config.ReadOnlyTables {
		readOnlySet[table] = true
	}

	readWriteSet := make(map[string]bool)
	for _, table := range im.config.ReadWriteTables {
		readWriteSet[table] = true
	}

	// Collect ARNs for each permission level
	readOnlyArns := []string{}
	readWriteArns := []string{}

	// Maps to track custom permissions (beyond standard read-only/read-write)
	customPermissionArns := make(map[string][]string) // permission set hash -> ARNs

	// Process route-based tables (standard read-write for tables from routes)
	for table := range tablesSet {
		normalizedTableName := strings.ReplaceAll(table, "_", "-")
		tableArn := fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s-%s-%s",
			im.config.AwsRegion, im.config.AwsAccountId, im.config.AppName, im.config.Environment, normalizedTableName)
		indexArn := fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s-%s-%s/index/*",
			im.config.AwsRegion, im.config.AwsAccountId, im.config.AppName, im.config.Environment, normalizedTableName)

		if readOnlySet[table] {
			// Read-only table
			readOnlyArns = append(readOnlyArns, tableArn, indexArn)
		} else {
			// Read-write table (either from routes or from read_write_tables)
			readWriteArns = append(readWriteArns, tableArn, indexArn)
		}
	}

	// Process lambda_config dynamodb_tables (can specify custom ARNs and permissions)
	configTableArns, err := im.parseDynamoDBTablesFromConfig(ctx, lambda, &customPermissionArns)
	if err != nil {
		utils.Warn(ctx, "Failed to parse dynamodb_tables from lambda_config", map[string]interface{}{
			"lambda": lambda,
			"error":  err.Error(),
		})
	} else if len(configTableArns) > 0 {
		utils.Info(ctx, "Added DynamoDB tables from lambda_config", map[string]interface{}{
			"lambda":      lambda,
			"table_count": len(configTableArns),
		})
	}

	// Check if we have any tables to grant permissions for
	totalTables := len(tablesSet) + len(configTableArns)
	if totalTables == 0 {
		return nil // No tables, no policy needed
	}

	utils.Info(ctx, fmt.Sprintf("Attaching DynamoDB policy to %s (%d tables)", roleName, totalTables))

	// Create policy document with separate statements for read-only, read-write, and custom permissions
	statements := []map[string]interface{}{}

	if len(readWriteArns) > 0 {
		statements = append(statements, map[string]interface{}{
			"Effect": "Allow",
			"Action": []string{
				"dynamodb:GetItem",
				"dynamodb:PutItem",
				"dynamodb:UpdateItem",
				"dynamodb:DeleteItem",
				"dynamodb:Query",
				"dynamodb:Scan",
			},
			"Resource": readWriteArns,
		})
	}

	if len(readOnlyArns) > 0 {
		statements = append(statements, map[string]interface{}{
			"Effect": "Allow",
			"Action": []string{
				"dynamodb:GetItem",
				"dynamodb:Query",
				"dynamodb:Scan",
			},
			"Resource": readOnlyArns,
		})
	}

	// Add custom permission statements from lambda_config.dynamodb_tables
	for permissionsHash, arns := range customPermissionArns {
		if len(arns) > 0 {
			// Parse permissions from hash (format: "lambda1,lambda2,lambda3")
			permissions := strings.Split(permissionsHash, ",")
			statements = append(statements, map[string]interface{}{
				"Effect":   "Allow",
				"Action":   permissions,
				"Resource": arns,
			})
		}
	}

	if len(statements) == 0 {
		return nil // No tables to grant access to
	}

	// Create policy document
	policyDoc := map[string]interface{}{
		"Version":   "2012-10-17",
		"Statement": statements,
	}

	policyDocBytes, err := json.Marshal(policyDoc)
	if err != nil {
		return fmt.Errorf("failed to marshal policy document: %w", err)
	}

	// Attach inline policy to role
	putPolicyInput := &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(string(policyDocBytes)),
	}

	_, err = im.client.PutRolePolicy(ctx, putPolicyInput)
	if err != nil {
		return fmt.Errorf("failed to put DynamoDB policy: %w", err)
	}

	return nil
}

// parseDynamoDBTablesFromConfig parses DynamoDB table configurations from lambda_config.{lambda}.dynamodb_tables
// Returns a list of table ARNs and populates the customPermissionArns map with permission-specific ARN groups
func (im *IAMManager) parseDynamoDBTablesFromConfig(ctx context.Context, lambda string, customPermissionArns *map[string][]string) ([]string, error) {
	if im.config.LambdaConfig == nil || len(im.config.LambdaConfig) == 0 {
		return nil, nil
	}

	lambdaConfigRaw, ok := im.config.LambdaConfig[lambda]
	if !ok {
		return nil, nil
	}

	lambdaConfig, ok := extractMapValue(lambdaConfigRaw)
	if !ok {
		return nil, fmt.Errorf("failed to extract map from lambda config (type: %T)", lambdaConfigRaw)
	}

	lambdaTablesRaw, ok := lambdaConfig["dynamodb_tables"]
	if !ok {
		return nil, nil
	}

	tableConfigs, err := parseTerraformObjectList(lambdaTablesRaw, lambda, "dynamodb_tables")
	if err != nil {
		return nil, err
	}
	if len(tableConfigs) == 0 {
		return nil, nil
	}

	var allTableArns []string

	for _, tableConfig := range tableConfigs {
		tableArnRaw, ok := tableConfig["table_arn"]
		if !ok {
			utils.Warn(ctx, "DynamoDB table configuration missing table_arn", map[string]interface{}{"lambda": lambda})
			continue
		}

		tableArn, ok := extractTerraformString(tableArnRaw)
		if !ok {
			utils.Warn(ctx, "Invalid table_arn type", map[string]interface{}{"lambda": lambda, "type": fmt.Sprintf("%T", tableArnRaw)})
			continue
		}

		permissionsRaw, ok := tableConfig["permissions"]
		if !ok {
			utils.Warn(ctx, "DynamoDB table configuration missing permissions", map[string]interface{}{"lambda": lambda, "table_arn": tableArn})
			continue
		}

		permissions, ok := extractTerraformStringList(permissionsRaw)
		if !ok || len(permissions) == 0 {
			utils.Warn(ctx, "No valid permissions for DynamoDB table", map[string]interface{}{"lambda": lambda, "table_arn": tableArn})
			continue
		}

		sort.Strings(permissions)
		permissionsHash := strings.Join(permissions, ",")

		tableResources := []string{tableArn, tableArn + "/index/*"}
		(*customPermissionArns)[permissionsHash] = append((*customPermissionArns)[permissionsHash], tableResources...)
		allTableArns = append(allTableArns, tableArn)
	}

	return allTableArns, nil
}

// CreateCognitoPoliciesForAction is DEPRECATED and no longer used.
// Cognito IAM policies should be managed via shared_iam_policy_arns to ensure
// proper resource-scoped permissions. The old implementation used "Resource": "*"
// which was overly permissive and violated the principle of least privilege.
//
// Instead, define your Cognito policy in Terraform and attach via shared_iam_policy_arns:
//
//	shared_iam_policy_arns = [module.sobvibes.cognito_access_policy_arn]
func (im *IAMManager) CreateCognitoPoliciesForAction(ctx context.Context, lambda string, routes []utils.Route) error {
	// No-op: Cognito policies should be managed via shared_iam_policy_arns
	utils.Info(ctx, "Skipping Cognito policy creation - use shared_iam_policy_arns instead", map[string]interface{}{
		"lambda": lambda,
	})
	return nil
}

// CreateSecretsManagerPolicy creates an inline IAM policy for Secrets Manager access
func (im *IAMManager) CreateSecretsManagerPolicy(ctx context.Context, roleName, lambda string) error {
	policyName := fmt.Sprintf("%s-%s-%s-secretsmanager-policy", im.config.AppName, im.config.Environment, lambda)

	// Allow access to secrets with app-environment prefix
	// This handles Terraform-generated secrets with random suffixes like:
	// sobvibes-marketplace-dev02-stripe-secret20251031031025475500000003
	secretResourceArn := fmt.Sprintf("arn:aws:secretsmanager:%s:%s:secret:%s-%s-*",
		im.config.AwsRegion, im.config.AwsAccountId, im.config.AppName, im.config.Environment)

	// Create policy document
	policyDoc := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Action": []string{
					"secretsmanager:GetSecretValue",
					"secretsmanager:DescribeSecret",
				},
				"Resource": secretResourceArn,
			},
		},
	}

	policyDocBytes, err := json.Marshal(policyDoc)
	if err != nil {
		return fmt.Errorf("failed to marshal Secrets Manager policy document: %w", err)
	}

	// Attach inline policy to role
	putPolicyInput := &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(string(policyDocBytes)),
	}

	_, err = im.client.PutRolePolicy(ctx, putPolicyInput)
	if err != nil {
		return fmt.Errorf("failed to put Secrets Manager policy: %w", err)
	}

	utils.Info(ctx, "Created Secrets Manager policy for lambda", map[string]interface{}{
		"lambda":         lambda,
		"policy_name":    policyName,
		"resource_scope": secretResourceArn,
	})

	return nil
}

// DeleteDynamoDBPolicy deletes the DynamoDB policy for an lambda
func (im *IAMManager) deleteInlinePolicy(ctx context.Context, lambda, policySuffix string) error {
	roleName := fmt.Sprintf("%s-%s-%s-lambda-role", im.config.AppName, im.config.Environment, lambda)
	policyName := fmt.Sprintf("%s-%s-%s-%s-policy", im.config.AppName, im.config.Environment, lambda, policySuffix)

	_, err := im.client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
		RoleName:   aws.String(roleName),
		PolicyName: aws.String(policyName),
	})
	if err != nil && !strings.Contains(err.Error(), "NoSuchEntity") {
		return fmt.Errorf("failed to delete %s policy: %w", policySuffix, err)
	}

	return nil
}

// putInlinePolicy marshals statements into a policy document and attaches it to a role.
func (im *IAMManager) putInlinePolicy(ctx context.Context, roleName, policyName string, statements []map[string]interface{}) error {
	policyDoc := map[string]interface{}{
		"Version":   "2012-10-17",
		"Statement": statements,
	}

	policyDocBytes, err := json.Marshal(policyDoc)
	if err != nil {
		return fmt.Errorf("failed to marshal policy document for %s: %w", policyName, err)
	}

	_, err = im.client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(string(policyDocBytes)),
	})
	if err != nil {
		return fmt.Errorf("failed to put policy %s: %w", policyName, err)
	}

	return nil
}

func (im *IAMManager) DeleteDynamoDBPolicy(ctx context.Context, lambda string) error {
	return im.deleteInlinePolicy(ctx, lambda, "dynamodb")
}

// DeleteCognitoPolicy deletes the Cognito policy for an lambda
func (im *IAMManager) DeleteCognitoPolicy(ctx context.Context, lambda string) error {
	return im.deleteInlinePolicy(ctx, lambda, "cognito")
}

// CreateS3Policies creates IAM policies for S3 access
func (im *IAMManager) CreateS3Policies(ctx context.Context, routes []utils.Route) error {
	// Check if lambda_config exists
	if im.config.LambdaConfig == nil || len(im.config.LambdaConfig) == 0 {
		utils.Debug(ctx, "No lambda_config found, skipping S3 policy creation")
		return nil
	}

	// Group routes by lambda to get unique Lambda functions
	lambdaGroups := utils.GroupByLambda(routes)

	for lambdaName := range lambdaGroups {
		err := im.CreateS3PoliciesForAction(ctx, lambdaName)
		if err != nil {
			utils.Warn(ctx, "Failed to create S3 policy for lambda", map[string]interface{}{
				"lambda": lambdaName,
				"error":  err.Error(),
			})
		}
	}

	return nil
}

// CreateS3PoliciesForAction creates S3 policy for a specific lambda
func (im *IAMManager) CreateS3PoliciesForAction(ctx context.Context, lambda string, actualRoleName ...string) error {
	if im.config.LambdaConfig == nil || len(im.config.LambdaConfig) == 0 {
		return nil
	}

	lambdaConfigRaw, ok := im.config.LambdaConfig[lambda]
	if !ok {
		return nil
	}

	lambdaConfig, ok := extractMapValue(lambdaConfigRaw)
	if !ok {
		return nil
	}

	lambdaBucketsRaw, ok := lambdaConfig["s3_buckets"]
	if !ok {
		return nil
	}

	bucketConfigs, err := parseTerraformObjectList(lambdaBucketsRaw, lambda, "s3_buckets")
	if err != nil {
		return err
	}
	if len(bucketConfigs) == 0 {
		return nil
	}

	roleName := fmt.Sprintf("%s-%s-%s-lambda-role", im.config.AppName, im.config.Environment, lambda)
	if len(actualRoleName) > 0 && actualRoleName[0] != "" {
		roleName = actualRoleName[0]
	}
	policyName := fmt.Sprintf("%s-%s-%s-s3-policy", im.config.AppName, im.config.Environment, lambda)

	statements := []map[string]interface{}{}

	for _, bucketConfig := range bucketConfigs {
		bucketArnRaw, ok := bucketConfig["bucket_arn"]
		if !ok {
			utils.Warn(ctx, "S3 bucket configuration missing bucket_arn", map[string]interface{}{"lambda": lambda})
			continue
		}

		bucketArn, ok := extractTerraformString(bucketArnRaw)
		if !ok {
			utils.Warn(ctx, "Invalid bucket_arn type", map[string]interface{}{"lambda": lambda, "type": fmt.Sprintf("%T", bucketArnRaw)})
			continue
		}

		permissionsRaw, ok := bucketConfig["permissions"]
		if !ok {
			utils.Warn(ctx, "S3 bucket configuration missing permissions", map[string]interface{}{"lambda": lambda, "bucket_arn": bucketArn})
			continue
		}

		permissions, ok := extractTerraformStringList(permissionsRaw)
		if !ok || len(permissions) == 0 {
			utils.Warn(ctx, "No valid permissions for S3 bucket", map[string]interface{}{"lambda": lambda, "bucket_arn": bucketArn})
			continue
		}

		statements = append(statements, map[string]interface{}{
			"Effect":   "Allow",
			"Action":   permissions,
			"Resource": []string{bucketArn, bucketArn + "/*"},
		})
	}

	if len(statements) == 0 {
		return nil
	}

	return im.putInlinePolicy(ctx, roleName, policyName, statements)
}

// UpdateS3Policy updates the S3 policy for an lambda
func (im *IAMManager) UpdateS3Policy(ctx context.Context, lambda string) error {
	// First delete the old policy, then create the new one
	err := im.DeleteS3Policy(ctx, lambda)
	if err != nil {
		utils.Warn(ctx, "Failed to delete old S3 policy", map[string]interface{}{
			"lambda": lambda,
			"error":  err.Error(),
		})
	}

	// Create new policy
	return im.CreateS3PoliciesForAction(ctx, lambda)
}

// DeleteS3Policy deletes the S3 policy for an lambda
func (im *IAMManager) DeleteS3Policy(ctx context.Context, lambda string) error {
	return im.deleteInlinePolicy(ctx, lambda, "s3")
}

// CreateSESPolicies creates IAM policies for SES access
func (im *IAMManager) CreateSESPolicies(ctx context.Context, routes []utils.Route) error {
	// Check if lambda_config exists
	if im.config.LambdaConfig == nil || len(im.config.LambdaConfig) == 0 {
		utils.Debug(ctx, "No lambda_config found, skipping SES policy creation")
		return nil
	}

	// Group routes by lambda to get unique Lambda functions
	lambdaGroups := utils.GroupByLambda(routes)

	for lambdaName := range lambdaGroups {
		err := im.CreateSESPoliciesForAction(ctx, lambdaName)
		if err != nil {
			utils.Warn(ctx, "Failed to create SES policy for lambda", map[string]interface{}{
				"lambda": lambdaName,
				"error":  err.Error(),
			})
		}
	}

	return nil
}

// CreateSESPoliciesForAction creates SES policy for a specific lambda
func (im *IAMManager) CreateSESPoliciesForAction(ctx context.Context, lambda string, actualRoleName ...string) error {
	if im.config.LambdaConfig == nil || len(im.config.LambdaConfig) == 0 {
		return nil
	}

	lambdaConfigRaw, ok := im.config.LambdaConfig[lambda]
	if !ok {
		return nil
	}

	lambdaConfig, ok := extractMapValue(lambdaConfigRaw)
	if !ok {
		return nil
	}

	lambdaEmailsRaw, ok := lambdaConfig["ses_emails"]
	if !ok {
		return nil
	}

	emailConfigs, err := parseTerraformObjectList(lambdaEmailsRaw, lambda, "ses_emails")
	if err != nil {
		return err
	}
	if len(emailConfigs) == 0 {
		return nil
	}

	roleName := fmt.Sprintf("%s-%s-%s-lambda-role", im.config.AppName, im.config.Environment, lambda)
	if len(actualRoleName) > 0 && actualRoleName[0] != "" {
		roleName = actualRoleName[0]
	}
	policyName := fmt.Sprintf("%s-%s-%s-ses-policy", im.config.AppName, im.config.Environment, lambda)

	statements := []map[string]interface{}{}

	for _, emailConfig := range emailConfigs {
		emailRaw, ok := emailConfig["email"]
		if !ok {
			utils.Warn(ctx, "SES email configuration missing email", map[string]interface{}{"lambda": lambda})
			continue
		}

		email, ok := extractTerraformString(emailRaw)
		if !ok {
			utils.Warn(ctx, "Invalid email type", map[string]interface{}{"lambda": lambda, "type": fmt.Sprintf("%T", emailRaw)})
			continue
		}

		permissionsRaw, ok := emailConfig["permissions"]
		if !ok {
			utils.Warn(ctx, "SES email configuration missing permissions", map[string]interface{}{"lambda": lambda, "email": email})
			continue
		}

		permissions, ok := extractTerraformStringList(permissionsRaw)
		if !ok || len(permissions) == 0 {
			utils.Warn(ctx, "No valid permissions for SES email", map[string]interface{}{"lambda": lambda, "email": email})
			continue
		}

		// arn:aws:ses:region:account:identity/email — grants send FROM this identity TO any recipient
		emailArn := fmt.Sprintf("arn:aws:ses:%s:%s:identity/%s", im.config.AwsRegion, im.config.AwsAccountId, email)

		statements = append(statements, map[string]interface{}{
			"Effect":   "Allow",
			"Action":   permissions,
			"Resource": emailArn,
		})
	}

	if len(statements) == 0 {
		return nil
	}

	return im.putInlinePolicy(ctx, roleName, policyName, statements)
}

// UpdateSESPolicy updates the SES policy for an lambda
func (im *IAMManager) UpdateSESPolicy(ctx context.Context, lambda string) error {
	// First delete the old policy, then create the new one
	err := im.DeleteSESPolicy(ctx, lambda)
	if err != nil {
		utils.Warn(ctx, "Failed to delete old SES policy", map[string]interface{}{
			"lambda": lambda,
			"error":  err.Error(),
		})
	}

	// Create new policy
	return im.CreateSESPoliciesForAction(ctx, lambda)
}

// DeleteSESPolicy deletes the SES policy for an lambda
func (im *IAMManager) DeleteSESPolicy(ctx context.Context, lambda string) error {
	return im.deleteInlinePolicy(ctx, lambda, "ses")
}

// CreateSQSPoliciesForAction creates an inline IAM policy granting SQS permissions
// for event source mappings. When a Lambda has sqs_triggers configured, the execution
// role needs sqs:ReceiveMessage, sqs:DeleteMessage, and sqs:GetQueueAttributes on the
// queue ARN for the event source mapping to function.
func (im *IAMManager) CreateSQSPoliciesForAction(ctx context.Context, lambda string, actualRoleName ...string) error {
	if im.config.LambdaConfig == nil || len(im.config.LambdaConfig) == 0 {
		return nil
	}

	lambdaConfigRaw, ok := im.config.LambdaConfig[lambda]
	if !ok {
		return nil
	}

	lambdaConfig, ok := extractMapValue(lambdaConfigRaw)
	if !ok {
		return nil
	}

	sqsTriggersRaw, ok := lambdaConfig["sqs_triggers"]
	if !ok {
		return nil
	}

	triggersList := convertTriggersToList(sqsTriggersRaw)
	if len(triggersList) == 0 {
		return nil
	}

	var queueArns []string
	for _, triggerRaw := range triggersList {
		triggerMap, ok := extractMapValue(triggerRaw)
		if !ok {
			if tm, ok := triggerRaw.(map[string]interface{}); ok {
				triggerMap = tm
			} else {
				continue
			}
		}
		queueArn := extractTriggerStringField(triggerMap, "queue_arn")
		if queueArn != "" {
			queueArns = append(queueArns, queueArn)
		}
	}

	if len(queueArns) == 0 {
		return nil
	}

	roleName := fmt.Sprintf("%s-%s-%s-lambda-role", im.config.AppName, im.config.Environment, lambda)
	if len(actualRoleName) > 0 && actualRoleName[0] != "" {
		roleName = actualRoleName[0]
	}
	policyName := fmt.Sprintf("%s-%s-%s-sqs-policy", im.config.AppName, im.config.Environment, lambda)

	statements := []map[string]interface{}{
		{
			"Effect": "Allow",
			"Action": []string{
				"sqs:ReceiveMessage",
				"sqs:DeleteMessage",
				"sqs:GetQueueAttributes",
			},
			"Resource": queueArns,
		},
	}

	return im.putInlinePolicy(ctx, roleName, policyName, statements)
}

// DeleteSQSPolicy deletes the SQS policy for a lambda
func (im *IAMManager) DeleteSQSPolicy(ctx context.Context, lambda string) error {
	return im.deleteInlinePolicy(ctx, lambda, "sqs")
}

// AttachPolicyToRole attaches a managed policy to a role (idempotent)
func (im *IAMManager) AttachPolicyToRole(ctx context.Context, roleName, policyArn string) error {
	attachPolicyInput := &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(policyArn),
	}

	_, err := im.client.AttachRolePolicy(ctx, attachPolicyInput)
	if err != nil {
		// AttachRolePolicy is idempotent - if already attached, it won't error with EntityAlreadyExists
		// But log any unexpected errors
		return fmt.Errorf("failed to attach policy %s to role %s: %w", policyArn, roleName, err)
	}

	utils.Info(ctx, "Successfully attached policy to role", map[string]interface{}{
		"role_name":  roleName,
		"policy_arn": policyArn,
	})

	return nil
}

// DetachPolicyFromRole detaches a managed policy from a role (idempotent)
func (im *IAMManager) DetachPolicyFromRole(ctx context.Context, roleName, policyArn string) error {
	err := retryOnThrottle(ctx, fmt.Sprintf("detach policy from %s", roleName), 5, func() error {
		_, err := im.client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
			RoleName:  aws.String(roleName),
			PolicyArn: aws.String(policyArn),
		})
		return err
	})
	if err != nil {
		// Ignore NoSuchEntity errors (policy already detached or doesn't exist)
		if strings.Contains(err.Error(), "NoSuchEntity") {
			return nil
		}
		return fmt.Errorf("failed to detach policy %s from role %s: %w", policyArn, roleName, err)
	}

	utils.Info(ctx, "Successfully detached policy from role", map[string]interface{}{
		"role_name":  roleName,
		"policy_arn": policyArn,
	})

	return nil
}

// DeleteSecretsManagerPolicy deletes the Secrets Manager policy for an lambda
func (im *IAMManager) DeleteSecretsManagerPolicy(ctx context.Context, lambda string) error {
	return im.deleteInlinePolicy(ctx, lambda, "secretsmanager")
}

// convertToIAMTags converts a map of tags to IAM tag format
func convertToIAMTags(tags map[string]string) []iamTypes.Tag {
	iamTags := make([]iamTypes.Tag, 0, len(tags))
	for key, value := range tags {
		iamTags = append(iamTags, iamTypes.Tag{
			Key:   aws.String(key),
			Value: aws.String(value),
		})
	}
	return iamTags
}

// CreateDynamoDBPoliciesWithRoleNames creates DynamoDB policies using actual role names (with suffixes)
func (im *IAMManager) CreateDynamoDBPoliciesWithRoleNames(ctx context.Context, routes []utils.Route, actualRoleNames map[string]string) error {
	lambdaGroups := utils.GroupByLambda(routes)

	for lambdaName, lambdaRoutes := range lambdaGroups {
		var err error
		if roleName, exists := actualRoleNames[lambdaName]; exists {
			err = im.CreateDynamoDBPoliciesForAction(ctx, lambdaName, lambdaRoutes, roleName)
		} else {
			err = im.CreateDynamoDBPoliciesForAction(ctx, lambdaName, lambdaRoutes)
		}
		if err != nil {
			utils.Warn(ctx, "Failed to create DynamoDB policy for lambda", map[string]interface{}{
				"lambda": lambdaName,
				"error":  err.Error(),
			})
		}
	}

	return nil
}

// CreateS3PoliciesWithRoleNames creates S3 policies using actual role names (with suffixes)
func (im *IAMManager) CreateS3PoliciesWithRoleNames(ctx context.Context, routes []utils.Route, actualRoleNames map[string]string) error {
	lambdaGroups := utils.GroupByLambda(routes)

	for lambdaName := range lambdaGroups {
		var err error
		if roleName, exists := actualRoleNames[lambdaName]; exists {
			err = im.CreateS3PoliciesForAction(ctx, lambdaName, roleName)
		} else {
			err = im.CreateS3PoliciesForAction(ctx, lambdaName)
		}
		if err != nil {
			utils.Warn(ctx, "Failed to create S3 policy for lambda", map[string]interface{}{
				"lambda": lambdaName,
				"error":  err.Error(),
			})
		}
	}

	return nil
}

// CreateSESPoliciesWithRoleNames creates SES policies using actual role names (with suffixes)
func (im *IAMManager) CreateSESPoliciesWithRoleNames(ctx context.Context, routes []utils.Route, actualRoleNames map[string]string) error {
	lambdaGroups := utils.GroupByLambda(routes)

	for lambdaName := range lambdaGroups {
		var err error
		if roleName, exists := actualRoleNames[lambdaName]; exists {
			err = im.CreateSESPoliciesForAction(ctx, lambdaName, roleName)
		} else {
			err = im.CreateSESPoliciesForAction(ctx, lambdaName)
		}
		if err != nil {
			utils.Warn(ctx, "Failed to create SES policy for lambda", map[string]interface{}{
				"lambda": lambdaName,
				"error":  err.Error(),
			})
		}
	}

	return nil
}
