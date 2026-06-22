#!/usr/bin/env ruby
# frozen_string_literal: true

require 'set'

# Converts parsed schema data back into valid Schema DSL syntax.
# Used for round-trip fidelity testing: parse → to_h → print → re-parse → to_h → assert equivalence.
module SchemaPrinter
  DSL_TYPE_MAP = {
    'string'  => 'string',
    'number'  => 'number',
    'integer' => 'integer',
    'boolean' => 'boolean',
    'array'   => 'array',
    'object'  => 'map'
  }.freeze

  def self.print(schema_hash)
    lines = ['TerraDispatch.schema.define do']

    request_models = schema_hash[:request_models] || schema_hash['request_models'] || {}
    response_models = schema_hash[:response_models] || schema_hash['response_models'] || {}

    request_models.each do |name, model|
      lines << print_request_model(name, model)
    end

    response_models.each do |name, model|
      lines << print_response_model(name, model)
    end

    lines << 'end'
    lines.join("\n")
  end

  def self.print_request_model(name, model)
    lines = ["  request :#{name} do"]

    properties = model[:properties] || model['properties'] || {}
    required = model[:required] || model['required'] || []
    required_set = Set.new(required.map(&:to_s))

    properties.each do |field_name, prop|
      type_str = prop[:type] || prop['type']
      dsl_type = DSL_TYPE_MAP[type_str] || type_str
      opts = required_set.include?(field_name.to_s) ? ', required: true' : ''
      lines << "    #{dsl_type} :#{field_name}#{opts}"
    end

    lines << '  end'
    lines.join("\n")
  end
  private_class_method :print_request_model

  def self.print_response_model(name, model)
    lines = ["  model :#{name} do"]

    # Direct properties (context-less response model)
    properties = model[:properties] || model['properties'] || {}
    properties.each do |field_name, prop|
      type_str = prop[:type] || prop['type']
      dsl_type = DSL_TYPE_MAP[type_str] || type_str
      lines << "    #{dsl_type} :#{field_name}"
    end

    # Context-wrapped properties
    contexts = model[:contexts] || model['contexts'] || {}
    contexts.each do |ctx_name, ctx|
      lines << "    context :#{ctx_name} do"

      ctx_properties = ctx[:properties] || ctx['properties'] || {}
      ctx_properties.each do |field_name, prop|
        type_str = prop[:type] || prop['type']
        dsl_type = DSL_TYPE_MAP[type_str] || type_str
        lines << "      #{dsl_type} :#{field_name}"
      end

      lines << '    end'
    end

    lines << '  end'
    lines.join("\n")
  end
  private_class_method :print_response_model
end
