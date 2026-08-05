// internal/resources/trigger_manager.go
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"terraform-provider-conveyor-belt/internal/utils"
)

// TriggerRetryConfig returns the default retry configuration for trigger operations.
// Uses more conservative defaults than route processing since trigger operations
// are typically less rate-limited but may have longer propagation delays.
//
// Requirements: 6.5 - Retry with exponential backoff
func TriggerRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 2 * time.Second,
		MaxBackoff:     30 * time.Second,
		BackoffFactor:  2.0,
	}
}

// isRetryableError determines if an error is retryable.
// Retryable errors are transient failures that may succeed on retry:
// - TooManyRequestsException: AWS rate limiting
// - ServiceException: Transient AWS service errors
// - ThrottlingException: AWS throttling
// - RequestLimitExceeded: Request limit exceeded
// - ProvisionedThroughputExceededException: DynamoDB throughput exceeded
// - Network timeouts and connection errors
//
// Non-retryable errors (should NOT be retried):
// - ResourceConflictException: Resource already exists (handled as idempotent success)
// - ResourceNotFoundException: Resource doesn't exist (handled as idempotent success for deletes)
// - InvalidParameterValueException: Invalid configuration (user error)
// - ValidationException: Validation failed (user error)
//
// Requirements: 6.5 - Retry with exponential backoff for retryable errors
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// Retryable AWS errors
	retryablePatterns := []string{
		"TooManyRequestsException",
		"ServiceException",
		"ThrottlingException",
		"RequestLimitExceeded",
		"ProvisionedThroughputExceededException",
		"InternalServiceError",
		"ServiceUnavailable",
		// Network-related errors
		"connection reset",
		"connection refused",
		"timeout",
		"i/o timeout",
		"net/http: request canceled",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// withRetry executes an operation with exponential backoff retry logic.
// It retries the operation up to MaxRetries times if the error is retryable.
// The backoff duration increases exponentially after each attempt, up to MaxBackoff.
//
// Parameters:
// - ctx: Context for cancellation
// - config: Retry configuration (use TriggerRetryConfig() for defaults)
// - operationName: Name of the operation for logging
// - operation: The function to execute
//
// Returns:
// - nil if the operation succeeds
// - The last error if all retries are exhausted
// - ctx.Err() if the context is cancelled
//
// Requirements: 6.5 - Retry with exponential backoff
func withRetry(ctx context.Context, config *RetryConfig, operationName string, operation func() error) error {
	var lastErr error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		// Execute the operation
		err := operation()
		if err == nil {
			// Success
			if attempt > 0 {
				utils.Info(ctx, "Operation succeeded after retry", map[string]interface{}{
					"operation": operationName,
					"attempt":   attempt + 1,
				})
			}
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			// Non-retryable error, return immediately
			return err
		}

		// Check if we've exhausted retries
		if attempt >= config.MaxRetries {
			utils.Warn(ctx, "Operation failed after all retries", map[string]interface{}{
				"operation":   operationName,
				"max_retries": config.MaxRetries,
				"error":       err.Error(),
			})
			return fmt.Errorf("operation %s failed after %d retries: %w", operationName, config.MaxRetries, lastErr)
		}

		// Calculate backoff duration with exponential increase
		// backoff = initialBackoff * backoffFactor^attempt, capped at maxBackoff
		backoff := time.Duration(float64(config.InitialBackoff) * pow(config.BackoffFactor, float64(attempt)))
		if backoff > config.MaxBackoff {
			backoff = config.MaxBackoff
		}

		utils.Warn(ctx, "Retryable error, backing off", map[string]interface{}{
			"operation": operationName,
			"attempt":   attempt + 1,
			"backoff":   backoff.String(),
			"error":     err.Error(),
		})

		// Wait for backoff duration or context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			// Continue to next retry
		}
	}

	return lastErr
}

// pow calculates base^exp for float64 values.
// This is a simple implementation for exponential backoff calculation.
func pow(base, exp float64) float64 {
	result := 1.0
	for i := 0; i < int(exp); i++ {
		result *= base
	}
	return result
}

// TriggerManager handles SNS and SQS trigger lifecycle management.
// It is responsible for creating, updating, and deleting triggers
// when lambda_config changes, ensuring AWS state matches configuration.
type TriggerManager struct {
	lambdaClient *lambda.Client
	snsClient    *sns.Client
	config       *DispatcherConfig
	retryConfig  *RetryConfig // Configuration for retry behavior
}

// SNSTriggerConfig represents an SNS trigger configuration from lambda_config.
// This is the desired state that should be reconciled with AWS.
//
// Requirements: 1.1 - SNS triggers are created from lambda_config.{action}.sns_triggers
type SNSTriggerConfig struct {
	TopicArn    string
	StatementId string // Optional, auto-generated if not provided
}

// ExistingSNSTrigger represents an SNS trigger found in AWS.
// This is the current state that needs to be compared with desired state.
//
// Requirements: 4.1 - Query existing SNS subscriptions for the Lambda
type ExistingSNSTrigger struct {
	TopicArn        string
	SubscriptionArn string
	StatementId     string
}

// NewTriggerManager creates a new TriggerManager.
// It takes ResourceClients (which contains all AWS service clients) and
// DispatcherConfig (which contains app configuration including lambda_config).
//
// Requirements: 5.1 - TriggerManager is called during Lambda updates
func NewTriggerManager(clients *ResourceClients, config *DispatcherConfig) *TriggerManager {
	return &TriggerManager{
		lambdaClient: clients.Lambda,
		snsClient:    clients.SNS,
		config:       config,
		retryConfig:  TriggerRetryConfig(),
	}
}

// NewTriggerManagerWithRetryConfig creates a new TriggerManager with custom retry configuration.
// This is useful for testing or when different retry behavior is needed.
//
// Requirements: 5.1, 6.5 - TriggerManager with configurable retry behavior
func NewTriggerManagerWithRetryConfig(clients *ResourceClients, config *DispatcherConfig, retryConfig *RetryConfig) *TriggerManager {
	return &TriggerManager{
		lambdaClient: clients.Lambda,
		snsClient:    clients.SNS,
		config:       config,
		retryConfig:  retryConfig,
	}
}

// extractSNSTriggers parses SNS triggers from lambda_config for a specific lambda.
// It looks up lambda_config.{lambdaName}.sns_triggers and converts each entry
// to an SNSTriggerConfig struct.
//
// The function handles both Terraform framework types (types.Object, types.Tuple)
// and plain Go types (map[string]interface{}, []interface{}).
//
// Requirements: 1.1 - Parse sns_triggers from lambda_config
func extractSNSTriggers(lambdaConfig map[string]interface{}, lambdaName string) []SNSTriggerConfig {
	var triggers []SNSTriggerConfig

	// Look up lambda-specific config: lambda_config.{lambdaName}
	lambdaConfigRaw, exists := lambdaConfig[lambdaName]
	if !exists {
		return triggers
	}

	// Extract the map from the lambda config
	lambdaCfg, ok := extractMapValue(lambdaConfigRaw)
	if !ok {
		return triggers
	}

	// Look up sns_triggers within the lambda config
	snsTriggersRaw, exists := lambdaCfg["sns_triggers"]
	if !exists {
		return triggers
	}

	// Convert to list - handles both Terraform types and plain Go types
	triggersList := convertTriggersToList(snsTriggersRaw)

	for _, triggerRaw := range triggersList {
		// Each trigger should be a map with topic_arn and optional statement_id
		triggerMap, ok := extractMapValue(triggerRaw)
		if !ok {
			// Try direct map[string]interface{} assertion
			if tm, ok := triggerRaw.(map[string]interface{}); ok {
				triggerMap = tm
			} else {
				continue
			}
		}

		// Extract topic_arn (required)
		topicArn := extractTriggerStringField(triggerMap, "topic_arn")
		if topicArn == "" {
			continue
		}

		// Extract statement_id (optional, auto-generated if not provided)
		statementId := extractTriggerStringField(triggerMap, "statement_id")
		if statementId == "" {
			// Auto-generate statement_id from topic ARN
			// Format: sns-{sanitized-topic-arn}
			statementId = generateSNSStatementId(topicArn)
		}

		triggers = append(triggers, SNSTriggerConfig{
			TopicArn:    topicArn,
			StatementId: statementId,
		})
	}

	return triggers
}

// convertTriggersToList converts trigger configuration to a list of interfaces.
// It handles both Terraform framework types (types.Tuple, types.List) and
// plain Go types ([]interface{}, []map[string]interface{}).
func convertTriggersToList(value interface{}) []interface{} {
	// First try convertToStableList for Terraform types
	if result := convertToStableList(value); len(result) > 0 {
		return result
	}

	// Handle plain Go slice types (used in tests and some code paths)
	switch v := value.(type) {
	case []interface{}:
		return v
	case []map[string]interface{}:
		result := make([]interface{}, len(v))
		for i, m := range v {
			result[i] = m
		}
		return result
	}

	return nil
}

// extractTriggerStringField extracts a string field from a trigger map.
// It handles both Terraform framework types and plain Go strings.
func extractTriggerStringField(triggerMap map[string]interface{}, fieldName string) string {
	fieldRaw, exists := triggerMap[fieldName]
	if !exists {
		return ""
	}

	// Try extractStringValue which handles Terraform types
	if strVal, ok := extractStringValue(fieldRaw); ok {
		return strVal
	}

	// Try direct string assertion
	if strVal, ok := fieldRaw.(string); ok {
		return strVal
	}

	return ""
}

// generateSNSStatementId generates a statement ID for an SNS trigger.
// The format is: sns-{sanitized-topic-arn}
// This matches the existing pattern in parallel_manager.go setupSNSTriggers.
func generateSNSStatementId(topicArn string) string {
	// Replace colons with dashes to create a valid statement ID
	// Example: arn:aws:sns:us-east-1:123456789:my-topic -> sns-arn-aws-sns-us-east-1-123456789-my-topic
	sanitized := strings.ReplaceAll(topicArn, ":", "-")
	return fmt.Sprintf("sns-%s", sanitized)
}

// SQSTriggerConfig represents an SQS trigger configuration from lambda_config.
// This is the desired state that should be reconciled with AWS.
//
// Requirements: 2.1 - SQS triggers are created from lambda_config.{action}.sqs_triggers
type SQSTriggerConfig struct {
	QueueArn  string
	BatchSize int32 // Default: 10
	Enabled   bool  // Default: true
}

// ExistingSQSTrigger represents an SQS trigger found in AWS.
// This is the current state that needs to be compared with desired state.
//
// Requirements: 4.2 - Query existing event source mappings for the Lambda
type ExistingSQSTrigger struct {
	QueueArn  string
	UUID      string
	BatchSize int32
	Enabled   bool
}

// extractSQSTriggers parses SQS triggers from lambda_config for a specific lambda.
// It looks up lambda_config.{lambdaName}.sqs_triggers and converts each entry
// to an SQSTriggerConfig struct.
//
// The function handles both Terraform framework types (types.Object, types.Tuple)
// and plain Go types (map[string]interface{}, []interface{}).
//
// Requirements: 2.1 - Parse sqs_triggers from lambda_config
func extractSQSTriggers(lambdaConfig map[string]interface{}, lambdaName string) []SQSTriggerConfig {
	var triggers []SQSTriggerConfig

	// Look up lambda-specific config: lambda_config.{lambdaName}
	lambdaConfigRaw, exists := lambdaConfig[lambdaName]
	if !exists {
		return triggers
	}

	// Extract the map from the lambda config
	lambdaCfg, ok := extractMapValue(lambdaConfigRaw)
	if !ok {
		return triggers
	}

	// Look up sqs_triggers within the lambda config
	sqsTriggersRaw, exists := lambdaCfg["sqs_triggers"]
	if !exists {
		return triggers
	}

	// Convert to list - handles both Terraform types and plain Go types
	triggersList := convertTriggersToList(sqsTriggersRaw)

	for _, triggerRaw := range triggersList {
		// Each trigger should be a map with queue_arn and optional batch_size, enabled
		triggerMap, ok := extractMapValue(triggerRaw)
		if !ok {
			// Try direct map[string]interface{} assertion
			if tm, ok := triggerRaw.(map[string]interface{}); ok {
				triggerMap = tm
			} else {
				continue
			}
		}

		// Extract queue_arn (required)
		queueArn := extractTriggerStringField(triggerMap, "queue_arn")
		if queueArn == "" {
			continue
		}

		// Extract batch_size (optional, default: 10)
		batchSize := int32(10)
		if batchSizeRaw, exists := triggerMap["batch_size"]; exists {
			if bsVal, ok := extractInt64Value(batchSizeRaw); ok {
				batchSize = int32(bsVal)
			} else if bsInt, ok := batchSizeRaw.(int); ok {
				batchSize = int32(bsInt)
			} else if bsInt32, ok := batchSizeRaw.(int32); ok {
				batchSize = bsInt32
			} else if bsInt64, ok := batchSizeRaw.(int64); ok {
				batchSize = int32(bsInt64)
			}
		}

		// Extract enabled (optional, default: true)
		enabled := true
		if enabledRaw, exists := triggerMap["enabled"]; exists {
			if enabledVal, ok := enabledRaw.(bool); ok {
				enabled = enabledVal
			}
		}

		triggers = append(triggers, SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: batchSize,
			Enabled:   enabled,
		})
	}

	return triggers
}

// getExistingSQSTriggers queries AWS to find existing SQS triggers for a Lambda function.
// It uses the Lambda ListEventSourceMappings API to find event source mappings that
// connect SQS queues to the Lambda function.
//
// The function returns a slice of ExistingSQSTrigger structs that represent the current
// AWS state for SQS triggers on this Lambda.
//
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func (tm *TriggerManager) getExistingSQSTriggers(ctx context.Context, functionName string) ([]ExistingSQSTrigger, error) {
	var existingTriggers []ExistingSQSTrigger

	// Use paginator to handle multiple pages of results
	var marker *string

	for {
		// Call Lambda ListEventSourceMappings API with FunctionName filter
		output, err := tm.lambdaClient.ListEventSourceMappings(ctx, &lambda.ListEventSourceMappingsInput{
			FunctionName: aws.String(functionName),
			Marker:       marker,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list event source mappings: %w", err)
		}

		// Filter for SQS event sources and extract trigger information
		for _, mapping := range output.EventSourceMappings {
			// Skip non-SQS event sources
			if mapping.EventSourceArn == nil || !strings.HasPrefix(*mapping.EventSourceArn, "arn:aws:sqs:") {
				continue
			}

			// Extract batch size (default to 10 if not set)
			batchSize := int32(10)
			if mapping.BatchSize != nil {
				batchSize = *mapping.BatchSize
			}

			// Extract enabled state (default to true if not set)
			enabled := true
			if mapping.State != nil {
				// State can be: Creating, Enabling, Enabled, Disabling, Disabled, Updating, Deleting
				// Consider "Enabled" and "Enabling" as enabled, others as disabled
				enabled = *mapping.State == "Enabled" || *mapping.State == "Enabling"
			}

			trigger := ExistingSQSTrigger{
				QueueArn:  *mapping.EventSourceArn,
				UUID:      aws.ToString(mapping.UUID),
				BatchSize: batchSize,
				Enabled:   enabled,
			}

			existingTriggers = append(existingTriggers, trigger)
		}

		// Check if there are more results
		if output.NextMarker == nil {
			break
		}
		marker = output.NextMarker
	}

	utils.Debug(ctx, "Found existing SQS triggers", map[string]interface{}{
		"function": functionName,
		"count":    len(existingTriggers),
	})

	return existingTriggers, nil
}

// lambdaPolicy represents the structure of a Lambda resource-based policy.
// This is used to parse the JSON policy returned by GetPolicy API.
type lambdaPolicy struct {
	Version   string            `json:"Version"`
	Id        string            `json:"Id,omitempty"`
	Statement []policyStatement `json:"Statement"`
}

// policyStatement represents a single statement in a Lambda resource-based policy.
type policyStatement struct {
	Sid       string      `json:"Sid"`
	Effect    string      `json:"Effect"`
	Principal interface{} `json:"Principal"` // Can be string or map
	Action    string      `json:"Action"`
	Resource  string      `json:"Resource"`
	Condition *struct {
		ArnLike map[string]string `json:"ArnLike,omitempty"`
	} `json:"Condition,omitempty"`
}

// getExistingSNSTriggers queries AWS to find existing SNS triggers for a Lambda function.
// It combines information from two sources:
// 1. Lambda GetPolicy API - to find Lambda permissions that allow SNS to invoke the function
// 2. SNS ListSubscriptionsByTopic API - to find SNS subscriptions that target the Lambda
//
// The function returns a slice of ExistingSNSTrigger structs that represent the current
// AWS state for SNS triggers on this Lambda.
//
// Requirements: 4.1 - Query existing SNS subscriptions for the Lambda
func (tm *TriggerManager) getExistingSNSTriggers(ctx context.Context, functionName string, lambdaArn string) ([]ExistingSNSTrigger, error) {
	var existingTriggers []ExistingSNSTrigger

	// Step 1: Get Lambda permissions from the resource policy
	snsPermissions, err := tm.getSNSPermissionsFromPolicy(ctx, functionName)
	if err != nil {
		// If there's no policy, that's fine - just means no permissions exist
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			utils.Debug(ctx, "No resource policy found for Lambda", map[string]interface{}{
				"function": functionName,
			})
			return existingTriggers, nil
		}
		return nil, fmt.Errorf("failed to get Lambda policy: %w", err)
	}

	// Step 2: For each SNS permission, find the corresponding subscription
	for _, perm := range snsPermissions {
		trigger := ExistingSNSTrigger{
			TopicArn:    perm.TopicArn,
			StatementId: perm.StatementId,
		}

		// Try to find the subscription for this topic
		subscriptionArn, err := tm.findSNSSubscription(ctx, perm.TopicArn, lambdaArn)
		if err != nil {
			utils.Warn(ctx, "Failed to find SNS subscription", map[string]interface{}{
				"topic_arn":  perm.TopicArn,
				"lambda_arn": lambdaArn,
				"error":      err.Error(),
			})
			// Continue without subscription ARN - the permission still exists
		}
		trigger.SubscriptionArn = subscriptionArn

		existingTriggers = append(existingTriggers, trigger)
	}

	utils.Debug(ctx, "Found existing SNS triggers", map[string]interface{}{
		"function": functionName,
		"count":    len(existingTriggers),
	})

	return existingTriggers, nil
}

// snsPermission represents an SNS permission found in the Lambda policy.
type snsPermission struct {
	TopicArn    string
	StatementId string
}

// getSNSPermissionsFromPolicy retrieves the Lambda resource policy and extracts
// all SNS-related permissions (statements where Principal is sns.amazonaws.com).
func (tm *TriggerManager) getSNSPermissionsFromPolicy(ctx context.Context, functionName string) ([]snsPermission, error) {
	var permissions []snsPermission

	// Call Lambda GetPolicy API
	output, err := tm.lambdaClient.GetPolicy(ctx, &lambda.GetPolicyInput{
		FunctionName: aws.String(functionName),
	})
	if err != nil {
		return nil, err
	}

	if output.Policy == nil {
		return permissions, nil
	}

	// Parse the policy JSON
	var policy lambdaPolicy
	if err := json.Unmarshal([]byte(*output.Policy), &policy); err != nil {
		return nil, fmt.Errorf("failed to parse Lambda policy: %w", err)
	}

	// Find all SNS-related statements
	for _, stmt := range policy.Statement {
		if !isSNSPrincipal(stmt.Principal) {
			continue
		}

		// Extract the topic ARN from the condition
		topicArn := extractTopicArnFromStatement(stmt)
		if topicArn == "" {
			utils.Debug(ctx, "SNS permission without topic ARN condition", map[string]interface{}{
				"statement_id": stmt.Sid,
			})
			continue
		}

		permissions = append(permissions, snsPermission{
			TopicArn:    topicArn,
			StatementId: stmt.Sid,
		})
	}

	return permissions, nil
}

// isSNSPrincipal checks if the principal in a policy statement is SNS.
// The principal can be either a string "sns.amazonaws.com" or a map with
// "Service": "sns.amazonaws.com".
func isSNSPrincipal(principal interface{}) bool {
	switch p := principal.(type) {
	case string:
		return p == "sns.amazonaws.com"
	case map[string]interface{}:
		if service, ok := p["Service"]; ok {
			if serviceStr, ok := service.(string); ok {
				return serviceStr == "sns.amazonaws.com"
			}
		}
	}
	return false
}

// extractTopicArnFromStatement extracts the SNS topic ARN from a policy statement.
// The topic ARN is typically in the Condition.ArnLike["AWS:SourceArn"] field.
func extractTopicArnFromStatement(stmt policyStatement) string {
	if stmt.Condition == nil {
		return ""
	}
	if stmt.Condition.ArnLike == nil {
		return ""
	}
	// Check for AWS:SourceArn (case-insensitive key matching)
	for key, value := range stmt.Condition.ArnLike {
		if strings.EqualFold(key, "AWS:SourceArn") {
			return value
		}
	}
	return ""
}

// findSNSSubscription finds the SNS subscription ARN for a given topic and Lambda endpoint.
// It uses the SNS ListSubscriptionsByTopic API to find subscriptions where the
// endpoint matches the Lambda ARN.
func (tm *TriggerManager) findSNSSubscription(ctx context.Context, topicArn string, lambdaArn string) (string, error) {
	var nextToken *string

	for {
		output, err := tm.snsClient.ListSubscriptionsByTopic(ctx, &sns.ListSubscriptionsByTopicInput{
			TopicArn:  aws.String(topicArn),
			NextToken: nextToken,
		})
		if err != nil {
			return "", fmt.Errorf("failed to list subscriptions for topic %s: %w", topicArn, err)
		}

		// Look for a subscription with our Lambda as the endpoint
		for _, sub := range output.Subscriptions {
			if sub.Protocol != nil && *sub.Protocol == "lambda" {
				if sub.Endpoint != nil && *sub.Endpoint == lambdaArn {
					if sub.SubscriptionArn != nil {
						return *sub.SubscriptionArn, nil
					}
				}
			}
		}

		// Check if there are more results
		if output.NextToken == nil {
			break
		}
		nextToken = output.NextToken
	}

	// No subscription found - this is not an error, just means the subscription
	// doesn't exist (yet) or was deleted
	return "", nil
}

// SNSTriggerDiffResult contains the results of comparing desired vs existing SNS triggers.
// It categorizes triggers into three sets for reconciliation:
// - ToCreate: triggers in config but not in AWS
// - ToUpdate: triggers in both but with different settings (statement_id changed)
// - ToDelete: triggers in AWS but not in config
//
// Requirements: 1.2, 1.3, 4.3 - Identify triggers to create, update, and delete
type SNSTriggerDiffResult struct {
	ToCreate []SNSTriggerConfig
	ToUpdate []SNSTriggerConfig
	ToDelete []ExistingSNSTrigger
}

// createSNSTrigger creates an SNS trigger for a Lambda function.
// It performs two operations:
// 1. Adds a Lambda permission to allow SNS to invoke the function
// 2. Subscribes the Lambda to the SNS topic
//
// The function is idempotent:
// - If the Lambda permission already exists (ResourceConflictException), it's treated as success (Requirement 6.1)
// - If the SNS subscription already exists, the Subscribe API returns the existing subscription ARN (Requirement 6.2)
//
// Both operations are wrapped with retry logic for transient failures (Requirement 6.5).
//
// Requirements: 1.1, 6.1, 6.2, 6.5
func (tm *TriggerManager) createSNSTrigger(ctx context.Context, functionName string, lambdaArn string, trigger SNSTriggerConfig) error {
	utils.Info(ctx, "Creating SNS trigger", map[string]interface{}{
		"function":     functionName,
		"topic_arn":    trigger.TopicArn,
		"statement_id": trigger.StatementId,
	})

	// Step 1: Add Lambda permission for SNS to invoke the function
	// This creates a resource-based policy statement on the Lambda
	// Wrapped with retry for transient failures (Requirement 6.5)
	err := withRetry(ctx, tm.retryConfig, "AddPermission", func() error {
		_, err := tm.lambdaClient.AddPermission(ctx, &lambda.AddPermissionInput{
			FunctionName: aws.String(functionName),
			StatementId:  aws.String(trigger.StatementId),
			Action:       aws.String("lambda:InvokeFunction"),
			Principal:    aws.String("sns.amazonaws.com"),
			SourceArn:    aws.String(trigger.TopicArn),
		})
		if err != nil {
			// Check if permission already exists - this is idempotent success (Requirement 6.1)
			// Don't retry for ResourceConflictException
			if strings.Contains(err.Error(), "ResourceConflictException") {
				utils.Info(ctx, "SNS permission already exists, treating as success", map[string]interface{}{
					"function":     functionName,
					"topic_arn":    trigger.TopicArn,
					"statement_id": trigger.StatementId,
				})
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to add Lambda permission for SNS trigger: %w", err)
	}

	// Step 2: Subscribe Lambda to SNS topic
	// The SNS Subscribe API is idempotent - if a subscription already exists for the
	// same topic/protocol/endpoint combination, it returns the existing subscription ARN
	// rather than creating a duplicate (Requirement 6.2)
	// Wrapped with retry for transient failures (Requirement 6.5)
	err = withRetry(ctx, tm.retryConfig, "SNSSubscribe", func() error {
		_, err := tm.snsClient.Subscribe(ctx, &sns.SubscribeInput{
			TopicArn: aws.String(trigger.TopicArn),
			Protocol: aws.String("lambda"),
			Endpoint: aws.String(lambdaArn),
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe Lambda to SNS topic: %w", err)
	}

	utils.Info(ctx, "Successfully created SNS trigger", map[string]interface{}{
		"function":  functionName,
		"topic_arn": trigger.TopicArn,
	})

	return nil
}

// updateSNSTrigger updates an SNS trigger when the statement_id changes.
// Since Lambda permissions can't be updated in-place, this performs a delete + create:
// 1. Remove the old Lambda permission using RemovePermission API with the old statement_id
// 2. Add the new Lambda permission using AddPermission API with the new statement_id
// 3. The SNS subscription doesn't need to change (it's based on topic_arn, not statement_id)
//
// Both operations are wrapped with retry logic for transient failures (Requirement 6.5).
//
// Requirements: 1.4, 6.5 - When an SNS trigger's statement_id changes, update the Lambda permission
func (tm *TriggerManager) updateSNSTrigger(ctx context.Context, functionName string, lambdaArn string, trigger SNSTriggerConfig, oldStatementId string) error {
	utils.Info(ctx, "Updating SNS trigger (statement_id change)", map[string]interface{}{
		"function":         functionName,
		"topic_arn":        trigger.TopicArn,
		"old_statement_id": oldStatementId,
		"new_statement_id": trigger.StatementId,
	})

	// Step 1: Remove the old Lambda permission
	// We ignore ResourceNotFoundException since the permission might already be gone
	// Wrapped with retry for transient failures (Requirement 6.5)
	err := withRetry(ctx, tm.retryConfig, "RemovePermission", func() error {
		_, err := tm.lambdaClient.RemovePermission(ctx, &lambda.RemovePermissionInput{
			FunctionName: aws.String(functionName),
			StatementId:  aws.String(oldStatementId),
		})
		if err != nil {
			// Check if permission doesn't exist - this is idempotent success (Requirement 6.4)
			// Don't retry for ResourceNotFoundException
			if strings.Contains(err.Error(), "ResourceNotFoundException") {
				utils.Info(ctx, "Old SNS permission not found, treating as success", map[string]interface{}{
					"function":     functionName,
					"statement_id": oldStatementId,
				})
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to remove old Lambda permission for SNS trigger: %w", err)
	}

	// Step 2: Add the new Lambda permission with the new statement_id
	// Wrapped with retry for transient failures (Requirement 6.5)
	err = withRetry(ctx, tm.retryConfig, "AddPermission", func() error {
		_, err := tm.lambdaClient.AddPermission(ctx, &lambda.AddPermissionInput{
			FunctionName: aws.String(functionName),
			StatementId:  aws.String(trigger.StatementId),
			Action:       aws.String("lambda:InvokeFunction"),
			Principal:    aws.String("sns.amazonaws.com"),
			SourceArn:    aws.String(trigger.TopicArn),
		})
		if err != nil {
			// Check if permission already exists - this is idempotent success (Requirement 6.1)
			// Don't retry for ResourceConflictException
			if strings.Contains(err.Error(), "ResourceConflictException") {
				utils.Info(ctx, "New SNS permission already exists, treating as success", map[string]interface{}{
					"function":     functionName,
					"topic_arn":    trigger.TopicArn,
					"statement_id": trigger.StatementId,
				})
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to add new Lambda permission for SNS trigger: %w", err)
	}

	// Note: The SNS subscription doesn't need to change since it's based on topic_arn,
	// not statement_id. The subscription connects the topic to the Lambda endpoint,
	// and that relationship remains the same.

	utils.Info(ctx, "Successfully updated SNS trigger", map[string]interface{}{
		"function":         functionName,
		"topic_arn":        trigger.TopicArn,
		"new_statement_id": trigger.StatementId,
	})

	return nil
}

// deleteSNSTrigger deletes an SNS trigger from a Lambda function.
// It performs two operations:
// 1. Removes the Lambda permission using RemovePermission API
// 2. Unsubscribes from the SNS topic using Unsubscribe API
//
// The function is idempotent (Requirement 6.4):
// - If the Lambda permission doesn't exist (ResourceNotFoundException), it's treated as success
// - If the SNS subscription doesn't exist (ResourceNotFoundException or InvalidParameter), it's treated as success
//
// Both operations are wrapped with retry logic for transient failures (Requirement 6.5).
//
// Requirements: 1.2, 6.4, 6.5
func (tm *TriggerManager) deleteSNSTrigger(ctx context.Context, functionName string, trigger ExistingSNSTrigger) error {
	utils.Info(ctx, "Deleting SNS trigger", map[string]interface{}{
		"function":         functionName,
		"topic_arn":        trigger.TopicArn,
		"statement_id":     trigger.StatementId,
		"subscription_arn": trigger.SubscriptionArn,
	})

	// Step 1: Remove Lambda permission for SNS to invoke the function
	// This removes the resource-based policy statement from the Lambda
	// Wrapped with retry for transient failures (Requirement 6.5)
	err := withRetry(ctx, tm.retryConfig, "RemovePermission", func() error {
		_, err := tm.lambdaClient.RemovePermission(ctx, &lambda.RemovePermissionInput{
			FunctionName: aws.String(functionName),
			StatementId:  aws.String(trigger.StatementId),
		})
		if err != nil {
			// Check if permission doesn't exist - this is idempotent success (Requirement 6.4)
			// Don't retry for ResourceNotFoundException
			if strings.Contains(err.Error(), "ResourceNotFoundException") {
				utils.Info(ctx, "SNS permission not found, treating as success (idempotent)", map[string]interface{}{
					"function":     functionName,
					"topic_arn":    trigger.TopicArn,
					"statement_id": trigger.StatementId,
				})
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to remove Lambda permission for SNS trigger: %w", err)
	}

	// Step 2: Unsubscribe from SNS topic
	// Only attempt if we have a subscription ARN
	// Wrapped with retry for transient failures (Requirement 6.5)
	if trigger.SubscriptionArn != "" {
		err = withRetry(ctx, tm.retryConfig, "SNSUnsubscribe", func() error {
			_, err := tm.snsClient.Unsubscribe(ctx, &sns.UnsubscribeInput{
				SubscriptionArn: aws.String(trigger.SubscriptionArn),
			})
			if err != nil {
				// Check if subscription doesn't exist - this is idempotent success (Requirement 6.4)
				// SNS can return ResourceNotFoundException or InvalidParameter for non-existent subscriptions
				// Don't retry for these errors
				if strings.Contains(err.Error(), "ResourceNotFoundException") ||
					strings.Contains(err.Error(), "InvalidParameter") {
					utils.Info(ctx, "SNS subscription not found, treating as success (idempotent)", map[string]interface{}{
						"function":         functionName,
						"topic_arn":        trigger.TopicArn,
						"subscription_arn": trigger.SubscriptionArn,
					})
					return nil
				}
				return err
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to unsubscribe Lambda from SNS topic: %w", err)
		}
	} else {
		utils.Debug(ctx, "No subscription ARN to unsubscribe", map[string]interface{}{
			"function":  functionName,
			"topic_arn": trigger.TopicArn,
		})
	}

	utils.Info(ctx, "Successfully deleted SNS trigger", map[string]interface{}{
		"function":  functionName,
		"topic_arn": trigger.TopicArn,
	})

	return nil
}

// ReconcileSNSTriggers manages the full lifecycle of SNS triggers for a Lambda function.
// It queries the current AWS state, compares it with the desired configuration, and
// executes the necessary create/update/delete operations to reconcile the state.
//
// The function is designed to be resilient:
// - It continues processing remaining triggers even if individual operations fail (Requirement 1.5)
// - It only returns an error if ALL operations fail
// - Individual failures are logged as warnings
//
// The function is idempotent (Requirement 4.5):
// - Running it multiple times with the same configuration produces the same result
// - This is achieved through idempotent create/update/delete operations
//
// Requirements: 1.5, 4.5
func (tm *TriggerManager) ReconcileSNSTriggers(
	ctx context.Context,
	functionName string,
	lambdaArn string,
	desiredTriggers []SNSTriggerConfig,
) error {
	utils.Info(ctx, "Reconciling SNS triggers", map[string]interface{}{
		"function":       functionName,
		"desired_count":  len(desiredTriggers),
	})

	// Step 1: Get existing SNS triggers from AWS
	existingTriggers, err := tm.getExistingSNSTriggers(ctx, functionName, lambdaArn)
	if err != nil {
		return fmt.Errorf("failed to get existing SNS triggers: %w", err)
	}

	utils.Debug(ctx, "Found existing SNS triggers", map[string]interface{}{
		"function":       functionName,
		"existing_count": len(existingTriggers),
	})

	// Step 2: Diff desired vs existing to determine actions
	diff := diffSNSTriggers(desiredTriggers, existingTriggers)

	utils.Debug(ctx, "SNS trigger diff result", map[string]interface{}{
		"function":  functionName,
		"to_create": len(diff.ToCreate),
		"to_update": len(diff.ToUpdate),
		"to_delete": len(diff.ToDelete),
	})

	// Track success/failure counts for final error determination
	totalOperations := len(diff.ToDelete) + len(diff.ToUpdate) + len(diff.ToCreate)
	if totalOperations == 0 {
		utils.Info(ctx, "No SNS trigger changes needed", map[string]interface{}{
			"function": functionName,
		})
		return nil
	}

	successCount := 0
	var lastError error

	// Build a map of existing triggers by topic ARN for looking up old statement IDs during updates
	existingByTopicArn := make(map[string]ExistingSNSTrigger)
	for _, trigger := range existingTriggers {
		existingByTopicArn[trigger.TopicArn] = trigger
	}

	// Step 3: Delete orphaned triggers first
	// We delete first to avoid potential conflicts with statement IDs
	for _, trigger := range diff.ToDelete {
		err := tm.deleteSNSTrigger(ctx, functionName, trigger)
		if err != nil {
			// Log warning but continue with other triggers (Requirement 1.5)
			utils.Warn(ctx, "Failed to delete SNS trigger, continuing with others", map[string]interface{}{
				"function":  functionName,
				"topic_arn": trigger.TopicArn,
				"error":     err.Error(),
			})
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 4: Update triggers with changed statement_id
	for _, trigger := range diff.ToUpdate {
		// Look up the old statement ID from existing triggers
		existingTrigger, exists := existingByTopicArn[trigger.TopicArn]
		if !exists {
			// This shouldn't happen since diff only includes triggers that exist
			utils.Warn(ctx, "Cannot find existing trigger for update, skipping", map[string]interface{}{
				"function":  functionName,
				"topic_arn": trigger.TopicArn,
			})
			lastError = fmt.Errorf("existing trigger not found for topic %s", trigger.TopicArn)
			continue
		}

		err := tm.updateSNSTrigger(ctx, functionName, lambdaArn, trigger, existingTrigger.StatementId)
		if err != nil {
			// Log warning but continue with other triggers (Requirement 1.5)
			utils.Warn(ctx, "Failed to update SNS trigger, continuing with others", map[string]interface{}{
				"function":         functionName,
				"topic_arn":        trigger.TopicArn,
				"old_statement_id": existingTrigger.StatementId,
				"new_statement_id": trigger.StatementId,
				"error":            err.Error(),
			})
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 5: Create new triggers
	for _, trigger := range diff.ToCreate {
		err := tm.createSNSTrigger(ctx, functionName, lambdaArn, trigger)
		if err != nil {
			// Log warning but continue with other triggers (Requirement 1.5)
			utils.Warn(ctx, "Failed to create SNS trigger, continuing with others", map[string]interface{}{
				"function":     functionName,
				"topic_arn":    trigger.TopicArn,
				"statement_id": trigger.StatementId,
				"error":        err.Error(),
			})
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 6: Determine final result
	// Only return error if ALL operations failed
	if successCount == 0 && totalOperations > 0 {
		return fmt.Errorf("all SNS trigger operations failed, last error: %w", lastError)
	}

	if lastError != nil {
		utils.Warn(ctx, "Some SNS trigger operations failed", map[string]interface{}{
			"function":         functionName,
			"success_count":    successCount,
			"total_operations": totalOperations,
		})
	} else {
		utils.Info(ctx, "Successfully reconciled SNS triggers", map[string]interface{}{
			"function":         functionName,
			"success_count":    successCount,
			"total_operations": totalOperations,
		})
	}

	return nil
}

// diffSNSTriggers compares desired SNS triggers (from config) with existing triggers (from AWS)
// and returns the actions needed to reconcile them.
//
// The primary key for comparison is topic_arn. The function:
// - Identifies triggers that need to be created (in desired but not in existing)
// - Identifies triggers that need to be updated (in both but with different statement_id)
// - Identifies triggers that need to be deleted (in existing but not in desired)
//
// Requirements:
// - 1.2: When an SNS trigger is removed from config, it should be deleted
// - 1.3: When topic_arn changes, old trigger is deleted and new one created
// - 4.3: When an SNS subscription exists in AWS but not in config, delete it
func diffSNSTriggers(desired []SNSTriggerConfig, existing []ExistingSNSTrigger) SNSTriggerDiffResult {
	result := SNSTriggerDiffResult{
		ToCreate: []SNSTriggerConfig{},
		ToUpdate: []SNSTriggerConfig{},
		ToDelete: []ExistingSNSTrigger{},
	}

	// Build a map of existing triggers by topic_arn for O(1) lookup
	existingByTopicArn := make(map[string]ExistingSNSTrigger)
	for _, trigger := range existing {
		existingByTopicArn[trigger.TopicArn] = trigger
	}

	// Build a set of desired topic ARNs for checking orphans
	desiredTopicArns := make(map[string]bool)
	for _, trigger := range desired {
		desiredTopicArns[trigger.TopicArn] = true
	}

	// Process desired triggers - determine if they need to be created or updated
	for _, desiredTrigger := range desired {
		existingTrigger, exists := existingByTopicArn[desiredTrigger.TopicArn]

		if !exists {
			// Trigger doesn't exist in AWS - needs to be created
			result.ToCreate = append(result.ToCreate, desiredTrigger)
		} else {
			// Trigger exists - check if statement_id changed (needs update)
			if desiredTrigger.StatementId != existingTrigger.StatementId {
				result.ToUpdate = append(result.ToUpdate, desiredTrigger)
			}
			// If statement_id is the same, no action needed
		}
	}

	// Process existing triggers - find orphans that need to be deleted
	for _, existingTrigger := range existing {
		if !desiredTopicArns[existingTrigger.TopicArn] {
			// Trigger exists in AWS but not in config - needs to be deleted
			result.ToDelete = append(result.ToDelete, existingTrigger)
		}
	}

	return result
}

// SQSTriggerDiffResult contains the results of comparing desired vs existing SQS triggers.
// It categorizes triggers into three sets for reconciliation:
// - ToCreate: triggers in config but not in AWS
// - ToUpdate: triggers in both but with different settings (batch_size or enabled changed)
// - ToDelete: triggers in AWS but not in config
//
// Requirements: 2.2, 2.4, 4.4 - Identify triggers to create, update, and delete
type SQSTriggerDiffResult struct {
	ToCreate []SQSTriggerConfig
	ToUpdate []SQSTriggerUpdate
	ToDelete []ExistingSQSTrigger
}

// createSQSTrigger creates an SQS trigger (event source mapping) for a Lambda function.
// It creates an event source mapping that connects the SQS queue to the Lambda.
//
// The function is idempotent (Requirement 6.3):
// - If an event source mapping already exists for the same queue (ResourceConflictException),
//   it finds the existing mapping and updates it rather than failing
//
// The operation is wrapped with retry logic for transient failures (Requirement 6.5).
//
// Returns the UUID of the created or updated event source mapping.
//
// Requirements: 2.1, 6.3, 6.5
func (tm *TriggerManager) createSQSTrigger(ctx context.Context, functionName string, trigger SQSTriggerConfig) (string, error) {
	utils.Info(ctx, "Creating SQS trigger", map[string]interface{}{
		"function":   functionName,
		"queue_arn":  trigger.QueueArn,
		"batch_size": trigger.BatchSize,
		"enabled":    trigger.Enabled,
	})

	var resultUUID string

	// Try to create the event source mapping
	// Wrapped with retry for transient failures (Requirement 6.5)
	err := withRetry(ctx, tm.retryConfig, "CreateEventSourceMapping", func() error {
		output, err := tm.lambdaClient.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
			FunctionName:   aws.String(functionName),
			EventSourceArn: aws.String(trigger.QueueArn),
			BatchSize:      aws.Int32(trigger.BatchSize),
			Enabled:        aws.Bool(trigger.Enabled),
		})

		if err != nil {
			// Check if mapping already exists - handle idempotently (Requirement 6.3)
			// Don't retry for ResourceConflictException - handle it specially
			if strings.Contains(err.Error(), "ResourceConflictException") {
				utils.Info(ctx, "SQS event source mapping already exists, updating instead", map[string]interface{}{
					"function":  functionName,
					"queue_arn": trigger.QueueArn,
				})

				// Find the existing mapping to get its UUID
				existingUUID, findErr := tm.findExistingSQSMappingUUID(ctx, functionName, trigger.QueueArn)
				if findErr != nil {
					return fmt.Errorf("failed to find existing event source mapping: %w", findErr)
				}

				if existingUUID == "" {
					return fmt.Errorf("ResourceConflictException but could not find existing mapping for queue %s", trigger.QueueArn)
				}

				// Update the existing mapping with the desired configuration
				updateOutput, updateErr := tm.lambdaClient.UpdateEventSourceMapping(ctx, &lambda.UpdateEventSourceMappingInput{
					UUID:      aws.String(existingUUID),
					BatchSize: aws.Int32(trigger.BatchSize),
					Enabled:   aws.Bool(trigger.Enabled),
				})
				if updateErr != nil {
					return fmt.Errorf("failed to update existing event source mapping: %w", updateErr)
				}

				utils.Info(ctx, "Successfully updated existing SQS trigger", map[string]interface{}{
					"function":  functionName,
					"queue_arn": trigger.QueueArn,
					"uuid":      existingUUID,
				})

				resultUUID = aws.ToString(updateOutput.UUID)
				return nil
			}

			return err
		}

		resultUUID = aws.ToString(output.UUID)
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to create event source mapping: %w", err)
	}

	utils.Info(ctx, "Successfully created SQS trigger", map[string]interface{}{
		"function":  functionName,
		"queue_arn": trigger.QueueArn,
		"uuid":      resultUUID,
	})

	return resultUUID, nil
}

// findExistingSQSMappingUUID finds the UUID of an existing event source mapping
// for a specific queue ARN. This is used when handling ResourceConflictException
// to find the existing mapping that needs to be updated.
func (tm *TriggerManager) findExistingSQSMappingUUID(ctx context.Context, functionName string, queueArn string) (string, error) {
	existingTriggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		return "", err
	}

	for _, trigger := range existingTriggers {
		if trigger.QueueArn == queueArn {
			return trigger.UUID, nil
		}
	}

	return "", nil
}

// SQSTriggerUpdate represents an SQS trigger that needs to be updated.
// It contains both the desired configuration and the UUID of the existing
// event source mapping that needs to be updated.
type SQSTriggerUpdate struct {
	Config SQSTriggerConfig
	UUID   string // UUID of the existing event source mapping to update
}

// diffSQSTriggers compares desired SQS triggers (from config) with existing triggers (from AWS)
// and returns the actions needed to reconcile them.
//
// The primary key for comparison is queue_arn. The function:
// - Identifies triggers that need to be created (in desired but not in existing)
// - Identifies triggers that need to be updated (in both but with different batch_size or enabled)
// - Identifies triggers that need to be deleted (in existing but not in desired)
//
// Requirements:
// - 2.2: When an SQS trigger is removed from config, it should be deleted
// - 2.4: When queue_arn changes, old trigger is deleted and new one created
// - 4.4: When an event source mapping exists in AWS but not in config, delete it
func diffSQSTriggers(desired []SQSTriggerConfig, existing []ExistingSQSTrigger) SQSTriggerDiffResult {
	result := SQSTriggerDiffResult{
		ToCreate: []SQSTriggerConfig{},
		ToUpdate: []SQSTriggerUpdate{},
		ToDelete: []ExistingSQSTrigger{},
	}

	// Build a map of existing triggers by queue_arn for O(1) lookup
	existingByQueueArn := make(map[string]ExistingSQSTrigger)
	for _, trigger := range existing {
		existingByQueueArn[trigger.QueueArn] = trigger
	}

	// Build a set of desired queue ARNs for checking orphans
	desiredQueueArns := make(map[string]bool)
	for _, trigger := range desired {
		desiredQueueArns[trigger.QueueArn] = true
	}

	// Process desired triggers - determine if they need to be created or updated
	for _, desiredTrigger := range desired {
		existingTrigger, exists := existingByQueueArn[desiredTrigger.QueueArn]

		if !exists {
			// Trigger doesn't exist in AWS - needs to be created
			result.ToCreate = append(result.ToCreate, desiredTrigger)
		} else {
			// Trigger exists - check if batch_size or enabled changed (needs update)
			if desiredTrigger.BatchSize != existingTrigger.BatchSize ||
				desiredTrigger.Enabled != existingTrigger.Enabled {
				result.ToUpdate = append(result.ToUpdate, SQSTriggerUpdate{
					Config: desiredTrigger,
					UUID:   existingTrigger.UUID,
				})
			}
			// If batch_size and enabled are the same, no action needed
		}
	}

	// Process existing triggers - find orphans that need to be deleted
	for _, existingTrigger := range existing {
		if !desiredQueueArns[existingTrigger.QueueArn] {
			// Trigger exists in AWS but not in config - needs to be deleted
			result.ToDelete = append(result.ToDelete, existingTrigger)
		}
	}

	return result
}

// updateSQSTrigger updates an existing SQS trigger (event source mapping) when
// batch_size or enabled changes. It uses the Lambda UpdateEventSourceMapping API
// to modify the existing mapping in place.
//
// The function takes an SQSTriggerUpdate which contains:
// - Config: The desired SQSTriggerConfig with the new batch_size and enabled values
// - UUID: The UUID of the existing event source mapping to update
//
// The operation is wrapped with retry logic for transient failures (Requirement 6.5).
//
// Requirements: 2.3, 6.5 - When an SQS trigger's batch_size changes, update the existing event source mapping
func (tm *TriggerManager) updateSQSTrigger(ctx context.Context, update SQSTriggerUpdate) error {
	utils.Info(ctx, "Updating SQS trigger", map[string]interface{}{
		"queue_arn":  update.Config.QueueArn,
		"uuid":       update.UUID,
		"batch_size": update.Config.BatchSize,
		"enabled":    update.Config.Enabled,
	})

	// Use Lambda UpdateEventSourceMapping API to update the existing mapping
	// Wrapped with retry for transient failures (Requirement 6.5)
	err := withRetry(ctx, tm.retryConfig, "UpdateEventSourceMapping", func() error {
		_, err := tm.lambdaClient.UpdateEventSourceMapping(ctx, &lambda.UpdateEventSourceMappingInput{
			UUID:      aws.String(update.UUID),
			BatchSize: aws.Int32(update.Config.BatchSize),
			Enabled:   aws.Bool(update.Config.Enabled),
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to update event source mapping %s: %w", update.UUID, err)
	}

	utils.Info(ctx, "Successfully updated SQS trigger", map[string]interface{}{
		"queue_arn":  update.Config.QueueArn,
		"uuid":       update.UUID,
		"batch_size": update.Config.BatchSize,
		"enabled":    update.Config.Enabled,
	})

	return nil
}

// deleteSQSTrigger deletes an SQS trigger (event source mapping) from a Lambda function.
// It uses the Lambda DeleteEventSourceMapping API to remove the mapping.
//
// The function is idempotent (Requirement 6.4):
// - If the event source mapping doesn't exist (ResourceNotFoundException), it's treated as success
//
// The operation is wrapped with retry logic for transient failures (Requirement 6.5).
//
// Requirements: 2.2, 6.4, 6.5
func (tm *TriggerManager) deleteSQSTrigger(ctx context.Context, trigger ExistingSQSTrigger) error {
	utils.Info(ctx, "Deleting SQS trigger", map[string]interface{}{
		"queue_arn": trigger.QueueArn,
		"uuid":      trigger.UUID,
	})

	// Use Lambda DeleteEventSourceMapping API to delete the mapping
	// Wrapped with retry for transient failures (Requirement 6.5)
	err := withRetry(ctx, tm.retryConfig, "DeleteEventSourceMapping", func() error {
		_, err := tm.lambdaClient.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{
			UUID: aws.String(trigger.UUID),
		})
		if err != nil {
			// Check if mapping doesn't exist - this is idempotent success (Requirement 6.4)
			// Don't retry for ResourceNotFoundException
			if strings.Contains(err.Error(), "ResourceNotFoundException") {
				utils.Info(ctx, "SQS event source mapping not found, treating as success (idempotent)", map[string]interface{}{
					"queue_arn": trigger.QueueArn,
					"uuid":      trigger.UUID,
				})
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to delete event source mapping %s: %w", trigger.UUID, err)
	}

	utils.Info(ctx, "Successfully deleted SQS trigger", map[string]interface{}{
		"queue_arn": trigger.QueueArn,
		"uuid":      trigger.UUID,
	})

	return nil
}

// ReconcileSQSTriggers manages the full lifecycle of SQS triggers for a Lambda function.
// It queries the current AWS state, compares it with the desired configuration, and
// executes the necessary create/update/delete operations to reconcile the state.
//
// The function is designed to be resilient:
// - It continues processing remaining triggers even if individual operations fail (Requirement 2.5)
// - It only returns an error if ALL operations fail
// - Individual failures are logged as warnings
//
// The function is idempotent (Requirement 4.5):
// - Running it multiple times with the same configuration produces the same result
// - This is achieved through idempotent create/update/delete operations
//
// Requirements: 2.5, 4.5
func (tm *TriggerManager) ReconcileSQSTriggers(
	ctx context.Context,
	functionName string,
	desiredTriggers []SQSTriggerConfig,
) error {
	utils.Info(ctx, "Reconciling SQS triggers", map[string]interface{}{
		"function":      functionName,
		"desired_count": len(desiredTriggers),
	})

	// Step 1: Get existing SQS triggers from AWS
	existingTriggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		return fmt.Errorf("failed to get existing SQS triggers: %w", err)
	}

	utils.Debug(ctx, "Found existing SQS triggers", map[string]interface{}{
		"function":       functionName,
		"existing_count": len(existingTriggers),
	})

	// Step 2: Diff desired vs existing to determine actions
	diff := diffSQSTriggers(desiredTriggers, existingTriggers)

	utils.Debug(ctx, "SQS trigger diff result", map[string]interface{}{
		"function":  functionName,
		"to_create": len(diff.ToCreate),
		"to_update": len(diff.ToUpdate),
		"to_delete": len(diff.ToDelete),
	})

	// Track success/failure counts for final error determination
	totalOperations := len(diff.ToDelete) + len(diff.ToUpdate) + len(diff.ToCreate)
	if totalOperations == 0 {
		utils.Info(ctx, "No SQS trigger changes needed", map[string]interface{}{
			"function": functionName,
		})
		return nil
	}

	successCount := 0
	var lastError error

	// Step 3: Delete orphaned triggers first
	// We delete first to avoid potential conflicts
	for _, trigger := range diff.ToDelete {
		err := tm.deleteSQSTrigger(ctx, trigger)
		if err != nil {
			// Log warning but continue with other triggers (Requirement 2.5)
			utils.Warn(ctx, "Failed to delete SQS trigger, continuing with others", map[string]interface{}{
				"function":  functionName,
				"queue_arn": trigger.QueueArn,
				"uuid":      trigger.UUID,
				"error":     err.Error(),
			})
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 4: Update triggers with changed batch_size or enabled
	for _, update := range diff.ToUpdate {
		err := tm.updateSQSTrigger(ctx, update)
		if err != nil {
			// Log warning but continue with other triggers (Requirement 2.5)
			utils.Warn(ctx, "Failed to update SQS trigger, continuing with others", map[string]interface{}{
				"function":   functionName,
				"queue_arn":  update.Config.QueueArn,
				"uuid":       update.UUID,
				"batch_size": update.Config.BatchSize,
				"enabled":    update.Config.Enabled,
				"error":      err.Error(),
			})
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 5: Create new triggers
	for _, trigger := range diff.ToCreate {
		_, err := tm.createSQSTrigger(ctx, functionName, trigger)
		if err != nil {
			// Log warning but continue with other triggers (Requirement 2.5)
			utils.Warn(ctx, "Failed to create SQS trigger, continuing with others", map[string]interface{}{
				"function":   functionName,
				"queue_arn":  trigger.QueueArn,
				"batch_size": trigger.BatchSize,
				"enabled":    trigger.Enabled,
				"error":      err.Error(),
			})
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 6: Determine final result
	// Only return error if ALL operations failed
	if successCount == 0 && totalOperations > 0 {
		return fmt.Errorf("all SQS trigger operations failed, last error: %w", lastError)
	}

	if lastError != nil {
		utils.Warn(ctx, "Some SQS trigger operations failed", map[string]interface{}{
			"function":         functionName,
			"success_count":    successCount,
			"total_operations": totalOperations,
		})
	} else {
		utils.Info(ctx, "Successfully reconciled SQS triggers", map[string]interface{}{
			"function":         functionName,
			"success_count":    successCount,
			"total_operations": totalOperations,
		})
	}

	return nil
}

// ReconcileTriggers is the main entry point for trigger reconciliation.
// It extracts triggers from lambda_config and calls ReconcileSNSTriggers and ReconcileSQSTriggers.
// This function is called during Lambda updates to ensure AWS trigger state matches configuration.
//
// The function is designed to be resilient:
// - It continues processing even if one type of trigger reconciliation fails
// - It logs warnings for failures but doesn't fail the entire operation
// - Individual trigger type failures are logged for debugging
//
// Requirements: 5.1 - TriggerManager is called during Lambda updates
func (tm *TriggerManager) ReconcileTriggers(
	ctx context.Context,
	lambdaName string,
	lambdaArn string,
	lambdaConfig map[string]interface{},
) error {
	utils.Info(ctx, "Reconciling triggers for Lambda", map[string]interface{}{
		"lambda": lambdaName,
	})

	// Build the full function name for AWS API calls
	functionName := fmt.Sprintf("%s-%s-%s", tm.config.AppName, tm.config.Environment, lambdaName)

	// Extract SNS triggers from lambda_config
	snsTriggers := extractSNSTriggers(lambdaConfig, lambdaName)
	utils.Debug(ctx, "Extracted SNS triggers from config", map[string]interface{}{
		"lambda": lambdaName,
		"count":  len(snsTriggers),
	})

	// Extract SQS triggers from lambda_config
	sqsTriggers := extractSQSTriggers(lambdaConfig, lambdaName)
	utils.Debug(ctx, "Extracted SQS triggers from config", map[string]interface{}{
		"lambda": lambdaName,
		"count":  len(sqsTriggers),
	})

	var snsErr, sqsErr error

	// Reconcile SNS triggers
	if err := tm.ReconcileSNSTriggers(ctx, functionName, lambdaArn, snsTriggers); err != nil {
		utils.Warn(ctx, "Failed to reconcile SNS triggers", map[string]interface{}{
			"lambda": lambdaName,
			"error":  err.Error(),
		})
		snsErr = err
	}

	// Reconcile SQS triggers
	if err := tm.ReconcileSQSTriggers(ctx, functionName, sqsTriggers); err != nil {
		utils.Warn(ctx, "Failed to reconcile SQS triggers", map[string]interface{}{
			"lambda": lambdaName,
			"error":  err.Error(),
		})
		sqsErr = err
	}

	// Return error only if both reconciliations failed
	if snsErr != nil && sqsErr != nil {
		return fmt.Errorf("failed to reconcile triggers: SNS error: %v, SQS error: %v", snsErr, sqsErr)
	}

	// Log success or partial success
	if snsErr != nil || sqsErr != nil {
		utils.Warn(ctx, "Trigger reconciliation completed with partial failures", map[string]interface{}{
			"lambda":    lambdaName,
			"sns_error": snsErr != nil,
			"sqs_error": sqsErr != nil,
		})
	} else {
		utils.Info(ctx, "Successfully reconciled all triggers", map[string]interface{}{
			"lambda":    lambdaName,
			"sns_count": len(snsTriggers),
			"sqs_count": len(sqsTriggers),
		})
	}

	return nil
}

// CleanupAllTriggers removes all SNS and SQS triggers for a Lambda function.
// This is called during Lambda deletion to ensure all associated triggers are cleaned up.
//
// The function is designed to be resilient:
// - It continues processing even if some trigger deletions fail
// - It logs warnings for failures but doesn't fail the entire operation
//
// Requirements: 5.3 - When a Lambda is deleted, clean up all associated triggers
func (tm *TriggerManager) CleanupAllTriggers(
	ctx context.Context,
	lambdaName string,
	lambdaArn string,
) error {
	utils.Info(ctx, "Cleaning up all triggers for Lambda", map[string]interface{}{
		"lambda": lambdaName,
	})

	// Build the full function name for AWS API calls
	functionName := fmt.Sprintf("%s-%s-%s", tm.config.AppName, tm.config.Environment, lambdaName)

	var snsErr, sqsErr error

	// Clean up SNS triggers by reconciling with empty desired state
	if err := tm.ReconcileSNSTriggers(ctx, functionName, lambdaArn, []SNSTriggerConfig{}); err != nil {
		utils.Warn(ctx, "Failed to clean up SNS triggers", map[string]interface{}{
			"lambda": lambdaName,
			"error":  err.Error(),
		})
		snsErr = err
	}

	// Clean up SQS triggers by reconciling with empty desired state
	if err := tm.ReconcileSQSTriggers(ctx, functionName, []SQSTriggerConfig{}); err != nil {
		utils.Warn(ctx, "Failed to clean up SQS triggers", map[string]interface{}{
			"lambda": lambdaName,
			"error":  err.Error(),
		})
		sqsErr = err
	}

	// Return error only if both cleanups failed
	if snsErr != nil && sqsErr != nil {
		return fmt.Errorf("failed to clean up triggers: SNS error: %v, SQS error: %v", snsErr, sqsErr)
	}

	utils.Info(ctx, "Trigger cleanup completed", map[string]interface{}{
		"lambda":    lambdaName,
		"sns_error": snsErr != nil,
		"sqs_error": sqsErr != nil,
	})

	return nil
}
