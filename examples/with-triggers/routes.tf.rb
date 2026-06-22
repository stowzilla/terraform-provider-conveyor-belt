# routes.tf.rb - API routes for triggers example
#
# This defines the API Gateway routes. The order_processor and
# background_worker Lambdas are standalone (no API routes) and
# are triggered by SNS/SQS instead.

namespace :api do
  # Order management endpoints
  resources :orders, only: [:index, :create, :show]
  
  # Health check
  get "/health"
end
