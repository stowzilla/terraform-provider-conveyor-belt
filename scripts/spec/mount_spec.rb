# frozen_string_literal: true

require 'rspec'
require_relative '../lib/terra_dispatch'

module MockGem
  module Routes
    def self.routes
      [
        { method: :get, path: '/status' },
        { method: :post, path: '/reindex', options: { tables: [:indexes] } },
        { method: :post, path: '/reindex/:owner_id', options: { tables: [:indexes] } }
      ]
    end
  end

  module Dashboard
    class Application
      def routes
        [
          { method: :get, path: '/' },
          { method: :post, path: '/rebuild' }
        ]
      end
    end

    def self.app
      Application.new
    end
  end
end

RSpec.describe 'mount DSL' do
  it 'mounts routes with an at: prefix' do
    dsl = TerraDispatch.routes.draw do
      namespace :ops, auth: :cognito do
        mount MockGem::Routes, at: 'search'
      end
    end

    gateway = dsl.api_gateways.first
    paths = gateway.routes.map(&:path)

    expect(paths).to include('/search/status')
    expect(paths).to include('/search/reindex')
    expect(paths).to include('/search/reindex/{owner_id}')
  end

  it 'mounts routes without a prefix' do
    dsl = TerraDispatch.routes.draw do
      namespace :ops, auth: :cognito do
        mount MockGem::Routes
      end
    end

    gateway = dsl.api_gateways.first
    paths = gateway.routes.map(&:path)

    expect(paths).to include('/status')
    expect(paths).to include('/reindex')
  end

  it 'merges tables from mount options' do
    dsl = TerraDispatch.routes.draw do
      namespace :ops, auth: :cognito do
        mount MockGem::Routes, at: 'search', tables: [:versions]
      end
    end

    gateway = dsl.api_gateways.first
    reindex_route = gateway.routes.find { |r| r.path == '/search/reindex' }

    expect(reindex_route.tables).to include(:versions)
    expect(reindex_route.tables).to include(:indexes)
  end

  it 'applies auth override to all mounted routes' do
    dsl = TerraDispatch.routes.draw do
      namespace :ops, auth: :cognito do
        mount MockGem::Routes, at: 'search', auth: :none
      end
    end

    gateway = dsl.api_gateways.first
    gateway.routes.each do |route|
      expect(route.auth).to eq(:none)
    end
  end

  it 'inherits namespace-level defaults when no override' do
    dsl = TerraDispatch.routes.draw do
      namespace :ops, auth: :cognito, tables: [:audit_logs] do
        mount MockGem::Routes, at: 'search'
      end
    end

    gateway = dsl.api_gateways.first
    status_route = gateway.routes.find { |r| r.path == '/search/status' }

    expect(status_route.tables).to include(:audit_logs)
    expect(status_route.auth).to eq(:cognito)
  end

  it 'inherits scope tables and auth' do
    dsl = TerraDispatch.routes.draw do
      namespace :ops, auth: :cognito do
        scope tables: [:shared_table], auth: :none do
          mount MockGem::Routes, at: 'search'
        end
      end
    end

    gateway = dsl.api_gateways.first
    status_route = gateway.routes.find { |r| r.path == '/search/status' }

    expect(status_route.tables).to include(:shared_table)
    expect(status_route.auth).to eq(:none)
  end

  it 'supports .app pattern (instance with #routes)' do
    dsl = TerraDispatch.routes.draw do
      namespace :ops, auth: :cognito do
        mount MockGem::Dashboard.app, at: 's3arch'
      end
    end

    gateway = dsl.api_gateways.first
    paths = gateway.routes.map(&:path)

    expect(paths).to include('/s3arch/')
    expect(paths).to include('/s3arch/rebuild')
  end
end
