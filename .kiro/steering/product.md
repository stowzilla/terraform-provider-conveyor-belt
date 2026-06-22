# Product Overview

terraform-provider-conveyor-belt is a Terraform provider for AWS serverless infrastructure that uses a Ruby routes DSL as the single source of truth.

## Architecture

The provider exposes three resource types:

- `conveyor_belt` (Recommended) - Orchestrator that reads routes.tf.rb and manages all infrastructure
- `conveyor_belt_lambda` - Individual Lambda functions (for advanced use cases)
- `conveyor_belt_gateway` - Individual API Gateways (for advanced use cases)

The `conveyor_belt` resource is the primary interface:
- Parses routes.tf.rb via Ruby script
- Generates OpenAPI 3.0.1 specs per gateway
- Deploys API Gateways via `PutRestApi` (OpenAPI import)
- Creates Lambda functions and API Gateways automatically
- Updates resources in parallel for faster deployments
- Supports per-action configuration via `lambda_config`

## API Gateway Deployment

### conveyor_belt resource — OpenAPI Import
The `conveyor_belt` resource uses **OpenAPI 3.0.1 import** for API Gateway deployment:
1. `OpenAPIGenerator` builds a complete spec per gateway (routes, CORS, auth, models, Lambda integrations)
2. `openapi_deployer.go` calls `PutRestApi` with `mode=overwrite` to deploy the spec
3. Hash-based change detection — only redeploys gateways whose spec hash changed
4. Specs are written to `.conveyor-belt/openapi/{gateway}.json` for inspection

### conveyor_belt_gateway resource — Imperative
The `conveyor_belt_gateway` resource uses **imperative** API calls (CreateResource, PutMethod, PutIntegration, etc.) via `ParallelRouteProcessor`. This is a separate code path.

## Generated Output (.conveyor-belt/)

On every apply, the provider writes generated artifacts to `.conveyor-belt/` in the project root (parent of `lambda_source_dir`):

- `.conveyor-belt/openapi/{gateway}.json` — Full OpenAPI 3.0.1 specs per gateway
- `.conveyor-belt/scripts/` — Extracted embedded Ruby scripts (list_routes.rb, list_schemas.rb)

These files are generated during `terraform plan` and `terraform apply` and are **not committed to the repo**. Running `terraform plan` is sufficient to regenerate them — no full deploy needed.

## Key Features

- **Routes as Source of Truth**: Define API routes in Ruby DSL, infrastructure follows
- **OpenAPI-Based Deployment**: API Gateways deployed via OpenAPI 3.0.1 spec import (`PutRestApi`)
- **Parallel Updates**: Multiple Lambdas update concurrently (configurable concurrency)
- **Smart Change Detection**: Hash-based detection for source code, configuration, and OpenAPI spec changes
- **Per-Action Configuration**: Override timeout, memory, env vars, IAM policies per Lambda
- **Standalone Lambdas**: Create Lambdas without API routes for background jobs
- **Automatic IAM**: Creates execution roles with DynamoDB, S3, SES permissions (parallel reconciliation)
- **Cognito Integration**: Built-in Cognito authorizer support for API Gateway routes
- **CloudWatch Alarms**: Automatic alarm creation with per-action thresholds
- **Trigger Lifecycle Management**: Full create/update/delete lifecycle for SNS and SQS triggers
- **Custom Domain Support**: Base path mappings for unified API access via custom domain
- **Request Validation**: Schema DSL (schema.tf.rb) generates API Gateway request models and validators
- **Friendly Errors**: Detailed error responses for non-production environments

## Resource Naming

AWS resources follow the pattern: `{app_name}-{environment}-{name}`
- Lambda: `myapp-prod-customer`
- IAM Role: `myapp-prod-customer-lambda-role`
- Log Group: `/aws/lambda/myapp-prod-customer`
- API Gateway: `myapp-prod-api`
