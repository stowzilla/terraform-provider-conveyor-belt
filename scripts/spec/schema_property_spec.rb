# frozen_string_literal: true

require 'rspec'
require 'rantly'
require 'rantly/rspec_extensions'
require_relative '../lib/schema_dsl'
require_relative '../lib/terra_dispatch'
require_relative '../lib/schema_printer'

RSpec.describe 'Schema Property Tests' do
  # **Feature: api-schema-system, Property 1: Schema Round-Trip**
  # **Validates: Requirements 1.3, 1.4, 1.5, 2.1, 2.2, 2.3, 2.4, 3.1, 3.2, 3.3, 3.4, 3.6, 4.3, 4.4, 4.5, 4.6, 5.1, 5.2, 5.3, 5.4, 5.5, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.8, 11.2**

  SUPPORTED_TYPES = %i[string number integer boolean array object map].freeze

  # Generate a valid Ruby identifier: lowercase letters and underscores,
  # no leading/trailing/consecutive underscores, at least 1 char.
  def random_field_name
    # Start with a letter, then mix letters and underscores, end with a letter
    len = rand(1..8)
    chars = []
    len.times do |i|
      if i == 0 || i == len - 1
        chars << ('a'..'z').to_a.sample
      elsif chars.last == '_'
        # Avoid consecutive underscores
        chars << ('a'..'z').to_a.sample
      else
        chars << (['_'] + ('a'..'z').to_a).sample
      end
    end
    chars.join
  end

  def random_type
    SUPPORTED_TYPES.sample
  end

  def build_random_schema
    num_request_models = rand(0..3)
    num_response_models = rand(0..3)

    request_dsl_lines = []
    response_dsl_lines = []

    expected_request_models = {}
    expected_response_models = {}

    num_request_models.times do |i|
      model_name = "req_#{random_field_name}"
      num_fields = rand(1..5)
      fields = []
      field_names_used = []

      num_fields.times do
        fname = random_field_name
        # Ensure unique field names within a model
        next if field_names_used.include?(fname)

        field_names_used << fname
        ftype = random_type
        freq = [true, false].sample
        fields << { name: fname, type: ftype, required: freq }
      end

      # Build DSL string for this request model
      lines = ["  request :#{model_name} do"]
      fields.each do |f|
        opts = f[:required] ? ', required: true' : ''
        lines << "    #{f[:type]} :#{f[:name]}#{opts}"
      end
      lines << '  end'
      request_dsl_lines << lines.join("\n")

      # Build expected hash
      properties = {}
      fields.each do |f|
        mapped_type = f[:type] == :map ? 'object' : f[:type].to_s
        properties[f[:name]] = { type: mapped_type }
      end
      required = fields.select { |f| f[:required] }.map { |f| f[:name] }

      expected_request_models[model_name.to_sym] = {
        name: model_name,
        properties: properties,
        required: required
      }
    end

    num_response_models.times do |i|
      model_name = "resp_#{random_field_name}"
      use_direct_fields = [true, false].sample

      if use_direct_fields
        # Direct-field response model (no contexts)
        num_fields = rand(1..5)
        fields = []
        field_names_used = []

        num_fields.times do
          fname = random_field_name
          next if field_names_used.include?(fname)

          field_names_used << fname
          ftype = random_type
          fields << { name: fname, type: ftype }
        end

        resp_lines = ["  model :#{model_name} do"]
        fields.each do |f|
          resp_lines << "    #{f[:type]} :#{f[:name]}"
        end
        resp_lines << '  end'
        response_dsl_lines << resp_lines.join("\n")

        direct_properties = {}
        fields.each do |f|
          mapped_type = f[:type] == :map ? 'object' : f[:type].to_s
          direct_properties[f[:name]] = { type: mapped_type }
        end

        expected_response_models[model_name.to_sym] = {
          name: model_name,
          contexts: {},
          properties: direct_properties
        }
      else
        # Context-wrapped response model
        num_contexts = rand(1..3)
        contexts_dsl = []
        expected_contexts = {}
        context_names_used = []

        num_contexts.times do
          ctx_name = "ctx_#{random_field_name}"
          next if context_names_used.include?(ctx_name)

          context_names_used << ctx_name
          num_fields = rand(1..4)
          fields = []
          field_names_used = []

          num_fields.times do
            fname = random_field_name
            next if field_names_used.include?(fname)

            field_names_used << fname
            ftype = random_type
            fields << { name: fname, type: ftype }
          end

          ctx_lines = ["    context :#{ctx_name} do"]
          fields.each do |f|
            ctx_lines << "      #{f[:type]} :#{f[:name]}"
          end
          ctx_lines << '    end'
          contexts_dsl << ctx_lines.join("\n")

          ctx_properties = {}
          fields.each do |f|
            mapped_type = f[:type] == :map ? 'object' : f[:type].to_s
            ctx_properties[f[:name]] = { type: mapped_type }
          end

          expected_contexts[ctx_name.to_sym] = {
            name: ctx_name,
            properties: ctx_properties
          }
        end

        resp_lines = ["  model :#{model_name} do"]
        resp_lines << contexts_dsl.join("\n")
        resp_lines << '  end'
        response_dsl_lines << resp_lines.join("\n")

        expected_response_models[model_name.to_sym] = {
          name: model_name,
          contexts: expected_contexts
        }
      end
    end

    dsl_string = "TerraDispatch.schema.define do\n"
    dsl_string += request_dsl_lines.join("\n") + "\n" unless request_dsl_lines.empty?
    dsl_string += response_dsl_lines.join("\n") + "\n" unless response_dsl_lines.empty?
    dsl_string += 'end'

    {
      dsl_string: dsl_string,
      expected: {
        request_models: expected_request_models,
        response_models: expected_response_models
      }
    }
  end

  describe 'Property 1: Schema Round-Trip' do
    it 'parse → to_h → print → re-parse → to_h produces equivalent data' do
      100.times do |iteration|
        # Step 1: Build a random schema programmatically
        schema_data = build_random_schema

        # Step 2: Reset TerraDispatch and eval the DSL string
        TerraDispatch.instance_variable_set(:@schema_builder, nil)
        eval(schema_data[:dsl_string]) # rubocop:disable Security/Eval

        # Step 3: Get the first hash via to_h
        first_hash = TerraDispatch.schema.to_h

        # Step 4: Print via SchemaPrinter
        printed = SchemaPrinter.print(first_hash)

        # Step 5: Reset and re-parse the printed output
        TerraDispatch.instance_variable_set(:@schema_builder, nil)
        eval(printed) # rubocop:disable Security/Eval

        # Step 6: Get the second hash via to_h
        second_hash = TerraDispatch.schema.to_h

        # Step 7: Assert equivalence
        # Compare request models
        expect(second_hash[:request_models].keys.sort).to eq(first_hash[:request_models].keys.sort),
          "Request model names differ on iteration #{iteration}.\n" \
          "First:  #{first_hash[:request_models].keys.sort}\n" \
          "Second: #{second_hash[:request_models].keys.sort}"

        first_hash[:request_models].each do |name, model|
          reparsed_model = second_hash[:request_models][name]
          expect(reparsed_model[:name]).to eq(model[:name]),
            "Request model '#{name}' name mismatch on iteration #{iteration}"
          expect(reparsed_model[:properties]).to eq(model[:properties]),
            "Request model '#{name}' properties mismatch on iteration #{iteration}.\n" \
            "First:  #{model[:properties]}\n" \
            "Second: #{reparsed_model[:properties]}"
          expect(reparsed_model[:required].sort).to eq(model[:required].sort),
            "Request model '#{name}' required fields mismatch on iteration #{iteration}.\n" \
            "First:  #{model[:required].sort}\n" \
            "Second: #{reparsed_model[:required].sort}"
        end

        # Compare response models
        expect(second_hash[:response_models].keys.sort).to eq(first_hash[:response_models].keys.sort),
          "Response model names differ on iteration #{iteration}.\n" \
          "First:  #{first_hash[:response_models].keys.sort}\n" \
          "Second: #{second_hash[:response_models].keys.sort}"

        first_hash[:response_models].each do |name, model|
          reparsed_model = second_hash[:response_models][name]
          expect(reparsed_model[:name]).to eq(model[:name]),
            "Response model '#{name}' name mismatch on iteration #{iteration}"

          # Compare direct properties (context-less models)
          if model[:properties]
            expect(reparsed_model[:properties]).to eq(model[:properties]),
              "Response model '#{name}' direct properties mismatch on iteration #{iteration}.\n" \
              "First:  #{model[:properties]}\n" \
              "Second: #{reparsed_model[:properties]}"
          end

          expect(reparsed_model[:contexts].keys.sort).to eq(model[:contexts].keys.sort),
            "Response model '#{name}' context names mismatch on iteration #{iteration}"

          model[:contexts].each do |ctx_name, ctx|
            reparsed_ctx = reparsed_model[:contexts][ctx_name]
            expect(reparsed_ctx[:name]).to eq(ctx[:name]),
              "Context '#{ctx_name}' name mismatch in model '#{name}' on iteration #{iteration}"
            expect(reparsed_ctx[:properties]).to eq(ctx[:properties]),
              "Context '#{ctx_name}' properties mismatch in model '#{name}' on iteration #{iteration}.\n" \
              "First:  #{ctx[:properties]}\n" \
              "Second: #{reparsed_ctx[:properties]}"
          end
        end

        # Cleanup
        TerraDispatch.instance_variable_set(:@schema_builder, nil)
      end
    end
  end
end
