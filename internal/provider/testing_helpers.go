// internal/provider/testing_helpers.go
package provider

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccProtoV6ProviderFactories is used to instantiate a provider during
// acceptance testing. The factory function will be invoked for every Terraform
// CLI command executed to create a provider server to which the CLI can
// reattach.
var TestAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"conveyor-belt": providerserver.NewProtocol6WithError(New()),
}

// TestAccPreCheck validates the necessary test API keys exist in the testing
// environment. This function should be called in the PreCheck field of
// resource.TestCase.
func TestAccPreCheck(t resource.TestCheckFunc) {
	// Add any pre-check validations here
	// For example, checking for required environment variables
}

// MockLambdaClient defines the interface for mocking Lambda operations
type MockLambdaClient interface {
	CreateFunction(ctx context.Context, params *lambda.CreateFunctionInput, optFns ...func(*lambda.Options)) (*lambda.CreateFunctionOutput, error)
	GetFunction(ctx context.Context, params *lambda.GetFunctionInput, optFns ...func(*lambda.Options)) (*lambda.GetFunctionOutput, error)
	UpdateFunctionCode(ctx context.Context, params *lambda.UpdateFunctionCodeInput, optFns ...func(*lambda.Options)) (*lambda.UpdateFunctionCodeOutput, error)
	UpdateFunctionConfiguration(ctx context.Context, params *lambda.UpdateFunctionConfigurationInput, optFns ...func(*lambda.Options)) (*lambda.UpdateFunctionConfigurationOutput, error)
	DeleteFunction(ctx context.Context, params *lambda.DeleteFunctionInput, optFns ...func(*lambda.Options)) (*lambda.DeleteFunctionOutput, error)
	ListFunctions(ctx context.Context, params *lambda.ListFunctionsInput, optFns ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error)
	AddPermission(ctx context.Context, params *lambda.AddPermissionInput, optFns ...func(*lambda.Options)) (*lambda.AddPermissionOutput, error)
	RemovePermission(ctx context.Context, params *lambda.RemovePermissionInput, optFns ...func(*lambda.Options)) (*lambda.RemovePermissionOutput, error)
}

// MockIAMClient defines the interface for mocking IAM operations
type MockIAMClient interface {
	CreateRole(ctx context.Context, params *iam.CreateRoleInput, optFns ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	GetRole(ctx context.Context, params *iam.GetRoleInput, optFns ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	DeleteRole(ctx context.Context, params *iam.DeleteRoleInput, optFns ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
	AttachRolePolicy(ctx context.Context, params *iam.AttachRolePolicyInput, optFns ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error)
	DetachRolePolicy(ctx context.Context, params *iam.DetachRolePolicyInput, optFns ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error)
	PutRolePolicy(ctx context.Context, params *iam.PutRolePolicyInput, optFns ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	DeleteRolePolicy(ctx context.Context, params *iam.DeleteRolePolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	ListAttachedRolePolicies(ctx context.Context, params *iam.ListAttachedRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)
	ListRolePolicies(ctx context.Context, params *iam.ListRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
}

// MockAPIGatewayClient defines the interface for mocking API Gateway operations
type MockAPIGatewayClient interface {
	CreateRestApi(ctx context.Context, params *apigateway.CreateRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error)
	GetRestApi(ctx context.Context, params *apigateway.GetRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.GetRestApiOutput, error)
	DeleteRestApi(ctx context.Context, params *apigateway.DeleteRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error)
	GetRestApis(ctx context.Context, params *apigateway.GetRestApisInput, optFns ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error)
	CreateResource(ctx context.Context, params *apigateway.CreateResourceInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error)
	GetResources(ctx context.Context, params *apigateway.GetResourcesInput, optFns ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error)
	DeleteResource(ctx context.Context, params *apigateway.DeleteResourceInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteResourceOutput, error)
	PutMethod(ctx context.Context, params *apigateway.PutMethodInput, optFns ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error)
	PutIntegration(ctx context.Context, params *apigateway.PutIntegrationInput, optFns ...func(*apigateway.Options)) (*apigateway.PutIntegrationOutput, error)
	CreateDeployment(ctx context.Context, params *apigateway.CreateDeploymentInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error)
	CreateStage(ctx context.Context, params *apigateway.CreateStageInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateStageOutput, error)
}

// MockCloudWatchLogsClient defines the interface for mocking CloudWatch Logs operations
type MockCloudWatchLogsClient interface {
	CreateLogGroup(ctx context.Context, params *cloudwatchlogs.CreateLogGroupInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error)
	DeleteLogGroup(ctx context.Context, params *cloudwatchlogs.DeleteLogGroupInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error)
	DescribeLogGroups(ctx context.Context, params *cloudwatchlogs.DescribeLogGroupsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
	PutRetentionPolicy(ctx context.Context, params *cloudwatchlogs.PutRetentionPolicyInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error)
}

// MockCloudWatchClient defines the interface for mocking CloudWatch operations
type MockCloudWatchClient interface {
	PutMetricAlarm(ctx context.Context, params *cloudwatch.PutMetricAlarmInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricAlarmOutput, error)
	DeleteAlarms(ctx context.Context, params *cloudwatch.DeleteAlarmsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.DeleteAlarmsOutput, error)
	DescribeAlarms(ctx context.Context, params *cloudwatch.DescribeAlarmsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error)
}

// MockSNSClient defines the interface for mocking SNS operations
type MockSNSClient interface {
	Subscribe(ctx context.Context, params *sns.SubscribeInput, optFns ...func(*sns.Options)) (*sns.SubscribeOutput, error)
	Unsubscribe(ctx context.Context, params *sns.UnsubscribeInput, optFns ...func(*sns.Options)) (*sns.UnsubscribeOutput, error)
	ListSubscriptionsByTopic(ctx context.Context, params *sns.ListSubscriptionsByTopicInput, optFns ...func(*sns.Options)) (*sns.ListSubscriptionsByTopicOutput, error)
}

// MockSQSClient defines the interface for mocking SQS operations
type MockSQSClient interface {
	GetQueueUrl(ctx context.Context, params *sqs.GetQueueUrlInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

// MockResourceClients holds mock implementations of all AWS service clients
type MockResourceClients struct {
	Lambda         MockLambdaClient
	IAM            MockIAMClient
	APIGateway     MockAPIGatewayClient
	CloudWatchLogs MockCloudWatchLogsClient
	CloudWatch     MockCloudWatchClient
	SNS            MockSNSClient
	SQS            MockSQSClient
}
