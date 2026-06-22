# Changelog

All notable changes to the Terraform Provider Dispatcher will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.32.18] - 2026-06-15

### Fixed

- **SQS IAM policy not created for pre-existing Lambdas on provider upgrade** — The Update handler's IAM reconciliation block was missing the `CreateSQSPoliciesForAction` call (DynamoDB, S3, and SES were present but SQS was omitted). Additionally, Lambdas with `sqs_triggers` configured before v0.32.17 never entered the reconciliation set because their config hash hadn't changed. Now any Lambda with `sqs_triggers` is included in IAM reconciliation on every apply (`PutRolePolicy` is idempotent). (Fizzy #950)

## [0.32.17] - 2026-06-15

### Fixed

- **SQS event source mappings fail silently due to missing IAM permissions** — When `sqs_triggers` is configured in `lambda_config`, the Lambda execution role needs `sqs:ReceiveMessage`, `sqs:DeleteMessage`, and `sqs:GetQueueAttributes` permissions on the queue ARN for the event source mapping to function. The `TriggerManager` correctly created event source mappings but the IAM role was never granted the required SQS permissions. Added `CreateSQSPoliciesForAction` (matching the existing S3/SES pattern) and wired it into both Lambda creation and config update paths. (GitHub #22, Fixes #950)

## [0.32.16] - 2026-06-12

### Added

- **`lambda_gem_dirs` attribute** — Specifies directories (relative to `lambda_source_dir`) to copy into the Docker gem build context for path-based gems. Enables `gem 'my_gem', path: 'gems/my_gem'` in Gemfiles without publishing to rubygems.org.

  ```hcl
  resource "dispatcher" "main" {
    lambda_gem_dirs = ["gems/belt", "gems/s3arch"]
  }
  ```

- **Auto-detect `vendor/cache`** — If `lambda_source_dir/vendor/cache/` exists, it's automatically included in the Docker build context. Bundler uses cached `.gem` files before reaching out to the network. Zero configuration required.

- **Gem directory change detection** — Local gem source directories and `vendor/cache` are included in Lambda source hash calculations, so changes to path-based gem code correctly trigger rebuilds.

- **`mount` DSL method for mounting gem route manifests** — Allows external gems to declare their routes and have them mounted into a namespace. The mountable must respond to `.routes` and return an array of route definition hashes. Supports `at:` prefix, `tables:` merging, and `auth:` override. Path parameters in `:param` style are automatically converted to `{param}` format. Inherits scope and namespace defaults (tables, auth) like other route methods.

  ```ruby
  namespace :ops, auth: :cognito do
    mount S3arch::Routes, at: 'search', tables: [:s3arch_versions]
  end
  ```

## [0.32.15] - 2026-06-01

### Fixed

- **Invalid JSON in gateway error response templates** — `$context.error.messageString` already includes surrounding quotes when API Gateway substitutes it. Wrapping it in additional quotes produced invalid JSON (e.g., `"details": " "Invalid request body""`), causing frontend `JSON.parse()` failures. Removed the extra quotes in both the OpenAPI generator (dispatcher resource) and the imperative gateway operations (dispatcher_gateway resource).

## [0.32.14] - 2026-05-27

### Changed

- **Reduce structural duplication via shared helpers (~750 lines removed)** — Extracted repeated patterns into reusable functions across IAM, CloudWatch, naming, and parallel gateway operations:
  - `collectLambdaConfigFields()` — shared config extraction for hash computation, eliminating duplication between `calculateLambdaHash` and `calculateLambdaConfigHash`
  - `deleteInlinePolicy()` — generic IAM inline policy deletion replacing 5 identical per-policy-type functions
  - `deleteLogGroups()` — shared CloudWatch log group deletion for both Lambda and API Gateway log groups
  - `runGatewayOpsInParallel()` — generic parallel gateway operation runner replacing duplicated semaphore/goroutine/channel boilerplate
  - `validateName()` + `nameValidationRules` — data-driven name validation replacing 5 structurally identical validator functions
  - `createLambdaAlarm` + `alarmSpec` — consolidated CloudWatch alarm creation eliminating duplication across error, duration, throttle, and invocations alarms

- **Fix pre-existing test defaults in `parallel_route_processor`** — `DefaultRetryConfig` restored to 500ms initial / 15s max backoff; `getRouteProcessingConcurrency` defaults to 5 (cap 10) when config is nil or both fields ≤ 0.

## [0.32.13] - 2026-05-18

### Added

- **`gateway_names` and `lambda_names` plan-time known outputs** — New computed attributes on the `dispatcher` resource that expose gateway namespace names and lambda function names as plan-time known values. These are derived purely from parsing `routes.tf.rb` and scanning `lambda_source_dir` — no AWS API calls required. Consumers can now use `toset(module.dispatcher.gateway_names)` in `for_each` without hitting "keys derived from resource attributes that cannot be determined until apply" errors. Previously, using `module.dispatcher.api_gateway_ids` as a `for_each` argument would fail whenever the dispatcher resource was changing, because Terraform treated the entire map (keys and values) as unknown.

## [0.32.12] - 2026-05-12

### Fixed

- **GetRestApis pagination bug — gateways beyond position 25 invisible to update/delete/read** — The `updateSingleGateway`, `deleteSingleGateway`, and `readSingleGateway` functions called `GetRestApis` without specifying a `Limit`, which defaults to 25 results from the AWS API. Accounts with more than 25 API Gateways could not find gateways beyond the first page, causing "API Gateway not found" warnings during apply while silently skipping route updates. Refactored all three call sites to use the existing `findExistingApiGateway` function which correctly sets `Limit: 500`.

## [0.32.11] - 2026-05-12

### Fixed

- **Lambda update failures silently swallowed — state written without applying changes** — When `UpdateFunctionConfiguration` failed (e.g., due to the AWS 4KB environment variable limit), the error was logged as a warning and state was written as if the update succeeded. This caused persistent drift where Terraform believed env vars were set but AWS didn't have them. Lambda update failures are now treated as errors, preventing state from being written for failed operations.

- **Env var drift detection during Read** — The provider now reads actual Lambda environment variables from AWS during the Read/Refresh phase and compares them with expected values. If drift is detected (e.g., from a previously swallowed error), the config hash is cleared from state so the next plan triggers a corrective update.

### Added

- **`types.Dynamic` unwrapping in type extraction helpers** — Added `types.Dynamic` handling to `extractMapValue`, `extractStringValue`, `extractInt64Value`, `convertToStableList`, `convertToStableValue`, and `extractStringSlice`. While not the root cause of this specific bug, this hardens the provider against future issues with Terraform's `DynamicAttribute` type wrapping.

## [0.32.10] - 2026-05-07

### Fixed

- **`simple_singularize` produces `shipping_boxe` instead of `shipping_box`** — Words ending in `-xes`, `-zes`, or `-ses` were incorrectly singularized when `simple_singularize` was called from within the `normalize_path` gsub block. The multi-argument `end_with?('ses', 'xes', 'zes')` check was being skipped, falling through to the generic `end_with?('s')` branch which only strips one character. Split into separate `elsif` branches. Also fixed the naive `singularize` in `route_dsl.rb` and `table_inference.rb` which had the same gap.

## [0.32.9] - 2026-05-06

### Fixed

- **First-time deploy fails with "Invalid model identifier specified: null"** — When deploying to a brand-new environment, API Gateway rejected the OpenAPI spec because request validation models weren't being included. Three issues were fixed:
  1. **Schema DSL missing `list` type** — `schema.tf.rb` files using `list` in response model contexts caused a silent `NoMethodError`, preventing all models from loading. Added `list` as an alias for `array` in the schema DSL.
  2. **Schema file not reliably found** — The Ruby script's auto-detection of `schema.tf.rb` failed when Terraform modules used relative paths across directories. The provider now resolves the schema path in Go and passes it explicitly via `--schema` flag.
  3. **API Gateway model registration** — Added `title` field to model schemas and a `definitions` section (Swagger 2.0 style) to the generated OpenAPI spec, ensuring API Gateway creates named Model resources during import.

## [0.32.8] - 2026-05-06

### Added

- **`suppress_table_env_vars` attribute** — When set to `true` on `dispatcher` or `dispatcher_lambda` resources, suppresses generation of individual `*_TABLE_NAME` environment variables and the `TABLES` comma-separated list on Lambda functions. IAM policies from `tables:` arrays are still created. Use this when Lambda code derives table names from `APP_NAME` and `ENVIRONMENT` (e.g., via `DynamoRecord::Base#default_table_name`). Frees env var space for Lambdas approaching the 4KB limit.

### Fixed

- **`stripVendorFat` too aggressive — breaks gem autoloading** — The vendor bundle stripping removed `.gemspec` files, `.md`/`.txt` documentation, `Gemfile`s, and files matching prefix patterns (`LICENSE*`, `README*`, etc.) from inside the vendor bundle. This broke gems like `activesupport` that rely on gemspecs for require path resolution and may ship required non-Ruby files. Now only removes: test/spec/doc directories at gem root level, C source artifacts (`.c`, `.h`, `.o`), `.rdoc` files, and build tool files (`Makefile`, `Rakefile`, CI configs). Native `.so` extensions are still stripped of debug symbols.

## [0.32.7] - 2026-04-26

### Fixed

- **Inconsistent plan when hash inputs are unknown** — Fix provider-produced inconsistent final plan when hash inputs are unknown during plan phase. (#15)

## [0.32.6] - 2026-04-26

### Fixed

- **Stale `-invoke` shared policies not detached during reconciliation** — `ReconcileSharedIamPolicies` included `-invoke` and `-cognito-access` in its provider-managed suffix skip list, but the provider never creates policies with those suffixes. User-managed shared policies ending in `-invoke` (e.g., `worker-invoke`) were incorrectly skipped, preventing detachment and leaving roles at the 10-policy AWS limit. Removed `-invoke` from the skip list and corrected `-cognito-access` to `-cognito-policy` to match the actual provider-created suffix. (GitHub #14)

## [0.32.5] - 2026-04-26

### Fixed

- **IAM policy quota exceeded when shared policies change** — When `shared_iam_policy_arns` changes (e.g., consolidating multiple policies into one), the provider attached new policies before detaching old ones. If a role was already at the AWS limit of 10 managed policies, this caused `LimitExceeded` errors. Now calls `ReconcileSharedIamPolicies` (detach stale) before `AttachSharedIamPolicies` (attach new) in both the parallel Lambda update path and the dispatcher resource IAM reconciliation path. (GitHub #13)

## [0.32.4] - 2026-04-26

### Fixed

- **Inconsistent `lambda_config_hashes` between plan and apply when routes change** — `ModifyPlan` used `calculateAllLambdaConfigHashes` (which pre-filters routes by lambda) while `Update`/`Create` used `calculateSeparateLambdaHashes` (which passed all routes). Although both should produce identical results, the differing code paths caused Terraform to reject the apply with "Provider produced inconsistent final plan." All hash computation now flows through the same `calculateAll*` functions, guaranteeing plan/apply consistency. (GitHub #11)

## [0.32.3] - 2026-04-22

### Fixed

- **Model hash drift not triggering gateway update when routes are unchanged** — v0.32.2's Read function correctly detects stale models and clears their hashes from state, but ModifyPlan's "routes unchanged" branch only compared lambda hashes. It never checked model hashes, so Plan reported "No changes" even after Read cleared a drifted model hash. The routes-unchanged branch now computes expected model hashes and compares them against state, triggering a `PutRestApi` redeployment for any gateway with model drift.

## [0.32.2] - 2026-04-22

### Fixed

- **Detect model hash state drift in Read** — v0.32.0 could write model hashes to state without actually deploying them via `PutRestApi`, poisoning the state. Subsequent versions (including v0.32.1) trusted the state hashes and saw no diff, so models were never deployed. The Read function now fetches actually deployed models from each API Gateway via `GetModels`, computes a fingerprint, and compares it against what the routes/schema files expect. If drift is detected, the affected model hash is cleared from state so the next `terraform plan` triggers a `PutRestApi` update. No taint or manual intervention required.

## [0.32.1] - 2026-04-22

### Fixed

- **Model-only schema changes not triggering API Gateway redeployment** — When request/response models were updated in `schema.tf.rb` without any route changes, `detectGatewayChanges` would not flag the gateway for update because it only compared gateway hashes (computed from route definitions). Model hashes were tracked in state but never compared during the Update lifecycle. The gateway hash stayed the same, so `PutRestApi` was never called, leaving stale models deployed. Now compares both gateway hashes and model hashes, so schema-only changes correctly trigger redeployment.

## [0.32.0] - 2026-04-22

### Fixed

- **Phantom Lambda rebuilds from non-deterministic hash computation** — Lambda functions were being rebuilt and updated on every `terraform apply` even when no source code or configuration had changed. Three root causes were identified and fixed:
  - **nil vs empty slice serialization**: `json.Marshal` produces `"null"` for nil slices but `"[]"` for empty slices, causing hash instability between plan and apply phases. All slice fields are now normalized to empty slices before hashing.
  - **Non-source files in hash computation**: `hashDirectoryContents` included all files (`.DS_Store`, editor swap files, dotfiles), causing hashes to change based on OS/editor artifacts. Now filters to Ruby source files only (`.rb`, `.erb`, `.yml`, `.yaml`, `.json`, `Gemfile`, `Gemfile.lock`).
  - **Unhandled `types.Int64` in `convertToStableValue`**: Raw Terraform framework types leaked into hash computation, producing non-deterministic serialization. Added handling for `types.Int64`.

### Migration Note

The first `terraform plan` after upgrading will show changes for all Lambdas due to one-time hash recomputation. Subsequent plans will be stable.

## [0.31.1] - 2026-04-21

### Fixed

- **Deadlock during IAM policy reconciliation when `docker_build_concurrency` is not set** — The IAM reconciliation step after parallel Lambda updates created a zero-capacity semaphore channel when `docker_build_concurrency` was unset (defaulting to 0), causing `terraform apply` to hang indefinitely. The same bug existed in the delete-reconciliation path. Both now fall back to `runtime.NumCPU()` when the value is ≤ 0, matching the behavior of `ParallelManager`.

## [0.31.0] - 2026-04-21

### Changed

- **API Gateway deployment rewritten to use OpenAPI 3.0.1 import** — The `dispatcher` resource now deploys API Gateways via `PutRestApi` with a generated OpenAPI 3.0.1 spec instead of imperative API calls (`CreateResource`, `PutMethod`, `PutIntegration`, etc.). This is a complete rewrite of the gateway deployment pipeline:
  - `OpenAPIGenerator` builds a full spec per gateway (routes, CORS, auth, models, Lambda integrations, gateway responses, request validators)
  - `openapi_deployer.go` deploys specs via `PutRestApi` with `mode=overwrite`
  - Hash-based change detection — only redeploys gateways whose spec hash changed
  - Specs written to `.dispatcher/openapi/{gateway}.json` for inspection
  - The `dispatcher_gateway` resource is unchanged and still uses imperative API calls

- **~3,600 lines of imperative gateway code removed** — Deleted `model_manager.go`, `cors_options_fix_test.go`, and imperative deployment branches from `parallel_manager.go`, `api_gateway_operations.go`, and `dispatcher_resource.go`. The `use_imperative_deploy` flag has been removed.

- **`terraform plan` now generates `.dispatcher/` artifacts** — Running `terraform plan` writes OpenAPI specs to `.dispatcher/openapi/` and embedded Ruby scripts to `.dispatcher/scripts/`. No AWS mutations occur — this is pure computation. Useful for inspecting specs and for external tools that need the DSL scripts without a full deploy.

### Fixed

- **400 Bad Request on GET routes with request models** — `requestBody` is now only added to POST/PUT/PATCH methods, not GET/DELETE.

- **PutRestApi error: model names must be alphanumeric** — Snake_case model names from the schema DSL are now converted to PascalCase before inclusion in the OpenAPI spec.

- **`undefined method 'object'` in schema DSL** — Added `object` to `SUPPORTED_TYPES` in both `RequestModelBuilder` and `ContextBuilder`. Both `object` and `map` now produce `type: "object"` in JSON Schema output.

- **`undefined method 'string'` for ResponseModelBuilder** — Added type methods (`string`, `integer`, `boolean`, `array`, `object`, `map`, `number`) to `ResponseModelBuilder` for context-less response model definitions.

- **Stale temp directory causing LoadError for embedded scripts** — `isExtractionIntact()` now validates file integrity, not just directory existence. `EnsureScriptsIntact()` re-validates before every Ruby invocation.

- **`.dispatcher/openapi/` written to wrong directory** — Specs are now written relative to `filepath.Dir(config.LambdaSourceDir)` (the project root) instead of the Terraform working directory.

- **Docker build failure: bigdecimal native extension** — Added `-e HOME=/tmp` to Docker commands and copy `Gemfile.lock` alongside `Gemfile` for reproducible builds.

- **Slow deploys: IAM reconciliation running for all lambdas** — IAM reconciliation now runs in parallel and is skipped entirely for source-only lambda updates. Unchanged lambdas skip IAM, models, and base path mapping work.

- **All gateways/lambdas marked as changed when only one route changes** — `ModifyPlan` now computes actual per-gateway and per-lambda hash values instead of marking entire maps as unknown.

- **"Provider produced inconsistent result" for `openapi_spec_hashes`** — Hash state updates are now gated on `plan.IsUnknown()` to prevent inconsistent results after apply.

- **Precise `openapi_spec_hashes` in plan** — Only changed gateways are marked as unknown in the plan, not the entire map.

### Added

- **`openapi_spec_hashes` computed attribute** — Map of gateway name → OpenAPI spec hash for change detection.

- **Embedded script extraction to `.dispatcher/scripts/`** — External deploy tools can reference scripts at a stable path without needing a provider repo checkout.

## [0.30.2] - 2026-04-15

### Fixed

- **Gateway deletion fails when base path mappings exist** — When a gateway was removed from `routes.tf.rb` and a `custom_domain_name` was configured, `Update` attempted to delete the REST API before removing its base path mappings. AWS rejects `DeleteRestApi` while mappings still reference the API, causing a "Please remove all base path mappings" error. Base path mappings for deleted gateways are now removed before calling `DeleteGatewaysInParallel`. Both the plan and state custom domain names are checked to handle domain changes correctly.

- **"Provider produced inconsistent result" after gateway deletion** — After a partial apply failure (e.g., the base path mapping error above), `ModifyPlan` would fail to detect that a gateway needed to be removed because it only checked `gateway_hashes` from state. If the previous apply updated hashes but failed to delete the gateway, the stale key remained in `api_gateway_ids`/`api_gateway_urls` but was absent from `gateway_hashes`, so `ModifyPlan` didn't mark those maps as unknown. Terraform then rejected the apply because map keys vanished unexpectedly. `ModifyPlan` now checks both `gateway_hashes` and `api_gateway_ids` (and both `lambda_hashes` and `lambda_functions`) when detecting removed resources.

## [0.30.1] - 2026-04-14

### Added

- **`--output-dir` flag for `list_routes.rb --ruby-output`** — New `--output-dir DIR` option overrides the default output directory (`../lambda/lib/routes/` relative to the script) when generating Ruby route manifest files. This allows consumer projects to write generated files directly to their own `lambda/lib/routes/` directory without copying from the provider repo. The flag is only meaningful when `--ruby-output` is also specified. Backward compatible — omitting `--output-dir` preserves existing behavior.

## [0.30.0] - 2026-04-13

### Added

- **API Gateway Model and Request Validator support** — New `schema_source` attribute on the `dispatcher` resource accepts a `schema.tf.rb` Ruby DSL file that defines request/response models. The provider creates API Gateway Models (JSON Schema) and a shared Request Validator (`dispatcher-body-validator`), then attaches them to POST/PUT/PATCH methods for runtime payload validation at the gateway level before Lambda is invoked. Response models are also wired using constructed `{model}_{context}_response` names. A new `model_hashes` computed attribute enables change detection for schema definitions.
  - New `ModelManager` component handles full Model CRUD and Request Validator lifecycle
  - Models are synced during both Create and Update flows via shared `syncModelsForGateways()` method
  - Orphaned models (not in current schema) are automatically deleted (built-in `Empty` and `Error` models are preserved)
  - Snake_case model names are automatically converted to PascalCase for API Gateway compliance

- **VPC configuration support for Lambda functions** — New `vpc_config` block in `lambda_config` per-lambda with `subnet_ids` and `security_group_ids`. When present, the provider passes `VpcConfig` to both `CreateFunctionInput` and `UpdateFunctionConfigurationInput`, and automatically attaches the `AWSLambdaVPCAccessExecutionRole` managed policy to the Lambda's IAM role for ENI management.

- **Embedded Ruby DSL scripts** — The provider binary now embeds all Ruby DSL scripts (`scripts/lib/route_dsl.rb`, `schema_dsl.rb`, `table_inference.rb`, `terra_dispatch.rb`, etc.) via `go:embed`. When `ruby_script_path` is not set, the provider auto-extracts embedded scripts to a content-hashed temp directory for idempotent reuse. This eliminates the need for consumers to have a repo checkout or manage script paths manually.

- **`dsl-path` subcommand** — The provider binary now supports `terraform-provider-dispatcher dsl-path`, which extracts the embedded Ruby DSL scripts to a temp directory and prints the path. Consumer scripts (`deploy.sh`, `generate_openapi.rb`, `compare-routes.rb`) use this to locate `list_routes.rb` and `lib/` without needing a repo checkout.

- **DSL version constants and mismatch detection** — Embedded scripts include `DSL_VERSION` constants. When a consumer's route file uses features from a newer DSL version, the provider emits STDERR warnings and handles `NoMethodError` from version mismatches gracefully.

### Changed

- **Reduced API Gateway rate limiting** — Default route processing concurrency lowered from 5 to 2 (cap from 10 to 5). Initial backoff increased from 500ms to 1s, max backoff from 15s to 30s. This reduces throttling errors on AWS accounts with lower API Gateway rate limits.

- **Go version bumped to 1.25.4**

### Fixed

- **Deployment failure on retry after partial gateway creation** — When a first `terraform apply` fails partway through creating API Gateways (e.g. due to rate limiting), the second apply would adopt the orphaned gateways but fail with "No integration defined for method" because methods from the first run existed without their Lambda integrations attached. The deployment section in `createSingleGateway` now calls `repairMethodsWithoutIntegrations` before the first deployment attempt to clean up dirty state, and on "No integration defined" errors it repairs and retries (up to 5 attempts) instead of immediately failing.

- **Pagination bugs in model listing and request validator lookup** — `listModels()` and `EnsureRequestValidator()` were not following the `Position` pagination token, so APIs with more than one page of models could miss existing models (creating duplicates) or fail to find an existing validator (creating duplicates). Both methods now loop through all pages.

- **Model wiring fixes** — `AttachModelToMethod` changed from `Op:Replace` to `Op:Add` to fix `NotFoundException` on new methods. `AttachResponseModelToMethod` switched from `PutMethodResponse` to `UpdateMethodResponse` PATCH to fix 409 Conflict on existing 200 responses (falls back to Put if 200 is missing). Request model attachment is now filtered to POST/PUT/PATCH only (skipping GET/DELETE).

- **Plan change detection failure from premature script cleanup** — `Read()` was calling `defer Cleanup()` which deleted the embedded scripts temp directory before `ModifyPlan()` could use them. `ModifyPlan` silently failed and reported no changes, even when `routes.tf.rb` had been modified. Removed the cleanup from Read.

- **Dropped `active_support` Ruby dependency** — Replaced the `active_support/core_ext/string/inflections` dependency in `list_routes.rb` with a built-in `simple_singularize` method. The gem isn't available in all Ruby environments and caused silent failures when missing.

## [0.24.0] - 2026-03-11

### Fixed

- **Phantom changes on every `terraform plan`** — Fixed a bug where every plan showed all Lambdas as modified even with zero file changes, causing unnecessary `(known after apply)` diffs across all computed attributes.
  - Root cause 1: `ModifyPlan` passed `nil` for `alarmConfig` when calculating lambda hashes, but `Update` passed the actual `config.AlarmConfig`. Any user with alarm configuration would get different hashes between plan and apply, triggering a perpetual update cycle.
  - Root cause 2: Env var values from `lambda_config` were stored as `types.String` (Terraform framework objects) in hash structs. `json.Marshal` serialized these as complex internal structs rather than plain strings, producing non-deterministic output between plan and state contexts. Added `stabilizeEnvVarsForHashing()` to convert to plain `map[string]string` before hashing.
  - Root cause 3: Computed attributes (`lambda_functions`, `api_gateway_ids`, `api_gateway_urls`, `routes_hash`, `gateway_hashes`, etc.) lacked `UseStateForUnknown()` plan modifiers. Terraform defaulted them to "unknown" during plan, and any `resp.Plan.Set()` call propagated those unknowns back, causing cascading `(known after apply)` diffs.
  - Fixed default `lambda_shared_dirs` mismatch between `ModifyPlan` and `calculateLambdaSourceHash`.
  - Removed leftover `fmt.Printf` debug logging from `calculateConfigHash`.

## [0.24.0-beta] - 2026-02-28

### Changed

- **Shared gem bundle build** — Docker `bundle install` now runs once and the resulting `vendor/bundle` is copied into each Lambda package. Previously, every Lambda triggered its own Docker build. For a project with N Lambdas, this reduces Docker invocations from N to 1.
- **Precise source/config change detection** — The provider now stores separate `lambda_source_hashes` and `lambda_config_hashes` in state. When only configuration changes (env vars, timeout, memory, IAM policies, triggers), Docker builds are skipped entirely. Only source code changes trigger package rebuilds.
- **Parallel IAM reconciliation** — Both IAM reconciliation passes in Update (shared policy detach and per-lambda policy sync) now run concurrently instead of sequentially.
- **Reduced AWS API calls during Lambda updates** — `updateSingleLambda` now receives the existing Lambda ARN from state instead of calling `GetFunction` up to 3 times per update. Falls back to API call only when ARN is unavailable.
- **Shared directory caching** — Shared directories (models, lib, helpers, templates) are copied once to a temp cache and reused across all Lambda package builds instead of being copied from source per-Lambda.

## [0.23.20] - 2026-03-03

### Fixed

- **Deadlock in parallel IAM reconciliation** — Fixed a deadlock caused by `make(chan struct{}, config.DockerBuildConcurrency)` where `DockerBuildConcurrency` defaults to 0 when not explicitly set. A zero-capacity semaphore channel blocks all goroutines permanently. Converted both IAM reconciliation passes (shared policy reconciliation and per-lambda policy sync) to sequential execution since they're lightweight AWS API calls that don't benefit from parallelism.

- **Stale shared IAM policies not detached (state-independent reconciliation)** — Fixed a bug where removing policies from `shared_iam_policy_arns` would fail with `DeleteConflict: Cannot delete a policy attached to entities` on subsequent applies. The previous detach logic compared old vs new state, but after a partially successful apply, Terraform state already contained the new policy list — making old and new identical, so nothing was detached.
  - Fix: Added `ReconcileSharedIamPolicies` to `IAMManager` which queries AWS directly via `ListAttachedRolePolicies` for each Lambda role and detaches any policy that is not in the current `shared_iam_policy_arns` config, not an AWS managed policy, and not a known provider-managed policy (identified by suffixes like `-dynamodb-policy`, `-invoke`, `-cognito-access`). This runs early in Update before package building to ensure stale policies are detached before Terraform's concurrent `aws_iam_policy` deletes execute.

## [0.23.18] - 2026-02-27

### Fixed

- **Orphaned Lambda roles block `shared_iam_policy_arns` policy deletion** - Fixed a bug where changing `shared_iam_policy_arns` (e.g., consolidating multiple policies into fewer ones) would fail with `DeleteConflict` because old policies were still attached to orphaned IAM roles not tracked in Terraform state.
  - Root cause: Previous provider versions created IAM roles with random suffixes (e.g., `-p92ypz`). These roles accumulated over time and were never cleaned up. When `shared_iam_policy_arns` changed, the reconciliation loop only detached policies from roles in the current Terraform state, leaving orphaned roles still referencing the old policies.
  - Fix: Added `ReconcileOrphanedRoles` to `IAMManager` which uses the IAM `ListRoles` API to discover all roles matching the `{app_name}-{environment}-*-lambda-role` pattern, then reconciles shared policies on any that aren't in the current known set. This runs early in Update before package building, ensuring stale policies are detached before Terraform's concurrent `aws_iam_policy` deletes execute.

## [0.23.17] - 2026-02-25

### Fixed

- **`timeout` and `memory_size` ignored for standalone Lambdas in `lambda_config`** - Fixed a bug where `timeout` and `memory_size` specified for standalone Lambdas (those not tied to an API Gateway namespace) were silently ignored. Deployed Lambdas always retained provider defaults (`default_lambda_timeout`, `default_lambda_memory`) regardless of per-lambda config.
  - Root cause 1: `extractInt64Value` did not handle `types.Number` or `types.Int64` (Terraform Plugin Framework types), so timeout/memory values read from Terraform state were always discarded, causing `getTimeoutAndMemory` to fall back to provider defaults.
  - Root cause 2: `calculateLambdaHash` and `calculateLambdaConfigHash` did not include `timeout` or `memory` in the hash, so changes to these fields never triggered an update.
  - Fix: Added `types.Number` and `types.Int64` cases to `extractInt64Value`, and included `Timeout` and `Memory` fields in both hash structs so config changes are detected and applied correctly.

## [0.23.16] - 2026-02-20

### Fixed

- **`shared_iam_policy_arns` not reconciled on existing Lambda roles** - Fixed a bug where adding new IAM policy ARNs to `shared_iam_policy_arns` would update Terraform state but never actually attach the policies to existing Lambda IAM roles in AWS. Only newly created Lambdas received the shared policies.
  - Root cause: `AttachSharedIamPolicies` was only called during Lambda creation (`createSingleLambda`), never during updates. The Update function's IAM reconciliation loop handled DynamoDB, S3, and SES policies but omitted shared IAM policies entirely.
  - Fix: Added `AttachSharedIamPolicies` to the existing IAM reconciliation loop in the dispatcher Update path, which runs for all managed Lambda roles on every apply. `AttachRolePolicy` is idempotent so this is safe.

## [0.23.15] - 2026-02-06

### Fixed

- **CloudWatch alarm configuration not updating** - Fixed a bug where changes to `alarm_config` (e.g., `error_period`, `error_threshold`, `duration_threshold`) were silently ignored during `terraform apply`. Existing alarms retained their original values indefinitely.
  - Root cause 1: `buildConfigFromModel` never extracted the `alarm_config` Terraform attribute into `config.AlarmConfig`, so it was always `nil`. The `CloudWatchManager` would skip alarm operations entirely ("Alarms not configured").
  - Root cause 2: `calculateLambdaHash` used `lambdaConfig["alarm_config"]` (always nil, since alarm_config is a top-level attribute, not inside lambda_config) instead of the `alarmConfig` parameter passed to it. This meant alarm config changes never affected the hash, so no lambdas were flagged for update.
  - Root cause 3: `updateSingleLambda` in `ParallelManager` never called `CreateOrUpdateLambdaAlarms` — that call only existed in the create path.
  - Fix: Added `extractAlarmConfig` to parse the Terraform attribute into the `AlarmConfig` struct, fixed the hash to use the actual `alarmConfig` parameter, and added `CreateOrUpdateLambdaAlarms` calls to both the `ParallelManager` and `lambdaResource` update paths.

## [0.23.14] - 2026-02-03

### Fixed

- **Race condition in OPTIONS method creation** - Fixed a race condition where multiple routes sharing the same API Gateway resource (e.g., `GET /containers/{id}/contents` and `POST /containers/{id}/contents`) could interfere with each other during parallel OPTIONS method creation. This could leave OPTIONS methods in an incomplete state (missing integration response), causing 500 errors on CORS preflight requests.
  - Root cause: When multiple routes share the same path, they all try to create OPTIONS methods for the same resource ID concurrently. The check-then-create pattern was not atomic, allowing race conditions.
  - Fix: OPTIONS method creation now uses the thread-safe resource cache's `GetOrCreate` pattern to coordinate concurrent creation attempts. Only one goroutine creates the OPTIONS method while others wait for completion.
  - Impact: Member routes inside `resources` blocks (like `get '/contents'` inside `member do`) will now reliably have complete OPTIONS configuration.

## [0.23.11] - 2026-02-02

### Added

- **Friendly error messages for missing routes** - New `friendly_errors` attribute enables detailed, developer-friendly error messages for API Gateway routing errors in non-production environments. When enabled, 404 errors include the attempted path, HTTP method, environment name, and helpful hints for debugging.
  - Route not found: "The API route you are trying to access does not exist. Check the HTTP method and path."
  - Wrong HTTP method: "This route exists but doesn't support the HTTP method you used."
  - Invalid JSON: "The request body is invalid or malformed JSON."
  - Configurable per-resource: Set `friendly_errors = true` for dev/uat/staging, `false` for production
  - Gateway response types configured: MISSING_AUTHENTICATION_TOKEN, RESOURCE_NOT_FOUND, BAD_REQUEST_BODY, UNAUTHORIZED, ACCESS_DENIED, DEFAULT_4XX, DEFAULT_5XX
  - All error responses include proper CORS headers

### Changed

- Gateway response configuration now includes response templates with environment-aware error messages
- `ConfigureGatewayResponses()` function enhanced to support friendly error templates based on `FriendlyErrors` config setting

## [0.23.10] - 2026-02-02

### Fixed

- **CORS errors on API Gateway auth failures** - Fixed a critical bug where browser-based frontends received CORS errors when API Gateway returned 401/403 responses from Cognito authorizer failures. The provider now automatically configures gateway responses with CORS headers for UNAUTHORIZED, ACCESS_DENIED, DEFAULT_4XX, and DEFAULT_5XX response types.
  - Root cause: When API Gateway's Cognito authorizer rejects a request, the rejection happens before Lambda is invoked, so Lambda's CORS headers are never added. Browsers then block the response due to missing `Access-Control-Allow-Origin` header.
  - Fix: Added `ConfigureGatewayResponses()` function in `api_gateway_operations.go` that creates gateway responses with proper CORS headers. Called during both gateway creation and updates.
  - Impact: Existing API Gateways will get CORS headers on error responses on next `terraform apply`. New gateways get them automatically.

### Added

- **Gateway response CORS configuration** - New `ConfigureGatewayResponses()` and `DeleteGatewayResponses()` functions in API Gateway operations for managing gateway-level CORS headers on error responses.

## [0.23.8] - 2026-01-30

### Fixed

- **OPTIONS method creation errors now properly reported** - Fixed a bug where errors during CORS OPTIONS method creation were silently ignored during parallel route processing. This could leave routes with incomplete OPTIONS configuration (missing integration response), causing 500 errors on preflight requests. Errors are now properly returned and logged with phase="cors".
- **Lambda permission errors now properly reported** - Enhanced error handling and logging when adding API Gateway invoke permissions to Lambda functions. Missing Lambda ARNs and permission failures are now logged as errors and returned to Terraform, preventing silent failures that could cause 500 errors.

### Added

- **Troubleshooting guide for OPTIONS 500 errors** - Added `docs/TROUBLESHOOTING_OPTIONS_500.md` with diagnosis steps and manual fix commands for incomplete CORS configuration.

### Notes

- The existing `createCorsOptionsMethod` already checks for and repairs missing integration responses during route processing. The fix ensures these repairs are properly reported if they fail.

## [0.23.7] - 2026-01-28

### Added

- **SNS/SQS Trigger Lifecycle Management** - Full create/update/delete lifecycle for SNS and SQS triggers. Previously, triggers were only set up during Lambda creation. Now triggers are reconciled during every `terraform apply`:
  - **SNS Triggers**: Creates Lambda permissions and SNS subscriptions when added, updates statement_id when changed, removes permissions and unsubscribes when deleted
  - **SQS Triggers**: Creates event source mappings when added, updates batch_size/enabled in-place when changed, deletes mappings when removed
  - **Orphan Cleanup**: Triggers in AWS but not in config are automatically deleted
  - **Idempotent Operations**: Safe to retry on transient failures
  - **Partial Failure Handling**: Individual trigger failures don't block other triggers

- **TriggerManager component** (`internal/resources/trigger_manager.go`) - New manager for trigger lifecycle operations:
  - `ReconcileTriggers()` - Main entry point called during Lambda updates
  - `ReconcileSNSTriggers()` / `ReconcileSQSTriggers()` - Protocol-specific reconciliation
  - Exponential backoff retry for transient AWS errors
  - Comprehensive logging for debugging

- **Trigger change detection** - Trigger configuration changes are now detected via hash calculation:
  - `sns_triggers` and `sqs_triggers` included in Lambda config hash
  - Trigger-only changes result in config update (no code redeployment)

- **New example** (`examples/with-triggers/`) - Complete example demonstrating event-driven architecture with SNS and SQS triggers

- **Trigger lifecycle documentation** (`docs/TRIGGER_LIFECYCLE.md`) - Comprehensive guide covering configuration, lifecycle operations, error handling, and troubleshooting

### Changed

- `ParallelManager.updateSingleLambda()` now calls `TriggerManager.ReconcileTriggers()` during config updates
- `ParallelManager.deleteSingleLambda()` now cleans up triggers before Lambda deletion
- Updated steering files and POWER.md with trigger lifecycle management documentation

## [0.23.6] - 2026-01-25

### Fixed

- **Critical: DynamoDB policies from lambda_config not applied** - The `buildConfigFromModel()` function was not populating `config.LambdaConfig`, causing `IAMManager.parseDynamoDBTablesFromConfig()` to return early without creating DynamoDB policies from `lambda_config.{action}.dynamodb_tables`.
  - Root cause: While `updateSingleLambda()` was correctly calling `CreateDynamoDBPoliciesForAction()`, the `IAMManager` was initialized with a config that had `LambdaConfig = nil`.
  - Fix: Added `LambdaConfig` population to `buildConfigFromModel()` so the IAMManager has access to the full lambda configuration.
  - Impact: Running `terraform apply` will now correctly create DynamoDB policies for standalone Lambdas that use `dynamodb_tables` in their `lambda_config`.

## [0.23.5] - 2026-01-25

### Fixed

- **Critical: DynamoDB policies not created during Lambda config updates** - When `lambda_config.{action}.dynamodb_tables` was added or modified on an existing Lambda, the IAM policy was never created, causing `AccessDeniedException` errors. This affected in-place upgrades while fresh deployments worked correctly.
  - Root cause: `updateSingleLambda()` in `parallel_manager.go` only updated Lambda code and configuration (env vars, timeout, memory, layers) but never touched IAM policies.
  - Fix: Added call to `CreateDynamoDBPoliciesForAction()` during config updates. The `PutRolePolicy` API is idempotent, so it safely creates or updates the policy.
  - Impact: Running `terraform apply` will now automatically create missing DynamoDB policies for Lambdas that were updated in-place.

## [0.23.4] - 2026-01-21

### Fixed

- **CORS OPTIONS method repair** - Fixed incomplete OPTIONS methods that had MOCK integration but were missing method response and integration response, causing 500 errors on CORS preflight requests. The provider now detects and repairs these incomplete OPTIONS methods during gateway updates.

## [0.23.2] - 2026-01-21

### Fixed

- **Critical: Lambda permissions for API Gateway invocation** - The `dispatcher` resource was not creating `aws_lambda_permission` resources, causing API Gateway to return 500 errors because it couldn't invoke Lambda functions. This affected UAT and prod environments while dev02 worked (likely had permissions created manually or from an older version).
  - Root cause: `ParallelManager.createSingleGateway()` and `updateSingleGateway()` were missing the Lambda permission creation that existed in the standalone `dispatcher_gateway` resource.
  - Fix: Added `addLambdaPermissionsForGateway()` function to `ParallelManager` that creates `lambda:InvokeFunction` permissions for each Lambda function used by API Gateway routes.
  - Permissions are now created during both gateway creation and updates, ensuring they stay in sync.

## [0.22.0] - 2026-01-12

### ⚠️ BREAKING CHANGES

This release introduces terminology changes and new custom domain support. **Route JSON format has changed.**

#### Terminology Changes

The `controller` and `action` fields have been renamed to better reflect AWS resources:

| Old Field    | New Field | Description                                            |
| ------------ | --------- | ------------------------------------------------------ |
| `controller` | `gateway` | Determines which API Gateway handles the route         |
| `action`     | `lambda`  | Determines which Lambda function processes the request |

**Route JSON format change:**

```json
// Before (v0.21.x)
{
  "name": "containers_index",
  "verb": "GET",
  "path": "/ops/containers",
  "controller": "ops",
  "action": "ops"
}

// After (v0.22.0+)
{
  "name": "containers_index",
  "verb": "GET",
  "path": "/ops/containers",
  "gateway": "ops",
  "lambda": "ops"
}
```

#### Output Attribute Changes

| Old Attribute       | New Attribute    |
| ------------------- | ---------------- |
| `api_gateway_ids`   | `gateway_ids`    |
| `api_gateway_urls`  | `gateway_urls`   |
| `controller_hashes` | `gateway_hashes` |

### Added

- **Custom domain support** via `custom_domain_name` attribute
  - Automatically creates base path mappings for each gateway
  - Routes traffic from unified domain to appropriate API Gateways
  - Example: `api.example.com/ops/*` → ops API Gateway

- **New output attributes** for custom domain:
  - `custom_domain_url` - Base URL (e.g., `https://api.example.com`)
  - `base_path_mappings` - Map of gateway names to base paths

- **BasePathMappingManager** component for managing AWS base path mappings
  - Creates, updates, and deletes mappings automatically
  - Validates custom domain exists before creating mappings
  - Handles conflicts and provides clear error messages

- **Custom domain setup guide** at `docs/CUSTOM_DOMAIN_SETUP.md`
  - Complete Terraform code for ACM certificate and custom domain
  - Verification steps and troubleshooting guide

### Changed

- Renamed internal functions to use new terminology:
  - `GroupByController` → `GroupByGateway`
  - `GroupByAction` → `GroupByLambda`
  - `GetUniqueControllers` → `GetUniqueGateways`
  - `GetUniqueActions` → `GetUniqueLambdas`
  - `GetTablesForAction` → `GetTablesForLambda`
  - `GetControllersWithAuth` → `GetGatewaysWithAuth`

- Updated all log messages to use `gateway` and `lambda` terminology

- Updated README with new terminology and migration guide

### Migration Guide

See the [Migration Guide](README.md#migration-guide) in the README for step-by-step instructions.

**Quick migration:**

1. Update route JSON to use `gateway` instead of `controller`
2. Update route JSON to use `lambda` instead of `action`
3. Update Terraform references from `api_gateway_*` to `gateway_*`
4. Update Terraform references from `controller_hashes` to `gateway_hashes`
5. Run `terraform plan` to verify changes

---

## [0.21.0] - 2026-01-12

### ⚠️ BREAKING CHANGES FROM v0.20.x

If you migrated to v0.20.x and are using `dispatcher_lambda` and `dispatcher_gateway` resources directly, you can continue using them. However, the recommended approach is now the restored `dispatcher` resource.

### Added

- **Restored `dispatcher` resource** as the primary orchestrator interface
  - Reads routes from `routes.tf.rb` Ruby DSL file
  - Automatically creates Lambda functions and API Gateways based on routes
  - Supports `lambda_config` for per-action configuration overrides
  - Supports standalone Lambdas (defined in `lambda_config` without API routes)

- **Parallel Lambda updates** with configurable concurrency
  - Multiple Lambda functions update concurrently when changes are detected
  - Respects `docker_build_concurrency` provider setting
  - If one Lambda update fails, others continue and all failures are reported

- **New `ParallelManager` component** for concurrent Lambda operations
  - Semaphore-based concurrency control
  - Supports create, update, and delete operations in parallel

- **Enhanced `lambda_config` options**:
  - `env_vars` - Environment variables (merged with shared config)
  - `timeout` - Lambda timeout override
  - `memory_size` - Lambda memory override
  - `dynamodb_tables` - DynamoDB access with read_only/read_write modes
  - `s3_buckets` - S3 bucket access permissions
  - `ses_emails` - SES email sending permissions
  - `sns_triggers` - SNS topic subscriptions
  - `sqs_triggers` - SQS queue event source mappings
  - `shared` key applies configuration to all Lambdas

- **CloudWatch alarm configuration** via `alarm_config`:
  - Enable/disable alarms globally
  - Configure error, duration, throttle, and invocation thresholds
  - Per-action threshold overrides via `lambda_overrides`

- **Resource outputs**:
  - `lambda_functions` - Map of action name to Lambda ARN
  - `api_gateway_ids` - Map of gateway name to API Gateway ID
  - `api_gateway_urls` - Map of gateway name to invoke URL

### Changed

- The `dispatcher` resource is now the recommended way to use the provider
- `dispatcher_lambda` and `dispatcher_gateway` remain available for advanced use cases
- Improved change detection using hash-based comparison for routes, actions, and controllers

### Technical Details

- New `dispatcher_resource.go` implements the orchestrator pattern
- `parallel_manager.go` provides concurrent Lambda operations
- Routes are parsed via Ruby script and converted to Lambda/Gateway resources
- Configuration merging: `shared` config → action-specific config → route-derived config

---

## [0.20.0] - 2026-01-10

### ⚠️ BREAKING CHANGES

This release completely replaces the monolithic `dispatcher` resource with granular resources. **This is a breaking change that requires recreating your infrastructure.**

- **Removed**: The `dispatcher` resource has been completely removed
- **Added**: `dispatcher_lambda` - Manages individual Lambda functions
- **Added**: `dispatcher_gateway` - Manages individual API Gateways

### Migration Required

You must recreate your infrastructure using the new granular resources. The old `dispatcher` resource is no longer supported.

**Before (old monolithic resource):**

```hcl
resource "dispatcher" "main" {
  source            = "routes.tf.rb"
  app_name          = "myapp"
  lambda_source_dir = "./lambda"
  # ... all config in one resource
}
```

**After (new granular resources):**

```hcl
resource "dispatcher_lambda" "customer" {
  name       = "customer"
  app_name   = "myapp"
  source_dir = "./lambda"
  tables     = ["customers"]
}

resource "dispatcher_gateway" "api" {
  name          = "api"
  app_name      = "myapp"
  frontend_urls = ["https://app.example.com"]
  routes = [
    {
      name       = "get_customer"
      verb       = "GET"
      path       = "/customer"
      lambda_arn = dispatcher_lambda.customer.arn
      auth       = "cognito"
    }
  ]
}
```

### Added

- **`dispatcher_lambda` resource**: Manages individual Lambda functions with:
  - IAM execution role with automatic DynamoDB and CloudWatch permissions
  - CloudWatch log group
  - CloudWatch alarms (errors, duration, throttles)
  - Support for Lambda Layers
  - Import support via `terraform import`

- **`dispatcher_gateway` resource**: Manages individual API Gateways with:
  - REST API with routes, methods, and integrations
  - Cognito authorizer support
  - CORS configuration
  - CloudWatch log group
  - Import support via `terraform import`

- **Provider-level configuration**:
  - `default_lambda_timeout` - Default timeout for all Lambda resources
  - `default_lambda_memory` - Default memory for all Lambda resources
  - `default_tags` - Tags applied to all resources
  - `docker_build_concurrency` - Control parallel package builds

- **Parallel Lambda updates**: Each Lambda is a separate Terraform resource, enabling parallel updates

- **Improved plan output**: Clear visibility into exactly which Lambda functions and API Gateways will change

- **Smart dependency detection**: Analyzes Ruby `require` statements to determine which Lambdas are affected by shared file changes

### Removed

- `dispatcher` resource (monolithic) - Use `dispatcher_lambda` and `dispatcher_gateway` instead
- `lambda_config` attribute - Configure each Lambda resource individually
- `shared_iam_policy_arns` attribute - Use `iam_policy_arns` on individual Lambda resources
- `alarm_config` attribute - CloudWatch alarms are now managed per-Lambda
- `sns_triggers` attribute - Will be added to `dispatcher_lambda` in a future release

### Technical Details

- Refactored to follow HashiCorp's "Resources should represent a single API object" principle
- New `PackageBuilder` component for parallel Lambda package building with semaphore-based concurrency
- New `DependencyAnalyzer` component for Ruby require statement analysis
- Deterministic resource naming: `{app_name}-{environment}-{name}` (no random suffixes)
- Hash-based change detection for source code and configuration

---

## [0.16.0] - 2025-12-09

### Fixed

- **CRITICAL**: Fixed catastrophic bug where `terraform apply` would delete all Lambda functions without recreating them
  - Root cause: `populateUpdateState()` was recalculating hashes during Update phase, causing mismatch with ModifyPlan's calculated hashes
  - This triggered Terraform's drift detection to incorrectly identify all resources as deleted
  - Solution: Removed hash recalculation from Update phase; hashes are now only calculated once in ModifyPlan and preserved through the lifecycle
  - Impact: Updates now work correctly - Lambda functions are preserved and only modified resources are updated

- Fixed Lambda permission creation to use actual function names (with suffixes) instead of base names
  - Lambda permissions were being created for base function names (e.g., `customer`) instead of actual names with suffixes (e.g., `customer-xq5kq1`)
  - This prevented API Gateway from invoking Lambda functions, causing 403 errors
  - Solution: `CreateLambdaPermissions()` now extracts actual function names from Lambda ARNs in state

- Fixed IAM policy attachment to use actual role names discovered from Lambda functions
  - DynamoDB, S3, and SES policies were being attached to base role names instead of actual role names with suffixes
  - Solution: `createIAMPolicies()` now calls Lambda `GetFunction` API to discover actual role names for each action
  - Added wrapper functions: `CreateDynamoDBPoliciesWithRoleNames`, `CreateS3PoliciesWithRoleNames`, `CreateSESPoliciesWithRoleNames`

- Fixed API Gateway integration URIs to reference actual Lambda ARNs
  - API Gateway integrations were pointing to base Lambda names without suffixes
  - Caused 500 errors when API Gateway tried to invoke non-existent Lambda functions
  - Solution: Reorganized Create() flow to create Lambda functions first, discover actual names, then create API Gateway integrations with correct ARNs

- Fixed Lambda deletion to use actual function names from state
  - `deleteActions()` was using base names for deletion, leaving orphaned Lambda functions in AWS
  - Solution: Now extracts actual function names from Lambda ARNs stored in state before deletion

### Changed

- Reorganized Create() lifecycle flow for better resource dependency handling:
  1. Create Lambda functions with random suffixes (returns actual role names)
  2. Discover actual Lambda function names from state
  3. Create API Gateway resources with correct Lambda ARNs
  4. Create Lambda permissions for actual function names
  5. Create IAM policies attached to actual role names

- Enhanced `CreateApiGateways` to accept and use Lambda function map
  - Renamed to `CreateApiGatewaysWithLambdas` to reflect new signature
  - Now passes actual Lambda ARNs to integration creation

### Technical Details

- Hash calculation and change detection flow:
  1. **ModifyPlan phase**: Executes Ruby script, calculates all hashes (routes, controllers, actions, sources, configs), sets them in plan
  2. **Update phase**: Reads hashes from plan, compares with state hashes to determine diff, performs incremental updates
  3. **populateUpdateState**: Preserves hashes from plan, does NOT recalculate (critical for stability)
- Files modified:
  - `internal/resources/resource_state.go`: Removed hash recalculation in `populateUpdateState()`
  - `internal/resources/dispatcher_resource.go`: Reorganized Create() flow, added IAM role discovery
  - `internal/resources/lambda.go`: Updated permission creation to use actual function names
  - `internal/resources/iam.go`: Added role name parameters to policy methods
  - `internal/resources/api_gateway_operations.go`: Updated to accept and use Lambda function map
  - `internal/resources/resource_lifecycle.go`: Updated Update() to thread Lambda functions through operations

### Migration Notes

- No configuration changes required
- Existing deployments will self-heal on next `terraform apply`
- If you have orphaned Lambda functions from previous bugs, manually delete them from AWS Console or use AWS CLI

---

## [0.15.0] and earlier

See individual bug fix documentation files:

- `UNKNOWN_VALUES_BUG_FIX.md` - Fixed unknown values after apply
- `INCONSISTENT_RESULT_BUG_FIX.md` - Fixed inconsistent result errors
- `SHARED_ENV_UPDATE_BUG_FIX.md` - Fixed shared environment variable updates
- Other historical fixes documented in dedicated files
