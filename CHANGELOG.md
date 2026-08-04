# Changelog

All notable changes to `terraform-provider-conveyor-belt` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.0.12] - 2026-08-04

### Added

- **`ruby_version` configuration option** — Set the Ruby version for both the Lambda runtime and the default Docker build image with a single provider-level setting. Example: `ruby_version = "4.0"` sets the Lambda runtime to `ruby4.0` and uses `public.ecr.aws/sam/build-ruby4.0:latest-x86_64` for gem compilation. Default: `"3.4"` (no breaking change). The explicit `docker_build_image` option still overrides the derived image for fully custom build environments.

## [0.0.11] - 2026-08-04

### Added

- **`docker_build_image` configuration option** — Override the Docker image used to build Lambda dependencies. Configurable at the provider level (applies to all resources) or per-resource (overrides the provider setting). Default remains `public.ecr.aws/sam/build-ruby3.4:latest-x86_64`. Any image with Ruby, Bundler, and `/bin/bash` available is compatible.

## [0.0.10] - 2026-08-04

### Added

- **Per-lambda `iam_policy_arns` in YAML config** — Lambda config files (`config/lambda/*.yml`) now support `iam_policy_arns` for attaching IAM policies to individual lambdas instead of all lambdas via `shared_iam_policy_arns`. Supports `ref()` markers resolved through `lambda_env_refs`.
- `AttachPolicyArns` method on IAMManager — shared utility for attaching a list of policy ARNs to a role, used by both shared and per-lambda paths.
- `extractPerLambdaIamPolicyArns` helper for reading resolved ARNs from lambda_config.

### Changed

- `AttachSharedIamPolicies` now delegates to `AttachPolicyArns` (no behavior change for existing users).

## [0.0.9] - 2026-07-31

### Changed

- **Contracts file auto-detection** — When `schema_source` is not explicitly set, auto-detection now checks for `contracts.rb` → `contracts.tf.rb` → `schema.tf.rb` (in that order) next to the routes source file. Existing apps with `schema.tf.rb` continue working without changes.
- Updated `source` attribute description to reference both `routes.rb` and `routes.tf.rb`.
- Updated `schema_source` attribute description to document the new detection order.

## [0.0.8] - 2026-07-30

### Added

- **Auto-materialize Gemfile `path:` gems** — When `Gemfile.lock` has `PATH` sources, the package build host-side `gem build`s each one into the Docker context `vendor/cache/`, rewrites the *build* Gemfile to a version pin, and re-locks. Docker then installs a normal gem with `specifications/` so Lambda bare `require` works. Absolute agent worktree paths are fine. Does not touch the app's real Gemfile. Source trees listed as PATH remotes are also hashed so edits trigger rebuilds without a version bump.

### Changed

- **`vendor/cache` resolution** — Pre-built `.gem` files are now found next to the Gemfile first (project root), then under `lambda_source_dir/vendor/cache`. Matches Bundler's natural cache location so unreleased local gems need only one copy for both `bundle lock` and Lambda packaging.

## [0.0.7] - 2026-07-23

### Added

- **`includes` with underscore partials** — Share config between a subset of lambdas without affecting all. Create partial files prefixed with `_` (e.g., `_worker_defaults.yml`) and reference them via the `includes` key:
  ```yaml
  # config/lambda/_worker_defaults.yml
  default:
    timeout: 900
    memory_size: 1024
    ephemeral_storage: 2048

  # config/lambda/background.yml
  includes: [_worker_defaults]
  default:
    env_vars:
      JOB_TYPE: batch
  ```
  Partials support environment-specific overrides (dev/prod blocks) just like regular configs. Multiple includes merge in order, and lambda-specific values always win. Priority: `shared.yml` < partials (in order) < lambda file.

## [0.0.6] - 2026-07-22

### Added

- **`runtime` support in YAML** — Specify Lambda runtime per function (e.g., `runtime: ruby3.4`).

- **S3 buckets by name in YAML** — Reference S3 buckets by logical name with convention-based ARN construction (`arn:aws:s3:::{app}-{env}-{name}`). Supports shorthand and expanded form with `ref()` for non-convention buckets:
  ```yaml
  s3_buckets:
    images: [PutObject, GetObject]
    custom_bucket:
      bucket_arn: ref(external_bucket_arn)
      permissions: [GetObject]
  ```

- **SNS triggers with `ref()` in YAML** — Declare SNS triggers with resolvable topic ARNs:
  ```yaml
  sns_triggers:
    - topic_arn: ref(ses_bounces_topic_arn)
      statement_id: AllowSESBounces
  ```

- **SQS triggers with `ref()` in YAML** — Declare SQS triggers with resolvable queue ARNs:
  ```yaml
  sqs_triggers:
    - queue_arn: ref(notifications_queue_arn)
      batch_size: 10
  ```

- S3 permission names auto-prefixed with `s3:` if no colon present.
- `s3_buckets`, `sns_triggers`, and `sqs_triggers` from YAML and TF `lambda_config` are now concatenated during merge (same as `dynamodb_tables`).

## [0.0.5] - 2026-07-22

### Added

- **`lambda_config_dir` attribute** — Read per-lambda YAML config files (database.yml style) from a directory. Each file defines timeout, memory_size, env_vars, and DynamoDB table access per environment. YAML values are merged with the HCL `lambda_config` variable (HCL wins on conflicts).

- **`lambda_env_refs` attribute** — Flat map of reference names to Terraform-resolved values. YAML env_vars use `ref(name)` syntax to inject dynamic values (Cognito IDs, bucket names, etc.) without raw HCL in the YAML.

- **DynamoDB table-by-name in YAML** — Reference DynamoDB tables by logical name instead of ARN. The provider builds ARNs by convention (`arn:aws:dynamodb:{region}:{account}:table/{app}-{env}-{table}`). Supports shorthand (`users: [BatchGetItem]`) and expanded form with indexes.

- **DynamoDB permission shorthand** — Permission names auto-prefixed with `dynamodb:` if no colon is present (e.g., `BatchWriteItem` → `dynamodb:BatchWriteItem`).

- **Index support in YAML** — Nest indexes under their parent table with their own permissions:
  ```yaml
  dynamodb_tables:
    slots:
      permissions: [BatchWriteItem]
      indexes:
        SponsorIndex: [Query]
  ```

### Fixed

- Plan/apply hash consistency when using `lambda_config_dir` — Plan and Update now use the same `buildConfigFromModel` code path for config construction, eliminating hash drift.

## [0.0.3] - 2026-06-26

### Changed

- **Route parsing now uses `belt routes` CLI** — The provider no longer bundles embedded Ruby scripts for parsing `routes.tf.rb`. Instead, it delegates to `belt routes -f json`, which handles both route definitions and schema models. This ensures the provider stays compatible with Belt DSL changes without requiring provider releases.

### Removed

- Embedded Ruby scripts (`scripts/` directory, `embeddedscripts.go`, `internal/embedded/` package)
- `dsl-path` CLI subcommand (use `belt routes` directly)
- `RubyScriptPath` / `EmbeddedScriptsDir` from all internal config structs
- `.conveyor-belt/scripts/` output during plan/apply (no longer needed)

### Deprecated

- `ruby_script_path` provider attribute — kept in schema for state compatibility but ignored

### Requirements

- `belt` gem must be installed in the execution environment (`gem install belt`)

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
