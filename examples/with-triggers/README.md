# Triggers Example

This example demonstrates how to use SNS and SQS triggers with the `conveyor_belt` resource to create event-driven Lambda functions.

## Resources Created

- 3 Lambda functions:
  - `api` - Handles API requests and publishes to SNS
  - `order_processor` - Triggered by SNS topic
  - `background_worker` - Triggered by SQS queue
- 1 API Gateway with routes
- 1 SNS topic for order events
- 1 SQS queue for background jobs
- IAM roles and policies for each Lambda
- CloudWatch log groups

## Architecture

```
API Request → API Gateway → api Lambda → SNS Topic → order_processor Lambda
                                      ↘
                                        SQS Queue → background_worker Lambda
```

## Usage

```bash
# Initialize
terraform init

# Preview changes
terraform plan

# Apply
terraform apply
```

## Trigger Lifecycle

The conveyor-belt provider manages triggers throughout their lifecycle:

1. **Creation**: When you add `sns_triggers` or `sqs_triggers` to `lambda_config`, the provider creates the necessary Lambda permissions and event source mappings.

2. **Updates**: When you change trigger settings (e.g., `batch_size` for SQS), the provider updates the existing configuration.

3. **Deletion**: When you remove triggers from config, the provider cleans up the AWS resources.

4. **Reconciliation**: During `terraform apply`, the provider ensures AWS state matches your configuration.

## Outputs

- `api_url` - The API Gateway invoke URL
- `sns_topic_arn` - The SNS topic ARN for order events
- `sqs_queue_url` - The SQS queue URL for background jobs
