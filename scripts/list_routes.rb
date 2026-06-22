#!/usr/bin/env ruby
# frozen_string_literal: true

# List Routes - Output route definitions as JSON for Terraform provider consumption
#
# Usage:
#   ./scripts/list_routes.rb                    # Full JSON output
#   ./scripts/list_routes.rb -c                 # Concise output
#   ./scripts/list_routes.rb -g availability    # Filter routes matching pattern
#   ./scripts/list_routes.rb -c -g ops          # Concise + filtered
#   ./scripts/list_routes.rb --schema path/to/schema.tf.rb  # Explicit schema file
#   ./scripts/list_routes.rb --tables-file path/to/dynamodb.tf  # DynamoDB table inference
#
# Output format (JSON):
# {
#   "routes": [
#     {
#       "name": "signup",
#       "verb": "POST",
#       "path": "/onboarding/signup",
#       "gateway": "onboarding",
#       "lambda": "onboarding",
#       "auth": "none",
#       "tables": [],
#       "request_model": "signup",
#       "response_model": ""
#     },
#     ...
#   ],
#   "models": [
#     {
#       "name": "signup",
#       "description": "Request model: signup",
#       "properties": { "email": { "type": "string" } },
#       "required": ["email"]
#     },
#     ...
#   ]
# }
#
# Models are auto-loaded from schema.tf.rb in the same directory as routes.tf.rb.
#
# Concise format:
#   POST /onboarding/signup onboarding:onboarding

require 'json'
require 'optparse'
require 'fileutils'
require_relative 'lib/route_dsl'
require_relative 'lib/terra_dispatch'
require_relative 'lib/table_inference'

class RouteListGenerator
  attr_reader :table_inference, :options

  def initialize(options = {})
    @options = options
  end

  # Infer controller and action from route definition following Rails conventions
  #
  # @param route [Route] The route object from the DSL
  # @param gateway [Gateway] The gateway/namespace the route belongs to
  # @return [Hash] Hash with :controller and :action keys
  def infer_controller_action(route, gateway)
    # If both controller and action are explicitly set, use them
    if route.controller && route.action
      return { controller: route.controller, action: route.action }
    end

    # If only controller is explicitly set, infer action
    if route.controller
      action = route.action || infer_action_from_route(route, gateway)
      return { controller: route.controller, action: action }
    end

    # If only action is explicitly set, infer controller
    if route.action
      controller = infer_controller_from_route(route, gateway)
      return { controller: controller, action: route.action }
    end

    path = route.path
    verb = route.method.upcase

    # Remove leading slash and split into segments
    segments = path.split('/').reject(&:empty?)

    # Filter out path parameters (segments starting with ':' or '{')
    non_param_segments = segments.reject { |s| s.start_with?(':', '{') }

    return { controller: 'root', action: 'index' } if non_param_segments.empty?

    # Determine the controller namespace - use lambda if different from gateway (scope module)
    controller_namespace = route.lambda.to_s != gateway.name.to_s ? route.lambda.to_s : gateway.name.to_s

    # Single segment routes like /signup, /profile, /items
    if non_param_segments.length == 1 && segments.length == 1
      segment = non_param_segments.first

      # Use route_type from DSL to determine if this is a resource or action
      if route.singular_resource?
        # Singular resource: /profile -> profile#show
        return { controller: segment.gsub('-', '_'), action: infer_singular_resource_action(verb) }
      elsif route.plural_resource?
        # Plural resource collection: /items -> items#index (GET) or items#create (POST)
        return { controller: segment.gsub('-', '_'), action: infer_restful_action(verb, false) }
      else
        # Action route: /signup -> namespace#signup, /me -> operations#me
        controller = standalone_action?(segment) ? 'operations' : controller_namespace
        return { controller: controller, action: segment.gsub('-', '_') }
      end
    end

    # Check for nested resources pattern: /parents/:id/children
    # e.g., /customers/:id/items -> customers/items#index
    if nested_resource?(segments, route)
      return infer_nested_resource(segments, verb)
    end

    # Check for member action pattern: /resource/:id/action
    # e.g., /containers/:id/contents -> containers#contents
    if member_action?(segments, route)
      return infer_member_action(segments, verb)
    end

    # Determine if this is a member route (has path parameter)
    has_id_param = segments.any? { |s| s.start_with?(':', '{') }
    last_segment_is_param = segments.last&.start_with?(':', '{')

    if non_param_segments.length == 1
      # Simple resource route: /containers or /containers/:id
      controller = non_param_segments.first.gsub('-', '_')
      action = infer_restful_action(verb, has_id_param && last_segment_is_param)
    else
      # Complex route: /billing/overview or /movement-queue/status
      controller = non_param_segments.first.gsub('-', '_')
      action = non_param_segments.last.gsub('-', '_')
    end

    { controller: controller, action: action }
  end

  # Infer action from route when controller is explicitly set
  #
  # @param route [Route] The route object from the DSL
  # @param gateway [Gateway] The gateway/namespace the route belongs to
  # @return [String] Action name
  def infer_action_from_route(route, gateway)
    path = route.path
    verb = route.method.upcase

    # Remove leading slash and split into segments
    segments = path.split('/').reject(&:empty?)

    # Filter out path parameters
    non_param_segments = segments.reject { |s| s.start_with?(':', '{') }

    # If single segment, check if it's a resource route
    if non_param_segments.length == 1
      segment = non_param_segments.first
      # For resource routes, use RESTful action inference
      if route.resource? || route.singular_resource? || route.plural_resource?
        has_id_param = segments.any? { |s| s.start_with?(':', '{') }
        last_segment_is_param = segments.last&.start_with?(':', '{')
        return infer_restful_action(verb, has_id_param && last_segment_is_param)
      end
      # Otherwise use segment name as action
      return segment.gsub('-', '_')
    end

    # For nested resources (e.g., /items/:id/suggestions), check if it's a resource route
    # Pattern: parent, :id, child where child is the resource
    if segments.length >= 3 && segments[1]&.start_with?(':', '{')
      child_segment = non_param_segments.last
      # If this is a resource route, use RESTful action inference
      if route.resource? || route.plural_resource?
        # Check if there's an ID parameter after the child resource
        has_child_id = segments.length > 3 && segments[3]&.start_with?(':', '{')
        return infer_restful_action(verb, has_child_id)
      end
    end

    # Otherwise use last non-param segment as action name
    non_param_segments.last.gsub('-', '_')
  end

  # Infer controller from route when action is explicitly set
  #
  # @param route [Route] The route object from the DSL
  # @param gateway [Gateway] The gateway/namespace the route belongs to
  # @return [String] Controller name
  def infer_controller_from_route(route, gateway)
    path = route.path

    # Remove leading slash and split into segments
    segments = path.split('/').reject(&:empty?)

    # Filter out path parameters
    non_param_segments = segments.reject { |s| s.start_with?(':', '{') }

    # Use first non-param segment as controller
    return gateway.name.to_s if non_param_segments.empty?

    non_param_segments.first.gsub('-', '_')
  end

  # Check if segment is a standalone action that maps to operations controller
  # These are special single-word endpoints that don't follow namespace patterns
  #
  # @param segment [String] Path segment
  # @return [Boolean] True if standalone action
  def standalone_action?(segment)
    # Known standalone actions that map to operations controller
    # These are actions that don't belong to the namespace controller
    standalone_actions = %w[me unsubscribe]
    standalone_actions.include?(segment)
  end

  # Infer action for singular resource based on HTTP verb
  #
  # @param verb [String] HTTP verb
  # @return [String] Action name
  def infer_singular_resource_action(verb)
    case verb
    when 'GET' then 'show'
    when 'PUT', 'PATCH' then 'update'
    when 'DELETE' then 'destroy'
    when 'POST' then 'create'
    else 'show'
    end
  end

  # Check if route is a member action pattern: /resource/:id/action
  # This is different from nested resources - it's an action on a single resource
  # Uses route_type from DSL to distinguish actions from nested resources
  #
  # @param segments [Array<String>] Path segments
  # @param route [Route] The route object (used to check route_type)
  # @return [Boolean] True if member action pattern
  def member_action?(segments, route)
    return false if segments.length < 3

    # Pattern: resource, :id, action (where action is not a resource name)
    # e.g., /containers/:id/contents, /waitlist/:id/invite
    first_is_resource = !segments[0].start_with?(':', '{')
    second_is_param = segments[1]&.start_with?(':', '{')
    third_is_action = segments[2] && !segments[2].start_with?(':', '{')

    return false unless first_is_resource && second_is_param && third_is_action

    # Use route_type from DSL: action routes are member actions, resource routes are nested resources
    route.action?
  end

  # Infer controller and action for member action pattern
  # e.g., /containers/:id/contents -> containers#contents
  # e.g., /containers/:id/qr-code/print -> containers#print_qr_code
  #
  # @param segments [Array<String>] Path segments
  # @param verb [String] HTTP verb
  # @return [Hash] Hash with :controller and :action keys
  def infer_member_action(segments, verb)
    controller = segments[0].gsub('-', '_')

    # Get all non-param segments after the first param
    action_segments = segments[2..-1].reject { |s| s.start_with?(':', '{') }

    # Join action segments with underscore and convert hyphens
    action = action_segments.map { |s| s.gsub('-', '_') }.join('_')

    { controller: controller, action: action }
  end

  # Check if route follows nested resource pattern
  # Pattern: resource/:param/resource (where the third segment is a resource, not an action)
  # Uses route_type from DSL to distinguish resources from actions
  #
  # @param segments [Array<String>] Path segments
  # @param route [Route] The route object (used to check route_type)
  # @return [Boolean] True if nested resource pattern
  def nested_resource?(segments, route)
    return false if segments.length < 3

    # Check if we have: non-param, param, non-param pattern
    first_is_resource = !segments[0].start_with?(':', '{')
    second_is_param = segments[1]&.start_with?(':', '{')
    third_is_resource = segments[2] && !segments[2].start_with?(':', '{')

    return false unless first_is_resource && second_is_param && third_is_resource

    # Use route_type from DSL: resource routes are nested resources, action routes are member actions
    route.resource?
  end

  # Infer controller and action for nested resources
  # e.g., /customers/:id/items -> controller: "customers/items", action: "index"
  #
  # @param segments [Array<String>] Path segments
  # @param verb [String] HTTP verb
  # @return [Hash] Hash with :controller and :action keys
  def infer_nested_resource(segments, verb)
    parent = segments[0]
    child = segments[2]

    # Check if there's a child ID parameter
    has_child_id = segments.length > 3 && segments[3]&.start_with?(':', '{')

    # Check for custom action after child resource
    # e.g., /customers/:id/items/:id/move
    if segments.length > 4 && !segments[4]&.start_with?(':', '{')
      action = segments[4].gsub('-', '_')
    elsif segments.length == 4 && !segments[3]&.start_with?(':', '{')
      # e.g., /customers/:id/items/search (collection action on nested resource)
      action = segments[3].gsub('-', '_')
    else
      action = infer_restful_action(verb, has_child_id)
    end

    { controller: "#{parent}/#{child}", action: action }
  end

  # Infer RESTful action from HTTP verb and member/collection context
  #
  # @param verb [String] HTTP verb (GET, POST, PUT, PATCH, DELETE)
  # @param is_member [Boolean] True if route targets a specific resource (has ID param at end)
  # @return [String] Action name
  def infer_restful_action(verb, is_member)
    case [verb, is_member]
    when ['GET', false] then 'index'
    when ['GET', true] then 'show'
    when ['POST', false] then 'create'
    when ['PUT', true], ['PATCH', true] then 'update'
    when ['DELETE', true] then 'destroy'
    else 'index'
    end
  end

  def generate(routes_file)
    unless File.exist?(routes_file)
      STDERR.puts "Error: Routes file not found: #{routes_file}"
      exit 1
    end

    begin
      # Resolve DynamoDB tables file
      dynamodb_tables_file = if options[:tables_file]
                               File.expand_path(options[:tables_file])
                             else
                               nil
                             end
      @table_inference = TableInference.new(dynamodb_tables_file)

      # Load the DSL
      dsl = load_routes_file(routes_file)

      # Transform to the desired format
      routes_array = []

      dsl.api_gateways.each do |gateway|
        gateway.routes.each do |route|
          routes_array << transform_route(route, gateway)
        end
      end

      # Sort routes by specificity (matches ActionRouter's sorting logic)
      # More specific routes (literal segments) should come before parameterized routes
      # This ensures /employees/me matches before /employees/{employee_id}
      routes_array.sort_by! { |r| route_specificity_for_sorting(r["path"], r["verb"]) }

      # Apply grep filter if specified
      if options[:grep]
        pattern = Regexp.new(options[:grep], Regexp::IGNORECASE)
        routes_array = routes_array.select do |r|
          r["path"].match?(pattern) ||
          r["gateway"].match?(pattern) ||
          r["lambda"].match?(pattern) ||
          r["verb"].match?(pattern)
        end
      end

      # Output in requested format
      if options[:ruby_output]
        output_ruby(routes_array, options[:ruby_output])
      elsif options[:concise]
        output_concise(routes_array)
      else
        output = { "routes" => routes_array }

        # Load schema models if schema file exists
        # Resolve relative to the routes file being parsed, or use explicit --schema path
        schema_file = options[:schema_file]
        unless schema_file
          routes_dir = File.dirname(File.expand_path(routes_file))
          schema_file = File.join(routes_dir, 'schema.tf.rb')
        end
        if schema_file && File.exist?(schema_file)
          models = load_schema_models(schema_file)
          output["models"] = models if models.any?
        end

        puts JSON.pretty_generate(output)
      end
    rescue NoMethodError => e
      STDERR.puts "Error: #{e.message}"
      STDERR.puts "This may indicate a version mismatch between your routes.tf.rb and the DSL library."
      STDERR.puts "Current DSL version: #{TerraDispatch::DSL_VERSION rescue 'unknown'}"
      STDERR.puts "Update the conveyor-belt provider to get the latest DSL."
      exit 1
    end
  end

  private

  # Load schema models from schema.tf.rb and convert to Go-provider-compatible format
  def load_schema_models(schema_file)
    # Reset schema builder to ensure clean state
    TerraDispatch.instance_variable_set(:@schema_builder, nil)

    begin
      load schema_file
    rescue => e
      STDERR.puts "Warning: Failed to load schema file #{schema_file}: #{e.message}"
      return []
    end

    schema = TerraDispatch.schema.to_h
    models = []

    # Convert request models to ModelDefinition format
    (schema[:request_models] || {}).each do |_name, model|
      models << {
        "name" => model[:name],
        "kind" => "request",
        "description" => "Request model: #{model[:name]}",
        "properties" => stringify_properties(model[:properties] || {}),
        "required" => (model[:required] || []).map(&:to_s)
      }
    end

    # Convert response models (each context becomes a separate model)
    (schema[:response_models] || {}).each do |_name, model|
      (model[:contexts] || {}).each do |ctx_name, ctx|
        model_name = "#{model[:name]}_#{ctx_name}_response"
        models << {
          "name" => model_name,
          "kind" => "response",
          "description" => "Response model: #{model[:name]} (#{ctx_name} context)",
          "properties" => stringify_properties(ctx[:properties] || {}),
          "required" => []
        }
      end
    end

    models
  end

  # Convert symbol keys to strings for JSON output
  def stringify_properties(properties)
    properties.each_with_object({}) do |(key, value), hash|
      hash[key.to_s] = value.transform_keys(&:to_s)
    end
  end

  # Calculate route specificity for sorting (matches ActionRouter logic)
  # More specific routes (literal segments) should match before parameterized routes
  #
  # Sorting order:
  # 1. Routes with fewer parameters (more literal segments)
  # 2. Routes with more total segments (deeper paths)
  # 3. Alphabetically by path
  # 4. By HTTP verb (for same path, order: GET, POST, PUT, PATCH, DELETE)
  #
  # @param path [String] Route path
  # @param verb [String] HTTP verb
  # @return [Array] Sortable specificity tuple
  def route_specificity_for_sorting(path, verb)
    segments = path.split('/').reject(&:empty?)
    param_count = segments.count { |s| s.start_with?('{') }
    segment_count = segments.length

    # Verb priority for same path (GET before POST before PUT, etc.)
    verb_priority = { 'GET' => 0, 'POST' => 1, 'PUT' => 2, 'PATCH' => 3, 'DELETE' => 4 }
    verb_order = verb_priority[verb] || 99

    # Sort by: fewer params first, then more segments first, then alphabetically, then by verb
    [param_count, -segment_count, path, verb_order]
  end

  def output_ruby(routes, namespace)
    # Filter routes for the specified namespace/lambda
    filtered_routes = routes.select { |r| r["lambda"] == namespace }

    if filtered_routes.empty?
      STDERR.puts "No routes found for namespace '#{namespace}' - skipping file generation"
      return
    end

    # Generate the output file path
    output_dir = if options[:output_dir]
                   File.expand_path(options[:output_dir])
                 else
                   File.expand_path('../lambda/lib/routes', __dir__)
                 end
    output_file = File.join(output_dir, "#{namespace}_routes.rb")

    # Ensure the routes directory exists
    FileUtils.mkdir_p(output_dir)

    # Generate the Ruby content
    content = generate_ruby_content(filtered_routes, namespace)

    # Write to file
    File.write(output_file, content)
    puts "Generated: #{output_file}"
    puts "Routes: #{filtered_routes.length}"
  end

  def generate_ruby_content(routes, namespace)
    constant_name = namespace.upcase

    lines = []
    lines << "# frozen_string_literal: true"
    lines << ""
    lines << "# Auto-generated by: ruby scripts/list_routes.rb --ruby-output #{namespace}"
    lines << "# Do not edit manually - regenerate with `scripts/list_routes.rb --ruby-output #{namespace}'"
    lines << ""
    lines << "module Routes"
    lines << "  #{constant_name} = ["

    routes.each_with_index do |route, index|
      lines << "    {"
      lines << "      verb: #{route['verb'].inspect},"
      lines << "      path: #{route['path'].inspect},"
      lines << "      gateway: #{route['gateway'].inspect},"
      lines << "      lambda: #{route['lambda'].inspect},"
      lines << "      controller: #{route['controller'].inspect},"
      lines << "      action: #{route['action'].inspect},"
      lines << "      auth: #{route['auth'].inspect},"
      lines << "      tables: #{route['tables'].inspect}"
      lines << "    }#{index < routes.length - 1 ? ',' : ''}"
    end

    lines << "  ].freeze"
    lines << "end"
    lines << ""

    lines.join("\n")
  end

  def output_concise(routes)
    # Calculate column widths for alignment
    verb_width = [routes.map { |r| r["verb"].length }.max || 6, 6].max
    path_width = [routes.map { |r| r["path"].length }.max || 20, 4].max
    target_width = [routes.map { |r| "#{r["gateway"]}:#{r["lambda"]}".length }.max || 15, 6].max

    # Print header
    header_verb = "VERB".ljust(verb_width)
    header_path = "PATH".ljust(path_width)
    header_target = "GATEWAY:LAMBDA".ljust(target_width)
    header_action = "CONTROLLER#ACTION"
    puts "#{header_verb}\t#{header_path}\t#{header_target}\t#{header_action}"
    puts "-" * (verb_width + path_width + target_width + 30)  # Approximate separator

    routes.each do |route|
      verb = route["verb"].ljust(verb_width)
      path = route["path"].ljust(path_width)
      target = "#{route["gateway"]}:#{route["lambda"]}".ljust(target_width)
      controller_action = "#{route["controller"]}##{route["action"]}"
      puts "#{verb}\t#{path}\t#{target}\t#{controller_action}"
    end
  end

  def load_routes_file(filename)
    content = File.read(filename)
    if content.include?('TerraDispatch.routes.draw')
      # New TerraDispatch style
      eval(content, binding, filename)
    else
      # Old RouteDSL style
      RouteDSL.load_from_file(filename)
    end
  end

  def transform_route(route, gateway)
    # Infer controller and action from route definition
    inference = infer_controller_action(route, gateway)

    {
      "name" => extract_route_name(route.path),
      "verb" => route.method.upcase,
      "path" => normalize_path(route.path),
      "gateway" => gateway.name.to_s,
      "lambda" => route.lambda.to_s,
      "controller" => inference[:controller],
      "action" => inference[:action],
      "auth" => route.auth.to_s,
      "tables" => get_route_tables(route),
      "request_model" => route.request_model.to_s,
      "response_model" => route.response_model.to_s,
      "response_context" => route.response_context.to_s
    }.tap { |h| h.delete("response_context") if h["response_context"].empty? }
  end

  def extract_route_name(path)
    # Extract meaningful name from path
    # /signup → signup
    # /schedule-pickup → schedule_pickup
    # /pickups/{pickupId} → pickups_show (or just pickups for index)
    # /profile → profile

    segments = path.split('/').reject(&:empty?)
    return "root" if segments.empty?

    # Get non-parameter segments
    base_segments = segments.reject { |s| s.start_with?('{') }

    # Join with underscore and clean up
    base_segments.map { |s| s.gsub('-', '_') }.join('_')
  end

  # Simple singularize without active_support dependency.
  # Handles common English plurals used in route resource names.
  def simple_singularize(word)
    return word if word.empty?

    if word.end_with?('ies')
      word[0..-4] + 'y'
    elsif word.end_with?('xes')
      word[0..-3]
    elsif word.end_with?('zes')
      word[0..-3]
    elsif word.end_with?('ses')
      word[0..-3]
    elsif word.end_with?('s') && !word.end_with?('ss')
      word[0..-2]
    else
      word
    end
  end

  # Normalize path by converting Rails-style :param to API Gateway-style {param}
  # Does NOT add gateway prefix - paths are gateway-agnostic
  #
  # @param path [String] Route path (e.g., '/items/:id')
  # @return [String] Normalized path (e.g., '/items/{item_id}')
  def normalize_path(path)
    # Ensure path starts with /
    path = if path.start_with?('/')
      path
    else
      "/#{path}"
    end

    # Replace :param_name with {param_name}
    # Special case: :id gets converted to the resource name + _id
    # e.g., /items/:id -> /items/{item_id}, /customers/:id -> /customers/{customer_id}
    path = path.gsub(%r{/([a-zA-Z_][a-zA-Z0-9_]*?)/:id(/|$)}) do
      resource = $1
      trailing = $2
      singular_resource = simple_singularize(resource)
      param_name = "#{singular_resource}_id"
      "/#{resource}/{#{param_name}}#{trailing}"
    end

    # Handle other named parameters (keep as-is)
    path.gsub(/:([a-zA-Z_][a-zA-Z0-9_]*)/) { "{#{$1}}" }
  end

  def get_route_tables(route)
    # Use explicitly defined tables if present, otherwise infer
    if route.tables.any?
      route.tables
    else
      @table_inference.infer_tables_from_route(route)
    end
  end
end

# Main execution
if __FILE__ == $0
  options = {}
  routes_file = nil

  OptionParser.new do |opts|
    opts.banner = "Usage: #{$0} [options] [routes_file]"

    opts.on("-g", "--grep PATTERN", "Filter routes matching pattern (case-insensitive)") do |pattern|
      options[:grep] = pattern
    end

    opts.on("-c", "--concise", "Concise output: VERB PATH GATEWAY:LAMBDA") do
      options[:concise] = true
    end

    opts.on("--ruby-output NAMESPACE", "Generate Ruby route file for NAMESPACE (e.g., ops, customer)") do |namespace|
      options[:ruby_output] = namespace
    end

    opts.on("--output-dir DIR", "Output directory for generated files (default: ../lambda/lib/routes/ relative to script)") do |dir|
      options[:output_dir] = dir
    end

    opts.on("--schema FILE", "Path to schema.tf.rb for model definitions") do |file|
      options[:schema_file] = file
    end

    opts.on("--tables-file FILE", "Path to Terraform file with DynamoDB table definitions") do |file|
      options[:tables_file] = file
    end

    opts.on("-h", "--help", "Show this help") do
      puts opts
      exit
    end
  end.parse!

  routes_file = ARGV[0] || File.expand_path('../infrastructure/routes.tf.rb', __dir__)
  generator = RouteListGenerator.new(options)
  generator.generate(routes_file)
end
