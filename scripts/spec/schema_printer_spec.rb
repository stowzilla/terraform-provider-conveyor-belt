# frozen_string_literal: true

require 'rspec'
require_relative '../lib/schema_dsl'
require_relative '../lib/terra_dispatch'
require_relative '../lib/schema_printer'

RSpec.describe SchemaPrinter do
  describe '.print' do
    it 'produces valid DSL syntax for a request model with required fields' do
      schema_hash = {
        request_models: {
          signup: {
            name: 'signup',
            properties: {
              'email' => { type: 'string' },
              'name' => { type: 'string' },
              'terms_accepted' => { type: 'boolean' }
            },
            required: %w[email name]
          }
        },
        response_models: {}
      }

      output = SchemaPrinter.print(schema_hash)

      expect(output).to include('TerraDispatch.schema.define do')
      expect(output).to include('request :signup do')
      expect(output).to include('string :email, required: true')
      expect(output).to include('string :name, required: true')
      expect(output).to include('boolean :terms_accepted')
      expect(output).not_to include('boolean :terms_accepted, required: true')
      expect(output).to include('end')
    end

    it 'produces valid DSL syntax for a response model with contexts' do
      schema_hash = {
        request_models: {},
        response_models: {
          item: {
            name: 'item',
            contexts: {
              ops: {
                name: 'ops',
                properties: {
                  'id' => { type: 'string' },
                  'price' => { type: 'number' }
                }
              },
              customer: {
                name: 'customer',
                properties: {
                  'id' => { type: 'string' }
                }
              }
            }
          }
        }
      }

      output = SchemaPrinter.print(schema_hash)

      expect(output).to include('model :item do')
      expect(output).to include('context :ops do')
      expect(output).to include('string :id')
      expect(output).to include('number :price')
      expect(output).to include('context :customer do')
    end

    it 'reverse-maps object type back to map DSL type' do
      schema_hash = {
        request_models: {
          test: {
            name: 'test',
            properties: { 'data' => { type: 'object' } },
            required: []
          }
        },
        response_models: {}
      }

      output = SchemaPrinter.print(schema_hash)
      expect(output).to include('map :data')
      expect(output).not_to include('object :data')
    end

    it 'handles all six supported types' do
      schema_hash = {
        request_models: {
          all_types: {
            name: 'all_types',
            properties: {
              'a' => { type: 'string' },
              'b' => { type: 'number' },
              'c' => { type: 'integer' },
              'd' => { type: 'boolean' },
              'e' => { type: 'array' },
              'f' => { type: 'object' }
            },
            required: []
          }
        },
        response_models: {}
      }

      output = SchemaPrinter.print(schema_hash)

      expect(output).to include('string :a')
      expect(output).to include('number :b')
      expect(output).to include('integer :c')
      expect(output).to include('boolean :d')
      expect(output).to include('array :e')
      expect(output).to include('map :f')
    end

    it 'produces output that can be re-evaluated by the DSL' do
      schema_hash = {
        request_models: {
          signup: {
            name: 'signup',
            properties: {
              'email' => { type: 'string' },
              'active' => { type: 'boolean' }
            },
            required: ['email']
          }
        },
        response_models: {
          item: {
            name: 'item',
            contexts: {
              ops: {
                name: 'ops',
                properties: {
                  'id' => { type: 'string' },
                  'price' => { type: 'number' }
                }
              }
            }
          }
        }
      }

      printed = SchemaPrinter.print(schema_hash)

      # Reset the memoized schema builder for a clean re-parse
      TerraDispatch.instance_variable_set(:@schema_builder, nil)
      eval(printed)
      reparsed = TerraDispatch.schema.to_h

      expect(reparsed[:request_models][:signup][:name]).to eq('signup')
      expect(reparsed[:request_models][:signup][:properties]).to eq(schema_hash[:request_models][:signup][:properties])
      expect(reparsed[:request_models][:signup][:required]).to eq(schema_hash[:request_models][:signup][:required])

      expect(reparsed[:response_models][:item][:name]).to eq('item')
      expect(reparsed[:response_models][:item][:contexts][:ops][:properties]).to eq(
        schema_hash[:response_models][:item][:contexts][:ops][:properties]
      )

      # Clean up
      TerraDispatch.instance_variable_set(:@schema_builder, nil)
    end

    it 'handles empty schema' do
      schema_hash = { request_models: {}, response_models: {} }
      output = SchemaPrinter.print(schema_hash)

      expect(output).to eq("TerraDispatch.schema.define do\nend")
    end

    it 'handles string keys gracefully' do
      schema_hash = {
        'request_models' => {
          test: {
            'name' => 'test',
            'properties' => { 'email' => { 'type' => 'string' } },
            'required' => ['email']
          }
        },
        'response_models' => {}
      }

      output = SchemaPrinter.print(schema_hash)
      expect(output).to include('string :email, required: true')
    end
  end
end
