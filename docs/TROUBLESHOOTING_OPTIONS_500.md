# Troubleshooting: OPTIONS Returns 500 Internal Server Error

## Symptom
- OPTIONS preflight requests return `500 Internal Server Error`
- CORS fails in browser with "No 'Access-Control-Allow-Origin' header"
- The API Gateway console shows OPTIONS method exists with MOCK integration

## Root Cause
**Missing Integration Response** - The OPTIONS method has:
- ✅ Method Response (200 with CORS headers defined)
- ✅ Integration (MOCK type with `{"statusCode": 200}`)
- ❌ **No Integration Response** mapping the CORS header values

Without the integration response, API Gateway doesn't know how to complete the request cycle.

## Diagnosis

```bash
# 1. Find the API Gateway ID
aws apigateway get-rest-apis --query "items[?contains(name, 'YOUR-API-NAME')].[id,name]" --output table

# 2. Find the resource ID for the failing path
aws apigateway get-resources --rest-api-id YOUR_API_ID --query "items[?contains(path, 'YOUR-PATH')].[id,path]" --output table

# 3. Check if integration response exists (this will error if missing)
aws apigateway get-integration-response \
  --rest-api-id YOUR_API_ID \
  --resource-id YOUR_RESOURCE_ID \
  --http-method OPTIONS \
  --status-code 200
```

If step 3 returns an error or empty result, the integration response is missing.

## Fix

```bash
# Create the missing integration response
aws apigateway put-integration-response \
  --rest-api-id YOUR_API_ID \
  --resource-id YOUR_RESOURCE_ID \
  --http-method OPTIONS \
  --status-code 200 \
  --response-parameters '{
    "method.response.header.Access-Control-Allow-Headers": "'"'"'Content-Type,X-Amz-Date,Authorization,X-Api-Key,X-Amz-Security-Token'"'"'",
    "method.response.header.Access-Control-Allow-Methods": "'"'"'GET,POST,PUT,DELETE,OPTIONS'"'"'",
    "method.response.header.Access-Control-Allow-Origin": "'"'"'*'"'"'",
    "method.response.header.Access-Control-Max-Age": "'"'"'86400'"'"'"
  }' \
  --response-templates '{"application/json": "{}"}'

# Deploy the changes
aws apigateway create-deployment \
  --rest-api-id YOUR_API_ID \
  --stage-name YOUR_STAGE \
  --description "Fix missing OPTIONS integration response"
```

## Bulk Check Script

Check all OPTIONS methods in an API for missing integration responses:

```bash
#!/bin/bash
API_ID="YOUR_API_ID"

echo "Checking OPTIONS methods for missing integration responses..."
for resource in $(aws apigateway get-resources --rest-api-id $API_ID --query "items[?resourceMethods.OPTIONS].[id,path]" --output text); do
  resource_id=$(echo $resource | cut -f1)
  path=$(echo $resource | cut -f2)
  
  if ! aws apigateway get-integration-response --rest-api-id $API_ID --resource-id $resource_id --http-method OPTIONS --status-code 200 >/dev/null 2>&1; then
    echo "MISSING: $path (resource: $resource_id)"
  fi
done
```

## Provider Bug (Fixed)

This was caused by a race condition in the terraform-conveyor-belt provider during parallel route processing. When multiple routes share the same API Gateway resource (e.g., `GET /path` and `POST /path`), they would all try to create OPTIONS methods concurrently. The check-then-create pattern was not atomic, allowing one goroutine to partially create the OPTIONS method while another overwrote it.

**Fixed in**: v0.23.14

The fix uses the thread-safe resource cache's `GetOrCreate` pattern to coordinate concurrent OPTIONS creation attempts. Only one goroutine creates the OPTIONS method while others wait for completion.

For older versions, running `terraform apply` again typically fixes it, as the provider will detect the incomplete state and retry.
