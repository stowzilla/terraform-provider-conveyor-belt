// internal/resources/trigger_manager_sqs_test.go
package resources

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// ============================================================================
// SQS Trigger Extraction Tests
// ============================================================================

// TestExtractSQSTriggers_EmptyConfig tests that extractSQSTriggers returns
// an empty slice when lambda_config is empty.
func TestExtractSQSTriggers_EmptyConfig(t *testing.T) {
	lambdaConfig := make(map[string]interface{})
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers, got %d", len(triggers))
	}
}

// TestExtractSQSTriggers_NoLambdaEntry tests that extractSQSTriggers returns
// an empty slice when the lambda has no entry in lambda_config.
func TestExtractSQSTriggers_NoLambdaEntry(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"other-lambda": map[string]interface{}{
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn": "arn:aws:sqs:us-east-1:123456789012:my-queue",
				},
			},
		},
	}
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers, got %d", len(triggers))
	}
}


// TestExtractSQSTriggers_NoSQSTriggers tests that extractSQSTriggers returns
// an empty slice when the lambda has no sqs_triggers field.
func TestExtractSQSTriggers_NoSQSTriggers(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"timeout": 30,
		},
	}
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers, got %d", len(triggers))
	}
}

// TestExtractSQSTriggers_SingleTrigger tests extracting a single SQS trigger
// with only queue_arn specified (batch_size and enabled should use defaults).
func TestExtractSQSTriggers_SingleTrigger(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn": queueArn,
				},
			},
		},
	}
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].QueueArn != queueArn {
		t.Errorf("Expected QueueArn %s, got %s", queueArn, triggers[0].QueueArn)
	}

	// BatchSize should default to 10
	if triggers[0].BatchSize != 10 {
		t.Errorf("Expected BatchSize 10, got %d", triggers[0].BatchSize)
	}

	// Enabled should default to true
	if triggers[0].Enabled != true {
		t.Errorf("Expected Enabled true, got %v", triggers[0].Enabled)
	}
}

// TestExtractSQSTriggers_CustomBatchSize tests extracting an SQS trigger
// with a custom batch_size specified.
func TestExtractSQSTriggers_CustomBatchSize(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn":  queueArn,
					"batch_size": 25,
				},
			},
		},
	}
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].QueueArn != queueArn {
		t.Errorf("Expected QueueArn %s, got %s", queueArn, triggers[0].QueueArn)
	}

	if triggers[0].BatchSize != 25 {
		t.Errorf("Expected BatchSize 25, got %d", triggers[0].BatchSize)
	}
}


// TestExtractSQSTriggers_DisabledTrigger tests extracting an SQS trigger
// with enabled set to false.
func TestExtractSQSTriggers_DisabledTrigger(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn": queueArn,
					"enabled":   false,
				},
			},
		},
	}
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].Enabled != false {
		t.Errorf("Expected Enabled false, got %v", triggers[0].Enabled)
	}
}

// TestExtractSQSTriggers_AllFields tests extracting an SQS trigger
// with all fields specified.
func TestExtractSQSTriggers_AllFields(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn":  queueArn,
					"batch_size": 50,
					"enabled":    false,
				},
			},
		},
	}
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].QueueArn != queueArn {
		t.Errorf("Expected QueueArn %s, got %s", queueArn, triggers[0].QueueArn)
	}

	if triggers[0].BatchSize != 50 {
		t.Errorf("Expected BatchSize 50, got %d", triggers[0].BatchSize)
	}

	if triggers[0].Enabled != false {
		t.Errorf("Expected Enabled false, got %v", triggers[0].Enabled)
	}
}


// TestExtractSQSTriggers_MultipleTriggers tests extracting multiple SQS triggers.
func TestExtractSQSTriggers_MultipleTriggers(t *testing.T) {
	queueArn1 := "arn:aws:sqs:us-east-1:123456789012:queue-1"
	queueArn2 := "arn:aws:sqs:us-east-1:123456789012:queue-2"
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn": queueArn1,
				},
				map[string]interface{}{
					"queue_arn":  queueArn2,
					"batch_size": 100,
					"enabled":    false,
				},
			},
		},
	}
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 2 {
		t.Fatalf("Expected 2 triggers, got %d", len(triggers))
	}

	// First trigger - defaults
	if triggers[0].QueueArn != queueArn1 {
		t.Errorf("Expected first QueueArn %s, got %s", queueArn1, triggers[0].QueueArn)
	}
	if triggers[0].BatchSize != 10 {
		t.Errorf("Expected first BatchSize 10, got %d", triggers[0].BatchSize)
	}
	if triggers[0].Enabled != true {
		t.Errorf("Expected first Enabled true, got %v", triggers[0].Enabled)
	}

	// Second trigger - custom values
	if triggers[1].QueueArn != queueArn2 {
		t.Errorf("Expected second QueueArn %s, got %s", queueArn2, triggers[1].QueueArn)
	}
	if triggers[1].BatchSize != 100 {
		t.Errorf("Expected second BatchSize 100, got %d", triggers[1].BatchSize)
	}
	if triggers[1].Enabled != false {
		t.Errorf("Expected second Enabled false, got %v", triggers[1].Enabled)
	}
}

// TestExtractSQSTriggers_MissingQueueArn tests that triggers without queue_arn are skipped.
func TestExtractSQSTriggers_MissingQueueArn(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"batch_size": 10,
					// Missing queue_arn
				},
			},
		},
	}
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers (missing queue_arn), got %d", len(triggers))
	}
}


// TestExtractSQSTriggers_EmptyQueueArn tests that triggers with empty queue_arn are skipped.
func TestExtractSQSTriggers_EmptyQueueArn(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sqs_triggers": []interface{}{
				map[string]interface{}{
					"queue_arn": "",
				},
			},
		},
	}
	triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers (empty queue_arn), got %d", len(triggers))
	}
}

// TestExtractSQSTriggers_BatchSizeTypes tests that batch_size handles different numeric types.
func TestExtractSQSTriggers_BatchSizeTypes(t *testing.T) {
	tests := []struct {
		name           string
		batchSizeValue interface{}
		expectedSize   int32
	}{
		{
			name:           "int type",
			batchSizeValue: 15,
			expectedSize:   15,
		},
		{
			name:           "int32 type",
			batchSizeValue: int32(20),
			expectedSize:   20,
		},
		{
			name:           "int64 type",
			batchSizeValue: int64(30),
			expectedSize:   30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lambdaConfig := map[string]interface{}{
				"my-lambda": map[string]interface{}{
					"sqs_triggers": []interface{}{
						map[string]interface{}{
							"queue_arn":  "arn:aws:sqs:us-east-1:123456789012:my-queue",
							"batch_size": tt.batchSizeValue,
						},
					},
				},
			}
			triggers := extractSQSTriggers(lambdaConfig, "my-lambda")

			if len(triggers) != 1 {
				t.Fatalf("Expected 1 trigger, got %d", len(triggers))
			}

			if triggers[0].BatchSize != tt.expectedSize {
				t.Errorf("Expected BatchSize %d, got %d", tt.expectedSize, triggers[0].BatchSize)
			}
		})
	}
}


// ============================================================================
// getExistingSQSTriggers Tests
// ============================================================================

// TestGetExistingSQSTriggers_NoMappings tests that getExistingSQSTriggers returns
// an empty slice when there are no event source mappings.
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_NoMappings(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"

	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			// Verify function name is passed correctly
			if params.FunctionName == nil || *params.FunctionName != functionName {
				t.Errorf("Expected FunctionName %s, got %v", functionName, params.FunctionName)
			}
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{},
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers, got %d", len(triggers))
	}
}


// TestGetExistingSQSTriggers_SingleSQSMapping tests that getExistingSQSTriggers
// correctly extracts a single SQS event source mapping.
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_SingleSQSMapping(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"
	batchSize := int32(25)
	state := "Enabled"

	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: aws.String(queueArn),
						UUID:           aws.String(uuid),
						BatchSize:      aws.Int32(batchSize),
						State:          aws.String(state),
					},
				},
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].QueueArn != queueArn {
		t.Errorf("Expected QueueArn %s, got %s", queueArn, triggers[0].QueueArn)
	}
	if triggers[0].UUID != uuid {
		t.Errorf("Expected UUID %s, got %s", uuid, triggers[0].UUID)
	}
	if triggers[0].BatchSize != batchSize {
		t.Errorf("Expected BatchSize %d, got %d", batchSize, triggers[0].BatchSize)
	}
	if triggers[0].Enabled != true {
		t.Errorf("Expected Enabled true, got %v", triggers[0].Enabled)
	}
}


// TestGetExistingSQSTriggers_MultipleSQSMappings tests that getExistingSQSTriggers
// correctly extracts multiple SQS event source mappings.
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_MultipleSQSMappings(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn1 := "arn:aws:sqs:us-east-1:123456789012:queue-1"
	queueArn2 := "arn:aws:sqs:us-east-1:123456789012:queue-2"

	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: aws.String(queueArn1),
						UUID:           aws.String("uuid-1"),
						BatchSize:      aws.Int32(10),
						State:          aws.String("Enabled"),
					},
					{
						EventSourceArn: aws.String(queueArn2),
						UUID:           aws.String("uuid-2"),
						BatchSize:      aws.Int32(50),
						State:          aws.String("Disabled"),
					},
				},
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(triggers) != 2 {
		t.Fatalf("Expected 2 triggers, got %d", len(triggers))
	}

	// First trigger
	if triggers[0].QueueArn != queueArn1 {
		t.Errorf("Expected first QueueArn %s, got %s", queueArn1, triggers[0].QueueArn)
	}
	if triggers[0].BatchSize != 10 {
		t.Errorf("Expected first BatchSize 10, got %d", triggers[0].BatchSize)
	}
	if triggers[0].Enabled != true {
		t.Errorf("Expected first Enabled true, got %v", triggers[0].Enabled)
	}

	// Second trigger
	if triggers[1].QueueArn != queueArn2 {
		t.Errorf("Expected second QueueArn %s, got %s", queueArn2, triggers[1].QueueArn)
	}
	if triggers[1].BatchSize != 50 {
		t.Errorf("Expected second BatchSize 50, got %d", triggers[1].BatchSize)
	}
	if triggers[1].Enabled != false {
		t.Errorf("Expected second Enabled false, got %v", triggers[1].Enabled)
	}
}


// TestGetExistingSQSTriggers_FiltersSQSOnly tests that getExistingSQSTriggers
// filters out non-SQS event sources (like DynamoDB streams, Kinesis).
// Requirements: 4.2 - Filter for SQS sources
func TestGetExistingSQSTriggers_FiltersSQSOnly(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	sqsQueueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	dynamoStreamArn := "arn:aws:dynamodb:us-east-1:123456789012:table/my-table/stream/2024-01-01T00:00:00.000"
	kinesisStreamArn := "arn:aws:kinesis:us-east-1:123456789012:stream/my-stream"

	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: aws.String(sqsQueueArn),
						UUID:           aws.String("sqs-uuid"),
						BatchSize:      aws.Int32(10),
						State:          aws.String("Enabled"),
					},
					{
						EventSourceArn: aws.String(dynamoStreamArn),
						UUID:           aws.String("dynamo-uuid"),
						BatchSize:      aws.Int32(100),
						State:          aws.String("Enabled"),
					},
					{
						EventSourceArn: aws.String(kinesisStreamArn),
						UUID:           aws.String("kinesis-uuid"),
						BatchSize:      aws.Int32(100),
						State:          aws.String("Enabled"),
					},
				},
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Should only return the SQS trigger, not DynamoDB or Kinesis
	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger (SQS only), got %d", len(triggers))
	}

	if triggers[0].QueueArn != sqsQueueArn {
		t.Errorf("Expected QueueArn %s, got %s", sqsQueueArn, triggers[0].QueueArn)
	}
}


// TestGetExistingSQSTriggers_HandlesNilEventSourceArn tests that getExistingSQSTriggers
// skips mappings with nil EventSourceArn.
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_HandlesNilEventSourceArn(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	validQueueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: nil, // Nil ARN should be skipped
						UUID:           aws.String("nil-uuid"),
						BatchSize:      aws.Int32(10),
						State:          aws.String("Enabled"),
					},
					{
						EventSourceArn: aws.String(validQueueArn),
						UUID:           aws.String("valid-uuid"),
						BatchSize:      aws.Int32(10),
						State:          aws.String("Enabled"),
					},
				},
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Should only return the valid SQS trigger
	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].QueueArn != validQueueArn {
		t.Errorf("Expected QueueArn %s, got %s", validQueueArn, triggers[0].QueueArn)
	}
}


// TestGetExistingSQSTriggers_StateMapping tests that getExistingSQSTriggers
// correctly maps various Lambda event source mapping states to Enabled boolean.
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_StateMapping(t *testing.T) {
	tests := []struct {
		name            string
		state           string
		expectedEnabled bool
	}{
		{
			name:            "Enabled state",
			state:           "Enabled",
			expectedEnabled: true,
		},
		{
			name:            "Enabling state",
			state:           "Enabling",
			expectedEnabled: true,
		},
		{
			name:            "Disabled state",
			state:           "Disabled",
			expectedEnabled: false,
		},
		{
			name:            "Disabling state",
			state:           "Disabling",
			expectedEnabled: false,
		},
		{
			name:            "Creating state",
			state:           "Creating",
			expectedEnabled: false,
		},
		{
			name:            "Updating state",
			state:           "Updating",
			expectedEnabled: false,
		},
		{
			name:            "Deleting state",
			state:           "Deleting",
			expectedEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			functionName := "myapp-prod-my-lambda"
			queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

			mockLambda := &mockLambdaClientForTrigger{
				listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
					return &lambda.ListEventSourceMappingsOutput{
						EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
							{
								EventSourceArn: aws.String(queueArn),
								UUID:           aws.String("test-uuid"),
								BatchSize:      aws.Int32(10),
								State:          aws.String(tt.state),
							},
						},
					}, nil
				},
			}

			tm := &testableTriggerManager{
				lambdaClient: mockLambda,
			}

			triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(triggers) != 1 {
				t.Fatalf("Expected 1 trigger, got %d", len(triggers))
			}

			if triggers[0].Enabled != tt.expectedEnabled {
				t.Errorf("For state %s: expected Enabled=%v, got %v", tt.state, tt.expectedEnabled, triggers[0].Enabled)
			}
		})
	}
}


// TestGetExistingSQSTriggers_DefaultBatchSize tests that getExistingSQSTriggers
// uses default batch size of 10 when BatchSize is nil.
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_DefaultBatchSize(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: aws.String(queueArn),
						UUID:           aws.String("test-uuid"),
						BatchSize:      nil, // Nil batch size should default to 10
						State:          aws.String("Enabled"),
					},
				},
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].BatchSize != 10 {
		t.Errorf("Expected default BatchSize 10, got %d", triggers[0].BatchSize)
	}
}

// TestGetExistingSQSTriggers_DefaultEnabledState tests that getExistingSQSTriggers
// uses default enabled state of true when State is nil.
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_DefaultEnabledState(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: aws.String(queueArn),
						UUID:           aws.String("test-uuid"),
						BatchSize:      aws.Int32(10),
						State:          nil, // Nil state should default to enabled=true
					},
				},
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].Enabled != true {
		t.Errorf("Expected default Enabled true, got %v", triggers[0].Enabled)
	}
}


// TestGetExistingSQSTriggers_Pagination tests that getExistingSQSTriggers
// correctly handles paginated results from ListEventSourceMappings.
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_Pagination(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn1 := "arn:aws:sqs:us-east-1:123456789012:queue-1"
	queueArn2 := "arn:aws:sqs:us-east-1:123456789012:queue-2"
	queueArn3 := "arn:aws:sqs:us-east-1:123456789012:queue-3"

	callCount := 0
	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			callCount++
			switch callCount {
			case 1:
				// First page - return first two mappings with NextMarker
				if params.Marker != nil {
					t.Errorf("Expected nil Marker on first call, got %v", params.Marker)
				}
				return &lambda.ListEventSourceMappingsOutput{
					EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
						{
							EventSourceArn: aws.String(queueArn1),
							UUID:           aws.String("uuid-1"),
							BatchSize:      aws.Int32(10),
							State:          aws.String("Enabled"),
						},
						{
							EventSourceArn: aws.String(queueArn2),
							UUID:           aws.String("uuid-2"),
							BatchSize:      aws.Int32(20),
							State:          aws.String("Enabled"),
						},
					},
					NextMarker: aws.String("page-2-marker"),
				}, nil
			case 2:
				// Second page - return last mapping with no NextMarker
				if params.Marker == nil || *params.Marker != "page-2-marker" {
					t.Errorf("Expected Marker 'page-2-marker' on second call, got %v", params.Marker)
				}
				return &lambda.ListEventSourceMappingsOutput{
					EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
						{
							EventSourceArn: aws.String(queueArn3),
							UUID:           aws.String("uuid-3"),
							BatchSize:      aws.Int32(30),
							State:          aws.String("Enabled"),
						},
					},
					NextMarker: nil, // No more pages
				}, nil
			default:
				t.Fatalf("Unexpected call count: %d", callCount)
				return nil, nil
			}
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Should have called the API twice (two pages)
	if callCount != 2 {
		t.Errorf("Expected 2 API calls for pagination, got %d", callCount)
	}

	// Should have all 3 triggers from both pages
	if len(triggers) != 3 {
		t.Fatalf("Expected 3 triggers from pagination, got %d", len(triggers))
	}

	// Verify all triggers are present
	queueArns := make(map[string]bool)
	for _, trigger := range triggers {
		queueArns[trigger.QueueArn] = true
	}
	if !queueArns[queueArn1] || !queueArns[queueArn2] || !queueArns[queueArn3] {
		t.Errorf("Missing expected queue ARNs in results")
	}
}


// TestGetExistingSQSTriggers_APIError tests that getExistingSQSTriggers
// returns an error when the ListEventSourceMappings API fails.
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_APIError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"

	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return nil, fmt.Errorf("AccessDeniedException: User is not authorized to perform lambda:ListEventSourceMappings")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if triggers != nil {
		t.Errorf("Expected nil triggers on error, got %v", triggers)
	}

	if !strings.Contains(err.Error(), "failed to list event source mappings") {
		t.Errorf("Expected error message to contain 'failed to list event source mappings', got: %v", err)
	}
}

// TestGetExistingSQSTriggers_NilUUID tests that getExistingSQSTriggers
// handles nil UUID gracefully (uses empty string).
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func TestGetExistingSQSTriggers_NilUUID(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	mockLambda := &mockLambdaClientForTrigger{
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: aws.String(queueArn),
						UUID:           nil, // Nil UUID
						BatchSize:      aws.Int32(10),
						State:          aws.String("Enabled"),
					},
				},
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	triggers, err := tm.getExistingSQSTriggers(ctx, functionName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	// UUID should be empty string when nil
	if triggers[0].UUID != "" {
		t.Errorf("Expected empty UUID, got %s", triggers[0].UUID)
	}
}


// TestExistingSQSTriggerStruct tests the ExistingSQSTrigger struct.
func TestExistingSQSTriggerStruct(t *testing.T) {
	trigger := ExistingSQSTrigger{
		QueueArn:  "arn:aws:sqs:us-east-1:123456789012:my-queue",
		UUID:      "12345678-1234-1234-1234-123456789012",
		BatchSize: 25,
		Enabled:   true,
	}

	if trigger.QueueArn != "arn:aws:sqs:us-east-1:123456789012:my-queue" {
		t.Errorf("Unexpected QueueArn: %s", trigger.QueueArn)
	}
	if trigger.UUID != "12345678-1234-1234-1234-123456789012" {
		t.Errorf("Unexpected UUID: %s", trigger.UUID)
	}
	if trigger.BatchSize != 25 {
		t.Errorf("Unexpected BatchSize: %d", trigger.BatchSize)
	}
	if trigger.Enabled != true {
		t.Errorf("Unexpected Enabled: %v", trigger.Enabled)
	}
}

// TestSQSTriggerConfigStruct tests the SQSTriggerConfig struct.
func TestSQSTriggerConfigStruct(t *testing.T) {
	config := SQSTriggerConfig{
		QueueArn:  "arn:aws:sqs:us-east-1:123456789012:my-queue",
		BatchSize: 50,
		Enabled:   false,
	}

	if config.QueueArn != "arn:aws:sqs:us-east-1:123456789012:my-queue" {
		t.Errorf("Unexpected QueueArn: %s", config.QueueArn)
	}
	if config.BatchSize != 50 {
		t.Errorf("Unexpected BatchSize: %d", config.BatchSize)
	}
	if config.Enabled != false {
		t.Errorf("Unexpected Enabled: %v", config.Enabled)
	}
}

// ============================================================================
// SQS Trigger Diffing Tests
// ============================================================================

// TestDiffSQSTriggers_EmptyBoth tests diffing when both desired and existing are empty.
func TestDiffSQSTriggers_EmptyBoth(t *testing.T) {
	desired := []SQSTriggerConfig{}
	existing := []ExistingSQSTrigger{}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSQSTriggers_CreateNew tests diffing when a new trigger needs to be created.
// Requirements: 2.1 - When a new SQS trigger is added, it should be created
func TestDiffSQSTriggers_CreateNew(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	desired := []SQSTriggerConfig{
		{
			QueueArn:  queueArn,
			BatchSize: 10,
			Enabled:   true,
		},
	}
	existing := []ExistingSQSTrigger{}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 1 {
		t.Fatalf("Expected 1 ToCreate, got %d", len(result.ToCreate))
	}
	if result.ToCreate[0].QueueArn != queueArn {
		t.Errorf("Expected QueueArn %s, got %s", queueArn, result.ToCreate[0].QueueArn)
	}
	if result.ToCreate[0].BatchSize != 10 {
		t.Errorf("Expected BatchSize 10, got %d", result.ToCreate[0].BatchSize)
	}
	if result.ToCreate[0].Enabled != true {
		t.Errorf("Expected Enabled true, got %v", result.ToCreate[0].Enabled)
	}
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSQSTriggers_DeleteOrphan tests diffing when an existing trigger should be deleted.
// Requirements: 2.2, 4.4 - When an SQS trigger is removed from config, it should be deleted
func TestDiffSQSTriggers_DeleteOrphan(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:orphan-queue"
	desired := []SQSTriggerConfig{}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  queueArn,
			UUID:      "uuid-orphan",
			BatchSize: 10,
			Enabled:   true,
		},
	}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}
	if len(result.ToDelete) != 1 {
		t.Fatalf("Expected 1 ToDelete, got %d", len(result.ToDelete))
	}
	if result.ToDelete[0].QueueArn != queueArn {
		t.Errorf("Expected QueueArn %s, got %s", queueArn, result.ToDelete[0].QueueArn)
	}
	if result.ToDelete[0].UUID != "uuid-orphan" {
		t.Errorf("Expected UUID 'uuid-orphan', got %s", result.ToDelete[0].UUID)
	}
}

// TestDiffSQSTriggers_UpdateBatchSize tests diffing when batch_size changes.
// Requirements: 2.3 - When batch_size changes, the trigger should be updated
func TestDiffSQSTriggers_UpdateBatchSize(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	desired := []SQSTriggerConfig{
		{
			QueueArn:  queueArn,
			BatchSize: 50, // Changed from 10 to 50
			Enabled:   true,
		},
	}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  queueArn,
			UUID:      "uuid-123",
			BatchSize: 10,
			Enabled:   true,
		},
	}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 1 {
		t.Fatalf("Expected 1 ToUpdate, got %d", len(result.ToUpdate))
	}
	if result.ToUpdate[0].Config.QueueArn != queueArn {
		t.Errorf("Expected QueueArn %s, got %s", queueArn, result.ToUpdate[0].Config.QueueArn)
	}
	if result.ToUpdate[0].Config.BatchSize != 50 {
		t.Errorf("Expected BatchSize 50, got %d", result.ToUpdate[0].Config.BatchSize)
	}
	if result.ToUpdate[0].UUID != "uuid-123" {
		t.Errorf("Expected UUID 'uuid-123', got %s", result.ToUpdate[0].UUID)
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSQSTriggers_UpdateEnabled tests diffing when enabled state changes.
// Requirements: 2.3 - When enabled changes, the trigger should be updated
func TestDiffSQSTriggers_UpdateEnabled(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	desired := []SQSTriggerConfig{
		{
			QueueArn:  queueArn,
			BatchSize: 10,
			Enabled:   false, // Changed from true to false
		},
	}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  queueArn,
			UUID:      "uuid-123",
			BatchSize: 10,
			Enabled:   true,
		},
	}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 1 {
		t.Fatalf("Expected 1 ToUpdate, got %d", len(result.ToUpdate))
	}
	if result.ToUpdate[0].Config.Enabled != false {
		t.Errorf("Expected Enabled false, got %v", result.ToUpdate[0].Config.Enabled)
	}
	if result.ToUpdate[0].UUID != "uuid-123" {
		t.Errorf("Expected UUID 'uuid-123', got %s", result.ToUpdate[0].UUID)
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSQSTriggers_UpdateBothBatchSizeAndEnabled tests diffing when both batch_size and enabled change.
func TestDiffSQSTriggers_UpdateBothBatchSizeAndEnabled(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	desired := []SQSTriggerConfig{
		{
			QueueArn:  queueArn,
			BatchSize: 100, // Changed
			Enabled:   false, // Changed
		},
	}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  queueArn,
			UUID:      "uuid-123",
			BatchSize: 10,
			Enabled:   true,
		},
	}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 1 {
		t.Fatalf("Expected 1 ToUpdate, got %d", len(result.ToUpdate))
	}
	if result.ToUpdate[0].Config.BatchSize != 100 {
		t.Errorf("Expected BatchSize 100, got %d", result.ToUpdate[0].Config.BatchSize)
	}
	if result.ToUpdate[0].Config.Enabled != false {
		t.Errorf("Expected Enabled false, got %v", result.ToUpdate[0].Config.Enabled)
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSQSTriggers_NoChange tests diffing when triggers match exactly.
func TestDiffSQSTriggers_NoChange(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	desired := []SQSTriggerConfig{
		{
			QueueArn:  queueArn,
			BatchSize: 10,
			Enabled:   true,
		},
	}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  queueArn,
			UUID:      "uuid-123",
			BatchSize: 10,
			Enabled:   true,
		},
	}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSQSTriggers_QueueArnChange tests diffing when queue_arn changes.
// Requirements: 2.4 - When queue_arn changes, old trigger is deleted and new one created
func TestDiffSQSTriggers_QueueArnChange(t *testing.T) {
	oldQueueArn := "arn:aws:sqs:us-east-1:123456789012:old-queue"
	newQueueArn := "arn:aws:sqs:us-east-1:123456789012:new-queue"
	desired := []SQSTriggerConfig{
		{
			QueueArn:  newQueueArn,
			BatchSize: 10,
			Enabled:   true,
		},
	}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  oldQueueArn,
			UUID:      "uuid-old",
			BatchSize: 10,
			Enabled:   true,
		},
	}

	result := diffSQSTriggers(desired, existing)

	// New queue should be created
	if len(result.ToCreate) != 1 {
		t.Fatalf("Expected 1 ToCreate, got %d", len(result.ToCreate))
	}
	if result.ToCreate[0].QueueArn != newQueueArn {
		t.Errorf("Expected ToCreate QueueArn %s, got %s", newQueueArn, result.ToCreate[0].QueueArn)
	}

	// No updates (different queue ARNs)
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}

	// Old queue should be deleted
	if len(result.ToDelete) != 1 {
		t.Fatalf("Expected 1 ToDelete, got %d", len(result.ToDelete))
	}
	if result.ToDelete[0].QueueArn != oldQueueArn {
		t.Errorf("Expected ToDelete QueueArn %s, got %s", oldQueueArn, result.ToDelete[0].QueueArn)
	}
}

// TestDiffSQSTriggers_MultipleTriggers tests diffing with multiple triggers.
func TestDiffSQSTriggers_MultipleTriggers(t *testing.T) {
	// Desired: queue-1 (unchanged), queue-2 (update batch_size), queue-3 (new)
	// Existing: queue-1 (unchanged), queue-2 (old batch_size), queue-4 (orphan)
	desired := []SQSTriggerConfig{
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-1",
			BatchSize: 10,
			Enabled:   true,
		},
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-2",
			BatchSize: 50, // Changed from 10
			Enabled:   true,
		},
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-3",
			BatchSize: 10,
			Enabled:   true,
		},
	}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-1",
			UUID:      "uuid-1",
			BatchSize: 10,
			Enabled:   true,
		},
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-2",
			UUID:      "uuid-2",
			BatchSize: 10, // Will be updated to 50
			Enabled:   true,
		},
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-4",
			UUID:      "uuid-4",
			BatchSize: 10,
			Enabled:   true,
		},
	}

	result := diffSQSTriggers(desired, existing)

	// queue-3 should be created
	if len(result.ToCreate) != 1 {
		t.Fatalf("Expected 1 ToCreate, got %d", len(result.ToCreate))
	}
	if result.ToCreate[0].QueueArn != "arn:aws:sqs:us-east-1:123456789012:queue-3" {
		t.Errorf("Expected ToCreate queue-3, got %s", result.ToCreate[0].QueueArn)
	}

	// queue-2 should be updated
	if len(result.ToUpdate) != 1 {
		t.Fatalf("Expected 1 ToUpdate, got %d", len(result.ToUpdate))
	}
	if result.ToUpdate[0].Config.QueueArn != "arn:aws:sqs:us-east-1:123456789012:queue-2" {
		t.Errorf("Expected ToUpdate queue-2, got %s", result.ToUpdate[0].Config.QueueArn)
	}
	if result.ToUpdate[0].Config.BatchSize != 50 {
		t.Errorf("Expected ToUpdate BatchSize 50, got %d", result.ToUpdate[0].Config.BatchSize)
	}
	if result.ToUpdate[0].UUID != "uuid-2" {
		t.Errorf("Expected ToUpdate UUID 'uuid-2', got %s", result.ToUpdate[0].UUID)
	}

	// queue-4 should be deleted
	if len(result.ToDelete) != 1 {
		t.Fatalf("Expected 1 ToDelete, got %d", len(result.ToDelete))
	}
	if result.ToDelete[0].QueueArn != "arn:aws:sqs:us-east-1:123456789012:queue-4" {
		t.Errorf("Expected ToDelete queue-4, got %s", result.ToDelete[0].QueueArn)
	}
}

// TestDiffSQSTriggers_AllNew tests diffing when all triggers are new.
func TestDiffSQSTriggers_AllNew(t *testing.T) {
	desired := []SQSTriggerConfig{
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-1",
			BatchSize: 10,
			Enabled:   true,
		},
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-2",
			BatchSize: 20,
			Enabled:   false,
		},
	}
	existing := []ExistingSQSTrigger{}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 2 {
		t.Errorf("Expected 2 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSQSTriggers_AllDeleted tests diffing when all triggers should be deleted.
func TestDiffSQSTriggers_AllDeleted(t *testing.T) {
	desired := []SQSTriggerConfig{}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-1",
			UUID:      "uuid-1",
			BatchSize: 10,
			Enabled:   true,
		},
		{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:queue-2",
			UUID:      "uuid-2",
			BatchSize: 20,
			Enabled:   true,
		},
	}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}
	if len(result.ToDelete) != 2 {
		t.Errorf("Expected 2 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSQSTriggers_DuplicateQueueArnsInDesired tests behavior with duplicate queue ARNs in desired.
// Note: This is an edge case - the last entry with the same queue_arn wins in the comparison.
func TestDiffSQSTriggers_DuplicateQueueArnsInDesired(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	desired := []SQSTriggerConfig{
		{
			QueueArn:  queueArn,
			BatchSize: 10,
			Enabled:   true,
		},
		{
			QueueArn:  queueArn,
			BatchSize: 50,
			Enabled:   false,
		},
	}
	existing := []ExistingSQSTrigger{}

	result := diffSQSTriggers(desired, existing)

	// Both entries should be in ToCreate since we process all desired triggers
	// In practice, the caller should ensure no duplicates, but the diff function
	// processes all entries
	if len(result.ToCreate) != 2 {
		t.Errorf("Expected 2 ToCreate (duplicates processed), got %d", len(result.ToCreate))
	}
}

// TestDiffSQSTriggers_EnabledFalseToTrue tests diffing when enabled changes from false to true.
func TestDiffSQSTriggers_EnabledFalseToTrue(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	desired := []SQSTriggerConfig{
		{
			QueueArn:  queueArn,
			BatchSize: 10,
			Enabled:   true, // Changed from false to true
		},
	}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  queueArn,
			UUID:      "uuid-123",
			BatchSize: 10,
			Enabled:   false,
		},
	}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 1 {
		t.Fatalf("Expected 1 ToUpdate, got %d", len(result.ToUpdate))
	}
	if result.ToUpdate[0].Config.Enabled != true {
		t.Errorf("Expected Enabled true, got %v", result.ToUpdate[0].Config.Enabled)
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSQSTriggers_PreservesUUIDForUpdate tests that the UUID is correctly preserved for updates.
func TestDiffSQSTriggers_PreservesUUIDForUpdate(t *testing.T) {
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	expectedUUID := "12345678-1234-1234-1234-123456789012"
	desired := []SQSTriggerConfig{
		{
			QueueArn:  queueArn,
			BatchSize: 100,
			Enabled:   true,
		},
	}
	existing := []ExistingSQSTrigger{
		{
			QueueArn:  queueArn,
			UUID:      expectedUUID,
			BatchSize: 10,
			Enabled:   true,
		},
	}

	result := diffSQSTriggers(desired, existing)

	if len(result.ToUpdate) != 1 {
		t.Fatalf("Expected 1 ToUpdate, got %d", len(result.ToUpdate))
	}
	if result.ToUpdate[0].UUID != expectedUUID {
		t.Errorf("Expected UUID %s, got %s", expectedUUID, result.ToUpdate[0].UUID)
	}
}

// TestSQSTriggerDiffResultStruct tests the SQSTriggerDiffResult struct initialization.
func TestSQSTriggerDiffResultStruct(t *testing.T) {
	result := SQSTriggerDiffResult{
		ToCreate: []SQSTriggerConfig{
			{QueueArn: "arn:aws:sqs:us-east-1:123456789012:queue-1", BatchSize: 10, Enabled: true},
		},
		ToUpdate: []SQSTriggerUpdate{
			{Config: SQSTriggerConfig{QueueArn: "arn:aws:sqs:us-east-1:123456789012:queue-2", BatchSize: 20, Enabled: false}, UUID: "uuid-2"},
		},
		ToDelete: []ExistingSQSTrigger{
			{QueueArn: "arn:aws:sqs:us-east-1:123456789012:queue-3", UUID: "uuid-3", BatchSize: 30, Enabled: true},
		},
	}

	if len(result.ToCreate) != 1 {
		t.Errorf("Expected 1 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 1 {
		t.Errorf("Expected 1 ToUpdate, got %d", len(result.ToUpdate))
	}
	if len(result.ToDelete) != 1 {
		t.Errorf("Expected 1 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestSQSTriggerUpdateStruct tests the SQSTriggerUpdate struct.
func TestSQSTriggerUpdateStruct(t *testing.T) {
	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  "arn:aws:sqs:us-east-1:123456789012:my-queue",
			BatchSize: 50,
			Enabled:   false,
		},
		UUID: "12345678-1234-1234-1234-123456789012",
	}

	if update.Config.QueueArn != "arn:aws:sqs:us-east-1:123456789012:my-queue" {
		t.Errorf("Unexpected QueueArn: %s", update.Config.QueueArn)
	}
	if update.Config.BatchSize != 50 {
		t.Errorf("Unexpected BatchSize: %d", update.Config.BatchSize)
	}
	if update.Config.Enabled != false {
		t.Errorf("Unexpected Enabled: %v", update.Config.Enabled)
	}
	if update.UUID != "12345678-1234-1234-1234-123456789012" {
		t.Errorf("Unexpected UUID: %s", update.UUID)
	}
}


// ============================================================================
// createSQSTrigger Tests
// ============================================================================

// TestCreateSQSTrigger_Success tests successful creation of an SQS trigger.
// Requirements: 2.1 - When a new SQS trigger is added, create event source mapping
func TestCreateSQSTrigger_Success(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	expectedUUID := "12345678-1234-1234-1234-123456789012"

	createCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			createCalled = true
			// Verify parameters
			if *params.FunctionName != functionName {
				t.Errorf("Expected FunctionName %s, got %s", functionName, *params.FunctionName)
			}
			if *params.EventSourceArn != queueArn {
				t.Errorf("Expected EventSourceArn %s, got %s", queueArn, *params.EventSourceArn)
			}
			if *params.BatchSize != 10 {
				t.Errorf("Expected BatchSize 10, got %d", *params.BatchSize)
			}
			if *params.Enabled != true {
				t.Errorf("Expected Enabled true, got %v", *params.Enabled)
			}
			return &lambda.CreateEventSourceMappingOutput{
				UUID: aws.String(expectedUUID),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := SQSTriggerConfig{
		QueueArn:  queueArn,
		BatchSize: 10,
		Enabled:   true,
	}

	uuid, err := tm.createSQSTrigger(ctx, functionName, trigger)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !createCalled {
		t.Error("Expected CreateEventSourceMapping to be called")
	}

	if uuid != expectedUUID {
		t.Errorf("Expected UUID %s, got %s", expectedUUID, uuid)
	}
}

// TestCreateSQSTrigger_CustomBatchSize tests creating an SQS trigger with custom batch size.
// Requirements: 2.1 - Create event source mapping with specified batch_size
func TestCreateSQSTrigger_CustomBatchSize(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	expectedUUID := "12345678-1234-1234-1234-123456789012"
	customBatchSize := int32(50)

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			if *params.BatchSize != customBatchSize {
				t.Errorf("Expected BatchSize %d, got %d", customBatchSize, *params.BatchSize)
			}
			return &lambda.CreateEventSourceMappingOutput{
				UUID: aws.String(expectedUUID),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := SQSTriggerConfig{
		QueueArn:  queueArn,
		BatchSize: customBatchSize,
		Enabled:   true,
	}

	uuid, err := tm.createSQSTrigger(ctx, functionName, trigger)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if uuid != expectedUUID {
		t.Errorf("Expected UUID %s, got %s", expectedUUID, uuid)
	}
}

// TestCreateSQSTrigger_DisabledTrigger tests creating a disabled SQS trigger.
// Requirements: 2.1 - Create event source mapping with enabled=false
func TestCreateSQSTrigger_DisabledTrigger(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	expectedUUID := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			if *params.Enabled != false {
				t.Errorf("Expected Enabled false, got %v", *params.Enabled)
			}
			return &lambda.CreateEventSourceMappingOutput{
				UUID: aws.String(expectedUUID),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := SQSTriggerConfig{
		QueueArn:  queueArn,
		BatchSize: 10,
		Enabled:   false,
	}

	uuid, err := tm.createSQSTrigger(ctx, functionName, trigger)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if uuid != expectedUUID {
		t.Errorf("Expected UUID %s, got %s", expectedUUID, uuid)
	}
}

// TestCreateSQSTrigger_IdempotentOnConflict tests that createSQSTrigger handles
// ResourceConflictException by updating the existing mapping.
// Requirements: 6.3 - When an event source mapping already exists, update it rather than fail
func TestCreateSQSTrigger_IdempotentOnConflict(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	existingUUID := "existing-uuid-1234"

	createCalled := false
	listCalled := false
	updateCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			createCalled = true
			// Simulate ResourceConflictException - mapping already exists
			return nil, fmt.Errorf("ResourceConflictException: An event source mapping with SQS arn already exists")
		},
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			listCalled = true
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: aws.String(queueArn),
						UUID:           aws.String(existingUUID),
						BatchSize:      aws.Int32(10),
						State:          aws.String("Enabled"),
					},
				},
			}, nil
		},
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			updateCalled = true
			// Verify the UUID is correct
			if *params.UUID != existingUUID {
				t.Errorf("Expected UUID %s, got %s", existingUUID, *params.UUID)
			}
			// Verify the new configuration
			if *params.BatchSize != 25 {
				t.Errorf("Expected BatchSize 25, got %d", *params.BatchSize)
			}
			if *params.Enabled != true {
				t.Errorf("Expected Enabled true, got %v", *params.Enabled)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(existingUUID),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := SQSTriggerConfig{
		QueueArn:  queueArn,
		BatchSize: 25, // Different batch size than existing
		Enabled:   true,
	}

	uuid, err := tm.createSQSTrigger(ctx, functionName, trigger)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !createCalled {
		t.Error("Expected CreateEventSourceMapping to be called")
	}
	if !listCalled {
		t.Error("Expected ListEventSourceMappings to be called to find existing mapping")
	}
	if !updateCalled {
		t.Error("Expected UpdateEventSourceMapping to be called")
	}

	if uuid != existingUUID {
		t.Errorf("Expected UUID %s, got %s", existingUUID, uuid)
	}
}

// TestCreateSQSTrigger_ConflictButMappingNotFound tests the error case where
// ResourceConflictException is returned but the mapping cannot be found.
// Requirements: 6.3 - Handle edge case where conflict occurs but mapping not found
func TestCreateSQSTrigger_ConflictButMappingNotFound(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("ResourceConflictException: An event source mapping with SQS arn already exists")
		},
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			// Return empty list - mapping not found (edge case)
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{},
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := SQSTriggerConfig{
		QueueArn:  queueArn,
		BatchSize: 10,
		Enabled:   true,
	}

	_, err := tm.createSQSTrigger(ctx, functionName, trigger)
	if err == nil {
		t.Fatal("Expected error when mapping not found after conflict")
	}

	if !strings.Contains(err.Error(), "could not find existing mapping") {
		t.Errorf("Expected error about not finding existing mapping, got: %v", err)
	}
}

// TestCreateSQSTrigger_ConflictListError tests error handling when listing mappings fails
// after a ResourceConflictException.
// Requirements: 6.3 - Handle errors during idempotent recovery
func TestCreateSQSTrigger_ConflictListError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("ResourceConflictException: An event source mapping with SQS arn already exists")
		},
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return nil, fmt.Errorf("AccessDeniedException: User is not authorized")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := SQSTriggerConfig{
		QueueArn:  queueArn,
		BatchSize: 10,
		Enabled:   true,
	}

	_, err := tm.createSQSTrigger(ctx, functionName, trigger)
	if err == nil {
		t.Fatal("Expected error when listing mappings fails")
	}

	if !strings.Contains(err.Error(), "failed to find existing event source mapping") {
		t.Errorf("Expected error about finding existing mapping, got: %v", err)
	}
}

// TestCreateSQSTrigger_ConflictUpdateError tests error handling when updating the existing
// mapping fails after a ResourceConflictException.
// Requirements: 6.3 - Handle errors during idempotent recovery
func TestCreateSQSTrigger_ConflictUpdateError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	existingUUID := "existing-uuid-1234"

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("ResourceConflictException: An event source mapping with SQS arn already exists")
		},
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: aws.String(queueArn),
						UUID:           aws.String(existingUUID),
						BatchSize:      aws.Int32(10),
						State:          aws.String("Enabled"),
					},
				},
			}, nil
		},
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("ResourceInUseException: The event source mapping is currently being updated")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := SQSTriggerConfig{
		QueueArn:  queueArn,
		BatchSize: 10,
		Enabled:   true,
	}

	_, err := tm.createSQSTrigger(ctx, functionName, trigger)
	if err == nil {
		t.Fatal("Expected error when update fails")
	}

	if !strings.Contains(err.Error(), "failed to update existing event source mapping") {
		t.Errorf("Expected error about updating mapping, got: %v", err)
	}
}

// TestCreateSQSTrigger_OtherError tests that non-conflict errors are propagated.
// Requirements: 2.1 - Proper error handling for create failures
func TestCreateSQSTrigger_OtherError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("InvalidParameterValueException: The provided queue ARN is invalid")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := SQSTriggerConfig{
		QueueArn:  queueArn,
		BatchSize: 10,
		Enabled:   true,
	}

	_, err := tm.createSQSTrigger(ctx, functionName, trigger)
	if err == nil {
		t.Fatal("Expected error for invalid parameter")
	}

	if !strings.Contains(err.Error(), "failed to create event source mapping") {
		t.Errorf("Expected error about creating mapping, got: %v", err)
	}
}

// TestCreateSQSTrigger_MultipleQueuesConflict tests that the correct queue is found
// when there are multiple event source mappings.
// Requirements: 6.3 - Find correct mapping among multiple
func TestCreateSQSTrigger_MultipleQueuesConflict(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	targetQueueArn := "arn:aws:sqs:us-east-1:123456789012:target-queue"
	otherQueueArn := "arn:aws:sqs:us-east-1:123456789012:other-queue"
	targetUUID := "target-uuid-1234"
	otherUUID := "other-uuid-5678"

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("ResourceConflictException: An event source mapping with SQS arn already exists")
		},
		listEventSourceMappingsFunc: func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
			return &lambda.ListEventSourceMappingsOutput{
				EventSourceMappings: []lambdatypes.EventSourceMappingConfiguration{
					{
						EventSourceArn: aws.String(otherQueueArn),
						UUID:           aws.String(otherUUID),
						BatchSize:      aws.Int32(10),
						State:          aws.String("Enabled"),
					},
					{
						EventSourceArn: aws.String(targetQueueArn),
						UUID:           aws.String(targetUUID),
						BatchSize:      aws.Int32(10),
						State:          aws.String("Enabled"),
					},
				},
			}, nil
		},
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			// Verify the correct UUID is being updated
			if *params.UUID != targetUUID {
				t.Errorf("Expected UUID %s, got %s", targetUUID, *params.UUID)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(targetUUID),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := SQSTriggerConfig{
		QueueArn:  targetQueueArn,
		BatchSize: 25,
		Enabled:   true,
	}

	uuid, err := tm.createSQSTrigger(ctx, functionName, trigger)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if uuid != targetUUID {
		t.Errorf("Expected UUID %s, got %s", targetUUID, uuid)
	}
}

// ============================================================================
// updateSQSTrigger Tests
// ============================================================================

// TestUpdateSQSTrigger_Success tests successful update of an SQS trigger's batch_size.
// Requirements: 2.3 - When an SQS trigger's batch_size changes, update the existing event source mapping
func TestUpdateSQSTrigger_Success(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"
	newBatchSize := int32(50)

	updateCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			updateCalled = true
			// Verify parameters
			if *params.UUID != uuid {
				t.Errorf("Expected UUID %s, got %s", uuid, *params.UUID)
			}
			if *params.BatchSize != newBatchSize {
				t.Errorf("Expected BatchSize %d, got %d", newBatchSize, *params.BatchSize)
			}
			if *params.Enabled != true {
				t.Errorf("Expected Enabled true, got %v", *params.Enabled)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(uuid),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: newBatchSize,
			Enabled:   true,
		},
		UUID: uuid,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !updateCalled {
		t.Error("Expected UpdateEventSourceMapping to be called")
	}
}

// TestUpdateSQSTrigger_UpdateEnabled tests updating an SQS trigger's enabled state.
// Requirements: 2.3 - When an SQS trigger's configuration changes, update the existing event source mapping
func TestUpdateSQSTrigger_UpdateEnabled(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			// Verify enabled is set to false
			if *params.Enabled != false {
				t.Errorf("Expected Enabled false, got %v", *params.Enabled)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(uuid),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: 10,
			Enabled:   false, // Disabling the trigger
		},
		UUID: uuid,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// TestUpdateSQSTrigger_UpdateBothBatchSizeAndEnabled tests updating both batch_size and enabled.
// Requirements: 2.3 - When an SQS trigger's configuration changes, update the existing event source mapping
func TestUpdateSQSTrigger_UpdateBothBatchSizeAndEnabled(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"
	newBatchSize := int32(100)

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			// Verify both parameters are updated
			if *params.BatchSize != newBatchSize {
				t.Errorf("Expected BatchSize %d, got %d", newBatchSize, *params.BatchSize)
			}
			if *params.Enabled != false {
				t.Errorf("Expected Enabled false, got %v", *params.Enabled)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(uuid),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: newBatchSize,
			Enabled:   false,
		},
		UUID: uuid,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// TestUpdateSQSTrigger_APIError tests error handling when UpdateEventSourceMapping fails.
// Requirements: 2.3 - Proper error handling for update failures
func TestUpdateSQSTrigger_APIError(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("ResourceNotFoundException: The event source mapping does not exist")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: 50,
			Enabled:   true,
		},
		UUID: uuid,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if !strings.Contains(err.Error(), "failed to update event source mapping") {
		t.Errorf("Expected error message to contain 'failed to update event source mapping', got: %v", err)
	}

	if !strings.Contains(err.Error(), uuid) {
		t.Errorf("Expected error message to contain UUID %s, got: %v", uuid, err)
	}
}

// TestUpdateSQSTrigger_ResourceInUseError tests error handling when the mapping is being updated.
// Requirements: 2.3 - Proper error handling for concurrent update failures
func TestUpdateSQSTrigger_ResourceInUseError(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("ResourceInUseException: The event source mapping is currently being updated")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: 50,
			Enabled:   true,
		},
		UUID: uuid,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if !strings.Contains(err.Error(), "ResourceInUseException") {
		t.Errorf("Expected error to contain 'ResourceInUseException', got: %v", err)
	}
}

// TestUpdateSQSTrigger_InvalidParameterError tests error handling for invalid parameters.
// Requirements: 2.3 - Proper error handling for invalid configuration
func TestUpdateSQSTrigger_InvalidParameterError(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("InvalidParameterValueException: BatchSize must be between 1 and 10000")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: 99999, // Invalid batch size
			Enabled:   true,
		},
		UUID: uuid,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if !strings.Contains(err.Error(), "InvalidParameterValueException") {
		t.Errorf("Expected error to contain 'InvalidParameterValueException', got: %v", err)
	}
}

// TestUpdateSQSTrigger_MinBatchSize tests updating to minimum batch size (1).
// Requirements: 2.3 - Support valid batch_size range
func TestUpdateSQSTrigger_MinBatchSize(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"
	minBatchSize := int32(1)

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			if *params.BatchSize != minBatchSize {
				t.Errorf("Expected BatchSize %d, got %d", minBatchSize, *params.BatchSize)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(uuid),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: minBatchSize,
			Enabled:   true,
		},
		UUID: uuid,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// TestUpdateSQSTrigger_MaxBatchSize tests updating to maximum batch size (10000 for standard queues).
// Requirements: 2.3 - Support valid batch_size range
func TestUpdateSQSTrigger_MaxBatchSize(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"
	maxBatchSize := int32(10000)

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			if *params.BatchSize != maxBatchSize {
				t.Errorf("Expected BatchSize %d, got %d", maxBatchSize, *params.BatchSize)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(uuid),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: maxBatchSize,
			Enabled:   true,
		},
		UUID: uuid,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// TestUpdateSQSTrigger_CorrectUUIDPassed tests that the correct UUID is passed to the API.
// Requirements: 2.3 - Update the correct event source mapping by UUID
func TestUpdateSQSTrigger_CorrectUUIDPassed(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	expectedUUID := "specific-uuid-12345678"

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			if *params.UUID != expectedUUID {
				t.Errorf("Expected UUID %s, got %s", expectedUUID, *params.UUID)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(expectedUUID),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: 25,
			Enabled:   true,
		},
		UUID: expectedUUID,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// TestUpdateSQSTrigger_EnableDisabledTrigger tests re-enabling a disabled trigger.
// Requirements: 2.3 - Support enabling/disabling triggers
func TestUpdateSQSTrigger_EnableDisabledTrigger(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			// Verify enabled is set to true (re-enabling)
			if *params.Enabled != true {
				t.Errorf("Expected Enabled true, got %v", *params.Enabled)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(uuid),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	update := SQSTriggerUpdate{
		Config: SQSTriggerConfig{
			QueueArn:  queueArn,
			BatchSize: 10,
			Enabled:   true, // Re-enabling the trigger
		},
		UUID: uuid,
	}

	err := tm.updateSQSTrigger(ctx, update)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// ============================================================================
// deleteSQSTrigger Tests
// ============================================================================

// TestDeleteSQSTrigger_Success tests successful deletion of an SQS trigger.
// Requirements: 2.2 - When an SQS trigger is removed from config, delete the event source mapping
func TestDeleteSQSTrigger_Success(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	deleteEventSourceMappingCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			deleteEventSourceMappingCalled = true
			// Verify UUID is passed correctly
			if params.UUID == nil || *params.UUID != uuid {
				t.Errorf("Expected UUID %s, got %v", uuid, params.UUID)
			}
			return &lambda.DeleteEventSourceMappingOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := ExistingSQSTrigger{
		QueueArn:  queueArn,
		UUID:      uuid,
		BatchSize: 10,
		Enabled:   true,
	}

	err := tm.deleteSQSTrigger(ctx, trigger)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !deleteEventSourceMappingCalled {
		t.Error("Expected DeleteEventSourceMapping to be called")
	}
}

// TestDeleteSQSTrigger_Idempotent_ResourceNotFound tests that deleting a non-existent
// event source mapping is treated as success (idempotent).
// Requirements: 6.4 - When deleting a trigger that doesn't exist, treat it as success
func TestDeleteSQSTrigger_Idempotent_ResourceNotFound(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "non-existent-uuid"

	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			// Simulate ResourceNotFoundException - mapping doesn't exist
			return nil, fmt.Errorf("ResourceNotFoundException: The resource you requested does not exist")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := ExistingSQSTrigger{
		QueueArn:  queueArn,
		UUID:      uuid,
		BatchSize: 10,
		Enabled:   true,
	}

	// Should NOT return an error - idempotent success
	err := tm.deleteSQSTrigger(ctx, trigger)
	if err != nil {
		t.Fatalf("Expected no error for ResourceNotFoundException (idempotent), got: %v", err)
	}
}

// TestDeleteSQSTrigger_APIError tests that non-ResourceNotFoundException errors are returned.
// Requirements: 2.2 - Delete should fail on actual errors
func TestDeleteSQSTrigger_APIError(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			// Simulate a real error (not ResourceNotFoundException)
			return nil, fmt.Errorf("AccessDeniedException: User is not authorized to perform lambda:DeleteEventSourceMapping")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := ExistingSQSTrigger{
		QueueArn:  queueArn,
		UUID:      uuid,
		BatchSize: 10,
		Enabled:   true,
	}

	err := tm.deleteSQSTrigger(ctx, trigger)
	if err == nil {
		t.Fatal("Expected error for AccessDeniedException, got nil")
	}

	if !strings.Contains(err.Error(), "failed to delete event source mapping") {
		t.Errorf("Expected error message to contain 'failed to delete event source mapping', got: %v", err)
	}
}

// TestDeleteSQSTrigger_CorrectUUIDPassed tests that the correct UUID is passed to the API.
// Requirements: 2.2 - Delete the correct event source mapping by UUID
func TestDeleteSQSTrigger_CorrectUUIDPassed(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	expectedUUID := "specific-uuid-12345678"

	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			if *params.UUID != expectedUUID {
				t.Errorf("Expected UUID %s, got %s", expectedUUID, *params.UUID)
			}
			return &lambda.DeleteEventSourceMappingOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := ExistingSQSTrigger{
		QueueArn:  queueArn,
		UUID:      expectedUUID,
		BatchSize: 10,
		Enabled:   true,
	}

	err := tm.deleteSQSTrigger(ctx, trigger)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// TestDeleteSQSTrigger_DisabledTrigger tests deleting a disabled trigger.
// Requirements: 2.2 - Delete should work regardless of enabled state
func TestDeleteSQSTrigger_DisabledTrigger(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	deleteEventSourceMappingCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			deleteEventSourceMappingCalled = true
			return &lambda.DeleteEventSourceMappingOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := ExistingSQSTrigger{
		QueueArn:  queueArn,
		UUID:      uuid,
		BatchSize: 10,
		Enabled:   false, // Disabled trigger
	}

	err := tm.deleteSQSTrigger(ctx, trigger)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !deleteEventSourceMappingCalled {
		t.Error("Expected DeleteEventSourceMapping to be called for disabled trigger")
	}
}

// TestDeleteSQSTrigger_ServiceException tests that ServiceException errors are returned.
// Requirements: 2.2 - Delete should fail on service errors
func TestDeleteSQSTrigger_ServiceException(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("ServiceException: Internal server error")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := ExistingSQSTrigger{
		QueueArn:  queueArn,
		UUID:      uuid,
		BatchSize: 10,
		Enabled:   true,
	}

	err := tm.deleteSQSTrigger(ctx, trigger)
	if err == nil {
		t.Fatal("Expected error for ServiceException, got nil")
	}

	if !strings.Contains(err.Error(), "failed to delete event source mapping") {
		t.Errorf("Expected error message to contain 'failed to delete event source mapping', got: %v", err)
	}
}

// TestDeleteSQSTrigger_TooManyRequestsException tests that rate limiting errors are returned.
// Requirements: 2.2 - Delete should fail on rate limiting (caller can retry)
func TestDeleteSQSTrigger_TooManyRequestsException(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("TooManyRequestsException: Rate exceeded")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := ExistingSQSTrigger{
		QueueArn:  queueArn,
		UUID:      uuid,
		BatchSize: 10,
		Enabled:   true,
	}

	err := tm.deleteSQSTrigger(ctx, trigger)
	if err == nil {
		t.Fatal("Expected error for TooManyRequestsException, got nil")
	}

	if !strings.Contains(err.Error(), "failed to delete event source mapping") {
		t.Errorf("Expected error message to contain 'failed to delete event source mapping', got: %v", err)
	}
}

// TestDeleteSQSTrigger_EmptyUUID tests behavior when UUID is empty.
// Requirements: 2.2 - Handle edge case of empty UUID
func TestDeleteSQSTrigger_EmptyUUID(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			// AWS would return an error for empty UUID
			if params.UUID == nil || *params.UUID == "" {
				return nil, fmt.Errorf("ValidationException: UUID cannot be empty")
			}
			return &lambda.DeleteEventSourceMappingOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := ExistingSQSTrigger{
		QueueArn:  queueArn,
		UUID:      "", // Empty UUID
		BatchSize: 10,
		Enabled:   true,
	}

	err := tm.deleteSQSTrigger(ctx, trigger)
	if err == nil {
		t.Fatal("Expected error for empty UUID, got nil")
	}
}

// TestDeleteSQSTrigger_ResourceInUseException tests that ResourceInUseException errors are returned.
// This can happen if the mapping is being updated while we try to delete it.
// Requirements: 2.2 - Delete should fail on resource conflicts
func TestDeleteSQSTrigger_ResourceInUseException(t *testing.T) {
	ctx := context.Background()
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			return nil, fmt.Errorf("ResourceInUseException: The event source mapping is currently being updated")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	trigger := ExistingSQSTrigger{
		QueueArn:  queueArn,
		UUID:      uuid,
		BatchSize: 10,
		Enabled:   true,
	}

	err := tm.deleteSQSTrigger(ctx, trigger)
	if err == nil {
		t.Fatal("Expected error for ResourceInUseException, got nil")
	}

	if !strings.Contains(err.Error(), "failed to delete event source mapping") {
		t.Errorf("Expected error message to contain 'failed to delete event source mapping', got: %v", err)
	}
}

// ============================================================================
// ReconcileSQSTriggers Tests
// ============================================================================

// reconcileSQSTriggers is a testable version of ReconcileSQSTriggers that uses interfaces.
// This allows us to test the orchestration logic without real AWS clients.
//
// Requirements: 2.5, 4.5
func (tm *testableTriggerManager) reconcileSQSTriggers(
	ctx context.Context,
	functionName string,
	desiredTriggers []SQSTriggerConfig,
	existingTriggers []ExistingSQSTrigger,
) error {
	// Step 1: Diff desired vs existing to determine actions
	diff := diffSQSTriggers(desiredTriggers, existingTriggers)

	// Track success/failure counts for final error determination
	totalOperations := len(diff.ToDelete) + len(diff.ToUpdate) + len(diff.ToCreate)
	if totalOperations == 0 {
		return nil
	}

	successCount := 0
	var lastError error

	// Step 2: Delete orphaned triggers first
	for _, trigger := range diff.ToDelete {
		err := tm.deleteSQSTrigger(ctx, trigger)
		if err != nil {
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 3: Update triggers with changed batch_size or enabled
	for _, update := range diff.ToUpdate {
		err := tm.updateSQSTrigger(ctx, update)
		if err != nil {
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 4: Create new triggers
	for _, trigger := range diff.ToCreate {
		_, err := tm.createSQSTrigger(ctx, functionName, trigger)
		if err != nil {
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 5: Determine final result - only return error if ALL operations failed
	if successCount == 0 && totalOperations > 0 {
		return fmt.Errorf("all SQS trigger operations failed, last error: %w", lastError)
	}

	return nil
}

// TestReconcileSQSTriggers_NoChanges tests reconciliation when no changes are needed.
// Requirements: 4.5 - Idempotent reconciliation
func TestReconcileSQSTriggers_NoChanges(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	// Desired and existing match exactly
	desired := []SQSTriggerConfig{
		{QueueArn: queueArn, BatchSize: 10, Enabled: true},
	}
	existing := []ExistingSQSTrigger{
		{QueueArn: queueArn, UUID: "uuid-1", BatchSize: 10, Enabled: true},
	}

	// No AWS calls should be made
	mockLambda := &mockLambdaClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestReconcileSQSTriggers_CreateOnly tests reconciliation when only creates are needed.
// Requirements: 2.1 - When a new SQS trigger is added, create event source mapping
func TestReconcileSQSTriggers_CreateOnly(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"

	desired := []SQSTriggerConfig{
		{QueueArn: queueArn, BatchSize: 10, Enabled: true},
	}
	existing := []ExistingSQSTrigger{} // No existing triggers

	createCalled := false
	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			createCalled = true
			if *params.EventSourceArn != queueArn {
				t.Errorf("Expected EventSourceArn %s, got %s", queueArn, *params.EventSourceArn)
			}
			return &lambda.CreateEventSourceMappingOutput{
				UUID: aws.String("new-uuid"),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !createCalled {
		t.Error("Expected CreateEventSourceMapping to be called")
	}
}

// TestReconcileSQSTriggers_DeleteOnly tests reconciliation when only deletes are needed.
// Requirements: 2.2 - When an SQS trigger is removed from config, delete the event source mapping
func TestReconcileSQSTriggers_DeleteOnly(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "existing-uuid"

	desired := []SQSTriggerConfig{} // No desired triggers
	existing := []ExistingSQSTrigger{
		{QueueArn: queueArn, UUID: uuid, BatchSize: 10, Enabled: true},
	}

	deleteCalled := false
	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			deleteCalled = true
			if *params.UUID != uuid {
				t.Errorf("Expected UUID %s, got %s", uuid, *params.UUID)
			}
			return &lambda.DeleteEventSourceMappingOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !deleteCalled {
		t.Error("Expected DeleteEventSourceMapping to be called")
	}
}

// TestReconcileSQSTriggers_UpdateOnly tests reconciliation when only updates are needed.
// Requirements: 2.3 - When an SQS trigger's batch_size changes, update the event source mapping
func TestReconcileSQSTriggers_UpdateOnly(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "existing-uuid"

	desired := []SQSTriggerConfig{
		{QueueArn: queueArn, BatchSize: 50, Enabled: true}, // Changed batch_size
	}
	existing := []ExistingSQSTrigger{
		{QueueArn: queueArn, UUID: uuid, BatchSize: 10, Enabled: true},
	}

	updateCalled := false
	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			updateCalled = true
			if *params.UUID != uuid {
				t.Errorf("Expected UUID %s, got %s", uuid, *params.UUID)
			}
			if *params.BatchSize != 50 {
				t.Errorf("Expected BatchSize 50, got %d", *params.BatchSize)
			}
			return &lambda.UpdateEventSourceMappingOutput{
				UUID: aws.String(uuid),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
	}

	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !updateCalled {
		t.Error("Expected UpdateEventSourceMapping to be called")
	}
}

// TestReconcileSQSTriggers_MixedOperations tests reconciliation with create, update, and delete.
// Requirements: 2.1, 2.2, 2.3, 4.5 - Full lifecycle management
func TestReconcileSQSTriggers_MixedOperations(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn1 := "arn:aws:sqs:us-east-1:123456789012:queue-1" // To delete
	queueArn2 := "arn:aws:sqs:us-east-1:123456789012:queue-2" // To update
	queueArn3 := "arn:aws:sqs:us-east-1:123456789012:queue-3" // To create

	desired := []SQSTriggerConfig{
		{QueueArn: queueArn2, BatchSize: 100, Enabled: true}, // Update batch_size
		{QueueArn: queueArn3, BatchSize: 10, Enabled: true},  // Create new
	}
	existing := []ExistingSQSTrigger{
		{QueueArn: queueArn1, UUID: "uuid-1", BatchSize: 10, Enabled: true}, // Delete
		{QueueArn: queueArn2, UUID: "uuid-2", BatchSize: 10, Enabled: true}, // Update
	}

	createCalled := false
	updateCalled := false
	deleteCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			createCalled = true
			if *params.EventSourceArn != queueArn3 {
				t.Errorf("Expected create for %s, got %s", queueArn3, *params.EventSourceArn)
			}
			return &lambda.CreateEventSourceMappingOutput{UUID: aws.String("uuid-3")}, nil
		},
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			updateCalled = true
			if *params.UUID != "uuid-2" {
				t.Errorf("Expected update for uuid-2, got %s", *params.UUID)
			}
			return &lambda.UpdateEventSourceMappingOutput{UUID: aws.String("uuid-2")}, nil
		},
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			deleteCalled = true
			if *params.UUID != "uuid-1" {
				t.Errorf("Expected delete for uuid-1, got %s", *params.UUID)
			}
			return &lambda.DeleteEventSourceMappingOutput{}, nil
		},
	}

	tm := &testableTriggerManager{lambdaClient: mockLambda}
	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !createCalled {
		t.Error("Expected create to be called")
	}
	if !updateCalled {
		t.Error("Expected update to be called")
	}
	if !deleteCalled {
		t.Error("Expected delete to be called")
	}
}

// TestReconcileSQSTriggers_ContinueOnFailure tests that reconciliation continues
// when individual operations fail.
// Requirements: 2.5 - IF creating an SQS trigger fails, THEN log a warning and continue
func TestReconcileSQSTriggers_ContinueOnFailure(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn1 := "arn:aws:sqs:us-east-1:123456789012:queue-1"
	queueArn2 := "arn:aws:sqs:us-east-1:123456789012:queue-2"

	desired := []SQSTriggerConfig{
		{QueueArn: queueArn1, BatchSize: 10, Enabled: true},
		{QueueArn: queueArn2, BatchSize: 10, Enabled: true},
	}
	existing := []ExistingSQSTrigger{}

	createCallCount := 0
	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			createCallCount++
			// First call fails, second succeeds
			if *params.EventSourceArn == queueArn1 {
				return nil, fmt.Errorf("AccessDeniedException: Not authorized")
			}
			return &lambda.CreateEventSourceMappingOutput{UUID: aws.String("uuid-2")}, nil
		},
	}

	tm := &testableTriggerManager{lambdaClient: mockLambda}
	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	// Should NOT return error because one operation succeeded
	if err != nil {
		t.Errorf("Expected no error (partial success), got: %v", err)
	}
	// Both creates should have been attempted
	if createCallCount != 2 {
		t.Errorf("Expected 2 create calls, got %d", createCallCount)
	}
}

// TestReconcileSQSTriggers_AllOperationsFail tests that error is returned only
// when ALL operations fail.
// Requirements: 2.5 - Return error only if all operations failed
func TestReconcileSQSTriggers_AllOperationsFail(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn1 := "arn:aws:sqs:us-east-1:123456789012:queue-1"
	queueArn2 := "arn:aws:sqs:us-east-1:123456789012:queue-2"

	desired := []SQSTriggerConfig{
		{QueueArn: queueArn1, BatchSize: 10, Enabled: true},
		{QueueArn: queueArn2, BatchSize: 10, Enabled: true},
	}
	existing := []ExistingSQSTrigger{}

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			// All creates fail
			return nil, fmt.Errorf("AccessDeniedException: Not authorized")
		},
	}

	tm := &testableTriggerManager{lambdaClient: mockLambda}
	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	// Should return error because ALL operations failed
	if err == nil {
		t.Error("Expected error when all operations fail, got nil")
	}
	if !strings.Contains(err.Error(), "all SQS trigger operations failed") {
		t.Errorf("Expected error message about all operations failing, got: %v", err)
	}
}

// TestReconcileSQSTriggers_UpdateEnabledState tests reconciliation when enabled state changes.
// Requirements: 2.3 - Update event source mapping when enabled changes
func TestReconcileSQSTriggers_UpdateEnabledState(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn := "arn:aws:sqs:us-east-1:123456789012:my-queue"
	uuid := "existing-uuid"

	desired := []SQSTriggerConfig{
		{QueueArn: queueArn, BatchSize: 10, Enabled: false}, // Disable trigger
	}
	existing := []ExistingSQSTrigger{
		{QueueArn: queueArn, UUID: uuid, BatchSize: 10, Enabled: true},
	}

	updateCalled := false
	mockLambda := &mockLambdaClientForTrigger{
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			updateCalled = true
			if *params.Enabled != false {
				t.Errorf("Expected Enabled false, got %v", *params.Enabled)
			}
			return &lambda.UpdateEventSourceMappingOutput{UUID: aws.String(uuid)}, nil
		},
	}

	tm := &testableTriggerManager{lambdaClient: mockLambda}
	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !updateCalled {
		t.Error("Expected UpdateEventSourceMapping to be called for enabled state change")
	}
}

// TestReconcileSQSTriggers_QueueArnChange tests reconciliation when queue_arn changes.
// Requirements: 2.4 - When queue_arn changes, delete old mapping and create new one
func TestReconcileSQSTriggers_QueueArnChange(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	oldQueueArn := "arn:aws:sqs:us-east-1:123456789012:old-queue"
	newQueueArn := "arn:aws:sqs:us-east-1:123456789012:new-queue"
	oldUUID := "old-uuid"

	// New queue ARN means old trigger should be deleted and new one created
	desired := []SQSTriggerConfig{
		{QueueArn: newQueueArn, BatchSize: 10, Enabled: true},
	}
	existing := []ExistingSQSTrigger{
		{QueueArn: oldQueueArn, UUID: oldUUID, BatchSize: 10, Enabled: true},
	}

	createCalled := false
	deleteCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			createCalled = true
			if *params.EventSourceArn != newQueueArn {
				t.Errorf("Expected create for %s, got %s", newQueueArn, *params.EventSourceArn)
			}
			return &lambda.CreateEventSourceMappingOutput{UUID: aws.String("new-uuid")}, nil
		},
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			deleteCalled = true
			if *params.UUID != oldUUID {
				t.Errorf("Expected delete for %s, got %s", oldUUID, *params.UUID)
			}
			return &lambda.DeleteEventSourceMappingOutput{}, nil
		},
	}

	tm := &testableTriggerManager{lambdaClient: mockLambda}
	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !createCalled {
		t.Error("Expected create to be called for new queue ARN")
	}
	if !deleteCalled {
		t.Error("Expected delete to be called for old queue ARN")
	}
}

// TestReconcileSQSTriggers_MultipleTriggers tests reconciliation with multiple triggers.
// Requirements: 4.5 - Idempotent reconciliation with multiple triggers
func TestReconcileSQSTriggers_MultipleTriggers(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn1 := "arn:aws:sqs:us-east-1:123456789012:queue-1"
	queueArn2 := "arn:aws:sqs:us-east-1:123456789012:queue-2"
	queueArn3 := "arn:aws:sqs:us-east-1:123456789012:queue-3"

	desired := []SQSTriggerConfig{
		{QueueArn: queueArn1, BatchSize: 10, Enabled: true},
		{QueueArn: queueArn2, BatchSize: 20, Enabled: true},
		{QueueArn: queueArn3, BatchSize: 30, Enabled: false},
	}
	existing := []ExistingSQSTrigger{
		{QueueArn: queueArn1, UUID: "uuid-1", BatchSize: 10, Enabled: true},
		{QueueArn: queueArn2, UUID: "uuid-2", BatchSize: 20, Enabled: true},
		{QueueArn: queueArn3, UUID: "uuid-3", BatchSize: 30, Enabled: false},
	}

	// No operations should be called since everything matches
	mockLambda := &mockLambdaClientForTrigger{
		createEventSourceMappingFunc: func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
			t.Error("Unexpected create call")
			return nil, nil
		},
		updateEventSourceMappingFunc: func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
			t.Error("Unexpected update call")
			return nil, nil
		},
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			t.Error("Unexpected delete call")
			return nil, nil
		},
	}

	tm := &testableTriggerManager{lambdaClient: mockLambda}
	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestReconcileSQSTriggers_EmptyDesiredAndExisting tests reconciliation with no triggers.
// Requirements: 4.5 - Idempotent reconciliation
func TestReconcileSQSTriggers_EmptyDesiredAndExisting(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"

	desired := []SQSTriggerConfig{}
	existing := []ExistingSQSTrigger{}

	mockLambda := &mockLambdaClientForTrigger{}

	tm := &testableTriggerManager{lambdaClient: mockLambda}
	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestReconcileSQSTriggers_DeleteFailureContinues tests that delete failures don't stop other operations.
// Requirements: 2.5 - Continue on individual failures
func TestReconcileSQSTriggers_DeleteFailureContinues(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	queueArn1 := "arn:aws:sqs:us-east-1:123456789012:queue-1"
	queueArn2 := "arn:aws:sqs:us-east-1:123456789012:queue-2"

	desired := []SQSTriggerConfig{} // Delete both
	existing := []ExistingSQSTrigger{
		{QueueArn: queueArn1, UUID: "uuid-1", BatchSize: 10, Enabled: true},
		{QueueArn: queueArn2, UUID: "uuid-2", BatchSize: 10, Enabled: true},
	}

	deleteCallCount := 0
	mockLambda := &mockLambdaClientForTrigger{
		deleteEventSourceMappingFunc: func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
			deleteCallCount++
			// First delete fails, second succeeds
			if *params.UUID == "uuid-1" {
				return nil, fmt.Errorf("ServiceException: Internal error")
			}
			return &lambda.DeleteEventSourceMappingOutput{}, nil
		},
	}

	tm := &testableTriggerManager{lambdaClient: mockLambda}
	err := tm.reconcileSQSTriggers(ctx, functionName, desired, existing)

	// Should NOT return error because one operation succeeded
	if err != nil {
		t.Errorf("Expected no error (partial success), got: %v", err)
	}
	// Both deletes should have been attempted
	if deleteCallCount != 2 {
		t.Errorf("Expected 2 delete calls, got %d", deleteCallCount)
	}
}
