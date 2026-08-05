# background_worker.rb - SQS-triggered Lambda handler
#
# This Lambda polls the SQS queue and processes messages in batches.
# It handles background tasks like order processing, notifications, etc.

require 'json'
require 'aws-sdk-dynamodb'

def handler(event:, context:)
  processed = 0
  failed = 0
  
  # SQS events contain records with messages
  event['Records'].each do |record|
    begin
      process_sqs_record(record)
      processed += 1
    rescue => e
      puts "Error processing message: #{e.message}"
      failed += 1
      # Re-raise to let Lambda retry the message
      raise e if failed == event['Records'].length
    end
  end
  
  {
    statusCode: 200,
    body: JSON.generate({ processed: processed, failed: failed })
  }
end

def process_sqs_record(record)
  message_body = JSON.parse(record['body'])
  
  puts "Processing SQS message: #{message_body['task']}"
  
  case message_body['task']
  when 'process_order'
    process_order_task(message_body['order_id'])
  when 'send_notification'
    send_notification_task(message_body)
  else
    puts "Unknown task type: #{message_body['task']}"
  end
end

def process_order_task(order_id)
  puts "Background processing for order: #{order_id}"
  
  # Fetch order from DynamoDB
  dynamodb = Aws::DynamoDB::Client.new
  result = dynamodb.get_item(
    table_name: ENV['ORDERS_TABLE'] || 'orders',
    key: { 'id' => order_id }
  )
  
  return unless result.item
  
  # Perform background processing (e.g., generate reports, sync to external systems)
  puts "Order #{order_id} background processing complete"
end

def send_notification_task(params)
  puts "Sending notification: #{params['type']}"
  # Notification logic here
end
