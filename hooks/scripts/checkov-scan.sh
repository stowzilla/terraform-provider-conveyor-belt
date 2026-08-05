#!/usr/bin/env bash
#
# Pre-commit hook: Run Checkov with Conveyor Belt custom policies
#
# Scans Terraform files for Conveyor Belt security and best-practice violations.
# Uses custom YAML policies shipped with the provider.
#
# Requirements: checkov (pip install checkov)
#

set -euo pipefail

# Find the hook script's directory (where policies live alongside it)
HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POLICIES_DIR="${HOOK_DIR}/../checkov-policies"

# Verify checkov is installed
if ! command -v checkov &> /dev/null; then
    echo "ERROR: checkov is not installed."
    echo "Install it with: pip install checkov"
    echo "Or: brew install checkov"
    exit 1
fi

# Verify policies directory exists
if [ ! -d "$POLICIES_DIR" ]; then
    echo "ERROR: Checkov policies directory not found at: $POLICIES_DIR"
    exit 1
fi

# Determine the directory to scan
# If files are passed as arguments, scan their parent directories
# Otherwise, scan the current directory
if [ $# -gt 0 ]; then
    # Get unique directories from passed files
    DIRS=()
    for file in "$@"; do
        dir=$(dirname "$file")
        # Deduplicate
        if [[ ! " ${DIRS[*]:-} " =~ " ${dir} " ]]; then
            DIRS+=("$dir")
        fi
    done

    EXIT_CODE=0
    for dir in "${DIRS[@]}"; do
        echo "Running Checkov on: $dir"
        checkov -d "$dir" \
            --external-checks-dir "$POLICIES_DIR" \
            --framework terraform \
            --check 'CKV_CONVEYOR*' \
            --compact \
            --quiet || EXIT_CODE=$?
    done
    exit $EXIT_CODE
else
    echo "Running Checkov on current directory"
    checkov -d . \
        --external-checks-dir "$POLICIES_DIR" \
        --framework terraform \
        --check 'CKV_CONVEYOR*' \
        --compact \
        --quiet
fi
