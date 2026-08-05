#!/usr/bin/env bash
# Validates routes.tf.rb (and schema.tf.rb) using the belt CLI.
# Exits 0 if routes parse successfully, non-zero on errors.

set -euo pipefail

# Check that belt is available
if ! command -v belt &> /dev/null; then
  echo "Error: 'belt' CLI not found. Install it with: gem install belt"
  echo "See: https://github.com/stowzilla/belt"
  exit 1
fi

errors=0

for file in "$@"; do
  echo "Validating routes: ${file}"

  # For schema files, just check Ruby syntax (belt routes handles the full parse)
  if [[ "$file" == *schema.tf.rb ]]; then
    if ! ruby -c "$file" > /dev/null 2>&1; then
      echo "  ✗ Syntax error in ${file}"
      ruby -c "$file"
      errors=$((errors + 1))
    else
      echo "  ✓ ${file}"
    fi
    continue
  fi

  # For routes files, use belt routes to validate
  dir=$(dirname "$file")

  # Try to find the routes file relative path for belt
  if ! output=$(cd "$dir" && belt routes -f json --source "$(basename "$file")" 2>&1); then
    echo "  ✗ Failed to parse ${file}"
    echo "$output" | sed 's/^/    /'
    errors=$((errors + 1))
  else
    # Count routes to give feedback
    route_count=$(echo "$output" | ruby -rjson -e 'puts JSON.parse(STDIN.read).length' 2>/dev/null || echo "?")
    echo "  ✓ ${file} (${route_count} routes)"
  fi
done

if [ $errors -gt 0 ]; then
  echo ""
  echo "${errors} file(s) failed validation"
  exit 1
fi
