# frozen_string_literal: true

require 'rspec'
require 'rantly'
require 'rantly/rspec_extensions'
require_relative '../lib/name_conversion'

RSpec.describe 'Name Conversion Property Tests' do
  # **Feature: api-schema-system, Property 2: Name Conversion Round-Trip**
  # **Validates: Requirements 7.1, 7.2, 7.3, 7.4**

  # Generate a valid snake_case string: lowercase letters and underscores,
  # no leading/trailing/consecutive underscores, at least 1 char.
  def random_snake_case
    # 1-4 segments of lowercase letters joined by single underscores
    num_segments = rand(1..4)
    segments = num_segments.times.map do
      len = rand(1..6)
      len.times.map { ('a'..'z').to_a.sample }.join
    end
    segments.join('_')
  end

  describe 'Property 2: Name Conversion Round-Trip' do
    it 'snake_to_camel → camel_to_snake produces the original snake_case string' do
      100.times do |iteration|
        original = random_snake_case

        camel = snake_to_camel(original)
        round_tripped = camel_to_snake(camel)

        expect(round_tripped).to eq(original),
          "Round-trip failed on iteration #{iteration}.\n" \
          "Original:      #{original.inspect}\n" \
          "camelCase:     #{camel.inspect}\n" \
          "Round-tripped: #{round_tripped.inspect}"
      end
    end

    it 'single-word names pass through unchanged' do
      100.times do |iteration|
        word = (rand(1..8)).times.map { ('a'..'z').to_a.sample }.join

        expect(snake_to_camel(word)).to eq(word),
          "Single word #{word.inspect} should pass through snake_to_camel unchanged (iteration #{iteration})"
        expect(camel_to_snake(word)).to eq(word),
          "Single word #{word.inspect} should pass through camel_to_snake unchanged (iteration #{iteration})"
      end
    end

    it 'removes underscores and capitalizes following letters' do
      100.times do
        original = random_snake_case
        camel = snake_to_camel(original)

        # Property: no underscores in camelCase output (unless original had none)
        if original.include?('_')
          expect(camel).not_to include('_'),
            "camelCase #{camel.inspect} should not contain underscores (from #{original.inspect})"
        end

        # Property: first character is always lowercase
        expect(camel[0]).to eq(camel[0].downcase),
          "camelCase #{camel.inspect} should start lowercase"
      end
    end

    it 'handles known examples correctly' do
      expect(snake_to_camel('zip_code')).to eq('zipCode')
      expect(snake_to_camel('customer_id')).to eq('customerId')
      expect(snake_to_camel('pickup_date')).to eq('pickupDate')
      expect(snake_to_camel('email')).to eq('email')
      expect(snake_to_camel('name')).to eq('name')

      expect(camel_to_snake('zipCode')).to eq('zip_code')
      expect(camel_to_snake('customerId')).to eq('customer_id')
      expect(camel_to_snake('pickupDate')).to eq('pickup_date')
      expect(camel_to_snake('email')).to eq('email')
      expect(camel_to_snake('name')).to eq('name')
    end
  end
end
