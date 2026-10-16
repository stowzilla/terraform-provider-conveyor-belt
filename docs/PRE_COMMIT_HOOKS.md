# Pre-commit Hooks for Conveyor Belt Projects

Conveyor Belt provides [pre-commit](https://pre-commit.com/) hooks to validate your infrastructure configuration before committing. These hooks help catch errors early — broken routes, invalid Ruby syntax, accidentally committed generated files, and **security policy violations via Checkov**.

## Quick Setup

### 1. Install pre-commit

```bash
# macOS
brew install pre-commit

# pip
pip install pre-commit
```

### 2. Create `.pre-commit-config.yaml`

Add this to the root of your infrastructure project (where your `routes.tf.rb` lives):

```yaml
repos:
  - repo: https://github.com/stowzilla/terraform-provider-conveyor-belt
    rev: v0.23.0  # Use the latest provider version
    hooks:
      - id: conveyor-belt-routes-validate
      - id: conveyor-belt-no-generated-files
      - id: conveyor-belt-lambda-syntax
      - id: conveyor-belt-checkov
```

See [`hooks/examples/pre-commit-config.yaml`](../hooks/examples/pre-commit-config.yaml) for a full example including Terraform formatting and validation hooks.

### 3. Install the hooks

```bash
pre-commit install
```

### 4. (Optional) Run against all files

```bash
pre-commit run --all-files
```

## Available Hooks

### `conveyor-belt-routes-validate`

Validates `routes.tf.rb` and `schema.tf.rb` files by running `belt routes -f json`. Catches DSL syntax errors, invalid route definitions, and schema issues before they reach `terraform plan`.

**Requires:** `belt` CLI (`gem install belt`)

**Triggers on:** Files matching `routes.tf.rb` or `schema.tf.rb`

### `conveyor-belt-no-generated-files`

Prevents accidentally committing files from the `.conveyor-belt/` directory. These are generated during `terraform plan` and should be in `.gitignore`.

**Triggers on:** All staged files (checks paths)

### `conveyor-belt-lambda-syntax`

Runs `ruby -c` on Lambda source files to catch syntax errors before committing.

**Requires:** `ruby`

**Triggers on:** `.rb` files (excluding `routes.tf.rb`, `schema.tf.rb`, `Gemfile`, etc.)

### `conveyor-belt-checkov`

Runs [Checkov](https://www.checkov.io/) with custom security policies designed specifically for Conveyor Belt infrastructure. Validates that your configuration follows security best practices.

**Requires:** `checkov` (`pip install checkov` or `brew install checkov`)

**Triggers on:** `.tf` files

**Custom policies included:**

| Policy ID | Category | Description |
|-----------|----------|-------------|
| `CKV_CONVEYOR_1` | Logging | Ensure CloudWatch alarms are enabled |
| `CKV_CONVEYOR_2` | Security | Ensure `friendly_errors` is disabled (prevents info leakage in production) |
| `CKV_CONVEYOR_3` | IAM | Ensure Cognito authentication is configured |
| `CKV_CONVEYOR_4` | Logging | Ensure alarm SNS topic is configured when alarms are enabled |
| `CKV_CONVEYOR_5` | Security | Ensure Lambda timeout does not exceed 900 seconds |
| `CKV_CONVEYOR_6` | IAM | Ensure shared IAM policies are defined |
| `CKV_CONVEYOR_7` | Data Protection | Ensure DynamoDB tables have deletion protection enabled |
| `CKV_CONVEYOR_8` | Backup & Recovery | Ensure DynamoDB tables have point-in-time recovery enabled |
| `CKV_CONVEYOR_9` | Backup & Recovery | Ensure S3 buckets have versioning enabled |
| `CKV_CONVEYOR_10` | Security | Ensure S3 buckets block all public access |
| `CKV_CONVEYOR_11` | Cost Control | Ensure Lambda memory is within cost-effective bounds (≤ 3008 MB) |
| `CKV_CONVEYOR_12` | Reliability | Ensure SQS queues have a dead-letter queue configured |
| `CKV_CONVEYOR_13` | Encryption | Ensure SQS queues have encryption enabled |
| `CKV_CONVEYOR_14` | Encryption | Ensure SNS topics have encryption enabled |

## Checkov Integration Details

### How It Works

Checkov is a static analysis tool that scans Terraform HCL files without requiring `terraform init` or AWS credentials. Our custom policies ship with the provider repo and validate Conveyor Belt resource configurations against security best practices.

When you add the `conveyor-belt-checkov` hook, it:
1. Scans your `.tf` files using the `terraform` framework
2. Applies only `CKV_CONVEYOR_*` policies (won't interfere with other Checkov rules)
3. Reports any violations before the commit is allowed

### Skipping Policies

Skip specific policies for a resource using inline comments:

```hcl
resource "conveyor_belt" "main" {
  #checkov:skip=CKV_CONVEYOR_2:Friendly errors intentionally enabled for staging
  friendly_errors = true
  # ...
}
```

Or skip globally via hook args:

```yaml
- repo: https://github.com/stowzilla/terraform-provider-conveyor-belt
  rev: v0.23.0
  hooks:
    - id: conveyor-belt-checkov
      args: ['--skip-check', 'CKV_CONVEYOR_2']
```

### Running Checkov Standalone

You can also run Checkov directly (without pre-commit) using the policies from this repo:

```bash
# Clone or reference the provider repo
checkov -d . \
  --external-checks-dir /path/to/terraform-provider-conveyor-belt/hooks/checkov-policies \
  --framework terraform \
  --check 'CKV_CONVEYOR*'
```

### Combining with Full Checkov Scans

For comprehensive security scanning, combine Conveyor Belt policies with a full Checkov scan. This catches both Conveyor Belt-specific issues AND general Terraform security problems:

```yaml
repos:
  # Conveyor Belt-specific checks
  - repo: https://github.com/stowzilla/terraform-provider-conveyor-belt
    rev: v0.23.0
    hooks:
      - id: conveyor-belt-checkov

  # Full Checkov scan (all built-in policies)
  - repo: https://github.com/bridgecrewio/checkov.git
    rev: '3.2.0'
    hooks:
      - id: checkov
        args: [--quiet, --compact]
```

### Terraform Plan Scanning

For the most thorough security analysis, scan your Terraform plan output. This expands all resources (including those managed by Conveyor Belt internally) into their planned state:

```bash
# Generate plan
terraform plan -out=tfplan.binary
terraform show -json tfplan.binary | jq > tfplan.json

# Scan with all Checkov policies
checkov -f tfplan.json

# Scan with Conveyor Belt policies only
checkov -f tfplan.json \
  --external-checks-dir /path/to/terraform-provider-conveyor-belt/hooks/checkov-policies \
  --check 'CKV_CONVEYOR*'
```

> **Note:** Plan scanning requires `terraform init` and valid AWS credentials, so it's better suited for CI/CD pipelines than pre-commit hooks.

### Writing Custom Policies

You can extend the bundled policies with your own. Create YAML policy files in your project:

```yaml
# my-checks/CKV_MYORG_1.yaml
metadata:
  id: "CKV_MYORG_1"
  name: "Ensure Lambda memory does not exceed 1024 MB (cost control)"
  category: "GENERAL_SECURITY"
  guideline: "Organization policy limits Lambda memory to 1024 MB."

scope:
  provider: "conveyor-belt"

definition:
  cond_type: "attribute"
  resource_types:
    - "conveyor_belt_lambda"
  attribute: "memory"
  operator: "less_than_or_equal"
  value: 1024
```

Then reference your custom checks directory:

```yaml
- repo: https://github.com/bridgecrewio/checkov.git
  rev: '3.2.0'
  hooks:
    - id: checkov
      args: [--external-checks-dir, 'my-checks']
```

## Prerequisites

| Hook | Requires |
|------|----------|
| `conveyor-belt-routes-validate` | `belt` CLI, `ruby` |
| `conveyor-belt-no-generated-files` | (none) |
| `conveyor-belt-lambda-syntax` | `ruby` |
| `conveyor-belt-checkov` | `checkov` |

Install dependencies:

```bash
gem install belt           # Route validation
pip install checkov        # Security scanning (or: brew install checkov)
```

## Recommended .gitignore

Add this to your project's `.gitignore`:

```gitignore
# Conveyor Belt generated artifacts
.conveyor-belt/

# Terraform plan files (may contain secrets)
tfplan.binary
tfplan.json
```

## Combining with Terraform Hooks

For a complete setup, add the [pre-commit-terraform](https://github.com/antonbabenko/pre-commit-terraform) hooks for Terraform formatting and validation:

```yaml
repos:
  # Conveyor Belt hooks
  - repo: https://github.com/stowzilla/terraform-provider-conveyor-belt
    rev: v0.23.0
    hooks:
      - id: conveyor-belt-routes-validate
      - id: conveyor-belt-no-generated-files
      - id: conveyor-belt-lambda-syntax
      - id: conveyor-belt-checkov

  # Terraform hooks (formatting, validation, full Checkov)
  - repo: https://github.com/antonbabenko/pre-commit-terraform
    rev: v1.96.1
    hooks:
      - id: terraform_fmt
      - id: terraform_validate
      - id: terraform_checkov
        args:
          - --args=--quiet
          - --args=--compact
```

## Customizing Hook Behavior

### Restrict Lambda syntax check to specific directories

```yaml
- repo: https://github.com/stowzilla/terraform-provider-conveyor-belt
  rev: v0.23.0
  hooks:
    - id: conveyor-belt-lambda-syntax
      files: '^lambda/'  # Only check files in the lambda/ directory
```

### Skip hooks temporarily

```bash
# Skip all hooks for one commit
git commit --no-verify

# Skip specific hooks
SKIP=conveyor-belt-checkov git commit -m "WIP: config in progress"
```

## CI/CD Integration

For CI pipelines, run Checkov with the custom policies as part of your PR checks:

```yaml
# .github/workflows/security.yml
name: Security Scan
on: [pull_request]

jobs:
  checkov:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install Checkov
        run: pip install checkov

      - name: Run Conveyor Belt security checks
        run: |
          checkov -d . \
            --external-checks-dir hooks/checkov-policies \
            --framework terraform \
            --check 'CKV_CONVEYOR*' \
            --compact

      - name: Run full Checkov scan
        run: |
          checkov -d . \
            --framework terraform \
            --quiet \
            --compact
```
