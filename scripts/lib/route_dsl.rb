#!/usr/bin/env ruby
# frozen_string_literal: true

require 'set'

# DSL for defining API Gateway routes that generate Terraform resources
#
# Example usage:
#   api_gateway :customer, auth: :cognito do
#     get '/profile'
#     post '/schedule-pickup'
#     resources :pickups, only: [:index, :show, :update]
#     resource :profile
#   end

class Route
  attr_reader :method, :path, :auth, :lambda, :cors, :tables, :route_type, :controller, :action,
              :request_model, :response_model, :response_context

  # Route types:
  # - :resource          - from `resource` (singular) DSL
  # - :resources         - from `resources` (plural) DSL
  # - :action            - from plain HTTP methods like `get`, `post` (RPC-style or custom actions)
  def initialize(method, path, options = {})
    @method = method.to_s.upcase
    @path = normalize_path(path)
    @auth = options[:auth]
    @lambda = options[:lambda]
    @cors = options.fetch(:cors, true)
    @tables = options[:tables] || []
    @route_type = options[:route_type] || :action
    @controller = options[:controller]  # Explicit controller override
    @action = options[:action]          # Explicit action override
    @request_model = options[:request_model]&.to_s
    @response_model = options[:response_model]&.to_s
    @response_context = options[:response_context]&.to_s
  end

  def resource?
    @route_type == :resource || @route_type == :resources
  end

  def singular_resource?
    @route_type == :resource
  end

  def plural_resource?
    @route_type == :resources
  end

  def action?
    @route_type == :action
  end

  def to_h
    h = {
      method: @method,
      path: @path,
      auth: @auth.to_s,
      lambda: @lambda.to_s,
      cors: @cors,
      tables: @tables,
      route_type: @route_type.to_s,
      terraform_name: terraform_name,
      base_path: base_path
    }
    h[:request_model] = @request_model if @request_model
    h[:response_model] = @response_model if @response_model
    h[:response_context] = @response_context if @response_context
    h
  end

  def to_json(*args)
    require 'json'
    to_h.to_json(*args)
  end

  # Convert path to Terraform-safe name (e.g., "/schedule-pickup" => "schedule_pickup")
  def terraform_name
    @path.gsub(%r{^/}, '').gsub(/[\/\-]/, '_').gsub(/\{(\w+)\}/, '\1')
  end

  # Get base path without parameters (e.g., "/pickups/{pickupId}" => "/pickups")
  def base_path
    segments = @path.split('/').reject(&:empty?)
    base_segments = segments.reject { |s| Route.path_parameter?(s) }
    base_segments.empty? ? @path : "/#{base_segments.join('/')}"
  end

  # Get terraform name for the base path (used for file grouping)
  def base_terraform_name
    base_path.gsub(%r{^/}, '').gsub(/[\/\-]/, '_')
  end

  # Get path segments for creating nested API Gateway resources
  def path_segments
    @path.split('/').reject(&:empty?)
  end

  # Check if this is a path parameter segment like {pickupId}
  def self.path_parameter?(segment)
    segment.start_with?('{') && segment.end_with?('}')
  end

  # Extract parameter name from {paramName}
  def self.extract_parameter(segment)
    segment.gsub(/[\{\}]/, '')
  end

  private

  def normalize_path(path)
    path = "/#{path}" unless path.start_with?('/')
    path
  end
end

# Helper class for building nested resources
class NestedResourceBuilder
  def initialize(gateway, prefix, collection_prefix, inherited_tables: [], inherited_auth: nil)
    @gateway = gateway
    @prefix = prefix              # e.g., "/availability_slots/{availability_slotId}"
    @collection_prefix = collection_prefix  # e.g., "/availability_slots"
    @inherited_tables = inherited_tables    # Tables inherited from parent resources block
    @inherited_auth = inherited_auth        # Auth inherited from parent resources block
  end

  # Nested resources
  def resources(name, options = {})
    resource_name = name.to_s
    singular = @gateway.send(:singularize, resource_name)
    param_name = options[:param] || "#{singular}_id"

    # Merge inherited options
    options = merge_inherited_options(options)

    # Auto-infer tables if not explicitly provided
    options = @gateway.send(:auto_infer_tables, resource_name, options)

    # Mark these as plural resource routes
    resource_options = options.merge(route_type: :resources)

    actions = @gateway.send(:determine_actions, options)

    @gateway.send(:add_route, :get, "#{@prefix}/#{resource_name}", resource_options) if actions.include?(:index)
    @gateway.send(:add_route, :post, "#{@prefix}/#{resource_name}", resource_options) if actions.include?(:create)
    @gateway.send(:add_route, :get, "#{@prefix}/#{resource_name}/{#{param_name}}", resource_options) if actions.include?(:show)
    @gateway.send(:add_route, :put, "#{@prefix}/#{resource_name}/{#{param_name}}", resource_options) if actions.include?(:update)
    @gateway.send(:add_route, :delete, "#{@prefix}/#{resource_name}/{#{param_name}}", resource_options) if actions.include?(:destroy)
  end

  # Block for grouping member routes (routes that include :id in path)
  # Example:
  #   resources :items do
  #     member do
  #       post '/upload-image'
  #       delete '/images'
  #     end
  #   end
  def member(&block)
    MemberCollectionBuilder.new(@gateway, @prefix, @inherited_tables, @inherited_auth, :member).instance_eval(&block)
  end

  # Block for grouping collection routes (routes without :id in path)
  # Example:
  #   resources :items do
  #     collection do
  #       get '/search'
  #       get '/types'
  #     end
  #   end
  def collection(&block)
    MemberCollectionBuilder.new(@gateway, @collection_prefix, @inherited_tables, @inherited_auth, :collection).instance_eval(&block)
  end

  # Individual HTTP methods for nested routes
  # Support on: :collection or on: :member (defaults to :member)
  [:get, :post, :put, :delete, :patch].each do |method|
    define_method(method) do |path, options = {}|
      # Check if this is a collection route (no :id in path)
      if options[:on] == :collection
        full_path = "#{@collection_prefix}#{path}"
      else
        # Default to member route (includes :id in path)
        full_path = "#{@prefix}#{path}"
      end

      # Merge inherited options
      options = merge_inherited_options(options)

      # Remove :on from options before passing to add_route
      route_options = options.reject { |k, _| k == :on }
      @gateway.send(:add_route, method, full_path, route_options)
    end
  end

  private

  # Merge inherited tables and auth with explicitly specified options
  def merge_inherited_options(options)
    result = options.dup

    # Merge tables (inherited + explicit)
    if @inherited_tables.any?
      explicit_tables = Array(result[:tables] || [])
      result[:tables] = (@inherited_tables + explicit_tables).uniq
    end

    # Inherit auth if not explicitly specified
    result[:auth] ||= @inherited_auth if @inherited_auth

    result
  end
end

# Helper class for member/collection blocks
class MemberCollectionBuilder
  def initialize(gateway, prefix, inherited_tables, inherited_auth, type)
    @gateway = gateway
    @prefix = prefix
    @inherited_tables = inherited_tables
    @inherited_auth = inherited_auth
    @type = type  # :member or :collection (for documentation purposes)
  end

  [:get, :post, :put, :delete, :patch].each do |method|
    define_method(method) do |path, options = {}|
      full_path = "#{@prefix}#{path}"

      # Merge inherited options
      options = merge_inherited_options(options)

      @gateway.send(:add_route, method, full_path, options)
    end
  end

  private

  def merge_inherited_options(options)
    result = options.dup

    # Merge tables
    if @inherited_tables.any?
      explicit_tables = Array(result[:tables] || [])
      result[:tables] = (@inherited_tables + explicit_tables).uniq
    end

    # Inherit auth
    result[:auth] ||= @inherited_auth if @inherited_auth

    result
  end
end

class ApiGateway
  attr_reader :name, :routes, :default_auth, :default_lambda, :default_cors, :default_tables, :lambdas

  def initialize(name, options = {})
    @name = name.to_s
    @routes = []
    @default_auth = options[:auth] || :cognito
    @default_lambda = options[:lambda] || name
    @default_cors = options.fetch(:cors, true)
    @default_tables = Array(options[:tables] || [])  # Namespace-level tables
    @lambdas = Set.new([name.to_sym])  # Track all unique lambdas used
    @current_lambda_context = nil  # For lambda blocks
  end

  def to_h
    {
      name: @name,
      routes: @routes.map(&:to_h),
      default_auth: @default_auth.to_s,
      default_lambda: @default_lambda.to_s,
      default_cors: @default_cors,
      lambdas: all_lambdas.map(&:to_s),
      tables: all_tables
    }
  end

  def to_json(*args)
    require 'json'
    to_h.to_json(*args)
  end

  # Lambda block - group routes under a specific lambda
  # Example: lambda :special_processor do ... end
  def lambda(name, &block)
    previous_context = @current_lambda_context
    @current_lambda_context = name.to_sym
    @lambdas.add(@current_lambda_context)
    instance_eval(&block) if block_given?
    @current_lambda_context = previous_context
  end

  # Define a simple HTTP method route
  [:get, :post, :put, :delete, :patch].each do |method|
    define_method(method) do |path, options = {}|
      add_route(method, path, options)
    end
  end

  # RESTful resources (plural) - generates index, create, show, update, destroy
  # Example: resources :pickups
  # Generates:
  #   GET    /pickups        (index)
  #   POST   /pickups        (create)
  #   GET    /pickups/:id    (show)
  #   PUT    /pickups/:id    (update)
  #   DELETE /pickups/:id    (destroy)
  #
  # Tables are inferred from the resource name if not explicitly provided.
  # Example: resources :pickups will automatically use tables: [:pickups]
  #
  # Table Inheritance:
  # Tables specified on a resources block are inherited by all nested routes and resources.
  # Nested routes can add additional tables which are merged with inherited ones.
  # Example:
  #   resources :customers, tables: [:customers, :containers] do
  #     get '/inventory', tables: [:inventory], on: :member  # Gets [:customers, :containers, :inventory]
  #     resources :items, tables: [:pickups]                  # Gets [:customers, :containers, :pickups]
  #   end
  def resources(name, options = {}, &block)
    resource_name = name.to_s
    singular = singularize(resource_name)
    param_name = options[:param] || "#{singular}_id"

    # Auto-infer tables if not explicitly provided
    options = auto_infer_tables(resource_name, options)

    # Mark these as plural resource routes
    resource_options = options.merge(route_type: :resources)

    actions = determine_actions(options)

    add_route(:get, "/#{resource_name}", resource_options) if actions.include?(:index)
    add_route(:post, "/#{resource_name}", resource_options) if actions.include?(:create)
    add_route(:get, "/#{resource_name}/{#{param_name}}", resource_options) if actions.include?(:show)
    add_route(:put, "/#{resource_name}/{#{param_name}}", resource_options) if actions.include?(:update)
    add_route(:delete, "/#{resource_name}/{#{param_name}}", resource_options) if actions.include?(:destroy)

    # Handle nested resources if block given
    if block_given?
      collection_prefix = "/#{resource_name}"
      member_prefix = "/#{resource_name}/{#{param_name}}"
      # Pass tables and auth from parent to nested builder for inheritance
      # Include both namespace-level tables and resource-level tables
      resource_tables = Array(options[:tables] || [])
      inherited_tables = (@default_tables + resource_tables).uniq
      # Auth: use resource-level auth if specified, otherwise namespace default
      inherited_auth = options[:auth] || @default_auth
      nested_builder = NestedResourceBuilder.new(self, member_prefix, collection_prefix,
                                                  inherited_tables: inherited_tables,
                                                  inherited_auth: inherited_auth)
      nested_builder.instance_eval(&block)
    end
  end

  # Singular resource - generates show, update, destroy (no index/create, no :id)
  # Example: resource :profile
  # Generates:
  #   GET    /profile    (show)
  #   PUT    /profile    (update)
  #   DELETE /profile    (destroy)
  #
  # Tables are NOT auto-inferred for singular resources since they often map to different tables
  # (e.g., resource :profile uses tables: [:customers]).
  # You must explicitly specify tables for singular resources.
  def resource(name, options = {})
    resource_name = name.to_s
    actions = determine_actions(options, default: [:show, :update, :destroy])

    # Mark these as resource routes (not plain actions)
    resource_options = options.merge(route_type: :resource)

    add_route(:get, "/#{resource_name}", resource_options) if actions.include?(:show)
    add_route(:put, "/#{resource_name}", resource_options) if actions.include?(:update)
    add_route(:delete, "/#{resource_name}", resource_options) if actions.include?(:destroy)
    add_route(:post, "/#{resource_name}", resource_options) if actions.include?(:create)
  end

  # Get all unique lambdas used by this API Gateway
  def all_lambdas
    @lambdas.to_a.sort
  end

  # Get all tables referenced by routes (including inferred ones)
  def all_tables
    tables = Set.new
    @routes.each do |route|
      tables.merge(route.tables) unless route.tables.empty?
    end
    tables.to_a.sort
  end

  private

  def add_route(method, path, options = {})
    # Determine which lambda to use (priority: option > current context > default)
    lambda_to_use = options[:lambda] || @current_lambda_context || @default_lambda

    # Merge namespace-level tables with route-specific tables
    route_tables = Array(options[:tables] || [])
    merged_tables = (@default_tables + route_tables).uniq

    # Parse 'to' option if present (Rails-style: 'controller#action')
    controller = options[:controller]
    action = options[:action]
    if options[:to]
      parsed = parse_to_option(options[:to])
      controller ||= parsed[:controller]
      action ||= parsed[:action]
    end

    route_options = {
      auth: options[:auth] || @default_auth,
      lambda: lambda_to_use,
      cors: options.fetch(:cors, @default_cors),
      tables: merged_tables,
      route_type: options[:route_type] || :action,
      controller: controller,  # Pass through explicit controller
      action: action,          # Pass through explicit action
      request_model: options[:request_model],
      response_model: options[:response_model],
      response_context: options[:response_context]
    }

    # Track this lambda
    @lambdas.add(lambda_to_use.to_sym)

    @routes << Route.new(method, path, route_options)
  end

  # Parse Rails-style 'to' option: 'controller#action'
  # Examples:
  #   'available_slots#index' => { controller: 'available_slots', action: 'index' }
  #   'customers/items#show' => { controller: 'customers/items', action: 'show' }
  #
  # @param to_string [String] The 'to' option value
  # @return [Hash] Hash with :controller and :action keys
  def parse_to_option(to_string)
    return {} unless to_string.is_a?(String)

    parts = to_string.split('#')
    return {} if parts.length != 2

    { controller: parts[0], action: parts[1] }
  end

  # Auto-infer tables from resource name if not explicitly provided
  # This allows: resources :pickups instead of resources :pickups, tables: [:pickups]
  def auto_infer_tables(resource_name, options)
    # If tables are already specified, don't infer
    return options if options.key?(:tables)

    # Infer table name from resource name (assumes plural resource name matches table name)
    # e.g., resources :pickups → tables: [:pickups]
    # e.g., resources :inventory → tables: [:inventory]
    inferred_table = resource_name.to_sym

    # Return options with inferred tables
    options.merge(tables: [inferred_table])
  end

  def determine_actions(options, default: [:index, :create, :show, :update, :destroy])
    if options[:only]
      # Ensure :only is always an array
      return Array(options[:only])
    end

    if options[:except]
      # Ensure :except is always an array
      return default - Array(options[:except])
    end

    default
  end

  # Simple singularize - handles most common cases
  def singularize(word)
    if word.end_with?('ies')
      word[0..-4] + 'y'
    elsif word.end_with?('xes')
      word[0..-3]
    elsif word.end_with?('zes')
      word[0..-3]
    elsif word.end_with?('ches')
      word[0..-3]
    elsif word.end_with?('shes')
      word[0..-3]
    elsif word.end_with?('ses')
      word[0..-3]
    elsif word.end_with?('s') && !word.end_with?('ss')
      word[0..-2]
    else
      word
    end
  end
end

class RouteDSL
  DSL_VERSION = "1.0.0"

  attr_reader :api_gateways

  def initialize
    @api_gateways = []
  end

  def to_h
    {
      schema_version: "1.0",
      api_gateways: @api_gateways.map(&:to_h),
      generated_at: Time.now.utc.iso8601
    }
  end

  def to_json(*args)
    require 'json'
    to_h.to_json(*args)
  end

  def api_gateway(name, options = {}, &block)
    gateway = ApiGateway.new(name, options)
    gateway.instance_eval(&block) if block_given?
    @api_gateways << gateway
  end

  def self.load_from_file(filename)
    dsl = new
    dsl.instance_eval(File.read(filename), filename)
    dsl
  end
end
