require 'json'

def lambda_handler(event:, context:)
  {
    statusCode: 200,
    headers: {
      'Content-Type' => 'application/json',
      'Access-Control-Allow-Origin' => '*'
    },
    body: JSON.generate({
      status: 'healthy',
      timestamp: Time.now.iso8601
    })
  }
end