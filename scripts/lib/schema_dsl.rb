#!/usr/bin/env ruby
# frozen_string_literal: true

# DSL for defining API request and response schema contracts
#
# Example usage:
#   TerraDispatch.schema.define do
#     request :signup do
#       string :email, required: true
#       string :name, required: true
#       boolean :terms_accepted, required: true
#     end
#
#     model :item do
#       context :ops do
#         string :id
#         string :name
#         number :price
#       end
#       context :customer do
#         string :id
#         string :name
#       end
#     end
#   end

class SchemaBuilder
  DSL_VERSION = "1.0.0"

  attr_reader :request_models, :response_models

  def initialize
    @request_models = {}
    @response_models = {}
  end

  def define(&block)
    instance_eval(&block) if block_given?
    self
  end

  def request(name, &block)
    builder = RequestModelBuilder.new(name)
    builder.instance_eval(&block) if block_given?
    @request_models[name] = builder
  end

  def model(name, &block)
    builder = ResponseModelBuilder.new(name)
    builder.instance_eval(&block) if block_given?
    @response_models[name] = builder
  end

  def to_h
    {
      request_models: @request_models.transform_values(&:to_h),
      response_models: @response_models.transform_values(&:to_h)
    }
  end
end


class RequestModelBuilder
  SUPPORTED_TYPES = %i[string number integer boolean array object map list].freeze

  attr_reader :name, :fields

  def initialize(name)
    @name = name
    @fields = []
  end

  SUPPORTED_TYPES.each do |type|
    define_method(type) do |field_name, options = {}|
      @fields << { name: field_name, type: type, required: options[:required] == true }
    end
  end

  def to_h
    {
      name: @name.to_s,
      properties: fields_to_properties,
      required: @fields.select { |f| f[:required] }.map { |f| f[:name].to_s }
    }
  end

  private

  def fields_to_properties
    @fields.each_with_object({}) do |field, hash|
      hash[field[:name].to_s] = { type: map_type(field[:type]) }
    end
  end

  def map_type(dsl_type)
    case dsl_type
    when :map then 'object'
    when :list then 'array'
    else dsl_type.to_s
    end
  end
end

class ResponseModelBuilder
  SUPPORTED_TYPES = %i[string number integer boolean array object map list].freeze

  attr_reader :name, :contexts, :fields

  def initialize(name)
    @name = name
    @contexts = {}
    @fields = []
  end

  SUPPORTED_TYPES.each do |type|
    define_method(type) do |field_name, options = {}|
      @fields << { name: field_name, type: type }
    end
  end

  def context(name, &block)
    builder = ContextBuilder.new(name)
    builder.instance_eval(&block) if block_given?
    @contexts[name] = builder
  end

  def to_h
    result = { name: @name.to_s, contexts: @contexts.transform_values(&:to_h) }
    result[:properties] = fields_to_properties unless @fields.empty?
    result
  end

  private

  def fields_to_properties
    @fields.each_with_object({}) do |field, hash|
      hash[field[:name].to_s] = { type: map_type(field[:type]) }
    end
  end

  def map_type(dsl_type)
    case dsl_type
    when :map then 'object'
    when :list then 'array'
    else dsl_type.to_s
    end
  end
end

class ContextBuilder
  SUPPORTED_TYPES = %i[string number integer boolean array object map list].freeze

  attr_reader :name, :fields

  def initialize(name)
    @name = name
    @fields = []
  end

  SUPPORTED_TYPES.each do |type|
    define_method(type) do |field_name, options = {}|
      @fields << { name: field_name, type: type }
    end
  end

  def to_h
    {
      name: @name.to_s,
      properties: fields_to_properties
    }
  end

  private

  def fields_to_properties
    @fields.each_with_object({}) do |field, hash|
      hash[field[:name].to_s] = { type: map_type(field[:type]) }
    end
  end

  def map_type(dsl_type)
    case dsl_type
    when :map then 'object'
    when :list then 'array'
    else dsl_type.to_s
    end
  end
end
