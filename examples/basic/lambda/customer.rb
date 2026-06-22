# Customer Lambda Handler
# Handles customer-related API requests

def handler(event:, context:)
  http_method = event['httpMethod']
  path = event['path']
  
  case http_method
  when 'GET'
    get_customer(event)
  else
    {
      statusCode: 405,
      body: JSON.generate({ error: 'Method not allowed' })
    }
  end
end

def get_customer(event)
  # Example: Return customer data
  {
    statusCode: 200,
    headers: {
      'Content-Type' => 'application/json',
      'Access-Control-Allow-Origin' => '*'
    },
    body: JSON.generate({
      id: '123',
      name: 'John Doe',
      email: 'john@example.com'
    })
  }
end
