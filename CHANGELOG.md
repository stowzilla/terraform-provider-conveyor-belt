# Changelog

All notable changes to `terraform-provider-conveyor-belt` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.0.2] - 2026-06-23

### Fixed

- **`terraform import` now correctly discovers existing infrastructure** — After importing a `conveyor_belt` resource, the provider queries AWS during plan to discover which Lambda functions and API Gateways already exist. Previously, all resources were incorrectly flagged as "to CREATE" because the empty post-import state had no way to distinguish new resources from existing ones.

- **Read no longer crashes when `source` is empty after import** — The Read function gracefully handles the post-import state where `source` is not yet available (it comes from config on the next plan/apply). Previously this caused a "Failed to parse routes" error.

- **"Provider produced inconsistent final plan/result" after import** — All computed outputs (`lambda_functions`, `api_gateway_ids`, `api_gateway_urls`, `base_path_mappings`, `custom_domain_url`, all hash maps) are marked as `unknown` during the first plan after import. This prevents Terraform from rejecting the apply when the provider populates these fields for the first time.

- **False model drift on every plan due to API Gateway adding `format` fields** — API Gateway automatically adds `"format": "int32"` to integer properties in model schemas during `PutRestApi`. The drift detection was comparing these enriched schemas against the provider's generated schemas (which don't include `format`), causing perpetual "Model Hash Drift Detected" warnings and unnecessary gateway redeployments. The deployed model fingerprint now strips API Gateway-added fields before comparison.

**Migration Guide**

`terraform state mv` doesn't allow moves between different resource types. Instead, pull the state,
rename the resource type in JSON, and push it back:

```bash
rm .terraform.lock.hcl
terraform init
terraform state replace-provider terraform.local/stowzilla/dispatcher stowzilla/conveyor-belt

# Pull state, rename the resource type, bump serial, push back
terraform state pull > state.json
sed -i 's/"type": "dispatcher"/"type": "conveyor_belt"/g' state.json
SERIAL=$(grep -o '"serial": [0-9]*' state.json | grep -o '[0-9]*')
sed -i "s/\"serial\": $SERIAL/\"serial\": $((SERIAL + 1))/" state.json
terraform state push state.json
rm state.json

# Move to the new module address (same resource type on both sides now)
terraform state mv \
  module.stowzilla.module.dispatcher.conveyor_belt.main \
  module.stowzilla.module.conveyor_belt.conveyor_belt.main

terraform init
terraform plan  # Should show no changes
```

This preserves all Terraform state (alarm thresholds, frontend_urls, lambda_shared_dirs, etc.).

## [0.0.1] - 2026-06-17

Initial release to the Terraform Registry as `stowzilla/conveyor-belt`.

Includes all functionality from the previous `dispatcher` provider (v0.32.18):

- Ruby routes DSL as single source of truth for AWS serverless infrastructure
- OpenAPI 3.0.1 import for API Gateway deployment
- Parallel Lambda create/update/delete with configurable concurrency
- Hash-based change detection (source, config, gateway, model, OpenAPI spec)
- Per-action Lambda configuration (timeout, memory, env vars, IAM policies)
- Automatic IAM role/policy management with parallel reconciliation
- Cognito authorizer integration
- CloudWatch alarms with per-action thresholds
- SNS/SQS trigger lifecycle management
- Custom domain base path mappings
- Request/response model validation via schema DSL
- Embedded Ruby DSL scripts (no external repo checkout needed)
