#!/usr/bin/env ruby
# frozen_string_literal: true

require_relative 'route_dsl'
require_relative 'schema_dsl'

# Rails-like routing DSL wrapper
# Provides TerraDispatch.routes.draw do ... end syntax
module TerraDispatch
  DSL_VERSION = "1.0.0"

  class Routes
    attr_reader :dsl

    def initialize
      @dsl = RouteDSL.new
      @namespace_stack = []
      @options_stack = []
    end

    def draw(&block)
      instance_eval(&block) if block_given?
      @dsl
    end

    # Namespace for grouping routes (maps to API Gateway)
    # Example: namespace :customer, auth: :cognito do ... end
    def namespace(name, options = {}, &block)
      # Push namespace context
      @namespace_stack.push(name)
      @options_stack.push(options)

      # Create API Gateway for this namespace
      gateway = ApiGateway.new(name, options)

      # Evaluate block in context that can add routes to this gateway
      RouteBuilder.new(gateway, self).instance_eval(&block) if block_given?

      # Add gateway to DSL
      @dsl.api_gateways << gateway

      # Pop namespace context
      @namespace_stack.pop
      @options_stack.pop
    end

    def to_h
      @dsl.to_h
    end

    def to_json(*args)
      require 'json'
      to_h.to_json(*args)
    end
  end

  # Builder class for adding routes within a namespace
  class RouteBuilder
    def initialize(gateway, parent)
      @gateway = gateway
      @parent = parent
      @scope_prefix = ''
      @scope_module = nil
      @scope_auth = nil      # Inherited auth from scope
      @scope_tables = []     # Inherited tables from scope
    end

    # Scope for grouping routes with a path prefix and inherited options
    # Example: scope module: :terms, auth: :none, tables: [:terms] do ... end
    def scope(options = {}, &block)
      previous_prefix = @scope_prefix
      previous_module = @scope_module
      previous_auth = @scope_auth
      previous_tables = @scope_tables

      @scope_prefix = options[:path] || @scope_prefix
      @scope_module = options[:module] || @scope_module
      @scope_auth = options[:auth] || @scope_auth
      # Merge tables: scope tables add to existing inherited tables
      @scope_tables = (@scope_tables + Array(options[:tables] || [])).uniq

      instance_eval(&block) if block_given?

      @scope_prefix = previous_prefix
      @scope_module = previous_module
      @scope_auth = previous_auth
      @scope_tables = previous_tables
    end

    # HTTP method helpers
    [:get, :post, :put, :delete, :patch].each do |method|
      define_method(method) do |path, options = {}|
        full_path = build_path(path)
        # Apply inherited options, with explicit options taking precedence
        route_options = options.dup
        route_options[:lambda] ||= @scope_module if @scope_module
        route_options[:auth] ||= @scope_auth if @scope_auth
        # Merge scope tables with route tables
        route_options[:tables] = (@scope_tables + Array(route_options[:tables] || [])).uniq if @scope_tables.any? || route_options[:tables]
        @gateway.send(method, full_path, route_options)
      end
    end

    # RESTful resources
    def resources(name, options = {}, &block)
      # Apply inherited scope options
      options = apply_scope_options(options)
      @gateway.resources(name, options, &block)
    end

    # Singular resource
    def resource(name, options = {})
      # Apply inherited scope options
      options = apply_scope_options(options)
      @gateway.resource(name, options)
    end

    # Lambda context block
    def lambda(name, &block)
      @gateway.lambda(name, &block)
    end

    # Mount a gem's route manifest into the current namespace
    # The mountable must respond to .routes and return an array of hashes:
    #   [{ method: :get, path: '/reindex', tables: [:versions] }, ...]
    #
    # Options:
    #   at:     - path prefix for all mounted routes (e.g., 'search')
    #   tables: - additional tables merged into all mounted routes
    #   auth:   - override auth for all mounted routes
    #
    # Example:
    #   mount S3arch::Routes, at: 'search', tables: [:s3arch_versions]
    def mount(mountable, options = {})
      prefix = options[:at]&.to_s&.gsub(%r{^/|/$}, '') || ''
      extra_tables = Array(options[:tables] || [])
      auth_override = options[:auth]

      route_definitions = mountable.respond_to?(:routes) ? mountable.routes : []

      route_definitions.each do |route_def|
        method = route_def[:method].to_sym
        path = normalize_mount_path(route_def[:path].to_s)

        # Build full path with mount prefix
        full_path = prefix.empty? ? path : "/#{prefix}#{path}"
        full_path = full_path.chomp('/') unless full_path == '/'
        full_path = build_path(full_path)

        route_options = (route_def[:options] || {}).dup
        route_options[:tables] = (extra_tables + Array(route_options[:tables] || [])).uniq
        route_options[:auth] = auth_override if auth_override
        route_options[:auth] ||= @scope_auth if @scope_auth
        route_options[:tables] = (@scope_tables + route_options[:tables]).uniq if @scope_tables.any?

        # Set controller to mount prefix so route inference doesn't fall back to namespace
        route_options[:controller] ||= prefix.gsub('-', '_') unless prefix.empty?
        # Infer action from mount path: '/' -> 'index', '/rebuild' -> 'rebuild'
        route_options[:action] ||= mount_action_from_path(path)

        @gateway.send(method, full_path, route_options)
      end
    end

    private

    def build_path(path)
      if @scope_prefix.empty?
        path
      else
        "/#{@scope_prefix}#{path}"
      end
    end

    # Convert Rails-style :param to API Gateway-style {param}
    def normalize_mount_path(path)
      path.gsub(/:([a-zA-Z_]\w*)/) { "{#{$1}}" }
    end

    # Infer action name from a mounted route's path
    # '/' -> 'index', '/rebuild' -> 'rebuild', '/foo-bar' -> 'foo_bar'
    def mount_action_from_path(path)
      stripped = path.gsub(%r{^/|/$}, '')
      stripped.empty? ? 'index' : stripped.gsub('-', '_')
    end

    # Apply scope-level options to route options
    def apply_scope_options(options)
      result = options.dup
      result[:auth] ||= @scope_auth if @scope_auth
      result[:lambda] ||= @scope_module if @scope_module
      # Merge scope tables with resource tables
      result[:tables] = (@scope_tables + Array(result[:tables] || [])).uniq if @scope_tables.any? || result[:tables]
      result
    end
  end

  def self.schema
    @schema_builder ||= SchemaBuilder.new
  end

  def self.routes
    Routes.new
  end
end
