# order_processor.rb - SNS-triggered Lambda handler
#
# This Lambda is invoked when messages are published to the SNS topic.
# It processes order events asynchronously.

require 'json'
require 'aws-sdk-dynamodb'

def handler(event:, context:)
  # SNS events contain records with the message
  event['Records'].each do |record|
    process_sns_record(record)
  end
  
  { statusCode: 200, body: JSON.generate({ processed: event['Records'].length }) }
end

def process_sns_record(record)
  # Extract the SNS message
  sns_message = record['Sns']
  message_body = JSON.parse(sns_message['Message'])
  
  puts "Processing SNS message: #{message_body['event']}"
  
  case message_body['event']
  when 'order_created'
    process_new_order(message_body['order'])
  else
    puts "Unknown event type: #{message_body['event']}"
  end
end

def process_new_order(order)
  puts "Processing new order: #{order['id']}"
  
  # Update order status in DynamoDB
  dynamodb = Aws::DynamoDB::Client.new
  dynamodb.update_item(
    table_name: ENV['ORDERS_TABLE'] || 'orders',
    key: { 'id' => order['id'] },
    update_expression: 'SET #status = :status, processed_at = :processed_at',
    expression_attribute_names: { '#status' => 'status' },
    expression_attribute_values: {
      ':status' => 'processed',
      ':processed_at' => Time.now.iso8601
    }
  )
  
  puts "Order #{order['id']} marked as processed"
end
