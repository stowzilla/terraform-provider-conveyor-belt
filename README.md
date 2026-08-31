# Terraform Provider Conveyor

A Terraform provider that manages AWS serverless infrastructure from a Ruby routes DSL file. Define your API routes in Ruby, and Conveyor creates Lambda functions, API Gateways, IAM roles, CloudWatch alarms, and custom domain mappings — all with parallel builds and incremental updates.

> **Requires the [Belt gem](https://github.com/stowzilla/belt).** Belt provides the Ruby DSL for defining routes (`routes.rb`) and contracts (`contracts.rb`), plus the `belt routes` and `belt contracts` CLI commands the provider depends on to parse them. You can't use this provider without it.

## Table of Contents

- [Installation](#installation)
- [Quick Start](#quick-start)
- [Provider Configuration](#provider-configuration)
- [Resources](#resources)
  - [conveyor_belt (Recommended)](#conveyor_belt)
  - [conveyor_belt_lambda](#conveyor_belt_lambda)
  - [conveyor_belt_gateway](#conveyor_belt_gateway)
- [Custom Domain Support](#custom-domain-support)
- [Data Sources](#data-sources)
- [Examples](#examples)
- [Development](#development)
- [Troubleshooting](#troubleshooting)

## Installation

### Prerequisites

- **Ruby** — For parsing route definitions
- **[Belt gem](https://github.com/stowzilla/belt)** — `gem install belt` (provides the `belt routes` CLI)
- **Docker** — For building Lambda packages
- **AWS CLI** — Configured with credentials
- **Terraform 1.0+**

### Install the Provider

The provider is available on the [Terraform Registry](https://registry.terraform.io/providers/stowzilla/conveyor-belt/latest). Just declare it in your Terraform configuration and run `terraform init`:

```hcl
terraform {
  required_providers {
    conveyor-belt = {
      source  = "stowzilla/conveyor-belt"
      version = "~> 0.0"
    }
  }
}
```

### Upgrade

Update the `version` constraint in your configuration, then:

```bash
terraform init -upgrade
```

## Quick Start

### 1. Define Routes (`routes.rb`)

Routes are defined using a Ruby DSL. The `gateway` keyword creates an API Gateway and a default Lambda function. Use `function` to route specific endpoints to a different Lambda.

```ruby
Belt.application.routes.draw do
  gateway :customer, auth: :cognito do
    resources :items, only: [:index], tables: [:inventory, :containers] do
      get '/search', on: :collection
    end
  end

  gateway :ops, auth: :cognito do
    resources :containers
    get "/health"
  end

  gateway :onboarding, auth: :none do
    post '/signup'
    post '/contact'
  end
end
```

The DSL generates JSON routes consumed by the provider (via `belt routes -f json`):

```json
{
  "name": "items_index",
  "verb": "GET",
  "path": "/items",
  "gateway": "customer",
  "lambda": "customer",
  "auth": "cognito",
  "tables": ["inventory", "containers"]
}
```

### Route DSL Keywords

| Keyword | Purpose | Affects Lambda? |
|---------|---------|-----------------|
| `gateway` | Creates an API Gateway + default Lambda | Yes — sets the default Lambda for all routes inside |
| `function` | Routes to a different Lambda | Yes — overrides the gateway's default |
| `namespace` | Adds path prefix + controller module | No — code organization only |
| `scope` | Flexible path/module/auth grouping | No — grouping and shared options only |

**Example combining keywords:**

```ruby
Belt.application.routes.draw do
  gateway :api, auth: :cognito do
    resources :posts                    # → lambda: api, path: /posts

    namespace :admin do
      resources :users                  # → lambda: api, path: /admin/users
    end

    function :worker do
      resources :jobs                   # → lambda: worker, path: /jobs
    end
  end
end
```

### 2. Configure the Provider

```hcl
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    conveyor-belt = {
      source  = "stowzilla/conveyor-belt"
      version = "~> 0.0"
    }
  }
}

provider "conveyor-belt" {
  environment            = var.environment
  aws_region             = var.aws_region
  default_lambda_timeout = 30
  default_lambda_memory  = 256
  default_tags = {
    Application = "myapp"
    Environment = var.environment
  }
}
```

### 3. Create the Conveyor Resource

```hcl
resource "conveyor_belt" "main" {
  source            = "${path.module}/routes.rb"
  app_name          = "myapp"
  lambda_source_dir = "${path.module}/lambda"

  frontend_urls = [
    "https://app.example.com",
    "https://admin.example.com"
  ]

  cognito_user_pool_arns = [module.cognito.user_pool_arn]

  # Custom domain for unified API access
  custom_domain_name = "api.example.com"

  # Friendly error messages for non-production
  friendly_errors = var.environment != "prod"

  # Per-lambda configuration
  lambda_config = {
    shared = {
      env_vars = { LOG_LEVEL = "info" }
    }

    customer = {
      timeout     = 60
      memory_size = 512
      env_vars    = { CACHE_TTL = "300" }
      dynamodb_tables = [
        { name = "customers", access = "read_write" }
      ]
    }

    # Standalone Lambda (no API route) — triggered by SQS
    background_worker = {
      timeout     = 300
      memory_size = 1024
      sqs_triggers = [
        { queue_arn = aws_sqs_queue.jobs.arn, batch_size = 10 }
      ]
    }
  }

  # Shared IAM policies for all Lambdas
  shared_iam_policy_arns = [aws_iam_policy.secrets_access.arn]

  # DynamoDB access for all Lambdas
  read_only_tables  = ["config"]
  read_write_tables = ["audit_log"]

  # CloudWatch alarms
  alarm_config = {
    enabled       = true
    sns_topic_arn = aws_sns_topic.alerts.arn

    lambda_overrides = {
      customer = {
        error_threshold    = 5
        duration_threshold = 10000
      }
    }
  }
}

output "api_url" {
  value = conveyor_belt.main.custom_domain_url
}
```

### 4. Apply

```bash
terraform init
terraform plan
terraform apply
```

## Provider Configuration

| Attribute | Required | Default | Description |
|-----------|----------|---------|-------------|
| `environment` | Yes | — | Environment name (dev, staging, prod) |
| `aws_region` | Yes | — | AWS region |
| `ruby_version` | No | `"3.4"` | Ruby version for Lambda runtime and default Docker build image |
| `docker_build_image` | No | — | Override the Docker image used to build Lambda dependencies |
| `default_lambda_timeout` | No | 30 | Default Lambda timeout (seconds) |
| `default_lambda_memory` | No | 128 | Default Lambda memory (MB) |
| `default_tags` | No | `{}` | Tags applied to all resources |
| `docker_build_concurrency` | No | CPU count | Max concurrent Docker builds |

## Resources

### conveyor_belt

The primary resource — orchestrates all infrastructure from a Ruby routes DSL file. This is the recommended way to use the provider.

#### How It Works

1. Parses `routes.rb` via `belt routes -f json` to extract routes
2. Builds Lambda packages in Docker (shared gem bundle, built once)
3. Creates one Lambda function per unique `lambda` value
4. Creates one API Gateway per unique `gateway` value
5. Wires Lambda integrations, IAM roles, CloudWatch alarms, and triggers
6. Creates base path mappings when a custom domain is configured
7. On subsequent applies, detects changes via hashes and updates only what changed — in parallel

#### Arguments

| Attribute | Required | Description |
|-----------|----------|-------------|
| `source` | Yes | Path to `routes.rb` |
| `app_name` | Yes | Application name for resource naming |
| `lambda_source_dir` | Yes | Directory containing Lambda source files |
| `frontend_urls` | Yes | Frontend URLs for CORS configuration |
| `cognito_user_pool_arns` | No | Cognito User Pool ARNs for authentication |
| `custom_domain_name` | No | Custom domain for unified API access (e.g., `api.example.com`) |
| `friendly_errors` | No | Enable detailed error messages for routing errors (recommended for non-prod) |
| `schema_source` | No | Path to contracts file (auto-detects `contracts.rb`) |
| `lambda_config_dir` | No | Directory for per-lambda YAML config files (database.yml style) |
| `lambda_env_refs` | No | Map of reference names to Terraform-resolved values for YAML `ref()` syntax |
| `shared_iam_policy_arns` | No | IAM policy ARNs attached to all Lambdas |
| `lambda_layer_arns` | No | Lambda Layer ARNs for all functions |
| `lambda_shared_dirs` | No | Shared directories (default: `models`, `lib`, `helpers`, `templates`) |
| `read_only_tables` | No | DynamoDB tables with read access for all Lambdas |
| `read_write_tables` | No | DynamoDB tables with read/write access for all Lambdas |
| `lambda_config` | No | Per-lambda configuration overrides (see below) |
| `alarm_config` | No | CloudWatch alarm configuration (see below) |

#### lambda_config

A map of lambda names to configuration objects. The special key `shared` applies to all Lambdas.

```hcl
lambda_config = {
  shared = {
    env_vars = { KEY = "value" }
  }

  my_lambda = {
    env_vars    = { KEY = "value" }
    timeout     = 30
    memory_size = 256
    runtime     = "ruby3.4"

    # DynamoDB access
    dynamodb_tables = [
      { name = "table_name", access = "read_only" },
      { name = "table_name", access = "read_write" }
    ]

    # S3 access
    s3_buckets = [
      { name = "bucket_name", access = "read_write" }
    ]

    # SES email sending
    ses_emails = [
      { identity = "noreply@example.com" }
    ]

    # Event triggers (fully managed lifecycle — see Trigger Lifecycle below)
    sns_triggers = [
      { topic_arn = "arn:aws:sns:..." }
    ]
    sqs_triggers = [
      { queue_arn = "arn:aws:sqs:...", batch_size = 10 }
    ]

    # Per-lambda IAM policies
    iam_policy_arns = [
      "arn:aws:iam::123456789:policy/my-policy"
    ]
  }
}
```

#### YAML Lambda Configuration

For complex projects, configure lambdas via YAML files (database.yml style). Create `config/lambda/<name>.yml`:

```yaml
# config/lambda/api.yml
default:
  timeout: 30
  memory_size: 256
  env_vars:
    LOG_LEVEL: info

prod:
  timeout: 60
  memory_size: 512

# DynamoDB tables by name (ARN constructed by convention)
dynamodb_tables:
  users: [Query, GetItem, PutItem]
  sessions:
    permissions: [Query, DeleteItem]
    indexes:
      UserIndex: [Query]

# S3 buckets by name
s3_buckets:
  images: [PutObject, GetObject]

# Triggers with ref() for dynamic ARNs
sns_triggers:
  - topic_arn: ref(bounces_topic_arn)
sqs_triggers:
  - queue_arn: ref(jobs_queue_arn)
    batch_size: 10
```

Reference Terraform values in YAML using `ref()` syntax:

```hcl
resource "conveyor_belt" "main" {
  lambda_config_dir = "${path.module}/config/lambda"
  lambda_env_refs = {
    bounces_topic_arn = aws_sns_topic.bounces.arn
    jobs_queue_arn    = aws_sqs_queue.jobs.arn
  }
}
```

#### Partial Configs (Shared YAML)

Share configuration between a subset of lambdas using underscore-prefixed partials:

```yaml
# config/lambda/_worker_defaults.yml
default:
  timeout: 900
  memory_size: 1024
  ephemeral_storage: 2048
```

```yaml
# config/lambda/background.yml
includes: [_worker_defaults]
default:
  env_vars:
    JOB_TYPE: batch
```

Priority: `shared.yml` < partials (in order) < lambda file.

#### alarm_config

```hcl
alarm_config = {
  enabled       = true
  sns_topic_arn = "arn:aws:sns:..."

  # Default thresholds
  error_threshold      = 1
  duration_threshold   = 5000   # milliseconds
  throttle_threshold   = 1
  invocation_threshold = 1000

  # Per-lambda overrides
  lambda_overrides = {
    my_lambda = {
      error_threshold    = 5
      duration_threshold = 10000
    }
  }
}
```

#### Computed Attributes

| Attribute | Description |
|-----------|-------------|
| `lambda_functions` | Map of lambda name → Lambda ARN |
| `gateway_ids` | Map of gateway name → API Gateway ID |
| `gateway_urls` | Map of gateway name → invoke URL |
| `routes_hash` | Hash of routes for change detection |
| `lambda_hashes` | Map of lambda name → source/config hash |
| `gateway_hashes` | Map of gateway name → routes hash |
| `base_path_mappings` | Map of gateway name → base path (when custom domain is set) |
| `custom_domain_url` | Base URL for custom domain (e.g., `https://api.example.com`) |
| `model_hashes` | Map of gateway name → model definition hash |

#### Parallel Updates & Build Optimization

When you modify source files or configuration:

1. Separate source and config hashes detect what actually changed
2. Config-only changes (env vars, timeout, IAM) skip Docker builds entirely
3. Source changes trigger a single shared gem bundle build, then per-Lambda packaging in parallel
4. Lambda functions update concurrently (respecting `docker_build_concurrency`)
5. API Gateway routes update only if routes changed
6. IAM reconciliation runs in parallel across all Lambda roles
7. If one Lambda update fails, others continue — all failures are reported

#### Path Gem Support

When your `Gemfile.lock` has `PATH` sources (local gems in development), the provider automatically:

1. Runs `gem build` for each path gem into the Docker context
2. Rewrites the build Gemfile to use version pins
3. Includes the built gems in the Lambda package

This means you can develop with `path:` gems locally and deploy without changing your Gemfile.

#### Standalone Lambdas

Define Lambdas in `lambda_config` that don't have API routes — useful for event-driven workers:

```hcl
lambda_config = {
  background_worker = {
    timeout     = 300
    memory_size = 1024
    sqs_triggers = [
      { queue_arn = aws_sqs_queue.jobs.arn, batch_size = 10 }
    ]
  }
}
```

#### Trigger Lifecycle Management

SNS and SQS triggers are fully managed. When you modify `sns_triggers` or `sqs_triggers`, the provider automatically:

- Creates new triggers when added
- Updates existing triggers when settings change (e.g., `batch_size`)
- Deletes triggers when removed from configuration
- Cleans up orphaned triggers found in AWS but not in config
- Operations are idempotent with exponential backoff retry
- Individual trigger failures don't block other triggers

See [docs/TRIGGER_LIFECYCLE.md](docs/TRIGGER_LIFECYCLE.md) for details.

#### Friendly Error Messages

When `friendly_errors = true`, API Gateway returns detailed error responses for routing errors instead of generic messages:

```json
{
  "error": "Route Not Found",
  "message": "The API route you are trying to access does not exist.",
  "path": "/api/customers",
  "method": "POST",
  "environment": "uat",
  "hint": "Run 'belt routes' to see all available routes"
}
```

Recommended for dev/uat/staging. Disable in production for security.

#### IAM Reconciliation

The provider reconciles IAM state on every apply:

- Shared policies (`shared_iam_policy_arns`) are attached/detached across all Lambda roles
- Per-lambda policies (`iam_policy_arns` in lambda_config) are attached to individual roles
- Orphaned IAM roles from previous provider versions are discovered and cleaned up
- Stale policies are detached by querying AWS directly (not relying on Terraform state)

---

### conveyor_belt_lambda

Manages a single Lambda function with its IAM role, CloudWatch log group, and alarms. Use this for fine-grained control over individual Lambda functions.

#### Arguments

| Attribute | Required | Description |
|-----------|----------|-------------|
| `name` | Yes | Lambda name (without app/env prefix) |
| `app_name` | Yes | Application name for resource naming |
| `source_dir` | Yes | Directory containing Lambda source files |
| `shared_dirs` | No | Shared directories to include (default: `models`, `lib`, `helpers`, `templates`) |
| `env_vars` | No | Environment variables map |
| `timeout` | No | Timeout in seconds (uses provider default if not set) |
| `memory` | No | Memory in MB (uses provider default if not set) |
| `layer_arns` | No | Lambda Layer ARNs to attach |
| `tables` | No | DynamoDB table names for IAM permissions |
| `iam_policy_arns` | No | Additional IAM policy ARNs to attach |
| `tags` | No | Resource tags (merged with provider defaults) |

#### Computed Attributes

| Attribute | Description |
|-----------|-------------|
| `arn` | Lambda function ARN |
| `function_name` | Full Lambda function name |
| `role_arn` | IAM execution role ARN |
| `source_hash` | Hash of source code |
| `config_hash` | Hash of configuration |

#### Example

```hcl
resource "conveyor_belt_lambda" "payment" {
  name       = "payment"
  app_name   = "myapp"
  source_dir = "${path.module}/../lambda"

  timeout = 30
  memory  = 512

  env_vars = {
    STRIPE_SECRET_NAME = aws_secretsmanager_secret.stripe.name
  }

  tables = ["orders", "payments"]

  iam_policy_arns = [
    aws_iam_policy.secrets_access.arn
  ]
}
```

---

### conveyor_belt_gateway

Manages a single API Gateway REST API with routes, methods, integrations, CORS, and Cognito authorizers.

#### Arguments

| Attribute | Required | Description |
|-----------|----------|-------------|
| `name` | Yes | Gateway name (without app/env prefix) |
| `app_name` | Yes | Application name for resource naming |
| `routes` | Yes | List of route configurations (see below) |
| `frontend_urls` | Yes | Frontend URLs for CORS |
| `cognito_user_pool_arns` | No | Cognito User Pool ARNs for auth |
| `tags` | No | Resource tags |

#### Route Configuration

| Attribute | Required | Description |
|-----------|----------|-------------|
| `name` | Yes | Route identifier |
| `verb` | Yes | HTTP method (`GET`, `POST`, `PUT`, `DELETE`, `PATCH`) |
| `path` | Yes | API path (e.g., `/users/{id}`) |
| `lambda_arn` | Yes | ARN of Lambda to invoke |
| `auth` | No | Auth type: `none`, `cognito`, `iam` (default: `none`) |

#### Computed Attributes

| Attribute | Description |
|-----------|-------------|
| `api_id` | API Gateway REST API ID |
| `invoke_url` | API Gateway invoke URL |
| `routes_hash` | Hash of routes for change detection |

#### Example

```hcl
resource "conveyor_belt_gateway" "public_api" {
  name     = "public"
  app_name = "myapp"

  frontend_urls = ["https://app.example.com"]

  routes = [
    {
      name       = "health"
      verb       = "GET"
      path       = "/health"
      lambda_arn = conveyor_belt_lambda.health.arn
      auth       = "none"
    }
  ]
}
```

## Custom Domain Support

When `custom_domain_name` is set, Conveyor creates base path mappings to route traffic from your domain to the appropriate API Gateways based on the first path segment:

```
https://api.example.com/ops/containers    → ops gateway    → /containers
https://api.example.com/customer/profile  → customer gateway → /profile
https://api.example.com/onboarding/signup → onboarding gateway → /signup
```

### Prerequisites

Before using `custom_domain_name`, create the custom domain in AWS:

1. ACM certificate for your domain
2. API Gateway custom domain name resource
3. Route 53 alias record pointing to the custom domain

See [docs/CUSTOM_DOMAIN_SETUP.md](docs/CUSTOM_DOMAIN_SETUP.md) for complete Terraform code.

### Configuration

```hcl
resource "conveyor_belt" "main" {
  # ...
  custom_domain_name = "api.example.com"
}

output "api_base_url" {
  value = conveyor_belt.main.custom_domain_url
  # => "https://api.example.com"
}

output "mappings" {
  value = conveyor_belt.main.base_path_mappings
  # => { "ops" = "/ops", "customer" = "/customer" }
}
```

### Path Handling

The gateway name is stripped from the path before routing to API Gateway:

| Request Path | Gateway | API Gateway Receives |
|--------------|---------|---------------------|
| `/ops/containers` | ops | `/containers` |
| `/ops/containers/{id}` | ops | `/containers/{id}` |
| `/ops` | ops | `/` |

## Data Sources

### conveyor_belt_routes

Parses route definitions from a Ruby DSL file without creating any resources.

```hcl
data "conveyor_belt_routes" "main" {
  source = "${path.module}/../routes.rb"
}

output "routes" {
  value = data.conveyor_belt_routes.main.routes
}
```

## Examples

See the [examples](./examples) directory:

- [Basic](./examples/basic) — Simple `conveyor_belt_lambda` + `conveyor_belt_gateway` setup
- [With Cognito](./examples/with-cognito) — Authentication with Cognito across multiple gateways
- [With Triggers](./examples/with-triggers) — Event-driven architecture with SNS and SQS triggers

## Development

### Build from Source

```bash
go build -o bin/terraform-provider-conveyor-belt ./cmd/terraform-provider-conveyor-belt
```

### Test Local Changes

Use Terraform's `dev_overrides` to point Terraform at your locally-built binary. Add this to `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "stowzilla/conveyor-belt" = "/path/to/terraform-provider-conveyor-belt/bin"
  }
  direct {}
}
```

Then run Terraform as normal — no `terraform init` required when using dev_overrides:

```bash
terraform plan
terraform apply
```

> **Note:** Remove or comment out the `dev_overrides` block when you want to use the published registry version again.

### Debug Logging

```bash
TF_LOG=INFO terraform apply
TF_LOG=DEBUG terraform apply 2>&1 | grep CONVEYOR
```

## Troubleshooting

### Provider Not Found

Ensure your `required_providers` block has the correct source and run `terraform init`:

```hcl
conveyor-belt = {
  source  = "stowzilla/conveyor-belt"
  version = "~> 0.0"
}
```

### Import Existing Resources

```bash
terraform import conveyor_belt.main myapp-prod
terraform import conveyor_belt_lambda.customer myapp-prod-customer
terraform import conveyor_belt_gateway.api abc123xyz
```

### OPTIONS 500 Errors

See [docs/TROUBLESHOOTING_OPTIONS_500.md](docs/TROUBLESHOOTING_OPTIONS_500.md) for diagnosis and manual fix commands.

## License

MIT
