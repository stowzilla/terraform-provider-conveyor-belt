#!/usr/bin/env ruby
# frozen_string_literal: true

# List Schemas - Output schema definitions as JSON for Go provider and OpenAPI consumption
#
# Usage:
#   ./scripts/list_schemas.rb                    # JSON output to stdout
#   ./scripts/list_schemas.rb --stdout           # Explicit stdout (same behavior)
#   ./scripts/list_schemas.rb path/to/schema.rb  # Custom schema file
#
# Output format (JSON):
# {
#   "request_models": {
#     "signup": {
#       "name": "signup",
#       "properties": {
#         "email": { "type": "string" }
#       },
#       "required": ["email"]
#     }
#   },
#   "response_models": {
#     "item": {
#       "name": "item",
#       "contexts": {
#         "ops": {
#           "name": "ops",
#           "properties": {
#             "id": { "type": "string" }
#           }
#         }
#       }
#     }
#   }
# }

require 'json'
require 'optparse'
require_relative 'lib/schema_dsl'
require_relative 'lib/terra_dispatch'

class SchemaListGenerator
  def generate(schema_file)
    unless File.exist?(schema_file)
      STDERR.puts "Error: Schema file not found: #{schema_file}"
      exit 1
    end

    # Reset schema builder to ensure clean state
    TerraDispatch.instance_variable_set(:@schema_builder, nil)

    begin
      load schema_file
    rescue SyntaxError => e
      STDERR.puts "Error loading schema file: #{e.message}"
      exit 1
    rescue => e
      STDERR.puts "Error loading schema file: #{e.message}"
      STDERR.puts "  at #{e.backtrace.first}" if e.backtrace&.first
      exit 1
    end

    schema = TerraDispatch.schema
    output = deep_stringify_keys(schema.to_h)
    puts JSON.pretty_generate(output)
  end

  private

  def deep_stringify_keys(obj)
    case obj
    when Hash
      obj.each_with_object({}) do |(key, value), result|
        result[key.to_s] = deep_stringify_keys(value)
      end
    when Array
      obj.map { |item| deep_stringify_keys(item) }
    else
      obj
    end
  end
end

# Main execution
if __FILE__ == $0
  options = {}

  OptionParser.new do |opts|
    opts.banner = "Usage: #{$0} [options] [schema_file]"

    opts.on("--stdout", "Output to stdout (default behavior)") do
      options[:stdout] = true
    end

    opts.on("-h", "--help", "Show this help") do
      puts opts
      exit
    end
  end.parse!

  schema_file = ARGV[0] || File.expand_path('infrastructure/schema.tf.rb')
  generator = SchemaListGenerator.new
  generator.generate(schema_file)
end
