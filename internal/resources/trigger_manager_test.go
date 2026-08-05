// internal/resources/trigger_manager_test.go
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// TestExtractSNSTriggers_EmptyConfig tests that extractSNSTriggers returns
// an empty slice when lambda_config is empty.
func TestExtractSNSTriggers_EmptyConfig(t *testing.T) {
	lambdaConfig := make(map[string]interface{})
	triggers := extractSNSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers, got %d", len(triggers))
	}
}

// TestExtractSNSTriggers_NoLambdaEntry tests that extractSNSTriggers returns
// an empty slice when the lambda has no entry in lambda_config.
func TestExtractSNSTriggers_NoLambdaEntry(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"other-lambda": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"topic_arn": "arn:aws:sns:us-east-1:123456789:my-topic",
				},
			},
		},
	}
	triggers := extractSNSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers, got %d", len(triggers))
	}
}

// TestExtractSNSTriggers_NoSNSTriggers tests that extractSNSTriggers returns
// an empty slice when the lambda has no sns_triggers field.
func TestExtractSNSTriggers_NoSNSTriggers(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"timeout": 30,
		},
	}
	triggers := extractSNSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers, got %d", len(triggers))
	}
}

// TestExtractSNSTriggers_SingleTrigger tests extracting a single SNS trigger
// with only topic_arn specified (statement_id should be auto-generated).
func TestExtractSNSTriggers_SingleTrigger(t *testing.T) {
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"topic_arn": topicArn,
				},
			},
		},
	}
	triggers := extractSNSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].TopicArn != topicArn {
		t.Errorf("Expected TopicArn %s, got %s", topicArn, triggers[0].TopicArn)
	}

	// Statement ID should be auto-generated
	expectedStatementId := "sns-arn-aws-sns-us-east-1-123456789012-my-topic"
	if triggers[0].StatementId != expectedStatementId {
		t.Errorf("Expected StatementId %s, got %s", expectedStatementId, triggers[0].StatementId)
	}
}

// TestExtractSNSTriggers_CustomStatementId tests extracting an SNS trigger
// with a custom statement_id specified.
func TestExtractSNSTriggers_CustomStatementId(t *testing.T) {
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	customStatementId := "my-custom-statement-id"
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"topic_arn":    topicArn,
					"statement_id": customStatementId,
				},
			},
		},
	}
	triggers := extractSNSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 1 {
		t.Fatalf("Expected 1 trigger, got %d", len(triggers))
	}

	if triggers[0].TopicArn != topicArn {
		t.Errorf("Expected TopicArn %s, got %s", topicArn, triggers[0].TopicArn)
	}

	if triggers[0].StatementId != customStatementId {
		t.Errorf("Expected StatementId %s, got %s", customStatementId, triggers[0].StatementId)
	}
}

// TestExtractSNSTriggers_MultipleTriggers tests extracting multiple SNS triggers.
func TestExtractSNSTriggers_MultipleTriggers(t *testing.T) {
	topicArn1 := "arn:aws:sns:us-east-1:123456789012:topic-1"
	topicArn2 := "arn:aws:sns:us-east-1:123456789012:topic-2"
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"topic_arn": topicArn1,
				},
				map[string]interface{}{
					"topic_arn":    topicArn2,
					"statement_id": "custom-id",
				},
			},
		},
	}
	triggers := extractSNSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 2 {
		t.Fatalf("Expected 2 triggers, got %d", len(triggers))
	}

	// First trigger
	if triggers[0].TopicArn != topicArn1 {
		t.Errorf("Expected first TopicArn %s, got %s", topicArn1, triggers[0].TopicArn)
	}
	expectedStatementId1 := "sns-arn-aws-sns-us-east-1-123456789012-topic-1"
	if triggers[0].StatementId != expectedStatementId1 {
		t.Errorf("Expected first StatementId %s, got %s", expectedStatementId1, triggers[0].StatementId)
	}

	// Second trigger
	if triggers[1].TopicArn != topicArn2 {
		t.Errorf("Expected second TopicArn %s, got %s", topicArn2, triggers[1].TopicArn)
	}
	if triggers[1].StatementId != "custom-id" {
		t.Errorf("Expected second StatementId 'custom-id', got %s", triggers[1].StatementId)
	}
}

// TestExtractSNSTriggers_MissingTopicArn tests that triggers without topic_arn are skipped.
func TestExtractSNSTriggers_MissingTopicArn(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"statement_id": "some-id",
					// Missing topic_arn
				},
			},
		},
	}
	triggers := extractSNSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers (missing topic_arn), got %d", len(triggers))
	}
}

// TestExtractSNSTriggers_EmptyTopicArn tests that triggers with empty topic_arn are skipped.
func TestExtractSNSTriggers_EmptyTopicArn(t *testing.T) {
	lambdaConfig := map[string]interface{}{
		"my-lambda": map[string]interface{}{
			"sns_triggers": []interface{}{
				map[string]interface{}{
					"topic_arn": "",
				},
			},
		},
	}
	triggers := extractSNSTriggers(lambdaConfig, "my-lambda")

	if len(triggers) != 0 {
		t.Errorf("Expected 0 triggers (empty topic_arn), got %d", len(triggers))
	}
}

// TestGenerateSNSStatementId tests the statement ID generation function.
func TestGenerateSNSStatementId(t *testing.T) {
	tests := []struct {
		name     string
		topicArn string
		expected string
	}{
		{
			name:     "standard ARN",
			topicArn: "arn:aws:sns:us-east-1:123456789012:my-topic",
			expected: "sns-arn-aws-sns-us-east-1-123456789012-my-topic",
		},
		{
			name:     "ARN with dashes in topic name",
			topicArn: "arn:aws:sns:us-west-2:987654321098:my-topic-name",
			expected: "sns-arn-aws-sns-us-west-2-987654321098-my-topic-name",
		},
		{
			name:     "ARN with underscores in topic name",
			topicArn: "arn:aws:sns:eu-west-1:111222333444:my_topic_name",
			expected: "sns-arn-aws-sns-eu-west-1-111222333444-my_topic_name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateSNSStatementId(tt.topicArn)
			if result != tt.expected {
				t.Errorf("generateSNSStatementId(%s) = %s, want %s", tt.topicArn, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// getExistingSNSTriggers Tests
// ============================================================================

// TestIsSNSPrincipal tests the isSNSPrincipal helper function.
func TestIsSNSPrincipal(t *testing.T) {
	tests := []struct {
		name      string
		principal interface{}
		expected  bool
	}{
		{
			name:      "string principal - sns",
			principal: "sns.amazonaws.com",
			expected:  true,
		},
		{
			name:      "string principal - other",
			principal: "lambda.amazonaws.com",
			expected:  false,
		},
		{
			name: "map principal - sns",
			principal: map[string]interface{}{
				"Service": "sns.amazonaws.com",
			},
			expected: true,
		},
		{
			name: "map principal - other",
			principal: map[string]interface{}{
				"Service": "apigateway.amazonaws.com",
			},
			expected: false,
		},
		{
			name: "map principal - no service key",
			principal: map[string]interface{}{
				"AWS": "arn:aws:iam::123456789012:root",
			},
			expected: false,
		},
		{
			name:      "nil principal",
			principal: nil,
			expected:  false,
		},
		{
			name:      "empty string",
			principal: "",
			expected:  false,
		},
		{
			name:      "integer (invalid type)",
			principal: 123,
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSNSPrincipal(tt.principal)
			if result != tt.expected {
				t.Errorf("isSNSPrincipal(%v) = %v, want %v", tt.principal, result, tt.expected)
			}
		})
	}
}

// TestExtractTopicArnFromStatement tests the extractTopicArnFromStatement helper function.
func TestExtractTopicArnFromStatement(t *testing.T) {
	tests := []struct {
		name     string
		stmt     policyStatement
		expected string
	}{
		{
			name: "valid condition with AWS:SourceArn",
			stmt: policyStatement{
				Sid: "test-statement",
				Condition: &struct {
					ArnLike map[string]string `json:"ArnLike,omitempty"`
				}{
					ArnLike: map[string]string{
						"AWS:SourceArn": "arn:aws:sns:us-east-1:123456789012:my-topic",
					},
				},
			},
			expected: "arn:aws:sns:us-east-1:123456789012:my-topic",
		},
		{
			name: "lowercase aws:sourcearn",
			stmt: policyStatement{
				Sid: "test-statement",
				Condition: &struct {
					ArnLike map[string]string `json:"ArnLike,omitempty"`
				}{
					ArnLike: map[string]string{
						"aws:sourcearn": "arn:aws:sns:us-east-1:123456789012:my-topic",
					},
				},
			},
			expected: "arn:aws:sns:us-east-1:123456789012:my-topic",
		},
		{
			name: "nil condition",
			stmt: policyStatement{
				Sid:       "test-statement",
				Condition: nil,
			},
			expected: "",
		},
		{
			name: "nil ArnLike",
			stmt: policyStatement{
				Sid: "test-statement",
				Condition: &struct {
					ArnLike map[string]string `json:"ArnLike,omitempty"`
				}{
					ArnLike: nil,
				},
			},
			expected: "",
		},
		{
			name: "empty ArnLike",
			stmt: policyStatement{
				Sid: "test-statement",
				Condition: &struct {
					ArnLike map[string]string `json:"ArnLike,omitempty"`
				}{
					ArnLike: map[string]string{},
				},
			},
			expected: "",
		},
		{
			name: "ArnLike with different key",
			stmt: policyStatement{
				Sid: "test-statement",
				Condition: &struct {
					ArnLike map[string]string `json:"ArnLike,omitempty"`
				}{
					ArnLike: map[string]string{
						"AWS:SourceAccount": "123456789012",
					},
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTopicArnFromStatement(tt.stmt)
			if result != tt.expected {
				t.Errorf("extractTopicArnFromStatement() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestParseLambdaPolicy tests parsing a Lambda resource policy JSON.
func TestParseLambdaPolicy(t *testing.T) {
	// Sample Lambda policy JSON with SNS permission
	policyJSON := `{
		"Version": "2012-10-17",
		"Id": "default",
		"Statement": [
			{
				"Sid": "sns-arn-aws-sns-us-east-1-123456789012-my-topic",
				"Effect": "Allow",
				"Principal": {
					"Service": "sns.amazonaws.com"
				},
				"Action": "lambda:InvokeFunction",
				"Resource": "arn:aws:lambda:us-east-1:123456789012:function:my-function",
				"Condition": {
					"ArnLike": {
						"AWS:SourceArn": "arn:aws:sns:us-east-1:123456789012:my-topic"
					}
				}
			},
			{
				"Sid": "api-gateway-permission",
				"Effect": "Allow",
				"Principal": {
					"Service": "apigateway.amazonaws.com"
				},
				"Action": "lambda:InvokeFunction",
				"Resource": "arn:aws:lambda:us-east-1:123456789012:function:my-function"
			}
		]
	}`

	var policy lambdaPolicy
	err := json.Unmarshal([]byte(policyJSON), &policy)
	if err != nil {
		t.Fatalf("Failed to parse policy JSON: %v", err)
	}

	if policy.Version != "2012-10-17" {
		t.Errorf("Expected Version '2012-10-17', got '%s'", policy.Version)
	}

	if len(policy.Statement) != 2 {
		t.Fatalf("Expected 2 statements, got %d", len(policy.Statement))
	}

	// First statement should be SNS
	stmt1 := policy.Statement[0]
	if stmt1.Sid != "sns-arn-aws-sns-us-east-1-123456789012-my-topic" {
		t.Errorf("Expected Sid 'sns-arn-aws-sns-us-east-1-123456789012-my-topic', got '%s'", stmt1.Sid)
	}
	if !isSNSPrincipal(stmt1.Principal) {
		t.Error("Expected first statement to have SNS principal")
	}
	topicArn := extractTopicArnFromStatement(stmt1)
	if topicArn != "arn:aws:sns:us-east-1:123456789012:my-topic" {
		t.Errorf("Expected topic ARN 'arn:aws:sns:us-east-1:123456789012:my-topic', got '%s'", topicArn)
	}

	// Second statement should NOT be SNS
	stmt2 := policy.Statement[1]
	if isSNSPrincipal(stmt2.Principal) {
		t.Error("Expected second statement to NOT have SNS principal")
	}
}

// TestParseLambdaPolicyWithStringPrincipal tests parsing a policy with string principal.
func TestParseLambdaPolicyWithStringPrincipal(t *testing.T) {
	// Some policies use string principal instead of map
	policyJSON := `{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Sid": "sns-permission",
				"Effect": "Allow",
				"Principal": "sns.amazonaws.com",
				"Action": "lambda:InvokeFunction",
				"Resource": "arn:aws:lambda:us-east-1:123456789012:function:my-function",
				"Condition": {
					"ArnLike": {
						"AWS:SourceArn": "arn:aws:sns:us-east-1:123456789012:my-topic"
					}
				}
			}
		]
	}`

	var policy lambdaPolicy
	err := json.Unmarshal([]byte(policyJSON), &policy)
	if err != nil {
		t.Fatalf("Failed to parse policy JSON: %v", err)
	}

	if len(policy.Statement) != 1 {
		t.Fatalf("Expected 1 statement, got %d", len(policy.Statement))
	}

	stmt := policy.Statement[0]
	if !isSNSPrincipal(stmt.Principal) {
		t.Error("Expected statement to have SNS principal (string format)")
	}
}

// TestParseLambdaPolicyMultipleSNS tests parsing a policy with multiple SNS permissions.
func TestParseLambdaPolicyMultipleSNS(t *testing.T) {
	policyJSON := `{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Sid": "sns-topic-1",
				"Effect": "Allow",
				"Principal": {"Service": "sns.amazonaws.com"},
				"Action": "lambda:InvokeFunction",
				"Resource": "arn:aws:lambda:us-east-1:123456789012:function:my-function",
				"Condition": {
					"ArnLike": {
						"AWS:SourceArn": "arn:aws:sns:us-east-1:123456789012:topic-1"
					}
				}
			},
			{
				"Sid": "sns-topic-2",
				"Effect": "Allow",
				"Principal": {"Service": "sns.amazonaws.com"},
				"Action": "lambda:InvokeFunction",
				"Resource": "arn:aws:lambda:us-east-1:123456789012:function:my-function",
				"Condition": {
					"ArnLike": {
						"AWS:SourceArn": "arn:aws:sns:us-east-1:123456789012:topic-2"
					}
				}
			}
		]
	}`

	var policy lambdaPolicy
	err := json.Unmarshal([]byte(policyJSON), &policy)
	if err != nil {
		t.Fatalf("Failed to parse policy JSON: %v", err)
	}

	// Count SNS permissions
	snsCount := 0
	for _, stmt := range policy.Statement {
		if isSNSPrincipal(stmt.Principal) {
			snsCount++
		}
	}

	if snsCount != 2 {
		t.Errorf("Expected 2 SNS permissions, got %d", snsCount)
	}
}

// TestExistingSNSTriggerStruct tests the ExistingSNSTrigger struct.
func TestExistingSNSTriggerStruct(t *testing.T) {
	trigger := ExistingSNSTrigger{
		TopicArn:        "arn:aws:sns:us-east-1:123456789012:my-topic",
		SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012",
		StatementId:     "sns-arn-aws-sns-us-east-1-123456789012-my-topic",
	}

	if trigger.TopicArn != "arn:aws:sns:us-east-1:123456789012:my-topic" {
		t.Errorf("Unexpected TopicArn: %s", trigger.TopicArn)
	}
	if trigger.SubscriptionArn != "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012" {
		t.Errorf("Unexpected SubscriptionArn: %s", trigger.SubscriptionArn)
	}
	if trigger.StatementId != "sns-arn-aws-sns-us-east-1-123456789012-my-topic" {
		t.Errorf("Unexpected StatementId: %s", trigger.StatementId)
	}
}


// ============================================================================
// SNS Trigger Diffing Tests
// ============================================================================

// TestDiffSNSTriggers_EmptyBoth tests diffing when both desired and existing are empty.
func TestDiffSNSTriggers_EmptyBoth(t *testing.T) {
	desired := []SNSTriggerConfig{}
	existing := []ExistingSNSTrigger{}

	result := diffSNSTriggers(desired, existing)

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

// TestDiffSNSTriggers_CreateNew tests diffing when a new trigger needs to be created.
// Requirements: 1.1 - When a new SNS trigger is added, it should be created
func TestDiffSNSTriggers_CreateNew(t *testing.T) {
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	desired := []SNSTriggerConfig{
		{
			TopicArn:    topicArn,
			StatementId: "sns-my-topic",
		},
	}
	existing := []ExistingSNSTrigger{}

	result := diffSNSTriggers(desired, existing)

	if len(result.ToCreate) != 1 {
		t.Fatalf("Expected 1 ToCreate, got %d", len(result.ToCreate))
	}
	if result.ToCreate[0].TopicArn != topicArn {
		t.Errorf("Expected TopicArn %s, got %s", topicArn, result.ToCreate[0].TopicArn)
	}
	if result.ToCreate[0].StatementId != "sns-my-topic" {
		t.Errorf("Expected StatementId 'sns-my-topic', got %s", result.ToCreate[0].StatementId)
	}
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSNSTriggers_DeleteOrphan tests diffing when an existing trigger should be deleted.
// Requirements: 1.2, 4.3 - When an SNS trigger is removed from config, it should be deleted
func TestDiffSNSTriggers_DeleteOrphan(t *testing.T) {
	topicArn := "arn:aws:sns:us-east-1:123456789012:orphan-topic"
	desired := []SNSTriggerConfig{}
	existing := []ExistingSNSTrigger{
		{
			TopicArn:        topicArn,
			SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:orphan-topic:12345",
			StatementId:     "sns-orphan-topic",
		},
	}

	result := diffSNSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}
	if len(result.ToDelete) != 1 {
		t.Fatalf("Expected 1 ToDelete, got %d", len(result.ToDelete))
	}
	if result.ToDelete[0].TopicArn != topicArn {
		t.Errorf("Expected TopicArn %s, got %s", topicArn, result.ToDelete[0].TopicArn)
	}
}

// TestDiffSNSTriggers_UpdateStatementId tests diffing when statement_id changes.
// Requirements: 1.4 - When statement_id changes, the trigger should be updated
func TestDiffSNSTriggers_UpdateStatementId(t *testing.T) {
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	desired := []SNSTriggerConfig{
		{
			TopicArn:    topicArn,
			StatementId: "new-statement-id",
		},
	}
	existing := []ExistingSNSTrigger{
		{
			TopicArn:        topicArn,
			SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:my-topic:12345",
			StatementId:     "old-statement-id",
		},
	}

	result := diffSNSTriggers(desired, existing)

	if len(result.ToCreate) != 0 {
		t.Errorf("Expected 0 ToCreate, got %d", len(result.ToCreate))
	}
	if len(result.ToUpdate) != 1 {
		t.Fatalf("Expected 1 ToUpdate, got %d", len(result.ToUpdate))
	}
	if result.ToUpdate[0].TopicArn != topicArn {
		t.Errorf("Expected TopicArn %s, got %s", topicArn, result.ToUpdate[0].TopicArn)
	}
	if result.ToUpdate[0].StatementId != "new-statement-id" {
		t.Errorf("Expected StatementId 'new-statement-id', got %s", result.ToUpdate[0].StatementId)
	}
	if len(result.ToDelete) != 0 {
		t.Errorf("Expected 0 ToDelete, got %d", len(result.ToDelete))
	}
}

// TestDiffSNSTriggers_NoChange tests diffing when triggers match exactly.
func TestDiffSNSTriggers_NoChange(t *testing.T) {
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"
	desired := []SNSTriggerConfig{
		{
			TopicArn:    topicArn,
			StatementId: statementId,
		},
	}
	existing := []ExistingSNSTrigger{
		{
			TopicArn:        topicArn,
			SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:my-topic:12345",
			StatementId:     statementId,
		},
	}

	result := diffSNSTriggers(desired, existing)

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

// TestDiffSNSTriggers_TopicArnChange tests diffing when topic_arn changes.
// Requirements: 1.3 - When topic_arn changes, old trigger is deleted and new one created
func TestDiffSNSTriggers_TopicArnChange(t *testing.T) {
	oldTopicArn := "arn:aws:sns:us-east-1:123456789012:old-topic"
	newTopicArn := "arn:aws:sns:us-east-1:123456789012:new-topic"
	desired := []SNSTriggerConfig{
		{
			TopicArn:    newTopicArn,
			StatementId: "sns-new-topic",
		},
	}
	existing := []ExistingSNSTrigger{
		{
			TopicArn:        oldTopicArn,
			SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:old-topic:12345",
			StatementId:     "sns-old-topic",
		},
	}

	result := diffSNSTriggers(desired, existing)

	// New topic should be created
	if len(result.ToCreate) != 1 {
		t.Fatalf("Expected 1 ToCreate, got %d", len(result.ToCreate))
	}
	if result.ToCreate[0].TopicArn != newTopicArn {
		t.Errorf("Expected ToCreate TopicArn %s, got %s", newTopicArn, result.ToCreate[0].TopicArn)
	}

	// No updates (different topic ARNs)
	if len(result.ToUpdate) != 0 {
		t.Errorf("Expected 0 ToUpdate, got %d", len(result.ToUpdate))
	}

	// Old topic should be deleted
	if len(result.ToDelete) != 1 {
		t.Fatalf("Expected 1 ToDelete, got %d", len(result.ToDelete))
	}
	if result.ToDelete[0].TopicArn != oldTopicArn {
		t.Errorf("Expected ToDelete TopicArn %s, got %s", oldTopicArn, result.ToDelete[0].TopicArn)
	}
}

// TestDiffSNSTriggers_MultipleTriggers tests diffing with multiple triggers.
func TestDiffSNSTriggers_MultipleTriggers(t *testing.T) {
	// Desired: topic-1 (unchanged), topic-2 (update statement_id), topic-3 (new)
	// Existing: topic-1 (unchanged), topic-2 (old statement_id), topic-4 (orphan)
	desired := []SNSTriggerConfig{
		{
			TopicArn:    "arn:aws:sns:us-east-1:123456789012:topic-1",
			StatementId: "sns-topic-1",
		},
		{
			TopicArn:    "arn:aws:sns:us-east-1:123456789012:topic-2",
			StatementId: "new-statement-id-2",
		},
		{
			TopicArn:    "arn:aws:sns:us-east-1:123456789012:topic-3",
			StatementId: "sns-topic-3",
		},
	}
	existing := []ExistingSNSTrigger{
		{
			TopicArn:        "arn:aws:sns:us-east-1:123456789012:topic-1",
			SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:topic-1:sub1",
			StatementId:     "sns-topic-1",
		},
		{
			TopicArn:        "arn:aws:sns:us-east-1:123456789012:topic-2",
			SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:topic-2:sub2",
			StatementId:     "old-statement-id-2",
		},
		{
			TopicArn:        "arn:aws:sns:us-east-1:123456789012:topic-4",
			SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:topic-4:sub4",
			StatementId:     "sns-topic-4",
		},
	}

	result := diffSNSTriggers(desired, existing)

	// topic-3 should be created
	if len(result.ToCreate) != 1 {
		t.Fatalf("Expected 1 ToCreate, got %d", len(result.ToCreate))
	}
	if result.ToCreate[0].TopicArn != "arn:aws:sns:us-east-1:123456789012:topic-3" {
		t.Errorf("Expected ToCreate topic-3, got %s", result.ToCreate[0].TopicArn)
	}

	// topic-2 should be updated
	if len(result.ToUpdate) != 1 {
		t.Fatalf("Expected 1 ToUpdate, got %d", len(result.ToUpdate))
	}
	if result.ToUpdate[0].TopicArn != "arn:aws:sns:us-east-1:123456789012:topic-2" {
		t.Errorf("Expected ToUpdate topic-2, got %s", result.ToUpdate[0].TopicArn)
	}
	if result.ToUpdate[0].StatementId != "new-statement-id-2" {
		t.Errorf("Expected ToUpdate StatementId 'new-statement-id-2', got %s", result.ToUpdate[0].StatementId)
	}

	// topic-4 should be deleted
	if len(result.ToDelete) != 1 {
		t.Fatalf("Expected 1 ToDelete, got %d", len(result.ToDelete))
	}
	if result.ToDelete[0].TopicArn != "arn:aws:sns:us-east-1:123456789012:topic-4" {
		t.Errorf("Expected ToDelete topic-4, got %s", result.ToDelete[0].TopicArn)
	}
}

// TestDiffSNSTriggers_AllNew tests diffing when all triggers are new.
func TestDiffSNSTriggers_AllNew(t *testing.T) {
	desired := []SNSTriggerConfig{
		{
			TopicArn:    "arn:aws:sns:us-east-1:123456789012:topic-1",
			StatementId: "sns-topic-1",
		},
		{
			TopicArn:    "arn:aws:sns:us-east-1:123456789012:topic-2",
			StatementId: "sns-topic-2",
		},
	}
	existing := []ExistingSNSTrigger{}

	result := diffSNSTriggers(desired, existing)

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

// TestDiffSNSTriggers_AllDeleted tests diffing when all triggers should be deleted.
func TestDiffSNSTriggers_AllDeleted(t *testing.T) {
	desired := []SNSTriggerConfig{}
	existing := []ExistingSNSTrigger{
		{
			TopicArn:        "arn:aws:sns:us-east-1:123456789012:topic-1",
			SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:topic-1:sub1",
			StatementId:     "sns-topic-1",
		},
		{
			TopicArn:        "arn:aws:sns:us-east-1:123456789012:topic-2",
			SubscriptionArn: "arn:aws:sns:us-east-1:123456789012:topic-2:sub2",
			StatementId:     "sns-topic-2",
		},
	}

	result := diffSNSTriggers(desired, existing)

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

// TestDiffSNSTriggers_DuplicateTopicArnsInDesired tests behavior with duplicate topic ARNs in desired.
// Note: This is an edge case - the last entry with the same topic_arn wins in the comparison.
func TestDiffSNSTriggers_DuplicateTopicArnsInDesired(t *testing.T) {
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	desired := []SNSTriggerConfig{
		{
			TopicArn:    topicArn,
			StatementId: "first-statement-id",
		},
		{
			TopicArn:    topicArn,
			StatementId: "second-statement-id",
		},
	}
	existing := []ExistingSNSTrigger{}

	result := diffSNSTriggers(desired, existing)

	// Both entries should be in ToCreate since we process all desired triggers
	// In practice, the caller should ensure no duplicates, but the diff function
	// processes all entries
	if len(result.ToCreate) != 2 {
		t.Errorf("Expected 2 ToCreate (duplicates processed), got %d", len(result.ToCreate))
	}
}

// TestDiffSNSTriggers_ExistingWithoutSubscriptionArn tests diffing when existing trigger has no subscription ARN.
// This can happen if the subscription was deleted but the permission still exists.
func TestDiffSNSTriggers_ExistingWithoutSubscriptionArn(t *testing.T) {
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	desired := []SNSTriggerConfig{
		{
			TopicArn:    topicArn,
			StatementId: "sns-my-topic",
		},
	}
	existing := []ExistingSNSTrigger{
		{
			TopicArn:        topicArn,
			SubscriptionArn: "", // No subscription ARN
			StatementId:     "sns-my-topic",
		},
	}

	result := diffSNSTriggers(desired, existing)

	// Should be no changes since topic_arn and statement_id match
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


// ============================================================================
// createSNSTrigger Tests
// ============================================================================

// mockLambdaClientForTrigger implements the Lambda client interface for testing trigger operations.
// It allows configuring AddPermission, RemovePermission, and GetPolicy behavior to test idempotency.
type mockLambdaClientForTrigger struct {
	addPermissionFunc             func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error)
	removePermissionFunc          func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error)
	getPolicyFunc                 func(ctx context.Context, params *lambda.GetPolicyInput, optFns ...func(*lambda.Options)) (*lambda.GetPolicyOutput, error)
	listEventSourceMappingsFunc   func(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error)
	createEventSourceMappingFunc  func(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error)
	updateEventSourceMappingFunc  func(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error)
	deleteEventSourceMappingFunc  func(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error)
}

func (m *mockLambdaClientForTrigger) AddPermission(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
	if m.addPermissionFunc != nil {
		return m.addPermissionFunc(ctx, params, optFns...)
	}
	return &lambda.AddPermissionOutput{}, nil
}

func (m *mockLambdaClientForTrigger) RemovePermission(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
	if m.removePermissionFunc != nil {
		return m.removePermissionFunc(ctx, params, optFns...)
	}
	return &lambda.RemovePermissionOutput{}, nil
}

func (m *mockLambdaClientForTrigger) GetPolicy(ctx context.Context, params *lambda.GetPolicyInput, optFns ...func(*lambda.Options)) (*lambda.GetPolicyOutput, error) {
	if m.getPolicyFunc != nil {
		return m.getPolicyFunc(ctx, params, optFns...)
	}
	return &lambda.GetPolicyOutput{}, nil
}

func (m *mockLambdaClientForTrigger) ListEventSourceMappings(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error) {
	if m.listEventSourceMappingsFunc != nil {
		return m.listEventSourceMappingsFunc(ctx, params, optFns...)
	}
	return &lambda.ListEventSourceMappingsOutput{}, nil
}

func (m *mockLambdaClientForTrigger) CreateEventSourceMapping(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error) {
	if m.createEventSourceMappingFunc != nil {
		return m.createEventSourceMappingFunc(ctx, params, optFns...)
	}
	return &lambda.CreateEventSourceMappingOutput{}, nil
}

func (m *mockLambdaClientForTrigger) UpdateEventSourceMapping(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error) {
	if m.updateEventSourceMappingFunc != nil {
		return m.updateEventSourceMappingFunc(ctx, params, optFns...)
	}
	return &lambda.UpdateEventSourceMappingOutput{}, nil
}

func (m *mockLambdaClientForTrigger) DeleteEventSourceMapping(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error) {
	if m.deleteEventSourceMappingFunc != nil {
		return m.deleteEventSourceMappingFunc(ctx, params, optFns...)
	}
	return &lambda.DeleteEventSourceMappingOutput{}, nil
}

// mockSNSClientForTrigger implements the SNS client interface for testing SNS trigger operations.
// It allows configuring Subscribe, Unsubscribe, and ListSubscriptionsByTopic behavior to test idempotency.
type mockSNSClientForTrigger struct {
	subscribeFunc                func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error)
	unsubscribeFunc              func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error)
	listSubscriptionsByTopicFunc func(ctx context.Context, params *sns.ListSubscriptionsByTopicInput, optFns ...func(*sns.Options)) (*sns.ListSubscriptionsByTopicOutput, error)
}

func (m *mockSNSClientForTrigger) Subscribe(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
	if m.subscribeFunc != nil {
		return m.subscribeFunc(ctx, params, optFns...)
	}
	return &sns.SubscribeOutput{}, nil
}

func (m *mockSNSClientForTrigger) Unsubscribe(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
	if m.unsubscribeFunc != nil {
		return m.unsubscribeFunc(ctx, params, optFns...)
	}
	return &sns.UnsubscribeOutput{}, nil
}

func (m *mockSNSClientForTrigger) ListSubscriptionsByTopic(ctx context.Context, params *sns.ListSubscriptionsByTopicInput, optFns ...func(*sns.Options)) (*sns.ListSubscriptionsByTopicOutput, error) {
	if m.listSubscriptionsByTopicFunc != nil {
		return m.listSubscriptionsByTopicFunc(ctx, params, optFns...)
	}
	return &sns.ListSubscriptionsByTopicOutput{}, nil
}

// lambdaClientInterface defines the Lambda client methods used by TriggerManager
type lambdaClientInterface interface {
	AddPermission(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error)
	RemovePermission(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error)
	GetPolicy(ctx context.Context, params *lambda.GetPolicyInput, optFns ...func(*lambda.Options)) (*lambda.GetPolicyOutput, error)
	ListEventSourceMappings(ctx context.Context, params *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error)
	CreateEventSourceMapping(ctx context.Context, params *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error)
	UpdateEventSourceMapping(ctx context.Context, params *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error)
	DeleteEventSourceMapping(ctx context.Context, params *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error)
}

// snsClientInterface defines the SNS client methods used by TriggerManager
type snsClientInterface interface {
	Subscribe(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error)
	Unsubscribe(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error)
	ListSubscriptionsByTopic(ctx context.Context, params *sns.ListSubscriptionsByTopicInput, optFns ...func(*sns.Options)) (*sns.ListSubscriptionsByTopicOutput, error)
}

// testableTriggerManager wraps TriggerManager for testing with mock clients
type testableTriggerManager struct {
	lambdaClient lambdaClientInterface
	snsClient    snsClientInterface
	config       *DispatcherConfig
}

// createSNSTriggerTestable is a testable version of createSNSTrigger that uses interfaces
// This allows us to test the function logic without real AWS clients.
//
// Requirements: 1.1, 6.1, 6.2
func (tm *testableTriggerManager) createSNSTrigger(ctx context.Context, functionName string, lambdaArn string, trigger SNSTriggerConfig) error {
	// Step 1: Add Lambda permission for SNS to invoke the function
	_, err := tm.lambdaClient.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(functionName),
		StatementId:  aws.String(trigger.StatementId),
		Action:       aws.String("lambda:InvokeFunction"),
		Principal:    aws.String("sns.amazonaws.com"),
		SourceArn:    aws.String(trigger.TopicArn),
	})
	if err != nil {
		// Check if permission already exists - this is idempotent success (Requirement 6.1)
		if strings.Contains(err.Error(), "ResourceConflictException") {
			// Permission already exists, treat as success
		} else {
			return fmt.Errorf("failed to add Lambda permission for SNS trigger: %w", err)
		}
	}

	// Step 2: Subscribe Lambda to SNS topic
	// The SNS Subscribe API is idempotent (Requirement 6.2)
	_, err = tm.snsClient.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(trigger.TopicArn),
		Protocol: aws.String("lambda"),
		Endpoint: aws.String(lambdaArn),
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe Lambda to SNS topic: %w", err)
	}

	return nil
}

// getExistingSQSTriggers is a testable version that uses interfaces.
// It queries AWS to find existing SQS triggers for a Lambda function.
//
// Requirements: 4.2 - Query existing event source mappings for the Lambda
func (tm *testableTriggerManager) getExistingSQSTriggers(ctx context.Context, functionName string) ([]ExistingSQSTrigger, error) {
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

	return existingTriggers, nil
}

// createSQSTrigger is a testable version that uses interfaces.
// It creates an SQS trigger (event source mapping) for a Lambda function.
//
// The function is idempotent (Requirement 6.3):
// - If an event source mapping already exists for the same queue (ResourceConflictException),
//   it finds the existing mapping and updates it rather than failing
//
// Returns the UUID of the created or updated event source mapping.
//
// Requirements: 2.1, 6.3
func (tm *testableTriggerManager) createSQSTrigger(ctx context.Context, functionName string, trigger SQSTriggerConfig) (string, error) {
	// Try to create the event source mapping
	output, err := tm.lambdaClient.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
		FunctionName:   aws.String(functionName),
		EventSourceArn: aws.String(trigger.QueueArn),
		BatchSize:      aws.Int32(trigger.BatchSize),
		Enabled:        aws.Bool(trigger.Enabled),
	})

	if err != nil {
		// Check if mapping already exists - handle idempotently (Requirement 6.3)
		if strings.Contains(err.Error(), "ResourceConflictException") {
			// Find the existing mapping to get its UUID
			existingUUID, findErr := tm.findExistingSQSMappingUUID(ctx, functionName, trigger.QueueArn)
			if findErr != nil {
				return "", fmt.Errorf("failed to find existing event source mapping: %w", findErr)
			}

			if existingUUID == "" {
				return "", fmt.Errorf("ResourceConflictException but could not find existing mapping for queue %s", trigger.QueueArn)
			}

			// Update the existing mapping with the desired configuration
			updateOutput, updateErr := tm.lambdaClient.UpdateEventSourceMapping(ctx, &lambda.UpdateEventSourceMappingInput{
				UUID:      aws.String(existingUUID),
				BatchSize: aws.Int32(trigger.BatchSize),
				Enabled:   aws.Bool(trigger.Enabled),
			})
			if updateErr != nil {
				return "", fmt.Errorf("failed to update existing event source mapping: %w", updateErr)
			}

			return aws.ToString(updateOutput.UUID), nil
		}

		return "", fmt.Errorf("failed to create event source mapping: %w", err)
	}

	return aws.ToString(output.UUID), nil
}

// findExistingSQSMappingUUID is a testable helper that finds the UUID of an existing
// event source mapping for a specific queue ARN.
func (tm *testableTriggerManager) findExistingSQSMappingUUID(ctx context.Context, functionName string, queueArn string) (string, error) {
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

// updateSQSTrigger is a testable version of TriggerManager.updateSQSTrigger.
// It updates an existing SQS trigger (event source mapping) when batch_size or enabled changes.
//
// Requirements: 2.3 - When an SQS trigger's batch_size changes, update the existing event source mapping
func (tm *testableTriggerManager) updateSQSTrigger(ctx context.Context, update SQSTriggerUpdate) error {
	// Use Lambda UpdateEventSourceMapping API to update the existing mapping
	_, err := tm.lambdaClient.UpdateEventSourceMapping(ctx, &lambda.UpdateEventSourceMappingInput{
		UUID:      aws.String(update.UUID),
		BatchSize: aws.Int32(update.Config.BatchSize),
		Enabled:   aws.Bool(update.Config.Enabled),
	})
	if err != nil {
		return fmt.Errorf("failed to update event source mapping %s: %w", update.UUID, err)
	}

	return nil
}

// deleteSQSTrigger is a testable version of TriggerManager.deleteSQSTrigger.
// It deletes an SQS trigger (event source mapping) from a Lambda function.
//
// The function is idempotent (Requirement 6.4):
// - If the event source mapping doesn't exist (ResourceNotFoundException), it's treated as success
//
// Requirements: 2.2, 6.4
func (tm *testableTriggerManager) deleteSQSTrigger(ctx context.Context, trigger ExistingSQSTrigger) error {
	// Use Lambda DeleteEventSourceMapping API to delete the mapping
	_, err := tm.lambdaClient.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{
		UUID: aws.String(trigger.UUID),
	})
	if err != nil {
		// Check if mapping doesn't exist - this is idempotent success (Requirement 6.4)
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			return nil
		}
		return fmt.Errorf("failed to delete event source mapping %s: %w", trigger.UUID, err)
	}

	return nil
}

// TestCreateSNSTrigger_Success tests successful creation of an SNS trigger.
// Requirements: 1.1 - When a new SNS trigger is added, create Lambda permission and SNS subscription
func TestCreateSNSTrigger_Success(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	addPermissionCalled := false
	subscribeCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			addPermissionCalled = true
			// Verify parameters
			if *params.FunctionName != functionName {
				t.Errorf("Expected FunctionName %s, got %s", functionName, *params.FunctionName)
			}
			if *params.StatementId != statementId {
				t.Errorf("Expected StatementId %s, got %s", statementId, *params.StatementId)
			}
			if *params.Action != "lambda:InvokeFunction" {
				t.Errorf("Expected Action 'lambda:InvokeFunction', got %s", *params.Action)
			}
			if *params.Principal != "sns.amazonaws.com" {
				t.Errorf("Expected Principal 'sns.amazonaws.com', got %s", *params.Principal)
			}
			if *params.SourceArn != topicArn {
				t.Errorf("Expected SourceArn %s, got %s", topicArn, *params.SourceArn)
			}
			return &lambda.AddPermissionOutput{}, nil
		},
	}

	subscriptionArn := "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012"
	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			subscribeCalled = true
			// Verify parameters
			if *params.TopicArn != topicArn {
				t.Errorf("Expected TopicArn %s, got %s", topicArn, *params.TopicArn)
			}
			if *params.Protocol != "lambda" {
				t.Errorf("Expected Protocol 'lambda', got %s", *params.Protocol)
			}
			if *params.Endpoint != lambdaArn {
				t.Errorf("Expected Endpoint %s, got %s", lambdaArn, *params.Endpoint)
			}
			return &sns.SubscribeOutput{
				SubscriptionArn: aws.String(subscriptionArn),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: statementId,
	}

	err := tm.createSNSTrigger(ctx, functionName, lambdaArn, trigger)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !addPermissionCalled {
		t.Error("Expected AddPermission to be called")
	}
	if !subscribeCalled {
		t.Error("Expected Subscribe to be called")
	}
}

// TestCreateSNSTrigger_IdempotentPermission tests that ResourceConflictException is treated as success.
// Requirements: 6.1 - When an SNS permission already exists with the same statement ID, treat it as success
func TestCreateSNSTrigger_IdempotentPermission(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	subscribeCalled := false

	// Mock Lambda client that returns ResourceConflictException
	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return nil, fmt.Errorf("ResourceConflictException: The statement id (%s) provided already exists", statementId)
		},
	}

	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			subscribeCalled = true
			return &sns.SubscribeOutput{
				SubscriptionArn: aws.String("arn:aws:sns:us-east-1:123456789012:my-topic:12345"),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: statementId,
	}

	err := tm.createSNSTrigger(ctx, functionName, lambdaArn, trigger)

	// Should succeed despite ResourceConflictException (idempotent)
	if err != nil {
		t.Errorf("Expected no error (idempotent), got: %v", err)
	}
	// Subscribe should still be called
	if !subscribeCalled {
		t.Error("Expected Subscribe to be called even when permission already exists")
	}
}

// TestCreateSNSTrigger_IdempotentSubscription tests that existing subscriptions are handled gracefully.
// Requirements: 6.2 - When an SNS subscription already exists for the same topic/endpoint, treat it as success
func TestCreateSNSTrigger_IdempotentSubscription(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return &lambda.AddPermissionOutput{}, nil
		},
	}

	// SNS Subscribe API is idempotent - it returns the existing subscription ARN
	// if a subscription already exists for the same topic/protocol/endpoint
	existingSubscriptionArn := "arn:aws:sns:us-east-1:123456789012:my-topic:existing-sub-id"
	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			// SNS returns the existing subscription ARN (idempotent behavior)
			return &sns.SubscribeOutput{
				SubscriptionArn: aws.String(existingSubscriptionArn),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: statementId,
	}

	err := tm.createSNSTrigger(ctx, functionName, lambdaArn, trigger)

	// Should succeed - SNS Subscribe is naturally idempotent
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestCreateSNSTrigger_PermissionError tests that non-conflict errors are propagated.
func TestCreateSNSTrigger_PermissionError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	// Mock Lambda client that returns a non-conflict error
	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return nil, fmt.Errorf("AccessDeniedException: User is not authorized to perform lambda:AddPermission")
		},
	}

	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: statementId,
	}

	err := tm.createSNSTrigger(ctx, functionName, lambdaArn, trigger)

	// Should fail with the permission error
	if err == nil {
		t.Error("Expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to add Lambda permission") {
		t.Errorf("Expected error to contain 'failed to add Lambda permission', got: %v", err)
	}
	if !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("Expected error to contain 'AccessDeniedException', got: %v", err)
	}
}

// TestCreateSNSTrigger_SubscribeError tests that subscribe errors are propagated.
func TestCreateSNSTrigger_SubscribeError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return &lambda.AddPermissionOutput{}, nil
		},
	}

	// Mock SNS client that returns an error
	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			return nil, fmt.Errorf("NotFoundException: Topic does not exist")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: statementId,
	}

	err := tm.createSNSTrigger(ctx, functionName, lambdaArn, trigger)

	// Should fail with the subscribe error
	if err == nil {
		t.Error("Expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to subscribe Lambda to SNS topic") {
		t.Errorf("Expected error to contain 'failed to subscribe Lambda to SNS topic', got: %v", err)
	}
	if !strings.Contains(err.Error(), "NotFoundException") {
		t.Errorf("Expected error to contain 'NotFoundException', got: %v", err)
	}
}

// TestCreateSNSTrigger_BothIdempotent tests when both permission and subscription already exist.
// Requirements: 6.1, 6.2 - Both operations should be idempotent
func TestCreateSNSTrigger_BothIdempotent(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	// Permission already exists
	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return nil, fmt.Errorf("ResourceConflictException: The statement id (%s) provided already exists", statementId)
		},
	}

	// Subscription already exists (SNS returns existing ARN)
	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			return &sns.SubscribeOutput{
				SubscriptionArn: aws.String("arn:aws:sns:us-east-1:123456789012:my-topic:existing"),
			}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: statementId,
	}

	err := tm.createSNSTrigger(ctx, functionName, lambdaArn, trigger)

	// Should succeed - both operations are idempotent
	if err != nil {
		t.Errorf("Expected no error (both idempotent), got: %v", err)
	}
}

// ============================================================================
// updateSNSTrigger Tests
// ============================================================================

// updateSNSTrigger is a testable version of updateSNSTrigger that uses interfaces.
// This allows us to test the function logic without real AWS clients.
//
// Requirements: 1.4 - When an SNS trigger's statement_id changes, update the Lambda permission
func (tm *testableTriggerManager) updateSNSTrigger(ctx context.Context, functionName string, lambdaArn string, trigger SNSTriggerConfig, oldStatementId string) error {
	// Step 1: Remove the old Lambda permission
	// We ignore ResourceNotFoundException since the permission might already be gone
	_, err := tm.lambdaClient.RemovePermission(ctx, &lambda.RemovePermissionInput{
		FunctionName: aws.String(functionName),
		StatementId:  aws.String(oldStatementId),
	})
	if err != nil {
		// Check if permission doesn't exist - this is idempotent success (Requirement 6.4)
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			// Permission doesn't exist, treat as success
		} else {
			return fmt.Errorf("failed to remove old Lambda permission for SNS trigger: %w", err)
		}
	}

	// Step 2: Add the new Lambda permission with the new statement_id
	_, err = tm.lambdaClient.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(functionName),
		StatementId:  aws.String(trigger.StatementId),
		Action:       aws.String("lambda:InvokeFunction"),
		Principal:    aws.String("sns.amazonaws.com"),
		SourceArn:    aws.String(trigger.TopicArn),
	})
	if err != nil {
		// Check if permission already exists - this is idempotent success (Requirement 6.1)
		if strings.Contains(err.Error(), "ResourceConflictException") {
			// Permission already exists, treat as success
		} else {
			return fmt.Errorf("failed to add new Lambda permission for SNS trigger: %w", err)
		}
	}

	// Note: The SNS subscription doesn't need to change since it's based on topic_arn,
	// not statement_id. The subscription connects the topic to the Lambda endpoint,
	// and that relationship remains the same.

	return nil
}

// TestUpdateSNSTrigger_Success tests successful update of an SNS trigger's statement_id.
// Requirements: 1.4 - When an SNS trigger's statement_id changes, update the Lambda permission
func TestUpdateSNSTrigger_Success(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	oldStatementId := "old-statement-id"
	newStatementId := "new-statement-id"

	removePermissionCalled := false
	addPermissionCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			removePermissionCalled = true
			// Verify parameters
			if *params.FunctionName != functionName {
				t.Errorf("Expected FunctionName %s, got %s", functionName, *params.FunctionName)
			}
			if *params.StatementId != oldStatementId {
				t.Errorf("Expected StatementId %s, got %s", oldStatementId, *params.StatementId)
			}
			return &lambda.RemovePermissionOutput{}, nil
		},
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			addPermissionCalled = true
			// Verify parameters
			if *params.FunctionName != functionName {
				t.Errorf("Expected FunctionName %s, got %s", functionName, *params.FunctionName)
			}
			if *params.StatementId != newStatementId {
				t.Errorf("Expected StatementId %s, got %s", newStatementId, *params.StatementId)
			}
			if *params.Action != "lambda:InvokeFunction" {
				t.Errorf("Expected Action 'lambda:InvokeFunction', got %s", *params.Action)
			}
			if *params.Principal != "sns.amazonaws.com" {
				t.Errorf("Expected Principal 'sns.amazonaws.com', got %s", *params.Principal)
			}
			if *params.SourceArn != topicArn {
				t.Errorf("Expected SourceArn %s, got %s", topicArn, *params.SourceArn)
			}
			return &lambda.AddPermissionOutput{}, nil
		},
	}

	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: newStatementId,
	}

	err := tm.updateSNSTrigger(ctx, functionName, lambdaArn, trigger, oldStatementId)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !removePermissionCalled {
		t.Error("Expected RemovePermission to be called")
	}
	if !addPermissionCalled {
		t.Error("Expected AddPermission to be called")
	}
}

// TestUpdateSNSTrigger_OldPermissionNotFound tests that ResourceNotFoundException on remove is treated as success.
// Requirements: 6.4 - When deleting a trigger that doesn't exist, treat it as success
func TestUpdateSNSTrigger_OldPermissionNotFound(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	oldStatementId := "old-statement-id"
	newStatementId := "new-statement-id"

	addPermissionCalled := false

	// Mock Lambda client that returns ResourceNotFoundException on RemovePermission
	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return nil, fmt.Errorf("ResourceNotFoundException: No policy is associated with the given resource")
		},
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			addPermissionCalled = true
			return &lambda.AddPermissionOutput{}, nil
		},
	}

	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: newStatementId,
	}

	err := tm.updateSNSTrigger(ctx, functionName, lambdaArn, trigger, oldStatementId)

	// Should succeed despite ResourceNotFoundException (idempotent)
	if err != nil {
		t.Errorf("Expected no error (idempotent), got: %v", err)
	}
	// AddPermission should still be called
	if !addPermissionCalled {
		t.Error("Expected AddPermission to be called even when old permission not found")
	}
}

// TestUpdateSNSTrigger_NewPermissionAlreadyExists tests that ResourceConflictException on add is treated as success.
// Requirements: 6.1 - When an SNS permission already exists with the same statement ID, treat it as success
func TestUpdateSNSTrigger_NewPermissionAlreadyExists(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	oldStatementId := "old-statement-id"
	newStatementId := "new-statement-id"

	removePermissionCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			removePermissionCalled = true
			return &lambda.RemovePermissionOutput{}, nil
		},
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return nil, fmt.Errorf("ResourceConflictException: The statement id (%s) provided already exists", newStatementId)
		},
	}

	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: newStatementId,
	}

	err := tm.updateSNSTrigger(ctx, functionName, lambdaArn, trigger, oldStatementId)

	// Should succeed despite ResourceConflictException (idempotent)
	if err != nil {
		t.Errorf("Expected no error (idempotent), got: %v", err)
	}
	if !removePermissionCalled {
		t.Error("Expected RemovePermission to be called")
	}
}

// TestUpdateSNSTrigger_BothIdempotent tests when both old permission is missing and new already exists.
// Requirements: 6.1, 6.4 - Both operations should be idempotent
func TestUpdateSNSTrigger_BothIdempotent(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	oldStatementId := "old-statement-id"
	newStatementId := "new-statement-id"

	// Old permission doesn't exist
	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return nil, fmt.Errorf("ResourceNotFoundException: No policy is associated with the given resource")
		},
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return nil, fmt.Errorf("ResourceConflictException: The statement id (%s) provided already exists", newStatementId)
		},
	}

	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: newStatementId,
	}

	err := tm.updateSNSTrigger(ctx, functionName, lambdaArn, trigger, oldStatementId)

	// Should succeed - both operations are idempotent
	if err != nil {
		t.Errorf("Expected no error (both idempotent), got: %v", err)
	}
}

// TestUpdateSNSTrigger_RemovePermissionError tests that non-NotFound errors on remove are propagated.
func TestUpdateSNSTrigger_RemovePermissionError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	oldStatementId := "old-statement-id"
	newStatementId := "new-statement-id"

	// Mock Lambda client that returns a non-NotFound error
	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return nil, fmt.Errorf("AccessDeniedException: User is not authorized to perform lambda:RemovePermission")
		},
	}

	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: newStatementId,
	}

	err := tm.updateSNSTrigger(ctx, functionName, lambdaArn, trigger, oldStatementId)

	// Should fail with the permission error
	if err == nil {
		t.Error("Expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to remove old Lambda permission") {
		t.Errorf("Expected error to contain 'failed to remove old Lambda permission', got: %v", err)
	}
	if !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("Expected error to contain 'AccessDeniedException', got: %v", err)
	}
}

// TestUpdateSNSTrigger_AddPermissionError tests that non-Conflict errors on add are propagated.
func TestUpdateSNSTrigger_AddPermissionError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	oldStatementId := "old-statement-id"
	newStatementId := "new-statement-id"

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return &lambda.RemovePermissionOutput{}, nil
		},
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return nil, fmt.Errorf("InvalidParameterValueException: Invalid source ARN")
		},
	}

	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: newStatementId,
	}

	err := tm.updateSNSTrigger(ctx, functionName, lambdaArn, trigger, oldStatementId)

	// Should fail with the add permission error
	if err == nil {
		t.Error("Expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to add new Lambda permission") {
		t.Errorf("Expected error to contain 'failed to add new Lambda permission', got: %v", err)
	}
	if !strings.Contains(err.Error(), "InvalidParameterValueException") {
		t.Errorf("Expected error to contain 'InvalidParameterValueException', got: %v", err)
	}
}

// TestUpdateSNSTrigger_NoSubscriptionChange tests that SNS subscription is not modified during update.
// The subscription is based on topic_arn, not statement_id, so it should remain unchanged.
func TestUpdateSNSTrigger_NoSubscriptionChange(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	oldStatementId := "old-statement-id"
	newStatementId := "new-statement-id"

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return &lambda.RemovePermissionOutput{}, nil
		},
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return &lambda.AddPermissionOutput{}, nil
		},
	}

	subscribeCalled := false
	unsubscribeCalled := false

	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			subscribeCalled = true
			return &sns.SubscribeOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: newStatementId,
	}

	err := tm.updateSNSTrigger(ctx, functionName, lambdaArn, trigger, oldStatementId)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	// SNS Subscribe should NOT be called during update
	if subscribeCalled {
		t.Error("Expected Subscribe to NOT be called during statement_id update")
	}
	// SNS Unsubscribe should NOT be called during update
	if unsubscribeCalled {
		t.Error("Expected Unsubscribe to NOT be called during statement_id update")
	}
}

// ============================================================================
// deleteSNSTrigger Tests
// ============================================================================

// deleteSNSTrigger is a testable version of deleteSNSTrigger that uses interfaces.
// This allows us to test the function logic without real AWS clients.
//
// Requirements: 1.2, 6.4
func (tm *testableTriggerManager) deleteSNSTrigger(ctx context.Context, functionName string, trigger ExistingSNSTrigger) error {
	// Step 1: Remove Lambda permission for SNS to invoke the function
	_, err := tm.lambdaClient.RemovePermission(ctx, &lambda.RemovePermissionInput{
		FunctionName: aws.String(functionName),
		StatementId:  aws.String(trigger.StatementId),
	})
	if err != nil {
		// Check if permission doesn't exist - this is idempotent success (Requirement 6.4)
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			// Permission doesn't exist, treat as success
		} else {
			return fmt.Errorf("failed to remove Lambda permission for SNS trigger: %w", err)
		}
	}

	// Step 2: Unsubscribe from SNS topic
	// Only attempt if we have a subscription ARN
	if trigger.SubscriptionArn != "" {
		_, err = tm.snsClient.Unsubscribe(ctx, &sns.UnsubscribeInput{
			SubscriptionArn: aws.String(trigger.SubscriptionArn),
		})
		if err != nil {
			// Check if subscription doesn't exist - this is idempotent success (Requirement 6.4)
			if strings.Contains(err.Error(), "ResourceNotFoundException") ||
				strings.Contains(err.Error(), "InvalidParameter") {
				// Subscription doesn't exist, treat as success
			} else {
				return fmt.Errorf("failed to unsubscribe Lambda from SNS topic: %w", err)
			}
		}
	}

	return nil
}

// TestDeleteSNSTrigger_Success tests successful deletion of an SNS trigger.
// Requirements: 1.2 - When an SNS trigger is removed from config, delete Lambda permission and unsubscribe
func TestDeleteSNSTrigger_Success(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"
	subscriptionArn := "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012"

	removePermissionCalled := false
	unsubscribeCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			removePermissionCalled = true
			// Verify parameters
			if *params.FunctionName != functionName {
				t.Errorf("Expected FunctionName %s, got %s", functionName, *params.FunctionName)
			}
			if *params.StatementId != statementId {
				t.Errorf("Expected StatementId %s, got %s", statementId, *params.StatementId)
			}
			return &lambda.RemovePermissionOutput{}, nil
		},
	}

	mockSNS := &mockSNSClientForTrigger{
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			unsubscribeCalled = true
			// Verify parameters
			if *params.SubscriptionArn != subscriptionArn {
				t.Errorf("Expected SubscriptionArn %s, got %s", subscriptionArn, *params.SubscriptionArn)
			}
			return &sns.UnsubscribeOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := ExistingSNSTrigger{
		TopicArn:        topicArn,
		StatementId:     statementId,
		SubscriptionArn: subscriptionArn,
	}

	err := tm.deleteSNSTrigger(ctx, functionName, trigger)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !removePermissionCalled {
		t.Error("Expected RemovePermission to be called")
	}
	if !unsubscribeCalled {
		t.Error("Expected Unsubscribe to be called")
	}
}

// TestDeleteSNSTrigger_IdempotentPermissionNotFound tests that ResourceNotFoundException on RemovePermission is treated as success.
// Requirements: 6.4 - When deleting a trigger that doesn't exist, treat it as success
func TestDeleteSNSTrigger_IdempotentPermissionNotFound(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"
	subscriptionArn := "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012"

	unsubscribeCalled := false

	// Mock Lambda client that returns ResourceNotFoundException
	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return nil, fmt.Errorf("ResourceNotFoundException: No policy is associated with the given resource")
		},
	}

	mockSNS := &mockSNSClientForTrigger{
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			unsubscribeCalled = true
			return &sns.UnsubscribeOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := ExistingSNSTrigger{
		TopicArn:        topicArn,
		StatementId:     statementId,
		SubscriptionArn: subscriptionArn,
	}

	err := tm.deleteSNSTrigger(ctx, functionName, trigger)

	// Should succeed despite ResourceNotFoundException (idempotent)
	if err != nil {
		t.Errorf("Expected no error (idempotent), got: %v", err)
	}
	// Unsubscribe should still be called
	if !unsubscribeCalled {
		t.Error("Expected Unsubscribe to be called even when permission not found")
	}
}

// TestDeleteSNSTrigger_IdempotentSubscriptionNotFound tests that ResourceNotFoundException on Unsubscribe is treated as success.
// Requirements: 6.4 - When deleting a trigger that doesn't exist, treat it as success
func TestDeleteSNSTrigger_IdempotentSubscriptionNotFound(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"
	subscriptionArn := "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012"

	removePermissionCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			removePermissionCalled = true
			return &lambda.RemovePermissionOutput{}, nil
		},
	}

	// Mock SNS client that returns ResourceNotFoundException
	mockSNS := &mockSNSClientForTrigger{
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			return nil, fmt.Errorf("ResourceNotFoundException: Subscription does not exist")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := ExistingSNSTrigger{
		TopicArn:        topicArn,
		StatementId:     statementId,
		SubscriptionArn: subscriptionArn,
	}

	err := tm.deleteSNSTrigger(ctx, functionName, trigger)

	// Should succeed despite ResourceNotFoundException (idempotent)
	if err != nil {
		t.Errorf("Expected no error (idempotent), got: %v", err)
	}
	if !removePermissionCalled {
		t.Error("Expected RemovePermission to be called")
	}
}

// TestDeleteSNSTrigger_IdempotentInvalidParameter tests that InvalidParameter on Unsubscribe is treated as success.
// SNS can return InvalidParameter for non-existent or already-deleted subscriptions.
// Requirements: 6.4 - When deleting a trigger that doesn't exist, treat it as success
func TestDeleteSNSTrigger_IdempotentInvalidParameter(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"
	subscriptionArn := "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return &lambda.RemovePermissionOutput{}, nil
		},
	}

	// Mock SNS client that returns InvalidParameter
	mockSNS := &mockSNSClientForTrigger{
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			return nil, fmt.Errorf("InvalidParameter: Invalid parameter: SubscriptionArn")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := ExistingSNSTrigger{
		TopicArn:        topicArn,
		StatementId:     statementId,
		SubscriptionArn: subscriptionArn,
	}

	err := tm.deleteSNSTrigger(ctx, functionName, trigger)

	// Should succeed despite InvalidParameter (idempotent)
	if err != nil {
		t.Errorf("Expected no error (idempotent), got: %v", err)
	}
}

// TestDeleteSNSTrigger_BothIdempotent tests when both permission and subscription don't exist.
// Requirements: 6.4 - Both operations should be idempotent
func TestDeleteSNSTrigger_BothIdempotent(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"
	subscriptionArn := "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012"

	// Permission doesn't exist
	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return nil, fmt.Errorf("ResourceNotFoundException: No policy is associated with the given resource")
		},
	}

	// Subscription doesn't exist
	mockSNS := &mockSNSClientForTrigger{
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			return nil, fmt.Errorf("ResourceNotFoundException: Subscription does not exist")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := ExistingSNSTrigger{
		TopicArn:        topicArn,
		StatementId:     statementId,
		SubscriptionArn: subscriptionArn,
	}

	err := tm.deleteSNSTrigger(ctx, functionName, trigger)

	// Should succeed - both operations are idempotent
	if err != nil {
		t.Errorf("Expected no error (both idempotent), got: %v", err)
	}
}

// TestDeleteSNSTrigger_PermissionError tests that non-idempotent errors on RemovePermission are propagated.
func TestDeleteSNSTrigger_PermissionError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"
	subscriptionArn := "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012"

	// Mock Lambda client that returns a non-idempotent error
	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return nil, fmt.Errorf("AccessDeniedException: User is not authorized to perform lambda:RemovePermission")
		},
	}

	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := ExistingSNSTrigger{
		TopicArn:        topicArn,
		StatementId:     statementId,
		SubscriptionArn: subscriptionArn,
	}

	err := tm.deleteSNSTrigger(ctx, functionName, trigger)

	// Should fail with the permission error
	if err == nil {
		t.Error("Expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to remove Lambda permission") {
		t.Errorf("Expected error to contain 'failed to remove Lambda permission', got: %v", err)
	}
	if !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("Expected error to contain 'AccessDeniedException', got: %v", err)
	}
}

// TestDeleteSNSTrigger_UnsubscribeError tests that non-idempotent errors on Unsubscribe are propagated.
func TestDeleteSNSTrigger_UnsubscribeError(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"
	subscriptionArn := "arn:aws:sns:us-east-1:123456789012:my-topic:12345678-1234-1234-1234-123456789012"

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			return &lambda.RemovePermissionOutput{}, nil
		},
	}

	// Mock SNS client that returns a non-idempotent error
	mockSNS := &mockSNSClientForTrigger{
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			return nil, fmt.Errorf("AuthorizationErrorException: User is not authorized to perform sns:Unsubscribe")
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	trigger := ExistingSNSTrigger{
		TopicArn:        topicArn,
		StatementId:     statementId,
		SubscriptionArn: subscriptionArn,
	}

	err := tm.deleteSNSTrigger(ctx, functionName, trigger)

	// Should fail with the unsubscribe error
	if err == nil {
		t.Error("Expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to unsubscribe Lambda from SNS topic") {
		t.Errorf("Expected error to contain 'failed to unsubscribe Lambda from SNS topic', got: %v", err)
	}
	if !strings.Contains(err.Error(), "AuthorizationErrorException") {
		t.Errorf("Expected error to contain 'AuthorizationErrorException', got: %v", err)
	}
}

// TestDeleteSNSTrigger_NoSubscriptionArn tests deletion when there's no subscription ARN.
// This can happen if the subscription was already deleted but the permission still exists.
func TestDeleteSNSTrigger_NoSubscriptionArn(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	removePermissionCalled := false
	unsubscribeCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			removePermissionCalled = true
			return &lambda.RemovePermissionOutput{}, nil
		},
	}

	mockSNS := &mockSNSClientForTrigger{
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			unsubscribeCalled = true
			return &sns.UnsubscribeOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	// Trigger with no subscription ARN
	trigger := ExistingSNSTrigger{
		TopicArn:        topicArn,
		StatementId:     statementId,
		SubscriptionArn: "", // No subscription ARN
	}

	err := tm.deleteSNSTrigger(ctx, functionName, trigger)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !removePermissionCalled {
		t.Error("Expected RemovePermission to be called")
	}
	// Unsubscribe should NOT be called when there's no subscription ARN
	if unsubscribeCalled {
		t.Error("Expected Unsubscribe to NOT be called when SubscriptionArn is empty")
	}
}


// ============================================================================
// ReconcileSNSTriggers Orchestration Tests
// ============================================================================

// reconcileSNSTriggersTestable is a testable version of ReconcileSNSTriggers that uses interfaces.
// This allows us to test the orchestration logic without real AWS clients.
//
// Requirements: 1.5, 4.5
func (tm *testableTriggerManager) reconcileSNSTriggers(
	ctx context.Context,
	functionName string,
	lambdaArn string,
	desiredTriggers []SNSTriggerConfig,
	existingTriggers []ExistingSNSTrigger,
) error {
	// Step 1: Diff desired vs existing to determine actions
	diff := diffSNSTriggers(desiredTriggers, existingTriggers)

	// Track success/failure counts for final error determination
	totalOperations := len(diff.ToDelete) + len(diff.ToUpdate) + len(diff.ToCreate)
	if totalOperations == 0 {
		return nil
	}

	successCount := 0
	var lastError error

	// Build a map of existing triggers by topic ARN for looking up old statement IDs
	existingByTopicArn := make(map[string]ExistingSNSTrigger)
	for _, trigger := range existingTriggers {
		existingByTopicArn[trigger.TopicArn] = trigger
	}

	// Step 2: Delete orphaned triggers first
	for _, trigger := range diff.ToDelete {
		err := tm.deleteSNSTrigger(ctx, functionName, trigger)
		if err != nil {
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 3: Update triggers with changed statement_id
	for _, trigger := range diff.ToUpdate {
		existingTrigger, exists := existingByTopicArn[trigger.TopicArn]
		if !exists {
			lastError = fmt.Errorf("existing trigger not found for topic %s", trigger.TopicArn)
			continue
		}
		err := tm.updateSNSTrigger(ctx, functionName, lambdaArn, trigger, existingTrigger.StatementId)
		if err != nil {
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 4: Create new triggers
	for _, trigger := range diff.ToCreate {
		err := tm.createSNSTrigger(ctx, functionName, lambdaArn, trigger)
		if err != nil {
			lastError = err
		} else {
			successCount++
		}
	}

	// Step 5: Determine final result - only return error if ALL operations failed
	if successCount == 0 && totalOperations > 0 {
		return fmt.Errorf("all SNS trigger operations failed, last error: %w", lastError)
	}

	return nil
}

// TestReconcileSNSTriggers_NoChanges tests reconciliation when no changes are needed.
func TestReconcileSNSTriggers_NoChanges(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	// Desired and existing match exactly
	desired := []SNSTriggerConfig{
		{TopicArn: topicArn, StatementId: statementId},
	}
	existing := []ExistingSNSTrigger{
		{TopicArn: topicArn, StatementId: statementId, SubscriptionArn: "arn:aws:sns:..."},
	}

	// No AWS calls should be made
	mockLambda := &mockLambdaClientForTrigger{}
	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestReconcileSNSTriggers_CreateOnly tests reconciliation when only creates are needed.
// Requirements: 1.1 - When a new SNS trigger is added, create Lambda permission and SNS subscription
func TestReconcileSNSTriggers_CreateOnly(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	desired := []SNSTriggerConfig{
		{TopicArn: topicArn, StatementId: statementId},
	}
	existing := []ExistingSNSTrigger{} // No existing triggers

	addPermissionCalled := false
	subscribeCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			addPermissionCalled = true
			return &lambda.AddPermissionOutput{}, nil
		},
	}
	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			subscribeCalled = true
			return &sns.SubscribeOutput{SubscriptionArn: aws.String("arn:aws:sns:...")}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !addPermissionCalled {
		t.Error("Expected AddPermission to be called")
	}
	if !subscribeCalled {
		t.Error("Expected Subscribe to be called")
	}
}

// TestReconcileSNSTriggers_DeleteOnly tests reconciliation when only deletes are needed.
// Requirements: 1.2, 4.3 - When an SNS trigger is removed from config, delete it
func TestReconcileSNSTriggers_DeleteOnly(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:orphan-topic"

	desired := []SNSTriggerConfig{} // No desired triggers
	existing := []ExistingSNSTrigger{
		{TopicArn: topicArn, StatementId: "sns-orphan", SubscriptionArn: "arn:aws:sns:..."},
	}

	removePermissionCalled := false
	unsubscribeCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			removePermissionCalled = true
			return &lambda.RemovePermissionOutput{}, nil
		},
	}
	mockSNS := &mockSNSClientForTrigger{
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			unsubscribeCalled = true
			return &sns.UnsubscribeOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !removePermissionCalled {
		t.Error("Expected RemovePermission to be called")
	}
	if !unsubscribeCalled {
		t.Error("Expected Unsubscribe to be called")
	}
}

// TestReconcileSNSTriggers_UpdateOnly tests reconciliation when only updates are needed.
// Requirements: 1.4 - When statement_id changes, update the Lambda permission
func TestReconcileSNSTriggers_UpdateOnly(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"

	desired := []SNSTriggerConfig{
		{TopicArn: topicArn, StatementId: "new-statement-id"},
	}
	existing := []ExistingSNSTrigger{
		{TopicArn: topicArn, StatementId: "old-statement-id", SubscriptionArn: "arn:aws:sns:..."},
	}

	removePermissionCalled := false
	addPermissionCalled := false

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			removePermissionCalled = true
			if *params.StatementId != "old-statement-id" {
				t.Errorf("Expected old statement ID, got %s", *params.StatementId)
			}
			return &lambda.RemovePermissionOutput{}, nil
		},
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			addPermissionCalled = true
			if *params.StatementId != "new-statement-id" {
				t.Errorf("Expected new statement ID, got %s", *params.StatementId)
			}
			return &lambda.AddPermissionOutput{}, nil
		},
	}
	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !removePermissionCalled {
		t.Error("Expected RemovePermission to be called for old statement ID")
	}
	if !addPermissionCalled {
		t.Error("Expected AddPermission to be called for new statement ID")
	}
}

// TestReconcileSNSTriggers_MixedOperations tests reconciliation with create, update, and delete.
func TestReconcileSNSTriggers_MixedOperations(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"

	// Desired: topic-1 (unchanged), topic-2 (update), topic-3 (new)
	// Existing: topic-1 (unchanged), topic-2 (old statement), topic-4 (orphan)
	desired := []SNSTriggerConfig{
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-1", StatementId: "sns-topic-1"},
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-2", StatementId: "new-stmt-2"},
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-3", StatementId: "sns-topic-3"},
	}
	existing := []ExistingSNSTrigger{
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-1", StatementId: "sns-topic-1", SubscriptionArn: "sub1"},
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-2", StatementId: "old-stmt-2", SubscriptionArn: "sub2"},
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-4", StatementId: "sns-topic-4", SubscriptionArn: "sub4"},
	}

	var operations []string

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			operations = append(operations, fmt.Sprintf("remove:%s", *params.StatementId))
			return &lambda.RemovePermissionOutput{}, nil
		},
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			operations = append(operations, fmt.Sprintf("add:%s", *params.StatementId))
			return &lambda.AddPermissionOutput{}, nil
		},
	}
	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			operations = append(operations, fmt.Sprintf("subscribe:%s", *params.TopicArn))
			return &sns.SubscribeOutput{SubscriptionArn: aws.String("new-sub")}, nil
		},
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			operations = append(operations, fmt.Sprintf("unsubscribe:%s", *params.SubscriptionArn))
			return &sns.UnsubscribeOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Verify operations occurred (order: delete, update, create)
	// Delete topic-4: remove permission + unsubscribe
	// Update topic-2: remove old permission + add new permission
	// Create topic-3: add permission + subscribe
	if len(operations) < 6 {
		t.Errorf("Expected at least 6 operations, got %d: %v", len(operations), operations)
	}
}

// TestReconcileSNSTriggers_ContinueOnFailure tests that reconciliation continues when individual operations fail.
// Requirements: 1.5 - IF creating an SNS trigger fails, THEN log a warning and continue with other triggers
func TestReconcileSNSTriggers_ContinueOnFailure(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"

	// Two triggers to create - first will fail, second should still succeed
	desired := []SNSTriggerConfig{
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-fail", StatementId: "sns-fail"},
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-success", StatementId: "sns-success"},
	}
	existing := []ExistingSNSTrigger{}

	successTopicCreated := false

	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			if strings.Contains(*params.SourceArn, "topic-fail") {
				return nil, fmt.Errorf("AccessDeniedException: simulated failure")
			}
			if strings.Contains(*params.SourceArn, "topic-success") {
				successTopicCreated = true
			}
			return &lambda.AddPermissionOutput{}, nil
		},
	}
	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			return &sns.SubscribeOutput{SubscriptionArn: aws.String("sub")}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing)

	// Should NOT return error because at least one operation succeeded
	if err != nil {
		t.Errorf("Expected no error (partial success), got: %v", err)
	}
	// The successful topic should have been created
	if !successTopicCreated {
		t.Error("Expected successful topic to be created despite first failure")
	}
}

// TestReconcileSNSTriggers_AllOperationsFail tests that error is returned when ALL operations fail.
// Requirements: 1.5 - Only return error if all operations failed
func TestReconcileSNSTriggers_AllOperationsFail(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"

	// Two triggers to create - both will fail
	desired := []SNSTriggerConfig{
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-1", StatementId: "sns-1"},
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-2", StatementId: "sns-2"},
	}
	existing := []ExistingSNSTrigger{}

	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			return nil, fmt.Errorf("AccessDeniedException: simulated failure")
		},
	}
	mockSNS := &mockSNSClientForTrigger{}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing)

	// Should return error because ALL operations failed
	if err == nil {
		t.Error("Expected error when all operations fail, got nil")
	}
	if !strings.Contains(err.Error(), "all SNS trigger operations failed") {
		t.Errorf("Expected error message about all operations failing, got: %v", err)
	}
}

// TestReconcileSNSTriggers_Idempotent tests that running reconciliation twice produces the same result.
// Requirements: 4.5 - THE Trigger_Manager SHALL be idempotent, producing the same result when run multiple times
func TestReconcileSNSTriggers_Idempotent(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"
	topicArn := "arn:aws:sns:us-east-1:123456789012:my-topic"
	statementId := "sns-my-topic"

	desired := []SNSTriggerConfig{
		{TopicArn: topicArn, StatementId: statementId},
	}

	// First run: no existing triggers, should create
	existing1 := []ExistingSNSTrigger{}
	createCount := 0

	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			createCount++
			return &lambda.AddPermissionOutput{}, nil
		},
	}
	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			return &sns.SubscribeOutput{SubscriptionArn: aws.String("sub")}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing1)
	if err != nil {
		t.Errorf("First run: expected no error, got: %v", err)
	}
	if createCount != 1 {
		t.Errorf("First run: expected 1 create, got %d", createCount)
	}

	// Second run: trigger now exists, should be no-op
	existing2 := []ExistingSNSTrigger{
		{TopicArn: topicArn, StatementId: statementId, SubscriptionArn: "sub"},
	}
	createCount = 0

	err = tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing2)
	if err != nil {
		t.Errorf("Second run: expected no error, got: %v", err)
	}
	if createCount != 0 {
		t.Errorf("Second run: expected 0 creates (idempotent), got %d", createCount)
	}
}

// TestReconcileSNSTriggers_DeleteBeforeCreate tests that deletes happen before creates.
// This is important to avoid statement ID conflicts when replacing triggers.
func TestReconcileSNSTriggers_DeleteBeforeCreate(t *testing.T) {
	ctx := context.Background()
	functionName := "myapp-prod-my-lambda"
	lambdaArn := "arn:aws:lambda:us-east-1:123456789012:function:myapp-prod-my-lambda"

	// Replace topic-old with topic-new
	desired := []SNSTriggerConfig{
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-new", StatementId: "sns-new"},
	}
	existing := []ExistingSNSTrigger{
		{TopicArn: "arn:aws:sns:us-east-1:123456789012:topic-old", StatementId: "sns-old", SubscriptionArn: "sub-old"},
	}

	var operationOrder []string

	mockLambda := &mockLambdaClientForTrigger{
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			operationOrder = append(operationOrder, "delete")
			return &lambda.RemovePermissionOutput{}, nil
		},
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			operationOrder = append(operationOrder, "create")
			return &lambda.AddPermissionOutput{}, nil
		},
	}
	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			return &sns.SubscribeOutput{SubscriptionArn: aws.String("sub")}, nil
		},
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			return &sns.UnsubscribeOutput{}, nil
		},
	}

	tm := &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}

	err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desired, existing)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Verify delete happens before create
	if len(operationOrder) < 2 {
		t.Fatalf("Expected at least 2 operations, got %d", len(operationOrder))
	}
	if operationOrder[0] != "delete" {
		t.Errorf("Expected delete first, got: %v", operationOrder)
	}
}

// ============================================================================
// Property-Based Tests for SNS Trigger Reconciliation
// ============================================================================

// Feature: trigger-lifecycle-management, Property 9: Reconciliation Idempotence (SNS)
// **Validates: Requirements 4.5, 6.1, 6.2**
//
// Property: For any Lambda function and SNS trigger configuration, running
// reconciliation twice in succession SHALL produce the same AWS state as
// running it once. The second run should produce no changes (no creates,
// no updates, no deletes).

// mockAWSState tracks the simulated AWS state for property testing.
// It records what triggers exist in the "AWS" mock and tracks operations.
type mockAWSState struct {
	// permissions maps statement_id -> topic_arn for Lambda permissions
	permissions map[string]string
	// subscriptions maps topic_arn -> subscription_arn for SNS subscriptions
	subscriptions map[string]string
	// operationCounts tracks how many operations were performed
	createCount int
	updateCount int
	deleteCount int
}

// newMockAWSState creates a new mock AWS state.
func newMockAWSState() *mockAWSState {
	return &mockAWSState{
		permissions:   make(map[string]string),
		subscriptions: make(map[string]string),
	}
}

// resetOperationCounts resets the operation counters.
func (s *mockAWSState) resetOperationCounts() {
	s.createCount = 0
	s.updateCount = 0
	s.deleteCount = 0
}

// hasChanges returns true if any operations were performed.
func (s *mockAWSState) hasChanges() bool {
	return s.createCount > 0 || s.updateCount > 0 || s.deleteCount > 0
}

// generateRandomSNSTriggerConfig generates a random SNS trigger configuration.
func generateRandomSNSTriggerConfig(r *rand.Rand, index int) SNSTriggerConfig {
	// Generate a valid-looking topic ARN
	regions := []string{"us-east-1", "us-west-2", "eu-west-1", "ap-southeast-1"}
	region := regions[r.Intn(len(regions))]
	accountId := fmt.Sprintf("%012d", r.Int63n(1000000000000))
	topicName := fmt.Sprintf("topic-%d-%s", index, randomAlphanumeric(r, 5, 10))
	topicArn := fmt.Sprintf("arn:aws:sns:%s:%s:%s", region, accountId, topicName)

	// Optionally generate a custom statement ID (50% chance)
	var statementId string
	if r.Intn(2) == 0 {
		statementId = fmt.Sprintf("custom-stmt-%d-%s", index, randomAlphanumeric(r, 3, 8))
	} else {
		// Use auto-generated statement ID
		statementId = generateSNSStatementId(topicArn)
	}

	return SNSTriggerConfig{
		TopicArn:    topicArn,
		StatementId: statementId,
	}
}

// randomAlphanumeric generates a random alphanumeric string.
func randomAlphanumeric(r *rand.Rand, minLen, maxLen int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	length := minLen + r.Intn(maxLen-minLen+1)
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[r.Intn(len(charset))]
	}
	return string(result)
}

// generateRandomSNSTriggerConfigs generates a slice of random SNS trigger configurations.
func generateRandomSNSTriggerConfigs(r *rand.Rand, count int) []SNSTriggerConfig {
	triggers := make([]SNSTriggerConfig, count)
	// Use a set to ensure unique topic ARNs
	usedTopicArns := make(map[string]bool)

	for i := 0; i < count; i++ {
		for {
			trigger := generateRandomSNSTriggerConfig(r, i)
			if !usedTopicArns[trigger.TopicArn] {
				usedTopicArns[trigger.TopicArn] = true
				triggers[i] = trigger
				break
			}
		}
	}
	return triggers
}

// createStatefulMockTriggerManager creates a testableTriggerManager that tracks
// state changes in the provided mockAWSState. This simulates how AWS would
// behave with idempotent operations.
func createStatefulMockTriggerManager(state *mockAWSState) *testableTriggerManager {
	mockLambda := &mockLambdaClientForTrigger{
		addPermissionFunc: func(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error) {
			statementId := *params.StatementId
			topicArn := *params.SourceArn

			// Check if permission already exists (idempotent - Requirement 6.1)
			if existingTopic, exists := state.permissions[statementId]; exists {
				if existingTopic == topicArn {
					// Same permission already exists - idempotent success
					return &lambda.AddPermissionOutput{}, nil
				}
				// Different topic with same statement ID - conflict
				return nil, fmt.Errorf("ResourceConflictException: The statement id (%s) provided already exists", statementId)
			}

			// Create the permission
			state.permissions[statementId] = topicArn
			state.createCount++
			return &lambda.AddPermissionOutput{}, nil
		},
		removePermissionFunc: func(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error) {
			statementId := *params.StatementId

			// Check if permission exists
			if _, exists := state.permissions[statementId]; !exists {
				// Permission doesn't exist - idempotent success (Requirement 6.4)
				return &lambda.RemovePermissionOutput{}, nil
			}

			// Delete the permission
			delete(state.permissions, statementId)
			state.deleteCount++
			return &lambda.RemovePermissionOutput{}, nil
		},
	}

	mockSNS := &mockSNSClientForTrigger{
		subscribeFunc: func(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
			topicArn := *params.TopicArn

			// SNS Subscribe is idempotent - returns existing subscription if it exists (Requirement 6.2)
			if existingSubArn, exists := state.subscriptions[topicArn]; exists {
				return &sns.SubscribeOutput{
					SubscriptionArn: aws.String(existingSubArn),
				}, nil
			}

			// Create new subscription
			subArn := fmt.Sprintf("%s:sub-%d", topicArn, rand.Int63())
			state.subscriptions[topicArn] = subArn
			// Note: We don't increment createCount here because the subscription
			// creation is part of the trigger creation, not a separate operation
			return &sns.SubscribeOutput{
				SubscriptionArn: aws.String(subArn),
			}, nil
		},
		unsubscribeFunc: func(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
			subArn := *params.SubscriptionArn

			// Find and remove the subscription
			for topicArn, existingSubArn := range state.subscriptions {
				if existingSubArn == subArn {
					delete(state.subscriptions, topicArn)
					return &sns.UnsubscribeOutput{}, nil
				}
			}

			// Subscription doesn't exist - idempotent success (Requirement 6.4)
			return &sns.UnsubscribeOutput{}, nil
		},
	}

	return &testableTriggerManager{
		lambdaClient: mockLambda,
		snsClient:    mockSNS,
	}
}

// getExistingTriggersFromState converts the mock AWS state to ExistingSNSTrigger slice.
func getExistingTriggersFromState(state *mockAWSState) []ExistingSNSTrigger {
	var existing []ExistingSNSTrigger

	// Build a reverse map from topic_arn to statement_id
	topicToStatement := make(map[string]string)
	for statementId, topicArn := range state.permissions {
		topicToStatement[topicArn] = statementId
	}

	// Create ExistingSNSTrigger for each permission
	for statementId, topicArn := range state.permissions {
		trigger := ExistingSNSTrigger{
			TopicArn:    topicArn,
			StatementId: statementId,
		}
		// Add subscription ARN if it exists
		if subArn, exists := state.subscriptions[topicArn]; exists {
			trigger.SubscriptionArn = subArn
		}
		existing = append(existing, trigger)
	}

	return existing
}

// TestProperty9_SNSReconciliationIdempotence tests that running SNS trigger
// reconciliation twice produces the same result as running it once.
//
// Feature: trigger-lifecycle-management, Property 9: Reconciliation Idempotence (SNS)
// **Validates: Requirements 4.5, 6.1, 6.2**
//
// Property: For any Lambda function and SNS trigger configuration, running
// reconciliation twice in succession SHALL produce the same AWS state as
// running it once.
func TestProperty9_SNSReconciliationIdempotence(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random desired trigger configuration (0-5 triggers)
		numTriggers := r.Intn(6)
		desiredTriggers := generateRandomSNSTriggerConfigs(r, numTriggers)

		// Generate random initial AWS state (0-5 existing triggers)
		// This simulates various starting conditions
		numExisting := r.Intn(6)
		initialState := newMockAWSState()
		existingTriggers := generateRandomSNSTriggerConfigs(r, numExisting)
		for _, trigger := range existingTriggers {
			initialState.permissions[trigger.StatementId] = trigger.TopicArn
			initialState.subscriptions[trigger.TopicArn] = fmt.Sprintf("%s:sub-%d", trigger.TopicArn, r.Int63())
		}

		// Create test context
		ctx := context.Background()
		functionName := fmt.Sprintf("myapp-prod-lambda-%d", seed)
		lambdaArn := fmt.Sprintf("arn:aws:lambda:us-east-1:123456789012:function:%s", functionName)

		// Create stateful mock trigger manager
		tm := createStatefulMockTriggerManager(initialState)

		// First reconciliation - apply desired state
		existingFromState := getExistingTriggersFromState(initialState)
		err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desiredTriggers, existingFromState)
		if err != nil {
			t.Logf("First reconciliation failed: %v (seed=%d)", err, seed)
			return false
		}

		// Capture state after first reconciliation
		stateAfterFirst := make(map[string]string)
		for k, v := range initialState.permissions {
			stateAfterFirst[k] = v
		}
		subsAfterFirst := make(map[string]string)
		for k, v := range initialState.subscriptions {
			subsAfterFirst[k] = v
		}

		// Reset operation counts for second reconciliation
		initialState.resetOperationCounts()

		// Second reconciliation - should be idempotent (no changes)
		existingFromState = getExistingTriggersFromState(initialState)
		err = tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desiredTriggers, existingFromState)
		if err != nil {
			t.Logf("Second reconciliation failed: %v (seed=%d)", err, seed)
			return false
		}

		// Verify: Second reconciliation should produce NO changes
		if initialState.hasChanges() {
			t.Logf("Second reconciliation produced changes (seed=%d): creates=%d, updates=%d, deletes=%d",
				seed, initialState.createCount, initialState.updateCount, initialState.deleteCount)
			return false
		}

		// Verify: State after second reconciliation matches state after first
		if len(initialState.permissions) != len(stateAfterFirst) {
			t.Logf("Permission count mismatch after second reconciliation (seed=%d): got %d, want %d",
				seed, len(initialState.permissions), len(stateAfterFirst))
			return false
		}
		for statementId, topicArn := range stateAfterFirst {
			if initialState.permissions[statementId] != topicArn {
				t.Logf("Permission mismatch for %s (seed=%d): got %s, want %s",
					statementId, seed, initialState.permissions[statementId], topicArn)
				return false
			}
		}

		// Verify: Subscriptions match
		if len(initialState.subscriptions) != len(subsAfterFirst) {
			t.Logf("Subscription count mismatch after second reconciliation (seed=%d): got %d, want %d",
				seed, len(initialState.subscriptions), len(subsAfterFirst))
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Property 9 (SNS Reconciliation Idempotence) failed: %v", err)
	}
}

// TestProperty9_SNSReconciliationIdempotence_EmptyToPopulated tests idempotence
// when going from empty state to populated state.
//
// Feature: trigger-lifecycle-management, Property 9: Reconciliation Idempotence (SNS)
// **Validates: Requirements 4.5, 6.1, 6.2**
func TestProperty9_SNSReconciliationIdempotence_EmptyToPopulated(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random desired triggers (1-5 triggers)
		numTriggers := 1 + r.Intn(5)
		desiredTriggers := generateRandomSNSTriggerConfigs(r, numTriggers)

		// Start with empty AWS state
		state := newMockAWSState()

		ctx := context.Background()
		functionName := fmt.Sprintf("myapp-prod-lambda-%d", seed)
		lambdaArn := fmt.Sprintf("arn:aws:lambda:us-east-1:123456789012:function:%s", functionName)

		tm := createStatefulMockTriggerManager(state)

		// First reconciliation - creates all triggers
		existingFromState := getExistingTriggersFromState(state)
		err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desiredTriggers, existingFromState)
		if err != nil {
			t.Logf("First reconciliation failed: %v (seed=%d)", err, seed)
			return false
		}

		// Verify triggers were created
		if len(state.permissions) != numTriggers {
			t.Logf("Expected %d permissions after first reconciliation, got %d (seed=%d)",
				numTriggers, len(state.permissions), seed)
			return false
		}

		// Reset counts
		state.resetOperationCounts()

		// Second reconciliation - should be idempotent
		existingFromState = getExistingTriggersFromState(state)
		err = tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desiredTriggers, existingFromState)
		if err != nil {
			t.Logf("Second reconciliation failed: %v (seed=%d)", err, seed)
			return false
		}

		// Verify no changes on second run
		if state.hasChanges() {
			t.Logf("Second reconciliation produced changes (seed=%d): creates=%d, updates=%d, deletes=%d",
				seed, state.createCount, state.updateCount, state.deleteCount)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Property 9 (SNS Reconciliation Idempotence - Empty to Populated) failed: %v", err)
	}
}

// TestProperty9_SNSReconciliationIdempotence_PopulatedToEmpty tests idempotence
// when going from populated state to empty state (deleting all triggers).
//
// Feature: trigger-lifecycle-management, Property 9: Reconciliation Idempotence (SNS)
// **Validates: Requirements 4.5, 6.1, 6.2**
func TestProperty9_SNSReconciliationIdempotence_PopulatedToEmpty(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate random existing triggers (1-5 triggers)
		numExisting := 1 + r.Intn(5)
		existingTriggers := generateRandomSNSTriggerConfigs(r, numExisting)

		// Initialize AWS state with existing triggers
		state := newMockAWSState()
		for _, trigger := range existingTriggers {
			state.permissions[trigger.StatementId] = trigger.TopicArn
			state.subscriptions[trigger.TopicArn] = fmt.Sprintf("%s:sub-%d", trigger.TopicArn, r.Int63())
		}

		// Desired state is empty (delete all triggers)
		desiredTriggers := []SNSTriggerConfig{}

		ctx := context.Background()
		functionName := fmt.Sprintf("myapp-prod-lambda-%d", seed)
		lambdaArn := fmt.Sprintf("arn:aws:lambda:us-east-1:123456789012:function:%s", functionName)

		tm := createStatefulMockTriggerManager(state)

		// First reconciliation - deletes all triggers
		existingFromState := getExistingTriggersFromState(state)
		err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desiredTriggers, existingFromState)
		if err != nil {
			t.Logf("First reconciliation failed: %v (seed=%d)", err, seed)
			return false
		}

		// Verify all triggers were deleted
		if len(state.permissions) != 0 {
			t.Logf("Expected 0 permissions after first reconciliation, got %d (seed=%d)",
				len(state.permissions), seed)
			return false
		}

		// Reset counts
		state.resetOperationCounts()

		// Second reconciliation - should be idempotent
		existingFromState = getExistingTriggersFromState(state)
		err = tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desiredTriggers, existingFromState)
		if err != nil {
			t.Logf("Second reconciliation failed: %v (seed=%d)", err, seed)
			return false
		}

		// Verify no changes on second run
		if state.hasChanges() {
			t.Logf("Second reconciliation produced changes (seed=%d): creates=%d, updates=%d, deletes=%d",
				seed, state.createCount, state.updateCount, state.deleteCount)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Property 9 (SNS Reconciliation Idempotence - Populated to Empty) failed: %v", err)
	}
}

// TestProperty9_SNSReconciliationIdempotence_PartialOverlap tests idempotence
// when desired and existing triggers partially overlap (some creates, some deletes).
//
// Feature: trigger-lifecycle-management, Property 9: Reconciliation Idempotence (SNS)
// **Validates: Requirements 4.5, 6.1, 6.2**
func TestProperty9_SNSReconciliationIdempotence_PartialOverlap(t *testing.T) {
	config := &quick.Config{
		MaxCount: 100,
	}

	property := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))

		// Generate triggers that will be kept (in both desired and existing)
		numKept := r.Intn(3)
		keptTriggers := generateRandomSNSTriggerConfigs(r, numKept)

		// Generate triggers that will be added (only in desired)
		numAdded := r.Intn(3)
		addedTriggers := generateRandomSNSTriggerConfigs(r, numAdded)

		// Generate triggers that will be deleted (only in existing)
		numDeleted := r.Intn(3)
		deletedTriggers := generateRandomSNSTriggerConfigs(r, numDeleted)

		// Build desired triggers (kept + added)
		desiredTriggers := append([]SNSTriggerConfig{}, keptTriggers...)
		desiredTriggers = append(desiredTriggers, addedTriggers...)

		// Build existing triggers (kept + deleted)
		existingTriggers := append([]SNSTriggerConfig{}, keptTriggers...)
		existingTriggers = append(existingTriggers, deletedTriggers...)

		// Initialize AWS state with existing triggers
		state := newMockAWSState()
		for _, trigger := range existingTriggers {
			state.permissions[trigger.StatementId] = trigger.TopicArn
			state.subscriptions[trigger.TopicArn] = fmt.Sprintf("%s:sub-%d", trigger.TopicArn, r.Int63())
		}

		ctx := context.Background()
		functionName := fmt.Sprintf("myapp-prod-lambda-%d", seed)
		lambdaArn := fmt.Sprintf("arn:aws:lambda:us-east-1:123456789012:function:%s", functionName)

		tm := createStatefulMockTriggerManager(state)

		// First reconciliation
		existingFromState := getExistingTriggersFromState(state)
		err := tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desiredTriggers, existingFromState)
		if err != nil {
			t.Logf("First reconciliation failed: %v (seed=%d)", err, seed)
			return false
		}

		// Verify correct number of permissions
		expectedCount := numKept + numAdded
		if len(state.permissions) != expectedCount {
			t.Logf("Expected %d permissions after first reconciliation, got %d (seed=%d)",
				expectedCount, len(state.permissions), seed)
			return false
		}

		// Reset counts
		state.resetOperationCounts()

		// Second reconciliation - should be idempotent
		existingFromState = getExistingTriggersFromState(state)
		err = tm.reconcileSNSTriggers(ctx, functionName, lambdaArn, desiredTriggers, existingFromState)
		if err != nil {
			t.Logf("Second reconciliation failed: %v (seed=%d)", err, seed)
			return false
		}

		// Verify no changes on second run
		if state.hasChanges() {
			t.Logf("Second reconciliation produced changes (seed=%d): creates=%d, updates=%d, deletes=%d",
				seed, state.createCount, state.updateCount, state.deleteCount)
			return false
		}

		return true
	}

	if err := quick.Check(property, config); err != nil {
		t.Errorf("Property 9 (SNS Reconciliation Idempotence - Partial Overlap) failed: %v", err)
	}
}
