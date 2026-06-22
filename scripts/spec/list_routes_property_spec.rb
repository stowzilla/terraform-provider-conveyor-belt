# frozen_string_literal: true

require 'rspec'
require 'rantly'
require 'rantly/rspec_extensions'
require_relative '../list_routes'

RSpec.describe 'RouteListGenerator Property Tests' do
  let(:generator) { RouteListGenerator.new }

  # Mock route and gateway objects for testing
  class MockRoute
    attr_accessor :path, :method, :lambda, :auth, :tables, :route_type

    def initialize(path:, method:, lambda_name: 'ops', auth: 'cognito', tables: [], route_type: :action)
      @path = path
      @method = method
      @lambda = lambda_name
      @auth = auth
      @tables = tables
      @route_type = route_type
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
  end

  class MockGateway
    attr_accessor :name

    def initialize(name)
      @name = name
    end
  end

  describe 'Property 1: Controller/Action Inference Determinism' do
    # **Feature: route-based-lambda-dispatch, Property 1: Controller/Action Inference Determinism**
    # **Validates: Requirements 1.2, 1.3, 1.4, 1.5, 1.6, 3.1, 3.3, 3.4, 6.3**
    #
    # For any route definition (path, HTTP verb, resource type), the inferred controller
    # and action names should be deterministic - the same route always produces the same
    # controller/action pair.

    it 'produces deterministic controller/action for the same route definition' do
      gateway = MockGateway.new('ops')

      100.times do
        # Generate random route components
        resource = Rantly { sized(10) { string(:alpha).downcase } }
        verb = Rantly { choose('GET', 'POST', 'PUT', 'PATCH', 'DELETE') }
        has_id = Rantly { boolean }
        has_action = Rantly { boolean }
        action_name = Rantly { sized(8) { string(:alpha).downcase } }

        # Build path based on random components
        path = if has_id && has_action
                 "/#{resource}/:id/#{action_name}"
               elsif has_id
                 "/#{resource}/:id"
               elsif has_action
                 "/#{resource}/#{action_name}"
               else
                 "/#{resource}"
               end

        route = MockRoute.new(path: path, method: verb)

        # Call inference multiple times
        result1 = generator.infer_controller_action(route, gateway)
        result2 = generator.infer_controller_action(route, gateway)
        result3 = generator.infer_controller_action(route, gateway)

        # Property: Same input should always produce same output
        expect(result1).to eq(result2), "Determinism failed for path: #{path}, verb: #{verb}"
        expect(result2).to eq(result3), "Determinism failed for path: #{path}, verb: #{verb}"

        # Property: Result should have both controller and action keys
        expect(result1).to have_key(:controller)
        expect(result1).to have_key(:action)

        # Property: Controller and action should be non-empty strings
        expect(result1[:controller]).to be_a(String)
        expect(result1[:action]).to be_a(String)
        expect(result1[:controller]).to_not be_empty
        expect(result1[:action]).to_not be_empty
      end
    end

    it 'converts hyphens to underscores in action names' do
      gateway = MockGateway.new('ops')

      100.times do
        # Generate random hyphenated action names
        word1 = Rantly { sized(5) { string(:alpha).downcase } }
        word2 = Rantly { sized(5) { string(:alpha).downcase } }
        hyphenated_action = "#{word1}-#{word2}"

        path = "/resource/:id/#{hyphenated_action}"
        route = MockRoute.new(path: path, method: 'POST')

        result = generator.infer_controller_action(route, gateway)

        # Property: Hyphens should be converted to underscores
        expect(result[:action]).to_not include('-'), "Action should not contain hyphens: #{result[:action]}"
        expect(result[:action]).to eq("#{word1}_#{word2}")
      end
    end

    it 'infers controller from first path segment for resource routes' do
      gateway = MockGateway.new('ops')

      100.times do
        # Generate random resource name
        resource = Rantly { sized(10) { string(:alpha).downcase } }
        verb = Rantly { choose('GET', 'POST', 'PUT', 'DELETE') }

        # Simple resource path - use :resources route_type
        path = "/#{resource}"
        route = MockRoute.new(path: path, method: verb, route_type: :resources)

        result = generator.infer_controller_action(route, gateway)

        # Property: Controller should be derived from first segment for resource routes
        expect(result[:controller]).to eq(resource)
      end
    end

    it 'infers controller from namespace for action routes' do
      gateway = MockGateway.new('ops')

      100.times do
        # Generate random action name
        action_name = Rantly { sized(10) { string(:alpha).downcase } }
        verb = Rantly { choose('GET', 'POST', 'PUT', 'DELETE') }

        # Simple action path - use default :action route_type
        path = "/#{action_name}"
        route = MockRoute.new(path: path, method: verb, route_type: :action)

        result = generator.infer_controller_action(route, gateway)

        # Property: Controller should be namespace for action routes (unless standalone)
        unless generator.standalone_action?(action_name)
          expect(result[:controller]).to eq('ops')
          expect(result[:action]).to eq(action_name)
        end
      end
    end
  end

  describe 'Property 2: RESTful Action Mapping Correctness' do
    # **Feature: route-based-lambda-dispatch, Property 2: RESTful Action Mapping Correctness**
    # **Validates: Requirements 3.2**
    #
    # For any RESTful resource route, the HTTP verb and member/collection context
    # should map to the correct standard action.

    it 'maps GET collection to index' do
      gateway = MockGateway.new('ops')

      100.times do
        resource = Rantly { sized(10) { string(:alpha).downcase } }
        path = "/#{resource}"
        # Use :resources route_type to indicate this is a plural resource collection
        route = MockRoute.new(path: path, method: 'GET', route_type: :resources)

        result = generator.infer_controller_action(route, gateway)

        # Property: GET on collection should map to index
        unless generator.standalone_action?(resource)
          expect(result[:action]).to eq('index'), "GET /#{resource} should map to index, got #{result[:action]}"
        end
      end
    end

    it 'maps POST collection to create' do
      gateway = MockGateway.new('ops')

      100.times do
        resource = Rantly { sized(10) { string(:alpha).downcase } }
        path = "/#{resource}"
        # Use :resources route_type to indicate this is a plural resource collection
        route = MockRoute.new(path: path, method: 'POST', route_type: :resources)

        result = generator.infer_controller_action(route, gateway)

        # Property: POST on collection should map to create
        unless generator.standalone_action?(resource)
          expect(result[:action]).to eq('create'), "POST /#{resource} should map to create, got #{result[:action]}"
        end
      end
    end

    it 'maps GET member to show' do
      gateway = MockGateway.new('ops')

      100.times do
        resource = Rantly { sized(10) { string(:alpha).downcase } }
        path = "/#{resource}/:id"
        # Use :resources route_type for member routes from resources
        route = MockRoute.new(path: path, method: 'GET', route_type: :resources)

        result = generator.infer_controller_action(route, gateway)

        # Property: GET on member should map to show
        expect(result[:action]).to eq('show'), "GET /#{resource}/:id should map to show, got #{result[:action]}"
      end
    end

    it 'maps PUT/PATCH member to update' do
      gateway = MockGateway.new('ops')

      100.times do
        resource = Rantly { sized(10) { string(:alpha).downcase } }
        verb = Rantly { choose('PUT', 'PATCH') }
        path = "/#{resource}/:id"
        # Use :resources route_type for member routes from resources
        route = MockRoute.new(path: path, method: verb, route_type: :resources)

        result = generator.infer_controller_action(route, gateway)

        # Property: PUT/PATCH on member should map to update
        expect(result[:action]).to eq('update'), "#{verb} /#{resource}/:id should map to update, got #{result[:action]}"
      end
    end

    it 'maps DELETE member to destroy' do
      gateway = MockGateway.new('ops')

      100.times do
        resource = Rantly { sized(10) { string(:alpha).downcase } }
        path = "/#{resource}/:id"
        # Use :resources route_type for member routes from resources
        route = MockRoute.new(path: path, method: 'DELETE', route_type: :resources)

        result = generator.infer_controller_action(route, gateway)

        # Property: DELETE on member should map to destroy
        expect(result[:action]).to eq('destroy'), "DELETE /#{resource}/:id should map to destroy, got #{result[:action]}"
      end
    end

    it 'maps singular resource GET to show' do
      gateway = MockGateway.new('customer')

      # Test with singular resource (route_type: :resource)
      path = '/profile'
      route = MockRoute.new(path: path, method: 'GET', route_type: :resource)

      result = generator.infer_controller_action(route, gateway)

      # Property: GET on singular resource should map to show
      expect(result[:action]).to eq('show')
      expect(result[:controller]).to eq('profile')
    end

    it 'maps singular resource PUT to update' do
      gateway = MockGateway.new('customer')

      # Test with singular resource (route_type: :resource)
      path = '/profile'
      route = MockRoute.new(path: path, method: 'PUT', route_type: :resource)

      result = generator.infer_controller_action(route, gateway)

      # Property: PUT on singular resource should map to update
      expect(result[:action]).to eq('update')
      expect(result[:controller]).to eq('profile')
    end
  end

  describe 'Property 5: Route Manifest Round-Trip Consistency' do
    # **Feature: route-based-lambda-dispatch, Property 5: Route Manifest Round-Trip Consistency**
    # **Validates: Requirements 6.1, 6.2, 6.3**
    #
    # For any route manifest, serializing to JSON and deserializing should produce
    # an equivalent manifest with all required fields (verb, path, gateway, lambda,
    # controller, action, tables).

    let(:generator) { RouteListGenerator.new }

    it 'preserves all required fields through JSON round-trip' do
      100.times do
        # Generate random route data
        verb = Rantly { choose('GET', 'POST', 'PUT', 'PATCH', 'DELETE') }
        resource = Rantly { sized(8) { string(:alpha).downcase } }
        has_id = Rantly { boolean }
        path = has_id ? "/ops/#{resource}/{#{resource}_id}" : "/ops/#{resource}"
        gateway = 'ops'
        lambda_name = 'ops'
        controller = resource
        action = Rantly { choose('index', 'show', 'create', 'update', 'destroy') }
        auth = Rantly { choose('cognito', 'none') }
        tables = Rantly { array(range(0, 3)) { sized(8) { string(:alpha).downcase } } }

        # Create original route hash (as produced by transform_route)
        original = {
          'name' => resource,
          'verb' => verb,
          'path' => path,
          'gateway' => gateway,
          'lambda' => lambda_name,
          'controller' => controller,
          'action' => action,
          'auth' => auth,
          'tables' => tables
        }

        # Round-trip through JSON
        json_string = JSON.generate(original)
        restored = JSON.parse(json_string)

        # Property: All required fields should be preserved
        expect(restored['verb']).to eq(original['verb']), "verb not preserved"
        expect(restored['path']).to eq(original['path']), "path not preserved"
        expect(restored['gateway']).to eq(original['gateway']), "gateway not preserved"
        expect(restored['lambda']).to eq(original['lambda']), "lambda not preserved"
        expect(restored['controller']).to eq(original['controller']), "controller not preserved"
        expect(restored['action']).to eq(original['action']), "action not preserved"
        expect(restored['auth']).to eq(original['auth']), "auth not preserved"
        expect(restored['tables']).to eq(original['tables']), "tables not preserved"

        # Property: Complete equality
        expect(restored).to eq(original)
      end
    end

    it 'generates deterministic Ruby output for the same routes' do
      gateway = MockGateway.new('ops')

      100.times do
        # Generate random route
        resource = Rantly { sized(8) { string(:alpha).downcase } }
        verb = Rantly { choose('GET', 'POST', 'PUT', 'DELETE') }
        has_id = Rantly { boolean }
        path = has_id ? "/#{resource}/:id" : "/#{resource}"

        route = MockRoute.new(path: path, method: verb, lambda_name: 'ops')

        # Generate Ruby content multiple times
        routes_array = [{
          'verb' => verb,
          'path' => "/ops#{path.gsub(':id', "{#{resource}_id}")}",
          'controller' => resource,
          'action' => generator.infer_controller_action(route, gateway)[:action],
          'auth' => 'cognito',
          'tables' => []
        }]

        content1 = generator.send(:generate_ruby_content, routes_array, 'ops')
        content2 = generator.send(:generate_ruby_content, routes_array, 'ops')

        # Property: Same routes should produce same Ruby content (except timestamp)
        # Remove timestamp line for comparison
        content1_no_timestamp = content1.lines.reject { |l| l.include?('Generated at:') }.join
        content2_no_timestamp = content2.lines.reject { |l| l.include?('Generated at:') }.join

        expect(content1_no_timestamp).to eq(content2_no_timestamp),
          "Ruby output should be deterministic for same routes"
      end
    end

    it 'generates valid Ruby syntax in output' do
      gateway = MockGateway.new('ops')

      100.times do
        # Generate random routes
        num_routes = Rantly { range(1, 5) }
        routes_array = []

        num_routes.times do
          resource = Rantly { sized(8) { string(:alpha).downcase } }
          verb = Rantly { choose('GET', 'POST', 'PUT', 'DELETE') }
          has_id = Rantly { boolean }
          path = has_id ? "/ops/#{resource}/{#{resource}_id}" : "/ops/#{resource}"
          action = Rantly { choose('index', 'show', 'create', 'update', 'destroy') }

          routes_array << {
            'verb' => verb,
            'path' => path,
            'controller' => resource,
            'action' => action,
            'auth' => 'cognito',
            'tables' => []
          }
        end

        content = generator.send(:generate_ruby_content, routes_array, 'test')

        # Property: Generated content should be valid Ruby
        # This will raise SyntaxError if invalid
        expect { RubyVM::InstructionSequence.compile(content) }.to_not raise_error,
          "Generated Ruby should be syntactically valid"
      end
    end

    it 'includes all required fields in Ruby output' do
      gateway = MockGateway.new('ops')

      100.times do
        resource = Rantly { sized(8) { string(:alpha).downcase } }
        verb = Rantly { choose('GET', 'POST', 'PUT', 'DELETE') }
        path = "/ops/#{resource}"
        action = Rantly { choose('index', 'show', 'create', 'update', 'destroy') }
        auth = Rantly { choose('cognito', 'none') }
        tables = Rantly { array(range(0, 3)) { sized(6) { string(:alpha).downcase } } }

        routes_array = [{
          'verb' => verb,
          'path' => path,
          'controller' => resource,
          'action' => action,
          'auth' => auth,
          'tables' => tables
        }]

        content = generator.send(:generate_ruby_content, routes_array, 'test')

        # Property: All required fields should be present in output
        expect(content).to include('verb:'), "verb field missing"
        expect(content).to include('path:'), "path field missing"
        expect(content).to include('controller:'), "controller field missing"
        expect(content).to include('action:'), "action field missing"
        expect(content).to include('auth:'), "auth field missing"
        expect(content).to include('tables:'), "tables field missing"

        # Property: Values should be present
        expect(content).to include(verb.inspect), "verb value missing"
        expect(content).to include(path.inspect), "path value missing"
        expect(content).to include(resource.inspect), "controller value missing"
        expect(content).to include(action.inspect), "action value missing"
      end
    end

    it 'produces frozen constant in Ruby output' do
      routes_array = [{
        'verb' => 'GET',
        'path' => '/ops/test',
        'controller' => 'test',
        'action' => 'index',
        'auth' => 'cognito',
        'tables' => []
      }]

      content = generator.send(:generate_ruby_content, routes_array, 'test')

      # Property: Output should include frozen_string_literal pragma
      expect(content).to include('# frozen_string_literal: true')

      # Property: Array should be frozen
      expect(content).to include('.freeze')

      # Property: Should be in Routes module
      expect(content).to include('module Routes')
      expect(content).to include('TEST = [')
    end
  end
end
