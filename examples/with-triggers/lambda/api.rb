# api.rb - API Lambda handler
#
# Handles API Gateway requests for order management.
# Publishes events to SNS and sends messages to SQS.

require 'json'
require 'aws-sdk-sns'
require 'aws-sdk-sqs'
require 'aws-sdk-dynamodb'

def handler(event:, context:)
  path = event['path']
  method = event['httpMethod']
  
  case [method, path]
  when ['GET', '/api/health']
    health_check
  when ['GET', '/api/orders']
    list_orders
  when ['POST', '/api/orders']
    create_order(JSON.parse(event['body'] || '{}'))
  when ['GET', %r{/api/orders/(.+)}]
    get_order($1)
  else
    { statusCode: 404, body: JSON.generate({ error: 'Not found' }) }
  end
end

def health_check
  { statusCode: 200, body: JSON.generate({ status: 'healthy' }) }
end

def list_orders
  dynamodb = Aws::DynamoDB::Client.new
  result = dynamodb.scan(table_name: ENV['ORDERS_TABLE'] || 'orders')
  
  { statusCode: 200, body: JSON.generate({ orders: result.items }) }
end

def create_order(params)
  order_id = SecureRandom.uuid
  order = { id: order_id, **params, created_at: Time.now.iso8601 }
  
  # Save to DynamoDB
  dynamodb = Aws::DynamoDB::Client.new
  dynamodb.put_item(table_name: ENV['ORDERS_TABLE'] || 'orders', item: order)
  
  # Publish event to SNS for order_processor
  if ENV['SNS_TOPIC_ARN']
    sns = Aws::SNS::Client.new
    sns.publish(
      topic_arn: ENV['SNS_TOPIC_ARN'],
      message: JSON.generate({ event: 'order_created', order: order })
    )
  end
  
  # Send message to SQS for background_worker
  if ENV['SQS_QUEUE_URL']
    sqs = Aws::SQS::Client.new
    sqs.send_message(
      queue_url: ENV['SQS_QUEUE_URL'],
      message_body: JSON.generate({ task: 'process_order', order_id: order_id })
    )
  end
  
  { statusCode: 201, body: JSON.generate({ order: order }) }
end

def get_order(id)
  dynamodb = Aws::DynamoDB::Client.new
  result = dynamodb.get_item(
    table_name: ENV['ORDERS_TABLE'] || 'orders',
    key: { 'id' => id }
  )
  
  if result.item
    { statusCode: 200, body: JSON.generate({ order: result.item }) }
  else
    { statusCode: 404, body: JSON.generate({ error: 'Order not found' }) }
  end
end
