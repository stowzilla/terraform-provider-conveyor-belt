# Orders Lambda Handler
# Handles order-related API requests

def handler(event:, context:)
  http_method = event['httpMethod']
  path = event['path']
  
  case http_method
  when 'GET'
    list_orders(event)
  when 'POST'
    create_order(event)
  else
    {
      statusCode: 405,
      body: JSON.generate({ error: 'Method not allowed' })
    }
  end
end

def list_orders(event)
  {
    statusCode: 200,
    headers: {
      'Content-Type' => 'application/json',
      'Access-Control-Allow-Origin' => '*'
    },
    body: JSON.generate({
      orders: [
        { id: '1', status: 'pending', total: 99.99 },
        { id: '2', status: 'completed', total: 149.99 }
      ]
    })
  }
end

def create_order(event)
  body = JSON.parse(event['body'] || '{}')
  
  {
    statusCode: 201,
    headers: {
      'Content-Type' => 'application/json',
      'Access-Control-Allow-Origin' => '*'
    },
    body: JSON.generate({
      id: SecureRandom.uuid,
      status: 'pending',
      items: body['items'] || []
    })
  }
end
