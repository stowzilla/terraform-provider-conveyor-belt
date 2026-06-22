#!/usr/bin/env ruby
# frozen_string_literal: true

require_relative 'route_dsl'

# Infers DynamoDB table names from API routes
# Reads from a Terraform file containing aws_dynamodb_table resources to discover available tables
class TableInference
  attr_reader :available_tables

  def initialize(dynamodb_tables_file)
    unless dynamodb_tables_file && File.exist?(dynamodb_tables_file)
      @available_tables = []
      return
    end
    @available_tables = parse_available_tables(dynamodb_tables_file)
  end

  # Infer table names from a route's path
  # Returns array of table names (empty if no inference possible)
  def infer_tables_from_route(route)
    # Extract the resource name from the path
    # e.g., /profile → profile, /inventory → inventory, /pickups/{id} → pickups
    path_segments = route.path.split('/').reject(&:empty?)
    return [] if path_segments.empty?

    # Get the first non-parameter segment as the resource name
    resource_segment = path_segments.find { |seg| !seg.start_with?('{') }
    return [] unless resource_segment

    # Try to find a matching table
    inferred = find_matching_table(resource_segment)
    inferred ? [inferred] : []
  end

  private

  def parse_available_tables(file_path)
    return [] unless File.exist?(file_path)

    content = File.read(file_path)
    tables = []

    # Extract table names from: resource "aws_dynamodb_table" "name" {
    content.scan(/resource\s+"aws_dynamodb_table"\s+"(\w+)"\s+\{/) do |match|
      tables << match[0]
    end

    tables
  end

  def find_matching_table(resource_name)
    # Try exact match first
    return resource_name if @available_tables.include?(resource_name)

    # Try plural form (add 's')
    plural = pluralize(resource_name)
    return plural if @available_tables.include?(plural)

    # Try singular form (remove 's')
    singular = singularize(resource_name)
    return singular if @available_tables.include?(singular)

    # Special cases
    case resource_name
    when 'profile'
      'customers' if @available_tables.include?('customers')
    when 'pickup', 'pickups'
      if @available_tables.include?('scheduled_pickups')
        'scheduled_pickups'
      elsif @available_tables.include?('pickup')
        'pickup'
      end
    else
      nil
    end
  end

  def pluralize(word)
    # Simple pluralization rules
    if word.end_with?('y')
      "#{word[0..-2]}ies"
    elsif word.end_with?('s', 'x', 'z', 'ch', 'sh')
      "#{word}es"
    else
      "#{word}s"
    end
  end

  def singularize(word)
    # Simple singularization rules
    if word.end_with?('ies')
      "#{word[0..-4]}y"
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
    elsif word.end_with?('s')
      word[0..-2]
    else
      word
    end
  end
end
