require 'json'
require 'lib/database'
require 'helpers/response_helper'

def lambda_handler(event:, context:)
  user_id = event.dig('pathParameters', 'id')
  
  return ResponseHelper.error_response('User ID is required', 400) unless user_id
  
  begin
    user = Database.find_user(user_id)
    ResponseHelper.success_response(user.to_hash)
  rescue => e
    ResponseHelper.error_response("Failed to fetch user: #{e.message}", 500)
  end
end