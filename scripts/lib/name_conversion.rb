# frozen_string_literal: true

# Snake-case / camelCase conversion helpers used by the OpenAPI generator.
#
# These are defined as top-level methods so they integrate naturally with
# the existing generate_openapi.rb script (which uses top-level methods).

# Convert snake_case to camelCase: "zip_code" → "zipCode"
# Single-word names pass through unchanged.
def snake_to_camel(str)
  str.to_s.gsub(/_([a-z])/) { $1.upcase }
end

# Convert camelCase to snake_case: "zipCode" → "zip_code"
# Each uppercase letter produced by snake_to_camel represents a new word boundary.
# We split on every uppercase letter boundary using lookaheads to avoid consuming chars.
def camel_to_snake(str)
  str.to_s
     .gsub(/([a-z\d])([A-Z])/, '\1_\2')
     .gsub(/([A-Z]+)([A-Z][a-z])/, '\1_\2')
     .downcase
end
