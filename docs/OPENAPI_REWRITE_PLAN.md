# OpenAPI Rewrite Plan

## Overview

Replace Conveyor's imperative API Gateway construction (450+ API calls per gateway) with OpenAPI spec generation + `PutRestApi` (2-3 API calls per gateway, atomic, debuggable).

## Current State: Phase 1 — Spec Generation (Zero Risk)

Phase 1 adds `internal/resources/openapi_generator.go` which generates OpenAPI 3.0.1 specs as a **read-only side effect** after the existing imperative deployment. No deployment behavior changes.

### What Phase 1 Does

1. After imperative gateway creation/update, generates one OpenAPI spec per gateway
2. Logs spec metadata at DEBUG level (`TF_LOG=DEBUG terraform apply | grep OpenAPI`)
3. Stores spec hashes in Terraform state (`openapi_spec_hashes` computed attribute)
4. Hash changes track when the spec would differ — useful for future Phase 2 comparison

### OpenAPI Spec Structure

Each gateway gets a spec like:

```json
{
  "openapi": "3.0.1",
  "info": { "title": "myapp-dev-customer", "version": "1.0" },
  "paths": {
    "/items": {
      "get": {
        "operationId": "items_index_get",
        "security": [{ "CognitoUserPoolAuthorizer": [] }],
        "x-amazon-apigateway-integration": {
          "type": "aws_proxy",
          "httpMethod": "POST",
          "uri": "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/{lambda-arn}/invocations"
        }
      },
      "options": { "...CORS mock integration..." }
    }
  },
  "components": {
    "securitySchemes": {
      "CognitoUserPoolAuthorizer": {
        "type": "apiKey",
        "x-amazon-apigateway-authorizer": {
          "type": "cognito_user_pools",
          "providerARNs": ["arn:aws:cognito-idp:..."]
        }
      }
    }
  },
  "x-amazon-apigateway-gateway-responses": {
    "UNAUTHORIZED": { "...CORS headers + error template..." },
    "DEFAULT_4XX": { "..." },
    "DEFAULT_5XX": { "..." }
  }
}
```

### What the Spec Covers

- **Paths**: One entry per unique route path, methods keyed by lowercase HTTP verb
- **Lambda integrations**: `x-amazon-apigateway-integration` with `aws_proxy` type
- **CORS**: OPTIONS method on every path with MOCK integration, computed Allow-Methods
- **Auth**: Cognito (`security` + `securitySchemes`) and IAM (`sigv4`) per-method
- **Gateway responses**: All 7 response types with CORS headers and error templates
- **Models**: Request/response schemas in `components/schemas` with `$ref` references
- **Request validation**: `x-amazon-apigateway-request-validator: body-only` when models present
- **Path parameters**: Extracted from `{param}` segments, declared in `parameters`

## Phase Roadmap

| Phase | Goal | Risk | Status |
|-------|------|------|--------|
| 1 | Generate specs, store hashes, validate | Zero | **Complete** |
| 2 | Flag-gated PutRestApi deployment | Low | **Complete** |
| 3 | OpenAPI as default, imperative as fallback | Medium | **Complete** |
| 4 | Delete ~2,600 lines of imperative code | Low | **Complete** |

## Phase 2: Flag-Gated PutRestApi Deployment

### How It Works

Set `use_openapi_import = true` on the `conveyor_belt` resource to opt in:

```hcl
resource "conveyor_belt" "main" {
  source            = "${path.module}/routes.tf.rb"
  app_name          = "myapp"
  lambda_source_dir = "${path.module}/lambda"
  use_openapi_import = true
  # ... other config
}
```

When enabled, gateway create/update uses this flow instead of imperative SDK calls:

1. Generate OpenAPI 3.0.1 spec (same generator as Phase 1)
2. `CreateRestApi` (for new gateways only)
3. `PutRestApi` with `mode=overwrite` to import the spec atomically
4. `CreateDeployment` to deploy the stage
5. `AddPermission` for Lambda invoke permissions

### Key Files

- `internal/resources/openapi_deployer.go` — PutRestApi-based create/update (~200 lines)
- `internal/resources/openapi_deployer_test.go` — Tests for request validators and config
- `internal/resources/openapi_generator.go` — Spec generation (Phase 1, now includes request validators)

### What Changed from Phase 1

- Added `use_openapi_import` boolean attribute to conveyor_belt resource schema
- Added `UseOpenAPIImport` to `ConveyorConfig`
- Added `x-amazon-apigateway-request-validators` to spec when models are present
- `createSingleGateway` and `updateSingleGateway` branch on the flag
- New `openapi_deployer.go` with `createGatewayViaOpenAPI` and `updateGatewayViaOpenAPI`
- Models parameter threaded through `CreateGatewaysInParallel` and `UpdateGatewaysInParallel`

## Key Files

- `internal/resources/openapi_generator.go` — Spec generation (~350 lines)
- `internal/resources/openapi_generator_test.go` — 24 tests including property-based
- `internal/resources/conveyor_belt_resource.go` — Integration point (side effect in Create/Update)

## Phase 3: OpenAPI as Default, Imperative as Fallback

### What Changed

Replaced `use_openapi_import = true` (opt-in) with `use_imperative_deploy = true` (opt-out fallback).

**Before (Phase 2):** Imperative deploy was the default. Set `use_openapi_import = true` to use OpenAPI.
**After (Phase 3):** OpenAPI import is the default. Set `use_imperative_deploy = true` to fall back to imperative.

### Migration

Remove `use_openapi_import = true` from your conveyor_belt resource. OpenAPI import is now automatic.

If you need the old imperative path:

```hcl
resource "conveyor_belt" "main" {
  # ...
  use_imperative_deploy = true
}
```

### Key Files Changed

- `internal/resources/shared.go` — `UseOpenAPIImport` → `UseImperativeDeploy`
- `internal/resources/conveyor_belt_resource.go` — Schema attribute renamed, description updated
- `internal/resources/parallel_manager.go` — Condition flipped: `!config.UseImperativeDeploy` → OpenAPI path
- `internal/resources/openapi_deployer_test.go` — Test updated for inverted semantics

## Open Questions (for Phase 4)

1. Which imperative code paths are safe to delete? (Need to audit all callers)
2. Should `use_imperative_deploy` be removed entirely in Phase 4, or kept as a permanent escape hatch?

## Phase 4: Delete Imperative Code (~3,600 lines removed)

### What Changed

Removed the `use_imperative_deploy` fallback flag and all imperative API Gateway code from the `conveyor_belt` resource. OpenAPI import via PutRestApi is now the only deployment path.

The standalone `conveyor_belt_gateway` resource retains its imperative path (it doesn't use OpenAPI).

### Deleted Files

- `internal/resources/model_manager.go` — Imperative model sync (SyncModels, EnsureRequestValidator, AttachModelToMethod)
- `internal/resources/model_manager_test.go` — Tests for above
- `internal/resources/preservation_test.go` — Tests for ModelManager pagination
- `internal/resources/pagination_bug_test.go` — Tests for ModelManager pagination
- `internal/resources/cors_options_fix_test.go` — Tests for deleted sequential CORS functions

### Deleted from `api_gateway_operations.go` (~900 lines)

- `UpdateApiGatewayResourcesSequential` and all sequential helper functions
- `createRouteResources`, `createResource`, `createMethodAndIntegration`
- `updateMethodAndIntegration`, `ensureIntegrationExists`
- `createCorsOptionsMethod`, `ensureCorsResponses`, `createCognitoAuthorizer`
- `createMethodResponseWithCors`, `DeleteGatewayResponses`
- `TestConnectivity`, `DeleteApiGateways`, `FindApiGatewayByName`, `FindApiGatewayByPrefix`
- `resourceCache` field (only used by sequential path)

### Deleted from `parallel_manager.go` (~330 lines)

- Imperative create block in `createSingleGateway` (after OpenAPI branch)
- Imperative update block in `updateSingleGateway` (after OpenAPI branch)

### Deleted from `conveyor_belt_resource.go` (~130 lines)

- `use_imperative_deploy` schema attribute, model field, config extraction
- `syncModelsForGateways` function (OpenAPI spec includes models atomically)

### Moved to `shared.go` (~120 lines)

Pure utility functions needed by the OpenAPI path, extracted from deleted `model_manager.go`:
- `GetModelsForGateway`, `CalculateModelHash`, `snakeToPascal`
- `propertiesToSchema`, `propertyToSchema`, `mapType`

### What's Preserved

The `conveyor_belt_gateway` resource still uses the full imperative path via:
- `ApiGatewayOperations` struct and its methods
- `ParallelRouteProcessor` for concurrent route creation
- `ConfigureGatewayResponses` for CORS error responses
- `CreateApiGatewayResources` / `UpdateApiGatewayResources` wrappers
