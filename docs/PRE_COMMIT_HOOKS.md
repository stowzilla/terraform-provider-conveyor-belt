# Pre-commit Hooks for Conveyor Belt Projects

Conveyor Belt provides [pre-commit](https://pre-commit.com/) hooks to validate your infrastructure configuration before committing. These hooks help catch errors early — broken routes, invalid Ruby syntax, or accidentally committed generated files.

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

## Prerequisites

| Hook | Requires |
|------|----------|
| `conveyor-belt-routes-validate` | `belt` CLI, `ruby` |
| `conveyor-belt-no-generated-files` | (none) |
| `conveyor-belt-lambda-syntax` | `ruby` |

Install the `belt` CLI:

```bash
gem install belt
```

## Recommended .gitignore

Add this to your project's `.gitignore`:

```gitignore
# Conveyor Belt generated artifacts
.conveyor-belt/
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

  # Terraform hooks
  - repo: https://github.com/antonbabenko/pre-commit-terraform
    rev: v1.96.1
    hooks:
      - id: terraform_fmt
      - id: terraform_validate
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
SKIP=conveyor-belt-routes-validate git commit -m "WIP: routes in progress"
```
