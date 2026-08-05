# Trigger Lifecycle Management

This document describes how the Conveyor provider manages SNS and SQS triggers throughout their lifecycle.

## Overview

SNS and SQS triggers allow Lambda functions to be invoked by events rather than HTTP requests. The Conveyor provider fully manages these triggers:

- **SNS Triggers**: Lambda is invoked when a message is published to an SNS topic
- **SQS Triggers**: Lambda polls an SQS queue and processes messages in batches

## Configuration

Triggers are configured in `lambda_config` for each Lambda:

```hcl
lambda_config = {
  order_processor = {
    # SNS trigger - invoked when message published to topic
    sns_triggers = [
      { 
        topic_arn   = aws_sns_topic.orders.arn
        statement_id = "allow-sns-orders"  # Optional, auto-generated if omitted
      }
    ]
    
    # SQS trigger - polls queue for messages
    sqs_triggers = [
      { 
        queue_arn  = aws_sqs_queue.jobs.arn
        batch_size = 10   # Default: 10, range: 1-10000
        enabled    = true # Default: true
      }
    ]
  }
}
```

## Lifecycle Operations

### Creation

When triggers are added to `lambda_config`:

**SNS Triggers:**
1. Lambda permission is added to allow SNS to invoke the function
2. Lambda is subscribed to the SNS topic

**SQS Triggers:**
1. Event source mapping is created connecting the queue to the Lambda

### Updates

When trigger configuration changes:

**SNS Triggers:**
- `statement_id` change: Old permission removed, new permission added
- `topic_arn` change: Old trigger deleted, new trigger created

**SQS Triggers:**
- `batch_size` change: Event source mapping is updated in-place
- `enabled` change: Event source mapping is updated in-place
- `queue_arn` change: Old mapping deleted, new mapping created

### Deletion

When triggers are removed from `lambda_config`:

**SNS Triggers:**
1. Lambda permission is removed
2. SNS subscription is deleted

**SQS Triggers:**
1. Event source mapping is deleted

### Reconciliation

During `terraform apply`, the provider:

1. Queries AWS for existing triggers on each Lambda
2. Compares existing state with desired configuration
3. Creates missing triggers
4. Updates triggers with changed settings
5. Deletes orphaned triggers (in AWS but not in config)

## Change Detection

Trigger changes are detected via hash calculation:

- `sns_triggers` and `sqs_triggers` are included in the Lambda config hash
- When triggers change, the hash changes, triggering a config update
- Trigger-only changes result in `LambdaUpdateTypeConfig` (no code redeployment)

## Error Handling

### Idempotency

All trigger operations are idempotent:

- Creating an existing SNS permission returns success
- Creating an existing SNS subscription returns the existing ARN
- Creating an existing SQS mapping updates it instead
- Deleting a non-existent trigger returns success

### Retry Logic

Transient failures are retried with exponential backoff:

- `TooManyRequestsException` - AWS rate limiting
- `ServiceException` - Transient AWS errors
- `ThrottlingException` - AWS throttling
- Network timeouts

Non-retryable errors are logged and the operation continues with other triggers.

### Partial Failures

If one trigger operation fails:

- Other triggers continue to be processed
- Failures are logged as warnings
- The Lambda update is not failed (best-effort reconciliation)

## AWS Resources Created

### SNS Triggers

| Resource | Description |
|----------|-------------|
| Lambda Permission | Resource-based policy allowing SNS to invoke Lambda |
| SNS Subscription | Connects topic to Lambda endpoint |

### SQS Triggers

| Resource | Description |
|----------|-------------|
| Event Source Mapping | Connects queue to Lambda with polling configuration |

## Best Practices

1. **Set appropriate timeouts**: SQS visibility timeout should be >= Lambda timeout
2. **Use batch sizes wisely**: Larger batches improve throughput but increase failure blast radius
3. **Handle partial batch failures**: Return failed message IDs for SQS to retry
4. **Monitor dead-letter queues**: Configure DLQs for messages that fail repeatedly

## Example: Event-Driven Architecture

```hcl
# SNS topic for order events
resource "aws_sns_topic" "orders" {
  name = "order-events"
}

# SQS queue for background processing
resource "aws_sqs_queue" "background" {
  name                       = "background-jobs"
  visibility_timeout_seconds = 300
}

resource "conveyor_belt" "main" {
  source            = "${path.module}/routes.tf.rb"
  app_name          = "myapp"
  lambda_source_dir = "${path.module}/lambda"
  frontend_urls     = ["https://app.example.com"]
  
  lambda_config = {
    # API Lambda publishes to SNS/SQS
    api = {
      env_vars = {
        SNS_TOPIC_ARN = aws_sns_topic.orders.arn
        SQS_QUEUE_URL = aws_sqs_queue.background.url
      }
    }
    
    # Triggered by SNS
    order_processor = {
      sns_triggers = [{ topic_arn = aws_sns_topic.orders.arn }]
    }
    
    # Triggered by SQS
    background_worker = {
      timeout = 300
      sqs_triggers = [{ queue_arn = aws_sqs_queue.background.arn, batch_size = 10 }]
    }
  }
}
```

## Troubleshooting

### Trigger not firing

1. Check Lambda permissions: `aws lambda get-policy --function-name <name>`
2. Check SNS subscriptions: `aws sns list-subscriptions-by-topic --topic-arn <arn>`
3. Check event source mappings: `aws lambda list-event-source-mappings --function-name <name>`

### Permission errors

Ensure the Lambda execution role has permissions to:
- Read from SQS queues (`sqs:ReceiveMessage`, `sqs:DeleteMessage`, `sqs:GetQueueAttributes`)
- The Conveyor provider automatically adds these when `sqs_triggers` is configured

### Debug logging

```bash
TF_LOG=DEBUG terraform apply 2>&1 | grep -E "(trigger|SNS|SQS)"
```
