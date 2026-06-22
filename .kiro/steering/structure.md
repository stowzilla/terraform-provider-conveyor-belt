# Project Structure

```
terraform-provider-conveyor-belt/
├── cmd/terraform-provider-conveyor-belt/
│   └── main.go                    # Provider entry point
├── internal/
│   ├── provider/
│   │   ├── provider.go            # Provider schema and configuration
│   │   └── testing_helpers.go     # Test helpers for provider setup
│   ├── resources/
│   │   ├── conveyor_belt_resource.go # conveyor_belt resource (orchestrator)
│   │   ├── lambda_resource.go     # conveyor_belt_lambda resource
│   │   ├── gateway_resource.go    # conveyor_belt_gateway resource
│   │   ├── openapi_generator.go   # OpenAPI 3.0.1 spec generation from routes
│   │   ├── openapi_deployer.go    # PutRestApi-based gateway deployment
│   │   ├── parallel_manager.go    # Concurrent Lambda operations
│   │   ├── parallel_route_processor.go # Imperative route processing (gateway_resource only)
│   │   ├── path_dependency_graph.go    # Path segment dependency ordering
│   │   ├── route_processing_result.go  # Route processing result types
│   │   ├── thread_safe_cache.go   # Thread-safe resource ID cache
│   │   ├── base_path_mapping.go   # Custom domain base path mapping management
│   │   ├── package_builder.go     # Parallel Lambda package building
│   │   ├── dependency_analyzer.go # Ruby require statement analysis
│   │   ├── api_gateway_operations.go # API Gateway SDK operations
│   │   ├── iam.go                 # IAM role and policy management
│   │   ├── cloudwatch.go          # CloudWatch log groups and alarms
│   │   ├── trigger_manager.go     # SNS/SQS trigger lifecycle management
│   │   ├── env_resolver.go        # Environment variable resolution
│   │   ├── naming.go              # Resource naming utilities
│   │   └── shared.go              # Shared types, utilities, model helpers
│   ├── datasources/
│   │   └── routes.go              # Routes data source
│   ├── embedded/
│   │   └── scripts.go             # Embedded Ruby scripts (list_routes.rb, list_schemas.rb)
│   └── utils/
│       ├── grouping.go            # Route grouping utilities
│       └── logger.go              # Logging helpers
├── .conveyor-belt/                    # Generated output (not committed)
│   ├── openapi/                   # OpenAPI specs per gateway (e.g., customer.json)
│   └── scripts/                   # Extracted Ruby scripts for external use
├── examples/                       # Usage examples
└── *.md                           # Documentation
```

## Key Components

### Resources
- `dispatcherResource` — Orchestrates all infrastructure from routes.tf.rb. Uses **OpenAPI import** (`PutRestApi`) for API Gateway deployment.
- `lambdaResource` — Manages individual Lambda function, IAM role, CloudWatch resources
- `gatewayResource` — Manages individual API Gateway using **imperative** API calls (separate code path)

### OpenAPI Pipeline (conveyor_belt resource)
1. `OpenAPIGenerator` — Generates OpenAPI 3.0.1 specs from parsed routes, models, and Lambda ARNs
2. `openapi_deployer.go` — Deploys specs via `PutRestApi` with `mode=overwrite`
3. Specs written to `.conveyor-belt/openapi/{gateway}.json` for inspection
4. Hash-based change detection — only redeploys gateways whose spec hash changed

### Managers
- `ParallelManager` — Concurrent Lambda create/update/delete operations
- `IAMManager` — Creates/deletes IAM roles and policies (parallel reconciliation)
- `CloudWatchManager` — Creates/deletes log groups and alarms
- `PackageBuilder` — Builds Lambda packages with concurrency control
- `DependencyAnalyzer` — Analyzes Ruby require statements
- `TriggerManager` — Manages SNS/SQS trigger lifecycle (create/update/delete)
- `BasePathMappingManager` — Custom domain base path mapping CRUD
- `ParallelRouteProcessor` — Imperative route processing (used by `conveyor_belt_gateway` only)

### Naming Convention
- Resources: `{app_name}-{environment}-{name}`
- IAM Roles: `{app_name}-{environment}-{name}-lambda-role`
- Log Groups: `/aws/lambda/{app_name}-{environment}-{name}`
- API Gateways: `{app_name}-{environment}-{gateway_name}`
