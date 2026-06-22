require 'json'
require 'lib/database'
require 'helpers/response_helper'

def lambda_handler(event:, context:)
  begin
    body = JSON.parse(event['body'] || '{}')
    
    user = User.new(
      id: SecureRandom.uuid,
      name: body['name'],
      email: body['email']
    )
    
    Database.save_user(user)
    ResponseHelper.success_response(user.to_hash)
  rescue JSON::ParserError
    ResponseHelper.error_response('Invalid JSON in request body', 400)
  rescue => e
    ResponseHelper.error_response("Failed to create user: #{e.message}", 500)
  end
end