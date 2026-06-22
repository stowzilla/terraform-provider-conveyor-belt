---
inclusion: fileMatch
fileMatchPattern: "internal/resources/openapi_generator.go,internal/resources/conveyor_belt_resource.go,internal/resources/shared.go,scripts/lib/schema_dsl.rb,scripts/lib/route_dsl.rb"
---

# API Gateway Model Wiring (OpenAPI Pipeline)

## Overview

The conveyor-belt provider creates API Gateway models from `schema.tf.rb` and wires them to methods via the **OpenAPI 3.0.1 spec import** pipeline. Models become `components/schemas` entries in the generated spec, and request validation is configured declaratively via `requestBody` and `x-amazon-apigateway-request-validator` on each operation. The spec is deployed atomically via `PutRestApi`.

## Pipeline

```
schema.tf.rb + routes.tf.rb
        │
        ▼  (ruby list_routes.rb)
   JSON: routes[] + models[]
        │
        ▼  (executeRubyScriptWithModels)
   []utils.Route + []utils.ModelDefinition
        │
        ▼  (OpenAPIGenerator.GenerateSpec, per gateway)
   1. GetModelsForGateway — filter models needed for this gateway
   2. buildComponents — convert models to components/schemas (snakeToPascal naming)
   3. buildPaths/buildOperation — wire requestBody $ref for POST/PUT/PATCH routes
   4. Add x-amazon-apigateway-request-validator: "body-only" on operations with models
   5. Serialize to JSON → PutRestApi with mode=overwrite
```

## Response Model Naming

Routes declare `response_model` and `response_context` separately. The provider constructs the full model name:

```
{response_model}_{response_context}_response
```

Example: `response_model: "item"` + `response_context: "customer"` → `item_customer_response`

If `response_context` is empty, the gateway name is used as the default context.

This construction happens in `GetModelsForGateway()` in `shared.go` — it builds the `needed` set of model names, then filters `allModels` to only those referenced by the gateway's routes.

## Request Model Wiring in OpenAPI

In `buildOperation()` (`openapi_generator.go`), when a route has `RequestModel` set and the verb is POST, PUT, or PATCH:

```go
op["requestBody"] = map[string]interface{}{
    "required": true,
    "content": map[string]interface{}{
        "application/json": map[string]interface{}{
            "schema": map[string]string{
                "$ref": "#/components/schemas/" + snakeToPascal(route.RequestModel),
            },
        },
    },
}
op["x-amazon-apigateway-request-validator"] = "body-only"
```

The `body-only` validator is added to the spec's `x-amazon-apigateway-request-validators` section when any route on the gateway uses a request model.

## Model Schema Conversion

`modelToOpenAPISchema()` converts `ModelDefinition` to OpenAPI JSON Schema:

- `type: "object"` always set
- `properties` mapped via `propertiesToSchema()` (recursive for nested objects/arrays)
- `required` fields sorted for deterministic output
- Model names converted from `snake_case` to `PascalCase` via `snakeToPascal()`

## HTTP Method Filtering

Request models only apply to methods that accept a body. `buildOperation()` skips request body wiring for GET and DELETE:

```go
if route.RequestModel != "" && (verb == "POST" || verb == "PUT" || verb == "PATCH") {
```

## Change Detection

Model changes are detected via `CalculateModelHash()` in `shared.go`:

- Per-gateway hash of model definitions
- Compared in `detectGatewayChanges()` alongside gateway route hashes
- If model hash changes (even without route changes), the gateway is flagged for update
- `PutRestApi` redeploys the full spec including updated models
- Read function detects drift by comparing deployed models (`GetModels` API) against expected fingerprint

## Route DSL — response_context Passthrough

In `scripts/lib/route_dsl.rb`, the `add_route` method must include `response_context` in the options hash:

```ruby
route_options = {
  auth: options[:auth],
  request_model: options[:request_model],
  response_model: options[:response_model],
  response_context: options[:response_context],  # Must be included
  ...
}
```

Without this, `response_context` is silently dropped and the provider can't construct the correct response model name.

## Key Files

| File | Responsibility |
|------|---------------|
| `internal/resources/openapi_generator.go` | GenerateSpec, buildComponents (schemas), buildOperation (requestBody wiring) |
| `internal/resources/openapi_deployer.go` | PutRestApi deployment, deployGateway stage creation |
| `internal/resources/shared.go` | GetModelsForGateway, CalculateModelHash, snakeToPascal, propertiesToSchema |
| `internal/resources/conveyor_belt_resource.go` | Orchestration: detectGatewayChanges (model hash comparison), Read drift detection |
| `internal/utils/grouping.go` | Route struct with RequestModel, ResponseModel, ResponseContext |
| `scripts/lib/route_dsl.rb` | Route class, response_context passthrough |
| `scripts/lib/schema_dsl.rb` | SchemaBuilder, request/response model parsing |

## Testing Model Wiring

After a deploy, verify with AWS CLI:

```bash
# Check models exist on gateway
aws apigateway get-models --rest-api-id <id> --query 'items[].name'

# Check the generated OpenAPI spec (written during plan/apply)
cat .conveyor-belt/openapi/<gateway>.json | jq '.components.schemas'

# Check request validator exists
aws apigateway get-request-validators --rest-api-id <id>

# Check a method has request model wired
aws apigateway get-method --rest-api-id <id> --resource-id <rid> --http-method PUT \
  --query '{requestModels: requestModels, requestValidatorId: requestValidatorId}'
```
