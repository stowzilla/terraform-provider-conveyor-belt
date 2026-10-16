# Conveyor Belt Security Review — July 2026

## Overview

Security audit of the Terraform Provider Conveyor Belt — a Go-based custom Terraform provider that creates AWS serverless infrastructure from a Ruby DSL.

**Review date:** 2026-07-28/29
**Reviewer:** Kaylee (automated security review)
**Scope:** All Go source files, CI/CD configuration, dependencies, AWS interactions, embedded scripts

## Findings Summary

| Severity | Count | Status |
|----------|-------|--------|
| Critical | 0 | — |
| High | 0 | — |
| Medium | 4 | 3 fixed, 1 documented |
| Low | 3 | Documented |
| Info | 8 | Positive observations |

## Fixed Issues

### 1. Environment Variable Value Logging (Medium) ✅ FIXED

**File:** `internal/resources/env_resolver.go`

**Issue:** `ResolveEnvVarsForAction` logged all environment variable **values** via debug logging. These values may contain secrets (DB passwords, JWT secrets, API keys, Stripe keys). Provider logs are visible in `TF_LOG=DEBUG` output and CI logs.

**Fix:** Only log variable keys and count, never values.

### 2. CORS Wildcard Fallback (Medium) ✅ FIXED

**File:** `internal/resources/shared.go`

**Issue:** `GetCORSOriginForConfig` returned `*` when multiple `frontend_urls` were configured. Wildcard CORS is overly permissive and actually breaks credentialed requests per the CORS spec.

**Fix:** Use the first configured URL. Belt's runtime `CorsOrigin` module handles multi-origin validation dynamically per-request.

### 3. Known Dependency Vulnerabilities (Medium) ✅ FIXED

**Issue:** `govulncheck` identified 22+ vulnerabilities with symbol-level reachability including gRPC auth bypass, AWS SDK panic on crafted input, and HTTP/2 DoS.

**Fix:** Updated all third-party dependencies:
- gRPC v1.75.1 → v1.82.1
- golang.org/x/text v0.31.0 → v0.40.0
- golang.org/x/net v0.53.0 → v0.57.0
- AWS SDK eventstream v1.7.2 → v1.7.15
- cloudflare/circl v1.6.1 → v1.6.4
- AWS SDK cloudwatchlogs and lambda packages to latest

**Remaining:** 16 Go stdlib vulnerabilities require Go 1.25.5+ toolchain upgrade (outside scope of this PR).

### 4. env_vars Attribute Not Marked Sensitive (Medium) ✅ FIXED

**File:** `internal/resources/conveyor_belt_resource.go`

**Issue:** The `env_vars` Terraform attribute was not marked as `Sensitive: true`, meaning values could appear in plan output.

**Fix:** Added `Sensitive: true` to the schema attribute.

## Documented Issues (No Fix Required)

### 5. External CLI Execution Without Integrity Verification (Medium)

**File:** `internal/resources/shared.go`

**Issue:** The provider executes `belt routes -f json` by looking up the `belt` binary from PATH without integrity verification (checksum, signature, version check).

**Mitigation:** The `belt` binary is a signed Ruby gem installed by the developer. It would require a compromised development environment to exploit. The provider is a developer tool, not a production service. Risk is accepted.

### 6. Docker Image Uses `latest` Tag (Low)

**File:** `internal/resources/package_builder.go`

**Issue:** Lambda package builds use `public.ecr.aws/sam/build-ruby3.4:latest-x86_64`. Non-reproducible builds.

**Mitigation:** This is the standard AWS SAM build image from AWS's official ECR registry. Risk of compromise is very low. A future enhancement could pin to a digest.

### 7. Friendly Errors Could Leak Info if Misconfigured (Low)

**File:** `internal/resources/openapi_generator.go`

**Issue:** `friendly_errors = true` exposes internal routing hints in API Gateway error responses. No enforcement prevents use in production.

**Mitigation:** This is a developer-controlled Terraform config variable. Adding a validation warning for production environments is a potential future enhancement.

### 8. Go Stdlib Vulnerabilities (Low)

**Issue:** 16 remaining vulnerabilities in Go standard library (crypto/tls, crypto/x509, net/textproto, os).

**Mitigation:** Requires upgrading Go toolchain to 1.25.5+. Tracked for next toolchain update cycle.

## Positive Security Observations

1. **IAM least privilege** — DynamoDB policies scoped per-Lambda based on route tables; Secrets Manager scoped to `{app}-{env}-*` prefix
2. **No command injection** — All `exec.Command` calls use array form (no shell interpolation)
3. **Secret handling** — env var values never logged; `env_vars` and `lambda_config` marked Sensitive in Terraform schema
4. **Input validation** — Resource names validated against AWS constraints (regex, length limits); `SanitizeName()` strips dangerous characters
5. **CORS properly restrictive** — Explicitly avoids `*` for CORS origin
6. **Retry/backoff** — Exponential backoff for rate limiting with context cancellation
7. **CI/CD security** — GPG-signed releases, pinned action versions, minimal permissions
8. **No hardcoded credentials** — AWS auth uses standard SDK config chain

## Recommendations

1. **Upgrade Go toolchain** to 1.25.5+ to resolve stdlib vulnerabilities
2. **Consider pinning Docker image** to a specific digest for reproducible builds
3. **Add production guard** for `friendly_errors = true` (validation warning)
4. **Document trust boundary** around `belt` CLI execution
