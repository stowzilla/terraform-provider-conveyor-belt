---
name: "terraform-conveyor-belt"
displayName: "Terraform Conveyor Provider"
description: "Complete guide for using the Conveyor Terraform provider to manage AWS serverless infrastructure with Ruby DSL route definitions"
keywords: ["terraform", "conveyor", "provider", "route", "routes", "aws", "serverless", "api-gateway", "lambda"]
author: "Stowzilla"
---

# Terraform Conveyor Provider

## Overview

Custom Terraform provider for AWS serverless infrastructure. Define routes in a Ruby DSL file → provider creates Lambda functions, API Gateways, IAM roles, and CloudWatch resources automatically.

**Key benefits:** Surgical updates (only changed resources modified), automatic permissions, built-in monitoring, parallel Lambda updates.

## Resources

| Resource | Purpose |
|----------|---------|
| `conveyor` | **Recommended** - Orchestrates all infrastructure from routes.tf.rb |
| `conveyor_belt_lambda` | Individual Lambda function (advanced use) |
| `conveyor_belt_gateway` | Individual API Gateway (advanced use) |

## Installation

Declare the provider in your Terraform configuration:

```hcl
terraform {
  required_providers {
    conveyor-belt = {
      source  = "stowzilla/conveyor-belt"
      version = "~> 0.32"
    }
  }
}
```

Then run `terraform init`. Upgrade with `terraform init -upgrade`.

## Quick Start

### 1. Routes File (routes.tf.rb)

```ruby
# namespace = which API Gateway handles the route
# lambda = which Lambda function processes the request, defaults to same name as namespace unless inside a scope block.
namespace :api, auth: :cognito do
  resources :customers
  resources :items, only: [:index], tables: [:inventory, :containers] do
    get '/path', on: :member
    get '/search', on: :collection
  end
end

namespace :ops do
  get "/health"
  scope module: :landing, auth: :none do
    post '/contact'
  end
end
```

Route JSON output format:
```json
{
  "name": "customers_index",
  "verb": "GET",
  "path": "/api/customers",
  "gateway": "api",
  "lambda": "customer",
  "auth": "none",
  "tables": []
}
```

### 2. Terraform Configuration

```hcl
terraform {
  required_providers {
    conveyor = {
      source  = "stowzilla/conveyor-belt"
      version = "~> 0.32"
    }
  }
}

provider "conveyor" {
  environment            = var.environment
  aws_region             = var.aws_region
  default_lambda_timeout = 30
  default_lambda_memory  = 256
  default_tags = {
    Application = "myapp"
  }
}

resource "conveyor_belt" "main" {
  source            = "${path.module}/routes.tf.rb"
  app_name          = "myapp"
  lambda_source_dir = "${path.module}/lambda"
  frontend_urls     = ["https://app.example.com"]
  
  cognito_user_pool_arns = [module.cognito.user_pool_arn]
  
  # Optional: unified API access via custom domain
  # custom_domain_name = "api.example.com"
  
  lambda_config = {
    shared = {
      env_vars = { LOG_LEVEL = "info" }
    }
    
    customer = {
      timeout     = 60
      memory_size = 512
      dynamodb_tables = [
        { name = "customers", access = "read_write" }
      ]
    }
    
    # Standalone Lambda (no API route)
    background_worker = {
      sqs_triggers = [
        { queue_arn = aws_sqs_queue.jobs.arn, batch_size = 10 }
      ]
    }
  }
  
  read_only_tables  = ["config"]
  read_write_tables = ["audit_log"]
  
  alarm_config = {
    enabled       = true
    sns_topic_arn = aws_sns_topic.alerts.arn
  }
}

output "api_url" {
  value = conveyor_belt.main.gateway_urls["api"]
}
```

### 3. Lambda Source Files

```
lambda/
├── Gemfile
├── customer.rb    # Matches lambda: "customer"
├── health.rb      # Matches lambda: "health"
└── background_worker.rb
```

## Provider Configuration

| Attribute | Required | Default | Description |
|-----------|----------|---------|-------------|
| `environment` | Yes | - | Environment name |
| `aws_region` | Yes | - | AWS region |
| `default_lambda_timeout` | No | 30 | Default timeout (seconds) |
| `default_lambda_memory` | No | 128 | Default memory (MB) |
| `default_tags` | No | {} | Tags for all resources |
| `docker_build_concurrency` | No | CPU count | Max concurrent Docker builds |

## conveyor Resource

### Arguments

| Attribute | Required | Description |
|-----------|----------|-------------|
| `source` | Yes | Path to routes.tf.rb |
| `app_name` | Yes | Application name for resource naming |
| `lambda_source_dir` | Yes | Directory with Lambda source files |
| `frontend_urls` | Yes | Frontend URLs for CORS |
| `cognito_user_pool_arns` | No | Cognito User Pool ARNs |
| `custom_domain_name` | No | Custom domain (e.g., `api.example.com`) |
| `friendly_errors` | No | Enable detailed error messages for routing errors (recommended for non-prod) |
| `schema_source` | No | Path to `schema.tf.rb` for API Gateway model definitions and request validation |
| `lambda_config` | No | Per-lambda configuration |
| `shared_iam_policy_arns` | No | IAM policies for all Lambdas |
| `lambda_layer_arns` | No | Lambda Layers for all functions |
| `lambda_shared_dirs` | No | Shared directories to include in Lambda packages (default: `models`, `lib`, `helpers`, `templates`) |
| `read_only_tables` | No | DynamoDB tables all Lambdas can read |
| `read_write_tables` | No | DynamoDB tables all Lambdas can read/write |
| `alarm_config` | No | CloudWatch alarm configuration |

### Outputs

| Attribute | Description |
|-----------|-------------|
| `lambda_functions` | Map of lambda name → ARN |
| `gateway_ids` | Map of gateway name → API Gateway ID |
| `gateway_urls` | Map of gateway name → invoke URL |
| `custom_domain_url` | Base URL for custom domain |
| `base_path_mappings` | Map of gateway name → base path |
| `routes_hash` | Hash of routes for change detection |
| `lambda_hashes` | Map of lambda name → source/config hash |
| `gateway_hashes` | Map of gateway name → routes hash |
| `model_hashes` | Map of gateway name → model definition hash |

### lambda_config Options

```hcl
lambda_config = {
  shared = { env_vars = { KEY = "value" } }  # Applied to all
  
  lambda_name = {
    env_vars    = { KEY = "value" }
    timeout     = 30
    memory_size = 256
    
    dynamodb_tables = [{ name = "table", access = "read_write" }]
    s3_buckets      = [{ name = "bucket", access = "read_only" }]
    ses_emails      = [{ identity = "noreply@example.com" }]
    sns_triggers    = [{ topic_arn = "arn:aws:sns:..." }]
    sqs_triggers    = [{ queue_arn = "arn:aws:sqs:...", batch_size = 10 }]
    
    # VPC configuration (optional)
    vpc_config = {
      subnet_ids         = ["subnet-abc123", "subnet-def456"]
      security_group_ids = ["sg-abc123"]
    }
  }
}
```

When `vpc_config` is provided, the provider automatically attaches the `AWSLambdaVPCAccessExecutionRole` managed policy to the Lambda's IAM role for ENI management.

### Trigger Lifecycle Management

SNS and SQS triggers are fully managed throughout the Lambda lifecycle:

```hcl
lambda_config = {
  order_processor = {
    # SNS triggers - Lambda invoked when message published
    sns_triggers = [
      { topic_arn = aws_sns_topic.orders.arn }
    ]
    
    # SQS triggers - Lambda polls queue
    sqs_triggers = [
      { queue_arn = aws_sqs_queue.jobs.arn, batch_size = 10 }
    ]
  }
}
```

Trigger changes are automatically detected and reconciled:
- New triggers are created when added to config
- Existing triggers are updated when settings change (e.g., batch_size)
- Removed triggers are deleted from AWS
- Orphaned triggers (in AWS but not in config) are cleaned up

### alarm_config Options

```hcl
alarm_config = {
  enabled            = true
  sns_topic_arn      = "arn:aws:sns:..."
  
  # Error alarms
  error_enabled            = true
  error_threshold          = 1
  error_period             = 300        # seconds
  error_evaluation_periods = 1
  
  # Duration alarms
  duration_enabled            = true
  duration_threshold          = 5000    # milliseconds
  duration_period             = 300
  duration_evaluation_periods = 1
  
  # Throttle alarms
  throttle_enabled            = true
  throttle_threshold          = 1
  throttle_period             = 300
  throttle_evaluation_periods = 1
  
  # Per-lambda overrides
  lambda_overrides = {
    payment = { error_threshold = 1, duration_threshold = 3000 }
  }
}
```

## CORS Support

### Automatic CORS Headers

The provider automatically configures CORS for all API Gateways:

1. **Lambda Response CORS**: OPTIONS methods with mock integrations return CORS headers for preflight requests
2. **Gateway Response CORS**: Error responses (401, 403, 4xx, 5xx) include CORS headers

This ensures browsers can properly handle both successful responses and error responses from API Gateway.

### Gateway Responses with CORS

When API Gateway returns errors before Lambda is invoked (e.g., Cognito authorizer failures), the provider automatically configures gateway responses with CORS headers for:

- `UNAUTHORIZED` (401) - Cognito/IAM auth failures
- `ACCESS_DENIED` (403) - Authorization policy denials  
- `DEFAULT_4XX` - All other 4xx errors
- `DEFAULT_5XX` - All 5xx errors
- `MISSING_AUTHENTICATION_TOKEN` (404) - Route not found
- `RESOURCE_NOT_FOUND` (404) - Wrong HTTP method
- `BAD_REQUEST_BODY` (400) - Invalid JSON

CORS headers configured:
- `Access-Control-Allow-Origin`: Based on `frontend_urls` (or `*` for multiple origins)
- `Access-Control-Allow-Headers`: `Content-Type,X-Amz-Date,Authorization,X-Api-Key,X-Amz-Security-Token`
- `Access-Control-Allow-Methods`: `GET,POST,PUT,DELETE,PATCH,OPTIONS`

This is critical for browser-based frontends calling authenticated endpoints - without these headers, browsers block error responses with CORS errors, masking the actual authentication failure.

### Friendly Error Messages

Enable detailed error messages for missing routes in development environments:

```hcl
resource "conveyor_belt" "main" {
  source            = "${path.module}/routes.tf.rb"
  app_name          = "myapp"
  lambda_source_dir = "${path.module}/lambda"
  frontend_urls     = ["https://app.example.com"]
  
  # Enable friendly errors for dev/uat/staging
  friendly_errors = var.environment != "prod"
}
```

**With `friendly_errors = true`** (Development):
```json
{
  "error": "Route Not Found",
  "message": "The API route you are trying to access does not exist. Check the HTTP method (GET/POST/PUT/DELETE) and path.",
  "path": "/api/customers",
  "method": "POST",
  "environment": "uat",
  "hint": "Run ./scripts/list_routes.rb to see all available routes"
}
```

**With `friendly_errors = false`** (Production - default):
```json
{
  "error": "Not Found",
  "message": "The requested resource does not exist"
}
```

## Custom Domain Support

Routes traffic from unified domain to appropriate API Gateways:

```
https://api.example.com/ops/health    → ops gateway → /health
https://api.example.com/api/customers → api gateway → /customers
```

**Prerequisites:** Create ACM certificate and API Gateway custom domain first. See `docs/CUSTOM_DOMAIN_SETUP.md`.

## Granular Resources (Advanced)

### conveyor_belt_lambda

```hcl
resource "conveyor_belt_lambda" "customer" {
  name       = "customer"
  app_name   = "myapp"
  source_dir = "${path.module}/lambda"
  timeout    = 30
  memory     = 512
  env_vars   = { LOG_LEVEL = "info" }
  tables     = ["customers"]
}
```

### conveyor_belt_gateway

```hcl
resource "conveyor_belt_gateway" "api" {
  name          = "api"
  app_name      = "myapp"
  frontend_urls = ["https://app.example.com"]
  
  routes = [
    {
      name       = "get_customer"
      verb       = "GET"
      path       = "/customer"
      lambda_arn = conveyor_belt_lambda.customer.arn
      auth       = "cognito"
    }
  ]
  
  cognito_user_pool_arns = [aws_cognito_user_pool.main.arn]
}
```

## Request Validation (Schema DSL)

Define API Gateway request models in `schema.tf.rb` for automatic payload validation at the gateway level:

```hcl
resource "conveyor_belt" "main" {
  source        = "${path.module}/routes.tf.rb"
  schema_source = "${path.module}/schema.tf.rb"
  # ... other attributes
}
```

Models are compiled into `components/schemas` in the generated OpenAPI 3.0.1 spec. Routes with `request_model` set (POST/PUT/PATCH only) get a `requestBody` with a `$ref` to the schema and `x-amazon-apigateway-request-validator: body-only`. API Gateway validates payloads before invoking Lambda — invalid requests get a 400 response without consuming Lambda invocations.

Response models are also supported via `response_model` and `response_context` on routes. The full model name is constructed as `{response_model}_{response_context}_response` (defaults to gateway name if no context specified).

## Import Support

All three resources support `terraform import`:

```bash
terraform import conveyor_belt.main myapp-prod
terraform import conveyor_belt_lambda.customer myapp-prod-customer
terraform import conveyor_belt_gateway.api abc123xyz
```

## Debugging

```bash
TF_LOG=INFO terraform apply
TF_LOG=DEBUG terraform apply 2>&1 | grep CONVEYOR
```

### Local Development

Use `dev_overrides` in `~/.terraformrc` to test locally-built binaries:

```hcl
provider_installation {
  dev_overrides {
    "stowzilla/conveyor-belt" = "/path/to/terraform-provider-conveyor-belt/bin"
  }
  direct {}
}
```

Build and test without needing `terraform init`:

```bash
go build -o bin/terraform-provider-conveyor-belt ./cmd/terraform-provider-conveyor-belt
terraform plan
```

## Resource Naming

Pattern: `{app_name}-{environment}-{name}`
- Lambda: `myapp-prod-customer`
- IAM Role: `myapp-prod-customer-lambda-role`
- Log Group: `/aws/lambda/myapp-prod-customer`
- API Gateway: `myapp-prod-api`
