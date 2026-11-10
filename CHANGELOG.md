# Changelog

All notable changes to `terraform-provider-conveyor-belt` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.0.22] - 2026-11-10

### Fixed

- **Missing SQS (and SNS) event source mappings are recreated on every apply** — Lambdas that declare `sqs_triggers` / `sns_triggers` already got their SQS IAM policy reconciled on every apply, but `ReconcileTriggers` (which creates the Lambda event source mapping / SNS subscription) only ran on create or when the Lambda *config* hash changed. A source-only `belt deploy` therefore left a missing mapping missing forever while the IAM policy stayed healthy — the worker looked wired up (`belt lambda-config` showed the trigger, role had `sqs:ReceiveMessage`) and still never consumed the queue. Seen on solarpunkit `inventory-prod-order_worker` (Sync Shopify hung with ~200 queued messages) and previously on Stowzilla's `search_indexer`. `Update()` now calls `ReconcileTriggers` in parallel for every non-new lambda that declares triggers, matching the IAM reconcile pattern. Idempotent; no plan-time API cost.

## [0.0.21] - 2026-11-06

### Fixed

- **"Provider produced inconsistent final plan" after big changes** — When the config held a known map/list/object with an unknown element (typically `lambda_env_refs = { kms_arn = aws_kms_key.new.arn }` for a resource created in the same apply, or an alarm `sns_topic_arn`/Cognito ARN), `buildConfigFromModel` failed on `lambda_env_refs.ElementsAs` and `ModifyPlan` returned early. Every hash attribute has `UseStateForUnknown`, so the plan promised the prior state's `routes_hash`, `lambda_*_hashes`, `gateway_hashes` and `openapi_spec_hashes`; the apply-time re-plan, with the ARN now known, computed the real values and Terraform aborted. The retry worked because nothing was unknown any more. Now: unknown `lambda_env_refs` entries are skipped instead of erroring; if `req.Config` is not fully known, all config-derived hashes (and gateway, model and OpenAPI spec hashes, and base path mappings) are deferred to apply; and any remaining `ModifyPlan` error path marks the hashes unknown rather than leaving state values in the plan. Source hashes are still computed at plan time, so source diffs stay visible. Also: `gem build` for PATH gems writes to a scratch directory instead of the PATH remote, and `hashPathGemContents` ignores `*.gem`. (Fizzy #1878)

## [0.0.20] - 2026-11-02

### Fixed

- **PATH gems no longer rebuild every Lambda on every plan** — Any app whose `Gemfile.lock` had a `PATH remote:` (a sibling gem checkout, or a monorepo root like belt-connect's demo-app → `../..`) planned "1 to change" forever: `ModifyPlan` marked `lambda_hashes`/`lambda_source_hashes` unknown whenever PATH gems existed, and the apply-time hash walked the entire remote including `.git/` internals, `tmp/` (where agent logs land), `node_modules/`, `vendor/`, specs, and the provider's own `.conveyor-belt/openapi/*.json`, so no two applies ever agreed and every one rebuilt every function (~5 min of Docker for nothing). PATH remotes are now hashed like any other source — at plan time — with a walker that honors the remote's root `.gitignore` (comments, negation, anchored and directory-only patterns, `*`/`?`/`**`) and always skips dot-directories plus `tmp`, `log(s)`, `node_modules`, `vendor`, `coverage`, `spec`, `test(s)`, `features`, `pkg`, `doc(s)`. `hashDirectoryContents` (shared dirs, gem dirs, `vendor/cache`) also skips dot-directories now. A clean tree gives "No changes"; a real source edit in the gem still rebuilds. (Fizzy #1756)
- **`reserved_concurrency` and `ephemeral_storage` in `config/lambda/<fn>.yml` are applied** — Both keys were passed through `convertToLambdaConfig` and then consumed by nothing, so `PutFunctionConcurrency` was never called and `/tmp` stayed at 512 MB. `reserved_concurrency` now calls `PutFunctionConcurrency` after create and before each config update (absent key → `DeleteFunctionConcurrency`, so removing it releases the reservation); `ephemeral_storage` is sent on `CreateFunction` and always on `UpdateFunctionConfiguration` (absent → 512 MB). Both are part of the config hash (omitted when unset, so existing hashes don't churn), and both are validated at plan time (`reserved_concurrency` integer ≥ 0, `ephemeral_storage` 512–10240) instead of being dropped. A `PutFunctionConcurrency` failure (e.g. an account whose limit can't spare the 100 unreserved executions AWS requires) fails the apply with the AWS message rather than deploying a public route without its cap. (Fizzy #1756)
- **CORS never answers `*` any more when `frontend_urls` has several entries** — The Lambda runtime already echoed the matching origin per request, but the mock `OPTIONS` preflight and the gateway error responses (401/403/404/5xx) fell back to `*` for multi-origin apps. The `OPTIONS` mock now carries a VTL response template that sets `Access-Control-Allow-Origin` to the request's `Origin` when it is in `frontend_urls` (static fallback: the first URL). Gateway responses cannot evaluate a template against the request, so with several URLs they map the header from `method.request.header.Origin` — functionally what `*` gave (these responses never carry `Allow-Credentials`) without advertising a wildcard. Single-URL apps are unchanged. The imperative `conveyor_belt_gateway` path gets the same treatment. (Fizzy #1756)
- **API Gateway 429s get an honest body** — `THROTTLED` and `QUOTA_EXCEEDED` gateway responses are now defined (status 429, CORS headers) with a "Too Many Requests … wait a moment and try again" body instead of falling through to `DEFAULT_4XX`'s "unsupported HTTP method" hint. (Fizzy #1756)
- **Docker build failures carry a one-line hint** — `Operation not permitted` while installing a native extension in the shared-gem build (Docker Desktop file-sharing state, fixed by restarting Docker) and "Cannot connect to the Docker daemon" are called out ahead of the bundler output. (Fizzy #1756)

### Added

- **Throttling knobs** — `throttle = { rate = <rps>, burst = <n> }` on `conveyor_belt` sets the stage-level default (`*/*` method settings) for every gateway. Per-route limits come from `throttle: { rate:, burst: }` in `config/lambda/<fn>.yml` (applies to every route that Lambda serves; `shared:` works too) or from a route-level `throttle` in the routes JSON (wins over the Lambda's; needs a Belt release that emits it). Limits land as stage method settings after each gateway deployment and are reconciled declaratively: method-level overrides that are no longer configured are removed. Throttle changes are part of the gateway hash, so they show as "Gateways to UPDATE". Values are validated at plan time (rate > 0, burst > 0). (Fizzy #1756)
- **`waf_web_acl_arn`** — Associates a REGIONAL WAFv2 web ACL with every gateway stage (one line instead of an `aws_wafv2_web_acl_association` per gateway spliced onto `api_gateway_ids`). Removing the attribute detaches the ACL the provider attached; an ACL attached by other means is left alone. May reference a web ACL created in the same apply (gateway hashes defer to apply when unknown). (Fizzy #1756)

## [0.0.19] - 2026-11-01

### Fixed

- **A new environment's Lambdas now get their `config/lambda/*.yml` settings on the first apply** — `Create()` built every function from `extractLambdaConfig(plan)`, which is only the HCL `lambda_config` map, and never used the YAML from `lambda_config_dir` that `buildConfigFromModel` had already merged in. A fresh environment (every new nested env, e.g. `belt generate environment fizzy-1580 dev`) therefore came up with only the built-in env vars (`APP_NAME`, `ENVIRONMENT`, table names, CORS): no YAML `env_vars` (`COGNITO_USER_POOL_ID`, bucket names, `ref()` values), no YAML `iam_policy_arns`, triggers or timeout/memory. The hashes Create stored were computed from the same partial config, so the *second* apply saw a diff and fixed it, which is why "deploy twice" was the workaround. `Create()` now uses the merged `config.LambdaConfig`, the same config `Update()` and `ModifyPlan()` use. `Read()` does too, so its env var and IAM drift checks compare against the YAML settings as well; an environment already deployed without them is flagged and repaired on its next apply. (Fizzy #1734)

## [0.0.18] - 2026-10-31

### Fixed

- **An existing Lambda whose role is missing its `iam_policy_arns` (or `shared_iam_policy_arns`) is now reported as drift** — 0.0.17 made the `Update()` IAM reconcile always re-attach per-lambda `iam_policy_arns`, but Terraform only calls `Update()` when the plan has a diff. A function deployed by an earlier provider version (e.g. `pay_webhooks` on Posh Nosh dev) had its config hash already in state, so `belt deploy` produced "No changes" and the reconcile never ran; the role stayed without its policies indefinitely. `Read()` now lists each role's attached managed policies (one `ListAttachedRolePolicies` per lambda that declares any, in parallel) and compares them with the desired set. When a desired ARN is missing it clears that lambda's config and combined hashes in state, so the next plan shows it under "Lambdas to UPDATE" and apply re-attaches the policies. Lambdas with no managed policies to check cost no extra IAM calls; a role that fails to list is logged and skipped rather than flagged. (Fizzy #1721)

## [0.0.17] - 2026-10-31

### Fixed

- **Per-lambda `iam_policy_arns` are now attached to every Lambda, not just the ones that happen to be reconciled** — A function's `iam_policy_arns` (from `config/lambda/<fn>.yml` or the HCL `lambda_config` map) were only attached in the post-update IAM reconcile pass, which runs for lambdas whose config hash changed or that declare `dynamodb_tables`/`sqs_triggers`. The Lambda *create* path never attached them, so a second function declared with `function :pay_webhooks do … end` came up with only the basic execution role plus the generated inline policies, and nothing ever revisited it because its config hash never changed again. Worse, the config-update path and the reconcile pass called `ReconcileSharedIamPolicies`, which detaches any customer-managed policy that is not in `shared_iam_policy_arns`, so a per-lambda policy that *was* attached could be stripped on the next config change. Three changes: the create path now attaches per-lambda policies right after the shared ones; reconciliation treats a lambda's own `iam_policy_arns` as desired (new `ReconcileIamPolicies`) so they are never detached as stale; and lambdas that declare `iam_policy_arns` are always included in the per-apply IAM reconcile, so existing deployments catch up on their next `belt deploy` with no config change needed. (Fizzy #1717)

## [0.0.16] - 2026-10-23

### Fixed

- **Changing `ruby_version` now migrates existing Lambdas (e.g. Ruby 3.4 → 4.0)** — Previously `ruby_version` was only applied at `CreateFunction`: `UpdateFunctionConfiguration` never set `Runtime`, and nothing flagged existing functions as changed, so switching `ruby_version = "4.0"` produced no plan diff and existing functions stayed on `ruby3.4` forever. `UpdateFunctionConfiguration` now always sets `Runtime` from `ruby_version`, and Read compares each deployed function's runtime with the expected one; on mismatch it clears that function's source and config hashes in state so the next plan shows it under "Lambdas to UPDATE" and apply rebuilds the code with the matching build image (`build-ruby4.0`, so `vendor/bundle/ruby/4.0.0` matches the runtime ABI) before updating the runtime. Ruby version is intentionally not part of the lambda hashes, so upgrading the provider alone does not redeploy anything.
- **`conveyor_belt_lambda` now honors provider-level `ruby_version`** — The standalone lambda resource ignored `ruby_version` entirely (always `ruby3.4` and the 3.4 build image). It now uses the derived runtime on create/update and the derived build image when `docker_build_image` is not set.

## [0.0.15] - 2026-09-11

### Fixed

- **Fresh IAM execution roles are now given time to propagate before the first `CreateFunction`** — A brand-new IAM role is eventually consistent and can take 30–60s+ to become assumable by `lambda.amazonaws.com`. On a first-time environment deploy the role is created milliseconds before the Lambda, so the first `CreateFunction` frequently tripped `InvalidParameterValueException: The role defined for the function cannot be assumed by Lambda`. Two changes address this: the `CreateFunction` retry now uses a capped-exponential backoff with a ~2-minute budget (the previous loop was linear despite a comment claiming otherwise, and gave up before propagation completed), and a one-time 8s pre-flight pause is inserted right after creating a fresh role (create path only — never on the `EntityAlreadyExists` path where the role is already propagated). Existing environments are unaffected because their roles are already assumable.

## [0.0.14] - 2026-09-10

### Fixed

- **Lambda DynamoDB policies now grant `BatchGetItem` and `BatchWriteItem`** — The generated inline policy granted only single-item operations (`GetItem`/`PutItem`/`UpdateItem`/`DeleteItem`) plus `Query`/`Scan`. ActiveItem loads multi-key finds and `has_many :through` associations with `BatchGetItem`, and batches writes with `BatchWriteItem`, so those paths failed at runtime with `AccessDeniedError: ... is not allowed to BatchGetItem on <table>` even though single-item access worked. `BatchGetItem` is now in both the read-write and read-only action sets; `BatchWriteItem` is in the read-write set.

## [0.0.13] - 2026-08-04

### Fixed

- **Per-lambda `iam_policy_arns` now triggers config hash change** — Previously, adding `iam_policy_arns` to a lambda's YAML config didn't change the config hash, so the IAM policy attachment never fired on redeploy. The hash now includes per-lambda IAM policy ARNs.

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
